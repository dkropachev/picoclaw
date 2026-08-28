package repoaudit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestRepositoryReviewCampaignBeginOperationalFailures(t *testing.T) {
	validRequest := func(repository string) BeginCampaignRequest {
		return BeginCampaignRequest{
			Repository: repository, CampaignID: NewRepositoryReviewCampaignID(),
			CommitSHA: repositoryReviewCampaignTestCommit, Exact: true,
		}
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newRepositoryAuditTestStore(t).BeginCampaign(
		canceled, validRequest("owner/canceled"),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled BeginCampaign error = %v", err)
	}

	if _, err := newRepositoryAuditTestStore(t).BeginCampaign(
		context.Background(), BeginCampaignRequest{},
	); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("invalid BeginCampaign error = %v", err)
	}

	t.Run("lock", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		if err := os.Mkdir(store.root+".lock", 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := store.BeginCampaign(
			context.Background(), validRequest("owner/lock-error"),
		); err == nil {
			t.Fatal("BeginCampaign ignored an invalid lock file")
		}
	})

	t.Run("cancel after lock", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		request := validRequest("owner/cancel-after-lock")
		key := store.root + "\x00" + request.Repository
		value, _ := storeLocks.LoadOrStore(key, &sync.Mutex{})
		mutex := value.(*sync.Mutex)
		mutex.Lock()
		ctx := &repositoryReviewCancelAfterFirstContext{
			Context: context.Background(), first: make(chan struct{}),
		}
		done := make(chan error, 1)
		go func() {
			_, err := store.BeginCampaign(ctx, request)
			done <- err
		}()
		<-ctx.first
		ctx.canceled.Store(true)
		mutex.Unlock()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("BeginCampaign after-lock cancellation error = %v", err)
		}
	})

	t.Run("load", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		request := validRequest("owner/load-error")
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(store.path(request.Repository), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := store.BeginCampaign(context.Background(), request); err == nil {
			t.Fatal("BeginCampaign ignored an invalid state file")
		}
	})

	t.Run("save", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, err := store.BeginCampaign(
			context.Background(), validRequest("owner/save-error"),
		); err == nil {
			t.Fatal("BeginCampaign ignored a durable-write failure")
		}
	})
}

func repositoryReviewCampaignReconcileFixture(
	t *testing.T,
	repository string,
) (Store, ReconcileCampaignRequest, FileRef) {
	t.Helper()
	store := newRepositoryAuditTestStore(t)
	campaignID, begun := beginRepositoryReviewCampaignForTest(t, store, repository, false)
	file := repositoryAuditTestFile("pkg/reconcile.go", "a", 1)
	return store, ReconcileCampaignRequest{
		Repository: repository, ExpectedReviewVersion: begun.ReviewVersion,
		Coverage: RepositoryReviewCampaignCoverage{
			ID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit,
			InventoryHash:       repositoryReviewCampaignTestInventory,
			ProfileHash:         repositoryReviewCampaignTestProfile,
			ScopeDigest:         repositoryReviewCampaignTestScopeDigest(t, file),
			RequiredAssignments: 1, SelectedFiles: 1,
			Paths: map[string]RepositoryReviewCampaignPathCoverage{},
		},
		SelectedScope: []FileRef{file},
	}, file
}

func TestRepositoryReviewCampaignReconcileOperationalFailures(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newRepositoryAuditTestStore(t).ReconcileCampaign(
		canceled, ReconcileCampaignRequest{},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled ReconcileCampaign error = %v", err)
	}

	t.Run("lock", func(t *testing.T) {
		store, request, _ := repositoryReviewCampaignReconcileFixture(t, "owner/reconcile-lock")
		if err := os.Remove(store.root + ".lock"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(store.root+".lock", 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReconcileCampaign(context.Background(), request); err == nil {
			t.Fatal("ReconcileCampaign ignored an invalid lock file")
		}
	})

	t.Run("cancel after lock", func(t *testing.T) {
		store, request, _ := repositoryReviewCampaignReconcileFixture(t, "owner/reconcile-cancel")
		key := store.root + "\x00" + request.Repository
		value, _ := storeLocks.LoadOrStore(key, &sync.Mutex{})
		mutex := value.(*sync.Mutex)
		mutex.Lock()
		ctx := &repositoryReviewCancelAfterFirstContext{
			Context: context.Background(), first: make(chan struct{}),
		}
		done := make(chan error, 1)
		go func() {
			_, err := store.ReconcileCampaign(ctx, request)
			done <- err
		}()
		<-ctx.first
		ctx.canceled.Store(true)
		mutex.Unlock()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("ReconcileCampaign after-lock cancellation error = %v", err)
		}
	})

	t.Run("load", func(t *testing.T) {
		store, request, _ := repositoryReviewCampaignReconcileFixture(t, "owner/reconcile-load")
		if err := os.Remove(store.path(request.Repository)); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(store.path(request.Repository), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReconcileCampaign(context.Background(), request); err == nil {
			t.Fatal("ReconcileCampaign ignored an invalid state file")
		}
	})

	t.Run("save", func(t *testing.T) {
		store, request, file := repositoryReviewCampaignReconcileFixture(t, "owner/reconcile-save")
		request.Coverage.Paths[file.Path] = RepositoryReviewCampaignPathCoverage{Inspected: true}
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, err := store.ReconcileCampaign(context.Background(), request); err == nil {
			t.Fatal("ReconcileCampaign ignored a durable-write failure")
		}
	})
}

func TestRepositoryReviewCampaignReconcileEnvelopeValidation(t *testing.T) {
	newFixture := func(t *testing.T) (Store, ReconcileCampaignRequest, FileRef) {
		t.Helper()
		return repositoryReviewCampaignReconcileFixture(
			t, "owner/reconcile-envelope-"+strings.ReplaceAll(t.Name(), "/", "-"),
		)
	}

	t.Run("noncanonical scope", func(t *testing.T) {
		store, request, file := newFixture(t)
		file.Path = " pkg/reconcile.go"
		request.SelectedScope = []FileRef{file}
		if _, err := store.ReconcileCampaign(context.Background(), request); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("noncanonical selected scope error = %v", err)
		}
	})

	t.Run("scope digest mismatch", func(t *testing.T) {
		store, request, _ := newFixture(t)
		request.SelectedScope = []FileRef{repositoryAuditTestFile("other.go", "b", 1)}
		if _, err := store.ReconcileCampaign(context.Background(), request); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("scope digest mismatch error = %v", err)
		}
	})

	t.Run("coverage path outside selected scope", func(t *testing.T) {
		store, request, file := newFixture(t)
		second := repositoryAuditTestFile("second.go", "b", 1)
		request.SelectedScope = []FileRef{file, second}
		request.Coverage.SelectedFiles = 2
		request.Coverage.ScopeDigest = repositoryReviewCampaignTestScopeDigest(t, file, second)
		request.Coverage.Paths = map[string]RepositoryReviewCampaignPathCoverage{
			"outside.go": {Inspected: true},
		}
		if _, err := store.ReconcileCampaign(context.Background(), request); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("outside recovered path error = %v", err)
		}
	})

	t.Run("invalid run recovery", func(t *testing.T) {
		store, request, _ := newFixture(t)
		request.Runs = []RepositoryReviewCampaignRunRecovery{{ID: "bad", Plan: Plan{}}}
		if _, err := store.ReconcileCampaign(context.Background(), request); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("invalid recovered run error = %v", err)
		}
	})

	for name, mutate := range map[string]func(*ReconcileCampaignRequest){
		"duplicate context IDs": func(request *ReconcileCampaignRequest) {
			request.ContextIDs = []string{"same", "same"}
		},
		"duplicate finding IDs": func(request *ReconcileCampaignRequest) {
			request.FindingIDs = []string{"same", "same"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, request, _ := newFixture(t)
			mutate(&request)
			if _, err := store.ReconcileCampaign(context.Background(), request); !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("duplicate record IDs error = %v", err)
			}
		})
	}
}

func TestRepositoryReviewCampaignReconcileRejectsCombinedRecoveryEnvelopeOverflow(t *testing.T) {
	store, request, _ := repositoryReviewCampaignReconcileFixture(t, "owner/reconcile-envelope-overflow")
	const records = 70_000
	request.ContextIDs = make([]string, records)
	request.FindingIDs = make([]string, records)
	for index := range records {
		prefix := fmt.Sprintf("%06d-", index)
		request.ContextIDs[index] = "context-" + prefix + strings.Repeat("c", 225)
		request.FindingIDs[index] = "finding-" + prefix + strings.Repeat("f", 225)
	}
	if _, err := store.ReconcileCampaign(context.Background(), request); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("combined recovery envelope overflow error = %v", err)
	}
	state, _, err := store.Get(request.Repository)
	if err != nil || state.CurrentCampaign == nil ||
		state.CurrentCampaign.RecoveryDigest != "" || repositoryReviewCampaignScopeBound(state.CurrentCampaign) {
		t.Fatalf("oversized recovery mutated campaign = %#v, %v", state.CurrentCampaign, err)
	}
}

func repositoryReviewCampaignRecoveryPlanForTest(
	t *testing.T,
	request ReconcileCampaignRequest,
	file FileRef,
) Plan {
	t.Helper()
	plan := Plan{
		Repository: request.Repository, CommitSHA: request.Coverage.CommitSHA,
		InventoryHash: request.Coverage.InventoryHash, ProfileHash: request.Coverage.ProfileHash,
		Authoritative: true, PendingFiles: []FileRef{file}, UnchangedFiles: []FileRef{},
		CreatedAt: repositoryAuditTestNow,
	}
	plan.ID = planDigest(plan)
	return plan
}

func TestRepositoryReviewCampaignReconcileRetainedRecordConflicts(t *testing.T) {
	newFixture := func(t *testing.T) (Store, ReconcileCampaignRequest, FileRef, Plan) {
		t.Helper()
		store, request, file := repositoryReviewCampaignReconcileFixture(
			t, "owner/reconcile-retained-"+strings.ReplaceAll(t.Name(), "/", "-"),
		)
		request.Coverage.Paths[file.Path] = RepositoryReviewCampaignPathCoverage{Inspected: true}
		plan := repositoryReviewCampaignRecoveryPlanForTest(t, request, file)
		return store, request, file, plan
	}
	load := func(t *testing.T, store Store, repository string) RepositoryState {
		t.Helper()
		state, _, err := store.Get(repository)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}

	t.Run("scope binding conflict", func(t *testing.T) {
		store, request, file, _ := newFixture(t)
		if _, err := store.PlanWithProfileLimitAuthoritativeForCampaign(
			context.Background(), request.Repository, request.Coverage.CommitSHA,
			request.Coverage.InventoryHash, "different-profile", request.Coverage.ID,
			1, []FileRef{file}, false, 1, true,
		); err != nil {
			t.Fatal(err)
		}
		state := load(t, store, request.Repository)
		request.ExpectedReviewVersion = state.ReviewVersion
		if _, err := store.ReconcileCampaign(context.Background(), request); !errors.Is(err, ErrConflict) {
			t.Fatalf("reconcile scope-binding conflict = %v", err)
		}
	})

	t.Run("monotonic path conflict", func(t *testing.T) {
		store, request, file, _ := newFixture(t)
		request.Coverage.Paths[file.Path] = RepositoryReviewCampaignPathCoverage{Unsupported: true}
		terminal, err := store.ReconcileCampaign(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		request.ExpectedReviewVersion = terminal.ReviewVersion
		request.Coverage.Paths[file.Path] = RepositoryReviewCampaignPathCoverage{Inspected: true}
		if _, err := store.ReconcileCampaign(context.Background(), request); !errors.Is(err, ErrConflict) {
			t.Fatalf("terminal path rewrite error = %v", err)
		}
	})

	t.Run("missing run", func(t *testing.T) {
		store, request, _, plan := newFixture(t)
		request.Runs = []RepositoryReviewCampaignRunRecovery{{ID: "missing-run", Plan: plan}}
		if _, err := store.ReconcileCampaign(context.Background(), request); !errors.Is(err, ErrConflict) {
			t.Fatalf("missing recovered run error = %v", err)
		}
	})

	for name, mutate := range map[string]func(*RepositoryState, *ReconcileCampaignRequest, Plan){
		"run provenance mismatch": func(state *RepositoryState, request *ReconcileCampaignRequest, plan Plan) {
			state.Runs = []ReviewRun{{
				ID: "legacy-run", PlanID: "other-plan", CommitSHA: request.Coverage.CommitSHA,
				InventoryHash: request.Coverage.InventoryHash, UnreviewedFiles: 1,
			}}
			request.Runs = []RepositoryReviewCampaignRunRecovery{{ID: "legacy-run", Plan: plan}}
		},
		"run campaign mismatch": func(state *RepositoryState, request *ReconcileCampaignRequest, plan Plan) {
			otherCampaign := NewRepositoryReviewCampaignID()
			state.CampaignHistory[otherCampaign] = request.Coverage.CommitSHA
			state.Runs = []ReviewRun{{
				ID: "legacy-run", CampaignID: otherCampaign, PlanID: plan.ID,
				CommitSHA: request.Coverage.CommitSHA, InventoryHash: request.Coverage.InventoryHash,
				ProfileHash: request.Coverage.ProfileHash,
				ScopeDigest: request.Coverage.ScopeDigest, UnreviewedFiles: 1,
			}}
			request.Runs = []RepositoryReviewCampaignRunRecovery{{ID: "legacy-run", Plan: plan}}
		},
		"tagged run metric rewrite": func(state *RepositoryState, request *ReconcileCampaignRequest, plan Plan) {
			state.Runs = []ReviewRun{{
				ID: "legacy-run", CampaignID: request.Coverage.ID, PlanID: plan.ID,
				CommitSHA: request.Coverage.CommitSHA, InventoryHash: request.Coverage.InventoryHash,
				ProfileHash: request.Coverage.ProfileHash,
				ScopeDigest: request.Coverage.ScopeDigest, InspectedFiles: 0, UnreviewedFiles: 1,
			}}
			request.Runs = []RepositoryReviewCampaignRunRecovery{{
				ID: "legacy-run", Plan: plan, InspectedFiles: 1,
			}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, request, _, plan := newFixture(t)
			state := load(t, store, request.Repository)
			mutate(&state, &request, plan)
			if err := store.save(&state); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReconcileCampaign(context.Background(), request); !errors.Is(err, ErrConflict) {
				t.Fatalf("retained run conflict error = %v", err)
			}
		})
	}

	for name, install := range map[string]func(*RepositoryState, ReconcileCampaignRequest, FileRef){
		"missing context": func(_ *RepositoryState, _ ReconcileCampaignRequest, _ FileRef) {},
		"context provenance mismatch": func(state *RepositoryState, request ReconcileCampaignRequest, file FileRef) {
			state.Contexts = []FindingContext{{
				ID: "context", Repository: "other/repo", CommitSHA: request.Coverage.CommitSHA,
				InventoryHash: request.Coverage.InventoryHash,
				ProfileHash:   request.Coverage.ProfileHash, Files: []FileRef{file},
			}}
		},
		"missing finding": func(_ *RepositoryState, _ ReconcileCampaignRequest, _ FileRef) {},
		"finding provenance mismatch": func(state *RepositoryState, request ReconcileCampaignRequest, file FileRef) {
			state.Findings = []Finding{{
				ID: "finding", Repository: "other/repo", CommitSHA: request.Coverage.CommitSHA,
				File: file, ContextIDs: []string{"context"},
			}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, request, file, _ := newFixture(t)
			state := load(t, store, request.Repository)
			install(&state, request, file)
			switch name {
			case "missing context", "context provenance mismatch":
				request.ContextIDs = []string{"context"}
			case "missing finding", "finding provenance mismatch":
				request.FindingIDs = []string{"finding"}
			}
			if err := store.save(&state); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ReconcileCampaign(context.Background(), request); !errors.Is(err, ErrConflict) {
				t.Fatalf("retained record conflict error = %v", err)
			}
		})
	}
}

func TestRepositoryReviewCampaignReconcileRejectsLatentLedgerCorruption(t *testing.T) {
	t.Run("coverage union exceeds selected scope", func(t *testing.T) {
		store, request, file := repositoryReviewCampaignReconcileFixture(t, "owner/reconcile-latent-scope")
		state, _, err := store.Get(request.Repository)
		if err != nil {
			t.Fatal(err)
		}
		state.CurrentCampaign.InventoryHash = request.Coverage.InventoryHash
		state.CurrentCampaign.ProfileHash = request.Coverage.ProfileHash
		state.CurrentCampaign.ScopeDigest = request.Coverage.ScopeDigest
		state.CurrentCampaign.RequiredAssignments = 1
		state.CurrentCampaign.SelectedFiles = 1
		state.CurrentCampaign.Paths = map[string]RepositoryReviewCampaignPathCoverage{
			"latent.go": {Inspected: true},
		}
		if err := store.save(&state); err != nil {
			t.Fatal(err)
		}
		request.Coverage.Paths[file.Path] = RepositoryReviewCampaignPathCoverage{Inspected: true}
		if _, err := store.ReconcileCampaign(context.Background(), request); err == nil ||
			!strings.Contains(err.Error(), "exceeds selected scope") {
			t.Fatalf("latent path-union error = %v", err)
		}
	})

	t.Run("context tagged to another campaign", func(t *testing.T) {
		store, request, file := repositoryReviewCampaignReconcileFixture(t, "owner/reconcile-context-tag")
		request.Coverage.Paths[file.Path] = RepositoryReviewCampaignPathCoverage{Inspected: true}
		plan := repositoryReviewCampaignRecoveryPlanForTest(t, request, file)
		otherCampaign := NewRepositoryReviewCampaignID()
		state, _, err := store.Get(request.Repository)
		if err != nil {
			t.Fatal(err)
		}
		state.CampaignHistory[otherCampaign] = request.Coverage.CommitSHA
		state.Runs = []ReviewRun{{
			ID: "legacy-run", PlanID: plan.ID, CommitSHA: request.Coverage.CommitSHA,
			InventoryHash: request.Coverage.InventoryHash, UnreviewedFiles: 1,
		}}
		state.Contexts = []FindingContext{{
			ID: "legacy-context", CampaignID: otherCampaign, Repository: request.Repository,
			CommitSHA: request.Coverage.CommitSHA, InventoryHash: request.Coverage.InventoryHash,
			ProfileHash: request.Coverage.ProfileHash, RunID: "legacy-run", Files: []FileRef{file},
		}}
		if err := store.save(&state); err != nil {
			t.Fatal(err)
		}
		request.Runs = []RepositoryReviewCampaignRunRecovery{{ID: "legacy-run", Plan: plan}}
		request.ContextIDs = []string{"legacy-context"}
		if _, err := store.ReconcileCampaign(context.Background(), request); !errors.Is(err, ErrConflict) {
			t.Fatalf("cross-campaign context recovery error = %v", err)
		}
	})
}

func TestRepositoryReviewCampaignReconcileNoChangeReplaySurvivesCheckpoint(t *testing.T) {
	store, request, file := repositoryReviewCampaignReconcileFixture(t, "owner/reconcile-no-change")
	planRepositoryReviewCampaignForTest(
		t, store, request.Repository, request.Coverage.ID, []FileRef{file}, false,
	)
	bound, _, err := store.Get(request.Repository)
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedReviewVersion = bound.ReviewVersion
	first, err := store.ReconcileCampaign(context.Background(), request)
	if err != nil || first.CurrentCampaign == nil || first.CurrentCampaign.RecoveryDigest == "" ||
		first.ReviewVersion != bound.ReviewVersion+1 {
		t.Fatalf("no-change reconciliation = %#v, %v", first, err)
	}

	currentReplay := request
	currentReplay.ExpectedReviewVersion = first.ReviewVersion
	unchanged, err := store.ReconcileCampaign(context.Background(), currentReplay)
	if err != nil || unchanged.ReviewVersion != first.ReviewVersion || unchanged.Version != first.Version {
		t.Fatalf("current-version reconciliation replay = %#v, %v", unchanged, err)
	}

	plan := planRepositoryReviewCampaignForTest(
		t, store, request.Repository, request.Coverage.ID, []FileRef{file}, false,
	)
	checkpoint, err := store.Record(context.Background(), RecordRequest{
		Plan: plan, RunID: "post-recovery-checkpoint", CompletedAt: repositoryAuditTestNow,
		InspectedFiles: []FileRef{}, CompletedFiles: []FileRef{},
		ReviewEvidence: []RepositoryReviewEvidence{{
			AssignmentID: "failed-required", ScopeFiles: []FileRef{file}, Required: true,
		}},
	})
	if err != nil || checkpoint.State.ReviewVersion <= first.ReviewVersion {
		t.Fatalf("post-recovery checkpoint = %#v, %v", checkpoint.State, err)
	}

	staleReplay, err := store.ReconcileCampaign(context.Background(), request)
	if err != nil || staleReplay.ReviewVersion != checkpoint.State.ReviewVersion {
		t.Fatalf("stale exact reconciliation replay = %#v, %v", staleReplay, err)
	}
	unrelated := request
	unrelated.ContextIDs = []string{"different-recovery"}
	if _, err := store.ReconcileCampaign(context.Background(), unrelated); !errors.Is(err, ErrConflict) {
		t.Fatalf("unrelated stale reconciliation error = %v", err)
	}
}

func TestRepositoryReviewCampaignRecoveryHelpers(t *testing.T) {
	file := repositoryAuditTestFile("a.go", "a", 1)
	request := ReconcileCampaignRequest{
		Repository: "owner/helpers",
		Coverage: RepositoryReviewCampaignCoverage{
			ID: NewRepositoryReviewCampaignID(), CommitSHA: repositoryReviewCampaignTestCommit,
			InventoryHash:       repositoryReviewCampaignTestInventory,
			ProfileHash:         repositoryReviewCampaignTestProfile,
			ScopeDigest:         repositoryReviewCampaignTestScopeDigest(t, file),
			RequiredAssignments: 1, SelectedFiles: 1,
			Paths: map[string]RepositoryReviewCampaignPathCoverage{file.Path: {Inspected: true}},
		},
		SelectedScope: []FileRef{file},
	}
	plan := repositoryReviewCampaignRecoveryPlanForTest(t, request, file)
	validRuns := []RepositoryReviewCampaignRunRecovery{
		{ID: "run-b", Plan: plan}, {ID: "run-a", Plan: plan},
	}
	validRuns[1].Plan.CreatedAt = validRuns[1].Plan.CreatedAt.Add(1)
	validRuns[1].Plan.ID = planDigest(validRuns[1].Plan)
	normalized, err := normalizeRepositoryReviewCampaignRuns(validRuns)
	if err != nil || normalized[0].ID != "run-a" || normalized[1].ID != "run-b" {
		t.Fatalf("normalized recovered runs = %#v, %v", normalized, err)
	}

	for name, runs := range map[string][]RepositoryReviewCampaignRunRecovery{
		"count":        make([]RepositoryReviewCampaignRunRecovery, maxAutomationRunIDs+1),
		"invalid plan": {{ID: "run", Plan: Plan{PendingFiles: []FileRef{{Path: " bad"}}}}},
		"bad envelope": {{ID: " run", Plan: plan}},
		"duplicate":    {{ID: "run", Plan: plan}, {ID: "run", Plan: plan}},
	} {
		t.Run("run "+name, func(t *testing.T) {
			if _, normalizationErr := normalizeRepositoryReviewCampaignRuns(
				runs,
			); !errors.Is(
				normalizationErr,
				ErrInvalidPlan,
			) {
				t.Fatalf("run normalization error = %v", normalizationErr)
			}
		})
	}

	if repositoryReviewCampaignRunMatchesCoverage(
		ReviewRun{}, RepositoryReviewCampaignRunRecovery{Plan: Plan{
			PendingFiles: []FileRef{{Path: " bad"}},
		}}, request.Coverage,
	) {
		t.Fatal("invalid recovered plan matched campaign coverage")
	}
	matchingRun := ReviewRun{
		ID: "run", PlanID: plan.ID, CommitSHA: request.Coverage.CommitSHA,
		InventoryHash: request.Coverage.InventoryHash,
	}
	if !repositoryReviewCampaignRunMatchesCoverage(
		matchingRun, RepositoryReviewCampaignRunRecovery{ID: "run", Plan: plan}, request.Coverage,
	) {
		t.Fatal("valid legacy run did not match campaign coverage")
	}

	for name, state := range map[string]RepositoryState{
		"runs":     {Runs: []ReviewRun{{ID: "same"}, {ID: "same"}}},
		"contexts": {Contexts: []FindingContext{{ID: "same"}, {ID: "same"}}},
		"findings": {Findings: []Finding{{ID: "same"}, {ID: "same"}}},
	} {
		t.Run("duplicate "+name, func(t *testing.T) {
			if _, indexErr := newRepositoryReviewCampaignIndexes(state); !errors.Is(indexErr, ErrConflict) {
				t.Fatalf("duplicate index error = %v", indexErr)
			}
		})
	}
	indexes, err := newRepositoryReviewCampaignIndexes(RepositoryState{
		Runs: []ReviewRun{{}, {ID: "run"}}, Contexts: []FindingContext{{}, {ID: "context"}},
		Findings: []Finding{{}, {ID: "finding"}},
	})
	if err != nil || len(indexes.runs) != 1 || len(indexes.contexts) != 1 || len(indexes.findings) != 1 {
		t.Fatalf("campaign indexes = %#v, %v", indexes, err)
	}
}

func TestRepositoryReviewCampaignProvenanceHelpers(t *testing.T) {
	first := repositoryAuditTestFile("a.go", "a", 1)
	second := repositoryAuditTestFile("b.go", "b", 1)
	campaignID := NewRepositoryReviewCampaignID()
	coverage := RepositoryReviewCampaignCoverage{
		ID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit,
		InventoryHash:       repositoryReviewCampaignTestInventory,
		ProfileHash:         repositoryReviewCampaignTestProfile,
		ScopeDigest:         repositoryReviewCampaignTestScopeDigest(t, first, second),
		RequiredAssignments: 1, SelectedFiles: 2,
		Paths: map[string]RepositoryReviewCampaignPathCoverage{
			first.Path: {Inspected: true}, second.Path: {Inspected: true},
		},
	}
	state := RepositoryState{
		Repository: "owner/provenance",
		Runs: []ReviewRun{{
			ID: "run", CampaignID: campaignID, CommitSHA: coverage.CommitSHA,
			InventoryHash: coverage.InventoryHash,
		}},
	}
	contextRecord := FindingContext{
		ID: "context", CampaignID: campaignID, Repository: state.Repository,
		CommitSHA: coverage.CommitSHA, InventoryHash: coverage.InventoryHash,
		ProfileHash: coverage.ProfileHash, RunID: "run", Files: []FileRef{first},
	}
	state.Contexts = []FindingContext{contextRecord}
	selected := map[string]FileRef{first.Path: first, second.Path: second}
	indexes, err := newRepositoryReviewCampaignIndexes(state)
	if err != nil {
		t.Fatal(err)
	}
	if !repositoryReviewCampaignContextMatchesCoverage(
		state, contextRecord, coverage, selected, indexes, nil,
	) {
		t.Fatal("valid campaign context did not match coverage")
	}

	for name, mutate := range map[string]func(*RepositoryState, *FindingContext, *repositoryReviewCampaignIndexes){
		"empty scope": func(_ *RepositoryState, record *FindingContext, _ *repositoryReviewCampaignIndexes) {
			record.Files = nil
		},
		"file outside scope": func(_ *RepositoryState, record *FindingContext, _ *repositoryReviewCampaignIndexes) {
			record.Files = []FileRef{repositoryAuditTestFile("outside.go", "c", 1)}
		},
		"missing run": func(_ *RepositoryState, record *FindingContext, _ *repositoryReviewCampaignIndexes) {
			record.RunID = "missing"
		},
		"wrong run campaign": func(candidate *RepositoryState, _ *FindingContext, _ *repositoryReviewCampaignIndexes) {
			candidate.Runs[0].CampaignID = NewRepositoryReviewCampaignID()
		},
	} {
		t.Run("context "+name, func(t *testing.T) {
			candidateState := state
			candidateState.Runs = append([]ReviewRun(nil), state.Runs...)
			candidate := contextRecord
			candidateIndexes := indexes
			mutate(&candidateState, &candidate, &candidateIndexes)
			if repositoryReviewCampaignContextMatchesCoverage(
				candidateState, candidate, coverage, selected, candidateIndexes, nil,
			) {
				t.Fatal("invalid campaign context matched coverage")
			}
		})
	}

	legacyPlan := repositoryReviewCampaignRecoveryPlanForTest(t, ReconcileCampaignRequest{
		Repository: state.Repository, Coverage: coverage,
	}, first)
	legacyPlan.PendingFiles = []FileRef{first, second}
	legacyPlan.ID = planDigest(legacyPlan)
	legacyRun := ReviewRun{
		ID: "legacy", PlanID: legacyPlan.ID, CommitSHA: coverage.CommitSHA,
		InventoryHash: coverage.InventoryHash,
	}
	legacyState := RepositoryState{Repository: state.Repository, Runs: []ReviewRun{legacyRun}}
	legacyContext := contextRecord
	legacyContext.RunID = legacyRun.ID
	legacyIndexes, err := newRepositoryReviewCampaignIndexes(legacyState)
	if err != nil {
		t.Fatal(err)
	}
	if repositoryReviewCampaignContextMatchesCoverage(
		legacyState, legacyContext, coverage, selected, legacyIndexes, nil,
	) {
		t.Fatal("untagged context matched without recovered-run provenance")
	}
	recoveredRuns := map[string]RepositoryReviewCampaignRunRecovery{
		legacyRun.ID: {ID: legacyRun.ID, Plan: legacyPlan},
	}
	if !repositoryReviewCampaignContextMatchesCoverage(
		legacyState, legacyContext, coverage, selected, legacyIndexes, recoveredRuns,
	) {
		t.Fatal("valid recovered legacy context did not match coverage")
	}

	finding := Finding{
		ID: "finding", CampaignID: campaignID, Repository: state.Repository,
		CommitSHA: coverage.CommitSHA, File: first, ContextIDs: []string{contextRecord.ID},
	}
	if !repositoryReviewCampaignFindingMatchesCoverage(
		state, finding, coverage, selected, indexes, nil,
	) {
		t.Fatal("valid campaign finding did not match coverage")
	}
	wrongCampaignContextState := state
	wrongCampaignContextState.Contexts = append([]FindingContext(nil), state.Contexts...)
	wrongCampaignContextState.Contexts[0].CampaignID = NewRepositoryReviewCampaignID()
	if repositoryReviewCampaignFindingMatchesCoverage(
		wrongCampaignContextState, finding, coverage, selected, indexes, nil,
	) {
		t.Fatal("finding matched a context tagged to another campaign")
	}
	secondaryContextState := state
	secondaryContextState.Contexts = append([]FindingContext(nil), state.Contexts...)
	secondaryContextState.Contexts[0].Files = []FileRef{second}
	if repositoryReviewCampaignFindingMatchesCoverage(
		secondaryContextState, finding, coverage, selected, indexes, nil,
	) {
		t.Fatal("finding matched a context that omitted its primary file")
	}
}

func TestRepositoryReviewCampaignRecordBindingEdgeCases(t *testing.T) {
	campaignID := NewRepositoryReviewCampaignID()
	if err := validateRepositoryReviewCampaignRecordBindings(RepositoryState{
		Runs: []ReviewRun{{}, {ID: "run", CampaignID: campaignID}},
		Contexts: []FindingContext{{}, {
			ID: "context", RunID: "run", CampaignID: NewRepositoryReviewCampaignID(),
		}},
	}); err == nil {
		t.Fatal("context tagged differently from its run was accepted")
	}
	if err := validateRepositoryReviewCampaignRecordBindings(RepositoryState{
		Runs:     []ReviewRun{{}},
		Contexts: []FindingContext{{}},
		Findings: []Finding{{ID: "same"}, {ID: "same"}},
	}); err == nil {
		t.Fatal("duplicate untagged finding identities were accepted")
	}
	if err := validateRepositoryReviewCampaignRecordBindings(RepositoryState{
		Runs: []ReviewRun{{}}, Contexts: []FindingContext{{}}, Findings: []Finding{{}},
	}); err != nil {
		t.Fatalf("anonymous legacy records error = %v", err)
	}
}

func repositoryReviewCampaignStoreWithLoadedStateForTest(
	t *testing.T,
	state RepositoryState,
) Store {
	t.Helper()
	store := newRepositoryAuditTestStore(t)
	store.loadForTest = func(string) (RepositoryState, error) { return state, nil }
	return store
}

func TestRepositoryReviewCampaignCorruptLifecycleReferencesFailClosed(t *testing.T) {
	repository := "owner/corrupt-campaign-lifecycle"

	t.Run("mapping claim missing occurrence", func(t *testing.T) {
		store := repositoryReviewCampaignStoreWithLoadedStateForTest(t, RepositoryState{
			Repository: repository,
			MappingJobs: []RepositoryMappingJob{{
				ID: "mapping", ReviewFindingID: "missing", State: RepositoryMappingPending,
			}},
		})
		if _, _, _, claimed, err := store.ClaimMappingJob(
			repository, "mapping", RepositoryMappingModelSnapshot{},
		); err == nil || claimed || !strings.Contains(err.Error(), "review finding is missing") {
			t.Fatalf("missing mapping occurrence claimed=%v err=%v", claimed, err)
		}
	})

	t.Run("mapping claim invalid durable state", func(t *testing.T) {
		store := repositoryReviewCampaignStoreWithLoadedStateForTest(t, RepositoryState{
			Repository: repository, Findings: []Finding{{ID: "finding"}},
			MappingJobs: []RepositoryMappingJob{{
				ID: "mapping", ReviewFindingID: "finding", State: RepositoryMappingJobState("invalid"),
			}},
		})
		if _, _, _, claimed, err := store.ClaimMappingJob(
			repository, "mapping", RepositoryMappingModelSnapshot{},
		); !errors.Is(err, ErrConflict) || claimed {
			t.Fatalf("invalid mapping state claimed=%v err=%v", claimed, err)
		}
	})

	t.Run("mapping completion missing occurrence", func(t *testing.T) {
		store := repositoryReviewCampaignStoreWithLoadedStateForTest(t, RepositoryState{
			Repository: repository,
			MappingJobs: []RepositoryMappingJob{{
				ID: "mapping", ReviewFindingID: "missing", State: RepositoryMappingRunning,
			}},
		})
		if _, _, err := store.CompleteMappingJob(repository, RepositoryMappingCompletion{
			JobID: "mapping", CreateMatchState: RepositoryMatchNew,
		}); err == nil || !strings.Contains(err.Error(), "review finding is missing") {
			t.Fatalf("missing mapping occurrence completion error = %v", err)
		}
	})

	t.Run("completed mapping occurrence disagreement", func(t *testing.T) {
		store := repositoryReviewCampaignStoreWithLoadedStateForTest(t, RepositoryState{
			Repository: repository,
			Findings:   []Finding{{ID: "finding", RepositoryFindingID: "aggregate-b"}},
			MappingJobs: []RepositoryMappingJob{{
				ID: "mapping", ReviewFindingID: "finding", State: RepositoryMappingCompleted,
				RepositoryFindingID: "aggregate-a",
			}},
		})
		if _, _, err := store.CompleteMappingJob(repository, RepositoryMappingCompletion{
			JobID: "mapping", RepositoryFindingID: "aggregate-a",
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("completed mapping disagreement error = %v", err)
		}
	})

	t.Run("completed mapping target missing", func(t *testing.T) {
		store := repositoryReviewCampaignStoreWithLoadedStateForTest(t, RepositoryState{
			Repository: repository,
			Findings:   []Finding{{ID: "finding", RepositoryFindingID: "aggregate"}},
			MappingJobs: []RepositoryMappingJob{{
				ID: "mapping", ReviewFindingID: "finding", State: RepositoryMappingCompleted,
				RepositoryFindingID: "aggregate",
			}},
		})
		if _, _, err := store.CompleteMappingJob(repository, RepositoryMappingCompletion{
			JobID: "mapping", RepositoryFindingID: "aggregate",
		}); err == nil || !strings.Contains(err.Error(), "target is missing") {
			t.Fatalf("missing completed mapping target error = %v", err)
		}
	})

	validationState := RepositoryState{
		Repository: repository,
		ValidationJobs: []RepositoryValidationJob{{
			ID: "validation", RepositoryFindingID: "missing", State: RepositoryValidationRunning,
		}},
	}
	t.Run("validation claim missing aggregate", func(t *testing.T) {
		store := repositoryReviewCampaignStoreWithLoadedStateForTest(t, validationState)
		if _, _, _, claimed, err := store.ClaimValidationJob(
			repository, "validation",
		); err == nil || claimed || !strings.Contains(err.Error(), "repository finding is missing") {
			t.Fatalf("missing validation aggregate claimed=%v err=%v", claimed, err)
		}
	})
	t.Run("validation completion missing aggregate", func(t *testing.T) {
		store := repositoryReviewCampaignStoreWithLoadedStateForTest(t, validationState)
		if _, _, _, err := store.CompleteValidationJob(repository, RepositoryValidationCompletion{
			JobID: "validation", Outcome: RepositoryValidationNotFixed,
		}); err == nil || !strings.Contains(err.Error(), "repository finding is missing") {
			t.Fatalf("missing validation aggregate completion error = %v", err)
		}
	})
	t.Run("validation release missing aggregate", func(t *testing.T) {
		store := repositoryReviewCampaignStoreWithLoadedStateForTest(t, validationState)
		if err := store.releaseValidationJob(repository, "validation"); err == nil ||
			!strings.Contains(err.Error(), "repository finding is missing") {
			t.Fatalf("missing validation aggregate release error = %v", err)
		}
	})
}

func TestRepositoryReviewCampaignFindingSelectionLegacyFallback(t *testing.T) {
	state := RepositoryState{
		Runs:     []ReviewRun{{ID: "run", FindingIDs: []string{"finding"}, CompletedAt: repositoryAuditTestNow}},
		Findings: []Finding{{ID: "finding"}},
	}
	selected := CurrentCampaignFindingsByID(
		state, " ", []string{"run"}, repositoryAuditTestNow.Add(-1),
	)
	if len(selected) != 1 || selected[0].ID != "finding" {
		t.Fatalf("legacy campaign finding selection = %#v", selected)
	}
}

func TestRepositoryReviewCampaignHelperValidationBoundaries(t *testing.T) {
	campaignID := NewRepositoryReviewCampaignID()
	file := repositoryAuditTestFile("pkg/a.go", "a", 1)
	digest := repositoryReviewCampaignTestScopeDigest(t, file)
	bound := &RepositoryReviewCampaignCoverage{
		ID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit,
		InventoryHash: repositoryReviewCampaignTestInventory,
		ProfileHash:   repositoryReviewCampaignTestProfile, ScopeDigest: digest,
		RequiredAssignments: 1, SelectedFiles: 1,
		Paths: map[string]RepositoryReviewCampaignPathCoverage{},
	}

	if changed, err := bindRepositoryReviewCampaignScope(
		nil, campaignID, repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		digest, 1, 1,
	); changed || !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("nil scope binding = %v, %v", changed, err)
	}
	wrongAuthority := RepositoryState{CurrentCampaign: &RepositoryReviewCampaignCoverage{
		ID: NewRepositoryReviewCampaignID(), CommitSHA: repositoryReviewCampaignTestCommit,
		Paths: map[string]RepositoryReviewCampaignPathCoverage{},
	}}
	if _, err := bindRepositoryReviewCampaignScope(
		&wrongAuthority, campaignID, repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		digest, 1, 1,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong scope authority error = %v", err)
	}
	corruptUnbound := RepositoryState{CurrentCampaign: &RepositoryReviewCampaignCoverage{
		ID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit,
		ScopeDigest: digest, Paths: map[string]RepositoryReviewCampaignPathCoverage{},
	}}
	if _, err := bindRepositoryReviewCampaignScope(
		&corruptUnbound, campaignID, repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		digest, 1, 1,
	); err == nil {
		t.Fatal("partially bound campaign scope was accepted")
	}
	unbound := RepositoryState{CurrentCampaign: &RepositoryReviewCampaignCoverage{
		ID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit,
	}}
	if changed, err := bindRepositoryReviewCampaignScope(
		&unbound, campaignID, repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		digest, 1, 1,
	); err != nil || !changed || unbound.CurrentCampaign.Paths == nil {
		t.Fatalf("nil-map scope binding = %#v, %v", unbound.CurrentCampaign, err)
	}

	if _, err := mergeRepositoryReviewCampaignPath(nil, file.Path,
		RepositoryReviewCampaignPathCoverage{Inspected: true}); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("nil coverage merge error = %v", err)
	}
	nilPaths := cloneRepositoryReviewCampaignCoverage(*bound)
	nilPaths.Paths = nil
	if changed, err := mergeRepositoryReviewCampaignPath(
		&nilPaths, file.Path, RepositoryReviewCampaignPathCoverage{Inspected: true},
	); err != nil || !changed || !nilPaths.Paths[file.Path].Inspected {
		t.Fatalf("nil-map path merge = %#v, %v", nilPaths, err)
	}
	conflict := cloneRepositoryReviewCampaignCoverage(*bound)
	conflict.Paths[file.Path] = RepositoryReviewCampaignPathCoverage{Unsupported: true}
	if _, err := mergeRepositoryReviewCampaignPath(
		&conflict, file.Path, RepositoryReviewCampaignPathCoverage{Inspected: true},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal path rewrite error = %v", err)
	}

	if validateRepositoryReviewCampaignCoverage(nil) != nil {
		t.Fatal("nil campaign coverage must remain valid")
	}
	for name, coverage := range map[string]*RepositoryReviewCampaignCoverage{
		"invalid identity": {
			ID: "bad", CommitSHA: repositoryReviewCampaignTestCommit,
			Paths: map[string]RepositoryReviewCampaignPathCoverage{},
		},
		"partial binding": {
			ID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit,
			InventoryHash: repositoryReviewCampaignTestInventory,
			Paths:         map[string]RepositoryReviewCampaignPathCoverage{},
		},
		"invalid path state": {
			ID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit,
			InventoryHash: repositoryReviewCampaignTestInventory,
			ProfileHash:   repositoryReviewCampaignTestProfile, ScopeDigest: digest,
			RequiredAssignments: 1, SelectedFiles: 1,
			Paths: map[string]RepositoryReviewCampaignPathCoverage{file.Path: {}},
		},
		"too many terminals": {
			ID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit,
			InventoryHash: repositoryReviewCampaignTestInventory,
			ProfileHash:   repositoryReviewCampaignTestProfile, ScopeDigest: digest,
			RequiredAssignments: 1, SelectedFiles: 0,
			Paths: map[string]RepositoryReviewCampaignPathCoverage{
				file.Path: {Completed: true},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateRepositoryReviewCampaignCoverage(coverage); err == nil {
				t.Fatal("invalid campaign coverage was accepted")
			}
		})
	}

	if clone := cloneRepositoryReviewCampaignCoverage(RepositoryReviewCampaignCoverage{}); clone.Paths != nil {
		t.Fatalf("nil path map clone = %#v", clone.Paths)
	}

	if _, err := normalizeRepositoryReviewCampaignRecordIDs([]string{"a", "b"}, 1, 8); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("record ID count bound error = %v", err)
	}
	for name, values := range map[string][]string{
		"whitespace": {" a"},
		"duplicate":  {"a", "a"},
	} {
		t.Run("record ID "+name, func(t *testing.T) {
			if _, err := normalizeRepositoryReviewCampaignRecordIDs(values, 2, 8); !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("normalization error = %v", err)
			}
		})
	}
	if got, err := normalizeRepositoryReviewCampaignRecordIDs([]string{"b", "a"}, 2, 8); err != nil ||
		len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("normalized IDs = %#v, %v", got, err)
	}
}

func TestRepositoryReviewCampaignEvidenceValidationMatrix(t *testing.T) {
	first := repositoryAuditTestFile("a.go", "1", 1)
	second := repositoryAuditTestFile("b.go", "2", 1)
	allowed := map[string]FileRef{first.Path: first, second.Path: second}
	observation := Observation{Model: "review-a", ScopeFiles: []FileRef{first}}
	valid := RepositoryReviewEvidence{
		AssignmentID: "assignment-a", ScopeFiles: []FileRef{first}, Required: true,
		Successful: true, AcknowledgedFiles: []FileRef{first}, Observation: &observation,
	}

	tests := []struct {
		name        string
		evidence    []RepositoryReviewEvidence
		required    int
		unsupported map[string]struct{}
	}{
		{name: "required denominator", evidence: []RepositoryReviewEvidence{valid}, required: 0},
		{name: "empty evidence", evidence: []RepositoryReviewEvidence{}, required: 1},
		{
			name:     "invalid assignment",
			evidence: []RepositoryReviewEvidence{{AssignmentID: " bad", ScopeFiles: []FileRef{first}, Required: true}},
			required: 1,
		},
		{
			name: "unsuccessful output",
			evidence: []RepositoryReviewEvidence{
				{AssignmentID: "failed", ScopeFiles: []FileRef{first}, Required: true, Observation: &observation},
			},
			required: 1,
		},
		{
			name: "successful missing observation",
			evidence: []RepositoryReviewEvidence{
				{AssignmentID: "success", ScopeFiles: []FileRef{first}, Required: true, Successful: true},
			},
			required: 1,
		},
		{
			name: "observation scope mismatch",
			evidence: []RepositoryReviewEvidence{
				{
					AssignmentID: "scope",
					ScopeFiles:   []FileRef{first},
					Required:     true,
					Successful:   true,
					Observation:  &Observation{Model: "review-a", ScopeFiles: []FileRef{second}},
				},
			},
			required: 1,
		},
		{
			name: "invalid acknowledgement",
			evidence: []RepositoryReviewEvidence{
				{
					AssignmentID:      "ack",
					ScopeFiles:        []FileRef{first},
					Required:          true,
					Successful:        true,
					AcknowledgedFiles: []FileRef{{Path: " bad"}},
					Observation:       &observation,
				},
			},
			required: 1,
		},
		{
			name: "unassigned acknowledgement",
			evidence: []RepositoryReviewEvidence{
				{
					AssignmentID:      "outside",
					ScopeFiles:        []FileRef{first},
					Required:          true,
					Successful:        true,
					AcknowledgedFiles: []FileRef{second},
					Observation:       &observation,
				},
			},
			required: 1,
		},
		{
			name: "missing required child",
			evidence: []RepositoryReviewEvidence{
				{AssignmentID: "optional", ScopeFiles: []FileRef{first}, Successful: false},
			},
			required: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			unsupported := test.unsupported
			if unsupported == nil {
				unsupported = map[string]struct{}{}
			}
			if _, _, _, err := deriveRepositoryReviewCampaignEvidence(
				test.evidence, map[string]FileRef{first.Path: first}, test.required, unsupported,
			); !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("evidence error = %v", err)
			}
		})
	}

	unsupportedOnly := map[string]struct{}{first.Path: {}}
	observations, inspected, completed, err := deriveRepositoryReviewCampaignEvidence(
		[]RepositoryReviewEvidence{}, map[string]FileRef{first.Path: first}, 1, unsupportedOnly,
	)
	if err != nil || len(observations) != 0 || len(inspected) != 0 || len(completed) != 0 {
		t.Fatalf("unsupported-only evidence = %#v %#v %#v, %v", observations, inspected, completed, err)
	}

	optional := valid
	optional.AssignmentID = "assignment-optional"
	optional.Required = false
	optional.ScopeFiles = []FileRef{second}
	optional.AcknowledgedFiles = []FileRef{second}
	optionalObservation := Observation{Model: "review-b", ScopeFiles: []FileRef{second}}
	optional.Observation = &optionalObservation
	observations, inspected, completed, err = deriveRepositoryReviewCampaignEvidence(
		[]RepositoryReviewEvidence{valid, optional}, allowed, 1, map[string]struct{}{second.Path: {}},
	)
	if err != nil || len(observations) != 2 || len(inspected) != 2 || len(completed) != 1 ||
		completed[0] != first {
		t.Fatalf("mixed required/optional evidence = %#v %#v %#v, %v", observations, inspected, completed, err)
	}
}

func TestRepositoryReviewCampaignEvidenceProjectionOrderingAndNestedBounds(t *testing.T) {
	first := repositoryAuditTestFile("a.go", "a", 1)
	second := repositoryAuditTestFile("b.go", "b", 1)
	observation := Observation{Model: "review-a", ScopeFiles: []FileRef{first, second}}
	observations, inspected, completed, err := deriveRepositoryReviewCampaignEvidence(
		[]RepositoryReviewEvidence{{
			AssignmentID: "assignment", ScopeFiles: []FileRef{first, second}, Required: true,
			Successful: true, AcknowledgedFiles: []FileRef{second, first}, Observation: &observation,
		}},
		map[string]FileRef{first.Path: first, second.Path: second}, 1, map[string]struct{}{},
	)
	if err != nil || len(observations) != 1 || len(inspected) != 2 || len(completed) != 2 ||
		inspected[0] != first || inspected[1] != second || completed[0] != first || completed[1] != second {
		t.Fatalf("ordered campaign projections = %#v %#v %#v, %v", observations, inspected, completed, err)
	}

	longFile := repositoryAuditTestFile(strings.Repeat("a", 4096), "c", 1)
	oversized := make([]FileRef, 4200)
	for index := range oversized {
		oversized[index] = longFile
	}
	for name, evidence := range map[string][]RepositoryReviewEvidence{
		"acknowledgements": {{
			AssignmentID: "assignment", ScopeFiles: []FileRef{first},
			AcknowledgedFiles: oversized,
		}},
		"observation scope": {{
			AssignmentID: "assignment", ScopeFiles: []FileRef{first},
			Observation: &Observation{ScopeFiles: oversized},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := deriveRepositoryReviewCampaignEvidence(
				evidence, map[string]FileRef{first.Path: first}, 1, map[string]struct{}{},
			); !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("nested evidence metadata error = %v", err)
			}
		})
	}
}

func TestRepositoryReviewCampaignPlanAndBindingHelpersRejectForgery(t *testing.T) {
	file := repositoryAuditTestFile("a.go", "1", 1)
	base := Plan{
		CampaignID: NewRepositoryReviewCampaignID(), Repository: "owner/repo",
		CommitSHA: repositoryReviewCampaignTestCommit, InventoryHash: repositoryReviewCampaignTestInventory,
		ProfileHash: repositoryReviewCampaignTestProfile, RequiredAssignments: 1,
		Authoritative: true, PendingFiles: []FileRef{file}, UnchangedFiles: []FileRef{},
	}
	mutations := map[string]func(*Plan){
		"non-authoritative":       func(plan *Plan) { plan.Authoritative = false },
		"duplicate across groups": func(plan *Plan) { plan.UnchangedFiles = []FileRef{file} },
		"bad unsupported reason": func(plan *Plan) {
			plan.PendingFiles = nil
			plan.UnsupportedFiles = []UnsupportedFile{{FileRef: file, Reason: " bad"}}
		},
		"unsupported overlap": func(plan *Plan) {
			plan.UnsupportedFiles = []UnsupportedFile{{FileRef: file, Reason: "binary"}}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			plan := base
			mutate(&plan)
			if _, err := validateRepositoryReviewCampaignPlan(plan); !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("plan validation error = %v", err)
			}
		})
	}

	bad := file
	bad.Path = " a.go"
	if _, err := repositoryReviewCampaignScopeDigestForFiles([]FileRef{bad}); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("scope digest error = %v", err)
	}
	if _, err := canonicalRepositoryReviewCampaignFiles([]FileRef{bad}); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("canonical exact file error = %v", err)
	}
	if _, err := bindRepositoryReviewCampaignFiles(
		[]FileRef{bad},
		map[string]FileRef{file.Path: file},
	); !errors.Is(
		err,
		ErrInvalidPlan,
	) {
		t.Fatalf("campaign file normalization error = %v", err)
	}
	other := repositoryAuditTestFile("other.go", "2", 1)
	if _, err := bindRepositoryReviewCampaignFiles(
		[]FileRef{other},
		map[string]FileRef{file.Path: file},
	); !errors.Is(
		err,
		ErrInvalidPlan,
	) {
		t.Fatalf("outside campaign file error = %v", err)
	}
	if containsRepositoryReviewFile([]FileRef{file}, "missing.go") {
		t.Fatal("missing path was reported in campaign file list")
	}
	if !containsRepositoryReviewFile([]FileRef{file}, file.Path) {
		t.Fatal("present path was absent from campaign file list")
	}
	if validRepositoryReviewPath(" a.go") {
		t.Fatal("repository review path with surrounding whitespace was accepted")
	}
}

func TestRepositoryReviewCampaignSemanticMergeRejectsUnrelatedAnchors(t *testing.T) {
	file := repositoryAuditTestFile("a.go", "a", 1)
	candidate := FindingCandidate{
		Title: "same title", Symbol: "Save",
		MatchHints: MatchHints{SourceAnchors: []string{"candidate-anchor"}},
	}
	existing := Finding{
		File: file, Title: "same title", Symbol: "Save",
		MatchHints: MatchHints{SourceAnchors: []string{"existing-anchor"}},
	}
	if index := semanticFindingIndex([]Finding{existing}, file, candidate); index != -1 {
		t.Fatalf("unrelated causal anchors merged at index %d", index)
	}
}

func TestRepositoryReviewCampaignPlanValidationInventoryBounds(t *testing.T) {
	file := repositoryAuditTestFile("a.go", "a", 1)
	base := Plan{
		CampaignID: NewRepositoryReviewCampaignID(), Repository: "owner/plan-bounds",
		CommitSHA: repositoryReviewCampaignTestCommit, InventoryHash: repositoryReviewCampaignTestInventory,
		ProfileHash: repositoryReviewCampaignTestProfile, RequiredAssignments: 1,
		Authoritative: true, PendingFiles: []FileRef{file}, UnchangedFiles: []FileRef{},
	}
	badPending := base
	badPending.PendingFiles = []FileRef{{Path: " bad"}}
	if _, err := validateRepositoryReviewCampaignPlan(badPending); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("noncanonical pending file error = %v", err)
	}
	badUnsupported := base
	badUnsupported.PendingFiles = nil
	badUnsupported.UnsupportedFiles = []UnsupportedFile{{
		FileRef: FileRef{Path: " bad"}, Reason: "binary",
	}}
	if _, err := validateRepositoryReviewCampaignPlan(badUnsupported); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("noncanonical unsupported file error = %v", err)
	}

	tooMany := base
	tooMany.PendingFiles = make([]FileRef, 50_001)
	tooMany.DeferredFiles = make([]FileRef, 50_000)
	for index := range 100_001 {
		candidate := repositoryAuditTestFile(fmt.Sprintf("f/%06d.go", index), "d", 1)
		if index < len(tooMany.PendingFiles) {
			tooMany.PendingFiles[index] = candidate
		} else {
			tooMany.DeferredFiles[index-len(tooMany.PendingFiles)] = candidate
		}
	}
	if _, err := validateRepositoryReviewCampaignPlan(tooMany); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("campaign selected-file count error = %v", err)
	}
}

func installRepositoryReviewCampaignCoverageForTest(
	t *testing.T,
	store Store,
	repository string,
	file FileRef,
	pathCoverage RepositoryReviewCampaignPathCoverage,
) RepositoryState {
	t.Helper()
	state, _, err := store.Get(repository)
	if err != nil {
		t.Fatal(err)
	}
	state.CurrentCampaign.InventoryHash = repositoryReviewCampaignTestInventory
	state.CurrentCampaign.ProfileHash = repositoryReviewCampaignTestProfile
	state.CurrentCampaign.ScopeDigest = repositoryReviewCampaignTestScopeDigest(t, file)
	state.CurrentCampaign.RequiredAssignments = 1
	state.CurrentCampaign.SelectedFiles = 1
	state.CurrentCampaign.Paths = map[string]RepositoryReviewCampaignPathCoverage{file.Path: pathCoverage}
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestRepositoryReviewCampaignPlanRejectsCoverageReclassification(t *testing.T) {
	file := repositoryAuditTestFile("service.go", "a", 1)
	for name, setup := range map[string]func(*RepositoryState){
		"reviewed over unsupported": func(state *RepositoryState) {
			state.Files[file.Path] = ReviewedFile{
				FileRef: file, CommitSHA: repositoryReviewCampaignTestCommit,
				ProfileHash: repositoryReviewCampaignTestProfile, RunID: "prior",
				ReviewedAt: repositoryAuditTestNow,
			}
		},
		"unsupported over inspected": func(state *RepositoryState) {
			state.Unsupported[file.Path] = UnsupportedFile{
				FileRef: file, CommitSHA: repositoryReviewCampaignTestCommit,
				ProfileHash: repositoryReviewCampaignTestProfile, Reason: "binary",
				UpdatedAt: repositoryAuditTestNow,
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := newRepositoryAuditTestStore(t)
			campaignID, _ := beginRepositoryReviewCampaignForTest(
				t, store, "owner/plan-conflict-"+strings.ReplaceAll(name, " ", "-"), true,
			)
			pathCoverage := RepositoryReviewCampaignPathCoverage{Unsupported: true}
			if name == "unsupported over inspected" {
				pathCoverage = RepositoryReviewCampaignPathCoverage{Inspected: true}
			}
			state := installRepositoryReviewCampaignCoverageForTest(
				t, store, "owner/plan-conflict-"+strings.ReplaceAll(name, " ", "-"), file, pathCoverage,
			)
			setup(&state)
			if err := store.save(&state); err != nil {
				t.Fatal(err)
			}
			if _, err := store.PlanWithProfileLimitAuthoritativeForCampaign(
				context.Background(), state.Repository, repositoryReviewCampaignTestCommit,
				repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
				campaignID, 1, []FileRef{file}, false, 1, true,
			); !errors.Is(err, ErrConflict) {
				t.Fatalf("campaign coverage reclassification error = %v", err)
			}
		})
	}

	store := newRepositoryAuditTestStore(t)
	campaignID, _ := beginRepositoryReviewCampaignForTest(t, store, "owner/plan-save", true)
	poisonRepositoryReviewStoreOnClock(t, &store)
	if _, err := store.PlanWithProfileLimitAuthoritativeForCampaign(
		context.Background(), "owner/plan-save", repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		campaignID, 1, []FileRef{file}, false, 1, true,
	); err == nil {
		t.Fatal("campaign plan ignored a durable-write failure")
	}
}

func TestRepositoryReviewCampaignRecordBoundaryFailures(t *testing.T) {
	newRequest := func(t *testing.T) (Store, RecordRequest, FileRef) {
		t.Helper()
		store := newRepositoryAuditTestStore(t)
		repository := "owner/record-boundary-" + strings.ReplaceAll(t.Name(), "/", "-")
		campaignID, _ := beginRepositoryReviewCampaignForTest(t, store, repository, true)
		file := repositoryAuditTestFile("service.go", "a", 1)
		plan := planRepositoryReviewCampaignForTest(t, store, repository, campaignID, []FileRef{file}, false)
		return store, RecordRequest{
			Plan: plan, RunID: "boundary-run", CompletedAt: repositoryAuditTestNow,
			InspectedFiles: []FileRef{}, CompletedFiles: []FileRef{},
			ReviewEvidence: []RepositoryReviewEvidence{{
				AssignmentID: "failed-required", ScopeFiles: []FileRef{file}, Required: true,
			}},
		}, file
	}

	t.Run("raw campaign ID", func(t *testing.T) {
		store, request, _ := newRequest(t)
		request.Plan.CampaignID = " " + request.Plan.CampaignID
		request.Plan.ID = planDigest(request.Plan)
		if _, err := store.Record(context.Background(), request); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("raw campaign ID error = %v", err)
		}
	})

	t.Run("nil evidence", func(t *testing.T) {
		store, request, _ := newRequest(t)
		request.ReviewEvidence = nil
		if _, err := store.Record(context.Background(), request); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("nil campaign evidence error = %v", err)
		}
	})

	for name, unsupported := range map[string][]UnsupportedFile{
		"invalid unsupported evidence": {{
			FileRef: repositoryAuditTestFile("service.go", "a", 1), Reason: " binary ",
		}},
		"duplicate unsupported evidence": {
			{FileRef: repositoryAuditTestFile("service.go", "a", 1), Reason: "binary"},
			{FileRef: repositoryAuditTestFile("service.go", "a", 1), Reason: "binary"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, request, _ := newRequest(t)
			request.UnsupportedFiles = unsupported
			request.ReviewEvidence = []RepositoryReviewEvidence{}
			if _, err := store.Record(context.Background(), request); !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("unsupported evidence error = %v", err)
			}
		})
	}

	t.Run("completed file outside plan", func(t *testing.T) {
		store, request, _ := newRequest(t)
		request.CompletedFiles = []FileRef{repositoryAuditTestFile("outside.go", "b", 1)}
		if _, err := store.Record(context.Background(), request); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("outside completed file error = %v", err)
		}
	})

	t.Run("unsupported overlaps inspection", func(t *testing.T) {
		store, request, file := newRequest(t)
		observation := Observation{Model: "review-a", ScopeFiles: []FileRef{file}}
		request.InspectedFiles = []FileRef{file}
		request.CompletedFiles = []FileRef{file}
		request.ReviewEvidence = []RepositoryReviewEvidence{
			repositoryReviewCampaignSuccessfulEvidence(
				[]FileRef{file}, []FileRef{file}, observation, true,
			),
		}
		request.UnsupportedFiles = []UnsupportedFile{{FileRef: file, Reason: "binary"}}
		if _, err := store.Record(context.Background(), request); !errors.Is(err, ErrInvalidPlan) {
			t.Fatalf("overlapping unsupported evidence error = %v", err)
		}
	})

	t.Run("authority changes without version", func(t *testing.T) {
		store, request, _ := newRequest(t)
		state, _, err := store.Get(request.Plan.Repository)
		if err != nil {
			t.Fatal(err)
		}
		replacementID := NewRepositoryReviewCampaignID()
		state.CampaignHistory[replacementID] = state.CurrentCampaign.CommitSHA
		state.CurrentCampaign.ID = replacementID
		if err := store.save(&state); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Record(context.Background(), request); !errors.Is(err, ErrConflict) {
			t.Fatalf("changed campaign authority error = %v", err)
		}
	})
}

func repositoryReviewCampaignManualNoopPlanForTest(
	state RepositoryState,
	file FileRef,
	unsupported bool,
) Plan {
	plan := Plan{
		CampaignID: state.CurrentCampaign.ID, Repository: state.Repository,
		CommitSHA: state.CurrentCampaign.CommitSHA, InventoryHash: state.CurrentCampaign.InventoryHash,
		ProfileHash:         state.CurrentCampaign.ProfileHash,
		RequiredAssignments: state.CurrentCampaign.RequiredAssignments,
		Authoritative:       true, StateVersion: state.ReviewVersion,
		PendingFiles: []FileRef{}, DeferredFiles: []FileRef{}, UnchangedFiles: []FileRef{},
		CreatedAt: repositoryAuditTestNow,
	}
	if unsupported {
		plan.UnsupportedFiles = []UnsupportedFile{{FileRef: file, Reason: "binary"}}
	} else {
		plan.UnchangedFiles = []FileRef{file}
	}
	plan.ID = planDigest(plan)
	return plan
}

func TestRepositoryReviewCampaignRecordRejectsDurableCoverageConflicts(t *testing.T) {
	file := repositoryAuditTestFile("service.go", "a", 1)
	newStore := func(t *testing.T, pathCoverage RepositoryReviewCampaignPathCoverage) (Store, RepositoryState) {
		t.Helper()
		store := newRepositoryAuditTestStore(t)
		repository := "owner/record-coverage-" + strings.ReplaceAll(t.Name(), "/", "-")
		_, _ = beginRepositoryReviewCampaignForTest(t, store, repository, true)
		state := installRepositoryReviewCampaignCoverageForTest(
			t, store, repository, file, pathCoverage,
		)
		return store, state
	}

	t.Run("inspection over unsupported", func(t *testing.T) {
		store, state := newStore(t, RepositoryReviewCampaignPathCoverage{Unsupported: true})
		plan, err := store.PlanWithProfileLimitAuthoritativeForCampaign(
			context.Background(), state.Repository, repositoryReviewCampaignTestCommit,
			repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
			state.CurrentCampaign.ID, 1, []FileRef{file}, false, 1, true,
		)
		if err != nil {
			t.Fatal(err)
		}
		observation := Observation{Model: "review-a", ScopeFiles: []FileRef{file}}
		if _, err := store.Record(context.Background(), RecordRequest{
			Plan: plan, RunID: "inspect-conflict", CompletedAt: repositoryAuditTestNow,
			InspectedFiles: []FileRef{file}, CompletedFiles: []FileRef{file},
			ReviewEvidence: []RepositoryReviewEvidence{
				repositoryReviewCampaignSuccessfulEvidence(
					[]FileRef{file}, []FileRef{file}, observation, true,
				),
			},
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("inspection over unsupported error = %v", err)
		}
	})

	t.Run("unsupported over inspected", func(t *testing.T) {
		store, state := newStore(t, RepositoryReviewCampaignPathCoverage{Inspected: true})
		plan, err := store.PlanWithProfileLimitAuthoritativeForCampaign(
			context.Background(), state.Repository, repositoryReviewCampaignTestCommit,
			repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
			state.CurrentCampaign.ID, 1, []FileRef{file}, false, 1, true,
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Record(context.Background(), RecordRequest{
			Plan: plan, RunID: "unsupported-conflict", CompletedAt: repositoryAuditTestNow,
			InspectedFiles: []FileRef{}, CompletedFiles: []FileRef{},
			UnsupportedFiles: []UnsupportedFile{{FileRef: file, Reason: "binary"}},
			ReviewEvidence:   []RepositoryReviewEvidence{},
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("unsupported over inspected error = %v", err)
		}
	})

	t.Run("prechecked over unsupported", func(t *testing.T) {
		store, state := newStore(t, RepositoryReviewCampaignPathCoverage{Unsupported: true})
		plan := repositoryReviewCampaignManualNoopPlanForTest(state, file, false)
		if _, err := store.Record(context.Background(), RecordRequest{
			Plan: plan, RunID: "prechecked-conflict", CompletedAt: repositoryAuditTestNow,
			InspectedFiles: []FileRef{}, CompletedFiles: []FileRef{},
			ReviewEvidence: []RepositoryReviewEvidence{},
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("prechecked over unsupported error = %v", err)
		}
	})

	t.Run("preclassified unsupported over inspected", func(t *testing.T) {
		store, state := newStore(t, RepositoryReviewCampaignPathCoverage{Inspected: true})
		plan := repositoryReviewCampaignManualNoopPlanForTest(state, file, true)
		if _, err := store.Record(context.Background(), RecordRequest{
			Plan: plan, RunID: "preclassified-conflict", CompletedAt: repositoryAuditTestNow,
			InspectedFiles: []FileRef{}, CompletedFiles: []FileRef{},
			ReviewEvidence: []RepositoryReviewEvidence{},
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("preclassified unsupported over inspected error = %v", err)
		}
	})
}

func TestRepositoryReviewCampaignFinalizeBoundaries(t *testing.T) {
	file := repositoryAuditTestFile("service.go", "a", 1)
	newStore := func(t *testing.T, pathCoverage RepositoryReviewCampaignPathCoverage) (Store, RepositoryState) {
		t.Helper()
		store := newRepositoryAuditTestStore(t)
		repository := "owner/finalize-" + strings.ReplaceAll(t.Name(), "/", "-")
		_, _ = beginRepositoryReviewCampaignForTest(t, store, repository, true)
		return store, installRepositoryReviewCampaignCoverageForTest(
			t, store, repository, file, pathCoverage,
		)
	}

	store, state := newStore(t, RepositoryReviewCampaignPathCoverage{Inspected: true})
	invalid := repositoryReviewCampaignManualNoopPlanForTest(state, file, true)
	invalid.RequiredAssignments = 0
	invalid.ID = planDigest(invalid)
	if _, err := store.FinalizeNoopPlan(invalid); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("invalid campaign no-op plan error = %v", err)
	}

	for name, pathCoverage := range map[string]RepositoryReviewCampaignPathCoverage{
		"prechecked over unsupported":              {Unsupported: true},
		"preclassified unsupported over inspected": {Inspected: true},
	} {
		t.Run(name, func(t *testing.T) {
			store, state := newStore(t, pathCoverage)
			plan := repositoryReviewCampaignManualNoopPlanForTest(
				state, file, name == "preclassified unsupported over inspected",
			)
			if _, err := store.FinalizeNoopPlan(plan); !errors.Is(err, ErrConflict) {
				t.Fatalf("finalize coverage conflict error = %v", err)
			}
		})
	}

	t.Run("authority changes without version", func(t *testing.T) {
		store, state := newStore(t, RepositoryReviewCampaignPathCoverage{Inspected: true})
		plan := repositoryReviewCampaignManualNoopPlanForTest(state, file, true)
		replacementID := NewRepositoryReviewCampaignID()
		state.CampaignHistory[replacementID] = state.CurrentCampaign.CommitSHA
		state.CurrentCampaign.ID = replacementID
		if err := store.save(&state); err != nil {
			t.Fatal(err)
		}
		if _, err := store.FinalizeNoopPlan(plan); !errors.Is(err, ErrConflict) {
			t.Fatalf("changed finalize authority error = %v", err)
		}
	})
}

func TestRepositoryReviewCampaignHistoryMigrationIntegrity(t *testing.T) {
	if changed, err := migrateRepositoryReviewCampaignHistory(nil); err != nil || changed {
		t.Fatalf("nil history migration = %v, %v", changed, err)
	}
	if changed, err := migrateRepositoryReviewCampaignHistory(&RepositoryState{}); err != nil || changed {
		t.Fatalf("empty history migration = %v, %v", changed, err)
	}
	campaignID := NewRepositoryReviewCampaignID()
	invalid := RepositoryState{Runs: []ReviewRun{{CampaignID: campaignID, CommitSHA: "bad"}}}
	if _, err := migrateRepositoryReviewCampaignHistory(&invalid); err == nil {
		t.Fatal("invalid tagged campaign history was accepted")
	}
	conflicting := RepositoryState{
		Runs: []ReviewRun{
			{CampaignID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit},
			{CampaignID: campaignID, CommitSHA: repositoryReviewCampaignOtherCommit},
		},
	}
	if _, err := migrateRepositoryReviewCampaignHistory(&conflicting); err == nil {
		t.Fatal("conflicting tagged campaign commits were accepted")
	}
	existingConflict := RepositoryState{
		Runs:            []ReviewRun{{CampaignID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit}},
		CampaignHistory: map[string]string{campaignID: repositoryReviewCampaignOtherCommit},
	}
	if _, err := migrateRepositoryReviewCampaignHistory(&existingConflict); err == nil {
		t.Fatal("history overwrite conflict was accepted")
	}
	for name, candidate := range map[string]RepositoryState{
		"current": {
			CurrentCampaign: &RepositoryReviewCampaignCoverage{ID: campaignID, CommitSHA: "bad"},
		},
		"context": {
			Contexts: []FindingContext{{CampaignID: campaignID, CommitSHA: "bad"}},
		},
		"finding": {
			Findings: []Finding{{CampaignID: campaignID, CommitSHA: "bad"}},
		},
	} {
		t.Run("invalid "+name+" tag", func(t *testing.T) {
			if _, err := migrateRepositoryReviewCampaignHistory(&candidate); err == nil {
				t.Fatal("invalid tagged campaign history was accepted")
			}
		})
	}
	crossRecordConflict := RepositoryState{
		Runs: []ReviewRun{{CampaignID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit}},
		Contexts: []FindingContext{{
			CampaignID: campaignID, CommitSHA: repositoryReviewCampaignOtherCommit,
		}},
	}
	if _, err := migrateRepositoryReviewCampaignHistory(&crossRecordConflict); err == nil {
		t.Fatal("cross-record campaign commit conflict was accepted")
	}

	state := repositoryReviewCoverageState("owner/migrate-campaign")
	state.CurrentCampaign = &RepositoryReviewCampaignCoverage{
		ID: campaignID, CommitSHA: repositoryReviewCampaignTestCommit, Exact: true,
	}
	state.CampaignHistory = nil
	migrated, err := migrateRepositoryState(&state)
	if err != nil || !migrated || state.CurrentCampaign.Paths == nil || state.CurrentCampaign.Exact ||
		state.CampaignHistory[campaignID] != repositoryReviewCampaignTestCommit {
		t.Fatalf("legacy campaign migration = %#v, migrated=%v err=%v", state, migrated, err)
	}
	invalidMigration := repositoryReviewCoverageState("owner/invalid-campaign-migration")
	invalidMigration.CurrentCampaign = &RepositoryReviewCampaignCoverage{
		ID: campaignID, CommitSHA: "bad", Paths: map[string]RepositoryReviewCampaignPathCoverage{},
	}
	if _, err := migrateRepositoryState(&invalidMigration); err == nil {
		t.Fatal("state migration ignored invalid campaign history")
	}
}

func TestRepositoryReviewCampaignControlValidation(t *testing.T) {
	store := newAutomationTestStore(t)
	automation := validAutomationForTest("rra_campaign_bounds", "Campaign bounds")
	automation.CampaignID = " " + NewRepositoryReviewCampaignID() + " "
	automation, err := store.CreateAutomation(context.Background(), automation)
	if err != nil || !ValidRepositoryReviewCampaignID(automation.CampaignID) {
		t.Fatalf("created campaign ID = %q, %v", automation.CampaignID, err)
	}
	automation.CampaignID = "rrc_-bad"
	if err := validateAutomation(automation); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("invalid automation campaign error = %v", err)
	}

	state := repositoryReviewCoverageState("owner/state-validation")
	state.CurrentCampaign = &RepositoryReviewCampaignCoverage{
		ID: NewRepositoryReviewCampaignID(), CommitSHA: repositoryReviewCampaignTestCommit,
		Paths: map[string]RepositoryReviewCampaignPathCoverage{},
	}
	state.CampaignHistory = map[string]string{}
	if err := validateState(state); err == nil || !strings.Contains(err.Error(), "absent from history") {
		t.Fatalf("missing current-campaign history error = %v", err)
	}
	state.CurrentCampaign.ID = "bad"
	if err := validateState(state); err == nil {
		t.Fatal("invalid current campaign coverage was accepted")
	}
	state.CurrentCampaign = nil
	state.CampaignHistory = map[string]string{"bad": repositoryReviewCampaignTestCommit}
	if err := validateState(state); err == nil {
		t.Fatal("invalid campaign history was accepted")
	}
}
