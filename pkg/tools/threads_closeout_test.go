package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/session"
	threadstore "github.com/sipeed/picoclaw/pkg/threads"
)

func TestCloseoutThreadsLifecycleActions(t *testing.T) {
	if result := (&ThreadsTool{}).Execute(context.Background(), map[string]any{
		"action": "get_policy",
	}); result == nil || result.IsError || !strings.Contains(result.ForLLM, "Thread routing policy") {
		t.Fatalf("nil-config policy result = %#v", result)
	}

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace")
	tool := NewThreadsTool(cfg, "  ")
	if result := tool.Execute(context.Background(), map[string]any{
		"action": "propose_switch",
		"query":  "nothing yet",
	}); result == nil || result.IsError || !result.ResponseHandled {
		t.Fatalf("proposal result = %#v", result)
	}
	if result := tool.Execute(context.Background(), map[string]any{
		"action": "register_current",
	}); result == nil || !result.IsError || !strings.Contains(result.ForLLM, "current session") {
		t.Fatalf("register without session = %#v", result)
	}
	if result := tool.Execute(context.Background(), map[string]any{
		"action": "attach_current",
	}); result == nil || !result.IsError || !strings.Contains(result.ForLLM, "current session") {
		t.Fatalf("attach without session = %#v", result)
	}

	created := closeoutCreateThread(t, tool, "target implementation", "Target implementation")
	originCtx := closeoutThreadContext(t, cfg.Agents.Defaults.Workspace, "telegram", "origin-one")
	attachedResult := tool.Execute(originCtx, map[string]any{
		"action":          "attach_current",
		"id":              created.ID,
		"query":           "target implementation",
		"handoff_summary": "continue the implementation",
	})
	if attachedResult == nil || attachedResult.IsError {
		t.Fatalf("attach current = %#v", attachedResult)
	}
	var attached threadSwitchCard
	if err := json.Unmarshal([]byte(attachedResult.ForUser), &attached); err != nil || attached.Handoff == nil {
		t.Fatalf("decode attached card = %#v, %v", attached, err)
	}
	if result := tool.Execute(context.Background(), map[string]any{
		"action":              "return_to_origin",
		"id":                  attached.Handoff.ID,
		"clear_active_thread": true,
	}); result == nil || result.IsError {
		t.Fatalf("return without current session = %#v", result)
	}
	reviewDetachScope := &session.SessionScope{
		Version: session.ScopeVersionV1,
		AgentID: "main",
		Channel: "review",
	}
	reviewDetachKey := session.BuildOpaqueSessionKey(
		"agent:main:review:direct:detach-review",
	)
	reviewScopeJSON, err := json.Marshal(reviewDetachScope)
	if err != nil {
		t.Fatal(err)
	}
	sessionStore, err := memory.NewJSONLStore(
		threadstore.ResolveSessionsDir(cfg.Agents.Defaults.Workspace),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := sessionStore.ReplaceSessionSnapshot(
		context.Background(),
		memory.SessionSnapshotReplacement{
			Key:     reviewDetachKey,
			History: []providers.Message{{Role: "user", Content: "private review"}},
			Scope:   reviewScopeJSON,
		},
	); err != nil {
		t.Fatal(err)
	}
	invalidSessionCtx := WithToolSessionContext(
		context.Background(),
		"main",
		reviewDetachKey,
		reviewDetachScope,
	)
	if result := tool.Execute(invalidSessionCtx, map[string]any{
		"action":              "return_to_origin",
		"id":                  attached.Handoff.ID,
		"clear_active_thread": true,
	}); result == nil || !result.IsError || !strings.Contains(result.ForLLM, "clearing current thread") {
		t.Fatalf("return with invalid current session = %#v", result)
	}
	returnResult := tool.Execute(originCtx, map[string]any{
		"action":              "return_to_origin",
		"id":                  attached.Handoff.ID,
		"clear_active_thread": true,
	})
	if returnResult == nil || returnResult.IsError || !returnResult.ResponseHandled {
		t.Fatalf("return to origin = %#v", returnResult)
	}
	if result := tool.Execute(originCtx, map[string]any{
		"action": "detach_current",
	}); result == nil || result.IsError {
		t.Fatalf("detach current = %#v", result)
	}
	if result := tool.Execute(context.Background(), map[string]any{
		"action": "detach_current",
	}); result == nil || result.IsError {
		t.Fatalf("detach without session = %#v", result)
	}
	if result := tool.Execute(invalidSessionCtx, map[string]any{
		"action": "detach_current",
	}); result == nil || !result.IsError {
		t.Fatalf("detach invalid session = %#v", result)
	}
	if result := tool.Execute(originCtx, map[string]any{
		"action":     "return_to_origin",
		"handoff_id": "missing-handoff",
	}); result == nil || !result.IsError || !strings.Contains(result.ForLLM, "not found") {
		t.Fatalf("missing handoff = %#v", result)
	}

	if result := tool.Execute(originCtx, map[string]any{
		"action": "attach_current",
		"query":  "no exact selection here",
	}); result == nil || result.IsError || !result.ResponseHandled {
		t.Fatalf("attach proposal = %#v", result)
	}
	createdAndAttached := tool.Execute(originCtx, map[string]any{
		"action":            "attach_current",
		"query":             "brand new focused task",
		"title":             "Brand new focused task",
		"type":              "coding",
		"create_if_missing": true,
	})
	if createdAndAttached == nil || createdAndAttached.IsError {
		t.Fatalf("attach with create = %#v", createdAndAttached)
	}

	secondCtx := closeoutThreadContext(t, cfg.Agents.Defaults.Workspace, "discord", "origin-two")
	registered := tool.Execute(secondCtx, map[string]any{
		"action":          "register_current",
		"query":           "register remote working thread",
		"title":           "Remote working thread",
		"type":            "investigating",
		"handoff_summary": "remote handoff",
	})
	if registered == nil || registered.IsError {
		t.Fatalf("non-pico register current = %#v", registered)
	}

	switchedCreated := tool.Execute(context.Background(), map[string]any{
		"action":            "switch",
		"query":             "create from switch",
		"title":             "Create from switch",
		"create_if_missing": true,
	})
	if switchedCreated == nil || switchedCreated.IsError {
		t.Fatalf("switch create-if-missing = %#v", switchedCreated)
	}
	_ = closeoutCreateThread(t, tool, "shared search alpha", "Shared alpha")
	_ = closeoutCreateThread(t, tool, "shared search beta", "Shared beta")
	if result := tool.Execute(context.Background(), map[string]any{
		"action": "switch",
		"query":  "shared search",
	}); result == nil || result.IsError || !result.ResponseHandled {
		t.Fatalf("ambiguous switch = %#v", result)
	}

	if result := tool.Execute(context.Background(), map[string]any{
		"action": "update_metadata",
	}); result == nil || !result.IsError {
		t.Fatalf("metadata without id = %#v", result)
	}
	if result := tool.Execute(context.Background(), map[string]any{
		"action": "update_metadata",
		"id":     "missing-thread",
	}); result == nil || !result.IsError || !strings.Contains(result.ForLLM, "not found") {
		t.Fatalf("metadata missing thread = %#v", result)
	}
	updated := tool.Execute(context.Background(), map[string]any{
		"action": "update_metadata",
		"id":     created.ID,
		"query":  "updated source query",
		"title":  "Updated title",
		"context": map[string]any{
			" Branch ": " main ",
			"ignored":  7,
		},
	})
	if updated == nil || updated.IsError || !strings.Contains(updated.ForUser, "Updated title") {
		t.Fatalf("metadata update = %#v", updated)
	}

	for name, args := range map[string]map[string]any{
		"drop missing id":  {"action": "drop"},
		"drop missing row": {"action": "drop_thread", "id": "missing-thread"},
		"unknown action":   {"action": "unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			if result := tool.Execute(context.Background(), args); result == nil || !result.IsError {
				t.Fatalf("Execute(%v) = %#v", args, result)
			}
		})
	}
	if result := tool.Execute(context.Background(), map[string]any{
		"action": "drop_thread",
		"id":     created.ID,
	}); result == nil || result.IsError || !strings.Contains(result.ForLLM, "from discovery") {
		t.Fatalf("drop existing = %#v", result)
	}
}

func TestCloseoutThreadsFilesystemAndPolicyErrors(t *testing.T) {
	parent := t.TempDir()
	invalidWorkspace := filepath.Join(parent, "workspace-file")
	if err := os.WriteFile(invalidWorkspace, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = invalidWorkspace
	tool := NewThreadsTool(cfg)
	ctx := WithToolContext(context.Background(), "telegram", "invalid-workspace")
	ctx = WithToolSessionContext(
		ctx,
		"main",
		session.BuildOpaqueSessionKey("agent:main:telegram:direct:invalid-workspace"),
		nil,
	)
	for name, args := range map[string]map[string]any{
		"search": {
			"action": "search",
			"query":  "anything",
		},
		"proposal": {
			"action": "propose_switch",
			"query":  "anything",
		},
		"create": {
			"action": "create",
			"query":  "create anything",
		},
		"register": {
			"action": "register_current",
			"query":  "register anything",
		},
		"attach": {
			"action": "attach_current",
			"query":  "attach anything",
		},
		"switch": {
			"action": "switch",
			"id":     "anything",
			"query":  "switch anything",
		},
		"drop": {
			"action": "drop",
			"id":     "anything",
		},
		"update": {
			"action": "update_metadata",
			"id":     "anything",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if result := tool.Execute(ctx, args); result == nil || !result.IsError {
				t.Fatalf("filesystem error action %s = %#v", name, result)
			}
		})
	}

	validCfg := config.DefaultConfig()
	validCfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace")
	validTool := NewThreadsTool(validCfg)
	if validTool.Name() != ThreadsToolName || validTool.Description() == "" ||
		validTool.Parameters()["type"] != "object" {
		t.Fatalf(
			"threads descriptor = %q %q %#v",
			validTool.Name(),
			validTool.Description(),
			validTool.Parameters(),
		)
	}
	validCtx := closeoutThreadContext(
		t,
		validCfg.Agents.Defaults.Workspace,
		"telegram",
		"error-origin",
	)
	if result := validTool.Execute(validCtx, map[string]any{
		"action":            "attach_current",
		"query":             "find me a thread regarding missing",
		"create_if_missing": true,
	}); result == nil || result.IsError || !result.ResponseHandled {
		t.Fatalf("lookup attach proposal = %#v", result)
	}
	if result := validTool.Execute(validCtx, map[string]any{
		"action":            "attach_current",
		"create_if_missing": true,
	}); result == nil || !result.IsError || !strings.Contains(result.ForLLM, "query is required") {
		t.Fatalf("blank attach create query = %#v", result)
	}

	target := closeoutCreateThread(t, validTool, "review target", "Review target")
	reviewScope := &session.SessionScope{
		Version: session.ScopeVersionV1,
		AgentID: "main",
		Channel: "review",
	}
	reviewCtx := WithToolContext(context.Background(), "review", "review-chat")
	reviewCtx = WithToolSessionContext(
		reviewCtx,
		"main",
		session.BuildOpaqueSessionKey("agent:main:review:direct:review-chat"),
		reviewScope,
	)
	if result := validTool.Execute(reviewCtx, map[string]any{
		"action": "attach_current",
		"id":     target.ID,
	}); result == nil || !result.IsError {
		t.Fatalf("review attach = %#v", result)
	}
	if result := validTool.Execute(reviewCtx, map[string]any{
		"action": "register_current",
		"query":  "register review-scoped remote thread",
	}); result == nil || !result.IsError {
		t.Fatalf("review remote register = %#v", result)
	}
	picoReviewCtx := WithToolContext(context.Background(), "pico", "review-chat")
	picoReviewCtx = WithToolSessionContext(
		picoReviewCtx,
		"main",
		session.BuildOpaqueSessionKey("agent:main:review:direct:pico-review-chat"),
		reviewScope,
	)
	if result := validTool.Execute(picoReviewCtx, map[string]any{
		"action": "register_current",
		"query":  "register review-scoped pico thread",
	}); result == nil || !result.IsError {
		t.Fatalf("review pico register = %#v", result)
	}

	threadRegistry := threadstore.NewStoreFromWorkspace(validCfg.Agents.Defaults.Workspace)
	if err := os.MkdirAll(threadRegistry.HandoffsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(threadRegistry.HandoffsDir, "corrupt.json"),
		[]byte("not json"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if result := validTool.Execute(context.Background(), map[string]any{
		"action":     "return_to_origin",
		"handoff_id": "corrupt",
	}); result == nil || !result.IsError {
		t.Fatalf("corrupt handoff = %#v", result)
	}

	loadFailureTool := NewThreadsTool(validCfg, t.TempDir())
	if result := loadFailureTool.Execute(context.Background(), map[string]any{
		"action":         "set_policy",
		"policy_enabled": true,
	}); result == nil || !result.IsError {
		t.Fatalf("policy load failure = %#v", result)
	}

	originalSave := saveThreadPolicyConfig
	saveThreadPolicyConfig = func(string, *config.Config, string) (string, error) {
		return "", errors.New("injected save failure")
	}
	t.Cleanup(func() { saveThreadPolicyConfig = originalSave })
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := config.SaveConfig(configPath, validCfg); err != nil {
		t.Fatal(err)
	}
	saveFailureTool := NewThreadsTool(validCfg, configPath)
	if result := saveFailureTool.Execute(context.Background(), map[string]any{
		"action":         "set_policy",
		"policy_enabled": true,
	}); result == nil || !result.IsError {
		t.Fatalf("policy save failure = %#v", result)
	}

	if result := validTool.Execute(context.Background(), map[string]any{
		"action": "search",
		"limit":  -1,
	}); result == nil || result.IsError {
		t.Fatalf("negative search limit fallback = %#v", result)
	}
	for _, args := range []map[string]any{
		{"action": "set_policy", "rules": "bad"},
		{"action": "set_policy", "agents": "bad"},
	} {
		if result := validTool.Execute(context.Background(), args); result == nil || !result.IsError {
			t.Fatalf("invalid policy collection = %#v", result)
		}
	}
	if result := validTool.threadPolicyResult(config.ThreadPolicyConfig{}); result == nil || result.IsError {
		t.Fatalf("empty policy result = %#v", result)
	}
	emptyModeCfg := config.DefaultConfig()
	emptyModeCfg.Tools.Threads.Policy.Mode = ""
	if _, policy, err := NewThreadsTool(emptyModeCfg).updateThreadPolicy(nil); err != nil ||
		policy.Mode != config.ThreadPolicyModeTool {
		t.Fatalf("empty policy mode default = %#v, %v", policy, err)
	}

	emptyWorkspaceCfg := config.DefaultConfig()
	emptyWorkspaceCfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace")
	emptyTool := NewThreadsTool(emptyWorkspaceCfg)
	emptyCtx := closeoutThreadContext(
		t,
		emptyWorkspaceCfg.Agents.Defaults.Workspace,
		"telegram",
		"empty-origin",
	)
	if result := emptyTool.Execute(emptyCtx, map[string]any{
		"action": "attach_current",
		"query":  "zzzz-no-thread-can-match-zzzz",
	}); result == nil || result.IsError || !result.ResponseHandled {
		t.Fatalf("empty attach proposal = %#v", result)
	}
	canceledBase, cancel := context.WithCancel(context.Background())
	canceledCtx := WithToolContext(canceledBase, "telegram", "canceled-origin")
	canceledCtx = WithToolSessionContext(
		canceledCtx,
		"main",
		session.BuildOpaqueSessionKey("agent:main:telegram:direct:canceled-origin"),
		nil,
	)
	cancel()
	for name, args := range map[string]map[string]any{
		"register": {
			"action": "register_current",
			"query":  "canceled register",
		},
		"attach": {
			"action":            "attach_current",
			"query":             "canceled attach",
			"create_if_missing": true,
		},
		"switch": {
			"action":            "switch",
			"query":             "canceled switch",
			"create_if_missing": true,
		},
	} {
		t.Run("canceled create "+name, func(t *testing.T) {
			if result := emptyTool.Execute(canceledCtx, args); result == nil || !result.IsError {
				t.Fatalf("%s = %#v", name, result)
			}
		})
	}
	if result := emptyTool.Execute(context.Background(), map[string]any{
		"action":            "switch",
		"query":             "fresh-switch-success-needle",
		"title":             "Fresh switch success",
		"create_if_missing": true,
	}); result == nil || result.IsError {
		t.Fatalf("fresh switch create = %#v", result)
	}

	createFailureWorkspace := t.TempDir()
	if err := os.MkdirAll(
		threadstore.ResolveThreadsDir(createFailureWorkspace),
		0o755,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		threadstore.ResolveSessionsDir(createFailureWorkspace),
		[]byte("sessions path is a file"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	createFailureCfg := config.DefaultConfig()
	createFailureCfg.Agents.Defaults.Workspace = createFailureWorkspace
	createFailureTool := NewThreadsTool(createFailureCfg)
	createFailureCtx := WithToolContext(context.Background(), "telegram", "create-failure")
	createFailureCtx = WithToolSessionContext(
		createFailureCtx,
		"main",
		session.BuildOpaqueSessionKey("agent:main:telegram:direct:create-failure"),
		nil,
	)
	for name, args := range map[string]map[string]any{
		"register": {
			"action": "register_current",
			"query":  "register create failure",
		},
		"attach": {
			"action":            "attach_current",
			"query":             "attach create failure",
			"create_if_missing": true,
		},
		"switch": {
			"action":            "switch",
			"query":             "switch create failure",
			"create_if_missing": true,
		},
	} {
		t.Run("create failure "+name, func(t *testing.T) {
			if result := createFailureTool.Execute(createFailureCtx, args); result == nil ||
				!result.IsError {
				t.Fatalf("%s = %#v", name, result)
			}
		})
	}

	invalidStore := threadstore.NewStoreFromWorkspace(invalidWorkspace)
	if _, _, resolveErr := resolveThreadForTool(
		invalidStore,
		"",
		"query",
		"",
		nil,
		8,
	); resolveErr == nil {
		t.Fatal("invalid thread store resolved without error")
	}
}

func TestCloseoutThreadsResolutionAndArgumentHelpers(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = filepath.Join(t.TempDir(), "workspace")
	tool := NewThreadsTool(cfg)
	first := closeoutCreateThread(t, tool, "zephyrquokka", "One")
	_ = closeoutCreateThread(t, tool, "many common subject", "Many one")
	_ = closeoutCreateThread(t, tool, "many common subject again", "Many two")
	store := threadstore.NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)

	if got, ok, err := resolveThreadForTool(store, first.ID, "", "", nil, 8); err != nil || !ok || got.ID != first.ID {
		t.Fatalf("resolve exact = %#v, %t, %v", got, ok, err)
	}
	got, ok, resolveErr := resolveThreadForTool(
		store,
		"missing",
		"zephyrquokka",
		"",
		nil,
		8,
	)
	if resolveErr != nil || !ok || got.ID != first.ID {
		t.Fatalf("resolve search = %#v, %t, %v", got, ok, resolveErr)
	}
	if _, ok, err := resolveThreadForTool(store, "", "many common", "", nil, 8); err != nil || ok {
		t.Fatalf("resolve ambiguous = ok:%t err:%v", ok, err)
	}

	if stringArg(nil, "x") != "" {
		t.Fatal("stringArg(nil) was nonempty")
	}
	if _, ok := stringArgOK(nil, "x"); ok {
		t.Fatal("stringArgOK(nil) reported a value")
	}
	for _, test := range []struct {
		raw  any
		want int
	}{
		{raw: int(2), want: 2},
		{raw: int64(3), want: 3},
		{raw: float64(4.8), want: 4},
		{raw: json.Number("5"), want: 5},
		{raw: json.Number("bad"), want: 9},
		{raw: "bad", want: 9},
	} {
		if got := intArg(test.raw, 9); got != test.want {
			t.Errorf("intArg(%#v) = %d, want %d", test.raw, got, test.want)
		}
	}
	for _, test := range []struct {
		raw  any
		want float64
	}{
		{raw: float32(1.5), want: 1.5},
		{raw: float64(2.5), want: 2.5},
		{raw: int(3), want: 3},
		{raw: int64(4), want: 4},
		{raw: json.Number("5.5"), want: 5.5},
		{raw: json.Number("bad"), want: 9},
		{raw: "bad", want: 9},
	} {
		if got := floatArg(test.raw, 9); got != test.want {
			t.Errorf("floatArg(%#v) = %v, want %v", test.raw, got, test.want)
		}
	}
	if contextArg("bad") != nil || contextArg(map[string]any{"ignored": 1}) != nil {
		t.Fatal("contextArg accepted non-string context")
	}
	if got := contextArg(map[string]any{" Key ": " Value "}); got["key"] != "Value" {
		t.Fatalf("contextArg normalized = %#v", got)
	}
	if !isThreadLookupRequest("please find some thread regarding cats") ||
		isThreadLookupRequest("ordinary implementation") {
		t.Fatal("thread lookup classification was inconsistent")
	}
	if normalizedThreadLookupText("") != "" || firstNonEmptyString(" ", " value ") != "value" ||
		firstNonEmptyString(" ", "") != "" {
		t.Fatal("string normalization helpers were inconsistent")
	}

	handoffResult := threadReturnResult(threadstore.ThreadHandoff{
		ID:               "handoff",
		OriginSessionKey: "origin-key",
	})
	if handoffResult == nil || handoffResult.IsError || !strings.Contains(handoffResult.ForUser, "origin-key") {
		t.Fatalf("threadReturnResult fallback = %#v", handoffResult)
	}
}

func TestCloseoutThreadsPolicyParsingBranches(t *testing.T) {
	for input, want := range map[string]string{
		"AUTO":      config.ThreadPolicyModeAuto,
		"":          config.ThreadPolicyModeTool,
		" suggest ": config.ThreadPolicyModeSuggest,
		"tool":      config.ThreadPolicyModeTool,
		"off":       config.ThreadPolicyModeOff,
	} {
		if got, err := normalizeThreadPolicyMode(input); err != nil || got != want {
			t.Errorf("normalizeThreadPolicyMode(%q) = %q, %v", input, got, err)
		}
	}
	if _, err := normalizeThreadPolicyMode("invalid"); err == nil {
		t.Fatal("invalid thread mode was accepted")
	}

	typedRules := []config.ThreadPolicyRule{{Type: "coding", Description: "typed"}}
	if rules, err := threadPolicyRulesArg(typedRules); err != nil || len(rules) != 1 {
		t.Fatalf("typed rules = %#v, %v", rules, err)
	}
	rules, rulesErr := threadPolicyRulesArg([]any{map[string]any{
		"type":                "reviewing",
		"description":         "review work",
		"mode":                "suggest",
		"attach_strategy":     "never",
		"min_messages":        int64(2),
		"min_text_chars":      json.Number("20"),
		"threshold_logic":     "all",
		"min_auto_confidence": json.Number("0.75"),
		"confirm_if_multiple": true,
	}})
	if rulesErr != nil || len(rules) != 1 || rules[0].MinMessages != 2 || rules[0].MinTextChars != 20 {
		t.Fatalf("object rules = %#v, %v", rules, rulesErr)
	}
	for name, raw := range map[string]any{
		"not array":      "bad",
		"non-object":     []any{"bad"},
		"no description": []any{map[string]any{"type": "coding"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, parseErr := threadPolicyRulesArg(raw); parseErr == nil {
				t.Fatalf("threadPolicyRulesArg(%#v) succeeded", raw)
			}
		})
	}

	typedAgents := map[string]config.ThreadAgentPolicy{
		" main ": {Mode: "AUTO", AttachStrategy: "NEVER"},
	}
	if agents, parseErr := threadPolicyAgentsArg(typedAgents); parseErr != nil || agents["main"].Mode != "auto" {
		t.Fatalf("typed agents = %#v, %v", agents, parseErr)
	}
	agents, agentsErr := threadPolicyAgentsArg(map[string]any{
		" worker ": map[string]any{"mode": "suggest", "attach_strategy": "never"},
		" ":        map[string]any{"mode": "off"},
	})
	if agentsErr != nil || len(agents) != 1 || agents["worker"].Mode != "suggest" {
		t.Fatalf("object agents = %#v, %v", agents, agentsErr)
	}
	for _, raw := range []any{"bad", map[string]any{"main": "bad"}} {
		if _, parseErr := threadPolicyAgentsArg(raw); parseErr == nil {
			t.Fatalf("threadPolicyAgentsArg(%#v) succeeded", raw)
		}
	}
	if normalizeThreadAgentPolicies(nil) != nil ||
		normalizeThreadAgentPolicies(map[string]config.ThreadAgentPolicy{" ": {}}) != nil {
		t.Fatal("empty agent policies were retained")
	}
	if optionalThreadPolicyMode(" ") != "" || optionalThreadAttachStrategy(" ") != "" {
		t.Fatal("blank optional policy value was retained")
	}

	tool := NewThreadsTool(nil)
	if result := tool.Execute(context.Background(), map[string]any{
		"action": "set_policy",
		"mode":   "invalid",
	}); result == nil || !result.IsError {
		t.Fatalf("invalid policy update = %#v", result)
	}
	updated, policy, err := tool.updateThreadPolicy(map[string]any{
		"policy_enabled": true,
		"instructions":   " trim me ",
		"rules":          typedRules,
		"agents":         typedAgents,
	})
	if err != nil || updated == nil || !policy.Enabled || policy.Instructions != "trim me" ||
		len(policy.Rules) != 1 || len(policy.Agents) != 1 {
		t.Fatalf("in-memory policy update = %#v, %#v, %v", updated, policy, err)
	}
}

func closeoutCreateThread(
	t *testing.T,
	tool *ThreadsTool,
	query string,
	title string,
) threadstore.Thread {
	t.Helper()
	result := tool.Execute(context.Background(), map[string]any{
		"action": "create",
		"query":  query,
		"title":  title,
		"type":   "coding",
	})
	if result == nil || result.IsError {
		t.Fatalf("create thread %q = %#v", query, result)
	}
	var card threadSwitchCard
	if err := json.Unmarshal([]byte(result.ForUser), &card); err != nil || card.Thread.ID == "" {
		t.Fatalf("decode created thread = %#v, %v", card, err)
	}
	return card.Thread
}

func closeoutThreadContext(
	t *testing.T,
	workspace string,
	channel string,
	chatID string,
) context.Context {
	t.Helper()
	key := session.BuildOpaqueSessionKey("agent:main:" + channel + ":direct:" + chatID)
	store, err := memory.NewJSONLStore(threadstore.ResolveSessionsDir(workspace))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceSessionSnapshot(
		context.Background(),
		memory.SessionSnapshotReplacement{
			Key:     key,
			History: []providers.Message{{Role: "user", Content: "origin"}},
			Scope:   json.RawMessage(`{}`),
		},
	); err != nil {
		t.Fatal(err)
	}
	ctx := WithToolContext(context.Background(), channel, chatID)
	return WithToolSessionContext(ctx, "main", key, nil)
}
