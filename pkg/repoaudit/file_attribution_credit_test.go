package repoaudit

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type repositoryReviewAttributionCreditFixture struct {
	assignmentCoverageFixture
	state        RepositoryState
	fence        RepositoryReviewFileAttributionCreditFence
	attributions []RepositoryReviewFileAttribution
}

func newRepositoryReviewAttributionCreditFixture(
	t *testing.T,
) repositoryReviewAttributionCreditFixture {
	t.Helper()
	base := newAssignmentCoverageFixture(t, 2, 2)
	state, found, err := base.store.Get(base.repository)
	if err != nil || !found {
		t.Fatalf("credit fixture state found=%v err=%v", found, err)
	}
	historicalProfile := "sha256:" + strings.Repeat("e", 64)
	completedAt := repositoryAuditTestNow.Add(-time.Hour)
	state.CurrentCampaign.Exact = true
	state.CurrentCampaign.RecoveryDigest = "sha256:" + strings.Repeat("f", 64)
	state.Runs = []ReviewRun{{
		ID: "legacy-run", CampaignID: base.campaignID, PlanID: "legacy-plan",
		CommitSHA: state.CurrentCampaign.CommitSHA, InventoryHash: state.CurrentCampaign.InventoryHash,
		ProfileHash: historicalProfile, ScopeDigest: state.CurrentCampaign.ScopeDigest,
		InspectedFiles: len(base.files), LegacyRecovered: true,
		UnreviewedFiles: len(base.files), RemainingFiles: len(base.files), CompletedAt: completedAt,
	}}
	if err := base.store.save(&state); err != nil {
		t.Fatal(err)
	}
	newAttribution := func(
		child int,
		focus string,
		files []FileRef,
	) RepositoryReviewFileAttribution {
		attribution, attributionErr := NewRepositoryReviewFileAttribution(
			RepositoryReviewFileAttribution{
				AutomationID: "rra_attribution_credit", RunID: "legacy-run",
				CommitSHA:     state.CurrentCampaign.CommitSHA,
				InventoryHash: state.CurrentCampaign.InventoryHash,
				ProfileHash:   historicalProfile, AssignmentID: "legacy-assignment",
				FocusID: focus, RootAgentID: "main", ReviewerIdentity: "review-a",
				Model: "provider/legacy", AcknowledgedFiles: append([]FileRef(nil), files...),
				EvidenceDigest: "sha256:" + strings.Repeat("d", 64),
				Source:         RepositoryReviewFileAttributionSourceLegacyManagedChild,
				ChildIndex:     child, Required: true, CompletedAt: completedAt,
			},
		)
		if attributionErr != nil {
			t.Fatal(attributionErr)
		}
		return attribution
	}
	return repositoryReviewAttributionCreditFixture{
		assignmentCoverageFixture: base,
		state:                     state,
		fence: RepositoryReviewFileAttributionCreditFence{
			AutomationID: "rra_attribution_credit", CampaignID: base.campaignID,
			ExpectedReviewVersion: state.ReviewVersion,
		},
		attributions: []RepositoryReviewFileAttribution{
			newAttribution(1, RepositoryReviewFocusSecurityTrust, base.files),
			newAttribution(2, RepositoryReviewFocusConcurrencyRecovery, base.files[:1]),
		},
	}
}

func TestRepositoryReviewFileAttributionCampaignCreditAtomicAndIdempotent(t *testing.T) {
	fixture := newRepositoryReviewAttributionCreditFixture(t)
	beforePaths := cloneRepositoryReviewCampaignCoverage(*fixture.state.CurrentCampaign).Paths
	preview, err := PreviewRepositoryReviewFileAttributionCredits(
		fixture.state, fixture.fence, fixture.attributions,
	)
	if err != nil {
		t.Fatal(err)
	}
	if preview.CampaignID != fixture.campaignID || len(preview.Credits) != 3 ||
		preview.EffectiveAssignmentCredits != 3 || preview.NewAssignmentCredits != 3 ||
		preview.EffectiveInspectedFiles != 2 || preview.NewInspectedFiles != 2 ||
		preview.ProjectedCompletedAssignments != 3 || preview.ProjectedPendingAssignments != 5 ||
		preview.ProjectedInspectedFiles != 2 || preview.ProjectedCompletedFiles != 0 {
		t.Fatalf("credit preview = %#v", preview)
	}
	if !reflect.DeepEqual(fixture.state.CurrentCampaign.Paths, beforePaths) {
		t.Fatal("credit preview mutated caller state")
	}
	for index := 1; index < len(preview.Credits); index++ {
		left, right := preview.Credits[index-1], preview.Credits[index]
		if left.File.Path > right.File.Path ||
			left.File.Path == right.File.Path && left.AssignmentID > right.AssignmentID {
			t.Fatalf("credit preview is not deterministic: %#v", preview.Credits)
		}
	}

	request := MergeRepositoryReviewFileAttributionsRequest{
		Repository: fixture.repository, ExpectedVersion: fixture.state.Version,
		Attributions: fixture.attributions, CampaignCredit: &fixture.fence,
	}
	merged, err := fixture.store.MergeRepositoryReviewFileAttributions(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Version != fixture.state.Version+1 ||
		merged.ReviewVersion != fixture.state.ReviewVersion+1 ||
		len(merged.FileAttributions) != 2 {
		t.Fatalf("credited merge = %#v", merged)
	}
	progress := CurrentCampaignAssignmentProgress(merged, fixture.campaignID)
	metrics := CurrentCampaignMetrics(merged, fixture.campaignID, nil, time.Time{})
	if progress.Total != 8 || progress.Completed != 3 || progress.Pending != 5 ||
		metrics.InspectedFiles != 2 || metrics.CompletedFiles != 0 || metrics.RemainingFiles != 2 {
		t.Fatalf("credited progress=%#v metrics=%#v", progress, metrics)
	}

	// Both repository and review versions in the original request are stale;
	// an exact semantic replay still succeeds without another write.
	replayed, err := fixture.store.MergeRepositoryReviewFileAttributions(t.Context(), request)
	if err != nil || replayed.Version != merged.Version ||
		replayed.ReviewVersion != merged.ReviewVersion ||
		!reflect.DeepEqual(replayed.CurrentCampaign, merged.CurrentCampaign) {
		t.Fatalf("credit replay = %#v err=%v", replayed, err)
	}

	plan, err := fixture.store.PlanAssignmentsForCampaign(
		t.Context(), fixture.repository, merged.CurrentCampaign.CommitSHA,
		merged.CurrentCampaign.InventoryHash, merged.CurrentCampaign.ProfileHash,
		fixture.campaignID, fixture.catalog, fixture.files, false, 2, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	missing := 0
	for _, assignment := range plan.AssignmentPlans {
		missing += len(assignment.Files)
	}
	if missing != 5 {
		t.Fatalf("missing assignment pairs=%d plans=%#v", missing, plan.AssignmentPlans)
	}
}

func TestRepositoryReviewFileAttributionCampaignCreditStoredOnlyAndFullCompletion(t *testing.T) {
	fixture := newRepositoryReviewAttributionCreditFixture(t)
	all := append([]RepositoryReviewFileAttribution(nil), fixture.attributions...)
	for index, focus := range []string{
		RepositoryReviewFocusCorrectnessState,
		RepositoryReviewFocusIntegrationValidation,
	} {
		input := fixture.attributions[0]
		input.ID = ""
		input.ChildIndex = index + 3
		input.FocusID = focus
		input.AcknowledgedFiles = append([]FileRef(nil), fixture.files[:1]...)
		attribution, err := NewRepositoryReviewFileAttribution(input)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, attribution)
	}
	stored, err := fixture.store.MergeRepositoryReviewFileAttributions(
		t.Context(), MergeRepositoryReviewFileAttributionsRequest{
			Repository: fixture.repository, ExpectedVersion: fixture.state.Version,
			Attributions: all,
		},
	)
	if err != nil || stored.ReviewVersion != fixture.state.ReviewVersion {
		t.Fatalf("stored attribution evidence = %#v err=%v", stored, err)
	}
	fence := fixture.fence
	fence.ExpectedReviewVersion = stored.ReviewVersion
	credited, err := fixture.store.MergeRepositoryReviewFileAttributions(
		t.Context(), MergeRepositoryReviewFileAttributionsRequest{
			Repository: fixture.repository, ExpectedVersion: stored.Version,
			CampaignCredit: &fence,
		},
	)
	if err != nil || credited.Version != stored.Version+1 ||
		credited.ReviewVersion != stored.ReviewVersion+1 {
		t.Fatalf("stored-only credit = %#v err=%v", credited, err)
	}
	progress := CurrentCampaignAssignmentProgress(credited, fixture.campaignID)
	metrics := CurrentCampaignMetrics(credited, fixture.campaignID, nil, time.Time{})
	if progress.Completed != 5 || progress.Pending != 3 ||
		metrics.InspectedFiles != 2 || metrics.CompletedFiles != 1 || metrics.RemainingFiles != 1 {
		t.Fatalf("full credit progress=%#v metrics=%#v", progress, metrics)
	}
}

func TestRepositoryReviewFileAttributionCampaignCreditCAS(t *testing.T) {
	for _, test := range []struct {
		name        string
		mutate      func(*MergeRepositoryReviewFileAttributionsRequest)
		wantInvalid bool
	}{
		{name: "stale repository", mutate: func(request *MergeRepositoryReviewFileAttributionsRequest) {
			request.ExpectedVersion--
		}},
		{name: "stale review", mutate: func(request *MergeRepositoryReviewFileAttributionsRequest) {
			request.CampaignCredit.ExpectedReviewVersion--
		}},
		{name: "invalid automation", mutate: func(request *MergeRepositoryReviewFileAttributionsRequest) {
			request.CampaignCredit.AutomationID = "invalid"
		}, wantInvalid: true},
		{name: "invalid campaign", mutate: func(request *MergeRepositoryReviewFileAttributionsRequest) {
			request.CampaignCredit.CampaignID = "invalid"
		}, wantInvalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRepositoryReviewAttributionCreditFixture(t)
			fence := fixture.fence
			request := MergeRepositoryReviewFileAttributionsRequest{
				Repository: fixture.repository, ExpectedVersion: fixture.state.Version,
				Attributions: fixture.attributions, CampaignCredit: &fence,
			}
			test.mutate(&request)
			_, err := fixture.store.MergeRepositoryReviewFileAttributions(t.Context(), request)
			if test.wantInvalid && !errors.Is(err, ErrInvalidPlan) ||
				!test.wantInvalid && !errors.Is(err, ErrConflict) {
				t.Fatalf("credit CAS error=%v", err)
			}
			state, _, loadErr := fixture.store.Get(fixture.repository)
			if loadErr != nil || state.Version != fixture.state.Version ||
				state.ReviewVersion != fixture.state.ReviewVersion || len(state.FileAttributions) != 0 {
				t.Fatalf("failed CAS mutated state=%#v err=%v", state, loadErr)
			}
		})
	}
}

func TestRepositoryReviewFileAttributionCampaignCreditEvidenceGates(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*testing.T, *repositoryReviewAttributionCreditFixture)
		allEvidence bool
		wantCredits int
		wantError   bool
	}{
		{name: "valid profile drift", wantCredits: 2},
		{name: "wrong automation", mutate: func(t *testing.T, fixture *repositoryReviewAttributionCreditFixture) {
			fixture.attributions[0].AutomationID = "rra_other_automation"
		}},
		{name: "live source", mutate: func(t *testing.T, fixture *repositoryReviewAttributionCreditFixture) {
			fixture.attributions[0].Source = RepositoryReviewFileAttributionSourceLiveCheckpoint
			fixture.attributions[0].ModelAlias = "review-a"
			fixture.attributions[0].Account = "account-a"
		}},
		{name: "optional evidence", mutate: func(t *testing.T, fixture *repositoryReviewAttributionCreditFixture) {
			fixture.attributions[0].Required = false
		}},
		{name: "wrong root", mutate: func(t *testing.T, fixture *repositoryReviewAttributionCreditFixture) {
			fixture.attributions[0].RootAgentID = "reviewer"
		}},
		{name: "missing run", mutate: func(t *testing.T, fixture *repositoryReviewAttributionCreditFixture) {
			fixture.attributions[0].RunID = "pruned-run"
		}},
		{name: "other campaign run", mutate: func(t *testing.T, fixture *repositoryReviewAttributionCreditFixture) {
			fixture.state.Runs[0].CampaignID = NewRepositoryReviewCampaignID()
		}},
		{name: "nonlegacy run", mutate: func(t *testing.T, fixture *repositoryReviewAttributionCreditFixture) {
			fixture.state.Runs[0].LegacyRecovered = false
		}, wantError: true},
		{name: "duplicate run", mutate: func(t *testing.T, fixture *repositoryReviewAttributionCreditFixture) {
			fixture.state.Runs = append(fixture.state.Runs, fixture.state.Runs[0])
		}, wantError: true},
		{name: "commit mismatch", mutate: func(t *testing.T, fixture *repositoryReviewAttributionCreditFixture) {
			fixture.attributions[0].CommitSHA = strings.Repeat("b", 40)
		}, wantError: true},
		{name: "inventory mismatch", mutate: func(t *testing.T, fixture *repositoryReviewAttributionCreditFixture) {
			fixture.attributions[0].InventoryHash = "other-inventory"
		}, wantError: true},
		{
			name: "historical profile mismatch",
			mutate: func(t *testing.T, fixture *repositoryReviewAttributionCreditFixture) {
				fixture.attributions[0].ProfileHash = "sha256:" + strings.Repeat("a", 64)
			},
			wantError: true,
		},
		{
			name: "completion time mismatch",
			mutate: func(t *testing.T, fixture *repositoryReviewAttributionCreditFixture) {
				fixture.attributions[0].CompletedAt = fixture.attributions[0].CompletedAt.Add(time.Second)
			},
			wantError: true,
		},
		{name: "reviewer mismatch", mutate: func(t *testing.T, fixture *repositoryReviewAttributionCreditFixture) {
			fixture.attributions[0].ReviewerIdentity = "review-b"
		}},
		{
			name: "focus absent from catalog",
			mutate: func(t *testing.T, fixture *repositoryReviewAttributionCreditFixture) {
				catalog := make([]RepositoryReviewAssignment, 0, 4)
				for index := range 4 {
					assignment, err := NewRepositoryReviewAssignment(
						RepositoryReviewFocusCorrectnessState, fmt.Sprintf("other-%d", index),
						"prompt-v1", fixture.state.CurrentCampaign.ProfileHash, true,
					)
					if err != nil {
						t.Fatal(err)
					}
					catalog = append(catalog, assignment)
				}
				fixture.state.CurrentCampaign.AssignmentCatalog = catalog
			},
		},
		{
			name: "inexact campaign",
			mutate: func(t *testing.T, fixture *repositoryReviewAttributionCreditFixture) {
				fixture.state.CurrentCampaign.Exact = false
			},
			wantError: true,
		},
		{
			name: "missing recovery digest",
			mutate: func(t *testing.T, fixture *repositoryReviewAttributionCreditFixture) {
				fixture.state.CurrentCampaign.RecoveryDigest = ""
			},
			wantError: true,
		},
		{
			name: "unsupported path",
			mutate: func(t *testing.T, fixture *repositoryReviewAttributionCreditFixture) {
				fixture.state.CurrentCampaign.Paths[fixture.files[0].Path] = RepositoryReviewCampaignPathCoverage{
					Unsupported: true,
				}
			},
			wantError: true,
		},
		{
			name:        "conflicting file refs",
			allEvidence: true,
			mutate: func(t *testing.T, fixture *repositoryReviewAttributionCreditFixture) {
				fixture.attributions[1].AcknowledgedFiles[0].BlobSHA = strings.Repeat("9", 40)
			},
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRepositoryReviewAttributionCreditFixture(t)
			if test.mutate != nil {
				test.mutate(t, &fixture)
			}
			count := 1
			if test.allEvidence {
				count = len(fixture.attributions)
			}
			for index := range count {
				fixture.attributions[index].ID = ""
				normalized, err := NewRepositoryReviewFileAttribution(fixture.attributions[index])
				if err != nil {
					t.Fatalf("normalize mutated attribution: %v", err)
				}
				fixture.attributions[index] = normalized
			}
			preview, err := PreviewRepositoryReviewFileAttributionCredits(
				fixture.state, fixture.fence, fixture.attributions[:count],
			)
			if test.wantError {
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("evidence gate error=%v preview=%#v", err, preview)
				}
				return
			}
			if err != nil || len(preview.Credits) != test.wantCredits {
				t.Fatalf("evidence gate credits=%d want=%d err=%v preview=%#v",
					len(preview.Credits), test.wantCredits, err, preview)
			}
		})
	}
}
