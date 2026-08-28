package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

const (
	gitWorkspaceMaximumSettingsBytes = int64(1<<53 - 1)
	gitWorkspaceMaximumDelaySeconds  = 1<<31 - 1
)

var (
	gitWorkspaceIDPattern = regexp.MustCompile(
		`^gw-[0-9a-f]{12}(?:-(?:[2-9]|[1-9][0-9]+))?$`,
	)
	gitWorkspaceReservedPrivateIDPattern = regexp.MustCompile(
		`^gw-[0-9a-f]{12}-pinned(?:-(?:[2-9]|[1-9][0-9]+))?$`,
	)
	gitWorkspaceHistoryIDPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

	gitWorkspaceCollectionSchema = mustCollectionQuerySchema(
		[]collectionquery.FieldSchema{
			{Name: "id", Type: collectionquery.TypeString, Sortable: true},
			{Name: "repository", Type: collectionquery.TypeString, Sortable: true},
			{Name: "branch", Type: collectionquery.TypeString, Sortable: true},
			{
				Name: "status", Type: collectionquery.TypeEnum, Sortable: true,
				SuggestedValues: []string{"available", "locked", "dropped"},
			},
			{
				Name: "locked", Type: collectionquery.TypeBoolean, Sortable: true,
				SuggestedValues: []string{"true", "false"},
			},
			{
				Name: "dirty", Type: collectionquery.TypeBoolean, Sortable: true,
				SuggestedValues: []string{"true", "false"},
			},
			{Name: "size", Type: collectionquery.TypeNumber, Sortable: true},
			{Name: "ignored", Type: collectionquery.TypeNumber, Sortable: true},
			{Name: "updated", Type: collectionquery.TypeTimestamp, Sortable: true},
		},
		[]collectionquery.SortField{{
			Field: "updated", Direction: collectionquery.Descending,
		}},
	)

	gitWorkspaceHistoryCollectionSchema = mustCollectionQuerySchema(
		[]collectionquery.FieldSchema{
			{Name: "action", Type: collectionquery.TypeString, Sortable: true},
			{Name: "workspace", Type: collectionquery.TypeString, Sortable: true},
			{Name: "repository", Type: collectionquery.TypeString, Sortable: true},
			{Name: "agent", Type: collectionquery.TypeString, Sortable: true},
			{Name: "time", Type: collectionquery.TypeTimestamp, Sortable: true},
		},
		[]collectionquery.SortField{{
			Field: "time", Direction: collectionquery.Descending,
		}},
	)
)

type gitWorkspaceManagerAPI interface {
	Stats(ctx context.Context) (gitworkspace.Stats, error)
	Reconcile(ctx context.Context) (gitworkspace.ReconcileResult, error)
	CleanupIgnored(ctx context.Context, id string) (gitworkspace.CleanupResult, error)
	Drop(ctx context.Context, id string) (gitworkspace.WorkspaceInfo, error)
}

type gitWorkspaceActionRequest struct {
	WorkspaceID string `json:"workspace_id"`
}

type gitWorkspaceSummary struct {
	ID         string    `json:"id"`
	Repository string    `json:"repository"`
	Branch     string    `json:"branch"`
	Status     string    `json:"status"`
	Locked     bool      `json:"locked"`
	Dirty      bool      `json:"dirty"`
	Size       int64     `json:"size"`
	Ignored    int64     `json:"ignored"`
	Updated    time.Time `json:"updated"`
}

type gitWorkspaceSafeLock struct {
	AgentID     string    `json:"agent_id,omitempty"`
	LockedAt    time.Time `json:"locked_at"`
	HeartbeatAt time.Time `json:"heartbeat_at"`
}

type gitWorkspaceDetail struct {
	gitWorkspaceSummary
	RepositoryID    string                `json:"repository_id"`
	RemoteURL       string                `json:"remote_url"`
	UpstreamURL     string                `json:"upstream_url,omitempty"`
	Ref             string                `json:"ref,omitempty"`
	Path            string                `json:"path"`
	PreservedBranch string                `json:"preserved_branch,omitempty"`
	Created         time.Time             `json:"created"`
	LastWork        *time.Time            `json:"last_work,omitempty"`
	LastCleaned     *time.Time            `json:"last_cleaned,omitempty"`
	Dropped         *time.Time            `json:"dropped,omitempty"`
	LockedBy        *gitWorkspaceSafeLock `json:"locked_by,omitempty"`
}

type gitWorkspaceHistorySummary struct {
	ID         string    `json:"id"`
	Action     string    `json:"action"`
	Workspace  string    `json:"workspace,omitempty"`
	Repository string    `json:"repository,omitempty"`
	Agent      string    `json:"agent,omitempty"`
	Time       time.Time `json:"time"`
}

type gitWorkspaceAggregate struct {
	MaxTotalSizeBytes          int64 `json:"max_total_size_bytes"`
	IgnoredCleanupDelaySeconds int64 `json:"ignored_cleanup_delay_seconds"`
	DropDelaySeconds           int64 `json:"drop_delay_seconds"`
	TotalSizeBytes             int64 `json:"total_size_bytes"`
	IgnoredBytes               int64 `json:"ignored_bytes"`
	RepositoryCount            int   `json:"repository_count"`
	WorkspaceCount             int   `json:"workspace_count"`
	LockedWorkspaceCount       int   `json:"locked_workspace_count"`
}

type gitWorkspaceCollectionResponse struct {
	gitWorkspaceAggregate
	Workspaces     []gitWorkspaceSummary  `json:"workspaces"`
	Total          int                    `json:"total"`
	NextCursor     string                 `json:"next_cursor"`
	CanonicalQuery string                 `json:"canonical_query"`
	QuerySchema    collectionquery.Schema `json:"query_schema"`
}

type gitWorkspaceHistoryResponse struct {
	History        []gitWorkspaceHistorySummary `json:"history"`
	Total          int                          `json:"total"`
	NextCursor     string                       `json:"next_cursor"`
	CanonicalQuery string                       `json:"canonical_query"`
	QuerySchema    collectionquery.Schema       `json:"query_schema"`
}

type gitWorkspaceSettings struct {
	MaxTotalSizeBytes          int64 `json:"max_total_size_bytes"`
	IgnoredCleanupDelaySeconds int   `json:"ignored_cleanup_delay_seconds"`
	DropDelaySeconds           int   `json:"drop_delay_seconds"`
}

type gitWorkspaceSettingsRequest struct {
	ExpectedConfigRevision string                `json:"expected_config_revision"`
	Settings               *gitWorkspaceSettings `json:"settings"`
}

type gitWorkspaceSettingsResponse struct {
	Configured     gitWorkspaceSettings `json:"configured"`
	Effective      gitWorkspaceSettings `json:"effective"`
	ConfigRevision string               `json:"config_revision"`
	Effects        agentEffects         `json:"effects"`
}

func registerGitWorkspaceCollectionSchemaSuggestions(
	items []gitWorkspaceSummary,
) collectionquery.Schema {
	ids := make([]string, 0, len(items))
	repositories := make([]string, 0, len(items))
	branches := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
		repositories = append(repositories, item.Repository)
		branches = append(branches, item.Branch)
	}
	return collectionSchemaWithSuggestions(
		gitWorkspaceCollectionSchema,
		map[collectionquery.Field][]string{
			"id": ids, "repository": repositories, "branch": branches,
		},
	)
}

func gitWorkspaceHistorySchemaWithSuggestions(
	items []gitWorkspaceHistorySummary,
) collectionquery.Schema {
	actions := make([]string, 0, len(items))
	workspaces := make([]string, 0, len(items))
	repositories := make([]string, 0, len(items))
	agents := make([]string, 0, len(items))
	for _, item := range items {
		actions = append(actions, item.Action)
		workspaces = append(workspaces, item.Workspace)
		repositories = append(repositories, item.Repository)
		agents = append(agents, item.Agent)
	}
	return collectionSchemaWithSuggestions(
		gitWorkspaceHistoryCollectionSchema,
		map[collectionquery.Field][]string{
			"action": actions, "workspace": workspaces,
			"repository": repositories, "agent": agents,
		},
	)
}

func (h *Handler) registerGitWorkspaceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/git-workspaces", h.handleListGitWorkspaces)
	mux.HandleFunc("GET /api/git-workspaces/history", h.handleListGitWorkspaceHistory)
	mux.HandleFunc("GET /api/git-workspaces/settings", h.handleGetGitWorkspaceSettings)
	mux.HandleFunc(
		"PUT /api/git-workspaces/settings",
		h.requireCollectionMutationOrigin(h.handlePutGitWorkspaceSettings),
	)
	mux.HandleFunc("GET /api/git-workspaces/{id}", h.handleGetGitWorkspace)
	mux.HandleFunc(
		"POST /api/git-workspaces/reconcile",
		h.requireCollectionMutationOrigin(h.handleReconcileGitWorkspaces),
	)
	mux.HandleFunc(
		"POST /api/git-workspaces/cleanup",
		h.requireCollectionMutationOrigin(h.handleCleanupGitWorkspace),
	)
	mux.HandleFunc(
		"DELETE /api/git-workspaces/{id}",
		h.requireCollectionMutationOrigin(h.handleDropGitWorkspace),
	)
}

func (h *Handler) handleListGitWorkspaces(w http.ResponseWriter, r *http.Request) {
	request, ok := parseCollectionListRequest(w, r, gitWorkspaceCollectionSchema)
	if !ok {
		return
	}
	stats, ok := h.loadPublicGitWorkspaceStats(w, r)
	if !ok {
		return
	}
	items := projectGitWorkspaceSummaries(stats.Workspaces)
	page, err := pageGitWorkspaces(items, request)
	if err != nil {
		writeCollectionPageError(w, err)
		return
	}
	writeCollectionJSON(w, http.StatusOK, gitWorkspaceCollectionResponse{
		gitWorkspaceAggregate: gitWorkspaceAggregateFromStats(stats),
		Workspaces:            page.Items, Total: page.Total, NextCursor: page.NextCursor,
		CanonicalQuery: request.Query.Canonical(),
		QuerySchema:    registerGitWorkspaceCollectionSchemaSuggestions(items),
	})
}

func (h *Handler) handleGetGitWorkspace(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !requirePublicGitWorkspaceID(w, id) {
		return
	}
	stats, ok := h.loadPublicGitWorkspaceStats(w, r)
	if !ok {
		return
	}
	workspace, found := findPublicGitWorkspace(stats, id)
	if !found {
		writeGitWorkspaceNotFound(w)
		return
	}
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"workspace": gitWorkspaceDetailFromInfo(workspace),
	})
}

func (h *Handler) handleListGitWorkspaceHistory(w http.ResponseWriter, r *http.Request) {
	request, ok := parseCollectionListRequest(
		w, r, gitWorkspaceHistoryCollectionSchema,
	)
	if !ok {
		return
	}
	stats, ok := h.loadPublicGitWorkspaceStats(w, r)
	if !ok {
		return
	}
	items := projectGitWorkspaceHistory(stats)
	page, err := pageGitWorkspaceHistory(items, request)
	if err != nil {
		writeCollectionPageError(w, err)
		return
	}
	writeCollectionJSON(w, http.StatusOK, gitWorkspaceHistoryResponse{
		History: page.Items, Total: page.Total, NextCursor: page.NextCursor,
		CanonicalQuery: request.Query.Canonical(),
		QuerySchema:    gitWorkspaceHistorySchemaWithSuggestions(items),
	})
}

func (h *Handler) handleReconcileGitWorkspaces(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	manager, err := h.gitWorkspaceManager()
	if err != nil {
		writeGitWorkspaceUnavailable(w, "Git workspace maintenance is unavailable")
		return
	}
	result, err := manager.Reconcile(r.Context())
	if err != nil {
		writeCollectionError(
			w, http.StatusInternalServerError, "git_workspace_reconcile_failed",
			"Git workspace maintenance failed", -1, nil,
		)
		return
	}
	public := projectGitWorkspaceSummaries(result.Stats.Workspaces)
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"cleaned": gitWorkspaceSummariesForInfos(public, result.Cleaned),
		"dropped": gitWorkspaceSummariesForInfos(public, result.Dropped),
		"stats":   gitWorkspaceAggregateFromStats(result.Stats),
	})
}

func (h *Handler) handleCleanupGitWorkspace(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	var request gitWorkspaceActionRequest
	if !decodeCollectionJSON(w, r, &request) {
		return
	}
	id := strings.TrimSpace(request.WorkspaceID)
	if !requirePublicGitWorkspaceID(w, id) {
		return
	}
	manager, _, ok := h.loadMutableGitWorkspace(w, r, id)
	if !ok {
		return
	}
	result, err := manager.CleanupIgnored(r.Context(), id)
	if err != nil {
		h.writeGitWorkspaceMutationFailure(w, r, manager, id, "cleanup")
		return
	}
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"workspace":            gitWorkspaceDetailFromInfo(result.Workspace),
		"before_ignored_bytes": result.Before,
		"after_ignored_bytes":  result.After,
	})
}

func (h *Handler) handleDropGitWorkspace(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if !requirePublicGitWorkspaceID(w, id) {
		return
	}
	manager, _, ok := h.loadMutableGitWorkspace(w, r, id)
	if !ok {
		return
	}
	info, err := manager.Drop(r.Context(), id)
	if err != nil {
		h.writeGitWorkspaceMutationFailure(w, r, manager, id, "drop")
		return
	}
	writeCollectionJSON(w, http.StatusOK, map[string]any{
		"workspace": gitWorkspaceDetailFromInfo(info),
	})
}

func (h *Handler) handleGetGitWorkspaceSettings(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r) {
		return
	}
	h.configMutationMu.Lock()
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	h.configMutationMu.Unlock()
	if err != nil {
		writeGitWorkspaceSettingsUnavailable(w)
		return
	}
	writeCollectionJSON(
		w,
		http.StatusOK,
		gitWorkspaceSettingsResponseForConfig(
			cfg, revision, agentEffectsForConfig(cfg),
		),
	)
}

func (h *Handler) handlePutGitWorkspaceSettings(w http.ResponseWriter, r *http.Request) {
	if !validateCollectionQueryParameters(w, r, "revision") {
		return
	}
	var request gitWorkspaceSettingsRequest
	if !decodeCollectionJSON(w, r, &request) {
		return
	}
	if request.Settings == nil || !validGitWorkspaceSettings(*request.Settings) {
		writeCollectionError(
			w, http.StatusUnprocessableEntity, "invalid_git_workspace_settings",
			"Git workspace settings are invalid", -1, nil,
		)
		return
	}
	h.configMutationMu.Lock()
	releaseConfigMutation := sync.OnceFunc(h.configMutationMu.Unlock)
	defer releaseConfigMutation()
	cfg, revision, err := config.LoadConfigForUpdateSnapshot(h.configPath)
	if err != nil {
		writeGitWorkspaceSettingsUnavailable(w)
		return
	}
	expected, ok := resolveCollectionRevision(
		w, r, request.ExpectedConfigRevision,
	)
	if !ok || !requireCollectionRevision(w, expected, revision) {
		return
	}
	cfg.GitWorkspaces.MaxTotalSizeBytes = request.Settings.MaxTotalSizeBytes
	cfg.GitWorkspaces.IgnoredCleanupDelaySeconds = request.Settings.IgnoredCleanupDelaySeconds
	cfg.GitWorkspaces.DropDelaySeconds = request.Settings.DropDelaySeconds
	nextRevision, err := h.saveConfigIfRevision(h.configPath, cfg, revision)
	if err != nil {
		writeCollectionConfigSaveError(w, err)
		return
	}
	releaseConfigMutation()
	writeCollectionJSON(
		w,
		http.StatusOK,
		gitWorkspaceSettingsResponseForConfig(
			cfg, nextRevision, agentEffectsForConfig(cfg),
		),
	)
}

func (h *Handler) loadPublicGitWorkspaceStats(
	w http.ResponseWriter,
	r *http.Request,
) (gitworkspace.Stats, bool) {
	manager, err := h.gitWorkspaceManager()
	if err != nil {
		writeGitWorkspaceUnavailable(w, "Git workspace inventory is unavailable")
		return gitworkspace.Stats{}, false
	}
	stats, err := manager.Stats(r.Context())
	if err != nil {
		writeGitWorkspaceUnavailable(w, "Git workspace inventory is unavailable")
		return gitworkspace.Stats{}, false
	}
	return stats, true
}

func (h *Handler) loadMutableGitWorkspace(
	w http.ResponseWriter,
	r *http.Request,
	id string,
) (gitWorkspaceManagerAPI, gitworkspace.WorkspaceInfo, bool) {
	manager, err := h.gitWorkspaceManager()
	if err != nil {
		writeGitWorkspaceUnavailable(w, "Git workspace inventory is unavailable")
		return nil, gitworkspace.WorkspaceInfo{}, false
	}
	stats, err := manager.Stats(r.Context())
	if err != nil {
		writeGitWorkspaceUnavailable(w, "Git workspace inventory is unavailable")
		return nil, gitworkspace.WorkspaceInfo{}, false
	}
	workspace, found := findPublicGitWorkspace(stats, id)
	if !found {
		writeGitWorkspaceNotFound(w)
		return nil, gitworkspace.WorkspaceInfo{}, false
	}
	if gitWorkspaceDropped(workspace) {
		writeGitWorkspaceDropped(w)
		return nil, gitworkspace.WorkspaceInfo{}, false
	}
	if workspace.LockedBy != nil || workspace.Status == "locked" {
		writeGitWorkspaceLocked(w)
		return nil, gitworkspace.WorkspaceInfo{}, false
	}
	return manager, workspace, true
}

func (h *Handler) writeGitWorkspaceMutationFailure(
	w http.ResponseWriter,
	r *http.Request,
	manager gitWorkspaceManagerAPI,
	id string,
	action string,
) {
	stats, err := manager.Stats(r.Context())
	if err == nil {
		workspace, found := findPublicGitWorkspace(stats, id)
		switch {
		case !found:
			writeGitWorkspaceNotFound(w)
			return
		case gitWorkspaceDropped(workspace):
			writeGitWorkspaceDropped(w)
			return
		case workspace.LockedBy != nil || workspace.Status == "locked":
			writeGitWorkspaceLocked(w)
			return
		}
	}
	writeCollectionError(
		w, http.StatusConflict, "git_workspace_"+action+"_failed",
		"Git workspace "+action+" failed", -1, nil,
	)
}

func (h *Handler) gitWorkspaceManager() (gitWorkspaceManagerAPI, error) {
	if h != nil && h.loadGitWorkspaceManager != nil {
		return h.loadGitWorkspaceManager()
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return nil, fmt.Errorf("load git workspace configuration: %w", err)
	}
	return gitworkspace.NewManager(gitworkspace.Options{
		RootDir:             cfg.GitWorkspaceRootPath(),
		MaxTotalSizeBytes:   cfg.GitWorkspaces.EffectiveMaxTotalSizeBytes(),
		IgnoredCleanupDelay: cfg.GitWorkspaces.EffectiveIgnoredCleanupDelay(),
		DropDelay:           cfg.GitWorkspaces.EffectiveDropDelay(),
	})
}

func pageGitWorkspaces(
	items []gitWorkspaceSummary,
	request collectionListRequest,
) (collectionquery.PageResult[gitWorkspaceSummary], error) {
	return collectionquery.Paginate(
		items,
		request.Query,
		request.Cursor,
		request.Limit,
		request.Now,
		collectionquery.PageOptions[gitWorkspaceSummary]{
			ID:         func(item gitWorkspaceSummary) (string, error) { return item.ID, nil },
			ValidateID: validGitWorkspaceID,
			Clone:      func(item gitWorkspaceSummary) gitWorkspaceSummary { return item },
			Resolve:    resolveGitWorkspaceCollectionField,
		},
	)
}

func pageGitWorkspaceHistory(
	items []gitWorkspaceHistorySummary,
	request collectionListRequest,
) (collectionquery.PageResult[gitWorkspaceHistorySummary], error) {
	return collectionquery.Paginate(
		items,
		request.Query,
		request.Cursor,
		request.Limit,
		request.Now,
		collectionquery.PageOptions[gitWorkspaceHistorySummary]{
			ID:         func(item gitWorkspaceHistorySummary) (string, error) { return item.ID, nil },
			ValidateID: validGitWorkspaceHistoryID,
			Clone:      func(item gitWorkspaceHistorySummary) gitWorkspaceHistorySummary { return item },
			Resolve:    resolveGitWorkspaceHistoryCollectionField,
		},
	)
}

func resolveGitWorkspaceCollectionField(
	item gitWorkspaceSummary,
	field collectionquery.Field,
	_ time.Time,
) (collectionquery.FieldValue, bool) {
	switch field {
	case "id":
		return collectionquery.StringValue(item.ID), true
	case "repository":
		return collectionquery.StringValue(item.Repository), true
	case "branch":
		return collectionquery.StringValue(item.Branch), true
	case "status":
		return collectionquery.EnumValue(item.Status), true
	case "locked":
		return collectionquery.BooleanValue(item.Locked), true
	case "dirty":
		return collectionquery.BooleanValue(item.Dirty), true
	case "size":
		return collectionquery.NumberValue(float64(item.Size)), true
	case "ignored":
		return collectionquery.NumberValue(float64(item.Ignored)), true
	case "updated":
		return collectionquery.TimestampValue(item.Updated), true
	default:
		return collectionquery.FieldValue{}, false
	}
}

func resolveGitWorkspaceHistoryCollectionField(
	item gitWorkspaceHistorySummary,
	field collectionquery.Field,
	_ time.Time,
) (collectionquery.FieldValue, bool) {
	switch field {
	case "action":
		return collectionquery.StringValue(item.Action), true
	case "workspace":
		return collectionquery.StringValue(item.Workspace), true
	case "repository":
		return collectionquery.StringValue(item.Repository), true
	case "agent":
		return collectionquery.StringValue(item.Agent), true
	case "time":
		return collectionquery.TimestampValue(item.Time), true
	default:
		return collectionquery.FieldValue{}, false
	}
}

func projectGitWorkspaceSummaries(
	workspaces []gitworkspace.WorkspaceInfo,
) []gitWorkspaceSummary {
	items := make([]gitWorkspaceSummary, 0, len(workspaces))
	for _, workspace := range workspaces {
		if !validGitWorkspaceID(workspace.ID) {
			continue
		}
		items = append(items, gitWorkspaceSummaryFromInfo(workspace))
	}
	return items
}

func gitWorkspaceSummaryFromInfo(
	workspace gitworkspace.WorkspaceInfo,
) gitWorkspaceSummary {
	return gitWorkspaceSummary{
		ID: workspace.ID, Repository: gitWorkspaceRepositoryLabel(workspace),
		Branch: gitWorkspaceBranch(workspace), Status: workspace.Status,
		Locked: workspace.LockedBy != nil || workspace.Status == "locked",
		Dirty:  workspace.Dirty, Size: safeGitWorkspaceByteCount(workspace.SizeBytes),
		Ignored: safeGitWorkspaceByteCount(workspace.IgnoredBytes), Updated: workspace.UpdatedAt,
	}
}

func gitWorkspaceDetailFromInfo(
	workspace gitworkspace.WorkspaceInfo,
) gitWorkspaceDetail {
	detail := gitWorkspaceDetail{
		gitWorkspaceSummary: gitWorkspaceSummaryFromInfo(workspace),
		RepositoryID:        workspace.RepoID, RemoteURL: workspace.RemoteURL,
		UpstreamURL: workspace.UpstreamURL, Ref: workspace.Ref, Path: workspace.Path,
		PreservedBranch: workspace.PreservedBranch, Created: workspace.CreatedAt,
		Dropped: workspace.DroppedAt,
	}
	if !workspace.LastWorkAt.IsZero() {
		value := workspace.LastWorkAt
		detail.LastWork = &value
	}
	if !workspace.LastCleanedAt.IsZero() {
		value := workspace.LastCleanedAt
		detail.LastCleaned = &value
	}
	if workspace.LockedBy != nil {
		detail.LockedBy = &gitWorkspaceSafeLock{
			AgentID:     workspace.LockedBy.AgentID,
			LockedAt:    workspace.LockedBy.LockedAt,
			HeartbeatAt: workspace.LockedBy.HeartbeatAt,
		}
	}
	return detail
}

func projectGitWorkspaceHistory(
	stats gitworkspace.Stats,
) []gitWorkspaceHistorySummary {
	workspaceRepositories := make(map[string]string, len(stats.Workspaces))
	publicRepositories := make(map[string]string, len(stats.Repositories))
	for _, workspace := range stats.Workspaces {
		if !validGitWorkspaceID(workspace.ID) {
			continue
		}
		label := gitWorkspaceRepositoryLabel(workspace)
		workspaceRepositories[workspace.ID] = label
		if workspace.RepoID != "" {
			publicRepositories[workspace.RepoID] = label
		}
	}
	for _, repository := range stats.Repositories {
		if _, exists := publicRepositories[repository.ID]; !exists {
			publicRepositories[repository.ID] = safeGitWorkspaceRepositoryLabel(
				repository.RemoteURL,
			)
		}
	}
	items := make([]gitWorkspaceHistorySummary, 0, len(stats.History))
	for _, entry := range stats.History {
		if !validGitWorkspaceHistoryID(entry.ID) ||
			(entry.WorkspaceID != "" && workspaceRepositories[entry.WorkspaceID] == "") ||
			(entry.RepoID != "" && publicRepositories[entry.RepoID] == "") {
			continue
		}
		repository := publicRepositories[entry.RepoID]
		if repository == "" {
			repository = workspaceRepositories[entry.WorkspaceID]
		}
		items = append(items, gitWorkspaceHistorySummary{
			ID: entry.ID, Action: safeGitWorkspaceHistoryValue(entry.Action, 128),
			Workspace: entry.WorkspaceID, Repository: repository,
			Agent: safeGitWorkspaceHistoryValue(entry.AgentID, 256), Time: entry.Time,
		})
	}
	return items
}

func gitWorkspaceSummariesForInfos(
	public []gitWorkspaceSummary,
	infos []gitworkspace.WorkspaceInfo,
) []gitWorkspaceSummary {
	byID := make(map[string]gitWorkspaceSummary, len(public))
	for _, item := range public {
		byID[item.ID] = item
	}
	items := make([]gitWorkspaceSummary, 0, len(infos))
	for _, info := range infos {
		if item, found := byID[info.ID]; found {
			items = append(items, item)
		}
	}
	return items
}

func findPublicGitWorkspace(
	stats gitworkspace.Stats,
	id string,
) (gitworkspace.WorkspaceInfo, bool) {
	for _, workspace := range stats.Workspaces {
		if workspace.ID == id && validGitWorkspaceID(workspace.ID) {
			return workspace, true
		}
	}
	return gitworkspace.WorkspaceInfo{}, false
}

func gitWorkspaceAggregateFromStats(
	stats gitworkspace.Stats,
) gitWorkspaceAggregate {
	return gitWorkspaceAggregate{
		MaxTotalSizeBytes:          safeGitWorkspaceByteCount(stats.MaxTotalSizeBytes),
		IgnoredCleanupDelaySeconds: stats.IgnoredCleanupDelaySeconds,
		DropDelaySeconds:           stats.DropDelaySeconds,
		TotalSizeBytes:             safeGitWorkspaceByteCount(stats.TotalSizeBytes),
		IgnoredBytes:               safeGitWorkspaceByteCount(stats.IgnoredBytes),
		RepositoryCount:            stats.RepositoryCount, WorkspaceCount: stats.WorkspaceCount,
		LockedWorkspaceCount: stats.LockedWorkspaceCount,
	}
}

func gitWorkspaceRepositoryLabel(workspace gitworkspace.WorkspaceInfo) string {
	if upstream := safeGitWorkspaceRepositoryLabel(workspace.UpstreamURL); upstream != "" {
		return upstream
	}
	return safeGitWorkspaceRepositoryLabel(workspace.RemoteURL)
}

func safeGitWorkspaceRepositoryLabel(value string) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, ""))
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		base := filepath.Base(filepath.Clean(value))
		if base == "." || base == string(filepath.Separator) || base == "" {
			return "local repository"
		}
		return "local/" + base
	}
	if len(value) > collectionquery.MaxSuggestedValueBytes {
		value = value[:collectionquery.MaxSuggestedValueBytes]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

func safeGitWorkspaceHistoryValue(value string, maximum int) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, ""))
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}

func safeGitWorkspaceByteCount(value int64) int64 {
	if value < 0 {
		return 0
	}
	if value > gitWorkspaceMaximumSettingsBytes {
		return gitWorkspaceMaximumSettingsBytes
	}
	return value
}

func gitWorkspaceBranch(workspace gitworkspace.WorkspaceInfo) string {
	for _, value := range []string{
		workspace.CurrentBranch, workspace.PreservedBranch, workspace.Ref,
	} {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return "detached"
}

func validGitWorkspaceID(value string) bool {
	return utf8.ValidString(value) && gitWorkspaceIDPattern.MatchString(value)
}

func requirePublicGitWorkspaceID(w http.ResponseWriter, value string) bool {
	if validGitWorkspaceID(value) {
		return true
	}
	if utf8.ValidString(value) && gitWorkspaceReservedPrivateIDPattern.MatchString(value) {
		writeGitWorkspaceNotFound(w)
		return false
	}
	writeCollectionError(
		w, http.StatusBadRequest, "invalid_git_workspace_id",
		"Invalid git workspace ID", -1, nil,
	)
	return false
}

func validGitWorkspaceHistoryID(value string) bool {
	return utf8.ValidString(value) && gitWorkspaceHistoryIDPattern.MatchString(value)
}

func gitWorkspaceDropped(workspace gitworkspace.WorkspaceInfo) bool {
	return workspace.DroppedAt != nil || workspace.Status == "dropped"
}

func validGitWorkspaceSettings(settings gitWorkspaceSettings) bool {
	return settings.MaxTotalSizeBytes >= 0 &&
		settings.MaxTotalSizeBytes <= gitWorkspaceMaximumSettingsBytes &&
		settings.IgnoredCleanupDelaySeconds >= 0 &&
		settings.IgnoredCleanupDelaySeconds <= gitWorkspaceMaximumDelaySeconds &&
		settings.DropDelaySeconds >= 0 &&
		settings.DropDelaySeconds <= gitWorkspaceMaximumDelaySeconds
}

func gitWorkspaceSettingsResponseForConfig(
	cfg *config.Config,
	revision string,
	effects agentEffects,
) gitWorkspaceSettingsResponse {
	configured := gitWorkspaceSettings{
		MaxTotalSizeBytes: safeGitWorkspaceByteCount(
			cfg.GitWorkspaces.MaxTotalSizeBytes,
		),
		IgnoredCleanupDelaySeconds: cfg.GitWorkspaces.IgnoredCleanupDelaySeconds,
		DropDelaySeconds:           cfg.GitWorkspaces.DropDelaySeconds,
	}
	effective := gitWorkspaceSettings{
		MaxTotalSizeBytes: safeGitWorkspaceByteCount(
			cfg.GitWorkspaces.EffectiveMaxTotalSizeBytes(),
		),
		IgnoredCleanupDelaySeconds: int(
			cfg.GitWorkspaces.EffectiveIgnoredCleanupDelay().Seconds(),
		),
		DropDelaySeconds: int(cfg.GitWorkspaces.EffectiveDropDelay().Seconds()),
	}
	return gitWorkspaceSettingsResponse{
		Configured: configured, Effective: effective,
		ConfigRevision: revision, Effects: effects,
	}
}

func writeGitWorkspaceUnavailable(w http.ResponseWriter, message string) {
	writeCollectionError(
		w, http.StatusInternalServerError, "git_workspaces_unavailable",
		message, -1, nil,
	)
}

func writeGitWorkspaceSettingsUnavailable(w http.ResponseWriter) {
	writeCollectionError(
		w, http.StatusInternalServerError, "git_workspace_settings_unavailable",
		"Git workspace settings are unavailable", -1, nil,
	)
}

func writeGitWorkspaceNotFound(w http.ResponseWriter) {
	writeCollectionError(
		w, http.StatusNotFound, "git_workspace_not_found",
		"Git workspace not found", -1, nil,
	)
}

func writeGitWorkspaceLocked(w http.ResponseWriter) {
	writeCollectionError(
		w, http.StatusConflict, "git_workspace_locked",
		"Git workspace is locked", -1, nil,
	)
}

func writeGitWorkspaceDropped(w http.ResponseWriter) {
	writeCollectionError(
		w, http.StatusConflict, "git_workspace_dropped",
		"Git workspace was already dropped", -1, nil,
	)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}
