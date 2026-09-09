package databasemigration

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

var (
	errExactBackupMountBoundary = errors.New("database backup tree crosses a mount boundary")
	errExactBackupMountUnknown  = errors.New("database backup mount identity is unavailable")
)

// exactBackupMountIdentity names one mounted filesystem instance, rather than
// only its backing device. The mountPoint component distinguishes same-device
// bind/null mounts on platforms whose fsid alone identifies the backing store.
type exactBackupMountIdentity struct {
	mechanism  string
	value      [2]uint64
	mountPoint string
}

func validateExactBackupMountIdentity(root, child exactBackupMountIdentity) error {
	if !root.valid() || !child.valid() || root.mechanism != child.mechanism {
		return errExactBackupMountUnknown
	}
	if root != child {
		return errExactBackupMountBoundary
	}
	return nil
}

func (identity exactBackupMountIdentity) valid() bool {
	if identity.mechanism == "" || identity.value == ([2]uint64{}) {
		return false
	}
	switch identity.mechanism {
	case "bsd-fstatfs", "netbsd-fstatvfs", "openbsd-fstatfs":
		return identity.mountPoint != ""
	default:
		return true
	}
}

func exactBackupMountName(value []byte) (string, bool) {
	index := bytes.IndexByte(value, 0)
	if index <= 0 {
		return "", false
	}
	return string(value[:index]), true
}

func openExactBackupChildByMountIdentity(
	root *os.Root,
	relative string,
	identity func(*os.File) (exactBackupMountIdentity, error),
) (result *os.File, returnErr error) {
	rootFile, err := openPinnedBackupChild(root, ".")
	if err != nil {
		return nil, fmt.Errorf("open database backup root mount descriptor: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, rootFile.Close())
		if returnErr != nil && result != nil {
			returnErr = errors.Join(returnErr, result.Close())
			result = nil
		}
	}()

	rootIdentity, err := identity(rootFile)
	if err != nil {
		return nil, fmt.Errorf("inspect database backup root mount: %w", err)
	}
	child, err := openPinnedBackupChild(root, relative)
	if err != nil {
		return nil, err
	}
	closeChild := true
	defer func() {
		if closeChild {
			returnErr = errors.Join(returnErr, child.Close())
		}
	}()
	childIdentity, err := identity(child)
	if err != nil {
		return nil, fmt.Errorf("inspect database backup child mount: %w", err)
	}
	if err := validateExactBackupMountIdentity(rootIdentity, childIdentity); err != nil {
		return nil, err
	}
	closeChild = false
	result = child
	return result, nil
}

func validateExactBackupRootMount(rootPath string, rootFile *os.File) (returnErr error) {
	parent, err := openExactBackupRoot(filepath.Dir(rootPath))
	if err != nil {
		return fmt.Errorf("open database backup root parent: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, parent.Close()) }()
	throughParent, err := openExactBackupChild(parent, filepath.Base(filepath.Clean(rootPath)))
	if err != nil {
		return fmt.Errorf("open database backup root through parent: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, throughParent.Close()) }()
	parentIdentity, parentType, parentErr := fileidentity.Opened(throughParent)
	rootIdentity, rootType, rootErr := fileidentity.Opened(rootFile)
	mountErr := validateExactBackupOpenedMount(rootFile, throughParent)
	if parentErr != nil || rootErr != nil || mountErr != nil ||
		parentType != fileidentity.ObjectTypeDirectory || rootType != fileidentity.ObjectTypeDirectory ||
		parentIdentity != rootIdentity {
		return errors.Join(
			errors.New("database backup root mount or identity changed while opening"),
			parentErr, rootErr, mountErr,
		)
	}
	return nil
}
