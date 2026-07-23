package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

type gitWorkspaceActionRequest struct {
	WorkspaceID string `json:"workspace_id"`
}

func (h *Handler) registerGitWorkspaceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/git-workspaces", h.handleListGitWorkspaces)
	mux.HandleFunc("POST /api/git-workspaces/reconcile", h.handleReconcileGitWorkspaces)
	mux.HandleFunc("POST /api/git-workspaces/cleanup", h.handleCleanupGitWorkspace)
	mux.HandleFunc("DELETE /api/git-workspaces/{id}", h.handleDropGitWorkspace)
}

func (h *Handler) handleListGitWorkspaces(w http.ResponseWriter, r *http.Request) {
	manager, err := h.gitWorkspaceManager()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stats, err := manager.Stats(r.Context())
	if err != nil {
		http.Error(
			w,
			fmt.Sprintf("Failed to list git workspaces: %v", err),
			http.StatusInternalServerError,
		)
		return
	}
	writeJSON(w, stats)
}

func (h *Handler) handleReconcileGitWorkspaces(w http.ResponseWriter, r *http.Request) {
	manager, err := h.gitWorkspaceManager()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	result, err := manager.Reconcile(r.Context())
	if err != nil {
		http.Error(
			w,
			fmt.Sprintf("Failed to reconcile git workspaces: %v", err),
			http.StatusInternalServerError,
		)
		return
	}
	writeJSON(w, result)
}

func (h *Handler) handleCleanupGitWorkspace(w http.ResponseWriter, r *http.Request) {
	manager, err := h.gitWorkspaceManager()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var req gitWorkspaceActionRequest
	if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", decodeErr), http.StatusBadRequest)
		return
	}
	result, err := manager.CleanupIgnored(r.Context(), req.WorkspaceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, result)
}

func (h *Handler) handleDropGitWorkspace(w http.ResponseWriter, r *http.Request) {
	manager, err := h.gitWorkspaceManager()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	info, err := manager.Drop(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]any{"workspace": info})
}

func (h *Handler) gitWorkspaceManager() (*gitworkspace.Manager, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return nil, fmt.Errorf("Failed to load config: %w", err)
	}
	return gitworkspace.NewManager(gitworkspace.Options{
		RootDir:             cfg.GitWorkspaceRootPath(),
		MaxTotalSizeBytes:   cfg.GitWorkspaces.EffectiveMaxTotalSizeBytes(),
		IgnoredCleanupDelay: cfg.GitWorkspaces.EffectiveIgnoredCleanupDelay(),
		DropDelay:           cfg.GitWorkspaces.EffectiveDropDelay(),
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}
