package sqliteprovider

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"
)

var controlTestDatabaseSequence atomic.Uint64

func openControlTestDatabase(t *testing.T) *sql.DB {
	t.Helper()

	name := "sqliteprovider-control-" + strconv.FormatUint(controlTestDatabaseSequence.Add(1), 10)
	database, err := sql.Open("sqlite", "file:"+name+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.PingContext(t.Context()); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close control database: %v", err)
		}
	})
	return database
}

func closedControlTestDatabase(t *testing.T) *sql.DB {
	t.Helper()

	database := openControlTestDatabase(t)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return database
}

func TestSchemaVersionRoundTripAndBounds(t *testing.T) {
	database := openControlTestDatabase(t)

	if err := SetSchemaVersion(t.Context(), database, 7); err != nil {
		t.Fatal(err)
	}
	if version, err := SchemaVersion(t.Context(), database); err != nil || version != 7 {
		t.Fatalf("SchemaVersion() = %d, %v; want 7, nil", version, err)
	}
	if err := SetSchemaVersion(t.Context(), database, 0); err != nil {
		t.Fatal(err)
	}
	if err := SetSchemaVersion(t.Context(), database, math.MaxInt32); err != nil {
		t.Fatal(err)
	}
	if version, err := SchemaVersion(t.Context(), database); err != nil || version != math.MaxInt32 {
		t.Fatalf("SchemaVersion() = %d, %v; want %d, nil", version, err, math.MaxInt32)
	}
	if _, err := database.ExecContext(t.Context(), "PRAGMA main.user_version = -1"); err != nil {
		t.Fatal(err)
	}
	if version, err := SchemaVersion(t.Context(), database); err == nil || version != 0 {
		t.Fatalf("SchemaVersion() = %d, %v after negative provider value", version, err)
	}

	for _, version := range []int{-1, -2} {
		if err := SetSchemaVersion(t.Context(), database, version); err == nil {
			t.Fatalf("SetSchemaVersion(%d) succeeded", version)
		}
	}
	if strconv.IntSize > 32 {
		tooLargeValue := int64(math.MaxInt32)
		tooLargeValue++
		tooLarge := int(tooLargeValue)
		if err := SetSchemaVersion(t.Context(), database, tooLarge); err == nil {
			t.Fatalf("SetSchemaVersion(%d) succeeded", tooLarge)
		}
	}
}

func TestControlBoundariesRejectNilContextsAndUnavailableQueries(t *testing.T) {
	database := openControlTestDatabase(t)
	if err := SetSchemaVersion(t.Context(), database, 11); err != nil {
		t.Fatal(err)
	}

	if _, err := SchemaVersion(nil, database); err == nil {
		t.Fatal("SchemaVersion accepted nil context")
	}
	if err := SetSchemaVersion(nil, database, 12); err == nil {
		t.Fatal("SetSchemaVersion accepted nil context")
	}
	if err := CheckIntegrity(nil, database); err == nil {
		t.Fatal("CheckIntegrity accepted nil context")
	}
	if err := CheckIntegrityOnly(nil, database); err == nil {
		t.Fatal("CheckIntegrityOnly accepted nil context")
	}
	if err := CheckForeignKeys(nil, database); err == nil {
		t.Fatal("CheckForeignKeys accepted nil context")
	}
	if version, err := SchemaVersion(t.Context(), database); err != nil || version != 11 {
		t.Fatalf("nil-context mutation changed version to %d: %v", version, err)
	}

	if _, err := SchemaVersion(t.Context(), nil); err == nil {
		t.Fatal("SchemaVersion accepted nil query boundary")
	}
	if err := SetSchemaVersion(t.Context(), nil, 1); err == nil {
		t.Fatal("SetSchemaVersion accepted nil execution boundary")
	}
	if err := CheckIntegrity(t.Context(), nil); err == nil {
		t.Fatal("CheckIntegrity accepted nil query boundary")
	}
	if err := CheckIntegrityOnly(t.Context(), nil); err == nil {
		t.Fatal("CheckIntegrityOnly accepted nil query boundary")
	}
	if err := CheckForeignKeys(t.Context(), nil); err == nil {
		t.Fatal("CheckForeignKeys accepted nil query boundary")
	}

	closed := closedControlTestDatabase(t)
	if _, err := SchemaVersion(t.Context(), closed); err == nil {
		t.Fatal("SchemaVersion accepted closed database")
	}
	if err := SetSchemaVersion(t.Context(), closed, 1); err == nil {
		t.Fatal("SetSchemaVersion accepted closed database")
	}
	if err := CheckIntegrityOnly(t.Context(), closed); err == nil {
		t.Fatal("CheckIntegrityOnly accepted closed database")
	}
	if err := CheckIntegrity(t.Context(), closed); err == nil {
		t.Fatal("CheckIntegrity accepted closed database")
	}
	if err := CheckForeignKeys(t.Context(), closed); err == nil {
		t.Fatal("CheckForeignKeys accepted closed database")
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CheckForeignKeys(canceled, database); !errors.Is(err, context.Canceled) {
		t.Fatalf("CheckForeignKeys canceled error = %v", err)
	}
}

func TestIntegrityChecksRealSQLiteState(t *testing.T) {
	database := openControlTestDatabase(t)
	if _, err := database.ExecContext(t.Context(), `
		CREATE TABLE parent (id INTEGER PRIMARY KEY);
		CREATE TABLE child (parent_id INTEGER REFERENCES parent(id));
	`); err != nil {
		t.Fatal(err)
	}
	if err := CheckIntegrity(t.Context(), database); err != nil {
		t.Fatalf("clean CheckIntegrity() = %v", err)
	}

	if _, err := database.ExecContext(t.Context(), "INSERT INTO child(parent_id) VALUES (99)"); err != nil {
		t.Fatal(err)
	}
	if err := CheckForeignKeys(t.Context(), database); err == nil {
		t.Fatal("CheckForeignKeys accepted orphan row")
	}
	if err := CheckIntegrity(t.Context(), database); err == nil {
		t.Fatal("CheckIntegrity accepted orphan row")
	}
}

type corruptIntegrityResultQueryer struct{ *sql.DB }

func (queryer corruptIntegrityResultQueryer) QueryRowContext(
	ctx context.Context,
	query string,
	arguments ...any,
) *sql.Row {
	if strings.Contains(query, "integrity_check") {
		return queryer.DB.QueryRowContext(ctx, "SELECT 'corrupt-detail-secret'")
	}
	return queryer.DB.QueryRowContext(ctx, query, arguments...)
}

func TestIntegrityCheckRedactsRealNonOKResult(t *testing.T) {
	database := openControlTestDatabase(t)
	err := CheckIntegrityOnly(t.Context(), corruptIntegrityResultQueryer{DB: database})
	if !errors.Is(err, errIntegrityCheck) {
		t.Fatalf("CheckIntegrityOnly non-ok result = %v", err)
	}
	if strings.Contains(err.Error(), "corrupt-detail-secret") {
		t.Fatalf("CheckIntegrityOnly exposed provider result: %v", err)
	}
}

func TestForeignKeyCheckPropagatesDeferredRowsCloseFailureSafely(t *testing.T) {
	closeErr := errors.New("sensitive provider rows close failure")
	database := openProviderScript(t, providerScriptStep{
		query: "foreign_key_check", columns: []string{"table"}, closeErr: closeErr,
		hasNextResultSet: true,
	})
	err := CheckForeignKeys(t.Context(), database)
	if !errors.Is(err, errControlUnavailable) {
		t.Fatalf("CheckForeignKeys close error = %v", err)
	}
	if errors.Is(err, closeErr) || strings.Contains(err.Error(), closeErr.Error()) {
		t.Fatalf("CheckForeignKeys exposed provider close diagnostics: %v", err)
	}
}

func TestControlDiagnosticClassifierPreservesOnlyBusyOrLockedCause(t *testing.T) {
	cause := errors.New("provider contention")
	fallback := errors.New("redacted diagnostic")
	classified := false
	got := controlDiagnosticErrorWithClassifier(
		t.Context(), cause, fallback, func(err error) bool {
			classified = true
			return errors.Is(err, cause)
		},
	)
	if !classified || !errors.Is(got, cause) {
		t.Fatalf("classified diagnostic = %v, classifier called=%t", got, classified)
	}
	if fallbackGot := controlDiagnosticErrorWithClassifier(
		t.Context(), cause, fallback, nil,
	); !errors.Is(fallbackGot, fallback) {
		t.Fatalf("nil-classifier diagnostic = %v", fallbackGot)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	classified = false
	got = controlDiagnosticErrorWithClassifier(canceled, cause, fallback, func(error) bool {
		classified = true
		return true
	})
	if !errors.Is(got, context.Canceled) || classified {
		t.Fatalf("canceled diagnostic = %v, classifier called=%t", got, classified)
	}
}
