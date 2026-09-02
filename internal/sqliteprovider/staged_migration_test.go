package sqliteprovider

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

func TestStagedMigrationFailureLeavesOriginalBytes(t *testing.T) {
	path := createStagedMigrationFixture(t)
	before, beforeReadErr := os.ReadFile(path)
	if beforeReadErr != nil {
		t.Fatal(beforeReadErr)
	}
	injected := errors.New("injected after independent commit")
	migrationErr := MigrateStagedOffline(t.Context(), path, 5*time.Second, 1,
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
		})
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
	ready, schemaErr := HasSchemaObjects(t.Context(), path, 5*time.Second, "original_marker")
	if schemaErr != nil || !ready {
		t.Fatalf("original marker ready=%t err=%v", ready, schemaErr)
	}
}

func TestStagedMigrationPanicBeforeCutoverLeavesOriginalBytes(t *testing.T) {
	path := createStagedMigrationFixture(t)
	before, beforeReadErr := os.ReadFile(path)
	if beforeReadErr != nil {
		t.Fatal(beforeReadErr)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("migration panic was not observed")
			}
		}()
		_ = MigrateStagedOffline(t.Context(), path, 5*time.Second, 1,
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
			})
	}()
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
	err := MigrateStagedOffline(t.Context(), path, 5*time.Second, 1,
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
		})
	if err != nil {
		t.Fatal(err)
	}
	ready, err := HasSchemaObjects(t.Context(), path, 5*time.Second, "original_marker", "installed")
	if err != nil || !ready {
		t.Fatalf("installed generation ready=%t err=%v", ready, err)
	}
	maintenance, err := MaintainOffline(t.Context(), path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if maintenance.AfterVersion != 1 {
		t.Fatalf("installed version = %d", maintenance.AfterVersion)
	}
}

func TestStagedMigrationActivationFailureIsOutcomeUnknown(t *testing.T) {
	path := createStagedMigrationFixture(t)
	originalActivation := stagedGenerationActivation
	stagedGenerationActivation = func(context.Context, string, time.Duration, int) error {
		return errors.New("injected activation failure")
	}
	t.Cleanup(func() { stagedGenerationActivation = originalActivation })

	err := MigrateStagedOffline(t.Context(), path, 5*time.Second, 1, installStagedFixtureTable)
	if dblayer.CodeOf(err) != dblayer.CodeOutcomeUnknown {
		t.Fatalf("activation failure code = %s, error = %v", dblayer.CodeOf(err), err)
	}
	ready, inspectErr := HasSchemaObjects(t.Context(), path, 5*time.Second, "installed")
	if inspectErr != nil || !ready {
		t.Fatalf("installed generation ready=%t err=%v", ready, inspectErr)
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
