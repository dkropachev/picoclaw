package weixin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const (
	weixinStateDatabaseFilename   = "state.db"
	weixinStateComponent          = "weixin-state"
	weixinStateLegacyArchiveLabel = "weixin-state-v1"
	weixinStateLegacyMaxBytes     = int64(16 << 20)
	weixinStateMaxValueBytes      = 64 << 10
	weixinStateMaxSources         = 10000
	weixinStateMaxTokens          = 10000
	weixinStateMaxAccounts        = 20000
	weixinStateMaxCursors         = 10000
	weixinStateMaxTokenRows       = 100000
	weixinStateLockShards         = 64

	weixinStateKindCursor = "cursor"
	weixinStateKindTokens = "tokens"
)

var errWeixinLegacyTokenLimit = errors.New("legacy Weixin context-token count exceeds its limit")

const weixinAccountsSchema = `CREATE TABLE weixin_accounts (
    account_key                 TEXT PRIMARY KEY,
    created_at_unix_seconds     INTEGER NOT NULL CHECK(created_at_unix_seconds BETWEEN -62167219200 AND 253402300799),
    created_at_nanosecond       INTEGER NOT NULL CHECK(created_at_nanosecond BETWEEN 0 AND 999999999),
    version                     INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    CHECK(account_key = 'default' OR (
        length(account_key) = 16
        AND account_key = lower(account_key)
        AND account_key NOT GLOB '*[^0-9a-f]*'
    ))
) STRICT`

const weixinCursorsSchema = `CREATE TABLE weixin_cursors (
    account_key                 TEXT PRIMARY KEY REFERENCES weixin_accounts(account_key) ON DELETE CASCADE,
    cursor_value                TEXT NOT NULL,
    updated_at_unix_seconds     INTEGER NOT NULL CHECK(updated_at_unix_seconds BETWEEN -62167219200 AND 253402300799),
    updated_at_nanosecond       INTEGER NOT NULL CHECK(updated_at_nanosecond BETWEEN 0 AND 999999999),
    version                     INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    CHECK(length(CAST(cursor_value AS BLOB)) <= 65536),
    CHECK(instr(cursor_value, char(0)) = 0)
) STRICT`

const weixinContextTokensSchema = `CREATE TABLE weixin_context_tokens (
    account_key                 TEXT NOT NULL REFERENCES weixin_accounts(account_key) ON DELETE CASCADE,
    user_id                     TEXT NOT NULL,
    context_token               TEXT NOT NULL,
    updated_at_unix_seconds     INTEGER NOT NULL CHECK(updated_at_unix_seconds BETWEEN -62167219200 AND 253402300799),
    updated_at_nanosecond       INTEGER NOT NULL CHECK(updated_at_nanosecond BETWEEN 0 AND 999999999),
    version                     INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
    PRIMARY KEY(account_key, user_id),
    CHECK(user_id <> ''),
    CHECK(length(CAST(user_id AS BLOB)) <= 65536),
    CHECK(length(CAST(context_token AS BLOB)) <= 65536),
    CHECK(instr(user_id, char(0)) = 0),
    CHECK(instr(context_token, char(0)) = 0)
) STRICT`

const weixinCursorsUpdatedIndexSchema = `CREATE INDEX weixin_cursors_updated_idx
    ON weixin_cursors(updated_at_unix_seconds, updated_at_nanosecond, account_key)`

const weixinTokensUpdatedIndexSchema = `CREATE INDEX weixin_context_tokens_updated_idx
    ON weixin_context_tokens(updated_at_unix_seconds, updated_at_nanosecond, account_key, user_id)`

type weixinStateStore struct {
	path       string
	root       string
	accountKey string
	legacyPath string
	legacyKind string
	canonical  bool
	now        func() time.Time
}

type weixinLegacyToken struct {
	userID string
	raw    json.RawMessage
	digest [sha256.Size]byte
}

var weixinStateLocks [weixinStateLockShards]sync.Mutex

func newWeixinStateStore(locator, kind string) (*weixinStateStore, error) {
	if kind != weixinStateKindCursor && kind != weixinStateKindTokens {
		return nil, errors.New("Weixin state kind is invalid")
	}
	if strings.ContainsRune(locator, '\x00') {
		return nil, errors.New("Weixin state path contains a NUL byte")
	}
	if strings.TrimSpace(locator) == "" {
		return nil, errors.New("Weixin state path is required")
	}
	resolved, err := filepath.Abs(filepath.Clean(locator))
	if err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(filepath.Base(resolved), filepath.Ext(resolved))
	directory := filepath.Base(filepath.Dir(resolved))
	if (directory == "sync" && kind == weixinStateKindCursor) ||
		(directory == "context-tokens" && kind == weixinStateKindTokens) {
		root := filepath.Dir(filepath.Dir(resolved))
		if !validWeixinAccountKey(base) {
			return nil, errors.New("Weixin account key is invalid")
		}
		return &weixinStateStore{
			path:       filepath.Join(root, weixinStateDatabaseFilename),
			root:       root,
			accountKey: base,
			canonical:  true,
			now:        time.Now,
		}, nil
	}
	databasePath := strings.TrimSuffix(resolved, filepath.Ext(resolved)) + ".db"
	legacyPath := resolved
	if databasePath == resolved {
		legacyPath = strings.TrimSuffix(resolved, filepath.Ext(resolved)) + ".json"
	}
	return &weixinStateStore{
		path:       databasePath,
		root:       filepath.Dir(resolved),
		accountKey: "default",
		legacyPath: legacyPath,
		legacyKind: kind,
		now:        time.Now,
	}, nil
}

func (s *weixinStateStore) loadCursor(ctx context.Context) (string, error) {
	db, unlock, err := s.open(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	defer db.Close()
	var cursor string
	err = db.QueryRowContext(
		ctx,
		`SELECT cursor_value FROM weixin_cursors WHERE account_key = ?`,
		s.accountKey,
	).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return cursor, err
}

func (s *weixinStateStore) saveCursor(ctx context.Context, cursor string) error {
	if err := validateWeixinStateValue(cursor, true); err != nil {
		return err
	}
	now := s.now().UTC()
	seconds, nanoseconds, err := weixinStateTimestampValues(now)
	if err != nil {
		return err
	}
	db, unlock, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	defer db.Close()
	return sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		if err := ensureWeixinAccount(ctx, conn, s.accountKey, seconds, nanoseconds); err != nil {
			return err
		}
		if err := ensureWeixinCursorCapacity(ctx, conn, s.accountKey); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO weixin_cursors (
            account_key, cursor_value, updated_at_unix_seconds, updated_at_nanosecond, version
        ) VALUES (?, ?, ?, ?, 1)
        ON CONFLICT(account_key) DO UPDATE SET
            cursor_value = excluded.cursor_value,
            updated_at_unix_seconds = excluded.updated_at_unix_seconds,
            updated_at_nanosecond = excluded.updated_at_nanosecond,
            version = weixin_cursors.version + 1`,
			s.accountKey,
			cursor,
			seconds,
			nanoseconds,
		)
		return err
	})
}

func (s *weixinStateStore) loadTokens(ctx context.Context) (map[string]string, error) {
	db, unlock, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT user_id, context_token
        FROM weixin_context_tokens WHERE account_key = ? ORDER BY user_id`, s.accountKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := make(map[string]string)
	for rows.Next() {
		var userID, token string
		if err := rows.Scan(&userID, &token); err != nil {
			return nil, err
		}
		tokens[userID] = token
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, nil
	}
	return tokens, nil
}

func (s *weixinStateStore) saveTokens(ctx context.Context, tokens map[string]string) error {
	if len(tokens) > weixinStateMaxTokens {
		return errors.New("Weixin context-token count exceeds its limit")
	}
	userIDs := make([]string, 0, len(tokens))
	for userID, token := range tokens {
		if err := validateWeixinStateValue(userID, false); err != nil {
			return err
		}
		if err := validateWeixinStateValue(token, true); err != nil {
			return err
		}
		userIDs = append(userIDs, userID)
	}
	sort.Strings(userIDs)
	now := s.now().UTC()
	seconds, nanoseconds, err := weixinStateTimestampValues(now)
	if err != nil {
		return err
	}
	db, unlock, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	defer db.Close()
	return sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		if err := ensureWeixinAccount(ctx, conn, s.accountKey, seconds, nanoseconds); err != nil {
			return err
		}
		if err := ensureWeixinTokenReplacementCapacity(
			ctx,
			conn,
			s.accountKey,
			len(userIDs),
		); err != nil {
			return err
		}
		// Delete absent identities before inserting new ones so even the private
		// transaction never transiently exceeds the global row cap.
		if err := deleteRemovedWeixinTokens(ctx, conn, s.accountKey, userIDs); err != nil {
			return err
		}
		for _, userID := range userIDs {
			if _, err := conn.ExecContext(ctx, `INSERT INTO weixin_context_tokens (
                account_key, user_id, context_token,
                updated_at_unix_seconds, updated_at_nanosecond, version
            ) VALUES (?, ?, ?, ?, ?, 1)
            ON CONFLICT(account_key, user_id) DO UPDATE SET
                context_token = excluded.context_token,
                updated_at_unix_seconds = excluded.updated_at_unix_seconds,
                updated_at_nanosecond = excluded.updated_at_nanosecond,
                version = weixin_context_tokens.version + 1`,
				s.accountKey,
				userID,
				tokens[userID],
				seconds,
				nanoseconds,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *weixinStateStore) saveToken(ctx context.Context, userID, token string) error {
	if err := validateWeixinStateValue(userID, false); err != nil {
		return err
	}
	if err := validateWeixinStateValue(token, true); err != nil {
		return err
	}
	now := s.now().UTC()
	seconds, nanoseconds, err := weixinStateTimestampValues(now)
	if err != nil {
		return err
	}
	db, unlock, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	defer db.Close()
	return sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		if err := ensureWeixinAccount(ctx, conn, s.accountKey, seconds, nanoseconds); err != nil {
			return err
		}
		if err := ensureWeixinTokenInsertCapacity(ctx, conn, s.accountKey, userID); err != nil {
			return err
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO weixin_context_tokens (
            account_key, user_id, context_token,
            updated_at_unix_seconds, updated_at_nanosecond, version
        ) VALUES (?, ?, ?, ?, ?, 1)
        ON CONFLICT(account_key, user_id) DO UPDATE SET
            context_token = excluded.context_token,
            updated_at_unix_seconds = excluded.updated_at_unix_seconds,
            updated_at_nanosecond = excluded.updated_at_nanosecond,
            version = weixin_context_tokens.version + 1`,
			s.accountKey,
			userID,
			token,
			seconds,
			nanoseconds,
		)
		return err
	})
}

func deleteRemovedWeixinTokens(
	ctx context.Context,
	conn *sql.Conn,
	accountKey string,
	retainedUserIDs []string,
) error {
	query := `DELETE FROM weixin_context_tokens WHERE account_key = ?`
	arguments := make([]any, 0, len(retainedUserIDs)+1)
	arguments = append(arguments, accountKey)
	if len(retainedUserIDs) != 0 {
		query += ` AND user_id NOT IN (` + strings.TrimSuffix(
			strings.Repeat("?,", len(retainedUserIDs)),
			",",
		) + `)`
		for _, userID := range retainedUserIDs {
			arguments = append(arguments, userID)
		}
	}
	_, err := conn.ExecContext(ctx, query, arguments...)
	return err
}

func (s *weixinStateStore) open(ctx context.Context) (*sql.DB, func(), error) {
	lock := weixinStateLocalLock(s.path)
	lock.Lock()
	unlockFile, err := lockWeixinStateDatabase(s.path)
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

func (s *weixinStateStore) options() sqlitestore.Options {
	return sqlitestore.Options{
		Component: weixinStateComponent,
		Migrations: []sqlitestore.Migration{{
			Version: 1,
			Statements: []string{
				weixinAccountsSchema,
				weixinCursorsSchema,
				weixinContextTokensSchema,
				weixinCursorsUpdatedIndexSchema,
				weixinTokensUpdatedIndexSchema,
			},
		}},
		Validate: validateWeixinStateSchema,
		Legacy: &sqlitestore.LegacyOptions{
			SourceRoot:    s.root,
			ArchiveRoot:   filepath.Join(s.root, "legacy-json", weixinStateLegacyArchiveLabel),
			Sources:       s.legacySources,
			Import:        s.importLegacy,
			MaxBytes:      weixinStateLegacyMaxBytes,
			MaxSources:    weixinStateMaxSources,
			MaxTotalBytes: weixinStateLegacyMaxBytes * 16,
		},
	}
}

func (s *weixinStateStore) legacySources() ([]sqlitestore.LegacySource, error) {
	if !s.canonical {
		return []sqlitestore.LegacySource{{
			ID:       weixinLegacySourceID(s.legacyKind, filepath.Base(s.legacyPath)),
			Relative: filepath.Base(s.legacyPath),
			MaxBytes: weixinStateLegacyMaxBytes,
		}}, nil
	}
	var sources []sqlitestore.LegacySource
	for _, item := range []struct {
		directory string
		kind      string
	}{
		{directory: "sync", kind: weixinStateKindCursor},
		{directory: "context-tokens", kind: weixinStateKindTokens},
	} {
		directoryPath := filepath.Join(s.root, item.directory)
		info, err := os.Lstat(directoryPath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
			info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 ||
			info.Mode().Perm()&0o022 != 0 {
			return nil, errors.New("Weixin legacy state directory is unsafe")
		}
		entries, err := os.ReadDir(directoryPath)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if strings.ToLower(filepath.Ext(entry.Name())) != ".json" {
				continue
			}
			relative := filepath.ToSlash(filepath.Join(item.directory, entry.Name()))
			sources = append(sources, sqlitestore.LegacySource{
				ID:       weixinLegacySourceID(item.kind, relative),
				Relative: relative,
				MaxBytes: weixinStateLegacyMaxBytes,
			})
		}
	}
	return sources, nil
}

func weixinLegacySourceID(kind, relative string) string {
	digest := sha256.Sum256([]byte(kind + "\x00" + filepath.ToSlash(relative)))
	return kind + "-" + hex.EncodeToString(digest[:8])
}

func (s *weixinStateStore) importLegacy(
	ctx context.Context,
	conn *sql.Conn,
	input sqlitestore.LegacyInput,
) (sqlitestore.ImportResult, error) {
	kind, accountKey, ok := parseWeixinLegacyRelative(
		input.Relative,
		s.legacyKind,
		s.accountKey,
	)
	if !ok {
		return skippedWeixinImport("invalid-account", input.Digest), nil
	}
	now := s.now().UTC()
	seconds, nanoseconds, err := weixinStateTimestampValues(now)
	if err != nil {
		return sqlitestore.ImportResult{}, err
	}
	switch kind {
	case weixinStateKindCursor:
		decoded, valid := decodeLegacyWeixinCursor(input.Data)
		if !valid || !validWeixinStateValue(decoded.GetUpdatesBuf, true) {
			return skippedWeixinImport("invalid-cursor", input.Digest), nil
		}
		if err := ensureWeixinAccount(ctx, conn, accountKey, seconds, nanoseconds); err != nil {
			return sqlitestore.ImportResult{}, err
		}
		if err := ensureWeixinCursorCapacity(ctx, conn, accountKey); err != nil {
			return sqlitestore.ImportResult{}, err
		}
		result, err := conn.ExecContext(ctx, `INSERT INTO weixin_cursors (
            account_key, cursor_value, updated_at_unix_seconds, updated_at_nanosecond, version
        ) VALUES (?, ?, ?, ?, 1) ON CONFLICT(account_key) DO NOTHING`,
			accountKey,
			decoded.GetUpdatesBuf,
			seconds,
			nanoseconds,
		)
		return weixinImportExecutionResult(result, err, input.Digest)
	case weixinStateKindTokens:
		tokens, overLimit, valid := decodeLegacyWeixinTokensForImport(input.Data)
		if overLimit {
			return sqlitestore.ImportResult{}, errWeixinLegacyTokenLimit
		}
		if !valid {
			return skippedWeixinImport("malformed-json", input.Digest), nil
		}
		return importLegacyWeixinTokens(
			ctx,
			conn,
			accountKey,
			tokens,
			seconds,
			nanoseconds,
		)
	default:
		return sqlitestore.ImportResult{}, errors.New("Weixin legacy state kind is invalid")
	}
}

func decodeLegacyWeixinCursor(data []byte) (syncCursorFile, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return syncCursorFile{}, false
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return syncCursorFile{}, false
	}
	var decoded syncCursorFile
	foundField := false
	foundValid := false
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return syncCursorFile{}, false
		}
		name, ok := nameToken.(string)
		if !ok {
			return syncCursorFile{}, false
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return syncCursorFile{}, false
		}
		if name != "get_updates_buf" {
			continue
		}
		foundField = true
		var cursor string
		if json.Unmarshal(raw, &cursor) == nil && validWeixinStateValue(cursor, true) && !foundValid {
			decoded.GetUpdatesBuf = cursor
			foundValid = true
		}
	}
	if _, err := decoder.Token(); err != nil {
		return syncCursorFile{}, false
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return syncCursorFile{}, false
	}
	if foundField && !foundValid {
		return syncCursorFile{}, false
	}
	return decoded, true
}

func decodeLegacyWeixinTokensForImport(data []byte) ([]weixinLegacyToken, bool, bool) {
	tokens, err := decodeLegacyWeixinTokens(data)
	return tokens, errors.Is(err, errWeixinLegacyTokenLimit), err == nil
}

func parseWeixinLegacyRelative(relative, customKind, customAccount string) (string, string, bool) {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
	directory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(clean)))
	accountKey := strings.TrimSuffix(filepath.Base(clean), filepath.Ext(clean))
	kind := customKind
	if directory == "sync" {
		kind = weixinStateKindCursor
	} else if directory == "context-tokens" {
		kind = weixinStateKindTokens
	} else if directory != "." {
		return "", "", false
	} else {
		accountKey = customAccount
	}
	return kind, accountKey, validWeixinAccountKey(accountKey) &&
		(kind == weixinStateKindCursor || kind == weixinStateKindTokens)
}

func decodeLegacyWeixinTokens(data []byte) ([]weixinLegacyToken, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, errors.New("Weixin context-token file is not an object")
	}
	var tokens []weixinLegacyToken
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := nameToken.(string)
		if !ok {
			return nil, errors.New("Weixin context-token field is invalid")
		}
		var raw json.RawMessage
		if decodeErr := decoder.Decode(&raw); decodeErr != nil {
			return nil, decodeErr
		}
		if name != "tokens" || string(raw) == "null" {
			continue
		}
		entries, err := decodeLegacyWeixinTokenObject(raw)
		if err != nil {
			return nil, err
		}
		if len(entries) > weixinStateMaxTokens-len(tokens) {
			return nil, errWeixinLegacyTokenLimit
		}
		tokens = append(tokens, entries...)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("Weixin context tokens have trailing JSON")
		}
		return nil, err
	}
	return tokens, nil
}

func decodeLegacyWeixinTokenObject(raw json.RawMessage) ([]weixinLegacyToken, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, errors.New("Weixin context tokens are not an object")
	}
	var tokens []weixinLegacyToken
	for decoder.More() {
		if len(tokens) >= weixinStateMaxTokens {
			return nil, errWeixinLegacyTokenLimit
		}
		userToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		userID, ok := userToken.(string)
		if !ok {
			return nil, errors.New("Weixin context-token user is invalid")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		tokens = append(tokens, weixinLegacyToken{
			userID: userID,
			raw:    value,
			digest: sha256.Sum256(value),
		})
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("Weixin context-token object has trailing JSON")
		}
		return nil, err
	}
	return tokens, nil
}

func importLegacyWeixinTokens(
	ctx context.Context,
	conn *sql.Conn,
	accountKey string,
	tokens []weixinLegacyToken,
	seconds,
	nanoseconds int64,
) (sqlitestore.ImportResult, error) {
	if len(tokens) > weixinStateMaxTokens {
		return sqlitestore.ImportResult{}, errWeixinLegacyTokenLimit
	}
	sort.SliceStable(tokens, func(left, right int) bool { return tokens[left].userID < tokens[right].userID })
	type candidate struct {
		token weixinLegacyToken
		value string
	}
	seen := make(map[string]struct{}, len(tokens))
	candidates := make([]candidate, 0, len(tokens))
	result := sqlitestore.ImportResult{}
	for _, token := range tokens {
		if _, exists := seen[token.userID]; exists {
			appendWeixinIssue(&result, "identity-conflict", token.digest)
			continue
		}
		var value string
		if err := json.Unmarshal(token.raw, &value); err != nil ||
			validateWeixinStateValue(token.userID, false) != nil ||
			validateWeixinStateValue(value, true) != nil {
			appendWeixinIssue(&result, "invalid-token", token.digest)
			continue
		}
		seen[token.userID] = struct{}{}
		candidates = append(candidates, candidate{token: token, value: value})
	}
	if len(candidates) == 0 {
		return result, nil
	}
	if err := ensureWeixinAccount(ctx, conn, accountKey, seconds, nanoseconds); err != nil {
		return sqlitestore.ImportResult{}, err
	}
	accountRows, totalRows, err := weixinTokenRowCounts(ctx, conn, accountKey)
	if err != nil {
		return sqlitestore.ImportResult{}, err
	}
	if accountRows > weixinStateMaxTokens || totalRows > weixinStateMaxTokenRows {
		return sqlitestore.ImportResult{}, errors.New("Weixin context-token row count exceeds its limit")
	}
	for _, candidate := range candidates {
		var exists int
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS(
            SELECT 1 FROM weixin_context_tokens
            WHERE account_key = ? AND user_id = ?
        )`, accountKey, candidate.token.userID).Scan(&exists); err != nil {
			return sqlitestore.ImportResult{}, err
		}
		if exists == 0 {
			if accountRows >= weixinStateMaxTokens || totalRows >= weixinStateMaxTokenRows {
				return sqlitestore.ImportResult{}, errors.New("Weixin context-token row count exceeds its limit")
			}
			accountRows++
			totalRows++
		}
	}
	for _, candidate := range candidates {
		executionResult, err := conn.ExecContext(ctx, `INSERT INTO weixin_context_tokens (
            account_key, user_id, context_token,
            updated_at_unix_seconds, updated_at_nanosecond, version
        ) VALUES (?, ?, ?, ?, ?, 1)
        ON CONFLICT(account_key, user_id) DO NOTHING`,
			accountKey,
			candidate.token.userID,
			candidate.value,
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
		if inserted == 1 {
			result.Imported++
		} else {
			appendWeixinIssue(&result, "sqlite-authoritative", candidate.token.digest)
		}
	}
	return result, nil
}

func weixinImportExecutionResult(
	result sql.Result,
	err error,
	digest [sha256.Size]byte,
) (sqlitestore.ImportResult, error) {
	if err != nil {
		return sqlitestore.ImportResult{}, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return sqlitestore.ImportResult{}, err
	}
	if inserted == 1 {
		return sqlitestore.ImportResult{Imported: 1}, nil
	}
	return skippedWeixinImport("sqlite-authoritative", digest), nil
}

func skippedWeixinImport(code string, digest [sha256.Size]byte) sqlitestore.ImportResult {
	result := sqlitestore.ImportResult{}
	appendWeixinIssue(&result, code, digest)
	return result
}

func appendWeixinIssue(result *sqlitestore.ImportResult, code string, digest [sha256.Size]byte) {
	result.Skipped++
	if len(result.Issues) < 512 {
		result.Issues = append(result.Issues, sqlitestore.ImportIssue{
			Code:         code,
			RecordDigest: digest,
		})
	}
}

func ensureWeixinAccount(
	ctx context.Context,
	conn *sql.Conn,
	accountKey string,
	seconds,
	nanoseconds int64,
) error {
	var exists int
	if err := conn.QueryRowContext(
		ctx,
		`SELECT EXISTS(SELECT 1 FROM weixin_accounts WHERE account_key = ?)`,
		accountKey,
	).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return nil
	}
	var accounts int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM weixin_accounts`).Scan(
		&accounts,
	); err != nil {
		return err
	}
	if accounts >= weixinStateMaxAccounts {
		return errors.New("Weixin account row count exceeds its limit")
	}
	_, err := conn.ExecContext(ctx, `INSERT INTO weixin_accounts (
        account_key, created_at_unix_seconds, created_at_nanosecond, version
    ) VALUES (?, ?, ?, 1) ON CONFLICT(account_key) DO NOTHING`,
		accountKey,
		seconds,
		nanoseconds,
	)
	return err
}

func ensureWeixinCursorCapacity(ctx context.Context, conn *sql.Conn, accountKey string) error {
	var exists int
	if err := conn.QueryRowContext(
		ctx,
		`SELECT EXISTS(SELECT 1 FROM weixin_cursors WHERE account_key = ?)`,
		accountKey,
	).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return nil
	}
	var cursors int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM weixin_cursors`).Scan(
		&cursors,
	); err != nil {
		return err
	}
	if cursors >= weixinStateMaxCursors {
		return errors.New("Weixin cursor row count exceeds its limit")
	}
	return nil
}

func ensureWeixinTokenReplacementCapacity(
	ctx context.Context,
	conn *sql.Conn,
	accountKey string,
	desiredRows int,
) error {
	if desiredRows < 0 || desiredRows > weixinStateMaxTokens {
		return errors.New("Weixin context-token count exceeds its limit")
	}
	accountRows, totalRows, err := weixinTokenRowCounts(ctx, conn, accountKey)
	if err != nil {
		return err
	}
	if accountRows > weixinStateMaxTokens || totalRows > weixinStateMaxTokenRows ||
		desiredRows > weixinStateMaxTokenRows-(totalRows-accountRows) {
		return errors.New("Weixin context-token row count exceeds its limit")
	}
	return nil
}

func ensureWeixinTokenInsertCapacity(
	ctx context.Context,
	conn *sql.Conn,
	accountKey,
	userID string,
) error {
	var exists int
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(
        SELECT 1 FROM weixin_context_tokens
        WHERE account_key = ? AND user_id = ?
    )`, accountKey, userID).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return nil
	}
	accountRows, totalRows, err := weixinTokenRowCounts(ctx, conn, accountKey)
	if err != nil {
		return err
	}
	if accountRows >= weixinStateMaxTokens || totalRows >= weixinStateMaxTokenRows {
		return errors.New("Weixin context-token row count exceeds its limit")
	}
	return nil
}

func weixinTokenRowCounts(
	ctx context.Context,
	conn *sql.Conn,
	accountKey string,
) (int, int, error) {
	var accountRows, totalRows int
	if err := conn.QueryRowContext(ctx, `SELECT
        COUNT(*) FILTER (WHERE account_key = ?), COUNT(*)
        FROM weixin_context_tokens`, accountKey).Scan(&accountRows, &totalRows); err != nil {
		return 0, 0, err
	}
	return accountRows, totalRows, nil
}

func validateWeixinStateSchema(ctx context.Context, conn *sql.Conn) error {
	for _, object := range []struct {
		objectType string
		name       string
		schema     string
	}{
		{objectType: "table", name: "weixin_accounts", schema: weixinAccountsSchema},
		{objectType: "table", name: "weixin_cursors", schema: weixinCursorsSchema},
		{objectType: "table", name: "weixin_context_tokens", schema: weixinContextTokensSchema},
		{objectType: "index", name: "weixin_cursors_updated_idx", schema: weixinCursorsUpdatedIndexSchema},
		{objectType: "index", name: "weixin_context_tokens_updated_idx", schema: weixinTokensUpdatedIndexSchema},
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
	for _, table := range []string{"weixin_accounts", "weixin_cursors", "weixin_context_tokens"} {
		if err := sqlitestore.ValidateUniqueIndexSet(ctx, conn, table); err != nil {
			return err
		}
	}
	var unexpected int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
        WHERE name NOT LIKE 'sqlite_%'
          AND name NOT IN (
              'weixin_accounts', 'weixin_cursors', 'weixin_context_tokens',
              'weixin_cursors_updated_idx', 'weixin_context_tokens_updated_idx',
              'storage_imports', 'storage_import_issues', 'storage_import_horizons',
              'storage_imports_archive_status_idx'
          )`).Scan(&unexpected); err != nil {
		return err
	}
	if unexpected != 0 {
		return errors.New("Weixin state schema has unexpected objects")
	}
	for _, boundedTable := range []struct {
		query string
		limit int
	}{
		{query: `SELECT COUNT(*) FROM weixin_accounts`, limit: weixinStateMaxAccounts},
		{query: `SELECT COUNT(*) FROM weixin_cursors`, limit: weixinStateMaxCursors},
		{query: `SELECT COUNT(*) FROM weixin_context_tokens`, limit: weixinStateMaxTokenRows},
	} {
		var rows int
		if err := conn.QueryRowContext(ctx, boundedTable.query).Scan(&rows); err != nil {
			return err
		}
		if rows > boundedTable.limit {
			return errors.New("Weixin state row count exceeds its limit")
		}
	}
	var maximumAccountTokens int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(token_count), 0) FROM (
        SELECT COUNT(*) AS token_count
        FROM weixin_context_tokens
        GROUP BY account_key
    )`).Scan(&maximumAccountTokens); err != nil {
		return err
	}
	if maximumAccountTokens > weixinStateMaxTokens {
		return errors.New("Weixin account context-token count exceeds its limit")
	}
	return nil
}

func validWeixinAccountKey(accountKey string) bool {
	if accountKey == "default" {
		return true
	}
	if len(accountKey) != 16 || accountKey != strings.ToLower(accountKey) {
		return false
	}
	_, err := hex.DecodeString(accountKey)
	return err == nil
}

func validateWeixinStateValue(value string, allowEmpty bool) error {
	if !allowEmpty && value == "" {
		return errors.New("Weixin state value is required")
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return errors.New("Weixin state value is invalid")
	}
	if len(value) > weixinStateMaxValueBytes {
		return errors.New("Weixin state value exceeds its size limit")
	}
	return nil
}

func validWeixinStateValue(value string, allowEmpty bool) bool {
	return validateWeixinStateValue(value, allowEmpty) == nil
}

func weixinStateTimestampValues(timestamp time.Time) (int64, int64, error) {
	if timestamp.Year() < 0 || timestamp.Year() > 9999 {
		return 0, 0, errors.New("Weixin state timestamp is outside the supported range")
	}
	return timestamp.Unix(), int64(timestamp.Nanosecond()), nil
}

func weixinStateLocalLock(path string) *sync.Mutex {
	digest := sha256.Sum256([]byte(path))
	return &weixinStateLocks[uint32(digest[0])%weixinStateLockShards]
}
