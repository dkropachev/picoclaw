package sqlbridge

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/database"
)

func TestRuntimeStatementPolicyAcceptsDataAccessAndReadOnlyMetadata(t *testing.T) {
	t.Parallel()

	allowed := []string{
		"SELECT 1",
		"  -- leading comment\n SeLeCt value FROM records",
		"/* CREATE TABLE hidden(x); */ SELECT 1",
		"SELECT '; DROP TABLE records', \"attach\", [vacuum] FROM records",
		"SELECT 'it''s; safe', `semi;colon` FROM records",
		"SELECT 1 /* ; ATTACH DATABASE '/tmp/x' AS x; */",
		"SELECT 1; -- one optional trailing terminator",
		"SELECT 1 /* trailing comment */ ; /* trailing comment */",
		"VALUES (1), (2)",
		"INSERT OR REPLACE INTO records(id, value) VALUES (?, ?)",
		"UPDATE records SET value = ? WHERE id = ?",
		"DELETE FROM records WHERE id = ?",
		"REPLACE INTO records(id, value) VALUES (?, ?)",
		"WITH selected AS (SELECT 1 AS id) SELECT id FROM selected",
		"WITH selected AS (SELECT 1 AS id) UPDATE records SET value='x' WHERE id IN selected",
		"PRAGMA user_version",
		"PrAgMa MAIN.user_version",
		"PRAGMA [schema_version]",
		"PRAGMA foreign_keys",
		"PRAGMA application_id",
		"PRAGMA table_info(records)",
		"PRAGMA main.table_xinfo('records')",
		"PRAGMA index_list(\"records\")",
		"PRAGMA index_info([records_by_id])",
		"PRAGMA index_xinfo(`records_by_id`)",
		"PRAGMA foreign_key_list(records)",
	}
	for _, statement := range allowed {
		if err := ValidateStatement(statement, ModeRuntime); err != nil {
			t.Errorf("ValidateStatement(%q, runtime): %v", statement, err)
		}
	}
}

func TestRuntimeStatementPolicyRejectsControlAndSmuggling(t *testing.T) {
	t.Parallel()

	forbidden := []string{
		"CREATE TABLE records(id INTEGER)",
		"create index records_by_id on records(id)",
		"ALTER TABLE records ADD COLUMN value TEXT",
		"DROP TABLE records",
		"ATTACH DATABASE '/tmp/other.db' AS other",
		"DETACH DATABASE other",
		"VACUUM",
		"VACUUM INTO '/tmp/copy.db'",
		"BEGIN",
		"BEGIN IMMEDIATE",
		"COMMIT",
		"END",
		"ROLLBACK",
		"ROLLBACK TO save",
		"SAVEPOINT save",
		"RELEASE save",
		"ANALYZE",
		"REINDEX records_by_id",
		"PRAGMA user_version = 2",
		"PRAGMA user_version(2)",
		"PRAGMA foreign_keys = OFF",
		"PRAGMA foreign_keys(OFF)",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA wal_checkpoint(TRUNCATE)",
		"PRAGMA optimize",
		"PRAGMA integrity_check",
		"PRAGMA other.user_version",
		"SELECT 1; SELECT 2",
		"SELECT 1;;",
		"SELECT 1; /**/ DROP TABLE records",
		"SELECT ';'; DELETE FROM records",
		"SELECT 1 -- semicolon hidden until newline\n; ATTACH DATABASE 'x' AS x",
		"\"SELECT\" 1",
		"[SELECT] 1",
		"EXPLAIN SELECT 1",
		"",
		"   -- comment only",
		"/* unterminated",
		"SELECT 'unterminated",
		"SELECT \"unterminated",
		"SELECT [unterminated",
		"SELECT 1\x00",
		strings.Repeat("x", MaxStatementBytes+1),
	}
	for _, statement := range forbidden {
		if err := ValidateStatement(statement, ModeRuntime); err == nil {
			t.Errorf("ValidateStatement(%q, runtime) unexpectedly succeeded", statement)
		}
	}
}

func TestOfflineStatementPolicyAllowsOnlyBoundedUpgradeSurface(t *testing.T) {
	t.Parallel()

	allowed := []string{
		"CREATE TABLE IF NOT EXISTS records(id INTEGER PRIMARY KEY, value TEXT)",
		"/* migration */ CrEaTe UnIqUe InDeX IF NOT EXISTS records_by_id ON records(id)",
		"CREATE INDEX records_by_value ON records(value)",
		"ALTER TABLE records ADD COLUMN updated_at INTEGER",
		"DROP TABLE IF EXISTS old_records",
		"DROP INDEX IF EXISTS old_records_by_id",
		"PRAGMA user_version = 0",
		"PRAGMA main.user_version = 2147483647",
		"INSERT INTO records(id) VALUES (1)",
		"UPDATE records SET value = 'migrated'",
		"DELETE FROM records WHERE id < 0",
		"SELECT COUNT(*) FROM records",
	}
	for _, statement := range allowed {
		if err := ValidateStatement(statement, ModeOffline); err != nil {
			t.Errorf("ValidateStatement(%q, offline): %v", statement, err)
		}
	}

	forbidden := []string{
		"CREATE TRIGGER records_update AFTER UPDATE ON records BEGIN SELECT 1; END",
		"CREATE VIEW record_view AS SELECT * FROM records",
		"CREATE VIRTUAL TABLE search USING fts5(value)",
		"CREATE TEMP TABLE records(id INTEGER)",
		"CREATE TEMPORARY TABLE records(id INTEGER)",
		"DROP VIEW record_view",
		"ATTACH DATABASE 'other.db' AS other",
		"DETACH other",
		"VACUUM",
		"BEGIN EXCLUSIVE",
		"COMMIT",
		"ROLLBACK",
		"SAVEPOINT migration",
		"RELEASE migration",
		"PRAGMA journal_mode = DELETE",
		"PRAGMA foreign_keys = OFF",
		"PRAGMA user_version = -1",
		"PRAGMA user_version = 2147483648",
		"PRAGMA user_version = 1 + 1",
		"PRAGMA user_version(2)",
		"CREATE TABLE records(id); ATTACH DATABASE 'other.db' AS other",
		"SELECT 1; -- split\n CREATE TABLE records(id)",
	}
	for _, statement := range forbidden {
		if err := ValidateStatement(statement, ModeOffline); err == nil {
			t.Errorf("ValidateStatement(%q, offline) unexpectedly succeeded", statement)
		}
	}
}

func TestStatementPolicyReturnsStructuredErrorsWithoutStatementText(t *testing.T) {
	t.Parallel()

	secret := "ATTACH DATABASE '/private/secret.db' AS stolen"
	err := ValidateStatement(secret, ModeRuntime)
	if err == nil {
		t.Fatal("forbidden statement unexpectedly succeeded")
	}
	if database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("error code = %s, want %s", database.CodeOf(err), database.CodeUnsupported)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "/private") {
		t.Fatalf("error reflected statement text: %v", err)
	}

	err = ValidateStatement("SELECT 'unterminated", ModeRuntime)
	if database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid lexical error code = %s, want %s", database.CodeOf(err), database.CodeInvalid)
	}
	err = ValidateStatement("SELECT 1", Mode("unknown"))
	if database.CodeOf(err) != database.CodeInvalid {
		t.Fatalf("invalid mode error code = %s, want %s", database.CodeOf(err), database.CodeInvalid)
	}
}
