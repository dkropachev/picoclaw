//go:build whatsapp_native

package sqliteadapter

import (
	"context"

	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	MigrationAfterUpgrade = "after-upgrade"
	MigrationAfterVersion = "after-version"
)

// MigrateDatabase upgrades the WhatsApp library schema while the caller holds
// the exclusive offline migration fence.
func MigrateDatabase(ctx context.Context, path string) error {
	return MigrateDatabaseWithCheckpoint(ctx, path, nil)
}

// MigrateDatabaseWithCheckpoint is exported for crash-generation conformance
// tests. Production callers use MigrateDatabase.
func MigrateDatabaseWithCheckpoint(
	ctx context.Context,
	path string,
	checkpoint func(string) error,
) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(database.CodeConflict, "WhatsApp migration requires the exclusive database fence")
	}
	if checkpoint == nil {
		checkpoint = func(string) error { return nil }
	}
	return sqliteprovider.MigrateStagedOffline(
		ctx,
		path,
		busyTimeout,
		1,
		func(ctx context.Context, stagedPath string) error {
			return migrateDatabaseStage(ctx, stagedPath, checkpoint)
		},
	)
}

func migrateDatabaseStage(
	ctx context.Context,
	path string,
	checkpoint func(string) error,
) error {
	db, err := sqliteprovider.OpenStore(path, busyTimeout)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := sqliteprovider.ConfigureOffline(ctx, db, busyTimeout); err != nil {
		_ = db.Close()
		return err
	}
	container := sqlstore.NewWithDB(db, sqliteprovider.DriverName(), waLog.Noop)
	defer container.Close()
	if err := container.Upgrade(ctx); err != nil {
		return err
	}
	if err := checkpoint(MigrationAfterUpgrade); err != nil {
		return err
	}
	if err := sqliteprovider.SetSchemaVersion(ctx, db, 1); err != nil {
		return err
	}
	return checkpoint(MigrationAfterVersion)
}
