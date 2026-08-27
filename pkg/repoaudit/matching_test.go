package repoaudit

import (
	"strings"
	"testing"
	"time"
)

func TestRepositoryFindingMatchingAcrossCommitsAndRenames(t *testing.T) {
	base := repositoryMatchingFinding("rfn_old", "old/scheduler.go", "waiter.signal")
	base.CommitSHA = strings.Repeat("a", 40)
	aggregate := repositoryMatchingAggregate("rrf_one", base)

	t.Run("same causal defect after rename", func(t *testing.T) {
		candidate := repositoryMatchingFinding("rfn_new", "core/scheduler.go", "waiter.signal")
		candidate.CommitSHA = strings.Repeat("b", 40)
		result := MatchRepositoryFinding(
			candidate,
			[]RepositoryFinding{aggregate},
			map[string]Finding{base.ID: base},
			func(left, right string) bool {
				return left == right || left == "core/scheduler.go" && right == "old/scheduler.go"
			},
		)
		if result.RepositoryFindingID != aggregate.ID || result.Method != "deterministic" {
			t.Fatalf("match = %#v", result)
		}
	})

	t.Run("same symbol different causal defect", func(t *testing.T) {
		candidate := repositoryMatchingFinding("rfn_distinct", "old/scheduler.go", "waiter.signal")
		candidate.MatchHints.Operation = "delete expired timer entry"
		candidate.MatchHints.FailureMode = "live timers are removed with expired timers"
		candidate.MatchHints.Trigger = "cleanup races with a timer refresh"
		candidate.MatchHints.ViolatedInvariant = "only expired timer generations may be deleted"
		candidate.MatchHints.ObservableOutcome = "a scheduled callback never executes"
		candidate.MatchHints.SourceAnchors = []string{"timer_generation", "deadline"}
		result := MatchRepositoryFinding(candidate, []RepositoryFinding{aggregate}, nil, nil)
		if result.RepositoryFindingID != "" || result.Method != "ai" || len(result.Candidates) != 1 {
			t.Fatalf("different defect collapsed: %#v", result)
		}
	})

	t.Run("numeric anchor conflict", func(t *testing.T) {
		candidate := repositoryMatchingFinding("rfn_numeric", "old/scheduler.go", "waiter.signal")
		candidate.MatchHints.SourceAnchors = []string{"retry_limit_3", "waiters"}
		aggregateWithNumber := aggregate
		aggregateWithNumber.MatchHints.SourceAnchors = []string{"retry_limit_5", "waiters"}
		result := MatchRepositoryFinding(candidate, []RepositoryFinding{aggregateWithNumber}, nil, nil)
		if result.Method != "distinct" || len(result.Candidates) != 0 {
			t.Fatalf("numeric conflict = %#v", result)
		}
	})

	t.Run("one disjoint trigger blocks deterministic merge", func(t *testing.T) {
		candidate := repositoryMatchingFinding("rfn_trigger", "old/scheduler.go", "waiter.signal")
		candidate.MatchHints.Trigger = "shutdown signal arrives before worker startup"
		result := MatchRepositoryFinding(candidate, []RepositoryFinding{aggregate}, nil, nil)
		if result.RepositoryFindingID != "" || result.Method != "ai" || len(result.Candidates) != 1 {
			t.Fatalf("disjoint trigger merged: %#v", result)
		}
	})
}

func TestRepositoryFindingBM25AndAdjudicationValidation(t *testing.T) {
	query := repositoryMatchingFinding("rfn_query", "scheduler.go", "waiter.signal")
	relevant := repositoryMatchingAggregate("rrf_relevant", query)
	unrelatedFinding := repositoryMatchingFinding("rfn_other", "auth.go", "token.parse")
	unrelatedFinding.Title = "Expired credentials bypass authorization"
	unrelatedFinding.MatchHints = MatchHints{
		Component: "authentication", Operation: "parse access token",
		FailureMode: "expiration is not checked", Trigger: "an expired token is supplied",
		ViolatedInvariant: "expired credentials never authorize a request",
		ObservableOutcome: "an unauthorized request succeeds",
		RelatedSymbols:    []string{"token.parse"}, SourceAnchors: []string{"exp", "sub"},
		DistinguishingFacts: []string{"requires an expired signed token"},
	}
	unrelated := repositoryMatchingAggregate("rrf_unrelated", unrelatedFinding)
	ranked := RankRepositoryFindingsBM25(query, []RepositoryFinding{unrelated, relevant}, 10)
	if len(ranked) == 0 || ranked[0].ID != relevant.ID {
		t.Fatalf("BM25 ordering = %#v", ranked)
	}

	valid := RepositoryMappingAdjudication{
		Decision: "same", CandidateID: relevant.ID, Confidence: 0.95,
		MatchingAnchors: []string{"waiters"}, Explanation: "causal identity agrees",
	}
	if err := ValidateRepositoryMappingAdjudication(valid, []string{relevant.ID}); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []RepositoryMappingAdjudication{
		{Decision: "merge", CandidateID: relevant.ID, Confidence: .9},
		{Decision: "same", CandidateID: "rrf_hidden", Confidence: .9},
		{Decision: "same", CandidateID: relevant.ID, Confidence: 1.1},
		{Decision: "distinct", CandidateID: relevant.ID, Confidence: .8},
	} {
		if err := ValidateRepositoryMappingAdjudication(invalid, []string{relevant.ID}); err == nil {
			t.Fatalf("accepted invalid adjudication %#v", invalid)
		}
	}
}

func repositoryMatchingFinding(id, pathValue, symbol string) Finding {
	return Finding{
		ID: id, Fingerprint: "sha256:" + id, Repository: "owner/repo",
		CommitSHA: strings.Repeat("a", 40), File: FileRef{Path: pathValue},
		Title:  "Predicate waiter remains attached to moved condition variable",
		Symbol: symbol,
		MatchHints: MatchHints{
			Component:           "core scheduling",
			Operation:           "requeue predicate waiter after condition variable move",
			FailureMode:         "waiter is attached to the moved from object",
			Trigger:             "move followed by an unsuccessful predicate wakeup",
			ViolatedInvariant:   "every waiter requeues on its current owner",
			ObservableOutcome:   "coroutine remains blocked indefinitely",
			RelatedSymbols:      []string{"waiter.signal", "condition.await"},
			SourceAnchors:       []string{"waiters", "owner", "add_waiter"},
			DistinguishingFacts: []string{"requires a moved condition variable", "predicate remains false"},
		},
	}
}

func repositoryMatchingAggregate(id string, finding Finding) RepositoryFinding {
	return RepositoryFinding{
		ID: id, Repository: finding.Repository, CanonicalTitle: finding.Title,
		MatchHints: finding.MatchHints, ReviewFindingIDs: []string{finding.ID},
		FoundCommits: []string{finding.CommitSHA}, MatchState: RepositoryMatchNew,
		Lifecycle: RepositoryFindingOpen, ValidationState: RepositoryValidationNotRequested,
		PathSymbolHistory: []RepositoryFindingPathSymbol{{
			ReviewFindingID: finding.ID, CommitSHA: finding.CommitSHA,
			Path: finding.File.Path, Symbol: finding.Symbol, ObservedAt: time.Now().UTC(),
		}},
	}
}
