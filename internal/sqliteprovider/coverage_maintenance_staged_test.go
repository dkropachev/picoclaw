//nolint:govet,prealloc // Boundary tables are extended conditionally by test phase.
package sqliteprovider

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCoverageProviderMaintenanceScriptedFailures(t *testing.T) {
	canary := errors.New("maintenance rows canary")
	for _, test := range providerIntegrityFailureScripts(canary) {
		t.Run(test.name, func(t *testing.T) {
			if err := maintenanceIntegrity(
				t.Context(),
				openProviderScript(t, test.steps...),
			); !IsMaintenanceIntegrity(
				err,
			) {
				t.Fatalf("maintenance integrity error = %v", err)
			}
		})
	}
	if err := maintenanceCheckpoint(t.Context(), openProviderScript(t,
		providerRow("wal_checkpoint", int64(0), int64(3), int64(3))), "FULL"); err != nil {
		t.Fatal(err)
	}
	if err := maintenanceCheckpoint(t.Context(), openProviderScript(t,
		providerRow("wal_checkpoint", int64(1), int64(3), int64(0))), "FULL"); err == nil {
		t.Fatal("busy checkpoint succeeded")
	}
	if err := maintenanceCheckpoint(t.Context(), openProviderScript(t,
		providerScriptStep{query: "wal_checkpoint", err: canary}), "FULL"); !errors.Is(err, canary) {
		t.Fatalf("checkpoint query error = %v", err)
	}
}

func TestCoverageMaintenanceBusyAndCanceledIntegrity(t *testing.T) {
	canary := errors.New("maintenance classifier canary")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := maintenanceIntegrityWithClassifier(
		ctx, openProviderScript(t,
			providerScriptStep{query: "integrity_check", err: canary}), nil,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled maintenance error = %v", err)
	}
}

func TestCoverageMaintainOfflinePhaseErrors(t *testing.T) {
	canary := errors.New("maintenance phase canary")
	path := filepath.Join(t.TempDir(), "store.db")
	base := func() maintenanceOps {
		return maintenanceOps{
			inspect:    func(context.Context, string, time.Duration) (int, error) { return 1, nil },
			boundary:   func(context.Context, string, time.Duration) error { return nil },
			checkpoint: func(context.Context, string, time.Duration) error { return nil },
			reopen:     func(context.Context, string, time.Duration) (int, error) { return 1, nil },
		}
	}
	for _, test := range []struct {
		name   string
		mutate func(*maintenanceOps)
	}{
		{name: "inspect", mutate: func(ops *maintenanceOps) {
			ops.inspect = func(context.Context, string, time.Duration) (int, error) { return 0, canary }
		}},
		{name: "boundary", mutate: func(ops *maintenanceOps) {
			ops.boundary = func(context.Context, string, time.Duration) error { return canary }
		}},
		{name: "checkpoint", mutate: func(ops *maintenanceOps) {
			ops.checkpoint = func(context.Context, string, time.Duration) error { return canary }
		}},
		{name: "reopen", mutate: func(ops *maintenanceOps) {
			ops.reopen = func(context.Context, string, time.Duration) (int, error) { return 0, canary }
		}},
		{name: "version", mutate: func(ops *maintenanceOps) {
			ops.reopen = func(context.Context, string, time.Duration) (int, error) { return 2, nil }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := base()
			test.mutate(&ops)
			if _, err := maintainOffline(t.Context(), path, time.Second, ops); err == nil {
				t.Fatal("maintenance phase succeeded")
			}
		})
	}
	ops := base()
	if result, err := maintainOffline(t.Context(), path, time.Second, ops); err != nil ||
		result.BeforeVersion != 1 || result.AfterVersion != 1 {
		t.Fatalf("maintenance result = %#v, %v", result, err)
	}
}

func TestCoverageMaintenanceDatabasePhases(t *testing.T) {
	canary := errors.New("maintenance phase canary")
	if _, err := inspectAndRecoverDatabase(
		t.Context(), openConfiguredProviderScript(t, canary),
	); !errors.Is(err, canary) {
		t.Fatalf("inspect ping error = %v", err)
	}
	if _, err := inspectAndRecoverDatabase(t.Context(), openProviderScript(t,
		providerScriptStep{query: "integrity_check", err: canary},
	)); !IsMaintenanceIntegrity(err) {
		t.Fatalf("inspect integrity error = %v", err)
	}
	steps := append(providerIntegrityOKSteps(), providerScriptStep{query: "user_version", err: canary})
	if _, err := inspectAndRecoverDatabase(
		t.Context(), openProviderScript(t, steps...),
	); !errors.Is(err, canary) {
		t.Fatalf("inspect version error = %v", err)
	}
	steps = append(providerIntegrityOKSteps(), providerRow("user_version", int64(1)))
	steps = append(steps, providerScriptStep{query: "wal_checkpoint", err: canary})
	if _, err := inspectAndRecoverDatabase(
		t.Context(), openProviderScript(t, steps...),
	); !errors.Is(err, canary) {
		t.Fatalf("inspect checkpoint error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "generation.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	boundaryTests := []struct {
		name  string
		steps []providerScriptStep
	}{
		{name: "locking query", steps: []providerScriptStep{{query: "locking_mode", err: canary}}},
		{name: "locking selection", steps: []providerScriptStep{providerRow("locking_mode", "normal")}},
		{name: "journal query", steps: []providerScriptStep{
			providerRow("locking_mode", "exclusive"), {query: "journal_mode", err: canary},
		}},
		{name: "journal selection", steps: []providerScriptStep{
			providerRow("locking_mode", "exclusive"), providerRow("journal_mode", "wal"),
		}},
		{name: "begin", steps: []providerScriptStep{
			providerRow("locking_mode", "exclusive"), providerRow("journal_mode", "delete"),
			{query: "BEGIN EXCLUSIVE", err: canary},
		}},
		{name: "integrity", steps: append([]providerScriptStep{
			providerRow("locking_mode", "exclusive"), providerRow("journal_mode", "delete"),
			{query: "BEGIN EXCLUSIVE"},
		}, providerScriptStep{query: "integrity_check", err: canary})},
	}
	base := []providerScriptStep{
		providerRow("locking_mode", "exclusive"), providerRow("journal_mode", "delete"),
		{query: "BEGIN EXCLUSIVE"},
	}
	base = append(base, providerIntegrityOKSteps()...)
	boundaryTests = append(boundaryTests,
		struct {
			name  string
			steps []providerScriptStep
		}{name: "commit", steps: append(append([]providerScriptStep(nil), base...),
			providerScriptStep{query: "COMMIT", err: canary})},
	)
	committed := append(append([]providerScriptStep(nil), base...), providerScriptStep{query: "COMMIT"})
	boundaryTests = append(boundaryTests,
		struct {
			name  string
			steps []providerScriptStep
		}{name: "restore journal query", steps: append(append([]providerScriptStep(nil), committed...),
			providerScriptStep{query: "journal_mode", err: canary})},
		struct {
			name  string
			steps []providerScriptStep
		}{name: "restore journal selection", steps: append(append([]providerScriptStep(nil), committed...),
			providerRow("journal_mode", "delete"))},
	)
	restored := append(append([]providerScriptStep(nil), committed...), providerRow("journal_mode", "wal"))
	boundaryTests = append(boundaryTests,
		struct {
			name  string
			steps []providerScriptStep
		}{name: "restore locking query", steps: append(append([]providerScriptStep(nil), restored...),
			providerScriptStep{query: "locking_mode", err: canary})},
		struct {
			name  string
			steps []providerScriptStep
		}{name: "restore locking selection", steps: append(append([]providerScriptStep(nil), restored...),
			providerRow("locking_mode", "exclusive"))},
	)
	for _, test := range boundaryTests {
		t.Run("boundary "+test.name, func(t *testing.T) {
			if err := exclusiveRollbackBoundaryDatabase(
				t.Context(), openProviderScript(t, test.steps...), path,
			); err == nil {
				t.Fatal("boundary phase succeeded")
			}
		})
	}
	success := append(append([]providerScriptStep(nil), restored...), providerRow("locking_mode", "normal"))
	if err := exclusiveRollbackBoundaryDatabase(
		t.Context(), openProviderScript(t, success...), path,
	); err != nil {
		t.Fatal(err)
	}

	if err := checkpointGenerationDatabase(
		t.Context(), openConfiguredProviderScript(t, canary), path,
	); !errors.Is(err, canary) {
		t.Fatalf("checkpoint ping error = %v", err)
	}
	if err := checkpointGenerationDatabase(t.Context(), openProviderScript(t,
		providerScriptStep{query: "wal_checkpoint", err: canary},
	), path); !errors.Is(err, canary) {
		t.Fatalf("checkpoint query error = %v", err)
	}
	if err := checkpointGenerationDatabase(t.Context(), openProviderScript(t,
		providerRow("wal_checkpoint", int64(0), int64(0), int64(0)),
	), path); err != nil {
		t.Fatal(err)
	}

	if _, err := reopenAndValidateDatabase(
		t.Context(), openConfiguredProviderScript(t, canary), path,
	); !errors.Is(err, canary) {
		t.Fatalf("reopen ping error = %v", err)
	}
	if _, err := reopenAndValidateDatabase(t.Context(), openProviderScript(t,
		providerScriptStep{query: "integrity_check", err: canary},
	), path); !IsMaintenanceIntegrity(err) {
		t.Fatalf("reopen integrity error = %v", err)
	}
	steps = append(providerIntegrityOKSteps(), providerScriptStep{query: "journal_mode", err: canary})
	if _, err := reopenAndValidateDatabase(
		t.Context(), openProviderScript(t, steps...), path,
	); !errors.Is(err, canary) {
		t.Fatalf("reopen journal error = %v", err)
	}
	steps = append(providerIntegrityOKSteps(), providerRow("journal_mode", "delete"))
	if _, err := reopenAndValidateDatabase(
		t.Context(), openProviderScript(t, steps...), path,
	); err == nil {
		t.Fatal("reopen accepted non-WAL journal")
	}
	steps = append(providerIntegrityOKSteps(), providerRow("journal_mode", "wal"),
		providerScriptStep{query: "user_version", err: canary})
	if _, err := reopenAndValidateDatabase(
		t.Context(), openProviderScript(t, steps...), path,
	); !errors.Is(err, canary) {
		t.Fatalf("reopen version error = %v", err)
	}
	steps = append(providerIntegrityOKSteps(), providerRow("journal_mode", "wal"),
		providerRow("user_version", int64(3)))
	if version, err := reopenAndValidateDatabase(
		t.Context(), openProviderScript(t, steps...), path,
	); err != nil || version != 3 {
		t.Fatalf("reopen version = %d, %v", version, err)
	}
}

func TestCoverageStagedMigrationInputAndCleanupEdges(t *testing.T) {
	validMigration := func(ctx context.Context, stage string) error {
		database, err := openOfflineStage(ctx, stage, 100*time.Millisecond)
		if err != nil {
			return err
		}
		defer database.Close()
		if _, err := database.ExecContext(ctx, "CREATE TABLE staged(id INTEGER PRIMARY KEY)"); err != nil {
			return err
		}
		return SetSchemaVersion(ctx, database, 1)
	}
	for _, test := range []struct {
		name    string
		path    string
		version int
		migrate StagedMigration
	}{
		{name: "empty path", version: 1, migrate: validMigration},
		{name: "memory", path: ":memory:", version: 1, migrate: validMigration},
		{name: "nul path", path: "bad\x00.db", version: 1, migrate: validMigration},
		{name: "zero version", path: filepath.Join(t.TempDir(), "zero.db"), migrate: validMigration},
		{name: "nil migration", path: filepath.Join(t.TempDir(), "nil.db"), version: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := migrateStagedFixture(
				t.Context(),
				test.path,
				time.Second,
				test.version,
				test.migrate,
				acceptStagedValidation,
			); err == nil {
				t.Fatal("invalid staged migration succeeded")
			}
		})
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := migrateStagedFixture(
		canceled, filepath.Join(t.TempDir(), "canceled.db"), time.Second, 1,
		validMigration, acceptStagedValidation,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled staged migration = %v", err)
	}

	newPath := filepath.Join(t.TempDir(), "new.db")
	if err := migrateStagedFixture(
		nil, newPath, 100*time.Millisecond, 1, validMigration, acceptStagedValidation,
	); err != nil {
		t.Fatal(err)
	}
	if ready, err := testSchemaObjects(t.Context(), newPath, "staged"); err != nil || !ready {
		t.Fatalf("new staged generation ready=%v err=%v", ready, err)
	}

	wrongVersionPath := filepath.Join(t.TempDir(), "wrong.db")
	if err := migrateStagedFixture(
		t.Context(), wrongVersionPath, 100*time.Millisecond, 2,
		validMigration, acceptStagedValidation,
	); err == nil || !strings.Contains(err.Error(), "staged schema version") {
		t.Fatalf("wrong staged version error = %v", err)
	}
	if _, err := os.Stat(wrongVersionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed staged destination remains: %v", err)
	}

	unsafe := filepath.Join(t.TempDir(), "unsafe.db")
	if err := os.Mkdir(unsafe, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := migrateStagedFixture(
		t.Context(), unsafe, time.Second, 1, validMigration, acceptStagedValidation,
	); err == nil {
		t.Fatal("staged migration accepted directory source")
	}
	if exists, err := regularGenerationExists(filepath.Join(t.TempDir(), "missing.db")); err != nil || exists {
		t.Fatalf("missing generation exists=%v err=%v", exists, err)
	}
	if exists, err := regularGenerationExists(unsafe); err == nil || exists {
		t.Fatalf("unsafe generation exists=%v err=%v", exists, err)
	}

	sidecarBase := filepath.Join(t.TempDir(), "sidecar.db")
	if err := os.WriteFile(sidecarBase+"-wal", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := requireNoGenerationSidecars(sidecarBase); err == nil {
		t.Fatal("active staged sidecar accepted")
	}
	if err := requireNoGenerationSidecars(filepath.Join(t.TempDir(), "clean.db")); err != nil {
		t.Fatal(err)
	}
	if err := discardStagedGeneration(filepath.Join(t.TempDir(), "missing.db"), time.Second); err != nil {
		t.Fatal(err)
	}
	corruptStage := filepath.Join(t.TempDir(), "corrupt-stage.db")
	if err := os.WriteFile(corruptStage, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := discardStagedGeneration(corruptStage, 100*time.Millisecond); err == nil {
		t.Fatal("corrupt disposable stage retention was not reported")
	}
	if _, err := os.Stat(corruptStage); err != nil {
		t.Fatalf("diagnostic corrupt stage was not retained: %v", err)
	}
}

func TestCoverageOnlineBackupStepping(t *testing.T) {
	if err := stepOnlineBackup(t.Context(), nil); err == nil {
		t.Fatal("nil online backup succeeded")
	}
	backup := &providerBackupScript{steps: []providerBackupStep{{more: true}, {more: false}}}
	if err := stepOnlineBackup(t.Context(), backup); err != nil || backup.finishCalls != 1 {
		t.Fatalf("successful backup = %v, finish calls %d", err, backup.finishCalls)
	}
	canary := errors.New("backup step canary")
	backup = &providerBackupScript{steps: []providerBackupStep{{err: canary}}}
	if err := stepOnlineBackup(t.Context(), backup); !errors.Is(err, canary) || backup.finishCalls != 1 {
		t.Fatalf("failed backup = %v, finish calls %d", err, backup.finishCalls)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	backup = &providerBackupScript{}
	if err := stepOnlineBackup(canceled, backup); !errors.Is(err, context.Canceled) ||
		backup.finishCalls != 1 {
		t.Fatalf("canceled backup = %v, finish calls %d", err, backup.finishCalls)
	}
	backup = &providerBackupScript{
		steps: []providerBackupStep{{more: false}}, finishErr: canary,
	}
	if err := stepOnlineBackup(t.Context(), backup); !errors.Is(err, canary) || backup.finishCalls != 1 {
		t.Fatalf("finish failure = %v, finish calls %d", err, backup.finishCalls)
	}
}

func TestCoverageOnlineBackupRejectsUnsupportedDriver(t *testing.T) {
	database := openProviderScript(t)
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := backupGenerationConnection(t.Context(), connection, "destination"); err == nil {
		t.Fatal("unsupported driver exposed online backup")
	}
}

func TestCoverageStagedMigrationValidationAndIdentityFailures(t *testing.T) {
	t.Run("contract", func(t *testing.T) {
		path := createStagedMigrationFixture(t)
		canary := errors.New("contract canary")
		err := migrateStagedFixture(
			t.Context(), path, time.Second, 1, installStagedFixtureTable,
			func(context.Context, string) error { return canary },
		)
		if !errors.Is(err, canary) {
			t.Fatalf("contract validation error = %v", err)
		}
	})
	t.Run("source sidecar after validation", func(t *testing.T) {
		path := createStagedMigrationFixture(t)
		err := migrateStagedFixture(
			t.Context(), path, time.Second, 1, installStagedFixtureTable,
			func(context.Context, string) error {
				return os.WriteFile(path+"-wal", nil, 0o600)
			},
		)
		if err == nil {
			t.Fatal("late source sidecar was accepted")
		}
	})
	t.Run("stage sidecar after validation", func(t *testing.T) {
		path := createStagedMigrationFixture(t)
		err := migrateStagedFixture(
			t.Context(), path, time.Second, 1, installStagedFixtureTable,
			func(_ context.Context, stage string) error {
				return os.WriteFile(stage+"-journal", nil, 0o600)
			},
		)
		if err == nil {
			t.Fatal("late staged sidecar was accepted")
		}
	})
	path := filepath.Join(t.TempDir(), "identity.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if same, err := sameRegularGeneration(filepath.Join(t.TempDir(), "missing.db"), info); err == nil || same {
		t.Fatalf("missing identity same=%t err=%v", same, err)
	}
	if same, err := sameRegularGeneration(path, nil); err != nil || same {
		t.Fatalf("nil identity same=%t err=%v", same, err)
	}
	if err := activateInstalledGeneration(
		t.Context(), filepath.Join(t.TempDir(), "missing.db"), time.Second, 1,
	); err == nil {
		t.Fatal("missing generation activated")
	}
	if err := backupGenerationToStage(
		t.Context(), "file:invalid.db", filepath.Join(t.TempDir(), "stage.db"), time.Second,
	); err == nil {
		t.Fatal("invalid source backup succeeded")
	}
}

func TestCoverageInstalledActivationAndStageCleanupPhases(t *testing.T) {
	canary := errors.New("installed phase canary")
	checkpoint := func(context.Context, string, time.Duration) error { return nil }
	reopen := func(context.Context, string, time.Duration) (int, error) { return 2, nil }
	if err := activateInstalledGenerationWithOps(
		t.Context(), "store.db", time.Second, 2, checkpoint, reopen,
	); err != nil {
		t.Fatal(err)
	}
	if err := activateInstalledGenerationWithOps(
		t.Context(), "store.db", time.Second, 2,
		func(context.Context, string, time.Duration) error { return canary }, reopen,
	); !errors.Is(err, canary) {
		t.Fatalf("activation checkpoint error = %v", err)
	}
	if err := activateInstalledGenerationWithOps(
		t.Context(), "store.db", time.Second, 2, checkpoint,
		func(context.Context, string, time.Duration) (int, error) { return 0, canary },
	); !errors.Is(err, canary) {
		t.Fatalf("activation reopen error = %v", err)
	}
	if err := activateInstalledGenerationWithOps(
		t.Context(), "store.db", time.Second, 2, checkpoint,
		func(context.Context, string, time.Duration) (int, error) { return 1, nil },
	); err == nil {
		t.Fatal("activation accepted wrong version")
	}

	openStage := func(context.Context, string, time.Duration) (*sql.DB, error) {
		return openProviderScript(t,
			providerRow("integrity_check", "ok"),
			providerScriptStep{query: "foreign_key_check", columns: []string{"table"}},
		), nil
	}
	if err := discardStagedGenerationWithOps(
		"stage.db", time.Second,
		func(string) (bool, error) { return false, canary }, openStage,
		func(string) error { return nil }, func(string) error { return nil },
		func(string) error { return nil },
	); !errors.Is(err, canary) {
		t.Fatalf("stage existence error = %v", err)
	}
	if err := discardStagedGenerationWithOps(
		"stage.db", time.Second,
		func(string) (bool, error) { return true, nil },
		func(context.Context, string, time.Duration) (*sql.DB, error) { return nil, canary },
		func(string) error { return nil }, func(string) error { return nil },
		func(string) error { return nil },
	); err == nil {
		t.Fatal("diagnostic stage open failure was not reported")
	}
	if err := discardStagedGenerationWithOps(
		"stage.db", time.Second,
		func(string) (bool, error) { return true, nil }, openStage,
		func(string) error { return canary }, func(string) error { return nil },
		func(string) error { return nil },
	); !errors.Is(err, canary) {
		t.Fatalf("stage sidecar error = %v", err)
	}
	if err := discardStagedGenerationWithOps(
		"stage.db", time.Second,
		func(string) (bool, error) { return true, nil }, openStage,
		func(string) error { return nil }, func(string) error { return canary },
		func(string) error { return nil },
	); !errors.Is(err, canary) {
		t.Fatalf("stage removal error = %v", err)
	}
	if err := discardStagedGenerationWithOps(
		"stage.db", time.Second,
		func(string) (bool, error) { return true, nil }, openStage,
		func(string) error { return nil }, func(string) error { return os.ErrNotExist },
		func(string) error { return canary },
	); !errors.Is(err, canary) {
		t.Fatalf("stage directory sync error = %v", err)
	}
}

func TestStagedGenerationCleanupBoundsIntegrityScan(t *testing.T) {
	database := openProviderScript(t, providerScriptStep{
		query: "integrity_check", waitForContext: true,
	})
	removed := false
	started := time.Now()
	err := discardStagedGenerationWithOps(
		"stage.db", 20*time.Millisecond,
		func(string) (bool, error) { return true, nil },
		func(context.Context, string, time.Duration) (*sql.DB, error) { return database, nil },
		func(string) error { return nil },
		func(string) error { removed = true; return nil },
		func(string) error { return nil },
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded cleanup error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded cleanup elapsed %s", elapsed)
	}
	if removed {
		t.Fatal("timed-out diagnostic stage was removed")
	}
}

func TestStagedGenerationCleanupCapsOpenTimeout(t *testing.T) {
	canary := errors.New("open canary")
	var receivedTimeout time.Duration
	var receivedDeadline time.Time
	err := discardStagedGenerationWithOps(
		"stage.db", time.Minute,
		func(string) (bool, error) { return true, nil },
		func(ctx context.Context, _ string, timeout time.Duration) (*sql.DB, error) {
			receivedTimeout = timeout
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("cleanup open context has no deadline")
			}
			receivedDeadline = deadline
			return nil, canary
		},
		func(string) error { return nil },
		func(string) error { return nil },
		func(string) error { return nil },
	)
	if !errors.Is(err, canary) {
		t.Fatalf("cleanup open error = %v", err)
	}
	if receivedTimeout != maximumStagedCleanupDuration {
		t.Fatalf("cleanup open timeout = %s, want %s", receivedTimeout, maximumStagedCleanupDuration)
	}
	if remaining := time.Until(receivedDeadline); remaining <= 0 || remaining > maximumStagedCleanupDuration {
		t.Fatalf("cleanup open deadline remaining = %s", remaining)
	}
}

func TestCoverageProviderMaintenanceHelperEdges(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.db")
	if _, err := inspectAndRecover(t.Context(), missing, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := exclusiveRollbackBoundary(t.Context(), missing, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := checkpointGeneration(t.Context(), missing, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := reopenAndValidate(t.Context(), missing, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, run := range map[string]func() error{
		"inspect":    func() error { _, err := inspectAndRecover(canceled, missing, time.Second); return err },
		"boundary":   func() error { return exclusiveRollbackBoundary(canceled, missing, time.Second) },
		"checkpoint": func() error { return checkpointGeneration(canceled, missing, time.Second) },
		"reopen":     func() error { _, err := reopenAndValidate(canceled, missing, time.Second); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); err == nil {
				t.Fatal("canceled maintenance helper succeeded")
			}
		})
	}
	unsafe := filepath.Join(t.TempDir(), "directory.db")
	if err := os.Mkdir(unsafe, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := openMaintenanceStore(t.Context(), unsafe, time.Second); err == nil {
		t.Fatal("maintenance opened directory endpoint")
	}
	if err := exclusiveRollbackBoundary(t.Context(), unsafe, time.Second); err == nil {
		t.Fatal("exclusive boundary opened directory endpoint")
	}
	if err := checkpointGeneration(t.Context(), unsafe, time.Second); err == nil {
		t.Fatal("checkpoint opened directory endpoint")
	}
	if _, err := reopenAndValidate(t.Context(), unsafe, time.Second); err == nil {
		t.Fatal("reopen accepted directory endpoint")
	}
}

func TestCoverageStagedGenerationHelperEdges(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "valid.db")
	database, err := openOfflineStage(t.Context(), valid, 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("CREATE TABLE item(id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if err := SetSchemaVersion(t.Context(), database, 1); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateStagedGeneration(t.Context(), valid, time.Second, 1); err != nil {
		t.Fatal(err)
	}
	if err := validateStagedGeneration(t.Context(), valid, time.Second, 2); err == nil {
		t.Fatal("staged generation accepted wrong version")
	}
	if err := activateInstalledGeneration(t.Context(), valid, time.Second, 2); err == nil {
		t.Fatal("activation accepted wrong version")
	}
	if exists, err := regularGenerationExists(valid); err != nil || !exists {
		t.Fatalf("regular generation = %v, %v", exists, err)
	}

	disposable := filepath.Join(root, "disposable.db")
	database, err = openOfflineStage(t.Context(), disposable, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := discardStagedGeneration(disposable, time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(disposable); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disposable generation remains: %v", err)
	}
	directory := filepath.Join(root, "directory.db")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := discardStagedGeneration(directory, time.Second); err == nil {
		t.Fatal("directory stage discarded")
	}
	if _, err := replaceStagedGeneration(filepath.Join(root, "missing-stage"), valid); err == nil {
		t.Fatal("missing stage replaced generation")
	}
	if err := syncStagedMigrationDirectory(filepath.Join(root, "missing-directory")); err == nil {
		t.Fatal("missing staged directory synced")
	}
}

func TestCoverageStagedMigrationFailurePhases(t *testing.T) {
	t.Run("corrupt source snapshot", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "source.db")
		if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := migrateStagedFixture(t.Context(), path, time.Second, 1,
			func(context.Context, string) error { return nil }, acceptStagedValidation)
		if err == nil || !strings.Contains(err.Error(), "snapshot SQLite migration stage") {
			t.Fatalf("corrupt source migration = %v", err)
		}
	})

	t.Run("source changes before normalization", func(t *testing.T) {
		path := createStagedMigrationFixture(t)
		err := migrateStagedFixture(t.Context(), path, time.Second, 1,
			func(ctx context.Context, stage string) error {
				if err := installStagedFixtureTable(ctx, stage); err != nil {
					return err
				}
				return os.WriteFile(path, []byte("corrupt after snapshot"), 0o600)
			}, acceptStagedValidation)
		if err == nil || !strings.Contains(err.Error(), "target changed before cutover") {
			t.Fatalf("changed source migration = %v", err)
		}
	})

	t.Run("stage sidecar", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "target.db")
		err := migrateStagedFixture(t.Context(), path, time.Second, 1,
			func(ctx context.Context, stage string) error {
				if err := installStagedFixtureTable(ctx, stage); err != nil {
					return err
				}
				return os.Mkdir(stage+"-journal", 0o700)
			}, acceptStagedValidation)
		if err == nil {
			t.Fatal("staged migration accepted active stage sidecar")
		}
	})

	t.Run("backup canceled", func(t *testing.T) {
		source := createStagedMigrationFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := backupGenerationToStage(
			ctx, source, filepath.Join(t.TempDir(), "stage.db"), time.Second,
		); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled stage backup = %v", err)
		}
	})

	t.Run("backup corrupt", func(t *testing.T) {
		source := filepath.Join(t.TempDir(), "corrupt.db")
		if err := os.WriteFile(source, []byte("corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := backupGenerationToStage(
			t.Context(), source, filepath.Join(t.TempDir(), "stage.db"), time.Second,
		); err == nil {
			t.Fatal("corrupt stage backup succeeded")
		}
	})

	t.Run("offline stage canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if database, err := openOfflineStage(
			ctx,
			filepath.Join(t.TempDir(), "stage.db"),
			time.Second,
		); err == nil ||
			database != nil {
			if database != nil {
				_ = database.Close()
			}
			t.Fatalf("canceled offline stage = %#v, %v", database, err)
		}
	})
}
