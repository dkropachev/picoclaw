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

type foreignOwnerFileInfo struct{ os.FileInfo }

func (info foreignOwnerFileInfo) Sys() any {
	return &syscall.Stat_t{Uid: uint32(os.Geteuid()) + 1}
}

type missingOwnerFileInfo struct{ os.FileInfo }

func (info missingOwnerFileInfo) Sys() any { return nil }

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

func TestDatabaseHomeRejectsWritableBoundaries(t *testing.T) {
	trusted := t.TempDir()
	trustedInfo, err := os.Lstat(trusted)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTrustedHomeDirectory(
		trusted,
		foreignOwnerFileInfo{trustedInfo},
	); CodeOf(err) != CodeUnauthorized {
		t.Fatalf("foreign home owner error = %v", err)
	}
	if err := validateTrustedHomeDirectory(
		trusted,
		missingOwnerFileInfo{trustedInfo},
	); CodeOf(err) != CodeIntegrity {
		t.Fatalf("missing home owner metadata error = %v", err)
	}

	existing := t.TempDir()
	if err := os.Chmod(existing, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(existing, 0o700) })
	if _, err := CanonicalHome(existing); CodeOf(err) != CodeIntegrity {
		t.Fatalf("writable existing home error = %v", err)
	}

	ancestor := t.TempDir()
	if err := os.Chmod(ancestor, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ancestor, 0o700) })
	child := filepath.Join(ancestor, "new-home")
	if _, err := PrepareHome(child); CodeOf(err) != CodeIntegrity {
		t.Fatalf("writable creation ancestor error = %v", err)
	}
	if _, err := os.Lstat(child); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("home created beneath writable ancestor: %v", err)
	}
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
	if chmodErr := os.Chmod(endpoint, 0o600); chmodErr != nil {
		t.Fatal(chmodErr)
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
	if chmodErr := os.Chmod(endpoint, 0o600); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	info, err := os.Lstat(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateOwnerOnlySocket(endpoint, foreignOwnerFileInfo{info}); CodeOf(err) != CodeUnauthorized {
		t.Fatalf("foreign socket owner metadata error = %v", err)
	}
}

func TestCleanupEndpointPropagatesParentPermissionFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory write permissions")
	}
	parent := shortCoverageUnixTempDir(t)
	endpoint := filepath.Join(parent, "broker.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(endpoint, 0o600); err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(parent, 0o700)
		_ = os.Remove(endpoint)
	})
	if err := cleanupEndpoint(endpoint); err == nil {
		t.Fatal("socket cleanup succeeded without parent write permission")
	}
}

func TestReadManifestPropagatesRealDescriptorExhaustion(t *testing.T) {
	home := t.TempDir()
	stateDir, err := prepareStateDirectory(home)
	if err != nil {
		t.Fatal(err)
	}
	manifest := validIPCManifest(t, stateDir)
	if err := writeManifest(stateDir, manifest); err != nil {
		t.Fatal(err)
	}
	var readErr error
	withIPCResourceLimit(t, unix.RLIMIT_NOFILE, func() {
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
	manifest := validIPCManifest(t, stateDir)
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)
	var writeErr error
	withIPCResourceLimit(t, unix.RLIMIT_FSIZE, func() {
		writeErr = writeManifest(stateDir, manifest)
	})
	if writeErr == nil || !strings.Contains(writeErr.Error(), "write database broker manifest") {
		t.Fatalf("manifest file-size limit error = %v", writeErr)
	}
}

func withIPCResourceLimit(t *testing.T, resource int, run func()) {
	t.Helper()
	var original unix.Rlimit
	if err := unix.Getrlimit(resource, &original); err != nil {
		t.Skipf("resource limit %d is unavailable: %v", resource, err)
	}
	restricted := original
	restricted.Cur = 0
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

func validIPCManifest(t *testing.T, stateDir string) Manifest {
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
