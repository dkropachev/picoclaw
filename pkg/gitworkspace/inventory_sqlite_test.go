package gitworkspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	for path, mode := range map[string]os.FileMode{
		root: 0o700, manager.checkoutRoot: 0o700, manager.lockRoot: 0o700,
		manager.databasePath(): 0o600,
	} {
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
	if journal != "wal" || foreignKeys != 1 || busyTimeout != 5000 || synchronous != 2 || userVersion != 2 {
		t.Fatalf("SQLite contract = journal=%q fk=%d busy=%d sync=%d version=%d",
			journal, foreignKeys, busyTimeout, synchronous, userVersion)
	}
	state, err := loadInventoryState(t.Context(), database)
	if err != nil || state.generation != 0 || len(state.Repositories) != 0 {
		t.Fatalf("fresh inventory = %#v, %v", state, err)
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
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
	if err := os.Rename(archive, legacy); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", manager.databasePath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE storage_imports
	    SET archive_status = 'pending', archived_at = NULL
	    WHERE component = ? AND source_id = ?`,
		inventoryDatabaseComponent,
		inventoryLegacySourceID,
	); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(Options{RootDir: root}); err != nil {
		t.Fatalf("archive retry with closed import horizon: %v", err)
	}
	if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
		t.Fatalf("archive retry retained source: %v", err)
	}
	if _, err := os.Lstat(archive); err != nil {
		t.Fatalf("archive retry lost destination: %v", err)
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestGitWorkspaceInventoryEmptyFirstOpenRejectsLateLegacySource(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".git-workspaces")
	manager, err := NewManager(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	state := inventorySQLiteValidState(t)
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, inventoryLegacyFilename)
	if err := os.WriteFile(legacy, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewManager(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	loaded := adversarialCloneInventory(t, reopened)
	if loaded.generation != 0 || len(loaded.Repositories) != 0 || len(loaded.Workspaces) != 0 {
		t.Fatalf("late legacy inventory became authoritative: %#v", loaded)
	}
	archive := filepath.Join(root, "legacy-json", inventoryLegacyArchive, inventoryLegacyFilename)
	if _, err := os.Lstat(archive); err != nil {
		t.Fatalf("late legacy inventory was not archived: %v", err)
	}
	database, err := manager.openInventoryDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var skipped, issues int
	if err := database.QueryRow(`SELECT
	    (SELECT skipped_count FROM storage_imports WHERE component = ? AND source_id = ?),
	    (SELECT COUNT(*) FROM storage_import_issues
	      WHERE component = ? AND source_id = ? AND issue_code = 'sqlite-authoritative')`,
		inventoryDatabaseComponent,
		inventoryLegacySourceID,
		inventoryDatabaseComponent,
		inventoryLegacySourceID,
	).Scan(&skipped, &issues); err != nil {
		t.Fatal(err)
	}
	if skipped != 1 || issues != 1 {
		t.Fatalf("late inventory audit = skipped:%d issues:%d", skipped, issues)
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestGitWorkspaceInventorySchemaV1UpgradeClosesImportAndAddsForeignKeyIndexes(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".git-workspaces")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, inventoryDatabaseFilename)
	v1, err := sqlitestore.Open(t.Context(), path, sqlitestore.Options{
		Component: inventoryDatabaseComponent,
		Migrations: []sqlitestore.Migration{{
			Version: 1,
			Statements: []string{
				inventoryMetaSchema,
				`INSERT INTO inventory_meta(singleton, generation) VALUES (1, 0)`,
				inventoryRepositoriesSchema,
				inventoryWorkspacesSchema,
				inventoryRepositoryWorkspaceOrderSchema,
				inventoryWorkspaceLocksSchema,
				inventoryDevelopmentLinesSchema,
				inventoryRetiredReservationsSchema,
				inventorySuspensionsSchema,
				inventoryRotationsSchema,
				inventoryHistorySchema,
				inventoryWorkspacesRepositoryIndexSchema,
				inventoryDevelopmentLinesStateIndexSchema,
				inventoryHistoryActionIndexSchema,
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := v1.Close(); err != nil {
		t.Fatal(err)
	}
	state := inventorySQLiteValidState(t)
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, inventoryLegacyFilename), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	loaded := adversarialCloneInventory(t, manager)
	if loaded.generation != 0 || len(loaded.Repositories) != 0 {
		t.Fatalf("v1 upgrade imported a late legacy source: %#v", loaded)
	}
	database, err := manager.openInventoryDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var version int
	if err := database.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("upgraded inventory user_version = %d", version)
	}
	for _, index := range []string{
		"inventory_workspaces_development_line_idx",
		"inventory_repository_workspace_order_workspace_idx",
		"inventory_development_lines_repository_idx",
		"inventory_suspensions_workspace_idx",
		"inventory_suspensions_repository_idx",
		"inventory_rotations_line_idx",
		"inventory_rotations_repository_idx",
	} {
		var rows int
		if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
		    WHERE type = 'index' AND name = ?`, index).Scan(&rows); err != nil || rows != 1 {
			t.Fatalf("upgraded inventory index %s = %d, %v", index, rows, err)
		}
	}
}

func TestGitWorkspaceInventoryMissingImportHorizonFailsClosed(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".git-workspaces")
	manager, err := NewManager(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", manager.databasePath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`DELETE FROM inventory_legacy_import_state`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(Options{RootDir: root}); !errors.Is(err, sqlitestore.ErrInvalidSchema) {
		t.Fatalf("missing inventory import horizon error = %v", err)
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

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestGitWorkspaceInventoryDuplicateJSONIdentityIsAuditedAndArchived(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".git-workspaces")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, inventoryLegacyFilename)
	duplicate := []byte(`{"version":4,"repositories":{},"Repositories":{}}`)
	if err := os.WriteFile(legacy, duplicate, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(Options{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.decodeLegacyInventory(duplicate); !errors.Is(err, errDuplicateLegacyInventoryIdentity) {
		t.Fatalf("duplicate inventory decoder error = %v", err)
	}
	archive := filepath.Join(root, "legacy-json", inventoryLegacyArchive, inventoryLegacyFilename)
	if _, err := os.Lstat(archive); err != nil {
		t.Fatalf("duplicate inventory was not archived: %v", err)
	}
	database, err := manager.openInventoryDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var skipped, issues int
	if err := database.QueryRow(`SELECT
	    (SELECT skipped_count FROM storage_imports WHERE component = ? AND source_id = ?),
	    (SELECT COUNT(*) FROM storage_import_issues
	      WHERE component = ? AND source_id = ? AND issue_code = 'duplicate-identity')`,
		inventoryDatabaseComponent,
		inventoryLegacySourceID,
		inventoryDatabaseComponent,
		inventoryLegacySourceID,
	).Scan(&skipped, &issues); err != nil {
		t.Fatal(err)
	}
	if skipped != 1 || issues != 1 {
		t.Fatalf("duplicate inventory audit = skipped:%d issues:%d", skipped, issues)
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
	if _, execErr := raw.Exec(`PRAGMA user_version = 3`); execErr != nil {
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

func TestGitWorkspaceInventorySQLitePropagatesRelationalReadFailures(t *testing.T) {
	manager, err := NewManager(Options{RootDir: filepath.Join(t.TempDir(), ".git-workspaces")})
	if err != nil {
		t.Fatal(err)
	}
	database, err := manager.openInventoryDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// One generation read plus nine ordered relational queries form the complete
	// load path. Exercise both driver failures and malformed result shapes at
	// every boundary so a later query refactor cannot silently ignore either.
	for _, malformed := range []bool{false, true} {
		for failAt := 1; failAt <= 10; failAt++ {
			queryer := &inventoryFaultQueryer{
				database:  database,
				failAt:    failAt,
				malformed: malformed,
			}
			if _, loadErr := loadInventoryStateFrom(t.Context(), queryer); loadErr == nil {
				t.Fatalf("load with malformed=%v failure=%d succeeded", malformed, failAt)
			}
			if queryer.calls != failAt {
				t.Fatalf("load with malformed=%v failure=%d made %d queries", malformed, failAt, queryer.calls)
			}
		}
	}
}

func TestGitWorkspaceInventorySQLiteLegacyDecoderFencesAggregateShapes(t *testing.T) {
	manager := &Manager{}
	if state, err := manager.decodeLegacyInventory([]byte(`{"version":"4"}`)); err != nil ||
		state.Repositories == nil || state.Workspaces == nil || state.DevelopmentLines == nil ||
		state.PinnedReservationRotations == nil {
		t.Fatalf("empty legacy aggregate = %#v, %v", state, err)
	}
	for name, payload := range map[string]string{
		"trailing value":      `{}` + `{}`,
		"malformed trailer":   `{}` + `{`,
		"future version":      `{"version":"999"}`,
		"controller evidence": `{"version":1,"development_lines":{"line":{}}}`,
		"bad relationship":    `{"version":"4","repositories":{"repository":null}}`,
		"bad identity": `{"version":"4","repositories":{"repository":{"id":"repository",` +
			`"remote_url":"https://example.invalid/repository.git"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if state, err := manager.decodeLegacyInventory([]byte(payload)); err == nil || state != nil {
				t.Fatalf("decodeLegacyInventory(%s) = %#v, %v", name, state, err)
			}
		})
	}
}

func TestGitWorkspaceInventorySQLiteValidationAndNilBoundaries(t *testing.T) {
	ctx := t.Context()
	var nilManager *Manager
	if database, err := nilManager.openInventoryDatabase(ctx); err == nil || database != nil {
		t.Fatalf("nil manager open = %#v, %v", database, err)
	}
	if state, err := loadInventoryState(ctx, nil); err == nil || state != nil {
		t.Fatalf("nil database load = %#v, %v", state, err)
	}
	if err := saveInventoryState(ctx, nil, &storeState{}); err == nil {
		t.Fatal("nil database save succeeded")
	}
	if count := inventoryStateRecordCount(nil); count != 0 {
		t.Fatalf("nil inventory count = %d", count)
	}
	countState := &storeState{
		Repositories: map[string]*RepositoryRecord{
			"nil":  nil,
			"full": {WorkspaceIDs: []string{"workspace"}},
		},
		DevelopmentLines: map[string]*developmentLineRecord{
			"nil": nil,
			"full": {
				RetiredReservationHashes: []string{"retired"},
				Suspensions:              []developmentLineSuspensionRecord{{}},
			},
		},
		PinnedReservationRotations: map[string][]pinnedReservationRotationRecord{
			"workspace": {{}},
		},
	}
	if count := inventoryStateRecordCount(countState); count != 8 {
		t.Fatalf("aggregate record count = %d, want 8", count)
	}

	complete := func() *storeState {
		return &storeState{
			Version:                    stateVersion,
			Repositories:               map[string]*RepositoryRecord{},
			Workspaces:                 map[string]*WorkspaceRecord{},
			DevelopmentLines:           map[string]*developmentLineRecord{},
			PinnedReservationRotations: map[string][]pinnedReservationRotationRecord{},
		}
	}
	tooMuchHistory := complete()
	tooMuchHistory.History = make([]HistoryEntry, historyLimit+1)
	oversizedRelationship := complete()
	oversizedRelationship.Repositories["repository"] = &RepositoryRecord{
		WorkspaceIDs: make([]string, inventoryMaximumRows+1),
	}
	missingRelationship := complete()
	missingRelationship.Workspaces["workspace"] = &WorkspaceRecord{ID: "workspace"}
	badRelationship := complete()
	badRelationship.Repositories["repository"] = &RepositoryRecord{WorkspaceIDs: []string{"missing"}}
	duplicateRelationship := complete()
	duplicateRelationship.Repositories["repository"] = &RepositoryRecord{
		WorkspaceIDs: []string{"workspace", "workspace"},
	}
	duplicateRelationship.Workspaces["workspace"] = &WorkspaceRecord{ID: "workspace", RepoID: "repository"}
	aggregateLimit := complete()
	aggregateLimit.DevelopmentLines["line"] = &developmentLineRecord{
		RetiredReservationHashes: make([]string, inventoryMaximumRows+1),
	}
	for name, state := range map[string]*storeState{
		"nil":                    nil,
		"incomplete":             {},
		"history limit":          tooMuchHistory,
		"relationship size":      oversizedRelationship,
		"relationship target":    badRelationship,
		"duplicate relationship": duplicateRelationship,
		"missing relationship":   missingRelationship,
		"aggregate limit":        aggregateLimit,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateInventoryRelationalState(state); err == nil {
				t.Fatalf("validateInventoryRelationalState(%s) succeeded", name)
			}
		})
	}
}

func TestGitWorkspaceInventorySQLiteWriteHelpersRejectInvalidRows(t *testing.T) {
	manager, err := NewManager(Options{RootDir: filepath.Join(t.TempDir(), ".git-workspaces")})
	if err != nil {
		t.Fatal(err)
	}
	database, err := manager.openInventoryDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	err = sqlitestore.Immediate(t.Context(), database, func(conn *sql.Conn) error {
		if rewriteErr := rewriteInventoryState(t.Context(), conn, nil, 0); rewriteErr == nil {
			return errors.New("nil rewrite succeeded")
		}
		checks := []func() error{
			func() error {
				return insertInventoryRepositories(t.Context(), conn, &storeState{
					Repositories: map[string]*RepositoryRecord{"repository": nil},
				})
			},
			func() error {
				return insertInventoryWorkspaces(t.Context(), conn, &storeState{
					Workspaces: map[string]*WorkspaceRecord{"workspace": nil},
				})
			},
			func() error {
				return insertInventoryDevelopmentLines(t.Context(), conn, &storeState{
					DevelopmentLines: map[string]*developmentLineRecord{"line": nil},
				})
			},
			func() error {
				return insertInventoryRotations(t.Context(), conn, &storeState{
					PinnedReservationRotations: map[string][]pinnedReservationRotationRecord{
						"workspace": {{}},
					},
				})
			},
			func() error {
				return insertInventoryHistory(t.Context(), conn, &storeState{
					History: []HistoryEntry{{}},
				})
			},
		}
		for index, check := range checks {
			if checkErr := check(); checkErr == nil {
				return fmt.Errorf("invalid write helper %d succeeded", index)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGitWorkspaceInventorySQLiteRewriteFailuresRollbackAtomically(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*storeState)
		setup  func(context.Context, *sql.Conn) error
	}{
		{
			name: "generation read",
			setup: func(_ context.Context, _ *sql.Conn) error {
				return context.Canceled
			},
		},
		{
			name: "history delete",
			setup: func(ctx context.Context, conn *sql.Conn) error {
				_, err := conn.ExecContext(ctx, `CREATE TEMP TRIGGER fail_inventory_history_delete
                    BEFORE DELETE ON inventory_history BEGIN SELECT RAISE(ABORT, 'injected'); END`)
				return err
			},
		},
		{
			name: "repository delete",
			setup: func(ctx context.Context, conn *sql.Conn) error {
				_, err := conn.ExecContext(ctx, `CREATE TEMP TRIGGER fail_inventory_repository_delete
                    BEFORE DELETE ON inventory_repositories BEGIN SELECT RAISE(ABORT, 'injected'); END`)
				return err
			},
		},
		{
			name: "repository insert",
			mutate: func(state *storeState) {
				for _, repository := range state.Repositories {
					repository.RemoteURL = ""
				}
			},
		},
		{
			name: "workspace insert",
			mutate: func(state *storeState) {
				for _, workspace := range state.Workspaces {
					workspace.Path = ""
				}
			},
		},
		{
			name: "development-line insert",
			mutate: func(state *storeState) {
				state.DevelopmentLines["line"] = &developmentLineRecord{}
			},
		},
		{
			name: "relationship insert",
			mutate: func(state *storeState) {
				for _, repository := range state.Repositories {
					repository.WorkspaceIDs = append(repository.WorkspaceIDs, "missing-workspace")
				}
			},
		},
		{
			name: "rotation insert",
			mutate: func(state *storeState) {
				state.PinnedReservationRotations["workspace"] = []pinnedReservationRotationRecord{{}}
			},
		},
		{
			name: "history insert",
			mutate: func(state *storeState) {
				state.History = []HistoryEntry{{}}
			},
		},
		{
			name: "generation update",
			setup: func(ctx context.Context, conn *sql.Conn) error {
				_, err := conn.ExecContext(ctx, `CREATE TEMP TRIGGER fail_inventory_generation_update
                    BEFORE UPDATE ON inventory_meta BEGIN SELECT RAISE(ABORT, 'injected'); END`)
				return err
			},
		},
		{
			name: "generation compare-and-swap",
			setup: func(ctx context.Context, conn *sql.Conn) error {
				_, err := conn.ExecContext(ctx, `CREATE TEMP TRIGGER ignore_inventory_generation_update
                    BEFORE UPDATE ON inventory_meta BEGIN SELECT RAISE(IGNORE); END`)
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, err := NewManager(Options{RootDir: filepath.Join(t.TempDir(), ".git-workspaces")})
			if err != nil {
				t.Fatal(err)
			}
			database, err := manager.openInventoryDatabase(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			state := inventorySQLiteValidState(t)
			if seedErr := saveInventoryState(t.Context(), database, state); seedErr != nil {
				t.Fatal(seedErr)
			}
			if test.mutate != nil {
				test.mutate(state)
			}
			rewriteErr := sqlitestore.Immediate(t.Context(), database, func(conn *sql.Conn) error {
				ctx := t.Context()
				if test.setup != nil {
					if setupErr := test.setup(ctx, conn); setupErr != nil {
						if errors.Is(setupErr, context.Canceled) {
							canceledCtx, cancel := context.WithCancel(ctx)
							cancel()
							return rewriteInventoryState(canceledCtx, conn, state, state.generation)
						}
						return setupErr
					}
				}
				return rewriteInventoryState(ctx, conn, state, state.generation)
			})
			if rewriteErr == nil {
				t.Fatal("injected rewrite failure committed")
			}
			loaded, err := loadInventoryState(t.Context(), database)
			if err != nil || loaded.generation != 1 || len(loaded.Repositories) != 1 ||
				len(loaded.Workspaces) != 1 || len(loaded.History) != 1 {
				t.Fatalf("state after failed rewrite = %#v, %v", loaded, err)
			}
		})
	}
}

func TestGitWorkspaceInventorySQLiteRejectsIndexDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		tamper string
	}{
		{"missing expected index", `DROP INDEX inventory_history_action_idx`},
		{"extra unique index", `CREATE UNIQUE INDEX inventory_history_identity_tamper
            ON inventory_history(history_id)`},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), ".git-workspaces")
			manager, err := NewManager(Options{RootDir: root})
			if err != nil {
				t.Fatal(err)
			}
			database, err := sql.Open("sqlite", manager.databasePath())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(test.tamper); err != nil {
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := NewManager(Options{RootDir: root}); !errors.Is(err, sqlitestore.ErrInvalidSchema) {
				t.Fatalf("index drift error = %v", err)
			}
		})
	}
}

func TestGitWorkspaceInventorySQLiteImportAuthorityAndRollback(t *testing.T) {
	t.Run("SQLite already authoritative", func(t *testing.T) {
		manager, err := NewManager(Options{RootDir: filepath.Join(t.TempDir(), ".git-workspaces")})
		if err != nil {
			t.Fatal(err)
		}
		database, err := manager.openInventoryDatabase(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		state := inventorySQLiteValidState(t)
		if seedErr := saveInventoryState(t.Context(), database, state); seedErr != nil {
			t.Fatal(seedErr)
		}
		err = sqlitestore.Immediate(t.Context(), database, func(conn *sql.Conn) error {
			result, importErr := manager.importLegacyInventory(t.Context(), conn, sqlitestore.LegacyInput{})
			if importErr != nil || result.Imported != 0 || result.Skipped != 1 ||
				len(result.Issues) != 1 || result.Issues[0].Code != "sqlite-authoritative" {
				return fmt.Errorf("authoritative import = %#v, %v", result, importErr)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("generation read failure", func(t *testing.T) {
		manager, err := NewManager(Options{RootDir: filepath.Join(t.TempDir(), ".git-workspaces")})
		if err != nil {
			t.Fatal(err)
		}
		database, err := manager.openInventoryDatabase(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		err = sqlitestore.Immediate(t.Context(), database, func(conn *sql.Conn) error {
			if _, deleteErr := conn.ExecContext(
				t.Context(),
				`DELETE FROM inventory_legacy_import_state`,
			); deleteErr != nil {
				return deleteErr
			}
			if _, dropErr := conn.ExecContext(t.Context(), `DROP TABLE inventory_meta`); dropErr != nil {
				return dropErr
			}
			_, importErr := manager.importLegacyInventory(t.Context(), conn, sqlitestore.LegacyInput{})
			return importErr
		})
		if err == nil {
			t.Fatal("import with missing generation table succeeded")
		}
	})

	t.Run("relational write failure", func(t *testing.T) {
		manager, err := NewManager(Options{RootDir: filepath.Join(t.TempDir(), ".git-workspaces")})
		if err != nil {
			t.Fatal(err)
		}
		database, err := manager.openInventoryDatabase(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		state := inventorySQLiteValidState(t)
		for _, repository := range state.Repositories {
			repository.RemoteURL = strings.Repeat("r", 4097)
			repository.ID = repoID(repository.RemoteURL)
			for _, workspace := range state.Workspaces {
				workspace.RepoID = repository.ID
				workspace.RemoteURL = repository.RemoteURL
			}
		}
		for _, workspace := range state.Workspaces {
			delete(state.Repositories, workspace.RepoID)
		}
		state.Repositories = map[string]*RepositoryRecord{}
		for _, workspace := range state.Workspaces {
			repository := &RepositoryRecord{
				ID: workspace.RepoID, RemoteURL: workspace.RemoteURL, WorkspaceIDs: []string{workspace.ID},
			}
			state.Repositories[repository.ID] = repository
		}
		encoded, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		err = sqlitestore.Immediate(t.Context(), database, func(conn *sql.Conn) error {
			if _, deleteErr := conn.ExecContext(
				t.Context(),
				`DELETE FROM inventory_legacy_import_state`,
			); deleteErr != nil {
				return deleteErr
			}
			_, importErr := manager.importLegacyInventory(t.Context(), conn, sqlitestore.LegacyInput{Data: encoded})
			return importErr
		})
		if err == nil {
			t.Fatal("oversized relational import succeeded")
		}
		loaded, loadErr := loadInventoryState(t.Context(), database)
		if loadErr != nil || loaded.generation != 0 || len(loaded.Repositories) != 0 {
			t.Fatalf("failed import state = %#v, %v", loaded, loadErr)
		}
	})
}

func TestGitWorkspaceInventorySQLiteRejectsRelationalRowTampering(t *testing.T) {
	hash := strings.Repeat("a", 64)
	commit := strings.Repeat("b", 40)
	for _, test := range []struct {
		name   string
		tamper func(*testing.T, *sql.DB)
	}{
		{
			name: "missing repository relationship",
			tamper: func(t *testing.T, database *sql.DB) {
				if _, err := database.Exec(`DELETE FROM inventory_repository_workspace_order`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "repository ordering gap",
			tamper: func(t *testing.T, database *sql.DB) {
				if _, err := database.Exec(`UPDATE inventory_repository_workspace_order SET ordinal = 1`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "orphan lock",
			tamper: func(t *testing.T, database *sql.DB) {
				if _, err := database.Exec(`INSERT INTO inventory_workspace_locks
                    VALUES ('missing', 'session', 'agent', 1, 2, 3, 4)`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "orphan retired reservation",
			tamper: func(t *testing.T, database *sql.DB) {
				if _, err := database.Exec(`INSERT INTO inventory_development_line_retired_reservations
                    VALUES ('missing', 0, ?)`, hash); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "orphan suspension",
			tamper: func(t *testing.T, database *sql.DB) {
				if _, err := database.Exec(`INSERT INTO inventory_development_line_suspensions VALUES (
                    'missing', 0, 'candidate', 'intent', ?, 'missing', 'repository', 'main', ?,
                    0, 1, ?, ?, ?, 'agent', ?, ?, 0, '', '', 0, ?, ?, 1, 2)`,
					hash, commit, commit, commit, hash, commit, hash, hash, hash); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "rotation ordering gap",
			tamper: func(t *testing.T, database *sql.DB) {
				if _, err := database.Exec(`INSERT INTO inventory_reservation_rotations VALUES (
                    'missing', 1, 'intent', NULL, 'repository', 'main', ?, 0, 0, '', '', '',
                    ?, ?, 'agent', ?, ?, 1, 2)`, commit, hash, hash, hash, hash); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "public history ordering gap",
			tamper: func(t *testing.T, database *sql.DB) {
				if _, err := database.Exec(`UPDATE inventory_history SET ordinal = 1 WHERE stream = 'public'`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "development history ordering gap",
			tamper: func(t *testing.T, database *sql.DB) {
				if _, err := database.Exec(`UPDATE inventory_history SET ordinal = 1 WHERE stream = 'development'`); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "invalid history stream",
			tamper: func(t *testing.T, database *sql.DB) {
				if _, err := database.Exec(`PRAGMA ignore_check_constraints = ON`); err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(`UPDATE inventory_history SET stream = 'invalid'
                    WHERE stream = 'public'`); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, err := NewManager(Options{RootDir: filepath.Join(t.TempDir(), ".git-workspaces")})
			if err != nil {
				t.Fatal(err)
			}
			database, err := manager.openInventoryDatabase(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			database.SetMaxOpenConns(1)
			state := inventorySQLiteValidState(t)
			if err := saveInventoryState(t.Context(), database, state); err != nil {
				t.Fatal(err)
			}
			if _, err := database.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
				t.Fatal(err)
			}
			test.tamper(t, database)
			if loaded, err := loadInventoryState(t.Context(), database); err == nil || loaded != nil {
				t.Fatalf("tampered relational load = %#v, %v", loaded, err)
			}
		})
	}

	manager, err := NewManager(Options{RootDir: filepath.Join(t.TempDir(), ".git-workspaces")})
	if err != nil {
		t.Fatal(err)
	}
	database, err := manager.openInventoryDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := saveInventoryState(t.Context(), database, &storeState{}); err == nil {
		t.Fatal("incomplete relational save succeeded")
	}
}

func inventorySQLiteValidState(t *testing.T) *storeState {
	t.Helper()
	remote := "https://example.invalid/sqlite-inventory.git"
	repositoryID := repoID(remote)
	workspaceID := "gw-sqlite-inventory"
	now := time.Date(2026, 8, 31, 20, 0, 0, 123456789, time.UTC)
	dropped := now.Add(time.Minute)
	return &storeState{
		Version: stateVersion,
		Repositories: map[string]*RepositoryRecord{
			repositoryID: {
				ID: repositoryID, RemoteURL: remote, WorkspaceIDs: []string{workspaceID},
				FirstSeenAt: now, LastSeenAt: now, LastWorkAt: now,
			},
		},
		Workspaces: map[string]*WorkspaceRecord{
			workspaceID: {
				ID: workspaceID, RepoID: repositoryID, RemoteURL: remote,
				Path: filepath.Join(t.TempDir(), "checkout"), CreatedAt: now, UpdatedAt: now,
				LastWorkAt: now, LastCleanedAt: now, DroppedAt: &dropped,
			},
		},
		DevelopmentLines:           map[string]*developmentLineRecord{},
		PinnedReservationRotations: map[string][]pinnedReservationRotationRecord{},
		History: []HistoryEntry{{
			ID: "history-sqlite", Time: now, Action: "created",
			RepoID: repositoryID, WorkspaceID: workspaceID,
		}},
		DevelopmentLineHistory: []HistoryEntry{{
			ID: "development-history-sqlite", Time: now, Action: "parked",
			RepoID: repositoryID, WorkspaceID: workspaceID,
		}},
	}
}

type inventoryFaultQueryer struct {
	database  *sql.DB
	failAt    int
	calls     int
	malformed bool
}

func (queryer *inventoryFaultQueryer) QueryRowContext(
	ctx context.Context,
	query string,
	arguments ...any,
) *sql.Row {
	queryer.calls++
	if queryer.calls == queryer.failAt {
		return queryer.database.QueryRowContext(ctx, `SELECT value FROM inventory_missing_table`)
	}
	return queryer.database.QueryRowContext(ctx, query, arguments...)
}

func (queryer *inventoryFaultQueryer) QueryContext(
	ctx context.Context,
	query string,
	arguments ...any,
) (*sql.Rows, error) {
	queryer.calls++
	if queryer.calls == queryer.failAt {
		if queryer.malformed {
			return queryer.database.QueryContext(ctx, `SELECT 1`)
		}
		return nil, errors.New("injected inventory query failure")
	}
	return queryer.database.QueryContext(ctx, query, arguments...)
}
