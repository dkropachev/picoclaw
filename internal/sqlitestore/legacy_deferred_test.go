//nolint:govet // Narrow test assertions intentionally use independent error scopes.
package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	dblayer "github.com/sipeed/picoclaw/pkg/database"
)

type deferredSourceState struct {
	info os.FileInfo
	data []byte
}

func snapshotDeferredSource(t *testing.T, path string) deferredSourceState {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return deferredSourceState{info: info, data: data}
}

func assertDeferredSourceState(t *testing.T, path string, want deferredSourceState) {
	t.Helper()
	got := snapshotDeferredSource(t, path)
	if !os.SameFile(want.info, got.info) || want.info.Mode() != got.info.Mode() ||
		want.info.Size() != got.info.Size() || !want.info.ModTime().Equal(got.info.ModTime()) ||
		string(want.data) != string(got.data) {
		t.Fatalf(
			"deferred source changed: identity=%t mode=%s/%s size=%d/%d mtime=%s/%s bytes=%q/%q",
			os.SameFile(want.info, got.info),
			want.info.Mode(),
			got.info.Mode(),
			want.info.Size(),
			got.info.Size(),
			want.info.ModTime(),
			got.info.ModTime(),
			want.data,
			got.data,
		)
	}
}

func deferredLegacyOptions(
	root string,
	sources func() ([]LegacySource, error),
	importer LegacyImporter,
) *LegacyOptions {
	return &LegacyOptions{
		SourceRoot: root,
		Closeout:   LegacyCloseoutDeferred,
		Sources:    sources,
		Import:     importer,
	}
}

func deferredMigrationContext(t *testing.T, home, path string) context.Context {
	t.Helper()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	fence, err := dblayer.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := fence.Close(); closeErr != nil {
			t.Errorf("close migration fence: %v", closeErr)
		}
	})
	ctx, err := fence.MigrationContext(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestOpenDeferredLegacyImportKeepsSourcePendingAndUnchanged(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(root, "legacy.json")
	if err := os.WriteFile(sourcePath, []byte("sealed payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 9, 1, 2, 3, 4, 0, time.UTC)
	if err := os.Chtimes(sourcePath, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	wantSource := snapshotDeferredSource(t, sourcePath)

	archiveCalls := 0
	swapTestHook(t, &archiveOpenedSQLiteLegacyFiles,
		func(context.Context, *sql.DB, string, LegacyOptions) error {
			archiveCalls++
			return errors.New("deferred import invoked archive hook")
		})
	imports := 0
	options := testOptions()
	options.Legacy = deferredLegacyOptions(root,
		func() ([]LegacySource, error) {
			return []LegacySource{{ID: "legacy", Relative: "legacy.json"}}, nil
		},
		func(ctx context.Context, conn *sql.Conn, _ LegacyInput) (ImportResult, error) {
			imports++
			_, err := conn.ExecContext(ctx,
				`INSERT INTO records(id, value) VALUES ('legacy', 'sealed')`)
			return ImportResult{Imported: 1}, err
		})
	databaseHome := t.TempDir()
	databasePath := filepath.Join(databaseHome, "store.db")
	migrationCtx := deferredMigrationContext(t, databaseHome, databasePath)
	for attempt := 1; attempt <= 2; attempt++ {
		database, err := Open(migrationCtx, databasePath, options)
		if err != nil {
			t.Fatalf("Open() attempt %d error = %v", attempt, err)
		}
		var status string
		var archivedAt sql.NullInt64
		if err := database.QueryRow(`SELECT archive_status, archived_at FROM storage_imports
            WHERE component = 'test-store' AND source_id = 'legacy'`).Scan(
			&status, &archivedAt,
		); err != nil || status != "pending" || archivedAt.Valid {
			t.Fatalf("deferred archive state = %q/%#v, %v", status, archivedAt, err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		assertDeferredSourceState(t, sourcePath, wantSource)
	}
	if imports != 1 || archiveCalls != 0 {
		t.Fatalf("deferred calls = import:%d archive:%d", imports, archiveCalls)
	}
}

func TestOpenFreezesDeferredCloseoutBeforeImporterCallback(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(root, "legacy.json")
	if err := os.WriteFile(sourcePath, []byte("sealed"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantSource := snapshotDeferredSource(t, sourcePath)
	archiveCalls := 0
	swapTestHook(t, &archiveOpenedSQLiteLegacyFiles,
		func(context.Context, *sql.DB, string, LegacyOptions) error {
			archiveCalls++
			return errors.New("frozen deferred import invoked archive hook")
		})
	legacy := deferredLegacyOptions(root,
		func() ([]LegacySource, error) {
			return []LegacySource{{ID: "legacy", Relative: "legacy.json"}}, nil
		},
		nil,
	)
	legacy.Import = func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
		legacy.Closeout = LegacyCloseoutArchive
		legacy.ArchiveRoot = filepath.Join(root, "archive")
		legacy.SourceRoot = filepath.Join(root, "changed")
		return ImportResult{Imported: 1}, nil
	}
	options := testOptions()
	options.Legacy = legacy
	databaseHome := t.TempDir()
	databasePath := filepath.Join(databaseHome, "store.db")
	database, err := Open(
		deferredMigrationContext(t, databaseHome, databasePath),
		databasePath,
		options,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if archiveCalls != 0 {
		t.Fatalf("archive calls after caller mutation = %d", archiveCalls)
	}
	assertDeferredSourceState(t, sourcePath, wantSource)
}

func TestOpenCanonicalizesDeferredSourceRootBeforeCallbacks(t *testing.T) {
	originalWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if restoreErr := os.Chdir(originalWorkingDirectory); restoreErr != nil {
			t.Errorf("restore working directory: %v", restoreErr)
		}
	}()
	firstParent := t.TempDir()
	secondParent := t.TempDir()
	for _, fixture := range []struct {
		parent string
		value  string
	}{
		{parent: firstParent, value: "first"},
		{parent: secondParent, value: "second"},
	} {
		root := filepath.Join(fixture.parent, "legacy")
		if mkdirErr := os.Mkdir(root, 0o700); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		if writeErr := os.WriteFile(
			filepath.Join(root, "source.json"),
			[]byte(fixture.value),
			0o600,
		); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if chdirErr := os.Chdir(firstParent); chdirErr != nil {
		t.Fatal(chdirErr)
	}
	options := testOptions()
	options.Legacy = deferredLegacyOptions("legacy",
		func() ([]LegacySource, error) {
			if chdirErr := os.Chdir(secondParent); chdirErr != nil {
				return nil, chdirErr
			}
			return []LegacySource{{ID: "source", Relative: "source.json"}}, nil
		},
		func(ctx context.Context, conn *sql.Conn, input LegacyInput) (ImportResult, error) {
			_, insertErr := conn.ExecContext(
				ctx,
				`INSERT INTO records(id, value) VALUES ('source', ?)`,
				string(input.Data),
			)
			return ImportResult{Imported: 1}, insertErr
		})
	databaseHome := t.TempDir()
	databasePath := filepath.Join(databaseHome, "store.db")
	database, err := Open(
		deferredMigrationContext(t, databaseHome, databasePath),
		databasePath,
		options,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var value string
	if err := database.QueryRow(`SELECT value FROM records WHERE id = 'source'`).Scan(&value); err != nil ||
		value != "first" {
		t.Fatalf("canonical deferred source value = %q, %v", value, err)
	}
}

func TestOpenRejectsInvalidLegacyCloseoutBeforeDatabaseMutation(t *testing.T) {
	safeRoot := t.TempDir()
	if err := os.Chmod(safeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	fileRoot := filepath.Join(t.TempDir(), "source-file")
	if err := os.WriteFile(fileRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	validSources := func() ([]LegacySource, error) { return nil, nil }
	validImporter := func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
		return ImportResult{}, nil
	}
	tests := []struct {
		name           string
		legacy         *LegacyOptions
		want           string
		authorizedRoot bool
		wantUnopened   bool
	}{
		{"unknown policy", &LegacyOptions{
			SourceRoot: safeRoot, Closeout: LegacyCloseoutPolicy(255),
			Sources: validSources, Import: validImporter,
		}, "closeout policy is invalid", false, true},
		{"archive without destination", &LegacyOptions{
			SourceRoot: safeRoot, Closeout: LegacyCloseoutArchive,
			Sources: validSources, Import: validImporter,
		}, "requires an archive root", false, true},
		{"deferred with destination", &LegacyOptions{
			SourceRoot: safeRoot, ArchiveRoot: filepath.Join(safeRoot, "archive"),
			Closeout: LegacyCloseoutDeferred, Sources: validSources, Import: validImporter,
		}, "does not accept an archive root", false, true},
		{"deferred without source root", &LegacyOptions{
			Closeout: LegacyCloseoutDeferred, Sources: validSources, Import: validImporter,
		}, "requires a source root", false, true},
		{"deferred with non-directory source root", &LegacyOptions{
			SourceRoot: fileRoot, Closeout: LegacyCloseoutDeferred,
			Sources: validSources, Import: validImporter,
		}, "real directory", true, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "must-not-open.db")
			options := testOptions()
			options.Legacy = test.legacy
			ctx := t.Context()
			if test.authorizedRoot {
				ctx = deferredMigrationContext(t, home, path)
			}
			database, err := Open(ctx, path, options)
			if database != nil {
				_ = database.Close()
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Open() error = %v, want %q", err, test.want)
			}
			if test.wantUnopened {
				if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("invalid policy opened database: %v", statErr)
				}
			}
		})
	}
	if err := archiveImportedSources(t.Context(), nil, "test-store", LegacyOptions{
		SourceRoot: safeRoot, Closeout: LegacyCloseoutDeferred,
	}); err == nil || !strings.Contains(err.Error(), "not archive") {
		t.Fatalf("direct deferred archive error = %v", err)
	}
}

func TestOpenDeferredLegacyImportRequiresExactTargetOfflineAuthority(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	calls := 0
	options := testOptions()
	options.Legacy = deferredLegacyOptions(root,
		func() ([]LegacySource, error) {
			calls++
			return nil, nil
		},
		func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
			calls++
			return ImportResult{}, nil
		})
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "store.db")
	if database, err := Open(t.Context(), path, options); err == nil ||
		!strings.Contains(err.Error(), "exact-target offline migration authority") {
		if database != nil {
			_ = database.Close()
		}
		t.Fatalf("ordinary deferred Open() error = %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ordinary deferred open touched database: %v", err)
	}
	fence, err := dblayer.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Close()
	wrongPath := filepath.Join(home, "wrong.db")
	wrongCtx, err := fence.MigrationContext(t.Context(), wrongPath)
	if err != nil {
		t.Fatal(err)
	}
	if database, err := Open(wrongCtx, path, options); err == nil ||
		!strings.Contains(err.Error(), "authority does not match path") {
		if database != nil {
			_ = database.Close()
		}
		t.Fatalf("wrong-target deferred Open() error = %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrong-target deferred open touched database: %v", err)
	}
	if database, err := Open(t.Context(), ":memory:", options); err == nil ||
		!strings.Contains(err.Error(), "exact-target offline migration authority") {
		if database != nil {
			_ = database.Close()
		}
		t.Fatalf("memory deferred Open() error = %v", err)
	}
	overlapPath := filepath.Join(root, "stage.db")
	overlapCtx := deferredMigrationContext(t, root, overlapPath)
	if database, err := Open(overlapCtx, overlapPath, options); err == nil ||
		!strings.Contains(err.Error(), "overlaps the database target") {
		if database != nil {
			_ = database.Close()
		}
		t.Fatalf("overlapping deferred Open() error = %v", err)
	}
	if _, err := os.Lstat(overlapPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("overlapping deferred open touched database: %v", err)
	}
	if calls != 0 {
		t.Fatalf("unauthorized deferred open inspected legacy inputs %d times", calls)
	}
}

func TestDeferredLegacyImportClosesHorizonAndAuditsLateSource(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(root, "late.json")
	visible := false
	imports := 0
	options := testOptions()
	options.Legacy = deferredLegacyOptions(root,
		func() ([]LegacySource, error) {
			if !visible {
				return nil, nil
			}
			return []LegacySource{{ID: "late", Relative: "late.json"}}, nil
		},
		func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
			imports++
			return ImportResult{Imported: 1}, nil
		})
	databaseHome := t.TempDir()
	databasePath := filepath.Join(databaseHome, "store.db")
	migrationCtx := deferredMigrationContext(t, databaseHome, databasePath)
	database, err := Open(migrationCtx, databasePath, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte("late sealed bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantSource := snapshotDeferredSource(t, sourcePath)
	visible = true
	for attempt := 1; attempt <= 2; attempt++ {
		database, err = Open(migrationCtx, databasePath, options)
		if err != nil {
			t.Fatalf("late Open() attempt %d error = %v", attempt, err)
		}
		var imported, skipped int
		var status, issue string
		if err := database.QueryRow(`SELECT imported_count, skipped_count, archive_status
            FROM storage_imports WHERE component = 'test-store' AND source_id = 'late'`).Scan(
			&imported, &skipped, &status,
		); err != nil || imported != 0 || skipped != 1 || status != "pending" {
			t.Fatalf("late import = %d/%d/%q, %v", imported, skipped, status, err)
		}
		if err := database.QueryRow(`SELECT issue_code FROM storage_import_issues
            WHERE component = 'test-store' AND source_id = 'late'`).Scan(&issue); err != nil || issue != "late-source" {
			t.Fatalf("late issue = %q, %v", issue, err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
		assertDeferredSourceState(t, sourcePath, wantSource)
	}
	if imports != 0 {
		t.Fatalf("closed horizon invoked importer %d times", imports)
	}
	if err := os.WriteFile(sourcePath, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if database, err = Open(migrationCtx, databasePath, options); err == nil ||
		!strings.Contains(err.Error(), "changed after import") {
		if database != nil {
			_ = database.Close()
		}
		t.Fatalf("changed late source error = %v", err)
	}
}

func TestDeferredLegacyImportRejectsPhysicalSourceAliases(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(root, "first.json")
	second := filepath.Join(root, "second.json")
	if err := os.WriteFile(first, []byte("shared"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(first, second); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	options := testOptions()
	options.Legacy = deferredLegacyOptions(root,
		func() ([]LegacySource, error) {
			return []LegacySource{
				{ID: "first", Relative: "first.json"},
				{ID: "second", Relative: "second.json"},
			}, nil
		},
		func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
			return ImportResult{Imported: 1}, nil
		})
	databaseHome := t.TempDir()
	databasePath := filepath.Join(databaseHome, "store.db")
	database, err := Open(
		deferredMigrationContext(t, databaseHome, databasePath),
		databasePath,
		options,
	)
	if database != nil {
		_ = database.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "physical alias") {
		t.Fatalf("aliased deferred sources error = %v", err)
	}
	raw, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var records int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name = 'records'`).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if records != 0 {
		t.Fatal("aliased deferred sources committed destination schema")
	}
}

func TestDeferredLegacyImportFailureAndCancellationRollBackWithoutSourceWrites(t *testing.T) {
	tests := []struct {
		name     string
		importer func(context.Context, context.CancelFunc, *sql.Conn) error
		want     error
	}{
		{
			name: "import failure",
			importer: func(ctx context.Context, _ context.CancelFunc, conn *sql.Conn) error {
				_, err := conn.ExecContext(ctx,
					`INSERT INTO records(id, value) VALUES ('legacy', 'value')`)
				if err != nil {
					return err
				}
				return errors.New("parser failed")
			},
			want: errors.New("parser failed"),
		},
		{
			name: "cancellation",
			importer: func(ctx context.Context, cancel context.CancelFunc, conn *sql.Conn) error {
				if _, err := conn.ExecContext(ctx,
					`INSERT INTO records(id, value) VALUES ('legacy', 'value')`); err != nil {
					return err
				}
				cancel()
				return ctx.Err()
			},
			want: context.Canceled,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			sourcePath := filepath.Join(root, "legacy.json")
			if err := os.WriteFile(sourcePath, []byte("stable"), 0o600); err != nil {
				t.Fatal(err)
			}
			wantSource := snapshotDeferredSource(t, sourcePath)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			options := testOptions()
			options.Legacy = deferredLegacyOptions(root,
				func() ([]LegacySource, error) {
					return []LegacySource{{ID: "legacy", Relative: "legacy.json"}}, nil
				},
				func(ctx context.Context, conn *sql.Conn, _ LegacyInput) (ImportResult, error) {
					return ImportResult{}, test.importer(ctx, cancel, conn)
				})
			databaseHome := t.TempDir()
			if err := os.Chmod(databaseHome, 0o700); err != nil {
				t.Fatal(err)
			}
			databasePath := filepath.Join(databaseHome, "store.db")
			fence, fenceErr := dblayer.AcquireMigrationFence(databaseHome)
			if fenceErr != nil {
				t.Fatal(fenceErr)
			}
			defer fence.Close()
			ctx, contextErr := fence.MigrationContext(ctx, databasePath)
			if contextErr != nil {
				t.Fatal(contextErr)
			}
			database, err := Open(ctx, databasePath, options)
			if database != nil {
				_ = database.Close()
			}
			if !errors.Is(err, test.want) && (err == nil || !strings.Contains(err.Error(), test.want.Error())) {
				t.Fatalf("Open() error = %v, want %v", err, test.want)
			}
			assertDeferredSourceState(t, sourcePath, wantSource)
			raw, err := sql.Open("sqlite", databasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			var records int
			if queryErr := raw.QueryRow(
				`SELECT COUNT(*) FROM sqlite_schema WHERE name = 'records'`,
			).Scan(&records); queryErr != nil {
				t.Fatal(queryErr)
			}
			if records != 0 {
				t.Fatal("failed deferred import committed destination schema")
			}
		})
	}
}

func TestDeferredLegacyImportRollsBackPostCallbackSourceDrift(t *testing.T) {
	tests := []struct {
		name       string
		inValidate bool
		mutate     func(*testing.T, string, os.FileInfo)
	}{
		{
			name: "bytes",
			mutate: func(t *testing.T, path string, _ os.FileInfo) {
				t.Helper()
				if err := os.WriteFile(path, []byte("changed bytes"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "identity replacement",
			mutate: func(t *testing.T, path string, _ os.FileInfo) {
				t.Helper()
				replacement := filepath.Join(filepath.Dir(path), "replacement.json")
				if err := os.WriteFile(replacement, []byte("stable"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "mode",
			mutate: func(t *testing.T, path string, _ os.FileInfo) {
				t.Helper()
				if err := os.Chmod(path, 0o400); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "mtime",
			mutate: func(t *testing.T, path string, info os.FileInfo) {
				t.Helper()
				changed := info.ModTime().Add(-24 * time.Hour)
				if err := os.Chtimes(path, changed, changed); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "source root mode",
			mutate: func(t *testing.T, path string, _ os.FileInfo) {
				t.Helper()
				root := filepath.Dir(path)
				t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
				if err := os.Chmod(root, 0o500); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:       "validation callback",
			inValidate: true,
			mutate: func(t *testing.T, path string, _ os.FileInfo) {
				t.Helper()
				if err := os.WriteFile(path, []byte("changed by validation"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			sourcePath := filepath.Join(root, "legacy.json")
			if err := os.WriteFile(sourcePath, []byte("stable"), 0o600); err != nil {
				t.Fatal(err)
			}
			original, err := os.Lstat(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			options := testOptions()
			options.Legacy = deferredLegacyOptions(root,
				func() ([]LegacySource, error) {
					return []LegacySource{{ID: "legacy", Relative: "legacy.json"}}, nil
				},
				func(ctx context.Context, conn *sql.Conn, _ LegacyInput) (ImportResult, error) {
					if _, err := conn.ExecContext(ctx,
						`INSERT INTO records(id, value) VALUES ('legacy', 'value')`); err != nil {
						return ImportResult{}, err
					}
					if !test.inValidate {
						test.mutate(t, sourcePath, original)
					}
					return ImportResult{Imported: 1}, nil
				})
			if test.inValidate {
				validate := options.Validate
				options.Validate = func(ctx context.Context, conn *sql.Conn) error {
					if err := validate(ctx, conn); err != nil {
						return err
					}
					test.mutate(t, sourcePath, original)
					return nil
				}
			}
			databaseHome := t.TempDir()
			databasePath := filepath.Join(databaseHome, "store.db")
			migrationCtx := deferredMigrationContext(t, databaseHome, databasePath)
			database, err := Open(migrationCtx, databasePath, options)
			if database != nil {
				_ = database.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatalf("Open() drift error = %v", err)
			}
			if _, err := os.Lstat(sourcePath); err != nil {
				t.Fatalf("drift failure cleaned up source: %v", err)
			}
			raw, err := sql.Open("sqlite", databasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			var records int
			if queryErr := raw.QueryRow(
				`SELECT COUNT(*) FROM sqlite_schema WHERE name = 'records'`,
			).Scan(&records); queryErr != nil {
				t.Fatal(queryErr)
			}
			if records != 0 {
				t.Fatal("post-callback source drift committed destination schema")
			}
		})
	}
}

func TestDeferredLegacyHelperFaultsAndDirectoryProofs(t *testing.T) {
	canary := errors.New("deferred helper canary")

	t.Run("target absolute paths", func(t *testing.T) {
		for failureCall := 1; failureCall <= 2; failureCall++ {
			t.Run(fmt.Sprintf("call-%d", failureCall), func(t *testing.T) {
				original := legacyAbsolutePath
				calls := 0
				swapTestHook(t, &legacyAbsolutePath, func(path string) (string, error) {
					calls++
					if calls == failureCall {
						return "", canary
					}
					return original(path)
				})
				if err := validateDeferredLegacyTarget("target.db", "legacy"); !errors.Is(err, canary) {
					t.Fatalf("validateDeferredLegacyTarget() error = %v", err)
				}
			})
		}
		root := t.TempDir()
		if err := validateDeferredLegacyTarget(root, filepath.Join(root, "nested")); err == nil {
			t.Fatal("validateDeferredLegacyTarget() accepted a target containing its source root")
		}
	})

	t.Run("directory snapshot", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Run("lstat", func(t *testing.T) {
			swapTestHook(t, &legacyPathLstat, func(string) (os.FileInfo, error) {
				return nil, canary
			})
			if _, err := snapshotDeferredLegacyDirectory(root); !errors.Is(err, canary) {
				t.Fatalf("snapshotDeferredLegacyDirectory() error = %v", err)
			}
		})
		t.Run("unsafe type", func(t *testing.T) {
			path := filepath.Join(root, "file")
			if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := snapshotDeferredLegacyDirectory(path); err == nil ||
				!strings.Contains(err.Error(), "real directory") {
				t.Fatalf("snapshotDeferredLegacyDirectory() error = %v", err)
			}
		})
		t.Run("identity", func(t *testing.T) {
			swapTestHook(t, &legacyExistingIdentity, func(
				string,
			) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
				return fileidentity.Identity{}, 0, false, canary
			})
			if _, err := snapshotDeferredLegacyDirectory(root); !errors.Is(err, canary) {
				t.Fatalf("snapshotDeferredLegacyDirectory() error = %v", err)
			}
		})
		t.Run("final lstat", func(t *testing.T) {
			original := legacyPathLstat
			calls := 0
			swapTestHook(t, &legacyPathLstat, func(path string) (os.FileInfo, error) {
				calls++
				if calls == 2 {
					return nil, canary
				}
				return original(path)
			})
			if _, err := snapshotDeferredLegacyDirectory(root); !errors.Is(err, canary) {
				t.Fatalf("snapshotDeferredLegacyDirectory() error = %v", err)
			}
		})
	})

	t.Run("directory inventory", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		nested := filepath.Join(root, "nested")
		if err := os.Mkdir(nested, 0o700); err != nil {
			t.Fatal(err)
		}
		rootSnapshot, err := snapshotDeferredLegacyDirectory(root)
		if err != nil {
			t.Fatal(err)
		}
		newProof := func() *deferredLegacyProof {
			return &deferredLegacyProof{
				directories: map[string]deferredLegacyDirectory{"": rootSnapshot},
				directoryIdentities: map[fileidentity.Identity]string{
					rootSnapshot.identity: "",
				},
			}
		}
		if err := rememberDeferredLegacyDirectories(root, "nested/source.json", nil); err == nil {
			t.Fatal("rememberDeferredLegacyDirectories() accepted nil proof")
		}
		proof := newProof()
		if err := rememberDeferredLegacyDirectories(root, "nested/first.json", proof); err != nil {
			t.Fatal(err)
		}
		if err := rememberDeferredLegacyDirectories(root, "nested/second.json", proof); err != nil {
			t.Fatal(err)
		}
		if len(proof.directories) != 2 {
			t.Fatalf("remembered directories = %d, want 2", len(proof.directories))
		}

		ambiguous := newProof()
		ambiguous.directories[legacyRelativePathKey("nested")] = deferredLegacyDirectory{
			path: filepath.Join(root, "other"),
		}
		if err := rememberDeferredLegacyDirectories(
			root,
			"nested/source.json",
			ambiguous,
		); err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("ambiguous directory error = %v", err)
		}

		missing := newProof()
		if err := rememberDeferredLegacyDirectories(
			root,
			"missing/source.json",
			missing,
		); err == nil {
			t.Fatal("rememberDeferredLegacyDirectories() accepted a missing directory")
		}

		aliased := newProof()
		swapTestHook(t, &legacyExistingIdentity, func(
			string,
		) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
			return rootSnapshot.identity, fileidentity.ObjectTypeDirectory, true, nil
		})
		if err := rememberDeferredLegacyDirectories(
			root,
			"nested/source.json",
			aliased,
		); err == nil || !strings.Contains(err.Error(), "physical aliases") {
			t.Fatalf("aliased directory error = %v", err)
		}
	})

	t.Run("final proof guards", func(t *testing.T) {
		if err := revalidateDeferredLegacyProof(
			t.Context(),
			"test-store",
			LegacyOptions{Closeout: LegacyCloseoutArchive},
			nil,
		); err != nil {
			t.Fatal(err)
		}
		if err := revalidateDeferredLegacyProof(
			t.Context(),
			"test-store",
			LegacyOptions{Closeout: LegacyCloseoutDeferred},
			nil,
		); err == nil {
			t.Fatal("revalidateDeferredLegacyProof() accepted nil proof")
		}
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		rootSnapshot, err := snapshotDeferredLegacyDirectory(root)
		if err != nil {
			t.Fatal(err)
		}
		proof := &deferredLegacyProof{
			directories: map[string]deferredLegacyDirectory{"": rootSnapshot},
		}
		canceled, cancel := context.WithCancel(t.Context())
		cancel()
		if err := revalidateDeferredLegacyProof(
			canceled,
			"test-store",
			LegacyOptions{Closeout: LegacyCloseoutDeferred},
			proof,
		); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled revalidateDeferredLegacyProof() error = %v", err)
		}
		proof.sources = []deferredLegacySource{{source: LegacySource{ID: "source"}}}
		if err := revalidateDeferredLegacyProof(
			canceled,
			"test-store",
			LegacyOptions{Closeout: LegacyCloseoutDeferred},
			proof,
		); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled source proof error = %v", err)
		}

		proof.sources = nil
		for _, name := range []string{"a", "b", "cc"} {
			path := filepath.Join(root, name)
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			snapshot, err := snapshotDeferredLegacyDirectory(path)
			if err != nil {
				t.Fatal(err)
			}
			proof.directories[name] = snapshot
		}
		rootSnapshot, err = snapshotDeferredLegacyDirectory(root)
		if err != nil {
			t.Fatal(err)
		}
		proof.directories[""] = rootSnapshot
		if err := revalidateDeferredLegacyProof(
			t.Context(),
			"test-store",
			LegacyOptions{Closeout: LegacyCloseoutDeferred},
			proof,
		); err != nil {
			t.Fatalf("complete directory proof error = %v", err)
		}
	})

	t.Run("source identity", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "source.json")
		if err := os.WriteFile(path, []byte("source"), 0o600); err != nil {
			t.Fatal(err)
		}
		source := LegacySource{ID: "source", Relative: "source.json"}
		swapTestHook(t, &legacyOpenedIdentity, func(
			*os.File,
		) (fileidentity.Identity, fileidentity.ObjectType, error) {
			return fileidentity.Identity{}, 0, canary
		})
		if _, _, _, err := readLegacySourceSnapshot(root, source, 1024, true); !errors.Is(err, canary) {
			t.Fatalf("readLegacySourceSnapshot() error = %v", err)
		}
	})

	t.Run("source revalidation read", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "source.json")
		if err := os.WriteFile(path, []byte("source"), 0o600); err != nil {
			t.Fatal(err)
		}
		source := LegacySource{ID: "source", Relative: "source.json"}
		_, snapshot, found, err := readLegacySourceSnapshot(root, source, 1024, true)
		if err != nil || !found {
			t.Fatalf("initial source snapshot = %t, %v", found, err)
		}
		swapTestHook(t, &legacySourceOpen, func(string) (*os.File, error) {
			return nil, canary
		})
		if err := revalidateDeferredLegacySource(root, source, 1024, snapshot); !errors.Is(err, canary) {
			t.Fatalf("revalidateDeferredLegacySource() error = %v", err)
		}
	})

	if err := validateLegacySourceOutsideArchive(
		LegacyOptions{Closeout: LegacyCloseoutPolicy(255)},
		"source.json",
	); err == nil {
		t.Fatal("validateLegacySourceOutsideArchive() accepted an unknown policy")
	}
}

func TestDeferredLegacyImportSetupFaults(t *testing.T) {
	canary := errors.New("deferred setup canary")
	validSources := func() ([]LegacySource, error) { return nil, nil }
	validImporter := func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
		return ImportResult{}, nil
	}
	if _, err := importLegacySources(t.Context(), nil, "test-store", LegacyOptions{
		SourceRoot: filepath.Join(t.TempDir(), "missing"),
		Closeout:   LegacyCloseoutDeferred,
		Sources:    validSources,
		Import:     validImporter,
	}); err == nil {
		t.Fatal("importLegacySources() accepted a missing deferred root")
	}

	t.Run("root identity", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		swapTestHook(t, &legacyExistingIdentity, func(
			string,
		) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
			return fileidentity.Identity{}, 0, false, canary
		})
		options := testOptions()
		options.Legacy = deferredLegacyOptions(root, validSources, validImporter)
		databaseHome := t.TempDir()
		databasePath := filepath.Join(databaseHome, "store.db")
		database, err := Open(
			deferredMigrationContext(t, databaseHome, databasePath),
			databasePath,
			options,
		)
		if database != nil {
			_ = database.Close()
		}
		if !errors.Is(err, canary) {
			t.Fatalf("deferred root identity error = %v", err)
		}
	})

	t.Run("nested directory identity", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		nested := filepath.Join(root, "nested")
		if err := os.Mkdir(nested, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nested, "source.json"), []byte("source"), 0o600); err != nil {
			t.Fatal(err)
		}
		originalIdentity := legacyExistingIdentity
		swapTestHook(t, &legacyExistingIdentity, func(
			path string,
		) (fileidentity.Identity, fileidentity.ObjectType, bool, error) {
			if filepath.Clean(path) == filepath.Clean(nested) {
				return fileidentity.Identity{}, 0, false, canary
			}
			return originalIdentity(path)
		})
		options := testOptions()
		options.Legacy = deferredLegacyOptions(root,
			func() ([]LegacySource, error) {
				return []LegacySource{{ID: "source", Relative: "nested/source.json"}}, nil
			},
			validImporter,
		)
		databaseHome := t.TempDir()
		databasePath := filepath.Join(databaseHome, "store.db")
		database, err := Open(
			deferredMigrationContext(t, databaseHome, databasePath),
			databasePath,
			options,
		)
		if database != nil {
			_ = database.Close()
		}
		if !errors.Is(err, canary) {
			t.Fatalf("deferred nested identity error = %v", err)
		}
	})
}
