package repoaudit

import (
	"context"
	"fmt"
)

func repositoryReviewDeduplicationSnapshotForTest() *RepositoryReviewDeduplicationSnapshot {
	return &RepositoryReviewDeduplicationSnapshot{
		ReviewerModel: "reviewer", DeduplicationModel: "reviewer",
		SimilarityThreshold: DeduplicationDefaultThreshold,
		CandidateLimit:      DeduplicationDefaultCandidateLimit,
	}
}

func (s Store) planAssignmentsForCampaignCountForTest(
	ctx context.Context,
	repository, commitSHA, inventoryHash, profileHash, campaignID string,
	requiredAssignments int,
	files []FileRef,
	force bool,
	maximumPending int,
	authoritative bool,
) (Plan, error) {
	catalog, err := repositoryReviewAssignmentCatalogCountForTest(
		profileHash, requiredAssignments, 0,
	)
	if err != nil {
		return Plan{}, err
	}
	return s.PlanAssignmentsForCampaign(
		ctx, repository, commitSHA, inventoryHash, profileHash, campaignID,
		catalog, files, force, maximumPending, authoritative,
	)
}

func repositoryReviewAssignmentCatalogCountForTest(
	profileHash string,
	requiredAssignments int,
	optionalAssignments int,
) ([]RepositoryReviewAssignment, error) {
	focuses := RepositoryReviewFocusIDs()
	if requiredAssignments < 1 || optionalAssignments < 0 ||
		requiredAssignments+optionalAssignments > len(focuses) {
		return nil, ErrInvalidPlan
	}
	catalog := make(
		[]RepositoryReviewAssignment, 0, requiredAssignments+optionalAssignments,
	)
	for index := range requiredAssignments + optionalAssignments {
		assignment, err := NewRepositoryReviewAssignment(
			focuses[index], fmt.Sprintf("reviewer-%d", index), "test-prompt-v1",
			profileHash, index < requiredAssignments,
		)
		if err != nil {
			return nil, err
		}
		catalog = append(catalog, assignment)
	}
	return catalog, nil
}
