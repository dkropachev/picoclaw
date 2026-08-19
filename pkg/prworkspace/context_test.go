package prworkspace

import (
	"testing"
	"time"
)

func TestSharedContextIsCurrentFencedAndAudienceProjected(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	charter := Charter{
		ID: "pcr_11111111111111111111111111111111", Type: PRTypeFix,
		Goal: "fix retry", HeadSHA: "head-current", Confirmed: true, CreatedAt: now,
	}
	currentStage := StageRun{
		ID: "psr_11111111111111111111111111111111", Stage: "review", State: ExecutionSucceeded,
		CharterID: charter.ID, HeadSHA: charter.HeadSHA, Attempt: 1, StartedAt: now,
		Evidence: &StageEvidence{
			Stage: "review", RunID: "psr_11111111111111111111111111111111",
			Coverage: Coverage{ReviewedAreas: []string{"retry"}}, CreatedAt: now,
		},
	}
	staleStage := currentStage
	staleStage.ID, staleStage.HeadSHA, staleStage.Evidence = "psr_22222222222222222222222222222222", "head-old", nil
	currentFinding := Finding{
		ID: "pfn_11111111111111111111111111111111", Fingerprint: "sha256:current",
		Origin: FindingOriginReview, OriginRunID: currentStage.ID, Disposition: FindingDeferred,
	}
	staleFinding := currentFinding
	staleFinding.ID, staleFinding.Fingerprint, staleFinding.OriginRunID = "pfn_22222222222222222222222222222222", "sha256:stale", staleStage.ID
	unboundFinding := currentFinding
	unboundFinding.ID, unboundFinding.Fingerprint, unboundFinding.OriginRunID = "pfn_33333333333333333333333333333333", "sha256:unbound", ""

	aggregate := Aggregate{
		Workspace: Workspace{
			ID: "prw_11111111111111111111111111111111", RepositoryID: "repo-1",
			ActiveCharterID: charter.ID,
		},
		ProviderSnapshot: ProviderSnapshot{RepositoryID: "repo-1", HeadSHA: charter.HeadSHA},
		Charters:         []Charter{charter}, StageRuns: []StageRun{currentStage, staleStage},
		Findings: []Finding{currentFinding, staleFinding, unboundFinding},
		Messages: []Message{
			{
				ID:        "review",
				Stage:     "review",
				Content:   "review guidance",
				CharterID: charter.ID,
				HeadSHA:   charter.HeadSHA,
			},
			{
				ID:        "impl",
				Stage:     "implementation",
				Content:   "implementation guidance",
				CharterID: charter.ID,
				HeadSHA:   charter.HeadSHA,
			},
			{ID: "both", Stage: "both", Content: "shared guidance", CharterID: charter.ID, HeadSHA: charter.HeadSHA},
			{ID: "stale", Stage: "both", Content: "stale guidance", CharterID: charter.ID, HeadSHA: "head-old"},
			{ID: "unbound", Stage: "both", Content: "unbound guidance", CharterID: charter.ID},
		},
		Corrections: []Correction{
			{ID: "workspace", Applicability: CorrectionReviewAndImpl, HeadSHA: charter.HeadSHA},
			{ID: "review", Applicability: CorrectionReviewOnly, CharterID: charter.ID, HeadSHA: charter.HeadSHA},
			{ID: "impl", Applicability: CorrectionImplementationOnly, CharterID: charter.ID, HeadSHA: charter.HeadSHA},
			{ID: "both", Applicability: CorrectionReviewAndImpl, CharterID: charter.ID, HeadSHA: charter.HeadSHA},
			{ID: "old", Applicability: CorrectionReviewAndImpl, CharterID: charter.ID, HeadSHA: charter.HeadSHA},
			{
				ID:            "replacement",
				Applicability: CorrectionReviewAndImpl,
				CharterID:     charter.ID,
				HeadSHA:       charter.HeadSHA,
				SupersedesID:  "old",
			},
			{ID: "stale", Applicability: CorrectionReviewAndImpl, CharterID: charter.ID, HeadSHA: "head-old"},
			{ID: "unbound", Applicability: CorrectionReviewAndImpl, CharterID: charter.ID},
		},
		RepositoryLessons: []RepositoryLesson{
			{
				ID:            "review",
				RepositoryID:  "repo-1",
				PRType:        PRTypeFix,
				Applicability: CorrectionReviewOnly,
				Active:        true,
			},
			{
				ID:            "impl",
				RepositoryID:  "repo-1",
				PRType:        PRTypeFix,
				Applicability: CorrectionImplementationOnly,
				Active:        true,
			},
			{
				ID:            "both",
				RepositoryID:  "repo-1",
				PRType:        PRTypeFix,
				Applicability: CorrectionReviewAndImpl,
				Active:        true,
			},
			{
				ID:            "other-repo",
				RepositoryID:  "repo-2",
				PRType:        PRTypeFix,
				Applicability: CorrectionReviewAndImpl,
				Active:        true,
			},
			{
				ID:            "other-type",
				RepositoryID:  "repo-1",
				PRType:        PRTypeFeature,
				Applicability: CorrectionReviewAndImpl,
				Active:        true,
			},
			{ID: "revoked", RepositoryID: "repo-1", PRType: PRTypeFix, Applicability: CorrectionReviewAndImpl},
		},
		DeferredGroups: []DeferredGroup{
			{ID: "pdg_11111111111111111111111111111111", FindingIDs: []string{currentFinding.ID}},
			{ID: "pdg_22222222222222222222222222222222", FindingIDs: []string{staleFinding.ID}},
		},
	}

	review := reviewContextBundle(aggregate)
	implementation := implementationContextBundle(aggregate)
	if idsOfFindings(review.Findings) != currentFinding.ID ||
		idsOfFindings(implementation.Findings) != currentFinding.ID {
		t.Fatalf(
			"current findings review=%q implementation=%q",
			idsOfFindings(review.Findings),
			idsOfFindings(implementation.Findings),
		)
	}
	if idsOfMessages(review.Messages) != "review,both" || idsOfMessages(implementation.Messages) != "impl,both" {
		t.Fatalf(
			"messages review=%q implementation=%q",
			idsOfMessages(review.Messages),
			idsOfMessages(implementation.Messages),
		)
	}
	if idsOfCorrections(review.Corrections) != "workspace,review,both,replacement" ||
		idsOfCorrections(implementation.Corrections) != "workspace,impl,both,replacement" {
		t.Fatalf(
			"corrections review=%q implementation=%q",
			idsOfCorrections(review.Corrections),
			idsOfCorrections(implementation.Corrections),
		)
	}
	if idsOfLessons(review.RepositoryLessons) != "review,both" ||
		idsOfLessons(implementation.RepositoryLessons) != "impl,both" {
		t.Fatalf(
			"lessons review=%q implementation=%q",
			idsOfLessons(review.RepositoryLessons),
			idsOfLessons(implementation.RepositoryLessons),
		)
	}
	if len(review.Deferred) != 1 || review.Deferred[0].FindingIDs[0] != currentFinding.ID ||
		len(review.PriorEvidence) != 1 {
		t.Fatalf("deferred/evidence = %#v / %#v", review.Deferred, review.PriorEvidence)
	}
}

func idsOfFindings(values []Finding) string {
	return joinContextIDs(values, func(value Finding) string { return value.ID })
}

func idsOfMessages(values []Message) string {
	return joinContextIDs(values, func(value Message) string { return value.ID })
}

func idsOfCorrections(values []Correction) string {
	return joinContextIDs(values, func(value Correction) string { return value.ID })
}

func idsOfLessons(values []RepositoryLesson) string {
	return joinContextIDs(values, func(value RepositoryLesson) string { return value.ID })
}

func joinContextIDs[T any](values []T, id func(T) string) string {
	result := ""
	for _, value := range values {
		if result != "" {
			result += ","
		}
		result += id(value)
	}
	return result
}

func TestCoverageMergeRemovesAreasLaterReviewed(t *testing.T) {
	coverage := mergeCoverage([]Coverage{
		{UnreviewedAreas: []string{"retry", "timeouts"}},
		{ReviewedAreas: []string{"retry"}},
	})
	if len(coverage.ReviewedAreas) != 1 || coverage.ReviewedAreas[0] != "retry" ||
		len(coverage.UnreviewedAreas) != 1 || coverage.UnreviewedAreas[0] != "timeouts" {
		t.Fatalf("coverage = %#v", coverage)
	}
}
