package seahorse

import (
	"errors"

	"github.com/sipeed/picoclaw/pkg/database"
)

// RunOfflineDatabaseMigration installs or upgrades the Seahorse schema while
// the caller holds the exclusive database migration fence.
func RunOfflineDatabaseMigration(path string) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(
			database.CodeConflict,
			"Seahorse migration requires the exclusive database fence",
		)
	}
	engine, err := NewOfflineEngine(OfflineConfig{DatabasePath: path}, nil)
	if err != nil {
		return err
	}
	var validationErr error
	if engine.store == nil || engine.store.db == nil {
		validationErr = errors.New("seahorse migration store is unavailable")
	} else {
		validationErr = validateCurrentSchema(engine.store.db)
	}
	return errors.Join(validationErr, engine.Close())
}
