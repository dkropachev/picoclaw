package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
)

// SubTurnSpawner is an interface for spawning sub-turns.
// This avoids circular dependency between tools and agent packages.
type SubTurnSpawner interface {
	SpawnSubTurn(ctx context.Context, cfg SubTurnConfig) (*ToolResult, error)
}

// SubTurnConfig holds configuration for spawning a sub-turn.
type SubTurnConfig struct {
	Model          string
	ModelFallbacks []string
	// Tools selects child authority by tool name only. Nil inherits, a non-nil
	// empty slice selects no tools, and supplied pointers are never injected.
	Tools              []Tool
	SystemPrompt       string
	MaxTokens          int
	Temperature        float64
	Async              bool          // direct SubTurn result-channel mode; manager-backed spawn uses false
	Critical           bool          // continue running after parent finishes gracefully
	Timeout            time.Duration // 0 = use default (5 minutes)
	MaxContextRunes    int           // 0 = auto, -1 = no limit, >0 = explicit limit
	ActualSystemPrompt string
	InitialMessages    []providers.Message
	InitialTokenBudget *atomic.Int64 // Shared token budget for team members; nil if no budget
	TargetAgentID      string        // If set, run as this agent (its workspace, model, tools)
}

type SubagentTask struct {
	ID               string
	Task             string
	Label            string
	AgentID          string
	OriginAgentID    string
	OriginSessionKey string
	OriginChannel    string
	OriginChatID     string
	Status           string
	Result           string
	Created          int64
}

// SubagentCompletion identifies the committed terminal state associated with
// an asynchronous subagent callback. Values returned from
// SubagentCompletionFromContext are detached copies.
type SubagentCompletion struct {
	TaskID string
	Status string
}

type subagentCompletionContextKey struct{}

type subagentCompletionContextValue struct {
	completion SubagentCompletion
}

type trackedSpawnCompletionContextKey struct{}

type trackedSpawnCompletionContextValue struct{}

func withSubagentCompletion(
	ctx context.Context,
	completion SubagentCompletion,
) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(
		ctx,
		subagentCompletionContextKey{},
		subagentCompletionContextValue{completion: completion},
	)
}

// SubagentCompletionFromContext returns the committed task identity and
// terminal status attached to a tracked subagent callback.
func SubagentCompletionFromContext(ctx context.Context) (SubagentCompletion, bool) {
	if ctx == nil {
		return SubagentCompletion{}, false
	}
	value, ok := ctx.Value(subagentCompletionContextKey{}).(subagentCompletionContextValue)
	if !ok {
		return SubagentCompletion{}, false
	}
	return value.completion, true
}

func withTrackedSpawnCompletion(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(
		ctx,
		trackedSpawnCompletionContextKey{},
		trackedSpawnCompletionContextValue{},
	)
}

// TrackedSpawnCompletionFromContext returns committed task metadata only when
// the callback came through the first-party SpawnTool delivery wrapper. Public
// SubagentManager callbacks carry SubagentCompletion but not this additional
// unforgeable provenance marker.
func TrackedSpawnCompletionFromContext(ctx context.Context) (SubagentCompletion, bool) {
	if !IsTrackedSpawnCompletionCallback(ctx) {
		return SubagentCompletion{}, false
	}
	return SubagentCompletionFromContext(ctx)
}

// IsTrackedSpawnCompletionCallback reports whether the callback came through
// the private first-party SpawnTool wrapper, independently of manager metadata.
func IsTrackedSpawnCompletionCallback(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	if _, ok := ctx.Value(trackedSpawnCompletionContextKey{}).(trackedSpawnCompletionContextValue); !ok {
		return false
	}
	return true
}

const (
	subagentTaskStatusRunning   = "running"
	subagentTaskStatusCompleted = "completed"
	subagentTaskStatusFailed    = "failed"
	subagentTaskStatusCanceled  = "canceled"
)

type subagentTaskOrigin struct {
	agentID string
	session string
	channel string
	chatID  string
}

type subagentTaskRunner func(context.Context, SubagentTask) (*ToolResult, error)

type SpawnSubTurnFunc func(
	ctx context.Context,
	task, label, agentID string,
	tools *ToolRegistry,
	maxTokens int,
	temperature float64,
	hasMaxTokens, hasTemperature bool,
) (*ToolResult, error)

type SubagentManager struct {
	tasks                 map[string]*SubagentTask
	mu                    sync.RWMutex
	provider              providers.LLMProvider
	defaultModel          string
	defaultModelFallbacks []string
	workspace             string
	tools                 *ToolRegistry
	maxIterations         int
	maxTokens             int
	temperature           float64
	hasMaxTokens          bool
	hasTemperature        bool
	nextID                int
	spawner               SpawnSubTurnFunc

	// mediaResolver resolves media:// refs in tool-loop messages before
	// each LLM call in the legacy RunToolLoop fallback path.
	// This lets subagents reuse the same media handling behavior as the
	// main agent loop without importing pkg/agent and creating a cycle.
	mediaResolver func([]providers.Message) []providers.Message
}

func NewSubagentManager(
	provider providers.LLMProvider,
	defaultModel, workspace string,
) *SubagentManager {
	return &SubagentManager{
		tasks:         make(map[string]*SubagentTask),
		provider:      provider,
		defaultModel:  defaultModel,
		workspace:     workspace,
		tools:         NewToolRegistry(),
		maxIterations: 10,
		nextID:        1,
	}
}

func (sm *SubagentManager) SetSpawner(spawner SpawnSubTurnFunc) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.spawner = spawner
}

// SetDefaultModelFallbacks configures exact fallback aliases for subagent
// turns. A non-nil empty slice explicitly disables inherited fallbacks.
func (sm *SubagentManager) SetDefaultModelFallbacks(fallbacks []string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.defaultModelFallbacks = cloneStringSlice(fallbacks)
}

func cloneStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	return append(make([]string, 0, len(values)), values...)
}

// SetMediaResolver injects a message preprocessor that resolves media:// refs
// into LLM-ready content before each tool-loop iteration.
// This is only used by the legacy RunToolLoop fallback path.
func (sm *SubagentManager) SetMediaResolver(
	resolver func([]providers.Message) []providers.Message,
) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.mediaResolver = resolver
}

// SetLLMOptions sets max tokens and temperature for subagent LLM calls.
func (sm *SubagentManager) SetLLMOptions(maxTokens int, temperature float64) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.maxTokens = maxTokens
	sm.hasMaxTokens = true
	sm.temperature = temperature
	sm.hasTemperature = true
}

// SetTools sets the tool registry for subagent execution.
// If not set, subagent will have access to the provided tools.
func (sm *SubagentManager) SetTools(tools *ToolRegistry) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.tools = tools
}

// RegisterTool registers a tool for subagent execution.
func (sm *SubagentManager) RegisterTool(tool Tool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.tools.Register(tool)
}

func (sm *SubagentManager) Spawn(
	ctx context.Context,
	task, label, agentID, originChannel, originChatID string,
	callback AsyncCallback,
) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runner := sm.legacyTaskRunnerSnapshot()
	taskID, err := sm.spawnTracked(
		ctx,
		task,
		label,
		agentID,
		subagentTaskOrigin{
			agentID: ToolAgentID(ctx),
			session: ToolSessionKey(ctx),
			channel: originChannel,
			chatID:  originChatID,
		},
		runner,
		callback,
		nil,
	)
	if err != nil {
		return "", err
	}
	return formatSubagentSpawnAcknowledgement(taskID, task, label), nil
}

func (sm *SubagentManager) spawnTracked(
	ctx context.Context,
	task, label, agentID string,
	origin subagentTaskOrigin,
	runner subagentTaskRunner,
	callback AsyncCallback,
	onDone func(),
) (string, error) {
	if sm == nil {
		return "", fmt.Errorf("subagent manager is nil")
	}
	if runner == nil {
		return "", fmt.Errorf("subagent task runner is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	sm.mu.Lock()
	if sm.tasks == nil {
		sm.tasks = make(map[string]*SubagentTask)
	}
	if sm.nextID <= 0 {
		sm.nextID = 1
	}
	taskID := fmt.Sprintf("subagent-%d", sm.nextID)
	sm.nextID++
	sm.tasks[taskID] = &SubagentTask{
		ID:               taskID,
		Task:             task,
		Label:            label,
		AgentID:          agentID,
		OriginAgentID:    origin.agentID,
		OriginSessionKey: origin.session,
		OriginChannel:    origin.channel,
		OriginChatID:     origin.chatID,
		Status:           subagentTaskStatusRunning,
		Created:          time.Now().UnixMilli(),
	}
	sm.mu.Unlock()

	go sm.runTask(ctx, taskID, runner, callback, onDone)
	return taskID, nil
}

func (sm *SubagentManager) runTask(
	ctx context.Context,
	taskID string,
	runner subagentTaskRunner,
	callback AsyncCallback,
	onDone func(),
) {
	defer callSubagentTaskFinalizer(onDone)

	task, ok := sm.GetTaskCopy(taskID)
	if !ok {
		return
	}

	preCanceled := ctx.Err() != nil
	var result *ToolResult
	var err error
	if preCanceled {
		err = ctx.Err()
	} else {
		result, err = callSubagentTaskRunner(ctx, task, runner)
	}

	status, storedResult, callbackResult := normalizeSubagentTaskOutcome(
		result,
		err,
		preCanceled,
	)
	committedTask, committed := sm.commitTaskTerminal(taskID, status, storedResult)
	if !committed {
		return
	}
	callbackCtx := withSubagentCompletion(ctx, SubagentCompletion{
		TaskID: committedTask.ID,
		Status: committedTask.Status,
	})
	callSubagentTaskCallback(callbackCtx, callback, callbackResult)
}

func (sm *SubagentManager) legacyTaskRunnerSnapshot() subagentTaskRunner {
	if sm == nil {
		return nil
	}
	sm.mu.RLock()
	spawner := sm.spawner
	toolRegistry := sm.tools
	provider := sm.provider
	model := sm.defaultModel
	maxIter := sm.maxIterations
	maxTokens := sm.maxTokens
	temperature := sm.temperature
	hasMaxTokens := sm.hasMaxTokens
	hasTemperature := sm.hasTemperature
	mediaResolver := sm.mediaResolver
	sm.mu.RUnlock()

	return func(ctx context.Context, task SubagentTask) (*ToolResult, error) {
		if spawner != nil {
			return spawner(
				ctx,
				task.Task,
				task.Label,
				task.AgentID,
				toolRegistry,
				maxTokens,
				temperature,
				hasMaxTokens,
				hasTemperature,
			)
		}

		systemPrompt := `You are a subagent. Complete the given task independently and report the result.
You have access to tools - use them as needed to complete your task.
After completing the task, provide a clear summary of what was done.`
		messages := []providers.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: task.Task},
		}
		var llmOptions map[string]any
		if hasMaxTokens || hasTemperature {
			llmOptions = make(map[string]any)
			if hasMaxTokens {
				llmOptions["max_tokens"] = maxTokens
			}
			if hasTemperature {
				llmOptions["temperature"] = temperature
			}
		}
		loopResult, err := RunToolLoop(ctx, ToolLoopConfig{
			Provider:      provider,
			Model:         model,
			Tools:         toolRegistry,
			MaxIterations: maxIter,
			LLMOptions:    llmOptions,
			MediaResolver: mediaResolver,
		}, messages, task.OriginChannel, task.OriginChatID)
		if err != nil {
			return nil, err
		}
		return &ToolResult{
			ForLLM: fmt.Sprintf(
				"Subagent '%s' completed (iterations: %d): %s",
				task.Label,
				loopResult.Iterations,
				loopResult.Content,
			),
			ForUser: loopResult.Content,
		}, nil
	}
}

func (sm *SubagentManager) commitTaskTerminal(
	taskID, status, result string,
) (SubagentTask, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	task := sm.tasks[taskID]
	if task == nil || task.Status != subagentTaskStatusRunning {
		return SubagentTask{}, false
	}
	task.Status = status
	task.Result = result
	return *task, true
}

func callSubagentTaskRunner(
	ctx context.Context,
	task SubagentTask,
	runner subagentTaskRunner,
) (result *ToolResult, returnErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.RecoverPanicNoExit(recovered)
			result = nil
			returnErr = fmt.Errorf("subagent task panicked: %v", recovered)
		}
	}()
	return runner(ctx, task)
}

func normalizeSubagentTaskOutcome(
	result *ToolResult,
	err error,
	preCanceled bool,
) (status, storedResult string, callbackResult *ToolResult) {
	if err == nil && result == nil {
		err = fmt.Errorf("subagent task returned nil result")
	}
	if err == nil && result.IsError {
		err = result.Err
		if err == nil {
			err = fmt.Errorf("subagent task returned an error result")
		}
	}
	if err == nil {
		return subagentTaskStatusCompleted, result.ForLLM, result
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		storedResult = "Task canceled during execution"
		if preCanceled {
			storedResult = "Task canceled before execution"
		}
		status = subagentTaskStatusCanceled
	} else {
		storedResult = fmt.Sprintf("Error: %v", err)
		status = subagentTaskStatusFailed
	}
	if result != nil && result.IsError {
		callbackCopy := *result
		callbackCopy.Err = err
		callbackResult = &callbackCopy
		if content := callbackResult.ContentForLLM(); content != "" {
			storedResult = content
		}
	} else {
		callbackResult = ErrorResult(storedResult).WithError(err)
	}
	return status, storedResult, callbackResult
}

func callSubagentTaskCallback(
	ctx context.Context,
	callback AsyncCallback,
	result *ToolResult,
) {
	if callback == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.RecoverPanicNoExit(recovered)
		}
	}()
	callback(ctx, result)
}

func callSubagentTaskFinalizer(finalizer func()) {
	if finalizer == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.RecoverPanicNoExit(recovered)
		}
	}()
	finalizer()
}

func formatSubagentSpawnAcknowledgement(taskID, task, label string) string {
	if label != "" {
		return fmt.Sprintf(
			"Spawned subagent '%s' for task: %s (task_id=%s)",
			label,
			task,
			taskID,
		)
	}
	return fmt.Sprintf("Spawned subagent for task: %s (task_id=%s)", task, taskID)
}

// GetTask returns a detached compatibility pointer, never the live map entry.
func (sm *SubagentManager) GetTask(taskID string) (*SubagentTask, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	task, ok := sm.tasks[taskID]
	if !ok {
		return nil, false
	}
	snapshot := *task
	return &snapshot, true
}

// GetTaskCopy returns a copy of the task with the given ID, taken under the
// read lock, so the caller receives a consistent snapshot with no data race.
func (sm *SubagentManager) GetTaskCopy(taskID string) (SubagentTask, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	task, ok := sm.tasks[taskID]
	if !ok {
		return SubagentTask{}, false
	}
	return *task, true
}

// ListTasks returns detached compatibility pointers for every current record.
func (sm *SubagentManager) ListTasks() []*SubagentTask {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	tasks := make([]*SubagentTask, 0, len(sm.tasks))
	for _, task := range sm.tasks {
		snapshot := *task
		tasks = append(tasks, &snapshot)
	}
	return tasks
}

// ListTaskCopies returns value copies of all tasks, taken under the read lock,
// so callers receive consistent snapshots with no data race.
func (sm *SubagentManager) ListTaskCopies() []SubagentTask {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	copies := make([]SubagentTask, 0, len(sm.tasks))
	for _, task := range sm.tasks {
		copies = append(copies, *task)
	}
	return copies
}

// SubagentTool executes a subagent task synchronously and returns the result.
// It directly calls SubTurnSpawner with Async=false for synchronous execution.
type SubagentTool struct {
	spawner               SubTurnSpawner
	defaultModel          string
	defaultModelFallbacks []string
	maxTokens             int
	temperature           float64
}

func NewSubagentTool(manager *SubagentManager) *SubagentTool {
	if manager == nil {
		return &SubagentTool{}
	}
	return &SubagentTool{
		defaultModel:          manager.defaultModel,
		defaultModelFallbacks: cloneStringSlice(manager.defaultModelFallbacks),
		maxTokens:             manager.maxTokens,
		temperature:           manager.temperature,
	}
}

// SetSpawner sets the SubTurnSpawner for direct sub-turn execution.
func (t *SubagentTool) SetSpawner(spawner SubTurnSpawner) {
	t.spawner = spawner
}

func (t *SubagentTool) Name() string {
	return "subagent"
}

func (t *SubagentTool) Description() string {
	return "Execute a subagent task synchronously and return the result. Use this for delegating specific tasks to an independent agent instance. Returns execution summary to user and full details to LLM."
}

func (t *SubagentTool) Parameters() map[string]any {
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
		},
		"required": []string{"task"},
	}
}

func (t *SubagentTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	task, ok := args["task"].(string)
	if !ok {
		return ErrorResult("task is required").WithError(fmt.Errorf("task parameter is required"))
	}

	label, ok := args["label"].(string)
	if !ok {
		label = ""
	}

	// Build system prompt for subagent
	systemPrompt := fmt.Sprintf(
		`You are a subagent. Complete the given task independently and provide a clear, concise result.

Task: %s`,
		task,
	)

	if label != "" {
		systemPrompt = fmt.Sprintf(
			`You are a subagent labeled "%s". Complete the given task independently and provide a clear, concise result.

Task: %s`,
			label,
			task,
		)
	}

	// Use spawner if available (direct SpawnSubTurn call)
	if t.spawner != nil {
		result, err := t.spawner.SpawnSubTurn(ctx, SubTurnConfig{
			Model:          t.defaultModel,
			ModelFallbacks: cloneStringSlice(t.defaultModelFallbacks),
			Tools:          nil, // Will inherit from parent via context
			SystemPrompt:   systemPrompt,
			MaxTokens:      t.maxTokens,
			Temperature:    t.temperature,
			Async:          false, // Synchronous execution
		})
		if err != nil {
			return ErrorResult(fmt.Sprintf("Subagent execution failed: %v", err)).WithError(err)
		}

		// Format result for display
		userContent := result.ForLLM
		if result.ForUser != "" {
			userContent = result.ForUser
		}
		maxUserLen := 500
		if len(userContent) > maxUserLen {
			userContent = userContent[:maxUserLen] + "..."
		}

		labelStr := label
		if labelStr == "" {
			labelStr = "(unnamed)"
		}
		llmContent := fmt.Sprintf("Subagent task completed:\nLabel: %s\nResult: %s",
			labelStr, result.ForLLM)

		return &ToolResult{
			ForLLM:  llmContent,
			ForUser: userContent,
			Silent:  false,
			IsError: result.IsError,
			Async:   false,
		}
	}

	// Fallback: spawner not configured
	return ErrorResult("Subagent manager not configured").WithError(fmt.Errorf("spawner not set"))
}
