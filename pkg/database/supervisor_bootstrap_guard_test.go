//nolint:govet // Independent failure-boundary assertions intentionally reuse narrow error names.
package database

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestSupervisorBootstrapGuardSuccessReplayAndCompatibility(t *testing.T) {
	home, guard := requireSupervisorBootstrapGuard(t)
	if err := guard.Validate(home); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	before := requireGuardDirectoryState(t, home)
	for range 3 {
		if err := guard.Validate(home); err != nil {
			t.Fatalf("repeated Validate() error = %v", err)
		}
	}
	after := requireGuardDirectoryState(t, home)
	if before != after {
		t.Fatalf("guard validation mutated state: before=%q after=%q", before, after)
	}

	const workers = 32
	errorsByWorker := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 20 {
				if err := guard.Validate(home); err != nil {
					errorsByWorker <- err
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		t.Fatal(err)
	}

	if _, err := ConsumeSupervisorBootstrapGuard(home); CodeOf(err) != CodeUnauthorized {
		t.Fatalf("bootstrap replay error = %v", err)
	}
	assertSupervisorBootstrapEnvironmentEmpty(t)

	compatibilityHome := t.TempDir()
	artifact, token := prepareGuardBootstrapAuthority(t, compatibilityHome)
	if !ConsumeSupervisorBootstrap(compatibilityHome) {
		t.Fatal("compatibility bootstrap wrapper rejected valid authority")
	}
	setSupervisorBootstrapTestAuthority(t, token, artifact)
	if ConsumeSupervisorBootstrap(compatibilityHome) {
		t.Fatal("compatibility bootstrap wrapper accepted replay")
	}
}

func TestSupervisorBootstrapGuardRejectsBoundaryReplacement(t *testing.T) {
	t.Run("whole home", func(t *testing.T) {
		home, guard := requireSupervisorBootstrapGuard(t)
		displaced := home + ".displaced"
		if err := os.Rename(home, displaced); err != nil {
			t.Fatal(err)
		}
		if err := createOwnerOnlyDirectory(home); err != nil {
			t.Fatal(err)
		}
		if err := createOwnerOnlyDirectory(filepath.Join(home, StateDirectoryName)); err != nil {
			t.Fatal(err)
		}
		restoreGuardHome(t, home, displaced)
		requireGuardIntegrity(t, guard.Validate(home), home)
		if info, err := os.Lstat(filepath.Join(home, StateDirectoryName)); err != nil || !info.IsDir() {
			t.Fatalf("replacement home was mutated: %#v, %v", info, err)
		}
	})

	t.Run("state directory", func(t *testing.T) {
		home, guard := requireSupervisorBootstrapGuard(t)
		stateDir := filepath.Join(home, StateDirectoryName)
		displaced := stateDir + ".displaced"
		if err := os.Rename(stateDir, displaced); err != nil {
			t.Fatal(err)
		}
		if err := createOwnerOnlyDirectory(stateDir); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = os.RemoveAll(stateDir)
			_ = os.Rename(displaced, stateDir)
		})
		requireGuardIntegrity(t, guard.Validate(home), home)
		if info, err := os.Lstat(stateDir); err != nil || !info.IsDir() {
			t.Fatalf("replacement state directory was mutated: %#v, %v", info, err)
		}
	})

	t.Run("home symlink", func(t *testing.T) {
		home, guard := requireSupervisorBootstrapGuard(t)
		displaced := home + ".displaced"
		if err := os.Rename(home, displaced); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(displaced, home); err != nil {
			_ = os.Rename(displaced, home)
			t.Skipf("symlinks unavailable: %v", err)
		}
		restoreGuardHome(t, home, displaced)
		requireGuardIntegrity(t, guard.Validate(home), home)
	})

	t.Run("unsafe state mode", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows state trust is enforced by DACL, not mode bits")
		}
		home, guard := requireSupervisorBootstrapGuard(t)
		stateDir := filepath.Join(home, StateDirectoryName)
		if err := os.Chmod(stateDir, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(stateDir, 0o700) })
		requireGuardIntegrity(t, guard.Validate(home), home)
	})
}

func TestSupervisorBootstrapGuardRejectsWrongAndInvalidGuard(t *testing.T) {
	home, guard := requireSupervisorBootstrapGuard(t)
	wrongHome := t.TempDir()
	if _, err := prepareStateDirectory(wrongHome); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		guard *SupervisorBootstrapGuard
		home  string
	}{
		{name: "nil", home: home},
		{name: "zero", guard: &SupervisorBootstrapGuard{}, home: home},
		{name: "wrong home", guard: guard, home: wrongHome},
		{
			name: "invalid relationship",
			guard: &SupervisorBootstrapGuard{
				home: guard.home, homeIdentity: guard.homeIdentity,
				stateDir: wrongHome, stateIdentity: guard.stateIdentity,
			},
			home: home,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireGuardIntegrity(t, test.guard.Validate(test.home), test.home)
		})
	}
}

func TestSupervisorBootstrapGuardWorksAsServerStartupGuard(t *testing.T) {
	home, guard := requireSupervisorBootstrapGuard(t)
	if err := guard.Validate(home); err != nil {
		t.Fatal(err)
	}
	server, err := StartServer(t.Context(), ServerOptions{
		Home: home,
		StartupGuard: func() error {
			return guard.Validate(home)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	closeServer(t, server)

	replacedHome, replacedGuard := requireSupervisorBootstrapGuard(t)
	if err := replacedGuard.Validate(replacedHome); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(replacedHome, StateDirectoryName)
	displaced := stateDir + ".displaced"
	if err := os.Rename(stateDir, displaced); err != nil {
		t.Fatal(err)
	}
	if err := createOwnerOnlyDirectory(stateDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(stateDir)
		_ = os.Rename(displaced, stateDir)
	})
	server, err = StartServer(t.Context(), ServerOptions{
		Home: replacedHome,
		StartupGuard: func() error {
			return replacedGuard.Validate(replacedHome)
		},
	})
	if server != nil || CodeOf(err) != CodeIntegrity {
		t.Fatalf("StartServer() with replaced state = %#v, %v", server, err)
	}
	if _, err := ReadManifest(replacedHome); CodeOf(err) != CodeUnavailable {
		t.Fatalf("replaced guarded startup published discovery: %v", err)
	}
}

func TestSupervisorBootstrapGuardOperationBoundaries(t *testing.T) {
	home := t.TempDir()
	if _, err := prepareStateDirectory(home); err != nil {
		t.Fatal(err)
	}
	validBoundary, err := captureSupervisorBootstrapBoundary(
		home,
		defaultSupervisorBootstrapGuardOps().boundary,
	)
	if err != nil {
		t.Fatal(err)
	}
	authority := guardTestAuthority(t)
	validOps := defaultSupervisorBootstrapGuardOps()
	validOps.consume = func(string, supervisorBootstrapAuthority, supervisorBootstrapConsumeOps) bool {
		return true
	}

	t.Run("invalid operations", func(t *testing.T) {
		if guard, err := consumeSupervisorBootstrapGuardWith(
			home, authority, supervisorBootstrapGuardOps{},
		); guard != nil || CodeOf(err) != CodeInvalid {
			t.Fatalf("invalid consume operations = %#v, %v", guard, err)
		}
		if _, err := captureSupervisorBootstrapBoundary(
			home,
			supervisorBootstrapBoundaryOps{},
		); CodeOf(err) != CodeInvalid {
			t.Fatalf("invalid boundary operations = %v", err)
		}
	})

	t.Run("invalid authority", func(t *testing.T) {
		for _, candidate := range []supervisorBootstrapAuthority{
			{},
			{token: authority.token, executableIdentity: authority.executableIdentity},
			{token: authority.token, bootstrapIdentity: authority.bootstrapIdentity},
		} {
			if guard, err := consumeSupervisorBootstrapGuardWith(home, candidate, validOps); guard != nil ||
				CodeOf(err) != CodeUnauthorized {
				t.Fatalf("invalid authority = %#v, %v", guard, err)
			}
		}
	})

	t.Run("image authority", func(t *testing.T) {
		for _, alter := range []func(*supervisorBootstrapGuardOps){
			func(ops *supervisorBootstrapGuardOps) { ops.consumeOps.executable = nil },
			func(ops *supervisorBootstrapGuardOps) {
				ops.consumeOps.executable = func() (string, error) { return "", errors.New("secret executable") }
			},
			func(ops *supervisorBootstrapGuardOps) {
				ops.consumeOps.pathIdentity = func(string) (supervisorFileIdentity, error) {
					return supervisorFileIdentity{}, errors.New("secret identity")
				}
			},
		} {
			ops := validOps
			alter(&ops)
			guard, err := consumeSupervisorBootstrapGuardWith(home, authority, ops)
			if guard != nil || (CodeOf(err) != CodeInvalid && CodeOf(err) != CodeUnauthorized) {
				t.Fatalf("invalid image authority = %#v, %v", guard, err)
			}
		}
	})

	t.Run("consume rejected", func(t *testing.T) {
		ops := validOps
		ops.consume = func(string, supervisorBootstrapAuthority, supervisorBootstrapConsumeOps) bool {
			return false
		}
		if guard, err := consumeSupervisorBootstrapGuardWith(home, authority, ops); guard != nil ||
			CodeOf(err) != CodeUnauthorized {
			t.Fatalf("rejected consume = %#v, %v", guard, err)
		}
	})

	t.Run("boundary changes after consume", func(t *testing.T) {
		ops := validOps
		calls := 0
		ops.boundary.canonicalHome = func(string) (string, error) {
			calls++
			if calls > 2 {
				return "", errors.New("secret changed home")
			}
			return validBoundary.home, nil
		}
		guard, err := consumeSupervisorBootstrapGuardWith(home, authority, ops)
		if guard != nil || CodeOf(err) != CodeIntegrity {
			t.Fatalf("changed post-consume boundary = %#v, %v", guard, err)
		}
	})

	t.Run("boundary changes during final validation", func(t *testing.T) {
		ops := validOps
		calls := 0
		ops.boundary.canonicalHome = func(string) (string, error) {
			calls++
			if calls > 4 {
				return "", errors.New("secret final validation change")
			}
			return validBoundary.home, nil
		}
		guard, err := consumeSupervisorBootstrapGuardWith(home, authority, ops)
		if guard != nil || CodeOf(err) != CodeIntegrity {
			t.Fatalf("changed final boundary = %#v, %v", guard, err)
		}
	})

	t.Run("direct image authority validation", func(t *testing.T) {
		missingExecutable := authority
		missingExecutable.executableIdentity = ""
		if supervisorBootstrapImageAuthorityValid(missingExecutable, validOps.consumeOps) {
			t.Fatal("image authority without expected executable identity was accepted")
		}
	})
}

func TestSupervisorBootstrapGuardRejectsStateSwapAroundConsumption(t *testing.T) {
	for _, test := range []struct {
		name       string
		swapBefore bool
		wantCode   ErrorCode
	}{
		{name: "before exact consume", swapBefore: true, wantCode: CodeUnauthorized},
		{name: "after exact consume", wantCode: CodeIntegrity},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			artifact, _ := prepareGuardBootstrapAuthority(t, home)
			authority := consumeSupervisorBootstrapEnvironment()
			stateDir := filepath.Join(home, StateDirectoryName)
			displaced := stateDir + ".displaced"
			restore := func() {
				_ = os.RemoveAll(stateDir)
				_ = os.Rename(displaced, stateDir)
			}
			t.Cleanup(restore)
			swap := func() {
				if err := os.Rename(stateDir, displaced); err != nil {
					t.Fatal(err)
				}
				if err := createOwnerOnlyDirectory(stateDir); err != nil {
					t.Fatal(err)
				}
			}
			ops := defaultSupervisorBootstrapGuardOps()
			realConsume := ops.consume
			ops.consume = func(
				home string,
				authority supervisorBootstrapAuthority,
				consumeOps supervisorBootstrapConsumeOps,
			) bool {
				if test.swapBefore {
					swap()
				}
				consumed := realConsume(home, authority, consumeOps)
				if !test.swapBefore {
					swap()
				}
				return consumed
			}
			guard, err := consumeSupervisorBootstrapGuardWith(home, authority, ops)
			if guard != nil || CodeOf(err) != test.wantCode {
				t.Fatalf("guard around state swap = %#v, %v", guard, err)
			}
			if info, statErr := os.Lstat(stateDir); statErr != nil || !info.IsDir() {
				t.Fatalf("replacement state was removed: %#v, %v", info, statErr)
			}
			if test.swapBefore {
				retained := filepath.Join(displaced, filepath.Base(artifact.path))
				if _, statErr := os.Lstat(retained); statErr != nil {
					t.Fatalf("unconsumed exact bootstrap was not preserved: %v", statErr)
				}
			}
		})
	}
}

func TestSupervisorBootstrapBoundaryInspectionFaults(t *testing.T) {
	home := t.TempDir()
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	homeIdentity, err := supervisorDirectoryIdentity(home)
	if err != nil {
		t.Fatal(err)
	}
	stateIdentity, err := supervisorDirectoryIdentity(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	canary := errors.New("secret boundary detail")
	valid := supervisorBootstrapBoundaryOps{
		canonicalHome:  func(string) (string, error) { return home, nil },
		stateDirectory: func(string) (string, error) { return stateDir, nil },
		directoryIdentity: func(path string) (supervisorFileIdentity, error) {
			if sameCanonicalPath(path, home) {
				return homeIdentity, nil
			}
			return stateIdentity, nil
		},
	}
	tests := []struct {
		name  string
		alter func(*supervisorBootstrapBoundaryOps)
	}{
		{name: "canonical error", alter: func(ops *supervisorBootstrapBoundaryOps) {
			ops.canonicalHome = func(string) (string, error) { return "", canary }
		}},
		{name: "canonical empty", alter: func(ops *supervisorBootstrapBoundaryOps) {
			ops.canonicalHome = func(string) (string, error) { return "", nil }
		}},
		{name: "canonical relative", alter: func(ops *supervisorBootstrapBoundaryOps) {
			ops.canonicalHome = func(string) (string, error) { return "relative", nil }
		}},
		{name: "canonical dirty", alter: func(ops *supervisorBootstrapBoundaryOps) {
			ops.canonicalHome = func(string) (string, error) {
				return home + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(home), nil
			}
		}},
		{name: "home identity error", alter: func(ops *supervisorBootstrapBoundaryOps) {
			ops.directoryIdentity = func(string) (supervisorFileIdentity, error) { return supervisorFileIdentity{}, canary }
		}},
		{name: "state error", alter: func(ops *supervisorBootstrapBoundaryOps) {
			ops.stateDirectory = func(string) (string, error) { return "", canary }
		}},
		{name: "state relative", alter: func(ops *supervisorBootstrapBoundaryOps) {
			ops.stateDirectory = func(string) (string, error) { return "relative", nil }
		}},
		{name: "state outside", alter: func(ops *supervisorBootstrapBoundaryOps) {
			ops.stateDirectory = func(string) (string, error) { return filepath.Dir(stateDir), nil }
		}},
		{name: "state identity error", alter: func(ops *supervisorBootstrapBoundaryOps) {
			ops.directoryIdentity = func(path string) (supervisorFileIdentity, error) {
				if sameCanonicalPath(path, home) {
					return homeIdentity, nil
				}
				return supervisorFileIdentity{}, canary
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ops := valid
			test.alter(&ops)
			boundary, err := inspectSupervisorBootstrapBoundary(home, ops)
			if boundary != (supervisorBootstrapBoundary{}) ||
				CodeOf(err) != CodeIntegrity || strings.Contains(err.Error(), canary.Error()) ||
				strings.Contains(err.Error(), home) {
				t.Fatalf("boundary fault = %#v, %v", boundary, err)
			}
		})
	}

	t.Run("mixed snapshots", func(t *testing.T) {
		ops := valid
		calls := 0
		ops.directoryIdentity = func(path string) (supervisorFileIdentity, error) {
			if sameCanonicalPath(path, home) {
				calls++
				if calls > 1 {
					return stateIdentity, nil
				}
				return homeIdentity, nil
			}
			return stateIdentity, nil
		}
		if _, err := captureSupervisorBootstrapBoundary(home, ops); CodeOf(err) != CodeIntegrity {
			t.Fatalf("mixed boundary snapshots = %v", err)
		}
	})
}

func requireSupervisorBootstrapGuard(t *testing.T) (string, *SupervisorBootstrapGuard) {
	t.Helper()
	home := t.TempDir()
	_, _ = prepareGuardBootstrapAuthority(t, home)
	guard, err := ConsumeSupervisorBootstrapGuard(home)
	if err != nil || guard == nil {
		t.Fatalf("ConsumeSupervisorBootstrapGuard() = %#v, %v", guard, err)
	}
	assertSupervisorBootstrapEnvironmentEmpty(t)
	return home, guard
}

func prepareGuardBootstrapAuthority(t *testing.T, home string) (supervisorBootstrapArtifact, string) {
	t.Helper()
	token, err := randomHex(tokenBytes)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := prepareSupervisorBootstrapArtifact(home, token)
	if err != nil {
		t.Fatal(err)
	}
	setSupervisorBootstrapTestAuthority(t, token, artifact)
	return artifact, token
}

func guardTestAuthority(t *testing.T) supervisorBootstrapAuthority {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := supervisorPathIdentity(executable)
	if err != nil {
		t.Fatal(err)
	}
	return supervisorBootstrapAuthority{
		token:              strings.Repeat("a", tokenBytes*2),
		bootstrapIdentity:  "test-bootstrap-identity",
		executableIdentity: identity.String(),
	}
}

func assertSupervisorBootstrapEnvironmentEmpty(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		supervisorBootstrapEnvironment,
		supervisorBootstrapIdentityEnvironment,
		supervisorExecutableIdentityEnvironment,
	} {
		if value := os.Getenv(name); value != "" {
			t.Fatalf("bootstrap environment %s remains %q", name, value)
		}
	}
}

func requireGuardIntegrity(t *testing.T, err error, secrets ...string) {
	t.Helper()
	if CodeOf(err) != CodeIntegrity {
		t.Fatalf("guard error = %v, want Integrity", err)
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Fatalf("guard error exposed %q: %v", secret, err)
		}
	}
}

func requireGuardDirectoryState(t *testing.T, home string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(home, StateDirectoryName))
	if err != nil {
		t.Fatal(err)
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.Name())
	}
	return strings.Join(result, "\x00")
}

func restoreGuardHome(t *testing.T, home, displaced string) {
	t.Helper()
	t.Cleanup(func() {
		info, err := os.Lstat(home)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(home)
		} else {
			_ = os.RemoveAll(home)
		}
		_ = os.Rename(displaced, home)
	})
}
