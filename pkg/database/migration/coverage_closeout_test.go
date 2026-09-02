//nolint:govet // Independent failure-boundary assertions intentionally reuse narrow error names.
package migration

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

	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestCoverageMigrationAdapterDispatchAndMetadata(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "workspace", "state", "store.db")
	domains := []string{
		"auth", "launcher-auth", "model-catalogs", "tool-adaptation", "workflows",
		"sessions", "eventing", "cron", "runtime-state", "account-routing",
		"repository-reviews", "repository-evaluations", "evolution", "local-ci",
		"seahorse", "channel-wecom", "channel-weixin", "channel-matrix",
		"channel-whatsapp", "git-workspace-inventory", "pr-workspace-checkpoints",
	}
	for _, domain := range domains {
		t.Run(domain, func(t *testing.T) {
			spec := storecatalog.Spec{
				ID: domain + ".store", Domain: domain, Path: path,
				LegacyRoots: []string{filepath.Join(root, "launcher-config.json")},
			}
			_ = applyDomainMigrationAdapter(t.Context(), spec)
			if !domainHasMigrationAdapter(domain) {
				t.Fatalf("adapter metadata omitted %q", domain)
			}
			if expectedSchemaVersion(domain) != map[string]int{
				"eventing": 20, "git-workspace-inventory": 2,
				"pr-workspace-checkpoints": 3,
			}[domain] && expectedSchemaVersion(domain) != 1 {
				t.Fatalf("unexpected schema version for %q", domain)
			}
		})
	}
	if err := applyDomainMigrationAdapter(t.Context(), storecatalog.Spec{Domain: "unknown"}); err == nil {
		t.Fatal("unknown domain acquired an adapter")
	}
	if domainHasMigrationAdapter("unknown") || domainUsesUnversionedAdapter("unknown") ||
		expectedSchemaVersion("unknown") != 0 {
		t.Fatal("unknown domain acquired migration metadata")
	}
	for _, domain := range []string{"channel-matrix", "channel-whatsapp", "seahorse"} {
		if !domainUsesUnversionedAdapter(domain) {
			t.Fatalf("%q is not classified as unversioned", domain)
		}
	}
}

func TestCoverageMigrationSelectionAndEngineBoundaries(t *testing.T) {
	if _, err := New("bad\x00home", nil); err == nil {
		t.Fatal("New accepted an invalid home")
	}
	var unavailable *Engine
	if _, err := unavailable.Run(t.Context(), Options{}); err == nil {
		t.Fatal("nil engine ran")
	}
	if _, err := (&Engine{}).Run(t.Context(), Options{}); err == nil {
		t.Fatal("empty engine ran")
	}

	home, _, cfg := migrationFixture(t)
	engine, err := New(home, nil)
	if err != nil || engine.config == nil {
		t.Fatalf("New nil config = %#v, %v", engine, err)
	}
	engine, err = New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := engine.Run(canceled, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Run = %v", err)
	}

	physical, err := storecatalog.Build(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	all, ids, err := selectStores(physical, nil)
	if err != nil || len(all) == 0 || len(all) != len(ids) {
		t.Fatalf("select all = %d/%d, %v", len(all), len(ids), err)
	}
	if _, _, err := selectStores(physical, []database.StoreID{"invalid id"}); !errors.Is(err, ErrUnknownStore) {
		t.Fatalf("invalid selection = %v", err)
	}
	authID := mustStoreID(t, "global.auth")
	if _, _, err := selectStores(
		physical,
		[]database.StoreID{authID, authID},
	); err == nil ||
		!strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate selection = %v", err)
	}
	if _, _, err := selectStores(physical, []database.StoreID{"global.unknown"}); !errors.Is(err, ErrUnknownStore) {
		t.Fatalf("unknown selection = %v", err)
	}
	selected, selectedIDs, err := selectStores(physical, []database.StoreID{
		mustStoreID(t, "workspace.workflows"), authID,
	})
	if err != nil || len(selected) != 2 || selected[0].ID != "global.auth" ||
		selectedIDs[0] != authID {
		t.Fatalf("sorted selection = %#v / %#v / %v", selected, selectedIDs, err)
	}

	result, err := engine.Run(nil, Options{
		Stores: []database.StoreID{authID}, BackupDir: filepath.Join(home, "missing-store-backup"),
	})
	if err != nil || len(result.Stores) != 1 || result.Stores[0].Exists || result.Stores[0].Migrated {
		t.Fatalf("missing ordinary store result = %#v, %v", result, err)
	}
	if readManifest(t, result.BackupDir).Outcome != "complete" {
		t.Fatal("missing store backup was not completed")
	}
}

func TestCoverageMigrationLegacyAndBackupPathHelpers(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	if exists, err := migrationLegacyInputExists([]string{missing}); err != nil || exists {
		t.Fatalf("missing legacy exists=%v err=%v", exists, err)
	}
	file := filepath.Join(root, "legacy.json")
	if err := os.WriteFile(file, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if exists, err := migrationLegacyInputExists([]string{missing, file}); err != nil || !exists {
		t.Fatalf("legacy exists=%v err=%v", exists, err)
	}
	alias := filepath.Join(root, "legacy-link")
	if err := os.Symlink(file, alias); err == nil {
		if _, err := migrationLegacyInputExists([]string{alias}); err == nil {
			t.Fatal("symlinked legacy input accepted")
		}
	}

	if got, err := validateBackupParent("", root); err != nil || got != filepath.Join(root, "backups") {
		t.Fatalf("default backup parent = %q, %v", got, err)
	}
	for _, invalid := range []string{" padded ", "bad\x00dir"} {
		if _, err := validateBackupParent(invalid, root); err == nil {
			t.Fatalf("backup parent %q accepted", invalid)
		}
	}
	absolute, err := validateBackupParent(filepath.Join(root, "custom"), root)
	if err != nil || !filepath.IsAbs(absolute) {
		t.Fatalf("absolute backup parent = %q, %v", absolute, err)
	}
}

func TestCoverageBackupSessionAndDirectoryHelpers(t *testing.T) {
	if err := (*backupSession)(nil).finish("complete", nil); err != nil {
		t.Fatal(err)
	}
	if err := (&backupSession{}).finish("complete", nil); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "backup")
	if err := ensurePrivateBackupDirectory(root); err != nil {
		t.Fatal(err)
	}
	session := &backupSession{root: root, manifest: BackupManifest{Version: 1}}
	canary := errors.New("migration failed")
	if err := session.finish("failed", canary); err != nil {
		t.Fatal(err)
	}
	manifest := readManifest(t, root)
	if manifest.Outcome != "failed" || manifest.Error != canary.Error() {
		t.Fatalf("failed manifest = %#v", manifest)
	}
	if err := session.finish("complete", nil); err != nil {
		t.Fatal(err)
	}
	if readManifest(t, root).Error != "" {
		t.Fatal("completed manifest retained stale error")
	}
	badRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(badRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	badSession := &backupSession{root: badRoot}
	if err := badSession.finish("failed", canary); err == nil {
		t.Fatal("manifest write to missing root succeeded")
	}

	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateBackupDirectory(blocked); err == nil {
		t.Fatal("file backup directory accepted")
	}
	if err := validateBackupAncestors(filepath.Join(blocked, "child")); err == nil {
		t.Fatal("unsafe backup ancestor accepted")
	}
	if runtime.GOOS != "windows" {
		realDirectory := filepath.Join(t.TempDir(), "real")
		if err := os.Mkdir(realDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(realDirectory, link); err == nil {
			if err := ensurePrivateBackupDirectory(filepath.Join(link, "child")); err == nil {
				t.Fatal("symlinked backup ancestor accepted")
			}
		}
	}
}

func TestCoverageWalkLegacyInputs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	backupRoot := filepath.Join(root, "backups", "current")
	if err := os.MkdirAll(backupRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(root, "input.json")
	if err := os.WriteFile(regular, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	var visited []string
	visit := func(path string) error {
		visited = append(visited, path)
		return nil
	}
	if err := walkLegacyInputs(ctx, filepath.Join(root, "missing"), backupRoot, nil, visit); err != nil {
		t.Fatal(err)
	}
	if err := walkLegacyInputs(ctx, regular, backupRoot, nil, visit); err != nil || len(visited) != 1 {
		t.Fatalf("regular legacy visit = %#v, %v", visited, err)
	}
	if err := walkLegacyInputs(ctx, regular, backupRoot,
		map[string]struct{}{filepath.Clean(regular): {}}, visit); err != nil || len(visited) != 1 {
		t.Fatalf("excluded legacy visit = %#v, %v", visited, err)
	}
	if err := walkLegacyInputs(ctx, regular, backupRoot, nil,
		func(string) error { return errors.New("visit canary") }); err == nil {
		t.Fatal("legacy visitor error ignored")
	}

	tree := filepath.Join(root, "tree")
	for _, path := range []string{
		filepath.Join(tree, "kept.json"),
		filepath.Join(tree, "nested", "kept.json"),
		filepath.Join(tree, "legacy-json", "ignored.json"),
		filepath.Join(tree, "backups", "ignored.json"),
		filepath.Join(backupRoot, "ignored.json"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	visited = nil
	if err := walkLegacyInputs(ctx, tree, backupRoot, nil, visit); err != nil || len(visited) != 2 {
		t.Fatalf("tree visits = %#v, %v", visited, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := walkLegacyInputs(canceled, tree, backupRoot, nil, visit); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled walk = %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(regular, link); err == nil {
		if err := walkLegacyInputs(ctx, link, backupRoot, nil, visit); err == nil {
			t.Fatal("symlinked legacy root accepted")
		}
		inside := filepath.Join(tree, "nested", "link")
		if err := os.Symlink(regular, inside); err == nil {
			if err := walkLegacyInputs(ctx, tree, backupRoot, nil, visit); err == nil {
				t.Fatal("symlinked legacy tree member accepted")
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

func TestCoverageBackupCopyBoundaries(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	payload := []byte("backup payload")
	if err := os.WriteFile(source, payload, 0o640); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "backup")
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := copyBackupFile(
		t.Context(), backup, "global.test", "legacy", source, filepath.Join("items", "source"),
	)
	if err != nil || record.Size != int64(len(payload)) || record.SHA256 == "" ||
		record.Backup != "items/source" {
		t.Fatalf("backup record = %#v, %v", record, err)
	}
	if copied, err := os.ReadFile(filepath.Join(backup, "items", "source")); err != nil ||
		!bytes.Equal(copied, payload) {
		t.Fatalf("copied payload = %q, %v", copied, err)
	}
	if _, err := copyBackupFile(t.Context(), backup, "id", "role", filepath.Join(root, "missing"), "x"); err == nil {
		t.Fatal("missing backup source accepted")
	}
	if _, err := copyBackupFile(t.Context(), backup, "id", "role", root, "directory"); err == nil {
		t.Fatal("directory backup source accepted")
	}
	if _, err := copyBackupFile(t.Context(), backup, "id", "role", source, "items/source"); err == nil {
		t.Fatal("duplicate backup destination accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := copyBackupFile(canceled, backup, "id", "role", source, "canceled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled backup = %v", err)
	}

	canary := errors.New("copy canary")
	standardHash := sha256.New()
	if count, err := copyWithContext(context.Background(), io.Discard, standardHash,
		strings.NewReader("payload")); err != nil || count != 7 {
		t.Fatalf("copy success = %d, %v", count, err)
	}
	if _, err := copyWithContext(
		canceled,
		io.Discard,
		sha256.New(),
		strings.NewReader("x"),
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled copy = %v", err)
	}
	if _, err := copyWithContext(context.Background(), coverageErrorWriter{canary}, sha256.New(),
		strings.NewReader("x")); !errors.Is(err, canary) {
		t.Fatalf("writer error = %v", err)
	}
	if _, err := copyWithContext(context.Background(), coverageShortWriter{}, sha256.New(),
		strings.NewReader("x")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short writer error = %v", err)
	}
	if _, err := copyWithContext(context.Background(), io.Discard,
		coverageHash{Hash: sha256.New(), err: canary}, strings.NewReader("x")); !errors.Is(err, canary) {
		t.Fatalf("digest error = %v", err)
	}
	if _, err := copyWithContext(
		context.Background(),
		io.Discard,
		sha256.New(),
		iotest.ErrReader(canary),
	); !errors.Is(
		err,
		canary,
	) {
		t.Fatalf("reader error = %v", err)
	}
}

func TestCoverageSnapshotRejectsAliasesAndUnsafeMembers(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string) (storecatalog.Spec, *storecatalog.Catalog)
	}{
		{
			name: "sidecar without database",
			setup: func(t *testing.T, home string) (storecatalog.Spec, *storecatalog.Catalog) {
				path := filepath.Join(home, "store.db")
				if err := os.WriteFile(path+"-wal", []byte("wal"), 0o600); err != nil {
					t.Fatal(err)
				}
				spec := storecatalog.Spec{ID: "global.test", Path: path}
				return spec, &storecatalog.Catalog{Home: home, Specs: []storecatalog.Spec{spec}}
			},
		},
		{
			name: "unsafe database member",
			setup: func(t *testing.T, home string) (storecatalog.Spec, *storecatalog.Catalog) {
				path := filepath.Join(home, "store.db")
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				spec := storecatalog.Spec{ID: "global.test", Path: path}
				return spec, &storecatalog.Catalog{Home: home, Specs: []storecatalog.Spec{spec}}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			spec, physical := test.setup(t, home)
			engine := &Engine{home: home, config: &config.Config{}, now: time.Now}
			session, err := engine.snapshot(t.Context(), physical, []storecatalog.Spec{spec},
				filepath.Join(home, "backups"))
			if err == nil || session == nil || readManifest(t, session.root).Outcome != "failed" {
				t.Fatalf("unsafe snapshot = %#v, %v", session, err)
			}
		})
	}

	t.Run("legacy aliases generation", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, "store.db")
		if err := os.WriteFile(path, []byte("generation"), 0o600); err != nil {
			t.Fatal(err)
		}
		legacy := filepath.Join(home, "legacy.json")
		if err := os.Link(path, legacy); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		spec := storecatalog.Spec{ID: "global.test", Path: path, LegacyRoots: []string{legacy}}
		physical := &storecatalog.Catalog{Home: home, Specs: []storecatalog.Spec{spec}}
		engine := &Engine{home: home, config: &config.Config{}, now: time.Now}
		if _, err := engine.snapshot(t.Context(), physical, []storecatalog.Spec{spec},
			filepath.Join(home, "backups")); err == nil {
			t.Fatal("legacy generation alias accepted")
		}
	})

	t.Run("canceled", func(t *testing.T) {
		home := t.TempDir()
		spec := storecatalog.Spec{ID: "global.test", Path: filepath.Join(home, "store.db")}
		physical := &storecatalog.Catalog{Home: home, Specs: []storecatalog.Spec{spec}}
		engine := &Engine{home: home, config: &config.Config{}, now: time.Now}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		session, err := engine.snapshot(ctx, physical, []storecatalog.Spec{spec},
			filepath.Join(home, "backups"))
		if !errors.Is(err, context.Canceled) || session == nil {
			t.Fatalf("canceled snapshot = %#v, %v", session, err)
		}
	})
}

func TestCoverageSnapshotDeduplicatesAndRejectsPhysicalLegacyAliases(t *testing.T) {
	t.Run("duplicate lexical source", func(t *testing.T) {
		home := t.TempDir()
		legacy := filepath.Join(home, "legacy.json")
		if err := os.WriteFile(legacy, []byte("legacy"), 0o600); err != nil {
			t.Fatal(err)
		}
		spec := storecatalog.Spec{
			ID: "global.test", Path: filepath.Join(home, "store.db"),
			LegacyRoots: []string{legacy, legacy},
		}
		physical := &storecatalog.Catalog{Home: home, Specs: []storecatalog.Spec{spec}}
		engine := &Engine{
			home: home, config: &config.Config{},
			now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		}
		session, err := engine.snapshot(t.Context(), physical, []storecatalog.Spec{spec},
			filepath.Join(home, "backups"))
		if err != nil || len(session.manifest.Files) != 1 {
			t.Fatalf("deduplicated snapshot = %#v, %v", session, err)
		}
		if _, err := engine.snapshot(t.Context(), physical, []storecatalog.Spec{spec},
			filepath.Join(home, "backups")); err == nil {
			t.Fatal("duplicate timestamp backup root accepted")
		}
	})

	t.Run("physical legacy aliases", func(t *testing.T) {
		home := t.TempDir()
		first := filepath.Join(home, "first.json")
		second := filepath.Join(home, "second.json")
		if err := os.WriteFile(first, []byte("legacy"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(first, second); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		spec := storecatalog.Spec{
			ID: "global.test", Path: filepath.Join(home, "store.db"),
			LegacyRoots: []string{first, second},
		}
		physical := &storecatalog.Catalog{Home: home, Specs: []storecatalog.Spec{spec}}
		engine := &Engine{home: home, config: &config.Config{}, now: time.Now}
		if _, err := engine.snapshot(t.Context(), physical, []storecatalog.Spec{spec},
			filepath.Join(home, "backups")); err == nil || !strings.Contains(err.Error(), "physical alias") {
			t.Fatalf("physical legacy alias = %v", err)
		}
	})
}
