//nolint:golines // Fault-table assertions keep calls together.
package databasemigration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"hash"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestLegacyTraversalBoundaries(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	backupRoot := filepath.Join(root, "backups", "active")
	writeMigrationFile(t, filepath.Join(backupRoot, "ignored"), []byte("backup"))
	regular := filepath.Join(root, "input.json")
	writeMigrationFile(t, regular, []byte("legacy"))
	var visited []string
	visit := func(path string) error { visited = append(visited, path); return nil }
	if err := walkLegacyInputs(
		ctx, filepath.Join(root, "missing"), backupRoot, nil, newBackupBudget(), visit,
	); err != nil {
		t.Fatal(err)
	}
	if err := walkLegacyInputs(
		ctx, regular, backupRoot, nil, newBackupBudget(), visit,
	); err != nil || len(visited) != 1 {
		t.Fatalf("regular visit = %#v, %v", visited, err)
	}
	if err := walkLegacyInputs(
		ctx, regular, backupRoot, map[string]struct{}{backupPathKey(regular): {}}, newBackupBudget(), visit,
	); err != nil || len(visited) != 1 {
		t.Fatalf("excluded visit = %#v, %v", visited, err)
	}
	canary := errors.New("visit canary")
	if err := walkLegacyInputs(
		ctx, regular, backupRoot, nil, newBackupBudget(), func(string) error { return canary },
	); !errors.Is(err, canary) {
		t.Fatalf("visitor error = %v", err)
	}

	tree := filepath.Join(root, "tree")
	for _, path := range []string{
		filepath.Join(tree, "kept.json"),
		filepath.Join(tree, "nested", "kept.json"),
		filepath.Join(tree, "legacy-json", "ignored.json"),
		filepath.Join(tree, "backups", "ignored.json"),
		filepath.Join(tree, database.StateDirectoryName, "ignored.json"),
		filepath.Join(tree, "active-backup", "ignored.json"),
	} {
		writeMigrationFile(t, path, []byte("legacy"))
	}
	visited = nil
	if err := walkLegacyInputs(
		ctx, tree, filepath.Join(tree, "active-backup"), nil, newBackupBudget(), visit,
	); err != nil || len(visited) != 2 {
		t.Fatalf("tree visits = %#v, %v", visited, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := walkLegacyInputs(canceled, tree, backupRoot, nil, newBackupBudget(), visit); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled walk = %v", err)
	}
	link := filepath.Join(root, "root-link")
	if err := os.Symlink(regular, link); err == nil {
		if err := walkLegacyInputs(ctx, link, backupRoot, nil, newBackupBudget(), visit); err == nil {
			t.Fatal("symlinked legacy root accepted")
		}
		nestedLink := filepath.Join(tree, "nested", "link")
		if err := os.Symlink(regular, nestedLink); err == nil {
			if err := walkLegacyInputs(ctx, tree, backupRoot, nil, newBackupBudget(), visit); err == nil {
				t.Fatal("symlinked legacy member accepted")
			}
		}
	}
}

type coverageErrorWriter struct{ err error }

func (writer coverageErrorWriter) Write([]byte) (int, error) { return 0, writer.err }

type coverageShortWriter struct{}

func (coverageShortWriter) Write(payload []byte) (int, error) { return len(payload) - 1, nil }

type coverageHash struct {
	hash.Hash
	err error
}

func (digest coverageHash) Write([]byte) (int, error) { return 0, digest.err }

func TestBackupCopyBoundaries(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	payload := []byte("backup payload")
	writeMigrationFile(t, source, payload)
	if err := os.Chmod(source, 0o640); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "backup")
	if err := ensurePrivateBackupDirectory(backup); err != nil {
		t.Fatal(err)
	}
	record, err := copyBackupFile(
		t.Context(), backup, "global/auth", "legacy", source,
		filepath.Join("items", "source"), newBackupBudget(),
	)
	if err != nil || record.Size != int64(len(payload)) || record.SHA256 == "" ||
		record.Backup != "items/source" || record.SourceMode != 0o640 {
		t.Fatalf("backup record = %#v, %v", record, err)
	}
	if copied, err := os.ReadFile(filepath.Join(backup, "items", "source")); err != nil ||
		!bytes.Equal(copied, payload) {
		t.Fatalf("copied payload = %q, %v", copied, err)
	}
	for _, test := range []struct{ source, destination string }{
		{filepath.Join(root, "missing"), "missing"},
		{root, "directory"},
		{source, "."},
		{source, "items/source"},
	} {
		if _, err := copyBackupFile(
			t.Context(), backup, "id", "role", test.source, test.destination, newBackupBudget(),
		); err == nil {
			t.Errorf("copyBackupFile(%q, %q) succeeded", test.source, test.destination)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := copyBackupFile(
		canceled, backup, "id", "role", source, "canceled", newBackupBudget(),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled backup = %v", err)
	}

	canary := errors.New("copy canary")
	if count, err := copyWithContext(
		context.Background(), io.Discard, sha256.New(), strings.NewReader("payload"), 7,
	); err != nil || count != 7 {
		t.Fatalf("copy success = %d, %v", count, err)
	}
	if _, err := copyWithContext(context.Background(), io.Discard, sha256.New(), strings.NewReader("x"), -1); err == nil {
		t.Fatal("negative copy limit succeeded")
	}
	if _, err := copyWithContext(canceled, io.Discard, sha256.New(), strings.NewReader("x"), 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled copy = %v", err)
	}
	if _, err := copyWithContext(
		context.Background(), io.Discard, sha256.New(), strings.NewReader("too long"), 1,
	); err == nil {
		t.Fatal("oversized copy succeeded")
	}
	if _, err := copyWithContext(
		context.Background(), coverageErrorWriter{canary}, sha256.New(), strings.NewReader("x"), 1,
	); !errors.Is(err, canary) {
		t.Fatalf("writer error = %v", err)
	}
	if _, err := copyWithContext(
		context.Background(), coverageShortWriter{}, sha256.New(), strings.NewReader("x"), 1,
	); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short writer error = %v", err)
	}
	if _, err := copyWithContext(
		context.Background(), io.Discard, coverageHash{Hash: sha256.New(), err: canary},
		strings.NewReader("x"), 1,
	); !errors.Is(err, canary) {
		t.Fatalf("digest error = %v", err)
	}
	if _, err := copyWithContext(
		context.Background(), io.Discard, sha256.New(), iotest.ErrReader(canary), 1,
	); !errors.Is(err, canary) {
		t.Fatalf("reader error = %v", err)
	}
}

func TestBackupCopyDurabilityFaultBoundaries(t *testing.T) {
	canary := errors.New("durability canary")
	for _, test := range []struct {
		name        string
		keepsFile   bool
		wrapsCanary bool
		mutate      func(*backupCopyOps, string, string)
	}{
		{
			name: "opened source stat",
			mutate: func(ops *backupCopyOps, _, _ string) {
				ops.stat = func(*os.File) (os.FileInfo, error) { return nil, canary }
			},
		},
		{
			name: "source changes during copy",
			mutate: func(ops *backupCopyOps, source, other string) {
				calls := 0
				ops.lstat = func(path string) (os.FileInfo, error) {
					calls++
					if calls == 2 {
						return os.Lstat(other)
					}
					return os.Lstat(path)
				}
			},
		},
		{
			name: "chmod", wrapsCanary: true,
			mutate: func(ops *backupCopyOps, _, _ string) {
				ops.chmod = func(*os.File, os.FileMode) error { return canary }
			},
		},
		{
			name: "file sync", wrapsCanary: true,
			mutate: func(ops *backupCopyOps, _, _ string) {
				ops.sync = func(*os.File) error { return canary }
			},
		},
		{
			name: "file close", wrapsCanary: true,
			mutate: func(ops *backupCopyOps, source, _ string) {
				ops.close = func(file *os.File) error {
					closeErr := file.Close()
					if filepath.Clean(file.Name()) == filepath.Clean(source) {
						return closeErr
					}
					return errors.Join(closeErr, canary)
				}
			},
		},
		{
			name: "private-file validation", wrapsCanary: true,
			mutate: func(ops *backupCopyOps, _, _ string) {
				ops.secure = func(string) error { return canary }
			},
		},
		{
			name: "directory sync", wrapsCanary: true,
			mutate: func(ops *backupCopyOps, _, _ string) {
				ops.syncDir = func(string) error { return canary }
			},
		},
		{
			name: "relative path", wrapsCanary: true,
			mutate: func(ops *backupCopyOps, _, _ string) {
				ops.rel = func(string, string) (string, error) { return "", canary }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			other := filepath.Join(root, "other")
			writeMigrationFile(t, source, []byte("payload"))
			writeMigrationFile(t, other, []byte("payload"))
			backup := filepath.Join(root, "backup")
			if err := ensurePrivateBackupDirectory(backup); err != nil {
				t.Fatal(err)
			}
			ops := defaultBackupCopyOps()
			test.mutate(&ops, source, other)
			record, err := copyBackupFileWithOps(
				t.Context(), backup, "global/auth", "legacy", source,
				filepath.Join("nested", "copy"), newBackupBudget(), ops,
			)
			if err == nil || test.wrapsCanary && !errors.Is(err, canary) || record.Backup != "" {
				t.Fatalf("faulted backup record = %#v, %v", record, err)
			}
			destination := filepath.Join(backup, "nested", "copy")
			_, statErr := os.Lstat(destination)
			if test.keepsFile {
				if statErr != nil {
					t.Fatalf("durable destination was removed: %v", statErr)
				}
			} else if !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed destination remains: %v", statErr)
			}
		})
	}
}

func TestBackupDirectoryBoundaries(t *testing.T) {
	root := filepath.Join(t.TempDir(), "backup")
	if err := ensurePrivateBackupDirectory(root); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateBackupDirectory(root); err != nil {
		t.Fatalf("existing private backup directory = %v", err)
	}
	blocked := filepath.Join(t.TempDir(), "blocked")
	writeMigrationFile(t, blocked, nil)
	if err := ensurePrivateBackupDirectory(blocked); err == nil {
		t.Fatal("regular backup directory succeeded")
	}
	if err := validateBackupAncestors(filepath.Join(blocked, "child")); err == nil {
		t.Fatal("regular backup ancestor succeeded")
	}
	overlong := filepath.Join(t.TempDir(), strings.Repeat("x", 4096))
	if err := validateBackupAncestors(overlong); err == nil {
		t.Fatal("overlong backup ancestor succeeded")
	}
	if runtime.GOOS != "windows" {
		realDirectory := filepath.Join(t.TempDir(), "real")
		if err := os.Mkdir(realDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(realDirectory, link); err == nil {
			if err := ensurePrivateBackupDirectory(filepath.Join(link, "child")); err == nil {
				t.Fatal("symlinked backup ancestor succeeded")
			}
		}
	}
	if err := secureAndValidateBackupDirectory(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("secured missing directory")
	}
	if err := secureAndValidateBackupFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("secured missing file")
	}
}

func TestBackupControlReadAndHashBoundaries(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "control")
	writeMigrationFile(t, path, []byte("control"))
	if payload, err := readPrivateBackupFile(path, 7); err != nil || string(payload) != "control" {
		t.Fatalf("readPrivateBackupFile(valid) = %q, %v", payload, err)
	}
	if payload, err := readPrivateBackupFile(path, 0); payload != nil || err == nil {
		t.Fatalf("readPrivateBackupFile(zero) = %q, %v", payload, err)
	}
	if payload, err := readPrivateBackupFile(filepath.Join(root, "missing"), 1); payload != nil || err == nil {
		t.Fatalf("readPrivateBackupFile(missing) = %q, %v", payload, err)
	}
	if payload, err := readPrivateBackupFile(root, 1024); payload != nil || err == nil {
		t.Fatalf("readPrivateBackupFile(directory) = %q, %v", payload, err)
	}
	if payload, err := readPrivateBackupFile(path, 1); payload != nil || err == nil {
		t.Fatalf("readPrivateBackupFile(oversized) = %q, %v", payload, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if payload, err := readPrivateBackupFile(path, 7); payload != nil || err == nil {
			t.Fatalf("readPrivateBackupFile(non-private) = %q, %v", payload, err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "control-link")
	if err := os.Symlink(path, link); err == nil {
		if payload, err := readPrivateBackupFile(link, 7); payload != nil || err == nil {
			t.Fatalf("readPrivateBackupFile(symlink) = %q, %v", payload, err)
		}
	}

	info, statErr := os.Lstat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	identity, identityErr := backupExistingIdentity(path, fileidentity.ObjectTypeRegular)
	if identityErr != nil {
		t.Fatal(identityErr)
	}
	digest, size, hashErr := hashBackupFile(t.Context(), path, info, identity, 7)
	if hashErr != nil || size != 7 || digest == "" {
		t.Fatalf("hashBackupFile(valid) = %q, %d, %v", digest, size, hashErr)
	}
	if digest, size, err := hashBackupFile(nil, path, info, identity, 7); err != nil || size != 7 || digest == "" {
		t.Fatalf("hashBackupFile(nil context) = %q, %d, %v", digest, size, err)
	}
	if digest, size, err := hashBackupFile(
		t.Context(), filepath.Join(root, "missing"), info, identity, 7,
	); digest != "" || size != 0 || err == nil {
		t.Fatalf("hashBackupFile(missing) = %q, %d, %v", digest, size, err)
	}
	if digest, size, err := hashBackupFile(
		t.Context(), path, nil, identity, 7,
	); digest != "" || size != 0 || err == nil {
		t.Fatalf("hashBackupFile(nil metadata) = %q, %d, %v", digest, size, err)
	}
	if digest, size, err := hashBackupFile(
		t.Context(), path, info, fileidentity.Identity{}, 7,
	); digest != "" || size != 0 || err == nil {
		t.Fatalf("hashBackupFile(nil identity) = %q, %d, %v", digest, size, err)
	}
	other := filepath.Join(root, "other")
	writeMigrationFile(t, other, []byte("control"))
	otherIdentity, identityErr := backupExistingIdentity(other, fileidentity.ObjectTypeRegular)
	if identityErr != nil {
		t.Fatal(identityErr)
	}
	if digest, size, err := hashBackupFile(
		t.Context(), path, mustMigrationInfo(t, other), otherIdentity, 7,
	); digest != "" || size != 0 || err == nil {
		t.Fatalf("hashBackupFile(wrong identity) = %q, %d, %v", digest, size, err)
	}
	if digest, size, err := hashBackupFile(
		t.Context(), path, info, otherIdentity, 7,
	); digest != "" || size != 0 || err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("hashBackupFile(wrong full identity) = %q, %d, %v", digest, size, err)
	}
	if digest, size, err := hashBackupFile(
		t.Context(), path, info, identity, 1,
	); digest != "" || size != 0 || err == nil {
		t.Fatalf("hashBackupFile(limit) = %q, %d, %v", digest, size, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		permissive := mustMigrationInfo(t, path)
		if digest, size, err := hashBackupFile(
			t.Context(), path, permissive, identity, 7,
		); digest != "" || size != 0 || err == nil {
			t.Fatalf("hashBackupFile(non-private) = %q, %d, %v", digest, size, err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		info = mustMigrationInfo(t, path)
		hardlink := filepath.Join(root, "hardlink")
		if err := os.Link(path, hardlink); err == nil {
			if payload, err := readPrivateBackupFile(path, 7); payload != nil || err == nil {
				t.Fatalf("readPrivateBackupFile(hardlink) = %q, %v", payload, err)
			}
			if err := os.Remove(hardlink); err != nil {
				t.Fatal(err)
			}
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if digest, size, err := hashBackupFile(canceled, path, info, identity, 7); digest != "" || size != 0 ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("hashBackupFile(canceled) = %q, %d, %v", digest, size, err)
	}
	before := mustMigrationInfo(t, path)
	changing := &actionAfterMigrationErrChecks{
		Context: context.Background(), allowed: 1,
		action: func() {
			if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
				t.Error(err)
			}
		},
	}
	if digest, _, err := hashBackupFile(
		changing, path, before, identity, 7,
	); digest != "" || err == nil {
		t.Fatalf("hashBackupFile(changing) = %q, %v", digest, err)
	}
}

func TestManifestEncodingBoundaries(t *testing.T) {
	if payload, err := marshalBackupManifestLimit(BackupManifest{}, 0); payload != nil || err == nil {
		t.Fatalf("zero-limit manifest = %q, %v", payload, err)
	}
	if payload, err := marshalBackupManifestLimit(BackupManifest{
		CreatedAt: time.Date(10_000, time.January, 1, 0, 0, 0, 0, time.UTC),
	}, backupMaxManifestSize); payload != nil || err == nil {
		t.Fatalf("invalid-time manifest = %q, %v", payload, err)
	}
}

func mustMigrationInfo(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestLegacyDirectoryBranchBoundaries(t *testing.T) {
	root := t.TempDir()
	overlong := filepath.Join(root, strings.Repeat("x", 4096))
	if err := walkLegacyInputs(
		t.Context(), overlong, t.TempDir(), nil, newBackupBudget(), func(string) error { return nil },
	); err == nil {
		t.Fatal("overlong legacy root succeeded")
	}
	regular := filepath.Join(root, "regular")
	writeMigrationFile(t, regular, []byte("legacy"))
	if err := walkLegacyInputs(
		t.Context(), regular, t.TempDir(), nil, &backupBudget{}, func(string) error { return nil },
	); err == nil {
		t.Fatal("regular root ignored exhausted budget")
	}

	if err := walkLegacyDirectory(
		t.Context(), filepath.Join(root, "missing"), ".", t.TempDir(), nil,
		newBackupBudget(), func(string) error { return nil },
	); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing directory walk = %v", err)
	}
	if err := walkLegacyDirectory(
		t.Context(), regular, ".", t.TempDir(), nil,
		newBackupBudget(), func(string) error { return nil },
	); err == nil {
		t.Fatal("regular file walked as directory")
	}
	walkRoot := filepath.Join(root, "walk")
	writeMigrationFile(t, filepath.Join(walkRoot, "file"), []byte("legacy"))
	if err := walkLegacyDirectory(
		t.Context(), walkRoot, ".", t.TempDir(), nil,
		&backupBudget{maxEntries: 0, maxFiles: 1, maxDepth: 1, maxFileBytes: 1, maxBytes: 1},
		func(string) error { return nil },
	); err == nil {
		t.Fatal("directory ignored exhausted entry budget")
	}

	midCanceled := &cancelAfterMigrationErrChecks{Context: context.Background(), allowed: 1}
	if err := walkLegacyDirectory(
		midCanceled, walkRoot, ".", t.TempDir(), nil, newBackupBudget(), func(string) error { return nil },
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("entry-loop cancellation = %v", err)
	}

	for _, test := range []struct {
		name       string
		child      string
		backupRoot func(string) string
	}{
		{name: "backup tree", child: "active", backupRoot: func(root string) string { return filepath.Join(root, "active") }},
		{name: "reserved tree", child: "backups", backupRoot: func(string) string { return filepath.Join(root, "elsewhere") }},
	} {
		t.Run(test.name+" budget", func(t *testing.T) {
			tree := t.TempDir()
			if err := os.Mkdir(filepath.Join(tree, test.child), 0o700); err != nil {
				t.Fatal(err)
			}
			budget := &backupBudget{
				maxEntries: 4, maxFiles: 4, maxDepth: 0, maxFileBytes: 4, maxBytes: 4,
			}
			if err := walkLegacyDirectory(
				t.Context(), tree, ".", test.backupRoot(tree), nil, budget, func(string) error { return nil },
			); err == nil {
				t.Fatal("skipped tree ignored depth budget")
			}
		})
	}

	t.Run("nested recursion error", func(t *testing.T) {
		tree := t.TempDir()
		if err := os.Mkdir(filepath.Join(tree, "nested"), 0o700); err != nil {
			t.Fatal(err)
		}
		budget := &backupBudget{
			maxEntries: 4, maxFiles: 4, maxDepth: 0, maxFileBytes: 4, maxBytes: 4,
		}
		if err := walkLegacyDirectory(
			t.Context(), tree, ".", filepath.Join(root, "elsewhere"), nil, budget,
			func(string) error { return nil },
		); err == nil {
			t.Fatal("nested directory ignored depth budget")
		}
	})

	t.Run("file budget and exclusion", func(t *testing.T) {
		tree := t.TempDir()
		file := filepath.Join(tree, "file")
		writeMigrationFile(t, file, []byte("legacy"))
		if err := walkLegacyDirectory(
			t.Context(), tree, ".", filepath.Join(root, "elsewhere"), nil,
			&backupBudget{maxEntries: 1, maxFiles: 2, maxDepth: 2, maxFileBytes: 8, maxBytes: 8},
			func(string) error { return nil },
		); err == nil {
			t.Fatal("tree file ignored entry budget")
		}
		visited := false
		if err := walkLegacyDirectory(
			t.Context(), tree, ".", filepath.Join(root, "elsewhere"),
			map[string]struct{}{backupPathKey(file): {}}, newBackupBudget(),
			func(string) error { visited = true; return nil },
		); err != nil || visited {
			t.Fatalf("excluded tree file = visited %t, %v", visited, err)
		}
		canary := errors.New("tree visitor canary")
		if err := walkLegacyDirectory(
			t.Context(), tree, ".", filepath.Join(root, "elsewhere"), nil, newBackupBudget(),
			func(string) error { return canary },
		); !errors.Is(err, canary) {
			t.Fatalf("tree visitor error = %v", err)
		}
	})

	t.Run("directory identity changes after traversal", func(t *testing.T) {
		tree := filepath.Join(t.TempDir(), "tree")
		file := filepath.Join(tree, "file")
		writeMigrationFile(t, file, []byte("legacy"))
		err := walkLegacyDirectory(
			t.Context(), tree, ".", filepath.Join(root, "elsewhere"), nil, newBackupBudget(),
			func(path string) error {
				if removeErr := os.Remove(path); removeErr != nil {
					return removeErr
				}
				return os.Remove(tree)
			},
		)
		if err == nil {
			t.Fatalf("removed tree walk = %v", err)
		}
	})

	t.Run("child disappears before inspection", func(t *testing.T) {
		tree := filepath.Join(t.TempDir(), "tree")
		file := filepath.Join(tree, "file")
		writeMigrationFile(t, file, []byte("legacy"))
		ctx := &actionAfterMigrationErrChecks{
			Context: context.Background(), allowed: 1,
			action: func() { _ = os.Remove(file) },
		}
		if err := walkLegacyDirectory(
			ctx, tree, ".", filepath.Join(root, "elsewhere"), nil, newBackupBudget(),
			func(string) error { return nil },
		); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("removed child walk = %v", err)
		}
	})

	t.Run("directory renamed before final identity check", func(t *testing.T) {
		tree := filepath.Join(t.TempDir(), "tree")
		file := filepath.Join(tree, "file")
		moved := tree + "-moved"
		writeMigrationFile(t, file, []byte("legacy"))
		err := walkLegacyDirectory(
			t.Context(), tree, ".", filepath.Join(root, "elsewhere"), nil, newBackupBudget(),
			func(string) error { return os.Rename(tree, moved) },
		)
		if err == nil || !strings.Contains(err.Error(), "changed during traversal") {
			t.Fatalf("renamed tree walk = %v", err)
		}
	})
}

type cancelAfterMigrationErrChecks struct {
	context.Context
	allowed int
}

func (ctx *cancelAfterMigrationErrChecks) Err() error {
	if ctx.allowed > 0 {
		ctx.allowed--
		return nil
	}
	return context.Canceled
}

type actionAfterMigrationErrChecks struct {
	context.Context
	allowed int
	action  func()
	done    bool
}

func (ctx *actionAfterMigrationErrChecks) Err() error {
	if ctx.allowed > 0 {
		ctx.allowed--
		return nil
	}
	if !ctx.done {
		ctx.done = true
		ctx.action()
	}
	return nil
}
