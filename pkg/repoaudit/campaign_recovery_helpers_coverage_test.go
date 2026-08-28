package repoaudit

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func repositoryReviewLegacyReconcileFixtureForCoverage(
	t *testing.T,
) (Store, RepositoryState, ReconcileCampaignRequest) {
	t.Helper()
	store, state := repositoryReviewCoverageStore(t, "owner/reconcile-edges")
	file := repositoryAuditTestFile("edge.go", "a", 1)
	plan, err := store.PlanWithProfileLimitAuthoritative(
		t.Context(), state.Repository, repositoryReviewCampaignTestCommit,
		repositoryReviewCampaignTestInventory, repositoryReviewCampaignTestProfile,
		[]FileRef{file}, false, 1, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := store.Record(t.Context(), RecordRequest{
		Plan: plan, RunID: "wr_edge", CompletedAt: repositoryAuditTestNow,
		CompletedFiles: []FileRef{},
		Observations: []Observation{{
			Model: "review", ScopeFiles: []FileRef{file},
			Findings: []FindingCandidate{repositoryReviewCampaignFinding(file, "Edge")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	state = recorded.State
	campaignID, begun := beginRepositoryReviewCampaignForTest(
		t, store, state.Repository, false,
	)
	state = begun
	coverage := RepositoryReviewCampaignCoverage{
		ID: campaignID, CommitSHA: plan.CommitSHA, InventoryHash: plan.InventoryHash,
		ProfileHash:         plan.ProfileHash,
		ScopeDigest:         repositoryReviewCampaignTestScopeDigest(t, file),
		RequiredAssignments: 1, SelectedFiles: 1, Exact: true,
		Paths: map[string]RepositoryReviewCampaignPathCoverage{file.Path: {Inspected: true}},
	}
	request := ReconcileCampaignRequest{
		Repository: state.Repository, ExpectedReviewVersion: begun.ReviewVersion,
		Coverage: coverage, SelectedScope: []FileRef{file},
		Runs: []RepositoryReviewCampaignRunRecovery{{
			ID: recorded.Run.ID, Plan: plan, InspectedFiles: 1, LegacyRecovered: true,
		}},
		ContextIDs: append([]string(nil), recorded.Run.FindingIDs...),
	}
	request.ContextIDs = nil
	for _, contextRecord := range state.Contexts {
		if contextRecord.RunID == recorded.Run.ID {
			request.ContextIDs = append(request.ContextIDs, contextRecord.ID)
		}
	}
	request.FindingIDs = append([]string(nil), recorded.Run.FindingIDs...)
	return store, state, request
}

func TestRepositoryReviewRecoveryExportedHelpersCoverCanonicalBoundaries(t *testing.T) {
	first := FileRef{Path: "a.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 1, Category: "code", Mode: "100644"}
	second := FileRef{Path: "b.go", BlobSHA: strings.Repeat("b", 40), SizeBytes: 2, Category: "code", Mode: "100644"}
	canonical, err := CanonicalRepositoryReviewCampaignScope([]FileRef{second, first})
	if err != nil || !reflect.DeepEqual(canonical, []FileRef{first, second}) {
		t.Fatalf("canonical scope=%#v err=%v", canonical, err)
	}
	left, err := RepositoryReviewCampaignScopeDigest([]FileRef{second, first})
	if err != nil {
		t.Fatal(err)
	}
	right, err := RepositoryReviewCampaignScopeDigest([]FileRef{first, second})
	if err != nil || left == "" || left != right {
		t.Fatalf("scope digests left=%q right=%q err=%v", left, right, err)
	}
	for name, files := range map[string][]FileRef{
		"duplicate": {first, first},
		"noncanonical": {{
			Path: " a.go", BlobSHA: first.BlobSHA, SizeBytes: first.SizeBytes,
			Category: first.Category, Mode: first.Mode,
		}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CanonicalRepositoryReviewCampaignScope(files); err == nil {
				t.Fatal("invalid exact scope was accepted")
			}
			if _, err := RepositoryReviewCampaignScopeDigest(files); err == nil {
				t.Fatal("invalid exact scope digest was accepted")
			}
		})
	}
}

func TestRepositoryReviewRecoveryIdentityAndCandidateHelpers(t *testing.T) {
	line := 7
	raw := FindingCandidate{
		Severity: " HIGH ", Title: " title ", Symbol: " Save ", File: " pkg/save.go ",
		Line: &line, Message: " message ", Evidence: " evidence ", Impact: " impact ",
		Validation: Validation{Status: " CONFIRMED ", Summary: " summary "},
		MatchHints: MatchHints{
			Component: " component ", RelatedSymbols: []string{" Save ", "Save"},
		},
		FixEffort: FixEffort{
			Quick:   FixEffortEstimate{Class: " TINY ", Rationale: " quick "},
			Quality: FixEffortEstimate{Class: " SMALL ", Rationale: " quality "},
		},
	}
	candidate := NormalizeRepositoryReviewFindingCandidate(raw)
	if candidate.Severity != "high" || candidate.Title != "title" || candidate.Symbol != "Save" ||
		candidate.File != "pkg/save.go" || candidate.Validation.Status != "confirmed" ||
		candidate.MatchHints.Component != "component" ||
		!reflect.DeepEqual(candidate.MatchHints.RelatedSymbols, []string{"Save", "Save"}) ||
		candidate.FixEffort.Quick.Class != "tiny" || candidate.FixEffort.Quality.Rationale != "quality" {
		t.Fatalf("normalized candidate=%#v", candidate)
	}

	file := FileRef{Path: candidate.File, BlobSHA: strings.Repeat("a", 40), SizeBytes: 10}
	contextRecord := FindingContext{
		Repository: "owner/repo", CommitSHA: strings.Repeat("b", 40),
		InventoryHash: "inventory", ProfileHash: "profile", RunID: "wr_legacy",
		Model: "model", Reviewer: "reviewer", Files: []FileRef{file}, RawDigest: "sha256:raw",
	}
	contextRecord.ID = stableID("rctx_", contextBindingDigest(contextRecord))
	if !ValidateRepositoryReviewLegacyContextIdentity(contextRecord) {
		t.Fatal("valid legacy context identity was rejected")
	}
	tagged := contextRecord
	tagged.CampaignID = NewRepositoryReviewCampaignID()
	if !ValidateRepositoryReviewLegacyContextIdentity(tagged) {
		t.Fatal("campaign tag changed immutable legacy context identity")
	}
	for _, invalid := range []FindingContext{{}, func() FindingContext {
		value := contextRecord
		value.ID = "wrong"
		return value
	}()} {
		if ValidateRepositoryReviewLegacyContextIdentity(invalid) {
			t.Fatalf("invalid legacy context identity accepted: %#v", invalid)
		}
	}

	outside := candidate
	outside.File = "outside.go"
	if ValidateRepositoryReviewLegacyFindingIdentity(Finding{}, contextRecord, outside) {
		t.Fatal("out-of-scope legacy finding identity was accepted")
	}
}

func TestRepositoryReviewLegacyReconcileRejectsPlanAndScopeDriftCoverage(t *testing.T) {
	for name, mutate := range map[string]func(*ReconcileCampaignRequest){
		"plan repository": func(request *ReconcileCampaignRequest) {
			request.Runs[0].Plan.Repository = "owner/other"
			request.Runs[0].Plan.ID = planDigest(request.Runs[0].Plan)
		},
		"outside selected scope": func(request *ReconcileCampaignRequest) {
			extra := repositoryAuditTestFile("outside.go", "b", 2)
			request.Runs[0].Plan.DeferredFiles = []FileRef{extra}
			request.Runs[0].Plan.ID = planDigest(request.Runs[0].Plan)
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, _, request := repositoryReviewLegacyReconcileFixtureForCoverage(t)
			mutate(&request)
			if _, err := store.ReconcileCampaign(
				context.Background(), request,
			); !errors.Is(err, ErrInvalidPlan) {
				t.Fatalf("drift error=%v", err)
			}
		})
	}

	t.Run("context profile", func(t *testing.T) {
		store, state, request := repositoryReviewLegacyReconcileFixtureForCoverage(t)
		state.Contexts[0].ProfileHash = "other-profile"
		if err := store.save(&state); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReconcileCampaign(
			context.Background(), request,
		); !errors.Is(err, ErrConflict) {
			t.Fatalf("context profile error=%v", err)
		}
	})
}
