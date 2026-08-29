package repoaudit

import (
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

const (
	RepositoryReviewFocusCorrectnessState      = "correctness_state"
	RepositoryReviewFocusSecurityTrust         = "security_trust"
	RepositoryReviewFocusConcurrencyRecovery   = "concurrency_recovery"
	RepositoryReviewFocusIntegrationValidation = "integration_validation"
)

var repositoryReviewFocusIDs = []string{
	RepositoryReviewFocusCorrectnessState,
	RepositoryReviewFocusSecurityTrust,
	RepositoryReviewFocusConcurrencyRecovery,
	RepositoryReviewFocusIntegrationValidation,
}

// RepositoryReviewFocusIDs returns the immutable built-in focus order. The
// same order is used by live planning and positional legacy-run recovery.
func RepositoryReviewFocusIDs() []string {
	return append([]string(nil), repositoryReviewFocusIDs...)
}

func validRepositoryReviewFocusID(value string) bool {
	for _, focusID := range repositoryReviewFocusIDs {
		if value == focusID {
			return true
		}
	}
	return false
}

// NewRepositoryReviewAssignment derives the stable identity of one campaign
// assignment. Reviewer is a logical reviewer identity ("default" for the
// configured fallback chain), never a provider credential or account ID.
func NewRepositoryReviewAssignment(
	focusID string,
	reviewer string,
	promptRevision string,
	profileHash string,
	required bool,
) (RepositoryReviewAssignment, error) {
	focusID = strings.TrimSpace(focusID)
	reviewer = strings.TrimSpace(reviewer)
	promptRevision = strings.TrimSpace(promptRevision)
	profileHash = strings.TrimSpace(profileHash)
	if !validRepositoryReviewFocusID(focusID) ||
		!validBoundedText(reviewer, 256) ||
		!validBoundedText(promptRevision, 256) ||
		!validBoundedText(profileHash, 256) {
		return RepositoryReviewAssignment{}, ErrInvalidPlan
	}
	return RepositoryReviewAssignment{
		ID:      stableID("rra_", focusID, reviewer, promptRevision, profileHash),
		FocusID: focusID, Reviewer: reviewer, PromptRevision: promptRevision,
		ProfileHash: profileHash, Required: required,
	}, nil
}

// NormalizeRepositoryReviewAssignmentCatalog validates a stable ordered
// catalog and returns a detached copy. Catalog order is significant because it
// defines compact bit positions.
func NormalizeRepositoryReviewAssignmentCatalog(
	catalog []RepositoryReviewAssignment,
) ([]RepositoryReviewAssignment, error) {
	if len(catalog) == 0 || len(catalog) > maxRepositoryReviewRequiredAssignments {
		return nil, ErrInvalidPlan
	}
	out := append([]RepositoryReviewAssignment(nil), catalog...)
	seen := make(map[string]struct{}, len(out))
	required := 0
	profileHash := ""
	for index, assignment := range out {
		normalized, err := NewRepositoryReviewAssignment(
			assignment.FocusID,
			assignment.Reviewer,
			assignment.PromptRevision,
			assignment.ProfileHash,
			assignment.Required,
		)
		if err != nil || normalized != assignment {
			return nil, fmt.Errorf("%w: invalid assignment %d", ErrInvalidPlan, index)
		}
		if _, duplicate := seen[assignment.ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate assignment %q", ErrInvalidPlan, assignment.ID)
		}
		seen[assignment.ID] = struct{}{}
		if profileHash == "" {
			profileHash = assignment.ProfileHash
		} else if profileHash != assignment.ProfileHash {
			return nil, fmt.Errorf("%w: assignment profile hashes differ", ErrInvalidPlan)
		}
		if assignment.Required {
			required++
		}
	}
	if required == 0 {
		return nil, fmt.Errorf("%w: assignment catalog has no required work", ErrInvalidPlan)
	}
	return out, nil
}

func repositoryReviewRequiredAssignmentCount(catalog []RepositoryReviewAssignment) int {
	count := 0
	for _, assignment := range catalog {
		if assignment.Required {
			count++
		}
	}
	return count
}

func repositoryReviewAssignmentIndex(
	catalog []RepositoryReviewAssignment,
	assignmentID string,
) (int, bool) {
	for index, assignment := range catalog {
		if assignment.ID == assignmentID {
			return index, true
		}
	}
	return 0, false
}

func repositoryReviewAssignmentMaskBytes(catalog []RepositoryReviewAssignment) int {
	return (len(catalog) + 7) / 8
}

func decodeRepositoryReviewAssignmentBits(
	encoded string,
	catalog []RepositoryReviewAssignment,
) ([]byte, error) {
	bytesRequired := repositoryReviewAssignmentMaskBytes(catalog)
	bits := make([]byte, bytesRequired)
	if encoded == "" {
		return bits, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(raw) != bytesRequired ||
		base64.RawStdEncoding.EncodeToString(raw) != encoded {
		return nil, errors.New("invalid repository review assignment bitmask")
	}
	copy(bits, raw)
	if remainder := len(catalog) % 8; remainder != 0 &&
		bits[len(bits)-1]&^byte((1<<remainder)-1) != 0 {
		return nil, errors.New("repository review assignment bitmask has unknown bits")
	}
	return bits, nil
}

func encodeRepositoryReviewAssignmentBits(bits []byte) string {
	for _, value := range bits {
		if value != 0 {
			return base64.RawStdEncoding.EncodeToString(bits)
		}
	}
	return ""
}

func repositoryReviewAssignmentBit(bits []byte, index int) bool {
	return index >= 0 && index/8 < len(bits) && bits[index/8]&(1<<uint(index%8)) != 0
}

func setRepositoryReviewAssignmentBit(bits []byte, index int) bool {
	if index < 0 || index/8 >= len(bits) {
		return false
	}
	mask := byte(1 << uint(index%8))
	changed := bits[index/8]&mask == 0
	bits[index/8] |= mask
	return changed
}

func projectRepositoryReviewAssignmentCoverage(
	coverage RepositoryReviewCampaignPathCoverage,
	catalog []RepositoryReviewAssignment,
) (RepositoryReviewCampaignPathCoverage, error) {
	if coverage.Unsupported {
		if coverage.AssignmentBits != "" {
			return RepositoryReviewCampaignPathCoverage{}, ErrConflict
		}
		coverage.Inspected = false
		coverage.Completed = false
		return coverage, nil
	}
	bits, err := decodeRepositoryReviewAssignmentBits(coverage.AssignmentBits, catalog)
	if err != nil {
		return RepositoryReviewCampaignPathCoverage{}, err
	}
	inspected := false
	completed := true
	for index, assignment := range catalog {
		set := repositoryReviewAssignmentBit(bits, index)
		inspected = inspected || set
		if assignment.Required && !set {
			completed = false
		}
	}
	coverage.AssignmentBits = encodeRepositoryReviewAssignmentBits(bits)
	coverage.Inspected = inspected
	coverage.Completed = completed
	return coverage, nil
}

func repositoryReviewAssignmentComplete(
	coverage RepositoryReviewCampaignPathCoverage,
	catalog []RepositoryReviewAssignment,
	assignmentID string,
) (bool, error) {
	index, found := repositoryReviewAssignmentIndex(catalog, assignmentID)
	if !found {
		return false, ErrInvalidPlan
	}
	bits, err := decodeRepositoryReviewAssignmentBits(coverage.AssignmentBits, catalog)
	if err != nil {
		return false, err
	}
	return repositoryReviewAssignmentBit(bits, index), nil
}

func setRepositoryReviewAssignmentComplete(
	coverage RepositoryReviewCampaignPathCoverage,
	catalog []RepositoryReviewAssignment,
	assignmentID string,
) (RepositoryReviewCampaignPathCoverage, bool, error) {
	if coverage.Unsupported {
		return RepositoryReviewCampaignPathCoverage{}, false, ErrConflict
	}
	index, found := repositoryReviewAssignmentIndex(catalog, assignmentID)
	if !found {
		return RepositoryReviewCampaignPathCoverage{}, false, ErrInvalidPlan
	}
	bits, err := decodeRepositoryReviewAssignmentBits(coverage.AssignmentBits, catalog)
	if err != nil {
		return RepositoryReviewCampaignPathCoverage{}, false, err
	}
	changed := setRepositoryReviewAssignmentBit(bits, index)
	coverage.AssignmentBits = encodeRepositoryReviewAssignmentBits(bits)
	coverage, err = projectRepositoryReviewAssignmentCoverage(coverage, catalog)
	return coverage, changed, err
}

func setAllRequiredRepositoryReviewAssignments(
	coverage RepositoryReviewCampaignPathCoverage,
	catalog []RepositoryReviewAssignment,
) (RepositoryReviewCampaignPathCoverage, bool, error) {
	if coverage.Unsupported {
		return RepositoryReviewCampaignPathCoverage{}, false, ErrConflict
	}
	bits, err := decodeRepositoryReviewAssignmentBits(coverage.AssignmentBits, catalog)
	if err != nil {
		return RepositoryReviewCampaignPathCoverage{}, false, err
	}
	changed := false
	for index, assignment := range catalog {
		if assignment.Required {
			changed = setRepositoryReviewAssignmentBit(bits, index) || changed
		}
	}
	coverage.AssignmentBits = encodeRepositoryReviewAssignmentBits(bits)
	coverage, err = projectRepositoryReviewAssignmentCoverage(coverage, catalog)
	return coverage, changed, err
}

// CreditRepositoryReviewAssignment returns a path projection with exactly one
// additional catalog bit. It is exposed for trusted legacy recovery adapters;
// persistence still requires ReconcileCampaign or a live checkpoint CAS.
func CreditRepositoryReviewAssignment(
	coverage RepositoryReviewCampaignPathCoverage,
	catalog []RepositoryReviewAssignment,
	assignmentID string,
) (RepositoryReviewCampaignPathCoverage, error) {
	normalized, err := NormalizeRepositoryReviewAssignmentCatalog(catalog)
	if err != nil {
		return RepositoryReviewCampaignPathCoverage{}, err
	}
	next, _, err := setRepositoryReviewAssignmentComplete(
		coverage, normalized, assignmentID,
	)
	return next, err
}

// CreditAllRequiredRepositoryReviewAssignments promotes an exact historical
// full-file checkpoint into every required catalog credit.
func CreditAllRequiredRepositoryReviewAssignments(
	coverage RepositoryReviewCampaignPathCoverage,
	catalog []RepositoryReviewAssignment,
) (RepositoryReviewCampaignPathCoverage, error) {
	normalized, err := NormalizeRepositoryReviewAssignmentCatalog(catalog)
	if err != nil {
		return RepositoryReviewCampaignPathCoverage{}, err
	}
	next, _, err := setAllRequiredRepositoryReviewAssignments(coverage, normalized)
	return next, err
}

func repositoryReviewAssignmentCatalogEqual(
	left []RepositoryReviewAssignment,
	right []RepositoryReviewAssignment,
) bool {
	return reflect.DeepEqual(left, right)
}

func normalizeRepositoryReviewAssignmentPlans(
	plans []RepositoryReviewAssignmentPlan,
	catalog []RepositoryReviewAssignment,
	allowed map[string]FileRef,
) ([]RepositoryReviewAssignmentPlan, error) {
	if len(plans) > len(catalog) {
		return nil, ErrInvalidPlan
	}
	out := make([]RepositoryReviewAssignmentPlan, 0, len(plans))
	seen := make(map[string]struct{}, len(plans))
	for _, plan := range plans {
		index, found := repositoryReviewAssignmentIndex(catalog, strings.TrimSpace(plan.AssignmentID))
		if !found || plan.AssignmentID != strings.TrimSpace(plan.AssignmentID) || len(plan.Files) == 0 {
			return nil, ErrInvalidPlan
		}
		assignment := catalog[index]
		expectedReviewer := assignment.Reviewer
		if expectedReviewer == "default" {
			expectedReviewer = ""
		}
		if plan.FocusID != assignment.FocusID || plan.Reviewer != expectedReviewer ||
			plan.Optional == assignment.Required || !validOptionalAutomationText(strings.TrimSpace(plan.Label), 1024) ||
			!validOptionalAutomationText(strings.TrimSpace(plan.Task), maxFindingTextBytes) {
			return nil, ErrInvalidPlan
		}
		if _, duplicate := seen[plan.AssignmentID]; duplicate {
			return nil, ErrInvalidPlan
		}
		seen[plan.AssignmentID] = struct{}{}
		files, err := normalizeFiles(plan.Files)
		if err != nil || len(files) != len(plan.Files) {
			return nil, ErrInvalidPlan
		}
		for _, file := range files {
			if bound, ok := allowed[file.Path]; !ok || bound != file {
				return nil, ErrInvalidPlan
			}
		}
		plan.Files = files
		plan.Label = strings.TrimSpace(plan.Label)
		plan.Task = strings.TrimSpace(plan.Task)
		out = append(out, plan)
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, _ := repositoryReviewAssignmentIndex(catalog, out[i].AssignmentID)
		right, _ := repositoryReviewAssignmentIndex(catalog, out[j].AssignmentID)
		return left < right
	})
	return out, nil
}

// BindRepositoryReviewAssignmentTasks adds trusted prompt text to a generated
// plan and renews its immutable digest. Assignment identities and file scopes
// remain unchanged.
func BindRepositoryReviewAssignmentTasks(
	plan Plan,
	tasks map[string]string,
) (Plan, error) {
	if plan.ID == "" || plan.ID != planDigest(plan) || len(plan.AssignmentPlans) == 0 {
		return Plan{}, ErrInvalidPlan
	}
	plan.ID = ""
	for index := range plan.AssignmentPlans {
		task := strings.TrimSpace(tasks[plan.AssignmentPlans[index].FocusID])
		if !validBoundedText(task, maxFindingTextBytes) {
			return Plan{}, ErrInvalidPlan
		}
		plan.AssignmentPlans[index].Task = task
		plan.AssignmentPlans[index].Label = plan.AssignmentPlans[index].FocusID
	}
	plan.ID = planDigest(plan)
	if _, err := validateRepositoryReviewCampaignPlan(plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// CurrentCampaignAssignmentProgress derives required file-assignment coverage
// from the durable campaign bitmasks and active reservations. Optional fallback
// work remains observable in the catalog but does not prevent this required
// coverage projection from reaching completion.
func CurrentCampaignAssignmentProgress(
	state RepositoryState,
	campaignID string,
) RepositoryReviewAssignmentProgress {
	coverage := state.CurrentCampaign
	campaignID = strings.TrimSpace(campaignID)
	if coverage == nil || campaignID == "" || coverage.ID != campaignID ||
		len(coverage.AssignmentCatalog) == 0 {
		return RepositoryReviewAssignmentProgress{}
	}
	unsupported := 0
	for _, pathCoverage := range coverage.Paths {
		if pathCoverage.Unsupported {
			unsupported++
		}
	}
	reviewableFiles := max(0, coverage.SelectedFiles-unsupported)
	progress := RepositoryReviewAssignmentProgress{}
	for _, assignment := range coverage.AssignmentCatalog {
		if !assignment.Required {
			continue
		}
		counts := repositoryReviewAssignmentFocusProgress(&progress, assignment.FocusID)
		counts.Total += reviewableFiles
		progress.Total += reviewableFiles
	}
	for _, pathCoverage := range coverage.Paths {
		if pathCoverage.Unsupported {
			continue
		}
		bits, err := decodeRepositoryReviewAssignmentBits(
			pathCoverage.AssignmentBits, coverage.AssignmentCatalog,
		)
		if err != nil {
			return RepositoryReviewAssignmentProgress{}
		}
		for index, assignment := range coverage.AssignmentCatalog {
			if !assignment.Required || !repositoryReviewAssignmentBit(bits, index) {
				continue
			}
			progress.Completed++
			repositoryReviewAssignmentFocusProgress(&progress, assignment.FocusID).Completed++
		}
	}
	if active := state.ActiveReviewRun; active != nil && active.CampaignID == campaignID {
		for assignmentID, reservation := range active.Reservations {
			if reservation.CheckpointDigest != "" {
				continue
			}
			index, found := repositoryReviewAssignmentIndex(
				coverage.AssignmentCatalog, assignmentID,
			)
			if !found || !coverage.AssignmentCatalog[index].Required {
				continue
			}
			activeCount := 0
			for _, file := range reservation.Files {
				pathCoverage := coverage.Paths[file.Path]
				complete, err := repositoryReviewAssignmentComplete(
					pathCoverage, coverage.AssignmentCatalog, assignmentID,
				)
				if err == nil && !complete && !pathCoverage.Unsupported {
					activeCount++
				}
			}
			progress.Active += activeCount
			repositoryReviewAssignmentFocusProgress(
				&progress, coverage.AssignmentCatalog[index].FocusID,
			).Active += activeCount
		}
	}
	progress.Pending = max(0, progress.Total-progress.Completed-progress.Active)
	for _, counts := range []*RepositoryReviewAssignmentFocusProgress{
		&progress.ByFocus.CorrectnessState,
		&progress.ByFocus.SecurityTrust,
		&progress.ByFocus.ConcurrencyRecovery,
		&progress.ByFocus.IntegrationValidation,
	} {
		counts.Pending = max(0, counts.Total-counts.Completed-counts.Active)
	}
	return progress
}

func repositoryReviewAssignmentFocusProgress(
	progress *RepositoryReviewAssignmentProgress,
	focusID string,
) *RepositoryReviewAssignmentFocusProgress {
	switch focusID {
	case RepositoryReviewFocusCorrectnessState:
		return &progress.ByFocus.CorrectnessState
	case RepositoryReviewFocusSecurityTrust:
		return &progress.ByFocus.SecurityTrust
	case RepositoryReviewFocusConcurrencyRecovery:
		return &progress.ByFocus.ConcurrencyRecovery
	default:
		return &progress.ByFocus.IntegrationValidation
	}
}
