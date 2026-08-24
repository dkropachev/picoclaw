package prworkspace

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/workflows"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

func TestSessionGateWorkingContextBinderSeparatesAgentOwnership(t *testing.T) {
	backend, closeStore := newGateWorkingContextTestBackend(t, t.TempDir())
	defer closeStore()
	binder := testGateWorkingContextBinder(backend)

	mainRef, err := binder.Bind(t.Context(), testGateWorkingContextRequest("main"))
	if err != nil {
		t.Fatal(err)
	}
	reviewerRef, err := binder.Bind(t.Context(), testGateWorkingContextRequest("reviewer"))
	if err != nil {
		t.Fatal(err)
	}
	if mainRef.Session == reviewerRef.Session || mainRef.ExpectedRevision == "" || reviewerRef.ExpectedRevision == "" {
		t.Fatalf("agent session refs main=%#v reviewer=%#v", mainRef, reviewerRef)
	}
	for _, ref := range []workflows.ReadOnlySessionRef{mainRef, reviewerRef} {
		snapshot, found, readErr := backend.ReadSessionSnapshot(t.Context(), ref.Session)
		if readErr != nil || !found {
			t.Fatalf("read %q = found %v, error %v", ref.AgentID, found, readErr)
		}
		if snapshot.Scope == nil || snapshot.Scope.AgentID != ref.AgentID ||
			snapshot.Scope.Channel != "review" || snapshot.Scope.Values["pr_workspace"] != testCreateInput().Workspace.ID ||
			snapshot.Revision != ref.ExpectedRevision {
			t.Fatalf("snapshot for %q = %#v", ref.AgentID, snapshot)
		}
	}

	invalid := testGateWorkingContextRequest("main")
	invalid.Context.WorkspaceID = "devw_22222222222222222222222222222222"
	if _, err := binder.Bind(t.Context(), invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-workspace context error = %v, want invalid", err)
	}
}

func TestSessionGateWorkingContextBinderSurvivesStoreRestart(t *testing.T) {
	directory := t.TempDir()
	backend, closeFirst := newGateWorkingContextTestBackend(t, directory)
	ref, err := testGateWorkingContextBinder(backend).Bind(t.Context(), testGateWorkingContextRequest("main"))
	if err != nil {
		closeFirst()
		t.Fatal(err)
	}
	closeFirst()

	reopened, closeReopened := newGateWorkingContextTestBackend(t, directory)
	defer closeReopened()
	snapshot, found, err := reopened.ReadSessionSnapshot(t.Context(), ref.Session)
	if err != nil || !found {
		t.Fatalf("reopened snapshot = found %v, error %v", found, err)
	}
	if snapshot.Revision != ref.ExpectedRevision || snapshot.Scope == nil || snapshot.Scope.AgentID != "main" ||
		len(snapshot.History) != 2 || snapshot.History[1].Content != "Keep this guidance after restart." {
		t.Fatalf("reopened snapshot = %#v, ref %#v", snapshot, ref)
	}
}

type staleAfterBindGateContext struct {
	binder  GateWorkingContextBinder
	backend *session.JSONLBackend
}

func (stale staleAfterBindGateContext) Bind(
	ctx context.Context,
	request GateWorkingContextRequest,
) (workflows.ReadOnlySessionRef, error) {
	ref, err := stale.binder.Bind(ctx, request)
	if err == nil {
		stale.backend.AddMessage(ref.Session, "user", "mutation after the exact projection")
	}
	return ref, err
}

func TestWorkflowGateEvaluatorRejectsWorkingContextChangedBeforeFreeze(t *testing.T) {
	backend, closeStore := newGateWorkingContextTestBackend(t, t.TempDir())
	defer closeStore()
	baseBinder := testGateWorkingContextBinder(backend)
	agent := &prGateV3Agent{
		reader: backend,
		fieldValues: map[string]any{
			"action": "approve",
		},
	}
	workspace := t.TempDir()
	executor := &workflows.Executor{
		WorkspaceDir: workspace,
		Store:        workflows.NewFileRunStore(workspace),
		Agents:       agent,
	}
	configured := config.DefaultPRLifecycleConfig()
	action := gatetypes.GateAction{
		Type: gatetypes.GateActionAI, AgentID: "main",
		Prompt:  "Check the exact PR workspace context.",
		Session: workflows.AgentSessionPrivate, History: "read_only", Cache: "none", Tools: workflows.AgentToolsNone,
	}
	configured.WorkflowConfigurations["working"] = config.PRLifecycleWorkflowConfiguration{
		Name:           "Working only",
		DeferredIssues: config.PRLifecycleDeferredIssueConfig{Mode: config.PRLifecycleDeferredIssuesAsk},
		Bindings: []config.PRLifecycleGateBinding{{
			WorkflowRef: PRLifecycleWorkflowRef, GateRef: "gates.charter-confirm", Action: &action,
		}},
	}
	configured.RepositoryAssignments["https://github.com|repo-stale"] = "working"
	evaluator := &WorkflowGateEvaluator{
		Config: configured, Executor: executor,
		WorkingContext: staleAfterBindGateContext{binder: baseBinder, backend: backend},
	}
	request := testGateWorkingContextRequest("main")
	_, err := evaluator.Start(t.Context(), GateRequest{
		WorkspaceID: request.WorkspaceID, WorkspaceVersion: request.WorkspaceVersion,
		ProviderOrigin: "https://github.com", RepositoryID: "repo-stale",
		DecisionPoint:  "pr.charter.confirm",
		Subject:        map[string]any{"charter": map[string]any{"type": "fix"}},
		SubjectDigest:  "sha256:2222222222222222222222222222222222222222222222222222222222222222",
		WorkingContext: request.Context,
	})
	if !errors.Is(err, workflows.ErrPrivateWorkflowContext) {
		t.Fatalf("Start() error = %v, want private-context rejection", err)
	}
	runs, listErr := executor.Store.ListRuns(t.Context())
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(runs) != 0 || len(agent.requests) != 0 || len(agent.captures) != 1 {
		t.Fatalf(
			"stale context effects = runs %d, requests %d, captures %d",
			len(runs),
			len(agent.requests),
			len(agent.captures),
		)
	}
}

func testGateWorkingContextRequest(agentID string) GateWorkingContextRequest {
	now := time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC)
	workspaceID := testCreateInput().Workspace.ID
	return GateWorkingContextRequest{
		WorkspaceID: workspaceID, WorkspaceVersion: 4, AgentID: agentID,
		Context: PRContextBundle{
			WorkspaceID: workspaceID,
			Provider:    ProviderSnapshot{Repository: "octo/repo", HeadSHA: "head"},
			Messages: []Message{{
				ID: "pms_33333333333333333333333333333333", Role: "user",
				Content: "Keep this guidance after restart.", CreatedAt: now,
			}},
		},
	}
}

func testGateWorkingContextBinder(backend *session.JSONLBackend) *SessionGateWorkingContextBinder {
	return &SessionGateWorkingContextBinder{
		Acquire: func(ctx context.Context, _ string) (context.Context, session.SessionStore, func(), error) {
			return ctx, backend, func() {}, nil
		},
	}
}

func newGateWorkingContextTestBackend(t *testing.T, directory string) (*session.JSONLBackend, func()) {
	t.Helper()
	lower, err := memory.NewJSONLStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	return session.NewJSONLBackend(lower), func() {
		if err := lower.Close(); err != nil {
			t.Errorf("close gate working-context store: %v", err)
		}
	}
}
