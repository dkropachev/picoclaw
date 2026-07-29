//go:build !mipsle && !netbsd && !(freebsd && arm)

package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
)

var (
	errEventTestCreateDispatch = errors.New("injected create dispatch failure")
	errEventTestExecute        = errors.New("injected workflow execution failure")
	errEventTestLinkDispatch   = errors.New("injected dispatch link failure")
	errEventTestRenewLease     = errors.New("injected lease renewal failure")
	errEventTestRenewRouting   = errors.New("injected routing lease renewal failure")
)

func TestEventContextFromEnvelopeIncludesFullDetachedEnvelope(t *testing.T) {
	occurredAt := time.Date(2026, 7, 27, 18, 2, 3, 456789, time.FixedZone("test", -4*60*60))
	receivedAt := time.Date(2026, 7, 27, 22, 3, 4, 567890, time.FixedZone("test", -4*60*60))
	envelope := eventing.Envelope{
		ID:         "ev_00000000000000000000000000000001",
		Source:     "github",
		Connector:  "primary",
		Type:       "pull_request.opened",
		OccurredAt: &occurredAt,
		ReceivedAt: receivedAt,
		Payload: json.RawMessage(`{
			"action":"opened",
			"count":9007199254740993,
			"nested":{"labels":["automation"]}
		}`),
		Attributes: map[string]string{"installation": "production"},
		ReplayOf:   "ev_00000000000000000000000000000002",
		Actor: &eventing.Actor{
			ID:          "dependabot[bot]",
			Type:        "bot",
			DisplayName: "Dependabot",
			Attributes:  map[string]string{"role": "automation"},
		},
		Subject: &eventing.Subject{
			ID:         "repo-1",
			Type:       "repository",
			Name:       "picoclaw",
			URL:        "https://example.invalid/picoclaw",
			Attributes: map[string]string{"repository": "acme/picoclaw"},
		},
	}

	got, err := EventContextFromEnvelope(envelope)
	if err != nil {
		t.Fatalf("EventContextFromEnvelope() error = %v", err)
	}
	if got["id"] != envelope.ID ||
		got["source"] != envelope.Source ||
		got["connector"] != envelope.Connector ||
		got["type"] != envelope.Type ||
		got["replay_of"] != envelope.ReplayOf {
		t.Fatalf("identity fields = %#v", got)
	}
	if got["occurred_at"] != occurredAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("occurred_at = %#v", got["occurred_at"])
	}
	if got["received_at"] != receivedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("received_at = %#v", got["received_at"])
	}

	payload, ok := got["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload = %T, want map[string]any", got["payload"])
	}
	if count, numberOK := payload["count"].(json.Number); !numberOK || count.String() != "9007199254740993" {
		t.Fatalf("payload count = %#v, want lossless json.Number", payload["count"])
	}
	actor, ok := got["actor"].(map[string]any)
	if !ok ||
		actor["id"] != "dependabot[bot]" ||
		actor["type"] != "bot" ||
		actor["display_name"] != "Dependabot" {
		t.Fatalf("actor = %#v", got["actor"])
	}
	subject, ok := got["subject"].(map[string]any)
	if !ok ||
		subject["id"] != "repo-1" ||
		subject["type"] != "repository" ||
		subject["name"] != "picoclaw" ||
		subject["url"] != "https://example.invalid/picoclaw" {
		t.Fatalf("subject = %#v", got["subject"])
	}

	// Every reference-backed envelope field must be detached from the
	// expression context in both directions.
	payload["action"] = "mutated"
	payload["nested"].(map[string]any)["labels"].([]any)[0] = "mutated"
	got["attributes"].(map[string]any)["installation"] = "mutated"
	actor["attributes"].(map[string]any)["role"] = "mutated"
	subject["attributes"].(map[string]any)["repository"] = "mutated"
	if string(envelope.Payload) == "" ||
		envelope.Attributes["installation"] != "production" ||
		envelope.Actor.Attributes["role"] != "automation" ||
		envelope.Subject.Attributes["repository"] != "acme/picoclaw" {
		t.Fatalf("mutating context changed envelope: %#v", envelope)
	}

	envelope.Payload[0] = '['
	envelope.Attributes["installation"] = "changed again"
	envelope.Actor.Attributes["role"] = "changed again"
	envelope.Subject.Attributes["repository"] = "changed again"
	if payload["action"] != "mutated" ||
		got["attributes"].(map[string]any)["installation"] != "mutated" ||
		actor["attributes"].(map[string]any)["role"] != "mutated" ||
		subject["attributes"].(map[string]any)["repository"] != "mutated" {
		t.Fatalf("mutating envelope changed context: %#v", got)
	}
}

func TestEventContextFromEnvelopePreservesMissingOptionalFields(t *testing.T) {
	got, err := EventContextFromEnvelope(eventing.Envelope{
		ReceivedAt: time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC),
		Payload:    json.RawMessage(`{"ok":true}`),
	})
	if err != nil {
		t.Fatalf("EventContextFromEnvelope() error = %v", err)
	}
	for _, key := range []string{"occurred_at", "actor", "subject"} {
		value, exists := got[key]
		if !exists || value != nil {
			t.Errorf("%s = %#v, exists %v; want present nil", key, value, exists)
		}
	}
	if value := got["attributes"]; value != nil {
		attributes, ok := value.(map[string]any)
		if !ok || len(attributes) != 0 {
			t.Fatalf("attributes = %#v, want nil or empty map", value)
		}
	}
}

func TestEventContextFromEnvelopeRejectsNonObjectPayload(t *testing.T) {
	for _, payload := range []string{`[]`, `null`, `{"unterminated":`, `{"ok":true} {}`} {
		_, err := EventContextFromEnvelope(eventing.Envelope{Payload: json.RawMessage(payload)})
		if err == nil {
			t.Errorf("EventContextFromEnvelope(%s) succeeded, want error", payload)
		}
	}
}

func TestEventWorkflowRunContextFromEnvelopeSharesProductionAndPreviewShape(t *testing.T) {
	envelope := eventing.Envelope{
		ID:         "ev_00000000000000000000000000000001",
		Source:     "github",
		Connector:  "primary",
		Type:       "issues.opened",
		ReceivedAt: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC),
		Payload:    json.RawMessage(`{"count":9007199254740993}`),
	}
	const workflowRef = "workflows/triage.yml"
	const dispatchID = "dsp_11111111111111111111111111111111"

	production, err := EventWorkflowRunContextFromEnvelope(
		workflowRef,
		dispatchID,
		envelope,
	)
	if err != nil {
		t.Fatalf("EventWorkflowRunContextFromEnvelope(production) error = %v", err)
	}
	preview, err := EventWorkflowRunContextFromEnvelope(workflowRef, "", envelope)
	if err != nil {
		t.Fatalf("EventWorkflowRunContextFromEnvelope(preview) error = %v", err)
	}
	if production.Inputs["dispatch_id"] != dispatchID {
		t.Fatalf("production dispatch_id = %#v", production.Inputs["dispatch_id"])
	}
	if production.Origin == nil ||
		production.Origin.Kind != RunOriginExternalEvent ||
		production.Origin.EventID != envelope.ID ||
		production.Origin.DispatchID != dispatchID ||
		production.Origin.RootRunID != "" {
		t.Fatalf("production origin = %#v", production.Origin)
	}
	if _, present := preview.Inputs["dispatch_id"]; present {
		t.Fatalf("preview invented dispatch_id: %#v", preview.Inputs)
	}
	if preview.Origin == nil ||
		preview.Origin.Kind != RunOriginExternalEventDraftTest ||
		preview.Origin.EventID != envelope.ID ||
		preview.Origin.DispatchID != "" ||
		preview.Origin.RootRunID != "" {
		t.Fatalf("preview origin = %#v", preview.Origin)
	}
	for _, context := range []EventWorkflowRunContext{production, preview} {
		if context.Inputs["event_id"] != envelope.ID ||
			context.Inputs["source"] != envelope.Source ||
			context.Inputs["connector"] != envelope.Connector ||
			context.Inputs["type"] != envelope.Type ||
			context.Session != EventWorkflowSession(workflowRef, envelope.ID) ||
			!reflect.DeepEqual(context.Delivery, Delivery{}) {
			t.Fatalf("run context = %#v, want production event shape", context)
		}
		payload := context.Event["payload"].(map[string]any)
		number, ok := payload["count"].(json.Number)
		if !ok || number.String() != "9007199254740993" {
			t.Fatalf("event payload count = %#v, want exact json.Number", payload["count"])
		}
		if !reflect.DeepEqual(context.Inputs["event"], context.Event) {
			t.Fatalf("inputs.event = %#v, event = %#v", context.Inputs["event"], context.Event)
		}
	}
}

func TestEventContextJSONNumbersMatchExistingExpressionSemantics(t *testing.T) {
	event, err := EventContextFromEnvelope(eventing.Envelope{
		Payload: json.RawMessage(`{"count":3.5,"zero":0,"one":1.0,"exponent":1e0}`),
	})
	if err != nil {
		t.Fatalf("EventContextFromEnvelope() error = %v", err)
	}
	value, err := evalExpression(
		"event.payload.count > 3",
		expressionContext{Event: event},
	)
	if err != nil {
		t.Fatalf("evalExpression() error = %v", err)
	}
	if value != true {
		t.Fatalf("evalExpression() = %#v, want true", value)
	}
	payload := event["payload"].(map[string]any)
	if truthy(payload["zero"]) {
		t.Fatal("truthy(event.payload.zero) = true, want false")
	}
	if !truthy(payload["count"]) {
		t.Fatal("truthy(event.payload.count) = false, want true")
	}
	if !compareValues(payload["one"], "==", float64(1)) ||
		!compareValues(payload["exponent"], "==", float64(1)) {
		t.Fatalf("numeric equality did not normalize JSON numbers: %#v", payload)
	}
}

func TestExpressionNumericComparisonIsExact(t *testing.T) {
	t.Parallel()

	ctx := expressionContext{
		Event: map[string]any{
			"large": json.Number("9007199254740993"),
			"tiny":  json.Number("1e-10000"),
		},
	}
	tests := []struct {
		expression string
		want       bool
	}{
		{expression: "event.large == 9007199254740993", want: true},
		{expression: "event.large == 9007199254740992", want: false},
		{expression: "event.large > 9007199254740992", want: true},
		{expression: "1.0 == 1", want: true},
		{expression: "1e0 == 1", want: true},
		{expression: "1e-10000 > 0", want: true},
		{expression: "event.tiny", want: true},
	}
	for _, test := range tests {
		t.Run(test.expression, func(t *testing.T) {
			t.Parallel()
			got, err := evalIf(test.expression, ctx)
			if err != nil {
				t.Fatalf("evalIf(%q) error = %v", test.expression, err)
			}
			if got != test.want {
				t.Fatalf("evalIf(%q) = %t, want %t", test.expression, got, test.want)
			}
		})
	}
}

func TestExpressionNumericLiteralRenderingKeepsFloat64Compatibility(t *testing.T) {
	t.Parallel()

	got, err := renderString("${{ 1 }}", expressionContext{})
	if err != nil {
		t.Fatalf("renderString() error = %v", err)
	}
	if got != float64(1) {
		t.Fatalf("renderString() = %#v (%T), want float64(1)", got, got)
	}
}

func TestEventWorkflowRouterAcknowledgesEventWithZeroMatches(t *testing.T) {
	clock := newEventTestClock()
	store := openEventTestStore(t, clock)
	workspace := t.TempDir()
	writeEventTestWorkflow(t, workspace, "nonmatch.yml", "issues.closed")
	inserted := insertEventTestEnvelope(t, store, "router-zero")

	router := newEventTestRouter(store, workspace, clock)
	processed, err := router.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("ProcessOne() error = %v", err)
	}
	if !processed {
		t.Fatal("ProcessOne() processed = false, want true")
	}
	stored, err := store.Get(context.Background(), inserted.Envelope.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.Routing.Status != eventing.RoutingSucceeded {
		t.Fatalf("routing status = %q, want %q", stored.Routing.Status, eventing.RoutingSucceeded)
	}
	page, err := store.ListDispatches(
		context.Background(),
		eventing.DispatchFilter{EventID: inserted.Envelope.ID},
	)
	if err != nil {
		t.Fatalf("ListDispatches() error = %v", err)
	}
	if len(page.Dispatches) != 0 {
		t.Fatalf("dispatches = %#v, want none", page.Dispatches)
	}
}

func TestEventWorkflowRouterFansOutAndDeduplicatesExistingDispatch(t *testing.T) {
	clock := newEventTestClock()
	store := openEventTestStore(t, clock)
	workspace := t.TempDir()
	writeEventTestWorkflow(t, workspace, "a.yml", "pull_request.*")
	writeEventTestWorkflow(t, workspace, "nested/b.yaml", "pull_request.opened")
	inserted := insertEventTestEnvelope(t, store, "router-fanout")

	existing, created, err := store.CreateDispatch(
		context.Background(),
		inserted.Envelope.ID,
		"workflows/a.yml",
	)
	if err != nil || !created {
		t.Fatalf("CreateDispatch() = %#v, %v, %v", existing, created, err)
	}

	router := newEventTestRouter(store, workspace, clock)
	processed, err := router.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("ProcessOne() error = %v", err)
	}
	if !processed {
		t.Fatal("ProcessOne() processed = false, want true")
	}
	page, err := store.ListDispatches(
		context.Background(),
		eventing.DispatchFilter{EventID: inserted.Envelope.ID},
	)
	if err != nil {
		t.Fatalf("ListDispatches() error = %v", err)
	}
	if len(page.Dispatches) != 2 {
		t.Fatalf("dispatch count = %d, want 2: %#v", len(page.Dispatches), page.Dispatches)
	}
	byRef := make(map[string]eventing.Dispatch, len(page.Dispatches))
	for _, dispatch := range page.Dispatches {
		byRef[dispatch.WorkflowRef] = dispatch
		if !strings.HasPrefix(dispatch.WorkflowRevision, "sha256:") {
			t.Fatalf("dispatch %s revision = %q, want persisted content hash", dispatch.ID, dispatch.WorkflowRevision)
		}
	}
	if byRef["workflows/a.yml"].ID != existing.ID ||
		byRef["workflows/a.yml"].RunID != existing.RunID {
		t.Fatalf("existing dispatch was not reused: old %#v, new %#v", existing, byRef["workflows/a.yml"])
	}
	if _, ok := byRef["workflows/nested/b.yaml"]; !ok {
		t.Fatalf("nested matching workflow was not dispatched: %#v", byRef)
	}
}

func TestEventWorkflowRouterPublishesCreatedDispatchMetadataOnce(t *testing.T) {
	clock := newEventTestClock()
	store := openEventTestStore(t, clock)
	workspace := t.TempDir()
	ref := writeEventTestWorkflow(t, workspace, "telemetry.yml", "*")
	insertEventTestEnvelope(t, store, "router-telemetry")
	publisher := &fakeRuntimeEventPublisher{}
	router := newEventTestRouter(store, workspace, clock)
	router.RuntimeEvents = publisher
	claimed, err := store.ClaimRouting(context.Background(), "telemetry-router", 1, time.Minute)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimRouting() = %#v, %v", claimed, err)
	}
	inserted := claimed[0]

	if routeErr := router.routeClaim(context.Background(), inserted); routeErr != nil {
		t.Fatalf("first routeClaim() error = %v", routeErr)
	}
	if routeErr := router.routeClaim(context.Background(), inserted); routeErr != nil {
		t.Fatalf("duplicate routeClaim() error = %v", routeErr)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("published events = %#v, want exactly one created-dispatch event", publisher.events)
	}
	event := publisher.events[0]
	if event.Kind != runtimeevents.KindWorkflowTriggered ||
		event.Severity != runtimeevents.SeverityInfo ||
		event.Source != (runtimeevents.Source{Component: "workflow", Name: ref}) {
		t.Fatalf("workflow trigger event identity = %#v", event)
	}
	if event.Scope.SessionKey != EventWorkflowSession(ref, inserted.Envelope.ID) ||
		event.Correlation.RequestID != inserted.Envelope.ID {
		t.Fatalf("workflow trigger scope/correlation = %#v / %#v", event.Scope, event.Correlation)
	}
	dispatches, err := store.ListDispatches(
		context.Background(),
		eventing.DispatchFilter{EventID: inserted.Envelope.ID},
	)
	if err != nil || len(dispatches.Dispatches) != 1 {
		t.Fatalf("ListDispatches() = %#v, %v", dispatches, err)
	}
	wantAttrs := map[string]any{
		"trigger":     "event",
		"event_id":    inserted.Envelope.ID,
		"dispatch_id": dispatches.Dispatches[0].ID,
		"source":      inserted.Envelope.Source,
		"connector":   inserted.Envelope.Connector,
		"event_type":  inserted.Envelope.Type,
	}
	if !reflect.DeepEqual(event.Attrs, wantAttrs) {
		t.Fatalf("workflow trigger attrs = %#v, want metadata-only %#v", event.Attrs, wantAttrs)
	}
	if event.Payload != nil {
		t.Fatalf("workflow trigger payload = %#v, want nil to avoid event payload or secrets", event.Payload)
	}
}

func TestEventWorkflowRouterRetriesPartialFanoutIdempotently(t *testing.T) {
	clock := newEventTestClock()
	store := openEventTestStore(t, clock)
	workspace := t.TempDir()
	writeEventTestWorkflow(t, workspace, "a.yml", "*")
	writeEventTestWorkflow(t, workspace, "b.yml", "*")
	inserted := insertEventTestEnvelope(t, store, "router-partial")
	inbox := &createFailureEventInbox{
		Store:      store,
		failOnCall: 2,
	}
	router := newEventTestRouter(inbox, workspace, clock)

	processed, err := router.ProcessOne(context.Background())
	if !processed || !errors.Is(err, errEventTestCreateDispatch) {
		t.Fatalf("first ProcessOne() = %v, %v, want processed injected error", processed, err)
	}
	afterFailure, err := store.Get(context.Background(), inserted.Envelope.ID)
	if err != nil {
		t.Fatalf("Get() after failure error = %v", err)
	}
	if afterFailure.Routing.Status != eventing.RoutingPending ||
		afterFailure.Routing.Attempts != 1 {
		t.Fatalf("routing after failure = %#v", afterFailure.Routing)
	}
	firstPage, err := store.ListDispatches(
		context.Background(),
		eventing.DispatchFilter{EventID: inserted.Envelope.ID},
	)
	if err != nil {
		t.Fatalf("ListDispatches() after failure error = %v", err)
	}
	if len(firstPage.Dispatches) != 1 {
		t.Fatalf("partial dispatch count = %d, want 1", len(firstPage.Dispatches))
	}
	firstID := firstPage.Dispatches[0].ID

	clock.Advance(time.Second)
	processed, err = router.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("second ProcessOne() = %v, %v", processed, err)
	}
	finished, err := store.Get(context.Background(), inserted.Envelope.ID)
	if err != nil {
		t.Fatalf("Get() after retry error = %v", err)
	}
	if finished.Routing.Status != eventing.RoutingSucceeded ||
		finished.Routing.Attempts != 2 {
		t.Fatalf("routing after retry = %#v", finished.Routing)
	}
	page, err := store.ListDispatches(
		context.Background(),
		eventing.DispatchFilter{EventID: inserted.Envelope.ID},
	)
	if err != nil {
		t.Fatalf("ListDispatches() after retry error = %v", err)
	}
	if len(page.Dispatches) != 2 {
		t.Fatalf("dispatch count = %d, want 2: %#v", len(page.Dispatches), page.Dispatches)
	}
	foundFirst := false
	for _, dispatch := range page.Dispatches {
		foundFirst = foundFirst || dispatch.ID == firstID
	}
	if !foundFirst {
		t.Fatalf("first durable dispatch %q was replaced on retry: %#v", firstID, page.Dispatches)
	}
}

func TestEventWorkflowRouterMarksAttemptExhaustionDead(t *testing.T) {
	clock := newEventTestClock()
	store := openEventTestStore(t, clock)
	workspace := t.TempDir()
	writeEventTestWorkflow(t, workspace, "always.yml", "*")
	inserted := insertEventTestEnvelope(t, store, "router-dead")
	inbox := &createFailureEventInbox{Store: store, alwaysFail: true}
	router := newEventTestRouter(inbox, workspace, clock)
	router.MaxAttempts = 1

	processed, err := router.ProcessOne(context.Background())
	if !processed || !errors.Is(err, errEventTestCreateDispatch) {
		t.Fatalf("ProcessOne() = %v, %v, want processed injected error", processed, err)
	}
	stored, err := store.Get(context.Background(), inserted.Envelope.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.Routing.Status != eventing.RoutingDead ||
		stored.Routing.Attempts != 1 ||
		!strings.Contains(stored.Routing.LastError, errEventTestCreateDispatch.Error()) {
		t.Fatalf("routing = %#v, want exhausted dead state", stored.Routing)
	}
}

func TestEventWorkflowRouterBoundsInitialLeaseRenewalFailures(t *testing.T) {
	clock := newEventTestClock()
	store := openEventTestStore(t, clock)
	workspace := t.TempDir()
	writeEventTestWorkflow(t, workspace, "renew-failure.yml", "*")
	inserted := insertEventTestEnvelope(t, store, "router-renew-dead")
	inbox := &routingRenewFailureEventInbox{Store: store}
	router := newEventTestRouter(inbox, workspace, clock)
	router.MaxAttempts = 1

	processed, err := router.ProcessOne(context.Background())
	if !processed || !errors.Is(err, errEventTestRenewRouting) {
		t.Fatalf("ProcessOne() = %v, %v, want exhausted routing renewal error", processed, err)
	}
	stored, err := store.Get(context.Background(), inserted.Envelope.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.Routing.Status != eventing.RoutingDead ||
		stored.Routing.Attempts != 1 ||
		!strings.Contains(stored.Routing.LastError, errEventTestRenewRouting.Error()) {
		t.Fatalf("routing = %#v, want exhausted dead state", stored.Routing)
	}
}

func TestEventWorkflowDispatcherRunsDeterministicRequestAndSucceeds(t *testing.T) {
	fixture := newEventDispatchFixture(t, "dispatcher-success")
	executor := &recordingEventExecutor{
		run: func(_ context.Context, req RunRequest) (*RunResult, error) {
			createAndLinkEventTestRun(t, req, fixture.runStore, &Run{
				ID:          req.RunID,
				WorkflowRef: req.WorkflowRef,
				Status:      RunStatusSucceeded,
			})
			return &RunResult{RunID: req.RunID, Status: RunStatusSucceeded}, nil
		},
	}
	dispatcher := fixture.dispatcher(executor)

	processed, err := dispatcher.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("ProcessOne() error = %v", err)
	}
	if !processed {
		t.Fatal("ProcessOne() processed = false, want true")
	}
	requests := executor.Requests()
	if len(requests) != 1 {
		t.Fatalf("executor request count = %d, want 1", len(requests))
	}
	request := requests[0]
	if request.RunID != fixture.dispatch.RunID ||
		request.Ref != fixture.dispatch.WorkflowRef ||
		request.WorkflowRef != fixture.dispatch.WorkflowRef {
		t.Fatalf("request identity = %#v, dispatch = %#v", request, fixture.dispatch)
	}
	wantSession := EventWorkflowSession(fixture.dispatch.WorkflowRef, fixture.event.Envelope.ID)
	if request.Session != wantSession {
		t.Fatalf("session = %q, want %q", request.Session, wantSession)
	}
	if !reflect.DeepEqual(request.Delivery, Delivery{}) {
		t.Fatalf("delivery = %#v, want empty", request.Delivery)
	}
	if request.Inputs["event_id"] != fixture.event.Envelope.ID ||
		request.Inputs["dispatch_id"] != fixture.dispatch.ID ||
		request.Inputs["source"] != fixture.event.Envelope.Source ||
		request.Inputs["connector"] != fixture.event.Envelope.Connector ||
		request.Inputs["type"] != fixture.event.Envelope.Type {
		t.Fatalf("request inputs = %#v", request.Inputs)
	}
	if request.Event["id"] != fixture.event.Envelope.ID ||
		request.Event["payload"].(map[string]any)["action"] != "opened" {
		t.Fatalf("request event = %#v", request.Event)
	}
	if request.Origin == nil ||
		request.Origin.Kind != RunOriginExternalEvent ||
		request.Origin.EventID != fixture.event.Envelope.ID ||
		request.Origin.DispatchID != fixture.dispatch.ID {
		t.Fatalf("request origin = %#v", request.Origin)
	}
	if request.Workflow == nil || request.Workflow.On.Event == nil {
		t.Fatalf("request workflow = %#v", request.Workflow)
	}

	finished := fixture.getDispatch(t)
	if finished.Status != eventing.DispatchSucceeded ||
		finished.RunID != fixture.dispatch.RunID ||
		finished.LinkedAt == nil ||
		finished.FinishedAt == nil {
		t.Fatalf("finished dispatch = %#v", finished)
	}
	duplicate, created, err := fixture.store.CreateDispatch(
		context.Background(),
		fixture.event.Envelope.ID,
		fixture.dispatch.WorkflowRef,
	)
	if err != nil || created ||
		duplicate.ID != fixture.dispatch.ID ||
		duplicate.RunID != fixture.dispatch.RunID {
		t.Fatalf("deterministic duplicate = %#v, %v, %v", duplicate, created, err)
	}
}

func TestEventWorkflowDispatcherRejectsDefinitionDriftAfterRouting(t *testing.T) {
	clock := newEventTestClock()
	store := openEventTestStore(t, clock)
	workspace := t.TempDir()
	ref := writeEventTestWorkflow(t, workspace, "drift.yml", "*")
	inserted := insertEventTestEnvelope(t, store, "dispatcher-definition-drift")
	router := newEventTestRouter(store, workspace, clock)
	processed, err := router.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("router ProcessOne() = %v, %v", processed, err)
	}
	page, err := store.ListDispatches(
		context.Background(),
		eventing.DispatchFilter{EventID: inserted.Envelope.ID},
	)
	if err != nil || len(page.Dispatches) != 1 {
		t.Fatalf("ListDispatches() = %#v, %v", page, err)
	}
	selected := page.Dispatches[0]
	if !strings.HasPrefix(selected.WorkflowRevision, "sha256:") {
		t.Fatalf("selected revision = %q, want content hash", selected.WorkflowRevision)
	}

	// Version B still matches, so trigger re-evaluation alone cannot authorize
	// it under version A's durable selection.
	writeEventTestWorkflow(t, workspace, "drift.yml", "pull_request.*")
	executor := &recordingEventExecutor{
		run: func(context.Context, RunRequest) (*RunResult, error) {
			t.Fatal("executor called after selected workflow revision drifted")
			return nil, nil
		},
	}
	dispatcher := &EventWorkflowDispatcher{
		Inbox:         store,
		Executor:      executor,
		RunStore:      NewFileRunStore(workspace),
		WorkspaceDir:  workspace,
		LeaseDuration: time.Minute,
		MaxAttempts:   1,
		RetryBase:     time.Second,
		RetryMax:      time.Second,
		Now:           clock.Now,
	}
	processed, err = dispatcher.ProcessOne(context.Background())
	if !processed || err == nil || !strings.Contains(err.Error(), "revision changed") {
		t.Fatalf("dispatcher ProcessOne() = %v, %v, want revision-drift failure", processed, err)
	}
	if len(executor.Requests()) != 0 {
		t.Fatalf("executor requests = %#v, want none", executor.Requests())
	}
	finished, err := store.GetDispatch(context.Background(), selected.ID)
	if err != nil {
		t.Fatalf("GetDispatch() error = %v", err)
	}
	if finished.Status != eventing.DispatchDead ||
		!strings.Contains(finished.LastError, "revision changed") {
		t.Fatalf("drifted dispatch = %#v, want dead fail-closed state", finished)
	}
	if finished.WorkflowRef != ref {
		t.Fatalf("workflow ref = %q, want %q", finished.WorkflowRef, ref)
	}
}

func TestEventWorkflowDispatcherLegacyDispatchReevaluatesCurrentTrigger(t *testing.T) {
	fixture := newEventDispatchFixture(t, "dispatcher-legacy-trigger-drift")
	if fixture.dispatch.WorkflowRevision != "" {
		t.Fatalf("legacy dispatch revision = %q, want empty", fixture.dispatch.WorkflowRevision)
	}
	writeEventTestWorkflow(t, fixture.workspace, "dispatch.yml", "issues.*")
	executor := &recordingEventExecutor{
		run: func(context.Context, RunRequest) (*RunResult, error) {
			t.Fatal("executor called for legacy dispatch whose current trigger does not match")
			return nil, nil
		},
	}
	dispatcher := fixture.dispatcher(executor)
	dispatcher.MaxAttempts = 1

	processed, err := dispatcher.ProcessOne(context.Background())
	if !processed || err == nil || !strings.Contains(err.Error(), "no longer matches") {
		t.Fatalf("ProcessOne() = %v, %v, want trigger re-evaluation failure", processed, err)
	}
	if len(executor.Requests()) != 0 {
		t.Fatalf("executor requests = %#v, want none", executor.Requests())
	}
	finished := fixture.getDispatch(t)
	if finished.Status != eventing.DispatchDead ||
		!strings.Contains(finished.LastError, "no longer matches") {
		t.Fatalf("legacy dispatch = %#v, want dead fail-closed state", finished)
	}
}

func TestEventWorkflowDispatcherFinishesFailedRun(t *testing.T) {
	fixture := newEventDispatchFixture(t, "dispatcher-failed")
	executor := &recordingEventExecutor{
		run: func(_ context.Context, req RunRequest) (*RunResult, error) {
			createAndLinkEventTestRun(t, req, fixture.runStore, &Run{
				ID:          req.RunID,
				WorkflowRef: req.WorkflowRef,
				Status:      RunStatusFailed,
				Error:       "workflow failed safely",
			})
			return &RunResult{
				RunID:  req.RunID,
				Status: RunStatusFailed,
				Error:  "workflow failed safely",
			}, errEventTestExecute
		},
	}

	processed, err := fixture.dispatcher(executor).ProcessOne(context.Background())
	if !processed || !errors.Is(err, errEventTestExecute) {
		t.Fatalf("ProcessOne() = %v, %v, want processed execution error", processed, err)
	}
	finished := fixture.getDispatch(t)
	if finished.Status != eventing.DispatchFailed ||
		finished.LastError != "workflow failed safely" ||
		finished.FinishedAt == nil {
		t.Fatalf("finished dispatch = %#v", finished)
	}
}

func TestEventWorkflowDispatcherRetriesConcurrencyThenSucceeds(t *testing.T) {
	fixture := newEventDispatchFixture(t, "dispatcher-concurrency")
	executor := &recordingEventExecutor{
		run: func(_ context.Context, _ RunRequest) (*RunResult, error) {
			return nil, ErrRunConcurrencyLimit
		},
	}
	dispatcher := fixture.dispatcher(executor)

	processed, err := dispatcher.ProcessOne(context.Background())
	if !processed || !errors.Is(err, ErrRunConcurrencyLimit) {
		t.Fatalf("first ProcessOne() = %v, %v, want concurrency retry", processed, err)
	}
	pending := fixture.getDispatch(t)
	if pending.Status != eventing.DispatchPending ||
		pending.Attempts != 1 ||
		!strings.Contains(pending.LastError, ErrRunConcurrencyLimit.Error()) ||
		pending.LinkedAt != nil {
		t.Fatalf("pending dispatch = %#v", pending)
	}

	fixture.clock.Advance(time.Second)
	executor.SetRun(func(_ context.Context, req RunRequest) (*RunResult, error) {
		createAndLinkEventTestRun(t, req, fixture.runStore, &Run{
			ID:          req.RunID,
			WorkflowRef: req.WorkflowRef,
			Status:      RunStatusSucceeded,
		})
		return &RunResult{RunID: req.RunID, Status: RunStatusSucceeded}, nil
	})
	processed, err = dispatcher.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("second ProcessOne() = %v, %v", processed, err)
	}
	finished := fixture.getDispatch(t)
	if finished.Status != eventing.DispatchSucceeded || finished.Attempts != 2 {
		t.Fatalf("finished dispatch = %#v", finished)
	}
	if len(executor.Requests()) != 2 {
		t.Fatalf("executor request count = %d, want 2", len(executor.Requests()))
	}
}

func TestEventWorkflowDispatcherMarksPrestartAttemptExhaustionDead(t *testing.T) {
	fixture := newEventDispatchFixture(t, "dispatcher-dead")
	executor := &recordingEventExecutor{
		run: func(_ context.Context, _ RunRequest) (*RunResult, error) {
			return nil, ErrRunConcurrencyLimit
		},
	}
	dispatcher := fixture.dispatcher(executor)
	dispatcher.MaxAttempts = 1

	processed, err := dispatcher.ProcessOne(context.Background())
	if !processed || !errors.Is(err, ErrRunConcurrencyLimit) {
		t.Fatalf("ProcessOne() = %v, %v, want exhausted concurrency error", processed, err)
	}
	finished := fixture.getDispatch(t)
	if finished.Status != eventing.DispatchDead ||
		finished.Attempts != 1 ||
		!strings.Contains(finished.LastError, ErrRunConcurrencyLimit.Error()) {
		t.Fatalf("finished dispatch = %#v, want dead", finished)
	}
}

func TestEventWorkflowDispatcherFailsPersistedRunWhenLinkFails(t *testing.T) {
	fixture := newEventDispatchFixture(t, "dispatcher-link-failed")
	executor := &recordingEventExecutor{
		run: func(_ context.Context, req RunRequest) (*RunResult, error) {
			run := &Run{
				ID:          req.RunID,
				WorkflowRef: req.WorkflowRef,
				Status:      RunStatusRunning,
			}
			createEventTestRun(t, fixture.runStore, run)
			callbackErr := req.OnRunPersisted(run)
			if !errors.Is(callbackErr, errEventTestLinkDispatch) {
				t.Fatalf("OnRunPersisted() error = %v, want dispatch link error", callbackErr)
			}
			run.Status = RunStatusFailed
			run.Error = callbackErr.Error()
			if err := fixture.runStore.UpdateRun(context.Background(), run); err != nil {
				t.Fatalf("UpdateRun() error = %v", err)
			}
			return &RunResult{
				RunID:  req.RunID,
				Status: RunStatusFailed,
				Error:  callbackErr.Error(),
			}, callbackErr
		},
	}
	dispatcher := fixture.dispatcher(executor)
	dispatcher.Inbox = &linkFailureEventInbox{Store: fixture.store}

	processed, err := dispatcher.ProcessOne(context.Background())
	if !processed || !errors.Is(err, errEventTestLinkDispatch) {
		t.Fatalf("ProcessOne() = %v, %v, want dispatch link error", processed, err)
	}
	finished := fixture.getDispatch(t)
	if finished.Status != eventing.DispatchFailed ||
		finished.Attempts != 1 ||
		!strings.Contains(finished.LastError, errEventTestLinkDispatch.Error()) {
		t.Fatalf("finished dispatch = %#v, want terminal failure", finished)
	}
	processed, err = dispatcher.ProcessOne(context.Background())
	if err != nil || processed {
		t.Fatalf("second ProcessOne() = %v, %v, want no replay", processed, err)
	}
	if len(executor.Requests()) != 1 {
		t.Fatalf("executor request count = %d, want 1", len(executor.Requests()))
	}
}

func TestEventWorkflowDispatcherRenewsNearExpiredLeaseBeforeExecution(t *testing.T) {
	fixture := newEventDispatchFixture(t, "dispatcher-prestart-renew")
	inbox := &advanceOnLinkEventInbox{
		Store:   fixture.store,
		clock:   fixture.clock,
		advance: 59 * time.Second,
	}
	executor := &recordingEventExecutor{
		run: func(_ context.Context, req RunRequest) (*RunResult, error) {
			createAndLinkEventTestRun(t, req, fixture.runStore, &Run{
				ID:          req.RunID,
				WorkflowRef: req.WorkflowRef,
				Status:      RunStatusSucceeded,
			})
			running := fixture.getDispatch(t)
			if running.LeaseUntil == nil ||
				!running.LeaseUntil.Equal(fixture.clock.Now().Add(time.Minute)) {
				t.Fatalf(
					"running lease deadline = %v, want synchronous renewal from %v",
					running.LeaseUntil,
					fixture.clock.Now(),
				)
			}
			fixture.clock.Advance(2 * time.Second)
			reclaimed, err := fixture.store.ClaimDispatches(
				context.Background(),
				"competing-dispatcher",
				1,
				time.Minute,
			)
			if err != nil {
				t.Fatalf("competing ClaimDispatches() error = %v", err)
			}
			if len(reclaimed) != 0 {
				t.Fatalf("competing dispatcher reclaimed active run: %#v", reclaimed)
			}
			return &RunResult{RunID: req.RunID, Status: RunStatusSucceeded}, nil
		},
	}
	dispatcher := fixture.dispatcher(executor)
	dispatcher.Inbox = inbox
	dispatcher.LeaseDuration = time.Minute

	processed, err := dispatcher.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = %v, %v", processed, err)
	}
	if finished := fixture.getDispatch(t); finished.Status != eventing.DispatchSucceeded {
		t.Fatalf("finished dispatch = %#v", finished)
	}
}

func TestEventWorkflowDispatcherRefusesReplayWhenCreatedRunRecordIsMissing(t *testing.T) {
	fixture := newEventDispatchFixture(t, "dispatcher-missing-created-run")
	claimed, err := fixture.store.ClaimDispatches(
		context.Background(),
		"first-dispatcher",
		1,
		time.Minute,
	)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimDispatches() = %#v, %v", claimed, err)
	}
	if linkErr := fixture.store.LinkDispatchRun(
		context.Background(),
		claimed[0].ID,
		claimed[0].LeaseToken,
		claimed[0].RunID,
	); linkErr != nil {
		t.Fatalf("LinkDispatchRun() error = %v", linkErr)
	}
	fixture.clock.Advance(time.Minute)
	executor := &recordingEventExecutor{
		run: func(_ context.Context, _ RunRequest) (*RunResult, error) {
			t.Fatal("executor called after a previously created run record disappeared")
			return nil, nil
		},
	}

	processed, err := fixture.dispatcher(executor).ProcessOne(context.Background())
	if !processed || err == nil || !strings.Contains(err.Error(), "refusing replay") {
		t.Fatalf("ProcessOne() = %v, %v, want missing-run fail closed", processed, err)
	}
	finished := fixture.getDispatch(t)
	if finished.Status != eventing.DispatchFailed ||
		!strings.Contains(finished.LastError, "refusing replay") {
		t.Fatalf("finished dispatch = %#v, want failed without replay", finished)
	}
	if len(executor.Requests()) != 0 {
		t.Fatalf("executor request count = %d, want 0", len(executor.Requests()))
	}
}

func TestEventWorkflowDispatcherReconcilesExistingTerminalRuns(t *testing.T) {
	tests := []struct {
		name          string
		status        string
		runError      string
		cancelReason  string
		wantStatus    eventing.DispatchStatus
		wantLastError string
	}{
		{
			name:       "succeeded",
			status:     RunStatusSucceeded,
			wantStatus: eventing.DispatchSucceeded,
		},
		{
			name:          "failed",
			status:        RunStatusFailed,
			runError:      "durable run failed",
			wantStatus:    eventing.DispatchFailed,
			wantLastError: "durable run failed",
		},
		{
			name:          "canceled",
			status:        RunStatusCanceled,
			cancelReason:  "operator canceled",
			wantStatus:    eventing.DispatchFailed,
			wantLastError: "operator canceled",
		},
		{
			name:          "skipped",
			status:        RunStatusSkipped,
			wantStatus:    eventing.DispatchFailed,
			wantLastError: "workflow run did not succeed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newEventDispatchFixture(t, "dispatcher-existing-"+test.name)
			createEventTestRun(t, fixture.runStore, &Run{
				ID:           fixture.dispatch.RunID,
				WorkflowRef:  fixture.dispatch.WorkflowRef,
				Status:       test.status,
				Error:        test.runError,
				CancelReason: test.cancelReason,
			})
			executor := &recordingEventExecutor{
				run: func(_ context.Context, _ RunRequest) (*RunResult, error) {
					t.Fatal("executor called for an existing terminal run")
					return nil, nil
				},
			}

			processed, err := fixture.dispatcher(executor).ProcessOne(context.Background())
			if err != nil || !processed {
				t.Fatalf("ProcessOne() = %v, %v", processed, err)
			}
			if len(executor.Requests()) != 0 {
				t.Fatalf("executor request count = %d, want 0", len(executor.Requests()))
			}
			finished := fixture.getDispatch(t)
			if finished.Status != test.wantStatus ||
				finished.LastError != test.wantLastError {
				t.Fatalf("finished dispatch = %#v", finished)
			}
		})
	}
}

func TestEventWorkflowDispatcherCancelsInterruptedExistingRun(t *testing.T) {
	fixture := newEventDispatchFixture(t, "dispatcher-existing-running")
	createEventTestRun(t, fixture.runStore, &Run{
		ID:          fixture.dispatch.RunID,
		WorkflowRef: fixture.dispatch.WorkflowRef,
		Status:      RunStatusRunning,
	})
	executor := &recordingEventExecutor{
		run: func(_ context.Context, _ RunRequest) (*RunResult, error) {
			t.Fatal("executor called for an interrupted existing run")
			return nil, nil
		},
	}

	processed, err := fixture.dispatcher(executor).ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = %v, %v", processed, err)
	}
	run, err := fixture.runStore.GetRun(context.Background(), fixture.dispatch.RunID)
	if err != nil {
		t.Fatalf("GetRun() error = %v", err)
	}
	const reason = "event dispatch recovered after an interrupted workflow execution"
	if run.Status != RunStatusCanceled || run.CancelReason != reason {
		t.Fatalf("reconciled run = %#v", run)
	}
	finished := fixture.getDispatch(t)
	if finished.Status != eventing.DispatchFailed ||
		finished.LastError != reason {
		t.Fatalf("finished dispatch = %#v", finished)
	}
}

func TestEventWorkflowDispatcherStaleWorkerCannotCancelReclaimedRunningRun(t *testing.T) {
	fixture := newEventDispatchFixture(t, "dispatcher-stale-reconcile")
	runStore := &blockingFirstGetRunStore{
		RunStore: fixture.runStore,
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	staleExecutor := &recordingEventExecutor{
		run: func(_ context.Context, _ RunRequest) (*RunResult, error) {
			t.Fatal("stale dispatcher called executor")
			return nil, nil
		},
	}
	staleDispatcher := fixture.dispatcher(staleExecutor)
	staleDispatcher.RunStore = runStore

	staleDone := make(chan error, 1)
	go func() {
		_, processErr := staleDispatcher.ProcessOne(context.Background())
		staleDone <- processErr
	}()
	select {
	case <-runStore.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("stale dispatcher did not block while loading the deterministic run")
	}

	// Expire the first claim without waiting for the real-time heartbeat. The
	// replacement worker must be able to own, link, and execute the run before
	// the stale GetRun call returns.
	fixture.clock.Advance(time.Minute)
	replacementRunning := make(chan struct{})
	finishReplacement := make(chan struct{})
	replacementExecutor := &recordingEventExecutor{
		run: func(_ context.Context, req RunRequest) (*RunResult, error) {
			createAndLinkEventTestRun(t, req, runStore, &Run{
				ID:          req.RunID,
				WorkflowRef: req.WorkflowRef,
				Status:      RunStatusRunning,
			})
			close(replacementRunning)
			<-finishReplacement
			run, getErr := runStore.RunStore.GetRun(context.Background(), req.RunID)
			if getErr != nil {
				return nil, getErr
			}
			run.Status = RunStatusSucceeded
			if updateErr := runStore.UpdateRun(context.Background(), run); updateErr != nil {
				return nil, updateErr
			}
			return &RunResult{RunID: req.RunID, Status: RunStatusSucceeded}, nil
		},
	}
	replacementDispatcher := fixture.dispatcher(replacementExecutor)
	replacementDispatcher.RunStore = runStore
	replacementDone := make(chan error, 1)
	go func() {
		_, processErr := replacementDispatcher.ProcessOne(context.Background())
		replacementDone <- processErr
	}()
	select {
	case <-replacementRunning:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement dispatcher did not start the deterministic run")
	}

	close(runStore.release)
	select {
	case staleErr := <-staleDone:
		if !errors.Is(staleErr, eventing.ErrStaleLease) {
			t.Fatalf("stale ProcessOne() error = %v, want stale lease", staleErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stale dispatcher did not return after GetRun was released")
	}
	if calls := runStore.CancelCalls(); calls != 0 {
		t.Fatalf("stale dispatcher canceled replacement run %d times", calls)
	}
	running, err := runStore.RunStore.GetRun(
		context.Background(),
		fixture.dispatch.RunID,
	)
	if err != nil {
		t.Fatalf("GetRun() while replacement owns run error = %v", err)
	}
	if running.Status != RunStatusRunning {
		t.Fatalf("replacement run status = %q, want running", running.Status)
	}

	close(finishReplacement)
	select {
	case replacementErr := <-replacementDone:
		if replacementErr != nil {
			t.Fatalf("replacement ProcessOne() error = %v", replacementErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replacement dispatcher did not finish")
	}
}

func TestEventWorkflowDispatcherHonorsRunThatSucceedsDuringCancellation(t *testing.T) {
	fixture := newEventDispatchFixture(t, "dispatcher-running-race")
	createEventTestRun(t, fixture.runStore, &Run{
		ID:          fixture.dispatch.RunID,
		WorkflowRef: fixture.dispatch.WorkflowRef,
		Status:      RunStatusRunning,
	})
	runStore := &cancelResultEventRunStore{
		RunStore: fixture.runStore,
		result: &Run{
			ID:          fixture.dispatch.RunID,
			WorkflowRef: fixture.dispatch.WorkflowRef,
			Status:      RunStatusSucceeded,
		},
	}
	executor := &recordingEventExecutor{
		run: func(_ context.Context, _ RunRequest) (*RunResult, error) {
			t.Fatal("executor called for concurrently completed run")
			return nil, nil
		},
	}
	dispatcher := fixture.dispatcher(executor)
	dispatcher.RunStore = runStore

	processed, err := dispatcher.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("ProcessOne() = %v, %v", processed, err)
	}
	if finished := fixture.getDispatch(t); finished.Status != eventing.DispatchSucceeded {
		t.Fatalf("finished dispatch = %#v, want succeeded", finished)
	}
}

func TestEventWorkflowDispatcherRejectsMismatchedExistingRunIdentity(t *testing.T) {
	fixture := newEventDispatchFixture(t, "dispatcher-mismatched-run")
	createEventTestRun(t, fixture.runStore, &Run{
		ID:          fixture.dispatch.RunID,
		WorkflowRef: "workflows/other.yml",
		Status:      RunStatusSucceeded,
	})
	executor := &recordingEventExecutor{
		run: func(_ context.Context, _ RunRequest) (*RunResult, error) {
			t.Fatal("executor called for mismatched existing run")
			return nil, nil
		},
	}

	processed, err := fixture.dispatcher(executor).ProcessOne(context.Background())
	if !processed || err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("ProcessOne() = %v, %v, want identity mismatch", processed, err)
	}
	pending := fixture.getDispatch(t)
	if pending.Status != eventing.DispatchPending ||
		!strings.Contains(pending.LastError, "identity mismatch") {
		t.Fatalf("pending dispatch = %#v, want fail-closed retry", pending)
	}
}

func TestEventWorkflowDispatcherReconcilesRunCreatedDespiteAlreadyExistsError(t *testing.T) {
	fixture := newEventDispatchFixture(t, "dispatcher-create-race")
	executor := &recordingEventExecutor{
		run: func(_ context.Context, req RunRequest) (*RunResult, error) {
			createAndLinkEventTestRun(t, req, fixture.runStore, &Run{
				ID:          req.RunID,
				WorkflowRef: req.WorkflowRef,
				Status:      RunStatusSucceeded,
			})
			return nil, ErrRunAlreadyExists
		},
	}

	processed, err := fixture.dispatcher(executor).ProcessOne(context.Background())
	if !processed || err != nil {
		t.Fatalf("ProcessOne() = %v, %v, want successful reconciliation", processed, err)
	}
	if finished := fixture.getDispatch(t); finished.Status != eventing.DispatchSucceeded {
		t.Fatalf("finished dispatch = %#v", finished)
	}
}

func TestEventWorkflowDispatcherRejectsTerminalResultWithoutDurableRun(t *testing.T) {
	fixture := newEventDispatchFixture(t, "dispatcher-nondurable-result")
	executor := &recordingEventExecutor{
		run: func(_ context.Context, _ RunRequest) (*RunResult, error) {
			return &RunResult{
				RunID:  "wr_wrong",
				Status: RunStatusSucceeded,
			}, nil
		},
	}

	processed, err := fixture.dispatcher(executor).ProcessOne(context.Background())
	if !processed || err == nil || !strings.Contains(err.Error(), "without durable run") {
		t.Fatalf("ProcessOne() = %v, %v, want non-durable result failure", processed, err)
	}
	finished := fixture.getDispatch(t)
	if finished.Status != eventing.DispatchFailed ||
		!strings.Contains(finished.LastError, "without durable run") {
		t.Fatalf("finished dispatch = %#v, want failed", finished)
	}
}

func TestEventWorkflowDispatcherRetriesMissingWorkflowBeforeExecutor(t *testing.T) {
	fixture := newEventDispatchFixture(t, "dispatcher-missing-workflow")
	if err := os.Remove(filepath.Join(fixture.workspace, fixture.dispatch.WorkflowRef)); err != nil {
		t.Fatalf("Remove() workflow error = %v", err)
	}
	executor := &recordingEventExecutor{
		run: func(_ context.Context, _ RunRequest) (*RunResult, error) {
			t.Fatal("executor called with missing workflow")
			return nil, nil
		},
	}

	processed, err := fixture.dispatcher(executor).ProcessOne(context.Background())
	if !processed || err == nil || !strings.Contains(err.Error(), "load event workflow") {
		t.Fatalf("ProcessOne() = %v, %v, want load retry", processed, err)
	}
	if len(executor.Requests()) != 0 {
		t.Fatalf("executor request count = %d, want 0", len(executor.Requests()))
	}
	pending := fixture.getDispatch(t)
	if pending.Status != eventing.DispatchPending ||
		pending.LinkedAt != nil ||
		!strings.Contains(pending.LastError, "load event workflow") {
		t.Fatalf("pending dispatch = %#v", pending)
	}
}

func TestEventWorkflowDispatcherInitialRenewalUsesBoundedAttemptBudget(t *testing.T) {
	fixture := newEventDispatchFixture(t, "dispatcher-initial-renew-budget")
	inbox := &alwaysFailRenewEventInbox{Store: fixture.store}
	executor := &recordingEventExecutor{
		run: func(_ context.Context, _ RunRequest) (*RunResult, error) {
			t.Fatal("executor called when initial lease renewal failed")
			return nil, nil
		},
	}
	dispatcher := fixture.dispatcher(executor)
	dispatcher.Inbox = inbox
	dispatcher.MaxAttempts = 2

	processed, err := dispatcher.ProcessOne(context.Background())
	if !processed || !errors.Is(err, errEventTestRenewLease) {
		t.Fatalf("first ProcessOne() = %v, %v, want renewal failure", processed, err)
	}
	pending := fixture.getDispatch(t)
	if pending.Status != eventing.DispatchPending || pending.Attempts != 1 {
		t.Fatalf("dispatch after first renewal failure = %#v", pending)
	}

	fixture.clock.Advance(time.Second)
	processed, err = dispatcher.ProcessOne(context.Background())
	if !processed || !errors.Is(err, errEventTestRenewLease) {
		t.Fatalf("second ProcessOne() = %v, %v, want renewal failure", processed, err)
	}
	dead := fixture.getDispatch(t)
	if dead.Status != eventing.DispatchDead || dead.Attempts != 2 {
		t.Fatalf("dispatch after exhausted renewal failures = %#v", dead)
	}
}

func TestEventWorkflowDispatcherReconcileRenewalRetriesBeforeMutation(t *testing.T) {
	fixture := newEventDispatchFixture(t, "dispatcher-reconcile-renew-retry")
	createEventTestRun(t, fixture.runStore, &Run{
		ID:          fixture.dispatch.RunID,
		WorkflowRef: fixture.dispatch.WorkflowRef,
		Status:      RunStatusSucceeded,
	})
	inbox := &failNthRenewEventInbox{
		Store:  fixture.store,
		failAt: 2,
	}
	executor := &recordingEventExecutor{
		run: func(_ context.Context, _ RunRequest) (*RunResult, error) {
			t.Fatal("executor called for existing successful run")
			return nil, nil
		},
	}
	dispatcher := fixture.dispatcher(executor)
	dispatcher.Inbox = inbox

	processed, err := dispatcher.ProcessOne(context.Background())
	if !processed || !errors.Is(err, errEventTestRenewLease) {
		t.Fatalf("first ProcessOne() = %v, %v, want reconcile renewal failure", processed, err)
	}
	pending := fixture.getDispatch(t)
	if pending.Status != eventing.DispatchPending ||
		pending.Attempts != 1 ||
		!strings.Contains(pending.LastError, "before run reconciliation") {
		t.Fatalf("dispatch after reconcile renewal failure = %#v", pending)
	}

	fixture.clock.Advance(time.Second)
	processed, err = dispatcher.ProcessOne(context.Background())
	if !processed || err != nil {
		t.Fatalf("second ProcessOne() = %v, %v, want successful retry", processed, err)
	}
	if finished := fixture.getDispatch(t); finished.Status != eventing.DispatchSucceeded {
		t.Fatalf("finished dispatch = %#v, want succeeded", finished)
	}
}

func TestEventWorkflowDispatcherCancelsRunWhenLeaseRenewalFails(t *testing.T) {
	fixture := newEventDispatchFixture(t, "dispatcher-heartbeat")
	executorStarted := make(chan struct{})
	renewed := make(chan struct{})
	inbox := &renewFailureEventInbox{
		Store:     fixture.store,
		failAfter: executorStarted,
		renewed:   renewed,
	}
	canceled := make(chan error, 1)
	executor := &recordingEventExecutor{
		run: func(ctx context.Context, req RunRequest) (*RunResult, error) {
			close(executorStarted)
			<-ctx.Done()
			canceled <- ctx.Err()
			return &RunResult{
				RunID:  req.RunID,
				Status: RunStatusCanceled,
				Error:  ctx.Err().Error(),
			}, ctx.Err()
		},
	}
	dispatcher := fixture.dispatcher(executor)
	dispatcher.Inbox = inbox
	dispatcher.LeaseDuration = 30 * time.Millisecond

	processDone := make(chan error, 1)
	go func() {
		_, err := dispatcher.ProcessOne(context.Background())
		processDone <- err
	}()
	select {
	case <-executorStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher did not start the executor")
	}
	select {
	case <-renewed:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher did not attempt to renew its lease")
	}
	select {
	case err := <-canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("executor context error = %v, want canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("executor was not canceled after lease renewal failed")
	}
	select {
	case err := <-processDone:
		if !errors.Is(err, errEventTestRenewLease) {
			t.Fatalf("ProcessOne() error = %v, want renewal failure", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ProcessOne() did not return after lease renewal failed")
	}
}

func TestEventWorkflowDispatcherReportsNoWork(t *testing.T) {
	clock := newEventTestClock()
	store := openEventTestStore(t, clock)
	executor := &recordingEventExecutor{}
	dispatcher := &EventWorkflowDispatcher{
		Inbox:        store,
		Executor:     executor,
		RunStore:     NewFileRunStore(t.TempDir()),
		WorkspaceDir: t.TempDir(),
	}
	processed, err := dispatcher.ProcessOne(context.Background())
	if err != nil || processed {
		t.Fatalf("ProcessOne() = %v, %v, want false, nil", processed, err)
	}
}

type eventTestClock struct {
	mu  sync.Mutex
	now time.Time
}

type cancelResultEventRunStore struct {
	RunStore
	result *Run
	err    error
}

type blockingFirstGetRunStore struct {
	RunStore
	once        sync.Once
	entered     chan struct{}
	release     chan struct{}
	mu          sync.Mutex
	cancelCalls int
}

func (s *blockingFirstGetRunStore) GetRun(_ context.Context, runID string) (*Run, error) {
	block := false
	s.once.Do(func() {
		block = true
		close(s.entered)
	})
	if block {
		<-s.release
	}
	return s.RunStore.GetRun(context.Background(), runID)
}

func (s *blockingFirstGetRunStore) CancelRun(
	ctx context.Context,
	runID string,
	reason string,
) (*Run, error) {
	s.mu.Lock()
	s.cancelCalls++
	s.mu.Unlock()
	return s.RunStore.CancelRun(ctx, runID, reason)
}

func (s *blockingFirstGetRunStore) CancelCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelCalls
}

func (s *cancelResultEventRunStore) CancelRun(
	context.Context,
	string,
	string,
) (*Run, error) {
	if s.result == nil {
		return nil, s.err
	}
	resultCopy := *s.result
	return &resultCopy, s.err
}

func newEventTestClock() *eventTestClock {
	return &eventTestClock{
		now: time.Date(2026, 7, 28, 17, 0, 0, 0, time.UTC),
	}
}

func (c *eventTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *eventTestClock) Advance(delta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(delta)
}

func openEventTestStore(t *testing.T, clock *eventTestClock) *eventing.Store {
	t.Helper()
	store, err := eventing.Open(
		context.Background(),
		filepath.Join(t.TempDir(), "eventing", "events.db"),
		eventing.WithClock(clock.Now),
	)
	if err != nil {
		t.Fatalf("eventing.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Store.Close() error = %v", err)
		}
	})
	return store
}

func insertEventTestEnvelope(
	t *testing.T,
	store *eventing.Store,
	dedupeKey string,
) eventing.StoredEvent {
	t.Helper()
	result, err := store.Insert(context.Background(), eventing.Envelope{
		Source:    "github",
		Connector: "primary",
		Type:      "pull_request.opened",
		DedupeKey: dedupeKey,
		Payload:   json.RawMessage(`{"action":"opened","number":42}`),
		Attributes: map[string]string{
			"installation": "production",
		},
		Actor: &eventing.Actor{
			ID:   "dependabot[bot]",
			Type: "bot",
		},
		Subject: &eventing.Subject{
			ID:   "repository-1",
			Type: "repository",
		},
	})
	if err != nil {
		t.Fatalf("Insert() error = %v", err)
	}
	if !result.Inserted {
		t.Fatalf("Insert() inserted = false for %q", dedupeKey)
	}
	return result.Event
}

func writeEventTestWorkflow(t *testing.T, workspace, name, eventType string) string {
	t.Helper()
	path := filepath.Join(workspace, "workflows", filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	contents := `name: Event test
on:
  event:
    types: ` + strconv.Quote(eventType) + `
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: tool/message
`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return "workflows/" + filepath.ToSlash(name)
}

func newEventTestRouter(
	inbox EventRoutingInbox,
	workspace string,
	clock *eventTestClock,
) *EventWorkflowRouter {
	return &EventWorkflowRouter{
		Inbox:         inbox,
		WorkspaceDir:  workspace,
		LeaseDuration: time.Minute,
		MaxAttempts:   4,
		RetryBase:     time.Second,
		RetryMax:      time.Second,
		Now:           clock.Now,
	}
}

type createFailureEventInbox struct {
	*eventing.Store
	mu         sync.Mutex
	callCount  int
	failOnCall int
	alwaysFail bool
}

func (i *createFailureEventInbox) CreateRevisionedDispatchForRoutingClaim(
	ctx context.Context,
	eventID, leaseToken, workflowRef, workflowRevision string,
) (eventing.Dispatch, bool, error) {
	i.mu.Lock()
	i.callCount++
	call := i.callCount
	fail := i.alwaysFail || call == i.failOnCall
	i.mu.Unlock()
	if fail {
		return eventing.Dispatch{}, false, errEventTestCreateDispatch
	}
	return i.Store.CreateRevisionedDispatchForRoutingClaim(
		ctx,
		eventID,
		leaseToken,
		workflowRef,
		workflowRevision,
	)
}

type routingRenewFailureEventInbox struct {
	*eventing.Store
}

func (*routingRenewFailureEventInbox) RenewRoutingLease(
	context.Context,
	string,
	string,
	time.Duration,
) error {
	return errEventTestRenewRouting
}

type recordingEventExecutor struct {
	mu       sync.Mutex
	requests []RunRequest
	run      func(context.Context, RunRequest) (*RunResult, error)
}

func (e *recordingEventExecutor) Run(
	ctx context.Context,
	request RunRequest,
) (*RunResult, error) {
	e.mu.Lock()
	e.requests = append(e.requests, request)
	run := e.run
	e.mu.Unlock()
	if run == nil {
		return nil, nil
	}
	return run(ctx, request)
}

func (e *recordingEventExecutor) Requests() []RunRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]RunRequest(nil), e.requests...)
}

func (e *recordingEventExecutor) SetRun(
	run func(context.Context, RunRequest) (*RunResult, error),
) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.run = run
}

type renewFailureEventInbox struct {
	*eventing.Store
	once      sync.Once
	failAfter <-chan struct{}
	renewed   chan struct{}
}

type alwaysFailRenewEventInbox struct {
	*eventing.Store
}

func (*alwaysFailRenewEventInbox) RenewDispatchLease(
	context.Context,
	string,
	string,
	time.Duration,
) error {
	return errEventTestRenewLease
}

type failNthRenewEventInbox struct {
	*eventing.Store
	mu     sync.Mutex
	calls  int
	failAt int
}

func (i *failNthRenewEventInbox) RenewDispatchLease(
	ctx context.Context,
	id string,
	leaseToken string,
	lease time.Duration,
) error {
	i.mu.Lock()
	i.calls++
	call := i.calls
	i.mu.Unlock()
	if call == i.failAt {
		return errEventTestRenewLease
	}
	return i.Store.RenewDispatchLease(ctx, id, leaseToken, lease)
}

func (i *renewFailureEventInbox) RenewDispatchLease(
	ctx context.Context,
	id string,
	leaseToken string,
	lease time.Duration,
) error {
	select {
	case <-i.failAfter:
		i.once.Do(func() {
			close(i.renewed)
		})
		return errEventTestRenewLease
	default:
		return i.Store.RenewDispatchLease(ctx, id, leaseToken, lease)
	}
}

type advanceOnLinkEventInbox struct {
	*eventing.Store
	clock   *eventTestClock
	advance time.Duration
}

func (i *advanceOnLinkEventInbox) LinkDispatchRun(
	ctx context.Context,
	id string,
	leaseToken string,
	runID string,
) error {
	i.clock.Advance(i.advance)
	return i.Store.LinkDispatchRun(ctx, id, leaseToken, runID)
}

type linkFailureEventInbox struct {
	*eventing.Store
}

func (*linkFailureEventInbox) LinkDispatchRun(
	context.Context,
	string,
	string,
	string,
) error {
	return errEventTestLinkDispatch
}

type eventDispatchFixture struct {
	store     *eventing.Store
	clock     *eventTestClock
	workspace string
	event     eventing.StoredEvent
	dispatch  eventing.Dispatch
	runStore  *FileRunStore
}

func newEventDispatchFixture(t *testing.T, key string) *eventDispatchFixture {
	t.Helper()
	clock := newEventTestClock()
	store := openEventTestStore(t, clock)
	workspace := t.TempDir()
	ref := writeEventTestWorkflow(t, workspace, "dispatch.yml", "*")
	event := insertEventTestEnvelope(t, store, key)
	dispatch, created, err := store.CreateDispatch(
		context.Background(),
		event.Envelope.ID,
		ref,
	)
	if err != nil {
		t.Fatalf("CreateDispatch() error = %v", err)
	}
	if !created {
		t.Fatal("CreateDispatch() created = false")
	}
	return &eventDispatchFixture{
		store:     store,
		clock:     clock,
		workspace: workspace,
		event:     event,
		dispatch:  dispatch,
		runStore:  NewFileRunStore(workspace),
	}
}

func (f *eventDispatchFixture) dispatcher(
	executor EventWorkflowExecutor,
) *EventWorkflowDispatcher {
	return &EventWorkflowDispatcher{
		Inbox:         f.store,
		Executor:      executor,
		RunStore:      f.runStore,
		WorkspaceDir:  f.workspace,
		LeaseDuration: time.Minute,
		MaxAttempts:   4,
		RetryBase:     time.Second,
		RetryMax:      time.Second,
		Now:           f.clock.Now,
	}
}

func (f *eventDispatchFixture) getDispatch(t *testing.T) eventing.Dispatch {
	t.Helper()
	dispatch, err := f.store.GetDispatch(context.Background(), f.dispatch.ID)
	if err != nil {
		t.Fatalf("GetDispatch() error = %v", err)
	}
	return dispatch
}

func createEventTestRun(t *testing.T, store RunStore, run *Run) {
	t.Helper()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Date(2026, 7, 28, 17, 0, 0, 0, time.UTC)
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = run.CreatedAt
	}
	if err := store.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
}

func createAndLinkEventTestRun(
	t *testing.T,
	request RunRequest,
	store RunStore,
	run *Run,
) {
	t.Helper()
	createEventTestRun(t, store, run)
	if request.OnRunPersisted == nil {
		t.Fatal("RunRequest.OnRunPersisted is nil")
	}
	if err := request.OnRunPersisted(run); err != nil {
		t.Fatalf("RunRequest.OnRunPersisted() error = %v", err)
	}
}

var (
	_ EventRoutingInbox             = (*createFailureEventInbox)(nil)
	_ EventDispatchInbox            = (*renewFailureEventInbox)(nil)
	_ eventing.DispatchLeaseRenewer = (*renewFailureEventInbox)(nil)
	_ EventWorkflowExecutor         = (*recordingEventExecutor)(nil)
	_ RunStore                      = (*FileRunStore)(nil)
)
