package repoaudit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryReviewAutomationSnapshotBoundaries(t *testing.T) {
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := NewStore(t.TempDir()).RepositoryReviewAutomationSnapshot(
			ctx,
			"rra_snapshot_canceled",
		); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled snapshot error = %v", err)
		}
	})

	t.Run("invalid id", func(t *testing.T) {
		if _, err := NewStore(t.TempDir()).RepositoryReviewAutomationSnapshot(
			context.Background(),
			"bad",
		); !errors.Is(err, ErrInvalidAutomation) {
			t.Fatalf("invalid snapshot error = %v", err)
		}
	})

	t.Run("missing automation", func(t *testing.T) {
		store := newAutomationTestStore(t)
		if _, err := store.RepositoryReviewAutomationSnapshot(
			context.Background(),
			"rra_snapshot_absent",
		); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing snapshot error = %v", err)
		}
	})

	t.Run("corrupt automation", func(t *testing.T) {
		store := newAutomationTestStore(t)
		automation := createAutomationForTest(t, store, "rra_snapshot_corrupt", "corrupt")
		purgeTestCorruptAutomation(t, store, automation.ID)
		if _, err := store.RepositoryReviewAutomationSnapshot(
			context.Background(),
			automation.ID,
		); err == nil {
			t.Fatal("snapshot accepted corrupt automation")
		}
	})

	t.Run("primary inventory failure", func(t *testing.T) {
		store := newAutomationTestStore(t)
		automation := createAutomationForTest(t, store, "rra_snapshot_primary_failure", "primary")
		sentinel := errors.New("injected primary snapshot failure")
		store.loadForTest = func(string) (RepositoryState, error) {
			return RepositoryState{}, sentinel
		}
		if _, err := store.RepositoryReviewAutomationSnapshot(
			context.Background(),
			automation.ID,
		); !errors.Is(err, sentinel) {
			t.Fatalf("primary snapshot error = %v", err)
		}
	})

	t.Run("empty history", func(t *testing.T) {
		store := newAutomationTestStore(t)
		automation := createAutomationForTest(t, store, "rra_snapshot_empty", "empty")
		snapshot, err := store.RepositoryReviewAutomationSnapshot(
			context.Background(),
			automation.ID,
		)
		if err != nil || snapshot.HistoryFound || snapshot.PurgeEligibility.HistoryFound ||
			!snapshot.PurgeEligibility.CanRemove || snapshot.PurgeEligibility.CanPurge {
			t.Fatalf("empty snapshot = %#v, error = %v", snapshot, err)
		}
	})
}

func TestRepositoryReviewPurgeRunInventoryBoundaries(t *testing.T) {
	t.Run("state resolver load failure", func(t *testing.T) {
		sentinel := errors.New("injected state resolver failure")
		store := Store{loadForTest: func(string) (RepositoryState, error) {
			return RepositoryState{}, sentinel
		}}
		if _, _, err := store.resolveRepositoryStateIgnoringPurge(
			validAutomationForTest("rra_resolve_load_failure", "load failure"),
		); !errors.Is(err, sentinel) {
			t.Fatalf("state resolver error = %v", err)
		}
	})

	t.Run("inventory catalog failure", func(t *testing.T) {
		databasePath := filepath.Join(t.TempDir(), "empty.db")
		store := Store{
			root: t.TempDir(),
			loadForTest: func(repository string) (RepositoryState, error) {
				return RepositoryState{Repository: repository}, nil
			},
			openForTest: func(context.Context) (*sql.DB, error) {
				return sql.Open("sqlite", databasePath)
			},
		}
		automation := validAutomationForTest("rra_inventory_catalog_failure", "catalog failure")
		automation.Repository = "owner/absent"
		automation.RunIDs = []string{"wr_inventory_catalog_failure"}
		if _, _, _, _, err := store.resolveRepositoryPurgeInventory(automation); err == nil {
			t.Fatal("inventory accepted a missing state catalog")
		}
	})

	t.Run("unmatched run", func(t *testing.T) {
		store := newAutomationTestStore(t)
		unmatched := createPurgeTestLedger(t, store, "legacy/unmatched-inventory")
		unmatched.Runs = []ReviewRun{{ID: "wr_other_inventory"}}
		unmatched.Version++
		if err := store.save(&unmatched); err != nil {
			t.Fatal(err)
		}
		automation := validAutomationForTest("rra_inventory_unmatched", "unmatched")
		automation.Repository = "owner/absent"
		automation.RunIDs = []string{"wr_wanted_inventory"}
		_, found, targets, states, err := store.resolveRepositoryPurgeInventory(automation)
		if err != nil || found || len(targets) != 0 || len(states) != 0 {
			t.Fatalf(
				"unmatched inventory found=%v targets=%#v states=%#v err=%v",
				found,
				targets,
				states,
				err,
			)
		}
	})

	t.Run("ambiguous run", func(t *testing.T) {
		store := newAutomationTestStore(t)
		for _, repository := range []string{"legacy/ambiguous-first", "legacy/ambiguous-second"} {
			state := createPurgeTestLedger(t, store, repository)
			state.Runs = []ReviewRun{{ID: "wr_ambiguous_inventory"}}
			state.Version++
			if err := store.save(&state); err != nil {
				t.Fatal(err)
			}
		}
		automation := validAutomationForTest("rra_inventory_ambiguous", "ambiguous")
		automation.Repository = "owner/absent"
		automation.RunIDs = []string{"wr_ambiguous_inventory"}
		if _, _, _, _, err := store.resolveRepositoryPurgeInventory(automation); err == nil ||
			!strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("ambiguous inventory error = %v", err)
		}
	})
}

type repositoryReviewPurgeAuditCoverageRow struct {
	name     string
	digest   []byte
	limit    int64
	mode     int64
	imported int64
	skipped  int64
	status   string
}

func newRepositoryReviewPurgeAuditCoverageStore(
	t *testing.T,
	root string,
	rows ...repositoryReviewPurgeAuditCoverageRow,
) Store {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "archive-audit.db")
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE storage_imports (
		component TEXT,
		source_id TEXT,
		source_relative TEXT,
		source_digest BLOB,
		source_limit INTEGER,
		source_mode INTEGER,
		imported_count INTEGER,
		skipped_count INTEGER,
		archive_status TEXT
	)`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	for index, row := range rows {
		if _, err := database.Exec(`INSERT INTO storage_imports VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			repositoryReviewDatabaseComponent,
			"source-"+automationTestIndex(index),
			row.name,
			row.digest,
			row.limit,
			row.mode,
			row.imported,
			row.skipped,
			row.status,
		); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return Store{
		root: root,
		openForTest: func(context.Context) (*sql.DB, error) {
			return sql.Open("sqlite", databasePath)
		},
	}
}

func repositoryReviewPurgeValidAuditRow(
	name string,
	data []byte,
) repositoryReviewPurgeAuditCoverageRow {
	digest := sha256.Sum256(data)
	return repositoryReviewPurgeAuditCoverageRow{
		name: name, digest: digest[:], limit: int64(len(data)), mode: 0o600,
		imported: 1, status: "complete",
	}
}

func TestRepositoryReviewPurgeArchiveInventoryBoundaries(t *testing.T) {
	t.Run("duplicate ledger source", func(t *testing.T) {
		store := newAutomationTestStore(t)
		intent := repositoryReviewPurgeIntent{
			AutomationID: "rra_duplicate_archive_source",
			LedgerTargets: []repositoryReviewPurgeLedgerTarget{
				{Repository: "owner/duplicate", Version: 1},
				{Repository: "owner/duplicate", Version: 1},
			},
		}
		if err := store.removeRepositoryReviewPurgeArchives(intent); err != nil {
			t.Fatalf("duplicate archive source error = %v", err)
		}
	})

	t.Run("audited archive root absent", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "absent")
		name := automationFilename("rra_absent_archive_root")
		store := newRepositoryReviewPurgeAuditCoverageStore(
			t,
			root,
			repositoryReviewPurgeValidAuditRow(name, []byte("archived")),
		)
		if err := store.removeRepositoryReviewPurgeArchives(repositoryReviewPurgeIntent{
			AutomationID: "rra_absent_archive_root",
		}); err != nil {
			t.Fatalf("absent archive root error = %v", err)
		}
		if err := store.validateRepositoryReviewPurgeArchives(repositoryReviewPurgeIntent{
			AutomationID: "rra_absent_archive_root",
		}); err != nil {
			t.Fatalf("absent archive validation error = %v", err)
		}
	})

	t.Run("audited archive rejected", func(t *testing.T) {
		root := t.TempDir()
		archiveRoot := filepath.Join(root, "legacy-json", repositoryReviewLegacyArchiveLabel)
		if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		name := automationFilename("rra_rejected_archive")
		if err := os.Mkdir(filepath.Join(archiveRoot, name), 0o700); err != nil {
			t.Fatal(err)
		}
		store := newRepositoryReviewPurgeAuditCoverageStore(
			t,
			root,
			repositoryReviewPurgeValidAuditRow(name, []byte("archived")),
		)
		if err := store.removeRepositoryReviewPurgeArchives(repositoryReviewPurgeIntent{
			AutomationID: "rra_rejected_archive",
		}); err == nil {
			t.Fatal("archive purge accepted an audited directory")
		}
	})

	t.Run("audit query failure", func(t *testing.T) {
		databasePath := filepath.Join(t.TempDir(), "missing-audit.db")
		store := Store{openForTest: func(context.Context) (*sql.DB, error) {
			return sql.Open("sqlite", databasePath)
		}}
		if _, err := store.repositoryReviewPurgeArchiveRecords([]repositoryReviewPurgeArchiveCandidate{{
			Name: "missing.json",
		}}); err == nil {
			t.Fatal("archive inventory accepted a missing audit table")
		}
	})

	t.Run("invalid audit", func(t *testing.T) {
		store := newRepositoryReviewPurgeAuditCoverageStore(
			t,
			t.TempDir(),
			repositoryReviewPurgeAuditCoverageRow{
				name: "invalid.json", digest: []byte{1}, limit: 1, mode: 0o600,
				imported: 1, status: "complete",
			},
		)
		if _, err := store.repositoryReviewPurgeArchiveRecords([]repositoryReviewPurgeArchiveCandidate{{
			Name: "invalid.json",
		}}); err == nil {
			t.Fatal("archive inventory accepted an invalid audit")
		}
	})

	t.Run("skipped and unimported audits", func(t *testing.T) {
		data := []byte("archive")
		skipped := repositoryReviewPurgeValidAuditRow("skipped.json", data)
		skipped.skipped = 1
		unimported := repositoryReviewPurgeValidAuditRow("unimported.json", data)
		unimported.imported = 0
		store := newRepositoryReviewPurgeAuditCoverageStore(t, t.TempDir(), skipped, unimported)
		records, err := store.repositoryReviewPurgeArchiveRecords(
			[]repositoryReviewPurgeArchiveCandidate{
				{Name: skipped.name},
				{Name: unimported.name, RequireImported: true},
			},
		)
		if err != nil || len(records) != 0 {
			t.Fatalf("skipped audit records = %#v, error = %v", records, err)
		}
	})
}

//nolint:govet // Independent test assertions intentionally reuse err.
func TestRepositoryReviewPurgePreparedRejectsDriftedArchive(t *testing.T) {
	fixture := newPurgePhaseFixture(t, repositoryReviewPurgePrepared, false)
	name := automationFilename(fixture.automation.ID)
	expected := []byte("expected")
	actual := []byte("tampered")
	digest := sha256.Sum256(expected)
	database, err := fixture.store.openDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), `INSERT INTO storage_imports (
		component, source_id, source_relative, source_digest, source_size, source_limit,
		source_mode, imported_count, skipped_count, archive_status, imported_at, archived_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		repositoryReviewDatabaseComponent,
		"drifted-prepared-archive",
		name,
		digest[:],
		len(expected),
		len(expected),
		0o600,
		1,
		0,
		"complete",
		automationTestNow.UnixNano(),
		automationTestNow.UnixNano(),
	); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	archiveRoot := filepath.Join(
		fixture.store.root,
		"legacy-json",
		repositoryReviewLegacyArchiveLabel,
	)
	if err := os.MkdirAll(archiveRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archiveRoot, name), actual, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.applyPurgeIntent(fixture.intent); err == nil {
		t.Fatal("prepared purge accepted a drifted audited archive")
	}
	current, found, err := fixture.store.GetAutomation(t.Context(), fixture.automation.ID)
	if err != nil || !found || current.Version != fixture.automation.Version {
		t.Fatalf(
			"automation changed after failed archive preflight: %#v found=%v err=%v",
			current,
			found,
			err,
		)
	}
}

func TestRepositoryReviewPurgeArchiveRootBoundaries(t *testing.T) {
	t.Run("missing store root", func(t *testing.T) {
		if root, found, err := openRepositoryReviewPurgeArchiveRoot(
			filepath.Join(t.TempDir(), "missing"),
		); err != nil || found || root != nil {
			t.Fatalf("missing archive root = %#v, found = %v, error = %v", root, found, err)
		}
	})

	t.Run("missing store parent", func(t *testing.T) {
		if root, found, err := openRepositoryReviewPurgeArchiveRoot(
			filepath.Join(t.TempDir(), "missing-parent", "store"),
		); err != nil || found || root != nil {
			t.Fatalf("missing archive parent = %#v, found = %v, error = %v", root, found, err)
		}
	})

	t.Run("store root is a file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := openRepositoryReviewPurgeArchiveRoot(path); err == nil {
			t.Fatal("archive root accepted a file")
		}
	})

	t.Run("store parent is a file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := openRepositoryReviewPurgeArchiveRoot(filepath.Join(path, "store")); err == nil {
			t.Fatal("archive root accepted a file parent")
		}
	})

	t.Run("missing child", func(t *testing.T) {
		root, err := os.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		if child, found, err := openRepositoryReviewPurgeChildRoot(root, "missing"); err != nil ||
			found ||
			child != nil {
			t.Fatalf("missing child = %#v, found = %v, error = %v", child, found, err)
		}
	})

	t.Run("closed parent", func(t *testing.T) {
		root, err := os.OpenRoot(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := root.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := openRepositoryReviewPurgeChildRoot(root, "child"); err == nil {
			t.Fatal("archive helper accepted a closed parent")
		}
	})
}

func TestRepositoryReviewAuditedArchivePortableBoundaries(t *testing.T) {
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("absent", func(t *testing.T) {
		if err := removeRepositoryReviewAuditedArchive(root, repositoryReviewPurgeArchiveRecord{
			Name: "absent.json",
		}); err != nil {
			t.Fatalf("absent archive removal error = %v", err)
		}
	})
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	t.Run("closed root", func(t *testing.T) {
		if err := removeRepositoryReviewAuditedArchive(root, repositoryReviewPurgeArchiveRecord{
			Name: "closed.json",
		}); err == nil {
			t.Fatal("archive removal accepted a closed root")
		}
	})

	t.Run("digest drift", func(t *testing.T) {
		directory := t.TempDir()
		if err := os.WriteFile(filepath.Join(directory, "drift.json"), []byte("drift"), 0o600); err != nil {
			t.Fatal(err)
		}
		archiveRoot, err := os.OpenRoot(directory)
		if err != nil {
			t.Fatal(err)
		}
		defer archiveRoot.Close()
		if err := removeRepositoryReviewAuditedArchive(archiveRoot, repositoryReviewPurgeArchiveRecord{
			Name: "drift.json", Limit: 5, Mode: 0o600,
		}); err == nil {
			t.Fatal("archive removal accepted digest drift")
		}
	})
}

func TestRepositoryReviewPurgePrimaryFenceScanBoundaries(t *testing.T) {
	t.Run("public catalog lock failures", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if err := os.Mkdir(
			repositoryReviewTestLockPath(t, store.root, "store.lock"),
			0o700,
		); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.GetByID(RepositoryID("owner/locked")); err == nil {
			t.Fatal("direct-ID read ignored lock failure")
		}
		if _, err := store.ListSummaries(); err == nil {
			t.Fatal("summary list ignored lock failure")
		}
		if _, err := store.List(); err == nil {
			t.Fatal("state list ignored lock failure")
		}
	})

	t.Run("direct state fence error", func(t *testing.T) {
		store := newAutomationTestStore(t)
		id := RepositoryID("owner/corrupt-direct-fence")
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(
			store.root,
			"purge_repository_"+strings.TrimPrefix(id, "rrp_")+".json",
		)
		if err := os.WriteFile(path, []byte(`{`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.GetByID(id); err == nil {
			t.Fatal("direct-ID read accepted corrupt fence")
		}
	})

	t.Run("invalid state id", func(t *testing.T) {
		store := newAutomationTestStore(t)
		if _, found, err := store.loadPurgeFenceForStateID("not-a-state-id"); err != nil || found {
			t.Fatalf("invalid state fence found=%v err=%v", found, err)
		}
	})

	t.Run("exact state fence", func(t *testing.T) {
		store := newAutomationTestStore(t)
		automation := createAutomationForTest(t, store, "rra_exact_state_fence", "exact fence")
		state := createPurgeTestLedger(t, store, automation.Repository)
		intent := purgeTestIntent(automation, state)
		if err := store.savePurgeIntent(intent); err != nil {
			t.Fatal(err)
		}
		loaded, found, err := store.loadPurgeFenceForStateID(RepositoryID(state.Repository))
		if err != nil || !found || loaded.AutomationID != automation.ID {
			t.Fatalf("exact state fence=%#v found=%v err=%v", loaded, found, err)
		}
	})

	t.Run("primary fence miss", func(t *testing.T) {
		store := newAutomationTestStore(t)
		automation := createAutomationForTest(t, store, "rra_primary_fence_miss", "fence miss")
		state := createPurgeTestLedger(t, store, automation.Repository)
		if err := store.savePurgeIntent(purgeTestIntent(automation, state)); err != nil {
			t.Fatal(err)
		}
		if _, found, err := store.loadPurgeFenceForStateID(RepositoryID("owner/unrelated")); err != nil ||
			found {
			t.Fatalf("unrelated state fence found=%v err=%v", found, err)
		}
	})

	t.Run("catalog read error", func(t *testing.T) {
		store := newAutomationTestStore(t)
		sentinel := errors.New("injected primary catalog read failure")
		original := repositoryReviewPurgeReadDir
		repositoryReviewPurgeReadDir = func(string) ([]os.DirEntry, error) { return nil, sentinel }
		t.Cleanup(func() { repositoryReviewPurgeReadDir = original })
		if _, _, err := store.loadPrimaryPurgeFence("owner/repo"); !errors.Is(err, sentinel) {
			t.Fatalf("primary catalog read error = %v", err)
		}
	})

	t.Run("catalog limit", func(t *testing.T) {
		store := newAutomationTestStore(t)
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			store.purgeAutomationIntentPath("rra_over_limit"),
			[]byte(`{}`),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		original := repositoryReviewPurgeIntentLimit
		repositoryReviewPurgeIntentLimit = 0
		t.Cleanup(func() { repositoryReviewPurgeIntentLimit = original })
		if _, _, err := store.loadPrimaryPurgeFence("owner/repo"); err == nil ||
			!strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("primary catalog limit error = %v", err)
		}
	})

	t.Run("corrupt primary marker", func(t *testing.T) {
		store := newAutomationTestStore(t)
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			store.purgeAutomationIntentPath("rra_corrupt_primary"),
			[]byte(`{`),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.loadPrimaryPurgeFence("owner/repo"); err == nil {
			t.Fatal("primary fence scan accepted a corrupt marker")
		}
	})
}
