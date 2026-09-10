//nolint:govet,golines // Dense failure-boundary fixtures intentionally use narrow scopes.
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

	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

func TestCoverageMaintainOfflineCancellationBoundaries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	base := func() maintenanceOps {
		return maintenanceOps{
			inspect:    func(context.Context, string, time.Duration) (int, error) { return 1, nil },
			boundary:   func(context.Context, string, time.Duration) error { return nil },
			checkpoint: func(context.Context, string, time.Duration) error { return nil },
			reopen:     func(context.Context, string, time.Duration) (int, error) { return 1, nil },
		}
	}
	if result, err := maintainOffline(nil, path, time.Second, base()); err != nil ||
		result.BeforeVersion != 1 || result.AfterVersion != 1 {
		t.Fatalf("nil-context maintenance = %#v, %v", result, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := maintainOffline(canceled, path, time.Second, base()); !errors.Is(err, context.Canceled) {
		t.Fatalf("initial cancellation = %v", err)
	}
	for _, phase := range []string{"inspect", "boundary", "checkpoint"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ops := base()
			switch phase {
			case "inspect":
				ops.inspect = func(context.Context, string, time.Duration) (int, error) {
					cancel()
					return 1, nil
				}
			case "boundary":
				ops.boundary = func(context.Context, string, time.Duration) error {
					cancel()
					return nil
				}
			case "checkpoint":
				ops.checkpoint = func(context.Context, string, time.Duration) error {
					cancel()
					return nil
				}
			}
			if _, err := maintainOffline(ctx, path, time.Second, ops); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation after %s = %v", phase, err)
			}
		})
	}
}

func TestCoverageMaintenanceOpenAndCorruptionBoundaries(t *testing.T) {
	ctx, err := checkedMaintenanceContext(nil)
	if err != nil || ctx == nil {
		t.Fatalf("nil maintenance context = %#v, %v", ctx, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if database, err := openMaintenanceStore(canceled, "unused.db", time.Second); database != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled maintenance open = %#v, %v", database, err)
	}

	directory := filepath.Join(t.TempDir(), "directory.db")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectAndRecover(t.Context(), directory, time.Second); err == nil {
		t.Fatal("inspection recovery opened a directory")
	}
	closed := openProviderScript(t)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := exclusiveRollbackBoundaryDatabase(t.Context(), closed, directory); err == nil {
		t.Fatal("exclusive boundary accepted a closed pool")
	}

	canary := errors.New("maintenance busy classifier canary")
	if err := maintenanceIntegrityWithClassifier(
		t.Context(),
		openProviderScript(t, providerScriptStep{query: "integrity_check", err: canary}),
		func(error) bool { return true },
	); !errors.Is(err, errControlUnavailable) {
		t.Fatalf("busy maintenance integrity = %v", err)
	}

	corruptPath := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(corruptPath, []byte("not a SQLite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	dsn, err := DSN(corruptPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	corrupt, err := open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer corrupt.Close()
	corruptErr := corrupt.PingContext(t.Context())
	if corruptErr == nil || !maintenanceCorruption(corruptErr) {
		t.Fatalf("corrupt provider error = %v", corruptErr)
	}
	if _, err := inspectAndRecoverDatabase(t.Context(), corrupt); !IsMaintenanceIntegrity(err) {
		t.Fatalf("corrupt recovery classification = %v", err)
	}
	if database, err := openMaintenanceStore(t.Context(), corruptPath, time.Second); database != nil || !IsMaintenanceIntegrity(err) {
		if database != nil {
			_ = database.Close()
		}
		t.Fatalf("corrupt maintenance open = %#v, %v", database, err)
	}
	openScript := func(string, time.Duration) (*sql.DB, error) {
		return openProviderScript(t), nil
	}
	if database, err := openMaintenanceStoreWithOps(
		t.Context(), "store.db", time.Second,
		maintenanceOpenOps{
			open: openScript,
			configure: func(context.Context, *sql.DB, time.Duration, bool) error {
				return corruptErr
			},
		},
	); database != nil || !IsMaintenanceIntegrity(err) {
		t.Fatalf("configure corruption = %#v, %v", database, err)
	}
	if database, err := openMaintenanceStoreWithOps(
		t.Context(), "store.db", time.Second,
		maintenanceOpenOps{
			open: openScript,
			configure: func(context.Context, *sql.DB, time.Duration, bool) error {
				return canary
			},
		},
	); database != nil || !errors.Is(err, canary) {
		t.Fatalf("configure failure = %#v, %v", database, err)
	}
	if database, err := openMaintenanceStoreWithOps(
		t.Context(), "store.db", time.Second, maintenanceOpenOps{},
	); database != nil || err == nil {
		t.Fatalf("missing maintenance open operations = %#v, %v", database, err)
	}

	memory, err := OpenStore(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	_, syntaxErr := memory.ExecContext(t.Context(), "not valid SQLite syntax")
	if syntaxErr == nil || maintenanceCorruption(syntaxErr) {
		t.Fatalf("syntax error corruption classification = %v", syntaxErr)
	}
}

func TestMigrateStagedOfflineFromUsesDistinctBackupSource(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(home, "backup-source.db")
	target := filepath.Join(home, "live-target.db")
	for _, path := range []string{source, target} {
		createOfflineFixtureAt(t, path)
	}
	lease := newOfflineProviderTestLease(t, target, &offlineProviderHookRecorder{})
	immutable := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, source)
	})
	if _, err := MigrateStagedOfflineFrom(
		t.Context(), lease, immutable, time.Second, 1,
		installStagedFixtureTable, validateInstalledFixture,
	); err != nil {
		t.Fatal(err)
	}
	ready, err := testSchemaObjects(t.Context(), target, "original_marker", "installed")
	if err != nil || !ready {
		t.Fatalf("installed target ready=%t err=%v", ready, err)
	}
	sourceReady, err := testSchemaObjects(t.Context(), source, "original_marker")
	if err != nil || !sourceReady {
		t.Fatalf("backup source changed or unreadable: ready=%t err=%v", sourceReady, err)
	}
}

func TestMigrateStagedOfflineFromRejectsPhysicalSourceAliasAndNewerVersion(t *testing.T) {
	t.Run("physical alias", func(t *testing.T) {
		home := t.TempDir()
		target := filepath.Join(home, "live.db")
		source := filepath.Join(home, "backup.db")
		createProviderOfflineFixture(t, target)
		if err := os.Link(target, source); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		lease := newOfflineProviderTestLease(t, target, &offlineProviderHookRecorder{})
		immutable := immutableGenerationSourceForTest(func(
			ctx context.Context,
			use func(context.Context, string) error,
		) error {
			return use(ctx, source)
		})
		if _, err := MigrateStagedOfflineFrom(
			t.Context(), lease, immutable, time.Second, 1,
			installStagedFixtureTable, validateInstalledFixture,
		); err == nil || !strings.Contains(err.Error(), "physically aliases") {
			t.Fatalf("physical source alias error = %v", err)
		}
	})

	t.Run("newer source", func(t *testing.T) {
		home := t.TempDir()
		target := filepath.Join(home, "live.db")
		source := filepath.Join(home, "backup.db")
		createProviderOfflineFixture(t, target)
		createProviderOfflineFixture(t, source)
		database, err := openOfflineStage(t.Context(), source, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if err := SetSchemaVersion(t.Context(), database, 2); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		migrateCalled := false
		lease := newOfflineProviderTestLease(t, target, &offlineProviderHookRecorder{})
		immutable := immutableGenerationSourceForTest(func(
			ctx context.Context,
			use func(context.Context, string) error,
		) error {
			return use(ctx, source)
		})
		result, err := MigrateStagedOfflineFrom(
			t.Context(), lease, immutable, 5*time.Second, 1,
			func(context.Context, string) error {
				migrateCalled = true
				return nil
			},
			acceptStagedValidation,
		)
		if err == nil || !strings.Contains(err.Error(), "newer than expected") ||
			result.BeforeVersion != 2 || migrateCalled {
			t.Fatalf("newer source result=%#v called=%t error=%v", result, migrateCalled, err)
		}
	})
}

func TestCoverageStagedMigrationRemainingPreCutoverBoundaries(t *testing.T) {
	validOps := stagedMigrationOps{
		replace:  func(string, string) (bool, error) { return true, nil },
		activate: func(context.Context, string, time.Duration, int) error { return nil },
	}
	validMigration := func(ctx context.Context, stage string) error {
		database, err := openOfflineStage(ctx, stage, time.Second)
		if err != nil {
			return err
		}
		defer database.Close()
		return SetSchemaVersion(ctx, database, 1)
	}
	target := filepath.Join(t.TempDir(), "target.db")
	if err := migrateStagedOffline(
		t.Context(), "", target, time.Second, 1,
		validMigration, acceptStagedValidation, validOps,
	); err == nil {
		t.Fatal("staged migration accepted empty source")
	}
	t.Run("public API rejects identical source and target", func(t *testing.T) {
		home := t.TempDir()
		if err := os.Chmod(home, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(home, "store.db")
		lease := newOfflineProviderTestLease(t, path, &offlineProviderHookRecorder{})
		immutable := immutableGenerationSourceForTest(func(
			ctx context.Context,
			use func(context.Context, string) error,
		) error {
			return use(ctx, path)
		})
		if _, err := MigrateStagedOfflineFrom(
			t.Context(), lease, immutable, time.Second, 1,
			validMigration, acceptStagedValidation,
		); err == nil {
			t.Fatal("public staged migration accepted identical source and target")
		}
	})

	t.Run("source and target existence differ", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target.db")
		createOfflineFixtureAt(t, target)
		if err := migrateStagedOffline(
			t.Context(), filepath.Join(root, "missing-source.db"), target,
			time.Second, 1, validMigration, acceptStagedValidation, validOps,
		); err == nil || !strings.Contains(err.Error(), "existence differ") {
			t.Fatalf("existence mismatch = %v", err)
		}
	})

	t.Run("unsafe source ancestor", func(t *testing.T) {
		root := t.TempDir()
		realDirectory := filepath.Join(root, "real")
		if err := os.Mkdir(realDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		alias := filepath.Join(root, "alias")
		if err := os.Symlink(realDirectory, alias); err != nil {
			t.Fatal(err)
		}
		if err := migrateStagedOffline(
			t.Context(), filepath.Join(alias, "source.db"), filepath.Join(root, "target.db"),
			time.Second, 1, validMigration, acceptStagedValidation, validOps,
		); err == nil {
			t.Fatal("staged migration accepted source symlink ancestor")
		}
	})

	t.Run("unsafe source generation", func(t *testing.T) {
		root := t.TempDir()
		source := filepath.Join(root, "source.db")
		if err := os.Mkdir(source, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := migrateStagedOffline(
			t.Context(), source, filepath.Join(root, "target.db"), time.Second, 1,
			validMigration, acceptStagedValidation, validOps,
		); err == nil {
			t.Fatal("staged migration accepted source directory")
		}
	})

	t.Run("stage removed by contract validation", func(t *testing.T) {
		root := t.TempDir()
		err := migrateStagedOffline(
			t.Context(), filepath.Join(root, "source.db"), filepath.Join(root, "target.db"),
			time.Second, 1, validMigration,
			func(_ context.Context, stage string) error { return os.Remove(stage) }, validOps,
		)
		if err == nil || !strings.Contains(err.Error(), "changed during domain validation") {
			t.Fatalf("removed stage error = %v", err)
		}
	})

	t.Run("target appears after validation", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target.db")
		err := migrateStagedOffline(
			t.Context(), filepath.Join(root, "source.db"), target,
			time.Second, 1, validMigration,
			func(context.Context, string) error { return os.WriteFile(target, nil, 0o600) }, validOps,
		)
		if err == nil || !strings.Contains(err.Error(), "target appeared") {
			t.Fatalf("appeared target error = %v", err)
		}
	})

	unknown := dblayer.NewError(dblayer.CodeOutcomeUnknown, "uncertain")
	if sanitized := sanitizePreCutoverError(unknown); sanitized == nil ||
		dblayer.CodeOf(sanitized) == dblayer.CodeOutcomeUnknown {
		t.Fatalf("pre-cutover outcome was not sanitized: %v", sanitized)
	}
	tooLong := filepath.Join(t.TempDir(), strings.Repeat("x", 4096))
	if available, err := stagedGenerationNamespaceAvailable(tooLong); err == nil || available {
		t.Fatalf("overlong staged namespace = %t, %v", available, err)
	}
	existingNamespace := filepath.Join(t.TempDir(), "existing-stage.db")
	if err := os.WriteFile(existingNamespace, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if available, err := stagedGenerationNamespaceAvailable(existingNamespace); err != nil || available {
		t.Fatalf("existing staged namespace = %t, %v", available, err)
	}
}

func TestCoverageBackupRejectsInvalidStageAndReferentialCorruption(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	createOfflineFixtureAt(t, source)
	stageDirectory := filepath.Join(root, "stage.db")
	if err := os.Mkdir(stageDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := backupGenerationToStage(t.Context(), source, stageDirectory, time.Second); err == nil {
		t.Fatal("backup accepted directory stage")
	}

	violating := filepath.Join(t.TempDir(), "violating.db")
	database, err := OpenStore(violating, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), "PRAGMA foreign_keys = OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), `
		CREATE TABLE parent(id INTEGER PRIMARY KEY);
		CREATE TABLE child(parent_id INTEGER REFERENCES parent(id));
		INSERT INTO child(parent_id) VALUES (99);
	`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := backupGenerationToStage(
		t.Context(), violating, filepath.Join(t.TempDir(), "stage.db"), time.Second,
	); !IsMaintenanceIntegrity(err) {
		t.Fatalf("referentially corrupt backup = %v", err)
	}

	validSource := filepath.Join(t.TempDir(), "backup-source.db")
	createOfflineFixtureAt(t, validSource)
	database, err = OpenStore(validSource, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	invalidDestination := "file:" + filepath.Join(t.TempDir(), "missing", "stage.db") + "?mode=rw"
	if err := backupGenerationConnection(t.Context(), connection, invalidDestination); err == nil {
		t.Fatal("online backup accepted unavailable destination")
	}
}

func TestValidateStagedGenerationRejectsInvalidSchemaControl(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid-version.db")
	database, err := openOfflineStage(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), "PRAGMA main.user_version = -1"); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateStagedGeneration(t.Context(), path, time.Second, 1); err == nil {
		t.Fatal("staged generation accepted invalid schema control")
	}
}

func createOfflineFixtureAt(t *testing.T, path string) {
	t.Helper()
	database, err := openOfflineStage(t.Context(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), "CREATE TABLE original_marker(value TEXT)"); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := SetSchemaVersion(t.Context(), database, 0); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}
