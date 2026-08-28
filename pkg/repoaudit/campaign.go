package repoaudit

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

const (
	repositoryReviewCampaignIDPrefix         = "rrc_"
	maxRepositoryReviewRequiredAssignments   = maxAutomationReviewers * 4
	maxRepositoryReviewCampaignRecoveryBytes = 32 << 20
)

// NewRepositoryReviewCampaignID returns a new opaque controller-owned campaign
// identity. Workflows may carry this value, but only BeginCampaign can install
// it as durable authority for a repository ledger.
func NewRepositoryReviewCampaignID() string {
	return repositoryReviewCampaignIDPrefix + strings.ToLower(rand.Text())
}

// ValidRepositoryReviewCampaignID reports whether value has the bounded opaque
// shape accepted by repository-review persistence.
func ValidRepositoryReviewCampaignID(value string) bool {
	if !strings.HasPrefix(value, repositoryReviewCampaignIDPrefix) ||
		len(value) <= len(repositoryReviewCampaignIDPrefix) || len(value) > 128 {
		return false
	}
	for index, character := range value[len(repositoryReviewCampaignIDPrefix):] {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			index > 0 && (character == '_' || character == '-') {
			continue
		}
		return false
	}
	return true
}

// BeginCampaignRequest is the trusted controller authorization boundary for a
// new current campaign. ExpectedReviewVersion fences replacement of any prior
// campaign; an exact replay of the same authorization is idempotent even after
// later review checkpoints have advanced that version.
type BeginCampaignRequest struct {
	Repository            string `json:"repository"`
	CampaignID            string `json:"campaign_id"`
	ExpectedCampaignID    string `json:"expected_campaign_id,omitempty"`
	CommitSHA             string `json:"commit_sha"`
	ExpectedReviewVersion int64  `json:"expected_review_version"`
	Exact                 bool   `json:"exact"`
}

// ReconcileCampaignRequest is the trusted recovery/backfill mutation for an
// already-authorized current campaign. Coverage is merged monotonically and
// Exact may only be promoted from false to true. Legacy record IDs are tagged
// atomically with the same state update.
type ReconcileCampaignRequest struct {
	Repository            string                                `json:"repository"`
	ExpectedReviewVersion int64                                 `json:"expected_review_version"`
	Coverage              RepositoryReviewCampaignCoverage      `json:"coverage"`
	SelectedScope         []FileRef                             `json:"selected_scope"`
	Runs                  []RepositoryReviewCampaignRunRecovery `json:"runs,omitempty"`
	ContextIDs            []string                              `json:"context_ids,omitempty"`
	FindingIDs            []string                              `json:"finding_ids,omitempty"`
}

// RepositoryReviewCampaignRunRecovery tags one retained legacy run and
// installs its exact successful-child inspection count during backfill.
type RepositoryReviewCampaignRunRecovery struct {
	ID              string `json:"id"`
	Plan            Plan   `json:"plan"`
	InspectedFiles  int    `json:"inspected_files"`
	LegacyRecovered bool   `json:"legacy_recovered,omitempty"`
}

// BeginCampaign installs controller-owned campaign authority without accepting
// inventory or profile data from the controller. The first matching Plan or
// Record binds that remaining immutable metadata. Plan and Record can never
// replace an already-authorized campaign.
func (s Store) BeginCampaign(
	ctx context.Context,
	request BeginCampaignRequest,
) (RepositoryState, error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return RepositoryState{}, contextErr
	}
	request.Repository = strings.TrimSpace(request.Repository)
	request.CampaignID = strings.TrimSpace(request.CampaignID)
	request.ExpectedCampaignID = strings.TrimSpace(request.ExpectedCampaignID)
	request.CommitSHA = strings.ToLower(strings.TrimSpace(request.CommitSHA))
	if !validBoundedText(request.Repository, maxRepositoryIdentityBytes) ||
		!ValidRepositoryReviewCampaignID(request.CampaignID) ||
		(request.ExpectedCampaignID != "" &&
			!ValidRepositoryReviewCampaignID(request.ExpectedCampaignID)) ||
		!validRepositoryReviewCommitSHA(request.CommitSHA) ||
		request.ExpectedReviewVersion < 0 {
		return RepositoryState{}, ErrInvalidPlan
	}
	unlock, err := s.lock(request.Repository)
	if err != nil {
		return RepositoryState{}, err
	}
	defer unlock()
	if contextErr := ctx.Err(); contextErr != nil {
		return RepositoryState{}, contextErr
	}
	state, err := s.load(request.Repository)
	if err != nil {
		return RepositoryState{}, err
	}
	if current := state.CurrentCampaign; current != nil && current.ID == request.CampaignID {
		if current.CommitSHA != request.CommitSHA || request.Exact && !current.Exact {
			return RepositoryState{}, ErrConflict
		}
		return state, nil
	}
	if _, reused := state.CampaignHistory[request.CampaignID]; reused {
		return RepositoryState{}, ErrConflict
	}
	if state.ReviewVersion != request.ExpectedReviewVersion {
		return RepositoryState{}, ErrConflict
	}
	currentCampaignID := ""
	if state.CurrentCampaign != nil {
		currentCampaignID = state.CurrentCampaign.ID
	}
	if currentCampaignID != request.ExpectedCampaignID {
		return RepositoryState{}, ErrConflict
	}
	state.CurrentCampaign = &RepositoryReviewCampaignCoverage{
		ID: request.CampaignID, CommitSHA: request.CommitSHA,
		Exact: request.Exact, Paths: make(map[string]RepositoryReviewCampaignPathCoverage),
	}
	if state.CampaignHistory == nil {
		state.CampaignHistory = make(map[string]string)
	}
	state.CampaignHistory[request.CampaignID] = request.CommitSHA
	state.ActiveForceCampaignID = ""
	state.ActiveForceProfileHash = ""
	state.ActiveForceCommitSHA = ""
	state.Version++
	state.ReviewVersion++
	state.UpdatedAt = s.clock()
	if err := s.save(&state); err != nil {
		return RepositoryState{}, err
	}
	return state, nil
}

// ReconcileCampaign installs recovered lower-bound coverage and legacy tags
// without granting authority to create or replace a campaign. Exact replays
// are idempotent even after the review version advances.
func (s Store) ReconcileCampaign(
	ctx context.Context,
	request ReconcileCampaignRequest,
) (RepositoryState, error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return RepositoryState{}, contextErr
	}
	request.Repository = strings.TrimSpace(request.Repository)
	pathsDeclared := request.Coverage.Paths != nil
	scopeDeclared := request.SelectedScope != nil
	recoveryDigestDeclared := request.Coverage.RecoveryDigest != ""
	request.Coverage = cloneRepositoryReviewCampaignCoverage(request.Coverage)
	if !validBoundedText(request.Repository, maxRepositoryIdentityBytes) ||
		request.ExpectedReviewVersion < 0 ||
		!pathsDeclared ||
		!scopeDeclared ||
		recoveryDigestDeclared ||
		!repositoryReviewCampaignScopeBound(&request.Coverage) ||
		validateRepositoryReviewCampaignCoverage(&request.Coverage) != nil {
		return RepositoryState{}, ErrInvalidPlan
	}
	var err error
	request.SelectedScope, err = canonicalRepositoryReviewCampaignFiles(request.SelectedScope)
	if err != nil || len(request.SelectedScope) != request.Coverage.SelectedFiles {
		return RepositoryState{}, ErrInvalidPlan
	}
	scopeDigest, err := repositoryReviewCampaignScopeDigestForFiles(request.SelectedScope)
	if err != nil || scopeDigest != request.Coverage.ScopeDigest {
		return RepositoryState{}, ErrInvalidPlan
	}
	selectedScope := make(map[string]FileRef, len(request.SelectedScope))
	for _, file := range request.SelectedScope {
		selectedScope[file.Path] = file
	}
	for pathValue := range request.Coverage.Paths {
		if _, selected := selectedScope[pathValue]; !selected {
			return RepositoryState{}, ErrInvalidPlan
		}
	}
	request.Runs, err = normalizeRepositoryReviewCampaignRuns(request.Runs)
	if err != nil {
		return RepositoryState{}, err
	}
	for _, recoveredRun := range request.Runs {
		if !recoveredRun.LegacyRecovered {
			continue
		}
		if recoveredRun.Plan.CampaignID != "" || recoveredRun.Plan.RequiredAssignments != 0 ||
			recoveredRun.Plan.Repository != request.Repository ||
			recoveredRun.Plan.CommitSHA != request.Coverage.CommitSHA ||
			recoveredRun.Plan.InventoryHash != request.Coverage.InventoryHash ||
			!recoveredRun.Plan.Authoritative {
			return RepositoryState{}, ErrInvalidPlan
		}
		// normalizeRepositoryReviewCampaignRuns already validated this exact
		// immutable plan manifest.
		manifest, _ := repositoryReviewCampaignFilesForPlan(recoveredRun.Plan)
		for _, file := range manifest {
			if selectedScope[file.Path] != file {
				return RepositoryState{}, ErrInvalidPlan
			}
		}
	}
	request.ContextIDs, err = normalizeRepositoryReviewCampaignRecordIDs(
		request.ContextIDs, 1_000_000, 256,
	)
	if err != nil {
		return RepositoryState{}, err
	}
	request.FindingIDs, err = normalizeRepositoryReviewCampaignRecordIDs(
		request.FindingIDs, maxReviewObservations, 256,
	)
	if err != nil {
		return RepositoryState{}, err
	}
	recoveryDigest, err := repositoryReviewCampaignRecoveryDigest(request)
	if err != nil {
		return RepositoryState{}, err
	}
	unlock, err := s.lock(request.Repository)
	if err != nil {
		return RepositoryState{}, err
	}
	defer unlock()
	if contextErr := ctx.Err(); contextErr != nil {
		return RepositoryState{}, contextErr
	}
	state, err := s.load(request.Repository)
	if err != nil {
		return RepositoryState{}, err
	}
	current := state.CurrentCampaign
	if current == nil || current.ID != request.Coverage.ID ||
		current.CommitSHA != request.Coverage.CommitSHA {
		return RepositoryState{}, ErrConflict
	}
	// load validates unique retained record identities while holding the same
	// repository lock, so index construction cannot conflict here.
	indexes, _ := newRepositoryReviewCampaignIndexes(state)
	recoveredRuns := make(map[string]RepositoryReviewCampaignRunRecovery, len(request.Runs))
	for _, recoveredRun := range request.Runs {
		recoveredRuns[recoveredRun.ID] = recoveredRun
	}
	if state.ReviewVersion != request.ExpectedReviewVersion {
		if current.RecoveryDigest == recoveryDigest {
			return state, nil
		}
		return RepositoryState{}, ErrConflict
	}
	nextCoverage := cloneRepositoryReviewCampaignCoverage(*current)
	temporary := RepositoryState{CurrentCampaign: &nextCoverage}
	bound, err := bindRepositoryReviewCampaignScope(
		&temporary,
		request.Coverage.ID,
		request.Coverage.CommitSHA,
		request.Coverage.InventoryHash,
		request.Coverage.ProfileHash,
		request.Coverage.ScopeDigest,
		request.Coverage.RequiredAssignments,
		request.Coverage.SelectedFiles,
	)
	if err != nil {
		return RepositoryState{}, err
	}
	changed := bound
	for pathValue, pathCoverage := range request.Coverage.Paths {
		merged, mergeErr := mergeRepositoryReviewCampaignPath(
			temporary.CurrentCampaign, pathValue, pathCoverage,
		)
		if mergeErr != nil {
			return RepositoryState{}, mergeErr
		}
		changed = changed || merged
	}
	if request.Coverage.Exact && !temporary.CurrentCampaign.Exact {
		temporary.CurrentCampaign.Exact = true
		changed = true
	}
	if err := validateRepositoryReviewCampaignCoverage(temporary.CurrentCampaign); err != nil {
		return RepositoryState{}, err
	}
	state.CurrentCampaign = temporary.CurrentCampaign
	for _, recoveredRun := range request.Runs {
		index, matched := indexes.runs[recoveredRun.ID]
		if !matched {
			return RepositoryState{}, ErrConflict
		}
		run := &state.Runs[index]
		if recoveredRun.Plan.Repository != state.Repository ||
			!repositoryReviewCampaignRunMatchesCoverage(*run, recoveredRun, request.Coverage) {
			return RepositoryState{}, ErrConflict
		}
		wasTagged := run.CampaignID != ""
		if wasTagged && run.CampaignID != request.Coverage.ID {
			return RepositoryState{}, ErrConflict
		}
		expectedProfileHash := request.Coverage.ProfileHash
		expectedScopeDigest := request.Coverage.ScopeDigest
		if recoveredRun.LegacyRecovered {
			expectedProfileHash = recoveredRun.Plan.ProfileHash
			expectedScopeDigest, _ = repositoryReviewCampaignScopeDigestForPlan(recoveredRun.Plan)
		}
		if wasTagged && (run.ProfileHash != expectedProfileHash ||
			run.ScopeDigest != expectedScopeDigest ||
			run.LegacyRecovered != recoveredRun.LegacyRecovered ||
			run.InspectedFiles != recoveredRun.InspectedFiles) {
			return RepositoryState{}, ErrConflict
		}
		if !wasTagged {
			run.CampaignID = request.Coverage.ID
			run.ProfileHash = expectedProfileHash
			run.ScopeDigest = expectedScopeDigest
			run.InspectedFiles = recoveredRun.InspectedFiles
			run.LegacyRecovered = recoveredRun.LegacyRecovered
			changed = true
		}
	}
	for _, contextID := range request.ContextIDs {
		index, found := indexes.contexts[contextID]
		if !found {
			return RepositoryState{}, ErrConflict
		}
		contextRecord := &state.Contexts[index]
		if !repositoryReviewCampaignContextMatchesCoverage(
			state, *contextRecord, request.Coverage, selectedScope, indexes, recoveredRuns,
		) {
			return RepositoryState{}, ErrConflict
		}
		if contextRecord.CampaignID != "" && contextRecord.CampaignID != request.Coverage.ID {
			return RepositoryState{}, ErrConflict
		}
		if contextRecord.CampaignID == "" {
			contextRecord.CampaignID = request.Coverage.ID
			changed = true
		}
	}
	for _, findingID := range request.FindingIDs {
		index, found := indexes.findings[findingID]
		if !found {
			return RepositoryState{}, ErrConflict
		}
		finding := &state.Findings[index]
		if !repositoryReviewCampaignFindingMatchesCoverage(
			state, *finding, request.Coverage, selectedScope, indexes, recoveredRuns,
		) {
			return RepositoryState{}, ErrConflict
		}
		if finding.CampaignID == "" {
			finding.CampaignID = request.Coverage.ID
			changed = true
		}
	}
	if state.CurrentCampaign.RecoveryDigest != recoveryDigest {
		state.CurrentCampaign.RecoveryDigest = recoveryDigest
		changed = true
	}
	if !changed {
		return state, nil
	}
	state.Version++
	state.ReviewVersion++
	state.UpdatedAt = s.clock()
	if err := s.save(&state); err != nil {
		return RepositoryState{}, err
	}
	return state, nil
}

// RepositoryReviewCampaignMetrics is an exact current-campaign projection when
// CoverageAvailable and CoverageExact are both true. Otherwise path counts are
// durable lower bounds. Finding counts are exact for a nonempty CampaignID and
// use legacy run/context membership only when CampaignID is empty.
type RepositoryReviewCampaignMetrics struct {
	CampaignID             string `json:"campaign_id,omitempty"`
	CoverageAvailable      bool   `json:"coverage_available"`
	CoverageExact          bool   `json:"coverage_exact"`
	SelectedFiles          int    `json:"selected_files"`
	InspectedFiles         int    `json:"inspected_files"`
	CompletedFiles         int    `json:"completed_files"`
	UnsupportedFiles       int    `json:"unsupported_files"`
	RemainingFiles         int    `json:"remaining_files"`
	FindingOccurrences     int    `json:"finding_occurrences"`
	FindingAggregates      int    `json:"finding_aggregates"`
	PendingFindingMappings int    `json:"pending_finding_mappings"`
}

// CurrentCampaignFindingsByID selects new campaign-tagged findings without
// consulting bounded run history. An empty campaign ID retains the legacy
// run/context selection contract.
func CurrentCampaignFindingsByID(
	state RepositoryState,
	campaignID string,
	legacyRunIDs []string,
	legacyStartedAt time.Time,
) []Finding {
	campaignID = strings.TrimSpace(campaignID)
	if campaignID == "" {
		return CurrentCampaignFindings(state, legacyRunIDs, legacyStartedAt)
	}
	out := make([]Finding, 0)
	for _, finding := range state.Findings {
		if finding.CampaignID == campaignID {
			out = append(out, finding)
		}
	}
	return out
}

// CurrentCampaignMetrics derives unique path and finding counts from durable
// campaign authority. Repository-level aggregate mappings remain live, so the
// distinct aggregate count may decrease when provisional duplicates merge.
func CurrentCampaignMetrics(
	state RepositoryState,
	campaignID string,
	legacyRunIDs []string,
	legacyStartedAt time.Time,
) RepositoryReviewCampaignMetrics {
	campaignID = strings.TrimSpace(campaignID)
	metrics := RepositoryReviewCampaignMetrics{CampaignID: campaignID}
	if coverage := state.CurrentCampaign; coverage != nil && coverage.ID == campaignID && campaignID != "" &&
		repositoryReviewCampaignScopeBound(coverage) {
		metrics.CoverageAvailable = true
		metrics.CoverageExact = coverage.Exact
		metrics.SelectedFiles = coverage.SelectedFiles
		terminal := 0
		for _, pathCoverage := range coverage.Paths {
			if pathCoverage.Inspected {
				metrics.InspectedFiles++
			}
			if pathCoverage.Completed {
				metrics.CompletedFiles++
				terminal++
			}
			if pathCoverage.Unsupported {
				metrics.UnsupportedFiles++
				terminal++
			}
		}
		metrics.RemainingFiles = max(0, coverage.SelectedFiles-terminal)
	}
	aggregates := make(map[string]struct{})
	for _, finding := range CurrentCampaignFindingsByID(
		state, campaignID, legacyRunIDs, legacyStartedAt,
	) {
		metrics.FindingOccurrences++
		if finding.RepositoryFindingID == "" {
			metrics.PendingFindingMappings++
			continue
		}
		aggregates[finding.RepositoryFindingID] = struct{}{}
	}
	metrics.FindingAggregates = len(aggregates)
	return metrics
}

func repositoryReviewCampaignScopeBound(coverage *RepositoryReviewCampaignCoverage) bool {
	return coverage != nil && coverage.InventoryHash != "" && coverage.ProfileHash != ""
}

func bindRepositoryReviewCampaignScope(
	state *RepositoryState,
	campaignID string,
	commitSHA string,
	inventoryHash string,
	profileHash string,
	scopeDigest string,
	requiredAssignments int,
	selectedFiles int,
) (bool, error) {
	if state == nil || !ValidRepositoryReviewCampaignID(campaignID) ||
		!validRepositoryReviewCommitSHA(commitSHA) ||
		!validBoundedText(inventoryHash, 256) || !validBoundedText(profileHash, 256) ||
		!validRepositoryReviewCampaignScopeDigest(scopeDigest) ||
		requiredAssignments < 1 || requiredAssignments > maxRepositoryReviewRequiredAssignments ||
		selectedFiles < 0 || selectedFiles > maxReviewFiles {
		return false, ErrInvalidPlan
	}
	coverage := state.CurrentCampaign
	if coverage == nil || coverage.ID != campaignID || coverage.CommitSHA != commitSHA {
		return false, ErrConflict
	}
	if repositoryReviewCampaignScopeBound(coverage) {
		if coverage.InventoryHash != inventoryHash || coverage.ProfileHash != profileHash ||
			coverage.ScopeDigest != scopeDigest ||
			coverage.RequiredAssignments != requiredAssignments ||
			coverage.SelectedFiles != selectedFiles {
			return false, ErrConflict
		}
		return false, nil
	}
	if coverage.InventoryHash != "" || coverage.ProfileHash != "" || coverage.ScopeDigest != "" ||
		coverage.RequiredAssignments != 0 ||
		coverage.SelectedFiles != 0 ||
		len(coverage.Paths) != 0 {
		return false, errors.New("invalid unbound repository review campaign")
	}
	coverage.InventoryHash = inventoryHash
	coverage.ProfileHash = profileHash
	coverage.ScopeDigest = scopeDigest
	coverage.RequiredAssignments = requiredAssignments
	coverage.SelectedFiles = selectedFiles
	if coverage.Paths == nil {
		coverage.Paths = make(map[string]RepositoryReviewCampaignPathCoverage)
	}
	return true, nil
}

func mergeRepositoryReviewCampaignPath(
	coverage *RepositoryReviewCampaignCoverage,
	pathValue string,
	update RepositoryReviewCampaignPathCoverage,
) (bool, error) {
	if coverage == nil || !repositoryReviewCampaignScopeBound(coverage) ||
		!validRepositoryReviewPath(pathValue) ||
		(!update.Inspected && !update.Completed && !update.Unsupported) ||
		(update.Unsupported && (update.Inspected || update.Completed)) {
		return false, ErrInvalidPlan
	}
	if coverage.Paths == nil {
		coverage.Paths = make(map[string]RepositoryReviewCampaignPathCoverage)
	}
	current := coverage.Paths[pathValue]
	if current.Unsupported && (update.Inspected || update.Completed) ||
		update.Unsupported && (current.Inspected || current.Completed) {
		return false, ErrConflict
	}
	next := RepositoryReviewCampaignPathCoverage{
		Inspected:   current.Inspected || update.Inspected,
		Completed:   current.Completed || update.Completed,
		Unsupported: current.Unsupported || update.Unsupported,
	}
	if next == current {
		return false, nil
	}
	coverage.Paths[pathValue] = next
	return true, nil
}

func validateRepositoryReviewCampaignCoverage(
	coverage *RepositoryReviewCampaignCoverage,
) error {
	if coverage == nil {
		return nil
	}
	if !ValidRepositoryReviewCampaignID(coverage.ID) ||
		!validRepositoryReviewCommitSHA(coverage.CommitSHA) ||
		(coverage.RecoveryDigest != "" && !validRepositoryReviewCampaignRecoveryDigest(coverage.RecoveryDigest)) ||
		coverage.Paths == nil || coverage.SelectedFiles < 0 ||
		coverage.SelectedFiles > maxReviewFiles || len(coverage.Paths) > maxReviewFiles {
		return errors.New("invalid repository review campaign coverage")
	}
	bound := repositoryReviewCampaignScopeBound(coverage)
	if (coverage.InventoryHash == "") != (coverage.ProfileHash == "") ||
		(bound && (!validBoundedText(coverage.InventoryHash, 256) ||
			!validBoundedText(coverage.ProfileHash, 256) ||
			!validRepositoryReviewCampaignScopeDigest(coverage.ScopeDigest) ||
			coverage.RequiredAssignments < 1 ||
			coverage.RequiredAssignments > maxRepositoryReviewRequiredAssignments ||
			coverage.InventoryHash != strings.TrimSpace(coverage.InventoryHash) ||
			coverage.ProfileHash != strings.TrimSpace(coverage.ProfileHash))) ||
		(!bound && (coverage.ScopeDigest != "" || coverage.RequiredAssignments != 0 ||
			coverage.SelectedFiles != 0 || len(coverage.Paths) != 0)) {
		return errors.New("invalid repository review campaign scope binding")
	}
	metadataBytes := 0
	terminal := 0
	for pathValue, pathCoverage := range coverage.Paths {
		metadataBytes += len(pathValue) + 32
		if metadataBytes > maxReviewFileMetadataBytes || !validRepositoryReviewPath(pathValue) ||
			(!pathCoverage.Inspected && !pathCoverage.Completed && !pathCoverage.Unsupported) ||
			(pathCoverage.Unsupported && (pathCoverage.Inspected || pathCoverage.Completed)) {
			return errors.New("invalid repository review campaign path coverage")
		}
		if pathCoverage.Completed || pathCoverage.Unsupported {
			terminal++
		}
	}
	if bound && (len(coverage.Paths) > coverage.SelectedFiles || terminal > coverage.SelectedFiles) {
		return errors.New("repository review campaign coverage exceeds selected scope")
	}
	return nil
}

func validateRepositoryReviewCampaignHistory(history map[string]string) error {
	if len(history) > maxReviewFiles {
		return errors.New("repository review campaign history exceeds its limit")
	}
	metadataBytes := 0
	for campaignID, commitSHA := range history {
		metadataBytes += len(campaignID) + len(commitSHA) + 8
		if metadataBytes > maxReviewFileMetadataBytes ||
			!ValidRepositoryReviewCampaignID(campaignID) ||
			!validRepositoryReviewCommitSHA(commitSHA) {
			return errors.New("invalid repository review campaign history")
		}
	}
	return nil
}

func migrateRepositoryReviewCampaignHistory(state *RepositoryState) (bool, error) {
	if state == nil {
		return false, nil
	}
	bindings := make(map[string]string)
	add := func(campaignID, commitSHA string) error {
		if campaignID == "" {
			return nil
		}
		if !ValidRepositoryReviewCampaignID(campaignID) ||
			!validRepositoryReviewCommitSHA(commitSHA) {
			return errors.New("invalid tagged repository review campaign history")
		}
		if existing := bindings[campaignID]; existing != "" && existing != commitSHA {
			return errors.New("repository review campaign history commit conflict")
		}
		bindings[campaignID] = commitSHA
		return nil
	}
	if state.CurrentCampaign != nil {
		if err := add(state.CurrentCampaign.ID, state.CurrentCampaign.CommitSHA); err != nil {
			return false, err
		}
	}
	for _, run := range state.Runs {
		if err := add(run.CampaignID, run.CommitSHA); err != nil {
			return false, err
		}
	}
	for _, contextRecord := range state.Contexts {
		if err := add(contextRecord.CampaignID, contextRecord.CommitSHA); err != nil {
			return false, err
		}
	}
	for _, finding := range state.Findings {
		if err := add(finding.CampaignID, finding.CommitSHA); err != nil {
			return false, err
		}
	}
	if len(bindings) == 0 {
		return false, nil
	}
	if state.CampaignHistory == nil {
		state.CampaignHistory = make(map[string]string, len(bindings))
	}
	changed := false
	for campaignID, commitSHA := range bindings {
		if existing := state.CampaignHistory[campaignID]; existing != "" && existing != commitSHA {
			return false, errors.New("repository review campaign history commit conflict")
		}
		if state.CampaignHistory[campaignID] == "" {
			state.CampaignHistory[campaignID] = commitSHA
			changed = true
		}
	}
	return changed, nil
}

func validRepositoryReviewCampaignRecoveryDigest(value string) bool {
	digest, ok := strings.CutPrefix(value, "sha256:")
	return ok && len(digest) == 64 && validHexDigest(digest)
}

func validRepositoryReviewCampaignScopeDigest(value string) bool {
	digest, ok := strings.CutPrefix(value, "sha256:")
	return ok && len(digest) == 64 && validHexDigest(digest)
}

func cloneRepositoryReviewCampaignCoverage(
	coverage RepositoryReviewCampaignCoverage,
) RepositoryReviewCampaignCoverage {
	if coverage.Paths == nil {
		return coverage
	}
	paths := make(map[string]RepositoryReviewCampaignPathCoverage, len(coverage.Paths))
	for pathValue, pathCoverage := range coverage.Paths {
		paths[pathValue] = pathCoverage
	}
	coverage.Paths = paths
	return coverage
}

func normalizeRepositoryReviewCampaignRecordIDs(
	values []string,
	maximum int,
	maximumBytes int,
) ([]string, error) {
	if len(values) > maximum {
		return nil, ErrInvalidPlan
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	metadataBytes := 0
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		metadataBytes += len(value) + 8
		if value != raw || !validBoundedText(value, maximumBytes) ||
			metadataBytes > maxRepositoryReviewCampaignRecoveryBytes {
			return nil, ErrInvalidPlan
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, ErrInvalidPlan
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
}

func normalizeRepositoryReviewCampaignRuns(
	runs []RepositoryReviewCampaignRunRecovery,
) ([]RepositoryReviewCampaignRunRecovery, error) {
	if len(runs) > maxAutomationRunIDs {
		return nil, ErrInvalidPlan
	}
	out := make([]RepositoryReviewCampaignRunRecovery, 0, len(runs))
	seen := make(map[string]struct{}, len(runs))
	envelopeBytes := 0
	for _, run := range runs {
		id := strings.TrimSpace(run.ID)
		if _, err := repositoryReviewCampaignScopeDigestForPlan(run.Plan); err != nil {
			return nil, ErrInvalidPlan
		}
		encodedPlan, _ := json.Marshal(run.Plan)
		envelopeBytes += len(encodedPlan) + len(id) + 32
		if id != run.ID || !validBoundedText(id, 1024) ||
			envelopeBytes > maxRepositoryReviewCampaignRecoveryBytes ||
			run.Plan.ID == "" || run.Plan.ID != planDigest(run.Plan) &&
			(!run.LegacyRecovered || run.Plan.ID != legacyRepositoryReviewPlanDigest(run.Plan)) ||
			(run.LegacyRecovered && (run.Plan.CampaignID != "" || !run.Plan.Authoritative ||
				run.Plan.RequiredAssignments != 0)) ||
			run.InspectedFiles < 0 ||
			run.InspectedFiles > maxReviewFiles {
			return nil, ErrInvalidPlan
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, ErrInvalidPlan
		}
		seen[id] = struct{}{}
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func legacyRepositoryReviewPlanDigest(plan Plan) string {
	if plan.TargetBranch != "" || plan.AdvertisedDefaultBranch != "" || plan.TargetIsDefault {
		return ""
	}
	legacy, _ := json.Marshal(struct {
		ID                 string            `json:"id"`
		Repository         string            `json:"repository"`
		CommitSHA          string            `json:"commit_sha"`
		InventoryHash      string            `json:"inventory_hash"`
		ProfileHash        string            `json:"profile_hash"`
		ForceCampaignID    string            `json:"force_campaign_id,omitempty"`
		Authoritative      bool              `json:"authoritative,omitempty"`
		StateVersion       int64             `json:"state_version"`
		PendingFiles       []FileRef         `json:"pending_files"`
		DeferredFiles      []FileRef         `json:"deferred_files,omitempty"`
		UnchangedFiles     []FileRef         `json:"unchanged_files"`
		UnsupportedFiles   []UnsupportedFile `json:"unsupported_files,omitempty"`
		PreviouslyReviewed int               `json:"previously_reviewed"`
		CreatedAt          time.Time         `json:"created_at"`
	}{
		Repository: plan.Repository, CommitSHA: plan.CommitSHA,
		InventoryHash: plan.InventoryHash, ProfileHash: plan.ProfileHash,
		ForceCampaignID: plan.ForceCampaignID, Authoritative: plan.Authoritative,
		StateVersion: plan.StateVersion, PendingFiles: plan.PendingFiles,
		DeferredFiles: plan.DeferredFiles, UnchangedFiles: plan.UnchangedFiles,
		UnsupportedFiles: plan.UnsupportedFiles, PreviouslyReviewed: plan.PreviouslyReviewed,
		CreatedAt: plan.CreatedAt,
	})
	return stableID("rpl_", string(legacy))
}

// ValidateRepositoryReviewCampaignRunRecovery verifies one retained legacy
// run and its digest-bound plan without mutating repository state.
func ValidateRepositoryReviewCampaignRunRecovery(
	run RepositoryReviewCampaignRunRecovery,
) error {
	_, err := normalizeRepositoryReviewCampaignRuns([]RepositoryReviewCampaignRunRecovery{run})
	return err
}

func repositoryReviewCampaignRecoveryDigest(request ReconcileCampaignRequest) (string, error) {
	request.ExpectedReviewVersion = 0
	request.Coverage.RecoveryDigest = ""
	data, _ := json.Marshal(request)
	if len(data) > maxRepositoryReviewCampaignRecoveryBytes {
		return "", ErrInvalidPlan
	}
	return stableID("sha256:", string(data)), nil
}

func repositoryReviewCampaignRunMatchesCoverage(
	run ReviewRun,
	recovered RepositoryReviewCampaignRunRecovery,
	coverage RepositoryReviewCampaignCoverage,
) bool {
	scopeDigest, err := repositoryReviewCampaignScopeDigestForPlan(recovered.Plan)
	if err != nil {
		return false
	}
	baseMatches := run.ID == recovered.ID && run.PlanID == recovered.Plan.ID &&
		run.CommitSHA == coverage.CommitSHA && run.InventoryHash == coverage.InventoryHash &&
		recovered.Plan.Repository != "" && recovered.Plan.CommitSHA == coverage.CommitSHA &&
		recovered.Plan.InventoryHash == coverage.InventoryHash
	if !baseMatches {
		return false
	}
	if recovered.LegacyRecovered {
		return recovered.Plan.CampaignID == "" &&
			recovered.Plan.Authoritative && recovered.Plan.RequiredAssignments == 0 &&
			validBoundedText(recovered.Plan.ProfileHash, 256) &&
			validRepositoryReviewCampaignScopeDigest(scopeDigest)
	}
	return recovered.Plan.ProfileHash == coverage.ProfileHash && scopeDigest == coverage.ScopeDigest &&
		(recovered.Plan.CampaignID == "" || recovered.Plan.CampaignID == coverage.ID)
}

type repositoryReviewCampaignIndexes struct {
	runs     map[string]int
	contexts map[string]int
	findings map[string]int
}

func newRepositoryReviewCampaignIndexes(
	state RepositoryState,
) (repositoryReviewCampaignIndexes, error) {
	indexes := repositoryReviewCampaignIndexes{
		runs:     make(map[string]int, len(state.Runs)),
		contexts: make(map[string]int, len(state.Contexts)),
		findings: make(map[string]int, len(state.Findings)),
	}
	for index, run := range state.Runs {
		if run.ID == "" {
			continue
		}
		if _, duplicate := indexes.runs[run.ID]; duplicate {
			return repositoryReviewCampaignIndexes{}, ErrConflict
		}
		indexes.runs[run.ID] = index
	}
	for index, contextRecord := range state.Contexts {
		if contextRecord.ID == "" {
			continue
		}
		if _, duplicate := indexes.contexts[contextRecord.ID]; duplicate {
			return repositoryReviewCampaignIndexes{}, ErrConflict
		}
		indexes.contexts[contextRecord.ID] = index
	}
	for index, finding := range state.Findings {
		if finding.ID == "" {
			continue
		}
		if _, duplicate := indexes.findings[finding.ID]; duplicate {
			return repositoryReviewCampaignIndexes{}, ErrConflict
		}
		indexes.findings[finding.ID] = index
	}
	return indexes, nil
}

func repositoryReviewCampaignContextMatchesCoverage(
	state RepositoryState,
	contextRecord FindingContext,
	coverage RepositoryReviewCampaignCoverage,
	selectedScope map[string]FileRef,
	indexes repositoryReviewCampaignIndexes,
	recoveredRuns map[string]RepositoryReviewCampaignRunRecovery,
) bool {
	if contextRecord.Repository != state.Repository || contextRecord.CommitSHA != coverage.CommitSHA ||
		contextRecord.InventoryHash != coverage.InventoryHash {
		return false
	}
	if len(contextRecord.Files) == 0 {
		return false
	}
	for _, file := range contextRecord.Files {
		if selected, exists := selectedScope[file.Path]; !exists || selected != file {
			return false
		}
	}
	runIndex, matchedRun := indexes.runs[contextRecord.RunID]
	if !matchedRun {
		return false
	}
	run := state.Runs[runIndex]
	if run.CommitSHA != coverage.CommitSHA || run.InventoryHash != coverage.InventoryHash ||
		(run.CampaignID != "" && run.CampaignID != coverage.ID) {
		return false
	}
	recovered, recoveredExists := recoveredRuns[run.ID]
	expectedProfileHash := coverage.ProfileHash
	if recoveredExists && recovered.LegacyRecovered {
		expectedProfileHash = recovered.Plan.ProfileHash
	}
	if contextRecord.ProfileHash != expectedProfileHash {
		return false
	}
	if run.CampaignID == "" {
		recovered, exists := recoveredRuns[run.ID]
		if !exists || !repositoryReviewCampaignRunMatchesCoverage(run, recovered, coverage) {
			return false
		}
	}
	return contextRecord.RunID != ""
}

func repositoryReviewCampaignFindingMatchesCoverage(
	state RepositoryState,
	finding Finding,
	coverage RepositoryReviewCampaignCoverage,
	selectedScope map[string]FileRef,
	indexes repositoryReviewCampaignIndexes,
	recoveredRuns map[string]RepositoryReviewCampaignRunRecovery,
) bool {
	if finding.Repository != state.Repository || finding.CommitSHA != coverage.CommitSHA ||
		len(finding.ContextIDs) == 0 ||
		!coverage.Paths[finding.File.Path].Inspected || selectedScope[finding.File.Path] != finding.File {
		return false
	}
	for _, contextID := range finding.ContextIDs {
		index, found := indexes.contexts[contextID]
		if !found || !repositoryReviewCampaignContextMatchesCoverage(
			state, state.Contexts[index], coverage, selectedScope, indexes, recoveredRuns,
		) || (state.Contexts[index].CampaignID != "" &&
			state.Contexts[index].CampaignID != coverage.ID) {
			return false
		}
		containsPrimary := false
		for _, file := range state.Contexts[index].Files {
			if file == finding.File {
				containsPrimary = true
				break
			}
		}
		if !containsPrimary {
			return false
		}
	}
	return true
}

func validateRepositoryReviewCampaignRecordBindings(state RepositoryState) error {
	runCampaigns := make(map[string]string, len(state.Runs))
	for _, run := range state.Runs {
		if run.ID == "" {
			continue
		}
		if _, duplicate := runCampaigns[run.ID]; duplicate {
			return errors.New("duplicate repository review run identity")
		}
		runCampaigns[run.ID] = run.CampaignID
	}
	contextCampaigns := make(map[string]string, len(state.Contexts))
	for _, contextRecord := range state.Contexts {
		if runCampaign := runCampaigns[contextRecord.RunID]; runCampaign != "" &&
			contextRecord.CampaignID != "" && runCampaign != contextRecord.CampaignID {
			return errors.New("repository review context campaign does not match its run")
		}
		if contextRecord.ID == "" {
			continue
		}
		if _, duplicate := contextCampaigns[contextRecord.ID]; duplicate {
			return errors.New("duplicate repository review context identity")
		}
		contextCampaigns[contextRecord.ID] = contextRecord.CampaignID
	}
	findingCampaigns := make(map[string]string, len(state.Findings))
	for _, finding := range state.Findings {
		if finding.CampaignID != "" && len(finding.ContextIDs) == 0 {
			return errors.New("repository review campaign finding has no context")
		}
		for _, contextID := range finding.ContextIDs {
			contextCampaign, exists := contextCampaigns[contextID]
			if finding.CampaignID != "" &&
				(!exists || contextCampaign != finding.CampaignID) {
				return errors.New("repository review campaign finding has an invalid context")
			}
		}
		if finding.ID != "" {
			if _, duplicate := findingCampaigns[finding.ID]; duplicate {
				return errors.New("duplicate repository review finding identity")
			}
			findingCampaigns[finding.ID] = finding.CampaignID
		}
	}
	for _, run := range state.Runs {
		if run.CampaignID == "" {
			continue
		}
		for _, findingID := range run.FindingIDs {
			findingCampaign, exists := findingCampaigns[findingID]
			if !exists || findingCampaign != run.CampaignID {
				return errors.New("repository review run campaign does not match its finding")
			}
		}
	}
	return nil
}

// CurrentCampaignFindings selects immutable review occurrences belonging to an
// automation campaign. Run FindingIDs are authoritative, while context
// membership preserves findings recorded by legacy checkpoints that omitted
// the run-level ID projection.
func CurrentCampaignFindings(
	state RepositoryState,
	runIDs []string,
	startedAt time.Time,
) []Finding {
	wantedRuns := make(map[string]struct{}, len(runIDs))
	for _, runID := range runIDs {
		if runID != "" {
			wantedRuns[runID] = struct{}{}
		}
	}
	selected := make(map[string]struct{})
	for _, run := range state.Runs {
		if _, ok := wantedRuns[run.ID]; !ok ||
			!startedAt.IsZero() && run.CompletedAt.Before(startedAt) {
			continue
		}
		for _, findingID := range run.FindingIDs {
			selected[findingID] = struct{}{}
		}
	}
	currentContexts := make(map[string]struct{})
	for _, contextRecord := range state.Contexts {
		if _, ok := wantedRuns[contextRecord.RunID]; !ok ||
			!startedAt.IsZero() && contextRecord.CreatedAt.Before(startedAt) {
			continue
		}
		currentContexts[contextRecord.ID] = struct{}{}
	}
	for _, finding := range state.Findings {
		for _, contextID := range finding.ContextIDs {
			if _, ok := currentContexts[contextID]; ok {
				selected[finding.ID] = struct{}{}
				break
			}
		}
	}
	out := make([]Finding, 0, len(selected))
	for _, finding := range state.Findings {
		if _, ok := selected[finding.ID]; ok {
			out = append(out, finding)
		}
	}
	return out
}
