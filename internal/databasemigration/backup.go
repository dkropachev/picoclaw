//nolint:govet // Snapshot stages intentionally use narrow error scopes.
package databasemigration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const (
	backupManifestVersion = 1
	backupManifestName    = "manifest.json"
	backupManifestHash    = "manifest.sha256"
	backupMaxEntries      = 65_536
	backupMaxFiles        = 32_768
	backupMaxDepth        = 64
	backupMaxFileBytes    = int64(8 << 30)
	backupMaxTotalBytes   = int64(32 << 30)
	backupMaxManifestSize = int64(16 << 20)
	backupMaxErrorBytes   = 4 << 10
	backupMaxPathBytes    = 16 << 10
	backupMaxComponent    = 255
)

// BackupManifest is the durable inventory required to verify and restore an
// exact pre-migration generation.
type BackupManifest struct {
	Version            int                   `json:"version"`
	CreatedAt          time.Time             `json:"created_at"`
	Outcome            string                `json:"outcome"`
	Error              string                `json:"error,omitempty"`
	Stores             []BackupStoreManifest `json:"stores"`
	Files              []BackupFileManifest  `json:"files"`
	CatalogGenerations []string              `json:"catalog_generations"`
}

// BackupStoreManifest identifies one selected logical store without exposing
// it through the normal application catalog API.
type BackupStoreManifest struct {
	StoreID     string `json:"store_id"`
	Exists      bool   `json:"exists"`
	LegacyRoots int    `json:"legacy_roots"`
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

type backupSession struct {
	root     string
	manifest BackupManifest
}

type backupBudget struct {
	entries      int
	files        int
	bytes        int64
	maxEntries   int
	maxFiles     int
	maxDepth     int
	maxFileBytes int64
	maxBytes     int64
}

func newBackupBudget() *backupBudget {
	return &backupBudget{
		maxEntries: backupMaxEntries, maxFiles: backupMaxFiles, maxDepth: backupMaxDepth,
		maxFileBytes: backupMaxFileBytes, maxBytes: backupMaxTotalBytes,
	}
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
	depth := 0
	if clean != "." {
		for current := clean; current != "."; current = filepath.Dir(current) {
			depth++
		}
	}
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

func snapshotBackup(
	ctx context.Context,
	now func() time.Time,
	home string,
	all []storecatalog.Spec,
	specs []storecatalog.Spec,
	configuredParent string,
) (*backupSession, error) {
	if now == nil {
		return nil, errors.New("database backup clock is unavailable")
	}
	parent, err := validateBackupParent(configuredParent, home, all)
	if err != nil {
		return nil, err
	}
	if !validBackupAbsolutePath(parent) {
		return nil, errors.New("database backup directory is invalid")
	}
	customParent := strings.TrimSpace(configuredParent) != "" &&
		backupPathKey(parent) != backupPathKey(filepath.Join(home, "backups"))
	if customParent {
		info, statErr := os.Lstat(parent)
		if statErr != nil || info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.Join(
				errors.New("custom database backup directory must already exist"), statErr,
			)
		}
		if err := fileutil.ValidatePrivateDirectory(parent, info); err != nil {
			return nil, fmt.Errorf("validate custom database backup directory: %w", err)
		}
	}
	if err := ensurePrivateBackupDirectory(parent); err != nil {
		return nil, err
	}
	timestamp := now().UTC()
	name := "database-migrate-" + timestamp.Format("20060102T150405.000000000Z")
	root := filepath.Join(parent, name)
	if err := os.Mkdir(root, 0o700); err != nil {
		return nil, fmt.Errorf("create database migration backup: %w", err)
	}
	if err := secureAndValidateBackupDirectory(root); err != nil {
		return &backupSession{root: root}, fmt.Errorf("secure database migration backup: %w", err)
	}
	if err := fileutil.SyncDirectory(parent); err != nil {
		return &backupSession{root: root}, fmt.Errorf("sync database backup parent: %w", err)
	}
	session := &backupSession{root: root, manifest: BackupManifest{
		Version: backupManifestVersion, CreatedAt: timestamp, Outcome: "snapshotting",
	}}
	for _, spec := range specs {
		session.manifest.Stores = append(
			session.manifest.Stores,
			BackupStoreManifest{
				StoreID: spec.ID.String(), LegacyRoots: len(spec.LegacyRoots),
			},
		)
	}
	failed := func(snapshotErr error) (*backupSession, error) {
		manifestErr := session.finish("failed", snapshotErr)
		return session, errors.Join(snapshotErr, manifestErr)
	}

	excluded := make(map[string]struct{}, len(all)*4)
	for _, known := range all {
		for _, generation := range generationPaths(known.Path) {
			key := backupPathKey(generation)
			if _, duplicate := excluded[key]; duplicate {
				continue
			}
			excluded[key] = struct{}{}
			session.manifest.CatalogGenerations = append(
				session.manifest.CatalogGenerations,
				filepath.Clean(generation),
			)
		}
	}
	sort.Slice(session.manifest.CatalogGenerations, func(left, right int) bool {
		leftKey := backupPathKey(session.manifest.CatalogGenerations[left])
		rightKey := backupPathKey(session.manifest.CatalogGenerations[right])
		if leftKey != rightKey {
			return leftKey < rightKey
		}
		return session.manifest.CatalogGenerations[left] < session.manifest.CatalogGenerations[right]
	})
	if err := session.finish("snapshotting", nil); err != nil {
		return session, fmt.Errorf("initialize database backup manifest: %w", err)
	}
	seenLegacy := make(map[string]struct{})
	budget := newBackupBudget()
	generationIdentities := make(map[fileidentity.Identity]struct{}, len(all)*4)
	for _, known := range all {
		for _, generation := range generationPaths(known.Path) {
			info, statErr := os.Lstat(generation)
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				return failed(statErr)
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return failed(errors.New("database generation is unsafe"))
			}
			identity, exists, identityErr := fileidentity.Existing(generation)
			if identityErr != nil {
				return failed(identityErr)
			}
			if !exists {
				return failed(errors.New("database generation changed during identity lookup"))
			}
			after, statErr := os.Lstat(generation)
			if statErr != nil || !os.SameFile(info, after) || !after.Mode().IsRegular() ||
				after.Mode()&os.ModeSymlink != 0 {
				return failed(errors.Join(
					errors.New("database generation changed during identity lookup"), statErr,
				))
			}
			generationIdentities[identity] = struct{}{}
		}
	}
	legacyIdentities := make(map[fileidentity.Identity]string)
	for storeIndex, spec := range specs {
		if err := ctx.Err(); err != nil {
			return failed(err)
		}
		storeID := spec.ID.String()
		storeDirectory := backupStoreDirectory(storeID)
		mainExists := false
		for generationIndex, source := range generationPaths(spec.Path) {
			info, statErr := os.Lstat(source)
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				return failed(fmt.Errorf("inspect store %s generation: %w", spec.ID, statErr))
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return failed(fmt.Errorf("store %s generation is unsafe", spec.ID))
			}
			if generationIndex == 0 {
				mainExists = true
			} else if !mainExists {
				return failed(fmt.Errorf("store %s has a sidecar without its database", spec.ID))
			}
			role := []string{"database", "wal", "shm", "journal"}[generationIndex]
			identity, identityExists, identityErr := fileidentity.Existing(source)
			if identityErr != nil || !identityExists {
				return failed(errors.Join(
					fmt.Errorf("store %s %s identity is unavailable", spec.ID, role), identityErr,
				))
			}
			destination := filepath.Join("stores", storeDirectory, "generation", role)
			record, copyErr := copyBackupFile(
				ctx, session.root, storeID, role, source, destination, budget,
			)
			if copyErr != nil {
				return failed(fmt.Errorf("snapshot store %s %s: %w", spec.ID, role, copyErr))
			}
			afterIdentity, afterExists, identityErr := fileidentity.Existing(source)
			if identityErr != nil || !afterExists || afterIdentity != identity {
				return failed(errors.Join(
					fmt.Errorf("store %s %s identity changed during snapshot", spec.ID, role), identityErr,
				))
			}
			record.SourceIdentity = identity.String()
			generationIdentities[identity] = struct{}{}
			session.manifest.Files = append(session.manifest.Files, record)
			if generationIndex == 0 {
				session.manifest.Stores[storeIndex].Exists = true
			}
		}
		for legacyRootIndex, legacyRoot := range spec.LegacyRoots {
			walkErr := walkLegacyInputs(
				ctx, legacyRoot, session.root, excluded, budget, func(source string) error {
					canonical := backupPathKey(source)
					storeSource := storeID + "\x00" + canonical
					if _, duplicate := seenLegacy[storeSource]; duplicate {
						return nil
					}
					seenLegacy[storeSource] = struct{}{}
					identity, exists, identityErr := fileidentity.Existing(source)
					if identityErr != nil {
						return identityErr
					}
					if !exists {
						return errors.New("legacy input disappeared during snapshot")
					}
					if _, alias := generationIdentities[identity]; alias {
						return errors.New("legacy input aliases a database generation")
					}
					if previous, alias := legacyIdentities[identity]; alias && previous != canonical {
						return errors.New("legacy inputs contain a physical alias")
					}
					legacyIdentities[identity] = canonical
					digest := sha256.Sum256([]byte(canonical))
					destination := filepath.Join(
						"stores", storeDirectory, "legacy", hex.EncodeToString(digest[:8]), filepath.Base(source),
					)
					record, copyErr := copyBackupFile(
						ctx, session.root, storeID, "legacy", source, destination, budget,
					)
					if copyErr != nil {
						return copyErr
					}
					afterIdentity, afterExists, identityErr := fileidentity.Existing(source)
					if identityErr != nil || !afterExists || afterIdentity != identity {
						return errors.Join(
							errors.New("legacy input identity changed during snapshot"), identityErr,
						)
					}
					record.SourceIdentity = identity.String()
					record.LegacyRoot = legacyRootIndex
					session.manifest.Files = append(session.manifest.Files, record)
					return nil
				},
			)
			if walkErr != nil {
				return failed(fmt.Errorf("snapshot store %s legacy inputs: %w", spec.ID, walkErr))
			}
		}
	}
	sort.Slice(session.manifest.Files, func(i, j int) bool {
		left, right := session.manifest.Files[i], session.manifest.Files[j]
		if left.StoreID != right.StoreID {
			return left.StoreID < right.StoreID
		}
		if left.Role != right.Role {
			return left.Role < right.Role
		}
		return left.Source < right.Source
	})
	if err := session.finish("snapshot_complete", nil); err != nil {
		return session, fmt.Errorf("write database backup manifest: %w", err)
	}
	return session, nil
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

func (b *backupSession) hasLegacy(id database.StoreID) bool {
	if b == nil {
		return false
	}
	for _, file := range b.manifest.Files {
		if file.StoreID == id.String() && file.Role == "legacy" {
			return true
		}
	}
	return false
}

// prepareGeneration recreates one provider generation exclusively from
// verified backup bytes. Returned main path may be absent when selected store
// was absent at snapshot time. Sidecars use SQLite's exact sibling names.
func (b *backupSession) prepareGeneration(
	ctx context.Context,
	spec storecatalog.Spec,
) (string, func() error, error) {
	noCleanup := func() error { return nil }
	if ctx == nil {
		ctx = context.Background()
	}
	if b == nil || b.root == "" || !spec.ID.Valid() {
		return "", noCleanup, errors.New("database backup generation is unavailable")
	}
	if err := b.verifyStore(ctx, spec.ID); err != nil {
		return "", noCleanup, fmt.Errorf("verify generation backup: %w", err)
	}

	storeID := spec.ID.String()
	storeFound := false
	storeExists := false
	for _, store := range b.manifest.Stores {
		if store.StoreID != storeID {
			continue
		}
		if storeFound {
			return "", noCleanup, errors.New("database backup store manifest is duplicated")
		}
		storeFound = true
		storeExists = store.Exists
		if store.LegacyRoots != len(spec.LegacyRoots) {
			return "", noCleanup, errors.New("database backup legacy-root count changed")
		}
	}
	if !storeFound {
		return "", noCleanup, errors.New("database backup store manifest is missing")
	}

	records := make(map[string]BackupFileManifest, 4)
	for _, record := range b.manifest.Files {
		if record.StoreID != storeID {
			continue
		}
		switch record.Role {
		case "database", "wal", "shm", "journal":
			if _, duplicate := records[record.Role]; duplicate {
				return "", noCleanup, errors.New("database backup generation role is duplicated")
			}
			records[record.Role] = record
		}
	}
	_, mainExists := records["database"]
	if mainExists != storeExists {
		return "", noCleanup, errors.New("database backup generation manifest is inconsistent")
	}
	if !mainExists && len(records) != 0 {
		return "", noCleanup, errors.New("database backup has a sidecar without its database")
	}
	if _, wal := records["wal"]; wal {
		if _, journal := records["journal"]; journal {
			return "", noCleanup, errors.New("database backup mixes WAL and rollback-journal state")
		}
	}

	parent := filepath.Dir(b.root)
	workRoot, err := os.MkdirTemp(parent, ".database-migration-generation-")
	if err != nil {
		return "", noCleanup, fmt.Errorf("create disposable migration generation: %w", err)
	}
	var cleanupOnce sync.Once
	var cleanupErr error
	cleanup := func() error {
		cleanupOnce.Do(func() {
			cleanupErr = os.RemoveAll(workRoot)
			if cleanupErr == nil {
				cleanupErr = fileutil.SyncDirectory(parent)
			}
		})
		return cleanupErr
	}
	fail := func(cause error) (string, func() error, error) {
		return "", noCleanup, errors.Join(cause, cleanup())
	}
	if err := secureAndValidateBackupDirectory(workRoot); err != nil {
		return fail(fmt.Errorf("secure disposable migration generation: %w", err))
	}
	if err := fileutil.SyncDirectory(parent); err != nil {
		return fail(fmt.Errorf("sync disposable migration generation parent: %w", err))
	}

	mainPath := filepath.Join(workRoot, "generation.db")
	budget := newBackupBudget()
	for roleIndex, role := range []string{"database", "wal", "shm", "journal"} {
		record, exists := records[role]
		if !exists {
			continue
		}
		source, sourceErr := backupFilePath(b.root, record.Backup)
		if sourceErr != nil {
			return fail(sourceErr)
		}
		suffix := []string{"", "-wal", "-shm", "-journal"}[roleIndex]
		destination := filepath.Base(mainPath) + suffix
		copied, copyErr := copyBackupFile(
			ctx, workRoot, storeID, role, source, destination, budget,
		)
		if copyErr != nil {
			return fail(fmt.Errorf("recreate disposable %s: %w", role, copyErr))
		}
		if copied.Size != record.Size || copied.SHA256 != record.SHA256 {
			return fail(errors.New("disposable database generation differs from backup"))
		}
	}
	return mainPath, cleanup, nil
}

// prepareLegacyInputs recreates one adapter's legacy view exclusively from
// verified backup bytes. The adapter never receives a live legacy path.
func (b *backupSession) prepareLegacyInputs(
	ctx context.Context,
	spec storecatalog.Spec,
) ([]string, func() error, error) {
	noCleanup := func() error { return nil }
	if len(spec.LegacyRoots) == 0 {
		return nil, noCleanup, nil
	}
	if b == nil || b.root == "" {
		return nil, noCleanup, errors.New("database legacy input backup is unavailable")
	}
	storeFound := false
	for _, store := range b.manifest.Stores {
		if store.StoreID == spec.ID.String() {
			storeFound = true
			if store.LegacyRoots != len(spec.LegacyRoots) {
				return nil, noCleanup, errors.New("database backup legacy-root count changed")
			}
			break
		}
	}
	if !storeFound {
		return nil, noCleanup, errors.New("database backup store manifest is missing")
	}
	if err := validateBackupManifest(b.manifest); err != nil {
		return nil, noCleanup, fmt.Errorf("validate legacy input backup: %w", err)
	}
	parent := filepath.Dir(b.root)
	workRoot, err := os.MkdirTemp(parent, ".database-migration-inputs-")
	if err != nil {
		return nil, noCleanup, fmt.Errorf("create disposable migration inputs: %w", err)
	}
	var cleanupOnce sync.Once
	var cleanupErr error
	cleanup := func() error {
		cleanupOnce.Do(func() {
			cleanupErr = os.RemoveAll(workRoot)
			if cleanupErr == nil {
				cleanupErr = fileutil.SyncDirectory(parent)
			}
		})
		return cleanupErr
	}
	fail := func(cause error) ([]string, func() error, error) {
		return nil, noCleanup, errors.Join(cause, cleanup())
	}
	if err := secureAndValidateBackupDirectory(workRoot); err != nil {
		return fail(fmt.Errorf("secure disposable migration inputs: %w", err))
	}

	type inputRecord struct {
		relative string
		record   BackupFileManifest
	}
	result := make([]string, len(spec.LegacyRoots))
	budget := newBackupBudget()
	for rootIndex, legacyRoot := range spec.LegacyRoots {
		leaf := filepath.Base(filepath.Clean(legacyRoot))
		if !validBackupPathComponent(leaf) {
			return fail(errors.New("legacy input root has an unsafe name"))
		}
		destinationRoot := filepath.Join(
			workRoot, fmt.Sprintf("root-%06d", rootIndex), leaf,
		)
		result[rootIndex] = destinationRoot
		var matches []inputRecord
		for _, record := range b.manifest.Files {
			if record.StoreID != spec.ID.String() || record.Role != "legacy" {
				continue
			}
			if record.LegacyRoot != rootIndex {
				continue
			}
			relative, inside, relativeErr := legacySourceRelative(legacyRoot, record.Source)
			if relativeErr != nil {
				return fail(relativeErr)
			}
			if inside {
				matches = append(matches, inputRecord{relative: relative, record: record})
			}
		}
		sort.Slice(matches, func(left, right int) bool {
			return matches[left].relative < matches[right].relative
		})
		rootIsFile := len(matches) == 1 && matches[0].relative == "."
		if len(matches) > 0 && !rootIsFile {
			if err := ensurePrivateBackupDirectory(destinationRoot); err != nil {
				return fail(err)
			}
		}
		for _, match := range matches {
			if match.relative == "." && !rootIsFile {
				return fail(errors.New("legacy input backup has conflicting root types"))
			}
			source, sourceErr := backupFilePath(b.root, match.record.Backup)
			if sourceErr != nil {
				return fail(sourceErr)
			}
			destination := destinationRoot
			if match.relative != "." {
				destination = filepath.Join(destinationRoot, match.relative)
			}
			relativeDestination, relErr := filepath.Rel(workRoot, destination)
			if relErr != nil {
				return fail(relErr)
			}
			copied, copyErr := copyBackupFile(
				ctx, workRoot, spec.ID.String(), "legacy-input", source,
				relativeDestination, budget,
			)
			if copyErr != nil {
				return fail(copyErr)
			}
			if copied.Size != match.record.Size || copied.SHA256 != match.record.SHA256 {
				return fail(errors.New("disposable legacy input differs from backup"))
			}
		}
	}
	return result, cleanup, nil
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
	if !strings.HasPrefix(sourceKey, rootKey+string(os.PathSeparator)) {
		return "", false, nil
	}
	relative, err := filepath.Rel(cleanRoot, cleanSource)
	if err != nil {
		return "", false, err
	}
	if !safeBackupRelative(relative) {
		return "", false, errors.New("legacy backup source escapes its root")
	}
	return relative, true, nil
}

func backupFilePath(root, relativeValue string) (string, error) {
	if !validBackupManifestRelative(relativeValue) {
		return "", errors.New("database backup manifest path is invalid")
	}
	relative := filepath.Clean(filepath.FromSlash(relativeValue))
	return filepath.Join(root, relative), nil
}

func safeBackupRelative(relative string) bool {
	return relative != "." && relative == filepath.Clean(relative) &&
		!filepath.IsAbs(relative) && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(os.PathSeparator)) &&
		validBackupPathComponents(relative)
}

func validBackupManifestRelative(value string) bool {
	if value == "" || len(value) > backupMaxPathBytes || !utf8.ValidString(value) ||
		strings.ContainsRune(value, 0) {
		return false
	}
	relative := filepath.FromSlash(value)
	return filepath.ToSlash(filepath.Clean(relative)) == value && safeBackupRelative(relative)
}

func validBackupAbsolutePath(path string) bool {
	return path != "" && len(path) <= backupMaxPathBytes && utf8.ValidString(path) &&
		!strings.ContainsRune(path, 0) && filepath.IsAbs(path) &&
		filepath.Clean(path) == path && validBackupPathComponents(path)
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

func (b *backupSession) finish(outcome string, migrationErr error) error {
	if b == nil || b.root == "" {
		return nil
	}
	b.manifest.Outcome = outcome
	if migrationErr != nil {
		b.manifest.Error = boundedBackupError(migrationErr)
	} else {
		b.manifest.Error = ""
	}
	payload, err := marshalBackupManifest(b.manifest)
	if err != nil {
		return err
	}
	if err := fileutil.WriteFileAtomic(filepath.Join(b.root, backupManifestName), payload, 0o600); err != nil {
		return err
	}
	if err := secureAndValidateBackupFile(filepath.Join(b.root, backupManifestName)); err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	hashPayload := append([]byte(hex.EncodeToString(digest[:])), '\n')
	if err := fileutil.WriteFileAtomic(filepath.Join(b.root, backupManifestHash), hashPayload, 0o600); err != nil {
		return err
	}
	if err := secureAndValidateBackupFile(filepath.Join(b.root, backupManifestHash)); err != nil {
		return err
	}
	return fileutil.SyncDirectory(b.root)
}

func boundedBackupError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToValidUTF8(err.Error(), "�")
	message = strings.ReplaceAll(message, "\x00", "�")
	if len(message) > backupMaxErrorBytes {
		message = message[:backupMaxErrorBytes]
	}
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}

// verify proves durable backup bytes and manifest match in-memory snapshot.
// Migration must call it before any provider open, recovery, or callback.
func (b *backupSession) verify(ctx context.Context) error {
	return b.verifyFiles(ctx, "")
}

func (b *backupSession) verifyStore(ctx context.Context, id database.StoreID) error {
	if !id.Valid() {
		return errors.New("database backup store identity is invalid")
	}
	return b.verifyFiles(ctx, id.String())
}

func (b *backupSession) verifyFiles(ctx context.Context, storeID string) error {
	if b == nil || b.root == "" {
		return errors.New("database backup session is unavailable")
	}
	if err := validateBackupManifest(b.manifest); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rootInfo, err := os.Lstat(b.root)
	if err != nil {
		return fmt.Errorf("inspect database backup root: %w", err)
	}
	if err := fileutil.ValidatePrivateDirectory(b.root, rootInfo); err != nil {
		return fmt.Errorf("validate database backup root: %w", err)
	}
	expected, err := marshalBackupManifest(b.manifest)
	if err != nil {
		return err
	}
	actual, err := readPrivateBackupFile(
		filepath.Join(b.root, backupManifestName), backupMaxManifestSize,
	)
	if err != nil {
		return fmt.Errorf("read database backup manifest: %w", err)
	}
	if !bytes.Equal(actual, expected) {
		return errors.New("database backup manifest changed after snapshot")
	}
	digest := sha256.Sum256(actual)
	expectedHash := append([]byte(hex.EncodeToString(digest[:])), '\n')
	actualHash, err := readPrivateBackupFile(
		filepath.Join(b.root, backupManifestHash), sha256.Size*2+2,
	)
	if err != nil {
		return fmt.Errorf("read database backup manifest hash: %w", err)
	}
	if !bytes.Equal(actualHash, expectedHash) {
		return errors.New("database backup manifest hash is invalid")
	}

	if len(b.manifest.Files) > backupMaxFiles {
		return errors.New("database backup manifest file count limit exceeded")
	}
	seen := make(map[string]struct{}, len(b.manifest.Files))
	var totalSize int64
	for _, record := range b.manifest.Files {
		if storeID != "" && record.StoreID != storeID {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !validBackupManifestRelative(record.Backup) ||
			!validBackupAbsolutePath(record.Source) {
			return errors.New("database backup manifest path is invalid")
		}
		relative := filepath.Clean(filepath.FromSlash(record.Backup))
		relativeKey := backupPathKey(relative)
		if _, duplicate := seen[relativeKey]; duplicate {
			return errors.New("database backup manifest path is duplicated")
		}
		seen[relativeKey] = struct{}{}
		path := filepath.Join(b.root, relative)
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return fmt.Errorf("inspect database backup file: %w", statErr)
		}
		if record.SourceMode&^uint32(0o777) != 0 ||
			record.Size < 0 || record.Size > backupMaxFileBytes ||
			record.Size > backupMaxTotalBytes-totalSize {
			return errors.New("database backup manifest size limit exceeded")
		}
		totalSize += record.Size
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
			info.Size() != record.Size || uint32(info.Mode().Perm()) != record.Mode {
			return errors.New("database backup file metadata is invalid")
		}
		if privateErr := fileutil.ValidatePrivateFile(path, info); privateErr != nil {
			return fmt.Errorf("validate database backup file: %w", privateErr)
		}
		fileDigest, size, hashErr := hashBackupFile(ctx, path, info, record.Size)
		if hashErr != nil {
			return hashErr
		}
		if size != record.Size || fileDigest != record.SHA256 {
			return errors.New("database backup file hash is invalid")
		}
	}
	return nil
}

// verifyLiveSources proves the claimed live inputs still describe the exact
// generation and legacy bytes recorded by snapshot. It is intentionally
// read-only and must run immediately before a staged cutover.
func (b *backupSession) verifyLiveSources(ctx context.Context, spec storecatalog.Spec) error {
	if b == nil || b.root == "" || !spec.ID.Valid() || !validBackupAbsolutePath(spec.Path) {
		return errors.New("database live-source verification input is invalid")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	excluded, err := b.catalogGenerationExclusions()
	if err != nil {
		return err
	}
	excludedKeys := make(map[string]struct{}, len(excluded))
	for key := range excluded {
		excludedKeys[key] = struct{}{}
	}
	for _, path := range generationPaths(spec.Path) {
		if _, present := excluded[backupPathKey(path)]; !present {
			return errors.New("database backup omits a catalog generation exclusion")
		}
	}

	identityOwners := make(map[fileidentity.Identity]string, len(excluded))
	for _, path := range sortedBackupPaths(excluded) {
		info, statErr := os.Lstat(path)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return fmt.Errorf("inspect catalog generation exclusion: %w", statErr)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("catalog generation exclusion is unsafe")
		}
		identity, exists, identityErr := fileidentity.Existing(path)
		if identityErr != nil || !exists {
			return errors.Join(errors.New("catalog generation identity is unavailable"), identityErr)
		}
		if err := rememberLiveIdentity(identityOwners, identity, path); err != nil {
			return err
		}
	}

	storeID := spec.ID.String()
	storeFound := false
	storeExists := false
	for _, store := range b.manifest.Stores {
		if store.StoreID != storeID {
			continue
		}
		if storeFound {
			return errors.New("database backup store manifest is duplicated")
		}
		storeFound = true
		storeExists = store.Exists
	}
	if !storeFound {
		return errors.New("database backup store manifest is missing")
	}

	generationRecords := make(map[string]BackupFileManifest, 4)
	legacyRecords := make(map[string]BackupFileManifest)
	for _, record := range b.manifest.Files {
		if record.StoreID != storeID {
			continue
		}
		switch record.Role {
		case "database", "wal", "shm", "journal":
			if _, duplicate := generationRecords[record.Role]; duplicate {
				return errors.New("database backup generation role is duplicated")
			}
			generationRecords[record.Role] = record
		case "legacy":
			key := backupPathKey(record.Source)
			if _, duplicate := legacyRecords[key]; duplicate {
				return errors.New("database backup legacy source is duplicated")
			}
			legacyRecords[key] = record
		default:
			return errors.New("database backup source role is invalid")
		}
	}
	_, mainRecorded := generationRecords["database"]
	if mainRecorded != storeExists {
		return errors.New("database backup generation manifest is inconsistent")
	}
	if !mainRecorded && len(generationRecords) != 0 {
		return errors.New("database backup has a sidecar without its database")
	}
	if _, wal := generationRecords["wal"]; wal {
		if _, journal := generationRecords["journal"]; journal {
			return errors.New("database generation has WAL and rollback journal sidecars")
		}
	}

	roles := []string{"database", "wal", "shm", "journal"}
	paths := generationPaths(spec.Path)
	for index, role := range roles {
		path := paths[index]
		record, recorded := generationRecords[role]
		info, statErr := os.Lstat(path)
		actual := statErr == nil
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("inspect live database %s: %w", role, statErr)
		}
		if actual != recorded {
			return fmt.Errorf("live database %s presence changed after snapshot", role)
		}
		if !actual {
			continue
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || record.Source != path {
			return fmt.Errorf("live database %s metadata changed after snapshot", role)
		}
		identity, verifyErr := verifyLiveSourceRecord(ctx, path, record)
		if verifyErr != nil {
			return fmt.Errorf("verify live database %s: %w", role, verifyErr)
		}
		if err := rememberLiveIdentity(identityOwners, identity, path); err != nil {
			return err
		}
	}

	legacyRoots := append([]string(nil), spec.LegacyRoots...)
	if len(legacyRoots) > backupMaxEntries {
		return errors.New("legacy input root count limit exceeded")
	}
	for _, root := range legacyRoots {
		if !validBackupAbsolutePath(root) {
			return errors.New("legacy input root is invalid")
		}
	}
	sort.Slice(legacyRoots, func(left, right int) bool {
		leftKey, rightKey := backupPathKey(legacyRoots[left]), backupPathKey(legacyRoots[right])
		if leftKey != rightKey {
			return leftKey < rightKey
		}
		return legacyRoots[left] < legacyRoots[right]
	})
	legacyRoots = deduplicateLiveLegacyRoots(legacyRoots)
	for key, record := range legacyRecords {
		if !validBackupAbsolutePath(record.Source) || key != backupPathKey(record.Source) {
			return errors.New("database backup legacy source path is invalid")
		}
		if _, generation := excluded[key]; generation {
			return errors.New("database backup legacy source overlaps a catalog generation")
		}
		inside := false
		for _, root := range legacyRoots {
			_, match, relativeErr := legacySourceRelative(root, record.Source)
			if relativeErr != nil {
				return relativeErr
			}
			inside = inside || match
		}
		if !inside {
			return errors.New("database backup legacy source is outside its catalog roots")
		}
	}

	actualLegacy := make(map[string]string, len(legacyRecords))
	budget := newBackupBudget()
	for _, root := range legacyRoots {
		walkErr := walkLegacyInputs(
			ctx,
			root,
			b.root,
			excludedKeys,
			budget,
			func(path string) error {
				key := backupPathKey(path)
				if _, duplicate := actualLegacy[key]; duplicate {
					return nil
				}
				if len(actualLegacy) >= backupMaxFiles {
					return errors.New("live legacy input file count limit exceeded")
				}
				actualLegacy[key] = filepath.Clean(path)
				return nil
			},
		)
		if walkErr != nil {
			return fmt.Errorf("enumerate live legacy inputs: %w", walkErr)
		}
	}

	seenLegacy := make(map[string]struct{}, len(actualLegacy))
	sortedLegacy := sortedBackupPaths(actualLegacy)
	for _, path := range sortedLegacy {
		identity, exists, identityErr := fileidentity.Existing(path)
		if identityErr != nil || !exists {
			return errors.Join(
				errors.New("live legacy input identity is unavailable"), identityErr,
			)
		}
		if err := rememberLiveIdentity(identityOwners, identity, path); err != nil {
			return err
		}
	}
	for _, path := range sortedLegacy {
		key := backupPathKey(path)
		record, expected := legacyRecords[key]
		if !expected {
			return errors.New("live legacy input was added after snapshot")
		}
		if record.Source != path {
			return errors.New("live legacy input path changed after snapshot")
		}
		identity, verifyErr := verifyLiveSourceRecord(ctx, path, record)
		if verifyErr != nil {
			return fmt.Errorf("verify live legacy input: %w", verifyErr)
		}
		if err := rememberLiveIdentity(identityOwners, identity, path); err != nil {
			return err
		}
		seenLegacy[key] = struct{}{}
	}
	if len(seenLegacy) != len(legacyRecords) {
		return errors.New("live legacy input was removed after snapshot")
	}
	return nil
}

func (b *backupSession) catalogGenerationExclusions() (map[string]string, error) {
	if len(b.manifest.CatalogGenerations) == 0 ||
		len(b.manifest.CatalogGenerations) > backupMaxEntries {
		return nil, errors.New("database backup catalog generation exclusions are invalid")
	}
	result := make(map[string]string, len(b.manifest.CatalogGenerations))
	for _, path := range b.manifest.CatalogGenerations {
		if !validBackupAbsolutePath(path) {
			return nil, errors.New("database backup catalog generation exclusion is invalid")
		}
		key := backupPathKey(path)
		if _, duplicate := result[key]; duplicate {
			return nil, errors.New("database backup catalog generation exclusion is duplicated")
		}
		result[key] = path
	}
	return result, nil
}

func sortedBackupPaths(paths map[string]string) []string {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		result = append(result, path)
	}
	sort.Slice(result, func(left, right int) bool {
		leftKey, rightKey := backupPathKey(result[left]), backupPathKey(result[right])
		if leftKey != rightKey {
			return leftKey < rightKey
		}
		return result[left] < result[right]
	})
	return result
}

func deduplicateLiveLegacyRoots(sortedRoots []string) []string {
	result := make([]string, 0, len(sortedRoots))
	for _, candidate := range sortedRoots {
		covered := false
		for _, root := range result {
			relative, inside, err := legacySourceRelative(root, candidate)
			if err != nil || !inside {
				continue
			}
			if relative == "." {
				covered = true
				break
			}
			first := relative
			if separator := strings.IndexRune(first, os.PathSeparator); separator >= 0 {
				first = first[:separator]
			}
			if !skipLegacyTopLevelDirectory(first) {
				covered = true
				break
			}
		}
		if !covered {
			result = append(result, candidate)
		}
	}
	return result
}

func rememberLiveIdentity(
	owners map[fileidentity.Identity]string,
	identity fileidentity.Identity,
	path string,
) error {
	if !identity.Valid() {
		return errors.New("live source physical identity is invalid")
	}
	if previous, duplicate := owners[identity]; duplicate &&
		backupPathKey(previous) != backupPathKey(path) {
		return errors.New("live database sources contain a physical alias")
	}
	owners[identity] = path
	return nil
}

func verifyLiveSourceRecord(
	ctx context.Context,
	path string,
	record BackupFileManifest,
) (fileidentity.Identity, error) {
	if record.SourceIdentity == "" || len(record.SourceIdentity) > backupMaxPathBytes ||
		!utf8.ValidString(record.SourceIdentity) || strings.ContainsRune(record.SourceIdentity, 0) ||
		record.Size < 0 || record.Size > backupMaxFileBytes ||
		record.SourceMode&^uint32(0o777) != 0 {
		return fileidentity.Identity{}, errors.New("live source manifest metadata is invalid")
	}
	decodedDigest, err := hex.DecodeString(record.SHA256)
	if err != nil || len(decodedDigest) != sha256.Size || hex.EncodeToString(decodedDigest) != record.SHA256 {
		return fileidentity.Identity{}, errors.New("live source manifest hash is invalid")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return fileidentity.Identity{}, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 ||
		before.Size() != record.Size || uint32(before.Mode().Perm()) != record.SourceMode {
		return fileidentity.Identity{}, errors.New("live source metadata changed after snapshot")
	}
	identity, exists, err := fileidentity.Existing(path)
	if err != nil || !exists || identity.String() != record.SourceIdentity {
		return fileidentity.Identity{}, errors.Join(
			errors.New("live source identity changed after snapshot"), err,
		)
	}
	file, err := os.Open(path)
	if err != nil {
		return fileidentity.Identity{}, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() ||
		opened.Mode()&os.ModeSymlink != 0 {
		_ = file.Close()
		return fileidentity.Identity{}, errors.Join(
			errors.New("live source changed while opening"), err,
		)
	}
	digest := sha256.New()
	size, hashErr := copyWithContext(ctx, io.Discard, digest, file, record.Size)
	closeErr := file.Close()
	if hashErr != nil || closeErr != nil {
		return fileidentity.Identity{}, errors.Join(hashErr, closeErr)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() ||
		before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		return fileidentity.Identity{}, errors.Join(
			errors.New("live source changed during verification"), err,
		)
	}
	afterIdentity, afterExists, err := fileidentity.Existing(path)
	if err != nil || !afterExists || afterIdentity != identity {
		return fileidentity.Identity{}, errors.Join(
			errors.New("live source identity changed during verification"), err,
		)
	}
	if size != record.Size || hex.EncodeToString(digest.Sum(nil)) != record.SHA256 {
		return fileidentity.Identity{}, errors.New("live source content changed after snapshot")
	}
	return identity, nil
}

func marshalBackupManifest(manifest BackupManifest) ([]byte, error) {
	return marshalBackupManifestLimit(manifest, backupMaxManifestSize)
}

func marshalBackupManifestLimit(manifest BackupManifest, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("database backup manifest size limit is invalid")
	}
	if err := validateBackupManifest(manifest); err != nil {
		return nil, err
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	payload = append(payload, '\n')
	if int64(len(payload)) > limit {
		return nil, errors.New("database backup manifest size limit exceeded")
	}
	return payload, nil
}

func hashBackupFile(
	ctx context.Context,
	path string,
	expected os.FileInfo,
	maxBytes int64,
) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || expected == nil || !os.SameFile(expected, opened) ||
		!opened.Mode().IsRegular() || opened.Mode()&os.ModeSymlink != 0 {
		return "", 0, errors.Join(errors.New("database backup file changed while opening"), err)
	}
	if err := fileutil.ValidatePrivateFile(path, opened); err != nil {
		return "", 0, fmt.Errorf("revalidate database backup file: %w", err)
	}
	digest := sha256.New()
	size, err := copyWithContext(ctx, io.Discard, digest, file, maxBytes)
	if err != nil {
		return "", size, err
	}
	return hex.EncodeToString(digest.Sum(nil)), size, nil
}

func ensurePrivateBackupDirectory(path string) error {
	if err := validateBackupAncestors(path); err != nil {
		return err
	}
	if err := sqliteprovider.EnsurePrivateDirectory(path); err != nil {
		return fmt.Errorf("create database backup directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("database backup directory is unsafe")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	resolvedAbsolute, err := filepath.Abs(resolved)
	if err != nil || filepath.Clean(absolute) != filepath.Clean(resolvedAbsolute) {
		return errors.New("database backup directory contains a symlink")
	}
	if err := secureAndValidateBackupDirectory(path); err != nil {
		return err
	}
	if err := validateBackupAncestors(path); err != nil {
		return err
	}
	return fileutil.SyncDirectory(path)
}

func secureAndValidateBackupDirectory(path string) error {
	secured, err := fileutil.SecurePrivateDirectory(path)
	if err != nil {
		return err
	}
	return fileutil.ValidatePrivateDirectory(path, secured)
}

func secureAndValidateBackupFile(path string) error {
	secured, err := fileutil.SecurePrivateFile(path)
	if err != nil {
		return err
	}
	return fileutil.ValidatePrivateFile(path, secured)
}

func readPrivateBackupFile(path string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, errors.New("database backup read limit is invalid")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxBytes {
		return nil, errors.New("database backup control file is unsafe")
	}
	if err := fileutil.ValidatePrivateFile(path, info); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() ||
		opened.Mode()&os.ModeSymlink != 0 || opened.Size() > maxBytes {
		return nil, errors.Join(errors.New("database backup control file changed while opening"), err)
	}
	if err := fileutil.ValidatePrivateFile(path, opened); err != nil {
		return nil, fmt.Errorf("revalidate database backup control file: %w", err)
	}
	payload, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > maxBytes {
		return nil, errors.New("database backup control file size limit exceeded")
	}
	return payload, nil
}

func validateBackupAncestors(path string) error {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	current := absolute
	for {
		info, statErr := os.Lstat(current)
		if statErr == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("database backup path contains an unsafe ancestor")
			}
			if _, exists, identityErr := fileidentity.Existing(current); identityErr != nil || !exists {
				return errors.Join(
					errors.New("database backup ancestor physical identity is unsafe"), identityErr,
				)
			}
			resolved, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil {
				return resolveErr
			}
			resolved, resolveErr = filepath.Abs(resolved)
			if resolveErr != nil || filepath.Clean(resolved) != filepath.Clean(current) {
				return errors.New("database backup path contains a symlinked ancestor")
			}
			return nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return statErr
		}
		current = parent
	}
}

func generationPaths(path string) []string {
	return []string{path, path + "-wal", path + "-shm", path + "-journal"}
}

func walkLegacyInputs(
	ctx context.Context,
	root string,
	backupRoot string,
	excluded map[string]struct{},
	budget *backupBudget,
	visit func(string) error,
) error {
	if !validBackupAbsolutePath(root) {
		return errors.New("legacy input path is invalid")
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("legacy input is a symlink")
	}
	if info.Mode().IsRegular() {
		if err := budget.enter("."); err != nil {
			return err
		}
		if _, skip := excluded[backupPathKey(root)]; skip {
			return nil
		}
		return visit(root)
	}
	if !info.IsDir() {
		return errors.New("legacy input is not a regular file or directory")
	}
	return walkLegacyDirectory(
		ctx, filepath.Clean(root), ".", filepath.Clean(backupRoot),
		excluded, budget, visit,
	)
}

func walkLegacyDirectory(
	ctx context.Context,
	path,
	relative,
	backupRoot string,
	excluded map[string]struct{},
	budget *backupBudget,
	visit func(string) error,
) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := budget.enter(relative); err != nil {
		return err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return errors.New("legacy input tree directory changed before traversal")
	}
	beforeIdentity, exists, identityErr := fileidentity.Existing(path)
	if identityErr != nil || !exists {
		return errors.Join(errors.New("legacy input directory identity is unsafe"), identityErr)
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, directory.Close()) }()
	opened, err := directory.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.IsDir() {
		return errors.Join(errors.New("legacy input tree directory changed while opening"), err)
	}
	for {
		entries, readErr := readBackupDirectoryBatch(directory)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !validBackupPathComponent(entry.Name()) {
				return errors.New("legacy input tree contains an invalid path component")
			}
			child := filepath.Join(path, entry.Name())
			childRelative := entry.Name()
			if relative != "." {
				childRelative = filepath.Join(relative, entry.Name())
			}
			clean := filepath.Clean(child)
			if clean == backupRoot || strings.HasPrefix(clean, backupRoot+string(os.PathSeparator)) {
				if err := budget.enter(childRelative); err != nil {
					return err
				}
				continue
			}
			info, statErr := os.Lstat(child)
			if statErr != nil {
				return statErr
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("legacy input tree contains a symlink")
			}
			if info.IsDir() {
				if relative == "." && skipLegacyTopLevelDirectory(entry.Name()) {
					if err := budget.enter(childRelative); err != nil {
						return err
					}
					continue
				}
				if err := walkLegacyDirectory(
					ctx, child, childRelative, backupRoot, excluded, budget, visit,
				); err != nil {
					return err
				}
				continue
			}
			if err := budget.enter(childRelative); err != nil {
				return err
			}
			if _, skip := excluded[backupPathKey(clean)]; skip {
				continue
			}
			if !info.Mode().IsRegular() {
				return errors.New("legacy input tree contains a non-regular file")
			}
			if err := visit(child); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			after, statErr := os.Lstat(path)
			if statErr != nil || after == nil || !after.IsDir() ||
				after.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, after) {
				return errors.Join(
					errors.New("legacy input tree directory changed during traversal"), statErr,
				)
			}
			afterIdentity, exists, identityErr := fileidentity.Existing(path)
			if identityErr != nil || !exists || afterIdentity != beforeIdentity ||
				!opened.ModTime().Equal(after.ModTime()) {
				return errors.Join(
					errors.New("legacy input tree directory changed during traversal"), identityErr,
				)
			}
			return nil
		}
	}
}

func skipLegacyTopLevelDirectory(name string) bool {
	return name == "legacy-json" || name == "backups" || name == database.StateDirectoryName
}

type backupDirectoryReader interface {
	ReadDir(int) ([]os.DirEntry, error)
}

func readBackupDirectoryBatch(directory backupDirectoryReader) ([]os.DirEntry, error) {
	if directory == nil {
		return nil, errors.New("legacy input directory reader is unavailable")
	}
	entries, err := directory.ReadDir(128)
	if len(entries) == 0 && err == nil {
		return nil, errors.New("legacy input directory read made no progress")
	}
	return entries, err
}

func validBackupPathComponent(component string) bool {
	return component != "" && component != "." && component != ".." &&
		len(component) <= backupMaxComponent && utf8.ValidString(component) &&
		!strings.ContainsRune(component, 0) &&
		!strings.ContainsRune(component, os.PathSeparator)
}

func backupPathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

// backupCopyOps keeps durability fault injection local to one private call.
// Production uses the exact os/fileutil operations returned below.
type backupCopyOps struct {
	lstat      func(string) (os.FileInfo, error)
	openInput  func(string) (*os.File, error)
	stat       func(*os.File) (os.FileInfo, error)
	openOutput func(string, int, os.FileMode) (*os.File, error)
	copy       func(context.Context, io.Writer, hash.Hash, io.Reader, int64) (int64, error)
	chmod      func(*os.File, os.FileMode) error
	sync       func(*os.File) error
	close      func(*os.File) error
	secure     func(string) error
	syncDir    func(string) error
	rel        func(string, string) (string, error)
}

func defaultBackupCopyOps() backupCopyOps {
	return backupCopyOps{
		lstat: os.Lstat,
		openInput: func(path string) (*os.File, error) {
			return os.Open(path)
		},
		stat:       func(file *os.File) (os.FileInfo, error) { return file.Stat() },
		openOutput: os.OpenFile,
		copy:       copyWithContext,
		chmod:      func(file *os.File, mode os.FileMode) error { return file.Chmod(mode) },
		sync:       func(file *os.File) error { return file.Sync() },
		close:      func(file *os.File) error { return file.Close() },
		secure:     secureAndValidateBackupFile,
		syncDir:    fileutil.SyncDirectory,
		rel:        filepath.Rel,
	}
}

func copyBackupFile(
	ctx context.Context,
	backupRoot, storeID, role, source, relativeDestination string,
	budget *backupBudget,
) (BackupFileManifest, error) {
	return copyBackupFileWithOps(
		ctx, backupRoot, storeID, role, source, relativeDestination, budget,
		defaultBackupCopyOps(),
	)
}

func copyBackupFileWithOps(
	ctx context.Context,
	backupRoot, storeID, role, source, relativeDestination string,
	budget *backupBudget,
	ops backupCopyOps,
) (BackupFileManifest, error) {
	var empty BackupFileManifest
	relativeDestination = filepath.Clean(relativeDestination)
	if !safeBackupRelative(relativeDestination) {
		return empty, errors.New("database backup destination is invalid")
	}
	before, err := ops.lstat(source)
	if err != nil {
		return empty, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return empty, errors.New("source is not a regular file")
	}
	if err := budget.reserveFile(before.Size()); err != nil {
		return empty, err
	}
	input, err := ops.openInput(source)
	if err != nil {
		return empty, err
	}
	defer ops.close(input)
	opened, err := ops.stat(input)
	if err != nil || !os.SameFile(before, opened) {
		return empty, errors.New("source changed while opening")
	}
	destination := filepath.Join(backupRoot, relativeDestination)
	if err := ensurePrivateBackupDirectory(filepath.Dir(destination)); err != nil {
		return empty, err
	}
	output, err := ops.openOutput(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return empty, err
	}
	keep := false
	defer func() {
		_ = ops.close(output)
		if !keep {
			_ = os.Remove(destination)
		}
	}()
	digest := sha256.New()
	written, err := ops.copy(ctx, output, digest, input, before.Size())
	if err != nil {
		return empty, err
	}
	after, err := ops.lstat(source)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() ||
		before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) || written != before.Size() {
		return empty, errors.New("source changed while copying")
	}
	if err := ops.chmod(output, 0o600); err != nil {
		return empty, err
	}
	if err := ops.sync(output); err != nil {
		return empty, err
	}
	if err := ops.close(output); err != nil {
		return empty, err
	}
	if err := ops.secure(destination); err != nil {
		return empty, err
	}
	if err := ops.syncDir(filepath.Dir(destination)); err != nil {
		return empty, err
	}
	keep = true
	backupRelative, err := ops.rel(backupRoot, destination)
	if err != nil {
		return empty, err
	}
	return BackupFileManifest{
		StoreID:    storeID,
		Role:       role,
		Source:     source,
		Backup:     filepath.ToSlash(backupRelative),
		SHA256:     hex.EncodeToString(digest.Sum(nil)),
		Size:       written,
		Mode:       0o600,
		SourceMode: uint32(before.Mode().Perm()),
	}, nil
}

func copyWithContext(
	ctx context.Context,
	destination io.Writer,
	digest hash.Hash,
	source io.Reader,
	maxBytes int64,
) (int64, error) {
	if maxBytes < 0 {
		return 0, errors.New("database backup copy limit is invalid")
	}
	buffer := make([]byte, 1<<20)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			if int64(count) > maxBytes-written {
				return written, errors.New("database backup source exceeded its size limit")
			}
			chunk := buffer[:count]
			outputCount, writeErr := destination.Write(chunk)
			if writeErr != nil {
				return written, writeErr
			}
			if outputCount != count {
				return written, io.ErrShortWrite
			}
			if _, writeErr := digest.Write(chunk); writeErr != nil {
				return written, writeErr
			}
			written += int64(count)
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
		if count == 0 {
			return written, errors.New("database backup source read made no progress")
		}
	}
}
