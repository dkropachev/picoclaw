package sqliteprovider

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

type stagedCoverageFileInfo struct {
	os.FileInfo
	size int64
}

type stagedCoverageAuthority struct {
	sync.Mutex
	checks                int
	failCheck             int
	checkErr              error
	pinErr                error
	discardErr            error
	checkReplacementErr   error
	failCheckReplacement  int
	checkReplacementCalls int
	pinned                string
	onCheck               func(int)
	onCheckReplacement    func(int, string)
}

func (authority *stagedCoverageAuthority) Check(context.Context) error {
	authority.Lock()
	defer authority.Unlock()
	authority.checks++
	if authority.onCheck != nil {
		authority.onCheck(authority.checks)
	}
	if authority.checks == authority.failCheck {
		return authority.checkErr
	}
	return nil
}

func (*stagedCoverageAuthority) Reconcile(context.Context) error { return nil }

func (authority *stagedCoverageAuthority) PinReplacement(_ context.Context, path string) error {
	authority.Lock()
	defer authority.Unlock()
	authority.pinned = path
	return authority.pinErr
}

func (authority *stagedCoverageAuthority) CheckReplacement(
	_ context.Context,
	path string,
) error {
	authority.Lock()
	defer authority.Unlock()
	authority.checkReplacementCalls++
	if authority.onCheckReplacement != nil {
		authority.onCheckReplacement(authority.checkReplacementCalls, path)
	}
	if authority.checkReplacementCalls == authority.failCheckReplacement {
		return authority.checkReplacementErr
	}
	return nil
}

func (authority *stagedCoverageAuthority) DiscardReplacement(context.Context) error {
	authority.Lock()
	defer authority.Unlock()
	return authority.discardErr
}

func (*stagedCoverageAuthority) ReconcileReplacement(context.Context) error { return nil }

func (info stagedCoverageFileInfo) Size() int64 { return info.size }

func stagedCoverageReplacement(t *testing.T) (string, string, os.FileInfo, *validatedReplacementSeal) {
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
	seal, err := sealValidatedReplacementStage(t.Context(), stage)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := seal.close(); err != nil {
			t.Errorf("close validated replacement seal: %v", err)
		}
	})
	return target, stage, identity, seal
}

func TestStagedCoverageValidatedReplacementSealBoundaries(t *testing.T) {
	t.Run("seal input and identity", func(t *testing.T) {
		root := t.TempDir()
		stage := filepath.Join(root, validatedReplacementTestName)
		if seal, err := sealValidatedReplacementStage(nil, stage); seal != nil ||
			!errors.Is(err, errValidatedReplacementContract) {
			t.Fatalf("nil-context seal = %#v, %v", seal, err)
		}
		if seal, err := sealValidatedReplacementStage(t.Context(), ""); seal != nil ||
			!errors.Is(err, errValidatedReplacementContract) {
			t.Fatalf("empty-path seal = %#v, %v", seal, err)
		}
		if seal, err := sealValidatedReplacementStage(t.Context(), stage); seal != nil ||
			dblayer.CodeOf(err) != dblayer.CodeIntegrity {
			t.Fatalf("missing seal = %#v, %v", seal, err)
		}
		if err := os.Mkdir(stage, 0o700); err != nil {
			t.Fatal(err)
		}
		if seal, err := sealValidatedReplacementStage(t.Context(), stage); seal != nil ||
			dblayer.CodeOf(err) != dblayer.CodeIntegrity {
			t.Fatalf("directory seal = %#v, %v", seal, err)
		}
	})

	t.Run("nil verify and repeated close", func(t *testing.T) {
		if verifyErr := (*validatedReplacementSeal)(nil).verify(t.Context()); !errors.Is(
			verifyErr,
			errValidatedReplacementContract,
		) {
			t.Fatalf("nil seal verify = %v", verifyErr)
		}
		if err := (*validatedReplacementSeal)(nil).close(); err != nil {
			t.Fatalf("nil seal close = %v", err)
		}
		_, _, _, seal := stagedCoverageReplacement(t)
		if err := seal.close(); err != nil {
			t.Fatal(err)
		}
		if err := seal.close(); err != nil {
			t.Fatalf("repeat seal close = %v", err)
		}
	})

	t.Run("fingerprint and digest changes", func(t *testing.T) {
		_, stage, identity, seal := stagedCoverageReplacement(t)
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		if err := seal.verify(canceled); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled seal verify = %v", err)
		}
		if err := os.WriteFile(stage, []byte("mutation!"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(stage, time.Now(), identity.ModTime()); err != nil {
			t.Fatal(err)
		}
		if err := seal.verify(t.Context()); err == nil ||
			!strings.Contains(err.Error(), "contents changed after validation") {
			t.Fatalf("seal digest change = %v", err)
		}
	})
}

func TestStagedCoverageValidatedReplacementOpenAndFingerprintBoundaries(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "stage.db")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, statErr := os.Lstat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	oversized := stagedCoverageFileInfo{
		FileInfo: identity,
		size:     maximumImmutableGenerationMemberBytes + 1,
	}
	for _, test := range []struct {
		name     string
		ctx      context.Context
		path     string
		expected os.FileInfo
	}{
		{name: "nil context", path: path, expected: identity},
		{name: "empty path", ctx: t.Context(), expected: identity},
		{name: "nil identity", ctx: t.Context(), path: path},
		{name: "negative size", ctx: t.Context(), path: path, expected: stagedCoverageFileInfo{FileInfo: identity, size: -1}},
		{name: "oversized", ctx: t.Context(), path: path, expected: oversized},
	} {
		t.Run(test.name, func(t *testing.T) {
			file, _, openErr := openValidatedReplacementStage(test.ctx, test.path, test.expected)
			if file != nil || !errors.Is(openErr, errValidatedReplacementContract) {
				t.Fatalf("invalid open = %#v, %v", file, openErr)
			}
		})
	}

	other := filepath.Join(root, "other.db")
	if writeErr := os.WriteFile(other, []byte("payload"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	otherIdentity, otherStatErr := os.Lstat(other)
	if otherStatErr != nil {
		t.Fatal(otherStatErr)
	}
	if file, _, openErr := openValidatedReplacementStage(
		t.Context(), path, otherIdentity,
	); file != nil || dblayer.CodeOf(openErr) != dblayer.CodeIntegrity {
		t.Fatalf("mismatched identity open = %#v, %v", file, openErr)
	}

	if runtime.GOOS != "windows" {
		if chmodErr := os.Chmod(path, 0o400); chmodErr != nil {
			t.Fatal(chmodErr)
		}
		readOnlyIdentity, readOnlyStatErr := os.Lstat(path)
		if readOnlyStatErr != nil {
			t.Fatal(readOnlyStatErr)
		}
		if file, _, openErr := openValidatedReplacementStage(
			t.Context(), path, readOnlyIdentity,
		); file != nil || openErr == nil {
			t.Fatalf("read-only replacement open = %#v, %v", file, openErr)
		}
		if seal, sealErr := sealValidatedReplacementStage(
			t.Context(), path,
		); seal != nil || sealErr == nil {
			t.Fatalf("read-only replacement seal = %#v, %v", seal, sealErr)
		}
	}

	if chmodErr := os.Chmod(path, 0o600); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	identity, statErr = os.Lstat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	file, openErr := os.OpenFile(path, os.O_RDWR, 0o600)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer file.Close()
	for _, test := range []struct {
		name     string
		ctx      context.Context
		expected os.FileInfo
		file     *os.File
	}{
		{name: "nil context", expected: identity, file: file},
		{name: "nil identity", ctx: t.Context(), file: file},
		{name: "nil file", ctx: t.Context(), expected: identity},
		{name: "oversized", ctx: t.Context(), expected: oversized, file: file},
	} {
		t.Run("fingerprint "+test.name, func(t *testing.T) {
			if _, fingerprintErr := fingerprintOpenedValidatedReplacementStage(
				test.ctx, path, test.expected, test.file,
			); !errors.Is(fingerprintErr, errValidatedReplacementContract) {
				t.Fatalf("invalid fingerprint = %v", fingerprintErr)
			}
		})
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if opened, _, canceledOpenErr := openValidatedReplacementStage(
		canceled, path, identity,
	); opened != nil || !errors.Is(canceledOpenErr, context.Canceled) {
		t.Fatalf("canceled replacement open = %#v, %v", opened, canceledOpenErr)
	}
	if _, fingerprintErr := fingerprintOpenedValidatedReplacementStage(
		canceled, path, identity, file,
	); !errors.Is(fingerprintErr, context.Canceled) {
		t.Fatalf("canceled fingerprint = %v", fingerprintErr)
	}
	if verifyErr := verifyOpenedValidatedReplacementStage(path, identity, nil); !errors.Is(
		verifyErr,
		errValidatedReplacementContract,
	) {
		t.Fatalf("nil opened replacement = %v", verifyErr)
	}
	closed, closedOpenErr := os.Open(path)
	if closedOpenErr != nil {
		t.Fatal(closedOpenErr)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := verifyOpenedValidatedReplacementStage(path, identity, closed); err == nil {
		t.Fatal("closed replacement handle passed retained verification")
	}
}

func TestStagedCoverageInvokeLiveVerificationBoundaries(t *testing.T) {
	target, stage, identity, seal := stagedCoverageReplacement(t)
	valid := func(context.Context, ValidatedReplacement) error { return nil }
	for _, test := range []struct {
		name    string
		ctx     context.Context
		id      dblayer.StoreID
		target  string
		seal    *validatedReplacementSeal
		verify  StagedLiveVerification
		wantNil bool
	}{
		{name: "nil verifier", ctx: t.Context(), id: "global/auth", target: target, seal: seal, wantNil: true},
		{name: "nil context", id: "global/auth", target: target, seal: seal, verify: valid},
		{name: "invalid store", ctx: t.Context(), id: "bad id", target: target, seal: seal, verify: valid},
		{name: "relative target", ctx: t.Context(), id: "global/auth", target: "relative.db", seal: seal, verify: valid},
		{name: "nil seal", ctx: t.Context(), id: "global/auth", target: target, verify: valid},
		{name: "empty seal", ctx: t.Context(), id: "global/auth", target: target, seal: &validatedReplacementSeal{}, verify: valid},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := invokeStagedLiveVerification(test.ctx, test.id, test.target, test.seal, test.verify)
			if test.wantNil {
				if err != nil {
					t.Fatalf("nil verifier = %v", err)
				}
				return
			}
			if test.name == "relative target" {
				if dblayer.CodeOf(err) != dblayer.CodeIntegrity {
					t.Fatalf("relative live-verification target = %v", err)
				}
				return
			}
			if !errors.Is(err, errValidatedReplacementContract) {
				t.Fatalf("invalid live verification = %v", err)
			}
		})
	}

	wrongTarget := filepath.Join(filepath.Dir(target), "other.db")
	if invokeErr := invokeStagedLiveVerification(
		t.Context(), "global/auth", wrongTarget, seal, valid,
	); dblayer.CodeOf(invokeErr) != dblayer.CodeIntegrity {
		t.Fatalf("wrong target-derived path = %v", invokeErr)
	}
	if err := seal.close(); err != nil {
		t.Fatal(err)
	}
	if invokeErr := invokeStagedLiveVerification(
		t.Context(), "global/auth", target, seal, valid,
	); !errors.Is(invokeErr, errValidatedReplacementContract) {
		t.Fatalf("closed seal invocation = %v", invokeErr)
	}

	panicTarget, _, _, panicSeal := stagedCoverageReplacement(t)
	if err := invokeStagedLiveVerification(
		t.Context(), "global/auth", panicTarget, panicSeal,
		func(context.Context, ValidatedReplacement) error { panic("callback") },
	); !errors.Is(err, errValidatedReplacementContract) {
		t.Fatalf("panicked live verification = %v", err)
	}
	_ = stage
	_ = identity
}

func TestStagedCoverageValidatedReplacementUseAndExactCheckErrors(t *testing.T) {
	t.Run("canceled use context", func(t *testing.T) {
		target, _, _, seal := stagedCoverageReplacement(t)
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		err := invokeStagedLiveVerification(
			t.Context(), "global/auth", target, seal,
			func(_ context.Context, replacement ValidatedReplacement) error {
				return replacement.Use(canceled, "global/auth", target, func(
					context.Context, string, ValidatedReplacementCheck,
				) error {
					t.Fatal("canceled use invoked callback")
					return nil
				})
			},
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled replacement use = %v", err)
		}
	})

	t.Run("canceled scope before use", func(t *testing.T) {
		target, _, _, seal := stagedCoverageReplacement(t)
		parent, cancel := context.WithCancel(t.Context())
		err := invokeStagedLiveVerification(
			parent, "global/auth", target, seal,
			func(_ context.Context, replacement ValidatedReplacement) error {
				cancel()
				return replacement.Use(t.Context(), "global/auth", target, func(
					context.Context, string, ValidatedReplacementCheck,
				) error {
					t.Fatal("canceled scope invoked callback")
					return nil
				})
			},
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled replacement scope = %v", err)
		}
	})

	t.Run("exact-check inputs and observation", func(t *testing.T) {
		target, _, identity, seal := stagedCoverageReplacement(t)
		otherPath := filepath.Join(filepath.Dir(target), "other")
		if err := os.WriteFile(otherPath, []byte("other"), 0o600); err != nil {
			t.Fatal(err)
		}
		other, otherStatErr := os.Lstat(otherPath)
		if otherStatErr != nil {
			t.Fatal(otherStatErr)
		}
		invokeErr := invokeStagedLiveVerification(
			t.Context(), "global/auth", target, seal,
			func(ctx context.Context, replacement ValidatedReplacement) error {
				return replacement.Use(ctx, "global/auth", target, func(
					ctx context.Context, _ string, check ValidatedReplacementCheck,
				) error {
					if _, checkErr := check(nil, identity); !errors.Is(
						checkErr,
						errValidatedReplacementContract,
					) {
						return errors.New("nil exact-check context was accepted")
					}
					canceled, cancel := context.WithCancel(ctx)
					cancel()
					if _, checkErr := check(canceled, identity); !errors.Is(
						checkErr,
						context.Canceled,
					) {
						return errors.New("canceled exact-check context was accepted")
					}
					if _, checkErr := check(ctx, other); dblayer.CodeOf(
						checkErr,
					) != dblayer.CodeIntegrity {
						return errors.New("mismatched exact-check observation was accepted")
					}
					return nil
				})
			},
		)
		if invokeErr != nil {
			t.Fatal(invokeErr)
		}
	})

	t.Run("stage changed before use", func(t *testing.T) {
		target, stage, _, seal := stagedCoverageReplacement(t)
		err := invokeStagedLiveVerification(
			t.Context(), "global/auth", target, seal,
			func(ctx context.Context, replacement ValidatedReplacement) error {
				if err := os.Rename(stage, stage+".old"); err != nil {
					return err
				}
				return replacement.Use(ctx, "global/auth", target, func(
					context.Context, string, ValidatedReplacementCheck,
				) error {
					return nil
				})
			},
		)
		if dblayer.CodeOf(err) != dblayer.CodeIntegrity {
			t.Fatalf("changed pre-use stage = %v", err)
		}
	})

	t.Run("closed retained handle", func(t *testing.T) {
		target, _, identity, seal := stagedCoverageReplacement(t)
		err := invokeStagedLiveVerification(
			t.Context(), "global/auth", target, seal,
			func(ctx context.Context, replacement ValidatedReplacement) error {
				return replacement.Use(ctx, "global/auth", target, func(
					ctx context.Context, _ string, check ValidatedReplacementCheck,
				) error {
					if err := replacement.state.stageFile.Close(); err != nil {
						return err
					}
					seal.file = nil
					_, err := check(ctx, identity)
					return err
				})
			},
		)
		if dblayer.CodeOf(err) != dblayer.CodeIntegrity {
			t.Fatalf("closed retained handle check = %v", err)
		}
	})
}

func TestStagedCoverageValidatedReplacementScopeCancellationWhileChecking(t *testing.T) {
	target, _, identity, seal := stagedCoverageReplacement(t)
	parent, cancel := context.WithCancel(t.Context())
	err := invokeStagedLiveVerification(
		parent, "global/auth", target, seal,
		func(_ context.Context, replacement ValidatedReplacement) error {
			return replacement.Use(t.Context(), "global/auth", target, func(
				_ context.Context, _ string, check ValidatedReplacementCheck,
			) error {
				replacement.state.stageFileMu.Lock()
				result := make(chan error, 1)
				go func() {
					_, checkErr := check(t.Context(), identity)
					result <- checkErr
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
						return errors.New("exact check was not admitted")
					}
					runtime.Gosched()
				}
				cancel()
				replacement.state.stageFileMu.Unlock()
				return <-result
			})
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("scope cancellation during exact check = %v", err)
	}
}

func TestStagedCoverageValidatedReplacementDetachedCallerSeesScopeCancellation(t *testing.T) {
	target, _, _, seal := stagedCoverageReplacement(t)
	parent, cancel := context.WithCancel(t.Context())
	err := invokeStagedLiveVerification(
		parent, "global/auth", target, seal,
		func(_ context.Context, replacement ValidatedReplacement) error {
			return replacement.Use(context.Background(), "global/auth", target, func(
				context.Context, string, ValidatedReplacementCheck,
			) error {
				cancel()
				return nil
			})
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("detached caller scope cancellation = %v", err)
	}
}

func TestStagedCoverageExactCheckReturnsDuringCallback(t *testing.T) {
	target, _, identity, seal := stagedCoverageReplacement(t)
	err := invokeStagedLiveVerification(
		t.Context(), "global/auth", target, seal,
		func(_ context.Context, replacement ValidatedReplacement) error {
			return replacement.Use(t.Context(), "global/auth", target, func(
				_ context.Context, _ string, check ValidatedReplacementCheck,
			) error {
				replacement.state.stageFileMu.Lock()
				go func() { _, _ = check(t.Context(), identity) }()
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
						return errors.New("exact check was not admitted")
					}
					runtime.Gosched()
				}
				go func() {
					time.Sleep(time.Millisecond)
					replacement.state.stageFileMu.Unlock()
				}()
				return nil
			})
		},
	)
	if !errors.Is(err, errValidatedReplacementContract) ||
		!strings.Contains(err.Error(), "returned during exact check") {
		t.Fatalf("callback returned during exact check = %v", err)
	}
}

func TestStagedCoverageValidatedReplacementRetainedIdentity(t *testing.T) {
	target, _, identity, seal := stagedCoverageReplacement(t)
	var retained fileidentity.Identity
	err := invokeStagedLiveVerification(
		t.Context(), "global/auth", target, seal,
		func(ctx context.Context, replacement ValidatedReplacement) error {
			return replacement.Use(ctx, "global/auth", target, func(
				ctx context.Context, _ string, check ValidatedReplacementCheck,
			) error {
				var err error
				retained, err = check(ctx, identity)
				return err
			})
		},
	)
	if err != nil || !retained.Valid() {
		t.Fatalf("retained replacement identity = %#v, %v", retained, err)
	}
}

func TestStagedCoverageLiveVerificationRejectsDriftedSealBeforeCallback(t *testing.T) {
	target, stage, _, seal := stagedCoverageReplacement(t)
	if err := stagedCoverageMutateSameMetadata(stage); err != nil {
		t.Fatal(err)
	}
	called := false
	err := invokeStagedLiveVerification(
		t.Context(), "global/auth", target, seal,
		func(context.Context, ValidatedReplacement) error {
			called = true
			return nil
		},
	)
	if err == nil || called || !strings.Contains(err.Error(), "contents changed") {
		t.Fatalf("drifted pre-callback seal called=%t error=%v", called, err)
	}
}

func stagedCoverageLiveVerifier(
	target string,
	operation func(string) error,
) StagedLiveVerification {
	return func(ctx context.Context, replacement ValidatedReplacement) error {
		return replacement.Use(ctx, "global/auth", target, func(
			ctx context.Context,
			stage string,
			check ValidatedReplacementCheck,
		) error {
			observed, err := os.Lstat(stage)
			if err != nil {
				return err
			}
			if _, err := check(ctx, observed); err != nil {
				return err
			}
			if operation != nil {
				return operation(stage)
			}
			return nil
		})
	}
}

func stagedCoverageSource(path string) ImmutableGenerationSource {
	return immutableGenerationSourceForTest(func(
		ctx context.Context,
		use func(context.Context, string) error,
	) error {
		return use(ctx, path)
	})
}

func stagedCoverageExistingMigration(
	t *testing.T,
	authority *stagedCoverageAuthority,
	ops stagedMigrationOps,
	live func(string, string) StagedLiveVerification,
) (MaintenanceResult, error) {
	t.Helper()
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.db")
	target := filepath.Join(root, "target.db")
	createProviderOfflineFixture(t, sourcePath)
	createProviderOfflineFixture(t, target)
	if ops.replace == nil {
		ops.replace = replaceStagedGeneration
	}
	if ops.activate == nil {
		ops.activate = activateInstalledGeneration
	}
	verification := stagedCoverageLiveVerifier(target, nil)
	if live != nil {
		verification = live(sourcePath, target)
	}
	return migrateStagedOfflineAuthorizedWithLiveVerification(
		t.Context(), stagedCoverageSource(sourcePath), target, 5*time.Second, 1,
		installProviderOfflineFixture, acceptStagedValidation,
		verification, authority, ops,
	)
}

func stagedCoverageMutateSameMetadata(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	buffer := []byte{0}
	if _, err := file.ReadAt(buffer, 0); err != nil {
		_ = file.Close()
		return err
	}
	buffer[0] ^= 0xff
	if _, err := file.WriteAt(buffer, 0); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Chtimes(path, time.Now(), info.ModTime())
}

func TestStagedCoverageAuthorizedMigrationAuthorityFailures(t *testing.T) {
	canary := errors.New("authority coverage failure")
	for _, test := range []struct {
		name      string
		failCheck int
		installed bool
	}{
		{name: "before working retirement", failCheck: 7},
		{name: "before replacement pin", failCheck: 8},
		{name: "after replacement pin", failCheck: 9},
		{name: "after live verification", failCheck: 10},
		{name: "after installed activation", failCheck: 11, installed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority := &stagedCoverageAuthority{
				failCheck: test.failCheck,
				checkErr:  canary,
			}
			result, err := stagedCoverageExistingMigration(
				t, authority, stagedMigrationOps{}, nil,
			)
			if !errors.Is(err, canary) || result.installed != test.installed {
				t.Fatalf("authority failure result=%#v checks=%d error=%v", result, authority.checks, err)
			}
			if test.installed && dblayer.CodeOf(err) != dblayer.CodeOutcomeUnknown {
				t.Fatalf("post-install authority error code = %v", err)
			}
		})
	}
}

func TestStagedCoverageAuthorizedMigrationPinFailures(t *testing.T) {
	canary := errors.New("replacement pin coverage failure")
	t.Run("pin and discard", func(t *testing.T) {
		authority := &stagedCoverageAuthority{pinErr: canary, discardErr: canary}
		result, err := stagedCoverageExistingMigration(t, authority, stagedMigrationOps{}, nil)
		if result.installed || !errors.Is(err, canary) ||
			!errors.Is(err, errStagedReplacementRemainsPinned) {
			t.Fatalf("pin/discard failure result=%#v error=%v", result, err)
		}
	})
	for _, call := range []int{1, 2} {
		t.Run("check replacement "+string(rune('0'+call)), func(t *testing.T) {
			authority := &stagedCoverageAuthority{
				failCheckReplacement: call,
				checkReplacementErr:  canary,
			}
			result, err := stagedCoverageExistingMigration(t, authority, stagedMigrationOps{}, nil)
			if result.installed || !errors.Is(err, canary) {
				t.Fatalf("replacement check %d result=%#v error=%v", call, result, err)
			}
		})
	}
}

func TestStagedCoverageAuthorizedMigrationSealAndTargetDrift(t *testing.T) {
	t.Run("seal drift before live verification", func(t *testing.T) {
		var mutationErr error
		authority := &stagedCoverageAuthority{
			onCheckReplacement: func(call int, stage string) {
				if call == 1 {
					mutationErr = stagedCoverageMutateSameMetadata(stage)
				}
			},
		}
		result, err := stagedCoverageExistingMigration(t, authority, stagedMigrationOps{}, nil)
		if mutationErr != nil {
			t.Fatal(mutationErr)
		}
		if result.installed || err == nil || !strings.Contains(err.Error(), "contents changed") {
			t.Fatalf("pre-live seal drift result=%#v error=%v", result, err)
		}
	})

	t.Run("seal drift after live verification", func(t *testing.T) {
		var mutationErr error
		authority := &stagedCoverageAuthority{
			onCheckReplacement: func(call int, stage string) {
				if call == 2 {
					mutationErr = stagedCoverageMutateSameMetadata(stage)
				}
			},
		}
		result, err := stagedCoverageExistingMigration(t, authority, stagedMigrationOps{}, nil)
		if mutationErr != nil {
			t.Fatal(mutationErr)
		}
		if result.installed || err == nil || !strings.Contains(err.Error(), "contents changed") {
			t.Fatalf("post-live seal drift result=%#v error=%v", result, err)
		}
	})

	t.Run("target drift after live verification", func(t *testing.T) {
		var target string
		authority := &stagedCoverageAuthority{}
		result, err := stagedCoverageExistingMigration(
			t,
			authority,
			stagedMigrationOps{},
			func(_ string, gotTarget string) StagedLiveVerification {
				target = gotTarget
				return stagedCoverageLiveVerifier(gotTarget, func(string) error {
					if err := os.Rename(gotTarget, gotTarget+".old"); err != nil {
						return err
					}
					return os.WriteFile(gotTarget, []byte("decoy"), 0o600)
				})
			},
		)
		if target == "" || result.installed || err == nil ||
			!strings.Contains(err.Error(), "target changed") {
			t.Fatalf("target drift result=%#v target=%q error=%v", result, target, err)
		}
	})
}

func TestStagedCoverageAuthorizedMigrationWorkingRetention(t *testing.T) {
	t.Run("external cleanup closes copied retention", func(t *testing.T) {
		ops := stagedMigrationOps{discard: discardStagedGeneration}
		result, err := stagedCoverageExistingMigration(
			t, &stagedCoverageAuthority{}, ops, nil,
		)
		if err != nil || !result.installed {
			t.Fatalf("external working cleanup result=%#v error=%v", result, err)
		}
	})

	t.Run("missing copied retention", func(t *testing.T) {
		ops := stagedMigrationOps{
			copySource: func(
				context.Context, ImmutableGenerationSource, string,
			) (bool, *retainedStagedGeneration, error) {
				return true, nil, nil
			},
		}
		result, err := stagedCoverageExistingMigration(
			t, &stagedCoverageAuthority{}, ops, nil,
		)
		if result.installed || err == nil || !strings.Contains(err.Error(), "retention is unavailable") {
			t.Fatalf("missing working retention result=%#v error=%v", result, err)
		}
	})

	t.Run("closed copied retention", func(t *testing.T) {
		ops := stagedMigrationOps{
			copySource: func(
				ctx context.Context,
				source ImmutableGenerationSource,
				stage string,
			) (bool, *retainedStagedGeneration, error) {
				exists, retained, err := copyImmutableGenerationToRetainedStage(ctx, source, stage)
				if err != nil {
					return exists, retained, err
				}
				if err := retained.Close(); err != nil {
					return false, nil, err
				}
				return exists, retained, nil
			},
		}
		result, err := stagedCoverageExistingMigration(
			t, &stagedCoverageAuthority{}, ops, nil,
		)
		if result.installed || err == nil || !strings.Contains(err.Error(), "recheck retained") {
			t.Fatalf("closed working retention result=%#v error=%v", result, err)
		}
	})

	t.Run("working sidecar before retirement", func(t *testing.T) {
		var working string
		var sidecarErr error
		ops := stagedMigrationOps{
			copySource: func(
				ctx context.Context,
				source ImmutableGenerationSource,
				stage string,
			) (bool, *retainedStagedGeneration, error) {
				working = stage
				return copyImmutableGenerationToRetainedStage(ctx, source, stage)
			},
		}
		authority := &stagedCoverageAuthority{
			onCheck: func(call int) {
				if call == 7 {
					sidecarErr = os.WriteFile(working+"-wal", []byte("wal"), 0o600)
				}
			},
		}
		result, err := stagedCoverageExistingMigration(t, authority, ops, nil)
		if sidecarErr != nil {
			t.Fatal(sidecarErr)
		}
		if result.installed || err == nil || !strings.Contains(err.Error(), "active sidecar") {
			t.Fatalf("working sidecar result=%#v error=%v", result, err)
		}
	})
}

func TestStagedCoverageAuthorizedMigrationPostCutoverCleanup(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "missing-source.db")
	target := filepath.Join(root, "target.db")
	canary := errors.New("post-cutover working cleanup failed")
	result, err := migrateStagedOfflineAuthorizedWithLiveVerification(
		t.Context(), stagedCoverageSource(source), target, 5*time.Second, 1,
		installProviderOfflineFixture, acceptStagedValidation,
		stagedCoverageLiveVerifier(target, nil),
		&stagedCoverageAuthority{},
		stagedMigrationOps{
			replace:  replaceStagedGeneration,
			activate: activateInstalledGeneration,
			discard: func(string, time.Duration) error {
				return canary
			},
		},
	)
	if !result.installed || !errors.Is(err, canary) ||
		dblayer.CodeOf(err) != dblayer.CodeOutcomeUnknown {
		t.Fatalf("post-cutover cleanup result=%#v error=%v", result, err)
	}
}

func TestStagedCoverageTargetIdentityHelpers(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(blocked, "target.db")
	if exists, identity, err := captureStagedTargetIdentity(child); err == nil || exists || identity != nil {
		t.Fatalf("uninspectable target identity = %t, %#v, %v", exists, identity, err)
	}
	target := filepath.Join(root, "target.db")
	if err := os.WriteFile(target+"-wal", []byte("wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if exists, identity, err := captureStagedTargetIdentity(target); err == nil || exists || identity != nil {
		t.Fatalf("sidecar target identity = %t, %#v, %v", exists, identity, err)
	}
	if err := recheckStagedTargetIdentity(target, false, nil); err == nil {
		t.Fatal("target sidecar passed final recheck")
	}
}

func stagedCoverageFilesystem(
	stage string,
	prepare func(string) error,
	retain func(context.Context, string) (*retainedStagedGeneration, error),
) stagedMigrationFilesystemOps {
	return stagedMigrationFilesystemOps{
		exists:  func(string) (bool, error) { return false, nil },
		lstat:   os.Lstat,
		unused:  func(string) (string, error) { return stage, nil },
		prepare: prepare,
		retain:  retain,
		backup: func(context.Context, string, string, time.Duration) error {
			return nil
		},
		validate: func(context.Context, string, time.Duration, int) error { return nil },
		same:     func(string, os.FileInfo) (bool, error) { return true, nil },
		noSidecars: func(string) error {
			return nil
		},
	}
}

func TestStagedCoverageIdentityManagedInnerStageFailures(t *testing.T) {
	canary := errors.New("identity-managed stage failure")
	t.Run("prepare", func(t *testing.T) {
		root := t.TempDir()
		stage := filepath.Join(root, "stage.db")
		filesystem := stagedCoverageFilesystem(
			stage,
			func(string) error { return canary },
			func(context.Context, string) (*retainedStagedGeneration, error) {
				t.Fatal("failed preparation attempted retention")
				return nil, nil
			},
		)
		err := migrateStagedOfflineWithFilesystem(
			t.Context(), filepath.Join(root, "source.db"), filepath.Join(root, "target.db"),
			time.Second, 1,
			func(context.Context, string) error { return nil }, acceptStagedValidation,
			stagedMigrationOps{
				replace: func(string, string) (bool, error) { return true, nil },
				activate: func(context.Context, string, time.Duration, int) error {
					return nil
				},
			},
			filesystem,
		)
		if !errors.Is(err, canary) || !strings.Contains(err.Error(), "prepare empty") {
			t.Fatalf("identity-managed prepare error = %v", err)
		}
	})

	t.Run("retain", func(t *testing.T) {
		root := t.TempDir()
		stage := filepath.Join(root, "stage.db")
		filesystem := stagedCoverageFilesystem(
			stage,
			func(path string) error { return os.WriteFile(path, []byte("stage"), 0o600) },
			func(context.Context, string) (*retainedStagedGeneration, error) {
				return nil, canary
			},
		)
		err := migrateStagedOfflineWithFilesystem(
			t.Context(), filepath.Join(root, "source.db"), filepath.Join(root, "target.db"),
			time.Second, 1,
			func(context.Context, string) error { return nil }, acceptStagedValidation,
			stagedMigrationOps{
				replace: func(string, string) (bool, error) { return true, nil },
				activate: func(context.Context, string, time.Duration, int) error {
					return nil
				},
			},
			filesystem,
		)
		if !errors.Is(err, canary) || !strings.Contains(err.Error(), "retain SQLite") {
			t.Fatalf("identity-managed retain error = %v", err)
		}
	})
}

func TestStagedCoverageIdentityManagedInnerStageChecks(t *testing.T) {
	newFixture := func(t *testing.T) (
		string,
		string,
		stagedMigrationFilesystemOps,
		**retainedStagedGeneration,
	) {
		t.Helper()
		root := t.TempDir()
		stage := filepath.Join(root, "stage.db")
		target := filepath.Join(root, "target.db")
		var retained *retainedStagedGeneration
		filesystem := stagedCoverageFilesystem(
			stage,
			func(path string) error { return os.WriteFile(path, []byte("stage"), 0o600) },
			func(ctx context.Context, path string) (*retainedStagedGeneration, error) {
				var err error
				retained, err = retainStagedGeneration(ctx, path)
				return retained, err
			},
		)
		return filepath.Join(root, "source.db"), target, filesystem, &retained
	}

	t.Run("closed before cutover", func(t *testing.T) {
		source, target, filesystem, retained := newFixture(t)
		calls := 0
		filesystem.same = func(string, os.FileInfo) (bool, error) {
			calls++
			if calls == 2 {
				if err := (*retained).Close(); err != nil {
					return false, err
				}
			}
			return true, nil
		}
		err := migrateStagedOfflineWithFilesystem(
			t.Context(), source, target, time.Second, 1,
			func(context.Context, string) error { return nil }, acceptStagedValidation,
			stagedMigrationOps{
				replace: func(string, string) (bool, error) { return true, nil },
				activate: func(context.Context, string, time.Duration, int) error {
					return nil
				},
			},
			filesystem,
		)
		if err == nil || !strings.Contains(err.Error(), "before cutover") {
			t.Fatalf("closed retained stage before cutover = %v", err)
		}
	})

	t.Run("missing installed target", func(t *testing.T) {
		source, target, filesystem, _ := newFixture(t)
		err := migrateStagedOfflineWithFilesystem(
			t.Context(), source, target, time.Second, 1,
			func(context.Context, string) error { return nil }, acceptStagedValidation,
			stagedMigrationOps{
				replace: func(string, string) (bool, error) { return true, nil },
				activate: func(context.Context, string, time.Duration, int) error {
					return nil
				},
			},
			filesystem,
		)
		if dblayer.CodeOf(err) != dblayer.CodeOutcomeUnknown ||
			!strings.Contains(err.Error(), "retained stage") {
			t.Fatalf("missing installed retained target = %v", err)
		}
	})
}

func TestStagedCoverageRetainedCleanupInputBoundaries(t *testing.T) {
	if err := cleanupRetainedStagedGeneration(
		&retainedStagedGeneration{}, "stage.db", nil, time.Second, true,
	); err == nil || !strings.Contains(err.Error(), "sidecar validation") {
		t.Fatalf("nil sidecar validation = %v", err)
	}
	canary := errors.New("sidecar validation failed")
	if err := cleanupRetainedStagedGeneration(
		&retainedStagedGeneration{}, "stage.db",
		func(string) error { return canary }, time.Second, true,
	); !errors.Is(err, canary) || !strings.Contains(err.Error(), "diagnostic") {
		t.Fatalf("failed sidecar validation = %v", err)
	}
}

func TestStagedCoverageImmutableSourceRetentionWrappers(t *testing.T) {
	t.Run("wrapper closes detached main", func(t *testing.T) {
		root := t.TempDir()
		sourcePath := filepath.Join(root, "source.db")
		stage := filepath.Join(root, "stage.db")
		writeImmutableSourceTestFile(t, sourcePath, []byte("source"), time.Now())
		ops := defaultImmutableGenerationCopyOps()
		ops.retain = retainStagedGeneration
		exists, err := copyImmutableGenerationToStageWithOps(
			t.Context(), stagedCoverageSource(sourcePath), stage, ops,
		)
		if err != nil || !exists {
			t.Fatalf("retained wrapper exists=%t error=%v", exists, err)
		}
		if _, err := os.Lstat(stage); err != nil {
			t.Fatalf("closed detached main path = %v", err)
		}
	})

	t.Run("detach failure propagates", func(t *testing.T) {
		root := t.TempDir()
		sourcePath := filepath.Join(root, "source.db")
		stage := filepath.Join(root, "stage.db")
		writeImmutableSourceTestFile(t, sourcePath, []byte("source"), time.Now())
		ops := defaultImmutableGenerationCopyOps()
		ops.retain = func(context.Context, string) (*retainedStagedGeneration, error) {
			return &retainedStagedGeneration{}, nil
		}
		if _, _, err := copyImmutableGenerationToRetainedStageWithOps(
			t.Context(), stagedCoverageSource(sourcePath), stage, ops,
		); err == nil || !strings.Contains(err.Error(), "recheck copied") {
			t.Fatalf("invalid detached retention = %v", err)
		}
	})
}

func TestStagedCoverageImmutableStageRetainFailure(t *testing.T) {
	rootPath := t.TempDir()
	sourcePath := filepath.Join(rootPath, "source.db")
	writeImmutableSourceTestFile(t, sourcePath, []byte("source"), time.Now())
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer sourceFile.Close()
	sourceInfo, err := sourceFile.Stat()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	canary := errors.New("retain copied member failed")
	ops := defaultImmutableStageWriteOps()
	ops.retain = func(context.Context, string) (*retainedStagedGeneration, error) {
		return nil, canary
	}
	stage := filepath.Join(rootPath, "stage.db")
	member, err := writeImmutableStageMemberWithOps(
		t.Context(), t.Context(), root, filepath.Base(stage), stage,
		&immutableGenerationOpenMember{
			path: sourcePath, info: sourceInfo, file: sourceFile,
		},
		ops,
	)
	if !member.retentionAttempted || !errors.Is(err, canary) {
		t.Fatalf("retained member=%#v error=%v", member, err)
	}
}

func stagedCoverageRetainedFile(
	t *testing.T,
	name string,
) (string, *retainedStagedGeneration) {
	t.Helper()
	root := t.TempDir()
	if err := EnsurePrivateDirectory(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	retained, err := retainStagedGeneration(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = retained.Close() })
	return path, retained
}

func TestStagedCoverageDetachImmutableStageRetentions(t *testing.T) {
	t.Run("invalid retained member", func(t *testing.T) {
		created := []immutableStageMember{{
			index: 0, path: filepath.Join(t.TempDir(), "missing.db"),
			retained: &retainedStagedGeneration{},
		}}
		if main, err := detachImmutableStageRetentions(
			t.Context(), created, true, true,
		); main != nil || err == nil || !strings.Contains(err.Error(), "recheck copied") {
			t.Fatalf("invalid retained member = %#v, %v", main, err)
		}
	})

	t.Run("multiple mains", func(t *testing.T) {
		firstPath, first := stagedCoverageRetainedFile(t, "first.db")
		secondPath, second := stagedCoverageRetainedFile(t, "second.db")
		created := []immutableStageMember{
			{index: 0, path: firstPath, retained: first},
			{index: 0, path: secondPath, retained: second},
		}
		if main, err := detachImmutableStageRetentions(
			t.Context(), created, true, true,
		); main != nil || err == nil || !strings.Contains(err.Error(), "multiple mains") {
			t.Fatalf("multiple retained mains = %#v, %v", main, err)
		}
	})

	t.Run("missing source with main", func(t *testing.T) {
		path, retained := stagedCoverageRetainedFile(t, "main.db")
		created := []immutableStageMember{{index: 0, path: path, retained: retained}}
		if main, err := detachImmutableStageRetentions(
			t.Context(), created, false, true,
		); main != nil || err == nil || !strings.Contains(err.Error(), "missing immutable") {
			t.Fatalf("missing source retained main = %#v, %v", main, err)
		}
	})

	t.Run("required main absent", func(t *testing.T) {
		created := []immutableStageMember{{index: 1}}
		if main, err := detachImmutableStageRetentions(
			t.Context(), created, true, true,
		); main != nil || err == nil || !strings.Contains(err.Error(), "main retention") {
			t.Fatalf("missing retained main = %#v, %v", main, err)
		}
	})

	t.Run("detaches main and skips nil", func(t *testing.T) {
		path, retained := stagedCoverageRetainedFile(t, "main.db")
		created := []immutableStageMember{
			{index: 1},
			{index: 0, path: path, retained: retained},
		}
		main, err := detachImmutableStageRetentions(t.Context(), created, true, true)
		if err != nil || main != retained || created[1].retained != nil {
			t.Fatalf("detached retained main = %#v, %v; created=%#v", main, err, created)
		}
	})
}

func TestStagedCoverageCleanupMissingRetention(t *testing.T) {
	member := immutableStageMember{
		path:               filepath.Join(t.TempDir(), "stage.db"),
		retentionAttempted: true,
	}
	if err := cleanupImmutableStage(
		[]immutableStageMember{member}, t.TempDir(), defaultImmutableGenerationCopyOps(),
	); err == nil || !strings.Contains(err.Error(), "retention was not established") {
		t.Fatalf("missing cleanup retention = %v", err)
	}
}

func TestStagedCoverageRemainingReachableBranches(t *testing.T) {
	t.Run("seal open failure", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Unix permission boundary")
		}
		stage := filepath.Join(t.TempDir(), "stage.db")
		if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(stage, 0o400); err != nil {
			t.Fatal(err)
		}
		if seal, err := sealValidatedReplacementStage(t.Context(), stage); seal != nil || err == nil {
			t.Fatalf("read-only replacement seal = %#v, %v", seal, err)
		}
	})

	t.Run("open fingerprint cancellation", func(t *testing.T) {
		stage := filepath.Join(t.TempDir(), "stage.db")
		if err := os.WriteFile(stage, []byte("stage"), 0o600); err != nil {
			t.Fatal(err)
		}
		identity, err := os.Lstat(stage)
		if err != nil {
			t.Fatal(err)
		}
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		file, _, err := openValidatedReplacementStage(canceled, stage, identity)
		if file != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled replacement open = %#v, %v", file, err)
		}
	})

	t.Run("working retention changes before inner staging", func(t *testing.T) {
		var working string
		var renameErr error
		ops := stagedMigrationOps{
			copySource: func(
				ctx context.Context,
				source ImmutableGenerationSource,
				stage string,
			) (bool, *retainedStagedGeneration, error) {
				working = stage
				return copyImmutableGenerationToRetainedStage(ctx, source, stage)
			},
		}
		authority := &stagedCoverageAuthority{
			onCheck: func(call int) {
				if call == 6 {
					renameErr = os.Rename(working, working+".moved")
				}
			},
		}
		result, err := stagedCoverageExistingMigration(t, authority, ops, nil)
		if renameErr != nil {
			t.Fatal(renameErr)
		}
		if result.installed || err == nil || !strings.Contains(err.Error(), "before staging") {
			t.Fatalf("working retention drift result=%#v error=%v", result, err)
		}
	})

	t.Run("retired flag cannot bypass namespace proof", func(t *testing.T) {
		var retained *retainedStagedGeneration
		ops := stagedMigrationOps{
			copySource: func(
				ctx context.Context,
				source ImmutableGenerationSource,
				stage string,
			) (bool, *retainedStagedGeneration, error) {
				var err error
				var exists bool
				exists, retained, err = copyImmutableGenerationToRetainedStage(ctx, source, stage)
				return exists, retained, err
			},
		}
		authority := &stagedCoverageAuthority{
			onCheck: func(call int) {
				if call == 7 && retained != nil {
					retained.Lock()
					retained.retired = true
					retained.Unlock()
				}
			},
		}
		result, err := stagedCoverageExistingMigration(t, authority, ops, nil)
		if result.installed || err == nil || !strings.Contains(err.Error(), "was not retired") {
			t.Fatalf("false retirement result=%#v error=%v", result, err)
		}
	})

	t.Run("generation namespace key failure", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows cannot remove the current directory")
		}
		original, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		working := t.TempDir()
		if err := os.Chdir(working); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Chdir(original); err != nil {
				t.Errorf("restore working directory: %v", err)
			}
		}()
		if err := os.Remove(working); err != nil {
			t.Fatal(err)
		}
		if !generationNamespacesOverlap("relative.db", filepath.Join(original, "target.db")) {
			t.Fatal("invalid generation namespace did not fail closed")
		}
		if _, err := migrateStagedOfflineAuthorizedWithLiveVerification(
			t.Context(),
			stagedCoverageSource(filepath.Join(original, "source.db")),
			"relative.db",
			time.Second,
			1,
			func(context.Context, string) error { return nil },
			acceptStagedValidation,
			func(context.Context, ValidatedReplacement) error { return nil },
			&stagedCoverageAuthority{},
			stagedMigrationOps{
				replace: func(string, string) (bool, error) { return false, nil },
				activate: func(context.Context, string, time.Duration, int) error {
					return nil
				},
			},
		); err == nil {
			t.Fatal("relative target unexpectedly resolved without a working directory")
		}
	})

	t.Run("staged name exhaustion", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "target.db")
		stage := filepath.Join(
			filepath.Dir(target),
			"."+filepath.Base(target)+".migration-stage-"+
				strings.Repeat("0", stagedReplacementRandomHexLength)+".db",
		)
		if err := os.WriteFile(stage, []byte("occupied"), 0o600); err != nil {
			t.Fatal(err)
		}
		originalReader := cryptorand.Reader
		cryptorand.Reader = bytes.NewReader(make(
			[]byte,
			(stagedReplacementRandomHexLength/2)*128,
		))
		defer func() { cryptorand.Reader = originalReader }()
		if generated, err := unusedStagedGenerationPath(target); generated != "" ||
			err == nil || !strings.Contains(err.Error(), "exhausted") {
			t.Fatalf("exhausted staged name = %q, %v", generated, err)
		}
	})
}
