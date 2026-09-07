//nolint:govet // Independent migration boundary assertions intentionally use narrow errors.
package migration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestSnapshotSortsRealStoreAndLegacyInventory(t *testing.T) {
	home := t.TempDir()
	legacyRoot := filepath.Join(home, "legacy")
	if err := os.MkdirAll(legacyRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	alphaLegacy := filepath.Join(legacyRoot, "alpha.json")
	zetaLegacy := filepath.Join(legacyRoot, "zeta.json")
	for path, payload := range map[string]string{
		alphaLegacy: "alpha legacy",
		zetaLegacy:  "zeta legacy",
	} {
		if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	alpha := storecatalog.Spec{
		ID: "global/alpha", Path: filepath.Join(home, "alpha.db"),
		LegacyRoots: []string{zetaLegacy, alphaLegacy},
	}
	zeta := storecatalog.Spec{ID: "global/zeta", Path: filepath.Join(home, "zeta.db")}
	if err := os.WriteFile(alpha.Path, []byte("alpha database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zeta.Path, []byte("zeta database"), 0o600); err != nil {
		t.Fatal(err)
	}
	physical := &storecatalog.Catalog{Home: home, Specs: []storecatalog.Spec{zeta, alpha}}
	engine := &Engine{
		home: home, config: &config.Config{},
		now: func() time.Time { return time.Unix(1_800_000_000, 0).UTC() },
	}
	session, err := engine.snapshot(
		t.Context(), physical, []storecatalog.Spec{zeta, alpha}, filepath.Join(home, "backups"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.manifest.Files) != 4 {
		t.Fatalf("snapshot inventory = %#v", session.manifest.Files)
	}
	keys := make([]string, len(session.manifest.Files))
	for index, file := range session.manifest.Files {
		keys[index] = file.StoreID + "\x00" + file.Role + "\x00" + file.Source
	}
	if !sort.StringsAreSorted(keys) {
		t.Fatalf("snapshot inventory is not canonical: %#v", keys)
	}
}

func TestSnapshotReportsRealPathValidationFailures(t *testing.T) {
	t.Run("backup parent", func(t *testing.T) {
		home := t.TempDir()
		engine := &Engine{home: home, config: &config.Config{}, now: time.Now}
		physical := &storecatalog.Catalog{Home: home}
		if session, err := engine.snapshot(t.Context(), physical, nil, " padded "); err == nil || session != nil {
			t.Fatalf("invalid backup parent = %#v, %v", session, err)
		}
	})

	for _, test := range []struct {
		name            string
		includePhysical bool
	}{
		{name: "known generation", includePhysical: true},
		{name: "selected generation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, strings.Repeat("x", 1024), "store.db")
			if _, err := os.Lstat(path); err == nil || errors.Is(err, os.ErrNotExist) {
				t.Skip("filesystem does not expose an overlong-path inspection error")
			}
			spec := storecatalog.Spec{ID: "global/test", Path: path}
			physical := &storecatalog.Catalog{Home: home}
			selected := []storecatalog.Spec{spec}
			if test.includePhysical {
				physical.Specs = []storecatalog.Spec{spec}
				selected = nil
			}
			engine := &Engine{home: home, config: &config.Config{}, now: time.Now}
			session, err := engine.snapshot(
				t.Context(), physical, selected, filepath.Join(home, "backups"),
			)
			if err == nil || session == nil {
				t.Fatalf("generation inspection = %#v, %v", session, err)
			}
			if manifest := readManifest(t, session.root); manifest.Outcome != "failed" {
				t.Fatalf("failed snapshot manifest = %#v", manifest)
			}
		})
	}
}

func TestWalkLegacyInputsSkipsRealBackupAndExcludedMembers(t *testing.T) {
	root := t.TempDir()
	tree := filepath.Join(root, "legacy")
	backupRoot := filepath.Join(tree, "active-backup")
	included := filepath.Join(tree, "included.json")
	excluded := filepath.Join(tree, "excluded.json")
	ignoredBackup := filepath.Join(backupRoot, "ignored.json")
	for _, path := range []string{included, excluded, ignoredBackup} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var visited []string
	err := walkLegacyInputs(
		t.Context(), tree, backupRoot,
		map[string]struct{}{filepath.Clean(excluded): {}},
		func(path string) error {
			visited = append(visited, path)
			return nil
		},
	)
	if err != nil || len(visited) != 1 || visited[0] != included {
		t.Fatalf("filtered legacy inventory = %#v, %v", visited, err)
	}

	visited = nil
	if err := walkLegacyInputs(t.Context(), tree, included, nil, func(path string) error {
		visited = append(visited, path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range visited {
		if path == included {
			t.Fatalf("backup file was visited: %#v", visited)
		}
	}

	overlong := filepath.Join(root, strings.Repeat("x", 1024))
	if _, probeErr := os.Lstat(overlong); probeErr != nil && !errors.Is(probeErr, os.ErrNotExist) {
		if err := walkLegacyInputs(t.Context(), overlong, backupRoot, nil, func(string) error {
			return nil
		}); err == nil {
			t.Fatal("legacy inspection accepted an overlong path")
		}
	}
}

func TestCopyBackupFileRejectsRealBlockedDestination(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	blockedRoot := filepath.Join(root, "blocked")
	if err := os.WriteFile(blockedRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := copyBackupFile(
		t.Context(), blockedRoot, "global/test", "database", source, filepath.Join("nested", "copy"),
	); err == nil {
		t.Fatal("backup copy accepted a regular file as its destination root")
	}
}

func TestMigrationRunRealFenceAndSelectionFailures(t *testing.T) {
	t.Run("invalid engine home", func(t *testing.T) {
		root := t.TempDir()
		homeFile := filepath.Join(root, "home-file")
		if err := os.WriteFile(homeFile, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		engine := &Engine{home: homeFile, config: &config.Config{}, now: time.Now}
		if _, err := engine.Run(t.Context(), Options{DryRun: true}); err == nil || errors.Is(err, ErrStorageActive) {
			t.Fatalf("invalid engine home error = %v", err)
		}
	})

	t.Run("physical claim contention", func(t *testing.T) {
		home, _, cfg := migrationFixture(t)
		claims, err := database.AcquireCatalogStoreClaims(home, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := claims.Close(); err != nil {
				t.Error(err)
			}
		}()
		engine, err := New(home, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := engine.Run(t.Context(), Options{DryRun: true}); !errors.Is(err, ErrStorageActive) {
			t.Fatalf("physical claim contention = %v", err)
		}
	})

	t.Run("unknown selected store", func(t *testing.T) {
		home, _, cfg := migrationFixture(t)
		engine, err := New(home, cfg)
		if err != nil {
			t.Fatal(err)
		}
		unknown, err := database.ParseStoreID("global/unknown")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := engine.Run(
			t.Context(), Options{Stores: []database.StoreID{unknown}, DryRun: true},
		); !errors.Is(err, ErrUnknownStore) {
			t.Fatalf("unknown selected store = %v", err)
		}
	})
}

func TestMigrationHelpersUseRealAdaptersAndFilesystemErrors(t *testing.T) {
	t.Run("cron adapter", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, "workspace", "cron", "jobs.db")
		withMigrationFence(t, home, func() {
			if err := applyDomainMigrationAdapter(t.Context(), storecatalog.Spec{
				ID: "workspace/cron", Domain: "cron", Path: path,
			}); err != nil {
				t.Fatal(err)
			}
		})
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("cron adapter generation = %#v, %v", info, err)
		}
	})

	t.Run("legacy inspection error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), strings.Repeat("x", 1024))
		if _, probeErr := os.Lstat(path); probeErr == nil || errors.Is(probeErr, os.ErrNotExist) {
			t.Skip("filesystem does not expose an overlong-path inspection error")
		}
		if _, err := migrationLegacyInputExists([]string{path}); err == nil {
			t.Fatal("legacy migration inspection ignored a filesystem error")
		}
	})

	t.Run("invalid projected store ID", func(t *testing.T) {
		physical := &storecatalog.Catalog{Specs: []storecatalog.Spec{{ID: "invalid id"}}}
		if _, _, err := selectStores(physical, nil); err == nil {
			t.Fatal("invalid projected store ID was accepted")
		}
	})
}

func TestMigrationReportsMalformedExistingDomainSchema(t *testing.T) {
	home, _, cfg := migrationFixture(t)
	path := filepath.Join(home, "auth.db")
	db, err := sqliteprovider.OpenStore(path, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqliteprovider.ConfigureOffline(t.Context(), db, 5*time.Second); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE auth_credentials (unexpected TEXT)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := sqliteprovider.SetSchemaVersion(t.Context(), db, 0); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	engine, err := New(home, cfg)
	if err != nil {
		t.Fatal(err)
	}
	id, err := database.ParseStoreID("global/auth")
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Run(t.Context(), Options{
		Stores: []database.StoreID{id}, BackupDir: filepath.Join(home, "malformed-backup"),
	})
	if !errors.Is(err, ErrMigrationRequired) || len(result.Stores) != 1 ||
		!result.Stores[0].AdapterRequired || result.BackupDir == "" {
		t.Fatalf("malformed domain migration = %#v, %v", result, err)
	}
	if manifest := readManifest(t, result.BackupDir); manifest.Outcome != "failed" {
		t.Fatalf("malformed domain backup = %#v", manifest)
	}
}

type afterSnapshotContext struct {
	context.Context
	backupParent string
	action       func(string)
	once         sync.Once
}

func (ctx *afterSnapshotContext) Err() error {
	if err := ctx.Context.Err(); err != nil {
		return err
	}
	manifests, _ := filepath.Glob(filepath.Join(ctx.backupParent, "database-migrate-*", backupManifestName))
	for _, manifest := range manifests {
		if _, err := os.Stat(manifest); err != nil {
			continue
		}
		ctx.once.Do(func() {
			if ctx.action != nil {
				ctx.action(filepath.Dir(manifest))
			}
		})
		break
	}
	return ctx.Context.Err()
}

func TestMigrationRunPropagatesRealPostSnapshotChanges(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		home, _, cfg := migrationFixture(t)
		engine, err := New(home, cfg)
		if err != nil {
			t.Fatal(err)
		}
		id, err := database.ParseStoreID("global/auth")
		if err != nil {
			t.Fatal(err)
		}
		backupParent := filepath.Join(home, "cancel-backup")
		base, cancel := context.WithCancel(context.Background())
		ctx := &afterSnapshotContext{
			Context: base, backupParent: backupParent,
			action: func(string) { cancel() },
		}
		result, err := engine.Run(ctx, Options{
			Stores: []database.StoreID{id}, BackupDir: backupParent,
		})
		if !errors.Is(err, context.Canceled) || result.BackupDir == "" {
			t.Fatalf("post-snapshot cancellation = %#v, %v", result, err)
		}
		if manifest := readManifest(t, result.BackupDir); manifest.Outcome != "failed" {
			t.Fatalf("canceled migration backup = %#v", manifest)
		}
	})

	t.Run("backup removal", func(t *testing.T) {
		home, _, cfg := migrationFixture(t)
		engine, err := New(home, cfg)
		if err != nil {
			t.Fatal(err)
		}
		id, err := database.ParseStoreID("global/auth")
		if err != nil {
			t.Fatal(err)
		}
		backupParent := filepath.Join(home, "removed-backup")
		ctx := &afterSnapshotContext{
			Context: context.Background(), backupParent: backupParent,
			action: func(root string) {
				if err := os.RemoveAll(root); err != nil {
					t.Errorf("remove completed snapshot: %v", err)
					return
				}
				if err := os.WriteFile(root, nil, 0o600); err != nil {
					t.Errorf("replace completed snapshot: %v", err)
				}
			},
		}
		result, err := engine.Run(ctx, Options{
			Stores: []database.StoreID{id}, BackupDir: backupParent,
		})
		if err == nil || result.BackupDir == "" ||
			!strings.Contains(err.Error(), "finalize database migration backup") {
			t.Fatalf("removed migration backup = %#v, %v", result, err)
		}
	})

	t.Run("legacy replacement", func(t *testing.T) {
		home, _, cfg := migrationFixture(t)
		legacy := filepath.Join(home, "auth.json")
		target := filepath.Join(home, "replacement.json")
		if err := os.WriteFile(legacy, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		probe := filepath.Join(home, "symlink-probe")
		if err := os.Symlink(target, probe); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := os.Remove(probe); err != nil {
			t.Fatal(err)
		}
		engine, err := New(home, cfg)
		if err != nil {
			t.Fatal(err)
		}
		id, err := database.ParseStoreID("global/auth")
		if err != nil {
			t.Fatal(err)
		}
		backupParent := filepath.Join(home, "legacy-replacement-backup")
		ctx := &afterSnapshotContext{
			Context: context.Background(), backupParent: backupParent,
			action: func(string) {
				if err := os.Remove(legacy); err != nil {
					t.Errorf("remove snapshotted legacy input: %v", err)
					return
				}
				if err := os.Symlink(target, legacy); err != nil {
					t.Errorf("replace snapshotted legacy input: %v", err)
				}
			},
		}
		result, err := engine.Run(ctx, Options{
			Stores: []database.StoreID{id}, BackupDir: backupParent,
		})
		if err == nil || result.BackupDir == "" || !strings.Contains(err.Error(), "legacy input") {
			t.Fatalf("replaced legacy input = %#v, %v", result, err)
		}
		if manifest := readManifest(t, result.BackupDir); manifest.Outcome != "failed" {
			t.Fatalf("replaced legacy backup = %#v", manifest)
		}
	})
}

func TestMigrateUnversionedStorePropagatesRealCanceledRevalidation(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "cron", "jobs.db")
	withMigrationFence(t, home, func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := migrateUnversionedStore(ctx, storecatalog.Spec{
			ID: "workspace/cron", Domain: "cron", Path: path,
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled cron revalidation = %v", err)
		}
	})
}
