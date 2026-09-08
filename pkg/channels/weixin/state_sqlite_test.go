package weixin

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
	"github.com/sipeed/picoclaw/pkg/config"
)

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestWeixinSQLiteSchemaPragmasPermissionsAndReopen(t *testing.T) {
	home := setWeixinPersistenceHome(t)
	cfg := &config.WeixinSettings{BaseURL: "https://ilink.example/"}
	cfg.SetToken("token")
	cursorLocator := buildWeixinSyncBufPath(cfg)
	tokenLocator := buildWeixinContextTokensPath(cfg)
	store, err := newWeixinStateStore(cursorLocator, weixinStateKindCursor)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time {
		return time.Date(2500, time.January, 1, 2, 3, 4, 567890123, time.UTC)
	}
	if err := store.saveCursor(t.Context(), "cursor-1"); err != nil {
		t.Fatal(err)
	}
	tokenStore, err := newWeixinStateStore(tokenLocator, weixinStateKindTokens)
	if err != nil {
		t.Fatal(err)
	}
	tokenStore.now = store.now
	if err := tokenStore.saveTokens(t.Context(), map[string]string{
		"user-a": "context-a",
		"user-b": "context-b",
	}); err != nil {
		t.Fatal(err)
	}

	db, unlock, err := store.open(t.Context())
	if err != nil {
		t.Fatal(err)
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
			t.Fatal(err)
		}
	}
	if version != 1 || foreignKeys != 1 || busyTimeout != 5000 || synchronous != 2 ||
		!strings.EqualFold(journal, "wal") {
		t.Fatalf(
			"SQLite config = version:%d fk:%d busy:%d sync:%d journal:%q",
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
	if err := validateWeixinStateSchema(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	unlock()

	raw := openRawWeixinDB(t, store.path)
	defer raw.Close()
	var cursorVersion, seconds int64
	var nanoseconds int
	if err := raw.QueryRow(`SELECT version, updated_at_unix_seconds, updated_at_nanosecond
        FROM weixin_cursors WHERE account_key = ?`, store.accountKey).Scan(
		&cursorVersion,
		&seconds,
		&nanoseconds,
	); err != nil {
		t.Fatal(err)
	}
	if cursorVersion != 1 || !time.Unix(seconds, int64(nanoseconds)).Equal(store.now()) {
		t.Fatalf("cursor row = version:%d updated:%v", cursorVersion, time.Unix(seconds, int64(nanoseconds)))
	}
	if err := store.saveCursor(t.Context(), "cursor-2"); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT version FROM weixin_cursors WHERE account_key = ?`,
		store.accountKey,
	).Scan(&cursorVersion); err != nil || cursorVersion != 2 {
		t.Fatalf("cursor version after update = %d, %v", cursorVersion, err)
	}
	for name, statement := range map[string]string{
		"invalid account": `INSERT INTO weixin_accounts (
            account_key, created_at_unix_seconds, created_at_nanosecond, version)
            VALUES ('INVALID', 1, 0, 1)`,
		"partial cursor time": `UPDATE weixin_cursors
            SET updated_at_nanosecond = NULL WHERE account_key = '` + store.accountKey + `'`,
		"empty user": `INSERT INTO weixin_context_tokens (
            account_key, user_id, context_token,
            updated_at_unix_seconds, updated_at_nanosecond, version)
            VALUES ('` + store.accountKey + `', '', '', 1, 0, 1)`,
		"zero version": `UPDATE weixin_accounts SET version = 0 WHERE account_key = '` + store.accountKey + `'`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := raw.Exec(statement); err == nil {
				t.Fatalf("constraint accepted %s", name)
			}
		})
	}
	if runtime.GOOS != "windows" {
		for path, mode := range map[string]os.FileMode{
			store.path:               0o600,
			store.path + "-wal":      0o600,
			store.path + "-shm":      0o600,
			filepath.Dir(store.path): 0o700,
			store.path + ".locks":    0o700,
			filepath.Join(store.path+".locks", "store.lock"): 0o600,
		} {
			info, statErr := os.Stat(path)
			if statErr != nil || info.Mode().Perm() != mode {
				t.Fatalf("mode for %s = %v, %v; want %o", path, info, statErr, mode)
			}
		}
	}
	if _, err := os.Stat(cursorLocator); !os.IsNotExist(err) {
		t.Fatalf("cursor JSON was written: %v", err)
	}
	if _, err := os.Stat(tokenLocator); !os.IsNotExist(err) {
		t.Fatalf("context-token JSON was written: %v", err)
	}
	if store.path != filepath.Join(home, "channels", "weixin", weixinStateDatabaseFilename) {
		t.Fatalf("Weixin database path = %q", store.path)
	}
	if cursor, err := loadGetUpdatesBuf(cursorLocator); err != nil || cursor != "cursor-2" {
		t.Fatalf("reopened cursor = %q, %v", cursor, err)
	}
	if tokens, err := loadContextTokens(tokenLocator); err != nil ||
		tokens["user-a"] != "context-a" || tokens["user-b"] != "context-b" {
		t.Fatalf("reopened tokens = %#v, %v", tokens, err)
	}
}

func TestWeixinSQLiteAccountIsolationReplacementAndConcurrency(t *testing.T) {
	home := setWeixinPersistenceHome(t)
	root := filepath.Join(home, "channels", "weixin")
	accountA := "0123456789abcdef"
	accountB := "fedcba9876543210"
	cursorA := filepath.Join(root, "sync", accountA+".json")
	cursorB := filepath.Join(root, "sync", accountB+".json")
	tokensA := filepath.Join(root, "context-tokens", accountA+".json")
	tokensB := filepath.Join(root, "context-tokens", accountB+".json")
	if err := saveGetUpdatesBuf(cursorA, "cursor-a"); err != nil {
		t.Fatal(err)
	}
	if err := saveGetUpdatesBuf(cursorB, "cursor-b"); err != nil {
		t.Fatal(err)
	}
	if err := saveContextTokens(tokensA, map[string]string{"user-a": "token-a", "remove": "old"}); err != nil {
		t.Fatal(err)
	}
	if err := saveContextTokens(tokensB, map[string]string{"user-b": "token-b"}); err != nil {
		t.Fatal(err)
	}
	if err := saveContextTokens(tokensA, map[string]string{"user-a": "token-a2"}); err != nil {
		t.Fatal(err)
	}
	loadedA, err := loadContextTokens(tokensA)
	if err != nil {
		t.Fatal(err)
	}
	loadedB, err := loadContextTokens(tokensB)
	if err != nil {
		t.Fatal(err)
	}
	if len(loadedA) != 1 || loadedA["user-a"] != "token-a2" || loadedA["remove"] != "" {
		t.Fatalf("account A tokens = %#v", loadedA)
	}
	if len(loadedB) != 1 || loadedB["user-b"] != "token-b" {
		t.Fatalf("account B tokens = %#v", loadedB)
	}
	raw := openRawWeixinDB(t, filepath.Join(root, weixinStateDatabaseFilename))
	var tokenVersion int
	if queryErr := raw.QueryRow(`SELECT version FROM weixin_context_tokens
		WHERE account_key = ? AND user_id = 'user-a'`, accountA).Scan(&tokenVersion); queryErr != nil {
		_ = raw.Close()
		t.Fatal(queryErr)
	}
	if tokenVersion != 2 {
		_ = raw.Close()
		t.Fatalf("updated context-token version = %d, want 2", tokenVersion)
	}
	if closeErr := raw.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if cursor, _ := loadGetUpdatesBuf(cursorA); cursor != "cursor-a" {
		t.Fatalf("account A cursor = %q", cursor)
	}
	if cursor, _ := loadGetUpdatesBuf(cursorB); cursor != "cursor-b" {
		t.Fatalf("account B cursor = %q", cursor)
	}

	const workers = 20
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for index := range workers {
		go func() {
			defer wait.Done()
			<-start
			locator := cursorA
			if index%2 != 0 {
				locator = cursorB
			}
			errs <- saveGetUpdatesBuf(locator, fmt.Sprintf("cursor-%02d", index))
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if cursor, _ := loadGetUpdatesBuf(cursorA); cursor == "" {
		t.Fatal("concurrent account A cursor is empty")
	}
	if cursor, _ := loadGetUpdatesBuf(cursorB); cursor == "" {
		t.Fatal("concurrent account B cursor is empty")
	}

	start = make(chan struct{})
	errs = make(chan error, workers)
	wait = sync.WaitGroup{}
	wait.Add(workers)
	for index := range workers {
		go func() {
			defer wait.Done()
			<-start
			errs <- saveContextToken(
				tokensA,
				fmt.Sprintf("concurrent-user-%02d", index),
				fmt.Sprintf("concurrent-token-%02d", index),
			)
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	loadedA, err = loadContextTokens(tokensA)
	if err != nil {
		t.Fatal(err)
	}
	if len(loadedA) != workers+1 || loadedA["user-a"] != "token-a2" {
		t.Fatalf("concurrent per-user tokens = %#v", loadedA)
	}
	for index := range workers {
		if loadedA[fmt.Sprintf("concurrent-user-%02d", index)] !=
			fmt.Sprintf("concurrent-token-%02d", index) {
			t.Fatalf("missing concurrent token %d", index)
		}
	}
}

func TestWeixinLegacyEnumerationMigrationAuditArchiveAndIdempotence(t *testing.T) {
	home := setWeixinPersistenceHome(t)
	root := filepath.Join(home, "channels", "weixin")
	account := "0123456789abcdef"
	cursorData := []byte(`{"get_updates_buf":"cursor-legacy","get_updates_buf":"later-cursor"}`)
	tokenData := []byte(`{
        "tokens": {
            "user-a":"first-token",
            "user-a":"later-token",
            "":"invalid-user",
            "user-b":4
        }
    }`)
	writeWeixinLegacyFile(t, root, filepath.Join("sync", account+".json"), cursorData, 0o600)
	writeWeixinLegacyFile(t, root, filepath.Join("context-tokens", account+".json"), tokenData, 0o640)
	cursorLocator := filepath.Join(root, "sync", account+".json")
	if cursor, err := loadGetUpdatesBuf(cursorLocator); err != nil || cursor != "cursor-legacy" {
		t.Fatalf("migrated cursor = %q, %v", cursor, err)
	}
	tokenLocator := filepath.Join(root, "context-tokens", account+".json")
	tokens, err := loadContextTokens(tokenLocator)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens["user-a"] != "first-token" {
		t.Fatalf("migrated tokens = %#v", tokens)
	}
	for relative, want := range map[string][]byte{
		filepath.ToSlash(filepath.Join("sync", account+".json")):           cursorData,
		filepath.ToSlash(filepath.Join("context-tokens", account+".json")): tokenData,
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); !os.IsNotExist(err) {
			t.Fatalf("legacy source %s remains: %v", relative, err)
		}
		archive := filepath.Join(
			root,
			"legacy-json",
			weixinStateLegacyArchiveLabel,
			filepath.FromSlash(relative),
		)
		if archived, err := os.ReadFile(archive); err != nil || !bytes.Equal(archived, want) {
			t.Fatalf("archive %s = %q, %v", relative, archived, err)
		}
	}
	if runtime.GOOS != "windows" {
		archive := filepath.Join(
			root,
			"legacy-json",
			weixinStateLegacyArchiveLabel,
			"context-tokens",
			account+".json",
		)
		info, err := os.Stat(archive)
		if err != nil || info.Mode().Perm() != 0o640 {
			t.Fatalf("context-token archive mode = %v, %v", info, err)
		}
	}
	db := openRawWeixinDB(t, filepath.Join(root, weixinStateDatabaseFilename))
	defer db.Close()
	var imports, imported, skipped, issues int
	if err := db.QueryRow(`SELECT COUNT(*), SUM(imported_count), SUM(skipped_count)
        FROM storage_imports WHERE component = ?`, weixinStateComponent).Scan(
		&imports,
		&imported,
		&skipped,
	); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM storage_import_issues WHERE component = ?`,
		weixinStateComponent,
	).Scan(&issues); err != nil {
		t.Fatal(err)
	}
	if imports != 2 || imported != 2 || skipped != 3 || issues != 3 {
		t.Fatalf(
			"migration audit = sources:%d imported:%d skipped:%d issues:%d",
			imports,
			imported,
			skipped,
			issues,
		)
	}
	if cursor, err := loadGetUpdatesBuf(cursorLocator); err != nil || cursor != "cursor-legacy" {
		t.Fatalf("idempotent cursor = %q, %v", cursor, err)
	}
}

func TestWeixinMalformedUnsafeSymlinkAndOversizedLegacySources(t *testing.T) {
	t.Run("malformed audited", func(t *testing.T) {
		home := setWeixinPersistenceHome(t)
		root := filepath.Join(home, "channels", "weixin")
		data := []byte(`{"tokens":{"private-user-canary":`)
		writeWeixinLegacyFile(t, root, "context-tokens/default.json", data, 0o600)
		if tokens, err := loadContextTokens(
			filepath.Join(root, "context-tokens", "default.json"),
		); err != nil || tokens != nil {
			t.Fatalf("malformed tokens = (%#v, %v)", tokens, err)
		}
		db := openRawWeixinDB(t, filepath.Join(root, weixinStateDatabaseFilename))
		defer db.Close()
		var code string
		var digest []byte
		if err := db.QueryRow(`SELECT issue_code, record_digest FROM storage_import_issues
            WHERE component = ?`, weixinStateComponent).Scan(&code, &digest); err != nil {
			t.Fatal(err)
		}
		if code != "malformed-json" || len(digest) != sha256.Size || strings.Contains(code, "private") {
			t.Fatalf("malformed audit = %q/%x", code, digest)
		}
	})

	t.Run("invalid cursor audited", func(t *testing.T) {
		home := setWeixinPersistenceHome(t)
		root := filepath.Join(home, "channels", "weixin")
		path := writeWeixinLegacyFile(t, root, "sync/default.json", []byte(`[]`), 0o600)
		if cursor, err := loadGetUpdatesBuf(path); err != nil || cursor != "" {
			t.Fatalf("invalid migrated cursor = %q, %v", cursor, err)
		}
		db := openRawWeixinDB(t, filepath.Join(root, weixinStateDatabaseFilename))
		defer db.Close()
		var code string
		if err := db.QueryRow(`SELECT issue_code FROM storage_import_issues
            WHERE component = ?`, weixinStateComponent).Scan(&code); err != nil {
			t.Fatal(err)
		}
		if code != "invalid-cursor" {
			t.Fatalf("invalid cursor issue = %q", code)
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("unsafe mode", func(t *testing.T) {
			home := setWeixinPersistenceHome(t)
			root := filepath.Join(home, "channels", "weixin")
			path := writeWeixinLegacyFile(t, root, "sync/default.json", []byte(`{}`), 0o600)
			if err := os.Chmod(path, 0o622); err != nil {
				t.Fatal(err)
			}
			if _, err := loadGetUpdatesBuf(path); err == nil || !strings.Contains(err.Error(), "unsafe") {
				t.Fatalf("unsafe source error = %v", err)
			}
		})
	}

	t.Run("symlink", func(t *testing.T) {
		home := setWeixinPersistenceHome(t)
		root := filepath.Join(home, "channels", "weixin")
		if err := os.MkdirAll(filepath.Join(root, "sync"), 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(outside, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "sync", "default.json")
		if err := os.Symlink(outside, path); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := loadGetUpdatesBuf(path); err == nil ||
			(!strings.Contains(err.Error(), "symlink") && !strings.Contains(err.Error(), "unsafe")) {
			t.Fatalf("symlink source error = %v", err)
		}
	})

	t.Run("symlink directory", func(t *testing.T) {
		home := setWeixinPersistenceHome(t)
		root := filepath.Join(home, "channels", "weixin")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		if err := os.WriteFile(
			filepath.Join(outside, "default.json"),
			[]byte(`{"get_updates_buf":"outside"}`),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(root, "sync")
		if err := os.Symlink(outside, directory); err != nil {
			t.Skipf("directory symlinks unavailable: %v", err)
		}
		if _, err := loadGetUpdatesBuf(filepath.Join(directory, "default.json")); err == nil ||
			!strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("symlink directory error = %v", err)
		}
		data, err := os.ReadFile(filepath.Join(outside, "default.json"))
		if err != nil || string(data) != `{"get_updates_buf":"outside"}` {
			t.Fatalf("outside legacy source = %q, %v", data, err)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		home := setWeixinPersistenceHome(t)
		root := filepath.Join(home, "channels", "weixin")
		path := filepath.Join(root, "sync", "default.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(weixinStateLegacyMaxBytes + 1); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := loadGetUpdatesBuf(path); err == nil || !strings.Contains(err.Error(), "size limit") {
			t.Fatalf("oversized source error = %v", err)
		}
	})

	t.Run("record count", func(t *testing.T) {
		home := setWeixinPersistenceHome(t)
		root := filepath.Join(home, "channels", "weixin")
		path := writeWeixinLegacyFile(
			t,
			root,
			"context-tokens/default.json",
			overLimitWeixinLegacyTokens(),
			0o600,
		)
		if _, err := loadContextTokens(path); !errors.Is(err, errWeixinLegacyTokenLimit) {
			t.Fatalf("legacy token count migration error = %v", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("over-limit source was removed: %v", err)
		}
		archive := filepath.Join(
			root,
			"legacy-json",
			weixinStateLegacyArchiveLabel,
			"context-tokens",
			"default.json",
		)
		if _, err := os.Stat(archive); !os.IsNotExist(err) {
			t.Fatalf("over-limit source was archived: %v", err)
		}
	})
}

func TestWeixinCustomPathFacadeImportsLegacyWithoutJSONWrites(t *testing.T) {
	setWeixinPersistenceHome(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	cursorPath := filepath.Join(root, "custom-cursor.json")
	if err := os.WriteFile(cursorPath, []byte(`{"get_updates_buf":"legacy-custom"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if cursor, err := loadGetUpdatesBuf(cursorPath); err != nil || cursor != "legacy-custom" {
		t.Fatalf("custom cursor migration = %q, %v", cursor, err)
	}
	if err := saveGetUpdatesBuf(cursorPath, "sqlite-custom"); err != nil {
		t.Fatal(err)
	}
	if cursor, err := loadGetUpdatesBuf(cursorPath); err != nil || cursor != "sqlite-custom" {
		t.Fatalf("custom cursor reopen = %q, %v", cursor, err)
	}
	if _, err := os.Stat(cursorPath); !os.IsNotExist(err) {
		t.Fatalf("custom legacy cursor remains: %v", err)
	}
}

func TestWeixinSQLiteAuthorityWinsLateLegacyConflict(t *testing.T) {
	home := setWeixinPersistenceHome(t)
	root := filepath.Join(home, "channels", "weixin")
	legacyPath := filepath.Join(root, "context-tokens", "default.json")
	if err := saveContextTokens(legacyPath, map[string]string{"user": "sqlite-token"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		legacyPath,
		[]byte(`{"tokens":{"user":"legacy-token"}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	tokens, err := loadContextTokens(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens["user"] != "sqlite-token" {
		t.Fatalf("authoritative tokens = %#v", tokens)
	}
	db := openRawWeixinDB(t, filepath.Join(root, weixinStateDatabaseFilename))
	defer db.Close()
	var imported, skipped int
	if err := db.QueryRow(`SELECT imported_count, skipped_count FROM storage_imports
        WHERE component = ?`, weixinStateComponent).Scan(&imported, &skipped); err != nil {
		t.Fatal(err)
	}
	if imported != 0 || skipped != 1 {
		t.Fatalf("authoritative import counts = %d/%d", imported, skipped)
	}
}

func TestWeixinStateRejectsFutureInvalidAndCorruptDatabases(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, *sql.DB){
		"future": func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`PRAGMA user_version = 2`); err != nil {
				t.Fatal(err)
			}
		},
		"invalid schema": func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`CREATE TABLE rogue_weixin_state (value TEXT)`); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			home := setWeixinPersistenceHome(t)
			locator := filepath.Join(home, "channels", "weixin", "sync", "default.json")
			if err := saveGetUpdatesBuf(locator, "cursor"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, "channels", "weixin", weixinStateDatabaseFilename)
			db := openRawWeixinDB(t, path)
			mutate(t, db)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := loadGetUpdatesBuf(locator); err == nil {
				t.Fatalf("%s database was accepted", name)
			}
		})
	}

	t.Run("corrupt", func(t *testing.T) {
		home := setWeixinPersistenceHome(t)
		root := filepath.Join(home, "channels", "weixin")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(root, weixinStateDatabaseFilename),
			[]byte("not SQLite"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := loadGetUpdatesBuf(filepath.Join(root, "sync", "default.json")); err == nil {
			t.Fatal("corrupt database was accepted")
		}
	})

	t.Run("lock symlink", func(t *testing.T) {
		home := setWeixinPersistenceHome(t)
		root := filepath.Join(home, "channels", "weixin")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		database := filepath.Join(root, weixinStateDatabaseFilename)
		lockDirectory := database + ".locks"
		if err := os.MkdirAll(lockDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.lock")
		if err := os.WriteFile(outside, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(lockDirectory, "store.lock")); err != nil {
			t.Skipf("lock symlinks unavailable: %v", err)
		}
		if _, err := loadGetUpdatesBuf(filepath.Join(root, "sync", "default.json")); err == nil ||
			!strings.Contains(err.Error(), "regular file") {
			t.Fatalf("lock symlink error = %v", err)
		}
	})
}

func TestWeixinStateSchemaRejectsEveryRowCountCap(t *testing.T) {
	tests := []struct {
		name   string
		insert func(*testing.T, *sql.DB)
	}{
		{
			name: "accounts",
			insert: func(t *testing.T, db *sql.DB) {
				t.Helper()
				_, err := db.Exec(`WITH RECURSIVE row_number(value) AS (
                    VALUES(1) UNION ALL SELECT value + 1 FROM row_number WHERE value < ?
                ) INSERT INTO weixin_accounts (
                    account_key, created_at_unix_seconds, created_at_nanosecond, version
                ) SELECT printf('%016x', value), 0, 0, 1 FROM row_number`, weixinStateMaxAccounts)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "cursors",
			insert: func(t *testing.T, db *sql.DB) {
				t.Helper()
				_, err := db.Exec(`WITH RECURSIVE row_number(value) AS (
                    VALUES(1) UNION ALL SELECT value + 1 FROM row_number WHERE value <= ?
                ) INSERT INTO weixin_accounts (
                    account_key, created_at_unix_seconds, created_at_nanosecond, version
                ) SELECT printf('%016x', value), 0, 0, 1 FROM row_number`, weixinStateMaxCursors)
				if err == nil {
					_, err = db.Exec(`INSERT INTO weixin_cursors (
                        account_key, cursor_value,
                        updated_at_unix_seconds, updated_at_nanosecond, version
                    ) SELECT account_key, 'cursor', 0, 0, 1
                      FROM weixin_accounts WHERE account_key <> 'default'`)
				}
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "per-account tokens",
			insert: func(t *testing.T, db *sql.DB) {
				t.Helper()
				_, err := db.Exec(`WITH RECURSIVE row_number(value) AS (
                    VALUES(1) UNION ALL SELECT value + 1 FROM row_number WHERE value <= ?
                ) INSERT INTO weixin_context_tokens (
                    account_key, user_id, context_token,
                    updated_at_unix_seconds, updated_at_nanosecond, version
                ) SELECT 'default', printf('user-%05d', value), 'token', 0, 0, 1
                    FROM row_number`, weixinStateMaxTokens)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "global tokens",
			insert: func(t *testing.T, db *sql.DB) {
				t.Helper()
				accountCount := (weixinStateMaxTokenRows / weixinStateMaxTokens) + 1
				if _, err := db.Exec(`WITH RECURSIVE row_number(value) AS (
                    VALUES(1) UNION ALL SELECT value + 1 FROM row_number WHERE value < ?
                ) INSERT INTO weixin_accounts (
                    account_key, created_at_unix_seconds, created_at_nanosecond, version
                ) SELECT printf('%016x', value), 0, 0, 1 FROM row_number`, accountCount); err != nil {
					t.Fatal(err)
				}
				_, err := db.Exec(`WITH RECURSIVE row_number(value) AS (
                    VALUES(1) UNION ALL SELECT value + 1 FROM row_number WHERE value <= ?
                ) INSERT INTO weixin_context_tokens (
                    account_key, user_id, context_token,
                    updated_at_unix_seconds, updated_at_nanosecond, version
                ) SELECT
                    printf('%016x', ((value - 1) / ?) + 1),
                    printf('user-%05d', ((value - 1) % ?) + 1),
                    'token', 0, 0, 1
                FROM row_number`, weixinStateMaxTokenRows, weixinStateMaxTokens, weixinStateMaxTokens)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := setWeixinPersistenceHome(t)
			locator := filepath.Join(home, "channels", "weixin", "sync", "default.json")
			if err := saveGetUpdatesBuf(locator, "cursor"); err != nil {
				t.Fatal(err)
			}
			db := openRawWeixinDB(t, filepath.Join(
				home,
				"channels",
				"weixin",
				weixinStateDatabaseFilename,
			))
			defer db.Close()
			test.insert(t, db)
			conn, err := db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			switch test.name {
			case "accounts":
				if err := ensureWeixinAccount(
					t.Context(), conn, "ffffffffffffffff", 0, 0,
				); err == nil || !strings.Contains(err.Error(), "account row count") {
					t.Fatalf("account runtime cap error = %v", err)
				}
			case "cursors":
				if _, err := conn.ExecContext(t.Context(), `INSERT INTO weixin_accounts (
                    account_key, created_at_unix_seconds, created_at_nanosecond, version
                ) VALUES ('ffffffffffffffff', 0, 0, 1)`); err != nil {
					t.Fatal(err)
				}
				if err := ensureWeixinCursorCapacity(
					t.Context(), conn, "ffffffffffffffff",
				); err == nil || !strings.Contains(err.Error(), "cursor row count") {
					t.Fatalf("cursor runtime cap error = %v", err)
				}
			case "per-account tokens", "global tokens":
				if err := ensureWeixinTokenReplacementCapacity(
					t.Context(), conn, "default", 1,
				); err == nil || !strings.Contains(err.Error(), "row count") {
					t.Fatalf("context-token runtime cap error = %v", err)
				}
			}
			if err := validateWeixinStateSchema(t.Context(), conn); err == nil ||
				!strings.Contains(err.Error(), "count") {
				t.Fatalf("over-cap schema error = %v", err)
			}
		})
	}
}

func TestWeixinStateSerializesAcrossProcesses(t *testing.T) {
	if os.Getenv("PICOCLAW_WEIXIN_STATE_HELPER") == "1" {
		locator := os.Getenv("PICOCLAW_WEIXIN_STATE_LOCATOR")
		value := os.Getenv("PICOCLAW_WEIXIN_STATE_VALUE")
		if err := saveGetUpdatesBuf(locator, value); err != nil {
			t.Fatal(err)
		}
		return
	}
	home := setWeixinPersistenceHome(t)
	root := filepath.Join(home, "channels", "weixin")
	locators := []string{
		filepath.Join(root, "sync", "0123456789abcdef.json"),
		filepath.Join(root, "sync", "fedcba9876543210.json"),
	}
	commands := make([]*exec.Cmd, 0, len(locators))
	outputs := make([]bytes.Buffer, len(locators))
	for index, locator := range locators {
		command := exec.Command(os.Args[0], "-test.run=^TestWeixinStateSerializesAcrossProcesses$")
		command.Env = append(
			os.Environ(),
			config.EnvHome+"="+home,
			"PICOCLAW_WEIXIN_STATE_HELPER=1",
			"PICOCLAW_WEIXIN_STATE_LOCATOR="+locator,
			fmt.Sprintf("PICOCLAW_WEIXIN_STATE_VALUE=cursor-%d", index),
		)
		command.Stdout = &outputs[index]
		command.Stderr = &outputs[index]
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, command)
	}
	for index, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("helper %d failed: %v\n%s", index, err, outputs[index].String())
		}
	}
	for index, locator := range locators {
		if cursor, err := loadGetUpdatesBuf(locator); err != nil || cursor != fmt.Sprintf("cursor-%d", index) {
			t.Fatalf("cross-process cursor %d = %q, %v", index, cursor, err)
		}
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestWeixinStateValidationBoundaries(t *testing.T) {
	setWeixinPersistenceHome(t)
	if _, err := newWeixinStateStore("invalid\x00path", weixinStateKindCursor); err == nil {
		t.Fatal("NUL path was accepted")
	}
	if _, err := newWeixinStateStore("   ", weixinStateKindCursor); err == nil {
		t.Fatal("whitespace path was accepted")
	}
	if _, err := newWeixinStateStore(filepath.Join(t.TempDir(), "state.json"), "invalid"); err == nil {
		t.Fatal("invalid state kind was accepted")
	}
	if _, err := newWeixinStateStore(
		filepath.Join(t.TempDir(), "sync", "INVALID.json"),
		weixinStateKindCursor,
	); err == nil {
		t.Fatal("invalid canonical account key was accepted")
	}
	path := filepath.Join(t.TempDir(), "cursor.json")
	store, err := newWeixinStateStore(path, weixinStateKindCursor)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.saveCursor(t.Context(), strings.Repeat("x", weixinStateMaxValueBytes+1)); err == nil {
		t.Fatal("oversized cursor was accepted")
	}
	if err := store.saveCursor(t.Context(), string([]byte{0xff})); err == nil {
		t.Fatal("invalid UTF-8 cursor was accepted")
	}
	if err := store.saveCursor(t.Context(), "cursor\x00value"); err == nil {
		t.Fatal("NUL-bearing cursor was accepted")
	}
	store.now = func() time.Time {
		return time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	}
	if err := store.saveCursor(t.Context(), "cursor"); err == nil {
		t.Fatal("unsupported timestamp was accepted")
	}
	tokenStore, err := newWeixinStateStore(filepath.Join(t.TempDir(), "tokens.json"), weixinStateKindTokens)
	if err != nil {
		t.Fatal(err)
	}
	if err := tokenStore.saveTokens(t.Context(), map[string]string{"": "token"}); err == nil {
		t.Fatal("empty user ID was accepted")
	}
	if err := tokenStore.saveTokens(t.Context(), map[string]string{
		"user": strings.Repeat("x", weixinStateMaxValueBytes+1),
	}); err == nil {
		t.Fatal("oversized context token was accepted")
	}
	tooManyTokens := make(map[string]string, weixinStateMaxTokens+1)
	for index := range weixinStateMaxTokens + 1 {
		tooManyTokens[fmt.Sprintf("user-%05d", index)] = "token"
	}
	if err := tokenStore.saveTokens(t.Context(), tooManyTokens); err == nil {
		t.Fatal("oversized context-token map was accepted")
	}
	if err := tokenStore.saveTokens(t.Context(), map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if tokens, err := tokenStore.loadTokens(t.Context()); err != nil || tokens != nil {
		t.Fatalf("empty token replacement = (%#v, %v)", tokens, err)
	}
	if _, err := lockWeixinStateDatabase(""); err == nil {
		t.Fatal("empty database lock path was accepted")
	}
	if validWeixinAccountKey("0123456789ABCDEf") || !validWeixinAccountKey("default") {
		t.Fatal("account-key validation mismatch")
	}
	explicitDatabase := filepath.Join(t.TempDir(), "custom.db")
	explicitStore, err := newWeixinStateStore(explicitDatabase, weixinStateKindCursor)
	if err != nil {
		t.Fatal(err)
	}
	if explicitStore.path != explicitDatabase ||
		explicitStore.legacyPath != strings.TrimSuffix(explicitDatabase, ".db")+".json" {
		t.Fatalf(
			"explicit database paths = database:%q legacy:%q",
			explicitStore.path,
			explicitStore.legacyPath,
		)
	}
	if _, err := decodeLegacyWeixinTokens(overLimitWeixinLegacyTokens()); !errors.Is(err, errWeixinLegacyTokenLimit) {
		t.Fatalf("legacy context-token count error = %v", err)
	}
}

type weixinResult struct {
	rows int64
	err  error
}

func (r weixinResult) LastInsertId() (int64, error) { return 0, r.err }
func (r weixinResult) RowsAffected() (int64, error) { return r.rows, r.err }

func TestWeixinStateInternalFailureBoundaries(t *testing.T) { //nolint:gocognit // Fault matrix is intentionally linear.
	setWeixinPersistenceHome(t)
	sentinel := errors.New("injected Weixin state failure")
	blockingParent := filepath.Join(t.TempDir(), "blocking")
	if err := os.WriteFile(blockingParent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lockWeixinStateDatabase(filepath.Join(blockingParent, "state.db")); err == nil {
		t.Fatal("database lock accepted a file parent")
	}
	blockedLockDatabase := filepath.Join(t.TempDir(), "blocked-lock.db")
	if err := os.WriteFile(blockedLockDatabase+".locks", []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lockWeixinStateDatabase(blockedLockDatabase); err == nil {
		t.Fatal("database lock accepted a lock-directory file")
	}
	failed := &weixinStateStore{
		path:       filepath.Join(blockingParent, "state.db"),
		root:       filepath.Dir(blockingParent),
		accountKey: "default",
		now:        time.Now,
	}
	if _, err := failed.loadCursor(t.Context()); err == nil {
		t.Fatal("loadCursor accepted unavailable database")
	}
	if err := failed.saveCursor(t.Context(), "cursor"); err == nil {
		t.Fatal("saveCursor accepted unavailable database")
	}
	if _, err := failed.loadTokens(t.Context()); err == nil {
		t.Fatal("loadTokens accepted unavailable database")
	}
	if err := failed.saveTokens(t.Context(), map[string]string{"user": "token"}); err == nil {
		t.Fatal("saveTokens accepted unavailable database")
	}
	if err := failed.saveToken(t.Context(), "user", "token"); err == nil {
		t.Fatal("saveToken accepted unavailable database")
	}
	if err := failed.saveToken(t.Context(), "", "token"); err == nil {
		t.Fatal("saveToken accepted empty user")
	}
	if err := failed.saveToken(t.Context(), "user", string([]byte{0xff})); err == nil {
		t.Fatal("saveToken accepted invalid token")
	}

	store, err := newWeixinStateStore(
		filepath.Join(t.TempDir(), "cursor.json"),
		weixinStateKindCursor,
	)
	if err != nil {
		t.Fatal(err)
	}
	db, unlock, err := store.open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := deleteRemovedWeixinTokens(t.Context(), conn, "default", nil); err == nil {
		t.Fatal("token deletion accepted closed connection")
	}
	if err := ensureWeixinAccount(t.Context(), conn, "default", 0, 0); err == nil {
		t.Fatal("account capacity accepted closed connection")
	}
	if err := ensureWeixinCursorCapacity(t.Context(), conn, "default"); err == nil {
		t.Fatal("cursor capacity accepted closed connection")
	}
	if err := ensureWeixinTokenReplacementCapacity(t.Context(), conn, "default", 0); err == nil {
		t.Fatal("token capacity accepted closed connection")
	}
	if err := ensureWeixinTokenInsertCapacity(t.Context(), conn, "default", "user"); err == nil {
		t.Fatal("token insert capacity accepted closed connection")
	}
	if _, _, err := weixinTokenRowCounts(t.Context(), conn, "default"); err == nil {
		t.Fatal("token row count accepted closed connection")
	}
	if err := validateWeixinStateSchema(t.Context(), conn); err == nil {
		t.Fatal("schema validation accepted closed connection")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	unlock()

	digest := sha256.Sum256([]byte("record"))
	invalidToken := weixinLegacyToken{userID: "", raw: json.RawMessage(`"token"`), digest: digest}
	if result, err := importLegacyWeixinTokens(
		t.Context(), conn, "default", []weixinLegacyToken{invalidToken}, 0, 0,
	); err != nil || result.Skipped != 1 {
		t.Fatalf("all-invalid token import = (%#v, %v)", result, err)
	}
	if _, err := importLegacyWeixinTokens(
		t.Context(), conn, "default", []weixinLegacyToken{{
			userID: "user", raw: json.RawMessage(`"token"`), digest: digest,
		}}, 0, 0,
	); err == nil {
		t.Fatal("token import accepted closed connection")
	}
	if _, err := importLegacyWeixinTokens(
		t.Context(), conn, "default", make([]weixinLegacyToken, weixinStateMaxTokens+1), 0, 0,
	); !errors.Is(err, errWeixinLegacyTokenLimit) {
		t.Fatalf("direct token import limit error = %v", err)
	}
	if result, err := weixinImportExecutionResult(nil, sentinel, digest); !errors.Is(err, sentinel) ||
		result.Imported != 0 {
		t.Fatalf("import execution error = (%#v, %v)", result, err)
	}
	if _, err := weixinImportExecutionResult(weixinResult{err: sentinel}, nil, digest); !errors.Is(err, sentinel) {
		t.Fatalf("rows-affected error = %v", err)
	}
	if result, err := weixinImportExecutionResult(weixinResult{}, nil, digest); err != nil ||
		result.Skipped != 1 {
		t.Fatalf("zero-row import result = (%#v, %v)", result, err)
	}
	result := sqlitestore.ImportResult{Issues: make([]sqlitestore.ImportIssue, 512)}
	appendWeixinIssue(&result, "bounded", digest)
	if result.Skipped != 1 || len(result.Issues) != 512 {
		t.Fatalf("bounded issue append = %#v", result)
	}
	if err := ensureWeixinTokenReplacementCapacity(t.Context(), conn, "default", -1); err == nil {
		t.Fatal("negative desired token count was accepted")
	}

	invalidAccountStore := &weixinStateStore{now: time.Now, legacyKind: weixinStateKindCursor}
	if result, err := invalidAccountStore.importLegacy(t.Context(), conn, sqlitestore.LegacyInput{
		Relative: "nested/default.json",
		Digest:   digest,
	}); err != nil || result.Skipped != 1 {
		t.Fatalf("invalid-account import = (%#v, %v)", result, err)
	}
	invalidTimeStore := &weixinStateStore{
		now: func() time.Time {
			return time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
		},
		legacyKind: weixinStateKindCursor,
		accountKey: "default",
	}
	if _, err := invalidTimeStore.importLegacy(t.Context(), conn, sqlitestore.LegacyInput{
		Relative: "cursor.json",
		Digest:   digest,
	}); err == nil {
		t.Fatal("invalid migration timestamp was accepted")
	}

	for name, data := range map[string][]byte{
		"empty":           nil,
		"array":           []byte(`[]`),
		"truncated value": []byte(`{"get_updates_buf":`),
		"missing close":   []byte(`{"get_updates_buf":"cursor"`),
		"trailing":        []byte(`{} []`),
		"invalid field":   []byte(`{"get_updates_buf":4}`),
	} {
		t.Run("cursor "+name, func(t *testing.T) {
			if _, valid := decodeLegacyWeixinCursor(data); valid {
				t.Fatal("invalid cursor JSON was accepted")
			}
		})
	}
	for name, data := range map[string][]byte{
		"empty":           nil,
		"array":           []byte(`[]`),
		"truncated value": []byte(`{"tokens":`),
		"missing close":   []byte(`{"tokens":{}`),
		"trailing":        []byte(`{} []`),
		"invalid tokens":  []byte(`{"tokens":[]}`),
	} {
		t.Run("tokens "+name, func(t *testing.T) {
			if _, err := decodeLegacyWeixinTokens(data); err == nil {
				t.Fatal("invalid token JSON was accepted")
			}
		})
	}
	for name, data := range map[string]json.RawMessage{
		"empty":           nil,
		"array":           json.RawMessage(`[]`),
		"truncated value": json.RawMessage(`{"user":`),
		"missing close":   json.RawMessage(`{"user":"token"`),
		"trailing":        json.RawMessage(`{} []`),
	} {
		t.Run("token object "+name, func(t *testing.T) {
			if _, err := decodeLegacyWeixinTokenObject(data); err == nil {
				t.Fatal("invalid token object was accepted")
			}
		})
	}
}

func overLimitWeixinLegacyTokens() []byte {
	var data strings.Builder
	data.WriteString(`{"tokens":{`)
	for index := range weixinStateMaxTokens + 1 {
		if index != 0 {
			data.WriteByte(',')
		}
		_, _ = fmt.Fprintf(
			&data,
			"%q:%q",
			fmt.Sprintf("user-%05d", index),
			"token",
		)
	}
	data.WriteString(`}}`)
	return []byte(data.String())
}

func openRawWeixinDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func setWeixinPersistenceHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvHome, home)
	return home
}

func writeWeixinLegacyFile(
	t *testing.T,
	root,
	relative string,
	data []byte,
	mode os.FileMode,
) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWeixinStateTooNewErrorIsTyped(t *testing.T) {
	home := setWeixinPersistenceHome(t)
	locator := filepath.Join(home, "channels", "weixin", "sync", "default.json")
	if err := saveGetUpdatesBuf(locator, "cursor"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "channels", "weixin", weixinStateDatabaseFilename)
	db := openRawWeixinDB(t, path)
	if _, err := db.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadGetUpdatesBuf(locator); !errors.Is(err, sqlitestore.ErrTooNew) {
		t.Fatalf("too-new error = %v", err)
	}
}
