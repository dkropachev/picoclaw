package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	threadstore "github.com/sipeed/picoclaw/pkg/threads"
)

const (
	ThreadsToolName       = "threads"
	threadSearchCardType  = "picoclaw.thread_search.v1"
	threadSwitchCardType  = "picoclaw.thread_switch.v1"
	defaultThreadToolSize = 8
)

type ThreadsTool struct {
	cfg        *config.Config
	configPath string
}

type threadSearchCard struct {
	Type    string               `json:"type"`
	Query   string               `json:"query"`
	Threads []threadstore.Thread `json:"threads"`
	Total   int                  `json:"total"`
}

type threadSwitchCard struct {
	Type       string             `json:"type"`
	Query      string             `json:"query,omitempty"`
	AutoSwitch bool               `json:"auto_switch"`
	Thread     threadstore.Thread `json:"thread"`
}

func NewThreadsTool(cfg *config.Config, configPath ...string) *ThreadsTool {
	path := ""
	if len(configPath) > 0 {
		path = strings.TrimSpace(configPath[0])
	}
	return &ThreadsTool{cfg: cfg, configPath: path}
}

func (t *ThreadsTool) Name() string {
	return ThreadsToolName
}

func (t *ThreadsTool) Description() string {
	return "Search, create, or switch PicoClaw UI threads, and inspect or update the automatic thread routing policy. Use when a request belongs in a separate thread, when the user asks to find previous threads, or when context like repo/location/branch/pr identifies an existing thread."
}

func (t *ThreadsTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Action to take: search, create, switch, get_policy, or set_policy.",
				"enum":        []string{"search", "create", "switch", "get_policy", "set_policy"},
			},
			"query": map[string]any{
				"type":        "string",
				"description": "Exact user/model search query. The UI thread search tile will reuse this exact query.",
			},
			"id": map[string]any{
				"type":        "string",
				"description": "Existing thread/session id to switch to, or a requested id for create.",
			},
			"type": map[string]any{
				"type":        "string",
				"description": "Thread type filter or created thread type.",
				"enum":        []string{"general", "coding", "reviewing", "investigating"},
			},
			"title": map[string]any{
				"type":        "string",
				"description": "Title for a created thread.",
			},
			"context": map[string]any{
				"type":                 "object",
				"description":          "Context tags such as repo, location, branch, pr.",
				"additionalProperties": map[string]any{"type": "string"},
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum search results to return.",
				"minimum":     1,
				"maximum":     threadstore.MaxLimit,
			},
			"create_if_missing": map[string]any{
				"type":        "boolean",
				"description": "For switch: create a new matching thread when no existing thread matches.",
			},
			"policy_enabled": map[string]any{
				"type":        "boolean",
				"description": "For set_policy: enable or disable automatic thread routing policy.",
			},
			"mode": map[string]any{
				"type":        "string",
				"description": "For set_policy: policy mode. auto creates/switches when rules match; suggest only suggests; off disables policy.",
				"enum":        []string{"auto", "suggest", "off"},
			},
			"instructions": map[string]any{
				"type":        "string",
				"description": "For set_policy: additional model-facing routing instructions. Pass an empty string to clear.",
			},
			"rules": map[string]any{
				"type":        "array",
				"description": "For set_policy: replacement routing rules.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"type": map[string]any{
							"type":        "string",
							"enum":        []string{"general", "coding", "reviewing", "investigating"},
							"description": "Thread type to create or switch to when the rule matches.",
						},
						"description": map[string]any{
							"type":        "string",
							"description": "Natural-language condition for when this rule should match.",
						},
					},
					"required": []string{"type", "description"},
				},
			},
		},
		"required": []string{"action"},
	}
}

func (t *ThreadsTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	cfg := t.cfg
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	store := threadstore.NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)

	action := strings.ToLower(strings.TrimSpace(stringArg(args, "action")))
	query := strings.TrimSpace(stringArg(args, "query"))
	threadType := strings.TrimSpace(stringArg(args, "type"))
	threadID := strings.TrimSpace(stringArg(args, "id"))
	title := strings.TrimSpace(stringArg(args, "title"))
	contextTags := contextArg(args["context"])
	limit := intArg(args["limit"], defaultThreadToolSize)
	if limit <= 0 {
		limit = defaultThreadToolSize
	}

	switch action {
	case "get_policy":
		return t.threadPolicyResult(cfg.Tools.Threads.Policy)

	case "set_policy":
		updatedCfg, policy, err := t.updateThreadPolicy(args)
		if err != nil {
			return ErrorResult("updating thread policy: " + err.Error()).WithError(err)
		}
		t.cfg = updatedCfg
		return t.threadPolicyResult(policy)

	case "search":
		items, err := store.Search(threadstore.SearchOptions{
			Query:   query,
			Type:    threadType,
			Context: contextTags,
			Limit:   limit,
		})
		if err != nil {
			return ErrorResult("searching threads: " + err.Error()).WithError(err)
		}
		return threadSearchResult(query, items)

	case "create":
		thread, err := store.CreatePicoThread(ctx, cfg, threadstore.CreateRequest{
			ID:          threadID,
			Type:        threadType,
			Title:       title,
			Context:     contextTags,
			SourceQuery: query,
		})
		if err != nil {
			return ErrorResult("creating thread: " + err.Error()).WithError(err)
		}
		return threadSwitchResult(query, thread)

	case "switch":
		if threadID != "" {
			thread, ok, err := store.Get(threadID)
			if err != nil {
				return ErrorResult("opening thread: " + err.Error()).WithError(err)
			}
			if ok {
				return threadSwitchResult(query, thread)
			}
		}

		items, err := store.Search(threadstore.SearchOptions{
			Query:   query,
			Type:    threadType,
			Context: contextTags,
			Limit:   limit,
		})
		if err != nil {
			return ErrorResult("searching threads: " + err.Error()).WithError(err)
		}
		if len(items) == 1 {
			return threadSwitchResult(query, items[0])
		}
		if len(items) == 0 && boolArg(args["create_if_missing"]) {
			thread, err := store.CreatePicoThread(ctx, cfg, threadstore.CreateRequest{
				ID:          threadID,
				Type:        threadType,
				Title:       firstNonEmptyString(title, query, "New thread"),
				Context:     contextTags,
				SourceQuery: query,
			})
			if err != nil {
				return ErrorResult("creating thread: " + err.Error()).WithError(err)
			}
			return threadSwitchResult(query, thread)
		}
		return threadSearchResult(query, items)

	default:
		return ErrorResult("action must be one of: search, create, switch, get_policy, set_policy")
	}
}

func (t *ThreadsTool) updateThreadPolicy(args map[string]any) (*config.Config, config.ThreadPolicyConfig, error) {
	cfg := t.cfg
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	if t.configPath != "" {
		loaded, err := config.LoadConfig(t.configPath)
		if err != nil {
			return nil, config.ThreadPolicyConfig{}, err
		}
		cfg = loaded
	}

	policy := cfg.Tools.Threads.Policy
	if enabled, ok := boolArgOK(args["policy_enabled"]); ok {
		policy.Enabled = enabled
	}
	if mode, ok := stringArgOK(args, "mode"); ok {
		normalized, err := normalizeThreadPolicyMode(mode)
		if err != nil {
			return nil, config.ThreadPolicyConfig{}, err
		}
		policy.Mode = normalized
	}
	if instructions, ok := stringArgOK(args, "instructions"); ok {
		policy.Instructions = strings.TrimSpace(instructions)
	}
	if rawRules, ok := args["rules"]; ok {
		rules, err := threadPolicyRulesArg(rawRules)
		if err != nil {
			return nil, config.ThreadPolicyConfig{}, err
		}
		policy.Rules = rules
	}
	if policy.Mode == "" {
		policy.Mode = config.ThreadPolicyModeAuto
	}
	policy.Rules = config.NormalizeThreadPolicyRules(policy.Rules)
	cfg.Tools.Threads.Policy = policy

	if t.configPath != "" {
		if err := config.SaveConfig(t.configPath, cfg); err != nil {
			return nil, config.ThreadPolicyConfig{}, err
		}
	} else if path := strings.TrimSpace(os.Getenv(config.EnvConfig)); path != "" {
		if err := config.SaveConfig(path, cfg); err != nil {
			return nil, config.ThreadPolicyConfig{}, err
		}
	}

	return cfg, policy, nil
}

func (t *ThreadsTool) threadPolicyResult(policy config.ThreadPolicyConfig) *ToolResult {
	if policy.Mode == "" {
		policy.Mode = config.ThreadPolicyModeAuto
	}
	policy.Rules = config.NormalizeThreadPolicyRules(policy.Rules)
	payload, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return ErrorResult("formatting thread policy: " + err.Error()).WithError(err)
	}
	return NewToolResult("Thread routing policy:\n" + string(payload))
}

func threadSearchResult(query string, items []threadstore.Thread) *ToolResult {
	card := threadSearchCard{
		Type:    threadSearchCardType,
		Query:   query,
		Threads: items,
		Total:   len(items),
	}
	payload, err := json.Marshal(card)
	if err != nil {
		return ErrorResult("formatting thread search result: " + err.Error()).WithError(err)
	}
	return (&ToolResult{
		ForLLM:  fmt.Sprintf("Found %d thread(s) for query %q.", len(items), query),
		ForUser: string(payload),
	}).WithResponseHandled()
}

func threadSwitchResult(query string, thread threadstore.Thread) *ToolResult {
	card := threadSwitchCard{
		Type:       threadSwitchCardType,
		Query:      query,
		AutoSwitch: true,
		Thread:     thread,
	}
	payload, err := json.Marshal(card)
	if err != nil {
		return ErrorResult("formatting thread switch result: " + err.Error()).WithError(err)
	}
	return (&ToolResult{
		ForLLM:  fmt.Sprintf("Switching UI to thread %s (%s).", thread.ID, thread.Title),
		ForUser: string(payload),
	}).WithResponseHandled()
}

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, _ := args[key].(string)
	return value
}

func stringArgOK(args map[string]any, key string) (string, bool) {
	if args == nil {
		return "", false
	}
	value, ok := args[key].(string)
	return value, ok
}

func intArg(raw any, fallback int) int {
	switch value := raw.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			return int(parsed)
		}
	}
	return fallback
}

func boolArg(raw any) bool {
	value, _ := raw.(bool)
	return value
}

func boolArgOK(raw any) (bool, bool) {
	value, ok := raw.(bool)
	return value, ok
}

func contextArg(raw any) map[string]string {
	obj, ok := raw.(map[string]any)
	if !ok || len(obj) == 0 {
		return nil
	}
	context := map[string]string{}
	for key, value := range obj {
		stringValue, ok := value.(string)
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		stringValue = strings.TrimSpace(stringValue)
		if key != "" && stringValue != "" {
			context[key] = stringValue
		}
	}
	if len(context) == 0 {
		return nil
	}
	return context
}

func normalizeThreadPolicyMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", config.ThreadPolicyModeAuto:
		return config.ThreadPolicyModeAuto, nil
	case config.ThreadPolicyModeSuggest:
		return config.ThreadPolicyModeSuggest, nil
	case config.ThreadPolicyModeOff:
		return config.ThreadPolicyModeOff, nil
	default:
		return "", fmt.Errorf("mode must be one of: auto, suggest, off")
	}
}

func threadPolicyRulesArg(raw any) ([]config.ThreadPolicyRule, error) {
	switch rules := raw.(type) {
	case []config.ThreadPolicyRule:
		return config.NormalizeThreadPolicyRules(rules), nil
	case []any:
		out := make([]config.ThreadPolicyRule, 0, len(rules))
		for _, item := range rules {
			obj, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("rules must contain objects")
			}
			threadType, _ := obj["type"].(string)
			description, _ := obj["description"].(string)
			if strings.TrimSpace(description) == "" {
				return nil, fmt.Errorf("rule description is required")
			}
			out = append(out, config.ThreadPolicyRule{
				Type:        config.NormalizeThreadPolicyType(threadType),
				Description: strings.TrimSpace(description),
			})
		}
		return config.NormalizeThreadPolicyRules(out), nil
	default:
		return nil, fmt.Errorf("rules must be an array")
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
