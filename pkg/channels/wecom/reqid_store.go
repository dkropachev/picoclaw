package wecom

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const (
	wecomReqIDDatabaseFilename   = "reqid-store.db"
	wecomReqIDLegacyFilename     = "reqid-store.json"
	wecomReqIDComponent          = "wecom-reqid"
	wecomReqIDLegacySourceID     = "reqid-json-v1"
	wecomReqIDLegacyArchiveLabel = "wecom-reqid-v1"
	wecomReqIDLegacyMaxBytes     = int64(16 << 20)
	wecomReqIDMaxValueBytes      = 64 << 10
	wecomReqIDMaxRoutes          = 10000
	wecomReqIDLockShards         = 64
)

var errWecomLegacyRouteLimit = errors.New("legacy WeCom route count exceeds its limit")

const wecomRoutesSchema = `CREATE TABLE wecom_request_routes (
    chat_id                    TEXT PRIMARY KEY,
    request_id                 TEXT NOT NULL,
    chat_type                  INTEGER NOT NULL CHECK(chat_type BETWEEN 0 AND 4294967295),
    expires_at_unix_seconds    INTEGER,
    expires_at_nanosecond      INTEGER,
    version                    INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    CHECK(chat_id <> ''),
    CHECK(request_id <> ''),
    CHECK(length(CAST(chat_id AS BLOB)) <= 65536),
    CHECK(length(CAST(request_id AS BLOB)) <= 65536),
    CHECK(instr(chat_id, char(0)) = 0),
    CHECK(instr(request_id, char(0)) = 0),
    CHECK(
        (expires_at_unix_seconds IS NULL AND expires_at_nanosecond IS NULL)
        OR (
            expires_at_unix_seconds BETWEEN -62167219200 AND 253402300799
            AND expires_at_nanosecond IS NOT NULL
            AND expires_at_nanosecond BETWEEN 0 AND 999999999
        )
    )
) STRICT`

const wecomRoutesExpiryIndexSchema = `CREATE INDEX wecom_request_routes_expiry_idx
    ON wecom_request_routes(expires_at_unix_seconds, expires_at_nanosecond, chat_id)`

const selectWecomRouteSQL = `SELECT
    request_id, chat_id, chat_type,
    expires_at_unix_seconds, expires_at_nanosecond, version
FROM wecom_request_routes`

type wecomRoute struct {
	ReqID     string    `json:"req_id"`
	ChatID    string    `json:"chat_id"`
	ChatType  uint32    `json:"chat_type"`
	ExpiresAt time.Time `json:"expires_at"`
}

type reqIDStore struct {
	path           string
	sourceRoot     string
	sourceRelative string
	archiveRoot    string
	now            func() time.Time
	initErr        error
}

type wecomLegacyRoute struct {
	key string
	raw json.RawMessage
}

var wecomReqIDLocks [wecomReqIDLockShards]sync.Mutex

func newReqIDStore(path string) *reqIDStore {
	databasePath, sourceRoot, sourceRelative, archiveRoot, err := resolveReqIDStorePaths(path)
	store := &reqIDStore{
		path:           databasePath,
		sourceRoot:     sourceRoot,
		sourceRelative: sourceRelative,
		archiveRoot:    archiveRoot,
		now:            time.Now,
		initErr:        err,
	}
	if err == nil {
		db, unlock, openErr := store.open(context.Background())
		if openErr == nil {
			openErr = db.Close()
			unlock()
		}
		store.initErr = openErr
	}
	return store
}

func defaultReqIDStorePath() string {
	return filepath.Join(config.GetHome(), "channels", "wecom", wecomReqIDDatabaseFilename)
}

func resolveReqIDStorePaths(path string) (string, string, string, string, error) {
	if strings.ContainsRune(path, '\x00') {
		return "", "", "", "", errors.New("WeCom request-route path contains a NUL byte")
	}
	if path == "" {
		home, err := filepath.Abs(filepath.Clean(config.GetHome()))
		if err != nil {
			return "", "", "", "", err
		}
		databasePath, err := filepath.Abs(defaultReqIDStorePath())
		if err != nil {
			return "", "", "", "", err
		}
		return databasePath,
			home,
			filepath.ToSlash(filepath.Join("wecom", wecomReqIDLegacyFilename)),
			filepath.Join(home, "legacy-json", wecomReqIDLegacyArchiveLabel),
			nil
	}
	if strings.TrimSpace(path) == "" {
		return "", "", "", "", errors.New("WeCom request-route path is invalid")
	}
	resolved, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", "", "", "", err
	}
	extension := strings.ToLower(filepath.Ext(resolved))
	legacyPath := resolved
	databasePath := resolved
	if extension == ".json" {
		databasePath = strings.TrimSuffix(resolved, filepath.Ext(resolved)) + ".db"
	} else if extension == "" {
		databasePath = resolved + ".db"
	} else {
		legacyPath = strings.TrimSuffix(resolved, filepath.Ext(resolved)) + ".json"
	}
	sourceRoot := filepath.Dir(legacyPath)
	return databasePath,
		sourceRoot,
		filepath.Base(legacyPath),
		filepath.Join(sourceRoot, "legacy-json", wecomReqIDLegacyArchiveLabel),
		nil
}

func (s *reqIDStore) initializationError() error {
	if s == nil {
		return errors.New("WeCom request-route store is unavailable")
	}
	return s.initErr
}

func (s *reqIDStore) Put(chatID, reqID string, chatType uint32, ttl time.Duration) error {
	if reqID == "" || chatID == "" {
		return nil
	}
	if err := validateWecomRouteValue(chatID); err != nil {
		return err
	}
	if err := validateWecomRouteValue(reqID); err != nil {
		return err
	}
	now := s.now().UTC()
	expiresAt := now.Add(ttl)
	seconds, nanoseconds, err := wecomTimestampValues(expiresAt)
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, unlock, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	defer db.Close()
	return sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		if pruneErr := deleteExpiredWecomRoutes(ctx, conn, now); pruneErr != nil {
			return pruneErr
		}
		if capacityErr := ensureWecomRouteCapacity(ctx, conn, chatID); capacityErr != nil {
			return capacityErr
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO wecom_request_routes (
            chat_id, request_id, chat_type,
            expires_at_unix_seconds, expires_at_nanosecond, version
        ) VALUES (?, ?, ?, ?, ?, 1)
        ON CONFLICT(chat_id) DO UPDATE SET
            request_id = excluded.request_id,
            chat_type = excluded.chat_type,
            expires_at_unix_seconds = excluded.expires_at_unix_seconds,
            expires_at_nanosecond = excluded.expires_at_nanosecond,
            version = wecom_request_routes.version + 1`,
			chatID,
			reqID,
			int64(chatType),
			seconds,
			nanoseconds,
		)
		return err
	})
}

func (s *reqIDStore) Get(chatID string) (wecomRoute, bool) {
	if s == nil || validateWecomRouteValue(chatID) != nil {
		return wecomRoute{}, false
	}
	ctx := context.Background()
	db, unlock, err := s.open(ctx)
	if err != nil {
		s.logError("Failed to open WeCom request-route store", err)
		return wecomRoute{}, false
	}
	defer unlock()
	defer db.Close()
	var route wecomRoute
	err = sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		now := s.now().UTC()
		if pruneErr := deleteExpiredWecomRoutes(ctx, conn, now); pruneErr != nil {
			return pruneErr
		}
		var scanErr error
		route, _, scanErr = scanWecomRoute(conn.QueryRowContext(
			ctx,
			selectWecomRouteSQL+" WHERE chat_id = ?",
			chatID,
		))
		return scanErr
	})
	if errors.Is(err, sql.ErrNoRows) {
		return wecomRoute{}, false
	}
	if err != nil {
		s.logError("Failed to load WeCom request route", err)
		return wecomRoute{}, false
	}
	return route, true
}

func (s *reqIDStore) Delete(chatID string) error {
	if chatID == "" {
		return nil
	}
	if err := validateWecomRouteValue(chatID); err != nil {
		return err
	}
	ctx := context.Background()
	db, unlock, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	defer db.Close()
	return sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(
			ctx,
			`DELETE FROM wecom_request_routes WHERE chat_id = ?`,
			chatID,
		)
		return err
	})
}

// load remains a source-compatible facade. Opening validates, imports, and
// archives legacy state; individual reads remain query based.
func (s *reqIDStore) load() error {
	db, unlock, err := s.open(context.Background())
	if err != nil {
		return err
	}
	defer unlock()
	return db.Close()
}

func (s *reqIDStore) open(ctx context.Context) (*sql.DB, func(), error) {
	if s == nil || s.initErr != nil {
		if s != nil && s.initErr != nil {
			return nil, nil, s.initErr
		}
		return nil, nil, errors.New("WeCom request-route store is unavailable")
	}
	lock := wecomReqIDLocalLock(s.path)
	lock.Lock()
	unlockFile, err := lockWecomReqIDDatabase(s.path)
	if err != nil {
		lock.Unlock()
		return nil, nil, err
	}
	unlock := func() {
		unlockFile()
		lock.Unlock()
	}
	db, err := sqlitestore.Open(ctx, s.path, s.options())
	if err != nil {
		unlock()
		return nil, nil, err
	}
	return db, unlock, nil
}

func (s *reqIDStore) options() sqlitestore.Options {
	return sqlitestore.Options{
		Component: wecomReqIDComponent,
		Migrations: []sqlitestore.Migration{{
			Version: 1,
			Statements: []string{
				wecomRoutesSchema,
				wecomRoutesExpiryIndexSchema,
			},
		}},
		Validate: validateWecomRouteSchema,
		Legacy: &sqlitestore.LegacyOptions{
			SourceRoot:  s.sourceRoot,
			ArchiveRoot: s.archiveRoot,
			Sources: func() ([]sqlitestore.LegacySource, error) {
				if _, err := os.Lstat(filepath.Join(
					s.sourceRoot,
					filepath.FromSlash(s.sourceRelative),
				)); os.IsNotExist(err) {
					return nil, nil
				} else if err != nil {
					return nil, err
				}
				return []sqlitestore.LegacySource{{
					ID:       wecomReqIDLegacySourceID,
					Relative: s.sourceRelative,
					MaxBytes: wecomReqIDLegacyMaxBytes,
				}}, nil
			},
			Import:   s.importLegacy,
			MaxBytes: wecomReqIDLegacyMaxBytes,
		},
	}
}

func validateWecomRouteSchema(ctx context.Context, conn *sql.Conn) error {
	for _, object := range []struct {
		objectType string
		name       string
		schema     string
	}{
		{objectType: "table", name: "wecom_request_routes", schema: wecomRoutesSchema},
		{objectType: "index", name: "wecom_request_routes_expiry_idx", schema: wecomRoutesExpiryIndexSchema},
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
	if err := sqlitestore.ValidateUniqueIndexSet(ctx, conn, "wecom_request_routes"); err != nil {
		return err
	}
	var unexpected int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
        WHERE name NOT LIKE 'sqlite_%'
          AND name NOT IN (
              'wecom_request_routes',
              'wecom_request_routes_expiry_idx',
              'storage_imports',
              'storage_import_issues',
              'storage_import_horizons',
              'storage_imports_archive_status_idx'
          )`).Scan(&unexpected); err != nil {
		return err
	}
	if unexpected != 0 {
		return errors.New("WeCom request-route schema has unexpected objects")
	}
	var routes int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM wecom_request_routes`).Scan(
		&routes,
	); err != nil {
		return err
	}
	if routes > wecomReqIDMaxRoutes {
		return errors.New("WeCom request-route row count exceeds its limit")
	}
	return nil
}

func (s *reqIDStore) importLegacy(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	routes, overLimit, valid := decodeLegacyWecomRoutesForImport(input.Data)
	if overLimit {
		return sqlitestore.ImportResult{}, errWecomLegacyRouteLimit
	}
	if !valid {
		return sqlitestore.ImportResult{
			Skipped: 1,
			Issues: []sqlitestore.ImportIssue{{
				Code:         "malformed-json",
				RecordDigest: input.Digest,
			}},
		}, nil
	}
	sort.SliceStable(routes, func(left, right int) bool { return routes[left].key < routes[right].key })
	var routeCount int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM wecom_request_routes`).Scan(
		&routeCount,
	); err != nil {
		return sqlitestore.ImportResult{}, err
	}
	if routeCount > wecomReqIDMaxRoutes {
		return sqlitestore.ImportResult{}, errors.New("WeCom request-route row count exceeds its limit")
	}
	seen := make(map[string]struct{}, len(routes))
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
	now := s.now().UTC()
	for _, source := range routes {
		digest := sha256.Sum256(source.raw)
		if _, exists := seen[source.key]; exists {
			addIssue("identity-conflict", digest)
			continue
		}
		var route wecomRoute
		if err := json.Unmarshal(source.raw, &route); err != nil {
			addIssue("invalid-route", digest)
			continue
		}
		route.ChatID = source.key
		if validateWecomRoute(route) != nil {
			addIssue("invalid-route", digest)
			continue
		}
		if !route.ExpiresAt.IsZero() && now.After(route.ExpiresAt) {
			addIssue("expired-route", digest)
			continue
		}
		seconds, nanoseconds, timestampErr := wecomNullableTimestampValues(route.ExpiresAt)
		if timestampErr != nil {
			addIssue("invalid-route", digest)
			continue
		}
		var exists int
		if err := conn.QueryRowContext(
			ctx,
			`SELECT EXISTS(SELECT 1 FROM wecom_request_routes WHERE chat_id = ?)`,
			route.ChatID,
		).Scan(&exists); err != nil {
			return sqlitestore.ImportResult{}, err
		}
		if exists == 0 && routeCount >= wecomReqIDMaxRoutes {
			return sqlitestore.ImportResult{}, errors.New("WeCom request-route row count exceeds its limit")
		}
		executionResult, err := conn.ExecContext(ctx, `INSERT INTO wecom_request_routes (
            chat_id, request_id, chat_type,
            expires_at_unix_seconds, expires_at_nanosecond, version
        ) VALUES (?, ?, ?, ?, ?, 1)
        ON CONFLICT(chat_id) DO NOTHING`,
			route.ChatID,
			route.ReqID,
			int64(route.ChatType),
			seconds,
			nanoseconds,
		)
		if err != nil {
			return sqlitestore.ImportResult{}, err
		}
		inserted, err := executionResult.RowsAffected()
		if err != nil {
			return sqlitestore.ImportResult{}, err
		}
		seen[source.key] = struct{}{}
		if inserted == 1 {
			result.Imported++
			routeCount++
		} else {
			addIssue("sqlite-authoritative", digest)
		}
	}
	return result, nil
}

func decodeLegacyWecomRoutesForImport(data []byte) ([]wecomLegacyRoute, bool, bool) {
	routes, err := decodeLegacyWecomRoutes(data)
	return routes, errors.Is(err, errWecomLegacyRouteLimit), err == nil
}

func decodeLegacyWecomRoutes(data []byte) ([]wecomLegacyRoute, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, errors.New("legacy WeCom routes are not an object")
	}
	var routes []wecomLegacyRoute
	for decoder.More() {
		if len(routes) >= wecomReqIDMaxRoutes {
			return nil, errWecomLegacyRouteLimit
		}
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("legacy WeCom route key is invalid")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		routes = append(routes, wecomLegacyRoute{key: key, raw: raw})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("legacy WeCom routes have trailing JSON")
		}
		return nil, err
	}
	return routes, nil
}

func validateWecomRoute(route wecomRoute) error {
	if err := validateWecomRouteValue(route.ChatID); err != nil {
		return err
	}
	if route.ReqID == "" {
		return errors.New("WeCom request ID is required")
	}
	if err := validateWecomRouteValue(route.ReqID); err != nil {
		return err
	}
	_, _, err := wecomNullableTimestampValues(route.ExpiresAt)
	return err
}

func validateWecomRouteValue(value string) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return errors.New("WeCom route value is invalid")
	}
	if len(value) > wecomReqIDMaxValueBytes {
		return errors.New("WeCom route value exceeds its size limit")
	}
	return nil
}

func wecomNullableTimestampValues(timestamp time.Time) (any, any, error) {
	if timestamp.IsZero() {
		return nil, nil, nil
	}
	seconds, nanoseconds, err := wecomTimestampValues(timestamp)
	return seconds, nanoseconds, err
}

func wecomTimestampValues(timestamp time.Time) (int64, int64, error) {
	if timestamp.Year() < 0 || timestamp.Year() > 9999 {
		return 0, 0, errors.New("WeCom route timestamp is outside the supported range")
	}
	return timestamp.Unix(), int64(timestamp.Nanosecond()), nil
}

type wecomRouteScanner interface {
	Scan(dest ...any) error
}

func scanWecomRoute(scanner wecomRouteScanner) (wecomRoute, int64, error) {
	var (
		route       wecomRoute
		chatType    int64
		seconds     sql.NullInt64
		nanoseconds sql.NullInt64
		version     int64
	)
	if err := scanner.Scan(
		&route.ReqID,
		&route.ChatID,
		&chatType,
		&seconds,
		&nanoseconds,
		&version,
	); err != nil {
		return wecomRoute{}, 0, err
	}
	if chatType < 0 || chatType > int64(^uint32(0)) {
		return wecomRoute{}, 0, errors.New("stored WeCom chat type is invalid")
	}
	if seconds.Valid != nanoseconds.Valid {
		return wecomRoute{}, 0, errors.New("stored WeCom expiry columns are inconsistent")
	}
	route.ChatType = uint32(chatType)
	if seconds.Valid {
		route.ExpiresAt = time.Unix(seconds.Int64, nanoseconds.Int64).UTC()
	}
	return route, version, nil
}

func deleteExpiredWecomRoutes(ctx context.Context, conn *sql.Conn, now time.Time) error {
	_, err := conn.ExecContext(ctx, `DELETE FROM wecom_request_routes
        WHERE expires_at_unix_seconds IS NOT NULL
          AND (
              expires_at_unix_seconds < ?
              OR (expires_at_unix_seconds = ? AND expires_at_nanosecond < ?)
          )`, now.Unix(), now.Unix(), now.Nanosecond())
	return err
}

func ensureWecomRouteCapacity(ctx context.Context, conn *sql.Conn, chatID string) error {
	var exists int
	if err := conn.QueryRowContext(
		ctx,
		`SELECT EXISTS(SELECT 1 FROM wecom_request_routes WHERE chat_id = ?)`,
		chatID,
	).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return nil
	}
	var routes int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM wecom_request_routes`).Scan(
		&routes,
	); err != nil {
		return err
	}
	if routes >= wecomReqIDMaxRoutes {
		return errors.New("WeCom request-route row count exceeds its limit")
	}
	return nil
}

func wecomReqIDLocalLock(path string) *sync.Mutex {
	digest := sha256.Sum256([]byte(path))
	return &wecomReqIDLocks[uint32(digest[0])%wecomReqIDLockShards]
}

func (s *reqIDStore) logError(message string, err error) {
	logger.WarnCF("wecom", message, map[string]any{"error": err.Error()})
}
