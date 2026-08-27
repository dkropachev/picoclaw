package repoaudit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type RepositoryMappingAICandidate struct {
	ID      string            `json:"id"`
	Finding RepositoryFinding `json:"finding"`
}

type RepositoryMappingAIRequest struct {
	Finding    Finding                        `json:"finding"`
	Candidates []RepositoryMappingAICandidate `json:"candidates"`
}

type RepositoryMappingAdjudicator func(
	context.Context,
	RepositoryMappingModelSnapshot,
	RepositoryMappingAIRequest,
) (RepositoryMappingAdjudication, error)

type RepositoryMappingProcessOptions struct {
	ModelSnapshot         RepositoryMappingModelSnapshot
	RenameEquivalent      RepositoryPathEquivalent
	Adjudicate            RepositoryMappingAdjudicator
	DefaultBranchVerified func(context.Context, Finding) (bool, error)
	RegressionVerified    func(context.Context, Finding, RepositoryFinding) (bool, error)
}

type RepositoryMappingProcessResult struct {
	Completed   int `json:"completed"`
	Associated  int `json:"associated"`
	Created     int `json:"created"`
	Provisional int `json:"provisional"`
	PendingAI   int `json:"pending_ai"`
	Deferred    int `json:"deferred"`
}

// ProcessPendingMappingJobs runs CPU matching and, when configured, isolated
// AI adjudication outside store locks. Every final association is committed by
// CompleteMappingJob together with job completion. Failed/malformed AI calls
// return the job to pending with a bounded safe error.
func (s Store) ProcessPendingMappingJobs(
	ctx context.Context,
	repository string,
	options RepositoryMappingProcessOptions,
) (RepositoryMappingProcessResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	state, found, err := s.Get(repository)
	if err != nil || !found {
		if err == nil {
			err = errors.New("repository review state not found")
		}
		return RepositoryMappingProcessResult{}, err
	}
	jobIDs := make([]string, 0, len(state.MappingJobs))
	jobSnapshots := make(map[string]RepositoryMappingModelSnapshot, len(state.MappingJobs))
	for _, job := range state.MappingJobs {
		if job.State == RepositoryMappingPending {
			jobIDs = append(jobIDs, job.ID)
			jobSnapshots[job.ID] = job.ModelSnapshot
		}
	}
	result := RepositoryMappingProcessResult{}
	var firstErr error
	for _, jobID := range jobIDs {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		claimSnapshot := options.ModelSnapshot
		if existing := jobSnapshots[jobID]; !mappingModelSnapshotEmpty(existing) {
			claimSnapshot = existing
		}
		claimedState, job, occurrence, claimed, claimErr := s.ClaimMappingJob(
			repository, jobID, claimSnapshot,
		)
		if claimErr != nil {
			return result, claimErr
		}
		if !claimed {
			continue
		}
		completion, _, completionErr := s.repositoryMappingCompletionForJob(
			ctx, claimedState, job, occurrence, options,
		)
		if completionErr != nil {
			if releaseErr := s.releaseMappingJob(
				repository,
				job.ID,
				completionErr,
			); releaseErr != nil &&
				firstErr == nil {
				firstErr = releaseErr
			}
			if errors.Is(completionErr, errRepositoryMappingNeedsAI) {
				result.PendingAI++
				continue
			}
			result.PendingAI++
			if firstErr == nil {
				firstErr = completionErr
			}
			continue
		}
		if completion.ExpectedUniverse == "" {
			completion.ExpectedUniverse = repositoryMatchingUniverseFingerprint(
				claimedState.RepositoryFindings,
			)
		}
		if completion.RepositoryFindingID == "" && !occurrenceMayCreateRepositoryFinding(
			occurrence,
			completion.DefaultBranchVerified,
		) {
			_ = s.releaseMappingJob(repository, job.ID, errors.New(
				"waiting for a matching default-branch repository finding",
			))
			result.Deferred++
			continue
		}
		_, mapped, completeErr := s.CompleteMappingJob(repository, completion)
		if completeErr != nil {
			if errors.Is(completeErr, errRepositoryMappingUniverseChanged) {
				result.Deferred++
				continue
			}
			_ = s.releaseMappingJob(repository, job.ID, completeErr)
			return result, completeErr
		}
		result.Completed++
		createdID := stableID("rrf_", claimedState.Repository, occurrence.ID)
		if completion.RepositoryFindingID == "" && mapped.ID == createdID {
			result.Created++
		} else {
			result.Associated++
		}
		if mapped.MatchState == RepositoryMatchProvisional {
			result.Provisional++
		}
	}
	return result, firstErr
}

var errRepositoryMappingNeedsAI = errors.New("repository mapping requires AI adjudication")

func (s Store) repositoryMappingCompletionForJob(
	ctx context.Context,
	state RepositoryState,
	job RepositoryMappingJob,
	occurrence Finding,
	options RepositoryMappingProcessOptions,
) (RepositoryMappingCompletion, string, error) {
	verified := false
	if options.DefaultBranchVerified != nil {
		var err error
		verified, err = options.DefaultBranchVerified(ctx, occurrence)
		if err != nil {
			return RepositoryMappingCompletion{}, "", err
		}
	}
	if !mappingAdjudicationEmpty(job.Adjudication) {
		completion := repositoryCompletionFromAdjudication(
			job.ID, job.Adjudication, nil, verified,
		)
		completion.ExpectedUniverse = job.CandidateUniverse
		completion, err := repositoryMappingRegressionCompletion(
			ctx, state, occurrence, completion, options,
		)
		return completion, "ai", err
	}
	occurrences := make(map[string]Finding, len(state.Findings))
	for _, finding := range state.Findings {
		occurrences[finding.ID] = finding
	}
	matched := MatchRepositoryFinding(
		occurrence, state.RepositoryFindings, occurrences, options.RenameEquivalent,
	)
	if matched.RepositoryFindingID != "" {
		completion := RepositoryMappingCompletion{
			JobID: job.ID, RepositoryFindingID: matched.RepositoryFindingID,
			DefaultBranchVerified: verified,
		}
		completion, err := repositoryMappingRegressionCompletion(
			ctx, state, occurrence, completion, options,
		)
		return completion, matched.Method, err
	}
	if matched.Method == "distinct" {
		return RepositoryMappingCompletion{
			JobID: job.ID, CreateMatchState: RepositoryMatchNew,
			DefaultBranchVerified: verified,
		}, matched.Method, nil
	}
	if options.Adjudicate == nil {
		return RepositoryMappingCompletion{}, matched.Method, errRepositoryMappingNeedsAI
	}
	request, opaqueToCanonical := repositoryOpaqueMappingRequest(
		occurrence, state.RepositoryFindings, matched.Candidates,
	)
	adjudication, err := options.Adjudicate(ctx, job.ModelSnapshot, request)
	if err != nil {
		return RepositoryMappingCompletion{}, matched.Method, fmt.Errorf("mapping adjudication failed: %w", err)
	}
	adjudication = normalizeMappingAdjudication(adjudication)
	opaqueIDs := make([]string, 0, len(request.Candidates))
	for _, candidate := range request.Candidates {
		opaqueIDs = append(opaqueIDs, candidate.ID)
	}
	if validationErr := ValidateRepositoryMappingAdjudication(adjudication, opaqueIDs); validationErr != nil {
		return RepositoryMappingCompletion{}, matched.Method, validationErr
	}
	if adjudication.CandidateID != "" {
		adjudication.CandidateID = opaqueToCanonical[adjudication.CandidateID]
	}
	if adjudication.Decision == "same" &&
		(adjudication.Confidence < 0.90 || len(adjudication.ConflictingAnchors) > 0) {
		adjudication.Decision = "uncertain"
	}
	universe := repositoryMatchingUniverseFingerprint(state.RepositoryFindings)
	if _, _, saveErr := s.SaveMappingAdjudication(
		state.Repository, job.ID, adjudication, universe,
	); saveErr != nil {
		return RepositoryMappingCompletion{}, matched.Method, saveErr
	}
	completion := repositoryCompletionFromAdjudication(
		job.ID, adjudication, matched.Candidates, verified,
	)
	completion.ExpectedUniverse = universe
	completion, err = repositoryMappingRegressionCompletion(
		ctx, state, occurrence, completion, options,
	)
	return completion, matched.Method, err
}

func repositoryMappingRegressionCompletion(
	ctx context.Context,
	state RepositoryState,
	occurrence Finding,
	completion RepositoryMappingCompletion,
	options RepositoryMappingProcessOptions,
) (RepositoryMappingCompletion, error) {
	if options.RegressionVerified == nil || !completion.DefaultBranchVerified {
		return completion, nil
	}
	candidateID := completion.RepositoryFindingID
	if candidateID == "" && len(completion.PossibleDuplicates) > 0 &&
		(completion.PossibleDuplicates[0].Relation == "same" ||
			completion.PossibleDuplicates[0].Relation == "uncertain") {
		candidateID = completion.PossibleDuplicates[0].CandidateID
	}
	index := repositoryFindingIndexByID(state.RepositoryFindings, candidateID)
	if index < 0 || state.RepositoryFindings[index].ValidationState != RepositoryValidationConfirmed {
		return completion, nil
	}
	verified, err := options.RegressionVerified(ctx, occurrence, state.RepositoryFindings[index])
	if err != nil {
		return RepositoryMappingCompletion{}, err
	}
	completion.RegressionVerified = verified
	if verified {
		completion.RegressionFixCommit = strings.ToLower(strings.TrimSpace(
			state.RepositoryFindings[index].FixCommitSHA,
		))
		completion.RegressionFindingID = state.RepositoryFindings[index].ID
	}
	return completion, nil
}

func repositoryOpaqueMappingRequest(
	occurrence Finding,
	findings []RepositoryFinding,
	candidates []RepositoryMatchCandidate,
) (RepositoryMappingAIRequest, map[string]string) {
	byID := make(map[string]RepositoryFinding, len(findings))
	for _, finding := range findings {
		byID[finding.ID] = finding
	}
	request := RepositoryMappingAIRequest{Finding: occurrence}
	request.Finding.RepositoryFindingID = ""
	request.Finding.RepositoryMatchState = ""
	mapping := make(map[string]string, len(candidates))
	for index, candidate := range candidates {
		finding, ok := byID[candidate.ID]
		if !ok {
			continue
		}
		opaqueID := fmt.Sprintf("candidate_%d", index+1)
		mapping[opaqueID] = candidate.ID
		finding.ID = ""
		finding.Repository = ""
		finding.Issue = RepositoryFindingIssueAssociation{}
		finding.PossibleDuplicates = nil
		finding.ResolutionHistory = nil
		finding.FixCommitSHA = ""
		finding.FixCommitTime = time.Time{}
		finding.FirstContainingTag = ""
		finding.Version = 0
		finding.CreatedAt = time.Time{}
		finding.UpdatedAt = time.Time{}
		for historyIndex := range finding.PathSymbolHistory {
			finding.PathSymbolHistory[historyIndex].ReviewFindingID = ""
		}
		finding.ReviewFindingIDs = nil
		request.Candidates = append(request.Candidates, RepositoryMappingAICandidate{
			ID: opaqueID, Finding: finding,
		})
	}
	return request, mapping
}

func repositoryCompletionFromAdjudication(
	jobID string,
	adjudication RepositoryMappingAdjudication,
	candidates []RepositoryMatchCandidate,
	defaultVerified bool,
) RepositoryMappingCompletion {
	completion := RepositoryMappingCompletion{
		JobID: jobID, DefaultBranchVerified: defaultVerified,
	}
	duplicate := func(relation string) RepositoryFindingPossibleDuplicate {
		return RepositoryFindingPossibleDuplicate{
			CandidateID: adjudication.CandidateID, Relation: relation,
			Confidence:         adjudication.Confidence,
			MatchingAnchors:    append([]string(nil), adjudication.MatchingAnchors...),
			ConflictingAnchors: append([]string(nil), adjudication.ConflictingAnchors...),
			Explanation:        adjudication.Explanation,
		}
	}
	switch adjudication.Decision {
	case "same":
		if adjudication.Confidence >= 0.90 && len(adjudication.ConflictingAnchors) == 0 {
			completion.RepositoryFindingID = adjudication.CandidateID
			return completion
		}
		completion.CreateMatchState = RepositoryMatchProvisional
		completion.PossibleDuplicates = []RepositoryFindingPossibleDuplicate{duplicate("same")}
	case "related":
		completion.CreateMatchState = RepositoryMatchNew
		completion.PossibleDuplicates = []RepositoryFindingPossibleDuplicate{duplicate("related")}
	case "uncertain":
		completion.CreateMatchState = RepositoryMatchProvisional
		completion.PossibleDuplicates = []RepositoryFindingPossibleDuplicate{duplicate("uncertain")}
	case "distinct":
		completion.CreateMatchState = RepositoryMatchNew
	}
	_ = candidates
	return completion
}

func (s Store) releaseMappingJob(repository, jobID string, cause error) error {
	unlock, err := s.lock(repository)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return err
	}
	index := mappingJobIndexByID(state.MappingJobs, jobID)
	if index < 0 {
		return errors.New("mapping job not found")
	}
	job := &state.MappingJobs[index]
	if job.State != RepositoryMappingRunning {
		return nil
	}
	job.State = RepositoryMappingPending
	job.ReservedAt = time.Time{}
	if errors.Is(cause, errRepositoryMappingNeedsAI) {
		job.Error = "AI adjudication is pending."
	} else {
		job.Error = "Mapping adjudication failed."
	}
	job.UpdatedAt = s.clock()
	state.Version++
	state.UpdatedAt = job.UpdatedAt
	return s.save(&state)
}
