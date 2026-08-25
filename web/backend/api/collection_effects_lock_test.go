package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/routing"
)

func TestAgentMutationReleasesConfigLockBeforeEffects(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentAPITestHarness(t, nil)
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatal(err)
	}

	saveEntered, allowSaveReturn := blockEffectsTestSave(t, harness.handler)
	request := newEffectsTestJSONRequest(t, http.MethodPost, "/api/agents", agentMutationRequest{
		ExpectedConfigRevision: &revision,
		Agent:                  &agentResource{ID: "lock-order-worker"},
	})
	requireEffectsResponseReleasesMutationLocks(
		t,
		harness.handler,
		harness.mux,
		request,
		saveEntered,
		allowSaveReturn,
		nil,
		http.StatusCreated,
	)
}

func TestModelCollectionMutationReleasesConfigLockBeforeEffects(t *testing.T) {
	resetGatewayTestState(t)
	configPath := modelAliasAPIConfig(t)
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	revision, err := config.ConfigRevision(configPath)
	if err != nil {
		t.Fatal(err)
	}

	saveEntered, allowSaveReturn := blockEffectsTestSave(t, handler)
	request := newEffectsTestJSONRequest(t, http.MethodPost, "/api/model-aliases", map[string]any{
		"expected_config_revision": revision,
		"model_alias": config.ModelAliasConfig{
			Name: "lock-order-alias", Model: "gpt-5.4",
		},
	})
	requireEffectsResponseReleasesMutationLocks(
		t,
		handler,
		mux,
		request,
		saveEntered,
		allowSaveReturn,
		nil,
		http.StatusCreated,
	)
}

func TestMCPMutationReleasesConfigAndMCPLocksBeforeEffects(t *testing.T) {
	resetGatewayTestState(t)
	harness := newMCPAPITestHarness(t, nil)

	originalSave := mcpSaveConfigIfRevision
	saveEntered := make(chan struct{})
	allowSaveReturn := make(chan struct{})
	mcpSaveConfigIfRevision = func(
		path string,
		cfg *config.Config,
		expectedRevision string,
	) (string, error) {
		revision, err := originalSave(path, cfg, expectedRevision)
		close(saveEntered)
		<-allowSaveReturn
		return revision, err
	}
	t.Cleanup(func() {
		mcpSaveConfigIfRevision = originalSave
	})

	request := newEffectsTestJSONRequest(t, http.MethodPatch, "/api/mcp/settings", map[string]any{
		"enabled": true,
		"discovery": map[string]any{
			"enabled":            true,
			"ttl":                60,
			"max_search_results": 5,
			"use_bm25":           true,
		},
	})
	requireEffectsResponseReleasesMutationLocks(
		t,
		harness.handler,
		harness.mux,
		request,
		saveEntered,
		allowSaveReturn,
		&harness.handler.mcpMu,
		http.StatusOK,
	)
}

func TestAgentCapabilitiesGetReleasesMutationLocksBeforeEffects(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentAPITestHarness(t, nil)
	effectsReached, allowEffects := blockAgentCapabilitiesResponseEffects(t)
	path := "/api/agents/" + routing.DefaultAgentID + "/capabilities"
	request := httptest.NewRequest(
		http.MethodGet,
		path,
		nil,
	)
	requireEffectsResponseReleasesMutationLocks(
		t,
		harness.handler,
		harness.mux,
		request,
		effectsReached,
		allowEffects,
		&agentCapabilitiesMutationMu,
		http.StatusOK,
	)
}

func TestAgentCapabilitiesNoopPatchReleasesMutationLocksBeforeEffects(t *testing.T) {
	resetGatewayTestState(t)
	harness := newAgentAPITestHarness(t, nil)
	path := "/api/agents/" + routing.DefaultAgentID + "/capabilities"

	current := httptest.NewRecorder()
	harness.mux.ServeHTTP(
		current,
		httptest.NewRequest(http.MethodGet, path, nil),
	)
	if current.Code != http.StatusOK {
		t.Fatalf("capabilities GET status = %d; body=%s", current.Code, current.Body.String())
	}
	var snapshot agentCapabilitiesResponse
	if err := json.Unmarshal(current.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}

	effectsReached, allowEffects := blockAgentCapabilitiesResponseEffects(t)
	values := []string{}
	request := newEffectsTestJSONRequest(
		t,
		http.MethodPatch,
		path,
		agentCapabilitiesPatchRequest{
			ExpectedRevision: snapshot.Revision,
			Tools: &agentCapabilityPolicyRequest{
				Mode:   capabilityModeAll,
				Values: &values,
			},
		},
	)
	requireEffectsResponseReleasesMutationLocks(
		t,
		harness.handler,
		harness.mux,
		request,
		effectsReached,
		allowEffects,
		&agentCapabilitiesMutationMu,
		http.StatusOK,
	)
}

func blockAgentCapabilitiesResponseEffects(
	t *testing.T,
) (<-chan struct{}, chan struct{}) {
	t.Helper()
	originalHook := agentCapabilitiesBeforeResponseEffects
	effectsReached := make(chan struct{})
	allowEffects := make(chan struct{})
	agentCapabilitiesBeforeResponseEffects = func() {
		close(effectsReached)
		<-allowEffects
	}
	t.Cleanup(func() {
		agentCapabilitiesBeforeResponseEffects = originalHook
	})
	return effectsReached, allowEffects
}

func blockEffectsTestSave(
	t *testing.T,
	handler *Handler,
) (<-chan struct{}, chan struct{}) {
	t.Helper()
	originalSave := handler.saveConfigIfRevision
	saveEntered := make(chan struct{})
	allowSaveReturn := make(chan struct{})
	handler.saveConfigIfRevision = func(
		path string,
		cfg *config.Config,
		expectedRevision string,
	) (string, error) {
		revision, err := originalSave(path, cfg, expectedRevision)
		close(saveEntered)
		<-allowSaveReturn
		return revision, err
	}
	return saveEntered, allowSaveReturn
}

func newEffectsTestJSONRequest(
	t *testing.T,
	method string,
	path string,
	payload any,
) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

// requireEffectsResponseReleasesMutationLocks recreates the gateway-start side
// of the lock order by holding gateway.mu while a handler reaches its final
// mutation barrier. The handler must release every mutation lock before it
// waits for gateway.mu to compute the response effects.
func requireEffectsResponseReleasesMutationLocks(
	t *testing.T,
	handler *Handler,
	mux *http.ServeMux,
	request *http.Request,
	mutationReached <-chan struct{},
	allowMutationReturn chan struct{},
	extraMutationLock *sync.Mutex,
	wantStatus int,
) {
	t.Helper()

	gateway.mu.Lock()
	gatewayLocked := true
	defer func() {
		if gatewayLocked {
			gateway.mu.Unlock()
		}
	}()

	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		response <- recorder
	}()

	select {
	case <-mutationReached:
	case <-time.After(2 * time.Second):
		close(allowMutationReturn)
		gateway.mu.Unlock()
		gatewayLocked = false
		t.Fatal("mutation did not reach the effects barrier")
	}

	configWasHeld := !handler.configMutationMu.TryLock()
	if !configWasHeld {
		handler.configMutationMu.Unlock()
	}
	extraWasHeld := true
	if extraMutationLock != nil {
		extraWasHeld = !extraMutationLock.TryLock()
		if !extraWasHeld {
			extraMutationLock.Unlock()
		}
	}
	close(allowMutationReturn)

	locksReleased := false
	var earlyResponse *httptest.ResponseRecorder
	deadline := time.NewTimer(2 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()

waitForRelease:
	for !locksReleased {
		if handler.configMutationMu.TryLock() {
			extraAcquired := extraMutationLock == nil || extraMutationLock.TryLock()
			if extraAcquired {
				if extraMutationLock != nil {
					extraMutationLock.Unlock()
				}
				handler.configMutationMu.Unlock()
				locksReleased = true
				break
			}
			handler.configMutationMu.Unlock()
		}
		select {
		case earlyResponse = <-response:
			break waitForRelease
		case <-ticker.C:
		case <-deadline.C:
			break waitForRelease
		}
	}

	if earlyResponse == nil {
		select {
		case earlyResponse = <-response:
		default:
		}
	}
	gateway.mu.Unlock()
	gatewayLocked = false

	recorder := earlyResponse
	if recorder == nil {
		select {
		case recorder = <-response:
		case <-time.After(2 * time.Second):
			t.Fatal("effects response did not resume after gateway lock release")
		}
	}
	if !configWasHeld || !extraWasHeld {
		t.Fatal("effects barrier was reached without all mutation locks held")
	}
	if !locksReleased {
		t.Fatal("mutation locks remained held while the effects response waited for gateway state")
	}
	if earlyResponse != nil {
		t.Fatal("effects response completed while gateway state was locked")
	}
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, wantStatus, recorder.Body.String())
	}
}
