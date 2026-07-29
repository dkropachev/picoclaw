package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/eventing"
	eventoperator "github.com/sipeed/picoclaw/pkg/eventing/operator"
	eventwebhook "github.com/sipeed/picoclaw/pkg/eventing/webhook"
	"github.com/sipeed/picoclaw/pkg/health"
)

func TestEventOperatorRouteUsesProtectedLiveStoreGeneration(t *testing.T) {
	workspace := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspace,
		filepath.Join(workspace, "eventing", "events.db"),
		true,
		false,
	)

	messageBus := bus.NewMessageBus()
	manager, err := channels.NewManager(cfg, messageBus, nil)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	const token = "operator-test-token"
	healthServer := health.NewServer("127.0.0.1", 0, token)
	manager.SetupHTTPServerListeners(
		[]net.Listener{listener},
		listener.Addr().String(),
		healthServer,
	)

	service, err := setupEventAutomationService(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("setupEventAutomationService() error = %v", err)
	}
	runningServices := &services{
		ChannelManager:  manager,
		EventAutomation: service,
		HealthServer:    healthServer,
		authToken:       token,
	}
	if err = prepareEventOperatorRoute(runningServices); err != nil {
		t.Fatalf("prepareEventOperatorRoute() error = %v", err)
	}
	if err = manager.StartAll(context.Background()); err != nil {
		t.Fatalf("StartAll() error = %v", err)
	}
	t.Cleanup(func() {
		drainCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = deactivateEventOperator(drainCtx, runningServices)
		_ = closeEventAutomationService(drainCtx, &runningServices.EventAutomation)
		_ = manager.StopAll(drainCtx)
		messageBus.Close()
	})

	baseURL := "http://" + listener.Addr().String() + eventoperator.RoutePrefix
	client := &http.Client{Timeout: 5 * time.Second}

	status, headers, _ := performEventOperatorRequest(
		t,
		client,
		http.MethodGet,
		baseURL+"events",
		"",
		nil,
	)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", status, http.StatusUnauthorized)
	}
	if headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("unauthenticated Cache-Control = %q, want no-store", headers.Get("Cache-Control"))
	}

	status, _, _ = performEventOperatorRequest(
		t,
		client,
		http.MethodGet,
		baseURL+"events",
		token,
		nil,
	)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("inactive status = %d, want %d", status, http.StatusServiceUnavailable)
	}

	payload := []byte(`{"large":9007199254740993,"safe":"value"}`)
	inserted, err := service.store.Insert(context.Background(), eventing.Envelope{
		Source:    "github",
		Connector: "primary",
		Type:      "issues.opened",
		DedupeKey: "delivery-private",
		Payload:   payload,
	})
	if err != nil {
		t.Fatalf("Insert() error = %v", err)
	}
	if err = activateEventOperator(runningServices); err != nil {
		t.Fatalf("activateEventOperator() error = %v", err)
	}

	status, headers, body := performEventOperatorRequest(
		t,
		client,
		http.MethodGet,
		baseURL+"events?source=github",
		token,
		nil,
	)
	if status != http.StatusOK {
		t.Fatalf("list status = %d, want %d: %s", status, http.StatusOK, body)
	}
	if headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("list Cache-Control = %q, want no-store", headers.Get("Cache-Control"))
	}
	for _, forbidden := range []string{
		"delivery-private",
		"lease_token",
		`"payload"`,
		"9007199254740993",
	} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("list response exposed %q: %s", forbidden, body)
		}
	}
	if !bytes.Contains(body, []byte(inserted.Event.Envelope.ID)) {
		t.Fatalf("list response does not contain event ID: %s", body)
	}

	status, _, body = performEventOperatorRequest(
		t,
		client,
		http.MethodGet,
		baseURL+"events/"+inserted.Event.Envelope.ID,
		token,
		nil,
	)
	if status != http.StatusOK {
		t.Fatalf("detail status = %d, want %d: %s", status, http.StatusOK, body)
	}
	if bytes.Contains(body, []byte("9007199254740993")) ||
		bytes.Contains(body, []byte("delivery-private")) {
		t.Fatalf("detail response exposed payload or dedupe key: %s", body)
	}

	status, headers, body = performEventOperatorRequest(
		t,
		client,
		http.MethodGet,
		baseURL+"events/"+inserted.Event.Envelope.ID+"/payload",
		token,
		nil,
	)
	if status != http.StatusOK {
		t.Fatalf("payload status = %d, want %d: %s", status, http.StatusOK, body)
	}
	if headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("payload Cache-Control = %q, want no-store", headers.Get("Cache-Control"))
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("payload body = %s, want exact %s", body, payload)
	}

	status, _, _ = performEventOperatorRequest(
		t,
		client,
		http.MethodPost,
		baseURL+"events/"+inserted.Event.Envelope.ID+"/replay",
		"",
		strings.NewReader("{}"),
	)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated replay status = %d, want %d", status, http.StatusUnauthorized)
	}
	status, _, _ = performEventOperatorRequest(
		t,
		client,
		http.MethodPost,
		baseURL+"events/"+inserted.Event.Envelope.ID+"/replay",
		token,
		strings.NewReader(`{"force":true}`),
	)
	if status != http.StatusBadRequest {
		t.Fatalf("malformed replay status = %d, want %d", status, http.StatusBadRequest)
	}
	page, err := service.store.List(context.Background(), eventing.EventFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List(after rejected replay) error = %v", err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("rejected replay created an event: count = %d, want 1", len(page.Events))
	}

	status, headers, body = performEventOperatorRequest(
		t,
		client,
		http.MethodPost,
		baseURL+"events/"+inserted.Event.Envelope.ID+"/replay",
		token,
		strings.NewReader("{}"),
	)
	if status != http.StatusCreated {
		t.Fatalf("replay status = %d, want %d: %s", status, http.StatusCreated, body)
	}
	var replayResponse struct {
		Event struct {
			ID       string `json:"id"`
			ReplayOf string `json:"replay_of"`
		} `json:"event"`
	}
	if err = json.Unmarshal(body, &replayResponse); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if replayResponse.Event.ReplayOf != inserted.Event.Envelope.ID ||
		headers.Get("Location") != eventoperator.RoutePrefix+"events/"+replayResponse.Event.ID {
		t.Fatalf(
			"replay response = %#v, Location = %q",
			replayResponse,
			headers.Get("Location"),
		)
	}
	replayed, err := service.store.Get(context.Background(), replayResponse.Event.ID)
	if err != nil {
		t.Fatalf("Get(replay) error = %v", err)
	}
	if replayed.Envelope.ReplayOf != inserted.Event.Envelope.ID ||
		replayed.Routing.Status != eventing.RoutingPending {
		t.Fatalf("stored replay = %#v", replayed)
	}

	if err = deactivateEventOperator(context.Background(), runningServices); err != nil {
		t.Fatalf("deactivateEventOperator() error = %v", err)
	}
	if err = closeEventAutomationService(
		context.Background(),
		&runningServices.EventAutomation,
	); err != nil {
		t.Fatalf("closeEventAutomationService() error = %v", err)
	}
	if err = activateEventOperator(runningServices); err != nil {
		t.Fatalf("activate disabled event operator error = %v", err)
	}
	status, _, _ = performEventOperatorRequest(
		t,
		client,
		http.MethodGet,
		baseURL+"events",
		token,
		nil,
	)
	if status != http.StatusNotFound {
		t.Fatalf("disabled route status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestPrepareEventOperatorRouteDisabledIsInert(t *testing.T) {
	runningServices := &services{}
	if err := prepareEventOperatorRouteForConfig(runningServices, nil); err != nil {
		t.Fatalf("prepareEventOperatorRouteForConfig(nil) error = %v", err)
	}
	if runningServices.eventOperatorController != nil ||
		runningServices.eventOperatorRelease != nil {
		t.Fatal("disabled event operator initialized an HTTP route")
	}
}

func TestEnsureEventOperatorRouteRequiresMatchingProtectedRuntime(t *testing.T) {
	for _, test := range []struct {
		name         string
		serviceToken string
		healthToken  string
	}{
		{name: "health auth disabled", serviceToken: "expected"},
		{name: "token mismatch", serviceToken: "expected", healthToken: "different"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runningServices := &services{
				ChannelManager: &channels.Manager{},
				HealthServer:   health.NewServer("127.0.0.1", 0, test.healthToken),
				authToken:      test.serviceToken,
			}
			err := ensureEventOperatorRoute(runningServices)
			if err == nil || !strings.Contains(err.Error(), "protected gateway runtime") {
				t.Fatalf("ensureEventOperatorRoute() error = %v, want protected runtime failure", err)
			}
			if runningServices.eventOperatorRelease != nil {
				t.Fatal("unprotected event operator route was registered")
			}
		})
	}
}

func TestActivateEventHTTPAdmissionsAbortsWebhookStageOnOperatorFailure(t *testing.T) {
	workspace := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspace,
		filepath.Join(workspace, "eventing", "events.db"),
		true,
		false,
	)
	service, err := setupEventAutomationService(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("setupEventAutomationService() error = %v", err)
	}
	operatorController := eventoperator.NewController()
	operatorGeneration, err := operatorController.Activate(service.operatorBackend)
	if err != nil {
		t.Fatalf("activate existing operator generation: %v", err)
	}
	webhookController := eventwebhook.NewController()
	runningServices := &services{
		EventAutomation:         service,
		eventOperatorController: operatorController,
		eventOperatorGeneration: operatorGeneration,
		eventOperatorRelease:    func() {},
		eventWebhookController:  webhookController,
		eventWebhookRelease:     func() {},
	}
	t.Cleanup(func() {
		_ = deactivateEventOperator(context.Background(), runningServices)
		_ = closeEventAutomationService(
			context.Background(),
			&runningServices.EventAutomation,
		)
	})

	err = activateEventHTTPAdmissions(runningServices)
	if !errors.Is(err, eventoperator.ErrActiveGeneration) {
		t.Fatalf("activateEventHTTPAdmissions() error = %v, want active operator failure", err)
	}
	if !operatorController.IsActive(operatorGeneration) {
		t.Fatal("failed recovery disturbed the pre-existing operator generation")
	}
	staged, stageErr := webhookController.Stage(nil)
	if stageErr != nil {
		t.Fatalf("webhook reservation was not aborted after operator failure: %v", stageErr)
	}
	staged.Abort()
}

func installTestEventOperatorGeneration(
	t *testing.T,
	runningServices *services,
) {
	t.Helper()
	if runningServices == nil ||
		runningServices.EventAutomation == nil ||
		runningServices.EventAutomation.operatorBackend == nil {
		t.Fatal("installTestEventOperatorGeneration requires an event backend")
	}
	controller := eventoperator.NewController()
	generation, err := controller.Activate(
		runningServices.EventAutomation.operatorBackend,
	)
	if err != nil {
		t.Fatalf("activate test event operator generation: %v", err)
	}
	runningServices.eventOperatorController = controller
	runningServices.eventOperatorGeneration = generation
	// Reload fixtures exercise lifecycle behavior below the shared listener.
	// A non-nil release models the protected route already mounted by startup.
	runningServices.eventOperatorRelease = func() {}
}

func performEventOperatorRequest(
	t *testing.T,
	client *http.Client,
	method, target, token string,
	body io.Reader,
) (int, http.Header, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if strings.TrimSpace(token) != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if method == http.MethodPost && body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("operator request error = %v", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("ReadAll(response) error = %v", err)
	}
	return response.StatusCode, response.Header.Clone(), responseBody
}
