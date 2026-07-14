package tools

import (
	"context"
	"encoding/json"
	"fmt"
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
	cfg *config.Config
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

func NewThreadsTool(cfg *config.Config) *ThreadsTool {
	return &ThreadsTool{cfg: cfg}
}

func (t *ThreadsTool) Name() string {
	return ThreadsToolName
}

func (t *ThreadsTool) Description() string {
	return "Search, create, or switch PicoClaw UI threads. Use when a request belongs in a separate thread, when the user asks to find previous threads, or when context like repo/location/branch/pr identifies an existing thread."
}

func (t *ThreadsTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "Action to take: search, create, or switch.",
				"enum":        []string{"search", "create", "switch"},
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
		return ErrorResult("action must be one of: search, create, switch")
	}
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

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
