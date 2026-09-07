package repoaudit

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRepositoryMatchingBoundaryCoverage(t *testing.T) {
	if !repositorySharedAnchor([]string{" Queue ID "}, []string{"queue id"}) ||
		repositorySharedAnchor([]string{"one"}, []string{"two"}) {
		t.Fatal("shared anchor normalization mismatch")
	}
	for _, test := range []struct {
		name        string
		left, right string
		want        bool
	}{
		{name: "no numbers", left: "retry queue", right: "retry queue", want: false},
		{name: "shared number", left: "retry queue 10", right: "retry queue 10", want: false},
		{name: "different mechanism", left: "retry queue 10", right: "close socket 20", want: false},
		{name: "numeric conflict", left: "retry queue slot 10", right: "retry queue slot 20", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := repositoryTextNumericConflict(test.left, test.right); got != test.want {
				t.Fatalf("numeric conflict = %v, want %v", got, test.want)
			}
		})
	}

	anchorOnly := Finding{MatchHints: MatchHints{SourceAnchors: []string{"generation"}}}
	aggregate := RepositoryFinding{
		ID: "rrf_candidate", MatchHints: MatchHints{SourceAnchors: []string{"generation"}},
	}
	metrics := repositoryMatchMetrics(anchorOnly, aggregate, func(string, string) bool { return false })
	if !repositoryMatchPrefilter(anchorOnly, aggregate, metrics) {
		t.Fatal("shared-anchor-only candidate was filtered")
	}
	metrics.HardConflict = true
	if repositoryMatchPrefilter(anchorOnly, aggregate, metrics) ||
		repositoryMatchPrefilter(anchorOnly, RepositoryFinding{}, RepositoryMatchCandidate{}) {
		t.Fatal("invalid or conflicting candidate passed prefilter")
	}

	related := RepositoryFinding{MatchHints: MatchHints{RelatedSymbols: []string{"Scheduler.Resume"}}}
	if !repositoryFindingSharesSymbol(
		Finding{MatchHints: MatchHints{RelatedSymbols: []string{"scheduler.resume"}}},
		related,
	) ||
		repositoryFindingHasSymbol(related, "", true) ||
		repositoryFindingHasSymbol(related, "Scheduler.Resume", false) {
		t.Fatal("related symbol matching boundary mismatch")
	}

	base := Finding{
		Title: "waiter remains blocked", Symbol: "Scheduler.Run", File: FileRef{Path: "queue.go"},
		MatchHints: MatchHints{
			Component: "scheduler", Operation: "requeue failed waiter", FailureMode: "stale queue owner",
			Trigger: "failed predicate wake", ViolatedInvariant: "waiter uses current queue owner",
			ObservableOutcome: "waiter remains blocked", SourceAnchors: []string{"waiters"},
		},
	}
	makeCandidate := func(id string) RepositoryFinding {
		return RepositoryFinding{
			ID: id, CanonicalTitle: base.Title, MatchHints: base.MatchHints,
			PathSymbolHistory: []RepositoryFindingPathSymbol{{Path: "queue.go", Symbol: base.Symbol}},
		}
	}
	// Equal deterministic evidence must be sent to adjudication instead of
	// whichever aggregate happens to appear first.
	tied := MatchRepositoryFinding(base, []RepositoryFinding{
		makeCandidate("rrf_b"), makeCandidate("rrf_a"),
	}, nil, nil)
	if tied.Method != "ai" || len(tied.Candidates) != 2 || tied.Candidates[0].ID != "rrf_a" {
		t.Fatalf("ambiguous deterministic match = %#v", tied)
	}
	if got := RankRepositoryFindingsBM25(base, []RepositoryFinding{makeCandidate("rrf_a")}, -1); len(got) != 1 {
		t.Fatalf("default BM25 limit result = %#v", got)
	}
	if normalizedRepositoryPath(" ././dir\\file.go ") != "dir/file.go" {
		t.Fatal("repository path normalization mismatch")
	}

	allowed := []string{"candidate_1"}
	tooMany := RepositoryMappingAdjudication{Decision: "same", CandidateID: "candidate_1", Confidence: .9}
	tooMany.MatchingAnchors = make([]string, 33)
	invalid := []RepositoryMappingAdjudication{
		{Decision: "merge", Confidence: .9},
		{Decision: "same", CandidateID: "candidate_1", Confidence: math.NaN()},
		{Decision: "same", CandidateID: "unknown", Confidence: .9},
		{Decision: "distinct", CandidateID: "candidate_1", Confidence: .9},
		{Decision: "same", CandidateID: "candidate_1", Confidence: .9, Explanation: strings.Repeat("x", 2049)},
		{Decision: "same", CandidateID: "candidate_1", Confidence: .9, MatchingAnchors: []string{"a", " A "}},
		{Decision: "same", CandidateID: "candidate_1", Confidence: .9, MatchingAnchors: []string{""}},
		tooMany,
	}
	for index, result := range invalid {
		if err := ValidateRepositoryMappingAdjudication(result, allowed); err == nil {
			t.Errorf("invalid adjudication %d accepted", index)
		}
	}
}

//nolint:govet // Boundary probes intentionally reuse short-lived result names across sequential checks.
func TestRepositoryLifecycleQueueMutationBoundaryCoverage(t *testing.T) {
	store := NewStore(t.TempDir())
	state, occurrence := recordLifecycleFinding(
		t, store, strings.Repeat("1", 40), strings.Repeat("2", 40), "queue-boundaries",
		"main", "main", true, "queue boundary defect",
	)
	job := lifecycleJobForFinding(t, state, occurrence.ID)
	snapshot := RepositoryMappingModelSnapshot{
		ProfileID: "rrpf_boundary", ProfileVersion: 1, Model: "reviewer", Account: "account",
	}
	if _, _, _, claimed, err := store.ClaimMappingJob(
		state.Repository,
		"missing",
		snapshot,
	); !errors.Is(err, os.ErrNotExist) ||
		claimed {
		t.Fatalf("missing mapping claim claimed=%v err=%v", claimed, err)
	}
	if _, _, err := store.SaveMappingAdjudication(
		state.Repository, job.ID, RepositoryMappingAdjudication{Decision: "distinct"}, "one", "two",
	); err == nil {
		t.Fatal("multiple candidate universes accepted")
	}
	if _, _, err := store.SaveMappingAdjudication(
		state.Repository, job.ID, RepositoryMappingAdjudication{Decision: "distinct"}, strings.Repeat("u", 129),
	); err == nil {
		t.Fatal("oversized candidate universe accepted")
	}
	if _, _, err := store.SaveMappingAdjudication(
		state.Repository, "missing", RepositoryMappingAdjudication{Decision: "distinct"},
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing adjudication save error = %v", err)
	}
	if _, _, err := store.SaveMappingAdjudication(
		state.Repository, job.ID, RepositoryMappingAdjudication{Decision: "distinct"},
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("pending adjudication save error = %v", err)
	}

	_, running, _, claimed, err := store.ClaimMappingJob(state.Repository, job.ID, snapshot)
	if err != nil || !claimed {
		t.Fatalf("mapping claim=%v err=%v", claimed, err)
	}
	if _, sameRunning, _, claimedAgain, err := store.ClaimMappingJob(
		state.Repository,
		job.ID,
		snapshot,
	); err != nil || claimedAgain ||
		sameRunning.State != RepositoryMappingRunning {
		t.Fatalf("running replay=%#v claimed=%v err=%v", sameRunning, claimedAgain, err)
	}
	adjudication := RepositoryMappingAdjudication{
		Decision:    "distinct",
		Confidence:  .99,
		Explanation: "independent causal path",
	}
	state, saved, err := store.SaveMappingAdjudication(state.Repository, running.ID, adjudication)
	if err != nil || saved.CandidateUniverse == "" {
		t.Fatalf("saved adjudication=%#v err=%v", saved, err)
	}
	if _, replay, err := store.SaveMappingAdjudication(
		state.Repository,
		running.ID,
		adjudication,
		saved.CandidateUniverse,
	); err != nil ||
		replay.CandidateUniverse != saved.CandidateUniverse {
		t.Fatalf("adjudication replay=%#v err=%v", replay, err)
	}
	if _, _, err := store.SaveMappingAdjudication(
		state.Repository,
		running.ID,
		RepositoryMappingAdjudication{Decision: "distinct", Confidence: .7},
		saved.CandidateUniverse,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting adjudication error = %v", err)
	}
	if _, _, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: running.ID, CreateMatchState: RepositoryMatchProvisional,
		DefaultBranchVerified: true, ExpectedUniverse: saved.CandidateUniverse,
	}); err == nil {
		t.Fatal("completion contradicting distinct adjudication accepted")
	}
	state, aggregate, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: running.ID, CreateMatchState: RepositoryMatchNew,
		DefaultBranchVerified: true, ExpectedUniverse: saved.CandidateUniverse,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, replay, err := store.SaveMappingAdjudication(
		state.Repository,
		running.ID,
		adjudication,
		saved.CandidateUniverse,
	); err != nil ||
		replay.State != RepositoryMappingCompleted {
		t.Fatalf("completed adjudication replay=%#v err=%v", replay, err)
	}
	if _, _, err := store.SaveMappingAdjudication(
		state.Repository,
		running.ID,
		RepositoryMappingAdjudication{Decision: "distinct", Confidence: .1},
		saved.CandidateUniverse,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("completed adjudication conflict error = %v", err)
	}
	if _, completedJob, _, claimed, err := store.ClaimMappingJob(
		state.Repository,
		running.ID,
		snapshot,
	); err != nil || claimed ||
		completedJob.State != RepositoryMappingCompleted {
		t.Fatalf("completed mapping claim=%#v claimed=%v err=%v", completedJob, claimed, err)
	}

	for index, completion := range []RepositoryMappingCompletion{
		{},
		{JobID: "job"},
		{JobID: "job", RepositoryFindingID: "rrf", CreateMatchState: RepositoryMatchNew},
		{JobID: "job", CreateMatchState: RepositoryMatchNew, RegressionVerified: true},
		{JobID: "job", CreateMatchState: RepositoryMatchNew, RegressionFixCommit: "bad", RegressionFindingID: "rrf"},
	} {
		if _, _, err := store.CompleteMappingJob(state.Repository, completion); err == nil {
			t.Errorf("invalid mapping completion %d accepted", index)
		}
	}
	if _, _, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: "missing", CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing completion job error = %v", err)
	}

	for index, request := range []RepositoryDuplicateResolution{
		{},
		{ProvisionalID: aggregate.ID, CandidateID: aggregate.ID, Decision: "merge", ExpectedProvisionalVersion: 1},
		{ProvisionalID: "one", CandidateID: "two", Decision: "invalid", ExpectedProvisionalVersion: 1},
	} {
		if _, _, err := store.ResolvePossibleDuplicate(state.Repository, request); err == nil {
			t.Errorf("invalid duplicate resolution %d accepted", index)
		}
	}
	if _, _, err := store.ResolvePossibleDuplicate(state.Repository, RepositoryDuplicateResolution{
		ProvisionalID: "missing", CandidateID: aggregate.ID, Decision: "distinct", ExpectedProvisionalVersion: 1,
	}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing duplicate resolution error = %v", err)
	}
	if _, _, err := store.ResolvePossibleDuplicate(state.Repository, RepositoryDuplicateResolution{
		ProvisionalID:              aggregate.ID,
		CandidateID:                "missing",
		Decision:                   "distinct",
		ExpectedProvisionalVersion: aggregate.Version,
	}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing duplicate candidate error = %v", err)
	}

	if _, _, err := store.ReserveValidationJobs(
		state.Repository,
		[]string{" "},
		RepositoryMappingModelSnapshot{},
	); err == nil {
		t.Fatal("blank validation target accepted")
	}
	if _, _, err := store.ReserveValidationJobs(
		state.Repository,
		[]string{aggregate.ID, aggregate.ID},
		RepositoryMappingModelSnapshot{},
	); err == nil {
		t.Fatal("duplicate validation target accepted")
	}
	if _, _, err := store.ReserveValidationJobs(
		state.Repository,
		[]string{aggregate.ID},
		RepositoryMappingModelSnapshot{ProfileID: "bad"},
	); err == nil {
		t.Fatal("invalid validation snapshot accepted")
	}
	if _, _, err := store.ReserveValidationJobs(
		state.Repository,
		[]string{"missing"},
		RepositoryMappingModelSnapshot{},
	); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("missing validation target error = %v", err)
	}
	state, validationJobs, err := store.ReserveValidationJobs(state.Repository, []string{aggregate.ID}, snapshot)
	if err != nil || len(validationJobs) != 1 {
		t.Fatalf("validation reservation=%#v err=%v", validationJobs, err)
	}
	if _, replayJobs, err := store.ReserveValidationJobs(
		state.Repository,
		[]string{aggregate.ID},
		snapshot,
	); err != nil || len(replayJobs) != 1 ||
		replayJobs[0].ID != validationJobs[0].ID {
		t.Fatalf("validation reservation replay=%#v err=%v", replayJobs, err)
	}
	if _, _, _, claimed, err := store.ClaimValidationJob(
		state.Repository,
		"missing",
	); !errors.Is(err, os.ErrNotExist) ||
		claimed {
		t.Fatalf("missing validation claim claimed=%v err=%v", claimed, err)
	}
	if _, _, err := store.SetValidationJobCandidates(
		state.Repository,
		"missing",
		[]string{strings.Repeat("3", 40)},
	); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("missing validation candidates error = %v", err)
	}
	if _, _, err := store.SetValidationJobCandidates(
		state.Repository,
		validationJobs[0].ID,
		[]string{strings.Repeat("3", 40)},
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("pending validation candidates error = %v", err)
	}
	state, validation, _, claimed, err := store.ClaimValidationJob(state.Repository, validationJobs[0].ID)
	if err != nil || !claimed {
		t.Fatalf("validation claim=%v err=%v", claimed, err)
	}
	if _, replay, _, claimed, err := store.ClaimValidationJob(
		state.Repository,
		validation.ID,
	); err != nil || claimed ||
		replay.State != RepositoryValidationRunning {
		t.Fatalf("running validation replay=%#v claimed=%v err=%v", replay, claimed, err)
	}
	commit := strings.Repeat("3", 40)
	state, validation, err = store.SetValidationJobCandidates(state.Repository, validation.ID, []string{commit})
	if err != nil {
		t.Fatal(err)
	}
	if _, replay, err := store.SetValidationJobCandidates(
		state.Repository,
		validation.ID,
		[]string{strings.ToUpper(commit)},
	); err != nil ||
		!slices.Equal(replay.CandidateCommits, []string{commit}) {
		t.Fatalf("validation candidate replay=%#v err=%v", replay, err)
	}
	if _, _, err := store.SetValidationJobCandidates(
		state.Repository,
		validation.ID,
		[]string{strings.Repeat("4", 40)},
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("conflicting validation candidates error = %v", err)
	}
	if _, _, _, err := store.CompleteValidationJob(state.Repository, RepositoryValidationCompletion{
		JobID: validation.ID, Outcome: RepositoryValidationConfirmed,
	}); err == nil {
		t.Fatal("incomplete confirmed validation accepted")
	}
	state, aggregate, validation, err = store.CompleteValidationJob(state.Repository, RepositoryValidationCompletion{
		JobID:   validation.ID,
		Outcome: RepositoryValidationNotFixed,
		Error:   "provider detail",
		Summary: "still reproducible",
	})
	if err != nil || validation.State != RepositoryValidationNotFixed || validation.Error != "Validation failed." {
		t.Fatalf("validation completion finding=%#v job=%#v err=%v", aggregate, validation, err)
	}
	if _, _, replay, err := store.CompleteValidationJob(state.Repository, RepositoryValidationCompletion{
		JobID: validation.ID, Outcome: RepositoryValidationNotFixed,
	}); err != nil || replay.State != RepositoryValidationNotFixed {
		t.Fatalf("validation completion replay=%#v err=%v", replay, err)
	}
	if _, _, _, err := store.CompleteValidationJob(state.Repository, RepositoryValidationCompletion{
		JobID: validation.ID, Outcome: RepositoryValidationInconclusive,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting validation replay error = %v", err)
	}
	if _, _, _, err := store.CompleteValidationJob(state.Repository, RepositoryValidationCompletion{
		JobID: "missing", Outcome: RepositoryValidationNotFixed,
	}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing validation completion error = %v", err)
	}
}

//nolint:govet // Table-driven mutation probes intentionally reuse short-lived error names.
func TestRepositoryLifecycleStateValidationRejectsBrokenTopology(t *testing.T) {
	store := NewStore(t.TempDir())
	state, occurrence := recordLifecycleFinding(
		t, store, strings.Repeat("5", 40), strings.Repeat("6", 40), "validation-topology",
		"main", "main", true, "topology validation defect",
	)
	mapping := lifecycleJobForFinding(t, state, occurrence.ID)
	mapping = claimLifecycleMappingJob(t, store, state.Repository, mapping, RepositoryMappingModelSnapshot{})
	state, aggregate, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: mapping.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateRepositoryLifecycleState(state); err != nil {
		t.Fatalf("valid mapped state rejected: %v", err)
	}
	clone := func(source RepositoryState) RepositoryState {
		t.Helper()
		data, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		var result RepositoryState
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	assertInvalid := func(name string, source RepositoryState, mutate func(*RepositoryState)) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			broken := clone(source)
			mutate(&broken)
			if err := validateRepositoryLifecycleState(broken); err == nil {
				t.Fatalf("broken lifecycle topology accepted: %#v", broken)
			}
		})
	}
	now := state.UpdatedAt
	otherID := "rrf_" + strings.Repeat("c", 64)
	conflictURL := "https://github.com/owner/repo/issues/1"

	assertInvalid("aggregate shape", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].CanonicalTitle = ""
	})
	assertInvalid("duplicate aggregate ID", state, func(s *RepositoryState) {
		s.RepositoryFindings = append(s.RepositoryFindings, s.RepositoryFindings[0])
	})
	assertInvalid("invalid aggregate hints", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].MatchHints.Operation = ""
	})
	assertInvalid("invalid aggregate effort", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].FixEffort.Quality = FixEffortEstimate{}
	})
	assertInvalid("missing occurrence", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].ReviewFindingIDs = []string{"rdf_missing"}
	})
	assertInvalid("duplicate occurrence", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].ReviewFindingIDs = append(
			s.RepositoryFindings[0].ReviewFindingIDs,
			s.RepositoryFindings[0].ReviewFindingIDs[0],
		)
	})
	assertInvalid("invalid found commit", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].FoundCommits = append(s.RepositoryFindings[0].FoundCommits, "")
	})
	assertInvalid("duplicate found commit", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].FoundCommits = append(
			s.RepositoryFindings[0].FoundCommits,
			s.RepositoryFindings[0].FoundCommits[0],
		)
	})
	assertInvalid("invalid path symbol history", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].PathSymbolHistory[0].ReviewFindingID = "rdf_missing"
	})
	assertInvalid("invalid possible duplicate", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].PossibleDuplicates = []RepositoryFindingPossibleDuplicate{{
			CandidateID: otherID, Relation: "wrong", Confidence: .5, CreatedAt: now,
		}}
	})
	assertInvalid("provisional without ambiguity", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].MatchState = RepositoryMatchProvisional
		s.Findings[0].RepositoryMatchState = RepositoryMatchProvisional
	})
	assertInvalid("final with ambiguity", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].PossibleDuplicates = []RepositoryFindingPossibleDuplicate{{
			CandidateID: otherID, Relation: "uncertain", Confidence: .5, CreatedAt: now,
		}}
	})
	assertInvalid("partial issue identity", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].Issue = RepositoryFindingIssueAssociation{
			State: RepositoryFindingIssueOpen, ExternalID: "1",
		}
	})
	assertInvalid("short issue conflict", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].Issue = RepositoryFindingIssueAssociation{
			State: RepositoryFindingIssueNone, Conflict: true, ConflictURLs: []string{conflictURL},
		}
	})
	assertInvalid("invalid issue origin", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].Issue.Origin = IssueDraftOrigin("untrusted")
	})
	assertInvalid("invalid conflict URL", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].Issue.ConflictURLs = []string{"not-a-url"}
	})
	assertInvalid("duplicate conflict URL", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].Issue.ConflictURLs = []string{conflictURL, conflictURL}
	})
	assertInvalid("possible duplicate missing time", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].PossibleDuplicates = []RepositoryFindingPossibleDuplicate{{
			CandidateID: otherID, Relation: "related", Confidence: .5,
		}}
	})
	assertInvalid("invalid resolution history", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].ResolutionHistory = []RepositoryFindingResolution{{
			Outcome: RepositoryValidationPending, ValidatedAt: now,
		}}
	})
	assertInvalid("invalid confirmed resolution", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].ResolutionHistory = []RepositoryFindingResolution{{
			Outcome: RepositoryValidationConfirmed, ValidatedAt: now,
			FixCommitSHA: "bad", FixCommitTime: now,
		}}
	})
	assertInvalid("non-confirmed fix metadata", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].ResolutionHistory = []RepositoryFindingResolution{{
			Outcome: RepositoryValidationNotFixed, ValidatedAt: now,
			FixCommitSHA: strings.Repeat("a", 40), FixCommitTime: now,
		}}
	})
	assertInvalid("resolved without fix", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].Lifecycle = RepositoryFindingResolved
	})
	assertInvalid("invalid containing tag", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].FirstContainingTag = "release-one"
	})
	assertInvalid("invalid regression proof", state, func(s *RepositoryState) {
		s.Findings[0].PostResolutionVerified = true
	})
	assertInvalid("unassociated match state", state, func(s *RepositoryState) {
		s.RepositoryFindings = nil
		s.MappingJobs = nil
		s.Findings[0].RepositoryFindingID = ""
	})
	assertInvalid("review association mismatch", state, func(s *RepositoryState) {
		s.Findings[0].RepositoryMatchState = RepositoryMatchProvisional
	})
	assertInvalid("self possible duplicate", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].PossibleDuplicates = []RepositoryFindingPossibleDuplicate{{
			CandidateID: aggregate.ID, Relation: "related", Confidence: .5, CreatedAt: now,
		}}
	})
	assertInvalid("missing possible duplicate target", state, func(s *RepositoryState) {
		s.RepositoryFindings[0].PossibleDuplicates = []RepositoryFindingPossibleDuplicate{{
			CandidateID: otherID, Relation: "related", Confidence: .5, CreatedAt: now,
		}}
	})
	assertInvalid("invalid mapping job", state, func(s *RepositoryState) {
		s.MappingJobs[0].Attempts = -1
	})
	assertInvalid("mapping job missing occurrence", state, func(s *RepositoryState) {
		s.MappingJobs[0].ReviewFindingID = "rdf_missing"
		s.MappingJobs[0].ID = mappingJobID("rdf_missing")
	})
	assertInvalid("duplicate mapping job", state, func(s *RepositoryState) {
		s.MappingJobs = append(s.MappingJobs, s.MappingJobs[0])
	})
	assertInvalid("invalid mapping reservation", state, func(s *RepositoryState) {
		s.MappingJobs[0].ReservedAt = now
	})
	assertInvalid("invalid mapping model", state, func(s *RepositoryState) {
		s.MappingJobs[0].ModelSnapshot = RepositoryMappingModelSnapshot{ProfileID: "profile"}
	})
	assertInvalid("adjudication missing universe", state, func(s *RepositoryState) {
		s.MappingJobs[0].Adjudication = RepositoryMappingAdjudication{Decision: "distinct", Confidence: .9}
		s.MappingJobs[0].CandidateUniverse = ""
	})
	assertInvalid("completed mapping association", state, func(s *RepositoryState) {
		s.MappingJobs[0].RepositoryFindingID = ""
	})

	validationState, validationJobs, err := store.ReserveValidationJobs(
		state.Repository, []string{aggregate.ID}, RepositoryMappingModelSnapshot{},
	)
	if err != nil || len(validationJobs) != 1 {
		t.Fatalf("validation state setup=%#v err=%v", validationJobs, err)
	}
	if err := validateRepositoryLifecycleState(validationState); err != nil {
		t.Fatalf("valid validation state rejected: %v", err)
	}
	assertInvalid("invalid validation job", validationState, func(s *RepositoryState) {
		s.ValidationJobs[0].ID = "invalid"
	})
	assertInvalid("validation job missing aggregate", validationState, func(s *RepositoryState) {
		s.ValidationJobs[0].RepositoryFindingID = otherID
	})
	assertInvalid("duplicate validation job", validationState, func(s *RepositoryState) {
		s.ValidationJobs = append(s.ValidationJobs, s.ValidationJobs[0])
	})
	assertInvalid("invalid validation reservation", validationState, func(s *RepositoryState) {
		s.ValidationJobs[0].ReservedAt = now
	})
	assertInvalid("invalid validation model", validationState, func(s *RepositoryState) {
		s.ValidationJobs[0].ModelSnapshot = RepositoryMappingModelSnapshot{ProfileID: "profile"}
	})
	assertInvalid("duplicate validation commit", validationState, func(s *RepositoryState) {
		commit := strings.Repeat("d", 40)
		s.ValidationJobs[0].CandidateCommits = []string{commit, commit}
	})
}

//nolint:govet // Boundary probes intentionally reuse short-lived result names across sequential checks.
func TestResolvePossibleDuplicateRewritesDependentTopologyAndRegression(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	clock := repositoryAuditTestNow
	store.now = func() time.Time { return clock }
	state, first := recordLifecycleFinding(
		t, store, strings.Repeat("1", 40), strings.Repeat("a", 40), "merge-target",
		"main", "main", true, "canonical merge target",
	)
	job := claimLifecycleMappingJob(
		t, store, state.Repository, lifecycleJobForFinding(t, state, first.ID), RepositoryMappingModelSnapshot{},
	)
	state, target, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: job.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, validationJobs, err := store.ReserveValidationJobs(
		state.Repository, []string{target.ID}, RepositoryMappingModelSnapshot{},
	)
	if err != nil {
		t.Fatal(err)
	}
	state, validation, _, claimed, err := store.ClaimValidationJob(state.Repository, validationJobs[0].ID)
	if err != nil || !claimed {
		t.Fatalf("target validation claim=%v err=%v", claimed, err)
	}
	fix := strings.Repeat("f", 40)
	state, validation, err = store.SetValidationJobCandidates(state.Repository, validation.ID, []string{fix})
	if err != nil {
		t.Fatal(err)
	}
	state, target, _, err = store.CompleteValidationJob(state.Repository, RepositoryValidationCompletion{
		JobID: validation.ID, Outcome: RepositoryValidationConfirmed,
		SelectedCommitSHA: fix, FixCommitTime: clock.Add(-time.Hour), Summary: "confirmed",
	})
	if err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(time.Minute)
	state, second := recordLifecycleFinding(
		t, store, strings.Repeat("2", 40), strings.Repeat("b", 40), "merge-provisional",
		"main", "main", true, "moved ambiguous defect", MatchHints{
			Component: "scheduler", Operation: "resume migrated waiter", FailureMode: "stale generation owner",
			Trigger: "retry after queue migration", ViolatedInvariant: "waiters use active generation",
			ObservableOutcome: "waiter remains blocked", RelatedSymbols: []string{"Scheduler.Resume"},
			SourceAnchors: []string{"generation"}, DistinguishingFacts: []string{"requires migration"},
		},
	)
	secondJob := claimLifecycleMappingJob(
		t, store, state.Repository, lifecycleJobForFinding(t, state, second.ID), RepositoryMappingModelSnapshot{},
	)
	secondAdjudication := RepositoryMappingAdjudication{
		Decision: "uncertain", CandidateID: target.ID, Confidence: .78,
		MatchingAnchors: []string{"generation"}, Explanation: "moved code needs manual decision",
	}
	state, secondJob, err = store.SaveMappingAdjudication(state.Repository, secondJob.ID, secondAdjudication)
	if err != nil {
		t.Fatal(err)
	}
	state, provisional, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: secondJob.ID, CreateMatchState: RepositoryMatchProvisional,
		DefaultBranchVerified: true, RegressionVerified: true,
		RegressionFixCommit: fix, RegressionFindingID: target.ID,
		ExpectedUniverse: secondJob.CandidateUniverse,
		PossibleDuplicates: []RepositoryFindingPossibleDuplicate{{
			CandidateID: target.ID, Relation: "uncertain", Confidence: .78,
			MatchingAnchors: []string{"generation"}, Explanation: "moved code needs manual decision",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(time.Minute)
	state, third := recordLifecycleFinding(
		t, store, strings.Repeat("3", 40), strings.Repeat("c", 40), "merge-dependent",
		"main", "main", true, "dependent ambiguous defect", MatchHints{
			Component: "scheduler", Operation: "transfer predicate owner", FailureMode: "old owner retained",
			Trigger: "second migration", ViolatedInvariant: "predicate uses current owner",
			ObservableOutcome: "predicate never resumes", RelatedSymbols: []string{"Predicate.Transfer"},
			SourceAnchors: []string{"generation"}, DistinguishingFacts: []string{"second migration"},
		},
	)
	thirdJob := claimLifecycleMappingJob(
		t, store, state.Repository, lifecycleJobForFinding(t, state, third.ID), RepositoryMappingModelSnapshot{},
	)
	thirdAdjudication := RepositoryMappingAdjudication{
		Decision: "uncertain", CandidateID: provisional.ID, Confidence: .66,
		Explanation: "depends on provisional diagnosis",
	}
	state, thirdJob, err = store.SaveMappingAdjudication(state.Repository, thirdJob.ID, thirdAdjudication)
	if err != nil {
		t.Fatal(err)
	}
	state, dependent, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: thirdJob.ID, CreateMatchState: RepositoryMatchProvisional,
		DefaultBranchVerified: true, ExpectedUniverse: thirdJob.CandidateUniverse,
		PossibleDuplicates: []RepositoryFindingPossibleDuplicate{
			{CandidateID: provisional.ID, Relation: "uncertain", Confidence: .66},
			{CandidateID: target.ID, Relation: "related", Confidence: .55},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Install a restart-safe validation reservation aimed at the provisional
	// aggregate, plus reciprocal ambiguity, to exercise every dependent rewrite.
	provisionalIndex := repositoryFindingIndexByID(state.RepositoryFindings, provisional.ID)
	dependentIndex := repositoryFindingIndexByID(state.RepositoryFindings, dependent.ID)
	state.RepositoryFindings[provisionalIndex].PossibleDuplicates = append(
		state.RepositoryFindings[provisionalIndex].PossibleDuplicates,
		RepositoryFindingPossibleDuplicate{
			CandidateID: dependent.ID, Relation: "uncertain", Confidence: .61, CreatedAt: clock,
		},
	)
	// Preserve this order so provisional->target rewriting encounters the
	// already-known target and removes the duplicate relation.
	state.RepositoryFindings[dependentIndex].PossibleDuplicates = []RepositoryFindingPossibleDuplicate{
		{CandidateID: target.ID, Relation: "related", Confidence: .55, CreatedAt: clock},
		{CandidateID: provisional.ID, Relation: "uncertain", Confidence: .66, CreatedAt: clock},
	}
	state.RepositoryFindings[provisionalIndex].ValidationState = RepositoryValidationPending
	state.RepositoryFindings[provisionalIndex].Version++
	manualValidation := RepositoryValidationJob{
		ID: stableID("rvj_", provisional.ID, "dependent"), RepositoryFindingID: provisional.ID,
		State:          RepositoryValidationPending,
		FindingVersion: state.RepositoryFindings[provisionalIndex].Version,
		CreatedAt:      clock, UpdatedAt: clock,
	}
	state.ValidationJobs = append(state.ValidationJobs, manualValidation)
	state.Version++
	state.UpdatedAt = clock
	if err := store.save(&state); err != nil {
		t.Fatal(err)
	}

	state, merged, err := store.ResolvePossibleDuplicate(state.Repository, RepositoryDuplicateResolution{
		ProvisionalID:              provisional.ID,
		CandidateID:                target.ID,
		Decision:                   "merge",
		ExpectedProvisionalVersion: state.RepositoryFindings[provisionalIndex].Version,
		ExpectedCandidateVersion:   state.RepositoryFindings[repositoryFindingIndexByID(state.RepositoryFindings, target.ID)].Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if repositoryFindingIndexByID(state.RepositoryFindings, provisional.ID) >= 0 ||
		merged.Lifecycle != RepositoryFindingRegressed ||
		merged.ValidationState != RepositoryValidationNotRequested ||
		merged.MatchState != RepositoryMatchProvisional ||
		!repositoryFindingHasPossibleDuplicate(merged, dependent.ID) {
		t.Fatalf("merged target = %#v", merged)
	}
	dependent = state.RepositoryFindings[repositoryFindingIndexByID(state.RepositoryFindings, dependent.ID)]
	if dependent.MatchState != RepositoryMatchNew || len(dependent.PossibleDuplicates) != 1 ||
		dependent.PossibleDuplicates[0].CandidateID != target.ID {
		t.Fatalf("rewritten dependent = %#v", dependent)
	}
	thirdOccurrence := state.Findings[findingIndexByID(state.Findings, third.ID)]
	if thirdOccurrence.RepositoryMatchState != RepositoryMatchNew {
		t.Fatalf("dependent occurrence = %#v", thirdOccurrence)
	}
	thirdJob = lifecycleJobForFinding(t, state, third.ID)
	if thirdJob.Adjudication.CandidateID != target.ID {
		t.Fatalf("rewritten mapping adjudication = %#v", thirdJob)
	}
	if state.ValidationJobs[len(state.ValidationJobs)-1].RepositoryFindingID != target.ID {
		t.Fatalf("rewritten validation job = %#v", state.ValidationJobs[len(state.ValidationJobs)-1])
	}

	clock = clock.Add(time.Minute)
	state, fourth := recordLifecycleFinding(
		t, store, strings.Repeat("4", 40), strings.Repeat("d", 40), "merge-later",
		"main", "main", true, "later unrelated aggregate",
		MatchHints{
			Component: "token decoder", Operation: "verify checksum",
			FailureMode: "accepts corrupt token", Trigger: "invalid checksum",
			ViolatedInvariant: "only verified tokens decode",
			ObservableOutcome: "corrupt token is accepted",
			RelatedSymbols:    []string{"Decoder.Verify"}, SourceAnchors: []string{"checksum"},
			DistinguishingFacts: []string{"unrelated token input"},
		},
	)
	fourthJob := claimLifecycleMappingJob(
		t, store, state.Repository, lifecycleJobForFinding(t, state, fourth.ID),
		RepositoryMappingModelSnapshot{},
	)
	state, later, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: fourthJob.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	provisionalIndex = repositoryFindingIndexByID(state.RepositoryFindings, target.ID)
	dependentIndex = repositoryFindingIndexByID(state.RepositoryFindings, dependent.ID)
	state, retained, err := store.ResolvePossibleDuplicate(
		state.Repository,
		RepositoryDuplicateResolution{
			ProvisionalID: target.ID, CandidateID: dependent.ID, Decision: "merge",
			ExpectedProvisionalVersion: state.RepositoryFindings[provisionalIndex].Version,
			ExpectedCandidateVersion:   state.RepositoryFindings[dependentIndex].Version,
		},
	)
	if err != nil || retained.ID != dependent.ID ||
		repositoryFindingIndexByID(state.RepositoryFindings, later.ID) < 0 {
		t.Fatalf("middle-candidate merge retained=%#v later=%#v err=%v", retained, later, err)
	}
}

func newLifecycleCoverageValidationStore(t *testing.T, run string) (Store, RepositoryState, RepositoryValidationJob) {
	t.Helper()
	store := NewStore(t.TempDir())
	state, occurrence := recordLifecycleFinding(
		t, store, strings.Repeat("a", 40), strings.Repeat("b", 40), run,
		"main", "main", true, "validation edge defect",
	)
	mapping := claimLifecycleMappingJob(
		t, store, state.Repository, lifecycleJobForFinding(t, state, occurrence.ID), RepositoryMappingModelSnapshot{},
	)
	state, aggregate, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: mapping.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, jobs, err := store.ReserveValidationJobs(
		state.Repository, []string{aggregate.ID}, RepositoryMappingModelSnapshot{},
	)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("validation setup jobs=%#v err=%v", jobs, err)
	}
	return store, state, jobs[0]
}

//nolint:govet // Independent restart subtests intentionally reuse short-lived error names.
func TestValidationWorkerRemainingRestartAndFailureCoverage(t *testing.T) {
	defaultOptions := RepositoryValidationProcessOptions{
		Evidence: func(context.Context, RepositoryFinding, []string) ([]RepositoryValidationEvidence, error) {
			return nil, nil
		},
		Adjudicate: func(context.Context, RepositoryMappingModelSnapshot, RepositoryFinding, []RepositoryValidationEvidence) (RepositoryValidationDecision, error) {
			return RepositoryValidationDecision{Outcome: RepositoryValidationNotFixed}, nil
		},
		VerifyAncestry: func(context.Context, string) (bool, error) { return true, nil },
	}

	t.Run("no pending jobs", func(t *testing.T) {
		store := NewStore(t.TempDir())
		state, _ := recordLifecycleFinding(
			t, store, strings.Repeat("1", 40), strings.Repeat("2", 40), "validation-no-jobs",
			"main", "main", true, "no validation job",
		)
		result, err := store.ProcessPendingValidationJobs(nil, state.Repository, defaultOptions)
		if err != nil || result != (RepositoryValidationProcessResult{}) {
			t.Fatalf("no-job result=%#v err=%v", result, err)
		}
	})

	t.Run("evidence is capped to eight", func(t *testing.T) {
		store, state, _ := newLifecycleCoverageValidationStore(t, "validation-cap")
		evidence := make([]RepositoryValidationEvidence, 10)
		hex := "0123456789abcdef"
		for index := range evidence {
			evidence[index] = RepositoryValidationEvidence{
				CommitSHA: strings.Repeat(string(hex[index]), 40), CommitTime: time.Now().UTC(),
			}
		}
		result, err := store.ProcessPendingValidationJobs(
			t.Context(),
			state.Repository,
			RepositoryValidationProcessOptions{
				Evidence: func(context.Context, RepositoryFinding, []string) ([]RepositoryValidationEvidence, error) {
					return evidence, nil
				},
				Adjudicate: func(_ context.Context, _ RepositoryMappingModelSnapshot, _ RepositoryFinding, got []RepositoryValidationEvidence) (RepositoryValidationDecision, error) {
					if len(got) != maxValidationCandidateCommits {
						t.Fatalf("bounded evidence length=%d", len(got))
					}
					return RepositoryValidationDecision{Outcome: RepositoryValidationNotFixed}, nil
				},
				VerifyAncestry: defaultOptions.VerifyAncestry,
			},
		)
		if err != nil || result.NotFixed != 1 {
			t.Fatalf("bounded evidence result=%#v err=%v", result, err)
		}
	})

	t.Run("frozen evidence mismatch fails durably", func(t *testing.T) {
		store, state, job := newLifecycleCoverageValidationStore(t, "validation-frozen-mismatch")
		_, running, _, claimed, err := store.ClaimValidationJob(state.Repository, job.ID)
		if err != nil || !claimed {
			t.Fatalf("claim=%v err=%v", claimed, err)
		}
		frozen := strings.Repeat("3", 40)
		if _, _, err := store.SetValidationJobCandidates(state.Repository, running.ID, []string{frozen}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReconcileJobs(context.Background()); err != nil {
			t.Fatal(err)
		}
		result, err := store.ProcessPendingValidationJobs(
			t.Context(),
			state.Repository,
			RepositoryValidationProcessOptions{
				Evidence: func(_ context.Context, _ RepositoryFinding, got []string) ([]RepositoryValidationEvidence, error) {
					if !slices.Equal(got, []string{frozen}) {
						t.Fatalf("frozen input=%#v", got)
					}
					return []RepositoryValidationEvidence{{CommitSHA: strings.Repeat("4", 40)}}, nil
				},
				Adjudicate:     defaultOptions.Adjudicate,
				VerifyAncestry: defaultOptions.VerifyAncestry,
			},
		)
		if err != nil || result.Failed != 1 {
			t.Fatalf("frozen mismatch result=%#v err=%v", result, err)
		}
	})

	t.Run("invalid decision falls back to failed", func(t *testing.T) {
		store, state, _ := newLifecycleCoverageValidationStore(t, "validation-invalid-outcome")
		result, err := store.ProcessPendingValidationJobs(
			t.Context(),
			state.Repository,
			RepositoryValidationProcessOptions{
				Evidence: defaultOptions.Evidence,
				Adjudicate: func(context.Context, RepositoryMappingModelSnapshot, RepositoryFinding, []RepositoryValidationEvidence) (RepositoryValidationDecision, error) {
					return RepositoryValidationDecision{}, nil
				},
				VerifyAncestry: defaultOptions.VerifyAncestry,
			},
		)
		if err != nil || result.Failed != 1 {
			t.Fatalf("invalid decision result=%#v err=%v", result, err)
		}
	})

	t.Run("failure after finding change requeues", func(t *testing.T) {
		store, state, _ := newLifecycleCoverageValidationStore(t, "validation-failure-change")
		result, err := store.ProcessPendingValidationJobs(
			t.Context(),
			state.Repository,
			RepositoryValidationProcessOptions{
				Evidence: func(_ context.Context, finding RepositoryFinding, _ []string) ([]RepositoryValidationEvidence, error) {
					if _, _, updateErr := store.UpdateRepositoryFindingIssueSnapshot(
						state.Repository,
						RepositoryIssueSnapshotUpdate{
							RepositoryFindingID: finding.ID, ExpectedVersion: finding.Version,
							State: RepositoryFindingIssueNone,
						},
					); updateErr != nil {
						t.Fatal(updateErr)
					}
					return nil, errors.New("evidence unavailable")
				},
				Adjudicate:     defaultOptions.Adjudicate,
				VerifyAncestry: defaultOptions.VerifyAncestry,
			},
		)
		if err != nil || result.Completed != 0 {
			t.Fatalf("changed failure result=%#v err=%v", result, err)
		}
		current, _, _ := store.Get(state.Repository)
		if current.ValidationJobs[0].State != RepositoryValidationPending {
			t.Fatalf("changed failure job=%#v", current.ValidationJobs[0])
		}
	})

	t.Run("candidate freeze race returns joined error", func(t *testing.T) {
		store, state, _ := newLifecycleCoverageValidationStore(t, "validation-freeze-race")
		result, err := store.ProcessPendingValidationJobs(
			t.Context(),
			state.Repository,
			RepositoryValidationProcessOptions{
				Evidence: func(_ context.Context, _ RepositoryFinding, _ []string) ([]RepositoryValidationEvidence, error) {
					current, _, getErr := store.Get(state.Repository)
					if getErr != nil {
						t.Fatal(getErr)
					}
					if releaseErr := store.releaseValidationJob(
						state.Repository,
						current.ValidationJobs[0].ID,
					); releaseErr != nil {
						t.Fatal(releaseErr)
					}
					return []RepositoryValidationEvidence{{CommitSHA: strings.Repeat("5", 40)}}, nil
				},
				Adjudicate:     defaultOptions.Adjudicate,
				VerifyAncestry: defaultOptions.VerifyAncestry,
			},
		)
		if err == nil || result.Completed != 0 {
			t.Fatalf("freeze race result=%#v err=%v", result, err)
		}
	})

	t.Run("terminal and missing jobs are not reclaimed", func(t *testing.T) {
		store, state, job := newLifecycleCoverageValidationStore(t, "validation-terminal-replay")
		result, err := store.ProcessPendingValidationJobs(t.Context(), state.Repository, defaultOptions)
		if err != nil || result.NotFixed != 1 {
			t.Fatalf("terminal setup=%#v err=%v", result, err)
		}
		terminal, err := store.processValidationJob(t.Context(), state.Repository, job.ID, defaultOptions)
		if err != nil || terminal != RepositoryValidationNotFixed {
			t.Fatalf("terminal replay=%q err=%v", terminal, err)
		}
		if _, err := store.processValidationJob(
			t.Context(),
			state.Repository,
			"missing",
			defaultOptions,
		); !errors.Is(
			err,
			os.ErrNotExist,
		) {
			t.Fatalf("missing process job error=%v", err)
		}
	})

	t.Run("slot retry and lock failure", func(t *testing.T) {
		store := NewStore(t.TempDir())
		releases := make([]func(), 0, RepositoryValidationConcurrency)
		for range RepositoryValidationConcurrency {
			release, err := store.AcquireValidationSlot(nil)
			if err != nil {
				t.Fatal(err)
			}
			releases = append(releases, release)
		}
		go func() {
			time.Sleep(2 * issueGenerationSlotRetryInterval)
			releases[0]()
		}()
		retried, err := store.AcquireValidationSlot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		retried()
		for _, release := range releases[1:] {
			release()
		}

		broken := NewStore(t.TempDir())
		lockPath := repositoryReviewTestLockPath(t, broken.root, "validation-slot-00.lock")
		if err := os.MkdirAll(lockPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := broken.AcquireValidationSlot(t.Context()); err == nil {
			t.Fatal("directory validation lock was accepted")
		}
	})

	t.Run("release clears stale candidates and reports save failure", func(t *testing.T) {
		store, state, job := newLifecycleCoverageValidationStore(t, "validation-release-stale")
		_, running, finding, claimed, err := store.ClaimValidationJob(state.Repository, job.ID)
		if err != nil || !claimed {
			t.Fatalf("claim=%v err=%v", claimed, err)
		}
		commit := strings.Repeat("6", 40)
		if _, _, err := store.SetValidationJobCandidates(state.Repository, running.ID, []string{commit}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.UpdateRepositoryFindingIssueSnapshot(state.Repository, RepositoryIssueSnapshotUpdate{
			RepositoryFindingID: finding.ID, ExpectedVersion: finding.Version,
			State: RepositoryFindingIssueNone,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.releaseValidationJob(state.Repository, running.ID); err != nil {
			t.Fatal(err)
		}
		current, _, _ := store.Get(state.Repository)
		if current.ValidationJobs[0].CandidateCommits != nil {
			t.Fatalf("stale candidates retained=%#v", current.ValidationJobs[0])
		}

		failedStore, failedState, failedJob := newLifecycleCoverageValidationStore(t, "validation-release-save")
		_, failedJob, _, claimed, err = failedStore.ClaimValidationJob(failedState.Repository, failedJob.ID)
		if err != nil || !claimed {
			t.Fatalf("save-failure claim=%v err=%v", claimed, err)
		}
		poisonRepositoryReviewStoreOnClock(t, &failedStore)
		if err := failedStore.releaseValidationJob(failedState.Repository, failedJob.ID); err == nil {
			t.Fatal("validation release ignored persistence failure")
		}
	})

	t.Run("release detects missing aggregate", func(t *testing.T) {
		t.Skip("direct JSON ledger tamper was replaced by SQLite payload-integrity tests")
		store, state, job := newLifecycleCoverageValidationStore(t, "validation-release-missing")
		state, job, _, claimed, err := store.ClaimValidationJob(state.Repository, job.ID)
		if err != nil || !claimed {
			t.Fatalf("claim=%v err=%v", claimed, err)
		}
		state.RepositoryFindings = nil
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.path(state.Repository), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := store.releaseValidationJob(state.Repository, job.ID); err == nil {
			t.Fatalf("missing aggregate release error=%v", err)
		}
	})
}

func TestRepositoryLifecycleMutationLockAndLoadFailures(t *testing.T) {
	type operation struct {
		name string
		call func(Store) error
	}
	repository := "owner/repo"
	sha := strings.Repeat("a", 40)
	operations := []operation{
		{name: "reconcile repository", call: func(store Store) error {
			_, _, _, err := store.reconcileRepositoryJobs(repository)
			return err
		}},
		{name: "claim mapping", call: func(store Store) error {
			_, _, _, _, err := store.ClaimMappingJob(repository, "rmj_missing", RepositoryMappingModelSnapshot{})
			return err
		}},
		{name: "save adjudication", call: func(store Store) error {
			_, _, err := store.SaveMappingAdjudication(
				repository,
				"rmj_missing",
				RepositoryMappingAdjudication{Decision: "distinct", Confidence: .9},
			)
			return err
		}},
		{name: "complete mapping", call: func(store Store) error {
			_, _, err := store.CompleteMappingJob(repository, RepositoryMappingCompletion{
				JobID: "rmj_missing", CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
			})
			return err
		}},
		{name: "resolve duplicate", call: func(store Store) error {
			_, _, err := store.ResolvePossibleDuplicate(repository, RepositoryDuplicateResolution{
				ProvisionalID: "rrf_one", CandidateID: "rrf_two", Decision: "distinct",
				ExpectedProvisionalVersion: 1,
			})
			return err
		}},
		{name: "reserve validation", call: func(store Store) error {
			_, _, err := store.ReserveValidationJobs(
				repository,
				[]string{"rrf_missing"},
				RepositoryMappingModelSnapshot{},
			)
			return err
		}},
		{name: "claim validation", call: func(store Store) error {
			_, _, _, _, err := store.ClaimValidationJob(repository, "rvj_missing")
			return err
		}},
		{name: "set validation candidates", call: func(store Store) error {
			_, _, err := store.SetValidationJobCandidates(repository, "rvj_missing", []string{sha})
			return err
		}},
		{name: "complete validation", call: func(store Store) error {
			_, _, _, err := store.CompleteValidationJob(repository, RepositoryValidationCompletion{
				JobID: "rvj_missing", Outcome: RepositoryValidationNotFixed,
			})
			return err
		}},
		{name: "update issue snapshot", call: func(store Store) error {
			_, _, err := store.UpdateRepositoryFindingIssueSnapshot(repository, RepositoryIssueSnapshotUpdate{
				RepositoryFindingID: "rrf_missing", State: RepositoryFindingIssueNone,
			})
			return err
		}},
		{name: "set lifecycle", call: func(store Store) error {
			_, _, err := store.SetRepositoryFindingLifecycle(
				repository, "rrf_missing", RepositoryFindingOpen, 1,
			)
			return err
		}},
		{name: "release mapping", call: func(store Store) error {
			return store.releaseMappingJob(repository, "rmj_missing", errors.New("failed"))
		}},
		{name: "release validation", call: func(store Store) error {
			return store.releaseValidationJob(repository, "rvj_missing")
		}},
	}

	for _, test := range operations {
		t.Run(test.name+" lock", func(t *testing.T) {
			store := NewStore(t.TempDir())
			if err := os.MkdirAll(repositoryReviewTestLockPath(t, store.root, "store.lock"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := test.call(store); err == nil {
				t.Fatal("mutation ignored lock failure")
			}
		})
		t.Run(test.name+" load", func(t *testing.T) {
			t.Skip("per-ledger JSON corruption was replaced by SQLite payload/integrity tests")
			store := NewStore(t.TempDir())
			if err := os.MkdirAll(store.root, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.path(repository), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := test.call(store); err == nil {
				t.Fatal("mutation ignored corrupt state")
			}
		})
	}

	t.Run("startup reconcile read and cancellation", func(t *testing.T) {
		broken := NewStore(t.TempDir())
		if err := os.WriteFile(broken.root, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := broken.ReconcileJobs(nil); err == nil {
			t.Fatal("startup reconciliation ignored unsafe root")
		}

		store := newRepositoryAuditTestStore(t)
		recordMappingWorkerFinding(t, store, "reconcile-canceled", strings.Repeat("9", 40), "wait.go", "wait.signal")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := store.ReconcileJobs(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled reconciliation error=%v", err)
		}
	})
}

func newLifecycleCoverageMappedStore(t *testing.T, run string) (Store, RepositoryState, Finding, RepositoryFinding) {
	t.Helper()
	store := newRepositoryAuditTestStore(t)
	state, occurrence := recordLifecycleFinding(
		t, store, strings.Repeat("a", 40), strings.Repeat("b", 40), run,
		"main", "main", true, "persistence boundary defect",
	)
	job := claimLifecycleMappingJob(
		t, store, state.Repository, lifecycleJobForFinding(t, state, occurrence.ID), RepositoryMappingModelSnapshot{},
	)
	state, aggregate, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: job.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, state, state.Findings[findingIndexByID(state.Findings, occurrence.ID)], aggregate
}

func TestRepositoryLifecycleMutationsReportPersistenceFailures(t *testing.T) {
	t.Run("claim mapping", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		state := recordMappingWorkerFinding(
			t,
			store,
			"save-claim-mapping",
			strings.Repeat("1", 40),
			"wait.go",
			"wait.signal",
		)
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, _, _, _, err := store.ClaimMappingJob(
			state.Repository, state.MappingJobs[0].ID, RepositoryMappingModelSnapshot{},
		); err == nil {
			t.Fatal("mapping claim ignored persistence failure")
		}
	})

	t.Run("save adjudication", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		state := recordMappingWorkerFinding(
			t,
			store,
			"save-adjudication",
			strings.Repeat("2", 40),
			"wait.go",
			"wait.signal",
		)
		_, job, _, claimed, err := store.ClaimMappingJob(
			state.Repository,
			state.MappingJobs[0].ID,
			RepositoryMappingModelSnapshot{},
		)
		if err != nil || !claimed {
			t.Fatalf("claim=%v err=%v", claimed, err)
		}
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, _, err := store.SaveMappingAdjudication(
			state.Repository, job.ID, RepositoryMappingAdjudication{Decision: "distinct", Confidence: .9},
		); err == nil {
			t.Fatal("adjudication save ignored persistence failure")
		}
	})

	t.Run("complete mapping", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		state := recordMappingWorkerFinding(
			t,
			store,
			"save-complete-mapping",
			strings.Repeat("3", 40),
			"wait.go",
			"wait.signal",
		)
		_, job, _, claimed, err := store.ClaimMappingJob(
			state.Repository,
			state.MappingJobs[0].ID,
			RepositoryMappingModelSnapshot{},
		)
		if err != nil || !claimed {
			t.Fatalf("claim=%v err=%v", claimed, err)
		}
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, _, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
			JobID: job.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
		}); err == nil {
			t.Fatal("mapping completion ignored persistence failure")
		}
	})

	t.Run("startup reconcile", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		state := recordMappingWorkerFinding(
			t,
			store,
			"save-reconcile",
			strings.Repeat("4", 40),
			"wait.go",
			"wait.signal",
		)
		if _, _, _, claimed, err := store.ClaimMappingJob(
			state.Repository,
			state.MappingJobs[0].ID,
			RepositoryMappingModelSnapshot{},
		); err != nil ||
			!claimed {
			t.Fatalf("claim=%v err=%v", claimed, err)
		}
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, err := store.ReconcileJobs(context.Background()); err == nil {
			t.Fatal("job reconciliation ignored persistence failure")
		}
	})

	t.Run("resolve duplicate", func(t *testing.T) {
		store, _, _, target := newLifecycleCoverageMappedStore(t, "save-duplicate-target")
		state, occurrence := recordLifecycleFinding(
			t, store, strings.Repeat("5", 40), strings.Repeat("c", 40), "save-duplicate-source",
			"main", "main", true, "provisional persistence defect", MatchHints{
				Component: "other", Operation: "other operation", FailureMode: "other failure",
				Trigger: "other trigger", ViolatedInvariant: "other invariant", ObservableOutcome: "other outcome",
				RelatedSymbols: []string{"Other.Run"}, SourceAnchors: []string{"other"},
				DistinguishingFacts: []string{"other fact"},
			},
		)
		job := claimLifecycleMappingJob(
			t,
			store,
			state.Repository,
			lifecycleJobForFinding(t, state, occurrence.ID),
			RepositoryMappingModelSnapshot{},
		)
		state, provisional, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
			JobID: job.ID, CreateMatchState: RepositoryMatchProvisional, DefaultBranchVerified: true,
			PossibleDuplicates: []RepositoryFindingPossibleDuplicate{{
				CandidateID: target.ID, Relation: "uncertain", Confidence: .6,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, _, err := store.ResolvePossibleDuplicate(state.Repository, RepositoryDuplicateResolution{
			ProvisionalID: provisional.ID, CandidateID: target.ID, Decision: "distinct",
			ExpectedProvisionalVersion: provisional.Version,
		}); err == nil {
			t.Fatal("duplicate resolution ignored persistence failure")
		}
	})

	t.Run("reserve validation", func(t *testing.T) {
		store, state, _, aggregate := newLifecycleCoverageMappedStore(t, "save-reserve-validation")
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, _, err := store.ReserveValidationJobs(
			state.Repository,
			[]string{aggregate.ID},
			RepositoryMappingModelSnapshot{},
		); err == nil {
			t.Fatal("validation reservation ignored persistence failure")
		}
	})

	t.Run("claim validation", func(t *testing.T) {
		store, state, _, aggregate := newLifecycleCoverageMappedStore(t, "save-claim-validation")
		state, jobs, err := store.ReserveValidationJobs(
			state.Repository,
			[]string{aggregate.ID},
			RepositoryMappingModelSnapshot{},
		)
		if err != nil {
			t.Fatal(err)
		}
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, _, _, _, err := store.ClaimValidationJob(state.Repository, jobs[0].ID); err == nil {
			t.Fatal("validation claim ignored persistence failure")
		}
	})

	t.Run("set validation candidates", func(t *testing.T) {
		store, state, _, aggregate := newLifecycleCoverageMappedStore(t, "save-set-candidates")
		state, jobs, err := store.ReserveValidationJobs(
			state.Repository,
			[]string{aggregate.ID},
			RepositoryMappingModelSnapshot{},
		)
		if err != nil {
			t.Fatal(err)
		}
		_, job, _, claimed, err := store.ClaimValidationJob(state.Repository, jobs[0].ID)
		if err != nil || !claimed {
			t.Fatalf("claim=%v err=%v", claimed, err)
		}
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, _, err := store.SetValidationJobCandidates(
			state.Repository,
			job.ID,
			[]string{strings.Repeat("d", 40)},
		); err == nil {
			t.Fatal("validation candidates ignored persistence failure")
		}
	})

	t.Run("complete validation", func(t *testing.T) {
		store, state, _, aggregate := newLifecycleCoverageMappedStore(t, "save-complete-validation")
		state, jobs, err := store.ReserveValidationJobs(
			state.Repository,
			[]string{aggregate.ID},
			RepositoryMappingModelSnapshot{},
		)
		if err != nil {
			t.Fatal(err)
		}
		_, job, _, claimed, err := store.ClaimValidationJob(state.Repository, jobs[0].ID)
		if err != nil || !claimed {
			t.Fatalf("claim=%v err=%v", claimed, err)
		}
		if _, _, err := store.SetValidationJobCandidates(state.Repository, job.ID, nil); err != nil {
			t.Fatal(err)
		}
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, _, _, err := store.CompleteValidationJob(state.Repository, RepositoryValidationCompletion{
			JobID: job.ID, Outcome: RepositoryValidationNotFixed,
		}); err == nil {
			t.Fatal("validation completion ignored persistence failure")
		}
	})

	t.Run("issue snapshot", func(t *testing.T) {
		store, state, _, aggregate := newLifecycleCoverageMappedStore(t, "save-issue-snapshot")
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, _, err := store.UpdateRepositoryFindingIssueSnapshot(state.Repository, RepositoryIssueSnapshotUpdate{
			RepositoryFindingID: aggregate.ID, ExpectedVersion: aggregate.Version,
			State: RepositoryFindingIssueNone,
		}); err == nil {
			t.Fatal("issue snapshot ignored persistence failure")
		}
	})

	t.Run("manual lifecycle", func(t *testing.T) {
		store, state, _, aggregate := newLifecycleCoverageMappedStore(t, "save-manual-lifecycle")
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, _, err := store.SetRepositoryFindingLifecycle(
			state.Repository, aggregate.ID, RepositoryFindingDismissed, aggregate.Version,
		); err == nil {
			t.Fatal("manual lifecycle ignored persistence failure")
		}
	})

	t.Run("release mapping", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		state := recordMappingWorkerFinding(
			t,
			store,
			"save-release-mapping",
			strings.Repeat("6", 40),
			"wait.go",
			"wait.signal",
		)
		_, job, _, claimed, err := store.ClaimMappingJob(
			state.Repository,
			state.MappingJobs[0].ID,
			RepositoryMappingModelSnapshot{},
		)
		if err != nil || !claimed {
			t.Fatalf("claim=%v err=%v", claimed, err)
		}
		poisonRepositoryReviewStoreOnClock(t, &store)
		if err := store.releaseMappingJob(state.Repository, job.ID, errors.New("failed")); err == nil {
			t.Fatal("mapping release ignored persistence failure")
		}
	})
}

//nolint:govet // Boundary probes intentionally reuse short-lived result names across sequential checks.
func TestCompleteMappingRemainingConcurrencyAndRecoveryBranches(t *testing.T) {
	store := NewStore(t.TempDir())
	_, first := recordLifecycleFinding(
		t, store, strings.Repeat("1", 40), strings.Repeat("a", 40), "fresh-first",
		"main", "main", true, "same concurrent defect",
	)
	state, second := recordLifecycleFinding(
		t, store, strings.Repeat("2", 40), strings.Repeat("b", 40), "fresh-second",
		"main", "main", true, "same concurrent defect",
	)
	firstJob := claimLifecycleMappingJob(
		t,
		store,
		state.Repository,
		lifecycleJobForFinding(t, state, first.ID),
		RepositoryMappingModelSnapshot{},
	)
	secondJob := claimLifecycleMappingJob(
		t,
		store,
		state.Repository,
		lifecycleJobForFinding(t, state, second.ID),
		RepositoryMappingModelSnapshot{},
	)
	state, target, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: firstJob.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, joined, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: secondJob.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil || joined.ID != target.ID || len(state.RepositoryFindings) != 1 {
		t.Fatalf("fresh concurrent join=%#v findings=%d err=%v", joined, len(state.RepositoryFindings), err)
	}

	otherHints := MatchHints{
		Component: "storage", Operation: "flush journal", FailureMode: "record omitted",
		Trigger: "checkpoint", ViolatedInvariant: "journal contains commit",
		ObservableOutcome: "record disappears", RelatedSymbols: []string{"Journal.Flush"},
		SourceAnchors: []string{"journal"}, DistinguishingFacts: []string{"checkpoint only"},
	}
	state, third := recordLifecycleFinding(
		t, store, strings.Repeat("3", 40), strings.Repeat("c", 40), "mapping-errors",
		"main", "main", true, "independent mapping defect", otherHints,
	)
	thirdJob := claimLifecycleMappingJob(
		t,
		store,
		state.Repository,
		lifecycleJobForFinding(t, state, third.ID),
		RepositoryMappingModelSnapshot{},
	)
	if _, _, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: thirdJob.ID, RepositoryFindingID: "rrf_missing", DefaultBranchVerified: true,
	}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing target error=%v", err)
	}
	if _, _, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: thirdJob.ID, CreateMatchState: RepositoryMatchProvisional, DefaultBranchVerified: true,
		PossibleDuplicates: []RepositoryFindingPossibleDuplicate{{
			CandidateID: "rrf_missing", Relation: "uncertain", Confidence: .5,
		}},
	}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing possible duplicate error=%v", err)
	}
	if _, _, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID:                 thirdJob.ID,
		CreateMatchState:      RepositoryMatchNew,
		DefaultBranchVerified: true,
		PossibleDuplicates: []RepositoryFindingPossibleDuplicate{
			{CandidateID: target.ID, Relation: "bad", Confidence: .5},
		},
	}); err == nil {
		t.Fatal("invalid possible duplicate normalization accepted")
	}
	_, normalized, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: thirdJob.ID, CreateMatchState: RepositoryMatchProvisional, DefaultBranchVerified: true,
		PossibleDuplicates: []RepositoryFindingPossibleDuplicate{{
			CandidateID: target.ID, Relation: "related", Confidence: .5,
		}},
	})
	if err != nil || normalized.MatchState != RepositoryMatchNew {
		t.Fatalf("related provisional normalization=%#v err=%v", normalized, err)
	}

	state, deferred := recordLifecycleFinding(
		t, store, strings.Repeat("4", 40), strings.Repeat("d", 40), "deferred-adjudication",
		"main", "main", true, "deferred network defect", MatchHints{
			Component:         "network",
			Operation:         "read frame",
			FailureMode:       "frame truncated",
			Trigger:           "partial read",
			ViolatedInvariant: "frame is complete",
			ObservableOutcome: "connection closes",
			RelatedSymbols: []string{
				"Frame.Read",
			},
			SourceAnchors:       []string{"frame"},
			DistinguishingFacts: []string{"partial read"},
		},
	)
	deferredJob := claimLifecycleMappingJob(
		t,
		store,
		state.Repository,
		lifecycleJobForFinding(t, state, deferred.ID),
		RepositoryMappingModelSnapshot{},
	)
	if _, _, err := store.SaveMappingAdjudication(state.Repository, deferredJob.ID, RepositoryMappingAdjudication{
		Decision: "related", CandidateID: target.ID, Confidence: .6,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReconcileJobs(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, creator := recordLifecycleFinding(
		t, store, strings.Repeat("5", 40), strings.Repeat("e", 40), "clear-deferred",
		"main", "main", true, "independent parser defect", MatchHints{
			Component:         "parser",
			Operation:         "decode header",
			FailureMode:       "header lost",
			Trigger:           "empty input",
			ViolatedInvariant: "header exists",
			ObservableOutcome: "parse fails",
			RelatedSymbols: []string{
				"Parser.Decode",
			},
			SourceAnchors:       []string{"header"},
			DistinguishingFacts: []string{"empty input"},
		},
	)
	creatorJob := claimLifecycleMappingJob(
		t,
		store,
		state.Repository,
		lifecycleJobForFinding(t, state, creator.ID),
		RepositoryMappingModelSnapshot{},
	)
	state, _, err = store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: creatorJob.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	deferredJob = lifecycleJobForFinding(t, state, deferred.ID)
	if !mappingAdjudicationEmpty(deferredJob.Adjudication) || deferredJob.CandidateUniverse != "" {
		t.Fatalf("stale deferred adjudication=%#v", deferredJob)
	}

	staleJob := claimLifecycleMappingJob(t, store, state.Repository, deferredJob, RepositoryMappingModelSnapshot{})
	poisonRepositoryReviewStoreOnClock(t, &store)
	if _, _, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: staleJob.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
		ExpectedUniverse: "rum_stale",
	}); err == nil {
		t.Fatal("universe restart ignored persistence failure")
	}
}

//nolint:govet // Independent lifecycle subtests intentionally reuse short-lived error names.
func TestLifecycleRemainingValidationIssueAndReconcileBranches(t *testing.T) {
	t.Run("reconcile pending and stale validation", func(t *testing.T) {
		store, state, _, aggregate := newLifecycleCoverageMappedStore(t, "reconcile-validation")
		state, jobs, err := store.ReserveValidationJobs(
			state.Repository,
			[]string{aggregate.ID},
			RepositoryMappingModelSnapshot{},
		)
		if err != nil {
			t.Fatal(err)
		}
		if created, mappingReset, validationReset, err := store.reconcileRepositoryJobs(
			state.Repository,
		); err != nil || created != 0 || mappingReset != 0 ||
			validationReset != 0 {
			t.Fatalf("no-change reconcile=%d/%d/%d err=%v", created, mappingReset, validationReset, err)
		}
		state, running, _, claimed, err := store.ClaimValidationJob(state.Repository, jobs[0].ID)
		if err != nil || !claimed {
			t.Fatalf("claim=%v err=%v", claimed, err)
		}
		if state, _, err = store.SetValidationJobCandidates(
			state.Repository,
			running.ID,
			[]string{strings.Repeat("a", 40)},
		); err != nil {
			t.Fatal(err)
		}
		index := repositoryFindingIndexByID(state.RepositoryFindings, aggregate.ID)
		state.RepositoryFindings[index].Version++
		state.Version++
		if err := store.save(&state); err != nil {
			t.Fatal(err)
		}
		if _, _, reset, err := store.reconcileRepositoryJobs(state.Repository); err != nil || reset != 1 {
			t.Fatalf("stale reconcile reset=%d err=%v", reset, err)
		}
		current, _, _ := store.Get(state.Repository)
		if current.ValidationJobs[0].CandidateCommits != nil {
			t.Fatalf("stale candidates=%#v", current.ValidationJobs[0])
		}
	})

	t.Run("mapping claim conflicts", func(t *testing.T) {
		store, state, occurrence, _ := newLifecycleCoverageMappedStore(t, "associated-claim")
		state.MappingJobs[0].State = RepositoryMappingPending
		state.MappingJobs[0].RepositoryFindingID = ""
		state.MappingJobs[0].ReservedAt = time.Time{}
		state.Version++
		if err := store.save(&state); err != nil {
			t.Fatal(err)
		}
		if _, _, _, _, err := store.ClaimMappingJob(
			state.Repository,
			state.MappingJobs[0].ID,
			RepositoryMappingModelSnapshot{},
		); !errors.Is(
			err,
			ErrConflict,
		) {
			t.Fatalf("associated claim error=%v occurrence=%#v", err, occurrence)
		}

		fresh := newRepositoryAuditTestStore(t)
		pending := recordMappingWorkerFinding(
			t,
			fresh,
			"snapshot-conflict",
			strings.Repeat("2", 40),
			"wait.go",
			"wait.signal",
		)
		firstSnapshot := RepositoryMappingModelSnapshot{Model: "one"}
		_, job, _, claimed, err := fresh.ClaimMappingJob(pending.Repository, pending.MappingJobs[0].ID, firstSnapshot)
		if err != nil || !claimed {
			t.Fatalf("claim=%v err=%v", claimed, err)
		}
		if err := fresh.releaseMappingJob(pending.Repository, job.ID, errors.New("retry")); err != nil {
			t.Fatal(err)
		}
		if _, _, _, _, err := fresh.ClaimMappingJob(
			pending.Repository,
			job.ID,
			RepositoryMappingModelSnapshot{Model: "two"},
		); !errors.Is(
			err,
			ErrConflict,
		) {
			t.Fatalf("snapshot conflict error=%v", err)
		}
	})

	t.Run("validation claim and completion fences", func(t *testing.T) {
		store, state, _, aggregate := newLifecycleCoverageMappedStore(t, "validation-fences-final")
		state, jobs, err := store.ReserveValidationJobs(
			state.Repository,
			[]string{aggregate.ID},
			RepositoryMappingModelSnapshot{},
		)
		if err != nil {
			t.Fatal(err)
		}
		index := repositoryFindingIndexByID(state.RepositoryFindings, aggregate.ID)
		state.RepositoryFindings[index].Issue = RepositoryFindingIssueAssociation{
			State: RepositoryFindingIssueNone, Conflict: true,
			ConflictURLs: []string{"https://example.test/one", "https://example.test/two"},
		}
		state.RepositoryFindings[index].Version++
		state.Version++
		if err := store.save(&state); err != nil {
			t.Fatal(err)
		}
		if _, _, _, _, err := store.ClaimValidationJob(state.Repository, jobs[0].ID); !errors.Is(err, ErrConflict) {
			t.Fatalf("conflicted validation claim error=%v", err)
		}

		staleStore, staleState, staleJob := newLifecycleCoverageValidationStore(t, "validation-stale-claim")
		staleIndex := repositoryFindingIndexByID(staleState.RepositoryFindings, staleJob.RepositoryFindingID)
		staleState.RepositoryFindings[staleIndex].Version++
		staleState.Version++
		staleJob.CandidateCommits = []string{strings.Repeat("b", 40)}
		staleState.ValidationJobs[0].CandidateCommits = append([]string(nil), staleJob.CandidateCommits...)
		if err := staleStore.save(&staleState); err != nil {
			t.Fatal(err)
		}
		_, claimedJob, _, claimed, err := staleStore.ClaimValidationJob(staleState.Repository, staleJob.ID)
		if err != nil || !claimed || claimedJob.CandidateCommits != nil {
			t.Fatalf("stale claim=%#v claimed=%v err=%v", claimedJob, claimed, err)
		}
		if _, _, err := staleStore.SetValidationJobCandidates(
			staleState.Repository,
			claimedJob.ID,
			[]string{"bad"},
		); err == nil {
			t.Fatal("invalid public candidate commit accepted")
		}
		if _, _, _, err := staleStore.CompleteValidationJob(staleState.Repository, RepositoryValidationCompletion{
			JobID: claimedJob.ID, Outcome: RepositoryValidationNotFixed,
			SelectedCommitSHA: strings.Repeat("c", 40), FixCommitTime: time.Now().UTC(),
		}); err == nil {
			t.Fatal("non-confirmed fix metadata accepted")
		}
	})

	t.Run("not-fixed reopens resolution pending", func(t *testing.T) {
		store, state, _, aggregate := newLifecycleCoverageMappedStore(t, "not-fixed-reopens")
		state, aggregate, err := store.UpdateRepositoryFindingIssueSnapshot(
			state.Repository,
			RepositoryIssueSnapshotUpdate{
				RepositoryFindingID: aggregate.ID, ExpectedVersion: aggregate.Version,
				ExternalID: "1", URL: "https://example.test/issues/1", Origin: IssueDraftOriginLinked,
				State: RepositoryFindingIssueClosed,
			},
		)
		if err != nil || aggregate.Lifecycle != RepositoryFindingResolutionPending {
			t.Fatalf("closed aggregate=%#v err=%v", aggregate, err)
		}
		state, jobs, err := store.ReserveValidationJobs(
			state.Repository,
			[]string{aggregate.ID},
			RepositoryMappingModelSnapshot{},
		)
		if err != nil {
			t.Fatal(err)
		}
		state, job, _, claimed, err := store.ClaimValidationJob(state.Repository, jobs[0].ID)
		if err != nil || !claimed {
			t.Fatalf("claim=%v err=%v", claimed, err)
		}
		if _, _, err := store.SetValidationJobCandidates(state.Repository, job.ID, nil); err != nil {
			t.Fatal(err)
		}
		_, reopened, _, err := store.CompleteValidationJob(state.Repository, RepositoryValidationCompletion{
			JobID: job.ID, Outcome: RepositoryValidationNotFixed,
		})
		if err != nil || reopened.Lifecycle != RepositoryFindingOpen {
			t.Fatalf("not-fixed aggregate=%#v err=%v", reopened, err)
		}
		if RepositoryFindingIssueSnapshotFresh(
			RepositoryFinding{Issue: RepositoryFindingIssueAssociation{State: RepositoryFindingIssueNone}},
			time.Now(),
		) {
			t.Fatal("empty issue snapshot reported fresh")
		}
	})

	t.Run("posted issue snapshot propagates and fences", func(t *testing.T) {
		store, state, occurrence, aggregate := newLifecycleCoverageMappedStore(t, "posted-snapshot")
		state, draft, err := store.LinkExistingIssue(ExistingIssueLink{
			Repository: state.Repository, FindingID: occurrence.ID, ExpectedFindingVersion: occurrence.Version,
			ExternalID: "7", ExternalURL: "https://github.com/owner/repo/issues/7", Title: "old title",
			State: "open", Origin: IssueDraftOriginLinked, Confirmed: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		aggregate = state.RepositoryFindings[repositoryFindingIndexByID(state.RepositoryFindings, aggregate.ID)]
		state, updated, err := store.UpdateRepositoryFindingIssueSnapshot(
			state.Repository,
			RepositoryIssueSnapshotUpdate{
				RepositoryFindingID: aggregate.ID, ExpectedVersion: aggregate.Version,
				ExternalID: "7", URL: draft.ExternalURL, Origin: IssueDraftOriginLinked,
				State: RepositoryFindingIssueClosed, Title: "new title",
			},
		)
		if err != nil || updated.Lifecycle != RepositoryFindingResolutionPending {
			t.Fatalf("updated snapshot=%#v err=%v", updated, err)
		}
		draft = state.IssueDrafts[issueDraftIndexByID(state.IssueDrafts, draft.ID)]
		if draft.ExternalState != "closed" || draft.Title != "new title" {
			t.Fatalf("updated draft=%#v", draft)
		}
		if _, _, err := store.UpdateRepositoryFindingIssueSnapshot(state.Repository, RepositoryIssueSnapshotUpdate{
			RepositoryFindingID: aggregate.ID, ExpectedVersion: updated.Version,
			ExternalID: "8", URL: "https://github.com/owner/repo/issues/8", Origin: IssueDraftOriginLinked,
			State: RepositoryFindingIssueOpen,
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("different URL snapshot error=%v", err)
		}

		provisionalStore, _, _, target := newLifecycleCoverageMappedStore(
			t,
			"snapshot-provisional-target",
		)
		provisionalState, source := recordLifecycleFinding(
			t, provisionalStore, strings.Repeat("d", 40), strings.Repeat("e", 40), "snapshot-provisional",
			"main", "main", true, "snapshot provisional", MatchHints{
				Component:           "other",
				Operation:           "other operation",
				FailureMode:         "other failure",
				Trigger:             "other trigger",
				ViolatedInvariant:   "other invariant",
				ObservableOutcome:   "other outcome",
				RelatedSymbols:      []string{"Other"},
				SourceAnchors:       []string{"other"},
				DistinguishingFacts: []string{"other"},
			},
		)
		mapping := claimLifecycleMappingJob(
			t,
			provisionalStore,
			provisionalState.Repository,
			lifecycleJobForFinding(t, provisionalState, source.ID),
			RepositoryMappingModelSnapshot{},
		)
		_, provisional, err := provisionalStore.CompleteMappingJob(
			provisionalState.Repository,
			RepositoryMappingCompletion{
				JobID:                 mapping.ID,
				CreateMatchState:      RepositoryMatchProvisional,
				DefaultBranchVerified: true,
				PossibleDuplicates: []RepositoryFindingPossibleDuplicate{
					{CandidateID: target.ID, Relation: "uncertain", Confidence: .5},
				},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := provisionalStore.UpdateRepositoryFindingIssueSnapshot(
			provisionalState.Repository,
			RepositoryIssueSnapshotUpdate{
				RepositoryFindingID: provisional.ID,
				ExpectedVersion:     provisional.Version,
				State:               RepositoryFindingIssueNone,
			},
		); !errors.Is(
			err,
			ErrConflict,
		) {
			t.Fatalf("provisional snapshot error=%v", err)
		}
	})

	proof := Finding{
		TargetIsDefault: true, DefaultBranchVerified: true, PostResolutionVerified: true,
		PostResolutionFixCommit: strings.Repeat("f", 40), PostResolutionFindingID: "rrf",
		CreatedAt: time.Now().UTC(),
	}
	if occurrenceAfterConfirmedResolution(proof, RepositoryFinding{
		ID: "rrf", ValidationState: RepositoryValidationConfirmed, Lifecycle: RepositoryFindingResolved,
		FixCommitSHA: strings.Repeat("f", 40),
	}) {
		t.Fatal("confirmed finding without history regressed")
	}
}

func newLifecycleCoverageProvisionalStore(
	t *testing.T,
	run string,
) (Store, RepositoryState, RepositoryFinding, RepositoryFinding) {
	t.Helper()
	store, _, _, target := newLifecycleCoverageMappedStore(t, run+"-target")
	state, occurrence := recordLifecycleFinding(
		t, store, strings.Repeat("7", 40), strings.Repeat("d", 40), run+"-source",
		"main", "main", true, "provisional offset defect", MatchHints{
			Component:           "offset",
			Operation:           "offset operation",
			FailureMode:         "offset failure",
			Trigger:             "offset trigger",
			ViolatedInvariant:   "offset invariant",
			ObservableOutcome:   "offset outcome",
			RelatedSymbols:      []string{"Offset.Run"},
			SourceAnchors:       []string{"offset"},
			DistinguishingFacts: []string{"offset fact"},
		},
	)
	job := claimLifecycleMappingJob(
		t,
		store,
		state.Repository,
		lifecycleJobForFinding(t, state, occurrence.ID),
		RepositoryMappingModelSnapshot{},
	)
	state, provisional, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: job.ID, CreateMatchState: RepositoryMatchProvisional, DefaultBranchVerified: true,
		PossibleDuplicates: []RepositoryFindingPossibleDuplicate{{
			CandidateID: target.ID, Relation: "uncertain", Confidence: .6,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return store, state, target, provisional
}

//nolint:govet // Independent lifecycle subtests intentionally reuse short-lived error names.
func TestFinalReachableLifecycleMutationOffsets(t *testing.T) {
	t.Run("pending mapping cannot complete", func(t *testing.T) {
		store := newRepositoryAuditTestStore(t)
		state := recordMappingWorkerFinding(
			t,
			store,
			"pending-completion",
			strings.Repeat("1", 40),
			"wait.go",
			"wait.signal",
		)
		if _, _, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
			JobID: state.MappingJobs[0].ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("pending completion error=%v", err)
		}
	})

	t.Run("provisional fences and stale duplicate resolution", func(t *testing.T) {
		store, state, target, provisional := newLifecycleCoverageProvisionalStore(t, "provisional-fences")
		if _, _, err := store.ResolvePossibleDuplicate(state.Repository, RepositoryDuplicateResolution{
			ProvisionalID: provisional.ID, CandidateID: target.ID, Decision: "distinct",
			ExpectedProvisionalVersion: provisional.Version + 1,
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale duplicate error=%v", err)
		}
		if _, _, err := store.ReserveValidationJobs(
			state.Repository,
			[]string{provisional.ID},
			RepositoryMappingModelSnapshot{},
		); !errors.Is(
			err,
			ErrConflict,
		) {
			t.Fatalf("provisional validation error=%v", err)
		}
	})

	t.Run("merge reports persistence failure", func(t *testing.T) {
		store, state, target, provisional := newLifecycleCoverageProvisionalStore(t, "merge-save-offset")
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, _, err := store.ResolvePossibleDuplicate(state.Repository, RepositoryDuplicateResolution{
			ProvisionalID: provisional.ID, CandidateID: target.ID, Decision: "merge",
			ExpectedProvisionalVersion: provisional.Version, ExpectedCandidateVersion: target.Version,
		}); err == nil {
			t.Fatal("merge ignored persistence failure")
		}
	})

	t.Run("stale validation restart reports persistence failure", func(t *testing.T) {
		store, state, job := newLifecycleCoverageValidationStore(t, "stale-validation-save")
		state, job, _, claimed, err := store.ClaimValidationJob(state.Repository, job.ID)
		if err != nil || !claimed {
			t.Fatalf("claim=%v err=%v", claimed, err)
		}
		index := repositoryFindingIndexByID(state.RepositoryFindings, job.RepositoryFindingID)
		state.RepositoryFindings[index].Version++
		state.Version++
		if err := store.save(&state); err != nil {
			t.Fatal(err)
		}
		poisonRepositoryReviewStoreOnClock(t, &store)
		if _, _, _, err := store.CompleteValidationJob(state.Repository, RepositoryValidationCompletion{
			JobID: job.ID, Outcome: RepositoryValidationNotFixed,
		}); err == nil {
			t.Fatal("stale validation restart ignored persistence failure")
		}
	})

	t.Run("issue draft state and timestamp projections", func(t *testing.T) {
		store, state, occurrence, aggregate := newLifecycleCoverageMappedStore(t, "draft-projection-offset")
		request := testIssueGenerationRequest(state.Repository, occurrence.ID, "offset-generation")
		state, draft, reserved, err := store.ReserveIssueGeneration(request)
		if err != nil || !reserved {
			t.Fatalf("reserve=%v err=%v", reserved, err)
		}
		state, draft, err = store.CompleteIssueGeneration(
			state.Repository, draft.ID, request.GenerationID, "draft title", "draft body", nil, "",
		)
		if err != nil || draft.State != IssueDraftEditing {
			t.Fatalf("draft=%#v err=%v", draft, err)
		}
		aggregate = state.RepositoryFindings[repositoryFindingIndexByID(state.RepositoryFindings, aggregate.ID)]
		if _, _, err := store.UpdateRepositoryFindingIssueSnapshot(state.Repository, RepositoryIssueSnapshotUpdate{
			RepositoryFindingID: aggregate.ID, ExpectedVersion: aggregate.Version,
			State: RepositoryFindingIssueNone,
		}); err != nil {
			t.Fatal(err)
		}

		postedStore, postedState, postedOccurrence, postedAggregate := newLifecycleCoverageMappedStore(
			t,
			"timestamp-projection-offset",
		)
		postedState, posted, err := postedStore.LinkExistingIssue(ExistingIssueLink{
			Repository:             postedState.Repository,
			FindingID:              postedOccurrence.ID,
			ExpectedFindingVersion: postedOccurrence.Version,
			ExternalID:             "12",
			ExternalURL:            "https://github.com/owner/repo/issues/12",
			Title:                  "old",
			State:                  "open",
			Origin:                 IssueDraftOriginLinked,
			Confirmed:              true,
		})
		if err != nil {
			t.Fatal(err)
		}
		clock := posted.UpdatedAt.Add(time.Minute)
		postedStore.now = func() time.Time { return clock }
		postedAggregate = postedState.RepositoryFindings[repositoryFindingIndexByID(postedState.RepositoryFindings, postedAggregate.ID)]
		updatedState, _, err := postedStore.UpdateRepositoryFindingIssueSnapshot(
			postedState.Repository,
			RepositoryIssueSnapshotUpdate{
				RepositoryFindingID: postedAggregate.ID, ExpectedVersion: postedAggregate.Version,
				ExternalID: "12", URL: posted.ExternalURL, Origin: IssueDraftOriginLinked,
				State: RepositoryFindingIssueOpen, Title: "new",
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		posted = updatedState.IssueDrafts[issueDraftIndexByID(updatedState.IssueDrafts, posted.ID)]
		if !posted.UpdatedAt.Equal(clock) {
			t.Fatalf("draft update time=%v want %v", posted.UpdatedAt, clock)
		}
	})
}

//nolint:govet // Boundary probes intentionally reuse short-lived result names across sequential checks.
func TestRepositoryLifecycleIssueAndManualStateCoverage(t *testing.T) {
	store := NewStore(t.TempDir())
	state, occurrence := recordLifecycleFinding(
		t, store, strings.Repeat("a", 40), strings.Repeat("b", 40), "manual-lifecycle",
		"main", "main", true, "manual lifecycle defect",
	)
	job := lifecycleJobForFinding(t, state, occurrence.ID)
	job = claimLifecycleMappingJob(t, store, state.Repository, job, RepositoryMappingModelSnapshot{})
	state, aggregate, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
		JobID: job.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	currentOccurrence := state.Findings[findingIndexByID(state.Findings, occurrence.ID)]
	if !RepositoryFindingIssueActionsAllowed(state, currentOccurrence) {
		t.Fatal("open known finding unexpectedly blocks issue actions")
	}
	if _, _, err := store.SetRepositoryFindingLifecycle(
		state.Repository,
		aggregate.ID,
		RepositoryFindingResolved,
		aggregate.Version,
	); err == nil {
		t.Fatal("invalid manual lifecycle accepted")
	}
	if _, _, err := store.SetRepositoryFindingLifecycle(
		state.Repository,
		"missing",
		RepositoryFindingDismissed,
		1,
	); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("missing manual lifecycle error = %v", err)
	}
	if _, _, err := store.SetRepositoryFindingLifecycle(
		state.Repository,
		aggregate.ID,
		RepositoryFindingDismissed,
		aggregate.Version+1,
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale dismissal error = %v", err)
	}
	state, dismissed, err := store.SetRepositoryFindingLifecycle(
		state.Repository, aggregate.ID, RepositoryFindingDismissed, aggregate.Version,
	)
	if err != nil || dismissed.Lifecycle != RepositoryFindingDismissed {
		t.Fatalf("dismissed = %#v err=%v", dismissed, err)
	}
	if _, same, err := store.SetRepositoryFindingLifecycle(
		state.Repository, aggregate.ID, RepositoryFindingDismissed, dismissed.Version+10,
	); err != nil || same.Version != dismissed.Version {
		t.Fatalf("idempotent dismissal = %#v err=%v", same, err)
	}
	state, reopened, err := store.SetRepositoryFindingLifecycle(
		state.Repository, aggregate.ID, RepositoryFindingOpen, dismissed.Version,
	)
	if err != nil || reopened.Lifecycle != RepositoryFindingOpen {
		t.Fatalf("reopened = %#v err=%v", reopened, err)
	}

	invalidUpdates := []RepositoryIssueSnapshotUpdate{
		{},
		{RepositoryFindingID: aggregate.ID, State: RepositoryFindingIssueOpen},
		{
			RepositoryFindingID: aggregate.ID,
			State:               RepositoryFindingIssueOpen,
			ExternalID:          "1",
			URL:                 "http://example.test/1",
		},
	}
	for index, update := range invalidUpdates {
		if _, _, err := store.UpdateRepositoryFindingIssueSnapshot(state.Repository, update); err == nil {
			t.Errorf("invalid issue snapshot %d accepted", index)
		}
	}
	if _, _, err := store.UpdateRepositoryFindingIssueSnapshot(state.Repository, RepositoryIssueSnapshotUpdate{
		RepositoryFindingID: "missing", State: RepositoryFindingIssueNone,
	}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing issue snapshot error = %v", err)
	}
	if _, _, err := store.UpdateRepositoryFindingIssueSnapshot(state.Repository, RepositoryIssueSnapshotUpdate{
		RepositoryFindingID: aggregate.ID, ExpectedVersion: reopened.Version + 1,
		State: RepositoryFindingIssueNone,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale issue snapshot error = %v", err)
	}
	if _, _, err := store.UpdateRepositoryFindingIssueSnapshot(state.Repository, RepositoryIssueSnapshotUpdate{
		RepositoryFindingID: aggregate.ID, ExpectedVersion: reopened.Version,
		ExternalID: "1", URL: "https://github.com/owner/repo/issues/1", Origin: IssueDraftOriginLinked,
		State: RepositoryFindingIssueOpen, SnapshotAt: time.Now().UTC().Add(2 * time.Minute),
	}); err == nil || !strings.Contains(err.Error(), "future") {
		t.Fatalf("future issue snapshot error = %v", err)
	}

	now := time.Now().UTC()
	issueState := RepositoryState{RepositoryFindings: []RepositoryFinding{{
		ID: "rrf_issue", Lifecycle: RepositoryFindingResolutionPending,
		Issue: RepositoryFindingIssueAssociation{
			URL: "https://github.com/owner/repo/issues/1", State: RepositoryFindingIssueClosed,
			Conflict: true, ConflictURLs: []string{
				"https://github.com/owner/repo/issues/1", "https://github.com/owner/repo/issues/2",
			},
		},
	}}}
	clearRepositoryFindingIssueAssociation(nil, "rrf_issue", "", now)
	clearRepositoryFindingIssueAssociation(&issueState, "", "", now)
	clearRepositoryFindingIssueAssociation(&issueState, "missing", "", now)
	clearRepositoryFindingIssueAssociation(&issueState, "rrf_issue", "https://github.com/owner/repo/issues/3", now)
	clearRepositoryFindingIssueAssociation(&issueState, "rrf_issue", "https://github.com/owner/repo/issues/2", now)
	if issueState.RepositoryFindings[0].Issue.State != RepositoryFindingIssueNone ||
		issueState.RepositoryFindings[0].Lifecycle != RepositoryFindingOpen {
		t.Fatalf("cleared association = %#v", issueState.RepositoryFindings[0])
	}

	left := RepositoryFindingIssueAssociation{
		URL: "https://example.test/one", State: RepositoryFindingIssueOpen, SnapshotAt: now,
	}
	right := RepositoryFindingIssueAssociation{
		URL: "https://example.test/one", State: RepositoryFindingIssueClosed, SnapshotAt: now.Add(time.Minute),
	}
	if merged := mergeRepositoryIssueAssociations(left, right); merged.State != RepositoryFindingIssueClosed {
		t.Fatalf("newest same-URL association = %#v", merged)
	}
	if merged := mergeRepositoryIssueAssociations(
		left,
		RepositoryFindingIssueAssociation{State: RepositoryFindingIssueNone},
	); merged.URL != left.URL {
		t.Fatalf("none association replaced existing = %#v", merged)
	}
	if merged := mergeRepositoryIssueAssociations(
		RepositoryFindingIssueAssociation{State: RepositoryFindingIssueDraft}, right,
	); merged.URL != right.URL {
		t.Fatalf("URL-less association won = %#v", merged)
	}
}

func TestMappingWorkerRegressionDeferralAndRecoveryCoverage(t *testing.T) {
	store := newRepositoryAuditTestStore(t)
	clock := repositoryAuditTestNow
	store.now = func() time.Time { return clock }
	if _, err := store.ProcessPendingMappingJobs(nil, "owner/missing", RepositoryMappingProcessOptions{}); err == nil {
		t.Fatal("missing mapping ledger accepted")
	}

	base := recordMappingWorkerFinding(t, store, "coverage-base", strings.Repeat("1", 40), "queue.go", "awaiter.signal")
	result, err := store.ProcessPendingMappingJobs(nil, base.Repository, RepositoryMappingProcessOptions{
		DefaultBranchVerified: func(context.Context, Finding) (bool, error) { return true, nil },
	})
	if err != nil || result.Created != 1 {
		t.Fatalf("base mapping result=%#v err=%v", result, err)
	}
	state, _, err := store.Get(base.Repository)
	if err != nil {
		t.Fatal(err)
	}
	aggregate := state.RepositoryFindings[0]
	// Mark the aggregate as confirmed using the durable validation boundary.
	state, jobs, err := store.ReserveValidationJobs(
		state.Repository, []string{aggregate.ID}, RepositoryMappingModelSnapshot{},
	)
	if err != nil {
		t.Fatal(err)
	}
	state, validation, _, claimed, err := store.ClaimValidationJob(state.Repository, jobs[0].ID)
	if err != nil || !claimed {
		t.Fatalf("validation claim=%v err=%v", claimed, err)
	}
	fix := strings.Repeat("f", 40)
	state, validation, err = store.SetValidationJobCandidates(state.Repository, validation.ID, []string{fix})
	if err != nil {
		t.Fatal(err)
	}
	_, aggregate, _, err = store.CompleteValidationJob(state.Repository, RepositoryValidationCompletion{
		JobID: validation.ID, Outcome: RepositoryValidationConfirmed, SelectedCommitSHA: fix,
		FixCommitTime: time.Now().UTC().Add(-time.Hour), Summary: "confirmed",
	})
	if err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(time.Minute)
	later := recordMappingWorkerFinding(
		t,
		store,
		"coverage-regression",
		strings.Repeat("2", 40),
		"queue.go",
		"awaiter.signal",
	)
	regressionCalls := 0
	result, err = store.ProcessPendingMappingJobs(t.Context(), later.Repository, RepositoryMappingProcessOptions{
		DefaultBranchVerified: func(context.Context, Finding) (bool, error) { return true, nil },
		RegressionVerified: func(_ context.Context, occurrence Finding, candidate RepositoryFinding) (bool, error) {
			regressionCalls++
			if candidate.ID != aggregate.ID || occurrence.ID == "" {
				t.Fatalf("regression callback inputs = %#v %#v", occurrence, candidate)
			}
			return true, nil
		},
	})
	if err != nil || result.Associated != 1 || regressionCalls != 1 {
		t.Fatalf("regression mapping result=%#v calls=%d err=%v", result, regressionCalls, err)
	}
	state, _, _ = store.Get(later.Repository)
	regressed := state.RepositoryFindings[repositoryFindingIndexByID(state.RepositoryFindings, aggregate.ID)]
	if regressed.Lifecycle != RepositoryFindingRegressed {
		t.Fatalf("regressed aggregate = %#v", regressed)
	}

	branchStore := newRepositoryAuditTestStore(t)
	branch := recordMappingWorkerFinding(
		t,
		branchStore,
		"coverage-deferred",
		strings.Repeat("3", 40),
		"branch.go",
		"branch.signal",
	)
	result, err = branchStore.ProcessPendingMappingJobs(t.Context(), branch.Repository, RepositoryMappingProcessOptions{
		DefaultBranchVerified: func(context.Context, Finding) (bool, error) { return false, nil },
	})
	if err != nil || result.Deferred != 1 || result.Completed != 0 {
		t.Fatalf("unverified mapping result=%#v err=%v", result, err)
	}
	branchState, _, _ := branchStore.Get(branch.Repository)
	branchJob := branchState.MappingJobs[0]
	if branchJob.State != RepositoryMappingPending || branchJob.Error == "" {
		t.Fatalf("deferred mapping job = %#v", branchJob)
	}

	errorStore := newRepositoryAuditTestStore(t)
	errorState := recordMappingWorkerFinding(
		t,
		errorStore,
		"coverage-verifier-error",
		strings.Repeat("4", 40),
		"error.go",
		"error.signal",
	)
	result, err = errorStore.ProcessPendingMappingJobs(
		t.Context(),
		errorState.Repository,
		RepositoryMappingProcessOptions{
			DefaultBranchVerified: func(context.Context, Finding) (bool, error) { return false, errors.New("git unavailable") },
		},
	)
	if err == nil || result.PendingAI != 1 {
		t.Fatalf("verifier failure result=%#v err=%v", result, err)
	}
	if err := errorStore.releaseMappingJob(errorState.Repository, "missing", errors.New("failure")); err == nil {
		t.Fatal("missing mapping release job accepted")
	}
	current, _, _ := errorStore.Get(errorState.Repository)
	if err := errorStore.releaseMappingJob(errorState.Repository, current.MappingJobs[0].ID, nil); err != nil {
		t.Fatalf("non-running mapping release: %v", err)
	}
}

//nolint:govet // Independent retry subtests intentionally reuse short-lived error names.
func TestValidationWorkerFailureRetryAndFrozenEvidenceCoverage(t *testing.T) {
	if _, err := NewStore(
		t.TempDir(),
	).ProcessPendingValidationJobs(nil, "owner/repo", RepositoryValidationProcessOptions{}); err == nil {
		t.Fatal("incomplete validation processor accepted")
	}
	completeOptions := RepositoryValidationProcessOptions{
		Evidence: func(context.Context, RepositoryFinding, []string) ([]RepositoryValidationEvidence, error) {
			return nil, nil
		},
		Adjudicate: func(context.Context, RepositoryMappingModelSnapshot, RepositoryFinding, []RepositoryValidationEvidence) (RepositoryValidationDecision, error) {
			return RepositoryValidationDecision{Outcome: RepositoryValidationNotFixed}, nil
		},
		VerifyAncestry: func(context.Context, string) (bool, error) { return true, nil },
	}
	if _, err := NewStore(t.TempDir()).ProcessPendingValidationJobs(nil, "owner/missing", completeOptions); err == nil {
		t.Fatal("missing validation ledger accepted")
	}

	newValidationStore := func(t *testing.T, run string) (Store, string, string) {
		t.Helper()
		store := NewStore(t.TempDir())
		state, occurrence := recordLifecycleFinding(
			t, store, strings.Repeat("a", 40), strings.Repeat("b", 40), run,
			"main", "main", true, "validation coverage defect",
		)
		mapping := lifecycleJobForFinding(t, state, occurrence.ID)
		mapping = claimLifecycleMappingJob(t, store, state.Repository, mapping, RepositoryMappingModelSnapshot{})
		state, aggregate, err := store.CompleteMappingJob(state.Repository, RepositoryMappingCompletion{
			JobID: mapping.ID, CreateMatchState: RepositoryMatchNew, DefaultBranchVerified: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, jobs, err := store.ReserveValidationJobs(
			state.Repository,
			[]string{aggregate.ID},
			RepositoryMappingModelSnapshot{},
		)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("reserve validation = %#v err=%v", jobs, err)
		}
		return store, state.Repository, jobs[0].ID
	}

	t.Run("provider error becomes durable failure", func(t *testing.T) {
		store, repository, _ := newValidationStore(t, "provider-error")
		result, err := store.ProcessPendingValidationJobs(t.Context(), repository, RepositoryValidationProcessOptions{
			Evidence: func(context.Context, RepositoryFinding, []string) ([]RepositoryValidationEvidence, error) {
				return nil, errors.New("provider secret")
			},
			Adjudicate: completeOptions.Adjudicate, VerifyAncestry: completeOptions.VerifyAncestry,
		})
		if err != nil || result.Failed != 1 || result.Completed != 1 {
			t.Fatalf("provider failure result=%#v err=%v", result, err)
		}
	})

	t.Run("invalid evidence fails safely", func(t *testing.T) {
		store, repository, _ := newValidationStore(t, "evidence-invalid")
		result, err := store.ProcessPendingValidationJobs(t.Context(), repository, RepositoryValidationProcessOptions{
			Evidence: func(context.Context, RepositoryFinding, []string) ([]RepositoryValidationEvidence, error) {
				return []RepositoryValidationEvidence{{CommitSHA: "not-a-commit"}}, nil
			},
			Adjudicate: completeOptions.Adjudicate, VerifyAncestry: completeOptions.VerifyAncestry,
		})
		if err != nil || result.Failed != 1 {
			t.Fatalf("invalid evidence result=%#v err=%v", result, err)
		}
	})

	t.Run("duplicate evidence is de-duplicated", func(t *testing.T) {
		store, repository, _ := newValidationStore(t, "evidence-duplicate")
		commit := strings.Repeat("c", 40)
		result, err := store.ProcessPendingValidationJobs(t.Context(), repository, RepositoryValidationProcessOptions{
			Evidence: func(context.Context, RepositoryFinding, []string) ([]RepositoryValidationEvidence, error) {
				return []RepositoryValidationEvidence{{CommitSHA: commit}, {CommitSHA: strings.ToUpper(commit)}}, nil
			},
			Adjudicate: completeOptions.Adjudicate, VerifyAncestry: completeOptions.VerifyAncestry,
		})
		if err != nil || result.NotFixed != 1 {
			t.Fatalf("duplicate evidence result=%#v err=%v", result, err)
		}
	})

	t.Run("current source record is not a selectable commit", func(t *testing.T) {
		store, repository, _ := newValidationStore(t, "current-source")
		result, err := store.ProcessPendingValidationJobs(t.Context(), repository, RepositoryValidationProcessOptions{
			Evidence: func(context.Context, RepositoryFinding, []string) ([]RepositoryValidationEvidence, error) {
				return []RepositoryValidationEvidence{{CurrentSource: "package scheduler"}}, nil
			},
			Adjudicate: completeOptions.Adjudicate, VerifyAncestry: completeOptions.VerifyAncestry,
		})
		if err != nil || result.NotFixed != 1 {
			t.Fatalf("current source result=%#v err=%v", result, err)
		}
	})

	t.Run("adjudication and confirmation fences", func(t *testing.T) {
		cases := []struct {
			name       string
			decision   RepositoryValidationDecision
			adjudicate RepositoryValidationAdjudicator
			verify     func(context.Context, string) (bool, error)
			tag        func(context.Context, string) (string, error)
		}{
			{
				name: "adjudicator error",
				adjudicate: func(context.Context, RepositoryMappingModelSnapshot, RepositoryFinding, []RepositoryValidationEvidence) (RepositoryValidationDecision, error) {
					return RepositoryValidationDecision{}, errors.New("model timeout")
				},
			},
			{
				name: "non-confirmed selection",
				decision: RepositoryValidationDecision{
					Outcome:           RepositoryValidationInconclusive,
					SelectedCommitSHA: strings.Repeat("d", 40),
				},
			},
			{
				name: "unsupplied selection",
				decision: RepositoryValidationDecision{
					Outcome:           RepositoryValidationConfirmed,
					SelectedCommitSHA: strings.Repeat("e", 40),
				},
			},
			{
				name: "unreachable selection",
				decision: RepositoryValidationDecision{
					Outcome:           RepositoryValidationConfirmed,
					SelectedCommitSHA: strings.Repeat("d", 40),
				},
				verify: func(context.Context, string) (bool, error) { return false, nil },
			},
			{
				name: "ancestry error",
				decision: RepositoryValidationDecision{
					Outcome:           RepositoryValidationConfirmed,
					SelectedCommitSHA: strings.Repeat("d", 40),
				},
				verify: func(context.Context, string) (bool, error) { return false, errors.New("git failed") },
			},
			{
				name: "tag error",
				decision: RepositoryValidationDecision{
					Outcome:           RepositoryValidationConfirmed,
					SelectedCommitSHA: strings.Repeat("d", 40),
				},
				tag: func(context.Context, string) (string, error) { return "", errors.New("tag failed") },
			},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				store, repository, _ := newValidationStore(t, "fence-"+strings.ReplaceAll(test.name, " ", "-"))
				adjudicate := test.adjudicate
				if adjudicate == nil {
					adjudicate = func(context.Context, RepositoryMappingModelSnapshot, RepositoryFinding, []RepositoryValidationEvidence) (RepositoryValidationDecision, error) {
						return test.decision, nil
					}
				}
				verify := test.verify
				if verify == nil {
					verify = func(context.Context, string) (bool, error) { return true, nil }
				}
				result, err := store.ProcessPendingValidationJobs(
					t.Context(),
					repository,
					RepositoryValidationProcessOptions{
						Evidence: func(context.Context, RepositoryFinding, []string) ([]RepositoryValidationEvidence, error) {
							return []RepositoryValidationEvidence{
								{CommitSHA: strings.Repeat("d", 40), CommitTime: time.Now().UTC()},
							}, nil
						},
						Adjudicate:       adjudicate,
						VerifyAncestry:   verify,
						FirstSemanticTag: test.tag,
					},
				)
				if err != nil || result.Failed != 1 {
					t.Fatalf("fence result=%#v err=%v", result, err)
				}
			})
		}
	})

	t.Run("frozen evidence is reproduced after restart", func(t *testing.T) {
		store, repository, jobID := newValidationStore(t, "frozen-restart")
		state, running, _, claimed, err := store.ClaimValidationJob(repository, jobID)
		if err != nil || !claimed {
			t.Fatalf("claim=%v err=%v", claimed, err)
		}
		commit := strings.Repeat("9", 40)
		if _, _, err := store.SetValidationJobCandidates(repository, running.ID, []string{commit}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReconcileJobs(context.Background()); err != nil {
			t.Fatal(err)
		}
		seenFrozen := false
		result, err := store.ProcessPendingValidationJobs(t.Context(), repository, RepositoryValidationProcessOptions{
			Evidence: func(_ context.Context, _ RepositoryFinding, frozen []string) ([]RepositoryValidationEvidence, error) {
				seenFrozen = slices.Equal(frozen, []string{commit})
				return []RepositoryValidationEvidence{{CommitSHA: commit, CommitTime: time.Now().UTC()}}, nil
			},
			Adjudicate: func(context.Context, RepositoryMappingModelSnapshot, RepositoryFinding, []RepositoryValidationEvidence) (RepositoryValidationDecision, error) {
				return RepositoryValidationDecision{Outcome: RepositoryValidationInconclusive, Summary: "no proof"}, nil
			},
			VerifyAncestry: completeOptions.VerifyAncestry,
		})
		if err != nil || result.Completed != 1 || !seenFrozen {
			t.Fatalf("frozen result=%#v seen=%v state=%#v err=%v", result, seenFrozen, state, err)
		}
	})

	t.Run("cancellation releases running job", func(t *testing.T) {
		store, repository, jobID := newValidationStore(t, "cancel-release")
		ctx, cancel := context.WithCancel(context.Background())
		result, err := store.ProcessPendingValidationJobs(ctx, repository, RepositoryValidationProcessOptions{
			Evidence: func(context.Context, RepositoryFinding, []string) ([]RepositoryValidationEvidence, error) {
				cancel()
				return nil, context.Canceled
			},
			Adjudicate: completeOptions.Adjudicate, VerifyAncestry: completeOptions.VerifyAncestry,
		})
		if err == nil || result.Completed != 0 {
			t.Fatalf("canceled result=%#v err=%v", result, err)
		}
		state, _, _ := store.Get(repository)
		job := lifecycleValidationJobByID(t, state, jobID)
		if job.State != RepositoryValidationPending || !job.ReservedAt.IsZero() {
			t.Fatalf("released validation job = %#v", job)
		}
		if err := store.releaseValidationJob(repository, jobID); err != nil {
			t.Fatalf("non-running release: %v", err)
		}
		if err := store.releaseValidationJob(repository, "missing"); err == nil {
			t.Fatal("missing validation release job accepted")
		}
	})
}
