package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/session"
	threadstore "github.com/sipeed/picoclaw/pkg/threads"
	"github.com/sipeed/picoclaw/pkg/utils"
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
	if switchCard.TargetSessionID == "" || switchCard.TargetSessionID != switchCard.Thread.UISessionID {
		t.Fatalf(
			"switch target = %q, thread ui session = %q",
			switchCard.TargetSessionID,
			switchCard.Thread.UISessionID,
		)
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

func TestThreadsToolRegisterCurrentUsesToolContext(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace")
	tool := NewThreadsTool(cfg)

	scope := &session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "pico",
		Dimensions: []string{"chat"},
		Values:     map[string]string{"chat": "direct:pico:current-ui-session"},
	}
	sessionKey := session.BuildSessionKey(*scope)
	ctx := WithToolContext(context.Background(), "pico", "current-ui-session")
	ctx = WithToolSessionContext(ctx, "main", sessionKey, scope)
	result := tool.Execute(ctx, map[string]any{
		"action": "register_current",
		"title":  "Current coding session",
		"type":   "coding",
	})
	if result.IsError {
		t.Fatalf("register_current result error: %s", result.ForLLM)
	}

	var switchCard threadSwitchCard
	if err := json.Unmarshal([]byte(result.ForUser), &switchCard); err != nil {
		t.Fatalf("Unmarshal(register ForUser) error = %v", err)
	}
	if switchCard.Thread.SessionKey != sessionKey {
		t.Fatalf("registered session key = %q, want %q", switchCard.Thread.SessionKey, sessionKey)
	}
	if switchCard.TargetSessionID != "current-ui-session" {
		t.Fatalf("target session id = %q, want current-ui-session", switchCard.TargetSessionID)
	}
}

func TestThreadsToolLookupRequestsDoNotCreateDuplicateThreads(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace")
	tool := NewThreadsTool(cfg)

	createResult := tool.Execute(context.Background(), map[string]any{
		"action": "create",
		"query":  "japan travel planning",
		"title":  "Japan travel",
		"type":   "general",
	})
	if createResult.IsError {
		t.Fatalf("create result error: %s", createResult.ForLLM)
	}
	var createdCard threadSwitchCard
	if err := json.Unmarshal([]byte(createResult.ForUser), &createdCard); err != nil {
		t.Fatalf("Unmarshal(create ForUser) error = %v", err)
	}

	switchResult := tool.Execute(context.Background(), map[string]any{
		"action":            "switch",
		"query":             "find me a thread regarding japan",
		"create_if_missing": true,
	})
	if switchResult.IsError {
		t.Fatalf("switch result error: %s", switchResult.ForLLM)
	}
	var switchedCard threadSwitchCard
	if err := json.Unmarshal([]byte(switchResult.ForUser), &switchedCard); err != nil {
		t.Fatalf("Unmarshal(switch ForUser) error = %v", err)
	}
	if switchedCard.Thread.ID != createdCard.Thread.ID {
		t.Fatalf("switched thread ID = %q, want %q", switchedCard.Thread.ID, createdCard.Thread.ID)
	}
	assertThreadCount(t, cfg, 1)

	continuationTitle := utils.ToolFeedbackContinuationHint + ": find me a thread regarding japan"
	duplicateCreate := tool.Execute(context.Background(), map[string]any{
		"action": "create",
		"title":  continuationTitle,
		"type":   "general",
	})
	if !duplicateCreate.IsError || !strings.Contains(duplicateCreate.ForLLM, "lookup requests must not create") {
		t.Fatalf("duplicate create result = %#v", duplicateCreate)
	}
	assertThreadCount(t, cfg, 1)

	scope := &session.SessionScope{
		Version:    session.ScopeVersionV1,
		AgentID:    "main",
		Channel:    "pico",
		Dimensions: []string{"chat"},
		Values:     map[string]string{"chat": "direct:pico:current-ui-session"},
	}
	sessionKey := session.BuildSessionKey(*scope)
	ctx := WithToolContext(context.Background(), "pico", "current-ui-session")
	ctx = WithToolSessionContext(ctx, "main", sessionKey, scope)
	duplicateRegister := tool.Execute(ctx, map[string]any{
		"action": "register_current",
		"title":  continuationTitle,
		"type":   "general",
	})
	if !duplicateRegister.IsError || !strings.Contains(duplicateRegister.ForLLM, "lookup requests must not create") {
		t.Fatalf("duplicate register result = %#v", duplicateRegister)
	}
	assertThreadCount(t, cfg, 1)
}

func TestThreadsToolLookupSwitchCreateIfMissingDoesNotCreate(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace")
	tool := NewThreadsTool(cfg)

	result := tool.Execute(context.Background(), map[string]any{
		"action":            "switch",
		"query":             "find me a thread regarding atlantis",
		"create_if_missing": true,
	})
	if result.IsError {
		t.Fatalf("switch result error: %s", result.ForLLM)
	}
	var proposalCard threadProposalCard
	if err := json.Unmarshal([]byte(result.ForUser), &proposalCard); err != nil {
		t.Fatalf("Unmarshal(switch ForUser) error = %v", err)
	}
	if proposalCard.Type != threadProposalCardType || proposalCard.Total != 0 {
		t.Fatalf("proposalCard = %#v", proposalCard)
	}
	assertThreadCount(t, cfg, 0)
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

func assertThreadCount(t *testing.T, cfg *config.Config, want int) {
	t.Helper()
	store := threadstore.NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
	items, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != want {
		t.Fatalf("thread count = %d, want %d; items = %#v", len(items), want, items)
	}
}
