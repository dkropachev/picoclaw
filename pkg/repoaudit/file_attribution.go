package repoaudit

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"

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
	Repository      string                            `json:"repository"`
	ExpectedVersion int64                             `json:"expected_version"`
	Attributions    []RepositoryReviewFileAttribution `json:"attributions"`
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
	if !validBoundedText(request.Repository, maxRepositoryIdentityBytes) ||
		request.ExpectedVersion < 0 || len(request.Attributions) == 0 ||
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
	if len(additions) == 0 {
		return state, nil
	}
	if state.Version != request.ExpectedVersion ||
		len(state.FileAttributions)+len(additions) > maxRepositoryReviewFileAttributions {
		return RepositoryState{}, ErrConflict
	}
	for _, attribution := range additions {
		state.FileAttributions = append(
			state.FileAttributions, cloneRepositoryReviewFileAttribution(attribution),
		)
	}
	sortRepositoryReviewFileAttributions(state.FileAttributions)
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
		if totalFiles > maxRepositoryReviewAttributedFileCredits {
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
