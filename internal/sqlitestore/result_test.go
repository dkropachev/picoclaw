package sqlitestore

import (
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

type resultFixture struct {
	rows int64
	err  error
}

func TestScanStrings(t *testing.T) {
	if _, err := ScanStrings(nil); err == nil {
		t.Fatal("nil rows accepted")
	}
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	rows, err := database.Query(`SELECT 'a' UNION ALL SELECT 'b'`)
	if err != nil {
		t.Fatal(err)
	}
	values, err := ScanStrings(rows)
	if err != nil || len(values) != 2 || values[0] != "a" || values[1] != "b" {
		t.Fatalf("values=%#v err=%v", values, err)
	}
	rows, err = database.Query(`SELECT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ScanStrings(rows); err == nil {
		t.Fatal("NULL text scanned")
	}
}

func (resultFixture) LastInsertId() (int64, error) { return 0, nil }
func (result resultFixture) RowsAffected() (int64, error) {
	return result.rows, result.err
}

func TestRequireOneRow(t *testing.T) {
	conflict := errors.New("conflict")
	if err := RequireOneRow(resultFixture{rows: 1}, conflict); err != nil {
		t.Fatal(err)
	}
	if err := RequireOneRow(nil, conflict); err == nil {
		t.Fatal("nil result accepted")
	}
	want := errors.New("driver")
	if err := RequireOneRow(resultFixture{err: want}, conflict); !errors.Is(err, want) {
		t.Fatalf("driver error=%v", err)
	}
	if err := RequireOneRow(resultFixture{}, conflict); !errors.Is(err, conflict) {
		t.Fatalf("conflict error=%v", err)
	}
}
