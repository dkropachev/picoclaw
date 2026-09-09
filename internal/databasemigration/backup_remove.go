package databasemigration

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
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

// removePinnedEmptyBackupDirectoryIdentity rolls back a directory created by
// this backup attempt. It never traverses or removes descendants: nonempty
// state fails before quarantine, and the final directory removal is an atomic
// empty-directory operation.
func removePinnedEmptyBackupDirectoryIdentity(
	path string,
	expected fileidentity.Identity,
) (returnErr error) {
	if !validBackupAbsolutePath(path) || !expected.Valid() {
		return errors.New("database backup empty-directory rollback identity is invalid")
	}
	root, leaf, err := openPinnedBackupParent(path)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	opened, err := openExactBackupChild(root, leaf)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, opened.Close()) }()
	identity, objectType, identityErr := fileidentity.Opened(opened)
	if identityErr != nil || identity != expected || objectType != fileidentity.ObjectTypeDirectory {
		return errors.Join(
			errors.New("database backup empty-directory rollback target changed"), identityErr,
		)
	}
	child, err := openExactBackupRemovalRoot(root, leaf, opened)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, child.Close()) }()
	if err := requireEmptyBackupRemovalRoot(path, child, expected); err != nil {
		return err
	}
	ops := defaultBackupRemovalOps()
	quarantine, exact, err := quarantineBackupRemovalLeaf(
		root, leaf, opened, expected, fileidentity.ObjectTypeDirectory, path, ops,
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
	if err := validateBackupRemovalRelativeBinding(
		root, quarantine, opened, expected, fileidentity.ObjectTypeDirectory,
	); err != nil {
		return err
	}
	if err := requireMissingBackupRemovalRootLeaf(root, leaf); err != nil {
		return errors.Join(errors.New("database backup rollback source name changed"), err)
	}
	if err := requireEmptyBackupRemovalRoot(path, child, expected); err != nil {
		return err
	}
	if err := ops.remove(root, quarantine, exact, true); err != nil {
		return err
	}
	if exact != nil {
		closeErr := exact.Close()
		exact = nil
		if closeErr != nil {
			return closeErr
		}
	}
	if err := requireMissingBackupRemovalRootLeaf(root, quarantine); err != nil {
		return errors.Join(errors.New("database backup rollback quarantine remains"), err)
	}
	return ops.sync(root, filepath.Dir(path))
}

func requireEmptyBackupRemovalRoot(
	path string,
	root *os.Root,
	expected fileidentity.Identity,
) (returnErr error) {
	if err := validatePinnedBackupRemovalRoot(path, root, expected); err != nil {
		return err
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, directory.Close()) }()
	entries, readErr := directory.ReadDir(1)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	if len(entries) != 0 || !errors.Is(readErr, io.EOF) {
		return errors.New("database backup rollback directory is not empty")
	}
	identity, objectType, identityErr := fileidentity.Opened(directory)
	if identityErr != nil || identity != expected || objectType != fileidentity.ObjectTypeDirectory {
		return errors.Join(
			errors.New("database backup rollback directory changed while checking emptiness"),
			identityErr,
		)
	}
	return nil
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
			if exact != nil {
				return "", nil, errors.Join(err, exact.Close())
			}
			continue
		}
		if err != nil {
			if exact != nil {
				err = errors.Join(err, exact.Close())
			}
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
	bound, err := openExactBackupChild(root, leaf)
	if err != nil {
		return err
	}
	boundIdentity, boundType, boundErr := fileidentity.Opened(bound)
	mountErr := validateExactBackupOpenedMount(opened, bound)
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
	if boundErr != nil || mountErr != nil || boundMetadataErr != nil || boundCloseErr != nil ||
		openedErr != nil || openedMetadataErr != nil ||
		boundType != expectedType || openedType != expectedType ||
		boundIdentity != expected || openedIdentity != expected {
		return errors.Join(
			errors.New("database backup removal leaf binding changed"),
			boundErr, mountErr, boundMetadataErr, boundCloseErr, openedErr, openedMetadataErr,
		)
	}
	return nil
}

// openExactBackupRemovalRoot opens a directory Root only after the leaf has
// passed platform mount-boundary checks, then proves both handles name the
// same object on the same mounted filesystem instance.
func openExactBackupRemovalRoot(
	parent *os.Root,
	leaf string,
	opened *os.File,
) (*os.Root, error) {
	return openBackupRootFromPinnedChild(parent, leaf, opened)
}

// openBackupRootFromPinnedChild converts a nonblocking pinned directory file
// into a recursive Root without trusting a second pathname resolution.
func openBackupRootFromPinnedChild(
	parent *os.Root,
	leaf string,
	opened *os.File,
) (result *os.Root, returnErr error) {
	if parent == nil || opened == nil || !validBackupPathComponent(leaf) {
		return nil, errors.New("database backup removal child root input is invalid")
	}
	openedIdentity, openedType, openedErr := fileidentity.Opened(opened)
	if openedErr != nil || openedType != fileidentity.ObjectTypeDirectory {
		return nil, errors.Join(
			errors.New("database backup removal child root is not a directory"), openedErr,
		)
	}
	// Making leaf an intermediate component routes its open through os.Root's
	// O_DIRECTORY path on Unix. A concurrent directory-to-FIFO/device swap
	// therefore fails without blocking before the terminal immutable dot opens.
	child, err := parent.OpenRoot(leaf + string(os.PathSeparator) + ".")
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, child.Close())
			result = nil
		}
	}()
	childFile, err := child.Open(".")
	if err != nil {
		return nil, err
	}
	childIdentity, childType, childErr := fileidentity.Opened(childFile)
	mountErr := validateExactBackupOpenedMount(opened, childFile)
	closeErr := childFile.Close()
	if childErr != nil || mountErr != nil || closeErr != nil ||
		childType != fileidentity.ObjectTypeDirectory || childIdentity != openedIdentity {
		return nil, errors.Join(
			errors.New("database backup removal child root changed while opening"),
			childErr, mountErr, closeErr,
		)
	}
	return child, nil
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
