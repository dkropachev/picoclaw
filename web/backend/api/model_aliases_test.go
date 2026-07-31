package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func modelAliasAPIConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "openai-work",
		Provider:  "openai",
		Model:     "gpt-5.4",
		APIKeys:   config.SimpleSecureStrings("sk-test"),
		Enabled:   true,
	}}
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "coding",
		Model: "gpt-5.4",
	}}
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	return path
}

func serveModelAliasRequest(
	t *testing.T,
	configPath string,
	method string,
	path string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	if method == http.MethodDelete {
		path = modelMutationURLWithCurrentRevision(t, configPath, path)
	}
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(recorder, request)
	return recorder
}

func modelMutationURLWithCurrentRevision(
	t *testing.T,
	configPath string,
	path string,
) string {
	t.Helper()
	revision, err := config.ConfigRevision(configPath)
	if err != nil {
		t.Fatalf("ConfigRevision() error = %v", err)
	}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "revision=" + url.QueryEscape(revision)
}

func TestListModelsIncludesAliasesAndSplitDefaultSelection(t *testing.T) {
	configPath := modelAliasAPIConfig(t)
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agents.Defaults.AccountRef = "openai-work"
	cfg.Agents.Defaults.ModelName = "coding"
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	recorder := serveModelAliasRequest(
		t,
		configPath,
		http.MethodGet,
		"/api/accounts/models",
		"",
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		DefaultAccountRef string                    `json:"default_account_ref"`
		DefaultModel      string                    `json:"default_model"`
		Revision          string                    `json:"revision"`
		ModelAliases      []config.ModelAliasConfig `json:"model_aliases"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.DefaultAccountRef != "openai-work" || response.DefaultModel != "coding" {
		t.Fatalf("default selection = %#v", response)
	}
	if !strings.HasPrefix(response.Revision, "sha256:") {
		t.Fatalf("revision = %q, want opaque sha256 revision", response.Revision)
	}
	if len(response.ModelAliases) != 1 ||
		response.ModelAliases[0].Name != "coding" ||
		response.ModelAliases[0].Model != "gpt-5.4" {
		t.Fatalf("model aliases = %#v", response.ModelAliases)
	}
}

func TestSetDefaultModelRequiresAtomicAccountAndAlias(t *testing.T) {
	configPath := modelAliasAPIConfig(t)

	missingAccount := serveModelAliasRequest(
		t,
		configPath,
		http.MethodPost,
		"/api/accounts/models/default",
		`{"model_name":"coding"}`,
	)
	if missingAccount.Code != http.StatusBadRequest {
		t.Fatalf("missing account status = %d", missingAccount.Code)
	}

	missingAlias := serveModelAliasRequest(
		t,
		configPath,
		http.MethodPost,
		"/api/accounts/models/default",
		`{"account_ref":"openai-work"}`,
	)
	if missingAlias.Code != http.StatusBadRequest ||
		missingAlias.Body.String() != "no model configured\n" {
		t.Fatalf("missing alias = %d %q", missingAlias.Code, missingAlias.Body.String())
	}

	success := serveModelAliasRequest(
		t,
		configPath,
		http.MethodPost,
		"/api/accounts/models/default",
		`{"account_ref":"openai-work","model_name":"coding"}`,
	)
	if success.Code != http.StatusOK {
		t.Fatalf("success status = %d, body=%s", success.Code, success.Body.String())
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agents.Defaults.AccountRef != "openai-work" ||
		cfg.Agents.Defaults.ModelName != "coding" {
		t.Fatalf("saved defaults = %#v", cfg.Agents.Defaults)
	}
}

func TestModelAliasCRUDValidatesConcreteAccountOverrides(t *testing.T) {
	configPath := modelAliasAPIConfig(t)

	add := serveModelAliasRequest(
		t,
		configPath,
		http.MethodPost,
		"/api/accounts/model-aliases",
		`{
			"name":"analysis",
			"model":"gpt-5.4-mini",
			"account_overrides":{"openai-work":"gpt-5.4"}
		}`,
	)
	if add.Code != http.StatusOK {
		t.Fatalf("add status = %d, body=%s", add.Code, add.Body.String())
	}

	update := serveModelAliasRequest(
		t,
		configPath,
		http.MethodPut,
		modelMutationURLWithCurrentRevision(
			t,
			configPath,
			"/api/accounts/model-aliases/1",
		),
		`{
			"name":"analysis",
			"model":"gpt-5.4-mini",
			"account_overrides":{"openai-work":"gpt-5.4-nano"}
		}`,
	)
	if update.Code != http.StatusOK {
		t.Fatalf("update status = %d, body=%s", update.Code, update.Body.String())
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ModelAliases[1].AccountOverrides["openai-work"]; got != "gpt-5.4-nano" {
		t.Fatalf("override = %q", got)
	}

	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name:    "router-1",
		Enabled: true,
		Entry:   "main",
		Blocks: []config.AccountRouterBlock{{
			ID:      "main",
			Type:    config.AccountRouterBlockTypeAccount,
			Account: "openai-work",
		}},
	}}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	routerOverride := serveModelAliasRequest(
		t,
		configPath,
		http.MethodPut,
		modelMutationURLWithCurrentRevision(
			t,
			configPath,
			"/api/accounts/model-aliases/1",
		),
		`{
			"name":"analysis",
			"model":"gpt-5.4-mini",
			"account_overrides":{"router-1":"gpt-5.4"}
		}`,
	)
	if routerOverride.Code != http.StatusBadRequest {
		t.Fatalf("router override status = %d, body=%s", routerOverride.Code, routerOverride.Body.String())
	}

	deleteAlias := serveModelAliasRequest(
		t,
		configPath,
		http.MethodDelete,
		"/api/accounts/model-aliases/1",
		"",
	)
	if deleteAlias.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body=%s", deleteAlias.Code, deleteAlias.Body.String())
	}
}

func TestDeleteModelAliasRejectsSubscriptionEquivalentReference(t *testing.T) {
	configPath := modelAliasAPIConfig(t)
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
		ModelName:                   "unused-metadata",
		Provider:                    "openai",
		Model:                       "unrelated-model",
		SubscriptionEquivalentModel: "coding",
	})
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	recorder := serveModelAliasRequest(
		t,
		configPath,
		http.MethodDelete,
		"/api/accounts/model-aliases/0",
		"",
	)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(
		recorder.Body.String(),
		"model_list[1].subscription_equivalent_model",
	) {
		t.Fatalf("body = %q, want subscription equivalent reference", recorder.Body.String())
	}
}

func TestSaveModelConfigMutationRejectsStaleRevision(t *testing.T) {
	configPath := modelAliasAPIConfig(t)

	staleConfig, staleRevision, err := config.LoadConfigForUpdateSnapshot(configPath)
	if err != nil {
		t.Fatal(err)
	}
	concurrentConfig, concurrentRevision, err := config.LoadConfigForUpdateSnapshot(configPath)
	if err != nil {
		t.Fatal(err)
	}
	concurrentConfig.Agents.Defaults.Workspace = "/concurrent-change"
	if _, saveErr := config.SaveConfigIfRevision(
		configPath,
		concurrentConfig,
		concurrentRevision,
	); saveErr != nil {
		t.Fatal(saveErr)
	}

	recorder := httptest.NewRecorder()
	if saveModelConfigMutation(recorder, configPath, staleConfig, staleRevision) {
		t.Fatal("stale model config mutation unexpectedly succeeded")
	}
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}

	saved, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Agents.Defaults.Workspace != "/concurrent-change" {
		t.Fatalf("concurrent change was lost: %#v", saved.Agents.Defaults)
	}
}

func TestDeleteModelAliasRejectsWebSearchReferences(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*config.Config)
		wantPath  string
	}{
		{
			name: "gemini",
			configure: func(cfg *config.Config) {
				cfg.Tools.Web.Gemini.ModelAlias = "coding"
			},
			wantPath: "tools.web.gemini.model_alias",
		},
		{
			name: "perplexity",
			configure: func(cfg *config.Config) {
				cfg.Tools.Web.Perplexity.ModelAlias = "coding"
			},
			wantPath: "tools.web.perplexity.model_alias",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath := modelAliasAPIConfig(t)
			cfg, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatalf("LoadConfig() error = %v", err)
			}
			tt.configure(cfg)
			if err := config.SaveConfig(configPath, cfg); err != nil {
				t.Fatalf("SaveConfig() error = %v", err)
			}

			recorder := serveModelAliasRequest(
				t,
				configPath,
				http.MethodDelete,
				"/api/accounts/model-aliases/0",
				"",
			)
			if recorder.Code != http.StatusConflict {
				t.Fatalf(
					"status = %d, want %d; body=%s",
					recorder.Code,
					http.StatusConflict,
					recorder.Body.String(),
				)
			}
			if !strings.Contains(recorder.Body.String(), tt.wantPath) {
				t.Fatalf("body = %q, want %s", recorder.Body.String(), tt.wantPath)
			}
		})
	}
}

func TestConcurrentModelAliasAddsPreserveBothMutations(t *testing.T) {
	configPath := modelAliasAPIConfig(t)
	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	bodies := []string{
		`{"name":"fast","model":"gpt-5.4-mini"}`,
		`{"name":"deep","model":"gpt-5.4"}`,
	}
	recorders := make([]*httptest.ResponseRecorder, len(bodies))
	var wg sync.WaitGroup
	for i := range bodies {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			recorders[i] = httptest.NewRecorder()
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/accounts/model-aliases",
				bytes.NewBufferString(bodies[i]),
			)
			request.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(recorders[i], request)
		}(i)
	}
	wg.Wait()

	for i, recorder := range recorders {
		if recorder.Code != http.StatusOK {
			t.Fatalf(
				"request %d status = %d, body=%s",
				i,
				recorder.Code,
				recorder.Body.String(),
			)
		}
	}
	saved, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fast", "deep"} {
		if _, err := saved.GetModelAlias(name); err != nil {
			t.Fatalf("concurrent alias %q was lost: %v", name, err)
		}
	}
}

func TestUpdateModelAliasRejectsStaleSameNameConcurrentEdit(t *testing.T) {
	configPath := modelAliasAPIConfig(t)
	staleRevision, err := config.ConfigRevision(configPath)
	if err != nil {
		t.Fatalf("ConfigRevision() error = %v", err)
	}

	concurrent, revision, err := config.LoadConfigForUpdateSnapshot(configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdateSnapshot() error = %v", err)
	}
	concurrent.ModelAliases[0].Model = "openai/gpt-4.1"
	if _, saveErr := config.SaveConfigIfRevision(
		configPath,
		concurrent,
		revision,
	); saveErr != nil {
		t.Fatalf("SaveConfigIfRevision() error = %v", saveErr)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile() before request error = %v", err)
	}

	recorder := serveModelAliasRequest(
		t,
		configPath,
		http.MethodPut,
		"/api/accounts/model-aliases/0?revision="+url.QueryEscape(staleRevision),
		`{"name":"coding","model":"openai/gpt-4o-mini"}`,
	)

	if recorder.Code != http.StatusConflict {
		t.Fatalf(
			"status = %d, want %d; body=%s",
			recorder.Code,
			http.StatusConflict,
			recorder.Body.String(),
		)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile() after request error = %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("stale alias update changed config bytes")
	}
	saved, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got := saved.ModelAliases[0].Model; got != "openai/gpt-4.1" {
		t.Fatalf("alias model = %q, want concurrent value", got)
	}
}

func TestDeleteModelAliasRejectsStaleListRevisionBeforeUsingIndex(t *testing.T) {
	configPath := modelAliasAPIConfig(t)
	staleRevision, err := config.ConfigRevision(configPath)
	if err != nil {
		t.Fatalf("ConfigRevision() error = %v", err)
	}

	concurrent, revision, err := config.LoadConfigForUpdateSnapshot(configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdateSnapshot() error = %v", err)
	}
	concurrent.ModelAliases = append(
		[]config.ModelAliasConfig{{Name: "new-first", Model: "gpt-4.1"}},
		concurrent.ModelAliases...,
	)
	if _, saveErr := config.SaveConfigIfRevision(
		configPath,
		concurrent,
		revision,
	); saveErr != nil {
		t.Fatalf("SaveConfigIfRevision() error = %v", saveErr)
	}

	handler := NewHandler(configPath)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodDelete,
		"/api/accounts/model-aliases/0?revision="+url.QueryEscape(staleRevision),
		nil,
	)
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Fatalf(
			"status = %d, want %d; body=%s",
			recorder.Code,
			http.StatusConflict,
			recorder.Body.String(),
		)
	}
	saved, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	for _, name := range []string{"new-first", "coding"} {
		if _, err := saved.GetModelAlias(name); err != nil {
			t.Fatalf("alias %q was deleted by stale index: %v", name, err)
		}
	}
}
