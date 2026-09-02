package localci

import "github.com/sipeed/picoclaw/pkg/database"

// RunOfflineDatabaseMigration initializes, upgrades, and validates the local
// CI passing cache under exclusive migration fencing.
func RunOfflineDatabaseMigration(evidenceRoot string) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(
			database.CodeConflict,
			"local-CI migration requires the exclusive database fence",
		)
	}
	store, err := openFileEvidenceStoreLocal(evidenceRoot)
	if err != nil {
		return err
	}
	return store.Close()
}
