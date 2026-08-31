package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const (
	authDatabaseFilename   = "auth.db"
	legacyAuthFilename     = "auth.json"
	authDatabaseComponent  = "auth"
	legacyAuthSourceID     = "auth-json-v1"
	legacyAuthMaxBytes     = int64(16 << 20)
	legacyAuthArchiveLabel = "auth-v1"

	maximumAuthCredentials     = 10_000
	maximumCredentialIDBytes   = 1 << 10
	maximumCredentialProvider  = 256
	maximumAccessTokenBytes    = 1 << 20
	maximumRefreshTokenBytes   = 1 << 20
	maximumTokenTypeBytes      = 256
	maximumOAuthTokenURLBytes  = 8 << 10
	maximumOAuthClientIDBytes  = 16 << 10
	maximumOAuthClientSecret   = 1 << 20
	maximumOAuthAuthStyleBytes = 256
	maximumAccountIDBytes      = 16 << 10
	maximumAuthMethodBytes     = 256
	maximumEmailBytes          = 8 << 10
	maximumProjectIDBytes      = 16 << 10
)

const authCredentialsSchema = `CREATE TABLE auth_credentials (
    credential_id              TEXT PRIMARY KEY,
    provider                   TEXT NOT NULL,
    access_token               TEXT NOT NULL,
    refresh_token              TEXT NOT NULL,
    token_type                 TEXT NOT NULL,
    oauth_token_url            TEXT NOT NULL,
    oauth_client_id            TEXT NOT NULL,
    oauth_client_secret        TEXT NOT NULL,
    oauth_auth_style           TEXT NOT NULL,
    account_id                 TEXT NOT NULL,
    expires_at_unix_seconds    INTEGER,
    expires_at_nanosecond      INTEGER,
    auth_method                TEXT NOT NULL,
    email                      TEXT NOT NULL,
    project_id                 TEXT NOT NULL,
    version                    INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    CHECK(length(CAST(credential_id AS BLOB)) BETWEEN 1 AND 1024),
    CHECK(credential_id = lower(trim(credential_id))),
    CHECK(credential_id NOT GLOB '*[^-a-z0-9._:]*'),
    CHECK(substr(credential_id, 1, 1) <> ':' AND substr(credential_id, -1, 1) <> ':'),
    CHECK(instr(substr(credential_id, instr(credential_id, ':') + 1), ':') = 0),
    CHECK(length(CAST(provider AS BLOB)) BETWEEN 1 AND 256),
    CHECK(provider = lower(trim(provider))),
    CHECK(provider NOT GLOB '*[^-a-z0-9._]*'),
    CHECK(length(CAST(access_token AS BLOB)) <= 1048576),
    CHECK(length(CAST(refresh_token AS BLOB)) <= 1048576),
    CHECK(length(CAST(token_type AS BLOB)) <= 256),
    CHECK(length(CAST(oauth_token_url AS BLOB)) <= 8192),
    CHECK(length(CAST(oauth_client_id AS BLOB)) <= 16384),
    CHECK(length(CAST(oauth_client_secret AS BLOB)) <= 1048576),
    CHECK(length(CAST(oauth_auth_style AS BLOB)) <= 256),
    CHECK(length(CAST(account_id AS BLOB)) <= 16384),
    CHECK(length(CAST(auth_method AS BLOB)) <= 256),
    CHECK(length(CAST(email AS BLOB)) <= 8192),
    CHECK(length(CAST(project_id AS BLOB)) <= 16384),
    CHECK(
        (expires_at_unix_seconds IS NULL AND expires_at_nanosecond IS NULL)
        OR (
            expires_at_unix_seconds IS NOT NULL
            AND expires_at_unix_seconds BETWEEN -62167219200 AND 253402300799
            AND expires_at_nanosecond IS NOT NULL
            AND expires_at_nanosecond BETWEEN 0 AND 999999999
        )
    )
) STRICT`

const authCredentialsProviderIndexSchema = `CREATE INDEX auth_credentials_provider_idx
    ON auth_credentials(provider, credential_id)`

const selectCredentialSQL = `SELECT
    credential_id, provider, access_token, refresh_token, token_type,
    oauth_token_url, oauth_client_id, oauth_client_secret, oauth_auth_style,
    account_id, expires_at_unix_seconds, expires_at_nanosecond,
    auth_method, email, project_id, version
FROM auth_credentials`

const insertCredentialSQL = `INSERT INTO auth_credentials (
    credential_id, provider, access_token, refresh_token, token_type,
    oauth_token_url, oauth_client_id, oauth_client_secret, oauth_auth_style,
    account_id, expires_at_unix_seconds, expires_at_nanosecond,
    auth_method, email, project_id, version
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`

const upsertCredentialSQL = `INSERT INTO auth_credentials (
    credential_id, provider, access_token, refresh_token, token_type,
    oauth_token_url, oauth_client_id, oauth_client_secret, oauth_auth_style,
    account_id, expires_at_unix_seconds, expires_at_nanosecond,
    auth_method, email, project_id, version
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
ON CONFLICT(credential_id) DO UPDATE SET
    provider = excluded.provider,
    access_token = excluded.access_token,
    refresh_token = excluded.refresh_token,
    token_type = excluded.token_type,
    oauth_token_url = excluded.oauth_token_url,
    oauth_client_id = excluded.oauth_client_id,
    oauth_client_secret = excluded.oauth_client_secret,
    oauth_auth_style = excluded.oauth_auth_style,
    account_id = excluded.account_id,
    expires_at_unix_seconds = excluded.expires_at_unix_seconds,
    expires_at_nanosecond = excluded.expires_at_nanosecond,
    auth_method = excluded.auth_method,
    email = excluded.email,
    project_id = excluded.project_id,
    version = auth_credentials.version + 1`

const updateCredentialSQL = `UPDATE auth_credentials SET
    provider = ?,
    access_token = ?,
    refresh_token = ?,
    token_type = ?,
    oauth_token_url = ?,
    oauth_client_id = ?,
    oauth_client_secret = ?,
    oauth_auth_style = ?,
    account_id = ?,
    expires_at_unix_seconds = ?,
    expires_at_nanosecond = ?,
    auth_method = ?,
    email = ?,
    project_id = ?,
    version = version + 1
WHERE credential_id = ? AND version = ?`

func authDatabasePath() string {
	return filepath.Join(config.GetHome(), authDatabaseFilename)
}

func legacyAuthFilePath() string {
	return filepath.Join(config.GetHome(), legacyAuthFilename)
}

func resolvedAuthDatabasePath() (string, error) {
	return filepath.Abs(authDatabasePath())
}

func resolvedAuthLockDirectoryPath() (string, error) {
	databasePath, err := resolvedAuthDatabasePath()
	if err != nil {
		return "", err
	}
	// Validate the configured database root before creating the sibling lock
	// directory. In particular, a symlinked PICOCLAW_HOME must not cause lock
	// artifacts to be created through that link before SQLite rejects it.
	if err := sqlitestore.EnsurePrivateDir(filepath.Dir(databasePath)); err != nil {
		return "", err
	}
	return databasePath + ".locks", nil
}

func authStoreLockPath() (string, error) {
	lockDirectory, err := resolvedAuthLockDirectoryPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(lockDirectory, "store"), nil
}

func openAuthDatabase(ctx context.Context) (*sql.DB, error) {
	unlock, err := lockAuthDatabaseAccess()
	if err != nil {
		return nil, err
	}
	defer unlock()
	return openAuthDatabaseUnlocked(ctx)
}

func openAuthDatabaseForWrite(ctx context.Context) (*sql.DB, func(), error) {
	unlock, err := lockAuthDatabaseAccess()
	if err != nil {
		return nil, nil, err
	}
	db, err := openAuthDatabaseUnlocked(ctx)
	if err != nil {
		unlock()
		return nil, nil, err
	}
	return db, unlock, nil
}

func openAuthDatabaseUnlocked(ctx context.Context) (*sql.DB, error) {
	path, err := resolvedAuthDatabasePath()
	if err != nil {
		return nil, fmt.Errorf("resolve auth database path: %w", err)
	}
	root := filepath.Dir(path)
	return sqlitestore.Open(ctx, path, authStoreOptions(root))
}

func lockAuthDatabaseAccess() (func(), error) {
	authDatabaseAccessMu.Lock()
	lockPath, err := authStoreLockPath()
	if err != nil {
		authDatabaseAccessMu.Unlock()
		return nil, err
	}
	unlockFile, err := lockAuthPath(lockPath)
	if err != nil {
		authDatabaseAccessMu.Unlock()
		return nil, err
	}
	return func() {
		unlockFile()
		authDatabaseAccessMu.Unlock()
	}, nil
}

func lockAuthPath(path string) (func(), error) {
	if err := sqlitestore.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return lockAuthStore(path)
}

func authStoreOptions(root string) sqlitestore.Options {
	return sqlitestore.Options{
		Component: authDatabaseComponent,
		Migrations: []sqlitestore.Migration{{
			Version: 1,
			Statements: []string{
				authCredentialsSchema,
				authCredentialsProviderIndexSchema,
			},
		}},
		Validate: validateAuthSchema,
		Legacy: &sqlitestore.LegacyOptions{
			SourceRoot:  root,
			ArchiveRoot: filepath.Join(root, "legacy-json", legacyAuthArchiveLabel),
			Sources: func() ([]sqlitestore.LegacySource, error) {
				return []sqlitestore.LegacySource{{
					ID:       legacyAuthSourceID,
					Relative: legacyAuthFilename,
					MaxBytes: legacyAuthMaxBytes,
				}}, nil
			},
			Import:   importLegacyAuthStore,
			MaxBytes: legacyAuthMaxBytes,
		},
	}
}

func validateAuthSchema(ctx context.Context, conn *sql.Conn) error {
	for _, object := range []struct {
		objectType string
		name       string
		schema     string
	}{
		{objectType: "table", name: "auth_credentials", schema: authCredentialsSchema},
		{
			objectType: "index",
			name:       "auth_credentials_provider_idx",
			schema:     authCredentialsProviderIndexSchema,
		},
	} {
		if err := sqlitestore.ValidateSchemaObject(
			ctx,
			conn,
			object.objectType,
			object.name,
			object.schema,
		); err != nil {
			return err
		}
	}
	if err := sqlitestore.ValidateUniqueIndexSet(ctx, conn, "auth_credentials"); err != nil {
		return err
	}
	var unexpected int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
        WHERE name NOT LIKE 'sqlite_%'
          AND name NOT IN (
              'auth_credentials',
              'auth_credentials_provider_idx',
              'storage_imports',
              'storage_import_issues',
              'storage_imports_archive_status_idx'
          )`).Scan(&unexpected); err != nil {
		return err
	}
	if unexpected != 0 {
		return errors.New("auth schema has unexpected objects")
	}
	var credentialCount int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_credentials`).Scan(
		&credentialCount,
	); err != nil {
		return err
	}
	if credentialCount > maximumAuthCredentials {
		return errors.New("auth database exceeds its credential limit")
	}
	return nil
}

type legacyAuthCandidate struct {
	credential *AuthCredential
	canonical  bool
	digest     [sha256.Size]byte
}

type legacyAuthRawCredential struct {
	sourceKey string
	raw       json.RawMessage
}

func importLegacyAuthStore(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	credentials, err := decodeLegacyAuthCredentials(input.Data)
	if err != nil {
		return sqlitestore.ImportResult{}, errors.New("legacy auth store is malformed")
	}
	sort.SliceStable(credentials, func(left, right int) bool {
		return credentials[left].sourceKey < credentials[right].sourceKey
	})

	winners := make(map[string]legacyAuthCandidate, len(credentials))
	result := sqlitestore.ImportResult{}
	addIssue := func(code string, digest [sha256.Size]byte) {
		result.Skipped++
		if len(result.Issues) < 512 {
			result.Issues = append(result.Issues, sqlitestore.ImportIssue{
				Code:         code,
				RecordDigest: digest,
			})
		}
	}

	for _, source := range credentials {
		sourceKey := source.sourceKey
		raw := source.raw
		digest := sha256.Sum256(raw)
		if string(raw) == "null" {
			addIssue("invalid-credential", digest)
			continue
		}
		var credential AuthCredential
		if err := json.Unmarshal(raw, &credential); err != nil {
			addIssue("invalid-credential", digest)
			continue
		}
		credentialID := canonicalCredentialID(sourceKey)
		if !validStoredCredentialID(credentialID) {
			addIssue("invalid-identity", digest)
			continue
		}
		normalized, err := normalizeCredentialForStorage(credentialID, &credential)
		if err != nil {
			addIssue("invalid-credential", digest)
			continue
		}
		candidate := legacyAuthCandidate{
			credential: normalized,
			canonical:  strings.ToLower(strings.TrimSpace(sourceKey)) == credentialID,
			digest:     digest,
		}
		current, exists := winners[credentialID]
		if !exists {
			winners[credentialID] = candidate
			continue
		}
		if shouldPreferCredential(
			candidate.credential,
			candidate.canonical,
			current.credential,
			current.canonical,
		) {
			addIssue("identity-conflict", current.digest)
			winners[credentialID] = candidate
		} else {
			addIssue("identity-conflict", candidate.digest)
		}
	}

	credentialIDs := make([]string, 0, len(winners))
	for credentialID := range winners {
		credentialIDs = append(credentialIDs, credentialID)
	}
	sort.Strings(credentialIDs)
	for _, credentialID := range credentialIDs {
		candidate := winners[credentialID]
		executionResult, err := insertCredential(ctx, conn, credentialID, candidate.credential)
		if err != nil {
			return sqlitestore.ImportResult{}, err
		}
		inserted, err := executionResult.RowsAffected()
		if err != nil {
			return sqlitestore.ImportResult{}, err
		}
		if inserted == 1 {
			result.Imported++
			continue
		}
		addIssue("sqlite-authoritative", candidate.digest)
	}
	return result, nil
}

func decodeLegacyAuthCredentials(data []byte) ([]legacyAuthRawCredential, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if token == nil {
		return nil, requireLegacyJSONEOF(decoder)
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, errors.New("legacy auth root is not an object")
	}
	var credentials []legacyAuthRawCredential
	for decoder.More() {
		nameToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return nil, tokenErr
		}
		name, ok := nameToken.(string)
		if !ok {
			return nil, errors.New("legacy auth field name is invalid")
		}
		var raw json.RawMessage
		if decodeErr := decoder.Decode(&raw); decodeErr != nil {
			return nil, decodeErr
		}
		if name != "credentials" {
			continue
		}
		entries, decodeErr := decodeLegacyCredentialObject(raw)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if len(entries) > maximumAuthCredentials-len(credentials) {
			return nil, errors.New("legacy auth credential count exceeds its limit")
		}
		credentials = append(credentials, entries...)
	}
	closing, closingErr := decoder.Token()
	if closingErr != nil {
		return nil, closingErr
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return nil, errors.New("legacy auth root is not closed")
	}
	if eofErr := requireLegacyJSONEOF(decoder); eofErr != nil {
		return nil, eofErr
	}
	return credentials, nil
}

func decodeLegacyCredentialObject(raw json.RawMessage) ([]legacyAuthRawCredential, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if token == nil {
		return nil, requireLegacyJSONEOF(decoder)
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, errors.New("legacy credentials field is not an object")
	}
	var credentials []legacyAuthRawCredential
	for decoder.More() {
		keyToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return nil, tokenErr
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("legacy credential key is invalid")
		}
		var credential json.RawMessage
		if decodeErr := decoder.Decode(&credential); decodeErr != nil {
			return nil, decodeErr
		}
		credentials = append(credentials, legacyAuthRawCredential{
			sourceKey: key,
			raw:       credential,
		})
		if len(credentials) > maximumAuthCredentials {
			return nil, errors.New("legacy auth credential count exceeds its limit")
		}
	}
	closing, closingErr := decoder.Token()
	if closingErr != nil {
		return nil, closingErr
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return nil, errors.New("legacy credentials field is not closed")
	}
	if eofErr := requireLegacyJSONEOF(decoder); eofErr != nil {
		return nil, eofErr
	}
	return credentials, nil
}

func requireLegacyJSONEOF(decoder *json.Decoder) error {
	var trailing json.RawMessage
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("legacy auth store has trailing JSON")
}

func validStoredCredentialID(credentialID string) bool {
	if !utf8.ValidString(credentialID) || len(credentialID) > maximumCredentialIDBytes {
		return false
	}
	provider, suffix, qualified := strings.Cut(credentialID, ":")
	if !validCredentialIDPart(provider) {
		return false
	}
	if !qualified {
		return true
	}
	return validCredentialIDPart(suffix)
}

func normalizeCredentialForStorage(
	credentialID string,
	credential *AuthCredential,
) (*AuthCredential, error) {
	if credential == nil {
		return nil, errors.New("credential is required")
	}
	if !validStoredCredentialID(credentialID) {
		return nil, fmt.Errorf("invalid credential id %q", credentialID)
	}
	normalized := cloneCredential(credential)
	normalized.Provider = canonicalProvider(normalized.Provider)
	if normalized.Provider == "" {
		normalized.Provider = credentialProvider(credentialID)
	}
	if !validCredentialIDPart(normalized.Provider) {
		return nil, fmt.Errorf("invalid credential provider %q", normalized.Provider)
	}
	if err := validateCredentialTextFields(normalized); err != nil {
		return nil, err
	}
	if _, _, err := credentialExpiryValues(normalized.ExpiresAt); err != nil {
		return nil, err
	}
	return normalized, nil
}

func validateCredentialTextFields(credential *AuthCredential) error {
	if credential == nil {
		return errors.New("credential is required")
	}
	fields := []struct {
		name    string
		value   string
		maximum int
	}{
		{name: "provider", value: credential.Provider, maximum: maximumCredentialProvider},
		{name: "access_token", value: credential.AccessToken, maximum: maximumAccessTokenBytes},
		{name: "refresh_token", value: credential.RefreshToken, maximum: maximumRefreshTokenBytes},
		{name: "token_type", value: credential.TokenType, maximum: maximumTokenTypeBytes},
		{name: "oauth_token_url", value: credential.OAuthTokenURL, maximum: maximumOAuthTokenURLBytes},
		{name: "oauth_client_id", value: credential.OAuthClientID, maximum: maximumOAuthClientIDBytes},
		{
			name:    "oauth_client_secret",
			value:   credential.OAuthClientSecret,
			maximum: maximumOAuthClientSecret,
		},
		{name: "oauth_auth_style", value: credential.OAuthAuthStyle, maximum: maximumOAuthAuthStyleBytes},
		{name: "account_id", value: credential.AccountID, maximum: maximumAccountIDBytes},
		{name: "auth_method", value: credential.AuthMethod, maximum: maximumAuthMethodBytes},
		{name: "email", value: credential.Email, maximum: maximumEmailBytes},
		{name: "project_id", value: credential.ProjectID, maximum: maximumProjectIDBytes},
	}
	for _, field := range fields {
		if !utf8.ValidString(field.value) || len(field.value) > field.maximum {
			return fmt.Errorf("credential field %s exceeds its UTF-8 byte limit", field.name)
		}
	}
	return nil
}

func credentialExpiryValues(expiresAt time.Time) (any, any, error) {
	if expiresAt.IsZero() {
		return nil, nil, nil
	}
	if expiresAt.Year() < 0 || expiresAt.Year() > 9999 {
		return nil, nil, errors.New("credential expiry is outside the supported timestamp range")
	}
	seconds := expiresAt.Unix()
	nanoseconds := int64(expiresAt.Nanosecond())
	return seconds, nanoseconds, nil
}

type credentialRowScanner interface {
	Scan(dest ...any) error
}

func scanCredential(scanner credentialRowScanner) (string, *AuthCredential, int64, error) {
	var (
		credentialID         string
		credential           AuthCredential
		expiresAtSeconds     sql.NullInt64
		expiresAtNanoseconds sql.NullInt64
		version              int64
	)
	if err := scanner.Scan(
		&credentialID,
		&credential.Provider,
		&credential.AccessToken,
		&credential.RefreshToken,
		&credential.TokenType,
		&credential.OAuthTokenURL,
		&credential.OAuthClientID,
		&credential.OAuthClientSecret,
		&credential.OAuthAuthStyle,
		&credential.AccountID,
		&expiresAtSeconds,
		&expiresAtNanoseconds,
		&credential.AuthMethod,
		&credential.Email,
		&credential.ProjectID,
		&version,
	); err != nil {
		return "", nil, 0, err
	}
	if expiresAtSeconds.Valid != expiresAtNanoseconds.Valid {
		return "", nil, 0, errors.New("credential expiry columns are inconsistent")
	}
	if expiresAtSeconds.Valid {
		credential.ExpiresAt = time.Unix(
			expiresAtSeconds.Int64,
			expiresAtNanoseconds.Int64,
		).UTC()
	}
	return credentialID, &credential, version, nil
}

func credentialArguments(credentialID string, credential *AuthCredential) ([]any, error) {
	if !validStoredCredentialID(credentialID) {
		return nil, errors.New("credential identity is invalid")
	}
	if credential == nil || !validCredentialIDPart(credential.Provider) {
		return nil, errors.New("credential provider is invalid")
	}
	if err := validateCredentialTextFields(credential); err != nil {
		return nil, err
	}
	expiresAtSeconds, expiresAtNanoseconds, err := credentialExpiryValues(credential.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return []any{
		credentialID,
		credential.Provider,
		credential.AccessToken,
		credential.RefreshToken,
		credential.TokenType,
		credential.OAuthTokenURL,
		credential.OAuthClientID,
		credential.OAuthClientSecret,
		credential.OAuthAuthStyle,
		credential.AccountID,
		expiresAtSeconds,
		expiresAtNanoseconds,
		credential.AuthMethod,
		credential.Email,
		credential.ProjectID,
	}, nil
}

func insertCredential(
	ctx context.Context,
	conn *sql.Conn,
	credentialID string,
	credential *AuthCredential,
) (sql.Result, error) {
	if err := ensureCredentialCapacity(ctx, conn, credentialID); err != nil {
		return nil, err
	}
	arguments, err := credentialArguments(credentialID, credential)
	if err != nil {
		return nil, err
	}
	return conn.ExecContext(ctx, insertCredentialSQL+" ON CONFLICT(credential_id) DO NOTHING", arguments...)
}

func upsertCredential(
	ctx context.Context,
	conn *sql.Conn,
	credentialID string,
	credential *AuthCredential,
) error {
	if err := ensureCredentialCapacity(ctx, conn, credentialID); err != nil {
		return err
	}
	return upsertCredentialUnchecked(ctx, conn, credentialID, credential)
}

func upsertCredentialUnchecked(
	ctx context.Context,
	conn *sql.Conn,
	credentialID string,
	credential *AuthCredential,
) error {
	arguments, err := credentialArguments(credentialID, credential)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, upsertCredentialSQL, arguments...)
	return err
}

func ensureCredentialCapacity(ctx context.Context, conn *sql.Conn, credentialID string) error {
	return ensureCredentialCapacityWithin(ctx, conn, credentialID, maximumAuthCredentials)
}

func ensureCredentialCapacityWithin(
	ctx context.Context,
	conn *sql.Conn,
	credentialID string,
	maximum int,
) error {
	if maximum < 1 {
		return errors.New("auth credential limit is invalid")
	}
	var exists, count int
	if err := conn.QueryRowContext(ctx, `SELECT
        EXISTS(SELECT 1 FROM auth_credentials WHERE credential_id = ?),
        (SELECT COUNT(*) FROM auth_credentials)`, credentialID).Scan(&exists, &count); err != nil {
		return err
	}
	if exists == 0 && count >= maximum {
		return errors.New("auth database has reached its credential limit")
	}
	return nil
}

func updateCredentialVersioned(
	ctx context.Context,
	conn *sql.Conn,
	credentialID string,
	credential *AuthCredential,
	version int64,
) error {
	expiresAtSeconds, expiresAtNanoseconds, err := credentialExpiryValues(credential.ExpiresAt)
	if err != nil {
		return err
	}
	result, err := conn.ExecContext(
		ctx,
		updateCredentialSQL,
		credential.Provider,
		credential.AccessToken,
		credential.RefreshToken,
		credential.TokenType,
		credential.OAuthTokenURL,
		credential.OAuthClientID,
		credential.OAuthClientSecret,
		credential.OAuthAuthStyle,
		credential.AccountID,
		expiresAtSeconds,
		expiresAtNanoseconds,
		credential.AuthMethod,
		credential.Email,
		credential.ProjectID,
		credentialID,
		version,
	)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("credential version changed during update")
	}
	return nil
}

func loadCredentialFromConn(
	ctx context.Context,
	conn *sql.Conn,
	credentialID string,
) (*AuthCredential, int64, bool, error) {
	_, credential, version, err := scanCredential(conn.QueryRowContext(
		ctx,
		selectCredentialSQL+" WHERE credential_id = ?",
		credentialID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	return credential, version, true, nil
}

func loadAuthStore(ctx context.Context, db *sql.DB) (*AuthStore, error) {
	rows, err := db.QueryContext(ctx, selectCredentialSQL+" ORDER BY credential_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	store := &AuthStore{Credentials: make(map[string]*AuthCredential)}
	for rows.Next() {
		credentialID, credential, _, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		store.Credentials[credentialID] = credential
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return store, nil
}

func normalizedAuthStore(store *AuthStore) (*AuthStore, error) {
	snapshot := &AuthStore{Credentials: make(map[string]*AuthCredential)}
	if store != nil {
		if len(store.Credentials) > maximumAuthCredentials {
			return nil, errors.New("auth store exceeds its credential limit")
		}
		for credentialID, credential := range store.Credentials {
			snapshot.Credentials[credentialID] = cloneCredential(credential)
		}
	}
	normalizeStore(snapshot)
	for credentialID, credential := range snapshot.Credentials {
		normalized, err := normalizeCredentialForStorage(credentialID, credential)
		if err != nil {
			return nil, err
		}
		snapshot.Credentials[credentialID] = normalized
	}
	return snapshot, nil
}

func existingCredentialIDs(ctx context.Context, conn *sql.Conn) ([]string, error) {
	rows, err := conn.QueryContext(ctx, `SELECT credential_id FROM auth_credentials ORDER BY credential_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var credentialIDs []string
	for rows.Next() {
		var credentialID string
		if err := rows.Scan(&credentialID); err != nil {
			return nil, err
		}
		credentialIDs = append(credentialIDs, credentialID)
	}
	return credentialIDs, rows.Err()
}

func closeAuthDatabase(db *sql.DB) {
	if db != nil {
		_ = db.Close()
	}
}
