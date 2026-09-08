package gitworkspace

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/sqlitestore"
)

func TestInventoryLegacyParserRejectsDefensiveBoundaryShapes(t *testing.T) {
	manager := &Manager{}
	if state, err := manager.decodeLegacyInventory([]byte(`1`)); err == nil || state != nil {
		t.Fatalf("scalar legacy inventory = %#v, %v", state, err)
	}

	deeplyNested := strings.Repeat("[", 130) + "0" + strings.Repeat("]", 130)
	if err := rejectDuplicateLegacyInventoryIdentities([]byte(deeplyNested)); err == nil ||
		!strings.Contains(err.Error(), "nesting") {
		t.Fatalf("deeply nested legacy inventory error = %v", err)
	}

	for name, payload := range map[string]string{
		"object name":         `{"valid":1,`,
		"object terminator":   `{"valid":1`,
		"array terminator":    `[1`,
		"nested object value": `{"valid":[`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := rejectDuplicateLegacyInventoryIdentities([]byte(payload)); err == nil {
				t.Fatalf("malformed parser boundary %q succeeded", payload)
			}
		})
	}

	decoder := json.NewDecoder(strings.NewReader(`{}`))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		t.Fatalf("consume opening delimiter = %v, %v", token, err)
	}
	if err := consumeUniqueLegacyInventoryJSONValue(decoder, 0); err == nil ||
		!strings.Contains(err.Error(), "delimiter") {
		t.Fatalf("standalone closing delimiter error = %v", err)
	}
}

func TestInventoryImportHorizonFailuresAndGenerationAuthority(t *testing.T) {
	manager := &Manager{}

	t.Run("closed connection", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		conn, err := database.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		if err := sealInventoryImport(t.Context(), conn, false); !errors.Is(err, sql.ErrConnDone) {
			t.Fatalf("seal on closed connection = %v", err)
		}
		if _, err := manager.importLegacyInventory(
			t.Context(), conn, sqlitestore.LegacyInput{},
		); !errors.Is(err, sql.ErrConnDone) {
			t.Fatalf("import on closed connection = %v", err)
		}
	})

	t.Run("invalid horizon cardinality", func(t *testing.T) {
		database, conn := openInventoryCoverageConnection(t,
			`CREATE TABLE inventory_legacy_import_state(singleton INTEGER, import_closed INTEGER)`,
			`INSERT INTO inventory_legacy_import_state VALUES (1, 1), (1, 1)`,
		)
		defer database.Close()
		defer conn.Close()
		if result, err := manager.importLegacyInventory(
			t.Context(), conn, sqlitestore.LegacyInput{},
		); err == nil || result.Imported != 0 || result.Skipped != 0 || len(result.Issues) != 0 ||
			!strings.Contains(err.Error(), "horizon is invalid") {
			t.Fatalf("invalid horizon import = %#v, %v", result, err)
		}
	})

	t.Run("nonzero generation", func(t *testing.T) {
		database, conn := openInventoryCoverageConnection(t,
			`CREATE TABLE inventory_legacy_import_state(singleton INTEGER, import_closed INTEGER)`,
			`CREATE TABLE inventory_meta(singleton INTEGER, generation INTEGER)`,
			`INSERT INTO inventory_meta VALUES (1, 7)`,
		)
		defer database.Close()
		defer conn.Close()
		input := sqlitestore.LegacyInput{Digest: [32]byte{1}}
		result, err := manager.importLegacyInventory(t.Context(), conn, input)
		if err != nil || result.Imported != 0 || result.Skipped != 1 ||
			len(result.Issues) != 1 || result.Issues[0].Code != "sqlite-authoritative" ||
			result.Issues[0].RecordDigest != input.Digest {
			t.Fatalf("nonzero generation import = %#v, %v", result, err)
		}
	})
}

func TestInventoryLoadersPropagateDeferredRowsErrors(t *testing.T) {
	manager, err := NewManager(Options{RootDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	database, err := manager.openInventoryDatabase(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	faultDatabase := openInventoryRowsErrorDatabase(t)
	defer faultDatabase.Close()

	checks := []struct {
		name   string
		failAt int
		load   func(inventoryQueryer) error
	}{
		{
			name: "repositories", failAt: 1,
			load: func(queryer inventoryQueryer) error {
				return loadInventoryRepositories(t.Context(), queryer, &storeState{
					Repositories: map[string]*RepositoryRecord{},
				})
			},
		},
		{
			name: "workspaces", failAt: 1,
			load: func(queryer inventoryQueryer) error {
				return loadInventoryWorkspaces(t.Context(), queryer, &storeState{
					Workspaces: map[string]*WorkspaceRecord{},
				})
			},
		},
		{
			name: "development lines", failAt: 1,
			load: func(queryer inventoryQueryer) error {
				return loadInventoryDevelopmentLines(t.Context(), queryer, &storeState{
					DevelopmentLines: map[string]*developmentLineRecord{},
				})
			},
		},
		{
			name: "retired reservations", failAt: 2,
			load: func(queryer inventoryQueryer) error {
				return loadInventoryDevelopmentLines(t.Context(), queryer, &storeState{
					DevelopmentLines: map[string]*developmentLineRecord{},
				})
			},
		},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			queryer := &inventoryCanceledRowsQueryer{
				database: database, faultDatabase: faultDatabase, failAt: check.failAt,
			}
			if err := check.load(queryer); !errors.Is(err, context.Canceled) {
				t.Fatalf("deferred rows error = %v, want context cancellation", err)
			}
		})
	}
}

func TestInventoryChildInsertFailuresAreReturned(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*storeState)
		insert func(context.Context, *sql.Conn, *storeState) error
	}{
		{
			name: "workspace lock",
			mutate: func(state *storeState) {
				state.Workspaces["workspace"].LockedBy = &LockInfo{
					LockedAt: time.Unix(1, 0).UTC(), HeartbeatAt: time.Unix(2, 0).UTC(),
				}
			},
			insert: insertInventoryWorkspaces,
		},
		{
			name: "retired reservation",
			mutate: func(state *storeState) {
				state.DevelopmentLines["line"].RetiredReservationHashes = []string{"short"}
			},
			insert: insertInventoryDevelopmentLines,
		},
		{
			name: "suspension",
			mutate: func(state *storeState) {
				state.DevelopmentLines["line"].Suspensions = []developmentLineSuspensionRecord{{}}
			},
			insert: insertInventoryDevelopmentLines,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, err := NewManager(Options{RootDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			database, err := manager.openInventoryDatabase(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			state := inventoryChildInsertState()
			test.mutate(state)
			err = sqlitestore.Immediate(t.Context(), database, func(conn *sql.Conn) error {
				if insertErr := insertInventoryRepositories(t.Context(), conn, state); insertErr != nil {
					return insertErr
				}
				if test.name != "workspace lock" {
					if insertErr := insertInventoryWorkspaces(t.Context(), conn, state); insertErr != nil {
						return insertErr
					}
				}
				if insertErr := test.insert(t.Context(), conn, state); insertErr == nil {
					return errors.New("invalid child row was inserted")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func openInventoryCoverageConnection(t *testing.T, statements ...string) (*sql.DB, *sql.Conn) {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, execErr := database.Exec(statement); execErr != nil {
			database.Close()
			t.Fatal(execErr)
		}
	}
	conn, err := database.Conn(t.Context())
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	return database, conn
}

type inventoryCanceledRowsQueryer struct {
	database      *sql.DB
	faultDatabase *sql.DB
	failAt        int
	calls         int
}

func (queryer *inventoryCanceledRowsQueryer) QueryRowContext(
	ctx context.Context,
	query string,
	arguments ...any,
) *sql.Row {
	return queryer.database.QueryRowContext(ctx, query, arguments...)
}

func (queryer *inventoryCanceledRowsQueryer) QueryContext(
	ctx context.Context,
	query string,
	arguments ...any,
) (*sql.Rows, error) {
	queryer.calls++
	if queryer.calls != queryer.failAt {
		return queryer.database.QueryContext(ctx, query, arguments...)
	}
	return queryer.faultDatabase.QueryContext(ctx, "injected rows error")
}

var registerInventoryRowsErrorDriver sync.Once

func openInventoryRowsErrorDatabase(t *testing.T) *sql.DB {
	t.Helper()
	registerInventoryRowsErrorDriver.Do(func() {
		sql.Register("gitworkspace-inventory-rows-error", inventoryRowsErrorDriver{})
	})
	database, err := sql.Open("gitworkspace-inventory-rows-error", "")
	if err != nil {
		t.Fatal(err)
	}
	return database
}

type inventoryRowsErrorDriver struct{}

func (inventoryRowsErrorDriver) Open(string) (driver.Conn, error) {
	return inventoryRowsErrorConnection{}, nil
}

type inventoryRowsErrorConnection struct{}

func (inventoryRowsErrorConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("inventory rows-error driver does not prepare statements")
}

func (inventoryRowsErrorConnection) Close() error { return nil }

func (inventoryRowsErrorConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("inventory rows-error driver does not begin transactions")
}

func (inventoryRowsErrorConnection) QueryContext(
	context.Context,
	string,
	[]driver.NamedValue,
) (driver.Rows, error) {
	return inventoryRowsErrorResult{}, nil
}

type inventoryRowsErrorResult struct{}

func (inventoryRowsErrorResult) Columns() []string { return []string{"value"} }
func (inventoryRowsErrorResult) Close() error      { return nil }
func (inventoryRowsErrorResult) Next([]driver.Value) error {
	return errors.Join(context.Canceled, io.ErrUnexpectedEOF)
}

func inventoryChildInsertState() *storeState {
	now := time.Unix(1, 0).UTC()
	return &storeState{
		Repositories: map[string]*RepositoryRecord{
			"repository": {ID: "repository", RemoteURL: "https://example.invalid/repository.git"},
		},
		Workspaces: map[string]*WorkspaceRecord{
			"workspace": {
				ID: "workspace", RepoID: "repository",
				RemoteURL: "https://example.invalid/repository.git", Path: "/private/workspace",
			},
		},
		DevelopmentLines: map[string]*developmentLineRecord{
			"line": {
				ID: "line", WorkspaceID: "workspace", RepoID: "repository", SourceRef: "main",
				SourceCommit: "commit", Branch: "branch", Tip: "commit", Tree: "tree",
				MutationEpoch: 1, State: "parked", CreatedAt: now, UpdatedAt: now,
			},
		},
		PinnedReservationRotations: map[string][]pinnedReservationRotationRecord{},
	}
}
