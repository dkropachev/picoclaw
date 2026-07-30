package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/routing"
)

const (
	// RuntimeAgentActivityRoutePrefix is the protected runtime route subtree.
	RuntimeAgentActivityRoutePrefix = "/runtime/agents/"

	DefaultAgentActivityLimit = 50
	MaxAgentActivityLimit     = 100

	agentActivityCapacity           = 512
	agentActivitySubscriptionBuffer = 256
	agentActivityDrainTimeout       = time.Second
	maxAgentActivityQueryBytes      = 512
	maxAgentActivityCursorBytes     = 128
)

var (
	ErrInvalidAgentActivityQuery  = errors.New("invalid agent activity query")
	ErrAgentActivityUnavailable   = errors.New("agent activity unavailable")
	activityGenerationFallbackSeq atomic.Uint64
	agentActivityProjectedKinds   = []runtimeevents.Kind{
		runtimeevents.KindAgentTurnStart,
		runtimeevents.KindAgentTurnEnd,
		runtimeevents.KindAgentLLMRequest,
		runtimeevents.KindAgentLLMResponse,
		runtimeevents.KindAgentLLMRetry,
		runtimeevents.KindAgentContextCompress,
		runtimeevents.KindAgentSessionSummarize,
		runtimeevents.KindAgentToolExecStart,
		runtimeevents.KindAgentToolExecEnd,
		runtimeevents.KindAgentToolExecSkipped,
		runtimeevents.KindAgentSteeringInjected,
		runtimeevents.KindAgentFollowUpQueued,
		runtimeevents.KindAgentInterruptReceived,
		runtimeevents.KindAgentSubTurnSpawn,
		runtimeevents.KindAgentSubTurnEnd,
		runtimeevents.KindAgentSubTurnResultDelivered,
		runtimeevents.KindAgentSubTurnOrphan,
		runtimeevents.KindAgentError,
	}
)

// AgentActivityPage is a bounded, privacy-safe projection of agent runtime
// activity. Cursor and counters are strings so browser clients never lose
// uint64 precision.
type AgentActivityPage struct {
	AgentID    string                       `json:"agent_id"`
	Events     []AgentActivityEvent         `json:"events"`
	NextCursor string                       `json:"next_cursor"`
	Reset      bool                         `json:"reset"`
	Truncated  bool                         `json:"truncated"`
	Dropped    AgentActivityDroppedCounters `json:"dropped"`
}

type AgentActivityDroppedCounters struct {
	Subscription string `json:"subscription"`
	Retention    string `json:"retention"`
	Projection   string `json:"projection"`
}

// AgentActivityEvent contains only a whitelisted kind and a concrete,
// kind-specific safe details value. It deliberately excludes event payloads,
// attributes, source, correlation, and all chat/session/turn identifiers.
type AgentActivityEvent struct {
	Sequence  string                 `json:"sequence"`
	AgentID   string                 `json:"agent_id"`
	Timestamp string                 `json:"timestamp"`
	Kind      runtimeevents.Kind     `json:"kind"`
	Severity  runtimeevents.Severity `json:"severity"`
	Details   AgentActivityDetails   `json:"details"`
}

// AgentActivityDetails is implemented only by the fixed safe projections below.
type AgentActivityDetails interface {
	agentActivityDetails()
}

type AgentActivityEmptyDetails struct{}

func (AgentActivityEmptyDetails) agentActivityDetails() {}

type AgentActivityTurnStartDetails struct {
	MediaCount int `json:"media_count"`
}

func (AgentActivityTurnStartDetails) agentActivityDetails() {}

type AgentActivityTurnEndDetails struct {
	Status     TurnEndStatus `json:"status"`
	Iterations int           `json:"iterations"`
	DurationMS string        `json:"duration_ms"`
}

func (AgentActivityTurnEndDetails) agentActivityDetails() {}

type AgentActivityLLMRequestDetails struct {
	MessagesCount int `json:"messages_count"`
	ToolsCount    int `json:"tools_count"`
}

func (AgentActivityLLMRequestDetails) agentActivityDetails() {}

type AgentActivityLLMResponseDetails struct {
	ToolCalls    int  `json:"tool_calls"`
	HasReasoning bool `json:"has_reasoning"`
}

func (AgentActivityLLMResponseDetails) agentActivityDetails() {}

type AgentActivityLLMRetryDetails struct {
	Attempt    int    `json:"attempt"`
	MaxRetries int    `json:"max_retries"`
	BackoffMS  string `json:"backoff_ms"`
}

func (AgentActivityLLMRetryDetails) agentActivityDetails() {}

type AgentActivityContextCompressDetails struct {
	Reason            ContextCompressReason `json:"reason"`
	DroppedMessages   int                   `json:"dropped_messages"`
	RemainingMessages int                   `json:"remaining_messages"`
}

func (AgentActivityContextCompressDetails) agentActivityDetails() {}

type AgentActivitySessionSummarizeDetails struct {
	SummarizedMessages int  `json:"summarized_messages"`
	KeptMessages       int  `json:"kept_messages"`
	OmittedOversized   bool `json:"omitted_oversized"`
}

func (AgentActivitySessionSummarizeDetails) agentActivityDetails() {}

type AgentActivityToolStartDetails struct {
	ToolName string `json:"tool_name"`
}

func (AgentActivityToolStartDetails) agentActivityDetails() {}

type AgentActivityToolEndDetails struct {
	ToolName   string `json:"tool_name"`
	DurationMS string `json:"duration_ms"`
	IsError    bool   `json:"is_error"`
	Async      bool   `json:"async"`
}

func (AgentActivityToolEndDetails) agentActivityDetails() {}

type AgentActivityToolSkippedDetails struct {
	ToolName string `json:"tool_name"`
}

func (AgentActivityToolSkippedDetails) agentActivityDetails() {}

type AgentActivitySteeringDetails struct {
	Count int `json:"count"`
}

func (AgentActivitySteeringDetails) agentActivityDetails() {}

type AgentActivityInterruptDetails struct {
	InterruptKind InterruptKind `json:"interrupt_kind"`
	QueueDepth    int           `json:"queue_depth"`
}

func (AgentActivityInterruptDetails) agentActivityDetails() {}

type AgentActivitySubTurnSpawnDetails struct {
	TargetAgentID string `json:"target_agent_id"`
}

func (AgentActivitySubTurnSpawnDetails) agentActivityDetails() {}

type AgentActivitySubTurnEndDetails struct {
	TargetAgentID string `json:"target_agent_id"`
	Status        string `json:"status"`
}

func (AgentActivitySubTurnEndDetails) agentActivityDetails() {}

type agentActivityRecord struct {
	sequence uint64
	event    AgentActivityEvent
}

type agentActivityRecorder struct {
	mu         sync.RWMutex
	generation [16]byte
	records    []agentActivityRecord
	start      int
	count      int
	next       uint64
	dropped    uint64
	projection uint64
}

func newAgentActivityRecorder(capacity int) *agentActivityRecorder {
	if capacity < 1 {
		capacity = 1
	}
	recorder := &agentActivityRecorder{
		records: make([]agentActivityRecord, capacity),
	}
	fillAgentActivityGeneration(&recorder.generation)
	return recorder
}

func fillAgentActivityGeneration(generation *[16]byte) {
	if generation == nil {
		return
	}
	if _, err := io.ReadFull(rand.Reader, generation[:]); err == nil {
		return
	}
	var seed [24]byte
	binary.BigEndian.PutUint64(seed[0:8], uint64(time.Now().UnixNano()))
	binary.BigEndian.PutUint64(seed[8:16], activityGenerationFallbackSeq.Add(1))
	binary.BigEndian.PutUint64(seed[16:24], uint64(time.Now().Unix()))
	sum := sha256.Sum256(seed[:])
	copy(generation[:], sum[:len(generation)])
}

func (r *agentActivityRecorder) handle(_ context.Context, evt runtimeevents.Event) error {
	if r == nil {
		return nil
	}
	projected, ok := projectAgentActivityEvent(evt)
	if !ok {
		r.mu.Lock()
		if r.projection != math.MaxUint64 {
			r.projection++
		}
		r.mu.Unlock()
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.next == math.MaxUint64 {
		fillAgentActivityGeneration(&r.generation)
		r.start = 0
		r.count = 0
		r.next = 0
		r.dropped = 0
		r.projection = 0
	}
	r.next++
	projected.Sequence = strconv.FormatUint(r.next, 10)
	record := agentActivityRecord{sequence: r.next, event: projected}
	if r.count < len(r.records) {
		index := (r.start + r.count) % len(r.records)
		r.records[index] = record
		r.count++
		return nil
	}
	r.records[r.start] = record
	r.start = (r.start + 1) % len(r.records)
	if r.dropped != math.MaxUint64 {
		r.dropped++
	}
	return nil
}

func (r *agentActivityRecorder) snapshot(
	agentID string,
	cursor string,
	limit int,
	subscriptionDropped uint64,
) (AgentActivityPage, error) {
	if r == nil {
		return AgentActivityPage{}, ErrAgentActivityUnavailable
	}
	if !routing.IsCanonicalAgentID(agentID) ||
		limit < 1 || limit > MaxAgentActivityLimit {
		return AgentActivityPage{}, ErrInvalidAgentActivityQuery
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	page := AgentActivityPage{
		AgentID: agentID,
		Events:  make([]AgentActivityEvent, 0, limit),
		Dropped: AgentActivityDroppedCounters{
			Subscription: strconv.FormatUint(subscriptionDropped, 10),
			Retention:    strconv.FormatUint(r.dropped, 10),
			Projection:   strconv.FormatUint(r.projection, 10),
		},
	}
	cursorSequence := uint64(0)
	hasCursor := cursor != ""
	if hasCursor {
		generation, sequence, err := decodeAgentActivityCursor(cursor)
		if err != nil {
			return AgentActivityPage{}, ErrInvalidAgentActivityQuery
		}
		if subtle.ConstantTimeCompare(generation[:], r.generation[:]) != 1 ||
			sequence > r.next {
			page.Reset = true
			hasCursor = false
		} else {
			cursorSequence = sequence
		}
	}

	records := r.recordsChronologicalLocked()
	if !hasCursor {
		matches := make([]AgentActivityEvent, 0, min(limit, len(records)))
		for index := len(records) - 1; index >= 0; index-- {
			if records[index].event.AgentID == agentID {
				if len(matches) < limit {
					matches = append(matches, records[index].event)
				} else {
					page.Truncated = true
					break
				}
			}
		}
		for left, right := 0, len(matches)-1; left < right; left, right = left+1, right-1 {
			matches[left], matches[right] = matches[right], matches[left]
		}
		page.Events = matches
		page.NextCursor = encodeAgentActivityCursor(r.generation, r.next)
		return page, nil
	}

	if len(records) > 0 &&
		cursorSequence < records[0].sequence-1 {
		page.Truncated = true
	}
	moreForAgent := false
	for _, record := range records {
		if record.sequence <= cursorSequence || record.event.AgentID != agentID {
			continue
		}
		if len(page.Events) == limit {
			moreForAgent = true
			break
		}
		page.Events = append(page.Events, record.event)
	}
	nextSequence := r.next
	if moreForAgent && len(page.Events) > 0 {
		nextSequence, _ = strconv.ParseUint(page.Events[len(page.Events)-1].Sequence, 10, 64)
		page.Truncated = true
	}
	page.NextCursor = encodeAgentActivityCursor(r.generation, nextSequence)
	return page, nil
}

func (r *agentActivityRecorder) recordsChronologicalLocked() []agentActivityRecord {
	records := make([]agentActivityRecord, 0, r.count)
	for offset := 0; offset < r.count; offset++ {
		records = append(records, r.records[(r.start+offset)%len(r.records)])
	}
	return records
}

func encodeAgentActivityCursor(generation [16]byte, sequence uint64) string {
	var raw [24]byte
	copy(raw[:16], generation[:])
	binary.BigEndian.PutUint64(raw[16:], sequence)
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

func decodeAgentActivityCursor(cursor string) ([16]byte, uint64, error) {
	var generation [16]byte
	if cursor == "" || len(cursor) > maxAgentActivityCursorBytes {
		return generation, 0, ErrInvalidAgentActivityQuery
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil || len(raw) != 24 {
		return generation, 0, ErrInvalidAgentActivityQuery
	}
	copy(generation[:], raw[:16])
	return generation, binary.BigEndian.Uint64(raw[16:]), nil
}

// ParseAgentActivityQuery validates the complete query contract shared by the
// launcher and protected runtime endpoint.
func ParseAgentActivityQuery(rawQuery string) (string, int, error) {
	if len(rawQuery) > maxAgentActivityQueryBytes {
		return "", 0, ErrInvalidAgentActivityQuery
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", 0, ErrInvalidAgentActivityQuery
	}
	for key, candidates := range values {
		if key != "cursor" && key != "limit" ||
			len(candidates) != 1 {
			return "", 0, ErrInvalidAgentActivityQuery
		}
	}
	cursor := ""
	if candidates, exists := values["cursor"]; exists {
		cursor = candidates[0]
		if cursor == "" || len(cursor) > maxAgentActivityCursorBytes {
			return "", 0, ErrInvalidAgentActivityQuery
		}
		if _, _, err = decodeAgentActivityCursor(cursor); err != nil {
			return "", 0, ErrInvalidAgentActivityQuery
		}
	}
	limit := DefaultAgentActivityLimit
	if candidates, exists := values["limit"]; exists {
		rawLimit := candidates[0]
		if rawLimit == "" {
			return "", 0, ErrInvalidAgentActivityQuery
		}
		for _, character := range rawLimit {
			if character < '0' || character > '9' {
				return "", 0, ErrInvalidAgentActivityQuery
			}
		}
		parsed, parseErr := strconv.Atoi(rawLimit)
		if parseErr != nil || parsed < 1 || parsed > MaxAgentActivityLimit {
			return "", 0, ErrInvalidAgentActivityQuery
		}
		if strconv.Itoa(parsed) != rawLimit {
			return "", 0, ErrInvalidAgentActivityQuery
		}
		limit = parsed
	}
	return cursor, limit, nil
}

func projectAgentActivityEvent(evt runtimeevents.Event) (AgentActivityEvent, bool) {
	if !routing.IsCanonicalAgentID(evt.Scope.AgentID) ||
		evt.Source.Component != "agent" ||
		evt.Source.Name != evt.Scope.AgentID ||
		evt.Time.IsZero() ||
		evt.Time.Year() < 1 ||
		evt.Time.Year() > 9999 {
		return AgentActivityEvent{}, false
	}
	details, ok := projectAgentActivityDetails(evt.Kind, evt.Payload)
	if !ok {
		return AgentActivityEvent{}, false
	}
	return AgentActivityEvent{
		AgentID:   evt.Scope.AgentID,
		Timestamp: evt.Time.UTC().Format(time.RFC3339Nano),
		Kind:      evt.Kind,
		Severity:  agentActivitySeverity(evt.Kind, details),
		Details:   details,
	}, true
}

func projectAgentActivityDetails(
	kind runtimeevents.Kind,
	payload any,
) (AgentActivityDetails, bool) {
	switch kind {
	case runtimeevents.KindAgentTurnStart:
		value, ok := payload.(TurnStartPayload)
		if !ok || value.MediaCount < 0 {
			return nil, false
		}
		return AgentActivityTurnStartDetails{MediaCount: value.MediaCount}, true
	case runtimeevents.KindAgentTurnEnd:
		value, ok := payload.(TurnEndPayload)
		if !ok || value.Iterations < 0 || value.Duration < 0 ||
			!validAgentActivityTurnStatus(value.Status) {
			return nil, false
		}
		return AgentActivityTurnEndDetails{
			Status:     value.Status,
			Iterations: value.Iterations,
			DurationMS: strconv.FormatInt(value.Duration.Milliseconds(), 10),
		}, true
	case runtimeevents.KindAgentLLMRequest:
		value, ok := payload.(LLMRequestPayload)
		if !ok || value.MessagesCount < 0 || value.ToolsCount < 0 {
			return nil, false
		}
		return AgentActivityLLMRequestDetails{
			MessagesCount: value.MessagesCount,
			ToolsCount:    value.ToolsCount,
		}, true
	case runtimeevents.KindAgentLLMResponse:
		value, ok := payload.(LLMResponsePayload)
		if !ok || value.ToolCalls < 0 {
			return nil, false
		}
		return AgentActivityLLMResponseDetails{
			ToolCalls:    value.ToolCalls,
			HasReasoning: value.HasReasoning,
		}, true
	case runtimeevents.KindAgentLLMRetry:
		value, ok := payload.(LLMRetryPayload)
		if !ok || value.Attempt < 0 || value.MaxRetries < 0 || value.Backoff < 0 {
			return nil, false
		}
		return AgentActivityLLMRetryDetails{
			Attempt:    value.Attempt,
			MaxRetries: value.MaxRetries,
			BackoffMS:  strconv.FormatInt(value.Backoff.Milliseconds(), 10),
		}, true
	case runtimeevents.KindAgentContextCompress:
		value, ok := payload.(ContextCompressPayload)
		if !ok || value.DroppedMessages < 0 || value.RemainingMessages < 0 ||
			!validAgentActivityCompressReason(value.Reason) {
			return nil, false
		}
		return AgentActivityContextCompressDetails{
			Reason:            value.Reason,
			DroppedMessages:   value.DroppedMessages,
			RemainingMessages: value.RemainingMessages,
		}, true
	case runtimeevents.KindAgentSessionSummarize:
		value, ok := payload.(SessionSummarizePayload)
		if !ok || value.SummarizedMessages < 0 || value.KeptMessages < 0 {
			return nil, false
		}
		return AgentActivitySessionSummarizeDetails{
			SummarizedMessages: value.SummarizedMessages,
			KeptMessages:       value.KeptMessages,
			OmittedOversized:   value.OmittedOversized,
		}, true
	case runtimeevents.KindAgentToolExecStart:
		value, ok := payload.(ToolExecStartPayload)
		if !ok || !validAgentActivityToolName(value.Tool) {
			return nil, false
		}
		return AgentActivityToolStartDetails{ToolName: value.Tool}, true
	case runtimeevents.KindAgentToolExecEnd:
		value, ok := payload.(ToolExecEndPayload)
		if !ok || value.Duration < 0 || !validAgentActivityToolName(value.Tool) {
			return nil, false
		}
		return AgentActivityToolEndDetails{
			ToolName:   value.Tool,
			DurationMS: strconv.FormatInt(value.Duration.Milliseconds(), 10),
			IsError:    value.IsError,
			Async:      value.Async,
		}, true
	case runtimeevents.KindAgentToolExecSkipped:
		value, ok := payload.(ToolExecSkippedPayload)
		if !ok || !validAgentActivityToolName(value.Tool) {
			return nil, false
		}
		return AgentActivityToolSkippedDetails{ToolName: value.Tool}, true
	case runtimeevents.KindAgentSteeringInjected:
		value, ok := payload.(SteeringInjectedPayload)
		if !ok || value.Count < 0 {
			return nil, false
		}
		return AgentActivitySteeringDetails{Count: value.Count}, true
	case runtimeevents.KindAgentFollowUpQueued:
		if _, ok := payload.(FollowUpQueuedPayload); !ok {
			return nil, false
		}
		return AgentActivityEmptyDetails{}, true
	case runtimeevents.KindAgentInterruptReceived:
		value, ok := payload.(InterruptReceivedPayload)
		if !ok || value.QueueDepth < 0 || !validAgentActivityInterruptKind(value.Kind) {
			return nil, false
		}
		return AgentActivityInterruptDetails{
			InterruptKind: value.Kind,
			QueueDepth:    value.QueueDepth,
		}, true
	case runtimeevents.KindAgentSubTurnSpawn:
		value, ok := payload.(SubTurnSpawnPayload)
		if !ok || !routing.IsCanonicalAgentID(value.AgentID) {
			return nil, false
		}
		return AgentActivitySubTurnSpawnDetails{TargetAgentID: value.AgentID}, true
	case runtimeevents.KindAgentSubTurnEnd:
		value, ok := payload.(SubTurnEndPayload)
		if !ok || !routing.IsCanonicalAgentID(value.AgentID) ||
			value.Status != "completed" && value.Status != "error" {
			return nil, false
		}
		return AgentActivitySubTurnEndDetails{
			TargetAgentID: value.AgentID,
			Status:        value.Status,
		}, true
	case runtimeevents.KindAgentSubTurnResultDelivered:
		if _, ok := payload.(SubTurnResultDeliveredPayload); !ok {
			return nil, false
		}
		return AgentActivityEmptyDetails{}, true
	case runtimeevents.KindAgentSubTurnOrphan:
		if _, ok := payload.(SubTurnOrphanPayload); !ok {
			return nil, false
		}
		return AgentActivityEmptyDetails{}, true
	case runtimeevents.KindAgentError:
		if _, ok := payload.(ErrorPayload); !ok {
			return nil, false
		}
		return AgentActivityEmptyDetails{}, true
	default:
		return nil, false
	}
}

func agentActivitySeverity(
	kind runtimeevents.Kind,
	details AgentActivityDetails,
) runtimeevents.Severity {
	switch kind {
	case runtimeevents.KindAgentError, runtimeevents.KindAgentSubTurnOrphan:
		return runtimeevents.SeverityError
	case runtimeevents.KindAgentLLMRetry,
		runtimeevents.KindAgentContextCompress,
		runtimeevents.KindAgentToolExecSkipped:
		return runtimeevents.SeverityWarn
	case runtimeevents.KindAgentTurnEnd:
		value, ok := details.(AgentActivityTurnEndDetails)
		if ok && value.Status == TurnEndStatusError {
			return runtimeevents.SeverityError
		}
		if ok && value.Status == TurnEndStatusAborted {
			return runtimeevents.SeverityWarn
		}
	case runtimeevents.KindAgentToolExecEnd:
		value, ok := details.(AgentActivityToolEndDetails)
		if ok && value.IsError {
			return runtimeevents.SeverityWarn
		}
	case runtimeevents.KindAgentSubTurnEnd:
		value, ok := details.(AgentActivitySubTurnEndDetails)
		if ok && value.Status == "error" {
			return runtimeevents.SeverityError
		}
	}
	return runtimeevents.SeverityInfo
}

func validAgentActivityTurnStatus(status TurnEndStatus) bool {
	return status == TurnEndStatusCompleted ||
		status == TurnEndStatusError ||
		status == TurnEndStatusAborted
}

func validAgentActivityCompressReason(reason ContextCompressReason) bool {
	return reason == ContextCompressReasonProactive ||
		reason == ContextCompressReasonRetry ||
		reason == ContextCompressReasonSummarize
}

func validAgentActivityInterruptKind(kind InterruptKind) bool {
	return kind == InterruptKindSteering ||
		kind == InterruptKindGraceful ||
		kind == InterruptKindHard
}

func validAgentActivityToolName(name string) bool {
	if len(name) < 1 || len(name) > 64 {
		return false
	}
	for _, character := range name {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			character == '_' ||
			character == '-' {
			continue
		}
		return false
	}
	return true
}

func (al *AgentLoop) initAgentActivityRecorder() error {
	if al == nil || al.runtimeEvents == nil {
		return ErrAgentActivityUnavailable
	}
	recorder := newAgentActivityRecorder(agentActivityCapacity)
	subscription, err := al.runtimeEvents.Channel().
		Source("agent").
		OfKind(agentActivityProjectedKinds...).
		Subscribe(
			context.Background(),
			runtimeevents.SubscribeOptions{
				Name:         "agent-activity-recorder",
				Buffer:       agentActivitySubscriptionBuffer,
				Concurrency:  runtimeevents.Locked,
				Backpressure: runtimeevents.DropOldest,
				PanicPolicy:  runtimeevents.RecoverAndLog,
			},
			recorder.handle,
		)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAgentActivityUnavailable, err)
	}
	al.agentActivityMu.Lock()
	al.agentActivity = recorder
	al.agentActivitySub = subscription
	al.agentActivityMu.Unlock()
	return nil
}

func (al *AgentLoop) closeAgentActivityRecorder() {
	if al == nil {
		return
	}
	al.agentActivityMu.Lock()
	subscription := al.agentActivitySub
	al.agentActivitySub = nil
	al.agentActivity = nil
	al.agentActivityMu.Unlock()
	if subscription != nil {
		_ = subscription.Close()
		timer := time.NewTimer(agentActivityDrainTimeout)
		defer timer.Stop()
		select {
		case <-subscription.Done():
		case <-timer.C:
		}
	}
}

// AgentActivity returns one bounded page for a currently addressed agent.
func (al *AgentLoop) AgentActivity(
	agentID string,
	cursor string,
	limit int,
) (AgentActivityPage, error) {
	if al == nil {
		return AgentActivityPage{}, ErrAgentActivityUnavailable
	}
	al.agentActivityMu.RLock()
	recorder := al.agentActivity
	subscription := al.agentActivitySub
	al.agentActivityMu.RUnlock()
	if recorder == nil || subscription == nil {
		return AgentActivityPage{}, ErrAgentActivityUnavailable
	}
	return recorder.snapshot(agentID, cursor, limit, subscription.Stats().Dropped)
}

// DecodeAgentActivityPage strictly decodes and validates a launcher upstream
// response before it is re-encoded for a browser.
func DecodeAgentActivityPage(raw []byte) (AgentActivityPage, error) {
	var page AgentActivityPage
	if !utf8.Valid(raw) {
		return AgentActivityPage{}, errors.New("activity response is not UTF-8")
	}
	if err := rejectDuplicateAgentActivityJSONKeys(raw); err != nil {
		return AgentActivityPage{}, err
	}
	members, err := requireExactAgentActivityObjectKeys(
		raw,
		"agent_id",
		"events",
		"next_cursor",
		"reset",
		"truncated",
		"dropped",
	)
	if err != nil {
		return AgentActivityPage{}, err
	}
	if _, err = requireExactAgentActivityObjectKeys(
		members["dropped"],
		"subscription",
		"retention",
		"projection",
	); err != nil {
		return AgentActivityPage{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&page); err != nil {
		return AgentActivityPage{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return AgentActivityPage{}, errors.New("multiple JSON values")
		}
		return AgentActivityPage{}, err
	}
	if err := validateAgentActivityPage(page); err != nil {
		return AgentActivityPage{}, err
	}
	return page, nil
}

func (event *AgentActivityEvent) UnmarshalJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("activity event is not UTF-8")
	}
	if err := rejectDuplicateAgentActivityJSONKeys(raw); err != nil {
		return err
	}
	if _, err := requireExactAgentActivityObjectKeys(
		raw,
		"sequence",
		"agent_id",
		"timestamp",
		"kind",
		"severity",
		"details",
	); err != nil {
		return err
	}
	type wireEvent struct {
		Sequence  string                 `json:"sequence"`
		AgentID   string                 `json:"agent_id"`
		Timestamp string                 `json:"timestamp"`
		Kind      runtimeevents.Kind     `json:"kind"`
		Severity  runtimeevents.Severity `json:"severity"`
		Details   json.RawMessage        `json:"details"`
	}
	var wire wireEvent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("invalid activity event")
	}
	details, err := decodeAgentActivityDetails(wire.Kind, wire.Details)
	if err != nil {
		return err
	}
	*event = AgentActivityEvent{
		Sequence:  wire.Sequence,
		AgentID:   wire.AgentID,
		Timestamp: wire.Timestamp,
		Kind:      wire.Kind,
		Severity:  wire.Severity,
		Details:   details,
	}
	return nil
}

func decodeAgentActivityDetails(
	kind runtimeevents.Kind,
	raw json.RawMessage,
) (AgentActivityDetails, error) {
	var destination AgentActivityDetails
	var requiredKeys []string
	switch kind {
	case runtimeevents.KindAgentTurnStart:
		destination = &AgentActivityTurnStartDetails{}
		requiredKeys = []string{"media_count"}
	case runtimeevents.KindAgentTurnEnd:
		destination = &AgentActivityTurnEndDetails{}
		requiredKeys = []string{"status", "iterations", "duration_ms"}
	case runtimeevents.KindAgentLLMRequest:
		destination = &AgentActivityLLMRequestDetails{}
		requiredKeys = []string{"messages_count", "tools_count"}
	case runtimeevents.KindAgentLLMResponse:
		destination = &AgentActivityLLMResponseDetails{}
		requiredKeys = []string{"tool_calls", "has_reasoning"}
	case runtimeevents.KindAgentLLMRetry:
		destination = &AgentActivityLLMRetryDetails{}
		requiredKeys = []string{"attempt", "max_retries", "backoff_ms"}
	case runtimeevents.KindAgentContextCompress:
		destination = &AgentActivityContextCompressDetails{}
		requiredKeys = []string{
			"reason",
			"dropped_messages",
			"remaining_messages",
		}
	case runtimeevents.KindAgentSessionSummarize:
		destination = &AgentActivitySessionSummarizeDetails{}
		requiredKeys = []string{
			"summarized_messages",
			"kept_messages",
			"omitted_oversized",
		}
	case runtimeevents.KindAgentToolExecStart:
		destination = &AgentActivityToolStartDetails{}
		requiredKeys = []string{"tool_name"}
	case runtimeevents.KindAgentToolExecEnd:
		destination = &AgentActivityToolEndDetails{}
		requiredKeys = []string{
			"tool_name",
			"duration_ms",
			"is_error",
			"async",
		}
	case runtimeevents.KindAgentToolExecSkipped:
		destination = &AgentActivityToolSkippedDetails{}
		requiredKeys = []string{"tool_name"}
	case runtimeevents.KindAgentSteeringInjected:
		destination = &AgentActivitySteeringDetails{}
		requiredKeys = []string{"count"}
	case runtimeevents.KindAgentInterruptReceived:
		destination = &AgentActivityInterruptDetails{}
		requiredKeys = []string{"interrupt_kind", "queue_depth"}
	case runtimeevents.KindAgentSubTurnSpawn:
		destination = &AgentActivitySubTurnSpawnDetails{}
		requiredKeys = []string{"target_agent_id"}
	case runtimeevents.KindAgentSubTurnEnd:
		destination = &AgentActivitySubTurnEndDetails{}
		requiredKeys = []string{"target_agent_id", "status"}
	case runtimeevents.KindAgentFollowUpQueued,
		runtimeevents.KindAgentSubTurnResultDelivered,
		runtimeevents.KindAgentSubTurnOrphan,
		runtimeevents.KindAgentError:
		destination = &AgentActivityEmptyDetails{}
	default:
		return nil, errors.New("unknown activity kind")
	}
	if _, err := requireExactAgentActivityObjectKeys(raw, requiredKeys...); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid activity details")
	}
	switch value := destination.(type) {
	case *AgentActivityTurnStartDetails:
		return *value, nil
	case *AgentActivityTurnEndDetails:
		return *value, nil
	case *AgentActivityLLMRequestDetails:
		return *value, nil
	case *AgentActivityLLMResponseDetails:
		return *value, nil
	case *AgentActivityLLMRetryDetails:
		return *value, nil
	case *AgentActivityContextCompressDetails:
		return *value, nil
	case *AgentActivitySessionSummarizeDetails:
		return *value, nil
	case *AgentActivityToolStartDetails:
		return *value, nil
	case *AgentActivityToolEndDetails:
		return *value, nil
	case *AgentActivityToolSkippedDetails:
		return *value, nil
	case *AgentActivitySteeringDetails:
		return *value, nil
	case *AgentActivityInterruptDetails:
		return *value, nil
	case *AgentActivitySubTurnSpawnDetails:
		return *value, nil
	case *AgentActivitySubTurnEndDetails:
		return *value, nil
	case *AgentActivityEmptyDetails:
		return *value, nil
	default:
		return nil, errors.New("invalid activity details type")
	}
}

func validateAgentActivityPage(page AgentActivityPage) error {
	if !routing.IsCanonicalAgentID(page.AgentID) ||
		page.Events == nil ||
		len(page.Events) > MaxAgentActivityLimit ||
		!canonicalUint64String(page.Dropped.Subscription) ||
		!canonicalUint64String(page.Dropped.Retention) ||
		!canonicalUint64String(page.Dropped.Projection) {
		return errors.New("invalid activity page")
	}
	_, cursorSequence, err := decodeAgentActivityCursor(page.NextCursor)
	if err != nil {
		return err
	}
	previous := uint64(0)
	for _, event := range page.Events {
		sequence, parseErr := strconv.ParseUint(event.Sequence, 10, 64)
		if parseErr != nil || sequence == 0 ||
			event.Sequence != strconv.FormatUint(sequence, 10) ||
			sequence <= previous ||
			event.AgentID != page.AgentID {
			return errors.New("invalid activity event sequence")
		}
		previous = sequence
		if err = validateAgentActivityEvent(event); err != nil {
			return err
		}
	}
	if previous > cursorSequence {
		return errors.New("activity cursor precedes events")
	}
	return nil
}

func validateAgentActivityEvent(event AgentActivityEvent) error {
	if !routing.IsCanonicalAgentID(event.AgentID) ||
		len(event.Timestamp) == 0 ||
		len(event.Timestamp) > len(time.RFC3339Nano)+10 {
		return errors.New("invalid activity event")
	}
	parsedTime, err := time.Parse(time.RFC3339Nano, event.Timestamp)
	if err != nil ||
		parsedTime.Year() < 1 ||
		parsedTime.Year() > 9999 ||
		event.Timestamp != parsedTime.UTC().Format(time.RFC3339Nano) {
		return errors.New("invalid activity timestamp")
	}
	if event.Details == nil ||
		!agentActivityDetailsMatchKind(event.Kind, event.Details) {
		return errors.New("invalid activity detail union")
	}
	if event.Severity != agentActivitySeverity(event.Kind, event.Details) {
		return errors.New("invalid activity severity")
	}
	switch details := event.Details.(type) {
	case AgentActivityTurnStartDetails:
		if details.MediaCount < 0 {
			return errors.New("invalid turn start details")
		}
	case AgentActivityTurnEndDetails:
		if !validAgentActivityTurnStatus(details.Status) ||
			details.Iterations < 0 ||
			!canonicalInt64String(details.DurationMS, false) {
			return errors.New("invalid turn end details")
		}
	case AgentActivityLLMRequestDetails:
		if details.MessagesCount < 0 || details.ToolsCount < 0 {
			return errors.New("invalid llm request details")
		}
	case AgentActivityLLMResponseDetails:
		if details.ToolCalls < 0 {
			return errors.New("invalid llm response details")
		}
	case AgentActivityLLMRetryDetails:
		if details.Attempt < 0 || details.MaxRetries < 0 ||
			!canonicalInt64String(details.BackoffMS, false) {
			return errors.New("invalid llm retry details")
		}
	case AgentActivityContextCompressDetails:
		if !validAgentActivityCompressReason(details.Reason) ||
			details.DroppedMessages < 0 ||
			details.RemainingMessages < 0 {
			return errors.New("invalid context details")
		}
	case AgentActivitySessionSummarizeDetails:
		if details.SummarizedMessages < 0 || details.KeptMessages < 0 {
			return errors.New("invalid summarize details")
		}
	case AgentActivityToolStartDetails:
		if !validAgentActivityToolName(details.ToolName) {
			return errors.New("invalid tool start details")
		}
	case AgentActivityToolEndDetails:
		if !validAgentActivityToolName(details.ToolName) ||
			!canonicalInt64String(details.DurationMS, false) {
			return errors.New("invalid tool details")
		}
	case AgentActivityToolSkippedDetails:
		if !validAgentActivityToolName(details.ToolName) {
			return errors.New("invalid tool skipped details")
		}
	case AgentActivitySteeringDetails:
		if details.Count < 0 {
			return errors.New("invalid steering details")
		}
	case AgentActivityInterruptDetails:
		if !validAgentActivityInterruptKind(details.InterruptKind) ||
			details.QueueDepth < 0 {
			return errors.New("invalid interrupt details")
		}
	case AgentActivitySubTurnSpawnDetails:
		if !routing.IsCanonicalAgentID(details.TargetAgentID) {
			return errors.New("invalid subturn details")
		}
	case AgentActivitySubTurnEndDetails:
		if !routing.IsCanonicalAgentID(details.TargetAgentID) ||
			details.Status != "completed" && details.Status != "error" {
			return errors.New("invalid subturn end details")
		}
	case AgentActivityEmptyDetails:
	default:
		return errors.New("invalid activity detail union")
	}
	return nil
}

func agentActivityDetailsMatchKind(
	kind runtimeevents.Kind,
	details AgentActivityDetails,
) bool {
	switch kind {
	case runtimeevents.KindAgentTurnStart:
		_, ok := details.(AgentActivityTurnStartDetails)
		return ok
	case runtimeevents.KindAgentTurnEnd:
		_, ok := details.(AgentActivityTurnEndDetails)
		return ok
	case runtimeevents.KindAgentLLMRequest:
		_, ok := details.(AgentActivityLLMRequestDetails)
		return ok
	case runtimeevents.KindAgentLLMResponse:
		_, ok := details.(AgentActivityLLMResponseDetails)
		return ok
	case runtimeevents.KindAgentLLMRetry:
		_, ok := details.(AgentActivityLLMRetryDetails)
		return ok
	case runtimeevents.KindAgentContextCompress:
		_, ok := details.(AgentActivityContextCompressDetails)
		return ok
	case runtimeevents.KindAgentSessionSummarize:
		_, ok := details.(AgentActivitySessionSummarizeDetails)
		return ok
	case runtimeevents.KindAgentToolExecStart:
		_, ok := details.(AgentActivityToolStartDetails)
		return ok
	case runtimeevents.KindAgentToolExecEnd:
		_, ok := details.(AgentActivityToolEndDetails)
		return ok
	case runtimeevents.KindAgentToolExecSkipped:
		_, ok := details.(AgentActivityToolSkippedDetails)
		return ok
	case runtimeevents.KindAgentSteeringInjected:
		_, ok := details.(AgentActivitySteeringDetails)
		return ok
	case runtimeevents.KindAgentInterruptReceived:
		_, ok := details.(AgentActivityInterruptDetails)
		return ok
	case runtimeevents.KindAgentSubTurnSpawn:
		_, ok := details.(AgentActivitySubTurnSpawnDetails)
		return ok
	case runtimeevents.KindAgentSubTurnEnd:
		_, ok := details.(AgentActivitySubTurnEndDetails)
		return ok
	case runtimeevents.KindAgentFollowUpQueued,
		runtimeevents.KindAgentSubTurnResultDelivered,
		runtimeevents.KindAgentSubTurnOrphan,
		runtimeevents.KindAgentError:
		_, ok := details.(AgentActivityEmptyDetails)
		return ok
	default:
		return false
	}
}

func canonicalUint64String(value string) bool {
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && value == strconv.FormatUint(parsed, 10)
}

func canonicalInt64String(value string, allowNegative bool) bool {
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil &&
		(allowNegative || parsed >= 0) &&
		value == strconv.FormatInt(parsed, 10)
}

func rejectDuplicateAgentActivityJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	tokenBudget := 0
	if err := consumeUniqueAgentActivityJSONValue(decoder, 0, &tokenBudget); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple activity JSON values")
		}
		return err
	}
	return nil
}

func requireExactAgentActivityObjectKeys(
	raw []byte,
	expected ...string,
) (map[string]json.RawMessage, error) {
	var members map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&members); err != nil || members == nil {
		return nil, errors.New("activity JSON value is not an object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid activity JSON object")
	}
	if len(members) != len(expected) {
		return nil, errors.New("activity JSON object has unexpected members")
	}
	for _, name := range expected {
		if _, exists := members[name]; !exists {
			return nil, errors.New("activity JSON object is missing required member")
		}
	}
	return members, nil
}

func consumeUniqueAgentActivityJSONValue(
	decoder *json.Decoder,
	depth int,
	tokenBudget *int,
) error {
	const (
		maxDepth  = 16
		maxTokens = 8192
	)
	if depth > maxDepth {
		return errors.New("activity JSON nesting exceeds limit")
	}
	*tokenBudget = *tokenBudget + 1
	if *tokenBudget > maxTokens {
		return errors.New("activity JSON token count exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return keyErr
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("activity JSON object key is not a string")
			}
			folded := foldAgentActivityJSONKey(key)
			if _, duplicate := seen[folded]; duplicate {
				return errors.New("duplicate activity JSON object key")
			}
			seen[folded] = struct{}{}
			if err = consumeUniqueAgentActivityJSONValue(
				decoder,
				depth+1,
				tokenBudget,
			); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim('}') {
			return errors.New("unterminated activity JSON object")
		}
	case '[':
		for decoder.More() {
			if err = consumeUniqueAgentActivityJSONValue(
				decoder,
				depth+1,
				tokenBudget,
			); err != nil {
				return err
			}
		}
		closing, closeErr := decoder.Token()
		if closeErr != nil || closing != json.Delim(']') {
			return errors.New("unterminated activity JSON array")
		}
	default:
		return errors.New("unexpected activity JSON delimiter")
	}
	return nil
}

func foldAgentActivityJSONKey(key string) string {
	var folded strings.Builder
	folded.Grow(len(key))
	for _, character := range key {
		minimum := character
		for cursor := unicode.SimpleFold(character); cursor != character; cursor = unicode.SimpleFold(cursor) {
			if cursor < minimum {
				minimum = cursor
			}
		}
		folded.WriteRune(minimum)
	}
	return folded.String()
}

// MarshalAgentActivityPage validates and produces the canonical launcher body.
func MarshalAgentActivityPage(page AgentActivityPage) ([]byte, error) {
	if err := validateAgentActivityPage(page); err != nil {
		return nil, err
	}
	return json.Marshal(page)
}

// AgentActivityUpstreamQuery returns the canonical query sent by the launcher.
func AgentActivityUpstreamQuery(cursor string, limit int) string {
	values := url.Values{}
	if strings.TrimSpace(cursor) != "" {
		values.Set("cursor", cursor)
	}
	values.Set("limit", strconv.Itoa(limit))
	return values.Encode()
}
