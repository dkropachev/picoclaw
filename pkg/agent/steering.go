package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/bus"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// SteeringMode controls how queued steering messages are dequeued.
type SteeringMode string

const (
	// SteeringOneAtATime dequeues only the first queued message per poll.
	SteeringOneAtATime SteeringMode = "one-at-a-time"
	// SteeringAll drains the entire queue in a single poll.
	SteeringAll SteeringMode = "all"
	// MaxQueueSize number of possible messages in the Steering Queue
	MaxQueueSize = 10
	// manualSteeringScope is the legacy fallback queue used when no active
	// turn/session scope is available.
	manualSteeringScope = "__manual__"
)

var (
	errNoActiveSteeringOwner   = errors.New("no active steering owner")
	errSessionTurnAlreadyOwned = errors.New("session turn is already owned")
)

// parseSteeringMode normalizes a config string into a SteeringMode.
func parseSteeringMode(s string) SteeringMode {
	switch s {
	case "all":
		return SteeringAll
	default:
		return SteeringOneAtATime
	}
}

// steeringQueue is a thread-safe queue of user messages that can be injected
// into a running agent loop to interrupt it between tool calls.
type steeringQueue struct {
	mu     sync.Mutex
	queues map[string][]providers.Message
	mode   SteeringMode
}

func newSteeringQueue(mode SteeringMode) *steeringQueue {
	return &steeringQueue{
		queues: make(map[string][]providers.Message),
		mode:   mode,
	}
}

func normalizeSteeringScope(scope string) string {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return manualSteeringScope
	}
	return scope
}

// push enqueues a steering message in the legacy fallback scope.
func (sq *steeringQueue) push(msg providers.Message) error {
	return sq.pushScope(manualSteeringScope, msg)
}

// pushScope enqueues a steering message for the provided scope.
func (sq *steeringQueue) pushScope(scope string, msg providers.Message) error {
	sq.mu.Lock()
	defer sq.mu.Unlock()

	scope = normalizeSteeringScope(scope)
	queue := sq.queues[scope]
	if len(queue) >= MaxQueueSize {
		return fmt.Errorf("steering queue is full")
	}
	sq.queues[scope] = append(queue, msg)
	return nil
}

// dequeue removes and returns pending steering messages from the legacy
// fallback scope according to the configured mode.
func (sq *steeringQueue) dequeue() []providers.Message {
	return sq.dequeueScope(manualSteeringScope)
}

// dequeueScope removes and returns pending steering messages for the provided
// scope according to the configured mode.
func (sq *steeringQueue) dequeueScope(scope string) []providers.Message {
	sq.mu.Lock()
	defer sq.mu.Unlock()

	return sq.dequeueLocked(normalizeSteeringScope(scope))
}

// dequeueScopeWithFallback drains the scoped queue first and falls back to the
// legacy manual scope for backwards compatibility.
func (sq *steeringQueue) dequeueScopeWithFallback(scope string) []providers.Message {
	sq.mu.Lock()
	defer sq.mu.Unlock()

	scope = strings.TrimSpace(scope)
	if scope != "" {
		if msgs := sq.dequeueLocked(scope); len(msgs) > 0 {
			return msgs
		}
	}

	return sq.dequeueLocked(manualSteeringScope)
}

func (sq *steeringQueue) dequeueLocked(scope string) []providers.Message {
	queue := sq.queues[scope]
	if len(queue) == 0 {
		return nil
	}

	switch sq.mode {
	case SteeringAll:
		msgs := append([]providers.Message(nil), queue...)
		delete(sq.queues, scope)
		return msgs
	default:
		msg := queue[0]
		queue[0] = providers.Message{} // Clear reference for GC
		queue = queue[1:]
		if len(queue) == 0 {
			delete(sq.queues, scope)
		} else {
			sq.queues[scope] = queue
		}
		return []providers.Message{msg}
	}
}

// len returns the number of queued messages across all scopes.
func (sq *steeringQueue) len() int {
	sq.mu.Lock()
	defer sq.mu.Unlock()

	total := 0
	for _, queue := range sq.queues {
		total += len(queue)
	}
	return total
}

// lenScope returns the number of queued messages for a specific scope.
func (sq *steeringQueue) lenScope(scope string) int {
	sq.mu.Lock()
	defer sq.mu.Unlock()
	return len(sq.queues[normalizeSteeringScope(scope)])
}

func (sq *steeringQueue) clearScope(scope string) int {
	sq.mu.Lock()
	defer sq.mu.Unlock()

	scope = normalizeSteeringScope(scope)
	count := len(sq.queues[scope])
	if count > 0 {
		delete(sq.queues, scope)
	}
	return count
}

// setMode updates the steering mode.
func (sq *steeringQueue) setMode(mode SteeringMode) {
	sq.mu.Lock()
	defer sq.mu.Unlock()
	sq.mode = mode
}

// getMode returns the current steering mode.
func (sq *steeringQueue) getMode() SteeringMode {
	sq.mu.Lock()
	defer sq.mu.Unlock()
	return sq.mode
}

// Steer enqueues a user message to be injected into the currently running
// agent loop. The message will be picked up after the current tool finishes
// executing, causing any remaining tool calls in the batch to be skipped.
func (al *AgentLoop) Steer(msg providers.Message) error {
	for {
		ts := al.getAnyActiveTurnState()
		if ts == nil {
			return al.enqueueSteeringMessage("", "", msg)
		}
		err := al.enqueueSteeringMessage(ts.sessionKey, ts.agentID, msg)
		if errors.Is(err, errNoActiveSteeringOwner) {
			// The sampled owner completed before the handoff lock was
			// acquired. Retry so a replacement owner is selected, or use the
			// historical manual fallback when the agent is now idle.
			continue
		}
		return err
	}
}

func (al *AgentLoop) enqueueSteeringMessage(scope, agentID string, msg providers.Message) error {
	if al.steering == nil {
		return fmt.Errorf("steering queue is not initialized")
	}

	normalizedScope := normalizeSteeringScope(scope)
	unlock := al.lockSessionTurn(normalizedScope)
	if normalizedScope != manualSteeringScope {
		if _, active := al.activeTurnStates.Load(normalizedScope); !active {
			unlock()
			return fmt.Errorf(
				"%w for scope %q",
				errNoActiveSteeringOwner,
				normalizedScope,
			)
		}
	}
	msg, queueDepth, err := al.pushSteeringMessage(scope, msg)
	unlock()
	al.reportSteeringEnqueue(scope, agentID, msg, queueDepth, err)
	return err
}

// pushSteeringMessage mutates only the queue. Callers coordinating an active
// turn handoff must hold the matching session-turn lock until any associated
// transient-UX rebind is complete.
func (al *AgentLoop) pushSteeringMessage(
	scope string,
	msg providers.Message,
) (providers.Message, int, error) {
	msg = steeringPromptMessage(msg)
	if err := al.steering.pushScope(scope, msg); err != nil {
		return msg, al.steering.lenScope(scope), err
	}
	return msg, al.steering.lenScope(scope), nil
}

// reportSteeringEnqueue performs logging and event delivery after the
// session-turn handoff lock has been released.
func (al *AgentLoop) reportSteeringEnqueue(
	scope, agentID string,
	msg providers.Message,
	queueDepth int,
	err error,
) {
	if err != nil {
		logger.WarnCF("agent", "Failed to enqueue steering message", map[string]any{
			"error": err.Error(),
			"role":  msg.Role,
			"scope": normalizeSteeringScope(scope),
		})
		return
	}

	logger.DebugCF("agent", "Steering message enqueued", map[string]any{
		"role":        msg.Role,
		"content_len": len(msg.Content),
		"media_count": len(msg.Media),
		"queue_len":   queueDepth,
		"scope":       normalizeSteeringScope(scope),
	})

	meta := HookMeta{
		Source:    "Steer",
		TracePath: "turn.interrupt.received",
	}
	if ts := al.getAnyActiveTurnState(); ts != nil {
		meta = ts.eventMeta("Steer", "turn.interrupt.received")
	} else {
		if strings.TrimSpace(agentID) != "" {
			meta.AgentID = agentID
		}
		normalizedScope := normalizeSteeringScope(scope)
		if normalizedScope != manualSteeringScope {
			meta.SessionKey = normalizedScope
		}
		if meta.AgentID == "" {
			if registry := al.GetRegistry(); registry != nil {
				if agent := registry.GetDefaultAgent(); agent != nil {
					meta.AgentID = agent.ID
				}
			}
		}
	}

	al.emitEvent(
		runtimeevents.KindAgentInterruptReceived,
		meta,
		InterruptReceivedPayload{
			Kind:       InterruptKindSteering,
			Role:       msg.Role,
			ContentLen: len(msg.Content),
			QueueDepth: queueDepth,
		},
	)
}

// SteeringMode returns the current steering mode.
func (al *AgentLoop) SteeringMode() SteeringMode {
	if al.steering == nil {
		return SteeringOneAtATime
	}
	return al.steering.getMode()
}

// SetSteeringMode updates the steering mode.
func (al *AgentLoop) SetSteeringMode(mode SteeringMode) {
	if al.steering == nil {
		return
	}
	al.steering.setMode(mode)
}

// dequeueSteeringMessages is the internal method called by the agent loop
// to poll for steering messages in the legacy fallback scope.
func (al *AgentLoop) dequeueSteeringMessages() []providers.Message {
	if al.steering == nil {
		return nil
	}
	unlock := al.lockSessionTurn(manualSteeringScope)
	defer unlock()
	return al.steering.dequeue()
}

func (al *AgentLoop) dequeueSteeringMessagesForScope(scope string) []providers.Message {
	if al.steering == nil {
		return nil
	}
	unlock := al.lockSessionTurn(normalizeSteeringScope(scope))
	defer unlock()
	return al.steering.dequeueScope(scope)
}

func (al *AgentLoop) dequeueSteeringMessagesForScopeWithFallback(scope string) []providers.Message {
	if al.steering == nil {
		return nil
	}
	unlock := al.lockSessionTurn(normalizeSteeringScope(scope))
	defer unlock()
	return al.steering.dequeueScopeWithFallback(scope)
}

func (al *AgentLoop) pendingSteeringCountForScope(scope string) int {
	if al.steering == nil {
		return 0
	}
	unlock := al.lockSessionTurn(normalizeSteeringScope(scope))
	defer unlock()
	return al.steering.lenScope(scope)
}

func (al *AgentLoop) clearSteeringMessagesForScope(scope string) int {
	if al.steering == nil {
		return 0
	}
	unlock := al.lockSessionTurn(normalizeSteeringScope(scope))
	defer unlock()
	return al.steering.clearScope(scope)
}

func (al *AgentLoop) continueWithSteeringMessages(
	ctx context.Context,
	agent *AgentInstance,
	sessionKey, channel, chatID string,
	scope *session.SessionScope,
	inboundContext *bus.InboundContext,
	reservation *turnState,
	steeringMsgs []providers.Message,
) (string, error) {
	dispatch := DispatchRequest{
		SessionKey:   sessionKey,
		SessionScope: session.CloneScope(scope),
	}
	if inboundContext != nil {
		dispatch.InboundContext = cloneInboundContext(inboundContext)
	}
	if dispatch.InboundContext == nil && (channel != "" || chatID != "") {
		dispatch.InboundContext = &bus.InboundContext{
			Channel:  channel,
			ChatID:   chatID,
			ChatType: inferChatTypeFromSessionScope(scope),
		}
	} else if dispatch.InboundContext != nil {
		if channel != "" {
			dispatch.InboundContext.Channel = channel
		}
		if chatID != "" {
			dispatch.InboundContext.ChatID = chatID
		}
	}
	return al.runAgentLoop(ctx, agent, processOptions{
		Dispatch:                dispatch,
		DefaultResponse:         defaultResponse,
		EnableSummary:           true,
		SendResponse:            false,
		InitialSteeringMessages: steeringMsgs,
		SkipInitialSteeringPoll: true,
		turnReservation:         reservation,
	})
}

func (al *AgentLoop) agentForSession(sessionKey string) *AgentInstance {
	if agent, ok := al.resolveAgentForSession(sessionKey); ok {
		return agent
	}

	registry := al.GetRegistry()
	if registry == nil {
		return nil
	}
	return registry.GetDefaultAgent()
}

func (al *AgentLoop) resolveAgentForSession(sessionKey string) (*AgentInstance, bool) {
	registry := al.GetRegistry()
	if registry == nil {
		return nil, false
	}

	agentIDs := registry.ListAgentIDs()
	sort.Strings(agentIDs)
	for _, agentID := range agentIDs {
		agent, ok := registry.GetAgent(agentID)
		if !ok || agent == nil {
			continue
		}
		resolvedAgentID := session.ResolveAgentID(agent.Sessions, sessionKey)
		if resolvedAgentID == "" {
			continue
		}
		if scopedAgent, ok := registry.GetAgent(resolvedAgentID); ok {
			return scopedAgent, true
		}
	}

	return nil, false
}

// Continue resumes an idle agent by dequeuing any pending steering messages
// and running them through the agent loop. This is used when the agent's last
// message was from the assistant (i.e., it has stopped processing) and the
// user has since enqueued steering messages.
//
// If no steering messages are pending, it returns an empty string.
func (al *AgentLoop) Continue(
	ctx context.Context,
	sessionKey, channel, chatID string,
) (string, error) {
	return al.continueWithInboundContext(ctx, sessionKey, channel, chatID, nil)
}

func (al *AgentLoop) continueWithInboundContext(
	ctx context.Context,
	sessionKey, channel, chatID string,
	inboundContext *bus.InboundContext,
) (string, error) {
	leaseCtx, releaseRuntime, err := al.acquireRuntimeUse(ctx)
	if err != nil {
		return "", err
	}
	defer releaseRuntime()
	ctx = leaseCtx

	// Complete fallible setup before publishing a placeholder that inbound
	// messages may otherwise treat as a live steering owner.
	if err := al.ensureHooksInitialized(ctx); err != nil {
		return "", err
	}
	if err := al.ensureMCPInitialized(ctx); err != nil {
		return "", err
	}

	agent := al.agentForSession(sessionKey)
	if agent == nil {
		return "", fmt.Errorf("no agent available for session %q", sessionKey)
	}

	if err := admitSessionMetadata(
		ctx,
		agent.Sessions,
		sessionKey,
		nil,
		nil,
		agent.ID,
	); err != nil {
		return "", fmt.Errorf("admit live session scope: %w", err)
	}
	var scope *session.SessionScope
	if metaStore, ok := agent.Sessions.(session.MetadataAwareSessionStore); ok {
		scope = metaStore.GetSessionScope(sessionKey)
	}

	// Claim the session with a unique placeholder to prevent a TOCTOU race where two
	// concurrent Continue calls for the same session both pass the active-turn
	// check and create parallel turns. The placeholder is replaced by the real
	// turnState inside continueWithSteeringMessages → runAgentLoop → registerActiveTurn.
	placeholder := &turnState{
		turnID:     "pending-continue-" + sessionKey + "-" + fmt.Sprintf("%d", al.turnSeq.Add(1)),
		agentID:    agent.ID,
		sessionKey: sessionKey,
		channel:    channel,
		chatID:     chatID,
		turnUXID: func() string {
			if inboundContext == nil {
				return ""
			}
			return inboundContext.TurnUXID
		}(),
		handoffContext: cloneInboundContext(inboundContext),
		phase:          TurnPhaseSetup,
	}
	unlockSessionTurn := al.lockSessionTurn(sessionKey)
	if _, loaded := al.activeTurnStates.LoadOrStore(sessionKey, placeholder); loaded {
		unlockSessionTurn()
		if active := al.GetActiveTurnBySession(sessionKey); active != nil {
			return "", fmt.Errorf(
				"%w: turn %s is still active for session %q",
				errSessionTurnAlreadyOwned,
				active.TurnID,
				sessionKey,
			)
		}
		// Another Continue just claimed the slot; let it handle the steering.
		return "", nil
	}

	// Claim, dequeue, and the empty-queue conditional delete are one handoff.
	// An inbound producer therefore either queues behind this owner before the
	// dequeue, or observes no owner and claims the session itself afterward.
	steeringMsgs := al.steering.dequeueScopeWithFallback(sessionKey)
	if len(steeringMsgs) == 0 {
		al.activeTurnStates.CompareAndDelete(sessionKey, placeholder)
		unlockSessionTurn()
		return "", nil
	}
	unlockSessionTurn()
	defer al.abandonSessionTurnState(ctx, sessionKey, placeholder)

	if tool, ok := agent.Tools.Get("message"); ok {
		if resetter, ok := tool.(interface{ ResetSentInRound(sessionKey string) }); ok {
			resetter.ResetSentInRound(sessionKey)
		}
	}

	return al.continueWithSteeringMessages(
		ctx,
		agent,
		sessionKey,
		channel,
		chatID,
		scope,
		inboundContext,
		placeholder,
		steeringMsgs,
	)
}

func (al *AgentLoop) InterruptGraceful(hint string) error {
	ts := al.getAnyActiveTurnState()
	if ts == nil {
		return fmt.Errorf("no active turn")
	}
	if !ts.requestGracefulInterrupt(hint) {
		return fmt.Errorf("turn %s cannot accept graceful interrupt", ts.turnID)
	}

	al.emitEvent(
		runtimeevents.KindAgentInterruptReceived,
		ts.eventMeta("InterruptGraceful", "turn.interrupt.received"),
		InterruptReceivedPayload{
			Kind:    InterruptKindGraceful,
			HintLen: len(hint),
		},
	)

	return nil
}

// InterruptHard aborts an arbitrary active turn. In parallel mode this may
// target the wrong session. Prefer HardAbort(sessionKey) instead.
//
// Deprecated: Use HardAbort(sessionKey) for session-safe aborts.
func (al *AgentLoop) InterruptHard() error {
	ts := al.getAnyActiveTurnState()
	if ts == nil {
		return fmt.Errorf("no active turn")
	}
	if strings.HasPrefix(ts.turnID, "pending-") {
		return fmt.Errorf("turn is still initializing for session %s", ts.sessionKey)
	}
	if !ts.requestHardAbort() {
		return fmt.Errorf("turn %s is already aborting", ts.turnID)
	}

	al.emitEvent(
		runtimeevents.KindAgentInterruptReceived,
		ts.eventMeta("InterruptHard", "turn.interrupt.received"),
		InterruptReceivedPayload{
			Kind: InterruptKindHard,
		},
	)

	return nil
}

// ====================== SubTurn Result Polling ======================

// dequeuePendingSubTurnResults polls the SubTurn result channel for the given
// session and returns all available results without blocking.
// Returns nil if no active turn state exists for this session.
func (al *AgentLoop) dequeuePendingSubTurnResults(sessionKey string) []*tools.ToolResult {
	tsInterface, ok := al.activeTurnStates.Load(sessionKey)
	if !ok {
		return nil
	}
	ts, ok := tsInterface.(*turnState)
	if !ok {
		return nil
	}

	var results []*tools.ToolResult
	for {
		select {
		case result, ok := <-ts.pendingResults:
			if !ok {
				return results
			}
			if result != nil {
				results = append(results, result)
			}
		default:
			return results
		}
	}
}

// ====================== Hard Abort ======================

// HardAbort requests cancellation of the exact running turn tree. runTurn owns
// the terminal transition, restore-point rollback, event, and active-owner
// cleanup; this method never truncates history or calls Finish.
//
// Use this when the user explicitly requests immediate termination (e.g., "stop now", "abort").
// For graceful interruption that allows the agent to finish the current tool and summarize,
// use Steer() instead.
func (al *AgentLoop) HardAbort(sessionKey string) error {
	tsInterface, ok := al.activeTurnStates.Load(sessionKey)
	if !ok {
		return fmt.Errorf("no active turn state found for session %s", sessionKey)
	}

	ts, ok := tsInterface.(*turnState)
	if !ok {
		return fmt.Errorf("invalid turn state type for session %s", sessionKey)
	}

	if strings.HasPrefix(ts.turnID, "pending-") {
		return fmt.Errorf("turn is still initializing for session %s", sessionKey)
	}

	logger.InfoCF("agent", "Hard abort triggered", map[string]any{
		"session_key":            sessionKey,
		"turn_id":                ts.turnID,
		"depth":                  ts.depth,
		"restore_history_length": ts.restorePointHistoryLength(),
	})

	// requestHardAbort atomically marks every nonterminal descendant before
	// invoking any provider/turn/owned-context cancellation.
	if !ts.requestHardAbort() {
		return fmt.Errorf("turn %s is already aborting or finished", ts.turnID)
	}

	return nil
}

// ====================== Follow-Up Injection ======================

// InjectFollowUp enqueues a message to be automatically processed after the current
// turn completes. Unlike Steer(), which interrupts the current execution, InjectFollowUp
// waits for the current turn to finish naturally before processing the message.
//
// This is useful for:
// - Automated workflows that need to chain multiple turns
// - Background tasks that should run after the main task completes
// - Scheduled follow-up actions
//
// The message will be processed via Continue() when the agent becomes idle.
func (al *AgentLoop) InjectFollowUp(msg providers.Message) error {
	// InjectFollowUp uses the same steering queue mechanism as Steer(),
	// but the semantic difference is in when it's called:
	// - Steer() is called during active execution to interrupt
	// - InjectFollowUp() is called when planning future work
	//
	// Both end up in the same queue and are processed by Continue()
	// when the agent is idle.
	return al.Steer(msg)
}

// ====================== API Aliases for Design Document Compatibility ======================

// InjectSteering is an alias for Steer() to match the design document naming.
// It injects a steering message into the currently running agent loop.
func (al *AgentLoop) InjectSteering(msg providers.Message) error {
	return al.Steer(msg)
}
