package workflows

import (
	"errors"
	"strings"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

const (
	maxRepositoryReviewManagedAssignments = 128
	// RepositoryReviewRequiredAssignmentsPerReviewer is the fixed built-in
	// correctness/challenge/corroboration/validation task cohort.
	RepositoryReviewRequiredAssignmentsPerReviewer = 4
)

// RepositoryReviewFocus defines the stable ID and trusted task text for one
// built-in assignment. IDs are persistence identity; task wording may only
// change together with the prompt revision.
type RepositoryReviewFocus struct {
	ID   string
	Task string
}

var repositoryBugFinderFocuses = []RepositoryReviewFocus{
	{ID: repoaudit.RepositoryReviewFocusCorrectnessState, Task: "Trace correctness and state invariants."},
	{ID: repoaudit.RepositoryReviewFocusSecurityTrust, Task: "Challenge security and trust boundaries."},
	{
		ID:   repoaudit.RepositoryReviewFocusConcurrencyRecovery,
		Task: "Challenge concurrency, cancellation, retries, and recovery.",
	},
	{
		ID:   repoaudit.RepositoryReviewFocusIntegrationValidation,
		Task: "Challenge integration contracts and validation gaps.",
	},
}

func RepositoryBugFinderFocuses() []RepositoryReviewFocus {
	return append([]RepositoryReviewFocus(nil), repositoryBugFinderFocuses...)
}

// RepositoryBugFinderAssignmentCatalog freezes focus and reviewer identity for
// one resolved profile. When the default fallback chain is enabled it is the
// required reviewer and explicit aliases retain their existing optional role.
func RepositoryBugFinderAssignmentCatalog(
	reviewerModels []string,
	includeDefaultReviewer bool,
	promptRevision string,
	profileHash string,
) ([]repoaudit.RepositoryReviewAssignment, error) {
	reviewerModels = repositoryReviewModelNames(reviewerModels)
	promptRevision = strings.TrimSpace(promptRevision)
	profileHash = strings.TrimSpace(profileHash)
	type reviewer struct {
		identity string
		required bool
	}
	reviewers := make([]reviewer, 0, len(reviewerModels)+1)
	if includeDefaultReviewer || len(reviewerModels) == 0 {
		reviewers = append(reviewers, reviewer{identity: "default", required: true})
	}
	for _, model := range reviewerModels {
		reviewers = append(reviewers, reviewer{
			identity: model, required: !includeDefaultReviewer,
		})
	}
	if len(reviewers) == 0 || len(reviewers)*len(repositoryBugFinderFocuses) > maxRepositoryReviewManagedAssignments {
		return nil, errors.New("invalid repository review assignment reviewer cohort")
	}
	catalog := make([]repoaudit.RepositoryReviewAssignment, 0,
		len(reviewers)*len(repositoryBugFinderFocuses),
	)
	for _, focus := range repositoryBugFinderFocuses {
		for _, reviewer := range reviewers {
			assignment, err := repoaudit.NewRepositoryReviewAssignment(
				focus.ID, reviewer.identity, promptRevision, profileHash, reviewer.required,
			)
			if err != nil {
				return nil, err
			}
			catalog = append(catalog, assignment)
		}
	}
	return repoaudit.NormalizeRepositoryReviewAssignmentCatalog(catalog)
}

// BindRepositoryBugFinderAssignmentTasks fills the trusted task text on a
// store-produced missing-only plan without changing assignment identity.
func BindRepositoryBugFinderAssignmentTasks(
	plan repoaudit.Plan,
) (repoaudit.Plan, error) {
	tasks := make(map[string]string, len(repositoryBugFinderFocuses))
	for _, focus := range repositoryBugFinderFocuses {
		tasks[focus.ID] = focus.Task
	}
	return repoaudit.BindRepositoryReviewAssignmentTasks(plan, tasks)
}

// RepositoryReviewRequiredAssignments returns the fixed required-child
// denominator for a resolved built-in reviewer ensemble.
func RepositoryReviewRequiredAssignments(reviewerCount int) (int, error) {
	if reviewerCount < 1 ||
		reviewerCount > maxRepositoryReviewManagedAssignments/RepositoryReviewRequiredAssignmentsPerReviewer {
		return 0, errors.New("invalid repository review reviewer count")
	}
	return reviewerCount * RepositoryReviewRequiredAssignmentsPerReviewer, nil
}

// RepositoryBugFinderRequiredAssignments applies the built-in default-chain
// semantics: fallback aliases are optional when the default reviewer is used.
func RepositoryBugFinderRequiredAssignments(
	reviewerModels []string,
	includeDefaultReviewer bool,
) (int, error) {
	if includeDefaultReviewer {
		return RepositoryReviewRequiredAssignments(1)
	}
	return RepositoryReviewRequiredAssignments(len(reviewerModels))
}
