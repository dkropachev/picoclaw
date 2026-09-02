package repoaudit

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	RepositoryReviewProfileSchemaVersion       = 4
	maxProfileFileBytes                  int64 = 1 << 20
	maxProfileCount                            = 10_000
	maxRepositoryReviewIssuePromptBytes        = 16 << 10
)

const DefaultRepositoryReviewIssuePrompt = `Present the confirmed diagnosis concisely. Include evidence, impact, validation already performed, the exact location, and commit/blob provenance. Do not include a fix or advice.`

var (
	ErrInvalidProfile  = errors.New("invalid repository review profile")
	ErrProfileAssigned = errors.New("repository review profile is assigned")
	ErrProfileActive   = errors.New("repository review profile has an active repository review")
)

// RepositoryReviewProfile is reusable review policy. Repository identity,
// branch selection, and controller runtime state remain on the automation.
type RepositoryReviewProfile struct {
	SchemaVersion                    int                          `json:"schema_version"`
	ID                               string                       `json:"id"`
	Version                          int64                        `json:"version"`
	Name                             string                       `json:"name"`
	ReviewFocus                      string                       `json:"review_focus"`
	ScopePolicy                      RepositoryReviewScopePolicy  `json:"scope_policy"`
	ReviewerModel                    string                       `json:"reviewer_model"`
	DeduplicationModel               string                       `json:"deduplication_model,omitempty"`
	DeduplicationSimilarityThreshold int                          `json:"deduplication_similarity_threshold"`
	DeduplicationCandidateLimit      int                          `json:"deduplication_candidate_limit"`
	IssueWriterModel                 string                       `json:"issue_writer_model,omitempty"`
	IssuePrompt                      string                       `json:"issue_prompt"`
	AccountRef                       string                       `json:"account_ref,omitempty"`
	Force                            bool                         `json:"force"`
	AutoContinue                     bool                         `json:"auto_continue"`
	MaxFilesPerRun                   int                          `json:"max_files_per_run"`
	MaxContentBytes                  int64                        `json:"max_content_bytes"`
	MaxParallelChildren              int                          `json:"max_parallel_children"`
	AssignmentTimeoutSeconds         int                          `json:"assignment_timeout_seconds"`
	BudgetPolicy                     RepositoryReviewBudgetPolicy `json:"budget"`
	CreatedAt                        time.Time                    `json:"created_at"`
	UpdatedAt                        time.Time                    `json:"updated_at"`
	// DeduplicationSettingsSpecified distinguishes an explicit zero/zero pair
	// from legacy Go callers which omit both newly added settings. It is an
	// in-memory create/update hint and is never persisted.
	DeduplicationSettingsSpecified bool `json:"-"`
}

func (s Store) ListProfiles(ctx context.Context) ([]RepositoryReviewProfile, error) {
	if s.broker != nil {
		return s.brokerListProfiles(ctx)
	}
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
	if s.broker != nil {
		return s.brokerGetProfile(ctx, id)
	}
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
	if s.broker != nil {
		return s.brokerCreateProfile(ctx, profile)
	}
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
	// The all-zero pair is the Go zero value used by callers which predate
	// campaign deduplication. Individually, zero remains a valid explicit
	// threshold or candidate limit.
	if !profile.DeduplicationSettingsSpecified &&
		profile.DeduplicationSimilarityThreshold == 0 &&
		profile.DeduplicationCandidateLimit == 0 {
		profile.DeduplicationSimilarityThreshold = DeduplicationDefaultThreshold
		profile.DeduplicationCandidateLimit = DeduplicationDefaultCandidateLimit
	}
	profile.DeduplicationSettingsSpecified = true
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
	profile.DeduplicationSettingsSpecified = false
	return cloneProfile(profile), nil
}

func (s Store) UpdateProfile(
	ctx context.Context,
	id string,
	expectedVersion int64,
	mutate func(*RepositoryReviewProfile) error,
) (RepositoryReviewProfile, error) {
	if s.broker != nil {
		return s.brokerUpdateProfile(ctx, id, expectedVersion, mutate)
	}
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
	candidate.DeduplicationSettingsSpecified = false
	return cloneProfile(candidate), nil
}

// IsProfileAssigned reports whether any repository configuration references
// the profile. The catalog-wide lock makes this safe against concurrent create,
// update, and delete operations in other processes.
func (s Store) IsProfileAssigned(ctx context.Context, id string) (bool, error) {
	if s.broker != nil {
		return s.brokerProfileAssigned(ctx, id)
	}
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
	if s.broker != nil {
		return s.brokerDeleteProfile(ctx, id, expectedVersion)
	}
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
	database, release, err := s.acquireDatabase(ctx)
	if err != nil {
		return err
	}
	defer release()
	return sqlitestore.Immediate(ctx, database, func(conn *sql.Conn) error {
		result, deleteErr := conn.ExecContext(ctx,
			`DELETE FROM repository_review_profiles WHERE profile_id = ? AND version = ?`,
			id, expectedVersion,
		)
		if deleteErr != nil {
			return deleteErr
		}
		return sqlitestore.RequireOneRow(result, ErrConflict)
	})
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
	automation.AccountRef = profile.AccountRef
	automation.ReviewFocus = profile.ReviewFocus
	automation.ScopePolicy = profile.ScopePolicy
	automation.ReviewerModels = []string{profile.ReviewerModel}
	automation.DeduplicationModel = profile.DeduplicationModel
	automation.DeduplicationSimilarityThreshold = profile.DeduplicationSimilarityThreshold
	automation.DeduplicationCandidateLimit = profile.DeduplicationCandidateLimit
	automation.DeduplicationSettingsSpecified = true
	automation.IssueWriterModel = strings.TrimSpace(profile.IssueWriterModel)
	if automation.IssueWriterModel == "" {
		automation.IssueWriterModel = profile.ReviewerModel
	}
	automation.CompareModels = false
	price, hasPrice := automation.ModelPrices[profile.ReviewerModel]
	automation.ModelPrices = make(map[string]RepositoryReviewModelPrice)
	if hasPrice {
		automation.ModelPrices[profile.ReviewerModel] = price
	}
	automation.Force = profile.Force
	automation.AutoContinue = profile.AutoContinue
	automation.MaxFilesPerRun = profile.MaxFilesPerRun
	automation.MaxContentBytes = profile.MaxContentBytes
	automation.MaxParallelChildren = profile.MaxParallelChildren
	automation.AssignmentTimeoutSeconds = profile.AssignmentTimeoutSeconds
	automation.EstimatedOutputTokens = defaultAutomationEstimatedOutputTokens
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
	database, release, err := s.acquireDatabase(context.Background())
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := database.Query(`
		SELECT profile_id FROM repository_review_profiles
	 ORDER BY updated_at_unix_nano DESC, profile_id ASC
	 LIMIT ?`, maximum+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	profiles := make([]RepositoryReviewProfile, 0)
	for rows.Next() {
		if len(profiles) >= maximum {
			return nil, fmt.Errorf("%w: profile catalog exceeds its limit", ErrInvalidProfile)
		}
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		profile, err := loadRepositoryReviewProfileRow(context.Background(), database, id)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func (s Store) loadProfile(id string) (RepositoryReviewProfile, bool, error) {
	if s.broker != nil {
		return s.brokerGetProfile(context.Background(), id)
	}
	if !validProfileID(id) {
		return RepositoryReviewProfile{}, false, fmt.Errorf("%w: invalid ID", ErrInvalidProfile)
	}
	database, release, err := s.acquireDatabase(context.Background())
	if err != nil {
		return RepositoryReviewProfile{}, false, err
	}
	defer release()
	profile, err := loadRepositoryReviewProfileRow(context.Background(), database, id)
	if errors.Is(err, sql.ErrNoRows) {
		return RepositoryReviewProfile{}, false, nil
	}
	if err != nil {
		return RepositoryReviewProfile{}, false, err
	}
	return profile, true, nil
}

func (s Store) saveProfile(profile RepositoryReviewProfile) error {
	if s.broker != nil {
		return database.NewError(database.CodeUnsupported, "repository review profile save is not broker-routed")
	}
	if err := normalizeProfile(&profile); err != nil {
		return err
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	if int64(len(encoded)) > maxProfileFileBytes {
		return errors.New("repository review profile exceeds its size limit")
	}
	database, release, err := s.acquireDatabase(context.Background())
	if err != nil {
		return err
	}
	defer release()
	return saveRepositoryReviewProfileDatabase(context.Background(), database, profile)
}

func normalizeProfile(profile *RepositoryReviewProfile) error {
	if profile == nil {
		return fmt.Errorf("%w: state is required", ErrInvalidProfile)
	}
	profile.ID = strings.TrimSpace(profile.ID)
	profile.Name = strings.TrimSpace(profile.Name)
	profile.ReviewFocus = strings.TrimSpace(profile.ReviewFocus)
	profile.ReviewerModel = strings.TrimSpace(profile.ReviewerModel)
	profile.DeduplicationModel = strings.TrimSpace(profile.DeduplicationModel)
	profile.IssueWriterModel = strings.TrimSpace(profile.IssueWriterModel)
	profile.IssuePrompt = strings.TrimSpace(profile.IssuePrompt)
	if profile.IssuePrompt == "" {
		profile.IssuePrompt = DefaultRepositoryReviewIssuePrompt
	}
	profile.AccountRef = strings.TrimSpace(profile.AccountRef)
	profile.BudgetPolicy.GuardExpression = strings.TrimSpace(profile.BudgetPolicy.GuardExpression)
	if profile.MaxFilesPerRun == 0 {
		profile.MaxFilesPerRun = defaultAutomationMaxFilesPerRun
	}
	if profile.MaxContentBytes == 0 {
		profile.MaxContentBytes = defaultAutomationMaxContentBytes
	}
	if profile.MaxParallelChildren == 0 {
		profile.MaxParallelChildren = defaultAutomationMaxParallelChildren
	}
	if profile.AssignmentTimeoutSeconds == 0 {
		profile.AssignmentTimeoutSeconds = DefaultRepositoryReviewAssignmentTimeoutSeconds
	}
	if err := normalizeRepositoryReviewScopePolicy(&profile.ScopePolicy); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProfile, err)
	}
	profile.CreatedAt = profile.CreatedAt.UTC()
	profile.UpdatedAt = profile.UpdatedAt.UTC()
	if profile.SchemaVersion != RepositoryReviewProfileSchemaVersion || !validProfileID(profile.ID) ||
		profile.Version < 1 || !validBoundedText(profile.Name, 256) ||
		!validBoundedText(profile.ReviewFocus, maxFindingTextBytes) ||
		!validBoundedText(profile.ReviewerModel, 256) ||
		!validOptionalAutomationText(profile.DeduplicationModel, 256) ||
		profile.DeduplicationSimilarityThreshold < 0 ||
		profile.DeduplicationSimilarityThreshold > 100 ||
		profile.DeduplicationCandidateLimit < 0 ||
		profile.DeduplicationCandidateLimit > DeduplicationMaximumShortlist ||
		!validOptionalAutomationText(profile.IssueWriterModel, 256) ||
		!validBoundedText(profile.IssuePrompt, maxRepositoryReviewIssuePromptBytes) ||
		!validOptionalAutomationText(profile.AccountRef, 256) ||
		profile.MaxFilesPerRun < 1 || profile.MaxFilesPerRun > maxReviewFiles ||
		profile.MaxContentBytes < 1 || profile.MaxContentBytes > defaultAutomationMaxContentBytes ||
		profile.MaxParallelChildren < 1 || profile.MaxParallelChildren > 64 ||
		profile.AssignmentTimeoutSeconds < MinRepositoryReviewAssignmentTimeoutSeconds ||
		profile.AssignmentTimeoutSeconds > MaxRepositoryReviewAssignmentTimeoutSeconds ||
		profile.AssignmentTimeoutSeconds%60 != 0 ||
		profile.CreatedAt.IsZero() || profile.UpdatedAt.IsZero() || profile.UpdatedAt.Before(profile.CreatedAt) {
		return ErrInvalidProfile
	}
	if err := validateBudgetPolicy(profile.BudgetPolicy); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProfile, err)
	}
	return nil
}

func cloneProfile(profile RepositoryReviewProfile) RepositoryReviewProfile {
	profile.ScopePolicy.CodeTypes = append([]RepositoryReviewCodeType(nil), profile.ScopePolicy.CodeTypes...)
	profile.ScopePolicy.IncludeFolders = append([]string(nil), profile.ScopePolicy.IncludeFolders...)
	profile.ScopePolicy.ExcludeFolders = append([]string(nil), profile.ScopePolicy.ExcludeFolders...)
	return profile
}

func (s Store) profileAssignedUnlocked(id string) (bool, error) {
	if s.broker != nil {
		return s.brokerProfileAssigned(context.Background(), id)
	}
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
	if s.broker != nil {
		return false, database.NewError(
			database.CodeUnsupported,
			"repository review profile activity is not broker-routed",
		)
	}
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
