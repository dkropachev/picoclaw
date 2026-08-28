package operator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestBackendProjectsSanitizedEventAndExactPayload(t *testing.T) {
	stored := testStoredEvent(testEventID)
	store := &fakeStore{
		getResult: stored,
		eventPage: eventing.EventPage{
			Events: []eventing.StoredEvent{stored},
		},
	}
	backend := testBackend(t, store)

	page, err := backend.ListEvents(context.Background(), EventListRequest{})
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("event count = %d, want 1", len(page.Events))
	}
	if got := page.Events[0].PayloadBytes; got != len(stored.Envelope.Payload) {
		t.Fatalf("payload_bytes = %d, want %d", got, len(stored.Envelope.Payload))
	}
	listCalls := store.eventFilters()
	if len(listCalls) != 1 || listCalls[0].Limit != DefaultLimit {
		t.Fatalf("list filters = %#v, want default limit %d", listCalls, DefaultLimit)
	}
	assertSerializedSecretsAbsent(t, page)
	serializedPage, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("json.Marshal(page) error = %v", err)
	}
	if strings.Contains(string(serializedPage), "large_integer") ||
		strings.Contains(string(serializedPage), "payload_json") ||
		strings.Contains(string(serializedPage), `"payload"`) {
		t.Fatalf("event list exposed payload: %s", serializedPage)
	}

	detail, err := backend.GetEvent(context.Background(), testEventID)
	if err != nil {
		t.Fatalf("GetEvent() error = %v", err)
	}
	assertSerializedSecretsAbsent(t, detail)
	serializedDetail, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("json.Marshal(detail) error = %v", err)
	}
	if strings.Contains(string(serializedDetail), "9007199254740993") ||
		strings.Contains(string(serializedDetail), "payload_json") ||
		strings.Contains(string(serializedDetail), `"payload"`) {
		t.Fatalf("event detail exposed payload: %s", serializedDetail)
	}

	payload, err := backend.GetEventPayload(context.Background(), testEventID)
	if err != nil {
		t.Fatalf("GetEventPayload() error = %v", err)
	}
	if string(payload) != string(stored.Envelope.Payload) {
		t.Fatalf("payload = %q, want exact %q", payload, stored.Envelope.Payload)
	}
	if !strings.Contains(string(payload), "9007199254740993") {
		t.Fatalf("large numeric token was changed: %s", payload)
	}
	payload[0] = '['
	secondPayload, err := backend.GetEventPayload(context.Background(), testEventID)
	if err != nil {
		t.Fatalf("GetEventPayload(second) error = %v", err)
	}
	if string(secondPayload) != string(stored.Envelope.Payload) {
		t.Fatal("mutating returned payload changed the stored projection")
	}

	workflowEvent, err := backend.GetWorkflowEvent(context.Background(), testEventID)
	if err != nil {
		t.Fatalf("GetWorkflowEvent() error = %v", err)
	}
	if workflowEvent.ID != testEventID ||
		string(workflowEvent.Payload) != string(stored.Envelope.Payload) {
		t.Fatalf("workflow event = %#v, want exact event payload", workflowEvent)
	}
	serializedWorkflowEvent, err := json.Marshal(workflowEvent)
	if err != nil {
		t.Fatalf("json.Marshal(workflow event) error = %v", err)
	}
	assertSerializedSecretsAbsent(t, workflowEvent)
	if !strings.Contains(string(serializedWorkflowEvent), "9007199254740993") {
		t.Fatalf("workflow event changed the large numeric token: %s", serializedWorkflowEvent)
	}
	if strings.Contains(string(serializedWorkflowEvent), "routing") ||
		strings.Contains(string(serializedWorkflowEvent), "payload_bytes") {
		t.Fatalf("workflow event exposed operator bookkeeping: %s", serializedWorkflowEvent)
	}

	page.Events[0].Attributes["action"] = "changed"
	page.Events[0].Actor.Attributes["login"] = "changed"
	page.Events[0].Subject.Attributes["repo"] = "changed"
	if stored.Envelope.Attributes["action"] != "opened" ||
		stored.Envelope.Actor.Attributes["login"] != "octocat" ||
		stored.Envelope.Subject.Attributes["repo"] != "owner/repo" {
		t.Fatal("mutating an event view changed source maps")
	}
}

func TestBackendWorkflowEventFailsClosedOnPayloadMetadataMismatch(t *testing.T) {
	stored := testStoredEvent(testEventID)
	changed := stored
	changed.Envelope.Payload = append(
		append(json.RawMessage(nil), stored.Envelope.Payload...),
		' ',
	)
	store := &fakeStore{getResult: changed}

	// The fake metadata read obtains this size, then the payload read obtains
	// the replacement. A real event is immutable; a discrepancy indicates an
	// inconsistent backend and must not produce a partial workflow context.
	metadata := testEventMetadata(stored)
	if metadata.PayloadBytes == len(store.getResult.Envelope.Payload) {
		t.Fatal("test fixture did not create a payload size mismatch")
	}

	// Exercise the invariant directly with a store that returns the original
	// metadata size and changed payload.
	mismatch := &workflowEventMismatchStore{
		fakeStore: store,
		metadata:  metadata,
	}
	backend := testBackend(t, mismatch)
	_, err := backend.GetWorkflowEvent(context.Background(), testEventID)
	requireErrorIs(t, err, ErrUnavailable)

	metadata = testEventMetadata(changed)
	metadata.Envelope.ID = testReplayID
	mismatch.metadata = metadata
	_, err = backend.GetWorkflowEvent(context.Background(), testEventID)
	requireErrorIs(t, err, ErrUnavailable)
}

type workflowEventMismatchStore struct {
	*fakeStore
	metadata eventing.StoredEventMetadata
}

func (store *workflowEventMismatchStore) GetEventMetadata(
	context.Context,
	string,
) (eventing.StoredEventMetadata, error) {
	return store.metadata, nil
}

func TestBackendProjectsDispatchWithoutLeaseToken(t *testing.T) {
	dispatch := testDispatch()
	dispatch.WorkflowRevision = "sha256:revision"
	finishedAt := testTime.Add(time.Second)
	dispatch.FinishedAt = &finishedAt
	metadata := testDispatchMetadata(dispatch)
	store := &fakeStore{
		dispatchResult: metadata,
		dispatchPage: eventing.DispatchMetadataPage{
			Dispatches: []eventing.DispatchMetadata{
				metadata,
			},
		},
	}
	backend := testBackend(t, store)

	page, err := backend.ListDispatches(context.Background(), DispatchListRequest{})
	if err != nil {
		t.Fatalf("ListDispatches() error = %v", err)
	}
	if len(page.Dispatches) != 1 {
		t.Fatalf("dispatch count = %d, want 1", len(page.Dispatches))
	}
	if page.Dispatches[0].Status != eventing.DispatchRunning ||
		page.Dispatches[0].WorkflowRef != dispatch.WorkflowRef {
		t.Fatalf("dispatch projection = %#v", page.Dispatches[0])
	}
	wantWorkflowID, idErr := workflows.WorkflowDefinitionID(dispatch.WorkflowRef)
	if idErr != nil || page.Dispatches[0].WorkflowID != wantWorkflowID {
		t.Fatalf("dispatch workflow ID = %q, %v; want %q", page.Dispatches[0].WorkflowID, idErr, wantWorkflowID)
	}
	assertSerializedSecretsAbsent(t, page)
	serialized, err := json.Marshal(page.Dispatches[0])
	if err != nil {
		t.Fatalf("json.Marshal(dispatch) error = %v", err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(serialized, &fields); err != nil {
		t.Fatalf("json.Unmarshal(dispatch) error = %v", err)
	}
	wantFields := []string{
		"id",
		"event_id",
		"workflow_ref",
		"workflow_id",
		"workflow_revision",
		"run_id",
		"status",
		"lease_until",
		"available_at",
		"attempts",
		"last_error",
		"created_at",
		"updated_at",
		"linked_at",
		"finished_at",
	}
	if len(fields) != len(wantFields) {
		t.Fatalf("dispatch JSON fields = %#v, want %v", fields, wantFields)
	}
	for _, field := range wantFields {
		if _, ok := fields[field]; !ok {
			t.Fatalf("dispatch JSON omitted public field %q: %s", field, serialized)
		}
	}
	calls := store.dispatchFilters()
	if len(calls) != 1 || calls[0].Limit != DefaultLimit {
		t.Fatalf("dispatch filters = %#v, want default limit", calls)
	}

	detail, err := backend.GetDispatch(context.Background(), testDispatchID)
	if err != nil {
		t.Fatalf("GetDispatch() error = %v", err)
	}
	if detail.ID != testDispatchID ||
		detail.WorkflowRevision != dispatch.WorkflowRevision ||
		detail.CreatedAt != dispatch.CreatedAt ||
		detail.LinkedAt == nil {
		t.Fatalf("dispatch detail = %#v", detail)
	}
	assertSerializedSecretsAbsent(t, detail)
	if ids := store.dispatchIDs(); len(ids) != 1 || ids[0] != testDispatchID {
		t.Fatalf("dispatch detail calls = %#v", ids)
	}
}

func TestBackendCursorIsKindAndFilterBound(t *testing.T) {
	eventCursor := eventing.EventCursor{
		ReceivedAt: testTime,
		ID:         testEventID,
	}
	dispatchCursor := eventing.DispatchCursor{
		CreatedAt: testTime,
		ID:        testDispatchID,
	}
	store := &fakeStore{
		eventPage: eventing.EventPage{Next: &eventCursor},
		dispatchPage: eventing.DispatchMetadataPage{
			Next: &dispatchCursor,
		},
	}
	backend := testBackend(t, store)
	request := EventListRequest{
		Source:        "github",
		Connector:     "primary",
		Type:          "issues.opened",
		RoutingStatus: eventing.RoutingPending,
		Limit:         25,
	}
	first, err := backend.ListEvents(context.Background(), request)
	if err != nil {
		t.Fatalf("ListEvents(first) error = %v", err)
	}
	if first.NextCursor == "" {
		t.Fatal("event next cursor is empty")
	}

	request.Cursor = first.NextCursor
	if _, err = backend.ListEvents(context.Background(), request); err != nil {
		t.Fatalf("ListEvents(second) error = %v", err)
	}
	eventCalls := store.eventFilters()
	if len(eventCalls) != 2 || eventCalls[1].After == nil {
		t.Fatalf("event list calls = %#v, want decoded cursor", eventCalls)
	}
	if eventCalls[1].After.ID != eventCursor.ID ||
		!eventCalls[1].After.ReceivedAt.Equal(eventCursor.ReceivedAt) {
		t.Fatalf("decoded event cursor = %#v, want %#v", eventCalls[1].After, eventCursor)
	}

	mismatched := request
	mismatched.Source = "email"
	_, err = backend.ListEvents(context.Background(), mismatched)
	requireErrorIs(t, err, ErrInvalidRequest)

	dispatchPage, err := backend.ListDispatches(
		context.Background(),
		DispatchListRequest{EventID: testEventID},
	)
	if err != nil {
		t.Fatalf("ListDispatches(first) error = %v", err)
	}
	wrongKind := request
	wrongKind.Cursor = dispatchPage.NextCursor
	_, err = backend.ListEvents(context.Background(), wrongKind)
	requireErrorIs(t, err, ErrInvalidRequest)
}

func TestBackendRejectsInvalidFiltersBeforeStore(t *testing.T) {
	store := &fakeStore{}
	backend := testBackend(t, store)
	eventTests := []EventListRequest{
		{Limit: -1},
		{Limit: MaximumLimit + 1},
		{Source: " leading"},
		{Connector: strings.Repeat("c", maxConnectorBytes+1)},
		{Type: string([]byte{0xff})},
		{RoutingStatus: "unknown"},
		{Cursor: "not-base64"},
	}
	for _, request := range eventTests {
		_, err := backend.ListEvents(context.Background(), request)
		requireErrorIs(t, err, ErrInvalidRequest)
	}

	dispatchTests := []DispatchListRequest{
		{Limit: -1},
		{Limit: MaximumLimit + 1},
		{EventID: "ev_invalid"},
		{WorkflowRef: " trailing "},
		{Status: "unknown"},
		{Cursor: "not-base64"},
	}
	for _, request := range dispatchTests {
		_, err := backend.ListDispatches(context.Background(), request)
		requireErrorIs(t, err, ErrInvalidRequest)
	}
	for _, id := range []string{
		"",
		"dsp_invalid",
		"dsp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		" dsp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	} {
		_, err := backend.GetDispatch(context.Background(), id)
		requireErrorIs(t, err, ErrInvalidRequest)
	}
	eventCalls := store.eventFilters()
	dispatchCalls := store.dispatchFilters()
	dispatchIDs := store.dispatchIDs()
	if len(eventCalls) != 0 || len(dispatchCalls) != 0 || len(dispatchIDs) != 0 {
		t.Fatalf(
			"invalid filters reached store: events=%d dispatches=%d detail=%d",
			len(eventCalls),
			len(dispatchCalls),
			len(dispatchIDs),
		)
	}
}

func TestBackendReplayIsSingleAdditiveSanitizedOperation(t *testing.T) {
	replayed := testStoredEvent(testReplayID)
	replayed.Envelope.ReplayOf = testEventID
	store := &fakeStore{
		replayResult: eventing.InsertResult{
			Event:    replayed,
			Inserted: true,
		},
	}
	backend := testBackend(t, store)

	result, err := backend.Replay(context.Background(), testEventID)
	if err != nil {
		t.Fatalf("Replay() error = %v", err)
	}
	if result.Event.ID != testReplayID || result.Event.ReplayOf != testEventID {
		t.Fatalf("Replay() = %#v", result)
	}
	assertSerializedSecretsAbsent(t, result)
	serialized, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal(result) error = %v", err)
	}
	if strings.Contains(string(serialized), "large_integer") {
		t.Fatalf("replay response exposed payload: %s", serialized)
	}
	replayCalls := store.replayIDs()
	if len(replayCalls) != 1 || replayCalls[0] != testEventID {
		t.Fatalf("Replay calls = %#v, want exactly original ID", replayCalls)
	}

	_, err = backend.Replay(context.Background(), "invalid")
	requireErrorIs(t, err, ErrInvalidRequest)
	replayCalls = store.replayIDs()
	if len(replayCalls) != 1 {
		t.Fatalf("invalid replay reached store: %#v", replayCalls)
	}
}

func TestBackendPreservesStoreErrorsAndRejectsNonInsertReplay(t *testing.T) {
	sentinel := errors.New("store unavailable")
	store := &fakeStore{
		getErr:         sentinel,
		listErr:        sentinel,
		dispatchErr:    sentinel,
		dispatchGetErr: sentinel,
		replayErr:      sentinel,
	}
	backend := testBackend(t, store)

	if _, err := backend.GetEvent(context.Background(), testEventID); !errors.Is(err, sentinel) {
		t.Fatalf("GetEvent() error = %v, want sentinel", err)
	}
	if _, err := backend.ListEvents(context.Background(), EventListRequest{}); !errors.Is(err, sentinel) {
		t.Fatalf("ListEvents() error = %v, want sentinel", err)
	}
	if _, err := backend.ListDispatches(
		context.Background(),
		DispatchListRequest{},
	); !errors.Is(err, sentinel) {
		t.Fatalf("ListDispatches() error = %v, want sentinel", err)
	}
	if _, err := backend.GetDispatch(
		context.Background(),
		testDispatchID,
	); !errors.Is(err, sentinel) {
		t.Fatalf("GetDispatch() error = %v, want sentinel", err)
	}
	store.dispatchGetErr = nil
	store.dispatchResult = testDispatchMetadata(testDispatch())
	store.dispatchResult.ID = "dsp_55555555555555555555555555555555"
	_, dispatchErr := backend.GetDispatch(context.Background(), testDispatchID)
	requireErrorIs(t, dispatchErr, ErrUnavailable)
	_, replayErr := backend.Replay(context.Background(), testEventID)
	if !errors.Is(replayErr, sentinel) {
		t.Fatalf("Replay() error = %v, want sentinel", replayErr)
	}

	store.replayErr = nil
	store.replayResult = eventing.InsertResult{Inserted: false}
	_, replayErr = backend.Replay(context.Background(), testEventID)
	requireErrorIs(t, replayErr, ErrUnavailable)
}

func TestNewBackendAndNilBackendRejectMissingStore(t *testing.T) {
	if _, err := NewBackend(BackendConfig{}); err == nil {
		t.Fatal("NewBackend() error = nil, want missing store")
	}
	var backend *Backend
	if _, err := backend.ListEvents(context.Background(), EventListRequest{}); !errors.Is(
		err,
		ErrUnavailable,
	) {
		t.Fatalf("nil Backend.ListEvents() error = %v", err)
	}
	if _, err := backend.GetDispatch(context.Background(), testDispatchID); !errors.Is(
		err,
		ErrUnavailable,
	) {
		t.Fatalf("nil Backend.GetDispatch() error = %v", err)
	}
}

func assertSerializedSecretsAbsent(t *testing.T, value any) {
	t.Helper()
	serialized, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, forbidden := range []string{
		testDedupeKey,
		testRoutingLease,
		testDispatchLease,
		"dedupe_key",
		"lease_token",
		`"owner":`,
	} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("serialized operator DTO contains %q: %s", forbidden, serialized)
		}
	}
}
