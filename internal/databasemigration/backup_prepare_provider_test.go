package databasemigration

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/internal/databaseproviderlease"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
)

func TestPreparedGenerationMintsStoreBoundProviderSource(t *testing.T) {
	home := migrationHome(t)
	spec := storecatalog.Spec{ID: "global/auth", Path: filepath.Join(home, "auth.db")}
	createPreparedProviderFixture(t, spec.Path)
	session, err := snapshotBackup(
		t.Context(), time.Now, home,
		[]storecatalog.Spec{spec}, []storecatalog.Spec{spec}, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	source, cleanup, err := session.prepareImmutableGenerationSource(t.Context(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cleanupErr := cleanup(); cleanupErr != nil {
			t.Errorf("clean prepared provider source: %v", cleanupErr)
		}
	}()

	lease := newPreparedProviderLease(t, spec)
	result, err := sqliteprovider.MigrateStagedOfflineFrom(
		t.Context(), lease, source, 5*time.Second, 2,
		func(ctx context.Context, stage string) (returnErr error) {
			database, openErr := sqliteprovider.OpenStore(stage, time.Second)
			if openErr != nil {
				return openErr
			}
			defer func() { returnErr = errors.Join(returnErr, database.Close()) }()
			if configureErr := sqliteprovider.ConfigureOffline(ctx, database, time.Second); configureErr != nil {
				return configureErr
			}
			if _, execErr := database.ExecContext(
				ctx, "CREATE TABLE migrated_marker(value TEXT NOT NULL) STRICT",
			); execErr != nil {
				return execErr
			}
			return sqliteprovider.SetSchemaVersion(ctx, database, 2)
		},
		func(ctx context.Context, stage string) error {
			inspection, inspectErr := sqliteprovider.Inspect(ctx, stage, time.Second)
			if inspectErr != nil {
				return inspectErr
			}
			ready, contractErr := inspection.HasSchemaObjects(ctx, "table", "migrated_marker")
			releaseErr := inspection.Release()
			if contractErr != nil || releaseErr != nil {
				return errors.Join(contractErr, releaseErr)
			}
			if !ready {
				return errors.New("prepared provider migration contract is incomplete")
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.BeforeVersion != 1 || result.AfterVersion != 2 {
		t.Fatalf("prepared provider result = %#v", result)
	}
	inspection, err := sqliteprovider.Inspect(t.Context(), spec.Path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ready, contractErr := inspection.HasSchemaObjects(
		t.Context(), "table", "original_marker", "migrated_marker",
	)
	releaseErr := inspection.Release()
	if contractErr != nil || releaseErr != nil || !ready || inspection.Version != 2 {
		t.Fatalf(
			"installed prepared source ready=%t version=%d contract=%v release=%v",
			ready, inspection.Version, contractErr, releaseErr,
		)
	}
}

func newPreparedProviderLease(t *testing.T, spec storecatalog.Spec) *databaseproviderlease.Lease {
	t.Helper()
	parent, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	lease, err := databaseproviderlease.New(
		parent,
		spec.ID,
		spec.Path,
		databaseproviderlease.Hooks{
			Check:                func(context.Context) error { return nil },
			Reconcile:            func(context.Context) error { return nil },
			PinReplacement:       func(context.Context, string) error { return nil },
			DiscardReplacement:   func(context.Context) error { return nil },
			ReconcileReplacement: func(context.Context) error { return nil },
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func createPreparedProviderFixture(t *testing.T, path string) {
	t.Helper()
	database, err := sqliteprovider.OpenStore(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := sqliteprovider.Configure(t.Context(), database, time.Second, false); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if _, err := database.ExecContext(
		t.Context(), "CREATE TABLE original_marker(value TEXT NOT NULL) STRICT",
	); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := sqliteprovider.SetSchemaVersion(t.Context(), database, 1); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}
