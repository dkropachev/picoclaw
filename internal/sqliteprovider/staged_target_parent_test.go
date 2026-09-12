//go:build (unix && !aix) || windows

//nolint:govet // Independent failure-boundary assertions use narrow error scopes.
package sqliteprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

func stagedTargetParentTestStageIdentity(
	t *testing.T,
	path string,
) fileidentity.Identity {
	t.Helper()
	identity, objectType, exists, err := fileidentity.ExistingWithType(path)
	if err != nil || !exists || objectType != fileidentity.ObjectTypeRegular ||
		!identity.Valid() {
		t.Fatalf("target-parent test stage identity = %#v, %v", identity, err)
	}
	return identity
}

func stagedTargetParentTestLiveVerifier(
	parentPath string,
	target string,
	after func(),
) StagedLiveVerification {
	return func(ctx context.Context, replacement ValidatedReplacement) error {
		err := replacement.UseWithTargetParent(
			ctx,
			"global/auth",
			target,
			func(
				useCtx context.Context,
				stage string,
				checkStage ValidatedReplacementCheck,
				checkParent ValidatedTargetParentCheck,
			) error {
				stageInfo, err := os.Lstat(stage)
				if err != nil {
					return err
				}
				if _, err := checkStage(useCtx, stageInfo); err != nil {
					return err
				}
				parentInfo, err := os.Lstat(parentPath)
				if err != nil {
					return err
				}
				_, err = checkParent(useCtx, parentInfo)
				return err
			},
		)
		if err == nil && after != nil {
			after()
		}
		return err
	}
}

func TestRetainedStagedTargetParentLifecycleAndInventory(t *testing.T) {
	root := t.TempDir()
	parentPath := filepath.Join(root, "nested", "repository_reviews")
	parent, err := prepareRetainedStagedTargetParent(t.Context(), parentPath)
	if err != nil || parent == nil {
		t.Fatalf("prepare retained target parent = %#v, %v", parent, err)
	}
	defer parent.Close()
	stage := filepath.Join(parentPath, ".repository-reviews.db.migration-stage-test")
	if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	stageInfo, err := os.Lstat(stage)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := parent.SealSoleStage(
		t.Context(), parentPath, stage, stageInfo,
		stagedTargetParentTestStageIdentity(t, stage),
	)
	if err != nil || !identity.Valid() {
		t.Fatalf("seal retained target parent = %#v, %v", identity, err)
	}
	observed, err := os.Lstat(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := parent.Check(t.Context(), parentPath, observed)
	if err != nil || checked != identity {
		t.Fatalf("check retained target parent = %#v, %v", checked, err)
	}
	checked, err = parent.CheckSoleStage(t.Context(), parentPath, stage, stageInfo)
	if err != nil || checked != identity {
		t.Fatalf("check sole retained stage = %#v, %v", checked, err)
	}
	if _, err := parent.SealSoleStage(
		t.Context(), parentPath, stage, stageInfo,
		stagedTargetParentTestStageIdentity(t, stage),
	); err == nil {
		t.Fatal("retained target parent accepted a second seal")
	}
	if err := os.Remove(stage); err != nil {
		t.Fatal(err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := parent.Check(t.Context(), parentPath, nil); err == nil {
		t.Fatal("closed retained target parent remained usable")
	}
}

func TestRetainedStagedTargetParentRollsBackOnlyEmptyCreatedLineage(t *testing.T) {
	t.Run("nested empty lineage", func(t *testing.T) {
		root := t.TempDir()
		createdRoot := filepath.Join(root, "workspace")
		parentPath := filepath.Join(createdRoot, "nested", "repository_reviews")
		parent, err := prepareRetainedStagedTargetParent(t.Context(), parentPath)
		if err != nil || parent == nil {
			t.Fatalf("prepare rollback parent = %#v, %v", parent, err)
		}
		if err := parent.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(createdRoot); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("empty created lineage remained after rollback: %v", err)
		}
		if _, err := os.Lstat(root); err != nil {
			t.Fatalf("pre-existing ancestor was removed: %v", err)
		}
	})

	t.Run("nonempty lineage is preserved", func(t *testing.T) {
		root := t.TempDir()
		parentPath := filepath.Join(root, "workspace", "repository_reviews")
		parent, err := prepareRetainedStagedTargetParent(t.Context(), parentPath)
		if err != nil || parent == nil {
			t.Fatalf("prepare nonempty rollback parent = %#v, %v", parent, err)
		}
		alien := filepath.Join(parentPath, "alien")
		if err := os.WriteFile(alien, []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := parent.Close(); err == nil {
			t.Fatal("nonempty created parent was reported as rolled back")
		}
		contents, err := os.ReadFile(alien)
		if err != nil || string(contents) != "preserve" {
			t.Fatalf("rollback modified alien entry = %q, %v", contents, err)
		}
	})
}

func TestRetainedStagedTargetParentBindsSealedTupleAndInstalledInventory(t *testing.T) {
	t.Run("sealed tuple", func(t *testing.T) {
		parent, parentPath, stage, _, _ := newSealedTargetParentTestFixture(t)
		moved := filepath.Join(parentPath, ".repository-reviews.db.migration-stage-moved")
		if err := os.Rename(stage, moved); err != nil {
			t.Fatal(err)
		}
		movedInfo, err := os.Lstat(moved)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parent.CheckSoleStage(t.Context(), parentPath, moved, movedInfo); err == nil {
			t.Fatal("sealed target-parent stage was rebound to another path")
		}
	})

	t.Run("installed inventory", func(t *testing.T) {
		parent, parentPath, stage, stageInfo, stageFile := newSealedTargetParentTestFixture(t)
		target := filepath.Join(parentPath, "repository-reviews.db")
		complete, err := parent.ReplaceStage(t.Context(), stage, target, stageInfo, stageFile)
		if err != nil || !complete {
			t.Fatalf("install retained target-parent stage = complete:%t error:%v", complete, err)
		}
		if _, err := parent.CheckSoleInstalledTarget(t.Context(), target); err != nil {
			t.Fatalf("installed target-parent inventory = %v", err)
		}
		targetInfo, err := os.Lstat(target)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parent.SealSoleStage(
			t.Context(), parentPath, target, targetInfo,
			stagedTargetParentTestStageIdentity(t, target),
		); err == nil {
			t.Fatal("installed target-parent capability was resealed")
		}
		if err := os.WriteFile(
			filepath.Join(parentPath, "late-extra"), []byte("extra"), 0o600,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := parent.CheckSoleInstalledTarget(t.Context(), target); err == nil {
			t.Fatal("installed target-parent inventory accepted a late extra entry")
		}
	})
}

func TestProviderCreatedTargetParentRollsBackAfterConclusivePreCutoverFailure(t *testing.T) {
	home := t.TempDir()
	createdRoot := filepath.Join(home, "workspace")
	parentPath := filepath.Join(createdRoot, "repository_reviews")
	target := filepath.Join(parentPath, "repository-reviews.db")
	sourcePath := filepath.Join(home, "backup", "missing.db")
	recorder := &offlineProviderHookRecorder{}
	lease := newOfflineProviderTestLease(t, target, recorder)
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})
	canary := errors.New("provider-created target-parent migration canary")
	result, err := MigrateStagedOfflineFromWithLiveVerification(
		t.Context(), lease, source, 5*time.Second, 1,
		installProviderOfflineFixture,
		func(context.Context, string) error { return nil },
		func(ctx context.Context, replacement ValidatedReplacement) error {
			return replacement.Use(
				ctx,
				"global/auth",
				target,
				func(context.Context, string, ValidatedReplacementCheck) error {
					return canary
				},
			)
		},
	)
	if err == nil || result.installed || !errors.Is(err, canary) {
		t.Fatalf("failed created-parent migration = result:%#v error:%v", result, err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed migration installed live target: %v", err)
	}
	if _, err := os.Lstat(createdRoot); !errors.Is(err, os.ErrNotExist) {
		entries, readErr := os.ReadDir(createdRoot)
		parentEntries, parentReadErr := os.ReadDir(parentPath)
		t.Fatalf(
			"failed migration retained created lineage: stat:%v entries:%v read:%v parent:%v parent-read:%v",
			err, entries, readErr, parentEntries, parentReadErr,
		)
	}
}

func TestProviderCreatedTargetParentPreservesUnvalidatedDiagnosticStage(t *testing.T) {
	home := t.TempDir()
	parentPath := filepath.Join(home, "workspace", "repository_reviews")
	target := filepath.Join(parentPath, "repository-reviews.db")
	sourcePath := filepath.Join(home, "backup", "missing.db")
	recorder := &offlineProviderHookRecorder{}
	lease := newOfflineProviderTestLease(t, target, recorder)
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})
	canary := errors.New("provider-created target-parent diagnostic canary")
	result, err := MigrateStagedOfflineFromWithLiveVerification(
		t.Context(), lease, source, 5*time.Second, 1,
		func(context.Context, string) error { return canary },
		func(context.Context, string) error { return nil },
		func(context.Context, ValidatedReplacement) error { return nil },
	)
	if err == nil || result.installed || !errors.Is(err, canary) ||
		!strings.Contains(err.Error(), "diagnostic migration stage was retained") {
		t.Fatalf("unvalidated created-parent migration = result:%#v error:%v", result, err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unvalidated migration installed live target: %v", err)
	}
	entries, readErr := os.ReadDir(parentPath)
	if readErr != nil || len(entries) != 1 ||
		!strings.Contains(entries[0].Name(), ".migration-stage-") {
		t.Fatalf("diagnostic target-parent inventory = %v, %v", entries, readErr)
	}
}

func TestProviderCreatedTargetParentChecksInventoryWhenActivationFails(t *testing.T) {
	root := t.TempDir()
	parentPath := filepath.Join(root, "workspace", "repository_reviews")
	target := filepath.Join(parentPath, "repository-reviews.db")
	source := stagedCoverageSource(filepath.Join(root, "missing-source.db"))
	canary := errors.New("provider-created target-parent activation canary")
	result, err := migrateStagedOfflineAuthorizedWithLiveVerification(
		t.Context(), source, target, 5*time.Second, 1,
		installProviderOfflineFixture, acceptStagedValidation,
		func(ctx context.Context, replacement ValidatedReplacement) error {
			return replacement.UseWithTargetParent(
				ctx,
				"global/auth",
				target,
				func(
					useCtx context.Context,
					stage string,
					checkStage ValidatedReplacementCheck,
					checkParent ValidatedTargetParentCheck,
				) error {
					stageInfo, statErr := os.Lstat(stage)
					if statErr != nil {
						return statErr
					}
					if _, checkErr := checkStage(useCtx, stageInfo); checkErr != nil {
						return checkErr
					}
					parentInfo, statErr := os.Lstat(parentPath)
					if statErr != nil {
						return statErr
					}
					_, checkErr := checkParent(useCtx, parentInfo)
					return checkErr
				},
			)
		},
		&stagedCoverageAuthority{},
		stagedMigrationOps{
			replace: replaceStagedGeneration,
			activate: func(context.Context, string, time.Duration, int) error {
				if err := os.WriteFile(
					filepath.Join(parentPath, "activation-extra"), []byte("extra"), 0o600,
				); err != nil {
					return err
				}
				return canary
			},
		},
	)
	if !result.installed || !errors.Is(err, canary) ||
		dblayer.CodeOf(err) != dblayer.CodeOutcomeUnknown ||
		!strings.Contains(err.Error(), "target parent changed during activation") {
		t.Fatalf("activation inventory result=%#v error=%v", result, err)
	}
}

func TestProviderCreatedTargetParentFinalPhaseInventoryFailures(t *testing.T) {
	t.Run("immediately before replacement", func(t *testing.T) {
		root := t.TempDir()
		parentPath := filepath.Join(root, "workspace", "repository_reviews")
		target := filepath.Join(parentPath, "repository-reviews.db")
		liveDone := false
		mutated := false
		var mutationErr error
		authority := &stagedCoverageAuthority{
			onCheck: func(int) {
				if liveDone && !mutated {
					mutated = true
					mutationErr = os.WriteFile(
						filepath.Join(parentPath, "pre-cutover-extra"), []byte("extra"), 0o600,
					)
				}
			},
		}
		result, err := migrateStagedOfflineAuthorizedWithLiveVerification(
			t.Context(), stagedCoverageSource(filepath.Join(root, "missing-source.db")),
			target, 5*time.Second, 1,
			installProviderOfflineFixture, acceptStagedValidation,
			stagedTargetParentTestLiveVerifier(parentPath, target, func() { liveDone = true }),
			authority,
			stagedMigrationOps{
				replace:  replaceStagedGeneration,
				activate: activateInstalledGeneration,
			},
		)
		if mutationErr != nil {
			t.Fatal(mutationErr)
		}
		if result.installed || err == nil || !mutated {
			t.Fatalf("pre-cutover inventory result=%#v mutated=%t error=%v", result, mutated, err)
		}
	})

	t.Run("after activation", func(t *testing.T) {
		root := t.TempDir()
		parentPath := filepath.Join(root, "workspace", "repository_reviews")
		target := filepath.Join(parentPath, "repository-reviews.db")
		activationDone := false
		mutated := false
		var mutationErr error
		authority := &stagedCoverageAuthority{
			onCheck: func(int) {
				if activationDone && !mutated {
					mutated = true
					mutationErr = os.WriteFile(
						filepath.Join(parentPath, "post-activation-extra"), []byte("extra"), 0o600,
					)
				}
			},
		}
		result, err := migrateStagedOfflineAuthorizedWithLiveVerification(
			t.Context(), stagedCoverageSource(filepath.Join(root, "missing-source.db")),
			target, 5*time.Second, 1,
			installProviderOfflineFixture, acceptStagedValidation,
			stagedTargetParentTestLiveVerifier(parentPath, target, nil),
			authority,
			stagedMigrationOps{
				replace: replaceStagedGeneration,
				activate: func(
					ctx context.Context, path string, timeout time.Duration, version int,
				) error {
					err := activateInstalledGeneration(ctx, path, timeout, version)
					activationDone = true
					return err
				},
			},
		)
		if mutationErr != nil {
			t.Fatal(mutationErr)
		}
		if !result.installed || err == nil || !mutated ||
			dblayer.CodeOf(err) != dblayer.CodeOutcomeUnknown ||
			!strings.Contains(err.Error(), "could not be revalidated") {
			t.Fatalf("post-activation inventory result=%#v mutated=%t error=%v", result, mutated, err)
		}
	})
}

func TestRetainedStagedTargetParentRejectsExistingAndUnexpectedEntries(t *testing.T) {
	t.Run("existing parent mints no proof", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "existing")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		parent, err := prepareRetainedStagedTargetParent(t.Context(), path)
		if err != nil || parent != nil {
			t.Fatalf("existing target parent = %#v, %v", parent, err)
		}
		if platform, err := createRetainedStagedTargetParentPlatform(t.Context(), path); platform != nil || err == nil {
			if platform != nil {
				_ = closeRetainedStagedTargetParentPlatform(platform, false)
			}
			t.Fatalf("existing target parent gained creation proof = %#v, %v", platform, err)
		}
	})

	for _, name := range []string{
		"ordinary.json",
		"legacy-json",
		"backups",
		"state",
		".repository-reviews.db.migration-stage-near",
		"repository-reviews.db-wal",
	} {
		t.Run(name, func(t *testing.T) {
			parentPath := filepath.Join(t.TempDir(), "repository_reviews")
			parent, err := prepareRetainedStagedTargetParent(t.Context(), parentPath)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			stage := filepath.Join(parentPath, ".repository-reviews.db.migration-stage-test")
			if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(name, ".json") || strings.HasSuffix(name, "-wal") ||
				strings.Contains(name, "stage-near") {
				if err := os.WriteFile(filepath.Join(parentPath, name), []byte("extra"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(filepath.Join(parentPath, name), 0o700); err != nil {
				t.Fatal(err)
			}
			stageInfo, err := os.Lstat(stage)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parent.SealSoleStage(
				t.Context(), parentPath, stage, stageInfo,
				stagedTargetParentTestStageIdentity(t, stage),
			); err == nil {
				t.Fatal("retained target parent accepted an unexpected entry")
			}
		})
	}
}

func TestRetainedStagedTargetParentRejectsWrongFullStageIdentity(t *testing.T) {
	parentPath := filepath.Join(t.TempDir(), "repository_reviews")
	parent, err := prepareRetainedStagedTargetParent(t.Context(), parentPath)
	if err != nil || parent == nil {
		t.Fatalf("prepare retained parent = %#v, %v", parent, err)
	}
	defer parent.Close()
	stage := filepath.Join(parentPath, ".repository-reviews.db.migration-stage-test")
	if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	stageInfo, err := os.Lstat(stage)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other.db")
	if err := os.WriteFile(other, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parent.SealSoleStage(
		t.Context(), parentPath, stage, stageInfo,
		stagedTargetParentTestStageIdentity(t, other),
	); err == nil {
		t.Fatal("target-parent seal accepted another file's full identity")
	}
}

func TestRetainedStagedTargetParentRejectsRenameAndTargetMismatch(t *testing.T) {
	parentPath := filepath.Join(t.TempDir(), "repository_reviews")
	parent, err := prepareRetainedStagedTargetParent(t.Context(), parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	stage := filepath.Join(parentPath, ".repository-reviews.db.migration-stage-test")
	if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	stageInfo, err := os.Lstat(stage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parent.SealSoleStage(
		t.Context(), parentPath, stage, stageInfo,
		stagedTargetParentTestStageIdentity(t, stage),
	); err != nil {
		t.Fatal(err)
	}
	stageFile, err := os.OpenFile(stage, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stageFile.Close()
	if complete, err := parent.ReplaceStage(
		t.Context(),
		stage,
		filepath.Join(filepath.Dir(parentPath), "outside.db"),
		stageInfo,
		stageFile,
	); complete || err == nil {
		t.Fatalf("outside-parent replacement = complete:%t error:%v", complete, err)
	}
	if runtime.GOOS == "windows" {
		return
	}

	moved := parentPath + ".moved"
	if err := os.Rename(parentPath, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := parent.Check(t.Context(), parentPath, nil); err == nil {
		t.Fatal("renamed and replaced target parent revalidated")
	}
}

func TestMigrateStagedOfflineFromUsesProviderCreatedTargetParent(t *testing.T) {
	home := t.TempDir()
	parentPath := filepath.Join(home, "workspace", "repository_reviews")
	target := filepath.Join(parentPath, "repository-reviews.db")
	sourcePath := filepath.Join(home, "backup", "missing.db")
	recorder := &offlineProviderHookRecorder{}
	lease := newOfflineProviderTestLease(t, target, recorder)
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})
	parentChecks := 0
	result, err := MigrateStagedOfflineFromWithLiveVerification(
		t.Context(), lease, source, 5*time.Second, 1,
		installProviderOfflineFixture,
		func(context.Context, string) error { return nil },
		func(ctx context.Context, replacement ValidatedReplacement) error {
			return replacement.UseWithTargetParent(
				ctx,
				"global/auth",
				target,
				func(
					useCtx context.Context,
					stage string,
					checkStage ValidatedReplacementCheck,
					checkParent ValidatedTargetParentCheck,
				) error {
					parentInfo, err := os.Lstat(parentPath)
					if err != nil {
						return err
					}
					parentIdentity, err := checkParent(useCtx, parentInfo)
					if err != nil || !parentIdentity.Valid() {
						return errors.Join(errors.New("created parent check failed"), err)
					}
					parentChecks++
					stageInfo, err := os.Lstat(stage)
					if err != nil {
						return err
					}
					stageIdentity, err := checkStage(useCtx, stageInfo)
					if err != nil || !stageIdentity.Valid() || stageIdentity == parentIdentity {
						return errors.Join(errors.New("created parent stage check failed"), err)
					}
					return nil
				},
			)
		},
	)
	if err != nil || !result.installed || parentChecks != 1 {
		t.Fatalf("created-parent migration = result:%#v checks:%d error:%v", result, parentChecks, err)
	}
	entries, err := os.ReadDir(parentPath)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(target) {
		t.Fatalf("installed target-parent inventory = %v, %v", entries, err)
	}
	if !providerOfflineTableExists(t, target, "installed") {
		t.Fatal("provider-created target parent did not receive installed generation")
	}
}

func TestMigrateStagedOfflineFromDoesNotClaimPreexistingTargetParent(t *testing.T) {
	home := t.TempDir()
	parentPath := filepath.Join(home, "repository_reviews")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parentPath, "repository-reviews.db")
	sourcePath := filepath.Join(home, "missing-backup.db")
	recorder := &offlineProviderHookRecorder{}
	lease := newOfflineProviderTestLease(t, target, recorder)
	source := immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, sourcePath)
	})
	result, err := MigrateStagedOfflineFromWithLiveVerification(
		t.Context(), lease, source, 5*time.Second, 1,
		installProviderOfflineFixture,
		func(context.Context, string) error { return nil },
		func(ctx context.Context, replacement ValidatedReplacement) error {
			return replacement.UseWithTargetParent(
				ctx,
				"global/auth",
				target,
				func(
					useCtx context.Context,
					_ string,
					_ ValidatedReplacementCheck,
					checkParent ValidatedTargetParentCheck,
				) error {
					info, statErr := os.Lstat(parentPath)
					if statErr != nil {
						return statErr
					}
					_, checkErr := checkParent(useCtx, info)
					return checkErr
				},
			)
		},
	)
	if err == nil || result.installed || !errors.Is(err, errValidatedReplacementContract) {
		t.Fatalf("preexisting-parent migration = result:%#v error:%v", result, err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preexisting-parent failure installed target: %v", err)
	}
}

func TestRetainedStagedTargetParentIdentityIsDistinctFromStage(t *testing.T) {
	parentPath := filepath.Join(t.TempDir(), "repository_reviews")
	parent, err := prepareRetainedStagedTargetParent(t.Context(), parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	stage := filepath.Join(parentPath, ".repository-reviews.db.migration-stage-test")
	if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	stageInfo, err := os.Lstat(stage)
	if err != nil {
		t.Fatal(err)
	}
	parentIdentity, err := parent.SealSoleStage(
		t.Context(), parentPath, stage, stageInfo,
		stagedTargetParentTestStageIdentity(t, stage),
	)
	if err != nil {
		t.Fatal(err)
	}
	stageIdentity, objectType, exists, err := fileidentity.ExistingWithType(stage)
	if err != nil || !exists || objectType != fileidentity.ObjectTypeRegular ||
		stageIdentity == parentIdentity {
		t.Fatalf("parent/stage identities = parent:%#v stage:%#v error:%v", parentIdentity, stageIdentity, err)
	}
}

func TestMigrateStagedOfflineFromRejectsCreatedParentDriftAndExtraEntries(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(string) error
	}{
		{
			name: "extra file",
			mutate: func(parent string) error {
				return os.WriteFile(filepath.Join(parent, "extra.json"), []byte("extra"), 0o600)
			},
		},
		{
			name: "permission drift",
			mutate: func(parent string) error {
				return os.Chmod(parent, 0o755)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			parentPath := filepath.Join(home, "workspace", "repository_reviews")
			target := filepath.Join(parentPath, "repository-reviews.db")
			sourcePath := filepath.Join(home, "backup", "missing.db")
			recorder := &offlineProviderHookRecorder{}
			lease := newOfflineProviderTestLease(t, target, recorder)
			source := immutableGenerationSourceForTest(func(
				ctx context.Context,
				use func(context.Context, string) error,
			) error {
				return use(ctx, sourcePath)
			})
			callbackCalls := 0
			result, err := MigrateStagedOfflineFromWithLiveVerification(
				t.Context(), lease, source, 5*time.Second, 1,
				installProviderOfflineFixture,
				func(_ context.Context, stage string) error {
					return test.mutate(filepath.Dir(stage))
				},
				func(ctx context.Context, replacement ValidatedReplacement) error {
					callbackCalls++
					return replacement.Use(ctx, "global/auth", target, func(
						context.Context,
						string,
						ValidatedReplacementCheck,
					) error {
						return nil
					})
				},
			)
			if err == nil || result.installed || callbackCalls != 0 {
				t.Fatalf("created-parent %s = result:%#v callbacks:%d error:%v", test.name, result, callbackCalls, err)
			}
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("created-parent %s installed target: %v", test.name, err)
			}
			entries, readErr := os.ReadDir(parentPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, entry := range entries {
				if test.name == "extra file" && strings.Contains(entry.Name(), ".migration-stage-") {
					t.Fatalf("created-parent %s retained stage %s", test.name, entry.Name())
				}
			}
		})
	}
}

func TestValidatedTargetParentCheckIsCallbackScoped(t *testing.T) {
	root := t.TempDir()
	parentPath := filepath.Join(root, "repository_reviews")
	parent, err := prepareRetainedStagedTargetParent(t.Context(), parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	target := filepath.Join(parentPath, "repository-reviews.db")
	stage := filepath.Join(
		parentPath,
		".repository-reviews.db.migration-stage-0123456789abcdef0123456789abcdef.db",
	)
	if err := os.WriteFile(stage, []byte("validated"), 0o600); err != nil {
		t.Fatal(err)
	}
	stageInfo, err := os.Lstat(stage)
	if err != nil {
		t.Fatal(err)
	}
	seal, err := sealValidatedReplacementStage(t.Context(), stage)
	if err != nil {
		t.Fatal(err)
	}
	defer seal.close()
	if _, err := parent.SealSoleStage(
		t.Context(), parentPath, stage, stageInfo,
		stagedTargetParentTestStageIdentity(t, stage),
	); err != nil {
		t.Fatal(err)
	}
	parentInfo, err := os.Lstat(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	var retained ValidatedTargetParentCheck
	checks := 0
	err = invokeStagedLiveVerificationWithTargetParent(
		t.Context(),
		"global/auth",
		target,
		seal,
		parent,
		func(ctx context.Context, replacement ValidatedReplacement) error {
			return replacement.UseWithTargetParent(
				ctx,
				"global/auth",
				target,
				func(
					useCtx context.Context,
					_ string,
					_ ValidatedReplacementCheck,
					checkParent ValidatedTargetParentCheck,
				) error {
					retained = checkParent
					identity, checkErr := checkParent(useCtx, parentInfo)
					if checkErr != nil || !identity.Valid() {
						return errors.Join(errors.New("target-parent callback check failed"), checkErr)
					}
					checks++
					return nil
				},
			)
		},
	)
	if err != nil || checks != 1 {
		t.Fatalf("target-parent callback checks = %d, %v", checks, err)
	}
	if retained == nil {
		t.Fatal("target-parent checker was not captured")
	}
	if _, err := retained(t.Context(), parentInfo); !errors.Is(err, errValidatedReplacementContract) {
		t.Fatalf("retained target-parent checker = %v", err)
	}
}
