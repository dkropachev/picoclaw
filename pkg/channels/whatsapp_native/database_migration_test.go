//go:build whatsapp_native

package whatsapp

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

func TestMigrateWhatsAppDatabaseInstallsCurrentLibrarySchema(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "whatsapp", "store.db")
	fence := acquireWhatsAppMigrationFence(t, home)
	defer fence.Close()
	if err := MigrateDatabase(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	ready, err := sqliteprovider.HasSchemaObjects(
		t.Context(), path, 5*time.Second, "whatsmeow_version", "whatsmeow_device",
	)
	if err != nil || !ready {
		t.Fatalf("WhatsApp migrated schema ready=%t error=%v", ready, err)
	}
	maintenance, err := sqliteprovider.MaintainOffline(t.Context(), path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if maintenance.AfterVersion != 1 {
		t.Fatalf("WhatsApp installed version = %d", maintenance.AfterVersion)
	}
}

func TestMigrateWhatsAppDatabaseFailureLeavesOriginalGeneration(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "whatsapp", "store.db")
	fence := acquireWhatsAppMigrationFence(t, home)
	defer fence.Close()
	createWhatsAppMigrationOriginal(t, path)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected after WhatsApp library upgrade")
	originalHook := whatsappMigrationCheckpoint
	whatsappMigrationCheckpoint = func(phase string) error {
		if phase == whatsappMigrationAfterUpgrade {
			return injected
		}
		return nil
	}
	t.Cleanup(func() { whatsappMigrationCheckpoint = originalHook })
	if err := MigrateDatabase(t.Context(), path); !errors.Is(err, injected) {
		t.Fatalf("WhatsApp migration error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed WhatsApp migration changed original bytes")
	}
	ready, err := sqliteprovider.HasSchemaObjects(t.Context(), path, 5*time.Second, "original_marker")
	if err != nil || !ready {
		t.Fatalf("original WhatsApp marker ready=%t err=%v", ready, err)
	}
	upgraded, err := sqliteprovider.HasSchemaObjects(t.Context(), path, 5*time.Second, "whatsmeow_version")
	if err != nil || upgraded {
		t.Fatalf("partial WhatsApp schema visible=%t err=%v", upgraded, err)
	}
	assertNoWhatsAppGenerationSidecars(t, path)
}

func TestMigrateWhatsAppDatabaseCrashBeforeCutoverLeavesOriginalGeneration(t *testing.T) {
	if os.Getenv("PICOCLAW_WHATSAPP_MIGRATION_CRASH_HELPER") == "1" {
		home := os.Getenv("PICOCLAW_WHATSAPP_MIGRATION_HOME")
		path := os.Getenv("PICOCLAW_WHATSAPP_MIGRATION_PATH")
		fence, err := database.AcquireMigrationFence(home)
		if err != nil {
			os.Exit(91)
		}
		defer fence.Close()
		whatsappMigrationCheckpoint = func(phase string) error {
			if phase == whatsappMigrationAfterVersion {
				os.Exit(92)
			}
			return nil
		}
		_ = MigrateDatabase(t.Context(), path)
		os.Exit(93)
	}

	home := t.TempDir()
	path := filepath.Join(home, "whatsapp", "store.db")
	fence := acquireWhatsAppMigrationFence(t, home)
	createWhatsAppMigrationOriginal(t, path)
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(
		os.Args[0],
		"-test.run=^TestMigrateWhatsAppDatabaseCrashBeforeCutoverLeavesOriginalGeneration$",
	)
	command.Env = append(os.Environ(),
		"PICOCLAW_WHATSAPP_MIGRATION_CRASH_HELPER=1",
		"PICOCLAW_WHATSAPP_MIGRATION_HOME="+home,
		"PICOCLAW_WHATSAPP_MIGRATION_PATH="+path,
	)
	err = command.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 92 {
		t.Fatalf("WhatsApp crash helper error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("pre-cutover WhatsApp crash changed original bytes")
	}
	assertNoWhatsAppGenerationSidecars(t, path)
}

func assertNoWhatsAppGenerationSidecars(t *testing.T, path string) {
	t.Helper()
	for _, sidecar := range []string{path + "-wal", path + "-shm", path + "-journal"} {
		if _, err := os.Lstat(sidecar); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("original WhatsApp sidecar changed: %s: %v", sidecar, err)
		}
	}
}

func TestMigrateWhatsAppDatabaseRequiresExclusiveFence(t *testing.T) {
	err := MigrateDatabase(t.Context(), filepath.Join(t.TempDir(), "store.db"))
	if database.CodeOf(err) != database.CodeConflict ||
		!strings.Contains(err.Error(), "exclusive") {
		t.Fatalf("unfenced WhatsApp migration error = %v", err)
	}
}

func acquireWhatsAppMigrationFence(t *testing.T, home string) *database.Fence {
	t.Helper()
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	return fence
}

func createWhatsAppMigrationOriginal(t *testing.T, path string) {
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
