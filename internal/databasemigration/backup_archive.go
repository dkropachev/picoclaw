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
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

// loadBackupSession reconstructs one crash-recovery capability only from a
// canonical committed archive. Partial, malformed, or non-canonical evidence
// never produces a session.
func loadBackupSession(ctx context.Context, root string) (*backupSession, error) {
	if !validBackupAbsolutePath(root) || backupPathHasSuffix(root, backupPartialSuffix) {
		return nil, errors.New("database backup recovery root is invalid")
	}
	parent := filepath.Dir(root)
	parentIdentity, err := pinPrivateBackupDirectory(parent)
	if err != nil {
		return nil, fmt.Errorf("pin database backup recovery parent: %w", err)
	}
	manifestPath := filepath.Join(root, backupManifestName)
	payload, parsedIdentity, exists, err := readPinnedPrivateBackupFileWithOps(
		manifestPath, backupMaxManifestSize, false, defaultBackupReadOps(),
	)
	if err != nil || !exists || !parsedIdentity.Valid() {
		return nil, errors.Join(errors.New("read database backup recovery manifest"), err)
	}
	if err := preflightBackupManifestJSON(payload); err != nil {
		return nil, fmt.Errorf("preflight database backup recovery manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var manifest BackupManifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode database backup recovery manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("database backup recovery manifest has trailing data")
	}
	canonical, err := marshalBackupManifest(manifest)
	if err != nil || !bytes.Equal(canonical, payload) {
		return nil, errors.Join(errors.New("database backup recovery manifest is not canonical"), err)
	}
	identity, objectType, exists, err := fileidentity.ExistingWithType(root)
	if err != nil || !exists || objectType != fileidentity.ObjectTypeDirectory {
		return nil, errors.Join(errors.New("database backup recovery identity is unavailable"), err)
	}
	if err := validatePinnedPrivateBackupDirectory(parent, parentIdentity); err != nil {
		return nil, err
	}
	session := &backupSession{
		root: root, identity: identity, parent: parent,
		parentIdentity: parentIdentity, manifest: manifest,
	}
	verifyOps := defaultBackupVerifyOps()
	readControl := verifyOps.read
	useParsedManifest := true
	verifyOps.read = func(path string, limit int64) ([]byte, fileidentity.Identity, error) {
		if useParsedManifest && path == manifestPath {
			useParsedManifest = false
			return payload, parsedIdentity, nil
		}
		return readControl(path, limit)
	}
	if err := session.verifyFilesWithOps(ctx, verifyOps); err != nil {
		return nil, err
	}
	if useParsedManifest {
		return nil, errors.New("database backup recovery manifest was not verified")
	}
	if _, _, err := session.readMigrationStatus(); err != nil {
		return nil, fmt.Errorf("read database backup recovery status: %w", err)
	}
	return session, nil
}

type backupSnapshotOps struct {
	lstat     func(string) (os.FileInfo, error)
	mkdir     func(string, os.FileMode) error
	rename    func(string, string) error
	removeAll func(string, fileidentity.Identity) error
	secureDir func(string) error
	syncDir   func(string) error
	commit    func(*backupSession) error
	identity  func(string) (fileidentity.Identity, bool, error)
	copyFile  func(
		context.Context, string, string, string, string, string, *backupBudget,
	) (BackupFileManifest, error)
	walkLegacy func(
		context.Context, string, string, map[string]struct{},
		map[fileidentity.Identity]struct{}, *backupBudget, func(string) error,
	) error
}

func defaultBackupSnapshotOps() backupSnapshotOps {
	return backupSnapshotOps{
		lstat: os.Lstat, mkdir: createPinnedBackupDirectory,
		rename: publishBackupDirectory, removeAll: removePinnedBackupTreeIdentity,
		secureDir: secureAndValidateBackupDirectory, syncDir: fileutil.SyncDirectory,
		commit: commitBackupSnapshot, identity: fileidentity.Existing,
		copyFile: copyBackupFile, walkLegacy: walkLegacyInputsWithPhysicalExclusions,
	}
}

// snapshotBackup copies raw SQLite generation members. Caller must already
// hold every participating PicoClaw claim, prevent new provider opens, close
// existing handles, and durably flush/checkpoint the selected stores. This
// layer revalidates bytes and identities but cannot enforce quiescence against
// non-participating same-user processes.
func snapshotBackup(
	ctx context.Context,
	now func() time.Time,
	home string,
	all []storecatalog.Spec,
	specs []storecatalog.Spec,
	configuredParent string,
) (*backupSession, error) {
	return snapshotBackupWithOps(
		ctx, now, home, all, specs, configuredParent, defaultBackupSnapshotOps(),
	)
}

func snapshotBackupWithOps(
	ctx context.Context,
	now func() time.Time,
	home string,
	all []storecatalog.Spec,
	specs []storecatalog.Spec,
	configuredParent string,
	ops backupSnapshotOps,
) (*backupSession, error) {
	if now == nil {
		return nil, errors.New("database backup clock is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	selected, err := validateBackupCatalogInputs(all, specs)
	if err != nil {
		return nil, err
	}
	parent, err := validateBackupParentWithContext(ctx, configuredParent, home, all)
	if err != nil {
		return nil, err
	}
	if !validBackupAbsolutePath(parent) {
		return nil, errors.New("database backup directory is invalid")
	}
	customParent := configuredBackupParentIsCustom(configuredParent, parent, home)
	if customParent {
		info, statErr := ops.lstat(parent)
		if statErr != nil || info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.Join(
				errors.New("custom database backup directory must already exist"), statErr,
			)
		}
		if err := fileutil.ValidatePrivateDirectory(parent, info); err != nil {
			return nil, fmt.Errorf("validate custom database backup directory: %w", err)
		}
	}
	parentWasMissing := false
	if _, statErr := os.Lstat(parent); errors.Is(statErr, os.ErrNotExist) {
		parentWasMissing = true
	} else if statErr != nil {
		return nil, fmt.Errorf("inspect database backup parent before creation: %w", statErr)
	}
	createdParentIdentity, err := exclusivelyCreateMissingBackupParent(parent, parentWasMissing)
	if err != nil {
		return nil, fmt.Errorf("exclusively create database backup parent: %w", err)
	}
	parentCreated := createdParentIdentity.Valid()
	if parentWasMissing && !parentCreated {
		return nil, errors.New("database backup parent appeared during exclusive creation")
	}
	if err := ensurePrivateBackupDirectory(parent); err != nil {
		if !parentCreated {
			return nil, err
		}
		return nil, errors.Join(
			err, removePinnedEmptyBackupDirectoryIdentity(parent, createdParentIdentity),
		)
	}
	parentIdentity, err := pinPrivateBackupDirectory(parent)
	if err != nil {
		cause := fmt.Errorf("pin database backup parent: %w", err)
		if parentCreated {
			cause = errors.Join(
				cause, removePinnedEmptyBackupDirectoryIdentity(parent, createdParentIdentity),
			)
		}
		return nil, cause
	}
	if parentCreated && parentIdentity != createdParentIdentity {
		return nil, errors.Join(
			errors.New("database backup parent identity changed after creation"),
			removePinnedEmptyBackupDirectoryIdentity(parent, createdParentIdentity),
		)
	}
	if err := validateBackupParentPhysicalAliasesBoundContext(
		ctx, parent, parentIdentity, all,
	); err != nil {
		cause := fmt.Errorf("revalidate database backup directory containment: %w", err)
		if parentCreated {
			cause = errors.Join(
				cause, removePinnedEmptyBackupDirectoryIdentity(parent, createdParentIdentity),
			)
		}
		return nil, cause
	}
	if err := validatePinnedPrivateBackupDirectory(parent, parentIdentity); err != nil {
		cause := fmt.Errorf("revalidate database backup parent after containment scan: %w", err)
		if parentCreated {
			cause = errors.Join(
				cause, removePinnedEmptyBackupDirectoryIdentity(parent, createdParentIdentity),
			)
		}
		return nil, cause
	}
	timestamp := now().UTC()
	if !validBackupManifestTime(timestamp) {
		return nil, errors.New("database backup timestamp is invalid")
	}
	name := "database-migrate-" + timestamp.Format("20060102T150405.000000000Z")
	finalRoot := filepath.Join(parent, name)
	stageRoot := finalRoot + backupPartialSuffix
	if !validBackupAbsolutePath(finalRoot) || !validBackupAbsolutePath(stageRoot) {
		return nil, errors.New("database migration backup target path is invalid")
	}
	statusPath := finalRoot + backupStatusSuffix
	if !validBackupAbsolutePath(statusPath) || filepath.Dir(statusPath) != parent {
		return nil, errors.New("database migration backup status path is invalid")
	}
	for _, candidate := range []string{finalRoot, stageRoot, statusPath} {
		if _, statErr := ops.lstat(candidate); statErr == nil {
			return nil, errors.New("database migration backup already exists")
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect database migration backup target: %w", statErr)
		}
	}
	if err := validatePinnedPrivateBackupDirectory(parent, parentIdentity); err != nil {
		return nil, err
	}
	if err := ops.mkdir(stageRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create database migration backup: %w", err)
	}
	if err := validatePinnedPrivateBackupDirectory(parent, parentIdentity); err != nil {
		return nil, errors.Join(
			errors.New("database backup parent changed during stage creation"), err,
		)
	}
	stageIdentity, stageExists, stageIdentityErr := ops.identity(stageRoot)
	if stageIdentityErr != nil || !stageExists {
		syncErr := ops.syncDir(parent)
		return nil, errors.Join(
			errors.New("database migration backup stage identity is unavailable"),
			stageIdentityErr, syncErr,
		)
	}
	session := &backupSession{
		root: stageRoot, finalRoot: finalRoot, identity: stageIdentity,
		parent: parent, parentIdentity: parentIdentity,
		manifest: BackupManifest{
			Version: backupManifestVersion, CreatedAt: timestamp,
			CaptureMode:        "offline_quiescent_raw",
			Stores:             make([]BackupStoreManifest, 0, len(selected)),
			Files:              make([]BackupFileManifest, 0),
			CatalogGenerations: make([]string, 0, len(all)*4),
		},
	}
	fail := func(cause error) (*backupSession, error) {
		if parentErr := validatePinnedPrivateBackupDirectory(parent, parentIdentity); parentErr != nil {
			return nil, errors.Join(cause, parentErr)
		}
		currentIdentity, exists, identityErr := ops.identity(stageRoot)
		var removeErr error
		if identityErr != nil || !exists || currentIdentity != stageIdentity {
			removeErr = errors.Join(errors.New("database backup stage identity changed"), identityErr)
		} else {
			removeErr = ops.removeAll(stageRoot, stageIdentity)
		}
		return nil, errors.Join(cause, removeErr)
	}
	budget := newBackupBudget()
	physicalExclusions := map[fileidentity.Identity]struct{}{
		parentIdentity: {},
		stageIdentity:  {},
	}
	selectedLegacyRoots := 0
	for _, spec := range selected {
		selectedLegacyRoots += len(spec.LegacyRoots)
	}
	if err := budget.reservePreparedLegacyRoots(selectedLegacyRoots); err != nil {
		return fail(err)
	}
	if err := ops.secureDir(stageRoot); err != nil {
		return fail(fmt.Errorf("secure database migration backup: %w", err))
	}
	securedStageIdentity, securedStageExists, securedStageErr := ops.identity(stageRoot)
	if securedStageErr != nil || !securedStageExists || securedStageIdentity != stageIdentity {
		return fail(errors.Join(
			errors.New("database migration backup stage changed while securing"), securedStageErr,
		))
	}
	if err := ops.syncDir(parent); err != nil {
		return fail(fmt.Errorf("sync database backup parent: %w", err))
	}
	for _, spec := range selected {
		store := BackupStoreManifest{
			StoreID: spec.ID.String(), Path: spec.Path,
			LegacyRoots:     append([]string{}, spec.LegacyRoots...),
			LegacyRootKinds: make([]string, 0, len(spec.LegacyRoots)),
		}
		if err := budget.reserveManifestStore(store); err != nil {
			return fail(err)
		}
		session.manifest.Stores = append(
			session.manifest.Stores,
			store,
		)
	}

	excluded := make(map[string]struct{}, len(all)*4)
	for _, known := range all {
		for _, generation := range generationPaths(known.Path) {
			key := backupPathKey(generation)
			excluded[key] = struct{}{}
			cleanGeneration := filepath.Clean(generation)
			if err := budget.reserveManifestStrings(0, cleanGeneration); err != nil {
				return fail(err)
			}
			session.manifest.CatalogGenerations = append(
				session.manifest.CatalogGenerations,
				cleanGeneration,
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
	generationIdentities := make(map[fileidentity.Identity]string, len(all)*4)
	for _, known := range all {
		for _, generation := range generationPaths(known.Path) {
			if err := ctx.Err(); err != nil {
				return fail(err)
			}
			info, statErr := ops.lstat(generation)
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				return fail(statErr)
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return fail(errors.New("database generation is unsafe"))
			}
			identity, exists, identityErr := ops.identity(generation)
			if identityErr != nil {
				return fail(identityErr)
			}
			if !exists {
				return fail(errors.New("database generation changed during identity lookup"))
			}
			after, statErr := ops.lstat(generation)
			afterIdentity, afterExists, afterIdentityErr := ops.identity(generation)
			if statErr != nil || afterIdentityErr != nil || !afterExists || afterIdentity != identity ||
				!after.Mode().IsRegular() || after.Mode()&os.ModeSymlink != 0 ||
				info.Size() != after.Size() || info.Mode() != after.Mode() ||
				!info.ModTime().Equal(after.ModTime()) {
				return fail(errors.Join(
					errors.New("database generation changed during identity lookup"),
					statErr, afterIdentityErr,
				))
			}
			if previous, alias := generationIdentities[identity]; alias &&
				!sameBackupPhysicalPath(previous, generation) {
				return fail(errors.New("database catalog generations contain a physical alias"))
			}
			generationIdentities[identity] = generation
		}
	}
	legacyIdentities := make(map[fileidentity.Identity]string)
	legacyRootIdentities := make(map[fileidentity.Identity]string)
	for storeIndex, spec := range selected {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		storeID := spec.ID.String()
		storeDirectory := backupStoreDirectory(storeID)
		mainExists := false
		for generationIndex, source := range generationPaths(spec.Path) {
			info, statErr := ops.lstat(source)
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				return fail(fmt.Errorf("inspect store %s generation: %w", spec.ID, statErr))
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return fail(fmt.Errorf("store %s generation is unsafe", spec.ID))
			}
			if generationIndex == 0 {
				mainExists = true
			} else if !mainExists {
				return fail(fmt.Errorf("store %s has a sidecar without its database", spec.ID))
			}
			role := []string{"database", "wal", "shm", "journal"}[generationIndex]
			identity, identityExists, identityErr := ops.identity(source)
			if identityErr != nil || !identityExists {
				return fail(errors.Join(
					fmt.Errorf("store %s %s identity is unavailable", spec.ID, role), identityErr,
				))
			}
			destination := filepath.Join("stores", storeDirectory, "generation", role)
			record, copyErr := ops.copyFile(
				ctx, session.root, storeID, role, source, destination, budget,
			)
			if copyErr != nil {
				return fail(fmt.Errorf("snapshot store %s %s: %w", spec.ID, role, copyErr))
			}
			afterIdentity, afterExists, identityErr := ops.identity(source)
			if identityErr != nil || !afterExists || afterIdentity != identity {
				return fail(errors.Join(
					fmt.Errorf("store %s %s identity changed during snapshot", spec.ID, role), identityErr,
				))
			}
			record.SourceIdentity = identity.String()
			generationIdentities[identity] = source
			if err := budget.reserveManifestFile(record); err != nil {
				return fail(err)
			}
			session.manifest.Files = append(session.manifest.Files, record)
			if generationIndex == 0 {
				session.manifest.Stores[storeIndex].Exists = true
			}
		}
		for legacyRootIndex, legacyRoot := range spec.LegacyRoots {
			rootInfo, rootErr := ops.lstat(legacyRoot)
			rootKind := "missing"
			if rootErr == nil {
				switch {
				case rootInfo.Mode()&os.ModeSymlink != 0:
					return fail(fmt.Errorf("store %s legacy root is a symlink", spec.ID))
				case rootInfo.Mode().IsRegular():
					rootKind = "file"
				case rootInfo.IsDir():
					rootKind = "directory"
				default:
					return fail(fmt.Errorf("store %s legacy root has an unsafe type", spec.ID))
				}
			} else if !errors.Is(rootErr, os.ErrNotExist) {
				return fail(fmt.Errorf("inspect store %s legacy root: %w", spec.ID, rootErr))
			}
			if rootKind != "missing" {
				rootIdentity, exists, identityErr := ops.identity(legacyRoot)
				if identityErr != nil || !exists {
					return fail(errors.Join(
						errors.New("legacy root identity is unavailable"), identityErr,
					))
				}
				if _, generationAlias := generationIdentities[rootIdentity]; generationAlias {
					return fail(errors.New("legacy root aliases a database generation"))
				}
				if previous, alias := legacyRootIdentities[rootIdentity]; alias &&
					!sameBackupPhysicalPath(previous, legacyRoot) {
					return fail(errors.New("legacy roots contain a physical alias"))
				}
				legacyRootIdentities[rootIdentity] = legacyRoot
			}
			session.manifest.Stores[storeIndex].LegacyRootKinds = append(
				session.manifest.Stores[storeIndex].LegacyRootKinds, rootKind,
			)
			if err := budget.reserveManifestStrings(0, rootKind); err != nil {
				return fail(err)
			}
			walkErr := ops.walkLegacy(
				ctx, legacyRoot, session.root, excluded, physicalExclusions, budget,
				func(source string) error {
					canonical := filepath.Clean(source)
					identity, exists, identityErr := ops.identity(source)
					if identityErr != nil {
						return identityErr
					}
					if !exists {
						return errors.New("legacy input disappeared during snapshot")
					}
					if _, alias := generationIdentities[identity]; alias {
						return errors.New("legacy input aliases a database generation")
					}
					if previous, alias := legacyIdentities[identity]; alias &&
						!sameBackupPhysicalPath(previous, canonical) {
						return errors.New("legacy inputs contain a physical alias")
					}
					legacyIdentities[identity] = canonical
					destination, destinationErr := legacyBackupDestination(
						storeDirectory, legacyRootIndex, legacyRoot, source,
					)
					if destinationErr != nil {
						return destinationErr
					}
					relativeSource, _, relativeErr := legacySourceRelative(legacyRoot, source)
					if relativeErr != nil {
						return relativeErr
					}
					if err := budget.reservePreparedLegacyPath(relativeSource); err != nil {
						return err
					}
					record, copyErr := ops.copyFile(
						ctx, session.root, storeID, "legacy", source, destination, budget,
					)
					if copyErr != nil {
						return copyErr
					}
					afterIdentity, afterExists, identityErr := ops.identity(source)
					if identityErr != nil || !afterExists || afterIdentity != identity {
						return errors.Join(
							errors.New("legacy input identity changed during snapshot"), identityErr,
						)
					}
					record.SourceIdentity = identity.String()
					record.LegacyRoot = legacyRootIndex
					if err := budget.reserveManifestFile(record); err != nil {
						return err
					}
					session.manifest.Files = append(session.manifest.Files, record)
					return nil
				},
			)
			if walkErr != nil {
				return fail(fmt.Errorf("snapshot store %s legacy inputs: %w", spec.ID, walkErr))
			}
		}
	}
	sortBackupManifestFiles(session.manifest.Files)
	if err := validateBackupManifest(session.manifest); err != nil {
		return fail(fmt.Errorf("validate database backup inventory: %w", err))
	}
	// Raw SQLite copying is permitted only for a caller-established quiescent
	// source. This final pass rejects any selected byte, member, or identity
	// drift that occurred while the snapshot was assembled.
	liveOps := defaultBackupLiveVerifyOps()
	liveState, err := session.newBackupLiveVerificationState(ctx, liveOps)
	if err != nil {
		return fail(fmt.Errorf("index final database backup source verification: %w", err))
	}
	for _, spec := range selected {
		if err := session.verifyLiveSourcesWithState(ctx, spec, liveOps, liveState); err != nil {
			return fail(fmt.Errorf("final database backup source verification: %w", err))
		}
	}
	if err := ops.commit(session); err != nil {
		return fail(fmt.Errorf("commit database backup inventory: %w", err))
	}
	if err := session.verifyFiles(ctx); err != nil {
		return fail(fmt.Errorf("verify staged database backup: %w", err))
	}
	if err := validatePinnedPrivateBackupDirectory(parent, parentIdentity); err != nil {
		return fail(err)
	}
	if _, err := ops.lstat(statusPath); err == nil {
		return fail(errors.New("database migration backup status path appeared before publication"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(fmt.Errorf("inspect database migration backup status path: %w", err))
	}
	if err := ops.rename(stageRoot, finalRoot); err != nil {
		return fail(fmt.Errorf("publish database migration backup: %w", err))
	}
	session.root = finalRoot
	if _, err := ops.lstat(statusPath); err == nil {
		return nil, errors.Join(
			errors.New("database migration backup status path appeared during publication"),
			ops.syncDir(parent),
		)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errors.Join(
			fmt.Errorf("reinspect database migration backup status path: %w", err),
			ops.syncDir(parent),
		)
	}
	if err := validatePinnedPrivateBackupDirectory(parent, parentIdentity); err != nil {
		return nil, fmt.Errorf("database backup parent changed during publication: %w", err)
	}
	if err := ops.syncDir(parent); err != nil {
		return nil, fmt.Errorf("sync published database backup parent: %w", err)
	}
	if err := session.verify(ctx); err != nil {
		return nil, fmt.Errorf("verify published database backup: %w", err)
	}
	return session, nil
}

func validateBackupCatalogInputs(
	all, selected []storecatalog.Spec,
) ([]storecatalog.Spec, error) {
	if len(all) == 0 || len(selected) == 0 || len(all) > backupMaxEntries/4 ||
		len(selected) > backupMaxEntries {
		return nil, errors.New("database backup catalog size is invalid")
	}
	byID := make(map[database.StoreID]storecatalog.Spec, len(all))
	generations := make(map[string]database.StoreID, len(all)*4)
	legacyRootTotal := 0
	for _, spec := range all {
		if !spec.ID.Valid() || spec.ID.String() == "" || !validBackupAbsolutePath(spec.Path) ||
			len(spec.LegacyRoots) > backupMaxLegacyRoots-legacyRootTotal {
			return nil, errors.New("database backup catalog specification is invalid")
		}
		if err := validateBackupAncestors(filepath.Dir(spec.Path)); err != nil {
			return nil, fmt.Errorf("validate database generation ancestors: %w", err)
		}
		legacyRootTotal += len(spec.LegacyRoots)
		if _, duplicate := byID[spec.ID]; duplicate {
			return nil, errors.New("database backup catalog store is duplicated")
		}
		rootKeys := make(map[string]struct{}, len(spec.LegacyRoots))
		for _, root := range spec.LegacyRoots {
			if !validBackupAbsolutePath(root) {
				return nil, errors.New("database backup legacy root is invalid")
			}
			if err := validateBackupAncestors(filepath.Dir(root)); err != nil {
				return nil, fmt.Errorf("validate database legacy-root ancestors: %w", err)
			}
			key := backupPathKey(root)
			if _, duplicate := rootKeys[key]; duplicate {
				return nil, errors.New("database backup legacy root is duplicated")
			}
			rootKeys[key] = struct{}{}
		}
		for _, path := range generationPaths(spec.Path) {
			if !validBackupAbsolutePath(path) {
				return nil, errors.New("database backup catalog generation path is invalid")
			}
			key := backupPathKey(path)
			if owner, duplicate := generations[key]; duplicate && owner != spec.ID {
				return nil, errors.New("database backup catalog generation is duplicated")
			}
			generations[key] = spec.ID
		}
		byID[spec.ID] = spec
	}
	result := make([]storecatalog.Spec, len(selected))
	seen := make(map[database.StoreID]struct{}, len(result))
	for index, spec := range selected {
		catalogSpec, present := byID[spec.ID]
		if !present || !sameBackupSpec(catalogSpec, spec) {
			return nil, errors.New("selected database backup specification differs from catalog")
		}
		if _, duplicate := seen[spec.ID]; duplicate {
			return nil, errors.New("selected database backup store is duplicated")
		}
		seen[spec.ID] = struct{}{}
		result[index] = catalogSpec
		result[index].LegacyRoots = append([]string(nil), catalogSpec.LegacyRoots...)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID.String() < result[j].ID.String() })
	return result, nil
}

func sameBackupSpec(left, right storecatalog.Spec) bool {
	if left.ID != right.ID || left.Domain != right.Domain || left.Path != right.Path ||
		left.Required != right.Required || len(left.LegacyRoots) != len(right.LegacyRoots) {
		return false
	}
	for index := range left.LegacyRoots {
		if left.LegacyRoots[index] != right.LegacyRoots[index] {
			return false
		}
	}
	return true
}

func validateBackupManifestStoreSpec(
	store BackupStoreManifest,
	spec storecatalog.Spec,
) error {
	if store.StoreID != spec.ID.String() || store.Path != spec.Path ||
		len(store.LegacyRoots) != len(spec.LegacyRoots) {
		return errors.New("database backup store provenance changed")
	}
	for index := range store.LegacyRoots {
		if store.LegacyRoots[index] != spec.LegacyRoots[index] {
			return errors.New("database backup legacy-root provenance changed")
		}
	}
	return nil
}

// verify proves durable backup bytes and manifest match in-memory snapshot.
// Migration must call it before any provider open, recovery, or callback.
func (b *backupSession) verify(ctx context.Context) error {
	return b.verifyFiles(ctx)
}

func (b *backupSession) verifyStore(ctx context.Context, id database.StoreID) error {
	if !id.Valid() {
		return errors.New("database backup store identity is invalid")
	}
	found := false
	if b != nil {
		for _, store := range b.manifest.Stores {
			found = found || store.StoreID == id.String()
		}
	}
	if !found {
		return errors.New("database backup store manifest is missing")
	}
	return b.verifyFiles(ctx)
}

type backupVerifyOps struct {
	validateManifest  func(BackupManifest) error
	lstat             func(string) (os.FileInfo, error)
	validateDirectory func(string, os.FileInfo) error
	marshal           func(BackupManifest) ([]byte, error)
	read              func(string, int64) ([]byte, fileidentity.Identity, error)
	hash              func(
		context.Context, string, os.FileInfo, fileidentity.Identity, int64,
	) (string, int64, error)
	identity   func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error)
	exactTree  func(context.Context, string, BackupManifest) (map[fileidentity.Identity]string, error)
	revalidate func(
		context.Context, string, map[string]fileidentity.Identity, map[string]os.FileInfo,
	) error
	validateRoot func(string, fileidentity.Identity) error
}

type backupLiveVerifyOps struct {
	lstat      func(string) (os.FileInfo, error)
	identity   func(string) (fileidentity.Identity, bool, error)
	walkLegacy func(
		context.Context, string, string, map[string]struct{}, *backupBudget, func(string) error,
	) error
	verifyRecord func(context.Context, string, BackupFileManifest) (fileidentity.Identity, error)
}

type backupLiveVerificationState struct {
	excludedKeys   map[string]struct{}
	identityOwners map[fileidentity.Identity]string
	stores         map[string]BackupStoreManifest
	records        map[string]backupLiveStoreRecords
}

type backupLiveStoreRecords struct {
	generation map[string]BackupFileManifest
	legacy     map[string]BackupFileManifest
}

func defaultBackupVerifyOps() backupVerifyOps {
	return backupVerifyOps{
		validateManifest:  validateBackupManifest,
		lstat:             os.Lstat,
		validateDirectory: fileutil.ValidatePrivateDirectory,
		marshal:           marshalBackupManifest,
		read: func(path string, limit int64) ([]byte, fileidentity.Identity, error) {
			payload, identity, exists, err := readPinnedPrivateBackupFileWithOps(
				path, limit, false, defaultBackupReadOps(),
			)
			if err != nil || !exists {
				return nil, fileidentity.Identity{}, errors.Join(
					errors.New("database backup verification control file is unavailable"), err,
				)
			}
			return payload, identity, nil
		},
		hash:         hashBackupFile,
		identity:     fileidentity.ExistingWithType,
		exactTree:    exactBackupTreeIdentities,
		revalidate:   revalidateHashedBackupFiles,
		validateRoot: validateFinalBackupRoot,
	}
}

func defaultBackupLiveVerifyOps() backupLiveVerifyOps {
	return backupLiveVerifyOps{
		lstat: os.Lstat, identity: fileidentity.Existing,
		walkLegacy: walkLegacyInputs, verifyRecord: verifyLiveSourceRecord,
	}
}

func (b *backupSession) verifyFiles(ctx context.Context) error {
	return b.verifyFilesWithOps(ctx, defaultBackupVerifyOps())
}

func (b *backupSession) verifyFilesWithOps(
	ctx context.Context,
	ops backupVerifyOps,
) error {
	if b == nil || b.root == "" {
		return errors.New("database backup session is unavailable")
	}
	if err := ops.validateManifest(b.manifest); err != nil {
		return err
	}
	if b.parent != filepath.Dir(b.root) {
		return errors.New("database backup parent provenance changed")
	}
	if err := validatePinnedPrivateBackupDirectory(b.parent, b.parentIdentity); err != nil {
		return fmt.Errorf("validate database backup parent: %w", err)
	}
	partial := backupPathHasSuffix(b.root, backupPartialSuffix)
	allowedStage := b.finalRoot != "" &&
		backupPathKey(b.root) == backupPathKey(b.finalRoot+backupPartialSuffix)
	if partial && !allowedStage || b.finalRoot != "" &&
		backupPathKey(b.root) != backupPathKey(b.finalRoot) && !allowedStage {
		return errors.New("database backup snapshot is not published")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rootInfo, err := ops.lstat(b.root)
	if err != nil {
		return fmt.Errorf("inspect database backup root: %w", err)
	}
	if err := ops.validateDirectory(b.root, rootInfo); err != nil {
		return fmt.Errorf("validate database backup root: %w", err)
	}
	expected, err := ops.marshal(b.manifest)
	if err != nil {
		return err
	}
	actual, manifestIdentity, err := ops.read(
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
	actualHash, markerIdentity, err := ops.read(
		filepath.Join(b.root, backupManifestHash), sha256.Size*2+2,
	)
	if err != nil {
		return fmt.Errorf("read database backup manifest hash: %w", err)
	}
	if !bytes.Equal(actualHash, expectedHash) {
		return errors.New("database backup manifest hash is invalid")
	}
	rootIdentity, rootType, rootExists, identityErr := ops.identity(b.root)
	if identityErr != nil || !rootExists || rootType != fileidentity.ObjectTypeDirectory ||
		!b.identity.Valid() || rootIdentity != b.identity {
		return errors.Join(errors.New("database backup root identity changed"), identityErr)
	}
	firstTree, err := ops.exactTree(ctx, b.root, b.manifest)
	if err != nil {
		return err
	}
	inventoryByPath, err := backupInventoryByPath(firstTree)
	if err != nil {
		return err
	}
	manifestTreeIdentity, manifestPresent := inventoryByPath[backupManifestName]
	markerTreeIdentity, markerPresent := inventoryByPath[backupManifestHash]
	if !manifestIdentity.Valid() || !markerIdentity.Valid() || !manifestPresent || !markerPresent ||
		manifestTreeIdentity != manifestIdentity || markerTreeIdentity != markerIdentity {
		return errors.New("database backup control file identity changed during verification")
	}

	verifiedMetadata := make(map[string]os.FileInfo, len(b.manifest.Files))
	for _, record := range b.manifest.Files {
		relative := filepath.Clean(filepath.FromSlash(record.Backup))
		path := filepath.Join(b.root, relative)
		expectedIdentity, present := inventoryByPath[relative]
		if !present {
			return errors.New("database backup inventory path identity is unavailable")
		}
		info, statErr := ops.lstat(path)
		if statErr != nil {
			return fmt.Errorf("inspect database backup file: %w", statErr)
		}
		if info.Size() != record.Size {
			return errors.New("database backup file metadata is invalid")
		}
		fileDigest, size, hashErr := ops.hash(
			ctx, path, info, expectedIdentity, record.Size,
		)
		if hashErr != nil {
			return hashErr
		}
		if size != record.Size || fileDigest != record.SHA256 {
			return errors.New("database backup file hash is invalid")
		}
		verifiedMetadata[relative] = info
	}
	secondTree, err := ops.exactTree(ctx, b.root, b.manifest)
	if err != nil {
		return err
	}
	if !sameBackupIdentityInventory(firstTree, secondTree) {
		return errors.New("database backup tree identities changed during verification")
	}
	if ops.revalidate == nil {
		return errors.New("database backup final file revalidation is unavailable")
	}
	if err := ops.revalidate(ctx, b.root, inventoryByPath, verifiedMetadata); err != nil {
		return err
	}
	finalManifest, finalManifestIdentity, manifestErr := ops.read(
		filepath.Join(b.root, backupManifestName), backupMaxManifestSize,
	)
	finalMarker, finalMarkerIdentity, markerErr := ops.read(
		filepath.Join(b.root, backupManifestHash), sha256.Size*2+2,
	)
	if manifestErr != nil || markerErr != nil || finalManifestIdentity != manifestIdentity ||
		finalMarkerIdentity != markerIdentity || !bytes.Equal(finalManifest, actual) ||
		!bytes.Equal(finalMarker, actualHash) {
		return errors.Join(
			errors.New("database backup control files changed during verification"),
			manifestErr, markerErr,
		)
	}
	finalRootIdentity, finalRootType, finalRootExists, identityErr := ops.identity(b.root)
	if identityErr != nil || !finalRootExists || finalRootType != fileidentity.ObjectTypeDirectory ||
		finalRootIdentity != rootIdentity {
		return errors.Join(errors.New("database backup root identity changed during verification"), identityErr)
	}
	if ops.validateRoot == nil {
		return errors.New("database backup final root validation is unavailable")
	}
	if err := ops.validateRoot(b.root, b.identity); err != nil {
		return fmt.Errorf("revalidate database backup root: %w", err)
	}
	return validatePinnedPrivateBackupDirectory(b.parent, b.parentIdentity)
}

func validateFinalBackupRoot(rootPath string, expected fileidentity.Identity) (returnErr error) {
	if !validBackupAbsolutePath(rootPath) || !expected.Valid() {
		return errors.New("database backup final root identity is invalid")
	}
	root, err := openExactBackupRoot(rootPath)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	rootFile, err := openPinnedBackupChild(root, ".")
	if err != nil {
		return err
	}
	info, statErr := rootFile.Stat()
	identity, objectType, identityErr := fileidentity.Opened(rootFile)
	privacyErr := fileutil.ValidatePrivateDirectory(rootPath, info)
	mountErr := validateExactBackupRootMount(rootPath, rootFile)
	closeErr := rootFile.Close()
	if statErr != nil || identityErr != nil || privacyErr != nil || mountErr != nil ||
		closeErr != nil || info == nil || !info.IsDir() ||
		objectType != fileidentity.ObjectTypeDirectory || identity != expected {
		return errors.Join(
			errors.New("database backup final root changed during verification"),
			statErr, identityErr, privacyErr, mountErr, closeErr,
		)
	}
	return nil
}

func revalidateHashedBackupFiles(
	ctx context.Context,
	rootPath string,
	inventory map[string]fileidentity.Identity,
	metadata map[string]os.FileInfo,
) (returnErr error) {
	root, err := openExactBackupRoot(rootPath)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	rootFile, err := openPinnedBackupChild(root, ".")
	if err != nil {
		return err
	}
	rootMountErr := validateExactBackupRootMount(rootPath, rootFile)
	rootCloseErr := rootFile.Close()
	if rootMountErr != nil || rootCloseErr != nil {
		return errors.Join(rootMountErr, rootCloseErr)
	}
	for relative, before := range metadata {
		if err := ctx.Err(); err != nil {
			return err
		}
		file, err := openExactBackupChild(root, relative)
		if err != nil {
			return err
		}
		info, statErr := file.Stat()
		identity, objectType, identityErr := fileidentity.Opened(file)
		path := filepath.Join(rootPath, relative)
		privateErr := fileutil.ValidatePrivateFile(path, info)
		platformErr := validateBackupPlatformFile(info, file, 0o600)
		closeErr := file.Close()
		if statErr != nil || identityErr != nil || privateErr != nil || platformErr != nil ||
			closeErr != nil || info == nil || objectType != fileidentity.ObjectTypeRegular ||
			identity != inventory[relative] || before.Size() != info.Size() ||
			before.Mode() != info.Mode() || !before.ModTime().Equal(info.ModTime()) {
			return errors.Join(
				errors.New("database backup file changed after hashing"),
				statErr, identityErr, privateErr, platformErr, closeErr,
			)
		}
	}
	return nil
}

func backupInventoryByPath(
	inventory map[fileidentity.Identity]string,
) (map[string]fileidentity.Identity, error) {
	result := make(map[string]fileidentity.Identity, len(inventory))
	for identity, path := range inventory {
		if !identity.Valid() || result[path].Valid() {
			return nil,
				errors.New("database backup inventory path identity is ambiguous")
		}
		result[path] = identity
	}
	return result, nil
}

func sameBackupIdentityInventory(
	left,
	right map[fileidentity.Identity]string,
) bool {
	if len(left) != len(right) {
		return false
	}
	for identity, path := range left {
		if right[identity] != path {
			return false
		}
	}
	return true
}

// verifyLiveSources proves the claimed live inputs still describe the exact
// generation and legacy bytes recorded by snapshot. It is intentionally
// read-only and must run immediately before a staged cutover.
func (b *backupSession) verifyLiveSources(ctx context.Context, spec storecatalog.Spec) error {
	if b == nil {
		return errors.New("database backup session is unavailable")
	}
	if err := b.verifyStore(ctx, spec.ID); err != nil {
		return fmt.Errorf("verify database backup before live sources: %w", err)
	}
	return b.verifyLiveSourcesWithOps(ctx, spec, defaultBackupLiveVerifyOps())
}

func (b *backupSession) verifyLiveSourcesWithOps(
	ctx context.Context,
	spec storecatalog.Spec,
	ops backupLiveVerifyOps,
) error {
	state, err := b.newBackupLiveVerificationState(ctx, ops)
	if err != nil {
		return err
	}
	return b.verifyLiveSourcesWithState(ctx, spec, ops, state)
}

func (b *backupSession) newBackupLiveVerificationState(
	ctx context.Context,
	ops backupLiveVerifyOps,
) (*backupLiveVerificationState, error) {
	if b == nil || b.root == "" {
		return nil, errors.New("database live-source verification session is invalid")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	excluded, err := b.catalogGenerationExclusions()
	if err != nil {
		return nil, err
	}
	state := &backupLiveVerificationState{
		excludedKeys:   make(map[string]struct{}, len(excluded)),
		identityOwners: make(map[fileidentity.Identity]string, len(excluded)),
		stores:         make(map[string]BackupStoreManifest, len(b.manifest.Stores)),
		records:        make(map[string]backupLiveStoreRecords, len(b.manifest.Stores)),
	}
	for _, store := range b.manifest.Stores {
		state.stores[store.StoreID] = store
		state.records[store.StoreID] = backupLiveStoreRecords{
			generation: make(map[string]BackupFileManifest, 4),
			legacy:     make(map[string]BackupFileManifest),
		}
	}
	for _, record := range b.manifest.Files {
		indexed, present := state.records[record.StoreID]
		if !present {
			return nil, errors.New("database backup live-source record names an unknown store")
		}
		switch record.Role {
		case "database", "wal", "shm", "journal":
			indexed.generation[record.Role] = record
		case "legacy":
			key := fmt.Sprintf("%06d\x00%s", record.LegacyRoot, backupPathKey(record.Source))
			indexed.legacy[key] = record
		}
	}
	for key := range excluded {
		state.excludedKeys[key] = struct{}{}
	}
	for _, path := range sortedBackupPaths(excluded) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, statErr := ops.lstat(path)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return nil, fmt.Errorf("inspect catalog generation exclusion: %w", statErr)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("catalog generation exclusion is unsafe")
		}
		identity, exists, identityErr := ops.identity(path)
		if identityErr != nil || !exists {
			return nil, errors.Join(errors.New("catalog generation identity is unavailable"), identityErr)
		}
		if err := rememberLiveIdentity(state.identityOwners, identity, path); err != nil {
			return nil, err
		}
	}
	return state, nil
}

func (b *backupSession) verifyLiveSourcesWithState(
	ctx context.Context,
	spec storecatalog.Spec,
	ops backupLiveVerifyOps,
	state *backupLiveVerificationState,
) error {
	if b == nil || b.root == "" || !spec.ID.Valid() || !validBackupAbsolutePath(spec.Path) {
		return errors.New("database live-source verification input is invalid")
	}
	if state == nil || state.excludedKeys == nil || state.identityOwners == nil ||
		state.stores == nil || state.records == nil {
		return errors.New("database live-source verification state is invalid")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store, present := state.stores[spec.ID.String()]
	if !present {
		return errors.New("database backup store manifest is missing")
	}
	if err := validateBackupManifestStoreSpec(store, spec); err != nil {
		return err
	}

	indexed := state.records[spec.ID.String()]
	generationRecords, legacyRecords := indexed.generation, indexed.legacy

	roles := []string{"database", "wal", "shm", "journal"}
	paths := generationPaths(spec.Path)
	for index, role := range roles {
		path := paths[index]
		record, recorded := generationRecords[role]
		info, statErr := ops.lstat(path)
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
		identity, verifyErr := ops.verifyRecord(ctx, path, record)
		if verifyErr != nil {
			return fmt.Errorf("verify live database %s: %w", role, verifyErr)
		}
		if err := rememberLiveIdentity(state.identityOwners, identity, path); err != nil {
			return err
		}
	}

	legacyRoots := spec.LegacyRoots

	actualLegacy := make(map[string]string, len(legacyRecords))
	budget := newBackupBudget()
	for rootIndex, root := range legacyRoots {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, statErr := ops.lstat(root)
		actualKind := "missing"
		if statErr == nil {
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				return errors.New("live legacy root is a symlink")
			case info.Mode().IsRegular():
				actualKind = "file"
			case info.IsDir():
				actualKind = "directory"
			default:
				return errors.New("live legacy root has an unsafe type")
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("inspect live legacy root: %w", statErr)
		}
		if actualKind != store.LegacyRootKinds[rootIndex] {
			return errors.New("live legacy root layout changed after snapshot")
		}
		if actualKind != "missing" {
			rootIdentity, exists, identityErr := ops.identity(root)
			if identityErr != nil || !exists {
				return errors.Join(errors.New("live legacy root identity is unavailable"), identityErr)
			}
			if err := rememberLiveIdentity(state.identityOwners, rootIdentity, root); err != nil {
				return err
			}
		}
		walkErr := ops.walkLegacy(
			ctx,
			root,
			b.root,
			state.excludedKeys,
			budget,
			func(path string) error {
				key := fmt.Sprintf("%06d\x00%s", rootIndex, backupPathKey(path))
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
		afterInfo, afterErr := ops.lstat(root)
		afterKind := "missing"
		if afterErr == nil {
			switch {
			case afterInfo.Mode()&os.ModeSymlink != 0:
				return errors.New("live legacy root became a symlink")
			case afterInfo.Mode().IsRegular():
				afterKind = "file"
			case afterInfo.IsDir():
				afterKind = "directory"
			default:
				return errors.New("live legacy root changed to an unsafe type")
			}
		} else if !errors.Is(afterErr, os.ErrNotExist) {
			return fmt.Errorf("reinspect live legacy root: %w", afterErr)
		}
		if afterKind != actualKind {
			return errors.New("live legacy root layout changed during verification")
		}
	}

	seenLegacy := make(map[string]struct{}, len(actualLegacy))
	sortedLegacy := make([]string, 0, len(actualLegacy))
	for key := range actualLegacy {
		sortedLegacy = append(sortedLegacy, key)
	}
	sort.Strings(sortedLegacy)
	for _, key := range sortedLegacy {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := actualLegacy[key]
		identity, exists, identityErr := ops.identity(path)
		if identityErr != nil || !exists {
			return errors.Join(
				errors.New("live legacy input identity is unavailable"), identityErr,
			)
		}
		if err := rememberLiveIdentity(state.identityOwners, identity, path); err != nil {
			return err
		}
	}
	for _, key := range sortedLegacy {
		path := actualLegacy[key]
		record, expected := legacyRecords[key]
		if !expected {
			return errors.New("live legacy input was added after snapshot")
		}
		if record.Source != path {
			return errors.New("live legacy input path changed after snapshot")
		}
		identity, verifyErr := ops.verifyRecord(ctx, path, record)
		if verifyErr != nil {
			return fmt.Errorf("verify live legacy input: %w", verifyErr)
		}
		if err := rememberLiveIdentity(state.identityOwners, identity, path); err != nil {
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
		!sameBackupPhysicalPath(previous, path) {
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
	if ctx == nil {
		ctx = context.Background()
	}
	before, err := os.Lstat(path)
	if err != nil {
		return fileidentity.Identity{}, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 ||
		before.Size() != record.Size || uint32(before.Mode().Perm()) != record.SourceMode {
		return fileidentity.Identity{}, errors.New("live source metadata changed after snapshot")
	}
	identity, objectType, exists, err := fileidentity.ExistingWithType(path)
	if err != nil || !exists || objectType != fileidentity.ObjectTypeRegular ||
		identity.String() != record.SourceIdentity {
		return fileidentity.Identity{}, errors.Join(
			errors.New("live source identity changed after snapshot"), err,
		)
	}
	file, opened, err := openPinnedBackupPath(path, false)
	if err != nil || opened == nil || !opened.Mode().IsRegular() ||
		opened.Mode()&os.ModeSymlink != 0 || before.Size() != opened.Size() ||
		before.Mode() != opened.Mode() || !before.ModTime().Equal(opened.ModTime()) {
		if file != nil {
			err = errors.Join(err, file.Close())
		}
		return fileidentity.Identity{}, errors.Join(
			errors.New("live source changed while opening"), err,
		)
	}
	if err := validateBackupSourceFile(opened, file); err != nil {
		return fileidentity.Identity{}, errors.Join(err, file.Close())
	}
	digest := sha256.New()
	size, hashErr := copyWithContext(ctx, io.Discard, digest, file, record.Size)
	after, err := os.Lstat(path)
	afterIdentity, afterType, afterExists, identityErr := fileidentity.ExistingWithType(path)
	afterOpened, afterOpenedErr := file.Stat()
	afterOpenedIdentity, afterOpenedType, afterOpenedIdentityErr := fileidentity.Opened(file)
	afterSourceErr := validateBackupSourceFile(afterOpened, file)
	closeErr := file.Close()
	if hashErr != nil || closeErr != nil {
		return fileidentity.Identity{}, errors.Join(hashErr, closeErr)
	}
	if err != nil || after == nil || identityErr != nil || !afterExists ||
		afterType != fileidentity.ObjectTypeRegular || afterIdentity != identity ||
		before.Size() != after.Size() ||
		before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) ||
		afterOpenedErr != nil || afterOpenedIdentityErr != nil || afterOpened == nil ||
		afterOpenedType != fileidentity.ObjectTypeRegular || afterOpenedIdentity != identity ||
		before.Size() != afterOpened.Size() || before.Mode() != afterOpened.Mode() ||
		!before.ModTime().Equal(afterOpened.ModTime()) ||
		afterSourceErr != nil {
		return fileidentity.Identity{}, errors.Join(
			errors.New("live source changed during verification"), err, identityErr,
			afterOpenedErr, afterOpenedIdentityErr, afterSourceErr,
		)
	}
	if size != record.Size || hex.EncodeToString(digest.Sum(nil)) != record.SHA256 {
		return fileidentity.Identity{}, errors.New("live source content changed after snapshot")
	}
	return identity, nil
}
