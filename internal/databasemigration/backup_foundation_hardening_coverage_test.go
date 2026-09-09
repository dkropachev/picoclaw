//go:build linux

//nolint:govet // Fault-path assertions intentionally use narrow error scopes.
package databasemigration

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func TestFoundationHardeningMountFailureCleanup(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "child"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := exactBackupMountIdentity{mechanism: "test-mount", value: [2]uint64{41}}
	canary := errors.New("mount identity canary")

	t.Run("closed root", func(t *testing.T) {
		root, err := os.OpenRoot(directory)
		if err != nil {
			t.Fatal(err)
		}
		if err := root.Close(); err != nil {
			t.Fatal(err)
		}
		file, err := openExactBackupChildByMountIdentity(
			root,
			"child",
			func(*os.File) (exactBackupMountIdentity, error) {
				return identity, nil
			},
		)
		if file != nil || err == nil {
			t.Fatalf("closed-root mount open = %#v, %v", file, err)
		}
	})

	for _, test := range []struct {
		name      string
		failCall  int
		wantCalls int
	}{
		{name: "root identity", failCall: 1, wantCalls: 1},
		{name: "child identity", failCall: 2, wantCalls: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			calls := 0
			file, err := openExactBackupChildByMountIdentity(
				root,
				"child",
				func(*os.File) (exactBackupMountIdentity, error) {
					calls++
					if calls == test.failCall {
						return exactBackupMountIdentity{}, canary
					}
					return identity, nil
				},
			)
			if file != nil || !errors.Is(err, canary) || calls != test.wantCalls {
				t.Fatalf("faulted mount open = %#v, calls=%d, %v", file, calls, err)
			}
		})
	}

	t.Run("result closes when root descriptor close fails", func(t *testing.T) {
		root, err := os.OpenRoot(directory)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		calls := 0
		var child *os.File
		file, err := openExactBackupChildByMountIdentity(
			root,
			"child",
			func(opened *os.File) (exactBackupMountIdentity, error) {
				calls++
				if calls == 1 {
					if err := opened.Close(); err != nil {
						t.Fatal(err)
					}
				} else {
					child = opened
				}
				return identity, nil
			},
		)
		if file != nil || err == nil || child == nil {
			t.Fatalf("root-close failure = %#v, child=%#v, %v", file, child, err)
		}
		if _, statErr := child.Stat(); !errors.Is(statErr, os.ErrClosed) {
			t.Fatalf("result descriptor remained open: %v", statErr)
		}
	})
}

func TestFoundationHardeningMountRootAndLinuxDescriptorFailures(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing", "root")
	if err := validateExactBackupRootMount(missing, nil); err == nil ||
		!strings.Contains(err.Error(), "root parent") {
		t.Fatalf("missing mount parent validation = %v", err)
	}

	first := t.TempDir()
	second := t.TempDir()
	rootFile, err := os.Open(second)
	if err != nil {
		t.Fatal(err)
	}
	defer rootFile.Close()
	if err := validateExactBackupRootMount(first, rootFile); err == nil ||
		!strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("mismatched mount root validation = %v", err)
	}

	if identity, err := linuxExactBackupMountIdentity(nil); identity.valid() ||
		!errors.Is(err, errExactBackupMountUnknown) {
		t.Fatalf("nil Linux mount identity = %#v, %v", identity, err)
	}
	closed, err := os.Open(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if identity, err := linuxExactBackupMountIdentity(closed); identity.valid() ||
		!errors.Is(err, errExactBackupMountUnknown) {
		t.Fatalf("closed Linux mount identity = %#v, %v", identity, err)
	}
	if err := validateExactBackupOpenedMount(nil, closed); !errors.Is(err, errExactBackupMountUnknown) {
		t.Fatalf("invalid opened mount validation = %v", err)
	}

	closedRoot, err := os.OpenRoot(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := closedRoot.Close(); err != nil {
		t.Fatal(err)
	}
	if file, err := openExactBackupChild(closedRoot, "."); file != nil || err == nil {
		if file != nil {
			_ = file.Close()
		}
		t.Fatalf("closed exact root open = %#v, %v", file, err)
	}
}

func TestFoundationHardeningRemainingSafeBoundaries(t *testing.T) {
	filesystem, err := os.OpenRoot(string(os.PathSeparator))
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close()
	file, err := openExactBackupChild(filesystem, filepath.Join("proc", "self"))
	if file != nil {
		_ = file.Close()
	}
	if !errors.Is(err, errExactBackupMountBoundary) {
		t.Fatalf("proc mount transition = %#v, %v", file, err)
	}

	root := t.TempDir()
	if !backupRemovalPlanHasDescendant(
		map[fileidentity.Identity]string{{}: filepath.Join(root, "child")}, root,
	) {
		t.Fatal("captured descendant was not detected")
	}
	realParent := t.TempDir()
	tree := filepath.Join(realParent, "tree")
	if err := os.Mkdir(tree, 0o700); err != nil {
		t.Fatal(err)
	}
	identity := foundationHardeningIdentity(t, tree, fileidentity.ObjectTypeDirectory)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realParent, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := removePinnedBackupTreeIdentity(filepath.Join(alias, "tree"), identity); err == nil {
		t.Fatal("removal through a symlinked ancestor succeeded")
	}
	if info, err := os.Lstat(tree); err != nil || !info.IsDir() {
		t.Fatalf("rejected removal changed tree = %#v, %v", info, err)
	}
}

func TestFoundationHardeningEmptyRollbackPreconditions(t *testing.T) {
	base := t.TempDir()
	reference := filepath.Join(base, "reference")
	if err := os.Mkdir(reference, 0o700); err != nil {
		t.Fatal(err)
	}
	referenceIdentity := foundationHardeningIdentity(t, reference, fileidentity.ObjectTypeDirectory)

	for _, test := range []struct {
		name     string
		path     string
		expected fileidentity.Identity
	}{
		{name: "relative path", path: "relative", expected: referenceIdentity},
		{name: "invalid identity", path: reference},
		{name: "missing parent", path: filepath.Join(base, "missing", "child"), expected: referenceIdentity},
		{name: "missing target", path: filepath.Join(base, "absent"), expected: referenceIdentity},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := removePinnedEmptyBackupDirectoryIdentity(test.path, test.expected); err == nil {
				t.Fatal("unsafe empty-directory rollback succeeded")
			}
		})
	}

	regular := filepath.Join(base, "regular")
	if err := os.WriteFile(regular, []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	regularIdentity := foundationHardeningIdentity(t, regular, fileidentity.ObjectTypeRegular)
	if err := removePinnedEmptyBackupDirectoryIdentity(regular, regularIdentity); err == nil ||
		!strings.Contains(err.Error(), "target changed") {
		t.Fatalf("regular-file rollback = %v", err)
	}
	if payload, err := os.ReadFile(regular); err != nil || string(payload) != "retain" {
		t.Fatalf("rejected rollback changed regular file = %q, %v", payload, err)
	}

	if err := requireEmptyBackupRemovalRoot(reference, nil, referenceIdentity); err == nil {
		t.Fatal("nil empty-directory root was accepted")
	}
}

func TestFoundationHardeningPinnedRemovalOpenFailures(t *testing.T) {
	base := t.TempDir()
	filePath := filepath.Join(base, "unreadable")
	if err := os.WriteFile(filePath, []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileIdentity := foundationHardeningIdentity(t, filePath, fileidentity.ObjectTypeRegular)
	if err := os.Chmod(filePath, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filePath, 0o600) })
	if probe, err := os.Open(filePath); err == nil {
		_ = probe.Close()
		t.Skip("process can bypass file read permissions")
	}
	if err := removePinnedBackupFile(filePath, fileIdentity); err == nil {
		t.Fatal("pinned removal opened an unreadable file")
	}
}

func TestFoundationHardeningPinnedChildRootRejectsMismatches(t *testing.T) {
	base := t.TempDir()
	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")
	for _, path := range []string{first, second} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	parent, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	firstFile, err := parent.Open("first")
	if err != nil {
		t.Fatal(err)
	}
	defer firstFile.Close()

	for _, test := range []struct {
		name   string
		parent *os.Root
		leaf   string
		opened *os.File
	}{
		{name: "nil parent", leaf: "first", opened: firstFile},
		{name: "nil opened", parent: parent, leaf: "first"},
		{name: "invalid leaf", parent: parent, leaf: "../first", opened: firstFile},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, err := openBackupRootFromPinnedChild(test.parent, test.leaf, test.opened)
			if root != nil {
				_ = root.Close()
			}
			if err == nil {
				t.Fatal("invalid pinned child root succeeded")
			}
		})
	}

	regularPath := filepath.Join(base, "regular")
	if err := os.WriteFile(regularPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	regular, err := parent.Open("regular")
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	if root, err := openBackupRootFromPinnedChild(parent, "regular", regular); root != nil || err == nil {
		if root != nil {
			_ = root.Close()
		}
		t.Fatalf("regular pinned child root = %#v, %v", root, err)
	}

	root, err := openBackupRootFromPinnedChild(parent, "second", firstFile)
	if root != nil {
		_ = root.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "changed while opening") {
		t.Fatalf("mismatched pinned child root = %#v, %v", root, err)
	}
}

func TestFoundationHardeningInventoryRejectsPreflightAndRemovalDrift(t *testing.T) {
	t.Run("duplicate preflight identity", func(t *testing.T) {
		rootPath := t.TempDir()
		childPath := filepath.Join(rootPath, "child")
		if err := os.WriteFile(childPath, []byte("retain"), 0o600); err != nil {
			t.Fatal(err)
		}
		root := foundationHardeningRoot(t, rootPath)
		rootIdentity := foundationHardeningIdentity(t, rootPath, fileidentity.ObjectTypeDirectory)
		childIdentity := foundationHardeningIdentity(t, childPath, fileidentity.ObjectTypeRegular)
		identities := map[fileidentity.Identity]string{
			rootIdentity:  rootPath,
			childIdentity: filepath.Join(rootPath, "previous-name"),
		}
		if err := validatePinnedBackupTreeInventory(
			rootPath, root, rootIdentity, identities, new(int),
		); err == nil || !strings.Contains(err.Error(), "physically alias") {
			t.Fatalf("duplicate preflight identity = %v", err)
		}
	})

	t.Run("bound entry limit", func(t *testing.T) {
		rootPath, root, rootIdentity := foundationHardeningRemovalRoot(t)
		if err := root.WriteFile("child", []byte("retain"), 0o600); err != nil {
			t.Fatal(err)
		}
		entries := backupMaxEntries
		if err := removePinnedBackupTreeContentsBound(
			rootPath, root, rootIdentity,
			map[fileidentity.Identity]string{rootIdentity: rootPath},
			&entries, defaultBackupRemovalOps(),
		); err == nil || !strings.Contains(err.Error(), "entry limit") {
			t.Fatalf("bound entry-limit removal = %v", err)
		}
	})

	t.Run("bound symlink", func(t *testing.T) {
		rootPath, root, rootIdentity := foundationHardeningRemovalRoot(t)
		if err := os.Symlink("missing", filepath.Join(rootPath, "link")); err != nil {
			t.Skipf("create symlink: %v", err)
		}
		if err := removePinnedBackupTreeContentsBound(
			rootPath, root, rootIdentity,
			map[fileidentity.Identity]string{rootIdentity: rootPath},
			new(int), defaultBackupRemovalOps(),
		); err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("bound symlink removal = %v", err)
		}
	})

	t.Run("bound unsafe object", func(t *testing.T) {
		rootPath, root, rootIdentity := foundationHardeningRemovalRoot(t)
		if err := unix.Mkfifo(filepath.Join(rootPath, "fifo"), 0o600); err != nil {
			t.Skipf("create FIFO: %v", err)
		}
		if err := removePinnedBackupTreeContentsBound(
			rootPath, root, rootIdentity,
			map[fileidentity.Identity]string{rootIdentity: rootPath},
			new(int), defaultBackupRemovalOps(),
		); err == nil || !strings.Contains(err.Error(), "changed while opening") {
			t.Fatalf("bound FIFO removal = %v", err)
		}
	})
}

func TestFoundationHardeningDirectoryPermissionFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses Unix directory permissions")
	}

	t.Run("empty rollback cannot open child root", func(t *testing.T) {
		base := t.TempDir()
		path := filepath.Join(base, "empty")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		identity := foundationHardeningIdentity(t, path, fileidentity.ObjectTypeDirectory)
		if err := os.Chmod(path, 0o400); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
		if err := removePinnedEmptyBackupDirectoryIdentity(path, identity); err == nil {
			t.Fatal("rollback traversed a directory without search permission")
		}
	})

	t.Run("empty rollback cannot quarantine", func(t *testing.T) {
		base := t.TempDir()
		path := filepath.Join(base, "empty")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		identity := foundationHardeningIdentity(t, path, fileidentity.ObjectTypeDirectory)
		if err := os.Chmod(base, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(base, 0o700) })
		if err := removePinnedEmptyBackupDirectoryIdentity(path, identity); err == nil {
			t.Fatal("rollback quarantined through a non-writable parent")
		}
		if info, err := os.Lstat(path); err != nil || !info.IsDir() {
			t.Fatalf("failed rollback changed target = %#v, %v", info, err)
		}
	})

	t.Run("tree removal cannot open exact target", func(t *testing.T) {
		base := t.TempDir()
		path := filepath.Join(base, "tree")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		identity := foundationHardeningIdentity(t, path, fileidentity.ObjectTypeDirectory)
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
		if err := removePinnedBackupTreeIdentity(path, identity); err == nil {
			t.Fatal("tree removal opened an inaccessible exact target")
		}
	})

	t.Run("tree removal cannot open recursive root", func(t *testing.T) {
		base := t.TempDir()
		path := filepath.Join(base, "tree")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		identity := foundationHardeningIdentity(t, path, fileidentity.ObjectTypeDirectory)
		if err := os.Chmod(path, 0o400); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
		if err := removePinnedBackupTreeIdentity(path, identity); err == nil {
			t.Fatal("tree removal opened a recursive root without search permission")
		}
	})
}

func TestFoundationHardeningLinuxMountIdentityFallbacks(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	for _, test := range []struct {
		name          string
		onlyUniqueBit bool
		wantMechanism string
	}{
		{name: "statx unavailable", wantMechanism: "linux-fdinfo-mount"},
		{name: "unique ID unavailable", onlyUniqueBit: true, wantMechanism: "linux-statx-mount"},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity, identityErr, filterErr := foundationHardeningMountIdentityWithStatxEINVAL(
				directory, test.onlyUniqueBit,
			)
			if filterErr != nil {
				t.Skipf("install thread-local statx filter: %v", filterErr)
			}
			if identityErr != nil || identity.mechanism != test.wantMechanism || !identity.valid() {
				t.Fatalf("filtered mount identity = %#v, %v", identity, identityErr)
			}
		})
	}
}

func foundationHardeningMountIdentityWithStatxEINVAL(
	file *os.File,
	onlyUniqueBit bool,
) (exactBackupMountIdentity, error, error) {
	type result struct {
		identity    exactBackupMountIdentity
		identityErr error
		filterErr   error
	}
	results := make(chan result, 1)
	go func() {
		// The filter is thread-local. Keep this goroutine pinned and terminate
		// its OS thread with Goexit so the restricted thread never reenters the
		// runtime pool and affects another test.
		runtime.LockOSThread()
		if err := foundationHardeningInstallStatxFilter(onlyUniqueBit); err != nil {
			results <- result{filterErr: err}
			runtime.Goexit()
		}
		identity, err := linuxExactBackupMountIdentity(file)
		results <- result{identity: identity, identityErr: err}
		runtime.Goexit()
	}()
	got := <-results
	return got.identity, got.identityErr, got.filterErr
}

func foundationHardeningInstallStatxFilter(onlyUniqueBit bool) error {
	load := uint16(unix.BPF_LD | unix.BPF_W | unix.BPF_ABS)
	jump := uint16(unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K)
	allow := unix.SockFilter{Code: uint16(unix.BPF_RET | unix.BPF_K), K: unix.SECCOMP_RET_ALLOW}
	deny := unix.SockFilter{
		Code: uint16(unix.BPF_RET | unix.BPF_K),
		K:    uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.EINVAL),
	}
	filters := []unix.SockFilter{
		{Code: load, K: 0},
		{Code: jump, Jf: 1, K: uint32(unix.SYS_STATX)},
		deny,
		allow,
	}
	if onlyUniqueBit {
		maskOffset := uint32(40) // seccomp_data.args[3], low word on little-endian Linux.
		switch runtime.GOARCH {
		case "mips", "mips64", "ppc64", "s390x":
			maskOffset += 4
		}
		filters = []unix.SockFilter{
			{Code: load, K: 0},
			{Code: jump, Jf: 3, K: uint32(unix.SYS_STATX)},
			{Code: load, K: maskOffset},
			{Code: uint16(unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K), Jf: 1, K: unix.STATX_MNT_ID_UNIQUE},
			deny,
			allow,
		}
	}
	program := unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	return unix.Prctl(
		unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER,
		uintptr(unsafe.Pointer(&program)), 0, 0,
	)
}

func foundationHardeningRemovalRoot(
	t *testing.T,
) (string, *os.Root, fileidentity.Identity) {
	t.Helper()
	rootPath := t.TempDir()
	root := foundationHardeningRoot(t, rootPath)
	return rootPath, root, foundationHardeningIdentity(t, rootPath, fileidentity.ObjectTypeDirectory)
}

func foundationHardeningRoot(t *testing.T, path string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func foundationHardeningIdentity(
	t *testing.T,
	path string,
	want fileidentity.ObjectType,
) fileidentity.Identity {
	t.Helper()
	identity, objectType, exists, err := fileidentity.ExistingWithType(path)
	if err != nil || !exists || objectType != want {
		t.Fatalf("identity for %q = %#v, %v, %t, %v", path, identity, objectType, exists, err)
	}
	return identity
}
