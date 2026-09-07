package repoaudit

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/routing"
)

const (
	maxRepositoryReviewFileAttributions      = 100_000
	maxRepositoryReviewFileAttributionFiles  = maxReviewFiles * maxRepositoryReviewRequiredAssignments
	maxRepositoryReviewAttributionChildIndex = 1_000_000
)

// RepositoryReviewFileAttributionSource identifies the trusted evidence path
// that produced an attribution record.
type RepositoryReviewFileAttributionSource string

const RepositoryReviewFileAttributionSourceLiveCheckpoint RepositoryReviewFileAttributionSource = "live_checkpoint"

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
		attributions, maxRepositoryReviewFileAttributionFiles,
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
	return source == RepositoryReviewFileAttributionSourceLiveCheckpoint
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
