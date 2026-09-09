//nolint:govet // Independent failure-boundary assertions use narrow error scopes.
package databasereadiness

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/databaseadapter"
	"github.com/sipeed/picoclaw/internal/databaseclaims"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/database"
)

func TestProbeClassifiesGenerationStates(t *testing.T) {
	tests := []struct {
		name       string
		prepare    func(*testing.T, string)
		contract   databaseadapter.Contract
		readiness  database.StoreReadiness
		code       database.ErrorCode
		mustRemain bool
	}{
		{
			name: "missing", contract: readinessContract(2, databaseadapter.EmptyInitializeOnline),
			readiness: database.StoreReady,
		},
		{
			name: "empty", prepare: func(t *testing.T, home string) {
				writeReadinessFile(t, filepath.Join(home, "auth.db"), nil)
			}, contract: readinessContract(2, databaseadapter.EmptyInitializeOnline),
			readiness: database.StoreReady, mustRemain: true,
		},
		{
			name: "empty-offline", prepare: func(t *testing.T, home string) {
				writeReadinessFile(t, filepath.Join(home, "auth.db"), nil)
			}, contract: readinessContract(2, databaseadapter.EmptyMigrateOffline),
			readiness: database.StoreMigrationRequired, code: database.CodeMigrationRequired,
		},
		{
			name: "legacy", prepare: func(t *testing.T, home string) {
				writeReadinessFile(t, filepath.Join(home, "auth.json"), []byte("legacy"))
			}, contract: readinessContract(2, databaseadapter.EmptyInitializeOnline),
			readiness: database.StoreMigrationRequired, code: database.CodeMigrationRequired,
		},
		{
			name: "old", prepare: func(t *testing.T, home string) {
				createReadinessDatabase(t, filepath.Join(home, "auth.db"), 1, true, true)
			}, contract: readinessContract(2, databaseadapter.EmptyInitializeOnline),
			readiness: database.StoreMigrationRequired, code: database.CodeMigrationRequired,
		},
		{
			name: "current", prepare: func(t *testing.T, home string) {
				createReadinessDatabase(t, filepath.Join(home, "auth.db"), 2, true, true)
			}, contract: readinessContract(2, databaseadapter.EmptyInitializeOnline),
			readiness: database.StoreReady,
		},
		{
			name: "too-new", prepare: func(t *testing.T, home string) {
				createReadinessDatabase(t, filepath.Join(home, "auth.db"), 3, true, true)
			}, contract: readinessContract(2, databaseadapter.EmptyInitializeOnline),
			readiness: database.StoreUnavailable, code: database.CodeUnsupported,
		},
		{
			name: "corrupt", prepare: func(t *testing.T, home string) {
				writeReadinessFile(t, filepath.Join(home, "auth.db"), []byte("not sqlite"))
			}, contract: readinessContract(2, databaseadapter.EmptyInitializeOnline),
			readiness: database.StoreIntegrityFailed, code: database.CodeIntegrity,
		},
		{
			name: "schema", prepare: func(t *testing.T, home string) {
				createReadinessDatabase(t, filepath.Join(home, "auth.db"), 2, false, true)
			}, contract: readinessContract(2, databaseadapter.EmptyInitializeOnline),
			readiness: database.StoreMigrationRequired, code: database.CodeMigrationRequired,
		},
		{
			name: "horizon", prepare: func(t *testing.T, home string) {
				createReadinessDatabase(t, filepath.Join(home, "auth.db"), 2, true, false)
			}, contract: readinessContract(2, databaseadapter.EmptyInitializeOnline),
			readiness: database.StoreMigrationRequired, code: database.CodeMigrationRequired,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			if test.prepare != nil {
				test.prepare(t, home)
			}
			lease, fence := acquireReadinessLease(t, home)
			registry := readinessRegistry(t, lease.Stores(), "auth", test.contract)
			snapshot, err := Probe(context.Background(), lease, registry)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = snapshot.Close() })
			status := readinessStatus(t, snapshot.Statuses(), "global/auth")
			if status.Readiness != test.readiness {
				t.Fatalf("readiness = %s, want %s (%v)", status.Readiness, test.readiness, status.Error)
			}
			if test.code == "" {
				if status.Error != nil {
					t.Fatalf("ready status error = %v", status.Error)
				}
			} else if status.Error == nil || status.Error.Code != test.code {
				t.Fatalf("status error = %v, want code %s", status.Error, test.code)
			}
			if test.name == "missing" {
				if _, statErr := os.Lstat(filepath.Join(home, "auth.db")); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("Probe initialized missing store: %v", statErr)
				}
			}
			_ = fence
		})
	}
}

func TestProbeRequiresCompleteRegistryBeforeProviderInspection(t *testing.T) {
	home := t.TempDir()
	writeReadinessFile(t, filepath.Join(home, "auth.db"), []byte("corrupt"))
	lease, _ := acquireReadinessLease(t, home)
	registry, err := databaseadapter.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := Probe(context.Background(), lease, registry)
	if snapshot != nil || database.CodeOf(err) != database.CodeUnsupported {
		t.Fatalf("Probe() = %#v, %v", snapshot, err)
	}
}

func TestProbeReturnsContextCancellation(t *testing.T) {
	home := t.TempDir()
	lease, _ := acquireReadinessLease(t, home)
	registry := readinessRegistry(
		t, lease.Stores(), "", databaseadapter.Contract{},
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot, err := Probe(ctx, lease, registry)
	if snapshot != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Probe() = %#v, %v", snapshot, err)
	}
}

func TestProbePoolCanBeAdoptedAndSurvivesSnapshotClose(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "auth.db")
	createReadinessDatabase(t, path, 2, true, true)
	lease, _ := acquireReadinessLease(t, home)
	registry := readinessRegistry(
		t, lease.Stores(), "auth", readinessContract(2, databaseadapter.EmptyInitializeOnline),
	)
	snapshot, err := Probe(context.Background(), lease, registry)
	if err != nil {
		t.Fatal(err)
	}
	db, err := snapshot.Adopt("global/auth")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		t.Fatalf("adopted pool after Snapshot.Close: version=%d error=%v", version, err)
	}
}

func TestProbePoolCannotBeAdoptedAfterClaimAuthorityExpires(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "auth.db")
	createReadinessDatabase(t, path, 2, true, true)
	lease, fence := acquireReadinessLease(t, home)
	registry := readinessRegistry(
		t, lease.Stores(), "auth", readinessContract(2, databaseadapter.EmptyInitializeOnline),
	)
	snapshot, err := Probe(t.Context(), lease, registry)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if err := fence.Close(); err != nil {
		t.Fatal(err)
	}
	if statuses := snapshot.Statuses(); statuses != nil {
		t.Fatalf("expired snapshot statuses = %#v", statuses)
	}
	if database, err := snapshot.Adopt("global/auth"); err == nil || database != nil {
		if database != nil {
			_ = database.Close()
		}
		t.Fatalf("expired readiness pool adoption = %#v, %v", database, err)
	}
}

func TestSnapshotAdoptAndCloseAreOneShotAndRaceSafe(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "auth.db")
	createReadinessDatabase(t, path, 2, true, true)
	lease, _ := acquireReadinessLease(t, home)
	registry := readinessRegistry(
		t, lease.Stores(), "auth", readinessContract(2, databaseadapter.EmptyInitializeOnline),
	)
	snapshot, err := Probe(t.Context(), lease, registry)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type adoption struct {
		database *sql.DB
		err      error
	}
	adopted := make(chan adoption, 1)
	closed := make(chan error, 1)
	go func() {
		<-start
		db, adoptErr := snapshot.Adopt("global/auth")
		adopted <- adoption{database: db, err: adoptErr}
	}()
	go func() {
		<-start
		closed <- snapshot.Close()
	}()
	close(start)
	result := <-adopted
	if closeErr := <-closed; closeErr != nil {
		t.Fatal(closeErr)
	}
	if result.database != nil {
		if result.err != nil {
			t.Fatalf("adoption returned database and error: %v", result.err)
		}
		if err := result.database.Close(); err != nil {
			t.Fatal(err)
		}
	} else if result.err == nil {
		t.Fatal("racing adoption returned neither database nor error")
	}
	if database, err := snapshot.Adopt("global/auth"); err == nil || database != nil {
		if database != nil {
			_ = database.Close()
		}
		t.Fatalf("repeat adoption = %#v, %v", database, err)
	}
}

func TestProbeClassifiesRealSQLiteLockContentionAsUnavailable(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "auth.db")
	createReadinessDatabase(t, path, 2, true, true)
	lease, _ := acquireReadinessLease(t, home)
	registry := readinessRegistry(
		t, lease.Stores(), "auth", readinessContract(2, databaseadapter.EmptyInitializeOnline),
	)

	locker, err := sqliteprovider.OpenStore(path, 25*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	locker.SetMaxOpenConns(1)
	if err := sqliteprovider.ConfigureOffline(t.Context(), locker, 25*time.Millisecond); err != nil {
		_ = locker.Close()
		t.Fatal(err)
	}
	connection, err := locker.Conn(t.Context())
	if err != nil {
		_ = locker.Close()
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(t.Context(), "BEGIN EXCLUSIVE"); err != nil {
		_ = connection.Close()
		_ = locker.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		_ = connection.Close()
		_ = locker.Close()
	})

	snapshot, err := Probe(t.Context(), lease, registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })
	status := readinessStatus(t, snapshot.Statuses(), "global/auth")
	if status.Readiness != database.StoreUnavailable || status.Error == nil ||
		status.Error.Code != database.CodeUnavailable {
		t.Fatalf("locked readiness = %#v", status)
	}
}

func readinessContract(version int, policy databaseadapter.EmptyPolicy) databaseadapter.Contract {
	return databaseadapter.Contract{
		CurrentVersion: version,
		EmptyPolicy:    policy,
		RequiredObjects: []databaseadapter.SchemaObject{
			{Type: "table", Name: "items"},
			{Type: "table", Name: "storage_import_horizons"},
		},
		RequiredColumns: []databaseadapter.ColumnSet{{Table: "items", Columns: []string{"id"}}},
		ImportHorizon:   "auth",
	}
}

func readinessRegistry(
	t *testing.T,
	specs []storecatalog.Spec,
	overrideDomain string,
	override databaseadapter.Contract,
) *databaseadapter.Registry {
	t.Helper()
	seen := make(map[string]struct{})
	var adapters []databaseadapter.Adapter
	for _, spec := range specs {
		if _, ok := seen[spec.Domain]; ok {
			continue
		}
		seen[spec.Domain] = struct{}{}
		contract := databaseadapter.Contract{
			CurrentVersion: 1, EmptyPolicy: databaseadapter.EmptyInitializeOnline,
		}
		if spec.Domain == overrideDomain {
			contract = override
		}
		adapters = append(adapters, databaseadapter.Adapter{Domain: spec.Domain, Contract: contract})
	}
	registry, err := databaseadapter.NewRegistry(adapters...)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func acquireReadinessLease(t *testing.T, home string) (*databaseclaims.Lease, *database.Fence) {
	t.Helper()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	fence, err := database.AcquireOnlineFence(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fence.Close() })
	claimRoot, err := databaseclaims.PrepareRootForTesting(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := databaseclaims.AcquireForTesting(
		storecatalog.Options{Home: home, Config: &config.Config{}}, nil, fence, claimRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	return lease, fence
}

func readinessStatus(
	t *testing.T,
	statuses []database.StoreStatus,
	id database.StoreID,
) database.StoreStatus {
	t.Helper()
	for _, status := range statuses {
		if status.ID == id {
			return status
		}
	}
	t.Fatalf("status %s absent", id)
	return database.StoreStatus{}
}

func createReadinessDatabase(
	t *testing.T,
	path string,
	version int,
	objects bool,
	horizon bool,
) {
	t.Helper()
	db, err := sqliteprovider.OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := sqliteprovider.Configure(context.Background(), db, time.Second, false); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if objects {
		if _, err := db.Exec("CREATE TABLE items (id INTEGER PRIMARY KEY)"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE storage_import_horizons (
		component TEXT PRIMARY KEY,
		completed_at INTEGER NOT NULL
	) STRICT`); err != nil {
		t.Fatal(err)
	}
	if horizon {
		if _, err := db.Exec(
			"INSERT INTO storage_import_horizons(component, completed_at) VALUES ('auth', 1)",
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := sqliteprovider.SetSchemaVersion(context.Background(), db, version); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeReadinessFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
