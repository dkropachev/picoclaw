package repoaudit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRepositoryReviewPurgeCompositeBoundaryCoverage(t *testing.T) {
	store := newAutomationTestStore(t)
	if _, _, err := store.PurgeAutomationHistory(
		context.Background(), "rra_missing_fence", 1, 0, " ", "owner/repo",
	); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("blank purge ledger fence error = %v", err)
	}
	if _, err := store.DeleteAutomationAndHistory(
		context.Background(), "rra_missing_fence", 1, 0, "", "owner/repo",
	); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("blank removal ledger fence error = %v", err)
	}

	automation := validAutomationForTest("rra_empty_active_id", "empty active id")
	eligibility := EvaluateRepositoryReviewPurge(automation, RepositoryState{
		Repository:      automation.Repository,
		Version:         1,
		ActiveReviewRun: &RepositoryReviewActiveRun{},
	}, true)
	if eligibility.CanRemove || len(eligibility.Blockers) != 1 ||
		eligibility.Blockers[0].Code != RepositoryReviewPurgeBlockerReviewActive {
		t.Fatalf("empty-ID active run eligibility = %#v", eligibility)
	}
	if message := repositoryReviewPurgeBlockerMessage(RepositoryReviewPurgeBlockerRetentionUnavailable); message == "" {
		t.Fatal("retention-unavailable blocker has no message")
	}
}

func TestRepositoryReviewPurgeEligibilitySnapshotFailures(t *testing.T) {
	t.Run("lock failure", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if err := os.Mkdir(store.root+".lock", 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := store.RepositoryReviewPurgeEligibilityForAutomation(
			validAutomationForTest("rra_eligibility_lock", "lock"),
		)
		if err == nil {
			t.Fatal("eligibility ignored lock failure")
		}
	})

	t.Run("automation load failure", func(t *testing.T) {
		store := newAutomationTestStore(t)
		automation := createAutomationForTest(t, store, "rra_eligibility_load", "load")
		purgeTestCorruptAutomation(t, store, automation.ID)
		if _, err := store.RepositoryReviewPurgeEligibilityForAutomation(automation); err == nil {
			t.Fatal("eligibility accepted corrupt automation")
		}
	})

	t.Run("stale snapshot", func(t *testing.T) {
		store := newAutomationTestStore(t)
		automation := createAutomationForTest(t, store, "rra_eligibility_stale", "stale")
		automation.Version++
		if _, err := store.RepositoryReviewPurgeEligibilityForAutomation(automation); !errors.Is(
			err,
			ErrConflict,
		) {
			t.Fatalf("stale eligibility error = %v", err)
		}
	})

	t.Run("active purge fence", func(t *testing.T) {
		store := newAutomationTestStore(t)
		automation := createAutomationForTest(t, store, "rra_eligibility_fenced", "fenced")
		state := createPurgeTestLedger(t, store, automation.Repository)
		if err := store.savePurgeIntent(purgeTestIntent(automation, state)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.RepositoryReviewPurgeEligibilityForAutomation(automation); !errors.Is(
			err,
			ErrRepositoryReviewPurgeInProgress,
		) {
			t.Fatalf("fenced eligibility error = %v", err)
		}
	})

	t.Run("corrupt purge fence", func(t *testing.T) {
		store := newAutomationTestStore(t)
		automation := createAutomationForTest(t, store, "rra_eligibility_bad_fence", "bad fence")
		if err := os.WriteFile(store.purgeRepositoryFencePath(automation.Repository), []byte(`{`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.RepositoryReviewPurgeEligibilityForAutomation(automation); err == nil {
			t.Fatal("eligibility accepted corrupt purge fence")
		}
	})

	t.Run("inventory load failure", func(t *testing.T) {
		store := newAutomationTestStore(t)
		automation := createAutomationForTest(t, store, "rra_eligibility_inventory", "inventory")
		sentinel := errors.New("injected eligibility inventory failure")
		store.loadForTest = func(string) (RepositoryState, error) {
			return RepositoryState{}, sentinel
		}
		if _, err := store.RepositoryReviewPurgeEligibilityForAutomation(automation); !errors.Is(
			err,
			sentinel,
		) {
			t.Fatalf("inventory eligibility error = %v", err)
		}
	})
}

func TestRepositoryReviewAutomationSnapshotKeepsPrimaryWhenAliasInventoryFails(t *testing.T) {
	store := newAutomationTestStore(t)
	input := validAutomationForTest("rra_snapshot_alias_failure", "snapshot alias failure")
	input.Repository = "https://github.com/Owner/Repo.git"
	automation, err := store.CreateAutomation(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	primary := repositoryReviewCoverageState(CanonicalRepositoryIdentity(automation.Repository))
	sentinel := errors.New("injected secondary alias failure")
	store.loadForTest = func(repository string) (RepositoryState, error) {
		if repository == primary.Repository {
			return primary, nil
		}
		return RepositoryState{}, sentinel
	}
	snapshot, err := store.RepositoryReviewAutomationSnapshot(context.Background(), automation.ID)
	if err != nil || !snapshot.HistoryFound || snapshot.State.Repository != primary.Repository ||
		!errors.Is(snapshot.PurgeInventoryError, sentinel) {
		t.Fatalf("snapshot=%#v err=%v", snapshot, err)
	}
}

func TestRepositoryReviewAutomationSnapshotRejectsInventoryPrimaryDrift(t *testing.T) {
	store := newAutomationTestStore(t)
	automation := createAutomationForTest(t, store, "rra_snapshot_primary_drift", "snapshot drift")
	loads := 0
	store.loadForTest = func(repository string) (RepositoryState, error) {
		loads++
		if loads == 1 {
			return RepositoryState{Repository: repository, Version: 1}, nil
		}
		return RepositoryState{Repository: "owner/drifted", Version: 1}, nil
	}
	if _, err := store.RepositoryReviewAutomationSnapshot(
		context.Background(),
		automation.ID,
	); !errors.Is(err, ErrConflict) {
		t.Fatalf("snapshot primary drift error = %v", err)
	}
}

func TestRepositoryReviewPurgeFenceCatalogReadFailures(t *testing.T) {
	store := newAutomationTestStore(t)
	state := createPurgeTestLedger(t, store, "owner/corrupt-fence-catalog")
	if err := os.WriteFile(store.purgeRepositoryFencePath(state.Repository), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListSummaries(); err == nil {
		t.Fatal("summary catalog accepted a corrupt purge fence")
	}
	if _, err := store.List(); err == nil {
		t.Fatal("state catalog accepted a corrupt purge fence")
	}
}

func TestRepositoryReviewPurgeCompositeIntentValidation(t *testing.T) {
	store := newAutomationTestStore(t)
	automation := createAutomationForTest(t, store, "rra_composite_intent", "composite intent")
	state := createPurgeTestLedger(t, store, automation.Repository)
	valid := purgeTestIntent(automation, state)

	tests := []struct {
		name   string
		mutate func(*repositoryReviewPurgeIntent)
	}{
		{
			name: "version without targets",
			mutate: func(intent *repositoryReviewPurgeIntent) {
				intent.LedgerTargets = nil
			},
		},
		{
			name: "duplicate target",
			mutate: func(intent *repositoryReviewPurgeIntent) {
				intent.LedgerTargets = append(intent.LedgerTargets, intent.LedgerTargets[0])
			},
		},
		{
			name: "missing primary target",
			mutate: func(intent *repositoryReviewPurgeIntent) {
				intent.LedgerTargets = []repositoryReviewPurgeLedgerTarget{{
					Repository: "owner/other", Version: intent.ExpectedRepositoryVersion,
				}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			intent := valid
			intent.LedgerTargets = append(
				[]repositoryReviewPurgeLedgerTarget(nil),
				valid.LedgerTargets...)
			test.mutate(&intent)
			if err := validateRepositoryReviewPurgeIntent(intent); !errors.Is(
				err,
				ErrInvalidAutomation,
			) {
				t.Fatalf("invalid composite intent error = %v", err)
			}
		})
	}
}

func TestRepositoryReviewPurgePreparedInventoryReadFailure(t *testing.T) {
	store := newAutomationTestStore(t)
	automation := createAutomationForTest(t, store, "rra_prepared_inventory", "prepared inventory")
	state := createPurgeTestLedger(t, store, automation.Repository)
	intent := purgeTestIntent(automation, state)
	sentinel := errors.New("injected second inventory read failure")
	loads := 0
	store.loadForTest = func(string) (RepositoryState, error) {
		loads++
		if loads > 1 {
			return RepositoryState{}, sentinel
		}
		return state, nil
	}
	if _, err := store.applyPurgeIntent(intent); !errors.Is(err, sentinel) {
		t.Fatalf("prepared inventory error = %v", err)
	}
}

func TestRepositoryReviewPurgeMultiLedgerRemovalIsAtomicOnFailure(t *testing.T) {
	store := newAutomationTestStore(t)
	first := createPurgeTestLedger(t, store, "owner/aaa")
	second := createPurgeTestLedger(t, store, "owner/zzz")

	err := store.removeRepositoryReviewLedgers([]repositoryReviewPurgeLedgerTarget{
		{Repository: first.Repository, Version: first.Version},
		{Repository: second.Repository, Version: second.Version + 1},
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("multi-ledger removal error = %v", err)
	}
	for _, state := range []RepositoryState{first, second} {
		loaded, found, err := store.Get(state.Repository)
		if err != nil || !found || loaded.Version != state.Version {
			t.Fatalf(
				"ledger %q rolled back=%#v found=%v err=%v",
				state.Repository,
				loaded,
				found,
				err,
			)
		}
	}
}

func TestRepositoryReviewPurgeIntentOwnershipFailureCoverage(t *testing.T) {
	t.Run("save rejects corrupt existing marker", func(t *testing.T) {
		store := newAutomationTestStore(t)
		automation := createAutomationForTest(t, store, "rra_save_corrupt_owner", "corrupt owner")
		state := createPurgeTestLedger(t, store, automation.Repository)
		intent := purgeTestIntent(automation, state)
		if err := os.WriteFile(store.purgeAutomationIntentPath(automation.ID), []byte(`{`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := store.savePurgeIntent(intent); err == nil {
			t.Fatal("intent save replaced a corrupt ownership marker")
		}
	})

	t.Run("remove rejects different owner", func(t *testing.T) {
		store := newAutomationTestStore(t)
		automation := createAutomationForTest(t, store, "rra_remove_other_owner", "other owner")
		state := createPurgeTestLedger(t, store, automation.Repository)
		intent := purgeTestIntent(automation, state)
		if err := store.savePurgeIntent(intent); err != nil {
			t.Fatal(err)
		}
		other := intent
		other.CreatedAt = other.CreatedAt.Add(time.Second)
		if err := store.removePurgeIntent(other); !errors.Is(
			err,
			ErrRepositoryReviewPurgeInProgress,
		) {
			t.Fatalf("different-owner cleanup error = %v", err)
		}
		if _, found, err := store.loadPurgeIntentForAutomation(automation.ID); err != nil ||
			!found {
			t.Fatalf("owned intent found=%v err=%v", found, err)
		}
	})

	t.Run("remove preserves markers on IO failure", func(t *testing.T) {
		store := newAutomationTestStore(t)
		automation := createAutomationForTest(t, store, "rra_remove_io_failure", "remove IO")
		state := createPurgeTestLedger(t, store, automation.Repository)
		intent := purgeTestIntent(automation, state)
		if err := store.savePurgeIntent(intent); err != nil {
			t.Fatal(err)
		}
		paths := store.purgeIntentPaths(intent)
		sentinel := errors.New("injected owned marker removal failure")
		original := repositoryReviewPurgeLstat
		calls := 0
		repositoryReviewPurgeLstat = func(path string) (os.FileInfo, error) {
			calls++
			if calls == len(paths)+1 {
				return nil, sentinel
			}
			return original(path)
		}
		t.Cleanup(func() { repositoryReviewPurgeLstat = original })
		if err := store.removePurgeIntent(intent); !errors.Is(err, sentinel) {
			t.Fatalf("owned marker removal error = %v", err)
		}
		for _, path := range paths {
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("marker %q stat error = %v", path, err)
			}
		}
	})

	if order := repositoryReviewPurgePhaseOrder(repositoryReviewPurgePhase("unknown")); order != 0 {
		t.Fatalf("unknown purge phase order = %d", order)
	}
}

func TestRepositoryReviewPurgePrimaryFencesPartialIntentPublication(t *testing.T) {
	store := newAutomationTestStore(t)
	automation := createAutomationForTest(t, store, "rra_partial_marker", "partial marker")
	state := createPurgeTestLedger(t, store, automation.Repository)
	intent := purgeTestIntent(automation, state)
	original := repositoryReviewPurgeWriteFileAtomic
	sentinel := errors.New("injected later fence write failure")
	writes := 0
	repositoryReviewPurgeWriteFileAtomic = func(path string, data []byte, mode os.FileMode) error {
		writes++
		if writes == 2 {
			return sentinel
		}
		return original(path, data, mode)
	}
	t.Cleanup(func() { repositoryReviewPurgeWriteFileAtomic = original })
	if err := store.savePurgeIntent(intent); !errors.Is(err, sentinel) {
		t.Fatalf("partial marker error = %v", err)
	}
	repositoryReviewPurgeWriteFileAtomic = original
	if _, err := os.Stat(store.purgeAutomationIntentPath(automation.ID)); err != nil {
		t.Fatalf("primary recovery intent missing: %v", err)
	}
	if _, err := os.Stat(store.purgeRepositoryFencePath(state.Repository)); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("later exact fence unexpectedly exists: %v", err)
	}
	if _, _, err := store.Get(state.Repository); !errors.Is(
		err,
		ErrRepositoryReviewPurgeInProgress,
	) {
		t.Fatalf("primary fallback fence error = %v", err)
	}
	if count, err := store.ReconcilePurgeIntents(context.Background()); err != nil || count != 1 {
		t.Fatalf("partial marker reconcile count=%d err=%v", count, err)
	}
}

func TestRepositoryReviewPurgeSQLiteRemovalFailureCoverage(t *testing.T) {
	sentinel := errors.New("injected purge database failure")
	t.Run("automation open", func(t *testing.T) {
		store := Store{openForTest: func(context.Context) (*sql.DB, error) { return nil, sentinel }}
		if err := store.removeRepositoryReviewAutomation(repositoryReviewPurgeIntent{}); !errors.Is(
			err,
			sentinel,
		) {
			t.Fatalf("automation open error = %v", err)
		}
	})
	t.Run("automation absent", func(t *testing.T) {
		store := newAutomationTestStore(t)
		if err := store.removeRepositoryReviewAutomation(repositoryReviewPurgeIntent{
			AutomationID: "rra_absent_sql_delete",
		}); err != nil {
			t.Fatalf("absent automation delete error = %v", err)
		}
	})
	t.Run("automation query", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "raw.db")
		store := Store{root: filepath.Dir(path)}
		store.openForTest = func(context.Context) (*sql.DB, error) { return sql.Open("sqlite", path) }
		if err := store.removeRepositoryReviewAutomation(repositoryReviewPurgeIntent{
			AutomationID: "rra_missing_table",
		}); err == nil {
			t.Fatal("automation delete accepted a missing table")
		}
	})
	t.Run("automation mismatch", func(t *testing.T) {
		store := newAutomationTestStore(t)
		automation := createAutomationForTest(t, store, "rra_delete_mismatch", "delete mismatch")
		intent := repositoryReviewPurgeIntent{
			AutomationID: automation.ID, ConfiguredRepository: automation.Repository,
			ExpectedAutomationVersion: automation.Version + 1,
		}
		if err := store.removeRepositoryReviewAutomation(intent); !errors.Is(err, ErrConflict) {
			t.Fatalf("automation mismatch error = %v", err)
		}
	})
	t.Run("automation delete", func(t *testing.T) {
		store := newAutomationTestStore(t)
		automation := createAutomationForTest(t, store, "rra_delete_trigger", "delete trigger")
		database, err := store.openDatabase(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(t.Context(), `
			CREATE TRIGGER reject_repository_review_automation_purge
			BEFORE DELETE ON repository_review_automations
			BEGIN
				SELECT RAISE(FAIL, 'injected automation purge failure');
			END`); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
		_ = database.Close()
		if err := store.removeRepositoryReviewAutomation(repositoryReviewPurgeIntent{
			AutomationID: automation.ID, ConfiguredRepository: automation.Repository,
			ExpectedAutomationVersion: automation.Version,
		}); err == nil {
			t.Fatal("automation delete ignored its trigger failure")
		}
	})
	t.Run("ledger absent", func(t *testing.T) {
		store := newAutomationTestStore(t)
		if err := store.removeRepositoryReviewLedger("owner/absent-ledger"); err != nil {
			t.Fatalf("absent ledger removal error = %v", err)
		}
	})
	t.Run("ledger open", func(t *testing.T) {
		store := Store{openForTest: func(context.Context) (*sql.DB, error) { return nil, sentinel }}
		if err := store.removeRepositoryReviewLedgers(nil); !errors.Is(err, sentinel) {
			t.Fatalf("ledger open error = %v", err)
		}
	})
	t.Run("ledger query", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "raw.db")
		store := Store{root: filepath.Dir(path)}
		store.openForTest = func(context.Context) (*sql.DB, error) { return sql.Open("sqlite", path) }
		if err := store.removeRepositoryReviewLedgers([]repositoryReviewPurgeLedgerTarget{{
			Repository: "owner/missing-table", Version: 1,
		}}); err == nil {
			t.Fatal("ledger delete accepted a missing table")
		}
	})
	t.Run("ledger ignored delete", func(t *testing.T) {
		store := newAutomationTestStore(t)
		state := createPurgeTestLedger(t, store, "owner/ignored-delete")
		database, err := store.openDatabase(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(t.Context(), `
			CREATE TRIGGER ignore_repository_review_ledger_purge
			BEFORE DELETE ON repository_review_states
			BEGIN
				SELECT RAISE(IGNORE);
			END`); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
		_ = database.Close()
		if err := store.removeRepositoryReviewLedgers([]repositoryReviewPurgeLedgerTarget{{
			Repository: state.Repository, Version: state.Version,
		}}); !errors.Is(err, ErrConflict) {
			t.Fatalf("ignored ledger delete error = %v", err)
		}
	})
}

func TestRepositoryReviewPurgeArchiveFailureCoverage(t *testing.T) {
	t.Run("apply archive removal", func(t *testing.T) {
		fixture := newPurgePhaseFixture(t, repositoryReviewPurgeLedgerCommitting, true)
		original := repositoryReviewPurgeRootLstat
		sentinel := errors.New("injected archive phase root failure")
		calls := 0
		repositoryReviewPurgeRootLstat = func(path string) (os.FileInfo, error) {
			calls++
			if calls == 1 {
				return nil, sentinel
			}
			return original(path)
		}
		t.Cleanup(func() { repositoryReviewPurgeRootLstat = original })
		if _, err := fixture.store.applyPurgeIntent(fixture.intent); err == nil {
			t.Fatal("ledger phase ignored legacy-source removal failure")
		}
	})
	t.Run("unsafe source root", func(t *testing.T) {
		store := NewStore(t.TempDir())
		if err := os.WriteFile(store.root, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := store.removeRepositoryReviewPurgeArchives(repositoryReviewPurgeIntent{}); err == nil {
			t.Fatal("archive cleanup accepted an unsafe source root")
		}
	})
	t.Run("source file", func(t *testing.T) {
		store := newAutomationTestStore(t)
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		intent := repositoryReviewPurgeIntent{AutomationID: "rra_archive_source_failure"}
		if err := os.Mkdir(filepath.Join(store.root, automationFilename(intent.AutomationID)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := store.removeRepositoryReviewPurgeArchives(intent); err == nil {
			t.Fatal("archive cleanup accepted a non-file source")
		}
	})
	t.Run("archive file", func(t *testing.T) {
		store := newAutomationTestStore(t)
		archive := filepath.Join(store.root, "legacy-json", repositoryReviewLegacyArchiveLabel)
		if err := os.MkdirAll(archive, 0o700); err != nil {
			t.Fatal(err)
		}
		name := automationFilename("rra_archive_file_failure")
		if err := os.Mkdir(filepath.Join(archive, name), 0o700); err != nil {
			t.Fatal(err)
		}
		root, found, err := openRepositoryReviewPurgeArchiveRoot(store.root)
		if err != nil || !found {
			t.Fatalf("archive root found=%v err=%v", found, err)
		}
		defer root.Close()
		if err := removeRepositoryReviewAuditedArchive(root, repositoryReviewPurgeArchiveRecord{
			Name: name, Limit: 1, Mode: 0o600,
		}); err == nil {
			t.Fatal("archive cleanup accepted a non-file archive")
		}
	})
	t.Run("audited archive", func(t *testing.T) {
		store := newAutomationTestStore(t)
		archive := filepath.Join(store.root, "legacy-json", repositoryReviewLegacyArchiveLabel)
		if err := os.MkdirAll(archive, 0o700); err != nil {
			t.Fatal(err)
		}
		name := "automation_rra_audited_archive.json"
		data := []byte(`{"safe":true}`)
		if err := os.WriteFile(filepath.Join(archive, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		root, found, err := openRepositoryReviewPurgeArchiveRoot(store.root)
		if err != nil || !found {
			t.Fatalf("archive root found=%v err=%v", found, err)
		}
		defer root.Close()
		digest := sha256.Sum256(data)
		if err := removeRepositoryReviewAuditedArchive(root, repositoryReviewPurgeArchiveRecord{
			Name: name, Digest: digest, Limit: int64(len(data)), Mode: 0o600,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(archive, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("audited archive remains: %v", err)
		}
	})
	t.Run("archive ancestor", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "legacy-json"), []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := openRepositoryReviewPurgeArchiveRoot(root); err == nil {
			t.Fatal("archive helper accepted a non-directory ancestor")
		}
	})
	t.Run("root lstat", func(t *testing.T) {
		original := repositoryReviewPurgeRootLstat
		sentinel := errors.New("injected root lstat failure")
		repositoryReviewPurgeRootLstat = func(string) (os.FileInfo, error) { return nil, sentinel }
		t.Cleanup(func() { repositoryReviewPurgeRootLstat = original })
		if err := repositoryReviewPurgeRequireRoot("ignored", false); !errors.Is(err, sentinel) {
			t.Fatalf("root lstat error = %v", err)
		}
	})
	t.Run("root mkdir", func(t *testing.T) {
		original := repositoryReviewPurgeMkdir
		sentinel := errors.New("injected root mkdir failure")
		repositoryReviewPurgeMkdir = func(string, os.FileMode) error { return sentinel }
		t.Cleanup(func() { repositoryReviewPurgeMkdir = original })
		if err := repositoryReviewPurgeEnsureRoot(filepath.Join(t.TempDir(), "missing")); !errors.Is(
			err,
			sentinel,
		) {
			t.Fatalf("root mkdir error = %v", err)
		}
	})
}
