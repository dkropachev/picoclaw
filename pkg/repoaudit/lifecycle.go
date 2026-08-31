package repoaudit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

var semanticVersionTagPattern = regexp.MustCompile(
	`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`,
)

func ValidSemanticVersionTag(value string) bool {
	return semanticVersionTagPattern.MatchString(strings.TrimSpace(value))
}

const (
	RepositoryIssueSnapshotTTL      = 15 * time.Minute
	RepositoryValidationConcurrency = 4
	maxRepositoryFindings           = 100_000
	maxRepositoryMappingJobs        = 100_000
	maxRepositoryValidationJobs     = 100_000
	maxRepositoryPossibleDuplicates = 100
	maxRepositoryResolutionHistory  = 100
	maxRepositoryPathSymbolHistory  = 100_000
	maxValidationBatch              = 50
	maxValidationCandidateCommits   = 8
	maxRepositoryLifecycleTextBytes = 4096
)

var errRepositoryValidationEvidenceChanged = errors.New(
	"repository finding changed while validation was running",
)

var errRepositoryMappingUniverseChanged = errors.New(
	"repository finding candidate universe changed while mapping was running",
)

type RepositoryJobReconciliation struct {
	Repositories        int `json:"repositories"`
	MappingJobsCreated  int `json:"mapping_jobs_created"`
	MappingJobsReset    int `json:"mapping_jobs_reset"`
	ValidationJobsReset int `json:"validation_jobs_reset"`
}

type RepositoryMappingCompletion struct {
	JobID                 string                               `json:"job_id"`
	RepositoryFindingID   string                               `json:"repository_finding_id,omitempty"`
	CreateMatchState      RepositoryMatchState                 `json:"create_match_state,omitempty"`
	DefaultBranchVerified bool                                 `json:"default_branch_verified,omitempty"`
	RegressionVerified    bool                                 `json:"regression_verified,omitempty"`
	RegressionFixCommit   string                               `json:"regression_fix_commit,omitempty"`
	RegressionFindingID   string                               `json:"regression_finding_id,omitempty"`
	ExpectedUniverse      string                               `json:"expected_universe,omitempty"`
	PossibleDuplicates    []RepositoryFindingPossibleDuplicate `json:"possible_duplicates,omitempty"`
}

type RepositoryDuplicateResolution struct {
	ProvisionalID              string `json:"provisional_id"`
	CandidateID                string `json:"candidate_id"`
	Decision                   string `json:"decision"`
	ExpectedProvisionalVersion int64  `json:"expected_provisional_version"`
	ExpectedCandidateVersion   int64  `json:"expected_candidate_version,omitempty"`
}

type RepositoryValidationCompletion struct {
	JobID              string                           `json:"job_id"`
	Outcome            RepositoryFindingValidationState `json:"outcome"`
	SelectedCommitSHA  string                           `json:"selected_commit_sha,omitempty"`
	FixCommitTime      time.Time                        `json:"fix_commit_time,omitempty"`
	FirstContainingTag string                           `json:"first_containing_tag,omitempty"`
	Summary            string                           `json:"summary,omitempty"`
	Error              string                           `json:"error,omitempty"`
	FailureCode        RepositoryValidationFailureCode  `json:"failure_code,omitempty"`
}

type RepositoryIssueSnapshotUpdate struct {
	RepositoryFindingID string                      `json:"repository_finding_id"`
	ExpectedVersion     int64                       `json:"expected_version,omitempty"`
	ExternalID          string                      `json:"external_id,omitempty"`
	URL                 string                      `json:"url,omitempty"`
	Origin              IssueDraftOrigin            `json:"origin,omitempty"`
	State               RepositoryFindingIssueState `json:"state"`
	Title               string                      `json:"title,omitempty"`
	SnapshotAt          time.Time                   `json:"snapshot_at,omitempty"`
}

// AcquireValidationSlot enforces the workspace-wide four-validator limit
// across launcher processes. The OS releases a held slot after process loss.
func (s Store) AcquireValidationSlot(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		for slot := 0; slot < RepositoryValidationConcurrency; slot++ {
			lockPath := fmt.Sprintf("%s.validation-slot-%02d.lock", s.root, slot)
			release, acquired, err := tryLockRepositoryReviewIssueFile(lockPath)
			if err != nil {
				return nil, err
			}
			if acquired {
				return release, nil
			}
		}
		timer := time.NewTimer(issueGenerationSlotRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func migrateRepositoryState(state *RepositoryState) (bool, error) {
	if state == nil {
		return false, errors.New("repository review state is required")
	}
	migrated := false
	// Historical deduplication was introduced by schema 4. The schema 5
	// attribution-only migration must not requeue already migrated findings.
	legacyDeduplicationSchema := state.SchemaVersion > 0 && state.SchemaVersion < 4
	switch state.SchemaVersion {
	case 1, 2, 3, 4:
		state.SchemaVersion = SchemaVersion
		migrated = true
	case SchemaVersion:
	default:
		return false, errors.New("invalid repository review state")
	}
	if state.Files == nil {
		state.Files = make(map[string]ReviewedFile)
		migrated = true
	}
	if state.Unsupported == nil {
		state.Unsupported = make(map[string]UnsupportedFile)
		migrated = true
	}
	if state.ReviewAttempts == nil {
		state.ReviewAttempts = make(map[string]int)
		migrated = true
	}
	if state.ReviewAttemptIdentities == nil {
		state.ReviewAttemptIdentities = make(map[string]string)
		migrated = true
	}
	if state.Findings == nil {
		state.Findings = []Finding{}
		migrated = true
	}
	if state.RawFindings == nil {
		state.RawFindings = []RawReviewFinding{}
		migrated = true
	}
	if state.DeduplicatedFindings == nil {
		state.DeduplicatedFindings = []DeduplicatedReviewFinding{}
		migrated = true
	}
	if state.DeduplicationJobs == nil {
		state.DeduplicationJobs = []DeduplicationJob{}
		migrated = true
	}
	rawIDsMigrated, rawIDsErr := migrateRepositoryReviewRawFindingIDs(state)
	if rawIDsErr != nil {
		return false, rawIDsErr
	}
	if rawIDsMigrated {
		migrated = true
	}
	if reconcileFindingsProcessingCounters(state) {
		migrated = true
	}
	if legacyDeduplicationSchema && (len(state.Findings) > 0 || len(state.RepositoryFindings) > 0) &&
		!state.HistoricalDeduplication.Required {
		state.HistoricalDeduplication.Required = true
		state.HistoricalDeduplication.Status = HistoricalDeduplicationPending
		state.HistoricalDeduplication.UpdatedAt = state.UpdatedAt.UTC()
		migrated = true
	}
	if state.Contexts == nil {
		state.Contexts = []FindingContext{}
		migrated = true
	}
	if state.Runs == nil {
		state.Runs = []ReviewRun{}
		migrated = true
	}
	if state.FileAttributions == nil {
		state.FileAttributions = []RepositoryReviewFileAttribution{}
		migrated = true
	}
	if state.IssueDrafts == nil {
		state.IssueDrafts = []IssueDraft{}
		migrated = true
	}
	if state.RepositoryFindings == nil {
		state.RepositoryFindings = []RepositoryFinding{}
		migrated = true
	}
	if state.MappingJobs == nil {
		state.MappingJobs = []RepositoryMappingJob{}
		migrated = true
	}
	if state.ValidationJobs == nil {
		state.ValidationJobs = []RepositoryValidationJob{}
		migrated = true
	}
	if state.CurrentCampaign != nil && state.CurrentCampaign.Paths == nil {
		state.CurrentCampaign.Paths = make(map[string]RepositoryReviewCampaignPathCoverage)
		// A missing legacy path ledger can never prove exact coverage.
		state.CurrentCampaign.Exact = false
		migrated = true
	}
	historyMigrated, historyErr := migrateRepositoryReviewCampaignHistory(state)
	if historyErr != nil {
		return false, historyErr
	}
	if historyMigrated {
		migrated = true
	}
	if backfillRepositoryFindingEvidence(state) {
		migrated = true
	}
	for index := range state.MappingJobs {
		job := &state.MappingJobs[index]
		if mappingAdjudicationEmpty(job.Adjudication) || job.CandidateUniverse != "" {
			continue
		}
		if job.State == RepositoryMappingCompleted {
			job.CandidateUniverse = repositoryMatchingUniverseFingerprint(state.RepositoryFindings)
		} else {
			job.Adjudication = RepositoryMappingAdjudication{}
			job.Error = ""
		}
		migrated = true
	}
	for index := range state.ValidationJobs {
		job := &state.ValidationJobs[index]
		if job.FindingVersion > 0 {
			continue
		}
		if findingIndex := repositoryFindingIndexByID(
			state.RepositoryFindings, job.RepositoryFindingID,
		); findingIndex >= 0 {
			job.FindingVersion = state.RepositoryFindings[findingIndex].Version
			if !repositoryValidationTerminal(job.State) {
				job.CandidateCommits = nil
			}
			migrated = true
		}
	}
	for index := range state.Findings {
		finding := &state.Findings[index]
		if finding.PostResolutionVerified &&
			(finding.PostResolutionFixCommit == "" || finding.PostResolutionFindingID == "") {
			finding.PostResolutionVerified = false
			finding.PostResolutionFixCommit = ""
			finding.PostResolutionFindingID = ""
			migrated = true
		}
	}
	return migrated, nil
}

// migrateRepositoryReviewRawFindingIDs repairs both pre-canonical identity
// shapes. Native raw findings used rrf_* while the compatibility Record path
// used rrl_* and promoted an rfn_* parent. Raw suffixes remain stable. An old
// compatibility parent is rewritten to the rdf_* identity derived from its
// migrated rrw_* source, while its rfn_* identity is retained only as the raw
// alias used by old bookmarks.
func migrateRepositoryReviewRawFindingIDs(state *RepositoryState) (bool, error) {
	if state == nil || len(state.RawFindings) == 0 {
		return false, nil
	}
	rawReplacements := make(map[string]string)
	parentReplacements := make(map[string]string)
	compatibilityAliases := make(map[int]string)
	seen := make(map[string]struct{}, len(state.RawFindings))
	for index, raw := range state.RawFindings {
		id := raw.ID
		legacyCompatibilityRaw := strings.HasPrefix(id, "rrl_")
		if strings.HasPrefix(id, "rrf_") || legacyCompatibilityRaw {
			id = "rrw_" + id[len("rrf_"):]
			rawReplacements[raw.ID] = id
		}
		if _, duplicate := seen[id]; duplicate {
			return false, errors.New("repository review raw finding ID migration conflicts")
		}
		seen[id] = struct{}{}
		oldParentID := strings.TrimSpace(raw.DeduplicatedFindingID)
		compatibilityRecord := legacyCompatibilityRaw ||
			strings.HasPrefix(raw.AssignmentID, "record-")
		if !compatibilityRecord || !strings.HasPrefix(oldParentID, "rfn_") {
			continue
		}
		if raw.LegacyFindingID != "" && raw.LegacyFindingID != oldParentID {
			return false, errors.New("repository review compatibility raw alias conflicts")
		}
		canonicalParentID := stableID("rdf_", id)
		if existing := parentReplacements[oldParentID]; existing != "" &&
			existing != canonicalParentID {
			return false, errors.New("repository review compatibility parent migration is ambiguous")
		}
		parentReplacements[oldParentID] = canonicalParentID
		compatibilityAliases[index] = oldParentID
	}
	if len(rawReplacements) == 0 && len(parentReplacements) == 0 {
		return false, nil
	}
	replaceRaw := func(id string) string {
		if replacement := rawReplacements[id]; replacement != "" {
			return replacement
		}
		return id
	}
	replaceParent := func(id string) string {
		if replacement := parentReplacements[id]; replacement != "" {
			return replacement
		}
		return id
	}
	if err := validateRepositoryReviewParentIdentityMigration(state, parentReplacements); err != nil {
		return false, err
	}

	rawDigests := make(map[string]string, len(state.RawFindings))
	for index := range state.RawFindings {
		raw := &state.RawFindings[index]
		raw.ID = replaceRaw(raw.ID)
		if alias := compatibilityAliases[index]; alias != "" {
			raw.LegacyFindingID = alias
		}
		raw.DeduplicatedFindingID = replaceParent(raw.DeduplicatedFindingID)
		for historyIndex := range raw.History {
			raw.History[historyIndex].DeduplicatedFindingID = replaceParent(
				raw.History[historyIndex].DeduplicatedFindingID,
			)
		}
		if alias := compatibilityAliases[index]; alias != "" {
			raw.DiagnosisDigest = RawReviewFindingDiagnosisDigest(*raw)
		}
		rawDigests[raw.ID] = raw.DiagnosisDigest
	}
	for index := range state.DeduplicationJobs {
		job := &state.DeduplicationJobs[index]
		job.RawFindingID = replaceRaw(
			state.DeduplicationJobs[index].RawFindingID,
		)
		job.Decision.CandidateID = replaceParent(job.Decision.CandidateID)
		for candidateIndex := range job.CandidateVersions {
			job.CandidateVersions[candidateIndex].CandidateID = replaceParent(
				job.CandidateVersions[candidateIndex].CandidateID,
			)
		}
		for candidateIndex := range job.ShortlistedScores {
			job.ShortlistedScores[candidateIndex].CandidateID = replaceParent(
				job.ShortlistedScores[candidateIndex].CandidateID,
			)
		}
	}
	for index := range state.DeduplicatedFindings {
		finding := &state.DeduplicatedFindings[index]
		oldFindingID := finding.ID
		finding.ID = replaceParent(oldFindingID)
		for sourceIndex := range finding.RawSourceIDs {
			finding.RawSourceIDs[sourceIndex] = replaceRaw(finding.RawSourceIDs[sourceIndex])
		}
		for historyIndex := range finding.History {
			finding.History[historyIndex].RawFindingID = replaceRaw(
				finding.History[historyIndex].RawFindingID,
			)
		}
		if finding.ID != oldFindingID && len(finding.RawSourceIDs) > 0 {
			finding.DiagnosisDigest = rawDigests[finding.RawSourceIDs[0]]
		}
	}
	for index := range state.Findings {
		state.Findings[index].ID = replaceParent(state.Findings[index].ID)
		state.Findings[index].PostResolutionFindingID = replaceParent(
			state.Findings[index].PostResolutionFindingID,
		)
		for rawIndex := range state.Findings[index].RawFindingIDs {
			state.Findings[index].RawFindingIDs[rawIndex] = replaceRaw(
				state.Findings[index].RawFindingIDs[rawIndex],
			)
		}
	}
	for index := range state.MappingJobs {
		job := &state.MappingJobs[index]
		oldFindingID := job.ReviewFindingID
		job.ReviewFindingID = replaceParent(oldFindingID)
		if job.ReviewFindingID != oldFindingID {
			job.ID = mappingJobID(job.ReviewFindingID)
		}
	}
	for index := range state.RepositoryFindings {
		finding := &state.RepositoryFindings[index]
		for occurrenceIndex := range finding.ReviewFindingIDs {
			finding.ReviewFindingIDs[occurrenceIndex] = replaceParent(
				finding.ReviewFindingIDs[occurrenceIndex],
			)
		}
		for historyIndex := range finding.PathSymbolHistory {
			finding.PathSymbolHistory[historyIndex].ReviewFindingID = replaceParent(
				finding.PathSymbolHistory[historyIndex].ReviewFindingID,
			)
		}
	}
	for index := range state.IssueDrafts {
		for findingIndex := range state.IssueDrafts[index].FindingIDs {
			state.IssueDrafts[index].FindingIDs[findingIndex] = replaceParent(
				state.IssueDrafts[index].FindingIDs[findingIndex],
			)
		}
	}
	for index := range state.Runs {
		for findingIndex := range state.Runs[index].FindingIDs {
			id := replaceRaw(state.Runs[index].FindingIDs[findingIndex])
			state.Runs[index].FindingIDs[findingIndex] = replaceParent(id)
		}
	}
	if state.ActiveReviewRun != nil {
		for findingIndex := range state.ActiveReviewRun.FindingIDs {
			id := replaceRaw(state.ActiveReviewRun.FindingIDs[findingIndex])
			state.ActiveReviewRun.FindingIDs[findingIndex] = replaceParent(id)
		}
	}
	return true, nil
}

func validateRepositoryReviewParentIdentityMigration(
	state *RepositoryState,
	replacements map[string]string,
) error {
	if len(replacements) == 0 {
		return nil
	}
	validateUnique := func(ids []string, kind string) error {
		seen := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			if replacement := replacements[id]; replacement != "" {
				id = replacement
			}
			if _, duplicate := seen[id]; duplicate {
				return fmt.Errorf("repository review %s identity migration conflicts", kind)
			}
			seen[id] = struct{}{}
		}
		return nil
	}
	deduplicatedIDs := make([]string, 0, len(state.DeduplicatedFindings))
	for _, finding := range state.DeduplicatedFindings {
		deduplicatedIDs = append(deduplicatedIDs, finding.ID)
	}
	if err := validateUnique(deduplicatedIDs, "deduplicated finding"); err != nil {
		return err
	}
	projectionIDs := make([]string, 0, len(state.Findings))
	for _, finding := range state.Findings {
		projectionIDs = append(projectionIDs, finding.ID)
	}
	if err := validateUnique(projectionIDs, "finding projection"); err != nil {
		return err
	}
	mappingIDs := make([]string, 0, len(state.MappingJobs))
	for _, job := range state.MappingJobs {
		findingID := job.ReviewFindingID
		if replacement := replacements[findingID]; replacement != "" {
			findingID = replacement
		}
		mappingIDs = append(mappingIDs, mappingJobID(findingID))
	}
	if err := validateUnique(mappingIDs, "mapping job"); err != nil {
		return err
	}
	for oldID := range replacements {
		if deduplicatedFindingIndexByID(state.DeduplicatedFindings, oldID) < 0 ||
			findingIndexByID(state.Findings, oldID) < 0 {
			return errors.New("repository review compatibility parent migration is incomplete")
		}
	}
	return nil
}

func backfillRepositoryFindingEvidence(state *RepositoryState) bool {
	if state == nil || len(state.RepositoryFindings) == 0 {
		return false
	}
	occurrences := make(map[string]Finding, len(state.Findings))
	occurrenceIndexes := make(map[string]int, len(state.Findings))
	for index, finding := range state.Findings {
		occurrences[finding.ID] = finding
		occurrenceIndexes[finding.ID] = index
	}
	changed := false
	for aggregateIndex := range state.RepositoryFindings {
		aggregate := &state.RepositoryFindings[aggregateIndex]
		aggregateChanged := false
		previousHistory := append([]RepositoryFindingPathSymbol(nil), aggregate.PathSymbolHistory...)
		sortRepositoryPathSymbolHistory(aggregate.PathSymbolHistory)
		if !reflect.DeepEqual(previousHistory, aggregate.PathSymbolHistory) {
			aggregateChanged = true
		}
		expectedMatchState := aggregate.MatchState
		if repositoryPossibleDuplicatesAreAmbiguous(aggregate.PossibleDuplicates) {
			expectedMatchState = RepositoryMatchProvisional
		} else if expectedMatchState == RepositoryMatchProvisional {
			expectedMatchState = RepositoryMatchNew
		}
		if aggregate.MatchState != expectedMatchState {
			aggregate.MatchState = expectedMatchState
			aggregateChanged = true
		}
		for _, occurrenceID := range aggregate.ReviewFindingIDs {
			if index, ok := occurrenceIndexes[occurrenceID]; ok &&
				state.Findings[index].RepositoryMatchState != expectedMatchState {
				state.Findings[index].RepositoryMatchState = expectedMatchState
				state.Findings[index].Version++
				aggregateChanged = true
			}
		}
		for historyIndex := range aggregate.PathSymbolHistory {
			history := &aggregate.PathSymbolHistory[historyIndex]
			if occurrence, ok := occurrences[history.ReviewFindingID]; ok &&
				occurrence.DefaultBranchVerified && !history.DefaultBranchVerified {
				history.DefaultBranchVerified = true
				aggregateChanged = true
			}
		}
		for _, occurrenceID := range aggregate.ReviewFindingIDs {
			occurrence, ok := occurrences[occurrenceID]
			if !ok {
				continue
			}
			if matchHintsEmpty(aggregate.MatchHints) && !matchHintsEmpty(occurrence.MatchHints) {
				aggregate.MatchHints = occurrence.MatchHints
				aggregateChanged = true
			}
			if aggregate.FixEffort == (FixEffort{}) && occurrence.FixEffort != (FixEffort{}) {
				aggregate.FixEffort = occurrence.FixEffort
				aggregateChanged = true
			}
		}
		if aggregateChanged {
			aggregate.Version++
			changed = true
		}
	}
	return changed
}

func normalizeRecordBranchProvenance(request *RecordRequest) error {
	if request == nil {
		return ErrInvalidPlan
	}
	if request.TargetBranch == "" && request.AdvertisedDefaultBranch == "" &&
		!request.TargetIsDefault {
		request.TargetBranch = request.Plan.TargetBranch
		request.AdvertisedDefaultBranch = request.Plan.AdvertisedDefaultBranch
		request.TargetIsDefault = request.Plan.TargetIsDefault
	}
	request.TargetBranch = strings.TrimSpace(request.TargetBranch)
	request.AdvertisedDefaultBranch = strings.TrimSpace(request.AdvertisedDefaultBranch)
	if request.TargetBranch != strings.TrimSpace(request.Plan.TargetBranch) ||
		request.AdvertisedDefaultBranch != strings.TrimSpace(request.Plan.AdvertisedDefaultBranch) ||
		request.TargetIsDefault != request.Plan.TargetIsDefault {
		return fmt.Errorf("%w: branch provenance does not match the immutable plan", ErrInvalidPlan)
	}
	if request.TargetBranch == "" && request.AdvertisedDefaultBranch == "" {
		// A checkout acquired from an advertised default can be detached at its
		// exact admitted commit, leaving both human branch names unavailable.
		// TargetIsDefault still preserves the server-verified relationship.
		return nil
	}
	if request.TargetBranch != "" && request.AdvertisedDefaultBranch == "" &&
		!request.TargetIsDefault {
		target, err := NormalizeRepositoryReviewBranch(request.TargetBranch)
		if err != nil || target == "" {
			return fmt.Errorf("%w: target branch is invalid", ErrInvalidPlan)
		}
		request.TargetBranch = target
		return nil
	}
	target, err := NormalizeRepositoryReviewBranch(request.TargetBranch)
	if err != nil || target == "" {
		return fmt.Errorf("%w: target branch is invalid", ErrInvalidPlan)
	}
	advertised, err := NormalizeRepositoryReviewBranch(request.AdvertisedDefaultBranch)
	if err != nil || advertised == "" {
		return fmt.Errorf("%w: advertised default branch is invalid", ErrInvalidPlan)
	}
	request.TargetBranch = target
	request.AdvertisedDefaultBranch = advertised
	if request.TargetIsDefault != (target == advertised) {
		return fmt.Errorf("%w: default-branch provenance is contradictory", ErrInvalidPlan)
	}
	return nil
}

func mappingJobID(reviewFindingID string) string {
	return stableID("rmj_", strings.TrimSpace(reviewFindingID))
}

func ensureMappingJobsForFindings(state *RepositoryState, findingIDs []string, now time.Time) int {
	if state == nil {
		return 0
	}
	existing := make(map[string]struct{}, len(state.MappingJobs))
	for _, job := range state.MappingJobs {
		existing[job.ReviewFindingID] = struct{}{}
	}
	byID := make(map[string]Finding, len(state.Findings))
	for _, finding := range state.Findings {
		byID[finding.ID] = finding
	}
	// Once the raw/deduplicated ledger is in use, compatibility Finding
	// projections are not themselves admission authority. Only a decided
	// DeduplicatedReviewFinding may enter repository mapping.
	deduplicated := make(map[string]struct{}, len(state.DeduplicatedFindings))
	for _, finding := range state.DeduplicatedFindings {
		deduplicated[finding.ID] = struct{}{}
	}
	created := 0
	for _, findingID := range findingIDs {
		finding, found := byID[strings.TrimSpace(findingID)]
		if !found || finding.DeduplicationPending || finding.RepositoryFindingID != "" {
			continue
		}
		if len(state.RawFindings) > 0 || state.HistoricalDeduplication.Required {
			if _, admitted := deduplicated[finding.ID]; !admitted {
				continue
			}
		}
		if _, found := existing[finding.ID]; found {
			continue
		}
		state.MappingJobs = append(state.MappingJobs, RepositoryMappingJob{
			ID: mappingJobID(finding.ID), ReviewFindingID: finding.ID,
			State: RepositoryMappingPending, CreatedAt: now, UpdatedAt: now,
		})
		existing[finding.ID] = struct{}{}
		created++
	}
	return created
}

// ReconcileJobs is the explicit startup recovery boundary. Ordinary reads do
// not steal running jobs from a live worker.
func (s Store) ReconcileJobs(ctx context.Context) (RepositoryJobReconciliation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	states, err := s.List()
	if err != nil {
		return RepositoryJobReconciliation{}, err
	}
	result := RepositoryJobReconciliation{Repositories: len(states)}
	for _, listed := range states {
		if err := ctx.Err(); err != nil {
			return RepositoryJobReconciliation{}, err
		}
		created, mappingReset, validationReset, err := s.reconcileRepositoryJobs(listed.Repository)
		if err != nil {
			return RepositoryJobReconciliation{}, err
		}
		result.MappingJobsCreated += created
		result.MappingJobsReset += mappingReset
		result.ValidationJobsReset += validationReset
	}
	return result, nil
}

func (s Store) reconcileRepositoryJobs(repository string) (int, int, int, error) {
	unlock, err := s.lock(repository)
	if err != nil {
		return 0, 0, 0, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return 0, 0, 0, err
	}
	now := s.clock()
	historicalMergeReleased := false
	if HistoricalDeduplicationMergeInProgress(state) {
		// The controller lease proves that no previous replay process remains
		// live. A crash between narrow-lease acquisition and completion is a
		// terminal attempt failure; release the fence and require an explicit
		// retry to take a fresh snapshot.
		state.HistoricalDeduplication.Status = HistoricalDeduplicationFailed
		state.HistoricalDeduplication.Error = "Historical deduplication was interrupted."
		state.HistoricalDeduplication.MergeLease = HistoricalDeduplicationMergeLease{}
		state.HistoricalDeduplication.UpdatedAt = now
		historicalMergeReleased = true
	}
	ids := make([]string, 0, len(state.Findings))
	rawProjections := make(map[string]struct{}, len(state.RawFindings))
	for _, raw := range state.RawFindings {
		if raw.LegacyFindingID != "" {
			rawProjections[raw.LegacyFindingID] = struct{}{}
		}
	}
	for _, finding := range state.Findings {
		if finding.RepositoryFindingID == "" {
			if _, undecidedRawProjection := rawProjections[finding.ID]; undecidedRawProjection {
				continue
			}
			ids = append(ids, finding.ID)
		}
	}
	created := ensureMappingJobsForFindings(&state, ids, now)
	mappingReset := 0
	for index := range state.MappingJobs {
		job := &state.MappingJobs[index]
		if job.State != RepositoryMappingRunning {
			continue
		}
		job.State = RepositoryMappingPending
		job.ReservedAt = time.Time{}
		job.Error = "Run finding status processing was interrupted."
		job.UpdatedAt = now
		mappingReset++
	}
	validationReset := 0
	for index := range state.ValidationJobs {
		job := &state.ValidationJobs[index]
		if job.State != RepositoryValidationRunning {
			continue
		}
		job.State = RepositoryValidationPending
		job.Failure = nil
		job.ReservedAt = time.Time{}
		job.UpdatedAt = now
		if findingIndex := repositoryFindingIndexByID(
			state.RepositoryFindings,
			job.RepositoryFindingID,
		); findingIndex >= 0 {
			finding := &state.RepositoryFindings[findingIndex]
			if job.FindingVersion != finding.Version {
				job.CandidateCommits = nil
			}
			finding.ValidationState = RepositoryValidationPending
			finding.Version++
			finding.UpdatedAt = now
			job.FindingVersion = finding.Version
		}
		validationReset++
	}
	if created == 0 && mappingReset == 0 && validationReset == 0 && !historicalMergeReleased {
		return 0, 0, 0, nil
	}
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return 0, 0, 0, err
	}
	return created, mappingReset, validationReset, nil
}

// RetryRunFindingStatus resets explicitly selected pending association work.
// Run findings themselves remain immutable; the durable job is reset under the
// repository lock so the ordinary worker can safely evaluate a fresh candidate
// universe while preserving its admitted model/profile snapshot.
func (s Store) RetryRunFindingStatus(
	repository string,
	findingIDs []string,
) (RepositoryState, []Finding, error) {
	if len(findingIDs) == 0 || len(findingIDs) > 200 {
		return RepositoryState{}, nil, errors.New("one to 200 run finding IDs are required")
	}
	repository = strings.TrimSpace(repository)
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, nil, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, nil, err
	}
	selected, ids, err := selectedFindings(state.Findings, findingIDs)
	if err != nil {
		return RepositoryState{}, nil, err
	}
	jobIndexes := make(map[string]int, len(state.MappingJobs))
	for index, job := range state.MappingJobs {
		jobIndexes[job.ReviewFindingID] = index
	}
	// Validate the complete selection before mutating the in-memory state so a
	// mixed eligible/ineligible request is rejected atomically.
	for index, finding := range selected {
		if finding.RepositoryFindingID != "" {
			return RepositoryState{}, nil, ErrConflict
		}
		jobIndex, found := jobIndexes[ids[index]]
		if !found || state.MappingJobs[jobIndex].State != RepositoryMappingPending ||
			state.MappingJobs[jobIndex].Attempts < RepositoryRunFindingStatusAttemptLimit {
			return RepositoryState{}, nil, ErrConflict
		}
	}
	now := s.clock()
	for _, findingID := range ids {
		job := &state.MappingJobs[jobIndexes[findingID]]
		job.Attempts = 0
		job.Error = ""
		job.ReservedAt = time.Time{}
		job.Adjudication = RepositoryMappingAdjudication{}
		job.CandidateUniverse = ""
		job.UpdatedAt = now
	}
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, nil, err
	}
	return state, selected, nil
}

func (s Store) ClaimMappingJob(
	repository, jobID string,
	snapshot RepositoryMappingModelSnapshot,
) (RepositoryState, RepositoryMappingJob, Finding, bool, error) {
	if err := validateMappingModelSnapshot(snapshot); err != nil {
		return RepositoryState{}, RepositoryMappingJob{}, Finding{}, false, err
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, RepositoryMappingJob{}, Finding{}, false, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, RepositoryMappingJob{}, Finding{}, false, err
	}
	if err := HistoricalDeduplicationMutationAllowed(state); err != nil {
		return RepositoryState{}, RepositoryMappingJob{}, Finding{}, false, err
	}
	jobIndex := mappingJobIndexByID(state.MappingJobs, jobID)
	if jobIndex < 0 {
		return RepositoryState{}, RepositoryMappingJob{}, Finding{}, false, os.ErrNotExist
	}
	job := &state.MappingJobs[jobIndex]
	if len(state.RawFindings) > 0 || state.HistoricalDeduplication.Required {
		admitted := false
		for _, finding := range state.DeduplicatedFindings {
			if finding.ID == job.ReviewFindingID {
				admitted = true
				break
			}
		}
		if !admitted {
			return RepositoryState{}, RepositoryMappingJob{}, Finding{}, false,
				errors.New("repository mapping requires a deduplicated finding")
		}
	}
	findingIndex := findingIndexByID(state.Findings, job.ReviewFindingID)
	if findingIndex < 0 {
		return RepositoryState{}, RepositoryMappingJob{}, Finding{}, false, errors.New(
			"mapping job review finding is missing",
		)
	}
	finding := &state.Findings[findingIndex]
	if state.HistoricalDeduplication.Required &&
		historicalReplayDeduplicatedFinding(state, job.ReviewFindingID) {
		// Replay-derived occurrences are mapped only after the historical
		// identity merge completes. New campaign findings in the same ledger
		// remain eligible throughout replay.
		return state, *job, *finding, false, nil
	}
	if finding.RepositoryFindingID != "" {
		if job.State == RepositoryMappingCompleted && job.RepositoryFindingID == finding.RepositoryFindingID {
			return state, *job, *finding, false, nil
		}
		return RepositoryState{}, RepositoryMappingJob{}, Finding{}, false, ErrConflict
	}
	if job.State == RepositoryMappingRunning || job.State == RepositoryMappingCompleted {
		return state, *job, *finding, false, nil
	}
	if job.State != RepositoryMappingPending {
		return RepositoryState{}, RepositoryMappingJob{}, Finding{}, false, ErrConflict
	}
	if job.Attempts >= RepositoryRunFindingStatusAttemptLimit {
		return state, *job, *finding, false, nil
	}
	if mappingModelSnapshotEmpty(job.ModelSnapshot) {
		job.ModelSnapshot = snapshot
	} else if !mappingModelSnapshotsEqual(job.ModelSnapshot, snapshot) && !mappingModelSnapshotEmpty(snapshot) {
		return RepositoryState{}, RepositoryMappingJob{}, Finding{}, false, ErrConflict
	}
	now := s.clock()
	job.State = RepositoryMappingRunning
	job.Attempts++
	job.Error = ""
	job.ReservedAt = now
	job.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, RepositoryMappingJob{}, Finding{}, false, err
	}
	return state, *job, *finding, true, nil
}

func historicalReplayDeduplicatedFinding(state RepositoryState, findingID string) bool {
	index := deduplicatedFindingIndexByID(state.DeduplicatedFindings, findingID)
	if index < 0 {
		return false
	}
	rawIDs := make(map[string]struct{}, len(state.DeduplicatedFindings[index].RawSourceIDs))
	for _, rawID := range state.DeduplicatedFindings[index].RawSourceIDs {
		rawIDs[rawID] = struct{}{}
	}
	for _, raw := range state.RawFindings {
		if _, selected := rawIDs[raw.ID]; selected && HistoricalDeduplicationRawFinding(raw) {
			return true
		}
	}
	return false
}

func (s Store) SaveMappingAdjudication(
	repository, jobID string,
	adjudication RepositoryMappingAdjudication,
	candidateUniverses ...string,
) (RepositoryState, RepositoryMappingJob, error) {
	adjudication = normalizeMappingAdjudication(adjudication)
	candidateUniverse := ""
	if len(candidateUniverses) > 1 {
		return RepositoryState{}, RepositoryMappingJob{}, errors.New("invalid mapping candidate universe")
	}
	if len(candidateUniverses) > 0 {
		candidateUniverse = strings.TrimSpace(candidateUniverses[0])
	}
	if err := validateMappingAdjudication(adjudication); err != nil ||
		!validOptionalLifecycleText(candidateUniverse, 128) {
		if err == nil {
			err = errors.New("invalid mapping candidate universe")
		}
		return RepositoryState{}, RepositoryMappingJob{}, err
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, RepositoryMappingJob{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, RepositoryMappingJob{}, err
	}
	if err := HistoricalDeduplicationMutationAllowed(state); err != nil {
		return RepositoryState{}, RepositoryMappingJob{}, err
	}
	if candidateUniverse == "" {
		candidateUniverse = repositoryMatchingUniverseFingerprint(state.RepositoryFindings)
	}
	index := mappingJobIndexByID(state.MappingJobs, jobID)
	if index < 0 {
		return RepositoryState{}, RepositoryMappingJob{}, os.ErrNotExist
	}
	job := &state.MappingJobs[index]
	if job.State == RepositoryMappingCompleted {
		if mappingAdjudicationsEqual(job.Adjudication, adjudication) &&
			(candidateUniverse == "" || job.CandidateUniverse == candidateUniverse) {
			return state, *job, nil
		}
		return RepositoryState{}, RepositoryMappingJob{}, ErrConflict
	}
	if job.State != RepositoryMappingRunning {
		return RepositoryState{}, RepositoryMappingJob{}, ErrConflict
	}
	if !mappingAdjudicationEmpty(job.Adjudication) {
		if mappingAdjudicationsEqual(job.Adjudication, adjudication) &&
			(candidateUniverse == "" || job.CandidateUniverse == candidateUniverse) {
			return state, *job, nil
		}
		return RepositoryState{}, RepositoryMappingJob{}, ErrConflict
	}
	now := s.clock()
	job.Adjudication = adjudication
	job.CandidateUniverse = candidateUniverse
	job.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, RepositoryMappingJob{}, err
	}
	return state, *job, nil
}

// CompleteMappingJob commits the occurrence association and terminal job state
// in one repository-ledger write. Replays return the already committed result.
func (s Store) CompleteMappingJob(
	repository string,
	completion RepositoryMappingCompletion,
) (RepositoryState, RepositoryFinding, error) {
	completion.JobID = strings.TrimSpace(completion.JobID)
	completion.RepositoryFindingID = strings.TrimSpace(completion.RepositoryFindingID)
	completion.RegressionFixCommit = strings.ToLower(strings.TrimSpace(completion.RegressionFixCommit))
	completion.RegressionFindingID = strings.TrimSpace(completion.RegressionFindingID)
	completion.ExpectedUniverse = strings.TrimSpace(completion.ExpectedUniverse)
	if completion.JobID == "" ||
		(completion.RepositoryFindingID == "" &&
			completion.CreateMatchState != RepositoryMatchNew &&
			completion.CreateMatchState != RepositoryMatchProvisional) ||
		completion.RepositoryFindingID != "" && completion.CreateMatchState != "" ||
		completion.RegressionVerified != (completion.RegressionFixCommit != "") ||
		completion.RegressionVerified != (completion.RegressionFindingID != "") ||
		(completion.RegressionFixCommit != "" &&
			!validRepositoryReviewCommitSHA(completion.RegressionFixCommit)) ||
		!validOptionalLifecycleText(completion.ExpectedUniverse, 128) ||
		len(completion.PossibleDuplicates) > maxRepositoryPossibleDuplicates {
		return RepositoryState{}, RepositoryFinding{}, errors.New("invalid mapping completion")
	}
	duplicates, err := normalizePossibleDuplicates(completion.PossibleDuplicates)
	if err != nil {
		return RepositoryState{}, RepositoryFinding{}, err
	}
	completion.PossibleDuplicates = duplicates
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, RepositoryFinding{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, RepositoryFinding{}, err
	}
	if err := HistoricalDeduplicationMutationAllowed(state); err != nil {
		return RepositoryState{}, RepositoryFinding{}, err
	}
	jobIndex := mappingJobIndexByID(state.MappingJobs, completion.JobID)
	if jobIndex < 0 {
		return RepositoryState{}, RepositoryFinding{}, os.ErrNotExist
	}
	job := &state.MappingJobs[jobIndex]
	findingIndex := findingIndexByID(state.Findings, job.ReviewFindingID)
	if findingIndex < 0 {
		return RepositoryState{}, RepositoryFinding{}, errors.New("mapping job review finding is missing")
	}
	occurrence := &state.Findings[findingIndex]
	if job.State == RepositoryMappingCompleted {
		if occurrence.RepositoryFindingID != job.RepositoryFindingID {
			return RepositoryState{}, RepositoryFinding{}, ErrConflict
		}
		index := repositoryFindingIndexByID(state.RepositoryFindings, job.RepositoryFindingID)
		if index < 0 {
			return RepositoryState{}, RepositoryFinding{}, errors.New("completed mapping target is missing")
		}
		return state, state.RepositoryFindings[index], nil
	}
	if job.State != RepositoryMappingRunning {
		return RepositoryState{}, RepositoryFinding{}, ErrConflict
	}
	if err := mappingCompletionMatchesAdjudication(*job, completion); err != nil {
		return RepositoryState{}, RepositoryFinding{}, err
	}
	now := s.clock()
	targetIndex := -1
	createdTarget := false
	if completion.RepositoryFindingID != "" {
		targetIndex = repositoryFindingIndexByID(state.RepositoryFindings, completion.RepositoryFindingID)
		if targetIndex < 0 {
			return RepositoryState{}, RepositoryFinding{}, os.ErrNotExist
		}
	} else {
		if !occurrenceMayCreateRepositoryFinding(*occurrence, completion.DefaultBranchVerified) {
			return RepositoryState{}, RepositoryFinding{}, errors.New(
				"non-default-branch occurrence cannot create a repository finding",
			)
		}
		occurrences := make(map[string]Finding, len(state.Findings))
		for _, finding := range state.Findings {
			occurrences[finding.ID] = finding
		}
		freshMatch := MatchRepositoryFinding(
			*occurrence, state.RepositoryFindings, occurrences, nil,
		)
		if freshMatch.RepositoryFindingID != "" {
			completion.RepositoryFindingID = freshMatch.RepositoryFindingID
			targetIndex = repositoryFindingIndexByID(
				state.RepositoryFindings, freshMatch.RepositoryFindingID,
			)
		}
		if targetIndex >= 0 {
			// Another job created the matching aggregate after this job's
			// candidate snapshot. Join it instead of creating a duplicate.
			goto mappingTargetSelected
		}
		if completion.ExpectedUniverse != "" && completion.ExpectedUniverse !=
			repositoryMatchingUniverseFingerprint(state.RepositoryFindings) {
			restartNow := s.clock()
			job.State = RepositoryMappingPending
			job.Adjudication = RepositoryMappingAdjudication{}
			job.CandidateUniverse = ""
			job.Error = "Repository finding candidates changed; status processing will restart."
			job.ReservedAt = time.Time{}
			job.UpdatedAt = restartNow
			state.Version++
			state.UpdatedAt = restartNow
			if err := s.save(&state); err != nil {
				return RepositoryState{}, RepositoryFinding{}, err
			}
			return state, RepositoryFinding{}, errRepositoryMappingUniverseChanged
		}
		id := stableID("rrf_", state.Repository, occurrence.ID)
		if repositoryFindingIndexByID(state.RepositoryFindings, id) >= 0 {
			return RepositoryState{}, RepositoryFinding{}, ErrConflict
		}
		for _, duplicate := range completion.PossibleDuplicates {
			if duplicate.CandidateID == id ||
				repositoryFindingIndexByID(state.RepositoryFindings, duplicate.CandidateID) < 0 {
				return RepositoryState{}, RepositoryFinding{}, errors.New("possible-duplicate candidate is missing")
			}
		}
		matchState := completion.CreateMatchState
		occurrence.DefaultBranchVerified = completion.DefaultBranchVerified
		occurrence.PostResolutionVerified = completion.RegressionVerified
		occurrence.PostResolutionFixCommit = completion.RegressionFixCommit
		occurrence.PostResolutionFindingID = completion.RegressionFindingID
		created := repositoryFindingFromOccurrence(
			state,
			*occurrence,
			id,
			matchState,
			completion.PossibleDuplicates,
			now,
		)
		state.RepositoryFindings = append(state.RepositoryFindings, created)
		targetIndex = len(state.RepositoryFindings) - 1
		createdTarget = true
	}

mappingTargetSelected:
	occurrence.DefaultBranchVerified = completion.DefaultBranchVerified
	occurrence.PostResolutionVerified = completion.RegressionVerified
	occurrence.PostResolutionFixCommit = completion.RegressionFixCommit
	occurrence.PostResolutionFindingID = completion.RegressionFindingID
	target := &state.RepositoryFindings[targetIndex]
	if completion.RepositoryFindingID != "" {
		associateOccurrenceWithRepositoryFinding(target, *occurrence, now)
		mergeOccurrenceIssueAssociation(&state, target, *occurrence)
		if target.MatchState == RepositoryMatchNew {
			target.MatchState = RepositoryMatchKnown
		}
	}
	if target.MatchState == RepositoryMatchProvisional &&
		!repositoryPossibleDuplicatesAreAmbiguous(target.PossibleDuplicates) {
		target.MatchState = RepositoryMatchNew
	}
	if target.Issue.State == RepositoryFindingIssueClosed &&
		target.Lifecycle == RepositoryFindingOpen {
		target.Lifecycle = RepositoryFindingResolutionPending
	}
	if occurrenceAfterConfirmedResolution(*occurrence, *target) {
		target.Lifecycle = RepositoryFindingRegressed
		target.ValidationState = RepositoryValidationNotRequested
	}
	if !createdTarget {
		target.Version++
	} else {
		for index := range state.MappingJobs {
			pending := &state.MappingJobs[index]
			if pending.ID == job.ID || pending.State != RepositoryMappingPending ||
				mappingAdjudicationEmpty(pending.Adjudication) || pending.Adjudication.Decision == "same" {
				continue
			}
			// A new aggregate changes the candidate universe. A prior
			// distinct/related/uncertain answer for a deferred occurrence is
			// no longer authoritative and must be CPU/AI matched again.
			pending.Adjudication = RepositoryMappingAdjudication{}
			pending.CandidateUniverse = ""
			pending.Error = ""
			pending.UpdatedAt = now
		}
	}
	target.UpdatedAt = now
	for _, associatedID := range target.ReviewFindingIDs {
		if associatedID == occurrence.ID {
			continue
		}
		if associatedIndex := findingIndexByID(state.Findings, associatedID); associatedIndex >= 0 &&
			state.Findings[associatedIndex].RepositoryMatchState != target.MatchState {
			state.Findings[associatedIndex].RepositoryMatchState = target.MatchState
			state.Findings[associatedIndex].Version++
			state.Findings[associatedIndex].UpdatedAt = now
		}
	}
	occurrence.RepositoryFindingID = target.ID
	occurrence.RepositoryMatchState = target.MatchState
	occurrence.Version++
	occurrence.UpdatedAt = now
	job.State = RepositoryMappingCompleted
	job.RepositoryFindingID = target.ID
	job.Error = ""
	job.ReservedAt = time.Time{}
	job.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, RepositoryFinding{}, err
	}
	return state, *target, nil
}

func (s Store) ResolvePossibleDuplicate(
	repository string,
	request RepositoryDuplicateResolution,
) (RepositoryState, RepositoryFinding, error) {
	request.ProvisionalID = strings.TrimSpace(request.ProvisionalID)
	request.CandidateID = strings.TrimSpace(request.CandidateID)
	request.Decision = strings.ToLower(strings.TrimSpace(request.Decision))
	if request.ProvisionalID == "" || request.CandidateID == "" ||
		request.ProvisionalID == request.CandidateID ||
		(request.Decision != "merge" && request.Decision != "distinct") ||
		request.ExpectedProvisionalVersion < 1 {
		return RepositoryState{}, RepositoryFinding{}, errors.New("invalid possible-duplicate resolution")
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, RepositoryFinding{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, RepositoryFinding{}, err
	}
	if err := HistoricalDeduplicationMutationAllowed(state); err != nil {
		return RepositoryState{}, RepositoryFinding{}, err
	}
	provisionalIndex := repositoryFindingIndexByID(state.RepositoryFindings, request.ProvisionalID)
	candidateIndex := repositoryFindingIndexByID(state.RepositoryFindings, request.CandidateID)
	if provisionalIndex < 0 || candidateIndex < 0 {
		return RepositoryState{}, RepositoryFinding{}, os.ErrNotExist
	}
	provisional := &state.RepositoryFindings[provisionalIndex]
	candidate := &state.RepositoryFindings[candidateIndex]
	if provisional.MatchState != RepositoryMatchProvisional ||
		provisional.Version != request.ExpectedProvisionalVersion ||
		(request.Decision == "merge" &&
			(request.ExpectedCandidateVersion < 1 || candidate.Version != request.ExpectedCandidateVersion)) ||
		!repositoryFindingHasPossibleDuplicate(*provisional, candidate.ID) {
		return RepositoryState{}, RepositoryFinding{}, ErrConflict
	}
	now := s.clock()
	if request.Decision == "distinct" {
		provisional.PossibleDuplicates = removeRepositoryPossibleDuplicate(
			provisional.PossibleDuplicates, candidate.ID,
		)
		if !repositoryPossibleDuplicatesAreAmbiguous(provisional.PossibleDuplicates) {
			provisional.MatchState = RepositoryMatchNew
			for _, findingID := range provisional.ReviewFindingIDs {
				if index := findingIndexByID(state.Findings, findingID); index >= 0 {
					state.Findings[index].RepositoryMatchState = RepositoryMatchNew
					state.Findings[index].Version++
					state.Findings[index].UpdatedAt = now
				}
			}
		}
		provisional.Version++
		provisional.UpdatedAt = now
		state.Version++
		state.UpdatedAt = now
		if err := s.save(&state); err != nil {
			return RepositoryState{}, RepositoryFinding{}, err
		}
		return state, *provisional, nil
	}

	merged := mergeRepositoryFindingRecords(candidate, *provisional, now)
	merged.MatchState = RepositoryMatchKnown
	if repositoryPossibleDuplicatesAreAmbiguous(merged.PossibleDuplicates) {
		merged.MatchState = RepositoryMatchProvisional
	}
	for _, findingID := range provisional.ReviewFindingIDs {
		if index := findingIndexByID(state.Findings, findingID); index >= 0 &&
			occurrenceAfterConfirmedResolution(state.Findings[index], *candidate) {
			merged.Lifecycle = RepositoryFindingRegressed
			merged.ValidationState = RepositoryValidationNotRequested
			break
		}
	}
	for _, findingID := range merged.ReviewFindingIDs {
		if index := findingIndexByID(state.Findings, findingID); index >= 0 {
			state.Findings[index].RepositoryFindingID = candidate.ID
			state.Findings[index].RepositoryMatchState = merged.MatchState
			state.Findings[index].Version++
			state.Findings[index].UpdatedAt = now
		}
	}
	for index := range state.MappingJobs {
		if state.MappingJobs[index].RepositoryFindingID == provisional.ID {
			state.MappingJobs[index].RepositoryFindingID = candidate.ID
			state.MappingJobs[index].UpdatedAt = now
		}
		if state.MappingJobs[index].Adjudication.CandidateID == provisional.ID {
			state.MappingJobs[index].Adjudication.CandidateID = candidate.ID
			state.MappingJobs[index].UpdatedAt = now
		}
	}
	for index := range state.ValidationJobs {
		if state.ValidationJobs[index].RepositoryFindingID == provisional.ID {
			state.ValidationJobs[index].RepositoryFindingID = candidate.ID
			state.ValidationJobs[index].UpdatedAt = now
		}
	}
	for index := range state.RepositoryFindings {
		other := &state.RepositoryFindings[index]
		if other.ID == provisional.ID || other.ID == candidate.ID {
			continue
		}
		changed := false
		updated := make([]RepositoryFindingPossibleDuplicate, 0, len(other.PossibleDuplicates))
		for _, duplicate := range other.PossibleDuplicates {
			if duplicate.CandidateID == provisional.ID {
				duplicate.CandidateID = candidate.ID
				changed = true
			}
			if duplicate.CandidateID == other.ID || possibleDuplicateContains(updated, duplicate.CandidateID) {
				continue
			}
			updated = append(updated, duplicate)
		}
		if changed {
			other.PossibleDuplicates = updated
			matchStateChanged := false
			if other.MatchState == RepositoryMatchProvisional &&
				!repositoryPossibleDuplicatesAreAmbiguous(updated) {
				other.MatchState = RepositoryMatchNew
				matchStateChanged = true
			}
			if matchStateChanged {
				for _, occurrenceID := range other.ReviewFindingIDs {
					if occurrenceIndex := findingIndexByID(state.Findings, occurrenceID); occurrenceIndex >= 0 {
						state.Findings[occurrenceIndex].RepositoryMatchState = other.MatchState
						state.Findings[occurrenceIndex].Version++
						state.Findings[occurrenceIndex].UpdatedAt = now
					}
				}
			}
			other.Version++
			other.UpdatedAt = now
		}
	}
	state.RepositoryFindings[candidateIndex] = merged
	state.RepositoryFindings = append(
		state.RepositoryFindings[:provisionalIndex],
		state.RepositoryFindings[provisionalIndex+1:]...,
	)
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, RepositoryFinding{}, err
	}
	index := repositoryFindingIndexByID(state.RepositoryFindings, candidate.ID)
	if index < 0 {
		return RepositoryState{}, RepositoryFinding{}, errors.New("merged repository finding disappeared")
	}
	return state, state.RepositoryFindings[index], nil
}

func (s Store) ReserveValidationJobs(
	repository string,
	repositoryFindingIDs []string,
	snapshot RepositoryMappingModelSnapshot,
) (RepositoryState, []RepositoryValidationJob, error) {
	if len(repositoryFindingIDs) == 0 || len(repositoryFindingIDs) > maxValidationBatch {
		return RepositoryState{}, nil, errors.New("one to 50 repository findings are required")
	}
	if err := validateMappingModelSnapshot(snapshot); err != nil {
		return RepositoryState{}, nil, err
	}
	ids := make([]string, len(repositoryFindingIDs))
	seen := make(map[string]struct{}, len(ids))
	for index, id := range repositoryFindingIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return RepositoryState{}, nil, errors.New("invalid repository finding ID")
		}
		if _, duplicate := seen[id]; duplicate {
			return RepositoryState{}, nil, errors.New("duplicate repository finding ID")
		}
		seen[id] = struct{}{}
		ids[index] = id
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, nil, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, nil, err
	}
	if err := HistoricalDeduplicationMutationAllowed(state); err != nil {
		return RepositoryState{}, nil, err
	}
	selected := make([]int, len(ids))
	for index, id := range ids {
		findingIndex := repositoryFindingIndexByID(state.RepositoryFindings, id)
		if findingIndex < 0 {
			return RepositoryState{}, nil, os.ErrNotExist
		}
		finding := state.RepositoryFindings[findingIndex]
		if finding.MatchState == RepositoryMatchProvisional ||
			finding.Lifecycle == RepositoryFindingDismissed || finding.Issue.Conflict {
			return RepositoryState{}, nil, ErrConflict
		}
		selected[index] = findingIndex
	}
	now := s.clock()
	jobs := make([]RepositoryValidationJob, 0, len(ids))
	changed := false
	for selectionIndex, findingIndex := range selected {
		finding := &state.RepositoryFindings[findingIndex]
		if activeIndex := activeValidationJobIndex(state.ValidationJobs, finding.ID); activeIndex >= 0 {
			jobs = append(jobs, state.ValidationJobs[activeIndex])
			continue
		}
		sequence := validationJobSequence(state.ValidationJobs, finding.ID) + 1
		job := RepositoryValidationJob{
			ID:                  stableID("rvj_", finding.ID, fmt.Sprint(sequence)),
			RepositoryFindingID: ids[selectionIndex], State: RepositoryValidationPending,
			ModelSnapshot: snapshot, CreatedAt: now, UpdatedAt: now,
		}
		finding.ValidationState = RepositoryValidationPending
		finding.Version++
		finding.UpdatedAt = now
		job.FindingVersion = finding.Version
		state.ValidationJobs = append(state.ValidationJobs, job)
		jobs = append(jobs, job)
		changed = true
	}
	if !changed {
		return state, jobs, nil
	}
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, nil, err
	}
	return state, jobs, nil
}

func (s Store) ClaimValidationJob(
	repository, jobID string,
) (RepositoryState, RepositoryValidationJob, RepositoryFinding, bool, error) {
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, RepositoryValidationJob{}, RepositoryFinding{}, false, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, RepositoryValidationJob{}, RepositoryFinding{}, false, err
	}
	if err := HistoricalDeduplicationMutationAllowed(state); err != nil {
		return RepositoryState{}, RepositoryValidationJob{}, RepositoryFinding{}, false, err
	}
	jobIndex := validationJobIndexByID(state.ValidationJobs, jobID)
	if jobIndex < 0 {
		return RepositoryState{}, RepositoryValidationJob{}, RepositoryFinding{}, false, os.ErrNotExist
	}
	job := &state.ValidationJobs[jobIndex]
	findingIndex := repositoryFindingIndexByID(state.RepositoryFindings, job.RepositoryFindingID)
	if findingIndex < 0 {
		return RepositoryState{}, RepositoryValidationJob{}, RepositoryFinding{}, false,
			errors.New("validation job repository finding is missing")
	}
	finding := &state.RepositoryFindings[findingIndex]
	if job.State == RepositoryValidationRunning || repositoryValidationTerminal(job.State) {
		return state, *job, *finding, false, nil
	}
	if job.State != RepositoryValidationPending || finding.MatchState == RepositoryMatchProvisional ||
		finding.Issue.Conflict {
		return RepositoryState{}, RepositoryValidationJob{}, RepositoryFinding{}, false, ErrConflict
	}
	now := s.clock()
	if job.FindingVersion != finding.Version {
		job.CandidateCommits = nil
	}
	job.State = RepositoryValidationRunning
	job.Attempts++
	job.Error = ""
	job.Failure = nil
	job.ReservedAt = now
	job.UpdatedAt = now
	finding.ValidationState = RepositoryValidationRunning
	finding.Version++
	finding.UpdatedAt = now
	job.FindingVersion = finding.Version
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, RepositoryValidationJob{}, RepositoryFinding{}, false, err
	}
	return state, *job, *finding, true, nil
}

// SetValidationJobCandidates persists the exact bounded commit universe the
// validator is allowed to select from.
func (s Store) SetValidationJobCandidates(
	repository, jobID string,
	commits []string,
) (RepositoryState, RepositoryValidationJob, error) {
	normalized, err := normalizeValidationCommits(commits)
	if err != nil {
		return RepositoryState{}, RepositoryValidationJob{}, err
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, RepositoryValidationJob{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, RepositoryValidationJob{}, err
	}
	if err := HistoricalDeduplicationMutationAllowed(state); err != nil {
		return RepositoryState{}, RepositoryValidationJob{}, err
	}
	index := validationJobIndexByID(state.ValidationJobs, jobID)
	if index < 0 {
		return RepositoryState{}, RepositoryValidationJob{}, os.ErrNotExist
	}
	job := &state.ValidationJobs[index]
	if job.State != RepositoryValidationRunning {
		return RepositoryState{}, RepositoryValidationJob{}, ErrConflict
	}
	if job.CandidateCommits != nil {
		if stringSlicesEqual(job.CandidateCommits, normalized) {
			return state, *job, nil
		}
		return RepositoryState{}, RepositoryValidationJob{}, ErrConflict
	}
	now := s.clock()
	job.CandidateCommits = normalized
	job.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, RepositoryValidationJob{}, err
	}
	return state, *job, nil
}

func (s Store) CompleteValidationJob(
	repository string,
	completion RepositoryValidationCompletion,
) (RepositoryState, RepositoryFinding, RepositoryValidationJob, error) {
	completion.JobID = strings.TrimSpace(completion.JobID)
	completion.SelectedCommitSHA = strings.ToLower(strings.TrimSpace(completion.SelectedCommitSHA))
	completion.FirstContainingTag = strings.TrimSpace(completion.FirstContainingTag)
	completion.Summary = strings.TrimSpace(completion.Summary)
	completion.Error = safeLifecycleError(completion.Error)
	completion.FailureCode = RepositoryValidationFailureCode(
		strings.TrimSpace(string(completion.FailureCode)),
	)
	if completion.JobID == "" || !repositoryValidationTerminal(completion.Outcome) ||
		!validOptionalLifecycleText(completion.Summary, maxRepositoryLifecycleTextBytes) ||
		!validOptionalLifecycleText(completion.Error, 1024) ||
		!validOptionalLifecycleText(completion.FirstContainingTag, 256) {
		return RepositoryState{}, RepositoryFinding{}, RepositoryValidationJob{},
			errors.New("invalid validation completion")
	}
	if completion.Outcome == RepositoryValidationConfirmed {
		if !validRepositoryReviewCommitSHA(completion.SelectedCommitSHA) || completion.FixCommitTime.IsZero() {
			return RepositoryState{}, RepositoryFinding{}, RepositoryValidationJob{},
				errors.New("confirmed validation requires a fix commit and time")
		}
		if completion.FirstContainingTag != "" &&
			!semanticVersionTagPattern.MatchString(completion.FirstContainingTag) {
			return RepositoryState{}, RepositoryFinding{}, RepositoryValidationJob{},
				errors.New("first containing tag is not semantic versioning")
		}
	} else if completion.SelectedCommitSHA != "" || !completion.FixCommitTime.IsZero() ||
		completion.FirstContainingTag != "" {
		return RepositoryState{}, RepositoryFinding{}, RepositoryValidationJob{},
			errors.New("non-confirmed validation cannot record a fix commit")
	}
	if completion.Outcome != RepositoryValidationFailed && completion.FailureCode != "" {
		return RepositoryState{}, RepositoryFinding{}, RepositoryValidationJob{},
			errors.New("non-failed validation cannot record a failure code")
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, RepositoryFinding{}, RepositoryValidationJob{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, RepositoryFinding{}, RepositoryValidationJob{}, err
	}
	if err := HistoricalDeduplicationMutationAllowed(state); err != nil {
		return RepositoryState{}, RepositoryFinding{}, RepositoryValidationJob{}, err
	}
	jobIndex := validationJobIndexByID(state.ValidationJobs, completion.JobID)
	if jobIndex < 0 {
		return RepositoryState{}, RepositoryFinding{}, RepositoryValidationJob{}, os.ErrNotExist
	}
	job := &state.ValidationJobs[jobIndex]
	findingIndex := repositoryFindingIndexByID(state.RepositoryFindings, job.RepositoryFindingID)
	if findingIndex < 0 {
		return RepositoryState{}, RepositoryFinding{}, RepositoryValidationJob{},
			errors.New("validation job repository finding is missing")
	}
	finding := &state.RepositoryFindings[findingIndex]
	if repositoryValidationTerminal(job.State) {
		if job.State == completion.Outcome &&
			(completion.Outcome != RepositoryValidationConfirmed || finding.FixCommitSHA == completion.SelectedCommitSHA) {
			return state, *finding, *job, nil
		}
		return RepositoryState{}, RepositoryFinding{}, RepositoryValidationJob{}, ErrConflict
	}
	if job.State != RepositoryValidationRunning {
		return RepositoryState{}, RepositoryFinding{}, RepositoryValidationJob{}, ErrConflict
	}
	if job.FindingVersion != finding.Version {
		now := s.clock()
		job.State = RepositoryValidationPending
		job.CandidateCommits = nil
		job.Error = "Repository finding changed; validation will restart."
		job.Failure = nil
		job.ReservedAt = time.Time{}
		job.UpdatedAt = now
		finding.ValidationState = RepositoryValidationPending
		finding.Version++
		finding.UpdatedAt = now
		job.FindingVersion = finding.Version
		state.Version++
		state.UpdatedAt = now
		if err := s.save(&state); err != nil {
			return RepositoryState{}, RepositoryFinding{}, RepositoryValidationJob{}, err
		}
		return state, *finding, *job, errRepositoryValidationEvidenceChanged
	}
	if completion.Outcome == RepositoryValidationConfirmed &&
		!containsExactString(job.CandidateCommits, completion.SelectedCommitSHA) {
		return RepositoryState{}, RepositoryFinding{}, RepositoryValidationJob{},
			errors.New("validator selected a commit outside the supplied candidate set")
	}
	now := s.clock()
	job.State = completion.Outcome
	job.Error = completion.Error
	job.Failure = nil
	if completion.Outcome == RepositoryValidationFailed {
		job.Failure = safeRepositoryValidationFailure(completion.FailureCode, now)
	}
	job.ReservedAt = time.Time{}
	job.UpdatedAt = now
	finding.ValidationState = completion.Outcome
	resolution := RepositoryFindingResolution{
		Outcome: completion.Outcome, ValidatedAt: now, Summary: completion.Summary,
		Failure: job.Failure,
	}
	if completion.Outcome == RepositoryValidationConfirmed {
		resolution.FixCommitSHA = completion.SelectedCommitSHA
		resolution.FixCommitTime = completion.FixCommitTime.UTC()
		resolution.FirstContainingTag = completion.FirstContainingTag
		finding.FixCommitSHA = resolution.FixCommitSHA
		finding.FixCommitTime = resolution.FixCommitTime
		finding.FirstContainingTag = resolution.FirstContainingTag
		finding.Lifecycle = RepositoryFindingResolved
	} else if completion.Outcome == RepositoryValidationNotFixed &&
		(finding.Lifecycle == RepositoryFindingResolved ||
			finding.Lifecycle == RepositoryFindingResolutionPending) {
		finding.Lifecycle = RepositoryFindingOpen
	}
	finding.ResolutionHistory = appendBoundedResolution(finding.ResolutionHistory, resolution)
	finding.Version++
	finding.UpdatedAt = now
	job.FindingVersion = finding.Version
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, RepositoryFinding{}, RepositoryValidationJob{}, err
	}
	return state, *finding, *job, nil
}

func RepositoryFindingIssueSnapshotFresh(
	finding RepositoryFinding,
	now time.Time,
) bool {
	if finding.Issue.State == RepositoryFindingIssueNone || finding.Issue.SnapshotAt.IsZero() {
		return false
	}
	now = now.UTC()
	return !now.Before(finding.Issue.SnapshotAt) &&
		now.Sub(finding.Issue.SnapshotAt) < RepositoryIssueSnapshotTTL
}

func (s Store) UpdateRepositoryFindingIssueSnapshot(
	repository string,
	update RepositoryIssueSnapshotUpdate,
) (RepositoryState, RepositoryFinding, error) {
	update.RepositoryFindingID = strings.TrimSpace(update.RepositoryFindingID)
	update.ExternalID = strings.TrimSpace(update.ExternalID)
	update.URL = strings.TrimSpace(update.URL)
	update.Title = strings.TrimSpace(update.Title)
	if update.RepositoryFindingID == "" || !validRepositoryIssueState(update.State) ||
		!validOptionalLifecycleText(update.ExternalID, 1024) ||
		!validOptionalLifecycleText(update.Title, 256) ||
		(update.URL != "" && !validHTTPSURL(update.URL)) ||
		(update.Origin != "" && update.Origin != IssueDraftOriginAIGenerated &&
			update.Origin != IssueDraftOriginLinked && update.Origin != IssueDraftOriginDiscovered &&
			update.Origin != IssueDraftOriginLegacy) {
		return RepositoryState{}, RepositoryFinding{}, errors.New("invalid issue snapshot")
	}
	if update.State != RepositoryFindingIssueNone && (update.ExternalID == "" || update.URL == "") {
		return RepositoryState{}, RepositoryFinding{}, errors.New("issue snapshot identity is required")
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, RepositoryFinding{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, RepositoryFinding{}, err
	}
	if err := HistoricalDeduplicationMutationAllowed(state); err != nil {
		return RepositoryState{}, RepositoryFinding{}, err
	}
	index := repositoryFindingIndexByID(state.RepositoryFindings, update.RepositoryFindingID)
	if index < 0 {
		return RepositoryState{}, RepositoryFinding{}, os.ErrNotExist
	}
	finding := &state.RepositoryFindings[index]
	if update.ExpectedVersion > 0 && finding.Version != update.ExpectedVersion {
		return RepositoryState{}, RepositoryFinding{}, ErrConflict
	}
	if finding.MatchState == RepositoryMatchProvisional {
		return RepositoryState{}, RepositoryFinding{}, ErrConflict
	}
	if finding.Issue.Conflict {
		return RepositoryState{}, RepositoryFinding{}, errors.New(
			"issue association conflict requires manual resolution",
		)
	}
	if finding.Issue.URL != "" && update.URL != "" && finding.Issue.URL != update.URL {
		return RepositoryState{}, RepositoryFinding{}, ErrConflict
	}
	now := s.clock()
	if update.SnapshotAt.IsZero() {
		update.SnapshotAt = now
	} else {
		update.SnapshotAt = update.SnapshotAt.UTC()
		if update.SnapshotAt.After(now.Add(time.Minute)) {
			return RepositoryState{}, RepositoryFinding{}, errors.New("issue snapshot time is in the future")
		}
	}
	externalState := ""
	if update.State == RepositoryFindingIssueOpen {
		externalState = "open"
	} else if update.State == RepositoryFindingIssueClosed {
		externalState = "closed"
	}
	for _, occurrenceID := range finding.ReviewFindingIDs {
		occurrenceIndex := findingIndexByID(state.Findings, occurrenceID)
		if occurrenceIndex < 0 || state.Findings[occurrenceIndex].IssueDraftID == "" {
			continue
		}
		draftIndex := issueDraftIndexByID(
			state.IssueDrafts, state.Findings[occurrenceIndex].IssueDraftID,
		)
		if draftIndex < 0 {
			continue
		}
		draft := &state.IssueDrafts[draftIndex]
		if draft.State != IssueDraftPosted || draft.ExternalURL != update.URL {
			continue
		}
		draft.ExternalID = update.ExternalID
		draft.ExternalState = externalState
		if update.Title != "" {
			draft.Title = update.Title
		}
		draft.Version++
		if update.SnapshotAt.After(draft.UpdatedAt) {
			draft.UpdatedAt = update.SnapshotAt
		}
	}
	finding.Issue = RepositoryFindingIssueAssociation{
		ExternalID: update.ExternalID, URL: update.URL, Origin: update.Origin,
		State: update.State, Title: update.Title, SnapshotAt: update.SnapshotAt,
	}
	switch update.State {
	case RepositoryFindingIssueClosed:
		if finding.Lifecycle != RepositoryFindingResolved &&
			finding.Lifecycle != RepositoryFindingDismissed &&
			finding.Lifecycle != RepositoryFindingRegressed {
			finding.Lifecycle = RepositoryFindingResolutionPending
		}
	case RepositoryFindingIssueOpen:
		finding.Lifecycle = RepositoryFindingOpen
	}
	finding.Version++
	finding.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, RepositoryFinding{}, err
	}
	return state, *finding, nil
}

func (s Store) SetRepositoryFindingLifecycle(
	repository, repositoryFindingID string,
	lifecycle RepositoryFindingLifecycle,
	expectedVersion int64,
) (RepositoryState, RepositoryFinding, error) {
	if lifecycle != RepositoryFindingOpen && lifecycle != RepositoryFindingDismissed {
		return RepositoryState{}, RepositoryFinding{}, errors.New("invalid manual repository finding lifecycle")
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, RepositoryFinding{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, RepositoryFinding{}, err
	}
	if err := HistoricalDeduplicationMutationAllowed(state); err != nil {
		return RepositoryState{}, RepositoryFinding{}, err
	}
	index := repositoryFindingIndexByID(state.RepositoryFindings, repositoryFindingID)
	if index < 0 {
		return RepositoryState{}, RepositoryFinding{}, os.ErrNotExist
	}
	finding := &state.RepositoryFindings[index]
	if finding.Lifecycle == lifecycle {
		return state, *finding, nil
	}
	if expectedVersion < 1 || finding.Version != expectedVersion ||
		finding.MatchState == RepositoryMatchProvisional ||
		finding.ValidationState == RepositoryValidationPending ||
		finding.ValidationState == RepositoryValidationRunning ||
		(lifecycle == RepositoryFindingOpen && finding.Lifecycle != RepositoryFindingDismissed) ||
		(lifecycle == RepositoryFindingDismissed && finding.Lifecycle != RepositoryFindingOpen &&
			finding.Lifecycle != RepositoryFindingRegressed) ||
		(lifecycle == RepositoryFindingDismissed && finding.Issue.State != RepositoryFindingIssueNone) {
		return RepositoryState{}, RepositoryFinding{}, ErrConflict
	}
	now := s.clock()
	finding.Lifecycle = lifecycle
	finding.Version++
	finding.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, RepositoryFinding{}, err
	}
	return state, *finding, nil
}

func validateMappingModelSnapshot(snapshot RepositoryMappingModelSnapshot) error {
	snapshot.ProfileID = strings.TrimSpace(snapshot.ProfileID)
	snapshot.Prompt = strings.TrimSpace(snapshot.Prompt)
	snapshot.Model = strings.TrimSpace(snapshot.Model)
	snapshot.Account = strings.TrimSpace(snapshot.Account)
	if (snapshot.ProfileID == "") != (snapshot.ProfileVersion == 0) ||
		!validOptionalLifecycleText(snapshot.ProfileID, 128) ||
		!validOptionalLifecycleText(snapshot.Prompt, 16<<10) ||
		!validOptionalLifecycleText(snapshot.Model, 256) ||
		!validOptionalLifecycleText(snapshot.Account, 256) {
		return errors.New("invalid mapping model snapshot")
	}
	if snapshot.ProfileID != "" && snapshot.ProfileVersion < 1 {
		return errors.New("invalid mapping model snapshot")
	}
	return nil
}

func mappingModelSnapshotEmpty(snapshot RepositoryMappingModelSnapshot) bool {
	return snapshot == (RepositoryMappingModelSnapshot{})
}

func mappingModelSnapshotsEqual(left, right RepositoryMappingModelSnapshot) bool {
	return left == right
}

func mappingJobIndexByID(jobs []RepositoryMappingJob, id string) int {
	id = strings.TrimSpace(id)
	for index := range jobs {
		if jobs[index].ID == id {
			return index
		}
	}
	return -1
}

func repositoryFindingIndexByID(findings []RepositoryFinding, id string) int {
	id = strings.TrimSpace(id)
	for index := range findings {
		if findings[index].ID == id {
			return index
		}
	}
	return -1
}

func validationJobIndexByID(jobs []RepositoryValidationJob, id string) int {
	id = strings.TrimSpace(id)
	for index := range jobs {
		if jobs[index].ID == id {
			return index
		}
	}
	return -1
}

func normalizeMappingAdjudication(value RepositoryMappingAdjudication) RepositoryMappingAdjudication {
	value.Decision = strings.ToLower(strings.TrimSpace(value.Decision))
	value.CandidateID = strings.TrimSpace(value.CandidateID)
	value.Explanation = strings.TrimSpace(value.Explanation)
	value.MatchingAnchors = normalizeFindingIdentityHints(value.MatchingAnchors)
	value.ConflictingAnchors = normalizeFindingIdentityHints(value.ConflictingAnchors)
	value.ConflictFields = normalizeRepositoryMappingConflictFields(value.ConflictFields)
	return value
}

func validateMappingAdjudication(value RepositoryMappingAdjudication) error {
	if value.Decision != "same" && value.Decision != "related" &&
		value.Decision != "distinct" && value.Decision != "uncertain" {
		return errors.New("invalid mapping adjudication decision")
	}
	if (value.Decision == "same" || value.Decision == "related") &&
		value.CandidateID == "" {
		return errors.New("mapping adjudication candidate is required")
	}
	if math.IsNaN(value.Confidence) || math.IsInf(value.Confidence, 0) ||
		value.Confidence < 0 || value.Confidence > 1 ||
		!validOptionalLifecycleText(value.Explanation, 2048) ||
		len(value.MatchingAnchors) > maxMatchHintItems ||
		len(value.ConflictingAnchors) > maxMatchHintItems {
		return errors.New("invalid mapping adjudication")
	}
	for _, anchors := range [][]string{value.MatchingAnchors, value.ConflictingAnchors} {
		seen := make(map[string]struct{}, len(anchors))
		for _, anchor := range anchors {
			if !validBoundedText(anchor, maxMatchHintIdentityBytes) || strings.ContainsAny(anchor, "\r\n") {
				return errors.New("invalid mapping adjudication anchor")
			}
			key := normalizedText(anchor)
			if _, duplicate := seen[key]; duplicate {
				return errors.New("duplicate mapping adjudication anchor")
			}
			seen[key] = struct{}{}
		}
	}
	if err := validateRepositoryMappingConflictFields(
		value.ConflictingAnchors,
		value.ConflictFields,
	); err != nil {
		return err
	}
	return nil
}

func mappingAdjudicationEmpty(value RepositoryMappingAdjudication) bool {
	return value.Decision == "" && value.CandidateID == "" && value.Confidence == 0 &&
		len(value.MatchingAnchors) == 0 && len(value.ConflictingAnchors) == 0 &&
		len(value.ConflictFields) == 0 &&
		value.Explanation == ""
}

func mappingAdjudicationsEqual(left, right RepositoryMappingAdjudication) bool {
	return left.Decision == right.Decision && left.CandidateID == right.CandidateID &&
		left.Confidence == right.Confidence && left.Explanation == right.Explanation &&
		stringSlicesEqual(left.MatchingAnchors, right.MatchingAnchors) &&
		stringSlicesEqual(left.ConflictingAnchors, right.ConflictingAnchors) &&
		stringSlicesEqual(left.ConflictFields, right.ConflictFields)
}

func normalizePossibleDuplicates(
	values []RepositoryFindingPossibleDuplicate,
) ([]RepositoryFindingPossibleDuplicate, error) {
	out := append([]RepositoryFindingPossibleDuplicate(nil), values...)
	seen := make(map[string]struct{}, len(out))
	for index := range out {
		value := &out[index]
		value.CandidateID = strings.TrimSpace(value.CandidateID)
		value.Relation = strings.ToLower(strings.TrimSpace(value.Relation))
		value.Explanation = strings.TrimSpace(value.Explanation)
		value.MatchingAnchors = normalizeFindingIdentityHints(value.MatchingAnchors)
		value.ConflictingAnchors = normalizeFindingIdentityHints(value.ConflictingAnchors)
		if value.CandidateID == "" ||
			!validBoundedText(value.CandidateID, 256) ||
			(value.Relation != "same" && value.Relation != "related" && value.Relation != "uncertain") ||
			math.IsNaN(value.Confidence) || math.IsInf(value.Confidence, 0) ||
			value.Confidence < 0 || value.Confidence > 1 ||
			!validOptionalLifecycleText(value.Explanation, 2048) ||
			len(value.MatchingAnchors) > maxMatchHintItems ||
			len(value.ConflictingAnchors) > maxMatchHintItems {
			return nil, errors.New("invalid possible duplicate")
		}
		if _, duplicate := seen[value.CandidateID]; duplicate {
			return nil, errors.New("duplicate possible-duplicate candidate")
		}
		seen[value.CandidateID] = struct{}{}
		value.CreatedAt = value.CreatedAt.UTC()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CandidateID < out[j].CandidateID })
	return out, nil
}

func mappingCompletionMatchesAdjudication(
	job RepositoryMappingJob,
	completion RepositoryMappingCompletion,
) error {
	decision := job.Adjudication.Decision
	if decision == "" {
		return nil // deterministic matcher did not require AI adjudication
	}
	switch decision {
	case "same":
		eligible := repositoryMappingAdjudicationAutoAssociates(job.Adjudication)
		if eligible {
			if completion.RepositoryFindingID != job.Adjudication.CandidateID {
				return errors.New("same adjudication target does not match the selected candidate")
			}
		} else if completion.RepositoryFindingID != "" ||
			completion.CreateMatchState != RepositoryMatchProvisional {
			return errors.New("low-confidence same adjudication requires a provisional repository finding")
		}
	case "uncertain":
		if completion.RepositoryFindingID != "" || completion.CreateMatchState != RepositoryMatchProvisional {
			return errors.New("uncertain adjudication requires a provisional repository finding")
		}
	case "related":
		if completion.RepositoryFindingID != "" {
			return errors.New("related adjudication cannot merge repository findings")
		}
	case "distinct":
		if completion.RepositoryFindingID != "" || completion.CreateMatchState != RepositoryMatchNew {
			return errors.New("distinct adjudication requires a new repository finding")
		}
	}
	return nil
}

func occurrenceMayCreateRepositoryFinding(finding Finding, defaultBranchVerified bool) bool {
	if finding.TargetIsDefault {
		return defaultBranchVerified
	}
	// Legacy occurrences did not retain either branch name. Their worker must
	// establish reachability from the current advertised default before create.
	return finding.TargetBranch == "" && finding.AdvertisedDefaultBranch == "" && defaultBranchVerified
}

func repositoryFindingFromOccurrence(
	state RepositoryState,
	finding Finding,
	id string,
	matchState RepositoryMatchState,
	duplicates []RepositoryFindingPossibleDuplicate,
	now time.Time,
) RepositoryFinding {
	for index := range duplicates {
		if duplicates[index].CreatedAt.IsZero() {
			duplicates[index].CreatedAt = now
		}
	}
	result := RepositoryFinding{
		ID: id, Repository: state.Repository, CanonicalTitle: finding.Title,
		CanonicalSeverity: finding.Severity, MatchHints: finding.MatchHints,
		FixEffort: finding.FixEffort, ReviewFindingIDs: []string{finding.ID},
		FoundCommits: []string{finding.CommitSHA},
		PathSymbolHistory: []RepositoryFindingPathSymbol{{
			ReviewFindingID: finding.ID, CommitSHA: finding.CommitSHA,
			Path: finding.File.Path, Symbol: finding.Symbol, ObservedAt: finding.CreatedAt,
			DefaultBranchVerified: finding.DefaultBranchVerified,
		}},
		MatchState: matchState, Lifecycle: RepositoryFindingOpen,
		Issue:              RepositoryFindingIssueAssociation{State: RepositoryFindingIssueNone},
		PossibleDuplicates: duplicates, ValidationState: RepositoryValidationNotRequested,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	mergeOccurrenceIssueAssociation(&state, &result, finding)
	return result
}

func associateOccurrenceWithRepositoryFinding(
	target *RepositoryFinding,
	finding Finding,
	now time.Time,
) {
	if target == nil || containsExactString(target.ReviewFindingIDs, finding.ID) {
		return
	}
	target.ReviewFindingIDs = append(target.ReviewFindingIDs, finding.ID)
	if !containsExactString(target.FoundCommits, finding.CommitSHA) {
		target.FoundCommits = append(target.FoundCommits, finding.CommitSHA)
	}
	target.PathSymbolHistory = append(target.PathSymbolHistory, RepositoryFindingPathSymbol{
		ReviewFindingID: finding.ID, CommitSHA: finding.CommitSHA,
		Path: finding.File.Path, Symbol: finding.Symbol, ObservedAt: finding.CreatedAt,
		DefaultBranchVerified: finding.DefaultBranchVerified,
	})
	sortRepositoryPathSymbolHistory(target.PathSymbolHistory)
	if matchHintsEmpty(target.MatchHints) && !matchHintsEmpty(finding.MatchHints) {
		target.MatchHints = finding.MatchHints
	}
	if target.FixEffort == (FixEffort{}) && finding.FixEffort != (FixEffort{}) {
		target.FixEffort = finding.FixEffort
	}
	if moreSevere(target.CanonicalSeverity, finding.Severity) == finding.Severity {
		target.CanonicalSeverity = finding.Severity
	}
	target.UpdatedAt = now
}

func mergeOccurrenceIssueAssociation(
	state *RepositoryState,
	target *RepositoryFinding,
	finding Finding,
) {
	if state == nil || target == nil || finding.IssueDraftID == "" {
		return
	}
	index := issueDraftIndexByID(state.IssueDrafts, finding.IssueDraftID)
	if index < 0 {
		return
	}
	draft := state.IssueDrafts[index]
	target.Issue = mergeRepositoryIssueAssociations(
		target.Issue, repositoryIssueAssociationFromDraft(draft),
	)
}

func repositoryIssueAssociationFromDraft(draft IssueDraft) RepositoryFindingIssueAssociation {
	association := RepositoryFindingIssueAssociation{
		ExternalID: draft.ExternalID, URL: draft.ExternalURL, Origin: draft.Origin,
		Title: draft.Title, SnapshotAt: draft.UpdatedAt,
	}
	switch draft.State {
	case IssueDraftPosted:
		if draft.ExternalState == "closed" {
			association.State = RepositoryFindingIssueClosed
		} else {
			association.State = RepositoryFindingIssueOpen
		}
	case IssueDraftPublishing, IssueDraftUnknown:
		association.State = RepositoryFindingIssueUnknown
	default:
		association.State = RepositoryFindingIssueDraft
	}
	return association
}

func synchronizeRepositoryFindingIssues(state *RepositoryState) {
	if state == nil || len(state.RepositoryFindings) == 0 {
		return
	}
	drafts := make(map[string]IssueDraft, len(state.IssueDrafts))
	for _, draft := range state.IssueDrafts {
		drafts[draft.ID] = draft
	}
	byRepositoryFinding := make(map[string][]RepositoryFindingIssueAssociation)
	for _, occurrence := range state.Findings {
		if occurrence.RepositoryFindingID == "" || occurrence.IssueDraftID == "" {
			continue
		}
		draft, found := drafts[occurrence.IssueDraftID]
		if !found {
			continue
		}
		byRepositoryFinding[occurrence.RepositoryFindingID] = append(
			byRepositoryFinding[occurrence.RepositoryFindingID],
			repositoryIssueAssociationFromDraft(draft),
		)
	}
	now := state.UpdatedAt
	for index := range state.RepositoryFindings {
		finding := &state.RepositoryFindings[index]
		associations := byRepositoryFinding[finding.ID]
		next := finding.Issue
		switch len(associations) {
		case 0:
			if next.State == RepositoryFindingIssueDraft {
				next = RepositoryFindingIssueAssociation{State: RepositoryFindingIssueNone}
			}
		case 1:
			projected := associations[0]
			if next.URL != "" && projected.URL != "" && next.URL != projected.URL &&
				len(finding.ReviewFindingIDs) == 1 &&
				reversibleIssueDraftOrigin(next.Origin) && reversibleIssueDraftOrigin(projected.Origin) {
				next = projected
			} else {
				next = mergeRepositoryIssueAssociations(next, projected)
			}
		default:
			next = RepositoryFindingIssueAssociation{State: RepositoryFindingIssueNone}
			for _, projected := range associations {
				next = mergeRepositoryIssueAssociations(next, projected)
			}
		}
		if reflect.DeepEqual(next, finding.Issue) {
			continue
		}
		previousIssueState := finding.Issue.State
		finding.Issue = next
		if !next.Conflict && next.State == RepositoryFindingIssueClosed &&
			previousIssueState != RepositoryFindingIssueClosed &&
			finding.Lifecycle == RepositoryFindingOpen {
			finding.Lifecycle = RepositoryFindingResolutionPending
		} else if !next.Conflict && next.State == RepositoryFindingIssueOpen &&
			previousIssueState != RepositoryFindingIssueOpen {
			finding.Lifecycle = RepositoryFindingOpen
		}
		finding.Version++
		if !now.IsZero() {
			finding.UpdatedAt = now
		}
	}
}

func clearRepositoryFindingIssueAssociation(
	state *RepositoryState,
	repositoryFindingID string,
	issueURL string,
	now time.Time,
) {
	if state == nil || repositoryFindingID == "" {
		return
	}
	index := repositoryFindingIndexByID(state.RepositoryFindings, repositoryFindingID)
	if index < 0 {
		return
	}
	finding := &state.RepositoryFindings[index]
	if issueURL != "" && finding.Issue.URL != issueURL {
		if !finding.Issue.Conflict || !containsExactString(finding.Issue.ConflictURLs, issueURL) {
			return
		}
	}
	finding.Issue = RepositoryFindingIssueAssociation{State: RepositoryFindingIssueNone}
	if finding.Lifecycle == RepositoryFindingResolutionPending {
		finding.Lifecycle = RepositoryFindingOpen
	}
	finding.Version++
	finding.UpdatedAt = now
}

func mergeRepositoryIssueAssociations(
	left, right RepositoryFindingIssueAssociation,
) RepositoryFindingIssueAssociation {
	if left.State == "" || left.State == RepositoryFindingIssueNone {
		return right
	}
	if right.State == "" || right.State == RepositoryFindingIssueNone {
		return left
	}
	if left.URL == right.URL && left.URL != "" {
		if right.SnapshotAt.After(left.SnapshotAt) {
			return right
		}
		return left
	}
	if left.URL == "" {
		return right
	}
	if right.URL == "" {
		return left
	}
	urls := append([]string{}, left.ConflictURLs...)
	urls = appendUnique(urls, left.URL)
	urls = appendUnique(urls, right.URL)
	for _, value := range right.ConflictURLs {
		urls = appendUnique(urls, value)
	}
	sort.Strings(urls)
	left.Conflict = true
	left.ConflictURLs = urls
	return left
}

func occurrenceAfterConfirmedResolution(finding Finding, target RepositoryFinding) bool {
	if target.ValidationState != RepositoryValidationConfirmed ||
		target.Lifecycle == RepositoryFindingRegressed || !finding.TargetIsDefault ||
		!finding.DefaultBranchVerified || !finding.PostResolutionVerified ||
		finding.PostResolutionFixCommit != target.FixCommitSHA ||
		finding.PostResolutionFindingID != target.ID {
		return false
	}
	for index := len(target.ResolutionHistory) - 1; index >= 0; index-- {
		resolution := target.ResolutionHistory[index]
		if resolution.Outcome == RepositoryValidationConfirmed {
			return finding.CreatedAt.After(resolution.ValidatedAt)
		}
	}
	return false
}

func repositoryFindingHasPossibleDuplicate(finding RepositoryFinding, candidateID string) bool {
	return possibleDuplicateContains(finding.PossibleDuplicates, candidateID)
}

func repositoryPossibleDuplicatesAreAmbiguous(
	values []RepositoryFindingPossibleDuplicate,
) bool {
	for _, value := range values {
		if value.Relation == "same" || value.Relation == "uncertain" {
			return true
		}
	}
	return false
}

func possibleDuplicateContains(
	values []RepositoryFindingPossibleDuplicate,
	candidateID string,
) bool {
	for _, candidate := range values {
		if candidate.CandidateID == candidateID {
			return true
		}
	}
	return false
}

func removeRepositoryPossibleDuplicate(
	values []RepositoryFindingPossibleDuplicate,
	candidateID string,
) []RepositoryFindingPossibleDuplicate {
	out := values[:0]
	for _, value := range values {
		if value.CandidateID != candidateID {
			out = append(out, value)
		}
	}
	return out
}

func mergeRepositoryFindingRecords(
	target *RepositoryFinding,
	source RepositoryFinding,
	now time.Time,
) RepositoryFinding {
	merged := *target
	for _, id := range source.ReviewFindingIDs {
		if !containsExactString(merged.ReviewFindingIDs, id) {
			merged.ReviewFindingIDs = append(merged.ReviewFindingIDs, id)
		}
	}
	for _, commit := range source.FoundCommits {
		if !containsExactString(merged.FoundCommits, commit) {
			merged.FoundCommits = append(merged.FoundCommits, commit)
		}
	}
	for _, history := range source.PathSymbolHistory {
		if !pathSymbolHistoryContains(merged.PathSymbolHistory, history.ReviewFindingID) {
			merged.PathSymbolHistory = append(merged.PathSymbolHistory, history)
		}
	}
	sortRepositoryPathSymbolHistory(merged.PathSymbolHistory)
	merged.Issue = mergeRepositoryIssueAssociations(merged.Issue, source.Issue)
	if matchHintsEmpty(merged.MatchHints) && !matchHintsEmpty(source.MatchHints) {
		merged.MatchHints = source.MatchHints
	}
	if merged.FixEffort == (FixEffort{}) && source.FixEffort != (FixEffort{}) {
		merged.FixEffort = source.FixEffort
	}
	merged.ResolutionHistory = mergeRepositoryResolutionHistories(
		merged.ResolutionHistory,
		source.ResolutionHistory,
	)
	for _, duplicate := range source.PossibleDuplicates {
		if duplicate.CandidateID != merged.ID && duplicate.CandidateID != source.ID &&
			!repositoryFindingHasPossibleDuplicate(merged, duplicate.CandidateID) {
			merged.PossibleDuplicates = append(merged.PossibleDuplicates, duplicate)
		}
	}
	merged.PossibleDuplicates = removeRepositoryPossibleDuplicate(merged.PossibleDuplicates, source.ID)
	if moreSevere(merged.CanonicalSeverity, source.CanonicalSeverity) == source.CanonicalSeverity {
		merged.CanonicalSeverity = source.CanonicalSeverity
	}
	if source.Lifecycle == RepositoryFindingRegressed || merged.Lifecycle == RepositoryFindingRegressed {
		merged.Lifecycle = RepositoryFindingRegressed
	}
	merged.MatchState = RepositoryMatchKnown
	merged.Version++
	merged.UpdatedAt = now
	return merged
}

func pathSymbolHistoryContains(values []RepositoryFindingPathSymbol, reviewFindingID string) bool {
	for _, value := range values {
		if value.ReviewFindingID == reviewFindingID {
			return true
		}
	}
	return false
}

func sortRepositoryPathSymbolHistory(values []RepositoryFindingPathSymbol) {
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].ObservedAt.Equal(values[j].ObservedAt) {
			return values[i].ReviewFindingID < values[j].ReviewFindingID
		}
		return values[i].ObservedAt.Before(values[j].ObservedAt)
	})
}

func activeValidationJobIndex(jobs []RepositoryValidationJob, repositoryFindingID string) int {
	for index := len(jobs) - 1; index >= 0; index-- {
		if jobs[index].RepositoryFindingID == repositoryFindingID &&
			(jobs[index].State == RepositoryValidationPending ||
				jobs[index].State == RepositoryValidationRunning) {
			return index
		}
	}
	return -1
}

func validationJobSequence(jobs []RepositoryValidationJob, repositoryFindingID string) int {
	count := 0
	for _, job := range jobs {
		if job.RepositoryFindingID == repositoryFindingID {
			count++
		}
	}
	return count
}

func repositoryValidationTerminal(state RepositoryFindingValidationState) bool {
	return state == RepositoryValidationConfirmed || state == RepositoryValidationNotFixed ||
		state == RepositoryValidationInconclusive || state == RepositoryValidationFailed
}

func normalizeValidationCommits(values []string) ([]string, error) {
	if len(values) > maxValidationCandidateCommits {
		return nil, errors.New("validation candidate commit set exceeds eight")
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if !validRepositoryReviewCommitSHA(value) {
			return nil, errors.New("invalid validation candidate commit")
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, errors.New("duplicate validation candidate commit")
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out, nil
}

func appendBoundedResolution(
	values []RepositoryFindingResolution,
	value RepositoryFindingResolution,
) []RepositoryFindingResolution {
	if len(values) >= maxRepositoryResolutionHistory {
		values = append([]RepositoryFindingResolution(nil), values[len(values)-maxRepositoryResolutionHistory+1:]...)
	}
	return append(values, value)
}

func mergeRepositoryResolutionHistories(
	left, right []RepositoryFindingResolution,
) []RepositoryFindingResolution {
	if len(left) == 0 && len(right) == 0 {
		return nil
	}
	merged := make([]RepositoryFindingResolution, 0, len(left)+len(right))
	merged = append(merged, left...)
	merged = append(merged, right...)
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].ValidatedAt.Before(merged[j].ValidatedAt)
	})
	if len(merged) > maxRepositoryResolutionHistory {
		merged = append(
			[]RepositoryFindingResolution(nil),
			merged[len(merged)-maxRepositoryResolutionHistory:]...,
		)
	}
	return merged
}

func safeLifecycleError(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return "Validation failed."
}

func validRepositoryIssueState(state RepositoryFindingIssueState) bool {
	return state == RepositoryFindingIssueNone || state == RepositoryFindingIssueDraft ||
		state == RepositoryFindingIssueOpen || state == RepositoryFindingIssueClosed ||
		state == RepositoryFindingIssueUnknown
}

func validRepositoryMatchState(state RepositoryMatchState) bool {
	return state == RepositoryMatchNew || state == RepositoryMatchKnown ||
		state == RepositoryMatchProvisional
}

func validRepositoryLifecycle(state RepositoryFindingLifecycle) bool {
	return state == RepositoryFindingOpen || state == RepositoryFindingResolutionPending ||
		state == RepositoryFindingResolved || state == RepositoryFindingRegressed ||
		state == RepositoryFindingDismissed
}

func validRepositoryValidationState(state RepositoryFindingValidationState) bool {
	return state == RepositoryValidationNotRequested || state == RepositoryValidationPending ||
		state == RepositoryValidationRunning || repositoryValidationTerminal(state)
}

func validMappingJobState(state RepositoryMappingJobState) bool {
	return state == RepositoryMappingPending || state == RepositoryMappingRunning ||
		state == RepositoryMappingCompleted
}

func validOptionalLifecycleText(value string, maximum int) bool {
	return value == "" || validBoundedText(value, maximum)
}

func containsExactString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func repositoryFindingAllowsIssueActions(state RepositoryState, finding Finding) bool {
	if finding.DeduplicationPending || finding.RepositoryMatchState == RepositoryMatchProvisional {
		return false
	}
	if finding.RepositoryFindingID == "" {
		for _, job := range state.MappingJobs {
			if job.ReviewFindingID == finding.ID && job.State != RepositoryMappingCompleted {
				return false
			}
		}
		return true // pre-queue legacy compatibility
	}
	index := repositoryFindingIndexByID(state.RepositoryFindings, finding.RepositoryFindingID)
	if index < 0 || state.RepositoryFindings[index].MatchState == RepositoryMatchProvisional {
		return false
	}
	if finding.IssueDraftID != "" {
		return true
	}
	return (state.RepositoryFindings[index].Lifecycle == RepositoryFindingOpen ||
		state.RepositoryFindings[index].Lifecycle == RepositoryFindingRegressed) &&
		(state.RepositoryFindings[index].Issue.State == "" ||
			state.RepositoryFindings[index].Issue.State == RepositoryFindingIssueNone)
}

// RepositoryFindingIssueActionsAllowed exposes the same atomic-ledger
// eligibility projection to protected gateway preflight. Store mutations
// always re-check it under lock.
func RepositoryFindingIssueActionsAllowed(state RepositoryState, finding Finding) bool {
	return repositoryFindingAllowsIssueActions(state, finding)
}

func repositoryFindingHasIssueConflict(state RepositoryState, finding Finding) bool {
	if finding.RepositoryFindingID == "" {
		return false
	}
	index := repositoryFindingIndexByID(state.RepositoryFindings, finding.RepositoryFindingID)
	return index >= 0 && state.RepositoryFindings[index].Issue.Conflict
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validateRepositoryLifecycleState(state RepositoryState) error {
	if len(state.RepositoryFindings) > maxRepositoryFindings ||
		len(state.MappingJobs) > maxRepositoryMappingJobs ||
		len(state.ValidationJobs) > maxRepositoryValidationJobs {
		return errors.New("repository finding lifecycle state exceeds its limit")
	}
	reviewFindings := make(map[string]Finding, len(state.Findings))
	for _, finding := range state.Findings {
		reviewFindings[finding.ID] = finding
	}
	repositoryFindings := make(map[string]RepositoryFinding, len(state.RepositoryFindings))
	for _, finding := range state.RepositoryFindings {
		findingIDSuffix, validFindingID := strings.CutPrefix(finding.ID, "rrf_")
		if !validFindingID || len(findingIDSuffix) != 64 || !validHexDigest(findingIDSuffix) ||
			finding.Repository != state.Repository ||
			!validBoundedText(finding.CanonicalTitle, maxFindingTextBytes) ||
			!validBoundedText(finding.CanonicalSeverity, 16) ||
			!validRepositoryMatchState(finding.MatchState) ||
			!validRepositoryLifecycle(finding.Lifecycle) ||
			!validRepositoryValidationState(finding.ValidationState) ||
			!validRepositoryIssueState(finding.Issue.State) ||
			finding.Version < 1 || finding.CreatedAt.IsZero() || finding.UpdatedAt.IsZero() ||
			finding.UpdatedAt.Before(finding.CreatedAt) || len(finding.ReviewFindingIDs) == 0 ||
			len(finding.PathSymbolHistory) > maxRepositoryPathSymbolHistory ||
			len(finding.PossibleDuplicates) > maxRepositoryPossibleDuplicates ||
			len(finding.ResolutionHistory) > maxRepositoryResolutionHistory {
			return errors.New("invalid repository finding")
		}
		if _, duplicate := repositoryFindings[finding.ID]; duplicate {
			return errors.New("duplicate repository finding ID")
		}
		if !matchHintsEmpty(finding.MatchHints) {
			if err := validateMatchHints(finding.MatchHints); err != nil {
				return err
			}
		}
		if finding.FixEffort != (FixEffort{}) {
			if err := validateFixEffort(finding.FixEffort); err != nil {
				return err
			}
		}
		seenOccurrences := make(map[string]struct{}, len(finding.ReviewFindingIDs))
		for _, reviewFindingID := range finding.ReviewFindingIDs {
			occurrence, exists := reviewFindings[reviewFindingID]
			if !exists || occurrence.RepositoryFindingID != finding.ID {
				return errors.New("repository finding occurrence association is invalid")
			}
			if _, duplicate := seenOccurrences[reviewFindingID]; duplicate {
				return errors.New("duplicate repository finding occurrence")
			}
			seenOccurrences[reviewFindingID] = struct{}{}
		}
		seenCommits := make(map[string]struct{}, len(finding.FoundCommits))
		for _, commit := range finding.FoundCommits {
			if !validBoundedText(commit, 256) {
				return errors.New("invalid repository finding commit")
			}
			if _, duplicate := seenCommits[commit]; duplicate {
				return errors.New("duplicate repository finding commit")
			}
			seenCommits[commit] = struct{}{}
		}
		for _, history := range finding.PathSymbolHistory {
			if _, exists := seenOccurrences[history.ReviewFindingID]; !exists ||
				!validBoundedText(history.CommitSHA, 256) || !validBoundedText(history.Path, 4096) ||
				!validOptionalLifecycleText(history.Symbol, 4096) || history.ObservedAt.IsZero() {
				return errors.New("invalid repository finding path/symbol history")
			}
		}
		duplicates, err := normalizePossibleDuplicates(finding.PossibleDuplicates)
		if err != nil || len(duplicates) != len(finding.PossibleDuplicates) {
			return errors.New("invalid repository finding possible duplicates")
		}
		if finding.MatchState == RepositoryMatchProvisional &&
			!repositoryPossibleDuplicatesAreAmbiguous(finding.PossibleDuplicates) {
			return errors.New("provisional repository finding has no possible duplicate")
		}
		if finding.MatchState != RepositoryMatchProvisional &&
			repositoryPossibleDuplicatesAreAmbiguous(finding.PossibleDuplicates) {
			return errors.New("non-provisional repository finding has unresolved ambiguity")
		}
		issueIdentityRequired := finding.Issue.State == RepositoryFindingIssueOpen ||
			finding.Issue.State == RepositoryFindingIssueClosed
		issueIdentityPartial := (finding.Issue.ExternalID == "") != (finding.Issue.URL == "")
		if issueIdentityPartial || issueIdentityRequired &&
			(!validBoundedText(finding.Issue.ExternalID, 1024) || !validHTTPSURL(finding.Issue.URL)) ||
			finding.Issue.URL != "" && !validHTTPSURL(finding.Issue.URL) {
			return errors.New("invalid repository finding issue association")
		}
		if finding.Issue.Conflict && len(finding.Issue.ConflictURLs) < 2 {
			return errors.New("invalid repository finding issue conflict")
		}
		if finding.Issue.Origin != "" && finding.Issue.Origin != IssueDraftOriginAIGenerated &&
			finding.Issue.Origin != IssueDraftOriginLinked &&
			finding.Issue.Origin != IssueDraftOriginDiscovered &&
			finding.Issue.Origin != IssueDraftOriginLegacy {
			return errors.New("invalid repository finding issue origin")
		}
		seenConflictURLs := make(map[string]struct{}, len(finding.Issue.ConflictURLs))
		for _, issueURL := range finding.Issue.ConflictURLs {
			if !validHTTPSURL(issueURL) {
				return errors.New("invalid repository finding issue conflict URL")
			}
			if _, duplicate := seenConflictURLs[issueURL]; duplicate {
				return errors.New("duplicate repository finding issue conflict URL")
			}
			seenConflictURLs[issueURL] = struct{}{}
		}
		for _, duplicate := range finding.PossibleDuplicates {
			if duplicate.CreatedAt.IsZero() {
				return errors.New("repository finding possible duplicate has no timestamp")
			}
		}
		for _, resolution := range finding.ResolutionHistory {
			if !repositoryValidationTerminal(resolution.Outcome) || resolution.ValidatedAt.IsZero() ||
				!validOptionalLifecycleText(resolution.Summary, maxRepositoryLifecycleTextBytes) ||
				!validRepositoryValidationFailure(resolution.Failure) ||
				(resolution.Failure != nil &&
					(resolution.Outcome != RepositoryValidationFailed ||
						!resolution.Failure.At.Equal(resolution.ValidatedAt))) {
				return errors.New("invalid repository finding resolution history")
			}
			if resolution.Outcome == RepositoryValidationConfirmed {
				if !validRepositoryReviewCommitSHA(resolution.FixCommitSHA) ||
					resolution.FixCommitTime.IsZero() ||
					!validOptionalLifecycleText(resolution.FirstContainingTag, 256) ||
					(resolution.FirstContainingTag != "" &&
						!semanticVersionTagPattern.MatchString(resolution.FirstContainingTag)) {
					return errors.New("invalid confirmed repository finding resolution")
				}
			} else if resolution.FixCommitSHA != "" || !resolution.FixCommitTime.IsZero() ||
				resolution.FirstContainingTag != "" {
				return errors.New("non-confirmed resolution contains fix metadata")
			}
		}
		if finding.Lifecycle == RepositoryFindingResolved &&
			(finding.ValidationState != RepositoryValidationConfirmed ||
				!validRepositoryReviewCommitSHA(finding.FixCommitSHA) || finding.FixCommitTime.IsZero()) {
			return errors.New("resolved repository finding has no confirmed fix")
		}
		if finding.FirstContainingTag != "" &&
			!semanticVersionTagPattern.MatchString(finding.FirstContainingTag) {
			return errors.New("repository finding contains an invalid semantic-version tag")
		}
		repositoryFindings[finding.ID] = finding
	}
	for _, finding := range state.Findings {
		if finding.PostResolutionVerified != (finding.PostResolutionFixCommit != "") ||
			finding.PostResolutionVerified != (finding.PostResolutionFindingID != "") ||
			(finding.PostResolutionFixCommit != "" &&
				!validRepositoryReviewCommitSHA(finding.PostResolutionFixCommit)) ||
			(finding.PostResolutionFindingID != "" &&
				repositoryFindings[finding.PostResolutionFindingID].ID == "") {
			return errors.New("invalid review-finding regression proof")
		}
		if finding.RepositoryFindingID == "" {
			if finding.RepositoryMatchState != "" {
				return errors.New("unassociated review finding has a match state")
			}
			continue
		}
		repositoryFinding, exists := repositoryFindings[finding.RepositoryFindingID]
		if !exists || !validRepositoryMatchState(finding.RepositoryMatchState) ||
			finding.RepositoryMatchState != repositoryFinding.MatchState ||
			!containsExactString(repositoryFinding.ReviewFindingIDs, finding.ID) {
			return errors.New("invalid review-finding repository association")
		}
	}
	for _, finding := range state.RepositoryFindings {
		for _, duplicate := range finding.PossibleDuplicates {
			if duplicate.CandidateID == finding.ID {
				return errors.New("repository finding references itself as a possible duplicate")
			}
			if _, exists := repositoryFindings[duplicate.CandidateID]; !exists {
				return errors.New("repository finding possible duplicate is missing")
			}
		}
	}
	mappingIDs := make(map[string]struct{}, len(state.MappingJobs))
	for _, job := range state.MappingJobs {
		if job.ID != mappingJobID(job.ReviewFindingID) || !validMappingJobState(job.State) ||
			job.Attempts < 0 || job.CreatedAt.IsZero() || job.UpdatedAt.IsZero() ||
			job.UpdatedAt.Before(job.CreatedAt) ||
			!validOptionalLifecycleText(job.Error, 1024) ||
			!validOptionalLifecycleText(job.CandidateUniverse, 128) {
			return errors.New("invalid repository mapping job")
		}
		if _, exists := reviewFindings[job.ReviewFindingID]; !exists {
			return errors.New("repository mapping job occurrence is missing")
		}
		// ID is derived solely from ReviewFindingID, so this also rejects a
		// second mapping job for the same occurrence.
		if _, duplicate := mappingIDs[job.ID]; duplicate {
			return errors.New("duplicate repository mapping job ID")
		}
		if (job.State == RepositoryMappingRunning) == job.ReservedAt.IsZero() ||
			(job.State != RepositoryMappingCompleted && job.RepositoryFindingID != "") {
			return errors.New("invalid repository mapping job reservation")
		}
		if err := validateMappingModelSnapshot(job.ModelSnapshot); err != nil {
			return err
		}
		if !mappingAdjudicationEmpty(job.Adjudication) {
			if err := validateMappingAdjudication(job.Adjudication); err != nil ||
				job.CandidateUniverse == "" {
				if err == nil {
					err = errors.New("mapping adjudication has no candidate universe")
				}
				return err
			}
		}
		if job.State == RepositoryMappingCompleted {
			occurrence := reviewFindings[job.ReviewFindingID]
			if job.RepositoryFindingID == "" || occurrence.RepositoryFindingID != job.RepositoryFindingID {
				return errors.New("completed mapping job association is invalid")
			}
		}
		mappingIDs[job.ID] = struct{}{}
	}
	validationIDs := make(map[string]struct{}, len(state.ValidationJobs))
	for _, job := range state.ValidationJobs {
		validationIDSuffix, validValidationID := strings.CutPrefix(job.ID, "rvj_")
		if !validValidationID || len(validationIDSuffix) != 64 ||
			!validHexDigest(validationIDSuffix) || !validRepositoryValidationState(job.State) ||
			job.State == RepositoryValidationNotRequested || job.Attempts < 0 || job.FindingVersion < 1 ||
			job.CreatedAt.IsZero() || job.UpdatedAt.IsZero() || job.UpdatedAt.Before(job.CreatedAt) ||
			!validOptionalLifecycleText(job.Error, 1024) ||
			len(job.CandidateCommits) > maxValidationCandidateCommits {
			return errors.New("invalid repository validation job")
		}
		if _, exists := repositoryFindings[job.RepositoryFindingID]; !exists {
			return errors.New("repository validation job finding is missing")
		}
		if _, duplicate := validationIDs[job.ID]; duplicate {
			return errors.New("duplicate repository validation job ID")
		}
		if (job.State == RepositoryValidationRunning) == job.ReservedAt.IsZero() {
			return errors.New("invalid repository validation job reservation")
		}
		if !validRepositoryValidationFailure(job.Failure) ||
			(job.Failure != nil &&
				(job.State != RepositoryValidationFailed ||
					job.Failure.At.Before(job.CreatedAt) || job.Failure.At.After(job.UpdatedAt))) {
			return errors.New("invalid repository validation job failure")
		}
		if err := validateMappingModelSnapshot(job.ModelSnapshot); err != nil {
			return err
		}
		if _, err := normalizeValidationCommits(job.CandidateCommits); err != nil {
			return err
		}
		validationIDs[job.ID] = struct{}{}
	}
	return nil
}

func matchHintsEmpty(hints MatchHints) bool {
	return hints.Component == "" && hints.Operation == "" && hints.FailureMode == "" &&
		hints.Trigger == "" && hints.ViolatedInvariant == "" && hints.ObservableOutcome == "" &&
		len(hints.RelatedSymbols) == 0 && len(hints.SourceAnchors) == 0 &&
		len(hints.DistinguishingFacts) == 0
}
