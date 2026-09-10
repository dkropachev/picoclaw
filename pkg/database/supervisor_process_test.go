//nolint:govet // Independent failure-boundary assertions intentionally reuse narrow error names.
package database

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

const testCatalogFingerprint = "sha256:" +
	"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestEnsureSupervisorAttachesAndReplacesChangedFingerprint(t *testing.T) {
	home := t.TempDir()
	server, err := StartServer(t.Context(), ServerOptions{
		Home: home, CatalogFingerprint: testCatalogFingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := EnsureSupervisor(t.Context(), EnsureOptions{
		Home: home, CatalogFingerprint: testCatalogFingerprint,
	})
	if err != nil || client.Epoch() != server.Manifest().Epoch {
		t.Fatalf("attached client = %#v, %v", client, err)
	}

	changed := "sha256:" + strings.Repeat("a", 64)
	_, err = EnsureSupervisor(t.Context(), EnsureOptions{
		Home:               home,
		Executable:         filepath.Join(t.TempDir(), "missing"),
		CatalogFingerprint: changed,
		Timeout:            100 * time.Millisecond,
	})
	if CodeOf(err) != CodeUnavailable {
		t.Fatalf("changed fingerprint error = %v, want Unavailable", err)
	}
	select {
	case <-server.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("mismatched supervisor was not shut down")
	}
}

func TestEnsureSupervisorRejectsInvalidInputAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name    string
		ctx     context.Context
		options EnsureOptions
		code    ErrorCode
	}{
		{name: "fingerprint", ctx: t.Context(), options: EnsureOptions{Home: t.TempDir()}, code: CodeInvalid},
		{
			name: "home", ctx: t.Context(),
			options: EnsureOptions{Home: "bad\x00home", CatalogFingerprint: testCatalogFingerprint},
			code:    CodeInvalid,
		},
		{
			name: "canceled", ctx: canceledContext(),
			options: EnsureOptions{
				Home: t.TempDir(), Executable: filepath.Join(t.TempDir(), "missing"),
				CatalogFingerprint: testCatalogFingerprint, Timeout: time.Second,
			},
			code: CodeDeadline,
		},
		{
			name: "timeout", ctx: t.Context(),
			options: EnsureOptions{
				Home: t.TempDir(), Executable: filepath.Join(t.TempDir(), "missing"),
				CatalogFingerprint: testCatalogFingerprint, Timeout: 60 * time.Millisecond,
			},
			code: CodeUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, err := EnsureSupervisor(test.ctx, test.options)
			if client != nil || CodeOf(err) != test.code {
				t.Fatalf("EnsureSupervisor() = %#v, %v, want nil/%s", client, err, test.code)
			}
		})
	}
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestSupervisorBootstrapIsOneTimeHomeBoundAndStrict(t *testing.T) {
	home := t.TempDir()
	token, err := randomHex(tokenBytes)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := prepareSupervisorBootstrapArtifact(home, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareSupervisorBootstrap(home, token); err == nil {
		t.Fatal("duplicate supervisor bootstrap was created")
	}
	setSupervisorBootstrapTestAuthority(t, token, artifact)
	if !ConsumeSupervisorBootstrap(home) {
		t.Fatal("valid supervisor bootstrap was rejected")
	}
	setSupervisorBootstrapTestAuthority(t, token, artifact)
	if ConsumeSupervisorBootstrap(home) {
		t.Fatal("supervisor bootstrap replay was accepted")
	}

	otherToken, _ := randomHex(tokenBytes)
	otherArtifact, err := prepareSupervisorBootstrapArtifact(home, otherToken)
	if err != nil {
		t.Fatal(err)
	}
	setSupervisorBootstrapTestAuthority(t, otherToken, otherArtifact)
	if ConsumeSupervisorBootstrap(t.TempDir()) {
		t.Fatal("supervisor bootstrap crossed homes")
	}
	for _, invalid := range []string{"", "bad", strings.Repeat("A", tokenBytes*2)} {
		if _, err := prepareSupervisorBootstrap(home, invalid); CodeOf(err) != CodeInvalid {
			t.Errorf("prepareSupervisorBootstrap(%q) error = %v", invalid, err)
		}
		setSupervisorBootstrapTestAuthority(t, invalid, artifact)
		if ConsumeSupervisorBootstrap(home) {
			t.Errorf("ConsumeSupervisorBootstrap(%q) succeeded", invalid)
		}
	}
	_ = discardSupervisorBootstrap(otherArtifact)
}

func TestConsumeSupervisorBootstrapRequiresExactIdentityAuthority(t *testing.T) {
	for _, test := range []struct {
		name  string
		alter func(*testing.T)
	}{
		{
			name: "missing bootstrap identity",
			alter: func(t *testing.T) {
				t.Setenv(supervisorBootstrapIdentityEnvironment, "")
			},
		},
		{
			name: "changed bootstrap identity",
			alter: func(t *testing.T) {
				t.Setenv(supervisorBootstrapIdentityEnvironment, "changed")
			},
		},
		{
			name: "missing executable identity",
			alter: func(t *testing.T) {
				t.Setenv(supervisorExecutableIdentityEnvironment, "")
			},
		},
		{
			name: "changed executable identity",
			alter: func(t *testing.T) {
				t.Setenv(supervisorExecutableIdentityEnvironment, "changed")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			token, err := randomHex(tokenBytes)
			if err != nil {
				t.Fatal(err)
			}
			artifact, err := prepareSupervisorBootstrapArtifact(home, token)
			if err != nil {
				t.Fatal(err)
			}
			setSupervisorBootstrapTestAuthority(t, token, artifact)
			test.alter(t)
			if ConsumeSupervisorBootstrap(home) {
				t.Fatal("bootstrap with incomplete or changed identity authority was consumed")
			}
			if _, err := os.Lstat(artifact.path); err != nil {
				t.Fatalf("rejected bootstrap identity was not preserved: %v", err)
			}
			for _, name := range []string{
				supervisorBootstrapEnvironment,
				supervisorBootstrapIdentityEnvironment,
				supervisorExecutableIdentityEnvironment,
			} {
				if value := os.Getenv(name); value != "" {
					t.Fatalf("consumed authority environment %s remains %q", name, value)
				}
			}
		})
	}
}

func TestConsumeSupervisorBootstrapRejectsReplacementBeforeOpen(t *testing.T) {
	home := t.TempDir()
	token, err := randomHex(tokenBytes)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := prepareSupervisorBootstrapArtifact(home, token)
	if err != nil {
		t.Fatal(err)
	}
	originalPath := artifact.path + ".original"
	if err := os.Rename(artifact.path, originalPath); err != nil {
		t.Fatal(err)
	}
	replacement, err := createOwnerOnlyExclusiveFile(artifact.path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
	replacementIdentity, err := supervisorPathIdentity(artifact.path)
	if err != nil {
		t.Fatal(err)
	}
	setSupervisorBootstrapTestAuthority(t, token, artifact)
	if ConsumeSupervisorBootstrap(home) {
		t.Fatal("replacement bootstrap identity was consumed")
	}
	currentReplacement, replacementErr := supervisorPathIdentity(artifact.path)
	currentOriginal, originalErr := supervisorPathIdentity(originalPath)
	if replacementErr != nil || originalErr != nil || currentReplacement != replacementIdentity ||
		currentOriginal != artifact.fileIdentity {
		t.Fatalf(
			"replacement rejection changed identities: replacement=%v/%v original=%v/%v",
			currentReplacement, replacementErr, currentOriginal, originalErr,
		)
	}
}

func setSupervisorBootstrapTestAuthority(
	t *testing.T,
	token string,
	artifact supervisorBootstrapArtifact,
) {
	t.Helper()
	currentExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executableIdentity, err := supervisorPathIdentity(currentExecutable)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(supervisorBootstrapEnvironment, token)
	t.Setenv(supervisorBootstrapIdentityEnvironment, artifact.fileIdentity.String())
	t.Setenv(supervisorExecutableIdentityEnvironment, executableIdentity.String())
}

func TestConsumeSupervisorBootstrapOperationFaults(t *testing.T) {
	currentExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executableIdentity, err := supervisorPathIdentity(currentExecutable)
	if err != nil {
		t.Fatal(err)
	}
	authority := supervisorBootstrapAuthority{
		token:              strings.Repeat("a", tokenBytes*2),
		bootstrapIdentity:  "bootstrap-identity",
		executableIdentity: executableIdentity.String(),
	}
	baseOps := func() supervisorBootstrapConsumeOps {
		return supervisorBootstrapConsumeOps{
			executable: func() (string, error) { return currentExecutable, nil },
			pathIdentity: func(string) (supervisorFileIdentity, error) {
				return executableIdentity, nil
			},
			stateDirectory: func(string) (string, error) { return "state", nil },
			directoryIdentity: func(string) (supervisorFileIdentity, error) {
				return executableIdentity, nil
			},
			consume: func(string, supervisorFileIdentity, string, string) error { return nil },
		}
	}
	if consumeSupervisorBootstrapWith("home", authority, supervisorBootstrapConsumeOps{}) {
		t.Fatal("invalid bootstrap consumption operations succeeded")
	}

	canary := errors.New("bootstrap consumption stage failed")
	for _, test := range []struct {
		name   string
		mutate func(*supervisorBootstrapConsumeOps)
	}{
		{
			name: "executable lookup",
			mutate: func(ops *supervisorBootstrapConsumeOps) {
				ops.executable = func() (string, error) { return "", canary }
			},
		},
		{
			name: "executable identity",
			mutate: func(ops *supervisorBootstrapConsumeOps) {
				ops.pathIdentity = func(string) (supervisorFileIdentity, error) {
					return supervisorFileIdentity{}, canary
				}
			},
		},
		{
			name: "state directory",
			mutate: func(ops *supervisorBootstrapConsumeOps) {
				ops.stateDirectory = func(string) (string, error) { return "", canary }
			},
		},
		{
			name: "state identity",
			mutate: func(ops *supervisorBootstrapConsumeOps) {
				ops.directoryIdentity = func(string) (supervisorFileIdentity, error) {
					return supervisorFileIdentity{}, canary
				}
			},
		},
		{
			name: "consume",
			mutate: func(ops *supervisorBootstrapConsumeOps) {
				ops.consume = func(string, supervisorFileIdentity, string, string) error {
					return canary
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := baseOps()
			test.mutate(&ops)
			if consumeSupervisorBootstrapWith("home", authority, ops) {
				t.Fatal("faulted bootstrap consumption succeeded")
			}
		})
	}

	consumed := false
	ops := baseOps()
	ops.consume = func(
		stateDir string,
		stateIdentity supervisorFileIdentity,
		name string,
		bootstrapIdentity string,
	) error {
		consumed = stateDir == "state" && stateIdentity == executableIdentity &&
			name == ".bootstrap-"+authority.token && bootstrapIdentity == authority.bootstrapIdentity
		return nil
	}
	if !consumeSupervisorBootstrapWith("home", authority, ops) || !consumed {
		t.Fatal("valid injected bootstrap consumption failed")
	}
}

func TestSupervisorBootstrapFilenameIsExact(t *testing.T) {
	token := strings.Repeat("a", tokenBytes*2)
	if got, err := supervisorBootstrapToken(".bootstrap-" + token); err != nil || got != token {
		t.Fatalf("valid bootstrap filename = %q, %v", got, err)
	}
	for _, name := range []string{"", ".bootstrap-", "bootstrap-" + token, ".consumed-" + token} {
		if got, err := supervisorBootstrapToken(name); CodeOf(err) != CodeInvalid || got != "" {
			t.Errorf("bootstrap filename %q = %q, %v", name, got, err)
		}
	}
}

func TestPrepareSupervisorBootstrapFailureStages(t *testing.T) {
	canary := errors.New("bootstrap stage failed")
	token := strings.Repeat("a", tokenBytes*2)
	_, err := prepareSupervisorBootstrapWith(
		t.TempDir(), token, supervisorBootstrapOps{},
	)
	if CodeOf(err) != CodeInvalid {
		t.Fatalf("invalid bootstrap ops error = %v", err)
	}

	for _, stage := range []string{"create", "sync-file", "close-file", "sync-directory"} {
		t.Run(stage, func(t *testing.T) {
			home := t.TempDir()
			removed := false
			ops := supervisorBootstrapOps{
				create: createOwnerOnlyExclusiveFile,
				syncFile: func(file *os.File) error {
					if stage == "sync-file" {
						return canary
					}
					return file.Sync()
				},
				closeFile: func(file *os.File) error {
					closeErr := file.Close()
					if stage == "close-file" {
						return canary
					}
					return closeErr
				},
				identity:          supervisorOpenedIdentity,
				directoryIdentity: supervisorDirectoryIdentity,
				discard: func(artifact supervisorBootstrapArtifact) error {
					removed = true
					return discardSupervisorBootstrap(artifact)
				},
				syncDir: func(path string) error {
					if stage == "sync-directory" {
						return canary
					}
					return syncDirectory(path)
				},
			}
			if stage == "create" {
				ops.create = func(string, os.FileMode) (*os.File, error) { return nil, canary }
			}
			path, err := prepareSupervisorBootstrapWith(home, token, ops)
			if path != "" || !errors.Is(err, canary) {
				t.Fatalf("%s failure = %q, %v", stage, path, err)
			}
			if stage != "create" && !removed {
				t.Fatalf("%s failure did not remove bootstrap", stage)
			}
		})
	}

	homeFile := filepath.Join(t.TempDir(), "home-file")
	if err := os.WriteFile(homeFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	validOps := supervisorBootstrapOps{
		create:            createOwnerOnlyExclusiveFile,
		syncFile:          func(file *os.File) error { return file.Sync() },
		closeFile:         func(file *os.File) error { return file.Close() },
		identity:          supervisorOpenedIdentity,
		directoryIdentity: supervisorDirectoryIdentity,
		discard:           discardSupervisorBootstrap, syncDir: syncDirectory,
	}
	if _, err := prepareSupervisorBootstrapWith(homeFile, token, validOps); err == nil {
		t.Fatal("bootstrap accepted a regular-file home")
	}

	t.Run("nil created file", func(t *testing.T) {
		home := t.TempDir()
		ops := validOps
		ops.create = func(string, os.FileMode) (*os.File, error) { return nil, nil }
		if _, err := prepareSupervisorBootstrapWith(home, token, ops); CodeOf(err) != CodeInternal {
			t.Fatalf("nil created bootstrap error = %v", err)
		}
	})

	t.Run("state directory identity", func(t *testing.T) {
		ops := validOps
		ops.directoryIdentity = func(string) (supervisorFileIdentity, error) {
			return supervisorFileIdentity{}, canary
		}
		if _, err := prepareSupervisorBootstrapWith(t.TempDir(), token, ops); !errors.Is(err, canary) {
			t.Fatalf("bootstrap state-directory identity error = %v", err)
		}
	})

	t.Run("opened identity failure leaves unproven path", func(t *testing.T) {
		home := t.TempDir()
		ops := validOps
		ops.identity = func(*os.File) (fileidentity.Identity, error) {
			return fileidentity.Identity{}, canary
		}
		if _, err := prepareSupervisorBootstrapWith(home, token, ops); CodeOf(err) != CodeIntegrity {
			t.Fatalf("bootstrap identity failure = %v", err)
		}
		stateDir, err := StateDirectory(home)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(stateDir, ".bootstrap-"+token)); err != nil {
			t.Fatalf("identity failure deleted unproven path: %v", err)
		}
	})

	t.Run("boundary and discard faults join", func(t *testing.T) {
		home := t.TempDir()
		cleanupCanary := errors.New("bootstrap discard failed")
		ops := validOps
		ops.create = func(path string, mode os.FileMode) (*os.File, error) {
			file, err := createOwnerOnlyExclusiveFile(path, mode)
			if err != nil {
				return nil, err
			}
			if err := file.Chmod(0o644); err != nil {
				_ = file.Close()
				return nil, err
			}
			return file, nil
		}
		ops.discard = func(artifact supervisorBootstrapArtifact) error {
			if err := os.Chmod(artifact.path, 0o600); err != nil {
				return errors.Join(cleanupCanary, err)
			}
			return errors.Join(cleanupCanary, discardSupervisorBootstrap(artifact))
		}
		path, err := prepareSupervisorBootstrapWith(home, token, ops)
		if path != "" || CodeOf(err) != CodeIntegrity || !errors.Is(err, cleanupCanary) {
			t.Fatalf("bootstrap boundary/discard faults = %q, %v", path, err)
		}
	})

	t.Run("identity-bound discard", func(t *testing.T) {
		artifact, err := prepareSupervisorBootstrapArtifact(t.TempDir(), token)
		if err != nil {
			t.Fatal(err)
		}
		if err := discardSupervisorBootstrap(artifact); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(artifact.path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("discarded bootstrap remains: %v", err)
		}
		if err := discardSupervisorBootstrap(supervisorBootstrapArtifact{}); CodeOf(err) != CodeInvalid {
			t.Fatalf("invalid bootstrap discard error = %v", err)
		}
	})
}

func TestSupervisorBootstrapBoundaryRequiresStablePrivateIdentities(t *testing.T) {
	home := t.TempDir()
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	stateIdentity, err := supervisorDirectoryIdentity(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, ".bootstrap-"+strings.Repeat("b", tokenBytes*2))
	file, err := createOwnerOnlyExclusiveFile(path, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if validationErr := validateSupervisorBootstrapBoundary(
		stateDir, stateIdentity, path, file,
	); validationErr != nil {
		t.Fatal(validationErr)
	}
	validationErr := validateSupervisorBootstrapBoundary(
		stateDir, fileidentity.Identity{}, path, file,
	)
	if CodeOf(validationErr) != CodeIntegrity {
		t.Fatalf("nil expected state error = %v", validationErr)
	}
	otherState := t.TempDir()
	otherIdentity, err := supervisorDirectoryIdentity(otherState)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSupervisorBootstrapBoundary(
		stateDir, otherIdentity, path, file,
	); CodeOf(err) != CodeIntegrity {
		t.Fatalf("changed state error = %v", err)
	}
	if err := validateSupervisorBootstrapBoundary(
		stateDir, stateIdentity, path, nil,
	); CodeOf(err) != CodeIntegrity {
		t.Fatalf("nil bootstrap file error = %v", err)
	}
}

func TestSupervisorProcessValidationEnvironmentAndSecureLog(t *testing.T) {
	home := t.TempDir()
	if matched, err := currentTestExecutable(filepath.Join(t.TempDir(), "missing")); matched || err == nil {
		t.Fatalf("missing executable test identity = %t, %v", matched, err)
	}
	if err := startSupervisorProcess(EnsureOptions{
		CatalogFingerprint: testCatalogFingerprint,
	}, home); CodeOf(err) != CodeInvalid {
		t.Fatalf("current test executable error = %v", err)
	}
	if err := startSupervisorProcess(EnsureOptions{
		Executable: filepath.Join(t.TempDir(), "missing"), CatalogFingerprint: testCatalogFingerprint,
	}, home); CodeOf(err) != CodeUnavailable {
		t.Fatalf("missing executable error = %v", err)
	}
	if err := startSupervisorProcess(EnsureOptions{
		Executable: t.TempDir(), CatalogFingerprint: testCatalogFingerprint,
	}, home); CodeOf(err) != CodeIntegrity {
		t.Fatalf("directory executable error = %v", err)
	}
	pathErr := startSupervisorProcess(
		EnsureOptions{
			Executable: "missing-picoclaw-supervisor-binary", CatalogFingerprint: testCatalogFingerprint,
		}, home,
	)
	if CodeOf(pathErr) != CodeUnavailable {
		t.Fatalf("PATH executable error = %v", pathErr)
	}
	executable := filepath.Join(t.TempDir(), "supervisor-exit")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	nonExecutable := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(nonExecutable, []byte("exit 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		nonExecutableHome := t.TempDir()
		if err := startSupervisorProcess(EnsureOptions{
			Executable: nonExecutable, CatalogFingerprint: testCatalogFingerprint,
		}, nonExecutableHome); CodeOf(err) != CodeIntegrity {
			t.Fatalf("non-executable start error = %v", err)
		}
		assertSupervisorLaunchHomeEmpty(t, nonExecutableHome)
	}
	symlinkDirectory := t.TempDir()
	symlink := filepath.Join(symlinkDirectory, "test-binary-link")
	if err := os.Symlink(os.Args[0], symlink); err != nil {
		t.Logf("symlink executable test unavailable: %v", err)
	} else if err := startSupervisorProcess(EnsureOptions{
		Executable: symlink, CatalogFingerprint: testCatalogFingerprint,
	}, home); CodeOf(err) != CodeIntegrity {
		t.Fatalf("symlinked test executable error = %v", err)
	}
	hardlink := filepath.Join(t.TempDir(), "test-binary-hardlink")
	if err := os.Link(os.Args[0], hardlink); err != nil {
		t.Logf("hard-link executable test unavailable: %v", err)
	} else if err := startSupervisorProcess(EnsureOptions{
		Executable: hardlink, CatalogFingerprint: testCatalogFingerprint,
	}, home); CodeOf(err) != CodeInvalid {
		t.Fatalf("hard-linked test executable error = %v", err)
	}
	unsafeStateHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(unsafeStateHome, StateDirectoryName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stateErr := startSupervisorProcess(EnsureOptions{
		Executable: executable, CatalogFingerprint: testCatalogFingerprint,
	}, unsafeStateHome)
	if CodeOf(stateErr) != CodeIntegrity {
		t.Fatalf("unsafe bootstrap state error = %v", stateErr)
	}

	for _, configPath := range []string{" relative.json", "relative.json", "bad\x00path"} {
		pathErr := startSupervisorProcess(EnsureOptions{
			Executable: executable, ConfigPath: configPath, CatalogFingerprint: testCatalogFingerprint,
		}, t.TempDir())
		if CodeOf(pathErr) != CodeInvalid {
			t.Errorf("config path %q error = %v", configPath, pathErr)
		}
	}
	unsafeLogHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(unsafeLogHome, "logs"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	logErr := startSupervisorProcess(EnsureOptions{
		Executable: executable, CatalogFingerprint: testCatalogFingerprint,
	}, unsafeLogHome)
	if CodeOf(logErr) != CodeIntegrity {
		t.Fatalf("unsafe process log error = %v", logErr)
	}
	if err := startSupervisorProcess(EnsureOptions{
		Executable: executable, ConfigPath: "/private/config.json",
		CatalogFingerprint: testCatalogFingerprint,
	}, home); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(home, "logs", "database-supervisor.log")
	info, err := os.Lstat(logPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("supervisor log = %#v, %v", info, err)
	}
	if chmodErr := os.Chmod(logPath, 0o644); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	if _, logErr := openSupervisorLog(home); CodeOf(logErr) != CodeIntegrity {
		t.Fatalf("unsafe existing log error = %v", logErr)
	}
	if chmodErr := os.Chmod(logPath, 0o600); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	logFile, err := openSupervisorLog(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := logFile.WriteString("append test\n"); err != nil {
		t.Fatal(err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}

	environment := replaceSupervisorEnvironment(
		[]string{"A=1", "PICOCLAW_CONFIG=old", "B=2", "PICOCLAW_CONFIG=older", "MALFORMED"},
		"PICOCLAW_CONFIG", "new",
	)
	if got := strings.Join(environment, ","); got != "A=1,B=2,MALFORMED,PICOCLAW_CONFIG=new" {
		t.Fatalf("replaced environment = %q", got)
	}
	caseInsensitive := sameSupervisorEnvironmentName("picoclaw_config", "PICOCLAW_CONFIG")
	if !sameSupervisorEnvironmentName("PICOCLAW_CONFIG", "PICOCLAW_CONFIG") ||
		caseInsensitive != (runtime.GOOS == "windows") {
		t.Fatal("non-Windows supervisor environment comparison changed")
	}
}

func TestSupervisorRejectsUnsafeExecutableModeAndAncestorBeforeMutation(t *testing.T) {
	if runtime.GOOS != "windows" {
		for _, mode := range []os.FileMode{0o722, 0o707} {
			t.Run(fmt.Sprintf("mode-%#o", mode), func(t *testing.T) {
				executable := filepath.Join(t.TempDir(), "unsafe-supervisor")
				if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(executable, mode); err != nil {
					t.Fatal(err)
				}
				home := t.TempDir()
				if err := startSupervisorProcess(EnsureOptions{
					Executable: executable, CatalogFingerprint: testCatalogFingerprint,
				}, home); CodeOf(err) != CodeIntegrity {
					t.Fatalf("unsafe executable mode %#o error = %v", mode, err)
				}
				assertSupervisorLaunchHomeEmpty(t, home)
			})
		}
	}

	realDirectory := t.TempDir()
	executable := filepath.Join(realDirectory, "supervisor")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "executable-directory-alias")
	if err := os.Symlink(realDirectory, alias); err != nil {
		t.Skipf("symlinked executable ancestor unavailable: %v", err)
	}
	home := t.TempDir()
	if err := startSupervisorProcess(EnsureOptions{
		Executable:         filepath.Join(alias, filepath.Base(executable)),
		CatalogFingerprint: testCatalogFingerprint,
	}, home); CodeOf(err) != CodeIntegrity {
		t.Fatalf("symlinked executable ancestor error = %v", err)
	}
	assertSupervisorLaunchHomeEmpty(t, home)
}

func TestSupervisorStartRejectsExecutableSwapBeforeLaunch(t *testing.T) {
	home := t.TempDir()
	executable := filepath.Join(t.TempDir(), "supervisor")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	original := executable + ".original"
	var artifact supervisorBootstrapArtifact
	launches := 0
	attempt, err := startSupervisorProcessUntilWith(EnsureOptions{
		Executable: executable, CatalogFingerprint: testCatalogFingerprint,
	}, home, time.Now().Add(time.Second), supervisorStartOps{
		prepareBootstrap: func(home, token string) (supervisorBootstrapArtifact, error) {
			var prepareErr error
			artifact, prepareErr = prepareSupervisorBootstrapArtifact(home, token)
			return artifact, prepareErr
		},
		discardBootstrap: discardSupervisorBootstrap,
		configure: func(*exec.Cmd, string) error {
			if renameErr := os.Rename(executable, original); renameErr != nil {
				return renameErr
			}
			return os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700)
		},
		validateExecutable: validateSupervisorExecutableIdentity,
		launch: func(*exec.Cmd) (*supervisorProcessLaunch, error) {
			launches++
			return nil, errors.New("launch must not be reached")
		},
	})
	if attempt != nil || CodeOf(err) != CodeIntegrity || launches != 0 {
		t.Fatalf("swapped executable start = %#v, %v, launches=%d", attempt, err, launches)
	}
	if artifact.path == "" {
		t.Fatal("swap seam did not prepare a bootstrap artifact")
	}
	if _, statErr := os.Lstat(artifact.path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("swapped executable left bootstrap candidate: %v", statErr)
	}
	if _, statErr := os.Lstat(filepath.Join(home, "logs")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("swapped executable created a supervisor log: %v", statErr)
	}
}

func TestSupervisorStartOperationFaults(t *testing.T) {
	canary := errors.New("supervisor start stage failed")
	cleanupCanary := errors.New("supervisor start cleanup failed")
	newFixture := func(t *testing.T) (EnsureOptions, string, time.Time) {
		t.Helper()
		executable := filepath.Join(t.TempDir(), "supervisor")
		if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(executable, 0o700); err != nil {
			t.Fatal(err)
		}
		return EnsureOptions{
			Executable: executable, CatalogFingerprint: testCatalogFingerprint,
		}, t.TempDir(), time.Now().Add(time.Second)
	}
	newOps := func() supervisorStartOps {
		return supervisorStartOps{
			prepareBootstrap: func(string, string) (supervisorBootstrapArtifact, error) {
				return supervisorBootstrapArtifact{}, nil
			},
			discardBootstrap: func(supervisorBootstrapArtifact) error { return nil },
			configure:        func(*exec.Cmd, string) error { return nil },
			validateExecutable: func(string, supervisorFileIdentity) error {
				return nil
			},
			launch: func(*exec.Cmd) (*supervisorProcessLaunch, error) {
				done := make(chan struct{})
				close(done)
				return &supervisorProcessLaunch{
					pid: 1, owner: &testSupervisorProcessOwner{}, done: done,
				}, nil
			},
		}
	}

	t.Run("invalid operations", func(t *testing.T) {
		options, home, deadline := newFixture(t)
		attempt, err := startSupervisorProcessUntilWith(
			options, home, deadline, supervisorStartOps{},
		)
		if attempt != nil || CodeOf(err) != CodeInvalid {
			t.Fatalf("invalid start operations = %#v, %v", attempt, err)
		}
	})

	t.Run("bootstrap preparation", func(t *testing.T) {
		options, home, deadline := newFixture(t)
		ops := newOps()
		ops.prepareBootstrap = func(string, string) (supervisorBootstrapArtifact, error) {
			return supervisorBootstrapArtifact{}, canary
		}
		attempt, err := startSupervisorProcessUntilWith(options, home, deadline, ops)
		if attempt != nil || !errors.Is(err, canary) {
			t.Fatalf("bootstrap preparation fault = %#v, %v", attempt, err)
		}
	})

	t.Run("configuration joins discard", func(t *testing.T) {
		options, home, deadline := newFixture(t)
		ops := newOps()
		ops.configure = func(*exec.Cmd, string) error { return canary }
		ops.discardBootstrap = func(supervisorBootstrapArtifact) error { return cleanupCanary }
		attempt, err := startSupervisorProcessUntilWith(options, home, deadline, ops)
		if attempt != nil || !errors.Is(err, canary) || !errors.Is(err, cleanupCanary) {
			t.Fatalf("configuration/discard faults = %#v, %v", attempt, err)
		}
	})

	t.Run("validation joins log close", func(t *testing.T) {
		options, home, deadline := newFixture(t)
		closedLog, err := os.CreateTemp(t.TempDir(), "closed-log")
		if err != nil {
			t.Fatal(err)
		}
		if err := closedLog.Close(); err != nil {
			t.Fatal(err)
		}
		ops := newOps()
		ops.configure = func(command *exec.Cmd, _ string) error {
			command.Stdout = closedLog
			return nil
		}
		ops.validateExecutable = func(string, supervisorFileIdentity) error { return canary }
		attempt, err := startSupervisorProcessUntilWith(options, home, deadline, ops)
		if attempt != nil || !errors.Is(err, canary) || !errors.Is(err, os.ErrClosed) {
			t.Fatalf("validation/log-close faults = %#v, %v", attempt, err)
		}
	})

	t.Run("launch error", func(t *testing.T) {
		options, home, deadline := newFixture(t)
		ops := newOps()
		ops.launch = func(*exec.Cmd) (*supervisorProcessLaunch, error) { return nil, canary }
		attempt, err := startSupervisorProcessUntilWith(options, home, deadline, ops)
		if attempt != nil || !errors.Is(err, canary) {
			t.Fatalf("launch fault = %#v, %v", attempt, err)
		}
	})

	t.Run("nil launch", func(t *testing.T) {
		options, home, deadline := newFixture(t)
		ops := newOps()
		ops.launch = func(*exec.Cmd) (*supervisorProcessLaunch, error) { return nil, nil }
		attempt, err := startSupervisorProcessUntilWith(options, home, deadline, ops)
		if attempt != nil || CodeOf(err) != CodeIntegrity {
			t.Fatalf("nil launch = %#v, %v", attempt, err)
		}
	})

	t.Run("invalid owned launch cleans owner", func(t *testing.T) {
		options, home, deadline := newFixture(t)
		owner := &testSupervisorProcessOwner{killErr: canary, closeErr: cleanupCanary}
		ops := newOps()
		ops.launch = func(*exec.Cmd) (*supervisorProcessLaunch, error) {
			return &supervisorProcessLaunch{owner: owner, done: make(chan struct{})}, nil
		}
		attempt, err := startSupervisorProcessUntilWith(options, home, deadline, ops)
		if attempt != nil || CodeOf(err) != CodeIntegrity ||
			!errors.Is(err, canary) || !errors.Is(err, cleanupCanary) ||
			owner.killed.Load() != 1 || owner.closed.Load() != 1 {
			t.Fatalf(
				"invalid owned launch = %#v, %v killed=%d closed=%d",
				attempt, err, owner.killed.Load(), owner.closed.Load(),
			)
		}
	})

	t.Run("post-launch log close terminates attempt", func(t *testing.T) {
		options, home, deadline := newFixture(t)
		closedLog, err := os.CreateTemp(t.TempDir(), "closed-log")
		if err != nil {
			t.Fatal(err)
		}
		if err := closedLog.Close(); err != nil {
			t.Fatal(err)
		}
		owner := &testSupervisorProcessOwner{}
		done := make(chan struct{})
		close(done)
		ops := newOps()
		ops.configure = func(command *exec.Cmd, _ string) error {
			command.Stdout = closedLog
			return nil
		}
		ops.launch = func(*exec.Cmd) (*supervisorProcessLaunch, error) {
			return &supervisorProcessLaunch{pid: 1, owner: owner, done: done}, nil
		}
		attempt, err := startSupervisorProcessUntilWith(options, home, deadline, ops)
		if attempt != nil || !errors.Is(err, os.ErrClosed) ||
			owner.killed.Load() != 1 || owner.closed.Load() != 1 {
			t.Fatalf(
				"post-launch close fault = %#v, %v killed=%d closed=%d",
				attempt, err, owner.killed.Load(), owner.closed.Load(),
			)
		}
	})

	t.Run("expired post-launch cleanup runs immediately", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			options, home, deadline := newFixture(t)
			owner := &testSupervisorProcessOwner{}
			done := make(chan struct{})
			close(done)
			discards := 0
			ops := newOps()
			ops.discardBootstrap = func(supervisorBootstrapArtifact) error {
				discards++
				return nil
			}
			ops.launch = func(*exec.Cmd) (*supervisorProcessLaunch, error) {
				timer := time.NewTimer(2 * time.Second)
				<-timer.C
				return &supervisorProcessLaunch{pid: 1, owner: owner, done: done}, nil
			}
			attempt, err := startSupervisorProcessUntilWith(options, home, deadline, ops)
			if err != nil || attempt == nil {
				t.Fatalf("expired post-launch cleanup start = %#v, %v", attempt, err)
			}
			synctest.Wait()
			if discards != 1 {
				t.Fatalf("expired post-launch cleanup discards = %d", discards)
			}
			if err := attempt.release(); err != nil {
				t.Fatal(err)
			}
		})
	})
}

func TestPrepareSupervisorLaunchAndAncestorFaults(t *testing.T) {
	canary := errors.New("supervisor launch resolution failed")
	executable := filepath.Join(t.TempDir(), "supervisor")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(executable, 0o700); err != nil {
		t.Fatal(err)
	}
	valid := EnsureOptions{
		Executable: executable, ConfigPath: filepath.Join(t.TempDir(), "config.json"),
		CatalogFingerprint: testCatalogFingerprint,
	}
	resolutionOps := func() supervisorLaunchResolutionOps {
		return supervisorLaunchResolutionOps{
			executable:  os.Executable,
			lstat:       os.Lstat,
			identity:    supervisorPathIdentity,
			currentTest: currentTestExecutable,
			trust:       validateSupervisorExecutableTrust,
			ancestors:   captureSupervisorExecutableAncestors,
			randomHex:   randomHex,
		}
	}

	for _, test := range []struct {
		name     string
		options  EnsureOptions
		deadline time.Time
		code     ErrorCode
	}{
		{name: "zero deadline", options: valid, code: CodeInvalid},
		{
			name: "expired deadline", options: valid,
			deadline: time.Now().Add(-time.Second), code: CodeDeadline,
		},
		{
			name: "padded executable",
			options: EnsureOptions{
				Executable: " " + executable, ConfigPath: valid.ConfigPath,
				CatalogFingerprint: testCatalogFingerprint,
			},
			deadline: time.Now().Add(time.Second), code: CodeInvalid,
		},
		{
			name: "NUL executable",
			options: EnsureOptions{
				Executable: executable + "\x00", ConfigPath: valid.ConfigPath,
				CatalogFingerprint: testCatalogFingerprint,
			},
			deadline: time.Now().Add(time.Second), code: CodeInvalid,
		},
		{
			name: "unclean absolute config",
			options: EnsureOptions{
				Executable: executable,
				ConfigPath: t.TempDir() + string(os.PathSeparator) + "dir" +
					string(os.PathSeparator) + ".." + string(os.PathSeparator) + "config.json",
				CatalogFingerprint: testCatalogFingerprint,
			},
			deadline: time.Now().Add(time.Second), code: CodeInvalid,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec, err := prepareSupervisorLaunch(test.options, t.TempDir(), test.deadline)
			if CodeOf(err) != test.code || spec.executable != "" {
				t.Fatalf("prepare launch fault = %#v, %v, want %s", spec, err, test.code)
			}
		})
	}

	t.Run("PATH lookup", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		spec, err := prepareSupervisorLaunch(EnsureOptions{
			Executable: "missing-supervisor", ConfigPath: valid.ConfigPath,
			CatalogFingerprint: testCatalogFingerprint,
		}, t.TempDir(), time.Now().Add(time.Second))
		if CodeOf(err) != CodeUnavailable || spec.executable != "" {
			t.Fatalf("missing PATH launch = %#v, %v", spec, err)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*supervisorLaunchResolutionOps, *EnsureOptions)
		code   ErrorCode
		canary bool
	}{
		{
			name: "implicit executable lookup",
			mutate: func(ops *supervisorLaunchResolutionOps, options *EnsureOptions) {
				options.Executable = ""
				ops.executable = func() (string, error) { return "", errors.New("executable lookup") }
			},
			code: CodeUnavailable,
		},
		{
			name: "leaf lstat",
			mutate: func(ops *supervisorLaunchResolutionOps, _ *EnsureOptions) {
				ops.lstat = func(string) (os.FileInfo, error) { return nil, errors.New("leaf lstat") }
			},
			code: CodeUnavailable,
		},
		{
			name: "leaf identity",
			mutate: func(ops *supervisorLaunchResolutionOps, _ *EnsureOptions) {
				ops.identity = func(string) (supervisorFileIdentity, error) {
					return supervisorFileIdentity{}, errors.New("leaf identity")
				}
			},
			code: CodeIntegrity,
		},
		{
			name: "current test identity",
			mutate: func(ops *supervisorLaunchResolutionOps, _ *EnsureOptions) {
				ops.currentTest = func(string) (bool, error) {
					return false, errors.New("current test identity")
				}
			},
			code: CodeIntegrity,
		},
		{
			name: "executable trust",
			mutate: func(ops *supervisorLaunchResolutionOps, _ *EnsureOptions) {
				ops.trust = func(string, os.FileInfo) error { return canary }
			},
			canary: true,
		},
		{
			name: "ancestor capture",
			mutate: func(ops *supervisorLaunchResolutionOps, _ *EnsureOptions) {
				ops.ancestors = func(string) ([]supervisorExecutableAncestor, error) {
					return nil, canary
				}
			},
			canary: true,
		},
		{
			name: "bootstrap randomness",
			mutate: func(ops *supervisorLaunchResolutionOps, _ *EnsureOptions) {
				ops.randomHex = func(int) (string, error) { return "", errors.New("randomness") }
			},
			code: CodeInternal,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := valid
			ops := resolutionOps()
			test.mutate(&ops, &options)
			spec, err := prepareSupervisorLaunchWith(
				options, t.TempDir(), time.Now().Add(time.Second), ops,
			)
			if spec.executable != "" || test.canary && !errors.Is(err, canary) ||
				!test.canary && CodeOf(err) != test.code {
				t.Fatalf("injected launch resolution fault = %#v, %v", spec, err)
			}
		})
	}

	t.Run("capture missing ancestor", func(t *testing.T) {
		ancestors, err := captureSupervisorExecutableAncestors(
			filepath.Join(t.TempDir(), "missing", "supervisor"),
		)
		if ancestors != nil || CodeOf(err) != CodeIntegrity {
			t.Fatalf("missing executable ancestors = %#v, %v", ancestors, err)
		}
	})

	t.Run("capture ancestor identity", func(t *testing.T) {
		parent := t.TempDir()
		ops := supervisorExecutableAncestorOps{
			lstat: os.Lstat, trust: validateSupervisorExecutableAncestorTrust,
			identity: func(string) (supervisorFileIdentity, error) {
				return supervisorFileIdentity{}, canary
			},
		}
		ancestors, err := captureSupervisorExecutableAncestorsWith(
			filepath.Join(parent, "supervisor"), ops,
		)
		if ancestors != nil || CodeOf(err) != CodeIntegrity {
			t.Fatalf("ancestor identity fault = %#v, %v", ancestors, err)
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("capture writable ancestor", func(t *testing.T) {
			ancestor := filepath.Join(t.TempDir(), "unsafe")
			if err := os.Mkdir(ancestor, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(ancestor, 0o777); err != nil {
				t.Fatal(err)
			}
			ancestors, err := captureSupervisorExecutableAncestors(
				filepath.Join(ancestor, "supervisor"),
			)
			if ancestors != nil || CodeOf(err) != CodeIntegrity {
				t.Fatalf("writable executable ancestors = %#v, %v", ancestors, err)
			}
		})
	}

	if err := validateSupervisorExecutableAncestors(nil); CodeOf(err) != CodeIntegrity {
		t.Fatalf("empty executable ancestors error = %v", err)
	}
	if err := validateSupervisorExecutableAncestors([]supervisorExecutableAncestor{{
		path: filepath.Join(t.TempDir(), "missing"),
	}}); CodeOf(err) != CodeIntegrity {
		t.Fatalf("missing captured ancestor error = %v", err)
	}

	t.Run("changed ancestor identity", func(t *testing.T) {
		root := t.TempDir()
		ancestor := filepath.Join(root, "bin")
		if err := os.Mkdir(ancestor, 0o700); err != nil {
			t.Fatal(err)
		}
		candidate := filepath.Join(ancestor, "supervisor")
		ancestors, err := captureSupervisorExecutableAncestors(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(ancestor, ancestor+".original"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(ancestor, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := validateSupervisorExecutableAncestors(ancestors); CodeOf(err) != CodeIntegrity {
			t.Fatalf("changed executable ancestor error = %v", err)
		}
	})
}

func assertSupervisorLaunchHomeEmpty(t *testing.T, home string) {
	t.Helper()
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected supervisor launch mutated home: entries=%v, error=%v", entries, err)
	}
}

func TestSupervisorRejectsCurrentTestExecutableBeforeLaunchSideEffects(t *testing.T) {
	originalFlags := flag.CommandLine
	flag.CommandLine = flag.NewFlagSet("replacement-without-test-flags", flag.ContinueOnError)
	t.Cleanup(func() { flag.CommandLine = originalFlags })
	if !testing.Testing() {
		t.Fatal("testing.Testing() = false inside a go test binary")
	}

	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	type executableCase struct {
		name       string
		executable string
	}
	cases := []executableCase{
		{name: "implicit"},
		{name: "explicit", executable: current},
	}
	hardlink := filepath.Join(t.TempDir(), "current-test-hardlink")
	if err := os.Link(current, hardlink); err != nil {
		t.Logf("hard-link current-test case unavailable: %v", err)
	} else {
		cases = append(cases, executableCase{name: "hardlink", executable: hardlink})
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			err := startSupervisorProcess(EnsureOptions{
				Executable: test.executable, CatalogFingerprint: testCatalogFingerprint,
			}, home)
			if CodeOf(err) != CodeInvalid {
				t.Fatalf("current test executable error = %v, want Invalid", err)
			}
			for _, path := range []string{
				filepath.Join(home, StateDirectoryName),
				filepath.Join(home, "logs"),
			} {
				if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("current test rejection created %q: %v", path, statErr)
				}
			}
			entries, readErr := os.ReadDir(home)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("current test rejection mutated home: entries=%v, error=%v", entries, readErr)
			}
		})
	}
}

func TestSupervisorProcessResolvesExecutableFromPath(t *testing.T) {
	directory := t.TempDir()
	name := "picoclaw-supervisor-path-helper"
	executable := filepath.Join(directory, name)
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := startSupervisorProcess(EnsureOptions{
		Executable: name, CatalogFingerprint: testCatalogFingerprint,
	}, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorSecureLogRejectsInvalidBoundaries(t *testing.T) {
	t.Run("logs file", func(t *testing.T) {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "logs"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := openSupervisorLog(home); CodeOf(err) != CodeIntegrity {
			t.Fatalf("logs file error = %v", err)
		}
		command := exec.Command("true")
		if err := configureSupervisorLog(command, home); CodeOf(err) != CodeIntegrity {
			t.Fatalf("configure invalid logs error = %v", err)
		}
	})
	t.Run("log directory", func(t *testing.T) {
		home := t.TempDir()
		logs := filepath.Join(home, "logs")
		if err := os.Mkdir(logs, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(logs, "database-supervisor.log"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := openSupervisorLog(home); CodeOf(err) != CodeIntegrity {
			t.Fatalf("log directory error = %v", err)
		}
	})
	t.Run("log symlink", func(t *testing.T) {
		home := t.TempDir()
		logs := filepath.Join(home, "logs")
		if err := os.Mkdir(logs, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(home, "target")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(logs, "database-supervisor.log")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := openSupervisorLog(home); CodeOf(err) != CodeIntegrity {
			t.Fatalf("log symlink error = %v", err)
		}
	})
	t.Run("log hardlink", func(t *testing.T) {
		home := t.TempDir()
		logs := filepath.Join(home, "logs")
		if err := os.Mkdir(logs, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(home, "target")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(target, filepath.Join(logs, "database-supervisor.log")); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		if _, err := openSupervisorLog(home); CodeOf(err) != CodeIntegrity {
			t.Fatalf("log hardlink error = %v", err)
		}
	})
	t.Run("public logs directory", func(t *testing.T) {
		home := t.TempDir()
		logs := filepath.Join(home, "logs")
		if err := os.Mkdir(logs, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := openSupervisorLog(home); CodeOf(err) != CodeIntegrity {
			t.Fatalf("public logs error = %v", err)
		}
	})
	t.Run("invalid home", func(t *testing.T) {
		if _, err := openSupervisorLog("bad\x00home"); CodeOf(err) != CodeInvalid {
			t.Fatalf("invalid log home error = %v", err)
		}
	})
	t.Run("directory identity", func(t *testing.T) {
		home := t.TempDir()
		first := filepath.Join(home, "first")
		second := filepath.Join(home, "second")
		if err := os.Mkdir(first, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(second, 0o700); err != nil {
			t.Fatal(err)
		}
		firstIdentity, err := supervisorDirectoryIdentity(first)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateSupervisorLogDirectory(second, firstIdentity); CodeOf(err) != CodeIntegrity {
			t.Fatalf("changed directory identity error = %v", err)
		}
		if err := validateSupervisorLogDirectory(first, firstIdentity); err != nil {
			t.Fatal(err)
		}
		if err := validateSupervisorLogDirectory(first, fileidentity.Identity{}); CodeOf(err) != CodeIntegrity {
			t.Fatalf("zero directory identity error = %v", err)
		}
	})
}

func TestSupervisorLogDescriptorsAlwaysAppend(t *testing.T) {
	home := t.TempDir()
	first, err := openSupervisorLog(home)
	if err != nil {
		t.Fatal(err)
	}
	second, err := openSupervisorLog(home)
	if err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	var writers sync.WaitGroup
	writers.Add(2)
	go func() {
		defer writers.Done()
		_, _ = first.WriteString("first\n")
	}()
	go func() {
		defer writers.Done()
		_, _ = second.WriteString("second\n")
	}()
	writers.Wait()
	if closeErr := first.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if closeErr := second.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	raw, err := os.ReadFile(filepath.Join(home, "logs", "database-supervisor.log"))
	if err != nil {
		t.Fatal(err)
	}
	contents := string(raw)
	if len(raw) != len("first\nsecond\n") || !strings.Contains(contents, "first\n") ||
		!strings.Contains(contents, "second\n") {
		t.Fatalf("concurrent append log = %q", contents)
	}
}

func TestSupervisorLogFailureStages(t *testing.T) {
	canary := errors.New("log stage failed")
	if file, err := openSupervisorLogWith(t.TempDir(), supervisorLogOps{}); file != nil ||
		CodeOf(err) != CodeInvalid {
		t.Fatalf("invalid log ops = %#v, %v", file, err)
	}

	t.Run("inspect logs", func(t *testing.T) {
		home := t.TempDir()
		ops := defaultSupervisorLogTestOps()
		ops.lstat = func(string) (os.FileInfo, error) { return nil, canary }
		if file, err := openSupervisorLogWith(home, ops); file != nil || !errors.Is(err, canary) {
			t.Fatalf("inspect logs = %#v, %v", file, err)
		}
	})
	t.Run("create logs", func(t *testing.T) {
		home := t.TempDir()
		ops := defaultSupervisorLogTestOps()
		ops.createDirectory = func(string) error { return canary }
		if file, err := openSupervisorLogWith(home, ops); file != nil || !errors.Is(err, canary) {
			t.Fatalf("create logs = %#v, %v", file, err)
		}
	})
	t.Run("log directory identity", func(t *testing.T) {
		home := t.TempDir()
		ops := defaultSupervisorLogTestOps()
		ops.directoryIdentity = func(string) (supervisorFileIdentity, error) {
			return supervisorFileIdentity{}, canary
		}
		if file, err := openSupervisorLogWith(home, ops); file != nil || CodeOf(err) != CodeIntegrity {
			t.Fatalf("log directory identity = %#v, %v", file, err)
		}
	})
	t.Run("create log", func(t *testing.T) {
		home := t.TempDir()
		if err := os.Mkdir(filepath.Join(home, "logs"), 0o700); err != nil {
			t.Fatal(err)
		}
		ops := defaultSupervisorLogTestOps()
		ops.createFile = func(string, os.FileMode) (*os.File, error) { return nil, canary }
		if file, err := openSupervisorLogWith(home, ops); file != nil || !errors.Is(err, canary) {
			t.Fatalf("create log = %#v, %v", file, err)
		}
	})
	t.Run("created log changed directory", func(t *testing.T) {
		home := t.TempDir()
		if err := os.Mkdir(filepath.Join(home, "logs"), 0o700); err != nil {
			t.Fatal(err)
		}
		ops := defaultSupervisorLogTestOps()
		ops.validateBoundary = func(string, fileidentity.Identity) error { return canary }
		if file, err := openSupervisorLogWith(home, ops); file != nil || !errors.Is(err, canary) {
			t.Fatalf("created log boundary = %#v, %v", file, err)
		}
	})
	t.Run("created log identity", func(t *testing.T) {
		home := t.TempDir()
		if err := os.Mkdir(filepath.Join(home, "logs"), 0o700); err != nil {
			t.Fatal(err)
		}
		ops := defaultSupervisorLogTestOps()
		ops.validateOpenedFile = func(string, *os.File, os.FileMode) error { return canary }
		if file, err := openSupervisorLogWith(home, ops); file != nil || !errors.Is(err, canary) {
			t.Fatalf("created log identity = %#v, %v", file, err)
		}
	})
	t.Run("create raced with existing log", func(t *testing.T) {
		home := t.TempDir()
		logs := filepath.Join(home, "logs")
		if err := os.Mkdir(logs, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(logs, "database-supervisor.log")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		ops := defaultSupervisorLogTestOps()
		firstLogStat := true
		ops.lstat = func(candidate string) (os.FileInfo, error) {
			if candidate == path && firstLogStat {
				firstLogStat = false
				return nil, os.ErrNotExist
			}
			return os.Lstat(candidate)
		}
		ops.createFile = func(string, os.FileMode) (*os.File, error) { return nil, os.ErrExist }
		file, err := openSupervisorLogWith(home, ops)
		if err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
	})
	t.Run("inspect existing log", func(t *testing.T) {
		home := t.TempDir()
		logs := filepath.Join(home, "logs")
		if err := os.Mkdir(logs, 0o700); err != nil {
			t.Fatal(err)
		}
		ops := defaultSupervisorLogTestOps()
		ops.lstat = func(candidate string) (os.FileInfo, error) {
			if strings.HasSuffix(candidate, "database-supervisor.log") {
				return nil, canary
			}
			return os.Lstat(candidate)
		}
		if file, err := openSupervisorLogWith(home, ops); file != nil || !errors.Is(err, canary) {
			t.Fatalf("inspect existing log = %#v, %v", file, err)
		}
	})
	t.Run("append existing log", func(t *testing.T) {
		home := t.TempDir()
		logs := filepath.Join(home, "logs")
		if err := os.Mkdir(logs, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(logs, "database-supervisor.log"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		ops := defaultSupervisorLogTestOps()
		ops.openAppend = func(string, os.FileMode) (*os.File, error) { return nil, canary }
		if file, err := openSupervisorLogWith(home, ops); file != nil || !errors.Is(err, canary) {
			t.Fatalf("append existing log = %#v, %v", file, err)
		}
	})
	t.Run("opened existing log identity", func(t *testing.T) {
		home := t.TempDir()
		logs := filepath.Join(home, "logs")
		if err := os.Mkdir(logs, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(logs, "database-supervisor.log")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		ops := defaultSupervisorLogTestOps()
		ops.validateOpenedFile = func(string, *os.File, os.FileMode) error { return canary }
		if file, err := openSupervisorLogWith(home, ops); file != nil || !errors.Is(err, canary) {
			t.Fatalf("existing log identity = %#v, %v", file, err)
		}
	})
	t.Run("appended log changed directory", func(t *testing.T) {
		home := t.TempDir()
		logs := filepath.Join(home, "logs")
		if err := os.Mkdir(logs, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(logs, "database-supervisor.log")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		ops := defaultSupervisorLogTestOps()
		ops.validateBoundary = func(string, fileidentity.Identity) error { return canary }
		if file, err := openSupervisorLogWith(home, ops); file != nil || !errors.Is(err, canary) {
			t.Fatalf("appended log boundary = %#v, %v", file, err)
		}
	})
}

func defaultSupervisorLogTestOps() supervisorLogOps {
	return supervisorLogOps{
		canonicalHome: CanonicalHome,
		lstat:         os.Lstat,
		createDirectory: func(path string) error {
			return createOwnerOnlyDirectory(path)
		},
		validateDirectory:  validateOwnerOnlyDirectory,
		createFile:         createOwnerOnlyExclusiveAppendFile,
		validateFile:       validateOwnerOnlyFile,
		validateOpenedFile: validateSupervisorOpenedFile,
		openAppend:         openOwnerOnlyAppendFile,
		validateBoundary:   validateSupervisorLogDirectory,
		directoryIdentity:  supervisorDirectoryIdentity,
	}
}

func TestSupervisorReadyReplacementAndMonitorHelpers(t *testing.T) {
	if err := shutdownBrokerForReplacement(t.Context(), nil, ""); CodeOf(err) != CodeUnavailable {
		t.Fatalf("nil replacement error = %v", err)
	}
	home := t.TempDir()
	server, err := StartServer(t.Context(), ServerOptions{
		Home: home, CatalogFingerprint: testCatalogFingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, status, err := connectReadyBrokerStatus(t.Context(), home)
	if err != nil || status.Epoch != server.Manifest().Epoch {
		t.Fatalf("ready status = %#v, %v", status, err)
	}
	if err := shutdownBrokerForReplacement(t.Context(), client, status.Epoch); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("replacement shutdown did not stop server")
	}
	if _, err := connectReadyBroker(t.Context(), home); err == nil {
		t.Fatal("stopped broker remained ready")
	}

	if err := MonitorSupervisor(nil, EnsureOptions{
		Home: "bad\x00home", CatalogFingerprint: testCatalogFingerprint,
	}); CodeOf(err) != CodeInvalid {
		t.Fatalf("monitor validation error = %v", err)
	}
	if err := monitorSupervisor(t.Context(), EnsureOptions{}, supervisorMonitorOps{}); CodeOf(err) != CodeInvalid {
		t.Fatalf("invalid monitor timing error = %v", err)
	}
	for _, code := range []ErrorCode{
		CodeInvalid, CodeUnauthorized, CodeIntegrity, CodeUnsupported,
	} {
		if !terminalSupervisorError(NewError(code, "terminal")) {
			t.Errorf("terminalSupervisorError(%s) = false", code)
		}
	}
	if terminalSupervisorError(NewError(CodeUnavailable, "retry")) ||
		terminalSupervisorError(errors.New("ordinary")) {
		t.Fatal("retryable supervisor error was terminal")
	}
}

func TestSupervisorReadyHelpersPropagateControlFailures(t *testing.T) {
	t.Run("ping cancellation", func(t *testing.T) {
		home := t.TempDir()
		server, err := StartServer(t.Context(), ServerOptions{Home: home})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = server.Close(t.Context()) }()
		if client, err := connectReadyBroker(canceledContext(), home); client != nil ||
			CodeOf(err) != CodeDeadline {
			t.Fatalf("canceled ready broker = %#v, %v", client, err)
		}
	})

	t.Run("invalid status", func(t *testing.T) {
		home := t.TempDir()
		server, err := StartServer(t.Context(), ServerOptions{
			Home: home,
			StatusProvider: func(context.Context) ([]StoreStatus, error) {
				return []StoreStatus{{ID: "invalid store", Readiness: StoreReady}}, nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = server.Close(t.Context()) }()
		client, status, err := connectReadyBrokerStatus(t.Context(), home)
		if client != nil || status.Epoch != "" || CodeOf(err) != CodeInvalid {
			t.Fatalf("invalid ready status = %#v, %#v, %v", client, status, err)
		}
	})

	t.Run("replacement shutdown cancellation", func(t *testing.T) {
		home := t.TempDir()
		server, err := StartServer(t.Context(), ServerOptions{Home: home})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = server.Close(t.Context()) }()
		client, err := Connect(home)
		if err != nil {
			t.Fatal(err)
		}
		if err := shutdownBrokerForReplacement(
			canceledContext(), client, server.Manifest().Epoch,
		); CodeOf(err) != CodeDeadline {
			t.Fatalf("canceled replacement shutdown = %v", err)
		}
		if _, err := client.Ping(t.Context()); err != nil {
			t.Fatalf("canceled replacement reached broker: %v", err)
		}
	})
}

func TestReplacementShutdownNeverTargetsNewerEpoch(t *testing.T) {
	home := t.TempDir()
	first, err := StartServer(t.Context(), ServerOptions{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	staleClient, firstStatus, err := connectReadyBrokerStatus(t.Context(), home)
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := first.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}
	second, err := StartServer(t.Context(), ServerOptions{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close(t.Context()) }()
	if shutdownErr := shutdownBrokerForReplacement(
		t.Context(), staleClient, firstStatus.Epoch,
	); shutdownErr != nil {
		t.Fatalf("stale epoch replacement observation = %v", shutdownErr)
	}
	secondClient, err := Connect(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secondClient.Ping(t.Context()); err != nil {
		t.Fatalf("newer epoch was shut down: %v", err)
	}
	if second.Manifest().Epoch == firstStatus.Epoch {
		t.Fatal("replacement server reused stale epoch")
	}
	if err := shutdownBrokerForReplacement(
		t.Context(), secondClient, firstStatus.Epoch,
	); CodeOf(err) != CodeConflict {
		t.Fatalf("changed client epoch error = %v", err)
	}
	if _, err := secondClient.Ping(t.Context()); err != nil {
		t.Fatalf("epoch mismatch affected newer server: %v", err)
	}
}

func TestReplacementEpochGoneRequiresAbsenceOrAuthenticatedSuccessor(t *testing.T) {
	t.Run("missing discovery proves absence", func(t *testing.T) {
		gone, err := replacementEpochGone(
			t.Context(), t.TempDir(), strings.Repeat("a", epochBytes*2),
		)
		if err != nil || !gone {
			t.Fatalf("missing replacement observation = %t, %v", gone, err)
		}
	})

	t.Run("malformed discovery is not absence", func(t *testing.T) {
		home := t.TempDir()
		stateDir, err := prepareStateDirectory(home)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(stateDir, manifestFileName)
		file, err := createOwnerOnlyExclusiveFile(path, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString("{}\n"); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		gone, err := replacementEpochGone(
			t.Context(), home, strings.Repeat("a", epochBytes*2),
		)
		if gone || CodeOf(err) != CodeIntegrity {
			t.Fatalf("malformed replacement observation = %t, %v", gone, err)
		}
	})

	t.Run("same epoch remains", func(t *testing.T) {
		home := t.TempDir()
		server, err := StartServer(t.Context(), ServerOptions{Home: home})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = server.Close(t.Context()) }()
		gone, err := replacementEpochGone(t.Context(), home, server.Manifest().Epoch)
		if err != nil || gone {
			t.Fatalf("same-epoch replacement observation = %t, %v", gone, err)
		}
	})

	t.Run("authenticated successor proves epoch change", func(t *testing.T) {
		home := t.TempDir()
		server, err := StartServer(t.Context(), ServerOptions{Home: home})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = server.Close(t.Context()) }()
		observedEpoch := strings.Repeat("a", epochBytes*2)
		if observedEpoch == server.Manifest().Epoch {
			observedEpoch = strings.Repeat("b", epochBytes*2)
		}
		gone, err := replacementEpochGone(t.Context(), home, observedEpoch)
		if err != nil || !gone {
			t.Fatalf("authenticated successor observation = %t, %v", gone, err)
		}
	})

	t.Run("forged successor does not prove epoch change", func(t *testing.T) {
		home := t.TempDir()
		server, err := StartServer(t.Context(), ServerOptions{Home: home})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = server.Close(t.Context()) }()
		original := server.Manifest()
		originalClient, err := ConnectWithManifest(home, original)
		if err != nil {
			t.Fatal(err)
		}
		stateDir, err := StateDirectory(home)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(stateDir, manifestFileName)); err != nil {
			t.Fatal(err)
		}
		forged := original
		forged.Epoch = strings.Repeat("a", epochBytes*2)
		if forged.Epoch == original.Epoch {
			forged.Epoch = strings.Repeat("b", epochBytes*2)
		}
		forged.Token = strings.Repeat("c", tokenBytes*2)
		if err := writeManifest(stateDir, forged); err != nil {
			t.Fatal(err)
		}
		gone, err := replacementEpochGone(t.Context(), home, original.Epoch)
		if gone || CodeOf(err) != CodeConflict {
			t.Fatalf("forged successor observation = %t, %v", gone, err)
		}
		if _, err := originalClient.Ping(t.Context()); err != nil {
			t.Fatalf("forged successor observation affected old broker: %v", err)
		}
	})
}

func TestReplacementShutdownFaults(t *testing.T) {
	observedEpoch := strings.Repeat("a", epochBytes*2)

	t.Run("invalid inherited manifest after disappearance", func(t *testing.T) {
		client := &Client{
			home: t.TempDir(), manifest: Manifest{Epoch: observedEpoch},
		}
		if err := shutdownBrokerForReplacement(t.Context(), client, observedEpoch); err != nil {
			t.Fatalf("disappeared invalid inherited manifest = %v", err)
		}
	})

	t.Run("invalid inherited and canonical manifests join", func(t *testing.T) {
		home := t.TempDir()
		stateDir, err := prepareStateDirectory(home)
		if err != nil {
			t.Fatal(err)
		}
		file, err := createOwnerOnlyExclusiveFile(
			filepath.Join(stateDir, manifestFileName), 0o600,
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString("{}\n"); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		client := &Client{home: home, manifest: Manifest{Epoch: observedEpoch}}
		if err := shutdownBrokerForReplacement(
			t.Context(), client, observedEpoch,
		); CodeOf(err) != CodeIntegrity {
			t.Fatalf("invalid inherited/canonical manifests error = %v", err)
		}
	})

	t.Run("terminal discovery before shutdown", func(t *testing.T) {
		home := t.TempDir()
		server, err := StartServer(t.Context(), ServerOptions{Home: home})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = server.Close(t.Context()) }()
		manifest := server.Manifest()
		client, err := ConnectWithManifest(home, manifest)
		if err != nil {
			t.Fatal(err)
		}
		stateDir, err := StateDirectory(home)
		if err != nil {
			t.Fatal(err)
		}
		manifestPath := filepath.Join(stateDir, manifestFileName)
		if err := os.Remove(manifestPath); err != nil {
			t.Fatal(err)
		}
		file, err := createOwnerOnlyExclusiveFile(manifestPath, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString("{}\n"); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := shutdownBrokerForReplacement(
			t.Context(), client, manifest.Epoch,
		); CodeOf(err) != CodeIntegrity {
			t.Fatalf("terminal pre-shutdown discovery error = %v", err)
		}
		if _, err := client.Ping(t.Context()); err != nil {
			t.Fatalf("terminal discovery reached old broker shutdown: %v", err)
		}
	})

	t.Run("canceled shutdown is not disappearance", func(t *testing.T) {
		home := t.TempDir()
		server, err := StartServer(t.Context(), ServerOptions{Home: home})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = server.Close(t.Context()) }()
		manifest := server.Manifest()
		client, err := ConnectWithManifest(home, manifest)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := shutdownBrokerForReplacement(ctx, client, manifest.Epoch); CodeOf(err) != CodeDeadline {
			t.Fatalf("canceled replacement shutdown error = %v", err)
		}
		if _, err := client.Ping(t.Context()); err != nil {
			t.Fatalf("canceled replacement stopped old broker: %v", err)
		}
	})

	newClient := func() *Client {
		return &Client{home: "prepared-home", manifest: Manifest{Epoch: observedEpoch}}
	}
	for _, test := range []struct {
		name     string
		sequence []struct {
			gone bool
			err  error
		}
		cancelAt  int
		wantError error
	}{
		{
			name: "shutdown error followed by disappearance",
			sequence: []struct {
				gone bool
				err  error
			}{{}, {gone: true}},
		},
		{
			name: "terminal observation after shutdown error",
			sequence: []struct {
				gone bool
				err  error
			}{{}, {err: NewError(CodeIntegrity, "terminal observation")}},
			wantError: NewError(CodeIntegrity, "terminal observation"),
		},
		{
			name: "terminal observation in retry loop",
			sequence: []struct {
				gone bool
				err  error
			}{{}, {}, {err: NewError(CodeIntegrity, "terminal retry observation")}},
			wantError: NewError(CodeIntegrity, "terminal retry observation"),
		},
		{
			name: "cancellation in retry loop",
			sequence: []struct {
				gone bool
				err  error
			}{{}, {}, {}},
			cancelAt: 3, wantError: NewError(CodeDeadline, "canceled replacement"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			ops := supervisorReplacementOps{
				bind: func(string, Manifest) (*Client, error) { return &Client{}, nil },
				gone: func(context.Context, string, string) (bool, error) {
					calls++
					if calls == test.cancelAt {
						cancel()
					}
					index := calls - 1
					if index >= len(test.sequence) {
						t.Fatalf("unexpected replacement observation %d", calls)
					}
					return test.sequence[index].gone, test.sequence[index].err
				},
				shutdown: func(context.Context, *Client) error {
					return NewError(CodeUnavailable, "shutdown response unavailable")
				},
			}
			err := shutdownBrokerForReplacementWith(ctx, newClient(), observedEpoch, ops)
			if test.wantError == nil && err != nil || test.wantError != nil && CodeOf(err) != CodeOf(test.wantError) {
				t.Fatalf("replacement sequence error = %v, calls=%d", err, calls)
			}
		})
	}

	if err := shutdownBrokerForReplacementWith(
		t.Context(), newClient(), observedEpoch, supervisorReplacementOps{},
	); CodeOf(err) != CodeInvalid {
		t.Fatalf("invalid replacement operations error = %v", err)
	}
}

func TestReplacementObservationInjectedFaults(t *testing.T) {
	observedEpoch := strings.Repeat("a", epochBytes*2)
	successor := Manifest{Epoch: strings.Repeat("b", epochBytes*2)}
	if gone, err := replacementEpochGoneWith(
		t.Context(), "home", observedEpoch, supervisorReplacementObservationOps{},
	); gone || CodeOf(err) != CodeInvalid {
		t.Fatalf("invalid replacement observation operations = %t, %v", gone, err)
	}
	canary := errors.New("bind authenticated successor")
	pinged := false
	gone, err := replacementEpochGoneWith(
		t.Context(), "home", observedEpoch, supervisorReplacementObservationOps{
			read: func(string) (Manifest, error) { return successor, nil },
			bind: func(string, Manifest) (*Client, error) { return nil, canary },
			ping: func(context.Context, *Client) error {
				pinged = true
				return nil
			},
		},
	)
	if gone || !errors.Is(err, canary) || pinged {
		t.Fatalf("replacement bind fault = %t, %v, pinged=%t", gone, err, pinged)
	}
}

func TestEnsureSupervisorStateMachine(t *testing.T) {
	matching := BrokerStatus{PID: 12345, CatalogFingerprint: testCatalogFingerprint}
	client := &Client{}
	now := time.Unix(1000, 0)
	baseOps := func(connect func(context.Context, string) (*Client, BrokerStatus, error)) supervisorEnsureOps {
		return supervisorEnsureOps{
			connect:  connect,
			shutdown: func(context.Context, *Client, string) error { return nil },
			start:    func(EnsureOptions, string, time.Time) (*supervisorAttempt, error) { return nil, nil },
			poll:     time.Millisecond,
			now:      func() time.Time { return now },
		}
	}

	t.Run("nil context exact attachment", func(t *testing.T) {
		got, err := ensureSupervisor(nil, EnsureOptions{
			Home: t.TempDir(), CatalogFingerprint: testCatalogFingerprint,
		}, baseOps(func(context.Context, string) (*Client, BrokerStatus, error) {
			return client, matching, nil
		}))
		if err != nil || got != client {
			t.Fatalf("ensureSupervisor() = %#v, %v", got, err)
		}
	})

	t.Run("invalid ops", func(t *testing.T) {
		if got, err := ensureSupervisor(t.Context(), EnsureOptions{
			Home: t.TempDir(), CatalogFingerprint: testCatalogFingerprint,
		}, supervisorEnsureOps{}); got != nil || CodeOf(err) != CodeInvalid {
			t.Fatalf("invalid ops = %#v, %v", got, err)
		}
	})

	t.Run("terminal connect prevents child start", func(t *testing.T) {
		terminal := NewError(CodeIntegrity, "unsafe discovery")
		starts := 0
		ops := baseOps(func(context.Context, string) (*Client, BrokerStatus, error) {
			return nil, BrokerStatus{}, terminal
		})
		ops.start = func(EnsureOptions, string, time.Time) (*supervisorAttempt, error) {
			starts++
			return nil, nil
		}
		_, err := ensureSupervisor(t.Context(), EnsureOptions{
			Home: t.TempDir(), CatalogFingerprint: testCatalogFingerprint,
		}, ops)
		if !errors.Is(err, terminal) || starts != 0 {
			t.Fatalf("terminal connect = %v, starts=%d", err, starts)
		}
	})

	t.Run("start then attach without duplicate start", func(t *testing.T) {
		calls := 0
		starts := 0
		owner := &testSupervisorProcessOwner{}
		done := make(chan struct{})
		ops := baseOps(func(context.Context, string) (*Client, BrokerStatus, error) {
			calls++
			if calls == 1 {
				return nil, BrokerStatus{}, NewError(CodeUnavailable, "not running")
			}
			return client, matching, nil
		})
		ops.start = func(EnsureOptions, string, time.Time) (*supervisorAttempt, error) {
			starts++
			return &supervisorAttempt{pid: matching.PID, owner: owner, done: done}, nil
		}
		got, err := ensureSupervisor(t.Context(), EnsureOptions{
			Home: t.TempDir(), CatalogFingerprint: testCatalogFingerprint, Timeout: time.Second,
		}, ops)
		close(done)
		if err != nil || got != client || starts != 1 || owner.closed.Load() != 1 {
			t.Fatalf(
				"start/attach = %#v, %v, starts=%d closed=%d",
				got, err, starts, owner.closed.Load(),
			)
		}
	})

	t.Run("successful start without attempt fails closed", func(t *testing.T) {
		starts := 0
		ops := baseOps(func(context.Context, string) (*Client, BrokerStatus, error) {
			return nil, BrokerStatus{}, NewError(CodeUnavailable, "not running")
		})
		ops.start = func(EnsureOptions, string, time.Time) (*supervisorAttempt, error) {
			starts++
			return nil, nil
		}
		got, err := ensureSupervisor(t.Context(), EnsureOptions{
			Home: t.TempDir(), CatalogFingerprint: testCatalogFingerprint, Timeout: time.Second,
		}, ops)
		if got != nil || CodeOf(err) != CodeInternal || starts != 1 {
			t.Fatalf("missing launch attempt = %#v, %v, starts=%d", got, err, starts)
		}
	})

	t.Run("ambiguous replacement failure does not start", func(t *testing.T) {
		shutdowns := 0
		starts := 0
		ops := baseOps(func(context.Context, string) (*Client, BrokerStatus, error) {
			return client, BrokerStatus{CatalogFingerprint: "sha256:" + strings.Repeat("a", 64)}, nil
		})
		ops.shutdown = func(context.Context, *Client, string) error {
			shutdowns++
			return NewError(CodeConflict, "retained")
		}
		ops.start = func(EnsureOptions, string, time.Time) (*supervisorAttempt, error) {
			starts++
			return nil, NewError(CodeUnavailable, "start failed")
		}
		_, err := ensureSupervisor(t.Context(), EnsureOptions{
			Home: t.TempDir(), CatalogFingerprint: testCatalogFingerprint, Timeout: 4 * time.Millisecond,
		}, ops)
		if CodeOf(err) != CodeUnavailable || shutdowns == 0 || starts != 0 {
			t.Fatalf("failed replacement = %v, shutdowns=%d starts=%d", err, shutdowns, starts)
		}
	})

	t.Run("cancellation prevents child start", func(t *testing.T) {
		starts := 0
		ops := baseOps(func(context.Context, string) (*Client, BrokerStatus, error) {
			return nil, BrokerStatus{}, NewError(CodeUnavailable, "not running")
		})
		ops.start = func(EnsureOptions, string, time.Time) (*supervisorAttempt, error) {
			starts++
			return nil, nil
		}
		_, err := ensureSupervisor(canceledContext(), EnsureOptions{
			Home: t.TempDir(), CatalogFingerprint: testCatalogFingerprint, Timeout: time.Second,
		}, ops)
		if CodeOf(err) != CodeDeadline || starts != 0 {
			t.Fatalf("canceled ensure = %v, starts=%d", err, starts)
		}
	})

	t.Run("terminal start failure", func(t *testing.T) {
		terminal := NewError(CodeInvalid, "bad executable")
		ops := baseOps(func(context.Context, string) (*Client, BrokerStatus, error) {
			return nil, BrokerStatus{}, NewError(CodeUnavailable, "not running")
		})
		ops.start = func(EnsureOptions, string, time.Time) (*supervisorAttempt, error) {
			return nil, terminal
		}
		_, err := ensureSupervisor(t.Context(), EnsureOptions{
			Home: t.TempDir(), CatalogFingerprint: testCatalogFingerprint,
		}, ops)
		if !errors.Is(err, terminal) {
			t.Fatalf("terminal start failure = %v", err)
		}
	})

	t.Run("terminal replacement failure", func(t *testing.T) {
		terminal := NewError(CodeIntegrity, "unsafe old broker")
		ops := baseOps(func(context.Context, string) (*Client, BrokerStatus, error) {
			return client, BrokerStatus{CatalogFingerprint: "sha256:" + strings.Repeat("c", 64)}, nil
		})
		ops.shutdown = func(context.Context, *Client, string) error { return terminal }
		_, err := ensureSupervisor(t.Context(), EnsureOptions{
			Home: t.TempDir(), CatalogFingerprint: testCatalogFingerprint,
		}, ops)
		if !errors.Is(err, terminal) {
			t.Fatalf("terminal replacement failure = %v", err)
		}
	})

	t.Run("one live attempt remains the only start after throttle elapses", func(t *testing.T) {
		calls := 0
		starts := 0
		owner := &testSupervisorProcessOwner{}
		done := make(chan struct{})
		ops := baseOps(func(context.Context, string) (*Client, BrokerStatus, error) {
			calls++
			if calls == 3 {
				return client, matching, nil
			}
			return nil, BrokerStatus{}, NewError(CodeUnavailable, "not running")
		})
		ops.start = func(EnsureOptions, string, time.Time) (*supervisorAttempt, error) {
			starts++
			return &supervisorAttempt{pid: matching.PID, owner: owner, done: done}, nil
		}
		ops.now = func() time.Time {
			now = now.Add(time.Second)
			return now
		}
		_, err := ensureSupervisor(t.Context(), EnsureOptions{
			Home: t.TempDir(), CatalogFingerprint: testCatalogFingerprint, Timeout: time.Second,
		}, ops)
		close(done)
		if err != nil || starts != 1 || owner.closed.Load() != 1 {
			t.Fatalf("one in-flight start = %d, %v, closed=%d", starts, err, owner.closed.Load())
		}
	})

	t.Run("winner release failure replaces successful result", func(t *testing.T) {
		calls := 0
		closeCanary := errors.New("winner release failed")
		owner := &testSupervisorProcessOwner{closeErr: closeCanary}
		done := make(chan struct{})
		ops := baseOps(func(context.Context, string) (*Client, BrokerStatus, error) {
			calls++
			if calls == 1 {
				return nil, BrokerStatus{}, NewError(CodeUnavailable, "not running")
			}
			return client, matching, nil
		})
		ops.start = func(EnsureOptions, string, time.Time) (*supervisorAttempt, error) {
			return &supervisorAttempt{pid: matching.PID, owner: owner, done: done}, nil
		}
		got, err := ensureSupervisor(t.Context(), EnsureOptions{
			Home: t.TempDir(), CatalogFingerprint: testCatalogFingerprint, Timeout: time.Second,
		}, ops)
		close(done)
		if got != nil || !errors.Is(err, closeCanary) || owner.closed.Load() != 1 {
			t.Fatalf("winner release fault = %#v, %v, closed=%d", got, err, owner.closed.Load())
		}
	})
}

type testSupervisorProcessOwner struct {
	terminated   atomic.Int32
	killed       atomic.Int32
	closed       atomic.Int32
	exitedFlag   atomic.Bool
	onTerminate  func()
	onKill       func()
	terminateErr error
	killErr      error
	closeErr     error
}

func (*testSupervisorProcessOwner) activate() error { return nil }

func (owner *testSupervisorProcessOwner) exited() bool { return owner.exitedFlag.Load() }

func (owner *testSupervisorProcessOwner) terminate() error {
	owner.terminated.Add(1)
	if owner.onTerminate != nil {
		owner.onTerminate()
	}
	return owner.terminateErr
}

func (owner *testSupervisorProcessOwner) kill() error {
	owner.killed.Add(1)
	if owner.onKill != nil {
		owner.onKill()
	}
	return owner.killErr
}

func (owner *testSupervisorProcessOwner) close() error {
	owner.closed.Add(1)
	return owner.closeErr
}

func TestCleanupSupervisorAttemptsDoesNotPreserveReusedExitedPID(t *testing.T) {
	const pid = 12345
	for _, test := range []struct {
		name     string
		keepPID  int
		readyPID func(string, string) int
	}{
		{name: "keep PID", keepPID: pid},
		{name: "final-probe PID", readyPID: func(string, string) int { return pid }},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := &testSupervisorProcessOwner{}
			done := make(chan struct{})
			close(done)
			attempt := &supervisorAttempt{pid: pid, owner: owner, done: done}
			err := cleanupSupervisorAttemptsWith(
				t.TempDir(), testCatalogFingerprint,
				[]*supervisorAttempt{attempt}, test.keepPID, test.readyPID,
			)
			if err != nil {
				t.Fatal(err)
			}
			if owner.terminated.Load() != 0 || owner.killed.Load() != 1 || owner.closed.Load() != 1 {
				t.Fatalf(
					"exited PID reuse cleanup terminated=%d killed=%d closed=%d",
					owner.terminated.Load(), owner.killed.Load(), owner.closed.Load(),
				)
			}
		})
	}
}

func TestCleanupSupervisorAttemptsPreservesLiveMatchingAttempt(t *testing.T) {
	const pid = 12345
	for _, test := range []struct {
		name     string
		keepPID  int
		readyPID func(string, string) int
	}{
		{name: "keep PID", keepPID: pid},
		{name: "final-probe PID", readyPID: func(string, string) int { return pid }},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := &testSupervisorProcessOwner{}
			done := make(chan struct{})
			attempt := &supervisorAttempt{pid: pid, owner: owner, done: done}
			err := cleanupSupervisorAttemptsWith(
				t.TempDir(), testCatalogFingerprint,
				[]*supervisorAttempt{attempt}, test.keepPID, test.readyPID,
			)
			close(done)
			if err != nil {
				t.Fatal(err)
			}
			if owner.terminated.Load() != 0 || owner.killed.Load() != 0 || owner.closed.Load() != 1 {
				t.Fatalf(
					"live matching cleanup terminated=%d killed=%d closed=%d",
					owner.terminated.Load(), owner.killed.Load(), owner.closed.Load(),
				)
			}
		})
	}
}

func TestCanceledEnsurePreservesConcurrentMatchingWinnerBeforeNextProbe(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	if err := os.Mkdir("relative-home", 0o700); err != nil {
		t.Fatal(err)
	}
	canonicalHome := filepath.Join(base, "relative-home")
	changedWorkingDirectory := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	owner := &testSupervisorProcessOwner{}
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	calls := 0
	type concurrentResult struct {
		server *Server
		client *Client
		err    error
	}
	result := make(chan concurrentResult, 1)
	_, err := ensureSupervisor(ctx, EnsureOptions{
		Home: "relative-home", CatalogFingerprint: testCatalogFingerprint,
		Timeout: 150 * time.Millisecond,
	}, supervisorEnsureOps{
		connect: func(context.Context, string) (*Client, BrokerStatus, error) {
			calls++
			return nil, BrokerStatus{}, NewError(CodeUnavailable, "probe delayed")
		},
		shutdown: func(context.Context, *Client, string) error { return nil },
		start: func(_ EnsureOptions, preparedHome string, deadline time.Time) (*supervisorAttempt, error) {
			cancel()
			attempt := &supervisorAttempt{
				pid: os.Getpid(), owner: owner, done: done, deadline: deadline,
			}
			if preparedHome != canonicalHome {
				preparedHomeErr := fmt.Errorf(
					"prepared home = %q, want %q", preparedHome, canonicalHome,
				)
				result <- concurrentResult{
					err: preparedHomeErr,
				}
				return attempt, preparedHomeErr
			}
			if chdirErr := os.Chdir(changedWorkingDirectory); chdirErr != nil {
				result <- concurrentResult{err: chdirErr}
				return attempt, chdirErr
			}
			go func() {
				// Publication happens after the canceled caller's one failed probe,
				// but before this attempt's absolute startup deadline.
				server, startErr := StartServer(context.Background(), ServerOptions{
					Home: preparedHome, CatalogFingerprint: testCatalogFingerprint,
				})
				if startErr != nil {
					result <- concurrentResult{err: startErr}
					return
				}
				client, attachErr := EnsureSupervisor(context.Background(), EnsureOptions{
					Home: preparedHome, CatalogFingerprint: testCatalogFingerprint,
					Timeout: time.Second,
				})
				result <- concurrentResult{server: server, client: client, err: attachErr}
			}()
			return attempt, nil
		},
		poll: time.Millisecond,
		now:  time.Now,
	})
	if restoreErr := os.Chdir(base); restoreErr != nil {
		t.Fatal(restoreErr)
	}
	concurrent := <-result
	if concurrent.server != nil {
		defer func() { _ = concurrent.server.Close(t.Context()) }()
	}
	if concurrent.err != nil || concurrent.client == nil {
		t.Fatalf("concurrent attach = %#v, %v", concurrent.client, concurrent.err)
	}
	if CodeOf(err) != CodeDeadline || calls != 1 || owner.terminated.Load() != 0 ||
		owner.killed.Load() != 0 || owner.closed.Load() != 1 {
		t.Fatalf(
			"canceled concurrent winner err=%v calls=%d terminated=%d killed=%d closed=%d",
			err, calls, owner.terminated.Load(), owner.killed.Load(), owner.closed.Load(),
		)
	}
	client, err := Connect(canonicalHome)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Ping(t.Context()); err != nil {
		t.Fatalf("canceled contender killed matching winner: %v", err)
	}
}

func TestCleanupSupervisorAttemptsWaitsAndProbesInParallel(t *testing.T) {
	const attemptCount = 4
	owners := make([]*testSupervisorProcessOwner, 0, attemptCount)
	attempts := make([]*supervisorAttempt, 0, attemptCount)
	for index := 0; index < attemptCount; index++ {
		owner := &testSupervisorProcessOwner{}
		done := make(chan struct{})
		close(done)
		owners = append(owners, owner)
		attempts = append(attempts, &supervisorAttempt{
			pid: 1000 + index, owner: owner, done: done,
		})
	}
	var active atomic.Int32
	var maximum atomic.Int32
	probeStarted := make(chan struct{}, attemptCount)
	releaseProbes := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseProbes) }) })
	probe := func(string, string) int {
		current := active.Add(1)
		for observed := maximum.Load(); current > observed; observed = maximum.Load() {
			if maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		probeStarted <- struct{}{}
		<-releaseProbes
		active.Add(-1)
		return 0
	}
	cleanupResult := make(chan error, 1)
	home := t.TempDir()
	go func() {
		cleanupResult <- cleanupSupervisorAttemptsWith(
			home, testCatalogFingerprint, attempts, 0, probe,
		)
	}()
	for index := 0; index < attemptCount; index++ {
		select {
		case <-probeStarted:
		case <-time.After(time.Second):
			t.Fatalf("only %d final probes started concurrently", index)
		}
	}
	if maximum.Load() != attemptCount {
		t.Fatalf("maximum concurrent final probes = %d, want %d", maximum.Load(), attemptCount)
	}
	releaseOnce.Do(func() { close(releaseProbes) })
	select {
	case err := <-cleanupResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("attempt cleanup did not finish after probes were released")
	}
	for index, owner := range owners {
		if owner.terminated.Load() != 0 || owner.killed.Load() != 1 || owner.closed.Load() != 1 {
			t.Fatalf(
				"attempt %d cleanup terminated=%d killed=%d closed=%d",
				index, owner.terminated.Load(), owner.killed.Load(), owner.closed.Load(),
			)
		}
	}
}

func TestFailedAttemptForceKillsOwnerAfterRootExit(t *testing.T) {
	done := make(chan struct{})
	owner := &testSupervisorProcessOwner{onKill: func() { close(done) }}
	owner.exitedFlag.Store(true)
	attempt := &supervisorAttempt{pid: 12345, owner: owner, done: done}
	if err := attempt.terminate(); err != nil {
		t.Fatal(err)
	}
	if owner.terminated.Load() != 0 || owner.killed.Load() != 1 || owner.closed.Load() != 1 {
		t.Fatalf(
			"exited-root cleanup terminated=%d killed=%d closed=%d",
			owner.terminated.Load(), owner.killed.Load(), owner.closed.Load(),
		)
	}
}

func TestFailedAttemptForceKillsAfterGracefulRootExit(t *testing.T) {
	done := make(chan struct{})
	owner := &testSupervisorProcessOwner{onTerminate: func() { close(done) }}
	attempt := &supervisorAttempt{pid: 12345, owner: owner, done: done}
	if err := attempt.terminate(); err != nil {
		t.Fatal(err)
	}
	if owner.terminated.Load() != 1 || owner.killed.Load() != 1 || owner.closed.Load() != 1 {
		t.Fatalf(
			"graceful-root cleanup terminated=%d killed=%d closed=%d",
			owner.terminated.Load(), owner.killed.Load(), owner.closed.Load(),
		)
	}
}

func TestSupervisorAttemptFaults(t *testing.T) {
	var nilAttempt *supervisorAttempt
	if err := nilAttempt.release(); err != nil {
		t.Fatalf("nil attempt release = %v", err)
	}
	if err := nilAttempt.terminate(); err != nil {
		t.Fatalf("nil attempt termination = %v", err)
	}
	if err := (&supervisorAttempt{}).terminate(); CodeOf(err) != CodeIntegrity {
		t.Fatalf("ownerless attempt termination = %v", err)
	}
	if live, err := nilAttempt.releaseIfLive(); live || err != nil {
		t.Fatalf("nil attempt live release = %t, %v", live, err)
	}

	done := make(chan struct{})
	owner := &testSupervisorProcessOwner{}
	if supervisorProcessExited(owner, done) {
		t.Fatal("open attempt was reported exited")
	}
	owner.exitedFlag.Store(true)
	if !supervisorProcessExited(owner, done) {
		t.Fatal("owner-confirmed exit was reported live")
	}
	close(done)

	if err := (&supervisorAttempt{}).release(); CodeOf(err) != CodeIntegrity {
		t.Fatalf("ownerless attempt release = %v", err)
	}
	closedAttempt := &supervisorAttempt{closed: true, owner: &testSupervisorProcessOwner{}}
	if err := closedAttempt.release(); err != nil {
		t.Fatalf("closed attempt release = %v", err)
	}
	if err := closedAttempt.terminate(); err != nil {
		t.Fatalf("closed attempt termination = %v", err)
	}

	closeCanary := errors.New("attempt close failed")
	closeOwner := &testSupervisorProcessOwner{closeErr: closeCanary}
	if err := (&supervisorAttempt{owner: closeOwner}).release(); !errors.Is(err, closeCanary) {
		t.Fatalf("attempt close fault = %v", err)
	}

	liveOwnerless := &supervisorAttempt{done: make(chan struct{})}
	if live, err := liveOwnerless.releaseIfLive(); live || CodeOf(err) != CodeIntegrity {
		t.Fatalf("ownerless live release = %t, %v", live, err)
	}
	liveCloseOwner := &testSupervisorProcessOwner{closeErr: closeCanary}
	if live, err := (&supervisorAttempt{
		owner: liveCloseOwner, done: make(chan struct{}),
	}).releaseIfLive(); !live || !errors.Is(err, closeCanary) {
		t.Fatalf("live close fault = %t, %v", live, err)
	}

	terminateCanary := errors.New("attempt TERM failed")
	killCanary := errors.New("attempt KILL failed")
	terminatedDone := make(chan struct{})
	failingOwner := &testSupervisorProcessOwner{
		terminateErr: terminateCanary,
		killErr:      killCanary,
		closeErr:     closeCanary,
		onTerminate:  func() { close(terminatedDone) },
	}
	err := (&supervisorAttempt{owner: failingOwner, done: terminatedDone}).terminate()
	if !errors.Is(err, terminateCanary) || !errors.Is(err, killCanary) ||
		!errors.Is(err, closeCanary) {
		t.Fatalf("joined attempt termination faults = %v", err)
	}

	synctest.Test(t, func(t *testing.T) {
		stuckOwner := &testSupervisorProcessOwner{}
		stuck := &supervisorAttempt{owner: stuckOwner, done: make(chan struct{})}
		err := stuck.terminate()
		if CodeOf(err) != CodeUnavailable || stuckOwner.terminated.Load() != 1 ||
			stuckOwner.killed.Load() != 1 || stuckOwner.closed.Load() != 1 {
			t.Fatalf(
				"stuck attempt termination = %v terminated=%d killed=%d closed=%d",
				err, stuckOwner.terminated.Load(), stuckOwner.killed.Load(), stuckOwner.closed.Load(),
			)
		}
	})
}

func TestCleanupSupervisorAttemptFaults(t *testing.T) {
	if err := cleanupSupervisorAttemptsWith(
		t.TempDir(), testCatalogFingerprint, []*supervisorAttempt{nil}, 0, nil,
	); err != nil {
		t.Fatalf("nil cleanup attempt = %v", err)
	}

	closeCanary := errors.New("cleanup release failed")
	for _, test := range []struct {
		name     string
		keepPID  int
		readyPID func(string, string) int
	}{
		{name: "keep PID", keepPID: 12345},
		{name: "final probe", readyPID: func(string, string) int { return 12345 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := &testSupervisorProcessOwner{closeErr: closeCanary}
			attempt := &supervisorAttempt{
				pid: 12345, owner: owner, done: make(chan struct{}),
			}
			err := cleanupSupervisorAttemptsWith(
				t.TempDir(), testCatalogFingerprint,
				[]*supervisorAttempt{attempt}, test.keepPID, test.readyPID,
			)
			if !errors.Is(err, closeCanary) || owner.closed.Load() != 1 {
				t.Fatalf("cleanup release fault = %v, closed=%d", err, owner.closed.Load())
			}
		})
	}

	killCanary := errors.New("cleanup kill failed")
	closeDone := make(chan struct{})
	close(closeDone)
	owner := &testSupervisorProcessOwner{killErr: killCanary, closeErr: closeCanary}
	err := cleanupSupervisorAttemptsWith(
		t.TempDir(), testCatalogFingerprint,
		[]*supervisorAttempt{{pid: 12345, owner: owner, done: closeDone}}, 0, nil,
	)
	if !errors.Is(err, killCanary) || !errors.Is(err, closeCanary) {
		t.Fatalf("cleanup termination faults = %v", err)
	}
}

func TestMonitorSupervisorStateMachine(t *testing.T) {
	retryable := NewError(CodeUnavailable, "retry")
	terminal := NewError(CodeIntegrity, "terminal")
	client := &Client{}

	run := func(t *testing.T, sequence []error, cancelAt int, initial, maximum time.Duration) error {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		return monitorSupervisor(ctx, EnsureOptions{}, supervisorMonitorOps{
			probeInterval:  time.Millisecond,
			initialBackoff: initial,
			maximumBackoff: maximum,
			ensure: func(context.Context, EnsureOptions) (*Client, error) {
				calls++
				if calls == cancelAt {
					cancel()
				}
				index := calls - 1
				if index >= len(sequence) {
					return client, nil
				}
				return client, sequence[index]
			},
		})
	}

	runErr := run(t, []error{terminal}, 0, time.Millisecond, 2*time.Millisecond)
	if !errors.Is(runErr, terminal) {
		t.Fatalf("initial failure = %v", runErr)
	}
	runErr = run(t, []error{retryable, nil}, 2, time.Millisecond, 2*time.Millisecond)
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("initial retry recovery = %v", runErr)
	}
	runErr = run(t, []error{nil}, 1, time.Millisecond, 2*time.Millisecond)
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("cancellation before probe = %v", runErr)
	}
	runErr = run(t, []error{nil, terminal}, 0, time.Millisecond, 2*time.Millisecond)
	if !errors.Is(runErr, terminal) {
		t.Fatalf("terminal probe = %v", runErr)
	}
	runErr = run(t, []error{nil, retryable, terminal}, 0, time.Millisecond, 2*time.Millisecond)
	if !errors.Is(runErr, terminal) {
		t.Fatalf("terminal retry = %v", runErr)
	}
	runErr = run(t, []error{nil, retryable, retryable}, 3, time.Millisecond, 2*time.Millisecond)
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("canceled backoff = %v", runErr)
	}
	runErr = run(t, []error{nil, nil}, 2, 0, time.Millisecond)
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("successful probe reset = %v", runErr)
	}
	runErr = run(t, []error{nil, retryable, nil}, 3, 0, time.Millisecond)
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("successful retry reset = %v", runErr)
	}
	runErr = run(
		t, []error{nil, retryable, retryable, retryable, nil}, 5,
		time.Millisecond, 1500*time.Microsecond,
	)
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("capped backoff = %v", runErr)
	}
}

func TestMonitorSupervisorDeterministicFaults(t *testing.T) {
	retryable := NewError(CodeUnavailable, "retry monitor")
	client := &Client{}

	t.Run("initial backoff cancellation", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			calls := 0
			err := monitorSupervisor(ctx, EnsureOptions{}, supervisorMonitorOps{
				probeInterval: time.Second, initialBackoff: time.Second,
				maximumBackoff: 2 * time.Second,
				ensure: func(context.Context, EnsureOptions) (*Client, error) {
					calls++
					cancel()
					return nil, retryable
				},
			})
			if !errors.Is(err, context.Canceled) || calls != 1 {
				t.Fatalf("initial backoff cancellation = %v, calls=%d", err, calls)
			}
		})
	})

	t.Run("initial backoff cap then probe cancellation", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			calls := 0
			err := monitorSupervisor(ctx, EnsureOptions{}, supervisorMonitorOps{
				probeInterval: time.Second, initialBackoff: 2 * time.Second,
				maximumBackoff: 3 * time.Second,
				ensure: func(context.Context, EnsureOptions) (*Client, error) {
					calls++
					if calls < 3 {
						return nil, retryable
					}
					cancel()
					return client, nil
				},
			})
			if !errors.Is(err, context.Canceled) || calls != 3 {
				t.Fatalf("initial capped recovery = %v, calls=%d", err, calls)
			}
		})
	})

	t.Run("terminal inner retry", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			terminal := NewError(CodeIntegrity, "terminal monitor retry")
			calls := 0
			err := monitorSupervisor(context.Background(), EnsureOptions{}, supervisorMonitorOps{
				probeInterval: time.Second, initialBackoff: time.Second,
				maximumBackoff: 2 * time.Second,
				ensure: func(context.Context, EnsureOptions) (*Client, error) {
					calls++
					switch calls {
					case 1:
						return client, nil
					case 2:
						return nil, retryable
					default:
						return nil, terminal
					}
				},
			})
			if !errors.Is(err, terminal) || calls != 3 {
				t.Fatalf("terminal inner retry = %v, calls=%d", err, calls)
			}
		})
	})

	t.Run("inner backoff cancellation", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			calls := 0
			err := monitorSupervisor(ctx, EnsureOptions{}, supervisorMonitorOps{
				probeInterval: time.Second, initialBackoff: time.Second,
				maximumBackoff: 2 * time.Second,
				ensure: func(context.Context, EnsureOptions) (*Client, error) {
					calls++
					if calls == 1 {
						return client, nil
					}
					if calls == 3 {
						cancel()
					}
					return nil, retryable
				},
			})
			if !errors.Is(err, context.Canceled) || calls != 3 {
				t.Fatalf("inner backoff cancellation = %v, calls=%d", err, calls)
			}
		})
	})

	t.Run("inner backoff cap and recovery", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			calls := 0
			err := monitorSupervisor(ctx, EnsureOptions{}, supervisorMonitorOps{
				probeInterval: time.Second, initialBackoff: 2 * time.Second,
				maximumBackoff: 3 * time.Second,
				ensure: func(context.Context, EnsureOptions) (*Client, error) {
					calls++
					if calls == 1 || calls >= 5 {
						if calls == 5 {
							cancel()
						}
						return client, nil
					}
					return nil, retryable
				},
			})
			if !errors.Is(err, context.Canceled) || calls < 5 || calls > 6 {
				t.Fatalf("inner capped recovery = %v, calls=%d", err, calls)
			}
		})
	})
}

func TestSupervisorOperationAndIdentityHelperBoundaries(t *testing.T) {
	canary := errors.New("supervisor operation canary")
	var nilOperation *supervisorOperationError
	if got := nilOperation.Error(); got != "database supervisor operation failed" {
		t.Fatalf("nil supervisor operation error = %q", got)
	}
	if nilOperation.Unwrap() != nil {
		t.Fatal("nil supervisor operation error unwrapped a cause")
	}
	wrapped := supervisorError("redacted supervisor operation", canary)
	if wrapped.Error() != "redacted supervisor operation" || !errors.Is(wrapped, canary) ||
		strings.Contains(wrapped.Error(), canary.Error()) {
		t.Fatalf("supervisor operation redaction = %q, unwrap=%t", wrapped, errors.Is(wrapped, canary))
	}
	if supervisorError("unused", nil) != nil {
		t.Fatal("nil supervisor cause produced an error")
	}
	if !supervisorAttemptDone(nil) {
		t.Fatal("nil attempt completion channel was treated as live")
	}
	if matched, err := currentTestExecutableWith("candidate", supervisorTestExecutableOps{
		testing: func() bool { return false },
	}); matched || err != nil {
		t.Fatalf("non-test executable identity = %t, %v", matched, err)
	}
	if matched, err := currentTestExecutableWith("candidate", supervisorTestExecutableOps{
		testing: func() bool { return true },
		executable: func() (string, error) {
			return "", canary
		},
	}); matched || err == nil {
		t.Fatalf("current executable lookup fault = %t, %v", matched, err)
	}

	regularPath := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(regularPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	regular, err := os.Open(regularPath)
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	if _, err := supervisorOpenedDirectoryIdentity(regular); CodeOf(err) != CodeIntegrity {
		t.Fatalf("regular file accepted as opened directory: %v", err)
	}
	if err := validateSupervisorOpenedFile(regularPath, nil, 0o600); CodeOf(err) != CodeIntegrity {
		t.Fatalf("nil supervisor file accepted: %v", err)
	}
}
