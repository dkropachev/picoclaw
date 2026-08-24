package repoaudit

import (
	"fmt"
	"strings"
	"unicode"
)

const maxRepositoryReviewBranchBytes = 255

// NormalizeRepositoryReviewBranch validates a repository-review branch. An
// empty value means the repository's default branch. Commit IDs, full refs,
// tags, URLs, and revision expressions are deliberately excluded: durable
// repository-review configurations follow branches, not arbitrary revisions.
func NormalizeRepositoryReviewBranch(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	if value != strings.TrimSpace(value) || len(value) > maxRepositoryReviewBranchBytes {
		return "", fmt.Errorf("%w: invalid repository review branch", ErrInvalidAutomation)
	}
	for _, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) || character == unicode.MaxASCII {
			return "", fmt.Errorf("%w: invalid repository review branch", ErrInvalidAutomation)
		}
	}
	lower := strings.ToLower(value)
	if lower == "head" || lower == "@" || strings.HasPrefix(lower, "refs/") ||
		strings.HasPrefix(lower, "tags/") || strings.Contains(lower, "://") ||
		len(value) >= 7 && len(value) <= 64 && validHexBranch(value) {
		return "", fmt.Errorf("%w: repository reviews require a branch", ErrInvalidAutomation)
	}
	if strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.Contains(value, "//") || strings.Contains(value, "..") || strings.Contains(value, "@{") ||
		strings.ContainsAny(value, `~^:?#*[\`) {
		return "", fmt.Errorf("%w: invalid repository review branch", ErrInvalidAutomation)
	}
	for _, component := range strings.Split(value, "/") {
		componentLower := strings.ToLower(component)
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".") ||
			strings.HasSuffix(componentLower, ".lock") {
			return "", fmt.Errorf("%w: invalid repository review branch", ErrInvalidAutomation)
		}
	}
	return value, nil
}

func validHexBranch(value string) bool {
	for _, character := range value {
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' ||
			character >= 'A' && character <= 'F' {
			continue
		}
		return false
	}
	return value != ""
}
