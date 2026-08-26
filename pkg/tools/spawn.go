package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type SpawnTool struct {
	manager               *SubagentManager
	spawner               SubTurnSpawner
	defaultModel          string
	defaultModelFallbacks []string
	maxTokens             int
	temperature           float64
	allowlistCheck        func(targetAgentID string) bool
}

type asyncSubTurnContextPreparer interface {
	PrepareAsyncSubTurn(ctx context.Context) (context.Context, func(), error)
}

type trackedSpawnAdmissionObserverContextKey struct{}

// WithTrackedSpawnAdmissionObserver installs the agent-owned route admission
// callback that first-party SpawnTool invokes after asynchronous preparation
// succeeds but before the manager record/goroutine is created.
func WithTrackedSpawnAdmissionObserver(
	ctx context.Context,
	observer func() error,
) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, trackedSpawnAdmissionObserverContextKey{}, observer)
}

func notifyTrackedSpawnAdmission(ctx context.Context) (err error) {
	if ctx == nil {
		return nil
	}
	observer, _ := ctx.Value(trackedSpawnAdmissionObserverContextKey{}).(func() error)
	if observer == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			err = errors.New("tracked spawn admission observer panicked")
		}
	}()
	return observer()
}

// Compile-time check: SpawnTool implements AsyncExecutor.
var _ AsyncExecutor = (*SpawnTool)(nil)

func NewSpawnTool(manager *SubagentManager) *SpawnTool {
	if manager == nil {
		return &SpawnTool{}
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return &SpawnTool{
		manager:               manager,
		defaultModel:          manager.defaultModel,
		defaultModelFallbacks: cloneStringSlice(manager.defaultModelFallbacks),
		maxTokens:             manager.maxTokens,
		temperature:           manager.temperature,
	}
}

// SetSpawner sets the SubTurnSpawner for direct sub-turn execution.
func (t *SpawnTool) SetSpawner(spawner SubTurnSpawner) {
	t.spawner = spawner
}

func (t *SpawnTool) Name() string {
	return "spawn"
}

func (t *SpawnTool) Description() string {
	return "Spawn a subagent to handle a task in the background. Use this for complex or time-consuming tasks that can run independently. The subagent will complete the task and report back when done."
}

func (t *SpawnTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{
				"type":        "string",
				"description": "The task for subagent to complete",
			},
			"label": map[string]any{
				"type":        "string",
				"description": "Optional short label for the task (for display)",
			},
			"agent_id": map[string]any{
				"type":        "string",
				"description": "Optional target agent ID to delegate the task to",
			},
		},
		"required": []string{"task"},
	}
}

func (t *SpawnTool) SetAllowlistChecker(check func(targetAgentID string) bool) {
	t.allowlistCheck = check
}

func (t *SpawnTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	return t.execute(ctx, args, nil)
}

// ExecuteAsync implements AsyncExecutor. The callback is passed through to the
// subagent manager as a call parameter — never stored on the SpawnTool instance.
func (t *SpawnTool) ExecuteAsync(
	ctx context.Context,
	args map[string]any,
	cb AsyncCallback,
) *ToolResult {
	return t.execute(ctx, args, cb)
}

func (t *SpawnTool) execute(
	ctx context.Context,
	args map[string]any,
	cb AsyncCallback,
) *ToolResult {
	if ctx == nil {
		ctx = context.Background()
	}
	task, ok := args["task"].(string)
	if !ok || strings.TrimSpace(task) == "" {
		return ErrorResult("task is required and must be a non-empty string")
	}

	label, ok := args["label"].(string)
	if !ok {
		label = ""
	}
	agentID, ok := args["agent_id"].(string)
	if !ok {
		agentID = ""
	}
	targetAgentID := strings.TrimSpace(agentID)

	// Check allowlist if targeting a specific agent
	if targetAgentID != "" && t.allowlistCheck != nil {
		if !t.allowlistCheck(targetAgentID) {
			return ErrorResult(fmt.Sprintf("not allowed to spawn agent '%s'", targetAgentID))
		}
	}

	// Build system prompt for spawned subagent
	systemPrompt := fmt.Sprintf(
		`You are a spawned subagent running in the background. Complete the given task independently and report back when done.

Task: %s`,
		task,
	)

	if label != "" {
		systemPrompt = fmt.Sprintf(
			`You are a spawned subagent labeled "%s" running in the background. Complete the given task independently and report back when done.

Task: %s`,
			label,
			task,
		)
	}

	if t.manager == nil || t.spawner == nil {
		return ErrorResult("Subagent manager not configured")
	}

	// Snapshot every wrapper and request field before async preparation. A
	// strict turn owner may close its registry as soon as ExecuteAsync returns;
	// the manager-owned goroutine retains detached values only.
	manager := t.manager
	spawner := t.spawner
	turnConfig := SubTurnConfig{
		Model:          t.defaultModel,
		ModelFallbacks: cloneStringSlice(t.defaultModelFallbacks),
		Tools:          nil,
		SystemPrompt:   systemPrompt,
		MaxTokens:      t.maxTokens,
		Temperature:    t.temperature,
		Async:          false,
		Critical:       true,
		TargetAgentID:  targetAgentID,
	}
	origin := subagentTaskOrigin{
		agentID: ToolAgentID(ctx),
		session: ToolSessionKey(ctx),
		channel: ToolChannel(ctx),
		chatID:  ToolChatID(ctx),
	}
	spawnCtx := ctx
	releasePrepared := func() {}
	if preparer, ok := spawner.(asyncSubTurnContextPreparer); ok {
		var err error
		spawnCtx, releasePrepared, err = preparer.PrepareAsyncSubTurn(ctx)
		if err != nil {
			return ErrorResult(fmt.Sprintf("Spawn failed: %v", err)).WithError(err)
		}
	}
	if err := notifyTrackedSpawnAdmission(spawnCtx); err != nil {
		releasePrepared()
		return ErrorResult(fmt.Sprintf("Spawn failed: %v", err)).WithError(err)
	}
	runner := func(runCtx context.Context, _ SubagentTask) (*ToolResult, error) {
		result, err := spawner.SpawnSubTurn(runCtx, turnConfig)
		if err != nil {
			return ErrorResult(fmt.Sprintf("Spawn failed: %v", err)).WithError(err), err
		}
		return result, nil
	}
	trackedCallback := cb
	if cb != nil {
		trackedCallback = func(callbackCtx context.Context, result *ToolResult) {
			cb(withTrackedSpawnCompletion(callbackCtx), result)
		}
	}
	taskID, err := manager.spawnTracked(
		spawnCtx,
		task,
		label,
		targetAgentID,
		origin,
		runner,
		trackedCallback,
		releasePrepared,
	)
	if err != nil {
		releasePrepared()
		return ErrorResult(fmt.Sprintf("Spawn failed: %v", err)).WithError(err)
	}
	return AsyncResult(formatSubagentSpawnAcknowledgement(taskID, task, label))
}
