//nolint:govet // Narrow assertions intentionally use independent error scopes.
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

func TestLegacyImportHorizonRejectsAndArchivesLateSources(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(root, "late.json")
	archiveRoot := filepath.Join(root, "archive")
	databasePath := filepath.Join(root, "db", "store.db")
	imports := 0
	finalizers := 0
	seals := 0
	options := testOptions()
	options.Legacy = &LegacyOptions{
		SourceRoot: root, ArchiveRoot: archiveRoot,
		Sources: func() ([]LegacySource, error) {
			if _, err := os.Lstat(sourcePath); errors.Is(err, os.ErrNotExist) {
				return nil, nil
			} else if err != nil {
				return nil, err
			}
			return []LegacySource{{ID: "late", Relative: "late.json"}}, nil
		},
		Import: func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
			imports++
			return ImportResult{Imported: 1}, nil
		},
		Finalize: func(context.Context, *sql.Conn, LegacyFinalizeInput) error {
			finalizers++
			return nil
		},
		Seal: func(context.Context, *sql.Conn) error {
			seals++
			return nil
		},
	}

	database, err := Open(t.Context(), databasePath, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if imports != 0 || finalizers != 0 || seals != 1 {
		t.Fatalf("empty first open calls = import:%d finalize:%d seal:%d", imports, finalizers, seals)
	}

	// Payload is deliberately not valid domain JSON. Closed horizons audit and
	// archive bytes without invoking parsers or relationship finalizers.
	if err := os.WriteFile(sourcePath, []byte("{malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalRemove := legacyArchiveRemove
	legacyArchiveRemove = func(string) error { return errors.New("injected archive interruption") }
	t.Cleanup(func() { legacyArchiveRemove = originalRemove })
	if database, err = Open(t.Context(), databasePath, options); err == nil {
		_ = database.Close()
		t.Fatal("interrupted late-source archive unexpectedly succeeded")
	}
	if imports != 0 || finalizers != 0 || seals != 2 {
		t.Fatalf("late-source calls = import:%d finalize:%d seal:%d, open error: %v",
			imports, finalizers, seals, err)
	}
	if _, err := os.Lstat(sourcePath); err != nil {
		t.Fatalf("archive interruption lost late source: %v", err)
	}

	legacyArchiveRemove = originalRemove
	database, err = Open(t.Context(), databasePath, options)
	if err != nil {
		t.Fatal(err)
	}
	var imported, skipped, horizons int
	if err := database.QueryRow(`SELECT imported_count, skipped_count
	    FROM storage_imports WHERE component = 'test-store' AND source_id = 'late'`).Scan(
		&imported, &skipped,
	); err != nil || imported != 0 || skipped != 1 {
		t.Fatalf("late accounting = %d/%d, %v", imported, skipped, err)
	}
	var code string
	if err := database.QueryRow(`SELECT issue_code FROM storage_import_issues
	    WHERE component = 'test-store' AND source_id = 'late'`).Scan(&code); err != nil ||
		code != "late-source" {
		t.Fatalf("late issue = %q, %v", code, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM storage_import_horizons
	    WHERE component = 'test-store'`).Scan(&horizons); err != nil || horizons != 1 {
		t.Fatalf("horizon rows = %d, %v", horizons, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if imports != 0 || finalizers != 0 || seals != 3 {
		t.Fatalf("archive retry calls = import:%d finalize:%d seal:%d", imports, finalizers, seals)
	}
	if _, err := os.Lstat(sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("late source remains after retry: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(archiveRoot, "late.json")); err != nil ||
		string(data) != "{malformed" {
		t.Fatalf("late archive = %q, %v", data, err)
	}

	// A recreated independent source cannot overwrite the immutable archive.
	if err := os.WriteFile(sourcePath, []byte("{malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	database, err = Open(t.Context(), databasePath, options)
	if database != nil {
		_ = database.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "exist independently") {
		t.Fatalf("recreated source error = %v", err)
	}
	if err := os.Remove(sourcePath); err != nil {
		t.Fatal(err)
	}

	// Changed bytes never replace the immutable archive or reach the importer.
	if err := os.WriteFile(sourcePath, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	database, err = Open(t.Context(), databasePath, options)
	if database != nil {
		_ = database.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "changed after import") {
		t.Fatalf("changed late source error = %v", err)
	}
	if imports != 0 || finalizers != 0 {
		t.Fatalf("closed horizon invoked domain migration = import:%d finalize:%d", imports, finalizers)
	}
}

func TestLegacyImportHorizonRollsBackWithCustomSeal(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(root, "legacy.json")
	if err := os.WriteFile(sourcePath, []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	sealFails := true
	imports := 0
	options := testOptions()
	options.Legacy = &LegacyOptions{
		SourceRoot: root, ArchiveRoot: filepath.Join(root, "archive"),
		Sources: func() ([]LegacySource, error) {
			return []LegacySource{{ID: "legacy", Relative: "legacy.json"}}, nil
		},
		Import: func(ctx context.Context, conn *sql.Conn, _ LegacyInput) (ImportResult, error) {
			imports++
			_, err := conn.ExecContext(ctx, `INSERT INTO records(id, value) VALUES ('legacy', 'value')`)
			return ImportResult{Imported: 1}, err
		},
		Seal: func(context.Context, *sql.Conn) error {
			if sealFails {
				return errors.New("injected seal crash")
			}
			return nil
		},
	}
	path := filepath.Join(root, "store.db")
	if database, err := Open(t.Context(), path, options); err == nil {
		_ = database.Close()
		t.Fatal("failed custom seal unexpectedly committed")
	}
	sealFails = false
	database, err := Open(t.Context(), path, options)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if imports != 2 {
		t.Fatalf("import calls = %d, want rollback plus committed retry", imports)
	}
	var records, horizons int
	if err := database.QueryRow(`SELECT COUNT(*) FROM records`).Scan(&records); err != nil || records != 1 {
		t.Fatalf("committed records = %d, %v", records, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM storage_import_horizons
	    WHERE component = 'test-store'`).Scan(&horizons); err != nil || horizons != 1 {
		t.Fatalf("committed horizons = %d, %v", horizons, err)
	}
}
