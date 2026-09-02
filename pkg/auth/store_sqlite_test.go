package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/config"
)

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestAuthSQLiteSchemaConfigurationPermissionsAndReopen(t *testing.T) {
	setTestAuthHome(t)
	expiresAt := time.Date(2026, time.August, 31, 11, 30, 45, 123456789, time.UTC)
	want := &AuthCredential{
		AccessToken:       "access-token",
		RefreshToken:      "refresh-token",
		TokenType:         "Bearer",
		OAuthTokenURL:     "https://issuer.example/token",
		OAuthClientID:     "client-id",
		OAuthClientSecret: "client-secret",
		OAuthAuthStyle:    "header",
		AccountID:         "account-id",
		ExpiresAt:         expiresAt,
		Provider:          "openai",
		AuthMethod:        "oauth",
		Email:             "person@example.com",
		ProjectID:         "project-id",
	}
	if err := SetCredential("openai:work", want); err != nil {
		t.Fatalf("SetCredential() error = %v", err)
	}

	db, err := openAuthDatabase(t.Context())
	if err != nil {
		t.Fatalf("openAuthDatabase() error = %v", err)
	}
	var version, foreignKeys, busyTimeout, synchronous int
	var journal string
	for _, query := range []struct {
		statement string
		dest      any
	}{
		{statement: "PRAGMA user_version", dest: &version},
		{statement: "PRAGMA foreign_keys", dest: &foreignKeys},
		{statement: "PRAGMA busy_timeout", dest: &busyTimeout},
		{statement: "PRAGMA synchronous", dest: &synchronous},
		{statement: "PRAGMA journal_mode", dest: &journal},
	} {
		if err := db.QueryRow(query.statement).Scan(query.dest); err != nil {
			t.Fatalf("%s error = %v", query.statement, err)
		}
	}
	if version != 1 || foreignKeys != 1 || busyTimeout != 5000 || synchronous != 2 ||
		!strings.EqualFold(journal, "wal") {
		t.Fatalf(
			"SQLite configuration = version:%d foreign_keys:%d busy:%d synchronous:%d journal:%q",
			version,
			foreignKeys,
			busyTimeout,
			synchronous,
			journal,
		)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAuthSchema(t.Context(), conn); err != nil {
		t.Fatalf("validateAuthSchema() error = %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if runtime.GOOS != "windows" {
		path := authDatabasePath()
		if info, statErr := os.Stat(path); statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("auth database mode = %v, %v", info, statErr)
		}
		if info, statErr := os.Stat(filepath.Dir(path)); statErr != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("auth database directory mode = %v, %v", info, statErr)
		}
		for _, companion := range []string{path + "-wal", path + "-shm"} {
			if info, statErr := os.Stat(companion); statErr == nil && info.Mode().Perm() != 0o600 {
				t.Fatalf("SQLite companion %s mode = %o, want 0600", companion, info.Mode().Perm())
			} else if statErr != nil && !os.IsNotExist(statErr) {
				t.Fatalf("Stat(%s) error = %v", companion, statErr)
			}
		}
	}

	got, err := GetCredential("OPENAI:WORK")
	if err != nil {
		t.Fatalf("GetCredential() after reopen error = %v", err)
	}
	if !authCredentialsEqual(got, want) {
		t.Fatalf("reopened credential = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(legacyAuthFilePath()); !os.IsNotExist(err) {
		t.Fatalf("new SQLite store created mutable auth JSON: %v", err)
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestAuthSQLiteRowVersionsConstraintsAndReplaceSemantics(t *testing.T) {
	setTestAuthHome(t)
	credentialID := "openai:work"
	first := &AuthCredential{AccessToken: "first", Provider: "openai", AuthMethod: "oauth"}
	if err := SetCredential(credentialID, first); err != nil {
		t.Fatal(err)
	}
	if got := authCredentialVersion(t, credentialID); got != 1 {
		t.Fatalf("initial row version = %d, want 1", got)
	}
	second := &AuthCredential{AccessToken: "second", Provider: "openai", AuthMethod: "oauth"}
	if err := SetCredential(credentialID, second); err != nil {
		t.Fatal(err)
	}
	if got := authCredentialVersion(t, credentialID); got != 2 {
		t.Fatalf("SetCredential row version = %d, want 2", got)
	}
	updated, err := UpdateCredential(credentialID, func(current *AuthCredential) (*AuthCredential, error) {
		current.AccessToken = "third"
		return current, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := authCredentialVersion(t, credentialID); got != 3 {
		t.Fatalf("UpdateCredential row version = %d, want 3", got)
	}
	replacement := cloneCredential(updated)
	replacement.AccessToken = "fourth"
	authoritative, committed, err := persistCredentialIfCurrentDetailed(
		credentialID,
		updated,
		replacement,
	)
	if err != nil || !committed || authoritative.AccessToken != "fourth" {
		t.Fatalf("CAS result = (%#v, %v, %v)", authoritative, committed, err)
	}
	if got := authCredentialVersion(t, credentialID); got != 4 {
		t.Fatalf("CAS row version = %d, want 4", got)
	}
	staleReplacement := cloneCredential(updated)
	staleReplacement.AccessToken = "stale"
	authoritative, committed, err = persistCredentialIfCurrentDetailed(
		credentialID,
		updated,
		staleReplacement,
	)
	if err != nil || committed || authoritative.AccessToken != "fourth" {
		t.Fatalf("stale CAS result = (%#v, %v, %v)", authoritative, committed, err)
	}
	if got := authCredentialVersion(t, credentialID); got != 4 {
		t.Fatalf("stale CAS changed row version to %d", got)
	}
	unsupportedExpiry := cloneCredential(authoritative)
	unsupportedExpiry.ExpiresAt = time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	if _, _, err := persistCredentialIfCurrentDetailed(
		credentialID,
		authoritative,
		unsupportedExpiry,
	); err == nil || !strings.Contains(err.Error(), "timestamp range") {
		t.Fatalf("CAS unsupported timestamp error = %v", err)
	}
	if got := authCredentialVersion(t, credentialID); got != 4 {
		t.Fatalf("rejected CAS changed row version to %d", got)
	}

	db, err := openAuthDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE auth_credentials SET version = 0 WHERE credential_id = ?`, credentialID); err == nil {
		t.Fatal("version constraint accepted zero")
	}
	if _, err := db.Exec(
		`UPDATE auth_credentials SET provider = 'Open AI' WHERE credential_id = ?`,
		credentialID,
	); err == nil {
		t.Fatal("provider identity constraint accepted noncanonical value")
	}
	if _, err := db.Exec(`UPDATE auth_credentials SET credential_id = 'openai:a:b'
        WHERE credential_id = ?`, credentialID); err == nil {
		t.Fatal("credential identity constraint accepted multiple qualifiers")
	}
	if _, err := db.Exec(`UPDATE auth_credentials
        SET expires_at_unix_seconds = 1, expires_at_nanosecond = NULL
        WHERE credential_id = ?`, credentialID); err == nil {
		t.Fatal("timestamp constraint accepted a partial timestamp")
	}
	if _, err := db.Exec(`UPDATE auth_credentials
        SET expires_at_unix_seconds = 1, expires_at_nanosecond = 1000000000
        WHERE credential_id = ?`, credentialID); err == nil {
		t.Fatal("timestamp constraint accepted an invalid nanosecond")
	}
	if _, err := db.Exec(`UPDATE auth_credentials
        SET expires_at_unix_seconds = 253402300800, expires_at_nanosecond = 0
        WHERE credential_id = ?`, credentialID); err == nil {
		t.Fatal("timestamp constraint accepted an out-of-range second")
	}

	replacementStore := &AuthStore{Credentials: map[string]*AuthCredential{
		"anthropic": {AccessToken: "anthropic", Provider: "anthropic", AuthMethod: "token"},
	}}
	if err := SaveStore(replacementStore); err != nil {
		t.Fatalf("SaveStore() error = %v", err)
	}
	loaded, err := LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Credentials) != 1 || loaded.Credentials["anthropic"] == nil ||
		loaded.Credentials[credentialID] != nil {
		t.Fatalf("replacement store = %#v", loaded.Credentials)
	}
}

func TestAuthSQLiteRejectsTooNewInvalidAndCorruptDatabases(t *testing.T) {
	t.Run("too new", func(t *testing.T) {
		setTestAuthHome(t)
		if err := SetCredential("openai", &AuthCredential{Provider: "openai"}); err != nil {
			t.Fatal(err)
		}
		raw := openRawAuthDatabase(t)
		if _, err := raw.Exec(`PRAGMA user_version = 2`); err != nil {
			t.Fatal(err)
		}
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadStore(); !errors.Is(err, sqlitestore.ErrTooNew) {
			t.Fatalf("LoadStore() error = %v, want ErrTooNew", err)
		}
	})

	t.Run("invalid schema", func(t *testing.T) {
		setTestAuthHome(t)
		if err := SetCredential("openai", &AuthCredential{Provider: "openai"}); err != nil {
			t.Fatal(err)
		}
		raw := openRawAuthDatabase(t)
		if _, err := raw.Exec(`DROP INDEX auth_credentials_provider_idx`); err != nil {
			t.Fatal(err)
		}
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadStore(); !errors.Is(err, sqlitestore.ErrInvalidSchema) {
			t.Fatalf("LoadStore() error = %v, want ErrInvalidSchema", err)
		}
	})

	t.Run("unexpected schema object", func(t *testing.T) {
		setTestAuthHome(t)
		if err := SetCredential("openai", &AuthCredential{Provider: "openai"}); err != nil {
			t.Fatal(err)
		}
		raw := openRawAuthDatabase(t)
		if _, err := raw.Exec(`CREATE TABLE rogue_auth_state (secret TEXT)`); err != nil {
			t.Fatal(err)
		}
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadStore(); !errors.Is(err, sqlitestore.ErrInvalidSchema) {
			t.Fatalf("LoadStore() error = %v, want ErrInvalidSchema", err)
		}
	})

	t.Run("corrupt", func(t *testing.T) {
		setTestAuthHome(t)
		if err := os.MkdirAll(filepath.Dir(authDatabasePath()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(authDatabasePath(), []byte("not a SQLite database"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadStore(); err == nil {
			t.Fatal("LoadStore() accepted corrupt database")
		}
	})
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestAuthSQLiteLegacyImportAuditArchiveAndIdempotence(t *testing.T) {
	setTestAuthHome(t)
	secretCanary := "legacy-secret-canary"
	expiresAt := time.Date(2026, time.August, 31, 10, 0, 0, 0, time.UTC)
	legacy := map[string]any{"credentials": map[string]any{
		"antigravity:work": map[string]any{
			"access_token": secretCanary + "-alias",
			"expires_at":   expiresAt.Format(time.RFC3339Nano),
			"provider":     "antigravity",
			"auth_method":  "oauth",
		},
		"google-antigravity:work": map[string]any{
			"access_token": secretCanary + "-canonical",
			"expires_at":   expiresAt.Format(time.RFC3339Nano),
			"provider":     "google-antigravity",
			"auth_method":  "oauth",
		},
		"openai:valid": map[string]any{
			"access_token": secretCanary + "-openai",
			"provider":     "openai",
			"auth_method":  "token",
		},
		"openai:bad/name": map[string]any{
			"access_token": secretCanary + "-invalid-id",
			"provider":     "openai",
		},
		"anthropic:invalid": "not-an-object",
		"mcp:nil":           nil,
		"mcp:invalid-provider": map[string]any{
			"access_token": secretCanary + "-invalid-provider",
			"provider":     "bad provider",
		},
	}}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := writeLegacyAuthFile(t, data, 0o600)
	legacyInfo, err := os.Stat(legacyPath)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadStore()
	if err != nil {
		t.Fatalf("LoadStore() error = %v", err)
	}
	if len(loaded.Credentials) != 2 {
		t.Fatalf("imported credentials = %#v", loaded.Credentials)
	}
	if got := loaded.Credentials["google-antigravity:work"]; got == nil ||
		got.AccessToken != secretCanary+"-canonical" {
		t.Fatalf("canonical collision winner = %#v", got)
	}
	if got := loaded.Credentials["openai:valid"]; got == nil ||
		got.AccessToken != secretCanary+"-openai" {
		t.Fatalf("openai import = %#v", got)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy source still exists: %v", err)
	}
	archivePath := filepath.Join(
		filepath.Dir(authDatabasePath()),
		"legacy-json",
		legacyAuthArchiveLabel,
		legacyAuthFilename,
	)
	archived, err := os.ReadFile(archivePath)
	if err != nil || !bytes.Equal(archived, data) {
		t.Fatalf("archived auth JSON differs: bytes=%d error=%v", len(archived), err)
	}
	if archiveInfo, statErr := os.Stat(archivePath); statErr != nil ||
		archiveInfo.Mode().Perm() != legacyInfo.Mode().Perm() {
		t.Fatalf("archive mode = %v, %v", archiveInfo, statErr)
	}

	db := openRawAuthDatabase(t)
	defer db.Close()
	var imported, skipped int
	var archiveStatus string
	if err := db.QueryRow(`SELECT imported_count, skipped_count, archive_status
        FROM storage_imports WHERE component = ? AND source_id = ?`,
		authDatabaseComponent,
		legacyAuthSourceID,
	).Scan(&imported, &skipped, &archiveStatus); err != nil {
		t.Fatal(err)
	}
	if imported != 2 || skipped != 5 || archiveStatus != "complete" {
		t.Fatalf("import audit = imported:%d skipped:%d archive:%q", imported, skipped, archiveStatus)
	}
	rows, err := db.Query(`SELECT issue_code, record_digest FROM storage_import_issues
        WHERE component = ? AND source_id = ? ORDER BY sequence`,
		authDatabaseComponent,
		legacyAuthSourceID,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var auditProjection strings.Builder
	issueCount := 0
	for rows.Next() {
		var code string
		var digest []byte
		if err := rows.Scan(&code, &digest); err != nil {
			t.Fatal(err)
		}
		if len(digest) != sha256.Size {
			t.Fatalf("issue digest length = %d", len(digest))
		}
		auditProjection.WriteString(code)
		issueCount++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if issueCount != 5 || strings.Contains(auditProjection.String(), secretCanary) {
		t.Fatalf("unsafe or incomplete issue audit = count:%d projection:%q", issueCount, auditProjection.String())
	}

	second, err := LoadStore()
	if err != nil || len(second.Credentials) != 2 {
		t.Fatalf("second LoadStore() = (%#v, %v)", second, err)
	}
	var importRecords int
	if err := db.QueryRow(`SELECT COUNT(*) FROM storage_imports
        WHERE component = ? AND source_id = ?`, authDatabaseComponent, legacyAuthSourceID).Scan(&importRecords); err != nil {
		t.Fatal(err)
	}
	if importRecords != 1 {
		t.Fatalf("import records after reopen = %d, want 1", importRecords)
	}

	if err := DeleteAllCredentials(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(authDatabasePath()); err != nil {
		t.Fatalf("DeleteAllCredentials removed database: %v", err)
	}
	afterDelete, err := LoadStore()
	if err != nil || len(afterDelete.Credentials) != 0 {
		t.Fatalf("LoadStore() after DeleteAll = (%#v, %v)", afterDelete, err)
	}
}

func TestAuthSQLiteLegacyDuplicateKeysKeepFirstValidRecord(t *testing.T) {
	setTestAuthHome(t)
	data := []byte(`{
        "credentials": {
            "openai:work": {
                "access_token": "first-token",
                "provider": "openai",
                "auth_method": "oauth"
            },
            "openai:work": {
                "access_token": "later-token",
                "provider": "openai",
                "auth_method": "oauth"
            },
            "anthropic:work": "invalid-first-record",
            "anthropic:work": {
                "access_token": "first-valid-token",
                "provider": "anthropic",
                "auth_method": "token"
            }
        }
    }`)
	writeLegacyAuthFile(t, data, 0o600)
	store, err := LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Credentials["openai:work"]; got == nil || got.AccessToken != "first-token" {
		t.Fatalf("duplicate exact-key winner = %#v, want first-token", got)
	}
	if got := store.Credentials["anthropic:work"]; got == nil || got.AccessToken != "first-valid-token" {
		t.Fatalf("duplicate invalid-first winner = %#v, want first-valid-token", got)
	}
	db := openRawAuthDatabase(t)
	defer db.Close()
	var imported, skipped, conflicts, invalid int
	if err := db.QueryRow(`SELECT imported_count, skipped_count FROM storage_imports
        WHERE component = ? AND source_id = ?`, authDatabaseComponent, legacyAuthSourceID).Scan(
		&imported,
		&skipped,
	); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT
        SUM(CASE WHEN issue_code = 'identity-conflict' THEN 1 ELSE 0 END),
        SUM(CASE WHEN issue_code = 'invalid-credential' THEN 1 ELSE 0 END)
        FROM storage_import_issues WHERE component = ? AND source_id = ?`,
		authDatabaseComponent,
		legacyAuthSourceID,
	).Scan(&conflicts, &invalid); err != nil {
		t.Fatal(err)
	}
	if imported != 2 || skipped != 2 || conflicts != 1 || invalid != 1 {
		t.Fatalf(
			"duplicate import audit = imported:%d skipped:%d conflicts:%d invalid:%d",
			imported,
			skipped,
			conflicts,
			invalid,
		)
	}
}

func TestLegacyAuthTokenDecoderBoundaries(t *testing.T) {
	valid := []struct {
		name  string
		data  string
		count int
	}{
		{name: "null root", data: `null`},
		{name: "empty root", data: `{}`},
		{name: "unknown field and null credentials", data: `{"unknown":[1],"credentials":null}`},
		{
			name: "duplicate credentials fields",
			data: `{
                "credentials":{"openai":{"provider":"openai"}},
                "credentials":{"anthropic":{"provider":"anthropic"}}
            }`,
			count: 2,
		},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			credentials, err := decodeLegacyAuthCredentials([]byte(test.data))
			if err != nil || len(credentials) != test.count {
				t.Fatalf("decodeLegacyAuthCredentials() = (%#v, %v), want %d entries", credentials, err, test.count)
			}
		})
	}

	for _, test := range []struct {
		name string
		data string
	}{
		{name: "empty", data: ``},
		{name: "nonobject", data: `[]`},
		{name: "malformed root", data: `{`},
		{name: "malformed field", data: `{"credentials":`},
		{name: "nonobject credentials", data: `{"credentials":[]}`},
		{name: "trailing value", data: `{} {}`},
		{name: "malformed trailing value", data: `{} x`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeLegacyAuthCredentials([]byte(test.data)); err == nil {
				t.Fatal("decodeLegacyAuthCredentials() error = nil")
			}
		})
	}

	for _, test := range []struct {
		name    string
		data    string
		wantErr bool
	}{
		{name: "null", data: `null`},
		{name: "empty object", data: `{}`},
		{name: "nonobject", data: `[]`, wantErr: true},
		{name: "malformed", data: `{`, wantErr: true},
		{name: "malformed value", data: `{"openai":`, wantErr: true},
		{name: "trailing", data: `{} {}`, wantErr: true},
	} {
		t.Run("credential object "+test.name, func(t *testing.T) {
			_, err := decodeLegacyCredentialObject(json.RawMessage(test.data))
			if (err != nil) != test.wantErr {
				t.Fatalf("decodeLegacyCredentialObject() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestAuthSQLiteMalformedLegacyAggregateFailsWithoutCommitOrArchive(t *testing.T) {
	setTestAuthHome(t)
	data := []byte(`{"credentials":{"openai":{"access_token":"private-canary"}}`)
	legacyPath := writeLegacyAuthFile(t, data, 0o600)
	if _, err := LoadStore(); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("LoadStore() malformed aggregate error = %v", err)
	}
	db := openRawAuthDatabase(t)
	defer db.Close()
	var domainTables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
        WHERE type = 'table' AND name IN ('auth_credentials', 'storage_imports')`).Scan(&domainTables); err != nil {
		t.Fatal(err)
	}
	if domainTables != 0 {
		t.Fatalf("malformed aggregate committed %d schema tables", domainTables)
	}
	if archived, err := os.ReadFile(legacyPath); err != nil || !bytes.Equal(archived, data) {
		t.Fatalf("malformed source changed = %q, %v", archived, err)
	}
	archivePath := filepath.Join(
		filepath.Dir(authDatabasePath()),
		"legacy-json",
		legacyAuthArchiveLabel,
		legacyAuthFilename,
	)
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("malformed source was archived: %v", err)
	}
}

func TestAuthSQLiteRetriesPendingArchiveWithoutReimport(t *testing.T) {
	setTestAuthHome(t)
	data := []byte(`{"credentials":{"openai":{"access_token":"archived-token","provider":"openai"}}}`)
	preparePendingAuthArchive(t, data, sha256.Sum256(data))
	store, err := LoadStore()
	if err != nil {
		t.Fatalf("LoadStore() pending archive retry error = %v", err)
	}
	if len(store.Credentials) != 0 {
		t.Fatalf("pending archive source was reimported: %#v", store.Credentials)
	}
	if _, err := os.Stat(legacyAuthFilePath()); !os.IsNotExist(err) {
		t.Fatalf("pending legacy source still exists: %v", err)
	}
	archivePath := filepath.Join(
		filepath.Dir(authDatabasePath()),
		"legacy-json",
		legacyAuthArchiveLabel,
		legacyAuthFilename,
	)
	if archived, err := os.ReadFile(archivePath); err != nil || !bytes.Equal(archived, data) {
		t.Fatalf("retried archive = %q, %v", archived, err)
	}
	db := openRawAuthDatabase(t)
	defer db.Close()
	var status string
	if err := db.QueryRow(`SELECT archive_status FROM storage_imports
        WHERE component = ? AND source_id = ?`, authDatabaseComponent, legacyAuthSourceID).Scan(
		&status,
	); err != nil {
		t.Fatal(err)
	}
	if status != "complete" {
		t.Fatalf("retried archive status = %q", status)
	}
}

func TestAuthSQLiteRefusesChangedPendingArchiveSource(t *testing.T) {
	setTestAuthHome(t)
	committed := []byte(`{"credentials":{}}`)
	changed := []byte(`{"credentials":{"openai":{"access_token":"changed-secret","provider":"openai"}}}`)
	preparePendingAuthArchive(t, changed, sha256.Sum256(committed))
	if _, err := LoadStore(); err == nil || !strings.Contains(err.Error(), "changed after import") {
		t.Fatalf("LoadStore() changed-source error = %v", err)
	}
	if source, err := os.ReadFile(legacyAuthFilePath()); err != nil || !bytes.Equal(source, changed) {
		t.Fatalf("changed source was removed or modified = %q, %v", source, err)
	}
	archivePath := filepath.Join(
		filepath.Dir(authDatabasePath()),
		"legacy-json",
		legacyAuthArchiveLabel,
		legacyAuthFilename,
	)
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("changed source was archived: %v", err)
	}
}

func TestAuthSQLiteRejectsUnsafeAndOversizedLegacySources(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Run("unsafe mode", func(t *testing.T) {
			setTestAuthHome(t)
			path := writeLegacyAuthFile(t, []byte(`{"credentials":{}}`), 0o600)
			if err := os.Chmod(path, 0o622); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadStore(); err == nil || !strings.Contains(err.Error(), "unsafe") {
				t.Fatalf("LoadStore() unsafe-mode error = %v", err)
			}
		})
	}

	t.Run("symlink", func(t *testing.T) {
		setTestAuthHome(t)
		home := filepath.Dir(authDatabasePath())
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(target, []byte(`{"credentials":{}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, legacyAuthFilePath()); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := LoadStore(); err == nil ||
			(!strings.Contains(err.Error(), "symlink") && !strings.Contains(err.Error(), "unsafe")) {
			t.Fatalf("LoadStore() symlink error = %v", err)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		setTestAuthHome(t)
		home := filepath.Dir(authDatabasePath())
		if err := os.MkdirAll(home, 0o700); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(legacyAuthFilePath(), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(legacyAuthMaxBytes + 1); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadStore(); err == nil || !strings.Contains(err.Error(), "size limit") {
			t.Fatalf("LoadStore() oversized error = %v", err)
		}
	})
}

func TestAuthSQLiteUpdateCredentialSerializesAcrossProcesses(t *testing.T) {
	if os.Getenv("PICOCLAW_AUTH_SQLITE_UPDATE_HELPER") == "1" {
		_, err := UpdateCredential("openai:shared", func(current *AuthCredential) (*AuthCredential, error) {
			if current == nil {
				return nil, errors.New("shared credential is missing")
			}
			count, parseErr := strconv.Atoi(current.AccountID)
			if parseErr != nil {
				return nil, parseErr
			}
			current.AccountID = strconv.Itoa(count + 1)
			return current, nil
		})
		if err != nil {
			t.Fatalf("helper UpdateCredential() error = %v", err)
		}
		return
	}

	setTestAuthHome(t)
	if err := SetCredential("openai:shared", &AuthCredential{
		AccessToken: "token",
		Provider:    "openai",
		AuthMethod:  "oauth",
		AccountID:   "0",
	}); err != nil {
		t.Fatal(err)
	}
	const processCount = 2
	commands := make([]*exec.Cmd, 0, processCount)
	outputs := make([]bytes.Buffer, processCount)
	for index := range processCount {
		command := exec.Command(
			os.Args[0],
			"-test.run=^TestAuthSQLiteUpdateCredentialSerializesAcrossProcesses$",
		)
		command.Env = append(os.Environ(), "PICOCLAW_AUTH_SQLITE_UPDATE_HELPER=1")
		command.Stdout = &outputs[index]
		command.Stderr = &outputs[index]
		if err := command.Start(); err != nil {
			t.Fatalf("start helper %d: %v", index, err)
		}
		commands = append(commands, command)
	}
	for index, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("helper %d error = %v\n%s", index, err, outputs[index].String())
		}
	}
	credential, err := GetCredential("openai:shared")
	if err != nil {
		t.Fatal(err)
	}
	if credential == nil || credential.AccountID != strconv.Itoa(processCount) {
		t.Fatalf("serialized credential = %#v, want account count %d", credential, processCount)
	}
	if got := authCredentialVersion(t, "openai:shared"); got != processCount+1 {
		t.Fatalf("serialized row version = %d, want %d", got, processCount+1)
	}
}

func authCredentialVersion(t *testing.T, credentialID string) int64 {
	t.Helper()
	db := openRawAuthDatabase(t)
	defer db.Close()
	var version int64
	if err := db.QueryRow(
		`SELECT version FROM auth_credentials WHERE credential_id = ?`,
		credentialID,
	).Scan(&version); err != nil {
		t.Fatalf("read credential version: %v", err)
	}
	return version
}

func openRawAuthDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", authDatabasePath())
	if err != nil {
		t.Fatalf("open raw auth database: %v", err)
	}
	return db
}

func writeLegacyAuthFile(t *testing.T, data []byte, mode os.FileMode) string {
	t.Helper()
	path := legacyAuthFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func preparePendingAuthArchive(t *testing.T, sourceData []byte, committedDigest [sha256.Size]byte) {
	t.Helper()
	options := authStoreOptions(filepath.Dir(authDatabasePath()))
	options.Legacy = nil
	db, err := sqlitestore.Open(t.Context(), authDatabasePath(), options)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	writeLegacyAuthFile(t, sourceData, 0o600)
	raw := openRawAuthDatabase(t)
	defer raw.Close()
	if _, err := raw.Exec(`INSERT INTO storage_imports (
        component, source_id, source_relative, source_digest, source_size, source_limit,
        source_mode, imported_count, skipped_count, archive_status, imported_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0, 'pending', ?)`,
		authDatabaseComponent,
		legacyAuthSourceID,
		legacyAuthFilename,
		committedDigest[:],
		len(sourceData),
		legacyAuthMaxBytes,
		0o600,
		time.Now().UTC().UnixNano(),
	); err != nil {
		t.Fatal(err)
	}
}

func TestAuthSQLitePathsUseConfiguredHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	if got, want := authDatabasePath(), filepath.Join(home, authDatabaseFilename); got != want {
		t.Fatalf("authDatabasePath() = %q, want %q", got, want)
	}
	if got, want := legacyAuthFilePath(), filepath.Join(home, legacyAuthFilename); got != want {
		t.Fatalf("legacyAuthFilePath() = %q, want %q", got, want)
	}
	resolved, err := resolvedAuthDatabasePath()
	if err != nil {
		t.Fatal(err)
	}
	if resolved != authDatabasePath() {
		t.Fatalf("resolved auth path = %q, want %q", resolved, authDatabasePath())
	}
	lockDirectory, err := resolvedAuthLockDirectoryPath()
	if err != nil {
		t.Fatal(err)
	}
	storeLock, err := authStoreLockPath()
	if err != nil {
		t.Fatal(err)
	}
	if storeLock != filepath.Join(lockDirectory, "store") {
		t.Fatalf("auth store lock path = %q, want child of %q", storeLock, lockDirectory)
	}
}

func TestAuthSQLiteLockDirectoryAndFilesArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are not enforced on Windows")
	}
	setTestAuthHome(t)
	if err := SetCredential("openai", &AuthCredential{Provider: "openai"}); err != nil {
		t.Fatal(err)
	}
	unlockedRefresh, err := lockCredentialRefresh("openai")
	if err != nil {
		t.Fatal(err)
	}
	unlockedRefresh()
	lockDirectory, err := resolvedAuthLockDirectoryPath()
	if err != nil {
		t.Fatal(err)
	}
	if info, statErr := os.Stat(lockDirectory); statErr != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("auth lock directory mode = %v, %v", info, statErr)
	}
	storeLock, err := authStoreLockPath()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{storeLock + ".lock"} {
		if info, statErr := os.Stat(path); statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("auth lock %s mode = %v, %v", filepath.Base(path), info, statErr)
		}
	}
	flatMatches, err := filepath.Glob(authDatabasePath() + ".refresh.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(flatMatches) != 0 {
		t.Fatalf("flat auth refresh lock sidecars = %v", flatMatches)
	}
}

func TestAuthSQLiteRejectsSymlinkedLockEndpoint(t *testing.T) {
	setTestAuthHome(t)
	lockPath, err := authStoreLockPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-lock")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, lockPath+".lock"); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := SetCredential("openai", &AuthCredential{Provider: "openai"}); err == nil ||
		!strings.Contains(err.Error(), "regular file") {
		t.Fatalf("SetCredential() symlinked-lock error = %v", err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "keep" {
		t.Fatalf("outside lock target changed = %q, %v", data, err)
	}
}

func TestAuthSQLiteRejectsUnsafeHomeBeforeCreatingLockArtifacts(t *testing.T) {
	t.Run("symlinked home", func(t *testing.T) {
		root := t.TempDir()
		outside := filepath.Join(root, "outside")
		if err := os.Mkdir(outside, 0o700); err != nil {
			t.Fatalf("Mkdir(outside): %v", err)
		}
		if err := os.Chmod(outside, 0o700); err != nil {
			t.Fatalf("Chmod(outside): %v", err)
		}
		linkedHome := filepath.Join(root, "linked-home")
		if err := os.Symlink(outside, linkedHome); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Setenv(config.EnvHome, linkedHome)
		if err := SetCredential("openai", &AuthCredential{Provider: "openai"}); err == nil ||
			!strings.Contains(err.Error(), "real directory") {
			t.Fatalf("SetCredential() symlinked-home error = %v", err)
		}
		for _, unexpected := range []string{
			filepath.Join(outside, authDatabaseFilename),
			filepath.Join(outside, authDatabaseFilename+".locks"),
		} {
			if _, err := os.Lstat(unexpected); !os.IsNotExist(err) {
				t.Fatalf("unsafe home created %s: %v", unexpected, err)
			}
		}
	})

	t.Run("home is a file", func(t *testing.T) {
		root := t.TempDir()
		homeFile := filepath.Join(root, "home-file")
		if err := os.WriteFile(homeFile, []byte("keep"), 0o600); err != nil {
			t.Fatalf("WriteFile(home): %v", err)
		}
		if err := os.Chmod(homeFile, 0o600); err != nil {
			t.Fatalf("Chmod(home): %v", err)
		}
		t.Setenv(config.EnvHome, homeFile)
		if err := SetCredential("openai", &AuthCredential{Provider: "openai"}); err == nil {
			t.Fatal("SetCredential() accepted a non-directory home")
		}
		if data, err := os.ReadFile(homeFile); err != nil || string(data) != "keep" {
			t.Fatalf("unsafe home file changed = %q, %v", data, err)
		}
	})
}

func TestAuthSQLiteCredentialCountAndUTF8ByteBounds(t *testing.T) {
	t.Run("text columns", func(t *testing.T) {
		setTestAuthHome(t)
		base := AuthCredential{Provider: "openai"}
		oversized := func(maximum int) string {
			return "secret-canary-" + strings.Repeat("x", maximum)
		}
		tests := []struct {
			name   string
			mutate func(*AuthCredential)
		}{
			{name: "provider", mutate: func(value *AuthCredential) {
				value.Provider = strings.Repeat("a", maximumCredentialProvider+1)
			}},
			{name: "access token", mutate: func(value *AuthCredential) {
				value.AccessToken = oversized(maximumAccessTokenBytes)
			}},
			{name: "refresh token", mutate: func(value *AuthCredential) {
				value.RefreshToken = oversized(maximumRefreshTokenBytes)
			}},
			{name: "token type", mutate: func(value *AuthCredential) {
				value.TokenType = oversized(maximumTokenTypeBytes)
			}},
			{name: "OAuth token URL", mutate: func(value *AuthCredential) {
				value.OAuthTokenURL = oversized(maximumOAuthTokenURLBytes)
			}},
			{name: "OAuth client ID", mutate: func(value *AuthCredential) {
				value.OAuthClientID = oversized(maximumOAuthClientIDBytes)
			}},
			{name: "OAuth client secret", mutate: func(value *AuthCredential) {
				value.OAuthClientSecret = oversized(maximumOAuthClientSecret)
			}},
			{name: "OAuth auth style", mutate: func(value *AuthCredential) {
				value.OAuthAuthStyle = oversized(maximumOAuthAuthStyleBytes)
			}},
			{name: "account ID", mutate: func(value *AuthCredential) {
				value.AccountID = oversized(maximumAccountIDBytes)
			}},
			{name: "auth method", mutate: func(value *AuthCredential) {
				value.AuthMethod = oversized(maximumAuthMethodBytes)
			}},
			{name: "email", mutate: func(value *AuthCredential) {
				value.Email = oversized(maximumEmailBytes)
			}},
			{name: "project ID", mutate: func(value *AuthCredential) {
				value.ProjectID = oversized(maximumProjectIDBytes)
			}},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				credential := base
				test.mutate(&credential)
				err := SetCredential("openai", &credential)
				if err == nil || !strings.Contains(err.Error(), "limit") ||
					strings.Contains(err.Error(), "secret-canary") {
					t.Fatalf("SetCredential() bounded error = %v", err)
				}
			})
		}
		invalidUTF8 := base
		invalidUTF8.AccessToken = string([]byte{0xff})
		if err := SetCredential("openai", &invalidUTF8); err == nil ||
			!strings.Contains(err.Error(), "UTF-8") {
			t.Fatalf("SetCredential() invalid UTF-8 error = %v", err)
		}
		if err := SetCredential(
			strings.Repeat("a", maximumCredentialIDBytes+1),
			&base,
		); err == nil || !strings.Contains(err.Error(), "invalid credential id") {
			t.Fatalf("SetCredential() oversized identity error = %v", err)
		}
	})

	t.Run("schema and transactional count", func(t *testing.T) {
		setTestAuthHome(t)
		db, openErr := openAuthDatabase(t.Context())
		if openErr != nil {
			t.Fatalf("openAuthDatabase() error = %v", openErr)
		}
		defer db.Close()
		if _, err := db.ExecContext(t.Context(), `INSERT INTO auth_credentials (
            credential_id, provider, access_token, refresh_token, token_type,
            oauth_token_url, oauth_client_id, oauth_client_secret,
            oauth_auth_style, account_id, auth_method, email, project_id
        ) VALUES ('openai:oversized', 'openai', ?, '', '', '', '', '', '', '', '', '', '')`,
			strings.Repeat("x", maximumAccessTokenBytes+1),
		); err == nil {
			t.Fatal("schema accepted an oversized access token")
		}
		conn, connErr := db.Conn(t.Context())
		if connErr != nil {
			t.Fatalf("db.Conn() error = %v", connErr)
		}
		for _, credentialID := range []string{"openai:old-a", "openai:old-b", "openai:retained"} {
			if _, err := insertCredential(t.Context(), conn, credentialID, &AuthCredential{
				Provider:    "openai",
				AccessToken: credentialID,
			}); err != nil {
				t.Fatalf("insert bounded fixture %s: %v", credentialID, err)
			}
		}
		if err := ensureCredentialCapacityWithin(
			t.Context(),
			conn,
			"openai:new",
			3,
		); err == nil || !strings.Contains(err.Error(), "credential limit") {
			t.Fatalf("ensureCredentialCapacityWithin(new) error = %v", err)
		}
		if err := ensureCredentialCapacityWithin(
			t.Context(),
			conn,
			"openai:retained",
			3,
		); err != nil {
			t.Fatalf("ensureCredentialCapacityWithin(existing) error = %v", err)
		}
		if err := conn.Close(); err != nil {
			t.Fatalf("conn.Close() error = %v", err)
		}
		replacement := &AuthStore{Credentials: map[string]*AuthCredential{
			"openai:retained": {Provider: "openai", AccessToken: "updated"},
			"openai:new-a":    {Provider: "openai", AccessToken: "new-a"},
			"openai:new-b":    {Provider: "openai", AccessToken: "new-b"},
		}}
		if err := sqlitestore.Immediate(t.Context(), db, func(transaction *sql.Conn) error {
			return replaceAuthStore(t.Context(), transaction, replacement, 3)
		}); err != nil {
			t.Fatalf("replaceAuthStore(at limit) error = %v", err)
		}
		rows, queryErr := db.QueryContext(t.Context(), `SELECT credential_id
			FROM auth_credentials ORDER BY credential_id`)
		if queryErr != nil {
			t.Fatalf("list replaced credentials: %v", queryErr)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var credentialID string
			if err := rows.Scan(&credentialID); err != nil {
				t.Fatalf("scan replaced credential: %v", err)
			}
			got = append(got, credentialID)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate replaced credentials: %v", err)
		}
		want := []string{"openai:new-a", "openai:new-b", "openai:retained"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("replacement identities = %v, want %v", got, want)
		}
	})

	t.Run("legacy decoder count", func(t *testing.T) {
		credentials := make(map[string]any, maximumAuthCredentials+1)
		for index := 0; index <= maximumAuthCredentials; index++ {
			credentials[fmt.Sprintf("openai:item-%05d", index)] = map[string]any{
				"provider": "openai",
			}
		}
		data, err := json.Marshal(map[string]any{"credentials": credentials})
		if err != nil {
			t.Fatalf("marshal legacy count fixture: %v", err)
		}
		if _, err := decodeLegacyAuthCredentials(data); err == nil ||
			!strings.Contains(err.Error(), "count exceeds") {
			t.Fatalf("decodeLegacyAuthCredentials() count error = %v", err)
		}
	})
}

func TestCredentialExpiryRejectsUnsupportedRange(t *testing.T) {
	setTestAuthHome(t)
	supported := time.Date(2500, time.January, 1, 0, 0, 0, 987654321, time.UTC)
	if err := SetCredential("openai", &AuthCredential{Provider: "openai", ExpiresAt: supported}); err != nil {
		t.Fatalf("SetCredential(supported timestamp) error = %v", err)
	}
	if credential, err := GetCredential("openai"); err != nil ||
		credential == nil || !credential.ExpiresAt.Equal(supported) {
		t.Fatalf("GetCredential(supported timestamp) = (%#v, %v)", credential, err)
	}
	tooFar := time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	err := SetCredential("openai", &AuthCredential{Provider: "openai", ExpiresAt: tooFar})
	if err == nil || !strings.Contains(err.Error(), "timestamp range") {
		t.Fatalf("SetCredential() timestamp error = %v", err)
	}
	if _, _, err := credentialExpiryValues(time.Time{}); err != nil {
		t.Fatalf("zero expiry error = %v", err)
	}
}

//nolint:govet // Independent branch assertions intentionally reuse short declarations.
func TestAuthSQLiteRemainingCoverageBoundaries(t *testing.T) {
	t.Run("normalization", func(t *testing.T) {
		if got := canonicalProvider(" CoPiLoT "); got != providerGitHubCopilot {
			t.Fatalf("canonicalProvider(copilot) = %q", got)
		}
		for _, test := range []struct {
			provider, credentialID string
		}{
			{provider: "", credentialID: "work"},
			{provider: "openai", credentialID: "openai:"},
			{provider: "openai", credentialID: "openai:bad/name"},
			{provider: "openai", credentialID: "bad/name"},
		} {
			if _, err := NormalizeCredentialID(test.provider, test.credentialID); err == nil {
				t.Fatalf("NormalizeCredentialID(%q, %q) accepted invalid input", test.provider, test.credentialID)
			}
		}
		later := time.Now().Add(time.Hour)
		if shouldPreferCredential(
			&AuthCredential{ExpiresAt: time.Now()},
			true,
			&AuthCredential{ExpiresAt: later},
			false,
		) {
			t.Fatal("shouldPreferCredential preferred an earlier expiry")
		}
		normalizeStore(nil)
		empty := &AuthStore{}
		normalizeStore(empty)
		if empty.Credentials == nil {
			t.Fatal("normalizeStore left a nil credential map")
		}
		if _, err := UpdateCredential("openai", nil); err == nil {
			t.Fatal("UpdateCredential accepted a nil callback")
		}
	})

	t.Run("replace and corrupt row failures", func(t *testing.T) {
		setTestAuthHome(t)
		if err := SetCredential("openai:old", &AuthCredential{Provider: "openai"}); err != nil {
			t.Fatal(err)
		}
		db := openRawAuthDatabase(t)
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := replaceAuthStore(t.Context(), conn, nil, 1); err == nil {
			t.Fatal("replaceAuthStore accepted nil state")
		}
		closedDB := openRawAuthDatabase(t)
		closedConn, err := closedDB.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := closedConn.Close(); err != nil {
			t.Fatal(err)
		}
		if err := replaceAuthStore(
			t.Context(),
			closedConn,
			&AuthStore{Credentials: map[string]*AuthCredential{}},
			1,
		); err == nil {
			t.Fatal("replaceAuthStore accepted a closed connection")
		}
		_ = closedDB.Close()

		if _, err := conn.ExecContext(t.Context(), `CREATE TRIGGER fail_auth_delete
			BEFORE DELETE ON auth_credentials BEGIN SELECT RAISE(ABORT, 'delete failed'); END`); err != nil {
			t.Fatal(err)
		}
		if err := replaceAuthStore(
			t.Context(),
			conn,
			&AuthStore{Credentials: map[string]*AuthCredential{
				"openai:new": {Provider: "openai"},
			}},
			2,
		); err == nil {
			t.Fatal("replaceAuthStore ignored a delete failure")
		}
		if _, err := conn.ExecContext(t.Context(), `DROP TRIGGER fail_auth_delete`); err != nil {
			t.Fatal(err)
		}
		if err := replaceAuthStore(
			t.Context(),
			conn,
			&AuthStore{Credentials: map[string]*AuthCredential{"openai:old": nil}},
			2,
		); err == nil {
			t.Fatal("replaceAuthStore accepted an invalid retained credential")
		}
		if _, err := conn.ExecContext(t.Context(), `PRAGMA ignore_check_constraints = ON`); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(t.Context(), `UPDATE auth_credentials
			SET expires_at_unix_seconds = 1, expires_at_nanosecond = NULL
			WHERE credential_id = 'openai:old'`); err != nil {
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := GetCredential("openai:old"); err == nil {
			t.Fatal("GetCredential accepted inconsistent expiry columns")
		}
		if _, _, err := persistCredentialIfCurrentDetailed(
			"openai:old",
			&AuthCredential{Provider: "openai"},
			&AuthCredential{Provider: "openai", AccessToken: "replacement"},
		); err == nil {
			t.Fatal("credential CAS accepted inconsistent stored columns")
		}
	})

	t.Run("version overflow update failures", func(t *testing.T) {
		setTestAuthHome(t)
		source := &AuthCredential{Provider: "openai", AccessToken: "source"}
		if err := SetCredential("openai", source); err != nil {
			t.Fatal(err)
		}
		db := openRawAuthDatabase(t)
		if _, err := db.ExecContext(t.Context(), `UPDATE auth_credentials
			SET version = 9223372036854775807 WHERE credential_id = 'openai'`); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := persistCredentialIfCurrentDetailed(
			"openai",
			source,
			&AuthCredential{Provider: "openai", AccessToken: "replacement"},
		); err == nil {
			t.Fatal("credential CAS accepted a version overflow")
		}
		if _, err := UpdateCredential("openai", func(*AuthCredential) (*AuthCredential, error) {
			return &AuthCredential{Provider: "openai", AccessToken: "updated"}, nil
		}); err == nil {
			t.Fatal("UpdateCredential accepted a version overflow")
		}
	})

	t.Run("lock and helper failures", func(t *testing.T) {
		setTestAuthHome(t)
		lockDirectory, err := resolvedAuthLockDirectoryPath()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lockDirectory, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		unlockRefresh, err := lockCredentialRefresh("openai")
		if err != nil {
			t.Fatalf("process-local refresh lock consulted filesystem: %v", err)
		}
		unlockRefresh()
		if _, err := openAuthLockFile(
			filepath.Join(t.TempDir(), strings.Repeat("x", 5000)),
		); err == nil {
			t.Fatal("openAuthLockFile accepted an overlong path")
		}
		if err := validateCredentialTextFields(nil); err == nil {
			t.Fatal("validateCredentialTextFields accepted nil")
		}
		valid := &AuthCredential{Provider: "openai"}
		for _, test := range []struct {
			id   string
			cred *AuthCredential
		}{
			{id: "bad/name", cred: valid},
			{id: "openai", cred: nil},
			{id: "openai", cred: &AuthCredential{Provider: string([]byte{0xff})}},
		} {
			if _, err := credentialArguments(test.id, test.cred); err == nil {
				t.Fatalf("credentialArguments(%q) accepted invalid input", test.id)
			}
		}
		invalidText := &AuthCredential{
			Provider:    "openai",
			AccessToken: string([]byte{0xff}),
		}
		if _, err := credentialArguments("openai", invalidText); err == nil {
			t.Fatal("credentialArguments accepted invalid UTF-8 text")
		}
		tooMany := &AuthStore{Credentials: make(map[string]*AuthCredential, maximumAuthCredentials+1)}
		for index := 0; index <= maximumAuthCredentials; index++ {
			tooMany.Credentials[fmt.Sprintf("openai:overflow-%05d", index)] = valid
		}
		if _, err := normalizedAuthStore(tooMany); err == nil {
			t.Fatal("normalizedAuthStore accepted too many credentials")
		}
	})

	t.Run("direct SQL helper failures", func(t *testing.T) {
		setTestAuthHome(t)
		if _, err := LoadStore(); err != nil {
			t.Fatal(err)
		}
		db := openRawAuthDatabase(t)
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		valid := &AuthCredential{Provider: "openai"}
		if _, err := insertCredential(t.Context(), conn, "bad/name", valid); err == nil {
			t.Fatal("insertCredential accepted an invalid identity")
		}
		if err := upsertCredentialUnchecked(t.Context(), conn, "bad/name", valid); err == nil {
			t.Fatal("upsertCredentialUnchecked accepted an invalid identity")
		}
		if err := ensureCredentialCapacityWithin(t.Context(), conn, "openai", 0); err == nil {
			t.Fatal("ensureCredentialCapacityWithin accepted a zero limit")
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		if err := ensureCredentialCapacityWithin(t.Context(), conn, "openai", 1); err == nil {
			t.Fatal("ensureCredentialCapacityWithin accepted a closed connection")
		}
		_ = db.Close()
	})
}

func TestLegacyImporterLeavesExistingSQLiteCredentialAuthoritative(t *testing.T) {
	setTestAuthHome(t)
	if err := SetCredential("openai", &AuthCredential{
		AccessToken: "sqlite-token",
		Provider:    "openai",
	}); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"credentials":{"openai":{"access_token":"legacy-token","provider":"openai"}}}`)
	writeLegacyAuthFile(t, data, 0o600)
	loaded, err := LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Credentials["openai"]; got == nil || got.AccessToken != "sqlite-token" {
		t.Fatalf("authoritative credential = %#v", got)
	}
	db := openRawAuthDatabase(t)
	defer db.Close()
	var imported, skipped, authoritativeIssues int
	if err := db.QueryRow(`SELECT imported_count, skipped_count FROM storage_imports
        WHERE component = ? AND source_id = ?`, authDatabaseComponent, legacyAuthSourceID).Scan(
		&imported,
		&skipped,
	); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM storage_import_issues
        WHERE component = ? AND source_id = ? AND issue_code = 'late-source'`,
		authDatabaseComponent,
		legacyAuthSourceID,
	).Scan(&authoritativeIssues); err != nil {
		t.Fatal(err)
	}
	if imported != 0 || skipped != 1 || authoritativeIssues != 1 {
		t.Fatalf(
			"authoritative import audit = imported:%d skipped:%d issues:%d",
			imported,
			skipped,
			authoritativeIssues,
		)
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestAuthSQLiteImmediateTransactionRollback(t *testing.T) {
	setTestAuthHome(t)
	if err := SetCredential("openai", &AuthCredential{AccessToken: "before", Provider: "openai"}); err != nil {
		t.Fatal(err)
	}
	db, err := openAuthDatabase(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	injected := errors.New("injected rollback")
	err = sqlitestore.Immediate(t.Context(), db, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(
			t.Context(),
			`UPDATE auth_credentials SET access_token = 'after', version = version + 1
             WHERE credential_id = 'openai'`,
		); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("Immediate() error = %v", err)
	}
	credential, err := GetCredential("openai")
	if err != nil || credential.AccessToken != "before" {
		t.Fatalf("credential after rollback = (%#v, %v)", credential, err)
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestAuthSQLiteHelperAndMutationErrorBoundaries(t *testing.T) {
	setTestAuthHome(t)
	injected := errors.New("injected credential update failure")
	created, err := UpdateCredential("openai:new", func(current *AuthCredential) (*AuthCredential, error) {
		if current != nil {
			t.Fatalf("new credential current state = %#v", current)
		}
		return &AuthCredential{AccessToken: "created", Provider: "openai"}, nil
	})
	if err != nil || created == nil || created.AccessToken != "created" {
		t.Fatalf("UpdateCredential(create) = (%#v, %v)", created, err)
	}
	if _, err := UpdateCredential("openai:new", func(*AuthCredential) (*AuthCredential, error) {
		return nil, injected
	}); !errors.Is(err, injected) {
		t.Fatalf("UpdateCredential(callback error) = %v", err)
	}
	if _, err := UpdateCredential("openai:new", func(*AuthCredential) (*AuthCredential, error) {
		return nil, nil
	}); err == nil || !strings.Contains(err.Error(), "returned nil") {
		t.Fatalf("UpdateCredential(nil replacement) = %v", err)
	}
	if _, err := UpdateCredential("openai:new", func(*AuthCredential) (*AuthCredential, error) {
		return &AuthCredential{Provider: "bad provider"}, nil
	}); err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("UpdateCredential(invalid provider) = %v", err)
	}
	if err := SaveStore(&AuthStore{Credentials: map[string]*AuthCredential{"openai": nil}}); err == nil {
		t.Fatal("SaveStore(nil credential) error = nil")
	}
	if err := SetCredential("openai:bad/name", &AuthCredential{Provider: "openai"}); err == nil {
		t.Fatal("SetCredential(invalid identity) error = nil")
	}
	if err := SetCredential("openai", nil); err == nil {
		t.Fatal("SetCredential(nil) error = nil")
	}
	if normalized, err := normalizedAuthStore(nil); err != nil || len(normalized.Credentials) != 0 {
		t.Fatalf("normalizedAuthStore(nil) = (%#v, %v)", normalized, err)
	}
	for credentialID, want := range map[string]bool{
		"":              false,
		"openai":        true,
		"openai:work":   true,
		"openai:bad/id": false,
	} {
		if got := validStoredCredentialID(credentialID); got != want {
			t.Fatalf("validStoredCredentialID(%q) = %v, want %v", credentialID, got, want)
		}
	}

	db, err := openAuthDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	current, version, found, err := loadCredentialFromConn(t.Context(), conn, "openai:new")
	if err != nil || !found || current == nil {
		t.Fatalf("loadCredentialFromConn() = (%#v, %d, %v, %v)", current, version, found, err)
	}
	current.AccessToken = "wrong-version"
	if err := updateCredentialVersioned(
		t.Context(), conn, "openai:new", current, version+1,
	); err == nil || !strings.Contains(err.Error(), "version changed") {
		t.Fatalf("updateCredentialVersioned(stale) error = %v", err)
	}

	inconsistent := db.QueryRow(`SELECT
        'openai:inconsistent', 'openai', '', '', '', '', '', '', '', '',
        1, NULL, '', '', '', 1`)
	if _, _, _, err := scanCredential(inconsistent); err == nil ||
		!strings.Contains(err.Error(), "inconsistent") {
		t.Fatalf("scanCredential(inconsistent expiry) error = %v", err)
	}
	if _, err := conn.ExecContext(t.Context(), `PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE auth_credentials
        SET expires_at_unix_seconds = 1, expires_at_nanosecond = NULL
        WHERE credential_id = 'openai:new'`); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAuthStore(t.Context(), db); err == nil || !strings.Contains(err.Error(), "inconsistent") {
		t.Fatalf("loadAuthStore(inconsistent row) error = %v", err)
	}
	if _, err := conn.ExecContext(t.Context(), `UPDATE auth_credentials
        SET expires_at_unix_seconds = NULL, expires_at_nanosecond = NULL
        WHERE credential_id = 'openai:new'`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `PRAGMA ignore_check_constraints = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX rogue_auth_provider
        ON auth_credentials(provider)`); err != nil {
		t.Fatal(err)
	}
	if err := validateAuthSchema(t.Context(), conn); err == nil {
		t.Fatal("validateAuthSchema() accepted rogue unique index")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := loadCredentialFromConn(t.Context(), conn, "openai:new"); err == nil {
		t.Fatal("loadCredentialFromConn(closed) error = nil")
	}
	if _, err := existingCredentialIDs(t.Context(), conn); err == nil {
		t.Fatal("existingCredentialIDs(closed) error = nil")
	}
	valid := &AuthCredential{Provider: "openai"}
	if _, err := insertCredential(t.Context(), conn, "openai:closed", valid); err == nil {
		t.Fatal("insertCredential(closed) error = nil")
	}
	if err := upsertCredential(t.Context(), conn, "openai:closed", valid); err == nil {
		t.Fatal("upsertCredential(closed) error = nil")
	}
	if err := updateCredentialVersioned(t.Context(), conn, "openai:new", valid, version); err == nil {
		t.Fatal("updateCredentialVersioned(closed) error = nil")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAuthStore(t.Context(), db); err == nil {
		t.Fatal("loadAuthStore(closed) error = nil")
	}
	if _, _, _, err := scanCredential(db.QueryRow(`SELECT 1`)); err == nil {
		t.Fatal("scanCredential(closed database) error = nil")
	}
	legacyData := []byte(`{"credentials":{"openai":{"provider":"openai"}}}`)
	if _, err := importLegacyAuthStore(t.Context(), conn, sqlitestore.LegacyInput{
		ID:       legacyAuthSourceID,
		Relative: legacyAuthFilename,
		Data:     legacyData,
		Digest:   sha256.Sum256(legacyData),
		Limit:    legacyAuthMaxBytes,
		Mode:     0o600,
	}); err == nil {
		t.Fatal("importLegacyAuthStore(closed connection) error = nil")
	}

	tooFar := &AuthCredential{
		Provider:  "openai",
		ExpiresAt: time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC),
	}
	if _, err := credentialArguments("openai", tooFar); err == nil {
		t.Fatal("credentialArguments(unsupported timestamp) error = nil")
	}
	if _, err := insertCredential(t.Context(), conn, "openai", tooFar); err == nil {
		t.Fatal("insertCredential(unsupported timestamp) error = nil")
	}
	if err := upsertCredential(t.Context(), conn, "openai", tooFar); err == nil {
		t.Fatal("upsertCredential(unsupported timestamp) error = nil")
	}
	if err := updateCredentialVersioned(t.Context(), conn, "openai", tooFar, 1); err == nil {
		t.Fatal("updateCredentialVersioned(unsupported timestamp) error = nil")
	}
}
