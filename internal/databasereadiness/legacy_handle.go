package databasereadiness

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

type pinnedLegacyFile struct {
	file       *os.File
	path       string
	identity   fileidentity.Identity
	objectType fileidentity.ObjectType
	info       os.FileInfo
}

type (
	legacyIdentityLookup func(string) (fileidentity.Identity, fileidentity.ObjectType, bool, error)
	legacyOpenedLookup   func(*os.File) (fileidentity.Identity, fileidentity.ObjectType, error)
	legacyPathOpen       func(string, fileidentity.ObjectType) (*os.File, bool, error)
	legacyChildOpen      func(
		*os.File,
		string,
		string,
		fileidentity.ObjectType,
	) (*os.File, bool, error)
)

func openPinnedLegacyPath(path string) (*pinnedLegacyFile, bool, error) {
	return openPinnedLegacyPathWith(path, fileidentity.ExistingWithType, openLegacyNoFollow)
}

func openPinnedLegacyPathWith(
	path string,
	lookup legacyIdentityLookup,
	openPath legacyPathOpen,
) (*pinnedLegacyFile, bool, error) {
	if lookup == nil || openPath == nil {
		return nil, false, fmt.Errorf("%w: legacy open operations are unavailable", errLegacyIntegrity)
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, false, err
	}
	path = absolute
	identity, objectType, exists, err := lookup(path)
	if err != nil {
		return nil, exists, classifyLegacyPathIdentityError(path, err)
	}
	if !exists {
		unexpected, opened, openErr := openPath(path, fileidentity.ObjectTypeRegular)
		if unexpected != nil {
			openErr = errors.Join(
				openErr,
				fmt.Errorf("%w: missing legacy observation returned a handle", errLegacyIntegrity),
				unexpected.Close(),
			)
		}
		if openErr != nil {
			return nil, false, openErr
		}
		if opened {
			return nil, false, fmt.Errorf(
				"%w: missing legacy input materialized", errLegacyIntegrity,
			)
		}
		return nil, false, nil
	}
	file, opened, err := openPath(path, objectType)
	if err != nil || !opened {
		if err == nil {
			err = fmt.Errorf("%w: legacy input disappeared", errLegacyIntegrity)
		}
		if file != nil {
			err = errors.Join(err, file.Close())
		}
		return nil, false, err
	}
	pinned, err := bindPinnedLegacyFile(file, path, identity, objectType)
	if err != nil {
		return nil, false, errors.Join(err, file.Close())
	}
	return pinned, true, nil
}

func openPinnedLegacyChild(parent *pinnedLegacyFile, name string) (*pinnedLegacyFile, error) {
	return openPinnedLegacyChildWith(
		parent, name, fileidentity.ExistingWithType, openLegacyChildNoFollow,
	)
}

func openPinnedLegacyChildWith(
	parent *pinnedLegacyFile,
	name string,
	lookup legacyIdentityLookup,
	openChild legacyChildOpen,
) (*pinnedLegacyFile, error) {
	if parent == nil || parent.file == nil ||
		parent.objectType != fileidentity.ObjectTypeDirectory || !validLegacyComponent(name) ||
		lookup == nil || openChild == nil {
		return nil, fmt.Errorf("%w: legacy child boundary is invalid", errLegacyIntegrity)
	}
	if err := parent.revalidateNamed(); err != nil {
		return nil, err
	}
	path := filepath.Join(parent.path, name)
	identity, objectType, exists, err := lookup(path)
	if err != nil {
		return nil, classifyLegacyPathIdentityError(path, err)
	}
	if !exists {
		return nil, fmt.Errorf("%w: legacy child disappeared", errLegacyIntegrity)
	}
	file, opened, err := openChild(parent.file, path, name, objectType)
	if err != nil {
		if file != nil {
			err = errors.Join(err, file.Close())
		}
		return nil, err
	}
	if !opened {
		resultErr := fmt.Errorf("%w: legacy child disappeared", errLegacyIntegrity)
		if file != nil {
			resultErr = errors.Join(resultErr, file.Close())
		}
		return nil, resultErr
	}
	pinned, err := bindPinnedLegacyFile(file, path, identity, objectType)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if err := parent.revalidateNamed(); err != nil {
		return nil, errors.Join(err, pinned.Close())
	}
	return pinned, nil
}

func bindPinnedLegacyFile(
	file *os.File,
	path string,
	expected fileidentity.Identity,
	expectedType fileidentity.ObjectType,
) (*pinnedLegacyFile, error) {
	if file == nil || !expected.Valid() {
		return nil, fmt.Errorf("%w: legacy handle is invalid", errLegacyIntegrity)
	}
	openedIdentity, openedType, err := fileidentity.Opened(file)
	if err != nil {
		return nil, classifyLegacyIdentityError(err)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	pinned := &pinnedLegacyFile{
		file: file, path: path, identity: openedIdentity, objectType: openedType, info: info,
	}
	if openedIdentity != expected || openedType != expectedType {
		return nil, fmt.Errorf("%w: legacy input changed while opening", errLegacyIntegrity)
	}
	if err := pinned.revalidateNamed(); err != nil {
		return nil, err
	}
	return pinned, nil
}

func (file *pinnedLegacyFile) revalidateNamed() error {
	return file.revalidateNamedWith(fileidentity.Opened, openLegacyNoFollow)
}

func (file *pinnedLegacyFile) revalidateNamedWith(
	opened legacyOpenedLookup,
	reopen legacyPathOpen,
) error {
	if file == nil || file.file == nil || !file.identity.Valid() ||
		opened == nil || reopen == nil {
		return fmt.Errorf("%w: legacy handle is unavailable", errLegacyIntegrity)
	}
	openedIdentity, openedType, openedErr := opened(file.file)
	reopened, exists, reopenErr := reopen(file.path, file.objectType)
	if openedErr != nil || reopenErr != nil {
		var closeErr error
		if reopened != nil {
			closeErr = reopened.Close()
		}
		return errors.Join(
			classifyLegacyIdentityError(openedErr),
			reopenErr,
			closeErr,
		)
	}
	if !exists || reopened == nil {
		var closeErr error
		if reopened != nil {
			closeErr = reopened.Close()
		}
		return errors.Join(
			fmt.Errorf("%w: legacy input identity changed", errLegacyIntegrity),
			closeErr,
		)
	}
	reopenedIdentity, reopenedType, reopenedErr := opened(reopened)
	closeErr := reopened.Close()
	if reopenedErr != nil || closeErr != nil {
		return errors.Join(classifyLegacyIdentityError(reopenedErr), closeErr)
	}
	if openedIdentity != file.identity || reopenedIdentity != file.identity ||
		openedType != file.objectType || reopenedType != file.objectType {
		return fmt.Errorf("%w: legacy input identity changed", errLegacyIntegrity)
	}
	return nil
}

func (file *pinnedLegacyFile) Close() error {
	if file == nil || file.file == nil {
		return nil
	}
	handle := file.file
	file.file = nil
	return handle.Close()
}

func classifyLegacyIdentityError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, fileidentity.ErrUnsafeType) ||
		errors.Is(err, fileidentity.ErrInvalidPath) {
		return errors.Join(errLegacyIntegrity, err)
	}
	return err
}

func classifyLegacyPathIdentityError(path string, err error) error {
	if !errors.Is(err, fileidentity.ErrUnsafeType) {
		return classifyLegacyIdentityError(err)
	}
	info, statErr := os.Lstat(path)
	if statErr == nil && info != nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.Join(fmt.Errorf("%w: symlink", errLegacyIntegrity), err)
	}
	if statErr == nil {
		return errors.Join(fmt.Errorf("%w: non-regular input", errLegacyIntegrity), err)
	}
	return errors.Join(errLegacyIntegrity, err, statErr)
}
