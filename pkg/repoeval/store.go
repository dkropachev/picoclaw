package repoeval

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	storeDirectory    = "repository_evaluations"
	stateFilePrefix   = "rme_"
	stateNamePrefix   = "evaluation_"
	stateFileSuffix   = ".json"
	stateIDRandomSize = 16
	maxStateFileBytes = int64(32 << 20)
)

var (
	evaluationStoreLocks                    sync.Map
	repositoryEvaluationRandRead            = rand.Read
	allowUnfencedEvaluationProviderForTests atomic.Bool
)

type Store struct {
	workspace   string
	root        string
	database    string
	now         func() time.Time
	newID       func() (string, error)
	openForTest func(context.Context) (*sql.DB, error)
	broker      *database.Client
	brokerErr   error
	storeID     database.StoreID
	brokerState *evaluationBrokerClientState
	retained    *retainedEvaluationDatabase
	brokerOwned bool
}

// BulkDeleteItem identifies one explicitly selected evaluation version.
type BulkDeleteItem struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

// BulkDeleteFailure is a safe, item-scoped reason that a selected evaluation
// was retained. Codes are stable API values and never include filesystem data.
type BulkDeleteFailure struct {
	ID   string `json:"id"`
	Code string `json:"code"`
}

// BulkDeleteResult reports the mixed outcome of one catalog-locked deletion.
type BulkDeleteResult struct {
	DeletedIDs []string            `json:"deleted_ids"`
	Failures   []BulkDeleteFailure `json:"failures"`
}

// NewSQLiteStore constructs the durable repository model-evaluation store.
// Schema creation and legacy import happen on the first operation.
func NewSQLiteStore(workspace string) Store {
	if client := evaluationBrokerClient(); client != nil {
		storeID, err := resolveEvaluationBrokerStoreID(context.Background(), client, workspace)
		return Store{
			workspace: workspace, now: time.Now, newID: randomEvaluationID,
			broker: client, storeID: storeID, brokerErr: err,
			brokerState: &evaluationBrokerClientState{},
		}
	}
	if !database.ProviderTestAuthorityHeld() && !allowUnfencedEvaluationProviderForTests.Load() {
		return Store{
			workspace: workspace,
			now:       time.Now,
			newID:     randomEvaluationID,
			brokerErr: database.NewError(
				database.CodeUnavailable,
				"repository evaluation database broker client is unavailable",
			),
		}
	}
	return newSQLiteStoreLocal(workspace)
}

func newSQLiteStoreLocal(workspace string) Store {
	if !evaluationLocalProviderAuthorized() {
		return Store{
			workspace: workspace,
			now:       time.Now,
			newID:     randomEvaluationID,
			brokerErr: database.NewError(
				database.CodeUnauthorized,
				"repository evaluation provider access requires database owner fencing",
			),
		}
	}
	root := filepath.Join(workspace, storeDirectory)
	return Store{
		workspace: workspace,
		root:      root,
		database:  filepath.Join(root, evaluationDatabaseFilename),
		now:       time.Now,
		newID:     randomEvaluationID,
		storeID:   EvaluationStoreID,
	}
}

func evaluationLocalProviderAuthorized() bool {
	return database.BrokerAuthorityHeld() || database.MigrationFenceHeld() ||
		database.ProviderTestAuthorityHeld() || allowUnfencedEvaluationProviderForTests.Load()
}

func (s Store) localProviderError() error {
	if s.brokerErr != nil {
		return s.brokerErr
	}
	return evaluationProviderAuthorityError()
}

func evaluationProviderAuthorityError() error {
	if !evaluationLocalProviderAuthorized() {
		return database.NewError(
			database.CodeUnauthorized,
			"repository evaluation provider access requires database owner fencing",
		)
	}
	return nil
}

// NewStore is retained for source compatibility and no longer writes JSON.
// Deprecated: use NewSQLiteStore.
func NewStore(workspace string) Store { return NewSQLiteStore(workspace) }

func (s Store) Create(ctx context.Context, request CreateRequest) (Evaluation, error) {
	if s.broker != nil {
		return s.brokerCreate(ctx, request)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Evaluation{}, ctxErr
	}
	normalized, err := normalizeCreate(request)
	if err != nil || validateCreate(normalized) != nil {
		return Evaluation{}, errors.Join(ErrInvalidEvaluation, err)
	}
	if normalized.OneShot != (normalized.InitialRunID != "") ||
		normalized.InitialRunID != "" && !validText(normalized.InitialRunID, maxRunIDBytes, false) {
		return Evaluation{}, ErrInvalidEvaluation
	}
	unlock, err := s.lock()
	if err != nil {
		return Evaluation{}, err
	}
	defer unlock()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Evaluation{}, ctxErr
	}
	count, err := s.stateCount()
	if err != nil {
		return Evaluation{}, err
	}
	if count >= maxEvaluations {
		return Evaluation{}, errors.New("repository evaluation catalog exceeds its evaluation limit")
	}
	now := s.clock()
	for attempt := 0; attempt < 4; attempt++ {
		id, idErr := s.idGenerator()()
		if idErr != nil {
			return Evaluation{}, fmt.Errorf("generate repository evaluation ID: %w", idErr)
		}
		if !validEvaluationID(id) {
			return Evaluation{}, errors.New("repository evaluation ID generator returned an invalid ID")
		}
		exists, existsErr := s.exists(ctx, id)
		if existsErr != nil {
			return Evaluation{}, existsErr
		}
		if exists {
			continue
		}
		status := StatusDraft
		progress := Progress{Stage: ProgressIdle, Languages: make(map[string]LanguageProgress), UpdatedAt: now}
		runIDs := []string{}
		var startedAt *time.Time
		if normalized.OneShot {
			status = StatusPreflighting
			progress.Stage = ProgressResolving
			progress.Message = "Resolving the exact repository commit."
			progress.Percent = 1
			runIDs = []string{normalized.InitialRunID}
			startedAt = timePointer(now)
		}
		evaluation := Evaluation{
			SchemaVersion: SchemaVersion,
			ID:            id, Version: 1, Status: status, OneShot: normalized.OneShot,
			Repository: normalized.Repository, Ref: normalized.Ref,
			CandidateModels:         append([]string(nil), normalized.CandidateModels...),
			SelectorModelAlias:      normalized.SelectorModelAlias,
			JudgeModelAlias:         normalized.JudgeModelAlias,
			Focus:                   normalized.Focus,
			Profile:                 cloneProfileSnapshot(normalized.Profile),
			DefaultFilesPerLanguage: normalized.DefaultFilesPerLanguage,
			FilesPerLanguage:        cloneIntMap(normalized.FilesPerLanguage),
			WorkSizingPlan:          append([]WorkSizingPoint(nil), normalized.WorkSizingPlan...),
			ModelStats:              make(map[string]ModelStats),
			Comparisons:             []ModelComparison{}, Warnings: []string{}, RunIDs: runIDs,
			CreatedAt: now, UpdatedAt: now, StartedAt: startedAt,
			Progress: progress,
		}
		evaluation, err = normalizeEvaluation(evaluation)
		if err != nil || validateEvaluation(evaluation) != nil {
			return Evaluation{}, errors.Join(ErrInvalidEvaluation, err)
		}
		if err := s.save(evaluation, true); err != nil {
			return Evaluation{}, err
		}
		return Clone(evaluation), nil
	}
	return Evaluation{}, errors.New("could not allocate a unique repository evaluation ID")
}

func (s Store) Get(ctx context.Context, id string) (Evaluation, bool, error) {
	if s.broker != nil {
		return s.brokerGet(ctx, id)
	}
	if err := ctx.Err(); err != nil {
		return Evaluation{}, false, err
	}
	if !validEvaluationID(strings.TrimSpace(id)) {
		return Evaluation{}, false, nil
	}
	unlock, err := s.lock()
	if err != nil {
		return Evaluation{}, false, err
	}
	defer unlock()
	evaluation, err := s.load(strings.TrimSpace(id))
	if os.IsNotExist(err) {
		return Evaluation{}, false, nil
	}
	if err != nil {
		return Evaluation{}, false, err
	}
	return Clone(evaluation), true, nil
}

func (s Store) List(ctx context.Context) ([]Evaluation, error) {
	if s.broker != nil {
		return s.brokerList(ctx)
	}
	return s.list(ctx, maxEvaluations)
}

func (s Store) list(ctx context.Context, maximum int) ([]Evaluation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	database, release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	rows, err := database.QueryContext(ctx, `
		SELECT evaluation_id
		  FROM repository_evaluations
	 ORDER BY updated_at_unix_nano DESC, evaluation_id ASC
	 LIMIT ?`, maximum+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	evaluations := make([]Evaluation, 0)
	for rows.Next() {
		if len(evaluations) >= maximum {
			return nil, errors.New("repository evaluation catalog exceeds its evaluation limit")
		}
		var id string
		if scanErr := rows.Scan(&id); scanErr != nil {
			return nil, scanErr
		}
		evaluation, loadErr := loadEvaluation(ctx, database, id)
		if loadErr != nil {
			return nil, loadErr
		}
		evaluations = append(evaluations, Clone(evaluation))
	}
	return evaluations, rows.Err()
}

// Update applies a mutation to a clone of the durable state. The mutation is
// committed only if expectedVersion still matches and the resulting lifecycle
// transition and bounded schema are valid.
func (s Store) Update(
	ctx context.Context,
	id string,
	expectedVersion int64,
	mutate func(*Evaluation) error,
) (Evaluation, error) {
	if s.broker != nil {
		return s.brokerUpdate(ctx, id, expectedVersion, mutate)
	}
	if err := ctx.Err(); err != nil {
		return Evaluation{}, err
	}
	id = strings.TrimSpace(id)
	if !validEvaluationID(id) || mutate == nil {
		return Evaluation{}, ErrInvalidEvaluation
	}
	unlock, err := s.lock()
	if err != nil {
		return Evaluation{}, err
	}
	defer unlock()
	original, err := s.load(id)
	if err != nil {
		return Evaluation{}, err
	}
	if expectedVersion < 1 || original.Version != expectedVersion {
		return Evaluation{}, ErrConflict
	}
	candidate := Clone(original)
	if mutationErr := mutate(&candidate); mutationErr != nil {
		return Evaluation{}, mutationErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Evaluation{}, ctxErr
	}
	if candidate.SchemaVersion != original.SchemaVersion || candidate.ID != original.ID ||
		candidate.Version != original.Version || !candidate.CreatedAt.Equal(original.CreatedAt) ||
		original.OneShot && !candidate.OneShot {
		return Evaluation{}, ErrInvalidEvaluation
	}
	// Store-owned timestamps are derived from the durable transition. A caller
	// cannot forge them through the mutation callback.
	candidate.UpdatedAt = original.UpdatedAt
	candidate.StartedAt = cloneTime(original.StartedAt)
	candidate.FinishedAt = cloneTime(original.FinishedAt)
	progressChanged := !progressPayloadEqual(candidate.Progress, original.Progress)
	if progressChanged {
		candidate.Progress.UpdatedAt = original.Progress.UpdatedAt
	}
	candidate, err = normalizeEvaluation(candidate)
	if err != nil {
		return Evaluation{}, err
	}
	if !candidate.Status.Valid() {
		return Evaluation{}, ErrInvalidEvaluation
	}
	configurationChanged := !sameConfiguration(original, candidate)
	terminalResume := original.Status == StatusFailed &&
		(candidate.Status == StatusPreflighting || candidate.Status == StatusRunning) &&
		!configurationChanged
	if original.Status.Terminal() && !terminalResume && !reflect.DeepEqual(candidate, original) {
		return Evaluation{}, ErrConflict
	}
	resetToDraft := false
	if configurationChanged {
		switch original.Status {
		case StatusDraft:
			resetDerivedState(&candidate)
			resetToDraft = true
			progressChanged = true
		default:
			return Evaluation{}, ErrConflict
		}
	}
	if !resetToDraft && !original.Status.CanTransitionTo(candidate.Status) {
		return Evaluation{}, ErrInvalidTransition
	}
	if terminalResume {
		if candidate.Status == StatusPreflighting && candidate.Corpus != nil ||
			candidate.Status == StatusRunning && candidate.Corpus == nil || candidate.Failure != "" {
			return Evaluation{}, ErrInvalidEvaluation
		}
		candidate.FinishedAt = nil
	}
	if !resetToDraft && original.Status != StatusDraft && original.Status != StatusPreflighting &&
		!reflect.DeepEqual(original.Corpus, candidate.Corpus) {
		return Evaluation{}, ErrConflict
	}
	if reflect.DeepEqual(candidate, original) {
		return Clone(original), nil
	}
	now := s.clock()
	if candidate.Status != StatusDraft && candidate.Status != StatusCanceled && candidate.StartedAt == nil {
		candidate.StartedAt = timePointer(now)
	}
	if candidate.Status.Terminal() && candidate.FinishedAt == nil {
		candidate.FinishedAt = timePointer(now)
	}
	if progressChanged {
		candidate.Progress.UpdatedAt = now
	}
	candidate.Version++
	candidate.UpdatedAt = now
	if err := validateEvaluation(candidate); err != nil {
		return Evaluation{}, err
	}
	if err := s.save(candidate, false); err != nil {
		return Evaluation{}, err
	}
	return Clone(candidate), nil
}

func (s Store) Delete(ctx context.Context, id string, expectedVersion int64) error {
	if s.broker != nil {
		return s.brokerDelete(ctx, id, expectedVersion)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	id = strings.TrimSpace(id)
	if !validEvaluationID(id) {
		return os.ErrNotExist
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	evaluation, err := s.load(id)
	if err != nil {
		return err
	}
	if expectedVersion < 1 || evaluation.Version != expectedVersion {
		return ErrConflict
	}
	if evaluation.Status != StatusDraft {
		return ErrInvalidTransition
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	database, release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return sqlitestore.Immediate(ctx, database, func(conn *sql.Conn) error {
		result, deleteErr := conn.ExecContext(ctx, `
			DELETE FROM repository_evaluations
			 WHERE evaluation_id = ? AND version = ? AND status = ?`,
			id, expectedVersion, string(StatusDraft))
		if deleteErr != nil {
			return deleteErr
		}
		return sqlitestore.RequireOneRow(result, ErrConflict)
	})
}

// BulkDelete holds the catalog lock while it classifies and removes at most
// 200 explicitly selected evaluations. Only version-matching drafts are
// removed; every other item remains durable with a stable failure code.
func (s Store) BulkDelete(ctx context.Context, items []BulkDeleteItem) (BulkDeleteResult, error) {
	if s.broker != nil {
		return s.brokerBulkDelete(ctx, items)
	}
	if err := ctx.Err(); err != nil {
		return BulkDeleteResult{}, err
	}
	if len(items) == 0 || len(items) > 200 {
		return BulkDeleteResult{}, ErrInvalidEvaluation
	}
	unlock, err := s.lock()
	if err != nil {
		return BulkDeleteResult{}, err
	}
	defer unlock()
	database, release, err := s.acquire(ctx)
	if err != nil {
		return BulkDeleteResult{}, err
	}
	defer release()

	counts := make(map[string]int, len(items))
	versions := make(map[string]int64, len(items))
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		counts[id]++
		versions[id] = item.Version
	}
	ids := make([]string, 0, len(counts))
	for id := range counts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := BulkDeleteResult{
		DeletedIDs: []string{},
		Failures:   []BulkDeleteFailure{},
	}
	deletable := make([]string, 0, len(ids))
	for _, id := range ids {
		if contextErr := ctx.Err(); contextErr != nil {
			return BulkDeleteResult{}, contextErr
		}
		if id == "" || !validEvaluationID(id) {
			result.Failures = append(result.Failures, BulkDeleteFailure{ID: id, Code: "invalid_id"})
			continue
		}
		if counts[id] != 1 {
			result.Failures = append(result.Failures, BulkDeleteFailure{ID: id, Code: "duplicate_id"})
			continue
		}
		if versions[id] < 1 {
			result.Failures = append(result.Failures, BulkDeleteFailure{ID: id, Code: "invalid_version"})
			continue
		}
		evaluation, loadErr := loadEvaluation(ctx, database, id)
		if errors.Is(loadErr, sql.ErrNoRows) {
			result.Failures = append(result.Failures, BulkDeleteFailure{ID: id, Code: "not_found"})
			continue
		}
		if loadErr != nil {
			return result, loadErr
		}
		if evaluation.Version != versions[id] {
			result.Failures = append(result.Failures, BulkDeleteFailure{ID: id, Code: "stale_version"})
			continue
		}
		if evaluation.Status != StatusDraft {
			result.Failures = append(result.Failures, BulkDeleteFailure{ID: id, Code: "not_draft"})
			continue
		}
		deletable = append(deletable, id)
	}
	// Cancellation before the deletion phase leaves the catalog unchanged.
	// Once removal starts, finish classifying every selected draft so callers
	// never receive a cancellation error after an unreported partial mutation.
	if contextErr := ctx.Err(); contextErr != nil {
		return BulkDeleteResult{}, contextErr
	}
	err = sqlitestore.Immediate(ctx, database, func(conn *sql.Conn) error {
		for _, id := range deletable {
			deleted, deleteErr := conn.ExecContext(ctx, `
				DELETE FROM repository_evaluations
				 WHERE evaluation_id = ? AND version = ? AND status = ?`,
				id, versions[id], string(StatusDraft))
			if deleteErr != nil {
				return deleteErr
			}
			if deleteErr := sqlitestore.RequireOneRow(deleted, ErrConflict); deleteErr != nil {
				return deleteErr
			}
			result.DeletedIDs = append(result.DeletedIDs, id)
		}
		return nil
	})
	if err != nil {
		return BulkDeleteResult{}, err
	}
	return result, nil
}

func (s Store) load(id string) (Evaluation, error) {
	if !validEvaluationID(id) {
		return Evaluation{}, os.ErrNotExist
	}
	database, release, err := s.acquire(context.Background())
	if err != nil {
		return Evaluation{}, err
	}
	defer release()
	evaluation, err := loadEvaluation(context.Background(), database, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Evaluation{}, os.ErrNotExist
	}
	return evaluation, err
}

func (s Store) save(evaluation Evaluation, exclusive bool) error {
	if err := validateEvaluation(evaluation); err != nil {
		return err
	}
	database, release, err := s.acquire(context.Background())
	if err != nil {
		return err
	}
	defer release()
	return saveEvaluation(context.Background(), database, evaluation, exclusive)
}

func (s Store) stateCount() (int, error) {
	database, release, err := s.acquire(context.Background())
	if err != nil {
		return 0, err
	}
	defer release()
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM repository_evaluations`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s Store) lock() (func(), error) {
	if err := s.localProviderError(); err != nil {
		return nil, err
	}
	value, _ := evaluationStoreLocks.LoadOrStore(s.root, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	if s.brokerOwned {
		if !mutex.TryLock() {
			return nil, ErrConflict
		}
	} else {
		mutex.Lock()
	}
	var unlockFile func()
	var err error
	if s.brokerOwned {
		unlockFile, err = tryLockRepositoryEvaluationStore(s.root)
	} else {
		unlockFile, err = lockRepositoryEvaluationStore(s.root)
	}
	if err != nil {
		mutex.Unlock()
		return nil, err
	}
	return func() {
		unlockFile()
		mutex.Unlock()
	}, nil
}

func (s Store) clock() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

func (s Store) idGenerator() func() (string, error) {
	if s.newID == nil {
		return randomEvaluationID
	}
	return s.newID
}

func (s Store) StoreID() database.StoreID {
	if s.brokerErr != nil {
		return ""
	}
	if s.broker != nil {
		return s.storeID
	}
	if s.storeID != "" {
		return s.storeID
	}
	return EvaluationStoreID
}

func randomEvaluationID() (string, error) {
	random := make([]byte, stateIDRandomSize)
	if _, err := repositoryEvaluationRandRead(random); err != nil {
		return "", err
	}
	return stateFilePrefix + hex.EncodeToString(random), nil
}

func validEvaluationID(id string) bool {
	digest, ok := strings.CutPrefix(id, stateFilePrefix)
	if !ok || len(digest) != stateIDRandomSize*2 {
		return false
	}
	for _, character := range digest {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func evaluationStateFilename(name string) bool {
	if !strings.HasPrefix(name, stateNamePrefix) || !strings.HasSuffix(name, stateFileSuffix) {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, stateNamePrefix), stateFileSuffix)
	return validEvaluationID(id)
}

func sameConfiguration(left, right Evaluation) bool {
	return left.Repository == right.Repository && left.Ref == right.Ref &&
		slices.Equal(left.CandidateModels, right.CandidateModels) &&
		left.SelectorModelAlias == right.SelectorModelAlias && left.JudgeModelAlias == right.JudgeModelAlias &&
		slices.Equal(left.Focus.CodeTypes, right.Focus.CodeTypes) &&
		slices.Equal(left.Focus.IncludeFolders, right.Focus.IncludeFolders) &&
		slices.Equal(left.Focus.ExcludeFolders, right.Focus.ExcludeFolders) &&
		left.Focus.FreeText == right.Focus.FreeText &&
		sameProfileSnapshot(left.Profile, right.Profile) &&
		left.DefaultFilesPerLanguage == right.DefaultFilesPerLanguage &&
		maps.Equal(left.FilesPerLanguage, right.FilesPerLanguage) &&
		slices.Equal(left.WorkSizingPlan, right.WorkSizingPlan)
}

func sameProfileSnapshot(left, right *ProfileSnapshot) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.ID == right.ID && left.Version == right.Version && left.Name == right.Name &&
		left.ReviewerModel == right.ReviewerModel && left.AccountRef == right.AccountRef &&
		left.ReviewFocus == right.ReviewFocus &&
		slices.Equal(left.Focus.CodeTypes, right.Focus.CodeTypes) &&
		slices.Equal(left.Focus.IncludeFolders, right.Focus.IncludeFolders) &&
		slices.Equal(left.Focus.ExcludeFolders, right.Focus.ExcludeFolders) &&
		left.Focus.FreeText == right.Focus.FreeText &&
		left.MaxFilesPerBatch == right.MaxFilesPerBatch &&
		left.MaxContentBytesPerBatch == right.MaxContentBytesPerBatch &&
		left.MaxParallelChildren == right.MaxParallelChildren
}

func resetDerivedState(evaluation *Evaluation) {
	evaluation.Status = StatusDraft
	evaluation.Corpus = nil
	evaluation.Progress = Progress{Stage: ProgressIdle, Languages: make(map[string]LanguageProgress)}
	evaluation.Usage = Usage{}
	if len(evaluation.WorkSizingPlan) > 0 {
		evaluation.WorkSizingUsage = make(map[string]map[string]Usage)
		evaluation.WorkSizingConcreteModels = make(map[string]map[string]map[string]int)
		evaluation.WorkSizingResults = []WorkSizingModelResult{}
	} else {
		evaluation.WorkSizingUsage = nil
		evaluation.WorkSizingConcreteModels = nil
		evaluation.WorkSizingResults = nil
	}
	evaluation.ModelStats = make(map[string]ModelStats)
	evaluation.Checkpoint = Checkpoint{}
	evaluation.Comparisons = []ModelComparison{}
	evaluation.Warnings = []string{}
	evaluation.RunIDs = []string{}
	evaluation.Failure = ""
	evaluation.StartedAt = nil
	evaluation.FinishedAt = nil
}

func progressPayloadEqual(left, right Progress) bool {
	left.UpdatedAt = time.Time{}
	right.UpdatedAt = time.Time{}
	return reflect.DeepEqual(left, right)
}

func timePointer(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}
