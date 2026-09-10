package databasemigration

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/sqliteprovider"
	"github.com/sipeed/picoclaw/internal/storecatalog"
	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

type backupPrepareOps struct {
	verifyStore func(*backupSession, context.Context, database.StoreID) error
	mkdirTemp   func(string, string) (string, error)
	removeAll   func(string, fileidentity.Identity) error
	syncDir     func(string) error
	secureDir   func(string) error
	ensureDir   func(string) error
	copyFile    func(
		context.Context, string, string, string, string, string, *backupBudget,
	) (BackupFileManifest, error)
	rel func(string, string) (string, error)
}

func defaultBackupPrepareOps() backupPrepareOps {
	return backupPrepareOps{
		verifyStore: (*backupSession).verifyStore,
		mkdirTemp:   createPinnedBackupTempDirectory,
		removeAll:   removePinnedBackupTreeIdentity,
		syncDir:     fileutil.SyncDirectory,
		secureDir:   secureAndValidateBackupDirectory,
		ensureDir:   ensurePrivateBackupDirectory,
		copyFile:    copyBackupFile,
		rel:         filepath.Rel,
	}
}

// prepareGeneration recreates one provider generation exclusively from
// verified backup bytes and returns a descriptor-pinned guarded-use seal. Its
// internal main path may be absent when selected store was absent at snapshot
// time. Sidecars use SQLite's exact sibling names.
func (b *backupSession) prepareGeneration(
	ctx context.Context,
	spec storecatalog.Spec,
) (*preparedGeneration, func() error, error) {
	return b.prepareGenerationWithSealer(ctx, spec, sealPreparedGeneration)
}

func (b *backupSession) prepareGenerationWithSealer(
	ctx context.Context,
	spec storecatalog.Spec,
	seal func(
		context.Context, string, BackupManifest, storecatalog.Spec,
	) (*preparedGeneration, error),
) (*preparedGeneration, func() error, error) {
	path, cleanup, err := b.prepareGenerationWithOps(ctx, spec, defaultBackupPrepareOps())
	if err != nil {
		return nil, cleanup, err
	}
	prepared, err := seal(ctx, path, b.manifest, spec)
	if err != nil {
		return nil, func() error { return nil }, errors.Join(err, cleanup())
	}
	var cleanupOnce sync.Once
	var cleanupErr error
	sealedCleanup := func() error {
		cleanupOnce.Do(func() {
			cleanupErr = errors.Join(prepared.close(), cleanup())
		})
		return cleanupErr
	}
	return prepared, sealedCleanup, nil
}

// prepareImmutableGenerationSource reconstructs and seals one manifest-bound
// generation, then mints the only provider source capability allowed to expose
// its path. The path remains available solely inside preparedGeneration.use;
// callers must observe cleanup after the provider operation finishes.
func (b *backupSession) prepareImmutableGenerationSource(
	ctx context.Context,
	spec storecatalog.Spec,
) (sqliteprovider.ImmutableGenerationSource, func() error, error) {
	return b.prepareImmutableGenerationSourceWithMinter(
		ctx, spec, sqliteprovider.NewImmutableGenerationSource,
	)
}

func (b *backupSession) prepareImmutableGenerationSourceWithMinter(
	ctx context.Context,
	spec storecatalog.Spec,
	mint func(
		database.StoreID,
		func(context.Context, func(context.Context, string) error) error,
	) (sqliteprovider.ImmutableGenerationSource, error),
) (sqliteprovider.ImmutableGenerationSource, func() error, error) {
	prepared, cleanup, err := b.prepareGeneration(ctx, spec)
	if err != nil {
		return sqliteprovider.ImmutableGenerationSource{}, cleanup, err
	}
	source, err := mint(spec.ID, prepared.use)
	if err != nil {
		return sqliteprovider.ImmutableGenerationSource{}, func() error { return nil },
			errors.Join(err, cleanup())
	}
	return source, cleanup, nil
}

func sealPreparedGeneration(
	ctx context.Context,
	path string,
	manifest BackupManifest,
	spec storecatalog.Spec,
) (*preparedGeneration, error) {
	return sealPreparedGenerationProvenance(
		ctx, path, manifest, backupProvenanceFromSpec(spec),
	)
}

func (b *backupSession) prepareGenerationWithOps(
	ctx context.Context,
	spec storecatalog.Spec,
	ops backupPrepareOps,
) (string, func() error, error) {
	noCleanup := func() error { return nil }
	if ctx == nil {
		ctx = context.Background()
	}
	if b == nil || b.root == "" || !spec.ID.Valid() {
		return "", noCleanup, errors.New("database backup generation is unavailable")
	}
	parent := filepath.Dir(b.root)
	if len(parent) > backupMaxPathBytes-128 {
		return "", noCleanup, errors.New("disposable migration generation parent path is too long")
	}
	if b.parent != parent {
		return "", noCleanup, errors.New("database backup generation parent provenance changed")
	}
	if err := validatePinnedPrivateBackupDirectory(parent, b.parentIdentity); err != nil {
		return "", noCleanup, fmt.Errorf("validate database backup generation parent: %w", err)
	}
	if err := ops.verifyStore(b, ctx, spec.ID); err != nil {
		return "", noCleanup, fmt.Errorf("verify generation backup: %w", err)
	}

	storeID := spec.ID.String()
	_, err := backupManifestStoreForSpec(b.manifest, spec)
	if err != nil {
		return "", noCleanup, err
	}

	records := make(map[string]BackupFileManifest, 4)
	for _, record := range b.manifest.Files {
		if record.StoreID != storeID {
			continue
		}
		switch record.Role {
		case "database", "wal", "shm", "journal":
			records[record.Role] = record
		}
	}

	workRoot, err := ops.mkdirTemp(parent, ".database-migration-generation-")
	if err != nil {
		return "", noCleanup, fmt.Errorf("create disposable migration generation: %w", err)
	}
	workIdentity, workType, workExists, identityErr := fileidentity.ExistingWithType(workRoot)
	if identityErr != nil || !workExists || workType != fileidentity.ObjectTypeDirectory {
		return "", noCleanup, errors.Join(
			errors.New("disposable migration generation identity is unavailable"),
			identityErr, ops.syncDir(parent),
		)
	}
	var cleanupOnce sync.Once
	var cleanupErr error
	cleanup := func() error {
		cleanupOnce.Do(func() {
			if err := validatePinnedPrivateBackupDirectory(parent, b.parentIdentity); err != nil {
				cleanupErr = err
				return
			}
			currentIdentity, objectType, exists, identityErr := fileidentity.ExistingWithType(workRoot)
			if identityErr != nil || !exists || objectType != fileidentity.ObjectTypeDirectory ||
				currentIdentity != workIdentity {
				cleanupErr = errors.Join(
					errors.New("disposable migration generation identity changed"), identityErr,
				)
				return
			}
			cleanupErr = errors.Join(ops.removeAll(workRoot, workIdentity), ops.syncDir(parent))
		})
		return cleanupErr
	}
	fail := func(cause error) (string, func() error, error) {
		return "", noCleanup, errors.Join(cause, cleanup())
	}
	if err := ops.secureDir(workRoot); err != nil {
		return fail(fmt.Errorf("secure disposable migration generation: %w", err))
	}
	securedIdentity, securedType, securedExists, securedErr := fileidentity.ExistingWithType(workRoot)
	if securedErr != nil || !securedExists || securedType != fileidentity.ObjectTypeDirectory ||
		securedIdentity != workIdentity {
		return fail(errors.Join(
			errors.New("disposable migration generation changed while securing"), securedErr,
		))
	}
	if err := ops.syncDir(parent); err != nil {
		return fail(fmt.Errorf("sync disposable migration generation parent: %w", err))
	}

	mainPath := filepath.Join(workRoot, "generation.db")
	budget := newBackupBudget()
	for roleIndex, role := range []string{"database", "wal", "shm", "journal"} {
		record, exists := records[role]
		if !exists {
			continue
		}
		source, _ := backupFilePath(b.root, record.Backup)
		suffix := []string{"", "-wal", "-shm", "-journal"}[roleIndex]
		destination := filepath.Base(mainPath) + suffix
		copied, copyErr := ops.copyFile(
			ctx, workRoot, storeID, role, source, destination, budget,
		)
		if copyErr != nil {
			return fail(fmt.Errorf("recreate disposable %s: %w", role, copyErr))
		}
		if copied.Size != record.Size || copied.SHA256 != record.SHA256 {
			return fail(errors.New("disposable database generation differs from backup"))
		}
	}
	if err := ops.verifyStore(b, ctx, spec.ID); err != nil {
		return fail(fmt.Errorf("reverify generation backup: %w", err))
	}
	return mainPath, cleanup, nil
}

// prepareLegacyInputs recreates one adapter's legacy view exclusively from
// verified backup bytes. The adapter never receives a live legacy path.
func (b *backupSession) prepareLegacyInputs(
	ctx context.Context,
	spec storecatalog.Spec,
) (*preparedLegacyInputs, func() error, error) {
	return b.prepareLegacyInputsWithSealer(ctx, spec, sealPreparedLegacyInputs)
}

func (b *backupSession) prepareLegacyInputsWithSealer(
	ctx context.Context,
	spec storecatalog.Spec,
	seal func(
		context.Context, string, []string, BackupManifest, storecatalog.Spec,
	) (*preparedLegacyInputs, error),
) (*preparedLegacyInputs, func() error, error) {
	roots, cleanup, err := b.prepareLegacyInputsWithOps(ctx, spec, defaultBackupPrepareOps())
	if err != nil || len(roots) == 0 {
		return nil, cleanup, err
	}
	workRoot := filepath.Dir(filepath.Dir(roots[0]))
	prepared, err := seal(ctx, workRoot, roots, b.manifest, spec)
	if err != nil {
		return nil, func() error { return nil }, errors.Join(err, cleanup())
	}
	var cleanupOnce sync.Once
	var cleanupErr error
	sealedCleanup := func() error {
		cleanupOnce.Do(func() {
			cleanupErr = errors.Join(prepared.close(), cleanup())
		})
		return cleanupErr
	}
	return prepared, sealedCleanup, nil
}

func sealPreparedLegacyInputs(
	ctx context.Context,
	root string,
	roots []string,
	manifest BackupManifest,
	spec storecatalog.Spec,
) (*preparedLegacyInputs, error) {
	return sealPreparedLegacyInputsProvenance(
		ctx, root, roots, manifest, backupProvenanceFromSpec(spec),
	)
}

func (b *backupSession) prepareLegacyInputsWithOps(
	ctx context.Context,
	spec storecatalog.Spec,
	ops backupPrepareOps,
) ([]string, func() error, error) {
	noCleanup := func() error { return nil }
	if ctx == nil {
		ctx = context.Background()
	}
	if b == nil || b.root == "" || !spec.ID.Valid() {
		return nil, noCleanup, errors.New("database legacy input backup is unavailable")
	}
	parent := filepath.Dir(b.root)
	if len(parent) > backupMaxPathBytes-128 {
		return nil, noCleanup, errors.New("disposable migration input parent path is too long")
	}
	if b.parent != parent {
		return nil, noCleanup, errors.New("database legacy input parent provenance changed")
	}
	if err := validatePinnedPrivateBackupDirectory(parent, b.parentIdentity); err != nil {
		return nil, noCleanup, fmt.Errorf("validate database legacy input parent: %w", err)
	}
	if err := ops.verifyStore(b, ctx, spec.ID); err != nil {
		return nil, noCleanup, fmt.Errorf("validate legacy input backup: %w", err)
	}
	store, err := backupManifestStoreForSpec(b.manifest, spec)
	if err != nil {
		return nil, noCleanup, err
	}
	if len(spec.LegacyRoots) == 0 {
		return nil, noCleanup, nil
	}
	workRoot, err := ops.mkdirTemp(parent, ".database-migration-inputs-")
	if err != nil {
		return nil, noCleanup, fmt.Errorf("create disposable migration inputs: %w", err)
	}
	workIdentity, workType, workExists, identityErr := fileidentity.ExistingWithType(workRoot)
	if identityErr != nil || !workExists || workType != fileidentity.ObjectTypeDirectory {
		return nil, noCleanup, errors.Join(
			errors.New("disposable migration input identity is unavailable"),
			identityErr, ops.syncDir(parent),
		)
	}
	var cleanupOnce sync.Once
	var cleanupErr error
	cleanup := func() error {
		cleanupOnce.Do(func() {
			if err := validatePinnedPrivateBackupDirectory(parent, b.parentIdentity); err != nil {
				cleanupErr = err
				return
			}
			currentIdentity, objectType, exists, identityErr := fileidentity.ExistingWithType(workRoot)
			if identityErr != nil || !exists || objectType != fileidentity.ObjectTypeDirectory ||
				currentIdentity != workIdentity {
				cleanupErr = errors.Join(
					errors.New("disposable migration input identity changed"), identityErr,
				)
				return
			}
			cleanupErr = errors.Join(ops.removeAll(workRoot, workIdentity), ops.syncDir(parent))
		})
		return cleanupErr
	}
	fail := func(cause error) ([]string, func() error, error) {
		return nil, noCleanup, errors.Join(cause, cleanup())
	}
	if err := ops.secureDir(workRoot); err != nil {
		return fail(fmt.Errorf("secure disposable migration inputs: %w", err))
	}
	securedIdentity, securedType, securedExists, securedErr := fileidentity.ExistingWithType(workRoot)
	if securedErr != nil || !securedExists || securedType != fileidentity.ObjectTypeDirectory ||
		securedIdentity != workIdentity {
		return fail(errors.Join(
			errors.New("disposable migration inputs changed while securing"), securedErr,
		))
	}
	if err := ops.syncDir(parent); err != nil {
		return fail(fmt.Errorf("sync disposable migration input parent: %w", err))
	}

	type inputRecord struct {
		relative string
		record   BackupFileManifest
	}
	result := make([]string, len(spec.LegacyRoots))
	budget := newBackupBudget()
	for rootIndex, legacyRoot := range spec.LegacyRoots {
		leaf := filepath.Base(filepath.Clean(legacyRoot))
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
			relative, _, _ := legacySourceRelative(legacyRoot, record.Source)
			matches = append(matches, inputRecord{relative: relative, record: record})
		}
		sort.Slice(matches, func(left, right int) bool {
			return matches[left].relative < matches[right].relative
		})
		if store.LegacyRootKinds[rootIndex] == "directory" {
			if err := ops.ensureDir(destinationRoot); err != nil {
				return fail(err)
			}
		}
		for _, match := range matches {
			source, _ := backupFilePath(b.root, match.record.Backup)
			destination := destinationRoot
			if match.relative != "." {
				destination = filepath.Join(destinationRoot, match.relative)
			}
			relativeDestination, relErr := ops.rel(workRoot, destination)
			if relErr != nil {
				return fail(relErr)
			}
			copied, copyErr := ops.copyFile(
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
	// Close the verify-to-use gap: adapters receive paths only after the source
	// archive has been revalidated following reconstruction.
	if err := ops.verifyStore(b, ctx, spec.ID); err != nil {
		return fail(fmt.Errorf("reverify legacy input backup: %w", err))
	}
	return result, cleanup, nil
}

func backupManifestStoreForSpec(
	manifest BackupManifest,
	spec storecatalog.Spec,
) (BackupStoreManifest, error) {
	if !spec.ID.Valid() || !validBackupAbsolutePath(spec.Path) {
		return BackupStoreManifest{}, errors.New("database backup store specification is invalid")
	}
	return backupManifestStoreForProvenance(manifest, backupProvenanceFromSpec(spec))
}

func backupProvenanceFromSpec(spec storecatalog.Spec) backupStoreProvenance {
	return backupStoreProvenance{
		storeID: spec.ID.String(), path: spec.Path,
		legacyRoots: append([]string(nil), spec.LegacyRoots...),
	}
}
