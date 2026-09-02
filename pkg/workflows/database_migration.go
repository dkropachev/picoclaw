package workflows

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/database"
)

// RunOfflineDatabaseMigration executes the workflow domain's schema and legacy
// adapter for the trusted workspace selected by the exclusively fenced
// database migrator. It returns only after its physical pool is closed.
func RunOfflineDatabaseMigration(ctx context.Context, workspace string) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(
			database.CodeConflict,
			"workflow migration requires the exclusive database fence",
		)
	}
	db, err := openWorkflowDatabase(ctx, workspace)
	if err != nil {
		return err
	}
	return db.Close()
}
