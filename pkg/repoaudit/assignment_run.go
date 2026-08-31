package repoaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// BeginRepositoryReviewRunRequest freezes the exact assignment scopes that a
// managed run may dispatch. The plan must have been produced against the
// current campaign and repository review version.
type BeginRepositoryReviewRunRequest struct {
	Plan            Plan      `json:"plan"`
	RunID           string    `json:"run_id"`
	ReviewableFiles []FileRef `json:"reviewable_files,omitempty"`
}

// VerifyRepositoryReviewAssignmentRequest is checked immediately before the
// first provider request for an assignment. Scope must exactly match the
// durable reservation; subsets and supersets both fail closed.
type VerifyRepositoryReviewAssignmentRequest struct {
	Repository   string    `json:"repository"`
	RunID        string    `json:"run_id"`
	AssignmentID string    `json:"assignment_id"`
	Files        []FileRef `json:"files"`
}

// CheckpointRepositoryReviewAssignmentRequest carries one already schema-
// validated child result. The store independently rebinds its immutable scope,
// acknowledged subset, findings, and digest before committing any credit.
type CheckpointRepositoryReviewAssignmentRequest struct {
	Plan              Plan        `json:"plan"`
	RunID             string      `json:"run_id"`
	AssignmentID      string      `json:"assignment_id"`
	Digest            string      `json:"digest"`
	AcknowledgedFiles []FileRef   `json:"acknowledged_files"`
	Observation       Observation `json:"observation"`
	CompletedAt       time.Time   `json:"completed_at,omitempty"`
}

type CheckpointRepositoryReviewAssignmentResult struct {
	State              RepositoryState `json:"state"`
	AcceptedFindingIDs []string        `json:"accepted_finding_ids,omitempty"`
	Idempotent         bool            `json:"idempotent"`
}

// FinalizeRepositoryReviewRunRequest records terminal unsupported files and a
// run summary, then releases all unfinished reservations. Successful child
// checkpoints were already durable and are never rolled back here.
type FinalizeRepositoryReviewRunRequest struct {
	Plan             Plan              `json:"plan"`
	RunID            string            `json:"run_id"`
	UnsupportedFiles []UnsupportedFile `json:"unsupported_files,omitempty"`
	ExcludedFiles    int               `json:"excluded_files,omitempty"`
	CompletedAt      time.Time         `json:"completed_at,omitempty"`
}

func (s Store) BeginRepositoryReviewRun(
	ctx context.Context,
	request BeginRepositoryReviewRunRequest,
) (RepositoryState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return RepositoryState{}, contextErr
	}
	request.RunID = strings.TrimSpace(request.RunID)
	if !validBoundedText(request.RunID, 1024) || request.Plan.ID == "" ||
		request.Plan.ID != planDigest(request.Plan) || len(request.Plan.AssignmentCatalog) == 0 ||
		len(request.Plan.AssignmentPlans) == 0 {
		return RepositoryState{}, ErrInvalidPlan
	}
	if _, err := validateRepositoryReviewCampaignPlan(request.Plan); err != nil {
		return RepositoryState{}, err
	}
	unlock, err := s.lock(request.Plan.Repository)
	if err != nil {
		return RepositoryState{}, err
	}
	defer unlock()
	if contextErr := ctx.Err(); contextErr != nil {
		return RepositoryState{}, contextErr
	}
	state, err := s.load(request.Plan.Repository)
	if err != nil {
		return RepositoryState{}, err
	}
	for _, run := range state.Runs {
		if run.ID == request.RunID {
			return RepositoryState{}, ErrConflict
		}
	}
	active, err := repositoryReviewActiveRunFromPlan(
		request.Plan, request.RunID, request.ReviewableFiles, s.clock(),
	)
	if err != nil {
		return RepositoryState{}, err
	}
	if state.ActiveReviewRun != nil {
		active.StartedAt = state.ActiveReviewRun.StartedAt
		if reflect.DeepEqual(*state.ActiveReviewRun, active) {
			return state, nil
		}
		return RepositoryState{}, ErrConflict
	}
	if state.ReviewVersion != request.Plan.StateVersion || state.CurrentCampaign == nil ||
		state.CurrentCampaign.ID != request.Plan.CampaignID ||
		state.CurrentCampaign.CommitSHA != request.Plan.CommitSHA ||
		state.CurrentCampaign.InventoryHash != request.Plan.InventoryHash ||
		state.CurrentCampaign.ProfileHash != request.Plan.ProfileHash ||
		!repositoryReviewAssignmentCatalogEqual(
			state.CurrentCampaign.AssignmentCatalog, request.Plan.AssignmentCatalog,
		) {
		return RepositoryState{}, ErrConflict
	}
	scopeDigest, scopeErr := repositoryReviewCampaignScopeDigestForPlan(request.Plan)
	selectedFiles, planErr := validateRepositoryReviewCampaignPlan(request.Plan)
	if scopeErr != nil || planErr != nil || scopeDigest != state.CurrentCampaign.ScopeDigest ||
		selectedFiles != state.CurrentCampaign.SelectedFiles {
		return RepositoryState{}, ErrConflict
	}
	for _, reservation := range active.Reservations {
		for _, file := range reservation.Files {
			complete, completeErr := repositoryReviewAssignmentComplete(
				state.CurrentCampaign.Paths[file.Path],
				state.CurrentCampaign.AssignmentCatalog,
				reservation.AssignmentID,
			)
			if completeErr != nil {
				return RepositoryState{}, completeErr
			}
			if complete {
				return RepositoryState{}, fmt.Errorf(
					"%w: assignment %q for %q is already complete",
					ErrConflict, reservation.AssignmentID, file.Path,
				)
			}
		}
	}
	state.ActiveReviewRun = &active
	state.Version++
	state.ReviewVersion++
	state.UpdatedAt = s.clock()
	if err := s.save(&state); err != nil {
		return RepositoryState{}, err
	}
	return state, nil
}

func repositoryReviewActiveRunFromPlan(
	plan Plan,
	runID string,
	reviewableFiles []FileRef,
	startedAt time.Time,
) (RepositoryReviewActiveRun, error) {
	reviewable := make(map[string]FileRef, len(plan.PendingFiles))
	if reviewableFiles == nil {
		for _, file := range plan.PendingFiles {
			reviewable[file.Path] = file
		}
	} else {
		pending := make(map[string]FileRef, len(plan.PendingFiles))
		for _, file := range plan.PendingFiles {
			pending[file.Path] = file
		}
		files, err := bindRepositoryReviewCampaignFiles(reviewableFiles, pending)
		if err != nil || len(files) != len(reviewableFiles) {
			return RepositoryReviewActiveRun{}, ErrInvalidPlan
		}
		for _, file := range files {
			reviewable[file.Path] = file
		}
	}
	reservations := make(map[string]RepositoryReviewAssignmentReservation, len(plan.AssignmentPlans))
	for _, assignmentPlan := range plan.AssignmentPlans {
		files := make([]FileRef, 0, len(assignmentPlan.Files))
		for _, file := range assignmentPlan.Files {
			if reviewable[file.Path] == file {
				files = append(files, file)
			}
		}
		if len(files) == 0 {
			continue
		}
		reservations[assignmentPlan.AssignmentID] = RepositoryReviewAssignmentReservation{
			AssignmentID: assignmentPlan.AssignmentID,
			Files:        files,
		}
	}
	return RepositoryReviewActiveRun{
		ID: runID, CampaignID: plan.CampaignID, PlanID: plan.ID,
		CommitSHA: plan.CommitSHA, InventoryHash: plan.InventoryHash,
		ProfileHash: plan.ProfileHash, Reservations: reservations,
		StartedAt: startedAt.UTC(),
	}, nil
}

func (s Store) VerifyRepositoryReviewAssignment(
	ctx context.Context,
	request VerifyRepositoryReviewAssignmentRequest,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	request.Repository = strings.TrimSpace(request.Repository)
	request.RunID = strings.TrimSpace(request.RunID)
	request.AssignmentID = strings.TrimSpace(request.AssignmentID)
	if !validBoundedText(request.Repository, maxRepositoryIdentityBytes) ||
		!validBoundedText(request.RunID, 1024) ||
		!validBoundedText(request.AssignmentID, 128) {
		return ErrInvalidPlan
	}
	files, err := canonicalRepositoryReviewCampaignFiles(request.Files)
	if err != nil || len(files) == 0 || !reflect.DeepEqual(files, request.Files) {
		return ErrInvalidPlan
	}
	unlock, err := s.lock(request.Repository)
	if err != nil {
		return err
	}
	defer unlock()
	state, err := s.load(request.Repository)
	if err != nil {
		return err
	}
	active := state.ActiveReviewRun
	if active == nil || active.ID != request.RunID || state.CurrentCampaign == nil ||
		active.CampaignID != state.CurrentCampaign.ID {
		return ErrConflict
	}
	reservation, found := active.Reservations[request.AssignmentID]
	if !found || reservation.CheckpointDigest != "" ||
		!reflect.DeepEqual(reservation.Files, files) {
		return ErrConflict
	}
	for _, file := range files {
		complete, completeErr := repositoryReviewAssignmentComplete(
			state.CurrentCampaign.Paths[file.Path],
			state.CurrentCampaign.AssignmentCatalog,
			request.AssignmentID,
		)
		if completeErr != nil {
			return completeErr
		}
		if complete {
			return fmt.Errorf(
				"%w: completed assignment %q for %q cannot be dispatched",
				ErrConflict, request.AssignmentID, file.Path,
			)
		}
	}
	return nil
}

func (s Store) CheckpointRepositoryReviewAssignment(
	ctx context.Context,
	request CheckpointRepositoryReviewAssignmentRequest,
) (CheckpointRepositoryReviewAssignmentResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return CheckpointRepositoryReviewAssignmentResult{}, contextErr
	}
	request.RunID = strings.TrimSpace(request.RunID)
	request.AssignmentID = strings.TrimSpace(request.AssignmentID)
	request.Digest = strings.TrimSpace(request.Digest)
	if !validBoundedText(request.RunID, 1024) ||
		!validBoundedText(request.AssignmentID, 128) ||
		!validRepositoryReviewCheckpointDigest(request.Digest) ||
		request.Plan.ID == "" || request.Plan.ID != planDigest(request.Plan) {
		return CheckpointRepositoryReviewAssignmentResult{}, ErrInvalidPlan
	}
	if _, err := validateRepositoryReviewCampaignPlan(request.Plan); err != nil {
		return CheckpointRepositoryReviewAssignmentResult{}, err
	}
	unlock, err := s.lock(request.Plan.Repository)
	if err != nil {
		return CheckpointRepositoryReviewAssignmentResult{}, err
	}
	defer unlock()
	if contextErr := ctx.Err(); contextErr != nil {
		return CheckpointRepositoryReviewAssignmentResult{}, contextErr
	}
	state, err := s.load(request.Plan.Repository)
	if err != nil {
		return CheckpointRepositoryReviewAssignmentResult{}, err
	}
	active := state.ActiveReviewRun
	var reservation RepositoryReviewAssignmentReservation
	var existingDigest string
	assignmentCatalog := request.Plan.AssignmentCatalog
	if active == nil {
		var finalizedRun *ReviewRun
		for index := range state.Runs {
			if state.Runs[index].ID == request.RunID {
				finalizedRun = &state.Runs[index]
				break
			}
		}
		if finalizedRun == nil || finalizedRun.PlanID != request.Plan.ID ||
			finalizedRun.CampaignID != request.Plan.CampaignID ||
			state.CampaignHistory[request.Plan.CampaignID] != request.Plan.CommitSHA {
			return CheckpointRepositoryReviewAssignmentResult{}, ErrConflict
		}
		checkpointScope, found := finalizedRun.CheckpointScopes[request.AssignmentID]
		if !found {
			return CheckpointRepositoryReviewAssignmentResult{}, ErrConflict
		}
		reservation = RepositoryReviewAssignmentReservation{
			AssignmentID: request.AssignmentID,
			Files:        append([]FileRef(nil), checkpointScope...),
		}
		var digestFound bool
		existingDigest, digestFound = finalizedRun.CheckpointDigests[request.AssignmentID]
		if !digestFound {
			return CheckpointRepositoryReviewAssignmentResult{}, ErrConflict
		}
	} else {
		if active.ID != request.RunID || active.PlanID != request.Plan.ID ||
			active.CampaignID != request.Plan.CampaignID || state.CurrentCampaign == nil ||
			state.CurrentCampaign.ID != active.CampaignID {
			return CheckpointRepositoryReviewAssignmentResult{}, ErrConflict
		}
		assignmentCatalog = state.CurrentCampaign.AssignmentCatalog
		var found bool
		reservation, found = active.Reservations[request.AssignmentID]
		if !found {
			return CheckpointRepositoryReviewAssignmentResult{}, ErrConflict
		}
		existingDigest = reservation.CheckpointDigest
	}
	allowed := make(map[string]FileRef, len(reservation.Files))
	for _, file := range reservation.Files {
		allowed[file.Path] = file
	}
	scope, err := bindRepositoryReviewCampaignFiles(request.Observation.ScopeFiles, allowed)
	if err != nil || !reflect.DeepEqual(scope, reservation.Files) {
		return CheckpointRepositoryReviewAssignmentResult{}, fmt.Errorf(
			"%w: checkpoint observation scope does not match its reservation", ErrInvalidPlan,
		)
	}
	request.Observation.ScopeFiles = scope
	acknowledged, err := bindRepositoryReviewCampaignFiles(request.AcknowledgedFiles, allowed)
	if err != nil {
		return CheckpointRepositoryReviewAssignmentResult{}, err
	}
	request.AcknowledgedFiles = acknowledged
	acknowledgedPaths := make(map[string]struct{}, len(acknowledged))
	for _, file := range acknowledged {
		acknowledgedPaths[file.Path] = struct{}{}
	}
	request.Observation.Model = strings.TrimSpace(request.Observation.Model)
	request.Observation.ModelAlias = strings.TrimSpace(request.Observation.ModelAlias)
	request.Observation.Account = strings.TrimSpace(request.Observation.Account)
	request.Observation.Reviewer = strings.TrimSpace(request.Observation.Reviewer)
	request.Observation.RawDigest = strings.TrimSpace(request.Observation.RawDigest)
	request.Observation.Summary = strings.TrimSpace(request.Observation.Summary)
	assignmentIndex, assignmentFound := repositoryReviewAssignmentIndex(
		assignmentCatalog, request.AssignmentID,
	)
	if !validFindingSourceProvenance(
		request.Observation.Model, request.Observation.ModelAlias, request.Observation.Account,
	) || request.Observation.ModelAlias == "" || request.Observation.Account == "" ||
		!assignmentFound ||
		request.Observation.Reviewer !=
			assignmentCatalog[assignmentIndex].FocusID ||
		!validRepositoryReviewCheckpointDigest(request.Observation.RawDigest) ||
		!validOptionalAutomationText(request.Observation.Summary, maxFindingTextBytes) ||
		len(request.Observation.Findings) > maxFindingsPerObservation {
		return CheckpointRepositoryReviewAssignmentResult{}, ErrInvalidPlan
	}
	for index, rawFinding := range request.Observation.Findings {
		finding := normalizeCandidate(rawFinding)
		if finding.Validation.Status != "confirmed" {
			return CheckpointRepositoryReviewAssignmentResult{}, fmt.Errorf(
				"%w: checkpoint finding %d is not confirmed", ErrInvalidPlan, index,
			)
		}
		if candidateErr := validateCandidate(finding); candidateErr != nil {
			return CheckpointRepositoryReviewAssignmentResult{}, fmt.Errorf(
				"%w: checkpoint finding %d is invalid: %v", ErrInvalidPlan, index, candidateErr,
			)
		}
		if _, acknowledged := acknowledgedPaths[finding.File]; !acknowledged {
			return CheckpointRepositoryReviewAssignmentResult{}, fmt.Errorf(
				"%w: checkpoint finding %d has no exact acknowledgement", ErrInvalidPlan, index,
			)
		}
		request.Observation.Findings[index] = finding
	}
	checkpointDigest := repositoryReviewCheckpointRequestDigest(request)
	if existingDigest != "" {
		if existingDigest != checkpointDigest {
			return CheckpointRepositoryReviewAssignmentResult{}, ErrConflict
		}
		return CheckpointRepositoryReviewAssignmentResult{
			State: state,
			AcceptedFindingIDs: repositoryReviewCheckpointRawFindingIDs(
				state.RawFindings, request.Plan.CampaignID, request.RunID, request.AssignmentID,
			),
			Idempotent: true,
		}, nil
	}
	completedAt := request.CompletedAt.UTC()
	if completedAt.IsZero() {
		completedAt = s.clock()
	}
	acceptedIDs, persistErr := persistRepositoryReviewCheckpointObservation(
		&state,
		request.Plan,
		request.RunID,
		request.AssignmentID,
		request.Observation,
		acknowledged,
		completedAt,
	)
	if persistErr != nil {
		return CheckpointRepositoryReviewAssignmentResult{}, persistErr
	}
	for _, file := range acknowledged {
		current := state.CurrentCampaign.Paths[file.Path]
		next, _, setErr := setRepositoryReviewAssignmentComplete(
			current, state.CurrentCampaign.AssignmentCatalog, request.AssignmentID,
		)
		if setErr != nil {
			return CheckpointRepositoryReviewAssignmentResult{}, setErr
		}
		state.CurrentCampaign.Paths[file.Path] = next
		if next.Completed {
			state.Files[file.Path] = ReviewedFile{
				FileRef: file, CommitSHA: request.Plan.CommitSHA,
				ProfileHash:     request.Plan.ProfileHash,
				ForceCampaignID: request.Plan.ForceCampaignID,
				RunID:           request.RunID, ReviewedAt: completedAt,
			}
			delete(state.Unsupported, file.Path)
			delete(state.ReviewAttempts, file.Path)
			delete(state.ReviewAttemptIdentities, file.Path)
		}
	}
	reservation.CheckpointDigest = checkpointDigest
	reservation.AcknowledgedFiles = acknowledged
	active.Reservations[request.AssignmentID] = reservation
	for _, findingID := range acceptedIDs {
		active.FindingIDs = appendUnique(active.FindingIDs, findingID)
	}
	state.LastCommitSHA = request.Plan.CommitSHA
	state.Version++
	state.ReviewVersion++
	state.UpdatedAt = completedAt
	if err := s.save(&state); err != nil {
		return CheckpointRepositoryReviewAssignmentResult{}, err
	}
	return CheckpointRepositoryReviewAssignmentResult{
		State: state, AcceptedFindingIDs: acceptedIDs,
	}, nil
}

func repositoryReviewCheckpointRawFindingIDs(
	findings []RawReviewFinding,
	campaignID string,
	runID string,
	assignmentID string,
) []string {
	selected := make([]RawReviewFinding, 0)
	for _, finding := range findings {
		if finding.CampaignID == campaignID && finding.RunID == runID &&
			finding.AssignmentID == assignmentID {
			selected = append(selected, finding)
		}
	}
	sort.Slice(selected, func(left, right int) bool {
		if selected[left].InsertionOrdinal != selected[right].InsertionOrdinal {
			return selected[left].InsertionOrdinal < selected[right].InsertionOrdinal
		}
		return selected[left].ID < selected[right].ID
	})
	ids := make([]string, 0, len(selected))
	for _, finding := range selected {
		ids = append(ids, finding.ID)
	}
	return ids
}

func persistRepositoryReviewCheckpointObservation(
	state *RepositoryState,
	plan Plan,
	runID string,
	assignmentID string,
	observation Observation,
	acknowledged []FileRef,
	completedAt time.Time,
) ([]string, error) {
	if state == nil {
		return nil, ErrInvalidPlan
	}
	contextRecord := FindingContext{
		CampaignID: plan.CampaignID,
		Repository: plan.Repository, CommitSHA: plan.CommitSHA,
		InventoryHash: plan.InventoryHash, ProfileHash: plan.ProfileHash,
		RunID: runID, Model: observation.Model, ModelAlias: observation.ModelAlias,
		Account: observation.Account, Reviewer: observation.Reviewer,
		Files:     append([]FileRef(nil), observation.ScopeFiles...),
		RawDigest: observation.RawDigest, CreatedAt: completedAt,
	}
	contextRecord.ID = stableID("rctx_", contextBindingDigest(contextRecord))
	initialFindingCount := len(state.Findings)
	acceptedIDs := make([]string, 0, len(observation.Findings))
	contextUsed := false
	for candidateIndex, rawCandidate := range observation.Findings {
		candidate := normalizeCandidate(rawCandidate)
		if candidate.Validation.Status != "confirmed" {
			return nil, errors.New("checkpoint finding is not confirmed")
		}
		primary, inScope := fileInScope(candidate.File, acknowledged)
		if !inScope {
			return nil, fmt.Errorf("checkpoint finding references an unacknowledged file")
		}
		if err := validateCandidate(candidate); err != nil {
			return nil, fmt.Errorf("checkpoint finding is invalid: %w", err)
		}
		admissionBucket, bucketErr := DeduplicationAdmissionBucket(
			plan.CampaignID, primary, candidate.Symbol,
		)
		if bucketErr != nil {
			return nil, fmt.Errorf("checkpoint finding has an invalid admission bucket: %w", bucketErr)
		}
		fingerprint := findingFingerprint(primary, candidate)
		rawID := stableID(
			"rrw_", plan.Repository, plan.CampaignID, plan.CommitSHA, runID,
			assignmentID, fmt.Sprint(candidateIndex), fingerprint,
		)
		if err := persistRawRepositoryReviewCheckpointFinding(
			state, rawID, admissionBucket, plan, runID, assignmentID,
			contextRecord.ID, observation, primary, candidate, completedAt,
		); err != nil {
			return nil, err
		}
		acceptedIDs = append(acceptedIDs, rawID)
		candidateObservation := findingObservationFrom(
			candidate, contextRecord.ID, observation.Model,
			observation.ModelAlias, observation.Account, observation.Reviewer,
		)
		contributorModel := observation.ModelAlias
		if contributorModel == "" {
			contributorModel = observation.Model
		}
		index := findingIndexByFingerprint(state.Findings[initialFindingCount:], fingerprint)
		if index >= 0 {
			index += initialFindingCount
		}
		if index < 0 {
			index = semanticFindingIndex(state.Findings[initialFindingCount:], primary, candidate)
			if index >= 0 {
				index += initialFindingCount
			}
		}
		if index < 0 {
			finding := Finding{
				ID: stableID(
					"rfn_", plan.Repository, plan.CommitSHA, runID, assignmentID, fingerprint,
				),
				CampaignID: plan.CampaignID, Fingerprint: fingerprint,
				Repository: plan.Repository, CommitSHA: plan.CommitSHA,
				File: primary, Line: candidate.Line, Severity: candidate.Severity,
				Title: candidate.Title, Symbol: candidate.Symbol,
				Message: candidate.Message, Evidence: candidate.Evidence,
				Impact: candidate.Impact, Validation: candidate.Validation,
				MatchHints: candidate.MatchHints, FixEffort: candidate.FixEffort,
				ContextIDs: []string{contextRecord.ID}, Models: []string{contributorModel},
				ObservationCount: 1, Status: FindingOpen,
				DeduplicationPending: true, RawFindingIDs: []string{rawID},
				Observations:            []FindingObservation{candidateObservation},
				TargetBranch:            plan.TargetBranch,
				AdvertisedDefaultBranch: plan.AdvertisedDefaultBranch,
				TargetIsDefault:         plan.TargetIsDefault,
				Version:                 1, CreatedAt: completedAt, UpdatedAt: completedAt,
			}
			state.Findings = append(state.Findings, finding)
			setRawReviewFindingLegacyProjection(state, rawID, finding.ID)
			contextUsed = true
			continue
		}
		finding := &state.Findings[index]
		finding.Severity = moreSevere(finding.Severity, candidate.Severity)
		finding.Observations, _ = upsertFindingObservation(
			finding.Observations, candidateObservation,
		)
		finding.ContextIDs = findingObservationContextIDs(finding.Observations)
		finding.Models = appendUnique(finding.Models, contributorModel)
		finding.ObservationCount = len(finding.Observations)
		finding.RawFindingIDs = appendUnique(finding.RawFindingIDs, rawID)
		finding.Version++
		finding.UpdatedAt = completedAt
		setRawReviewFindingLegacyProjection(state, rawID, finding.ID)
		contextUsed = true
	}
	if contextUsed {
		existing := -1
		for index := range state.Contexts {
			if state.Contexts[index].ID == contextRecord.ID {
				existing = index
				break
			}
		}
		if existing >= 0 {
			state.Contexts[existing] = contextRecord
		} else {
			state.Contexts = append(state.Contexts, contextRecord)
		}
	}
	pruneUnreferencedFindingContexts(state)
	reconcileFindingsProcessingCounters(state)
	state.FindingsProcessing.UpdatedAt = completedAt
	return acceptedIDs, nil
}

func setRawReviewFindingLegacyProjection(state *RepositoryState, rawID, findingID string) {
	if state == nil {
		return
	}
	if index := rawFindingIndexByID(state.RawFindings, rawID); index >= 0 {
		state.RawFindings[index].LegacyFindingID = findingID
		state.RawFindings[index].DiagnosisDigest = RawReviewFindingDiagnosisDigest(
			state.RawFindings[index],
		)
	}
}

func repositoryReviewCheckpointDeduplicationSnapshot(
	coverage *RepositoryReviewCampaignCoverage,
	reviewerModel string,
) RepositoryReviewDeduplicationSnapshot {
	if coverage != nil && coverage.DeduplicationSnapshot != nil {
		return *cloneRepositoryReviewDeduplicationSnapshot(coverage.DeduplicationSnapshot)
	}
	reviewerModel = strings.TrimSpace(reviewerModel)
	return RepositoryReviewDeduplicationSnapshot{
		ReviewerModel: reviewerModel, DeduplicationModel: reviewerModel,
		SimilarityThreshold: DeduplicationDefaultThreshold,
		CandidateLimit:      DeduplicationDefaultCandidateLimit,
	}
}

func persistRawRepositoryReviewCheckpointFinding(
	state *RepositoryState,
	rawID string,
	admissionBucket string,
	plan Plan,
	runID string,
	assignmentID string,
	contextID string,
	observation Observation,
	primary FileRef,
	candidate FindingCandidate,
	completedAt time.Time,
) error {
	if !validFindingSourceProvenance(
		observation.Model, observation.ModelAlias, observation.Account,
	) || observation.ModelAlias == "" || observation.Account == "" {
		return ErrInvalidPlan
	}
	for _, existing := range state.RawFindings {
		if existing.ID != rawID {
			continue
		}
		if existing.CampaignID != plan.CampaignID ||
			existing.AdmissionBucket != admissionBucket || existing.Repository != plan.Repository ||
			existing.CommitSHA != plan.CommitSHA || existing.File != primary ||
			existing.ContextID != contextID || existing.RunID != runID ||
			existing.AssignmentID != assignmentID || existing.Model != observation.Model ||
			existing.ModelAlias != observation.ModelAlias || existing.Account != observation.Account ||
			existing.Reviewer != observation.Reviewer ||
			!reflect.DeepEqual(rawFindingCandidate(existing), candidate) {
			return ErrConflict
		}
		for _, job := range state.DeduplicationJobs {
			if job.RawFindingID == rawID {
				return nil
			}
		}
		return errors.New("raw checkpoint finding is missing its deduplication job")
	}
	ordinal := state.NextDeduplicationOrdinal
	if ordinal == 0 {
		ordinal = 1
	}
	state.NextDeduplicationOrdinal = ordinal + 1
	reviewerModel := observation.ModelAlias
	if reviewerModel == "" {
		reviewerModel = observation.Model
	}
	snapshot := repositoryReviewCheckpointDeduplicationSnapshot(
		state.CurrentCampaign, reviewerModel,
	)
	raw := RawReviewFinding{
		ID: rawID, Version: 1, CampaignID: plan.CampaignID,
		AdmissionBucket: admissionBucket, InsertionOrdinal: ordinal,
		Repository: plan.Repository, CommitSHA: plan.CommitSHA, File: primary,
		Line: candidate.Line, Severity: candidate.Severity, Title: candidate.Title,
		Symbol: candidate.Symbol, Message: candidate.Message, Evidence: candidate.Evidence,
		Impact: candidate.Impact, Validation: candidate.Validation,
		MatchHints: candidate.MatchHints, FixEffort: candidate.FixEffort,
		ContextID: contextID, RunID: runID, AssignmentID: assignmentID,
		Model: observation.Model, ModelAlias: observation.ModelAlias, Account: observation.Account,
		Reviewer: observation.Reviewer,
		State:    RawFindingDeduplicationPending, Disposition: RawFindingDispositionUndecided,
		CreatedAt: completedAt, UpdatedAt: completedAt,
	}
	raw.DiagnosisDigest = RawReviewFindingDiagnosisDigest(raw)
	raw.History = []RawFindingHistoryEntry{{
		State: raw.State, Disposition: raw.Disposition, At: completedAt,
	}}
	state.RawFindings = append(state.RawFindings, raw)
	state.DeduplicationJobs = append(state.DeduplicationJobs, DeduplicationJob{
		ID: stableID("rdj_", rawID), RawFindingID: rawID,
		State: DeduplicationJobPending, AdmissionBucket: admissionBucket,
		InsertionOrdinal: ordinal, ModelSnapshot: snapshot,
		History:   []DeduplicationJobHistoryEntry{{State: DeduplicationJobPending, At: completedAt}},
		CreatedAt: completedAt, UpdatedAt: completedAt,
	})
	return nil
}

func (s Store) FinalizeRepositoryReviewRun(
	ctx context.Context,
	request FinalizeRepositoryReviewRunRequest,
) (RecordResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return RecordResult{}, contextErr
	}
	request.RunID = strings.TrimSpace(request.RunID)
	if !validBoundedText(request.RunID, 1024) || request.Plan.ID == "" ||
		request.Plan.ID != planDigest(request.Plan) || request.ExcludedFiles < 0 ||
		request.ExcludedFiles > maxReviewFiles {
		return RecordResult{}, ErrInvalidPlan
	}
	unlock, err := s.lock(request.Plan.Repository)
	if err != nil {
		return RecordResult{}, err
	}
	defer unlock()
	state, err := s.load(request.Plan.Repository)
	if err != nil {
		return RecordResult{}, err
	}
	for _, run := range state.Runs {
		if run.ID == request.RunID {
			if run.PlanID != request.Plan.ID || run.CampaignID != request.Plan.CampaignID {
				return RecordResult{}, ErrConflict
			}
			if run.Interrupted {
				return RecordResult{}, ErrConflict
			}
			return RecordResult{
				State: state, Run: run,
				AcceptedFindingIDs: append([]string(nil), run.FindingIDs...),
			}, nil
		}
	}
	active := state.ActiveReviewRun
	if active == nil || active.ID != request.RunID || active.PlanID != request.Plan.ID ||
		state.CurrentCampaign == nil || state.CurrentCampaign.ID != request.Plan.CampaignID {
		return RecordResult{}, ErrConflict
	}
	completedAt := request.CompletedAt.UTC()
	if completedAt.IsZero() {
		completedAt = s.clock()
	}
	pending := make(map[string]FileRef, len(request.Plan.PendingFiles))
	for _, file := range request.Plan.PendingFiles {
		pending[file.Path] = file
	}
	unsupported := make(map[string]UnsupportedFile, len(request.UnsupportedFiles))
	for _, item := range request.UnsupportedFiles {
		bound, found := pending[item.Path]
		item.Reason = strings.TrimSpace(item.Reason)
		if !found || bound != item.FileRef || !validBoundedText(item.Reason, 256) {
			return RecordResult{}, ErrInvalidPlan
		}
		pathCoverage := state.CurrentCampaign.Paths[item.Path]
		if pathCoverage.AssignmentBits != "" || pathCoverage.Inspected || pathCoverage.Completed {
			return RecordResult{}, ErrConflict
		}
		item.FileRef = bound
		item.CommitSHA = request.Plan.CommitSHA
		item.ProfileHash = request.Plan.ProfileHash
		item.ForceCampaignID = request.Plan.ForceCampaignID
		item.UpdatedAt = completedAt
		unsupported[item.Path] = item
	}
	for pathValue, item := range unsupported {
		state.Unsupported[pathValue] = item
		delete(state.Files, pathValue)
		delete(state.ReviewAttempts, pathValue)
		delete(state.ReviewAttemptIdentities, pathValue)
		if _, mergeErr := mergeRepositoryReviewCampaignPath(
			state.CurrentCampaign, pathValue,
			RepositoryReviewCampaignPathCoverage{Unsupported: true},
		); mergeErr != nil {
			return RecordResult{}, mergeErr
		}
	}
	for _, item := range request.Plan.UnsupportedFiles {
		if _, mergeErr := mergeRepositoryReviewCampaignPath(
			state.CurrentCampaign, item.Path,
			RepositoryReviewCampaignPathCoverage{Unsupported: true},
		); mergeErr != nil {
			return RecordResult{}, mergeErr
		}
	}
	for _, file := range request.Plan.UnchangedFiles {
		if _, mergeErr := mergeRepositoryReviewCampaignPath(
			state.CurrentCampaign, file.Path,
			RepositoryReviewCampaignPathCoverage{Completed: true},
		); mergeErr != nil {
			return RecordResult{}, mergeErr
		}
	}
	inspectedPaths := make(map[string]struct{})
	checkpointDigests := make(map[string]string)
	checkpointScopes := make(map[string][]FileRef)
	models := make([]string, 0)
	for _, reservation := range active.Reservations {
		if reservation.CheckpointDigest != "" {
			checkpointDigests[reservation.AssignmentID] = reservation.CheckpointDigest
			checkpointScopes[reservation.AssignmentID] = append(
				[]FileRef(nil), reservation.Files...,
			)
		}
		for _, file := range reservation.AcknowledgedFiles {
			inspectedPaths[file.Path] = struct{}{}
		}
	}
	for _, contextRecord := range state.Contexts {
		if contextRecord.RunID == request.RunID && contextRecord.CampaignID == request.Plan.CampaignID {
			model := contextRecord.ModelAlias
			if model == "" {
				model = contextRecord.Model
			}
			models = appendUnique(models, model)
		}
	}
	completedFiles := 0
	unreviewedPaths := make([]string, 0)
	for _, file := range request.Plan.PendingFiles {
		if _, terminal := unsupported[file.Path]; terminal {
			continue
		}
		coverage := state.CurrentCampaign.Paths[file.Path]
		if coverage.Completed {
			completedFiles++
			continue
		}
		identity := reviewAttemptIdentity(file, request.Plan.ProfileHash)
		if state.ReviewAttemptIdentities[file.Path] != identity {
			state.ReviewAttempts[file.Path] = 0
		}
		state.ReviewAttemptIdentities[file.Path] = identity
		state.ReviewAttempts[file.Path]++
		unreviewedPaths = append(unreviewedPaths, file.Path)
	}
	sort.Strings(unreviewedPaths)
	unsupportedPaths := make([]string, 0, len(unsupported))
	for pathValue := range unsupported {
		unsupportedPaths = append(unsupportedPaths, pathValue)
	}
	sort.Strings(unsupportedPaths)
	run := ReviewRun{
		ID: request.RunID, CampaignID: request.Plan.CampaignID,
		PlanID: request.Plan.ID, CommitSHA: request.Plan.CommitSHA,
		InventoryHash: request.Plan.InventoryHash, ProfileHash: request.Plan.ProfileHash,
		ScopeDigest:    state.CurrentCampaign.ScopeDigest,
		InspectedFiles: len(inspectedPaths), ReviewedFiles: completedFiles,
		UnreviewedFiles:  len(request.Plan.PendingFiles) - completedFiles - len(unsupported),
		UnsupportedCount: len(unsupported),
		RemainingFiles: len(request.Plan.DeferredFiles) +
			len(request.Plan.PendingFiles) - completedFiles - len(unsupported),
		UnreviewedPaths: unreviewedPaths, UnsupportedPaths: unsupportedPaths,
		SkippedFiles: len(request.Plan.UnchangedFiles), ExcludedFiles: request.ExcludedFiles,
		AcceptedFindings:  len(active.FindingIDs),
		FindingIDs:        append([]string(nil), active.FindingIDs...),
		CheckpointDigests: checkpointDigests, CheckpointScopes: checkpointScopes, Models: models,
		CompletedAt:             completedAt,
		TargetBranch:            request.Plan.TargetBranch,
		AdvertisedDefaultBranch: request.Plan.AdvertisedDefaultBranch,
		TargetIsDefault:         request.Plan.TargetIsDefault,
	}
	state.Runs = append(state.Runs, run)
	if len(state.Runs) > 1000 {
		state.Runs = append([]ReviewRun(nil), state.Runs[len(state.Runs)-1000:]...)
	}
	state.ActiveReviewRun = nil
	pruneCheckpointMetadata(&state, request.Plan, request.Plan.PendingFiles)
	state.LastCommitSHA = request.Plan.CommitSHA
	state.LastExcludedFiles = request.ExcludedFiles
	state.Version++
	state.ReviewVersion++
	state.UpdatedAt = completedAt
	if err := s.save(&state); err != nil {
		return RecordResult{}, err
	}
	return RecordResult{
		State: state, Run: run,
		AcceptedFindingIDs: append([]string(nil), active.FindingIDs...),
	}, nil
}

// InterruptRepositoryReviewRun releases the unfinished reservations of an
// abandoned run. Assignment bits, contexts, findings, and mapping jobs from
// checkpoints that committed before interruption are intentionally untouched.
func (s Store) InterruptRepositoryReviewRun(
	ctx context.Context,
	repository string,
	runID string,
) (RepositoryState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return RepositoryState{}, contextErr
	}
	repository = strings.TrimSpace(repository)
	runID = strings.TrimSpace(runID)
	if !validBoundedText(repository, maxRepositoryIdentityBytes) ||
		!validBoundedText(runID, 1024) {
		return RepositoryState{}, ErrInvalidPlan
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, err
	}
	if state.ActiveReviewRun == nil {
		return state, nil
	}
	if state.ActiveReviewRun.ID != runID {
		return RepositoryState{}, ErrConflict
	}
	archiveInterruptedRepositoryReviewRun(&state, s.clock())
	state.Version++
	state.ReviewVersion++
	state.UpdatedAt = s.clock()
	if err := s.save(&state); err != nil {
		return RepositoryState{}, err
	}
	return state, nil
}

// InterruptAbandonedRepositoryReviewRun releases whichever active assignment
// run is present and returns its ID. It is intended for launcher startup, where
// process ownership has already been proven lost.
func (s Store) InterruptAbandonedRepositoryReviewRun(
	ctx context.Context,
	repository string,
) (RepositoryState, string, error) {
	repository = strings.TrimSpace(repository)
	if !validBoundedText(repository, maxRepositoryIdentityBytes) {
		return RepositoryState{}, "", ErrInvalidPlan
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, "", err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, "", err
	}
	if state.ActiveReviewRun == nil {
		return state, "", nil
	}
	runID := state.ActiveReviewRun.ID
	archiveInterruptedRepositoryReviewRun(&state, s.clock())
	state.Version++
	state.ReviewVersion++
	state.UpdatedAt = s.clock()
	if err := s.save(&state); err != nil {
		return RepositoryState{}, "", err
	}
	return state, runID, nil
}

func archiveInterruptedRepositoryReviewRun(state *RepositoryState, completedAt time.Time) {
	if state == nil || state.ActiveReviewRun == nil {
		return
	}
	active := state.ActiveReviewRun
	checkpointDigests := make(map[string]string)
	checkpointScopes := make(map[string][]FileRef)
	inspected := make(map[string]struct{})
	reserved := make(map[string]struct{})
	completed := make(map[string]struct{})
	models := make([]string, 0)
	for _, reservation := range active.Reservations {
		for _, file := range reservation.Files {
			reserved[file.Path] = struct{}{}
			if state.CurrentCampaign != nil && state.CurrentCampaign.Paths[file.Path].Completed {
				completed[file.Path] = struct{}{}
			}
		}
		if reservation.CheckpointDigest == "" {
			continue
		}
		checkpointDigests[reservation.AssignmentID] = reservation.CheckpointDigest
		checkpointScopes[reservation.AssignmentID] = append(
			[]FileRef(nil), reservation.Files...,
		)
		for _, file := range reservation.AcknowledgedFiles {
			inspected[file.Path] = struct{}{}
		}
	}
	for _, contextRecord := range state.Contexts {
		if contextRecord.RunID == active.ID && contextRecord.CampaignID == active.CampaignID {
			model := contextRecord.ModelAlias
			if model == "" {
				model = contextRecord.Model
			}
			models = appendUnique(models, model)
		}
	}
	unreviewedPaths := make([]string, 0, len(reserved))
	for pathValue := range reserved {
		if _, done := completed[pathValue]; !done {
			unreviewedPaths = append(unreviewedPaths, pathValue)
		}
	}
	sort.Strings(unreviewedPaths)
	scopeDigest := ""
	remainingFiles := len(unreviewedPaths)
	if state.CurrentCampaign != nil && state.CurrentCampaign.ID == active.CampaignID {
		scopeDigest = state.CurrentCampaign.ScopeDigest
		terminal := 0
		for _, coverage := range state.CurrentCampaign.Paths {
			if coverage.Completed || coverage.Unsupported {
				terminal++
			}
		}
		remainingFiles = max(0, state.CurrentCampaign.SelectedFiles-terminal)
	}
	state.Runs = append(state.Runs, ReviewRun{
		ID: active.ID, CampaignID: active.CampaignID, PlanID: active.PlanID,
		CommitSHA: active.CommitSHA, InventoryHash: active.InventoryHash,
		ProfileHash: active.ProfileHash, ScopeDigest: scopeDigest,
		InspectedFiles: len(inspected), ReviewedFiles: len(completed),
		UnreviewedFiles: len(unreviewedPaths), RemainingFiles: remainingFiles,
		UnreviewedPaths:   unreviewedPaths,
		AcceptedFindings:  len(active.FindingIDs),
		FindingIDs:        append([]string(nil), active.FindingIDs...),
		Models:            models,
		CheckpointDigests: checkpointDigests, CheckpointScopes: checkpointScopes,
		Interrupted: true, CompletedAt: completedAt.UTC(),
	})
	if len(state.Runs) > 1000 {
		state.Runs = append([]ReviewRun(nil), state.Runs[len(state.Runs)-1000:]...)
	}
	state.ActiveReviewRun = nil
}

func repositoryReviewCheckpointRequestDigest(
	request CheckpointRepositoryReviewAssignmentRequest,
) string {
	data, _ := json.Marshal(struct {
		PlanID            string      `json:"plan_id"`
		CampaignID        string      `json:"campaign_id"`
		RunID             string      `json:"run_id"`
		AssignmentID      string      `json:"assignment_id"`
		ProviderDigest    string      `json:"provider_digest"`
		AcknowledgedFiles []FileRef   `json:"acknowledged_files"`
		Observation       Observation `json:"observation"`
	}{
		PlanID: request.Plan.ID, CampaignID: request.Plan.CampaignID,
		RunID: request.RunID, AssignmentID: request.AssignmentID,
		ProviderDigest:    request.Digest,
		AcknowledgedFiles: request.AcknowledgedFiles,
		Observation:       request.Observation,
	})
	return stableID("sha256:", string(data))
}

func validRepositoryReviewCheckpointDigest(value string) bool {
	digest, ok := strings.CutPrefix(value, "sha256:")
	return ok && len(digest) == 64 && validHexDigest(digest)
}

func validateRepositoryReviewActiveRun(
	state RepositoryState,
) error {
	active := state.ActiveReviewRun
	if active == nil {
		return nil
	}
	for _, run := range state.Runs {
		if run.ID == active.ID {
			return errors.New("active repository review run duplicates retained history")
		}
	}
	if !validBoundedText(active.ID, 1024) || !ValidRepositoryReviewCampaignID(active.CampaignID) ||
		!validBoundedText(active.PlanID, 128) || !validRepositoryReviewCommitSHA(active.CommitSHA) ||
		!validBoundedText(active.InventoryHash, 256) || !validBoundedText(active.ProfileHash, 256) ||
		active.StartedAt.IsZero() || active.Reservations == nil ||
		len(active.Reservations) > maxRepositoryReviewRequiredAssignments ||
		state.CurrentCampaign == nil || state.CurrentCampaign.ID != active.CampaignID ||
		state.CurrentCampaign.CommitSHA != active.CommitSHA ||
		state.CurrentCampaign.InventoryHash != active.InventoryHash ||
		state.CurrentCampaign.ProfileHash != active.ProfileHash {
		return errors.New("invalid active repository review run")
	}
	for assignmentID, reservation := range active.Reservations {
		if assignmentID != reservation.AssignmentID {
			return errors.New("invalid repository review assignment reservation")
		}
		if _, found := repositoryReviewAssignmentIndex(
			state.CurrentCampaign.AssignmentCatalog, assignmentID,
		); !found {
			return errors.New("unknown repository review assignment reservation")
		}
		files, err := canonicalRepositoryReviewCampaignFiles(reservation.Files)
		if err != nil || len(files) == 0 || !reflect.DeepEqual(files, reservation.Files) {
			return errors.New("invalid repository review assignment reservation files")
		}
		allowed := make(map[string]FileRef, len(files))
		for _, file := range files {
			allowed[file.Path] = file
		}
		var acknowledged []FileRef
		if len(reservation.AcknowledgedFiles) > 0 {
			acknowledged, err = bindRepositoryReviewCampaignFiles(
				reservation.AcknowledgedFiles, allowed,
			)
		}
		if err != nil || len(reservation.AcknowledgedFiles) > 0 &&
			!reflect.DeepEqual(acknowledged, reservation.AcknowledgedFiles) ||
			reservation.CheckpointDigest == "" && len(reservation.AcknowledgedFiles) > 0 {
			return errors.New("invalid repository review assignment checkpoint")
		}
		if reservation.CheckpointDigest != "" &&
			!validRepositoryReviewCheckpointDigest(reservation.CheckpointDigest) {
			return errors.New("invalid repository review assignment checkpoint digest")
		}
	}
	for _, findingID := range active.FindingIDs {
		index := rawFindingIndexByID(state.RawFindings, findingID)
		if index < 0 || state.RawFindings[index].CampaignID != active.CampaignID {
			return errors.New("invalid active repository review finding")
		}
	}
	return nil
}

func rawFindingIndexByID(findings []RawReviewFinding, id string) int {
	for index := range findings {
		if findings[index].ID == id {
			return index
		}
	}
	return -1
}
