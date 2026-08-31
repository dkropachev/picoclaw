package accountrouter

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

type accountRouterQueryerFunc func(context.Context, string, ...any) (*sql.Rows, error)

func (f accountRouterQueryerFunc) QueryContext(
	ctx context.Context,
	query string,
	args ...any,
) (*sql.Rows, error) {
	return f(ctx, query, args...)
}

//nolint:gocognit // Boundary matrix is intentionally linear.
func TestAccountRouterStorePathLockAndLegacyEnumerationBoundaries(t *testing.T) {
	if _, err := resolveAccountRouterStorePaths(" "); err == nil {
		t.Fatal("blank locator was accepted")
	}
	if _, err := resolveAccountRouterStorePaths("bad\x00path"); err == nil {
		t.Fatal("NUL locator was accepted")
	}
	root := privateAccountRouterWorkspace(t)
	customDB := filepath.Join(root, "custom.db")
	paths, err := resolveAccountRouterStorePaths(customDB)
	if err != nil || paths.databasePath != customDB || paths.sourceRelative != "custom.json" {
		t.Fatalf("custom DB paths = %#v, %v", paths, err)
	}
	directoryPaths, err := resolveAccountRouterStorePaths(filepath.Join(root, "workspace"))
	if err != nil || directoryPaths.databasePath != filepath.Join(
		root,
		"workspace",
		"state",
		accountRouterDatabaseFilename,
	) {
		t.Fatalf("directory paths = %#v, %v", directoryPaths, err)
	}
	var nilStore *Store
	if _, _, err := nilStore.open(t.Context()); err == nil {
		t.Fatal("nil store open error = nil")
	}
	if nilStore.hasLegacyState() {
		t.Fatal("nil store has legacy state")
	}
	if _, err := lockAccountRouterDatabase(""); err == nil {
		t.Fatal("blank database lock path was accepted")
	}
	blockingParent := filepath.Join(root, "blocking-parent")
	if err := os.WriteFile(blockingParent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lockAccountRouterDatabase(filepath.Join(blockingParent, "router.db")); err == nil {
		t.Fatal("lock accepted file parent")
	}
	if _, _, err := (&Store{path: filepath.Join(blockingParent, "router.db")}).open(
		t.Context(),
	); err == nil {
		t.Fatal("store open accepted file parent")
	}
	blockedLocks := filepath.Join(root, "blocked.db")
	if err := os.WriteFile(blockedLocks+".locks", []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lockAccountRouterDatabase(blockedLocks); err == nil {
		t.Fatal("lock accepted lock-directory file")
	}

	if _, err := (&Store{}).legacySources(); err == nil {
		t.Fatal("store without legacy identity enumerated sources")
	}
	legacyStore := &Store{
		sourceRoot: root, sourceRelative: accountRouterLegacyFilename,
		archiveRoot: filepath.Join(root, "state", "legacy-json", accountRouterLegacyArchiveLabel),
	}
	if legacyStore.hasLegacyState() {
		t.Fatal("fresh store reported legacy state")
	}
	if err := os.MkdirAll(legacyStore.archiveRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if !legacyStore.hasLegacyState() {
		t.Fatal("archive root did not enable pending legacy handling")
	}
	if err := os.Remove(legacyStore.archiveRoot); err != nil {
		t.Fatal(err)
	}
	badRoot := filepath.Join(root, "source-file")
	if err := os.WriteFile(badRoot, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyStore.sourceRoot = badRoot
	if !legacyStore.hasLegacyState() {
		t.Fatal("source enumeration error did not fail closed")
	}
	if _, err := legacyStore.legacySources(); err == nil {
		t.Fatal("legacy source enumeration accepted file root")
	}

	if _, _, err := decodeLegacyAccountRouterEntries([]byte(`{"routers":{"router":{}`)); err == nil {
		t.Fatal("truncated routers object was accepted")
	}
	if _, err := decodeLegacyRouterObject([]byte(`{"router":{}`)); err == nil {
		t.Fatal("truncated router object was accepted")
	}
	if _, err := decodeLegacyRouterObject([]byte(`{"router":`)); err == nil {
		t.Fatal("truncated router value was accepted")
	}
	if _, err := decodeLegacyRouterObject(nil); err == nil {
		t.Fatal("empty router object was accepted")
	}
	if result, err := importLegacyAccountRouterSource(
		t.Context(), nil, sqlitestore.LegacyInput{ID: "unknown"},
	); err == nil || result.Imported != 0 {
		t.Fatalf("unknown legacy source = (%#v, %v)", result, err)
	}
	result := sqlitestore.ImportResult{Issues: make([]sqlitestore.ImportIssue, 512)}
	appendAccountRouterImportIssue(&result, "bounded", [32]byte{1})
	if result.Skipped != 1 || len(result.Issues) != 512 {
		t.Fatalf("bounded issues = %#v", result)
	}
}

//nolint:gocognit,govet // Loader matrix intentionally uses independent error scopes.
func TestAccountRouterLoadersRejectMalformedRowsAndClosedConnections(t *testing.T) {
	root := privateAccountRouterWorkspace(t)
	store, err := getStore(filepath.Join(root, "router.db"))
	if err != nil {
		t.Fatal(err)
	}
	db := openRawAccountRouterDB(t, store.path)
	defer db.Close()
	query := func(statement string, args ...any) accountRouterQueryer {
		return accountRouterQueryerFunc(func(context.Context, string, ...any) (*sql.Rows, error) {
			return db.Query(statement, args...)
		})
	}
	if _, err := loadAccountRouterState(t.Context(), query(`SELECT 1`)); err == nil {
		t.Fatal("router loader accepted malformed row")
	}
	state := &State{Version: stateVersion, Routers: map[string]*RouterState{}}
	if err := loadAccountRouterAccounts(t.Context(), query(`SELECT
        'missing', 'account', 'operational', '', 0, 0, NULL, NULL,
        0, 0, 0, 0, NULL, NULL, NULL, NULL, NULL, NULL, '', ''`), state); err == nil {
		t.Fatal("account loader accepted missing router")
	}
	if err := loadAccountRouterAccounts(t.Context(), query(`SELECT 1`), state); err == nil {
		t.Fatal("account loader accepted malformed row")
	}
	if err := loadAccountRouterSessions(t.Context(), query(`SELECT
        'missing', 'session', 'hash', 1, 0`), state); err == nil {
		t.Fatal("session loader accepted missing router")
	}
	if err := loadAccountRouterSessions(t.Context(), query(`SELECT 1`), state); err == nil {
		t.Fatal("session loader accepted malformed row")
	}
	if err := loadAccountRouterAffinities(t.Context(), query(`SELECT
        'missing', 'session', 'block', 'account', 'initial', 1, 0`), state); err == nil {
		t.Fatal("affinity loader accepted missing session")
	}
	if err := loadAccountRouterAffinities(t.Context(), query(`SELECT 1`), state); err == nil {
		t.Fatal("affinity loader accepted malformed row")
	}
	if err := loadAccountRouterBlockCursors(t.Context(), query(`SELECT
        'missing', 'block', 0, NULL, NULL`), state); err == nil {
		t.Fatal("block loader accepted missing router")
	}
	if err := loadAccountRouterBlockCursors(t.Context(), query(`SELECT 1`), state); err == nil {
		t.Fatal("block loader accepted malformed row")
	}

	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAccountRouterState(t.Context(), conn); err == nil {
		t.Fatal("state loader accepted closed connection")
	}
	if err := loadAccountRouterAccounts(t.Context(), conn, state); err == nil {
		t.Fatal("account loader accepted closed connection")
	}
	if err := loadAccountRouterSessions(t.Context(), conn, state); err == nil {
		t.Fatal("session loader accepted closed connection")
	}
	if err := loadAccountRouterAffinities(t.Context(), conn, state); err == nil {
		t.Fatal("affinity loader accepted closed connection")
	}
	if err := loadAccountRouterBlockCursors(t.Context(), conn, state); err == nil {
		t.Fatal("block loader accepted closed connection")
	}
	if err := writeAccountRouterState(t.Context(), conn, &State{Routers: map[string]*RouterState{}}); err == nil {
		t.Fatal("state writer accepted closed connection")
	}
	if err := store.applyCredentialAuthInvalidations(t.Context(), conn, state); err == nil {
		t.Fatal("invalidation loader accepted closed connection")
	}
	if err := validateAccountRouterSchema(t.Context(), conn); err == nil {
		t.Fatal("schema validation accepted closed connection")
	}
}

//nolint:gocognit,govet // Import matrix intentionally uses independent error scopes.
func TestAccountRouterDirectLegacyImportAndInvalidationBoundaryMatrix(t *testing.T) {
	root := privateAccountRouterWorkspace(t)
	store, err := getStore(filepath.Join(root, "router.db"))
	if err != nil {
		t.Fatal(err)
	}
	db := openRawAccountRouterDB(t, store.path)
	defer db.Close()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	input := func(data string) sqlitestore.LegacyInput {
		return sqlitestore.LegacyInput{
			ID: accountRouterLegacySourceID, Data: []byte(data),
			Digest: [32]byte{1},
		}
	}
	for name, data := range map[string]string{
		"unsupported version":  `{"version":2,"routers":{}}`,
		"invalid router value": `{"version":1,"routers":{"router":"invalid"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			result, err := importLegacyAccountRouterState(t.Context(), conn, input(data))
			if err != nil || result.Skipped != 1 {
				t.Fatalf("legacy state import = (%#v, %v)", result, err)
			}
		})
	}
	validRouter := `{"version":1,"routers":{"router":{"config_hash":"hash","accounts":{},"sessions":{},"blocks":{},"updated_at":"2026-08-31T12:00:00Z"}}}`
	if result, err := importLegacyAccountRouterState(
		t.Context(), conn, input(validRouter),
	); err != nil || result.Imported != 1 {
		t.Fatalf("first direct legacy import = (%#v, %v)", result, err)
	}
	if result, err := importLegacyAccountRouterState(
		t.Context(), conn, input(validRouter),
	); err != nil || result.Skipped != 1 {
		t.Fatalf("authoritative direct legacy import = (%#v, %v)", result, err)
	}

	markerInput := func(data string) sqlitestore.LegacyInput {
		return sqlitestore.LegacyInput{
			ID: accountRouterLegacySidecarPrefix + "direct", Data: []byte(data),
			Digest: [32]byte{2},
		}
	}
	for name, data := range map[string]string{
		"invalid credential": `{"version":1,"credential_id":"openai:bad/name","generation":"g"}`,
		"invalid generation": `{"version":1,"credential_id":"openai:work","generation":"bad\u0000generation"}`,
	} {
		t.Run(name, func(t *testing.T) {
			result, err := importLegacyAccountRouterInvalidation(t.Context(), conn, markerInput(data))
			if err != nil || result.Skipped != 1 {
				t.Fatalf("legacy invalidation = (%#v, %v)", result, err)
			}
		})
	}
	validMarker := `{"version":1,"credential_id":"openai:work","generation":"generation"}`
	if result, err := importLegacyAccountRouterInvalidation(
		t.Context(), conn, markerInput(validMarker),
	); err != nil || result.Imported != 1 {
		t.Fatalf("first invalidation import = (%#v, %v)", result, err)
	}
	if result, err := importLegacyAccountRouterInvalidation(
		t.Context(), conn, markerInput(validMarker),
	); err != nil || result.Skipped != 1 {
		t.Fatalf("authoritative invalidation import = (%#v, %v)", result, err)
	}

	state := &State{Routers: map[string]*RouterState{
		"nil": nil,
		"sparse": {Accounts: map[string]*AccountState{
			"nil":                       nil,
			"plain":                     {},
			"credential:openai:missing": {},
			"credential:openai:work":    {AuthInvalidationGeneration: "generation"},
		}},
	}}
	if err := store.applyCredentialAuthInvalidations(t.Context(), conn, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.applyCredentialAuthInvalidations(t.Context(), conn, state); err != nil {
		t.Fatal(err)
	}

	closed, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := importLegacyAccountRouterState(t.Context(), closed, input(validRouter)); err == nil {
		t.Fatal("legacy state import accepted closed connection")
	}
	if _, err := importLegacyAccountRouterInvalidation(
		t.Context(), closed, markerInput(validMarker),
	); err == nil {
		t.Fatal("legacy invalidation import accepted closed connection")
	}
}

//nolint:govet // Schema cases intentionally use independent error scopes.
func TestAccountRouterSchemaObjectAndCloneFailureBoundaries(t *testing.T) {
	objects := []struct {
		objectType string
		name       string
	}{
		{objectType: "index", name: "account_router_invalidations_created_idx"},
		{objectType: "index", name: "account_router_affinities_account_idx"},
		{objectType: "index", name: "account_router_sessions_updated_idx"},
		{objectType: "index", name: "account_router_accounts_health_idx"},
	}
	for _, object := range objects {
		t.Run(object.name, func(t *testing.T) {
			root := privateAccountRouterWorkspace(t)
			store, err := getStore(filepath.Join(root, object.name+".db"))
			if err != nil {
				t.Fatal(err)
			}
			db := openRawAccountRouterDB(t, store.path)
			defer db.Close()
			if _, err := db.Exec(`DROP ` + object.objectType + ` ` + object.name); err != nil {
				t.Fatal(err)
			}
			conn, err := db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := validateAccountRouterSchema(t.Context(), conn); err == nil {
				t.Fatalf("missing %s was accepted", object.name)
			}
		})
	}

	clone := cloneAccountRouterState(State{Routers: map[string]*RouterState{
		"nil": nil,
		"sparse": {
			Accounts: map[string]*AccountState{"nil": nil},
			Sessions: map[string]*SessionState{"nil": nil},
			Blocks:   map[string]*BlockRunState{"nil": nil},
		},
	}})
	if clone.Routers["nil"] != nil || clone.Routers["sparse"] == nil ||
		clone.Routers["sparse"].Accounts["nil"] != nil ||
		clone.Routers["sparse"].Sessions["nil"] != nil ||
		clone.Routers["sparse"].Blocks["nil"] != nil {
		t.Fatalf("sparse clone = %#v", clone)
	}
	var nilStore *Store
	if err := nilStore.refresh(); err == nil {
		t.Fatal("nil refresh error = nil")
	}
	if err := nilStore.update(func(*State) {}); err != nil {
		t.Fatalf("nil update = %v", err)
	}
	failed := &Store{initErr: errors.New("injected init failure")}
	if err := failed.update(func(*State) {}); err == nil {
		t.Fatal("failed store update error = nil")
	}

	corruptRoot := privateAccountRouterWorkspace(t)
	corruptStore, err := getStore(filepath.Join(corruptRoot, "corrupt.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corruptStore.path, []byte("not SQLite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SessionKeys(corruptStore.path, "router"); err == nil {
		t.Fatal("SessionKeys accepted corrupt cached database")
	}
}

func TestAccountRouterLockSymlinkAndPlatformBoundaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix lock path assertions")
	}
	root := privateAccountRouterWorkspace(t)
	database := filepath.Join(root, "router.db")
	lockDirectory := database + ".locks"
	if err := os.MkdirAll(lockDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "target.lock")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(lockDirectory, "store.lock")
	if err := os.Symlink(target, lockPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := lockAccountRouterDatabase(database); err == nil ||
		!strings.Contains(err.Error(), "regular file") {
		t.Fatalf("lock symlink error = %v", err)
	}
}
