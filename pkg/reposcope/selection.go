package reposcope

import (
	"fmt"
	"sort"
)

// SelectDeterministic selects a region- and module-diverse corpus without an AI
// proposal. It is equivalent to validating an empty proposal.
func SelectDeterministic(candidates []Candidate, policy SelectionPolicy) (SelectionResult, error) {
	return ValidateAISelection(candidates, AISelection{}, policy)
}

// ValidateAISelection validates opaque AI choices, enforces immutable
// candidates and quotas, then deterministically fills every omission up to each
// language's quota. The same inputs always produce the same result.
func ValidateAISelection(
	candidates []Candidate,
	proposal AISelection,
	policy SelectionPolicy,
) (SelectionResult, error) {
	byID := make(map[string]Candidate, len(candidates))
	byLanguage := make(map[Language][]Candidate)
	var commitID, inventoryID string
	for _, candidate := range candidates {
		if err := ValidateCandidate(candidate); err != nil {
			return SelectionResult{}, err
		}
		if _, duplicate := byID[candidate.ID]; duplicate {
			return SelectionResult{}, fmt.Errorf("%w: duplicate immutable ID %q", ErrInvalidCandidate, candidate.ID)
		}
		if commitID == "" {
			commitID, inventoryID = candidate.CommitID, candidate.InventoryID
		} else if candidate.CommitID != commitID || candidate.InventoryID != inventoryID {
			return SelectionResult{}, fmt.Errorf(
				"%w: candidates span multiple repository snapshots",
				ErrInvalidCandidate,
			)
		}
		byID[candidate.ID] = candidate
		byLanguage[candidate.Language] = append(byLanguage[candidate.Language], candidate)
	}
	normalizedPolicy, err := normalizePolicy(policy, byLanguage)
	if err != nil {
		return SelectionResult{}, err
	}

	chosenByLanguage := make(map[Language][]Candidate)
	chosenIDs := make(map[string]struct{}, len(proposal.CandidateIDs))
	result := SelectionResult{}
	for _, id := range proposal.CandidateIDs {
		if _, duplicate := chosenIDs[id]; duplicate {
			return SelectionResult{}, fmt.Errorf("%w: %q", ErrDuplicateCandidate, id)
		}
		candidate, ok := byID[id]
		if !ok {
			return SelectionResult{}, fmt.Errorf("%w: %q", ErrUnknownCandidate, id)
		}
		quota := quotaFor(normalizedPolicy, candidate.Language)
		if len(chosenByLanguage[candidate.Language]) >= quota {
			return SelectionResult{}, fmt.Errorf(
				"%w: language %q has quota %d",
				ErrQuotaExceeded,
				candidate.Language,
				quota,
			)
		}
		chosenIDs[id] = struct{}{}
		chosenByLanguage[candidate.Language] = append(chosenByLanguage[candidate.Language], candidate)
		result.AcceptedAIIDs = append(result.AcceptedAIIDs, id)
	}

	languages := make([]Language, 0, len(byLanguage))
	for language := range byLanguage {
		languages = append(languages, language)
	}
	sort.Slice(languages, func(i, j int) bool { return languages[i] < languages[j] })
	for _, language := range languages {
		quota := quotaFor(normalizedPolicy, language)
		target := min(quota, len(byLanguage[language]))
		for len(chosenByLanguage[language]) < target {
			// target never exceeds the validated unique candidate count, so an
			// unchosen fill candidate necessarily exists here.
			candidate, _ := bestFillCandidate(
				byLanguage[language],
				chosenIDs,
				chosenByLanguage[language],
				normalizedPolicy.PreferredMinBytes,
			)
			chosenIDs[candidate.ID] = struct{}{}
			chosenByLanguage[language] = append(chosenByLanguage[language], candidate)
			result.FilledIDs = append(result.FilledIDs, candidate.ID)
		}
		result.Selected = append(result.Selected, chosenByLanguage[language]...)
	}
	return result, nil
}

func normalizePolicy(policy SelectionPolicy, candidates map[Language][]Candidate) (SelectionPolicy, error) {
	if policy.DefaultPerLanguage == 0 {
		policy.DefaultPerLanguage = DefaultPerLanguageQuota
	}
	if policy.DefaultPerLanguage < 1 || policy.DefaultPerLanguage > MaxPerLanguageQuota {
		return SelectionPolicy{}, fmt.Errorf(
			"%w: default quota must be between 1 and %d",
			ErrInvalidPolicy,
			MaxPerLanguageQuota,
		)
	}
	if policy.PreferredMinBytes == 0 {
		policy.PreferredMinBytes = DefaultPreferredMinBytes
	}
	if policy.PreferredMinBytes < 1 || policy.PreferredMinBytes > AbsoluteMaxFileBytes {
		return SelectionPolicy{}, fmt.Errorf(
			"%w: preferred minimum bytes must be between 1 and %d",
			ErrInvalidPolicy,
			AbsoluteMaxFileBytes,
		)
	}
	for language, quota := range policy.PerLanguage {
		if _, ok := candidates[language]; !ok {
			return SelectionPolicy{}, fmt.Errorf("%w: quota names absent language %q", ErrInvalidPolicy, language)
		}
		if quota < 1 || quota > MaxPerLanguageQuota {
			return SelectionPolicy{}, fmt.Errorf(
				"%w: quota for %q must be between 1 and %d",
				ErrInvalidPolicy,
				language,
				MaxPerLanguageQuota,
			)
		}
	}
	return policy, nil
}

func quotaFor(policy SelectionPolicy, language Language) int {
	if quota, ok := policy.PerLanguage[language]; ok {
		return quota
	}
	return policy.DefaultPerLanguage
}

func bestFillCandidate(
	candidates []Candidate,
	chosenIDs map[string]struct{},
	chosen []Candidate,
	preferredMinBytes int64,
) (Candidate, bool) {
	regionCounts := make(map[string]int)
	moduleCounts := make(map[string]int)
	for _, candidate := range chosen {
		regionCounts[candidate.Region]++
		moduleCounts[candidate.Module]++
	}
	var best Candidate
	found := false
	for _, candidate := range candidates {
		if _, alreadyChosen := chosenIDs[candidate.ID]; alreadyChosen {
			continue
		}
		if !found || fillLess(candidate, best, regionCounts, moduleCounts, preferredMinBytes) {
			best = candidate
			found = true
		}
	}
	return best, found
}

func fillLess(left, right Candidate, regionCounts, moduleCounts map[string]int, preferredMinBytes int64) bool {
	leftSufficient := left.Size >= preferredMinBytes
	rightSufficient := right.Size >= preferredMinBytes
	if leftSufficient != rightSufficient {
		return leftSufficient
	}
	if regionCounts[left.Region] != regionCounts[right.Region] {
		return regionCounts[left.Region] < regionCounts[right.Region]
	}
	if moduleCounts[left.Module] != moduleCounts[right.Module] {
		return moduleCounts[left.Module] < moduleCounts[right.Module]
	}
	if left.Size != right.Size {
		return left.Size > right.Size
	}
	if left.Path != right.Path {
		return left.Path < right.Path
	}
	return left.ID < right.ID
}
