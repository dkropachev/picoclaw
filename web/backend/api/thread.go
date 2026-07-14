package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	threadstore "github.com/sipeed/picoclaw/pkg/threads"
)

type threadCreateRequest struct {
	ID          string            `json:"id,omitempty"`
	Type        string            `json:"type,omitempty"`
	Title       string            `json:"title,omitempty"`
	Context     map[string]string `json:"context,omitempty"`
	SourceQuery string            `json:"source_query,omitempty"`
}

func (h *Handler) registerThreadRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/threads", h.handleListThreads)
	mux.HandleFunc("POST /api/threads", h.handleCreateThread)
}

func (h *Handler) handleListThreads(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, "failed to load config", http.StatusInternalServerError)
		return
	}

	query := r.URL.Query()
	opts := threadstore.SearchOptions{
		Query:  query.Get("query"),
		Type:   query.Get("type"),
		Offset: threadstore.ParsePositiveInt(query.Get("offset"), 0),
		Limit:  threadstore.ParsePositiveInt(query.Get("limit"), threadstore.DefaultLimit),
	}
	if contextFilter := parseThreadContextQuery(query.Get("context")); len(contextFilter) > 0 {
		opts.Context = contextFilter
	}

	store := threadstore.NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
	items, err := store.Search(opts)
	if err != nil {
		http.Error(w, "failed to list threads", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

func (h *Handler) handleCreateThread(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, "failed to load config", http.StatusInternalServerError)
		return
	}

	var req threadCreateRequest
	if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
		http.Error(w, "invalid thread request", http.StatusBadRequest)
		return
	}

	store := threadstore.NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
	thread, err := store.CreatePicoThread(r.Context(), cfg, threadstore.CreateRequest{
		ID:          req.ID,
		Type:        req.Type,
		Title:       req.Title,
		Context:     req.Context,
		SourceQuery: req.SourceQuery,
	})
	if err != nil {
		http.Error(w, "failed to create thread", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(thread)
}

func parseThreadContextQuery(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	context := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			context[key] = value
		}
	}
	if len(context) == 0 {
		return nil
	}
	return context
}
