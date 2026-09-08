//go:build unix && !aix

//nolint:govet // Failure-boundary assertions intentionally reuse narrow error names.
package sqliteprovider

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCoverageProviderRelativePathsFailWithoutWorkingDirectory(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	removed := filepath.Join(root, "removed")
	if err := os.Mkdir(removed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(removed); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(original); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	}()
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}

	if _, err := inspectedPoolKey("store.db"); err == nil {
		t.Fatal("inspected key succeeded without working directory")
	}
	if _, err := acquireProviderOpenLock("store.db"); err == nil {
		t.Fatal("provider path lock succeeded without working directory")
	}
	if database, err := OpenStore("store.db", time.Second); err == nil || database != nil {
		t.Fatalf("OpenStore without working directory = %#v, %v", database, err)
	}
	if _, err := DSN("store.db", time.Second); err == nil {
		t.Fatal("DSN succeeded without working directory")
	}
	if err := validateProviderAncestors("store.db"); err == nil {
		t.Fatal("ancestor validation succeeded without working directory")
	}
	if err := makeProviderDirectories("store", 0o700); err == nil {
		t.Fatal("directory creation succeeded without working directory")
	}
	if file, err := providerOpenFile("store.db", os.O_RDWR, 0o600); err == nil || file != nil {
		t.Fatalf("providerOpenFile without working directory = %#v, %v", file, err)
	}
}

func TestCoverageProviderUnixDirectoryFailureBoundaries(t *testing.T) {
	root := t.TempDir()
	fileAncestor := filepath.Join(root, "file")
	if err := os.WriteFile(fileAncestor, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := makeProviderDirectories(fileAncestor, 0o700); err == nil {
		t.Fatal("directory creation accepted a file target")
	}
	if err := makeProviderDirectories(filepath.Join(fileAncestor, "child"), 0o700); err == nil {
		t.Fatal("directory creation accepted a file ancestor")
	}

	overlong := filepath.Join("/tmp", strings.Repeat("x", 300))
	if err := makeProviderDirectories(overlong, 0o700); err == nil {
		t.Fatal("directory creation accepted an overlong component")
	}
	if err := validateProviderAncestors(filepath.Join(overlong, "store.db")); err == nil {
		t.Fatal("ancestor validation accepted an overlong component")
	}

	blockedOpen := filepath.Join(root, "blocked-open")
	if err := os.Mkdir(blockedOpen, 0); err != nil {
		t.Fatal(err)
	}
	if err := makeProviderDirectories(blockedOpen, 0o700); err == nil {
		t.Fatal("directory traversal opened a mode-zero component")
	}
	if err := os.Chmod(blockedOpen, 0o700); err != nil {
		t.Fatal(err)
	}

	blockedCreate := filepath.Join(root, "blocked-create")
	if err := os.Mkdir(blockedCreate, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := makeProviderDirectories(filepath.Join(blockedCreate, "child"), 0o700); err == nil {
		t.Fatal("directory creation ignored parent write permissions")
	}
	if err := os.Chmod(blockedCreate, 0o700); err != nil {
		t.Fatal(err)
	}

	unreadableCreated := filepath.Join(root, "unreadable-created")
	if err := makeProviderDirectories(unreadableCreated, 0o300); err == nil {
		t.Fatal("directory creation retained an unreadable new component")
	}
	if err := os.Chmod(unreadableCreated, 0o700); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}

	wide, err := os.MkdirTemp("/tmp", "pc-provider-wide-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chmod(wide, 0o700)
		_ = os.RemoveAll(wide)
	}()
	if err := os.Chmod(wide, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := makeProviderDirectories(filepath.Join(wide, "child"), 0o700); err == nil {
		t.Fatal("directory creation trusted a writable existing boundary")
	}
}

func TestCoverageProviderUnixRootComponentBranches(t *testing.T) {
	if err := makeProviderDirectories(string(os.PathSeparator), 0o700); err != nil {
		t.Fatal(err)
	}
	missingRootEntry := string(os.PathSeparator) + "picoclaw-provider-coverage-missing"
	_ = os.Remove(missingRootEntry)
	if file, err := providerOpenFile(missingRootEntry, os.O_RDWR, 0o600); err == nil || file != nil {
		t.Fatalf("missing root entry open = %#v, %v", file, err)
	}
}

func TestCoverageProviderUnixConcurrentDirectoryCreators(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "one", "two", "three")
	start := make(chan struct{})
	errorsSeen := make(chan error, 32)
	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			errorsSeen <- makeProviderDirectories(target, 0o700)
		}()
	}
	close(start)
	workers.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent directory creation = %v", err)
		}
	}
}

func TestCoverageProviderUnixOpenFileFailures(t *testing.T) {
	root := t.TempDir()
	fileAncestor := filepath.Join(root, "file")
	if err := os.WriteFile(fileAncestor, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if file, err := providerOpenFile(
		filepath.Join(fileAncestor, "child.db"), os.O_RDWR|os.O_CREATE, 0o600,
	); err == nil || file != nil {
		t.Fatalf("open beneath file ancestor = %#v, %v", file, err)
	}
	if file, err := providerOpenFile(root, os.O_RDWR, 0o600); err == nil || file != nil {
		t.Fatalf("open directory as store = %#v, %v", file, err)
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if file, err := providerOpenFile(link, os.O_RDWR, 0o600); err == nil || file != nil {
		t.Fatalf("open symlink as store = %#v, %v", file, err)
	}
}

func TestCoverageProviderUnixSecurityChmodAndReplacementFailures(t *testing.T) {
	procPath := "/proc/self/status"
	procInfo, err := os.Lstat(procPath)
	if err != nil {
		t.Skipf("procfs unavailable: %v", err)
	}
	procFile, err := os.Open(procPath)
	if err != nil {
		t.Skipf("procfs status unavailable: %v", err)
	}
	defer procFile.Close()
	stat, ok := procInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("procfs status lacks Unix identity")
	}
	if err := secureUnixProviderHandle(procPath, false, procInfo, procFile, stat.Uid); err == nil {
		t.Fatal("procfs file chmod unexpectedly succeeded")
	}

	root := t.TempDir()
	path := filepath.Join(root, "store.db")
	oldPath := filepath.Join(root, "store.old")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	expected, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := os.Rename(path, oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := secureUnixProviderHandle(path, false, expected, file, uint32(os.Geteuid())); err == nil {
		t.Fatal("security handle accepted replacement pathname")
	}

	if _, trusted := trustedProviderUnixDirectory(unix.Stat_t{
		Mode: unix.S_IFREG | 0o600, Uid: uint32(os.Geteuid()),
	}, false); trusted {
		t.Fatal("regular file trusted as directory")
	}
	foreignUID := uint32(os.Geteuid()) + 1
	if _, trusted := trustedProviderUnixDirectory(unix.Stat_t{
		Mode: unix.S_IFDIR | 0o700, Uid: foreignUID,
	}, false); trusted {
		t.Fatal("foreign directory trusted")
	}
}
