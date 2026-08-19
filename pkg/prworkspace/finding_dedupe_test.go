package prworkspace

import (
	"testing"
	"time"
)

const (
	validHourClause = "Only hours in the inclusive range 0 through 23 are valid; every other hour returns ErrInvalidHour."
	periodClause    = "Hours 0 through 11 produce the morning greeting period, hours 12 through 17 produce the afternoon greeting period, and hours 18 through 23 produce the evening greeting period."
	blankNameClause = "A blank or whitespace-only name is rendered as stranger, consistent with the existing Hello behavior."
	testClause      = "go test ./... passes, including tests for period boundaries and invalid-hour inputs."
)

func findingLine(value int) *int { return &value }

func liveInitialReviewFindings() []AgentFinding {
	return []AgentFinding{
		{
			Title:          "Negative hours and hour 24 are accepted",
			File:           "greeting/time_aware.go",
			Line:           findingLine(11),
			Message:        "The validity check rejects only values greater than 24. It therefore treats every negative hour and hour 24 as valid, contrary to the required inclusive range 0 through 23.",
			CharterClauses: []string{validHourClause, testClause},
		},
		{
			Title:          "Period boundary hours 12 and 18 are misclassified",
			File:           "greeting/time_aware.go",
			Line:           findingLine(16),
			Message:        "The comparisons include 12 in morning and 18 in afternoon. The charter requires afternoon to begin at 12 and evening to begin at 18.",
			CharterClauses: []string{periodClause, testClause},
		},
		{
			Title:          "Blank names are not rendered as stranger",
			File:           "greeting/time_aware.go",
			Line:           findingLine(21),
			Message:        "TimeAware trims the supplied name but directly concatenates the trimmed value. It does not replace an empty result with stranger as required.",
			CharterClauses: []string{blankNameClause},
		},
		{
			Title:          "Required boundary and invalid-input test coverage is missing",
			File:           "greeting/time_aware_test.go",
			Line:           findingLine(8),
			Message:        "The test suite exercises only typical valid hours 9, 15, and 21 and one invalid value, 25. It does not exercise any period transition, the valid endpoints, negative invalid input, hour 24, trimming, or blank-name fallback.",
			CharterClauses: []string{testClause, validHourClause, periodClause, blankNameClause},
		},
	}
}

func liveFirstNudgeFindings() []AgentFinding {
	return []AgentFinding{
		{
			Title:          "Hour validation accepts negative hours and hour 24",
			File:           "greeting/time_aware.go",
			Line:           findingLine(12),
			Message:        "The validation rejects only values greater than 24, so negative hours and 24 return successful greetings instead of ErrInvalidHour.",
			CharterClauses: []string{validHourClause, testClause},
		},
		{
			Title:          "Noon and 18:00 use the preceding greeting periods",
			File:           "greeting/time_aware.go",
			Line:           findingLine(17),
			Message:        "The inclusive comparisons assign hour 12 to morning and hour 18 to afternoon, contrary to the specified period boundaries.",
			CharterClauses: []string{periodClause, testClause},
		},
		{
			Title:          "Blank names do not fall back to stranger",
			File:           "greeting/time_aware.go",
			Line:           findingLine(23),
			Message:        "The name is trimmed but the result is concatenated directly, without replacing an empty trimmed value with stranger.",
			CharterClauses: []string{blankNameClause},
		},
		{
			Title:          "Required boundary and invalid-input test coverage is missing",
			File:           "greeting/time_aware_test.go",
			Line:           findingLine(12),
			Message:        "The new tests cover only typical interior hours and one invalid high value; they do not exercise any period boundary, the valid-range endpoints, negative input, or hour 24.",
			CharterClauses: []string{testClause, validHourClause},
		},
	}
}

func liveSecondNudgeFindings() []AgentFinding {
	return []AgentFinding{
		{
			Title:          "Hour validation accepts 24 and all negative values",
			File:           "greeting/time_aware.go",
			Line:           findingLine(11),
			Message:        "The validation rejects only hours greater than 24, so invalid inputs such as -1 and 24 proceed as successful greetings.",
			CharterClauses: []string{validHourClause, testClause},
		},
		{
			Title:          "Noon and 18:00 are assigned to the wrong greeting periods",
			File:           "greeting/time_aware.go",
			Line:           findingLine(16),
			Message:        "The inclusive comparisons make hour 12 morning and hour 18 afternoon, contrary to the specified ranges.",
			CharterClauses: []string{periodClause, testClause},
		},
		{
			Title:          "Blank names are rendered as empty instead of stranger",
			File:           "greeting/time_aware.go",
			Line:           findingLine(21),
			Message:        "The result directly concatenates the trimmed name without substituting stranger when trimming produces an empty string.",
			CharterClauses: []string{blankNameClause},
		},
	}
}

func TestSemanticFindingSetCollapsesLiveReviewParaphrases(t *testing.T) {
	seen := newSemanticFindingSet()
	if novel, duplicates := countNovelFindings(liveInitialReviewFindings(), seen); novel != 4 || duplicates != 0 {
		t.Fatalf("initial novel=%d duplicates=%d, want 4/0", novel, duplicates)
	}
	if novel, duplicates := countNovelFindings(liveFirstNudgeFindings(), seen); novel != 0 || duplicates != 4 {
		t.Fatalf("first nudge novel=%d duplicates=%d, want 0/4", novel, duplicates)
	}
	if novel, duplicates := countNovelFindings(liveSecondNudgeFindings(), seen); novel != 0 || duplicates != 3 {
		t.Fatalf("second nudge novel=%d duplicates=%d, want 0/3", novel, duplicates)
	}
}

func TestSemanticFindingSetPreservesDistinctNearbyDefects(t *testing.T) {
	seen := newSemanticFindingSet()
	distinct := []AgentFinding{
		{
			Title: "Hour validation accepts hour 24", File: "greeting/time_aware.go", Line: findingLine(11),
			Message: "The invalid value 24 passes validation.", CharterClauses: []string{testClause},
		},
		{
			Title: "Period boundary hours 12 and 18 are wrong", File: "greeting/time_aware.go", Line: findingLine(16),
			Message: "Hours 12 and 18 select the preceding periods.", CharterClauses: []string{testClause},
		},
		{
			Title: "Authorization check can be bypassed", File: "greeting/time_aware.go", Line: findingLine(17),
			Message: "An untrusted caller can skip the ownership guard.", CharterClauses: []string{testClause},
		},
	}
	if novel, duplicates := countNovelFindings(distinct, seen); novel != len(distinct) || duplicates != 0 {
		t.Fatalf("nearby distinct findings novel=%d duplicates=%d, want %d/0", novel, duplicates, len(distinct))
	}
}

func TestSemanticFindingSetPreservesRepeatedDefectAtDifferentLocations(t *testing.T) {
	seen := newSemanticFindingSet()
	repeated := []AgentFinding{
		{
			Title:          "Nil input panics",
			File:           "pkg/parse.go",
			Line:           findingLine(10),
			Message:        "A nil request is dereferenced before validation.",
			CharterClauses: []string{"Invalid requests return an error."},
		},
		{
			Title:          "Nil input panics",
			File:           "pkg/parse.go",
			Line:           findingLine(90),
			Message:        "A nil request is dereferenced before validation.",
			CharterClauses: []string{"Invalid requests return an error."},
		},
		{
			Title:          "Nil input panics",
			File:           "pkg/other.go",
			Line:           findingLine(10),
			Message:        "A nil request is dereferenced before validation.",
			CharterClauses: []string{"Invalid requests return an error."},
		},
	}
	if novel, duplicates := countNovelFindings(repeated, seen); novel != len(repeated) || duplicates != 0 {
		t.Fatalf("separate occurrences novel=%d duplicates=%d, want %d/0", novel, duplicates, len(repeated))
	}
}

func TestSemanticFindingSetSeedsManualNudgeFromMaterializedFinding(t *testing.T) {
	initial := liveInitialReviewFindings()[0]
	stored := Finding{
		ID: "pfn_existing", Fingerprint: agentFindingFingerprint(initial), Title: initial.Title,
		File: initial.File, Line: initial.Line, Message: initial.Message,
		Scope: ScopeAssessment{CharterClauses: append([]string(nil), initial.CharterClauses...)},
	}
	seen := newSemanticFindingSet()
	seedSemanticSeenFindings(seen, []Finding{stored})
	if novel, duplicates := countNovelFindings(liveFirstNudgeFindings()[:1], seen); novel != 0 || duplicates != 1 {
		t.Fatalf("manual nudge novel=%d duplicates=%d, want 0/1", novel, duplicates)
	}
}

func TestMaterializeReviewRoundsUsesSemanticDuplicateIdentity(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	rounds := []ReviewRound{
		{Initial: true, Result: ReviewPass{Findings: liveInitialReviewFindings()}, NovelFindings: 4},
		{Round: 1, Result: ReviewPass{Findings: liveFirstNudgeFindings()}, NovelFindings: 0, DuplicateCount: 4},
		{Round: 2, Result: ReviewPass{Findings: liveSecondNudgeFindings()}, NovelFindings: 0, DuplicateCount: 3},
	}
	findings, nudges := materializeReviewRounds(
		Aggregate{Workspace: Workspace{ID: "prw_semantic_dedupe"}},
		"psr_semantic_dedupe", rounds,
		NudgePolicy{MinimumAdditionalRounds: 2, MaximumAdditionalRounds: 2}, now,
	)
	if len(findings) != 4 {
		t.Fatalf("materialized findings=%d, want four unique defects: %#v", len(findings), findings)
	}
	if len(nudges) != 2 || len(nudges[0].FindingIDs) != 0 || len(nudges[1].FindingIDs) != 0 {
		t.Fatalf("duplicate nudge findings were materialized: %#v", nudges)
	}
}
