//go:build unix && !aix

package databasemigration

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func closeoutBackupParent(t *testing.T) string {
	t.Helper()
	parent := filepath.Join(t.TempDir(), "backups")
	if err := ensurePrivateBackupDirectory(parent); err != nil {
		t.Fatal(err)
	}
	return parent
}

func TestPinnedBackupPathDefensiveBranches(t *testing.T) {
	root := t.TempDir()
	if err := secureAndValidateBackupDirectory(root); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file")
	directory := filepath.Join(root, "directory")
	writeMigrationFile(t, file, []byte("file"))
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "symlink")
	if err := os.Symlink(file, symlink); err != nil {
		symlink = ""
	}
	realParent := filepath.Join(root, "real-parent")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkParent := filepath.Join(root, "linked-parent")
	if err := os.Symlink(realParent, symlinkParent); err != nil {
		symlinkParent = ""
	} else {
		writeMigrationFile(t, filepath.Join(realParent, "file"), []byte("file"))
	}
	for _, test := range []struct {
		name      string
		path      string
		directory bool
	}{
		{name: "relative", path: "relative"},
		{name: "root leaf", path: string(os.PathSeparator)},
		{name: "missing parent", path: filepath.Join(root, "missing", "file")},
		{name: "file parent", path: filepath.Join(file, "child")},
		{name: "missing target", path: filepath.Join(root, "missing")},
		{name: "directory as file", path: directory},
		{name: "file as directory", path: file, directory: true},
		{name: "symlink", path: symlink},
		{name: "symlink ancestor", path: filepath.Join(symlinkParent, "file")},
	} {
		if test.path == "" {
			continue
		}
		t.Run("open "+test.name, func(t *testing.T) {
			opened, _, err := openPinnedBackupPath(test.path, test.directory)
			if opened != nil {
				_ = opened.Close()
			}
			if err == nil {
				t.Fatal("unsafe pinned open succeeded")
			}
		})
	}
	opened, _, openErr := openPinnedBackupPath(directory, true)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"relative", string(os.PathSeparator), filepath.Join(root, "missing", "new"),
		filepath.Join(file, "new"), filepath.Join(symlinkParent, "new"),
	} {
		if created, err := createPinnedBackupFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err == nil {
			_ = created.Close()
			t.Fatalf("unsafe pinned create succeeded: %q", path)
		}
	}
	if created, err := createPinnedBackupFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err == nil {
		_ = created.Close()
		t.Fatal("pinned create replaced existing file")
	}
	createdDirectory := filepath.Join(root, "created-directory")
	if err := createPinnedBackupDirectory(createdDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(createdDirectory); err != nil || !info.IsDir() {
		t.Fatalf("pinned directory = %#v, %v", info, err)
	}
	for _, path := range []string{
		"relative", string(os.PathSeparator), filepath.Join(file, "child"), createdDirectory,
	} {
		if err := createPinnedBackupDirectory(path, 0o700); err == nil {
			t.Fatalf("unsafe pinned directory creation succeeded: %q", path)
		}
	}
}

func TestExclusiveBackupControlAndPinnedRemoval(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "control")
	if err := writePrivateBackupFileExclusive(path, []byte("control"), 0o600); err != nil {
		t.Fatal(err)
	}
	if payload, err := os.ReadFile(path); err != nil || string(payload) != "control" {
		t.Fatalf("exclusive control payload = %q, %v", payload, err)
	}
	if err := writePrivateBackupFileExclusive(path, []byte("replacement"), 0o600); err == nil {
		t.Fatal("exclusive control writer replaced existing file")
	}
	identity, exists, err := fileidentity.Existing(path)
	if err != nil || !exists {
		t.Fatal(err)
	}
	replacement := filepath.Join(root, "replacement")
	writeMigrationFile(t, replacement, []byte("replacement"))
	if err := removePinnedBackupFile(replacement, identity); err == nil {
		t.Fatal("pinned file removal accepted replaced identity")
	}
	if _, err := os.Lstat(replacement); err != nil {
		t.Fatalf("replacement file was removed: %v", err)
	}
	if err := removePinnedBackupFile(filepath.Join(root, "missing-file"), identity); err != nil {
		t.Fatalf("missing pinned file removal = %v", err)
	}
	if err := writePrivateBackupFileExclusive("relative", nil, 0o600); err == nil {
		t.Fatal("exclusive control writer accepted relative path")
	}

	tree := filepath.Join(root, "tree")
	writeMigrationFile(t, filepath.Join(tree, "nested", "file"), []byte("file"))
	if err := removePinnedBackupTree(tree); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pinned tree remains: %v", err)
	}
	if err := removePinnedBackupTree(tree); err != nil {
		t.Fatalf("missing pinned tree removal = %v", err)
	}
	if err := removePinnedBackupTree("relative"); err == nil {
		t.Fatal("pinned removal accepted relative path")
	}
	file := filepath.Join(root, "file")
	writeMigrationFile(t, file, nil)
	if err := removePinnedBackupTree(file); err == nil {
		t.Fatal("pinned tree removal accepted regular file")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(root, link); err == nil {
		if err := removePinnedBackupTree(link); err == nil {
			t.Fatal("pinned tree removal accepted symlink")
		}
	}
}

func TestBackupControlWriterFailureCleanupPhases(t *testing.T) {
	canary := errors.New("backup control writer canary")
	for _, test := range []struct {
		name   string
		mutate func(*backupControlWriteOps, os.FileInfo)
	}{
		{name: "stat", mutate: func(ops *backupControlWriteOps, _ os.FileInfo) {
			ops.stat = func(*os.File) (os.FileInfo, error) { return nil, canary }
		}},
		{name: "write", mutate: func(ops *backupControlWriteOps, _ os.FileInfo) {
			ops.write = func(*os.File, []byte) (int, error) { return 0, canary }
		}},
		{name: "short write", mutate: func(ops *backupControlWriteOps, _ os.FileInfo) {
			ops.write = func(*os.File, []byte) (int, error) { return 1, nil }
		}},
		{name: "chmod", mutate: func(ops *backupControlWriteOps, _ os.FileInfo) {
			ops.chmod = func(*os.File, os.FileMode) error { return canary }
		}},
		{name: "sync", mutate: func(ops *backupControlWriteOps, _ os.FileInfo) {
			ops.sync = func(*os.File) error { return canary }
		}},
		{name: "close", mutate: func(ops *backupControlWriteOps, _ os.FileInfo) {
			ops.close = func(*os.File) error { return canary }
		}},
		{name: "secure", mutate: func(ops *backupControlWriteOps, _ os.FileInfo) {
			ops.secure = func(string) error { return canary }
		}},
		{name: "reinspect", mutate: func(ops *backupControlWriteOps, _ os.FileInfo) {
			ops.lstat = func(string) (os.FileInfo, error) { return nil, canary }
		}},
		{name: "identity change", mutate: func(ops *backupControlWriteOps, _ os.FileInfo) {
			other := filepath.Join(t.TempDir(), "other")
			if err := os.WriteFile(other, []byte("other"), 0o600); err != nil {
				t.Fatal(err)
			}
			otherInfo, err := os.Lstat(other)
			if err != nil {
				t.Fatal(err)
			}
			otherIdentity, otherType, exists, err := fileidentity.ExistingWithType(other)
			if err != nil || !exists {
				t.Fatal(err)
			}
			original := ops.identity
			calls := 0
			ops.identity = func(path string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
				calls++
				if calls > 1 {
					return otherIdentity, otherType, true, nil
				}
				return original(path)
			}
			ops.lstat = func(string) (os.FileInfo, error) { return otherInfo, nil }
		}},
		{name: "final directory sync", mutate: func(ops *backupControlWriteOps, _ os.FileInfo) {
			calls := 0
			ops.syncDir = func(string) error {
				calls++
				if calls == 1 {
					return canary
				}
				return nil
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "status.json")
			backing := filepath.Join(root, "backing")
			if err := os.WriteFile(backing, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(backing, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = file.Close() })
			created, err := file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			createdIdentity, createdType, err := fileidentity.Opened(file)
			if err != nil {
				t.Fatal(err)
			}
			closeCalls, removeCalls, syncCalls := 0, 0, 0
			ops := backupControlWriteOps{
				create: func(string, int, os.FileMode) (*os.File, error) { return file, nil },
				stat:   func(*os.File) (os.FileInfo, error) { return created, nil },
				identity: func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
					return createdIdentity, createdType, true, nil
				},
				opened: func(*os.File) (fileidentity.Identity, fileidentity.ObjectType, error) {
					return createdIdentity, createdType, nil
				},
				write:   func(*os.File, []byte) (int, error) { return len("payload"), nil },
				chmod:   func(*os.File, os.FileMode) error { return nil },
				sync:    func(*os.File) error { return nil },
				close:   func(*os.File) error { return nil },
				secure:  func(string) error { return nil },
				lstat:   func(string) (os.FileInfo, error) { return created, nil },
				remove:  func(string, fileidentity.Identity) error { return nil },
				syncDir: func(string) error { return nil },
			}
			test.mutate(&ops, created)
			closeOperation := ops.close
			ops.close = func(file *os.File) error {
				closeCalls++
				return closeOperation(file)
			}
			removeOperation := ops.remove
			ops.remove = func(path string, identity fileidentity.Identity) error {
				removeCalls++
				return removeOperation(path, identity)
			}
			syncOperation := ops.syncDir
			ops.syncDir = func(path string) error {
				syncCalls++
				return syncOperation(path)
			}
			err = writePrivateBackupFileExclusiveWithOps(path, []byte("payload"), 0o600, ops)
			if err == nil || test.name != "identity change" &&
				!errors.Is(err, canary) && !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("writer fault = %v", err)
			}
			wantRemove := 1
			wantSyncAtLeast := 1
			if removeCalls != wantRemove || syncCalls < wantSyncAtLeast || closeCalls == 0 {
				t.Fatalf(
					"writer cleanup close=%d remove=%d sync=%d",
					closeCalls, removeCalls, syncCalls,
				)
			}
		})
	}
}

func TestBackupControlWriterSuccessRetainsExactFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "status.json")
	if err := writePrivateBackupFileExclusive(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil || string(payload) != "payload" {
		t.Fatalf("retained control file = %q, %v", payload, err)
	}
}

func TestPinnedBackupRemovalAndParentIdentityBoundaries(t *testing.T) {
	if err := removePinnedBackupFile("relative", fileidentity.Identity{}); err == nil {
		t.Fatal("invalid pinned file removal succeeded")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	missing := filepath.Join(root, "missing")
	if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, exists, err := fileidentity.Existing(source)
	if err != nil || !exists {
		t.Fatal(err)
	}
	if err := removePinnedBackupFile(missing, identity); err != nil {
		t.Fatalf("missing pinned file removal = %v", err)
	}
	if err := removePinnedBackupTree("relative"); err == nil {
		t.Fatal("relative pinned tree removal succeeded")
	}
	if err := removePinnedBackupTree(filepath.Join(root, "missing-tree")); err != nil {
		t.Fatalf("missing pinned tree removal = %v", err)
	}
}

func TestCopyBackupFileAdditionalFaultAndCleanupBoundaries(t *testing.T) {
	canary := errors.New("copy closeout canary")
	newFixture := func(t *testing.T) (string, string, *backupBudget) {
		t.Helper()
		root := closeoutBackupParent(t)
		source := filepath.Join(t.TempDir(), "source")
		writeMigrationFile(t, source, []byte("payload"))
		return root, source, newBackupBudget()
	}

	t.Run("nil context", func(t *testing.T) {
		root, source, budget := newFixture(t)
		record, err := copyBackupFileWithOps(
			nil, root, "global/auth", "legacy", source, "payload", budget,
			defaultBackupCopyOps(),
		)
		if err != nil || record.Size != int64(len("payload")) || record.SHA256 == "" {
			t.Fatalf("nil-context copy = %#v, %v", record, err)
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *backupCopyOps, *backupBudget, string, string)
	}{
		{name: "archive budget", mutate: func(_ *testing.T, _ *backupCopyOps, budget *backupBudget, _, _ string) {
			budget.archiveEntries = budget.maxEntries
		}},
		{name: "file budget", mutate: func(_ *testing.T, _ *backupCopyOps, budget *backupBudget, _, _ string) {
			budget.maxFileBytes = 1
		}},
		{name: "output stat", mutate: func(_ *testing.T, ops *backupCopyOps, _ *backupBudget, _, _ string) {
			original := ops.stat
			calls := 0
			ops.stat = func(file *os.File) (os.FileInfo, error) {
				calls++
				if calls == 2 {
					return nil, canary
				}
				return original(file)
			}
		}},
		{name: "input close", mutate: func(t *testing.T, ops *backupCopyOps, _ *backupBudget, _, _ string) {
			originalOpen := ops.openInput
			var input *os.File
			ops.openInput = func(path string) (*os.File, error) {
				var err error
				input, err = originalOpen(path)
				if input != nil {
					t.Cleanup(func() { _ = input.Close() })
				}
				return input, err
			}
			originalClose := ops.close
			ops.close = func(file *os.File) error {
				if file == input {
					return canary
				}
				return originalClose(file)
			}
		}},
		{name: "published open", mutate: func(_ *testing.T, ops *backupCopyOps, _ *backupBudget, root, _ string) {
			ops.secure = func(path string) error {
				if !strings.HasPrefix(path, root) {
					return errors.New("copy destination escaped root")
				}
				return os.Remove(path)
			}
		}},
		{name: "final directory sync", mutate: func(_ *testing.T, ops *backupCopyOps, _ *backupBudget, _, _ string) {
			ops.syncDir = func(string) error { return canary }
		}},
		{name: "relative result", mutate: func(_ *testing.T, ops *backupCopyOps, _ *backupBudget, _, _ string) {
			ops.rel = func(string, string) (string, error) { return "", canary }
		}},
		{name: "copy aliases source", mutate: func(t *testing.T, ops *backupCopyOps, _ *backupBudget, _, source string) {
			sourceInfo, err := os.Lstat(source)
			if err != nil {
				t.Fatal(err)
			}
			originalLstat := ops.lstat
			ops.lstat = func(path string) (os.FileInfo, error) {
				if path == source {
					return sourceInfo, nil
				}
				return originalLstat(path)
			}
			original := ops.openOutput
			ops.openOutput = func(path string, _ int, _ os.FileMode) (*os.File, error) {
				if err := os.Link(source, path); err != nil {
					t.Skipf("hardlinks unavailable: %v", err)
					return original(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
				}
				return os.OpenFile(path, os.O_WRONLY, 0)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, source, budget := newFixture(t)
			ops := defaultBackupCopyOps()
			test.mutate(t, &ops, budget, root, source)
			record, err := copyBackupFileWithOps(
				t.Context(), root, "global/auth", "legacy", source, "payload", budget, ops,
			)
			if err == nil || record.StoreID != "" {
				t.Fatalf("copy fault = %#v, %v", record, err)
			}
		})
	}

	root, source, budget := newFixture(t)
	if record, err := copyBackupFileWithOps(
		t.Context(), "relative", "global/auth", "legacy", source, "payload", budget,
		defaultBackupCopyOps(),
	); err == nil || record.StoreID != "" {
		t.Fatalf("relative backup root copy = %#v, %v", record, err)
	}
	if record, err := copyBackupFileWithOps(
		t.Context(), root, "global/auth", "legacy", "relative", "payload", budget,
		defaultBackupCopyOps(),
	); err == nil || record.StoreID != "" {
		t.Fatalf("relative source copy = %#v, %v", record, err)
	}
}

func TestBackupManifestComparatorAndProvenanceCloseout(t *testing.T) {
	left := BackupFileManifest{
		StoreID: "global/auth", Role: "legacy", LegacyRoot: 0,
		Source: filepath.Join(t.TempDir(), "a"),
	}
	right := left
	right.Source = filepath.Join(filepath.Dir(left.Source), "b")
	if !backupManifestFileLess(left, right) || backupManifestFileLess(right, left) {
		t.Fatal("manifest source ordering is not deterministic")
	}
	if err := validateBackupRecordProvenance(
		BackupStoreManifest{StoreID: "global/auth"},
		BackupFileManifest{StoreID: "global/auth", Role: "invalid"},
	); err == nil {
		t.Fatal("unknown provenance role succeeded")
	}
}

func TestBackupReadersRejectNoProgress(t *testing.T) {
	if entries, err := readBackupDirectoryBatch(noProgressDirectoryReader{}); len(entries) != 0 || err == nil {
		t.Fatalf("readBackupDirectoryBatch() = %d entries, %v", len(entries), err)
	}
	if written, err := copyWithContext(
		context.Background(), io.Discard, sha256.New(), noProgressReader{}, 1,
	); written != 0 || err == nil {
		t.Fatalf("copyWithContext(no progress) = %d, %v", written, err)
	}
}

type noProgressDirectoryReader struct{}

func (noProgressDirectoryReader) ReadDir(int) ([]os.DirEntry, error) { return nil, nil }

type noProgressReader struct{}

func (noProgressReader) Read([]byte) (int, error) { return 0, nil }

func TestUnixLegacyTraversalRejectsIrregularAndUnreadableInputs(t *testing.T) {
	root := t.TempDir()
	pipe := filepath.Join(root, "legacy-pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := walkLegacyInputs(
		t.Context(), pipe, t.TempDir(), nil, newBackupBudget(), func(string) error { return nil },
	); err == nil {
		t.Fatal("named-pipe legacy root succeeded")
	}
	tree := filepath.Join(root, "tree")
	if err := os.Mkdir(tree, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(tree, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := walkLegacyInputs(
		t.Context(), tree, t.TempDir(), nil, newBackupBudget(), func(string) error { return nil },
	); err == nil {
		t.Fatal("named-pipe legacy tree member succeeded")
	}

	if os.Geteuid() == 0 {
		return
	}
	blocked := filepath.Join(root, "blocked")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	if err := walkLegacyDirectory(
		t.Context(), blocked, ".", t.TempDir(), nil, newBackupBudget(), func(string) error { return nil },
	); err == nil {
		t.Fatal("unreadable legacy directory succeeded")
	}
}

func TestUnixBackupCreationAndCopyPermissionFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses ordinary filesystem permissions")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	if err := ensurePrivateBackupDirectory(filepath.Join(parent, "child")); err == nil {
		t.Fatal("backup directory creation below non-writable parent succeeded")
	}

	sourceRoot := t.TempDir()
	source := filepath.Join(sourceRoot, "source")
	writeMigrationFile(t, source, []byte("payload"))
	if err := os.Chmod(source, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(source, 0o600) })
	if _, err := copyBackupFile(
		t.Context(), t.TempDir(), "global/auth", "legacy", source, "copy", newBackupBudget(),
	); err == nil {
		t.Fatal("unreadable backup source copied")
	}

	blockedRoot := filepath.Join(t.TempDir(), "root-file")
	writeMigrationFile(t, blockedRoot, nil)
	readable := filepath.Join(t.TempDir(), "readable")
	writeMigrationFile(t, readable, []byte("payload"))
	if _, err := copyBackupFile(
		t.Context(), blockedRoot, "global/auth", "legacy", readable,
		filepath.Join("nested", "copy"), newBackupBudget(),
	); err == nil {
		t.Fatal("copy below regular-file backup root succeeded")
	}
}

func TestUnixExistingSymlinkBackupAncestorIsRejected(t *testing.T) {
	base := t.TempDir()
	realDirectory := filepath.Join(base, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(realDirectory, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := validateBackupAncestors(filepath.Join(alias, "missing")); err == nil ||
		!strings.Contains(err.Error(), "unsafe ancestor") {
		t.Fatalf("symlink backup ancestor = %v", err)
	}
}
