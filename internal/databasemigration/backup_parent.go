package databasemigration

import (
	"context"
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
			if backupPathsOverlap(absolute, generation) {
				return "", errors.New("database backup directory overlaps a database generation")
			}
		}
		for _, legacyRoot := range spec.LegacyRoots {
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

func exclusivelyCreateMissingBackupParent(path string, observedMissing bool) (owned bool, err error) {
	if !observedMissing {
		return false, nil
	}
	if err := createPinnedBackupDirectory(path, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			// Another actor won the exclusive create race. Continue validation,
			// but never treat that directory as rollback-owned.
			return false, nil
		}
		return false, err
	}
	return true, nil
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
	parentResolved, parentIdentity, exists, err := existingBackupDirectoryIdentity(parent)
	if err != nil {
		return fmt.Errorf("inspect database backup directory identity: %w", err)
	}
	prospective := !exists
	anchorPath := filepath.Clean(parent)
	if prospective {
		if expected.Valid() {
			return errors.New("database backup directory binding changed before containment validation")
		}
		anchorPath, parentResolved, parentIdentity, err = nearestExistingBackupDirectoryIdentity(parent)
		if err != nil {
			return fmt.Errorf("inspect prospective database backup directory ancestry: %w", err)
		}
	}
	if expected.Valid() && parentIdentity != expected {
		return errors.New("database backup directory binding changed before containment validation")
	}

	seen := make(map[string]struct{}, len(specs)*2)
	ancestors := &backupParentAncestorState{ctx: ctx, seen: make(map[string]struct{})}
	legacyRoots := make([]string, 0)
	check := func(path, kind string) error {
		cleaned := filepath.Clean(path)
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
		if err := check(filepath.Dir(spec.Path), "a database generation directory"); err != nil {
			return err
		}
		for _, generation := range generationPaths(spec.Path) {
			if err := check(generation, "a database generation directory"); err != nil {
				return err
			}
		}
		for _, legacyRoot := range spec.LegacyRoots {
			if err := check(legacyRoot, "a legacy input directory"); err != nil {
				return err
			}
			legacyRoots = append(legacyRoots, legacyRoot)
		}
	}
	if err := validateBackupParentOutsideLegacyTrees(ctx, parentIdentity, legacyRoots); err != nil {
		return err
	}
	if prospective {
		if _, statErr := os.Lstat(parent); !errors.Is(statErr, os.ErrNotExist) {
			return errors.Join(
				errors.New("database backup directory appeared during prospective containment validation"),
				statErr,
			)
		}
		afterAnchor, afterResolved, afterIdentity, afterErr :=
			nearestExistingBackupDirectoryIdentity(parent)
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

func nearestExistingBackupDirectoryIdentity(path string) (
	ancestor string,
	resolved string,
	identity fileidentity.Identity,
	err error,
) {
	current := filepath.Clean(path)
	for steps := 0; steps <= backupMaxEntries; steps++ {
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
