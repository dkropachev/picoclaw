package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func modelReferenceSafetyMux(t *testing.T, cfg *config.Config) (string, *http.ServeMux) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	mux := http.NewServeMux()
	NewHandler(configPath).RegisterRoutes(mux)
	return configPath, mux
}

func loadedModelIndex(t *testing.T, configPath, name string) int {
	t.Helper()
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	for i, model := range cfg.ModelList {
		if model != nil && model.ModelName == name {
			return i
		}
	}
	t.Fatalf("model entry %q not found", name)
	return -1
}

func TestDeleteAccountRejectsAliasOverrideReference(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{ModelName: "openai-old", Provider: "openai", Enabled: true},
		{ModelName: "openai-current", Provider: "openai", Enabled: true},
	}
	cfg.ModelAliases = []config.ModelAliasConfig{{
		Name:  "coding",
		Model: "gpt-5.4",
		AccountOverrides: map[string]string{
			"openai-old": "gpt-4.1",
		},
	}}
	cfg.Agents.Defaults.AccountRef = "openai-current"
	cfg.Agents.Defaults.ModelName = "coding"
	configPath, mux := modelReferenceSafetyMux(t, cfg)

	index := loadedModelIndex(t, configPath, "openai-old")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodDelete,
		modelMutationURLWithCurrentRevision(
			t,
			configPath,
			"/api/accounts/models/"+strconv.Itoa(index),
		),
		nil,
	)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "account_overrides") {
		t.Fatalf("body = %q, want account_overrides reference", rec.Body.String())
	}
	if got := loadedModelIndex(t, configPath, "openai-old"); got < 0 {
		t.Fatal("referenced account was deleted")
	}
}

func TestDeleteModelRouterRejectsDefaultSelectorReference(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "openai-current",
		Provider:  "openai",
		Enabled:   true,
	}}
	cfg.ModelAliases = []config.ModelAliasConfig{{Name: "coding", Model: "gpt-5.4"}}
	cfg.ModelRouters = []config.ModelRouterConfig{{
		Name:    "task-router",
		Enabled: true,
		Entry:   "coding",
		Blocks: []config.ModelRouterBlock{{
			ID:    "coding",
			Type:  config.ModelRouterBlockTypeModel,
			Model: "coding",
		}},
	}}
	cfg.Agents.Defaults.AccountRef = "openai-current"
	cfg.Agents.Defaults.ModelName = "task-router"
	configPath, mux := modelReferenceSafetyMux(t, cfg)

	index := loadedModelIndex(t, configPath, "task-router")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodDelete,
		modelMutationURLWithCurrentRevision(
			t,
			configPath,
			"/api/accounts/models/"+strconv.Itoa(index),
		),
		nil,
	)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "agents.defaults.model_name") {
		t.Fatalf("body = %q, want default model selector reference", rec.Body.String())
	}
	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() after rejected delete error = %v", err)
	}
	if findModelRouterIndex(loaded, "task-router") < 0 {
		t.Fatal("referenced model router was deleted")
	}
}

func TestUpdateRejectsAccountAndRouterRename(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *config.Config
		entry       string
		requestBody string
	}{
		{
			name: "concrete account",
			cfg: func() *config.Config {
				cfg := config.DefaultConfig()
				cfg.ModelList = []*config.ModelConfig{{
					ModelName: "openai-work",
					Provider:  "openai",
					Enabled:   true,
				}}
				return cfg
			}(),
			entry:       "openai-work",
			requestBody: `{"model_name":"renamed","provider":"openai","enabled":true}`,
		},
		{
			name: "model router",
			cfg: func() *config.Config {
				cfg := config.DefaultConfig()
				cfg.ModelAliases = []config.ModelAliasConfig{{
					Name:  "coding",
					Model: "gpt-5.4",
				}}
				cfg.ModelRouters = []config.ModelRouterConfig{{
					Name:    "task-router",
					Enabled: true,
					Entry:   "coding",
					Blocks: []config.ModelRouterBlock{{
						ID:    "coding",
						Type:  config.ModelRouterBlockTypeModel,
						Model: "coding",
					}},
				}}
				return cfg
			}(),
			entry: "task-router",
			requestBody: `{
				"model_name":"renamed",
				"provider":"model-router",
				"model":"renamed",
				"enabled":true,
				"model_router":{
					"name":"renamed",
					"enabled":true,
					"entry":"coding",
					"blocks":[{"id":"coding","type":"model","model":"coding"}]
				}
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configPath, mux := modelReferenceSafetyMux(t, tt.cfg)
			index := loadedModelIndex(t, configPath, tt.entry)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(
				http.MethodPut,
				modelMutationURLWithCurrentRevision(
					t,
					configPath,
					"/api/accounts/models/"+strconv.Itoa(index),
				),
				bytes.NewBufferString(tt.requestBody),
			)
			req.Header.Set("Content-Type", "application/json")
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "names are immutable") {
				t.Fatalf("body = %q, want immutable-name error", rec.Body.String())
			}
			if got := loadedModelIndex(t, configPath, tt.entry); got < 0 {
				t.Fatalf("entry %q disappeared after rejected rename", tt.entry)
			}
		})
	}
}
