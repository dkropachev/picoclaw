package databasemigration

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/database"
)

// validateBackupManifest rejects malformed or attacker-amplified metadata
// before it can be serialized or used to select backup files.
func validateBackupManifest(manifest BackupManifest) error {
	return validateBackupManifestLimit(manifest, backupMaxManifestSize)
}

func validateBackupManifestLimit(manifest BackupManifest, manifestLimit int64) error {
	if manifestLimit <= 0 || manifestLimit > backupMaxManifestSize {
		return errors.New("database backup manifest metadata limit is invalid")
	}
	if manifest.Version != backupManifestVersion {
		return errors.New("database backup manifest version is invalid")
	}
	if !validBackupManifestTime(manifest.CreatedAt) {
		return errors.New("database backup manifest timestamp is invalid")
	}
	if manifest.CaptureMode != "offline_quiescent_raw" {
		return errors.New("database backup manifest capture mode is invalid")
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
	if manifest.Stores == nil || manifest.Files == nil || manifest.CatalogGenerations == nil {
		return errors.New("database backup manifest inventory arrays are invalid")
	}

	stores := make(map[database.StoreID]BackupStoreManifest, len(manifest.Stores))
	metadataBudget := newBackupBudget()
	metadataBudget.maxManifest = manifestLimit
	legacyScopes := make(map[string]struct{})
	legacyRootTotal := 0
	previousStoreID := ""
	for index, record := range manifest.Stores {
		id, err := database.ParseStoreID(record.StoreID)
		if err != nil || id.String() != record.StoreID {
			return errors.New("database backup manifest store ID is invalid")
		}
		if index > 0 && record.StoreID <= previousStoreID {
			return errors.New("database backup manifest stores are not canonically ordered")
		}
		previousStoreID = record.StoreID
		if !validBackupAbsolutePath(record.Path) || record.LegacyRoots == nil ||
			record.LegacyRootKinds == nil || len(record.LegacyRootKinds) != len(record.LegacyRoots) ||
			len(record.LegacyRoots) > backupMaxLegacyRoots ||
			len(record.LegacyRoots) > backupMaxLegacyRoots-legacyRootTotal {
			return errors.New("database backup manifest store provenance is invalid")
		}
		legacyRootTotal += len(record.LegacyRoots)
		if err := metadataBudget.reserveManifestStore(record); err != nil {
			return err
		}
		for rootIndex, root := range record.LegacyRoots {
			if !validBackupAbsolutePath(root) {
				return errors.New("database backup manifest legacy root is invalid")
			}
			key := backupPathKey(root)
			if _, duplicate := legacyScopes[key]; duplicate {
				return errors.New("database backup manifest source namespaces overlap")
			}
			legacyScopes[key] = struct{}{}
			if !validBackupLegacyRootKind(record.LegacyRootKinds[rootIndex]) {
				return errors.New("database backup manifest legacy root kind is invalid")
			}
		}
		// Strict ascending order above already excludes duplicate StoreIDs.
		stores[id] = record
	}
	preparedEntries := legacyRootTotal * 2

	backupPaths := make(map[string]struct{}, len(manifest.Files))
	records := make(map[string]struct{}, len(manifest.Files))
	sourceIdentities := make(map[string]string, len(manifest.Files))
	sourcePaths := make(map[string]string, len(manifest.Files))
	generationRoles := make(map[string]struct{}, len(manifest.Files))
	generationStates := make(map[database.StoreID]backupManifestGenerationState, len(manifest.Stores))
	legacyRecordCounts := make(map[database.StoreID][]int, len(manifest.Stores))
	var totalSize int64
	archiveEntries := 3 // root and two control files
	if !sort.SliceIsSorted(manifest.Files, func(i, j int) bool {
		return backupManifestFileLess(manifest.Files[i], manifest.Files[j])
	}) {
		return errors.New("database backup manifest files are not canonically ordered")
	}
	for _, record := range manifest.Files {
		if err := metadataBudget.reserveManifestFile(record); err != nil {
			return err
		}
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
		if record.LegacyRoot < 0 || record.LegacyRoot >= backupMaxLegacyRoots ||
			record.Role != "legacy" && record.LegacyRoot != 0 {
			return errors.New("database backup manifest legacy root is invalid")
		}
		if record.Role == "legacy" && record.LegacyRoot >= len(store.LegacyRoots) {
			return errors.New("database backup manifest legacy root is outside its store")
		}
		if record.Role == "legacy" {
			relative, inside, relativeErr := legacySourceRelative(
				store.LegacyRoots[record.LegacyRoot], record.Source,
			)
			if relativeErr != nil || !inside {
				return errors.Join(
					errors.New("database backup legacy source is outside its root"), relativeErr,
				)
			}
			if relative != "." {
				depth := backupPathDepth(relative)
				if depth > backupMaxEntries-preparedEntries {
					return errors.New("database backup prepared legacy entry budget is exceeded")
				}
				preparedEntries += depth
			}
		}
		if !validBackupAbsolutePath(record.Source) ||
			!validBackupManifestRelative(record.Backup) {
			return errors.New("database backup manifest file path is invalid")
		}
		depth := backupPathDepth(filepath.FromSlash(record.Backup))
		if depth > backupMaxEntries-archiveEntries {
			return errors.New("database backup manifest archive entry budget is exceeded")
		}
		archiveEntries += depth
		if record.SourceIdentity == "" ||
			len(record.SourceIdentity) > backupMaxPathBytes ||
			!utf8.ValidString(record.SourceIdentity) ||
			strings.ContainsRune(record.SourceIdentity, 0) {
			return errors.New("database backup manifest source identity is invalid")
		}
		if previous, present := sourceIdentities[record.SourceIdentity]; present &&
			!sameBackupPhysicalPath(previous, record.Source) {
			return errors.New("database backup manifest sources contain a physical alias")
		}
		sourceIdentities[record.SourceIdentity] = record.Source
		sourceKey := backupPathKey(record.Source)
		if previous, present := sourcePaths[sourceKey]; present && previous != record.SourceIdentity {
			return errors.New("database backup manifest source identity is inconsistent")
		}
		sourcePaths[sourceKey] = record.SourceIdentity
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
		if err := validateBackupRecordProvenance(store, record); err != nil {
			return err
		}
		if record.Role == "legacy" {
			counts := legacyRecordCounts[id]
			if counts == nil {
				counts = make([]int, len(store.LegacyRoots))
			}
			counts[record.LegacyRoot]++
			legacyRecordCounts[id] = counts
		}

		backupKey := backupPathKey(filepath.FromSlash(record.Backup))
		if _, duplicate := backupPaths[backupKey]; duplicate {
			return errors.New("database backup manifest backup path is duplicated")
		}
		backupPaths[backupKey] = struct{}{}
		recordKey := record.StoreID + "\x00" + record.Role + "\x00" +
			strconv.Itoa(record.LegacyRoot) + "\x00" + backupPathKey(record.Source)
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
			state.shm = true
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
		if state.shm && !state.wal {
			return errors.New("database backup manifest has SHM without WAL")
		}
		counts := legacyRecordCounts[id]
		for index, kind := range record.LegacyRootKinds {
			count := 0
			if counts != nil {
				count = counts[index]
			}
			if kind == "file" && count != 1 || kind == "missing" && count != 0 {
				return errors.New("database backup manifest legacy root inventory is inconsistent")
			}
		}
	}

	catalogPaths := make(map[string]struct{}, len(manifest.CatalogGenerations))
	catalogScopes := make([]string, 0, len(manifest.CatalogGenerations))
	previousCatalogKey := ""
	previousCatalogPath := ""
	for index, path := range manifest.CatalogGenerations {
		if err := metadataBudget.reserveManifestStrings(0, path); err != nil {
			return err
		}
		if !validBackupAbsolutePath(path) {
			return errors.New("database backup manifest catalog generation is invalid")
		}
		key := backupPathKey(path)
		if index > 0 && (key < previousCatalogKey || key == previousCatalogKey && path <= previousCatalogPath) {
			return errors.New("database backup manifest catalog generations are not canonically ordered")
		}
		previousCatalogKey, previousCatalogPath = key, path
		if _, duplicate := catalogPaths[key]; duplicate {
			return errors.New("database backup manifest catalog generation is duplicated")
		}
		catalogPaths[key] = struct{}{}
		catalogScopes = append(catalogScopes, path)
	}
	for key := range legacyScopes {
		if _, collision := catalogPaths[key]; collision {
			return errors.New("database backup manifest source namespaces overlap")
		}
	}
	if backupGenerationScopesOverlap(catalogScopes) {
		return errors.New("database backup manifest generation namespaces overlap")
	}
	for _, store := range manifest.Stores {
		for _, path := range generationPaths(store.Path) {
			if _, present := catalogPaths[backupPathKey(path)]; !present {
				return errors.New("database backup manifest omits a selected catalog generation")
			}
		}
	}
	return nil
}

func sameBackupPhysicalPath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

// backupGenerationScopesOverlap detects generation containment. Exact
// collisions are rejected while constructing the catalog-generation map.
// Appending a separator makes every ancestor sort immediately before its
// descendant range without an O(n^2) comparison across bounded inventories.
func backupGenerationScopesOverlap(paths []string) bool {
	keys := make([]string, len(paths))
	separator := string(os.PathSeparator)
	for index, path := range paths {
		key := backupPathKey(path)
		if !strings.HasSuffix(key, separator) {
			key += separator
		}
		keys[index] = key
	}
	sort.Strings(keys)
	for index := 1; index < len(keys); index++ {
		if strings.HasPrefix(keys[index], keys[index-1]) {
			return true
		}
	}
	return false
}

type backupManifestGenerationState struct {
	database bool
	sidecar  bool
	wal      bool
	shm      bool
	journal  bool
}

func validBackupManifestTime(value time.Time) bool {
	return !value.IsZero() && value.Year() >= 1 && value.Year() <= 9999 &&
		value.Location() == time.UTC && value == value.Round(0)
}

func validBackupManifestRole(value string) bool {
	switch value {
	case "database", "wal", "shm", "journal", "legacy":
		return true
	default:
		return false
	}
}

func validBackupLegacyRootKind(value string) bool {
	switch value {
	case "missing", "file", "directory":
		return true
	default:
		return false
	}
}

func backupManifestFileLess(left, right BackupFileManifest) bool {
	if left.StoreID != right.StoreID {
		return left.StoreID < right.StoreID
	}
	leftRole, rightRole := backupRoleOrder(left.Role), backupRoleOrder(right.Role)
	if leftRole != rightRole {
		return leftRole < rightRole
	}
	if left.LegacyRoot != right.LegacyRoot {
		return left.LegacyRoot < right.LegacyRoot
	}
	leftSource, rightSource := backupPathKey(left.Source), backupPathKey(right.Source)
	if leftSource != rightSource {
		return leftSource < rightSource
	}
	return left.Source < right.Source
}

func validateBackupRecordProvenance(
	store BackupStoreManifest,
	record BackupFileManifest,
) error {
	storeDirectory := backupStoreDirectory(record.StoreID)
	if record.Role == "legacy" {
		root := store.LegacyRoots[record.LegacyRoot]
		kind := store.LegacyRootKinds[record.LegacyRoot]
		if kind == "missing" || kind == "file" && record.Source != root ||
			kind == "directory" && backupPathKey(record.Source) == backupPathKey(root) {
			return errors.New("database backup manifest legacy root layout is invalid")
		}
		expected, err := legacyBackupDestination(storeDirectory, record.LegacyRoot, root, record.Source)
		if err != nil || filepath.ToSlash(expected) != record.Backup {
			return errors.New("database backup manifest legacy provenance is invalid")
		}
		return nil
	}
	roles := []string{"database", "wal", "shm", "journal"}
	paths := generationPaths(store.Path)
	for index, role := range roles {
		if record.Role != role {
			continue
		}
		expectedBackup := filepath.ToSlash(filepath.Join("stores", storeDirectory, "generation", role))
		if record.Source != paths[index] || record.Backup != expectedBackup {
			return errors.New("database backup manifest generation provenance is invalid")
		}
		return nil
	}
	return errors.New("database backup manifest file role is invalid")
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
