package prworkspace

import (
	"path"
	"sort"
	"strings"
	"unicode"
)

const semanticFindingLineRadius = 5

// semanticFindingSet supplements the durable, exact finding fingerprint with
// a deliberately conservative semantic comparison. Agent prose is not stable
// across review passes: the same defect is routinely reworded by a nudge and
// may be cited one or two lines away. Exact fingerprints remain authoritative
// for exact matches; semantic matching is only allowed for the same file and a
// tightly bounded source location.
type semanticFindingSet struct {
	byFingerprint map[string]int
	entries       []semanticFindingEntry
}

type semanticFindingEntry struct {
	identity semanticFindingIdentity
	id       string
}

type semanticFindingIdentity struct {
	file       string
	line       *int
	title      map[string]struct{}
	body       map[string]struct{}
	numbers    map[string]struct{}
	clauseKeys map[string]struct{}
}

func newSemanticFindingSet() *semanticFindingSet {
	return &semanticFindingSet{byFingerprint: make(map[string]int)}
}

// add records candidate and returns the canonical finding ID when it matches
// an existing entry. Duplicate paraphrases are retained as aliases so a later
// round can match either wording, while all aliases still resolve to the first
// materialized finding.
func (set *semanticFindingSet) add(candidate AgentFinding, fingerprint, id string) (string, bool) {
	if set == nil {
		return "", false
	}
	if fingerprint == "" {
		fingerprint = agentFindingFingerprint(candidate)
	}
	if index, exists := set.byFingerprint[fingerprint]; exists {
		return set.entries[index].id, true
	}
	identity := newSemanticFindingIdentity(candidate)
	for _, entry := range set.entries {
		if semanticFindingIdentitiesDuplicate(entry.identity, identity) {
			set.entries = append(set.entries, semanticFindingEntry{
				identity: identity,
				id:       entry.id,
			})
			set.byFingerprint[fingerprint] = len(set.entries) - 1
			return entry.id, true
		}
	}
	set.entries = append(set.entries, semanticFindingEntry{
		identity: identity,
		id:       id,
	})
	set.byFingerprint[fingerprint] = len(set.entries) - 1
	return "", false
}

func (set *semanticFindingSet) addStored(finding Finding) {
	set.add(agentFindingFromRecord(finding), finding.Fingerprint, finding.ID)
}

func agentFindingFromRecord(finding Finding) AgentFinding {
	return AgentFinding{
		Severity:         finding.Severity,
		Title:            finding.Title,
		File:             finding.File,
		Line:             finding.Line,
		Message:          finding.Message,
		Evidence:         finding.Evidence,
		Impact:           finding.Impact,
		Recommendation:   finding.Recommendation,
		Validation:       finding.Validation,
		ScopeDistance:    finding.Scope.Distance,
		ChangeSize:       finding.Scope.Size,
		TypeCompatible:   finding.Scope.TypeCompatible,
		ScopeConfidence:  finding.Scope.Confidence,
		ScopeExplanation: finding.Scope.Explanation,
		CharterClauses:   append([]string(nil), finding.Scope.CharterClauses...),
	}
}

func newSemanticFindingIdentity(finding AgentFinding) semanticFindingIdentity {
	body := semanticTokenSet(finding.Title + "\n" + finding.Message)
	return semanticFindingIdentity{
		file:       normalizeFindingPath(finding.File),
		line:       finding.Line,
		title:      semanticTokenSet(finding.Title),
		body:       body,
		numbers:    numericSemanticTokens(body),
		clauseKeys: semanticClauseKeys(finding.CharterClauses),
	}
}

func semanticFindingIdentitiesDuplicate(left, right semanticFindingIdentity) bool {
	if left.file == "" || left.file != right.file || !nearbyFindingLines(left.line, right.line) {
		return false
	}
	if conflictingNumericAnchors(left.numbers, right.numbers) {
		return false
	}

	titleSimilarity := tokenDice(left.title, right.title)
	bodySimilarity := tokenDice(left.body, right.body)
	sharedClause := shareSemanticClause(left.clauseKeys, right.clauseKeys)

	// Matching titles plus corroborating detail cover stable issue names (for
	// example, missing boundary tests) without treating a shared noun as an
	// identity.
	if titleSimilarity >= 0.65 && (bodySimilarity >= 0.35 || sharedClause) {
		return true
	}
	// A strongly overlapping defect explanation is sufficient at the same
	// bounded source location even when the short titles use different words.
	if bodySimilarity >= 0.72 {
		return true
	}
	// Charter clauses disambiguate paraphrases such as "noon" versus "hour
	// 12". Text must still agree so a broad validation clause cannot collapse
	// two adjacent defects.
	return sharedClause && titleSimilarity >= 0.25 && bodySimilarity >= 0.40
}

func normalizeFindingPath(value string) string {
	value = strings.TrimSpace(value)
	for strings.HasPrefix(value, "./") {
		value = strings.TrimPrefix(value, "./")
	}
	if value == "" || value == "." {
		return ""
	}
	return path.Clean(value)
}

func nearbyFindingLines(left, right *int) bool {
	if left == nil || right == nil {
		// A missing line cannot establish proximity. Semantic thresholds below
		// remain strict enough to deduplicate file-level findings while exact
		// fingerprints are handled before this check.
		return left == nil && right == nil
	}
	delta := *left - *right
	if delta < 0 {
		delta = -delta
	}
	return delta <= semanticFindingLineRadius
}

var semanticFindingStopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "been": {},
	"being": {}, "but": {}, "by": {}, "do": {}, "does": {}, "for": {}, "from": {}, "has": {},
	"have": {}, "in": {}, "into": {}, "is": {}, "it": {}, "its": {}, "of": {}, "on": {},
	"only": {}, "or": {}, "so": {}, "that": {}, "the": {}, "their": {}, "then": {}, "there": {},
	"these": {}, "this": {}, "those": {}, "to": {}, "use": {}, "uses": {}, "was": {}, "were": {},
	"when": {}, "where": {}, "which": {}, "while": {}, "with": {}, "without": {},
}

func semanticTokenSet(value string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		token = normalizeSemanticToken(token)
		if token == "" {
			continue
		}
		if _, stopped := semanticFindingStopWords[token]; stopped {
			continue
		}
		result[token] = struct{}{}
	}
	return result
}

func normalizeSemanticToken(token string) string {
	switch token {
	case "noon":
		return "12"
	case "midnight":
		return "0"
	}
	if len(token) > 4 && strings.HasSuffix(token, "ies") {
		return token[:len(token)-3] + "y"
	}
	if len(token) > 5 && strings.HasSuffix(token, "ing") {
		return token[:len(token)-3]
	}
	if len(token) > 4 && strings.HasSuffix(token, "ed") {
		return token[:len(token)-2]
	}
	if len(token) > 3 && strings.HasSuffix(token, "s") && !strings.HasSuffix(token, "ss") {
		return token[:len(token)-1]
	}
	return token
}

func tokenDice(left, right map[string]struct{}) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	common := 0
	for token := range left {
		if _, exists := right[token]; exists {
			common++
		}
	}
	return float64(2*common) / float64(len(left)+len(right))
}

func semanticClauseKeys(clauses []string) map[string]struct{} {
	keys := make(map[string]struct{}, len(clauses))
	for _, clause := range clauses {
		tokens := semanticTokenSet(clause)
		if len(tokens) == 0 {
			continue
		}
		ordered := make([]string, 0, len(tokens))
		for token := range tokens {
			ordered = append(ordered, token)
		}
		sort.Strings(ordered)
		keys[strings.Join(ordered, "\x00")] = struct{}{}
	}
	return keys
}

func shareSemanticClause(left, right map[string]struct{}) bool {
	if len(left) > len(right) {
		left, right = right, left
	}
	for key := range left {
		if _, exists := right[key]; exists {
			return true
		}
	}
	return false
}

func numericSemanticTokens(tokens map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{})
	for token := range tokens {
		if semanticNumericToken(token) {
			result[token] = struct{}{}
		}
	}
	return result
}

func conflictingNumericAnchors(leftNumbers, rightNumbers map[string]struct{}) bool {
	if len(leftNumbers) == 0 || len(rightNumbers) == 0 {
		return false
	}
	for token := range leftNumbers {
		if _, exists := rightNumbers[token]; exists {
			return false
		}
	}
	return true
}

func semanticNumericToken(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
