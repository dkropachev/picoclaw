package repoeval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var evaluationTestNow = time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

func TestStoreLifecyclePersistsPinnedEvaluation(t *testing.T) {
	store := newEvaluationTestStore(t, 1)
	created, err := store.Create(context.Background(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != StatusDraft || created.Version != 1 || created.DefaultFilesPerLanguage != 20 ||
		created.Repository != "owner/repo" || created.Ref != "main" ||
		!reflect.DeepEqual(created.Focus.IncludeFolders, []string{"pkg", "web/frontend"}) {
		t.Fatalf("unexpected created evaluation: %#v", created)
	}
	if filepath.Base(store.databasePath()) != evaluationDatabaseFilename ||
		!strings.HasPrefix(created.ID, "rme_") {
		t.Fatalf("unexpected evaluation identity/path: %q %q", created.ID, store.databasePath())
	}
	info, err := os.Stat(store.databasePath())
	if err != nil {
		t.Fatal(err)
	}
	if !repositoryEvaluationPermissionsSafe(0o644) && info.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %o, want 600", info.Mode().Perm())
	}

	created.CandidateModels[0] = "caller-mutation"
	created.FilesPerLanguage["Go"] = 19
	loaded, found, err := store.Get(context.Background(), created.ID)
	if err != nil || !found || loaded.CandidateModels[0] != "model-a" || loaded.FilesPerLanguage["Go"] != 3 {
		t.Fatalf("Get returned state affected by caller: %#v found=%v err=%v", loaded, found, err)
	}

	configured := updateEvaluation(t, store, loaded, func(value *Evaluation) {
		value.Focus.FreeText = "prioritize concurrency"
	})
	preflighting := updateEvaluation(t, store, configured, func(value *Evaluation) {
		value.Status = StatusPreflighting
		value.RunIDs = append(value.RunIDs, "selector-run")
	})
	if preflighting.StartedAt == nil || preflighting.RestartDirective() != RecoveryResume ||
		preflighting.LatestRunID() != "selector-run" {
		t.Fatalf("preflight recovery state = %#v", preflighting)
	}

	ready := updateEvaluation(t, store, preflighting, func(value *Evaluation) {
		value.Status = StatusReady
		value.Corpus = validManifest()
		value.Progress = Progress{TotalFiles: 100, SelectedFiles: 2, TotalTasks: 4, Percent: 25}
	})
	if ready.Corpus == nil || ready.Corpus.RegionCountForTest() != 2 || ready.Progress.UpdatedAt.IsZero() {
		t.Fatalf("ready evaluation missing corpus/progress: %#v", ready)
	}

	running := updateEvaluation(t, store, ready, func(value *Evaluation) {
		value.Status = StatusRunning
		value.Progress.CompletedFiles = 1
		value.Progress.CompletedTasks = 2
		value.Progress.CurrentModel = "model-a"
		value.Progress.CurrentPath = "pkg/service.go"
		value.Progress.Percent = 50
		value.Usage = Usage{
			Requests:         2,
			InputTokens:      100,
			OutputTokens:     20,
			DurationMillis:   50,
			EstimatedCostUSD: floatPointer(.01),
		}
		value.ModelStats["model-a"] = ModelStats{FilesSelected: 2, FilesCompleted: 1, Attempts: 1, Successes: 1}
		value.RunIDs = append(value.RunIDs, "candidate-run-a")
	})
	judging := updateEvaluation(t, store, running, func(value *Evaluation) { value.Status = StatusJudging })
	analyzing := updateEvaluation(t, store, judging, func(value *Evaluation) { value.Status = StatusAnalyzing })
	completed := updateEvaluation(t, store, analyzing, func(value *Evaluation) {
		value.Status = StatusCompleted
		value.Progress.CompletedFiles = 2
		value.Progress.CompletedTasks = 4
		value.Progress.CurrentModel = ""
		value.Progress.CurrentPath = ""
		value.Progress.Percent = 100
		value.Comparisons = validComparisons()
	})
	if completed.FinishedAt == nil || !completed.Status.Terminal() || completed.RestartDirective() != RecoveryNone ||
		len(completed.Comparisons) != 2 || completed.Comparisons[0].ConfirmedFindings != 3 {
		t.Fatalf("completed evaluation = %#v", completed)
	}

	listed, err := store.List(context.Background())
	if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0], completed) {
		t.Fatalf("List = %#v, err=%v", listed, err)
	}
	listed[0].Corpus.Files[0].Chunks[0].ID = "mutated"
	again, _, err := store.Get(context.Background(), completed.ID)
	if err != nil || again.Corpus.Files[0].Chunks[0].ID != "chunk-1" {
		t.Fatalf("List leaked mutable state: %#v err=%v", again.Corpus, err)
	}
}

func TestStoreCreatesOneShotPreflightAtomically(t *testing.T) {
	store := newEvaluationTestStore(t, 61)
	request := validCreateRequest()
	request.OneShot = true
	request.InitialRunID = "wr_initial_one_shot"
	created, err := store.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !created.OneShot || created.Status != StatusPreflighting ||
		created.Progress.Stage != ProgressResolving || created.Progress.Percent != 1 ||
		created.LatestRunID() != request.InitialRunID || created.StartedAt == nil {
		t.Fatalf("atomic one-shot create=%#v", created)
	}
	loaded, found, err := store.Get(t.Context(), created.ID)
	if err != nil || !found || !reflect.DeepEqual(loaded, created) {
		t.Fatalf("atomic one-shot persisted=%#v found=%v err=%v", loaded, found, err)
	}

	for name, alter := range map[string]func(*CreateRequest){
		"missing run identity":          func(value *CreateRequest) { value.InitialRunID = "" },
		"run identity without one-shot": func(value *CreateRequest) { value.OneShot = false },
		"oversized run identity": func(value *CreateRequest) {
			value.InitialRunID = strings.Repeat("r", maxRunIDBytes+1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			invalid := request
			alter(&invalid)
			if _, createErr := store.Create(t.Context(), invalid); !errors.Is(
				createErr,
				ErrInvalidEvaluation,
			) {
				t.Fatalf("invalid one-shot create error=%v", createErr)
			}
		})
	}
}

func TestStoreBulkDeleteHoldsMixedVersionedDraftSemantics(t *testing.T) {
	store := newEvaluationTestStore(t, 62)
	create := func() Evaluation {
		evaluation, err := store.Create(t.Context(), validCreateRequest())
		if err != nil {
			t.Fatal(err)
		}
		return evaluation
	}
	deletedDraft := create()
	duplicateDraft := create()
	staleDraft := create()
	invalidVersionDraft := create()
	active := create()
	active = updateEvaluation(t, store, active, func(value *Evaluation) {
		value.Status = StatusPreflighting
		value.RunIDs = append(value.RunIDs, "run-active")
	})

	result, err := store.BulkDelete(t.Context(), []BulkDeleteItem{
		{ID: deletedDraft.ID, Version: deletedDraft.Version},
		{ID: duplicateDraft.ID, Version: duplicateDraft.Version},
		{ID: duplicateDraft.ID, Version: duplicateDraft.Version},
		{ID: staleDraft.ID, Version: staleDraft.Version + 1},
		{ID: invalidVersionDraft.ID, Version: 0},
		{ID: active.ID, Version: active.Version},
		{ID: "rme_ffffffffffffffffffffffffffffffff", Version: 1},
		{ID: "invalid", Version: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.DeletedIDs) != 1 || result.DeletedIDs[0] != deletedDraft.ID ||
		len(result.Failures) != 6 {
		t.Fatalf("BulkDelete() = %#v", result)
	}
	wantCodes := map[string]string{
		duplicateDraft.ID:                      "duplicate_id",
		staleDraft.ID:                          "stale_version",
		invalidVersionDraft.ID:                 "invalid_version",
		active.ID:                              "not_draft",
		"rme_ffffffffffffffffffffffffffffffff": "not_found",
		"invalid":                              "invalid_id",
	}
	for _, failure := range result.Failures {
		if wantCodes[failure.ID] != failure.Code {
			t.Fatalf("failure = %#v, want code %q", failure, wantCodes[failure.ID])
		}
	}
	if _, found, getErr := store.Get(t.Context(), deletedDraft.ID); getErr != nil || found {
		t.Fatalf("deleted draft found=%v err=%v", found, getErr)
	}
	for _, retained := range []Evaluation{duplicateDraft, staleDraft, invalidVersionDraft, active} {
		if _, found, getErr := store.Get(t.Context(), retained.ID); getErr != nil || !found {
			t.Fatalf("retained %s found=%v err=%v", retained.ID, found, getErr)
		}
	}
}

func TestStoreBulkDeleteCancellationBeforeDeletionRetainsDraft(t *testing.T) {
	store := newEvaluationTestStore(t, 63)
	draft, err := store.Create(t.Context(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := store.lock()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, bulkErr := store.BulkDelete(ctx, []BulkDeleteItem{{ID: draft.ID, Version: draft.Version}})
		done <- bulkErr
	}()
	<-started
	cancel()
	unlock()
	select {
	case bulkErr := <-done:
		if !errors.Is(bulkErr, context.Canceled) {
			t.Fatalf("BulkDelete() error=%v, want context cancellation", bulkErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("BulkDelete did not return after catalog lock release")
	}
	if _, found, getErr := store.Get(t.Context(), draft.ID); getErr != nil || !found {
		t.Fatalf("draft after canceled bulk found=%v err=%v", found, getErr)
	}
}

func TestStoreCASNoopAndImmutableInputs(t *testing.T) {
	store := newEvaluationTestStore(t, 2)
	created, err := store.Create(context.Background(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	noOp, err := store.Update(context.Background(), created.ID, created.Version, func(*Evaluation) error { return nil })
	if err != nil || noOp.Version != created.Version || !noOp.UpdatedAt.Equal(created.UpdatedAt) {
		t.Fatalf("no-op Update = %#v err=%v", noOp, err)
	}
	_, err = store.Update(context.Background(), created.ID, created.Version+1, func(*Evaluation) error { return nil })
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Update error = %v", err)
	}
	callbackErr := errors.New("stop")
	_, err = store.Update(
		context.Background(),
		created.ID,
		created.Version,
		func(*Evaluation) error { return callbackErr },
	)
	if !errors.Is(err, callbackErr) {
		t.Fatalf("callback error = %v", err)
	}
	for name, mutate := range map[string]func(*Evaluation){
		"schema":  func(value *Evaluation) { value.SchemaVersion++ },
		"id":      func(value *Evaluation) { value.ID = testEvaluationID(99) },
		"version": func(value *Evaluation) { value.Version++ },
		"created": func(value *Evaluation) { value.CreatedAt = value.CreatedAt.Add(time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			_, updateErr := store.Update(
				context.Background(),
				created.ID,
				created.Version,
				func(value *Evaluation) error {
					mutate(value)
					return nil
				},
			)
			if !errors.Is(updateErr, ErrInvalidEvaluation) {
				t.Fatalf("error = %v, want ErrInvalidEvaluation", updateErr)
			}
		})
	}
	preflight := updateEvaluation(t, store, created, func(value *Evaluation) { value.Status = StatusPreflighting })
	_, err = store.Update(context.Background(), preflight.ID, preflight.Version, func(value *Evaluation) error {
		value.CandidateModels[0] = "changed"
		return nil
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("preflight config edit error = %v", err)
	}
	draft, err := store.Create(context.Background(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(context.Background(), draft.ID, draft.Version, func(value *Evaluation) error {
		value.Status = StatusRunning
		return nil
	})
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("skipped transition error = %v", err)
	}
	preflight = updateEvaluation(t, store, draft, func(value *Evaluation) { value.Status = StatusPreflighting })
	ready := updateEvaluation(t, store, preflight, func(value *Evaluation) {
		value.Status = StatusReady
		value.Corpus = validManifest()
	})
	_, err = store.Update(context.Background(), ready.ID, ready.Version, func(value *Evaluation) error {
		value.Corpus.PolicyHash = "changed-policy"
		return nil
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("repinned corpus error = %v", err)
	}
	_, err = store.Update(context.Background(), ready.ID, ready.Version, func(value *Evaluation) error {
		value.Repository = "other/repo"
		value.Ref = "release"
		return nil
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("ready config edit error = %v", err)
	}
	running := updateEvaluation(t, store, ready, func(value *Evaluation) { value.Status = StatusRunning })
	_, err = store.Update(context.Background(), running.ID, running.Version, func(value *Evaluation) error {
		value.JudgeModelAlias = "other-judge"
		return nil
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("running config edit error = %v", err)
	}
}

func TestStoreCancelFailureAndTerminalTransitions(t *testing.T) {
	store := newEvaluationTestStore(t, 3)
	created, err := store.Create(context.Background(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	canceled := updateEvaluation(t, store, created, func(value *Evaluation) { value.Status = StatusCanceled })
	if canceled.StartedAt != nil || canceled.FinishedAt == nil {
		t.Fatalf("direct canceled timestamps = %#v", canceled)
	}
	_, err = store.Update(context.Background(), canceled.ID, canceled.Version, func(value *Evaluation) error {
		value.Status = StatusDraft
		return nil
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal transition error = %v", err)
	}

	failedStore := newEvaluationTestStore(t, 4)
	failedDraft, err := failedStore.Create(context.Background(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	_, err = failedStore.Update(
		context.Background(),
		failedDraft.ID,
		failedDraft.Version,
		func(value *Evaluation) error {
			value.Status = StatusFailed
			return nil
		},
	)
	if !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("failure without detail error = %v", err)
	}
	failed := updateEvaluation(t, failedStore, failedDraft, func(value *Evaluation) {
		value.Status = StatusFailed
		value.Failure = "selector unavailable"
	})
	if failed.StartedAt == nil || failed.FinishedAt == nil || failed.Failure == "" {
		t.Fatalf("failed evaluation = %#v", failed)
	}

	cancelStore := newEvaluationTestStore(t, 5)
	cancelDraft, _ := cancelStore.Create(context.Background(), validCreateRequest())
	preflight := updateEvaluation(
		t,
		cancelStore,
		cancelDraft,
		func(value *Evaluation) { value.Status = StatusPreflighting },
	)
	canceling := updateEvaluation(t, cancelStore, preflight, func(value *Evaluation) { value.Status = StatusCanceling })
	if canceling.RestartDirective() != RecoveryFinishCancel {
		t.Fatalf("canceling recovery = %q", canceling.RestartDirective())
	}
	finished := updateEvaluation(t, cancelStore, canceling, func(value *Evaluation) { value.Status = StatusCanceled })
	if finished.FinishedAt == nil {
		t.Fatal("canceled evaluation has no finished timestamp")
	}
}

func TestStoreDeleteAndMissingBehavior(t *testing.T) {
	store := newEvaluationTestStore(t, 6)
	missing, found, err := store.Get(context.Background(), "bad/../../id")
	if err != nil || found || missing.ID != "" {
		t.Fatalf("invalid Get = %#v found=%v err=%v", missing, found, err)
	}
	missing, found, err = store.Get(context.Background(), testEvaluationID(77))
	if err != nil || found || missing.ID != "" {
		t.Fatalf("missing Get = %#v found=%v err=%v", missing, found, err)
	}
	created, err := store.Create(context.Background(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	if deleteErr := store.Delete(
		context.Background(),
		created.ID,
		created.Version+1,
	); !errors.Is(deleteErr, ErrConflict) {
		t.Fatalf("stale Delete error = %v", deleteErr)
	}
	if deleteErr := store.Delete(context.Background(), "invalid", 1); !errors.Is(deleteErr, os.ErrNotExist) {
		t.Fatalf("invalid Delete error = %v", deleteErr)
	}
	started := updateEvaluation(t, store, created, func(value *Evaluation) {
		value.Status = StatusPreflighting
	})
	if deleteErr := store.Delete(context.Background(), started.ID, started.Version); !errors.Is(
		deleteErr,
		ErrInvalidTransition,
	) {
		t.Fatalf("started Delete error = %v", deleteErr)
	}
	deletable, err := store.Create(context.Background(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	if deleteErr := store.Delete(context.Background(), deletable.ID, deletable.Version); deleteErr != nil {
		t.Fatal(deleteErr)
	}
	if _, found, getErr := store.Get(context.Background(), deletable.ID); getErr != nil || found {
		t.Fatalf("deleted Get found=%v err=%v", found, getErr)
	}
	if deleteErr := store.Delete(
		context.Background(),
		deletable.ID,
		deletable.Version,
	); !errors.Is(deleteErr, os.ErrNotExist) {
		t.Fatalf("repeated Delete error = %v", deleteErr)
	}
}

func TestStoreContextsAndConcurrentCAS(t *testing.T) {
	store := newEvaluationTestStore(t, 7)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Create(canceled, validCreateRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Create error = %v", err)
	}
	if _, _, err := store.Get(canceled, testEvaluationID(1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Get error = %v", err)
	}
	if _, err := store.List(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled List error = %v", err)
	}
	if err := store.Delete(canceled, testEvaluationID(1), 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Delete error = %v", err)
	}
	created, err := store.Create(context.Background(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var conflicts atomic.Int32
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, updateErr := store.Update(
				context.Background(),
				created.ID,
				created.Version,
				func(value *Evaluation) error {
					value.Warnings = append(value.Warnings, fmt.Sprintf("writer-%d", index))
					return nil
				},
			)
			switch {
			case updateErr == nil:
				successes.Add(1)
			case errors.Is(updateErr, ErrConflict):
				conflicts.Add(1)
			default:
				t.Errorf("unexpected concurrent Update error: %v", updateErr)
			}
		}(index)
	}
	wait.Wait()
	if successes.Load() != 1 || conflicts.Load() != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes.Load(), conflicts.Load())
	}
}

func TestStoreListSortsAndIgnoresUnrelatedFiles(t *testing.T) {
	store := newEvaluationTestStore(t, 10)
	first, err := store.Create(context.Background(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return evaluationTestNow.Add(time.Hour) }
	second, err := store.Create(context.Background(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(filepath.Join(store.root, "README"), []byte("ignored"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if mkdirErr := os.Mkdir(filepath.Join(store.root, "ignored-dir"), 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	listed, err := store.List(context.Background())
	if err != nil || len(listed) != 2 || listed[0].ID != second.ID || listed[1].ID != first.ID {
		t.Fatalf("sorted List = %#v err=%v", listed, err)
	}
}

func TestStoreRejectsUnsafeStorageAndCorruption(t *testing.T) {
	t.Skip("legacy JSON corruption cases are replaced by hardened SQLite corruption tests")
	t.Run("symlink root", func(t *testing.T) {
		workspace := t.TempDir()
		if err := os.Symlink(t.TempDir(), filepath.Join(workspace, storeDirectory)); err != nil {
			t.Skip(err)
		}
		store := NewStore(workspace)
		if _, err := store.Create(context.Background(), validCreateRequest()); err == nil {
			t.Fatal("Create followed symlink root")
		}
	})
	t.Run("file root", func(t *testing.T) {
		workspace := t.TempDir()
		if err := os.WriteFile(filepath.Join(workspace, storeDirectory), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		store := NewStore(workspace)
		if _, err := store.List(context.Background()); err == nil {
			t.Fatal("List accepted file root")
		}
	})
	t.Run("broad root mode", func(t *testing.T) {
		if repositoryEvaluationPermissionsSafe(0o755) {
			t.Skip("POSIX permission bits are not meaningful on this platform")
		}
		store := NewStore(t.TempDir())
		if err := os.Mkdir(store.root, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := store.List(context.Background()); err == nil {
			t.Fatal("List accepted broad root permissions")
		}
	})
	t.Run("symlink state", func(t *testing.T) {
		store := newEvaluationTestStore(t, 11)
		created, err := store.Create(context.Background(), validCreateRequest())
		if err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.json")
		if err := os.Rename(store.path(created.ID), outside); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, store.path(created.ID)); err != nil {
			t.Skip(err)
		}
		if _, _, err := store.Get(context.Background(), created.ID); err == nil {
			t.Fatal("Get followed state symlink")
		}
		if _, err := store.List(context.Background()); err == nil {
			t.Fatal("List followed state symlink")
		}
	})
	t.Run("broad state mode", func(t *testing.T) {
		if repositoryEvaluationPermissionsSafe(0o644) {
			t.Skip("POSIX permission bits are not meaningful on this platform")
		}
		store := newEvaluationTestStore(t, 12)
		created, _ := store.Create(context.Background(), validCreateRequest())
		if err := os.Chmod(store.path(created.ID), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Get(context.Background(), created.ID); err == nil {
			t.Fatal("Get accepted broad state permissions")
		}
	})
	t.Run("oversize state", func(t *testing.T) {
		store := newEvaluationTestStore(t, 13)
		created, _ := store.Create(context.Background(), validCreateRequest())
		if err := os.Truncate(store.path(created.ID), maxStateFileBytes+1); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Get(context.Background(), created.ID); err == nil {
			t.Fatal("Get accepted oversized state")
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		store := newEvaluationTestStore(t, 14)
		created, _ := store.Create(context.Background(), validCreateRequest())
		if err := os.WriteFile(store.path(created.ID), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Get(context.Background(), created.ID); err == nil {
			t.Fatal("Get accepted invalid JSON")
		}
	})
	t.Run("identity mismatch", func(t *testing.T) {
		store := newEvaluationTestStore(t, 15)
		created, _ := store.Create(context.Background(), validCreateRequest())
		data, _ := os.ReadFile(store.path(created.ID))
		var evaluation Evaluation
		_ = json.Unmarshal(data, &evaluation)
		evaluation.ID = testEvaluationID(99)
		data, _ = json.Marshal(evaluation)
		if err := os.WriteFile(store.path(created.ID), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Get(context.Background(), created.ID); err == nil {
			t.Fatal("Get accepted identity mismatch")
		}
	})
	t.Run("invalid schema", func(t *testing.T) {
		store := newEvaluationTestStore(t, 16)
		created, _ := store.Create(context.Background(), validCreateRequest())
		data, _ := os.ReadFile(store.path(created.ID))
		var evaluation Evaluation
		_ = json.Unmarshal(data, &evaluation)
		evaluation.SchemaVersion++
		data, _ = json.Marshal(evaluation)
		if err := os.WriteFile(store.path(created.ID), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Get(context.Background(), created.ID); !errors.Is(err, ErrInvalidEvaluation) {
			t.Fatalf("invalid schema error = %v", err)
		}
	})
}

func TestStoreIDGeneratorFailuresAndCatalogBound(t *testing.T) {
	t.Run("error", func(t *testing.T) {
		store := newEvaluationTestStore(t, 20)
		store.newID = func() (string, error) { return "", errors.New("entropy failed") }
		if _, err := store.Create(
			context.Background(),
			validCreateRequest(),
		); err == nil ||
			!strings.Contains(err.Error(), "entropy") {
			t.Fatalf("Create error = %v", err)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		store := newEvaluationTestStore(t, 21)
		store.newID = func() (string, error) { return "../../bad", nil }
		if _, err := store.Create(context.Background(), validCreateRequest()); err == nil {
			t.Fatal("Create accepted invalid generated ID")
		}
	})
	t.Run("collision exhaustion", func(t *testing.T) {
		store := newEvaluationTestStore(t, 22)
		first, err := store.Create(context.Background(), validCreateRequest())
		if err != nil {
			t.Fatal(err)
		}
		store.newID = func() (string, error) { return first.ID, nil }
		if _, err := store.Create(
			context.Background(),
			validCreateRequest(),
		); err == nil ||
			!strings.Contains(err.Error(), "unique") {
			t.Fatalf("collision Create error = %v", err)
		}
	})
	t.Run("catalog", func(t *testing.T) {
		t.Skip("legacy JSON file-count fixture is not a SQLite catalog fixture")
		store := newEvaluationTestStore(t, 23)
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		for index := 0; index < maxEvaluations; index++ {
			name := stateNamePrefix + testEvaluationID(index+1000) + stateFileSuffix
			if err := os.WriteFile(filepath.Join(store.root, name), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.Create(
			context.Background(),
			validCreateRequest(),
		); err == nil ||
			!strings.Contains(err.Error(), "catalog") {
			t.Fatalf("catalog Create error = %v", err)
		}
	})
}

func TestCreateValidationAndNormalization(t *testing.T) {
	tests := map[string]func(*CreateRequest){
		"repository": func(value *CreateRequest) { value.Repository = "" },
		"repository credentials": func(value *CreateRequest) {
			value.Repository = "https://token@github.com/owner/repo?access_token=secret"
		},
		"models low": func(value *CreateRequest) { value.CandidateModels = []string{"one"} },
		"models high": func(value *CreateRequest) {
			value.CandidateModels = []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}
		},
		"selector": func(value *CreateRequest) { value.SelectorModelAlias = "" },
		"judge":    func(value *CreateRequest) { value.JudgeModelAlias = strings.Repeat("x", maxAliasBytes+1) },
		"default":  func(value *CreateRequest) { value.DefaultFilesPerLanguage = 21 },
		"language low": func(value *CreateRequest) {
			value.FilesPerLanguage = map[string]int{"Go": 0}
		},
		"language high": func(value *CreateRequest) {
			value.FilesPerLanguage = map[string]int{"Go": 21}
		},
		"language blank": func(value *CreateRequest) { value.FilesPerLanguage = map[string]int{" ": 2} },
		"invalid code type": func(value *CreateRequest) {
			value.Focus.CodeTypes = []CodeType{"generated"}
		},
		"include traversal": func(value *CreateRequest) { value.Focus.IncludeFolders = []string{"../secret"} },
		"exclude absolute":  func(value *CreateRequest) { value.Focus.ExcludeFolders = []string{"/tmp"} },
		"free text":         func(value *CreateRequest) { value.Focus.FreeText = strings.Repeat("x", maxFreeTextBytes+1) },
	}
	for name, alter := range tests {
		t.Run(name, func(t *testing.T) {
			request := validCreateRequest()
			alter(&request)
			store := newEvaluationTestStore(t, 30)
			if _, err := store.Create(context.Background(), request); !errors.Is(err, ErrInvalidEvaluation) {
				t.Fatalf("Create error = %v", err)
			}
		})
	}
	blankRef := validCreateRequest()
	blankRef.Ref = "   "
	defaulted, err := newEvaluationTestStore(t, 32).Create(context.Background(), blankRef)
	if err != nil || defaulted.Ref != "HEAD" {
		t.Fatalf("blank ref default = %q, err=%v", defaulted.Ref, err)
	}

	request := validCreateRequest()
	request.CandidateModels = []string{" model-a ", "model-a", " model-b"}
	request.Focus.CodeTypes = []CodeType{CodeTypeTest, CodeTypeCode}
	request.Focus.IncludeFolders = []string{" web\\frontend ", "pkg", "pkg"}
	request.FilesPerLanguage = map[string]int{" Go ": 3}
	created, err := newEvaluationTestStore(t, 31).Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(created.CandidateModels, []string{"model-a", "model-b"}) ||
		!reflect.DeepEqual(created.Focus.CodeTypes, []CodeType{CodeTypeCode, CodeTypeTest}) ||
		created.FilesPerLanguage["Go"] != 3 {
		t.Fatalf("normalization result = %#v", created)
	}
}

func TestUpdateRejectsInvalidBoundedResults(t *testing.T) {
	tests := map[string]func(*Evaluation){
		"unknown status":    func(value *Evaluation) { value.Status = "unknown" },
		"progress negative": func(value *Evaluation) { value.Progress.TotalFiles = -1 },
		"progress relation": func(value *Evaluation) {
			value.Progress.TotalFiles = 1
			value.Progress.SelectedFiles = 2
		},
		"progress percent": func(value *Evaluation) { value.Progress.Percent = math.NaN() },
		"progress path":    func(value *Evaluation) { value.Progress.CurrentPath = "../bad" },
		"usage":            func(value *Evaluation) { value.Usage.InputTokens = -1 },
		"cost":             func(value *Evaluation) { value.Usage.EstimatedCostUSD = floatPointer(math.Inf(1)) },
		"warning":          func(value *Evaluation) { value.Warnings = []string{strings.Repeat("w", maxWarningBytes+1)} },
		"run id":           func(value *Evaluation) { value.RunIDs = []string{strings.Repeat("r", maxRunIDBytes+1)} },
		"stats key":        func(value *Evaluation) { value.ModelStats[""] = ModelStats{} },
		"stats relation": func(value *Evaluation) {
			value.ModelStats["model-a"] = ModelStats{Attempts: 1, Successes: 2}
		},
		"stats score": func(value *Evaluation) {
			value.ModelStats["model-a"] = ModelStats{OverallScore: math.NaN()}
		},
		"nonfailure detail": func(value *Evaluation) { value.Failure = "not failed" },
	}
	for name, alter := range tests {
		t.Run(name, func(t *testing.T) {
			store := newEvaluationTestStore(t, 40)
			created, err := store.Create(context.Background(), validCreateRequest())
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.Update(context.Background(), created.ID, created.Version, func(value *Evaluation) error {
				alter(value)
				return nil
			})
			if !errors.Is(err, ErrInvalidEvaluation) && !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("Update error = %v", err)
			}
		})
	}
}

func TestManifestAndComparisonValidation(t *testing.T) {
	manifestTests := map[string]func(*CorpusManifest){
		"commit":       func(value *CorpusManifest) { value.CommitSHA = "short" },
		"inventory":    func(value *CorpusManifest) { value.InventoryHash = "" },
		"policy":       func(value *CorpusManifest) { value.PolicyHash = "" },
		"rubric":       func(value *CorpusManifest) { value.RubricHash = "" },
		"selector run": func(value *CorpusManifest) { value.SelectorRunID = "" },
		"generated":    func(value *CorpusManifest) { value.GeneratedAt = time.Time{} },
		"no files":     func(value *CorpusManifest) { value.Files = nil; value.LanguageCounts = map[string]int{} },
		"duplicate": func(value *CorpusManifest) {
			value.Files = append(value.Files, value.Files[0])
			value.LanguageCounts["Go"]++
		},
		"duplicate candidate": func(value *CorpusManifest) {
			value.Files[1].CandidateID = value.Files[0].CandidateID
		},
		"counts":    func(value *CorpusManifest) { value.LanguageCounts["Go"] = 9 },
		"candidate": func(value *CorpusManifest) { value.Files[0].CandidateID = "cand_bad" },
		"path":      func(value *CorpusManifest) { value.Files[0].Path = "../bad" },
		"blob":      func(value *CorpusManifest) { value.Files[0].BlobSHA = "bad" },
		"size":      func(value *CorpusManifest) { value.Files[0].SizeBytes = -1 },
		"language":  func(value *CorpusManifest) { value.Files[0].Language = "" },
		"code type": func(value *CorpusManifest) { value.Files[0].CodeType = "other" },
		"module":    func(value *CorpusManifest) { value.Files[0].Module = "../other" },
		"region":    func(value *CorpusManifest) { value.Files[0].Region = "../other" },
		"no chunks": func(value *CorpusManifest) { value.Files[0].Chunks = nil },
		"chunk id":  func(value *CorpusManifest) { value.Files[0].Chunks[0].ID = "" },
		"chunk hash": func(value *CorpusManifest) {
			value.Files[0].Chunks[0].ContentHash = ""
		},
		"chunk lines": func(value *CorpusManifest) { value.Files[0].Chunks[0].StartLine = 0 },
		"overlap": func(value *CorpusManifest) {
			value.Files[0].Chunks = append(
				value.Files[0].Chunks,
				CorpusChunk{ID: "two", StartLine: 2, EndLine: 4, ContentHash: "sha256:two"},
			)
		},
		"language limit": func(value *CorpusManifest) {
			value.Files = append(value.Files, CorpusFile{
				CandidateID: testCandidateID("d"), Path: "pkg/extra.go",
				BlobSHA: strings.Repeat("c", 40), SizeBytes: 1,
				Language: "Go", CodeType: CodeTypeCode, Module: "pkg", Region: "pkg",
				Chunks: []CorpusChunk{{ID: "extra", StartLine: 1, EndLine: 1, ContentHash: "sha256:extra"}},
			})
			value.LanguageCounts["Go"]++
		},
	}
	for name, alter := range manifestTests {
		t.Run(name, func(t *testing.T) {
			store := newEvaluationTestStore(t, 50)
			request := validCreateRequest()
			if name == "language limit" {
				request.FilesPerLanguage["Go"] = 1
			}
			created, _ := store.Create(context.Background(), request)
			preflight := updateEvaluation(
				t,
				store,
				created,
				func(value *Evaluation) { value.Status = StatusPreflighting },
			)
			_, err := store.Update(
				context.Background(),
				preflight.ID,
				preflight.Version,
				func(value *Evaluation) error {
					value.Corpus = validManifest()
					alter(value.Corpus)
					return nil
				},
			)
			if !errors.Is(err, ErrInvalidEvaluation) {
				t.Fatalf("Update error = %v", err)
			}
		})
	}

	comparisonTests := map[string]func(*Evaluation){
		"unknown model": func(value *Evaluation) { value.Comparisons[0].ModelAlias = "other" },
		"duplicate model": func(value *Evaluation) {
			value.Comparisons[1].ModelAlias = value.Comparisons[0].ModelAlias
		},
		"duplicate rank": func(value *Evaluation) { value.Comparisons[1].Rank = 1 },
		"missing rank":   func(value *Evaluation) { value.Comparisons[0].Rank = 0 },
		"score": func(value *Evaluation) {
			value.Comparisons[0].OverallScore = floatPointer(math.NaN())
		},
		"metric":       func(value *Evaluation) { value.Comparisons[0].Scores[""] = 1 },
		"metric score": func(value *Evaluation) { value.Comparisons[0].Scores["quality"] = math.Inf(1) },
		"region":       func(value *Evaluation) { value.Comparisons[0].Regions = []string{"../bad"} },
		"language": func(value *Evaluation) {
			value.Comparisons[0].Languages = []string{strings.Repeat("l", maxLanguageBytes+1)}
		},
		"files":     func(value *Evaluation) { value.Comparisons[0].FilesAnalyzed = -1 },
		"bytes":     func(value *Evaluation) { value.Comparisons[0].BytesAnalyzed = -1 },
		"confirmed": func(value *Evaluation) { value.Comparisons[0].ConfirmedFindings = -1 },
		"unsupported claims": func(value *Evaluation) {
			value.Comparisons[0].UnsupportedClaims = intPointer(-1)
		},
		"unsupported": func(value *Evaluation) { value.Comparisons[0].UnsupportedFiles = -1 },
		"strength": func(value *Evaluation) {
			value.Comparisons[0].Strengths = []string{strings.Repeat("s", maxWarningBytes+1)}
		},
	}
	for name, alter := range comparisonTests {
		t.Run("comparison "+name, func(t *testing.T) {
			store, analyzing := evaluationAtAnalyzing(t, 60)
			_, err := store.Update(
				context.Background(),
				analyzing.ID,
				analyzing.Version,
				func(value *Evaluation) error {
					value.Status = StatusCompleted
					value.Comparisons = validComparisons()
					alter(value)
					return nil
				},
			)
			if !errors.Is(err, ErrInvalidEvaluation) {
				t.Fatalf("Update error = %v", err)
			}
		})
	}
}

func TestCloneAndStatusHelpers(t *testing.T) {
	evaluation := Evaluation{
		CandidateModels: []string{
			"a",
			"b",
		},
		Focus:            Focus{CodeTypes: []CodeType{CodeTypeCode}, IncludeFolders: []string{"pkg"}},
		FilesPerLanguage: map[string]int{"Go": 2},
		Corpus:           validManifest(),
		ModelStats:       map[string]ModelStats{"a": {StartedAt: timePointer(evaluationTestNow)}},
		Comparisons:      validComparisons(),
		Warnings:         []string{"warn"},
		RunIDs:           []string{"run"},
		StartedAt:        timePointer(evaluationTestNow),
		FinishedAt:       timePointer(evaluationTestNow),
	}
	clone := Clone(evaluation)
	clone.CandidateModels[0] = "changed"
	clone.Focus.CodeTypes[0] = CodeTypeTest
	clone.Focus.IncludeFolders[0] = "web"
	clone.FilesPerLanguage["Go"] = 20
	clone.Corpus.Files[0].Chunks[0].ID = "changed"
	clone.Corpus.LanguageCounts["Go"] = 20
	stats := clone.ModelStats["a"]
	*stats.StartedAt = stats.StartedAt.Add(time.Hour)
	clone.ModelStats["a"] = stats
	clone.Comparisons[0].Scores["quality"] = 0
	clone.Comparisons[0].Languages[0] = "Rust"
	clone.Comparisons[0].Regions[0] = "web"
	clone.Comparisons[0].Strengths[0] = "changed"
	clone.Comparisons[0].Limitations[0] = "changed"
	*clone.Comparisons[0].Usage.EstimatedCostUSD = 999
	clone.Warnings[0] = "changed"
	clone.RunIDs[0] = "changed"
	*clone.StartedAt = clone.StartedAt.Add(time.Hour)
	if evaluation.CandidateModels[0] != "a" || evaluation.Corpus.Files[0].Chunks[0].ID != "chunk-1" ||
		evaluation.Comparisons[0].Scores["quality"] == 0 || evaluation.Warnings[0] != "warn" ||
		evaluation.StartedAt.Equal(*clone.StartedAt) {
		t.Fatalf("Clone was shallow: original=%#v clone=%#v", evaluation, clone)
	}

	statuses := []Status{
		StatusDraft, StatusPreflighting, StatusReady, StatusRunning, StatusJudging,
		StatusAnalyzing, StatusCompleted, StatusCanceling, StatusCanceled, StatusFailed,
	}
	for _, status := range statuses {
		if !status.Valid() || !status.CanTransitionTo(status) {
			t.Errorf("status %q should be valid and support idempotent transition", status)
		}
	}
	if Status("bad").Valid() || Status("bad").Terminal() || Status("bad").InFlight() ||
		Status("bad").RecoveryDirective() != RecoveryNone || StatusCompleted.CanTransitionTo(StatusRunning) {
		t.Fatal("invalid/terminal status helpers returned unsafe result")
	}
	for _, status := range []Status{StatusPreflighting, StatusRunning, StatusJudging, StatusAnalyzing, StatusCanceling} {
		if !status.InFlight() {
			t.Errorf("status %q should be in flight", status)
		}
	}
	if !CodeTypeHotpath.Valid() || !CodeTypeCode.Valid() || !CodeTypeTest.Valid() || !CodeTypeBenchmark.Valid() ||
		CodeType("bad").Valid() {
		t.Fatal("code type validity is wrong")
	}
	if !ModelCompletionPending.Valid() || !ModelCompletionCompleted.Valid() || !ModelCompletionPartial.Valid() ||
		!ModelCompletionFailed.Valid() || ModelCompletion("bad").Valid() {
		t.Fatal("model completion validity is wrong")
	}
	if (Evaluation{}).LatestRunID() != "" {
		t.Fatal("empty LatestRunID should be empty")
	}
}

func validCreateRequest() CreateRequest {
	return CreateRequest{
		Repository: " owner/repo ", Ref: " main ",
		CandidateModels:    []string{"model-a", "model-b"},
		SelectorModelAlias: "selector", JudgeModelAlias: "judge",
		Focus: Focus{
			CodeTypes:      []CodeType{CodeTypeTest, CodeTypeCode},
			IncludeFolders: []string{" web/frontend ", "pkg"}, ExcludeFolders: []string{"vendor"},
		},
		FilesPerLanguage: map[string]int{"Go": 3, "TypeScript": 2},
	}
}

func validManifest() *CorpusManifest {
	return &CorpusManifest{
		CommitSHA: strings.Repeat("a", 40), InventoryHash: "sha256:inventory",
		PolicyHash: "sha256:policy", RubricHash: "sha256:rubric", SelectorRunID: "selector-run",
		SelectionRationale: "Representative language and region coverage.",
		Files: []CorpusFile{
			{
				CandidateID: testCandidateID("a"), Path: "pkg/service.go",
				BlobSHA: strings.Repeat("b", 40), SizeBytes: 120,
				Language: "Go", CodeType: CodeTypeCode, Module: "pkg", Region: "pkg",
				Chunks: []CorpusChunk{{ID: "chunk-1", StartLine: 1, EndLine: 20, ContentHash: "sha256:chunk-1"}},
			},
			{
				CandidateID: testCandidateID("b"), Path: "web/frontend/app.ts",
				BlobSHA: strings.Repeat("c", 40), SizeBytes: 90,
				Language: "TypeScript", CodeType: CodeTypeCode, Module: "web/frontend", Region: "web/frontend",
				Chunks: []CorpusChunk{{ID: "chunk-2", StartLine: 2, EndLine: 10, ContentHash: "sha256:chunk-2"}},
			},
		},
		LanguageCounts: map[string]int{"Go": 1, "TypeScript": 1}, GeneratedAt: evaluationTestNow,
	}
}

func validComparisons() []ModelComparison {
	return []ModelComparison{
		{
			ModelAlias:        "model-a",
			ConcreteModels:    map[string]int{"openai/model-a": 2},
			Completion:        ModelCompletionCompleted,
			Rank:              1,
			OverallScore:      floatPointer(91),
			Scores:            map[string]float64{"quality": 95, "precision": 87},
			Languages:         []string{"Go", "TypeScript"},
			Regions:           []string{"pkg", "web/frontend"},
			FilesAnalyzed:     2,
			BytesAnalyzed:     210,
			ConfirmedFindings: 3,
			Usage:             Usage{Requests: 2, DurationMillis: 10, EstimatedCostUSD: floatPointer(.02)},
			Verdict:           "winner",
			Summary:           "Best precision",
			Strengths:         []string{"precise"},
			Limitations:       []string{"slower"},
		},
		{
			ModelAlias: "model-b", ConcreteModels: map[string]int{"openai/model-b": 2},
			Completion: ModelCompletionPartial, Failure: "one unsupported file", Failures: 1,
			Scores: map[string]float64{}, Languages: []string{"Go", "TypeScript"},
			Regions: []string{"pkg", "web/frontend"}, FilesAnalyzed: 2, BytesAnalyzed: 210,
			ConfirmedFindings: 2, UnsupportedFiles: 1, Summary: "Fast",
		},
	}
}

func newEvaluationTestStore(t *testing.T, seed int) Store {
	t.Helper()
	store := NewStore(t.TempDir())
	store.now = func() time.Time { return evaluationTestNow }
	next := seed
	store.newID = func() (string, error) {
		id := testEvaluationID(next)
		next++
		return id, nil
	}
	return store
}

func testEvaluationID(value int) string {
	return fmt.Sprintf("rme_%032x", value)
}

func floatPointer(value float64) *float64 { return &value }
func intPointer(value int) *int           { return &value }

func testCandidateID(marker string) string { return "cand_" + strings.Repeat(marker, 64) }

func updateEvaluation(t *testing.T, store Store, evaluation Evaluation, mutate func(*Evaluation)) Evaluation {
	t.Helper()
	updated, err := store.Update(
		context.Background(),
		evaluation.ID,
		evaluation.Version,
		func(value *Evaluation) error {
			mutate(value)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func evaluationAtAnalyzing(t *testing.T, seed int) (Store, Evaluation) {
	t.Helper()
	store := newEvaluationTestStore(t, seed)
	created, err := store.Create(context.Background(), validCreateRequest())
	if err != nil {
		t.Fatal(err)
	}
	preflight := updateEvaluation(t, store, created, func(value *Evaluation) { value.Status = StatusPreflighting })
	ready := updateEvaluation(t, store, preflight, func(value *Evaluation) {
		value.Status = StatusReady
		value.Corpus = validManifest()
	})
	running := updateEvaluation(t, store, ready, func(value *Evaluation) { value.Status = StatusRunning })
	judging := updateEvaluation(t, store, running, func(value *Evaluation) { value.Status = StatusJudging })
	analyzing := updateEvaluation(t, store, judging, func(value *Evaluation) { value.Status = StatusAnalyzing })
	return store, analyzing
}

// RegionCountForTest deliberately lives only in tests; it keeps the lifecycle
// assertion readable without adding a production helper for a one-off count.
func (manifest *CorpusManifest) RegionCountForTest() int {
	regions := make(map[string]struct{})
	for _, file := range manifest.Files {
		regions[file.Region] = struct{}{}
	}
	return len(regions)
}
