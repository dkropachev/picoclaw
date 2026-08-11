package prdevelopment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const publicationAttentionTestID = "pdpub_c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1"

type publicationAttentionDecisionBinding struct {
	mu    sync.Mutex
	key   string
	runID string
}

func (binding *publicationAttentionDecisionBinding) Find(
	_ context.Context,
	key string,
) (string, bool, error) {
	binding.mu.Lock()
	defer binding.mu.Unlock()
	if binding.key == "" {
		return "", false, nil
	}
	if binding.key != key {
		return "", false, workflows.ErrRunAdmissionConflict
	}
	return binding.runID, true, nil
}

func (binding *publicationAttentionDecisionBinding) Admit(
	ctx context.Context,
	key string,
	create func(context.Context) error,
) (string, bool, error) {
	binding.mu.Lock()
	defer binding.mu.Unlock()
	if binding.key != "" {
		if binding.key != key {
			return "", false, workflows.ErrRunAdmissionConflict
		}
		return binding.runID, true, nil
	}
	runID, err := sharedattention.RunIDForDecisionKey(key)
	if err != nil {
		return "", false, err
	}
	if err = create(ctx); err != nil {
		return "", false, err
	}
	binding.key = key
	binding.runID = runID
	return runID, false, nil
}

type publicationAttentionBridgeFixture struct {
	bridge   *AttentionBridge
	store    *developmentAttentionBridgeTestStore
	caseID   string
	key      eventing.PRDevelopmentPublicationDecisionKey
	runID    string
	runs     workflows.RunStore
	executor *workflows.Executor
}

func newPublicationAttentionBridgeFixture(
	t *testing.T,
) *publicationAttentionBridgeFixture {
	t.Helper()
	runtimeFixture := newAttentionRuntimeFixture(t)
	gates := []workflows.GateSpec{{
		ID:        "publication",
		Kind:      workflows.GateDeterministic,
		When:      "true",
		Title:     "Choose publication behavior",
		Questions: []any{"How should this reviewed candidate proceed?"},
	}}
	snapshot := sharedattention.PolicySnapshot{
		Revision: "publication-attention-policy-v1",
		Global:   gates,
	}
	prepared, err := sharedattention.PrepareSnapshot(snapshot)
	if err != nil || prepared.IsNoop() {
		t.Fatalf("PrepareSnapshot() = (%#v, %v)", prepared, err)
	}
	key := eventing.PRDevelopmentPublicationDecisionKey{
		PublicationID:           publicationAttentionTestID,
		ReviewLedgerEntryID:     runtimeFixture.snapshot.ReviewEntry.ID,
		ReviewLedgerEntryHash:   runtimeFixture.snapshot.ReviewEntry.EntryHash,
		PolicyRevision:          prepared.DecisionRevision(),
		SubjectRevision:         "sha256:" + strings.Repeat("c", 64),
		ProviderObservationHash: strings.Repeat("d", 64),
	}
	decisionKey, err := canonicalPRDevelopmentPublicationDecisionKey(key)
	if err != nil {
		t.Fatal(err)
	}
	executor := &workflows.Executor{
		WorkspaceDir: runtimeFixture.workspacePath,
		Store:        runtimeFixture.runs,
	}
	runner, err := sharedattention.NewPrivateRunner(sharedattention.PrivateRunnerConfig{
		Executor: executor,
		Runs:     runtimeFixture.runs,
		Policies: sharedattention.PolicySourceFunc(func(
			ctx context.Context,
			_ sharedattention.PolicySelector,
			use sharedattention.PolicyUse,
		) error {
			return use(ctx, snapshot)
		}),
		Decisions: &publicationAttentionDecisionBinding{},
	})
	if err != nil {
		t.Fatal(err)
	}
	launched, err := runner.Launch(context.Background(), sharedattention.PrivateLaunchRequest{
		DecisionKey: decisionKey,
		Policy:      prepared,
	})
	if err != nil || launched.Status != workflows.RunStatusWaiting || launched.RunID == "" {
		t.Fatalf("Launch() = (%#v, %v), want waiting publication gate", launched, err)
	}
	wantRunID, err := prDevelopmentPublicationRunID(key)
	if err != nil || launched.RunID != wantRunID {
		t.Fatalf("publication run = (%q, %v), want %q", launched.RunID, err, wantRunID)
	}
	link := eventing.PRDevelopmentPublicationDecisionRunLink{Key: key, RunID: launched.RunID}
	store := &developmentAttentionBridgeTestStore{
		attentionRuntimeStore: runtimeFixture.store,
		projection: eventing.PRDevelopmentAttentionTriggerCaseSnapshot{
			CaseID:                 runtimeFixture.snapshot.Case.ID,
			ConversationVersion:    runtimeFixture.snapshot.Conversation.Version,
			CurrentReviewEntryID:   key.ReviewLedgerEntryID,
			CurrentReviewEntryHash: key.ReviewLedgerEntryHash,
			CurrentReviewOutcome:   eventing.PRDevelopmentLedgerReviewPassed,
			Publication: &eventing.PRDevelopmentPublicationAttentionProjection{
				CaseID:       runtimeFixture.snapshot.Case.ID,
				DecisionRun:  link,
				PinnedPolicy: prepared.Canonical(),
				Status:       eventing.PRDevelopmentPublicationGateWaiting,
			},
			PublicationCurrent:           true,
			PublicationAttentionRequired: true,
		},
	}
	service, err := NewService(ServiceConfig{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewAttentionBridge(AttentionBridgeConfig{
		Service:  service,
		Executor: executor,
		RunStore: runtimeFixture.runs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &publicationAttentionBridgeFixture{
		bridge:   bridge,
		store:    store,
		caseID:   runtimeFixture.snapshot.Case.ID,
		key:      key,
		runID:    launched.RunID,
		runs:     runtimeFixture.runs,
		executor: executor,
	}
}

func TestPRDevelopmentPublicationAttentionBridgeRespondsWithSeparateAuthority(
	t *testing.T,
) {
	fixture := newPublicationAttentionBridgeFixture(t)
	first, err := fixture.bridge.Project(context.Background(), fixture.caseID)
	if err != nil || first.Status != AttentionStatusWaiting || !first.CanRespond ||
		len(first.Turns) != 1 ||
		!sharedattention.ValidConversationResponseToken(first.Turns[0].ResponseToken) {
		t.Fatalf("Project() = (%#v, %v), want actionable publication turn", first, err)
	}
	token := first.Turns[0].ResponseToken
	fixture.store.advanceConversationVersion()
	second, err := fixture.bridge.Project(context.Background(), fixture.caseID)
	if err != nil || second.CaseVersion != first.CaseVersion+1 ||
		second.Turns[0].ResponseToken != token {
		t.Fatalf("Project() after chat = (%#v, %v), want stable token/new case fence", second, err)
	}
	response := "Continue after preserving the compatibility contract."
	_, err = fixture.bridge.Respond(context.Background(), AttentionResponseRequest{
		CaseID:              fixture.caseID,
		ExpectedCaseVersion: first.CaseVersion,
		ResponseToken:       token,
		Response:            response,
	})
	if !errors.Is(err, eventing.ErrPRDevelopmentConversationConflict) {
		t.Fatalf("stale Respond() error = %v, want conversation conflict", err)
	}
	request := AttentionResponseRequest{
		CaseID:              fixture.caseID,
		ExpectedCaseVersion: second.CaseVersion,
		ResponseToken:       token,
		Response:            response,
	}
	completed, err := fixture.bridge.Respond(context.Background(), request)
	if err != nil || completed.Status != AttentionStatusCompleted ||
		completed.CanRespond || len(completed.Turns) != 1 ||
		completed.Turns[0].Response != response ||
		completed.Turns[0].ResponseToken != "" {
		t.Fatalf("Respond() = (%#v, %v), want completed publication gate", completed, err)
	}
	tasks, err := fixture.executor.ListHumanTasks(context.Background(), fixture.runID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() = (%#v, %v)", tasks, err)
	}
	wantResponseID := sharedattention.ConversationResponseID(
		prDevelopmentPublicationAttentionResponseIDDomain,
		token,
		response,
	)
	reviewResponseID := sharedattention.ConversationResponseID(
		prDevelopmentAttentionResponseIDDomain,
		token,
		response,
	)
	if tasks[0].ResponseID != wantResponseID || tasks[0].ResponseID == reviewResponseID {
		t.Fatalf("response ID = %q, want publication domain %q", tasks[0].ResponseID, wantResponseID)
	}
	replayed, err := fixture.bridge.Respond(context.Background(), request)
	if err != nil || replayed.Status != AttentionStatusCompleted {
		t.Fatalf("Respond(replay) = (%#v, %v), want idempotent completion", replayed, err)
	}
	changed := request
	changed.Response = "Choose another behavior."
	if _, err = fixture.bridge.Respond(context.Background(), changed); !errors.Is(
		err,
		eventing.ErrPRDevelopmentConversationConflict,
	) {
		t.Fatalf("Respond(changed replay) error = %v, want conflict", err)
	}

	fixture.store.mutateProjection(func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
		snapshot.Publication.Status = eventing.PRDevelopmentPublicationPushReady
		snapshot.PublicationAttentionRequired = false
	})
	history, err := fixture.bridge.Project(context.Background(), fixture.caseID)
	if err != nil || history.Status != AttentionStatusCompleted || history.CanRespond ||
		len(history.Turns) != 1 || history.Turns[0].Response != response ||
		history.Turns[0].ResponseToken != "" {
		t.Fatalf("Project(push-ready) = (%#v, %v), want read-only history", history, err)
	}
	if _, err = fixture.bridge.Respond(context.Background(), request); !errors.Is(
		err,
		eventing.ErrPRDevelopmentConversationConflict,
	) {
		t.Fatalf("Respond(push-ready replay) error = %v, want authority conflict", err)
	}
	encoded, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{
		fixture.key.PublicationID,
		fixture.key.ReviewLedgerEntryID,
		fixture.key.PolicyRevision,
		fixture.key.SubjectRevision,
		fixture.runID,
		"decision_point",
	} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("public history exposed %q: %s", private, encoded)
		}
	}
}

func TestPRDevelopmentPublicationAttentionBridgeLifecycleIsFailClosed(t *testing.T) {
	t.Run("linked pending requeue is queued without task authority", func(t *testing.T) {
		fixture := newPublicationAttentionBridgeFixture(t)
		fixture.store.mutateProjection(func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
			snapshot.Publication.Status = eventing.PRDevelopmentPublicationPending
			snapshot.PublicationAttentionRequired = false
		})
		view, err := fixture.bridge.Project(context.Background(), fixture.caseID)
		if err != nil || view.Status != AttentionStatusQueued || view.CanRespond ||
			len(view.Turns) != 0 {
			t.Fatalf("Project() = (%#v, %v), want queued noninteractive retry", view, err)
		}
	})

	t.Run("claimed from gate wait remains actionable", func(t *testing.T) {
		fixture := newPublicationAttentionBridgeFixture(t)
		fixture.store.mutateProjection(func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
			snapshot.Publication.Status = eventing.PRDevelopmentPublicationClaimed
			snapshot.Publication.ClaimFrom = eventing.PRDevelopmentPublicationGateWaiting
		})
		view, err := fixture.bridge.Project(context.Background(), fixture.caseID)
		if err != nil || view.Status != AttentionStatusWaiting || !view.CanRespond ||
			len(view.Turns) != 1 || view.Turns[0].ResponseToken == "" {
			t.Fatalf("Project() = (%#v, %v), want claimed wait authority", view, err)
		}
	})

	t.Run("initial claim is checking without task authority", func(t *testing.T) {
		fixture := newPublicationAttentionBridgeFixture(t)
		fixture.store.mutateProjection(func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
			snapshot.Publication.Status = eventing.PRDevelopmentPublicationClaimed
			snapshot.Publication.ClaimFrom = eventing.PRDevelopmentPublicationPending
			snapshot.PublicationAttentionRequired = false
		})
		view, err := fixture.bridge.Project(context.Background(), fixture.caseID)
		if err != nil || view.Status != AttentionStatusChecking || view.CanRespond ||
			len(view.Turns) != 0 {
			t.Fatalf("Project() = (%#v, %v), want noninteractive checking", view, err)
		}
	})

	t.Run("waiting run cannot appear after gate completion", func(t *testing.T) {
		fixture := newPublicationAttentionBridgeFixture(t)
		fixture.store.mutateProjection(func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
			snapshot.Publication.Status = eventing.PRDevelopmentPublicationPushReady
			snapshot.PublicationAttentionRequired = false
		})
		if _, err := fixture.bridge.Project(context.Background(), fixture.caseID); !errors.Is(
			err,
			ErrUnavailable,
		) {
			t.Fatalf("Project(waiting push-ready) error = %v, want unavailable", err)
		}
	})

	t.Run("waiting run cannot appear behind a terminal publication", func(t *testing.T) {
		fixture := newPublicationAttentionBridgeFixture(t)
		fixture.store.mutateProjection(func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
			snapshot.Publication.Status = eventing.PRDevelopmentPublicationFailed
			snapshot.PublicationAttentionRequired = false
		})
		for name, bridge := range map[string]*AttentionBridge{
			"enabled runtime": fixture.bridge,
			"disabled runtime": func() *AttentionBridge {
				readOnly, err := NewAttentionBridge(AttentionBridgeConfig{
					Service:  fixture.bridge.service,
					RunStore: fixture.runs,
				})
				if err != nil {
					t.Fatal(err)
				}
				return readOnly
			}(),
		} {
			t.Run(name, func(t *testing.T) {
				if _, err := bridge.Project(context.Background(), fixture.caseID); !errors.Is(
					err,
					ErrUnavailable,
				) {
					t.Fatalf("Project(waiting terminal) error = %v, want unavailable", err)
				}
			})
		}
	})

	t.Run("superseded publication is hidden", func(t *testing.T) {
		fixture := newPublicationAttentionBridgeFixture(t)
		fixture.store.mutateProjection(func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
			snapshot.PublicationCurrent = false
			snapshot.PublicationAttentionRequired = false
		})
		view, err := fixture.bridge.Project(context.Background(), fixture.caseID)
		if err != nil || view.Status != AttentionStatusNone || view.CanRespond ||
			len(view.Turns) != 0 {
			t.Fatalf("Project(superseded) = (%#v, %v), want hidden history", view, err)
		}
	})

	t.Run("workflow-disabled bridge is read only", func(t *testing.T) {
		fixture := newPublicationAttentionBridgeFixture(t)
		disabled, err := NewAttentionBridge(AttentionBridgeConfig{
			Service:  fixture.bridge.service,
			RunStore: fixture.runs,
		})
		if err != nil {
			t.Fatal(err)
		}
		view, err := disabled.Project(context.Background(), fixture.caseID)
		if err != nil || view.Status != AttentionStatusWaiting || view.CanRespond ||
			len(view.Turns) != 1 || view.Turns[0].ResponseToken != "" {
			t.Fatalf("Project(disabled) = (%#v, %v), want read-only wait", view, err)
		}
		enabled, err := fixture.bridge.Project(context.Background(), fixture.caseID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = disabled.Respond(context.Background(), AttentionResponseRequest{
			CaseID:              fixture.caseID,
			ExpectedCaseVersion: enabled.CaseVersion,
			ResponseToken:       enabled.Turns[0].ResponseToken,
			Response:            "Continue.",
		})
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Respond(disabled) error = %v, want unavailable", err)
		}
	})
}

func TestPRDevelopmentPublicationAttentionBridgeRetainsCompletedCurrentHistory(
	t *testing.T,
) {
	fixture := newPublicationAttentionBridgeFixture(t)
	waiting, err := fixture.bridge.Project(context.Background(), fixture.caseID)
	if err != nil {
		t.Fatal(err)
	}
	response := "The reviewed candidate can proceed."
	_, err = fixture.bridge.Respond(context.Background(), AttentionResponseRequest{
		CaseID:              fixture.caseID,
		ExpectedCaseVersion: waiting.CaseVersion,
		ResponseToken:       waiting.Turns[0].ResponseToken,
		Response:            response,
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		status    eventing.PRDevelopmentPublicationStatus
		claimFrom eventing.PRDevelopmentPublicationStatus
	}{
		{name: "push ready", status: eventing.PRDevelopmentPublicationPushReady},
		{
			name: "claimed from push ready", status: eventing.PRDevelopmentPublicationClaimed,
			claimFrom: eventing.PRDevelopmentPublicationPushReady,
		},
		{
			name: "push started", status: eventing.PRDevelopmentPublicationPushStarted,
			claimFrom: eventing.PRDevelopmentPublicationPushReady,
		},
		{name: "published", status: eventing.PRDevelopmentPublicationPublished},
		{name: "outcome unknown", status: eventing.PRDevelopmentPublicationOutcomeUnknown},
		{name: "conflict", status: eventing.PRDevelopmentPublicationConflict},
		{name: "failed", status: eventing.PRDevelopmentPublicationFailed},
		{
			name:   "recovery required",
			status: eventing.PRDevelopmentPublicationRecoveryRequired,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture.store.mutateProjection(func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
				snapshot.Publication.Status = test.status
				snapshot.Publication.ClaimFrom = test.claimFrom
				snapshot.PublicationAttentionRequired = false
			})
			view, projectErr := fixture.bridge.Project(context.Background(), fixture.caseID)
			if projectErr != nil || view.Status != AttentionStatusCompleted ||
				view.CanRespond || len(view.Turns) != 1 ||
				view.Turns[0].Response != response || view.Turns[0].ResponseToken != "" {
				t.Fatalf("Project() = (%#v, %v), want completed read-only history", view, projectErr)
			}
		})
	}
	fixture.store.mutateProjection(func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
		snapshot.PublicationCurrent = false
		snapshot.PublicationAttentionRequired = false
	})
	hidden, err := fixture.bridge.Project(context.Background(), fixture.caseID)
	if err != nil || hidden.Status != AttentionStatusNone || len(hidden.Turns) != 0 {
		t.Fatalf("Project(superseded completed history) = (%#v, %v), want none", hidden, err)
	}
}

func TestPRDevelopmentPublicationAttentionTokenIsSourceAndOccurrenceBound(t *testing.T) {
	fixture := newPublicationAttentionBridgeFixture(t)
	view, err := fixture.bridge.Project(context.Background(), fixture.caseID)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := fixture.executor.ListHumanTasks(context.Background(), fixture.runID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListHumanTasks() = (%#v, %v)", tasks, err)
	}
	task := tasks[0]
	publicationToken := view.Turns[0].ResponseToken
	reviewToken, err := developmentAttentionResponseTokenFactory(
		eventing.PRDevelopmentAttentionTrigger{
			CaseID:              fixture.caseID,
			ReviewEntryID:       fixture.key.ReviewLedgerEntryID,
			ReviewEntryHash:     fixture.key.ReviewLedgerEntryHash,
			ConversationVersion: view.CaseVersion,
			TranscriptDigest:    strings.Repeat("a", 64),
			DecisionPoint:       eventing.PRDevelopmentAttentionDecisionReviewRequired,
			PolicyRevision:      fixture.key.PolicyRevision,
			SubjectRevision:     fixture.key.SubjectRevision,
			RunID:               fixture.runID,
		},
	)(task, task.Revision)
	if err != nil || reviewToken == publicationToken {
		t.Fatalf("review token = (%q, %v), publication token %q", reviewToken, err, publicationToken)
	}
	otherCaseToken, err := developmentPublicationAttentionResponseTokenFactory(
		"pdc_ffffffffffffffffffffffffffffffff",
		fixture.key,
		fixture.runID,
	)(task, task.Revision)
	if err != nil || otherCaseToken == publicationToken {
		t.Fatalf("other-case token = (%q, %v), publication token %q", otherCaseToken, err, publicationToken)
	}
	otherKey := fixture.key
	otherKey.ProviderObservationHash = strings.Repeat("e", 64)
	if _, err = developmentPublicationAttentionResponseTokenFactory(
		fixture.caseID,
		otherKey,
		fixture.runID,
	)(task, task.Revision); !errors.Is(err, sharedattention.ErrConversationUnavailable) {
		t.Fatalf("cross-publication token error = %v, want unavailable", err)
	}
	for name, token := range map[string]string{
		"review source": reviewToken,
		"other case":    otherCaseToken,
	} {
		t.Run(name, func(t *testing.T) {
			_, respondErr := fixture.bridge.Respond(context.Background(), AttentionResponseRequest{
				CaseID:              fixture.caseID,
				ExpectedCaseVersion: view.CaseVersion,
				ResponseToken:       token,
				Response:            "Continue.",
			})
			if !errors.Is(respondErr, eventing.ErrPRDevelopmentConversationConflict) {
				t.Fatalf("Respond(cross token) error = %v, want conflict", respondErr)
			}
		})
	}
}

func TestPRDevelopmentPublicationAttentionBridgeRejectsIdentityCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*publicationAttentionBridgeFixture)
	}{
		{name: "malformed policy", mutate: func(fixture *publicationAttentionBridgeFixture) {
			fixture.store.mutateProjection(func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
				snapshot.Publication.PinnedPolicy = json.RawMessage(`{"invalid":true}`)
			})
		}},
		{name: "decision policy mismatch", mutate: func(fixture *publicationAttentionBridgeFixture) {
			other, err := sharedattention.PrepareSnapshot(sharedattention.PolicySnapshot{
				Revision: "other-publication-policy-v1",
				Global: []workflows.GateSpec{{
					ID: "other", Kind: workflows.GateDeterministic, When: "true",
					Title: "Other", Questions: []any{"Other?"},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			fixture.store.mutateProjection(func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
				snapshot.Publication.PinnedPolicy = other.Canonical()
			})
		}},
		{name: "non-deterministic run link", mutate: func(fixture *publicationAttentionBridgeFixture) {
			fixture.store.mutateProjection(func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
				snapshot.Publication.DecisionRun.RunID = "wr_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			})
		}},
		{name: "lookup link differs", mutate: func(fixture *publicationAttentionBridgeFixture) {
			changed := fixture.store.projection.Publication.DecisionRun
			changed.RunID = "wr_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			fixture.store.publicationLinkOverride = &changed
		}},
		{name: "lookup link missing", mutate: func(fixture *publicationAttentionBridgeFixture) {
			fixture.store.publicationLinkErr = eventing.ErrNotFound
		}},
		{name: "current review binding differs", mutate: func(fixture *publicationAttentionBridgeFixture) {
			fixture.store.mutateProjection(func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
				snapshot.CurrentReviewEntryHash = strings.Repeat("e", 64)
			})
		}},
		{
			name: "historical projection review binding differs",
			mutate: func(fixture *publicationAttentionBridgeFixture) {
				fixture.store.mutateProjection(func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
					snapshot.PublicationCurrent = false
					snapshot.PublicationAttentionRequired = false
					snapshot.CurrentReviewEntryHash = strings.Repeat("e", 64)
				})
			},
		},
		{name: "attention-required flag differs", mutate: func(fixture *publicationAttentionBridgeFixture) {
			fixture.store.mutateProjection(func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
				snapshot.PublicationAttentionRequired = false
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPublicationAttentionBridgeFixture(t)
			test.mutate(fixture)
			if _, err := fixture.bridge.Project(context.Background(), fixture.caseID); !errors.Is(
				err,
				ErrUnavailable,
			) {
				t.Fatalf("Project() error = %v, want unavailable", err)
			}
		})
	}
}

func TestPRDevelopmentAttentionBridgeRejectsTwoCurrentSources(t *testing.T) {
	review := newDevelopmentAttentionBridgeFixture(t)
	publication := newPublicationAttentionBridgeFixture(t)
	review.store.mutateProjection(func(snapshot *eventing.PRDevelopmentAttentionTriggerCaseSnapshot) {
		projected := *publication.store.projection.Publication
		projected.CaseID = snapshot.CaseID
		snapshot.Publication = &projected
		snapshot.PublicationCurrent = true
		snapshot.PublicationAttentionRequired = true
	})
	if _, err := review.bridge.Project(context.Background(), review.caseID); !errors.Is(
		err,
		ErrUnavailable,
	) {
		t.Fatalf("Project(two current sources) error = %v, want unavailable", err)
	}
}
