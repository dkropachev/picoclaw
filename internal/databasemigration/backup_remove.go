package databasemigration

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

const backupRemovalNameAttempts = 8

type backupRemovalOps struct {
	rename func(*os.Root, string, string, *os.File) (*os.File, error)
	remove func(*os.Root, string, *os.File, bool) error
	sync   func(*os.Root, string) error

	// Test-only race seams. Production leaves both nil.
	beforeRename    func(*os.Root, string) error
	afterQuarantine func(*os.Root, string) error
	beforeRemove    func(*os.Root, string) error
}

func defaultBackupRemovalOps() backupRemovalOps {
	return backupRemovalOps{
		rename: renameBackupRemovalRootNoReplace,
		remove: removeBackupRemovalRootEntry,
		sync: func(root *os.Root, _ string) error {
			return syncBackupRemovalRoot(root)
		},
	}
}

func removeBackupTreeDurable(
	path string,
	root *os.Root,
	leaf string,
	child *os.Root,
	expected fileidentity.Identity,
) error {
	return removeBackupTreeDurableWithOps(
		path, root, leaf, child, expected, defaultBackupRemovalOps(),
	)
}

func removeBackupTreeDurableWithSync(
	path string,
	root *os.Root,
	leaf string,
	child *os.Root,
	expected fileidentity.Identity,
	syncDir func(string) error,
) error {
	if syncDir == nil {
		return errors.New("database backup removal directory sync is unavailable")
	}
	ops := defaultBackupRemovalOps()
	ops.sync = func(root *os.Root, label string) error {
		return errors.Join(syncBackupRemovalRoot(root), syncDir(label))
	}
	return removeBackupTreeDurableWithOps(path, root, leaf, child, expected, ops)
}

func removeBackupTreeDurableWithOps(
	path string,
	root *os.Root,
	leaf string,
	child *os.Root,
	expected fileidentity.Identity,
	ops backupRemovalOps,
) (returnErr error) {
	if err := validateBackupRemovalTreeBinding(path, root, leaf, child, expected); err != nil {
		return err
	}
	opened, err := child.Open(".")
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, opened.Close()) }()
	validated := map[fileidentity.Identity]string{expected: path}
	validatedEntries := 0
	if err := validatePinnedBackupTreeInventory(
		path, child, expected, validated, &validatedEntries,
	); err != nil {
		return err
	}
	entries := 0
	return removeBackupTreeRelative(path, root, leaf, child, opened, expected, validated, &entries, ops)
}

func removeBackupTreeRelative(
	label string,
	parent *os.Root,
	leaf string,
	child *os.Root,
	opened *os.File,
	expected fileidentity.Identity,
	identities map[fileidentity.Identity]string,
	entries *int,
	ops backupRemovalOps,
) (returnErr error) {
	quarantine, exact, err := quarantineBackupRemovalLeaf(
		parent, leaf, opened, expected, fileidentity.ObjectTypeDirectory, label, ops,
	)
	if err != nil {
		return err
	}
	if exact != nil {
		defer func() {
			if exact != nil {
				returnErr = errors.Join(returnErr, exact.Close())
			}
		}()
	}
	if ops.afterQuarantine != nil {
		if err := ops.afterQuarantine(parent, quarantine); err != nil {
			return err
		}
	}
	if err := validateBackupRemovalRelativeBinding(
		parent, quarantine, opened, expected, fileidentity.ObjectTypeDirectory,
	); err != nil {
		return err
	}
	if err := requireMissingBackupRemovalRootLeaf(parent, leaf); err != nil {
		return errors.Join(errors.New("database backup removal source name changed"), err)
	}
	if err := validateBackupRemovalInventoryPlan(label, child, expected, identities); err != nil {
		return err
	}
	if err := removePinnedBackupTreeContentsBound(
		label, child, expected, identities, entries, ops,
	); err != nil {
		return err
	}
	return removeQuarantinedBackupLeaf(
		parent, leaf, quarantine, opened, &exact, expected,
		fileidentity.ObjectTypeDirectory, label, ops,
	)
}

func validateBackupRemovalInventoryPlan(
	rootPath string,
	root *os.Root,
	expectedRoot fileidentity.Identity,
	planned map[fileidentity.Identity]string,
) error {
	observed := map[fileidentity.Identity]string{expectedRoot: rootPath}
	entries := 0
	if err := validatePinnedBackupTreeInventory(
		rootPath, root, expectedRoot, observed, &entries,
	); err != nil {
		return err
	}
	for identity, path := range observed {
		if planned[identity] != path {
			return errors.New("database backup removal inventory changed")
		}
	}
	prefix := rootPath + string(filepath.Separator)
	for identity, path := range planned {
		if (path == rootPath || len(path) > len(prefix) && path[:len(prefix)] == prefix) &&
			observed[identity] != path {
			return errors.New("database backup removal inventory changed")
		}
	}
	return nil
}

func removeBackupFileDurable(
	path string,
	root *os.Root,
	leaf string,
	file *os.File,
	expected fileidentity.Identity,
) error {
	return removeBackupFileDurableWithOps(
		path, root, leaf, file, expected, defaultBackupRemovalOps(),
	)
}

func removeBackupFileDurableWithSync(
	path string,
	root *os.Root,
	leaf string,
	file *os.File,
	expected fileidentity.Identity,
	syncDir func(string) error,
) error {
	if syncDir == nil {
		return errors.New("database backup removal directory sync is unavailable")
	}
	ops := defaultBackupRemovalOps()
	ops.sync = func(root *os.Root, label string) error {
		return errors.Join(syncBackupRemovalRoot(root), syncDir(label))
	}
	return removeBackupFileDurableWithOps(path, root, leaf, file, expected, ops)
}

func removeBackupFileDurableWithOps(
	path string,
	root *os.Root,
	leaf string,
	file *os.File,
	expected fileidentity.Identity,
	ops backupRemovalOps,
) error {
	if err := validateBackupRemovalBinding(
		path, root, leaf, file, expected, fileidentity.ObjectTypeRegular,
	); err != nil {
		return err
	}
	return removeBackupFileRelative(path, root, leaf, file, expected, ops)
}

func removeBackupFileRelative(
	label string,
	root *os.Root,
	leaf string,
	file *os.File,
	expected fileidentity.Identity,
	ops backupRemovalOps,
) (returnErr error) {
	quarantine, exact, err := quarantineBackupRemovalLeaf(
		root, leaf, file, expected, fileidentity.ObjectTypeRegular, label, ops,
	)
	if err != nil {
		return err
	}
	if exact != nil {
		defer func() {
			if exact != nil {
				returnErr = errors.Join(returnErr, exact.Close())
			}
		}()
	}
	if ops.afterQuarantine != nil {
		if err := ops.afterQuarantine(root, quarantine); err != nil {
			return err
		}
	}
	return removeQuarantinedBackupLeaf(
		root, leaf, quarantine, file, &exact, expected,
		fileidentity.ObjectTypeRegular, label, ops,
	)
}

func quarantineBackupRemovalLeaf(
	root *os.Root,
	leaf string,
	opened *os.File,
	expected fileidentity.Identity,
	expectedType fileidentity.ObjectType,
	label string,
	ops backupRemovalOps,
) (string, *os.File, error) {
	if ops.rename == nil || ops.remove == nil || ops.sync == nil {
		return "", nil, errors.New("database backup removal operations are invalid")
	}
	if err := validateBackupRemovalRelativeBinding(
		root, leaf, opened, expected, expectedType,
	); err != nil {
		return "", nil, err
	}
	if ops.beforeRename != nil {
		if err := ops.beforeRename(root, leaf); err != nil {
			return "", nil, err
		}
	}
	for range backupRemovalNameAttempts {
		quarantine, err := randomBackupRemovalLeaf()
		if err != nil {
			return "", nil, err
		}
		exact, err := ops.rename(root, leaf, quarantine, opened)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		if err := ops.sync(root, filepath.Dir(label)); err != nil {
			if exact != nil {
				err = errors.Join(err, exact.Close())
			}
			return "", nil, err
		}
		if err := requireMissingBackupRemovalRootLeaf(root, leaf); err != nil {
			if exact != nil {
				err = errors.Join(err, exact.Close())
			}
			return "", nil, errors.Join(
				errors.New("database backup removal source name changed"), err,
			)
		}
		if err := validateBackupRemovalRelativeBinding(
			root, quarantine, opened, expected, expectedType,
		); err != nil {
			if exact != nil {
				err = errors.Join(err, exact.Close())
			}
			return "", nil, errors.Join(
				errors.New("database backup removal quarantine binding changed"), err,
			)
		}
		return quarantine, exact, nil
	}
	return "", nil, errors.New("database backup removal quarantine name is unavailable")
}

func removeQuarantinedBackupLeaf(
	root *os.Root,
	source string,
	quarantine string,
	opened *os.File,
	exact **os.File,
	expected fileidentity.Identity,
	expectedType fileidentity.ObjectType,
	label string,
	ops backupRemovalOps,
) error {
	if exact == nil {
		return errors.New("database backup removal exact-handle state is invalid")
	}
	if err := validateBackupRemovalRelativeBinding(
		root, quarantine, opened, expected, expectedType,
	); err != nil {
		return err
	}
	if err := requireMissingBackupRemovalRootLeaf(root, source); err != nil {
		return errors.Join(errors.New("database backup removal source name changed"), err)
	}
	if ops.beforeRemove != nil {
		if err := ops.beforeRemove(root, quarantine); err != nil {
			return err
		}
	}
	if err := validateBackupRemovalRelativeBinding(
		root, quarantine, opened, expected, expectedType,
	); err != nil {
		return err
	}
	if err := requireMissingBackupRemovalRootLeaf(root, source); err != nil {
		return errors.Join(errors.New("database backup removal source name changed"), err)
	}
	if err := ops.remove(
		root, quarantine, *exact, expectedType == fileidentity.ObjectTypeDirectory,
	); err != nil {
		return err
	}
	if *exact != nil {
		closeErr := (*exact).Close()
		*exact = nil
		if closeErr != nil {
			return closeErr
		}
	}
	if err := requireMissingBackupRemovalRootLeaf(root, quarantine); err != nil {
		return errors.Join(errors.New("database backup removal quarantine remains"), err)
	}
	return ops.sync(root, filepath.Dir(label))
}

func randomBackupRemovalLeaf() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return ".database-backup-remove-" + hex.EncodeToString(random[:]), nil
}

func validateBackupRemovalBinding(
	path string,
	root *os.Root,
	leaf string,
	opened *os.File,
	expected fileidentity.Identity,
	expectedType fileidentity.ObjectType,
) error {
	if !validBackupAbsolutePath(path) || filepath.Base(path) != leaf {
		return errors.New("database backup removal binding is invalid")
	}
	if err := validateBackupRemovalRelativeBinding(
		root, leaf, opened, expected, expectedType,
	); err != nil {
		return err
	}
	parent, err := root.Open(".")
	if err != nil {
		return err
	}
	parentIdentity, parentType, openedErr := fileidentity.Opened(parent)
	closeErr := parent.Close()
	pathIdentity, pathType, exists, pathErr := fileidentity.ExistingWithType(filepath.Dir(path))
	if openedErr != nil || closeErr != nil || pathErr != nil || !exists ||
		parentType != fileidentity.ObjectTypeDirectory ||
		pathType != fileidentity.ObjectTypeDirectory || parentIdentity != pathIdentity {
		return errors.Join(
			errors.New("database backup removal parent binding changed"),
			openedErr, closeErr, pathErr,
		)
	}
	return nil
}

func validateBackupRemovalRelativeBinding(
	root *os.Root,
	leaf string,
	opened *os.File,
	expected fileidentity.Identity,
	expectedType fileidentity.ObjectType,
) error {
	if root == nil || opened == nil || !expected.Valid() || !validBackupPathComponent(leaf) {
		return errors.New("database backup removal binding is invalid")
	}
	bound, err := openPinnedBackupChild(root, leaf)
	if err != nil {
		return err
	}
	boundIdentity, boundType, boundErr := fileidentity.Opened(bound)
	var boundMetadataErr, openedMetadataErr error
	if expectedType == fileidentity.ObjectTypeRegular {
		boundInfo, statErr := bound.Stat()
		openedInfo, openedStatErr := opened.Stat()
		boundMetadataErr = errors.Join(
			statErr, validateBackupPlatformFile(boundInfo, bound, 0o600),
		)
		openedMetadataErr = errors.Join(
			openedStatErr, validateBackupPlatformFile(openedInfo, opened, 0o600),
		)
	}
	boundCloseErr := bound.Close()
	openedIdentity, openedType, openedErr := fileidentity.Opened(opened)
	if boundErr != nil || boundMetadataErr != nil || boundCloseErr != nil ||
		openedErr != nil || openedMetadataErr != nil ||
		boundType != expectedType || openedType != expectedType ||
		boundIdentity != expected || openedIdentity != expected {
		return errors.Join(
			errors.New("database backup removal leaf binding changed"),
			boundErr, boundMetadataErr, boundCloseErr, openedErr, openedMetadataErr,
		)
	}
	return nil
}

func validateBackupRemovalTreeBinding(
	path string,
	root *os.Root,
	leaf string,
	child *os.Root,
	expected fileidentity.Identity,
) error {
	if child == nil {
		return errors.New("database backup removal tree binding is invalid")
	}
	opened, err := child.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(
		validateBackupRemovalBinding(
			path, root, leaf, opened, expected, fileidentity.ObjectTypeDirectory,
		),
		opened.Close(),
	)
}

func requireMissingBackupRemovalRootLeaf(root *os.Root, leaf string) error {
	if root == nil || !validBackupPathComponent(leaf) {
		return errors.New("database backup removal missing-path proof is invalid")
	}
	_, err := root.Lstat(leaf)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("database backup removal path is not absent")
}
