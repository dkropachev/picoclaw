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

const maxHistoricalDeduplicationMergeGroups = 100_000

var (
	// ErrHistoricalDeduplicationInProgress is returned by merge-sensitive
	// repository-finding mutations while the narrow historical merge lease is
	// active. Reads, reviews, and campaign finding ingestion do not use this
	// fence.
	ErrHistoricalDeduplicationInProgress   = errors.New("historical deduplication in progress")
	ErrHistoricalDeduplicationNotQuiescent = errors.New(
		"historical deduplication is waiting for downstream work to quiesce",
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

// HistoricalDeduplicationProfileSnapshot freezes the same assigned profile
// policy and account/model revision as a live campaign. A retry deliberately
// clears this snapshot so intervening profile changes are observed.
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

// HistoricalDeduplicationReplay is the compact durable replay marker. It does
// not contain model output. Model work is restartable; only the exact merge
// targets and their versions are retained while the narrow fence is active.
type HistoricalDeduplicationReplay struct {
	Required        bool                                   `json:"required"`
	Status          HistoricalDeduplicationReplayStatus    `json:"status,omitempty"`
	ProfileSnapshot HistoricalDeduplicationProfileSnapshot `json:"profile_snapshot,omitempty"`
	SnapshotVersion int64                                  `json:"snapshot_version,omitempty"`
	Attempts        int                                    `json:"attempts,omitempty"`
	Error           string                                 `json:"error,omitempty"`
	MergeLease      HistoricalDeduplicationMergeLease      `json:"merge_lease,omitempty"`
	UpdatedAt       time.Time                              `json:"updated_at,omitempty"`
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

type HistoricalDeduplicationQuiescence struct {
	IssueGenerations int `json:"issue_generations"`
	Publications     int `json:"publications"`
	Mappings         int `json:"mappings"`
	Validations      int `json:"validations"`
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
				contextRecord := FindingContext{
					ID: raw.ContextID, Repository: raw.Repository, CommitSHA: raw.CommitSHA,
					InventoryHash: "historical-replay", ProfileHash: "historical-replay",
					RunID: raw.RunID, Model: raw.Model, Reviewer: raw.Reviewer,
					Files: []FileRef{raw.File}, CreatedAt: raw.CreatedAt,
				}
				state.Contexts = append(state.Contexts, contextRecord)
				contextsByID[contextRecord.ID] = contextRecord
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
	boundaryID := historicalDeduplicationBoundaryID(batch.BoundaryID)
	bucket, err := DeduplicationAdmissionBucket(boundaryID, finding.File, finding.Symbol)
	if err != nil {
		return RawReviewFinding{}, DeduplicationJob{}, err
	}
	contextID := ""
	reviewer := ""
	model := ""
	for _, id := range finding.ContextIDs {
		contextRecord, exists := contexts[id]
		if !exists {
			continue
		}
		if contextID == "" {
			contextID = contextRecord.ID
		}
		if model == "" {
			model = strings.TrimSpace(contextRecord.Model)
		}
		if reviewer == "" {
			reviewer = strings.TrimSpace(contextRecord.Reviewer)
		}
	}
	if contextID == "" {
		contextID = stableID("legacy-context_", finding.ID)
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
		CampaignID: boundaryID, AdmissionBucket: bucket, InsertionOrdinal: ordinal,
		LegacyFindingID: finding.ID, Repository: finding.Repository,
		CommitSHA: finding.CommitSHA, File: finding.File, Line: finding.Line,
		Severity: finding.Severity, Title: finding.Title, Symbol: finding.Symbol,
		Message: finding.Message, Evidence: finding.Evidence, Impact: finding.Impact,
		Validation: finding.Validation, MatchHints: finding.MatchHints, FixEffort: finding.FixEffort,
		ContextID: contextID, RunID: batch.BoundaryID, AssignmentID: "historical-replay",
		Model: model, Reviewer: reviewer, State: RawFindingDeduplicationPending,
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

func historicalDeduplicationBoundaryID(value string) string {
	value = strings.TrimSpace(value)
	if value != "" && len(value) <= 256 && !strings.ContainsRune(value, 0) {
		return value
	}
	return stableID("rrb_", value)
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
		if raw.LegacyFindingID == "" || !strings.HasPrefix(raw.ID, "rrw_") {
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
			if !exists || raw.LegacyFindingID == "" || !strings.HasPrefix(raw.ID, "rrw_") {
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
		if err := resetHistoricalDeduplicationModelWork(&state, snapshot, now); err != nil {
			return RepositoryState{}, HistoricalDeduplicationReplay{}, err
		}
	}
	replay.ProfileSnapshot = snapshot
	replay.SnapshotVersion = state.Version
	replay.Status = HistoricalDeduplicationReplaying
	replay.Attempts++
	replay.Error = ""
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
	historicalRawIDs := make(map[string]struct{})
	rawIndexes := make(map[string]int, len(state.RawFindings))
	for index, raw := range state.RawFindings {
		rawIndexes[raw.ID] = index
		if raw.LegacyFindingID != "" && strings.HasPrefix(raw.ID, "rrw_") {
			historicalRawIDs[raw.ID] = struct{}{}
		}
	}
	jobsByRawID := make(map[string]*DeduplicationJob, len(state.DeduplicationJobs))
	for index := range state.DeduplicationJobs {
		job := &state.DeduplicationJobs[index]
		jobsByRawID[job.RawFindingID] = job
	}
	removedDeduplicated := make(map[string]struct{})
	replacementIDs := make(map[string]string)
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
		removedDeduplicated[finding.ID] = struct{}{}
		if len(nonHistoricalRawIDs) == 0 {
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
	for repositoryIndex := range state.RepositoryFindings {
		repositoryFinding := &state.RepositoryFindings[repositoryIndex]
		changed := false
		for occurrenceIndex, occurrenceID := range repositoryFinding.ReviewFindingIDs {
			if replacementID := replacementIDs[occurrenceID]; replacementID != "" {
				repositoryFinding.ReviewFindingIDs[occurrenceIndex] = replacementID
				changed = true
			} else if _, removed := removedDeduplicated[occurrenceID]; removed {
				return ErrConflict
			}
		}
		for historyIndex := range repositoryFinding.PathSymbolHistory {
			historyFindingID := repositoryFinding.PathSymbolHistory[historyIndex].ReviewFindingID
			if replacementID := replacementIDs[historyFindingID]; replacementID != "" {
				repositoryFinding.PathSymbolHistory[historyIndex].ReviewFindingID = replacementID
				changed = true
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
			for findingIndex, findingID := range draft.FindingIDs {
				if replacementID := replacementIDs[findingID]; replacementID != "" {
					draft.FindingIDs[findingIndex] = replacementID
					changed = true
				}
			}
			if changed {
				draft.Version++
				draft.UpdatedAt = now
			}
		}
		for runIndex := range state.Runs {
			for findingIndex, findingID := range state.Runs[runIndex].FindingIDs {
				if replacementID := replacementIDs[findingID]; replacementID != "" {
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
		if job == nil || job.State == DeduplicationJobRunning {
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
			if !found || !strings.HasPrefix(raw.ID, "rrw_") || raw.LegacyFindingID == "" {
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
	if replay.Status == HistoricalDeduplicationMerging && replay.MergeLease.ID != leaseID {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, ErrConflict
	}
	now := s.clock()
	replay.Status = HistoricalDeduplicationFailed
	replay.Error = "Historical deduplication failed."
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
	replay := &state.HistoricalDeduplication
	if !replay.Required || replay.Status == HistoricalDeduplicationCompleted {
		return state, *replay, nil
	}
	if replay.Status != HistoricalDeduplicationFailed {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, ErrConflict
	}
	now := s.clock()
	replay.Status = HistoricalDeduplicationPending
	replay.ProfileSnapshot = HistoricalDeduplicationProfileSnapshot{}
	replay.SnapshotVersion = 0
	replay.Error = ""
	replay.MergeLease = HistoricalDeduplicationMergeLease{}
	replay.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, HistoricalDeduplicationReplay{}, err
	}
	return state, *replay, nil
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
		if replay.Required || replay.MergeLease.ID != "" {
			return errors.New("invalid completed historical deduplication replay")
		}
		return nil
	}
	if !replay.Required {
		return errors.New("invalid inactive historical deduplication replay")
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
