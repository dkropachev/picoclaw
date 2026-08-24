package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/health"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/reviews"
	"github.com/sipeed/picoclaw/pkg/tools"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestRepositoryReviewPublicationRouteLifecycle(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.MCP.Enabled = false
	messageBus := bus.NewMessageBus()
	loop := agent.NewAgentLoop(cfg, messageBus, &startupBlockedProvider{reason: "not used"})
	t.Cleanup(func() {
		loop.Stop()
		messageBus.Close()
		loop.Close()
	})

	manager, err := channels.NewManager(cfg, messageBus, nil)
	if err != nil {
		t.Fatal(err)
	}
	healthServer := health.NewServer("127.0.0.1", 0, "publication-token")
	manager.SetupHTTPServer("127.0.0.1:0", healthServer)
	running := &services{
		ChannelManager: manager,
		HealthServer:   healthServer,
		authToken:      "publication-token",
	}
	if err := prepareRepositoryReviewPublicationRoute(running, loop); err != nil {
		t.Fatal(err)
	}
	if running.repositoryReviewPublicationHandler == nil || running.repositoryReviewPublicationRelease == nil {
		t.Fatal("publication route was not installed")
	}
	if err := prepareRepositoryReviewPublicationRoute(running, loop); err != nil {
		t.Fatalf("refresh installed route: %v", err)
	}
	running.repositoryReviewPublicationHandler = nil
	if err := prepareRepositoryReviewPublicationRoute(running, loop); err == nil {
		t.Fatal("accepted an installed route without handler state")
	}
	releaseRepositoryReviewPublicationRoute(running)
	if running.repositoryReviewPublicationRelease != nil || running.repositoryReviewPublicationHandler != nil {
		t.Fatal("publication route state survived release")
	}
	releaseRepositoryReviewPublicationRoute(running)
	releaseRepositoryReviewPublicationRoute(nil)
}

func TestRepositoryReviewPublicationRouteRejectsUnsafeRuntime(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	messageBus := bus.NewMessageBus()
	loop := agent.NewAgentLoop(cfg, messageBus, &startupBlockedProvider{reason: "not used"})
	t.Cleanup(func() {
		loop.Stop()
		messageBus.Close()
		loop.Close()
	})
	manager, err := channels.NewManager(cfg, messageBus, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetupHTTPServer("127.0.0.1:0", health.NewServer("127.0.0.1", 0, "other"))

	for _, running := range []*services{
		nil,
		{},
		{ChannelManager: manager},
		{ChannelManager: manager, HealthServer: health.NewServer("127.0.0.1", 0, "other"), authToken: "wrong"},
	} {
		if routeErr := prepareRepositoryReviewPublicationRoute(running, loop); routeErr == nil {
			t.Fatalf("prepareRepositoryReviewPublicationRoute(%#v) succeeded", running)
		}
	}
	if nilLoopErr := prepareRepositoryReviewPublicationRoute(
		&services{ChannelManager: manager},
		nil,
	); nilLoopErr == nil {
		t.Fatal("accepted a nil agent loop")
	}

	release, err := manager.RegisterHTTPRoute(repositoryReviewPublicationRoute, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	running := &services{
		ChannelManager: manager,
		HealthServer:   health.NewServer("127.0.0.1", 0, "token"),
		authToken:      "token",
	}
	if err := prepareRepositoryReviewPublicationRoute(running, loop); err == nil {
		t.Fatal("duplicate route registration succeeded")
	}
}

func TestRepositoryReviewPublicationHandlerRejectsMalformedRequests(t *testing.T) {
	handler := newRepositoryReviewPublicationHandler(nil)
	validPath := repositoryReviewPublicationRoute + "rrp_missing/issue-drafts/rid_missing/publish"
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
		code   string
	}{
		{
			name:   "route",
			method: http.MethodGet,
			path:   validPath,
			body:   `{}`,
			status: http.StatusNotFound,
			code:   "not_found",
		},
		{
			name:   "syntax",
			method: http.MethodPost,
			path:   validPath,
			body:   `{`,
			status: http.StatusBadRequest,
			code:   "invalid_request",
		},
		{
			name:   "version",
			method: http.MethodPost,
			path:   validPath,
			body:   `{"expected_version":0}`,
			status: http.StatusBadRequest,
			code:   "invalid_request",
		},
		{
			name:   "unknown",
			method: http.MethodPost,
			path:   validPath,
			body:   `{"expected_version":1,"extra":true}`,
			status: http.StatusBadRequest,
			code:   "invalid_request",
		},
		{
			name:   "trailing",
			method: http.MethodPost,
			path:   validPath,
			body:   `{"expected_version":1}{}`,
			status: http.StatusBadRequest,
			code:   "invalid_request",
		},
		{
			name:   "runtime",
			method: http.MethodPost,
			path:   validPath,
			body:   `{"expected_version":1}`,
			status: http.StatusServiceUnavailable,
			code:   "publication_unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" ||
				response.Header().Get("X-Content-Type-Options") != "nosniff" ||
				response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
				t.Fatalf("response headers = %#v", response.Header())
			}
		})
	}
}

func TestRepositoryReviewPublicationHandlerReportsMissingLedger(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	messageBus := bus.NewMessageBus()
	loop := agent.NewAgentLoop(cfg, messageBus, &startupBlockedProvider{reason: "not used"})
	t.Cleanup(func() {
		loop.Stop()
		messageBus.Close()
		loop.Close()
	})
	handler := newRepositoryReviewPublicationHandler(loop)
	request := httptest.NewRequest(
		http.MethodPost,
		repositoryReviewPublicationRoute+"rrp_missing/issue-drafts/rid_missing/publish",
		strings.NewReader(`{"expected_version":1}`),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":"not_found"`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestRepositoryReviewPublicationHandlerRejectsMissingToolRuntime(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.MCP.Enabled = false
	messageBus := bus.NewMessageBus()
	loop := agent.NewAgentLoop(cfg, messageBus, &startupBlockedProvider{reason: "not used"})
	t.Cleanup(func() {
		loop.Stop()
		messageBus.Close()
		loop.Close()
	})
	store, state, draft := repositoryReviewPublicationTestDraft(t, cfg.WorkspacePath(), "owner/repo")
	_ = store
	defaultAgent := loop.GetRegistry().GetDefaultAgent()
	toolRegistry := defaultAgent.Tools
	defaultAgent.Tools = nil
	t.Cleanup(func() { defaultAgent.Tools = toolRegistry })

	request := httptest.NewRequest(
		http.MethodPost,
		repositoryReviewPublicationRoute+state.ID+"/issue-drafts/"+draft.ID+"/publish",
		strings.NewReader(`{"expected_version":1}`),
	)
	response := httptest.NewRecorder()
	newRepositoryReviewPublicationHandler(loop).ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), `"code":"publication_unavailable"`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestRepositoryReviewPublicationHandlerReportsClaimPersistenceFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can bypass directory permission checks")
	}
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.MCP.Enabled = false
	messageBus := bus.NewMessageBus()
	loop := agent.NewAgentLoop(cfg, messageBus, &startupBlockedProvider{reason: "not used"})
	t.Cleanup(func() {
		loop.Stop()
		messageBus.Close()
		loop.Close()
	})
	store, state, draft := repositoryReviewPublicationTestDraft(t, cfg.WorkspacePath(), "owner/repo")
	root := filepath.Join(cfg.WorkspacePath(), "repository_reviews")
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	restore := func() { _ = os.Chmod(root, 0o700) }
	t.Cleanup(restore)

	request := httptest.NewRequest(
		http.MethodPost,
		repositoryReviewPublicationRoute+state.ID+"/issue-drafts/"+draft.ID+"/publish",
		strings.NewReader(`{"expected_version":`+strconv.FormatInt(draft.Version, 10)+`}`),
	)
	response := httptest.NewRecorder()
	newRepositoryReviewPublicationHandler(loop).ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable ||
		!strings.Contains(response.Body.String(), `"code":"publication_unavailable"`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	restore()
	current, found, err := store.GetByID(state.ID)
	if err != nil || !found || len(current.IssueDrafts) != 1 ||
		current.IssueDrafts[0].State != repoaudit.IssueDraftEditing {
		t.Fatalf("claim failure state=%#v found=%v err=%v", current.IssueDrafts, found, err)
	}
}

func TestRepositoryReviewPublicationHandlerCoversDurableDraftStates(t *testing.T) {
	tests := []struct {
		name       string
		repository string
		prepare    func(t *testing.T, store repoaudit.Store, state repoaudit.RepositoryState, draft repoaudit.IssueDraft) (string, int64)
		status     int
	}{
		{
			name:       "missing draft",
			repository: "owner/repo",
			prepare: func(_ *testing.T, _ repoaudit.Store, _ repoaudit.RepositoryState, draft repoaudit.IssueDraft) (string, int64) {
				return "rid_missing", draft.Version
			},
			status: http.StatusNotFound,
		},
		{
			name:       "stale draft",
			repository: "owner/repo",
			prepare: func(_ *testing.T, _ repoaudit.Store, _ repoaudit.RepositoryState, draft repoaudit.IssueDraft) (string, int64) {
				return draft.ID, draft.Version + 1
			},
			status: http.StatusConflict,
		},
		{
			name:       "posted draft",
			repository: "owner/repo",
			prepare: func(t *testing.T, store repoaudit.Store, _ repoaudit.RepositoryState, draft repoaudit.IssueDraft) (string, int64) {
				_, claimed, didClaim, err := store.ClaimIssueDraftPublication("owner/repo", draft.ID, draft.Version)
				if err != nil || !didClaim {
					t.Fatalf("claim draft: claimed=%v err=%v", didClaim, err)
				}
				_, posted, err := store.SetIssueDraftPublication(
					"owner/repo", draft.ID, claimed.Version, repoaudit.IssueDraftPosted,
					"12", "https://github.com/owner/repo/issues/12",
				)
				if err != nil {
					t.Fatal(err)
				}
				return posted.ID, posted.Version
			},
			status: http.StatusOK,
		},
		{
			name:       "invalid GitHub identity",
			repository: "/tmp/repo",
			prepare: func(_ *testing.T, _ repoaudit.Store, _ repoaudit.RepositoryState, draft repoaudit.IssueDraft) (string, int64) {
				return draft.ID, draft.Version
			},
			status: http.StatusBadRequest,
		},
		{
			name:       "provider unavailable",
			repository: "owner/repo",
			prepare: func(_ *testing.T, _ repoaudit.Store, _ repoaudit.RepositoryState, draft repoaudit.IssueDraft) (string, int64) {
				return draft.ID, draft.Version
			},
			status: http.StatusServiceUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.Workspace = t.TempDir()
			cfg.Tools.MCP.Enabled = false
			messageBus := bus.NewMessageBus()
			loop := agent.NewAgentLoop(cfg, messageBus, &startupBlockedProvider{reason: "not used"})
			t.Cleanup(func() {
				loop.Stop()
				messageBus.Close()
				loop.Close()
			})

			store, state, draft := repositoryReviewPublicationTestDraft(t, cfg.WorkspacePath(), test.repository)
			draftID, expectedVersion := test.prepare(t, store, state, draft)
			request := httptest.NewRequest(
				http.MethodPost,
				repositoryReviewPublicationRoute+state.ID+"/issue-drafts/"+draftID+"/publish",
				strings.NewReader(`{"expected_version":`+strconv.FormatInt(expectedVersion, 10)+`}`),
			)
			response := httptest.NewRecorder()
			newRepositoryReviewPublicationHandler(loop).ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("response = %d %s, want %d", response.Code, response.Body.String(), test.status)
			}
		})
	}
}

func repositoryReviewPublicationTestDraft(
	t *testing.T,
	workspace string,
	repository string,
) (repoaudit.Store, repoaudit.RepositoryState, repoaudit.IssueDraft) {
	t.Helper()
	store := repoaudit.NewStore(workspace)
	file := repoaudit.FileRef{
		Path: "service.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 10,
		Category: "code", Mode: "100644",
	}
	plan, err := store.Plan(t.Context(), repository, "commit-a", "inventory-a", []repoaudit.FileRef{file}, false)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := store.Record(t.Context(), repoaudit.RecordRequest{
		Plan: plan, RunID: "publication-run",
		Observations: []repoaudit.Observation{{
			Model: "review-a", ScopeFiles: []repoaudit.FileRef{file},
			Findings: []repoaudit.FindingCandidate{{
				Severity: "high", Title: "Lost update", File: file.Path,
				Evidence: "unfenced write", Impact: "data loss",
				Validation: repoaudit.Validation{Status: "confirmed", Summary: "reproduced"},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, draft, err := store.PrepareIssue(repoaudit.IssueDraftRequest{
		Repository: repository, FindingIDs: []string{recorded.State.Findings[0].ID},
		ExpectedVersion: recorded.State.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, state, draft
}

type repositoryReviewPublicationMCPManager struct {
	searchText   string
	createText   string
	searchErr    error
	createErr    error
	beforeReturn func(string)
}

func (manager *repositoryReviewPublicationMCPManager) CallTool(
	_ context.Context,
	_, toolName string,
	_ map[string]any,
) (*sdkmcp.CallToolResult, error) {
	text, err := manager.searchText, manager.searchErr
	if toolName == reviews.GitHubIssueWriteTool {
		text, err = manager.createText, manager.createErr
	}
	if manager.beforeReturn != nil {
		manager.beforeReturn(toolName)
	}
	if err != nil {
		return nil, err
	}
	return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: text}}}, nil
}

func TestRepositoryReviewPublicationHandlerReportsPersistenceFailures(t *testing.T) {
	tests := []struct {
		name         string
		initialState repoaudit.IssueDraftState
		searchText   func(repoaudit.IssueDraft) string
		createText   string
		createErr    error
		poisonTool   string
	}{
		{
			name: "recovered issue update", poisonTool: reviews.GitHubSearchIssuesTool,
			searchText: func(draft repoaudit.IssueDraft) string {
				return `{"items":[{"id":31,"html_url":"https://github.com/owner/repo/issues/31","body":"` +
					repositoryReviewIssueMarker(draft.ID) + `"}]}`
			},
		},
		{
			name: "publishing transition", initialState: repoaudit.IssueDraftPublishing,
			poisonTool: reviews.GitHubSearchIssuesTool,
			searchText: func(repoaudit.IssueDraft) string { return `{"items":[]}` },
		},
		{
			name: "ambiguous transition", poisonTool: reviews.GitHubIssueWriteTool,
			searchText: func(repoaudit.IssueDraft) string { return `{"items":[]}` },
			createErr:  errors.New("connection reset by peer"),
		},
		{
			name: "definite failure reset", poisonTool: reviews.GitHubIssueWriteTool,
			searchText: func(repoaudit.IssueDraft) string { return `{"items":[]}` },
			createErr:  errors.New("HTTP status: 422 validation failed"),
		},
		{
			name: "posted transition", poisonTool: reviews.GitHubIssueWriteTool,
			searchText: func(repoaudit.IssueDraft) string { return `{"items":[]}` },
			createText: `{"id":32,"html_url":"https://github.com/owner/repo/issues/32"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.Workspace = workspace
			cfg.Tools.MCP.Enabled = false
			messageBus := bus.NewMessageBus()
			loop := agent.NewAgentLoop(cfg, messageBus, &startupBlockedProvider{reason: "not used"})
			t.Cleanup(func() {
				loop.Stop()
				messageBus.Close()
				loop.Close()
			})
			manager := &repositoryReviewPublicationMCPManager{
				createText: test.createText, createErr: test.createErr,
			}
			loop.RegisterTool(tools.NewMCPTool(manager, reviews.DefaultGitHubMCPServer, &sdkmcp.Tool{
				Name:        reviews.GitHubSearchIssuesTool,
				InputSchema: map[string]any{"type": "object", "additionalProperties": true},
			}))
			loop.RegisterTool(tools.NewMCPTool(manager, reviews.DefaultGitHubMCPServer, &sdkmcp.Tool{
				Name:        reviews.GitHubIssueWriteTool,
				InputSchema: map[string]any{"type": "object", "additionalProperties": true},
			}))
			store, state, draft := repositoryReviewPublicationTestDraft(t, workspace, "owner/repo")
			if test.initialState == repoaudit.IssueDraftPublishing {
				_, claimed, didClaim, err := store.ClaimIssueDraftPublication("owner/repo", draft.ID, draft.Version)
				if err != nil || !didClaim {
					t.Fatalf("claim draft: claimed=%v err=%v", didClaim, err)
				}
				draft = claimed
			}
			manager.searchText = test.searchText(draft)
			manager.beforeReturn = func(toolName string) {
				if toolName != test.poisonTool {
					return
				}
				root := filepath.Join(workspace, "repository_reviews")
				if err := os.RemoveAll(root); err != nil {
					t.Errorf("remove store root: %v", err)
					return
				}
				if err := os.WriteFile(root, []byte("not a directory"), 0o600); err != nil {
					t.Errorf("replace store root: %v", err)
				}
			}
			request := httptest.NewRequest(
				http.MethodPost,
				repositoryReviewPublicationRoute+state.ID+"/issue-drafts/"+draft.ID+"/publish",
				strings.NewReader(`{"expected_version":`+strconv.FormatInt(draft.Version, 10)+`}`),
			)
			response := httptest.NewRecorder()
			newRepositoryReviewPublicationHandler(loop).ServeHTTP(response, request)
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestRepositoryReviewPublicationHandlerPublishesAndRecovers(t *testing.T) {
	tests := []struct {
		name         string
		configure    func(manager *repositoryReviewPublicationMCPManager, draft repoaudit.IssueDraft)
		initialState repoaudit.IssueDraftState
		wantStatus   int
		wantDraft    repoaudit.IssueDraftState
		wantOutcome  string
		wantExternal string
	}{
		{
			name: "recover existing issue",
			configure: func(manager *repositoryReviewPublicationMCPManager, draft repoaudit.IssueDraft) {
				manager.searchText = `{"items":[{"id":21,"html_url":"https://github.com/owner/repo/issues/21","body":"` +
					repositoryReviewIssueMarker(
						draft.ID,
					) + `"}]}`
			},
			wantStatus: http.StatusOK, wantDraft: repoaudit.IssueDraftPosted,
			wantExternal: "21",
		},
		{
			name: "create issue",
			configure: func(manager *repositoryReviewPublicationMCPManager, _ repoaudit.IssueDraft) {
				manager.searchText = `{"items":[]}`
				manager.createText = `{"id":22,"html_url":"https://github.com/owner/repo/issues/22"}`
			},
			wantStatus: http.StatusOK, wantDraft: repoaudit.IssueDraftPosted,
			wantExternal: "22",
		},
		{
			name: "already publishing becomes unknown",
			configure: func(manager *repositoryReviewPublicationMCPManager, _ repoaudit.IssueDraft) {
				manager.searchText = `{"items":[]}`
			},
			initialState: repoaudit.IssueDraftPublishing,
			wantStatus:   http.StatusAccepted, wantDraft: repoaudit.IssueDraftUnknown,
			wantOutcome: "unknown",
		},
		{
			name: "already unknown remains unknown",
			configure: func(manager *repositoryReviewPublicationMCPManager, _ repoaudit.IssueDraft) {
				manager.searchText = `{"items":[]}`
			},
			initialState: repoaudit.IssueDraftUnknown,
			wantStatus:   http.StatusAccepted, wantDraft: repoaudit.IssueDraftUnknown,
			wantOutcome: "unknown",
		},
		{
			name: "ambiguous create",
			configure: func(manager *repositoryReviewPublicationMCPManager, _ repoaudit.IssueDraft) {
				manager.searchText = `{"items":[]}`
				manager.createErr = errors.New("connection reset by peer")
			},
			wantStatus: http.StatusAccepted, wantDraft: repoaudit.IssueDraftUnknown,
			wantOutcome: "unknown",
		},
		{
			name: "definite create failure",
			configure: func(manager *repositoryReviewPublicationMCPManager, _ repoaudit.IssueDraft) {
				manager.searchText = `{"items":[]}`
				manager.createErr = errors.New("HTTP status: 422 validation failed")
			},
			wantStatus: http.StatusServiceUnavailable, wantDraft: repoaudit.IssueDraftEditing,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.Agents.Defaults.Workspace = t.TempDir()
			cfg.Tools.MCP.Enabled = false
			messageBus := bus.NewMessageBus()
			loop := agent.NewAgentLoop(cfg, messageBus, &startupBlockedProvider{reason: "not used"})
			t.Cleanup(func() {
				loop.Stop()
				messageBus.Close()
				loop.Close()
			})
			manager := &repositoryReviewPublicationMCPManager{}
			loop.RegisterTool(tools.NewMCPTool(manager, reviews.DefaultGitHubMCPServer, &sdkmcp.Tool{
				Name: reviews.GitHubSearchIssuesTool,
				InputSchema: map[string]any{
					"type": "object", "additionalProperties": true,
				},
			}))
			loop.RegisterTool(tools.NewMCPTool(manager, reviews.DefaultGitHubMCPServer, &sdkmcp.Tool{
				Name: reviews.GitHubIssueWriteTool,
				InputSchema: map[string]any{
					"type": "object", "additionalProperties": true,
				},
			}))

			store, state, draft := repositoryReviewPublicationTestDraft(t, cfg.WorkspacePath(), "owner/repo")
			if test.initialState != "" {
				_, claimed, didClaim, err := store.ClaimIssueDraftPublication("owner/repo", draft.ID, draft.Version)
				if err != nil || !didClaim {
					t.Fatalf("claim draft: claimed=%v err=%v", didClaim, err)
				}
				draft = claimed
				if test.initialState == repoaudit.IssueDraftUnknown {
					_, draft, err = store.SetIssueDraftPublication(
						"owner/repo", draft.ID, draft.Version, repoaudit.IssueDraftUnknown, "", "",
					)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			test.configure(manager, draft)
			request := httptest.NewRequest(
				http.MethodPost,
				repositoryReviewPublicationRoute+state.ID+"/issue-drafts/"+draft.ID+"/publish",
				strings.NewReader(`{"expected_version":`+strconv.FormatInt(draft.Version, 10)+`}`),
			)
			response := httptest.NewRecorder()
			newRepositoryReviewPublicationHandler(loop).ServeHTTP(response, request)
			if response.Code != test.wantStatus ||
				(test.wantOutcome != "" && !strings.Contains(response.Body.String(), `"outcome":"`+test.wantOutcome+`"`)) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			persisted, found, err := store.GetByID(state.ID)
			if err != nil || !found {
				t.Fatalf("persisted state found=%v err=%v", found, err)
			}
			persistedDraft, found := repositoryReviewDraft(persisted, draft.ID)
			if !found || persistedDraft.State != test.wantDraft ||
				(test.wantExternal != "" && persistedDraft.ExternalID != test.wantExternal) {
				t.Fatalf("persisted draft = %#v", persistedDraft)
			}
		})
	}
}

func TestRepositoryReviewPublicationHelpersCoverBoundaryResponses(t *testing.T) {
	state := repoaudit.RepositoryState{IssueDrafts: []repoaudit.IssueDraft{{ID: "rid_match"}}}
	if draft, found := repositoryReviewDraft(state, "rid_match"); !found || draft.ID != "rid_match" {
		t.Fatalf("draft = %#v, found=%v", draft, found)
	}
	if _, found := repositoryReviewDraft(state, "rid_missing"); found {
		t.Fatal("missing draft was found")
	}

	for _, test := range []struct {
		err    error
		found  bool
		status int
	}{
		{err: nil, found: false, status: http.StatusNotFound},
		{err: os.ErrNotExist, found: true, status: http.StatusNotFound},
		{err: repoaudit.ErrConflict, found: true, status: http.StatusConflict},
		{err: errors.New("disk unavailable"), found: true, status: http.StatusServiceUnavailable},
	} {
		response := httptest.NewRecorder()
		writeRepositoryReviewPublicationStoreError(response, test.err, test.found)
		if response.Code != test.status {
			t.Fatalf("write store error status=%d, want %d", response.Code, test.status)
		}
	}

	invalidRequests := []*http.Request{
		nil,
		httptest.NewRequest(http.MethodGet, repositoryReviewPublicationRoute+"r/issue-drafts/d/publish", nil),
		httptest.NewRequest(http.MethodPost, repositoryReviewPublicationRoute+"r/issue-drafts/d/publish?q=1", nil),
		httptest.NewRequest(http.MethodPost, repositoryReviewPublicationRoute+"r/other/d/publish", nil),
	}
	for _, request := range invalidRequests {
		if _, _, ok := repositoryReviewPublicationRouteIDs(request); ok {
			t.Fatalf("accepted route %#v", request)
		}
	}

	for _, repository := range []string{
		strings.Repeat("a", 101) + "/repo",
		"owner/.",
		"owner/..",
		"owner/repo!",
	} {
		if validRepositoryReviewGitHubIdentity(repository) {
			t.Fatalf("accepted invalid GitHub identity %q", repository)
		}
	}
	if repositoryReviewIssueCreateAmbiguous(
		errors.Join(reviews.ErrWorkspaceProviderCallNotDispatched, errors.New("invalid request")),
	) {
		t.Fatal("pre-dispatch workspace provider error became ambiguous")
	}
}

func TestRepositoryReviewProviderHelpersRejectMalformedProviderResults(t *testing.T) {
	tests := []struct {
		name       string
		repository string
		result     map[string]any
		err        error
	}{
		{name: "identity", repository: "invalid", result: map[string]any{"text": `{}`}},
		{name: "dispatch", repository: "owner/repo", err: errors.New("write failed")},
		{name: "json", repository: "owner/repo", result: map[string]any{"text": `not-json`}},
		{
			name:       "foreign",
			repository: "owner/repo",
			result:     map[string]any{"text": `{"id":1,"html_url":"https://github.com/other/repo/issues/1"}`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, err := reviews.NewGitHubProvider(prWorkspaceProviderToolRunnerFunc(func(
				_ context.Context,
				_ workflows.ToolRequest,
			) (map[string]any, error) {
				return test.result, test.err
			}), "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := createRepositoryReviewIssue(
				t.Context(), provider, test.repository, repoaudit.IssueDraft{Title: "title"}, "marker",
			); err == nil {
				t.Fatal("createRepositoryReviewIssue() succeeded")
			}
		})
	}
}

func TestFindRepositoryReviewIssueHandlesProviderWireShapes(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		err   error
		found bool
		fail  bool
	}{
		{name: "provider", err: errors.New("search failed"), fail: true},
		// A scalar is valid provider JSON but neither accepted issue-search wire
		// shape, so the publication boundary must reject both envelope and direct
		// array decoding paths.
		{name: "invalid", text: `42`, fail: true},
		{
			name:  "direct",
			text:  `[{"id":2,"html_url":"https://github.com/owner/repo/issues/2","body":"marker"}]`,
			found: true,
		},
		{name: "empty", text: `{"items":[]}`},
		{
			name: "foreign",
			text: `{"items":[{"id":2,"html_url":"https://github.com/other/repo/issues/2","body":"marker"},{"id":3,"html_url":"https://github.com/owner/repo/issues/3","body":"other"}]}`,
		},
		{
			name: "multiple",
			text: `{"items":[{"id":2,"html_url":"https://github.com/owner/repo/issues/2","body":"marker"},{"id":3,"html_url":"https://github.com/owner/repo/issues/3","body":"marker"}]}`,
			fail: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, err := reviews.NewGitHubProvider(prWorkspaceProviderToolRunnerFunc(func(
				_ context.Context,
				_ workflows.ToolRequest,
			) (map[string]any, error) {
				return map[string]any{"text": test.text}, test.err
			}), "")
			if err != nil {
				t.Fatal(err)
			}
			_, found, findErr := findRepositoryReviewIssue(t.Context(), provider, "owner/repo", "marker")
			if found != test.found || (findErr != nil) != test.fail {
				t.Fatalf("found=%v err=%v", found, findErr)
			}
		})
	}
}
