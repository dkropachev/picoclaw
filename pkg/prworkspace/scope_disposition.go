package prworkspace

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

type ScopeDispositionMode string

const (
	ScopeDispositionStrict  ScopeDispositionMode = "strict"
	ScopeDispositionRelaxed ScopeDispositionMode = "relaxed"
)

type ScopeDispositionRule struct {
	Mode   ScopeDispositionMode
	Prompt string
}

type ScopeDispositionPolicy struct {
	Default ScopeDispositionRule
	ByType  map[PRType]ScopeDispositionRule
}

func DefaultScopeDispositionPolicy() ScopeDispositionPolicy {
	return ScopeDispositionPolicy{
		Default: ScopeDispositionRule{Mode: ScopeDispositionStrict},
		ByType:  make(map[PRType]ScopeDispositionRule),
	}
}

func (policy ScopeDispositionPolicy) Rule(prType PRType) ScopeDispositionRule {
	rule := policy.Default
	if rule.Mode == "" {
		rule.Mode = ScopeDispositionStrict
	}
	if selected, ok := policy.ByType[prType]; ok {
		rule = selected
	}
	if rule.Mode != ScopeDispositionRelaxed {
		rule.Mode = ScopeDispositionStrict
	}
	return rule
}

func decideFindingDisposition(
	scope ScopeAssessment,
	finding AgentFinding,
	charter Charter,
	policy ScopeDispositionPolicy,
) FindingDisposition {
	if !scope.TypeCompatible || scope.Distance == ScopeUnrelated {
		return FindingDeferred
	}
	// Infrastructure work is deliberately exceptional. Even when a model calls
	// it exact or tiny, it stays out of the current change unless the confirmed
	// charter explicitly authorized that class of work.
	if infrastructureFinding(finding) && !charterExplicitlyTargetsInfrastructure(charter) {
		return FindingDeferred
	}
	if DecideScope(scope) == ScopeActionProceed {
		return FindingInScope
	}
	// Exact charter work that exceeds the small-change bound cannot simply be
	// dropped by a deferral preference; it needs the ordinary scope/size gate.
	if scope.Distance == ScopeExact {
		return FindingOpen
	}
	rule := policy.Rule(charter.Type)
	if rule.Mode != ScopeDispositionRelaxed || scope.Confidence < 0.80 ||
		(scope.Size != ChangeSizeXS && scope.Size != ChangeSizeS) {
		return FindingDeferred
	}
	if scope.Distance != ScopeNecessaryAdjacent && scope.Distance != ScopeRelatedFollowup {
		return FindingDeferred
	}
	return FindingInScope
}

func infrastructureFinding(finding AgentFinding) bool {
	value := strings.ToLower(strings.Join([]string{
		finding.File, finding.Title, finding.Message, finding.ScopeExplanation,
	}, " "))
	for _, marker := range []string{
		".github/workflows/", "ci/cd", "cicd", "pipeline", "deployment", "deploy",
		"release", "dependency upgrade", "dependencies", "migration", "generated code",
		"broad cleanup",
	} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func charterExplicitlyTargetsInfrastructure(charter Charter) bool {
	values := make([]string, 1, 1+len(charter.AcceptanceCriteria)+len(charter.IncludedAreas))
	values[0] = charter.Goal
	values = append(values, charter.AcceptanceCriteria...)
	values = append(values, charter.IncludedAreas...)
	joined := strings.ToLower(strings.Join(values, " "))
	for _, marker := range []string{
		"ci/cd", "cicd", "pipeline", "deployment", "release", "dependency", "migration", "generated",
	} {
		if strings.Contains(joined, marker) {
			return true
		}
	}
	return false
}

func scopeDispositionEvidence(rule ScopeDispositionRule, prType PRType) (string, string) {
	promptDigest := ""
	if rule.Prompt != "" {
		digest := sha256.Sum256([]byte(rule.Prompt))
		promptDigest = "sha256:" + hex.EncodeToString(digest[:])
	}
	revision := sha256.Sum256([]byte(strings.Join([]string{
		"picoclaw-scope-disposition-v1", string(prType), string(rule.Mode), promptDigest,
	}, "\x00")))
	return "sha256:" + hex.EncodeToString(revision[:]), promptDigest
}
