package memory

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/database"
)

// RunOfflineDatabaseMigration initializes, upgrades, and validates a trusted
// sessions store without consulting runtime broker state.
func RunOfflineDatabaseMigration(ctx context.Context, directory string) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(
			database.CodeConflict,
			"session migration requires the exclusive database fence",
		)
	}
	store, err := openLocalSQLiteStore(ctx, directory)
	if err != nil {
		return err
	}
	return store.Close()
}
