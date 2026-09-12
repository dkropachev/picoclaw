//nolint:govet // Independent failure-boundary assertions use narrow error scopes.
package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sealedAbsentOpenOptions(
	root string,
	sources func() ([]LegacySource, error),
	importer LegacyImporter,
) Options {
	options := testOptions()
	options.Legacy = &LegacyOptions{
		SourceRoot:       root,
		SourceRootPolicy: LegacySourceRootSealedAbsent,
		Closeout:         LegacyCloseoutDeferred,
		Sources:          sources,
		Import:           importer,
	}
	return options
}

func TestOpenSealedAbsentLegacyRootClosesOneHorizonWithoutSourceMutation(t *testing.T) {
	ancestor := t.TempDir()
	if err := os.Chmod(ancestor, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(ancestor, "root-000000", "repository_reviews")
	before, err := os.Lstat(ancestor)
	if err != nil {
		t.Fatal(err)
	}

	enumerations := 0
	imports := 0
	seals := 0
	options := sealedAbsentOpenOptions(root,
		func() ([]LegacySource, error) {
			enumerations++
			if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
				return nil, errors.New("sealed absent source root materialized")
			}
			return []LegacySource{}, nil
		},
		func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
			imports++
			return ImportResult{}, errors.New("sealed absent importer was invoked")
		},
	)
	options.Legacy.Seal = func(ctx context.Context, conn *sql.Conn) error {
		seals++
		var imports int
		return conn.QueryRowContext(
			ctx,
			`SELECT COUNT(*) FROM storage_imports WHERE component = 'test-store'`,
		).Scan(&imports)
	}

	databaseHome := t.TempDir()
	databasePath := filepath.Join(databaseHome, "store.db")
	ctx := deferredMigrationContext(t, databaseHome, databasePath)
	for attempt := 1; attempt <= 2; attempt++ {
		database, err := Open(ctx, databasePath, options)
		if err != nil {
			t.Fatalf("Open() attempt %d error = %v", attempt, err)
		}
		var horizons, recorded int
		if err := database.QueryRow(`SELECT COUNT(*) FROM storage_import_horizons
			WHERE component = 'test-store'`).Scan(&horizons); err != nil || horizons != 1 {
			_ = database.Close()
			t.Fatalf("attempt %d horizons = %d, %v", attempt, horizons, err)
		}
		if err := database.QueryRow(`SELECT COUNT(*) FROM storage_imports
			WHERE component = 'test-store'`).Scan(&recorded); err != nil || recorded != 0 {
			_ = database.Close()
			t.Fatalf("attempt %d imports = %d, %v", attempt, recorded, err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if enumerations != 2 || imports != 0 || seals != 2 {
		t.Fatalf(
			"sealed absent callbacks = enumeration:%d import:%d seal:%d",
			enumerations,
			imports,
			seals,
		)
	}
	if _, err := os.Lstat(filepath.Join(ancestor, "root-000000")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sealed absent namespace was touched: %v", err)
	}
	after, err := os.Lstat(ancestor)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() ||
		!before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("sealed absent ancestor changed = %#v/%#v, %v", before, after, err)
	}
}

func TestOpenSealedAbsentLegacyRootRejectsPolicyMisuseBeforeDatabaseOpen(t *testing.T) {
	validSources := func() ([]LegacySource, error) { return nil, nil }
	validImporter := func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
		return ImportResult{}, nil
	}
	tests := []struct {
		name      string
		configure func(*LegacyOptions, string)
		authorize bool
		want      string
	}{
		{
			name: "default missing directory",
			configure: func(options *LegacyOptions, root string) {
				options.SourceRoot = root
				options.Closeout = LegacyCloseoutDeferred
				options.Sources = validSources
				options.Import = validImporter
			},
			authorize: true,
			want:      "deferred legacy import",
		},
		{
			name: "unknown source policy",
			configure: func(options *LegacyOptions, root string) {
				options.SourceRoot = root
				options.SourceRootPolicy = LegacySourceRootPolicy(255)
				options.Closeout = LegacyCloseoutDeferred
				options.Sources = validSources
				options.Import = validImporter
			},
			authorize: true,
			want:      "source root policy is invalid",
		},
		{
			name: "sealed absent archive",
			configure: func(options *LegacyOptions, root string) {
				options.SourceRoot = root
				options.SourceRootPolicy = LegacySourceRootSealedAbsent
				options.ArchiveRoot = filepath.Join(root, "archive")
				options.Closeout = LegacyCloseoutArchive
				options.Sources = validSources
				options.Import = validImporter
			},
			want: "requires deferred closeout",
		},
		{
			name: "sealed absent with archive destination",
			configure: func(options *LegacyOptions, root string) {
				options.SourceRoot = root
				options.SourceRootPolicy = LegacySourceRootSealedAbsent
				options.ArchiveRoot = filepath.Join(filepath.Dir(root), "archive")
				options.Closeout = LegacyCloseoutDeferred
				options.Sources = validSources
				options.Import = validImporter
			},
			want: "does not accept an archive root",
		},
		{
			name: "sealed absent without authority",
			configure: func(options *LegacyOptions, root string) {
				*options = *sealedAbsentOpenOptions(root, validSources, validImporter).Legacy
			},
			want: "exact-target offline migration authority",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ancestor := t.TempDir()
			if err := os.Chmod(ancestor, 0o700); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(ancestor, "missing", "legacy")
			databaseHome := t.TempDir()
			databasePath := filepath.Join(databaseHome, "must-not-open.db")
			options := testOptions()
			options.Legacy = &LegacyOptions{}
			test.configure(options.Legacy, root)
			ctx := t.Context()
			if test.authorize {
				ctx = deferredMigrationContext(t, databaseHome, databasePath)
			}
			database, err := Open(ctx, databasePath, options)
			if database != nil {
				_ = database.Close()
			}
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("Open() error = %v, want %q", err, test.want)
			}
			if _, statErr := os.Lstat(databasePath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("invalid sealed absent policy opened database: %v", statErr)
			}
		})
	}
}

func TestOpenSealedAbsentLegacyRootRejectsExistingObjectsBeforeDatabaseOpen(t *testing.T) {
	for _, kind := range []string{"directory", "file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			ancestor := t.TempDir()
			if err := os.Chmod(ancestor, 0o700); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(ancestor, "legacy")
			switch kind {
			case "directory":
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(root, []byte("legacy"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(ancestor, "missing-target"), root); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			databaseHome := t.TempDir()
			databasePath := filepath.Join(databaseHome, "must-not-open.db")
			options := sealedAbsentOpenOptions(root,
				func() ([]LegacySource, error) { return nil, nil },
				func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
					return ImportResult{}, nil
				},
			)
			database, err := Open(
				deferredMigrationContext(t, databaseHome, databasePath),
				databasePath,
				options,
			)
			if database != nil {
				_ = database.Close()
			}
			if err == nil {
				t.Fatal("Open() accepted an existing sealed absent root")
			}
			if _, statErr := os.Lstat(databasePath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("existing sealed absent root opened database: %v", statErr)
			}
		})
	}
}

func TestOpenSealedAbsentLegacyRootRejectsSourcesAndPersistentDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string, *Options, *int)
		want   string
	}{
		{
			name: "enumerated source",
			mutate: func(_ string, options *Options, _ *int) {
				options.Legacy.Sources = func() ([]LegacySource, error) {
					return []LegacySource{{ID: "late", Relative: "late.json"}}, nil
				}
			},
			want: "returned sources",
		},
		{
			name: "root appears during enumeration",
			mutate: func(root string, options *Options, _ *int) {
				options.Legacy.Sources = func() ([]LegacySource, error) {
					return nil, os.MkdirAll(root, 0o700)
				}
			},
			want: "appeared",
		},
		{
			name: "root appears during seal",
			mutate: func(root string, options *Options, _ *int) {
				options.Legacy.Seal = func(context.Context, *sql.Conn) error {
					return os.MkdirAll(root, 0o700)
				}
			},
			want: "appeared",
		},
		{
			name: "root appears during validation",
			mutate: func(root string, options *Options, _ *int) {
				options.Validate = func(context.Context, *sql.Conn) error {
					return os.MkdirAll(root, 0o700)
				}
			},
			want: "appeared",
		},
		{
			name: "canceled by enumeration",
			mutate: func(_ string, options *Options, cancelCalls *int) {
				original := options.Legacy.Sources
				options.Legacy.Sources = func() ([]LegacySource, error) {
					(*cancelCalls)++
					return original()
				}
			},
			want: "context canceled",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ancestor := t.TempDir()
			if err := os.Chmod(ancestor, 0o700); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(ancestor, "missing", "legacy")
			imports := 0
			options := sealedAbsentOpenOptions(root,
				func() ([]LegacySource, error) { return nil, nil },
				func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
					imports++
					return ImportResult{}, nil
				},
			)
			cancelCalls := 0
			test.mutate(root, &options, &cancelCalls)
			databaseHome := t.TempDir()
			databasePath := filepath.Join(databaseHome, "rolled-back.db")
			ctx := deferredMigrationContext(t, databaseHome, databasePath)
			var cancel context.CancelFunc
			if test.name == "canceled by enumeration" {
				ctx, cancel = context.WithCancel(ctx)
				original := options.Legacy.Sources
				options.Legacy.Sources = func() ([]LegacySource, error) {
					cancel()
					return original()
				}
			}
			database, err := Open(ctx, databasePath, options)
			if database != nil {
				_ = database.Close()
			}
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("Open() error = %v, want %q", err, test.want)
			}
			if imports != 0 {
				t.Fatalf("sealed absent importer calls = %d", imports)
			}
			assertAbsentOpenTransactionRolledBack(t, databasePath)
		})
	}
}

func TestImmediateBeforeCommitGuardIsLastTransactionalCallback(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	order := make([]string, 0, 2)
	err = immediateWithBeforeCommit(t.Context(), database, func(conn *sql.Conn) error {
		order = append(order, "transaction")
		_, err := conn.ExecContext(t.Context(), `CREATE TABLE guarded (id INTEGER)`)
		return err
	}, func() error {
		order = append(order, "guard")
		return nil
	})
	if err != nil || strings.Join(order, ",") != "transaction,guard" {
		t.Fatalf("guarded transaction = %v, %v", order, err)
	}

	canary := errors.New("before commit canary")
	err = immediateWithBeforeCommit(t.Context(), database, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(t.Context(), `CREATE TABLE rolled_back (id INTEGER)`)
		return err
	}, func() error { return canary })
	if !errors.Is(err, canary) {
		t.Fatalf("before-commit failure = %v", err)
	}
	var rolledBack int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
		WHERE type = 'table' AND name = 'rolled_back'`).Scan(&rolledBack); err != nil || rolledBack != 0 {
		t.Fatalf("guard failure committed table = %d, %v", rolledBack, err)
	}

	canceled, cancel := context.WithCancel(t.Context())
	guardCalls := 0
	err = immediateWithBeforeCommit(canceled, database, func(*sql.Conn) error {
		cancel()
		return nil
	}, func() error {
		guardCalls++
		return nil
	})
	if !errors.Is(err, context.Canceled) || guardCalls != 0 {
		t.Fatalf("canceled guarded transaction = calls:%d error:%v", guardCalls, err)
	}
}

func assertAbsentOpenTransactionRolledBack(t *testing.T, path string) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var committed int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
		WHERE name IN ('records', 'storage_imports', 'storage_import_issues',
			'storage_import_horizons', 'storage_imports_archive_status_idx')`).Scan(
		&committed,
	); err != nil || committed != 0 {
		t.Fatalf("failed sealed absent open committed schema = %d, %v", committed, err)
	}
}
