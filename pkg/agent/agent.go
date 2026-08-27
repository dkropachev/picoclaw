// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent/interfaces"
	"github.com/sipeed/picoclaw/pkg/audio/asr"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/commands"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/constants"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/state"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/utils"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type AgentLoop struct {
	// Core dependencies
	bus        interfaces.MessageBus
	cfg        *config.Config
	configPath string
	registry   *AgentRegistry
	state      *state.Manager

	// Runtime event system
	runtimeEvents      runtimeevents.Bus
	ownsRuntimeEvents  bool
	runtimeEventLogMu  sync.RWMutex
	runtimeEventLogger *runtimeEventLogger
	runtimeEventLogSub runtimeevents.Subscription
	agentActivityMu    sync.RWMutex
	agentActivity      *agentActivityRecorder
	agentActivitySub   runtimeevents.Subscription
	hooks              *HookManager
	toolPolicy         tools.ToolPolicy

	// Runtime state
	running         atomic.Bool
	contextManager  ContextManager
	contextResolver contextManagerResolver
	fallback        *providers.FallbackChain
	channelManager  interfaces.ChannelManager
	mediaStoreMu    sync.Mutex
	mediaStore      media.MediaStore
	transcriber     asr.Transcriber
	cmdRegistry     *commands.Registry
	mcp             mcpRuntime
	evolution       *evolutionBridge
	hookRuntime     hookRuntime
	steering        *steeringQueue
	steeringRescues sync.Map

	trackedSubagentResults      trackedSubagentResultMailbox
	trackedSubagentWorkerMu     sync.Mutex
	trackedSubagentWorkerCtx    context.Context
	trackedSubagentWorkerCancel context.CancelFunc

	gitWorkspaces gitWorkspaceManager
	pendingSkills sync.Map
	pendingStops  sync.Map
	mu            sync.RWMutex

	// workerSem limits concurrent turn processing workers.
	workerSem chan struct{}

	// activeTurnStates tracks active turns per session to prevent duplicates.
	activeTurnStates   sync.Map
	sessionTurnLocksMu sync.Mutex
	sessionTurnLocks   map[string]*sessionTurnLock
	subTurnCounter     atomic.Int64

	turnSeq atomic.Uint64

	// activeReqMu/activeReqCond/activeReqCount replace sync.WaitGroup to
	// avoid the "WaitGroup is reused before previous Wait has returned" panic
	// that occurs when Add(1) races with a goroutine-launched Wait().
	activeReqMu    sync.Mutex
	activeReqCond  *sync.Cond
	activeReqCount int

	// runtimeGate pauses new root runtime users and drains current users before
	// a registry/provider generation is replaced. This covers the full interval
	// from agent selection through turn/tool completion, not only LLM calls.
	runtimeGateMu           sync.Mutex
	runtimeGateChanged      chan struct{}
	runtimeGatePaused       bool
	runtimeGateStopped      bool
	runtimeStartupBarrier   bool
	runtimeGatePauses       int
	runtimeGateActive       int
	runtimeGateTransitionMu sync.Mutex
	reloadMu                sync.Mutex

	deferEvolutionActivation bool

	runLifecycleMu   sync.Mutex
	runCancel        context.CancelFunc
	runDone          chan struct{}
	runStopRequested bool

	workflowAutomationMu    sync.RWMutex
	workflowAutomationReset chan workflowAutomationResetRequest
	workflowAutomationDone  chan struct{}

	reloadFunc func() error

	providerFactory    func(*config.ModelConfig) (providers.LLMProvider, string, error)
	registryFactory    func(*config.Config, providers.LLMProvider) *AgentRegistry
	recursionInstaller recursionCatalogInstaller
}

// processOptions configures how a message is processed
type processOptions struct {
	Dispatch                DispatchRequest // Normalized routed request boundary for this turn
	SessionKey              string          // Session identifier for history/context
	SessionAliases          []string        // Compatibility aliases for the session key
	Channel                 string          // Target channel for tool execution
	ChatID                  string          // Target chat ID for tool execution
	MessageID               string          // Current inbound platform message ID
	ReplyToMessageID        string          // Current inbound reply target message ID
	SenderID                string          // Current sender ID for dynamic context
	SenderDisplayName       string          // Current sender display name for dynamic context
	UserMessage             string          // User message content (may include prefix)
	ForcedSkills            []string        // Skills explicitly requested for this message
	TurnProfile             config.EffectiveTurnProfile
	SystemPromptOverride    string              // Override the default system prompt (Used by SubTurns)
	SuppressDefaultContext  bool                // Keep only explicit system overlays for isolated turns
	Media                   []string            // media:// refs from inbound message
	InitialSteeringMessages []providers.Message // Steering messages from refactor/agent
	DefaultResponse         string              // Response when LLM returns empty
	PromptCacheKey          string              // Optional provider prompt cache key override
	ModelNameOverride       string              // Optional exact model alias override for this isolated turn
	ModelFallbacksOverride  []string            // Optional exact fallback aliases; non-nil replaces inherited fallbacks
	AccountRefOverride      string              // Optional concrete account or account-router override for this isolated turn
	ReasoningEffortOverride string              // Optional reasoning_effort override for this isolated turn
	EnableSummary           bool                // Whether to trigger summarization
	SendResponse            bool                // Whether to send response via bus
	AllowInterimPicoPublish bool                // Whether pico tool-call interim text can be published when SendResponse is false
	SuppressToolFeedback    bool                // Whether to suppress inline tool feedback messages
	NoHistory               bool                // If true, don't load session history (for heartbeat)
	DisableTools            bool                // If true, no provider or runtime tools are callable this turn
	DisablePromptCache      bool                // If true, omit provider prompt cache key
	SkipInitialSteeringPoll bool                // If true, skip the steering poll at loop start (used by Continue)

	trackedResultOutputOwner *trackedSubagentResultOutputOwner // outer response owner releases late-result pumping
	requireExistingSession   bool                              // strict continuation must not create/admit a missing key
	retainSessionUntilOutput bool                              // exact result continuation keeps ownership through output

	InboundContext  *bus.InboundContext     // Normalized inbound facts for events/hooks
	RouteResult     *routing.ResolvedRoute  // Route decision snapshot for events/hooks
	SessionScope    *session.SessionScope   // Session scope snapshot for events/hooks
	turnReservation *turnState              // exact root/continuation placeholder, process-local only
	resultModelName *string                 // private caller-owned successful model provenance
	resultUsage     *[]workflows.AgentUsage // private caller-owned detached per-model usage
	usageObserver   workflows.AgentUsageObserver
	callAdmission   workflows.AgentCallAdmission
}

type continuationTarget struct {
	SessionKey     string
	Channel        string
	ChatID         string
	InboundContext *bus.InboundContext
}

const (
	defaultResponse            = "The model returned an empty response. This may indicate a provider error or token limit."
	toolLimitResponse          = "I've reached `max_tool_iterations` without a final response. Increase `max_tool_iterations` in config.json if this task needs more tool steps."
	handledToolResponseSummary = "Requested output delivered via tool attachment."
	sessionKeyAgentPrefix      = "agent:"
	pendingTurnPrefix          = "pending-"
	providerReloadGracePeriod  = 30 * time.Second
	metadataKeyMessageKind     = "message_kind"
	metadataKeyToolCalls       = "tool_calls"
	metadataKeyOutboundKind    = "outbound_kind"
	messageKindThought         = "thought"
	messageKindToolFeedback    = "tool_feedback"
	messageKindToolCalls       = "tool_calls"
	outboundKindFinal          = "final"
	metadataKeyAccountID       = "account_id"
	metadataKeyGuildID         = "guild_id"
	metadataKeyTeamID          = "team_id"
	metadataKeyReplyToMessage  = "reply_to_message_id"
	metadataKeyParentPeerKind  = "parent_peer_kind"
	metadataKeyParentPeerID    = "parent_peer_id"
)

// registerSharedTools registers tools that are shared across all agents (web, message, spawn).

func (al *AgentLoop) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, runCancel := context.WithCancel(ctx)
	runDone := make(chan struct{})
	al.runLifecycleMu.Lock()
	if al.runDone != nil {
		al.runLifecycleMu.Unlock()
		runCancel()
		return fmt.Errorf("agent loop is already running")
	}
	if al.runStopRequested {
		al.runLifecycleMu.Unlock()
		runCancel()
		return nil
	}
	al.runCancel = runCancel
	al.runDone = runDone
	al.runLifecycleMu.Unlock()
	defer func() {
		runCancel()
		al.running.Store(false)
		close(runDone)
		al.runLifecycleMu.Lock()
		if al.runDone == runDone {
			al.runCancel = nil
			al.runDone = nil
		}
		al.runLifecycleMu.Unlock()
	}()
	ctx = runCtx

	al.running.Store(true)

	if err := al.ensureHooksInitialized(ctx); err != nil {
		return err
	}
	if err := al.ensureMCPInitialized(ctx); err != nil {
		return err
	}
	stopWorkflowAutomations := al.startWorkflowAutomations(ctx)
	defer stopWorkflowAutomations()

	idleTicker := time.NewTicker(100 * time.Millisecond)
	defer idleTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-idleTicker.C:
			if !al.running.Load() {
				return nil
			}
		case msg, ok := <-al.bus.InboundChan():
			if !ok {
				return nil
			}
			inboundCtx, releaseInbound, err := al.acquireRuntimeUse(ctx)
			if err != nil {
				al.cleanupInboundTurnUX(ctx, msg)
				if ctx.Err() != nil {
					return nil
				}
				logger.WarnCF(
					"agent",
					"Failed to acquire inbound message runtime",
					map[string]any{"error": err.Error()},
				)
				continue
			}
			// Route and admit before workflow-trigger handling. Trigger matching can
			// consume the message and start durable work without entering the normal
			// turn path, so it must not bypass protected-session ownership.
			sessionKey, agentID, routable := al.resolveSteeringTarget(msg)
			if routable {
				if admissionErr := al.admitInboundMessageSession(
					inboundCtx,
					msg,
					sessionKey,
				); admissionErr != nil {
					outboundEnqueued := al.maybePublishError(
						inboundCtx,
						msg.Channel,
						msg.ChatID,
						sessionKey,
						admissionErr,
						&msg.Context,
					)
					if al.channelManager != nil && !outboundEnqueued {
						cleanupTurnUXForMessage(
							inboundCtx,
							al.channelManager,
							msg.Channel,
							msg.ChatID,
							msg.Context.TurnUXID,
						)
					}
					releaseInbound()
					continue
				}
			}
			if al.handleWorkflowTriggers(inboundCtx, msg) {
				al.cleanupInboundTurnUX(inboundCtx, msg)
				releaseInbound()
				continue
			}

			if !routable {
				// Non-routable message (e.g., system) — process immediately.
				// Note: system messages are processed in the main goroutine,
				// so they block the receive loop but guarantee session serialization.
				al.processMessageSync(inboundCtx, msg)
				releaseInbound()
				continue
			}

			// Atomically claim the session key with a unique placeholder sentinel
			// to prevent a TOCTOU race where multiple messages for the same session
			// pass the Load check before either registers.
			// The placeholder ensures GetActiveTurnBySession() never returns nil
			// during turn setup. Each placeholder has a unique turnID to prevent
			// cross-worker cleanup issues.
			placeholder := &turnState{
				turnID:         makePendingTurnID(sessionKey, al.turnSeq.Add(1)),
				agentID:        agentID,
				sessionKey:     sessionKey,
				channel:        msg.Channel,
				chatID:         msg.ChatID,
				turnUXID:       msg.Context.TurnUXID,
				handoffContext: cloneInboundContext(&msg.Context),
				phase:          TurnPhaseSetup,
			}
			unlockSessionTurn := al.lockSessionTurn(sessionKey)
			_, loaded := al.activeTurnStates.LoadOrStore(
				sessionKey,
				placeholder,
			)
			unlockSessionTurn()
			messagePrepared := false
			if loaded {
				// Pre-trigger admission above already fenced this fast path before
				// active-turn lookup. Ownership cannot change afterward, and repeating
				// admission would rewrite metadata/revision for every steering message.
				if al.tryHandleStopCommand(inboundCtx, msg, sessionKey) {
					releaseInbound()
					continue
				}

				msg = al.prepareInboundMessageForAgent(inboundCtx, msg)
				messagePrepared = true

				// ASR and other preparation may be slow. Recheck the active
				// owner afterward: commit to that pinned owner, or claim the
				// session when it has already exited.
				unlockSessionTurn = al.lockSessionTurn(sessionKey)
				current, stillActive := al.activeTurnStates.Load(sessionKey)
				if !stillActive {
					al.activeTurnStates.Store(sessionKey, placeholder)
					unlockSessionTurn()
				} else {
					activeTurnUXID := ""
					activeChannel := ""
					activeChatID := ""
					if active, ok := current.(*turnState); ok {
						activeChannel = active.channel
						activeChatID = active.chatID
						activeTurnUXID = active.turnUXID
					}
					queued, queueDepth, enqueueErr := al.pushSteeringMessage(
						sessionKey,
						providers.Message{
							Role:    "user",
							Content: msg.Content,
							Media:   append([]string(nil), msg.Media...),
						},
					)
					cleanupSteeringUX := false
					if enqueueErr == nil && al.channelManager != nil {
						// Scoped dequeues use this same handoff lock, so the
						// active turn cannot consume the entry first.
						if activeTurnUXID != "" &&
							activeChannel == msg.Channel &&
							activeChatID == msg.ChatID {
							rebindTurnUXForMessage(
								al.channelManager,
								msg.Channel,
								msg.ChatID,
								msg.Context.TurnUXID,
								activeTurnUXID,
							)
						} else {
							// Session scopes such as global/per-peer can steer
							// from another chat. The active turn's eventual
							// outbound cleanup cannot address that secondary
							// chat key, so retire its exact transient UX after
							// the queue commit instead of orphaning it.
							cleanupSteeringUX = true
						}
					}
					unlockSessionTurn()
					if cleanupSteeringUX {
						cleanupTurnUXForMessage(
							inboundCtx,
							al.channelManager,
							msg.Channel,
							msg.ChatID,
							msg.Context.TurnUXID,
						)
					}
					al.reportSteeringEnqueue(
						sessionKey,
						agentID,
						queued,
						queueDepth,
						enqueueErr,
					)
					if enqueueErr != nil {
						if al.channelManager != nil {
							cleanupTurnUXForMessage(
								inboundCtx,
								al.channelManager,
								msg.Channel,
								msg.ChatID,
								msg.Context.TurnUXID,
							)
						}
						logger.WarnCF("agent", "Failed to enqueue steering message",
							map[string]any{
								"error":       enqueueErr.Error(),
								"channel":     msg.Channel,
								"chat_id":     msg.ChatID,
								"session_key": sessionKey,
							})
					}
					releaseInbound()
					continue
				}
			}

			workerCtx, releaseWorker, err := al.retainInboundWorkerRuntime(
				inboundCtx,
				sessionKey,
				placeholder,
				msg,
			)
			if err != nil {
				releaseInbound()
				logger.WarnCF(
					"agent",
					"Failed to retain inbound message runtime",
					map[string]any{
						"error":       err.Error(),
						"channel":     msg.Channel,
						"chat_id":     msg.ChatID,
						"session_key": sessionKey,
					},
				)
				continue
			}

			// Session claimed — spawn a worker goroutine that acquires a semaphore
			// slot. The goroutine is spawned immediately so the main loop keeps
			// draining the inbound channel. The goroutine blocks on the semaphore.
			go func(
				workerCtx context.Context,
				releaseWorker func(),
				sessionKey string,
				m bus.InboundMessage,
				ph *turnState,
				prepared bool,
			) {
				defer releaseWorker()
				// Acquire semaphore slot (blocks if at capacity)
				select {
				case al.workerSem <- struct{}{}:
					// Got slot, start worker
				case <-workerCtx.Done():
					// Context canceled while waiting for a slot — clean up the
					// placeholder to prevent session-level deadlock.
					transferred := al.abandonSessionTurnState(
						workerCtx,
						sessionKey,
						ph,
					)
					if al.channelManager != nil && !transferred {
						cleanupTurnUXForMessage(
							workerCtx,
							al.channelManager,
							m.Channel,
							m.ChatID,
							m.Context.TurnUXID,
						)
					}
					return
				}

				defer func() {
					if r := recover(); r != nil {
						logger.RecoverPanicNoExit(r)
						logger.ErrorCF("agent", "Worker goroutine panicked",
							map[string]any{
								"session_key": sessionKey,
								"channel":     m.Channel,
								"chat_id":     m.ChatID,
								"panic":       fmt.Sprintf("%v", r),
							})
					}
				}()
				defer func() { <-al.workerSem }() // Release slot

				outboundEnqueued := false
				defer func() {
					// If setup never replaced the exact reservation, abandon it
					// before checking for a committed steering handoff. A rescue
					// that accepts the queue also takes ownership of this turn's
					// rebound transient UX.
					transferred := al.abandonSessionTurnState(
						workerCtx,
						sessionKey,
						ph,
					)
					if !transferred {
						transferred = al.rescueOrClearOrphanedSteering(
							workerCtx,
							sessionKey,
							m.Channel,
							m.ChatID,
							&m.Context,
							outboundEnqueued,
						)
					}
					if al.channelManager == nil || transferred {
						return
					}
					if outboundEnqueued {
						// Buffered outbound delivery still owns the reaction
						// and placeholder. Preserve the historical typing
						// stop without racing that delivery.
						invokeTypingStopForMessage(
							al.channelManager,
							m.Channel,
							m.ChatID,
							m.Context.TurnUXID,
						)
						return
					}
					cleanupTurnUXForMessage(
						workerCtx,
						al.channelManager,
						m.Channel,
						m.ChatID,
						m.Context.TurnUXID,
					)
				}()

				if al.takePendingStop(sessionKey) {
					al.releaseSessionTurnState(sessionKey, ph)
					target := &continuationTarget{
						SessionKey:     sessionKey,
						Channel:        m.Channel,
						ChatID:         m.ChatID,
						InboundContext: cloneInboundContext(&m.Context),
					}
					continued, continueErr := al.drainQueuedSteeringContinuations(workerCtx, target)
					if continueErr != nil {
						outboundEnqueued = al.maybePublishError(
							workerCtx,
							m.Channel,
							m.ChatID,
							sessionKey,
							continueErr,
							&m.Context,
						)
						return
					}
					if continued != "" {
						outboundEnqueued = al.publishResponseIfNeeded(
							workerCtx,
							target.Channel,
							target.ChatID,
							target.SessionKey,
							continued,
							target.InboundContext,
						)
					}
					if al.messageToolSentTo(
						target.SessionKey,
						target.Channel,
						target.ChatID,
					) {
						outboundEnqueued = true
					}
					return
				}

				turnCtx := withTurnReservation(workerCtx, ph)
				outboundEnqueued = al.runTurnWithSteering(turnCtx, m, prepared)
			}(workerCtx, releaseWorker, sessionKey, msg, placeholder, messagePrepared)
			releaseInbound()

			// TODO: Re-enable media cleanup after inbound media is properly consumed by the agent.
			// Currently disabled because files are deleted before the LLM can access their content.
			// defer func() {
			// 	if al.mediaStore != nil && msg.MediaScope != "" {
			// 		if releaseErr := al.mediaStore.ReleaseAll(msg.MediaScope); releaseErr != nil {
			// 			logger.WarnCF("agent", "Failed to release media", map[string]any{
			// 				"scope": msg.MediaScope,
			// 				"error": releaseErr.Error(),
			// 			})
			// 		}
			// 	}
			// }()
		}
	}
}

func (al *AgentLoop) cleanupInboundTurnUX(
	ctx context.Context,
	msg bus.InboundMessage,
) {
	if al == nil || al.channelManager == nil {
		return
	}
	msg = bus.NormalizeInboundMessage(msg)
	cleanupTurnUXForMessage(
		ctx,
		al.channelManager,
		msg.Channel,
		msg.ChatID,
		msg.Context.TurnUXID,
	)
}

func (al *AgentLoop) retainInboundWorkerRuntime(
	ctx context.Context,
	sessionKey string,
	placeholder *turnState,
	msg bus.InboundMessage,
) (context.Context, func(), error) {
	workerCtx, releaseWorker, err := al.retainRuntimeUse(ctx)
	if err == nil {
		return workerCtx, releaseWorker, nil
	}
	if !al.abandonSessionTurnState(ctx, sessionKey, placeholder) {
		al.cleanupInboundTurnUX(ctx, msg)
	}
	return workerCtx, releaseWorker, err
}

// processMessageSync processes a message synchronously (for non-routable/system messages).

// runTurnWithSteering runs a complete turn for a message and drains its steering queue.

// maybePublishError publishes an error response unless the error is
// context.Canceled and reports whether outbound delivery accepted it.

// publishResponseOrError publishes the response, or an error message if processing failed.

func (al *AgentLoop) Stop() {
	al.running.Store(false)
	al.runtimeGateMu.Lock()
	al.runtimeGateStopped = true
	al.signalRuntimeGateChangedLocked()
	al.runtimeGateMu.Unlock()
	al.cancelTrackedSubagentWorkers()
	al.runLifecycleMu.Lock()
	al.runStopRequested = true
	cancel := al.runCancel
	al.runLifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// WaitStopped waits for Run and its owned automation controller to return.
func (al *AgentLoop) WaitStopped(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	al.runLifecycleMu.Lock()
	done := al.runDone
	al.runLifecycleMu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close releases quiesced registry, session, context, and generation resources.
// Call after Stop.
func (al *AgentLoop) Close() {
	al.cancelTrackedSubagentWorkers()
	mcpManager := al.mcp.takeManager()
	evolution := al.currentEvolutionBridge()
	if evolution != nil {
		if err := evolution.Close(); err != nil {
			logger.ErrorCF("agent", "Failed to close evolution bridge",
				map[string]any{
					"error": err.Error(),
				})
		}
	}

	if err := closeContextManager(al.contextManager); err != nil {
		logger.ErrorCF("agent", "Failed to close context manager",
			map[string]any{
				"error": err.Error(),
			})
	}
	al.GetRegistry().Close()
	if mcpManager != nil {
		if err := mcpManager.Close(); err != nil {
			logger.ErrorCF("agent", "Failed to close MCP manager",
				map[string]any{
					"error": err.Error(),
				})
		}
	}
	if al.hooks != nil {
		al.hooks.Close()
	}
	al.closeRuntimeEventLogger()
	al.closeAgentActivityRecorder()
	if al.runtimeEvents != nil && al.ownsRuntimeEvents {
		if err := al.runtimeEvents.Close(); err != nil {
			logger.ErrorCF("agent", "Failed to close runtime event bus",
				map[string]any{
					"error": err.Error(),
				})
		}
	}
}

// MountHook registers an in-process hook on the agent loop.

// UnmountHook removes a previously registered in-process hook.

type turnEventScope struct {
	agentID    string
	sessionKey string
	turnID     string
	context    *TurnContext
}

// ReloadProviderAndConfig atomically swaps the provider and config with proper synchronization.
// It uses a context to allow timeout control from the caller.
// Returns an error if the reload fails or context is canceled.
func (al *AgentLoop) ReloadProviderAndConfig(
	ctx context.Context,
	provider providers.LLMProvider,
	cfg *config.Config,
) error {
	_, err := al.reloadProviderAndConfig(ctx, provider, cfg, true)
	return err
}

// ReloadProviderAndConfigRetainingPrevious atomically swaps the provider and
// config but leaves the previous provider open so a multi-service owner can
// either commit the reload or roll it back.
func (al *AgentLoop) ReloadProviderAndConfigRetainingPrevious(
	ctx context.Context,
	provider providers.LLMProvider,
	cfg *config.Config,
) (providers.LLMProvider, error) {
	return al.reloadProviderAndConfig(ctx, provider, cfg, false)
}

// CloseRetainedProvider drains active requests before closing a stateful
// provider retained by ReloadProviderAndConfigRetainingPrevious.
func (al *AgentLoop) CloseRetainedProvider(
	ctx context.Context,
	provider providers.LLMProvider,
) {
	if stateful, ok := provider.(providers.StatefulProvider); ok {
		al.closeReloadedProvider(ctx, stateful)
	}
}

// PauseRuntimeForReload blocks admission of new root turns and waits for
// current runtime users to drain. The returned function must be called to
// resume admission. Nested pauses are supported so a multi-service owner can
// hold the boundary across an AgentLoop swap and service commit or rollback.
func (al *AgentLoop) PauseRuntimeForReload(ctx context.Context) (func(), error) {
	return al.pauseRuntimeUses(ctx)
}

// PauseRuntimeForReloadWithContext pauses and drains root runtime admission,
// then marks runtimeCtx as owned by that pause for synchronous replacement
// setup. The returned resume function revokes the context before reopening
// admission and must always be called.
func (al *AgentLoop) PauseRuntimeForReloadWithContext(
	waitCtx context.Context,
	runtimeCtx context.Context,
) (context.Context, func(), error) {
	return al.pauseRuntimeUsesWithContext(waitCtx, runtimeCtx)
}

func (al *AgentLoop) reloadProviderAndConfig(
	ctx context.Context,
	provider providers.LLMProvider,
	cfg *config.Config,
	closePrevious bool,
) (providers.LLMProvider, error) {
	if runtimeLeaseOwner(ctx) == al {
		return nil, fmt.Errorf("cannot reload provider from an active agent runtime lease")
	}
	al.reloadMu.Lock()
	defer al.reloadMu.Unlock()

	// Validate inputs
	if provider == nil {
		return nil, fmt.Errorf("provider cannot be nil")
	}
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	var registry *AgentRegistry
	func() {
		defer func() {
			if r := recover(); r != nil {
				logger.RecoverPanicNoExit(r)
				logger.ErrorCF("agent", "Panic during registry creation",
					map[string]any{"panic": r})
				registry = nil
			}
		}()
		if al.registryFactory != nil {
			registry = al.registryFactory(cfg, provider)
		} else {
			registry = NewAgentRegistry(cfg, provider)
		}
	}()
	if registry == nil {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("context canceled during registry creation: %w", err)
		}
		return nil, fmt.Errorf("registry creation failed")
	}
	registryInstalled := false
	defer func() {
		if !registryInstalled {
			registry.Close()
		}
	}()

	// Check context again before proceeding
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("context canceled after registry creation: %w", err)
	}

	// Ensure shared tools are re-registered on the new registry
	if err := registerSharedTools(al, cfg, al.bus, registry, provider); err != nil {
		return nil, fmt.Errorf("install reloaded shared tool catalog: %w", err)
	}

	newEvolution, evolutionErr := newEvolutionBridge(registry, cfg, provider)
	if evolutionErr != nil {
		logger.WarnCF("agent", "Failed to reinitialize evolution bridge during reload",
			map[string]any{"error": evolutionErr.Error()})
	}
	evolutionInstalled := false
	defer func() {
		if evolutionInstalled || newEvolution == nil {
			return
		}
		if closeErr := closeAgentResource(
			"reload candidate evolution bridge",
			newEvolution.Close,
		); closeErr != nil {
			logger.WarnCF("agent", "Failed to close reloaded evolution candidate",
				map[string]any{"error": closeErr.Error()})
		}
	}()
	if newEvolution != nil {
		newEvolution.setCurrentCheck(al.isCurrentEvolutionBridge)
		if err := newEvolution.subscribeRuntimeEvents(al.runtimeEvents.Channel()); err != nil {
			logger.WarnCF("agent", "Failed to subscribe reloaded evolution bridge to runtime events",
				map[string]any{"error": err.Error()})
		}
	}

	resumeRuntime, err := al.pauseRuntimeUses(ctx)
	if err != nil {
		return nil, fmt.Errorf("drain active agent runtime before reload: %w", err)
	}
	defer resumeRuntime()

	// SetMediaStore and candidate media application/publication share this
	// boundary, so no setter can retain the retired registry or overtake a
	// candidate swap. The runtime gate is already paused and drained here.
	var oldRegistry *AgentRegistry
	var oldEvolution *evolutionBridge
	var oldContextManager ContextManager
	func() {
		al.mediaStoreMu.Lock()
		defer al.mediaStoreMu.Unlock()

		mediaStore := al.mediaStoreSnapshot()
		setAgentRegistryMediaStore(registry, mediaStore)

		// Atomically swap the config and registry under the same lock order used
		// by SetMediaStore: mediaStoreMu then al.mu.
		al.mu.Lock()
		oldRegistry = al.registry
		oldEvolution = al.evolution
		oldContextManager = al.contextManager

		al.cfg = cfg
		al.registry = registry
		registryInstalled = true
		al.evolution = newEvolution
		evolutionInstalled = true

		// Also update fallback chain with new config; rebuild rate limiter registry.
		newRL := providers.NewRateLimiterRegistry()
		for _, agentID := range registry.ListAgentIDs() {
			if agent, ok := registry.GetAgent(agentID); ok {
				newRL.RegisterCandidates(agent.Candidates)
				newRL.RegisterCandidates(agent.LightCandidates)
				if agent.AccountRouter != nil {
					for _, account := range agent.AccountRouter.Accounts {
						newRL.RegisterCandidates(account.Candidates)
					}
				}
			}
		}
		al.fallback = providers.NewFallbackChain(providers.NewCooldownTracker(), newRL)
		al.mu.Unlock()
	}()

	// Context-manager factories derive per-agent resources from the current
	// registry. Rebuild after the registry/config swap while the runtime gate
	// remains paused, closing the previous generation first so Seahorse cannot
	// retain stale engines or SQLite handles after workspace/topology changes.
	if err := closeContextManager(oldContextManager); err != nil {
		logger.WarnCF("agent", "Failed to close previous context manager during reload",
			map[string]any{"error": err.Error()})
	}
	newContextManager := al.resolveContextManagerWithContext(ctx)
	al.mu.Lock()
	al.contextManager = newContextManager
	al.mu.Unlock()

	if oldEvolution != nil {
		if err := oldEvolution.Close(); err != nil {
			logger.WarnCF("agent", "Failed to close previous evolution bridge during reload",
				map[string]any{"error": err.Error()})
		}
	}
	if newEvolution != nil {
		if err := newEvolution.activate(al); err != nil {
			logger.WarnCF("agent", "Failed to activate reloaded evolution bridge",
				map[string]any{"error": err.Error()})
		}
	}
	al.refreshRuntimeEventLogger(cfg)

	oldProvider, hasOldProvider := extractProvider(oldRegistry)
	oldMCPManager := al.mcp.reset()
	al.hookRuntime.reset(al)
	configureHookManagerFromConfig(al.hooks, cfg)
	if err := al.ensureHooksInitialized(ctx); err != nil {
		logger.WarnCF("agent", "Configured hooks failed to reinitialize after reload",
			map[string]any{"error": err.Error()})
	}
	// Runtime uses are paused and the old context/evolution owners are closed.
	// Release every old compatibility-source tool lease before its borrowed MCP
	// manager or provider generation disappears.
	oldRegistry.Close()
	if oldMCPManager != nil {
		if err := oldMCPManager.Close(); err != nil {
			logger.WarnCF("agent", "Failed to close previous MCP manager during reload",
				map[string]any{"error": err.Error()})
		}
	}
	if err := al.ensureMCPInitializedForGeneration(ctx, cfg, registry); err != nil {
		logger.WarnCF("agent", "MCP failed to reinitialize after reload",
			map[string]any{"error": err.Error()})
	}
	// Close old provider after releasing the lock
	// This prevents blocking readers while closing
	if closePrevious && hasOldProvider {
		if stateful, ok := oldProvider.(providers.StatefulProvider); ok {
			al.closeReloadedProvider(ctx, stateful)
		}
	}

	logger.InfoCF("agent", "Provider and config reloaded successfully",
		map[string]any{
			"model": cfg.Agents.Defaults.GetModelName(),
		})

	if !hasOldProvider {
		return nil, nil
	}
	return oldProvider, nil
}

// GetRegistry returns the current registry (thread-safe)

// GetConfig returns the current config (thread-safe)

// SetMediaStore injects a MediaStore for media lifecycle management.

// SetTranscriber injects a voice transcriber for agent-level audio transcription.

// SetReloadFunc sets the callback function for triggering config reload.

var audioAnnotationRe = regexp.MustCompile(`\[(voice|audio)(?::[^\]]*)?\]`)

// transcribeAudioInMessage resolves audio media refs, transcribes them, and
// replaces audio annotations in msg.Content with the transcribed text.
// Returns the (possibly modified) message and true if audio was transcribed.

// sendTranscriptionFeedback sends feedback to the user with the result of
// audio transcription if the option is enabled. It uses Manager.SendMessage
// which executes synchronously (rate limiting, splitting, retry) so that
// ordering with the subsequent placeholder is guaranteed.

// inferMediaType determines the media type ("image", "audio", "video", "file")
// from a filename and MIME content type.

// RecordLastChannel records the last active channel for this workspace.
// This uses the atomic state save mechanism to prevent data loss on crash.

// RecordLastChatID records the last active chat ID for this workspace.
// This uses the atomic state save mechanism to prevent data loss on crash.

// ProcessHeartbeat processes a heartbeat request without session history.
// Each heartbeat is independent and doesn't accumulate context.

// runAgentLoop remains the top-level shell that starts a turn and publishes
// any post-turn work. runTurn owns the full turn lifecycle.
func (al *AgentLoop) runAgentLoop(
	ctx context.Context,
	agent *AgentInstance,
	opts processOptions,
) (string, error) {
	leaseCtx, releaseRuntime, err := al.acquireRuntimeUse(ctx)
	if err != nil {
		return "", err
	}
	defer releaseRuntime()
	ctx = leaseCtx
	if agent == nil {
		return "", fmt.Errorf("agent is required")
	}
	agent, err = al.resolveCurrentRuntimeAgent(agent)
	if err != nil {
		return "", err
	}

	opts = normalizeProcessOptions(opts)
	opts, err = resolveTurnProfileOptions(al.GetConfig(), opts)
	if err != nil {
		return "", err
	}
	var admissionErr error
	if opts.requireExistingSession {
		admissionErr = validateTrackedSubagentExistingSession(
			ctx,
			agent,
			opts.Dispatch.SessionKey,
			opts.Dispatch.SessionScope,
		)
	} else {
		admissionErr = admitSessionMetadata(
			ctx,
			agent.Sessions,
			opts.Dispatch.SessionKey,
			opts.Dispatch.SessionScope,
			opts.Dispatch.SessionAliases,
			agent.ID,
		)
	}
	if admissionErr != nil {
		return "", fmt.Errorf("admit live session scope: %w", admissionErr)
	}

	// Record last channel for heartbeat notifications (skip internal channels and cli)
	if opts.Dispatch.Channel() != "" &&
		opts.Dispatch.ChatID() != "" &&
		!constants.IsInternalChannel(opts.Dispatch.Channel()) {
		channelKey := fmt.Sprintf("%s:%s", opts.Dispatch.Channel(), opts.Dispatch.ChatID())
		if recordErr := al.RecordLastChannel(channelKey); recordErr != nil {
			logger.WarnCF(
				"agent",
				"Failed to record last channel",
				map[string]any{"error": recordErr.Error()},
			)
		}
	}

	turnScope := al.newTurnEventScope(
		agent.ID,
		opts.Dispatch.SessionKey,
		newTurnContext(opts.Dispatch.InboundContext, opts.Dispatch.RouteResult, opts.Dispatch.SessionScope),
	)
	ts := newTurnState(agent, opts, turnScope)
	outputOwner := opts.trackedResultOutputOwner
	if outputOwner == nil {
		outputOwner = &trackedSubagentResultOutputOwner{}
		defer outputOwner.release(al)
	}
	outputOwner.record(al, ts.turnID, opts.Dispatch.SessionKey)
	if opts.resultUsage != nil {
		defer func() {
			*opts.resultUsage = cloneWorkflowAgentUsage(ts.workflowAgentUsageSnapshot())
		}()
	}
	pipeline := NewPipeline(al)
	result, err := al.runTurn(ctx, ts, pipeline)
	if err != nil {
		return "", err
	}
	if result.status == TurnEndStatusAborted {
		return "", nil
	}

	for _, followUp := range result.followUps {
		if pubErr := al.bus.PublishInbound(ctx, followUp); pubErr != nil {
			logger.WarnCF("agent", "Failed to publish follow-up after turn",
				map[string]any{
					"turn_id": ts.turnID,
					"error":   pubErr.Error(),
				})
		}
	}

	if opts.SendResponse && result.finalContent != "" {
		agentID, sessionKey, scope := outboundTurnMetadata(
			agent.ID,
			opts.Dispatch.SessionKey,
			opts.Dispatch.SessionScope,
		)
		msg := bus.OutboundMessage{
			Context: outboundContextFromInbound(
				opts.Dispatch.InboundContext,
				opts.Dispatch.Channel(),
				opts.Dispatch.ChatID(),
				opts.Dispatch.ReplyToMessageID(),
			),
			AgentID:      agentID,
			SessionKey:   sessionKey,
			Scope:        scope,
			Content:      result.finalContent,
			ContextUsage: computeContextUsage(agent, opts.Dispatch.SessionKey),
		}
		if modelName := strings.TrimSpace(result.modelName); modelName != "" {
			if msg.Context.Raw == nil {
				msg.Context.Raw = make(map[string]string, 1)
			}
			msg.Context.Raw["model_name"] = modelName
		}
		markFinalOutbound(&msg)
		al.bus.PublishOutbound(ctx, msg)
	}

	if result.finalContent != "" {
		responsePreview := utils.Truncate(result.finalContent, 120)
		logger.InfoCF("agent", fmt.Sprintf("Response: %s", responsePreview),
			map[string]any{
				"agent_id":     agent.ID,
				"session_key":  opts.Dispatch.SessionKey,
				"iterations":   ts.currentIteration(),
				"final_length": len(result.finalContent),
			})
	}
	if opts.resultModelName != nil {
		*opts.resultModelName = strings.TrimSpace(result.modelName)
	}
	if opts.resultUsage != nil {
		*opts.resultUsage = cloneWorkflowAgentUsage(result.usage)
	}

	return result.finalContent, nil
}

func (al *AgentLoop) resolveCurrentRuntimeAgent(
	captured *AgentInstance,
) (*AgentInstance, error) {
	registry := al.GetRegistry()
	if registry == nil {
		return nil, fmt.Errorf("agent registry not configured")
	}
	if captured != nil && captured.ID != "" {
		current, ok := registry.GetAgent(captured.ID)
		if !ok || current == nil {
			return nil, fmt.Errorf("agent %q is not present in the current runtime", captured.ID)
		}
		return current, nil
	}
	for _, agentID := range registry.ListAgentIDs() {
		current, ok := registry.GetAgent(agentID)
		if ok && current == captured {
			return current, nil
		}
	}
	return nil, fmt.Errorf("captured agent is not present in the current runtime")
}

// selectCandidates returns the model candidates and resolved model name to use
// for a conversation turn. When model routing is configured and the incoming
// message scores below the complexity threshold, it returns the light model
// candidates instead of the primary ones.
//
// The returned (candidates, model) pair is used for all LLM calls within one
// turn — tool follow-up iterations use the same tier as the initial call so
// that a multi-step tool chain doesn't switch models mid-way.

// resolveContextManager selects the ContextManager implementation based on config.

// GetStartupInfo returns information about loaded tools and skills for logging.

// formatMessagesForLog formats messages for logging

// formatToolsForLog formats tool definitions for logging

// summarizeSession summarizes the conversation history for a session.
// findNearestUserMessage finds the nearest user message to the given index.
// It searches backward first, then forward if no user message is found.
// retryLLMCall calls the LLM with retry logic.
// summarizeBatch summarizes a batch of messages.
// estimateTokens estimates the number of tokens in a message list.
// Counts Content, ToolCalls arguments, and ToolCallID metadata so that
// tool-heavy conversations are not systematically undercounted.

// askSideQuestion handles /btw commands by creating an isolated provider instance
// that doesn't share state with the main conversation provider.

// shallowCloneLLMOptions creates a shallow copy of LLM options map.
// Note: This is a shallow copy - nested maps/slices are shared.

// hasMediaRefs checks if any message has media references.

// isolatedSideQuestionProvider creates a separate provider instance for /btw commands
// to avoid sharing state with the main conversation provider.

// sideQuestionModelConfig resolves the model config for side questions.

// sideQuestionModelName determines which model name to use for side questions.

// modelNameFromIdentityKey extracts the model name from an identity key.

// closeProviderIfStateful closes a provider if it implements StatefulProvider.

// makePendingTurnID generates a unique turn ID for placeholder turns.
// Format: "pending-{sessionKey}-{sequence}"

// isNativeSearchProvider reports whether the given LLM provider implements
// NativeSearchCapable and returns true for SupportsNativeSearch.

// filterClientWebSearch returns a copy of tools with the client-side
// web_search tool removed. Used when native provider search is preferred.

// Helper to extract provider from registry for cleanup
