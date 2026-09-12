//go:build windows

package sqlitestore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

type sealedAbsentLegacyRootPlatform struct {
	ancestor *os.File
	identity fileidentity.Identity
}

type sealedAbsentWindowsAttributeTagInformation struct {
	attributes uint32
	reparseTag uint32
}

func captureSealedAbsentLegacyRootPlatform(
	ctx context.Context,
	path string,
) (_ *sealedAbsentLegacyRootPlatform, _ string, _ []string, returnErr error) {
	rootPath, components, err := sealedAbsentWindowsPathComponents(path)
	if err != nil {
		return nil, "", nil, err
	}
	current, err := openSealedAbsentWindowsVolumeRoot(rootPath)
	if err != nil {
		return nil, "", nil, err
	}
	currentPath := rootPath
	defer func() {
		if returnErr != nil && current != nil {
			returnErr = errors.Join(returnErr, current.Close())
		}
	}()

	for index, component := range components {
		if err := context.Cause(ctx); err != nil {
			return nil, "", nil, err
		}
		next, openErr := openSealedAbsentWindowsDirectoryAt(current, component)
		if sealedAbsentWindowsNameMissing(openErr) {
			identity, identityErr := sealedAbsentWindowsPrivateDirectoryIdentity(current)
			if identityErr != nil {
				return nil, "", nil, identityErr
			}
			platform := &sealedAbsentLegacyRootPlatform{
				ancestor: current,
				identity: identity,
			}
			current = nil
			return platform, filepath.Clean(currentPath), append([]string(nil), components[index:]...), nil
		}
		if openErr != nil {
			return nil, "", nil, fmt.Errorf(
				"inspect sealed absent legacy root component without following reparse points: %w",
				openErr,
			)
		}
		if closeErr := current.Close(); closeErr != nil {
			return nil, "", nil, errors.Join(closeErr, next.Close())
		}
		current = next
		currentPath = filepath.Join(currentPath, component)
	}
	return nil, "", nil, errors.New("sealed absent legacy root unexpectedly exists")
}

func revalidateSealedAbsentLegacyRootPlatform(
	ctx context.Context,
	_ string,
	ancestorPath string,
	suffix []string,
	platform *sealedAbsentLegacyRootPlatform,
) error {
	if ctx == nil || platform == nil || platform.ancestor == nil ||
		!platform.identity.Valid() || len(suffix) == 0 {
		return errors.New("sealed absent legacy root Windows proof is unavailable")
	}
	if err := validateSealedAbsentWindowsRetainedAncestor(platform); err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := validateSealedAbsentWindowsNamedAncestor(ctx, ancestorPath, platform.identity); err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := requireSealedAbsentWindowsName(platform.ancestor, suffix[0]); err != nil {
		return err
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := validateSealedAbsentWindowsNamedAncestor(ctx, ancestorPath, platform.identity); err != nil {
		return err
	}
	return validateSealedAbsentWindowsRetainedAncestor(platform)
}

func closeSealedAbsentLegacyRootPlatform(platform *sealedAbsentLegacyRootPlatform) error {
	if platform == nil || platform.ancestor == nil {
		return nil
	}
	ancestor := platform.ancestor
	platform.ancestor = nil
	return ancestor.Close()
}

func sealedAbsentWindowsPathComponents(path string) (string, []string, error) {
	normalized := strings.ReplaceAll(path, "/", `\`)
	volume := filepath.VolumeName(normalized)
	lowerVolume := strings.ToLower(volume)
	if volume == "" || strings.HasPrefix(lowerVolume, `\\?\`) ||
		strings.HasPrefix(lowerVolume, `\\.\`) || strings.HasPrefix(lowerVolume, `\??\`) {
		return "", nil, errors.New("sealed absent legacy root Windows volume is invalid")
	}
	if strings.HasPrefix(volume, `\\`) {
		volumeParts := strings.Split(strings.TrimPrefix(volume, `\\`), `\`)
		if len(volumeParts) != 2 ||
			!validSealedAbsentWindowsComponent(volumeParts[0]) ||
			!validSealedAbsentWindowsComponent(volumeParts[1]) {
			return "", nil, errors.New("sealed absent legacy root UNC volume is invalid")
		}
	} else if len(volume) != 2 || volume[1] != ':' ||
		(volume[0] < 'A' || volume[0] > 'Z') && (volume[0] < 'a' || volume[0] > 'z') {
		return "", nil, errors.New("sealed absent legacy root drive volume is invalid")
	}
	rootPath := volume + `\`
	relative, err := filepath.Rel(rootPath, normalized)
	if err != nil || relative == "." || filepath.IsAbs(relative) ||
		relative == ".." || strings.HasPrefix(relative, `..\`) {
		return "", nil, errors.Join(
			errors.New("sealed absent legacy root Windows path escapes its volume"),
			err,
		)
	}
	components := strings.Split(relative, `\`)
	if len(components) == 0 || len(components) > maximumSealedAbsentLegacyComponents {
		return "", nil, errors.New("sealed absent legacy root Windows component count is invalid")
	}
	for _, component := range components {
		if !validSealedAbsentWindowsComponent(component) {
			return "", nil, errors.New("sealed absent legacy root Windows component is ambiguous")
		}
	}
	return filepath.Clean(rootPath), components, nil
}

func validSealedAbsentWindowsComponent(component string) bool {
	if !validSealedAbsentLegacyComponent(component) ||
		component != strings.TrimRight(component, " .") ||
		strings.ContainsAny(component, `<>:"/\|?*`) ||
		sealedAbsentWindowsShortNameLike(component) {
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

func sealedAbsentWindowsShortNameLike(component string) bool {
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

func openSealedAbsentWindowsVolumeRoot(path string) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open sealed absent legacy volume root: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("sealed absent legacy volume root handle is unavailable")
	}
	if _, err := sealedAbsentWindowsDirectoryIdentity(file, false); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func openSealedAbsentWindowsDirectoryAt(parent *os.File, component string) (*os.File, error) {
	if parent == nil || !validSealedAbsentWindowsComponent(component) {
		return nil, errors.New("sealed absent legacy Windows directory lookup is invalid")
	}
	return openSealedAbsentWindowsObjectAt(parent, component, true)
}

func openSealedAbsentWindowsObjectAt(
	parent *os.File,
	component string,
	directory bool,
) (*os.File, error) {
	objectName, err := windows.NewNTUnicodeString(component)
	if err != nil {
		return nil, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		RootDirectory: windows.Handle(parent.Fd()),
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	attributes.Length = uint32(unsafe.Sizeof(*attributes))
	options := uint32(
		windows.FILE_OPEN_REPARSE_POINT |
			windows.FILE_OPEN_FOR_BACKUP_INTENT |
			windows.FILE_SYNCHRONOUS_IO_NONALERT,
	)
	access := uint32(windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE)
	if directory {
		options |= windows.FILE_DIRECTORY_FILE
		access |= windows.FILE_LIST_DIRECTORY | windows.READ_CONTROL
	}
	var status windows.IO_STATUS_BLOCK
	var handle windows.Handle
	err = windows.NtCreateFile(
		&handle,
		access,
		attributes,
		&status,
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		windows.FILE_OPEN,
		options,
		0,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), component)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, windows.ERROR_INVALID_HANDLE
	}
	if directory {
		if _, err := sealedAbsentWindowsDirectoryIdentity(file, false); err != nil {
			return nil, errors.Join(err, file.Close())
		}
	}
	return file, nil
}

func sealedAbsentWindowsNameMissing(err error) bool {
	if err == nil {
		return false
	}
	status, ok := err.(windows.NTStatus)
	if ok && (status == windows.STATUS_OBJECT_NAME_NOT_FOUND || status == windows.STATUS_NO_SUCH_FILE) {
		return true
	}
	return errors.Is(err, syscall.ERROR_FILE_NOT_FOUND)
}

func sealedAbsentWindowsDirectoryIdentity(
	file *os.File,
	private bool,
) (fileidentity.Identity, error) {
	if file == nil {
		return fileidentity.Identity{}, errors.New("sealed absent legacy Windows directory is unavailable")
	}
	identity, objectType, err := fileidentity.Opened(file)
	if err != nil || objectType != fileidentity.ObjectTypeDirectory || !identity.Valid() {
		return fileidentity.Identity{}, errors.Join(
			errors.New("sealed absent legacy Windows directory identity is unsafe"),
			err,
		)
	}
	handle := windows.Handle(file.Fd())
	var tag sealedAbsentWindowsAttributeTagInformation
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&tag)),
		uint32(unsafe.Sizeof(tag)),
	); err != nil {
		return fileidentity.Identity{}, err
	}
	if tag.attributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT|
		windows.FILE_ATTRIBUTE_DEVICE) != windows.FILE_ATTRIBUTE_DIRECTORY || tag.reparseTag != 0 {
		return fileidentity.Identity{}, errors.New(
			"sealed absent legacy Windows directory is a reparse point or unsafe object",
		)
	}
	if private {
		if err := validateSealedAbsentWindowsPrivateDirectory(handle); err != nil {
			return fileidentity.Identity{}, err
		}
	}
	return identity, nil
}

func sealedAbsentWindowsPrivateDirectoryIdentity(file *os.File) (fileidentity.Identity, error) {
	return sealedAbsentWindowsDirectoryIdentity(file, true)
}

func validateSealedAbsentWindowsPrivateDirectory(handle windows.Handle) error {
	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		return errors.Join(
			errors.New("sealed absent legacy Windows ancestor security is unavailable"),
			err,
		)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return errors.Join(
			errors.New("sealed absent legacy Windows ancestor owner is unavailable"),
			err,
		)
	}
	owner, ownerDefaulted, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.IsValid() || ownerDefaulted ||
		!owner.Equals(user.User.Sid) {
		return errors.Join(
			errors.New("sealed absent legacy Windows ancestor owner is unsafe"),
			err,
		)
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 ||
		control&windows.SE_DACL_PROTECTED == 0 {
		return errors.Join(
			errors.New("sealed absent legacy Windows ancestor DACL is not protected"),
			err,
		)
	}
	dacl, defaulted, err := descriptor.DACL()
	if err != nil || dacl == nil || defaulted || dacl.AceCount != 1 {
		return errors.Join(
			errors.New("sealed absent legacy Windows ancestor DACL is not owner-only"),
			err,
		)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil || ace == nil ||
		ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
		ace.Mask&windows.GENERIC_ALL == 0 ||
		!(*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(user.User.Sid) {
		return errors.Join(
			errors.New("sealed absent legacy Windows ancestor DACL grants another principal"),
			err,
		)
	}
	return nil
}

func validateSealedAbsentWindowsRetainedAncestor(platform *sealedAbsentLegacyRootPlatform) error {
	identity, err := sealedAbsentWindowsPrivateDirectoryIdentity(platform.ancestor)
	if err != nil || identity != platform.identity {
		return errors.Join(
			errors.New("sealed absent legacy retained Windows ancestor identity or type changed"),
			err,
		)
	}
	return nil
}

func validateSealedAbsentWindowsNamedAncestor(
	ctx context.Context,
	path string,
	expected fileidentity.Identity,
) (returnErr error) {
	if ctx == nil {
		return errors.New("sealed absent legacy named-ancestor context is unavailable")
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	rootPath, components, err := sealedAbsentWindowsPathComponentsAllowRoot(path)
	if err != nil {
		return err
	}
	current, err := openSealedAbsentWindowsVolumeRoot(rootPath)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, current.Close()) }()
	for _, component := range components {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		next, openErr := openSealedAbsentWindowsDirectoryAt(current, component)
		if openErr != nil {
			return fmt.Errorf("reopen sealed absent legacy named Windows ancestor: %w", openErr)
		}
		if closeErr := current.Close(); closeErr != nil {
			_ = next.Close()
			return closeErr
		}
		current = next
	}
	if err := context.Cause(ctx); err != nil {
		return err
	}
	identity, err := sealedAbsentWindowsPrivateDirectoryIdentity(current)
	if err != nil || identity != expected {
		return errors.Join(
			errors.New("sealed absent legacy named Windows ancestor identity or type changed"),
			err,
		)
	}
	return nil
}

func sealedAbsentWindowsPathComponentsAllowRoot(path string) (string, []string, error) {
	normalized := strings.ReplaceAll(path, "/", `\`)
	volume := filepath.VolumeName(normalized)
	rootPath := filepath.Clean(volume + `\`)
	if strings.EqualFold(filepath.Clean(normalized), rootPath) {
		if _, _, err := sealedAbsentWindowsPathComponents(filepath.Join(rootPath, "probe")); err != nil {
			return "", nil, err
		}
		return rootPath, nil, nil
	}
	return sealedAbsentWindowsPathComponents(normalized)
}

func requireSealedAbsentWindowsName(parent *os.File, component string) error {
	if parent == nil || !validSealedAbsentWindowsComponent(component) {
		return errors.New("sealed absent legacy Windows missing-name probe is invalid")
	}
	file, err := openSealedAbsentWindowsObjectAt(parent, component, false)
	if sealedAbsentWindowsNameMissing(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect sealed absent legacy missing Windows component: %w", err)
	}
	closeErr := file.Close()
	return errors.Join(
		errors.New("sealed absent legacy root or missing Windows ancestor appeared"),
		closeErr,
	)
}
