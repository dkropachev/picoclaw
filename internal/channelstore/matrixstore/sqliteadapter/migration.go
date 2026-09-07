package sqliteadapter

import (
	"context"
	"errors"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/sqlstatestore"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	MigrationAfterState   = "after-state"
	MigrationAfterCrypto  = "after-crypto"
	MigrationAfterVersion = "after-version"
)

// MigrateDatabase installs the Matrix state and crypto schemas while the
// caller holds the exclusive offline migration fence.
func MigrateDatabase(ctx context.Context, path string) error {
	return MigrateDatabaseWithCheckpoint(ctx, path, nil)
}

// MigrateDatabaseWithCheckpoint is the fault-injection form used by migration
// conformance tests. It performs the same staged, atomic migration.
func MigrateDatabaseWithCheckpoint(
	ctx context.Context,
	path string,
	checkpoint func(string) error,
) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(database.CodeConflict, "Matrix migration requires the exclusive database fence")
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
) (returnErr error) {
	db, openErr := sqliteprovider.OpenStore(path, busyTimeout)
	if openErr != nil {
		return openErr
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := sqliteprovider.ConfigureOffline(ctx, db, busyTimeout); err != nil {
		_ = db.Close()
		return err
	}
	wrapped, err := dbutil.NewWithDB(db, sqliteprovider.DriverName())
	if err != nil {
		_ = db.Close()
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, wrapped.Close()) }()
	log := dbutil.ZeroLogger(zerolog.Nop())
	stateStore := sqlstatestore.NewSQLStateStore(wrapped, log, false)
	if err := stateStore.Upgrade(ctx); err != nil {
		return err
	}
	if err := checkpoint(MigrationAfterState); err != nil {
		return err
	}
	cryptoStore := crypto.NewSQLCryptoStore(
		wrapped,
		log,
		"offline-migration",
		id.DeviceID("offline-migration"),
		[]byte("offline-migration"),
	)
	if err := cryptoStore.DB.Upgrade(ctx); err != nil {
		return err
	}
	if err := checkpoint(MigrationAfterCrypto); err != nil {
		return err
	}
	if err := sqliteprovider.SetSchemaVersion(ctx, db, 1); err != nil {
		return err
	}
	return checkpoint(MigrationAfterVersion)
}
