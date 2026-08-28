package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
)

const (
	maxTrackedSubagentResultsPerScope = 16
	maxTrackedSubagentResultBytes     = 64 * 1024
)

type trackedSubagentResultID struct {
	SourceTurnID string
	TaskID       string
}

type trackedSubagentResultScope struct {
	AgentID    string
	SessionKey string
}

type trackedSubagentResultRoute struct {
	SourceTurnID                string
	SourceAgentID               string
	SourceSessionKey            string
	RootTurnID                  string
	RootAgentID                 string
	RootSessionKey              string
	RootChannel                 string
	RootChatID                  string
	RootPersistent              bool
	RootScope                   *session.SessionScope
	RootInbound                 bus.InboundContext
	RootProfile                 config.EffectiveTurnProfile
	RootDisableTools            bool
	RootSuppressContext         bool
	RootDisablePromptCache      bool
	RootLateContinuationAllowed bool
	RootEnableSummary           bool
}

type trackedSubagentResultState uint8

const (
	trackedSubagentResultPendingPreferred trackedSubagentResultState = iota + 1
	trackedSubagentResultPendingRoot
	trackedSubagentResultClaimed
	trackedSubagentResultOrphaned
)

type trackedSubagentResultRecord struct {
	id                 trackedSubagentResultID
	route              trackedSubagentResultRoute
	completion         tools.SubagentCompletion
	content            string
	fingerprint        [sha256.Size]byte
	state              trackedSubagentResultState
	currentScope       trackedSubagentResultScope
	rootEligible       bool
	orphanReason       string
	conflictSeen       bool
	preflightAttempts  int
	preflightNotBefore time.Time
}

type trackedSubagentResultScopeState struct {
	queue                   []trackedSubagentResultID
	pending                 int
	pumping                 bool
	rescuingSteering        bool
	steeringRescueAttempts  int
	steeringRescueNotBefore time.Time
	steeringWakeScheduled   bool
}

type trackedSubagentTurnRelease struct {
	status      TurnEndStatus
	released    bool
	outputReady bool
}

// trackedSubagentResultMailbox is deliberately zero-value ready. Records and
// deduplication tombstones have the same process lifetime as P006's manager
// task records; restart persistence and eviction belong to the later task
// store rather than this delivery repair.
type trackedSubagentResultMailbox struct {
	mu              sync.Mutex
	trackedTurns    sync.Map
	records         map[trackedSubagentResultID]*trackedSubagentResultRecord
	scopes          map[trackedSubagentResultScope]*trackedSubagentResultScopeState
	released        map[string]trackedSubagentTurnRelease
	pendingBySource map[string]map[trackedSubagentResultID]struct{}
	pendingByRoot   map[string]map[trackedSubagentResultID]struct{}
	rootsBySession  map[string]map[trackedSubagentResultScope]struct{}
	outputHolds     map[string]int
}

type trackedSubagentResultClaim struct {
	id         trackedSubagentResultID
	route      trackedSubagentResultRoute
	completion tools.SubagentCompletion
	content    string
}

type trackedSubagentResultOrphan struct {
	route  trackedSubagentResultRoute
	taskID string
	status string
	reason string
}

type trackedSubagentResultQueued struct {
	route      trackedSubagentResultRoute
	completion tools.SubagentCompletion
	contentLen int
}

type trackedSubagentResultOutputOwner struct {
	mu    sync.Mutex
	turns map[string]string
}

type trackedSubagentResultOutputOwnerContextKey struct{}

func (al *AgentLoop) trackedSubagentWorkerContext() context.Context {
	if al == nil {
		return context.Background()
	}
	al.trackedSubagentWorkerMu.Lock()
	if al.trackedSubagentWorkerCtx == nil {
		workerCtx, workerCancel := context.WithCancel(context.Background())
		al.trackedSubagentWorkerCtx = workerCtx
		al.trackedSubagentWorkerCancel = workerCancel
	}
	ctx := al.trackedSubagentWorkerCtx
	cancel := al.trackedSubagentWorkerCancel
	al.trackedSubagentWorkerMu.Unlock()

	al.runtimeGateMu.Lock()
	stopped := al.runtimeGateStopped
	al.runtimeGateMu.Unlock()
	if stopped && cancel != nil {
		cancel()
	}
	return ctx
}

func (al *AgentLoop) cancelTrackedSubagentWorkers() {
	if al == nil {
		return
	}
	al.trackedSubagentWorkerMu.Lock()
	cancel := al.trackedSubagentWorkerCancel
	al.trackedSubagentWorkerMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func withTrackedSubagentResultOutputOwner(
	ctx context.Context,
	owner *trackedSubagentResultOutputOwner,
) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if owner == nil {
		return ctx
	}
	return context.WithValue(ctx, trackedSubagentResultOutputOwnerContextKey{}, owner)
}

func trackedSubagentResultOutputOwnerFromContext(
	ctx context.Context,
) *trackedSubagentResultOutputOwner {
	if ctx == nil {
		return nil
	}
	owner, _ := ctx.Value(trackedSubagentResultOutputOwnerContextKey{}).(*trackedSubagentResultOutputOwner)
	return owner
}

func (owner *trackedSubagentResultOutputOwner) record(
	al *AgentLoop,
	turnID, sessionKey string,
) {
	if owner == nil || al == nil || strings.TrimSpace(turnID) == "" ||
		strings.TrimSpace(sessionKey) == "" {
		return
	}
	owner.mu.Lock()
	if owner.turns == nil {
		owner.turns = make(map[string]string)
	}
	if _, exists := owner.turns[turnID]; exists {
		owner.mu.Unlock()
		return
	}
	owner.turns[turnID] = sessionKey
	owner.mu.Unlock()
	al.trackedSubagentResults.mu.Lock()
	al.trackedSubagentResults.initLocked()
	al.trackedSubagentResults.outputHolds[sessionKey]++
	al.trackedSubagentResults.mu.Unlock()
}

func (owner *trackedSubagentResultOutputOwner) release(al *AgentLoop) {
	if owner == nil || al == nil {
		return
	}
	owner.mu.Lock()
	turns := make(map[string]string, len(owner.turns))
	for turnID, sessionKey := range owner.turns {
		turns[turnID] = sessionKey
	}
	owner.turns = nil
	owner.mu.Unlock()
	for turnID := range turns {
		al.markTrackedSubagentResultOutputReady(turnID)
	}
	wakeSessions := make(map[string]struct{})
	al.trackedSubagentResults.mu.Lock()
	for _, sessionKey := range turns {
		if al.trackedSubagentResults.outputHolds[sessionKey] > 1 {
			al.trackedSubagentResults.outputHolds[sessionKey]--
		} else {
			delete(al.trackedSubagentResults.outputHolds, sessionKey)
		}
		wakeSessions[sessionKey] = struct{}{}
	}
	al.trackedSubagentResults.mu.Unlock()
	for sessionKey := range wakeSessions {
		al.wakeTrackedSubagentResultsForSession(sessionKey)
	}
}

func snapshotTrackedSubagentResultRoute(
	source *turnState,
) (trackedSubagentResultRoute, error) {
	if source == nil {
		return trackedSubagentResultRoute{}, fmt.Errorf("source turn is required")
	}

	visited := make(map[*turnState]struct{})
	current := source
	var sourceSnapshot trackedSubagentResultRoute
	for depth := 0; current != nil && depth < 128; depth++ {
		if _, duplicate := visited[current]; duplicate {
			return trackedSubagentResultRoute{}, fmt.Errorf("subturn parent cycle")
		}
		visited[current] = struct{}{}

		current.mu.RLock()
		turnID := strings.TrimSpace(current.turnID)
		agentID := strings.TrimSpace(current.agentID)
		sessionKey := strings.TrimSpace(current.sessionKey)
		channel := strings.TrimSpace(current.channel)
		chatID := strings.TrimSpace(current.chatID)
		parentTurnID := strings.TrimSpace(current.parentTurnID)
		parent := current.parentTurnState
		noHistory := current.opts.NoHistory
		profile := cloneTrackedSubagentTurnProfile(current.profile)
		disableTools := current.opts.DisableTools
		suppressContext := current.opts.SuppressDefaultContext
		disablePromptCache := current.opts.DisablePromptCache
		enableSummary := current.opts.EnableSummary
		lateContinuationAllowed := current.opts.callAdmission == nil &&
			current.opts.usageObserver == nil &&
			current.opts.resultUsage == nil &&
			current.opts.resultModelName == nil &&
			len(current.opts.ForcedSkills) == 0 &&
			strings.TrimSpace(current.opts.SystemPromptOverride) == "" &&
			(strings.TrimSpace(current.opts.PromptCacheKey) == "" ||
				current.opts.DisablePromptCache) &&
			strings.TrimSpace(current.opts.ModelNameOverride) == "" &&
			current.opts.ModelFallbacksOverride == nil &&
			strings.TrimSpace(current.opts.AccountRefOverride) == "" &&
			strings.TrimSpace(current.opts.ReasoningEffortOverride) == ""
		rootScope := session.CloneScope(current.opts.Dispatch.SessionScope)
		rootInbound := sanitizeTrackedSubagentInbound(
			current.opts.Dispatch.InboundContext,
			channel,
			chatID,
			rootScope,
		)
		current.mu.RUnlock()

		if turnID == "" || agentID == "" || sessionKey == "" {
			return trackedSubagentResultRoute{}, fmt.Errorf("incomplete turn identity")
		}
		if (channel == "") != (chatID == "") {
			return trackedSubagentResultRoute{}, fmt.Errorf("partial channel/chat route")
		}
		if depth == 0 {
			sourceSnapshot.SourceTurnID = turnID
			sourceSnapshot.SourceAgentID = agentID
			sourceSnapshot.SourceSessionKey = sessionKey
		}
		if parent == nil {
			if channel == "" || chatID == "" {
				return trackedSubagentResultRoute{}, fmt.Errorf("root channel/chat route is required")
			}
			sourceSnapshot.RootTurnID = turnID
			sourceSnapshot.RootAgentID = agentID
			sourceSnapshot.RootSessionKey = sessionKey
			sourceSnapshot.RootChannel = channel
			sourceSnapshot.RootChatID = chatID
			sourceSnapshot.RootPersistent = !noHistory
			sourceSnapshot.RootLateContinuationAllowed = lateContinuationAllowed
			sourceSnapshot.RootScope = rootScope
			sourceSnapshot.RootInbound = rootInbound
			sourceSnapshot.RootProfile = profile
			sourceSnapshot.RootDisableTools = disableTools
			sourceSnapshot.RootSuppressContext = suppressContext
			sourceSnapshot.RootDisablePromptCache = disablePromptCache
			sourceSnapshot.RootEnableSummary = enableSummary
			return sourceSnapshot, nil
		}

		parent.mu.RLock()
		actualParentTurnID := strings.TrimSpace(parent.turnID)
		parent.mu.RUnlock()
		if parentTurnID == "" || parentTurnID != actualParentTurnID {
			return trackedSubagentResultRoute{}, fmt.Errorf("invalid subturn parent edge")
		}
		current = parent
	}

	return trackedSubagentResultRoute{}, fmt.Errorf("subturn parent depth exceeded")
}

func sanitizeTrackedSubagentInbound(
	inbound *bus.InboundContext,
	channel, chatID string,
	scope *session.SessionScope,
) bus.InboundContext {
	result := bus.InboundContext{
		Channel:  channel,
		ChatID:   chatID,
		ChatType: inferChatTypeFromSessionScope(scope),
	}
	if inbound == nil {
		return result
	}
	result.Account = strings.TrimSpace(inbound.Account)
	result.ChatType = strings.TrimSpace(inbound.ChatType)
	result.TopicID = strings.TrimSpace(inbound.TopicID)
	result.SpaceID = strings.TrimSpace(inbound.SpaceID)
	result.SpaceType = strings.TrimSpace(inbound.SpaceType)
	result.SenderID = strings.TrimSpace(inbound.SenderID)
	result.ConversationName = strings.TrimSpace(inbound.ConversationName)
	if result.Channel == "" {
		result.Channel = strings.TrimSpace(inbound.Channel)
	}
	if result.ChatID == "" {
		result.ChatID = strings.TrimSpace(inbound.ChatID)
	}
	if result.ChatType == "" {
		result.ChatType = inferChatTypeFromSessionScope(scope)
	}
	return result
}

func cloneTrackedSubagentResultRoute(
	route trackedSubagentResultRoute,
) trackedSubagentResultRoute {
	route.RootScope = session.CloneScope(route.RootScope)
	route.RootProfile = cloneTrackedSubagentTurnProfile(route.RootProfile)
	route.RootInbound = sanitizeTrackedSubagentInbound(
		&route.RootInbound,
		route.RootChannel,
		route.RootChatID,
		route.RootScope,
	)
	return route
}

func cloneTrackedSubagentTurnProfile(
	profile config.EffectiveTurnProfile,
) config.EffectiveTurnProfile {
	profile.AllowedSkills = append([]string(nil), profile.AllowedSkills...)
	profile.AllowedTools = append([]string(nil), profile.AllowedTools...)
	return profile
}

func (al *AgentLoop) acceptTrackedSubagentResult(
	route trackedSubagentResultRoute,
	completion tools.SubagentCompletion,
	result *tools.ToolResult,
) {
	if al == nil {
		return
	}
	route = cloneTrackedSubagentResultRoute(route)
	al.watchTrackedSubagentResultRoute(route)
	completion.TaskID = strings.TrimSpace(completion.TaskID)
	completion.Status = strings.TrimSpace(completion.Status)

	reason := validateTrackedSubagentResult(route, completion, result)
	content := ""
	if reason == "" {
		content = result.ContentForLLM()
		if cfg := al.GetConfig(); cfg != nil {
			content = cfg.FilterSensitiveData(content)
		}
		content = boundTrackedSubagentResult(content)
		if strings.TrimSpace(content) == "" {
			reason = "empty_result"
		}
	}

	id := trackedSubagentResultID{
		SourceTurnID: route.SourceTurnID,
		TaskID:       completion.TaskID,
	}
	record := &trackedSubagentResultRecord{
		id:          id,
		route:       route,
		completion:  completion,
		content:     content,
		fingerprint: trackedSubagentResultFingerprint(route, completion, content),
	}

	sourceScope := trackedSubagentResultScope{
		AgentID: route.SourceAgentID, SessionKey: route.SourceSessionKey,
	}
	rootScope := trackedSubagentResultScope{
		AgentID: route.RootAgentID, SessionKey: route.RootSessionKey,
	}
	var (
		orphan       *trackedSubagentResultOrphan
		queuedEvent  *trackedSubagentResultQueued
		queued       bool
		scheduleRoot bool
	)

	// The source-session handoff lock closes the only dangerous insertion gap:
	// either the exact source remains published while this record is queued, or
	// its release deletes the source first and this admission routes to root.
	unlockSource := al.lockSessionTurn(route.SourceSessionKey)
	al.trackedSubagentResults.mu.Lock()
	al.trackedSubagentResults.initLocked()
	if existing := al.trackedSubagentResults.records[id]; existing != nil {
		if existing.fingerprint != record.fingerprint && !existing.conflictSeen {
			existing.conflictSeen = true
			orphan = &trackedSubagentResultOrphan{
				route: route, taskID: completion.TaskID,
				status: completion.Status, reason: "identity_conflict",
			}
		}
		al.trackedSubagentResults.mu.Unlock()
		unlockSource()
		if orphan != nil {
			al.emitTrackedSubagentResultOrphan(*orphan)
		}
		return
	}
	al.trackedSubagentResults.records[id] = record

	rootState := al.trackedSubagentResults.scopeLocked(rootScope)
	if reason == "" && rootState.pending >= maxTrackedSubagentResultsPerScope {
		reason = "mailbox_full"
	}
	if reason != "" {
		record.state = trackedSubagentResultOrphaned
		record.orphanReason = reason
		orphan = &trackedSubagentResultOrphan{
			route: route, taskID: completion.TaskID,
			status: completion.Status, reason: reason,
		}
	} else if terminal, ok := al.trackedSubagentResults.released[route.RootTurnID]; ok && terminal.status != TurnEndStatusCompleted {
		record.state = trackedSubagentResultOrphaned
		record.orphanReason = "root_failed"
		orphan = &trackedSubagentResultOrphan{
			route: route, taskID: completion.TaskID,
			status: completion.Status, reason: "root_failed",
		}
	} else {
		rootState.pending++
		if actual, active := al.activeTurnStates.Load(route.SourceSessionKey); active &&
			activeTurnMatchesTrackedResult(actual, route.SourceTurnID, route.SourceAgentID) {
			record.state = trackedSubagentResultPendingPreferred
			record.currentScope = sourceScope
			al.trackedSubagentResults.enqueueLocked(sourceScope, id)
			queued = true
		} else if actual, active := al.activeTurnStates.Load(route.RootSessionKey); active &&
			activeTurnMatchesTrackedResult(actual, route.RootTurnID, route.RootAgentID) {
			record.state = trackedSubagentResultPendingRoot
			record.currentScope = rootScope
			al.trackedSubagentResults.enqueueLocked(rootScope, id)
			queued = true
		} else if !route.RootPersistent {
			orphanReason := "root_not_persistent"
			record.state = trackedSubagentResultOrphaned
			record.orphanReason = orphanReason
			rootState.pending--
			orphan = &trackedSubagentResultOrphan{
				route: route, taskID: completion.TaskID,
				status: completion.Status, reason: orphanReason,
			}
		} else if terminal, known := al.trackedSubagentResults.released[route.RootTurnID]; known && terminal.released && !route.RootLateContinuationAllowed {
			record.state = trackedSubagentResultOrphaned
			record.orphanReason = "root_continuation_policy"
			rootState.pending--
			orphan = &trackedSubagentResultOrphan{
				route: route, taskID: completion.TaskID,
				status: completion.Status, reason: "root_continuation_policy",
			}
		} else {
			record.state = trackedSubagentResultPendingRoot
			record.currentScope = rootScope
			if terminal, ok := al.trackedSubagentResults.released[route.RootTurnID]; ok {
				record.rootEligible = terminal.released &&
					terminal.outputReady &&
					terminal.status == TurnEndStatusCompleted
			}
			al.trackedSubagentResults.enqueueLocked(rootScope, id)
			queued = true
			if terminal, ok := al.trackedSubagentResults.released[route.RootTurnID]; ok {
				scheduleRoot = record.rootEligible && terminal.outputReady
			}
		}
	}
	if queued {
		al.trackedSubagentResults.indexPendingLocked(record)
		queuedEvent = &trackedSubagentResultQueued{
			route:      cloneTrackedSubagentResultRoute(record.route),
			completion: record.completion,
			contentLen: len(record.content),
		}
	} else if record.state == trackedSubagentResultOrphaned {
		compactTrackedSubagentTerminalRecord(record)
	}
	al.trackedSubagentResults.mu.Unlock()
	unlockSource()

	if orphan != nil {
		al.emitTrackedSubagentResultOrphan(*orphan)
		return
	}
	if queuedEvent != nil {
		al.emitTrackedSubagentResultQueued(*queuedEvent)
	}
	if scheduleRoot {
		al.maybeStartTrackedSubagentResultPump(rootScope)
	}
}

func validateTrackedSubagentResult(
	route trackedSubagentResultRoute,
	completion tools.SubagentCompletion,
	result *tools.ToolResult,
) string {
	switch {
	case route.SourceTurnID == "", route.SourceAgentID == "", route.SourceSessionKey == "",
		route.RootTurnID == "", route.RootAgentID == "", route.RootSessionKey == "":
		return "invalid_parent_route"
	case route.RootChannel == "" || route.RootChatID == "":
		return "invalid_parent_route"
	case completion.TaskID == "":
		return "missing_task_identity"
	case completion.Status != "completed" && completion.Status != "failed" &&
		completion.Status != "canceled":
		return "invalid_task_status"
	case result == nil:
		return "nil_result"
	default:
		return ""
	}
}

func trackedSubagentResultFingerprint(
	route trackedSubagentResultRoute,
	completion tools.SubagentCompletion,
	content string,
) [sha256.Size]byte {
	digest := sha256.New()
	writeField := func(value string) {
		_, _ = fmt.Fprintf(digest, "%d:", len(value))
		_, _ = digest.Write([]byte(value))
	}
	for _, value := range []string{
		route.SourceTurnID,
		route.SourceAgentID,
		route.SourceSessionKey,
		route.RootTurnID,
		route.RootAgentID,
		route.RootSessionKey,
		route.RootChannel,
		route.RootChatID,
		route.RootInbound.Channel,
		route.RootInbound.Account,
		route.RootInbound.ChatID,
		route.RootInbound.ChatType,
		route.RootInbound.TopicID,
		route.RootInbound.SpaceID,
		route.RootInbound.SpaceType,
		route.RootInbound.SenderID,
		route.RootInbound.ConversationName,
		completion.TaskID,
		completion.Status,
		string(route.RootProfile.HistoryMode),
		string(route.RootProfile.SystemPromptMode),
		string(route.RootProfile.SkillsMode),
		string(route.RootProfile.ToolsMode),
		strings.Join(route.RootProfile.AllowedSkills, "\x00"),
		strings.Join(route.RootProfile.AllowedTools, "\x00"),
		content,
	} {
		writeField(value)
	}
	if route.RootPersistent {
		writeField("persistent")
	} else {
		writeField("ephemeral")
	}
	writeField(fmt.Sprintf(
		"profile=%t;disable_tools=%t;suppress_context=%t;disable_cache=%t;late_allowed=%t;summary=%t",
		route.RootProfile.Enabled,
		route.RootDisableTools,
		route.RootSuppressContext,
		route.RootDisablePromptCache,
		route.RootLateContinuationAllowed,
		route.RootEnableSummary,
	))
	if route.RootScope == nil {
		writeField("nil-scope")
	} else {
		writeField(session.CanonicalScopeSignature(*route.RootScope))
	}
	var result [sha256.Size]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func boundTrackedSubagentResult(content string) string {
	// Bound before UTF-8 normalization. Keep at most one sequence beyond the
	// byte cap so a cut sequence becomes one replacement rune without retaining
	// the remaining policy-filtered input.
	if len(content) > maxTrackedSubagentResultBytes+utf8.UTFMax {
		content = content[:maxTrackedSubagentResultBytes+utf8.UTFMax]
	}
	content = strings.ToValidUTF8(content, "\uFFFD")
	if len(content) <= maxTrackedSubagentResultBytes {
		return strings.Clone(content)
	}
	end := maxTrackedSubagentResultBytes
	for end > 0 && !utf8.ValidString(content[:end]) {
		end--
	}
	return strings.Clone(content[:end])
}

func activeTurnMatchesTrackedResult(actual any, turnID, agentID string) bool {
	turn, ok := actual.(*turnState)
	if !ok || turn == nil {
		return false
	}
	// Identity fields are immutable after publication. Do not acquire turn.mu
	// under the session handoff lock: terminal cleanup takes those boundaries
	// in the opposite temporal order. A terminalizing but still-published turn
	// may accept the queue; its exact release notifier will rehome it.
	return turn.turnID == turnID && turn.agentID == agentID && !turn.isFinished.Load()
}

func (mailbox *trackedSubagentResultMailbox) initLocked() {
	if mailbox.records == nil {
		mailbox.records = make(map[trackedSubagentResultID]*trackedSubagentResultRecord)
	}
	if mailbox.scopes == nil {
		mailbox.scopes = make(map[trackedSubagentResultScope]*trackedSubagentResultScopeState)
	}
	if mailbox.released == nil {
		mailbox.released = make(map[string]trackedSubagentTurnRelease)
	}
	if mailbox.pendingBySource == nil {
		mailbox.pendingBySource = make(map[string]map[trackedSubagentResultID]struct{})
	}
	if mailbox.pendingByRoot == nil {
		mailbox.pendingByRoot = make(map[string]map[trackedSubagentResultID]struct{})
	}
	if mailbox.rootsBySession == nil {
		mailbox.rootsBySession = make(
			map[string]map[trackedSubagentResultScope]struct{},
		)
	}
	if mailbox.outputHolds == nil {
		mailbox.outputHolds = make(map[string]int)
	}
}

func (al *AgentLoop) watchTrackedSubagentResultRoute(route trackedSubagentResultRoute) {
	if al == nil || route.SourceTurnID == "" || route.RootTurnID == "" ||
		route.RootAgentID == "" || route.RootSessionKey == "" {
		return
	}
	al.trackedSubagentResults.trackedTurns.Store(route.SourceTurnID, struct{}{})
	al.trackedSubagentResults.trackedTurns.Store(route.RootTurnID, struct{}{})
	rootScope := trackedSubagentResultScope{
		AgentID: route.RootAgentID, SessionKey: route.RootSessionKey,
	}
	al.trackedSubagentResults.mu.Lock()
	al.trackedSubagentResults.initLocked()
	scopes := al.trackedSubagentResults.rootsBySession[route.RootSessionKey]
	if scopes == nil {
		scopes = make(map[trackedSubagentResultScope]struct{})
		al.trackedSubagentResults.rootsBySession[route.RootSessionKey] = scopes
	}
	scopes[rootScope] = struct{}{}
	al.trackedSubagentResults.mu.Unlock()
}

func (mailbox *trackedSubagentResultMailbox) indexPendingLocked(
	record *trackedSubagentResultRecord,
) {
	if record == nil {
		return
	}
	mailbox.initLocked()
	bySource := mailbox.pendingBySource[record.route.SourceTurnID]
	if bySource == nil {
		bySource = make(map[trackedSubagentResultID]struct{})
		mailbox.pendingBySource[record.route.SourceTurnID] = bySource
	}
	bySource[record.id] = struct{}{}
	byRoot := mailbox.pendingByRoot[record.route.RootTurnID]
	if byRoot == nil {
		byRoot = make(map[trackedSubagentResultID]struct{})
		mailbox.pendingByRoot[record.route.RootTurnID] = byRoot
	}
	byRoot[record.id] = struct{}{}
}

func (mailbox *trackedSubagentResultMailbox) unindexPendingLocked(
	record *trackedSubagentResultRecord,
) {
	if record == nil {
		return
	}
	deleteTrackedSubagentPendingIndex(
		mailbox.pendingBySource,
		record.route.SourceTurnID,
		record.id,
	)
	deleteTrackedSubagentPendingIndex(
		mailbox.pendingByRoot,
		record.route.RootTurnID,
		record.id,
	)
}

func deleteTrackedSubagentPendingIndex(
	index map[string]map[trackedSubagentResultID]struct{},
	turnID string,
	id trackedSubagentResultID,
) {
	entries := index[turnID]
	delete(entries, id)
	if len(entries) == 0 {
		delete(index, turnID)
	}
}

func (mailbox *trackedSubagentResultMailbox) pendingForTurnLocked(
	turnID string,
) []trackedSubagentResultID {
	seen := make(map[trackedSubagentResultID]struct{})
	for id := range mailbox.pendingBySource[turnID] {
		seen[id] = struct{}{}
	}
	for id := range mailbox.pendingByRoot[turnID] {
		seen[id] = struct{}{}
	}
	result := make([]trackedSubagentResultID, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	return result
}

func (mailbox *trackedSubagentResultMailbox) scopeLocked(
	scope trackedSubagentResultScope,
) *trackedSubagentResultScopeState {
	mailbox.initLocked()
	state := mailbox.scopes[scope]
	if state == nil {
		state = &trackedSubagentResultScopeState{}
		mailbox.scopes[scope] = state
	}
	return state
}

func (mailbox *trackedSubagentResultMailbox) enqueueLocked(
	scope trackedSubagentResultScope,
	id trackedSubagentResultID,
) {
	state := mailbox.scopeLocked(scope)
	state.queue = append(state.queue, id)
}

func (mailbox *trackedSubagentResultMailbox) removeFromScopeLocked(
	scope trackedSubagentResultScope,
	id trackedSubagentResultID,
) {
	state := mailbox.scopes[scope]
	if state == nil {
		return
	}
	for index, candidate := range state.queue {
		if candidate != id {
			continue
		}
		copy(state.queue[index:], state.queue[index+1:])
		state.queue = state.queue[:len(state.queue)-1]
		return
	}
}

func (al *AgentLoop) dequeueTrackedSubagentResult(
	ts *turnState,
) (providers.Message, bool) {
	if al == nil || ts == nil {
		return providers.Message{}, false
	}
	scope := trackedSubagentResultScope{
		AgentID: strings.TrimSpace(ts.agentID), SessionKey: strings.TrimSpace(ts.sessionKey),
	}
	if scope.AgentID == "" || scope.SessionKey == "" || strings.TrimSpace(ts.turnID) == "" {
		return providers.Message{}, false
	}

	unlock := al.lockSessionTurn(scope.SessionKey)
	actual, active := al.activeTurnStates.Load(scope.SessionKey)
	if !active || actual != ts {
		unlock()
		return providers.Message{}, false
	}
	al.trackedSubagentResults.mu.Lock()
	claim, ok := al.trackedSubagentResults.claimForTurnLocked(
		scope,
		ts.turnID,
		ts.channel,
		ts.chatID,
		ts.opts.Dispatch.SessionScope,
	)
	al.trackedSubagentResults.mu.Unlock()
	unlock()
	if !ok {
		return providers.Message{}, false
	}

	if cfg := al.GetConfig(); cfg != nil {
		claim.content = cfg.FilterSensitiveData(claim.content)
	}
	claim.content = boundTrackedSubagentResult(claim.content)
	message := trackedSubagentResultPromptMessage(claim)
	al.emitTrackedSubagentEventSafely(
		runtimeevents.KindAgentSubTurnResultDelivered,
		ts.eventMeta("trackedSubagentResultMailbox", "subturn.result.delivered"),
		SubTurnResultDeliveredPayload{
			TargetChannel: claim.route.RootChannel,
			TargetChatID:  claim.route.RootChatID,
			SourceTurnID:  claim.id.SourceTurnID,
			TaskID:        claim.completion.TaskID,
			Status:        claim.completion.Status,
			ContentLen:    len(claim.content),
		},
	)
	return message, true
}

func (mailbox *trackedSubagentResultMailbox) claimForTurnLocked(
	scope trackedSubagentResultScope,
	turnID, channel, chatID string,
	turnScope *session.SessionScope,
) (trackedSubagentResultClaim, bool) {
	state := mailbox.scopes[scope]
	if state == nil {
		return trackedSubagentResultClaim{}, false
	}
	for index, id := range state.queue {
		record := mailbox.records[id]
		if record == nil {
			continue
		}
		claimable := record.state == trackedSubagentResultPendingPreferred &&
			record.route.SourceTurnID == turnID &&
			record.route.SourceAgentID == scope.AgentID &&
			record.route.SourceSessionKey == scope.SessionKey
		if record.state == trackedSubagentResultPendingRoot &&
			record.route.RootAgentID == scope.AgentID &&
			record.route.RootSessionKey == scope.SessionKey &&
			(record.route.RootTurnID == turnID || record.rootEligible) &&
			trackedSubagentResultRouteMatchesTurn(record.route, channel, chatID, turnScope) {
			claimable = true
		}
		if !claimable {
			continue
		}
		copy(state.queue[index:], state.queue[index+1:])
		state.queue = state.queue[:len(state.queue)-1]
		claim := trackedSubagentResultClaim{
			id: id, route: cloneTrackedSubagentResultRoute(record.route),
			completion: record.completion, content: record.content,
		}
		mailbox.unindexPendingLocked(record)
		record.state = trackedSubagentResultClaimed
		if root := mailbox.scopes[trackedSubagentResultScope{
			AgentID: record.route.RootAgentID, SessionKey: record.route.RootSessionKey,
		}]; root != nil && root.pending > 0 {
			root.pending--
		}
		compactTrackedSubagentTerminalRecord(record)
		return claim, true
	}
	return trackedSubagentResultClaim{}, false
}

func trackedSubagentResultRouteMatchesTurn(
	route trackedSubagentResultRoute,
	channel, chatID string,
	turnScope *session.SessionScope,
) bool {
	if route.RootChannel != channel || route.RootChatID != chatID {
		return false
	}
	if route.RootScope == nil {
		return true
	}
	return turnScope != nil && session.CanonicalScopeSignature(*route.RootScope) ==
		session.CanonicalScopeSignature(*turnScope)
}

func trackedSubagentResultPromptMessage(
	claim trackedSubagentResultClaim,
) providers.Message {
	content := fmt.Sprintf(
		"[Subagent Result task_id=%s status=%s source_turn_id=%s] %s",
		claim.completion.TaskID,
		claim.completion.Status,
		claim.id.SourceTurnID,
		claim.content,
	)
	return promptMessageWithMetadata(
		providers.Message{Role: "user", Content: content},
		PromptLayerTurn,
		PromptSlotSubTurn,
		PromptSourceSubTurnResult,
	)
}

func (al *AgentLoop) handleTrackedSubagentResultTurnReleased(ts *turnState) {
	if al == nil || ts == nil {
		return
	}
	ts.mu.RLock()
	turnID := strings.TrimSpace(ts.turnID)
	status := ts.terminalStatus
	ts.mu.RUnlock()
	if turnID == "" {
		return
	}
	if status == "" {
		status = TurnEndStatusError
	}
	if _, tracked := al.trackedSubagentResults.trackedTurns.Load(turnID); !tracked {
		return
	}

	var (
		orphans    []trackedSubagentResultOrphan
		toSchedule = make(map[trackedSubagentResultScope]struct{})
	)
	al.trackedSubagentResults.mu.Lock()
	al.trackedSubagentResults.initLocked()
	previous := al.trackedSubagentResults.released[turnID]
	al.trackedSubagentResults.released[turnID] = trackedSubagentTurnRelease{
		status: status, released: true, outputReady: previous.outputReady,
	}
	for _, id := range al.trackedSubagentResults.pendingForTurnLocked(turnID) {
		record := al.trackedSubagentResults.records[id]
		if record == nil || (record.state != trackedSubagentResultPendingPreferred &&
			record.state != trackedSubagentResultPendingRoot) {
			continue
		}
		rootScope := trackedSubagentResultScope{
			AgentID: record.route.RootAgentID, SessionKey: record.route.RootSessionKey,
		}
		if record.route.RootTurnID == turnID && status != TurnEndStatusCompleted {
			if al.trackedSubagentResults.orphanLocked(record, "root_failed") {
				orphans = append(orphans, trackedSubagentResultOrphan{
					route: record.route, taskID: record.completion.TaskID,
					status: record.completion.Status, reason: "root_failed",
				})
				compactTrackedSubagentTerminalRecord(record)
			}
			continue
		}
		if record.route.RootTurnID == turnID && status == TurnEndStatusCompleted &&
			(!record.route.RootPersistent || !record.route.RootLateContinuationAllowed) &&
			(record.state == trackedSubagentResultPendingRoot ||
				record.route.SourceTurnID == turnID) {
			orphanReason := "root_continuation_policy"
			if !record.route.RootPersistent {
				orphanReason = "root_not_persistent"
			}
			if al.trackedSubagentResults.orphanLocked(record, orphanReason) {
				orphans = append(orphans, trackedSubagentResultOrphan{
					route: record.route, taskID: record.completion.TaskID,
					status: record.completion.Status, reason: orphanReason,
				})
				compactTrackedSubagentTerminalRecord(record)
			}
			continue
		}
		if record.state == trackedSubagentResultPendingPreferred &&
			record.route.SourceTurnID == turnID {
			rootRelease, rootKnown := al.trackedSubagentResults.released[record.route.RootTurnID]
			if rootKnown && rootRelease.released && !record.route.RootPersistent {
				orphanReason := "root_not_persistent"
				if al.trackedSubagentResults.orphanLocked(record, orphanReason) {
					orphans = append(orphans, trackedSubagentResultOrphan{
						route: record.route, taskID: record.completion.TaskID,
						status: record.completion.Status, reason: orphanReason,
					})
					compactTrackedSubagentTerminalRecord(record)
				}
				continue
			}
			if rootKnown && rootRelease.released &&
				!record.route.RootLateContinuationAllowed {
				if al.trackedSubagentResults.orphanLocked(record, "root_continuation_policy") {
					orphans = append(orphans, trackedSubagentResultOrphan{
						route: record.route, taskID: record.completion.TaskID,
						status: record.completion.Status, reason: "root_continuation_policy",
					})
					compactTrackedSubagentTerminalRecord(record)
				}
				continue
			}
			if rootKnown && rootRelease.status != TurnEndStatusCompleted {
				if al.trackedSubagentResults.orphanLocked(record, "root_failed") {
					orphans = append(orphans, trackedSubagentResultOrphan{
						route: record.route, taskID: record.completion.TaskID,
						status: record.completion.Status, reason: "root_failed",
					})
					compactTrackedSubagentTerminalRecord(record)
				}
				continue
			}
			al.trackedSubagentResults.removeFromScopeLocked(record.currentScope, record.id)
			record.state = trackedSubagentResultPendingRoot
			record.currentScope = rootScope
			if rootRelease, known := al.trackedSubagentResults.released[record.route.RootTurnID]; known {
				record.rootEligible = rootRelease.released &&
					rootRelease.outputReady &&
					rootRelease.status == TurnEndStatusCompleted
			}
			al.trackedSubagentResults.enqueueLocked(rootScope, record.id)
		}
		if record.state == trackedSubagentResultPendingRoot &&
			record.route.RootTurnID == turnID && status == TurnEndStatusCompleted {
			record.rootEligible = previous.outputReady
		}
		if record.state == trackedSubagentResultPendingRoot && record.rootEligible {
			toSchedule[rootScope] = struct{}{}
		}
	}
	al.trackedSubagentResults.mu.Unlock()

	for _, orphan := range orphans {
		al.emitTrackedSubagentResultOrphan(orphan)
	}
	for scope := range toSchedule {
		al.maybeStartTrackedSubagentResultPump(scope)
	}
}

// noteTrackedSubagentResultTurnTerminalLocked runs while the committing turn
// holds turnState.mu. No mailbox path acquires a turn lock, so a failed root's
// terminal outcome becomes visible at the same mailbox boundary as competing
// child-result claims. A successful root is not continuation-eligible until
// exact active ownership is released later.
func (al *AgentLoop) noteTrackedSubagentResultTurnTerminalLocked(
	turnID string,
	status TurnEndStatus,
) []trackedSubagentResultOrphan {
	if al == nil || strings.TrimSpace(turnID) == "" || status == "" {
		return nil
	}
	if _, tracked := al.trackedSubagentResults.trackedTurns.Load(turnID); !tracked {
		return nil
	}
	var orphans []trackedSubagentResultOrphan
	al.trackedSubagentResults.mu.Lock()
	defer al.trackedSubagentResults.mu.Unlock()
	al.trackedSubagentResults.initLocked()
	previous := al.trackedSubagentResults.released[turnID]
	al.trackedSubagentResults.released[turnID] = trackedSubagentTurnRelease{
		status: status, released: previous.released, outputReady: previous.outputReady,
	}
	if status != TurnEndStatusCompleted {
		for id := range al.trackedSubagentResults.pendingByRoot[turnID] {
			record := al.trackedSubagentResults.records[id]
			if record == nil {
				continue
			}
			if al.trackedSubagentResults.orphanLocked(record, "root_failed") {
				orphans = append(orphans, trackedSubagentResultOrphan{
					route: record.route, taskID: record.completion.TaskID,
					status: record.completion.Status, reason: "root_failed",
				})
				compactTrackedSubagentTerminalRecord(record)
			}
		}
	}
	return orphans
}

func (al *AgentLoop) noteTrackedSubagentResultTurnTerminalSafely(
	turnID string,
	status TurnEndStatus,
) (orphans []trackedSubagentResultOrphan) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.ErrorSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentTrackedSubagentTurnTerminalPanicRecovered,
				logger.NewSafeFields(
					agentDiagnosticTurnField(turnID),
					agentDiagnosticReasonField(string(status)),
					agentDiagnosticPanicField(recovered),
				),
			)
			orphans = nil
		}
	}()
	return al.noteTrackedSubagentResultTurnTerminalLocked(turnID, status)
}

func (mailbox *trackedSubagentResultMailbox) orphanLocked(
	record *trackedSubagentResultRecord,
	reason string,
) bool {
	if record == nil || (record.state != trackedSubagentResultPendingPreferred &&
		record.state != trackedSubagentResultPendingRoot) {
		return false
	}
	mailbox.removeFromScopeLocked(record.currentScope, record.id)
	mailbox.unindexPendingLocked(record)
	if root := mailbox.scopes[trackedSubagentResultScope{
		AgentID: record.route.RootAgentID, SessionKey: record.route.RootSessionKey,
	}]; root != nil && root.pending > 0 {
		root.pending--
	}
	record.state = trackedSubagentResultOrphaned
	record.orphanReason = reason
	return true
}

func compactTrackedSubagentTerminalRecord(record *trackedSubagentResultRecord) {
	if record == nil {
		return
	}
	record.content = ""
	record.route = trackedSubagentResultRoute{}
	record.currentScope = trackedSubagentResultScope{}
	record.rootEligible = false
	record.preflightAttempts = 0
	record.preflightNotBefore = time.Time{}
}

func (al *AgentLoop) maybeStartTrackedSubagentResultPump(
	scope trackedSubagentResultScope,
) {
	if al == nil || scope.AgentID == "" || scope.SessionKey == "" {
		return
	}
	var (
		id    trackedSubagentResultID
		route trackedSubagentResultRoute
		start bool
	)
	unlock := al.lockSessionTurn(scope.SessionKey)
	if _, active := al.activeTurnStates.Load(scope.SessionKey); !active {
		al.trackedSubagentResults.mu.Lock()
		state := al.trackedSubagentResults.scopes[scope]
		if state != nil && !state.pumping &&
			al.trackedSubagentResults.outputHolds[scope.SessionKey] == 0 {
			for _, candidate := range state.queue {
				record := al.trackedSubagentResults.records[candidate]
				terminal := al.trackedSubagentResults.released[recordRouteRootTurnID(record)]
				if record != nil && record.state == trackedSubagentResultPendingRoot &&
					record.rootEligible && terminal.outputReady &&
					!time.Now().Before(record.preflightNotBefore) {
					id = candidate
					route = cloneTrackedSubagentResultRoute(record.route)
					state.pumping = true
					start = true
					break
				}
			}
		}
		al.trackedSubagentResults.mu.Unlock()
	}
	unlock()
	if start {
		go al.runTrackedSubagentResultPump(scope, id, route)
	}
}

func recordRouteRootTurnID(record *trackedSubagentResultRecord) string {
	if record == nil {
		return ""
	}
	return record.route.RootTurnID
}

func (al *AgentLoop) markTrackedSubagentResultOutputReady(rootTurnID string) {
	if al == nil || strings.TrimSpace(rootTurnID) == "" {
		return
	}
	toSchedule := make(map[trackedSubagentResultScope]struct{})
	al.trackedSubagentResults.mu.Lock()
	al.trackedSubagentResults.initLocked()
	terminal, known := al.trackedSubagentResults.released[rootTurnID]
	if known {
		terminal.outputReady = true
		al.trackedSubagentResults.released[rootTurnID] = terminal
	}
	if known && terminal.released && terminal.status == TurnEndStatusCompleted {
		for id := range al.trackedSubagentResults.pendingByRoot[rootTurnID] {
			record := al.trackedSubagentResults.records[id]
			if record == nil || record.state != trackedSubagentResultPendingRoot {
				continue
			}
			record.rootEligible = true
			toSchedule[trackedSubagentResultScope{
				AgentID: record.route.RootAgentID, SessionKey: record.route.RootSessionKey,
			}] = struct{}{}
		}
	}
	al.trackedSubagentResults.mu.Unlock()
	for scope := range toSchedule {
		al.maybeStartTrackedSubagentResultPump(scope)
	}
}

func (al *AgentLoop) wakeTrackedSubagentResultsForSession(sessionKey string) {
	if al == nil || strings.TrimSpace(sessionKey) == "" {
		return
	}
	scopes := make(map[trackedSubagentResultScope]struct{})
	al.trackedSubagentResults.mu.Lock()
	for scope := range al.trackedSubagentResults.rootsBySession[sessionKey] {
		scopes[scope] = struct{}{}
	}
	al.trackedSubagentResults.mu.Unlock()
	for scope := range scopes {
		al.maybeStartTrackedSubagentResultPump(scope)
	}
}

func (al *AgentLoop) runTrackedSubagentResultPump(
	scope trackedSubagentResultScope,
	id trackedSubagentResultID,
	route trackedSubagentResultRoute,
) {
	reschedule := true
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.ErrorSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentTrackedSubagentResultPumpPanicRecovered,
				logger.NewSafeFields(
					agentDiagnosticAgentField(scope.AgentID),
					agentDiagnosticSessionField(scope.SessionKey),
					agentDiagnosticParentTurnField(id.SourceTurnID),
					agentDiagnosticTaskField(id.TaskID),
					agentDiagnosticPanicField(recovered),
				),
			)
			al.orphanTrackedSubagentResult(id, "continuation_panic")
		}
		al.finishTrackedSubagentResultPump(scope, reschedule)
	}()

	// Admission itself may wait through an arbitrarily slow but valid reload;
	// pending mailbox data owns no lease and must not turn that pause into loss.
	// Once one coherent generation admits the worker, bound all continuation
	// setup/provider/output work in that generation.
	ctx, releaseRuntime, err := al.acquireRuntimeUse(al.trackedSubagentWorkerContext())
	if err != nil {
		al.orphanTrackedSubagentResult(id, "runtime_unavailable")
		return
	}
	defer releaseRuntime()
	continuationTimeout := al.getSubTurnConfig().defaultTimeout
	if continuationTimeout <= 0 {
		continuationTimeout = defaultSubTurnTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, continuationTimeout)
	defer cancel()
	outputOwner := &trackedSubagentResultOutputOwner{}
	ctx = withTrackedSubagentResultOutputOwner(ctx, outputOwner)

	agent, snapshot, preflightReason, err := al.preflightTrackedSubagentResultContinuation(
		ctx,
		route,
	)
	if err != nil {
		if (preflightReason == "runtime_setup_failed" ||
			preflightReason == "session_snapshot_failed") &&
			al.retryTrackedSubagentResultPreflight(id, scope) {
			reschedule = false
			return
		}
		al.orphanTrackedSubagentResult(id, preflightReason)
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentTrackedSubagentResultContinuationRejected,
			logger.NewSafeFields(
				agentDiagnosticAgentField(route.RootAgentID),
				agentDiagnosticTaskField(id.TaskID),
				agentDiagnosticReasonField(preflightReason),
			),
		)
		return
	}

	claim, placeholder, claimed := al.claimTrackedSubagentResultForContinuation(
		scope, id, route,
	)
	if !claimed {
		return
	}
	defer func() {
		outputOwner.release(al)
		al.releaseTrackedSubagentOutputTurn(route.RootSessionKey, placeholder)
		al.maybeStartTrackedSubagentSteeringRescue(route)
	}()

	if cfg := al.GetConfig(); cfg != nil {
		claim.content = cfg.FilterSensitiveData(claim.content)
	}
	claim.content = boundTrackedSubagentResult(claim.content)
	resultMessage := trackedSubagentResultPromptMessage(claim)
	initialMessages := []providers.Message{resultMessage}
	al.emitTrackedSubagentEventSafely(
		runtimeevents.KindAgentSubTurnResultDelivered,
		HookMeta{
			AgentID: route.RootAgentID, TurnID: placeholder.turnID,
			SessionKey: route.RootSessionKey, Source: "trackedSubagentResultMailbox",
			TracePath: "subturn.result.delivered",
		},
		SubTurnResultDeliveredPayload{
			TargetChannel: route.RootChannel,
			TargetChatID:  route.RootChatID,
			SourceTurnID:  claim.id.SourceTurnID,
			TaskID:        claim.completion.TaskID,
			Status:        claim.completion.Status,
			ContentLen:    len(claim.content),
		},
	)

	resetTrackedSubagentMessageTool(agent, route.RootSessionKey)
	response, runErr := al.continueTrackedSubagentResultMessages(
		ctx,
		agent,
		snapshot.Scope,
		route,
		placeholder,
		initialMessages,
	)
	if runErr != nil {
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentTrackedSubagentResultContinuationFailed,
			logger.NewSafeFields(
				agentDiagnosticAgentField(route.RootAgentID),
				agentDiagnosticTaskField(id.TaskID),
			),
		)
	}
	if runErr != nil && response == "" {
		return
	}
	if response != "" {
		if !al.publishTrackedSubagentResultResponse(
			ctx,
			agent,
			snapshot.Scope,
			route,
			response,
		) {
			logger.WarnSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentTrackedSubagentResultOutboundWasNotAccepted,
				logger.NewSafeFields(
					agentDiagnosticAgentField(route.RootAgentID),
					agentDiagnosticTaskField(id.TaskID),
				),
			)
		}
	}
}

func (al *AgentLoop) continueTrackedSubagentResultMessages(
	ctx context.Context,
	agent *AgentInstance,
	scope *session.SessionScope,
	route trackedSubagentResultRoute,
	reservation *turnState,
	messages []providers.Message,
) (string, error) {
	dispatch := DispatchRequest{
		SessionKey:     route.RootSessionKey,
		SessionScope:   session.CloneScope(scope),
		InboundContext: cloneInboundContext(&route.RootInbound),
	}
	return al.runAgentLoop(ctx, agent, processOptions{
		Dispatch:                 dispatch,
		TurnProfile:              cloneTrackedSubagentTurnProfile(route.RootProfile),
		SuppressDefaultContext:   route.RootSuppressContext,
		DefaultResponse:          defaultResponse,
		EnableSummary:            route.RootEnableSummary,
		SendResponse:             false,
		SuppressToolFeedback:     true,
		DisableTools:             route.RootDisableTools,
		DisablePromptCache:       route.RootDisablePromptCache,
		InitialSteeringMessages:  messages,
		SkipInitialSteeringPoll:  true,
		trackedResultOutputOwner: trackedSubagentResultOutputOwnerFromContext(ctx),
		requireExistingSession:   true,
		retainSessionUntilOutput: true,
		turnReservation:          reservation,
	})
}

func validateTrackedSubagentExistingSession(
	ctx context.Context,
	agent *AgentInstance,
	sessionKey string,
	expectedScope *session.SessionScope,
) error {
	if agent == nil || agent.Sessions == nil {
		return fmt.Errorf("named agent session store is unavailable")
	}
	reader, ok := agent.Sessions.(session.SnapshotReader)
	if !ok {
		return fmt.Errorf("named session store lacks strict snapshots")
	}
	snapshot, found, err := reader.ReadSessionSnapshot(ctx, sessionKey)
	if err != nil {
		return err
	}
	if !found || snapshot.Key != sessionKey {
		return fmt.Errorf("named session does not exist canonically")
	}
	if snapshot.Scope != nil &&
		(snapshot.Scope.AgentID != agent.ID || isReviewSessionScope(snapshot.Scope)) {
		return fmt.Errorf("named session owner is invalid")
	}
	if snapshot.Scope == nil {
		if metadata, ok := agent.Sessions.(session.MetadataAwareSessionStore); ok {
			snapshot.Scope = metadata.GetSessionScope(sessionKey)
		}
	}
	if snapshot.Scope != nil &&
		(snapshot.Scope.AgentID != agent.ID || isReviewSessionScope(snapshot.Scope)) {
		return fmt.Errorf("named session owner is invalid")
	}
	if expectedScope != nil && (snapshot.Scope == nil ||
		session.CanonicalScopeSignature(*snapshot.Scope) !=
			session.CanonicalScopeSignature(*expectedScope)) {
		return fmt.Errorf("named session scope changed")
	}
	return nil
}

func (al *AgentLoop) runTrackedSubagentSteeringContinuation(
	ctx context.Context,
	agent *AgentInstance,
	scope *session.SessionScope,
	route trackedSubagentResultRoute,
	placeholder *turnState,
	messages []providers.Message,
) (string, error) {
	resetTrackedSubagentMessageTool(agent, route.RootSessionKey)
	return al.continueTrackedSubagentResultMessages(
		ctx,
		agent,
		scope,
		route,
		placeholder,
		messages,
	)
}

func (al *AgentLoop) releaseTrackedSubagentOutputTurn(
	sessionKey string,
	reservation *turnState,
) bool {
	if al == nil || reservation == nil {
		return false
	}
	var releasedTurn *turnState
	unlock := al.lockSessionTurn(sessionKey)
	actual, loaded := al.activeTurnStates.Load(sessionKey)
	turn, isTurn := actual.(*turnState)
	if loaded && isTurn && turn != nil &&
		(turn == reservation || turn.opts.turnReservation == reservation) {
		al.activeTurnStates.Delete(sessionKey)
		releasedTurn = turn
	}
	unlock()
	if releasedTurn == nil {
		return false
	}
	if releasedTurn != reservation {
		al.handleTrackedSubagentResultTurnReleased(releasedTurn)
	}
	al.wakeTrackedSubagentResultsForSession(sessionKey)
	return true
}

func resetTrackedSubagentMessageTool(agent *AgentInstance, sessionKey string) {
	if agent == nil || agent.Tools == nil {
		return
	}
	if tool, ok := agent.Tools.Get("message"); ok {
		if resetter, ok := tool.(interface{ ResetSentInRound(sessionKey string) }); ok {
			resetter.ResetSentInRound(sessionKey)
		}
	}
}

func (al *AgentLoop) maybeStartTrackedSubagentSteeringRescue(
	route trackedSubagentResultRoute,
) {
	if al == nil || al.steering == nil {
		return
	}
	scope := trackedSubagentResultScope{
		AgentID: route.RootAgentID, SessionKey: route.RootSessionKey,
	}
	start := false
	wakeAfterHold := false
	unlock := al.lockSessionTurn(route.RootSessionKey)
	if _, active := al.activeTurnStates.Load(route.RootSessionKey); !active &&
		al.steering.lenScope(route.RootSessionKey) > 0 {
		al.trackedSubagentResults.mu.Lock()
		state := al.trackedSubagentResults.scopeLocked(scope)
		held := al.trackedSubagentResults.outputHolds[route.RootSessionKey] != 0
		if held && !state.steeringWakeScheduled {
			state.steeringWakeScheduled = true
			wakeAfterHold = true
		}
		if !held && !state.rescuingSteering &&
			!time.Now().Before(state.steeringRescueNotBefore) {
			state.rescuingSteering = true
			start = true
		}
		al.trackedSubagentResults.mu.Unlock()
	}
	unlock()
	if start {
		go al.runTrackedSubagentSteeringRescue(scope, route)
	}
	if wakeAfterHold {
		time.AfterFunc(25*time.Millisecond, func() {
			al.trackedSubagentResults.mu.Lock()
			if state := al.trackedSubagentResults.scopes[scope]; state != nil {
				state.steeringWakeScheduled = false
			}
			al.trackedSubagentResults.mu.Unlock()
			al.maybeStartTrackedSubagentSteeringRescue(route)
		})
	}
}

func (al *AgentLoop) runTrackedSubagentSteeringRescue(
	scope trackedSubagentResultScope,
	route trackedSubagentResultRoute,
) {
	retryMode := "immediate"
	var retryDelay time.Duration
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.ErrorSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentTrackedSubagentSteeringRescuePanicRecovered,
				logger.NewSafeFields(
					agentDiagnosticAgentField(route.RootAgentID),
					agentDiagnosticSessionField(route.RootSessionKey),
					agentDiagnosticParentTurnField(route.RootTurnID),
					agentDiagnosticPanicField(recovered),
				),
			)
			retryDelay, retryMode = al.nextTrackedSubagentSteeringRescueRetry(scope)
			if retryMode == "none" {
				al.clearTrackedSubagentSteeringRescue(scope, route.RootSessionKey)
			}
		}
		al.trackedSubagentResults.mu.Lock()
		if state := al.trackedSubagentResults.scopes[scope]; state != nil {
			state.rescuingSteering = false
		}
		al.trackedSubagentResults.mu.Unlock()
		switch retryMode {
		case "immediate":
			al.maybeStartTrackedSubagentSteeringRescue(route)
		case "delayed":
			time.AfterFunc(retryDelay, func() {
				al.maybeStartTrackedSubagentSteeringRescue(route)
			})
		}
	}()

	ctx, releaseRuntime, err := al.acquireRuntimeUse(al.trackedSubagentWorkerContext())
	if err != nil {
		retryMode = "none"
		return
	}
	defer releaseRuntime()
	rescueTimeout := al.getSubTurnConfig().defaultTimeout
	if rescueTimeout <= 0 {
		rescueTimeout = defaultSubTurnTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, rescueTimeout)
	defer cancel()
	agent, snapshot, reason, err := al.preflightTrackedSubagentResultContinuation(ctx, route)
	if err != nil {
		if reason == "runtime_setup_failed" || reason == "session_snapshot_failed" {
			retryDelay, retryMode = al.nextTrackedSubagentSteeringRescueRetry(scope)
			if retryMode == "none" {
				al.clearTrackedSubagentSteeringRescue(scope, route.RootSessionKey)
			}
		} else {
			retryMode = "none"
			al.clearTrackedSubagentSteeringRescue(scope, route.RootSessionKey)
		}
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentTrackedSubagentSteeringRescueRejected,
			logger.NewSafeFields(
				agentDiagnosticAgentField(route.RootAgentID),
				agentDiagnosticReasonField(reason),
			),
		)
		return
	}
	owner := &trackedSubagentResultOutputOwner{}
	ctx = withTrackedSubagentResultOutputOwner(ctx, owner)
	if err := validateTrackedSubagentExistingSession(
		ctx,
		agent,
		route.RootSessionKey,
		snapshot.Scope,
	); err != nil {
		retryDelay, retryMode = al.nextTrackedSubagentSteeringRescueRetry(scope)
		if retryMode == "none" {
			al.clearTrackedSubagentSteeringRescue(scope, route.RootSessionKey)
		}
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentTrackedSubagentSteeringRescueRecheckFailed,
			logger.NewSafeFields(agentDiagnosticAgentField(route.RootAgentID)),
		)
		return
	}
	placeholder, messages, claimed := al.claimTrackedSubagentSteeringContinuation(route)
	if !claimed {
		return
	}
	defer func() {
		owner.release(al)
		al.releaseTrackedSubagentOutputTurn(route.RootSessionKey, placeholder)
	}()
	response, runErr := al.runTrackedSubagentSteeringContinuation(
		ctx,
		agent,
		snapshot.Scope,
		route,
		placeholder,
		messages,
	)
	if runErr != nil {
		response = formatProcessingError(runErr)
		retryMode = "immediate"
		al.resetTrackedSubagentSteeringRescueAttempts(scope)
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentTrackedSubagentSteeringRescueFailed,
			logger.NewSafeFields(agentDiagnosticAgentField(route.RootAgentID)),
		)
	}
	if runErr == nil {
		al.resetTrackedSubagentSteeringRescueAttempts(scope)
	}
	if response != "" {
		_ = al.publishTrackedSubagentResultResponse(ctx, agent, snapshot.Scope, route, response)
	}
}

func (al *AgentLoop) nextTrackedSubagentSteeringRescueRetry(
	scope trackedSubagentResultScope,
) (time.Duration, string) {
	const maxAttempts = 3
	al.trackedSubagentResults.mu.Lock()
	state := al.trackedSubagentResults.scopeLocked(scope)
	if state.steeringRescueAttempts >= maxAttempts {
		al.trackedSubagentResults.mu.Unlock()
		return 0, "none"
	}
	state.steeringRescueAttempts++
	attempt := state.steeringRescueAttempts
	delay := time.Duration(attempt) * 50 * time.Millisecond
	state.steeringRescueNotBefore = time.Now().Add(delay)
	al.trackedSubagentResults.mu.Unlock()
	return delay, "delayed"
}

func (al *AgentLoop) resetTrackedSubagentSteeringRescueAttempts(
	scope trackedSubagentResultScope,
) {
	al.trackedSubagentResults.mu.Lock()
	if state := al.trackedSubagentResults.scopes[scope]; state != nil {
		state.steeringRescueAttempts = 0
		state.steeringRescueNotBefore = time.Time{}
	}
	al.trackedSubagentResults.mu.Unlock()
}

func (al *AgentLoop) clearTrackedSubagentSteeringRescue(
	scope trackedSubagentResultScope,
	sessionKey string,
) {
	al.clearSteeringMessagesForScope(sessionKey)
	al.resetTrackedSubagentSteeringRescueAttempts(scope)
}

func (al *AgentLoop) claimTrackedSubagentSteeringContinuation(
	route trackedSubagentResultRoute,
) (*turnState, []providers.Message, bool) {
	if al.steering == nil {
		return nil, nil, false
	}
	unlock := al.lockSessionTurn(route.RootSessionKey)
	defer unlock()
	if _, active := al.activeTurnStates.Load(route.RootSessionKey); active {
		return nil, nil, false
	}
	messages := al.steering.dequeueScope(route.RootSessionKey)
	if len(messages) == 0 {
		return nil, nil, false
	}
	placeholder := al.newTrackedSubagentResultPlaceholder(route)
	al.activeTurnStates.Store(route.RootSessionKey, placeholder)
	return placeholder, messages, true
}

func (al *AgentLoop) preflightTrackedSubagentResultContinuation(
	ctx context.Context,
	route trackedSubagentResultRoute,
) (*AgentInstance, session.SessionSnapshot, string, error) {
	if err := al.ensureHooksInitialized(ctx); err != nil {
		return nil, session.SessionSnapshot{}, "runtime_setup_failed", err
	}
	if err := al.ensureMCPInitialized(ctx); err != nil {
		return nil, session.SessionSnapshot{}, "runtime_setup_failed", err
	}
	registry := al.GetRegistry()
	if registry == nil {
		return nil, session.SessionSnapshot{}, "invalid_named_session",
			fmt.Errorf("agent registry is unavailable")
	}
	agent, ok := registry.GetAgent(route.RootAgentID)
	if !ok || agent == nil || agent.ID != route.RootAgentID {
		return nil, session.SessionSnapshot{}, "invalid_named_session",
			fmt.Errorf("named agent is unavailable")
	}
	reader, ok := agent.Sessions.(session.SnapshotReader)
	if !ok {
		return nil, session.SessionSnapshot{}, "invalid_named_session",
			fmt.Errorf("named session store lacks strict snapshots")
	}
	snapshot, found, err := reader.ReadSessionSnapshot(ctx, route.RootSessionKey)
	if err != nil {
		return nil, session.SessionSnapshot{}, "session_snapshot_failed", err
	}
	if !found || snapshot.Key != route.RootSessionKey {
		return nil, session.SessionSnapshot{}, "invalid_named_session",
			fmt.Errorf("named session does not exist canonically")
	}
	if snapshot.Scope != nil {
		if snapshot.Scope.AgentID != route.RootAgentID || isReviewSessionScope(snapshot.Scope) {
			return nil, session.SessionSnapshot{}, "invalid_named_session",
				fmt.Errorf("named session owner is invalid")
		}
	} else if metadata, ok := agent.Sessions.(session.MetadataAwareSessionStore); ok {
		snapshot.Scope = metadata.GetSessionScope(route.RootSessionKey)
		if snapshot.Scope != nil && (snapshot.Scope.AgentID != route.RootAgentID ||
			isReviewSessionScope(snapshot.Scope)) {
			return nil, session.SessionSnapshot{}, "invalid_named_session",
				fmt.Errorf("named session owner is invalid")
		}
	}
	if route.RootScope != nil {
		if snapshot.Scope == nil || session.CanonicalScopeSignature(*snapshot.Scope) !=
			session.CanonicalScopeSignature(*route.RootScope) {
			return nil, session.SessionSnapshot{}, "invalid_named_session",
				fmt.Errorf("named session scope changed")
		}
	}
	return agent, snapshot, "", nil
}

func (al *AgentLoop) claimTrackedSubagentResultForContinuation(
	scope trackedSubagentResultScope,
	id trackedSubagentResultID,
	route trackedSubagentResultRoute,
) (trackedSubagentResultClaim, *turnState, bool) {
	placeholder := al.newTrackedSubagentResultPlaceholder(route)

	unlock := al.lockSessionTurn(route.RootSessionKey)
	if _, active := al.activeTurnStates.Load(route.RootSessionKey); active {
		unlock()
		return trackedSubagentResultClaim{}, nil, false
	}
	al.trackedSubagentResults.mu.Lock()
	record := al.trackedSubagentResults.records[id]
	if record == nil || record.state != trackedSubagentResultPendingRoot ||
		!record.rootEligible || record.currentScope != scope ||
		al.trackedSubagentResults.outputHolds[route.RootSessionKey] != 0 {
		al.trackedSubagentResults.mu.Unlock()
		unlock()
		return trackedSubagentResultClaim{}, nil, false
	}
	al.activeTurnStates.Store(route.RootSessionKey, placeholder)
	al.trackedSubagentResults.removeFromScopeLocked(scope, id)
	claim := trackedSubagentResultClaim{
		id: id, route: cloneTrackedSubagentResultRoute(record.route),
		completion: record.completion, content: record.content,
	}
	al.trackedSubagentResults.unindexPendingLocked(record)
	record.state = trackedSubagentResultClaimed
	if root := al.trackedSubagentResults.scopes[scope]; root != nil && root.pending > 0 {
		root.pending--
	}
	compactTrackedSubagentTerminalRecord(record)
	al.trackedSubagentResults.mu.Unlock()
	unlock()
	return claim, placeholder, true
}

func (al *AgentLoop) newTrackedSubagentResultPlaceholder(
	route trackedSubagentResultRoute,
) *turnState {
	return &turnState{
		turnID: "pending-subagent-result-" + route.RootSessionKey + "-" +
			fmt.Sprintf("%d", al.turnSeq.Add(1)),
		agentID: route.RootAgentID, sessionKey: route.RootSessionKey,
		channel: route.RootChannel, chatID: route.RootChatID,
		handoffContext: cloneInboundContext(&route.RootInbound), phase: TurnPhaseSetup,
	}
}

func (al *AgentLoop) publishTrackedSubagentResultResponse(
	ctx context.Context,
	agent *AgentInstance,
	scope *session.SessionScope,
	route trackedSubagentResultRoute,
	response string,
) bool {
	if response == "" {
		return false
	}
	if trackedSubagentMessageToolSentTo(
		agent,
		route.RootSessionKey,
		route.RootChannel,
		route.RootChatID,
	) {
		return true
	}
	if al.bus == nil {
		return false
	}
	message := bus.OutboundMessage{
		Channel: route.RootChannel,
		ChatID:  route.RootChatID,
		Context: outboundContextFromInbound(
			&route.RootInbound,
			route.RootChannel,
			route.RootChatID,
			"",
		),
		AgentID:      route.RootAgentID,
		SessionKey:   route.RootSessionKey,
		Scope:        outboundScopeFromSessionScope(scope),
		Content:      response,
		ContextUsage: computeContextUsage(agent, route.RootSessionKey),
	}
	markFinalOutbound(&message)
	return al.bus.PublishOutbound(ctx, message) == nil
}

func trackedSubagentMessageToolSentTo(
	agent *AgentInstance,
	sessionKey, channel, chatID string,
) bool {
	if agent == nil || agent.Tools == nil {
		return false
	}
	tool, ok := agent.Tools.Get("message")
	if !ok {
		return false
	}
	messageTool, ok := tool.(*tools.MessageTool)
	return ok && messageTool.HasSentTo(sessionKey, channel, chatID)
}

func (al *AgentLoop) orphanTrackedSubagentResult(
	id trackedSubagentResultID,
	reason string,
) {
	var orphan *trackedSubagentResultOrphan
	al.trackedSubagentResults.mu.Lock()
	record := al.trackedSubagentResults.records[id]
	if al.trackedSubagentResults.orphanLocked(record, reason) {
		orphan = &trackedSubagentResultOrphan{
			route: record.route, taskID: record.completion.TaskID,
			status: record.completion.Status, reason: reason,
		}
		compactTrackedSubagentTerminalRecord(record)
	}
	al.trackedSubagentResults.mu.Unlock()
	if orphan != nil {
		al.emitTrackedSubagentResultOrphan(*orphan)
	}
}

func (al *AgentLoop) finishTrackedSubagentResultPump(
	scope trackedSubagentResultScope,
	reschedule bool,
) {
	al.trackedSubagentResults.mu.Lock()
	if state := al.trackedSubagentResults.scopes[scope]; state != nil {
		state.pumping = false
	}
	al.trackedSubagentResults.mu.Unlock()
	if reschedule {
		al.maybeStartTrackedSubagentResultPump(scope)
	}
}

func (al *AgentLoop) retryTrackedSubagentResultPreflight(
	id trackedSubagentResultID,
	scope trackedSubagentResultScope,
) bool {
	const maxPreflightAttempts = 3
	al.trackedSubagentResults.mu.Lock()
	record := al.trackedSubagentResults.records[id]
	if record == nil || record.state != trackedSubagentResultPendingRoot ||
		record.preflightAttempts >= maxPreflightAttempts {
		al.trackedSubagentResults.mu.Unlock()
		return false
	}
	record.preflightAttempts++
	attempt := record.preflightAttempts
	delay := time.Duration(attempt) * 50 * time.Millisecond
	record.preflightNotBefore = time.Now().Add(delay)
	if state := al.trackedSubagentResults.scopes[scope]; state != nil {
		state.pumping = false
	}
	al.trackedSubagentResults.mu.Unlock()
	time.AfterFunc(delay, func() {
		al.maybeStartTrackedSubagentResultPump(scope)
	})
	return true
}

func (al *AgentLoop) emitTrackedSubagentResultQueued(
	queued trackedSubagentResultQueued,
) {
	al.emitTrackedSubagentEventSafely(
		runtimeevents.KindAgentFollowUpQueued,
		HookMeta{
			AgentID: queued.route.SourceAgentID, TurnID: queued.route.SourceTurnID,
			SessionKey: queued.route.SourceSessionKey,
			Source:     "trackedSubagentResultMailbox", TracePath: "turn.follow_up.queued",
		},
		FollowUpQueuedPayload{
			SourceTool: "spawn", SourceTurnID: queued.route.SourceTurnID,
			TaskID: queued.completion.TaskID,
			Status: queued.completion.Status, ContentLen: queued.contentLen,
		},
	)
}

func (al *AgentLoop) emitTrackedSubagentResultOrphan(
	orphan trackedSubagentResultOrphan,
) {
	al.emitTrackedSubagentEventSafely(
		runtimeevents.KindAgentSubTurnOrphan,
		HookMeta{
			AgentID: orphan.route.SourceAgentID, TurnID: orphan.route.SourceTurnID,
			SessionKey: orphan.route.SourceSessionKey,
			Source:     "trackedSubagentResultMailbox", TracePath: "subturn.orphan",
		},
		SubTurnOrphanPayload{
			ParentTurnID: orphan.route.SourceTurnID,
			SourceTurnID: orphan.route.SourceTurnID,
			TaskID:       orphan.taskID,
			Status:       orphan.status,
			Reason:       orphan.reason,
		},
	)
}

func (al *AgentLoop) emitTrackedSubagentEventSafely(
	kind runtimeevents.Kind,
	meta HookMeta,
	payload any,
) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.ErrorSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentTrackedSubagentEventPanicRecovered,
				logger.NewSafeFields(
					agentDiagnosticRuntimeEventKindField(kind),
					agentDiagnosticAgentField(meta.AgentID),
					agentDiagnosticTurnField(meta.TurnID),
					agentDiagnosticParentTurnField(meta.ParentTurnID),
					agentDiagnosticPanicField(recovered),
				),
			)
		}
	}()
	al.emitEvent(kind, meta, payload)
}
