package repoaudit

import (
	"context"
	"crypto/rand"
	"errors"
	"reflect"
	"strings"
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
	Repository            string                                 `json:"repository"`
	CampaignID            string                                 `json:"campaign_id"`
	ExpectedCampaignID    string                                 `json:"expected_campaign_id,omitempty"`
	CommitSHA             string                                 `json:"commit_sha"`
	ExpectedReviewVersion int64                                  `json:"expected_review_version"`
	Exact                 bool                                   `json:"exact"`
	DeduplicationSnapshot *RepositoryReviewDeduplicationSnapshot `json:"deduplication_snapshot,omitempty"`
}

// BeginCampaign installs controller-owned campaign and deduplication authority.
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
	request.DeduplicationSnapshot = cloneRepositoryReviewDeduplicationSnapshot(
		request.DeduplicationSnapshot,
	)
	if !validBoundedText(request.Repository, maxRepositoryIdentityBytes) ||
		!ValidRepositoryReviewCampaignID(request.CampaignID) ||
		(request.ExpectedCampaignID != "" &&
			!ValidRepositoryReviewCampaignID(request.ExpectedCampaignID)) ||
		!validRepositoryReviewCommitSHA(request.CommitSHA) ||
		request.ExpectedReviewVersion < 0 || request.DeduplicationSnapshot == nil ||
		validateRepositoryReviewDeduplicationSnapshot(*request.DeduplicationSnapshot) != nil {
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
		if !reflect.DeepEqual(current.DeduplicationSnapshot, request.DeduplicationSnapshot) {
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
		DeduplicationSnapshot: request.DeduplicationSnapshot,
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

// RepositoryReviewCampaignMetrics is an exact current-campaign projection when
// CoverageAvailable and CoverageExact are both true. Otherwise path counts are
// durable lower bounds. Finding counts are selected by the canonical campaign.
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

// CurrentCampaignFindings selects canonical findings by durable campaign.
func CurrentCampaignFindings(
	state RepositoryState,
	campaignID string,
) []Finding {
	campaignID = strings.TrimSpace(campaignID)
	if campaignID == "" {
		return []Finding{}
	}
	out := make([]Finding, 0)
	for _, finding := range state.Findings {
		if finding.CampaignID == campaignID {
			out = append(out, finding)
		}
	}
	return out
}

// CurrentCampaignRawFindings returns persisted raw evidence for one campaign.
func CurrentCampaignRawFindings(
	state RepositoryState,
	campaignID string,
) []RawReviewFinding {
	campaignID = strings.TrimSpace(campaignID)
	result := make([]RawReviewFinding, 0, len(state.RawFindings))
	for _, raw := range state.RawFindings {
		if campaignID != "" && raw.CampaignID == campaignID {
			result = append(result, raw)
		}
	}
	return result
}

// CurrentCampaignMetrics derives unique path and finding counts from durable
// campaign authority. Repository-level aggregate mappings remain live, so the
// distinct aggregate count may decrease when provisional duplicates merge.
func CurrentCampaignMetrics(
	state RepositoryState,
	campaignID string,
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
	for _, finding := range CurrentCampaignFindings(state, campaignID) {
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

// DeduplicatedFindingBelongsToCampaign checks the finding's durable campaign.
func DeduplicatedFindingBelongsToCampaign(
	_ RepositoryState,
	finding Finding,
	campaignID string,
) bool {
	return finding.CampaignID == strings.TrimSpace(campaignID)
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

func bindRepositoryReviewCampaignAssignmentCatalog(
	state *RepositoryState,
	campaignID string,
	commitSHA string,
	inventoryHash string,
	profileHash string,
	scopeDigest string,
	catalog []RepositoryReviewAssignment,
	selectedFiles int,
) (bool, error) {
	normalized, err := NormalizeRepositoryReviewAssignmentCatalog(catalog)
	if err != nil || normalized[0].ProfileHash != profileHash {
		return false, ErrInvalidPlan
	}
	requiredAssignments := repositoryReviewRequiredAssignmentCount(normalized)
	changed, err := bindRepositoryReviewCampaignScope(
		state,
		campaignID,
		commitSHA,
		inventoryHash,
		profileHash,
		scopeDigest,
		requiredAssignments,
		selectedFiles,
	)
	if err != nil {
		return false, err
	}
	coverage := state.CurrentCampaign
	if len(coverage.AssignmentCatalog) > 0 {
		if !repositoryReviewAssignmentCatalogEqual(coverage.AssignmentCatalog, normalized) {
			return false, ErrConflict
		}
		return changed, nil
	}
	coverage.AssignmentCatalog = normalized
	return true, nil
}

func mergeRepositoryReviewCampaignPath(
	coverage *RepositoryReviewCampaignCoverage,
	pathValue string,
	update RepositoryReviewCampaignPathCoverage,
) (bool, error) {
	if coverage == nil || !repositoryReviewCampaignScopeBound(coverage) ||
		len(coverage.AssignmentCatalog) == 0 ||
		!validRepositoryReviewPath(pathValue) ||
		(!update.Inspected && !update.Completed && !update.Unsupported && update.AssignmentBits == "") ||
		(update.Unsupported && (update.Inspected || update.Completed)) {
		return false, ErrInvalidPlan
	}
	if coverage.Paths == nil {
		coverage.Paths = make(map[string]RepositoryReviewCampaignPathCoverage)
	}
	current := coverage.Paths[pathValue]
	if update.Unsupported {
		if current.AssignmentBits != "" {
			return false, ErrConflict
		}
		next := RepositoryReviewCampaignPathCoverage{Unsupported: true}
		if current == next {
			return false, nil
		}
		coverage.Paths[pathValue] = next
		return true, nil
	}
	currentBits, err := decodeRepositoryReviewAssignmentBits(
		current.AssignmentBits, coverage.AssignmentCatalog,
	)
	if err != nil {
		return false, err
	}
	updateBits, err := decodeRepositoryReviewAssignmentBits(
		update.AssignmentBits, coverage.AssignmentCatalog,
	)
	if err != nil {
		return false, err
	}
	if update.Completed && update.AssignmentBits == "" {
		for index, assignment := range coverage.AssignmentCatalog {
			if assignment.Required {
				setRepositoryReviewAssignmentBit(updateBits, index)
			}
		}
	}
	changed := false
	for index := range currentBits {
		next := currentBits[index] | updateBits[index]
		changed = changed || next != currentBits[index]
		currentBits[index] = next
	}
	current.AssignmentBits = encodeRepositoryReviewAssignmentBits(currentBits)
	projected, err := projectRepositoryReviewAssignmentCoverage(
		current, coverage.AssignmentCatalog,
	)
	if err != nil {
		return false, err
	}
	if !changed && projected == coverage.Paths[pathValue] {
		return false, nil
	}
	coverage.Paths[pathValue] = projected
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
		coverage.DeduplicationSnapshot == nil ||
		validateRepositoryReviewDeduplicationSnapshot(*coverage.DeduplicationSnapshot) != nil ||
		coverage.Paths == nil || coverage.SelectedFiles < 0 ||
		coverage.SelectedFiles > maxReviewFiles || len(coverage.Paths) > maxReviewFiles {
		return errors.New("invalid repository review campaign coverage")
	}
	bound := repositoryReviewCampaignScopeBound(coverage)
	assignmentCatalog := coverage.AssignmentCatalog
	if len(assignmentCatalog) > 0 {
		normalized, err := NormalizeRepositoryReviewAssignmentCatalog(assignmentCatalog)
		if err != nil || !repositoryReviewAssignmentCatalogEqual(normalized, assignmentCatalog) ||
			assignmentCatalog[0].ProfileHash != coverage.ProfileHash ||
			repositoryReviewRequiredAssignmentCount(assignmentCatalog) != coverage.RequiredAssignments {
			return errors.New("invalid repository review assignment catalog")
		}
	}
	if (coverage.InventoryHash == "") != (coverage.ProfileHash == "") ||
		(bound && (!validBoundedText(coverage.InventoryHash, 256) ||
			!validBoundedText(coverage.ProfileHash, 256) ||
			!validRepositoryReviewCampaignScopeDigest(coverage.ScopeDigest) ||
			coverage.RequiredAssignments < 1 ||
			coverage.RequiredAssignments > maxRepositoryReviewRequiredAssignments ||
			coverage.InventoryHash != strings.TrimSpace(coverage.InventoryHash) ||
			coverage.ProfileHash != strings.TrimSpace(coverage.ProfileHash))) ||
		(!bound && (coverage.ScopeDigest != "" || coverage.RequiredAssignments != 0 ||
			coverage.SelectedFiles != 0 || len(coverage.Paths) != 0 || len(assignmentCatalog) != 0)) {
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
		if len(assignmentCatalog) > 0 {
			projected, err := projectRepositoryReviewAssignmentCoverage(pathCoverage, assignmentCatalog)
			if err != nil || projected != pathCoverage {
				return errors.New("invalid repository review campaign assignment projection")
			}
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

func validRepositoryReviewCampaignScopeDigest(value string) bool {
	digest, ok := strings.CutPrefix(value, "sha256:")
	return ok && len(digest) == 64 && validHexDigest(digest)
}

func cloneRepositoryReviewCampaignCoverage(
	coverage RepositoryReviewCampaignCoverage,
) RepositoryReviewCampaignCoverage {
	coverage.AssignmentCatalog = append(
		[]RepositoryReviewAssignment(nil), coverage.AssignmentCatalog...,
	)
	coverage.DeduplicationSnapshot = cloneRepositoryReviewDeduplicationSnapshot(
		coverage.DeduplicationSnapshot,
	)
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

func validateRepositoryReviewCampaignRecordBindings(state RepositoryState) error {
	runCampaigns := make(map[string]string, len(state.Runs))
	for _, run := range state.Runs {
		if run.ID == "" || !ValidRepositoryReviewCampaignID(run.CampaignID) {
			return errors.New("invalid repository review run identity")
		}
		if _, duplicate := runCampaigns[run.ID]; duplicate {
			return errors.New("duplicate repository review run identity")
		}
		runCampaigns[run.ID] = run.CampaignID
	}
	contextCampaigns := make(map[string]string, len(state.Contexts))
	for _, contextRecord := range state.Contexts {
		if !ValidRepositoryReviewCampaignID(contextRecord.CampaignID) {
			return errors.New("invalid repository review context campaign")
		}
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
	findingIDs := make(map[string]struct{}, len(state.Findings))
	for _, finding := range state.Findings {
		if len(finding.ContextIDs) == 0 {
			return errors.New("repository review campaign finding has no context")
		}
		for _, contextID := range finding.ContextIDs {
			contextCampaign, exists := contextCampaigns[contextID]
			if !exists || contextCampaign != finding.CampaignID {
				return errors.New("repository review campaign finding has an invalid context")
			}
		}
		if finding.ID != "" {
			if _, duplicate := findingIDs[finding.ID]; duplicate {
				return errors.New("duplicate repository review finding identity")
			}
			findingIDs[finding.ID] = struct{}{}
		}
	}
	rawFindingCampaigns := make(map[string]string, len(state.RawFindings))
	for _, finding := range state.RawFindings {
		if finding.ID == "" {
			continue
		}
		if _, duplicate := findingIDs[finding.ID]; duplicate {
			return errors.New("duplicate repository review raw finding identity")
		}
		if _, duplicate := rawFindingCampaigns[finding.ID]; duplicate {
			return errors.New("duplicate repository review raw finding identity")
		}
		rawFindingCampaigns[finding.ID] = finding.CampaignID
	}
	for _, run := range state.Runs {
		for _, findingID := range run.FindingIDs {
			findingCampaign, exists := rawFindingCampaigns[findingID]
			if !exists || findingCampaign != run.CampaignID {
				return errors.New("repository review run campaign does not match its finding")
			}
		}
	}
	return nil
}
