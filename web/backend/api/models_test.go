package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"

	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func resetModelProbeHooks(t *testing.T) {
	t.Helper()

	origTCPProbe := probeTCPServiceFunc
	origOllamaProbe := probeOllamaModelFunc
	origOpenAIProbe := probeOpenAICompatibleModelFunc
	origCommandProbe := probeCommandAvailableFunc
	origNow := modelProbeNowFunc
	resetModelProbeCache()
	t.Cleanup(func() {
		probeTCPServiceFunc = origTCPProbe
		probeOllamaModelFunc = origOllamaProbe
		probeOpenAICompatibleModelFunc = origOpenAIProbe
		probeCommandAvailableFunc = origCommandProbe
		modelProbeNowFunc = origNow
		resetModelProbeCache()
	})
}

func addModelAndLoadLatest(t *testing.T, configPath string, body string) *config.ModelConfig {
	t.Helper()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if len(cfg.ModelList) == 0 {
		t.Fatal("model_list should contain the newly added model")
	}

	return cfg.ModelList[len(cfg.ModelList)-1]
}

func TestNormalizeStoredModelConfigNormalizesReasoningEffort(t *testing.T) {
	model := &config.ModelConfig{
		ModelName:       "openai",
		Model:           "openai/gpt-5.4",
		ReasoningEffort: "HIGH",
	}

	if !normalizeStoredModelConfig(model) {
		t.Fatal("normalizeStoredModelConfig() = false, want true")
	}
	if got := model.ReasoningEffort; got != "high" {
		t.Fatalf("reasoning_effort = %q, want high", got)
	}
}

func TestNormalizeStoredModelConfigTrimsAndDerivesProvider(t *testing.T) {
	if normalizeStoredModelConfig(nil) {
		t.Fatal("normalizeStoredModelConfig(nil) = true, want false")
	}

	model := &config.ModelConfig{
		ModelName:    "stored",
		Model:        " anthropic/claude-sonnet-4.5 ",
		Provider:     " ",
		AuthMethod:   " TOKEN ",
		CredentialID: " work ",
	}

	if !normalizeStoredModelConfig(model) {
		t.Fatal("normalizeStoredModelConfig() = false, want true")
	}
	if model.Provider != "anthropic" {
		t.Fatalf("provider = %q, want anthropic", model.Provider)
	}
	if model.Model != "claude-sonnet-4.5" {
		t.Fatalf("model = %q, want claude-sonnet-4.5", model.Model)
	}
	if model.AuthMethod != "token" {
		t.Fatalf("auth_method = %q, want token", model.AuthMethod)
	}
	if model.CredentialID != "work" {
		t.Fatalf("credential_id = %q, want work", model.CredentialID)
	}
}

func TestNormalizeStoredModelConfigPreservesExplicitElevenLabsModel(t *testing.T) {
	model := &config.ModelConfig{
		ModelName: "voice",
		Model:     " elevenlabs/other-model ",
		Provider:  " ElevenLabs ",
	}

	if !normalizeStoredModelConfig(model) {
		t.Fatal("normalizeStoredModelConfig() = false, want true")
	}
	if model.Provider != "elevenlabs" {
		t.Fatalf("provider = %q, want elevenlabs", model.Provider)
	}
	if model.Model != "other-model" {
		t.Fatalf("model = %q, want explicit model preserved", model.Model)
	}
}

func TestNormalizeIncomingModelConfigClearsRouterTransportFields(t *testing.T) {
	model := &config.ModelConfig{
		ModelName:    "router-main",
		Model:        " legacy-router-model ",
		Provider:     " router ",
		APIKeys:      config.SimpleSecureStrings("sk-router"),
		APIBase:      "https://example.com/v1",
		Proxy:        "http://proxy.example.com",
		AuthMethod:   " TOKEN ",
		CredentialID: " github-copilot:work ",
		ConnectMode:  "local",
		Workspace:    "/tmp/work",
		Router: &config.AccountRouterConfig{
			Enabled: true,
			Entry:   "primary",
			Blocks: []config.AccountRouterBlock{{
				ID:      "primary",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: "credential:github-copilot:work",
			}},
		},
	}

	normalizeIncomingModelConfig(model)

	if model.Provider != config.AccountRouterProvider {
		t.Fatalf("provider = %q, want router", model.Provider)
	}
	if model.Model != "" {
		t.Fatalf("model = %q, want empty for account router", model.Model)
	}
	if len(model.APIKeys) != 0 || model.APIBase != "" || model.Proxy != "" ||
		model.AuthMethod != "" || model.CredentialID != "" ||
		model.ConnectMode != "" || model.Workspace != "" {
		t.Fatalf("router transport fields were not cleared: %#v", model)
	}
}

func TestNormalizeIncomingModelConfigNormalizesNonRouterFields(t *testing.T) {
	model := &config.ModelConfig{
		ModelName:       "voice",
		Model:           " elevenlabs/other-model ",
		Provider:        " ElevenLabs ",
		AuthMethod:      " TOKEN ",
		CredentialID:    " account-id ",
		ReasoningEffort: "OFF",
	}

	normalizeIncomingModelConfig(model)

	if model.Provider != "elevenlabs" {
		t.Fatalf("provider = %q, want elevenlabs", model.Provider)
	}
	if model.Model != "other-model" {
		t.Fatalf("model = %q, want other-model", model.Model)
	}
	if model.AuthMethod != "token" {
		t.Fatalf("auth_method = %q, want token", model.AuthMethod)
	}
	if model.CredentialID != "elevenlabs:account-id" {
		t.Fatalf("credential_id = %q, want elevenlabs:account-id", model.CredentialID)
	}
	if model.ReasoningEffort != "none" {
		t.Fatalf("reasoning_effort = %q, want none", model.ReasoningEffort)
	}
}

func TestValidateIncomingModelConfigAcceptsEmptyRouterModel(t *testing.T) {
	model := &config.ModelConfig{
		ModelName: "router-main",
		Provider:  config.AccountRouterProvider,
		Router: &config.AccountRouterConfig{
			Enabled: true,
			Entry:   "primary",
			Blocks: []config.AccountRouterBlock{{
				ID:      "primary",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: "credential:github-copilot:work",
			}},
		},
	}

	if err := validateIncomingModelConfig(nil, nil); err == nil {
		t.Fatal("validateIncomingModelConfig(nil) error = nil, want error")
	}
	if err := validateIncomingModelConfig(model, nil); err != nil {
		t.Fatalf("validateIncomingModelConfig(router) error = %v, want nil", err)
	}
}

func TestHandleListModels_AvailabilityUsesRuntimeProbesForLocalModels(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)
	resetModelProbeHooks(t)

	var mu sync.Mutex
	var openAIProbes []string
	var ollamaProbes []string
	var tcpProbes []string

	probeOpenAICompatibleModelFunc = func(apiBase, modelID, apiKey string) bool {
		mu.Lock()
		openAIProbes = append(openAIProbes, apiBase+"|"+modelID+"|"+apiKey)
		mu.Unlock()
		return apiBase == "http://127.0.0.1:8000/v1" && modelID == "custom-model" && apiKey == ""
	}
	probeOllamaModelFunc = func(apiBase, modelID string) bool {
		mu.Lock()
		ollamaProbes = append(ollamaProbes, apiBase+"|"+modelID)
		mu.Unlock()
		return apiBase == "http://localhost:11434/v1" && modelID == "llama3"
	}
	probeTCPServiceFunc = func(apiBase string) bool {
		mu.Lock()
		tcpProbes = append(tcpProbes, apiBase)
		mu.Unlock()
		return apiBase == "http://127.0.0.1:4321"
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName:  "openai-oauth",
			Model:      "openai/gpt-5.4",
			AuthMethod: "oauth",
		},
		{
			ModelName: "vllm-local",
			Model:     "vllm/custom-model",
			APIBase:   "http://127.0.0.1:8000/v1",
		},
		{
			ModelName: "ollama-default",
			Model:     "ollama/llama3",
		},
		{
			ModelName: "vllm-remote",
			Model:     "vllm/custom-model",
			APIBase:   "https://models.example.com/v1",
			APIKeys:   config.SimpleSecureStrings("remote-key"),
		},
		{
			ModelName: "copilot-gpt-5.4",
			Model:     "github-copilot/gpt-5.4",
			APIBase:   "http://127.0.0.1:4321",
		},
	}
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Models []modelResponse `json:"models"`
	}
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	gotAvailable := make(map[string]bool, len(resp.Models))
	gotStatus := make(map[string]string, len(resp.Models))
	for _, model := range resp.Models {
		gotAvailable[model.ModelName] = model.Available
		gotStatus[model.ModelName] = model.Status
	}

	if gotAvailable["openai-oauth"] {
		t.Fatalf("openai oauth model available = true, want false without stored credential")
	}
	if !gotAvailable["vllm-local"] {
		t.Fatalf("vllm local model available = false, want true when local probe succeeds")
	}
	if !gotAvailable["ollama-default"] {
		t.Fatalf("ollama default model available = false, want true when default local probe succeeds")
	}
	if !gotAvailable["vllm-remote"] {
		t.Fatalf("remote vllm model available = false, want true with api_key")
	}
	if !gotAvailable["copilot-gpt-5.4"] {
		t.Fatalf("copilot model available = false, want true when local bridge probe succeeds")
	}
	if gotStatus["openai-oauth"] != modelStatusUnconfigured {
		t.Fatalf("openai oauth model status = %q, want %q", gotStatus["openai-oauth"], modelStatusUnconfigured)
	}
	if gotStatus["vllm-local"] != modelStatusAvailable {
		t.Fatalf("vllm local model status = %q, want %q", gotStatus["vllm-local"], modelStatusAvailable)
	}
	if gotStatus["ollama-default"] != modelStatusAvailable {
		t.Fatalf("ollama default model status = %q, want %q", gotStatus["ollama-default"], modelStatusAvailable)
	}
	if gotStatus["vllm-remote"] != modelStatusAvailable {
		t.Fatalf("remote vllm model status = %q, want %q", gotStatus["vllm-remote"], modelStatusAvailable)
	}
	if gotStatus["copilot-gpt-5.4"] != modelStatusAvailable {
		t.Fatalf("copilot model status = %q, want %q", gotStatus["copilot-gpt-5.4"], modelStatusAvailable)
	}
	if len(openAIProbes) != 1 || openAIProbes[0] != "http://127.0.0.1:8000/v1|custom-model|" {
		t.Fatalf("openAI probes = %#v, want only local vllm probe", openAIProbes)
	}
	if len(ollamaProbes) != 1 || ollamaProbes[0] != "http://localhost:11434/v1|llama3" {
		t.Fatalf("ollama probes = %#v, want default local probe", ollamaProbes)
	}
	if len(tcpProbes) != 1 || tcpProbes[0] != "http://127.0.0.1:4321" {
		t.Fatalf("tcp probes = %#v, want only local copilot probe", tcpProbes)
	}
}

func TestHandleListModels_AvailabilityForOAuthModelWithCredential(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)
	resetModelProbeHooks(t)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName:  "claude-oauth",
		Model:      "anthropic/claude-sonnet-4.6",
		AuthMethod: "oauth",
		Enabled:    true,
	}}
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	if setCredentialErr := auth.SetCredential(oauthProviderAnthropic, &auth.AuthCredential{
		AccessToken: "anthropic-token",
		Provider:    oauthProviderAnthropic,
		AuthMethod:  "oauth",
	}); setCredentialErr != nil {
		t.Fatalf("SetCredential() error = %v", setCredentialErr)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Models []modelResponse `json:"models"`
	}
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(resp.Models))
	}
	if !resp.Models[0].Available {
		t.Fatalf("oauth model available = false, want true with stored credential")
	}
}

func TestHandleListModels_GitHubCopilotTokenCredentialDoesNotProbeLocalBridge(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)
	resetModelProbeHooks(t)

	var mu sync.Mutex
	var tcpProbes []string
	probeTCPServiceFunc = func(apiBase string) bool {
		mu.Lock()
		tcpProbes = append(tcpProbes, apiBase)
		mu.Unlock()
		return false
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName:    "copilot-work",
		Provider:     "github-copilot",
		Model:        "auto",
		AuthMethod:   "token",
		CredentialID: "github-copilot:work",
		Enabled:      true,
	}}
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	if err := auth.SetCredential("github-copilot:work", &auth.AuthCredential{
		AccessToken: "gho_copilot-token",
		Provider:    "github-copilot",
		AuthMethod:  "token",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Models []modelResponse `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(resp.Models))
	}
	if !resp.Models[0].Available || resp.Models[0].Status != modelStatusAvailable {
		t.Fatalf(
			"copilot status = available:%v status:%q, want available",
			resp.Models[0].Available,
			resp.Models[0].Status,
		)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(tcpProbes) != 0 {
		t.Fatalf("tcp probes = %#v, want none for token-backed Copilot", tcpProbes)
	}
}

func TestHasModelConfiguration_OAuthWithoutMappedCredentialFallsBackToAPIKey(t *testing.T) {
	noKey := &config.ModelConfig{
		Provider:   "gemini",
		Model:      "gemini-2.5-flash",
		AuthMethod: "oauth",
	}
	if hasModelConfiguration(noKey) {
		t.Fatal("oauth model without credential mapping and api key should be unconfigured")
	}

	withKey := &config.ModelConfig{
		Provider:   "gemini",
		Model:      "gemini-2.5-flash",
		AuthMethod: "oauth",
		APIKeys:    config.SimpleSecureStrings("gemini-key"),
	}
	if !hasModelConfiguration(withKey) {
		t.Fatal("oauth model without credential mapping should fall back to api key configuration")
	}
}

func TestHandleListModels_AntigravityImplicitOAuthAvailability(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)
	resetModelProbeHooks(t)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "gemini-flash",
		Provider:  "antigravity",
		Model:     "gemini-3-flash",
		Enabled:   true,
	}}
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	if err := auth.SetCredential(oauthProviderGoogleAntigravity, &auth.AuthCredential{
		AccessToken: "antigravity-token",
		Provider:    oauthProviderGoogleAntigravity,
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Models []modelResponse `json:"models"`
	}
	if unmarshalErr := json.Unmarshal(rec.Body.Bytes(), &resp); unmarshalErr != nil {
		t.Fatalf("Unmarshal() error = %v", unmarshalErr)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(resp.Models))
	}
	if !resp.Models[0].Available {
		t.Fatal("antigravity model available = false, want true with stored credential even without auth_method")
	}
}

func TestHandleListModels_BedrockUsesAmbientCredentialStatus(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)
	resetModelProbeHooks(t)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "bedrock-claude",
		Provider:  "bedrock",
		Model:     "us.anthropic.claude-sonnet-4-20250514-v1:0",
	}}
	if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
		t.Fatalf("SaveConfig() error = %v", saveErr)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Models []modelResponse `json:"models"`
	}
	if unmarshalErr := json.Unmarshal(rec.Body.Bytes(), &resp); unmarshalErr != nil {
		t.Fatalf("Unmarshal() error = %v", unmarshalErr)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(resp.Models))
	}
	if !resp.Models[0].Available {
		t.Fatal("bedrock model available = false, want true because Bedrock uses ambient AWS credentials")
	}
	if resp.Models[0].Status != modelStatusAvailable {
		t.Fatalf("bedrock model status = %q, want %q", resp.Models[0].Status, modelStatusAvailable)
	}
}

func TestHandleListModels_CLIProvidersRequireInstalledCommands(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)
	resetModelProbeHooks(t)

	probeCommandAvailableFunc = func(command string) bool {
		switch command {
		case "claude":
			return false
		case "codex":
			return true
		default:
			return false
		}
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "claude-cli-model",
			Provider:  "claude-cli",
			Model:     "claude-cli",
		},
		{
			ModelName: "codex-cli-model",
			Provider:  "codex-cli",
			Model:     "codex-cli",
		},
	}
	if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
		t.Fatalf("SaveConfig() error = %v", saveErr)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Models          []modelResponse                 `json:"models"`
		ProviderOptions []providers.ModelProviderOption `json:"provider_options"`
	}
	if unmarshalErr := json.Unmarshal(rec.Body.Bytes(), &resp); unmarshalErr != nil {
		t.Fatalf("Unmarshal() error = %v", unmarshalErr)
	}

	modelsByName := make(map[string]modelResponse, len(resp.Models))
	for _, model := range resp.Models {
		modelsByName[model.ModelName] = model
	}
	if model := modelsByName["claude-cli-model"]; model.Available || model.Status != modelStatusUnreachable {
		t.Fatalf(
			"claude-cli status = (%t, %q), want (%t, %q)",
			model.Available,
			model.Status,
			false,
			modelStatusUnreachable,
		)
	}
	if model := modelsByName["codex-cli-model"]; !model.Available || model.Status != modelStatusAvailable {
		t.Fatalf(
			"codex-cli status = (%t, %q), want (%t, %q)",
			model.Available,
			model.Status,
			true,
			modelStatusAvailable,
		)
	}

	optionsByID := make(map[string]providers.ModelProviderOption, len(resp.ProviderOptions))
	for _, option := range resp.ProviderOptions {
		optionsByID[option.ID] = option
	}
	if option, ok := optionsByID["claude-cli"]; !ok {
		t.Fatal("claude-cli provider option missing")
	} else if option.CreateAllowed {
		t.Fatal("claude-cli should not be creatable when the claude command is missing")
	}
	if option, ok := optionsByID["codex-cli"]; !ok {
		t.Fatal("codex-cli provider option missing")
	} else if !option.CreateAllowed {
		t.Fatal("codex-cli should be creatable when the codex command is available")
	}
}

func TestHandleListModels_ProbesLocalModelsConcurrently(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)
	resetModelProbeHooks(t)

	started := make(chan string, 2)
	release := make(chan struct{})

	probeOpenAICompatibleModelFunc = func(apiBase, modelID, apiKey string) bool {
		started <- apiBase + "|" + modelID
		<-release
		return true
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "local-vllm-a",
			Model:     "vllm/custom-a",
			APIBase:   "http://127.0.0.1:8000/v1",
		},
		{
			ModelName: "local-vllm-b",
			Model:     "vllm/custom-b",
			APIBase:   "http://127.0.0.1:8001/v1",
		},
	}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	recCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
		mux.ServeHTTP(rec, req)
		recCh <- rec
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(200 * time.Millisecond):
			t.Fatal("expected both local probes to start before the first one completed")
		}
	}
	close(release)

	rec := <-recCh
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestHandleListModels_NormalizesWildcardLocalAPIBaseForProbe(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)
	resetModelProbeHooks(t)

	var gotProbe string
	probeOpenAICompatibleModelFunc = func(apiBase, modelID, apiKey string) bool {
		gotProbe = apiBase + "|" + modelID + "|" + apiKey
		return apiBase == "http://127.0.0.1:8000/v1" && modelID == "custom-model" && apiKey == ""
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "vllm-local",
		Model:     "vllm/custom-model",
		APIBase:   "http://0.0.0.0:8000/v1",
	}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Models []modelResponse `json:"models"`
	}
	if unmarshalErr := json.Unmarshal(rec.Body.Bytes(), &resp); unmarshalErr != nil {
		t.Fatalf("Unmarshal() error = %v", unmarshalErr)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(resp.Models))
	}
	if !resp.Models[0].Available {
		t.Fatal("wildcard-bound local model available = false, want true after probe host normalization")
	}
	if gotProbe != "http://127.0.0.1:8000/v1|custom-model|" {
		t.Fatalf("probe api base = %q, want %q", gotProbe, "http://127.0.0.1:8000/v1|custom-model|")
	}
}

func TestHandleListModelsIncludesCredentialAccountsAsVirtualDefaults(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = nil
	cfg.Agents.Defaults.AccountRef = "credential:openai:work"
	cfg.Agents.Defaults.ModelName = "coding"
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "coding",
		Model: "openai/gpt-5.4",
	}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	if err := auth.SetCredential("openai:work", &auth.AuthCredential{
		Provider:    "openai",
		AuthMethod:  "oauth",
		AccessToken: "oauth-token",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}
	if err := auth.SetCredential("github-copilot:gh-copilot", &auth.AuthCredential{
		Provider:    "github-copilot",
		AuthMethod:  "token",
		AccessToken: "gho_token",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Models            []modelResponse `json:"models"`
		DefaultAccountRef string          `json:"default_account_ref"`
		DefaultModel      string          `json:"default_model"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	modelsByName := make(map[string]modelResponse, len(resp.Models))
	for _, model := range resp.Models {
		modelsByName[model.ModelName] = model
	}

	openAIAccount, ok := modelsByName["credential:openai:work"]
	if !ok {
		t.Fatalf("credential:openai:work missing from models: %#v", modelsByName)
	}
	if !openAIAccount.IsVirtual {
		t.Fatal("credential account IsVirtual = false, want true")
	}
	if !openAIAccount.IsDefault {
		t.Fatal("credential account IsDefault = false, want true")
	}
	if openAIAccount.Provider != "openai" || openAIAccount.AuthMethod != "oauth" {
		t.Fatalf(
			"openai account provider/auth = %q/%q, want openai/oauth",
			openAIAccount.Provider,
			openAIAccount.AuthMethod,
		)
	}
	if openAIAccount.Model != "" {
		t.Fatalf("openai account model = %q, want no implicit model", openAIAccount.Model)
	}

	copilotAccount, ok := modelsByName["credential:github-copilot:gh-copilot"]
	if !ok {
		t.Fatalf("credential:github-copilot:gh-copilot missing from models: %#v", modelsByName)
	}
	if !copilotAccount.Available || copilotAccount.Model != "" {
		t.Fatalf(
			"copilot account available/model = %v/%q, want true with no implicit model",
			copilotAccount.Available,
			copilotAccount.Model,
		)
	}
	if resp.DefaultAccountRef != "credential:openai:work" || resp.DefaultModel != "coding" {
		t.Fatalf(
			"default selection = %q/%q, want credential:openai:work/coding",
			resp.DefaultAccountRef,
			resp.DefaultModel,
		)
	}
}

func TestHandleListModelsDisabledCredentialEntryDoesNotHideVirtualAccount(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName:    "legacy-openai-work",
		Provider:     "openai",
		Model:        "gpt-5.4",
		AuthMethod:   "oauth",
		CredentialID: "openai:work",
		Enabled:      false,
	}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	if err := auth.SetCredential("openai:work", &auth.AuthCredential{
		Provider:    "openai",
		AuthMethod:  "oauth",
		AccessToken: "oauth-token",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Models []modelResponse `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	modelsByName := make(map[string]modelResponse, len(resp.Models))
	for _, model := range resp.Models {
		modelsByName[model.ModelName] = model
	}
	if legacy, ok := modelsByName["legacy-openai-work"]; !ok || legacy.Enabled {
		t.Fatalf("disabled legacy account = %#v, want present and disabled", legacy)
	}
	credential, ok := modelsByName["credential:openai:work"]
	if !ok {
		t.Fatalf("live credential account missing from models: %#v", modelsByName)
	}
	if !credential.Enabled || !credential.Available || !credential.IsVirtual {
		t.Fatalf(
			"credential account enabled/available/virtual = %v/%v/%v, want true/true/true",
			credential.Enabled,
			credential.Available,
			credential.IsVirtual,
		)
	}
}

func TestHandleListModelsSkipsCredentialWithMismatchedProvider(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	if err := auth.SetCredential("openai:cross-wired", &auth.AuthCredential{
		Provider:    "anthropic",
		AuthMethod:  "token",
		AccessToken: "must-not-be-exposed-as-openai",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp struct {
		Models []modelResponse `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	for _, model := range resp.Models {
		if model.ModelName == "credential:openai:cross-wired" {
			t.Fatalf("mismatched credential was exposed in model list: %#v", model)
		}
	}
}

func TestHandleSetDefaultModelAcceptsCredentialAccountRef(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = nil
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "coding",
		Model: "openai/gpt-5.4",
	}}
	cfg.Agents.Defaults.AccountRef = ""
	cfg.Agents.Defaults.ModelName = ""
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	err = auth.SetCredential("openai:work", &auth.AuthCredential{
		Provider:    "openai",
		AuthMethod:  "oauth",
		AccessToken: "oauth-token",
	})
	if err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/accounts/models/default",
		strings.NewReader(`{
			"account_ref":"credential:openai:work",
			"model_name":"coding"
		}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := updated.Agents.Defaults.AccountRef; got != "credential:openai:work" {
		t.Fatalf("default account = %q, want credential:openai:work", got)
	}
	if got := updated.Agents.Defaults.ModelName; got != "coding" {
		t.Fatalf("default model alias = %q, want coding", got)
	}
}

func TestHandleListModels_StatusMarksUnreachableLocalModel(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)
	resetModelProbeHooks(t)

	probeOpenAICompatibleModelFunc = func(apiBase, modelID, apiKey string) bool {
		return false
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "vllm-local-down",
		Model:     "vllm/custom-model",
		APIBase:   "http://127.0.0.1:8000/v1",
		APIKeys:   config.SimpleSecureStrings("test-key"),
	}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Models []modelResponse `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(resp.Models))
	}

	if resp.Models[0].Available {
		t.Fatal("unreachable local model available = true, want false")
	}
	if resp.Models[0].Status != modelStatusUnreachable {
		t.Fatalf("unreachable local model status = %q, want %q", resp.Models[0].Status, modelStatusUnreachable)
	}
	if resp.Models[0].APIKey == "" {
		t.Fatal("masked API key preview should still be returned when API key is configured")
	}
}

func TestHandleListModels_RuntimeProbeUsesExplicitProviderField(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)
	resetModelProbeHooks(t)

	var gotProbe string
	probeOpenAICompatibleModelFunc = func(apiBase, modelID, apiKey string) bool {
		gotProbe = apiBase + "|" + modelID + "|" + apiKey
		return true
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "vllm-local",
		Provider:  "vllm",
		Model:     "custom-model",
		APIBase:   "http://127.0.0.1:8000/v1",
	}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	if gotProbe != "http://127.0.0.1:8000/v1|custom-model|" {
		t.Fatalf("probe = %q, want %q", gotProbe, "http://127.0.0.1:8000/v1|custom-model|")
	}
}

func TestHandleAddModel_PersistsAPIKey(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models", bytes.NewBufferString(`{
		"model_name":"new-model",
		"provider":"openai",
		"model":"openai/gpt-4o-mini",
		"api_key":"sk-new-model-key"
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if len(cfg.ModelList) != 2 {
		t.Fatalf("len(model_list) = %d, want 2", len(cfg.ModelList))
	}

	added := cfg.ModelList[1]
	if added.ModelName != "new-model" {
		t.Fatalf("model_name = %q, want %q", added.ModelName, "new-model")
	}
	if added.APIKey() != "sk-new-model-key" {
		t.Fatalf("api_key = %q, want %q", added.APIKey(), "sk-new-model-key")
	}
}

func TestHandleAddModel_PersistsProvider(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models", bytes.NewBufferString(`{
		"model_name":"nvidia-glm",
		"provider":"nvidia",
		"model":"z-ai/glm-5.1",
		"api_key":"nv-key"
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	added := cfg.ModelList[len(cfg.ModelList)-1]
	if added.Provider != "nvidia" {
		t.Fatalf("provider = %q, want %q", added.Provider, "nvidia")
	}
	if added.Model != "z-ai/glm-5.1" {
		t.Fatalf("model = %q, want %q", added.Model, "z-ai/glm-5.1")
	}
}

func TestHandleListModels_ReturnsStreamingConfig(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "streaming-model",
		Provider:  "openai",
		Model:     "gpt-4o-mini",
		APIKeys:   config.SimpleSecureStrings("sk-existing"),
		Streaming: config.ModelStreamingConfig{Enabled: true},
	}}
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Models []modelResponse `json:"models"`
	}
	if err = json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(resp.Models))
	}
	if !resp.Models[0].Streaming.Enabled {
		t.Fatal("streaming.enabled = false, want true")
	}
}

func TestHandleAddModel_RejectsUnsupportedProvider(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models", bytes.NewBufferString(`{
		"model_name":"bad-provider",
		"provider":"not-supported",
		"model":"gpt-4o-mini"
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `provider "not-supported" is not supported`) {
		t.Fatalf("body = %q, want unsupported provider error", rec.Body.String())
	}
}

func TestHandleAddModel_RejectsUnsupportedReasoningEffort(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models", bytes.NewBufferString(`{
		"model_name":"openai-max",
		"provider":"openai",
		"model":"gpt-5.4",
		"reasoning_effort":"max"
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `unsupported reasoning_effort "max"`) {
		t.Fatalf("body = %q, want unsupported reasoning_effort error", rec.Body.String())
	}
}

func TestHandleAddModel_AllowsBedrockProvider(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models", bytes.NewBufferString(`{
		"model_name":"bedrock-claude",
		"provider":"bedrock",
		"model":"us.anthropic.claude-sonnet-4-20250514-v1:0"
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	added := cfg.ModelList[len(cfg.ModelList)-1]
	if got := added.Provider; got != "bedrock" {
		t.Fatalf("provider = %q, want %q", got, "bedrock")
	}
	if got := added.Model; got != "us.anthropic.claude-sonnet-4-20250514-v1:0" {
		t.Fatalf("model = %q, want bedrock model ID", got)
	}
}

func TestHandleAddModel_NormalizesLegacyElevenLabsASRConfig(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "elevenlabs-asr",
		Model:     "elevenlabs/scribe_v1",
		APIKeys:   config.SimpleSecureStrings("sk_elevenlabs_test"),
	}}
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models", bytes.NewBufferString(`{
		"model_name":"new-model",
		"provider":"openai",
		"model":"gpt-4o-mini",
		"api_key":"sk-new-model-key"
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if len(updated.ModelList) != 2 {
		t.Fatalf("len(model_list) = %d, want 2", len(updated.ModelList))
	}
	if got := updated.ModelList[0].Provider; got != "elevenlabs" {
		t.Fatalf("provider = %q, want %q after normalization", got, "elevenlabs")
	}
	if got := updated.ModelList[0].Model; got != "scribe_v1" {
		t.Fatalf("model = %q, want %q after normalization", got, "scribe_v1")
	}
}

func TestHandleAddModelPreservesExplicitStoredElevenLabsModelID(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "elevenlabs-asr",
		Provider:  "elevenlabs",
		Model:     "scribe_v2",
		APIKeys:   config.SimpleSecureStrings("sk_elevenlabs_test"),
	}}
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models", bytes.NewBufferString(`{
		"model_name":"new-model",
		"provider":"openai",
		"model":"gpt-4o-mini",
		"api_key":"sk-new-model-key"
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := updated.ModelList[0].Provider; got != "elevenlabs" {
		t.Fatalf("provider = %q, want %q after normalization", got, "elevenlabs")
	}
	if got := updated.ModelList[0].Model; got != "scribe_v2" {
		t.Fatalf("model = %q, want explicit stored model preserved", got)
	}
}

func TestHandleAddModel_RejectsMissingCLIProviderCommand(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetOAuthHooks(t)
	resetModelProbeHooks(t)

	probeCommandAvailableFunc = func(command string) bool {
		return false
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models", bytes.NewBufferString(`{
		"model_name":"claude-cli-model",
		"provider":"claude-cli",
		"model":"claude-cli"
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `provider "claude-cli" is not available for new models`) {
		t.Fatalf("body = %q, want missing cli command error", rec.Body.String())
	}
}

func TestHandleAddModel_DefaultsAntigravityToOAuth(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	added := addModelAndLoadLatest(t, configPath, `{
		"model_name":"gemini-flash",
		"provider":"antigravity",
		"model":"gemini-3-flash"
	}`)
	if got := added.AuthMethod; got != "oauth" {
		t.Fatalf("auth_method = %q, want %q", got, "oauth")
	}
}

func TestHandleAddModel_NormalizesMixedCaseAuthMethod(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	added := addModelAndLoadLatest(t, configPath, `{
		"model_name":"openai-oauth",
		"provider":"openai",
		"model":"gpt-5.4",
		"auth_method":"OAuth"
	}`)
	if got := added.AuthMethod; got != "oauth" {
		t.Fatalf("auth_method = %q, want %q", got, "oauth")
	}
}

func TestHandleAddModel_PreservesExplicitProviderPrefixedModel(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models", bytes.NewBufferString(`{
		"model_name":"openai-gpt",
		"provider":"openai",
		"model":"openai/gpt-4o-mini",
		"api_key":"sk-openai"
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	added := cfg.ModelList[len(cfg.ModelList)-1]
	if got := added.Provider; got != "openai" {
		t.Fatalf("provider = %q, want %q", got, "openai")
	}
	if got := added.Model; got != "openai/gpt-4o-mini" {
		t.Fatalf("model = %q, want %q", got, "openai/gpt-4o-mini")
	}
}

func TestHandleAddModel_PersistsCustomHeaders(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models", bytes.NewBufferString(`{
		"model_name":"new-model-headers",
		"provider":"openai",
		"model":"openai/gpt-4o-mini",
		"custom_headers":{"X-Source":"coding-plan","X-Agent":"openclaw"}
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if len(cfg.ModelList) != 2 {
		t.Fatalf("len(model_list) = %d, want 2", len(cfg.ModelList))
	}

	added := cfg.ModelList[1]
	if added.CustomHeaders == nil {
		t.Fatal("custom_headers should not be nil")
	}
	if got := added.CustomHeaders["X-Source"]; got != "coding-plan" {
		t.Fatalf("custom_headers[X-Source] = %q, want %q", got, "coding-plan")
	}
	if got := added.CustomHeaders["X-Agent"]; got != "openclaw" {
		t.Fatalf("custom_headers[X-Agent] = %q, want %q", got, "openclaw")
	}
}

func TestHandleAddModel_PersistsToolSchemaTransform(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models", bytes.NewBufferString(`{
		"model_name":"new-model-transform",
		"provider":"openai",
		"model":"openai/gpt-4o-mini",
		"tool_schema_transform":"simple"
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	added := cfg.ModelList[len(cfg.ModelList)-1]
	if got := added.ToolSchemaTransform; got != "simple" {
		t.Fatalf("tool_schema_transform = %q, want %q", got, "simple")
	}
}

func TestHandleUpdateModel_CustomHeadersPreserveAndClear(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName:     "editable",
		Model:         "openai/gpt-4o-mini",
		APIKeys:       config.SimpleSecureStrings("sk-existing"),
		CustomHeaders: map[string]string{"X-Source": "coding-plan"},
	}}
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Omitted custom_headers should preserve existing value.
	recPreserve := httptest.NewRecorder()
	reqPreserve := httptest.NewRequest(
		http.MethodPut,
		modelMutationURLWithCurrentRevision(t, configPath, "/api/accounts/models/0"),
		bytes.NewBufferString(`{
			"model_name":"editable",
			"model":"openai/gpt-4o-mini"
		}`),
	)
	reqPreserve.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recPreserve, reqPreserve)
	if recPreserve.Code != http.StatusOK {
		t.Fatalf("preserve status = %d, want %d, body=%s", recPreserve.Code, http.StatusOK, recPreserve.Body.String())
	}

	afterPreserve, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() after preserve error = %v", err)
	}
	if got := afterPreserve.ModelList[0].CustomHeaders["X-Source"]; got != "coding-plan" {
		t.Fatalf("preserved custom_headers[X-Source] = %q, want %q", got, "coding-plan")
	}

	// Empty object should clear custom_headers.
	recClear := httptest.NewRecorder()
	reqClear := httptest.NewRequest(
		http.MethodPut,
		modelMutationURLWithCurrentRevision(t, configPath, "/api/accounts/models/0"),
		bytes.NewBufferString(`{
			"model_name":"editable",
			"model":"openai/gpt-4o-mini",
			"custom_headers":{}
		}`),
	)
	reqClear.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recClear, reqClear)
	if recClear.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want %d, body=%s", recClear.Code, http.StatusOK, recClear.Body.String())
	}

	afterClear, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() after clear error = %v", err)
	}
	if afterClear.ModelList[0].CustomHeaders != nil {
		t.Fatalf("custom_headers = %#v, want nil", afterClear.ModelList[0].CustomHeaders)
	}
}

func TestHandleUpdateModel_ToolSchemaTransformPreserveAndClear(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName:           "editable",
		Model:               "openai/gpt-4o-mini",
		APIKeys:             config.SimpleSecureStrings("sk-existing"),
		ToolSchemaTransform: "simple",
	}}
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	recPreserve := httptest.NewRecorder()
	reqPreserve := httptest.NewRequest(
		http.MethodPut,
		modelMutationURLWithCurrentRevision(t, configPath, "/api/accounts/models/0"),
		bytes.NewBufferString(`{
			"model_name":"editable",
			"model":"openai/gpt-4o-mini"
		}`),
	)
	reqPreserve.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recPreserve, reqPreserve)
	if recPreserve.Code != http.StatusOK {
		t.Fatalf("preserve status = %d, want %d, body=%s", recPreserve.Code, http.StatusOK, recPreserve.Body.String())
	}

	afterPreserve, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() after preserve error = %v", err)
	}
	if got := afterPreserve.ModelList[0].ToolSchemaTransform; got != "simple" {
		t.Fatalf("preserved tool_schema_transform = %q, want %q", got, "simple")
	}

	recClear := httptest.NewRecorder()
	reqClear := httptest.NewRequest(
		http.MethodPut,
		modelMutationURLWithCurrentRevision(t, configPath, "/api/accounts/models/0"),
		bytes.NewBufferString(`{
			"model_name":"editable",
			"model":"openai/gpt-4o-mini",
			"tool_schema_transform":""
		}`),
	)
	reqClear.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recClear, reqClear)
	if recClear.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want %d, body=%s", recClear.Code, http.StatusOK, recClear.Body.String())
	}

	afterClear, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() after clear error = %v", err)
	}
	if afterClear.ModelList[0].ToolSchemaTransform != "" {
		t.Fatalf("tool_schema_transform = %q, want empty", afterClear.ModelList[0].ToolSchemaTransform)
	}
}

func TestHandleUpdateModel_StreamingPreserveAndChange(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "editable",
		Provider:  "openai",
		Model:     "gpt-4o-mini",
		APIKeys:   config.SimpleSecureStrings("sk-existing"),
		Streaming: config.ModelStreamingConfig{Enabled: true},
	}}
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	recPreserve := httptest.NewRecorder()
	reqPreserve := httptest.NewRequest(
		http.MethodPut,
		modelMutationURLWithCurrentRevision(t, configPath, "/api/accounts/models/0"),
		bytes.NewBufferString(`{
			"model_name":"editable",
			"provider":"openai",
			"model":"gpt-4o-mini"
		}`),
	)
	reqPreserve.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recPreserve, reqPreserve)
	if recPreserve.Code != http.StatusOK {
		t.Fatalf("preserve status = %d, want %d, body=%s", recPreserve.Code, http.StatusOK, recPreserve.Body.String())
	}

	afterPreserve, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() after preserve error = %v", err)
	}
	if !afterPreserve.ModelList[0].Streaming.Enabled {
		t.Fatal("preserved streaming.enabled = false, want true")
	}

	recChange := httptest.NewRecorder()
	reqChange := httptest.NewRequest(
		http.MethodPut,
		modelMutationURLWithCurrentRevision(t, configPath, "/api/accounts/models/0"),
		bytes.NewBufferString(`{
			"model_name":"editable",
			"provider":"openai",
			"model":"gpt-4o-mini",
			"streaming":{"enabled":false}
		}`),
	)
	reqChange.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recChange, reqChange)
	if recChange.Code != http.StatusOK {
		t.Fatalf("change status = %d, want %d, body=%s", recChange.Code, http.StatusOK, recChange.Body.String())
	}

	afterChange, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() after change error = %v", err)
	}
	if afterChange.ModelList[0].Streaming.Enabled {
		t.Fatal("streaming.enabled = true, want false after explicit update")
	}
}

func TestHandleUpdateModel_PersistsProvider(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "editable",
		Model:     "gpt-4o",
		Provider:  "openai",
	}}
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		modelMutationURLWithCurrentRevision(t, configPath, "/api/accounts/models/0"),
		bytes.NewBufferString(`{
			"model_name":"editable",
			"provider":"openrouter",
			"model":"openai/gpt-4o"
		}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := updated.ModelList[0].Provider; got != "openrouter" {
		t.Fatalf("provider = %q, want %q", got, "openrouter")
	}
}

func TestHandleUpdateModel_PreservesExplicitProviderPrefixedModel(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "editable",
		Model:     "gpt-4o",
		Provider:  "openai",
	}}
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		modelMutationURLWithCurrentRevision(t, configPath, "/api/accounts/models/0"),
		bytes.NewBufferString(`{
			"model_name":"editable",
			"provider":"openai",
			"model":"openai/gpt-5.4"
		}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := updated.ModelList[0].Provider; got != "openai" {
		t.Fatalf("provider = %q, want %q", got, "openai")
	}
	if got := updated.ModelList[0].Model; got != "openai/gpt-5.4" {
		t.Fatalf("model = %q, want %q", got, "openai/gpt-5.4")
	}
}

func TestHandleListModels_PreservesExplicitProviderPrefixedModel(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "openrouter-auto-explicit",
		Provider:  "openrouter",
		Model:     "openrouter/auto",
	}}
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Models []modelResponse `json:"models"`
	}
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(resp.Models))
	}
	if got := resp.Models[0].Provider; got != "openrouter" {
		t.Fatalf("provider = %q, want %q", got, "openrouter")
	}
	if got := resp.Models[0].Model; got != "openrouter/auto" {
		t.Fatalf("model = %q, want %q", got, "openrouter/auto")
	}
}

func TestHandleListModels_ExposesElevenLabsASRProvider(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "elevenlabs-asr",
		Model:     "elevenlabs/scribe_v1",
		APIKeys:   config.SimpleSecureStrings("sk_elevenlabs_test"),
	}}
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Models []modelResponse `json:"models"`
	}
	if err = json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(resp.Models))
	}
	if got := resp.Models[0].Provider; got != "elevenlabs" {
		t.Fatalf("provider = %q, want %q", got, "elevenlabs")
	}
	if got := resp.Models[0].Model; got != "scribe_v1" {
		t.Fatalf("model = %q, want %q", got, "scribe_v1")
	}
}

func TestHandleUpdateModel_PreservesLegacyModelPrefixWhenProviderOmitted(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "legacy-openrouter",
		Model:     "openrouter/openai/gpt-5.4",
	}}
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Simulate a provider-omitted update: the client reads the Accounts model
	// API, ignores the provider field, then PUTs the visible model string back
	// unchanged.
	recList := httptest.NewRecorder()
	reqList := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(recList, reqList)

	if recList.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d, body=%s", recList.Code, http.StatusOK, recList.Body.String())
	}

	var listResp struct {
		Models []modelResponse `json:"models"`
	}
	if err = json.Unmarshal(recList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(listResp.Models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(listResp.Models))
	}
	if got := listResp.Models[0].Provider; got != "openrouter" {
		t.Fatalf("provider = %q, want %q", got, "openrouter")
	}
	if got := listResp.Models[0].Model; got != "openai/gpt-5.4" {
		t.Fatalf("model = %q, want %q", got, "openai/gpt-5.4")
	}

	recUpdate := httptest.NewRecorder()
	reqUpdate := httptest.NewRequest(
		http.MethodPut,
		modelMutationURLWithCurrentRevision(t, configPath, "/api/accounts/models/0"),
		bytes.NewBufferString(`{
			"model_name":"legacy-openrouter",
			"model":"openai/gpt-5.4"
		}`),
	)
	reqUpdate.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recUpdate, reqUpdate)

	if recUpdate.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d, body=%s", recUpdate.Code, http.StatusOK, recUpdate.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := updated.ModelList[0].Provider; got != "openrouter" {
		t.Fatalf("provider = %q, want %q", got, "openrouter")
	}
	if got := updated.ModelList[0].Model; got != "openai/gpt-5.4" {
		t.Fatalf("model = %q, want %q", got, "openai/gpt-5.4")
	}
}

func TestHandleUpdateModel_RejectsUnsupportedReasoningEffort(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "openai",
		Provider:  "openai",
		Model:     "gpt-5.4",
	}}
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		modelMutationURLWithCurrentRevision(t, configPath, "/api/accounts/models/0"),
		bytes.NewBufferString(`{
			"model_name":"openai",
			"provider":"openai",
			"model":"gpt-5.4",
			"reasoning_effort":"max"
		}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `unsupported reasoning_effort "max"`) {
		t.Fatalf("body = %q, want unsupported reasoning_effort error", rec.Body.String())
	}
}

func TestHandleUpdateModel_MigratesLegacyElevenLabsASRWhenProviderOmitted(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "elevenlabs-asr",
		Model:     "elevenlabs/scribe_v1",
		APIKeys:   config.SimpleSecureStrings("sk_elevenlabs_test"),
	}}
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	recList := httptest.NewRecorder()
	reqList := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(recList, reqList)

	if recList.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d, body=%s", recList.Code, http.StatusOK, recList.Body.String())
	}

	var listResp struct {
		Models []modelResponse `json:"models"`
	}
	if err = json.Unmarshal(recList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(listResp.Models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(listResp.Models))
	}
	if got := listResp.Models[0].Provider; got != "elevenlabs" {
		t.Fatalf("provider = %q, want %q", got, "elevenlabs")
	}
	if got := listResp.Models[0].Model; got != "scribe_v1" {
		t.Fatalf("model = %q, want %q", got, "scribe_v1")
	}

	recUpdate := httptest.NewRecorder()
	reqUpdate := httptest.NewRequest(
		http.MethodPut,
		modelMutationURLWithCurrentRevision(t, configPath, "/api/accounts/models/0"),
		bytes.NewBufferString(`{
			"model_name":"elevenlabs-asr",
			"model":"scribe_v1",
			"api_base":"https://api.elevenlabs.io"
		}`),
	)
	reqUpdate.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recUpdate, reqUpdate)

	if recUpdate.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d, body=%s", recUpdate.Code, http.StatusOK, recUpdate.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := updated.ModelList[0].Provider; got != "elevenlabs" {
		t.Fatalf("provider = %q, want %q", got, "elevenlabs")
	}
	if got := updated.ModelList[0].Model; got != "scribe_v1" {
		t.Fatalf("model = %q, want %q", got, "scribe_v1")
	}
	if got := updated.ModelList[0].APIBase; got != "https://api.elevenlabs.io" {
		t.Fatalf("api_base = %q, want %q", got, "https://api.elevenlabs.io")
	}
}

func TestHandleUpdateModel_RoundTripsExplicitLegacyElevenLabsModelID(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "elevenlabs-asr",
		Provider:  "elevenlabs",
		Model:     "scribe_v2",
		APIKeys:   config.SimpleSecureStrings("sk_elevenlabs_test"),
	}}
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	recList := httptest.NewRecorder()
	reqList := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(recList, reqList)

	if recList.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d, body=%s", recList.Code, http.StatusOK, recList.Body.String())
	}

	var listResp struct {
		Models []modelResponse `json:"models"`
	}
	if err = json.Unmarshal(recList.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(listResp.Models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(listResp.Models))
	}
	if got := listResp.Models[0].Provider; got != "elevenlabs" {
		t.Fatalf("provider = %q, want %q", got, "elevenlabs")
	}
	if got := listResp.Models[0].Model; got != "scribe_v2" {
		t.Fatalf("model = %q, want explicit stored model preserved", got)
	}

	recUpdate := httptest.NewRecorder()
	reqUpdate := httptest.NewRequest(
		http.MethodPut,
		modelMutationURLWithCurrentRevision(t, configPath, "/api/accounts/models/0"),
		bytes.NewBufferString(`{
			"model_name":"elevenlabs-asr",
			"provider":"elevenlabs",
			"model":"scribe_v1",
			"api_base":"https://api.elevenlabs.io"
		}`),
	)
	reqUpdate.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recUpdate, reqUpdate)

	if recUpdate.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d, body=%s", recUpdate.Code, http.StatusOK, recUpdate.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := updated.ModelList[0].Provider; got != "elevenlabs" {
		t.Fatalf("provider = %q, want %q", got, "elevenlabs")
	}
	if got := updated.ModelList[0].Model; got != "scribe_v1" {
		t.Fatalf("model = %q, want %q", got, "scribe_v1")
	}
	if got := updated.ModelList[0].APIBase; got != "https://api.elevenlabs.io" {
		t.Fatalf("api_base = %q, want %q", got, "https://api.elevenlabs.io")
	}
}

func TestHandleUpdateModelPreservesIndependentDefaultAliasSelection(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "elevenlabs-asr",
		Provider:  "elevenlabs",
		APIKeys:   config.SimpleSecureStrings("sk_elevenlabs_test"),
		Enabled:   true,
	}}
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "transcription",
		Model: "elevenlabs/scribe_v1",
	}}
	cfg.Voice.AccountRef = "elevenlabs-asr"
	cfg.Voice.ModelName = "transcription"
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		modelMutationURLWithCurrentRevision(t, configPath, "/api/accounts/models/0"),
		bytes.NewBufferString(`{
			"model_name":"elevenlabs-asr",
			"provider":"elevenlabs",
			"model":"scribe_v1",
			"api_base":"https://api.elevenlabs.io"
		}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := updated.Voice.AccountRef; got != "elevenlabs-asr" {
		t.Fatalf("voice account = %q, want preserved", got)
	}
	if got := updated.Voice.ModelName; got != "transcription" {
		t.Fatalf("voice model alias = %q, want preserved", got)
	}
}

func TestHandleAddModel_RejectsUnsupportedElevenLabsModelID(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models", bytes.NewBufferString(`{
		"model_name":"elevenlabs-asr",
		"provider":"elevenlabs",
		"model":"scribe_v2"
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `provider "elevenlabs" only supports model "scribe_v1"`) {
		t.Fatalf("body = %q, want elevenlabs model validation error", rec.Body.String())
	}
}

func TestHandleUpdateModel_PreservesLegacyModelPrefixWhenProviderOmittedAndModelChanges(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "legacy-openrouter",
		Model:     "openrouter/openai/gpt-5.4",
	}}
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		modelMutationURLWithCurrentRevision(t, configPath, "/api/accounts/models/0"),
		bytes.NewBufferString(`{
			"model_name":"legacy-openrouter",
			"model":"openai/gpt-5.5"
		}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := updated.ModelList[0].Provider; got != "openrouter" {
		t.Fatalf("provider = %q, want %q", got, "openrouter")
	}
	if got := updated.ModelList[0].Model; got != "openai/gpt-5.5" {
		t.Fatalf("model = %q, want %q", got, "openai/gpt-5.5")
	}
}

func TestHandleListModels_ReturnsProviderOptionsWithoutPersistingLegacyMigration(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "legacy-openrouter",
		Model:     "openrouter/openai/gpt-5.4",
	}}
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Models          []modelResponse                 `json:"models"`
		ProviderOptions []providers.ModelProviderOption `json:"provider_options"`
	}
	if unmarshalErr := json.Unmarshal(rec.Body.Bytes(), &resp); unmarshalErr != nil {
		t.Fatalf("Unmarshal() error = %v", unmarshalErr)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(resp.Models))
	}
	if got := resp.Models[0].Provider; got != "openrouter" {
		t.Fatalf("provider = %q, want %q", got, "openrouter")
	}
	if got := resp.Models[0].Model; got != "openai/gpt-5.4" {
		t.Fatalf("model = %q, want %q", got, "openai/gpt-5.4")
	}

	optionsByID := make(map[string]providers.ModelProviderOption, len(resp.ProviderOptions))
	for _, option := range resp.ProviderOptions {
		optionsByID[option.ID] = option
	}
	if len(optionsByID) == 0 {
		t.Fatal("provider_options should not be empty")
	}
	if option, ok := optionsByID["openai"]; !ok {
		t.Fatal("openai provider option missing")
	} else if option.DefaultAPIBase != "https://api.openai.com/v1" {
		t.Fatalf("openai default_api_base = %q, want %q", option.DefaultAPIBase, "https://api.openai.com/v1")
	} else if !option.SupportsFetch {
		t.Fatal("openai provider option should report supports_fetch")
	} else if option.DisplayName != "OpenAI" {
		t.Fatalf("openai display_name = %q, want %q", option.DisplayName, "OpenAI")
	} else if len(option.CommonModels) == 0 {
		t.Fatal("openai common_models should not be empty")
	}
	if option, ok := optionsByID["anthropic"]; !ok {
		t.Fatal("anthropic provider option missing")
	} else if option.DefaultAPIBase != "https://api.anthropic.com/v1" {
		t.Fatalf("anthropic default_api_base = %q, want %q", option.DefaultAPIBase, "https://api.anthropic.com/v1")
	}
	if _, ok := optionsByID["azure"]; !ok {
		t.Fatal("azure provider option missing")
	}
	if option, ok := optionsByID["github-copilot"]; !ok {
		t.Fatal("github-copilot provider option missing")
	} else if option.DefaultAPIBase != "localhost:4321" {
		t.Fatalf("github-copilot default_api_base = %q, want %q", option.DefaultAPIBase, "localhost:4321")
	} else if !option.Local {
		t.Fatal("github-copilot should be marked local")
	} else if option.DefaultAuthMethod != "token" {
		t.Fatalf("github-copilot default_auth_method = %q, want token", option.DefaultAuthMethod)
	} else if !option.SupportsFetch {
		t.Fatal("github-copilot provider option should report supports_fetch")
	} else if len(option.CommonModels) == 0 {
		t.Fatal("github-copilot common_models should not be empty")
	}
	if option, ok := optionsByID["elevenlabs"]; !ok {
		t.Fatal("elevenlabs provider option missing")
	} else if option.DefaultAPIBase != "https://api.elevenlabs.io" {
		t.Fatalf("elevenlabs default_api_base = %q, want %q", option.DefaultAPIBase, "https://api.elevenlabs.io")
	}
	if option, ok := optionsByID["lmstudio"]; !ok {
		t.Fatal("lmstudio provider option missing")
	} else if !option.EmptyAPIKeyAllowed {
		t.Fatal("lmstudio should allow empty api keys")
	}
	if option, ok := optionsByID["gpt4free"]; !ok {
		t.Fatal("gpt4free provider option missing")
	} else {
		if option.DefaultAPIBase != "http://localhost:1337/v1" {
			t.Fatalf("gpt4free default_api_base = %q, want %q", option.DefaultAPIBase, "http://localhost:1337/v1")
		}
		if !option.EmptyAPIKeyAllowed {
			t.Fatal("gpt4free should allow empty api keys")
		}
		if !option.SupportsFetch {
			t.Fatal("gpt4free provider option should report supports_fetch")
		}
	}
	if option, ok := optionsByID["siliconflow"]; !ok {
		t.Fatal("siliconflow provider option missing")
	} else if option.DefaultAPIBase != "https://api.siliconflow.cn/v1" {
		t.Fatalf(
			"siliconflow default_api_base = %q, want %q",
			option.DefaultAPIBase,
			"https://api.siliconflow.cn/v1",
		)
	}
	if option, ok := optionsByID["nearai"]; !ok {
		t.Fatal("nearai provider option missing")
	} else if option.DefaultAPIBase != "https://cloud-api.near.ai/v1" {
		t.Fatalf("nearai default_api_base = %q, want %q", option.DefaultAPIBase, "https://cloud-api.near.ai/v1")
	} else if !option.SupportsFetch {
		t.Fatal("nearai provider option should report supports_fetch")
	}
	if option, ok := optionsByID["bedrock"]; !ok {
		t.Fatal("bedrock provider option missing")
	} else if !option.CreateAllowed {
		t.Fatal("bedrock should stay creatable and defer AWS credential failures to runtime")
	}
	if option, ok := optionsByID["antigravity"]; !ok {
		t.Fatal("antigravity provider option missing")
	} else {
		if option.DefaultAuthMethod != "oauth" {
			t.Fatalf("antigravity default_auth_method = %q, want %q", option.DefaultAuthMethod, "oauth")
		}
		if !option.AuthMethodLocked {
			t.Fatal("antigravity auth method should be locked")
		}
	}
	if option, ok := optionsByID["qwen-portal"]; !ok {
		t.Fatal("qwen-portal provider option missing")
	} else if len(option.Aliases) == 0 || option.Aliases[0] != "qwen" {
		t.Fatalf("qwen-portal aliases = %#v, want to include qwen", option.Aliases)
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := updated.ModelList[0].Provider; got != "" {
		t.Fatalf("persisted provider = %q, want unchanged empty provider", got)
	}
	if got := updated.ModelList[0].Model; got != "openrouter/openai/gpt-5.4" {
		t.Fatalf("persisted model = %q, want unchanged legacy model", got)
	}
}

func TestHandleListModels_ReturnsProviderField(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "nvidia-glm",
		Provider:  "nvidia",
		Model:     "z-ai/glm-5.1",
		APIKeys:   config.SimpleSecureStrings("nv-key"),
	}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Models []modelResponse `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(resp.Models))
	}
	if got := resp.Models[0].Provider; got != "nvidia" {
		t.Fatalf("provider = %q, want %q", got, "nvidia")
	}
}

func TestHandleListModels_PreservesKnownProviderInCatalog(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "bedrock-claude",
		Model:     "bedrock/us.anthropic.claude-sonnet-4-20250514-v1:0",
	}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Models          []modelResponse                 `json:"models"`
		ProviderOptions []providers.ModelProviderOption `json:"provider_options"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(resp.Models))
	}
	if got := resp.Models[0].Provider; got != "bedrock" {
		t.Fatalf("provider = %q, want %q", got, "bedrock")
	}
	if got := resp.Models[0].Model; got != "us.anthropic.claude-sonnet-4-20250514-v1:0" {
		t.Fatalf("model = %q, want %q", got, "us.anthropic.claude-sonnet-4-20250514-v1:0")
	}
	foundBedrock := false
	for _, option := range resp.ProviderOptions {
		if option.ID == "bedrock" {
			foundBedrock = true
			if !option.CreateAllowed {
				t.Fatal("bedrock should stay creatable in provider_options")
			}
		}
	}
	if !foundBedrock {
		t.Fatal("bedrock should be included in provider_options for compatibility")
	}
}

func TestHandleUpdateModel_AllowsExistingBedrockProvider(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "bedrock-claude",
		Provider:  "bedrock",
		Model:     "us.anthropic.claude-sonnet-4-20250514-v1:0",
		APIBase:   "us-west-2",
	}}
	if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
		t.Fatalf("SaveConfig() error = %v", saveErr)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPut,
		modelMutationURLWithCurrentRevision(t, configPath, "/api/accounts/models/0"),
		bytes.NewBufferString(`{
			"model_name":"bedrock-claude",
			"provider":"bedrock",
			"model":"us.anthropic.claude-3-7-sonnet-20250219-v1:0",
			"api_base":"us-east-1"
		}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := updated.ModelList[0].Provider; got != "bedrock" {
		t.Fatalf("provider = %q, want %q", got, "bedrock")
	}
	if got := updated.ModelList[0].Model; got != "us.anthropic.claude-3-7-sonnet-20250219-v1:0" {
		t.Fatalf("model = %q, want updated bedrock model", got)
	}
	if got := updated.ModelList[0].APIBase; got != "us-east-1" {
		t.Fatalf("api_base = %q, want %q", got, "us-east-1")
	}
}

func TestHandleListModels_ReturnsEffectiveProviderField(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "plain-openai",
			Model:     "gpt-4o",
		},
		{
			ModelName: "explicit-google",
			Provider:  "google",
			Model:     "gemini-2.5-pro",
		},
		{
			ModelName: "explicit-qwen-intl",
			Provider:  "qwen-international",
			Model:     "qwen3-coder-plus",
		},
	}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Models []modelResponse `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if len(resp.Models) != 3 {
		t.Fatalf("len(models) = %d, want 3", len(resp.Models))
	}

	if got := resp.Models[0].Provider; got != "openai" {
		t.Fatalf("provider[0] = %q, want %q", got, "openai")
	}
	if got := resp.Models[0].Model; got != "gpt-4o" {
		t.Fatalf("model[0] = %q, want %q", got, "gpt-4o")
	}
	if got := resp.Models[1].Provider; got != "gemini" {
		t.Fatalf("provider[1] = %q, want %q", got, "gemini")
	}
	if got := resp.Models[1].Model; got != "gemini-2.5-pro" {
		t.Fatalf("model[1] = %q, want %q", got, "gemini-2.5-pro")
	}
	if got := resp.Models[2].Provider; got != "qwen-intl" {
		t.Fatalf("provider[2] = %q, want %q", got, "qwen-intl")
	}
	if got := resp.Models[2].Model; got != "qwen3-coder-plus" {
		t.Fatalf("model[2] = %q, want %q", got, "qwen3-coder-plus")
	}
}

// TestHandleSetDefaultModel_RejectsNonexistentModel tests that setting a non-existent
// model as default returns 404. This covers the case where virtual models (which are
// filtered by SaveConfig) cannot be set as default.
func TestHandleSetDefaultModel_RejectsNonexistentModel(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	// First save a valid config with a primary model
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{
		{ModelName: "gpt-4", Provider: "openai", Enabled: true},
	}
	cfg.ModelAliases = []config.ModelAliasConfig{{Name: "coding", Model: "openai/gpt-4o"}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	// Try to set a non-existent model (like a virtual model name) as default
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models/default", bytes.NewBufferString(`{
		"account_ref": "gpt-4",
		"model_name": "gpt-4__key_1"
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	// Should return 404 because the virtual model doesn't exist in the persisted config
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not configured") {
		t.Fatalf("error message should mention 'not configured', got: %s", rec.Body.String())
	}
}

func TestHandleSetDefaultModel_RejectsElevenLabsASRProvider(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "elevenlabs-asr",
			Provider:  "elevenlabs",
			APIKeys:   config.SimpleSecureStrings("sk_elevenlabs_test"),
			Enabled:   true,
		},
	}
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "transcription",
		Model: "elevenlabs/scribe_v1",
	}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models/default", bytes.NewBufferString(`{
		"account_ref": "elevenlabs-asr",
		"model_name": "transcription"
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "is not usable for chat") {
		t.Fatalf("body = %q, want chat model rejection", rec.Body.String())
	}
}

func TestHandleModels_AccountRouterRoundTripAndDefault(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()
	resetModelProbeHooks(t)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{
		{ModelName: "account-a", Provider: "openai", Model: "gpt-4o", Enabled: true},
		{ModelName: "account-b", Provider: "openai", Model: "gpt-4o-mini", Enabled: true},
	}
	cfg.ModelAliases = []config.ModelAliasConfig{{Name: "coding", Model: "gpt-4o"}}
	cfg.Agents.Defaults.AccountRef = "account-a"
	cfg.Agents.Defaults.ModelName = "coding"
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	addBody := `{
		"model_name": "router-main",
		"model": "gpt-5.4",
		"provider": "router",
		"api_key": "sk-should-not-persist",
		"router": {
			"enabled": true,
			"entry": "entry",
			"blocks": [
				{"id": "entry", "type": "account", "account": "account-a", "fallback": "fallback"},
				{"id": "fallback", "type": "account", "account": "account-b"}
			]
		}
	}`
	addRec := httptest.NewRecorder()
	addReq := httptest.NewRequest(http.MethodPost, "/api/accounts/models", bytes.NewBufferString(addBody))
	addReq.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(addRec, addReq)
	if addRec.Code != http.StatusOK {
		t.Fatalf("add status = %d, want %d, body=%s", addRec.Code, http.StatusOK, addRec.Body.String())
	}

	var addResp struct {
		Index int `json:"index"`
	}
	err = json.Unmarshal(addRec.Body.Bytes(), &addResp)
	if err != nil {
		t.Fatalf("add response unmarshal: %v", err)
	}

	updateBody := `{
		"model_name": "router-main",
		"model": "gpt-5.4",
		"provider": "router",
		"router": {
			"enabled": true,
			"entry": "pool",
			"refresh_interval_seconds": 45,
			"blocks": [
				{
					"id": "pool",
					"type": "load_balance",
					"accounts": ["account-a", "account-b"],
					"strategy": "closest_limit",
					"refresh_interval_seconds": 45
				}
			]
		}
	}`
	updateRec := httptest.NewRecorder()
	updateReq := httptest.NewRequest(
		http.MethodPut,
		modelMutationURLWithCurrentRevision(
			t,
			configPath,
			fmt.Sprintf("/api/accounts/models/%d", addResp.Index),
		),
		bytes.NewBufferString(updateBody),
	)
	updateReq.SetPathValue("index", fmt.Sprint(addResp.Index))
	updateReq.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d, body=%s", updateRec.Code, http.StatusOK, updateRec.Body.String())
	}

	defaultRec := httptest.NewRecorder()
	defaultReq := httptest.NewRequest(http.MethodPost, "/api/accounts/models/default", bytes.NewBufferString(`{
		"account_ref": "router-main",
		"model_name": "coding"
	}`))
	defaultReq.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(defaultRec, defaultReq)
	if defaultRec.Code != http.StatusOK {
		t.Fatalf("default status = %d, want %d, body=%s", defaultRec.Code, http.StatusOK, defaultRec.Body.String())
	}

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/accounts/models", nil)
	mux.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d, body=%s", listRec.Code, http.StatusOK, listRec.Body.String())
	}

	var listResp struct {
		Models          []modelResponse                 `json:"models"`
		ProviderOptions []providers.ModelProviderOption `json:"provider_options"`
	}
	err = json.Unmarshal(listRec.Body.Bytes(), &listResp)
	if err != nil {
		t.Fatalf("list response unmarshal: %v", err)
	}

	var routerModel *modelResponse
	for i := range listResp.Models {
		if listResp.Models[i].ModelName == "router-main" {
			routerModel = &listResp.Models[i]
			break
		}
	}
	if routerModel == nil {
		t.Fatal("router-main missing from list response")
	}
	if routerModel.Provider != config.AccountRouterProvider {
		t.Fatalf("router provider = %q, want %q", routerModel.Provider, config.AccountRouterProvider)
	}
	if routerModel.Router == nil || routerModel.Router.Entry != "pool" {
		t.Fatalf("router config = %#v, want entry pool", routerModel.Router)
	}
	if !routerModel.Available || routerModel.Status != modelStatusAvailable {
		t.Fatalf("router status = (%t, %q), want available", routerModel.Available, routerModel.Status)
	}
	if !routerModel.IsDefault {
		t.Fatal("router should be marked as the selected default account")
	}
	if routerModel.APIKey != "" {
		t.Fatalf("router api_key = %q, want empty", routerModel.APIKey)
	}

	optionsByID := make(map[string]providers.ModelProviderOption, len(listResp.ProviderOptions))
	for _, option := range listResp.ProviderOptions {
		optionsByID[option.ID] = option
	}
	if option, ok := optionsByID[config.AccountRouterProvider]; !ok {
		t.Fatal("router provider option missing")
	} else if option.CreateAllowed {
		t.Fatal("router provider option should not be creatable through generic provider form")
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() after writes error = %v", err)
	}
	if got := updated.Agents.Defaults.AccountRef; got != "router-main" {
		t.Fatalf("default account = %q, want router-main", got)
	}
	if got := updated.Agents.Defaults.ModelName; got != "coding" {
		t.Fatalf("default alias = %q, want coding", got)
	}
	if len(updated.AccountRouters) != 1 {
		t.Fatalf("len(account_routers) = %d, want 1", len(updated.AccountRouters))
	}
	if got := updated.AccountRouters[0].Name; got != "router-main" {
		t.Fatalf("account router name = %q, want router-main", got)
	}
	rawConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile(config) error = %v", err)
	}
	var persisted struct {
		ModelList []struct {
			ModelName string                     `json:"model_name"`
			Router    map[string]any             `json:"router"`
			APIKeys   []string                   `json:"api_keys"`
			Extra     map[string]json.RawMessage `json:"-"`
		} `json:"model_list"`
		AccountRouters []config.AccountRouterConfig `json:"account_routers"`
	}
	if err = json.Unmarshal(rawConfig, &persisted); err != nil {
		t.Fatalf("Unmarshal persisted config error = %v", err)
	}
	if len(persisted.AccountRouters) != 1 {
		t.Fatalf("persisted account_routers = %d, want 1", len(persisted.AccountRouters))
	}
	for _, model := range persisted.ModelList {
		if model.ModelName == "router-main" {
			t.Fatalf("router-main persisted in model_list: %#v", model)
		}
	}
}

func TestLegacyModelRoutesAreNotRegistered(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/models"},
		{method: http.MethodPost, path: "/api/models"},
		{method: http.MethodPost, path: "/api/models/fetch"},
		{method: http.MethodGet, path: "/api/models/catalog"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
			}
		})
	}
}

func TestHandleAddModel_RejectsRouterUnknownAccountAsBadRequest(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{
		{ModelName: "account-a", Provider: "openai", Model: "gpt-4o"},
	}
	err = config.SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models", bytes.NewBufferString(`{
		"model_name": "router-main",
		"model": "gpt-4o",
		"provider": "router",
		"router": {
			"enabled": true,
			"entry": "entry",
			"blocks": [
				{"id": "entry", "type": "account", "account": "missing"}
			]
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unknown account") {
		t.Fatalf("body = %q, want unknown account validation", rec.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() after rejected add error = %v", err)
	}
	if len(updated.ModelList) != 1 {
		t.Fatalf("model_list len = %d, want 1", len(updated.ModelList))
	}
}

func TestHandleAddModelIgnoresLegacyAccountRouterModel(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{
		{ModelName: "account-a", Provider: "openai", Model: "gpt-4o", Enabled: true},
	}
	if err = config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	addBody := `{
		"model_name": "router-main",
		"model": "router-main",
		"provider": "router",
		"router": {
			"enabled": true,
			"entry": "entry",
			"blocks": [
				{"id": "entry", "type": "account", "account": "account-a"}
			]
		}
	}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models", bytes.NewBufferString(addBody))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("add status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() after add error = %v", err)
	}
	router := updated.ModelList[len(updated.ModelList)-1]
	if router.Model != "" {
		t.Fatalf("router model = %q, want legacy input discarded", router.Model)
	}
}

func TestHandleAddModel_AcceptsGitHubCopilotCredentialAccountRouterRef(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	if err := auth.SetCredential("github-copilot:gh-copilot", &auth.AuthCredential{
		AccessToken: "gho_test-token",
		Provider:    "github-copilot",
		AuthMethod:  "token",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models", bytes.NewBufferString(`{
		"model_name": "copilot-router",
		"model": "gpt-4o",
		"provider": "router",
		"router": {
			"enabled": true,
			"entry": "account-1",
			"blocks": [
				{
					"id": "account-1",
					"type": "account",
					"account": "credential:github-copilot:gh-copilot"
				}
			]
		}
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() after add error = %v", err)
	}
	router := updated.ModelList[len(updated.ModelList)-1]
	if router.Router == nil || len(router.Router.Blocks) != 1 {
		t.Fatalf("router config = %#v, want one block", router.Router)
	}
	if got := router.Router.Blocks[0].Account; got != "credential:github-copilot:gh-copilot" {
		t.Fatalf("router account = %q, want credential:github-copilot:gh-copilot", got)
	}
	if got := router.Model; got != "" {
		t.Fatalf("router model = %q, want empty", got)
	}
}

func TestHandleAddModel_AcceptsGitHubCopilotCredentialLoadBalanceRouterRefs(t *testing.T) {
	for _, strategy := range []string{
		config.AccountRouterStrategyBlind,
		config.AccountRouterStrategyTokensSpent,
		config.AccountRouterStrategyClosestLimit,
	} {
		t.Run(strategy, func(t *testing.T) {
			configPath, cleanup := setupOAuthTestEnv(t)
			defer cleanup()

			for _, credentialID := range []string{"github-copilot:gh-copilot", "github-copilot:backup"} {
				if err := auth.SetCredential(credentialID, &auth.AuthCredential{
					AccessToken: "gho_test-token",
					Provider:    "github-copilot",
					AuthMethod:  "token",
				}); err != nil {
					t.Fatalf("SetCredential(%q) error = %v", credentialID, err)
				}
			}

			h := NewHandler(configPath)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/accounts/models", bytes.NewBufferString(fmt.Sprintf(`{
				"model_name": "copilot-router",
				"model": "gpt-4o",
				"provider": "router",
				"router": {
					"enabled": true,
					"entry": "pool",
					"blocks": [
						{
							"id": "pool",
							"type": "load_balance",
							"accounts": ["credential:github-copilot:gh-copilot", "credential:github-copilot:backup"],
							"strategy": %q
						}
					]
				}
			}`, strategy)))
			req.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}

			updated, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig() after add error = %v", err)
			}
			router := updated.ModelList[len(updated.ModelList)-1]
			if router.Router == nil || len(router.Router.Blocks) != 1 {
				t.Fatalf("router config = %#v, want one block", router.Router)
			}
			block := router.Router.Blocks[0]
			if block.Type != config.AccountRouterBlockTypeLoadBalance {
				t.Fatalf("router block type = %q, want load_balance", block.Type)
			}
			if block.Strategy != strategy {
				t.Fatalf("router strategy = %q, want %q", block.Strategy, strategy)
			}
			wantAccounts := []string{"credential:github-copilot:gh-copilot", "credential:github-copilot:backup"}
			if fmt.Sprint(block.Accounts) != fmt.Sprint(wantAccounts) {
				t.Fatalf("router accounts = %v, want %v", block.Accounts, wantAccounts)
			}
			if got := router.Model; got != "" {
				t.Fatalf("router model = %q, want empty", got)
			}
		})
	}
}

func TestMaskAPIKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{
			name: "empty key",
			key:  "",
			want: "",
		},
		{
			name: "short key fully masked",
			key:  "abcd",
			want: "****",
		},
		{
			name: "length 8 boundary fully masked",
			key:  "12345678",
			want: "****",
		},
		{
			name: "length 9 boundary shows last 2",
			key:  "123456789",
			want: "123****89",
		},
		{
			name: "length 12 boundary shows last 2",
			key:  "abcdefghijkl",
			want: "abc****kl",
		},
		{
			name: "length 13 boundary shows last 4",
			key:  "abcdefghijklm",
			want: "abc****jklm",
		},
		{
			name: "typical api key",
			key:  "sk-1234567890abcd",
			want: "sk-****abcd",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := maskAPIKey(tc.key)
			if got != tc.want {
				t.Fatalf("maskAPIKey(%q) = %q, want %q", tc.key, got, tc.want)
			}

			if tc.key != "" {
				displayed := strings.Replace(tc.want, "****", "", 1)
				if len(tc.key) <= 8 {
					if displayed != "" {
						t.Fatalf("maskAPIKey(%q) displayed part = %q, want empty", tc.key, displayed)
					}
				} else {
					if len(displayed)*10 > len(tc.key)*6 {
						t.Fatalf(
							"maskAPIKey(%q) displayed length = %d, want at most 60%% of %d",
							tc.key,
							len(displayed),
							len(tc.key),
						)
					}
				}
			}
		})
	}
}

func TestFetchOpenAICompatibleModels_ResponseShapes(t *testing.T) {
	tests := []struct {
		name      string
		response  string
		apiKey    string
		wantLen   int
		wantFirst struct {
			id, ownedBy string
		}
		wantSecond struct {
			id, ownedBy string
		}
	}{
		{
			name:     "envelope shape",
			response: `{"data":[{"id":"gpt-4o","owned_by":"openai"},{"id":"gpt-4o-mini","owned_by":"openai"}]}`,
			apiKey:   "test-key",
			wantLen:  2,
			wantFirst: struct {
				id, ownedBy string
			}{id: "gpt-4o", ownedBy: "openai"},
			wantSecond: struct {
				id, ownedBy string
			}{id: "gpt-4o-mini", ownedBy: "openai"},
		},
		{
			name:     "bare array shape",
			response: `[{"id":"qwen-max","owned_by":"qwen"},{"id":"qwen-plus","owned_by":"qwen"}]`,
			apiKey:   "",
			wantLen:  2,
			wantFirst: struct {
				id, ownedBy string
			}{id: "qwen-max", ownedBy: "qwen"},
			wantSecond: struct {
				id, ownedBy string
			}{id: "qwen-plus", ownedBy: "qwen"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tt.response)
			}))
			defer srv.Close()

			models, err := fetchOpenAICompatibleModels(t.Context(), srv.URL+"/models", tt.apiKey)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if len(models) != tt.wantLen {
				t.Fatalf("len(models) = %d, want %d", len(models), tt.wantLen)
			}
			if models[0].ID != tt.wantFirst.id || models[0].OwnedBy != tt.wantFirst.ownedBy {
				t.Fatalf("models[0] = %+v, want {ID:%s OwnedBy:%s}", models[0], tt.wantFirst.id, tt.wantFirst.ownedBy)
			}
			if models[1].ID != tt.wantSecond.id || models[1].OwnedBy != tt.wantSecond.ownedBy {
				t.Fatalf("models[1] = %+v, want {ID:%s OwnedBy:%s}", models[1], tt.wantSecond.id, tt.wantSecond.ownedBy)
			}
		})
	}
}

func TestFetchOpenAICompatibleModels_EmptyEnvelopeReturnsEmptySlice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()

	models, err := fetchOpenAICompatibleModels(t.Context(), srv.URL+"/models", "k")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("len(models) = %d, want 0", len(models))
	}
}

func TestFetchOpenAICompatibleModels_EmptyBareArrayReturnsEmptySlice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	models, err := fetchOpenAICompatibleModels(t.Context(), srv.URL+"/models", "k")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("len(models) = %d, want 0", len(models))
	}
}

func TestFetchOpenAICompatibleModels_UnrecognizedShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"models":[],"error":"unsupported"}`)
	}))
	defer srv.Close()

	_, err := fetchOpenAICompatibleModels(t.Context(), srv.URL+"/models", "k")
	if err == nil {
		t.Fatal("error = nil, want unrecognized shape error")
	}
	if !strings.Contains(err.Error(), "unrecognized shape") {
		t.Fatalf("error = %q, want it to contain 'unrecognized shape'", err.Error())
	}
}

func TestFetchOpenAICompatibleModels_FiltersEmptyIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[`+
			`{"id":"gpt-4o","owned_by":"openai"},`+
			`{"id":"","owned_by":"openai"},`+
			`{"id":"gpt-4o-mini"}]}`)
	}))
	defer srv.Close()

	models, err := fetchOpenAICompatibleModels(t.Context(), srv.URL+"/models", "k")
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2 (empty IDs should be filtered)", len(models))
	}
	if models[0].ID != "gpt-4o" {
		t.Fatalf("models[0].ID = %q, want %q", models[0].ID, "gpt-4o")
	}
	if models[1].ID != "gpt-4o-mini" {
		t.Fatalf("models[1].ID = %q, want %q", models[1].ID, "gpt-4o-mini")
	}
}

func TestFetchOpenAICompatibleModels_SetsAuthorizationHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"m1"}]}`)
	}))
	defer srv.Close()

	if _, err := fetchOpenAICompatibleModels(t.Context(), srv.URL+"/models", "my-secret-key"); err != nil {
		t.Fatalf("error = %v", err)
	}
	if gotAuth != "Bearer my-secret-key" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer my-secret-key")
	}
}

func TestFetchOpenAICompatibleModels_NoAuthHeaderWhenKeyEmpty(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":"m1"}]`)
	}))
	defer srv.Close()

	if _, err := fetchOpenAICompatibleModels(t.Context(), srv.URL+"/models", ""); err != nil {
		t.Fatalf("error = %v", err)
	}
	if gotAuth != "" {
		t.Fatalf("Authorization = %q, want empty", gotAuth)
	}
}

func TestHandleFetchModels_SiliconFlowUsesOpenAICompatibleEndpoint(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	var gotPath string
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"deepseek-ai/DeepSeek-V3","owned_by":"siliconflow"}]}`)
	}))
	defer srv.Close()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models/fetch", bytes.NewBufferString(fmt.Sprintf(`{
		"provider":"siliconflow",
		"api_key":"sk-siliconflow",
		"api_base":"%s"
	}`, srv.URL)))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	if gotPath != "/models" {
		t.Fatalf("path = %q, want %q", gotPath, "/models")
	}
	if gotAuth != "Bearer sk-siliconflow" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer sk-siliconflow")
	}

	var resp struct {
		Models []upstreamModel `json:"models"`
		Total  int             `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if resp.Total != 1 || len(resp.Models) != 1 {
		t.Fatalf("response = %+v, want one fetched model", resp)
	}
	if resp.Models[0].ID != "deepseek-ai/DeepSeek-V3" {
		t.Fatalf("model id = %q, want %q", resp.Models[0].ID, "deepseek-ai/DeepSeek-V3")
	}
}

func TestHandleFetchModels_NearAIUsesPublicModelListEndpoint(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	var gotPath string
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"models":[`+
			`{"modelId":"zai-org/GLM-5.1-FP8","metadata":{"ownedBy":"nearai"}},`+
			`{"modelId":"openai/gpt-oss-120b","metadata":{"ownedBy":"nearai"}},`+
			`{"modelId":""}]}`)
	}))
	defer srv.Close()

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models/fetch", bytes.NewBufferString(fmt.Sprintf(`{
		"provider":"nearai",
		"api_key":"nearai-key",
		"api_base":"%s"
	}`, srv.URL)))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	if gotPath != "/model/list" {
		t.Fatalf("path = %q, want %q", gotPath, "/model/list")
	}
	if gotAuth != "Bearer nearai-key" {
		t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer nearai-key")
	}

	var resp struct {
		Models []upstreamModel `json:"models"`
		Total  int             `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if resp.Total != 2 || len(resp.Models) != 2 {
		t.Fatalf("response = %+v, want two fetched models", resp)
	}
	if resp.Models[0].ID != "zai-org/GLM-5.1-FP8" || resp.Models[0].OwnedBy != "nearai" {
		t.Fatalf("models[0] = %+v, want GLM model owned by nearai", resp.Models[0])
	}
	if resp.Models[1].ID != "openai/gpt-oss-120b" || resp.Models[1].OwnedBy != "nearai" {
		t.Fatalf("models[1] = %+v, want GPT OSS model owned by nearai", resp.Models[1])
	}
}

func TestFetchUpstreamModels_GitHubCopilotReturnsStaticModelsWithoutCredential(t *testing.T) {
	var mu sync.Mutex
	hitServer := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hitServer = true
		mu.Unlock()
		http.Error(w, "unexpected request", http.StatusTeapot)
	}))
	defer srv.Close()

	models, err := fetchUpstreamModels(t.Context(), upstreamFetchOptions{
		Provider: "github-copilot",
		APIBase:  srv.URL,
		APIKey:   "ignored",
	})
	if err != nil {
		t.Fatalf("fetchUpstreamModels() error = %v", err)
	}
	if len(models) == 0 {
		t.Fatal("fetchUpstreamModels() returned no models")
	}
	if models[0].ID == "auto" || models[0].OwnedBy != "github-copilot" {
		t.Fatalf("models[0] = %+v, want an explicit model owned by github-copilot", models[0])
	}

	mu.Lock()
	defer mu.Unlock()
	if hitServer {
		t.Fatal("github-copilot fetch should not call the OpenAI-compatible /models endpoint")
	}
}

func TestHandleFetchModels_GitHubCopilotCredentialUsesDirectModelList(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	oldList := listGitHubCopilotModelsWithToken
	t.Cleanup(func() { listGitHubCopilotModelsWithToken = oldList })

	var gotToken string
	listGitHubCopilotModelsWithToken = func(
		_ context.Context,
		token string,
	) ([]copilot.ModelInfo, error) {
		gotToken = token
		return []copilot.ModelInfo{
			{ID: "gpt-5", Name: "GPT-5"},
			{ID: "claude-sonnet-4.5", Name: "Claude Sonnet 4.5"},
			{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro"},
			{ID: "gpt-5", Name: "duplicate"},
			{ID: ""},
		}, nil
	}

	if err := auth.SetCredential("github-copilot:gh-copilot", &auth.AuthCredential{
		AccessToken: "gho_dynamic-model-token",
		Provider:    "github-copilot",
		AuthMethod:  "token",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models/fetch", bytes.NewBufferString(`{
		"provider":"github-copilot",
		"auth_method":"token",
		"credential_id":"github-copilot:gh-copilot"
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if gotToken != "gho_dynamic-model-token" {
		t.Fatalf("Copilot token = %q, want stored credential token", gotToken)
	}

	var resp struct {
		Models []upstreamModel `json:"models"`
		Total  int             `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if resp.Total != 3 || len(resp.Models) != 3 {
		t.Fatalf("response = %+v, want three unique direct Copilot models", resp)
	}
	if resp.Models[0].ID != "gpt-5" ||
		resp.Models[1].ID != "claude-sonnet-4.5" ||
		resp.Models[2].ID != "gemini-2.5-pro" {
		t.Fatalf("models = %+v, want direct Copilot model IDs in order", resp.Models)
	}
}

func TestHandleFetchModels_OpenAIOAuthStoredModelUsesCodexModelsEndpoint(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	var gotPath string
	var gotClientVersion string
	var gotAuth string
	var gotAccountID string
	var gotProductSKU string
	var gotOriginator string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotClientVersion = r.URL.Query().Get("client_version")
		gotAuth = r.Header.Get("Authorization")
		gotAccountID = r.Header.Get("Chatgpt-Account-Id")
		gotProductSKU = r.Header.Get("Oai-Product-Sku")
		gotOriginator = r.Header.Get("Originator")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"models":[`+
			`{"slug":"gpt-5.4","display_name":"GPT-5.4","visibility":"list","supported_in_api":true},`+
			`{"slug":"hidden-model","visibility":"hidden","supported_in_api":true},`+
			`{"slug":""}]}`)
	}))
	defer srv.Close()

	oldEndpoint := openAICodexModelsEndpoint
	openAICodexModelsEndpoint = srv.URL + "/backend-api/codex/models"
	t.Cleanup(func() { openAICodexModelsEndpoint = oldEndpoint })

	if err := auth.SetCredential("openai:work", &auth.AuthCredential{
		AccessToken: "chatgpt-token",
		AccountID:   "acc-123",
		Provider:    "openai",
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName:    "openai-codex",
			Provider:     "openai",
			Model:        "gpt-5.4",
			AuthMethod:   "oauth",
			CredentialID: "openai:work",
		},
	}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models/fetch", bytes.NewBufferString(`{
		"provider":"openai",
		"api_base":"https://api.openai.com/v1",
		"model_index":0
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if gotPath != "/backend-api/codex/models" {
		t.Fatalf("path = %q, want Codex models endpoint", gotPath)
	}
	if gotClientVersion != openAICodexModelsClientVersionDefault {
		t.Fatalf("client_version = %q, want %q", gotClientVersion, openAICodexModelsClientVersionDefault)
	}
	if gotAuth != "Bearer chatgpt-token" {
		t.Fatalf("Authorization = %q, want ChatGPT bearer token", gotAuth)
	}
	if gotAccountID != "acc-123" {
		t.Fatalf("Chatgpt-Account-Id = %q, want acc-123", gotAccountID)
	}
	if gotProductSKU != "codex" {
		t.Fatalf("Oai-Product-Sku = %q, want codex", gotProductSKU)
	}
	if gotOriginator != "codex_cli_rs" {
		t.Fatalf("Originator = %q, want codex_cli_rs", gotOriginator)
	}

	var resp struct {
		Models []upstreamModel `json:"models"`
		Total  int             `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if resp.Total != 1 || len(resp.Models) != 1 {
		t.Fatalf("response = %+v, want one visible Codex model", resp)
	}
	if resp.Models[0].ID != "gpt-5.4" || resp.Models[0].OwnedBy != "openai-codex" {
		t.Fatalf("models[0] = %+v, want Codex model", resp.Models[0])
	}
}

func TestHandleFetchModels_OpenAIOAuthRequestCredentialFetchesWithoutAPIKey(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer default-chatgpt-token" {
			t.Errorf("Authorization = %q, want ChatGPT token", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"models":[{"slug":"gpt-5.4","visibility":"list"}]}`)
	}))
	defer srv.Close()

	oldEndpoint := openAICodexModelsEndpoint
	openAICodexModelsEndpoint = srv.URL + "/models"
	t.Cleanup(func() { openAICodexModelsEndpoint = oldEndpoint })

	if err := auth.SetCredential("openai", &auth.AuthCredential{
		AccessToken: "default-chatgpt-token",
		AccountID:   "acc-default",
		Provider:    "openai",
		AuthMethod:  "oauth",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models/fetch", bytes.NewBufferString(`{
		"provider":"openai",
		"api_base":"https://api.openai.com/v1",
		"auth_method":"oauth"
	}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Models []upstreamModel `json:"models"`
		Total  int             `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if resp.Total != 1 || resp.Models[0].ID != "gpt-5.4" {
		t.Fatalf("response = %+v, want fetched Codex model", resp)
	}
}

func TestOpenAICodexModelsClientVersionUsesCodexCompatibilityVersion(t *testing.T) {
	oldVersion := config.Version
	t.Cleanup(func() { config.Version = oldVersion })

	for _, version := range []string{"dev", "59065cd4", "", "v0.2.9"} {
		config.Version = version
		if got := openAICodexModelsClientVersion(); got != openAICodexModelsClientVersionDefault {
			t.Fatalf(
				"openAICodexModelsClientVersion(%q) = %q, want %q",
				version,
				got,
				openAICodexModelsClientVersionDefault,
			)
		}
	}
}

func TestUpstreamStatusErrorIncludesResponseBody(t *testing.T) {
	err := upstreamStatusError(
		"codex upstream",
		http.StatusBadRequest,
		strings.NewReader(`{"detail":"Invalid client_version format"}`),
	)
	if err == nil {
		t.Fatal("upstreamStatusError() returned nil")
	}
	if !strings.Contains(err.Error(), "Invalid client_version format") {
		t.Fatalf("error = %q, want response body detail", err.Error())
	}
}

func TestHandleFetchModels_ModelIndexUsesStoredKey(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"gpt-4o","owned_by":"openai"}]}`)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	oldHome := os.Getenv("PICOCLAW_HOME")
	t.Setenv("PICOCLAW_HOME", filepath.Join(tmp, ".picoclaw"))
	defer func() {
		if oldHome != "" {
			os.Setenv("PICOCLAW_HOME", oldHome)
		} else {
			os.Unsetenv("PICOCLAW_HOME")
		}
	}()

	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "my-openai",
			Provider:  "openai",
			Model:     "gpt-4o",
			APIKeys:   config.SimpleSecureStrings("sk-stored-secret"),
			APIBase:   srv.URL,
		},
	}
	configPath := filepath.Join(tmp, "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig error: %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	idx := 0
	body := fmt.Sprintf(`{"provider":"openai","api_base":"%s","model_index":%d}`, srv.URL, idx)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models/fetch", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if gotAuth != "Bearer sk-stored-secret" {
		t.Fatalf("Authorization = %q, want stored key to be used", gotAuth)
	}

	var resp struct {
		Models []upstreamModel `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if len(resp.Models) != 1 || resp.Models[0].ID != "gpt-4o" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestHandleFetchModels_ModelIndexProviderMismatchRejectsKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("stored key should NOT be sent to mismatched provider")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	t.Setenv("PICOCLAW_HOME", filepath.Join(tmp, ".picoclaw"))

	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "my-openai",
			Provider:  "openai",
			Model:     "gpt-4o",
			APIKeys:   config.SimpleSecureStrings("sk-openai-secret"),
			APIBase:   "https://api.openai.com/v1",
		},
	}
	configPath := filepath.Join(tmp, "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig error: %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	body := fmt.Sprintf(`{"provider":"siliconflow","api_base":"%s","model_index":0}`, srv.URL)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/models/fetch", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
}

func TestHandleFetchModels_AccountRefCredential(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	oldList := listGitHubCopilotModelsWithToken
	t.Cleanup(func() { listGitHubCopilotModelsWithToken = oldList })

	var gotToken string
	listGitHubCopilotModelsWithToken = func(
		_ context.Context,
		token string,
	) ([]copilot.ModelInfo, error) {
		gotToken = token
		return []copilot.ModelInfo{
			{ID: "gpt-5.4"},
			{ID: "claude-sonnet-4.6"},
		}, nil
	}
	if err := auth.SetCredential(
		"github-copilot:work",
		&auth.AuthCredential{
			AccessToken: "gho-work-token",
			Provider:    "github-copilot",
			AuthMethod:  "token",
		},
	); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/accounts/models/fetch",
		strings.NewReader(`{"account_ref":"credential:github-copilot:work"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if gotToken != "gho-work-token" {
		t.Fatalf("token = %q, want stored credential token", gotToken)
	}
	var resp fetchModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if resp.Total != 2 || len(resp.Models) != 2 {
		t.Fatalf("response = %+v, want two credential models", resp)
	}
	if resp.Models[0].ID != "gpt-5.4" || resp.Models[1].ID != "claude-sonnet-4.6" {
		t.Fatalf("models = %+v, want fetched credential models", resp.Models)
	}
	if len(resp.Issues) != 0 {
		t.Fatalf("issues = %+v, want none", resp.Issues)
	}
}

func TestHandleFetchModels_AntigravityCredentialUsesLiveModelList(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	oldFetch := fetchAntigravityModels
	t.Cleanup(func() { fetchAntigravityModels = oldFetch })

	var gotToken string
	var gotProjectID string
	fetchAntigravityModels = func(
		_ context.Context,
		accessToken string,
		projectID string,
	) ([]providers.AntigravityModelInfo, error) {
		gotToken = accessToken
		gotProjectID = projectID
		return []providers.AntigravityModelInfo{
			{ID: " gemini-3-pro "},
			{ID: "GEMINI-3-PRO"},
			{ID: "gemini-3-flash"},
			{ID: "gemini-3-exhausted", IsExhausted: true},
			{},
		}, nil
	}
	if err := auth.SetCredential(
		"google-antigravity:work",
		&auth.AuthCredential{
			AccessToken: "google-work-token",
			Provider:    "google-antigravity",
			AuthMethod:  "oauth",
			ProjectID:   "cloud-code-project",
		},
	); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/accounts/models/fetch",
		strings.NewReader(`{"account_ref":"credential:antigravity:work"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if gotToken != "google-work-token" {
		t.Fatalf("token = %q, want stored credential token", gotToken)
	}
	if gotProjectID != "cloud-code-project" {
		t.Fatalf("project ID = %q, want stored project ID", gotProjectID)
	}
	var resp fetchModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if resp.Total != 2 || len(resp.Models) != 2 {
		t.Fatalf("response = %+v, want two unique live models", resp)
	}
	if resp.Models[0] != (upstreamModel{ID: "gemini-3-pro", OwnedBy: "antigravity"}) ||
		resp.Models[1] != (upstreamModel{ID: "gemini-3-flash", OwnedBy: "antigravity"}) {
		t.Fatalf("models = %+v, want normalized live Antigravity models", resp.Models)
	}
	if len(resp.Issues) != 0 {
		t.Fatalf("issues = %+v, want none", resp.Issues)
	}
}

func TestHandleFetchModels_AntigravityCredentialRefreshesBeforeLiveModelList(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	oldFetch := fetchAntigravityModels
	oldRefresh := refreshAntigravityCredential
	t.Cleanup(func() {
		fetchAntigravityModels = oldFetch
		refreshAntigravityCredential = oldRefresh
	})

	var gotToken string
	var gotProjectID string
	fetchAntigravityModels = func(
		_ context.Context,
		accessToken string,
		projectID string,
	) ([]providers.AntigravityModelInfo, error) {
		gotToken = accessToken
		gotProjectID = projectID
		return []providers.AntigravityModelInfo{{ID: "gemini-3-pro"}}, nil
	}
	refreshAntigravityCredential = func(
		credential *auth.AuthCredential,
		cfg auth.OAuthProviderConfig,
	) (*auth.AuthCredential, error) {
		if credential.AccessToken != "expired-token" ||
			credential.RefreshToken != "refresh-token" {
			t.Fatalf("refresh credential = %+v, want stored expired credential", credential)
		}
		if cfg.TokenURL == "" {
			t.Fatal("refresh config is not the Antigravity OAuth config")
		}
		return &auth.AuthCredential{
			AccessToken:  "refreshed-token",
			RefreshToken: "refresh-token",
			ExpiresAt:    time.Now().Add(time.Hour),
			Provider:     "google-antigravity",
			AuthMethod:   "oauth",
		}, nil
	}

	if err := auth.SetCredential(
		"google-antigravity:work",
		&auth.AuthCredential{
			AccessToken:  "expired-token",
			RefreshToken: "refresh-token",
			ExpiresAt:    time.Now().Add(-time.Hour),
			Provider:     "google-antigravity",
			AuthMethod:   "oauth",
			Email:        "work@example.com",
			ProjectID:    "cloud-code-project",
		},
	); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/accounts/models/fetch",
		strings.NewReader(`{"account_ref":"credential:antigravity:work"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if gotToken != "refreshed-token" || gotProjectID != "cloud-code-project" {
		t.Fatalf(
			"live fetch auth = (%q, %q), want refreshed token and preserved project",
			gotToken,
			gotProjectID,
		)
	}
	stored, err := auth.GetCredential("google-antigravity:work")
	if err != nil {
		t.Fatalf("GetCredential() error = %v", err)
	}
	if stored == nil ||
		stored.AccessToken != "refreshed-token" ||
		stored.Email != "work@example.com" ||
		stored.ProjectID != "cloud-code-project" {
		t.Fatalf("stored refreshed credential = %+v", stored)
	}
}

func TestFetchModelsForAccountConfig_AntigravityExhaustedModelsDoNotFallback(t *testing.T) {
	_, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	oldFetch := fetchAntigravityModels
	t.Cleanup(func() { fetchAntigravityModels = oldFetch })
	fetchAntigravityModels = func(
		context.Context,
		string,
		string,
	) ([]providers.AntigravityModelInfo, error) {
		return []providers.AntigravityModelInfo{{
			ID:          "gemini-3-flash",
			IsExhausted: true,
		}}, nil
	}
	if err := auth.SetCredential(
		"google-antigravity:work",
		&auth.AuthCredential{
			AccessToken: "google-work-token",
			Provider:    "google-antigravity",
			AuthMethod:  "oauth",
			ProjectID:   "cloud-code-project",
		},
	); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	models, err := fetchModelsForAccountConfig(t.Context(), &config.ModelConfig{
		ModelName:    "antigravity-work",
		Provider:     "antigravity",
		Model:        "gemini-3-flash",
		AuthMethod:   "oauth",
		CredentialID: "google-antigravity:work",
		Enabled:      true,
	})
	if err == nil || !strings.Contains(err.Error(), "no available models") {
		t.Fatalf("fetchModelsForAccountConfig() error = %v, want no available models", err)
	}
	if len(models) != 0 {
		t.Fatalf("models = %+v, want no exhausted or static fallback models", models)
	}
}

func TestFetchModelsForAccountConfig_AntigravityCredentialValidationUsesFallback(t *testing.T) {
	tests := []struct {
		name       string
		credential *auth.AuthCredential
		wantError  string
	}{
		{
			name: "provider mismatch",
			credential: &auth.AuthCredential{
				AccessToken: "wrong-provider-token",
				Provider:    "openai",
				AuthMethod:  "oauth",
				ProjectID:   "cloud-code-project",
			},
			wantError: "not antigravity",
		},
		{
			name: "missing access token",
			credential: &auth.AuthCredential{
				Provider:   "google-antigravity",
				AuthMethod: "oauth",
				ProjectID:  "cloud-code-project",
			},
			wantError: "has no access token",
		},
		{
			name: "missing project ID",
			credential: &auth.AuthCredential{
				AccessToken: "google-work-token",
				Provider:    "google-antigravity",
				AuthMethod:  "oauth",
			},
			wantError: "has no project ID",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, cleanup := setupOAuthTestEnv(t)
			defer cleanup()

			oldFetch := fetchAntigravityModels
			t.Cleanup(func() { fetchAntigravityModels = oldFetch })
			fetchCalled := false
			fetchAntigravityModels = func(
				context.Context,
				string,
				string,
			) ([]providers.AntigravityModelInfo, error) {
				fetchCalled = true
				return nil, nil
			}
			if err := auth.SetCredential(
				"google-antigravity:work",
				tt.credential,
			); err != nil {
				t.Fatalf("SetCredential() error = %v", err)
			}

			models, err := fetchModelsForAccountConfig(t.Context(), &config.ModelConfig{
				ModelName:    "antigravity-work",
				Provider:     "antigravity",
				Model:        "gemini-3-flash",
				AuthMethod:   "oauth",
				CredentialID: "google-antigravity:work",
				Enabled:      true,
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("fetchModelsForAccountConfig() error = %v, want %q", err, tt.wantError)
			}
			if len(models) != 1 ||
				models[0] != (upstreamModel{ID: "gemini-3-flash", OwnedBy: "antigravity"}) {
				t.Fatalf("models = %+v, want static Antigravity fallback", models)
			}
			if fetchCalled {
				t.Fatal("live Antigravity fetch called with invalid credential")
			}
		})
	}
}

func TestHandleFetchModels_AntigravityLiveFailureReturnsFallbackIssue(t *testing.T) {
	configPath, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	oldFetch := fetchAntigravityModels
	t.Cleanup(func() { fetchAntigravityModels = oldFetch })
	fetchAntigravityModels = func(
		context.Context,
		string,
		string,
	) ([]providers.AntigravityModelInfo, error) {
		return nil, fmt.Errorf("quota service unavailable")
	}
	if err := auth.SetCredential(
		"google-antigravity:work",
		&auth.AuthCredential{
			AccessToken: "google-work-token",
			Provider:    "google-antigravity",
			AuthMethod:  "oauth",
			ProjectID:   "cloud-code-project",
		},
	); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName:    "antigravity-work",
		Provider:     "antigravity",
		Model:        "gemini-3-flash",
		AuthMethod:   "oauth",
		CredentialID: "google-antigravity:work",
		Enabled:      true,
	}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/accounts/models/fetch",
		strings.NewReader(`{"account_ref":"antigravity-work"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp fetchModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if resp.Total != 1 ||
		len(resp.Models) != 1 ||
		resp.Models[0] != (upstreamModel{ID: "gemini-3-flash", OwnedBy: "antigravity"}) {
		t.Fatalf("models = %+v, want static Antigravity fallback", resp.Models)
	}
	if len(resp.Issues) != 1 ||
		resp.Issues[0].AccountRef != "antigravity-work" ||
		!strings.Contains(resp.Issues[0].Error, "quota service unavailable") {
		t.Fatalf("issues = %+v, want live fetch failure", resp.Issues)
	}
}

func TestResolveAccountModelConfigNormalizesNamedCredentialAliases(t *testing.T) {
	_, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	for credentialID, credential := range map[string]*auth.AuthCredential{
		"github-copilot:work": {
			AccessToken: "gho-work-token",
			Provider:    "github-copilot",
			AuthMethod:  "token",
		},
		"google-antigravity:work": {
			AccessToken: "google-work-token",
			Provider:    "google-antigravity",
			AuthMethod:  "oauth",
		},
	} {
		if err := auth.SetCredential(credentialID, credential); err != nil {
			t.Fatalf("SetCredential(%q) error = %v", credentialID, err)
		}
	}

	tests := []struct {
		accountRef       string
		wantProvider     string
		wantCredentialID string
	}{
		{
			accountRef:       "credential:copilot:work",
			wantProvider:     "github-copilot",
			wantCredentialID: "github-copilot:work",
		},
		{
			accountRef:       "credential:antigravity:work",
			wantProvider:     "antigravity",
			wantCredentialID: "google-antigravity:work",
		},
	}
	for _, tt := range tests {
		t.Run(tt.accountRef, func(t *testing.T) {
			if !credentialAccountAvailable(tt.accountRef) {
				t.Fatal("credentialAccountAvailable() = false, want true")
			}
			got, err := resolveAccountModelConfig(config.DefaultConfig(), tt.accountRef)
			if err != nil {
				t.Fatalf("resolveAccountModelConfig() error = %v", err)
			}
			if got.Provider != tt.wantProvider ||
				got.CredentialID != tt.wantCredentialID ||
				got.Model != "" {
				t.Fatalf(
					"model config = %#v, want provider=%q credential=%q and no synthesized model",
					got,
					tt.wantProvider,
					tt.wantCredentialID,
				)
			}
		})
	}
}

func TestResolveAccountModelConfigRejectsCredentialProviderMismatch(t *testing.T) {
	_, cleanup := setupOAuthTestEnv(t)
	defer cleanup()

	if err := auth.SetCredential("openai:work", &auth.AuthCredential{
		AccessToken: "anthropic-token",
		Provider:    "anthropic",
		AuthMethod:  "token",
	}); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	accountRef := "credential:openai:work"
	if credentialAccountAvailable(accountRef) {
		t.Fatal("credentialAccountAvailable() = true for mismatched provider")
	}
	if _, err := resolveAccountModelConfig(config.DefaultConfig(), accountRef); err == nil ||
		!strings.Contains(err.Error(), `belongs to provider "anthropic"`) {
		t.Fatalf("resolveAccountModelConfig() error = %v, want provider mismatch", err)
	}
	if _, err := resolveOpenAICodexCredential("openai:work"); err == nil ||
		!strings.Contains(err.Error(), "not openai") {
		t.Fatalf("resolveOpenAICodexCredential() error = %v, want provider mismatch", err)
	}
}

func TestResolveAccountModelConfigRejectsDisabledAlias(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "disabled-account",
		Provider:  "openai",
		Model:     "gpt-5.4",
		APIKeys:   config.SimpleSecureStrings("sk-disabled"),
		Enabled:   false,
	}}

	if _, err := resolveAccountModelConfig(cfg, "disabled-account"); err == nil ||
		!strings.Contains(err.Error(), "disabled") {
		t.Fatalf("resolveAccountModelConfig() error = %v, want disabled account", err)
	}
}

func TestHandleFetchModels_AccountRouterIntersectsReachableAliases(t *testing.T) {
	var mu sync.Mutex
	requests := map[string]int{}
	authHeaders := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests[r.URL.Path]++
		authHeaders[r.URL.Path] = r.Header.Get("Authorization")
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/a/models":
			fmt.Fprint(w, `{"data":[{"id":" Shared "},{"id":"alpha"},{"id":"shared"}]}`)
		case "/b/models":
			fmt.Fprint(w, `{"data":[{"id":"SHARED"},{"id":"beta"}]}`)
		case "/failed/models":
			http.Error(w, "account unavailable", http.StatusServiceUnavailable)
		case "/metrics/models", "/orphan/models":
			fmt.Fprint(w, `{"data":[{"id":"not-shared"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	tmp := t.TempDir()
	t.Setenv("PICOCLAW_HOME", filepath.Join(tmp, ".picoclaw"))
	threshold := 0.0
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "account-a",
			Provider:  "openai",
			Model:     "shared",
			APIBase:   srv.URL + "/a",
			APIKeys:   config.SimpleSecureStrings("sk-a"),
			Enabled:   true,
		},
		{
			ModelName: "account-b",
			Provider:  "openai",
			Model:     "shared",
			APIBase:   srv.URL + "/b",
			APIKeys:   config.SimpleSecureStrings("sk-b"),
			Enabled:   true,
		},
		{
			ModelName: "account-fallback",
			Provider:  "openai",
			Model:     "shared",
			APIBase:   srv.URL + "/failed",
			APIKeys:   config.SimpleSecureStrings("sk-failed"),
			Enabled:   true,
		},
		{
			ModelName: "metrics-only",
			Provider:  "openai",
			Model:     "not-shared",
			APIBase:   srv.URL + "/metrics",
			APIKeys:   config.SimpleSecureStrings("sk-metrics"),
			Enabled:   true,
		},
		{
			ModelName: "orphan",
			Provider:  "openai",
			Model:     "not-shared",
			APIBase:   srv.URL + "/orphan",
			APIKeys:   config.SimpleSecureStrings("sk-orphan"),
			Enabled:   true,
		},
	}
	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name:    "router-main",
		Enabled: true,
		Entry:   "branch",
		Blocks: []config.AccountRouterBlock{
			{
				ID:   "branch",
				Type: config.AccountRouterBlockTypeBranch,
				Condition: &config.AccountRouterCondition{
					Left: config.AccountRouterExpression{
						Account: "metrics-only",
						Metric:  "rpm",
					},
					Operator: config.AccountRouterBranchOpGT,
					Right:    config.AccountRouterExpression{Value: &threshold},
				},
				Then:     "account-a",
				Else:     "account-b",
				Fallback: "account-fallback",
			},
			{
				ID:      "account-a",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: "account-a",
			},
			{
				ID:      "account-b",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: "account-b",
			},
			{
				ID:      "account-fallback",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: "account-fallback",
			},
			{
				ID:      "orphan",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: "orphan",
			},
		},
	}}
	configPath := filepath.Join(tmp, "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/accounts/models/fetch",
		strings.NewReader(`{"account_ref":"router-main"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp fetchModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if resp.Total != 1 || len(resp.Models) != 1 || resp.Models[0].ID != "Shared" {
		t.Fatalf("models = %+v, want case-insensitive intersection [Shared]", resp.Models)
	}
	if len(resp.Issues) != 1 ||
		resp.Issues[0].AccountRef != "account-fallback" ||
		!strings.Contains(resp.Issues[0].Error, "503") {
		t.Fatalf("issues = %+v, want partial account-fallback failure", resp.Issues)
	}

	mu.Lock()
	defer mu.Unlock()
	for path, wantAuth := range map[string]string{
		"/a/models":      "Bearer sk-a",
		"/b/models":      "Bearer sk-b",
		"/failed/models": "Bearer sk-failed",
	} {
		if requests[path] != 1 {
			t.Errorf("requests[%q] = %d, want 1", path, requests[path])
		}
		if authHeaders[path] != wantAuth {
			t.Errorf("Authorization for %q = %q, want %q", path, authHeaders[path], wantAuth)
		}
	}
	if requests["/metrics/models"] != 0 {
		t.Errorf("metric-only account fetched %d times, want 0", requests["/metrics/models"])
	}
	if requests["/orphan/models"] != 0 {
		t.Errorf("orphan account fetched %d times, want 0", requests["/orphan/models"])
	}
}

func TestHandleFetchModels_AccountRefUsesConfiguredStaticFallback(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("PICOCLAW_HOME", filepath.Join(tmp, ".picoclaw"))
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "bedrock-work",
		Provider:  "bedrock",
		Model:     "anthropic.claude-sonnet-4-6",
		Enabled:   true,
	}}
	configPath := filepath.Join(tmp, "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/accounts/models/fetch",
		strings.NewReader(`{"account_ref":"bedrock-work"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp fetchModelsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if resp.Total != 1 ||
		len(resp.Models) != 1 ||
		resp.Models[0].ID != "anthropic.claude-sonnet-4-6" {
		t.Fatalf("response = %+v, want configured static model", resp)
	}
	if len(resp.Issues) != 0 {
		t.Fatalf("issues = %+v, want none", resp.Issues)
	}
}
