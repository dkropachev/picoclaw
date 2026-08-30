package repoaudit

import (
	"fmt"
	"strings"
	"time"
)

// persistLegacyRecordFinding preserves the Raw -> Deduplicated -> Repository
// gate for the pre-assignment Record API. That API has no trusted account/model
// snapshot, so its frozen compatibility policy is candidate_limit=0: every
// validated raw finding is deterministically promoted as a new deduplicated
// occurrence without a model call.
func persistLegacyRecordFinding(
	state *RepositoryState,
	plan Plan,
	runID string,
	observationIndex, findingIndex int,
	contextRecord FindingContext,
	observation Observation,
	primary FileRef,
	candidate FindingCandidate,
	completedAt time.Time,
) (string, error) {
	boundaryID := strings.TrimSpace(plan.CampaignID)
	if boundaryID == "" {
		boundaryID = stableID("rrc_", plan.Repository, runID)
	}
	bucket, err := DeduplicationAdmissionBucket(boundaryID, primary, candidate.Symbol)
	if err != nil {
		return "", err
	}
	ordinal := state.NextDeduplicationOrdinal
	if ordinal == 0 {
		ordinal = 1
	}
	state.NextDeduplicationOrdinal = ordinal + 1
	fingerprint := findingFingerprint(primary, candidate)
	rawID := stableID(
		"rrl_", plan.Repository, boundaryID, plan.CommitSHA, runID,
		fmt.Sprint(observationIndex), fmt.Sprint(findingIndex), fingerprint,
	)
	if rawFindingIndexByID(state.RawFindings, rawID) >= 0 {
		return "", ErrConflict
	}
	snapshot := RepositoryReviewDeduplicationSnapshot{
		ReviewerModel: observation.Model, DeduplicationModel: observation.Model,
		SimilarityThreshold: DeduplicationDefaultThreshold, CandidateLimit: 0,
	}
	if state.CurrentCampaign != nil && state.CurrentCampaign.ID == plan.CampaignID &&
		state.CurrentCampaign.DeduplicationSnapshot != nil {
		snapshot = *cloneRepositoryReviewDeduplicationSnapshot(
			state.CurrentCampaign.DeduplicationSnapshot,
		)
		// This compatibility API cannot safely dispatch a model because its
		// request predates a trusted provider snapshot boundary.
		snapshot.CandidateLimit = 0
	}
	raw := RawReviewFinding{
		ID: rawID, Version: 1, CampaignID: boundaryID,
		AdmissionBucket: bucket, InsertionOrdinal: ordinal,
		Repository: plan.Repository, CommitSHA: plan.CommitSHA, File: primary,
		Line: candidate.Line, Severity: candidate.Severity, Title: candidate.Title,
		Symbol: candidate.Symbol, Message: candidate.Message, Evidence: candidate.Evidence,
		Impact: candidate.Impact, Validation: candidate.Validation,
		MatchHints: candidate.MatchHints, FixEffort: candidate.FixEffort,
		ContextID: contextRecord.ID, RunID: runID,
		AssignmentID: fmt.Sprintf("record-%03d-%03d", observationIndex, findingIndex),
		Model:        observation.Model, Reviewer: observation.Reviewer,
		State: RawFindingDeduplicationCompleted, Disposition: RawFindingDispositionNew,
		CreatedAt: completedAt, UpdatedAt: completedAt,
	}
	raw.DiagnosisDigest = RawReviewFindingDiagnosisDigest(raw)
	deduplicated := newDeduplicatedReviewFinding(raw, ordinal, state.Findings, completedAt)
	// Preserve the legacy Record API's stable occurrence identity so retained
	// workflow evidence remains recoverable, while the presence of the durable
	// DeduplicatedReviewFinding is now the mapping admission authority.
	legacyID := stableID(
		"rfn_", plan.Repository, plan.CommitSHA, runID, fingerprint,
	)
	if findingIndexByID(state.Findings, legacyID) < 0 {
		deduplicated.ID = legacyID
	}
	deduplicated.TargetBranch = plan.TargetBranch
	deduplicated.AdvertisedDefaultBranch = plan.AdvertisedDefaultBranch
	deduplicated.TargetIsDefault = plan.TargetIsDefault
	raw.DeduplicatedFindingID = deduplicated.ID
	raw.History = []RawFindingHistoryEntry{
		{State: RawFindingDeduplicationPending, Disposition: RawFindingDispositionUndecided, At: completedAt},
		{
			State: RawFindingDeduplicationCompleted, Disposition: RawFindingDispositionNew,
			DeduplicatedFindingID: deduplicated.ID, At: completedAt,
		},
	}
	state.RawFindings = append(state.RawFindings, raw)
	state.DeduplicatedFindings = append(state.DeduplicatedFindings, deduplicated)
	projection := deduplicatedFindingProjection(deduplicated, raw, state.Findings)
	projection.CampaignID = plan.CampaignID
	state.Findings = append(state.Findings, projection)
	state.DeduplicationJobs = append(state.DeduplicationJobs, DeduplicationJob{
		ID: stableID("rdj_", raw.ID), RawFindingID: raw.ID,
		State: DeduplicationJobCompleted, AdmissionBucket: bucket,
		InsertionOrdinal: ordinal, ModelSnapshot: snapshot,
		Decision: DeduplicationJudgment{Decision: "new"},
		History: []DeduplicationJobHistoryEntry{
			{State: DeduplicationJobPending, At: completedAt},
			{State: DeduplicationJobCompleted, At: completedAt},
		},
		CreatedAt: completedAt, UpdatedAt: completedAt,
	})
	return deduplicated.ID, nil
}
