package repoaudit

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const (
	RepositoryReviewAutomationSchemaVersion = 1

	defaultAutomationMaxFilesPerRun              = 24
	defaultAutomationMaxContentBytes             = 512 << 10
	defaultAutomationMaxParallelChildren         = 1
	defaultAutomationEstimatedOutputTokens       = 1_800
	defaultAutomationCheckInterval               = 60
	maxAutomationFileBytes                 int64 = 4 << 20
	maxAutomationCount                           = 10_000
	maxAutomationRunIDs                          = 1_000
	maxAutomationReviewers                       = 32
	maxAutomationAccounts                        = 64
	maxAutomationWindowPolicies                  = 32
	maxAutomationAccountSnapshots                = 256
	automationModelCoverageSketchBytes           = 8 << 10
	maxAutomationModelPrice                      = 1_000_000.0
	maxAutomationEstimatedCost                   = 1_000_000_000.0
	maxAutomationTokens                    int64 = 1_000_000_000_000
)

var (
	ErrInvalidAutomation          = errors.New("invalid repository review automation")
	ErrAutomationControllerLocked = errors.New("repository review automation controller is already active")
)

// RepositoryReviewAutomationStatus describes the durable controller state.
type RepositoryReviewAutomationStatus string

const (
	RepositoryReviewAutomationIdle      RepositoryReviewAutomationStatus = "idle"
	RepositoryReviewAutomationRunning   RepositoryReviewAutomationStatus = "running"
	RepositoryReviewAutomationStopping  RepositoryReviewAutomationStatus = "stopping"
	RepositoryReviewAutomationPaused    RepositoryReviewAutomationStatus = "paused"
	RepositoryReviewAutomationCompleted RepositoryReviewAutomationStatus = "completed"
	RepositoryReviewAutomationFailed    RepositoryReviewAutomationStatus = "failed"
)

// RepositoryReviewPauseReason records why a controller stopped admitting work.
type RepositoryReviewPauseReason string

const (
	RepositoryReviewPauseManual         RepositoryReviewPauseReason = "manual"
	RepositoryReviewPauseTokenBudget    RepositoryReviewPauseReason = "token_budget"
	RepositoryReviewPauseCostBudget     RepositoryReviewPauseReason = "cost_budget"
	RepositoryReviewPauseAccountLimit   RepositoryReviewPauseReason = "account_limit"
	RepositoryReviewPauseRunFailed      RepositoryReviewPauseReason = "run_failed"
	RepositoryReviewPauseServiceRestart RepositoryReviewPauseReason = "service_restart"
)

// RepositoryReviewBudgetPolicy controls admission and automatic quota recovery.
// Zero token/cost limits disable the corresponding aggregate budget.
type RepositoryReviewBudgetPolicy struct {
	MaxTotalTokens              int64              `json:"max_total_tokens,omitempty"`
	MaxEstimatedCostUSD         float64            `json:"max_estimated_cost_usd,omitempty"`
	AccountIDs                  []string           `json:"account_ids,omitempty"`
	MinRemainingPercent         float64            `json:"min_remaining_percent,omitempty"`
	MinRemainingPercentByWindow map[string]float64 `json:"min_remaining_percent_by_window,omitempty"`
	AutoResume                  bool               `json:"auto_resume"`
	PauseOnUnknown              bool               `json:"pause_on_unknown"`
	CheckIntervalSeconds        int                `json:"check_interval_seconds"`
}

// RepositoryReviewModelPrice is caller-supplied comparison metadata keyed by
// the reviewer alias selected in ReviewerModels.
type RepositoryReviewModelPrice struct {
	InputPricePer1M  float64 `json:"input_price_per_1m"`
	OutputPricePer1M float64 `json:"output_price_per_1m"`
	Subscription     bool    `json:"subscription,omitempty"`
	EquivalentModel  string  `json:"equivalent_model,omitempty"`
}

// RepositoryReviewTokenUsage is the cumulative token accounting accepted by
// the controller. CachedTokens is included in, rather than added to,
// PromptTokens when providers report it that way.
type RepositoryReviewTokenUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	CachedTokens     int64 `json:"cached_tokens,omitempty"`
	TotalTokens      int64 `json:"total_tokens"`
}

type RepositoryReviewProgress struct {
	Stage            string `json:"stage,omitempty"`
	CompletedBatches int    `json:"completed_batches"`
	TotalBatches     int    `json:"total_batches"`
	ReviewedFiles    int    `json:"reviewed_files"`
	RemainingFiles   int    `json:"remaining_files"`
	UnsupportedFiles int    `json:"unsupported_files"`
	Findings         int    `json:"findings"`
}

type RepositoryReviewModelStats struct {
	Tokens           RepositoryReviewTokenUsage `json:"tokens"`
	EstimatedCostUSD float64                    `json:"estimated_cost_usd"`
	Requests         int64                      `json:"requests"`
	Failures         int64                      `json:"failures"`
	Findings         int                        `json:"findings"`
	ReviewedFiles    int                        `json:"reviewed_files"`
	LatencyMillis    int64                      `json:"latency_millis"`
}

// RepositoryReviewAccountLimitSnapshot is a flattened account/window reading.
// A nil RemainingPercent means the account limit was unknown at CheckedAt.
type RepositoryReviewAccountLimitSnapshot struct {
	AccountID        string    `json:"account_id"`
	Name             string    `json:"name,omitempty"`
	Window           string    `json:"window"`
	RemainingPercent *float64  `json:"remaining_percent,omitempty"`
	ResetsAt         time.Time `json:"resets_at,omitempty"`
	CheckedAt        time.Time `json:"checked_at"`
	Detail           string    `json:"detail,omitempty"`
}

// RepositoryReviewAutomation is independent of a RepositoryState and may be
// created before the first review plan, run, or finding exists.
type RepositoryReviewAutomation struct {
	SchemaVersion         int                                    `json:"schema_version"`
	ID                    string                                 `json:"id"`
	Version               int64                                  `json:"version"`
	Name                  string                                 `json:"name"`
	Repository            string                                 `json:"repository"`
	Ref                   string                                 `json:"ref,omitempty"`
	Target                string                                 `json:"target"`
	ReviewFocus           string                                 `json:"review_focus"`
	ReviewerModels        []string                               `json:"reviewer_models"`
	CompareModels         bool                                   `json:"compare_models"`
	ModelPrices           map[string]RepositoryReviewModelPrice  `json:"model_prices,omitempty"`
	Force                 bool                                   `json:"force"`
	AutoContinue          bool                                   `json:"auto_continue"`
	MaxFilesPerRun        int                                    `json:"max_files_per_run"`
	MaxContentBytes       int64                                  `json:"max_content_bytes"`
	MaxParallelChildren   int                                    `json:"max_parallel_children"`
	EstimatedOutputTokens int                                    `json:"estimated_output_tokens"`
	BudgetPolicy          RepositoryReviewBudgetPolicy           `json:"budget"`
	Status                RepositoryReviewAutomationStatus       `json:"status"`
	PauseReason           RepositoryReviewPauseReason            `json:"pause_reason,omitempty"`
	PauseDetail           string                                 `json:"pause_detail,omitempty"`
	RequestedPauseReason  RepositoryReviewPauseReason            `json:"requested_pause_reason,omitempty"`
	RequestedPauseDetail  string                                 `json:"requested_pause_detail,omitempty"`
	ActiveRunID           string                                 `json:"active_run_id,omitempty"`
	RunIDs                []string                               `json:"run_ids"`
	Usage                 RepositoryReviewTokenUsage             `json:"usage"`
	EstimatedCostUSD      float64                                `json:"estimated_cost_usd"`
	Progress              RepositoryReviewProgress               `json:"progress"`
	ModelStats            map[string]RepositoryReviewModelStats  `json:"model_stats"`
	ModelCoverageSketches map[string]string                      `json:"model_coverage_sketches,omitempty"`
	AccountLimitSnapshots []RepositoryReviewAccountLimitSnapshot `json:"account_limits"`
	NextCheckAt           time.Time                              `json:"next_check_at,omitempty"`
	StartedAt             time.Time                              `json:"started_at,omitempty"`
	CompletedAt           time.Time                              `json:"completed_at,omitempty"`
	CreatedAt             time.Time                              `json:"created_at"`
	UpdatedAt             time.Time                              `json:"updated_at"`
}

func (s Store) ListAutomations(ctx context.Context) ([]RepositoryReviewAutomation, error) {
	return s.listAutomations(ctx, maxAutomationCount)
}

func (s Store) listAutomations(ctx context.Context, maximum int) ([]RepositoryReviewAutomation, error) {
	return s.listAutomationsWithLoader(ctx, maximum, s.loadAutomation)
}

func (s Store) listAutomationsWithLoader(
	ctx context.Context,
	maximum int,
	load func(string) (RepositoryReviewAutomation, bool, error),
) ([]RepositoryReviewAutomation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	unlock, lockErr := s.lock("repository-review-automations")
	if lockErr != nil {
		return nil, lockErr
	}
	defer unlock()
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	if rootErr := s.requireSafeRoot(true); rootErr != nil {
		return nil, rootErr
	}
	entries, err := os.ReadDir(s.root)
	if os.IsNotExist(err) {
		return []RepositoryReviewAutomation{}, nil
	}
	if err != nil {
		return nil, err
	}
	automations := make([]RepositoryReviewAutomation, 0)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "automation_") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return nil, fmt.Errorf("repository review automation %q must be a regular file", entry.Name())
		}
		id := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "automation_"), ".json")
		if !validAutomationID(id) || entry.Name() != automationFilename(id) {
			return nil, fmt.Errorf("%w: invalid automation filename", ErrInvalidAutomation)
		}
		if len(automations) >= maximum {
			return nil, fmt.Errorf("%w: automation catalog exceeds its limit", ErrInvalidAutomation)
		}
		automation, found, loadErr := load(id)
		if loadErr != nil {
			return nil, loadErr
		}
		if !found {
			return nil, errors.New("repository review automation disappeared while locked")
		}
		automations = append(automations, automation)
	}
	sort.Slice(automations, func(i, j int) bool {
		if automations[i].UpdatedAt.Equal(automations[j].UpdatedAt) {
			return automations[i].ID < automations[j].ID
		}
		return automations[i].UpdatedAt.After(automations[j].UpdatedAt)
	})
	return automations, nil
}

func (s Store) GetAutomation(ctx context.Context, id string) (RepositoryReviewAutomation, bool, error) {
	if err := ctx.Err(); err != nil {
		return RepositoryReviewAutomation{}, false, err
	}
	id = strings.TrimSpace(id)
	if !validAutomationID(id) {
		return RepositoryReviewAutomation{}, false, fmt.Errorf("%w: invalid ID", ErrInvalidAutomation)
	}
	unlock, err := s.lock("automation:" + id)
	if err != nil {
		return RepositoryReviewAutomation{}, false, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return RepositoryReviewAutomation{}, false, err
	}
	return s.loadAutomation(id)
}

func (s Store) CreateAutomation(
	ctx context.Context,
	automation RepositoryReviewAutomation,
) (RepositoryReviewAutomation, error) {
	if err := ctx.Err(); err != nil {
		return RepositoryReviewAutomation{}, err
	}
	automation = cloneAutomation(automation)
	if strings.TrimSpace(automation.ID) == "" {
		automation.ID = newAutomationID()
	}
	automation.ID = strings.TrimSpace(automation.ID)
	if !validAutomationID(automation.ID) {
		return RepositoryReviewAutomation{}, fmt.Errorf("%w: invalid ID", ErrInvalidAutomation)
	}
	unlock, err := s.lock("automation:" + automation.ID)
	if err != nil {
		return RepositoryReviewAutomation{}, err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return RepositoryReviewAutomation{}, err
	}
	if _, found, err := s.loadAutomation(automation.ID); err != nil {
		return RepositoryReviewAutomation{}, err
	} else if found {
		return RepositoryReviewAutomation{}, ErrConflict
	}
	now := s.clock()
	automation.SchemaVersion = RepositoryReviewAutomationSchemaVersion
	automation.Version = 1
	automation.CreatedAt = now
	automation.UpdatedAt = now
	if automation.Status == "" {
		automation.Status = RepositoryReviewAutomationIdle
	}
	if err := normalizeAutomation(&automation); err != nil {
		return RepositoryReviewAutomation{}, err
	}
	if err := s.saveAutomation(automation); err != nil {
		return RepositoryReviewAutomation{}, err
	}
	return cloneAutomation(automation), nil
}

func (s Store) UpdateAutomation(
	ctx context.Context,
	id string,
	expectedVersion int64,
	mutate func(*RepositoryReviewAutomation) error,
) (RepositoryReviewAutomation, error) {
	if err := ctx.Err(); err != nil {
		return RepositoryReviewAutomation{}, err
	}
	id = strings.TrimSpace(id)
	if !validAutomationID(id) || mutate == nil {
		return RepositoryReviewAutomation{}, fmt.Errorf("%w: invalid update", ErrInvalidAutomation)
	}
	unlock, lockErr := s.lock("automation:" + id)
	if lockErr != nil {
		return RepositoryReviewAutomation{}, lockErr
	}
	defer unlock()
	if contextErr := ctx.Err(); contextErr != nil {
		return RepositoryReviewAutomation{}, contextErr
	}
	current, found, err := s.loadAutomation(id)
	if err != nil {
		return RepositoryReviewAutomation{}, err
	}
	if !found {
		return RepositoryReviewAutomation{}, os.ErrNotExist
	}
	if expectedVersion < 1 || current.Version != expectedVersion {
		return RepositoryReviewAutomation{}, ErrConflict
	}
	candidate := cloneAutomation(current)
	if err := mutate(&candidate); err != nil {
		return RepositoryReviewAutomation{}, err
	}
	if candidate.ID != current.ID || candidate.Version != current.Version ||
		!candidate.CreatedAt.Equal(current.CreatedAt) || candidate.SchemaVersion != current.SchemaVersion {
		return RepositoryReviewAutomation{}, fmt.Errorf("%w: immutable fields changed", ErrInvalidAutomation)
	}
	// A controller callback may assign slices, maps, or snapshot pointers owned
	// by its request. Detach them before normalization and persistence.
	candidate = cloneAutomation(candidate)
	candidate.Version++
	candidate.UpdatedAt = s.clock()
	if err := normalizeAutomation(&candidate); err != nil {
		return RepositoryReviewAutomation{}, err
	}
	if err := s.saveAutomation(candidate); err != nil {
		return RepositoryReviewAutomation{}, err
	}
	return cloneAutomation(candidate), nil
}

func (s Store) DeleteAutomation(ctx context.Context, id string, expectedVersion int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if !validAutomationID(id) {
		return fmt.Errorf("%w: invalid ID", ErrInvalidAutomation)
	}
	unlock, lockErr := s.lock("automation:" + id)
	if lockErr != nil {
		return lockErr
	}
	defer unlock()
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	automation, found, err := s.loadAutomation(id)
	if err != nil {
		return err
	}
	if !found {
		return os.ErrNotExist
	}
	if expectedVersion < 1 || automation.Version != expectedVersion {
		return ErrConflict
	}
	return fileutil.RemoveDurable(s.automationPath(id))
}

func (s Store) loadAutomation(id string) (RepositoryReviewAutomation, bool, error) {
	if !validAutomationID(id) {
		return RepositoryReviewAutomation{}, false, fmt.Errorf("%w: invalid ID", ErrInvalidAutomation)
	}
	if err := s.requireSafeRoot(true); err != nil {
		return RepositoryReviewAutomation{}, false, err
	}
	statePath := s.automationPath(id)
	info, err := os.Lstat(statePath)
	if os.IsNotExist(err) {
		return RepositoryReviewAutomation{}, false, nil
	}
	if err != nil {
		return RepositoryReviewAutomation{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return RepositoryReviewAutomation{}, false, errors.New("repository review automation must be a regular file")
	}
	if info.Size() > maxAutomationFileBytes {
		return RepositoryReviewAutomation{}, false, errors.New("repository review automation exceeds its size limit")
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		return RepositoryReviewAutomation{}, false, err
	}
	var automation RepositoryReviewAutomation
	if err := json.Unmarshal(data, &automation); err != nil {
		return RepositoryReviewAutomation{}, false, err
	}
	if automation.ID != id {
		return RepositoryReviewAutomation{}, false, errors.New("repository review automation identity mismatch")
	}
	if err := normalizeAutomation(&automation); err != nil {
		return RepositoryReviewAutomation{}, false, err
	}
	return automation, true, nil
}

func (s Store) saveAutomation(automation RepositoryReviewAutomation) error {
	if err := normalizeAutomation(&automation); err != nil {
		return err
	}
	if err := s.ensureSafeRoot(fileutil.MkdirAllDurable); err != nil {
		return err
	}
	statePath := s.automationPath(automation.ID)
	if info, err := os.Lstat(statePath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("repository review automation must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := json.Marshal(automation)
	if err != nil {
		return err
	}
	if int64(len(data)) > maxAutomationFileBytes {
		return errors.New("repository review automation exceeds its size limit")
	}
	return fileutil.WriteFileAtomic(statePath, data, 0o600)
}

func (s Store) automationPath(id string) string {
	return filepath.Join(s.root, automationFilename(id))
}

func automationFilename(id string) string {
	return "automation_" + id + ".json"
}

func newAutomationID() string {
	return "rra_" + strings.ToLower(rand.Text())
}

func validAutomationID(id string) bool {
	if !strings.HasPrefix(id, "rra_") || len(id) < 5 || len(id) > 128 {
		return false
	}
	for index, character := range id[4:] {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			index > 0 && (character == '_' || character == '-') {
			continue
		}
		return false
	}
	return true
}

func normalizeAutomation(automation *RepositoryReviewAutomation) error {
	if automation == nil {
		return fmt.Errorf("%w: state is required", ErrInvalidAutomation)
	}
	automation.ID = strings.TrimSpace(automation.ID)
	automation.Name = strings.TrimSpace(automation.Name)
	automation.Repository = strings.TrimSpace(automation.Repository)
	automation.Ref = strings.TrimSpace(automation.Ref)
	automation.Target = strings.TrimSpace(automation.Target)
	automation.ReviewFocus = strings.TrimSpace(automation.ReviewFocus)
	automation.PauseDetail = strings.TrimSpace(automation.PauseDetail)
	automation.RequestedPauseDetail = strings.TrimSpace(automation.RequestedPauseDetail)
	automation.ActiveRunID = strings.TrimSpace(automation.ActiveRunID)
	automation.Progress.Stage = strings.TrimSpace(automation.Progress.Stage)
	automation.Status = RepositoryReviewAutomationStatus(strings.ToLower(strings.TrimSpace(string(automation.Status))))
	automation.PauseReason = RepositoryReviewPauseReason(
		strings.ToLower(strings.TrimSpace(string(automation.PauseReason))),
	)
	if automation.Target == "" {
		automation.Target = "all"
	}
	if automation.MaxFilesPerRun == 0 {
		automation.MaxFilesPerRun = defaultAutomationMaxFilesPerRun
	}
	if automation.MaxContentBytes == 0 {
		automation.MaxContentBytes = defaultAutomationMaxContentBytes
	}
	if automation.MaxParallelChildren == 0 {
		automation.MaxParallelChildren = defaultAutomationMaxParallelChildren
	}
	if automation.EstimatedOutputTokens == 0 {
		automation.EstimatedOutputTokens = defaultAutomationEstimatedOutputTokens
	}
	if automation.BudgetPolicy.CheckIntervalSeconds == 0 {
		automation.BudgetPolicy.CheckIntervalSeconds = defaultAutomationCheckInterval
	}
	var err error
	automation.ReviewerModels, err = normalizeUniqueAutomationStrings(
		automation.ReviewerModels, maxAutomationReviewers, 256, "reviewer model",
	)
	if err != nil {
		return err
	}
	automation.BudgetPolicy.AccountIDs, err = normalizeUniqueAutomationStrings(
		automation.BudgetPolicy.AccountIDs, maxAutomationAccounts, 1024, "account ID",
	)
	if err != nil {
		return err
	}
	if len(automation.RunIDs) > maxAutomationRunIDs {
		automation.RunIDs = append([]string(nil), automation.RunIDs[len(automation.RunIDs)-maxAutomationRunIDs:]...)
	}
	automation.RunIDs, err = normalizeUniqueAutomationStrings(
		automation.RunIDs, maxAutomationRunIDs, 1024, "run ID",
	)
	if err != nil {
		return err
	}
	if automation.Usage.TotalTokens == 0 &&
		automation.Usage.PromptTokens >= 0 && automation.Usage.CompletionTokens >= 0 {
		automation.Usage.TotalTokens = automation.Usage.PromptTokens + automation.Usage.CompletionTokens
	}
	if err := normalizeModelPrices(automation); err != nil {
		return err
	}
	if err := normalizeModelStats(automation); err != nil {
		return err
	}
	if err := normalizeModelCoverageSketches(automation); err != nil {
		return err
	}
	if err := normalizeWindowPolicies(&automation.BudgetPolicy); err != nil {
		return err
	}
	if err := normalizeAccountSnapshots(automation); err != nil {
		return err
	}
	automation.NextCheckAt = automation.NextCheckAt.UTC()
	automation.StartedAt = automation.StartedAt.UTC()
	automation.CompletedAt = automation.CompletedAt.UTC()
	automation.CreatedAt = automation.CreatedAt.UTC()
	automation.UpdatedAt = automation.UpdatedAt.UTC()
	return validateAutomation(*automation)
}

func validateAutomation(automation RepositoryReviewAutomation) error {
	if automation.SchemaVersion != RepositoryReviewAutomationSchemaVersion ||
		!validAutomationID(automation.ID) || automation.Version < 1 ||
		!validBoundedText(automation.Name, 256) ||
		!validBoundedText(automation.Repository, maxRepositoryIdentityBytes) ||
		!validAutomationRepository(automation.Repository) ||
		!validOptionalAutomationText(automation.Ref, 1024) ||
		!validBoundedText(automation.Target, 4096) ||
		!validBoundedText(automation.ReviewFocus, maxFindingTextBytes) ||
		len(automation.ReviewerModels) == 0 || len(automation.ReviewerModels) > maxAutomationReviewers ||
		automation.CompareModels && len(automation.ReviewerModels) < 2 ||
		automation.MaxFilesPerRun < 1 || automation.MaxFilesPerRun > maxReviewFiles ||
		automation.MaxContentBytes < 1 || automation.MaxContentBytes > defaultAutomationMaxContentBytes ||
		automation.MaxParallelChildren < 1 || automation.MaxParallelChildren > 64 ||
		automation.EstimatedOutputTokens < 1 || automation.EstimatedOutputTokens > 65_536 ||
		!validOptionalAutomationText(automation.PauseDetail, 4096) ||
		!validOptionalAutomationText(automation.RequestedPauseDetail, 4096) ||
		!validOptionalAutomationText(automation.ActiveRunID, 1024) ||
		len(automation.RunIDs) > maxAutomationRunIDs ||
		!finiteNonnegative(automation.EstimatedCostUSD, maxAutomationEstimatedCost) ||
		automation.CreatedAt.IsZero() || automation.UpdatedAt.IsZero() ||
		automation.UpdatedAt.Before(automation.CreatedAt) {
		return ErrInvalidAutomation
	}
	if err := validateBudgetPolicy(automation.BudgetPolicy); err != nil {
		return err
	}
	if (automation.BudgetPolicy.MaxTotalTokens > 0 ||
		automation.BudgetPolicy.MaxEstimatedCostUSD > 0 ||
		len(automation.BudgetPolicy.AccountIDs) > 0 ||
		automation.BudgetPolicy.MinRemainingPercent > 0 ||
		len(automation.BudgetPolicy.MinRemainingPercentByWindow) > 0 ||
		automation.BudgetPolicy.PauseOnUnknown) &&
		automation.MaxParallelChildren != 1 {
		return fmt.Errorf(
			"%w: token, cost, and account guards require max_parallel_children=1 to bound overshoot to one provider response",
			ErrInvalidAutomation,
		)
	}
	if automation.BudgetPolicy.MaxEstimatedCostUSD > 0 {
		reviewers := automation.ReviewerModels
		if !automation.CompareModels && len(reviewers) > 1 {
			reviewers = reviewers[:1]
		}
		for _, reviewer := range reviewers {
			price, exists := automation.ModelPrices[reviewer]
			if !exists || price.InputPricePer1M <= 0 && price.OutputPricePer1M <= 0 {
				return fmt.Errorf(
					"%w: cost budget requires a positive price for reviewer %q",
					ErrInvalidAutomation,
					reviewer,
				)
			}
		}
	}
	if err := validateTokenUsage(automation.Usage); err != nil {
		return err
	}
	if err := validateProgress(automation.Progress); err != nil {
		return err
	}
	if !validAutomationStatus(automation.Status) || !validAutomationPauseReason(automation.PauseReason) ||
		!validAutomationPauseReason(automation.RequestedPauseReason) {
		return ErrInvalidAutomation
	}
	switch automation.Status {
	case RepositoryReviewAutomationRunning:
		if automation.ActiveRunID == "" || automation.PauseReason != "" || automation.PauseDetail != "" ||
			automation.RequestedPauseReason != "" || automation.RequestedPauseDetail != "" {
			return fmt.Errorf("%w: running status requires a run and no pause request", ErrInvalidAutomation)
		}
	case RepositoryReviewAutomationStopping:
		if automation.ActiveRunID == "" || automation.PauseReason != "" || automation.PauseDetail != "" ||
			automation.RequestedPauseReason == "" {
			return fmt.Errorf("%w: stopping status requires a run and requested pause reason", ErrInvalidAutomation)
		}
	case RepositoryReviewAutomationPaused:
		if automation.ActiveRunID != "" || automation.PauseReason == "" ||
			automation.RequestedPauseReason != "" || automation.RequestedPauseDetail != "" {
			return fmt.Errorf("%w: paused status requires a reason and no active run", ErrInvalidAutomation)
		}
	case RepositoryReviewAutomationFailed:
		if automation.ActiveRunID != "" || automation.PauseReason != RepositoryReviewPauseRunFailed ||
			automation.RequestedPauseReason != "" || automation.RequestedPauseDetail != "" {
			return fmt.Errorf("%w: failed status requires run_failed", ErrInvalidAutomation)
		}
	case RepositoryReviewAutomationIdle, RepositoryReviewAutomationCompleted:
		if automation.ActiveRunID != "" || automation.PauseReason != "" || automation.PauseDetail != "" ||
			automation.RequestedPauseReason != "" || automation.RequestedPauseDetail != "" {
			return fmt.Errorf("%w: inactive status has active pause or run state", ErrInvalidAutomation)
		}
	}
	if automation.ActiveRunID != "" && !containsAutomationString(automation.RunIDs, automation.ActiveRunID) {
		return fmt.Errorf("%w: active run is missing from run history", ErrInvalidAutomation)
	}
	return nil
}

func validAutomationRepository(repository string) bool {
	repository = strings.TrimSpace(repository)
	if repository == "" || !strings.Contains(repository, "://") {
		return repository != ""
	}
	parsed, err := url.Parse(repository)
	return err == nil && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func validateBudgetPolicy(policy RepositoryReviewBudgetPolicy) error {
	if policy.MaxTotalTokens < 0 || policy.MaxTotalTokens > maxAutomationTokens ||
		!finiteNonnegative(policy.MaxEstimatedCostUSD, maxAutomationEstimatedCost) ||
		!validPercent(policy.MinRemainingPercent) ||
		policy.CheckIntervalSeconds < 15 || policy.CheckIntervalSeconds > 3600 ||
		len(policy.AccountIDs) > maxAutomationAccounts ||
		len(policy.MinRemainingPercentByWindow) > maxAutomationWindowPolicies {
		return fmt.Errorf("%w: invalid budget policy", ErrInvalidAutomation)
	}
	return nil
}

func validateTokenUsage(usage RepositoryReviewTokenUsage) error {
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 || usage.CachedTokens < 0 ||
		usage.TotalTokens < usage.PromptTokens+usage.CompletionTokens ||
		usage.PromptTokens > maxAutomationTokens || usage.CompletionTokens > maxAutomationTokens ||
		usage.CachedTokens > usage.PromptTokens || usage.TotalTokens > maxAutomationTokens {
		return fmt.Errorf("%w: invalid token usage", ErrInvalidAutomation)
	}
	return nil
}

func validateProgress(progress RepositoryReviewProgress) error {
	if !validOptionalAutomationText(progress.Stage, 256) || progress.CompletedBatches < 0 ||
		progress.TotalBatches < 0 || progress.CompletedBatches > progress.TotalBatches ||
		progress.ReviewedFiles < 0 || progress.ReviewedFiles > maxReviewFiles ||
		progress.RemainingFiles < 0 || progress.RemainingFiles > maxReviewFiles ||
		progress.UnsupportedFiles < 0 || progress.UnsupportedFiles > maxReviewFiles ||
		progress.Findings < 0 || progress.Findings > maxReviewObservations {
		return fmt.Errorf("%w: invalid progress", ErrInvalidAutomation)
	}
	return nil
}

func normalizeModelPrices(automation *RepositoryReviewAutomation) error {
	if len(automation.ModelPrices) > maxAutomationReviewers {
		return fmt.Errorf("%w: too many model prices", ErrInvalidAutomation)
	}
	selected := automationReviewerSet(automation.ReviewerModels)
	normalized := make(map[string]RepositoryReviewModelPrice, len(automation.ModelPrices))
	for rawAlias, price := range automation.ModelPrices {
		alias := strings.TrimSpace(rawAlias)
		price.EquivalentModel = strings.TrimSpace(price.EquivalentModel)
		if alias == "" || alias != rawAlias && containsAutomationMapKey(normalized, alias) {
			return fmt.Errorf("%w: duplicate model price alias", ErrInvalidAutomation)
		}
		if _, exists := selected[alias]; !exists || !validBoundedText(alias, 256) ||
			!finiteNonnegative(price.InputPricePer1M, maxAutomationModelPrice) ||
			!finiteNonnegative(price.OutputPricePer1M, maxAutomationModelPrice) ||
			!validOptionalAutomationText(price.EquivalentModel, 256) {
			return fmt.Errorf("%w: invalid model price", ErrInvalidAutomation)
		}
		if _, duplicate := normalized[alias]; duplicate {
			return fmt.Errorf("%w: duplicate model price alias", ErrInvalidAutomation)
		}
		normalized[alias] = price
	}
	automation.ModelPrices = normalized
	return nil
}

func normalizeModelStats(automation *RepositoryReviewAutomation) error {
	if len(automation.ModelStats) > maxAutomationReviewers {
		return fmt.Errorf("%w: too many model statistics", ErrInvalidAutomation)
	}
	selected := automationReviewerSet(automation.ReviewerModels)
	normalized := make(map[string]RepositoryReviewModelStats, len(automation.ModelStats))
	for rawAlias, stats := range automation.ModelStats {
		alias := strings.TrimSpace(rawAlias)
		if _, exists := selected[alias]; !exists || !validBoundedText(alias, 256) {
			return fmt.Errorf("%w: invalid model statistics alias", ErrInvalidAutomation)
		}
		if stats.Tokens.TotalTokens == 0 && stats.Tokens.PromptTokens >= 0 && stats.Tokens.CompletionTokens >= 0 {
			stats.Tokens.TotalTokens = stats.Tokens.PromptTokens + stats.Tokens.CompletionTokens
		}
		if err := validateTokenUsage(stats.Tokens); err != nil {
			return err
		}
		if !finiteNonnegative(stats.EstimatedCostUSD, maxAutomationEstimatedCost) ||
			stats.Requests < 0 || stats.Failures < 0 || stats.Failures > stats.Requests ||
			stats.Findings < 0 || stats.Findings > maxReviewObservations ||
			stats.ReviewedFiles < 0 || stats.ReviewedFiles > maxReviewFiles ||
			stats.LatencyMillis < 0 {
			return fmt.Errorf("%w: invalid model statistics", ErrInvalidAutomation)
		}
		if _, duplicate := normalized[alias]; duplicate {
			return fmt.Errorf("%w: duplicate model statistics alias", ErrInvalidAutomation)
		}
		normalized[alias] = stats
	}
	automation.ModelStats = normalized
	return nil
}

func normalizeModelCoverageSketches(automation *RepositoryReviewAutomation) error {
	selected := automationReviewerSet(automation.ReviewerModels)
	normalized := make(map[string]string, len(automation.ModelCoverageSketches))
	for rawAlias, encoded := range automation.ModelCoverageSketches {
		alias := strings.TrimSpace(rawAlias)
		if _, exists := selected[alias]; !exists || !validBoundedText(alias, 256) {
			return fmt.Errorf("%w: invalid model coverage alias", ErrInvalidAutomation)
		}
		raw, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(encoded))
		if err != nil || len(raw) != automationModelCoverageSketchBytes {
			return fmt.Errorf("%w: invalid model coverage sketch", ErrInvalidAutomation)
		}
		normalized[alias] = base64.RawStdEncoding.EncodeToString(raw)
	}
	automation.ModelCoverageSketches = normalized
	return nil
}

func normalizeWindowPolicies(policy *RepositoryReviewBudgetPolicy) error {
	if len(policy.MinRemainingPercentByWindow) > maxAutomationWindowPolicies {
		return fmt.Errorf("%w: too many account-limit windows", ErrInvalidAutomation)
	}
	normalized := make(map[string]float64, len(policy.MinRemainingPercentByWindow))
	for rawWindow, percent := range policy.MinRemainingPercentByWindow {
		window := strings.ToLower(strings.TrimSpace(rawWindow))
		if !validBoundedText(window, 64) || !validPercent(percent) {
			return fmt.Errorf("%w: invalid account-limit window", ErrInvalidAutomation)
		}
		if _, duplicate := normalized[window]; duplicate {
			return fmt.Errorf("%w: duplicate account-limit window", ErrInvalidAutomation)
		}
		normalized[window] = percent
	}
	policy.MinRemainingPercentByWindow = normalized
	return nil
}

func normalizeAccountSnapshots(automation *RepositoryReviewAutomation) error {
	if len(automation.AccountLimitSnapshots) > maxAutomationAccountSnapshots {
		return fmt.Errorf("%w: too many account-limit snapshots", ErrInvalidAutomation)
	}
	configured := make(map[string]struct{}, len(automation.BudgetPolicy.AccountIDs))
	for _, id := range automation.BudgetPolicy.AccountIDs {
		configured[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(automation.AccountLimitSnapshots))
	for index := range automation.AccountLimitSnapshots {
		snapshot := &automation.AccountLimitSnapshots[index]
		snapshot.AccountID = strings.TrimSpace(snapshot.AccountID)
		snapshot.Name = strings.TrimSpace(snapshot.Name)
		snapshot.Window = strings.ToLower(strings.TrimSpace(snapshot.Window))
		snapshot.Detail = strings.TrimSpace(snapshot.Detail)
		snapshot.ResetsAt = snapshot.ResetsAt.UTC()
		snapshot.CheckedAt = snapshot.CheckedAt.UTC()
		if !validBoundedText(snapshot.AccountID, 1024) ||
			!validOptionalAutomationText(snapshot.Name, 256) ||
			!validBoundedText(snapshot.Window, 64) ||
			!validOptionalAutomationText(snapshot.Detail, 1024) || snapshot.CheckedAt.IsZero() {
			return fmt.Errorf("%w: invalid account-limit snapshot", ErrInvalidAutomation)
		}
		if len(configured) > 0 {
			if _, exists := configured[snapshot.AccountID]; !exists {
				return fmt.Errorf("%w: account-limit snapshot is not configured", ErrInvalidAutomation)
			}
		}
		if snapshot.RemainingPercent != nil && !validPercent(*snapshot.RemainingPercent) {
			return fmt.Errorf("%w: invalid remaining percentage", ErrInvalidAutomation)
		}
		key := snapshot.AccountID + "\x00" + snapshot.Name + "\x00" + snapshot.Window
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("%w: duplicate account-limit snapshot", ErrInvalidAutomation)
		}
		seen[key] = struct{}{}
	}
	sort.Slice(automation.AccountLimitSnapshots, func(i, j int) bool {
		left, right := automation.AccountLimitSnapshots[i], automation.AccountLimitSnapshots[j]
		if left.AccountID == right.AccountID && left.Name == right.Name {
			return left.Window < right.Window
		}
		if left.AccountID == right.AccountID {
			return left.Name < right.Name
		}
		return left.AccountID < right.AccountID
	})
	return nil
}

func normalizeUniqueAutomationStrings(values []string, maximum, maxBytes int, field string) ([]string, error) {
	if len(values) > maximum {
		return nil, fmt.Errorf("%w: too many %ss", ErrInvalidAutomation, field)
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !validBoundedText(value, maxBytes) {
			return nil, fmt.Errorf("%w: invalid %s", ErrInvalidAutomation, field)
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, fmt.Errorf("%w: duplicate %s", ErrInvalidAutomation, field)
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out, nil
}

func cloneAutomation(automation RepositoryReviewAutomation) RepositoryReviewAutomation {
	automation.ReviewerModels = append([]string{}, automation.ReviewerModels...)
	automation.RunIDs = append([]string{}, automation.RunIDs...)
	automation.BudgetPolicy.AccountIDs = append([]string{}, automation.BudgetPolicy.AccountIDs...)
	automation.BudgetPolicy.MinRemainingPercentByWindow = cloneAutomationFloatMap(
		automation.BudgetPolicy.MinRemainingPercentByWindow,
	)
	if automation.ModelPrices != nil {
		prices := make(map[string]RepositoryReviewModelPrice, len(automation.ModelPrices))
		for alias, price := range automation.ModelPrices {
			prices[alias] = price
		}
		automation.ModelPrices = prices
	}
	if automation.ModelStats != nil {
		stats := make(map[string]RepositoryReviewModelStats, len(automation.ModelStats))
		for alias, value := range automation.ModelStats {
			stats[alias] = value
		}
		automation.ModelStats = stats
	}
	if automation.ModelCoverageSketches != nil {
		sketches := make(map[string]string, len(automation.ModelCoverageSketches))
		for alias, value := range automation.ModelCoverageSketches {
			sketches[alias] = value
		}
		automation.ModelCoverageSketches = sketches
	}
	automation.AccountLimitSnapshots = append(
		[]RepositoryReviewAccountLimitSnapshot(nil), automation.AccountLimitSnapshots...,
	)
	for index := range automation.AccountLimitSnapshots {
		if remaining := automation.AccountLimitSnapshots[index].RemainingPercent; remaining != nil {
			remainingPercent := *remaining
			automation.AccountLimitSnapshots[index].RemainingPercent = &remainingPercent
		}
	}
	return automation
}

func cloneAutomationFloatMap(source map[string]float64) map[string]float64 {
	if source == nil {
		return nil
	}
	cloned := make(map[string]float64, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func automationReviewerSet(reviewers []string) map[string]struct{} {
	set := make(map[string]struct{}, len(reviewers))
	for _, reviewer := range reviewers {
		set[reviewer] = struct{}{}
	}
	return set
}

func containsAutomationString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsAutomationMapKey[V any](values map[string]V, key string) bool {
	_, exists := values[key]
	return exists
}

func validAutomationStatus(status RepositoryReviewAutomationStatus) bool {
	switch status {
	case RepositoryReviewAutomationIdle, RepositoryReviewAutomationRunning,
		RepositoryReviewAutomationStopping, RepositoryReviewAutomationPaused,
		RepositoryReviewAutomationCompleted, RepositoryReviewAutomationFailed:
		return true
	default:
		return false
	}
}

func validAutomationPauseReason(reason RepositoryReviewPauseReason) bool {
	switch reason {
	case "", RepositoryReviewPauseManual, RepositoryReviewPauseTokenBudget,
		RepositoryReviewPauseCostBudget, RepositoryReviewPauseAccountLimit,
		RepositoryReviewPauseRunFailed, RepositoryReviewPauseServiceRestart:
		return true
	default:
		return false
	}
}

func finiteNonnegative(value, maximum float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= maximum
}

func validPercent(value float64) bool {
	return finiteNonnegative(value, 100)
}

func validOptionalAutomationText(value string, maximum int) bool {
	return value == "" || validBoundedText(value, maximum)
}
