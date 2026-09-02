package matrix

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestMigrateCryptoDatabaseInstallsCurrentLibrarySchemas(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "matrix", "store.db")
	fence := acquireMatrixMigrationFence(t, home)
	defer fence.Close()
	if err := MigrateCryptoDatabase(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	ready, err := sqliteprovider.HasSchemaObjects(
		t.Context(), path, 5*time.Second, "crypto_version", "mx_version", "crypto_account",
	)
	if err != nil || !ready {
		t.Fatalf("Matrix migrated schema ready=%t error=%v", ready, err)
	}
	maintenance, err := sqliteprovider.MaintainOffline(t.Context(), path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if maintenance.AfterVersion != 1 {
		t.Fatalf("Matrix installed version = %d", maintenance.AfterVersion)
	}
}

func TestMigrateCryptoDatabaseFailureLeavesOriginalGeneration(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "matrix", "store.db")
	fence := acquireMatrixMigrationFence(t, home)
	defer fence.Close()
	createMatrixMigrationOriginal(t, path)
	before, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	injected := errors.New("injected after Matrix state upgrade")
	originalHook := matrixMigrationCheckpoint
	matrixMigrationCheckpoint = func(phase string) error {
		if phase == matrixMigrationAfterState {
			return injected
		}
		return nil
	}
	t.Cleanup(func() { matrixMigrationCheckpoint = originalHook })
	if err := MigrateCryptoDatabase(t.Context(), path); !errors.Is(err, injected) {
		t.Fatalf("Matrix migration error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed Matrix migration changed original bytes")
	}
	ready, err := sqliteprovider.HasSchemaObjects(t.Context(), path, 5*time.Second, "original_marker")
	if err != nil || !ready {
		t.Fatalf("original Matrix marker ready=%t err=%v", ready, err)
	}
	upgraded, err := sqliteprovider.HasSchemaObjects(t.Context(), path, 5*time.Second, "mx_version")
	if err != nil || upgraded {
		t.Fatalf("partial Matrix schema visible=%t err=%v", upgraded, err)
	}
	assertNoMatrixGenerationSidecars(t, path)
}

func TestMigrateCryptoDatabaseCrashBeforeCutoverLeavesOriginalGeneration(t *testing.T) {
	if os.Getenv("PICOCLAW_MATRIX_MIGRATION_CRASH_HELPER") == "1" {
		home := os.Getenv("PICOCLAW_MATRIX_MIGRATION_HOME")
		path := os.Getenv("PICOCLAW_MATRIX_MIGRATION_PATH")
		fence, err := database.AcquireMigrationFence(home)
		if err != nil {
			os.Exit(81)
		}
		defer fence.Close()
		matrixMigrationCheckpoint = func(phase string) error {
			if phase == matrixMigrationAfterVersion {
				os.Exit(82)
			}
			return nil
		}
		_ = MigrateCryptoDatabase(t.Context(), path)
		os.Exit(83)
	}

	home := t.TempDir()
	path := filepath.Join(home, "matrix", "store.db")
	fence := acquireMatrixMigrationFence(t, home)
	createMatrixMigrationOriginal(t, path)
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(
		os.Args[0],
		"-test.run=^TestMigrateCryptoDatabaseCrashBeforeCutoverLeavesOriginalGeneration$",
	)
	command.Env = append(os.Environ(),
		"PICOCLAW_MATRIX_MIGRATION_CRASH_HELPER=1",
		"PICOCLAW_MATRIX_MIGRATION_HOME="+home,
		"PICOCLAW_MATRIX_MIGRATION_PATH="+path,
	)
	err = command.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 82 {
		t.Fatalf("Matrix crash helper error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("pre-cutover Matrix crash changed original bytes")
	}
	assertNoMatrixGenerationSidecars(t, path)
}

func assertNoMatrixGenerationSidecars(t *testing.T, path string) {
	t.Helper()
	for _, sidecar := range []string{path + "-wal", path + "-shm", path + "-journal"} {
		if _, err := os.Lstat(sidecar); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("original Matrix sidecar changed: %s: %v", sidecar, err)
		}
	}
}

func TestMigrateCryptoDatabaseRequiresExclusiveFence(t *testing.T) {
	err := MigrateCryptoDatabase(t.Context(), filepath.Join(t.TempDir(), "store.db"))
	if database.CodeOf(err) != database.CodeConflict ||
		!strings.Contains(err.Error(), "exclusive") {
		t.Fatalf("unfenced Matrix migration error = %v", err)
	}
}

func acquireMatrixMigrationFence(t *testing.T, home string) *database.Fence {
	t.Helper()
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	return fence
}

func createMatrixMigrationOriginal(t *testing.T, path string) {
	t.Helper()
	db, err := sqliteprovider.OpenStore(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := sqliteprovider.ConfigureOffline(t.Context(), db, 5*time.Second); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), "CREATE TABLE original_marker(value TEXT)"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := sqliteprovider.SetSchemaVersion(t.Context(), db, 0); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
