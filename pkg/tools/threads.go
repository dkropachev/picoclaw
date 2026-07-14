package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	threadstore "github.com/sipeed/picoclaw/pkg/threads"
	"github.com/sipeed/picoclaw/pkg/utils"
)

const (
	ThreadsToolName        = "threads"
	threadSearchCardType   = "picoclaw.thread_search.v2"
	threadProposalCardType = "picoclaw.thread_proposal.v1"
	threadSwitchCardType   = "picoclaw.thread_switch.v2"
	threadReturnCardType   = "picoclaw.thread_return.v1"
	defaultThreadToolSize  = 8
)

var validThreadActions = []string{
	"find",
	"search",
	"propose_switch",
	"create",
	"register_current",
	"attach_current",
	"switch",
	"return_to_origin",
	"detach_current",
	"update_metadata",
	"get_policy",
	"set_policy",
}

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

type threadProposalCard struct {
	Type    string               `json:"type"`
	Query   string               `json:"query,omitempty"`
	Reason  string               `json:"reason,omitempty"`
	Threads []threadstore.Thread `json:"threads"`
	Total   int                  `json:"total"`
}

type threadSwitchCard struct {
	Type            string                     `json:"type"`
	Query           string                     `json:"query,omitempty"`
	AutoSwitch      bool                       `json:"auto_switch"`
	Thread          threadstore.Thread         `json:"thread"`
	TargetSessionID string                     `json:"target_session_id"`
	Handoff         *threadstore.ThreadHandoff `json:"handoff,omitempty"`
}

type threadReturnCard struct {
	Type            string `json:"type"`
	TargetSessionID string `json:"target_session_id"`
	HandoffID       string `json:"handoff_id"`
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
	return "Find, propose, register, attach, switch, return from, or update PicoClaw UI threads, and inspect or update the automatic thread routing policy. Use when a request belongs in a separate thread, when the user asks to find previous threads, or when context like repo/location/branch/pr identifies an existing thread."
}

func (t *ThreadsTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Action to take.",
				"enum": []string{
					"find", "search", "propose_switch", "create", "register_current",
					"attach_current", "switch", "return_to_origin", "detach_current",
					"update_metadata", "get_policy", "set_policy",
				},
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
				"description": "For switch/attach_current: create a new matching thread when no existing thread matches. Do not set this for requests to find, search, show, or list existing threads.",
			},
			"handoff_summary": map[string]any{
				"type":        "string",
				"description": "For attach_current: concise summary to write into the target thread.",
			},
			"handoff_id": map[string]any{
				"type":        "string",
				"description": "For return_to_origin: handoff id returned by attach_current/switch.",
			},
			"clear_active_thread": map[string]any{
				"type":        "boolean",
				"description": "For return_to_origin/detach_current: clear current session's active thread link.",
			},
			"policy_enabled": map[string]any{
				"type":        "boolean",
				"description": "For set_policy: enable or disable automatic thread routing policy.",
			},
			"mode": map[string]any{
				"type":        "string",
				"description": "For set_policy: policy mode. auto creates/switches when rules match; suggest only suggests; off disables policy.",
				"enum":        []string{"auto", "tool", "suggest", "off"},
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
						"mode": map[string]any{
							"type":        "string",
							"enum":        []string{"auto", "tool", "suggest", "off"},
							"description": "Optional rule-specific mode.",
						},
						"attach_strategy": map[string]any{
							"type":        "string",
							"enum":        []string{"search_then_create", "search_then_ask", "never"},
							"description": "Optional rule-specific attach strategy.",
						},
						"min_auto_confidence": map[string]any{
							"type":        "number",
							"description": "Optional confidence threshold for automatic attach/switch.",
						},
						"confirm_if_multiple": map[string]any{
							"type":        "boolean",
							"description": "Ask before switching when multiple plausible threads match.",
						},
					},
					"required": []string{"type", "description"},
				},
			},
			"agents": map[string]any{
				"type":        "object",
				"description": "For set_policy: per-agent thread policy overrides keyed by agent id.",
				"additionalProperties": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"mode": map[string]any{
							"type":        "string",
							"enum":        []string{"auto", "tool", "suggest", "off"},
							"description": "Agent-specific mode.",
						},
						"attach_strategy": map[string]any{
							"type":        "string",
							"enum":        []string{"search_then_create", "search_then_ask", "never"},
							"description": "Agent-specific attach strategy.",
						},
					},
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
	handoffSummary := strings.TrimSpace(stringArg(args, "handoff_summary"))
	handoffID := strings.TrimSpace(stringArg(args, "handoff_id"))
	lookupRequest := isThreadLookupRequest(query, title)
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

	case "find", "search":
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

	case "propose_switch":
		items, err := store.Search(threadstore.SearchOptions{
			Query:   query,
			Type:    threadType,
			Context: contextTags,
			Limit:   limit,
		})
		if err != nil {
			return ErrorResult("searching threads: " + err.Error()).WithError(err)
		}
		return threadProposalResult(query, "confirm before switching", items)

	case "create":
		if lookupRequest {
			return threadLookupCreateDeniedResult("creating thread")
		}
		thread, err := store.CreatePicoThread(ctx, cfg, threadstore.CreateRequest{
			ID:           threadID,
			Type:         threadType,
			Title:        title,
			Context:      contextTags,
			SourceQuery:  query,
			Registration: threadstore.RegistrationManual,
		})
		if err != nil {
			return ErrorResult("creating thread: " + err.Error()).WithError(err)
		}
		return threadSwitchResult(query, thread)

	case "register_current":
		sessionKey := ToolSessionKey(ctx)
		if strings.TrimSpace(sessionKey) == "" {
			return ErrorResult("registering current thread: current session is unavailable")
		}
		if lookupRequest {
			return threadLookupCreateDeniedResult("registering current thread")
		}
		if !strings.EqualFold(strings.TrimSpace(ToolChannel(ctx)), "pico") {
			thread, err := store.CreatePicoThread(ctx, cfg, threadstore.CreateRequest{
				ID:           threadID,
				Type:         threadType,
				Title:        firstNonEmptyString(title, query, "New thread"),
				Context:      contextTags,
				SourceQuery:  query,
				AgentID:      ToolAgentID(ctx),
				Registration: threadstore.RegistrationTool,
			})
			if err != nil {
				return ErrorResult("registering current thread: " + err.Error()).WithError(err)
			}
			attached, handoff, err := store.AttachCurrent(ctx, threadstore.AttachRequest{
				ThreadID:        thread.ID,
				SessionKey:      sessionKey,
				AgentID:         ToolAgentID(ctx),
				OriginSessionID: ToolChatID(ctx),
				Summary:         handoffSummary,
				Scope:           ToolSessionScope(ctx),
			})
			if err != nil {
				return ErrorResult("attaching current session: " + err.Error()).WithError(err)
			}
			return threadSwitchResultWithHandoff(query, attached, &handoff)
		}
		thread, err := store.RegisterCurrent(ctx, threadstore.CreateRequest{
			ID:                threadID,
			Type:              threadType,
			Title:             firstNonEmptyString(title, query, "New thread"),
			Context:           contextTags,
			SourceQuery:       query,
			PrimarySessionKey: sessionKey,
			AgentID:           ToolAgentID(ctx),
			Registration:      threadstore.RegistrationTool,
		}, ToolSessionScope(ctx))
		if err != nil {
			return ErrorResult("registering current thread: " + err.Error()).WithError(err)
		}
		return threadSwitchResult(query, thread)

	case "attach_current":
		sessionKey := ToolSessionKey(ctx)
		if strings.TrimSpace(sessionKey) == "" {
			return ErrorResult("attaching current session: current session is unavailable")
		}
		thread, ok, err := resolveThreadForTool(store, threadID, query, threadType, contextTags, limit)
		if err != nil {
			return ErrorResult("finding thread: " + err.Error()).WithError(err)
		}
		if !ok {
			if !boolArg(args["create_if_missing"]) {
				items, searchErr := store.Search(threadstore.SearchOptions{
					Query: query, Type: threadType, Context: contextTags, Limit: limit,
				})
				if searchErr != nil {
					return ErrorResult("searching threads: " + searchErr.Error()).WithError(searchErr)
				}
				return threadProposalResult(query, "no exact thread selected", items)
			}
			if lookupRequest {
				items, searchErr := store.Search(threadstore.SearchOptions{
					Query: query, Type: threadType, Context: contextTags, Limit: limit,
				})
				if searchErr != nil {
					return ErrorResult("searching threads: " + searchErr.Error()).WithError(searchErr)
				}
				return threadProposalResult(query, "thread lookup did not authorize creating a new thread", items)
			}
			created, createErr := store.CreatePicoThread(ctx, cfg, threadstore.CreateRequest{
				ID:           threadID,
				Type:         threadType,
				Title:        firstNonEmptyString(title, query, "New thread"),
				Context:      contextTags,
				SourceQuery:  query,
				Registration: threadstore.RegistrationTool,
			})
			if createErr != nil {
				return ErrorResult("creating thread: " + createErr.Error()).WithError(createErr)
			}
			thread = created
		}
		attached, handoff, err := store.AttachCurrent(ctx, threadstore.AttachRequest{
			ThreadID:        thread.ID,
			SessionKey:      sessionKey,
			AgentID:         ToolAgentID(ctx),
			OriginSessionID: ToolChatID(ctx),
			Summary:         handoffSummary,
			Scope:           ToolSessionScope(ctx),
		})
		if err != nil {
			return ErrorResult("attaching current session: " + err.Error()).WithError(err)
		}
		return threadSwitchResultWithHandoff(query, attached, &handoff)

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
			if lookupRequest {
				return threadProposalResult(query, "thread lookup did not authorize creating a new thread", items)
			}
			thread, err := store.CreatePicoThread(ctx, cfg, threadstore.CreateRequest{
				ID:           threadID,
				Type:         threadType,
				Title:        firstNonEmptyString(title, query, "New thread"),
				Context:      contextTags,
				SourceQuery:  query,
				Registration: threadstore.RegistrationTool,
			})
			if err != nil {
				return ErrorResult("creating thread: " + err.Error()).WithError(err)
			}
			return threadSwitchResult(query, thread)
		}
		return threadProposalResult(query, "multiple or no matching threads", items)

	case "return_to_origin":
		if handoffID == "" {
			handoffID = threadID
		}
		handoff, ok, err := store.ReturnToOrigin(handoffID)
		if err != nil {
			return ErrorResult("returning to origin: " + err.Error()).WithError(err)
		}
		if !ok {
			return ErrorResult("returning to origin: handoff not found")
		}
		if boolArg(args["clear_active_thread"]) {
			if err := store.DetachCurrent(ToolSessionKey(ctx)); err != nil {
				return ErrorResult("clearing current thread: " + err.Error()).WithError(err)
			}
		}
		return threadReturnResult(handoff)

	case "detach_current":
		if err := store.DetachCurrent(ToolSessionKey(ctx)); err != nil {
			return ErrorResult("detaching current session: " + err.Error()).WithError(err)
		}
		return NewToolResult("Detached the current session from its active thread.")

	case "update_metadata":
		if threadID == "" {
			return ErrorResult("updating thread metadata: id is required")
		}
		thread, ok, err := store.UpdateThread(threadID, threadstore.UpdateRequest{
			Title: title, Type: threadType, Context: contextTags, SourceQuery: query,
		})
		if err != nil {
			return ErrorResult("updating thread metadata: " + err.Error()).WithError(err)
		}
		if !ok {
			return ErrorResult("updating thread metadata: thread not found")
		}
		return threadSearchResult(query, []threadstore.Thread{thread})

	default:
		return ErrorResult("action must be one of: " + strings.Join(validThreadActions, ", "))
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
	if rawAgents, ok := args["agents"]; ok {
		agents, err := threadPolicyAgentsArg(rawAgents)
		if err != nil {
			return nil, config.ThreadPolicyConfig{}, err
		}
		policy.Agents = agents
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
	return threadSwitchResultWithHandoff(query, thread, nil)
}

func threadSwitchResultWithHandoff(
	query string,
	thread threadstore.Thread,
	handoff *threadstore.ThreadHandoff,
) *ToolResult {
	card := threadSwitchCard{
		Type:            threadSwitchCardType,
		Query:           query,
		AutoSwitch:      true,
		Thread:          thread,
		TargetSessionID: firstNonEmptyString(thread.UISessionID, thread.ID),
		Handoff:         handoff,
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

func threadProposalResult(query, reason string, items []threadstore.Thread) *ToolResult {
	card := threadProposalCard{
		Type:    threadProposalCardType,
		Query:   query,
		Reason:  reason,
		Threads: items,
		Total:   len(items),
	}
	payload, err := json.Marshal(card)
	if err != nil {
		return ErrorResult("formatting thread proposal result: " + err.Error()).WithError(err)
	}
	return (&ToolResult{
		ForLLM:  fmt.Sprintf("Found %d candidate thread(s) for query %q; ask before switching.", len(items), query),
		ForUser: string(payload),
	}).WithResponseHandled()
}

func threadReturnResult(handoff threadstore.ThreadHandoff) *ToolResult {
	target := firstNonEmptyString(handoff.OriginSessionID, handoff.OriginSessionKey)
	card := threadReturnCard{
		Type:            threadReturnCardType,
		TargetSessionID: target,
		HandoffID:       handoff.ID,
	}
	payload, err := json.Marshal(card)
	if err != nil {
		return ErrorResult("formatting thread return result: " + err.Error()).WithError(err)
	}
	return (&ToolResult{
		ForLLM:  fmt.Sprintf("Returning UI to original session %s.", target),
		ForUser: string(payload),
	}).WithResponseHandled()
}

func resolveThreadForTool(
	store threadstore.Store,
	threadID, query, threadType string,
	contextTags map[string]string,
	limit int,
) (threadstore.Thread, bool, error) {
	if strings.TrimSpace(threadID) != "" {
		thread, ok, err := store.Get(threadID)
		if err != nil || ok {
			return thread, ok, err
		}
	}
	items, err := store.Search(threadstore.SearchOptions{
		Query: query, Type: threadType, Context: contextTags, Limit: limit,
	})
	if err != nil {
		return threadstore.Thread{}, false, err
	}
	if len(items) == 1 {
		return items[0], true, nil
	}
	return threadstore.Thread{}, false, nil
}

func threadLookupCreateDeniedResult(action string) *ToolResult {
	return ErrorResult(
		action + ": thread lookup requests must not create or register a new thread; " +
			"use action=\"find\", action=\"search\", or action=\"switch\" without create_if_missing, " +
			"or continue in the already selected thread.",
	)
}

func isThreadLookupRequest(parts ...string) bool {
	text := normalizedThreadLookupText(parts...)
	if text == "" {
		return false
	}
	if !strings.Contains(text, " thread ") && !strings.Contains(text, " threads ") {
		return false
	}
	lookupPhrases := []string{
		" find me a thread ",
		" find me threads ",
		" find a thread ",
		" find the thread ",
		" find threads ",
		" search thread ",
		" search threads ",
		" search for a thread ",
		" search for threads ",
		" look up a thread ",
		" look up threads ",
		" lookup thread ",
		" lookup threads ",
		" show me a thread ",
		" show me threads ",
		" show thread ",
		" show threads ",
		" list thread ",
		" list threads ",
	}
	for _, phrase := range lookupPhrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	lookupWords := []string{" find ", " search ", " lookup "}
	threadQualifiers := []string{
		" thread about ",
		" thread regarding ",
		" thread related ",
		" thread on ",
		" threads about ",
		" threads regarding ",
		" threads related ",
		" threads on ",
	}
	for _, word := range lookupWords {
		if !strings.Contains(text, word) {
			continue
		}
		for _, qualifier := range threadQualifiers {
			if strings.Contains(text, qualifier) {
				return true
			}
		}
	}
	return false
}

func normalizedThreadLookupText(parts ...string) string {
	joined := strings.ToLower(strings.TrimSpace(strings.Join(parts, " ")))
	if joined == "" {
		return ""
	}
	hint := strings.ToLower(utils.ToolFeedbackContinuationHint)
	if strings.HasPrefix(joined, hint) {
		joined = strings.TrimLeft(strings.TrimPrefix(joined, hint), " \t\r\n:.-")
	}
	joined = strings.Map(func(r rune) rune {
		switch r {
		case '.', ',', ';', ':', '!', '?', '"', '\'', '`', '(', ')', '[', ']', '{', '}':
			return ' '
		default:
			return r
		}
	}, joined)
	return " " + strings.Join(strings.Fields(joined), " ") + " "
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

func floatArg(raw any, fallback float64) float64 {
	switch value := raw.(type) {
	case float32:
		return float64(value)
	case float64:
		return value
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case json.Number:
		if parsed, err := value.Float64(); err == nil {
			return parsed
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
	case config.ThreadPolicyModeTool:
		return config.ThreadPolicyModeTool, nil
	case config.ThreadPolicyModeOff:
		return config.ThreadPolicyModeOff, nil
	default:
		return "", fmt.Errorf("mode must be one of: auto, tool, suggest, off")
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
			mode, _ := obj["mode"].(string)
			attachStrategy, _ := obj["attach_strategy"].(string)
			minAutoConfidence := floatArg(obj["min_auto_confidence"], 0)
			confirmIfMultiple := boolArg(obj["confirm_if_multiple"])
			out = append(out, config.ThreadPolicyRule{
				Type:              config.NormalizeThreadPolicyType(threadType),
				Description:       strings.TrimSpace(description),
				Mode:              optionalThreadPolicyMode(mode),
				AttachStrategy:    optionalThreadAttachStrategy(attachStrategy),
				MinAutoConfidence: minAutoConfidence,
				ConfirmIfMultiple: confirmIfMultiple,
			})
		}
		return config.NormalizeThreadPolicyRules(out), nil
	default:
		return nil, fmt.Errorf("rules must be an array")
	}
}

func optionalThreadPolicyMode(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return config.NormalizeThreadPolicyMode(value)
}

func optionalThreadAttachStrategy(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return config.NormalizeThreadAttachStrategy(value)
}

func threadPolicyAgentsArg(raw any) (map[string]config.ThreadAgentPolicy, error) {
	switch agents := raw.(type) {
	case map[string]config.ThreadAgentPolicy:
		return normalizeThreadAgentPolicies(agents), nil
	case map[string]any:
		out := make(map[string]config.ThreadAgentPolicy, len(agents))
		for agentID, rawPolicy := range agents {
			obj, ok := rawPolicy.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("agents must map to objects")
			}
			mode, _ := obj["mode"].(string)
			attachStrategy, _ := obj["attach_strategy"].(string)
			out[agentID] = config.ThreadAgentPolicy{
				Mode:           optionalThreadPolicyMode(mode),
				AttachStrategy: optionalThreadAttachStrategy(attachStrategy),
			}
		}
		return normalizeThreadAgentPolicies(out), nil
	default:
		return nil, fmt.Errorf("agents must be an object")
	}
}

func normalizeThreadAgentPolicies(src map[string]config.ThreadAgentPolicy) map[string]config.ThreadAgentPolicy {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]config.ThreadAgentPolicy, len(src))
	for agentID, policy := range src {
		agentID = strings.TrimSpace(agentID)
		if agentID == "" {
			continue
		}
		if strings.TrimSpace(policy.Mode) != "" {
			policy.Mode = config.NormalizeThreadPolicyMode(policy.Mode)
		}
		if strings.TrimSpace(policy.AttachStrategy) != "" {
			policy.AttachStrategy = config.NormalizeThreadAttachStrategy(policy.AttachStrategy)
		}
		out[agentID] = policy
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
