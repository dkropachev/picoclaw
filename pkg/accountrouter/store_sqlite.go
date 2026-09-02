package accountrouter

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const (
	accountRouterDatabaseFilename    = "account-router.db"
	accountRouterLegacyFilename      = "account_router_state.json"
	accountRouterDatabaseComponent   = "account-router"
	accountRouterLegacyArchiveLabel  = "account-router-v1"
	accountRouterLegacySourceID      = "account-router-json-v1"
	accountRouterLegacySidecarPrefix = "account-router-auth-invalidation-"
	accountRouterLegacyMaxBytes      = int64(64 << 20)

	maximumAccountRouterRouters        = 10_000
	maximumAccountRouterAccounts       = 100_000
	maximumAccountRouterSessions       = 100_000
	maximumAccountRouterAffinities     = 500_000
	maximumAccountRouterBlockCursors   = 100_000
	maximumAccountRouterInvalidations  = 100_000
	maximumAccountRouterIdentityBytes  = 64 << 10
	maximumAccountRouterErrorBytes     = 1 << 20
	maximumAccountRouterAggregateBytes = int64(1 << 30)
)

const accountRouterStoreSchema = `CREATE TABLE account_router_store (
    id                  INTEGER PRIMARY KEY CHECK(id = 1),
    generation          INTEGER NOT NULL DEFAULT 0 CHECK(generation >= 0),
    version             INTEGER NOT NULL DEFAULT 1 CHECK(version > 0)
) STRICT`

const accountRouterStoreSeedSchema = `INSERT INTO account_router_store(id, generation, version)
    VALUES (1, 0, 1)`

const accountRouterRoutersSchema = `CREATE TABLE account_router_routers (
    router_name                 TEXT PRIMARY KEY,
    config_hash                 TEXT NOT NULL,
    updated_at_unix_seconds     INTEGER,
    updated_at_nanosecond       INTEGER,
    touched_generation          INTEGER NOT NULL CHECK(touched_generation >= 0),
    version                     INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    CHECK(length(CAST(router_name AS BLOB)) BETWEEN 1 AND 65536),
    CHECK(length(CAST(config_hash AS BLOB)) BETWEEN 1 AND 256),
    CHECK(instr(router_name, char(0)) = 0),
    CHECK(instr(config_hash, char(0)) = 0),
    CHECK(
        (updated_at_unix_seconds IS NULL AND updated_at_nanosecond IS NULL)
        OR (
            updated_at_unix_seconds IS NOT NULL
            AND updated_at_nanosecond IS NOT NULL
            AND updated_at_unix_seconds BETWEEN -62167219200 AND 253402300799
            AND updated_at_nanosecond BETWEEN 0 AND 999999999
        )
    )
) STRICT`

const accountRouterAccountsSchema = `CREATE TABLE account_router_accounts (
    router_name                         TEXT NOT NULL REFERENCES account_router_routers(router_name) ON DELETE CASCADE,
    account_ref                         TEXT NOT NULL,
    health_state                        TEXT NOT NULL CHECK(health_state IN ('operational', 'unavailable')),
    failure_reason                      TEXT NOT NULL CHECK(failure_reason IN (
        '', 'auth', 'rate_limit', 'billing', 'network', 'timeout', 'format',
        'safety_filter', 'context_overflow', 'overloaded', 'unknown'
    )),
    failure_count                       INTEGER NOT NULL CHECK(failure_count >= 0),
    requests                            INTEGER NOT NULL CHECK(requests >= 0),
    rate_window_start_unix_seconds      INTEGER,
    rate_window_start_nanosecond        INTEGER,
    rate_window_requests                INTEGER NOT NULL CHECK(rate_window_requests >= 0),
    prompt_tokens                       INTEGER NOT NULL CHECK(prompt_tokens >= 0),
    completion_tokens                   INTEGER NOT NULL CHECK(completion_tokens >= 0),
    total_tokens                        INTEGER NOT NULL CHECK(total_tokens >= 0),
    unavailable_until_unix_seconds      INTEGER,
    unavailable_until_nanosecond        INTEGER,
    last_failure_at_unix_seconds        INTEGER,
    last_failure_at_nanosecond          INTEGER,
    last_success_at_unix_seconds        INTEGER,
    last_success_at_nanosecond          INTEGER,
    last_error                          TEXT NOT NULL,
    auth_invalidation_generation        TEXT NOT NULL,
    touched_generation                  INTEGER NOT NULL CHECK(touched_generation >= 0),
    version                             INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    PRIMARY KEY(router_name, account_ref),
    CHECK(length(CAST(account_ref AS BLOB)) BETWEEN 1 AND 65536),
    CHECK(length(CAST(last_error AS BLOB)) <= 1048576),
    CHECK(length(CAST(auth_invalidation_generation AS BLOB)) <= 256),
    CHECK(instr(account_ref, char(0)) = 0),
    CHECK(instr(last_error, char(0)) = 0),
    CHECK(instr(auth_invalidation_generation, char(0)) = 0),
    CHECK(
        (rate_window_start_unix_seconds IS NULL AND rate_window_start_nanosecond IS NULL)
        OR (rate_window_start_unix_seconds IS NOT NULL AND rate_window_start_nanosecond IS NOT NULL
            AND rate_window_start_unix_seconds BETWEEN -62167219200 AND 253402300799
            AND rate_window_start_nanosecond BETWEEN 0 AND 999999999)
    ),
    CHECK(
        (unavailable_until_unix_seconds IS NULL AND unavailable_until_nanosecond IS NULL)
        OR (unavailable_until_unix_seconds IS NOT NULL AND unavailable_until_nanosecond IS NOT NULL
            AND unavailable_until_unix_seconds BETWEEN -62167219200 AND 253402300799
            AND unavailable_until_nanosecond BETWEEN 0 AND 999999999)
    ),
    CHECK(
        (last_failure_at_unix_seconds IS NULL AND last_failure_at_nanosecond IS NULL)
        OR (last_failure_at_unix_seconds IS NOT NULL AND last_failure_at_nanosecond IS NOT NULL
            AND last_failure_at_unix_seconds BETWEEN -62167219200 AND 253402300799
            AND last_failure_at_nanosecond BETWEEN 0 AND 999999999)
    ),
    CHECK(
        (last_success_at_unix_seconds IS NULL AND last_success_at_nanosecond IS NULL)
        OR (last_success_at_unix_seconds IS NOT NULL AND last_success_at_nanosecond IS NOT NULL
            AND last_success_at_unix_seconds BETWEEN -62167219200 AND 253402300799
            AND last_success_at_nanosecond BETWEEN 0 AND 999999999)
    )
) STRICT`

const accountRouterSessionsSchema = `CREATE TABLE account_router_sessions (
    router_name                 TEXT NOT NULL REFERENCES account_router_routers(router_name) ON DELETE CASCADE,
    session_key                 TEXT NOT NULL,
    config_hash                 TEXT NOT NULL,
    updated_at_unix_seconds     INTEGER NOT NULL CHECK(updated_at_unix_seconds BETWEEN -62167219200 AND 253402300799),
    updated_at_nanosecond       INTEGER NOT NULL CHECK(updated_at_nanosecond BETWEEN 0 AND 999999999),
    touched_generation          INTEGER NOT NULL CHECK(touched_generation >= 0),
    version                     INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    PRIMARY KEY(router_name, session_key),
    CHECK(length(CAST(session_key AS BLOB)) BETWEEN 1 AND 65536),
    CHECK(length(CAST(config_hash AS BLOB)) BETWEEN 1 AND 256),
    CHECK(instr(session_key, char(0)) = 0),
    CHECK(instr(config_hash, char(0)) = 0)
) STRICT`

const accountRouterAffinitiesSchema = `CREATE TABLE account_router_session_affinities (
    router_name                 TEXT NOT NULL,
    session_key                 TEXT NOT NULL,
    block_id                    TEXT NOT NULL,
    account_ref                 TEXT NOT NULL,
    select_reason               TEXT NOT NULL CHECK(select_reason IN ('initial', 'compression', 'unhealthy')),
    selected_at_unix_seconds    INTEGER NOT NULL CHECK(selected_at_unix_seconds BETWEEN -62167219200 AND 253402300799),
    selected_at_nanosecond      INTEGER NOT NULL CHECK(selected_at_nanosecond BETWEEN 0 AND 999999999),
    touched_generation          INTEGER NOT NULL CHECK(touched_generation >= 0),
    version                     INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    PRIMARY KEY(router_name, session_key, block_id),
    FOREIGN KEY(router_name, session_key)
        REFERENCES account_router_sessions(router_name, session_key) ON DELETE CASCADE,
    CHECK(length(CAST(block_id AS BLOB)) BETWEEN 1 AND 65536),
    CHECK(length(CAST(account_ref AS BLOB)) BETWEEN 1 AND 65536),
    CHECK(instr(block_id, char(0)) = 0),
    CHECK(instr(account_ref, char(0)) = 0)
) STRICT`

const accountRouterBlockCursorsSchema = `CREATE TABLE account_router_block_cursors (
    router_name                 TEXT NOT NULL REFERENCES account_router_routers(router_name) ON DELETE CASCADE,
    block_id                    TEXT NOT NULL,
    cursor                      INTEGER NOT NULL CHECK(cursor >= 0),
    updated_at_unix_seconds     INTEGER,
    updated_at_nanosecond       INTEGER,
    touched_generation          INTEGER NOT NULL CHECK(touched_generation >= 0),
    version                     INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    PRIMARY KEY(router_name, block_id),
    CHECK(length(CAST(block_id AS BLOB)) BETWEEN 1 AND 65536),
    CHECK(instr(block_id, char(0)) = 0),
    CHECK(
        (updated_at_unix_seconds IS NULL AND updated_at_nanosecond IS NULL)
        OR (updated_at_unix_seconds IS NOT NULL AND updated_at_nanosecond IS NOT NULL
            AND updated_at_unix_seconds BETWEEN -62167219200 AND 253402300799
            AND updated_at_nanosecond BETWEEN 0 AND 999999999)
    )
) STRICT`

const accountRouterInvalidationsSchema = `CREATE TABLE account_router_auth_invalidations (
    credential_id                  TEXT PRIMARY KEY,
    generation                     TEXT NOT NULL,
    created_at_unix_seconds        INTEGER NOT NULL CHECK(created_at_unix_seconds BETWEEN -62167219200 AND 253402300799),
    created_at_nanosecond          INTEGER NOT NULL CHECK(created_at_nanosecond BETWEEN 0 AND 999999999),
    version                        INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    CHECK(length(CAST(credential_id AS BLOB)) BETWEEN 1 AND 65536),
    CHECK(length(CAST(generation AS BLOB)) BETWEEN 1 AND 256),
    CHECK(instr(credential_id, char(0)) = 0),
    CHECK(instr(generation, char(0)) = 0)
) STRICT`

const accountRouterAccountsHealthIndexSchema = `CREATE INDEX account_router_accounts_health_idx
    ON account_router_accounts(health_state, unavailable_until_unix_seconds, router_name, account_ref)`

const accountRouterSessionsUpdatedIndexSchema = `CREATE INDEX account_router_sessions_updated_idx
    ON account_router_sessions(updated_at_unix_seconds, updated_at_nanosecond, router_name, session_key)`

const accountRouterAffinitiesAccountIndexSchema = `CREATE INDEX account_router_affinities_account_idx
    ON account_router_session_affinities(account_ref, router_name, session_key, block_id)`

const accountRouterInvalidationsCreatedIndexSchema = `CREATE INDEX account_router_invalidations_created_idx
    ON account_router_auth_invalidations(created_at_unix_seconds, created_at_nanosecond, credential_id)`

type accountRouterStorePaths struct {
	databasePath   string
	sourceRoot     string
	sourceRelative string
	archiveRoot    string
}

type credentialAuthInvalidation struct {
	Version      int    `json:"version"`
	CredentialID string `json:"credential_id"`
	Generation   string `json:"generation"`
}

var accountRouterRandomRead = rand.Read

var allowUnfencedAccountRouterProviderForTests atomic.Bool

// databasePath returns the broker-side/offline account-router database.
func databasePath(workspace string) string {
	return filepath.Join(workspace, "state", accountRouterDatabaseFilename)
}

func resolveAccountRouterStorePaths(locator string) (accountRouterStorePaths, error) {
	if strings.TrimSpace(locator) == "" || strings.ContainsRune(locator, '\x00') {
		return accountRouterStorePaths{}, errors.New("account-router state path is required")
	}
	resolved, err := filepath.Abs(filepath.Clean(locator))
	if err != nil {
		return accountRouterStorePaths{}, err
	}
	extension := strings.ToLower(filepath.Ext(resolved))
	paths := accountRouterStorePaths{}
	switch extension {
	case ".json":
		paths.databasePath = strings.TrimSuffix(resolved, filepath.Ext(resolved)) + ".db"
		paths.sourceRoot = filepath.Dir(resolved)
		paths.sourceRelative = filepath.Base(resolved)
		paths.archiveRoot = filepath.Join(paths.sourceRoot, "legacy-json", accountRouterLegacyArchiveLabel)
	case ".db":
		paths.databasePath = resolved
		if filepath.Base(resolved) == accountRouterDatabaseFilename &&
			filepath.Base(filepath.Dir(resolved)) == "state" {
			workspace := filepath.Dir(filepath.Dir(resolved))
			paths.sourceRoot = workspace
			paths.sourceRelative = accountRouterLegacyFilename
			paths.archiveRoot = filepath.Join(
				workspace, "state", "legacy-json", accountRouterLegacyArchiveLabel,
			)
		} else {
			paths.sourceRoot = filepath.Dir(resolved)
			paths.sourceRelative = strings.TrimSuffix(filepath.Base(resolved), filepath.Ext(resolved)) + ".json"
			paths.archiveRoot = filepath.Join(paths.sourceRoot, "legacy-json", accountRouterLegacyArchiveLabel)
		}
	default:
		paths.databasePath = databasePath(resolved)
		paths.sourceRoot = resolved
		paths.sourceRelative = accountRouterLegacyFilename
		paths.archiveRoot = filepath.Join(
			resolved, "state", "legacy-json", accountRouterLegacyArchiveLabel,
		)
	}
	return paths, nil
}

func (s *Store) open(ctx context.Context) (*sql.DB, func(), error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil, nil, errors.New("account-router store is unavailable")
	}
	if !database.BrokerAuthorityHeld() && !database.MigrationFenceHeld() &&
		!database.ProviderTestAuthorityHeld() &&
		!allowUnfencedAccountRouterProviderForTests.Load() {
		return nil, nil, database.NewError(
			database.CodeUnauthorized,
			"account-router provider access requires database owner fencing",
		)
	}
	if s.retainedDB != nil {
		return s.retainedDB, func() {}, nil
	}
	unlock, err := lockAccountRouterDatabase(s.path)
	if err != nil {
		return nil, nil, err
	}
	db, err := sqlitestore.Open(ctx, s.path, s.options())
	if err != nil {
		unlock()
		return nil, nil, err
	}
	return db, unlock, nil
}

func (s *Store) retain() error {
	if s == nil {
		return errors.New("account-router store is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retainedDB != nil {
		return nil
	}
	db, unlock, err := s.open(context.Background())
	if err != nil {
		return err
	}
	s.retainedDB = db
	s.retainedUnlock = unlock
	return nil
}

func (s *Store) closeRetained() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	db := s.retainedDB
	unlock := s.retainedUnlock
	s.retainedDB = nil
	s.retainedUnlock = nil
	var err error
	if db != nil {
		err = db.Close()
	}
	if unlock != nil {
		unlock()
	}
	return err
}

func (s *Store) closeOpened(db *sql.DB) {
	if db != nil && db != s.retainedDB {
		_ = db.Close()
	}
}

func (s *Store) options() sqlitestore.Options {
	return sqlitestore.Options{
		Component: accountRouterDatabaseComponent,
		Migrations: []sqlitestore.Migration{{
			Version: 1,
			Statements: []string{
				accountRouterStoreSchema,
				accountRouterStoreSeedSchema,
				accountRouterRoutersSchema,
				accountRouterAccountsSchema,
				accountRouterSessionsSchema,
				accountRouterAffinitiesSchema,
				accountRouterBlockCursorsSchema,
				accountRouterInvalidationsSchema,
				accountRouterAccountsHealthIndexSchema,
				accountRouterSessionsUpdatedIndexSchema,
				accountRouterAffinitiesAccountIndexSchema,
				accountRouterInvalidationsCreatedIndexSchema,
			},
		}},
		Validate: validateAccountRouterSchema,
		Legacy:   s.legacyOptions(),
	}
}

func validateAccountRouterSchema(ctx context.Context, conn *sql.Conn) error {
	for _, object := range []struct {
		objectType string
		name       string
		schema     string
	}{
		{objectType: "table", name: "account_router_store", schema: accountRouterStoreSchema},
		{objectType: "table", name: "account_router_routers", schema: accountRouterRoutersSchema},
		{objectType: "table", name: "account_router_accounts", schema: accountRouterAccountsSchema},
		{objectType: "table", name: "account_router_sessions", schema: accountRouterSessionsSchema},
		{objectType: "table", name: "account_router_session_affinities", schema: accountRouterAffinitiesSchema},
		{objectType: "table", name: "account_router_block_cursors", schema: accountRouterBlockCursorsSchema},
		{objectType: "table", name: "account_router_auth_invalidations", schema: accountRouterInvalidationsSchema},
		{objectType: "index", name: "account_router_accounts_health_idx", schema: accountRouterAccountsHealthIndexSchema},
		{objectType: "index", name: "account_router_sessions_updated_idx", schema: accountRouterSessionsUpdatedIndexSchema},
		{objectType: "index", name: "account_router_affinities_account_idx", schema: accountRouterAffinitiesAccountIndexSchema},
		{objectType: "index", name: "account_router_invalidations_created_idx", schema: accountRouterInvalidationsCreatedIndexSchema},
	} {
		if err := sqlitestore.ValidateSchemaObject(
			ctx, conn, object.objectType, object.name, object.schema,
		); err != nil {
			return err
		}
	}
	for _, table := range []string{
		"account_router_store",
		"account_router_routers",
		"account_router_accounts",
		"account_router_sessions",
		"account_router_session_affinities",
		"account_router_block_cursors",
		"account_router_auth_invalidations",
	} {
		if err := sqlitestore.ValidateUniqueIndexSet(ctx, conn, table); err != nil {
			return err
		}
	}
	var unexpected int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
        WHERE name NOT LIKE 'sqlite_%'
          AND name NOT IN (
              'account_router_store', 'account_router_routers', 'account_router_accounts',
              'account_router_sessions', 'account_router_session_affinities',
              'account_router_block_cursors', 'account_router_auth_invalidations',
              'account_router_accounts_health_idx', 'account_router_sessions_updated_idx',
              'account_router_affinities_account_idx', 'account_router_invalidations_created_idx',
              'storage_imports', 'storage_import_issues', 'storage_import_horizons',
              'storage_imports_archive_status_idx'
          )`).Scan(&unexpected); err != nil {
		return err
	}
	if unexpected != 0 {
		return errors.New("account-router database has unexpected schema objects")
	}
	var singleton, routers, accounts, sessions, affinities, blocks, invalidations int
	var aggregate int64
	if err := conn.QueryRowContext(ctx, `SELECT
        (SELECT COUNT(*) FROM account_router_store WHERE id = 1),
        (SELECT COUNT(*) FROM account_router_routers),
        (SELECT COUNT(*) FROM account_router_accounts),
        (SELECT COUNT(*) FROM account_router_sessions),
        (SELECT COUNT(*) FROM account_router_session_affinities),
        (SELECT COUNT(*) FROM account_router_block_cursors),
        (SELECT COUNT(*) FROM account_router_auth_invalidations),
        (SELECT
            COALESCE((SELECT SUM(length(CAST(router_name AS BLOB)) + length(CAST(config_hash AS BLOB))) FROM account_router_routers), 0) +
            COALESCE((SELECT SUM(length(CAST(account_ref AS BLOB)) + length(CAST(last_error AS BLOB)) + length(CAST(auth_invalidation_generation AS BLOB))) FROM account_router_accounts), 0) +
            COALESCE((SELECT SUM(length(CAST(session_key AS BLOB)) + length(CAST(config_hash AS BLOB))) FROM account_router_sessions), 0) +
            COALESCE((SELECT SUM(length(CAST(block_id AS BLOB)) + length(CAST(account_ref AS BLOB))) FROM account_router_session_affinities), 0) +
            COALESCE((SELECT SUM(length(CAST(block_id AS BLOB))) FROM account_router_block_cursors), 0) +
            COALESCE((SELECT SUM(length(CAST(credential_id AS BLOB)) + length(CAST(generation AS BLOB))) FROM account_router_auth_invalidations), 0)
        )`).Scan(
		&singleton, &routers, &accounts, &sessions, &affinities, &blocks, &invalidations, &aggregate,
	); err != nil {
		return err
	}
	if singleton != 1 || routers > maximumAccountRouterRouters ||
		accounts > maximumAccountRouterAccounts || sessions > maximumAccountRouterSessions ||
		affinities > maximumAccountRouterAffinities || blocks > maximumAccountRouterBlockCursors ||
		invalidations > maximumAccountRouterInvalidations ||
		aggregate > maximumAccountRouterAggregateBytes {
		return errors.New("account-router database violates its data bounds")
	}
	return nil
}

type accountRouterQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func loadAccountRouterState(ctx context.Context, queryer accountRouterQueryer) (State, error) {
	state := State{Version: stateVersion, Routers: make(map[string]*RouterState)}
	rows, err := queryer.QueryContext(ctx, `SELECT
        router_name, config_hash, updated_at_unix_seconds, updated_at_nanosecond
        FROM account_router_routers ORDER BY router_name`)
	if err != nil {
		return State{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, configHash string
		var seconds, nanoseconds sql.NullInt64
		if err := rows.Scan(&name, &configHash, &seconds, &nanoseconds); err != nil {
			return State{}, err
		}
		updated, err := accountRouterScannedTime(seconds, nanoseconds)
		if err != nil {
			return State{}, err
		}
		state.Routers[name] = &RouterState{
			ConfigHash: configHash,
			Accounts:   make(map[string]*AccountState),
			Sessions:   make(map[string]*SessionState),
			Blocks:     make(map[string]*BlockRunState),
			UpdatedAt:  updated,
		}
	}
	if err := rows.Close(); err != nil {
		return State{}, err
	}
	if err := rows.Err(); err != nil {
		return State{}, err
	}
	if err := loadAccountRouterAccounts(ctx, queryer, &state); err != nil {
		return State{}, err
	}
	if err := loadAccountRouterSessions(ctx, queryer, &state); err != nil {
		return State{}, err
	}
	if err := loadAccountRouterAffinities(ctx, queryer, &state); err != nil {
		return State{}, err
	}
	if err := loadAccountRouterBlockCursors(ctx, queryer, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func loadAccountRouterAccounts(ctx context.Context, queryer accountRouterQueryer, state *State) error {
	rows, err := queryer.QueryContext(ctx, `SELECT
        router_name, account_ref, health_state, failure_reason, failure_count,
        requests, rate_window_start_unix_seconds, rate_window_start_nanosecond,
        rate_window_requests, prompt_tokens, completion_tokens, total_tokens,
        unavailable_until_unix_seconds, unavailable_until_nanosecond,
        last_failure_at_unix_seconds, last_failure_at_nanosecond,
        last_success_at_unix_seconds, last_success_at_nanosecond,
        last_error, auth_invalidation_generation
        FROM account_router_accounts ORDER BY router_name, account_ref`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var routerName, accountRef, healthState, reason, lastError, generation string
		var failureCount int
		var requests, rateRequests, prompt, completion, total int64
		var rateSeconds, rateNanos, untilSeconds, untilNanos sql.NullInt64
		var failureSeconds, failureNanos, successSeconds, successNanos sql.NullInt64
		if err := rows.Scan(
			&routerName, &accountRef, &healthState, &reason, &failureCount,
			&requests, &rateSeconds, &rateNanos, &rateRequests, &prompt, &completion, &total,
			&untilSeconds, &untilNanos, &failureSeconds, &failureNanos,
			&successSeconds, &successNanos, &lastError, &generation,
		); err != nil {
			return err
		}
		router := state.Routers[routerName]
		if router == nil {
			return errors.New("account-router account references a missing router")
		}
		rateStart, err := accountRouterScannedTime(rateSeconds, rateNanos)
		if err != nil {
			return err
		}
		until, err := accountRouterScannedTime(untilSeconds, untilNanos)
		if err != nil {
			return err
		}
		lastFailure, err := accountRouterScannedTime(failureSeconds, failureNanos)
		if err != nil {
			return err
		}
		lastSuccess, err := accountRouterScannedTime(successSeconds, successNanos)
		if err != nil {
			return err
		}
		router.Accounts[accountRef] = &AccountState{
			State: healthState, Reason: providers.FailoverReason(reason), FailureCount: failureCount,
			Requests: requests, RateWindowStart: rateStart, RateWindowReqs: rateRequests,
			PromptTokens: prompt, CompletionTokens: completion, TotalTokens: total,
			UnavailableUntil: until, LastFailureAt: lastFailure, LastSuccessAt: lastSuccess,
			LastError: lastError, AuthInvalidationGeneration: generation,
		}
	}
	return rows.Err()
}

func loadAccountRouterSessions(ctx context.Context, queryer accountRouterQueryer, state *State) error {
	rows, err := queryer.QueryContext(ctx, `SELECT
        router_name, session_key, config_hash, updated_at_unix_seconds, updated_at_nanosecond
        FROM account_router_sessions ORDER BY router_name, session_key`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var routerName, sessionKey, configHash string
		var seconds, nanos int64
		if err := rows.Scan(&routerName, &sessionKey, &configHash, &seconds, &nanos); err != nil {
			return err
		}
		router := state.Routers[routerName]
		if router == nil {
			return errors.New("account-router session references a missing router")
		}
		router.Sessions[sessionKey] = &SessionState{
			ConfigHash: configHash,
			Blocks:     make(map[string]BlockAffinity),
			UpdatedAt:  time.Unix(seconds, nanos).UTC(),
		}
	}
	return rows.Err()
}

func loadAccountRouterAffinities(ctx context.Context, queryer accountRouterQueryer, state *State) error {
	rows, err := queryer.QueryContext(ctx, `SELECT
        router_name, session_key, block_id, account_ref, select_reason,
        selected_at_unix_seconds, selected_at_nanosecond
        FROM account_router_session_affinities
        ORDER BY router_name, session_key, block_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var routerName, sessionKey, blockID, accountRef, reason string
		var seconds, nanos int64
		if err := rows.Scan(
			&routerName, &sessionKey, &blockID, &accountRef, &reason, &seconds, &nanos,
		); err != nil {
			return err
		}
		router := state.Routers[routerName]
		if router == nil || router.Sessions[sessionKey] == nil {
			return errors.New("account-router affinity references a missing session")
		}
		router.Sessions[sessionKey].Blocks[blockID] = BlockAffinity{
			Account: accountRef, Reason: SelectReason(reason),
			SelectedAt: time.Unix(seconds, nanos).UTC(),
		}
	}
	return rows.Err()
}

func loadAccountRouterBlockCursors(ctx context.Context, queryer accountRouterQueryer, state *State) error {
	rows, err := queryer.QueryContext(ctx, `SELECT
        router_name, block_id, cursor, updated_at_unix_seconds, updated_at_nanosecond
        FROM account_router_block_cursors ORDER BY router_name, block_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var routerName, blockID string
		var cursor int
		var seconds, nanos sql.NullInt64
		if err := rows.Scan(&routerName, &blockID, &cursor, &seconds, &nanos); err != nil {
			return err
		}
		router := state.Routers[routerName]
		if router == nil {
			return errors.New("account-router block cursor references a missing router")
		}
		updated, err := accountRouterScannedTime(seconds, nanos)
		if err != nil {
			return err
		}
		router.Blocks[blockID] = &BlockRunState{Cursor: cursor, UpdatedAt: updated}
	}
	return rows.Err()
}

//nolint:gocognit // Relational projection is intentionally explicit.
func writeAccountRouterState(
	ctx context.Context,
	conn *sql.Conn,
	state *State,
) error {
	if state == nil {
		return errors.New("account-router state is required")
	}
	state.Version = stateVersion
	if err := validateAccountRouterState(state); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE account_router_store
        SET generation = generation + 1, version = version + 1 WHERE id = 1`); err != nil {
		return err
	}
	var generation int64
	if err := conn.QueryRowContext(ctx, `SELECT generation FROM account_router_store WHERE id = 1`).Scan(
		&generation,
	); err != nil {
		return err
	}
	for _, routerName := range sortedRouterNames(state.Routers) {
		router := state.Routers[routerName]
		updatedSeconds, updatedNanos, err := accountRouterTimeValues(router.UpdatedAt)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO account_router_routers (
            router_name, config_hash, updated_at_unix_seconds, updated_at_nanosecond,
            touched_generation, version
        ) VALUES (?, ?, ?, ?, ?, 1)
        ON CONFLICT(router_name) DO UPDATE SET
            config_hash = excluded.config_hash,
            updated_at_unix_seconds = excluded.updated_at_unix_seconds,
            updated_at_nanosecond = excluded.updated_at_nanosecond,
            touched_generation = excluded.touched_generation,
            version = account_router_routers.version + 1`,
			routerName, router.ConfigHash, updatedSeconds, updatedNanos, generation,
		); err != nil {
			return err
		}
		for _, accountRef := range sortedAccountNames(router.Accounts) {
			account := router.Accounts[accountRef]
			rateSeconds, rateNanos, err := accountRouterTimeValues(account.RateWindowStart)
			if err != nil {
				return err
			}
			untilSeconds, untilNanos, err := accountRouterTimeValues(account.UnavailableUntil)
			if err != nil {
				return err
			}
			failureSeconds, failureNanos, err := accountRouterTimeValues(account.LastFailureAt)
			if err != nil {
				return err
			}
			successSeconds, successNanos, err := accountRouterTimeValues(account.LastSuccessAt)
			if err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO account_router_accounts (
                router_name, account_ref, health_state, failure_reason, failure_count,
                requests, rate_window_start_unix_seconds, rate_window_start_nanosecond,
                rate_window_requests, prompt_tokens, completion_tokens, total_tokens,
                unavailable_until_unix_seconds, unavailable_until_nanosecond,
                last_failure_at_unix_seconds, last_failure_at_nanosecond,
                last_success_at_unix_seconds, last_success_at_nanosecond,
                last_error, auth_invalidation_generation, touched_generation, version
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
            ON CONFLICT(router_name, account_ref) DO UPDATE SET
                health_state = excluded.health_state,
                failure_reason = excluded.failure_reason,
                failure_count = excluded.failure_count,
                requests = excluded.requests,
                rate_window_start_unix_seconds = excluded.rate_window_start_unix_seconds,
                rate_window_start_nanosecond = excluded.rate_window_start_nanosecond,
                rate_window_requests = excluded.rate_window_requests,
                prompt_tokens = excluded.prompt_tokens,
                completion_tokens = excluded.completion_tokens,
                total_tokens = excluded.total_tokens,
                unavailable_until_unix_seconds = excluded.unavailable_until_unix_seconds,
                unavailable_until_nanosecond = excluded.unavailable_until_nanosecond,
                last_failure_at_unix_seconds = excluded.last_failure_at_unix_seconds,
                last_failure_at_nanosecond = excluded.last_failure_at_nanosecond,
                last_success_at_unix_seconds = excluded.last_success_at_unix_seconds,
                last_success_at_nanosecond = excluded.last_success_at_nanosecond,
                last_error = excluded.last_error,
                auth_invalidation_generation = excluded.auth_invalidation_generation,
                touched_generation = excluded.touched_generation,
                version = account_router_accounts.version + 1`,
				routerName, accountRef, account.State, string(account.Reason), account.FailureCount,
				account.Requests, rateSeconds, rateNanos, account.RateWindowReqs,
				account.PromptTokens, account.CompletionTokens, account.TotalTokens,
				untilSeconds, untilNanos, failureSeconds, failureNanos,
				successSeconds, successNanos, account.LastError,
				account.AuthInvalidationGeneration, generation,
			); err != nil {
				return err
			}
		}
		for _, sessionKey := range sortedSessionKeys(router.Sessions) {
			session := router.Sessions[sessionKey]
			seconds, nanos, err := accountRouterRequiredTimeValues(session.UpdatedAt)
			if err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO account_router_sessions (
                router_name, session_key, config_hash, updated_at_unix_seconds,
                updated_at_nanosecond, touched_generation, version
            ) VALUES (?, ?, ?, ?, ?, ?, 1)
            ON CONFLICT(router_name, session_key) DO UPDATE SET
                config_hash = excluded.config_hash,
                updated_at_unix_seconds = excluded.updated_at_unix_seconds,
                updated_at_nanosecond = excluded.updated_at_nanosecond,
                touched_generation = excluded.touched_generation,
                version = account_router_sessions.version + 1`,
				routerName, sessionKey, session.ConfigHash, seconds, nanos, generation,
			); err != nil {
				return err
			}
			for _, blockID := range sortedAffinityBlockIDs(session.Blocks) {
				affinity := session.Blocks[blockID]
				selectedSeconds, selectedNanos, err := accountRouterRequiredTimeValues(affinity.SelectedAt)
				if err != nil {
					return err
				}
				if _, err := conn.ExecContext(ctx, `INSERT INTO account_router_session_affinities (
                    router_name, session_key, block_id, account_ref, select_reason,
                    selected_at_unix_seconds, selected_at_nanosecond, touched_generation, version
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)
                ON CONFLICT(router_name, session_key, block_id) DO UPDATE SET
                    account_ref = excluded.account_ref,
                    select_reason = excluded.select_reason,
                    selected_at_unix_seconds = excluded.selected_at_unix_seconds,
                    selected_at_nanosecond = excluded.selected_at_nanosecond,
                    touched_generation = excluded.touched_generation,
                    version = account_router_session_affinities.version + 1`,
					routerName, sessionKey, blockID, affinity.Account, string(affinity.Reason),
					selectedSeconds, selectedNanos, generation,
				); err != nil {
					return err
				}
			}
		}
		for _, blockID := range sortedBlockCursorIDs(router.Blocks) {
			block := router.Blocks[blockID]
			seconds, nanos, err := accountRouterTimeValues(block.UpdatedAt)
			if err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO account_router_block_cursors (
                router_name, block_id, cursor, updated_at_unix_seconds,
                updated_at_nanosecond, touched_generation, version
            ) VALUES (?, ?, ?, ?, ?, ?, 1)
            ON CONFLICT(router_name, block_id) DO UPDATE SET
                cursor = excluded.cursor,
                updated_at_unix_seconds = excluded.updated_at_unix_seconds,
                updated_at_nanosecond = excluded.updated_at_nanosecond,
                touched_generation = excluded.touched_generation,
                version = account_router_block_cursors.version + 1`,
				routerName, blockID, block.Cursor, seconds, nanos, generation,
			); err != nil {
				return err
			}
		}
	}
	for _, table := range []string{
		"account_router_session_affinities",
		"account_router_sessions",
		"account_router_accounts",
		"account_router_block_cursors",
		"account_router_routers",
	} {
		if _, err := conn.ExecContext(
			ctx, `DELETE FROM `+table+` WHERE touched_generation <> ?`, generation,
		); err != nil {
			return err
		}
	}
	return nil
}

//nolint:gocognit // Bounded graph validation is intentionally centralized.
func validateAccountRouterState(state *State) error {
	if state == nil || len(state.Routers) > maximumAccountRouterRouters {
		return errors.New("account-router state exceeds its router limit")
	}
	var accountCount, sessionCount, affinityCount, blockCount int
	var aggregate int64
	for routerName, router := range state.Routers {
		if router == nil || validateAccountRouterText(routerName, maximumAccountRouterIdentityBytes, true) != nil ||
			validateAccountRouterText(router.ConfigHash, 256, true) != nil ||
			accountRouterValidateTime(router.UpdatedAt, true) != nil {
			return errors.New("account-router router state is invalid")
		}
		aggregate += int64(len(routerName) + len(router.ConfigHash))
		accountCount += len(router.Accounts)
		sessionCount += len(router.Sessions)
		blockCount += len(router.Blocks)
		for accountRef, account := range router.Accounts {
			if err := validateAccountRouterAccountState(accountRef, account); err != nil {
				return err
			}
			aggregate += int64(len(accountRef) + len(account.LastError) + len(account.AuthInvalidationGeneration))
		}
		for sessionKey, session := range router.Sessions {
			if err := validateAccountRouterSessionState(sessionKey, session); err != nil {
				return err
			}
			affinityCount += len(session.Blocks)
			aggregate += int64(len(sessionKey) + len(session.ConfigHash))
			for blockID, affinity := range session.Blocks {
				if err := validateAccountRouterAffinity(blockID, affinity); err != nil {
					return err
				}
				aggregate += int64(len(blockID) + len(affinity.Account))
			}
		}
		for blockID, block := range router.Blocks {
			if block == nil || block.Cursor < 0 ||
				validateAccountRouterText(blockID, maximumAccountRouterIdentityBytes, true) != nil ||
				accountRouterValidateTime(block.UpdatedAt, true) != nil {
				return errors.New("account-router block cursor is invalid")
			}
			aggregate += int64(len(blockID))
		}
	}
	if accountCount > maximumAccountRouterAccounts || sessionCount > maximumAccountRouterSessions ||
		affinityCount > maximumAccountRouterAffinities || blockCount > maximumAccountRouterBlockCursors ||
		aggregate > maximumAccountRouterAggregateBytes {
		return errors.New("account-router state exceeds its aggregate bounds")
	}
	return nil
}

func validateAccountRouterSessionState(sessionKey string, session *SessionState) error {
	if session == nil ||
		validateAccountRouterText(sessionKey, maximumAccountRouterIdentityBytes, true) != nil ||
		validateAccountRouterText(session.ConfigHash, 256, true) != nil ||
		accountRouterValidateTime(session.UpdatedAt, false) != nil {
		return errors.New("account-router session state is invalid")
	}
	return nil
}

func validateAccountRouterAffinity(blockID string, affinity BlockAffinity) error {
	if validateAccountRouterText(blockID, maximumAccountRouterIdentityBytes, true) != nil ||
		validateAccountRouterText(affinity.Account, maximumAccountRouterIdentityBytes, true) != nil ||
		!validAccountRouterSelectReason(affinity.Reason) ||
		accountRouterValidateTime(affinity.SelectedAt, false) != nil {
		return errors.New("account-router affinity is invalid")
	}
	return nil
}

func validateAccountRouterAccountState(accountRef string, account *AccountState) error {
	if account == nil ||
		validateAccountRouterText(accountRef, maximumAccountRouterIdentityBytes, true) != nil {
		return errors.New("account-router account state is invalid")
	}
	if account.State != "operational" && account.State != "unavailable" {
		return errors.New("account-router account health state is invalid")
	}
	if !validAccountRouterFailureReason(account.Reason) || account.FailureCount < 0 ||
		account.Requests < 0 || account.RateWindowReqs < 0 || account.PromptTokens < 0 ||
		account.CompletionTokens < 0 || account.TotalTokens < 0 {
		return errors.New("account-router account counters are invalid")
	}
	if validateAccountRouterText(account.LastError, maximumAccountRouterErrorBytes, false) != nil ||
		validateAccountRouterText(account.AuthInvalidationGeneration, 256, false) != nil {
		return errors.New("account-router account diagnostics are invalid")
	}
	for _, timestamp := range []time.Time{
		account.RateWindowStart,
		account.UnavailableUntil,
		account.LastFailureAt,
		account.LastSuccessAt,
	} {
		if accountRouterValidateTime(timestamp, true) != nil {
			return errors.New("account-router account timestamp is invalid")
		}
	}
	return nil
}

func validAccountRouterFailureReason(reason providers.FailoverReason) bool {
	switch reason {
	case "", providers.FailoverAuth, providers.FailoverRateLimit, providers.FailoverBilling,
		providers.FailoverNetwork, providers.FailoverTimeout, providers.FailoverFormat,
		providers.FailoverSafetyFilter, providers.FailoverContextOverflow,
		providers.FailoverOverloaded, providers.FailoverUnknown:
		return true
	default:
		return false
	}
}

func validAccountRouterSelectReason(reason SelectReason) bool {
	return reason == SelectReasonInitial || reason == SelectReasonCompression || reason == SelectReasonUnhealthy
}

func validateAccountRouterText(value string, maximum int, required bool) error {
	if (required && value == "") || !utf8.ValidString(value) ||
		strings.ContainsRune(value, '\x00') || len(value) > maximum {
		return errors.New("account-router text value is invalid")
	}
	return nil
}

func accountRouterValidateTime(value time.Time, allowZero bool) error {
	if value.IsZero() {
		if allowZero {
			return nil
		}
		return errors.New("account-router timestamp is required")
	}
	if value.Year() < 0 || value.Year() > 9999 {
		return errors.New("account-router timestamp is outside the supported range")
	}
	return nil
}

func accountRouterTimeValues(value time.Time) (any, any, error) {
	if value.IsZero() {
		return nil, nil, nil
	}
	seconds, nanos, err := accountRouterRequiredTimeValues(value)
	return seconds, nanos, err
}

func accountRouterRequiredTimeValues(value time.Time) (int64, int64, error) {
	if err := accountRouterValidateTime(value, false); err != nil {
		return 0, 0, err
	}
	return value.Unix(), int64(value.Nanosecond()), nil
}

func accountRouterScannedTime(seconds, nanos sql.NullInt64) (time.Time, error) {
	if seconds.Valid != nanos.Valid {
		return time.Time{}, errors.New("account-router timestamp columns are inconsistent")
	}
	if !seconds.Valid {
		return time.Time{}, nil
	}
	return time.Unix(seconds.Int64, nanos.Int64).UTC(), nil
}

func (s *Store) applyCredentialAuthInvalidations(
	ctx context.Context,
	queryer accountRouterQueryer,
	state *State,
) error {
	if state == nil {
		return nil
	}
	rows, err := queryer.QueryContext(ctx, `SELECT credential_id, generation
        FROM account_router_auth_invalidations ORDER BY credential_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	markers := make(map[string]string)
	for rows.Next() {
		var credentialID, generation string
		if err := rows.Scan(&credentialID, &generation); err != nil {
			return err
		}
		markers[credentialID] = generation
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, router := range state.Routers {
		if router == nil {
			continue
		}
		for accountRef, accountState := range router.Accounts {
			if accountState == nil {
				continue
			}
			credentialID, ok := normalizedCredentialIDForAccount(accountRef)
			if !ok {
				continue
			}
			generation := markers[credentialID]
			if generation == "" || accountState.AuthInvalidationGeneration == generation {
				continue
			}
			if accountState.Reason == providers.FailoverAuth {
				resetAccountAuthFailure(accountState)
			}
			accountState.AuthInvalidationGeneration = generation
		}
	}
	return nil
}

func getStore(locator string) (*Store, error) {
	if !database.BrokerAuthorityHeld() && !database.MigrationFenceHeld() &&
		!database.ProviderTestAuthorityHeld() &&
		!allowUnfencedAccountRouterProviderForTests.Load() {
		return nil, database.NewError(
			database.CodeUnauthorized,
			"account-router local store requires database owner fencing",
		)
	}
	paths, err := resolveAccountRouterStorePaths(locator)
	if err != nil {
		return nil, err
	}
	if value, ok := stores.Load(paths.databasePath); ok {
		store := value.(*Store)
		return store, store.initErr
	}
	store := &Store{
		path:           paths.databasePath,
		sourceRoot:     paths.sourceRoot,
		sourceRelative: paths.sourceRelative,
		archiveRoot:    paths.archiveRoot,
		st:             State{Version: stateVersion, Routers: map[string]*RouterState{}},
		now:            time.Now,
	}
	store.initErr = store.refresh()
	actual, loaded := stores.LoadOrStore(paths.databasePath, store)
	if loaded {
		store = actual.(*Store)
	}
	return store, store.initErr
}

func (s *Store) refresh() error {
	if s == nil {
		return errors.New("account-router store is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	db, unlock, err := s.open(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	defer s.closeOpened(db)
	state, err := loadAccountRouterState(context.Background(), db)
	if err != nil {
		return err
	}
	s.st = cloneAccountRouterState(state)
	return nil
}

func (s *Store) update(fn func(*State)) error {
	if s == nil || fn == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.initErr != nil {
		return s.initErr
	}
	ctx := context.Background()
	db, unlock, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	defer s.closeOpened(db)
	var committed State
	err = sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		state, loadErr := loadAccountRouterState(ctx, conn)
		if loadErr != nil {
			return loadErr
		}
		if invalidationErr := s.applyCredentialAuthInvalidations(ctx, conn, &state); invalidationErr != nil {
			return invalidationErr
		}
		fn(&state)
		if invalidationErr := s.applyCredentialAuthInvalidations(ctx, conn, &state); invalidationErr != nil {
			return invalidationErr
		}
		if writeErr := writeAccountRouterState(ctx, conn, &state); writeErr != nil {
			return writeErr
		}
		committed = cloneAccountRouterState(state)
		return nil
	})
	if err != nil {
		return err
	}
	s.st = committed
	return nil
}

// InvalidateCredentialAuthFailure records a new credential generation in the
// same SQLite authority consumed by router health updates.
func InvalidateCredentialAuthFailure(statePath, credentialID string) error {
	if database.RuntimeClient() != nil {
		return InvalidateCredentialAuthFailureForStore(AccountRoutingStoreID, credentialID)
	}
	if !database.ProviderTestAuthorityHeld() && !allowUnfencedAccountRouterProviderForTests.Load() {
		return database.NewError(
			database.CodeUnavailable,
			"account-router database broker client is unavailable",
		)
	}
	if _, err := normalizeCredentialID(credentialID); err != nil {
		return err
	}
	paths, err := resolveAccountRouterStorePaths(statePath)
	if err != nil {
		return err
	}
	legacyPath := filepath.Join(paths.sourceRoot, filepath.FromSlash(paths.sourceRelative))
	databaseExists, err := accountRouterPathExists(paths.databasePath)
	if err != nil {
		return fmt.Errorf("stat account router database: %w", err)
	}
	legacyExists, err := accountRouterPathExists(legacyPath)
	if err != nil {
		return fmt.Errorf("stat account router legacy state: %w", err)
	}
	if !databaseExists && !legacyExists {
		return nil
	}
	store, err := getStore(statePath)
	if err != nil {
		return err
	}
	return invalidateCredentialAuthFailureStore(store, credentialID)
}

// InvalidateCredentialAuthFailureForWorkspace invalidates credential health in
// the workspace's opaque account-routing store. Runtime callers never derive a
// physical database path.
func InvalidateCredentialAuthFailureForWorkspace(workspace, credentialID string) error {
	if database.RuntimeClient() != nil {
		return InvalidateCredentialAuthFailureForStore(AccountRoutingStoreID, credentialID)
	}
	if !database.ProviderTestAuthorityHeld() && !allowUnfencedAccountRouterProviderForTests.Load() {
		return database.NewError(
			database.CodeUnavailable,
			"account-router database broker client is unavailable",
		)
	}
	return InvalidateCredentialAuthFailure(databasePath(workspace), credentialID)
}

func invalidateCredentialAuthFailureStore(store *Store, credentialID string) error {
	if store == nil {
		return errors.New("account-router store is unavailable")
	}
	normalizedCredentialID, err := normalizeCredentialID(credentialID)
	if err != nil {
		return err
	}
	generationBytes := make([]byte, 16)
	if _, randomErr := accountRouterRandomRead(generationBytes); randomErr != nil {
		return fmt.Errorf("generate account router auth invalidation: %w", randomErr)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	ctx := context.Background()
	db, unlock, err := store.open(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	defer store.closeOpened(db)
	now := store.now().UTC()
	seconds, nanos, err := accountRouterRequiredTimeValues(now)
	if err != nil {
		return err
	}
	return sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		var count int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*)
            FROM account_router_auth_invalidations`).Scan(&count); err != nil {
			return err
		}
		var exists int
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS(
            SELECT 1 FROM account_router_auth_invalidations WHERE credential_id = ?
        )`, normalizedCredentialID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 && count >= maximumAccountRouterInvalidations {
			return errors.New("account-router invalidation count exceeds its limit")
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO account_router_auth_invalidations (
            credential_id, generation, created_at_unix_seconds, created_at_nanosecond, version
        ) VALUES (?, ?, ?, ?, 1)
        ON CONFLICT(credential_id) DO UPDATE SET
            generation = excluded.generation,
            created_at_unix_seconds = excluded.created_at_unix_seconds,
            created_at_nanosecond = excluded.created_at_nanosecond,
            version = account_router_auth_invalidations.version + 1`,
			normalizedCredentialID, hex.EncodeToString(generationBytes), seconds, nanos,
		)
		return err
	})
}

func accountRouterPathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func cloneAccountRouterState(state State) State {
	clone := State{Version: state.Version, Routers: make(map[string]*RouterState, len(state.Routers))}
	for name, router := range state.Routers {
		if router == nil {
			clone.Routers[name] = nil
			continue
		}
		routerClone := &RouterState{
			ConfigHash: router.ConfigHash,
			Accounts:   make(map[string]*AccountState, len(router.Accounts)),
			Sessions:   make(map[string]*SessionState, len(router.Sessions)),
			Blocks:     make(map[string]*BlockRunState, len(router.Blocks)),
			UpdatedAt:  router.UpdatedAt,
		}
		for account, accountState := range router.Accounts {
			if accountState != nil {
				clonedAccount := *accountState
				routerClone.Accounts[account] = &clonedAccount
			}
		}
		for sessionKey, session := range router.Sessions {
			if session == nil {
				continue
			}
			sessionClone := &SessionState{
				ConfigHash: session.ConfigHash,
				Blocks:     make(map[string]BlockAffinity, len(session.Blocks)),
				UpdatedAt:  session.UpdatedAt,
			}
			for blockID, affinity := range session.Blocks {
				sessionClone.Blocks[blockID] = affinity
			}
			routerClone.Sessions[sessionKey] = sessionClone
		}
		for blockID, cursor := range router.Blocks {
			if cursor != nil {
				clonedCursor := *cursor
				routerClone.Blocks[blockID] = &clonedCursor
			}
		}
		clone.Routers[name] = routerClone
	}
	return clone
}

func sortedRouterNames(values map[string]*RouterState) []string {
	return sortedAccountRouterKeys(values)
}

func sortedAccountNames(values map[string]*AccountState) []string {
	return sortedAccountRouterKeys(values)
}

func sortedSessionKeys(values map[string]*SessionState) []string {
	return sortedAccountRouterKeys(values)
}

func sortedAffinityBlockIDs(values map[string]BlockAffinity) []string {
	return sortedAccountRouterKeys(values)
}

func sortedBlockCursorIDs(values map[string]*BlockRunState) []string {
	return sortedAccountRouterKeys(values)
}

func sortedAccountRouterKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
