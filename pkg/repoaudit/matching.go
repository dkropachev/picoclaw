package repoaudit

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/utils"
)

const repositoryMatchCandidateLimit = 10

type RepositoryPathEquivalent func(left, right string) bool

type RepositoryMatchCandidate struct {
	ID                 string   `json:"id"`
	Score              float64  `json:"score"`
	PathEquivalent     bool     `json:"path_equivalent"`
	PrimarySymbolEqual bool     `json:"primary_symbol_equal"`
	CausalDice         float64  `json:"causal_dice"`
	TitleDice          float64  `json:"title_dice"`
	AnchorJaccard      float64  `json:"anchor_jaccard"`
	HardConflict       bool     `json:"hard_conflict"`
	Conflicts          []string `json:"conflicts,omitempty"`
}

type RepositoryMatchResult struct {
	RepositoryFindingID string                     `json:"repository_finding_id,omitempty"`
	Method              string                     `json:"method"`
	Candidates          []RepositoryMatchCandidate `json:"candidates,omitempty"`
}

// MatchRepositoryFinding implements the CPU side of repository-finding
// association. Exact and deterministic matches are returned directly;
// otherwise Candidates contains the bounded BM25 retrieval set for isolated AI
// adjudication. occurrences is used only to prove an exact same-commit
// fingerprint and may be nil.
func MatchRepositoryFinding(
	finding Finding,
	repositoryFindings []RepositoryFinding,
	occurrences map[string]Finding,
	renameEquivalent RepositoryPathEquivalent,
) RepositoryMatchResult {
	if renameEquivalent == nil {
		renameEquivalent = func(left, right string) bool {
			return normalizedRepositoryPath(left) == normalizedRepositoryPath(right)
		}
	}
	for _, aggregate := range repositoryFindings {
		for _, occurrenceID := range aggregate.ReviewFindingIDs {
			occurrence, ok := occurrences[occurrenceID]
			if ok && occurrence.CommitSHA == finding.CommitSHA &&
				occurrence.Fingerprint != "" && occurrence.Fingerprint == finding.Fingerprint {
				return RepositoryMatchResult{
					RepositoryFindingID: aggregate.ID,
					Method:              "exact_same_commit_fingerprint",
				}
			}
		}
	}

	metrics := make(map[string]RepositoryMatchCandidate, len(repositoryFindings))
	auto := make([]RepositoryMatchCandidate, 0, 1)
	retrievalCorpus := make([]RepositoryFinding, 0, len(repositoryFindings))
	for _, aggregate := range repositoryFindings {
		candidate := repositoryMatchMetrics(finding, aggregate, renameEquivalent)
		metrics[aggregate.ID] = candidate
		if candidate.HardConflict {
			continue
		}
		if repositoryMatchPrefilter(finding, aggregate, candidate) {
			retrievalCorpus = append(retrievalCorpus, aggregate)
		}
		if candidate.PathEquivalent && candidate.PrimarySymbolEqual && len(candidate.Conflicts) == 0 &&
			(candidate.CausalDice >= 0.72 ||
				candidate.TitleDice >= 0.65 && candidate.CausalDice >= 0.50 &&
					candidate.AnchorJaccard >= 0.50) {
			candidate.Score = repositoryDeterministicScore(candidate)
			auto = append(auto, candidate)
		}
	}
	if len(auto) > 0 {
		sortRepositoryMatchCandidates(auto)
		// Equal evidence for two aggregates is an ambiguity, not permission to
		// choose whichever happened to be stored first.
		if len(auto) == 1 || auto[0].Score-auto[1].Score > 1e-9 {
			return RepositoryMatchResult{
				RepositoryFindingID: auto[0].ID,
				Method:              "deterministic",
			}
		}
	}

	// Same-component retrieval intentionally crosses path/symbol boundaries so
	// moved or refactored defects still reach adjudication.
	for _, aggregate := range repositoryFindings {
		if normalizedText(aggregate.MatchHints.Component) != "" &&
			normalizedText(aggregate.MatchHints.Component) == normalizedText(finding.MatchHints.Component) &&
			!containsRepositoryFinding(retrievalCorpus, aggregate.ID) &&
			!metrics[aggregate.ID].HardConflict {
			retrievalCorpus = append(retrievalCorpus, aggregate)
		}
	}
	if len(retrievalCorpus) == 0 {
		return RepositoryMatchResult{Method: "distinct"}
	}
	engine := utils.NewBM25Engine(retrievalCorpus, repositoryFindingSearchText)
	results := engine.Search(repositoryReviewFindingSearchText(finding), len(retrievalCorpus))
	ranked := make([]RepositoryMatchCandidate, 0, min(repositoryMatchCandidateLimit, len(retrievalCorpus)))
	seen := make(map[string]struct{}, len(results))
	for _, result := range results {
		candidate := metrics[result.Document.ID]
		candidate.Score = float64(result.Score)
		ranked = append(ranked, candidate)
		seen[candidate.ID] = struct{}{}
	}
	// BM25 returns no zero-score documents. Preserve same-component candidates
	// as bounded fallback inputs with a deterministic ID tie-break.
	for _, aggregate := range retrievalCorpus {
		if _, ok := seen[aggregate.ID]; ok {
			continue
		}
		ranked = append(ranked, metrics[aggregate.ID])
	}
	sortRepositoryMatchCandidates(ranked)
	if len(ranked) > repositoryMatchCandidateLimit {
		ranked = ranked[:repositoryMatchCandidateLimit]
	}
	return RepositoryMatchResult{Method: "ai", Candidates: ranked}
}

// RankRepositoryFindingsBM25 exposes the deterministic retrieval stage for
// issue discovery, validation commit selection, and focused tests.
func RankRepositoryFindingsBM25(
	query Finding,
	candidates []RepositoryFinding,
	limit int,
) []RepositoryMatchCandidate {
	if limit <= 0 || limit > repositoryMatchCandidateLimit {
		limit = repositoryMatchCandidateLimit
	}
	engine := utils.NewBM25Engine(candidates, repositoryFindingSearchText)
	results := engine.Search(repositoryReviewFindingSearchText(query), limit)
	out := make([]RepositoryMatchCandidate, 0, len(results))
	for _, result := range results {
		out = append(out, RepositoryMatchCandidate{ID: result.Document.ID, Score: float64(result.Score)})
	}
	sortRepositoryMatchCandidates(out)
	return out
}

func repositoryMatchMetrics(
	finding Finding,
	aggregate RepositoryFinding,
	renameEquivalent RepositoryPathEquivalent,
) RepositoryMatchCandidate {
	pathEquivalent := false
	for _, history := range aggregate.PathSymbolHistory {
		if renameEquivalent(finding.File.Path, history.Path) {
			pathEquivalent = true
			break
		}
	}
	primaryEqual := normalizedText(finding.Symbol) != "" &&
		repositoryFindingHasSymbol(aggregate, finding.Symbol, false)
	hardConflicts := repositoryHardCausalConflicts(finding.MatchHints, aggregate.MatchHints)
	conflicts := append(
		append([]string(nil), hardConflicts...),
		repositoryCausalFieldConflicts(finding.MatchHints, aggregate.MatchHints)...,
	)
	return RepositoryMatchCandidate{
		ID:                 aggregate.ID,
		PathEquivalent:     pathEquivalent,
		PrimarySymbolEqual: primaryEqual,
		CausalDice: tokenDice(
			findingTokens(repositoryCausalText(finding.MatchHints)),
			findingTokens(repositoryCausalText(aggregate.MatchHints)),
		),
		TitleDice: tokenDice(findingTokens(finding.Title), findingTokens(aggregate.CanonicalTitle)),
		AnchorJaccard: repositoryAnchorJaccard(
			finding.MatchHints.SourceAnchors,
			aggregate.MatchHints.SourceAnchors,
		),
		HardConflict: len(hardConflicts) > 0,
		Conflicts:    conflicts,
	}
}

func repositoryMatchPrefilter(
	finding Finding,
	aggregate RepositoryFinding,
	metrics RepositoryMatchCandidate,
) bool {
	if metrics.HardConflict || aggregate.ID == "" {
		return false
	}
	if metrics.PathEquivalent || repositoryFindingSharesSymbol(finding, aggregate) ||
		normalizedText(finding.MatchHints.Component) != "" &&
			normalizedText(finding.MatchHints.Component) == normalizedText(aggregate.MatchHints.Component) {
		return true
	}
	return repositorySharedAnchor(finding.MatchHints.SourceAnchors, aggregate.MatchHints.SourceAnchors)
}

func repositoryFindingSharesSymbol(finding Finding, aggregate RepositoryFinding) bool {
	symbols := append([]string{finding.Symbol}, finding.MatchHints.RelatedSymbols...)
	for _, symbol := range symbols {
		if repositoryFindingHasSymbol(aggregate, symbol, true) {
			return true
		}
	}
	return false
}

func repositoryFindingHasSymbol(
	aggregate RepositoryFinding,
	symbol string,
	includeRelated bool,
) bool {
	symbol = normalizedText(symbol)
	if symbol == "" {
		return false
	}
	for _, history := range aggregate.PathSymbolHistory {
		if normalizedText(history.Symbol) == symbol {
			return true
		}
	}
	if includeRelated {
		for _, related := range aggregate.MatchHints.RelatedSymbols {
			if normalizedText(related) == symbol {
				return true
			}
		}
	}
	return false
}

func repositoryHardCausalConflicts(left, right MatchHints) []string {
	conflicts := make([]string, 0, 4)
	if repositoryNumericAnchorConflict(left.SourceAnchors, right.SourceAnchors) {
		conflicts = append(conflicts, "numeric_source_anchor")
	}
	fields := []struct {
		name        string
		left, right string
	}{
		{"operation", left.Operation, right.Operation},
		{"failure_mode", left.FailureMode, right.FailureMode},
		{"trigger", left.Trigger, right.Trigger},
		{"violated_invariant", left.ViolatedInvariant, right.ViolatedInvariant},
		{"observable_outcome", left.ObservableOutcome, right.ObservableOutcome},
	}
	for _, field := range fields {
		if repositoryTextNumericConflict(field.left, field.right) {
			conflicts = append(conflicts, "numeric_"+field.name)
		}
	}
	return conflicts
}

func repositoryCausalFieldConflicts(left, right MatchHints) []string {
	fields := []struct {
		name        string
		left, right string
	}{
		{"operation", left.Operation, right.Operation},
		{"failure_mode", left.FailureMode, right.FailureMode},
		{"trigger", left.Trigger, right.Trigger},
		{"violated_invariant", left.ViolatedInvariant, right.ViolatedInvariant},
		{"observable_outcome", left.ObservableOutcome, right.ObservableOutcome},
	}
	conflicts := make([]string, 0, len(fields))
	for _, field := range fields {
		leftTokens, rightTokens := findingTokens(field.left), findingTokens(field.right)
		if len(leftTokens) >= 2 && len(rightTokens) >= 2 && tokenDice(leftTokens, rightTokens) == 0 {
			conflicts = append(conflicts, "semantic_"+field.name)
		}
	}
	return conflicts
}

func repositoryNumericAnchorConflict(left, right []string) bool {
	for _, leftAnchor := range left {
		leftStem, leftNumbers := repositoryAnchorParts(leftAnchor)
		if leftStem == "" || len(leftNumbers) == 0 {
			continue
		}
		for _, rightAnchor := range right {
			rightStem, rightNumbers := repositoryAnchorParts(rightAnchor)
			if leftStem == rightStem && len(rightNumbers) > 0 &&
				!repositoryStringSetsIntersect(leftNumbers, rightNumbers) {
				return true
			}
		}
	}
	return false
}

func repositoryTextNumericConflict(left, right string) bool {
	leftTokens, rightTokens := findingTokens(left), findingTokens(right)
	leftNumbers, rightNumbers := repositoryNumericTokens(leftTokens), repositoryNumericTokens(rightTokens)
	if len(leftNumbers) == 0 || len(rightNumbers) == 0 ||
		repositoryStringSetsIntersect(leftNumbers, rightNumbers) {
		return false
	}
	for number := range leftNumbers {
		delete(leftTokens, number)
	}
	for number := range rightNumbers {
		delete(rightTokens, number)
	}
	return tokenDice(leftTokens, rightTokens) >= 0.5
}

func repositoryAnchorParts(value string) (string, map[string]struct{}) {
	value = strings.ToLower(strings.TrimSpace(value))
	numbers := make(map[string]struct{})
	var stem strings.Builder
	var digits strings.Builder
	flushDigits := func() {
		if digits.Len() == 0 {
			return
		}
		numbers[digits.String()] = struct{}{}
		digits.Reset()
		stem.WriteRune('#')
	}
	for _, character := range value {
		if unicode.IsDigit(character) {
			digits.WriteRune(character)
			continue
		}
		flushDigits()
		if unicode.IsLetter(character) || character == '_' || character == '-' || character == '.' {
			stem.WriteRune(character)
		}
	}
	flushDigits()
	return stem.String(), numbers
}

func repositoryNumericTokens(tokens map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{})
	for token := range tokens {
		if token != "" && strings.IndexFunc(token, func(character rune) bool {
			return !unicode.IsDigit(character)
		}) < 0 {
			out[token] = struct{}{}
		}
	}
	return out
}

func repositoryStringSetsIntersect(left, right map[string]struct{}) bool {
	for value := range left {
		if _, ok := right[value]; ok {
			return true
		}
	}
	return false
}

func repositoryAnchorJaccard(left, right []string) float64 {
	leftSet, rightSet := repositoryNormalizedStrings(left), repositoryNormalizedStrings(right)
	if len(leftSet) == 0 || len(rightSet) == 0 {
		return 0
	}
	shared := 0
	union := make(map[string]struct{}, len(leftSet)+len(rightSet))
	for value := range leftSet {
		union[value] = struct{}{}
	}
	for value := range rightSet {
		if _, ok := leftSet[value]; ok {
			shared++
		}
		union[value] = struct{}{}
	}
	return float64(shared) / float64(len(union))
}

func repositorySharedAnchor(left, right []string) bool {
	leftSet := repositoryNormalizedStrings(left)
	for value := range repositoryNormalizedStrings(right) {
		if _, ok := leftSet[value]; ok {
			return true
		}
	}
	return false
}

func repositoryNormalizedStrings(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = normalizedText(value); value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

func repositoryCausalText(hints MatchHints) string {
	return strings.Join([]string{
		hints.Operation,
		hints.FailureMode,
		hints.Trigger,
		hints.ViolatedInvariant,
		hints.ObservableOutcome,
		strings.Join(hints.DistinguishingFacts, " "),
	}, " ")
}

func repositoryReviewFindingSearchText(finding Finding) string {
	return strings.Join([]string{
		finding.Title,
		finding.MatchHints.Component,
		repositoryCausalText(finding.MatchHints),
		finding.Symbol,
		strings.Join(finding.MatchHints.RelatedSymbols, " "),
		strings.Join(finding.MatchHints.SourceAnchors, " "),
	}, " ")
}

func repositoryFindingSearchText(finding RepositoryFinding) string {
	symbols := append([]string(nil), finding.MatchHints.RelatedSymbols...)
	for _, history := range finding.PathSymbolHistory {
		symbols = append(symbols, history.Symbol)
	}
	return strings.Join([]string{
		finding.CanonicalTitle,
		finding.MatchHints.Component,
		repositoryCausalText(finding.MatchHints),
		strings.Join(symbols, " "),
		strings.Join(finding.MatchHints.SourceAnchors, " "),
	}, " ")
}

// repositoryMatchingUniverseFingerprint changes only when evidence visible to
// CPU/AI matching changes. Issue synchronization and lifecycle-only updates do
// not invalidate an in-flight causal adjudication.
func repositoryMatchingUniverseFingerprint(findings []RepositoryFinding) string {
	ordered := append([]RepositoryFinding(nil), findings...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	values := make([]string, 0, len(ordered)*8)
	for _, finding := range ordered {
		values = append(values,
			finding.ID,
			finding.CanonicalTitle,
			string(finding.MatchState),
			finding.MatchHints.Component,
			repositoryCausalText(finding.MatchHints),
			strings.Join(finding.MatchHints.RelatedSymbols, "\x1f"),
			strings.Join(finding.MatchHints.SourceAnchors, "\x1f"),
		)
		for _, history := range finding.PathSymbolHistory {
			values = append(values, history.CommitSHA, history.Path, history.Symbol)
		}
	}
	return stableID("rum_", values...)
}

func repositoryDeterministicScore(candidate RepositoryMatchCandidate) float64 {
	return candidate.CausalDice*4 + candidate.TitleDice*2 + candidate.AnchorJaccard
}

func sortRepositoryMatchCandidates(candidates []RepositoryMatchCandidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		if math.Abs(candidates[i].Score-candidates[j].Score) <= 1e-9 {
			return candidates[i].ID < candidates[j].ID
		}
		return candidates[i].Score > candidates[j].Score
	})
}

func containsRepositoryFinding(findings []RepositoryFinding, id string) bool {
	for _, finding := range findings {
		if finding.ID == id {
			return true
		}
	}
	return false
}

func normalizedRepositoryPath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	for strings.HasPrefix(value, "./") {
		value = strings.TrimPrefix(value, "./")
	}
	return value
}

// ValidateRepositoryMappingAdjudication enforces the closed AI fallback
// result. Candidate IDs are opaque and must be drawn from the supplied set.
func ValidateRepositoryMappingAdjudication(
	result RepositoryMappingAdjudication,
	allowedCandidateIDs []string,
) error {
	result.Decision = strings.ToLower(strings.TrimSpace(result.Decision))
	switch result.Decision {
	case "same", "related", "distinct", "uncertain":
	default:
		return errors.New("invalid repository mapping decision")
	}
	if math.IsNaN(result.Confidence) || math.IsInf(result.Confidence, 0) ||
		result.Confidence < 0 || result.Confidence > 1 {
		return errors.New("invalid repository mapping confidence")
	}
	allowed := make(map[string]struct{}, len(allowedCandidateIDs))
	for _, id := range allowedCandidateIDs {
		allowed[id] = struct{}{}
	}
	if result.Decision == "distinct" {
		if strings.TrimSpace(result.CandidateID) != "" {
			return errors.New("distinct repository mapping must not select a candidate")
		}
	} else if _, ok := allowed[strings.TrimSpace(result.CandidateID)]; !ok {
		return errors.New("repository mapping selected an unknown candidate")
	}
	if len(result.MatchingAnchors) > 32 || len(result.ConflictingAnchors) > 32 ||
		!validOptionalMappingText(result.Explanation, 2048) {
		return errors.New("repository mapping explanation exceeds its bound")
	}
	for _, values := range [][]string{result.MatchingAnchors, result.ConflictingAnchors} {
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			value = strings.TrimSpace(value)
			if !validBoundedText(value, 256) {
				return fmt.Errorf("invalid repository mapping anchor %q", value)
			}
			key := normalizedText(value)
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate repository mapping anchor")
			}
			seen[key] = struct{}{}
		}
	}
	if err := validateRepositoryMappingConflictFields(
		result.ConflictingAnchors,
		result.ConflictFields,
	); err != nil {
		return err
	}
	return nil
}

func normalizeRepositoryMappingConflictFields(values []string) []string {
	if values == nil {
		return nil
	}
	normalized := make([]string, len(values))
	for index, value := range values {
		normalized[index] = strings.ToLower(strings.TrimSpace(value))
	}
	return normalized
}

func validRepositoryMappingConflictField(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case RepositoryMappingConflictFieldSeverity,
		RepositoryMappingConflictFieldTitleWording,
		RepositoryMappingConflictFieldFixEffort,
		RepositoryMappingConflictFieldLifecycleStatus,
		RepositoryMappingConflictFieldCausalIdentity,
		RepositoryMappingConflictFieldLocation,
		RepositoryMappingConflictFieldSymbol,
		RepositoryMappingConflictFieldEvidence,
		RepositoryMappingConflictFieldImpact,
		RepositoryMappingConflictFieldValidationContent,
		RepositoryMappingConflictFieldOther:
		return true
	default:
		return false
	}
}

func validateRepositoryMappingConflictFields(conflicts, fields []string) error {
	// A missing field array is the legacy representation. It remains readable,
	// but the policy below treats every retained legacy conflict as blocking.
	if fields == nil {
		return nil
	}
	if len(fields) != len(conflicts) {
		return errors.New("repository mapping conflict fields are not aligned")
	}
	for _, field := range fields {
		if !validRepositoryMappingConflictField(field) {
			return fmt.Errorf("invalid repository mapping conflict field %q", field)
		}
	}
	return nil
}

func repositoryMappingAdjudicationHasBlockingConflicts(
	adjudication RepositoryMappingAdjudication,
) bool {
	if adjudication.ConflictFields == nil {
		return len(adjudication.ConflictingAnchors) > 0
	}
	if len(adjudication.ConflictFields) != len(adjudication.ConflictingAnchors) {
		return true
	}
	for _, field := range adjudication.ConflictFields {
		switch strings.ToLower(strings.TrimSpace(field)) {
		case RepositoryMappingConflictFieldSeverity,
			RepositoryMappingConflictFieldTitleWording,
			RepositoryMappingConflictFieldFixEffort,
			RepositoryMappingConflictFieldLifecycleStatus:
			continue
		default:
			// Closed blocking classifications and unknown future values both
			// fail closed here, even if validation was accidentally bypassed.
			return true
		}
	}
	return false
}

func repositoryMappingAdjudicationAutoAssociates(
	adjudication RepositoryMappingAdjudication,
) bool {
	return strings.EqualFold(strings.TrimSpace(adjudication.Decision), "same") &&
		adjudication.Confidence >= 0.90 &&
		!repositoryMappingAdjudicationHasBlockingConflicts(adjudication)
}

func validOptionalMappingText(value string, maximum int) bool {
	return value == "" || utf8.ValidString(value) && len(value) <= maximum &&
		!strings.ContainsRune(value, 0)
}
