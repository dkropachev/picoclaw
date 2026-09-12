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

	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

const validatedReplacementTestName = ".target.db.migration-stage-0123456789abcdef0123456789abcdef.db"

func invokeStagedLiveVerificationForTest(
	ctx context.Context,
	storeID dblayer.StoreID,
	target string,
	stage string,
	expected os.FileInfo,
	verify StagedLiveVerification,
) (returnErr error) {
	seal, err := sealValidatedReplacementStage(ctx, stage)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, seal.close()) }()
	if expected == nil || !os.SameFile(expected, seal.identity) {
		return errors.New("test replacement identity differs from its seal")
	}
	return invokeStagedLiveVerification(ctx, storeID, target, seal, verify)
}

func TestValidatedReplacementIsExactSingleUseAndCallbackScoped(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.db")
	stage := filepath.Join(root, validatedReplacementTestName)
	if err := os.WriteFile(stage, []byte("validated"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := os.Lstat(stage)
	if err != nil {
		t.Fatal(err)
	}
	var retained ValidatedReplacement
	uses := 0
	err = invokeStagedLiveVerificationForTest(
		t.Context(),
		"global/auth",
		target,
		stage,
		identity,
		func(ctx context.Context, replacement ValidatedReplacement) error {
			retained = replacement
			return replacement.Use(ctx, "global/auth", target, func(
				_ context.Context, got string, _ ValidatedReplacementCheck,
			) error {
				uses++
				if got != stage {
					return errors.New("replacement stage changed")
				}
				return nil
			})
		},
	)
	if err != nil || uses != 1 {
		t.Fatalf("validated replacement use count=%d error=%v", uses, err)
	}
	if err := retained.Use(
		t.Context(), "global/auth", target,
		func(context.Context, string, ValidatedReplacementCheck) error { return nil },
	); !errors.Is(err, errValidatedReplacementContract) {
		t.Fatalf("retained validated replacement = %v", err)
	}
	if err := (ValidatedReplacement{}).Use(
		t.Context(), "global/auth", target,
		func(context.Context, string, ValidatedReplacementCheck) error { return nil },
	); !errors.Is(err, errValidatedReplacementContract) {
		t.Fatalf("zero validated replacement = %v", err)
	}
}

func TestValidatedReplacementRejectsOmittedWrongAndRepeatedUse(t *testing.T) {
	newFixture := func(t *testing.T) (string, string, os.FileInfo) {
		t.Helper()
		root := t.TempDir()
		target := filepath.Join(root, "target.db")
		stage := filepath.Join(root, validatedReplacementTestName)
		if err := os.WriteFile(stage, []byte("validated"), 0o600); err != nil {
			t.Fatal(err)
		}
		identity, err := os.Lstat(stage)
		if err != nil {
			t.Fatal(err)
		}
		return target, stage, identity
	}
	for _, test := range []struct {
		name string
		use  func(context.Context, ValidatedReplacement, string) error
	}{
		{
			name: "omitted",
			use:  func(context.Context, ValidatedReplacement, string) error { return nil },
		},
		{
			name: "wrong store",
			use: func(ctx context.Context, replacement ValidatedReplacement, target string) error {
				return replacement.Use(ctx, "launcher/auth", target, func(
					context.Context, string, ValidatedReplacementCheck,
				) error {
					return nil
				})
			},
		},
		{
			name: "wrong target",
			use: func(ctx context.Context, replacement ValidatedReplacement, target string) error {
				return replacement.Use(ctx, "global/auth", target+".other", func(
					context.Context, string, ValidatedReplacementCheck,
				) error {
					return nil
				})
			},
		},
		{
			name: "repeated",
			use: func(ctx context.Context, replacement ValidatedReplacement, target string) error {
				first := replacement.Use(ctx, "global/auth", target, func(
					context.Context, string, ValidatedReplacementCheck,
				) error {
					return nil
				})
				second := replacement.Use(ctx, "global/auth", target, func(
					context.Context, string, ValidatedReplacementCheck,
				) error {
					return nil
				})
				return errors.Join(first, second)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			target, stage, identity := newFixture(t)
			err := invokeStagedLiveVerificationForTest(
				t.Context(), "global/auth", target, stage, identity,
				func(ctx context.Context, replacement ValidatedReplacement) error {
					return test.use(ctx, replacement, target)
				},
			)
			if !errors.Is(err, errValidatedReplacementContract) {
				t.Fatalf("validated replacement contract error = %v", err)
			}
		})
	}
}

func TestValidatedReplacementDetectsStageAndSidecarDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "main replacement",
			mutate: func(t *testing.T, stage string) {
				t.Helper()
				if err := os.Rename(stage, stage+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(stage, []byte("validated"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "wal appearance",
			mutate: func(t *testing.T, stage string) {
				t.Helper()
				if err := os.WriteFile(stage+"-wal", []byte("wal"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "shm appearance",
			mutate: func(t *testing.T, stage string) {
				t.Helper()
				if err := os.WriteFile(stage+"-shm", []byte("shm"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "journal appearance",
			mutate: func(t *testing.T, stage string) {
				t.Helper()
				if err := os.WriteFile(stage+"-journal", []byte("journal"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "target.db")
			stage := filepath.Join(root, validatedReplacementTestName)
			if err := os.WriteFile(stage, []byte("validated"), 0o600); err != nil {
				t.Fatal(err)
			}
			identity, err := os.Lstat(stage)
			if err != nil {
				t.Fatal(err)
			}
			err = invokeStagedLiveVerificationForTest(
				t.Context(), "global/auth", target, stage, identity,
				func(ctx context.Context, replacement ValidatedReplacement) error {
					return replacement.Use(ctx, "global/auth", target, func(
						context.Context, string, ValidatedReplacementCheck,
					) error {
						test.mutate(t, stage)
						return nil
					})
				},
			)
			if err == nil || !strings.Contains(err.Error(), "validated SQLite replacement") {
				t.Fatalf("stage drift error = %v", err)
			}
		})
	}
}

func TestValidatedReplacementDetectsSameMetadataContentMutation(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.db")
	stage := filepath.Join(root, validatedReplacementTestName)
	if err := os.WriteFile(stage, []byte("validated"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := os.Lstat(stage)
	if err != nil {
		t.Fatal(err)
	}
	err = invokeStagedLiveVerificationForTest(
		t.Context(), "global/auth", target, stage, identity,
		func(ctx context.Context, replacement ValidatedReplacement) error {
			return replacement.Use(ctx, "global/auth", target, func(
				ctx context.Context,
				_ string,
				check ValidatedReplacementCheck,
			) error {
				observed, statErr := os.Lstat(stage)
				if statErr != nil {
					return statErr
				}
				if _, checkErr := check(ctx, observed); checkErr != nil {
					return checkErr
				}
				if writeErr := os.WriteFile(stage, []byte("mutated!!"), 0o600); writeErr != nil {
					return writeErr
				}
				return os.Chtimes(stage, time.Now(), identity.ModTime())
			})
		},
	)
	if err == nil || !strings.Contains(err.Error(), "contents changed") {
		t.Fatalf("same-metadata content mutation = %v", err)
	}
}

func TestValidatedReplacementSealRejectsStageSidecars(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(strings.TrimPrefix(suffix, "-"), func(t *testing.T) {
			root := t.TempDir()
			stage := filepath.Join(root, validatedReplacementTestName)
			if err := os.WriteFile(stage, []byte("validated"), 0o600); err != nil {
				t.Fatal(err)
			}
			seal, err := sealValidatedReplacementStage(t.Context(), stage)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if closeErr := seal.close(); closeErr != nil {
					t.Errorf("close replacement seal: %v", closeErr)
				}
			}()
			if err := os.WriteFile(stage+suffix, []byte("sidecar"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := seal.verify(t.Context()); err == nil ||
				!strings.Contains(err.Error(), "sidecars changed") {
				t.Fatalf("seal with stage%s = %v", suffix, err)
			}
		})
	}
}

func TestValidatedReplacementWaitsForEscapedUseAndRejectsIt(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.db")
	stage := filepath.Join(root, validatedReplacementTestName)
	if err := os.WriteFile(stage, []byte("validated"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := os.Lstat(stage)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	callbackReturned := make(chan struct{})
	useResult := make(chan error, 1)
	result := make(chan error, 1)
	go func() {
		result <- invokeStagedLiveVerificationForTest(
			t.Context(), "global/auth", target, stage, identity,
			func(ctx context.Context, replacement ValidatedReplacement) error {
				go func() {
					useResult <- replacement.Use(
						ctx, "global/auth", target, func(
							context.Context, string, ValidatedReplacementCheck,
						) error {
							close(entered)
							<-release
							return nil
						},
					)
				}()
				<-entered
				close(callbackReturned)
				return nil
			},
		)
	}()
	<-callbackReturned
	select {
	case err := <-result:
		t.Fatalf("escaped use was not drained: %v", err)
	default:
	}
	close(release)
	if err := <-useResult; !errors.Is(err, errValidatedReplacementContract) {
		t.Fatalf("in-flight use contract error = %v", err)
	}
	if err := <-result; !errors.Is(err, errValidatedReplacementContract) {
		t.Fatalf("escaped use contract error = %v", err)
	}
}

func TestValidatedReplacementRejectsConcurrentExactChecks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.db")
	stage := filepath.Join(root, validatedReplacementTestName)
	if err := os.WriteFile(stage, []byte("validated"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := os.Lstat(stage)
	if err != nil {
		t.Fatal(err)
	}
	err = invokeStagedLiveVerificationForTest(
		t.Context(), "global/auth", target, stage, identity,
		func(ctx context.Context, replacement ValidatedReplacement) error {
			return replacement.Use(ctx, "global/auth", target, func(
				ctx context.Context,
				_ string,
				check ValidatedReplacementCheck,
			) error {
				replacement.state.stageFileMu.Lock()
				first := make(chan error, 1)
				go func() {
					_, checkErr := check(ctx, identity)
					first <- checkErr
				}()
				deadline := time.Now().Add(time.Second)
				for {
					replacement.state.Lock()
					checking := replacement.state.checking
					replacement.state.Unlock()
					if checking {
						break
					}
					if time.Now().After(deadline) {
						replacement.state.stageFileMu.Unlock()
						return errors.New("first exact check was not admitted")
					}
					runtime.Gosched()
				}
				_, concurrentErr := check(ctx, identity)
				replacement.state.stageFileMu.Unlock()
				if firstErr := <-first; firstErr != nil {
					return firstErr
				}
				return concurrentErr
			})
		},
	)
	if !errors.Is(err, errValidatedReplacementContract) {
		t.Fatalf("concurrent exact check = %v", err)
	}
}

func TestValidatedReplacementPathRequiresExactTargetDerivedName(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.db")
	valid := filepath.Join(root, validatedReplacementTestName)
	if err := validateTargetDerivedReplacementPath(target, valid); err != nil {
		t.Fatalf("valid target-derived replacement path = %v", err)
	}
	for _, test := range []struct {
		name  string
		stage string
	}{
		{name: "near name", stage: valid + ".other"},
		{name: "wrong target base", stage: filepath.Join(root, strings.Replace(validatedReplacementTestName, "target.db", "other.db", 1))},
		{name: "wrong parent", stage: filepath.Join(t.TempDir(), validatedReplacementTestName)},
		{name: "uppercase nonce", stage: filepath.Join(root, strings.Replace(validatedReplacementTestName, "abcdef", "ABCDEF", 1))},
		{name: "short nonce", stage: filepath.Join(root, ".target.db.migration-stage-0123456789abcdef.db")},
		{name: "long nonce", stage: filepath.Join(root, ".target.db.migration-stage-0123456789abcdef0123456789abcdef00.db")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateTargetDerivedReplacementPath(target, test.stage); err == nil {
				t.Fatalf("invalid target-derived replacement path was accepted: %s", test.stage)
			}
		})
	}
}
