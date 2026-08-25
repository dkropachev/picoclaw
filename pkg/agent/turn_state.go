// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

// =============================================================================
// TurnPhase - represents the current phase of a turn
// =============================================================================

type TurnPhase string

const (
	TurnPhaseSetup      TurnPhase = "setup"
	TurnPhaseRunning    TurnPhase = "running"
	TurnPhaseTools      TurnPhase = "tools"
	TurnPhaseFinalizing TurnPhase = "finalizing"
	TurnPhaseCompleted  TurnPhase = "completed"
	TurnPhaseAborted    TurnPhase = "aborted"
)

// =============================================================================
// Control signals - returned from Pipeline methods to drive runTurn's coordinator loop
// =============================================================================

type Control int

const (
	// ControlContinue tells the coordinator to jump back to the top of the turn loop
	// (equivalent to the original "goto turnLoop").
	ControlContinue Control = iota
	// ControlBreak tells the coordinator to exit the turn loop and proceed to Finalize.
	ControlBreak
	// ControlToolLoop tells the coordinator to execute the tool loop.
	ControlToolLoop
)

// ToolControl signals returned from ExecuteTools to drive tool loop iteration.
type ToolControl int

const (
	// ToolControlContinue tells the tool loop to jump to the next iteration
	// (pendingMessages arrived, SubTurn results, etc.).
	ToolControlContinue ToolControl = iota
	// ToolControlBreak tells the tool loop to exit and return to the coordinator.
	ToolControlBreak
	// ToolControlFinalize tells the coordinator that all tool responses were
	// handled and the turn should finalize without another LLM call.
	ToolControlFinalize
)

// LLMPhase indicates which phase the turn is executing in.
type LLMPhase int

const (
	LLMPhaseSetup LLMPhase = iota
	LLMPhasePreLLM
	LLMPhaseLLMCall
	LLMPhaseProcessing
	LLMPhaseToolLoop
	LLMPhaseTools
	LLMPhaseFinalizing
	LLMPhaseCompleted
	LLMPhaseAborted
)

// =============================================================================
// turnResult - returned from runTurn
// =============================================================================

type turnResult struct {
	finalContent string
	modelName    string
	usage        []workflows.AgentUsage
	status       TurnEndStatus
	followUps    []bus.InboundMessage
}

// =============================================================================
// ActiveTurnInfo - public info about an active turn
// =============================================================================

type ActiveTurnInfo struct {
	TurnID       string
	AgentID      string
	SessionKey   string
	Channel      string
	ChatID       string
	UserMessage  string
	Phase        TurnPhase
	Iteration    int
	StartedAt    time.Time
	Depth        int
	ParentTurnID string
	ChildTurnIDs []string
}

// =============================================================================
// turnExecution - mutable state that persists across turn loop iterations
// =============================================================================

type turnExecution struct {
	// Core message state (accumulates throughout the turn)
	messages         []providers.Message // built from ContextBuilder, grows per-iteration
	pendingMessages  []providers.Message // steering/SubTurn messages awaiting injection
	history          []providers.Message // from ContextManager.Assemble
	summary          string
	currentTurnStart int

	// Turn output
	finalContent string

	// Iteration tracking
	iteration int

	// Per-iteration state set by Pipeline.PreLLM
	activeCandidates  []providers.FallbackCandidate
	activeModel       string
	activeModelConfig *config.ModelConfig
	activeProvider    providers.LLMProvider
	usedLight         bool
	accountRouter     *accountrouter.Router
	routerSelection   accountrouter.Selection

	// LLM call per-iteration state
	response            *providers.LLMResponse
	normalizedToolCalls []providers.ToolCall
	allResponsesHandled bool
	streamingPublisher  *streamingChunkPublisher
	streamingFallback   bool
	suppressReasoning   bool
	callMessages        []providers.Message
	providerToolDefs    []providers.ToolDefinition
	visibleToolSurface  string
	llmModel            string
	llmModelName        string
	llmOpts             map[string]any
	gracefulTerminal    bool
	useNativeSearch     bool

	// Phase tracking
	phase LLMPhase

	// Abort signaling for coordinator (set by Pipeline methods)
	abortedByHardAbort bool // true when hard abort triggered during LLM/tools
	abortedByHook      bool // true when HookActionAbortTurn triggered
}

// newTurnExecution creates a turnExecution initialized from turnState and options.
func newTurnExecution(
	agent *AgentInstance,
	opts processOptions,
	history []providers.Message,
	summary string,
	messages []providers.Message,
) *turnExecution {
	return &turnExecution{
		history:          history,
		summary:          summary,
		messages:         messages,
		pendingMessages:  append([]providers.Message(nil), opts.InitialSteeringMessages...),
		currentTurnStart: len(messages),
		iteration:        0,
		phase:            LLMPhaseSetup,
	}
}

// =============================================================================
// turnState - the full state for a turn, constructed once per turn
// =============================================================================

type turnState struct {
	mu sync.RWMutex

	agent   *AgentInstance
	opts    processOptions
	profile config.EffectiveTurnProfile
	scope   turnEventScope

	turnID            string
	agentID           string
	sessionKey        string
	activeSkills      []string
	attemptedSkills   []string
	skillContextTrace []SkillContextSnapshot
	toolKinds         []string
	toolExecutions    []ToolExecutionRecord
	turnCtx           *TurnContext

	channel        string
	chatID         string
	turnUXID       string
	handoffContext *bus.InboundContext
	workspace      string
	userMessage    string
	media          []string

	phase        TurnPhase
	iteration    int
	startedAt    time.Time
	finalContent string

	followUps []bus.InboundMessage

	gracefulInterrupt     bool
	gracefulInterruptHint string
	gracefulTerminalUsed  bool
	hardAbort             bool
	providerCancel        context.CancelFunc
	turnCancel            context.CancelFunc

	restorePointHistory []providers.Message
	restorePointSummary string
	persistedMessages   []providers.Message

	// SubTurn support (from HEAD)
	depth                int                    // SubTurn depth (0 for root turn)
	parentTurnID         string                 // Parent turn ID (empty for root turn)
	childTurnIDs         []string               // Child turn IDs
	childTurns           map[string]*turnState  // Exact retained child graph for cascades
	pendingResults       chan *tools.ToolResult // Channel for SubTurn results
	concurrencySem       chan struct{}          // Semaphore for limiting concurrent SubTurns
	runAdmitted          bool                   // One runTurn admission, guarded by mu
	terminalClaimed      bool                   // Linearizes terminal commit against admission/abort
	isFinished           atomic.Bool            // Whether this turn has finished
	session              session.SessionStore   // Session store reference
	initialHistoryLength int                    // Snapshot of history length at turn start

	// Additional SubTurn fields
	ctx              context.Context    // Context for this turn
	cancelFunc       context.CancelFunc // Cancel function for this turn's context
	critical         bool               // Whether this SubTurn should continue after parent ends
	parentTurnState  *turnState         // Reference to parent turnState
	parentEnded      atomic.Bool        // Whether parent has ended
	finishOnce       sync.Once          // Owns the complete terminal transition
	finishedChan     chan struct{}      // Closed when turn finishes
	terminalStatus   TurnEndStatus      // Final status, guarded by mu
	cancelRequested  bool               // Rejects child attachment during error/abort cascade
	cancelDispatched bool               // Prevents repeated non-hard subtree cancellation

	// Token budget tracking
	tokenBudget      *atomic.Int64        // Shared token budget counter
	lastFinishReason string               // Last LLM finish_reason
	lastUsage        *providers.UsageInfo // Last LLM usage info
	usage            *workflowAgentUsageAccumulator

	// Back-reference to the owning AgentLoop, bound before active publication.
	al *AgentLoop
}

// =============================================================================
// turnState constructors and active turn management
// =============================================================================

type sessionTurnLock struct {
	mu   sync.Mutex
	refs int
}

type turnReservationContextKey struct{}

const defaultPendingSubTurnResultBuffer = 16

func withTurnReservation(ctx context.Context, reservation *turnState) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if reservation == nil {
		return ctx
	}
	return context.WithValue(ctx, turnReservationContextKey{}, reservation)
}

func turnReservationFromContext(ctx context.Context) *turnState {
	if ctx == nil {
		return nil
	}
	reservation, _ := ctx.Value(turnReservationContextKey{}).(*turnState)
	return reservation
}

// lockSessionTurn serializes the short ownership handoff between the active
// turn registry and the scoped steering queue. Without this boundary, an
// inbound message can observe a turn that is just about to unregister, enqueue
// behind it, and be left with no worker responsible for draining the queue.
func (al *AgentLoop) lockSessionTurn(sessionKey string) func() {
	al.sessionTurnLocksMu.Lock()
	if al.sessionTurnLocks == nil {
		al.sessionTurnLocks = make(map[string]*sessionTurnLock)
	}
	keyLock := al.sessionTurnLocks[sessionKey]
	if keyLock == nil {
		keyLock = &sessionTurnLock{}
		al.sessionTurnLocks[sessionKey] = keyLock
	}
	keyLock.refs++
	al.sessionTurnLocksMu.Unlock()

	keyLock.mu.Lock()
	return func() {
		keyLock.mu.Unlock()
		al.sessionTurnLocksMu.Lock()
		keyLock.refs--
		if keyLock.refs == 0 {
			delete(al.sessionTurnLocks, sessionKey)
		}
		al.sessionTurnLocksMu.Unlock()
	}
}

func newTurnState(agent *AgentInstance, opts processOptions, scope turnEventScope) *turnState {
	ts := &turnState{
		agent:        agent,
		opts:         opts,
		profile:      opts.TurnProfile,
		scope:        scope,
		turnID:       scope.turnID,
		agentID:      agent.ID,
		sessionKey:   opts.Dispatch.SessionKey,
		activeSkills: activeSkillNames(agent, opts),
		turnCtx:      cloneTurnContext(scope.context),
		channel:      opts.Dispatch.Channel(),
		chatID:       opts.Dispatch.ChatID(),
		turnUXID:     opts.Dispatch.TurnUXID(),
		workspace:    agent.Workspace,
		userMessage:  opts.Dispatch.UserMessage,
		media:        append([]string(nil), opts.Dispatch.Media...),
		phase:        TurnPhaseSetup,
		startedAt:    time.Now(),
		usage:        newWorkflowAgentUsageAccumulator(opts.usageObserver),
		finishedChan: make(chan struct{}),
	}

	// Bind the session store. restorePointHistory/restorePointSummary are the
	// authoritative rollback source; initialHistoryLength remains telemetry for
	// compatibility with existing turn snapshots.
	if agent != nil && agent.Sessions != nil {
		ts.session = agent.Sessions
		history := agent.Sessions.GetHistory(opts.Dispatch.SessionKey)
		ts.initialHistoryLength = len(history)
		ts.restorePointHistory = append([]providers.Message(nil), history...)
		ts.restorePointSummary = agent.Sessions.GetSummary(opts.Dispatch.SessionKey)
	}

	return ts
}

// prepareTurnState binds every supervisor-owned dependency before a turn can
// become visible in activeTurnStates. It is idempotent so spawnSubTurn can
// prepare a child before attaching it and runTurn can repeat the invariant at
// its own publication boundary.
func (al *AgentLoop) prepareTurnState(ts *turnState) {
	if al == nil || ts == nil {
		return
	}
	runtimeCfg := al.getSubTurnConfig()

	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.al = al
	if ts.pendingResults == nil {
		ts.pendingResults = make(chan *tools.ToolResult, defaultPendingSubTurnResultBuffer)
	}
	if ts.concurrencySem == nil {
		ts.concurrencySem = make(chan struct{}, runtimeCfg.maxConcurrent)
	}
	if ts.finishedChan == nil {
		ts.finishedChan = make(chan struct{})
	}
}

func (al *AgentLoop) newAdHocRootTurnState(ctx context.Context) *turnState {
	if ctx == nil {
		ctx = context.Background()
	}
	ts := &turnState{
		ctx:     ctx,
		turnID:  "adhoc-root",
		depth:   0,
		session: nil,
	}
	al.prepareTurnState(ts)
	return ts
}

// attachChildTurn publishes one exact child while holding the parent's state
// lock. Abort/error terminalization takes the same lock before setting its
// rejection markers, so a child is either fully attached and discoverable by
// cascade traversal or rejected before publication.
func (al *AgentLoop) attachChildTurn(parent, child *turnState) bool {
	if al == nil || parent == nil || child == nil {
		return false
	}
	if child.sessionKey == "" || child.turnID == "" ||
		child.parentTurnState != parent || child.parentTurnID != parent.turnID {
		return false
	}

	parent.mu.Lock()
	defer parent.mu.Unlock()
	if parent.terminalClaimed || parent.isFinished.Load() || parent.cancelRequested || parent.hardAbort {
		return false
	}
	if _, loaded := al.activeTurnStates.LoadOrStore(child.sessionKey, child); loaded {
		return false
	}
	if parent.childTurns == nil {
		parent.childTurns = make(map[string]*turnState)
	}
	parent.childTurns[child.sessionKey] = child
	parent.childTurnIDs = append(parent.childTurnIDs, child.sessionKey)
	return true
}

func (ts *turnState) acceptsChildren() bool {
	if ts == nil {
		return false
	}
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return !ts.terminalClaimed && !ts.isFinished.Load() && !ts.cancelRequested && !ts.hardAbort
}

func (ts *turnState) admitRun() bool {
	if ts == nil {
		return false
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.runAdmitted || ts.terminalClaimed || ts.isFinished.Load() {
		return false
	}
	ts.runAdmitted = true
	return true
}

func (ts *turnState) releaseRunAdmission() {
	if ts == nil {
		return
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if !ts.terminalClaimed && !ts.isFinished.Load() {
		ts.runAdmitted = false
	}
}

func (al *AgentLoop) registerActiveTurn(ts *turnState) bool {
	unlock := al.lockSessionTurn(ts.sessionKey)
	defer unlock()

	actual, loaded := al.activeTurnStates.Load(ts.sessionKey)
	reservation := ts.opts.turnReservation
	if reservation != nil && reservation.sessionKey == ts.sessionKey {
		if !loaded || actual != reservation {
			return false
		}
		al.activeTurnStates.Store(ts.sessionKey, ts)
		return true
	}
	if !loaded {
		al.activeTurnStates.Store(ts.sessionKey, ts)
		return true
	}
	if actual == ts {
		return true
	}
	return false
}

func (al *AgentLoop) clearActiveTurn(ts *turnState) {
	al.releaseSessionTurnState(ts.sessionKey, ts)
}

func (al *AgentLoop) releaseSessionTurnState(sessionKey string, expected *turnState) {
	unlock := al.lockSessionTurn(sessionKey)
	defer unlock()
	if expected == nil {
		return
	}
	if actual, ok := al.activeTurnStates.Load(sessionKey); ok && actual == expected {
		al.activeTurnStates.Delete(sessionKey)
	}
}

// abandonSessionTurnState releases an unstarted reservation. If another
// inbound message already committed to its steering queue, a live runtime
// schedules a continuation so that message does not remain stranded behind
// the failed placeholder. During shutdown the orphaned queue is cleared.
func (al *AgentLoop) abandonSessionTurnState(
	ctx context.Context,
	sessionKey string,
	expected *turnState,
) bool {
	if expected == nil {
		return false
	}

	unlock := al.lockSessionTurn(sessionKey)
	actual, loaded := al.activeTurnStates.Load(sessionKey)
	if !loaded || actual != expected {
		unlock()
		return false
	}

	al.activeTurnStates.Delete(sessionKey)
	unlock()

	return al.rescueOrClearOrphanedSteering(
		ctx,
		sessionKey,
		expected.channel,
		expected.chatID,
		expected.handoffContext,
	)
}

type steeringRescueRequest struct {
	parentContext    context.Context
	channel          string
	chatID           string
	inboundContext   *bus.InboundContext
	outboundEnqueued bool
}

// steeringRescueState is guarded by the matching session-turn lock. Keeping
// the pending ownership records beside the marker prevents a finishing rescue
// from overlooking a later abandonment that observed the marker.
type steeringRescueState struct {
	pending []steeringRescueRequest
}

func (al *AgentLoop) rescueOrClearOrphanedSteering(
	ctx context.Context,
	sessionKey, channel, chatID string,
	inboundContext *bus.InboundContext,
	bufferedOutbound ...bool,
) bool {
	if al.steering == nil {
		return false
	}

	unlock := al.lockSessionTurn(sessionKey)
	if _, active := al.activeTurnStates.Load(sessionKey); active {
		unlock()
		return false
	}
	queueDepth := al.steering.lenScope(sessionKey)
	if queueDepth == 0 {
		unlock()
		return false
	}
	canResume := al.running.Load() && (ctx == nil || ctx.Err() == nil)
	if !canResume {
		al.steering.clearScope(sessionKey)
		unlock()
		return false
	}

	request := steeringRescueRequest{
		parentContext:  ctx,
		channel:        channel,
		chatID:         chatID,
		inboundContext: cloneInboundContext(inboundContext),
		outboundEnqueued: len(bufferedOutbound) > 0 &&
			bufferedOutbound[0],
	}
	if current, loaded := al.steeringRescues.Load(sessionKey); loaded {
		state, ok := current.(*steeringRescueState)
		if !ok || state == nil {
			// Heal an unexpected legacy/corrupt marker while ownership is
			// serialized. No running supervisor can safely own this request.
			al.steeringRescues.Delete(sessionKey)
		} else {
			state.pending = append(state.pending, request)
			unlock()
			// The existing supervisor now explicitly owns both this queue
			// handoff and its exact abandoned UX context.
			return true
		}
	}

	state := &steeringRescueState{}
	al.steeringRescues.Store(sessionKey, state)
	unlock()
	go al.runSteeringRescue(
		sessionKey,
		state,
		request,
		request.outboundEnqueued,
	)
	return true
}

func (al *AgentLoop) runSteeringRescue(
	sessionKey string,
	state *steeringRescueState,
	request steeringRescueRequest,
	inheritedOutbound bool,
) {
	outboundEnqueued := inheritedOutbound
	retryRemainingQueue := true
	defer func() {
		if recovered := recover(); recovered != nil {
			retryRemainingQueue = false
			logger.RecoverPanicNoExit(recovered)
			logger.ErrorCF(
				"agent",
				"Steering rescue panicked",
				map[string]any{
					"session_key": sessionKey,
					"channel":     request.channel,
					"chat_id":     request.chatID,
				},
			)
		}
		al.finishSteeringRescue(
			sessionKey,
			state,
			request,
			outboundEnqueued,
			retryRemainingQueue,
		)
	}()

	resumeCtx := context.Background()
	if request.parentContext != nil {
		resumeCtx = context.WithoutCancel(request.parentContext)
	}
	resumeCtx, cancel := context.WithTimeout(resumeCtx, 30*time.Second)
	defer cancel()

	response, err := al.continueWithInboundContext(
		resumeCtx,
		sessionKey,
		request.channel,
		request.chatID,
		request.inboundContext,
	)
	if errors.Is(err, errSessionTurnAlreadyOwned) {
		// Another live owner won the session claim and therefore owns the
		// committed queue. This is a handoff, not a user-facing processing
		// failure.
		return
	}
	if err != nil {
		outboundEnqueued = al.maybePublishError(
			resumeCtx,
			request.channel,
			request.chatID,
			sessionKey,
			err,
			request.inboundContext,
		)
		// Continue either failed before dequeue or consumed the attempted
		// steering and failed while processing it. In both cases the error
		// response is terminal for this abandoned ownership request; retrying
		// an untouched queue here would otherwise create an unbounded loop.
		retryRemainingQueue = false
		return
	}

	target := &continuationTarget{
		SessionKey:     sessionKey,
		Channel:        request.channel,
		ChatID:         request.chatID,
		InboundContext: cloneInboundContext(request.inboundContext),
	}
	continued, continueErr := al.drainQueuedSteeringContinuations(
		resumeCtx,
		target,
	)
	if continued != "" {
		response = continued
	}
	if errors.Is(continueErr, errSessionTurnAlreadyOwned) {
		// A later inbound claimed the idle session between continuations. It
		// owns the remaining queue; preserve any response already completed by
		// this rescue without publishing the ownership race as an error.
		continueErr = nil
	}
	if continueErr != nil {
		retryRemainingQueue = false
		if resumeCtx.Err() == nil {
			outboundEnqueued = al.maybePublishError(
				resumeCtx,
				request.channel,
				request.chatID,
				sessionKey,
				continueErr,
				request.inboundContext,
			) || outboundEnqueued
			logger.WarnCF(
				"agent",
				"Failed to resume steering after reservation abandonment",
				map[string]any{
					"error":       continueErr.Error(),
					"session_key": sessionKey,
					"queue_depth": al.pendingSteeringCountForScope(sessionKey),
				},
			)
		}
	}

	if response != "" {
		outboundEnqueued = al.publishResponseIfNeeded(
			resumeCtx,
			request.channel,
			request.chatID,
			sessionKey,
			response,
			request.inboundContext,
		) || outboundEnqueued
	}
	if al.messageToolSentTo(
		sessionKey,
		request.channel,
		request.chatID,
	) {
		outboundEnqueued = true
	}
}

func (al *AgentLoop) finishSteeringRescue(
	sessionKey string,
	state *steeringRescueState,
	request steeringRescueRequest,
	outboundEnqueued bool,
	retryRemainingQueue bool,
) {
	var (
		nextRequest      *steeringRescueRequest
		carryOutbound    bool
		cleanupCurrent   = true
		pendingToCleanup []steeringRescueRequest
	)

	unlock := al.lockSessionTurn(sessionKey)
	current, loaded := al.steeringRescues.Load(sessionKey)
	if !loaded || current != state {
		unlock()
		al.cleanupSteeringRescueUX(request, outboundEnqueued)
		return
	}

	_, active := al.activeTurnStates.Load(sessionKey)
	queueDepth := al.steering.lenScope(sessionKey)
	canResume := al.running.Load()
	switch {
	case active:
		// The live owner owns every remaining queue entry. No abandoned UX
		// record can be addressed by that owner's exact outbound identity.
		pendingToCleanup = append(pendingToCleanup, state.pending...)
		state.pending = nil
		al.steeringRescues.Delete(sessionKey)
	case queueDepth == 0:
		// Any request appended after this rescue's last dequeue was consumed by
		// the same continuation before its abandonment reached the supervisor.
		pendingToCleanup = append(pendingToCleanup, state.pending...)
		state.pending = nil
		al.steeringRescues.Delete(sessionKey)
	case !canResume || !retryRemainingQueue:
		al.steering.clearScope(sessionKey)
		pendingToCleanup = append(pendingToCleanup, state.pending...)
		state.pending = nil
		al.steeringRescues.Delete(sessionKey)
	case len(state.pending) > 0:
		next := state.pending[0]
		state.pending = state.pending[1:]
		nextRequest = &next
		carryOutbound = next.outboundEnqueued
	default:
		// Steering committed to this rescue after its final dequeue but before
		// marker retirement. Keep the same exact UX owner and remember whether
		// an earlier buffered response already owns its delivery artifacts.
		next := request
		nextRequest = &next
		carryOutbound = outboundEnqueued
		cleanupCurrent = false
	}
	unlock()

	if cleanupCurrent {
		al.cleanupSteeringRescueUX(request, outboundEnqueued)
	}
	for _, pending := range pendingToCleanup {
		al.cleanupSteeringRescueUX(
			pending,
			pending.outboundEnqueued,
		)
	}
	if nextRequest != nil {
		go al.runSteeringRescue(
			sessionKey,
			state,
			*nextRequest,
			carryOutbound,
		)
	}
}

func (al *AgentLoop) cleanupSteeringRescueUX(
	request steeringRescueRequest,
	outboundEnqueued bool,
) {
	if al.channelManager == nil || request.inboundContext == nil {
		return
	}
	if outboundEnqueued {
		invokeTypingStopForMessage(
			al.channelManager,
			request.channel,
			request.chatID,
			request.inboundContext.TurnUXID,
		)
		return
	}
	cleanupTurnUXForMessage(
		context.Background(),
		al.channelManager,
		request.channel,
		request.chatID,
		request.inboundContext.TurnUXID,
	)
}

func (al *AgentLoop) getActiveTurnState(sessionKey string) *turnState {
	if val, ok := al.activeTurnStates.Load(sessionKey); ok {
		if ts, ok := val.(*turnState); ok {
			return ts
		}
		// Unexpected non-*turnState value — treat as "no active turn" to avoid
		// panics. This should not happen under normal operation.
	}
	return nil
}

// getAnyActiveTurnState returns any active turn state (for backward compatibility)
func (al *AgentLoop) getAnyActiveTurnState() *turnState {
	var firstTS *turnState
	al.activeTurnStates.Range(func(key, value any) bool {
		if ts, ok := value.(*turnState); ok {
			firstTS = ts
			return false
		}
		return true
	})
	return firstTS
}

func (al *AgentLoop) GetActiveTurn() *ActiveTurnInfo {
	// For backward compatibility, return the first active turn found
	// In the new architecture, there can be multiple concurrent turns
	var firstTS *turnState
	al.activeTurnStates.Range(func(key, value any) bool {
		if ts, ok := value.(*turnState); ok {
			firstTS = ts
			return false
		}
		return true
	})
	if firstTS == nil {
		return nil
	}
	info := firstTS.snapshot()
	return &info
}

func (al *AgentLoop) GetActiveTurnBySession(sessionKey string) *ActiveTurnInfo {
	ts := al.getActiveTurnState(sessionKey)
	if ts == nil {
		return nil
	}
	info := ts.snapshot()
	return &info
}

func (al *AgentLoop) GetActiveTurns() []ActiveTurnInfo {
	var turns []ActiveTurnInfo
	al.activeTurnStates.Range(func(_, value any) bool {
		if ts, ok := value.(*turnState); ok {
			turns = append(turns, ts.snapshot())
		}
		return true
	})
	sort.SliceStable(turns, func(i, j int) bool {
		if turns[i].StartedAt.Equal(turns[j].StartedAt) {
			return turns[i].SessionKey < turns[j].SessionKey
		}
		return turns[i].StartedAt.Before(turns[j].StartedAt)
	})
	return turns
}

// =============================================================================
// turnState - getters and setters
// =============================================================================

func (ts *turnState) snapshot() ActiveTurnInfo {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	return ActiveTurnInfo{
		TurnID:       ts.turnID,
		AgentID:      ts.agentID,
		SessionKey:   ts.sessionKey,
		Channel:      ts.channel,
		ChatID:       ts.chatID,
		UserMessage:  ts.userMessage,
		Phase:        ts.phase,
		Iteration:    ts.iteration,
		StartedAt:    ts.startedAt,
		Depth:        ts.depth,
		ParentTurnID: ts.parentTurnID,
		ChildTurnIDs: append([]string(nil), ts.childTurnIDs...),
	}
}

func (ts *turnState) setPhase(phase TurnPhase) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.phase = phase
}

func (ts *turnState) setIteration(iteration int) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.iteration = iteration
}

func (ts *turnState) currentIteration() int {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.iteration
}

func (ts *turnState) setFinalContent(content string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.finalContent = content
}

func (ts *turnState) finalContentLen() int {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return len(ts.finalContent)
}

func (ts *turnState) finalContentSnapshot() string {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.finalContent
}

func (ts *turnState) recordToolKind(tool string) {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return
	}

	ts.mu.Lock()
	defer ts.mu.Unlock()

	for _, existing := range ts.toolKinds {
		if existing == tool {
			return
		}
	}
	ts.toolKinds = append(ts.toolKinds, tool)
}

func (ts *turnState) toolKindsSnapshot() []string {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return append([]string(nil), ts.toolKinds...)
}

func (ts *turnState) recordToolExecution(tool string, success bool, errorSummary string, skillNames []string) {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return
	}

	ts.recordToolKind(tool)

	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.toolExecutions = append(ts.toolExecutions, ToolExecutionRecord{
		Name:         tool,
		Success:      success,
		ErrorSummary: strings.TrimSpace(errorSummary),
		SkillNames:   append([]string(nil), skillNames...),
	})
}

func (ts *turnState) toolExecutionsSnapshot() []ToolExecutionRecord {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	if len(ts.toolExecutions) == 0 {
		return nil
	}

	out := make([]ToolExecutionRecord, 0, len(ts.toolExecutions))
	for _, exec := range ts.toolExecutions {
		out = append(out, ToolExecutionRecord{
			Name:         exec.Name,
			Success:      exec.Success,
			ErrorSummary: exec.ErrorSummary,
			SkillNames:   append([]string(nil), exec.SkillNames...),
		})
	}
	return out
}

func (ts *turnState) recordAttemptedSkills(skillNames []string) {
	if len(skillNames) == 0 {
		return
	}

	ts.mu.Lock()
	defer ts.mu.Unlock()

	for _, skillName := range skillNames {
		skillName = strings.TrimSpace(skillName)
		if skillName == "" {
			continue
		}
		seen := false
		for _, existing := range ts.attemptedSkills {
			if existing == skillName {
				seen = true
				break
			}
		}
		if seen {
			continue
		}
		ts.attemptedSkills = append(ts.attemptedSkills, skillName)
	}
}

func (ts *turnState) attemptedSkillsSnapshot() []string {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return append([]string(nil), ts.attemptedSkills...)
}

func (ts *turnState) recordSkillContextSnapshot(trigger string, skillNames []string) {
	if len(skillNames) == 0 {
		return
	}

	filtered := make([]string, 0, len(skillNames))
	for _, skillName := range skillNames {
		skillName = strings.TrimSpace(skillName)
		if skillName == "" {
			continue
		}
		filtered = append(filtered, skillName)
	}
	if len(filtered) == 0 {
		return
	}

	ts.recordAttemptedSkills(filtered)

	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.skillContextTrace = append(ts.skillContextTrace, SkillContextSnapshot{
		Sequence:   len(ts.skillContextTrace) + 1,
		Trigger:    trigger,
		SkillNames: append([]string(nil), filtered...),
	})
}

func (ts *turnState) latestSkillContextSnapshot() []string {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	if len(ts.skillContextTrace) == 0 {
		return nil
	}
	return append([]string(nil), ts.skillContextTrace[len(ts.skillContextTrace)-1].SkillNames...)
}

func (ts *turnState) skillContextSnapshotsSnapshot() []SkillContextSnapshot {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	if len(ts.skillContextTrace) == 0 {
		return nil
	}

	snapshots := make([]SkillContextSnapshot, 0, len(ts.skillContextTrace))
	for _, snapshot := range ts.skillContextTrace {
		snapshots = append(snapshots, SkillContextSnapshot{
			Sequence:   snapshot.Sequence,
			Trigger:    snapshot.Trigger,
			SkillNames: append([]string(nil), snapshot.SkillNames...),
		})
	}
	return snapshots
}

func (ts *turnState) setTurnCancel(cancel context.CancelFunc) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.turnCancel = cancel
}

func (ts *turnState) setRuntimeContext(ctx context.Context) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.ctx = ctx
}

func (ts *turnState) setProviderCancel(cancel context.CancelFunc) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.providerCancel = cancel
}

func (ts *turnState) clearProviderCancel(_ context.CancelFunc) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.providerCancel = nil
}

func (ts *turnState) requestGracefulInterrupt(hint string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.hardAbort || ts.cancelRequested || ts.terminalClaimed || ts.isFinished.Load() {
		return false
	}
	ts.gracefulInterrupt = true
	ts.gracefulInterruptHint = hint
	return true
}

func (ts *turnState) gracefulInterruptRequested() (bool, string) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.gracefulInterrupt && !ts.gracefulTerminalUsed, ts.gracefulInterruptHint
}

func (ts *turnState) markGracefulTerminalUsed() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.gracefulTerminalUsed = true
}

func (ts *turnState) requestHardAbort() bool {
	return requestTurnTreeCancellationWithClaim(ts.al, ts, true, false)
}

// requestTurnTreeCancellation first marks every reachable descendant and only
// then invokes cancellation. Exact child pointers are retained after an
// intermediate child leaves activeTurnStates, so critical grandchildren cannot
// escape a later ancestor abort. The active registry lookup is compatibility
// support for turn states built before the pointer graph was populated.
func requestTurnTreeCancellation(al *AgentLoop, root *turnState, hard bool) bool {
	return requestTurnTreeCancellationWithClaim(al, root, hard, false)
}

func requestTurnTreeCancellationWithClaim(
	al *AgentLoop,
	root *turnState,
	hard bool,
	allowClaimedRoot bool,
) bool {
	if root == nil {
		return false
	}

	type cancellationSet struct {
		provider context.CancelFunc
		turn     context.CancelFunc
		owned    context.CancelFunc
	}

	stack := []*turnState{root}
	visited := make(map[*turnState]struct{})
	cancellations := make([]cancellationSet, 0, 4)
	rootChanged := false

	for len(stack) > 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		if current == nil {
			continue
		}
		if _, seen := visited[current]; seen {
			continue
		}
		visited[current] = struct{}{}

		current.mu.Lock()
		if current == root && hard && (current.isFinished.Load() || current.hardAbort ||
			current.terminalClaimed && !allowClaimedRoot) {
			current.mu.Unlock()
			return false
		}
		if current == root && !hard && current.cancelDispatched {
			current.mu.Unlock()
			return false
		}
		if !hard && current.cancelDispatched {
			current.mu.Unlock()
			continue
		}
		terminal := current.isFinished.Load() ||
			current.terminalClaimed && !(current == root && hard && allowClaimedRoot)
		if current == root && hard && !current.hardAbort {
			rootChanged = true
		}
		if !terminal {
			current.cancelRequested = true
			if hard {
				current.hardAbort = true
			}
		}
		if !hard {
			current.cancelDispatched = true
		}
		children := make([]*turnState, 0, len(current.childTurns))
		retained := make(map[string]struct{}, len(current.childTurns))
		for childID, child := range current.childTurns {
			retained[childID] = struct{}{}
			if child != nil && child.sessionKey == childID &&
				child.parentTurnState == current && child.parentTurnID == current.turnID {
				children = append(children, child)
			}
		}
		legacyChildIDs := append([]string(nil), current.childTurnIDs...)
		if !terminal {
			cancellations = append(cancellations, cancellationSet{
				provider: current.providerCancel,
				turn:     current.turnCancel,
				owned:    current.cancelFunc,
			})
		}
		current.mu.Unlock()

		stack = append(stack, children...)
		if al == nil {
			continue
		}
		for _, childID := range legacyChildIDs {
			if _, ok := retained[childID]; ok {
				continue
			}
			value, ok := al.activeTurnStates.Load(childID)
			if !ok {
				continue
			}
			child, ok := value.(*turnState)
			if !ok || child == nil {
				continue
			}
			child.mu.RLock()
			exactParent := child.parentTurnState == current &&
				child.parentTurnID == current.turnID
			child.mu.RUnlock()
			if exactParent {
				stack = append(stack, child)
			}
		}
	}

	for _, cancellation := range cancellations {
		if cancellation.provider != nil {
			cancellation.provider()
		}
		if cancellation.turn != nil {
			cancellation.turn()
		}
		if cancellation.owned != nil {
			cancellation.owned()
		}
	}
	return rootChanged
}

func (ts *turnState) hardAbortRequested() bool {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.hardAbort
}

func (ts *turnState) cancellationRequested() bool {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.cancelRequested
}

func (ts *turnState) terminalStatusSnapshot(fallback TurnEndStatus) TurnEndStatus {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	if ts.terminalStatus != "" {
		return ts.terminalStatus
	}
	if ts.hardAbort {
		return TurnEndStatusAborted
	}
	if ts.cancelRequested && fallback == TurnEndStatusCompleted {
		return TurnEndStatusError
	}
	return fallback
}

func (ts *turnState) eventMeta(source, tracePath string) HookMeta {
	snap := ts.snapshot()
	return HookMeta{
		AgentID:     snap.AgentID,
		TurnID:      snap.TurnID,
		SessionKey:  snap.SessionKey,
		Iteration:   snap.Iteration,
		Source:      source,
		TracePath:   tracePath,
		turnContext: cloneTurnContext(ts.turnCtx),
	}
}

func (ts *turnState) captureRestorePoint(history []providers.Message, summary string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.restorePointHistory = append([]providers.Message(nil), history...)
	ts.restorePointSummary = summary
}

func (ts *turnState) restorePointHistoryLength() int {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return len(ts.restorePointHistory)
}

func (ts *turnState) recordPersistedMessage(msg providers.Message) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.persistedMessages = append(ts.persistedMessages, msg)
}

func (ts *turnState) persistedMessagesSnapshot() []providers.Message {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return append([]providers.Message(nil), ts.persistedMessages...)
}

func (ts *turnState) refreshRestorePointFromSession(agent *AgentInstance) {
	history := agent.Sessions.GetHistory(ts.sessionKey)
	summary := agent.Sessions.GetSummary(ts.sessionKey)

	persisted := ts.persistedMessagesSnapshot()

	if matched := matchingTurnMessageTail(history, persisted); matched > 0 {
		history = append([]providers.Message(nil), history[:len(history)-matched]...)
	}

	ts.captureRestorePoint(history, summary)
}

// ingestMessage calls the ContextManager's Ingest method for a persisted message.
// Errors are logged but never block the turn.
func (ts *turnState) ingestMessage(ctx context.Context, al *AgentLoop, msg providers.Message) {
	if al.contextManager == nil {
		return
	}
	if err := al.contextManager.Ingest(ctx, &IngestRequest{
		SessionKey: ts.sessionKey,
		Message:    msg,
	}); err != nil {
		logger.WarnCF("agent", "Context manager ingest failed", map[string]any{
			"session_key": ts.sessionKey,
			"error":       err.Error(),
		})
	}
}

func (ts *turnState) restoreSession(agent *AgentInstance) error {
	ts.mu.RLock()
	history := append([]providers.Message(nil), ts.restorePointHistory...)
	summary := ts.restorePointSummary
	ts.mu.RUnlock()

	agent.Sessions.SetHistory(ts.sessionKey, history)
	agent.Sessions.SetSummary(ts.sessionKey, summary)
	return agent.Sessions.Save(ts.sessionKey)
}

func matchingTurnMessageTail(history, persisted []providers.Message) int {
	maxMatch := min(len(history), len(persisted))
	for size := maxMatch; size > 0; size-- {
		if messageSlicesEquivalent(history[len(history)-size:], persisted[len(persisted)-size:]) {
			return size
		}
	}
	return 0
}

func splitHistoryForActiveTurn(
	history []providers.Message,
	persisted []providers.Message,
) ([]providers.Message, []providers.Message) {
	matched := matchingTurnMessageTail(history, persisted)
	if matched <= 0 {
		return append([]providers.Message(nil), history...), nil
	}

	stable := append([]providers.Message(nil), history[:len(history)-matched]...)
	protected := append([]providers.Message(nil), history[len(history)-matched:]...)
	return stable, protected
}

func messageSlicesEquivalent(a, b []providers.Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !messagesEquivalent(a[i], b[i]) {
			return false
		}
	}
	return true
}

func messagesEquivalent(a, b providers.Message) bool {
	return reflect.DeepEqual(normalizeMessageForComparison(a), normalizeMessageForComparison(b))
}

func normalizeMessageForComparison(msg providers.Message) providers.Message {
	msg.PromptLayer = ""
	msg.PromptSlot = ""
	msg.PromptSource = ""

	if len(msg.Media) == 0 {
		msg.Media = nil
	}
	if len(msg.Attachments) == 0 {
		msg.Attachments = nil
	}
	if len(msg.Parts) == 0 {
		msg.Parts = nil
	}
	if len(msg.SystemParts) == 0 {
		msg.SystemParts = nil
	} else {
		msg.SystemParts = append([]providers.ContentBlock(nil), msg.SystemParts...)
		for i := range msg.SystemParts {
			msg.SystemParts[i].PromptLayer = ""
			msg.SystemParts[i].PromptSlot = ""
			msg.SystemParts[i].PromptSource = ""
		}
	}
	if len(msg.ToolCalls) == 0 {
		msg.ToolCalls = nil
	} else {
		msg.ToolCalls = append([]providers.ToolCall(nil), msg.ToolCalls...)
		for i := range msg.ToolCalls {
			msg.ToolCalls[i].Name = ""
			msg.ToolCalls[i].Arguments = nil
			msg.ToolCalls[i].ThoughtSignature = ""
			if msg.ToolCalls[i].Function != nil {
				fn := *msg.ToolCalls[i].Function
				fn.ThoughtSignature = ""
				msg.ToolCalls[i].Function = &fn
			}
		}
	}

	return msg
}

func (ts *turnState) interruptHintMessage() providers.Message {
	_, hint := ts.gracefulInterruptRequested()
	content := "Interrupt requested. Stop scheduling tools and provide a short final summary."
	if hint != "" {
		content += "\n\nInterrupt hint: " + hint
	}
	return interruptPromptMessage(content)
}

// =============================================================================
// SubTurn-related methods
// =============================================================================

// commitClaimedTerminal commits one immutable terminal outcome. External
// cleanup deliberately remains outside finishOnce so a panicking dependency
// cannot poison the once and strand active ownership.
func (ts *turnState) commitClaimedTerminal(candidate TurnEndStatus) (TurnEndStatus, bool) {
	actual := candidate
	committed := false
	ts.finishOnce.Do(func() {
		committed = true
		ts.mu.Lock()
		defer ts.mu.Unlock()
		if ts.hardAbort {
			actual = TurnEndStatusAborted
		} else if ts.cancelRequested && actual == TurnEndStatusCompleted {
			actual = TurnEndStatusError
		}
		ts.terminalStatus = actual
		ts.isFinished.Store(true)
		switch actual {
		case TurnEndStatusCompleted:
			ts.phase = TurnPhaseCompleted
			ts.parentEnded.Store(true)
		case TurnEndStatusAborted:
			ts.phase = TurnPhaseAborted
		}
		if ts.finishedChan == nil {
			ts.finishedChan = make(chan struct{})
		}
		close(ts.finishedChan)
	})
	if committed {
		return actual, true
	}

	ts.mu.RLock()
	actual = ts.terminalStatus
	ts.mu.RUnlock()
	return actual, false
}

func (ts *turnState) claimRunTerminal(candidate TurnEndStatus) (TurnEndStatus, bool) {
	ts.mu.Lock()
	if !ts.runAdmitted || ts.terminalClaimed || ts.isFinished.Load() {
		actual := ts.terminalStatus
		ts.mu.Unlock()
		return actual, false
	}
	ts.terminalClaimed = true
	ts.mu.Unlock()
	return ts.commitClaimedTerminal(candidate)
}

func (ts *turnState) claimDetachedTerminal() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.runAdmitted || ts.terminalClaimed || ts.isFinished.Load() {
		return false
	}
	ts.terminalClaimed = true
	return true
}

// runTerminal is the detached compatibility terminal primitive used by
// isolated state tests. Production runTurn uses claimRunTerminal.
func (ts *turnState) runTerminal(candidate TurnEndStatus) (TurnEndStatus, bool) {
	if !ts.claimDetachedTerminal() {
		ts.mu.RLock()
		actual := ts.terminalStatus
		ts.mu.RUnlock()
		return actual, false
	}
	return ts.commitClaimedTerminal(candidate)
}

// Finish remains as an internal compatibility helper for isolated SubTurn
// state tests. Production turn completion is owned exclusively by runTurn.
// pendingResults is intentionally never closed; GC owns mailbox lifetime.
func (ts *turnState) Finish(isHardAbort bool) {
	if !ts.claimDetachedTerminal() {
		return
	}
	status := TurnEndStatusCompleted
	if isHardAbort {
		status = TurnEndStatusAborted
		_ = requestTurnTreeCancellationWithClaim(ts.al, ts, true, true)
	}
	ts.commitClaimedTerminal(status)
}

// Finished returns whether the turn has finished
func (ts *turnState) Finished() chan struct{} {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.finishedChan == nil {
		ts.finishedChan = make(chan struct{})
	}
	return ts.finishedChan
}

// IsParentEnded checks if the parent turn has ended
func (ts *turnState) IsParentEnded() bool {
	if ts.parentTurnState == nil {
		return false
	}
	return ts.parentTurnState.parentEnded.Load()
}

// GetLastFinishReason returns the last LLM finish_reason
func (ts *turnState) GetLastFinishReason() string {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.lastFinishReason
}

// SetLastFinishReason sets the last LLM finish_reason
func (ts *turnState) SetLastFinishReason(reason string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.lastFinishReason = reason
}

// GetLastUsage returns the last LLM usage info
func (ts *turnState) GetLastUsage() *providers.UsageInfo {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.lastUsage
}

// SetLastUsage sets the last LLM usage info
func (ts *turnState) SetLastUsage(usage *providers.UsageInfo) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.lastUsage = usage
}

// =============================================================================
// Context helper functions for turnState
// =============================================================================

type turnStateKeyType struct{}

var turnStateKey = turnStateKeyType{}

func withTurnState(ctx context.Context, ts *turnState) context.Context {
	return context.WithValue(ctx, turnStateKey, ts)
}

func turnStateFromContext(ctx context.Context) *turnState {
	ts, _ := ctx.Value(turnStateKey).(*turnState)
	return ts
}

// TurnStateFromContext retrieves turnState from context (exported for tools)
func TurnStateFromContext(ctx context.Context) *turnState {
	return turnStateFromContext(ctx)
}
