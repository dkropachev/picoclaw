//nolint:govet // Independent failure-boundary assertions intentionally reuse narrow error names.
package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestCoverageOpenAuthorityAndOnlineFailureBoundaries(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "store.db")
	restore := database.SuspendProviderTestAuthority()
	if opened, err := Open(t.Context(), path, testOptions()); opened != nil ||
		database.CodeOf(err) != database.CodeUnauthorized {
		t.Fatalf("unauthorized Open = %#v, %v", opened, err)
	}
	restore()

	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fence.Close() })

	canary := errors.New("online source canary")
	options := testOptions()
	options.Legacy = &LegacyOptions{
		SourceRoot: home, ArchiveRoot: filepath.Join(home, "archive"),
		Sources: func() ([]LegacySource, error) { return nil, canary },
		Import: func(context.Context, *sql.Conn, LegacyInput) (ImportResult, error) {
			return ImportResult{}, nil
		},
	}
	if opened, err := Open(
		t.Context(),
		filepath.Join(home, "source-error.db"),
		options,
	); opened != nil ||
		!errors.Is(err, canary) {
		t.Fatalf("online source error = %#v, %v", opened, err)
	}
	legacy := filepath.Join(home, "legacy.json")
	if err := os.WriteFile(legacy, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	options.Legacy.Sources = func() ([]LegacySource, error) {
		return []LegacySource{{ID: "legacy", Relative: "legacy.json"}}, nil
	}
	if opened, err := Open(
		t.Context(),
		filepath.Join(home, "legacy-present.db"),
		options,
	); opened != nil ||
		database.CodeOf(err) != database.CodeMigrationRequired {
		t.Fatalf("online legacy source = %#v, %v", opened, err)
	}

	originalMigrate := migrateOpenedSQLiteDatabase
	migrateOpenedSQLiteDatabase = func(context.Context, *sql.DB, Options) error { return canary }
	if opened, err := Open(
		t.Context(),
		filepath.Join(home, "fresh-failure.db"),
		testOptions(),
	); opened != nil ||
		!errors.Is(err, canary) {
		t.Fatalf("online initialization failure = %#v, %v", opened, err)
	}
	migrateOpenedSQLiteDatabase = originalMigrate

	currentPath := filepath.Join(home, "current.db")
	current, err := Open(t.Context(), currentPath, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	invalidOptions := testOptions()
	invalidOptions.Validate = func(context.Context, *sql.Conn) error { return canary }
	if opened, err := Open(t.Context(), currentPath, invalidOptions); opened != nil ||
		!errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("online schema validation failure = %#v, %v", opened, err)
	}
}

func TestCoverageOnlineReadinessAndLegacySourceHelpers(t *testing.T) {
	if exists, err := onlineLegacySourceExists(nil); err != nil || exists {
		t.Fatalf("nil legacy options = %v, %v", exists, err)
	}
	if exists, err := onlineLegacySourceExists(&LegacyOptions{}); err != nil || exists {
		t.Fatalf("nil source enumerator = %v, %v", exists, err)
	}
	root := t.TempDir()
	canary := errors.New("legacy source canary")
	if _, err := onlineLegacySourceExists(&LegacyOptions{
		Sources: func() ([]LegacySource, error) { return nil, canary },
	}); !errors.Is(err, canary) {
		t.Fatalf("source enumerator error = %v", err)
	}
	for _, relative := range []string{".", "../escape", filepath.Join(root, "absolute")} {
		options := &LegacyOptions{SourceRoot: root, Sources: func() ([]LegacySource, error) {
			return []LegacySource{{ID: "source", Relative: relative}}, nil
		}}
		if _, err := onlineLegacySourceExists(options); err == nil {
			t.Fatalf("invalid relative source %q accepted", relative)
		}
	}
	missingOptions := &LegacyOptions{SourceRoot: root, Sources: func() ([]LegacySource, error) {
		return []LegacySource{{ID: "missing", Relative: "missing.json"}}, nil
	}}
	if exists, err := onlineLegacySourceExists(missingOptions); err != nil || exists {
		t.Fatalf("missing source = %v, %v", exists, err)
	}
	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	directoryOptions := &LegacyOptions{SourceRoot: root, Sources: func() ([]LegacySource, error) {
		return []LegacySource{{ID: "directory", Relative: "directory"}}, nil
	}}
	if _, err := onlineLegacySourceExists(directoryOptions); err == nil {
		t.Fatal("directory legacy source accepted")
	}
	regular := filepath.Join(root, "regular.json")
	if err := os.WriteFile(regular, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	regularOptions := &LegacyOptions{SourceRoot: root, Sources: func() ([]LegacySource, error) {
		return []LegacySource{{ID: "regular", Relative: "regular.json"}}, nil
	}}
	if exists, err := onlineLegacySourceExists(regularOptions); err != nil || !exists {
		t.Fatalf("regular source = %v, %v", exists, err)
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(regular, link); err == nil {
		linkOptions := &LegacyOptions{SourceRoot: root, Sources: func() ([]LegacySource, error) {
			return []LegacySource{{ID: "link", Relative: "link.json"}}, nil
		}}
		if _, err := onlineLegacySourceExists(linkOptions); err == nil {
			t.Fatal("symlink legacy source accepted")
		}
	}
}

func TestCoverageCurrentStoreReadinessQueries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := Open(t.Context(), path, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	options := testOptions()
	if initialize, err := requireOnlineCurrentStore(t.Context(), db, options); err != nil || initialize {
		t.Fatalf("current readiness = %v, %v", initialize, err)
	}
	if ready, err := sharedImportSchemaObjectsPresent(t.Context(), db); err != nil || !ready {
		t.Fatalf("shared import schema = %v, %v", ready, err)
	}
	if closed, err := sharedImportHorizonClosed(t.Context(), db, options.Component); err != nil || closed {
		t.Fatalf("shared import horizon = %v, %v", closed, err)
	}
	if _, err := db.Exec(
		"INSERT INTO storage_import_horizons(component, completed_at) VALUES (?, 1)", options.Component,
	); err != nil {
		t.Fatal(err)
	}
	if closed, err := sharedImportHorizonClosed(t.Context(), db, options.Component); err != nil || !closed {
		t.Fatalf("closed shared import horizon = %v, %v", closed, err)
	}
	if _, err := db.Exec("PRAGMA user_version = 9"); err != nil {
		t.Fatal(err)
	}
	if _, err := requireOnlineCurrentStore(t.Context(), db, options); !errors.Is(err, ErrTooNew) {
		t.Fatalf("too-new online readiness = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := requireOnlineCurrentStore(t.Context(), db, options); err == nil {
		t.Fatal("closed database readiness succeeded")
	}
	if _, err := sharedImportSchemaObjectsPresent(t.Context(), db); err == nil {
		t.Fatal("closed database import schema query succeeded")
	}
	if _, err := sharedImportHorizonClosed(t.Context(), db, options.Component); err == nil {
		t.Fatal("closed database import horizon query succeeded")
	}
	if err := validateCurrentStore(t.Context(), db, options); err == nil {
		t.Fatal("closed current store validated")
	}
}

func TestCoverageCurrentStoreMissingImportSchemaAndConfigureErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := Open(t.Context(), path, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DROP TABLE storage_import_horizons"); err != nil {
		t.Fatal(err)
	}
	if err := validateCurrentStore(t.Context(), db, testOptions()); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("missing import schema validation = %v", err)
	}
	_ = db.Close()

	home := t.TempDir()
	fence, err := database.AcquireMigrationFence(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := configure(t.Context(), nil, time.Second, false, "test-store"); err == nil {
		t.Fatal("offline configure accepted nil database")
	}
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	closed, err := sqliteprovider.OpenStore(":memory:", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	if err := Immediate(t.Context(), closed, func(*sql.Conn) error { return nil }); err == nil {
		t.Fatal("transaction began on closed database")
	}
}
