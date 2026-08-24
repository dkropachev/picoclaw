package repoeval

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	"time"

	"github.com/sipeed/picoclaw/pkg/fileutil"
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
	evaluationStoreLocks         sync.Map
	repositoryEvaluationRandRead = rand.Read
)

type Store struct {
	root  string
	now   func() time.Time
	newID func() (string, error)
}

func NewStore(workspace string) Store {
	return Store{root: filepath.Join(workspace, storeDirectory), now: time.Now, newID: randomEvaluationID}
}

func (s Store) Create(ctx context.Context, request CreateRequest) (Evaluation, error) {
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
		if _, statErr := os.Lstat(s.path(id)); statErr == nil {
			continue
		} else if !os.IsNotExist(statErr) {
			return Evaluation{}, statErr
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
			DefaultFilesPerLanguage: normalized.DefaultFilesPerLanguage,
			FilesPerLanguage:        cloneIntMap(normalized.FilesPerLanguage),
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
	if rootErr := s.requireSafeRoot(true); rootErr != nil {
		return nil, rootErr
	}
	entries, err := os.ReadDir(s.root)
	if os.IsNotExist(err) {
		return []Evaluation{}, nil
	}
	if err != nil {
		return nil, err
	}
	evaluations := make([]Evaluation, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("repository evaluation state %q must not be a symlink", entry.Name())
		}
		if entry.IsDir() || !entry.Type().IsRegular() || !evaluationStateFilename(entry.Name()) {
			continue
		}
		if len(evaluations) >= maximum {
			return nil, errors.New("repository evaluation catalog exceeds its evaluation limit")
		}
		id := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), stateNamePrefix), stateFileSuffix)
		evaluation, loadErr := s.load(id)
		if loadErr != nil {
			return nil, loadErr
		}
		evaluations = append(evaluations, Clone(evaluation))
	}
	sort.Slice(evaluations, func(i, j int) bool {
		if evaluations[i].UpdatedAt.Equal(evaluations[j].UpdatedAt) {
			return evaluations[i].ID < evaluations[j].ID
		}
		return evaluations[i].UpdatedAt.After(evaluations[j].UpdatedAt)
	})
	return evaluations, nil
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
	if err := ctx.Err(); err != nil {
		return err
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
	if err := ctx.Err(); err != nil {
		return err
	}
	return fileutil.RemoveDurable(s.path(id))
}

func (s Store) load(id string) (Evaluation, error) {
	if !validEvaluationID(id) {
		return Evaluation{}, os.ErrNotExist
	}
	if err := s.requireSafeRoot(true); err != nil {
		return Evaluation{}, err
	}
	statePath := s.path(id)
	info, err := os.Lstat(statePath)
	if err != nil {
		return Evaluation{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Evaluation{}, errors.New("repository evaluation state must be a regular file")
	}
	if info.Size() > maxStateFileBytes {
		return Evaluation{}, errors.New("repository evaluation state exceeds its size limit")
	}
	if !repositoryEvaluationPermissionsSafe(info.Mode()) {
		return Evaluation{}, errors.New("repository evaluation state permissions are too broad")
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		return Evaluation{}, err
	}
	var evaluation Evaluation
	if err := json.Unmarshal(data, &evaluation); err != nil {
		return Evaluation{}, err
	}
	if evaluation.ID != id {
		return Evaluation{}, errors.New("repository evaluation state ID mismatch")
	}
	if err := validateEvaluation(evaluation); err != nil {
		return Evaluation{}, err
	}
	return evaluation, nil
}

func (s Store) save(evaluation Evaluation, exclusive bool) error {
	if err := validateEvaluation(evaluation); err != nil {
		return err
	}
	if err := s.ensureSafeRoot(); err != nil {
		return err
	}
	statePath := s.path(evaluation.ID)
	if info, err := os.Lstat(statePath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("repository evaluation state must be a regular file")
		}
		if exclusive {
			return os.ErrExist
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if !exclusive {
		return os.ErrNotExist
	}
	data, err := json.Marshal(evaluation)
	if err != nil {
		return err
	}
	if int64(len(data)) > maxStateFileBytes {
		return errors.New("repository evaluation state exceeds its size limit")
	}
	return fileutil.WriteFileAtomic(statePath, data, 0o600)
}

func (s Store) stateCount() (int, error) {
	if err := s.requireSafeRoot(true); err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(s.root)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return 0, fmt.Errorf("repository evaluation state %q must not be a symlink", entry.Name())
		}
		if !entry.IsDir() && entry.Type().IsRegular() && evaluationStateFilename(entry.Name()) {
			count++
		}
	}
	return count, nil
}

func (s Store) requireSafeRoot(allowMissing bool) error {
	info, err := os.Lstat(s.root)
	if os.IsNotExist(err) && allowMissing {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("repository evaluation storage root must be a real directory")
	}
	if !repositoryEvaluationPermissionsSafe(info.Mode()) {
		return errors.New("repository evaluation storage root permissions are too broad")
	}
	return nil
}

func (s Store) ensureSafeRoot() error {
	if err := s.requireSafeRoot(true); err != nil {
		return err
	}
	if err := fileutil.MkdirAllDurable(s.root, 0o700); err != nil {
		return err
	}
	return s.requireSafeRoot(false)
}

func (s Store) path(id string) string {
	return filepath.Join(s.root, stateNamePrefix+id+stateFileSuffix)
}

func (s Store) lock() (func(), error) {
	value, _ := evaluationStoreLocks.LoadOrStore(s.root, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	unlockFile, err := lockRepositoryEvaluationStore(s.root)
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
		left.DefaultFilesPerLanguage == right.DefaultFilesPerLanguage &&
		maps.Equal(left.FilesPerLanguage, right.FilesPerLanguage)
}

func resetDerivedState(evaluation *Evaluation) {
	evaluation.Status = StatusDraft
	evaluation.Corpus = nil
	evaluation.Progress = Progress{Stage: ProgressIdle, Languages: make(map[string]LanguageProgress)}
	evaluation.Usage = Usage{}
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
