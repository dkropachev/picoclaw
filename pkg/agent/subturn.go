package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/providers/messageutil"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// ====================== Config & Constants ======================
const (
	// Default values for SubTurn configuration (used when config is not set or is zero)
	defaultMaxSubTurnDepth       = 3
	defaultMaxConcurrentSubTurns = 5
	defaultConcurrencyTimeout    = 30 * time.Second
	defaultSubTurnTimeout        = 5 * time.Minute
	// maxEphemeralHistorySize limits the number of messages stored in ephemeral sessions.
	// This prevents memory accumulation in long-running sub-turns.
	maxEphemeralHistorySize = 50
)

var (
	ErrDepthLimitExceeded   = errors.New("sub-turn depth limit exceeded")
	ErrInvalidSubTurnConfig = errors.New("invalid sub-turn config")
	ErrConcurrencyTimeout   = errors.New("timeout waiting for concurrency slot")
)

// getSubTurnConfig returns the effective SubTurn configuration with defaults applied.
func (al *AgentLoop) getSubTurnConfig() subTurnRuntimeConfig {
	runtimeConfig := al.GetConfig()
	if runtimeConfig == nil {
		runtimeConfig = config.DefaultConfig()
	}
	cfg := runtimeConfig.Agents.Defaults.SubTurn

	maxDepth := cfg.MaxDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxSubTurnDepth
	}

	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxConcurrentSubTurns
	}

	concurrencyTimeout := time.Duration(cfg.ConcurrencyTimeoutSec) * time.Second
	if concurrencyTimeout <= 0 {
		concurrencyTimeout = defaultConcurrencyTimeout
	}

	defaultTimeout := time.Duration(cfg.DefaultTimeoutMinutes) * time.Minute
	if defaultTimeout <= 0 {
		defaultTimeout = defaultSubTurnTimeout
	}

	return subTurnRuntimeConfig{
		maxDepth:           maxDepth,
		maxConcurrent:      maxConcurrent,
		concurrencyTimeout: concurrencyTimeout,
		defaultTimeout:     defaultTimeout,
		defaultTokenBudget: cfg.DefaultTokenBudget,
	}
}

// subTurnRuntimeConfig holds the effective runtime configuration for SubTurn execution.
type subTurnRuntimeConfig struct {
	maxDepth           int
	maxConcurrent      int
	concurrencyTimeout time.Duration
	defaultTimeout     time.Duration
	defaultTokenBudget int
}

func acquireSubTurnConcurrencyLease(
	ctx context.Context,
	parent *turnState,
	runtimeCfg subTurnRuntimeConfig,
) (*subTurnConcurrencyLease, error) {
	if parent == nil {
		return nil, fmt.Errorf("parent turn is unavailable")
	}
	lease := &subTurnConcurrencyLease{owner: parent, slot: parent.concurrencySem}
	lease.state.Store(subTurnConcurrencyLeasePrepared)
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if lease.slot == nil {
		return lease, nil
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, runtimeCfg.concurrencyTimeout)
	defer cancel()
	select {
	case lease.slot <- struct{}{}:
		if err := ctx.Err(); err != nil {
			lease.release()
			return nil, err
		}
		return lease, nil
	case <-parent.Finished():
		return nil, fmt.Errorf(
			"parent turn %s finished while waiting for a concurrency slot: %w",
			parent.turnID,
			context.Canceled,
		)
	case <-timeoutCtx.Done():
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf(
			"%w: all %d slots occupied for %v",
			ErrConcurrencyTimeout,
			runtimeCfg.maxConcurrent,
			runtimeCfg.concurrencyTimeout,
		)
	}
}

// ====================== SubTurn Config ======================

// SubTurnConfig configures the execution of a child sub-turn.
//
// Usage Examples:
//
// Synchronous sub-turn (Async=false):
//
//	cfg := SubTurnConfig{
//	    Model: "fast",
//	    SystemPrompt: "Analyze this code",
//	    Async: false,  // Result returned immediately
//	}
//	result, err := SpawnSubTurn(ctx, cfg)
//	// Use result directly here
//	processResult(result)
//
// Asynchronous sub-turn (Async=true):
//
//	cfg := SubTurnConfig{
//	    Model: "fast",
//	    SystemPrompt: "Background analysis",
//	    Async: true,  // Result delivered to channel
//	}
//	result, err := SpawnSubTurn(ctx, cfg)
//	// Result also available in parent's pendingResults channel
//	// Parent turn will poll and process it in a later iteration
type SubTurnConfig struct {
	Model          string
	ModelFallbacks []string
	// Tools is an optional name-only child authority cap. Nil inherits the
	// immediate parent's eligible capabilities; a non-nil empty slice permits
	// no tools. Non-empty entries contribute only panic-safely resolved names;
	// their pointers are never registered or executed.
	Tools        []tools.Tool
	SystemPrompt string
	MaxTokens    int

	// Async controls the result delivery mechanism:
	//
	// When Async = false (synchronous sub-turn):
	//   - The caller blocks until the sub-turn completes
	//   - The result is ONLY returned via the function return value
	//   - The result is NOT delivered to the parent's pendingResults channel
	//   - This prevents double delivery: caller gets result immediately, no need for channel
	//   - Use case: When the caller needs the result immediately to continue execution
	//   - Example: A tool that needs to process the sub-turn result before returning
	//
	// When Async = true (asynchronous sub-turn):
	//   - The sub-turn runs in the background (still blocks the caller, but semantically async)
	//   - The result is delivered to the parent's pendingResults channel
	//   - The result is ALSO returned via the function return value (for consistency)
	//   - The parent turn can poll pendingResults in later iterations to process results
	//   - Use case: Fire-and-forget operations, or when results are processed in batches
	//   - Example: Spawning multiple sub-turns in parallel and collecting results later
	//
	// IMPORTANT: The Async flag does NOT make the call non-blocking. It only controls
	// whether the result is delivered via the channel. For true non-blocking execution,
	// the caller must spawn the sub-turn in a separate goroutine.
	Async bool

	// Critical indicates this SubTurn's result is important and should continue
	// running even after the parent turn finishes gracefully.
	//
	// When parent finishes gracefully (Finish(false)):
	//   - Critical=true: SubTurn continues running, delivers result as orphan
	//   - Critical=false: SubTurn exits gracefully without error
	//
	// When parent finishes with hard abort (Finish(true)):
	//   - All SubTurns are canceled regardless of Critical flag
	Critical bool

	// Timeout is the maximum duration for this SubTurn.
	// If the SubTurn runs longer than this, it will be canceled.
	// Default is 5 minutes (defaultSubTurnTimeout) if not specified.
	Timeout time.Duration

	// MaxContextRunes limits the context size (in runes) passed to the SubTurn.
	// This prevents context window overflow by truncating message history before LLM calls.
	//
	// Values:
	//   0  = Auto-calculate based on model's ContextWindow * 0.75 (default, recommended)
	//   -1 = No limit (disable soft truncation, rely only on hard context errors)
	//   >0 = Use specified rune limit
	//
	// The soft limit acts as a first line of defense before hitting the provider's
	// hard context window limit. When exceeded, older messages are intelligently
	// truncated while preserving system messages and recent context.
	MaxContextRunes int

	// ActualSystemPrompt is injected as the true 'system' role message for the childAgent.
	// The legacy SystemPrompt field is actually used as the first 'user' message (task description).
	ActualSystemPrompt string

	// InitialMessages preloads the ephemeral session history before the agent loop starts.
	// Used by evaluator-optimizer patterns to pass the full worker context across multiple iterations.
	InitialMessages []providers.Message

	// InitialTokenBudget is a shared atomic counter for tracking remaining tokens.
	// If set, the SubTurn will inherit this budget and deduct tokens after each LLM call.
	// If nil, the SubTurn will inherit the parent's tokenBudget (if any).
	// Used by team tool to enforce token limits across all team members.
	InitialTokenBudget *atomic.Int64

	// TargetAgentID, when set, runs the sub-turn as the specified agent.
	// The target agent's workspace, model, tools, and system prompt are used
	// instead of the caller's. If empty, the sub-turn runs as the parent agent.
	TargetAgentID string
}

func cloneOptionalModelFallbacks(values []string) []string {
	if values == nil {
		return nil
	}
	return append(make([]string, 0, len(values)), values...)
}

// ====================== Context Keys ======================
type agentLoopKeyType struct{}

var agentLoopKey = agentLoopKeyType{}

// WithAgentLoop injects AgentLoop into context for tool access
func WithAgentLoop(ctx context.Context, al *AgentLoop) context.Context {
	return context.WithValue(ctx, agentLoopKey, al)
}

// AgentLoopFromContext retrieves AgentLoop from context
func AgentLoopFromContext(ctx context.Context) *AgentLoop {
	al, _ := ctx.Value(agentLoopKey).(*AgentLoop)
	return al
}

// ====================== Helper Functions ======================

func (al *AgentLoop) generateSubTurnID() string {
	return fmt.Sprintf("subturn-%d", al.subTurnCounter.Add(1))
}

// ====================== Core Function: spawnSubTurn ======================

// AgentLoopSpawner implements tools.SubTurnSpawner interface.
// This allows tools to spawn sub-turns without circular dependency.
type AgentLoopSpawner struct {
	al *AgentLoop
}

// PrepareAsyncSubTurn retains the caller's runtime generation before SpawnTool
// launches its goroutine.
func (s *AgentLoopSpawner) PrepareAsyncSubTurn(
	ctx context.Context,
) (context.Context, func(), error) {
	if s == nil || s.al == nil {
		return ctx, func() {}, errors.New("agent loop not configured")
	}
	retainedCtx, releaseRuntime, err := s.al.retainRuntimeUse(ctx)
	if err != nil {
		return ctx, func() {}, err
	}
	parent := turnStateFromContext(retainedCtx)
	if parent == nil {
		releaseRuntime()
		return ctx, func() {}, errors.New(
			"parent turnState not found in context - cannot prepare sub-turn",
		)
	}
	s.al.prepareTurnState(parent)
	lease, err := parent.retainSubTurnConstruction()
	if err != nil {
		releaseRuntime()
		return ctx, func() {}, err
	}
	if err := retainedCtx.Err(); err != nil {
		lease.release()
		releaseRuntime()
		return ctx, func() {}, err
	}
	retainedCtx = withSubTurnConstructionLease(retainedCtx, lease)
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			lease.release()
			releaseRuntime()
		})
	}
	return retainedCtx, release, nil
}

// SpawnSubTurn implements tools.SubTurnSpawner interface.
func (s *AgentLoopSpawner) SpawnSubTurn(
	ctx context.Context,
	cfg tools.SubTurnConfig,
) (*tools.ToolResult, error) {
	parentTS := turnStateFromContext(ctx)
	if parentTS == nil {
		return nil, errors.New(
			"parent turnState not found in context - cannot spawn sub-turn outside of a turn",
		)
	}

	// Convert tools.SubTurnConfig to agent.SubTurnConfig
	agentCfg := SubTurnConfig{
		Model:              cfg.Model,
		ModelFallbacks:     cloneOptionalModelFallbacks(cfg.ModelFallbacks),
		Tools:              cfg.Tools,
		SystemPrompt:       cfg.SystemPrompt,
		ActualSystemPrompt: cfg.ActualSystemPrompt,
		InitialMessages:    cfg.InitialMessages,
		InitialTokenBudget: cfg.InitialTokenBudget,
		MaxTokens:          cfg.MaxTokens,
		Async:              cfg.Async,
		Critical:           cfg.Critical,
		Timeout:            cfg.Timeout,
		MaxContextRunes:    cfg.MaxContextRunes,
		TargetAgentID:      cfg.TargetAgentID,
	}

	return spawnSubTurn(ctx, s.al, parentTS, agentCfg)
}

// NewSubTurnSpawner creates a SubTurnSpawner for the given AgentLoop.
func NewSubTurnSpawner(al *AgentLoop) *AgentLoopSpawner {
	return &AgentLoopSpawner{al: al}
}

// SpawnSubTurn is the exported entry point for tools to spawn sub-turns.
// It retrieves AgentLoop and parent turnState from context and delegates to spawnSubTurn.
func SpawnSubTurn(ctx context.Context, cfg SubTurnConfig) (*tools.ToolResult, error) {
	al := AgentLoopFromContext(ctx)
	if al == nil {
		return nil, errors.New(
			"AgentLoop not found in context - ensure context is properly initialized",
		)
	}

	parentTS := turnStateFromContext(ctx)
	if parentTS == nil {
		return nil, errors.New(
			"parent turnState not found in context - cannot spawn sub-turn outside of a turn",
		)
	}

	return spawnSubTurn(ctx, al, parentTS, cfg)
}

func spawnSubTurn(
	ctx context.Context,
	al *AgentLoop,
	parentTS *turnState,
	cfg SubTurnConfig,
) (result *tools.ToolResult, err error) {
	// Freeze every caller-owned slice before retention, validation, or detached
	// construction. Async tool wrappers already snapshot these inputs, while
	// this boundary also protects direct exported/custom callers.
	cfg.ModelFallbacks = cloneOptionalModelFallbacks(cfg.ModelFallbacks)
	if cfg.Tools != nil {
		frozenTools := make([]tools.Tool, len(cfg.Tools))
		copy(frozenTools, cfg.Tools)
		cfg.Tools = frozenTools
	}
	cfg.InitialMessages = cloneProviderMessages(cfg.InitialMessages)

	var releaseRuntime func()
	if cfg.Async || cfg.Critical {
		ctx, releaseRuntime, err = al.retainRuntimeUse(ctx)
	} else {
		ctx, releaseRuntime, err = al.acquireRuntimeUse(ctx)
	}
	if err != nil {
		return nil, err
	}
	defer releaseRuntime()
	rtCfg := al.getSubTurnConfig()
	al.prepareTurnState(parentTS)
	sourceLease := subTurnConstructionLeaseFromContext(ctx)
	concurrencyLease, err := acquireSubTurnConcurrencyLease(ctx, parentTS, rtCfg)
	if err != nil {
		if sourceLease != nil && sourceLease.owner == parentTS {
			sourceLease.release()
		}
		return nil, err
	}
	if !concurrencyLease.consumeFor(parentTS) {
		concurrencyLease.release()
		return nil, fmt.Errorf("cannot consume child concurrency lease")
	}
	defer concurrencyLease.release()
	if !parentTS.acceptsPreAdmittedSubTurnConstruction(sourceLease) {
		if sourceLease != nil && sourceLease.owner == parentTS {
			sourceLease.release()
		}
		return nil, fmt.Errorf(
			"parent turn %s no longer accepts children: %w",
			parentTS.turnID,
			context.Canceled,
		)
	}
	if !sourceLease.consumeFor(parentTS) {
		sourceLease, err = parentTS.retainSubTurnConstruction()
		if err != nil {
			return nil, err
		}
		if !sourceLease.consumeFor(parentTS) {
			sourceLease.release()
			return nil, fmt.Errorf("cannot consume child construction lease")
		}
		ctx = withSubTurnConstructionLease(ctx, sourceLease)
	}
	sourceLeaseReleased := false
	defer func() {
		if !sourceLeaseReleased {
			sourceLease.release()
		}
	}()

	// 1. Depth limit check
	if parentTS.depth >= rtCfg.maxDepth {
		logger.WarnCF("subturn", "Depth limit exceeded", map[string]any{
			"parent_id": parentTS.turnID,
			"depth":     parentTS.depth,
			"max_depth": rtCfg.maxDepth,
		})
		return nil, ErrDepthLimitExceeded
	}

	// 2. Config validation: Model is required unless TargetAgentID is set
	//    (the target agent provides its own model).
	if cfg.Model == "" && cfg.TargetAgentID == "" {
		return nil, ErrInvalidSubTurnConfig
	}

	// 3. Determine timeout for child SubTurn
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = rtCfg.defaultTimeout
	}

	// 4. Create an independently cancelable child context while preserving the
	// retained runtime-generation marker. WithoutCancel lets a critical child
	// survive parent completion; the child timeout remains its protection.
	childCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	childID := al.generateSubTurnID()

	// Resolve the agent instance for the child turn.
	// When TargetAgentID is set, look up that agent from the registry so the
	// child runs with the target's workspace, model, tools, and system prompt.
	// Otherwise fall back to the parent's agent (existing behavior).
	registry := al.GetRegistry()
	if registry == nil {
		return nil, errors.New("agent registry not configured")
	}
	var baseAgent *AgentInstance
	if cfg.TargetAgentID != "" {
		if parentTS.agent == nil || parentTS.agent.ID == "" ||
			!registry.CanSpawnSubagent(parentTS.agent.ID, cfg.TargetAgentID) {
			return nil, fmt.Errorf(
				"target agent %q is not allowed for parent %q: %w",
				cfg.TargetAgentID,
				parentTS.agentID,
				ErrInvalidSubTurnConfig,
			)
		}
		var ok bool
		baseAgent, ok = registry.GetAgent(cfg.TargetAgentID)
		if !ok {
			return nil, fmt.Errorf(
				"%w: target agent %q not found in registry",
				ErrInvalidSubTurnConfig,
				cfg.TargetAgentID,
			)
		}
		cfg.TargetAgentID = baseAgent.ID
	} else {
		baseAgent = parentTS.agent
	}
	if baseAgent == nil {
		return nil, errors.New("parent turnState has no agent instance")
	}
	ephemeralStore := newEphemeralSession(nil)
	resourceGuard := &turnState{turnSession: ephemeralStore}
	resourcesTransferred := false
	defer func() {
		if !resourcesTransferred {
			_, _ = resourceGuard.closeOwnedTurnResources()
		}
	}()
	agent := *baseAgent // shallow copy
	agent.Sessions = ephemeralStore
	toolSelection, selectionErr := selectEffectiveSubTurnTools(
		al.GetConfig(),
		parentTS,
		baseAgent,
		cfg.Tools,
		parentTS.depth+1,
		rtCfg.maxDepth,
		subTurnToolSelectionOptions{
			implementationProviderSetProven: subTurnUsesProvenImplementationProviderSet(
				baseAgent,
				cfg,
			),
			parentAuthorityFrozen: true,
			parentAuthority:       sourceLease.nativeAuthority,
		},
	)
	if selectionErr != nil {
		return nil, selectionErr
	}
	// Selectors are name-only input. Drop every caller pointer before child
	// construction or execution so the turn retains only exact resolved keys.
	cfg.Tools = nil
	dispatch := DispatchRequest{
		SessionKey:     childID,
		UserMessage:    cfg.SystemPrompt,
		Media:          nil,
		InboundContext: cloneInboundContext(parentTS.opts.Dispatch.InboundContext),
	}
	scope := al.newTurnEventScope(
		agent.ID,
		childID,
		newTurnContext(dispatch.InboundContext, dispatch.RouteResult, dispatch.SessionScope),
	)
	ownedTools, constructionErr := baseAgent.Tools.InstantiateForOwnerSelection(
		tools.ToolOwner{
			Scope:      tools.ToolOwnerScopeTurn,
			AgentID:    baseAgent.ID,
			SessionKey: childID,
			TurnID:     scope.turnID,
		},
		toolSelection.roots,
	)
	if constructionErr != nil {
		return nil, fmt.Errorf("construct child tools: %w", constructionErr)
	}
	resourceGuard.turnTools = ownedTools
	agent.Tools = ownedTools

	// Create processOptions for the child turn
	opts := processOptions{
		Dispatch:                dispatch,
		SenderID:                parentTS.opts.Dispatch.SenderID(),
		SenderDisplayName:       parentTS.opts.SenderDisplayName,
		TurnProfile:             toolSelection.profile,
		SystemPromptOverride:    cfg.ActualSystemPrompt,
		InitialSteeringMessages: cfg.InitialMessages,
		DefaultResponse:         "",
		EnableSummary:           false,
		SendResponse:            false,
		NoHistory:               true, // SubTurns don't use session history
		SkipInitialSteeringPoll: true,
	}
	if cfg.TargetAgentID == "" {
		opts.ModelNameOverride = strings.TrimSpace(cfg.Model)
		if cfg.ModelFallbacks != nil {
			opts.ModelFallbacksOverride = cloneOptionalModelFallbacks(cfg.ModelFallbacks)
		}
	}
	if !opts.TurnProfile.Enabled {
		opts.TurnProfile = parentTS.opts.TurnProfile
	}

	// Create child turnState using the new API
	childTS := newTurnState(&agent, opts, scope)

	// Set SubTurn-specific fields
	childTS.cancelFunc = cancel
	childTS.critical = cfg.Critical
	childTS.depth = parentTS.depth + 1
	childTS.parentTurnID = parentTS.turnID
	childTS.parentTurnState = parentTS
	childTS.session = ephemeralStore // same store as agent.Sessions
	childTS.toolAuthorityBound = true
	childTS.nativeSearchAllowed = toolSelection.nativeSearch
	childTS.turnTools = ownedTools
	childTS.turnSession = ephemeralStore
	defer func() {
		closeErr, closed := childTS.closeOwnedTurnResources()
		if !closed || closeErr == nil {
			return
		}
		err = errors.Join(err, closeErr)
		if result != nil {
			result = tools.ErrorResult(fmt.Sprintf("SubTurn cleanup failed: %v", closeErr)).WithError(err)
		}
	}()
	resourcesTransferred = true
	al.prepareTurnState(childTS)

	// Token budget initialization/inheritance
	// If InitialTokenBudget is explicitly provided (e.g., by team tool), use it.
	// Otherwise, inherit from parent's tokenBudget (for nested SubTurns).
	if cfg.InitialTokenBudget != nil {
		childTS.tokenBudget = cfg.InitialTokenBudget
	} else if parentTS.tokenBudget != nil {
		childTS.tokenBudget = parentTS.tokenBudget
	} else if rtCfg.defaultTokenBudget > 0 {
		// Apply default token budget from config if no budget is set
		budget := &atomic.Int64{}
		budget.Store(int64(rtCfg.defaultTokenBudget))
		childTS.tokenBudget = budget
	}

	// IMPORTANT: Put childTS into childCtx so that code inside runTurn can retrieve it
	childCtx = withTurnState(childCtx, childTS)
	childCtx = WithAgentLoop(childCtx, al) // Propagate AgentLoop to child turn

	childTS.ctx = childCtx

	if !sourceLease.reserveAttachmentFor(parentTS, childTS) {
		return nil, fmt.Errorf("cannot reserve child attachment")
	}

	// Publish the exact child and parent edge as one parent-locked operation.
	// A terminal/canceling parent rejects attachment, so no child can escape a
	// concurrent error or hard-abort tree traversal.
	if !al.attachChildTurnWithLease(parentTS, childTS, sourceLease) {
		return nil, fmt.Errorf("parent turn %s no longer accepts children: %w", parentTS.turnID, context.Canceled)
	}
	sourceLease.release()
	sourceLeaseReleased = true
	defer al.releaseSessionTurnState(childTS.sessionKey, childTS)

	// 6. Emit Spawn event
	al.emitEvent(runtimeevents.KindAgentSubTurnSpawn,
		childTS.eventMeta("spawnSubTurn", "subturn.spawn"),
		SubTurnSpawnPayload{
			AgentID:      childTS.agentID,
			Label:        childID,
			ParentTurnID: parentTS.turnID,
		},
	)

	childOutcome := TurnEndStatusError
	// 7. Defer cleanup: deliver result (for async), emit End event, and recover from panics
	defer func() {
		if r := recover(); r != nil {
			logger.RecoverPanicNoExit(r)
			err = fmt.Errorf("subturn panicked: %v", r)
			result = tools.ErrorResult(fmt.Sprintf("SubTurn failed: %v", err)).WithError(err)
			logger.ErrorCF("subturn", "SubTurn panicked", map[string]any{
				"child_id":  childID,
				"parent_id": parentTS.turnID,
				"panic":     r,
			})
		}

		// Result Delivery Strategy (Async vs Sync)
		if cfg.Async {
			deliverSubTurnResult(al, parentTS, childID, result)
		}

		status := "error"
		if err == nil && childOutcome == TurnEndStatusCompleted {
			status = "completed"
		}
		al.emitEvent(runtimeevents.KindAgentSubTurnEnd,
			childTS.eventMeta("spawnSubTurn", "subturn.end"),
			SubTurnEndPayload{
				AgentID: childTS.agentID,
				Status:  status,
			},
		)
	}()

	// 8. Execute sub-turn via the real agent loop.
	pipeline := NewPipeline(al)
	turnRes, turnErr := al.runTurn(childCtx, childTS, pipeline)
	childOutcome = turnRes.status

	// Free the execution slot before async result delivery can block on the
	// parent's mailbox. The idempotent defer covers every earlier return.
	concurrencyLease.release()

	// Convert turnResult to tools.ToolResult
	if turnErr != nil {
		err = turnErr
		result = tools.ErrorResult(fmt.Sprintf("SubTurn failed: %v", turnErr)).WithError(turnErr)
	} else if turnRes.status == TurnEndStatusAborted {
		err = fmt.Errorf("subturn aborted: %w", context.Canceled)
		result = tools.ErrorResult("SubTurn aborted").WithError(err)
	} else if turnRes.status == TurnEndStatusError {
		err = fmt.Errorf("subturn canceled after parent failure: %w", context.Canceled)
		result = tools.ErrorResult("SubTurn canceled after parent failure").WithError(err)
	} else {
		result = &tools.ToolResult{
			ForLLM:  turnRes.finalContent,
			ForUser: turnRes.finalContent,
		}
	}

	return result, err
}

// ====================== Result Delivery ======================

// deliverSubTurnResult delivers a sub-turn result to the parent turn's pendingResults channel.
//
// IMPORTANT: This function is ONLY called for asynchronous sub-turns (Async=true).
// For synchronous sub-turns (Async=false), results are returned directly via the function
// return value to avoid double delivery.
//
// Delivery behavior:
//   - If parent turn is still running: attempts to deliver to pendingResults channel
//   - If channel is full: emits agent.subturn.orphan (result is lost from channel but tracked)
//   - If parent turn has finished: emits agent.subturn.orphan (late arrival)
//
// Thread safety:
//   - Terminal commit and the non-blocking send are serialized by parent.mu
//   - pendingResults is never closed; GC owns mailbox lifetime
//
// Event emissions:
//   - agent.subturn.result_delivered: successful delivery to channel
//   - agent.subturn.orphan: delivery failed (parent finished or channel full)
func deliverSubTurnResult(al *AgentLoop, parentTS *turnState, childID string, result *tools.ToolResult) {
	if parentTS == nil {
		return
	}

	delivered := false
	reason := ""
	contentLen := 0
	parentTS.mu.Lock()
	switch {
	case result == nil:
		reason = "nil_result"
	case parentTS.terminalClaimed || parentTS.isFinished.Load() || parentTS.cancelRequested:
		reason = "parent_finished"
	case parentTS.pendingResults == nil:
		reason = "parent_mailbox_unavailable"
	default:
		select {
		case parentTS.pendingResults <- result:
			delivered = true
			contentLen = len(result.ForLLM)
		default:
			reason = "channel_full"
		}
	}
	parentTS.mu.Unlock()

	if al == nil {
		return
	}
	if delivered {
		al.emitEvent(runtimeevents.KindAgentSubTurnResultDelivered,
			parentTS.eventMeta("deliverSubTurnResult", "subturn.result_delivered"),
			SubTurnResultDeliveredPayload{ContentLen: contentLen},
		)
		return
	}
	al.emitEvent(runtimeevents.KindAgentSubTurnOrphan,
		parentTS.eventMeta("deliverSubTurnResult", "subturn.orphan"),
		SubTurnOrphanPayload{
			ParentTurnID: parentTS.turnID,
			ChildTurnID:  childID,
			Reason:       reason,
		},
	)
}

// ====================== Other Types ======================

// ephemeralSessionStore is an in-memory session.SessionStore used by SubTurns.
// It does not persist to disk and auto-truncates history to maxEphemeralHistorySize.
type ephemeralSessionStore struct {
	mu      sync.Mutex
	history []providers.Message
	summary string
}

func newEphemeralSession(initial []providers.Message) ephemeralSessionStoreIface {
	s := &ephemeralSessionStore{}
	if len(initial) > 0 {
		s.history = append(s.history, initial...)
	}
	return s
}

// ephemeralSessionStoreIface is satisfied by *ephemeralSessionStore.
// Declared so newEphemeralSession can return a typed interface.
type ephemeralSessionStoreIface interface {
	AddMessage(sessionKey, role, content string)
	AddFullMessage(sessionKey string, msg providers.Message)
	GetHistory(key string) []providers.Message
	GetSummary(key string) string
	SetSummary(key, summary string)
	SetHistory(key string, history []providers.Message)
	TruncateHistory(key string, keepLast int)
	Save(key string) error
	ListSessions() []string
	Close() error
}

func (e *ephemeralSessionStore) AddMessage(_, role, content string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.history = append(e.history, providers.Message{Role: role, Content: content})
	e.truncateLocked()
}

func (e *ephemeralSessionStore) AddFullMessage(_ string, msg providers.Message) {
	if messageutil.IsTransientAssistantThoughtMessage(msg) {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.history = append(e.history, msg)
	e.truncateLocked()
}

func (e *ephemeralSessionStore) GetHistory(_ string) []providers.Message {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]providers.Message, len(e.history))
	copy(out, e.history)
	return out
}

func (e *ephemeralSessionStore) GetSummary(_ string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.summary
}

func (e *ephemeralSessionStore) SetSummary(_, summary string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.summary = summary
}

func (e *ephemeralSessionStore) SetHistory(_ string, history []providers.Message) {
	e.mu.Lock()
	defer e.mu.Unlock()
	history = messageutil.FilterInvalidHistoryMessages(history)
	e.history = make([]providers.Message, len(history))
	copy(e.history, history)
	e.truncateLocked()
}

func (e *ephemeralSessionStore) TruncateHistory(_ string, keepLast int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if keepLast <= 0 {
		e.history = nil
		return
	}

	if keepLast >= len(e.history) {
		return
	}
	e.history = e.history[len(e.history)-keepLast:]
}

func (e *ephemeralSessionStore) Save(_ string) error    { return nil }
func (e *ephemeralSessionStore) Close() error           { return nil }
func (e *ephemeralSessionStore) ListSessions() []string { return nil }

func (e *ephemeralSessionStore) truncateLocked() {
	if len(e.history) > maxEphemeralHistorySize {
		e.history = e.history[len(e.history)-maxEphemeralHistorySize:]
	}
}
