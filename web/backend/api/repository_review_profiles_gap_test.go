package api

import (
	"net/http"
	"testing"

	"github.com/sipeed/picoclaw/pkg/collectionquery"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryReviewCanonicalProfileDeduplicationFieldsAndBounds(t *testing.T) {
	profile := repoaudit.RepositoryReviewProfile{
		ReviewerModel: "cheap", DeduplicationSimilarityThreshold: 91,
		DeduplicationCandidateLimit: 7,
	}
	for _, field := range []collectionquery.Field{
		"deduplicator", "deduplication_threshold", "deduplication_candidates",
	} {
		if _, ok := repositoryReviewProfileCollectionField(profile, field); !ok {
			t.Fatalf("profile field %q was unresolved", field)
		}
	}

	handler, mux, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	invalidCreate := repositoryReviewProfileCreateBody("Invalid deduplication", "cheap")
	invalidCreate["deduplication_similarity_threshold"] = 101
	response := repositoryReviewAutomationMutation(
		t, mux, http.MethodPost, "/api/repository-reviews/profiles", invalidCreate,
	)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid deduplication create status=%d body=%s", response.Code, response.Body.String())
	}
	created := createRepositoryReviewProfileForTest(t, mux, "Deduplication coverage", "cheap")
	invalidUpdate := repositoryReviewProfileBody(created)
	invalidUpdate["expected_version"] = created.Version
	invalidUpdate["deduplication_candidate_limit"] = 21
	response = repositoryReviewAutomationMutation(
		t, mux, http.MethodPatch, "/api/repository-reviews/profiles/"+created.ID, invalidUpdate,
	)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid deduplication update status=%d body=%s", response.Code, response.Body.String())
	}
}
