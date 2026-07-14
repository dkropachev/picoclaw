package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	threadstore "github.com/sipeed/picoclaw/pkg/threads"
)

func TestThreadsToolCreateSearchAndSwitchCards(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace")
	tool := NewThreadsTool(cfg)

	createResult := tool.Execute(context.Background(), map[string]any{
		"action": "create",
		"query":  "code in /extra/dkropachev/picoclaw branch:main",
		"title":  "Implement thread UI",
		"type":   "coding",
		"context": map[string]any{
			"branch": "main",
		},
	})
	if createResult.IsError {
		t.Fatalf("create result error: %s", createResult.ForLLM)
	}
	if !createResult.ResponseHandled {
		t.Fatal("create result was not marked response-handled")
	}

	var switchCard threadSwitchCard
	if err := json.Unmarshal([]byte(createResult.ForUser), &switchCard); err != nil {
		t.Fatalf("Unmarshal(create ForUser) error = %v", err)
	}
	if switchCard.Type != threadSwitchCardType || !switchCard.AutoSwitch {
		t.Fatalf("switchCard = %#v", switchCard)
	}
	if switchCard.Thread.ID == "" {
		t.Fatal("created thread ID is empty")
	}
	if switchCard.Thread.Type != threadstore.TypeCoding {
		t.Fatalf("created thread type = %q", switchCard.Thread.Type)
	}
	if got := switchCard.Thread.Context["branch"]; got != "main" {
		t.Fatalf("created thread branch context = %q", got)
	}

	searchResult := tool.Execute(context.Background(), map[string]any{
		"action": "search",
		"query":  "branch:main",
		"type":   "coding",
		"limit":  float64(2),
	})
	if searchResult.IsError {
		t.Fatalf("search result error: %s", searchResult.ForLLM)
	}
	if !searchResult.ResponseHandled {
		t.Fatal("search result was not marked response-handled")
	}

	var searchCard threadSearchCard
	if err := json.Unmarshal([]byte(searchResult.ForUser), &searchCard); err != nil {
		t.Fatalf("Unmarshal(search ForUser) error = %v", err)
	}
	if searchCard.Type != threadSearchCardType || len(searchCard.Threads) != 1 {
		t.Fatalf("searchCard = %#v", searchCard)
	}
	if searchCard.Threads[0].ID != switchCard.Thread.ID {
		t.Fatalf("search result ID = %q, want %q", searchCard.Threads[0].ID, switchCard.Thread.ID)
	}

	explicitSwitchResult := tool.Execute(context.Background(), map[string]any{
		"action": "switch",
		"id":     switchCard.Thread.ID,
	})
	if explicitSwitchResult.IsError {
		t.Fatalf("switch result error: %s", explicitSwitchResult.ForLLM)
	}
	if err := json.Unmarshal([]byte(explicitSwitchResult.ForUser), &switchCard); err != nil {
		t.Fatalf("Unmarshal(switch ForUser) error = %v", err)
	}
	if switchCard.Type != threadSwitchCardType || switchCard.Thread.ID == "" {
		t.Fatalf("explicit switchCard = %#v", switchCard)
	}
}

func TestThreadsToolSetPolicyPersistsConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	tool := NewThreadsTool(cfg, configPath)
	result := tool.Execute(context.Background(), map[string]any{
		"action":         "set_policy",
		"policy_enabled": true,
		"mode":           "suggest",
		"instructions":   "Ask first for risky production work.",
		"rules": []any{
			map[string]any{
				"type":        "coding",
				"description": "Move implementation work to a coding thread.",
			},
		},
	})
	if result.IsError {
		t.Fatalf("set_policy result error: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, `"mode": "suggest"`) {
		t.Fatalf("set_policy result missing updated mode: %s", result.ForLLM)
	}

	updated, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig(updated) error = %v", err)
	}
	if !updated.Tools.Threads.Policy.Enabled {
		t.Fatal("thread policy should be enabled")
	}
	if updated.Tools.Threads.Policy.Mode != config.ThreadPolicyModeSuggest {
		t.Fatalf("mode = %q, want suggest", updated.Tools.Threads.Policy.Mode)
	}
	if updated.Tools.Threads.Policy.Instructions != "Ask first for risky production work." {
		t.Fatalf("instructions = %q", updated.Tools.Threads.Policy.Instructions)
	}
	if len(updated.Tools.Threads.Policy.Rules) != 1 ||
		updated.Tools.Threads.Policy.Rules[0].Type != "coding" {
		t.Fatalf("rules = %#v", updated.Tools.Threads.Policy.Rules)
	}
}
