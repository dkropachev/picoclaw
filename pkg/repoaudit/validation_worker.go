package repoaudit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type RepositoryValidationEvidence struct {
	CommitSHA     string    `json:"commit_sha"`
	CommitTime    time.Time `json:"commit_time"`
	Summary       string    `json:"summary,omitempty"`
	Diff          string    `json:"diff,omitempty"`
	CurrentSource string    `json:"current_source,omitempty"`
}

type RepositoryValidationDecision struct {
	Outcome           RepositoryFindingValidationState `json:"outcome"`
	SelectedCommitSHA string                           `json:"selected_commit_sha,omitempty"`
	Summary           string                           `json:"summary,omitempty"`
}

type RepositoryValidationEvidenceProvider func(
	context.Context,
	RepositoryFinding,
	[]string,
) ([]RepositoryValidationEvidence, error)

type RepositoryValidationAdjudicator func(
	context.Context,
	RepositoryMappingModelSnapshot,
	RepositoryFinding,
	[]RepositoryValidationEvidence,
) (RepositoryValidationDecision, error)

type RepositoryValidationProcessOptions struct {
	Evidence         RepositoryValidationEvidenceProvider
	Adjudicate       RepositoryValidationAdjudicator
	VerifyAncestry   func(context.Context, string) (bool, error)
	FirstSemanticTag func(context.Context, string) (string, error)
}

type RepositoryValidationProcessResult struct {
	Completed int `json:"completed"`
	Confirmed int `json:"confirmed"`
	NotFixed  int `json:"not_fixed"`
	Failed    int `json:"failed"`
}

// ProcessPendingValidationJobs dispatches at most four validators. Provider
// work happens outside ledger locks; SetValidationJobCandidates freezes the
// exact commit universe before the AI call, and CompleteValidationJob enforces
// that a confirmed selection came from that universe.
func (s Store) ProcessPendingValidationJobs(
	ctx context.Context,
	repository string,
	options RepositoryValidationProcessOptions,
) (RepositoryValidationProcessResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if options.Evidence == nil || options.Adjudicate == nil || options.VerifyAncestry == nil {
		return RepositoryValidationProcessResult{}, errors.New("validation processor is incomplete")
	}
	state, found, err := s.Get(repository)
	if err != nil || !found {
		if err == nil {
			err = errors.New("repository review state not found")
		}
		return RepositoryValidationProcessResult{}, err
	}
	jobIDs := make([]string, 0, len(state.ValidationJobs))
	for _, job := range state.ValidationJobs {
		if job.State == RepositoryValidationPending {
			jobIDs = append(jobIDs, job.ID)
		}
	}
	if len(jobIDs) == 0 {
		return RepositoryValidationProcessResult{}, nil
	}
	workers := min(RepositoryValidationConcurrency, len(jobIDs))
	jobs := make(chan string)
	type outcome struct {
		state RepositoryFindingValidationState
		err   error
	}
	outcomes := make(chan outcome, len(jobIDs))
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for jobID := range jobs {
				terminal, processErr := s.processValidationJob(ctx, repository, jobID, options)
				outcomes <- outcome{state: terminal, err: processErr}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, jobID := range jobIDs {
			select {
			case jobs <- jobID:
			case <-ctx.Done():
				return
			}
		}
	}()
	wait.Wait()
	close(outcomes)
	result := RepositoryValidationProcessResult{}
	var joined error
	for item := range outcomes {
		if item.err != nil {
			joined = errors.Join(joined, item.err)
			continue
		}
		if !repositoryValidationTerminal(item.state) {
			continue
		}
		result.Completed++
		switch item.state {
		case RepositoryValidationConfirmed:
			result.Confirmed++
		case RepositoryValidationNotFixed:
			result.NotFixed++
		case RepositoryValidationFailed:
			result.Failed++
		}
	}
	if ctx.Err() != nil {
		joined = errors.Join(joined, ctx.Err())
	}
	return result, joined
}

func (s Store) processValidationJob(
	ctx context.Context,
	repository, jobID string,
	options RepositoryValidationProcessOptions,
) (RepositoryFindingValidationState, error) {
	releaseSlot, err := s.AcquireValidationSlot(ctx)
	if err != nil {
		return "", err
	}
	defer releaseSlot()
	_, job, finding, claimed, err := s.ClaimValidationJob(repository, jobID)
	if err != nil {
		return "", err
	}
	if !claimed {
		return job.State, nil
	}
	frozenCommits := job.CandidateCommits
	if frozenCommits != nil {
		frozenCommits = append([]string{}, frozenCommits...)
	}
	evidence, err := options.Evidence(ctx, finding, frozenCommits)
	if err != nil {
		return s.failValidationJob(ctx, repository, job.ID, err)
	}
	if len(evidence) > maxValidationCandidateCommits {
		evidence = evidence[:maxValidationCandidateCommits]
	}
	commits := make([]string, 0, len(evidence))
	byCommit := make(map[string]RepositoryValidationEvidence, len(evidence))
	for _, candidate := range evidence {
		candidate.CommitSHA = strings.ToLower(strings.TrimSpace(candidate.CommitSHA))
		if candidate.CommitSHA == "" && candidate.CurrentSource != "" &&
			candidate.Summary == "" && candidate.Diff == "" {
			continue
		}
		if !validRepositoryReviewCommitSHA(candidate.CommitSHA) {
			return s.failValidationJob(ctx, repository, job.ID, errors.New("validation evidence has an invalid commit"))
		}
		if _, duplicate := byCommit[candidate.CommitSHA]; duplicate {
			continue
		}
		candidate.CommitTime = candidate.CommitTime.UTC()
		commits = append(commits, candidate.CommitSHA)
		byCommit[candidate.CommitSHA] = candidate
	}
	if job.CandidateCommits == nil {
		if _, _, setErr := s.SetValidationJobCandidates(repository, job.ID, commits); setErr != nil {
			return s.failValidationJob(ctx, repository, job.ID, setErr)
		}
	} else if !stringSlicesEqual(job.CandidateCommits, commits) {
		return s.failValidationJob(
			ctx,
			repository, job.ID, errors.New("frozen validation evidence could not be reproduced"),
		)
	}
	decision, err := options.Adjudicate(ctx, job.ModelSnapshot, finding, evidence)
	if err != nil {
		return s.failValidationJob(ctx, repository, job.ID, err)
	}
	decision.SelectedCommitSHA = strings.ToLower(strings.TrimSpace(decision.SelectedCommitSHA))
	decision.Summary = strings.TrimSpace(decision.Summary)
	completion := RepositoryValidationCompletion{
		JobID: job.ID, Outcome: decision.Outcome, Summary: decision.Summary,
	}
	if decision.Outcome != RepositoryValidationConfirmed && decision.SelectedCommitSHA != "" {
		return s.failValidationJob(
			ctx, repository, job.ID,
			errors.New("non-confirmed validation selected a commit"),
		)
	}
	if decision.Outcome == RepositoryValidationConfirmed {
		candidate, supplied := byCommit[decision.SelectedCommitSHA]
		if !supplied {
			return s.failValidationJob(ctx, repository, job.ID, errors.New("validator selected an unsupplied commit"))
		}
		reachable, verifyErr := options.VerifyAncestry(ctx, decision.SelectedCommitSHA)
		if verifyErr != nil || !reachable {
			if verifyErr == nil {
				verifyErr = errors.New("selected fix commit is not on the default branch")
			}
			return s.failValidationJob(ctx, repository, job.ID, verifyErr)
		}
		completion.SelectedCommitSHA = decision.SelectedCommitSHA
		completion.FixCommitTime = candidate.CommitTime
		if options.FirstSemanticTag != nil {
			completion.FirstContainingTag, err = options.FirstSemanticTag(ctx, decision.SelectedCommitSHA)
			if err != nil {
				return s.failValidationJob(ctx, repository, job.ID, err)
			}
		}
	}
	_, _, completed, err := s.CompleteValidationJob(repository, completion)
	if err != nil {
		if errors.Is(err, errRepositoryValidationEvidenceChanged) {
			return RepositoryValidationPending, nil
		}
		return s.failValidationJob(ctx, repository, job.ID, err)
	}
	return completed.State, nil
}

func (s Store) failValidationJob(
	ctx context.Context,
	repository, jobID string,
	cause error,
) (RepositoryFindingValidationState, error) {
	if ctx != nil && ctx.Err() != nil {
		releaseErr := s.releaseValidationJob(repository, jobID)
		return RepositoryValidationPending, errors.Join(ctx.Err(), releaseErr)
	}
	_, _, completed, err := s.CompleteValidationJob(repository, RepositoryValidationCompletion{
		JobID: jobID, Outcome: RepositoryValidationFailed,
		Error: "Validation failed.", Summary: "Validation could not reach a conclusive result.",
	})
	if err != nil {
		if errors.Is(err, errRepositoryValidationEvidenceChanged) {
			return RepositoryValidationPending, nil
		}
		return "", errors.Join(cause, err)
	}
	return completed.State, nil
}

func (s Store) releaseValidationJob(repository, jobID string) error {
	unlock, err := s.lock(repository)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return err
	}
	jobIndex := validationJobIndexByID(state.ValidationJobs, jobID)
	if jobIndex < 0 {
		return errors.New("validation job not found")
	}
	job := &state.ValidationJobs[jobIndex]
	if job.State != RepositoryValidationRunning {
		return nil
	}
	findingIndex := repositoryFindingIndexByID(state.RepositoryFindings, job.RepositoryFindingID)
	if findingIndex < 0 {
		return errors.New("validation job repository finding is missing")
	}
	finding := &state.RepositoryFindings[findingIndex]
	if job.FindingVersion != finding.Version {
		job.CandidateCommits = nil
	}
	now := s.clock()
	job.State = RepositoryValidationPending
	job.Error = ""
	job.ReservedAt = time.Time{}
	job.UpdatedAt = now
	finding.ValidationState = RepositoryValidationPending
	finding.Version++
	finding.UpdatedAt = now
	job.FindingVersion = finding.Version
	state.Version++
	state.UpdatedAt = now
	return s.save(&state)
}
