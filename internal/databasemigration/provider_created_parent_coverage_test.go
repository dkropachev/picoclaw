//go:build (unix && !aix) || windows

package databasemigration

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

type providerCreatedParentCoverageDirEntry string

func (entry providerCreatedParentCoverageDirEntry) Name() string         { return string(entry) }
func (providerCreatedParentCoverageDirEntry) IsDir() bool                { return false }
func (providerCreatedParentCoverageDirEntry) Type() os.FileMode          { return 0 }
func (providerCreatedParentCoverageDirEntry) Info() (os.FileInfo, error) { return nil, nil }

func newProviderCreatedParentWalkFixture(
	t *testing.T,
) (string, string, *legacyExactExclusion, *backupBudget, providerCreatedTargetParentWalkOps) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repository_reviews")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, ".repository-reviews.db.migration-stage-test")
	if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	parentIdentity, parentType, exists, err := fileidentity.ExistingWithType(root)
	if err != nil || !exists || parentType != fileidentity.ObjectTypeDirectory {
		t.Fatal(err)
	}
	stageIdentity, stageType, exists, err := fileidentity.ExistingWithType(stage)
	if err != nil || !exists || stageType != fileidentity.ObjectTypeRegular {
		t.Fatal(err)
	}
	exact := &legacyExactExclusion{
		path:         stage,
		targetParent: root,
		validate: func(context.Context, string, os.FileInfo) error {
			return nil
		},
		validateTargetParent: func(
			context.Context,
			string,
			os.FileInfo,
		) (fileidentity.Identity, error) {
			return parentIdentity, nil
		},
	}
	ops := providerCreatedTargetParentWalkOps{
		lstat: os.Lstat,
		open: func(path string) (*os.File, os.FileInfo, error) {
			return openPinnedBackupPath(path, true)
		},
		opened: fileidentity.Opened,
		read:   readBackupDirectoryBatch,
		close:  func(file *os.File) error { return file.Close() },
	}
	_ = stageIdentity
	return root, stage, exact, newBackupBudget(), ops
}

func TestProviderCreatedTargetParentWalkFaultCoverage(t *testing.T) {
	root, stage, exact, budget, validOps := newProviderCreatedParentWalkFixture(t)
	canary := errors.New("provider-created target-parent walk canary")
	if err := walkProviderCreatedTargetParentWithOps(
		t.Context(), root, exact, budget, providerCreatedTargetParentWalkOps{},
	); err == nil {
		t.Fatal("incomplete created-parent walk ops succeeded")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := walkProviderCreatedTargetParentWithOps(
		canceled, root, exact, newBackupBudget(), validOps,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled created-parent walk = %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(string, *legacyExactExclusion, *providerCreatedTargetParentWalkOps)
	}{
		{
			name: "root lstat",
			mutate: func(_ string, _ *legacyExactExclusion, ops *providerCreatedTargetParentWalkOps) {
				ops.lstat = func(string) (os.FileInfo, error) { return nil, canary }
			},
		},
		{
			name: "parent proof",
			mutate: func(_ string, exact *legacyExactExclusion, _ *providerCreatedTargetParentWalkOps) {
				exact.validateTargetParent = func(
					context.Context,
					string,
					os.FileInfo,
				) (fileidentity.Identity, error) {
					return fileidentity.Identity{}, canary
				}
			},
		},
		{
			name: "open",
			mutate: func(_ string, _ *legacyExactExclusion, ops *providerCreatedTargetParentWalkOps) {
				ops.open = func(string) (*os.File, os.FileInfo, error) { return nil, nil, canary }
			},
		},
		{
			name: "opened identity",
			mutate: func(_ string, _ *legacyExactExclusion, ops *providerCreatedTargetParentWalkOps) {
				ops.opened = func(*os.File) (
					fileidentity.Identity,
					fileidentity.ObjectType,
					error,
				) {
					return fileidentity.Identity{}, fileidentity.ObjectType(0), canary
				}
			},
		},
		{
			name: "read",
			mutate: func(_ string, _ *legacyExactExclusion, ops *providerCreatedTargetParentWalkOps) {
				ops.read = func(backupDirectoryReader) ([]os.DirEntry, error) { return nil, canary }
			},
		},
		{
			name: "stage lstat",
			mutate: func(stage string, _ *legacyExactExclusion, ops *providerCreatedTargetParentWalkOps) {
				original := ops.lstat
				ops.lstat = func(path string) (os.FileInfo, error) {
					if path == stage {
						return nil, canary
					}
					return original(path)
				}
			},
		},
		{
			name: "stage proof",
			mutate: func(_ string, exact *legacyExactExclusion, _ *providerCreatedTargetParentWalkOps) {
				exact.validate = func(context.Context, string, os.FileInfo) error { return canary }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, candidateStage, candidate, candidateBudget, ops := newProviderCreatedParentWalkFixture(t)
			test.mutate(candidateStage, candidate, &ops)
			if err := walkProviderCreatedTargetParentWithOps(
				t.Context(), candidate.targetParent, candidate, candidateBudget, ops,
			); err == nil {
				t.Fatal("faulted created-parent walk succeeded")
			}
		})
	}

	t.Run("empty inventory", func(t *testing.T) {
		if err := os.Remove(stage); err != nil {
			t.Fatal(err)
		}
		exact.seen = 0
		if err := walkProviderCreatedTargetParentWithOps(
			t.Context(), root, exact, newBackupBudget(), validOps,
		); err == nil {
			t.Fatal("empty created-parent inventory succeeded")
		}
	})

	t.Run("close", func(t *testing.T) {
		_, _, candidate, candidateBudget, ops := newProviderCreatedParentWalkFixture(t)
		ops.close = func(file *os.File) error { return errors.Join(canary, file.Close()) }
		if err := walkProviderCreatedTargetParentWithOps(
			t.Context(), candidate.targetParent, candidate, candidateBudget, ops,
		); !errors.Is(err, canary) {
			t.Fatalf("created-parent close error = %v", err)
		}
	})
}

func TestProviderCreatedTargetParentWalkAdditionalBranchCoverage(t *testing.T) {
	t.Run("nil context", func(t *testing.T) {
		_, _, exact, budget, ops := newProviderCreatedParentWalkFixture(t)
		if err := walkProviderCreatedTargetParentWithOps(
			nil, exact.targetParent, exact, budget, ops,
		); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("root budget", func(t *testing.T) {
		_, _, exact, budget, ops := newProviderCreatedParentWalkFixture(t)
		budget.maxEntries = 0
		if err := walkProviderCreatedTargetParentWithOps(
			t.Context(), exact.targetParent, exact, budget, ops,
		); err == nil {
			t.Fatal("exhausted root budget succeeded")
		}
	})

	t.Run("canceled during entry", func(t *testing.T) {
		_, _, exact, budget, ops := newProviderCreatedParentWalkFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		original := ops.read
		ops.read = func(directory backupDirectoryReader) ([]os.DirEntry, error) {
			entries, err := original(directory)
			cancel()
			return entries, err
		}
		if err := walkProviderCreatedTargetParentWithOps(
			ctx, exact.targetParent, exact, budget, ops,
		); !errors.Is(err, context.Canceled) {
			t.Fatalf("entry cancellation = %v", err)
		}
	})

	t.Run("invalid entry", func(t *testing.T) {
		_, _, exact, budget, ops := newProviderCreatedParentWalkFixture(t)
		read := false
		ops.read = func(backupDirectoryReader) ([]os.DirEntry, error) {
			if read {
				return nil, io.EOF
			}
			read = true
			return []os.DirEntry{providerCreatedParentCoverageDirEntry("..")}, nil
		}
		if err := walkProviderCreatedTargetParentWithOps(
			t.Context(), exact.targetParent, exact, budget, ops,
		); err == nil {
			t.Fatal("invalid strict-parent entry succeeded")
		}
	})

	t.Run("unexpected entry", func(t *testing.T) {
		root, _, exact, budget, ops := newProviderCreatedParentWalkFixture(t)
		if err := os.WriteFile(filepath.Join(root, "extra"), []byte("extra"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := walkProviderCreatedTargetParentWithOps(
			t.Context(), root, exact, budget, ops,
		); err == nil {
			t.Fatal("unexpected strict-parent entry succeeded")
		}
	})

	t.Run("entry budget", func(t *testing.T) {
		_, _, exact, budget, ops := newProviderCreatedParentWalkFixture(t)
		budget.maxEntries = 1
		if err := walkProviderCreatedTargetParentWithOps(
			t.Context(), exact.targetParent, exact, budget, ops,
		); err == nil {
			t.Fatal("exhausted entry budget succeeded")
		}
	})

	t.Run("final root stat", func(t *testing.T) {
		root, stage, exact, budget, ops := newProviderCreatedParentWalkFixture(t)
		original := ops.lstat
		calls := 0
		canary := errors.New("strict-parent final stat canary")
		ops.lstat = func(path string) (os.FileInfo, error) {
			calls++
			if calls == 3 {
				return nil, canary
			}
			return original(path)
		}
		_ = stage
		if err := walkProviderCreatedTargetParentWithOps(
			t.Context(), root, exact, budget, ops,
		); !errors.Is(err, canary) {
			t.Fatalf("final root stat = %v", err)
		}
	})

	t.Run("final root metadata", func(t *testing.T) {
		root, _, exact, budget, ops := newProviderCreatedParentWalkFixture(t)
		exact.validate = func(context.Context, string, os.FileInfo) error {
			return os.Chtimes(root, time.Now(), time.Now().Add(time.Second))
		}
		if err := walkProviderCreatedTargetParentWithOps(
			t.Context(), root, exact, budget, ops,
		); err == nil {
			t.Fatal("changed strict-parent metadata succeeded")
		}
	})

	t.Run("final parent proof", func(t *testing.T) {
		root, _, exact, budget, ops := newProviderCreatedParentWalkFixture(t)
		original := exact.validateTargetParent
		calls := 0
		canary := errors.New("strict-parent final proof canary")
		exact.validateTargetParent = func(
			ctx context.Context, path string, info os.FileInfo,
		) (fileidentity.Identity, error) {
			calls++
			if calls == 2 {
				return fileidentity.Identity{}, canary
			}
			return original(ctx, path, info)
		}
		if err := walkProviderCreatedTargetParentWithOps(
			t.Context(), root, exact, budget, ops,
		); !errors.Is(err, canary) {
			t.Fatalf("final parent proof = %v", err)
		}
	})
}

func TestProviderCreatedTargetParentRoutingFaultCoverage(t *testing.T) {
	t.Run("missing strict walker", func(t *testing.T) {
		fixture := newMissingLiveTargetParentFixture(t)
		ops := defaultBackupLiveVerifyOps()
		ops.walkCreatedTargetParent = nil
		if err := fixture.session.verifyLiveSourcesForStageAndParentWithOps(
			t.Context(), fixture.spec, fixture.stage,
			liveStageCheck(t, fixture.stage), liveTargetParentCheck(t, fixture.root), ops,
		); err == nil {
			t.Fatal("missing provider-created target-parent walker succeeded")
		}
	})

	t.Run("recorded legacy input", func(t *testing.T) {
		fixture := newMissingLiveTargetParentFixture(t)
		fixture.session.manifest.Files = append(
			fixture.session.manifest.Files,
			BackupFileManifest{
				StoreID: fixture.spec.ID.String(), Role: "legacy",
				Source: filepath.Join(fixture.root, "recorded"), LegacyRoot: 0,
			},
		)
		if err := fixture.session.verifyLiveSourcesForStageAndParentWithOps(
			t.Context(), fixture.spec, fixture.stage,
			liveStageCheck(t, fixture.stage), liveTargetParentCheck(t, fixture.root),
			defaultBackupLiveVerifyOps(),
		); err == nil {
			t.Fatal("snapshot-missing parent with a recorded legacy input succeeded")
		}
	})

	t.Run("parent proof", func(t *testing.T) {
		fixture := newMissingLiveTargetParentFixture(t)
		canary := errors.New("provider-created target-parent proof canary")
		if err := fixture.session.verifyLiveSourcesForStageAndParentWithOps(
			t.Context(), fixture.spec, fixture.stage,
			liveStageCheck(t, fixture.stage),
			func(context.Context, os.FileInfo) (fileidentity.Identity, error) {
				return fileidentity.Identity{}, canary
			},
			defaultBackupLiveVerifyOps(),
		); !errors.Is(err, canary) {
			t.Fatalf("provider-created target-parent proof error = %v", err)
		}
	})

	t.Run("parent identity aliases stage", func(t *testing.T) {
		fixture := newMissingLiveTargetParentFixture(t)
		stageIdentity, _, exists, err := fileidentity.ExistingWithType(fixture.stage)
		if err != nil || !exists {
			t.Fatal(err)
		}
		if err := fixture.session.verifyLiveSourcesForStageAndParentWithOps(
			t.Context(), fixture.spec, fixture.stage,
			liveStageCheck(t, fixture.stage),
			func(context.Context, os.FileInfo) (fileidentity.Identity, error) {
				return stageIdentity, nil
			},
			defaultBackupLiveVerifyOps(),
		); err == nil {
			t.Fatal("target-parent identity aliasing the stage succeeded")
		}
	})

	t.Run("strict walker failure", func(t *testing.T) {
		fixture := newMissingLiveTargetParentFixture(t)
		canary := errors.New("provider-created target-parent walker canary")
		ops := defaultBackupLiveVerifyOps()
		ops.walkCreatedTargetParent = func(
			context.Context, string, *legacyExactExclusion, *backupBudget,
		) error {
			return canary
		}
		if err := fixture.session.verifyLiveSourcesForStageAndParentWithOps(
			t.Context(), fixture.spec, fixture.stage,
			liveStageCheck(t, fixture.stage), liveTargetParentCheck(t, fixture.root), ops,
		); !errors.Is(err, canary) {
			t.Fatalf("provider-created target-parent walker error = %v", err)
		}
	})

	t.Run("parent proof path", func(t *testing.T) {
		fixture := newMissingLiveTargetParentFixture(t)
		ops := defaultBackupLiveVerifyOps()
		ops.walkCreatedTargetParent = func(
			ctx context.Context,
			root string,
			exact *legacyExactExclusion,
			_ *backupBudget,
		) error {
			info, err := os.Lstat(root)
			if err != nil {
				return err
			}
			_, err = exact.validateTargetParent(ctx, root+"-wrong", info)
			return err
		}
		if err := fixture.session.verifyLiveSourcesForStageAndParentWithOps(
			t.Context(), fixture.spec, fixture.stage,
			liveStageCheck(t, fixture.stage), liveTargetParentCheck(t, fixture.root), ops,
		); err == nil {
			t.Fatal("wrong provider-created target-parent proof path succeeded")
		}
	})

	t.Run("duplicate eligible roots", func(t *testing.T) {
		fixture := newMissingLiveTargetParentFixture(t)
		fixture.spec.LegacyRoots = []string{fixture.root, fixture.root}
		for index := range fixture.session.manifest.Stores {
			store := &fixture.session.manifest.Stores[index]
			if store.StoreID == fixture.spec.ID.String() {
				store.LegacyRoots = append([]string(nil), fixture.spec.LegacyRoots...)
				store.LegacyRootKinds = []string{"missing", "missing"}
			}
		}
		if err := fixture.session.verifyLiveSourcesForStageAndParentWithOps(
			t.Context(), fixture.spec, fixture.stage,
			liveStageCheck(t, fixture.stage), liveTargetParentCheck(t, fixture.root),
			defaultBackupLiveVerifyOps(),
		); err == nil {
			t.Fatal("multiple eligible provider-created target-parent roots succeeded")
		}
	})
}
