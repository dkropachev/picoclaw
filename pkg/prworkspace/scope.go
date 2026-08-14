package prworkspace

import (
	"path/filepath"
	"strings"
)

type SizeThreshold struct {
	Files         int `json:"files"`
	SemanticLines int `json:"semantic_lines"`
	Modules       int `json:"modules"`
}

type SizePolicy struct {
	XS SizeThreshold `json:"xs"`
	S  SizeThreshold `json:"s"`
	M  SizeThreshold `json:"m"`
}

func DefaultSizePolicy() SizePolicy {
	return SizePolicy{
		XS: SizeThreshold{Files: 1, SemanticLines: 20, Modules: 1},
		S:  SizeThreshold{Files: 3, SemanticLines: 100, Modules: 1},
		M:  SizeThreshold{Files: 10, SemanticLines: 500, Modules: 3},
	}
}

func (policy SizePolicy) Valid() bool {
	return positiveThreshold(policy.XS) && positiveThreshold(policy.S) &&
		positiveThreshold(policy.M) &&
		policy.XS.Files <= policy.S.Files && policy.S.Files <= policy.M.Files &&
		policy.XS.SemanticLines <= policy.S.SemanticLines &&
		policy.S.SemanticLines <= policy.M.SemanticLines &&
		policy.XS.Modules <= policy.S.Modules && policy.S.Modules <= policy.M.Modules
}

func positiveThreshold(value SizeThreshold) bool {
	return value.Files > 0 && value.SemanticLines > 0 && value.Modules > 0
}

func ClassifyChangeSize(files, semanticLines, modules int, policy SizePolicy) ChangeSize {
	if !policy.Valid() {
		policy = DefaultSizePolicy()
	}
	if files < 0 || semanticLines < 0 || modules < 0 {
		return ChangeSizeL
	}
	if withinThreshold(files, semanticLines, modules, policy.XS) {
		return ChangeSizeXS
	}
	if withinThreshold(files, semanticLines, modules, policy.S) {
		return ChangeSizeS
	}
	if withinThreshold(files, semanticLines, modules, policy.M) {
		return ChangeSizeM
	}
	return ChangeSizeL
}

func withinThreshold(files, lines, modules int, threshold SizeThreshold) bool {
	return files <= threshold.Files && lines <= threshold.SemanticLines &&
		modules <= threshold.Modules
}

type ScopeAction string

const (
	ScopeActionProceed       ScopeAction = "proceed"
	ScopeActionGate          ScopeAction = "gate"
	ScopeActionReviseOrDefer ScopeAction = "revise_or_defer"
	ScopeActionDefer         ScopeAction = "defer"
)

// DecideScope applies the non-bypassable default charter policy. Callers may
// gate an exact large change, but they may not admit external work without a
// new charter revision.
func DecideScope(assessment ScopeAssessment) ScopeAction {
	if !assessment.TypeCompatible {
		return ScopeActionReviseOrDefer
	}
	switch assessment.Distance {
	case ScopeExact:
		if assessment.Size == ChangeSizeXS || assessment.Size == ChangeSizeS {
			return ScopeActionProceed
		}
		return ScopeActionGate
	case ScopeNecessaryAdjacent:
		return ScopeActionReviseOrDefer
	case ScopeRelatedFollowup, ScopeUnrelated:
		return ScopeActionDefer
	default:
		return ScopeActionReviseOrDefer
	}
}

// HardCandidateScopeBlocker identifies candidate code that no configurable
// authorization gate may simply approve. S0 large work and S1 necessary
// adjacent work may be explicitly classified by policy; type-incompatible,
// S2, S3, or malformed candidate scope requires removal or a revised and
// reconfirmed charter.
func HardCandidateScopeBlocker(assessment ScopeAssessment) bool {
	if assessment.Presence != WorkCandidatePresent || !assessment.TypeCompatible {
		return assessment.Presence == WorkCandidatePresent
	}
	return assessment.Distance != ScopeExact && assessment.Distance != ScopeNecessaryAdjacent
}

type FileClass string

const (
	FileRuntime   FileClass = "runtime"
	FileTest      FileClass = "test"
	FileDocs      FileClass = "documentation"
	FileFixture   FileClass = "fixture"
	FileMigration FileClass = "migration"
	FileGenerated FileClass = "generated"
	FileUnknown   FileClass = "unknown"
)

// ClassifyFile supplies deterministic evidence to the semantic type auditor.
// It never decides intent by itself.
func ClassifyFile(path string) FileClass {
	normalized := strings.ToLower(filepath.ToSlash(strings.TrimSpace(path)))
	if normalized == "" || strings.HasPrefix(normalized, "/") ||
		strings.Contains(normalized, "../") {
		return FileUnknown
	}
	base := filepath.Base(normalized)
	if strings.Contains(normalized, "/generated/") ||
		strings.Contains(normalized, "/vendor/") ||
		strings.HasSuffix(base, ".generated.go") || strings.HasSuffix(base, ".gen.ts") {
		return FileGenerated
	}
	if strings.Contains(normalized, "/migrations/") ||
		strings.Contains(normalized, "/migration/") || strings.HasSuffix(base, ".sql") {
		return FileMigration
	}
	if strings.HasPrefix(normalized, "docs/") || strings.Contains(normalized, "/docs/") ||
		strings.HasSuffix(base, ".md") || strings.HasSuffix(base, ".mdx") ||
		strings.HasSuffix(base, ".rst") {
		return FileDocs
	}
	if strings.Contains(normalized, "/fixtures/") ||
		strings.Contains(normalized, "/testdata/") || strings.Contains(normalized, "/__snapshots__/") {
		return FileFixture
	}
	if strings.Contains(normalized, "/tests/") || strings.Contains(normalized, "/__tests__/") ||
		strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, ".test.ts") ||
		strings.HasSuffix(base, ".test.tsx") || strings.HasSuffix(base, ".spec.ts") ||
		strings.HasSuffix(base, ".spec.tsx") {
		return FileTest
	}
	return FileRuntime
}

// DeterministicTypeCompatible rejects only combinations whose file category
// proves a mismatch. Semantic audit remains required for other combinations.
func DeterministicTypeCompatible(prType PRType, class FileClass) bool {
	switch prType {
	case PRTypeDocumentation:
		return class == FileDocs || class == FileGenerated
	case PRTypeTest:
		return class == FileTest || class == FileFixture || class == FileGenerated
	case PRTypeRefactor:
		return class != FileMigration
	case PRTypeFix, PRTypeFeature:
		return class != FileUnknown
	default:
		return false
	}
}
