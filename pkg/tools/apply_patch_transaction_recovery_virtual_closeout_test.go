package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionRecoveryVirtualCloseoutRootedArtifactConflicts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *applyPatchPreparedTransaction)
	}{
		{
			"anchor identity",
			func(_ *testing.T, tx *applyPatchPreparedTransaction) {
				stage, _ := requireApplyPatchTxnArtifact(
					tx.journal, 0, applyPatchTransactionArtifactPostimageStage,
				)
				stage.Rooted.AnchorIdentity.File++
			},
		},
		{
			"artifact identity",
			func(_ *testing.T, tx *applyPatchPreparedTransaction) {
				stage, _ := requireApplyPatchTxnArtifact(
					tx.journal, 0, applyPatchTransactionArtifactPostimageStage,
				)
				stage.Rooted.Identity.File++
			},
		},
		{
			"artifact content",
			func(t *testing.T, tx *applyPatchPreparedTransaction) {
				stage, _ := requireApplyPatchTxnArtifact(
					tx.journal, 0, applyPatchTransactionArtifactPostimageStage,
				)
				path := filepath.Join(stage.Rooted.AnchorCanonicalPath, stage.Rooted.Basename)
				if err := os.WriteFile(path, []byte("drift\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			"artifact link count",
			func(t *testing.T, tx *applyPatchPreparedTransaction) {
				stage, _ := requireApplyPatchTxnArtifact(
					tx.journal, 0, applyPatchTransactionArtifactPostimageStage,
				)
				source := filepath.Join(stage.Rooted.AnchorCanonicalPath, stage.Rooted.Basename)
				alien := filepath.Join(stage.Rooted.AnchorCanonicalPath, "undeclared-hardlink")
				if err := os.Link(source, alien); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
			tx := fixture.begin(t)
			defer tx.abortPreparing()
			test.mutate(t, tx)
			if err := validateApplyPatchTxnVirtualRootedArtifacts(tx); err == nil {
				t.Fatal("invalid virtual rooted artifact was accepted")
			}
		})
	}
}

func TestApplyPatchTransactionRecoveryVirtualCloseoutAliasAndForestConflicts(t *testing.T) {
	t.Run("alias anchor", func(t *testing.T) {
		fixture := newApplyPatchTxnRecoveryFixture(t, "result.txt")
		tx := fixture.begin(t)
		defer tx.abortPreparing()
		stage, _ := requireApplyPatchTxnArtifact(
			tx.journal, 0, applyPatchTransactionArtifactPostimageStage,
		)
		stage.Rooted.AnchorIdentity.File++
		if _, err := collectApplyPatchTxnVirtualRegularAliases(tx.intent, tx.journal); err == nil {
			t.Fatal("changed alias anchor was accepted")
		}
	})
	t.Run("forest witness", func(t *testing.T) {
		fixture := newApplyPatchTxnRecoveryFixture(t, "nested/deeper/result.txt")
		tx := fixture.begin(t)
		defer tx.abortPreparing()
		aliases, err := collectApplyPatchTxnVirtualRegularAliases(tx.intent, tx.journal)
		if err != nil {
			t.Fatal(err)
		}
		forest := &tx.journal.Forests[0]
		forest.SentinelRelativePath = "missing"
		if err := validateApplyPatchTxnVirtualForestWitnesses(tx, aliases); err == nil {
			t.Fatal("missing virtual forest sentinel was accepted")
		}
	})
	t.Run("preparing forest drift", func(t *testing.T) {
		fixture := newApplyPatchTxnRecoveryFixture(t, "nested/deeper/result.txt")
		tx := fixture.begin(t)
		defer tx.abortPreparing()
		if err := validateApplyPatchTxnPreparingVirtualForests(tx); err != nil {
			t.Fatalf("valid preparing virtual forest = %v", err)
		}
		forest := &tx.journal.Forests[0]
		entry := &forest.Entries[len(forest.Entries)-1]
		path := filepath.Join(tx.intent.forests[0].anchorPath, tx.intent.forests[0].stageRoot)
		path = filepath.Join(path, filepath.FromSlash(entry.RelativePath))
		if err := os.WriteFile(path, []byte("drift\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := validateApplyPatchTxnPreparingVirtualForests(tx); err == nil {
			t.Fatal("drifted preparing virtual forest was accepted")
		}
	})
}

func TestApplyPatchTransactionRecoveryVirtualCloseoutForestRemovalReconciliation(t *testing.T) {
	for _, state := range []string{"attempted absent", "unexpected", "attempted present", "identity conflict"} {
		t.Run(state, func(t *testing.T) {
			fixture := newApplyPatchTxnRecoveryFixture(t, "nested/deeper/result.txt")
			tx := fixture.begin(t)
			defer tx.abortPreparing()
			initializeApplyPatchTxnCrashEffects(tx)
			intent := tx.intent.forests[0]
			forest := &tx.journal.Forests[0]
			entry := &forest.Entries[len(forest.Entries)-1]
			rootPath := filepath.Join(intent.anchorPath, intent.stageRoot)
			parentPath := filepath.Join(
				rootPath,
				filepath.Dir(filepath.FromSlash(entry.RelativePath)),
			)
			basename := filepath.Base(filepath.FromSlash(entry.RelativePath))
			source := filepath.Join(parentPath, basename)
			removal := filepath.Join(parentPath, entry.RemovalBasename)
			switch state {
			case "attempted absent":
				entry.RemovalAttempted = true
			case "unexpected":
				if err := os.Link(source, removal); err != nil {
					t.Fatal(err)
				}
			case "attempted present":
				entry.RemovalAttempted = true
				if err := os.Rename(source, removal); err != nil {
					t.Fatal(err)
				}
			case "identity conflict":
				entry.RemovalAttempted = true
				if err := os.WriteFile(removal, []byte("alien\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := reconcileApplyPatchTxnForestEntryRemovalQuarantines(tx)
			if state == "attempted absent" || state == "attempted present" {
				if err != nil {
					t.Fatalf("reconciled forest removal = %v", err)
				}
			} else if err == nil {
				t.Fatal("conflicting forest removal reconciled")
			}
		})
	}
}

func TestApplyPatchTransactionRecoveryVirtualCloseoutPreflightGuards(t *testing.T) {
	if err := validateApplyPatchTxnPreparingVirtualForests(
		&applyPatchPreparedTransaction{
			journal: &applyPatchTransactionJournal{Phase: applyPatchTransactionPhasePrepared},
		},
	); err != nil {
		t.Fatalf("non-preparing virtual forest validation = %v", err)
	}
}

func TestApplyPatchTransactionRecoveryVirtualCloseoutForestEntryNameStates(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*testing.T, *applyPatchPreparedTransaction)
		wantErr bool
	}{
		{
			name: "uncheckpointed forest skipped",
			mutate: func(_ *testing.T, tx *applyPatchPreparedTransaction) {
				tx.journal.Forests[0].StageRoot.Identity = nil
			},
		},
		{
			name: "absent forest root skipped",
			mutate: func(t *testing.T, tx *applyPatchPreparedTransaction) {
				intent := tx.intent.forests[0]
				if err := os.Rename(
					filepath.Join(intent.anchorPath, intent.stageRoot),
					filepath.Join(intent.anchorPath, "detached-root"),
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "uncheckpointed existing entry",
			mutate: func(_ *testing.T, tx *applyPatchPreparedTransaction) {
				forest := &tx.journal.Forests[0]
				forest.Entries[len(forest.Entries)-1].Identity = nil
			},
			wantErr: true,
		},
		{
			name: "missing parent skipped",
			mutate: func(t *testing.T, tx *applyPatchPreparedTransaction) {
				forest := &tx.journal.Forests[0]
				intent := tx.intent.forests[0]
				for _, entry := range forest.Entries {
					if entry.Kind == "directory" && entry.RelativePath != "." {
						path := filepath.Join(
							intent.anchorPath,
							intent.stageRoot,
							filepath.FromSlash(entry.RelativePath),
						)
						if err := os.RemoveAll(path); err != nil {
							t.Fatal(err)
						}
						return
					}
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApplyPatchTxnRecoveryFixture(t, "nested/deeper/result.txt")
			tx := fixture.begin(t)
			defer tx.abortPreparing()
			test.mutate(t, tx)
			err := applyApplyPatchTxnVirtualForestEntryNames(tx.intent, tx.journal)
			if test.wantErr && err == nil {
				t.Fatal("invalid virtual forest entry state was accepted")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("valid virtual forest entry state = %v", err)
			}
		})
	}
}
