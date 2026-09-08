//go:build unix

package database

import (
	"errors"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

type foreignOwnerSocketInfo struct{ os.FileInfo }

func (info foreignOwnerSocketInfo) Sys() any {
	return &syscall.Stat_t{Uid: uint32(os.Geteuid()) + 1}
}

func TestRelativeDatabasePathsFailFromRemovedWorkingDirectory(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	removed := filepath.Join(parent, "removed")
	if err := os.Mkdir(removed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(removed); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	if err := os.Remove(removed); err != nil {
		if errors.Is(err, syscall.EBUSY) {
			t.Skip("platform does not unlink the current working directory")
		}
		t.Fatal(err)
	}

	if _, err := CanonicalHome("relative-home"); err == nil {
		t.Fatal("relative home resolved from a removed working directory")
	}
	if err := rejectExistingAncestorAlias("."); err == nil {
		t.Fatal("removed working directory was accepted as a canonical ancestor")
	}
	if err := startSupervisorProcess(EnsureOptions{
		Executable: "./picoclaw-test-helper",
	}, parent); CodeOf(err) != CodeInvalid {
		t.Fatalf("relative supervisor executable error = %v", err)
	}
}

func TestDatabaseHomeRejectsRealAliasAndStateBoundaries(t *testing.T) {
	root := t.TempDir()
	realParent := filepath.Join(root, "real")
	realHome := filepath.Join(realParent, "home")
	if err := os.MkdirAll(realHome, 0o700); err != nil {
		t.Fatal(err)
	}
	aliasParent := filepath.Join(root, "alias")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := CanonicalHome(filepath.Join(aliasParent, "home")); CodeOf(err) != CodeInvalid {
		t.Fatalf("existing home through alias error = %v", err)
	}

	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "missing"), dangling); err != nil {
		t.Fatal(err)
	}
	if err := rejectExistingAncestorAlias(dangling); err == nil {
		t.Fatal("dangling home ancestor was accepted")
	}

	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, StateDirectoryName), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareStateDirectory(home); CodeOf(err) != CodeIntegrity {
		t.Fatalf("public state directory error = %v", err)
	}
}

func TestDatabaseHomePropagatesRealPermissionFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	t.Run("create home", func(t *testing.T) {
		parent := t.TempDir()
		if err := os.Chmod(parent, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
		if _, err := PrepareHome(filepath.Join(parent, "child")); err == nil {
			t.Fatal("home was created beneath a non-writable parent")
		}
	})

	t.Run("create state directory", func(t *testing.T) {
		home := t.TempDir()
		if err := os.Chmod(home, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(home, 0o700) })
		if _, err := prepareStateDirectory(home); err == nil {
			t.Fatal("state directory was created beneath a non-writable home")
		}
	})
}

func TestDatabasePathsPropagateRealTraversalFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory traversal permissions")
	}
	parent := t.TempDir()
	blocked := filepath.Join(parent, "blocked")
	if err := os.Mkdir(blocked, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	child := filepath.Join(blocked, "child")
	if _, err := CanonicalHome(child); err == nil {
		t.Fatal("home inspection crossed a non-searchable directory")
	}
	if err := rejectExistingAncestorAlias(child); err == nil {
		t.Fatal("ancestor inspection crossed a non-searchable directory")
	}
	if err := cleanupEndpoint(filepath.Join(blocked, "broker.sock")); err == nil {
		t.Fatal("endpoint inspection crossed a non-searchable directory")
	}
}

func TestPrepareEndpointPropagatesRealParentPermissionFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	parent := shortCoverageUnixTempDir(t)
	endpoint := filepath.Join(parent, "stale.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(endpoint, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(parent, 0o700)
		_ = os.Remove(endpoint)
	})
	if err := prepareEndpoint(endpoint); err == nil {
		t.Fatal("stale endpoint removal succeeded without parent write permission")
	}
}

func TestUnixSocketValidationRejectsForeignOwnerMetadata(t *testing.T) {
	endpoint := filepath.Join(shortCoverageUnixTempDir(t), "foreign-owner.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(endpoint, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateOwnerOnlySocket(endpoint, foreignOwnerSocketInfo{info}); CodeOf(err) != CodeUnauthorized {
		t.Fatalf("foreign socket owner metadata error = %v", err)
	}
}

func TestReadManifestPropagatesRealDescriptorExhaustion(t *testing.T) {
	home := t.TempDir()
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	manifest := validContingencyManifest(t, stateDir)
	if err := writeManifest(stateDir, manifest); err != nil {
		t.Fatal(err)
	}
	var readErr error
	withContingencyResourceLimit(t, unix.RLIMIT_NOFILE, 0, func() {
		_, readErr = ReadManifest(home)
	})
	if readErr == nil || !strings.Contains(readErr.Error(), "open database broker manifest") {
		t.Fatalf("manifest descriptor exhaustion error = %v", readErr)
	}
}

func TestWriteManifestPropagatesRealFileSizeLimit(t *testing.T) {
	home := t.TempDir()
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	manifest := validContingencyManifest(t, stateDir)
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	var writeErr error
	withContingencyResourceLimit(t, unix.RLIMIT_FSIZE, 0, func() {
		writeErr = writeManifest(stateDir, manifest)
	})
	if writeErr == nil || !strings.Contains(writeErr.Error(), "write database broker manifest") {
		t.Fatalf("manifest file-size limit error = %v", writeErr)
	}
}

func withContingencyResourceLimit(t *testing.T, resource int, current uint64, run func()) {
	t.Helper()
	var original unix.Rlimit
	if err := unix.Getrlimit(resource, &original); err != nil {
		t.Skipf("resource limit %d is unavailable: %v", resource, err)
	}
	restricted := original
	restricted.Cur = current
	if err := unix.Setrlimit(resource, &restricted); err != nil {
		t.Skipf("resource limit %d cannot be reduced: %v", resource, err)
	}
	defer func() {
		if err := unix.Setrlimit(resource, &original); err != nil {
			t.Errorf("restore resource limit %d: %v", resource, err)
		}
	}()
	run()
}

func validContingencyManifest(t *testing.T, stateDir string) Manifest {
	t.Helper()
	token, err := randomHex(tokenBytes)
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := randomHex(epochBytes)
	if err != nil {
		t.Fatal(err)
	}
	return Manifest{
		PID: os.Getpid(), Protocol: ProtocolVersion, Token: token,
		Endpoint: endpointForStateDirectory(stateDir), Epoch: epoch,
	}
}

func TestConfigureSupervisorProcessRejectsDirectoryLogTarget(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "logs", "database-supervisor.log")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := configureSupervisorProcess(nil, home); err == nil {
		t.Fatal("supervisor process accepted a directory log target")
	}
}

func TestReadManifestPropagatesRealOverlongLeafPath(t *testing.T) {
	const targetStateDirectoryLength = 4090
	root := shortCoverageUnixTempDir(t)
	home := root
	targetHomeLength := targetStateDirectoryLength - 1 - len(StateDirectoryName)
	for len(home) < targetHomeLength {
		componentLength := targetHomeLength - len(home) - 1
		if componentLength > 200 {
			componentLength = 200
		}
		if componentLength <= 0 {
			break
		}
		name := make([]byte, componentLength)
		for index := range name {
			name[index] = 'x'
		}
		next := filepath.Join(home, string(name))
		if err := os.Mkdir(next, 0o700); err != nil {
			t.Skipf("filesystem cannot construct a near-limit path: %v", err)
		}
		home = next
	}
	stateDir := filepath.Join(home, StateDirectoryName)
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Skipf("filesystem cannot construct a near-limit state directory: %v", err)
	}
	if _, err := StateDirectory(home); err != nil {
		t.Skipf("near-limit state directory is unsupported: %v", err)
	}
	manifestPath := filepath.Join(stateDir, manifestFileName)
	if _, err := os.Lstat(manifestPath); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Skip("filesystem does not expose an overlong manifest leaf error")
	}
	if _, err := ReadManifest(home); err == nil {
		t.Fatal("overlong manifest leaf was accepted")
	}
}
