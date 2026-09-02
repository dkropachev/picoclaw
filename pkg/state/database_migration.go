package state

import "github.com/sipeed/picoclaw/pkg/database"

// RunOfflineDatabaseMigration initializes, upgrades, and validates a trusted
// runtime-state store without consulting runtime broker state.
func RunOfflineDatabaseMigration(workspace string) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(
			database.CodeConflict,
			"runtime-state migration requires the exclusive database fence",
		)
	}
	manager, err := newSQLiteManagerLocal(workspace)
	if err != nil {
		return err
	}
	return manager.Close()
}
