package accountrouter

import "github.com/sipeed/picoclaw/pkg/database"

// RunOfflineDatabaseMigration initializes, upgrades, and validates the
// workspace account-routing store under exclusive migration fencing.
func RunOfflineDatabaseMigration(workspace string) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(
			database.CodeConflict,
			"account-routing migration requires the exclusive database fence",
		)
	}
	store, err := getStore(databasePath(workspace))
	if err != nil {
		return err
	}
	if err := store.retain(); err != nil {
		return err
	}
	return store.closeRetained()
}
