package gitworkspace

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

func TestGitWorkspaceInventorySQLiteFreshSchemaDurabilityAndPermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".git-workspaces")
	manager, err := NewManager(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]os.FileMode{root: 0o700, manager.databasePath(): 0o600} {
		info, statErr := os.Lstat(path)
		if statErr != nil || info.Mode().Perm() != mode || info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("%s mode = %v, %v", path, info.Mode(), statErr)
		}
	}
	database, err := manager.openInventoryDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var journal string
	var foreignKeys, busyTimeout, synchronous, userVersion int
	if queryErr := database.QueryRow(`PRAGMA journal_mode`).Scan(&journal); queryErr != nil {
		t.Fatal(queryErr)
	}
	if queryErr := database.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); queryErr != nil {
		t.Fatal(queryErr)
	}
	if queryErr := database.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); queryErr != nil {
		t.Fatal(queryErr)
	}
	if queryErr := database.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); queryErr != nil {
		t.Fatal(queryErr)
	}
	if queryErr := database.QueryRow(`PRAGMA user_version`).Scan(&userVersion); queryErr != nil {
		t.Fatal(queryErr)
	}
	if journal != "wal" || foreignKeys != 1 || busyTimeout != 5000 || synchronous != 2 || userVersion != 1 {
		t.Fatalf("SQLite contract = journal=%q fk=%d busy=%d sync=%d version=%d",
			journal, foreignKeys, busyTimeout, synchronous, userVersion)
	}
	state, err := loadInventoryState(t.Context(), database)
	if err != nil || state.generation != 0 || len(state.Repositories) != 0 {
		t.Fatalf("fresh inventory = %#v, %v", state, err)
	}
}

func TestGitWorkspaceInventorySQLiteMigratesArchivesAndReopensIdempotently(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".git-workspaces")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 12, 34, 56, 987654321, time.UTC)
	remote := "https://example.invalid/owner/repository.git"
	repositoryID := repoID(remote)
	state := &storeState{
		Version: stateVersion,
		Repositories: map[string]*RepositoryRecord{
			repositoryID: {
				ID: repositoryID, RemoteURL: remote,
				FirstSeenAt: now, LastSeenAt: now.Add(time.Second), LastWorkAt: now.Add(2 * time.Second),
			},
		},
		Workspaces:                 map[string]*WorkspaceRecord{},
		DevelopmentLines:           map[string]*developmentLineRecord{},
		PinnedReservationRotations: map[string][]pinnedReservationRotationRecord{},
	}
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, inventoryLegacyFilename)
	if writeErr := os.WriteFile(legacy, encoded, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	manager, err := NewManager(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	loaded := adversarialCloneInventory(t, manager)
	repository := loaded.Repositories[repositoryID]
	if repository == nil || repository.RemoteURL != state.Repositories[repositoryID].RemoteURL ||
		!repository.FirstSeenAt.Equal(now) || !repository.LastWorkAt.Equal(now.Add(2*time.Second)) {
		t.Fatalf("migrated repository = %#v", repository)
	}
	archive := filepath.Join(root, "legacy-json", inventoryLegacyArchive, inventoryLegacyFilename)
	if _, statErr := os.Stat(legacy); !os.IsNotExist(statErr) {
		t.Fatalf("legacy source remains: %v", statErr)
	}
	archiveInfo, err := os.Stat(archive)
	if err != nil || archiveInfo.Mode().Perm() != 0o600 {
		t.Fatalf("legacy archive mode = %v, %v", archiveInfo.Mode(), err)
	}
	database, err := manager.openInventoryDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var imported, skipped int
	var status string
	if queryErr := database.QueryRow(`SELECT imported_count, skipped_count, archive_status
        FROM storage_imports WHERE component = ? AND source_id = ?`,
		inventoryDatabaseComponent, inventoryLegacySourceID).Scan(&imported, &skipped, &status); queryErr != nil {
		t.Fatal(queryErr)
	}
	_ = database.Close()
	if imported != 1 || skipped != 0 || status != "complete" {
		t.Fatalf("import ledger = imported %d skipped %d status %q", imported, skipped, status)
	}
	if _, reopenErr := NewManager(Options{RootDir: root}); reopenErr != nil {
		t.Fatalf("idempotent reopen: %v", reopenErr)
	}
}

func TestGitWorkspaceInventorySQLiteMigrationFailureRollsBackAndRetries(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".git-workspaces")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, inventoryLegacyFilename)
	if err := os.WriteFile(legacy, []byte(`{"repositories":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(Options{RootDir: root}); err == nil || strings.Contains(err.Error(), "repositories") {
		t.Fatalf("malformed migration error = %v", err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("failed migration removed source: %v", err)
	}
	valid := &storeState{
		Version:                    stateVersion,
		Repositories:               map[string]*RepositoryRecord{},
		Workspaces:                 map[string]*WorkspaceRecord{},
		DevelopmentLines:           map[string]*developmentLineRecord{},
		PinnedReservationRotations: map[string][]pinnedReservationRotationRecord{},
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(legacy, encoded, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, retryErr := NewManager(Options{RootDir: root}); retryErr != nil {
		t.Fatalf("retry valid migration: %v", retryErr)
	}
}

func TestGitWorkspaceInventorySQLiteGenerationCASAndSchemaFences(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".git-workspaces")
	manager, err := NewManager(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	database, err := manager.openInventoryDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	first, err := loadInventoryState(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadInventoryState(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}
	if saveErr := saveInventoryState(t.Context(), database, first); saveErr != nil {
		t.Fatal(saveErr)
	}
	if saveErr := saveInventoryState(t.Context(), database, second); saveErr == nil ||
		!strings.Contains(saveErr.Error(), "generation conflict") {
		t.Fatalf("stale generation save = %v", saveErr)
	}
	if closeErr := database.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	raw, err := sql.Open("sqlite", manager.databasePath())
	if err != nil {
		t.Fatal(err)
	}
	if _, execErr := raw.Exec(`PRAGMA user_version = 2`); execErr != nil {
		t.Fatal(execErr)
	}
	_ = raw.Close()
	if _, reopenErr := NewManager(Options{RootDir: root}); !errors.Is(reopenErr, sqlitestore.ErrTooNew) {
		t.Fatalf("too-new schema error = %v", reopenErr)
	}
}

func TestGitWorkspaceInventorySQLiteRejectsUnexpectedSchemaObjects(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".git-workspaces")
	manager, err := NewManager(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", manager.databasePath())
	if err != nil {
		t.Fatal(err)
	}
	if _, execErr := raw.Exec(`CREATE TABLE injected(value TEXT) STRICT`); execErr != nil {
		t.Fatal(execErr)
	}
	_ = raw.Close()
	if _, reopenErr := NewManager(Options{RootDir: root}); !errors.Is(reopenErr, sqlitestore.ErrInvalidSchema) {
		t.Fatalf("unexpected schema object error = %v", reopenErr)
	}
}

func TestGitWorkspaceInventorySQLiteRejectsLegacySymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".git-workspaces")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "inventory.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, inventoryLegacyFilename)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := NewManager(Options{RootDir: root}); err == nil {
		t.Fatal("legacy inventory symlink was accepted")
	}
}
