package repoaudit

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"
)

const DeduplicationHistoryLimit = 32

// RepositoryReviewDeduplicationSnapshot freezes the policy and provider
// identity used for every raw finding admitted to one campaign.  The
// configured deduplication model may equal the reviewer model after the
// profile's blank-model fallback has been resolved by the controller.
type RepositoryReviewDeduplicationSnapshot struct {
	ProfileID            string `json:"profile_id,omitempty"`
	ProfileVersion       int64  `json:"profile_version,omitempty"`
	ReviewerModel        string `json:"reviewer_model,omitempty"`
	DeduplicationModel   string `json:"deduplication_model,omitempty"`
	AccountRef           string `json:"account_ref,omitempty"`
	AccountModelRevision string `json:"account_model_revision,omitempty"`
	SimilarityThreshold  int    `json:"similarity_threshold"`
	CandidateLimit       int    `json:"candidate_limit"`
}

type RawFindingDeduplicationState string

const (
	RawFindingDeduplicationPending   RawFindingDeduplicationState = "pending"
	RawFindingDeduplicationRunning   RawFindingDeduplicationState = "running"
	RawFindingDeduplicationCompleted RawFindingDeduplicationState = "completed"
	RawFindingDeduplicationFailed    RawFindingDeduplicationState = "failed"
)

type RawFindingDisposition string

const (
	RawFindingDispositionUndecided RawFindingDisposition = "undecided"
	RawFindingDispositionNew       RawFindingDisposition = "new"
	RawFindingDispositionDuplicate RawFindingDisposition = "duplicate"
)

// DeduplicationFailure is deliberately safe for API responses. Provider
// payloads, prompts, credentials, and source content must never be stored in
// it.
type DeduplicationFailure struct {
	Code      string    `json:"code"`
	Message   string    `json:"message"`
	Retryable bool      `json:"retryable"`
	At        time.Time `json:"at"`
}

type RawFindingHistoryEntry struct {
	State                 RawFindingDeduplicationState `json:"state"`
	Disposition           RawFindingDisposition        `json:"disposition"`
	DeduplicatedFindingID string                       `json:"deduplicated_finding_id,omitempty"`
	Attempt               int                          `json:"attempt,omitempty"`
	Failure               *DeduplicationFailure        `json:"failure,omitempty"`
	At                    time.Time                    `json:"at"`
}

// RawReviewFinding preserves the first validated diagnosis exactly as the
// finding agent returned it. Processing fields may advance, but diagnosis and
// provenance fields are immutable after insertion.
type RawReviewFinding struct {
	ID                    string                       `json:"id"`
	Version               int64                        `json:"version"`
	CampaignID            string                       `json:"campaign_id"`
	AdmissionBucket       string                       `json:"admission_bucket"`
	InsertionOrdinal      uint64                       `json:"insertion_ordinal"`
	DiagnosisDigest       string                       `json:"diagnosis_digest"`
	LegacyFindingID       string                       `json:"legacy_finding_id,omitempty"`
	Repository            string                       `json:"repository"`
	CommitSHA             string                       `json:"commit_sha"`
	File                  FileRef                      `json:"file"`
	Line                  *int                         `json:"line,omitempty"`
	Severity              string                       `json:"severity"`
	Title                 string                       `json:"title"`
	Symbol                string                       `json:"symbol,omitempty"`
	Message               string                       `json:"message,omitempty"`
	Evidence              string                       `json:"evidence"`
	Impact                string                       `json:"impact"`
	Validation            Validation                   `json:"validation"`
	MatchHints            MatchHints                   `json:"match_hints,omitempty"`
	FixEffort             FixEffort                    `json:"fix_effort,omitempty"`
	ContextID             string                       `json:"context_id"`
	RunID                 string                       `json:"run_id"`
	AssignmentID          string                       `json:"assignment_id"`
	Model                 string                       `json:"model"`
	ModelAlias            string                       `json:"model_alias,omitempty"`
	Account               string                       `json:"account,omitempty"`
	Reviewer              string                       `json:"reviewer,omitempty"`
	State                 RawFindingDeduplicationState `json:"deduplication_state"`
	Disposition           RawFindingDisposition        `json:"disposition"`
	DeduplicatedFindingID string                       `json:"deduplicated_finding_id,omitempty"`
	History               []RawFindingHistoryEntry     `json:"history,omitempty"`
	Failure               *DeduplicationFailure        `json:"failure,omitempty"`
	CreatedAt             time.Time                    `json:"created_at"`
	UpdatedAt             time.Time                    `json:"updated_at"`
}

type DeduplicatedFindingHistoryEntry struct {
	Action              string    `json:"action"`
	RawFindingID        string    `json:"raw_finding_id,omitempty"`
	RepositoryFindingID string    `json:"repository_finding_id,omitempty"`
	At                  time.Time `json:"at"`
}

// DeduplicatedReviewFinding is the campaign-level occurrence admitted to
// repository mapping. Its diagnosis is copied from the first raw source and
// is never consolidated or rewritten when later source IDs are attached.
type DeduplicatedReviewFinding struct {
	ID                      string                            `json:"id"`
	Version                 int64                             `json:"version"`
	CampaignID              string                            `json:"campaign_id"`
	AdmissionBucket         string                            `json:"admission_bucket"`
	CreationOrdinal         uint64                            `json:"creation_ordinal"`
	DiagnosisDigest         string                            `json:"diagnosis_digest"`
	Repository              string                            `json:"repository"`
	CommitSHA               string                            `json:"commit_sha"`
	File                    FileRef                           `json:"file"`
	Line                    *int                              `json:"line,omitempty"`
	Severity                string                            `json:"severity"`
	Title                   string                            `json:"title"`
	Symbol                  string                            `json:"symbol,omitempty"`
	Message                 string                            `json:"message,omitempty"`
	Evidence                string                            `json:"evidence"`
	Impact                  string                            `json:"impact"`
	Validation              Validation                        `json:"validation"`
	MatchHints              MatchHints                        `json:"match_hints,omitempty"`
	FixEffort               FixEffort                         `json:"fix_effort,omitempty"`
	RawSourceIDs            []string                          `json:"raw_source_ids"`
	History                 []DeduplicatedFindingHistoryEntry `json:"history,omitempty"`
	Status                  FindingStatus                     `json:"status"`
	IssueDraftID            string                            `json:"issue_draft_id,omitempty"`
	RepositoryFindingID     string                            `json:"repository_finding_id,omitempty"`
	RepositoryMatchState    RepositoryMatchState              `json:"repository_match_state,omitempty"`
	TargetBranch            string                            `json:"target_branch,omitempty"`
	AdvertisedDefaultBranch string                            `json:"advertised_default_branch,omitempty"`
	TargetIsDefault         bool                              `json:"target_is_default"`
	CreatedAt               time.Time                         `json:"created_at"`
	UpdatedAt               time.Time                         `json:"updated_at"`
}

type DeduplicationJobState string

const (
	DeduplicationJobPending   DeduplicationJobState = "pending"
	DeduplicationJobRunning   DeduplicationJobState = "running"
	DeduplicationJobCompleted DeduplicationJobState = "completed"
	DeduplicationJobFailed    DeduplicationJobState = "failed"
)

type DeduplicationCandidateVersion struct {
	CandidateID string `json:"candidate_id"`
	Version     int64  `json:"version"`
}

type DeduplicationJobHistoryEntry struct {
	State   DeduplicationJobState `json:"state"`
	Attempt int                   `json:"attempt,omitempty"`
	LeaseID string                `json:"lease_id,omitempty"`
	Failure *DeduplicationFailure `json:"failure,omitempty"`
	At      time.Time             `json:"at"`
}

type DeduplicationJob struct {
	ID                      string                                `json:"id"`
	RawFindingID            string                                `json:"raw_finding_id"`
	State                   DeduplicationJobState                 `json:"state"`
	AdmissionBucket         string                                `json:"admission_bucket"`
	InsertionOrdinal        uint64                                `json:"insertion_ordinal"`
	LeaseID                 string                                `json:"lease_id,omitempty"`
	LeaseExpiresAt          time.Time                             `json:"lease_expires_at,omitempty"`
	Attempts                int                                   `json:"attempts"`
	ModelSnapshot           RepositoryReviewDeduplicationSnapshot `json:"model_snapshot"`
	CandidateUniverseDigest string                                `json:"candidate_universe_digest,omitempty"`
	CandidateVersions       []DeduplicationCandidateVersion       `json:"candidate_versions,omitempty"`
	ShortlistedScores       []DeduplicationCandidateScore         `json:"shortlisted_scores,omitempty"`
	Decision                DeduplicationJudgment                 `json:"decision,omitempty"`
	History                 []DeduplicationJobHistoryEntry        `json:"history,omitempty"`
	Failure                 *DeduplicationFailure                 `json:"failure,omitempty"`
	CreatedAt               time.Time                             `json:"created_at"`
	UpdatedAt               time.Time                             `json:"updated_at"`
}

type FindingsProcessingCounters struct {
	RawTotal   int       `json:"raw_total"`
	Pending    int       `json:"pending"`
	Processing int       `json:"processing"`
	Failed     int       `json:"failed"`
	Completed  int       `json:"completed"`
	New        int       `json:"new"`
	Duplicates int       `json:"duplicates"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

// RepositoryReviewDeduplicationSnapshotFromAutomation resolves the profile's
// optional model fallback and the controller's effective account once, before
// campaign authorization. Subsequent profile or account changes cannot alter
// jobs already admitted to that campaign.
func RepositoryReviewDeduplicationSnapshotFromAutomation(
	automation RepositoryReviewAutomation,
) (RepositoryReviewDeduplicationSnapshot, error) {
	account := strings.TrimSpace(automation.EffectiveAccountRef)
	if account == "" {
		account = strings.TrimSpace(automation.AccountRef)
	}
	model := strings.TrimSpace(automation.DeduplicationModel)
	if model == "" && len(automation.ReviewerModels) > 0 {
		model = strings.TrimSpace(automation.ReviewerModels[0])
	}
	reviewer := ""
	if len(automation.ReviewerModels) > 0 {
		reviewer = strings.TrimSpace(automation.ReviewerModels[0])
	}
	snapshot := RepositoryReviewDeduplicationSnapshot{
		ProfileID: automation.ProfileID, ProfileVersion: automation.ProfileVersion,
		ReviewerModel: reviewer, DeduplicationModel: model,
		AccountRef: account, AccountModelRevision: automation.AccountModelRevision,
		SimilarityThreshold: automation.DeduplicationSimilarityThreshold,
		CandidateLimit:      automation.DeduplicationCandidateLimit,
	}
	if err := validateRepositoryReviewDeduplicationSnapshot(snapshot); err != nil {
		return RepositoryReviewDeduplicationSnapshot{}, err
	}
	return snapshot, nil
}

func cloneRepositoryReviewDeduplicationSnapshot(
	snapshot *RepositoryReviewDeduplicationSnapshot,
) *RepositoryReviewDeduplicationSnapshot {
	if snapshot == nil {
		return nil
	}
	cloned := *snapshot
	return &cloned
}

func validateRepositoryReviewDeduplicationSnapshot(
	snapshot RepositoryReviewDeduplicationSnapshot,
) error {
	snapshot.ProfileID = strings.TrimSpace(snapshot.ProfileID)
	snapshot.ReviewerModel = strings.TrimSpace(snapshot.ReviewerModel)
	snapshot.DeduplicationModel = strings.TrimSpace(snapshot.DeduplicationModel)
	snapshot.AccountRef = strings.TrimSpace(snapshot.AccountRef)
	snapshot.AccountModelRevision = strings.TrimSpace(snapshot.AccountModelRevision)
	if (snapshot.ProfileID == "") != (snapshot.ProfileVersion == 0) ||
		snapshot.ProfileID != "" && (!validProfileID(snapshot.ProfileID) || snapshot.ProfileVersion < 1) ||
		!validBoundedText(snapshot.ReviewerModel, 256) ||
		!validBoundedText(snapshot.DeduplicationModel, 256) ||
		!validOptionalAutomationText(snapshot.AccountRef, 256) ||
		!validOptionalAutomationText(snapshot.AccountModelRevision, 256) ||
		snapshot.SimilarityThreshold < 0 || snapshot.SimilarityThreshold > 100 ||
		snapshot.CandidateLimit < 0 || snapshot.CandidateLimit > DeduplicationMaximumShortlist {
		return errors.New("invalid repository review deduplication snapshot")
	}
	return nil
}

func reconcileFindingsProcessingCounters(state *RepositoryState) bool {
	if state == nil {
		return false
	}
	next := FindingsProcessingCounters{RawTotal: len(state.RawFindings)}
	var maximumOrdinal uint64
	for _, finding := range state.RawFindings {
		if finding.InsertionOrdinal > maximumOrdinal {
			maximumOrdinal = finding.InsertionOrdinal
		}
		switch finding.State {
		case RawFindingDeduplicationPending:
			next.Pending++
		case RawFindingDeduplicationRunning:
			next.Processing++
		case RawFindingDeduplicationFailed:
			next.Failed++
		case RawFindingDeduplicationCompleted:
			next.Completed++
		}
		switch finding.Disposition {
		case RawFindingDispositionNew:
			next.New++
		case RawFindingDispositionDuplicate:
			next.Duplicates++
		}
	}
	for _, finding := range state.DeduplicatedFindings {
		if finding.CreationOrdinal > maximumOrdinal {
			maximumOrdinal = finding.CreationOrdinal
		}
	}
	for _, job := range state.DeduplicationJobs {
		if job.InsertionOrdinal > maximumOrdinal {
			maximumOrdinal = job.InsertionOrdinal
		}
	}
	changed := false
	if len(state.RawFindings) > 0 {
		next.UpdatedAt = state.FindingsProcessing.UpdatedAt.UTC()
		if next.UpdatedAt.IsZero() {
			next.UpdatedAt = state.UpdatedAt.UTC()
		}
	}
	if !reflect.DeepEqual(state.FindingsProcessing, next) {
		state.FindingsProcessing = next
		changed = true
	}
	if maximumOrdinal > 0 && state.NextDeduplicationOrdinal <= maximumOrdinal {
		state.NextDeduplicationOrdinal = maximumOrdinal + 1
		changed = true
	}
	return changed
}

// synchronizeDeduplicatedFindingProjections mirrors only downstream mutable
// association fields from the compatibility Finding consumed by the mature
// repository-mapping and issue pipelines. The immutable diagnosis and ordered
// raw provenance remain owned by DeduplicatedReviewFinding.
func synchronizeDeduplicatedFindingProjections(state *RepositoryState) bool {
	if state == nil || len(state.DeduplicatedFindings) == 0 || len(state.Findings) == 0 {
		return false
	}
	byID := make(map[string]Finding, len(state.Findings))
	for _, finding := range state.Findings {
		byID[finding.ID] = finding
	}
	changed := false
	for index := range state.DeduplicatedFindings {
		deduplicated := &state.DeduplicatedFindings[index]
		projection, found := byID[deduplicated.ID]
		if !found {
			continue
		}
		if deduplicated.Status == projection.Status &&
			deduplicated.IssueDraftID == projection.IssueDraftID &&
			deduplicated.RepositoryFindingID == projection.RepositoryFindingID &&
			deduplicated.RepositoryMatchState == projection.RepositoryMatchState {
			continue
		}
		associationChanged := deduplicated.RepositoryFindingID != projection.RepositoryFindingID ||
			deduplicated.RepositoryMatchState != projection.RepositoryMatchState
		deduplicated.Status = projection.Status
		deduplicated.IssueDraftID = projection.IssueDraftID
		deduplicated.RepositoryFindingID = projection.RepositoryFindingID
		deduplicated.RepositoryMatchState = projection.RepositoryMatchState
		deduplicated.Version++
		if projection.UpdatedAt.After(deduplicated.UpdatedAt) {
			deduplicated.UpdatedAt = projection.UpdatedAt
		}
		if associationChanged {
			at := projection.UpdatedAt
			if at.IsZero() {
				at = state.UpdatedAt
			}
			deduplicated.History = appendDeduplicatedFindingHistory(
				deduplicated.History,
				DeduplicatedFindingHistoryEntry{
					Action: "repository_associated", RepositoryFindingID: projection.RepositoryFindingID,
					At: at,
				},
			)
		}
		changed = true
	}
	return changed
}

func validateDeduplicationState(state RepositoryState) error {
	rawByID := make(map[string]RawReviewFinding, len(state.RawFindings))
	maximumOrdinal := uint64(0)
	for _, raw := range state.RawFindings {
		validCommit := validRepositoryReviewCommitSHA(raw.CommitSHA)
		if strings.HasPrefix(raw.ID, "rrl_") {
			validCommit = validBoundedText(raw.CommitSHA, 256)
		}
		if _, duplicate := rawByID[raw.ID]; duplicate ||
			!validBoundedText(raw.ID, 256) || raw.Version < 1 ||
			!validBoundedText(raw.CampaignID, 256) ||
			!validBoundedText(raw.AdmissionBucket, 256) || raw.InsertionOrdinal == 0 ||
			raw.DiagnosisDigest != RawReviewFindingDiagnosisDigest(raw) ||
			!validBoundedText(raw.Repository, maxRepositoryIdentityBytes) || raw.Repository != state.Repository ||
			!validCommit ||
			!validRepositoryReviewPath(raw.File.Path) || !validBlobSHA(raw.File.BlobSHA) ||
			raw.File.SizeBytes < 0 ||
			!validBoundedText(raw.ContextID, 256) || !validBoundedText(raw.RunID, 1024) ||
			!validBoundedText(raw.AssignmentID, 128) ||
			!validFindingSourceProvenance(raw.Model, raw.ModelAlias, raw.Account) ||
			!validOptionalAutomationText(raw.Reviewer, 256) ||
			len(raw.History) > DeduplicationHistoryLimit || raw.CreatedAt.IsZero() ||
			raw.UpdatedAt.Before(raw.CreatedAt) || !validRawFindingState(raw) ||
			!validDeduplicationFailure(raw.Failure) {
			return errors.New("invalid raw repository review finding")
		}
		if candidate := rawFindingCandidate(raw); validateCandidate(candidate) != nil ||
			candidate.Validation.Status != "confirmed" || raw.File.Path != candidate.File {
			return errors.New("invalid raw repository review finding diagnosis")
		}
		for _, history := range raw.History {
			if history.At.IsZero() || history.Attempt < 0 ||
				history.Attempt > DeduplicationAttemptLimit ||
				!validRawFindingHistoryState(history.State, history.Disposition) ||
				!validDeduplicationFailure(history.Failure) {
				return errors.New("invalid raw repository review finding history")
			}
		}
		rawByID[raw.ID] = raw
		if raw.InsertionOrdinal > maximumOrdinal {
			maximumOrdinal = raw.InsertionOrdinal
		}
	}
	for _, projection := range state.Findings {
		if !projection.DeduplicationPending {
			continue
		}
		if len(projection.RawFindingIDs) == 0 || projection.RepositoryFindingID != "" ||
			projection.IssueDraftID != "" {
			return errors.New("invalid pending deduplication finding projection")
		}
		seen := make(map[string]struct{}, len(projection.RawFindingIDs))
		for _, rawID := range projection.RawFindingIDs {
			raw, found := rawByID[rawID]
			if !found || raw.CampaignID != projection.CampaignID || raw.Repository != projection.Repository {
				return errors.New("pending deduplication finding projection has an invalid raw source")
			}
			if _, duplicate := seen[rawID]; duplicate {
				return errors.New("pending deduplication finding projection repeats a raw source")
			}
			seen[rawID] = struct{}{}
		}
	}
	deduplicatedByID := make(map[string]DeduplicatedReviewFinding, len(state.DeduplicatedFindings))
	for _, finding := range state.DeduplicatedFindings {
		validCommit := validRepositoryReviewCommitSHA(finding.CommitSHA)
		if len(finding.RawSourceIDs) > 0 {
			if first, found := rawByID[finding.RawSourceIDs[0]]; found &&
				strings.HasPrefix(first.ID, "rrl_") {
				validCommit = validBoundedText(finding.CommitSHA, 256)
			}
		}
		if _, duplicate := deduplicatedByID[finding.ID]; duplicate ||
			!validBoundedText(finding.ID, 256) || finding.Version < 1 ||
			!validBoundedText(finding.CampaignID, 256) ||
			!validBoundedText(finding.AdmissionBucket, 256) || finding.CreationOrdinal == 0 ||
			!validBoundedText(finding.Repository, maxRepositoryIdentityBytes) ||
			!validCommit || len(finding.RawSourceIDs) == 0 ||
			len(finding.History) > DeduplicationHistoryLimit || finding.CreatedAt.IsZero() ||
			finding.UpdatedAt.Before(finding.CreatedAt) ||
			(finding.Status != FindingOpen && finding.Status != FindingDismissed &&
				finding.Status != FindingPosted) ||
			(finding.RepositoryFindingID == "") != (finding.RepositoryMatchState == "") ||
			(finding.RepositoryFindingID != "" &&
				repositoryFindingIndexByID(state.RepositoryFindings, finding.RepositoryFindingID) < 0) {
			return errors.New("invalid deduplicated repository review finding")
		}
		seenSources := make(map[string]struct{}, len(finding.RawSourceIDs))
		for _, rawID := range finding.RawSourceIDs {
			raw, found := rawByID[rawID]
			if !found || raw.CampaignID != finding.CampaignID ||
				raw.AdmissionBucket != finding.AdmissionBucket {
				return errors.New("invalid deduplicated repository review finding source")
			}
			if _, duplicate := seenSources[rawID]; duplicate {
				return errors.New("duplicate raw repository review finding source")
			}
			seenSources[rawID] = struct{}{}
		}
		first := rawByID[finding.RawSourceIDs[0]]
		if finding.DiagnosisDigest != first.DiagnosisDigest ||
			!deduplicatedFindingMatchesRaw(finding, first) {
			return errors.New("deduplicated repository review finding rewrites its first diagnosis")
		}
		for _, history := range finding.History {
			if !validBoundedText(strings.TrimSpace(history.Action), 128) || history.At.IsZero() ||
				!validOptionalAutomationText(history.RawFindingID, 256) ||
				!validOptionalAutomationText(history.RepositoryFindingID, 256) {
				return errors.New("invalid deduplicated repository review finding history")
			}
		}
		deduplicatedByID[finding.ID] = finding
		if finding.CreationOrdinal > maximumOrdinal {
			maximumOrdinal = finding.CreationOrdinal
		}
	}
	jobsByRaw := make(map[string]struct{}, len(state.DeduplicationJobs))
	jobIDs := make(map[string]struct{}, len(state.DeduplicationJobs))
	for _, job := range state.DeduplicationJobs {
		raw, found := rawByID[job.RawFindingID]
		_, duplicateJob := jobIDs[job.ID]
		_, duplicateRaw := jobsByRaw[job.RawFindingID]
		if duplicateJob || duplicateRaw || !found || !validBoundedText(job.ID, 256) ||
			job.AdmissionBucket != raw.AdmissionBucket || job.InsertionOrdinal < raw.InsertionOrdinal ||
			job.Attempts < 0 || job.Attempts > DeduplicationAttemptLimit ||
			len(job.History) > DeduplicationHistoryLimit || job.CreatedAt.IsZero() ||
			job.UpdatedAt.Before(job.CreatedAt) || !validDeduplicationJobState(job) ||
			validateRepositoryReviewDeduplicationSnapshot(job.ModelSnapshot) != nil ||
			!validDeduplicationFailure(job.Failure) {
			return errors.New("invalid repository review deduplication job")
		}
		candidateIDs := make(map[string]struct{}, len(job.CandidateVersions))
		for _, candidate := range job.CandidateVersions {
			if !validBoundedText(candidate.CandidateID, 256) || candidate.Version < 1 {
				return errors.New("invalid deduplication job candidate version")
			}
			if _, duplicate := candidateIDs[candidate.CandidateID]; duplicate {
				return errors.New("duplicate deduplication job candidate version")
			}
			candidateIDs[candidate.CandidateID] = struct{}{}
		}
		scoreIDs := make(map[string]struct{}, len(job.ShortlistedScores))
		if len(job.ShortlistedScores) > DeduplicationMaximumShortlist {
			return errors.New("deduplication job shortlist exceeds its limit")
		}
		for _, score := range job.ShortlistedScores {
			if !validBoundedText(score.CandidateID, 256) || score.Score < 0 || score.Score > 100 ||
				!validDeduplicationExplanation(score.Explanation) {
				return errors.New("invalid deduplication job shortlist score")
			}
			if _, duplicate := scoreIDs[score.CandidateID]; duplicate {
				return errors.New("duplicate deduplication job shortlist score")
			}
			scoreIDs[score.CandidateID] = struct{}{}
		}
		for _, history := range job.History {
			if !validDeduplicationJobHistoryState(history.State) || history.Attempt < 0 ||
				history.Attempt > DeduplicationAttemptLimit || history.At.IsZero() ||
				!validOptionalAutomationText(history.LeaseID, 256) ||
				!validDeduplicationFailure(history.Failure) {
				return errors.New("invalid repository review deduplication job history")
			}
		}
		jobIDs[job.ID] = struct{}{}
		jobsByRaw[job.RawFindingID] = struct{}{}
		if job.InsertionOrdinal > maximumOrdinal {
			maximumOrdinal = job.InsertionOrdinal
		}
	}
	if len(state.RawFindings) != len(state.DeduplicationJobs) {
		return errors.New("raw repository review findings and deduplication jobs differ")
	}
	for _, raw := range state.RawFindings {
		if raw.DeduplicatedFindingID != "" {
			if _, found := deduplicatedByID[raw.DeduplicatedFindingID]; !found {
				return errors.New("raw repository review finding has an invalid target")
			}
		}
	}
	expected := state
	reconcileFindingsProcessingCounters(&expected)
	if state.FindingsProcessing.RawTotal != expected.FindingsProcessing.RawTotal ||
		state.FindingsProcessing.Pending != expected.FindingsProcessing.Pending ||
		state.FindingsProcessing.Processing != expected.FindingsProcessing.Processing ||
		state.FindingsProcessing.Failed != expected.FindingsProcessing.Failed ||
		state.FindingsProcessing.Completed != expected.FindingsProcessing.Completed ||
		state.FindingsProcessing.New != expected.FindingsProcessing.New ||
		state.FindingsProcessing.Duplicates != expected.FindingsProcessing.Duplicates ||
		maximumOrdinal > 0 && state.NextDeduplicationOrdinal <= maximumOrdinal {
		return errors.New("invalid repository review findings processing counters")
	}
	return nil
}

func rawFindingCandidate(raw RawReviewFinding) FindingCandidate {
	return FindingCandidate{
		Severity: raw.Severity, Title: raw.Title, Symbol: raw.Symbol, File: raw.File.Path,
		Line: raw.Line, Message: raw.Message, Evidence: raw.Evidence, Impact: raw.Impact,
		Validation: raw.Validation, MatchHints: raw.MatchHints, FixEffort: raw.FixEffort,
	}
}

// RawReviewFindingDiagnosisDigest seals every immutable diagnosis and
// provenance field while leaving only processing metadata free to advance.
func RawReviewFindingDiagnosisDigest(raw RawReviewFinding) string {
	encoded, _ := json.Marshal(struct {
		CampaignID       string                 `json:"campaign_id"`
		AdmissionBucket  string                 `json:"admission_bucket"`
		InsertionOrdinal uint64                 `json:"insertion_ordinal"`
		LegacyFindingID  string                 `json:"legacy_finding_id,omitempty"`
		Repository       string                 `json:"repository"`
		CommitSHA        string                 `json:"commit_sha"`
		File             FileRef                `json:"file"`
		Line             *int                   `json:"line,omitempty"`
		ContextID        string                 `json:"context_id"`
		RunID            string                 `json:"run_id"`
		AssignmentID     string                 `json:"assignment_id"`
		Model            string                 `json:"model"`
		ModelAlias       string                 `json:"model_alias,omitempty"`
		Account          string                 `json:"account,omitempty"`
		Reviewer         string                 `json:"reviewer,omitempty"`
		Diagnosis        DeduplicationDiagnosis `json:"diagnosis"`
	}{
		CampaignID: raw.CampaignID, AdmissionBucket: raw.AdmissionBucket,
		InsertionOrdinal: raw.InsertionOrdinal, LegacyFindingID: raw.LegacyFindingID,
		Repository: raw.Repository, CommitSHA: raw.CommitSHA,
		File: raw.File, Line: raw.Line, ContextID: raw.ContextID, RunID: raw.RunID,
		AssignmentID: raw.AssignmentID, Model: raw.Model, ModelAlias: raw.ModelAlias,
		Account: raw.Account, Reviewer: raw.Reviewer,
		Diagnosis: DeduplicationDiagnosis{
			Severity: raw.Severity, Title: raw.Title, Symbol: raw.Symbol,
			Message: raw.Message, Evidence: raw.Evidence, Impact: raw.Impact,
			Validation: raw.Validation, MatchHints: raw.MatchHints, FixEffort: raw.FixEffort,
		},
	})
	return stableID("sha256:", string(encoded))
}

func deduplicatedFindingMatchesRaw(
	finding DeduplicatedReviewFinding,
	raw RawReviewFinding,
) bool {
	return finding.CampaignID == raw.CampaignID &&
		finding.AdmissionBucket == raw.AdmissionBucket &&
		finding.Repository == raw.Repository && finding.CommitSHA == raw.CommitSHA &&
		finding.File == raw.File && reflect.DeepEqual(finding.Line, raw.Line) &&
		finding.Severity == raw.Severity && finding.Title == raw.Title &&
		finding.Symbol == raw.Symbol && finding.Message == raw.Message &&
		finding.Evidence == raw.Evidence && finding.Impact == raw.Impact &&
		reflect.DeepEqual(finding.Validation, raw.Validation) &&
		reflect.DeepEqual(finding.MatchHints, raw.MatchHints) &&
		reflect.DeepEqual(finding.FixEffort, raw.FixEffort)
}

func validRawFindingState(raw RawReviewFinding) bool {
	switch raw.State {
	case RawFindingDeduplicationPending, RawFindingDeduplicationRunning:
		return raw.Disposition == RawFindingDispositionUndecided &&
			raw.DeduplicatedFindingID == "" && raw.Failure == nil
	case RawFindingDeduplicationCompleted:
		return (raw.Disposition == RawFindingDispositionNew ||
			raw.Disposition == RawFindingDispositionDuplicate) &&
			raw.DeduplicatedFindingID != "" && raw.Failure == nil
	case RawFindingDeduplicationFailed:
		return raw.Disposition == RawFindingDispositionUndecided &&
			raw.DeduplicatedFindingID == "" && raw.Failure != nil
	default:
		return false
	}
}

func validRawFindingHistoryState(
	state RawFindingDeduplicationState,
	disposition RawFindingDisposition,
) bool {
	switch state {
	case RawFindingDeduplicationPending, RawFindingDeduplicationRunning,
		RawFindingDeduplicationFailed:
		return disposition == RawFindingDispositionUndecided
	case RawFindingDeduplicationCompleted:
		return disposition == RawFindingDispositionNew ||
			disposition == RawFindingDispositionDuplicate
	default:
		return false
	}
}

func validDeduplicationJobState(job DeduplicationJob) bool {
	switch job.State {
	case DeduplicationJobPending:
		return job.LeaseID == "" && job.LeaseExpiresAt.IsZero() && job.Failure == nil
	case DeduplicationJobRunning:
		return validBoundedText(job.LeaseID, 256) && !job.LeaseExpiresAt.IsZero() && job.Failure == nil
	case DeduplicationJobCompleted:
		return job.LeaseID == "" && job.LeaseExpiresAt.IsZero() && job.Failure == nil &&
			(job.Decision.Decision == "new" || job.Decision.Decision == "duplicate")
	case DeduplicationJobFailed:
		return job.LeaseID == "" && job.LeaseExpiresAt.IsZero() && job.Failure != nil
	default:
		return false
	}
}

func validDeduplicationJobHistoryState(state DeduplicationJobState) bool {
	switch state {
	case DeduplicationJobPending, DeduplicationJobRunning,
		DeduplicationJobCompleted, DeduplicationJobFailed:
		return true
	default:
		return false
	}
}

func validDeduplicationFailure(failure *DeduplicationFailure) bool {
	return failure == nil || validBoundedText(strings.TrimSpace(failure.Code), 128) &&
		validBoundedText(strings.TrimSpace(failure.Message), 4096) && !failure.At.IsZero()
}
