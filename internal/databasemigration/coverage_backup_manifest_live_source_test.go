//nolint:govet // Independent filesystem boundary assertions intentionally use narrow errors.
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
	"unicode/utf8"

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestBackupBudgetAndPathHelpers(t *testing.T) {
	if err := (*backupBudget)(nil).enter("."); err == nil {
		t.Fatal("nil traversal budget succeeded")
	}
	for _, value := range []string{string(os.PathSeparator), "..", filepath.Join("..", "escape")} {
		if err := newBackupBudget().enter(value); err == nil {
			t.Errorf("backup budget accepted %q", value)
		}
	}
	if err := (*backupBudget)(nil).reserveFile(0); err == nil {
		t.Fatal("nil file budget succeeded")
	}
	if err := newBackupBudget().reserveFile(-1); err == nil {
		t.Fatal("negative file size succeeded")
	}
	for _, budget := range []*backupBudget{
		{maxFiles: 1, maxFileBytes: 1, maxBytes: 0},
		{maxFiles: 1, maxFileBytes: 2, maxBytes: 1},
	} {
		if err := budget.reserveFile(2); err == nil {
			t.Fatalf("invalid file reservation succeeded: %#v", budget)
		}
	}

	root := migrationHome(t)
	for _, value := range []string{" padded ", "bad\x00path"} {
		if path, err := validateBackupParent(value, root, nil); path != "" || err == nil {
			t.Errorf("validateBackupParent(%q) = %q, %v", value, path, err)
		}
	}
	if path, err := validateBackupParent("", root, nil); err != nil || path != filepath.Join(root, "backups") {
		t.Fatalf("default backup parent = %q, %v", path, err)
	}
	if path, err := validateBackupParent(filepath.Join(root, "custom"), root, nil); err != nil || !filepath.IsAbs(path) {
		t.Fatalf("custom backup parent = %q, %v", path, err)
	}

	base := filepath.Join(root, "legacy")
	for _, test := range []struct {
		source string
		want   string
		inside bool
	}{
		{source: base, want: ".", inside: true},
		{source: filepath.Join(base, "nested", "file"), want: filepath.Join("nested", "file"), inside: true},
		{source: filepath.Join(root, "other")},
	} {
		got, inside, err := legacySourceRelative(base, test.source)
		if err != nil || got != test.want || inside != test.inside {
			t.Errorf("legacySourceRelative(%q) = %q, %t, %v", test.source, got, inside, err)
		}
	}
	for _, value := range []string{".", "..", filepath.Join("..", "escape"), string(os.PathSeparator)} {
		if path, err := backupFilePath(root, value); path != "" || err == nil {
			t.Errorf("backupFilePath(%q) = %q, %v", value, path, err)
		}
	}
	if path, err := backupFilePath(root, filepath.Join("stores", "auth")); err != nil ||
		path != filepath.Join(root, "stores", "auth") {
		t.Fatalf("backupFilePath(valid) = %q, %v", path, err)
	}
}

func TestBackupSessionFinishAndErrorBounds(t *testing.T) {
	if (*backupSession)(nil).hasLegacy("global/auth") {
		t.Fatal("nil backup session reported legacy input")
	}
	if err := (*backupSession)(nil).finish("complete", nil); err != nil {
		t.Fatal(err)
	}
	if err := (&backupSession{}).finish("complete", nil); err != nil {
		t.Fatal(err)
	}
	if got := boundedBackupError(nil); got != "" {
		t.Fatalf("boundedBackupError(nil) = %q", got)
	}
	short := errors.New("short")
	if got := boundedBackupError(short); got != short.Error() {
		t.Fatalf("bounded short error = %q", got)
	}
	long := errors.New(strings.Repeat("x", backupMaxErrorBytes-1) + "€tail")
	bounded := boundedBackupError(long)
	if len(bounded) > backupMaxErrorBytes || !utf8.ValidString(bounded) || strings.Contains(bounded, "tail") {
		t.Fatalf("bounded UTF-8 error length=%d valid=%t", len(bounded), utf8.ValidString(bounded))
	}

	root := filepath.Join(t.TempDir(), "backup")
	if err := ensurePrivateBackupDirectory(root); err != nil {
		t.Fatal(err)
	}
	session := &backupSession{root: root, manifest: BackupManifest{
		Version: backupManifestVersion, CreatedAt: time.Unix(1, 0).UTC(),
		Stores:             []BackupStoreManifest{{StoreID: "global/auth"}},
		CatalogGenerations: []string{filepath.Join(t.TempDir(), "store.db")},
	}}
	if err := session.finish("failed", short); err != nil {
		t.Fatal(err)
	}
	if session.manifest.Outcome != "failed" || session.manifest.Error != "short" {
		t.Fatalf("failed manifest = %#v", session.manifest)
	}
	if err := session.finish("complete", nil); err != nil || session.manifest.Error != "" {
		t.Fatalf("completed finish = %#v, %v", session.manifest, err)
	}
	if session.hasLegacy("global/auth") {
		t.Fatal("empty backup manifest reported legacy input")
	}
	hashPath := filepath.Join(root, backupManifestHash)
	if err := os.Remove(hashPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(hashPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := session.finish("complete", nil); err == nil {
		t.Fatal("finish replaced directory at manifest hash path")
	}
	badRoot := filepath.Join(t.TempDir(), "file")
	writeMigrationFile(t, badRoot, nil)
	if err := (&backupSession{root: badRoot}).finish("failed", short); err == nil {
		t.Fatal("finish into regular file succeeded")
	}
	unencodable := &backupSession{root: root, manifest: BackupManifest{
		CreatedAt: time.Date(10_000, time.January, 1, 0, 0, 0, 0, time.UTC),
	}}
	if err := unencodable.finish("failed", nil); err == nil {
		t.Fatal("finish encoded invalid timestamp")
	}
}

func TestLegacyTraversalBoundaries(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	backupRoot := filepath.Join(root, "backups", "active")
	writeMigrationFile(t, filepath.Join(backupRoot, "ignored"), []byte("backup"))
	regular := filepath.Join(root, "input.json")
	writeMigrationFile(t, regular, []byte("legacy"))
	var visited []string
	visit := func(path string) error { visited = append(visited, path); return nil }
	if err := walkLegacyInputs(ctx, filepath.Join(root, "missing"), backupRoot, nil, newBackupBudget(), visit); err != nil {
		t.Fatal(err)
	}
	if err := walkLegacyInputs(ctx, regular, backupRoot, nil, newBackupBudget(), visit); err != nil || len(visited) != 1 {
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
			name: "relative path", keepsFile: true, wrapsCanary: true,
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
		real := filepath.Join(t.TempDir(), "real")
		if err := os.Mkdir(real, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(real, link); err == nil {
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

func TestSnapshotFaultAndOrderingBoundaries(t *testing.T) {
	fixed := time.Unix(1_800_000_000, 123).UTC()
	newEngine := func() *Engine { return &Engine{now: func() time.Time { return fixed }} }
	if session, err := snapshotBackup(t.Context(), nil, t.TempDir(), nil, nil, ""); session != nil || err == nil {
		t.Fatalf("snapshot without clock = %#v, %v", session, err)
	}

	t.Run("backup parent unavailable", func(t *testing.T) {
		home := migrationHome(t)
		parent := filepath.Join(home, "parent-file")
		writeMigrationFile(t, parent, nil)
		if session, err := newEngine().snapshot(t.Context(), home, nil, nil, parent); session != nil || err == nil {
			t.Fatalf("snapshot(parent file) = %#v, %v", session, err)
		}
	})

	t.Run("duplicate backup identity", func(t *testing.T) {
		home := migrationHome(t)
		parent := filepath.Join(home, "backups")
		spec := storecatalog.Spec{ID: "global/test", Path: filepath.Join(home, "store.db")}
		first, err := newEngine().snapshot(
			t.Context(), home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec}, parent,
		)
		if err != nil || first == nil {
			t.Fatalf("first snapshot = %#v, %v", first, err)
		}
		if second, err := newEngine().snapshot(
			t.Context(), home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec}, parent,
		); second != nil || err == nil {
			t.Fatalf("duplicate snapshot = %#v, %v", second, err)
		}
	})

	t.Run("known generation inspection", func(t *testing.T) {
		home := migrationHome(t)
		overlong := filepath.Join(home, strings.Repeat("x", 4096), "store.db")
		spec := storecatalog.Spec{ID: "global/test", Path: overlong}
		if session, err := newEngine().snapshot(
			t.Context(), home, []storecatalog.Spec{spec}, nil, filepath.Join(home, "backups"),
		); session == nil || err == nil {
			t.Fatalf("known overlong generation = %#v, %v", session, err)
		}
	})

	for _, test := range []struct {
		name  string
		setup func(*testing.T, string) storecatalog.Spec
	}{
		{
			name: "selected generation inspection",
			setup: func(_ *testing.T, home string) storecatalog.Spec {
				return storecatalog.Spec{
					ID: "global/test", Path: filepath.Join(home, strings.Repeat("x", 4096), "store.db"),
				}
			},
		},
		{
			name: "unsafe main",
			setup: func(t *testing.T, home string) storecatalog.Spec {
				path := filepath.Join(home, "store.db")
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				return storecatalog.Spec{ID: "global/test", Path: path}
			},
		},
		{
			name: "orphan sidecar",
			setup: func(t *testing.T, home string) storecatalog.Spec {
				path := filepath.Join(home, "store.db")
				writeMigrationFile(t, path+"-wal", []byte("wal"))
				return storecatalog.Spec{ID: "global/test", Path: path}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := migrationHome(t)
			spec := test.setup(t, home)
			session, err := newEngine().snapshot(
				t.Context(), home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec},
				filepath.Join(home, "backups"),
			)
			if session == nil || err == nil ||
				test.name != "selected generation inspection" && session.manifest.Outcome != "failed" {
				t.Fatalf("fault snapshot = %#v, %v", session, err)
			}
		})
	}

	t.Run("canceled", func(t *testing.T) {
		home := migrationHome(t)
		spec := storecatalog.Spec{ID: "global/test", Path: filepath.Join(home, "store.db")}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		session, err := newEngine().snapshot(
			ctx, home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec}, filepath.Join(home, "backups"),
		)
		if session == nil || !errors.Is(err, context.Canceled) || session.manifest.Outcome != "failed" {
			t.Fatalf("canceled snapshot = %#v, %v", session, err)
		}
	})

	t.Run("generation legacy alias", func(t *testing.T) {
		home := migrationHome(t)
		path := filepath.Join(home, "store.db")
		legacy := filepath.Join(home, "legacy.json")
		writeMigrationFile(t, path, []byte("generation"))
		if err := os.Link(path, legacy); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		spec := storecatalog.Spec{ID: "global/test", Path: path, LegacyRoots: []string{legacy}}
		if session, err := newEngine().snapshot(
			t.Context(), home, []storecatalog.Spec{spec}, []storecatalog.Spec{spec},
			filepath.Join(home, "backups"),
		); session == nil || err == nil || !strings.Contains(err.Error(), "aliases a database generation") {
			t.Fatalf("generation alias snapshot = %#v, %v", session, err)
		}
	})

	t.Run("duplicate and physical legacy aliases", func(t *testing.T) {
		home := migrationHome(t)
		first := filepath.Join(home, "first.json")
		second := filepath.Join(home, "second.json")
		writeMigrationFile(t, first, []byte("legacy"))
		if err := os.Link(first, second); err != nil {
			t.Skipf("hardlinks unavailable: %v", err)
		}
		dedup := storecatalog.Spec{
			ID: "global/test", Path: filepath.Join(home, "store.db"), LegacyRoots: []string{first, first},
		}
		for _, parent := range []string{
			filepath.Join(home, "dedup-backups"), filepath.Join(home, "alias-backups"),
		} {
			if err := ensurePrivateBackupDirectory(parent); err != nil {
				t.Fatal(err)
			}
		}
		session, err := newEngine().snapshot(
			t.Context(), home, []storecatalog.Spec{dedup}, []storecatalog.Spec{dedup},
			filepath.Join(home, "dedup-backups"),
		)
		if err != nil || len(session.manifest.Files) != 1 {
			t.Fatalf("deduplicated snapshot = %#v, %v", session, err)
		}
		aliased := dedup
		aliased.LegacyRoots = []string{first, second}
		if session, err := newEngine().snapshot(
			t.Context(), home, []storecatalog.Spec{aliased}, []storecatalog.Spec{aliased},
			filepath.Join(home, "alias-backups"),
		); session == nil || err == nil || !strings.Contains(err.Error(), "physical alias") {
			t.Fatalf("physical alias snapshot = %#v, %v", session, err)
		}
	})

	t.Run("canonical file ordering", func(t *testing.T) {
		home := migrationHome(t)
		alphaLegacy := filepath.Join(home, "alpha-a.json")
		zetaLegacy := filepath.Join(home, "alpha-z.json")
		alpha := storecatalog.Spec{
			ID: "global/alpha", Path: filepath.Join(home, "alpha.db"),
			LegacyRoots: []string{zetaLegacy, alphaLegacy},
		}
		zeta := storecatalog.Spec{ID: "global/zeta", Path: filepath.Join(home, "zeta.db")}
		for path, payload := range map[string]string{
			alpha.Path: "alpha database", zeta.Path: "zeta database",
			alphaLegacy: "alpha legacy", zetaLegacy: "zeta legacy",
		} {
			writeMigrationFile(t, path, []byte(payload))
		}
		session, err := newEngine().snapshot(
			t.Context(), home, []storecatalog.Spec{zeta, alpha}, []storecatalog.Spec{zeta, alpha},
			filepath.Join(home, "backups"),
		)
		if err != nil {
			t.Fatal(err)
		}
		keys := make([]string, len(session.manifest.Files))
		for index, record := range session.manifest.Files {
			keys[index] = record.StoreID + "\x00" + record.Role + "\x00" + record.Source
		}
		if !stringsAreSorted(keys) {
			t.Fatalf("snapshot file order = %#v", keys)
		}
	})

	t.Run("manifest timestamp encoding", func(t *testing.T) {
		home := migrationHome(t)
		engine := &Engine{now: func() time.Time {
			return time.Date(10_000, time.January, 1, 0, 0, 0, 0, time.UTC)
		}}
		session, err := engine.snapshot(t.Context(), home, nil, nil, filepath.Join(home, "backups"))
		if session == nil || err == nil || !strings.Contains(err.Error(), "manifest") {
			t.Fatalf("invalid timestamp snapshot = %#v, %v", session, err)
		}
	})
}

func stringsAreSorted(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index] < values[index-1] {
			return false
		}
	}
	return true
}

func TestBackupVerificationManifestAndFilesystemBoundaries(t *testing.T) {
	if err := (*backupSession)(nil).verify(t.Context()); err == nil {
		t.Fatal("nil backup verification succeeded")
	}
	if err := (&backupSession{}).verify(t.Context()); err == nil {
		t.Fatal("empty backup verification succeeded")
	}
	if err := (&backupSession{root: filepath.Join(t.TempDir(), "missing")}).verify(t.Context()); err == nil {
		t.Fatal("missing backup root verified")
	}
	rootFile := filepath.Join(t.TempDir(), "root-file")
	writeMigrationFile(t, rootFile, nil)
	if err := (&backupSession{root: rootFile}).verify(t.Context()); err == nil {
		t.Fatal("regular-file backup root verified")
	}

	t.Run("nil context", func(t *testing.T) {
		session, _ := verifiedBackupFixture(t, []byte("payload"))
		if err := session.verify(nil); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unencodable expected manifest", func(t *testing.T) {
		session, _ := verifiedBackupFixture(t, []byte("payload"))
		session.manifest.CreatedAt = time.Date(10_000, time.January, 1, 0, 0, 0, 0, time.UTC)
		if err := session.verify(t.Context()); err == nil {
			t.Fatal("unencodable in-memory manifest verified")
		}
	})

	for _, name := range []string{backupManifestName, backupManifestHash} {
		t.Run("missing "+name, func(t *testing.T) {
			session, _ := verifiedBackupFixture(t, []byte("payload"))
			if err := os.Remove(filepath.Join(session.root, name)); err != nil {
				t.Fatal(err)
			}
			if err := session.verify(t.Context()); err == nil {
				t.Fatalf("backup verified without %s", name)
			}
		})
	}

	t.Run("canceled file verification", func(t *testing.T) {
		session, _ := verifiedBackupFixture(t, []byte("payload"))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := session.verify(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled verification = %v", err)
		}
	})

	for _, test := range []struct {
		name          string
		rejectMarshal bool
		mutate        func(*backupSession, string)
	}{
		{
			name: "invalid relative path", rejectMarshal: true,
			mutate: func(session *backupSession, _ string) {
				session.manifest.Files[0].Backup = ".."
			},
		},
		{
			name: "missing backup file",
			mutate: func(session *backupSession, _ string) {
				session.manifest.Files[0].Backup = "files/missing"
			},
		},
		{
			name: "invalid source mode", rejectMarshal: true,
			mutate: func(session *backupSession, _ string) {
				session.manifest.Files[0].SourceMode = 1 << 16
			},
		},
		{
			name: "negative size", rejectMarshal: true,
			mutate: func(session *backupSession, _ string) {
				session.manifest.Files[0].Size = -1
			},
		},
		{
			name: "file size limit", rejectMarshal: true,
			mutate: func(session *backupSession, _ string) {
				session.manifest.Files[0].Size = backupMaxFileBytes + 1
			},
		},
		{
			name: "metadata mismatch",
			mutate: func(session *backupSession, _ string) {
				session.manifest.Files[0].Size++
			},
		},
		{
			name: "non-private metadata", rejectMarshal: true,
			mutate: func(session *backupSession, path string) {
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
				session.manifest.Files[0].Mode = 0o644
			},
		},
		{
			name: "hash mismatch",
			mutate: func(session *backupSession, _ string) {
				session.manifest.Files[0].SHA256 = strings.Repeat("0", sha256.Size*2)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			session, path := verifiedBackupFixture(t, []byte("payload"))
			test.mutate(session, path)
			finishErr := session.finish("snapshot_complete", nil)
			if test.rejectMarshal {
				if finishErr == nil {
					t.Fatal("invalid backup manifest was serialized")
				}
				return
			}
			if finishErr != nil {
				t.Fatal(finishErr)
			}
			if err := session.verify(t.Context()); err == nil {
				t.Fatal("invalid backup verified")
			}
		})
	}

	t.Run("duplicate manifest path", func(t *testing.T) {
		session, _ := verifiedBackupFixture(t, []byte("payload"))
		session.manifest.Files = append(session.manifest.Files, session.manifest.Files[0])
		if err := session.finish("snapshot_complete", nil); err == nil {
			t.Fatal("duplicate manifest path was serialized")
		}
	})

	t.Run("manifest file count limit", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "backup")
		if err := ensurePrivateBackupDirectory(root); err != nil {
			t.Fatal(err)
		}
		session := &backupSession{root: root, manifest: BackupManifest{
			Version: backupManifestVersion, CreatedAt: time.Unix(1, 0).UTC(),
			Files: make([]BackupFileManifest, backupMaxFiles+1),
		}}
		if err := session.finish("snapshot_complete", nil); err == nil ||
			!strings.Contains(err.Error(), "file count") {
			t.Fatalf("oversized manifest serialization = %v", err)
		}
	})

	t.Run("hash cancellation", func(t *testing.T) {
		session, _ := verifiedBackupFixture(t, []byte("payload"))
		ctx := &cancelAfterMigrationErrChecks{Context: context.Background(), allowed: 1}
		if err := session.verify(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("hash cancellation verification = %v", err)
		}
	})
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

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	digest, size, err := hashBackupFile(t.Context(), path, info, 7)
	if err != nil || size != 7 || digest == "" {
		t.Fatalf("hashBackupFile(valid) = %q, %d, %v", digest, size, err)
	}
	if digest, size, err := hashBackupFile(t.Context(), filepath.Join(root, "missing"), info, 7); digest != "" || size != 0 || err == nil {
		t.Fatalf("hashBackupFile(missing) = %q, %d, %v", digest, size, err)
	}
	if digest, size, err := hashBackupFile(t.Context(), path, nil, 7); digest != "" || size != 0 || err == nil {
		t.Fatalf("hashBackupFile(nil identity) = %q, %d, %v", digest, size, err)
	}
	other := filepath.Join(root, "other")
	writeMigrationFile(t, other, []byte("control"))
	if digest, size, err := hashBackupFile(t.Context(), path, mustMigrationInfo(t, other), 7); digest != "" || size != 0 || err == nil {
		t.Fatalf("hashBackupFile(wrong identity) = %q, %d, %v", digest, size, err)
	}
	if digest, size, err := hashBackupFile(t.Context(), path, info, 1); digest != "" || size != 0 || err == nil {
		t.Fatalf("hashBackupFile(limit) = %q, %d, %v", digest, size, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		permissive := mustMigrationInfo(t, path)
		if digest, size, err := hashBackupFile(t.Context(), path, permissive, 7); digest != "" || size != 0 || err == nil {
			t.Fatalf("hashBackupFile(non-private) = %q, %d, %v", digest, size, err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		info = mustMigrationInfo(t, path)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if digest, size, err := hashBackupFile(canceled, path, info, 7); digest != "" || size != 0 ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("hashBackupFile(canceled) = %q, %d, %v", digest, size, err)
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

func TestPrepareLegacyInputFailureAndEmptyBoundaries(t *testing.T) {
	noRoots := &backupSession{}
	roots, cleanup, err := noRoots.prepareLegacyInputs(t.Context(), storecatalog.Spec{ID: "global/auth"})
	if err != nil || roots != nil || cleanup == nil || cleanup() != nil {
		t.Fatalf("no-root legacy inputs = %q, %v", roots, err)
	}
	if roots, cleanup, err := (*backupSession)(nil).prepareLegacyInputs(
		t.Context(), storecatalog.Spec{ID: "global/auth", LegacyRoots: []string{"legacy"}},
	); roots != nil || cleanup == nil || err == nil {
		t.Fatalf("nil-session legacy inputs = %q, %v", roots, err)
	}

	t.Run("unmatched root", func(t *testing.T) {
		session, _ := verifiedBackupFixture(t, []byte("payload"))
		roots, cleanup, err := session.prepareLegacyInputs(t.Context(), storecatalog.Spec{
			ID: "global/auth", LegacyRoots: []string{filepath.Join(t.TempDir(), "missing.json")},
		})
		if err != nil || len(roots) != 1 {
			t.Fatalf("unmatched legacy input = %q, %v", roots, err)
		}
		if _, err := os.Lstat(roots[0]); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unmatched root exists: %v", err)
		}
		if err := cleanup(); err != nil {
			t.Fatal(err)
		}
		if err := cleanup(); err != nil {
			t.Fatalf("second cleanup = %v", err)
		}
	})

	t.Run("unsafe root name", func(t *testing.T) {
		session, _ := verifiedBackupFixture(t, []byte("payload"))
		roots, cleanup, err := session.prepareLegacyInputs(t.Context(), storecatalog.Spec{
			ID: "global/auth", LegacyRoots: []string{string(os.PathSeparator)},
		})
		if roots != nil || cleanup == nil || err == nil {
			t.Fatalf("unsafe legacy root = %q, %v", roots, err)
		}
	})

	t.Run("conflicting root types", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "backup")
		if err := ensurePrivateBackupDirectory(root); err != nil {
			t.Fatal(err)
		}
		liveRoot := filepath.Join(t.TempDir(), "legacy")
		firstRelative := filepath.Join("files", "root")
		secondRelative := filepath.Join("files", "nested")
		firstPath := filepath.Join(root, firstRelative)
		secondPath := filepath.Join(root, secondRelative)
		writeMigrationFile(t, firstPath, []byte("root"))
		writeMigrationFile(t, secondPath, []byte("nested"))
		session := &backupSession{root: root, manifest: BackupManifest{
			Version: backupManifestVersion, CreatedAt: time.Unix(1, 0).UTC(),
			Stores:             []BackupStoreManifest{{StoreID: "global/auth", LegacyRoots: 1}},
			CatalogGenerations: []string{filepath.Join(t.TempDir(), "store.db")},
			Files: []BackupFileManifest{
				backupRecord(t, firstPath, "global/auth", liveRoot, firstRelative),
				backupRecord(t, secondPath, "global/auth", filepath.Join(liveRoot, "child"), secondRelative),
			},
		}}
		if err := session.finish("snapshot_complete", nil); err != nil {
			t.Fatal(err)
		}
		roots, cleanup, err := session.prepareLegacyInputs(t.Context(), storecatalog.Spec{
			ID: "global/auth", LegacyRoots: []string{liveRoot},
		})
		if roots != nil || cleanup == nil || err == nil {
			t.Fatalf("conflicting legacy roots = %q, %v", roots, err)
		}
	})
}

func verifiedBackupFixture(t *testing.T, payload []byte) (*backupSession, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "backup")
	if err := ensurePrivateBackupDirectory(root); err != nil {
		t.Fatal(err)
	}
	relative := filepath.Join("files", "payload")
	path := filepath.Join(root, relative)
	writeMigrationFile(t, path, payload)
	session := &backupSession{root: root, manifest: BackupManifest{
		Version: backupManifestVersion, CreatedAt: time.Unix(1, 0).UTC(),
		Stores:             []BackupStoreManifest{{StoreID: "global/auth", LegacyRoots: 1}},
		CatalogGenerations: []string{filepath.Join(t.TempDir(), "store.db")},
		Files: []BackupFileManifest{
			backupRecord(t, path, "global/auth", filepath.Join(t.TempDir(), "source"), relative),
		},
	}}
	if err := session.finish("snapshot_complete", nil); err != nil {
		t.Fatal(err)
	}
	if err := session.verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	return session, path
}

func backupRecord(t *testing.T, path, storeID, source, relative string) BackupFileManifest {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	info := mustMigrationInfo(t, path)
	return BackupFileManifest{
		StoreID: storeID, Role: "legacy", Source: source, SourceIdentity: "fixture-source",
		Backup: filepath.ToSlash(relative),
		SHA256: formatDigest(digest),
		Size:   int64(len(payload)), Mode: uint32(info.Mode().Perm()), SourceMode: 0o600,
	}
}

func formatDigest(digest [sha256.Size]byte) string {
	const digits = "0123456789abcdef"
	result := make([]byte, len(digest)*2)
	for index, value := range digest {
		result[index*2] = digits[value>>4]
		result[index*2+1] = digits[value&0xf]
	}
	return string(result)
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
