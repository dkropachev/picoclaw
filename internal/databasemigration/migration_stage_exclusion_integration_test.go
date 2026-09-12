package databasemigration

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/databaseadapter"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestRepositoryReviewMigrationExcludesPinnedStageFromNestedLegacyRoot(t *testing.T) {
	for _, existingTarget := range []bool{false, true} {
		name := "missing target"
		if existingTarget {
			name = "existing target"
		}
		t.Run(name, func(t *testing.T) {
			home := migrationHome(t)
			legacyRoot := filepath.Join(home, "workspace", "repository_reviews")
			legacyPath := filepath.Join(legacyRoot, "profile_legacy.json")
			targetPath := filepath.Join(legacyRoot, "repository-reviews.db")
			writeMigrationFile(t, legacyPath, []byte("sealed repository review"))
			stamp := time.Date(2026, 9, 10, 12, 34, 56, 0, time.UTC)
			if err := os.Chtimes(legacyPath, stamp, stamp); err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(legacyPath)
			if err != nil {
				t.Fatal(err)
			}
			if existingTarget {
				createEmptyRepositoryReviewTarget(t, targetPath)
			}

			contract := repositoryReviewStageExclusionContract()
			registry := migrationRegistry(t, databaseadapter.Adapter{
				Domain:   "repository-reviews",
				Contract: contract,
				Migrate: func(ctx context.Context, target databaseadapter.Target) error {
					if target.ID != "workspace/repository-reviews" ||
						len(target.LegacyRoots) != 1 || target.LegacyRoots[0] == legacyRoot {
						return database.NewError(
							database.CodeIntegrity,
							"repository-review adapter received unsealed inputs",
						)
					}
					store, openErr := sqlitestore.Open(ctx, target.GenerationPath, sqlitestore.Options{
						Component: "repository-reviews",
						Migrations: []sqlitestore.Migration{{
							Version: 1,
							Statements: []string{`CREATE TABLE items (
								id TEXT PRIMARY KEY,
								value TEXT NOT NULL
							) STRICT`},
						}},
						Legacy: &sqlitestore.LegacyOptions{
							SourceRoot: target.LegacyRoots[0],
							Closeout:   sqlitestore.LegacyCloseoutDeferred,
							Sources: func() ([]sqlitestore.LegacySource, error) {
								return []sqlitestore.LegacySource{{
									ID: "profile", Relative: "profile_legacy.json",
								}}, nil
							},
							Import: func(
								ctx context.Context,
								conn *sql.Conn,
								input sqlitestore.LegacyInput,
							) (sqlitestore.ImportResult, error) {
								_, importErr := conn.ExecContext(
									ctx,
									`INSERT INTO items(id, value) VALUES (?, ?)`,
									input.ID,
									string(input.Data),
								)
								return sqlitestore.ImportResult{Imported: 1}, importErr
							},
						},
					})
					if openErr != nil {
						return openErr
					}
					return store.Close()
				},
			})

			result, runErr := migrationEngine(t, home, registry).Run(t.Context(), Options{
				Stores: []database.StoreID{"workspace/repository-reviews"},
			})
			if runErr != nil || len(result.Stores) != 1 || !result.Stores[0].Migrated {
				t.Fatalf("Run() = %#v, %v", result, runErr)
			}

			after, err := os.Lstat(legacyPath)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := os.ReadFile(legacyPath)
			if err != nil || string(payload) != "sealed repository review" ||
				!os.SameFile(before, after) || before.Mode() != after.Mode() ||
				before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
				t.Fatalf("live repository-review input changed = %q, %#v/%#v, %v", payload, before, after, err)
			}
			assertRepositoryReviewImport(t, targetPath)
			for _, pattern := range []string{
				".*.migration-stage-*.db*",
				".sqlite-retirement-*",
			} {
				stages, err := filepath.Glob(filepath.Join(legacyRoot, pattern))
				if err != nil || len(stages) != 0 {
					t.Fatalf("repository-review stages matching %q remain = %v, %v", pattern, stages, err)
				}
			}
		})
	}
}

func TestRepositoryReviewMigrationCreatesProvenParentForSealedAbsentRoot(t *testing.T) {
	home := migrationHome(t)
	liveRoot := filepath.Join(home, "workspace", "repository_reviews")
	targetPath := filepath.Join(liveRoot, "repository-reviews.db")
	if _, err := os.Lstat(liveRoot); !os.IsNotExist(err) {
		t.Fatalf("fresh repository-review root is present: %v", err)
	}

	var disposableRoot string
	imports := 0
	registry := migrationRegistry(t, databaseadapter.Adapter{
		Domain:   "repository-reviews",
		Contract: repositoryReviewStageExclusionContract(),
		Migrate: func(ctx context.Context, target databaseadapter.Target) error {
			if target.ID != "workspace/repository-reviews" ||
				len(target.LegacyRoots) != 1 || target.LegacyRoots[0] == liveRoot {
				return database.NewError(
					database.CodeIntegrity,
					"repository-review adapter received unsealed absent inputs",
				)
			}
			disposableRoot = target.LegacyRoots[0]
			if _, err := os.Lstat(disposableRoot); !os.IsNotExist(err) {
				return errors.New("sealed absent disposable root was materialized")
			}
			store, openErr := sqlitestore.Open(ctx, target.GenerationPath, sqlitestore.Options{
				Component: "repository-reviews",
				Migrations: []sqlitestore.Migration{{
					Version: 1,
					Statements: []string{`CREATE TABLE items (
						id TEXT PRIMARY KEY,
						value TEXT NOT NULL
					) STRICT`},
				}},
				Legacy: &sqlitestore.LegacyOptions{
					SourceRoot:       disposableRoot,
					SourceRootPolicy: sqlitestore.LegacySourceRootSealedAbsent,
					Closeout:         sqlitestore.LegacyCloseoutDeferred,
					Sources: func() ([]sqlitestore.LegacySource, error) {
						return nil, nil
					},
					Import: func(
						context.Context,
						*sql.Conn,
						sqlitestore.LegacyInput,
					) (sqlitestore.ImportResult, error) {
						imports++
						return sqlitestore.ImportResult{}, errors.New(
							"sealed absent repository-review importer was invoked",
						)
					},
				},
			})
			if openErr != nil {
				return openErr
			}
			return store.Close()
		},
	})

	result, runErr := migrationEngine(t, home, registry).Run(t.Context(), Options{
		Stores: []database.StoreID{"workspace/repository-reviews"},
	})
	if runErr != nil || len(result.Stores) != 1 || !result.Stores[0].Migrated || imports != 0 {
		t.Fatalf("fresh repository-review Run() = %#v, imports:%d error:%v", result, imports, runErr)
	}
	if disposableRoot == "" {
		t.Fatal("fresh repository-review adapter did not receive a disposable root")
	}
	if _, err := os.Lstat(disposableRoot); !os.IsNotExist(err) {
		t.Fatalf("sealed absent disposable root survived migration: %v", err)
	}
	entries, err := os.ReadDir(liveRoot)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(targetPath) {
		t.Fatalf("fresh repository-review live inventory = %v, %v", entries, err)
	}
	for _, suffix := range []string{
		"-wal", "-shm", "-journal",
	} {
		if _, statErr := os.Lstat(targetPath + suffix); !os.IsNotExist(statErr) {
			t.Fatalf("fresh repository-review sidecar %s remains: %v", suffix, statErr)
		}
	}
	stages, err := filepath.Glob(filepath.Join(liveRoot, ".*.migration-stage-*.db*"))
	if err != nil || len(stages) != 0 {
		t.Fatalf("fresh repository-review stages remain = %v, %v", stages, err)
	}
	store, err := sqliteprovider.OpenStore(targetPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var horizons, rows int
	if err := store.QueryRow(`SELECT COUNT(*) FROM storage_import_horizons
		WHERE component = 'repository-reviews'`).Scan(&horizons); err != nil || horizons != 1 {
		t.Fatalf("fresh repository-review horizon = %d, %v", horizons, err)
	}
	if err := store.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("fresh repository-review rows = %d, %v", rows, err)
	}
}

func repositoryReviewStageExclusionContract() databaseadapter.Contract {
	return databaseadapter.Contract{
		CurrentVersion: 1,
		EmptyPolicy:    databaseadapter.EmptyMigrateOffline,
		RequiredObjects: []databaseadapter.SchemaObject{
			{Type: "table", Name: "items"},
			{Type: "table", Name: "storage_imports"},
			{Type: "table", Name: "storage_import_issues"},
			{Type: "table", Name: "storage_import_horizons"},
			{Type: "index", Name: "storage_imports_archive_status_idx"},
		},
		RequiredColumns: []databaseadapter.ColumnSet{
			{Table: "items", Columns: []string{"id", "value"}},
			{Table: "storage_imports", Columns: []string{
				"component", "source_id", "archive_status",
			}},
			{Table: "storage_import_horizons", Columns: []string{
				"component", "completed_at",
			}},
		},
		ImportHorizon: "repository-reviews",
	}
}

func createEmptyRepositoryReviewTarget(t *testing.T, path string) {
	t.Helper()
	store, err := sqliteprovider.OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	store.SetMaxOpenConns(1)
	store.SetMaxIdleConns(1)
	if err := sqliteprovider.ConfigureOffline(t.Context(), store, time.Second); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertRepositoryReviewImport(t *testing.T, path string) {
	t.Helper()
	store, err := sqliteprovider.OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var value, archiveStatus string
	var archivedAt any
	if err := store.QueryRow(`SELECT item.value, imported.archive_status, imported.archived_at
		FROM items AS item
		JOIN storage_imports AS imported ON imported.source_id = item.id
		WHERE imported.component = 'repository-reviews' AND item.id = 'profile'`).Scan(
		&value,
		&archiveStatus,
		&archivedAt,
	); err != nil || value != "sealed repository review" || archiveStatus != "pending" || archivedAt != nil {
		t.Fatalf("installed repository-review import = %q/%q/%#v, %v", value, archiveStatus, archivedAt, err)
	}
	var horizons int
	if err := store.QueryRow(`SELECT COUNT(*) FROM storage_import_horizons
		WHERE component = 'repository-reviews'`).Scan(&horizons); err != nil || horizons != 1 {
		t.Fatalf("installed repository-review import horizon = %d, %v", horizons, err)
	}
}
