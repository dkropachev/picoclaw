//go:build linux

package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionStageMarginRegularGuards(t *testing.T) {
	newRegular := func(t *testing.T) (*applyPatchTxnIntentPlan, *applyPatchTransactionJournal) {
		t.Helper()
		intent, journal, _ := newApplyPatchTxnStageCloseoutFixture(
			t,
			"*** Begin Patch\n*** Add File: result.txt\n+result\n*** End Patch",
		)
		return intent, journal
	}
	stagePath := func(intent *applyPatchTxnIntentPlan) string {
		op := intent.operations[0]
		return filepath.Join(op.targetAnchor.canonical, op.stageName)
	}

	t.Run("occupied stage", func(t *testing.T) {
		intent, journal := newRegular(t)
		defer intent.Close()
		if err := os.WriteFile(stagePath(intent), []byte("occupied\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := stageApplyPatchTxnRegularPostimage(
			context.Background(), intent.operations[0], journal,
			func(*applyPatchTransactionJournal) error { return nil },
		); err == nil {
			t.Fatal("occupied regular stage was accepted")
		}
	})
	t.Run("missing stage artifact", func(t *testing.T) {
		intent, journal := newRegular(t)
		defer intent.Close()
		for index := range journal.Artifacts {
			if journal.Artifacts[index].Role == applyPatchTransactionArtifactPostimageStage {
				journal.Artifacts = append(journal.Artifacts[:index], journal.Artifacts[index+1:]...)
				break
			}
		}
		if err := stageApplyPatchTxnRegularPostimage(
			context.Background(), intent.operations[0], journal,
			func(*applyPatchTransactionJournal) error { return nil },
		); err == nil {
			t.Fatal("missing regular stage artifact was accepted")
		}
	})
	t.Run("canceled write", func(t *testing.T) {
		intent, journal := newRegular(t)
		defer intent.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := stageApplyPatchTxnRegularPostimage(
			ctx, intent.operations[0], journal,
			func(*applyPatchTransactionJournal) error { return nil },
		); err == nil {
			t.Fatal("canceled regular stage write was accepted")
		}
	})
	t.Run("post-write sync", func(t *testing.T) {
		intent, journal := newRegular(t)
		defer intent.Close()
		err := stageApplyPatchTxnRegularPostimage(
			context.Background(), intent.operations[0], journal,
			func(*applyPatchTransactionJournal) error {
				return intent.operations[0].targetAnchor.Close()
			},
		)
		if err == nil {
			t.Fatal("closed regular stage anchor was accepted after write")
		}
	})
	t.Run("stage identity changed", func(t *testing.T) {
		intent, journal := newRegular(t)
		defer intent.Close()
		op := intent.operations[0]
		path := stagePath(intent)
		err := stageApplyPatchTxnRegularPostimage(
			context.Background(), op, journal,
			func(*applyPatchTransactionJournal) error {
				if err := os.Rename(path, path+".old"); err != nil {
					return err
				}
				mode := os.FileMode(journal.Operations[0].After.Mode)
				if err := os.WriteFile(path, op.planned.after, mode); err != nil {
					return err
				}
				return os.Chmod(path, mode)
			},
		)
		if err == nil {
			t.Fatal("replaced regular stage was accepted")
		}
	})
	t.Run("missing witness artifact", func(t *testing.T) {
		intent, journal := newRegular(t)
		defer intent.Close()
		for index := range journal.Artifacts {
			if journal.Artifacts[index].Role == applyPatchTransactionArtifactPostimageWitness {
				journal.Artifacts = append(journal.Artifacts[:index], journal.Artifacts[index+1:]...)
				break
			}
		}
		if err := stageApplyPatchTxnRegularPostimage(
			context.Background(), intent.operations[0], journal,
			func(*applyPatchTransactionJournal) error { return nil },
		); err == nil {
			t.Fatal("missing regular witness artifact was accepted")
		}
	})
	t.Run("occupied witness", func(t *testing.T) {
		intent, journal := newRegular(t)
		defer intent.Close()
		op := intent.operations[0]
		calls := 0
		err := stageApplyPatchTxnRegularPostimage(
			context.Background(), op, journal,
			func(*applyPatchTransactionJournal) error {
				calls++
				if calls == 2 {
					return os.WriteFile(
						filepath.Join(op.targetAnchor.canonical, op.postWitnessName),
						[]byte("occupied\n"),
						0o600,
					)
				}
				return nil
			},
		)
		if err == nil {
			t.Fatal("occupied regular witness was accepted")
		}
	})
	t.Run("witness expected state changed", func(t *testing.T) {
		intent, journal := newRegular(t)
		defer intent.Close()
		calls := 0
		err := stageApplyPatchTxnRegularPostimage(
			context.Background(), intent.operations[0], journal,
			func(*applyPatchTransactionJournal) error {
				calls++
				if calls == 2 {
					journal.Operations[0].After.Length++
				}
				return nil
			},
		)
		if err == nil {
			t.Fatal("changed regular witness expectation was accepted")
		}
	})
	t.Run("initial directory sync", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory read permissions")
		}
		intent, journal := newRegular(t)
		defer intent.Close()
		directory := intent.operations[0].targetAnchor.canonical
		if err := os.Chmod(directory, 0o300); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
		if err := stageApplyPatchTxnRegularPostimage(
			context.Background(), intent.operations[0], journal,
			func(*applyPatchTransactionJournal) error { return nil },
		); err == nil {
			t.Fatal("unsyncable regular stage directory was accepted")
		}
	})
	t.Run("post-link directory sync", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory read permissions")
		}
		intent, journal := newRegular(t)
		defer intent.Close()
		directory := intent.operations[0].targetAnchor.canonical
		t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
		calls := 0
		err := stageApplyPatchTxnRegularPostimage(
			context.Background(), intent.operations[0], journal,
			func(*applyPatchTransactionJournal) error {
				calls++
				if calls == 2 {
					return os.Chmod(directory, 0o300)
				}
				return nil
			},
		)
		if err == nil {
			t.Fatal("unsyncable linked witness directory was accepted")
		}
	})
}

func TestApplyPatchTransactionStageMarginForestGuards(t *testing.T) {
	newForest := func(t *testing.T) (*applyPatchTxnIntentPlan, *applyPatchTransactionJournal) {
		t.Helper()
		intent, journal, _ := newApplyPatchTxnStageCloseoutFixture(
			t,
			"*** Begin Patch\n*** Add File: nested/result.txt\n+result\n*** End Patch",
		)
		return intent, journal
	}

	t.Run("missing journal forest", func(t *testing.T) {
		intent, journal := newForest(t)
		defer intent.Close()
		journal.Forests = nil
		if err := stageApplyPatchTxnForest(
			context.Background(), intent.forests[0], journal,
			func(*applyPatchTransactionJournal) error { return nil },
		); err == nil {
			t.Fatal("missing staging forest was accepted")
		}
	})
	t.Run("occupied stage root", func(t *testing.T) {
		intent, journal := newForest(t)
		defer intent.Close()
		forest := intent.forests[0]
		if err := os.Mkdir(filepath.Join(forest.anchorPath, forest.stageRoot), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := stageApplyPatchTxnForest(
			context.Background(), forest, journal,
			func(*applyPatchTransactionJournal) error { return nil },
		); err == nil {
			t.Fatal("occupied forest stage root was accepted")
		}
	})
	t.Run("canceled entry loop", func(t *testing.T) {
		intent, journal := newForest(t)
		defer intent.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := stageApplyPatchTxnForest(
			ctx, intent.forests[0], journal,
			func(*applyPatchTransactionJournal) error {
				cancel()
				return nil
			},
		)
		if err == nil {
			t.Fatal("canceled forest entry loop was accepted")
		}
	})
	t.Run("missing staged parent", func(t *testing.T) {
		intent, journal := newForest(t)
		defer intent.Close()
		entry := &journal.Forests[0].Entries[1]
		entry.RelativePath = "missing/result.txt"
		if err := stageApplyPatchTxnForest(
			context.Background(), intent.forests[0], journal,
			func(*applyPatchTransactionJournal) error { return nil },
		); err == nil {
			t.Fatal("missing forest parent was accepted")
		}
	})
	t.Run("occupied forest directory", func(t *testing.T) {
		intent, journal, _ := newApplyPatchTxnStageCloseoutFixture(
			t,
			"*** Begin Patch\n*** Add File: nested/deeper/result.txt\n+result\n*** End Patch",
		)
		defer intent.Close()
		forest := intent.forests[0]
		var directoryEntry applyPatchTransactionJournalForestEntry
		for _, entry := range journal.Forests[0].Entries {
			if entry.Kind == "directory" && entry.RelativePath != "." {
				directoryEntry = entry
				break
			}
		}
		if directoryEntry.RelativePath == "" {
			t.Fatal("forest directory entry is unavailable")
		}
		err := stageApplyPatchTxnForest(
			context.Background(), forest, journal,
			func(*applyPatchTransactionJournal) error {
				return os.MkdirAll(
					filepath.Join(
						forest.anchorPath,
						forest.stageRoot,
						filepath.FromSlash(directoryEntry.RelativePath),
					),
					0o700,
				)
			},
		)
		if err == nil {
			t.Fatal("occupied forest directory was accepted")
		}
	})
	t.Run("unbound file", func(t *testing.T) {
		intent, journal := newForest(t)
		defer intent.Close()
		journal.Forests[0].Entries[1].OperationIndex = nil
		if err := stageApplyPatchTxnForest(
			context.Background(), intent.forests[0], journal,
			func(*applyPatchTransactionJournal) error { return nil },
		); err == nil {
			t.Fatal("unbound forest file was accepted")
		}
	})
	t.Run("missing operation", func(t *testing.T) {
		intent, journal := newForest(t)
		defer intent.Close()
		missing := 99
		journal.Forests[0].Entries[1].OperationIndex = &missing
		if err := stageApplyPatchTxnForest(
			context.Background(), intent.forests[0], journal,
			func(*applyPatchTransactionJournal) error { return nil },
		); err == nil {
			t.Fatal("missing forest operation was accepted")
		}
	})
	t.Run("occupied forest file", func(t *testing.T) {
		intent, journal := newForest(t)
		defer intent.Close()
		forest := intent.forests[0]
		entry := journal.Forests[0].Entries[1]
		calls := 0
		err := stageApplyPatchTxnForest(
			context.Background(), forest, journal,
			func(*applyPatchTransactionJournal) error {
				calls++
				if calls == 1 {
					return os.WriteFile(
						filepath.Join(forest.anchorPath, forest.stageRoot, filepath.FromSlash(entry.RelativePath)),
						[]byte("occupied\n"),
						0o600,
					)
				}
				return nil
			},
		)
		if err == nil {
			t.Fatal("occupied forest file was accepted")
		}
	})
	t.Run("canceled forest write", func(t *testing.T) {
		intent, journal := newForest(t)
		defer intent.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		err := stageApplyPatchTxnForest(
			ctx, intent.forests[0], journal,
			func(*applyPatchTransactionJournal) error {
				calls++
				if calls == 2 {
					cancel()
				}
				return nil
			},
		)
		if err == nil {
			t.Fatal("canceled forest write was accepted")
		}
	})
	t.Run("forest file expected state changed", func(t *testing.T) {
		intent, journal := newForest(t)
		defer intent.Close()
		calls := 0
		err := stageApplyPatchTxnForest(
			context.Background(), intent.forests[0], journal,
			func(*applyPatchTransactionJournal) error {
				calls++
				if calls == 2 {
					journal.Operations[0].After.Length++
				}
				return nil
			},
		)
		if err == nil {
			t.Fatal("changed forest file expectation was accepted")
		}
	})
	t.Run("missing sentinel", func(t *testing.T) {
		intent, journal := newForest(t)
		defer intent.Close()
		if err := stageApplyPatchTxnForestSentinel(
			intent.forests[0], journal, &journal.Forests[0],
			func(*applyPatchTransactionJournal) error { return nil },
		); err == nil {
			t.Fatal("missing forest sentinel was accepted")
		}
	})
	t.Run("missing sentinel parent", func(t *testing.T) {
		intent, journal := newForest(t)
		defer intent.Close()
		entry := &journal.Forests[0].Entries[1]
		entry.Identity = &applyPatchTxnIdentity{Device: 1, File: 1, Kind: "regular"}
		if err := stageApplyPatchTxnForestSentinel(
			intent.forests[0], journal, &journal.Forests[0],
			func(*applyPatchTransactionJournal) error { return nil },
		); err == nil {
			t.Fatal("missing forest sentinel parent was accepted")
		}
	})
	t.Run("occupied sentinel witness", func(t *testing.T) {
		intent, journal := newForest(t)
		defer intent.Close()
		forest := intent.forests[0]
		calls := 0
		err := stageApplyPatchTxnForest(
			context.Background(), forest, journal,
			func(*applyPatchTransactionJournal) error {
				calls++
				if calls == 3 {
					return os.WriteFile(
						filepath.Join(forest.anchorPath, forest.sentinelWitnessName),
						[]byte("occupied\n"),
						0o600,
					)
				}
				return nil
			},
		)
		if err == nil {
			t.Fatal("occupied forest sentinel witness was accepted")
		}
	})
	t.Run("sentinel expected state changed", func(t *testing.T) {
		intent, journal := newForest(t)
		defer intent.Close()
		calls := 0
		err := stageApplyPatchTxnForest(
			context.Background(), intent.forests[0], journal,
			func(*applyPatchTransactionJournal) error {
				calls++
				if calls == 3 {
					for index := range journal.Forests[0].Entries {
						entry := &journal.Forests[0].Entries[index]
						if entry.RelativePath == journal.Forests[0].SentinelRelativePath {
							entry.Length++
							break
						}
					}
				}
				return nil
			},
		)
		if err == nil {
			t.Fatal("changed forest sentinel expectation was accepted")
		}
	})
	t.Run("permission sync failures", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory read permissions")
		}
		t.Run("stage root", func(t *testing.T) {
			intent, journal := newForest(t)
			defer intent.Close()
			forest := intent.forests[0]
			if err := os.Chmod(forest.anchorPath, 0o300); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(forest.anchorPath, 0o700) })
			if err := stageApplyPatchTxnForest(
				context.Background(), forest, journal,
				func(*applyPatchTransactionJournal) error { return nil },
			); err == nil {
				t.Fatal("unsyncable forest anchor was accepted")
			}
		})
		t.Run("directory entry", func(t *testing.T) {
			intent, journal, _ := newApplyPatchTxnStageCloseoutFixture(
				t,
				"*** Begin Patch\n*** Add File: nested/deeper/result.txt\n+result\n*** End Patch",
			)
			defer intent.Close()
			forest := intent.forests[0]
			stageRoot := filepath.Join(forest.anchorPath, forest.stageRoot)
			t.Cleanup(func() { _ = os.Chmod(stageRoot, 0o700) })
			if err := stageApplyPatchTxnForest(
				context.Background(), forest, journal,
				func(*applyPatchTransactionJournal) error {
					return os.Chmod(stageRoot, 0o300)
				},
			); err == nil {
				t.Fatal("unsyncable forest directory entry was accepted")
			}
		})
		t.Run("file creation", func(t *testing.T) {
			intent, journal := newForest(t)
			defer intent.Close()
			forest := intent.forests[0]
			stageRoot := filepath.Join(forest.anchorPath, forest.stageRoot)
			t.Cleanup(func() { _ = os.Chmod(stageRoot, 0o700) })
			if err := stageApplyPatchTxnForest(
				context.Background(), forest, journal,
				func(*applyPatchTransactionJournal) error {
					return os.Chmod(stageRoot, 0o300)
				},
			); err == nil {
				t.Fatal("unsyncable forest file creation was accepted")
			}
		})
		t.Run("file post-write", func(t *testing.T) {
			intent, journal := newForest(t)
			defer intent.Close()
			forest := intent.forests[0]
			stageRoot := filepath.Join(forest.anchorPath, forest.stageRoot)
			t.Cleanup(func() { _ = os.Chmod(stageRoot, 0o700) })
			calls := 0
			if err := stageApplyPatchTxnForest(
				context.Background(), forest, journal,
				func(*applyPatchTransactionJournal) error {
					calls++
					if calls == 2 {
						return os.Chmod(stageRoot, 0o300)
					}
					return nil
				},
			); err == nil {
				t.Fatal("unsyncable written forest file was accepted")
			}
		})
		t.Run("sentinel link", func(t *testing.T) {
			intent, journal := newForest(t)
			defer intent.Close()
			forest := intent.forests[0]
			t.Cleanup(func() { _ = os.Chmod(forest.anchorPath, 0o700) })
			calls := 0
			if err := stageApplyPatchTxnForest(
				context.Background(), forest, journal,
				func(*applyPatchTransactionJournal) error {
					calls++
					if calls == 3 {
						return os.Chmod(forest.anchorPath, 0o300)
					}
					return nil
				},
			); err == nil {
				t.Fatal("unsyncable forest sentinel witness was accepted")
			}
		})
	})
}
