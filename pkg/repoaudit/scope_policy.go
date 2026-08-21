package repoaudit

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

const (
	maxRepositoryReviewScopeFolders        = 64
	maxRepositoryReviewScopePrefixBytes    = 1024
	maxRepositoryReviewScopeTextBytes      = 16 << 10
	maxRepositoryReviewScopeSummaryBytes   = 4096
	maxRepositoryReviewScopeRationaleBytes = 16 << 10
	maxRepositoryReviewScopeWarnings       = 32
	maxRepositoryReviewScopeWarningBytes   = 2048
)

// RepositoryReviewCodeType is a deterministic inventory category selectable
// by a repository-review scope policy.
type RepositoryReviewCodeType string

const (
	RepositoryReviewCodeTypeHotpathCode RepositoryReviewCodeType = "hotpath-code"
	RepositoryReviewCodeTypeCode        RepositoryReviewCodeType = "code"
	RepositoryReviewCodeTypeTest        RepositoryReviewCodeType = "test"
	RepositoryReviewCodeTypeBenchTest   RepositoryReviewCodeType = "bench-test"
)

// RepositoryReviewScopePolicy is the reusable, commit-independent scope saved
// with an automation. IncludeFolders narrows the selected code types when it is
// non-empty. ExcludeFolders always wins over both category and include matches.
type RepositoryReviewScopePolicy struct {
	CodeTypes      []RepositoryReviewCodeType `json:"code_types"`
	IncludeFolders []string                   `json:"include_folders"`
	ExcludeFolders []string                   `json:"exclude_folders"`
	FreeText       string                     `json:"free_text,omitempty"`
}

// RepositoryReviewScopePlanCounts is a bounded summary of one commit-bound
// preflight plan. The planner/selector owns the exact count semantics.
type RepositoryReviewScopePlanCounts struct {
	TotalFiles    int `json:"total_files"`     // all files in the commit inventory
	CodeTypeFiles int `json:"code_type_files"` // files matching CodeTypes
	IncludeFiles  int `json:"include_files"`   // type matches after include narrowing
	ExcludedFiles int `json:"excluded_files"`  // include matches removed by excludes
	SelectedFiles int `json:"selected_files"`  // retained candidates after exclusions
}

// RepositoryReviewScopePlan records the durable explanation of a scope plan
// without persisting an unbounded file manifest in the automation profile.
type RepositoryReviewScopePlan struct {
	CommitSHA  string                          `json:"commit_sha"`
	PolicyHash string                          `json:"policy_hash"`
	Hash       string                          `json:"hash"`
	Summary    string                          `json:"summary"`
	Rationale  string                          `json:"rationale,omitempty"`
	Warnings   []string                        `json:"warnings"`
	Counts     RepositoryReviewScopePlanCounts `json:"counts"`
}

func defaultRepositoryReviewScopeCodeTypes() []RepositoryReviewCodeType {
	return []RepositoryReviewCodeType{
		RepositoryReviewCodeTypeHotpathCode,
		RepositoryReviewCodeTypeCode,
	}
}

// NormalizeRepositoryReviewScopePolicy returns a detached canonical policy.
// A zero policy is the backwards-compatible normal-production-code default.
func NormalizeRepositoryReviewScopePolicy(
	policy RepositoryReviewScopePolicy,
) (RepositoryReviewScopePolicy, error) {
	policy.CodeTypes = append([]RepositoryReviewCodeType(nil), policy.CodeTypes...)
	policy.IncludeFolders = append([]string(nil), policy.IncludeFolders...)
	policy.ExcludeFolders = append([]string(nil), policy.ExcludeFolders...)
	if err := normalizeRepositoryReviewScopePolicy(&policy); err != nil {
		return RepositoryReviewScopePolicy{}, err
	}
	return policy, nil
}

func normalizeRepositoryReviewScopePolicy(policy *RepositoryReviewScopePolicy) error {
	if policy == nil {
		return fmt.Errorf("%w: scope policy is required", ErrInvalidAutomation)
	}
	if len(policy.CodeTypes) == 0 {
		policy.CodeTypes = defaultRepositoryReviewScopeCodeTypes()
	}
	seenTypes := make(map[RepositoryReviewCodeType]struct{}, len(policy.CodeTypes))
	for index, raw := range policy.CodeTypes {
		codeType := RepositoryReviewCodeType(strings.ToLower(strings.TrimSpace(string(raw))))
		if !validRepositoryReviewCodeType(codeType) {
			return fmt.Errorf("%w: invalid scope code type", ErrInvalidAutomation)
		}
		if _, duplicate := seenTypes[codeType]; duplicate {
			return fmt.Errorf("%w: duplicate scope code type", ErrInvalidAutomation)
		}
		seenTypes[codeType] = struct{}{}
		policy.CodeTypes[index] = codeType
	}
	sort.Slice(policy.CodeTypes, func(i, j int) bool {
		return repositoryReviewCodeTypeRank(policy.CodeTypes[i]) < repositoryReviewCodeTypeRank(policy.CodeTypes[j])
	})

	var err error
	policy.IncludeFolders, err = normalizeRepositoryReviewFolderPrefixes(policy.IncludeFolders, "include")
	if err != nil {
		return err
	}
	policy.ExcludeFolders, err = normalizeRepositoryReviewFolderPrefixes(policy.ExcludeFolders, "exclude")
	if err != nil {
		return err
	}
	policy.FreeText = strings.TrimSpace(policy.FreeText)
	if !validOptionalAutomationText(policy.FreeText, maxRepositoryReviewScopeTextBytes) {
		return fmt.Errorf("%w: invalid free-text scope", ErrInvalidAutomation)
	}
	return nil
}

func normalizeRepositoryReviewFolderPrefixes(prefixes []string, field string) ([]string, error) {
	if len(prefixes) > maxRepositoryReviewScopeFolders {
		return nil, fmt.Errorf("%w: too many %s folder prefixes", ErrInvalidAutomation, field)
	}
	normalized := make([]string, 0, len(prefixes))
	seen := make(map[string]struct{}, len(prefixes))
	for _, raw := range prefixes {
		prefix := strings.TrimSpace(raw)
		if !validRepositoryReviewFolderPrefix(prefix) {
			return nil, fmt.Errorf("%w: invalid %s folder prefix", ErrInvalidAutomation, field)
		}
		if _, duplicate := seen[prefix]; duplicate {
			return nil, fmt.Errorf("%w: duplicate %s folder prefix", ErrInvalidAutomation, field)
		}
		seen[prefix] = struct{}{}
		normalized = append(normalized, prefix)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func validRepositoryReviewFolderPrefix(prefix string) bool {
	if !validBoundedText(prefix, maxRepositoryReviewScopePrefixBytes) ||
		strings.Contains(prefix, `\`) || strings.HasPrefix(prefix, "/") ||
		strings.HasSuffix(prefix, "/") {
		return false
	}
	cleaned := path.Clean(prefix)
	return cleaned == prefix && cleaned != "." && cleaned != ".." &&
		!strings.HasPrefix(cleaned, "../")
}

func validRepositoryReviewCodeType(codeType RepositoryReviewCodeType) bool {
	switch codeType {
	case RepositoryReviewCodeTypeHotpathCode, RepositoryReviewCodeTypeCode,
		RepositoryReviewCodeTypeTest, RepositoryReviewCodeTypeBenchTest:
		return true
	default:
		return false
	}
}

func repositoryReviewCodeTypeRank(codeType RepositoryReviewCodeType) int {
	switch codeType {
	case RepositoryReviewCodeTypeHotpathCode:
		return 0
	case RepositoryReviewCodeTypeCode:
		return 1
	case RepositoryReviewCodeTypeTest:
		return 2
	case RepositoryReviewCodeTypeBenchTest:
		return 3
	default:
		return 4
	}
}

func normalizeRepositoryReviewScopePlan(plan *RepositoryReviewScopePlan) error {
	if plan == nil {
		return fmt.Errorf("%w: scope plan is required", ErrInvalidAutomation)
	}
	plan.CommitSHA = strings.ToLower(strings.TrimSpace(plan.CommitSHA))
	plan.PolicyHash = strings.ToLower(strings.TrimSpace(plan.PolicyHash))
	plan.Hash = strings.ToLower(strings.TrimSpace(plan.Hash))
	plan.Summary = strings.TrimSpace(plan.Summary)
	plan.Rationale = strings.TrimSpace(plan.Rationale)
	if repositoryReviewScopePlanEmpty(*plan) {
		plan.Warnings = []string{}
		return nil
	}
	if !validRepositoryReviewCommitSHA(plan.CommitSHA) ||
		!validRepositoryReviewScopeHash(plan.PolicyHash) ||
		!validRepositoryReviewScopeHash(plan.Hash) ||
		!validBoundedText(plan.Summary, maxRepositoryReviewScopeSummaryBytes) ||
		!validOptionalAutomationText(plan.Rationale, maxRepositoryReviewScopeRationaleBytes) ||
		!validRepositoryReviewScopePlanCounts(plan.Counts) {
		return fmt.Errorf("%w: invalid commit-bound scope plan", ErrInvalidAutomation)
	}
	warnings, err := normalizeUniqueAutomationStrings(
		plan.Warnings,
		maxRepositoryReviewScopeWarnings,
		maxRepositoryReviewScopeWarningBytes,
		"scope warning",
	)
	if err != nil {
		return err
	}
	plan.Warnings = warnings
	return nil
}

func repositoryReviewScopePlanEmpty(plan RepositoryReviewScopePlan) bool {
	return plan.CommitSHA == "" && plan.PolicyHash == "" && plan.Hash == "" &&
		plan.Summary == "" && plan.Rationale == "" && len(plan.Warnings) == 0 &&
		plan.Counts == (RepositoryReviewScopePlanCounts{})
}

func validRepositoryReviewCommitSHA(value string) bool {
	return (len(value) == 40 || len(value) == 64) && validHexDigest(value)
}

func validRepositoryReviewScopeHash(value string) bool {
	return len(value) == 64 && validHexDigest(value)
}

func validRepositoryReviewScopePlanCounts(counts RepositoryReviewScopePlanCounts) bool {
	for _, value := range []int{
		counts.TotalFiles,
		counts.CodeTypeFiles,
		counts.IncludeFiles,
		counts.ExcludedFiles,
		counts.SelectedFiles,
	} {
		if value < 0 || value > maxReviewFiles {
			return false
		}
	}
	return counts.CodeTypeFiles <= counts.TotalFiles &&
		counts.IncludeFiles <= counts.CodeTypeFiles &&
		counts.ExcludedFiles <= counts.IncludeFiles &&
		counts.SelectedFiles <= counts.IncludeFiles &&
		counts.SelectedFiles+counts.ExcludedFiles <= counts.IncludeFiles
}
