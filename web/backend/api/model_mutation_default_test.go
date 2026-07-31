package api

import (
	"bytes"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func atomicDefaultMutationConfig(t *testing.T) string {
	t.Helper()
	configPath := modelAliasAPIConfig(t)
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.ModelAliases[0].Model = "openai/gpt-5.4"
	cfg.Agents.Defaults.AccountRef = "openai-work"
	cfg.Agents.Defaults.ModelName = "coding"
	if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
		t.Fatalf("SaveConfig() error = %v", saveErr)
	}
	return configPath
}

func readConfigBytes(t *testing.T, configPath string) []byte {
	t.Helper()
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return body
}

func findLoadedModel(t *testing.T, cfg *config.Config, name string) *config.ModelConfig {
	t.Helper()
	for _, model := range cfg.ModelList {
		if model != nil && model.ModelName == name {
			return model
		}
	}
	t.Fatalf("model %q not found", name)
	return nil
}

func TestAddModelSetAsDefaultRejectsMissingModelWithoutPersisting(t *testing.T) {
	configPath := atomicDefaultMutationConfig(t)
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Agents.Defaults.ModelName = ""
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	before := readConfigBytes(t, configPath)

	recorder := serveModelAliasRequest(
		t,
		configPath,
		http.MethodPost,
		"/api/accounts/models",
		`{
			"model_name":"openai-second",
			"provider":"openai",
			"model":"gpt-5.4-mini",
			"enabled":true,
			"set_as_default":true
		}`,
	)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), config.ErrNoModelConfigured.Error()) {
		t.Fatalf("body = %q, want no-model error", recorder.Body.String())
	}
	if after := readConfigBytes(t, configPath); !bytes.Equal(after, before) {
		t.Fatal("rejected add changed the persisted config")
	}
}

func TestAddModelSetAsDefaultRejectsIncompatiblePairWithoutPersisting(t *testing.T) {
	configPath := atomicDefaultMutationConfig(t)
	before := readConfigBytes(t, configPath)

	recorder := serveModelAliasRequest(
		t,
		configPath,
		http.MethodPost,
		"/api/accounts/models",
		`{
			"model_name":"anthropic-work",
			"provider":"anthropic",
			"model":"claude-sonnet-4.6",
			"enabled":true,
			"set_as_default":true
		}`,
	)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "does not match account provider") {
		t.Fatalf("body = %q, want provider compatibility error", recorder.Body.String())
	}
	if after := readConfigBytes(t, configPath); !bytes.Equal(after, before) {
		t.Fatal("rejected add changed the persisted config")
	}
}

func TestAddModelRouterSetAsDefaultRejectsMissingAccountWithoutPersisting(t *testing.T) {
	configPath := atomicDefaultMutationConfig(t)
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	cfg.Agents.Defaults.AccountRef = ""
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	before := readConfigBytes(t, configPath)

	recorder := serveModelAliasRequest(
		t,
		configPath,
		http.MethodPost,
		"/api/accounts/models",
		`{
			"model_name":"task-router",
			"provider":"model-router",
			"model":"task-router",
			"enabled":true,
			"set_as_default":true,
			"model_router":{
				"name":"task-router",
				"enabled":true,
				"entry":"coding",
				"blocks":[
					{"id":"coding","type":"model","model":"coding"}
				]
			}
		}`,
	)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "no account configured") {
		t.Fatalf("body = %q, want no-account error", recorder.Body.String())
	}
	if after := readConfigBytes(t, configPath); !bytes.Equal(after, before) {
		t.Fatal("rejected model-router add changed the persisted config")
	}
}

func TestAddModelSetAsDefaultPersistsAccountAndSelectionTogether(t *testing.T) {
	configPath := atomicDefaultMutationConfig(t)

	recorder := serveModelAliasRequest(
		t,
		configPath,
		http.MethodPost,
		"/api/accounts/models",
		`{
			"model_name":"openai-second",
			"provider":"openai",
			"model":"gpt-5.4-mini",
			"enabled":true,
			"set_as_default":true
		}`,
	)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if model := findLoadedModel(t, cfg, "openai-second"); model.Provider != "openai" {
		t.Fatalf("saved provider = %q, want openai", model.Provider)
	}
	if cfg.Agents.Defaults.AccountRef != "openai-second" ||
		cfg.Agents.Defaults.ModelName != "coding" {
		t.Fatalf("saved defaults = %#v", cfg.Agents.Defaults)
	}
}

func TestAddRoutersSetAppropriateDefaultHalfAtomically(t *testing.T) {
	t.Run("account router", func(t *testing.T) {
		configPath := atomicDefaultMutationConfig(t)

		recorder := serveModelAliasRequest(
			t,
			configPath,
			http.MethodPost,
			"/api/accounts/models",
			`{
				"model_name":"router-main",
				"provider":"router",
				"model":"router-main",
				"set_as_default":true,
				"router":{
					"enabled":true,
					"entry":"account",
					"blocks":[
						{"id":"account","type":"account","account":"openai-work"}
					]
				}
			}`,
		)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
		}
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			t.Fatalf("LoadConfig() error = %v", err)
		}
		if findAccountRouterIndex(cfg, "router-main") < 0 {
			t.Fatal("account router was not persisted")
		}
		if cfg.Agents.Defaults.AccountRef != "router-main" ||
			cfg.Agents.Defaults.ModelName != "coding" {
			t.Fatalf("saved defaults = %#v", cfg.Agents.Defaults)
		}
	})

	t.Run("model router", func(t *testing.T) {
		configPath := atomicDefaultMutationConfig(t)

		recorder := serveModelAliasRequest(
			t,
			configPath,
			http.MethodPost,
			"/api/accounts/models",
			`{
				"model_name":"task-router",
				"provider":"model-router",
				"model":"task-router",
				"enabled":true,
				"set_as_default":true,
				"model_router":{
					"name":"task-router",
					"enabled":true,
					"entry":"coding",
					"blocks":[
						{"id":"coding","type":"model","model":"coding"}
					]
				}
			}`,
		)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
		}
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			t.Fatalf("LoadConfig() error = %v", err)
		}
		if findModelRouterIndex(cfg, "task-router") < 0 {
			t.Fatal("model router was not persisted")
		}
		if cfg.Agents.Defaults.AccountRef != "openai-work" ||
			cfg.Agents.Defaults.ModelName != "task-router" {
			t.Fatalf("saved defaults = %#v", cfg.Agents.Defaults)
		}
	})
}

func TestUpdateModelSetAsDefaultIsAtomic(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		configPath := atomicDefaultMutationConfig(t)
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			t.Fatalf("LoadConfig() error = %v", err)
		}
		cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
			ModelName: "openai-second",
			Provider:  "openai",
			Model:     "gpt-5.4-mini",
			Enabled:   true,
		})
		if saveErr := config.SaveConfig(configPath, cfg); saveErr != nil {
			t.Fatalf("SaveConfig() error = %v", saveErr)
		}
		index := loadedModelIndex(t, configPath, "openai-second")

		recorder := serveModelAliasRequest(
			t,
			configPath,
			http.MethodPut,
			modelMutationURLWithCurrentRevision(
				t,
				configPath,
				"/api/accounts/models/"+strconv.Itoa(index),
			),
			`{
				"model_name":"openai-second",
				"provider":"openai",
				"model":"gpt-5.4-mini",
				"proxy":"http://proxy.internal:8080",
				"enabled":true,
				"set_as_default":true
			}`,
		)

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
		}
		updated, err := config.LoadConfig(configPath)
		if err != nil {
			t.Fatalf("LoadConfig() error = %v", err)
		}
		if updated.Agents.Defaults.AccountRef != "openai-second" {
			t.Fatalf(
				"default account = %q, want openai-second",
				updated.Agents.Defaults.AccountRef,
			)
		}
		if model := findLoadedModel(t, updated, "openai-second"); model.Proxy !=
			"http://proxy.internal:8080" {
			t.Fatalf("saved proxy = %q", model.Proxy)
		}
	})

	t.Run("failure leaves edit and default unchanged", func(t *testing.T) {
		configPath := atomicDefaultMutationConfig(t)
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			t.Fatalf("LoadConfig() error = %v", err)
		}
		cfg.ModelList = append(cfg.ModelList, &config.ModelConfig{
			ModelName: "openai-second",
			Provider:  "openai",
			Model:     "gpt-5.4-mini",
			Enabled:   true,
		})
		if err := config.SaveConfig(configPath, cfg); err != nil {
			t.Fatalf("SaveConfig() error = %v", err)
		}
		index := loadedModelIndex(t, configPath, "openai-second")
		before := readConfigBytes(t, configPath)

		recorder := serveModelAliasRequest(
			t,
			configPath,
			http.MethodPut,
			modelMutationURLWithCurrentRevision(
				t,
				configPath,
				"/api/accounts/models/"+strconv.Itoa(index),
			),
			`{
				"model_name":"openai-second",
				"provider":"anthropic",
				"model":"claude-sonnet-4.6",
				"enabled":true,
				"set_as_default":true
			}`,
		)

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", recorder.Code, recorder.Body.String())
		}
		if after := readConfigBytes(t, configPath); !bytes.Equal(after, before) {
			t.Fatal("rejected update changed the persisted config")
		}
	})
}
