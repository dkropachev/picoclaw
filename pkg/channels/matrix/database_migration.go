package matrix

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/crypto"
	"maunium.net/go/mautrix/id"
	"maunium.net/go/mautrix/sqlstatestore"

	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/pkg/database"
)

const (
	matrixMigrationAfterState   = "after-state"
	matrixMigrationAfterCrypto  = "after-crypto"
	matrixMigrationAfterVersion = "after-version"
)

var matrixMigrationCheckpoint = func(string) error { return nil }

// MigrateCryptoDatabase upgrades Matrix state and crypto schemas while the
// caller holds the exclusive offline migration fence.
func MigrateCryptoDatabase(ctx context.Context, path string) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(database.CodeConflict, "Matrix migration requires the exclusive database fence")
	}
	return sqliteprovider.MigrateStagedOffline(
		ctx,
		path,
		5*time.Second,
		1,
		migrateCryptoDatabaseStage,
	)
}

func migrateCryptoDatabaseStage(ctx context.Context, path string) (returnErr error) {
	db, openErr := sqliteprovider.OpenStore(path, 5*time.Second)
	if openErr != nil {
		return openErr
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := sqliteprovider.ConfigureOffline(ctx, db, 5*time.Second); err != nil {
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
	if err := matrixMigrationCheckpoint(matrixMigrationAfterState); err != nil {
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
	if err := matrixMigrationCheckpoint(matrixMigrationAfterCrypto); err != nil {
		return err
	}
	if err := sqliteprovider.SetSchemaVersion(ctx, db, 1); err != nil {
		return err
	}
	return matrixMigrationCheckpoint(matrixMigrationAfterVersion)
}
