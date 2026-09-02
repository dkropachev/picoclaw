package eventing

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/database"
)

// RunOfflineDatabaseMigration initializes, upgrades, and validates a trusted
// event store without consulting runtime broker state.
func RunOfflineDatabaseMigration(ctx context.Context, path string) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(
			database.CodeConflict,
			"event migration requires the exclusive database fence",
		)
	}
	store, err := openLocal(ctx, path)
	if err != nil {
		return err
	}
	return store.Close()
}
