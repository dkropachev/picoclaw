package evolution

import (
	"context"
	"path/filepath"

	"github.com/sipeed/picoclaw/pkg/database"
)

// RunOfflineDatabaseMigration initializes, upgrades, and validates one trusted
// evolution store while the caller owns exclusive migration fencing.
func RunOfflineDatabaseMigration(ctx context.Context, path string) error {
	if !database.MigrationFenceHeld() {
		return database.NewError(
			database.CodeConflict,
			"evolution migration requires the exclusive database fence",
		)
	}
	store := &Store{paths: normalizedEvolutionPaths(Paths{
		RootDir: filepath.Dir(path), Database: path,
	})}
	db, err := store.open(ctx)
	if err != nil {
		return err
	}
	return db.Close()
}
