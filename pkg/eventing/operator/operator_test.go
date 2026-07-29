package operator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const (
	testEventID       = "ev_11111111111111111111111111111111"
	testReplayID      = "ev_22222222222222222222222222222222"
	testDispatchID    = "dsp_33333333333333333333333333333333"
	testRoutingLease  = "lease_routing_must_never_be_exposed"
	testDispatchLease = "lease_dispatch_must_never_be_exposed"
	testDedupeKey     = "dedupe_key_must_never_be_exposed"
)

var testTime = time.Date(2026, time.July, 29, 12, 34, 56, 789, time.UTC)

type fakeStore struct {
	mu sync.Mutex

	getResult eventing.StoredEvent
	getErr    error
	getCalls  []string

	eventPage   eventing.EventPage
	listErr     error
	listCalls   []eventing.EventFilter
	listEntered chan struct{}
	listRelease chan struct{}

	dispatchPage  eventing.DispatchMetadataPage
	dispatchErr   error
	dispatchCalls []eventing.DispatchFilter

	replayResult eventing.InsertResult
	replayErr    error
	replayCalls  []string
}

func (store *fakeStore) Get(
	ctx context.Context,
	id string,
) (eventing.StoredEvent, error) {
	store.mu.Lock()
	store.getCalls = append(store.getCalls, id)
	result, err := store.getResult, store.getErr
	store.mu.Unlock()
	return result, err
}

func (store *fakeStore) GetEventMetadata(
	ctx context.Context,
	id string,
) (eventing.StoredEventMetadata, error) {
	stored, err := store.Get(ctx, id)
	return testEventMetadata(stored), err
}

func (store *fakeStore) GetEventPayload(
	ctx context.Context,
	id string,
) ([]byte, error) {
	stored, err := store.Get(ctx, id)
	return append([]byte(nil), stored.Envelope.Payload...), err
}

func (store *fakeStore) List(
	ctx context.Context,
	filter eventing.EventFilter,
) (eventing.EventPage, error) {
	store.mu.Lock()
	store.listCalls = append(store.listCalls, filter)
	entered, release := store.listEntered, store.listRelease
	result, err := store.eventPage, store.listErr
	store.mu.Unlock()
	if entered != nil {
		select {
		case <-entered:
		default:
			close(entered)
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return eventing.EventPage{}, ctx.Err()
		}
	}
	return result, err
}

func (store *fakeStore) ListEventMetadata(
	ctx context.Context,
	filter eventing.EventFilter,
) (eventing.EventMetadataPage, error) {
	page, err := store.List(ctx, filter)
	metadata := eventing.EventMetadataPage{
		Events: make([]eventing.StoredEventMetadata, len(page.Events)),
		Next:   page.Next,
	}
	for index := range page.Events {
		metadata.Events[index] = testEventMetadata(page.Events[index])
	}
	return metadata, err
}

func (store *fakeStore) ListDispatchMetadata(
	ctx context.Context,
	filter eventing.DispatchFilter,
) (eventing.DispatchMetadataPage, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.dispatchCalls = append(store.dispatchCalls, filter)
	return store.dispatchPage, store.dispatchErr
}

func (store *fakeStore) Replay(
	ctx context.Context,
	id string,
) (eventing.InsertResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.replayCalls = append(store.replayCalls, id)
	return store.replayResult, store.replayErr
}

func (store *fakeStore) eventFilters() []eventing.EventFilter {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]eventing.EventFilter(nil), store.listCalls...)
}

func (store *fakeStore) dispatchFilters() []eventing.DispatchFilter {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]eventing.DispatchFilter(nil), store.dispatchCalls...)
}

func (store *fakeStore) replayIDs() []string {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]string(nil), store.replayCalls...)
}

func testStoredEvent(id string) eventing.StoredEvent {
	occurredAt := testTime.Add(-time.Minute)
	leaseUntil := testTime.Add(time.Minute)
	return eventing.StoredEvent{
		Envelope: eventing.Envelope{
			ID:         id,
			Source:     "github",
			Connector:  "primary",
			Type:       "issues.opened",
			DedupeKey:  testDedupeKey,
			OccurredAt: &occurredAt,
			ReceivedAt: testTime,
			Payload: json.RawMessage(
				`{"large_integer":9007199254740993,"redacted":"[REDACTED]"}`,
			),
			Actor: &eventing.Actor{
				ID:          "user-1",
				Type:        "user",
				DisplayName: "Example",
				Attributes:  map[string]string{"login": "octocat"},
			},
			Subject: &eventing.Subject{
				ID:         "issue-1",
				Type:       "issue",
				Name:       "Fix",
				URL:        "https://example.invalid/issues/1",
				Attributes: map[string]string{"repo": "owner/repo"},
			},
			Attributes: map[string]string{"action": "opened"},
		},
		Routing: eventing.RoutingState{
			Status:      eventing.RoutingClaimed,
			LeaseToken:  testRoutingLease,
			LeaseUntil:  &leaseUntil,
			AvailableAt: testTime,
			Attempts:    2,
			LastError:   "temporary error",
			UpdatedAt:   testTime,
		},
	}
}

func testEventMetadata(stored eventing.StoredEvent) eventing.StoredEventMetadata {
	payloadBytes := len(stored.Envelope.Payload)
	stored.Envelope.Payload = nil
	stored.Envelope.DedupeKey = ""
	stored.Routing.LeaseToken = ""
	return eventing.StoredEventMetadata{
		Envelope:     stored.Envelope,
		Routing:      stored.Routing,
		PayloadBytes: payloadBytes,
	}
}

func testDispatch() eventing.Dispatch {
	leaseUntil := testTime.Add(time.Minute)
	linkedAt := testTime.Add(-time.Second)
	return eventing.Dispatch{
		ID:          testDispatchID,
		EventID:     testEventID,
		WorkflowRef: "workflows/issues.yml",
		RunID:       "wr_44444444444444444444444444444444",
		Status:      eventing.DispatchRunning,
		LeaseToken:  testDispatchLease,
		LeaseUntil:  &leaseUntil,
		AvailableAt: testTime,
		Attempts:    3,
		LastError:   "retry detail",
		CreatedAt:   testTime.Add(-time.Minute),
		UpdatedAt:   testTime,
		LinkedAt:    &linkedAt,
	}
}

func testDispatchMetadata(dispatch eventing.Dispatch) eventing.DispatchMetadata {
	return eventing.DispatchMetadata{
		ID:          dispatch.ID,
		EventID:     dispatch.EventID,
		WorkflowRef: dispatch.WorkflowRef,
		RunID:       dispatch.RunID,
		Status:      dispatch.Status,
		LeaseUntil:  cloneTime(dispatch.LeaseUntil),
		AvailableAt: dispatch.AvailableAt,
		Attempts:    dispatch.Attempts,
		LastError:   dispatch.LastError,
		CreatedAt:   dispatch.CreatedAt,
		UpdatedAt:   dispatch.UpdatedAt,
		LinkedAt:    cloneTime(dispatch.LinkedAt),
		FinishedAt:  cloneTime(dispatch.FinishedAt),
	}
}

func testBackend(t *testing.T, store Store) *Backend {
	t.Helper()
	backend, err := NewBackend(BackendConfig{Store: store})
	if err != nil {
		t.Fatalf("NewBackend() error = %v", err)
	}
	return backend
}

func testController(t *testing.T, store Store) (*Controller, Generation) {
	t.Helper()
	controller := NewController()
	generation, err := controller.Activate(testBackend(t, store))
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	t.Cleanup(func() {
		_ = controller.Deactivate(context.Background(), generation)
	})
	return controller, generation
}

func performRequest(
	handler http.Handler,
	method, target, body string,
	contentType bool,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if contentType {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func requireNoStoreJSON(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func requireErrorIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want errors.Is(%v)", err, target)
	}
}
