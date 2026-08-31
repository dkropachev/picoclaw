package accountrouter

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
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestAccountRouterSQLiteSchemaPragmasPermissionsAndFacade(t *testing.T) {
	workspace := privateAccountRouterWorkspace(t)
	databasePath := DatabasePath(workspace)
	now := time.Date(2500, time.January, 2, 3, 4, 5, 678901234, time.UTC)
	router, err := NewSQLite(
		"router-main",
		testAccountRouterConfig(),
		testAccountRouterAccounts(),
		databasePath,
	)
	if err != nil {
		t.Fatal(err)
	}
	router.now = func() time.Time { return now }
	selection := router.Select("session", SelectReasonInitial)
	if selectedAccount(t, selection) != "account-a" {
		t.Fatalf("selection = %#v", selection)
	}
	router.RecordFallbackResult(selection, successResult(selection, 42), nil)

	db, unlock, err := router.store.open(t.Context())
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
			version, foreignKeys, busyTimeout, synchronous, journal,
		)
	}
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAccountRouterSchema(t.Context(), conn); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	unlock()
	db = openRawAccountRouterDB(t, databasePath)
	defer db.Close()
	var requests, totalTokens, accountVersion int64
	var successSeconds int64
	var successNanos int
	if err := db.QueryRow(`SELECT requests, total_tokens, last_success_at_unix_seconds,
        last_success_at_nanosecond, version FROM account_router_accounts
        WHERE router_name = 'router-main' AND account_ref = 'account-a'`).Scan(
		&requests, &totalTokens, &successSeconds, &successNanos, &accountVersion,
	); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || totalTokens != 42 || accountVersion < 2 ||
		!time.Unix(successSeconds, int64(successNanos)).Equal(now) {
		t.Fatalf(
			"account row = requests:%d tokens:%d version:%d success:%v",
			requests, totalTokens, accountVersion, time.Unix(successSeconds, int64(successNanos)),
		)
	}
	for name, statement := range map[string]string{
		"invalid health": `UPDATE account_router_accounts SET health_state = 'broken'
            WHERE router_name = 'router-main' AND account_ref = 'account-a'`,
		"negative count": `UPDATE account_router_accounts SET requests = -1
            WHERE router_name = 'router-main' AND account_ref = 'account-a'`,
		"invalid reason": `UPDATE account_router_accounts SET failure_reason = 'private'
            WHERE router_name = 'router-main' AND account_ref = 'account-a'`,
		"partial time": `UPDATE account_router_accounts
            SET last_success_at_nanosecond = NULL
            WHERE router_name = 'router-main' AND account_ref = 'account-a'`,
		"bad affinity reason": `UPDATE account_router_session_affinities
            SET select_reason = 'other' WHERE router_name = 'router-main'`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := db.Exec(statement); err == nil {
				t.Fatalf("constraint accepted %s", name)
			}
		})
	}
	if runtime.GOOS != "windows" {
		for path, mode := range map[string]os.FileMode{
			databasePath:               0o600,
			databasePath + "-wal":      0o600,
			databasePath + "-shm":      0o600,
			filepath.Dir(databasePath): 0o700,
			databasePath + ".locks":    0o700,
			filepath.Join(databasePath+".locks", "store.lock"): 0o600,
		} {
			info, statErr := os.Stat(path)
			if statErr != nil || info.Mode().Perm() != mode {
				t.Fatalf("mode for %s = %v, %v; want %o", path, info, statErr, mode)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(workspace, accountRouterLegacyFilename)); !os.IsNotExist(err) {
		t.Fatalf("mutable legacy JSON was written: %v", err)
	}
	if router.StatePath != databasePath {
		t.Fatalf("StatePath = %q, want %q", router.StatePath, databasePath)
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestAccountRouterLegacyStateAndInvalidationMigration(t *testing.T) {
	root := privateAccountRouterWorkspace(t)
	legacyPath := filepath.Join(root, accountRouterLegacyFilename)
	now := time.Date(2026, time.August, 31, 12, 0, 0, 123, time.UTC)
	valid := RouterState{
		ConfigHash: "0123456789abcdef",
		Accounts: map[string]*AccountState{
			"credential:openai:work": {
				State: "unavailable", Reason: providers.FailoverAuth, FailureCount: 2,
				Requests: 3, LastFailureAt: now, UnavailableUntil: now.Add(time.Hour),
				LastError: "authentication failed",
			},
		},
		Sessions: map[string]*SessionState{
			"session": {
				ConfigHash: "0123456789abcdef", UpdatedAt: now,
				Blocks: map[string]BlockAffinity{
					"pool": {Account: "credential:openai:work", Reason: SelectReasonInitial, SelectedAt: now},
				},
			},
		},
		Blocks:    map[string]*BlockRunState{"pool": {Cursor: 3, UpdatedAt: now}},
		UpdatedAt: now,
	}
	validRaw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	legacyData := []byte(fmt.Sprintf(
		`{"version":1,"routers":{"router-main":%s,"router-main":{"config_hash":"later"},"bad":null}}`,
		validRaw,
	))
	if err := os.WriteFile(legacyPath, legacyData, 0o640); err != nil {
		t.Fatal(err)
	}
	normalizedCredential := "openai:work"
	sidecarName := legacyInvalidationFilename(filepath.Base(legacyPath), normalizedCredential)
	validSidecar := []byte(`{"version":1,"credential_id":"OPENAI:WORK","generation":"generation-1"}`)
	if err := os.WriteFile(filepath.Join(root, sidecarName), validSidecar, 0o600); err != nil {
		t.Fatal(err)
	}
	invalidSidecarName := filepath.Base(legacyPath) + ".auth-invalidation.ffffffffffffffffffffffffffffffff"
	invalidSidecar := []byte(`{"version":2,"credential_id":"openai:other"}`)
	if err := os.WriteFile(filepath.Join(root, invalidSidecarName), invalidSidecar, 0o600); err != nil {
		t.Fatal(err)
	}

	router, err := NewSQLite(
		"unrelated-router",
		testAccountRouterConfig(),
		testAccountRouterAccounts(),
		legacyPath,
	)
	if err != nil {
		t.Fatal(err)
	}
	databasePath := strings.TrimSuffix(legacyPath, ".json") + ".db"
	if router.StatePath != databasePath {
		t.Fatalf("facade database = %q", router.StatePath)
	}
	migrated := router.store.st.Routers["router-main"]
	if migrated == nil || migrated.Accounts["credential:openai:work"] == nil ||
		migrated.Accounts["credential:openai:work"].FailureCount != 2 ||
		migrated.Sessions["session"].Blocks["pool"].Account != "credential:openai:work" ||
		migrated.Blocks["pool"].Cursor != 3 {
		t.Fatalf("migrated router = %#v", migrated)
	}

	db := openRawAccountRouterDB(t, databasePath)
	defer db.Close()
	var generation string
	if err := db.QueryRow(`SELECT generation FROM account_router_auth_invalidations
        WHERE credential_id = ?`, normalizedCredential).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation != "generation-1" {
		t.Fatalf("invalidation generation = %q", generation)
	}
	var imported, skipped, issues int
	if err := db.QueryRow(`SELECT COALESCE(SUM(imported_count), 0),
        COALESCE(SUM(skipped_count), 0) FROM storage_imports
        WHERE component = ?`, accountRouterDatabaseComponent).Scan(&imported, &skipped); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM storage_import_issues
        WHERE component = ?`, accountRouterDatabaseComponent).Scan(&issues); err != nil {
		t.Fatal(err)
	}
	if imported != 2 || skipped != 3 || issues != 3 {
		t.Fatalf("migration audit = imported:%d skipped:%d issues:%d", imported, skipped, issues)
	}
	archiveRoot := filepath.Join(root, "legacy-json", accountRouterLegacyArchiveLabel)
	for name, want := range map[string][]byte{
		filepath.Base(legacyPath): legacyData,
		sidecarName:               validSidecar,
		invalidSidecarName:        invalidSidecar,
	} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("legacy source %s remains: %v", name, err)
		}
		archived, err := os.ReadFile(filepath.Join(archiveRoot, name))
		if err != nil || !bytes.Equal(archived, want) {
			t.Fatalf("archive %s = %q, %v", name, archived, err)
		}
	}
	stores.Delete(databasePath)
	if _, err := NewSQLite(
		"unrelated-router", testAccountRouterConfig(), testAccountRouterAccounts(), legacyPath,
	); err != nil {
		t.Fatalf("idempotent reopen = %v", err)
	}
}

//nolint:govet // Narrow test assertions intentionally use independent error scopes.
func TestAccountRouterRejectsUnsafeLegacyCorruptAndFutureDatabases(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Run("unsafe legacy mode", func(t *testing.T) {
			root := privateAccountRouterWorkspace(t)
			path := filepath.Join(root, accountRouterLegacyFilename)
			if err := os.WriteFile(path, []byte(`{"version":1,"routers":{}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o622); err != nil {
				t.Fatal(err)
			}
			if _, err := NewSQLite(
				"router",
				testAccountRouterConfig(),
				testAccountRouterAccounts(),
				path,
			); err == nil || !strings.Contains(err.Error(), "unsafe") {
				t.Fatalf("unsafe legacy error = %v", err)
			}
		})
	}
	t.Run("legacy symlink", func(t *testing.T) {
		root := privateAccountRouterWorkspace(t)
		outside := filepath.Join(t.TempDir(), "outside.json")
		if err := os.WriteFile(outside, []byte(`{"version":1,"routers":{}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, accountRouterLegacyFilename)
		if err := os.Symlink(outside, path); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := NewSQLite(
			"router",
			testAccountRouterConfig(),
			testAccountRouterAccounts(),
			path,
		); err == nil ||
			(!strings.Contains(err.Error(), "symlink") && !strings.Contains(err.Error(), "unsafe")) {
			t.Fatalf("legacy symlink error = %v", err)
		}
	})
	t.Run("corrupt database", func(t *testing.T) {
		root := privateAccountRouterWorkspace(t)
		path := filepath.Join(root, "router.db")
		if err := os.WriteFile(path, []byte("not SQLite"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewSQLite("router", testAccountRouterConfig(), testAccountRouterAccounts(), path); err == nil {
			t.Fatal("corrupt database was accepted")
		}
	})
	for name, mutation := range map[string]string{
		"future":         `PRAGMA user_version = 2`,
		"invalid schema": `CREATE TABLE rogue_account_router (value TEXT)`,
	} {
		t.Run(name, func(t *testing.T) {
			root := privateAccountRouterWorkspace(t)
			path := filepath.Join(root, "router.db")
			router, err := NewSQLite("router", testAccountRouterConfig(), testAccountRouterAccounts(), path)
			if err != nil {
				t.Fatal(err)
			}
			_ = router.Select("", SelectReasonInitial)
			db := openRawAccountRouterDB(t, path)
			if _, err := db.Exec(mutation); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			stores.Delete(path)
			_, err = NewSQLite("router", testAccountRouterConfig(), testAccountRouterAccounts(), path)
			if err == nil {
				t.Fatalf("%s database was accepted", name)
			}
			if name == "future" && !errors.Is(err, sqlitestore.ErrTooNew) {
				t.Fatalf("too-new error = %v", err)
			}
		})
	}
}

func TestAccountRouterSerializesAcrossProcesses(t *testing.T) {
	if os.Getenv("PICOCLAW_ACCOUNT_ROUTER_HELPER") == "1" {
		path := os.Getenv("PICOCLAW_ACCOUNT_ROUTER_PATH")
		sessionID := os.Getenv("PICOCLAW_ACCOUNT_ROUTER_SESSION")
		router, err := NewSQLite(
			"router-main", testAccountRouterConfig(), testAccountRouterAccounts(), path,
		)
		if err != nil {
			t.Fatal(err)
		}
		selection := router.Select("session-"+sessionID, SelectReasonInitial)
		router.RecordFallbackResult(selection, successResult(selection, 1), nil)
		return
	}
	root := privateAccountRouterWorkspace(t)
	path := filepath.Join(root, "router.db")
	commands := make([]*exec.Cmd, 0, 2)
	outputs := make([]bytes.Buffer, 2)
	for index := range 2 {
		command := exec.Command(os.Args[0], "-test.run=^TestAccountRouterSerializesAcrossProcesses$")
		command.Env = append(
			os.Environ(),
			"PICOCLAW_ACCOUNT_ROUTER_HELPER=1",
			"PICOCLAW_ACCOUNT_ROUTER_PATH="+path,
			fmt.Sprintf("PICOCLAW_ACCOUNT_ROUTER_SESSION=%d", index),
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
	db := openRawAccountRouterDB(t, path)
	defer db.Close()
	var routers, accounts, sessions, requests int
	if err := db.QueryRow(`SELECT
        (SELECT COUNT(*) FROM account_router_routers),
        (SELECT COUNT(*) FROM account_router_accounts),
        (SELECT COUNT(*) FROM account_router_sessions),
        (SELECT SUM(requests) FROM account_router_accounts)`).Scan(
		&routers, &accounts, &sessions, &requests,
	); err != nil {
		t.Fatal(err)
	}
	if routers != 1 || accounts != 2 || sessions != 2 || requests != 2 {
		t.Fatalf(
			"cross-process rows = routers:%d accounts:%d sessions:%d requests:%d",
			routers, accounts, sessions, requests,
		)
	}
}

func testAccountRouterConfig() *config.AccountRouterConfig {
	return &config.AccountRouterConfig{
		Enabled: true,
		Entry:   "pool",
		Blocks: []config.AccountRouterBlock{{
			ID: "pool", Type: config.AccountRouterBlockTypeLoadBalance,
			Accounts: []string{"account-a", "account-b"},
			Strategy: config.AccountRouterStrategyTokensSpent,
		}},
	}
}

func testAccountRouterAccounts() map[string]Account {
	return map[string]Account{
		"account-a": {Candidates: []providers.FallbackCandidate{candidate("account-a")}, RPM: 60},
		"account-b": {Candidates: []providers.FallbackCandidate{candidate("account-b")}, RPM: 60},
	}
}

func privateAccountRouterWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func openRawAccountRouterDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func legacyInvalidationFilename(base, normalizedCredential string) string {
	digest := sha256.Sum256([]byte(normalizedCredential))
	return fmt.Sprintf("%s.auth-invalidation.%x", base, digest[:16])
}
