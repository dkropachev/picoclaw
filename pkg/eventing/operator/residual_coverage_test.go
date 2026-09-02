package operator

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

func TestOperatorBackendResidualFailureAndProjectionMatrix(t *testing.T) {
	var nilBackend *Backend
	if _, err := nilBackend.GetEvent(t.Context(), testEventID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil GetEvent error = %v", err)
	}
	if _, err := nilBackend.GetEventPayload(t.Context(), testEventID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil GetEventPayload error = %v", err)
	}
	if _, err := nilBackend.GetWorkflowEvent(t.Context(), testEventID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil GetWorkflowEvent error = %v", err)
	}
	store := &fakeStore{}
	backend := testBackend(t, store)
	for _, id := range []string{"", "bad", "ev_ABCDEF00000000000000000000000000"} {
		if _, err := backend.GetEvent(t.Context(), id); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("GetEvent(%q) error = %v", id, err)
		}
		if _, err := backend.GetEventPayload(t.Context(), id); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("GetEventPayload(%q) error = %v", id, err)
		}
		if _, err := backend.GetWorkflowEvent(t.Context(), id); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("GetWorkflowEvent(%q) error = %v", id, err)
		}
	}
	store.getErr = errors.New("read failed")
	if _, err := backend.GetEvent(t.Context(), testEventID); err == nil {
		t.Fatal("GetEvent store failure lost")
	}
	if _, err := backend.GetEventPayload(t.Context(), testEventID); err == nil {
		t.Fatal("GetEventPayload store failure lost")
	}
	store.getErr = nil
	stored := testStoredEvent(testEventID)
	store.getResult = stored
	store.getResult.Envelope.ID = "ev_99999999999999999999999999999999"
	if _, err := backend.GetWorkflowEvent(t.Context(), testEventID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("workflow identity mismatch error = %v", err)
	}
	store.getResult = stored
	store.getResult.Envelope.Payload = json.RawMessage(`[]`)
	if _, err := backend.GetWorkflowEvent(t.Context(), testEventID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("workflow payload shape error = %v", err)
	}

	store.replayResult = eventing.InsertResult{Inserted: true, Event: eventing.StoredEvent{Envelope: eventing.Envelope{
		ID: "bad", ReplayOf: testEventID,
	}}}
	if _, err := backend.Replay(t.Context(), testEventID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("invalid replay ID error = %v", err)
	}
	store.replayResult.Event.Envelope.ID = "ev_99999999999999999999999999999999"
	store.replayResult.Event.Envelope.ReplayOf = "ev_22222222222222222222222222222222"
	if _, err := backend.Replay(t.Context(), testEventID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("invalid replay lineage error = %v", err)
	}

	if projectActor(nil) != nil || projectSubject(nil) != nil || cloneTime(nil) != nil || cloneStringMap(nil) != nil {
		t.Fatal("nil projection was non-nil")
	}
	now := time.Now()
	actor := &eventing.Actor{ID: "actor", Attributes: map[string]string{"key": "value"}}
	subject := &eventing.Subject{ID: "subject", Attributes: map[string]string{"key": "value"}}
	actorView, subjectView := projectActor(actor), projectSubject(subject)
	actorView.Attributes["key"], subjectView.Attributes["key"] = "changed", "changed"
	if actor.Attributes["key"] != "value" || subject.Attributes["key"] != "value" ||
		cloneTime(&now) == &now {
		t.Fatal("projection aliased source")
	}
}

func TestOperatorCursorAndRequestResidualBoundaries(t *testing.T) {
	if _, err := validateLimit(-1); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("negative limit error = %v", err)
	}
	if _, err := validateLimit(MaximumLimit + 1); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("large limit error = %v", err)
	}
	for _, value := range []string{" bad ", string([]byte{0xff}), "toolong"} {
		maximum := 6
		if err := validateOptionalText("field", value, maximum); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("invalid optional text %q error = %v", value, err)
		}
	}
	if err := validateOptionalText("field", "", 1); err != nil {
		t.Fatal(err)
	}
	for _, status := range []eventing.RoutingStatus{"", eventing.RoutingPending, eventing.RoutingDead} {
		if !validRoutingStatus(status) {
			t.Errorf("valid routing status rejected: %q", status)
		}
	}
	if validRoutingStatus("bad") || validDispatchStatus("bad") {
		t.Fatal("invalid status accepted")
	}
	if validEventID("ev_ABCDEF00000000000000000000000000") ||
		validDispatchID("dsp_ABCDEF00000000000000000000000000") {
		t.Fatal("uppercase ID accepted")
	}
	if validPrefixedHexID("ev_"+string(make([]byte, 32)), "ev_") {
		t.Fatal("NUL ID accepted")
	}
}
