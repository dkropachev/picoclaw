//go:build integration

package gitworkspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestIntegrationRuntimeOwnedJSONLegacyGitInventoryRelations complements the
// aggregate storage fixture with package-private Git controller relationships.
// It serializes a real repository/workspace/development-line/rotation/history
// generation, removes only its disposable SQLite authority to model an old
// installation, and proves first-open relational import plus idempotent reopen.
//
//nolint:govet // Integration assertions intentionally keep sequential errors at exact boundaries.
func TestIntegrationRuntimeOwnedJSONLegacyGitInventoryRelations(t *testing.T) {
	if os.Getenv("PICOCLAW_STORAGE_JSON_ALLOWLIST_SUITE") == "" {
		t.Skip("runtime storage integration suite is not enabled")
	}
	ctx := context.Background()
	fixture := newPinnedLineTestFixture(t, "pr-development/legacy-relations-old")
	rotationRequest := boundPinnedReservationRotationRequest(
		fixture,
		"pdrr_legacy_relations",
		"pr-development/legacy-relations-new",
	)
	rotated, err := fixture.manager.RotatePinnedReservation(ctx, rotationRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !rotated.Bound || rotated.AlreadyRotated {
		t.Fatalf("seed rotation = %#v", rotated)
	}
	wantState := adversarialCloneInventory(t, fixture.manager)
	wantJSON, err := json.Marshal(wantState)
	if err != nil {
		t.Fatal(err)
	}
	root := fixture.manager.rootDir
	databasePath := fixture.manager.databasePath()
	for _, candidate := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	legacyPath := filepath.Join(root, inventoryLegacyFilename)
	if err := os.WriteFile(legacyPath, wantJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewManager(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyRelationalInventoryEquivalent(t, reopened, wantJSON)
	archive := filepath.Join(root, "legacy-json", inventoryLegacyArchive, inventoryLegacyFilename)
	if _, err := os.Lstat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy relational inventory remains: %v", err)
	}
	archived, err := os.ReadFile(archive)
	if err != nil || !bytes.Equal(archived, wantJSON) {
		t.Fatalf("legacy relational inventory archive = %d bytes err=%v", len(archived), err)
	}
	archiveIdentity, err := os.Stat(archive)
	if err != nil || archiveIdentity.Mode().Perm() != 0o600 {
		t.Fatalf("legacy relational inventory archive mode = %#v err=%v", archiveIdentity, err)
	}

	database, err := reopened.openInventoryDatabase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for table, want := range map[string]int{
		"inventory_repositories":                 1,
		"inventory_workspaces":                   1,
		"inventory_repository_workspace_order":   1,
		"inventory_workspace_locks":              1,
		"inventory_development_lines":            1,
		"inventory_reservation_rotations":        1,
		"inventory_development_line_suspensions": 0,
	} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != want {
			_ = database.Close()
			t.Fatalf("%s rows = %d err=%v; want %d", table, count, err, want)
		}
	}
	var historyCount, imported, skipped, horizon int
	if err := database.QueryRow(`SELECT COUNT(*) FROM inventory_history`).Scan(&historyCount); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if historyCount != len(wantState.History)+len(wantState.DevelopmentLineHistory) {
		_ = database.Close()
		t.Fatalf("inventory history rows = %d, want %d", historyCount,
			len(wantState.History)+len(wantState.DevelopmentLineHistory))
	}
	if err := database.QueryRow(`SELECT imported_count, skipped_count FROM storage_imports
		WHERE component=? AND source_id=?`, inventoryDatabaseComponent, inventoryLegacySourceID).Scan(
		&imported, &skipped,
	); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM storage_import_horizons
		WHERE component=?`, inventoryDatabaseComponent).Scan(&horizon); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if imported != inventoryStateRecordCount(wantState) || skipped != 0 || horizon != 1 {
		t.Fatalf("relational import audit = imported:%d skipped:%d horizon:%d; want %d/0/1",
			imported, skipped, horizon, inventoryStateRecordCount(wantState))
	}
	ledgerBefore := legacyRelationalInventoryLedger(t, reopened)

	second, err := NewManager(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	assertLegacyRelationalInventoryEquivalent(t, second, wantJSON)
	secondArchive, err := os.ReadFile(archive)
	if err != nil || !bytes.Equal(secondArchive, wantJSON) {
		t.Fatalf("relational archive changed on reopen: %d bytes err=%v", len(secondArchive), err)
	}
	if _, err := os.Lstat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy relational source reappeared on second open: %v", err)
	}
	secondArchiveIdentity, err := os.Stat(archive)
	if err != nil || secondArchiveIdentity.Mode().Perm() != 0o600 ||
		!os.SameFile(archiveIdentity, secondArchiveIdentity) {
		t.Fatalf("relational archive identity/mode changed: %#v err=%v", secondArchiveIdentity, err)
	}
	if ledgerAfter := legacyRelationalInventoryLedger(t, second); ledgerAfter != ledgerBefore {
		t.Fatalf("relational import ledger changed on reopen:\nbefore=%s\nafter=%s",
			ledgerBefore, ledgerAfter)
	}
}

func assertLegacyRelationalInventoryEquivalent(t *testing.T, manager *Manager, wantJSON []byte) {
	t.Helper()
	got := adversarialCloneInventory(t, manager)
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("relational legacy inventory changed:\ngot=%s\nwant=%s", gotJSON, wantJSON)
	}
}

func legacyRelationalInventoryLedger(t *testing.T, manager *Manager) string {
	t.Helper()
	database, err := manager.openInventoryDatabase(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var component, sourceID, relative, digest, status string
	var size, limit, mode, imported, skipped, importedAt, archivedAt int64
	if err := database.QueryRow(`SELECT component, source_id, source_relative,
		hex(source_digest), source_size, source_limit, source_mode, imported_count,
		skipped_count, archive_status, imported_at, archived_at FROM storage_imports`).Scan(
		&component, &sourceID, &relative, &digest, &size, &limit, &mode, &imported,
		&skipped, &status, &importedAt, &archivedAt,
	); err != nil {
		t.Fatal(err)
	}
	var horizonComponent string
	var completedAt int64
	var horizonRows int
	if err := database.QueryRow(`SELECT COUNT(*), MIN(component), MIN(completed_at)
		FROM storage_import_horizons`).Scan(&horizonRows, &horizonComponent, &completedAt); err != nil {
		t.Fatal(err)
	}
	if horizonRows != 1 || horizonComponent != inventoryDatabaseComponent {
		t.Fatalf("relational horizon set = %d/%q", horizonRows, horizonComponent)
	}
	return fmt.Sprintf("%s|%s|%s|%s|%d|%d|%d|%d|%d|%s|%d|%d|%s|%d",
		component, sourceID, relative, digest, size, limit, mode, imported, skipped,
		status, importedAt, archivedAt, horizonComponent, completedAt)
}
