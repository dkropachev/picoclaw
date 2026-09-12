//go:build (unix && !aix) || windows

//nolint:govet // Independent defensive-branch assertions use narrow error scopes.
package sqliteprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

func newSealedTargetParentTestFixture(
	t *testing.T,
) (*retainedStagedTargetParent, string, string, os.FileInfo, *os.File) {
	t.Helper()
	parentPath := filepath.Join(t.TempDir(), "nested", "repository_reviews")
	parent, err := prepareRetainedStagedTargetParent(t.Context(), parentPath)
	if err != nil || parent == nil {
		t.Fatalf("prepare retained target parent = %#v, %v", parent, err)
	}
	t.Cleanup(func() { _ = parent.Close() })
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
	t.Cleanup(func() { _ = stageFile.Close() })
	return parent, parentPath, stage, stageInfo, stageFile
}

func TestStagedTargetParentCommonDefensiveCoverage(t *testing.T) {
	if parent, err := prepareRetainedStagedTargetParent(nil, "/invalid"); parent != nil || err == nil {
		t.Fatalf("nil-context prepare = %#v, %v", parent, err)
	}
	if parent, err := prepareRetainedStagedTargetParent(t.Context(), "relative"); parent != nil || err == nil {
		t.Fatalf("relative prepare = %#v, %v", parent, err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if parent, err := prepareRetainedStagedTargetParent(
		canceled,
		filepath.Join(t.TempDir(), "missing"),
	); parent != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled prepare = %#v, %v", parent, err)
	}
	excessive := t.TempDir()
	for index := 0; index <= maximumStagedTargetParentCreatedComponents; index++ {
		excessive = filepath.Join(excessive, "x")
	}
	if parent, err := prepareRetainedStagedTargetParent(
		t.Context(), excessive,
	); parent != nil || err == nil {
		t.Fatalf("excessive target-parent lineage = %#v, %v", parent, err)
	}

	root := t.TempDir()
	regular := filepath.Join(root, "regular")
	if err := os.WriteFile(regular, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if parent, err := prepareRetainedStagedTargetParent(t.Context(), regular); parent != nil || err == nil {
		t.Fatalf("regular target parent = %#v, %v", parent, err)
	}
	if parent, err := prepareRetainedStagedTargetParent(
		t.Context(), filepath.Join(regular, "child"),
	); parent != nil || err == nil {
		t.Fatalf("uninspectable target parent = %#v, %v", parent, err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err == nil {
		if parent, err := prepareRetainedStagedTargetParent(t.Context(), link); parent != nil || err == nil {
			t.Fatalf("symlink target parent = %#v, %v", parent, err)
		}
	}
	unsafeRoot := t.TempDir()
	actual := filepath.Join(unsafeRoot, "actual")
	if err := os.MkdirAll(filepath.Join(actual, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	unsafeLink := filepath.Join(unsafeRoot, "ancestor-link")
	if err := os.Symlink(actual, unsafeLink); err == nil {
		if parent, err := prepareRetainedStagedTargetParent(
			t.Context(), filepath.Join(unsafeLink, "child"),
		); parent != nil || err == nil {
			t.Fatalf("unsafe existing target-parent ancestor = %#v, %v", parent, err)
		}
	}

	var nilParent *retainedStagedTargetParent
	if _, err := nilParent.Check(t.Context(), root, nil); err == nil {
		t.Fatal("nil target-parent check succeeded")
	}
	if _, err := nilParent.CheckSoleStage(t.Context(), root, regular, nil); err == nil {
		t.Fatal("nil target-parent inventory check succeeded")
	}
	if _, err := nilParent.CheckSoleInstalledTarget(t.Context(), regular); err == nil {
		t.Fatal("nil installed target-parent check succeeded")
	}
	if complete, err := nilParent.ReplaceStage(t.Context(), regular, regular, nil, nil); complete || err == nil {
		t.Fatalf("nil target-parent replacement = complete:%t error:%v", complete, err)
	}
	if err := nilParent.Close(); err != nil {
		t.Fatalf("nil target-parent close = %v", err)
	}
	empty := &retainedStagedTargetParent{}
	if err := empty.Close(); err != nil || !empty.closed {
		t.Fatalf("empty target-parent close = closed:%t error:%v", empty.closed, err)
	}
	if err := empty.Close(); err != nil {
		t.Fatalf("repeated empty target-parent close = %v", err)
	}
}

func TestStagedTargetParentCommonAdditionalStateCoverage(t *testing.T) {
	parentPath := filepath.Join(t.TempDir(), "repository_reviews")
	parent, err := prepareRetainedStagedTargetParent(t.Context(), parentPath)
	if err != nil || parent == nil {
		t.Fatalf("prepare target parent = %#v, %v", parent, err)
	}
	defer parent.Close()
	if _, err := parent.CheckSoleInstalledTarget(
		t.Context(), filepath.Join(parentPath, "repository-reviews.db"),
	); err == nil {
		t.Fatal("uninstalled target parent passed installed check")
	}
	stage := filepath.Join(parentPath, ".repository-reviews.db.migration-stage-test")
	if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	stageInfo, err := os.Lstat(stage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parent.SealSoleStage(
		t.Context(), parentPath, stage, stageInfo, fileidentity.Identity{},
	); err == nil {
		t.Fatal("target parent accepted an invalid stage identity")
	}
	if _, err := parent.SealSoleStage(
		t.Context(), parentPath, stage, stageInfo,
		stagedTargetParentTestStageIdentity(t, stage),
	); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		moved := parentPath + ".moved"
		if err := os.Rename(parentPath, moved); err != nil {
			t.Fatal(err)
		}
		if _, err := parent.CheckSoleStage(t.Context(), parentPath, stage, stageInfo); err == nil {
			t.Fatal("missing named target parent passed sealed inventory")
		}
	}
}

func TestStagedTargetParentCommonInstalledAndPlatformFailureCoverage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix metadata-fault coverage")
	}
	t.Run("platform inventory failure", func(t *testing.T) {
		parent, parentPath, stage, stageInfo, stageFile := newSealedTargetParentTestFixture(t)
		if err := os.Chmod(stage, 0o400); err != nil {
			t.Fatal(err)
		}
		if complete, err := parent.ReplaceStage(
			t.Context(), stage, filepath.Join(parentPath, "repository-reviews.db"),
			stageInfo, stageFile,
		); complete || err == nil {
			t.Fatalf("unsafe stage replacement = complete:%t error:%v", complete, err)
		}
	})

	t.Run("canceled installed check", func(t *testing.T) {
		parent, parentPath, stage, stageInfo, stageFile := newSealedTargetParentTestFixture(t)
		target := filepath.Join(parentPath, "repository-reviews.db")
		complete, err := parent.ReplaceStage(t.Context(), stage, target, stageInfo, stageFile)
		if err != nil || !complete {
			t.Fatalf("replacement = complete:%t error:%v", complete, err)
		}
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := parent.CheckSoleInstalledTarget(canceled, target); !errors.Is(
			err, context.Canceled,
		) {
			t.Fatalf("canceled installed target check = %v", err)
		}
	})
}

func TestStagedTargetParentCommonCheckFaultCoverage(t *testing.T) {
	parent, parentPath, stage, stageInfo, stageFile := newSealedTargetParentTestFixture(t)
	if _, err := parent.Check(t.Context(), filepath.Dir(parentPath), nil); err == nil {
		t.Fatal("mismatched target-parent path succeeded")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := parent.Check(canceled, parentPath, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled target-parent check = %v", err)
	}
	otherInfo, err := os.Lstat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parent.Check(t.Context(), parentPath, otherInfo); err == nil {
		t.Fatal("wrong target-parent observation succeeded")
	}
	if _, err := parent.CheckSoleStage(
		t.Context(),
		parentPath,
		filepath.Join(parentPath, "other"),
		stageInfo,
	); err == nil {
		t.Fatal("wrong retained stage path succeeded")
	}
	if _, err := parent.CheckSoleStage(t.Context(), parentPath, stage, stageInfo); err != nil {
		t.Fatal(err)
	}
	otherParentInfo, err := os.Lstat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lstatCalls := 0
	if _, err := parent.checkSoleStageWithLstat(
		t.Context(), parentPath, stage, stageInfo, fileidentity.Identity{}, false,
		func(path string) (os.FileInfo, error) {
			lstatCalls++
			if lstatCalls == 2 {
				return otherParentInfo, nil
			}
			return os.Lstat(path)
		},
	); err == nil {
		t.Fatal("target-parent metadata change during inventory succeeded")
	}
	if err := os.Chmod(parentPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := parent.CheckSoleStage(t.Context(), parentPath, stage, stageInfo); err == nil {
		t.Fatal("post-seal parent metadata drift succeeded")
	}
	if complete, err := parent.ReplaceStage(
		canceled,
		stage,
		filepath.Join(parentPath, "repository-reviews.db"),
		stageInfo,
		stageFile,
	); complete || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled target-parent replacement = complete:%t error:%v", complete, err)
	}
}

func TestStagedTargetParentConclusiveTargetCollision(t *testing.T) {
	parent, parentPath, stage, stageInfo, stageFile := newSealedTargetParentTestFixture(t)
	target := filepath.Join(parentPath, "repository-reviews.db")
	if err := os.WriteFile(target, []byte("collision"), 0o600); err != nil {
		t.Fatal(err)
	}
	complete, err := parent.ReplaceStage(t.Context(), stage, target, stageInfo, stageFile)
	if complete || err == nil {
		t.Fatalf("target collision = complete:%t error:%v", complete, err)
	}
	current, statErr := os.Lstat(stage)
	if statErr != nil || !os.SameFile(stageInfo, current) {
		t.Fatalf("target collision moved stage: %v", statErr)
	}
}

func TestValidatedReplacementUseRejectsNilCallback(t *testing.T) {
	if err := (ValidatedReplacement{}).Use(t.Context(), "global/auth", "/tmp/auth.db", nil); !errors.Is(
		err,
		errValidatedReplacementContract,
	) {
		t.Fatalf("nil validated-replacement callback = %v", err)
	}
	if err := (ValidatedReplacement{}).UseWithTargetParent(
		t.Context(), "global/auth", "/tmp/auth.db", nil,
	); !errors.Is(err, errValidatedReplacementContract) {
		t.Fatalf("nil target-parent callback = %v", err)
	}
}

func TestStagedTargetParentCommonFailureTransitions(t *testing.T) {
	parent, parentPath, stage, stageInfo, stageFile := newSealedTargetParentTestFixture(t)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := parent.Check(canceled, parentPath, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled parent check = %v", err)
	}
	if _, err := parent.CheckSoleStage(canceled, parentPath, stage, stageInfo); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled parent inventory = %v", err)
	}
	if err := os.Remove(stage); err != nil {
		t.Fatal(err)
	}
	if _, err := parent.CheckSoleStage(t.Context(), parentPath, stage, stageInfo); err == nil {
		t.Fatal("missing sealed stage revalidated")
	}
	if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if complete, err := parent.ReplaceStage(
		t.Context(),
		stage,
		filepath.Join(parentPath, "repository-reviews.db"),
		stageInfo,
		stageFile,
	); complete || err == nil {
		t.Fatalf("substituted stage replacement = complete:%t error:%v", complete, err)
	}
}

func newTargetParentLiveVerificationCoverageFixture(
	t *testing.T,
) (*retainedStagedTargetParent, string, string, string, *validatedReplacementSeal) {
	t.Helper()
	parentPath := filepath.Join(t.TempDir(), "repository_reviews")
	parent, err := prepareRetainedStagedTargetParent(t.Context(), parentPath)
	if err != nil || parent == nil {
		t.Fatalf("prepare live-verification target parent = %#v, %v", parent, err)
	}
	t.Cleanup(func() { _ = parent.Close() })
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
	t.Cleanup(func() { _ = seal.close() })
	if _, err := parent.SealSoleStage(
		t.Context(), parentPath, stage, stageInfo, seal.handleIdentity,
	); err != nil {
		t.Fatal(err)
	}
	return parent, parentPath, target, stage, seal
}

func TestStagedTargetParentFullIdentityAndCheckerFaultCoverage(t *testing.T) {
	t.Run("replacement state full identity mismatch", func(t *testing.T) {
		target, stage, identity, seal := stagedCoverageReplacement(t)
		other := filepath.Join(t.TempDir(), "other.db")
		if err := os.WriteFile(other, []byte("other"), 0o600); err != nil {
			t.Fatal(err)
		}
		scope := t.Context()
		state := &validatedReplacementState{
			active: true, storeID: "global/auth", target: target,
			stage: stage, stageIdentity: identity,
			stageHandleID: stagedTargetParentTestStageIdentity(t, other),
			stageDigest:   seal.digest, stageFile: seal.file, scope: scope,
		}
		err := (ValidatedReplacement{state: state}).Use(
			t.Context(), "global/auth", target,
			func(context.Context, string, ValidatedReplacementCheck) error {
				t.Fatal("full-identity mismatch reached replacement callback")
				return nil
			},
		)
		if err == nil || dblayer.CodeOf(err) != dblayer.CodeIntegrity {
			t.Fatalf("full-identity mismatch = %v", err)
		}
	})

	t.Run("seal named identity mismatch", func(t *testing.T) {
		_, _, _, seal := stagedCoverageReplacement(t)
		other := filepath.Join(t.TempDir(), "other.db")
		if err := os.WriteFile(other, []byte("other"), 0o600); err != nil {
			t.Fatal(err)
		}
		seal.handleIdentity = stagedTargetParentTestStageIdentity(t, other)
		if err := seal.verify(t.Context()); err == nil ||
			dblayer.CodeOf(err) != dblayer.CodeIntegrity {
			t.Fatalf("seal named full-identity mismatch = %v", err)
		}
	})

	t.Run("checker without provider parent", func(t *testing.T) {
		target, _, _, seal := stagedCoverageReplacement(t)
		parentInfo, err := os.Lstat(filepath.Dir(target))
		if err != nil {
			t.Fatal(err)
		}
		err = invokeStagedLiveVerification(
			t.Context(), "global/auth", target, seal,
			func(ctx context.Context, replacement ValidatedReplacement) error {
				return replacement.UseWithTargetParent(
					ctx, "global/auth", target,
					func(
						useCtx context.Context,
						_ string,
						_ ValidatedReplacementCheck,
						checkParent ValidatedTargetParentCheck,
					) error {
						_, checkErr := checkParent(useCtx, parentInfo)
						return checkErr
					},
				)
			},
		)
		if !errors.Is(err, errValidatedReplacementContract) {
			t.Fatalf("missing provider parent checker = %v", err)
		}
	})

	for _, test := range []struct {
		name string
		use  func(
			t *testing.T,
			cancel context.CancelFunc,
			parentPath string,
			check ValidatedTargetParentCheck,
		) error
	}{
		{
			name: "nil check context",
			use: func(
				_ *testing.T, _ context.CancelFunc, _ string, check ValidatedTargetParentCheck,
			) error {
				_, err := check(nil, nil)
				return err
			},
		},
		{
			name: "canceled check context",
			use: func(
				t *testing.T, _ context.CancelFunc, parentPath string,
				check ValidatedTargetParentCheck,
			) error {
				info, err := os.Lstat(parentPath)
				if err != nil {
					return err
				}
				canceled, cancel := context.WithCancel(t.Context())
				cancel()
				_, err = check(canceled, info)
				return err
			},
		},
		{
			name: "canceled capability scope",
			use: func(
				t *testing.T, cancel context.CancelFunc, parentPath string,
				check ValidatedTargetParentCheck,
			) error {
				info, err := os.Lstat(parentPath)
				if err != nil {
					return err
				}
				cancel()
				_, err = check(t.Context(), info)
				return err
			},
		},
		{
			name: "wrong parent observation",
			use: func(
				t *testing.T, _ context.CancelFunc, _ string, check ValidatedTargetParentCheck,
			) error {
				wrong, err := os.Lstat(t.TempDir())
				if err != nil {
					return err
				}
				_, err = check(t.Context(), wrong)
				return err
			},
		},
		{
			name: "parent inventory changed",
			use: func(
				t *testing.T, _ context.CancelFunc, parentPath string,
				check ValidatedTargetParentCheck,
			) error {
				if err := os.WriteFile(
					filepath.Join(parentPath, "extra"), []byte("extra"), 0o600,
				); err != nil {
					return err
				}
				info, err := os.Lstat(parentPath)
				if err != nil {
					return err
				}
				_, err = check(t.Context(), info)
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent, parentPath, target, _, seal := newTargetParentLiveVerificationCoverageFixture(t)
			invokeCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			err := invokeStagedLiveVerificationWithTargetParent(
				invokeCtx, "global/auth", target, seal, parent,
				func(ctx context.Context, replacement ValidatedReplacement) error {
					return replacement.UseWithTargetParent(
						ctx, "global/auth", target,
						func(
							_ context.Context,
							_ string,
							_ ValidatedReplacementCheck,
							checkParent ValidatedTargetParentCheck,
						) error {
							return test.use(t, cancel, parentPath, checkParent)
						},
					)
				},
			)
			if err == nil {
				t.Fatal("faulted target-parent checker succeeded")
			}
		})
	}

	t.Run("initial target-parent precheck", func(t *testing.T) {
		parent, parentPath, target, _, seal := newTargetParentLiveVerificationCoverageFixture(t)
		if err := os.WriteFile(filepath.Join(parentPath, "extra"), []byte("extra"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := invokeStagedLiveVerificationWithTargetParent(
			t.Context(), "global/auth", target, seal, parent,
			func(context.Context, ValidatedReplacement) error { return nil },
		); err == nil {
			t.Fatal("changed target parent passed invocation precheck")
		}
	})
}
