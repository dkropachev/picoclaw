//go:build windows

package sqliteprovider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

type stagedRetirementPlatform struct {
	parentPath  string
	path        string
	leaf        string
	quarantine  string
	parent      *os.File
	stage       *os.File
	deleteStage *os.File
	parentID    fileidentity.Identity
	identity    fileidentity.Identity
	unlinked    bool
}

var stagedRetirementReOpenFile = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

type windowsStagedRetirementRenameInformation struct {
	flags          uint32
	rootDirectory  windows.Handle
	fileNameLength uint32
	fileName       [1]uint16
}

type windowsStagedRetirementDispositionInformation struct {
	flags uint32
}

type windowsStagedRetirementStandardInformation struct {
	allocationSize int64
	endOfFile      int64
	numberOfLinks  uint32
	deletePending  byte
	directory      byte
	padding        [2]byte
}

func retainStagedGenerationPlatform(
	path string,
) (result *stagedRetirementPlatform, returnErr error) {
	parentPath := filepath.Dir(path)
	parent, err := openWindowsStagedRetirementParent(parentPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, parent.Close())
		}
	}()
	parentID, parentType, err := fileidentity.Opened(parent)
	if err != nil || parentType != fileidentity.ObjectTypeDirectory || !parentID.Valid() {
		return nil, errors.Join(
			errors.New("SQLite staged retirement parent identity is unsafe"),
			err,
		)
	}
	stage, err := openWindowsStagedRetirementFile(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, stage.Close())
		}
	}()
	identity, err := validateWindowsStagedRetirementFile(stage, fileidentity.Identity{}, false)
	if err != nil {
		return nil, err
	}
	result = &stagedRetirementPlatform{
		parentPath: parentPath,
		path:       path,
		leaf:       filepath.Base(path),
		parent:     parent,
		stage:      stage,
		parentID:   parentID,
		identity:   identity,
	}
	if err := result.validateParent(); err != nil {
		return nil, err
	}
	if err := result.validateOriginal(); err != nil {
		return nil, err
	}
	return result, nil
}

func (retained *stagedRetirementPlatform) retire(ctx context.Context) error {
	if retained == nil || retained.parent == nil || retained.stage == nil ||
		!retained.parentID.Valid() || !retained.identity.Valid() {
		return errors.New("SQLite staged retirement handles are unavailable")
	}
	if retained.unlinked {
		return retained.finishUnlinked()
	}
	if retained.quarantine == "" {
		if err := retained.validateOriginal(); err != nil {
			return err
		}
		if retained.deleteStage == nil {
			deleteStage, err := reopenWindowsStagedRetirementDeleteHandle(
				retained.stage,
				retained.identity,
			)
			if err != nil {
				return err
			}
			retained.deleteStage = deleteStage
		} else if _, err := validateWindowsStagedRetirementFile(
			retained.deleteStage,
			retained.identity,
			false,
		); err != nil {
			return err
		}
		if err := retained.validateOriginal(); err != nil {
			return err
		}
		quarantine, err := unusedStagedRetirementLeaf(retained.quarantineAvailable)
		if err != nil {
			return err
		}
		if err := context.Cause(ctx); err != nil {
			return err
		}
		// The rename and handle disposition form one synchronous retirement
		// critical section. Cancellation is honored immediately before it, not
		// after the retained identity has left its original name.
		if err := renameWindowsStagedRetirementHandle(
			retained.parent,
			retained.deleteStage,
			quarantine,
		); err != nil {
			return fmt.Errorf("quarantine SQLite staged retirement identity: %w", err)
		}
		retained.quarantine = quarantine
	}
	if err := retained.validateQuarantine(); err != nil {
		return err
	}
	if err := disposeWindowsStagedRetirementHandle(retained.deleteStage); err != nil {
		if pendingErr := retained.requireHandleDeletionPending(); pendingErr == nil {
			retained.unlinked = true
			return errors.Join(
				fmt.Errorf("dispose quarantined SQLite stage: %w", err),
				retained.finishUnlinked(),
			)
		}
		return fmt.Errorf("dispose quarantined SQLite stage: %w", err)
	}
	retained.unlinked = true
	return retained.finishUnlinked()
}

func (retained *stagedRetirementPlatform) validateOriginal() error {
	return retained.check(retained.path)
}

func (retained *stagedRetirementPlatform) check(path string) error {
	if filepath.Dir(path) != retained.parentPath {
		return errors.Join(
			errProviderGenerationTransition,
			errors.New("SQLite staged retention path has a different parent"),
		)
	}
	if err := retained.validateParent(); err != nil {
		return err
	}
	if _, err := validateWindowsStagedRetirementFile(
		retained.stage,
		retained.identity,
		false,
	); err != nil {
		return err
	}
	current, err := openWindowsStagedRetirementFile(path)
	if err != nil {
		return errors.Join(
			errProviderGenerationTransition,
			errors.New("SQLite staged retirement path is unavailable"),
			err,
		)
	}
	defer current.Close()
	if _, err := validateWindowsStagedRetirementFile(
		current,
		retained.identity,
		false,
	); err != nil {
		return errors.Join(
			errProviderGenerationTransition,
			errors.New("SQLite staged retirement path no longer names its retained identity"),
			err,
		)
	}
	return nil
}

func (retained *stagedRetirementPlatform) validateQuarantine() error {
	if retained.quarantine == "" {
		return errors.New("SQLite staged retirement quarantine is unavailable")
	}
	if err := retained.validateParent(); err != nil {
		return err
	}
	if _, err := validateWindowsStagedRetirementFile(
		retained.stage,
		retained.identity,
		false,
	); err != nil {
		return err
	}
	if err := retained.requireMissing(retained.path); err != nil {
		return errors.Join(
			errors.New("SQLite staged retirement source name remains after quarantine"),
			err,
		)
	}
	quarantinePath := filepath.Join(retained.parentPath, retained.quarantine)
	current, err := openWindowsStagedRetirementFile(quarantinePath)
	if err != nil {
		return fmt.Errorf("open quarantined SQLite stage: %w", err)
	}
	defer current.Close()
	if _, err := validateWindowsStagedRetirementFile(
		current,
		retained.identity,
		false,
	); err != nil {
		return errors.Join(
			errProviderGenerationTransition,
			errors.New("SQLite staged retirement quarantine does not name its retained identity"),
			err,
		)
	}
	return nil
}

func (retained *stagedRetirementPlatform) validateParent() error {
	identity, objectType, err := fileidentity.Opened(retained.parent)
	if err != nil || identity != retained.parentID || objectType != fileidentity.ObjectTypeDirectory {
		return errors.Join(
			errProviderGenerationTransition,
			errors.New("SQLite staged retirement parent handle changed"),
			err,
		)
	}
	if err := validateWindowsStagedRetirementHandleType(
		windows.Handle(retained.parent.Fd()),
		true,
	); err != nil {
		return err
	}
	currentID, currentType, exists, err := fileidentity.ExistingWithType(retained.parentPath)
	if err != nil || !exists || currentID != retained.parentID ||
		currentType != fileidentity.ObjectTypeDirectory {
		return errors.Join(
			errProviderGenerationTransition,
			errors.New("SQLite staged retirement parent path changed"),
			err,
		)
	}
	return nil
}

func (retained *stagedRetirementPlatform) quarantineAvailable(leaf string) (bool, error) {
	_, _, exists, err := fileidentity.ExistingWithType(filepath.Join(retained.parentPath, leaf))
	if err != nil {
		return false, err
	}
	return !exists, nil
}

func (retained *stagedRetirementPlatform) requireMissing(path string) error {
	_, _, exists, err := fileidentity.ExistingWithType(path)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("SQLite staged retirement path unexpectedly exists")
	}
	return nil
}

func (retained *stagedRetirementPlatform) requireHandleDeletionPending() error {
	file := retained.deleteStage
	if file == nil {
		return errors.New("SQLite staged retirement delete handle is unavailable")
	}
	identity, objectType, err := fileidentity.Opened(file)
	if err != nil || identity != retained.identity || objectType != fileidentity.ObjectTypeRegular {
		return errors.Join(
			errors.New("SQLite staged retirement retained identity changed after disposition"),
			err,
		)
	}
	var standard windowsStagedRetirementStandardInformation
	if err := windows.GetFileInformationByHandleEx(
		windows.Handle(file.Fd()),
		windows.FileStandardInfo,
		(*byte)(unsafe.Pointer(&standard)),
		uint32(unsafe.Sizeof(standard)),
	); err != nil {
		return err
	}
	if standard.deletePending == 0 || standard.directory != 0 {
		return errors.New("SQLite staged retirement handle is not deletion-pending")
	}
	return nil
}

func (retained *stagedRetirementPlatform) finishUnlinked() error {
	return errors.Join(
		retained.requireHandleDeletionPending(),
		retained.requireMissing(retained.path),
		retained.requireMissing(filepath.Join(retained.parentPath, retained.quarantine)),
		retained.validateParent(),
	)
}

func (retained *stagedRetirementPlatform) close() error {
	if retained == nil {
		return nil
	}
	var result error
	if retained.deleteStage != nil {
		result = errors.Join(result, retained.deleteStage.Close())
		retained.deleteStage = nil
	}
	if retained.stage != nil {
		result = errors.Join(result, retained.stage.Close())
		retained.stage = nil
	}
	if retained.parent != nil {
		result = errors.Join(result, retained.parent.Close())
		retained.parent = nil
	}
	return result
}

func openWindowsStagedRetirementParent(path string) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS|
			windows.FILE_FLAG_WRITE_THROUGH,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("retain SQLite staged retirement parent: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("SQLite staged retirement parent handle is unavailable")
	}
	if err := validateWindowsStagedRetirementHandleType(handle, true); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func openWindowsStagedRetirementFile(path string) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_WRITE_THROUGH,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("retain SQLite staged retirement identity: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("SQLite staged retirement identity handle is unavailable")
	}
	return file, nil
}

func reopenWindowsStagedRetirementDeleteHandle(
	original *os.File,
	expected fileidentity.Identity,
) (*os.File, error) {
	if original == nil || !expected.Valid() {
		return nil, errors.New("SQLite staged retirement reopen input is invalid")
	}
	result, _, callErr := stagedRetirementReOpenFile.Call(
		original.Fd(),
		uintptr(windows.DELETE|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE),
		uintptr(windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE),
		uintptr(windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_WRITE_THROUGH),
	)
	handle := windows.Handle(result)
	if handle == windows.InvalidHandle {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return nil, callErr
		}
		return nil, syscall.EINVAL
	}
	file := os.NewFile(uintptr(handle), original.Name())
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("SQLite staged retirement delete handle is unavailable")
	}
	if _, err := validateWindowsStagedRetirementFile(file, expected, false); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func validateWindowsStagedRetirementFile(
	file *os.File,
	expected fileidentity.Identity,
	deletionPending bool,
) (fileidentity.Identity, error) {
	if file == nil {
		return fileidentity.Identity{}, errors.New("SQLite staged retirement file handle is unavailable")
	}
	identity, objectType, err := fileidentity.Opened(file)
	if err != nil || objectType != fileidentity.ObjectTypeRegular || !identity.Valid() ||
		expected.Valid() && identity != expected {
		return fileidentity.Identity{}, errors.Join(
			errProviderGenerationTransition,
			errors.New("SQLite staged retirement file identity changed"),
			err,
		)
	}
	if err := validateWindowsStagedRetirementHandleType(
		windows.Handle(file.Fd()),
		false,
	); err != nil {
		return fileidentity.Identity{}, err
	}
	var standard windowsStagedRetirementStandardInformation
	if err := windows.GetFileInformationByHandleEx(
		windows.Handle(file.Fd()),
		windows.FileStandardInfo,
		(*byte)(unsafe.Pointer(&standard)),
		uint32(unsafe.Sizeof(standard)),
	); err != nil {
		return fileidentity.Identity{}, err
	}
	if standard.numberOfLinks != 1 || standard.directory != 0 ||
		(standard.deletePending != 0) != deletionPending {
		return fileidentity.Identity{}, errors.Join(
			errProviderGenerationTransition,
			errors.New("SQLite staged retirement file link state is unsafe"),
		)
	}
	return identity, nil
}

func validateWindowsStagedRetirementHandleType(handle windows.Handle, directory bool) error {
	fileType, err := windows.GetFileType(handle)
	if err != nil {
		return err
	}
	if fileType != windows.FILE_TYPE_DISK {
		return errors.New("SQLite staged retirement handle is not a disk object")
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return err
	}
	if information.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|
		windows.FILE_ATTRIBUTE_DEVICE) != 0 ||
		(information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite staged retirement handle type is unsafe"),
		)
	}
	var tag providerWindowsFileAttributeTagInfo
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&tag)),
		uint32(unsafe.Sizeof(tag)),
	); err != nil {
		return err
	}
	if tag.fileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || tag.reparseTag != 0 {
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite staged retirement handle is a reparse point"),
		)
	}
	return nil
}

func renameWindowsStagedRetirementHandle(
	parent *os.File,
	stage *os.File,
	target string,
) error {
	if parent == nil || stage == nil || target == "" || filepath.Base(target) != target {
		return errors.New("SQLite staged retirement rename is invalid")
	}
	name, err := windows.UTF16FromString(target)
	if err != nil {
		return err
	}
	nameBytes := (len(name) - 1) * 2
	var layout windowsStagedRetirementRenameInformation
	buffer := make([]byte, int(unsafe.Offsetof(layout.fileName))+nameBytes)
	information := (*windowsStagedRetirementRenameInformation)(unsafe.Pointer(&buffer[0]))
	information.rootDirectory = windows.Handle(parent.Fd())
	information.fileNameLength = uint32(nameBytes)
	copy(
		unsafe.Slice(&information.fileName[0], nameBytes/2),
		name[:len(name)-1],
	)
	var status windows.IO_STATUS_BLOCK
	if err := windows.NtSetInformationFile(
		windows.Handle(stage.Fd()),
		&status,
		&buffer[0],
		uint32(len(buffer)),
		windows.FileRenameInformation,
	); err != nil {
		if status, ok := err.(windows.NTStatus); ok {
			return status.Errno()
		}
		return err
	}
	return nil
}

func disposeWindowsStagedRetirementHandle(stage *os.File) error {
	if stage == nil {
		return errors.New("SQLite staged retirement disposition handle is unavailable")
	}
	information := windowsStagedRetirementDispositionInformation{
		flags: windows.FILE_DISPOSITION_DELETE |
			windows.FILE_DISPOSITION_POSIX_SEMANTICS |
			windows.FILE_DISPOSITION_FORCE_IMAGE_SECTION_CHECK |
			windows.FILE_DISPOSITION_IGNORE_READONLY_ATTRIBUTE,
	}
	return windows.SetFileInformationByHandle(
		windows.Handle(stage.Fd()),
		windows.FileDispositionInfoEx,
		(*byte)(unsafe.Pointer(&information)),
		uint32(unsafe.Sizeof(information)),
	)
}
