package dashboardauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/web/backend/launcherconfig"
)

func TestOpenCreatesHardenedSchemaAndReopens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, databaseFilename)
	store, openErr := Open(path)
	if openErr != nil {
		t.Fatalf("Open() error = %v", openErr)
	}

	var version, foreignKeys, busyTimeout, synchronous int
	var journal string
	for query, destination := range map[string]any{
		"PRAGMA user_version": &version,
		"PRAGMA foreign_keys": &foreignKeys,
		"PRAGMA busy_timeout": &busyTimeout,
		"PRAGMA synchronous":  &synchronous,
		"PRAGMA journal_mode": &journal,
	} {
		if err := store.db.QueryRow(query).Scan(destination); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	if version != 1 || foreignKeys != 1 || busyTimeout != 5000 || synchronous != 2 ||
		!strings.EqualFold(journal, "wal") {
		t.Fatalf("SQLite settings = version:%d fk:%d busy:%d sync:%d journal:%q",
			version, foreignKeys, busyTimeout, synchronous, journal)
	}
	conn, connErr := store.db.Conn(t.Context())
	if connErr != nil {
		t.Fatal(connErr)
	}
	for name, want := range map[string]struct{ objectType, sql string }{
		"dashboard_credentials":                   {"table", sqlCreateCredentials},
		"launcher_auth_legacy_imports":            {"table", sqlCreateLegacyImports},
		"launcher_auth_legacy_imports_status_idx": {"index", sqlCreateLegacyImportsIndex},
	} {
		if err := sqlitestore.ValidateSchemaObject(t.Context(), conn, want.objectType, name, want.sql); err != nil {
			t.Fatalf("validate DDL %s: %v", name, err)
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO dashboard_credentials(id, bcrypt_hash) VALUES (2, 'x')`); err == nil {
		t.Fatal("dashboard credential singleton constraint was not enforced")
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("database mode = %v, %v", info, err)
		}
		if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("database directory mode = %v, %v", info, err)
		}
	}
	if err := store.SetPassword(t.Context(), "reopen-password"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, reopenErr := Open(path)
	if reopenErr != nil {
		t.Fatalf("reopen: %v", reopenErr)
	}
	defer reopened.Close()
	if ok, err := reopened.VerifyPassword(t.Context(), "reopen-password"); err != nil || !ok {
		t.Fatalf("VerifyPassword() = %t, %v", ok, err)
	}
}

func TestOpenRejectsTooNewAndInvalidSchema(t *testing.T) {
	t.Run("too new", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), databaseFilename)
		store, openErr := Open(path)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if _, err := store.db.Exec(`PRAGMA user_version = 2`); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		_, reopenErr := Open(path)
		if !errors.Is(reopenErr, sqlitestore.ErrTooNew) {
			t.Fatalf("Open() error = %v, want ErrTooNew", reopenErr)
		}
	})

	t.Run("invalid schema", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), databaseFilename)
		store, openErr := Open(path)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if _, err := store.db.Exec(`DROP INDEX launcher_auth_legacy_imports_status_idx`); err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		_, reopenErr := Open(path)
		if !errors.Is(reopenErr, sqlitestore.ErrInvalidSchema) {
			t.Fatalf("Open() error = %v, want ErrInvalidSchema", reopenErr)
		}
	})
}

func TestOpenRejectsUnrelatedSchemaObjects(t *testing.T) {
	for name, statement := range map[string]string{
		"table": `CREATE TABLE rogue_launcher_state(id INTEGER PRIMARY KEY) STRICT`,
		"view":  `CREATE VIEW rogue_launcher_view AS SELECT id FROM dashboard_credentials`,
		"index": `CREATE INDEX rogue_launcher_import_idx ON storage_imports(source_id)`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), DBFilename)
			store, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(statement); err != nil {
				_ = store.Close()
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(path); !errors.Is(err, sqlitestore.ErrInvalidSchema) {
				if reopened != nil {
					_ = reopened.Close()
				}
				t.Fatalf("Open() error = %v, want ErrInvalidSchema", err)
			}
		})
	}
}

func TestOpenPreservesUnversionedDatabaseAndArchivesLegacyConfig(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, databaseFilename)
	legacyHash := testHash(t, "database-password")
	raw, sqlOpenErr := sql.Open("sqlite", dbPath)
	if sqlOpenErr != nil {
		t.Fatal(sqlOpenErr)
	}
	if _, err := raw.Exec(
		strings.Replace(sqlLegacyCreateCredentials, "CREATE TABLE", "CREATE TABLE IF NOT EXISTS", 1),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO dashboard_credentials(id, bcrypt_hash) VALUES (1, ?)`, legacyHash); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(dir, launcherconfig.FileName)
	original := []byte("{\n  \"port\": 19001,\n  \"public\": true,\n  \"dashboard_password_hash\": \"" +
		testHash(t, "config-password") + "\",\n  \"launcher_token\": \"token-password\"\n}\n")
	if err := os.WriteFile(configPath, original, 0o640); err != nil {
		t.Fatal(err)
	}
	store, openErr := OpenWithLauncherConfig(dbPath, configPath)
	if openErr != nil {
		t.Fatalf("OpenWithLauncherConfig() error = %v", openErr)
	}
	defer store.Close()
	if ok, err := store.VerifyPassword(t.Context(), "database-password"); err != nil || !ok {
		t.Fatalf("existing database password = %t, %v", ok, err)
	}
	if ok, err := store.VerifyPassword(t.Context(), "config-password"); err != nil || ok {
		t.Fatalf("config password unexpectedly won = %t, %v", ok, err)
	}
	assertCleanConfigAndArchive(t, configPath, original, 0o640)
	var source, status string
	var imported, skipped int
	if err := store.db.QueryRow(`SELECT credential_source, imported_count, skipped_count, archive_status
        FROM launcher_auth_legacy_imports WHERE source_id = ?`, legacySourceID).
		Scan(&source, &imported, &skipped, &status); err != nil {
		t.Fatal(err)
	}
	if source != "existing-database" || imported != 0 || skipped != 0 || status != "complete" {
		t.Fatalf("import ledger = %q/%d/%d/%q", source, imported, skipped, status)
	}
}

func TestLegacyCredentialPrecedenceAndInvalidHashFallback(t *testing.T) {
	tests := []struct {
		name       string
		hash       string
		token      string
		password   string
		source     string
		skipped    int
		issueValid bool
	}{
		{
			name: "valid hash wins", hash: testHash(t, "hash-password"), token: "token-password",
			password: "hash-password", source: "dashboard-password-hash",
		},
		{
			name: "invalid hash falls through to token", hash: "not-a-bcrypt-hash", token: "token-password",
			password: "token-password", source: "launcher-token", skipped: 1, issueValid: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, launcherconfig.FileName)
			body, marshalErr := json.Marshal(map[string]any{
				"port": DefaultTestPort, "dashboard_password_hash": tt.hash, "launcher_token": tt.token,
			})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			body = append(body, '\n')
			if err := os.WriteFile(configPath, body, 0o600); err != nil {
				t.Fatal(err)
			}
			store, openErr := OpenWithLauncherConfig(filepath.Join(dir, databaseFilename), configPath)
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer store.Close()
			if ok, err := store.VerifyPassword(t.Context(), tt.password); err != nil || !ok {
				t.Fatalf("VerifyPassword() = %t, %v", ok, err)
			}
			var source string
			var skipped int
			var issue sql.NullString
			if err := store.db.QueryRow(`SELECT credential_source, skipped_count, issue_code
                    FROM launcher_auth_legacy_imports`).Scan(&source, &skipped, &issue); err != nil {
				t.Fatal(err)
			}
			if source != tt.source || skipped != tt.skipped || issue.Valid != tt.issueValid {
				t.Fatalf("ledger = source:%q skipped:%d issue:%v", source, skipped, issue)
			}
			assertCleanConfigAndArchive(t, configPath, body, 0o600)
		})
	}
}

func TestLegacyArchiveRetryAndConcurrentFinish(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, launcherconfig.FileName)
	body := []byte(`{"port":18800,"launcher_token":"retry-password"}` + "\n")
	if err := os.WriteFile(configPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, "legacy-json", legacyArchiveVersion, launcherconfig.FileName)
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, []byte("collision"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, databaseFilename)
	if _, err := OpenWithLauncherConfig(dbPath, configPath); err == nil {
		t.Fatal("archive collision unexpectedly succeeded")
	}
	data, err := os.ReadFile(configPath)
	if err != nil || !legacyAuthFieldsPresent(t, data) {
		t.Fatalf("legacy fields were stripped before archive: %q, %v", data, err)
	}
	if err := os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}

	const openers = 4
	stores := make([]*Store, openers)
	errs := make([]error, openers)
	var wg sync.WaitGroup
	for i := range openers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			stores[index], errs[index] = OpenWithLauncherConfig(dbPath, configPath)
		}(i)
	}
	wg.Wait()
	for i := range openers {
		if errs[i] != nil {
			t.Fatalf("concurrent open %d: %v", i, errs[i])
		}
		if ok, err := stores[i].VerifyPassword(context.Background(), "retry-password"); err != nil || !ok {
			t.Fatalf("concurrent store %d password = %t, %v", i, ok, err)
		}
		if err := stores[i].Close(); err != nil {
			t.Fatal(err)
		}
	}
	assertCleanConfigAndArchive(t, configPath, body, 0o600)
}

func TestConfigWithoutLegacyAuthDoesNotCreateArchive(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, launcherconfig.FileName)
	if err := os.WriteFile(configPath, []byte(`{"port":18800}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenWithLauncherConfig(filepath.Join(dir, databaseFilename), configPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM launcher_auth_legacy_imports`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("legacy import rows = %d, want 0", count)
	}
	if _, err := os.Stat(filepath.Join(dir, "legacy-json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy archive root exists: %v", err)
	}
}

func TestLegacyArchiveRejectsSymlinkParentWithoutCleaningSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available to unprivileged Windows tests")
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, launcherconfig.FileName)
	body := []byte(`{"port":18800,"launcher_token":"symlink-password"}`)
	if err := os.WriteFile(configPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "legacy-json")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWithLauncherConfig(filepath.Join(dir, databaseFilename), configPath); err == nil ||
		!strings.Contains(err.Error(), "real directory") {
		t.Fatalf("OpenWithLauncherConfig() error = %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil || !legacyAuthFieldsPresent(t, data) {
		t.Fatalf("legacy source changed after rejected archive parent: %q, %v", data, err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("symlink destination entries = %v, %v", entries, err)
	}
}

const DefaultTestPort = 18800

func testHash(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(hash)
}

func assertCleanConfigAndArchive(
	t *testing.T,
	configPath string,
	original []byte,
	mode os.FileMode,
) {
	t.Helper()
	clean, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if legacyAuthFieldsPresent(t, clean) {
		t.Fatalf("cleaned config retains legacy auth fields: %s", clean)
	}
	archivePath := filepath.Join(filepath.Dir(configPath), "legacy-json", legacyArchiveVersion, launcherconfig.FileName)
	archived, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(archived) != string(original) {
		t.Fatalf("archive = %q, want %q", archived, original)
	}
	entries, err := os.ReadDir(filepath.Dir(archivePath))
	if err != nil || len(entries) != 1 || entries[0].Name() != launcherconfig.FileName {
		t.Fatalf("archive directory entries = %v, %v", entries, err)
	}
	configInfo, configErr := os.Stat(configPath)
	archiveInfo, archiveErr := os.Stat(archivePath)
	if configErr != nil || archiveErr != nil || os.SameFile(configInfo, archiveInfo) {
		t.Fatalf("config/archive inode separation = %v/%v, %v/%v", configInfo, archiveInfo, configErr, archiveErr)
	}
	if runtime.GOOS != "windows" {
		if archiveInfo.Mode().Perm() != mode.Perm() {
			t.Fatalf("archive mode = %v, want %o", archiveInfo.Mode().Perm(), mode.Perm())
		}
	}
}

func legacyAuthFieldsPresent(t *testing.T, data []byte) bool {
	t.Helper()
	present, err := hasLegacyAuthFields(data)
	if err != nil {
		t.Fatal(err)
	}
	return present
}
