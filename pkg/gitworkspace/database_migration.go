package gitworkspace

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/database"
)

// RunOfflineDatabaseMigration initializes, upgrades, imports, and validates a
// trusted git-workspace inventory while the caller owns exclusive fencing.
func RunOfflineDatabaseMigration(ctx context.Context, root string) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(
			database.CodeConflict,
			"git workspace inventory migration requires the exclusive database fence",
		)
	}
	manager, err := prepareManager(Options{RootDir: root})
	if err != nil {
		return err
	}
	return manager.initializeLocalInventory(ctx)
}
