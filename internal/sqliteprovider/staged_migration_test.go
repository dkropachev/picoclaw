package sqliteprovider

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/database"
)

func migrateStagedFixture(
	ctx context.Context,
	path string,
	busyTimeout time.Duration,
	expectedVersion int,
	migrate StagedMigration,
	validate StagedValidation,
) error {
	return migrateStagedOffline(
		ctx,
		path,
		path,
		busyTimeout,
		expectedVersion,
		migrate,
		validate,
		stagedMigrationOps{
			replace: replaceStagedGeneration, activate: activateInstalledGeneration,
		},
	)
}

func TestStagedMigrationFailureLeavesOriginalBytes(t *testing.T) {
	path := createStagedMigrationFixture(t)
	before, beforeReadErr := os.ReadFile(path)
	if beforeReadErr != nil {
		t.Fatal(beforeReadErr)
	}
	injected := errors.New("injected after independent commit")
	migrationErr := migrateStagedFixture(t.Context(), path, 5*time.Second, 1,
		func(ctx context.Context, stage string) error {
			database, err := openOfflineStage(ctx, stage, 5*time.Second)
			if err != nil {
				return err
			}
			defer database.Close()
			if _, err := database.ExecContext(ctx, "CREATE TABLE partial_commit(value TEXT)"); err != nil {
				return err
			}
			return injected
		}, validateInstalledFixture)
	if !errors.Is(migrationErr, injected) {
		t.Fatalf("migration error = %v", migrationErr)
	}
	after, afterReadErr := os.ReadFile(path)
	if afterReadErr != nil {
		t.Fatal(afterReadErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed staged migration changed original database bytes")
	}
	ready, schemaErr := testSchemaObjects(t.Context(), path, "original_marker")
	if schemaErr != nil || !ready {
		t.Fatalf("original marker ready=%t err=%v", ready, schemaErr)
	}
}

func TestStagedMigrationPanicBeforeCutoverReturnsErrorAndLeavesOriginalBytes(t *testing.T) {
	path := createStagedMigrationFixture(t)
	before, beforeReadErr := os.ReadFile(path)
	if beforeReadErr != nil {
		t.Fatal(beforeReadErr)
	}
	migrationErr := migrateStagedFixture(t.Context(), path, 5*time.Second, 1,
		func(ctx context.Context, stage string) error {
			database, err := openOfflineStage(ctx, stage, 5*time.Second)
			if err != nil {
				return err
			}
			defer database.Close()
			if _, err := database.ExecContext(ctx, "CREATE TABLE committed_before_crash(value TEXT)"); err != nil {
				return err
			}
			if err := SetSchemaVersion(ctx, database, 1); err != nil {
				return err
			}
			panic("injected crash before cutover")
		}, validateInstalledFixture)
	if migrationErr == nil || database.CodeOf(migrationErr) == database.CodeOutcomeUnknown {
		t.Fatalf("pre-cutover panic error = %v", migrationErr)
	}
	after, afterReadErr := os.ReadFile(path)
	if afterReadErr != nil {
		t.Fatal(afterReadErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("pre-cutover panic changed original database bytes")
	}
}

func TestStagedMigrationCutoverReopensAndValidates(t *testing.T) {
	path := createStagedMigrationFixture(t)
	err := migrateStagedFixture(t.Context(), path, 5*time.Second, 1,
		func(ctx context.Context, stage string) error {
			database, err := openOfflineStage(ctx, stage, 5*time.Second)
			if err != nil {
				return err
			}
			defer database.Close()
			if _, err := database.ExecContext(ctx, `
				CREATE TABLE installed(value TEXT NOT NULL);
				INSERT INTO installed(value) VALUES ('ready');
			`); err != nil {
				return err
			}
			return SetSchemaVersion(ctx, database, 1)
		}, validateInstalledFixture)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := testSchemaObjects(t.Context(), path, "original_marker", "installed")
	if err != nil || !ready {
		t.Fatalf("installed generation ready=%t err=%v", ready, err)
	}
	maintenance, err := maintainOfflineFixture(t, t.Context(), path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if maintenance.AfterVersion != 1 {
		t.Fatalf("installed version = %d", maintenance.AfterVersion)
	}
}

func TestStagedMigrationRejectsWrongVersionBeforeCutover(t *testing.T) {
	path := createStagedMigrationFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	err = migrateStagedFixture(t.Context(), path, time.Second, 1,
		func(ctx context.Context, stage string) error {
			db, openErr := openOfflineStage(ctx, stage, time.Second)
			if openErr != nil {
				return openErr
			}
			defer db.Close()
			if _, execErr := db.ExecContext(ctx, "CREATE TABLE installed(value TEXT)"); execErr != nil {
				return execErr
			}
			return SetSchemaVersion(ctx, db, 2)
		}, validateInstalledFixture)
	if err == nil {
		t.Fatal("wrong staged version was accepted")
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("wrong staged version changed original bytes")
	}
}

func TestStagedMigrationRejectsActiveSourceSidecarBeforeOpen(t *testing.T) {
	path := createStagedMigrationFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"-wal", []byte("active"), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	err = migrateStagedFixture(t.Context(), path, time.Second, 1,
		func(context.Context, string) error {
			called = true
			return nil
		}, validateInstalledFixture)
	if err == nil || called {
		t.Fatalf("active source sidecar migration called=%t err=%v", called, err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("active source rejection changed original bytes")
	}
}

func TestStagedMigrationReplacementFailureLeavesOriginalBytes(t *testing.T) {
	path := createStagedMigrationFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("replace failed")
	err = migrateStagedOffline(
		t.Context(), path, path, time.Second, 1,
		installStagedFixtureTable, validateInstalledFixture,
		stagedMigrationOps{
			replace:  func(string, string) (bool, error) { return false, injected },
			activate: activateInstalledGeneration,
		},
	)
	if !errors.Is(err, injected) || database.CodeOf(err) == database.CodeOutcomeUnknown {
		t.Fatalf("replacement failure = %v", err)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed replacement changed original bytes")
	}
}

func TestStagedMigrationPostReplacementFailuresAreUnknown(t *testing.T) {
	tests := []struct {
		name string
		ops  func(error) stagedMigrationOps
	}{
		{
			name: "durability",
			ops: func(injected error) stagedMigrationOps {
				return stagedMigrationOps{
					replace: func(stage, target string) (bool, error) {
						if err := os.Rename(stage, target); err != nil {
							return false, err
						}
						return true, injected
					},
					activate: activateInstalledGeneration,
				}
			},
		},
		{
			name: "activation",
			ops: func(injected error) stagedMigrationOps {
				return stagedMigrationOps{
					replace: replaceStagedGeneration,
					activate: func(context.Context, string, time.Duration, int) error {
						return injected
					},
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := createStagedMigrationFixture(t)
			injected := errors.New("post-replacement failure")
			err := migrateStagedOffline(
				t.Context(), path, path, time.Second, 1,
				installStagedFixtureTable, validateInstalledFixture, test.ops(injected),
			)
			if database.CodeOf(err) != database.CodeOutcomeUnknown {
				t.Fatalf("post-replacement failure = %v", err)
			}
			if ready, readyErr := testSchemaObjects(
				t.Context(), path, "original_marker", "installed",
			); readyErr != nil || !ready {
				t.Fatalf("installed stage ready=%t err=%v", ready, readyErr)
			}
		})
	}
}

func installStagedFixtureTable(ctx context.Context, stage string) error {
	database, err := openOfflineStage(ctx, stage, 5*time.Second)
	if err != nil {
		return err
	}
	defer database.Close()
	if _, err := database.ExecContext(ctx, "CREATE TABLE installed(value TEXT NOT NULL)"); err != nil {
		return err
	}
	return SetSchemaVersion(ctx, database, 1)
}

func validateInstalledFixture(ctx context.Context, stage string) error {
	ready, err := testSchemaObjects(ctx, stage, "original_marker", "installed")
	if err != nil {
		return err
	}
	if !ready {
		return errors.New("staged fixture contract is incomplete")
	}
	return nil
}

func testSchemaObjects(ctx context.Context, path string, names ...string) (bool, error) {
	inspection, err := Inspect(ctx, path, 5*time.Second)
	if err != nil {
		return false, err
	}
	defer inspection.Release()
	return inspection.HasSchemaObjects(ctx, "table", names...)
}

func createStagedMigrationFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	database, err := openOfflineStage(context.Background(), path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("CREATE TABLE original_marker(value TEXT)"); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := SetSchemaVersion(context.Background(), database, 0); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
