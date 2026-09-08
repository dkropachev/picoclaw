//go:build windows

package sqliteprovider

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func makeProviderDirectories(path string, mode os.FileMode) error {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	ancestor := absolute
	missing := make([]string, 0, 4)
	for {
		info, statErr := os.Lstat(ancestor)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return errors.New("SQLite provider directory ancestor is unsafe")
			}
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return errors.New("SQLite provider directory has no existing ancestor")
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = parent
	}
	_, parentFile, err := openPinnedProviderWindowsDirectory(ancestor)
	if err != nil {
		return err
	}
	defer func() { _ = parentFile.Close() }()
	current := ancestor
	for index := len(missing) - 1; index >= 0; index-- {
		candidate := filepath.Join(current, missing[index])
		if !pinnedProviderWindowsDirectoryMatches(parentFile, current) {
			return errors.New("SQLite provider directory ancestor changed before creation")
		}
		if err := createPrivateProviderWindowsDirectory(candidate); err != nil &&
			!errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return err
		}
		if !pinnedProviderWindowsDirectoryMatches(parentFile, current) {
			return errors.New("SQLite provider directory ancestor changed during creation")
		}
		if err := secureWindowsProviderPath(candidate, true); err != nil {
			return err
		}
		_, nextFile, err := openPinnedProviderWindowsDirectory(candidate)
		if err != nil {
			return err
		}
		_ = parentFile.Close()
		parentFile = nextFile
		current = candidate
	}
	return nil
}

func createPrivateProviderWindowsDirectory(path string) error {
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || current == nil || current.User.Sid == nil || !current.User.Sid.IsValid() {
		return errors.New("SQLite provider Windows owner is unavailable")
	}
	descriptor, err := windows.SecurityDescriptorFromString(
		"O:" + current.User.Sid.String() + "D:P(A;OICI;GA;;;" + current.User.Sid.String() + ")",
	)
	if err != nil {
		return err
	}
	pointer, err := providerWindowsPathPointer(path)
	if err != nil {
		return err
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	err = windows.CreateDirectory(pointer, attributes)
	runtime.KeepAlive(descriptor)
	return err
}

func openPinnedProviderWindowsDirectory(path string) (windows.Handle, *os.File, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return 0, nil, err
	}
	pointer, err := providerWindowsPathPointer(absolute)
	if err != nil {
		return 0, nil, err
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return 0, nil, err
	}
	file := os.NewFile(uintptr(handle), absolute)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return 0, nil, errors.New("SQLite provider directory handle is unavailable")
	}
	opened, statErr := file.Stat()
	current, lstatErr := os.Lstat(absolute)
	var information windows.ByHandleFileInformation
	infoErr := windows.GetFileInformationByHandle(handle, &information)
	if statErr != nil || lstatErr != nil || infoErr != nil || opened == nil || current == nil ||
		!opened.IsDir() || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(opened, current) ||
		information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = file.Close()
		return 0, nil, errors.Join(
			errors.New("SQLite provider directory changed while opening"),
			statErr,
			lstatErr,
			infoErr,
		)
	}
	user, userErr := windows.GetCurrentProcessToken().GetTokenUser()
	if userErr != nil || user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		_ = file.Close()
		return 0, nil, errors.Join(errors.New("SQLite provider Windows owner is unavailable"), userErr)
	}
	if boundaryErr := validateWindowsProviderCreationBoundary(handle, user.User.Sid); boundaryErr != nil {
		_ = file.Close()
		return 0, nil, boundaryErr
	}
	return handle, file, nil
}

func pinnedProviderWindowsDirectoryMatches(file *os.File, path string) bool {
	if file == nil {
		return false
	}
	opened, openedErr := file.Stat()
	current, currentErr := os.Lstat(path)
	if openedErr != nil || currentErr != nil || opened == nil || current == nil ||
		!opened.IsDir() || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(opened, current) {
		return false
	}
	attributes, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(path))
	return err == nil && attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0
}

func validateWindowsProviderCreationBoundary(handle windows.Handle, current *windows.SID) error {
	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		return errors.Join(errors.New("SQLite provider directory security is invalid"), err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.Equals(current) {
		return errors.Join(errors.New("SQLite provider directory is owned by another user"), err)
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 {
		return errors.Join(errors.New("SQLite provider directory DACL is unavailable"), err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return errors.Join(errors.New("SQLite provider directory DACL is invalid"), err)
	}
	const (
		accessAllowedCompoundACE = 4
		accessAllowedObjectACE   = 5
		accessAllowedCallbackACE = 9
		accessAllowedCallbackObj = 11
		fileDeleteChild          = windows.ACCESS_MASK(0x40)
	)
	untrustedMutation := windows.ACCESS_MASK(
		windows.GENERIC_ALL|windows.GENERIC_WRITE|windows.DELETE|
			windows.WRITE_DAC|windows.WRITE_OWNER|windows.FILE_WRITE_DATA|
			windows.FILE_APPEND_DATA|windows.FILE_WRITE_EA|
			windows.FILE_WRITE_ATTRIBUTES,
	) | fileDeleteChild
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil {
			return errors.New("SQLite provider directory DACL entry is invalid")
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		switch ace.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE:
		case accessAllowedCompoundACE, accessAllowedObjectACE,
			accessAllowedCallbackACE, accessAllowedCallbackObj:
			return errors.New("SQLite provider directory DACL contains an unsupported allow entry")
		default:
			continue
		}
		trustee := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if trustee == nil || !trustee.IsValid() {
			return errors.New("SQLite provider directory DACL trustee is invalid")
		}
		if trustee.Equals(current) || trustee.IsWellKnown(windows.WinLocalSystemSid) ||
			trustee.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
			continue
		}
		if ace.Mask&untrustedMutation != 0 {
			return errors.New("SQLite provider directory is writable by another Windows principal")
		}
	}
	return nil
}

func providerWindowsPathPointer(path string) (*uint16, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	syscallPath := absolute
	if strings.HasPrefix(absolute, `\\`) {
		syscallPath = `\\?\UNC\` + strings.TrimPrefix(absolute, `\\`)
	} else if !strings.HasPrefix(absolute, `\\?\`) {
		syscallPath = `\\?\` + absolute
	}
	return windows.UTF16PtrFromString(syscallPath)
}
