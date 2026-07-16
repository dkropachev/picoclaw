package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
	threadstore "github.com/sipeed/picoclaw/pkg/threads"
)

type threadCreateRequest struct {
	ID           string            `json:"id,omitempty"`
	Type         string            `json:"type,omitempty"`
	Title        string            `json:"title,omitempty"`
	Context      map[string]string `json:"context,omitempty"`
	SourceQuery  string            `json:"source_query,omitempty"`
	Discoverable *bool             `json:"discoverable,omitempty"`
}

type threadAttachRequest struct {
	SessionID      string `json:"session_id,omitempty"`
	SessionKey     string `json:"session_key,omitempty"`
	HandoffSummary string `json:"handoff_summary,omitempty"`
}

type threadReturnResponse struct {
	TargetSessionID string `json:"target_session_id"`
	HandoffID       string `json:"handoff_id"`
}

type gatewayActiveTurnsResponse struct {
	ActiveTurns []struct {
		SessionKey string `json:"session_key"`
	} `json:"active_turns"`
}

var gatewayActiveTurnsDo = func(req *http.Request, timeout time.Duration) (*http.Response, error) {
	client := http.Client{Timeout: timeout}
	return client.Do(req)
}

func (h *Handler) registerThreadRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/threads", h.handleListThreads)
	mux.HandleFunc("POST /api/threads", h.handleCreateThread)
	mux.HandleFunc("GET /api/threads/{id}", h.handleGetThread)
	mux.HandleFunc("PATCH /api/threads/{id}", h.handleUpdateThread)
	mux.HandleFunc("DELETE /api/threads/{id}", h.handleDropThread)
	mux.HandleFunc("POST /api/threads/{id}/attach-current", h.handleAttachCurrentThread)
	mux.HandleFunc("POST /api/threads/handoffs/{id}/return", h.handleReturnThreadHandoff)
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
	opts.IncludeDropped = parseThreadBoolQuery(query.Get("include_dropped"))
	if contextFilter := parseThreadContextQuery(query.Get("context")); len(contextFilter) > 0 {
		opts.Context = contextFilter
	}

	store := threadstore.NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
	items, err := store.Search(opts)
	if err != nil {
		http.Error(w, "failed to list threads", http.StatusInternalServerError)
		return
	}
	items = h.annotateThreadsWorking(cfg, items)

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

func (h *Handler) handleGetThread(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, "failed to load config", http.StatusInternalServerError)
		return
	}
	store := threadstore.NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
	thread, ok, err := store.Get(r.PathValue("id"))
	if err != nil {
		http.Error(w, "failed to load thread", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "thread not found", http.StatusNotFound)
		return
	}
	thread = h.annotateThreadWorking(cfg, thread)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(thread)
}

func (h *Handler) handleUpdateThread(w http.ResponseWriter, r *http.Request) {
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
	thread, ok, err := store.UpdateThread(r.PathValue("id"), threadstore.UpdateRequest{
		Title:        req.Title,
		Type:         req.Type,
		Context:      req.Context,
		SourceQuery:  req.SourceQuery,
		Discoverable: req.Discoverable,
	})
	if err != nil {
		http.Error(w, "failed to update thread", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "thread not found", http.StatusNotFound)
		return
	}
	thread = h.annotateThreadWorking(cfg, thread)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(thread)
}

func (h *Handler) handleDropThread(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, "failed to load config", http.StatusInternalServerError)
		return
	}
	store := threadstore.NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
	thread, ok, err := store.DropThread(r.PathValue("id"))
	if err != nil {
		http.Error(w, "failed to drop thread", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "thread not found", http.StatusNotFound)
		return
	}
	thread = h.annotateThreadWorking(cfg, thread)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(thread)
}

func (h *Handler) handleAttachCurrentThread(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, "failed to load config", http.StatusInternalServerError)
		return
	}
	var req threadAttachRequest
	if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
		http.Error(w, "invalid attach request", http.StatusBadRequest)
		return
	}
	sessionKey := strings.TrimSpace(req.SessionKey)
	if sessionKey == "" {
		sessionKey, err = resolveThreadAPISessionKey(r.Context(), cfg.Agents.Defaults.Workspace, req.SessionID)
		if err != nil {
			http.Error(w, "failed to resolve session", http.StatusBadRequest)
			return
		}
	}
	store := threadstore.NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
	thread, handoff, err := store.AttachCurrent(r.Context(), threadstore.AttachRequest{
		ThreadID:        r.PathValue("id"),
		SessionKey:      sessionKey,
		OriginSessionID: req.SessionID,
		Summary:         req.HandoffSummary,
	})
	if err != nil {
		http.Error(w, "failed to attach thread", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Thread  threadstore.Thread        `json:"thread"`
		Handoff threadstore.ThreadHandoff `json:"handoff"`
	}{Thread: h.annotateThreadWorking(cfg, thread), Handoff: handoff})
}

func (h *Handler) handleReturnThreadHandoff(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		http.Error(w, "failed to load config", http.StatusInternalServerError)
		return
	}
	store := threadstore.NewStoreFromWorkspace(cfg.Agents.Defaults.Workspace)
	handoff, ok, err := store.ReturnToOrigin(r.PathValue("id"))
	if err != nil {
		http.Error(w, "failed to load handoff", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "handoff not found", http.StatusNotFound)
		return
	}
	target := strings.TrimSpace(handoff.OriginSessionID)
	if target == "" {
		target = handoff.OriginSessionKey
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(threadReturnResponse{
		TargetSessionID: target,
		HandoffID:       handoff.ID,
	})
}

func (h *Handler) annotateThreadsWorking(
	cfg *config.Config,
	items []threadstore.Thread,
) []threadstore.Thread {
	activeSessions := h.gatewayActiveSessionKeys(cfg)
	if len(activeSessions) == 0 {
		return items
	}
	for i := range items {
		items[i].IsWorking = threadHasActiveSession(items[i], activeSessions)
	}
	return items
}

func (h *Handler) annotateThreadWorking(
	cfg *config.Config,
	thread threadstore.Thread,
) threadstore.Thread {
	activeSessions := h.gatewayActiveSessionKeys(cfg)
	if len(activeSessions) == 0 {
		return thread
	}
	thread.IsWorking = threadHasActiveSession(thread, activeSessions)
	return thread
}

func threadHasActiveSession(thread threadstore.Thread, activeSessions map[string]bool) bool {
	candidates := []string{
		thread.PrimarySessionKey,
		thread.SessionKey,
		thread.UISessionID,
		thread.ID,
	}
	for _, candidate := range candidates {
		if activeSessions[strings.TrimSpace(candidate)] {
			return true
		}
	}
	return false
}

func (h *Handler) gatewayActiveSessionKeys(cfg *config.Config) map[string]bool {
	pidData := h.activeTurnsGatewayPidData(cfg)
	if pidData == nil || strings.TrimSpace(pidData.Token) == "" {
		return nil
	}

	host := gatewayProbeHost(pidData.Host)
	if host == "" {
		host = gatewayProbeHost(h.effectiveGatewayBindHost(cfg))
	}
	port := pidData.Port
	if port == 0 {
		port = 18790
		if cfg != nil && cfg.Gateway.Port != 0 {
			port = cfg.Gateway.Port
		}
	}

	req, err := http.NewRequest(
		http.MethodGet,
		"http://"+net.JoinHostPort(host, strconv.Itoa(port))+"/runtime/active-turns",
		nil,
	)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+pidData.Token)

	resp, err := gatewayActiveTurnsDo(req, 800*time.Millisecond)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var payload gatewayActiveTurnsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil
	}
	activeSessions := make(map[string]bool, len(payload.ActiveTurns))
	for _, turn := range payload.ActiveTurns {
		sessionKey := strings.TrimSpace(turn.SessionKey)
		if sessionKey != "" {
			activeSessions[sessionKey] = true
		}
	}
	return activeSessions
}

func (h *Handler) activeTurnsGatewayPidData(cfg *config.Config) *ppid.PidFileData {
	if pidData := h.sanitizeGatewayPidData(ppid.ReadPidFileWithCheck(globalConfigDir()), cfg); pidData != nil {
		return pidData
	}

	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if gateway.pidData == nil {
		return nil
	}
	return &ppid.PidFileData{
		PID:     gateway.pidData.PID,
		Token:   gateway.pidData.Token,
		Version: gateway.pidData.Version,
		Port:    gateway.pidData.Port,
		Host:    gateway.pidData.Host,
	}
}

func resolveThreadAPISessionKey(ctx context.Context, workspace, sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", errors.New("missing session id")
	}
	store, err := memory.NewJSONLStore(threadstore.ResolveSessionsDir(workspace))
	if err != nil {
		return "", err
	}
	key, found, err := store.ResolveSessionKey(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("session not found")
	}
	return key, nil
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

func parseThreadBoolQuery(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
