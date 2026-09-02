package repoeval

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/database"
)

// RunOfflineDatabaseMigration initializes, upgrades, and validates the
// repository-evaluation store under exclusive migration fencing.
func RunOfflineDatabaseMigration(ctx context.Context, workspace string) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(
			database.CodeConflict,
			"repository-evaluation migration requires the exclusive database fence",
		)
	}
	store := newSQLiteStoreLocal(workspace)
	if err := store.Preflight(ctx); err != nil {
		return err
	}
	return store.Close()
}
