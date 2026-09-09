package databasemigration

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func TestPinnedBackupFileRemovalQuarantinesExactIdentity(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "control")
	if err := os.WriteFile(path, []byte("control"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected := pinnedFaultIdentity(t, path, fileidentity.ObjectTypeRegular)

	if removalErr := removePinnedBackupFile(path, expected); removalErr != nil {
		t.Fatalf("remove exact control identity: %v", removalErr)
	}
	if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("removed control remains: %v", statErr)
	}
	tombstones, globErr := filepath.Glob(filepath.Join(parent, ".database-backup-remove-*"))
	if globErr != nil || len(tombstones) != 0 {
		t.Fatalf("successful file-removal tombstones = %q, %v", tombstones, globErr)
	}
}

func TestPinnedBackupFileRemovalPreservesUnsafeTargets(t *testing.T) {
	parent := t.TempDir()
	directory := filepath.Join(parent, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	directoryIdentity := pinnedFaultIdentity(t, directory, fileidentity.ObjectTypeDirectory)
	if removalErr := removePinnedBackupFile(directory, directoryIdentity); removalErr == nil ||
		!strings.Contains(removalErr.Error(), "target changed") {
		t.Fatalf("directory file-removal result = %v", removalErr)
	}
	if info, statErr := os.Lstat(directory); statErr != nil || !info.IsDir() {
		t.Fatalf("rejected directory target = %#v, %v", info, statErr)
	}

	realParent := filepath.Join(parent, "real-parent")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(realParent, "control")
	if err := os.WriteFile(path, []byte("control"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected := pinnedFaultIdentity(t, path, fileidentity.ObjectTypeRegular)
	linkedParent := filepath.Join(parent, "linked-parent")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Skipf("create parent symlink: %v", err)
	}
	linkedPath := filepath.Join(linkedParent, "control")
	if err := removePinnedBackupFile(linkedPath, expected); err == nil ||
		!strings.Contains(err.Error(), "ancestor") {
		t.Fatalf("symlink-parent file-removal result = %v", err)
	}
	if payload, err := os.ReadFile(path); err != nil || string(payload) != "control" {
		t.Fatalf("rejected symlink target payload = %q, %v", payload, err)
	}
}

func TestPinnedBackupFileRemovalKeepsFileWhenQuarantineCannotBeReserved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not enforce Unix directory write mode bits")
	}
	parent := t.TempDir()
	path := filepath.Join(parent, "control")
	if err := os.WriteFile(path, []byte("control"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected := pinnedFaultIdentity(t, path, fileidentity.ObjectTypeRegular)
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	if err := removePinnedBackupFile(path, expected); err == nil {
		t.Fatal("file removal succeeded without permission to reserve quarantine")
	}
	if payload, err := os.ReadFile(path); err != nil || string(payload) != "control" {
		t.Fatalf("unquarantined control payload = %q, %v", payload, err)
	}
}

func TestPinnedBackupOpenRejectsUnreadableRegularFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not enforce Unix file read mode bits")
	}
	parent := t.TempDir()
	path := filepath.Join(parent, "unreadable")
	if err := os.WriteFile(path, []byte("payload"), 0o000); err != nil {
		t.Fatal(err)
	}
	opened, _, err := openPinnedBackupPath(path, false)
	if opened != nil {
		_ = opened.Close()
	}
	if err == nil {
		t.Fatal("pinned open accepted unreadable regular file")
	}
}

func TestReserveBackupRemovalPathRejectsMissingParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "missing")
	if path, err := reserveBackupTreeRemovalPath(parent); path != "" || err == nil {
		t.Fatalf("missing-parent reservation = %q, %v", path, err)
	}
}

type pinnedRemovalFaultCase struct {
	name            string
	want            string
	configure       func(*testing.T, pinnedRemovalFixture, *backupTreeRemovalOps, error)
	wantRemoveCalls int
	afterQuarantine bool
	tombstoneGone   bool
}

type pinnedRemovalFixture struct {
	base      string
	parent    string
	path      string
	tombstone string
	expected  fileidentity.Identity
}

func TestPinnedBackupTreeRemovalRejectsInvalidOperations(t *testing.T) {
	fixture := newPinnedRemovalFixture(t)
	pathErr := removePinnedBackupTreeWithOps("relative", fixture.expected, defaultBackupTreeRemovalOps())
	if pathErr == nil || !strings.Contains(pathErr.Error(), "path is invalid") {
		t.Fatalf("relative removal = %v", pathErr)
	}
	removalErr := removePinnedBackupTreeWithOps(
		fixture.path, fixture.expected, backupTreeRemovalOps{},
	)
	if removalErr == nil || !strings.Contains(removalErr.Error(), "operations are invalid") {
		t.Fatalf("invalid-operation removal = %v", removalErr)
	}
	assertPinnedRemovalPayload(t, fixture.path)
}

func TestPinnedBackupTreeRemovalRejectsDisappearedParent(t *testing.T) {
	fixture := newPinnedRemovalFixture(t)
	operations := defaultBackupTreeRemovalOps()
	lookup := operations.identity
	movedParent := filepath.Join(fixture.base, "moved-parent")
	firstLookup := true
	operations.identity = func(
		candidate string,
	) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
		identity, objectType, exists, lookupErr := lookup(candidate)
		if candidate == fixture.path && firstLookup {
			firstLookup = false
			if renameErr := os.Rename(fixture.parent, movedParent); renameErr != nil {
				return fileidentity.Identity{}, 0, false, renameErr
			}
		}
		return identity, objectType, exists, lookupErr
	}

	removalErr := removePinnedBackupTreeWithOps(fixture.path, fixture.expected, operations)
	if removalErr == nil {
		t.Fatal("removal accepted a parent that disappeared after identity lookup")
	}
	assertPinnedRemovalPayload(t, filepath.Join(movedParent, "tree"))
}

func TestPinnedBackupTreeRemovalPreQuarantineFaults(t *testing.T) {
	canary := errors.New("injected pinned-removal fault")
	tests := []pinnedRemovalFaultCase{
		{
			name: "parent handle identity",
			want: canary.Error(),
			configure: func(
				_ *testing.T, _ pinnedRemovalFixture, operations *backupTreeRemovalOps, fault error,
			) {
				failPinnedOpenedLookup(operations, 1, fault)
			},
		},
		{
			name: "parent path identity",
			want: "parent identity changed",
			configure: func(
				_ *testing.T, fixture pinnedRemovalFixture,
				operations *backupTreeRemovalOps, fault error,
			) {
				failPinnedPathLookup(operations, fixture.parent, 1, fault)
			},
		},
		{
			name: "source handle identity",
			want: "target handle identity changed",
			configure: func(
				_ *testing.T, _ pinnedRemovalFixture, operations *backupTreeRemovalOps, fault error,
			) {
				failPinnedOpenedLookup(operations, 2, fault)
			},
		},
		{
			name: "source handle mismatch",
			want: "target handle identity changed",
			configure: func(
				subtest *testing.T, fixture pinnedRemovalFixture,
				operations *backupTreeRemovalOps, _ error,
			) {
				other := filepath.Join(fixture.parent, "other")
				if mkdirErr := os.Mkdir(other, 0o700); mkdirErr != nil {
					subtest.Fatal(mkdirErr)
				}
				wrong := pinnedFaultIdentity(subtest, other, fileidentity.ObjectTypeDirectory)
				replacePinnedOpenedLookup(operations, 2, wrong)
			},
		},
		{
			name: "reservation",
			want: canary.Error(),
			configure: func(
				_ *testing.T, _ pinnedRemovalFixture, operations *backupTreeRemovalOps, fault error,
			) {
				operations.reserve = func(string) (string, error) { return "", fault }
			},
		},
		{
			name: "invalid tombstone",
			want: "tombstone path is invalid",
			configure: func(
				_ *testing.T, _ pinnedRemovalFixture, operations *backupTreeRemovalOps, _ error,
			) {
				operations.reserve = func(string) (string, error) { return "relative", nil }
			},
		},
		{
			name: "occupied tombstone",
			want: "tombstone is not vacant",
			configure: func(
				subtest *testing.T, fixture pinnedRemovalFixture,
				operations *backupTreeRemovalOps, _ error,
			) {
				if mkdirErr := os.Mkdir(fixture.tombstone, 0o700); mkdirErr != nil {
					subtest.Fatal(mkdirErr)
				}
				operations.reserve = func(string) (string, error) {
					return fixture.tombstone, nil
				}
			},
		},
		{
			name: "tombstone vacancy lookup",
			want: "tombstone is not vacant",
			configure: func(
				_ *testing.T, fixture pinnedRemovalFixture,
				operations *backupTreeRemovalOps, fault error,
			) {
				operations.reserve = func(string) (string, error) {
					return fixture.tombstone, nil
				}
				failPinnedPathLookup(operations, fixture.tombstone, 1, fault)
			},
		},
		{
			name: "second parent identity",
			want: "parent identity changed",
			configure: func(
				_ *testing.T, fixture pinnedRemovalFixture,
				operations *backupTreeRemovalOps, fault error,
			) {
				operations.reserve = func(string) (string, error) {
					return fixture.tombstone, nil
				}
				failPinnedPathLookup(operations, fixture.parent, 2, fault)
			},
		},
		{
			name: "second target identity",
			want: "target identity changed",
			configure: func(
				_ *testing.T, fixture pinnedRemovalFixture,
				operations *backupTreeRemovalOps, fault error,
			) {
				operations.reserve = func(string) (string, error) {
					return fixture.tombstone, nil
				}
				failPinnedPathLookup(operations, fixture.path, 2, fault)
			},
		},
		{
			name: "rename",
			want: canary.Error(),
			configure: func(
				_ *testing.T, fixture pinnedRemovalFixture,
				operations *backupTreeRemovalOps, fault error,
			) {
				operations.reserve = func(string) (string, error) {
					return fixture.tombstone, nil
				}
				operations.rename = func(string, string) error { return fault }
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runPinnedRemovalFaultCase(t, test, canary)
		})
	}
}

func TestPinnedBackupTreeRemovalPostQuarantineFaults(t *testing.T) {
	canary := errors.New("injected pinned-removal fault")
	tests := []pinnedRemovalFaultCase{
		{
			name:            "second tombstone identity",
			want:            "tombstone identity changed",
			afterQuarantine: true,
			configure: func(
				_ *testing.T, fixture pinnedRemovalFixture,
				operations *backupTreeRemovalOps, fault error,
			) {
				failPinnedPathLookup(operations, fixture.tombstone, 3, fault)
			},
		},
		{
			name:            "third parent identity",
			want:            "parent identity changed",
			afterQuarantine: true,
			configure: func(
				_ *testing.T, fixture pinnedRemovalFixture,
				operations *backupTreeRemovalOps, fault error,
			) {
				failPinnedPathLookup(operations, fixture.parent, 3, fault)
			},
		},
		{
			name:            "second missing-source proof",
			want:            "source name changed",
			afterQuarantine: true,
			configure: func(
				_ *testing.T, fixture pinnedRemovalFixture,
				operations *backupTreeRemovalOps, _ error,
			) {
				claimPinnedPathExists(operations, fixture.path, 4, fixture.expected)
			},
		},
		{
			name:            "retained-handle deletion",
			want:            canary.Error(),
			wantRemoveCalls: 1,
			afterQuarantine: true,
			configure: func(
				_ *testing.T, _ pinnedRemovalFixture, operations *backupTreeRemovalOps, fault error,
			) {
				operations.removeTree = func(
					string, *os.Root, string, *os.Root, fileidentity.Identity,
				) error {
					return fault
				}
			},
		},
		{
			name:            "tombstone remains",
			want:            "tombstone remains",
			wantRemoveCalls: 1,
			afterQuarantine: true,
			configure: func(
				_ *testing.T, _ pinnedRemovalFixture, operations *backupTreeRemovalOps, _ error,
			) {
				operations.removeTree = func(
					string, *os.Root, string, *os.Root, fileidentity.Identity,
				) error {
					return nil
				}
			},
		},
		{
			name:            "final missing-source proof",
			want:            "source name changed",
			wantRemoveCalls: 1,
			tombstoneGone:   true,
			configure: func(
				_ *testing.T, fixture pinnedRemovalFixture,
				operations *backupTreeRemovalOps, _ error,
			) {
				claimPinnedPathExists(operations, fixture.path, 5, fixture.expected)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runPinnedRemovalFaultCase(t, test, canary)
		})
	}
}

func TestPinnedBackupTreeContentsRejectsUnsafeInventories(t *testing.T) {
	if err := removePinnedBackupTreeContents("relative", nil, nil, nil); err == nil {
		t.Fatal("tree-content removal accepted invalid inputs")
	}

	t.Run("entry limit", func(t *testing.T) {
		fixture := newPinnedRemovalFixture(t)
		root, openErr := os.OpenRoot(fixture.path)
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer func() { _ = root.Close() }()
		entries := backupMaxEntries
		identities := map[fileidentity.Identity]string{fixture.expected: fixture.path}
		removalErr := removePinnedBackupTreeContents(
			fixture.path, root, identities, &entries,
		)
		if removalErr == nil || !strings.Contains(removalErr.Error(), "entry limit") {
			t.Fatalf("over-budget tree-content removal = %v", removalErr)
		}
		assertPinnedRemovalPayload(t, fixture.path)
	})

	t.Run("physical alias", func(t *testing.T) {
		fixture := newPinnedRemovalFixture(t)
		payload := filepath.Join(fixture.path, "nested", "payload")
		alias := filepath.Join(fixture.path, "nested", "alias")
		if linkErr := os.Link(payload, alias); linkErr != nil {
			t.Skipf("create hard link: %v", linkErr)
		}
		removalErr := removePinnedBackupTree(fixture.path)
		if removalErr == nil || !strings.Contains(removalErr.Error(), "physically alias") {
			t.Fatalf("hard-linked tree removal = %v", removalErr)
		}
		assertPinnedRemovalQuarantine(t, fixture.parent)
	})

	t.Run("unsafe child", func(t *testing.T) {
		fixture := newPinnedRemovalFixture(t)
		unsafe := filepath.Join(fixture.path, "unsafe")
		if linkErr := os.Symlink("nested/payload", unsafe); linkErr != nil {
			t.Skipf("create symlink: %v", linkErr)
		}
		removalErr := removePinnedBackupTree(fixture.path)
		if removalErr == nil || !strings.Contains(removalErr.Error(), "identity is unavailable") {
			t.Fatalf("symlinked tree removal = %v", removalErr)
		}
		assertPinnedRemovalQuarantine(t, fixture.parent)
	})
}

func TestPinnedBackupTreeContentsHandleAndPermissionFaults(t *testing.T) {
	t.Run("missing opened child", func(t *testing.T) {
		parent := t.TempDir()
		root, openErr := os.OpenRoot(parent)
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer func() { _ = root.Close() }()
		child, _, _, childErr := openedBackupRemovalDirectory(
			root, "missing", fileidentity.Opened,
		)
		if child != nil {
			_ = child.Close()
		}
		if childErr == nil {
			t.Fatal("missing removal child opened")
		}
	})

	t.Run("closed root", func(t *testing.T) {
		fixture := newPinnedRemovalFixture(t)
		root, openErr := os.OpenRoot(fixture.path)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if closeErr := root.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		entries := 0
		identities := map[fileidentity.Identity]string{fixture.expected: fixture.path}
		if removalErr := removePinnedBackupTreeContents(
			fixture.path, root, identities, &entries,
		); removalErr == nil {
			t.Fatal("tree-content removal accepted a closed root")
		}
		assertPinnedRemovalPayload(t, fixture.path)
	})

	if runtime.GOOS == "windows" {
		return
	}
	t.Run("unreadable child", func(t *testing.T) {
		fixture := newPinnedRemovalFixture(t)
		payload := filepath.Join(fixture.path, "nested", "payload")
		if chmodErr := os.Chmod(payload, 0o000); chmodErr != nil {
			t.Fatal(chmodErr)
		}
		t.Cleanup(func() { _ = os.Chmod(payload, 0o600) })
		if pinnedFaultCanRead(payload) {
			t.Skip("process can bypass file read permissions")
		}
		removalErr := removePinnedBackupTree(fixture.path)
		if removalErr == nil {
			t.Fatal("tree removal opened an unreadable child")
		}
		tombstones, globErr := filepath.Glob(
			filepath.Join(fixture.parent, ".database-backup-remove-*"),
		)
		if globErr != nil || len(tombstones) != 1 {
			t.Fatalf("unreadable-child tombstones = %q, %v", tombstones, globErr)
		}
		if chmodErr := os.Chmod(
			filepath.Join(tombstones[0], "nested", "payload"), 0o600,
		); chmodErr != nil {
			t.Fatal(chmodErr)
		}
		assertPinnedRemovalQuarantine(t, fixture.parent)
	})

	t.Run("unremovable child", func(t *testing.T) {
		base := t.TempDir()
		rootPath := filepath.Join(base, "tree")
		if mkdirErr := os.Mkdir(rootPath, 0o700); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		payload := filepath.Join(rootPath, "payload")
		if writeErr := os.WriteFile(payload, []byte("payload"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		rootIdentity := pinnedFaultIdentity(t, rootPath, fileidentity.ObjectTypeDirectory)
		root, openErr := os.OpenRoot(rootPath)
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer func() { _ = root.Close() }()
		if chmodErr := os.Chmod(rootPath, 0o500); chmodErr != nil {
			t.Fatal(chmodErr)
		}
		t.Cleanup(func() { _ = os.Chmod(rootPath, 0o700) })
		if pinnedFaultCanCreate(rootPath) {
			t.Skip("process can bypass directory write permissions")
		}
		entries := 0
		identities := map[fileidentity.Identity]string{rootIdentity: rootPath}
		removalErr := removePinnedBackupTreeContents(rootPath, root, identities, &entries)
		if removalErr == nil {
			t.Fatal("tree-content removal unlinked a child through an unwritable root")
		}
		if retained, readErr := os.ReadFile(payload); readErr != nil || string(retained) != "payload" {
			t.Fatalf("unremovable child payload = %q, %v", retained, readErr)
		}
	})
}

func TestPinnedBackupControlWriterRejectsEarlyIdentityFaults(t *testing.T) {
	canary := errors.New("injected control identity fault")
	for _, test := range []struct {
		name      string
		configure func(*backupControlWriteOps, fileidentity.Identity)
		want      string
	}{
		{
			name: "opened handle",
			configure: func(operations *backupControlWriteOps, _ fileidentity.Identity) {
				operations.opened = func(
					*os.File,
				) (fileidentity.Identity, fileidentity.ObjectType, error) {
					return fileidentity.Identity{}, 0, canary
				}
			},
			want: "handle identity is unsafe",
		},
		{
			name: "path identity",
			configure: func(
				operations *backupControlWriteOps, _ fileidentity.Identity,
			) {
				operations.identity = func(
					string,
				) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
					return fileidentity.Identity{}, fileidentity.ObjectTypeRegular, true, nil
				}
			},
			want: "control file identity is unsafe",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "control")
			file, createErr := os.CreateTemp(t.TempDir(), "backing-")
			if createErr != nil {
				t.Fatal(createErr)
			}
			t.Cleanup(func() { _ = file.Close() })
			created := pinnedFaultOpenedIdentity(t, file, fileidentity.ObjectTypeRegular)
			closeCalls := 0
			removeCalls := 0
			operations := pinnedFaultControlWriteOps(file, created)
			test.configure(&operations, created)
			closeOperation := operations.close
			operations.close = func(file *os.File) error {
				closeCalls++
				return closeOperation(file)
			}
			operations.remove = func(string, fileidentity.Identity) error {
				removeCalls++
				return nil
			}

			writeErr := writePrivateBackupFileExclusiveWithOps(path, nil, 0o600, operations)
			if writeErr == nil || !strings.Contains(writeErr.Error(), test.want) {
				t.Fatalf("early identity fault = %v", writeErr)
			}
			wantRemoveCalls := 1
			if test.name == "opened handle" {
				wantRemoveCalls = 0
			}
			if closeCalls != 1 || removeCalls != wantRemoveCalls {
				t.Fatalf("early cleanup close=%d remove=%d", closeCalls, removeCalls)
			}
		})
	}
}

func TestPinnedBackupFileRemovalRejectsUnsafeIdentityLookup(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if writeErr := os.WriteFile(target, []byte("target"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	expected := pinnedFaultIdentity(t, target, fileidentity.ObjectTypeRegular)
	link := filepath.Join(parent, "link")
	if linkErr := os.Symlink(target, link); linkErr != nil {
		t.Skipf("create symlink: %v", linkErr)
	}
	removalErr := removePinnedBackupFile(link, expected)
	if removalErr == nil {
		t.Fatal("pinned file removal accepted a symlink identity")
	}
	if retained, readErr := os.ReadFile(target); readErr != nil || string(retained) != "target" {
		t.Fatalf("symlink-rejected target payload = %q, %v", retained, readErr)
	}
}

func runPinnedRemovalFaultCase(
	t *testing.T,
	test pinnedRemovalFaultCase,
	canary error,
) {
	t.Helper()
	fixture := newPinnedRemovalFixture(t)
	operations := defaultBackupTreeRemovalOps()
	operations.reserve = func(string) (string, error) { return fixture.tombstone, nil }
	test.configure(t, fixture, &operations, canary)
	removeOperation := operations.removeTree
	removeCalls := 0
	operations.removeTree = func(
		path string,
		parent *os.Root,
		leaf string,
		child *os.Root,
		expected fileidentity.Identity,
	) error {
		removeCalls++
		return removeOperation(path, parent, leaf, child, expected)
	}

	removalErr := removePinnedBackupTreeWithOps(fixture.path, fixture.expected, operations)
	if removalErr == nil || !strings.Contains(removalErr.Error(), test.want) {
		t.Fatalf("faulted removal = %v, want %q", removalErr, test.want)
	}
	if removeCalls != test.wantRemoveCalls {
		t.Fatalf("retained-handle deletion calls = %d, want %d", removeCalls, test.wantRemoveCalls)
	}
	if test.tombstoneGone {
		if _, statErr := os.Lstat(fixture.tombstone); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("deleted tombstone remains: %v", statErr)
		}
		return
	}
	if test.afterQuarantine {
		assertPinnedRemovalPayload(t, fixture.tombstone)
		return
	}
	assertPinnedRemovalPayload(t, fixture.path)
}

func newPinnedRemovalFixture(t *testing.T) pinnedRemovalFixture {
	t.Helper()
	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	if mkdirErr := os.Mkdir(parent, 0o700); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	path := filepath.Join(parent, "tree")
	writeBackupRemovalTree(t, path)
	return pinnedRemovalFixture{
		base:      base,
		parent:    parent,
		path:      path,
		tombstone: filepath.Join(parent, ".database-backup-remove-test"),
		expected:  pinnedFaultIdentity(t, path, fileidentity.ObjectTypeDirectory),
	}
}

func pinnedFaultIdentity(
	t *testing.T,
	path string,
	want fileidentity.ObjectType,
) fileidentity.Identity {
	t.Helper()
	identity, objectType, exists, identityErr := fileidentity.ExistingWithType(path)
	if identityErr != nil || !exists || objectType != want {
		t.Fatalf("identity for %q = %#v, %v, %t, %v", path, identity, objectType, exists, identityErr)
	}
	return identity
}

func pinnedFaultOpenedIdentity(
	t *testing.T,
	file *os.File,
	want fileidentity.ObjectType,
) fileidentity.Identity {
	t.Helper()
	identity, objectType, identityErr := fileidentity.Opened(file)
	if identityErr != nil || objectType != want {
		t.Fatalf("opened identity = %#v, %v, %v", identity, objectType, identityErr)
	}
	return identity
}

func pinnedFaultControlWriteOps(
	file *os.File,
	identity fileidentity.Identity,
) backupControlWriteOps {
	return backupControlWriteOps{
		create: func(string, int, os.FileMode) (*os.File, error) { return file, nil },
		stat:   func(candidate *os.File) (os.FileInfo, error) { return candidate.Stat() },
		identity: func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
			return identity, fileidentity.ObjectTypeRegular, true, nil
		},
		opened: func(*os.File) (fileidentity.Identity, fileidentity.ObjectType, error) {
			return identity, fileidentity.ObjectTypeRegular, nil
		},
		write:   func(*os.File, []byte) (int, error) { return 0, nil },
		chmod:   func(*os.File, os.FileMode) error { return nil },
		sync:    func(*os.File) error { return nil },
		close:   func(*os.File) error { return nil },
		secure:  func(string) error { return nil },
		lstat:   func(string) (os.FileInfo, error) { return file.Stat() },
		remove:  func(string, fileidentity.Identity) error { return nil },
		syncDir: func(string) error { return nil },
	}
}

func pinnedFaultCanRead(path string) bool {
	file, openErr := os.Open(path)
	if openErr != nil {
		return false
	}
	_ = file.Close()
	return true
}

func pinnedFaultCanCreate(parent string) bool {
	probe := filepath.Join(parent, ".permission-probe")
	createErr := os.Mkdir(probe, 0o700)
	if createErr != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

func failPinnedPathLookup(
	operations *backupTreeRemovalOps,
	target string,
	occurrence int,
	fault error,
) {
	lookup := operations.identity
	calls := 0
	operations.identity = func(
		candidate string,
	) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
		if candidate == target {
			calls++
			if calls == occurrence {
				return fileidentity.Identity{}, 0, false, fault
			}
		}
		return lookup(candidate)
	}
}

func failPinnedOpenedLookup(operations *backupTreeRemovalOps, occurrence int, fault error) {
	lookup := operations.opened
	calls := 0
	operations.opened = func(
		file *os.File,
	) (fileidentity.Identity, fileidentity.ObjectType, error) {
		calls++
		if calls == occurrence {
			return fileidentity.Identity{}, 0, fault
		}
		return lookup(file)
	}
}

func replacePinnedOpenedLookup(
	operations *backupTreeRemovalOps,
	occurrence int,
	replacement fileidentity.Identity,
) {
	lookup := operations.opened
	calls := 0
	operations.opened = func(
		file *os.File,
	) (fileidentity.Identity, fileidentity.ObjectType, error) {
		calls++
		identity, objectType, lookupErr := lookup(file)
		if calls == occurrence && lookupErr == nil {
			return replacement, objectType, nil
		}
		return identity, objectType, lookupErr
	}
}

func claimPinnedPathExists(
	operations *backupTreeRemovalOps,
	target string,
	occurrence int,
	identity fileidentity.Identity,
) {
	lookup := operations.identity
	calls := 0
	operations.identity = func(
		candidate string,
	) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
		if candidate == target {
			calls++
			if calls == occurrence {
				return identity, fileidentity.ObjectTypeDirectory, true, nil
			}
		}
		return lookup(candidate)
	}
}

func assertPinnedRemovalPayload(t *testing.T, root string) {
	t.Helper()
	payload, readErr := os.ReadFile(filepath.Join(root, "nested", "payload"))
	if readErr != nil || string(payload) != "payload" {
		t.Fatalf("retained removal payload = %q, %v", payload, readErr)
	}
}

func assertPinnedRemovalQuarantine(t *testing.T, parent string) {
	t.Helper()
	tombstones, globErr := filepath.Glob(filepath.Join(parent, ".database-backup-remove-*"))
	if globErr != nil || len(tombstones) != 1 {
		t.Fatalf("retained removal tombstones = %q, %v", tombstones, globErr)
	}
	assertPinnedRemovalPayload(t, tombstones[0])
}
