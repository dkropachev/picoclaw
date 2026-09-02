package wecom

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
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

func TestReqIDStorePersistsRoutes(t *testing.T) {
	setWecomPersistenceHome(t)
	storePath := filepath.Join(t.TempDir(), "reqids.json")
	store := newReqIDStore(storePath)
	if err := store.initializationError(); err != nil {
		t.Fatal(err)
	}
	if err := store.Put("chat-1", "req-1", 2, time.Hour); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	reloaded := newReqIDStore(storePath)
	route, ok := reloaded.Get("chat-1")
	if !ok {
		t.Fatal("expected persisted route to be loaded")
	}
	if route.ChatID != "chat-1" || route.ReqID != "req-1" || route.ChatType != 2 {
		t.Fatalf("loaded route = %+v", route)
	}
	if _, err := os.Stat(storePath); !os.IsNotExist(err) {
		t.Fatalf("SQLite facade wrote legacy JSON: %v", err)
	}
	if _, err := os.Stat(strings.TrimSuffix(storePath, ".json") + ".db"); err != nil {
		t.Fatalf("SQLite database is missing: %v", err)
	}
}

func TestDefaultReqIDStorePathUsesExplicitPicoclawHome(t *testing.T) {
	home := setWecomPersistenceHome(t)
	want := filepath.Join(home, "channels", "wecom", wecomReqIDDatabaseFilename)
	if got := defaultReqIDStorePath(); got != want {
		t.Fatalf("defaultReqIDStorePath() = %q, want %q", got, want)
	}
	store := newReqIDStore("")
	if err := store.initializationError(); err != nil {
		t.Fatal(err)
	}
	if store.path != want {
		t.Fatalf("default store path = %q, want %q", store.path, want)
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestReqIDStoreSchemaPragmasPermissionsConstraintsAndVersions(t *testing.T) {
	setWecomPersistenceHome(t)
	store := newReqIDStore(filepath.Join(t.TempDir(), "routes.db"))
	if err := store.initializationError(); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time {
		return time.Date(2500, time.January, 1, 1, 2, 3, 456789123, time.UTC)
	}
	if err := store.Put("chat", "request", ^uint32(0), time.Hour); err != nil {
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
	if err := validateWecomRouteSchema(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	unlock()

	raw := openRawWecomDB(t, store.path)
	defer raw.Close()
	var rowVersion int
	var seconds int64
	var nanoseconds int
	if err := raw.QueryRow(`SELECT version, expires_at_unix_seconds, expires_at_nanosecond
        FROM wecom_request_routes WHERE chat_id = 'chat'`).Scan(
		&rowVersion,
		&seconds,
		&nanoseconds,
	); err != nil {
		t.Fatal(err)
	}
	if rowVersion != 1 || !time.Unix(seconds, int64(nanoseconds)).Equal(store.now().Add(time.Hour)) {
		t.Fatalf("stored row = version:%d expiry:%v", rowVersion, time.Unix(seconds, int64(nanoseconds)))
	}
	if err := store.Put("chat", "request-2", 1, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT version FROM wecom_request_routes WHERE chat_id = 'chat'`).Scan(
		&rowVersion,
	); err != nil || rowVersion != 2 {
		t.Fatalf("updated version = %d, %v", rowVersion, err)
	}
	for name, statement := range map[string]string{
		"empty chat": `INSERT INTO wecom_request_routes (
            chat_id, request_id, chat_type, version) VALUES ('', 'r', 1, 1)`,
		"chat type": `UPDATE wecom_request_routes SET chat_type = 4294967296 WHERE chat_id = 'chat'`,
		"partial time": `UPDATE wecom_request_routes
            SET expires_at_unix_seconds = 1, expires_at_nanosecond = NULL WHERE chat_id = 'chat'`,
		"zero version": `UPDATE wecom_request_routes SET version = 0 WHERE chat_id = 'chat'`,
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
}

func TestReqIDStoreCRUDExpiryAndConcurrentInstances(t *testing.T) {
	setWecomPersistenceHome(t)
	path := filepath.Join(t.TempDir(), "routes.json")
	first := newReqIDStore(path)
	second := newReqIDStore(path)
	base := time.Date(2026, time.August, 31, 10, 0, 0, 0, time.UTC)
	first.now = func() time.Time { return base }
	second.now = func() time.Time { return base }
	const workers = 20
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for index := range workers {
		go func() {
			defer wait.Done()
			<-start
			store := first
			if index%2 != 0 {
				store = second
			}
			errs <- store.Put(
				fmt.Sprintf("chat-%02d", index),
				fmt.Sprintf("req-%02d", index),
				uint32(index),
				time.Hour,
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
	for index := range workers {
		route, ok := first.Get(fmt.Sprintf("chat-%02d", index))
		if !ok || route.ReqID != fmt.Sprintf("req-%02d", index) {
			t.Fatalf("route %d = (%#v, %v)", index, route, ok)
		}
	}
	if err := first.Delete("chat-00"); err != nil {
		t.Fatal(err)
	}
	if _, ok := second.Get("chat-00"); ok {
		t.Fatal("deleted route remains visible")
	}
	first.now = func() time.Time { return base.Add(2 * time.Hour) }
	if _, ok := first.Get("chat-01"); ok {
		t.Fatal("expired route remains visible")
	}
	if err := first.Put("", "", 0, time.Hour); err != nil {
		t.Fatalf("empty Put compatibility error = %v", err)
	}
	if err := first.Delete(""); err != nil {
		t.Fatalf("empty Delete compatibility error = %v", err)
	}
}

func TestReqIDStoreEnforcesRuntimeAndSchemaRowCaps(t *testing.T) {
	setWecomPersistenceHome(t)
	path := filepath.Join(t.TempDir(), "routes.db")
	store := newReqIDStore(path)
	if err := store.initializationError(); err != nil {
		t.Fatal(err)
	}
	db := openRawWecomDB(t, store.path)
	defer db.Close()
	if _, err := db.Exec(`WITH RECURSIVE route_number(value) AS (
        VALUES(1) UNION ALL SELECT value + 1 FROM route_number WHERE value < ?
    ) INSERT INTO wecom_request_routes (
        chat_id, request_id, chat_type, version
    ) SELECT printf('chat-%05d', value), printf('request-%05d', value), 1, 1
      FROM route_number`, wecomReqIDMaxRoutes); err != nil {
		t.Fatal(err)
	}
	if err := store.Put("chat-00001", "updated", 2, time.Hour); err != nil {
		t.Fatalf("existing route update at cap = %v", err)
	}
	if err := store.Put("new-chat", "new-request", 1, time.Hour); err == nil ||
		!strings.Contains(err.Error(), "row count") {
		t.Fatalf("new route beyond cap error = %v", err)
	}
	legacyPath := strings.TrimSuffix(path, ".db") + ".json"
	future := time.Date(2500, time.January, 1, 0, 0, 0, 0, time.UTC)
	if err := os.WriteFile(legacyPath, []byte(fmt.Sprintf(
		`{"legacy-chat":{"req_id":"legacy-request","chat_id":"legacy-chat","chat_type":1,"expires_at":%q}}`,
		future.Format(time.RFC3339Nano),
	)), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened := newReqIDStore(path)
	if err := reopened.initializationError(); err != nil {
		t.Fatalf("late legacy route open error = %v", err)
	}
	if _, ok := reopened.Get("legacy-chat"); ok {
		t.Fatal("late legacy route bypassed the SQLite row cap")
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("late legacy source remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(
		reopened.archiveRoot, filepath.FromSlash(reopened.sourceRelative),
	)); err != nil {
		t.Fatalf("late legacy source was not archived: %v", err)
	}
	var lateIssues int
	if err := db.QueryRow(`SELECT COUNT(*) FROM storage_import_issues
	    WHERE component = ? AND issue_code = 'late-source'`, wecomReqIDComponent).Scan(
		&lateIssues,
	); err != nil || lateIssues != 1 {
		t.Fatalf("late legacy audit = %d, %v", lateIssues, err)
	}
	if _, err := db.Exec(`INSERT INTO wecom_request_routes (
        chat_id, request_id, chat_type, version
    ) VALUES ('over-cap', 'over-cap', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := newReqIDStore(path).initializationError(); err == nil ||
		!strings.Contains(err.Error(), "row count") {
		t.Fatalf("over-cap schema error = %v", err)
	}
}

func TestReqIDStoreLegacyMigrationAuditArchiveAndIdempotence(t *testing.T) {
	setWecomPersistenceHome(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(root, "routes.json")
	future := time.Date(2500, time.August, 31, 12, 0, 0, 123, time.UTC)
	data := []byte(fmt.Sprintf(`{
        "chat-a":{"req_id":"first","chat_id":"wrong","chat_type":2,"expires_at":%q},
        "chat-a":{"req_id":"later","chat_id":"chat-a","chat_type":3,"expires_at":%q},
        "chat-invalid":{"req_id":"","chat_id":"chat-invalid","chat_type":1},
		"chat-expired":{"req_id":"expired","chat_id":"chat-expired","chat_type":1,"expires_at":"2000-01-01T00:00:00Z"},
        "chat-b":"invalid"
    }`, future.Format(time.RFC3339Nano), future.Format(time.RFC3339Nano)))
	if err := os.WriteFile(legacyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := newReqIDStore(legacyPath)
	store.now = func() time.Time { return future.Add(-time.Hour) }
	// Constructor migration used the real clock; future is intentionally beyond
	// the test execution date and remains valid regardless of the seam above.
	if err := store.initializationError(); err != nil {
		t.Fatal(err)
	}
	route, ok := store.Get("chat-a")
	if !ok || route.ReqID != "first" || route.ChatID != "chat-a" || route.ChatType != 2 {
		t.Fatalf("migrated duplicate route = (%#v, %v)", route, ok)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy route source still exists: %v", err)
	}
	archive := filepath.Join(root, "legacy-json", wecomReqIDLegacyArchiveLabel, "routes.json")
	if archived, err := os.ReadFile(archive); err != nil || !bytes.Equal(archived, data) {
		t.Fatalf("archive = %q, %v", archived, err)
	}
	db := openRawWecomDB(t, store.path)
	defer db.Close()
	var imported, skipped, issues int
	if err := db.QueryRow(`SELECT imported_count, skipped_count FROM storage_imports
        WHERE component = ?`, wecomReqIDComponent).Scan(&imported, &skipped); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM storage_import_issues WHERE component = ?`,
		wecomReqIDComponent,
	).Scan(&issues); err != nil {
		t.Fatal(err)
	}
	if imported != 1 || skipped != 4 || issues != 4 {
		t.Fatalf("migration audit = imported:%d skipped:%d issues:%d", imported, skipped, issues)
	}
	reopened := newReqIDStore(legacyPath)
	if err := reopened.initializationError(); err != nil {
		t.Fatal(err)
	}
	if route, ok := reopened.Get("chat-a"); !ok || route.ReqID != "first" {
		t.Fatalf("idempotent reopen route = (%#v, %v)", route, ok)
	}
}

func TestReqIDStoreDefaultLegacyLocationMigratesToChannelDatabase(t *testing.T) {
	home := setWecomPersistenceHome(t)
	legacyPath := filepath.Join(home, "wecom", wecomReqIDLegacyFilename)
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	future := time.Date(2500, time.September, 1, 2, 3, 4, 5, time.UTC)
	data := []byte(fmt.Sprintf(
		`{"chat":{"req_id":"request","chat_id":"chat","chat_type":7,"expires_at":%q}}`,
		future.Format(time.RFC3339Nano),
	))
	if err := os.WriteFile(legacyPath, data, 0o640); err != nil {
		t.Fatal(err)
	}
	store := newReqIDStore("")
	if err := store.initializationError(); err != nil {
		t.Fatal(err)
	}
	if store.path != filepath.Join(home, "channels", "wecom", wecomReqIDDatabaseFilename) {
		t.Fatalf("default database path = %q", store.path)
	}
	if route, ok := store.Get("chat"); !ok || route.ReqID != "request" || route.ChatType != 7 {
		t.Fatalf("default migrated route = (%#v, %v)", route, ok)
	}
	archive := filepath.Join(
		home,
		"legacy-json",
		wecomReqIDLegacyArchiveLabel,
		"wecom",
		wecomReqIDLegacyFilename,
	)
	archived, err := os.ReadFile(archive)
	if err != nil || !bytes.Equal(archived, data) {
		t.Fatalf("default archive = %q, %v", archived, err)
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(archive)
		if statErr != nil || info.Mode().Perm() != 0o640 {
			t.Fatalf("default archive mode = %v, %v", info, statErr)
		}
	}
}

func TestReqIDStoreSQLiteAuthorityWinsLateLegacyConflict(t *testing.T) {
	setWecomPersistenceHome(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(root, "routes.db")
	store := newReqIDStore(databasePath)
	if err := store.Put("chat", "sqlite-request", 1, time.Hour); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(root, "routes.json")
	future := time.Date(2500, time.January, 1, 0, 0, 0, 0, time.UTC)
	data := []byte(fmt.Sprintf(
		`{"chat":{"req_id":"legacy-request","chat_id":"chat","chat_type":2,"expires_at":%q}}`,
		future.Format(time.RFC3339Nano),
	))
	if err := os.WriteFile(legacyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened := newReqIDStore(databasePath)
	if err := reopened.initializationError(); err != nil {
		t.Fatal(err)
	}
	if route, ok := reopened.Get("chat"); !ok || route.ReqID != "sqlite-request" {
		t.Fatalf("authoritative route = (%#v, %v)", route, ok)
	}
	db := openRawWecomDB(t, databasePath)
	defer db.Close()
	var imported, skipped int
	if err := db.QueryRow(`SELECT imported_count, skipped_count FROM storage_imports
        WHERE component = ?`, wecomReqIDComponent).Scan(&imported, &skipped); err != nil {
		t.Fatal(err)
	}
	if imported != 0 || skipped != 1 {
		t.Fatalf("authoritative import counts = %d/%d", imported, skipped)
	}
}

func TestReqIDStoreMalformedUnsafeAndOversizedLegacySources(t *testing.T) {
	t.Run("malformed audited", func(t *testing.T) {
		setWecomPersistenceHome(t)
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		legacyPath := filepath.Join(root, "routes.json")
		data := []byte(`{"chat":"private-request-canary"`)
		if err := os.WriteFile(legacyPath, data, 0o600); err != nil {
			t.Fatal(err)
		}
		store := newReqIDStore(legacyPath)
		if err := store.initializationError(); err != nil {
			t.Fatal(err)
		}
		db := openRawWecomDB(t, store.path)
		defer db.Close()
		var code string
		var digest []byte
		if err := db.QueryRow(`SELECT issue_code, record_digest FROM storage_import_issues
            WHERE component = ?`, wecomReqIDComponent).Scan(&code, &digest); err != nil {
			t.Fatal(err)
		}
		if code != "malformed-json" || len(digest) != sha256.Size ||
			strings.Contains(code, "private-request-canary") {
			t.Fatalf("unsafe malformed audit = %q/%x", code, digest)
		}
	})

	if runtime.GOOS != "windows" {
		t.Run("unsafe mode", func(t *testing.T) {
			setWecomPersistenceHome(t)
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "routes.json")
			if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o622); err != nil {
				t.Fatal(err)
			}
			if err := newReqIDStore(path).initializationError(); err == nil ||
				!strings.Contains(err.Error(), "unsafe") {
				t.Fatalf("unsafe mode error = %v", err)
			}
		})
	}

	t.Run("symlink", func(t *testing.T) {
		setWecomPersistenceHome(t)
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(outside, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "routes.json")
		if err := os.Symlink(outside, path); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := newReqIDStore(path).initializationError(); err == nil ||
			(!strings.Contains(err.Error(), "symlink") && !strings.Contains(err.Error(), "unsafe")) {
			t.Fatalf("symlink error = %v", err)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		setWecomPersistenceHome(t)
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "routes.json")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(wecomReqIDLegacyMaxBytes + 1); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := newReqIDStore(path).initializationError(); err == nil ||
			!strings.Contains(err.Error(), "size limit") {
			t.Fatalf("oversized error = %v", err)
		}
	})

	t.Run("record count", func(t *testing.T) {
		setWecomPersistenceHome(t)
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "routes.json")
		if err := os.WriteFile(path, overLimitWecomLegacyRoutes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := newReqIDStore(path).initializationError(); !errors.Is(err, errWecomLegacyRouteLimit) {
			t.Fatalf("legacy route count migration error = %v", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("over-limit source was removed: %v", err)
		}
		archive := filepath.Join(root, "legacy-json", wecomReqIDLegacyArchiveLabel, "routes.json")
		if _, err := os.Stat(archive); !os.IsNotExist(err) {
			t.Fatalf("over-limit source was archived: %v", err)
		}
	})
}

func TestReqIDStoreRejectsFutureInvalidCorruptAndSymlinkedDatabases(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, *reqIDStore){
		"future": func(t *testing.T, store *reqIDStore) {
			db := openRawWecomDB(t, store.path)
			defer db.Close()
			if _, err := db.Exec(`PRAGMA user_version = 2`); err != nil {
				t.Fatal(err)
			}
		},
		"invalid schema": func(t *testing.T, store *reqIDStore) {
			db := openRawWecomDB(t, store.path)
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE rogue_wecom_state (value TEXT)`); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			setWecomPersistenceHome(t)
			path := filepath.Join(t.TempDir(), "routes.db")
			store := newReqIDStore(path)
			if err := store.initializationError(); err != nil {
				t.Fatal(err)
			}
			mutate(t, store)
			reopened := newReqIDStore(path)
			if reopened.initializationError() == nil {
				t.Fatalf("%s database was accepted", name)
			}
		})
	}

	t.Run("corrupt", func(t *testing.T) {
		setWecomPersistenceHome(t)
		path := filepath.Join(t.TempDir(), "routes.db")
		if err := os.WriteFile(path, []byte("not SQLite"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := newReqIDStore(path).initializationError(); err == nil {
			t.Fatal("corrupt database was accepted")
		}
	})

	t.Run("database symlink", func(t *testing.T) {
		setWecomPersistenceHome(t)
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside.db")
		if err := os.WriteFile(outside, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "routes.db")
		if err := os.Symlink(outside, path); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := newReqIDStore(path).initializationError(); err == nil ||
			(!strings.Contains(err.Error(), "regular file") && !strings.Contains(err.Error(), "unsafe member")) {
			t.Fatalf("database symlink error = %v", err)
		}
	})

	t.Run("lock symlink", func(t *testing.T) {
		setWecomPersistenceHome(t)
		path := filepath.Join(t.TempDir(), "routes.db")
		lockDirectory := path + ".locks"
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
		if err := newReqIDStore(path).initializationError(); err == nil ||
			!strings.Contains(err.Error(), "regular file") {
			t.Fatalf("lock symlink error = %v", err)
		}
	})
}

func TestReqIDStoreSerializesAcrossProcesses(t *testing.T) {
	if os.Getenv("PICOCLAW_WECOM_STORE_HELPER") == "1" {
		store := newReqIDStore(os.Getenv("PICOCLAW_WECOM_STORE_PATH"))
		if err := store.initializationError(); err != nil {
			t.Fatal(err)
		}
		chatID := os.Getenv("PICOCLAW_WECOM_STORE_CHAT")
		if err := store.Put(chatID, "req-"+chatID, 1, time.Hour); err != nil {
			t.Fatal(err)
		}
		return
	}
	home := setWecomPersistenceHome(t)
	path := filepath.Join(t.TempDir(), "routes.db")
	store := newReqIDStore(path)
	commands := make([]*exec.Cmd, 0, 2)
	outputs := make([]bytes.Buffer, 2)
	for index := range 2 {
		command := exec.Command(os.Args[0], "-test.run=^TestReqIDStoreSerializesAcrossProcesses$")
		command.Env = append(
			os.Environ(),
			config.EnvHome+"="+home,
			"PICOCLAW_WECOM_STORE_HELPER=1",
			"PICOCLAW_WECOM_STORE_PATH="+path,
			fmt.Sprintf("PICOCLAW_WECOM_STORE_CHAT=chat-%d", index),
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
	for index := range 2 {
		if route, ok := store.Get(fmt.Sprintf("chat-%d", index)); !ok || route.ReqID == "" {
			t.Fatalf("cross-process route %d = (%#v, %v)", index, route, ok)
		}
	}
}

func openRawWecomDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func setWecomPersistenceHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvHome, home)
	return home
}

func TestReqIDStoreInternalValidationBoundaries(t *testing.T) {
	setWecomPersistenceHome(t)
	store := newReqIDStore(filepath.Join(t.TempDir(), "routes.db"))
	if err := store.Put(strings.Repeat("x", wecomReqIDMaxValueBytes+1), "request", 1, time.Hour); err == nil {
		t.Fatal("oversized chat ID was accepted")
	}
	if err := store.Put("chat", string([]byte{0xff}), 1, time.Hour); err == nil {
		t.Fatal("invalid UTF-8 request ID was accepted")
	}
	store.now = func() time.Time {
		return time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	}
	if err := store.Put("chat", "request", 1, time.Hour); err == nil {
		t.Fatal("unsupported expiry was accepted")
	}
	var nilStore *reqIDStore
	if _, ok := nilStore.Get("chat"); ok {
		t.Fatal("nil store returned a route")
	}
	if err := nilStore.load(); err == nil {
		t.Fatal("nil store load error = nil")
	}
	if _, err := lockWecomReqIDDatabase(""); err == nil {
		t.Fatal("empty lock database path was accepted")
	}
	if _, _, _, _, err := resolveReqIDStorePaths("invalid\x00path"); err == nil {
		t.Fatal("invalid custom path was accepted")
	}
	if _, _, _, _, err := resolveReqIDStorePaths("   "); err == nil {
		t.Fatal("whitespace custom path was accepted")
	}
	legacyWithoutExtension := filepath.Join(t.TempDir(), "routes")
	databasePath, sourceRoot, sourceRelative, _, err := resolveReqIDStorePaths(
		legacyWithoutExtension,
	)
	if err != nil {
		t.Fatal(err)
	}
	if databasePath != legacyWithoutExtension+".db" ||
		sourceRoot != filepath.Dir(legacyWithoutExtension) ||
		sourceRelative != filepath.Base(legacyWithoutExtension) {
		t.Fatalf(
			"extensionless paths = database:%q root:%q source:%q",
			databasePath,
			sourceRoot,
			sourceRelative,
		)
	}
	if _, err := decodeLegacyWecomRoutes(overLimitWecomLegacyRoutes()); !errors.Is(err, errWecomLegacyRouteLimit) {
		t.Fatalf("legacy route count error = %v", err)
	}
}

type wecomRouteScannerFunc func(...any) error

func (f wecomRouteScannerFunc) Scan(dest ...any) error { return f(dest...) }

func TestReqIDStoreInternalFailureBoundaries(t *testing.T) { //nolint:gocognit // Fault matrix is intentionally linear.
	setWecomPersistenceHome(t)
	sentinel := errors.New("injected request-route failure")
	failed := &reqIDStore{initErr: sentinel, now: time.Now}
	if err := failed.initializationError(); !errors.Is(err, sentinel) {
		t.Fatalf("initialization error = %v", err)
	}
	if err := failed.Put("chat", "request", 1, time.Hour); !errors.Is(err, sentinel) {
		t.Fatalf("Put open error = %v", err)
	}
	if _, ok := failed.Get("chat"); ok {
		t.Fatal("Get returned state after open failure")
	}
	if err := failed.Delete("chat"); !errors.Is(err, sentinel) {
		t.Fatalf("Delete open error = %v", err)
	}
	if err := failed.load(); !errors.Is(err, sentinel) {
		t.Fatalf("load open error = %v", err)
	}
	if _, _, err := failed.open(t.Context()); !errors.Is(err, sentinel) {
		t.Fatalf("open error = %v", err)
	}
	var nilStore *reqIDStore
	if err := nilStore.initializationError(); err == nil {
		t.Fatal("nil initialization error = nil")
	}
	if err := failed.Delete(strings.Repeat("x", wecomReqIDMaxValueBytes+1)); err == nil {
		t.Fatal("Delete accepted oversized identity")
	}

	root := t.TempDir()
	blockingParent := filepath.Join(root, "blocking")
	if err := os.WriteFile(blockingParent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lockWecomReqIDDatabase(filepath.Join(blockingParent, "routes.db")); err == nil {
		t.Fatal("database lock accepted a file parent")
	}
	blockedLockDatabase := filepath.Join(root, "blocked-lock.db")
	if err := os.WriteFile(blockedLockDatabase+".locks", []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lockWecomReqIDDatabase(blockedLockDatabase); err == nil {
		t.Fatal("database lock accepted a lock-directory file")
	}
	sourceFailure := &reqIDStore{
		sourceRoot:     root,
		sourceRelative: "blocking/source.json",
		archiveRoot:    filepath.Join(root, "legacy-json", wecomReqIDLegacyArchiveLabel),
		now:            time.Now,
	}
	if sources, err := sourceFailure.options().Legacy.Sources(); err == nil || sources != nil {
		t.Fatalf("legacy source enumeration = %#v, %v", sources, err)
	}

	store := newReqIDStore(filepath.Join(t.TempDir(), "routes.db"))
	db := openRawWecomDB(t, store.path)
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateWecomRouteSchema(t.Context(), conn); err == nil {
		t.Fatal("schema validation accepted closed connection")
	}
	if _, err := sourceFailure.importLegacy(t.Context(), conn, sqlitestore.LegacyInput{
		Data: []byte(`{}`),
	}); err == nil {
		t.Fatal("legacy import accepted closed connection")
	}
	if err := deleteExpiredWecomRoutes(t.Context(), conn, time.Now()); err == nil {
		t.Fatal("expiry deletion accepted closed connection")
	}
	if err := ensureWecomRouteCapacity(t.Context(), conn, "chat"); err == nil {
		t.Fatal("capacity check accepted closed connection")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for name, data := range map[string][]byte{
		"empty":            nil,
		"array":            []byte(`[]`),
		"truncated value":  []byte(`{"chat":`),
		"missing close":    []byte(`{"chat":null`),
		"trailing value":   []byte(`{} []`),
		"invalid trailing": []byte(`{} ?`),
	} {
		t.Run("decode "+name, func(t *testing.T) {
			if _, err := decodeLegacyWecomRoutes(data); err == nil {
				t.Fatal("invalid legacy routes were accepted")
			}
		})
	}
	if err := validateWecomRoute(wecomRoute{ChatID: "chat"}); err == nil {
		t.Fatal("empty request ID was accepted")
	}
	if err := validateWecomRoute(wecomRoute{
		ChatID: strings.Repeat("x", wecomReqIDMaxValueBytes+1),
		ReqID:  "request",
	}); err == nil {
		t.Fatal("oversized route identity was accepted")
	}
	if seconds, nanoseconds, err := wecomNullableTimestampValues(time.Time{}); err != nil ||
		seconds != nil || nanoseconds != nil {
		t.Fatalf("zero timestamp = (%v, %v, %v)", seconds, nanoseconds, err)
	}

	scanValues := func(chatType int64, seconds, nanoseconds sql.NullInt64) wecomRouteScannerFunc {
		return func(dest ...any) error {
			*dest[0].(*string) = "request"
			*dest[1].(*string) = "chat"
			*dest[2].(*int64) = chatType
			*dest[3].(*sql.NullInt64) = seconds
			*dest[4].(*sql.NullInt64) = nanoseconds
			*dest[5].(*int64) = 1
			return nil
		}
	}
	if _, _, err := scanWecomRoute(wecomRouteScannerFunc(func(...any) error {
		return sentinel
	})); !errors.Is(err, sentinel) {
		t.Fatalf("scanner error = %v", err)
	}
	if _, _, err := scanWecomRoute(scanValues(-1, sql.NullInt64{}, sql.NullInt64{})); err == nil {
		t.Fatal("negative chat type was accepted")
	}
	if _, _, err := scanWecomRoute(scanValues(
		1,
		sql.NullInt64{Int64: 1, Valid: true},
		sql.NullInt64{},
	)); err == nil {
		t.Fatal("partial expiry was accepted")
	}
}

func overLimitWecomLegacyRoutes() []byte {
	var data strings.Builder
	data.WriteByte('{')
	for index := range wecomReqIDMaxRoutes + 1 {
		if index != 0 {
			data.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&data, "%q:null", fmt.Sprintf("chat-%05d", index))
	}
	data.WriteByte('}')
	return []byte(data.String())
}

func TestReqIDStoreTooNewErrorIsTyped(t *testing.T) {
	setWecomPersistenceHome(t)
	path := filepath.Join(t.TempDir(), "routes.db")
	store := newReqIDStore(path)
	db := openRawWecomDB(t, store.path)
	if _, err := db.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := newReqIDStore(path).initializationError(); !errors.Is(err, sqlitestore.ErrTooNew) {
		t.Fatalf("too-new error = %v", err)
	}
}
