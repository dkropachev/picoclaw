package prworkspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

var errGateAggregatePersistence = errors.New("gate aggregate persistence failed")

type failGateAggregatePersistenceOnceStore struct {
	Store
	remaining int
}

type mixedAssignedGateAgent struct {
	reader   session.SnapshotReader
	captures []workflows.ReadOnlySessionRef
	requests []workflows.AgentRequest
}

func (agent *mixedAssignedGateAgent) CaptureReadOnlySession(
	ctx context.Context,
	ref workflows.ReadOnlySessionRef,
) (*workflows.FrozenReadOnlySession, error) {
	agent.captures = append(agent.captures, ref)
	snapshot, found, err := agent.reader.ReadSessionSnapshot(ctx, ref.Session)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("mixed gate session was not found")
	}
	return &workflows.FrozenReadOnlySession{
		AgentID: ref.AgentID, Snapshot: snapshot,
		HistoryRevision: "sha256:mixed-assigned-gate-history",
		FrozenMedia:     media.FrozenSet{Version: media.FrozenSetVersion},
	}, nil
}

func (agent *mixedAssignedGateAgent) RunAgent(
	_ context.Context,
	request workflows.AgentRequest,
) (map[string]any, error) {
	agent.requests = append(agent.requests, request)
	response := fmt.Sprintf(`{"outcome":"pass","reason":"%s passed","questions":[]}`, request.AgentID)
	structured := workflows.ValidateAgentStructuredOutput(response, request.Output)
	if !structured.Valid {
		return nil, fmt.Errorf("mixed gate response is invalid: %s", structured.Error)
	}
	return map[string]any{
		"text": response, "structured": structured.Structured,
		"structured_json": structured.RawJSON, "structured_valid": true,
	}, nil
}

func TestWorkflowGateEvaluatorExecutesAssignedMixedProfileWithFrozenWorkingContext(t *testing.T) {
	now := time.Date(2026, 8, 14, 0, 30, 0, 0, time.UTC)
	configured := config.DefaultPRLifecycleConfig()
	configured.GateProfiles["mixed"] = config.PRLifecycleGateProfile{
		Name: "Mixed execution",
		Workflows: map[string]workflows.GateWorkflowSpec{
			"pr.charter.confirm": {
				ID: "mixed-charter", Name: "Mixed charter gate",
				Purpose: workflows.GatePurposeAuthorization, DecisionPoint: "pr.charter.confirm",
				Stages: []workflows.GateStageSpec{
					{ID: "explicit", Kind: workflows.GateZero},
					{ID: "verified", Kind: workflows.GateDeterministic, Title: "Verified subject", When: "inputs.gate_subject.verified == true"},
					{ID: "isolated", Kind: workflows.GateAIIsolatedContext, Title: "Isolated audit", AgentID: "reviewer", Criteria: "Check the frozen subject."},
					{ID: "working", Kind: workflows.GateAIWorkingContext, Title: "Workspace discussion", AgentID: "main", Criteria: "Check unresolved workspace guidance."},
					{ID: "human", Kind: workflows.GateHuman, Title: "Final approval", Questions: []any{"Approve the charter?"}},
				},
			},
		},
	}
	configured.RepositoryAssignments["https://github.com|repo-assigned"] = "mixed"
	if err := configured.Validate(); err != nil {
		t.Fatal(err)
	}
	lower, err := memory.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lower.Close() })
	sessions := session.NewJSONLBackend(lower)
	agent := &mixedAssignedGateAgent{reader: sessions}
	workflowWorkspace := t.TempDir()
	executor := &workflows.Executor{
		WorkspaceDir: workflowWorkspace,
		Store:        workflows.NewFileRunStore(workflowWorkspace),
		Agents:       agent,
	}
	binder := &SessionGateWorkingContextBinder{
		Acquire: func(ctx context.Context, agentID string) (context.Context, session.SessionStore, func(), error) {
			if agentID != "main" {
				return ctx, nil, func() {}, fmt.Errorf("unexpected working-context agent %q", agentID)
			}
			return ctx, sessions, func() {}, nil
		},
	}
	evaluator := &WorkflowGateEvaluator{
		Config: configured, Executor: executor, WorkingContext: binder,
		Now: func() time.Time { return now },
	}
	workspaceID := "prw_11111111111111111111111111111111"
	gate, err := evaluator.Start(t.Context(), GateRequest{
		WorkspaceID: workspaceID, WorkspaceVersion: 7,
		ProviderOrigin: "https://github.com", RepositoryID: "repo-assigned",
		DecisionPoint: "pr.charter.confirm", Purpose: "authorization",
		Subject:       map[string]any{"verified": true, "charter": map[string]any{"type": "feature"}},
		SubjectDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		WorkingContext: PRContextBundle{
			WorkspaceID: workspaceID,
			Provider:    ProviderSnapshot{Repository: "octo/repo", HeadSHA: "head"},
			Messages: []Message{{
				ID: "pms_11111111111111111111111111111111", Role: "user",
				Content: "Keep the compatibility decision explicit.", CreatedAt: now,
			}},
			Corrections: []Correction{{
				ID: "pco_11111111111111111111111111111111", OriginalClaim: "implicit compatibility",
				Correction: "No backward compatibility is required.", CreatedAt: now,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gate.runtime == nil || gate.runtime.ProfileID != "mixed" ||
		gate.State != ExecutionWaitingUser || len(gate.Turns) != 5 {
		t.Fatalf("started mixed gate = %#v", gate)
	}
	for _, index := range []int{0, 1, 2, 3} {
		if gate.Turns[index].Status != "answered" || gate.Turns[index].Outcome != GatePass {
			t.Fatalf("started turn %d = %#v", index, gate.Turns[index])
		}
	}
	if gate.Turns[4].Status != "waiting" {
		t.Fatalf("human turn = %#v, want waiting", gate.Turns[4])
	}
	if len(agent.captures) != 1 || agent.captures[0].AgentID != "main" ||
		agent.captures[0].Session == "" || agent.captures[0].ExpectedRevision == "" {
		t.Fatalf("working-context captures = %#v", agent.captures)
	}
	if len(agent.requests) != 2 || !agent.requests[0].EphemeralSession ||
		agent.requests[0].FrozenReadOnlySession != nil || agent.requests[1].EphemeralSession ||
		agent.requests[1].FrozenReadOnlySession == nil {
		t.Fatalf("mixed AI requests = %#v", agent.requests)
	}
	history := agent.requests[1].FrozenReadOnlySession.Snapshot.History
	if len(history) != 2 || !strings.Contains(history[0].Content, "No backward compatibility is required.") ||
		history[1].Content != "Keep the compatibility decision explicit." {
		t.Fatalf("frozen working-context history = %#v", history)
	}

	completed, err := evaluator.Respond(t.Context(), gate, GatePass, map[string]any{"approved": true}, "Approved.")
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != ExecutionSucceeded || completed.Outcome != GatePass {
		t.Fatalf("completed mixed gate = %#v", completed)
	}
	for index, turn := range completed.Turns {
		if turn.Status != "answered" || turn.Outcome != GatePass {
			t.Fatalf("completed turn %d = %#v", index, turn)
		}
	}
}

func (store *failGateAggregatePersistenceOnceStore) Mutate(ctx context.Context, mutation Mutation) (MutationResult, error) {
	if store.remaining > 0 && len(mutation.Patch.ReplaceGates) > 0 {
		store.remaining--
		current, _ := store.Store.Get(ctx, mutation.WorkspaceID)
		return MutationResult{Aggregate: current}, errGateAggregatePersistence
	}
	return store.Store.Mutate(ctx, mutation)
}

func TestWorkflowGateEvaluatorNormalizesTypedDomainSubject(t *testing.T) {
	now := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	evaluator := &WorkflowGateEvaluator{
		Config: config.DefaultPRLifecycleConfig(),
		Now:    func() time.Time { return now },
	}
	gate, err := evaluator.Start(t.Context(), GateRequest{
		WorkspaceID:    "prw_11111111111111111111111111111111",
		ProviderOrigin: "https://github.com",
		RepositoryID:   "1333775490",
		DecisionPoint:  "pr.charter.confirm",
		Purpose:        "authorization",
		SubjectDigest:  "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		Subject: map[string]any{
			"charter": Charter{
				ID: "pcr_11111111111111111111111111111111", Revision: 1,
				Type: PRTypeFeature, Goal: "Add time-aware greetings",
				CreatedAt: now,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gate.State != ExecutionWaitingUser || len(gate.Turns) != 1 ||
		gate.Turns[0].Kind != "human" || gate.runtime == nil {
		t.Fatalf("gate = %#v", gate)
	}
	if gate.Evidence.CharterType != PRTypeFeature || gate.Evidence.CharterGoal != "Add time-aware greetings" {
		t.Fatalf("gate evidence = %#v", gate.Evidence)
	}
}

func TestTurnsFromCompilationProjectsPersistedHumanStep(t *testing.T) {
	configured := config.DefaultPRLifecycleConfig()
	_, profile, _, err := configured.ProfileForRepository("https://github.com", "1")
	if err != nil {
		t.Fatal(err)
	}
	compilation, err := workflows.CompileGateWorkflowV2(
		profile.Workflows["pr.charter.confirm"],
		map[string]any{"charter": map[string]any{"type": "feature"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	stepID := compilation.Stages[0].StepID
	run := &workflows.Run{Steps: map[string]workflows.StepExecution{
		"gates/" + stepID: {
			ID: stepID, Status: workflows.RunStatusSucceeded,
			Outputs: map[string]any{"response": map[string]any{
				"decision": "pass", "answers": map[string]any{"approved": true},
				"comment": "Charter is appropriately bounded.",
			}},
		},
	}}
	turns := turnsFromCompilation(compilation, run)
	if len(turns) != 1 || turns[0].Status != "answered" || turns[0].Outcome != GatePass ||
		turns[0].Answers["approved"] != true || turns[0].Comment != "Charter is appropriately bounded." {
		t.Fatalf("turns = %#v", turns)
	}
}

func TestWorkflowGateEvaluatorRetryReconcilesTerminalRunAfterAggregatePersistenceFailure(t *testing.T) {
	now := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	workflowWorkspace := t.TempDir()
	executor := &workflows.Executor{
		WorkspaceDir: workflowWorkspace,
		Store:        workflows.NewFileRunStore(workflowWorkspace),
	}
	evaluator := &WorkflowGateEvaluator{
		Config: config.DefaultPRLifecycleConfig(), Executor: executor,
		Now: func() time.Time { return now },
	}
	input := testCreateInput()
	input.Provider.ProviderOrigin = "https://github.com"
	input.Provider.RepositoryID = "repo-gate-recovery"
	input.Workspace.ProviderOrigin = input.Provider.ProviderOrigin
	input.Workspace.RepositoryID = input.Provider.RepositoryID
	input.Workspace.Phase = PhaseCharter
	baseStore := NewMemoryStore()
	created, err := baseStore.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	charter := Charter{
		ID: "pcr_33333333333333333333333333333333", Revision: 1, Type: PRTypeFix,
		Goal: "Recover a committed workflow decision", BaseSHA: input.Provider.BaseSHA,
		HeadSHA: input.Provider.HeadSHA, CreatedAt: now,
	}
	subject := map[string]any{"charter": charter}
	subjectDigest, err := fingerprintValue(subject)
	if err != nil {
		t.Fatal(err)
	}
	gate, err := evaluator.Start(t.Context(), GateRequest{
		WorkspaceID: input.Workspace.ID, ProviderOrigin: input.Provider.ProviderOrigin,
		RepositoryID: input.Provider.RepositoryID, DecisionPoint: "pr.charter.confirm",
		Purpose: "authorization", Subject: subject, SubjectDigest: subjectDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	gate.TargetID = charter.ID
	if gate.State != ExecutionWaitingUser || gate.runtime == nil || gate.runtime.WorkflowRunID == "" {
		t.Fatalf("workflow gate = %#v", gate)
	}
	waitingState := ExecutionWaitingGate
	seeded, err := baseStore.Mutate(t.Context(), Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-seed-workflow-gate-recovery",
		Patch: AggregatePatch{
			ExecutionState: &waitingState, AppendCharters: []Charter{charter}, AppendGates: []GateRun{gate},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	failingStore := &failGateAggregatePersistenceOnceStore{Store: baseStore, remaining: 1}
	service, err := NewService(ServiceConfig{Store: failingStore, Gates: evaluator, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	request := RespondGateRequest{
		WorkspaceID: input.Workspace.ID, GateRunID: gate.ID,
		ExpectedVersion: seeded.Aggregate.Workspace.Version,
		RequestID:       "request-answer-workflow-before-aggregate-failure",
		Decision:        GatePass, Answers: map[string]any{"approved": true}, Comment: "Approved.",
	}
	failed, err := service.RespondGate(t.Context(), request)
	if !errors.Is(err, errGateAggregatePersistence) {
		t.Fatalf("first RespondGate() error = %v", err)
	}
	if failed.Workspace.Version != seeded.Aggregate.Workspace.Version {
		t.Fatalf("failed aggregate version = %d, want %d", failed.Workspace.Version, seeded.Aggregate.Workspace.Version)
	}
	for _, task := range mustListWorkflowGateTasks(t, executor, gate.runtime.WorkflowRunID) {
		if task.Status == workflows.HumanTaskStatusWaiting {
			t.Fatalf("workflow task remained waiting after committed response: %#v", task)
		}
	}
	stillWaiting, err := baseStore.Get(t.Context(), input.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	persistedGate, _ := findGate(stillWaiting.Gates, gate.ID)
	if persistedGate.State != ExecutionWaitingUser {
		t.Fatalf("aggregate gate unexpectedly changed after failed mutation: %#v", persistedGate)
	}

	request.RequestID = "request-reconcile-terminal-workflow-gate"
	reconciled, err := service.RespondGate(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	reconciledGate, found := findGate(reconciled.Gates, gate.ID)
	if !found || reconciledGate.State != ExecutionSucceeded || reconciledGate.Outcome != GatePass ||
		reconciled.Workspace.Phase != PhaseReview || !reconciled.Charters[0].Confirmed {
		t.Fatalf("reconciled aggregate = workspace %#v gate %#v charter %#v", reconciled.Workspace, reconciledGate, reconciled.Charters)
	}
}

func mustListWorkflowGateTasks(t *testing.T, executor *workflows.Executor, runID string) []workflows.WorkflowHumanTask {
	t.Helper()
	tasks, err := executor.ListHumanTasks(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return tasks
}
