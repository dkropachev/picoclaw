package repoaudit

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRepositoryReviewCoverageCushionSeams(t *testing.T) {
	if _, err := decodeLegacyAutomationPriceMetadata([]byte(`{`)); err == nil {
		t.Fatal("malformed legacy automation metadata was accepted")
	}
	if err := validateEncodedAutomationSize(make([]byte, maxAutomationFileBytes+1)); err == nil {
		t.Fatal("oversized encoded automation was accepted")
	}
	allowed, err := repositoryReviewGuardBooleanResult(knownGuardNumber(1))
	if allowed || !errors.Is(err, ErrInvalidRepositoryReviewGuardExpression) {
		t.Fatalf("numeric guard result=(%v, %v)", allowed, err)
	}
}

func TestRepositoryReviewCoverageCushionFindingProvenance(t *testing.T) {
	candidate := FindingCandidate{
		Severity: "high", Title: "Lost update", File: "store.go",
		Evidence: "Concurrent writes share a stale version.", Impact: "Data is lost.",
		Validation: Validation{Status: "confirmed", Summary: "Reproduced."},
	}
	withoutProvenance := findingObservationFrom(candidate, "context", "model")
	withLegacyReviewer := findingObservationFrom(candidate, "context", "model", "reviewer")
	if withoutProvenance.Reviewer != "" || withLegacyReviewer.Reviewer != "reviewer" {
		t.Fatalf(
			"observation provenance without=%#v legacy=%#v",
			withoutProvenance,
			withLegacyReviewer,
		)
	}
	state := RepositoryState{
		ActiveReviewRun: &RepositoryReviewActiveRun{
			ID: "run", CampaignID: "campaign", Reservations: map[string]RepositoryReviewAssignmentReservation{},
		},
		Contexts: []FindingContext{{
			RunID: "run", CampaignID: "campaign", Model: "provider/model",
		}},
	}
	archiveInterruptedRepositoryReviewRun(&state, time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC))
	if len(state.Runs) != 1 || len(state.Runs[0].Models) != 1 ||
		state.Runs[0].Models[0] != "provider/model" {
		t.Fatalf("interrupted run model fallback=%#v", state.Runs)
	}
}

func TestRepositoryReviewCoverageCushionRawCheckpointConflicts(t *testing.T) {
	now := time.Date(2026, 8, 31, 3, 30, 0, 0, time.UTC)
	file := FileRef{
		Path: "pkg/store.go", BlobSHA: strings.Repeat("a", 40), SizeBytes: 64,
		Category: "code", Mode: "100644",
	}
	plan := Plan{
		CampaignID: "rrc_coverage", Repository: "owner/repo",
		CommitSHA: strings.Repeat("b", 40),
	}
	candidate := FindingCandidate{
		Severity: "high", Title: "Lost update", File: file.Path,
		Evidence: "Concurrent writes share a stale version.", Impact: "Data is lost.",
		Validation: Validation{Status: "confirmed", Summary: "Reproduced."},
	}
	invalid := Observation{Model: "provider/model"}
	if err := persistRawRepositoryReviewCheckpointFinding(
		&RepositoryState{}, "rrw_invalid", "bucket", plan, "run", "assignment",
		"context", invalid, file, candidate, now,
	); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("invalid raw provenance error=%v", err)
	}

	observation := Observation{
		Model: "provider/model", ModelAlias: "reviewer", Account: "account",
		Reviewer: "child",
	}
	state := RepositoryState{}
	if err := persistRawRepositoryReviewCheckpointFinding(
		&state, "rrw_coverage", "bucket", plan, "run", "assignment",
		"context", observation, file, candidate, now,
	); err != nil {
		t.Fatal(err)
	}
	mismatched := state
	mismatched.RawFindings = append([]RawReviewFinding(nil), state.RawFindings...)
	changed := candidate
	changed.Title = "Different diagnosis"
	if err := persistRawRepositoryReviewCheckpointFinding(
		&mismatched, "rrw_coverage", "bucket", plan, "run", "assignment",
		"context", observation, file, changed, now,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched raw replay error=%v", err)
	}

	state.DeduplicationJobs = nil
	if err := persistRawRepositoryReviewCheckpointFinding(
		&state, "rrw_coverage", "bucket", plan, "run", "assignment",
		"context", observation, file, candidate, now,
	); err == nil || !strings.Contains(err.Error(), "missing its deduplication job") {
		t.Fatalf("missing raw job error=%v", err)
	}
}
