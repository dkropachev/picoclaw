package databasemigration

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/pkg/fileutil"
)

// openPinnedBackupPath resolves one child relative to a pinned parent handle,
// then proves the path and opened descriptor still name the same physical
// object. Callers own the returned descriptor.
func openPinnedBackupPath(path string, directory bool) (*os.File, os.FileInfo, error) {
	root, leaf, err := openPinnedBackupParent(path)
	if err != nil {
		return nil, nil, err
	}
	closeRoot := func(cause error) error { return errors.Join(cause, root.Close()) }
	before, err := root.Lstat(leaf)
	if err != nil || before == nil || before.Mode()&os.ModeSymlink != 0 ||
		before.IsDir() != directory || !directory && !before.Mode().IsRegular() {
		return nil, nil, closeRoot(errors.Join(
			errors.New("database backup open target is unsafe"), err,
		))
	}
	wantType := fileidentity.ObjectTypeRegular
	if directory {
		wantType = fileidentity.ObjectTypeDirectory
	}
	pathIdentity, pathType, exists, err := fileidentity.ExistingWithType(path)
	if err != nil || !exists || pathType != wantType {
		return nil, nil, closeRoot(errors.Join(
			errors.New("database backup open target identity is unavailable"), err,
		))
	}
	file, err := openPinnedBackupChild(root, leaf)
	if err != nil {
		return nil, nil, closeRoot(err)
	}
	fail := func(cause error) (*os.File, os.FileInfo, error) {
		return nil, nil, errors.Join(cause, file.Close(), root.Close())
	}
	opened, err := file.Stat()
	openedIdentity, openedType, openedIdentityErr := fileidentity.Opened(file)
	if err != nil || openedIdentityErr != nil || openedIdentity != pathIdentity ||
		openedType != wantType || opened.IsDir() != directory ||
		!directory && !opened.Mode().IsRegular() {
		return fail(errors.Join(
			errors.New("database backup target changed while opening"), err, openedIdentityErr,
		))
	}
	after, err := root.Lstat(leaf)
	if err != nil || after.Mode()&os.ModeSymlink != 0 {
		return fail(errors.Join(errors.New("database backup target changed after opening"), err))
	}
	afterIdentity, afterType, exists, err := fileidentity.ExistingWithType(path)
	if err != nil || !exists || afterType != wantType || afterIdentity != pathIdentity {
		return fail(errors.Join(errors.New("database backup target identity changed while opening"), err))
	}
	if err := root.Close(); err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, opened, nil
}

func createPinnedBackupFile(path string, flag int, mode os.FileMode) (*os.File, error) {
	root, leaf, err := openPinnedBackupParent(path)
	if err != nil {
		return nil, err
	}
	file, err := root.OpenFile(leaf, flag, mode)
	if err != nil {
		return nil, errors.Join(err, root.Close())
	}
	if err := root.Close(); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func createPinnedBackupDirectory(path string, mode os.FileMode) (returnErr error) {
	root, leaf, err := openPinnedBackupParent(path)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	if mkdirErr := root.Mkdir(leaf, mode); mkdirErr != nil {
		return mkdirErr
	}
	created, err := root.Lstat(leaf)
	if err != nil || created == nil || !created.IsDir() || created.Mode()&os.ModeSymlink != 0 {
		return errors.Join(errors.New("database backup created directory is unsafe"), err)
	}
	return nil
}

func openPinnedBackupParent(path string) (*os.Root, string, error) {
	if !validBackupAbsolutePath(path) {
		return nil, "", errors.New("database backup child path is invalid")
	}
	parentPath, leaf := filepath.Dir(path), filepath.Base(path)
	if !validBackupPathComponent(leaf) {
		return nil, "", errors.New("database backup child path has an invalid leaf")
	}
	if err := validateBackupAncestors(parentPath); err != nil {
		return nil, "", fmt.Errorf("validate database backup child ancestors: %w", err)
	}
	parentBefore, err := os.Lstat(parentPath)
	if err != nil || parentBefore == nil || !parentBefore.IsDir() ||
		parentBefore.Mode()&os.ModeSymlink != 0 {
		return nil, "", errors.Join(errors.New("database backup child parent is unsafe"), err)
	}
	parentIdentity, parentType, parentExists, err := fileidentity.ExistingWithType(parentPath)
	if err != nil || !parentExists || parentType != fileidentity.ObjectTypeDirectory {
		return nil, "", errors.Join(
			errors.New("database backup child parent identity is unavailable"), err,
		)
	}
	root, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, "", err
	}
	parentFile, err := root.Open(".")
	if err != nil {
		return nil, "", errors.Join(err, root.Close())
	}
	openedIdentity, openedType, openedErr := fileidentity.Opened(parentFile)
	afterIdentity, afterType, afterExists, identityErr := fileidentity.ExistingWithType(parentPath)
	closeErr := parentFile.Close()
	if openedErr != nil || identityErr != nil || closeErr != nil || !afterExists ||
		openedType != fileidentity.ObjectTypeDirectory || afterType != fileidentity.ObjectTypeDirectory ||
		openedIdentity != parentIdentity || afterIdentity != parentIdentity {
		return nil, "", errors.Join(
			errors.New("database backup child parent changed while pinning"),
			openedErr, identityErr, closeErr, root.Close(),
		)
	}
	return root, leaf, nil
}

func writePrivateBackupFileExclusive(
	path string,
	payload []byte,
	mode os.FileMode,
) (returnErr error) {
	return writePrivateBackupFileExclusiveWithOps(
		path, payload, mode, defaultBackupControlWriteOps(),
	)
}

type backupControlWriteOps struct {
	create   func(string, int, os.FileMode) (*os.File, error)
	stat     func(*os.File) (os.FileInfo, error)
	identity func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error)
	opened   func(*os.File) (fileidentity.Identity, fileidentity.ObjectType, error)
	write    func(*os.File, []byte) (int, error)
	chmod    func(*os.File, os.FileMode) error
	sync     func(*os.File) error
	close    func(*os.File) error
	secure   func(string) error
	lstat    func(string) (os.FileInfo, error)
	remove   func(string, fileidentity.Identity) error
	syncDir  func(string) error
}

func defaultBackupControlWriteOps() backupControlWriteOps {
	return backupControlWriteOps{
		create:   createPinnedBackupFile,
		stat:     func(file *os.File) (os.FileInfo, error) { return file.Stat() },
		identity: fileidentity.ExistingWithType,
		opened:   fileidentity.Opened,
		write:    func(file *os.File, payload []byte) (int, error) { return file.Write(payload) },
		chmod:    func(file *os.File, mode os.FileMode) error { return file.Chmod(mode) },
		sync:     func(file *os.File) error { return file.Sync() },
		close:    func(file *os.File) error { return file.Close() },
		secure:   secureAndValidateBackupFile,
		lstat:    os.Lstat, remove: removePinnedBackupFile, syncDir: fileutil.SyncDirectory,
	}
}

func writePrivateBackupFileExclusiveWithOps(
	path string,
	payload []byte,
	mode os.FileMode,
	ops backupControlWriteOps,
) (returnErr error) {
	file, err := ops.create(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	createdIdentity, createdType, identityErr := ops.opened(file)
	if identityErr != nil || createdType != fileidentity.ObjectTypeRegular ||
		!createdIdentity.Valid() {
		return errors.Join(
			errors.New("database backup control file handle identity is unsafe"),
			identityErr, ops.close(file),
		)
	}
	keep := false
	closed := false
	defer func() {
		if !closed {
			returnErr = errors.Join(returnErr, ops.close(file))
		}
		if !keep {
			returnErr = errors.Join(
				returnErr, ops.remove(path, createdIdentity), ops.syncDir(filepath.Dir(path)),
			)
		}
	}()
	if _, statErr := ops.stat(file); statErr != nil {
		return statErr
	}
	pathIdentity, pathType, exists, pathIdentityErr := ops.identity(path)
	if pathIdentityErr != nil || !exists || pathType != fileidentity.ObjectTypeRegular ||
		createdIdentity != pathIdentity {
		return errors.Join(
			errors.New("database backup control file identity is unsafe"), pathIdentityErr,
		)
	}
	written, err := ops.write(file, payload)
	if err != nil || written != len(payload) {
		return errors.Join(err, io.ErrShortWrite)
	}
	if chmodErr := ops.chmod(file, mode); chmodErr != nil {
		return chmodErr
	}
	if syncErr := ops.sync(file); syncErr != nil {
		return syncErr
	}
	if closeErr := ops.close(file); closeErr != nil {
		closed = true
		return closeErr
	}
	closed = true
	if secureErr := ops.secure(path); secureErr != nil {
		return secureErr
	}
	secured, err := ops.lstat(path)
	securedIdentity, securedType, securedExists, securedIdentityErr := ops.identity(path)
	if err != nil || securedIdentityErr != nil || !securedExists || secured == nil ||
		!secured.Mode().IsRegular() || securedType != fileidentity.ObjectTypeRegular ||
		securedIdentity != createdIdentity {
		return errors.Join(
			errors.New("database backup control file changed while securing"),
			err, securedIdentityErr,
		)
	}
	if err := ops.syncDir(filepath.Dir(path)); err != nil {
		return err
	}
	keep = true
	return nil
}

func removePinnedBackupFile(path string, expected fileidentity.Identity) (returnErr error) {
	if !validBackupAbsolutePath(path) || !expected.Valid() {
		return errors.New("database backup file removal input is invalid")
	}
	currentIdentity, currentType, exists, identityErr := fileidentity.ExistingWithType(path)
	if identityErr != nil {
		return identityErr
	}
	if !exists {
		return fileutil.SyncDirectory(filepath.Dir(path))
	}
	if currentType != fileidentity.ObjectTypeRegular || currentIdentity != expected {
		return errors.New("database backup file removal target changed")
	}
	root, _, err := openPinnedBackupParent(path)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	parentPath := filepath.Dir(path)
	parentFile, err := root.Open(".")
	if err != nil {
		return err
	}
	parentIdentity, parentType, openedErr := fileidentity.Opened(parentFile)
	closeErr := parentFile.Close()
	if openedErr != nil || closeErr != nil || parentType != fileidentity.ObjectTypeDirectory {
		return errors.Join(errors.New("database backup file removal parent is unsafe"), openedErr, closeErr)
	}
	tombstone, err := reserveBackupTreeRemovalPath(parentPath)
	if err != nil {
		return err
	}
	if matchErr := matchBackupRemovalIdentity(
		parentPath, parentIdentity, fileidentity.ObjectTypeDirectory, fileidentity.ExistingWithType,
	); matchErr != nil {
		return matchErr
	}
	if matchErr := matchBackupRemovalIdentity(
		path, expected, fileidentity.ObjectTypeRegular, fileidentity.ExistingWithType,
	); matchErr != nil {
		return matchErr
	}
	if publishErr := publishBackupDirectory(path, tombstone); publishErr != nil {
		return publishErr
	}
	if syncErr := fileutil.SyncDirectory(parentPath); syncErr != nil {
		return syncErr
	}
	if missingErr := requireMissingBackupRemovalPath(path, fileidentity.ExistingWithType); missingErr != nil {
		return missingErr
	}
	if matchErr := matchBackupRemovalIdentity(
		tombstone, expected, fileidentity.ObjectTypeRegular, fileidentity.ExistingWithType,
	); matchErr != nil {
		return errors.Join(errors.New("database backup file tombstone identity changed"), matchErr)
	}
	tombstoneLeaf := filepath.Base(tombstone)
	file, _, err := openPinnedBackupPath(tombstone, false)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	openedIdentity, openedType, openedErr := fileidentity.Opened(file)
	if openedErr != nil || openedType != fileidentity.ObjectTypeRegular ||
		openedIdentity != expected {
		return errors.Join(
			errors.New("database backup file tombstone handle identity changed"),
			openedErr,
		)
	}
	if err := matchBackupRemovalIdentity(
		tombstone, expected, fileidentity.ObjectTypeRegular, fileidentity.ExistingWithType,
	); err != nil {
		return errors.Join(errors.New("database backup file tombstone identity changed"), err)
	}
	if err := removeBackupFileDurable(tombstone, root, tombstoneLeaf, file, expected); err != nil {
		return err
	}
	if err := requireMissingBackupRemovalPath(tombstone, fileidentity.ExistingWithType); err != nil {
		return err
	}
	return requireMissingBackupRemovalPath(path, fileidentity.ExistingWithType)
}

type backupTreeRemovalOps struct {
	identity   func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error)
	opened     func(*os.File) (fileidentity.Identity, fileidentity.ObjectType, error)
	reserve    func(string) (string, error)
	rename     func(string, string) error
	syncDir    func(string) error
	removeTree func(string, *os.Root, string, *os.Root, fileidentity.Identity) error
}

func defaultBackupTreeRemovalOps() backupTreeRemovalOps {
	return backupTreeRemovalOps{
		identity: fileidentity.ExistingWithType,
		opened:   fileidentity.Opened,
		reserve:  reserveBackupTreeRemovalPath,
		rename:   publishBackupDirectory,
		syncDir:  fileutil.SyncDirectory,
		removeTree: func(
			path string,
			root *os.Root,
			leaf string,
			child *os.Root,
			expected fileidentity.Identity,
		) error {
			return removeBackupTreeDurable(path, root, leaf, child, expected)
		},
	}
}

func reserveBackupTreeRemovalPath(parent string) (string, error) {
	tombstone, err := os.MkdirTemp(parent, ".database-backup-remove-")
	if err != nil {
		return "", err
	}
	if err := os.Remove(tombstone); err != nil {
		return "", err
	}
	return tombstone, nil
}

func removePinnedBackupTree(path string) error {
	if !validBackupAbsolutePath(path) {
		return errors.New("database backup removal path is invalid")
	}
	identity, objectType, exists, err := fileidentity.ExistingWithType(path)
	if err != nil {
		return err
	}
	if !exists {
		return fileutil.SyncDirectory(filepath.Dir(path))
	}
	if objectType != fileidentity.ObjectTypeDirectory {
		return errors.New("database backup removal target is not a directory")
	}
	return removePinnedBackupTreeIdentity(path, identity)
}

// removePinnedBackupTreeIdentity removes only the directory generation named by
// expected. The original name is first atomically quarantined to a unique
// sibling; recursive deletion is permitted only after both the tombstone name
// and an opened tombstone handle resolve to the expected directory identity.
func removePinnedBackupTreeIdentity(path string, expected fileidentity.Identity) error {
	return removePinnedBackupTreeWithOps(path, expected, defaultBackupTreeRemovalOps())
}

func removePinnedBackupTreeWithOps(
	path string,
	expected fileidentity.Identity,
	ops backupTreeRemovalOps,
) (returnErr error) {
	if !validBackupAbsolutePath(path) {
		return errors.New("database backup removal path is invalid")
	}
	if !expected.Valid() || ops.identity == nil || ops.opened == nil || ops.reserve == nil ||
		ops.rename == nil || ops.syncDir == nil || ops.removeTree == nil {
		return errors.New("database backup removal identity or operations are invalid")
	}
	before, beforeType, exists, identityErr := ops.identity(path)
	if errors.Is(identityErr, os.ErrNotExist) || identityErr == nil && !exists {
		return ops.syncDir(filepath.Dir(path))
	}
	if identityErr != nil || beforeType != fileidentity.ObjectTypeDirectory || before != expected {
		return errors.Join(errors.New("database backup removal target identity changed"), identityErr)
	}
	root, leaf, openErr := openPinnedBackupParent(path)
	if openErr != nil {
		return openErr
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	parentPath := filepath.Dir(path)
	parentFile, parentOpenErr := root.Open(".")
	if parentOpenErr != nil {
		return parentOpenErr
	}
	parentIdentity, parentType, openedErr := ops.opened(parentFile)
	closeErr := parentFile.Close()
	if openedErr != nil || closeErr != nil || parentType != fileidentity.ObjectTypeDirectory {
		return errors.Join(
			errors.New("database backup removal parent handle is unsafe"), openedErr, closeErr,
		)
	}
	parentMatchErr := matchBackupRemovalIdentity(
		parentPath, parentIdentity, fileidentity.ObjectTypeDirectory, ops.identity,
	)
	if parentMatchErr != nil {
		return errors.Join(errors.New("database backup removal parent identity changed"), parentMatchErr)
	}
	sourceRoot, openedIdentity, openedType, sourceOpenErr := openedBackupRemovalDirectory(
		root, leaf, ops.opened,
	)
	if sourceOpenErr != nil || openedType != fileidentity.ObjectTypeDirectory ||
		openedIdentity != expected {
		if sourceRoot != nil {
			sourceOpenErr = errors.Join(sourceOpenErr, sourceRoot.Close())
		}
		return errors.Join(
			errors.New("database backup removal target handle identity changed"),
			sourceOpenErr,
		)
	}
	if closeErr := sourceRoot.Close(); closeErr != nil {
		return closeErr
	}

	tombstone, reserveErr := ops.reserve(parentPath)
	if reserveErr != nil {
		return reserveErr
	}
	tombstone = filepath.Clean(tombstone)
	if !validBackupAbsolutePath(tombstone) || filepath.Clean(filepath.Dir(tombstone)) != parentPath ||
		!validBackupPathComponent(filepath.Base(tombstone)) {
		return errors.New("database backup removal tombstone path is invalid")
	}
	if _, _, tombstoneExists, identityErr := ops.identity(tombstone); identityErr != nil || tombstoneExists {
		return errors.Join(errors.New("database backup removal tombstone is not vacant"), identityErr)
	}
	parentMatchErr = matchBackupRemovalIdentity(
		parentPath, parentIdentity, fileidentity.ObjectTypeDirectory, ops.identity,
	)
	if parentMatchErr != nil {
		return errors.Join(errors.New("database backup removal parent identity changed"), parentMatchErr)
	}
	targetMatchErr := matchBackupRemovalIdentity(
		path, expected, fileidentity.ObjectTypeDirectory, ops.identity,
	)
	if targetMatchErr != nil {
		return errors.Join(errors.New("database backup removal target identity changed"), targetMatchErr)
	}
	renameErr := ops.rename(path, tombstone)
	if renameErr != nil {
		return renameErr
	}
	if syncErr := ops.syncDir(parentPath); syncErr != nil {
		return syncErr
	}

	// From this point forward, every error deliberately leaves the quarantined
	// tree in place. Moving it back could replace a new occupant of the original
	// name, while recursively deleting it without proof could delete a substitute.
	sourceMissingErr := requireMissingBackupRemovalPath(path, ops.identity)
	if sourceMissingErr != nil {
		return errors.Join(
			errors.New("database backup removal source name remained after quarantine"),
			sourceMissingErr,
		)
	}
	tombstoneMatchErr := matchBackupRemovalIdentity(
		tombstone, expected, fileidentity.ObjectTypeDirectory, ops.identity,
	)
	if tombstoneMatchErr != nil {
		return errors.Join(
			errors.New("database backup removal tombstone identity changed"), tombstoneMatchErr,
		)
	}
	tombstoneLeaf := filepath.Base(tombstone)
	tombstoneRoot, openedIdentity, openedType, tombstoneOpenErr := openedBackupRemovalDirectory(
		root, tombstoneLeaf, ops.opened,
	)
	if tombstoneOpenErr != nil || openedType != fileidentity.ObjectTypeDirectory ||
		openedIdentity != expected {
		if tombstoneRoot != nil {
			tombstoneOpenErr = errors.Join(tombstoneOpenErr, tombstoneRoot.Close())
		}
		return errors.Join(
			errors.New("database backup removal tombstone handle identity changed"),
			tombstoneOpenErr,
		)
	}
	defer func() { returnErr = errors.Join(returnErr, tombstoneRoot.Close()) }()
	tombstoneMatchErr = matchBackupRemovalIdentity(
		tombstone, expected, fileidentity.ObjectTypeDirectory, ops.identity,
	)
	if tombstoneMatchErr != nil {
		return errors.Join(
			errors.New("database backup removal tombstone identity changed"), tombstoneMatchErr,
		)
	}
	parentMatchErr = matchBackupRemovalIdentity(
		parentPath, parentIdentity, fileidentity.ObjectTypeDirectory, ops.identity,
	)
	if parentMatchErr != nil {
		return errors.Join(errors.New("database backup removal parent identity changed"), parentMatchErr)
	}
	sourceMissingErr = requireMissingBackupRemovalPath(path, ops.identity)
	if sourceMissingErr != nil {
		return errors.Join(errors.New("database backup removal source name changed"), sourceMissingErr)
	}
	removeErr := ops.removeTree(tombstone, root, tombstoneLeaf, tombstoneRoot, expected)
	if removeErr != nil {
		return removeErr
	}
	tombstoneMissingErr := requireMissingBackupRemovalPath(tombstone, ops.identity)
	if tombstoneMissingErr != nil {
		return errors.Join(errors.New("database backup removal tombstone remains"), tombstoneMissingErr)
	}
	sourceMissingErr = requireMissingBackupRemovalPath(path, ops.identity)
	if sourceMissingErr != nil {
		return errors.Join(errors.New("database backup removal source name changed"), sourceMissingErr)
	}
	return nil
}

func openedBackupRemovalDirectory(
	parent *os.Root,
	leaf string,
	lookup func(*os.File) (fileidentity.Identity, fileidentity.ObjectType, error),
) (*os.Root, fileidentity.Identity, fileidentity.ObjectType, error) {
	directory, err := parent.OpenRoot(leaf)
	if err != nil {
		return nil, fileidentity.Identity{}, 0, err
	}
	file, err := directory.Open(".")
	if err != nil {
		return nil, fileidentity.Identity{}, 0, errors.Join(err, directory.Close())
	}
	identity, objectType, identityErr := lookup(file)
	if closeErr := file.Close(); identityErr != nil || closeErr != nil {
		return nil, fileidentity.Identity{}, 0, errors.Join(
			identityErr, closeErr, directory.Close(),
		)
	}
	return directory, identity, objectType, nil
}

func removePinnedBackupTreeContents(
	rootPath string,
	root *os.Root,
	identities map[fileidentity.Identity]string,
	entries *int,
) (returnErr error) {
	return removePinnedBackupTreeContentsWithSync(
		rootPath, root, identities, entries, fileutil.SyncDirectory,
	)
}

func removePinnedBackupTreeContentsWithSync(
	rootPath string,
	root *os.Root,
	identities map[fileidentity.Identity]string,
	entries *int,
	syncDir func(string) error,
) (returnErr error) {
	if !validBackupAbsolutePath(rootPath) || root == nil || identities == nil || entries == nil {
		return errors.New("database backup tree-content removal input is invalid")
	}
	if syncDir == nil {
		return errors.New("database backup removal directory sync is unavailable")
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, directory.Close()) }()
	for {
		batch, readErr := directory.ReadDir(128)
		for _, entry := range batch {
			*entries++
			if *entries > backupMaxEntries || !validBackupPathComponent(entry.Name()) {
				return errors.New("database backup removal tree entry limit is exceeded")
			}
			childPath := filepath.Join(rootPath, entry.Name())
			identity, objectType, exists, identityErr := fileidentity.ExistingWithType(childPath)
			if identityErr != nil || !exists {
				return errors.Join(
					errors.New("database backup removal child identity is unavailable"), identityErr,
				)
			}
			if previous, duplicate := identities[identity]; duplicate {
				return fmt.Errorf(
					"database backup removal paths %q and %q physically alias",
					previous, childPath,
				)
			}
			identities[identity] = childPath
			switch objectType {
			case fileidentity.ObjectTypeDirectory:
				childRoot, openedIdentity, openedType, openErr := openedBackupRemovalDirectory(
					root, entry.Name(), fileidentity.Opened,
				)
				if openErr != nil || openedType != fileidentity.ObjectTypeDirectory ||
					openedIdentity != identity {
					if childRoot != nil {
						openErr = errors.Join(openErr, childRoot.Close())
					}
					return errors.Join(
						errors.New("database backup removal child directory changed while opening"),
						openErr,
					)
				}
				removeErr := removePinnedBackupTreeContentsWithSync(
					childPath, childRoot, identities, entries, syncDir,
				)
				if removeErr == nil {
					removeErr = matchBackupRemovalIdentity(
						childPath, identity, fileidentity.ObjectTypeDirectory,
						fileidentity.ExistingWithType,
					)
				}
				if removeErr == nil {
					removeErr = root.Remove(entry.Name())
				}
				removeErr = errors.Join(removeErr, childRoot.Close())
				if removeErr != nil {
					return removeErr
				}
			case fileidentity.ObjectTypeRegular:
				file, openErr := openPinnedBackupChild(root, entry.Name())
				if openErr != nil {
					return openErr
				}
				openedIdentity, openedType, openedErr := fileidentity.Opened(file)
				if openedErr == nil && (openedType != fileidentity.ObjectTypeRegular ||
					openedIdentity != identity) {
					openedErr = errors.New("database backup removal child file identity changed")
				}
				if openedErr == nil {
					openedErr = matchBackupRemovalIdentity(
						childPath, identity, fileidentity.ObjectTypeRegular,
						fileidentity.ExistingWithType,
					)
				}
				if openedErr == nil {
					openedErr = root.Remove(entry.Name())
				}
				openedErr = errors.Join(openedErr, file.Close())
				if openedErr != nil {
					return openedErr
				}
			default:
				return errors.New("database backup removal tree contains an unsafe object")
			}
			if missingErr := requireMissingBackupRemovalPath(
				childPath, fileidentity.ExistingWithType,
			); missingErr != nil {
				return errors.Join(
					errors.New("database backup removal child name remains"), missingErr,
				)
			}
			if syncErr := syncDir(rootPath); syncErr != nil {
				return syncErr
			}
		}
		if errors.Is(readErr, io.EOF) {
			// A retry can observe no entries after a prior unlink succeeded but
			// its directory sync failed. Re-sync before reporting completion.
			return syncDir(rootPath)
		}
		if readErr != nil {
			return readErr
		}
		if len(batch) == 0 {
			return errors.New("database backup removal directory read made no progress")
		}
	}
}

func matchBackupRemovalIdentity(
	path string,
	expected fileidentity.Identity,
	expectedType fileidentity.ObjectType,
	lookup func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error),
) error {
	identity, objectType, exists, err := lookup(path)
	if err != nil || !exists || objectType != expectedType || identity != expected {
		return errors.Join(errors.New("database backup removal identity does not match"), err)
	}
	return nil
}

func requireMissingBackupRemovalPath(
	path string,
	lookup func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error),
) error {
	_, _, exists, err := lookup(path)
	if err != nil || exists {
		return errors.Join(errors.New("database backup removal path is not absent"), err)
	}
	return nil
}
