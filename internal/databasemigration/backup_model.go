package databasemigration

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	backupManifestVersion = 2
	backupManifestName    = "manifest.json"
	// snapshot.complete.sha256 is the immutable snapshot-complete marker. It is
	// created only after every payload and manifest byte is durable.
	backupManifestHash    = "snapshot.complete.sha256"
	backupStatusSuffix    = ".migration-status.json"
	backupStatusVersion   = 2
	backupPartialSuffix   = ".partial"
	backupMaxEntries      = 65_536
	backupMaxLegacyRoots  = backupMaxEntries / 2
	backupMaxFiles        = 32_768
	backupMaxDepth        = 64
	backupMaxArchiveDepth = backupMaxDepth + 16
	backupMaxFileBytes    = int64(8 << 30)
	backupMaxTotalBytes   = int64(32 << 30)
	backupMaxManifestSize = int64(16 << 20)
	backupMaxErrorBytes   = 4 << 10
	backupMaxStatusSize   = int64(32 << 10)
	backupMaxPathBytes    = 16 << 10
	backupMaxComponent    = 255
)

// BackupManifest is the durable inventory required to verify and restore an
// exact pre-migration generation.
type BackupManifest struct {
	Version            int                   `json:"version"`
	CreatedAt          time.Time             `json:"created_at"`
	CaptureMode        string                `json:"capture_mode"`
	Stores             []BackupStoreManifest `json:"stores"`
	Files              []BackupFileManifest  `json:"files"`
	CatalogGenerations []string              `json:"catalog_generations"`
}

// BackupStoreManifest identifies one selected logical store without exposing
// it through the normal application catalog API.
type BackupStoreManifest struct {
	StoreID         string   `json:"store_id"`
	Path            string   `json:"path"`
	Exists          bool     `json:"exists"`
	LegacyRoots     []string `json:"legacy_roots"`
	LegacyRootKinds []string `json:"legacy_root_kinds"`
}

// BackupFileManifest records a copied generation or legacy input.
type BackupFileManifest struct {
	StoreID        string `json:"store_id"`
	Role           string `json:"role"`
	LegacyRoot     int    `json:"legacy_root,omitempty"`
	Source         string `json:"source"`
	SourceIdentity string `json:"source_identity"`
	Backup         string `json:"backup"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
	Mode           uint32 `json:"mode"`
	// SourceMode preserves restore metadata while Mode remains owner-only.
	SourceMode uint32 `json:"source_mode"`
}

type backupBudget struct {
	entries         int
	archiveEntries  int
	files           int
	bytes           int64
	manifestBytes   int64
	preparedEntries int
	maxEntries      int
	maxFiles        int
	maxDepth        int
	maxFileBytes    int64
	maxBytes        int64
	maxManifest     int64
}

func newBackupBudget() *backupBudget {
	return &backupBudget{
		maxEntries: backupMaxEntries, maxFiles: backupMaxFiles, maxDepth: backupMaxDepth,
		maxFileBytes: backupMaxFileBytes, maxBytes: backupMaxTotalBytes,
		maxManifest: backupMaxManifestSize, archiveEntries: 3, manifestBytes: 4 << 10,
	}
}

func (b *backupBudget) reserveManifestStrings(fixed int64, values ...string) error {
	if b == nil || fixed < 0 || b.maxManifest < 0 || fixed > b.maxManifest-b.manifestBytes {
		return errors.New("database backup manifest metadata budget is exceeded")
	}
	next := b.manifestBytes + fixed
	for _, value := range values {
		// JSON encoding cannot be shorter than the raw UTF-8 input. Reject
		// oversized strings before allocating their escaped representation.
		if int64(len(value)+16) > b.maxManifest-next {
			return errors.New("database backup manifest metadata budget is exceeded")
		}
		// Encoding a valid Go string as JSON cannot fail.
		encoded, _ := json.Marshal(value)
		// Include conservative per-element JSON indentation, comma, newline,
		// and enclosing-field overhead before any aggregate marshal occurs.
		cost := int64(len(encoded) + 16)
		if cost > b.maxManifest-next {
			return errors.New("database backup manifest metadata budget is exceeded")
		}
		next += cost
	}
	b.manifestBytes = next
	return nil
}

func (b *backupBudget) reserveManifestStore(store BackupStoreManifest) error {
	values := make([]string, 0, 2+len(store.LegacyRoots)+len(store.LegacyRootKinds))
	values = append(values, store.StoreID, store.Path)
	values = append(values, store.LegacyRoots...)
	values = append(values, store.LegacyRootKinds...)
	return b.reserveManifestStrings(192, values...)
}

func (b *backupBudget) reserveManifestFile(record BackupFileManifest) error {
	return b.reserveManifestStrings(
		256,
		record.StoreID,
		record.Role,
		record.Source,
		record.SourceIdentity,
		record.Backup,
		record.SHA256,
	)
}

func (b *backupBudget) enter(relative string) error {
	if b == nil {
		return errors.New("database backup traversal budget is unavailable")
	}
	clean := filepath.Clean(relative)
	if filepath.IsAbs(clean) || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return errors.New("database backup traversal path is invalid")
	}
	depth := backupPathDepth(clean)
	if depth > b.maxDepth {
		return errors.New("database backup traversal depth limit exceeded")
	}
	b.entries++
	if b.entries > b.maxEntries {
		return errors.New("database backup traversal entry limit exceeded")
	}
	return nil
}

func (b *backupBudget) reserveFile(size int64) error {
	if b == nil || size < 0 {
		return errors.New("database backup file budget is invalid")
	}
	if size > b.maxFileBytes {
		return errors.New("database backup file size limit exceeded")
	}
	if b.files >= b.maxFiles {
		return errors.New("database backup file count limit exceeded")
	}
	if size > b.maxBytes-b.bytes {
		return errors.New("database backup total size limit exceeded")
	}
	b.files++
	b.bytes += size
	return nil
}

func (b *backupBudget) reserveArchivePath(relative string) error {
	if b == nil || !safeBackupRelative(relative) {
		return errors.New("database backup archive path budget is invalid")
	}
	depth := backupPathDepth(relative)
	if depth > backupMaxArchiveDepth || depth > b.maxEntries-b.archiveEntries {
		return errors.New("database backup archive entry budget is exceeded")
	}
	b.archiveEntries += depth
	return nil
}

func (b *backupBudget) reservePreparedLegacyRoots(count int) error {
	if b == nil || count < 0 || count > backupMaxLegacyRoots ||
		count > (b.maxEntries-b.preparedEntries)/2 {
		return errors.New("database backup prepared legacy-root budget is exceeded")
	}
	b.preparedEntries += count * 2
	return nil
}

func (b *backupBudget) reservePreparedLegacyPath(relative string) error {
	if b == nil || relative == "" {
		return errors.New("database backup prepared legacy path is invalid")
	}
	if relative == "." {
		return nil
	}
	if !safeBackupRelative(relative) {
		return errors.New("database backup prepared legacy path is invalid")
	}
	depth := backupPathDepth(relative)
	if depth > b.maxDepth || depth > b.maxEntries-b.preparedEntries {
		return errors.New("database backup prepared legacy entry budget is exceeded")
	}
	b.preparedEntries += depth
	return nil
}

func sortBackupManifestFiles(files []BackupFileManifest) {
	sort.Slice(files, func(i, j int) bool {
		return backupManifestFileLess(files[i], files[j])
	})
}

func backupRoleOrder(role string) int {
	switch role {
	case "database":
		return 0
	case "wal":
		return 1
	case "shm":
		return 2
	case "journal":
		return 3
	case "legacy":
		return 4
	default:
		return 5
	}
}

func backupStoreDirectory(storeID string) string {
	encoded := hex.EncodeToString([]byte(storeID))
	parts := []string{"by-id"}
	for len(encoded) > 120 {
		parts = append(parts, encoded[:120])
		encoded = encoded[120:]
	}
	parts = append(parts, encoded)
	return filepath.Join(parts...)
}

func legacySourceRelative(root, source string) (string, bool, error) {
	if !validBackupAbsolutePath(root) || !validBackupAbsolutePath(source) {
		return "", false, errors.New("legacy backup source path is invalid")
	}
	cleanRoot, cleanSource := filepath.Clean(root), filepath.Clean(source)
	rootKey, sourceKey := backupPathKey(cleanRoot), backupPathKey(cleanSource)
	if sourceKey == rootKey {
		return ".", true, nil
	}
	rootPrefix := rootKey
	if !strings.HasSuffix(rootPrefix, string(os.PathSeparator)) {
		rootPrefix += string(os.PathSeparator)
	}
	if !strings.HasPrefix(sourceKey, rootPrefix) {
		return "", false, nil
	}
	relative, _ := filepath.Rel(cleanRoot, cleanSource)
	return relative, true, nil
}

func backupFilePath(root, relativeValue string) (string, error) {
	if !validBackupAbsolutePath(root) || !validBackupManifestRelative(relativeValue) {
		return "", errors.New("database backup manifest path is invalid")
	}
	relative := filepath.Clean(filepath.FromSlash(relativeValue))
	return filepath.Join(root, relative), nil
}

func safeBackupRelative(relative string) bool {
	return relative != "." && len(relative) <= backupMaxPathBytes && utf8.ValidString(relative) &&
		!strings.ContainsRune(relative, 0) && relative == filepath.Clean(relative) &&
		!filepath.IsAbs(relative) && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(os.PathSeparator)) &&
		backupPathDepth(relative) <= backupMaxArchiveDepth &&
		validBackupPlatformPath(relative, false) && validBackupPathComponents(relative)
}

func backupPathDepth(path string) int {
	depth := 0
	for current := filepath.Clean(path); current != "."; {
		depth++
		parent := filepath.Dir(current)
		if parent == current {
			return backupMaxArchiveDepth + 1
		}
		current = parent
	}
	return depth
}

func validBackupManifestRelative(value string) bool {
	if value == "" || len(value) > backupMaxPathBytes || !utf8.ValidString(value) ||
		strings.ContainsRune(value, 0) || !validBackupPlatformPath(value, false) {
		return false
	}
	relative := filepath.FromSlash(value)
	return filepath.ToSlash(filepath.Clean(relative)) == value && safeBackupRelative(relative)
}

func validBackupAbsolutePath(path string) bool {
	return path != "" && len(path) <= backupMaxPathBytes && utf8.ValidString(path) &&
		!strings.ContainsRune(path, 0) && filepath.IsAbs(path) &&
		filepath.Clean(path) == path && validBackupPlatformPath(path, true) &&
		validBackupPathComponents(path)
}

func validBackupPathComponents(path string) bool {
	volume := filepath.VolumeName(path)
	remainder := strings.TrimPrefix(path, volume)
	for _, component := range strings.Split(remainder, string(os.PathSeparator)) {
		if component == "" || component == "." {
			continue
		}
		if component == ".." || len(component) > backupMaxComponent ||
			!utf8.ValidString(component) || strings.ContainsRune(component, 0) {
			return false
		}
	}
	return true
}

func marshalBackupManifest(manifest BackupManifest) ([]byte, error) {
	return marshalBackupManifestLimit(manifest, backupMaxManifestSize)
}

func marshalBackupManifestLimit(manifest BackupManifest, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("database backup manifest size limit is invalid")
	}
	if err := validateBackupManifestLimit(manifest, limit); err != nil {
		return nil, err
	}
	// The validated manifest contains only JSON-safe scalar and slice fields.
	payload, _ := json.MarshalIndent(manifest, "", "  ")
	payload = append(payload, '\n')
	if int64(len(payload)) > limit {
		return nil, errors.New("database backup manifest size limit exceeded")
	}
	return payload, nil
}

func generationPaths(path string) []string {
	return []string{path, path + "-wal", path + "-shm", path + "-journal"}
}

func validBackupPathComponent(component string) bool {
	return component != "" && component != "." && component != ".." &&
		len(component) <= backupMaxComponent && utf8.ValidString(component) &&
		!strings.ContainsRune(component, 0) &&
		!strings.ContainsRune(component, os.PathSeparator) &&
		validBackupPlatformComponent(component)
}

func backupPathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func backupPathHasSuffix(path, suffix string) bool {
	return strings.HasSuffix(backupPathKey(filepath.Base(path)), backupPathKey(suffix))
}
