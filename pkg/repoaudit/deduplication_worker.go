package repoaudit

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DeduplicationDefaultLeaseDuration = 5 * time.Minute
	DeduplicationMaximumLeaseDuration = time.Hour
)

var (
	ErrDeduplicationUniverseChanged = errors.New("deduplication candidate universe changed")
	ErrDeduplicationLeaseExpired    = errors.New("deduplication job lease expired")
)

type DeduplicationClaim struct {
	Job            DeduplicationJob                 `json:"job"`
	RawFinding     RawReviewFinding                 `json:"raw_finding"`
	Candidates     []DeduplicationCandidateSnapshot `json:"candidates"`
	UniverseDigest string                           `json:"candidate_universe_digest"`
}

type DeduplicationCompletion struct {
	JobID                   string                        `json:"job_id"`
	LeaseID                 string                        `json:"lease_id"`
	CandidateUniverseDigest string                        `json:"candidate_universe_digest"`
	ShortlistedScores       []DeduplicationCandidateScore `json:"shortlisted_scores,omitempty"`
	Decision                DeduplicationJudgment         `json:"decision"`
}

type DeduplicationProcessOptions struct {
	Score             DeduplicationScorer
	Judge             DeduplicationJudge
	ModelInputCeiling int
	LeaseDuration     time.Duration
}

type DeduplicationProcessResult struct {
	Completed  int `json:"completed"`
	Created    int `json:"created"`
	Duplicates int `json:"duplicates"`
	Failed     int `json:"failed"`
	Deferred   int `json:"deferred"`
}

// DeduplicationRetryFailure is one safe, source-specific rejection from a
// bulk retry request. It never exposes stored provider output or internal
// persistence errors.
type DeduplicationRetryFailure struct {
	SourceID string `json:"source_id"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// DeduplicationRetryResult reports every source that was re-admitted and every
// valid source selection that could not be retried. Both slices are kept in
// request order.
type DeduplicationRetryResult struct {
	RetriedIDs []string                    `json:"retried_ids"`
	Failures   []DeduplicationRetryFailure `json:"failures"`
}

type deduplicationProcessOutcome struct {
	created    bool
	duplicate  bool
	failed     bool
	deferred   bool
	historical bool
	err        error
}

// ClaimDeduplicationJob atomically freezes the complete existing candidate
// universe for the raw finding's campaign and bucket. An earlier nonterminal
// job in the same bucket always wins, providing strict FIFO serialization.
func (s Store) ClaimDeduplicationJob(
	repository, jobID string,
	leaseDuration time.Duration,
) (RepositoryState, DeduplicationClaim, bool, error) {
	repository = strings.TrimSpace(repository)
	jobID = strings.TrimSpace(jobID)
	if leaseDuration == 0 {
		leaseDuration = DeduplicationDefaultLeaseDuration
	}
	if repository == "" || jobID == "" || leaseDuration <= 0 ||
		leaseDuration > DeduplicationMaximumLeaseDuration {
		return RepositoryState{}, DeduplicationClaim{}, false, errors.New("invalid deduplication claim")
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, DeduplicationClaim{}, false, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, DeduplicationClaim{}, false, err
	}
	jobIndex := deduplicationJobIndexByID(state.DeduplicationJobs, jobID)
	if jobIndex < 0 {
		return RepositoryState{}, DeduplicationClaim{}, false, os.ErrNotExist
	}
	job := &state.DeduplicationJobs[jobIndex]
	rawIndex := rawFindingIndexByID(state.RawFindings, job.RawFindingID)
	if rawIndex < 0 {
		return RepositoryState{}, DeduplicationClaim{}, false, errors.New("deduplication job raw finding is missing")
	}
	raw := &state.RawFindings[rawIndex]
	now := s.clock()
	if job.State == DeduplicationJobCompleted || job.State == DeduplicationJobFailed {
		return state, DeduplicationClaim{Job: *job, RawFinding: *raw}, false, nil
	}
	if job.State == DeduplicationJobRunning && now.Before(job.LeaseExpiresAt) {
		return state, DeduplicationClaim{Job: *job, RawFinding: *raw}, false, nil
	}
	if job.State == DeduplicationJobRunning {
		failure := safeDeduplicationFailure("lease_expired", true, now)
		job.State = DeduplicationJobPending
		job.LeaseID = ""
		job.LeaseExpiresAt = time.Time{}
		job.Failure = nil
		job.CandidateUniverseDigest = ""
		job.CandidateVersions = nil
		job.ShortlistedScores = nil
		job.Decision = DeduplicationJudgment{}
		job.History = appendDeduplicationJobHistory(job.History, DeduplicationJobHistoryEntry{
			State: DeduplicationJobPending, Attempt: job.Attempts, Failure: failure, At: now,
		})
		raw.State = RawFindingDeduplicationPending
		raw.Version++
		raw.UpdatedAt = now
		raw.History = appendRawFindingHistory(raw.History, RawFindingHistoryEntry{
			State: RawFindingDeduplicationPending, Disposition: RawFindingDispositionUndecided,
			Attempt: job.Attempts, Failure: failure, At: now,
		})
	}
	if job.State != DeduplicationJobPending || raw.State != RawFindingDeduplicationPending ||
		raw.Disposition != RawFindingDispositionUndecided {
		return RepositoryState{}, DeduplicationClaim{}, false, ErrConflict
	}
	if historicalDeduplicationFailedFence(state, *job) {
		return state, DeduplicationClaim{Job: *job, RawFinding: *raw}, false, nil
	}
	if job.Attempts >= DeduplicationAttemptLimit {
		markDeduplicationFailed(raw, job, "attempt_limit", now)
		state.Version++
		state.UpdatedAt = now
		reconcileFindingsProcessingCounters(&state)
		state.FindingsProcessing.UpdatedAt = now
		if saveErr := s.save(&state); saveErr != nil {
			return RepositoryState{}, DeduplicationClaim{}, false, saveErr
		}
		return state, DeduplicationClaim{Job: *job, RawFinding: *raw}, false, nil
	}
	for index := range state.DeduplicationJobs {
		other := state.DeduplicationJobs[index]
		if other.ID == job.ID || other.AdmissionBucket != job.AdmissionBucket ||
			other.InsertionOrdinal >= job.InsertionOrdinal {
			continue
		}
		if other.State != DeduplicationJobPending && other.State != DeduplicationJobRunning {
			continue
		}
		return state, DeduplicationClaim{Job: *job, RawFinding: *raw}, false, nil
	}
	candidates := deduplicationCandidateSnapshots(state, raw.CampaignID, raw.AdmissionBucket)
	digest, err := DeduplicationCandidateUniverseDigest(candidates)
	if err != nil {
		return RepositoryState{}, DeduplicationClaim{}, false, err
	}
	versions := make([]DeduplicationCandidateVersion, len(candidates))
	for index, candidate := range candidates {
		versions[index] = DeduplicationCandidateVersion{
			CandidateID: candidate.ID, Version: candidate.Version,
		}
	}
	job.Attempts++
	job.State = DeduplicationJobRunning
	job.LeaseID = "rdl_" + strings.ToLower(rand.Text())
	job.LeaseExpiresAt = now.Add(leaseDuration)
	job.CandidateUniverseDigest = digest
	job.CandidateVersions = versions
	job.ShortlistedScores = nil
	job.Decision = DeduplicationJudgment{}
	job.Failure = nil
	job.UpdatedAt = now
	job.History = appendDeduplicationJobHistory(job.History, DeduplicationJobHistoryEntry{
		State: DeduplicationJobRunning, Attempt: job.Attempts, LeaseID: job.LeaseID, At: now,
	})
	raw.State = RawFindingDeduplicationRunning
	raw.Failure = nil
	raw.Version++
	raw.UpdatedAt = now
	raw.History = appendRawFindingHistory(raw.History, RawFindingHistoryEntry{
		State: RawFindingDeduplicationRunning, Disposition: RawFindingDispositionUndecided,
		Attempt: job.Attempts, At: now,
	})
	state.Version++
	state.UpdatedAt = now
	reconcileFindingsProcessingCounters(&state)
	state.FindingsProcessing.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, DeduplicationClaim{}, false, err
	}
	claim := DeduplicationClaim{
		Job: *job, RawFinding: *raw, Candidates: candidates, UniverseDigest: digest,
	}
	return state, claim, true, nil
}

func historicalDeduplicationFailedFence(
	state RepositoryState,
	job DeduplicationJob,
) bool {
	rawIndex := rawFindingIndexByID(state.RawFindings, job.RawFindingID)
	if rawIndex < 0 || !HistoricalDeduplicationRawFinding(state.RawFindings[rawIndex]) {
		return false
	}
	for _, earlier := range state.DeduplicationJobs {
		if earlier.ID == job.ID || earlier.AdmissionBucket != job.AdmissionBucket ||
			earlier.InsertionOrdinal >= job.InsertionOrdinal ||
			earlier.State != DeduplicationJobFailed {
			continue
		}
		earlierRawIndex := rawFindingIndexByID(state.RawFindings, earlier.RawFindingID)
		if earlierRawIndex >= 0 &&
			HistoricalDeduplicationRawFinding(state.RawFindings[earlierRawIndex]) {
			return true
		}
	}
	return false
}

func deduplicationCandidateSnapshots(
	state RepositoryState, campaignID, admissionBucket string,
) []DeduplicationCandidateSnapshot {
	candidates := make([]DeduplicationCandidateSnapshot, 0)
	for _, finding := range state.DeduplicatedFindings {
		if finding.CampaignID != campaignID || finding.AdmissionBucket != admissionBucket {
			continue
		}
		candidates = append(candidates, DeduplicationCandidateSnapshot{
			ID: finding.ID, Version: finding.Version, CreationOrdinal: finding.CreationOrdinal,
			Diagnosis: deduplicationDiagnosisFromDeduplicated(finding),
		})
	}
	ordered, _ := normalizeDeduplicationCandidateSnapshots(candidates)
	return ordered
}

func deduplicationDiagnosisFromRaw(raw RawReviewFinding) DeduplicationDiagnosis {
	return DeduplicationDiagnosis{
		Severity: raw.Severity, Title: raw.Title, Symbol: raw.Symbol, Message: raw.Message,
		Evidence: raw.Evidence, Impact: raw.Impact, Validation: raw.Validation,
		MatchHints: raw.MatchHints, FixEffort: raw.FixEffort,
	}
}

func deduplicationDiagnosisFromDeduplicated(finding DeduplicatedReviewFinding) DeduplicationDiagnosis {
	return DeduplicationDiagnosis{
		Severity: finding.Severity, Title: finding.Title, Symbol: finding.Symbol, Message: finding.Message,
		Evidence: finding.Evidence, Impact: finding.Impact, Validation: finding.Validation,
		MatchHints: finding.MatchHints, FixEffort: finding.FixEffort,
	}
}

// CompleteDeduplicationJob atomically promotes one raw finding after
// rechecking its lease and the complete frozen candidate universe. It returns
// created=true only when this completion produced a new deduplicated finding.
func (s Store) CompleteDeduplicationJob(
	repository string,
	completion DeduplicationCompletion,
) (RepositoryState, DeduplicatedReviewFinding, bool, error) {
	repository = strings.TrimSpace(repository)
	completion.JobID = strings.TrimSpace(completion.JobID)
	completion.LeaseID = strings.TrimSpace(completion.LeaseID)
	completion.CandidateUniverseDigest = strings.TrimSpace(completion.CandidateUniverseDigest)
	completion.Decision.Decision = strings.ToLower(strings.TrimSpace(completion.Decision.Decision))
	completion.Decision.CandidateID = strings.TrimSpace(completion.Decision.CandidateID)
	if repository == "" || completion.JobID == "" || completion.LeaseID == "" ||
		completion.CandidateUniverseDigest == "" ||
		(completion.Decision.Decision != "new" && completion.Decision.Decision != "duplicate") ||
		(completion.Decision.Decision == "new") != (completion.Decision.CandidateID == "") {
		return RepositoryState{}, DeduplicatedReviewFinding{}, false,
			errors.New("invalid deduplication completion")
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, DeduplicatedReviewFinding{}, false, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, DeduplicatedReviewFinding{}, false, err
	}
	jobIndex := deduplicationJobIndexByID(state.DeduplicationJobs, completion.JobID)
	if jobIndex < 0 {
		return RepositoryState{}, DeduplicatedReviewFinding{}, false, os.ErrNotExist
	}
	job := &state.DeduplicationJobs[jobIndex]
	rawIndex := rawFindingIndexByID(state.RawFindings, job.RawFindingID)
	if rawIndex < 0 {
		return RepositoryState{}, DeduplicatedReviewFinding{}, false,
			errors.New("deduplication job raw finding is missing")
	}
	raw := &state.RawFindings[rawIndex]
	if job.State == DeduplicationJobCompleted && raw.State == RawFindingDeduplicationCompleted {
		targetIndex := deduplicatedFindingIndexByID(
			state.DeduplicatedFindings, raw.DeduplicatedFindingID,
		)
		if targetIndex < 0 || job.Decision != completion.Decision {
			return RepositoryState{}, DeduplicatedReviewFinding{}, false, ErrConflict
		}
		return state, state.DeduplicatedFindings[targetIndex],
			raw.Disposition == RawFindingDispositionNew, nil
	}
	now := s.clock()
	if job.State != DeduplicationJobRunning || raw.State != RawFindingDeduplicationRunning ||
		job.LeaseID != completion.LeaseID {
		return RepositoryState{}, DeduplicatedReviewFinding{}, false, ErrConflict
	}
	if !now.Before(job.LeaseExpiresAt) {
		return RepositoryState{}, DeduplicatedReviewFinding{}, false,
			ErrDeduplicationLeaseExpired
	}
	currentCandidates := deduplicationCandidateSnapshots(
		state, raw.CampaignID, raw.AdmissionBucket,
	)
	currentDigest, digestErr := DeduplicationCandidateUniverseDigest(currentCandidates)
	if digestErr != nil {
		return RepositoryState{}, DeduplicatedReviewFinding{}, false, digestErr
	}
	if completion.CandidateUniverseDigest != job.CandidateUniverseDigest ||
		currentDigest != job.CandidateUniverseDigest ||
		!deduplicationCandidateVersionsMatch(job.CandidateVersions, currentCandidates) {
		return RepositoryState{}, DeduplicatedReviewFinding{}, false,
			ErrDeduplicationUniverseChanged
	}
	shortlisted, shortlistErr := normalizeDurableDeduplicationScores(
		completion.ShortlistedScores, job, currentCandidates,
	)
	if shortlistErr != nil {
		return RepositoryState{}, DeduplicatedReviewFinding{}, false, shortlistErr
	}
	if completion.Decision.Decision == "duplicate" {
		selected := false
		for _, score := range shortlisted {
			if score.CandidateID == completion.Decision.CandidateID {
				selected = true
				break
			}
		}
		if !selected {
			return RepositoryState{}, DeduplicatedReviewFinding{}, false,
				errors.New("deduplication selected a candidate outside its shortlist")
		}
	}
	var target *DeduplicatedReviewFinding
	created := completion.Decision.Decision == "new"
	if created {
		finding := newDeduplicatedReviewFinding(
			*raw, job.InsertionOrdinal, state.Findings, now,
		)
		if deduplicatedFindingIndexByID(state.DeduplicatedFindings, finding.ID) >= 0 ||
			findingIndexByID(state.Findings, finding.ID) >= 0 {
			return RepositoryState{}, DeduplicatedReviewFinding{}, false, ErrConflict
		}
		state.DeduplicatedFindings = append(state.DeduplicatedFindings, finding)
		state.Findings = append(state.Findings, deduplicatedFindingProjection(finding, *raw, state.Findings))
		target = &state.DeduplicatedFindings[len(state.DeduplicatedFindings)-1]
		ensureMappingJobsForFindings(&state, []string{target.ID}, now)
		raw.Disposition = RawFindingDispositionNew
	} else {
		targetIndex := deduplicatedFindingIndexByID(
			state.DeduplicatedFindings, completion.Decision.CandidateID,
		)
		if targetIndex < 0 {
			return RepositoryState{}, DeduplicatedReviewFinding{}, false,
				ErrDeduplicationUniverseChanged
		}
		target = &state.DeduplicatedFindings[targetIndex]
		if target.CampaignID != raw.CampaignID || target.AdmissionBucket != raw.AdmissionBucket ||
			containsExactString(target.RawSourceIDs, raw.ID) {
			return RepositoryState{}, DeduplicatedReviewFinding{}, false, ErrConflict
		}
		target.RawSourceIDs = append(target.RawSourceIDs, raw.ID)
		target.History = appendDeduplicatedFindingHistory(
			target.History,
			DeduplicatedFindingHistoryEntry{Action: "source_attached", RawFindingID: raw.ID, At: now},
		)
		target.Version++
		target.UpdatedAt = now
		raw.Disposition = RawFindingDispositionDuplicate
	}
	raw.State = RawFindingDeduplicationCompleted
	raw.DeduplicatedFindingID = target.ID
	raw.Failure = nil
	raw.Version++
	raw.UpdatedAt = now
	raw.History = appendRawFindingHistory(raw.History, RawFindingHistoryEntry{
		State: RawFindingDeduplicationCompleted, Disposition: raw.Disposition,
		DeduplicatedFindingID: target.ID, Attempt: job.Attempts, At: now,
	})
	job.State = DeduplicationJobCompleted
	job.LeaseID = ""
	job.LeaseExpiresAt = time.Time{}
	job.ShortlistedScores = shortlisted
	job.Decision = completion.Decision
	job.Failure = nil
	job.UpdatedAt = now
	job.History = appendDeduplicationJobHistory(job.History, DeduplicationJobHistoryEntry{
		State: DeduplicationJobCompleted, Attempt: job.Attempts, At: now,
	})
	if HistoricalDeduplicationRawFinding(*raw) {
		if restoreErr := restoreHistoricalDeduplicatedLifecycle(
			&state, *raw, target.ID, now,
		); restoreErr != nil {
			return RepositoryState{}, DeduplicatedReviewFinding{}, false, restoreErr
		}
	}
	state.Version++
	state.UpdatedAt = now
	reconcileFindingsProcessingCounters(&state)
	state.FindingsProcessing.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, DeduplicatedReviewFinding{}, false, err
	}
	targetIndex := deduplicatedFindingIndexByID(state.DeduplicatedFindings, target.ID)
	return state, state.DeduplicatedFindings[targetIndex], created, nil
}

func deduplicationCandidateVersionsMatch(
	expected []DeduplicationCandidateVersion,
	candidates []DeduplicationCandidateSnapshot,
) bool {
	if len(expected) != len(candidates) {
		return false
	}
	for index := range candidates {
		if expected[index].CandidateID != candidates[index].ID ||
			expected[index].Version != candidates[index].Version {
			return false
		}
	}
	return true
}

func normalizeDurableDeduplicationScores(
	scores []DeduplicationCandidateScore,
	job *DeduplicationJob,
	candidates []DeduplicationCandidateSnapshot,
) ([]DeduplicationCandidateScore, error) {
	if job == nil || len(scores) > job.ModelSnapshot.CandidateLimit {
		return nil, errors.New("invalid durable deduplication shortlist")
	}
	versions := make(map[string]int64, len(candidates))
	for _, candidate := range candidates {
		versions[candidate.ID] = candidate.Version
	}
	seen := make(map[string]struct{}, len(scores))
	result := append([]DeduplicationCandidateScore(nil), scores...)
	for _, score := range result {
		if _, found := versions[score.CandidateID]; !found ||
			score.Score < job.ModelSnapshot.SimilarityThreshold || score.Score > 100 ||
			!validDeduplicationExplanation(score.Explanation) {
			return nil, errors.New("invalid durable deduplication score")
		}
		if _, duplicate := seen[score.CandidateID]; duplicate {
			return nil, errors.New("duplicate durable deduplication score")
		}
		seen[score.CandidateID] = struct{}{}
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].Score != result[right].Score {
			return result[left].Score > result[right].Score
		}
		leftOrdinal, rightOrdinal := uint64(0), uint64(0)
		for _, candidate := range candidates {
			if candidate.ID == result[left].CandidateID {
				leftOrdinal = candidate.CreationOrdinal
			}
			if candidate.ID == result[right].CandidateID {
				rightOrdinal = candidate.CreationOrdinal
			}
		}
		if leftOrdinal != rightOrdinal {
			return leftOrdinal < rightOrdinal
		}
		return result[left].CandidateID < result[right].CandidateID
	})
	return result, nil
}

func newDeduplicatedReviewFinding(
	raw RawReviewFinding,
	creationOrdinal uint64,
	projections []Finding,
	now time.Time,
) DeduplicatedReviewFinding {
	finding := DeduplicatedReviewFinding{
		ID: stableID("rdf_", raw.ID), Version: 1, CampaignID: raw.CampaignID,
		AdmissionBucket: raw.AdmissionBucket, CreationOrdinal: creationOrdinal,
		DiagnosisDigest: raw.DiagnosisDigest,
		Repository:      raw.Repository, CommitSHA: raw.CommitSHA, File: raw.File, Line: raw.Line,
		Severity: raw.Severity, Title: raw.Title, Symbol: raw.Symbol, Message: raw.Message,
		Evidence: raw.Evidence, Impact: raw.Impact, Validation: raw.Validation,
		MatchHints: raw.MatchHints, FixEffort: raw.FixEffort,
		RawSourceIDs: []string{raw.ID}, Status: FindingOpen,
		History:   []DeduplicatedFindingHistoryEntry{{Action: "created", RawFindingID: raw.ID, At: now}},
		CreatedAt: now, UpdatedAt: now,
	}
	if legacyIndex := findingIndexByID(projections, raw.LegacyFindingID); legacyIndex >= 0 {
		legacy := projections[legacyIndex]
		if legacy.Status == FindingOpen || legacy.Status == FindingDismissed ||
			legacy.Status == FindingPosted {
			finding.Status = legacy.Status
		}
		finding.IssueDraftID = legacy.IssueDraftID
		finding.TargetBranch = legacy.TargetBranch
		finding.AdvertisedDefaultBranch = legacy.AdvertisedDefaultBranch
		finding.TargetIsDefault = legacy.TargetIsDefault
	}
	return finding
}

func deduplicatedFindingProjection(
	finding DeduplicatedReviewFinding,
	raw RawReviewFinding,
	legacyProjections []Finding,
) Finding {
	candidate := rawFindingCandidate(raw)
	contributorModel := raw.ModelAlias
	if contributorModel == "" {
		contributorModel = raw.Model
	}
	projection := Finding{
		ID: finding.ID, CampaignID: finding.CampaignID,
		Fingerprint: findingFingerprint(finding.File, candidate),
		Repository:  finding.Repository, CommitSHA: finding.CommitSHA,
		File: finding.File, Line: finding.Line, Severity: finding.Severity,
		Title: finding.Title, Symbol: finding.Symbol, Message: finding.Message,
		Evidence: finding.Evidence, Impact: finding.Impact, Validation: finding.Validation,
		MatchHints: finding.MatchHints, FixEffort: finding.FixEffort,
		ContextIDs: []string{raw.ContextID}, Models: []string{contributorModel},
		ObservationCount: 1,
		Observations: []FindingObservation{findingObservationFrom(
			candidate, raw.ContextID, raw.Model, raw.ModelAlias, raw.Account, raw.Reviewer,
		)},
		Status: finding.Status, TargetBranch: finding.TargetBranch,
		AdvertisedDefaultBranch: finding.AdvertisedDefaultBranch,
		TargetIsDefault:         finding.TargetIsDefault,
		Version:                 1, CreatedAt: finding.CreatedAt, UpdatedAt: finding.UpdatedAt,
	}
	if legacyIndex := findingIndexByID(legacyProjections, raw.LegacyFindingID); legacyIndex >= 0 {
		legacy := legacyProjections[legacyIndex]
		projection.DefaultBranchVerified = legacy.DefaultBranchVerified
	}
	return projection
}

// ProcessPendingDeduplicationJobs runs one FIFO queue per bucket with up to
// four queues in parallel. Every model call occurs after Claim has released
// the JSON-ledger lock and before Complete reacquires it.
func (s Store) ProcessPendingDeduplicationJobs(
	ctx context.Context,
	repository string,
	options DeduplicationProcessOptions,
) (DeduplicationProcessResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	state, found, err := s.Get(repository)
	if err != nil || !found {
		if err == nil {
			err = os.ErrNotExist
		}
		return DeduplicationProcessResult{}, err
	}
	type bucketQueue struct {
		bucket string
		jobs   []DeduplicationJob
	}
	byBucket := make(map[string][]DeduplicationJob)
	for _, job := range state.DeduplicationJobs {
		if job.State == DeduplicationJobPending {
			byBucket[job.AdmissionBucket] = append(byBucket[job.AdmissionBucket], job)
		}
	}
	queues := make([]bucketQueue, 0, len(byBucket))
	for bucket, jobs := range byBucket {
		sort.SliceStable(jobs, func(left, right int) bool {
			if jobs[left].InsertionOrdinal != jobs[right].InsertionOrdinal {
				return jobs[left].InsertionOrdinal < jobs[right].InsertionOrdinal
			}
			return jobs[left].ID < jobs[right].ID
		})
		queues = append(queues, bucketQueue{bucket: bucket, jobs: jobs})
	}
	sort.SliceStable(queues, func(left, right int) bool {
		if queues[left].jobs[0].InsertionOrdinal != queues[right].jobs[0].InsertionOrdinal {
			return queues[left].jobs[0].InsertionOrdinal < queues[right].jobs[0].InsertionOrdinal
		}
		return queues[left].bucket < queues[right].bucket
	})
	if len(queues) == 0 {
		return DeduplicationProcessResult{}, nil
	}
	workerCount := min(DeduplicationConcurrency, len(queues))
	work := make(chan bucketQueue)
	outcomes := make(chan deduplicationProcessOutcome, len(state.DeduplicationJobs))
	var wait sync.WaitGroup
	for range workerCount {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for queue := range work {
				for _, queuedJob := range queue.jobs {
					item := s.processOneDeduplicationJob(ctx, repository, queuedJob.ID, options)
					outcomes <- item
					if item.historical && item.failed {
						// A terminal historical failure is a durable bucket
						// checkpoint fence. Other buckets remain independent.
						break
					}
					if item.err != nil || item.failed || item.deferred {
						// A pending earlier job must continue to fence later jobs in
						// this bucket until a later processor pass.
						if item.deferred || !item.failed {
							break
						}
					}
				}
			}
		}()
	}
	go func() {
		defer close(work)
		for _, queue := range queues {
			select {
			case work <- queue:
			case <-ctx.Done():
				return
			}
		}
	}()
	wait.Wait()
	close(outcomes)
	result := DeduplicationProcessResult{}
	var joined error
	for item := range outcomes {
		if item.created || item.duplicate {
			result.Completed++
		}
		if item.created {
			result.Created++
		}
		if item.duplicate {
			result.Duplicates++
		}
		if item.failed {
			result.Failed++
		}
		if item.deferred {
			result.Deferred++
		}
		joined = errors.Join(joined, item.err)
	}
	if ctx.Err() != nil {
		joined = errors.Join(joined, ctx.Err())
	}
	return result, joined
}

func (s Store) processOneDeduplicationJob(
	ctx context.Context,
	repository, jobID string,
	options DeduplicationProcessOptions,
) (result deduplicationProcessOutcome) {
	_, claim, claimed, err := s.ClaimDeduplicationJob(repository, jobID, options.LeaseDuration)
	if err != nil {
		result.err = err
		return result
	}
	if !claimed {
		result.historical = HistoricalDeduplicationRawFinding(claim.RawFinding)
		result.deferred = claim.Job.State == DeduplicationJobPending ||
			claim.Job.State == DeduplicationJobRunning
		result.failed = claim.Job.State == DeduplicationJobFailed
		return result
	}
	result.historical = HistoricalDeduplicationRawFinding(claim.RawFinding)
	needsModel := claim.Job.ModelSnapshot.CandidateLimit > 0 && len(claim.Candidates) > 0
	var releaseSlot func()
	if needsModel {
		releaseSlot, err = s.AcquireDeduplicationSlot(ctx)
		if err != nil {
			_, _, terminal, releaseErr := s.FailDeduplicationJob(
				repository, claim.Job.ID, claim.Job.LeaseID, err,
			)
			result.failed = terminal
			result.err = errors.Join(err, releaseErr)
			return result
		}
	}
	modelResult, modelErr := EvaluateDeduplicationCandidates(
		ctx, claim.Job.ModelSnapshot, deduplicationDiagnosisFromRaw(claim.RawFinding),
		claim.Candidates, options.ModelInputCeiling, options.Score, options.Judge,
	)
	if releaseSlot != nil {
		releaseSlot()
	}
	if modelErr != nil {
		_, _, terminal, releaseErr := s.FailDeduplicationJob(
			repository, claim.Job.ID, claim.Job.LeaseID, modelErr,
		)
		result.failed = terminal
		result.err = errors.Join(modelErr, releaseErr)
		return result
	}
	completion := DeduplicationCompletion{
		JobID: claim.Job.ID, LeaseID: claim.Job.LeaseID,
		CandidateUniverseDigest: claim.UniverseDigest,
		Decision:                modelResult.Judgment,
	}
	for _, shortlisted := range modelResult.Shortlisted {
		completion.ShortlistedScores = append(
			completion.ShortlistedScores,
			DeduplicationCandidateScore{
				CandidateID: shortlisted.ID, Score: shortlisted.Score,
				Explanation: shortlisted.Explanation,
			},
		)
	}
	if completion.Decision.Decision == "duplicate" {
		for _, shortlisted := range modelResult.Shortlisted {
			if shortlisted.OpaqueID == completion.Decision.CandidateID {
				completion.Decision.CandidateID = shortlisted.ID
				break
			}
		}
	}
	_, _, created, completionErr := s.CompleteDeduplicationJob(repository, completion)
	if completionErr != nil {
		_, _, terminal, releaseErr := s.FailDeduplicationJob(
			repository, claim.Job.ID, claim.Job.LeaseID, completionErr,
		)
		result.failed = terminal
		result.err = errors.Join(completionErr, releaseErr)
		return result
	}
	result.created = created
	result.duplicate = !created
	return result
}

// FailDeduplicationJob releases a verified lease after a provider, validation,
// or stale-universe failure. Attempts below the limit return to pending; the
// third failure is retained as a readable terminal raw finding.
func (s Store) FailDeduplicationJob(
	repository, jobID, leaseID string,
	cause error,
) (RepositoryState, RawReviewFinding, bool, error) {
	repository = strings.TrimSpace(repository)
	jobID = strings.TrimSpace(jobID)
	leaseID = strings.TrimSpace(leaseID)
	if repository == "" || jobID == "" || leaseID == "" {
		return RepositoryState{}, RawReviewFinding{}, false, errors.New("invalid deduplication failure release")
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, RawReviewFinding{}, false, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, RawReviewFinding{}, false, err
	}
	jobIndex := deduplicationJobIndexByID(state.DeduplicationJobs, jobID)
	if jobIndex < 0 {
		return RepositoryState{}, RawReviewFinding{}, false, os.ErrNotExist
	}
	job := &state.DeduplicationJobs[jobIndex]
	rawIndex := rawFindingIndexByID(state.RawFindings, job.RawFindingID)
	if rawIndex < 0 {
		return RepositoryState{}, RawReviewFinding{}, false, errors.New("deduplication job raw finding is missing")
	}
	raw := &state.RawFindings[rawIndex]
	if job.State != DeduplicationJobRunning || raw.State != RawFindingDeduplicationRunning ||
		job.LeaseID != leaseID {
		return RepositoryState{}, RawReviewFinding{}, false, ErrConflict
	}
	now := s.clock()
	code := "processing_failed"
	if errors.Is(cause, ErrDeduplicationUniverseChanged) {
		code = "candidate_universe_changed"
	} else if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		code = "processing_interrupted"
	} else if errors.Is(cause, ErrDeduplicationLeaseExpired) {
		code = "lease_expired"
	}
	terminal := job.Attempts >= DeduplicationAttemptLimit
	if terminal {
		markDeduplicationFailed(raw, job, code, now)
	} else {
		failure := safeDeduplicationFailure(code, true, now)
		job.State = DeduplicationJobPending
		job.LeaseID = ""
		job.LeaseExpiresAt = time.Time{}
		job.CandidateUniverseDigest = ""
		job.CandidateVersions = nil
		job.ShortlistedScores = nil
		job.Decision = DeduplicationJudgment{}
		job.Failure = nil
		job.UpdatedAt = now
		job.History = appendDeduplicationJobHistory(job.History, DeduplicationJobHistoryEntry{
			State: DeduplicationJobPending, Attempt: job.Attempts, Failure: failure, At: now,
		})
		raw.State = RawFindingDeduplicationPending
		raw.Failure = nil
		raw.Version++
		raw.UpdatedAt = now
		raw.History = appendRawFindingHistory(raw.History, RawFindingHistoryEntry{
			State: RawFindingDeduplicationPending, Disposition: RawFindingDispositionUndecided,
			Attempt: job.Attempts, Failure: failure, At: now,
		})
	}
	state.Version++
	state.UpdatedAt = now
	reconcileFindingsProcessingCounters(&state)
	state.FindingsProcessing.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, RawReviewFinding{}, false, err
	}
	return state, *raw, terminal, nil
}

func markDeduplicationFailed(
	raw *RawReviewFinding, job *DeduplicationJob, code string, now time.Time,
) {
	failure := safeDeduplicationFailure(code, true, now)
	job.State = DeduplicationJobFailed
	job.LeaseID = ""
	job.LeaseExpiresAt = time.Time{}
	job.Failure = failure
	job.UpdatedAt = now
	job.History = appendDeduplicationJobHistory(job.History, DeduplicationJobHistoryEntry{
		State: DeduplicationJobFailed, Attempt: job.Attempts, Failure: failure, At: now,
	})
	raw.State = RawFindingDeduplicationFailed
	raw.Disposition = RawFindingDispositionUndecided
	raw.DeduplicatedFindingID = ""
	raw.Failure = failure
	raw.Version++
	raw.UpdatedAt = now
	raw.History = appendRawFindingHistory(raw.History, RawFindingHistoryEntry{
		State: RawFindingDeduplicationFailed, Disposition: RawFindingDispositionUndecided,
		Attempt: job.Attempts, Failure: failure, At: now,
	})
}

func safeDeduplicationFailure(code string, retryable bool, now time.Time) *DeduplicationFailure {
	message := "Deduplication processing failed."
	switch code {
	case "candidate_universe_changed":
		message = "Finding candidates changed; deduplication will retry."
	case "processing_interrupted":
		message = "Deduplication processing was interrupted."
	case "lease_expired":
		message = "Deduplication processing lease expired."
	case "attempt_limit":
		message = "Deduplication processing reached its attempt limit."
	}
	return &DeduplicationFailure{Code: code, Message: message, Retryable: retryable, At: now}
}

// RetryDeduplication explicitly re-admits one terminal failed raw finding at
// the tail of its bucket. The raw diagnosis and provenance remain unchanged.
func (s Store) RetryDeduplication(
	repository, rawID string,
) (RepositoryState, RawReviewFinding, error) {
	repository = strings.TrimSpace(repository)
	rawID = strings.TrimSpace(rawID)
	if repository == "" || rawID == "" {
		return RepositoryState{}, RawReviewFinding{}, errors.New("repository and raw finding ID are required")
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, RawReviewFinding{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, RawReviewFinding{}, err
	}
	now := s.clock()
	retried, err := retryDeduplicationInState(&state, rawID, now)
	if err != nil {
		return RepositoryState{}, RawReviewFinding{}, err
	}
	state.Version++
	state.UpdatedAt = now
	reconcileFindingsProcessingCounters(&state)
	state.FindingsProcessing.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, RawReviewFinding{}, err
	}
	return state, retried, nil
}

// RetryDeduplications re-admits up to 200 explicitly selected failed sources
// under one repository lock and one durable write. Selection-shape errors are
// rejected before the repository is loaded; valid mixed selections receive a
// safe failure for each ineligible source.
func (s Store) RetryDeduplications(
	repository string,
	sourceIDs []string,
) (RepositoryState, DeduplicationRetryResult, error) {
	repository = strings.TrimSpace(repository)
	normalizedIDs, err := NormalizeDeduplicationRetrySourceIDs(sourceIDs)
	if err != nil {
		return RepositoryState{}, DeduplicationRetryResult{}, err
	}
	if repository == "" {
		return RepositoryState{}, DeduplicationRetryResult{}, errors.New("repository is required")
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, DeduplicationRetryResult{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, DeduplicationRetryResult{}, err
	}
	result := DeduplicationRetryResult{
		RetriedIDs: make([]string, 0, len(normalizedIDs)),
		Failures:   make([]DeduplicationRetryFailure, 0),
	}
	now := s.clock()
	for _, sourceID := range normalizedIDs {
		rawIndex := rawFindingIndexByID(state.RawFindings, sourceID)
		if rawIndex < 0 {
			result.Failures = append(result.Failures, deduplicationRetryFailure(
				sourceID,
				"not_found",
				"Finding processing source was not found.",
			))
			continue
		}
		if HistoricalDeduplicationRawFinding(state.RawFindings[rawIndex]) {
			result.Failures = append(result.Failures, deduplicationRetryFailure(
				sourceID,
				"historical_replay_required",
				"Historical sources must be retried through historical consolidation.",
			))
			continue
		}
		if _, retryErr := retryDeduplicationInState(&state, sourceID, now); retryErr != nil {
			result.Failures = append(result.Failures, deduplicationRetryFailure(
				sourceID,
				"not_retryable",
				"Finding processing source is not retryable.",
			))
			continue
		}
		result.RetriedIDs = append(result.RetriedIDs, sourceID)
	}
	if len(result.RetriedIDs) == 0 {
		return state, result, nil
	}
	state.Version++
	state.UpdatedAt = now
	reconcileFindingsProcessingCounters(&state)
	state.FindingsProcessing.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, DeduplicationRetryResult{}, err
	}
	return state, result, nil
}

// NormalizeDeduplicationRetrySourceIDs validates and normalizes one bounded
// explicit retry selection without reading or mutating repository state.
func NormalizeDeduplicationRetrySourceIDs(sourceIDs []string) ([]string, error) {
	if len(sourceIDs) == 0 || len(sourceIDs) > 200 {
		return nil, errors.New("one to 200 source IDs are required")
	}
	normalized := make([]string, 0, len(sourceIDs))
	seen := make(map[string]struct{}, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		sourceID = strings.TrimSpace(sourceID)
		if !validBoundedText(sourceID, 256) {
			return nil, errors.New("invalid source ID")
		}
		if _, duplicate := seen[sourceID]; duplicate {
			return nil, errors.New("duplicate source ID")
		}
		seen[sourceID] = struct{}{}
		normalized = append(normalized, sourceID)
	}
	return normalized, nil
}

func retryDeduplicationInState(
	state *RepositoryState,
	rawID string,
	now time.Time,
) (RawReviewFinding, error) {
	if state == nil {
		return RawReviewFinding{}, errors.New("repository review state is required")
	}
	rawIndex := rawFindingIndexByID(state.RawFindings, rawID)
	if rawIndex < 0 {
		return RawReviewFinding{}, os.ErrNotExist
	}
	jobIndex := deduplicationJobIndexByRawID(state.DeduplicationJobs, rawID)
	if jobIndex < 0 {
		return RawReviewFinding{}, errors.New("raw finding deduplication job is missing")
	}
	raw := &state.RawFindings[rawIndex]
	job := &state.DeduplicationJobs[jobIndex]
	if raw.State != RawFindingDeduplicationFailed || job.State != DeduplicationJobFailed ||
		raw.DeduplicatedFindingID != "" {
		return RawReviewFinding{}, ErrConflict
	}
	ordinal := state.NextDeduplicationOrdinal
	if ordinal == 0 {
		ordinal = 1
	}
	state.NextDeduplicationOrdinal = ordinal + 1
	raw.State = RawFindingDeduplicationPending
	raw.Disposition = RawFindingDispositionUndecided
	raw.Failure = nil
	raw.Version++
	raw.UpdatedAt = now
	raw.History = appendRawFindingHistory(raw.History, RawFindingHistoryEntry{
		State: RawFindingDeduplicationPending, Disposition: RawFindingDispositionUndecided, At: now,
	})
	job.State = DeduplicationJobPending
	job.InsertionOrdinal = ordinal
	job.LeaseID = ""
	job.LeaseExpiresAt = time.Time{}
	job.Attempts = 0
	job.CandidateUniverseDigest = ""
	job.CandidateVersions = nil
	job.ShortlistedScores = nil
	job.Decision = DeduplicationJudgment{}
	job.Failure = nil
	job.UpdatedAt = now
	job.History = appendDeduplicationJobHistory(job.History, DeduplicationJobHistoryEntry{
		State: DeduplicationJobPending, At: now,
	})
	return *raw, nil
}

func deduplicationRetryFailure(
	sourceID string,
	code string,
	message string,
) DeduplicationRetryFailure {
	return DeduplicationRetryFailure{SourceID: sourceID, Code: code, Message: message}
}

func deduplicationJobIndexByRawID(jobs []DeduplicationJob, rawID string) int {
	for index := range jobs {
		if jobs[index].RawFindingID == rawID {
			return index
		}
	}
	return -1
}

func deduplicationJobIndexByID(jobs []DeduplicationJob, id string) int {
	for index := range jobs {
		if jobs[index].ID == id {
			return index
		}
	}
	return -1
}

func deduplicatedFindingIndexByID(findings []DeduplicatedReviewFinding, id string) int {
	for index := range findings {
		if findings[index].ID == id {
			return index
		}
	}
	return -1
}

func appendRawFindingHistory(
	history []RawFindingHistoryEntry, entry RawFindingHistoryEntry,
) []RawFindingHistoryEntry {
	if len(history) >= DeduplicationHistoryLimit {
		history = append([]RawFindingHistoryEntry{}, history[len(history)-DeduplicationHistoryLimit+1:]...)
	}
	return append(history, entry)
}

func appendDeduplicationJobHistory(
	history []DeduplicationJobHistoryEntry, entry DeduplicationJobHistoryEntry,
) []DeduplicationJobHistoryEntry {
	if len(history) >= DeduplicationHistoryLimit {
		history = append([]DeduplicationJobHistoryEntry{}, history[len(history)-DeduplicationHistoryLimit+1:]...)
	}
	return append(history, entry)
}

func appendDeduplicatedFindingHistory(
	history []DeduplicatedFindingHistoryEntry, entry DeduplicatedFindingHistoryEntry,
) []DeduplicatedFindingHistoryEntry {
	if len(history) >= DeduplicationHistoryLimit {
		history = append([]DeduplicatedFindingHistoryEntry{}, history[len(history)-DeduplicationHistoryLimit+1:]...)
	}
	return append(history, entry)
}

// ReconcileDeduplicationJobs is the startup recovery boundary for leases held
// by a prior controller process. It performs no model calls.
func (s Store) ReconcileDeduplicationJobs(ctx context.Context) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	states, err := s.List()
	if err != nil {
		return 0, err
	}
	reset := 0
	for _, listed := range states {
		if err := ctx.Err(); err != nil {
			return reset, err
		}
		count, reconcileErr := s.reconcileRepositoryDeduplicationJobs(listed.Repository)
		if reconcileErr != nil {
			return reset, reconcileErr
		}
		reset += count
	}
	return reset, nil
}

func (s Store) reconcileRepositoryDeduplicationJobs(repository string) (int, error) {
	unlock, err := s.lock(repository)
	if err != nil {
		return 0, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return 0, err
	}
	now := s.clock()
	reset := 0
	for index := range state.DeduplicationJobs {
		job := &state.DeduplicationJobs[index]
		if job.State != DeduplicationJobRunning {
			continue
		}
		rawIndex := rawFindingIndexByID(state.RawFindings, job.RawFindingID)
		if rawIndex < 0 {
			return 0, errors.New("running deduplication job raw finding is missing")
		}
		raw := &state.RawFindings[rawIndex]
		if job.Attempts >= DeduplicationAttemptLimit {
			markDeduplicationFailed(raw, job, "processing_interrupted", now)
			reset++
			continue
		}
		failure := safeDeduplicationFailure("processing_interrupted", true, now)
		job.State = DeduplicationJobPending
		job.LeaseID = ""
		job.LeaseExpiresAt = time.Time{}
		job.CandidateUniverseDigest = ""
		job.CandidateVersions = nil
		job.ShortlistedScores = nil
		job.Decision = DeduplicationJudgment{}
		job.Failure = nil
		job.UpdatedAt = now
		job.History = appendDeduplicationJobHistory(job.History, DeduplicationJobHistoryEntry{
			State: DeduplicationJobPending, Attempt: job.Attempts, Failure: failure, At: now,
		})
		raw.State = RawFindingDeduplicationPending
		raw.Failure = nil
		raw.Version++
		raw.UpdatedAt = now
		raw.History = appendRawFindingHistory(raw.History, RawFindingHistoryEntry{
			State: RawFindingDeduplicationPending, Disposition: RawFindingDispositionUndecided,
			Attempt: job.Attempts, Failure: failure, At: now,
		})
		reset++
	}
	if reset == 0 {
		return 0, nil
	}
	state.Version++
	state.UpdatedAt = now
	reconcileFindingsProcessingCounters(&state)
	state.FindingsProcessing.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return 0, err
	}
	return reset, nil
}
