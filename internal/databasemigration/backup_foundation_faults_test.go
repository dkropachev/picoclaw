//nolint:govet // Independent fault assertions intentionally use narrow error scopes.
package databasemigration

import (
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
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

func TestBackupFoundationHashAndReadPrivacyFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode/link assertions")
	}
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "public mode",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "hard link",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Link(path, filepath.Join(t.TempDir(), "alias")); err != nil {
					t.Skipf("hard links unavailable: %v", err)
				}
			},
		},
	} {
		t.Run("hash "+test.name, func(t *testing.T) {
			path := filepath.Join(migrationHome(t), "payload")
			writeMigrationFile(t, path, []byte("payload"))
			test.mutate(t, path)
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			identity, err := backupExistingIdentity(path, fileidentity.ObjectTypeRegular)
			if err != nil {
				t.Fatal(err)
			}
			digest, size, hashErr := hashBackupFile(nil, path, info, identity, info.Size())
			if digest != "" || size != 0 || hashErr == nil {
				t.Fatalf("unsafe hash = %q, %d, %v", digest, size, hashErr)
			}
		})
		t.Run("read "+test.name, func(t *testing.T) {
			path := filepath.Join(migrationHome(t), "control")
			writeMigrationFile(t, path, []byte("control"))
			test.mutate(t, path)
			if payload, err := readPrivateBackupFile(path, 64); payload != nil || err == nil {
				t.Fatalf("unsafe control read = %q, %v", payload, err)
			}
		})
	}
	missing := filepath.Join(migrationHome(t), "missing")
	if payload, err := readPrivateBackupFile(missing, 64); payload != nil || err == nil {
		t.Fatalf("missing control read = %q, %v", payload, err)
	}
	if digest, size, err := hashBackupFile(
		nil, missing, nil, fileidentity.Identity{}, 0,
	); digest != "" || size != 0 || err == nil {
		t.Fatalf("missing hash = %q, %d, %v", digest, size, err)
	}
}

func TestBackupFoundationHashBindsExpectedPhysicalFile(t *testing.T) {
	root := migrationHome(t)
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	writeMigrationFile(t, first, []byte("payload"))
	writeMigrationFile(t, second, []byte("payload"))
	stamp := time.Unix(1_700_000_000, 0)
	for _, path := range []string{first, second} {
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	firstInfo, err := os.Lstat(first)
	if err != nil {
		t.Fatal(err)
	}
	secondInfo, err := os.Lstat(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstInfo.Size() != secondInfo.Size() || firstInfo.Mode() != secondInfo.Mode() ||
		!firstInfo.ModTime().Equal(secondInfo.ModTime()) || os.SameFile(firstInfo, secondInfo) {
		t.Fatal("physical-file fixture metadata is not collision-equivalent")
	}
	firstIdentity, err := backupExistingIdentity(first, fileidentity.ObjectTypeRegular)
	if err != nil {
		t.Fatal(err)
	}
	if digest, size, err := hashBackupFile(
		t.Context(), second, firstInfo, firstIdentity, firstInfo.Size(),
	); digest != "" || size != 0 || err == nil {
		t.Fatalf("different physical file matched expected metadata: %q, %d, %v", digest, size, err)
	}
}

func TestBackupFoundationFilesystemPermissionFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission assertions")
	}
	root := migrationHome(t)
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if err := ensurePrivateBackupDirectory(filepath.Join(locked, "child")); err == nil {
		t.Fatal("private directory was created through an unreadable parent")
	}
	if err := walkLegacyInputs(
		nil, filepath.Join(locked, "legacy"), root, nil, newBackupBudget(), func(string) error {
			return nil
		},
	); err == nil {
		t.Fatal("legacy walk accepted an unreadable parent")
	}
	if _, _, err := openPinnedBackupParent(filepath.Join(locked, "child")); err == nil {
		t.Fatal("pinned parent open accepted an unreadable directory")
	}
}

func TestBackupFoundationPrivateDirectoryOperationFaults(t *testing.T) {
	canary := errors.New("private directory operation canary")
	t.Run("relative path before operations", func(t *testing.T) {
		ops := defaultEnsureBackupDirectoryOps()
		called := false
		ops.validate = func(string) error {
			called = true
			return nil
		}
		if err := ensurePrivateBackupDirectoryWithOps("relative", ops); err == nil || called {
			t.Fatalf("relative private directory reached operations: called=%t err=%v", called, err)
		}
	})
	for _, test := range []struct {
		name   string
		mutate func(*ensureBackupDirectoryOps)
	}{
		{name: "ancestor", mutate: func(ops *ensureBackupDirectoryOps) {
			ops.validate = func(string) error { return canary }
		}},
		{name: "ensure", mutate: func(ops *ensureBackupDirectoryOps) {
			ops.ensure = func(string) error { return canary }
		}},
		{name: "lstat", mutate: func(ops *ensureBackupDirectoryOps) {
			ops.lstat = func(string) (os.FileInfo, error) { return nil, canary }
		}},
		{name: "resolve", mutate: func(ops *ensureBackupDirectoryOps) {
			ops.resolve = func(string) (string, error) { return "", canary }
		}},
		{name: "absolute", mutate: func(ops *ensureBackupDirectoryOps) {
			ops.absolute = func(string) (string, error) { return "", canary }
		}},
		{name: "secure", mutate: func(ops *ensureBackupDirectoryOps) {
			ops.secure = func(string) error { return canary }
		}},
		{name: "sync", mutate: func(ops *ensureBackupDirectoryOps) {
			ops.sync = func(string) error { return canary }
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(migrationHome(t), "private")
			ops := defaultEnsureBackupDirectoryOps()
			test.mutate(&ops)
			err := ensurePrivateBackupDirectoryWithOps(path, ops)
			if err == nil || test.name != "lstat" && !errors.Is(err, canary) {
				t.Fatalf("faulted private directory = %v", err)
			}
		})
	}

	t.Run("resolved identity mismatch", func(t *testing.T) {
		path := filepath.Join(migrationHome(t), "private")
		ops := defaultEnsureBackupDirectoryOps()
		calls := 0
		ops.absolute = func(value string) (string, error) {
			calls++
			if calls == 1 {
				return path, nil
			}
			return path + ".other", nil
		}
		if err := ensurePrivateBackupDirectoryWithOps(path, ops); err == nil ||
			!strings.Contains(err.Error(), "symlink") {
			t.Fatalf("resolved private directory mismatch = %v", err)
		}
	})
}

func TestBackupFoundationCopyRejectsOutputHandleIdentityFailure(t *testing.T) {
	root := migrationHome(t)
	source := filepath.Join(root, "source")
	writeMigrationFile(t, source, []byte("payload"))
	archive := filepath.Join(root, "archive")
	if err := ensurePrivateBackupDirectory(archive); err != nil {
		t.Fatal(err)
	}
	ops := defaultBackupCopyOps()
	originalOpened := ops.opened
	openedCalls := 0
	canary := errors.New("output identity unavailable")
	ops.opened = func(file *os.File) (fileidentity.Identity, fileidentity.ObjectType, error) {
		openedCalls++
		if openedCalls == 2 {
			return fileidentity.Identity{}, fileidentity.ObjectTypeRegular, canary
		}
		return originalOpened(file)
	}
	record, err := copyBackupFileWithOps(
		context.Background(), archive, "global/auth", "legacy", source,
		"payload", newBackupBudget(), ops,
	)
	if record.StoreID != "" || !errors.Is(err, canary) || openedCalls != 2 {
		t.Fatalf("output-identity copy = %#v, calls=%d, %v", record, openedCalls, err)
	}
	_ = os.Remove(filepath.Join(archive, "payload"))
}

func TestBackupFoundationCopyRejectsDestinationReplacementDuringRehash(t *testing.T) {
	root := migrationHome(t)
	source := filepath.Join(root, "source")
	writeMigrationFile(t, source, []byte("payload"))
	archive := filepath.Join(root, "archive")
	if err := ensurePrivateBackupDirectory(archive); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(archive, "payload")
	displaced := filepath.Join(archive, "displaced")
	ops := defaultBackupCopyOps()
	copyBytes := ops.copy
	ops.copy = func(
		_ context.Context,
		writer io.Writer,
		digest hash.Hash,
		reader io.Reader,
		limit int64,
	) (int64, error) {
		return copyBytes(context.Background(), writer, digest, reader, limit)
	}
	duringRehash := &actionAfterMigrationErrChecks{
		Context: context.Background(),
		action: func() {
			if err := os.Rename(destination, displaced); err != nil {
				t.Fatal(err)
			}
			writeMigrationFile(t, destination, []byte("forged!"))
		},
	}
	record, err := copyBackupFileWithOps(
		duringRehash, archive, "global/auth", "legacy", source,
		"payload", newBackupBudget(), ops,
	)
	if err == nil || record.StoreID != "" || !duringRehash.done {
		t.Fatalf("rehash replacement copy = %#v, action=%t, %v", record, duringRehash.done, err)
	}
	if payload, readErr := os.ReadFile(destination); readErr != nil || string(payload) != "forged!" {
		t.Fatalf("replacement destination was removed: %q, %v", payload, readErr)
	}
	if payload, readErr := os.ReadFile(displaced); readErr != nil || string(payload) != "payload" {
		t.Fatalf("displaced owned output changed: %q, %v", payload, readErr)
	}
}

func TestBackupFoundationCopyRejectsSourceIdentityFailure(t *testing.T) {
	root := migrationHome(t)
	source := filepath.Join(root, "source")
	writeMigrationFile(t, source, []byte("payload"))
	archive := filepath.Join(root, "archive")
	if err := ensurePrivateBackupDirectory(archive); err != nil {
		t.Fatal(err)
	}
	canary := errors.New("source identity unavailable")
	ops := defaultBackupCopyOps()
	ops.identity = func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
		return fileidentity.Identity{}, 0, false, canary
	}
	record, err := copyBackupFileWithOps(
		t.Context(), archive, "global/auth", "legacy", source,
		"payload", newBackupBudget(), ops,
	)
	if record.StoreID != "" || !errors.Is(err, canary) {
		t.Fatalf("source-identity copy = %#v, %v", record, err)
	}
}

func TestBackupFoundationCopyReturnsSelfValidatingIdentity(t *testing.T) {
	root := migrationHome(t)
	source := filepath.Join(root, "source")
	writeMigrationFile(t, source, []byte("payload"))
	archive := filepath.Join(root, "archive")
	if err := ensurePrivateBackupDirectory(archive); err != nil {
		t.Fatal(err)
	}
	record, err := copyBackupFile(
		t.Context(), archive, "global/auth", "legacy", source,
		"payload", newBackupBudget(),
	)
	if err != nil || record.SourceIdentity == "" {
		t.Fatalf("self-validating copied record = %#v, %v", record, err)
	}
	identity, objectType, exists, err := fileidentity.ExistingWithType(source)
	if err != nil || !exists || objectType != fileidentity.ObjectTypeRegular ||
		record.SourceIdentity != identity.String() {
		t.Fatalf("copied source identity = %q, %#v, %v", record.SourceIdentity, identity, err)
	}
}

func TestBackupFoundationPreparedHashAndDestinationBoundaries(t *testing.T) {
	path := filepath.Join(migrationHome(t), "payload")
	writeMigrationFile(t, path, []byte("payload"))
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if digest, err := hashPreparedGenerationMember(t.Context(), file, 8); digest != "" || err == nil {
		t.Fatalf("short prepared hash = %q, %v", digest, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if digest, err := hashPreparedGenerationMember(t.Context(), file, 7); digest != "" || err == nil {
		t.Fatalf("closed prepared hash = %q, %v", digest, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	file, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if digest, err := hashPreparedGenerationMember(canceled, file, 7); digest != "" ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("canceled prepared hash = %q, %v", digest, err)
	}

	root := filepath.Join(migrationHome(t), "legacy")
	if destination, err := legacyBackupDestination("store", 0, root, root); err != nil ||
		!strings.HasSuffix(destination, filepath.Join("root-000000", "root")) {
		t.Fatalf("legacy root destination = %q, %v", destination, err)
	}
	if destination, err := legacyBackupDestination(
		"store", 0, root, filepath.Join(root, "nested", "file"),
	); err != nil || !strings.HasSuffix(destination, filepath.Join("tree", "nested", "file")) {
		t.Fatalf("nested legacy destination = %q, %v", destination, err)
	}
	if destination, err := legacyBackupDestination(
		"store", 0, root, filepath.Join(filepath.Dir(root), "outside"),
	); destination != "" || err == nil {
		t.Fatalf("outside legacy destination = %q, %v", destination, err)
	}
}

func TestBackupFoundationIdentityAndMetadataBoundaries(t *testing.T) {
	root := migrationHome(t)
	identity, err := pinPrivateBackupDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePinnedPrivateBackupDirectory(root, identity); err != nil {
		t.Fatal(err)
	}
	if identity, err := backupPathMatchesOpened(
		root, nil, fileidentity.ObjectTypeDirectory,
	); identity.Valid() || err == nil {
		t.Fatalf("nil handle path match = %#v, %v", identity, err)
	}
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	writeMigrationFile(t, first, []byte("first"))
	writeMigrationFile(t, second, []byte("second"))
	secondFile, err := os.Open(second)
	if err != nil {
		t.Fatal(err)
	}
	defer secondFile.Close()
	if identity, err := backupPathMatchesOpened(
		first, secondFile, fileidentity.ObjectTypeRegular,
	); identity.Valid() || err == nil || !strings.Contains(err.Error(), "differ") {
		t.Fatalf("different path/handle match = %#v, %v", identity, err)
	}
	if err := validateBackupPlatformFile(nil, nil, 0o600); err == nil {
		t.Fatal("nil platform file metadata was accepted")
	}
	if err := validateBackupSourceFile(nil, nil); err == nil {
		t.Fatal("nil source metadata was accepted")
	}
}

func TestBackupFoundationCopyUsesBackgroundForNilContext(t *testing.T) {
	written, err := copyWithContext(
		nil, io.Discard, sha256.New(), strings.NewReader("x"), 1,
	)
	if err != nil || written != 1 {
		t.Fatalf("nil-context bounded copy = %d, %v", written, err)
	}
}

func TestBackupFoundationPrivateReaderFaultSeams(t *testing.T) {
	root := migrationHome(t)
	path := filepath.Join(root, "control")
	writeMigrationFile(t, path, []byte("control"))
	canary := errors.New("private reader canary")
	for _, test := range []struct {
		name   string
		mutate func(*backupReadOps)
	}{
		{
			name: "lstat",
			mutate: func(ops *backupReadOps) {
				ops.lstat = func(string) (os.FileInfo, error) { return nil, canary }
			},
		},
		{
			name: "private metadata",
			mutate: func(ops *backupReadOps) {
				ops.validatePrivateFile = func(string, os.FileInfo) error { return canary }
			},
		},
		{
			name: "open",
			mutate: func(ops *backupReadOps) {
				ops.openPinned = func(string, bool) (*os.File, os.FileInfo, error) {
					return nil, nil, canary
				}
			},
		},
		{
			name: "opened identity",
			mutate: func(ops *backupReadOps) {
				ops.openedIdentity = func(
					*os.File, fileidentity.ObjectType,
				) (fileidentity.Identity, error) {
					return fileidentity.Identity{}, canary
				}
			},
		},
		{
			name: "platform metadata",
			mutate: func(ops *backupReadOps) {
				ops.validatePlatformFile = func(os.FileInfo, *os.File, uint32) error {
					return canary
				}
			},
		},
		{
			name: "read",
			mutate: func(ops *backupReadOps) {
				ops.readAll = func(io.Reader) ([]byte, error) { return nil, canary }
			},
		},
		{
			name: "final stat",
			mutate: func(ops *backupReadOps) {
				ops.stat = func(*os.File) (os.FileInfo, error) { return nil, canary }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ops := defaultBackupReadOps()
			test.mutate(&ops)
			payload, identity, exists, err := readPinnedPrivateBackupFileWithOps(
				path, 64, false, ops,
			)
			if payload != nil || identity.Valid() || exists || !errors.Is(err, canary) {
				t.Fatalf("faulted private read = %q, %#v, %t, %v", payload, identity, exists, err)
			}
		})
	}
}

func TestBackupFoundationDurableRemovalRejectsMismatchedObjects(t *testing.T) {
	parent := migrationHome(t)
	tree := filepath.Join(parent, "tree")
	if err := os.Mkdir(tree, 0o700); err != nil {
		t.Fatal(err)
	}
	treeIdentity := backupRemovalDirectoryIdentity(t, tree)
	other := filepath.Join(parent, "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	wrongIdentity := backupRemovalDirectoryIdentity(t, other)
	parentRoot, err := os.OpenRoot(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer parentRoot.Close()
	treeRoot, err := parentRoot.OpenRoot("tree")
	if err != nil {
		t.Fatal(err)
	}
	defer treeRoot.Close()
	if removeErr := removeBackupTreeDurable(
		tree, parentRoot, "tree", treeRoot, wrongIdentity,
	); removeErr == nil {
		t.Fatal("durable tree removal accepted the wrong identity")
	}
	if _, statErr := os.Lstat(tree); statErr != nil {
		t.Fatalf("wrong-identity tree was removed: %v", statErr)
	}
	wrongParentRoot, err := os.OpenRoot(other)
	if err != nil {
		t.Fatal(err)
	}
	defer wrongParentRoot.Close()
	if removeErr := removeBackupTreeDurable(
		tree, wrongParentRoot, "missing", treeRoot, treeIdentity,
	); removeErr == nil {
		t.Fatal("durable tree removal accepted a mismatched parent handle")
	}

	filePath := filepath.Join(parent, "file")
	writeMigrationFile(t, filePath, []byte("file"))
	file, err := os.Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	fileIdentity, objectType, exists, err := fileidentity.ExistingWithType(filePath)
	if err != nil || !exists || objectType != fileidentity.ObjectTypeRegular {
		t.Fatal(err)
	}
	if err := removeBackupFileDurable(
		filePath, parentRoot, "file", nil, fileIdentity,
	); err == nil {
		t.Fatal("durable file removal accepted a nil handle")
	}
	if err := removeBackupFileDurableWithSync(
		filePath, parentRoot, "missing", file, fileIdentity, func(string) error { return nil },
	); err == nil {
		t.Fatal("durable file removal accepted a mismatched parent leaf")
	}
	if err := os.Rename(filePath, filePath+".original"); err != nil {
		t.Fatal(err)
	}
	writeMigrationFile(t, filePath, []byte("replacement"))
	if err := removeBackupFileDurable(
		filePath, parentRoot, "file", file, fileIdentity,
	); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("durable file replacement removal = %v", err)
	}
	if _, err := os.Lstat(filePath); err != nil {
		t.Fatalf("replacement file was removed: %v", err)
	}
}

func TestBackupFoundationAdditionalDirectoryAndReaderTransitions(t *testing.T) {
	canary := errors.New("additional foundation transition")
	t.Run("final ancestor validation", func(t *testing.T) {
		path := filepath.Join(migrationHome(t), "private")
		ops := defaultEnsureBackupDirectoryOps()
		validate := ops.validate
		calls := 0
		ops.validate = func(path string) error {
			calls++
			if calls == 2 {
				return canary
			}
			return validate(path)
		}
		if err := ensurePrivateBackupDirectoryWithOps(path, ops); !errors.Is(err, canary) {
			t.Fatalf("final ancestor transition = %v", err)
		}
	})

	root := migrationHome(t)
	path := filepath.Join(root, "control")
	writeMigrationFile(t, path, []byte("control"))
	t.Run("opened private metadata", func(t *testing.T) {
		ops := defaultBackupReadOps()
		validate := ops.validatePrivateFile
		calls := 0
		ops.validatePrivateFile = func(path string, info os.FileInfo) error {
			calls++
			if calls == 2 {
				return canary
			}
			return validate(path, info)
		}
		if _, _, _, err := readPinnedPrivateBackupFileWithOps(path, 64, false, ops); !errors.Is(err, canary) {
			t.Fatalf("opened metadata transition = %v", err)
		}
	})
	t.Run("post-read size", func(t *testing.T) {
		ops := defaultBackupReadOps()
		ops.readAll = func(io.Reader) ([]byte, error) {
			return []byte(strings.Repeat("x", 65)), nil
		}
		if _, _, _, err := readPinnedPrivateBackupFileWithOps(path, 64, false, ops); err == nil {
			t.Fatal("post-read oversized private file was accepted")
		}
	})
	t.Run("allowed missing", func(t *testing.T) {
		payload, identity, exists, err := readPinnedPrivateBackupFileWithOps(
			filepath.Join(root, "missing"), 64, true, defaultBackupReadOps(),
		)
		if payload != nil || identity.Valid() || exists || err != nil {
			t.Fatalf("allowed missing private file = %q, %#v, %t, %v", payload, identity, exists, err)
		}
	})
}

func TestBackupFoundationRemainingFaultSeams(t *testing.T) {
	if entries, err := readBackupDirectoryBatch(nil); entries != nil || err == nil {
		t.Fatalf("nil directory reader = %#v, %v", entries, err)
	}
	root := migrationHome(t)
	source := filepath.Join(root, "source")
	writeMigrationFile(t, source, []byte("payload"))
	archive := filepath.Join(root, "archive")
	if err := ensurePrivateBackupDirectory(archive); err != nil {
		t.Fatal(err)
	}

	t.Run("source metadata", func(t *testing.T) {
		ops := defaultBackupCopyOps()
		stat := ops.stat
		calls := 0
		ops.stat = func(file *os.File) (os.FileInfo, error) {
			calls++
			info, err := stat(file)
			if calls == 1 && err == nil {
				return foundationNoSysInfo{FileInfo: info}, nil
			}
			return info, err
		}
		if record, err := copyBackupFileWithOps(
			t.Context(), archive, "global/auth", "legacy", source,
			"metadata", newBackupBudget(), ops,
		); record.StoreID != "" || err == nil {
			t.Fatalf("source metadata copy = %#v, %v", record, err)
		}
	})

	t.Run("final destination identity", func(t *testing.T) {
		ops := defaultBackupCopyOps()
		identity := ops.identity
		calls := 0
		canary := errors.New("final destination identity")
		ops.identity = func(path string) (
			fileidentity.Identity, fileidentity.ObjectType, bool, error,
		) {
			calls++
			if calls == 4 {
				return fileidentity.Identity{}, 0, false, canary
			}
			return identity(path)
		}
		if record, err := copyBackupFileWithOps(
			t.Context(), archive, "global/auth", "legacy", source,
			"identity", newBackupBudget(), ops,
		); record.StoreID != "" || !errors.Is(err, canary) {
			t.Fatalf("final destination identity copy = %#v, %v", record, err)
		}
	})

	info, err := os.Lstat(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateBackupSourceFile(foundationNoSysInfo{FileInfo: info}, nil); err == nil {
		t.Fatal("source metadata without platform identity was accepted")
	}
	if err := validateBackupManifestLimit(validManifestValidationFixture(t), 4_700); err == nil ||
		!strings.Contains(err.Error(), "metadata budget") {
		t.Fatalf("file metadata aggregate budget = %v", err)
	}
}

type foundationNoSysInfo struct {
	os.FileInfo
}

func (foundationNoSysInfo) Sys() any { return nil }
