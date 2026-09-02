package repoaudit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type assignmentSecondErrorContext struct {
	calls atomic.Int32
}

func (ctx *assignmentSecondErrorContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *assignmentSecondErrorContext) Done() <-chan struct{}       { return nil }
func (ctx *assignmentSecondErrorContext) Value(any) any               { return nil }
func (ctx *assignmentSecondErrorContext) Err() error {
	if ctx.calls.Add(1) >= 2 {
		return context.Canceled
	}
	return nil
}

func TestRepositoryReviewAssignmentGovernanceProgressBranches(t *testing.T) {
	profileHash := "sha256:" + strings.Repeat("a", 64)
	required, err := NewRepositoryReviewAssignment(
		RepositoryReviewFocusCorrectnessState, "required", "prompt", profileHash, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	optional, err := NewRepositoryReviewAssignment(
		RepositoryReviewFocusSecurityTrust, "optional", "prompt", profileHash, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	campaignID := NewRepositoryReviewCampaignID()
	file := repositoryAuditTestFile("progress.go", "a", 1)
	state := RepositoryState{
		CurrentCampaign: &RepositoryReviewCampaignCoverage{
			ID: campaignID, SelectedFiles: 1,
			AssignmentCatalog: []RepositoryReviewAssignment{required, optional},
			Paths:             map[string]RepositoryReviewCampaignPathCoverage{file.Path: {}},
		},
		ActiveReviewRun: &RepositoryReviewActiveRun{
			CampaignID: campaignID,
			Reservations: map[string]RepositoryReviewAssignmentReservation{
				required.ID: {AssignmentID: required.ID, Files: []FileRef{file}, CheckpointDigest: "done"},
				optional.ID: {AssignmentID: optional.ID, Files: []FileRef{file}},
				"unknown":   {AssignmentID: "unknown", Files: []FileRef{file}},
			},
		},
	}
	progress := CurrentCampaignAssignmentProgress(state, campaignID)
	if progress.Total != 1 || progress.Active != 0 || progress.Pending != 1 {
		t.Fatalf("progress = %#v", progress)
	}
}

func TestRepositoryReviewAssignmentGovernanceLockAndContextErrors(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	checkpoint := assignmentCoverageCheckpoint(fixture, "run", 0, fixture.files)
	verify := VerifyRepositoryReviewAssignmentRequest{
		Repository: fixture.repository, RunID: "run",
		AssignmentID: fixture.plan.AssignmentPlans[0].AssignmentID,
		Files:        fixture.plan.AssignmentPlans[0].Files,
	}
	for name, call := range map[string]func(Store) error{
		"begin": func(store Store) error {
			_, err := store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
				Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
			})
			return err
		},
		"verify": func(store Store) error {
			return store.VerifyRepositoryReviewAssignment(t.Context(), verify)
		},
		"checkpoint": func(store Store) error {
			_, err := store.CheckpointRepositoryReviewAssignment(t.Context(), checkpoint)
			return err
		},
		"finalize": func(store Store) error {
			_, err := store.FinalizeRepositoryReviewRun(t.Context(), FinalizeRepositoryReviewRunRequest{
				Plan: fixture.plan, RunID: "run",
			})
			return err
		},
		"interrupt": func(store Store) error {
			_, err := store.InterruptRepositoryReviewRun(t.Context(), fixture.repository, "run")
			return err
		},
		"abandoned": func(store Store) error {
			_, _, err := store.InterruptAbandonedRepositoryReviewRun(t.Context(), fixture.repository)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			store := NewStore(t.TempDir())
			if err := os.MkdirAll(repositoryReviewTestLockPath(t, store.root, "store.lock"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := call(store); err == nil {
				t.Fatal("irregular lock was accepted")
			}
		})
	}

	if _, err := fixture.store.BeginRepositoryReviewRun(
		&assignmentSecondErrorContext{},
		BeginRepositoryReviewRunRequest{Plan: fixture.plan, RunID: "begin-canceled", ReviewableFiles: fixture.files},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("post-lock begin cancellation = %v", err)
	}
	if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CheckpointRepositoryReviewAssignment(
		&assignmentSecondErrorContext{}, checkpoint,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("post-lock checkpoint cancellation = %v", err)
	}
}

func TestRepositoryReviewAssignmentGovernancePersistenceErrors(t *testing.T) {
	t.Run("begin", func(t *testing.T) {
		fixture := newAssignmentCoverageFixture(t, 1, 1)
		state, _, err := fixture.store.Get(fixture.repository)
		if err != nil {
			t.Fatal(err)
		}
		state.ID = "invalid"
		store := fixture.store
		store.loadForTest = func(string) (RepositoryState, error) { return state, nil }
		if _, err := store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
		}); err == nil {
			t.Fatal("begin save failure was hidden")
		}
	})

	for _, operation := range []string{"checkpoint", "finalize", "interrupt", "abandoned"} {
		t.Run(operation, func(t *testing.T) {
			fixture := newAssignmentCoverageFixture(t, 1, 1)
			if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
				Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
			}); err != nil {
				t.Fatal(err)
			}
			state, _, err := fixture.store.Get(fixture.repository)
			if err != nil {
				t.Fatal(err)
			}
			state.ID = "invalid"
			store := fixture.store
			store.loadForTest = func(string) (RepositoryState, error) { return state, nil }
			switch operation {
			case "checkpoint":
				_, err = store.CheckpointRepositoryReviewAssignment(
					t.Context(), assignmentCoverageCheckpoint(fixture, "run", 0, fixture.files),
				)
			case "finalize":
				_, err = store.FinalizeRepositoryReviewRun(t.Context(), FinalizeRepositoryReviewRunRequest{
					Plan: fixture.plan, RunID: "run",
				})
			case "interrupt":
				_, err = store.InterruptRepositoryReviewRun(t.Context(), fixture.repository, "run")
			default:
				_, _, err = store.InterruptAbandonedRepositoryReviewRun(t.Context(), fixture.repository)
			}
			if err == nil {
				t.Fatal("save failure was hidden")
			}
		})
	}
}

func TestRepositoryReviewAssignmentGovernanceCorruptStateErrors(t *testing.T) {
	if _, err := (Store{}).CheckpointRepositoryReviewAssignment(
		nil, CheckpointRepositoryReviewAssignmentRequest{},
	); err == nil {
		t.Fatal("empty nil-context checkpoint accepted")
	}

	t.Run("begin bits", func(t *testing.T) {
		fixture := newAssignmentCoverageFixture(t, 1, 1)
		state, _, err := fixture.store.Get(fixture.repository)
		if err != nil {
			t.Fatal(err)
		}
		state.CurrentCampaign.Paths[fixture.files[0].Path] = RepositoryReviewCampaignPathCoverage{
			AssignmentBits: "!",
		}
		store := fixture.store
		store.loadForTest = func(string) (RepositoryState, error) { return state, nil }
		if _, err := store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
		}); err == nil {
			t.Fatal("corrupt completion bits were accepted")
		}
	})

	t.Run("finalized digest", func(t *testing.T) {
		fixture := newAssignmentCoverageFixture(t, 1, 1)
		state, _, err := fixture.store.Get(fixture.repository)
		if err != nil {
			t.Fatal(err)
		}
		assignmentID := fixture.plan.AssignmentPlans[0].AssignmentID
		state.Runs = []ReviewRun{{
			ID: "run", PlanID: fixture.plan.ID, CampaignID: fixture.campaignID,
			CommitSHA:         fixture.plan.CommitSHA,
			CheckpointScopes:  map[string][]FileRef{assignmentID: fixture.files},
			CheckpointDigests: map[string]string{},
		}}
		store := fixture.store
		store.loadForTest = func(string) (RepositoryState, error) { return state, nil }
		if _, err := store.CheckpointRepositoryReviewAssignment(
			t.Context(), assignmentCoverageCheckpoint(fixture, "run", 0, fixture.files),
		); !errors.Is(err, ErrConflict) {
			t.Fatalf("missing finalized digest error = %v", err)
		}
	})

	t.Run("checkpoint bits", func(t *testing.T) {
		fixture := newAssignmentCoverageFixture(t, 1, 1)
		if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
		}); err != nil {
			t.Fatal(err)
		}
		state, _, err := fixture.store.Get(fixture.repository)
		if err != nil {
			t.Fatal(err)
		}
		state.CurrentCampaign.Paths[fixture.files[0].Path] = RepositoryReviewCampaignPathCoverage{
			AssignmentBits: "!",
		}
		store := fixture.store
		store.loadForTest = func(string) (RepositoryState, error) { return state, nil }
		if _, err := store.CheckpointRepositoryReviewAssignment(
			t.Context(), assignmentCoverageCheckpoint(fixture, "run", 0, fixture.files),
		); err == nil {
			t.Fatal("corrupt checkpoint bits were accepted")
		}
	})
}

func TestRepositoryReviewAssignmentGovernanceSemanticMerge(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	first := repositoryReviewCampaignFinding(fixture.files[0], "semantic finding")
	second := first
	second.Evidence += " Additional corroborating evidence."
	state := RepositoryState{Findings: []Finding{}, Contexts: []FindingContext{}}
	accepted, err := persistRepositoryReviewCheckpointObservation(
		&state, fixture.plan, "run", fixture.catalog[0].ID,
		Observation{
			Model: "provider/review-a", ModelAlias: "review-a", Account: "review-account",
			Reviewer:   fixture.catalog[0].FocusID,
			ScopeFiles: fixture.files, RawDigest: "sha256:" + strings.Repeat("a", 64),
			Findings: []FindingCandidate{first, second},
		},
		fixture.files, repositoryAuditTestNow,
	)
	if err != nil || len(accepted) != 2 || len(state.RawFindings) != 2 ||
		len(state.DeduplicationJobs) != 2 || len(state.Findings) != 1 || state.Findings[0].Version != 2 {
		t.Fatalf("semantic merge accepted=%v findings=%#v err=%v", accepted, state.Findings, err)
	}
}

func TestRepositoryReviewAssignmentGovernanceFinalizeMergeAndTrim(t *testing.T) {
	t.Run("plan unsupported conflict", func(t *testing.T) {
		fixture := newAssignmentCoverageFixture(t, 1, 1)
		if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
		}); err != nil {
			t.Fatal(err)
		}
		state, _, err := fixture.store.Get(fixture.repository)
		if err != nil {
			t.Fatal(err)
		}
		credited, err := CreditRepositoryReviewAssignment(
			state.CurrentCampaign.Paths[fixture.files[0].Path], fixture.catalog, fixture.catalog[0].ID,
		)
		if err != nil {
			t.Fatal(err)
		}
		state.CurrentCampaign.Paths[fixture.files[0].Path] = credited
		plan := fixture.plan
		plan.UnsupportedFiles = []UnsupportedFile{{FileRef: fixture.files[0], Reason: "historical"}}
		plan.ID = planDigest(plan)
		state.ActiveReviewRun.PlanID = plan.ID
		store := fixture.store
		store.loadForTest = func(string) (RepositoryState, error) { return state, nil }
		if _, err := store.FinalizeRepositoryReviewRun(t.Context(), FinalizeRepositoryReviewRunRequest{
			Plan: plan, RunID: "run",
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("plan unsupported conflict = %v", err)
		}
	})

	t.Run("unchanged conflict", func(t *testing.T) {
		fixture := newAssignmentCoverageFixture(t, 1, 1)
		if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
		}); err != nil {
			t.Fatal(err)
		}
		state, _, err := fixture.store.Get(fixture.repository)
		if err != nil {
			t.Fatal(err)
		}
		state.CurrentCampaign.Paths[fixture.files[0].Path] = RepositoryReviewCampaignPathCoverage{Unsupported: true}
		plan := fixture.plan
		plan.UnchangedFiles = []FileRef{fixture.files[0]}
		plan.ID = planDigest(plan)
		state.ActiveReviewRun.PlanID = plan.ID
		store := fixture.store
		store.loadForTest = func(string) (RepositoryState, error) { return state, nil }
		if _, err := store.FinalizeRepositoryReviewRun(t.Context(), FinalizeRepositoryReviewRunRequest{
			Plan: plan, RunID: "run",
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("unchanged conflict = %v", err)
		}
	})

	t.Run("run trimming", func(t *testing.T) {
		fixture := newAssignmentCoverageFixture(t, 1, 1)
		if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
		}); err != nil {
			t.Fatal(err)
		}
		state, _, err := fixture.store.Get(fixture.repository)
		if err != nil {
			t.Fatal(err)
		}
		for index := 0; index < 1000; index++ {
			state.Runs = append(state.Runs, ReviewRun{ID: fmt.Sprintf("old-%04d", index)})
		}
		store := fixture.store
		store.loadForTest = func(string) (RepositoryState, error) { return state, nil }
		result, err := store.FinalizeRepositoryReviewRun(nil, FinalizeRepositoryReviewRunRequest{
			Plan: fixture.plan, RunID: "run",
		})
		if err != nil || len(result.State.Runs) != 1000 {
			t.Fatalf("trimmed finalization runs=%d err=%v", len(result.State.Runs), err)
		}
	})

	t.Run("interrupted trimming", func(t *testing.T) {
		fixture := newAssignmentCoverageFixture(t, 1, 1)
		state := RepositoryState{
			CurrentCampaign: &RepositoryReviewCampaignCoverage{
				ID: fixture.campaignID, ScopeDigest: "scope", SelectedFiles: 1,
				Paths: map[string]RepositoryReviewCampaignPathCoverage{
					fixture.files[0].Path: {Completed: true},
				},
			},
			ActiveReviewRun: &RepositoryReviewActiveRun{
				ID: "run", CampaignID: fixture.campaignID,
				Reservations: map[string]RepositoryReviewAssignmentReservation{
					fixture.catalog[0].ID: {
						AssignmentID: fixture.catalog[0].ID, Files: fixture.files,
					},
				},
			},
		}
		for index := 0; index < 1000; index++ {
			state.Runs = append(state.Runs, ReviewRun{ID: fmt.Sprintf("old-%04d", index)})
		}
		archiveInterruptedRepositoryReviewRun(&state, repositoryAuditTestNow)
		if state.ActiveReviewRun != nil || len(state.Runs) != 1000 || state.Runs[999].ReviewedFiles != 1 {
			t.Fatalf("interrupted trim = %#v", state.Runs[999])
		}
	})
}

//nolint:govet // Boundary assertions intentionally reuse err in short scopes.
func TestRepositoryReviewAssignmentGovernanceCampaignBranches(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	state, _, err := fixture.store.Get(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	coverage := cloneRepositoryReviewCampaignCoverage(*state.CurrentCampaign)
	if _, err := fixture.store.ReconcileCampaign(t.Context(), ReconcileCampaignRequest{
		Repository: fixture.repository, ExpectedReviewVersion: state.ReviewVersion,
		Coverage: coverage, SelectedScope: fixture.files,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := bindRepositoryReviewCampaignAssignmentCatalog(
		&state, NewRepositoryReviewCampaignID(), fixture.plan.CommitSHA,
		fixture.plan.InventoryHash, fixture.plan.ProfileHash,
		state.CurrentCampaign.ScopeDigest, fixture.catalog, 1,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("catalog bind conflict = %v", err)
	}

	corrupt := RepositoryState{CurrentCampaign: &RepositoryReviewCampaignCoverage{
		ID: fixture.campaignID, CommitSHA: fixture.plan.CommitSHA,
		InventoryHash: fixture.plan.InventoryHash, ProfileHash: fixture.plan.ProfileHash,
		ScopeDigest:         state.CurrentCampaign.ScopeDigest,
		RequiredAssignments: len(fixture.catalog), SelectedFiles: 1,
		Paths: map[string]RepositoryReviewCampaignPathCoverage{
			fixture.files[0].Path: {Completed: true, AssignmentBits: "!"},
		},
	}}
	if _, err := bindRepositoryReviewCampaignAssignmentCatalog(
		&corrupt, fixture.campaignID, fixture.plan.CommitSHA,
		fixture.plan.InventoryHash, fixture.plan.ProfileHash,
		state.CurrentCampaign.ScopeDigest, fixture.catalog, 1,
	); err == nil {
		t.Fatal("corrupt legacy assignment bits were accepted")
	}

	mergeCoverage := cloneRepositoryReviewCampaignCoverage(*state.CurrentCampaign)
	mergeCoverage.Paths[fixture.files[0].Path] = RepositoryReviewCampaignPathCoverage{Unsupported: true}
	if _, err := mergeRepositoryReviewCampaignPath(
		&mergeCoverage, fixture.files[0].Path,
		RepositoryReviewCampaignPathCoverage{Completed: true},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("assignment projection conflict = %v", err)
	}

	invalidCatalog := cloneRepositoryReviewCampaignCoverage(*state.CurrentCampaign)
	invalidCatalog.AssignmentCatalog[0].ID = "forged"
	if err := validateRepositoryReviewCampaignCoverage(&invalidCatalog); err == nil {
		t.Fatal("invalid campaign catalog was accepted")
	}
	nonprojected := cloneRepositoryReviewCampaignCoverage(*state.CurrentCampaign)
	credited, err := CreditRepositoryReviewAssignment(
		RepositoryReviewCampaignPathCoverage{}, fixture.catalog, fixture.catalog[0].ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	credited.Inspected = false
	credited.Completed = true
	nonprojected.Paths[fixture.files[0].Path] = credited
	if err := validateRepositoryReviewCampaignCoverage(&nonprojected); err == nil {
		t.Fatal("nonprojected assignment coverage was accepted")
	}

	invalidProgress := RepositoryReviewAssignmentProgress{}
	invalidProgress.ByFocus.SecurityTrust.Total = -1
	if validRepositoryReviewAssignmentProgress(invalidProgress) {
		t.Fatal("invalid focus progress was accepted")
	}
}

func TestRepositoryReviewAssignmentGovernanceRunMetadataValidation(t *testing.T) {
	fixture := newAssignmentCoverageFixture(t, 1, 1)
	if _, err := fixture.store.BeginRepositoryReviewRun(t.Context(), BeginRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "run", ReviewableFiles: fixture.files,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.FinalizeRepositoryReviewRun(t.Context(), FinalizeRepositoryReviewRunRequest{
		Plan: fixture.plan, RunID: "run",
	}); err != nil {
		t.Fatal(err)
	}
	baseline, _, err := fixture.store.Get(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	assignmentID := fixture.catalog[0].ID
	digest := "sha256:" + strings.Repeat("a", 64)
	for name, mutate := range map[string]func(*ReviewRun){
		"too many digests": func(run *ReviewRun) {
			run.CheckpointDigests = make(map[string]string, maxRepositoryReviewRequiredAssignments+1)
			for index := 0; index <= maxRepositoryReviewRequiredAssignments; index++ {
				run.CheckpointDigests[fmt.Sprintf("assignment-%03d", index)] = digest
			}
		},
		"scope count": func(run *ReviewRun) {
			run.CheckpointDigests = map[string]string{assignmentID: digest}
			run.CheckpointScopes = nil
		},
		"empty scope": func(run *ReviewRun) {
			run.CheckpointDigests = map[string]string{assignmentID: digest}
			run.CheckpointScopes = map[string][]FileRef{assignmentID: nil}
		},
		"invalid digest": func(run *ReviewRun) {
			run.CheckpointDigests = map[string]string{assignmentID: "bad"}
			run.CheckpointScopes = map[string][]FileRef{assignmentID: fixture.files}
		},
	} {
		t.Run(name, func(t *testing.T) {
			state := baseline
			state.Runs = append([]ReviewRun(nil), baseline.Runs...)
			mutate(&state.Runs[0])
			if err := validateState(state); err == nil {
				t.Fatal("invalid checkpoint metadata was accepted")
			}
		})
	}
}

func TestRepositoryReviewAssignmentGovernanceLegacyIssueBranches(t *testing.T) {
	t.Run("draft finding unavailable", func(t *testing.T) {
		store := NewStore(t.TempDir())
		state := RepositoryState{IssueDrafts: []IssueDraft{{
			ID: "draft", Canonical: true, State: IssueDraftPublishing, Version: 1,
			FindingIDs: []string{"missing-finding"},
		}}}
		store.loadForTest = func(string) (RepositoryState, error) { return state, nil }
		if _, _, err := store.SetIssueDraftPublication(
			"owner/issue-branch", "draft", 1, IssueDraftPosted,
			"123", "https://example.com/issues/123",
		); !errors.Is(err, ErrConflict) {
			t.Fatalf("missing draft finding error = %v", err)
		}
	})

	t.Run("missing occurrence draft", func(t *testing.T) {
		store := NewStore(t.TempDir())
		state := RepositoryState{
			RepositoryFindings: []RepositoryFinding{{
				ID: "aggregate", Version: 1, MatchState: RepositoryMatchKnown,
				ReviewFindingIDs: []string{"occurrence"},
			}},
			Findings: []Finding{{ID: "occurrence", IssueDraftID: "missing-draft"}},
		}
		store.loadForTest = func(string) (RepositoryState, error) { return state, nil }
		if _, _, err := store.UpdateRepositoryFindingIssueSnapshot(
			"owner/issue-snapshot", RepositoryIssueSnapshotUpdate{
				RepositoryFindingID: "aggregate", ExpectedVersion: 1,
				ExternalID: "123", URL: "https://example.com/issues/123",
				State: RepositoryFindingIssueOpen,
			},
		); err == nil {
			t.Fatal("invalid missing-draft state unexpectedly saved")
		}
	})
}
