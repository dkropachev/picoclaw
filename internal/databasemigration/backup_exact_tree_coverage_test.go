package databasemigration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func TestExpectedBackupTreeCoverage(t *testing.T) {
	for _, test := range []struct {
		name  string
		files []BackupFileManifest
		want  string
	}{
		{name: "invalid relative", files: []BackupFileManifest{{Backup: "../escape"}}, want: "path is invalid"},
		{name: "control collision", files: []BackupFileManifest{{Backup: backupManifestName}}, want: "control or path collision"},
		{name: "file directory collision", files: []BackupFileManifest{{Backup: "parent"}, {Backup: "parent/child"}}, want: "file-directory collision"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := expectedBackupTree(BackupManifest{Files: test.files})
			parentTreeRequireError(t, err, test.want)
		})
	}
}

func TestExactBackupTreeTopLevelCoverage(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if identities, err := exactBackupTreeIdentities(nil, root, BackupManifest{}); err == nil || identities != nil ||
		!strings.Contains(err.Error(), "omits an inventory path") {
		t.Fatalf("incomplete exact tree = %#v, %v", identities, err)
	}
	if identities, err := exactBackupTreeIdentities(
		t.Context(), root, BackupManifest{Files: []BackupFileManifest{{Backup: "../escape"}}},
	); err == nil || identities != nil {
		t.Fatalf("invalid inventory exact tree = %#v, %v", identities, err)
	}

	regular := filepath.Join(t.TempDir(), "regular")
	writeMigrationFile(t, regular, []byte("regular"))
	if identities, err := exactBackupTreeIdentities(
		t.Context(),
		regular,
		BackupManifest{},
	); err == nil ||
		identities != nil {
		t.Fatalf("regular exact-tree root = %#v, %v", identities, err)
	}
	public := t.TempDir()
	if err := os.Chmod(public, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(public, 0o700) })
	if identities, err := exactBackupTreeIdentities(
		t.Context(),
		public,
		BackupManifest{},
	); err == nil ||
		identities != nil ||
		!strings.Contains(err.Error(), "validate database backup root") {
		t.Fatalf("public exact-tree root = %#v, %v", identities, err)
	}
}

func TestExactBackupDirectoryCoverage(t *testing.T) {
	t.Run("initial validation", func(t *testing.T) {
		rootPath := t.TempDir()
		if err := os.Chmod(rootPath, 0o700); err != nil {
			t.Fatal(err)
		}
		root := parentTreeRoot(t, rootPath)
		parentTreeRequireError(t, verifyExactBackupDirectory(
			canceledParentTreeContext(), rootPath, root, ".",
			map[string]backupTreeKind{".": backupTreeDirectory}, map[string]struct{}{},
			map[fileidentity.Identity]string{},
		), context.Canceled.Error())
		writeMigrationFile(t, filepath.Join(rootPath, "member"), []byte("member"))
		parentTreeRequireError(t, verifyExactBackupDirectory(
			&exactTreeActionContext{Context: t.Context(), allowed: 1, result: context.Canceled},
			rootPath, root, ".",
			map[string]backupTreeKind{".": backupTreeDirectory, "member": backupTreeFile},
			map[string]struct{}{".": {}}, map[fileidentity.Identity]string{},
		), context.Canceled.Error())
		if err := os.Chmod(rootPath, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(rootPath, 0o700) })
		parentTreeRequireError(t, verifyExactBackupDirectory(
			t.Context(), rootPath, root, ".",
			map[string]backupTreeKind{".": backupTreeDirectory}, map[string]struct{}{},
			map[fileidentity.Identity]string{},
		), "validate database backup tree directory")
	})

	for _, test := range []struct {
		name     string
		kind     backupTreeKind
		prepare  func(*testing.T, string)
		seen     func(string) map[string]struct{}
		expected func(string) map[string]backupTreeKind
		want     string
	}{
		{
			name: "entry limit", kind: backupTreeFile,
			prepare:  exactTreeWriteRegular,
			seen:     func(string) map[string]struct{} { return map[string]struct{}{".": {}} },
			expected: func(string) map[string]backupTreeKind { return map[string]backupTreeKind{".": backupTreeDirectory} },
			want:     "entry limit",
		},
		{
			name: "unexpected path", kind: backupTreeFile,
			prepare: exactTreeWriteRegular,
			expected: func(string) map[string]backupTreeKind {
				return map[string]backupTreeKind{".": backupTreeDirectory, "other": backupTreeFile}
			}, want: "unexpected path",
		},
		{
			name: "duplicate inventory path", kind: backupTreeFile,
			prepare: exactTreeWriteRegular,
			seen:    func(child string) map[string]struct{} { return map[string]struct{}{child: {}} },
			expected: func(child string) map[string]backupTreeKind {
				return map[string]backupTreeKind{".": backupTreeDirectory, child: backupTreeFile, "absent": backupTreeFile}
			}, want: "duplicate inventory path",
		},
		{
			name: "unsafe symlink", kind: backupTreeFile,
			prepare: func(t *testing.T, path string) {
				if err := os.Symlink("absent", path); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}, want: "object is unsafe",
		},
		{
			name: "directory wrong type", kind: backupTreeDirectory,
			prepare: exactTreeWriteRegular, want: "directory has wrong type",
		},
		{
			name: "file wrong type", kind: backupTreeFile,
			prepare: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}, want: "file has wrong type",
		},
		{
			name: "recursive directory rejected", kind: backupTreeDirectory,
			prepare: func(t *testing.T, path string) {
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			}, want: "validate database backup tree directory",
		},
		{
			name: "regular file rejected", kind: backupTreeFile,
			prepare: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("member"), 0o644); err != nil {
					t.Fatal(err)
				}
			}, want: "validate database backup inventory file",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			rootPath := t.TempDir()
			if err := os.Chmod(rootPath, 0o700); err != nil {
				t.Fatal(err)
			}
			child := "member"
			test.prepare(t, filepath.Join(rootPath, child))
			root := parentTreeRoot(t, rootPath)
			seen := map[string]struct{}{".": {}}
			if test.seen != nil {
				seen = test.seen(child)
			}
			expected := map[string]backupTreeKind{".": backupTreeDirectory, child: test.kind}
			if test.expected != nil {
				expected = test.expected(child)
			}
			parentTreeRequireError(t, verifyExactBackupDirectory(
				t.Context(), rootPath, root, ".", expected, seen,
				map[fileidentity.Identity]string{},
			), test.want)
		})
	}

	t.Run("root changes during traversal", func(t *testing.T) {
		rootPath := t.TempDir()
		if err := os.Chmod(rootPath, 0o700); err != nil {
			t.Fatal(err)
		}
		child := filepath.Join(rootPath, "member")
		writeMigrationFile(t, child, []byte("member"))
		t.Cleanup(func() { _ = os.Chmod(rootPath, 0o700) })
		root := parentTreeRoot(t, rootPath)
		ctx := &exactTreeActionContext{Context: t.Context(), allowed: 1, action: func() {
			if err := os.Chmod(rootPath, 0o755); err != nil {
				t.Fatal(err)
			}
		}}
		parentTreeRequireError(t, verifyExactBackupDirectory(
			ctx, rootPath, root, ".",
			map[string]backupTreeKind{".": backupTreeDirectory, "member": backupTreeFile},
			map[string]struct{}{".": {}}, map[fileidentity.Identity]string{},
		), "directory changed during traversal")
	})

	t.Run("invalid entry name", func(t *testing.T) {
		rootPath := t.TempDir()
		if err := os.Chmod(rootPath, 0o700); err != nil {
			t.Fatal(err)
		}
		name := string([]byte{0xff})
		if err := os.WriteFile(filepath.Join(rootPath, name), []byte("member"), 0o600); err != nil {
			t.Skipf("invalid UTF-8 names unavailable: %v", err)
		}
		root := parentTreeRoot(t, rootPath)
		parentTreeRequireError(t, verifyExactBackupDirectory(
			t.Context(), rootPath, root, ".",
			map[string]backupTreeKind{".": backupTreeDirectory, name: backupTreeFile},
			map[string]struct{}{".": {}}, map[fileidentity.Identity]string{},
		), "invalid path component")
	})
}

func TestExactBackupRegularAndRevalidationCoverage(t *testing.T) {
	t.Run("physical alias", func(t *testing.T) {
		rootPath := t.TempDir()
		path := filepath.Join(rootPath, "member")
		writeMigrationFile(t, path, []byte("member"))
		root := parentTreeRoot(t, rootPath)
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		identity := parentTreeIdentity(t, path)
		parentTreeRequireError(t, verifyExactBackupRegular(
			rootPath, root, "member", info,
			map[fileidentity.Identity]string{identity: "other"},
		), "physical object alias")
	})

	t.Run("unsafe permissions and hard links", func(t *testing.T) {
		for _, test := range []struct {
			name    string
			prepare func(*testing.T, string)
			want    string
		}{
			{name: "public mode", prepare: func(t *testing.T, path string) {
				if err := os.WriteFile(path, []byte("member"), 0o644); err != nil {
					t.Fatal(err)
				}
			}, want: "validate database backup inventory file"},
			{name: "hard link", prepare: func(t *testing.T, path string) {
				writeMigrationFile(t, path, []byte("member"))
				if err := os.Link(path, path+"-alias"); err != nil {
					t.Skipf("hard links unavailable: %v", err)
				}
			}, want: "hard-link count"},
		} {
			t.Run(test.name, func(t *testing.T) {
				rootPath := t.TempDir()
				path := filepath.Join(rootPath, "member")
				test.prepare(t, path)
				root := parentTreeRoot(t, rootPath)
				info, err := os.Lstat(path)
				if err != nil {
					t.Fatal(err)
				}
				parentTreeRequireError(t, verifyExactBackupRegular(
					rootPath, root, "member", info, map[fileidentity.Identity]string{},
				), test.want)
			})
		}
	})

	t.Run("file changes after opening", func(t *testing.T) {
		rootPath := t.TempDir()
		path := filepath.Join(rootPath, "member")
		writeMigrationFile(t, path, []byte("member"))
		root := parentTreeRoot(t, rootPath)
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		before := &exactTreeActionFileInfo{FileInfo: info, action: func() {
			if err := os.Truncate(path, 1); err != nil {
				t.Fatal(err)
			}
		}}
		parentTreeRequireError(t, verifyExactBackupRegular(
			rootPath, root, "member", before, map[fileidentity.Identity]string{},
		), "file changed during inspection")
	})

	t.Run("reopen errors and type mismatch", func(t *testing.T) {
		rootPath := t.TempDir()
		path := filepath.Join(rootPath, "member")
		writeMigrationFile(t, path, []byte("member"))
		root := parentTreeRoot(t, rootPath)
		identity := parentTreeIdentity(t, path)
		if err := revalidateExactBackupChild(
			root, "missing", filepath.Join(rootPath, "missing"), identity,
			fileidentity.ObjectTypeRegular,
		); err == nil {
			t.Fatal("missing child revalidation succeeded")
		}
		parentTreeRequireError(t, revalidateExactBackupChild(
			root, "member", path, parentTreeIdentity(t, rootPath), fileidentity.ObjectTypeRegular,
		), "object changed during mount revalidation")
	})
}

type exactTreeActionContext struct {
	context.Context
	allowed int
	action  func()
	done    bool
	result  error
}

func (ctx *exactTreeActionContext) Err() error {
	if ctx.allowed > 0 {
		ctx.allowed--
		return nil
	}
	if !ctx.done {
		ctx.done = true
		if ctx.action != nil {
			ctx.action()
		}
	}
	return ctx.result
}

type exactTreeActionFileInfo struct {
	os.FileInfo
	once   sync.Once
	action func()
}

func (info *exactTreeActionFileInfo) ModTime() time.Time {
	value := info.FileInfo.ModTime()
	info.once.Do(info.action)
	return value
}

func exactTreeWriteRegular(t *testing.T, path string) {
	t.Helper()
	writeMigrationFile(t, path, []byte("member"))
}
