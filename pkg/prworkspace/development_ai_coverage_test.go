package prworkspace

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type developmentAIStub struct {
	response map[string]any
	err      error
	requests []IsolatedAIRequest
}

func (stub *developmentAIStub) RunIsolated(
	_ context.Context,
	request IsolatedAIRequest,
) (IsolatedAIResult, error) {
	stub.requests = append(stub.requests, request)
	if stub.err != nil {
		return IsolatedAIResult{}, stub.err
	}
	return successfulIsolatedAIResult(stub.response), nil
}

type planningEvidenceStub struct {
	evidence json.RawMessage
	err      error
}

func (stub planningEvidenceStub) LoadPlanningEvidence(
	context.Context,
	string,
	ProviderSnapshot,
) (json.RawMessage, error) {
	return stub.evidence, stub.err
}

func developmentConversationResponse(scopeChange bool) map[string]any {
	return map[string]any{
		"scope_change": scopeChange,
		"explanation":  "This request is classified against the confirmed charter.",
	}
}

func developmentAskResponse() map[string]any {
	return map[string]any{"reply": "The current candidate matches the confirmed charter."}
}

func developmentPlanningResponse(withFinding bool) map[string]any {
	findings := []any{}
	if withFinding {
		findings = append(findings, map[string]any{
			"severity": "high", "title": "Implement the requested behavior",
			"file": "pkg/example.go", "message": "The confirmed behavior is not implemented.",
			"evidence": "The repository evidence contains no implementation.",
			"impact":   "The feature remains unavailable.", "validation": "Reviewed the supplied evidence.",
			"scope_distance": "S0_exact", "change_size": "XS", "type_compatible": true,
			"scope_confidence": 1.0, "scope_explanation": "Directly required by the charter.",
			"charter_clauses": []any{"Implement the requested behavior"},
		})
	}
	return map[string]any{
		"summary": "Bounded feature plan", "findings": findings,
		"coverage": map[string]any{
			"reviewed_areas": []any{"pkg"}, "unreviewed_areas": []any{},
			"tests_considered": []any{"go test"}, "residual_risks": []any{},
		},
	}
}

func seededDevelopmentAIService(
	t *testing.T,
	phase Phase,
	state ExecutionState,
	runner IsolatedAIRunner,
) (*Service, Aggregate) {
	t.Helper()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	input := testCreateInput()
	input.RequestID = "request-development-ai-seed"
	input.Provider.Intent = IntentImplementFeature
	input.Provider.SourceKind = SourceBrief
	input.Provider.SourceID = "brief-development-ai"
	input.Provider.SourceNumber = 0
	input.Provider.PullRequestID = ""
	input.Provider.PullNumber = 0
	input.Provider.Title = "Add the requested behavior"
	input.Provider.Body = "Implement the feature from this bounded brief."
	input.Provider.BaseRef = "main"
	input.Provider.BaseSHA = "base-commit"
	input.Provider.HeadSHA = "base-commit"
	input.Provider.ObservedAt = now
	input.Workspace.Intent = input.Provider.Intent
	input.Workspace.SourceKind = input.Provider.SourceKind
	input.Workspace.SourceID = input.Provider.SourceID
	input.Workspace.SourceNumber = 0
	input.Workspace.PullRequestID = ""
	input.Workspace.PullNumber = 0
	input.Workspace.ProviderHeadSHA = input.Provider.HeadSHA
	input.Workspace.CreatedAt = now
	store := NewMemoryStore()
	created, err := store.Create(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	charter := charterInvariantRecord(input, "Implement the requested behavior", 1, now)
	charter.Type = PRTypeFeature
	charter.Confirmed = true
	charterID := charter.ID
	seeded, err := store.Mutate(t.Context(), Mutation{
		WorkspaceID: input.Workspace.ID, ExpectedVersion: created.Aggregate.Workspace.Version,
		RequestID: "request-development-ai-charter",
		Patch: AggregatePatch{
			Phase: &phase, ExecutionState: &state, ActiveCharterID: &charterID,
			AppendCharters: []Charter{charter},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceConfig{
		Store:            store,
		AI:               runner,
		PlanningEvidence: planningEvidenceStub{evidence: json.RawMessage(`{"files":["pkg/example.go"]}`)},
		Now:              func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, seeded.Aggregate
}

func TestDevelopmentConversationAskAndSteerLifecycle(t *testing.T) {
	t.Run("ask", func(t *testing.T) {
		runner := &developmentAIStub{response: developmentAskResponse()}
		service, aggregate := seededDevelopmentAIService(t, PhaseTriage, ExecutionQueued, runner)
		page, err := service.Conversation(t.Context(), aggregate.Workspace.ID)
		if err != nil || page.Revision != 0 || len(page.Messages) != 0 {
			t.Fatalf("initial conversation = %#v, %v", page, err)
		}
		page, err = service.SendConversationMessage(t.Context(), ConversationMessageRequest{
			WorkspaceID: aggregate.Workspace.ID, ExpectedRevision: page.Revision,
			RequestID: "request-development-ask", Mode: "ask", Content: "What is the current status?",
		})
		if err != nil || page.Revision != 2 || len(page.Messages) != 2 ||
			page.Messages[0].Status != "answered" || page.Messages[1].Role != "assistant" {
			t.Fatalf("ask result = %#v, %v", page, err)
		}
		if len(runner.requests) != 1 || runner.requests[0].Operation != "development.ask" ||
			!strings.Contains(runner.requests[0].UserPrompt, "What is the current status?") {
			t.Fatalf("ask request = %#v", runner.requests)
		}
	})

	t.Run("in-scope steer requeues failed implementation", func(t *testing.T) {
		runner := &developmentAIStub{response: developmentConversationResponse(false)}
		service, aggregate := seededDevelopmentAIService(t, PhaseImplementation, ExecutionFailed, runner)
		page, err := service.SendConversationMessage(t.Context(), ConversationMessageRequest{
			WorkspaceID: aggregate.Workspace.ID, ExpectedRevision: 0,
			RequestID: "request-development-steer", Mode: "steer", Content: "Use the existing retry helper.",
		})
		if err != nil || page.Revision != 1 || page.Messages[0].Status != "queued" {
			t.Fatalf("steer result = %#v, %v", page, err)
		}
		updated, getErr := service.Get(t.Context(), aggregate.Workspace.ID)
		if getErr != nil || updated.Workspace.ExecutionState != ExecutionQueued {
			t.Fatalf("steered workspace = %#v, %v", updated.Workspace, getErr)
		}
	})

	t.Run("scope-changing steer asks for clarification", func(t *testing.T) {
		runner := &developmentAIStub{response: developmentConversationResponse(true)}
		service, aggregate := seededDevelopmentAIService(t, PhaseTriage, ExecutionQueued, runner)
		page, err := service.SendConversationMessage(t.Context(), ConversationMessageRequest{
			WorkspaceID: aggregate.Workspace.ID, ExpectedRevision: 0,
			RequestID: "request-development-scope", Mode: "steer", Content: "Also deploy this change.",
		})
		if err != nil || page.Revision != 2 || page.Messages[0].Status != "needs_clarification" ||
			page.Messages[1].Status != "needs_clarification" {
			t.Fatalf("scope steer = %#v, %v", page, err)
		}
	})
}

func TestDevelopmentConversationCandidateAndPublicationFences(t *testing.T) {
	runner := &developmentAIStub{response: developmentAskResponse()}
	service, aggregate := seededDevelopmentAIService(t, PhaseValidation, ExecutionSucceeded, runner)
	now := service.now().UTC()
	repair := RepairAttempt{
		ID: "pra_11111111111111111111111111111111", StageRunID: "psr_11111111111111111111111111111111",
		Number: 1, State: ExecutionSucceeded, WorkspaceID: aggregate.Workspace.ID,
		CandidateSHA: "candidate-commit", StartedAt: now,
		PublicationFence: &ImplementationPublicationFence{BaseCommit: "base-commit", Tip: "candidate-commit"},
	}
	_, err := service.store.Mutate(t.Context(), Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-development-candidate", Patch: AggregatePatch{AppendRepairs: []RepairAttempt{repair}},
	})
	if err != nil {
		t.Fatal(err)
	}
	service.candidateEvidence = fixedCandidateEvidenceLoader{value: CandidateEvidence{
		CandidateSHA: repair.CandidateSHA, CandidateDiff: "+candidate", EvidenceDigest: "sha256:candidate",
	}}
	page, err := service.SendConversationMessage(t.Context(), ConversationMessageRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedRevision: 0,
		RequestID: "request-development-candidate-ask", Mode: "ask", Content: "Review this candidate.",
		CandidateRevision: repair.CandidateSHA,
	})
	if err != nil || page.Revision != 2 || !strings.Contains(runner.requests[0].UserPrompt, "candidate-commit") {
		t.Fatalf("candidate ask = %#v, requests=%#v, err=%v", page, runner.requests, err)
	}

	service, aggregate = seededDevelopmentAIService(t, PhasePublication, ExecutionWaitingGate,
		&developmentAIStub{response: developmentConversationResponse(false)})
	publication := Publication{
		ID: "ppb_11111111111111111111111111111111", Kind: PublicationBranchPush,
		State: ExecutionQueued, PayloadDigest: "sha256:publication", CreatedAt: now, UpdatedAt: now,
	}
	_, err = service.store.Mutate(t.Context(), Mutation{
		WorkspaceID:     aggregate.Workspace.ID,
		ExpectedVersion: aggregate.Workspace.Version,
		RequestID:       "request-development-publication",
		Patch:           AggregatePatch{AppendPublications: []Publication{publication}},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err = service.SendConversationMessage(t.Context(), ConversationMessageRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedRevision: 0,
		RequestID: "request-development-publication-steer", Mode: "steer", Content: "Use the bounded helper.",
	})
	if err != nil || page.Revision != 1 {
		t.Fatalf("publication steer = %#v, %v", page, err)
	}
	updated, err := service.Get(t.Context(), aggregate.Workspace.ID)
	if err != nil || updated.Workspace.Phase != PhaseImplementation ||
		updated.Publications[0].State != ExecutionStale {
		t.Fatalf("publication invalidation = %#v, %v", updated, err)
	}

	service, aggregate = seededDevelopmentAIService(t, PhasePublication, ExecutionRunning,
		&developmentAIStub{response: developmentConversationResponse(false)})
	_, err = service.store.Mutate(t.Context(), Mutation{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-development-running-publication",
		Patch: AggregatePatch{AppendPublications: []Publication{{
			ID: publication.ID, Kind: PublicationBranchPush, State: ExecutionRunning,
			PayloadDigest: publication.PayloadDigest, CreatedAt: now, UpdatedAt: now,
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SendConversationMessage(t.Context(), ConversationMessageRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedRevision: 0,
		RequestID: "request-development-running-steer", Mode: "steer", Content: "Use the bounded helper.",
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("running publication steer error = %v", err)
	}
}

func TestDevelopmentConversationRejectsInvalidAndUnavailableRequests(t *testing.T) {
	service, aggregate := seededDevelopmentAIService(t, PhasePlanning, ExecutionQueued, nil)
	invalid := []ConversationMessageRequest{
		{},
		{
			WorkspaceID:      aggregate.Workspace.ID,
			ExpectedRevision: -1,
			RequestID:        "request-negative",
			Mode:             "ask",
			Content:          "status",
		},
		{WorkspaceID: aggregate.Workspace.ID, RequestID: "request-mode", Mode: "write", Content: "status"},
		{WorkspaceID: aggregate.Workspace.ID, RequestID: "request-empty", Mode: "ask", Content: " "},
	}
	for _, request := range invalid {
		if _, err := service.SendConversationMessage(t.Context(), request); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid request %#v error = %v", request, err)
		}
	}
	if _, err := service.SendConversationMessage(t.Context(), ConversationMessageRequest{
		WorkspaceID: aggregate.Workspace.ID, RequestID: "request-no-ai-0001", Mode: "ask", Content: "status",
	}); err == nil || !strings.Contains(err.Error(), "AI is unavailable") {
		t.Fatalf("missing AI error = %v", err)
	}
	if _, err := service.SendConversationMessage(t.Context(), ConversationMessageRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedRevision: 1,
		RequestID: "request-stale-chat", Mode: "steer", Content: "status",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale conversation error = %v", err)
	}
	if _, err := service.SendConversationMessage(t.Context(), ConversationMessageRequest{
		WorkspaceID: aggregate.Workspace.ID, RequestID: "request-stale-candidate", Mode: "ask", Content: "status",
		CandidateRevision: "missing-candidate",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale candidate error = %v", err)
	}
	if !service.claimImplementation(aggregate.Workspace.ID) {
		t.Fatal("failed to claim implementation fixture")
	}
	_, err := service.SendConversationMessage(t.Context(), ConversationMessageRequest{
		WorkspaceID: aggregate.Workspace.ID, RequestID: "request-busy-chat", Mode: "ask", Content: "status",
	})
	service.releaseImplementation(aggregate.Workspace.ID)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("busy conversation error = %v", err)
	}

	for _, test := range []struct {
		name     string
		response map[string]any
		runErr   error
	}{
		{name: "runner error", runErr: context.Canceled},
		{name: "invalid ask", response: map[string]any{"reply": 7}},
		{name: "invalid steer", response: map[string]any{"scope_change": "no", "explanation": "invalid"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &developmentAIStub{response: test.response, err: test.runErr}
			mode := "ask"
			if test.name == "invalid steer" {
				mode = "steer"
			}
			candidate, seeded := seededDevelopmentAIService(t, PhaseTriage, ExecutionQueued, runner)
			_, sendErr := candidate.SendConversationMessage(t.Context(), ConversationMessageRequest{
				WorkspaceID: seeded.Workspace.ID,
				RequestID:   "request-invalid-ai-" + strings.ReplaceAll(test.name, " ", "-"),
				Mode:        mode,
				Content:     "status",
			})
			if sendErr == nil {
				t.Fatal("invalid AI response was accepted")
			}
		})
	}
}

func TestRunFeaturePlanningPersistsBoundedPlan(t *testing.T) {
	for _, withFinding := range []bool{false, true} {
		t.Run(map[bool]string{false: "zero findings", true: "one finding"}[withFinding], func(t *testing.T) {
			runner := &developmentAIStub{response: developmentPlanningResponse(withFinding)}
			service, aggregate := seededDevelopmentAIService(t, PhasePlanning, ExecutionQueued, runner)
			planned, err := service.RunFeaturePlanning(t.Context(), RunFeaturePlanningRequest{
				WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
				RequestID: "request-feature-planning",
			})
			if err != nil || len(planned.StageRuns) != 1 || planned.StageRuns[0].Stage != "planning" {
				t.Fatalf("planned aggregate = %#v, %v", planned, err)
			}
			if withFinding {
				if len(planned.Findings) != 1 || planned.Workspace.ExecutionState != ExecutionQueued {
					t.Fatalf("finding plan = %#v", planned)
				}
			} else if len(planned.Findings) != 0 || planned.Workspace.ExecutionState != ExecutionBlocked {
				t.Fatalf("zero-finding plan = %#v", planned)
			}
			if len(runner.requests) != 1 || runner.requests[0].Operation != "feature.plan" ||
				!strings.Contains(runner.requests[0].UserPrompt, "pkg/example.go") {
				t.Fatalf("planning request = %#v", runner.requests)
			}
		})
	}
}

func TestRunFeaturePlanningRejectsInvalidStateAndDependencies(t *testing.T) {
	if _, err := (*Service)(
		nil,
	).RunFeaturePlanning(t.Context(), RunFeaturePlanningRequest{}); !errors.Is(
		err,
		ErrInvalid,
	) {
		t.Fatalf("nil service planning error = %v", err)
	}
	service, aggregate := seededDevelopmentAIService(t, PhaseTriage, ExecutionQueued,
		&developmentAIStub{response: developmentPlanningResponse(false)})
	if _, err := service.RunFeaturePlanning(t.Context(), RunFeaturePlanningRequest{
		WorkspaceID: aggregate.Workspace.ID, ExpectedVersion: aggregate.Workspace.Version,
		RequestID: "request-feature-wrong-phase",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong phase planning error = %v", err)
	}

	for _, test := range []struct {
		name       string
		runner     *developmentAIStub
		evidence   PlanningEvidenceLoader
		wantErr    error
		wantPhrase string
	}{
		{name: "missing evidence", runner: &developmentAIStub{response: developmentPlanningResponse(false)}, wantPhrase: "evidence is unavailable"},
		{name: "evidence error", runner: &developmentAIStub{response: developmentPlanningResponse(false)}, evidence: planningEvidenceStub{err: context.Canceled}, wantErr: context.Canceled},
		{name: "runner error", runner: &developmentAIStub{err: context.DeadlineExceeded}, evidence: planningEvidenceStub{evidence: json.RawMessage(`{}`)}, wantErr: context.DeadlineExceeded},
		{name: "invalid plan", runner: &developmentAIStub{response: map[string]any{"summary": "missing fields", "extra": true}}, evidence: planningEvidenceStub{evidence: json.RawMessage(`{}`)}, wantPhrase: "plan is invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate, seeded := seededDevelopmentAIService(t, PhasePlanning, ExecutionQueued, test.runner)
			candidate.planningEvidence = test.evidence
			_, runErr := candidate.RunFeaturePlanning(t.Context(), RunFeaturePlanningRequest{
				WorkspaceID: seeded.Workspace.ID, ExpectedVersion: seeded.Workspace.Version,
				RequestID: "request-feature-" + strings.ReplaceAll(test.name, " ", "-"),
			})
			if test.wantErr != nil && !errors.Is(runErr, test.wantErr) {
				t.Fatalf("planning error = %v, want %v", runErr, test.wantErr)
			}
			if test.wantPhrase != "" && (runErr == nil || !strings.Contains(runErr.Error(), test.wantPhrase)) {
				t.Fatalf("planning error = %v, want phrase %q", runErr, test.wantPhrase)
			}
		})
	}
}
