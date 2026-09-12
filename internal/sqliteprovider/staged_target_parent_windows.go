//go:build windows

package sqliteprovider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

const maximumStagedTargetParentWindowsPathComponents = 1024

type stagedTargetParentPlatform struct {
	directory *os.File
	identity  fileidentity.Identity
	created   []stagedTargetParentWindowsComponent
}

type stagedTargetParentWindowsComponent struct {
	parent    *os.File
	directory *os.File
	leaf      string
	identity  fileidentity.Identity
}

type stagedTargetParentWindowsDispositionInformation struct {
	flags uint32
}

func createRetainedStagedTargetParentPlatform(
	ctx context.Context,
	path string,
) (_ *stagedTargetParentPlatform, returnErr error) {
	if ctx == nil {
		return nil, errors.New("SQLite staged target parent Windows context is unavailable")
	}
	rootPath, components, err := stagedTargetParentWindowsPathComponents(path)
	if err != nil {
		return nil, err
	}
	current, err := openStagedTargetParentWindowsVolumeRoot(rootPath)
	if err != nil {
		return nil, err
	}
	firstMissing := -1
	for index, component := range components {
		if cause := context.Cause(ctx); cause != nil {
			return nil, errors.Join(cause, current.Close())
		}
		next, openErr := openStagedTargetParentWindowsDirectoryAt(current, component)
		if stagedTargetParentWindowsNameMissing(openErr) {
			firstMissing = index
			break
		}
		if openErr != nil {
			return nil, errors.Join(
				fmt.Errorf("inspect SQLite staged target parent Windows component: %w", openErr),
				current.Close(),
			)
		}
		if closeErr := current.Close(); closeErr != nil {
			return nil, errors.Join(closeErr, next.Close())
		}
		current = next
	}
	if firstMissing < 0 {
		return nil, errors.Join(
			errors.New("SQLite staged target parent creation ownership is ambiguous"),
			current.Close(),
		)
	}
	if len(components)-firstMissing > maximumStagedTargetParentCreatedComponents {
		return nil, errors.Join(
			errors.New("SQLite staged target parent has too many missing components"),
			current.Close(),
		)
	}
	user, descriptor, err := stagedTargetParentWindowsOwnerAndDescriptor()
	if err != nil {
		return nil, errors.Join(err, current.Close())
	}
	if err := validateWindowsProviderCreationBoundary(
		windows.Handle(current.Fd()),
		user,
	); err != nil {
		return nil, errors.Join(err, current.Close())
	}

	platform := &stagedTargetParentPlatform{}
	fail := func(cause error) (*stagedTargetParentPlatform, error) {
		if len(platform.created) == 0 {
			return nil, errors.Join(cause, current.Close())
		}
		return nil, errors.Join(
			cause,
			closeRetainedStagedTargetParentPlatform(platform, true),
		)
	}
	for _, component := range components[firstMissing:] {
		if cause := context.Cause(ctx); cause != nil {
			return fail(cause)
		}
		next, createErr := createStagedTargetParentWindowsDirectoryAt(
			current,
			component,
			descriptor,
		)
		runtime.KeepAlive(descriptor)
		if createErr != nil {
			if stagedTargetParentWindowsNameCollision(createErr) {
				createErr = errors.Join(
					errors.New("SQLite staged target parent creation ownership is ambiguous"),
					createErr,
				)
			}
			return fail(createErr)
		}
		platform.created = append(platform.created, stagedTargetParentWindowsComponent{
			parent: current, directory: next, leaf: component,
		})
		created := &platform.created[len(platform.created)-1]
		identity, identityErr := stagedTargetParentWindowsDirectoryIdentity(next, true)
		if identityErr != nil {
			return fail(identityErr)
		}
		created.identity = identity
		platform.directory = next
		platform.identity = identity
		current = next
	}
	if platform.directory == nil || !platform.identity.Valid() {
		return fail(errors.New("SQLite staged target parent Windows lineage is invalid"))
	}
	if _, err := checkRetainedStagedTargetParentPlatform(ctx, path, nil, platform); err != nil {
		return fail(err)
	}
	return platform, nil
}

func checkRetainedStagedTargetParentPlatform(
	ctx context.Context,
	path string,
	observed os.FileInfo,
	platform *stagedTargetParentPlatform,
) (result fileidentity.Identity, returnErr error) {
	if ctx == nil || platform == nil || platform.directory == nil ||
		!platform.identity.Valid() {
		return fileidentity.Identity{}, errors.New(
			"SQLite staged target parent Windows proof is unavailable",
		)
	}
	if cause := context.Cause(ctx); cause != nil {
		return fileidentity.Identity{}, cause
	}
	retainedIdentity, err := stagedTargetParentWindowsDirectoryIdentity(
		platform.directory,
		true,
	)
	if err != nil || retainedIdentity != platform.identity {
		return fileidentity.Identity{}, errors.Join(
			errors.New("SQLite staged target parent retained Windows identity changed"),
			err,
		)
	}
	named, err := openNamedStagedTargetParentWindowsDirectory(ctx, path)
	if err != nil {
		return fileidentity.Identity{}, errors.Join(
			errors.New("SQLite staged target parent named Windows identity changed"),
			err,
		)
	}
	defer func() { returnErr = errors.Join(returnErr, named.Close()) }()
	namedIdentity, identityErr := stagedTargetParentWindowsDirectoryIdentity(named, true)
	namedInfo, statErr := named.Stat()
	if identityErr != nil || statErr != nil || namedIdentity != platform.identity ||
		namedInfo == nil || !namedInfo.IsDir() {
		return fileidentity.Identity{}, errors.Join(
			errors.New("SQLite staged target parent named Windows identity changed"),
			identityErr,
			statErr,
		)
	}
	if observed != nil && (!observed.IsDir() || observed.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(namedInfo, observed) || namedInfo.Mode() != observed.Mode()) {
		return fileidentity.Identity{}, errors.New(
			"SQLite staged target parent Windows observation changed",
		)
	}
	return retainedIdentity, context.Cause(ctx)
}

func checkRetainedStagedTargetParentSoleStagePlatform(
	ctx context.Context,
	path string,
	stage string,
	stageInfo os.FileInfo,
	stageIdentity fileidentity.Identity,
	platform *stagedTargetParentPlatform,
) (fileidentity.Identity, error) {
	return checkRetainedStagedTargetParentSoleEntryWindows(
		ctx,
		path,
		stage,
		stageInfo,
		stageIdentity,
		platform,
		"stage",
		true,
	)
}

func checkRetainedStagedTargetParentSoleInstalledPlatform(
	ctx context.Context,
	path string,
	target string,
	targetInfo os.FileInfo,
	targetIdentity fileidentity.Identity,
	platform *stagedTargetParentPlatform,
) (fileidentity.Identity, error) {
	return checkRetainedStagedTargetParentSoleEntryWindows(
		ctx,
		path,
		target,
		targetInfo,
		targetIdentity,
		platform,
		"installed target",
		false,
	)
}

func checkRetainedStagedTargetParentSoleEntryWindows(
	ctx context.Context,
	path string,
	entry string,
	entryInfo os.FileInfo,
	entryIdentity fileidentity.Identity,
	platform *stagedTargetParentPlatform,
	entryKind string,
	exactMetadata bool,
) (result fileidentity.Identity, returnErr error) {
	if entryInfo == nil || !entryIdentity.Valid() || platform == nil ||
		len(platform.created) == 0 || filepath.Dir(entry) != path {
		return fileidentity.Identity{}, errors.New(
			"SQLite staged target parent Windows entry proof is unavailable",
		)
	}
	result, err := checkRetainedStagedTargetParentPlatform(ctx, path, nil, platform)
	if err != nil {
		return fileidentity.Identity{}, err
	}
	last := &platform.created[len(platform.created)-1]
	directory, err := openStagedTargetParentWindowsDirectoryAt(last.parent, last.leaf)
	if err != nil {
		return fileidentity.Identity{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, directory.Close()) }()
	directoryIdentity, err := stagedTargetParentWindowsDirectoryIdentity(directory, true)
	if err != nil || directoryIdentity != platform.identity {
		return fileidentity.Identity{}, errors.Join(
			errors.New("SQLite staged target parent Windows inventory handle changed"),
			err,
		)
	}
	entries, readErr := directory.ReadDir(2)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return fileidentity.Identity{}, readErr
	}
	leaf := filepath.Base(entry)
	if len(entries) != 1 || entries[0].Name() != leaf {
		return fileidentity.Identity{}, errors.New(
			"SQLite staged target parent does not contain exactly its pinned " + entryKind,
		)
	}
	openedEntry, err := openStagedTargetParentWindowsRegularAt(platform.directory, leaf)
	if err != nil {
		return fileidentity.Identity{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, openedEntry.Close()) }()
	openedIdentity, identityErr := validateWindowsStagedRetirementFile(
		openedEntry,
		entryIdentity,
		false,
	)
	openedInfo, statErr := openedEntry.Stat()
	if identityErr != nil || statErr != nil || openedIdentity != entryIdentity ||
		openedInfo == nil || !openedInfo.Mode().IsRegular() ||
		exactMetadata && !sameValidatedReplacementMetadata(entryInfo, openedInfo) {
		return fileidentity.Identity{}, errors.Join(
			errors.New(
				"SQLite staged target parent contains a different "+entryKind+" identity",
			),
			identityErr,
			statErr,
		)
	}
	finalIdentity, err := checkRetainedStagedTargetParentPlatform(ctx, path, nil, platform)
	if err != nil || finalIdentity != result {
		return fileidentity.Identity{}, errors.Join(
			errors.New("SQLite staged target parent changed during inventory"),
			err,
		)
	}
	return result, nil
}

func closeRetainedStagedTargetParentPlatform(
	platform *stagedTargetParentPlatform,
	rollback bool,
) (returnErr error) {
	if platform == nil {
		return nil
	}
	if rollback {
		for index := len(platform.created) - 1; index >= 0; index-- {
			if err := rollbackStagedTargetParentWindowsComponent(
				&platform.created[index],
			); err != nil {
				returnErr = errors.Join(returnErr, err)
				break
			}
		}
	}
	platform.directory = nil
	for index := len(platform.created) - 1; index >= 0; index-- {
		component := &platform.created[index]
		if component.directory != nil {
			directory := component.directory
			component.directory = nil
			returnErr = errors.Join(returnErr, directory.Close())
		}
	}
	if len(platform.created) > 0 && platform.created[0].parent != nil {
		anchor := platform.created[0].parent
		platform.created[0].parent = nil
		returnErr = errors.Join(returnErr, anchor.Close())
	}
	for index := range platform.created {
		platform.created[index].parent = nil
	}
	platform.created = nil
	platform.identity = fileidentity.Identity{}
	return returnErr
}

func rollbackStagedTargetParentWindowsComponent(
	component *stagedTargetParentWindowsComponent,
) (returnErr error) {
	if component == nil || component.parent == nil || component.directory == nil ||
		!component.identity.Valid() || !validStagedTargetParentWindowsComponent(component.leaf) {
		return errors.New("SQLite staged target parent Windows rollback proof is unavailable")
	}
	retainedIdentity, err := stagedTargetParentWindowsDirectoryIdentity(
		component.directory,
		true,
	)
	if err != nil || retainedIdentity != component.identity {
		return errors.Join(
			errors.New("SQLite staged target parent Windows rollback identity changed"),
			err,
		)
	}
	named, err := openStagedTargetParentWindowsDirectoryAt(component.parent, component.leaf)
	if err != nil {
		return errors.Join(
			errors.New("SQLite staged target parent Windows rollback name changed"),
			err,
		)
	}
	namedIdentity, identityErr := stagedTargetParentWindowsDirectoryIdentity(named, true)
	entries, readErr := named.ReadDir(1)
	closeErr := named.Close()
	if identityErr != nil || namedIdentity != component.identity ||
		readErr != nil && !errors.Is(readErr, io.EOF) || len(entries) != 0 || closeErr != nil {
		if len(entries) != 0 {
			readErr = errors.Join(
				readErr,
				errors.New("SQLite staged target parent Windows rollback directory is not empty"),
			)
		}
		return errors.Join(
			errors.New("SQLite staged target parent Windows rollback name was substituted"),
			identityErr,
			readErr,
			closeErr,
		)
	}
	information := stagedTargetParentWindowsDispositionInformation{
		flags: windows.FILE_DISPOSITION_DELETE |
			windows.FILE_DISPOSITION_POSIX_SEMANTICS |
			windows.FILE_DISPOSITION_FORCE_IMAGE_SECTION_CHECK |
			windows.FILE_DISPOSITION_IGNORE_READONLY_ATTRIBUTE,
	}
	if err := windows.SetFileInformationByHandle(
		windows.Handle(component.directory.Fd()),
		windows.FileDispositionInfoEx,
		(*byte)(unsafe.Pointer(&information)),
		uint32(unsafe.Sizeof(information)),
	); err != nil {
		return err
	}
	directory := component.directory
	component.directory = nil
	closeErr = directory.Close()
	missingErr := requireStagedTargetParentWindowsNameMissing(
		component.parent,
		component.leaf,
		true,
	)
	return errors.Join(closeErr, missingErr)
}

func replaceRetainedStagedTargetParentStagePlatform(
	ctx context.Context,
	path string,
	stage string,
	target string,
	stageInfo os.FileInfo,
	stageFile *os.File,
	stageIdentity fileidentity.Identity,
	platform *stagedTargetParentPlatform,
) (complete bool, returnErr error) {
	if ctx == nil || platform == nil || platform.directory == nil || stageFile == nil ||
		!stageIdentity.Valid() || filepath.Dir(stage) != path || filepath.Dir(target) != path {
		return false, errors.New("SQLite retained target-parent Windows replacement is invalid")
	}
	if cause := context.Cause(ctx); cause != nil {
		return false, cause
	}
	openedIdentity, identityErr := validateWindowsStagedRetirementFile(
		stageFile,
		stageIdentity,
		false,
	)
	openedStage, statErr := stageFile.Stat()
	if identityErr != nil || statErr != nil || openedIdentity != stageIdentity ||
		openedStage == nil || !sameValidatedReplacementMetadata(stageInfo, openedStage) {
		return false, errors.Join(
			errors.New("SQLite retained target-parent Windows stage handle changed"),
			identityErr,
			statErr,
		)
	}
	renameHandle, err := reopenWindowsStagedRetirementDeleteHandle(stageFile, stageIdentity)
	if err != nil {
		return false, err
	}
	defer func() { returnErr = errors.Join(returnErr, renameHandle.Close()) }()
	renameErr := renameWindowsStagedRetirementHandle(
		platform.directory,
		renameHandle,
		filepath.Base(target),
	)
	stageMatches, targetMatches, postErr := retainedTargetParentWindowsRenameState(
		platform.directory,
		filepath.Base(stage),
		filepath.Base(target),
		stageInfo,
		stageIdentity,
	)
	if renameErr != nil {
		if stageMatches && !targetMatches && postErr == nil {
			return false, renameErr
		}
		return true, errors.Join(
			errors.New("SQLite retained target-parent Windows rename outcome is ambiguous"),
			renameErr,
			postErr,
		)
	}
	if postErr != nil || stageMatches || !targetMatches {
		return true, errors.Join(
			errors.New("SQLite retained target-parent Windows rename postcondition failed"),
			postErr,
		)
	}
	if _, err := checkRetainedStagedTargetParentSoleEntryWindows(
		ctx,
		path,
		target,
		stageInfo,
		stageIdentity,
		platform,
		"target",
		true,
	); err != nil {
		return true, err
	}
	return true, nil
}

func retainedTargetParentWindowsRenameState(
	parent *os.File,
	stageLeaf string,
	targetLeaf string,
	expected os.FileInfo,
	expectedIdentity fileidentity.Identity,
) (stageMatches bool, targetMatches bool, returnErr error) {
	inspect := func(leaf string) (_ bool, _ bool, inspectErr error) {
		file, err := openStagedTargetParentWindowsRegularAt(parent, leaf)
		if stagedTargetParentWindowsNameMissing(err) {
			return false, false, nil
		}
		if err != nil {
			return false, false, err
		}
		defer func() { inspectErr = errors.Join(inspectErr, file.Close()) }()
		identity, identityErr := validateWindowsStagedRetirementFile(
			file,
			fileidentity.Identity{},
			false,
		)
		info, statErr := file.Stat()
		if identityErr != nil || statErr != nil {
			return true, false, errors.Join(identityErr, statErr)
		}
		return true,
			identity == expectedIdentity && sameValidatedReplacementMetadata(expected, info),
			nil
	}
	stageExists, stageMatches, stageErr := inspect(stageLeaf)
	targetExists, targetMatches, targetErr := inspect(targetLeaf)
	if stageExists && !stageMatches {
		stageErr = errors.Join(
			stageErr,
			errors.New("SQLite retained target-parent Windows stage name was substituted"),
		)
	}
	if targetExists && !targetMatches {
		targetErr = errors.Join(
			targetErr,
			errors.New("SQLite retained target-parent Windows target name was substituted"),
		)
	}
	return stageExists && stageMatches, targetExists && targetMatches,
		errors.Join(stageErr, targetErr)
}

func openNamedStagedTargetParentWindowsDirectory(
	ctx context.Context,
	path string,
) (_ *os.File, returnErr error) {
	rootPath, components, err := stagedTargetParentWindowsPathComponents(path)
	if err != nil {
		return nil, err
	}
	current, err := openStagedTargetParentWindowsVolumeRoot(rootPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil && current != nil {
			returnErr = errors.Join(returnErr, current.Close())
		}
	}()
	for _, component := range components {
		if cause := context.Cause(ctx); cause != nil {
			return nil, cause
		}
		next, openErr := openStagedTargetParentWindowsDirectoryAt(current, component)
		if openErr != nil {
			return nil, openErr
		}
		if closeErr := current.Close(); closeErr != nil {
			return nil, errors.Join(closeErr, next.Close())
		}
		current = next
	}
	result := current
	current = nil
	return result, nil
}

func openStagedTargetParentWindowsVolumeRoot(path string) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.FILE_LIST_DIRECTORY|windows.FILE_TRAVERSE|windows.FILE_READ_ATTRIBUTES|
			windows.READ_CONTROL|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open SQLite staged target parent Windows volume root: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		return nil, errors.Join(
			errors.New("SQLite staged target parent Windows volume handle is unavailable"),
			windows.CloseHandle(handle),
		)
	}
	if _, err := stagedTargetParentWindowsDirectoryIdentity(file, false); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func openStagedTargetParentWindowsDirectoryAt(
	parent *os.File,
	component string,
) (*os.File, error) {
	return openStagedTargetParentWindowsObjectAt(
		parent,
		component,
		true,
		windows.FILE_LIST_DIRECTORY|windows.FILE_TRAVERSE|windows.FILE_READ_ATTRIBUTES|
			windows.READ_CONTROL|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		nil,
	)
}

func openStagedTargetParentWindowsRegularAt(
	parent *os.File,
	component string,
) (*os.File, error) {
	return openStagedTargetParentWindowsObjectAt(
		parent,
		component,
		false,
		windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		nil,
	)
}

func createStagedTargetParentWindowsDirectoryAt(
	parent *os.File,
	component string,
	descriptor *windows.SECURITY_DESCRIPTOR,
) (*os.File, error) {
	if descriptor == nil || !descriptor.IsValid() {
		return nil, errors.New("SQLite staged target parent Windows descriptor is unavailable")
	}
	return openStagedTargetParentWindowsObjectAt(
		parent,
		component,
		true,
		windows.FILE_LIST_DIRECTORY|windows.FILE_TRAVERSE|windows.FILE_READ_ATTRIBUTES|
			windows.READ_CONTROL|windows.SYNCHRONIZE|windows.DELETE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		windows.FILE_CREATE,
		descriptor,
	)
}

func openStagedTargetParentWindowsObjectAt(
	parent *os.File,
	component string,
	directory bool,
	access uint32,
	share uint32,
	disposition uint32,
	descriptor *windows.SECURITY_DESCRIPTOR,
) (*os.File, error) {
	if parent == nil || !validStagedTargetParentWindowsComponent(component) {
		return nil, errors.New("SQLite staged target parent Windows lookup is invalid")
	}
	objectName, err := windows.NewNTUnicodeString(component)
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		RootDirectory:      windows.Handle(parent.Fd()),
		ObjectName:         objectName,
		Attributes:         windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
		SecurityDescriptor: descriptor,
	}
	attributes.Length = uint32(unsafe.Sizeof(*attributes))
	options := uint32(
		windows.FILE_OPEN_REPARSE_POINT |
			windows.FILE_OPEN_FOR_BACKUP_INTENT |
			windows.FILE_SYNCHRONOUS_IO_NONALERT,
	)
	fileAttributes := uint32(windows.FILE_ATTRIBUTE_NORMAL)
	if directory {
		options |= windows.FILE_DIRECTORY_FILE
		fileAttributes = windows.FILE_ATTRIBUTE_DIRECTORY
	} else {
		options |= windows.FILE_NON_DIRECTORY_FILE
	}
	var status windows.IO_STATUS_BLOCK
	var handle windows.Handle
	err = windows.NtCreateFile(
		&handle,
		access,
		attributes,
		&status,
		nil,
		fileAttributes,
		share,
		disposition,
		options,
		0,
		0,
	)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), component)
	if file == nil {
		return nil, errors.Join(windows.ERROR_INVALID_HANDLE, windows.CloseHandle(handle))
	}
	return file, nil
}

func stagedTargetParentWindowsDirectoryIdentity(
	file *os.File,
	private bool,
) (fileidentity.Identity, error) {
	if file == nil {
		return fileidentity.Identity{}, errors.New(
			"SQLite staged target parent Windows directory is unavailable",
		)
	}
	identity, objectType, err := fileidentity.Opened(file)
	if err != nil || objectType != fileidentity.ObjectTypeDirectory || !identity.Valid() {
		return fileidentity.Identity{}, errors.Join(
			errors.New("SQLite staged target parent Windows directory identity is unsafe"),
			err,
		)
	}
	handle := windows.Handle(file.Fd())
	if err := validateWindowsStagedRetirementHandleType(handle, true); err != nil {
		return fileidentity.Identity{}, err
	}
	if private {
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil || user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
			return fileidentity.Identity{}, errors.Join(
				errors.New("SQLite staged target parent Windows owner is unavailable"),
				err,
			)
		}
		if err := validateWindowsProviderHandle(handle, user.User.Sid, true); err != nil {
			return fileidentity.Identity{}, err
		}
	}
	return identity, nil
}

func stagedTargetParentWindowsOwnerAndDescriptor() (
	*windows.SID,
	*windows.SECURITY_DESCRIPTOR,
	error,
) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return nil, nil, errors.Join(
			errors.New("SQLite staged target parent Windows owner is unavailable"),
			err,
		)
	}
	descriptor, err := windows.SecurityDescriptorFromString(
		"O:" + user.User.Sid.String() + "D:P(A;OICI;GA;;;" + user.User.Sid.String() + ")",
	)
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		return nil, nil, errors.Join(
			errors.New("SQLite staged target parent Windows descriptor is unavailable"),
			err,
		)
	}
	return user.User.Sid, descriptor, nil
}

func requireStagedTargetParentWindowsNameMissing(
	parent *os.File,
	component string,
	directory bool,
) error {
	var file *os.File
	var err error
	if directory {
		file, err = openStagedTargetParentWindowsDirectoryAt(parent, component)
	} else {
		file, err = openStagedTargetParentWindowsRegularAt(parent, component)
	}
	if stagedTargetParentWindowsNameMissing(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.Join(
		errors.New("SQLite staged target parent Windows name unexpectedly exists"),
		file.Close(),
	)
}

func stagedTargetParentWindowsNameMissing(err error) bool {
	if err == nil {
		return false
	}
	status, ok := err.(windows.NTStatus)
	if ok && (status == windows.STATUS_OBJECT_NAME_NOT_FOUND ||
		status == windows.STATUS_NO_SUCH_FILE) {
		return true
	}
	return errors.Is(err, syscall.ERROR_FILE_NOT_FOUND)
}

func stagedTargetParentWindowsNameCollision(err error) bool {
	status, ok := err.(windows.NTStatus)
	return ok && status == windows.STATUS_OBJECT_NAME_COLLISION ||
		errors.Is(err, windows.ERROR_ALREADY_EXISTS) ||
		errors.Is(err, windows.ERROR_FILE_EXISTS)
}

func stagedTargetParentWindowsPathComponents(path string) (string, []string, error) {
	normalized := strings.ReplaceAll(path, "/", `\`)
	volume := filepath.VolumeName(normalized)
	lowerVolume := strings.ToLower(volume)
	if volume == "" || strings.HasPrefix(lowerVolume, `\\?\`) ||
		strings.HasPrefix(lowerVolume, `\\.\`) || strings.HasPrefix(lowerVolume, `\??\`) {
		return "", nil, errors.New("SQLite staged target parent Windows volume is invalid")
	}
	if strings.HasPrefix(volume, `\\`) {
		volumeParts := strings.Split(strings.TrimPrefix(volume, `\\`), `\`)
		if len(volumeParts) != 2 ||
			!validStagedTargetParentWindowsComponent(volumeParts[0]) ||
			!validStagedTargetParentWindowsComponent(volumeParts[1]) {
			return "", nil, errors.New("SQLite staged target parent UNC volume is invalid")
		}
	} else if len(volume) != 2 || volume[1] != ':' ||
		(volume[0] < 'A' || volume[0] > 'Z') && (volume[0] < 'a' || volume[0] > 'z') {
		return "", nil, errors.New("SQLite staged target parent drive volume is invalid")
	}
	rootPath := volume + `\`
	relative, err := filepath.Rel(rootPath, normalized)
	if err != nil || relative == "." || filepath.IsAbs(relative) ||
		relative == ".." || strings.HasPrefix(relative, `..\`) {
		return "", nil, errors.Join(
			errors.New("SQLite staged target parent Windows path escapes its volume"),
			err,
		)
	}
	components := strings.Split(relative, `\`)
	if len(components) == 0 || len(components) > maximumStagedTargetParentWindowsPathComponents {
		return "", nil, errors.New(
			"SQLite staged target parent Windows component count is invalid",
		)
	}
	for _, component := range components {
		if !validStagedTargetParentWindowsComponent(component) {
			return "", nil, errors.New(
				"SQLite staged target parent Windows component is ambiguous",
			)
		}
	}
	return filepath.Clean(rootPath), components, nil
}

func validStagedTargetParentWindowsComponent(component string) bool {
	if component == "" || component == "." || component == ".." ||
		component != strings.TrimRight(component, " .") ||
		strings.ContainsRune(component, 0) || strings.ContainsAny(component, `<>:"/\|?*`) ||
		stagedTargetParentWindowsShortNameLike(component) {
		return false
	}
	encoded := utf16.Encode([]rune(component))
	if len(encoded) == 0 || len(encoded) > 255 {
		return false
	}
	for _, character := range component {
		if character < 32 {
			return false
		}
	}
	base, _, _ := strings.Cut(component, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$", "CONIN$", "CONOUT$",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"COM¹", "COM²", "COM³",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
		"LPT¹", "LPT²", "LPT³":
		return false
	default:
		return true
	}
}

func stagedTargetParentWindowsShortNameLike(component string) bool {
	base, _, _ := strings.Cut(component, ".")
	tilde := strings.LastIndexByte(base, '~')
	if tilde <= 0 || tilde == len(base)-1 {
		return false
	}
	for _, character := range base[tilde+1:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
