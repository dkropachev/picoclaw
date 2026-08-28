package workflows

import (
	"errors"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/reposcope"
)

const (
	repositoryReviewLegacyScopeRecoveryRationale = "Recovered the exact union of retained legacy campaign scopes."
	repositoryReviewLegacyScopeRecoveryWarning   = "Legacy campaign scopes differed; continuation uses their exact retained union."
)

// RecoverRepositoryReviewFrozenScope maps a retained exact path union onto one
// commit-bound full candidate catalog and delegates plan construction to the
// native scope filter, including its canonical hash and count semantics.
func RecoverRepositoryReviewFrozenScope(
	candidatesValue any,
	hardScope any,
	commit string,
	inventoryHash string,
	selectedPaths []string,
) (repoaudit.RepositoryReviewScopeSelection, repoaudit.RepositoryReviewScopePlan, error) {
	var candidates []reposcope.Candidate
	if err := nativeRepositoryEvaluationDecode(candidatesValue, &candidates); err != nil {
		return repoaudit.RepositoryReviewScopeSelection{}, repoaudit.RepositoryReviewScopePlan{}, err
	}
	byPath := make(map[string]reposcope.Candidate, len(candidates))
	for _, candidate := range candidates {
		if err := reposcope.ValidateCandidate(candidate); err != nil ||
			candidate.CommitID != strings.ToLower(strings.TrimSpace(commit)) ||
			candidate.InventoryID != strings.TrimSpace(inventoryHash) {
			return repoaudit.RepositoryReviewScopeSelection{}, repoaudit.RepositoryReviewScopePlan{},
				reposcope.ErrInvalidCandidate
		}
		if _, duplicate := byPath[candidate.Path]; duplicate {
			return repoaudit.RepositoryReviewScopeSelection{}, repoaudit.RepositoryReviewScopePlan{},
				reposcope.ErrDuplicateCandidate
		}
		byPath[candidate.Path] = candidate
	}
	paths := append([]string(nil), selectedPaths...)
	sort.Strings(paths)
	ids := make([]string, 0, len(paths))
	for index, pathValue := range paths {
		if strings.TrimSpace(pathValue) != pathValue || pathValue == "" ||
			index > 0 && paths[index-1] == pathValue {
			return repoaudit.RepositoryReviewScopeSelection{}, repoaudit.RepositoryReviewScopePlan{},
				errors.New("invalid recovered repository scope path")
		}
		candidate, exists := byPath[pathValue]
		if !exists {
			return repoaudit.RepositoryReviewScopeSelection{}, repoaudit.RepositoryReviewScopePlan{},
				errors.New("recovered repository scope path is absent from the retained catalog")
		}
		ids = append(ids, candidate.ID)
	}
	sort.Strings(ids)
	filtered, err := nativeRepositoryEvaluationFilter(map[string]any{
		"candidates": candidates,
		"planner": map[string]any{
			"includePrefixes": []string{}, "excludePrefixes": []string{},
			"candidateIds": ids, "hotpathCandidateIds": []string{},
			"rationale": repositoryReviewLegacyScopeRecoveryRationale,
			"warnings":  []string{repositoryReviewLegacyScopeRecoveryWarning},
		},
		"scope_planned": false,
		"hard_scope":    hardScope,
		"commit":        strings.ToLower(strings.TrimSpace(commit)),
	})
	if err != nil {
		return repoaudit.RepositoryReviewScopeSelection{}, repoaudit.RepositoryReviewScopePlan{}, err
	}
	// The native filter constructed both values immediately above through the
	// same strict serializers; parsing cannot fail or change the selected paths.
	selection, _ := nativeRepositoryEvaluationParseScopeSelection(filtered["scopeSelection"])
	plan, _ := nativeRepositoryEvaluationParseScopePlan(filtered["scopePlan"])
	actualPaths := nativeStringSlice(filtered["selectedPaths"])
	sort.Strings(actualPaths)
	if !nativeRepositoryEvaluationStringsEqual(paths, actualPaths) {
		return repoaudit.RepositoryReviewScopeSelection{}, repoaudit.RepositoryReviewScopePlan{},
			errors.New("recovered repository scope did not reproduce its exact path union")
	}
	return selection, plan, nil
}
