package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

type reviewAttentionAPITestHarness struct {
	configPath string
	handler    *Handler
	mux        *http.ServeMux
}

func newReviewAttentionAPITestHarness(
	t *testing.T,
	configure func(*config.Config),
) *reviewAttentionAPITestHarness {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	t.Setenv(config.EnvHome, home)
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(root, "workspace")
	if configure != nil {
		configure(cfg)
	}
	configPath := filepath.Join(root, "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	return &reviewAttentionAPITestHarness{
		configPath: configPath,
		handler:    handler,
		mux:        mux,
	}
}

func (h *reviewAttentionAPITestHarness) request(
	t *testing.T,
	method string,
	path string,
	body string,
	headers http.Header,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	return recorder
}

func (h *reviewAttentionAPITestHarness) get(
	t *testing.T,
) (*httptest.ResponseRecorder, reviewAttentionPoliciesResponse) {
	t.Helper()
	recorder := h.request(
		t,
		http.MethodGet,
		reviewAttentionPoliciesPath,
		"",
		nil,
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response reviewAttentionPoliciesResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("GET json.Unmarshal() error = %v", err)
	}
	return recorder, response
}

func (h *reviewAttentionAPITestHarness) put(
	t *testing.T,
	revision string,
	attention config.ReviewAttentionConfig,
) *httptest.ResponseRecorder {
	t.Helper()
	payload := struct {
		ExpectedConfigRevision string `json:"expected_config_revision"`
		config.ReviewAttentionConfig
	}{
		ExpectedConfigRevision: revision,
		ReviewAttentionConfig:  attention,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return h.request(
		t,
		http.MethodPut,
		reviewAttentionPoliciesPath,
		string(encoded),
		http.Header{
			"Content-Type":   {"application/json; charset=utf-8"},
			"Sec-Fetch-Site": {"same-origin"},
		},
	)
}

func reviewAttentionDirectorySnapshot(
	t *testing.T,
	directory string,
) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir(%q) error = %v", directory, err)
	}
	snapshot := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		if readErr != nil {
			t.Fatalf("ReadFile(%q) error = %v", entry.Name(), readErr)
		}
		snapshot[entry.Name()] = data
	}
	return snapshot
}

func completeReviewAttentionPolicy() config.ReviewAttentionConfig {
	return config.ReviewAttentionConfig{
		Global: map[string][]gatetypes.GateSpec{
			"review.submitted": {
				{
					ID:       "working",
					Kind:     gatetypes.GateAIWorkingContext,
					AgentID:  "main",
					Criteria: "Ask when the implementation direction is ambiguous.",
					Title:    "Discuss implementation direction",
				},
				{
					ID:       "isolated",
					Kind:     gatetypes.GateAIIsolatedContext,
					AgentID:  "main",
					Criteria: "Ask when the finding needs product judgment.",
					Title:    "Resolve review finding",
				},
				{
					ID:        "deterministic",
					Kind:      gatetypes.GateDeterministic,
					When:      "true",
					Title:     "Confirm deterministic policy",
					Questions: []any{"Continue?"},
				},
				{ID: "zero", Kind: gatetypes.GateZero},
			},
		},
		Repositories: map[string]map[string]gatetypes.RepositoryGatePolicy{
			"Acme/Widgets": {
				"review.submitted": {
					Mode: gatetypes.GatePolicyOverlay,
					Gates: []gatetypes.GateSpec{
						{ID: "isolated", Kind: gatetypes.GateZero},
						{
							ID:    "repository_rule",
							Kind:  gatetypes.GateDeterministic,
							When:  "false",
							Title: "Repository rule",
							Questions: map[string]any{
								"prompt": "Escalate?",
								"Foo":    "one",
								"foo":    "two",
							},
						},
					},
				},
			},
		},
	}
}

func decodeReviewAttentionResponse(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
) reviewAttentionPoliciesResponse {
	t.Helper()
	var response reviewAttentionPoliciesResponse
	decoder := json.NewDecoder(strings.NewReader(recorder.Body.String()))
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil {
		t.Fatalf("response decode error = %v; body=%s", err, recorder.Body.String())
	}
	return response
}

func decodeReviewAttentionError(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
) string {
	t.Helper()
	var response struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("error response decode = %v; body=%s", err, recorder.Body.String())
	}
	return response.Error
}

func injectConcurrentAliasBeforeReviewAttentionCAS(
	h *Handler,
) func() bool {
	originalSave := h.saveReviewAttention
	injected := false
	h.saveReviewAttention = func(
		path string,
		attention config.ReviewAttentionConfig,
		expectedRevision string,
	) (string, error) {
		if !injected {
			current, revision, err := config.LoadConfigForUpdateSnapshot(path)
			if err != nil {
				return "", err
			}
			current.ModelAliases = append(
				current.ModelAliases,
				config.ModelAliasConfig{
					Name:  concurrentWriterAlias,
					Model: "openai/gpt-5.4",
				},
			)
			if _, err := config.SaveConfigIfRevision(
				path,
				current,
				revision,
			); err != nil {
				return "", err
			}
			injected = true
		}
		return originalSave(path, attention, expectedRevision)
	}
	return func() bool { return injected }
}

func TestReviewAttentionPoliciesGetAndPutFullReplacement(t *testing.T) {
	resetGatewayTestState(t)
	old := config.ReviewAttentionConfig{
		Global: map[string][]gatetypes.GateSpec{
			"review.old": {{ID: "old", Kind: gatetypes.GateZero}},
		},
		Repositories: map[string]map[string]gatetypes.RepositoryGatePolicy{
			"Old/Repository": {
				"review.old": {Mode: gatetypes.GatePolicyDisable},
			},
		},
	}
	harness := newReviewAttentionAPITestHarness(t, func(cfg *config.Config) {
		cfg.Gateway.Port = 23456
		cfg.Agents.List = []config.AgentConfig{{
			ID:      "main",
			Name:    "unrelated-agent-sentinel",
			Default: true,
		}}
		cfg.Reviews.Attention = old
	})
	before, err := os.ReadFile(harness.configPath)
	if err != nil {
		t.Fatalf("ReadFile(before) error = %v", err)
	}

	getRecorder, getResponse := harness.get(t)
	if !reflect.DeepEqual(getResponse.ReviewAttentionConfig, old) {
		t.Fatalf("GET policies = %#v, want %#v", getResponse.ReviewAttentionConfig, old)
	}
	if !strings.HasPrefix(getResponse.CatalogRevision, "sha256:") ||
		getResponse.ConfigRevision == "" ||
		getResponse.Effects.GatewayEffect != "applied" {
		t.Fatalf("GET metadata = %#v", getResponse)
	}
	if strings.Contains(getRecorder.Body.String(), "unrelated-agent-sentinel") {
		t.Fatalf("GET leaked unrelated config: %s", getRecorder.Body.String())
	}
	if getRecorder.Header().Get("Cache-Control") != "no-store" ||
		getRecorder.Header().Get("X-Content-Type-Options") != "nosniff" ||
		getRecorder.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("GET headers = %#v", getRecorder.Header())
	}
	afterGet, err := os.ReadFile(harness.configPath)
	if err != nil || string(afterGet) != string(before) {
		t.Fatalf("GET changed config: err=%v", err)
	}

	next := completeReviewAttentionPolicy()
	putRecorder := harness.put(t, getResponse.ConfigRevision, next)
	if putRecorder.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", putRecorder.Code, putRecorder.Body.String())
	}
	putResponse := decodeReviewAttentionResponse(t, putRecorder)
	if putResponse.ConfigRevision == getResponse.ConfigRevision ||
		putResponse.CatalogRevision == getResponse.CatalogRevision ||
		!reflect.DeepEqual(putResponse.ReviewAttentionConfig, next) {
		t.Fatalf("PUT response = %#v", putResponse)
	}
	saved, _, err := config.LoadCurrentConfigSnapshot(harness.configPath)
	if err != nil {
		t.Fatalf("LoadCurrentConfigSnapshot() error = %v", err)
	}
	if saved.Gateway.Port != 23456 || saved.Agents.List[0].Name != "unrelated-agent-sentinel" {
		t.Fatalf("PUT changed unrelated config: %#v", saved)
	}
	if _, exists := saved.Reviews.Attention.Global["review.old"]; exists {
		t.Fatal("PUT merged instead of fully replacing global policies")
	}
	if _, exists := saved.Reviews.Attention.Repositories["Old/Repository"]; exists {
		t.Fatal("PUT merged instead of fully replacing repository policies")
	}
	if len(saved.Reviews.Attention.Global["review.submitted"]) != 4 {
		t.Fatalf("saved policies = %#v", saved.Reviews.Attention)
	}
	repositoryPolicy := saved.Reviews.Attention.Repositories["Acme/Widgets"]["review.submitted"]
	questions, ok := repositoryPolicy.Gates[1].Questions.(map[string]any)
	if !ok || questions["Foo"] != "one" || questions["foo"] != "two" {
		t.Fatalf("saved case-sensitive questions = %#v", questions)
	}
	_, roundTrip := harness.get(t)
	if roundTrip.ConfigRevision != putResponse.ConfigRevision ||
		roundTrip.CatalogRevision != putResponse.CatalogRevision ||
		!reflect.DeepEqual(roundTrip.ReviewAttentionConfig, putResponse.ReviewAttentionConfig) {
		t.Fatalf("GET after PUT changed the policy generation: %#v", roundTrip)
	}
}

func TestReviewAttentionPolicySavePreservesPersistedShapeAndSecurity(t *testing.T) {
	resetGatewayTestState(t)
	harness := newReviewAttentionAPITestHarness(t, func(cfg *config.Config) {
		cfg.Gateway.Port = 23456
	})
	raw, err := os.ReadFile(harness.configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var root map[string]json.RawMessage
	if err = json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("json.Unmarshal(config) error = %v", err)
	}
	var agents map[string]json.RawMessage
	if err = json.Unmarshal(root["agents"], &agents); err != nil {
		t.Fatalf("json.Unmarshal(agents) error = %v", err)
	}
	var defaults map[string]json.RawMessage
	if err = json.Unmarshal(agents["defaults"], &defaults); err != nil {
		t.Fatalf("json.Unmarshal(agent defaults) error = %v", err)
	}
	delete(defaults, "workspace")
	agents["defaults"], err = json.Marshal(defaults)
	if err != nil {
		t.Fatalf("json.Marshal(agent defaults) error = %v", err)
	}
	root["agents"], err = json.Marshal(agents)
	if err != nil {
		t.Fatalf("json.Marshal(agents) error = %v", err)
	}
	raw, err = json.MarshalIndent(root, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent(config) error = %v", err)
	}
	if err = os.WriteFile(harness.configPath, raw, 0o600); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	securityPath := filepath.Join(filepath.Dir(harness.configPath), ".security.yml")
	securityBefore, err := os.ReadFile(securityPath)
	if err != nil {
		t.Fatalf("ReadFile(security) error = %v", err)
	}
	t.Setenv("PICOCLAW_GATEWAY_PORT", "34567")

	_, current := harness.get(t)
	recorder := harness.put(
		t,
		current.ConfigRevision,
		completeReviewAttentionPolicy(),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", recorder.Code, recorder.Body.String())
	}

	persisted, err := os.ReadFile(harness.configPath)
	if err != nil {
		t.Fatalf("ReadFile(saved config) error = %v", err)
	}
	var savedRoot map[string]json.RawMessage
	if err = json.Unmarshal(persisted, &savedRoot); err != nil {
		t.Fatalf("json.Unmarshal(saved config) error = %v", err)
	}
	var gateway struct {
		Port int `json:"port"`
	}
	if err = json.Unmarshal(savedRoot["gateway"], &gateway); err != nil {
		t.Fatalf("json.Unmarshal(saved gateway) error = %v", err)
	}
	if gateway.Port != 23456 {
		t.Fatalf("persisted gateway port = %d, want disk value 23456", gateway.Port)
	}
	if err = json.Unmarshal(savedRoot["agents"], &agents); err != nil {
		t.Fatalf("json.Unmarshal(saved agents) error = %v", err)
	}
	if err = json.Unmarshal(agents["defaults"], &defaults); err != nil {
		t.Fatalf("json.Unmarshal(saved defaults) error = %v", err)
	}
	if _, persistedWorkspace := defaults["workspace"]; persistedWorkspace {
		t.Fatal("policy PUT persisted a derived default workspace")
	}
	securityAfter, err := os.ReadFile(securityPath)
	if err != nil {
		t.Fatalf("ReadFile(saved security) error = %v", err)
	}
	if string(securityAfter) != string(securityBefore) {
		t.Fatal("policy PUT rewrote the security sidecar")
	}
}

func TestReviewAttentionPolicySaveCreatesMinimalMissingConfig(t *testing.T) {
	resetGatewayTestState(t)
	harness := newReviewAttentionAPITestHarness(t, nil)
	securityPath := filepath.Join(filepath.Dir(harness.configPath), ".security.yml")
	for _, path := range []string{harness.configPath, securityPath} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatalf("Remove(%q) error = %v", path, err)
		}
	}
	t.Setenv("PICOCLAW_GATEWAY_PORT", "34567")

	_, current := harness.get(t)
	if current.ConfigRevision != "missing" {
		t.Fatalf("missing config revision = %q, want missing", current.ConfigRevision)
	}
	recorder := harness.put(
		t,
		current.ConfigRevision,
		completeReviewAttentionPolicy(),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", recorder.Code, recorder.Body.String())
	}

	raw, err := os.ReadFile(harness.configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("json.Unmarshal(config) error = %v", err)
	}
	if len(document) != 2 {
		t.Fatalf("missing policy save materialized unrelated config: %s", raw)
	}
	if string(document["version"]) != strconv.Itoa(config.CurrentVersion) {
		t.Fatalf("saved version = %s, want %d", document["version"], config.CurrentVersion)
	}
	if _, ok := document["reviews"]; !ok {
		t.Fatalf("saved config has no reviews member: %s", raw)
	}
	if _, err := os.Stat(securityPath); !os.IsNotExist(err) {
		t.Fatalf("missing policy save created security sidecar: %v", err)
	}
}

func TestReviewAttentionPoliciesRejectSecuritySidecarWithoutPublicConfig(
	t *testing.T,
) {
	resetGatewayTestState(t)
	harness := newReviewAttentionAPITestHarness(t, nil)
	securityPath := filepath.Join(filepath.Dir(harness.configPath), ".security.yml")
	securityBefore, err := os.ReadFile(securityPath)
	if err != nil {
		t.Fatalf("ReadFile(security) error = %v", err)
	}
	if err = os.Remove(harness.configPath); err != nil {
		t.Fatalf("Remove(config) error = %v", err)
	}
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatalf("ConfigRevision() error = %v", err)
	}

	for _, recorder := range []*httptest.ResponseRecorder{
		harness.request(t, http.MethodGet, reviewAttentionPoliciesPath, "", nil),
		harness.put(t, revision, completeReviewAttentionPolicy()),
	} {
		if recorder.Code != http.StatusInternalServerError ||
			decodeReviewAttentionError(t, recorder) != reviewAttentionPoliciesUnavailable {
			t.Fatalf("sidecar-only request = %d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	if _, err = os.Stat(harness.configPath); !os.IsNotExist(err) {
		t.Fatalf("sidecar-only request created public config: %v", err)
	}
	securityAfter, err := os.ReadFile(securityPath)
	if err != nil {
		t.Fatalf("ReadFile(security after) error = %v", err)
	}
	if string(securityAfter) != string(securityBefore) {
		t.Fatal("sidecar-only request changed security bytes")
	}
}

func TestReviewAttentionPoliciesRejectLegacyWithoutMutation(t *testing.T) {
	resetGatewayTestState(t)
	harness := newReviewAttentionAPITestHarness(t, nil)
	raw, err := os.ReadFile(harness.configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var root map[string]json.RawMessage
	if err = json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("json.Unmarshal(config) error = %v", err)
	}
	root["version"] = json.RawMessage(strconv.Itoa(config.CurrentVersion - 1))
	legacy, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent(legacy) error = %v", err)
	}
	if err = os.WriteFile(harness.configPath, legacy, 0o600); err != nil {
		t.Fatalf("WriteFile(legacy) error = %v", err)
	}
	revision, err := config.ConfigRevision(harness.configPath)
	if err != nil {
		t.Fatalf("ConfigRevision(legacy) error = %v", err)
	}
	directory := filepath.Dir(harness.configPath)
	before := reviewAttentionDirectorySnapshot(t, directory)

	for _, recorder := range []*httptest.ResponseRecorder{
		harness.request(t, http.MethodGet, reviewAttentionPoliciesPath, "", nil),
		harness.put(t, revision, config.ReviewAttentionConfig{}),
	} {
		if recorder.Code != http.StatusInternalServerError ||
			decodeReviewAttentionError(t, recorder) != reviewAttentionPoliciesUnavailable {
			t.Fatalf("legacy request = %d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	after := reviewAttentionDirectorySnapshot(t, directory)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("legacy policy request mutated config directory: before=%v after=%v", before, after)
	}
}

func TestReviewAttentionPoliciesStrictEnvelopeAndRoutes(t *testing.T) {
	resetGatewayTestState(t)
	harness := newReviewAttentionAPITestHarness(t, nil)
	_, current := harness.get(t)
	validHeaders := http.Header{
		"Content-Type":   {"application/json"},
		"Sec-Fetch-Site": {"same-origin"},
	}
	validBody := `{"expected_config_revision":"` + current.ConfigRevision + `"}`

	tests := []struct {
		name      string
		method    string
		path      string
		body      string
		headers   http.Header
		want      int
		wantError string
		wantAllow string
	}{
		{
			name: "head is unsupported", method: http.MethodHead,
			path: reviewAttentionPoliciesPath, want: http.StatusMethodNotAllowed,
			wantError: "method not allowed", wantAllow: "GET, PUT",
		},
		{
			name: "post is unsupported", method: http.MethodPost,
			path: reviewAttentionPoliciesPath, want: http.StatusMethodNotAllowed,
			wantError: "method not allowed", wantAllow: "GET, PUT",
		},
		{
			name: "query rejected", method: http.MethodGet,
			path: reviewAttentionPoliciesPath + "?extra=true",
			want: http.StatusBadRequest, wantError: reviewAttentionPolicyInvalidRequest,
		},
		{
			name: "top level null", method: http.MethodPut,
			path: reviewAttentionPoliciesPath, body: "null", headers: validHeaders,
			want: http.StatusBadRequest, wantError: reviewAttentionPolicyInvalidRequest,
		},
		{
			name: "explicit global null", method: http.MethodPut,
			path:    reviewAttentionPoliciesPath,
			body:    `{"expected_config_revision":"` + current.ConfigRevision + `","GLOBAL":null}`,
			headers: validHeaders, want: http.StatusBadRequest,
			wantError: reviewAttentionPolicyInvalidRequest,
		},
		{
			name: "explicit repositories null", method: http.MethodPut,
			path:    reviewAttentionPoliciesPath,
			body:    `{"expected_config_revision":"` + current.ConfigRevision + `","repositories":null}`,
			headers: validHeaders, want: http.StatusBadRequest,
			wantError: reviewAttentionPolicyInvalidRequest,
		},
		{
			name: "missing expected revision", method: http.MethodPut,
			path: reviewAttentionPoliciesPath, body: `{}`, headers: validHeaders,
			want:      http.StatusBadRequest,
			wantError: reviewAttentionExpectedRevisionRequired,
		},
		{
			name: "unknown field", method: http.MethodPut,
			path:    reviewAttentionPoliciesPath,
			body:    strings.TrimSuffix(validBody, "}") + `,"surprise":true}`,
			headers: validHeaders, want: http.StatusBadRequest,
			wantError: reviewAttentionPolicyInvalidRequest,
		},
		{
			name: "case folded duplicate field", method: http.MethodPut,
			path:    reviewAttentionPoliciesPath,
			body:    strings.TrimSuffix(validBody, "}") + `,"GLOBAL":{},"global":{}}`,
			headers: validHeaders, want: http.StatusBadRequest,
			wantError: reviewAttentionPolicyInvalidRequest,
		},
		{
			name:   "decision name cannot weaken gate duplicate checks",
			method: http.MethodPut,
			path:   reviewAttentionPoliciesPath,
			body: strings.TrimSuffix(validBody, "}") +
				`,"global":{"questions":[{"id":"first","ID":"second","kind":"zero"}]}}`,
			headers: validHeaders, want: http.StatusBadRequest,
			wantError: reviewAttentionPolicyInvalidRequest,
		},
		{
			name: "unpaired unicode surrogate", method: http.MethodPut,
			path: reviewAttentionPoliciesPath,
			body: strings.TrimSuffix(validBody, "}") +
				`,"global":{"review.surrogate":[{"id":"gate","kind":"ai_isolated_context",` +
				`"agent_id":"main","criteria":"\ud800","title":"Review"}]}}`,
			headers: validHeaders, want: http.StatusBadRequest,
			wantError: reviewAttentionPolicyInvalidRequest,
		},
		{
			name: "trailing JSON", method: http.MethodPut,
			path: reviewAttentionPoliciesPath, body: validBody + `{}`,
			headers: validHeaders, want: http.StatusBadRequest,
			wantError: reviewAttentionPolicyInvalidRequest,
		},
		{
			name: "missing content type", method: http.MethodPut,
			path: reviewAttentionPoliciesPath, body: validBody,
			headers:   http.Header{"Sec-Fetch-Site": {"same-origin"}},
			want:      http.StatusUnsupportedMediaType,
			wantError: reviewAttentionPolicyInvalidRequest,
		},
		{
			name: "non identity encoding", method: http.MethodPut,
			path: reviewAttentionPoliciesPath, body: validBody,
			headers: http.Header{
				"Content-Type": {"application/json"}, "Content-Encoding": {"gzip"},
				"Sec-Fetch-Site": {"same-origin"},
			},
			want:      http.StatusUnsupportedMediaType,
			wantError: reviewAttentionPolicyInvalidRequest,
		},
		{
			name: "origin metadata required", method: http.MethodPut,
			path: reviewAttentionPoliciesPath, body: validBody,
			headers: http.Header{"Content-Type": {"application/json"}},
			want:    http.StatusForbidden, wantError: reviewAttentionPolicyInvalidRequest,
		},
		{
			name: "oversized", method: http.MethodPut,
			path:    reviewAttentionPoliciesPath,
			body:    strings.Repeat(" ", reviewAttentionPolicyRequestMaxBytes+1),
			headers: validHeaders, want: http.StatusRequestEntityTooLarge,
			wantError: reviewAttentionPolicyInvalidRequest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := harness.request(
				t,
				test.method,
				test.path,
				test.body,
				test.headers,
			)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.want, recorder.Body.String())
			}
			if got := decodeReviewAttentionError(t, recorder); got != test.wantError {
				t.Fatalf("error = %q, want %q", got, test.wantError)
			}
			if got := recorder.Header().Get("Allow"); got != test.wantAllow {
				t.Fatalf("Allow = %q, want %q", got, test.wantAllow)
			}
		})
	}
}

func TestReviewAttentionDuplicateScannerPreservesQuestionKeyCase(t *testing.T) {
	raw := []byte(`{"expected_config_revision":"revision","repositories":{"Acme/Widgets":{` +
		`"review.submitted":{"mode":"replace","gates":[{"id":"gate",` +
		`"kind":"deterministic","when":"true","title":"Review",` +
		`"questions":{"Foo":"one","foo":"two"}}]}}}}`)
	if err := rejectDuplicateReviewAttentionJSONKeys(raw); err != nil {
		t.Fatalf("rejectDuplicateReviewAttentionJSONKeys() error = %v", err)
	}
}

func TestReviewAttentionPoliciesUseNumberAndFenceCAS(t *testing.T) {
	resetGatewayTestState(t)
	harness := newReviewAttentionAPITestHarness(t, nil)
	_, current := harness.get(t)
	numericBody := `{"expected_config_revision":"` + current.ConfigRevision +
		`","global":{"review.number":[{"id":"number","kind":"deterministic",` +
		`"when":"true","title":"Exact number","questions":9007199254740993}]}}`
	recorder := harness.request(
		t,
		http.MethodPut,
		reviewAttentionPoliciesPath,
		numericBody,
		http.Header{
			"Content-Type":   {"application/json"},
			"Sec-Fetch-Site": {"same-origin"},
		},
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("numeric PUT status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "9007199254740993") {
		t.Fatalf("numeric PUT rounded response: %s", recorder.Body.String())
	}
	persisted, err := os.ReadFile(harness.configPath)
	if err != nil || !strings.Contains(string(persisted), "9007199254740993") {
		t.Fatalf("numeric PUT rounded persistence: err=%v config=%s", err, persisted)
	}

	// Fetch the post-write revision, then inject a competing writer between
	// validation and SaveConfigIfRevision. The candidate replacement must lose.
	_, afterNumeric := harness.get(t)
	wasInjected := injectConcurrentAliasBeforeReviewAttentionCAS(harness.handler)
	candidate := completeReviewAttentionPolicy()
	conflict := harness.put(t, afterNumeric.ConfigRevision, candidate)
	if conflict.Code != http.StatusConflict ||
		decodeReviewAttentionError(t, conflict) != reviewAttentionConfigRevisionMismatch {
		t.Fatalf("CAS conflict = %d body=%s", conflict.Code, conflict.Body.String())
	}
	if !wasInjected() {
		t.Fatal("concurrent writer was not injected")
	}
	saved := requireConcurrentAlias(t, harness.configPath)
	if _, exists := saved.Reviews.Attention.Global["review.submitted"]; exists {
		t.Fatal("stale attention policy candidate was persisted")
	}
}

func TestReviewAttentionPoliciesAreIsolatedFromGenericConfigAPI(t *testing.T) {
	resetGatewayTestState(t)
	const largeInteger = "9007199254740993"
	harness := newReviewAttentionAPITestHarness(t, func(cfg *config.Config) {
		cfg.Reviews.Attention = config.ReviewAttentionConfig{
			Global: map[string][]gatetypes.GateSpec{
				"review.number": {{
					ID: "number", Kind: gatetypes.GateDeterministic,
					When: "true", Title: "Exact number",
					Questions: map[string]any{"value": json.Number(largeInteger)},
				}},
			},
		}
	})

	genericGet := harness.request(t, http.MethodGet, "/api/config", "", nil)
	if genericGet.Code != http.StatusOK {
		t.Fatalf("generic GET status = %d, body=%s", genericGet.Code, genericGet.Body.String())
	}
	var publicConfig map[string]json.RawMessage
	if err := json.Unmarshal(genericGet.Body.Bytes(), &publicConfig); err != nil {
		t.Fatalf("generic GET decode = %v", err)
	}
	if _, exposed := publicConfig["reviews"]; exposed {
		t.Fatalf("generic GET exposed dedicated policy: %s", genericGet.Body.String())
	}

	genericPut := harness.request(
		t,
		http.MethodPut,
		"/api/config",
		genericGet.Body.String(),
		http.Header{"Content-Type": {"application/json"}},
	)
	if genericPut.Code != http.StatusOK {
		t.Fatalf("generic PUT status = %d, body=%s", genericPut.Code, genericPut.Body.String())
	}
	publicConfig["reviews"] = json.RawMessage(`{}`)
	placeholderBody, err := json.Marshal(publicConfig)
	if err != nil {
		t.Fatalf("generic placeholder marshal = %v", err)
	}
	placeholderPut := harness.request(
		t,
		http.MethodPut,
		"/api/config",
		string(placeholderBody),
		http.Header{"Content-Type": {"application/json"}},
	)
	if placeholderPut.Code != http.StatusOK {
		t.Fatalf(
			"generic placeholder PUT status = %d, body=%s",
			placeholderPut.Code,
			placeholderPut.Body.String(),
		)
	}
	genericPatch := harness.request(
		t,
		http.MethodPatch,
		"/api/config",
		`{"gateway":{"port":23457}}`,
		http.Header{"Content-Type": {"application/json"}},
	)
	if genericPatch.Code != http.StatusOK {
		t.Fatalf("generic PATCH status = %d, body=%s", genericPatch.Code, genericPatch.Body.String())
	}

	for _, mutation := range []struct {
		method string
		body   string
	}{
		{
			method: http.MethodPut,
			body:   `{"reviews":{"attention":{"global":{"review.changed":[]}}}}`,
		},
		{method: http.MethodPatch, body: `{"REVIEWS":{}}`},
	} {
		recorder := harness.request(
			t,
			mutation.method,
			"/api/config",
			mutation.body,
			http.Header{"Content-Type": {"application/json"}},
		)
		if recorder.Code != http.StatusBadRequest ||
			!strings.Contains(recorder.Body.String(), reviewAttentionPoliciesPath) {
			t.Fatalf(
				"generic %s policy mutation = %d body=%s",
				mutation.method,
				recorder.Code,
				recorder.Body.String(),
			)
		}
	}

	saved, _, err := config.LoadCurrentConfigSnapshot(harness.configPath)
	if err != nil {
		t.Fatalf("LoadCurrentConfigSnapshot() error = %v", err)
	}
	questions := saved.Reviews.Attention.Global["review.number"][0].Questions.(map[string]any)
	if questions["value"] != json.Number(largeInteger) || saved.Gateway.Port != 23457 {
		t.Fatalf("generic config changed policy fidelity: %#v", saved.Reviews.Attention)
	}
}

func TestReviewAttentionPoliciesRejectSemanticCatalogAndAgentReferences(t *testing.T) {
	resetGatewayTestState(t)
	harness := newReviewAttentionAPITestHarness(t, func(cfg *config.Config) {
		cfg.Agents.List = []config.AgentConfig{
			{ID: "main", Default: true},
			{ID: "reviewer"},
		}
	})
	_, current := harness.get(t)
	invalidEffective := config.ReviewAttentionConfig{
		Global: map[string][]gatetypes.GateSpec{
			"review.ready": {{
				ID: "global", Kind: gatetypes.GateAIWorkingContext,
				AgentID: "main", Criteria: "Ask", Title: "Global",
			}},
		},
		Repositories: map[string]map[string]gatetypes.RepositoryGatePolicy{
			"Acme/Widgets": {
				"review.ready": {
					Mode: gatetypes.GatePolicyOverlay,
					Gates: []gatetypes.GateSpec{{
						ID: "repository", Kind: gatetypes.GateAIWorkingContext,
						AgentID: "reviewer", Criteria: "Ask", Title: "Repository",
					}},
				},
			},
		},
	}
	if err := invalidEffective.Validate(); err == nil {
		t.Fatal("config validation accepted an invalid effective working-context policy")
	}
	recorder := harness.put(t, current.ConfigRevision, invalidEffective)
	if recorder.Code != http.StatusUnprocessableEntity ||
		decodeReviewAttentionError(t, recorder) != reviewAttentionPoliciesInvalid {
		t.Fatalf("semantic invalid response = %d body=%s", recorder.Code, recorder.Body.String())
	}

	unknownAgent := config.ReviewAttentionConfig{
		Global: map[string][]gatetypes.GateSpec{
			"review.ready": {{
				ID: "agent", Kind: gatetypes.GateAIIsolatedContext,
				AgentID: "missing", Criteria: "Ask", Title: "Missing",
			}},
		},
	}
	recorder = harness.put(t, current.ConfigRevision, unknownAgent)
	if recorder.Code != http.StatusUnprocessableEntity ||
		decodeReviewAttentionError(t, recorder) != reviewAttentionPoliciesInvalid {
		t.Fatalf("unknown agent response = %d body=%s", recorder.Code, recorder.Body.String())
	}

	cfg := config.DefaultConfig()
	cfg.Agents.List = []config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "reviewer"},
	}
	cfg.Reviews.Attention = invalidEffective
	errs := validateConfig(cfg)
	found := false
	for _, err := range errs {
		if strings.Contains(err, "reviews.attention") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("general config validation accepted semantic policy: %v", errs)
	}
}

func TestConfigSignatureTracksOnlyActiveCanonicalAttentionCatalog(t *testing.T) {
	policy := completeReviewAttentionPolicy()

	disabledIngress := config.DefaultConfig()
	disabledIngress.Agents.Defaults.Workspace = t.TempDir()
	disabledIngress.Workflows.Enabled = true
	before := computeConfigSignature(disabledIngress)
	disabledIngress.Reviews.Attention = policy
	if got := computeConfigSignature(disabledIngress); got != before {
		t.Fatal("attention policy changed signature while event ingress was disabled")
	}

	disabledWorkflows := config.DefaultConfig()
	disabledWorkflows.Agents.Defaults.Workspace = t.TempDir()
	disabledWorkflows.Events.Ingress.Enabled = true
	disabledWorkflows.Workflows.Enabled = false
	before = computeConfigSignature(disabledWorkflows)
	disabledWorkflows.Reviews.Attention = policy
	if got := computeConfigSignature(disabledWorkflows); got != before {
		t.Fatal("attention policy changed signature while workflows were disabled")
	}

	active := config.DefaultConfig()
	active.Agents.Defaults.Workspace = t.TempDir()
	active.Events.Ingress.Enabled = true
	active.Workflows.Enabled = true
	before = computeConfigSignature(active)
	active.Reviews.Attention = policy
	activeSignature := computeConfigSignature(active)
	if activeSignature == before {
		t.Fatal("active attention policy did not change runtime signature")
	}

	canonical := config.DefaultConfig()
	canonical.Agents.Defaults.Workspace = active.Agents.Defaults.Workspace
	canonical.Events.Ingress.Enabled = true
	canonical.Workflows.Enabled = true
	canonical.Reviews.Attention = completeReviewAttentionPolicy()
	repositoryPolicy := canonical.Reviews.Attention.Repositories["Acme/Widgets"]
	delete(canonical.Reviews.Attention.Repositories, "Acme/Widgets")
	canonical.Reviews.Attention.Repositories["acme/widgets"] = repositoryPolicy
	if got := computeConfigSignature(canonical); got != activeSignature {
		t.Fatal("repository key case changed canonical attention signature")
	}

	ordered := canonical.Reviews.Attention.Global["review.submitted"]
	ordered[0], ordered[1] = ordered[1], ordered[0]
	canonical.Reviews.Attention.Global["review.submitted"] = ordered
	if got := computeConfigSignature(canonical); got == activeSignature {
		t.Fatal("gate order did not change active attention signature")
	}

	nilCatalog := config.DefaultConfig()
	nilCatalog.Agents.Defaults.Workspace = active.Agents.Defaults.Workspace
	nilCatalog.Events.Ingress.Enabled = true
	nilCatalog.Workflows.Enabled = true
	emptyCatalog := *nilCatalog
	emptyCatalog.Reviews.Attention.Global = map[string][]gatetypes.GateSpec{}
	emptyCatalog.Reviews.Attention.Repositories = map[string]map[string]gatetypes.RepositoryGatePolicy{}
	if computeConfigSignature(nilCatalog) != computeConfigSignature(&emptyCatalog) {
		t.Fatal("nil and empty attention catalogs changed canonical signature")
	}
}
