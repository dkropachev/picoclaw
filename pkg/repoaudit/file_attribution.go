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

	"github.com/sipeed/picoclaw/pkg/routing"
)

const (
	maxRepositoryReviewFileAttributions      = 100_000
	maxRepositoryReviewAttributedFileCredits = maxReviewFiles * maxRepositoryReviewRequiredAssignments
	maxRepositoryReviewAttributionChildIndex = 1_000_000
)

// RepositoryReviewFileAttributionSource identifies the trusted evidence path
// that produced an attribution record.
type RepositoryReviewFileAttributionSource string

const (
	RepositoryReviewFileAttributionSourceLiveCheckpoint     RepositoryReviewFileAttributionSource = "live_checkpoint"
	RepositoryReviewFileAttributionSourceLegacyManagedChild RepositoryReviewFileAttributionSource = "legacy_managed_child"
)

// MergeRepositoryReviewFileAttributionsRequest appends immutable attribution
// evidence at a repository-state compare-and-swap boundary.
type MergeRepositoryReviewFileAttributionsRequest struct {
	Repository      string                                      `json:"repository"`
	ExpectedVersion int64                                       `json:"expected_version"`
	Attributions    []RepositoryReviewFileAttribution           `json:"attributions"`
	CampaignCredit  *RepositoryReviewFileAttributionCreditFence `json:"campaign_credit,omitempty"`
}

// RepositoryReviewFileAttributionCreditFence authorizes one exact recovered
// campaign to reuse immutable legacy file acknowledgements as current catalog
// assignment credits. ExpectedReviewVersion fences the campaign authority
// independently from the repository version used by the attribution merge.
type RepositoryReviewFileAttributionCreditFence struct {
	AutomationID          string `json:"automation_id"`
	CampaignID            string `json:"campaign_id"`
	ExpectedReviewVersion int64  `json:"expected_review_version"`
}

// RepositoryReviewFileAttributionAssignmentCredit is one deterministic
// attribution-to-current-catalog mapping. The exact FileRef and current
// assignment identity make the dry-run plan suitable for digest binding.
type RepositoryReviewFileAttributionAssignmentCredit struct {
	AssignmentID     string  `json:"assignment_id"`
	FocusID          string  `json:"focus_id"`
	ReviewerIdentity string  `json:"reviewer_identity"`
	File             FileRef `json:"file"`
}

// RepositoryReviewFileAttributionCreditPreview reports the complete eligible
// semantic credit plan and the subset that would change current campaign
// coverage. Effective counts describe attribution-backed credits, not all
// native campaign bits that may already exist independently.
type RepositoryReviewFileAttributionCreditPreview struct {
	CampaignID                    string                                            `json:"campaign_id"`
	Credits                       []RepositoryReviewFileAttributionAssignmentCredit `json:"credits"`
	EffectiveAssignmentCredits    int                                               `json:"effective_assignment_credits"`
	NewAssignmentCredits          int                                               `json:"new_assignment_credits"`
	EffectiveInspectedFiles       int                                               `json:"effective_inspected_files"`
	NewInspectedFiles             int                                               `json:"new_inspected_files"`
	ProjectedCompletedAssignments int                                               `json:"projected_completed_assignments"`
	ProjectedPendingAssignments   int                                               `json:"projected_pending_assignments"`
	ProjectedInspectedFiles       int                                               `json:"projected_inspected_files"`
	ProjectedCompletedFiles       int                                               `json:"projected_completed_files"`
}

// NewRepositoryReviewFileAttribution normalizes and validates one immutable
// attribution record and derives its stable logical-child identity. Supplying
// an ID is optional, but a supplied ID must match the derived identity.
func NewRepositoryReviewFileAttribution(
	input RepositoryReviewFileAttribution,
) (RepositoryReviewFileAttribution, error) {
	providedID := strings.TrimSpace(input.ID)
	input.ID = ""
	input.AutomationID = strings.TrimSpace(input.AutomationID)
	input.RunID = strings.TrimSpace(input.RunID)
	input.CommitSHA = strings.ToLower(strings.TrimSpace(input.CommitSHA))
	input.InventoryHash = strings.TrimSpace(input.InventoryHash)
	input.ProfileHash = strings.TrimSpace(input.ProfileHash)
	input.AssignmentID = strings.TrimSpace(input.AssignmentID)
	input.FocusID = strings.TrimSpace(input.FocusID)
	input.RootAgentID = strings.TrimSpace(input.RootAgentID)
	input.ReviewerIdentity = strings.TrimSpace(input.ReviewerIdentity)
	input.Model = strings.TrimSpace(input.Model)
	input.ModelAlias = strings.TrimSpace(input.ModelAlias)
	input.Account = strings.TrimSpace(input.Account)
	input.UsageModel = strings.TrimSpace(input.UsageModel)
	input.EvidenceDigest = strings.TrimSpace(input.EvidenceDigest)
	input.Source = RepositoryReviewFileAttributionSource(strings.TrimSpace(string(input.Source)))
	input.CompletedAt = input.CompletedAt.UTC()

	files, err := normalizeFiles(input.AcknowledgedFiles)
	if err != nil || len(files) == 0 || len(files) > maxReviewFiles {
		return RepositoryReviewFileAttribution{}, ErrInvalidPlan
	}
	input.AcknowledgedFiles = files
	if !validAutomationID(input.AutomationID) ||
		!validBoundedText(input.RunID, 1024) ||
		!validRepositoryReviewCommitSHA(input.CommitSHA) ||
		!validBoundedText(input.InventoryHash, 256) ||
		!validBoundedText(input.ProfileHash, 256) ||
		!validBoundedText(input.AssignmentID, 128) ||
		!validRepositoryReviewFocusID(input.FocusID) ||
		!routing.IsCanonicalAgentID(input.RootAgentID) ||
		!validBoundedText(input.ReviewerIdentity, 256) ||
		!validFindingSourceProvenance(input.Model, input.ModelAlias, input.Account) ||
		input.Source == RepositoryReviewFileAttributionSourceLiveCheckpoint &&
			(input.ModelAlias == "" || input.Account == "") ||
		!validOptionalAutomationText(input.UsageModel, 256) ||
		!validRepositoryReviewCheckpointDigest(input.EvidenceDigest) ||
		!validRepositoryReviewFileAttributionSource(input.Source) ||
		input.ChildIndex < 1 || input.ChildIndex > maxRepositoryReviewAttributionChildIndex ||
		input.CompletedAt.IsZero() {
		return RepositoryReviewFileAttribution{}, ErrInvalidPlan
	}
	input.ID = repositoryReviewFileAttributionID(input)
	if providedID != "" && providedID != input.ID {
		return RepositoryReviewFileAttribution{}, ErrInvalidPlan
	}
	return cloneRepositoryReviewFileAttribution(input), nil
}

// PreviewRepositoryReviewFileAttributionCredits derives the exact legacy
// attribution credits eligible for one recovered campaign without mutating
// state. Supplied records are evaluated together with retained records, so the
// same function supports both a dry run and an idempotent post-merge replay.
func PreviewRepositoryReviewFileAttributionCredits(
	state RepositoryState,
	fence RepositoryReviewFileAttributionCreditFence,
	supplied []RepositoryReviewFileAttribution,
) (RepositoryReviewFileAttributionCreditPreview, error) {
	fence, err := normalizeRepositoryReviewFileAttributionCreditFence(fence)
	if err != nil {
		return RepositoryReviewFileAttributionCreditPreview{}, err
	}
	preview := RepositoryReviewFileAttributionCreditPreview{CampaignID: fence.CampaignID}
	coverage := state.CurrentCampaign
	if coverage == nil || coverage.ID != fence.CampaignID || !coverage.Exact ||
		coverage.RecoveryDigest == "" || len(coverage.AssignmentCatalog) == 0 {
		return preview, fmt.Errorf("%w: campaign credit authority changed", ErrConflict)
	}
	catalog, err := NormalizeRepositoryReviewAssignmentCatalog(coverage.AssignmentCatalog)
	if err != nil || !reflect.DeepEqual(catalog, coverage.AssignmentCatalog) {
		return preview, ErrInvalidPlan
	}

	attributions, err := repositoryReviewFileAttributionCreditCandidates(
		state.FileAttributions, supplied,
	)
	if err != nil {
		return preview, err
	}
	runs := make(map[string]ReviewRun, len(state.Runs))
	for _, run := range state.Runs {
		if run.ID == "" {
			continue
		}
		if _, duplicate := runs[run.ID]; duplicate {
			return preview, fmt.Errorf("%w: duplicate campaign credit run", ErrConflict)
		}
		runs[run.ID] = run
	}

	type assignmentKey struct {
		focus    string
		reviewer string
	}
	assignments := make(map[assignmentKey]RepositoryReviewAssignment)
	for _, assignment := range catalog {
		if !assignment.Required {
			continue
		}
		key := assignmentKey{focus: assignment.FocusID, reviewer: assignment.Reviewer}
		if _, duplicate := assignments[key]; duplicate {
			return preview, fmt.Errorf("%w: ambiguous campaign credit assignment", ErrConflict)
		}
		assignments[key] = assignment
	}

	type creditKey struct {
		path         string
		assignmentID string
	}
	credits := make(map[creditKey]RepositoryReviewFileAttributionAssignmentCredit)
	filesByPath := make(map[string]FileRef)
	for _, attribution := range attributions {
		if attribution.AutomationID != fence.AutomationID ||
			attribution.Source != RepositoryReviewFileAttributionSourceLegacyManagedChild ||
			!attribution.Required || attribution.RootAgentID != "main" {
			continue
		}
		run, found := runs[attribution.RunID]
		if !found || run.CampaignID != fence.CampaignID {
			continue
		}
		if !run.LegacyRecovered || run.CommitSHA != coverage.CommitSHA ||
			run.InventoryHash != coverage.InventoryHash ||
			attribution.CommitSHA != run.CommitSHA ||
			attribution.InventoryHash != run.InventoryHash ||
			attribution.ProfileHash != run.ProfileHash ||
			!attribution.CompletedAt.Equal(run.CompletedAt) {
			return preview, fmt.Errorf("%w: legacy attribution run evidence changed", ErrConflict)
		}
		assignment, matched := assignments[assignmentKey{
			focus: attribution.FocusID, reviewer: attribution.ReviewerIdentity,
		}]
		if !matched {
			continue
		}
		for _, file := range attribution.AcknowledgedFiles {
			if retained, exists := filesByPath[file.Path]; exists && retained != file {
				return preview, fmt.Errorf("%w: conflicting attributed file revision", ErrConflict)
			}
			filesByPath[file.Path] = file
			if coverage.Paths[file.Path].Unsupported {
				return preview, fmt.Errorf("%w: attributed path is unsupported", ErrConflict)
			}
			key := creditKey{path: file.Path, assignmentID: assignment.ID}
			credit := RepositoryReviewFileAttributionAssignmentCredit{
				AssignmentID: assignment.ID, FocusID: assignment.FocusID,
				ReviewerIdentity: assignment.Reviewer, File: file,
			}
			if retained, duplicate := credits[key]; duplicate {
				if retained != credit {
					return preview, ErrConflict
				}
				continue
			}
			credits[key] = credit
			if len(credits) > maxRepositoryReviewAttributedFileCredits {
				return preview, ErrInvalidPlan
			}
		}
	}

	preview.Credits = make([]RepositoryReviewFileAttributionAssignmentCredit, 0, len(credits))
	for _, credit := range credits {
		preview.Credits = append(preview.Credits, credit)
	}
	sort.Slice(preview.Credits, func(i, j int) bool {
		if preview.Credits[i].File.Path != preview.Credits[j].File.Path {
			return preview.Credits[i].File.Path < preview.Credits[j].File.Path
		}
		return preview.Credits[i].AssignmentID < preview.Credits[j].AssignmentID
	})
	preview.EffectiveAssignmentCredits = len(preview.Credits)
	inspected := make(map[string]struct{}, len(filesByPath))
	newInspected := make(map[string]struct{}, len(filesByPath))
	for _, credit := range preview.Credits {
		inspected[credit.File.Path] = struct{}{}
		pathCoverage := coverage.Paths[credit.File.Path]
		complete, completeErr := repositoryReviewAssignmentComplete(
			pathCoverage, catalog, credit.AssignmentID,
		)
		if completeErr != nil {
			return preview, completeErr
		}
		if !complete {
			preview.NewAssignmentCredits++
		}
		if !pathCoverage.Inspected {
			newInspected[credit.File.Path] = struct{}{}
		}
	}
	preview.EffectiveInspectedFiles = len(inspected)
	preview.NewInspectedFiles = len(newInspected)
	projected := state
	projectedCoverage := cloneRepositoryReviewCampaignCoverage(*coverage)
	projected.CurrentCampaign = &projectedCoverage
	if _, err := applyRepositoryReviewFileAttributionCreditPreview(&projected, preview); err != nil {
		return RepositoryReviewFileAttributionCreditPreview{}, err
	}
	progress := CurrentCampaignAssignmentProgress(projected, fence.CampaignID)
	metrics := CurrentCampaignMetrics(projected, fence.CampaignID, nil, time.Time{})
	preview.ProjectedCompletedAssignments = progress.Completed
	preview.ProjectedPendingAssignments = progress.Pending
	preview.ProjectedInspectedFiles = metrics.InspectedFiles
	preview.ProjectedCompletedFiles = metrics.CompletedFiles
	return preview, nil
}

func applyRepositoryReviewFileAttributionCreditPreview(
	state *RepositoryState,
	preview RepositoryReviewFileAttributionCreditPreview,
) (bool, error) {
	if state == nil || state.CurrentCampaign == nil ||
		state.CurrentCampaign.ID != preview.CampaignID {
		return false, fmt.Errorf("%w: campaign credit authority changed", ErrConflict)
	}
	changed := false
	for _, credit := range preview.Credits {
		current := state.CurrentCampaign.Paths[credit.File.Path]
		next, err := CreditRepositoryReviewAssignment(
			current, state.CurrentCampaign.AssignmentCatalog, credit.AssignmentID,
		)
		if err != nil {
			return false, err
		}
		if next == current {
			continue
		}
		state.CurrentCampaign.Paths[credit.File.Path] = next
		changed = true
	}
	return changed, nil
}

func normalizeRepositoryReviewFileAttributionCreditFence(
	fence RepositoryReviewFileAttributionCreditFence,
) (RepositoryReviewFileAttributionCreditFence, error) {
	fence.AutomationID = strings.TrimSpace(fence.AutomationID)
	fence.CampaignID = strings.TrimSpace(fence.CampaignID)
	if !validAutomationID(fence.AutomationID) ||
		!ValidRepositoryReviewCampaignID(fence.CampaignID) ||
		fence.ExpectedReviewVersion < 0 {
		return RepositoryReviewFileAttributionCreditFence{}, ErrInvalidPlan
	}
	return fence, nil
}

func repositoryReviewFileAttributionCreditCandidates(
	retained []RepositoryReviewFileAttribution,
	supplied []RepositoryReviewFileAttribution,
) ([]RepositoryReviewFileAttribution, error) {
	if len(retained)+len(supplied) > maxRepositoryReviewFileAttributions*2 {
		return nil, ErrInvalidPlan
	}
	byID := make(map[string]RepositoryReviewFileAttribution, len(retained)+len(supplied))
	out := make([]RepositoryReviewFileAttribution, 0, len(retained)+len(supplied))
	appendCandidate := func(input RepositoryReviewFileAttribution) error {
		attribution, err := NewRepositoryReviewFileAttribution(input)
		if err != nil {
			return err
		}
		if existing, duplicate := byID[attribution.ID]; duplicate {
			if !reflect.DeepEqual(existing, attribution) {
				return ErrConflict
			}
			return nil
		}
		byID[attribution.ID] = attribution
		out = append(out, attribution)
		return nil
	}
	for _, attribution := range retained {
		if err := appendCandidate(attribution); err != nil {
			return nil, err
		}
	}
	for _, attribution := range supplied {
		if err := appendCandidate(attribution); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// MergeRepositoryReviewFileAttributions appends new immutable evidence. An
// exact replay succeeds even after the repository version advances; a logical
// child replay with different evidence conflicts.
func (s Store) MergeRepositoryReviewFileAttributions(
	ctx context.Context,
	request MergeRepositoryReviewFileAttributionsRequest,
) (RepositoryState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return RepositoryState{}, contextErr
	}
	request.Repository = strings.TrimSpace(request.Repository)
	var creditFence *RepositoryReviewFileAttributionCreditFence
	if request.CampaignCredit != nil {
		normalizedFence, err := normalizeRepositoryReviewFileAttributionCreditFence(
			*request.CampaignCredit,
		)
		if err != nil {
			return RepositoryState{}, err
		}
		creditFence = &normalizedFence
	}
	if !validBoundedText(request.Repository, maxRepositoryIdentityBytes) ||
		request.ExpectedVersion < 0 || len(request.Attributions) == 0 && creditFence == nil ||
		len(request.Attributions) > maxRepositoryReviewFileAttributions {
		return RepositoryState{}, ErrInvalidPlan
	}
	normalized := make([]RepositoryReviewFileAttribution, 0, len(request.Attributions))
	byID := make(map[string]RepositoryReviewFileAttribution, len(request.Attributions))
	for _, input := range request.Attributions {
		attribution, err := NewRepositoryReviewFileAttribution(input)
		if err != nil {
			return RepositoryState{}, err
		}
		if existing, duplicate := byID[attribution.ID]; duplicate {
			if !reflect.DeepEqual(existing, attribution) {
				return RepositoryState{}, ErrConflict
			}
			continue
		}
		byID[attribution.ID] = attribution
		normalized = append(normalized, attribution)
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
	existing := make(map[string]RepositoryReviewFileAttribution, len(state.FileAttributions))
	for _, attribution := range state.FileAttributions {
		existing[attribution.ID] = attribution
	}
	additions := make([]RepositoryReviewFileAttribution, 0, len(normalized))
	for _, attribution := range normalized {
		if retained, found := existing[attribution.ID]; found {
			if !reflect.DeepEqual(retained, attribution) {
				return RepositoryState{}, ErrConflict
			}
			continue
		}
		additions = append(additions, attribution)
	}
	var creditPreview RepositoryReviewFileAttributionCreditPreview
	creditChanged := false
	if creditFence != nil {
		creditPreview, err = PreviewRepositoryReviewFileAttributionCredits(
			state, *creditFence, normalized,
		)
		if err != nil {
			return RepositoryState{}, err
		}
		creditChanged = creditPreview.NewAssignmentCredits > 0
	}
	if len(additions) == 0 && !creditChanged {
		return state, nil
	}
	if state.Version != request.ExpectedVersion || creditChanged &&
		state.ReviewVersion != creditFence.ExpectedReviewVersion ||
		len(state.FileAttributions)+len(additions) > maxRepositoryReviewFileAttributions {
		return RepositoryState{}, ErrConflict
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return RepositoryState{}, contextErr
	}
	for _, attribution := range additions {
		state.FileAttributions = append(
			state.FileAttributions, cloneRepositoryReviewFileAttribution(attribution),
		)
	}
	sortRepositoryReviewFileAttributions(state.FileAttributions)
	if creditChanged {
		changed, creditErr := applyRepositoryReviewFileAttributionCreditPreview(
			&state, creditPreview,
		)
		if creditErr != nil {
			return RepositoryState{}, creditErr
		}
		if !changed {
			return RepositoryState{}, ErrConflict
		}
		state.ReviewVersion++
	}
	state.Version++
	state.UpdatedAt = s.clock()
	if err := s.save(&state); err != nil {
		return RepositoryState{}, err
	}
	return state, nil
}

func repositoryReviewFileAttributionID(attribution RepositoryReviewFileAttribution) string {
	data, _ := json.Marshal(struct {
		AutomationID string `json:"automation_id"`
		RunID        string `json:"run_id"`
		ChildIndex   int    `json:"child_index"`
	}{
		AutomationID: attribution.AutomationID,
		RunID:        attribution.RunID,
		ChildIndex:   attribution.ChildIndex,
	})
	return stableID("rfa_", string(data))
}

func appendRepositoryReviewFileAttribution(
	state *RepositoryState,
	attribution RepositoryReviewFileAttribution,
) (bool, error) {
	if state == nil {
		return false, errors.New("repository review state is required")
	}
	normalized, err := NewRepositoryReviewFileAttribution(attribution)
	if err != nil {
		return false, err
	}
	for _, existing := range state.FileAttributions {
		if existing.ID != normalized.ID {
			continue
		}
		if reflect.DeepEqual(existing, normalized) {
			return false, nil
		}
		return false, ErrConflict
	}
	if len(state.FileAttributions) >= maxRepositoryReviewFileAttributions {
		return false, errors.New("repository review file attribution limit exceeded")
	}
	state.FileAttributions = append(
		state.FileAttributions, cloneRepositoryReviewFileAttribution(normalized),
	)
	sortRepositoryReviewFileAttributions(state.FileAttributions)
	return true, nil
}

func validateRepositoryReviewFileAttributions(
	attributions []RepositoryReviewFileAttribution,
) error {
	return validateRepositoryReviewFileAttributionsWithCreditLimit(
		attributions, maxRepositoryReviewAttributedFileCredits,
	)
}

func validateRepositoryReviewFileAttributionsWithCreditLimit(
	attributions []RepositoryReviewFileAttribution,
	maximumCredits int,
) error {
	if len(attributions) > maxRepositoryReviewFileAttributions {
		return errors.New("invalid repository review file attributions")
	}
	seen := make(map[string]struct{}, len(attributions))
	totalFiles := 0
	for _, attribution := range attributions {
		normalized, err := NewRepositoryReviewFileAttribution(attribution)
		if err != nil || !reflect.DeepEqual(normalized, attribution) {
			return errors.New("invalid repository review file attribution")
		}
		if _, duplicate := seen[attribution.ID]; duplicate {
			return errors.New("duplicate repository review file attribution")
		}
		seen[attribution.ID] = struct{}{}
		totalFiles += len(attribution.AcknowledgedFiles)
		if totalFiles > maximumCredits {
			return errors.New("repository review attributed file limit exceeded")
		}
	}
	return nil
}

func validRepositoryReviewFileAttributionSource(
	source RepositoryReviewFileAttributionSource,
) bool {
	return source == RepositoryReviewFileAttributionSourceLiveCheckpoint ||
		source == RepositoryReviewFileAttributionSourceLegacyManagedChild
}

func validRepositoryReviewAttributionAgentID(agentID string) bool {
	return routing.IsCanonicalAgentID(agentID)
}

func cloneRepositoryReviewFileAttribution(
	attribution RepositoryReviewFileAttribution,
) RepositoryReviewFileAttribution {
	attribution.AcknowledgedFiles = append([]FileRef(nil), attribution.AcknowledgedFiles...)
	return attribution
}

func sortRepositoryReviewFileAttributions(attributions []RepositoryReviewFileAttribution) {
	sort.SliceStable(attributions, func(left, right int) bool {
		if !attributions[left].CompletedAt.Equal(attributions[right].CompletedAt) {
			return attributions[left].CompletedAt.Before(attributions[right].CompletedAt)
		}
		return attributions[left].ID < attributions[right].ID
	})
}
