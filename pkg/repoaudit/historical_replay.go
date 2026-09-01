package repoaudit

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"
)

const (
	maxHistoricalDeduplicationMergeGroups = 100_000
	historicalReplayAssignmentID          = "historical-replay"
)

var (
	// ErrHistoricalDeduplicationInProgress is returned by merge-sensitive
	// repository-finding mutations while the narrow historical merge lease is
	// active. Reads, reviews, and campaign finding ingestion do not use this
	// fence.
	ErrHistoricalDeduplicationInProgress   = errors.New("historical deduplication in progress")
	ErrHistoricalDeduplicationNotQuiescent = errors.New(
		"historical deduplication is waiting for downstream work to quiesce",
	)
	ErrHistoricalDeduplicationRestartRequired = errors.New(
		"historical deduplication incompatible work restart required",
	)
)

type HistoricalDeduplicationReplayStatus string

const (
	HistoricalDeduplicationPending   HistoricalDeduplicationReplayStatus = "pending"
	HistoricalDeduplicationReplaying HistoricalDeduplicationReplayStatus = "replaying"
	HistoricalDeduplicationMerging   HistoricalDeduplicationReplayStatus = "merging"
	HistoricalDeduplicationFailed    HistoricalDeduplicationReplayStatus = "failed"
	HistoricalDeduplicationCompleted HistoricalDeduplicationReplayStatus = "completed"
)

// HistoricalDeduplicationFailurePhase identifies the durable boundary at
// which a replay stopped. Older ledgers omit the field; callers must use
// HistoricalDeduplicationFailurePhaseForState to obtain the inferred value.
type HistoricalDeduplicationFailurePhase string

const (
	HistoricalDeduplicationFailureSetup      HistoricalDeduplicationFailurePhase = "setup"
	HistoricalDeduplicationFailureProcessing HistoricalDeduplicationFailurePhase = "processing"
	HistoricalDeduplicationFailureMerge      HistoricalDeduplicationFailurePhase = "merge"
)

// HistoricalDeduplicationProfileSnapshot freezes the same assigned profile
// policy and account/model revision as a live campaign. Checkpoint resume
// deliberately preserves it; adopting a different snapshot requires an
// explicit incompatible-work restart.
type HistoricalDeduplicationProfileSnapshot = RepositoryReviewDeduplicationSnapshot

type HistoricalDeduplicationFindingVersion struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

// HistoricalDeduplicationMergeGroup is one model-confirmed identity group.
// Members are stored in ID order, but completion always retains the earliest
// CreatedAt (then ID) repository-finding identity.
type HistoricalDeduplicationMergeGroup struct {
	Members []HistoricalDeduplicationFindingVersion `json:"members"`
}

type HistoricalDeduplicationMergeLease struct {
	ID         string                              `json:"id"`
	Groups     []HistoricalDeduplicationMergeGroup `json:"groups"`
	AcquiredAt time.Time                           `json:"acquired_at"`
}

// HistoricalDeduplicationMergeLeaseID derives an opaque idempotency key for
// one freshly snapshotted merge universe.
func HistoricalDeduplicationMergeLeaseID(
	repository string,
	stateVersion int64,
	groupDigest string,
) string {
	return stableID(
		"rhl_", strings.TrimSpace(repository), fmt.Sprint(stateVersion), groupDigest,
	)
}

// HistoricalDeduplicationReplay is the compact durable replay marker. Model
// output remains in the ordinary raw and deduplicated checkpoint ledgers; the
// marker retains only the frozen profile identity, failure phase, and exact
// merge targets held while the narrow fence is active.
type HistoricalDeduplicationReplay struct {
	Required        bool                                   `json:"required"`
	Status          HistoricalDeduplicationReplayStatus    `json:"status,omitempty"`
	ProfileSnapshot HistoricalDeduplicationProfileSnapshot `json:"profile_snapshot,omitempty"`
	SnapshotVersion int64                                  `json:"snapshot_version,omitempty"`
	Attempts        int                                    `json:"attempts,omitempty"`
	Error           string                                 `json:"error,omitempty"`
	FailurePhase    HistoricalDeduplicationFailurePhase    `json:"failure_phase,omitempty"`
	MergeLease      HistoricalDeduplicationMergeLease      `json:"merge_lease,omitempty"`
	UpdatedAt       time.Time                              `json:"updated_at,omitempty"`
}

// HistoricalDeduplicationFailurePhaseForState returns the recorded phase or
// infers it for phase-less ledgers written before checkpoint resume existed.
// A terminal historical raw is processing evidence; a fully completed raw
// ledger is merge evidence; every other shape stopped during setup.
func HistoricalDeduplicationFailurePhaseForState(
	state RepositoryState,
) HistoricalDeduplicationFailurePhase {
	if phase := state.HistoricalDeduplication.FailurePhase; phase != "" {
		return phase
	}
	return inferHistoricalDeduplicationFailurePhase(state)
}

func inferHistoricalDeduplicationFailurePhase(
	state RepositoryState,
) HistoricalDeduplicationFailurePhase {
	phase, _ := historicalDeduplicationFailureEvidence(state)
	return phase
}

func historicalDeduplicationFailureEvidence(
	state RepositoryState,
) (HistoricalDeduplicationFailurePhase, bool) {
	historical := 0
	allCompleted := true
	for _, raw := range state.RawFindings {
		if !HistoricalDeduplicationRawFinding(raw) {
			continue
		}
		historical++
		if raw.State == RawFindingDeduplicationFailed {
			return HistoricalDeduplicationFailureProcessing, true
		}
		allCompleted = allCompleted && raw.State == RawFindingDeduplicationCompleted
	}
	if historical > 0 && allCompleted {
		return HistoricalDeduplicationFailureMerge, true
	}
	if historical > 0 || len(HistoricalDeduplicationReplayBatches(state)) > 0 {
		return HistoricalDeduplicationFailureSetup, true
	}
	// With neither a source nor a legacy batch, raw evidence cannot
	// distinguish an empty merge from setup. Setup is the conservative
	// inference for phase-less ledgers; an explicitly recorded merge remains
	// valid evidence from the state transition that held the merge lease.
	return HistoricalDeduplicationFailureSetup, false
}

// historicalDeduplicationResumePhase reconciles a recorded transition phase
// with checkpoints that may have advanced while the replay was failed. A raw
// terminal failure always needs processing resume, a nonempty fully completed
// raw ledger needs merge resume, and only an empty recorded merge relies on
// the persisted transition evidence.
func historicalDeduplicationResumePhase(
	state RepositoryState,
) HistoricalDeduplicationFailurePhase {
	historical := 0
	allCompleted := true
	for _, raw := range state.RawFindings {
		if !HistoricalDeduplicationRawFinding(raw) {
			continue
		}
		historical++
		if raw.State == RawFindingDeduplicationFailed {
			return HistoricalDeduplicationFailureProcessing
		}
		allCompleted = allCompleted && raw.State == RawFindingDeduplicationCompleted
	}
	if historical > 0 && allCompleted {
		return HistoricalDeduplicationFailureMerge
	}
	if historical == 0 &&
		state.HistoricalDeduplication.FailurePhase == HistoricalDeduplicationFailureMerge {
		return HistoricalDeduplicationFailureMerge
	}
	return HistoricalDeduplicationFailureSetup
}

// HistoricalDeduplicationReplayBatch is a deterministic projection of legacy
// findings. Campaign identity is preferred; campaign-less findings use their
// workflow/run batch as the replay boundary.
type HistoricalDeduplicationReplayBatch struct {
	BoundaryID string    `json:"boundary_id"`
	CampaignID string    `json:"campaign_id,omitempty"`
	FindingIDs []string  `json:"finding_ids"`
	CreatedAt  time.Time `json:"created_at"`
}

type HistoricalDeduplicationAdmission struct {
	Batch       HistoricalDeduplicationReplayBatch `json:"batch"`
	RawFindings []RawReviewFinding                 `json:"raw_findings"`
	Admitted    int                                `json:"admitted"`
	Complete    bool                               `json:"complete"`
	AllComplete bool                               `json:"all_complete"`
}

// HistoricalDeduplicationDependency is the exact processing identity for one
// legacy source. A complete dependency plan includes admitted and not-yet-
// admitted sources so compatibility checks cannot silently change a later
// checkpoint.
type HistoricalDeduplicationDependency struct {
	LegacyFindingID string `json:"legacy_finding_id"`
	RawFindingID    string `json:"raw_finding_id"`
	CampaignID      string `json:"campaign_id"`
	AdmissionBucket string `json:"admission_bucket"`
}

type HistoricalDeduplicationRestartRequest struct {
	ProfileSnapshot HistoricalDeduplicationProfileSnapshot `json:"profile_snapshot"`
	Dependencies    []HistoricalDeduplicationDependency    `json:"dependencies"`
}

type HistoricalDeduplicationQuiescence struct {
	IssueGenerations int `json:"issue_generations"`
	Publications     int `json:"publications"`
	Mappings         int `json:"mappings"`
	Validations      int `json:"validations"`
}

// HistoricalDeduplicationRawFinding reports whether raw was admitted by the
// historical replay adapter. AssignmentID is the durable discriminator. The
// prefix-only fallback accepts records written by early development builds
// and deliberately excludes native findings, whose assignment identity is
// always nonempty.
func HistoricalDeduplicationRawFinding(raw RawReviewFinding) bool {
	if strings.TrimSpace(raw.LegacyFindingID) == "" {
		return false
	}
	if raw.AssignmentID == historicalReplayAssignmentID {
		return true
	}
	return raw.AssignmentID == "" && strings.HasPrefix(raw.ID, "rrw_")
}

func (q HistoricalDeduplicationQuiescence) Ready() bool {
	return q.IssueGenerations == 0 && q.Publications == 0 && q.Mappings == 0 &&
		q.Validations == 0
}

// HistoricalDeduplicationReplayBatches orders campaigns oldest-first, then
// findings by creation time and ID. Missing legacy campaign metadata is not
// guessed across workflow batches.
func HistoricalDeduplicationReplayBatches(
	state RepositoryState,
) []HistoricalDeduplicationReplayBatch {
	contexts := make(map[string]FindingContext, len(state.Contexts))
	for _, contextRecord := range state.Contexts {
		contexts[contextRecord.ID] = contextRecord
	}
	runForFinding := make(map[string]string)
	runs := append([]ReviewRun(nil), state.Runs...)
	sort.SliceStable(runs, func(left, right int) bool {
		if runs[left].CompletedAt.Equal(runs[right].CompletedAt) {
			return runs[left].ID < runs[right].ID
		}
		return runs[left].CompletedAt.Before(runs[right].CompletedAt)
	})
	for _, run := range runs {
		for _, findingID := range run.FindingIDs {
			if _, found := runForFinding[findingID]; !found {
				runForFinding[findingID] = run.ID
			}
		}
	}
	type batchBuilder struct {
		campaignID string
		findings   []Finding
	}
	builders := make(map[string]*batchBuilder)
	projectionIDs := make(map[string]struct{}, len(state.DeduplicatedFindings))
	for _, finding := range state.DeduplicatedFindings {
		projectionIDs[finding.ID] = struct{}{}
	}
	for _, finding := range state.Findings {
		if finding.DeduplicationPending {
			continue
		}
		if _, projection := projectionIDs[finding.ID]; projection {
			continue
		}
		boundaryID := strings.TrimSpace(finding.CampaignID)
		campaignID := boundaryID
		if boundaryID == "" {
			for _, contextID := range finding.ContextIDs {
				if runID := strings.TrimSpace(contexts[contextID].RunID); runID != "" {
					boundaryID = runID
					break
				}
			}
		}
		if boundaryID == "" {
			boundaryID = strings.TrimSpace(runForFinding[finding.ID])
		}
		if boundaryID == "" {
			// Corrupt or very old ledgers must not collapse unrelated records
			// into one guessed campaign.
			boundaryID = "legacy:" + finding.ID
		}
		builder := builders[boundaryID]
		if builder == nil {
			builder = &batchBuilder{campaignID: campaignID}
			builders[boundaryID] = builder
		}
		builder.findings = append(builder.findings, finding)
	}
	result := make([]HistoricalDeduplicationReplayBatch, 0, len(builders))
	for boundaryID, builder := range builders {
		sort.SliceStable(builder.findings, func(left, right int) bool {
			if builder.findings[left].CreatedAt.Equal(builder.findings[right].CreatedAt) {
				return builder.findings[left].ID < builder.findings[right].ID
			}
			return builder.findings[left].CreatedAt.Before(builder.findings[right].CreatedAt)
		})
		batch := HistoricalDeduplicationReplayBatch{
			BoundaryID: boundaryID, CampaignID: builder.campaignID,
			FindingIDs: make([]string, 0, len(builder.findings)),
		}
		for _, finding := range builder.findings {
			batch.FindingIDs = append(batch.FindingIDs, finding.ID)
			if batch.CreatedAt.IsZero() || finding.CreatedAt.Before(batch.CreatedAt) {
				batch.CreatedAt = finding.CreatedAt
			}
		}
		result = append(result, batch)
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].CreatedAt.Equal(result[right].CreatedAt) {
			return result[left].BoundaryID < result[right].BoundaryID
		}
		return result[left].CreatedAt.Before(result[right].CreatedAt)
	})
	return result
}

// HistoricalDeduplicationDependencies projects the exact campaign and bucket
// identity for every historical source. recoveredFindingIDs may override a
// proven subset with recoveredCampaignID; all other sources use the campaign
// currently proven by their legacy batch. The admitted raw ID is retained,
// while compatibility comparison against its durable campaign happens under
// the Store lock.
func HistoricalDeduplicationDependencies(
	state RepositoryState,
	recoveredCampaignID string,
	recoveredFindingIDs []string,
) ([]HistoricalDeduplicationDependency, error) {
	recoveredCampaignID = strings.TrimSpace(recoveredCampaignID)
	if (recoveredCampaignID == "") != (len(recoveredFindingIDs) == 0) ||
		(recoveredCampaignID != "" && !ValidRepositoryReviewCampaignID(recoveredCampaignID)) {
		return nil, errors.New("invalid historical deduplication dependency recovery")
	}
	recovered := make(map[string]struct{}, len(recoveredFindingIDs))
	for _, findingID := range recoveredFindingIDs {
		findingID = strings.TrimSpace(findingID)
		if !validBoundedText(findingID, 256) {
			return nil, errors.New("invalid historical deduplication dependency finding")
		}
		if _, duplicate := recovered[findingID]; duplicate {
			return nil, errors.New("duplicate historical deduplication dependency finding")
		}
		recovered[findingID] = struct{}{}
	}
	findingsByID := make(map[string]Finding, len(state.Findings))
	for _, finding := range state.Findings {
		findingsByID[finding.ID] = finding
	}
	rawByLegacyID := make(map[string]RawReviewFinding, len(state.RawFindings))
	for _, raw := range state.RawFindings {
		if HistoricalDeduplicationRawFinding(raw) {
			rawByLegacyID[raw.LegacyFindingID] = raw
		}
	}
	dependencies := make([]HistoricalDeduplicationDependency, 0)
	seen := make(map[string]struct{})
	for _, batch := range HistoricalDeduplicationReplayBatches(state) {
		for _, findingID := range batch.FindingIDs {
			finding, found := findingsByID[findingID]
			if !found {
				return nil, ErrConflict
			}
			if _, duplicate := seen[findingID]; duplicate {
				return nil, ErrConflict
			}
			seen[findingID] = struct{}{}
			campaignID := historicalDeduplicationCampaignID(
				state.Repository, finding.CommitSHA, batch,
			)
			rawID := stableID("rrw_", state.Repository, "historical", findingID)
			if raw, admitted := rawByLegacyID[findingID]; admitted {
				rawID = raw.ID
			}
			if _, selected := recovered[findingID]; selected {
				campaignID = recoveredCampaignID
				delete(recovered, findingID)
			}
			bucket, err := DeduplicationAdmissionBucket(campaignID, finding.File, finding.Symbol)
			if err != nil {
				return nil, err
			}
			dependencies = append(dependencies, HistoricalDeduplicationDependency{
				LegacyFindingID: findingID,
				RawFindingID:    rawID,
				CampaignID:      campaignID,
				AdmissionBucket: bucket,
			})
		}
	}
	if len(recovered) != 0 {
		return nil, ErrConflict
	}
	return dependencies, nil
}

func normalizeHistoricalDeduplicationDependencies(
	state RepositoryState,
	dependencies []HistoricalDeduplicationDependency,
) ([]HistoricalDeduplicationDependency, error) {
	current, err := HistoricalDeduplicationDependencies(state, "", nil)
	if err != nil {
		return nil, err
	}
	if len(dependencies) != len(current) {
		return nil, errors.New("incomplete historical deduplication dependency plan")
	}
	wanted := make(map[string]HistoricalDeduplicationDependency, len(current))
	for _, dependency := range current {
		wanted[dependency.LegacyFindingID] = dependency
	}
	admitted := make(map[string]struct{})
	for _, raw := range state.RawFindings {
		if HistoricalDeduplicationRawFinding(raw) {
			admitted[raw.LegacyFindingID] = struct{}{}
		}
	}
	result := make([]HistoricalDeduplicationDependency, 0, len(dependencies))
	seen := make(map[string]struct{}, len(dependencies))
	for _, dependency := range dependencies {
		dependency.LegacyFindingID = strings.TrimSpace(dependency.LegacyFindingID)
		dependency.RawFindingID = strings.TrimSpace(dependency.RawFindingID)
		dependency.CampaignID = strings.TrimSpace(dependency.CampaignID)
		dependency.AdmissionBucket = strings.TrimSpace(dependency.AdmissionBucket)
		baseline, found := wanted[dependency.LegacyFindingID]
		if !found || dependency.RawFindingID != baseline.RawFindingID ||
			!ValidRepositoryReviewCampaignID(dependency.CampaignID) ||
			!validBoundedText(dependency.AdmissionBucket, 256) {
			return nil, errors.New("invalid historical deduplication dependency plan")
		}
		findingIndex := findingIndexByID(state.Findings, dependency.LegacyFindingID)
		if findingIndex < 0 {
			return nil, ErrConflict
		}
		bucket, bucketErr := DeduplicationAdmissionBucket(
			dependency.CampaignID,
			state.Findings[findingIndex].File,
			state.Findings[findingIndex].Symbol,
		)
		if bucketErr != nil || bucket != dependency.AdmissionBucket {
			return nil, errors.New("invalid historical deduplication dependency bucket")
		}
		if _, exists := admitted[dependency.LegacyFindingID]; !exists &&
			(dependency.CampaignID != baseline.CampaignID ||
				dependency.AdmissionBucket != baseline.AdmissionBucket) {
			return nil, ErrHistoricalDeduplicationRestartRequired
		}
		if _, duplicate := seen[dependency.LegacyFindingID]; duplicate {
			return nil, errors.New("duplicate historical deduplication dependency")
		}
		seen[dependency.LegacyFindingID] = struct{}{}
		result = append(result, dependency)
	}
	sort.SliceStable(result, func(left, right int) bool {
		return result[left].LegacyFindingID < result[right].LegacyFindingID
	})
	return result, nil
}

// AdmitNextHistoricalDeduplicationBatch atomically inserts raw findings and
// jobs for only the oldest incomplete historical boundary. It performs no
// model calls and does not acquire the narrow merge lease.
func (s Store) AdmitNextHistoricalDeduplicationBatch(
	repository string,
) (RepositoryState, HistoricalDeduplicationAdmission, error) {
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationAdmission{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationAdmission{}, err
	}
	replay := &state.HistoricalDeduplication
	if !replay.Required || replay.Status != HistoricalDeduplicationReplaying {
		return RepositoryState{}, HistoricalDeduplicationAdmission{}, ErrConflict
	}
	if err := validateHistoricalDeduplicationProfileSnapshot(replay.ProfileSnapshot); err != nil {
		return RepositoryState{}, HistoricalDeduplicationAdmission{}, err
	}
	batches := HistoricalDeduplicationReplayBatches(state)
	rawByLegacyID := make(map[string]int, len(state.RawFindings))
	for index, raw := range state.RawFindings {
		if raw.LegacyFindingID != "" {
			rawByLegacyID[raw.LegacyFindingID] = index
		}
	}
	findingsByID := make(map[string]Finding, len(state.Findings))
	for _, finding := range state.Findings {
		findingsByID[finding.ID] = finding
	}
	contextsByID := make(map[string]FindingContext, len(state.Contexts))
	for _, contextRecord := range state.Contexts {
		contextsByID[contextRecord.ID] = contextRecord
	}
	for _, batch := range batches {
		admission := HistoricalDeduplicationAdmission{Batch: batch}
		batchComplete := true
		for _, findingID := range batch.FindingIDs {
			if rawIndex, exists := rawByLegacyID[findingID]; exists {
				raw := state.RawFindings[rawIndex]
				admission.RawFindings = append(admission.RawFindings, raw)
				if raw.State != RawFindingDeduplicationCompleted {
					batchComplete = false
				}
				continue
			}
			batchComplete = false
		}
		if batchComplete {
			continue
		}
		now := s.clock()
		for _, findingID := range batch.FindingIDs {
			if _, exists := rawByLegacyID[findingID]; exists {
				continue
			}
			finding, exists := findingsByID[findingID]
			if !exists {
				return RepositoryState{}, HistoricalDeduplicationAdmission{}, ErrConflict
			}
			raw, job, admissionErr := historicalRawFindingAndJob(
				state, finding, batch, contextsByID, replay.ProfileSnapshot,
				state.NextDeduplicationOrdinal, now,
			)
			if admissionErr != nil {
				return RepositoryState{}, HistoricalDeduplicationAdmission{}, admissionErr
			}
			if _, exists := contextsByID[raw.ContextID]; !exists {
				contextRunID := raw.RunID
				if !ValidRepositoryReviewCampaignID(strings.TrimSpace(batch.CampaignID)) {
					// A replay-only context must not temporarily claim the real run.
					// Exact recovery can tag that run before retry rebinds this clone.
					contextRunID = stableID("historical-run_", raw.RunID, raw.LegacyFindingID)
				}
				contextRecord := FindingContext{
					ID: raw.ContextID, Repository: raw.Repository, CommitSHA: raw.CommitSHA,
					InventoryHash: "historical-replay", ProfileHash: "historical-replay",
					RunID: contextRunID, Model: raw.Model, ModelAlias: raw.ModelAlias,
					Account: raw.Account, Reviewer: raw.Reviewer,
					Files: []FileRef{raw.File}, CreatedAt: raw.CreatedAt,
				}
				state.Contexts = append(state.Contexts, contextRecord)
				contextsByID[contextRecord.ID] = contextRecord
			}
			if bindErr := bindHistoricalDeduplicationCampaign(
				&state, finding.ID, raw.ContextID, raw.CampaignID, raw.CommitSHA,
			); bindErr != nil {
				return RepositoryState{}, HistoricalDeduplicationAdmission{}, bindErr
			}
			if state.NextDeduplicationOrdinal == 0 {
				state.NextDeduplicationOrdinal = 1
				raw.InsertionOrdinal = 1
				job.InsertionOrdinal = 1
			}
			state.NextDeduplicationOrdinal = raw.InsertionOrdinal + 1
			state.RawFindings = append(state.RawFindings, raw)
			state.DeduplicationJobs = append(state.DeduplicationJobs, job)
			rawByLegacyID[findingID] = len(state.RawFindings) - 1
			admission.RawFindings = append(admission.RawFindings, raw)
			admission.Admitted++
		}
		sort.SliceStable(admission.RawFindings, func(left, right int) bool {
			if admission.RawFindings[left].InsertionOrdinal != admission.RawFindings[right].InsertionOrdinal {
				return admission.RawFindings[left].InsertionOrdinal < admission.RawFindings[right].InsertionOrdinal
			}
			return admission.RawFindings[left].ID < admission.RawFindings[right].ID
		})
		if admission.Admitted > 0 {
			replay.UpdatedAt = now
			state.Version++
			state.UpdatedAt = now
			reconcileFindingsProcessingCounters(&state)
			state.FindingsProcessing.UpdatedAt = now
			if err := s.save(&state); err != nil {
				return RepositoryState{}, HistoricalDeduplicationAdmission{}, err
			}
		}
		return state, admission, nil
	}
	return state, HistoricalDeduplicationAdmission{Complete: true, AllComplete: true}, nil
}

func historicalRawFindingAndJob(
	state RepositoryState,
	finding Finding,
	batch HistoricalDeduplicationReplayBatch,
	contexts map[string]FindingContext,
	snapshot RepositoryReviewDeduplicationSnapshot,
	ordinal uint64,
	now time.Time,
) (RawReviewFinding, DeduplicationJob, error) {
	if ordinal == 0 {
		ordinal = 1
	}
	campaignID := historicalDeduplicationCampaignID(
		state.Repository, finding.CommitSHA, batch,
	)
	bucket, err := DeduplicationAdmissionBucket(campaignID, finding.File, finding.Symbol)
	if err != nil {
		return RawReviewFinding{}, DeduplicationJob{}, err
	}
	contextID := ""
	runID := ""
	reviewer := ""
	model := ""
	modelAlias := ""
	account := ""
	for _, id := range finding.ContextIDs {
		contextRecord, exists := contexts[id]
		if !exists {
			continue
		}
		contextID = contextRecord.ID
		runID = strings.TrimSpace(contextRecord.RunID)
		model = strings.TrimSpace(contextRecord.Model)
		candidateAlias := strings.TrimSpace(contextRecord.ModelAlias)
		candidateAccount := strings.TrimSpace(contextRecord.Account)
		if candidateAlias != "" && candidateAccount != "" {
			modelAlias = candidateAlias
			account = candidateAccount
		}
		reviewer = strings.TrimSpace(contextRecord.Reviewer)
		break
	}
	if !ValidRepositoryReviewCampaignID(strings.TrimSpace(batch.CampaignID)) {
		// Keep unproven legacy contexts untouched so a later exact campaign
		// recovery can still validate their original stable identities. The
		// replay-only clone carries synthetic processing authority instead.
		contextID = stableID("legacy-context_", state.Repository, finding.ID)
	} else if contextID == "" {
		contextID = stableID("legacy-context_", state.Repository, finding.ID)
	}
	if runID == "" {
		for _, run := range state.Runs {
			if containsExactString(run.FindingIDs, finding.ID) {
				runID = strings.TrimSpace(run.ID)
				break
			}
		}
	}
	if runID == "" {
		runID = batch.BoundaryID
	}
	if model == "" && len(finding.Models) > 0 {
		model = strings.TrimSpace(finding.Models[0])
	}
	if model == "" {
		model = snapshot.ReviewerModel
	}
	if reviewer == "" {
		reviewer = model
	}
	raw := RawReviewFinding{
		ID: stableID("rrw_", state.Repository, "historical", finding.ID), Version: 1,
		CampaignID: campaignID, AdmissionBucket: bucket, InsertionOrdinal: ordinal,
		LegacyFindingID: finding.ID, Repository: finding.Repository,
		CommitSHA: finding.CommitSHA, File: finding.File, Line: finding.Line,
		Severity: finding.Severity, Title: finding.Title, Symbol: finding.Symbol,
		Message: finding.Message, Evidence: finding.Evidence, Impact: finding.Impact,
		Validation: finding.Validation, MatchHints: finding.MatchHints, FixEffort: finding.FixEffort,
		ContextID: contextID, RunID: runID, AssignmentID: historicalReplayAssignmentID,
		Model: model, ModelAlias: modelAlias, Account: account, Reviewer: reviewer,
		State:       RawFindingDeduplicationPending,
		Disposition: RawFindingDispositionUndecided, CreatedAt: finding.CreatedAt, UpdatedAt: now,
	}
	if raw.CreatedAt.IsZero() {
		raw.CreatedAt = now
	}
	if raw.UpdatedAt.Before(raw.CreatedAt) {
		raw.UpdatedAt = raw.CreatedAt
	}
	raw.DiagnosisDigest = RawReviewFindingDiagnosisDigest(raw)
	raw.History = []RawFindingHistoryEntry{{
		State: raw.State, Disposition: raw.Disposition, At: raw.UpdatedAt,
	}}
	job := DeduplicationJob{
		ID: stableID("rdj_", raw.ID), RawFindingID: raw.ID,
		State: DeduplicationJobPending, AdmissionBucket: bucket,
		InsertionOrdinal: ordinal, ModelSnapshot: snapshot,
		History:   []DeduplicationJobHistoryEntry{{State: DeduplicationJobPending, At: now}},
		CreatedAt: now, UpdatedAt: now,
	}
	return raw, job, nil
}

func historicalDeduplicationCampaignID(
	repository string,
	commitSHA string,
	batch HistoricalDeduplicationReplayBatch,
) string {
	if campaignID := strings.TrimSpace(batch.CampaignID); ValidRepositoryReviewCampaignID(campaignID) {
		return campaignID
	}
	return stableID(
		"rrc_",
		strings.TrimSpace(repository),
		strings.ToLower(strings.TrimSpace(commitSHA)),
		"historical-replay-boundary",
		historicalDeduplicationBoundaryID(batch.BoundaryID),
	)
}

func historicalDeduplicationBoundaryID(value string) string {
	value = strings.TrimSpace(value)
	if value != "" && len(value) <= 256 && !strings.ContainsRune(value, 0) {
		return value
	}
	return stableID("rrb_", value)
}

// bindHistoricalDeduplicationCampaign installs only the minimum processing
// authority required for a replay-derived deduplicated projection to pass the
// ordinary campaign invariants. Exact recovery has already tagged proven
// records. Unproven records receive an isolated synthetic campaign without
// claiming that their workflow run belongs to a wider campaign.
func bindHistoricalDeduplicationCampaign(
	state *RepositoryState,
	legacyFindingID string,
	rawContextID string,
	campaignID string,
	commitSHA string,
) error {
	if state == nil || !ValidRepositoryReviewCampaignID(campaignID) ||
		!validRepositoryReviewCommitSHA(commitSHA) {
		return ErrInvalidPlan
	}
	if state.CampaignHistory == nil {
		state.CampaignHistory = make(map[string]string)
	}
	if existing := state.CampaignHistory[campaignID]; existing != "" && existing != commitSHA {
		return ErrConflict
	}
	state.CampaignHistory[campaignID] = commitSHA

	contextIndexes := make(map[string]int, len(state.Contexts))
	for index := range state.Contexts {
		contextRecord := state.Contexts[index]
		if contextRecord.ID != "" {
			contextIndexes[contextRecord.ID] = index
		}
	}
	tagContext := func(contextID string, replayContext bool) error {
		index, found := contextIndexes[contextID]
		if !found {
			return ErrConflict
		}
		contextRecord := &state.Contexts[index]
		if contextRecord.CommitSHA != commitSHA {
			return ErrConflict
		}
		if contextRecord.CampaignID != "" && contextRecord.CampaignID != campaignID &&
			(!replayContext || contextRecord.InventoryHash != "historical-replay" ||
				contextRecord.ProfileHash != "historical-replay") {
			return ErrConflict
		}
		contextRecord.CampaignID = campaignID
		return nil
	}
	if err := tagContext(rawContextID, true); err != nil {
		return err
	}

	findingIndex := findingIndexByID(state.Findings, legacyFindingID)
	if findingIndex < 0 {
		return ErrConflict
	}
	finding := &state.Findings[findingIndex]
	if finding.CommitSHA != commitSHA ||
		(finding.CampaignID != "" && finding.CampaignID != campaignID) {
		return ErrConflict
	}
	if len(finding.ContextIDs) == 0 || finding.CampaignID == "" {
		// Very old context-free occurrences retain their original untagged
		// provenance. Unproven campaign-less occurrences do the same so exact
		// recovery can still validate their original identities later. The
		// replay-only raw context is sufficient for the new projection.
		return nil
	}
	for _, contextID := range finding.ContextIDs {
		if err := tagContext(contextID, false); err != nil {
			return err
		}
	}
	return nil
}

func HistoricalDeduplicationMergeInProgress(state RepositoryState) bool {
	replay := state.HistoricalDeduplication
	return replay.Required && replay.Status == HistoricalDeduplicationMerging &&
		strings.TrimSpace(replay.MergeLease.ID) != ""
}

func HistoricalDeduplicationMutationAllowed(state RepositoryState) error {
	if HistoricalDeduplicationMergeInProgress(state) {
		return ErrHistoricalDeduplicationInProgress
	}
	return nil
}

func HistoricalDeduplicationQuiescenceForState(
	state RepositoryState,
) HistoricalDeduplicationQuiescence {
	var result HistoricalDeduplicationQuiescence
	for _, draft := range state.IssueDrafts {
		switch draft.State {
		case IssueDraftGenerating:
			result.IssueGenerations++
		case IssueDraftPublishing, IssueDraftUnknown:
			result.Publications++
		}
	}
	for _, job := range state.MappingJobs {
		if job.State == RepositoryMappingRunning {
			result.Mappings++
		}
	}
	for _, job := range state.ValidationJobs {
		if job.State == RepositoryValidationPending || job.State == RepositoryValidationRunning {
			result.Validations++
		}
	}
	return result
}

// HistoricalDeduplicationRepositoryMergeGroups projects model-admitted raw
// source groups onto their pre-replay repository-finding identities. Shared
// identities are unioned so every historical record appears in at most one
// atomic merge group.
func HistoricalDeduplicationRepositoryMergeGroups(
	state RepositoryState,
) ([]HistoricalDeduplicationMergeGroup, error) {
	legacyFindings := make(map[string]Finding, len(state.Findings))
	for _, finding := range state.Findings {
		legacyFindings[finding.ID] = finding
	}
	rawByID := make(map[string]RawReviewFinding, len(state.RawFindings))
	historicalRaw := 0
	for _, raw := range state.RawFindings {
		rawByID[raw.ID] = raw
		if !HistoricalDeduplicationRawFinding(raw) {
			continue
		}
		historicalRaw++
		if raw.State != RawFindingDeduplicationCompleted || raw.DeduplicatedFindingID == "" {
			return nil, ErrHistoricalDeduplicationNotQuiescent
		}
	}
	if historicalRaw == 0 {
		return []HistoricalDeduplicationMergeGroup{}, nil
	}
	parent := make(map[string]string)
	var root func(string) string
	root = func(id string) string {
		if parent[id] == "" {
			parent[id] = id
			return id
		}
		if parent[id] != id {
			parent[id] = root(parent[id])
		}
		return parent[id]
	}
	union := func(left, right string) {
		left, right = root(left), root(right)
		if left == right {
			return
		}
		if right < left {
			left, right = right, left
		}
		parent[right] = left
	}
	for _, deduplicated := range state.DeduplicatedFindings {
		repositoryIDs := make([]string, 0, len(deduplicated.RawSourceIDs))
		seen := make(map[string]struct{})
		hasHistoricalSource := false
		for _, rawID := range deduplicated.RawSourceIDs {
			raw, exists := rawByID[rawID]
			if !exists || !HistoricalDeduplicationRawFinding(raw) {
				continue
			}
			hasHistoricalSource = true
			legacy, exists := legacyFindings[raw.LegacyFindingID]
			if !exists {
				return nil, ErrConflict
			}
			repositoryID := strings.TrimSpace(legacy.RepositoryFindingID)
			if repositoryID == "" {
				continue
			}
			if repositoryFindingIndexByID(state.RepositoryFindings, repositoryID) < 0 {
				return nil, ErrConflict
			}
			if _, duplicate := seen[repositoryID]; duplicate {
				continue
			}
			seen[repositoryID] = struct{}{}
			repositoryIDs = append(repositoryIDs, repositoryID)
			root(repositoryID)
		}
		if hasHistoricalSource && deduplicated.RepositoryFindingID != "" {
			repositoryID := deduplicated.RepositoryFindingID
			if repositoryFindingIndexByID(state.RepositoryFindings, repositoryID) < 0 {
				return nil, ErrConflict
			}
			if _, duplicate := seen[repositoryID]; !duplicate {
				seen[repositoryID] = struct{}{}
				repositoryIDs = append(repositoryIDs, repositoryID)
				root(repositoryID)
			}
		}
		for index := 1; index < len(repositoryIDs); index++ {
			union(repositoryIDs[0], repositoryIDs[index])
		}
	}
	components := make(map[string][]string)
	for id := range parent {
		component := root(id)
		components[component] = append(components[component], id)
	}
	groups := make([]HistoricalDeduplicationMergeGroup, 0, len(components))
	for _, ids := range components {
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		group := HistoricalDeduplicationMergeGroup{
			Members: make([]HistoricalDeduplicationFindingVersion, 0, len(ids)),
		}
		for _, id := range ids {
			index := repositoryFindingIndexByID(state.RepositoryFindings, id)
			group.Members = append(group.Members, HistoricalDeduplicationFindingVersion{
				ID: id, Version: state.RepositoryFindings[index].Version,
			})
		}
		groups = append(groups, group)
	}
	return normalizeHistoricalDeduplicationMergeGroups(groups)
}

func (s Store) FreezeHistoricalDeduplicationReplay(
	repository string,
	snapshot HistoricalDeduplicationProfileSnapshot,
) (RepositoryState, HistoricalDeduplicationReplay, error) {
	if err := validateHistoricalDeduplicationProfileSnapshot(snapshot); err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	replay := &state.HistoricalDeduplication
	if !replay.Required || replay.Status == HistoricalDeduplicationCompleted {
		return state, *replay, nil
	}
	if replay.Status == HistoricalDeduplicationMerging {
		return RepositoryState{}, HistoricalDeduplicationReplay{},
			ErrHistoricalDeduplicationInProgress
	}
	if replay.Status == HistoricalDeduplicationReplaying {
		if reflect.DeepEqual(replay.ProfileSnapshot, snapshot) {
			return state, *replay, nil
		}
		return RepositoryState{}, HistoricalDeduplicationReplay{}, ErrConflict
	}
	if replay.Status != HistoricalDeduplicationPending {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, ErrConflict
	}
	now := s.clock()
	if replay.Attempts > 0 {
		if !HistoricalDeduplicationQuiescenceForState(state).Ready() {
			return state, *replay, ErrHistoricalDeduplicationNotQuiescent
		}
		for _, raw := range state.RawFindings {
			if HistoricalDeduplicationRawFinding(raw) &&
				deduplicationJobIndexByRawID(state.DeduplicationJobs, raw.ID) < 0 {
				return RepositoryState{}, HistoricalDeduplicationReplay{}, ErrConflict
			}
		}
	}
	replay.ProfileSnapshot = snapshot
	replay.SnapshotVersion = state.Version
	replay.Status = HistoricalDeduplicationReplaying
	replay.Attempts++
	replay.Error = ""
	replay.FailurePhase = ""
	replay.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	return state, *replay, nil
}

func resetHistoricalDeduplicationModelWork(
	state *RepositoryState,
	snapshot RepositoryReviewDeduplicationSnapshot,
	now time.Time,
) error {
	if state == nil {
		return errors.New("repository review state is required")
	}
	selected := make(map[string]struct{})
	dependencies := make(map[string]HistoricalDeduplicationDependency)
	campaignByLegacyFinding := make(map[string]string)
	if state.Repository != "" {
		for _, batch := range HistoricalDeduplicationReplayBatches(*state) {
			for _, findingID := range batch.FindingIDs {
				findingIndex := findingIndexByID(state.Findings, findingID)
				if findingIndex < 0 {
					return ErrConflict
				}
				campaignByLegacyFinding[findingID] = historicalDeduplicationCampaignID(
					state.Repository, state.Findings[findingIndex].CommitSHA, batch,
				)
			}
		}
	}
	for _, raw := range state.RawFindings {
		if HistoricalDeduplicationRawFinding(raw) {
			selected[raw.ID] = struct{}{}
			campaignID := raw.CampaignID
			bucket := raw.AdmissionBucket
			if state.Repository != "" {
				campaignID = campaignByLegacyFinding[raw.LegacyFindingID]
				if campaignID == "" {
					if findingIndexByID(state.Findings, raw.LegacyFindingID) < 0 {
						return ErrConflict
					}
					campaignID = historicalDeduplicationCampaignID(
						state.Repository, raw.CommitSHA,
						HistoricalDeduplicationReplayBatch{BoundaryID: raw.RunID},
					)
				}
				var err error
				bucket, err = DeduplicationAdmissionBucket(campaignID, raw.File, raw.Symbol)
				if err != nil {
					return err
				}
			}
			dependencies[raw.ID] = HistoricalDeduplicationDependency{
				LegacyFindingID: raw.LegacyFindingID,
				RawFindingID:    raw.ID,
				CampaignID:      campaignID,
				AdmissionBucket: bucket,
			}
		}
	}
	return resetHistoricalDeduplicationModelWorkSelection(
		state, snapshot, selected, dependencies, now,
	)
}

func resetHistoricalDeduplicationModelWorkSelection(
	state *RepositoryState,
	snapshot RepositoryReviewDeduplicationSnapshot,
	historicalRawIDs map[string]struct{},
	dependenciesByRawID map[string]HistoricalDeduplicationDependency,
	now time.Time,
) error {
	if state == nil {
		return errors.New("repository review state is required")
	}
	rawIndexes := make(map[string]int, len(state.RawFindings))
	for index, raw := range state.RawFindings {
		rawIndexes[raw.ID] = index
		if _, selected := historicalRawIDs[raw.ID]; selected &&
			!HistoricalDeduplicationRawFinding(raw) {
			return ErrConflict
		}
	}
	jobsByRawID := make(map[string]*DeduplicationJob, len(state.DeduplicationJobs))
	for index := range state.DeduplicationJobs {
		job := &state.DeduplicationJobs[index]
		jobsByRawID[job.RawFindingID] = job
	}
	for rawID := range historicalRawIDs {
		rawIndex, found := rawIndexes[rawID]
		if !found {
			return ErrConflict
		}
		raw := &state.RawFindings[rawIndex]
		job := jobsByRawID[raw.ID]
		dependency, found := dependenciesByRawID[raw.ID]
		if job == nil || !found || dependency.LegacyFindingID != raw.LegacyFindingID ||
			dependency.RawFindingID != raw.ID {
			return ErrConflict
		}
		if raw.CampaignID != dependency.CampaignID && state.Repository != "" {
			if err := bindHistoricalDeduplicationCampaign(
				state, raw.LegacyFindingID, raw.ContextID,
				dependency.CampaignID, raw.CommitSHA,
			); err != nil {
				// Confirmed restart may move an admitted checkpoint away
				// from a different, still-valid legacy campaign. Preserve
				// that legacy provenance and retag only the replay clone.
				contextIndex := -1
				for index := range state.Contexts {
					if state.Contexts[index].ID == raw.ContextID {
						contextIndex = index
						break
					}
				}
				if contextIndex < 0 ||
					state.Contexts[contextIndex].InventoryHash != "historical-replay" ||
					state.Contexts[contextIndex].ProfileHash != "historical-replay" ||
					state.Contexts[contextIndex].CommitSHA != raw.CommitSHA ||
					state.CampaignHistory[dependency.CampaignID] != raw.CommitSHA {
					return err
				}
				state.Contexts[contextIndex].CampaignID = dependency.CampaignID
			}
		}
		raw.CampaignID = dependency.CampaignID
		raw.AdmissionBucket = dependency.AdmissionBucket
		raw.DiagnosisDigest = RawReviewFindingDiagnosisDigest(*raw)
		job.AdmissionBucket = dependency.AdmissionBucket
	}
	removedDeduplicated := make(map[string]struct{})
	replacementIDs := make(map[string]string)
	rehomedLegacyIDs := make(map[string]string)
	replacements := make([]DeduplicatedReviewFinding, 0)
	keptDeduplicated := make([]DeduplicatedReviewFinding, 0, len(state.DeduplicatedFindings))
	for _, finding := range state.DeduplicatedFindings {
		historicalSources := 0
		nonHistoricalRawIDs := make([]string, 0, len(finding.RawSourceIDs))
		for _, rawID := range finding.RawSourceIDs {
			if _, historical := historicalRawIDs[rawID]; historical {
				historicalSources++
			} else {
				nonHistoricalRawIDs = append(nonHistoricalRawIDs, rawID)
			}
		}
		if historicalSources == 0 {
			keptDeduplicated = append(keptDeduplicated, finding)
			continue
		}
		if len(nonHistoricalRawIDs) == 0 {
			removedDeduplicated[finding.ID] = struct{}{}
			legacyID, rehomeErr := rehomeHistoricalDeduplicatedLifecycle(
				state, finding, historicalRawIDs, rawIndexes, now,
			)
			if rehomeErr != nil {
				return rehomeErr
			}
			rehomedLegacyIDs[finding.ID] = legacyID
			continue
		}
		firstIndex, found := rawIndexes[nonHistoricalRawIDs[0]]
		if !found {
			return ErrConflict
		}
		firstRaw := state.RawFindings[firstIndex]
		firstJob := jobsByRawID[firstRaw.ID]
		if firstJob == nil || firstJob.State != DeduplicationJobCompleted {
			return ErrConflict
		}
		replacement := newDeduplicatedReviewFinding(
			firstRaw, firstJob.InsertionOrdinal, state.Findings, now,
		)
		if replacement.ID == finding.ID {
			// The retained first source already owns this aggregate identity.
			// Only aggregate membership changes; every completed retained raw,
			// job, projection, association, and downstream reference remains a
			// durable checkpoint.
			replacement = finding
			replacement.RawSourceIDs = append([]string(nil), nonHistoricalRawIDs...)
			replacement.Version++
			replacement.UpdatedAt = now
			replacement.History = appendDeduplicatedFindingHistory(
				replacement.History,
				DeduplicatedFindingHistoryEntry{
					Action: "historical_sources_split", At: now,
				},
			)
			keptDeduplicated = append(keptDeduplicated, replacement)
			continue
		}
		removedDeduplicated[finding.ID] = struct{}{}
		replacement.Status = finding.Status
		replacement.IssueDraftID = finding.IssueDraftID
		replacement.RepositoryFindingID = finding.RepositoryFindingID
		replacement.RepositoryMatchState = finding.RepositoryMatchState
		replacement.TargetBranch = finding.TargetBranch
		replacement.AdvertisedDefaultBranch = finding.AdvertisedDefaultBranch
		replacement.TargetIsDefault = finding.TargetIsDefault
		replacement.RawSourceIDs = append([]string(nil), nonHistoricalRawIDs...)
		for _, rawID := range nonHistoricalRawIDs[1:] {
			replacement.History = appendDeduplicatedFindingHistory(
				replacement.History,
				DeduplicatedFindingHistoryEntry{Action: "source_attached", RawFindingID: rawID, At: now},
			)
		}
		if deduplicatedFindingIndexByID(keptDeduplicated, replacement.ID) >= 0 {
			return ErrConflict
		}
		keptDeduplicated = append(keptDeduplicated, replacement)
		replacements = append(replacements, replacement)
		replacementIDs[finding.ID] = replacement.ID
	}
	referenceReplacementID := func(id string) string {
		if replacementID := replacementIDs[id]; replacementID != "" {
			return replacementID
		}
		return rehomedLegacyIDs[id]
	}
	for repositoryIndex := range state.RepositoryFindings {
		repositoryFinding := &state.RepositoryFindings[repositoryIndex]
		changed := false
		reviewFindingIDs := make([]string, 0, len(repositoryFinding.ReviewFindingIDs))
		for _, occurrenceID := range repositoryFinding.ReviewFindingIDs {
			replacementID := referenceReplacementID(occurrenceID)
			if replacementID != "" {
				changed = changed || replacementID != occurrenceID
				occurrenceID = replacementID
			} else if _, removed := removedDeduplicated[occurrenceID]; removed {
				return ErrConflict
			}
			before := len(reviewFindingIDs)
			reviewFindingIDs = appendUnique(reviewFindingIDs, occurrenceID)
			changed = changed || len(reviewFindingIDs) == before
		}
		repositoryFinding.ReviewFindingIDs = reviewFindingIDs
		for historyIndex := range repositoryFinding.PathSymbolHistory {
			historyFindingID := repositoryFinding.PathSymbolHistory[historyIndex].ReviewFindingID
			if replacementID := referenceReplacementID(historyFindingID); replacementID != "" {
				repositoryFinding.PathSymbolHistory[historyIndex].ReviewFindingID = replacementID
				changed = changed || replacementID != historyFindingID
			}
		}
		if changed {
			repositoryFinding.Version++
			repositoryFinding.UpdatedAt = now
		}
	}
	state.DeduplicatedFindings = keptDeduplicated
	if len(removedDeduplicated) > 0 {
		findings := state.Findings[:0]
		for _, finding := range state.Findings {
			if _, removed := removedDeduplicated[finding.ID]; !removed {
				findings = append(findings, finding)
			}
		}
		state.Findings = findings
		mappingJobs := state.MappingJobs[:0]
		for _, job := range state.MappingJobs {
			if replacementID := replacementIDs[job.ReviewFindingID]; replacementID != "" {
				if job.State == RepositoryMappingRunning {
					return ErrHistoricalDeduplicationNotQuiescent
				}
				job.ReviewFindingID = replacementID
				job.ID = mappingJobID(replacementID)
				job.UpdatedAt = now
				mappingJobs = append(mappingJobs, job)
			} else if _, removed := removedDeduplicated[job.ReviewFindingID]; !removed {
				mappingJobs = append(mappingJobs, job)
			}
		}
		state.MappingJobs = mappingJobs
		for draftIndex := range state.IssueDrafts {
			draft := &state.IssueDrafts[draftIndex]
			changed := false
			findingIDs := make([]string, 0, len(draft.FindingIDs))
			for _, findingID := range draft.FindingIDs {
				if replacementID := referenceReplacementID(findingID); replacementID != "" {
					changed = changed || replacementID != findingID
					findingID = replacementID
				}
				before := len(findingIDs)
				findingIDs = appendUnique(findingIDs, findingID)
				changed = changed || len(findingIDs) == before
			}
			draft.FindingIDs = findingIDs
			if changed {
				draft.Version++
				draft.UpdatedAt = now
			}
		}
		for runIndex := range state.Runs {
			for findingIndex, findingID := range state.Runs[runIndex].FindingIDs {
				if replacementID := referenceReplacementID(findingID); replacementID != "" {
					state.Runs[runIndex].FindingIDs[findingIndex] = replacementID
				}
			}
		}
	}
	for _, replacement := range replacements {
		firstRawIndex := rawIndexes[replacement.RawSourceIDs[0]]
		firstRaw := state.RawFindings[firstRawIndex]
		projection := deduplicatedFindingProjection(replacement, firstRaw, state.Findings)
		projection.IssueDraftID = replacement.IssueDraftID
		projection.RepositoryFindingID = replacement.RepositoryFindingID
		projection.RepositoryMatchState = replacement.RepositoryMatchState
		state.Findings = append(state.Findings, projection)
		for sourceIndex, rawID := range replacement.RawSourceIDs {
			rawIndex, found := rawIndexes[rawID]
			job := jobsByRawID[rawID]
			if !found || job == nil || job.State != DeduplicationJobCompleted {
				return ErrConflict
			}
			raw := &state.RawFindings[rawIndex]
			raw.DeduplicatedFindingID = replacement.ID
			raw.Disposition = RawFindingDispositionDuplicate
			job.Decision = DeduplicationJudgment{
				Decision: "duplicate", CandidateID: replacement.ID,
			}
			if sourceIndex == 0 {
				raw.Disposition = RawFindingDispositionNew
				job.Decision = DeduplicationJudgment{Decision: "new"}
			}
			raw.Version++
			raw.UpdatedAt = now
			raw.History = appendRawFindingHistory(raw.History, RawFindingHistoryEntry{
				State: RawFindingDeduplicationCompleted, Disposition: raw.Disposition,
				DeduplicatedFindingID: replacement.ID, At: now,
			})
			job.CandidateUniverseDigest = ""
			job.CandidateVersions = nil
			job.ShortlistedScores = nil
			job.UpdatedAt = now
		}
		ensureMappingJobsForFindings(state, []string{replacement.ID}, now)
	}
	for index := range state.RawFindings {
		raw := &state.RawFindings[index]
		if _, historical := historicalRawIDs[raw.ID]; !historical {
			continue
		}
		job := jobsByRawID[raw.ID]
		if job == nil {
			return ErrConflict
		}
		raw.State = RawFindingDeduplicationPending
		raw.Disposition = RawFindingDispositionUndecided
		raw.DeduplicatedFindingID = ""
		raw.Failure = nil
		raw.Version++
		raw.UpdatedAt = now
		raw.History = appendRawFindingHistory(raw.History, RawFindingHistoryEntry{
			State:       RawFindingDeduplicationPending,
			Disposition: RawFindingDispositionUndecided, At: now,
		})
		job.State = DeduplicationJobPending
		job.LeaseID = ""
		job.LeaseExpiresAt = time.Time{}
		job.Attempts = 0
		job.ModelSnapshot = snapshot
		job.CandidateUniverseDigest = ""
		job.CandidateVersions = nil
		job.ShortlistedScores = nil
		job.Decision = DeduplicationJudgment{}
		job.Failure = nil
		job.UpdatedAt = now
		job.History = appendDeduplicationJobHistory(job.History, DeduplicationJobHistoryEntry{
			State: DeduplicationJobPending, At: now,
		})
	}
	reconcileFindingsProcessingCounters(state)
	state.FindingsProcessing.UpdatedAt = now
	return nil
}

// rehomeHistoricalDeduplicatedLifecycle moves user-controlled lifecycle and
// association state from a replay-derived rdf_* projection back onto its
// retained legacy occurrence while model work is reset. The legacy occurrence
// is stable across retry attempts and gives drafts/repository history a valid
// durable referent until completion restores the canonical rdf_* identity.
func rehomeHistoricalDeduplicatedLifecycle(
	state *RepositoryState,
	finding DeduplicatedReviewFinding,
	historicalRawIDs map[string]struct{},
	rawIndexes map[string]int,
	now time.Time,
) (string, error) {
	if state == nil {
		return "", errors.New("repository review state is required")
	}
	legacyID := ""
	for _, rawID := range finding.RawSourceIDs {
		if _, historical := historicalRawIDs[rawID]; !historical {
			continue
		}
		rawIndex, found := rawIndexes[rawID]
		if !found {
			return "", ErrConflict
		}
		legacyID = state.RawFindings[rawIndex].LegacyFindingID
		if legacyID != "" {
			break
		}
	}
	legacyIndex := findingIndexByID(state.Findings, legacyID)
	if legacyID == "" || legacyIndex < 0 {
		return "", ErrConflict
	}
	legacy := &state.Findings[legacyIndex]
	if finding.Status == FindingOpen || finding.Status == FindingDismissed ||
		finding.Status == FindingPosted {
		legacy.Status = finding.Status
	}
	if finding.IssueDraftID != "" {
		legacy.IssueDraftID = finding.IssueDraftID
	}
	if finding.RepositoryFindingID != "" {
		legacy.RepositoryFindingID = finding.RepositoryFindingID
		legacy.RepositoryMatchState = finding.RepositoryMatchState
	}
	legacy.TargetBranch = finding.TargetBranch
	legacy.AdvertisedDefaultBranch = finding.AdvertisedDefaultBranch
	legacy.TargetIsDefault = finding.TargetIsDefault
	legacy.Version++
	legacy.UpdatedAt = now
	return legacyID, nil
}

// restoreHistoricalDeduplicatedLifecycle moves draft ownership and lifecycle
// status from the retained legacy occurrence onto the canonical rdf_* selected
// by replay. Repository associations deliberately remain on the legacy
// occurrence until the historical repository merge consumes that provenance.
func restoreHistoricalDeduplicatedLifecycle(
	state *RepositoryState,
	raw RawReviewFinding,
	targetID string,
	now time.Time,
) error {
	if state == nil || !HistoricalDeduplicationRawFinding(raw) {
		return nil
	}
	legacyIndex := findingIndexByID(state.Findings, raw.LegacyFindingID)
	targetIndex := deduplicatedFindingIndexByID(state.DeduplicatedFindings, targetID)
	projectionIndex := findingIndexByID(state.Findings, targetID)
	if legacyIndex < 0 || targetIndex < 0 || projectionIndex < 0 {
		return ErrConflict
	}
	legacy := &state.Findings[legacyIndex]
	target := &state.DeduplicatedFindings[targetIndex]
	projection := &state.Findings[projectionIndex]
	status := mergeHistoricalFindingStatus(target.Status, legacy.Status)
	target.Status = status
	projection.Status = status
	restoreDraft := target.IssueDraftID == "" || legacy.IssueDraftID == "" ||
		target.IssueDraftID == legacy.IssueDraftID
	if restoreDraft {
		if target.IssueDraftID == "" {
			target.IssueDraftID = legacy.IssueDraftID
		}
		if projection.IssueDraftID == "" {
			projection.IssueDraftID = legacy.IssueDraftID
		}
		for draftIndex := range state.IssueDrafts {
			draft := &state.IssueDrafts[draftIndex]
			changed := false
			findingIDs := make([]string, 0, len(draft.FindingIDs))
			for _, findingID := range draft.FindingIDs {
				if findingID == legacy.ID {
					findingID = target.ID
					changed = true
				}
				before := len(findingIDs)
				findingIDs = appendUnique(findingIDs, findingID)
				changed = changed || len(findingIDs) == before
			}
			if changed {
				draft.FindingIDs = findingIDs
				draft.Version++
				draft.UpdatedAt = now
			}
		}
		legacy.IssueDraftID = ""
	}
	legacy.Version++
	legacy.UpdatedAt = now
	target.Version++
	target.UpdatedAt = now
	target.History = appendDeduplicatedFindingHistory(
		target.History,
		DeduplicatedFindingHistoryEntry{
			Action: "historical_lifecycle_restored", RawFindingID: raw.ID, At: now,
		},
	)
	projection.Version++
	projection.UpdatedAt = now
	return nil
}

func mergeHistoricalFindingStatus(left, right FindingStatus) FindingStatus {
	rank := func(status FindingStatus) int {
		switch status {
		case FindingPosted:
			return 3
		case FindingDismissed:
			return 2
		case FindingOpen:
			return 1
		default:
			return 0
		}
	}
	if rank(right) > rank(left) {
		return right
	}
	if rank(left) == 0 {
		return FindingOpen
	}
	return left
}

func (s Store) AcquireHistoricalDeduplicationMerge(
	repository, leaseID string,
	groups []HistoricalDeduplicationMergeGroup,
) (RepositoryState, HistoricalDeduplicationReplay, bool, error) {
	leaseID = strings.TrimSpace(leaseID)
	groups, err := normalizeHistoricalDeduplicationMergeGroups(groups)
	if err != nil || !validBoundedText(leaseID, 256) {
		if err == nil {
			err = errors.New("invalid historical deduplication merge lease")
		}
		return RepositoryState{}, HistoricalDeduplicationReplay{}, false, err
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, false, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, false, err
	}
	replay := &state.HistoricalDeduplication
	if replay.Status == HistoricalDeduplicationMerging {
		if replay.MergeLease.ID == leaseID && reflect.DeepEqual(replay.MergeLease.Groups, groups) {
			return state, *replay, false, nil
		}
		return RepositoryState{}, HistoricalDeduplicationReplay{}, false,
			ErrHistoricalDeduplicationInProgress
	}
	if !replay.Required || replay.Status != HistoricalDeduplicationReplaying {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, false, ErrConflict
	}
	if !HistoricalDeduplicationQuiescenceForState(state).Ready() {
		return state, *replay, false, ErrHistoricalDeduplicationNotQuiescent
	}
	if err := validateHistoricalDeduplicationMergeTargets(state, groups); err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, false, err
	}
	now := s.clock()
	replay.Status = HistoricalDeduplicationMerging
	replay.MergeLease = HistoricalDeduplicationMergeLease{
		ID: leaseID, Groups: groups, AcquiredAt: now,
	}
	replay.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, false, err
	}
	return state, *replay, true, nil
}

// RecoverHistoricalDeduplicationMerge atomically releases a merge lease left
// by an interrupted controller and returns to the result-preserving merge
// preparation boundary. The next pass recomputes groups and current target
// versions before acquiring a fresh lease; no model checkpoint is reset.
func (s Store) RecoverHistoricalDeduplicationMerge(
	repository, leaseID string,
) (RepositoryState, HistoricalDeduplicationReplay, error) {
	leaseID = strings.TrimSpace(leaseID)
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	replay := &state.HistoricalDeduplication
	if replay.Required && replay.Status == HistoricalDeduplicationReplaying &&
		replay.MergeLease.ID == "" {
		return state, *replay, nil
	}
	if !replay.Required || replay.Status != HistoricalDeduplicationMerging ||
		leaseID == "" || replay.MergeLease.ID != leaseID {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, ErrConflict
	}
	now := s.clock()
	replay.Status = HistoricalDeduplicationReplaying
	replay.Attempts++
	replay.Error = ""
	replay.FailurePhase = ""
	replay.MergeLease = HistoricalDeduplicationMergeLease{}
	replay.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	return state, *replay, nil
}

func (s Store) CompleteHistoricalDeduplicationMerge(
	repository, leaseID string,
) (RepositoryState, HistoricalDeduplicationReplay, error) {
	leaseID = strings.TrimSpace(leaseID)
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	replay := &state.HistoricalDeduplication
	if !replay.Required || replay.Status != HistoricalDeduplicationMerging ||
		replay.MergeLease.ID != leaseID {
		if !replay.Required && replay.Status == HistoricalDeduplicationCompleted &&
			replay.MergeLease.ID == "" {
			return state, *replay, nil
		}
		return RepositoryState{}, HistoricalDeduplicationReplay{}, ErrConflict
	}
	groups := replay.MergeLease.Groups
	if err := validateHistoricalDeduplicationMergeTargets(state, groups); err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	now := s.clock()
	for _, group := range groups {
		if err := mergeHistoricalRepositoryFindingGroup(&state, group, now); err != nil {
			return RepositoryState{}, HistoricalDeduplicationReplay{}, err
		}
	}
	associateHistoricalDeduplicatedFindings(&state, now)
	replay = &state.HistoricalDeduplication
	replay.Required = false
	replay.Status = HistoricalDeduplicationCompleted
	replay.Error = ""
	replay.FailurePhase = ""
	replay.MergeLease = HistoricalDeduplicationMergeLease{}
	replay.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	return state, *replay, nil
}

func associateHistoricalDeduplicatedFindings(state *RepositoryState, now time.Time) {
	if state == nil {
		return
	}
	rawByID := make(map[string]RawReviewFinding, len(state.RawFindings))
	for _, raw := range state.RawFindings {
		rawByID[raw.ID] = raw
	}
	legacyByID := make(map[string]Finding, len(state.Findings))
	for _, finding := range state.Findings {
		legacyByID[finding.ID] = finding
	}
	for index := range state.DeduplicatedFindings {
		deduplicated := &state.DeduplicatedFindings[index]
		if deduplicated.RepositoryFindingID != "" {
			continue
		}
		targetIDs := make(map[string]struct{})
		historical := false
		for _, rawID := range deduplicated.RawSourceIDs {
			raw, found := rawByID[rawID]
			if !found || !HistoricalDeduplicationRawFinding(raw) {
				continue
			}
			historical = true
			if targetID := legacyByID[raw.LegacyFindingID].RepositoryFindingID; targetID != "" {
				targetIDs[targetID] = struct{}{}
			}
		}
		if !historical || len(targetIDs) != 1 {
			continue
		}
		targetID := ""
		for id := range targetIDs {
			targetID = id
		}
		targetIndex := repositoryFindingIndexByID(state.RepositoryFindings, targetID)
		projectionIndex := findingIndexByID(state.Findings, deduplicated.ID)
		if targetIndex < 0 || projectionIndex < 0 {
			continue
		}
		target := &state.RepositoryFindings[targetIndex]
		projection := &state.Findings[projectionIndex]
		associateOccurrenceWithRepositoryFinding(target, *projection, now)
		target.Version++
		projection.RepositoryFindingID = target.ID
		projection.RepositoryMatchState = target.MatchState
		projection.Version++
		projection.UpdatedAt = now
		deduplicated.RepositoryFindingID = target.ID
		deduplicated.RepositoryMatchState = target.MatchState
		deduplicated.Version++
		deduplicated.UpdatedAt = now
		deduplicated.History = appendDeduplicatedFindingHistory(
			deduplicated.History,
			DeduplicatedFindingHistoryEntry{
				Action: "historical_repository_associated", RepositoryFindingID: target.ID, At: now,
			},
		)
		if jobIndex := mappingJobIndexByReviewFindingID(
			state.MappingJobs, deduplicated.ID,
		); jobIndex >= 0 {
			job := &state.MappingJobs[jobIndex]
			job.State = RepositoryMappingCompleted
			job.RepositoryFindingID = target.ID
			job.ReservedAt = time.Time{}
			job.Error = ""
			job.UpdatedAt = now
		}
	}
}

func mappingJobIndexByReviewFindingID(jobs []RepositoryMappingJob, findingID string) int {
	for index := range jobs {
		if jobs[index].ReviewFindingID == findingID {
			return index
		}
	}
	return -1
}

func (s Store) FailHistoricalDeduplicationReplay(
	repository, leaseID string,
) (RepositoryState, HistoricalDeduplicationReplay, error) {
	leaseID = strings.TrimSpace(leaseID)
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	replay := &state.HistoricalDeduplication
	if !replay.Required || replay.Status == HistoricalDeduplicationCompleted {
		return state, *replay, nil
	}
	if replay.Status == HistoricalDeduplicationFailed {
		return state, *replay, nil
	}
	if replay.Status == HistoricalDeduplicationMerging && replay.MergeLease.ID != leaseID {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, ErrConflict
	}
	now := s.clock()
	phase := HistoricalDeduplicationFailurePhaseForState(state)
	if replay.Status == HistoricalDeduplicationMerging {
		phase = HistoricalDeduplicationFailureMerge
	}
	replay.Status = HistoricalDeduplicationFailed
	replay.Error = "Historical deduplication failed."
	replay.FailurePhase = phase
	replay.MergeLease = HistoricalDeduplicationMergeLease{}
	replay.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	return state, *replay, nil
}

func (s Store) RetryHistoricalDeduplicationReplay(
	repository string,
) (RepositoryState, HistoricalDeduplicationReplay, error) {
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	now := s.clock()
	changed, err := retryHistoricalDeduplicationReplayInState(&state, now)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	if !changed {
		return state, state.HistoricalDeduplication, nil
	}
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	return state, state.HistoricalDeduplication, nil
}

// ResumeHistoricalDeduplicationReplay atomically verifies the current exact
// profile and source dependency plan before performing non-destructive
// checkpoint resume. Drift is reported without mutating the ledger.
func (s Store) ResumeHistoricalDeduplicationReplay(
	repository string,
	snapshot HistoricalDeduplicationProfileSnapshot,
	dependencies []HistoricalDeduplicationDependency,
) (RepositoryState, HistoricalDeduplicationReplay, error) {
	if err := validateHistoricalDeduplicationProfileSnapshot(snapshot); err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	if !state.HistoricalDeduplication.Required ||
		state.HistoricalDeduplication.Status == HistoricalDeduplicationCompleted {
		return state, state.HistoricalDeduplication, nil
	}
	dependencies, err = normalizeHistoricalDeduplicationDependencies(state, dependencies)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	compatible, err := historicalDeduplicationDependenciesCompatible(
		state, snapshot, dependencies,
	)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	if !compatible {
		return state, state.HistoricalDeduplication,
			ErrHistoricalDeduplicationRestartRequired
	}
	now := s.clock()
	changed, err := retryHistoricalDeduplicationReplayInState(&state, now)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	if !changed {
		return state, state.HistoricalDeduplication, nil
	}
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	return state, state.HistoricalDeduplication, nil
}

func historicalDeduplicationDependenciesCompatible(
	state RepositoryState,
	snapshot HistoricalDeduplicationProfileSnapshot,
	dependencies []HistoricalDeduplicationDependency,
) (bool, error) {
	replay := state.HistoricalDeduplication
	if validateHistoricalDeduplicationProfileSnapshot(replay.ProfileSnapshot) == nil {
		if !reflect.DeepEqual(replay.ProfileSnapshot, snapshot) {
			return false, nil
		}
	} else if historicalDeduplicationHasAdmittedRaw(state) {
		return false, nil
	}
	current, err := currentHistoricalDeduplicationDependencies(state)
	if err != nil {
		return false, err
	}
	currentByLegacy := make(map[string]HistoricalDeduplicationDependency, len(current))
	for _, dependency := range current {
		currentByLegacy[dependency.LegacyFindingID] = dependency
	}
	for _, dependency := range dependencies {
		if currentByLegacy[dependency.LegacyFindingID] != dependency {
			return false, nil
		}
	}
	return true, nil
}

func currentHistoricalDeduplicationDependencies(
	state RepositoryState,
) ([]HistoricalDeduplicationDependency, error) {
	current, err := HistoricalDeduplicationDependencies(state, "", nil)
	if err != nil {
		return nil, err
	}
	indexes := make(map[string]int, len(current))
	for index, dependency := range current {
		indexes[dependency.LegacyFindingID] = index
	}
	for _, raw := range state.RawFindings {
		if !HistoricalDeduplicationRawFinding(raw) {
			continue
		}
		index, found := indexes[raw.LegacyFindingID]
		if !found || current[index].RawFindingID != raw.ID {
			return nil, ErrConflict
		}
		current[index].CampaignID = raw.CampaignID
		current[index].AdmissionBucket = raw.AdmissionBucket
	}
	return current, nil
}

// RestartHistoricalDeduplicationReplay removes only the incompatible
// dependency closure and applies fresh identities to that reset work. Profile
// drift selects all historical sources; campaign/bucket drift expands through
// both old/new buckets and shared deduplicated aggregates.
func (s Store) RestartHistoricalDeduplicationReplay(
	repository string,
	request HistoricalDeduplicationRestartRequest,
) (RepositoryState, HistoricalDeduplicationReplay, error) {
	if err := validateHistoricalDeduplicationProfileSnapshot(request.ProfileSnapshot); err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	if !state.HistoricalDeduplication.Required ||
		state.HistoricalDeduplication.Status == HistoricalDeduplicationCompleted {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, ErrConflict
	}
	dependencies, err := normalizeHistoricalDeduplicationDependencies(
		state, request.Dependencies,
	)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	compatible, err := historicalDeduplicationDependenciesCompatible(
		state, request.ProfileSnapshot, dependencies,
	)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	if state.HistoricalDeduplication.Status != HistoricalDeduplicationFailed {
		if compatible {
			return state, state.HistoricalDeduplication, nil
		}
		return RepositoryState{}, HistoricalDeduplicationReplay{}, ErrConflict
	}
	if compatible {
		now := s.clock()
		changed, retryErr := retryHistoricalDeduplicationReplayInState(&state, now)
		if retryErr != nil {
			return RepositoryState{}, HistoricalDeduplicationReplay{}, retryErr
		}
		if !changed {
			return state, state.HistoricalDeduplication, nil
		}
		state.Version++
		state.UpdatedAt = now
		if err := s.save(&state); err != nil {
			return RepositoryState{}, HistoricalDeduplicationReplay{}, err
		}
		return state, state.HistoricalDeduplication, nil
	}
	if !HistoricalDeduplicationQuiescenceForState(state).Ready() ||
		!historicalDeduplicationRunningWorkQuiescent(state) {
		return state, state.HistoricalDeduplication,
			ErrHistoricalDeduplicationNotQuiescent
	}
	current, err := currentHistoricalDeduplicationDependencies(state)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	selected, err := historicalDeduplicationRestartClosure(
		state, current, dependencies,
		historicalDeduplicationProfileRestartRequired(state, request.ProfileSnapshot),
	)
	if err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	desiredByLegacy := make(map[string]HistoricalDeduplicationDependency, len(dependencies))
	for _, dependency := range dependencies {
		desiredByLegacy[dependency.LegacyFindingID] = dependency
	}
	selectedRawIDs := make(map[string]struct{})
	desiredByRawID := make(map[string]HistoricalDeduplicationDependency)
	for _, raw := range state.RawFindings {
		if !HistoricalDeduplicationRawFinding(raw) {
			continue
		}
		if _, reset := selected[raw.LegacyFindingID]; !reset {
			continue
		}
		dependency, found := desiredByLegacy[raw.LegacyFindingID]
		if !found || dependency.RawFindingID != raw.ID {
			return RepositoryState{}, HistoricalDeduplicationReplay{}, ErrConflict
		}
		selectedRawIDs[raw.ID] = struct{}{}
		desiredByRawID[raw.ID] = dependency
	}
	now := s.clock()
	if len(selectedRawIDs) > 0 {
		if err := resetHistoricalDeduplicationModelWorkSelection(
			&state, request.ProfileSnapshot, selectedRawIDs, desiredByRawID, now,
		); err != nil {
			return RepositoryState{}, HistoricalDeduplicationReplay{}, err
		}
	}
	// Dependency restart and ordinary checkpoint resume are orthogonal. A
	// failed source outside the incompatible closure must still be re-admitted
	// without adopting the fresh identities applied to reset work.
	if historicalDeduplicationHasFailedRaw(state) {
		if err := resetFailedHistoricalDeduplicationModelWork(&state, now); err != nil {
			return RepositoryState{}, HistoricalDeduplicationReplay{}, err
		}
	}
	replay := &state.HistoricalDeduplication
	replay.ProfileSnapshot = request.ProfileSnapshot
	replay.SnapshotVersion = state.Version
	replay.Status = HistoricalDeduplicationReplaying
	replay.Attempts++
	replay.Error = ""
	replay.FailurePhase = ""
	replay.MergeLease = HistoricalDeduplicationMergeLease{}
	replay.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	return state, *replay, nil
}

func historicalDeduplicationHasFailedRaw(state RepositoryState) bool {
	for _, raw := range state.RawFindings {
		if HistoricalDeduplicationRawFinding(raw) &&
			raw.State == RawFindingDeduplicationFailed {
			return true
		}
	}
	return false
}

func historicalDeduplicationHasAdmittedRaw(state RepositoryState) bool {
	for _, raw := range state.RawFindings {
		if HistoricalDeduplicationRawFinding(raw) {
			return true
		}
	}
	return false
}

func historicalDeduplicationProfileRestartRequired(
	state RepositoryState,
	snapshot HistoricalDeduplicationProfileSnapshot,
) bool {
	frozen := state.HistoricalDeduplication.ProfileSnapshot
	if validateHistoricalDeduplicationProfileSnapshot(frozen) != nil {
		return historicalDeduplicationHasAdmittedRaw(state)
	}
	return !reflect.DeepEqual(frozen, snapshot)
}

func historicalDeduplicationRunningWorkQuiescent(state RepositoryState) bool {
	for _, job := range state.DeduplicationJobs {
		if job.State != DeduplicationJobRunning {
			continue
		}
		rawIndex := rawFindingIndexByID(state.RawFindings, job.RawFindingID)
		if rawIndex >= 0 && HistoricalDeduplicationRawFinding(state.RawFindings[rawIndex]) {
			return false
		}
	}
	return true
}

// HistoricalDeduplicationModelWorkQuiescent reports whether no historical
// model lease is active. Callers that span multiple Store mutations must also
// hold their controller's deduplication-worker gate across this check and the
// mutations so pending work cannot become running between them.
func HistoricalDeduplicationModelWorkQuiescent(state RepositoryState) bool {
	return historicalDeduplicationRunningWorkQuiescent(state)
}

func historicalDeduplicationRestartClosure(
	state RepositoryState,
	current []HistoricalDeduplicationDependency,
	desired []HistoricalDeduplicationDependency,
	profileChanged bool,
) (map[string]struct{}, error) {
	currentByLegacy := make(map[string]HistoricalDeduplicationDependency, len(current))
	desiredByLegacy := make(map[string]HistoricalDeduplicationDependency, len(desired))
	for _, dependency := range current {
		currentByLegacy[dependency.LegacyFindingID] = dependency
	}
	for _, dependency := range desired {
		desiredByLegacy[dependency.LegacyFindingID] = dependency
	}
	selected := make(map[string]struct{})
	affectedBuckets := make(map[string]struct{})
	bucketKey := func(dependency HistoricalDeduplicationDependency) string {
		return dependency.CampaignID + "\x00" + dependency.AdmissionBucket
	}
	for legacyID, wanted := range desiredByLegacy {
		was, found := currentByLegacy[legacyID]
		if !found {
			return nil, ErrConflict
		}
		if profileChanged || was != wanted {
			selected[legacyID] = struct{}{}
			affectedBuckets[bucketKey(was)] = struct{}{}
			affectedBuckets[bucketKey(wanted)] = struct{}{}
		}
	}
	rawByID := make(map[string]RawReviewFinding, len(state.RawFindings))
	for _, raw := range state.RawFindings {
		rawByID[raw.ID] = raw
	}
	changed := true
	for changed {
		changed = false
		for legacyID, wanted := range desiredByLegacy {
			was := currentByLegacy[legacyID]
			_, oldAffected := affectedBuckets[bucketKey(was)]
			_, newAffected := affectedBuckets[bucketKey(wanted)]
			if !oldAffected && !newAffected {
				continue
			}
			if _, found := selected[legacyID]; !found {
				selected[legacyID] = struct{}{}
				changed = true
			}
			if _, found := affectedBuckets[bucketKey(was)]; !found {
				affectedBuckets[bucketKey(was)] = struct{}{}
				changed = true
			}
			if _, found := affectedBuckets[bucketKey(wanted)]; !found {
				affectedBuckets[bucketKey(wanted)] = struct{}{}
				changed = true
			}
		}
		for _, aggregate := range state.DeduplicatedFindings {
			sharesSelected := false
			for _, rawID := range aggregate.RawSourceIDs {
				raw, found := rawByID[rawID]
				if found && HistoricalDeduplicationRawFinding(raw) {
					_, sharesSelected = selected[raw.LegacyFindingID]
				}
				if sharesSelected {
					break
				}
			}
			if !sharesSelected {
				continue
			}
			for _, rawID := range aggregate.RawSourceIDs {
				raw, found := rawByID[rawID]
				if !found || !HistoricalDeduplicationRawFinding(raw) {
					continue
				}
				if _, exists := desiredByLegacy[raw.LegacyFindingID]; !exists {
					return nil, ErrConflict
				}
				if _, found := selected[raw.LegacyFindingID]; !found {
					selected[raw.LegacyFindingID] = struct{}{}
					was := currentByLegacy[raw.LegacyFindingID]
					wanted := desiredByLegacy[raw.LegacyFindingID]
					affectedBuckets[bucketKey(was)] = struct{}{}
					affectedBuckets[bucketKey(wanted)] = struct{}{}
					changed = true
				}
			}
		}
	}
	return selected, nil
}

func retryHistoricalDeduplicationReplayInState(
	state *RepositoryState,
	now time.Time,
) (bool, error) {
	if state == nil {
		return false, errors.New("repository review state is required")
	}
	replay := &state.HistoricalDeduplication
	if !replay.Required || replay.Status == HistoricalDeduplicationCompleted {
		return false, nil
	}
	// A lost response may race the worker through these states. Returning the
	// durable state is idempotent and never repeats running model work.
	if replay.Status == HistoricalDeduplicationPending ||
		replay.Status == HistoricalDeduplicationReplaying ||
		replay.Status == HistoricalDeduplicationMerging {
		return false, nil
	}
	if replay.Status != HistoricalDeduplicationFailed {
		return false, ErrConflict
	}
	phase := historicalDeduplicationResumePhase(*state)
	switch phase {
	case HistoricalDeduplicationFailureProcessing:
		if err := resetFailedHistoricalDeduplicationModelWork(state, now); err != nil {
			return false, err
		}
		replay = &state.HistoricalDeduplication
		replay.Status = HistoricalDeduplicationReplaying
		replay.Attempts++
	case HistoricalDeduplicationFailureMerge:
		// All raw and deduplicated decisions are durable checkpoints. Returning
		// to replaying makes the controller recompute merge groups and current
		// repository-finding versions before acquiring a fresh lease.
		replay.Status = HistoricalDeduplicationReplaying
		replay.Attempts++
	case HistoricalDeduplicationFailureSetup:
		// Setup can fail before a profile is frozen. If it was already frozen,
		// continue directly without adopting a different dependency snapshot.
		if validateHistoricalDeduplicationProfileSnapshot(replay.ProfileSnapshot) == nil {
			replay.Status = HistoricalDeduplicationReplaying
			replay.Attempts++
		} else {
			replay.Status = HistoricalDeduplicationPending
			replay.Attempts++
		}
	default:
		return false, ErrConflict
	}
	replay.Error = ""
	replay.FailurePhase = ""
	replay.MergeLease = HistoricalDeduplicationMergeLease{}
	replay.UpdatedAt = now
	return true, nil
}

// resetFailedHistoricalDeduplicationModelWork re-admits only terminal failed
// replay jobs. Completed raw sources, aggregate projections, associations,
// histories, IDs, versions, ordinals, dependency identities, and model
// snapshots remain byte-for-byte unchanged.
func resetFailedHistoricalDeduplicationModelWork(
	state *RepositoryState,
	now time.Time,
) error {
	if state == nil {
		return errors.New("repository review state is required")
	}
	jobsByRawID := make(map[string]*DeduplicationJob, len(state.DeduplicationJobs))
	for index := range state.DeduplicationJobs {
		job := &state.DeduplicationJobs[index]
		jobsByRawID[job.RawFindingID] = job
	}
	reset := 0
	for index := range state.RawFindings {
		raw := &state.RawFindings[index]
		if !HistoricalDeduplicationRawFinding(*raw) ||
			raw.State != RawFindingDeduplicationFailed {
			continue
		}
		job := jobsByRawID[raw.ID]
		if job == nil || job.State != DeduplicationJobFailed ||
			raw.DeduplicatedFindingID != "" ||
			job.AdmissionBucket != raw.AdmissionBucket ||
			job.InsertionOrdinal != raw.InsertionOrdinal {
			return ErrConflict
		}
		raw.State = RawFindingDeduplicationPending
		raw.Disposition = RawFindingDispositionUndecided
		raw.Failure = nil
		raw.Version++
		raw.UpdatedAt = now
		raw.History = appendRawFindingHistory(raw.History, RawFindingHistoryEntry{
			State:       RawFindingDeduplicationPending,
			Disposition: RawFindingDispositionUndecided,
			At:          now,
		})
		job.State = DeduplicationJobPending
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
		reset++
	}
	if reset == 0 {
		return ErrConflict
	}
	reconcileFindingsProcessingCounters(state)
	state.FindingsProcessing.UpdatedAt = now
	return nil
}

func normalizeHistoricalDeduplicationMergeGroups(
	groups []HistoricalDeduplicationMergeGroup,
) ([]HistoricalDeduplicationMergeGroup, error) {
	if len(groups) > maxHistoricalDeduplicationMergeGroups {
		return nil, errors.New("historical deduplication merge set exceeds its limit")
	}
	normalized := make([]HistoricalDeduplicationMergeGroup, len(groups))
	seen := make(map[string]struct{})
	for groupIndex, group := range groups {
		if len(group.Members) < 2 || len(group.Members) > maxRepositoryFindings {
			return nil, errors.New("historical deduplication merge group must have at least two members")
		}
		normalized[groupIndex].Members = append(
			[]HistoricalDeduplicationFindingVersion(nil), group.Members...,
		)
		sort.Slice(normalized[groupIndex].Members, func(left, right int) bool {
			return normalized[groupIndex].Members[left].ID < normalized[groupIndex].Members[right].ID
		})
		for _, member := range normalized[groupIndex].Members {
			if strings.TrimSpace(member.ID) == "" || member.ID != strings.TrimSpace(member.ID) ||
				member.Version < 1 {
				return nil, errors.New("invalid historical deduplication merge member")
			}
			if _, duplicate := seen[member.ID]; duplicate {
				return nil, errors.New("duplicate historical deduplication merge member")
			}
			seen[member.ID] = struct{}{}
		}
	}
	sort.Slice(normalized, func(left, right int) bool {
		return normalized[left].Members[0].ID < normalized[right].Members[0].ID
	})
	return normalized, nil
}

func validateHistoricalDeduplicationMergeTargets(
	state RepositoryState,
	groups []HistoricalDeduplicationMergeGroup,
) error {
	for _, group := range groups {
		for _, member := range group.Members {
			index := repositoryFindingIndexByID(state.RepositoryFindings, member.ID)
			if index < 0 {
				return os.ErrNotExist
			}
			if state.RepositoryFindings[index].Version != member.Version {
				return ErrConflict
			}
		}
	}
	return nil
}

func mergeHistoricalRepositoryFindingGroup(
	state *RepositoryState,
	group HistoricalDeduplicationMergeGroup,
	now time.Time,
) error {
	if state == nil {
		return errors.New("repository review state is required")
	}
	members := make([]RepositoryFinding, 0, len(group.Members))
	for _, expected := range group.Members {
		index := repositoryFindingIndexByID(state.RepositoryFindings, expected.ID)
		if index < 0 {
			return os.ErrNotExist
		}
		members = append(members, state.RepositoryFindings[index])
	}
	sort.SliceStable(members, func(left, right int) bool {
		if members[left].CreatedAt.Equal(members[right].CreatedAt) {
			return members[left].ID < members[right].ID
		}
		return members[left].CreatedAt.Before(members[right].CreatedAt)
	})
	survivorID := members[0].ID
	merged := members[0]
	removed := make(map[string]struct{}, len(members)-1)
	for _, source := range members[1:] {
		removed[source.ID] = struct{}{}
		merged = mergeRepositoryFindingRecords(&merged, source, now)
		merged.Lifecycle = mergeHistoricalRepositoryFindingLifecycle(
			merged.Lifecycle, source.Lifecycle,
		)
		mergeHistoricalValidationSnapshot(&merged, source)
	}
	for index := range state.Findings {
		finding := &state.Findings[index]
		_, removedAssociation := removed[finding.RepositoryFindingID]
		if removedAssociation || finding.RepositoryFindingID == survivorID {
			changed := finding.RepositoryFindingID != survivorID ||
				finding.RepositoryMatchState != RepositoryMatchKnown
			finding.RepositoryFindingID = survivorID
			finding.RepositoryMatchState = RepositoryMatchKnown
			if changed {
				finding.Version++
				finding.UpdatedAt = now
			}
		}
		if _, selected := removed[finding.PostResolutionFindingID]; selected {
			finding.PostResolutionFindingID = survivorID
			finding.Version++
			finding.UpdatedAt = now
		}
	}
	for index := range state.DeduplicatedFindings {
		finding := &state.DeduplicatedFindings[index]
		_, removedAssociation := removed[finding.RepositoryFindingID]
		if !removedAssociation && finding.RepositoryFindingID != survivorID {
			continue
		}
		changed := finding.RepositoryFindingID != survivorID ||
			finding.RepositoryMatchState != RepositoryMatchKnown
		finding.RepositoryFindingID = survivorID
		finding.RepositoryMatchState = RepositoryMatchKnown
		if !changed {
			continue
		}
		finding.Version++
		finding.UpdatedAt = now
		finding.History = append(finding.History, DeduplicatedFindingHistoryEntry{
			Action: "historical_repository_merge", RepositoryFindingID: survivorID, At: now,
		})
		if len(finding.History) > DeduplicationHistoryLimit {
			finding.History = append(
				[]DeduplicatedFindingHistoryEntry(nil),
				finding.History[len(finding.History)-DeduplicationHistoryLimit:]...,
			)
		}
	}
	for index := range state.MappingJobs {
		job := &state.MappingJobs[index]
		if _, selected := removed[job.RepositoryFindingID]; selected {
			job.RepositoryFindingID = survivorID
			job.UpdatedAt = now
		}
		if _, selected := removed[job.Adjudication.CandidateID]; selected {
			job.Adjudication.CandidateID = survivorID
			job.UpdatedAt = now
		}
	}
	for index := range state.ValidationJobs {
		job := &state.ValidationJobs[index]
		if _, selected := removed[job.RepositoryFindingID]; selected {
			job.RepositoryFindingID = survivorID
			job.UpdatedAt = now
		}
	}
	result := make([]RepositoryFinding, 0, len(state.RepositoryFindings)-len(removed))
	for _, finding := range state.RepositoryFindings {
		if finding.ID == survivorID {
			result = append(result, merged)
			continue
		}
		if _, selected := removed[finding.ID]; selected {
			continue
		}
		changed := false
		updated := make([]RepositoryFindingPossibleDuplicate, 0, len(finding.PossibleDuplicates))
		for _, duplicate := range finding.PossibleDuplicates {
			if _, selected := removed[duplicate.CandidateID]; selected {
				duplicate.CandidateID = survivorID
				changed = true
			}
			if duplicate.CandidateID == finding.ID ||
				possibleDuplicateContains(updated, duplicate.CandidateID) {
				continue
			}
			updated = append(updated, duplicate)
		}
		if changed {
			finding.PossibleDuplicates = updated
			if finding.MatchState == RepositoryMatchProvisional &&
				!repositoryPossibleDuplicatesAreAmbiguous(updated) {
				finding.MatchState = RepositoryMatchNew
			}
			finding.Version++
			finding.UpdatedAt = now
		}
		result = append(result, finding)
	}
	state.RepositoryFindings = result
	return nil
}

func mergeHistoricalRepositoryFindingLifecycle(
	left, right RepositoryFindingLifecycle,
) RepositoryFindingLifecycle {
	priority := func(value RepositoryFindingLifecycle) int {
		switch value {
		case RepositoryFindingRegressed:
			return 5
		case RepositoryFindingResolutionPending:
			return 4
		case RepositoryFindingOpen:
			return 3
		case RepositoryFindingDismissed:
			return 2
		case RepositoryFindingResolved:
			return 1
		default:
			return 0
		}
	}
	if priority(right) > priority(left) {
		return right
	}
	return left
}

func mergeHistoricalValidationSnapshot(target *RepositoryFinding, source RepositoryFinding) {
	if target == nil {
		return
	}
	if historicalValidationRank(source.ValidationState) <=
		historicalValidationRank(target.ValidationState) {
		return
	}
	target.ValidationState = source.ValidationState
	target.FixCommitSHA = source.FixCommitSHA
	target.FixCommitTime = source.FixCommitTime
	target.FirstContainingTag = source.FirstContainingTag
}

func historicalValidationRank(state RepositoryFindingValidationState) int {
	switch state {
	case RepositoryValidationConfirmed:
		return 5
	case RepositoryValidationNotFixed:
		return 4
	case RepositoryValidationInconclusive:
		return 3
	case RepositoryValidationFailed:
		return 2
	case RepositoryValidationRunning, RepositoryValidationPending:
		return 1
	default:
		return 0
	}
}

func validateHistoricalDeduplicationProfileSnapshot(
	snapshot HistoricalDeduplicationProfileSnapshot,
) error {
	if (strings.TrimSpace(snapshot.ProfileID) == "") != (snapshot.ProfileVersion == 0) ||
		(snapshot.ProfileID != "" &&
			(!validProfileID(strings.TrimSpace(snapshot.ProfileID)) || snapshot.ProfileVersion < 1)) ||
		!validBoundedText(strings.TrimSpace(snapshot.ReviewerModel), 256) ||
		!validBoundedText(strings.TrimSpace(snapshot.DeduplicationModel), 256) ||
		!validOptionalLifecycleText(strings.TrimSpace(snapshot.AccountRef), 256) ||
		!validOptionalLifecycleText(strings.TrimSpace(snapshot.AccountModelRevision), 256) ||
		snapshot.SimilarityThreshold < 0 || snapshot.SimilarityThreshold > 100 ||
		snapshot.CandidateLimit < 0 || snapshot.CandidateLimit > DeduplicationMaximumShortlist {
		return errors.New("invalid historical deduplication profile snapshot")
	}
	return nil
}

func validateHistoricalDeduplicationReplay(state RepositoryState) error {
	replay := state.HistoricalDeduplication
	if !replay.Required && replay.Status == "" {
		return nil
	}
	if replay.Attempts < 0 || replay.UpdatedAt.IsZero() ||
		(replay.Status != HistoricalDeduplicationPending &&
			replay.Status != HistoricalDeduplicationReplaying &&
			replay.Status != HistoricalDeduplicationMerging &&
			replay.Status != HistoricalDeduplicationFailed &&
			replay.Status != HistoricalDeduplicationCompleted) {
		return errors.New("invalid historical deduplication replay")
	}
	if replay.Status == HistoricalDeduplicationCompleted {
		if replay.Required || replay.MergeLease.ID != "" || replay.FailurePhase != "" {
			return errors.New("invalid completed historical deduplication replay")
		}
		return nil
	}
	if !replay.Required {
		return errors.New("invalid inactive historical deduplication replay")
	}
	if replay.Status == HistoricalDeduplicationFailed {
		phase := HistoricalDeduplicationFailurePhaseForState(state)
		if phase != HistoricalDeduplicationFailureSetup &&
			phase != HistoricalDeduplicationFailureProcessing &&
			phase != HistoricalDeduplicationFailureMerge {
			return errors.New("invalid historical deduplication failure phase")
		}
	} else if replay.FailurePhase != "" {
		return errors.New("inactive historical deduplication failure phase")
	}
	if replay.Status == HistoricalDeduplicationReplaying ||
		replay.Status == HistoricalDeduplicationMerging {
		if err := validateHistoricalDeduplicationProfileSnapshot(replay.ProfileSnapshot); err != nil {
			return err
		}
	}
	if replay.Status == HistoricalDeduplicationMerging {
		groups, err := normalizeHistoricalDeduplicationMergeGroups(replay.MergeLease.Groups)
		if err != nil || replay.MergeLease.ID == "" || replay.MergeLease.AcquiredAt.IsZero() ||
			!reflect.DeepEqual(groups, replay.MergeLease.Groups) {
			return errors.New("invalid historical deduplication merge lease")
		}
	} else if replay.MergeLease.ID != "" || len(replay.MergeLease.Groups) != 0 ||
		!replay.MergeLease.AcquiredAt.IsZero() {
		return errors.New("inactive historical deduplication replay has a merge lease")
	}
	if !validOptionalLifecycleText(replay.Error, 1024) {
		return fmt.Errorf("invalid historical deduplication replay error")
	}
	return nil
}
