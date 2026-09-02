package dashboardauth

const (
	databaseFilename = "launcher-auth.db"

	// bcryptCost is deliberately high enough to slow brute-force attempts.
	bcryptCost = 12

	databaseComponent    = "launcher-auth"
	legacySourceID       = "launcher-config-auth-v1"
	legacyArchiveVersion = "launcher-auth-v1"
	legacyConfigMaxBytes = int64(4 << 20)

	sqlCreateCredentials = `CREATE TABLE dashboard_credentials (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    bcrypt_hash TEXT NOT NULL
) STRICT`

	sqlLegacyCreateCredentials = `CREATE TABLE dashboard_credentials (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    bcrypt_hash TEXT NOT NULL
)`

	sqlCreateLegacyImports = `CREATE TABLE launcher_auth_legacy_imports (
    source_id          TEXT PRIMARY KEY CHECK (source_id = 'launcher-config-auth-v1'),
    source_relative    TEXT NOT NULL,
    source_digest      BLOB NOT NULL CHECK (length(source_digest) = 32),
    source_size        INTEGER NOT NULL CHECK (source_size >= 0),
    source_limit       INTEGER NOT NULL CHECK (source_limit >= source_size),
    source_mode        INTEGER NOT NULL CHECK (source_mode >= 0 AND source_mode <= 511),
    credential_source  TEXT NOT NULL CHECK (credential_source IN (
        'existing-database', 'dashboard-password-hash', 'launcher-token', 'none'
    )),
    imported_count     INTEGER NOT NULL CHECK (imported_count IN (0, 1)),
    skipped_count      INTEGER NOT NULL CHECK (skipped_count >= 0),
    issue_code         TEXT CHECK (issue_code IS NULL OR issue_code = 'invalid-bcrypt-hash'),
    archive_status     TEXT NOT NULL CHECK (archive_status IN ('pending', 'complete')),
    imported_at        TEXT NOT NULL,
    archived_at        TEXT,
    CHECK (
        (credential_source IN ('dashboard-password-hash', 'launcher-token') AND imported_count = 1)
        OR (credential_source IN ('existing-database', 'none') AND imported_count = 0)
    ),
    CHECK (
        (archive_status = 'pending' AND archived_at IS NULL)
        OR (archive_status = 'complete' AND archived_at IS NOT NULL)
    )
) STRICT`

	sqlCreateLegacyImportsIndex = `CREATE INDEX launcher_auth_legacy_imports_status_idx
    ON launcher_auth_legacy_imports(archive_status, source_id)`

	sqlCountCredentials = `SELECT COUNT(*) FROM dashboard_credentials WHERE id = 1`

	sqlUpsertHash = `
		INSERT INTO dashboard_credentials (id, bcrypt_hash) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET bcrypt_hash = excluded.bcrypt_hash`

	sqlSelectHash = `SELECT bcrypt_hash FROM dashboard_credentials WHERE id = 1`
)
