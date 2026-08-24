package repoaudit

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const (
	RepositoryReviewProfileSchemaVersion       = 1
	maxProfileFileBytes                  int64 = 1 << 20
	maxProfileCount                            = 10_000
)

var (
	ErrInvalidProfile  = errors.New("invalid repository review profile")
	ErrProfileAssigned = errors.New("repository review profile is assigned")
	ErrProfileActive   = errors.New("repository review profile has an active repository review")
)

// RepositoryReviewProfile is reusable review policy. Repository identity,
// branch selection, and controller runtime state remain on the automation.
type RepositoryReviewProfile struct {
	SchemaVersion         int                          `json:"schema_version"`
	ID                    string                       `json:"id"`
	Version               int64                        `json:"version"`
	Name                  string                       `json:"name"`
	ReviewFocus           string                       `json:"review_focus"`
	ScopePolicy           RepositoryReviewScopePolicy  `json:"scope_policy"`
	ReviewerModel         string                       `json:"reviewer_model"`
	ModelPrice            RepositoryReviewModelPrice   `json:"model_price"`
	Force                 bool                         `json:"force"`
	AutoContinue          bool                         `json:"auto_continue"`
	MaxFilesPerRun        int                          `json:"max_files_per_run"`
	MaxContentBytes       int64                        `json:"max_content_bytes"`
	MaxParallelChildren   int                          `json:"max_parallel_children"`
	EstimatedOutputTokens int                          `json:"estimated_output_tokens"`
	BudgetPolicy          RepositoryReviewBudgetPolicy `json:"budget"`
	CreatedAt             time.Time                    `json:"created_at"`
	UpdatedAt             time.Time                    `json:"updated_at"`
}

func (s Store) ListProfiles(ctx context.Context) ([]RepositoryReviewProfile, error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	unlock, err := s.lock("repository-review-profiles")
	if err != nil {
		return nil, err
	}
	defer unlock()
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	return s.listProfilesUnlocked(maxProfileCount)
}

func (s Store) GetProfile(ctx context.Context, id string) (RepositoryReviewProfile, bool, error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return RepositoryReviewProfile{}, false, contextErr
	}
	id = strings.TrimSpace(id)
	if !validProfileID(id) {
		return RepositoryReviewProfile{}, false, fmt.Errorf("%w: invalid ID", ErrInvalidProfile)
	}
	unlock, err := s.lock("profile:" + id)
	if err != nil {
		return RepositoryReviewProfile{}, false, err
	}
	defer unlock()
	if contextErr := ctx.Err(); contextErr != nil {
		return RepositoryReviewProfile{}, false, contextErr
	}
	return s.loadProfile(id)
}

func (s Store) CreateProfile(
	ctx context.Context,
	profile RepositoryReviewProfile,
) (RepositoryReviewProfile, error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return RepositoryReviewProfile{}, contextErr
	}
	profile = cloneProfile(profile)
	if strings.TrimSpace(profile.ID) == "" {
		profile.ID = newProfileID()
	}
	profile.ID = strings.TrimSpace(profile.ID)
	if !validProfileID(profile.ID) {
		return RepositoryReviewProfile{}, fmt.Errorf("%w: invalid ID", ErrInvalidProfile)
	}
	unlock, err := s.lock("profile:" + profile.ID)
	if err != nil {
		return RepositoryReviewProfile{}, err
	}
	defer unlock()
	if contextErr := ctx.Err(); contextErr != nil {
		return RepositoryReviewProfile{}, contextErr
	}
	if _, found, err := s.loadProfile(profile.ID); err != nil {
		return RepositoryReviewProfile{}, err
	} else if found {
		return RepositoryReviewProfile{}, ErrConflict
	}
	now := s.clock()
	profile.SchemaVersion = RepositoryReviewProfileSchemaVersion
	profile.Version = 1
	profile.CreatedAt = now
	profile.UpdatedAt = now
	if err := normalizeProfile(&profile); err != nil {
		return RepositoryReviewProfile{}, err
	}
	if err := s.saveProfile(profile); err != nil {
		return RepositoryReviewProfile{}, err
	}
	return cloneProfile(profile), nil
}

func (s Store) UpdateProfile(
	ctx context.Context,
	id string,
	expectedVersion int64,
	mutate func(*RepositoryReviewProfile) error,
) (RepositoryReviewProfile, error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return RepositoryReviewProfile{}, contextErr
	}
	id = strings.TrimSpace(id)
	if !validProfileID(id) || mutate == nil {
		return RepositoryReviewProfile{}, fmt.Errorf("%w: invalid update", ErrInvalidProfile)
	}
	unlock, err := s.lock("profile:" + id)
	if err != nil {
		return RepositoryReviewProfile{}, err
	}
	defer unlock()
	if contextErr := ctx.Err(); contextErr != nil {
		return RepositoryReviewProfile{}, contextErr
	}
	current, found, err := s.loadProfile(id)
	if err != nil {
		return RepositoryReviewProfile{}, err
	}
	if !found {
		return RepositoryReviewProfile{}, os.ErrNotExist
	}
	if expectedVersion < 1 || current.Version != expectedVersion {
		return RepositoryReviewProfile{}, ErrConflict
	}
	active, err := s.profileActiveUnlocked(id)
	if err != nil {
		return RepositoryReviewProfile{}, err
	}
	if active {
		return RepositoryReviewProfile{}, ErrProfileActive
	}
	candidate := cloneProfile(current)
	if err := mutate(&candidate); err != nil {
		return RepositoryReviewProfile{}, err
	}
	if candidate.ID != current.ID || candidate.Version != current.Version ||
		candidate.SchemaVersion != current.SchemaVersion || !candidate.CreatedAt.Equal(current.CreatedAt) {
		return RepositoryReviewProfile{}, fmt.Errorf("%w: immutable fields changed", ErrInvalidProfile)
	}
	candidate = cloneProfile(candidate)
	candidate.Version++
	candidate.UpdatedAt = s.clock()
	if err := normalizeProfile(&candidate); err != nil {
		return RepositoryReviewProfile{}, err
	}
	if err := s.saveProfile(candidate); err != nil {
		return RepositoryReviewProfile{}, err
	}
	return cloneProfile(candidate), nil
}

// IsProfileAssigned reports whether any repository configuration references
// the profile. The catalog-wide lock makes this safe against concurrent create,
// update, and delete operations in other processes.
func (s Store) IsProfileAssigned(ctx context.Context, id string) (bool, error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return false, contextErr
	}
	id = strings.TrimSpace(id)
	if !validProfileID(id) {
		return false, fmt.Errorf("%w: invalid ID", ErrInvalidProfile)
	}
	unlock, err := s.lock("profile-assignment:" + id)
	if err != nil {
		return false, err
	}
	defer unlock()
	if contextErr := ctx.Err(); contextErr != nil {
		return false, contextErr
	}
	return s.profileAssignedUnlocked(id)
}

func (s Store) DeleteProfile(ctx context.Context, id string, expectedVersion int64) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	id = strings.TrimSpace(id)
	if !validProfileID(id) {
		return fmt.Errorf("%w: invalid ID", ErrInvalidProfile)
	}
	unlock, err := s.lock("profile:" + id)
	if err != nil {
		return err
	}
	defer unlock()
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	profile, found, err := s.loadProfile(id)
	if err != nil {
		return err
	}
	if !found {
		return os.ErrNotExist
	}
	if expectedVersion < 1 || profile.Version != expectedVersion {
		return ErrConflict
	}
	assigned, err := s.profileAssignedUnlocked(id)
	if err != nil {
		return err
	}
	if assigned {
		return ErrProfileAssigned
	}
	return fileutil.RemoveDurable(s.profilePath(id))
}

// MaterializeRepositoryReviewAutomation applies reusable profile policy to a
// detached automation while preserving repository identity and runtime state.
func MaterializeRepositoryReviewAutomation(
	profile RepositoryReviewProfile,
	automation RepositoryReviewAutomation,
) (RepositoryReviewAutomation, error) {
	profile = cloneProfile(profile)
	if err := normalizeProfile(&profile); err != nil {
		return RepositoryReviewAutomation{}, err
	}
	automation = cloneAutomation(automation)
	automation.ProfileID = profile.ID
	automation.ProfileVersion = profile.Version
	automation.ReviewFocus = profile.ReviewFocus
	automation.ScopePolicy = profile.ScopePolicy
	automation.ReviewerModels = []string{profile.ReviewerModel}
	automation.CompareModels = false
	automation.ModelPrices = map[string]RepositoryReviewModelPrice{profile.ReviewerModel: profile.ModelPrice}
	automation.Force = profile.Force
	automation.AutoContinue = profile.AutoContinue
	automation.MaxFilesPerRun = profile.MaxFilesPerRun
	automation.MaxContentBytes = profile.MaxContentBytes
	automation.MaxParallelChildren = profile.MaxParallelChildren
	automation.EstimatedOutputTokens = profile.EstimatedOutputTokens
	automation.BudgetPolicy = profile.BudgetPolicy
	automation.Target = "all"
	branch, err := NormalizeRepositoryReviewBranch(automation.Ref)
	if err != nil {
		return RepositoryReviewAutomation{}, err
	}
	automation.Ref = branch
	return automation, nil
}

func (s Store) listProfilesUnlocked(maximum int) ([]RepositoryReviewProfile, error) {
	if err := s.requireSafeRoot(true); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.root)
	if os.IsNotExist(err) {
		return []RepositoryReviewProfile{}, nil
	}
	if err != nil {
		return nil, err
	}
	profiles := make([]RepositoryReviewProfile, 0)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "profile_") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return nil, fmt.Errorf("repository review profile %q must be a regular file", entry.Name())
		}
		id := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "profile_"), ".json")
		if !validProfileID(id) || entry.Name() != profileFilename(id) {
			return nil, fmt.Errorf("%w: invalid profile filename", ErrInvalidProfile)
		}
		if len(profiles) >= maximum {
			return nil, fmt.Errorf("%w: profile catalog exceeds its limit", ErrInvalidProfile)
		}
		profile, found, err := s.loadProfile(id)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, errors.New("repository review profile disappeared while locked")
		}
		profiles = append(profiles, profile)
	}
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].UpdatedAt.Equal(profiles[j].UpdatedAt) {
			return profiles[i].ID < profiles[j].ID
		}
		return profiles[i].UpdatedAt.After(profiles[j].UpdatedAt)
	})
	return profiles, nil
}

func (s Store) loadProfile(id string) (RepositoryReviewProfile, bool, error) {
	if !validProfileID(id) {
		return RepositoryReviewProfile{}, false, fmt.Errorf("%w: invalid ID", ErrInvalidProfile)
	}
	statePath := s.profilePath(id)
	data, found, err := s.readStateFile(
		statePath,
		maxProfileFileBytes,
		"repository review profile",
	)
	if err != nil {
		return RepositoryReviewProfile{}, false, err
	}
	if !found {
		return RepositoryReviewProfile{}, false, nil
	}
	var profile RepositoryReviewProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		return RepositoryReviewProfile{}, false, err
	}
	if profile.ID != id {
		return RepositoryReviewProfile{}, false, errors.New("repository review profile identity mismatch")
	}
	if err := normalizeProfile(&profile); err != nil {
		return RepositoryReviewProfile{}, false, err
	}
	return profile, true, nil
}

func (s Store) saveProfile(profile RepositoryReviewProfile) error {
	if err := normalizeProfile(&profile); err != nil {
		return err
	}
	if err := s.ensureSafeRoot(fileutil.MkdirAllDurable); err != nil {
		return err
	}
	statePath := s.profilePath(profile.ID)
	if info, err := os.Lstat(statePath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("repository review profile must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	if int64(len(data)) > maxProfileFileBytes {
		return errors.New("repository review profile exceeds its size limit")
	}
	return fileutil.WriteFileAtomic(statePath, data, 0o600)
}

func normalizeProfile(profile *RepositoryReviewProfile) error {
	if profile == nil {
		return fmt.Errorf("%w: state is required", ErrInvalidProfile)
	}
	profile.ID = strings.TrimSpace(profile.ID)
	profile.Name = strings.TrimSpace(profile.Name)
	profile.ReviewFocus = strings.TrimSpace(profile.ReviewFocus)
	profile.ReviewerModel = strings.TrimSpace(profile.ReviewerModel)
	profile.ModelPrice.EquivalentModel = strings.TrimSpace(profile.ModelPrice.EquivalentModel)
	if profile.MaxFilesPerRun == 0 {
		profile.MaxFilesPerRun = defaultAutomationMaxFilesPerRun
	}
	if profile.MaxContentBytes == 0 {
		profile.MaxContentBytes = defaultAutomationMaxContentBytes
	}
	if profile.MaxParallelChildren == 0 {
		profile.MaxParallelChildren = defaultAutomationMaxParallelChildren
	}
	if profile.EstimatedOutputTokens == 0 {
		profile.EstimatedOutputTokens = defaultAutomationEstimatedOutputTokens
	}
	if profile.BudgetPolicy.CheckIntervalSeconds == 0 {
		profile.BudgetPolicy.CheckIntervalSeconds = defaultAutomationCheckInterval
	}
	var err error
	profile.BudgetPolicy.AccountIDs, err = normalizeUniqueAutomationStrings(
		profile.BudgetPolicy.AccountIDs, maxAutomationAccounts, 1024, "account ID",
	)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProfile, err)
	}
	if err := normalizeRepositoryReviewScopePolicy(&profile.ScopePolicy); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProfile, err)
	}
	if err := normalizeWindowPolicies(&profile.BudgetPolicy); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProfile, err)
	}
	profile.CreatedAt = profile.CreatedAt.UTC()
	profile.UpdatedAt = profile.UpdatedAt.UTC()
	if profile.SchemaVersion != RepositoryReviewProfileSchemaVersion || !validProfileID(profile.ID) ||
		profile.Version < 1 || !validBoundedText(profile.Name, 256) ||
		!validBoundedText(profile.ReviewFocus, maxFindingTextBytes) ||
		!validBoundedText(profile.ReviewerModel, 256) ||
		!finiteNonnegative(profile.ModelPrice.InputPricePer1M, maxAutomationModelPrice) ||
		!finiteNonnegative(profile.ModelPrice.OutputPricePer1M, maxAutomationModelPrice) ||
		!validOptionalAutomationText(profile.ModelPrice.EquivalentModel, 256) ||
		profile.MaxFilesPerRun < 1 || profile.MaxFilesPerRun > maxReviewFiles ||
		profile.MaxContentBytes < 1 || profile.MaxContentBytes > defaultAutomationMaxContentBytes ||
		profile.MaxParallelChildren < 1 || profile.MaxParallelChildren > 64 ||
		profile.EstimatedOutputTokens < 1 || profile.EstimatedOutputTokens > 65_536 ||
		profile.CreatedAt.IsZero() || profile.UpdatedAt.IsZero() || profile.UpdatedAt.Before(profile.CreatedAt) {
		return ErrInvalidProfile
	}
	if err := validateBudgetPolicy(profile.BudgetPolicy); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProfile, err)
	}
	if (profile.BudgetPolicy.MaxTotalTokens > 0 || profile.BudgetPolicy.MaxEstimatedCostUSD > 0 ||
		len(profile.BudgetPolicy.AccountIDs) > 0 || profile.BudgetPolicy.MinRemainingPercent > 0 ||
		len(profile.BudgetPolicy.MinRemainingPercentByWindow) > 0 || profile.BudgetPolicy.PauseOnUnknown) &&
		profile.MaxParallelChildren != 1 {
		return fmt.Errorf("%w: guarded budgets require max_parallel_children=1", ErrInvalidProfile)
	}
	if profile.BudgetPolicy.MaxEstimatedCostUSD > 0 &&
		profile.ModelPrice.InputPricePer1M <= 0 && profile.ModelPrice.OutputPricePer1M <= 0 {
		return fmt.Errorf("%w: cost budget requires positive model pricing", ErrInvalidProfile)
	}
	return nil
}

func cloneProfile(profile RepositoryReviewProfile) RepositoryReviewProfile {
	profile.ScopePolicy.CodeTypes = append([]RepositoryReviewCodeType(nil), profile.ScopePolicy.CodeTypes...)
	profile.ScopePolicy.IncludeFolders = append([]string(nil), profile.ScopePolicy.IncludeFolders...)
	profile.ScopePolicy.ExcludeFolders = append([]string(nil), profile.ScopePolicy.ExcludeFolders...)
	profile.BudgetPolicy.AccountIDs = append([]string(nil), profile.BudgetPolicy.AccountIDs...)
	profile.BudgetPolicy.MinRemainingPercentByWindow = cloneAutomationFloatMap(
		profile.BudgetPolicy.MinRemainingPercentByWindow,
	)
	return profile
}

func (s Store) profileAssignedUnlocked(id string) (bool, error) {
	automations, err := s.listAutomationsUnlocked(maxAutomationCount)
	if err != nil {
		return false, err
	}
	for _, automation := range automations {
		if automation.ProfileID == id {
			return true, nil
		}
	}
	return false, nil
}

func (s Store) profileActiveUnlocked(id string) (bool, error) {
	automations, err := s.listAutomationsUnlocked(maxAutomationCount)
	if err != nil {
		return false, err
	}
	for _, automation := range automations {
		if automation.ProfileID == id &&
			(automation.Status == RepositoryReviewAutomationRunning ||
				automation.Status == RepositoryReviewAutomationStopping) {
			return true, nil
		}
	}
	return false, nil
}

func (s Store) profilePath(id string) string { return filepath.Join(s.root, profileFilename(id)) }

func profileFilename(id string) string { return "profile_" + id + ".json" }

func newProfileID() string { return "rrpf_" + strings.ToLower(rand.Text()) }

func validProfileID(id string) bool {
	if !strings.HasPrefix(id, "rrpf_") || len(id) < 6 || len(id) > 128 {
		return false
	}
	for index, character := range id[5:] {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			index > 0 && (character == '_' || character == '-') {
			continue
		}
		return false
	}
	return true
}
