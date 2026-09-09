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
	root, leaf, err := openPinnedBackupParent(path)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	file, err := openPinnedBackupChild(root, leaf)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	return removeBackupFileDurable(path, root, leaf, file, expected)
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

func removePinnedBackupTreeIdentity(
	path string,
	expected fileidentity.Identity,
) (returnErr error) {
	if !validBackupAbsolutePath(path) || !expected.Valid() {
		return errors.New("database backup removal identity is invalid")
	}
	current, objectType, exists, err := fileidentity.ExistingWithType(path)
	if err != nil {
		return err
	}
	if !exists {
		return fileutil.SyncDirectory(filepath.Dir(path))
	}
	if objectType != fileidentity.ObjectTypeDirectory || current != expected {
		return errors.New("database backup removal target identity changed")
	}
	root, leaf, err := openPinnedBackupParent(path)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	child, err := root.OpenRoot(leaf)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, child.Close()) }()
	return removeBackupTreeDurable(path, root, leaf, child, expected)
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

func validatePinnedBackupTreeInventory(
	rootPath string,
	root *os.Root,
	expectedRoot fileidentity.Identity,
	identities map[fileidentity.Identity]string,
	entries *int,
) (returnErr error) {
	if err := validatePinnedBackupRemovalRoot(rootPath, root, expectedRoot); err != nil {
		return err
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
			info, statErr := root.Lstat(entry.Name())
			if statErr != nil || info == nil || info.Mode()&os.ModeSymlink != 0 {
				return errors.Join(errors.New("database backup removal child is unsafe"), statErr)
			}
			file, openErr := openPinnedBackupChild(root, entry.Name())
			if openErr != nil {
				return openErr
			}
			identity, objectType, identityErr := fileidentity.Opened(file)
			var metadataErr error
			if objectType == fileidentity.ObjectTypeRegular {
				info, statErr := file.Stat()
				metadataErr = errors.Join(
					statErr, validateBackupPlatformFile(info, file, 0o600),
				)
			}
			closeErr := file.Close()
			if identityErr != nil || metadataErr != nil || closeErr != nil || objectType == 0 || info.IsDir() !=
				(objectType == fileidentity.ObjectTypeDirectory) {
				return errors.Join(
					errors.New("database backup removal child is unsafe"),
					identityErr, metadataErr, closeErr,
				)
			}
			if previous, duplicate := identities[identity]; duplicate {
				return fmt.Errorf(
					"database backup removal paths %q and %q physically alias", previous, childPath,
				)
			}
			identities[identity] = childPath
			if objectType == fileidentity.ObjectTypeDirectory {
				child, err := root.OpenRoot(entry.Name())
				if err != nil {
					return err
				}
				err = validatePinnedBackupTreeInventory(
					childPath, child, identity, identities, entries,
				)
				if closeErr := child.Close(); err != nil || closeErr != nil {
					return errors.Join(err, closeErr)
				}
			} else if objectType != fileidentity.ObjectTypeRegular {
				return errors.New("database backup removal tree contains an unsafe object")
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		if len(batch) == 0 {
			return errors.New("database backup removal directory read made no progress")
		}
	}
}

func removePinnedBackupTreeContents(
	rootPath string,
	root *os.Root,
	identities map[fileidentity.Identity]string,
	entries *int,
) (returnErr error) {
	if !validBackupAbsolutePath(rootPath) || root == nil || identities == nil || entries == nil {
		return errors.New("database backup tree-content removal input is invalid")
	}
	expectedRoot, err := openedPinnedBackupRemovalRootIdentity(rootPath, root)
	registeredPath, registered := identities[expectedRoot]
	if err != nil || !registered || registeredPath != rootPath {
		return errors.Join(errors.New("database backup removal root identity is not registered"), err)
	}
	if err := matchBackupRemovalIdentity(
		rootPath, expectedRoot, fileidentity.ObjectTypeDirectory, fileidentity.ExistingWithType,
	); err != nil {
		return errors.Join(errors.New("database backup removal root path changed"), err)
	}
	validated := cloneBackupRemovalIdentities(identities)
	validatedEntries := *entries
	if err := validatePinnedBackupTreeInventory(
		rootPath, root, expectedRoot, validated, &validatedEntries,
	); err != nil {
		return err
	}
	return removePinnedBackupTreeContentsBound(
		rootPath, root, expectedRoot, validated, entries, defaultBackupRemovalOps(),
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
	expectedRoot, rootErr := openedPinnedBackupRemovalRootIdentity(rootPath, root)
	registeredPath, registered := identities[expectedRoot]
	if rootErr != nil || !registered || registeredPath != rootPath {
		return errors.Join(
			errors.New("database backup removal root identity is not registered"), rootErr,
		)
	}
	if err := matchBackupRemovalIdentity(
		rootPath, expectedRoot, fileidentity.ObjectTypeDirectory, fileidentity.ExistingWithType,
	); err != nil {
		return errors.Join(errors.New("database backup removal root path changed"), err)
	}
	validated := cloneBackupRemovalIdentities(identities)
	validatedEntries := *entries
	if err := validatePinnedBackupTreeInventory(
		rootPath, root, expectedRoot, validated, &validatedEntries,
	); err != nil {
		return err
	}
	ops := defaultBackupRemovalOps()
	ops.sync = func(root *os.Root, label string) error {
		return errors.Join(syncBackupRemovalRoot(root), syncDir(label))
	}
	return removePinnedBackupTreeContentsBound(rootPath, root, expectedRoot, validated, entries, ops)
}

func cloneBackupRemovalIdentities(
	identities map[fileidentity.Identity]string,
) map[fileidentity.Identity]string {
	clone := make(map[fileidentity.Identity]string, len(identities))
	for identity, path := range identities {
		clone[identity] = path
	}
	return clone
}

func removePinnedBackupTreeContentsBound(
	rootPath string,
	root *os.Root,
	expectedRoot fileidentity.Identity,
	identities map[fileidentity.Identity]string,
	entries *int,
	ops backupRemovalOps,
) error {
	if err := validatePinnedBackupRemovalRoot(rootPath, root, expectedRoot); err != nil {
		return err
	}
	for {
		directory, err := root.Open(".")
		if err != nil {
			return err
		}
		batch, readErr := directory.ReadDir(128)
		if closeErr := directory.Close(); readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil {
			return errors.Join(readErr, closeErr)
		}
		for _, entry := range batch {
			*entries++
			if *entries > backupMaxEntries || !validBackupPathComponent(entry.Name()) {
				return errors.New("database backup removal tree entry limit is exceeded")
			}
			childPath := filepath.Join(rootPath, entry.Name())
			info, statErr := root.Lstat(entry.Name())
			if statErr != nil || info == nil || info.Mode()&os.ModeSymlink != 0 {
				return errors.Join(errors.New("database backup removal child is unsafe"), statErr)
			}
			file, openErr := openPinnedBackupChild(root, entry.Name())
			if openErr != nil {
				return openErr
			}
			identity, objectType, identityErr := fileidentity.Opened(file)
			if identityErr != nil || objectType == 0 || info.IsDir() !=
				(objectType == fileidentity.ObjectTypeDirectory) {
				return errors.Join(identityErr, file.Close())
			}
			if plannedPath, captured := identities[identity]; !captured || plannedPath != childPath {
				return errors.Join(
					errors.New("database backup removal child was not captured by preflight"),
					file.Close(),
				)
			}
			var removeErr error
			switch objectType {
			case fileidentity.ObjectTypeDirectory:
				childRoot, err := root.OpenRoot(entry.Name())
				if err != nil {
					removeErr = err
				} else {
					removeErr = removeBackupTreeRelative(
						childPath, root, entry.Name(), childRoot, file, identity,
						identities, entries, ops,
					)
					removeErr = errors.Join(removeErr, childRoot.Close())
				}
			case fileidentity.ObjectTypeRegular:
				removeErr = removeBackupFileRelative(childPath, root, entry.Name(), file, identity, ops)
			default:
				removeErr = errors.New("database backup removal tree contains an unsafe object")
			}
			if closeErr := file.Close(); removeErr != nil || closeErr != nil {
				return errors.Join(removeErr, closeErr)
			}
			delete(identities, identity)
		}
		if len(batch) == 0 && errors.Is(readErr, io.EOF) {
			if backupRemovalPlanHasDescendant(identities, rootPath) {
				return errors.New("database backup removal captured inventory remains")
			}
			return ops.sync(root, rootPath)
		}
		if len(batch) == 0 {
			return errors.New("database backup removal directory read made no progress")
		}
	}
}

func backupRemovalPlanHasDescendant(
	identities map[fileidentity.Identity]string,
	rootPath string,
) bool {
	prefix := rootPath + string(filepath.Separator)
	for _, path := range identities {
		if len(path) > len(prefix) && path[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func validatePinnedBackupRemovalRoot(
	rootPath string,
	root *os.Root,
	expected fileidentity.Identity,
) error {
	if root == nil || !expected.Valid() {
		return errors.New("database backup removal root identity is not registered")
	}
	openedIdentity, err := openedPinnedBackupRemovalRootIdentity(rootPath, root)
	if err != nil || openedIdentity != expected {
		return errors.Join(errors.New("database backup removal root binding changed"), err)
	}
	return nil
}

func openedPinnedBackupRemovalRootIdentity(
	rootPath string,
	root *os.Root,
) (fileidentity.Identity, error) {
	if !validBackupAbsolutePath(rootPath) || root == nil {
		return fileidentity.Identity{}, errors.New("database backup removal root is unavailable")
	}
	rootFile, err := root.Open(".")
	if err != nil {
		return fileidentity.Identity{}, err
	}
	openedIdentity, openedType, openedErr := fileidentity.Opened(rootFile)
	closeErr := rootFile.Close()
	if openedErr != nil || closeErr != nil || openedType != fileidentity.ObjectTypeDirectory {
		return fileidentity.Identity{}, errors.Join(
			errors.New("database backup removal root binding changed"),
			openedErr, closeErr,
		)
	}
	return openedIdentity, nil
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
