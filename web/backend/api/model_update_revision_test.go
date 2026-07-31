package api

import (
	"bytes"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

func TestUpdateModelRejectsStaleRevisionBeforeDuplicateRowInterpretation(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "shared-account",
			Provider:  "openai",
			Model:     "gpt-row-a",
			Proxy:     "http://row-a",
			Enabled:   true,
		},
		{
			ModelName: "shared-account",
			Provider:  "openai",
			Model:     "gpt-row-b",
			Proxy:     "http://row-b",
			Enabled:   true,
		},
		{
			ModelName: "shared-account",
			Provider:  "openai",
			Model:     "gpt-row-c",
			Proxy:     "http://row-c",
			Enabled:   true,
		},
	}
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	staleRevision, err := config.ConfigRevision(configPath)
	if err != nil {
		t.Fatalf("ConfigRevision() error = %v", err)
	}

	current, currentRevision, err := config.LoadConfigForUpdateSnapshot(configPath)
	if err != nil {
		t.Fatalf("LoadConfigForUpdateSnapshot() error = %v", err)
	}
	current.ModelList = current.ModelList[1:]
	if _, saveErr := config.SaveConfigIfRevision(
		configPath,
		current,
		currentRevision,
	); saveErr != nil {
		t.Fatalf("SaveConfigIfRevision() error = %v", saveErr)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	recorder := serveModelAliasRequest(
		t,
		configPath,
		http.MethodPut,
		"/api/accounts/models/1?revision="+url.QueryEscape(staleRevision),
		`{
			"model_name":"shared-account",
			"provider":"openai",
			"model":"gpt-wrong-row",
			"proxy":"http://wrong-row",
			"enabled":true
		}`,
	)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", recorder.Code, recorder.Body.String())
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("stale indexed update changed the persisted config")
	}

	saved, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if len(saved.ModelList) != 2 {
		t.Fatalf("model_list length = %d, want 2", len(saved.ModelList))
	}
	if saved.ModelList[0].Proxy != "http://row-b" ||
		saved.ModelList[1].Proxy != "http://row-c" {
		t.Fatalf(
			"shifted rows were mutated: proxies = %q, %q",
			saved.ModelList[0].Proxy,
			saved.ModelList[1].Proxy,
		)
	}
}
