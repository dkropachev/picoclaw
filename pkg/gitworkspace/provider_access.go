package gitworkspace

import "github.com/sipeed/picoclaw/pkg/database"

func inventoryProviderAuthorityHeld() bool {
	return database.BrokerAuthorityHeld() || database.MigrationFenceHeld() ||
		database.ProviderTestAuthorityHeld()
}
