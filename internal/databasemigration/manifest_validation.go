package databasemigration

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/database"
)

// validateBackupManifest rejects malformed or attacker-amplified metadata
// before it can be serialized or used to select backup files.
func validateBackupManifest(manifest BackupManifest) error {
	if manifest.Version != backupManifestVersion {
		return errors.New("database backup manifest version is invalid")
	}
	if !validBackupManifestTime(manifest.CreatedAt) {
		return errors.New("database backup manifest timestamp is invalid")
	}
	if !validBackupManifestOutcome(manifest.Outcome) {
		return errors.New("database backup manifest outcome is invalid")
	}
	if len(manifest.Error) > backupMaxErrorBytes || !utf8.ValidString(manifest.Error) ||
		strings.ContainsRune(manifest.Error, 0) {
		return errors.New("database backup manifest error is invalid")
	}
	if len(manifest.Stores) > backupMaxEntries {
		return errors.New("database backup manifest store count limit exceeded")
	}
	if len(manifest.Files) > backupMaxFiles {
		return errors.New("database backup manifest file count limit exceeded")
	}
	if len(manifest.CatalogGenerations) > backupMaxEntries {
		return errors.New("database backup manifest catalog generation count limit exceeded")
	}
	if len(manifest.Stores) == 0 {
		return errors.New("database backup manifest has no stores")
	}
	if len(manifest.CatalogGenerations) == 0 {
		return errors.New("database backup manifest has no catalog generations")
	}

	stores := make(map[database.StoreID]BackupStoreManifest, len(manifest.Stores))
	for _, record := range manifest.Stores {
		id, err := database.ParseStoreID(record.StoreID)
		if err != nil || id.String() != record.StoreID {
			return errors.New("database backup manifest store ID is invalid")
		}
		if record.LegacyRoots < 0 || record.LegacyRoots > backupMaxEntries {
			return errors.New("database backup manifest legacy-root count is invalid")
		}
		if _, duplicate := stores[id]; duplicate {
			return errors.New("database backup manifest store is duplicated")
		}
		stores[id] = record
	}

	backupPaths := make(map[string]struct{}, len(manifest.Files))
	records := make(map[string]struct{}, len(manifest.Files))
	generationRoles := make(map[string]struct{}, len(manifest.Files))
	generationStates := make(map[database.StoreID]backupManifestGenerationState, len(manifest.Stores))
	var totalSize int64
	for _, record := range manifest.Files {
		id, err := database.ParseStoreID(record.StoreID)
		if err != nil || id.String() != record.StoreID {
			return errors.New("database backup manifest file store ID is invalid")
		}
		store, present := stores[id]
		if !present {
			return errors.New("database backup manifest file references an unknown store")
		}
		if !validBackupManifestRole(record.Role) {
			return errors.New("database backup manifest file role is invalid")
		}
		if record.LegacyRoot < 0 || record.LegacyRoot >= backupMaxEntries ||
			record.Role != "legacy" && record.LegacyRoot != 0 {
			return errors.New("database backup manifest legacy root is invalid")
		}
		if record.Role == "legacy" && record.LegacyRoot >= store.LegacyRoots {
			return errors.New("database backup manifest legacy root is outside its store")
		}
		if !validBackupAbsolutePath(record.Source) ||
			!validBackupManifestRelative(record.Backup) {
			return errors.New("database backup manifest file path is invalid")
		}
		if record.SourceIdentity == "" ||
			len(record.SourceIdentity) > backupMaxPathBytes ||
			!utf8.ValidString(record.SourceIdentity) ||
			strings.ContainsRune(record.SourceIdentity, 0) {
			return errors.New("database backup manifest source identity is invalid")
		}
		if !validBackupDigest(record.SHA256) {
			return errors.New("database backup manifest file hash is invalid")
		}
		if record.Size < 0 || record.Size > backupMaxFileBytes ||
			record.Size > backupMaxTotalBytes-totalSize {
			return errors.New("database backup manifest file size is invalid")
		}
		totalSize += record.Size
		if record.Mode != 0o600 {
			return errors.New("database backup manifest file mode is not private")
		}
		if record.SourceMode&^uint32(os.ModePerm) != 0 {
			return errors.New("database backup manifest source mode is invalid")
		}

		backupKey := backupPathKey(filepath.FromSlash(record.Backup))
		if _, duplicate := backupPaths[backupKey]; duplicate {
			return errors.New("database backup manifest backup path is duplicated")
		}
		backupPaths[backupKey] = struct{}{}
		recordKey := record.StoreID + "\x00" + record.Role + "\x00" + backupPathKey(record.Source)
		if _, duplicate := records[recordKey]; duplicate {
			return errors.New("database backup manifest file record is duplicated")
		}
		records[recordKey] = struct{}{}
		if record.Role != "legacy" {
			roleKey := record.StoreID + "\x00" + record.Role
			if _, duplicate := generationRoles[roleKey]; duplicate {
				return errors.New("database backup manifest generation role is duplicated")
			}
			generationRoles[roleKey] = struct{}{}
		}
		state := generationStates[id]
		switch record.Role {
		case "database":
			state.database = true
		case "wal":
			state.sidecar = true
			state.wal = true
		case "shm":
			state.sidecar = true
		case "journal":
			state.sidecar = true
			state.journal = true
		}
		generationStates[id] = state
	}
	for _, record := range manifest.Stores {
		id, _ := database.ParseStoreID(record.StoreID)
		state := generationStates[id]
		if record.Exists != state.database {
			return errors.New("database backup manifest store existence is inconsistent")
		}
		if state.sidecar && !state.database {
			return errors.New("database backup manifest has a sidecar without its database")
		}
		if state.wal && state.journal {
			return errors.New("database backup manifest mixes WAL and rollback-journal state")
		}
	}

	catalogPaths := make(map[string]struct{}, len(manifest.CatalogGenerations))
	for _, path := range manifest.CatalogGenerations {
		if !validBackupAbsolutePath(path) {
			return errors.New("database backup manifest catalog generation is invalid")
		}
		key := backupPathKey(path)
		if _, duplicate := catalogPaths[key]; duplicate {
			return errors.New("database backup manifest catalog generation is duplicated")
		}
		catalogPaths[key] = struct{}{}
	}
	return nil
}

type backupManifestGenerationState struct {
	database bool
	sidecar  bool
	wal      bool
	journal  bool
}

func validBackupManifestTime(value time.Time) bool {
	return !value.IsZero() && value.Year() >= 1 && value.Year() <= 9999 &&
		value.Location() == time.UTC && value == value.Round(0)
}

func validBackupManifestOutcome(value string) bool {
	switch value {
	case "snapshotting", "snapshot_complete", "migration_in_progress", "dry_run", "complete", "failed", "outcome_unknown":
		return true
	default:
		return false
	}
}

func validBackupManifestRole(value string) bool {
	switch value {
	case "database", "wal", "shm", "journal", "legacy":
		return true
	default:
		return false
	}
}

func validBackupDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for index := range value {
		character := value[index]
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}
