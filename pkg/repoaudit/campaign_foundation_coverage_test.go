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
			DeduplicationSnapshot: repositoryReviewDeduplicationSnapshotForTest(),
			Repository:            repository, CampaignID: NewRepositoryReviewCampaignID(),
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
		if err := os.Mkdir(repositoryReviewTestLockPath(t, store.root, "store.lock"), 0o700); err != nil {
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
		t.Skip("per-ledger JSON load fault replaced by SQLite integrity coverage")
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
	catalog, err := repositoryReviewAssignmentCatalogCountForTest(
		repositoryReviewCampaignTestProfile, 1, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	state.CurrentCampaign.AssignmentCatalog = catalog
	state.CurrentCampaign.RequiredAssignments = 1
	state.CurrentCampaign.SelectedFiles = 1
	if pathCoverage.Inspected {
		bits := make([]byte, (len(catalog)+7)/8)
		setRepositoryReviewAssignmentBit(bits, 1)
		pathCoverage.AssignmentBits = encodeRepositoryReviewAssignmentBits(bits)
		pathCoverage, err = projectRepositoryReviewAssignmentCoverage(pathCoverage, catalog)
		if err != nil {
			t.Fatal(err)
		}
	}
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
			if _, err := store.planAssignmentsForCampaignCountForTest(
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
	if _, err := store.planAssignmentsForCampaignCountForTest(
		context.Background(), "owner/plan-save", repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		campaignID, 1, []FileRef{file}, false, 1, true,
	); err == nil {
		t.Fatal("campaign plan ignored a durable-write failure")
	}
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
		AssignmentCatalog: append(
			[]RepositoryReviewAssignment(nil), state.CurrentCampaign.AssignmentCatalog...,
		),
		Authoritative: true, StateVersion: state.ReviewVersion,
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
