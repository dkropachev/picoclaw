package databasemigration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/internal/storecatalog"
)

func validateBackupParent(
	value,
	canonicalHome string,
	specs []storecatalog.Spec,
) (string, error) {
	return validateBackupParentWithContext(context.Background(), value, canonicalHome, specs)
}

func validateBackupParentWithContext(
	ctx context.Context,
	value,
	canonicalHome string,
	specs []storecatalog.Spec,
) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := validateBackupParentCatalogBounds(ctx, specs); err != nil {
		return "", err
	}
	if !validBackupAbsolutePath(canonicalHome) {
		return "", errors.New("canonical database home is invalid")
	}
	if strings.TrimSpace(value) == "" {
		value = filepath.Join(canonicalHome, "backups")
	}
	if value != strings.TrimSpace(value) || strings.ContainsRune(value, 0) {
		return "", errors.New("database backup directory is invalid")
	}
	absolute, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", err
	}
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		for _, generation := range generationPaths(spec.Path) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if backupPathsOverlap(absolute, generation) {
				return "", errors.New("database backup directory overlaps a database generation")
			}
		}
		for _, legacyRoot := range spec.LegacyRoots {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if backupPathsOverlap(absolute, legacyRoot) {
				return "", errors.New("database backup directory overlaps a legacy input")
			}
		}
	}
	if _, err := os.Lstat(absolute); errors.Is(err, os.ErrNotExist) {
		if err := validateBackupParentPhysicalAliasesBoundContext(
			ctx, absolute, fileidentity.Identity{}, specs,
		); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", fmt.Errorf("inspect database backup directory before creation: %w", err)
	}
	return absolute, nil
}

func configuredBackupParentIsCustom(configured, parent, canonicalHome string) bool {
	return strings.TrimSpace(configured) != "" &&
		filepath.Clean(parent) != filepath.Clean(filepath.Join(canonicalHome, "backups"))
}

func validateBackupParentCatalogBounds(ctx context.Context, specs []storecatalog.Spec) (int, error) {
	if len(specs) > backupMaxEntries/4 {
		return 0, errors.New("database backup parent generation catalog limit exceeded")
	}
	legacyRoots := 0
	for index, spec := range specs {
		if ctx != nil && index > 0 && index%256 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
		}
		if len(spec.LegacyRoots) > backupMaxLegacyRoots-legacyRoots {
			return 0, errors.New("database backup parent legacy catalog limit exceeded")
		}
		legacyRoots += len(spec.LegacyRoots)
	}
	return legacyRoots, nil
}

const backupParentCreationNameAttempts = 8

const backupMaxParentProjectionSources = backupMaxEntries + backupMaxLegacyRoots

type backupParentCreationOps struct {
	random    func() (string, error)
	mkdir     func(*os.Root, string, os.FileMode) error
	open      func(*os.Root, string) (*os.File, error)
	opened    func(*os.File) (fileidentity.Identity, fileidentity.ObjectType, error)
	validate  func(*os.Root, string, *os.File, fileidentity.Identity, fileidentity.ObjectType) error
	secure    func(*os.File, fileidentity.Identity) error
	syncChild func(*os.File, fileidentity.Identity) error
	publish   func(*os.Root, string, string, *os.File) (*os.File, error)
	missing   func(*os.Root, string) error
	sync      func(*os.Root) error
	cleanup   func(string, fileidentity.Identity) error
}

func defaultBackupParentCreationOps() backupParentCreationOps {
	return backupParentCreationOps{
		random: randomBackupParentCreationLeaf,
		mkdir: func(root *os.Root, leaf string, mode os.FileMode) error {
			return root.Mkdir(leaf, mode)
		},
		open: openExactBackupChild, opened: fileidentity.Opened,
		validate:  validateBackupRemovalRelativeBinding,
		secure:    secureBackupParentCreatedDirectoryHandle,
		syncChild: syncBackupParentCreatedDirectoryHandle,
		publish:   renameBackupRemovalRootNoReplace,
		missing:   requireMissingBackupRemovalRootLeaf,
		sync:      syncBackupRemovalRoot,
		cleanup:   removeCapturedEmptyBackupParent,
	}
}

func exclusivelyCreateMissingBackupParent(
	path string,
	observedMissing bool,
) (fileidentity.Identity, error) {
	return exclusivelyCreateMissingBackupParentWithOps(
		path, observedMissing, defaultBackupParentCreationOps(),
	)
}

func exclusivelyCreateMissingBackupParentWithOps(
	path string,
	observedMissing bool,
	ops backupParentCreationOps,
) (owned fileidentity.Identity, returnErr error) {
	if !observedMissing {
		return fileidentity.Identity{}, nil
	}
	if !validBackupAbsolutePath(path) || ops.random == nil || ops.mkdir == nil ||
		ops.open == nil || ops.opened == nil || ops.validate == nil || ops.publish == nil ||
		ops.secure == nil || ops.syncChild == nil || ops.missing == nil || ops.sync == nil ||
		ops.cleanup == nil {
		return fileidentity.Identity{}, errors.New("database backup parent creation input is invalid")
	}
	root, finalLeaf, err := openPinnedBackupParent(path)
	if err != nil {
		return fileidentity.Identity{}, err
	}
	parentPath := filepath.Dir(path)
	var (
		temporaryLeaf string
		opened        *os.File
		exact         *os.File
		captured      fileidentity.Identity
		keep          bool
	)
	defer func() {
		var exactCloseErr error
		if exact != nil && exact != opened {
			exactCloseErr = exact.Close()
		}
		var openedCloseErr error
		if opened != nil {
			openedCloseErr = opened.Close()
		}
		returnErr = errors.Join(returnErr, exactCloseErr, openedCloseErr, root.Close())
		if returnErr != nil {
			keep = false
		}
		if !keep && captured.Valid() {
			if temporaryLeaf != "" {
				returnErr = errors.Join(
					returnErr,
					ops.cleanup(filepath.Join(parentPath, temporaryLeaf), captured),
				)
			}
			returnErr = errors.Join(returnErr, ops.cleanup(path, captured))
		}
		if returnErr != nil {
			owned = fileidentity.Identity{}
		}
	}()

	created := false
	for range backupParentCreationNameAttempts {
		temporaryLeaf, err = ops.random()
		if err != nil {
			return fileidentity.Identity{}, err
		}
		if !validBackupPathComponent(temporaryLeaf) || temporaryLeaf == finalLeaf {
			return fileidentity.Identity{}, errors.New("database backup temporary parent name is invalid")
		}
		if err = ops.mkdir(root, temporaryLeaf, 0o700); errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return fileidentity.Identity{}, err
		}
		created = true
		break
	}
	if !created {
		return fileidentity.Identity{}, errors.New("database backup temporary parent name is unavailable")
	}
	opened, err = ops.open(root, temporaryLeaf)
	if err != nil {
		// Without a handle identity the temporary name is not cleanup authority.
		return fileidentity.Identity{}, fmt.Errorf("open temporary database backup parent: %w", err)
	}
	identity, objectType, identityErr := ops.opened(opened)
	if identityErr != nil || !identity.Valid() || objectType != fileidentity.ObjectTypeDirectory {
		return fileidentity.Identity{}, errors.Join(
			errors.New("temporary database backup parent identity is unavailable"), identityErr,
		)
	}
	captured = identity
	temporaryPath := filepath.Join(parentPath, temporaryLeaf)
	if err := ops.validate(
		root, temporaryLeaf, opened, captured, fileidentity.ObjectTypeDirectory,
	); err != nil {
		return fileidentity.Identity{}, fmt.Errorf("bind temporary database backup parent: %w", err)
	}
	if err := validateBackupRemovalBinding(
		temporaryPath, root, temporaryLeaf, opened, captured, fileidentity.ObjectTypeDirectory,
	); err != nil {
		return fileidentity.Identity{}, fmt.Errorf("fence temporary database backup parent: %w", err)
	}
	if err := ops.secure(opened, captured); err != nil {
		return fileidentity.Identity{}, fmt.Errorf("secure temporary database backup parent: %w", err)
	}
	if err := validateBackupRemovalBinding(
		temporaryPath, root, temporaryLeaf, opened, captured, fileidentity.ObjectTypeDirectory,
	); err != nil {
		return fileidentity.Identity{}, fmt.Errorf(
			"revalidate secured temporary database backup parent: %w", err,
		)
	}
	if err := ops.syncChild(opened, captured); err != nil {
		return fileidentity.Identity{}, fmt.Errorf("sync temporary database backup parent: %w", err)
	}
	if err := validateBackupRemovalBinding(
		temporaryPath, root, temporaryLeaf, opened, captured, fileidentity.ObjectTypeDirectory,
	); err != nil {
		return fileidentity.Identity{}, fmt.Errorf(
			"revalidate synced temporary database backup parent: %w", err,
		)
	}
	exact, err = ops.publish(root, temporaryLeaf, finalLeaf, opened)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			// Another actor won the final-name race. The deferred cleanup is
			// authorized only for our captured temporary identity.
			return fileidentity.Identity{}, nil
		}
		return fileidentity.Identity{}, fmt.Errorf("publish database backup parent: %w", err)
	}
	if exact != nil {
		exactIdentity, exactType, exactErr := ops.opened(exact)
		mountErr := validateExactBackupOpenedMount(opened, exact)
		if exactErr != nil || mountErr != nil || exactIdentity != captured ||
			exactType != fileidentity.ObjectTypeDirectory {
			return fileidentity.Identity{}, errors.Join(
				errors.New("published database backup parent exact handle changed"),
				exactErr, mountErr,
			)
		}
	}
	if err := ops.missing(root, temporaryLeaf); err != nil {
		return fileidentity.Identity{}, errors.Join(
			errors.New("database backup temporary parent name remains after publication"), err,
		)
	}
	if err := validateBackupRemovalBinding(
		path, root, finalLeaf, opened, captured, fileidentity.ObjectTypeDirectory,
	); err != nil {
		return fileidentity.Identity{}, fmt.Errorf("bind published database backup parent: %w", err)
	}
	if err := ops.sync(root); err != nil {
		return fileidentity.Identity{}, fmt.Errorf("sync published database backup parent: %w", err)
	}
	if err := validateBackupRemovalBinding(
		path, root, finalLeaf, opened, captured, fileidentity.ObjectTypeDirectory,
	); err != nil {
		return fileidentity.Identity{}, fmt.Errorf("revalidate published database backup parent: %w", err)
	}
	keep = true
	return captured, nil
}

func randomBackupParentCreationLeaf() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return ".database-backup-parent-" + hex.EncodeToString(value[:]), nil
}

func removeCapturedEmptyBackupParent(path string, expected fileidentity.Identity) error {
	if !validBackupAbsolutePath(path) || !expected.Valid() {
		return errors.New("captured database backup parent cleanup identity is invalid")
	}
	identity, objectType, exists, err := fileidentity.ExistingWithType(path)
	if errors.Is(err, fileidentity.ErrUnsafeType) {
		// A substituted symlink or special object was never owned by the
		// captured directory handle and must not be removed.
		return nil
	}
	if err != nil {
		return err
	}
	if !exists || objectType != fileidentity.ObjectTypeDirectory || identity != expected {
		return nil
	}
	return removePinnedEmptyBackupDirectoryIdentity(path, expected)
}

func validateBackupParentPhysicalAliases(parent string, specs []storecatalog.Spec) error {
	return validateBackupParentPhysicalAliasesBound(parent, fileidentity.Identity{}, specs)
}

func validateBackupParentPhysicalAliasesBound(
	parent string,
	expected fileidentity.Identity,
	specs []storecatalog.Spec,
) error {
	return validateBackupParentPhysicalAliasesBoundContext(
		context.Background(), parent, expected, specs,
	)
}

func validateBackupParentPhysicalAliasesBoundContext(
	ctx context.Context,
	parent string,
	expected fileidentity.Identity,
	specs []storecatalog.Spec,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	legacyRootCount, err := validateBackupParentCatalogBounds(ctx, specs)
	if err != nil {
		return err
	}
	parentResolved, parentIdentity, exists, err := existingBackupDirectoryIdentity(parent)
	if err != nil {
		return fmt.Errorf("inspect database backup directory identity: %w", err)
	}
	prospective := !exists
	anchorPath := filepath.Clean(parent)
	var projection *backupParentProjectionState
	if prospective {
		if expected.Valid() {
			return errors.New("database backup directory binding changed before containment validation")
		}
		anchorPath, parentResolved, parentIdentity, err = nearestExistingBackupDirectoryIdentityContext(
			ctx, parent,
		)
		if err != nil {
			return fmt.Errorf("inspect prospective database backup directory ancestry: %w", err)
		}
		projection, err = newBackupParentProjectionState(
			ctx, anchorPath, parent, parentIdentity, fileidentity.ExistingWithType,
		)
		if err != nil {
			return fmt.Errorf("prepare prospective database backup directory projection: %w", err)
		}
	}
	if expected.Valid() && parentIdentity != expected {
		return errors.New("database backup directory binding changed before containment validation")
	}

	seen := make(map[string]struct{}, len(specs)*2)
	ancestors := &backupParentAncestorState{ctx: ctx, seen: make(map[string]struct{})}
	legacyRoots := make([]string, 0, legacyRootCount)
	check := func(path, kind string, projectProspective bool) error {
		cleaned := filepath.Clean(path)
		if prospective && projectProspective {
			if err := validateProspectiveBackupParentOutsideSource(cleaned, projection); err != nil {
				return fmt.Errorf("database backup directory physically overlaps %s: %w", kind, err)
			}
		}
		key := cleaned
		if _, duplicate := seen[key]; duplicate {
			return nil
		}
		seen[key] = struct{}{}

		resolved, identity, inputExists, identityErr := existingBackupDirectoryIdentity(cleaned)
		if identityErr != nil {
			return fmt.Errorf("inspect %s directory identity: %w", kind, identityErr)
		}
		if !prospective && inputExists && (backupPathKey(parentResolved) == backupPathKey(resolved) ||
			parentIdentity.Valid() && identity.Valid() && parentIdentity == identity) {
			return fmt.Errorf("database backup directory physically aliases %s", kind)
		}
		if !prospective {
			if err := validateBackupParentOutsideSourceAncestors(
				cleaned, parentIdentity, ancestors,
			); err != nil {
				return fmt.Errorf("database backup directory physically overlaps %s: %w", kind, err)
			}
		}
		return nil
	}

	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := check(filepath.Dir(spec.Path), "a database generation directory", false); err != nil {
			return err
		}
		for _, generation := range generationPaths(spec.Path) {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := check(generation, "a database generation directory", true); err != nil {
				return err
			}
		}
		for _, legacyRoot := range spec.LegacyRoots {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := check(legacyRoot, "a legacy input directory", true); err != nil {
				return err
			}
			legacyRoots = append(legacyRoots, legacyRoot)
		}
	}
	if err := validateBackupParentOutsideLegacyTrees(ctx, parentIdentity, legacyRoots); err != nil {
		return err
	}
	if prospective {
		if err := validateBackupParentProjectionStable(projection); err != nil {
			return fmt.Errorf("revalidate prospective database backup directory projection: %w", err)
		}
		if _, statErr := os.Lstat(parent); !errors.Is(statErr, os.ErrNotExist) {
			return errors.Join(
				errors.New("database backup directory appeared during prospective containment validation"),
				statErr,
			)
		}
		afterAnchor, afterResolved, afterIdentity, afterErr :=
			nearestExistingBackupDirectoryIdentityContext(ctx, parent)
		if afterErr != nil || filepath.Clean(afterAnchor) != filepath.Clean(anchorPath) ||
			afterIdentity != parentIdentity || filepath.Clean(afterResolved) != filepath.Clean(parentResolved) {
			return errors.Join(
				errors.New("database backup directory ancestor changed during containment validation"),
				afterErr,
			)
		}
		return nil
	}
	afterResolved, afterIdentity, afterExists, afterErr := existingBackupDirectoryIdentity(parent)
	if afterErr != nil || !afterExists || afterIdentity != parentIdentity ||
		backupPathKey(afterResolved) != backupPathKey(parentResolved) ||
		expected.Valid() && afterIdentity != expected {
		return errors.Join(
			errors.New("database backup directory binding changed during containment validation"),
			afterErr,
		)
	}
	return nil
}

type backupParentIdentityLookup func(string) (
	fileidentity.Identity,
	fileidentity.ObjectType,
	bool,
	error,
)

// backupParentProjectionState carries the portion of a prospective backup
// parent below its nearest existing directory. Projecting that suffix from a
// physically identical source ancestor exposes bind/null-mount aliases that
// pathname canonicalization cannot represent.
type backupParentProjectionState struct {
	ctx          context.Context
	anchor       fileidentity.Identity
	suffix       string
	steps        int
	sourceChecks int
	comparisons  int
	identity     backupParentIdentityLookup
	seen         map[string]backupParentProjectionObservation
	seenOrder    []string
	sources      map[string]struct{}
	sourceOrder  []string
	projected    map[string]struct{}
}

type backupParentProjectionObservation struct {
	identity   fileidentity.Identity
	objectType fileidentity.ObjectType
	exists     bool
}

func newBackupParentProjectionState(
	ctx context.Context,
	anchorPath,
	parent string,
	anchor fileidentity.Identity,
	identity backupParentIdentityLookup,
) (*backupParentProjectionState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validBackupAbsolutePath(anchorPath) || !validBackupAbsolutePath(parent) ||
		!anchor.Valid() || identity == nil {
		return nil, errors.New("database backup prospective projection state is invalid")
	}
	suffix, err := filepath.Rel(anchorPath, parent)
	if err != nil || !safeBackupRelative(suffix) {
		return nil, errors.Join(
			errors.New("database backup prospective projection suffix is invalid"), err,
		)
	}
	return &backupParentProjectionState{
		ctx: ctx, anchor: anchor, suffix: suffix, identity: identity,
		seen:    make(map[string]backupParentProjectionObservation),
		sources: make(map[string]struct{}), projected: make(map[string]struct{}),
	}, nil
}

func validateProspectiveBackupParentOutsideSource(
	source string,
	state *backupParentProjectionState,
) error {
	if !validBackupAbsolutePath(source) || state == nil || state.ctx == nil ||
		!state.anchor.Valid() || !safeBackupRelative(state.suffix) || state.identity == nil ||
		state.seen == nil || state.sources == nil || state.projected == nil {
		return errors.New("database backup prospective projection state is invalid")
	}
	source = filepath.Clean(source)
	if _, duplicate := state.sources[source]; duplicate {
		return nil
	}
	state.sourceChecks++
	if state.sourceChecks > backupMaxParentProjectionSources {
		return errors.New("database backup prospective projection source limit exceeded")
	}
	state.sources[source] = struct{}{}
	state.sourceOrder = append(state.sourceOrder, source)
	for projected := range state.projected {
		if err := compareProspectiveBackupProjection(projected, source, state); err != nil {
			return err
		}
	}
	// Generation members and legacy files are leaves, while an existing legacy
	// directory is covered by the retained-handle tree scan below. Starting at
	// the containing directory also makes all four members of one generation
	// share the same bounded ancestor observations.
	current := filepath.Dir(source)
	for {
		if err := state.ctx.Err(); err != nil {
			return err
		}
		key := filepath.Clean(current)
		if _, observed := state.seen[key]; observed {
			return nil
		}
		state.steps++
		if state.steps > backupMaxEntries {
			return errors.New("database backup prospective projection limit exceeded")
		}
		identity, objectType, exists, err := state.identity(current)
		if err != nil {
			return fmt.Errorf("inspect catalog source projection ancestor: %w", err)
		}
		observation := backupParentProjectionObservation{
			identity: identity, objectType: objectType, exists: exists,
		}
		state.seen[key] = observation
		state.seenOrder = append(state.seenOrder, key)
		if observation.exists && observation.objectType == fileidentity.ObjectTypeDirectory &&
			observation.identity == state.anchor {
			projected := filepath.Join(current, state.suffix)
			if !validBackupAbsolutePath(projected) {
				return errors.New("database backup prospective projected path is invalid")
			}
			if _, known := state.projected[projected]; !known {
				state.projected[projected] = struct{}{}
				for _, priorSource := range state.sourceOrder {
					if err := compareProspectiveBackupProjection(
						projected, priorSource, state,
					); err != nil {
						return err
					}
				}
			}
		}
		next := filepath.Dir(current)
		if next == current {
			return nil
		}
		current = next
	}
}

func compareProspectiveBackupProjection(
	projected,
	source string,
	state *backupParentProjectionState,
) error {
	if err := state.ctx.Err(); err != nil {
		return err
	}
	state.comparisons++
	if state.comparisons > backupMaxParentProjectionSources {
		return errors.New("database backup prospective projection comparison limit exceeded")
	}
	if backupPathsOverlap(projected, source) {
		return errors.New("prospective backup parent aliases a catalog source")
	}
	return nil
}

func validateBackupParentProjectionStable(state *backupParentProjectionState) error {
	if state == nil || state.ctx == nil || state.identity == nil || state.seen == nil ||
		len(state.seenOrder) != len(state.seen) {
		return errors.New("database backup prospective projection state is invalid")
	}
	for _, path := range state.seenOrder {
		if err := state.ctx.Err(); err != nil {
			return err
		}
		identity, objectType, exists, err := state.identity(path)
		if err != nil {
			return fmt.Errorf("reinspect catalog source projection ancestor: %w", err)
		}
		if state.seen[path] != (backupParentProjectionObservation{
			identity: identity, objectType: objectType, exists: exists,
		}) {
			return errors.New("catalog source projection ancestor changed during validation")
		}
	}
	return nil
}

func nearestExistingBackupDirectoryIdentity(path string) (
	ancestor string,
	resolved string,
	identity fileidentity.Identity,
	err error,
) {
	return nearestExistingBackupDirectoryIdentityContext(context.Background(), path)
}

func nearestExistingBackupDirectoryIdentityContext(
	ctx context.Context,
	path string,
) (
	ancestor string,
	resolved string,
	identity fileidentity.Identity,
	err error,
) {
	if ctx == nil {
		ctx = context.Background()
	}
	current := filepath.Clean(path)
	for steps := 0; steps <= backupMaxEntries; steps++ {
		if steps > 0 && steps%256 == 0 {
			if err := ctx.Err(); err != nil {
				return "", "", fileidentity.Identity{}, err
			}
		}
		resolved, identity, exists, inspectErr := existingBackupDirectoryIdentity(current)
		if inspectErr != nil {
			return "", "", fileidentity.Identity{}, inspectErr
		}
		if exists {
			return current, resolved, identity, nil
		}
		next := filepath.Dir(current)
		if next == current {
			break
		}
		current = next
	}
	return "", "", fileidentity.Identity{},
		errors.New("database backup directory has no stable existing ancestor")
}

type backupParentAncestorState struct {
	ctx   context.Context
	steps int
	seen  map[string]struct{}
}

func validateBackupParentOutsideSourceAncestors(
	path string,
	parentIdentity fileidentity.Identity,
	state *backupParentAncestorState,
) error {
	if !parentIdentity.Valid() || state == nil || state.seen == nil {
		return errors.New("database backup ancestor validation state is invalid")
	}
	current := filepath.Clean(path)
	info, err := os.Lstat(current)
	if err != nil || info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		current = filepath.Dir(current)
	}
	for {
		if state.ctx != nil {
			if err := state.ctx.Err(); err != nil {
				return err
			}
		}
		// Work deduplication must preserve case. Darwin supports both
		// case-sensitive and case-insensitive volumes, so a folded key could
		// suppress a distinct /A versus /a ancestor chain.
		key := filepath.Clean(current)
		if _, duplicate := state.seen[key]; duplicate {
			return nil
		}
		state.steps++
		if state.steps > backupMaxEntries {
			return errors.New("database backup physical ancestor limit exceeded")
		}
		state.seen[key] = struct{}{}
		identity, objectType, exists, identityErr := fileidentity.ExistingWithType(current)
		if identityErr != nil {
			return fmt.Errorf("inspect catalog source ancestor: %w", identityErr)
		}
		if exists && objectType == fileidentity.ObjectTypeDirectory && identity == parentIdentity {
			return errors.New("backup parent is a physical ancestor of a catalog source")
		}
		next := filepath.Dir(current)
		if next == current {
			return nil
		}
		current = next
	}
}

type backupParentLegacyScan struct {
	ctx       context.Context
	entries   int
	forbidden fileidentity.Identity
}

func validateBackupParentOutsideLegacyTrees(
	ctx context.Context,
	parentIdentity fileidentity.Identity,
	roots []string,
) error {
	if !parentIdentity.Valid() {
		return errors.New("database backup parent identity is unavailable")
	}
	state := &backupParentLegacyScan{
		ctx: ctx, forbidden: parentIdentity,
	}
	for _, root := range roots {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		info, err := os.Lstat(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect legacy root for physical containment: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			continue
		}
		if err := scanBackupLegacyDirectoriesForParent(filepath.Clean(root), 0, state); err != nil {
			return err
		}
	}
	return nil
}

func scanBackupLegacyDirectoriesForParent(
	path string,
	depth int,
	state *backupParentLegacyScan,
) (returnErr error) {
	if state == nil || !state.forbidden.Valid() || depth > backupMaxDepth {
		return errors.New("database backup legacy containment scan limit exceeded")
	}
	if state.ctx != nil {
		if err := state.ctx.Err(); err != nil {
			return err
		}
	}
	before, err := os.Lstat(path)
	if err != nil || before == nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return errors.Join(errors.New("database backup legacy containment directory is unsafe"), err)
	}
	identity, objectType, exists, identityErr := fileidentity.ExistingWithType(path)
	if identityErr != nil || !exists || objectType != fileidentity.ObjectTypeDirectory {
		return errors.Join(
			errors.New("database backup legacy containment identity is unavailable"), identityErr,
		)
	}
	root, err := openExactBackupRoot(path)
	if err != nil {
		return fmt.Errorf("open legacy containment root: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	if err := scanBackupLegacyDirectoryRoot(path, depth, state, root, identity, before); err != nil {
		return err
	}
	if err := validateBackupLegacyContainmentRootPath(path, root, identity); err != nil {
		return err
	}
	after, err := os.Lstat(path)
	afterIdentity, afterType, afterExists, afterIdentityErr := fileidentity.ExistingWithType(path)
	if err != nil || afterIdentityErr != nil || !afterExists ||
		afterType != fileidentity.ObjectTypeDirectory || afterIdentity != identity ||
		after == nil || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
		return errors.Join(
			errors.New("database backup legacy containment root changed during scan"),
			err, afterIdentityErr,
		)
	}
	return nil
}

func scanBackupLegacyDirectoryRoot(
	path string,
	depth int,
	state *backupParentLegacyScan,
	root *os.Root,
	expected fileidentity.Identity,
	before os.FileInfo,
) (returnErr error) {
	if state == nil || root == nil || before == nil || !expected.Valid() ||
		depth > backupMaxDepth {
		return errors.New("database backup legacy containment scan limit exceeded")
	}
	if state.ctx != nil {
		if err := state.ctx.Err(); err != nil {
			return err
		}
	}
	directory, err := openPinnedBackupChild(root, ".")
	if err != nil {
		return fmt.Errorf("open legacy containment directory: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, directory.Close()) }()
	opened, statErr := directory.Stat()
	identity, objectType, identityErr := fileidentity.Opened(directory)
	if statErr != nil || identityErr != nil || identity != expected ||
		objectType != fileidentity.ObjectTypeDirectory || opened == nil || !opened.IsDir() ||
		opened.Mode() != before.Mode() || !opened.ModTime().Equal(before.ModTime()) {
		return errors.Join(
			errors.New("database backup legacy containment directory changed while opening"),
			statErr, identityErr,
		)
	}
	if identity == state.forbidden {
		return errors.New("legacy input physically contains the database backup directory")
	}
	state.entries++
	if state.entries > backupMaxEntries {
		return errors.New("database backup legacy containment entry limit exceeded")
	}
	for {
		entries, readErr := readBackupDirectoryBatch(directory)
		for _, entry := range entries {
			if state.ctx != nil {
				if err := state.ctx.Err(); err != nil {
					return err
				}
			}
			if !validBackupPathComponent(entry.Name()) {
				return errors.New("database backup legacy containment path is invalid")
			}
			child := filepath.Join(path, entry.Name())
			info, childStatErr := root.Lstat(entry.Name())
			if childStatErr != nil {
				return fmt.Errorf("inspect legacy containment child: %w", childStatErr)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				state.entries++
				if state.entries > backupMaxEntries {
					return errors.New("database backup legacy containment entry limit exceeded")
				}
				continue
			}
			openedChild, err := openPinnedBackupChild(root, entry.Name())
			if err != nil {
				return fmt.Errorf("open legacy containment child: %w", err)
			}
			childInfo, childOpenedStatErr := openedChild.Stat()
			childIdentity, childType, childIdentityErr := fileidentity.Opened(openedChild)
			if childOpenedStatErr != nil || childIdentityErr != nil || childInfo == nil ||
				childType != fileidentity.ObjectTypeDirectory || !childInfo.IsDir() ||
				!os.SameFile(info, childInfo) || info.Mode() != childInfo.Mode() ||
				!info.ModTime().Equal(childInfo.ModTime()) {
				return errors.Join(
					errors.New("database backup legacy containment child changed while opening"),
					childOpenedStatErr, childIdentityErr, openedChild.Close(),
				)
			}
			childRoot, err := openBackupRootFromPinnedChild(root, entry.Name(), openedChild)
			childCloseErr := openedChild.Close()
			if err != nil || childCloseErr != nil {
				if childRoot != nil {
					childCloseErr = errors.Join(childCloseErr, childRoot.Close())
				}
				return errors.Join(err, childCloseErr)
			}
			if err := scanBackupLegacyDirectoryRoot(
				child, depth+1, state, childRoot, childIdentity, childInfo,
			); err != nil {
				return errors.Join(err, childRoot.Close())
			}
			bindingErr := validateBackupLegacyContainmentChildBinding(
				root, entry.Name(), childRoot, childIdentity,
			)
			if closeErr := childRoot.Close(); bindingErr != nil || closeErr != nil {
				return errors.Join(bindingErr, closeErr)
			}
			if state.ctx != nil {
				if err := state.ctx.Err(); err != nil {
					return err
				}
			}
			if finalInfo, finalErr := root.Lstat(entry.Name()); finalErr != nil ||
				finalInfo == nil || !os.SameFile(info, finalInfo) ||
				finalInfo.Mode() != info.Mode() || !finalInfo.ModTime().Equal(info.ModTime()) {
				return errors.Join(
					errors.New("database backup legacy containment child changed during scan"),
					finalErr,
				)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	after, afterErr := root.Lstat(".")
	finalOpened, finalStatErr := directory.Stat()
	finalIdentity, finalType, finalIdentityErr := fileidentity.Opened(directory)
	if afterErr != nil || finalStatErr != nil || finalIdentityErr != nil ||
		finalType != fileidentity.ObjectTypeDirectory || finalIdentity != expected ||
		after == nil || finalOpened == nil || after.Mode() != before.Mode() ||
		finalOpened.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) ||
		!finalOpened.ModTime().Equal(before.ModTime()) {
		return errors.Join(
			errors.New("database backup legacy containment directory changed during scan"),
			afterErr, finalStatErr, finalIdentityErr,
		)
	}
	return nil
}

func validateBackupLegacyContainmentChildBinding(
	parent *os.Root,
	leaf string,
	child *os.Root,
	expected fileidentity.Identity,
) (returnErr error) {
	if parent == nil || child == nil || !expected.Valid() || !validBackupPathComponent(leaf) {
		return errors.New("database backup legacy containment child binding is invalid")
	}
	retained, err := child.Open(".")
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, retained.Close()) }()
	bound, err := openPinnedBackupChild(parent, leaf)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, bound.Close()) }()
	retainedIdentity, retainedType, retainedErr := fileidentity.Opened(retained)
	boundIdentity, boundType, boundErr := fileidentity.Opened(bound)
	mountErr := validateExactBackupOpenedMount(retained, bound)
	if retainedErr != nil || boundErr != nil || mountErr != nil ||
		retainedType != fileidentity.ObjectTypeDirectory ||
		boundType != fileidentity.ObjectTypeDirectory ||
		retainedIdentity != expected || boundIdentity != expected {
		return errors.Join(
			errors.New("database backup legacy containment child binding changed"),
			retainedErr, boundErr, mountErr,
		)
	}
	return nil
}

func validateBackupLegacyContainmentRootPath(
	path string,
	root *os.Root,
	expected fileidentity.Identity,
) (returnErr error) {
	if root == nil || !expected.Valid() || !validBackupAbsolutePath(path) {
		return errors.New("database backup legacy containment root binding is invalid")
	}
	if filepath.Dir(path) != filepath.Clean(path) {
		parent, leaf, err := openPinnedBackupParent(path)
		if err != nil {
			return err
		}
		defer func() { returnErr = errors.Join(returnErr, parent.Close()) }()
		return validateBackupLegacyContainmentChildBinding(parent, leaf, root, expected)
	}
	retained, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, retained.Close()) }()
	current, err := openExactBackupRoot(path)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, current.Close()) }()
	bound, err := current.Open(".")
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, bound.Close()) }()
	retainedIdentity, retainedType, retainedErr := fileidentity.Opened(retained)
	boundIdentity, boundType, boundErr := fileidentity.Opened(bound)
	mountErr := validateExactBackupOpenedMount(retained, bound)
	if retainedErr != nil || boundErr != nil || mountErr != nil ||
		retainedType != fileidentity.ObjectTypeDirectory ||
		boundType != fileidentity.ObjectTypeDirectory ||
		retainedIdentity != expected || boundIdentity != expected {
		return errors.Join(
			errors.New("database backup legacy containment root binding changed"),
			retainedErr, boundErr, mountErr,
		)
	}
	return nil
}

func existingBackupDirectoryIdentity(path string) (
	resolved string,
	identity fileidentity.Identity,
	exists bool,
	err error,
) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fileidentity.Identity{}, false, nil
	}
	if err != nil {
		return "", fileidentity.Identity{}, false, err
	}
	if !info.IsDir() {
		return "", fileidentity.Identity{}, false, nil
	}
	resolved, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fileidentity.Identity{}, false, err
	}
	resolved, err = filepath.Abs(filepath.Clean(resolved))
	if err != nil {
		return "", fileidentity.Identity{}, false, err
	}
	identity, exists, err = fileidentity.Existing(resolved)
	if errors.Is(err, fileidentity.ErrUnsupported) {
		return resolved, fileidentity.Identity{}, true, nil
	}
	if err != nil || !exists {
		return "", fileidentity.Identity{}, false, errors.Join(
			errors.New("physical directory identity is unavailable"), err,
		)
	}
	return resolved, identity, true, nil
}

func backupPathsOverlap(left, right string) bool {
	leftKey := backupPathKey(filepath.Clean(left))
	rightKey := backupPathKey(filepath.Clean(right))
	if leftKey == rightKey {
		return true
	}
	separator := string(os.PathSeparator)
	leftPrefix := leftKey
	if !strings.HasSuffix(leftPrefix, separator) {
		leftPrefix += separator
	}
	rightPrefix := rightKey
	if !strings.HasSuffix(rightPrefix, separator) {
		rightPrefix += separator
	}
	return strings.HasPrefix(leftKey, rightPrefix) ||
		strings.HasPrefix(rightKey, leftPrefix)
}
