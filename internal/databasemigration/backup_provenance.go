package databasemigration

import "errors"

type backupStoreProvenance struct {
	storeID     string
	path        string
	legacyRoots []string
}

func backupManifestStoreForProvenance(
	manifest BackupManifest,
	provenance backupStoreProvenance,
) (BackupStoreManifest, error) {
	if provenance.storeID == "" || !validBackupAbsolutePath(provenance.path) {
		return BackupStoreManifest{}, errors.New("database backup store provenance is invalid")
	}
	var result BackupStoreManifest
	for _, store := range manifest.Stores {
		if store.StoreID == provenance.storeID {
			result = store
			break
		}
	}
	if result.StoreID == "" {
		return BackupStoreManifest{}, errors.New("database backup store manifest is missing")
	}
	if result.StoreID != provenance.storeID || result.Path != provenance.path ||
		len(result.LegacyRoots) != len(provenance.legacyRoots) {
		return BackupStoreManifest{}, errors.New("database backup store provenance changed")
	}
	for index := range result.LegacyRoots {
		if result.LegacyRoots[index] != provenance.legacyRoots[index] {
			return BackupStoreManifest{}, errors.New("database backup legacy-root provenance changed")
		}
	}
	return result, nil
}
