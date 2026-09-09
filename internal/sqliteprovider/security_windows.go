//go:build windows

package sqliteprovider

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func validateProviderPathSyntax(path string) error {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(absolute)
	normalized := strings.ReplaceAll(absolute, "/", `\`)
	if strings.HasPrefix(volume, `\\?\`) || strings.HasPrefix(volume, `\\.\`) ||
		strings.HasPrefix(normalized, `\??\`) {
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider path uses a Windows device namespace"),
		)
	}
	remainder := strings.TrimPrefix(absolute, volume)
	for _, component := range strings.FieldsFunc(remainder, func(character rune) bool {
		return character == '/' || character == '\\'
	}) {
		if component != strings.TrimRight(component, " .") || strings.ContainsRune(component, ':') ||
			providerWindowsShortNameLike(component) || providerWindowsReservedName(component) {
			return errors.Join(
				errProviderUnsafeBoundary,
				errors.New("SQLite provider path contains an ambiguous Windows component"),
			)
		}
	}
	return nil
}

func providerWindowsShortNameLike(component string) bool {
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

func providerWindowsReservedName(component string) bool {
	base, _, _ := strings.Cut(component, ".")
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"COM¹", "COM²", "COM³",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
		"LPT¹", "LPT²", "LPT³":
		return true
	default:
		return false
	}
}

func validateProviderAncestors(path string) error {
	if err := validateProviderPathSyntax(path); err != nil {
		return err
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	for ancestor := filepath.Dir(absolute); ; ancestor = filepath.Dir(ancestor) {
		info, statErr := os.Lstat(ancestor)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return errors.Join(errProviderUnsafeBoundary, errors.New("SQLite provider ancestor is unsafe"))
			}
			attributes, attrErr := windows.GetFileAttributes(windows.StringToUTF16Ptr(ancestor))
			if attrErr != nil {
				return attrErr
			}
			if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
				return errors.Join(
					errProviderUnsafeBoundary,
					errors.New("SQLite provider ancestor is a reparse point"),
				)
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return nil
		}
	}
}

func secureProviderDirectory(path string) error { return secureWindowsProviderPath(path, true) }
func secureProviderFile(path string) error      { return secureWindowsProviderPath(path, false) }

func secureWindowsProviderPath(path string, directory bool) error {
	expected, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if expected.Mode()&os.ModeSymlink != 0 || directory != expected.IsDir() ||
		!directory && !expected.Mode().IsRegular() {
		return errors.Join(errProviderUnsafeBoundary, errors.New("SQLite provider security boundary is unsafe"))
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	openFlags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		openFlags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(
		name,
		windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		openFlags,
		0,
	)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return errors.New("SQLite provider Windows security handle is unavailable")
	}
	defer file.Close()
	opened, statErr := file.Stat()
	currentPath, lstatErr := os.Lstat(path)
	if statErr != nil || lstatErr != nil || opened == nil || currentPath == nil ||
		!os.SameFile(expected, opened) || !os.SameFile(opened, currentPath) ||
		opened.IsDir() != directory || opened.Mode()&os.ModeSymlink != 0 {
		if statErr != nil || lstatErr != nil {
			return errors.Join(
				errors.New("inspect SQLite provider Windows path while opening"), statErr, lstatErr,
			)
		}
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider Windows path changed while opening"),
		)
	}
	var attributes providerWindowsFileAttributeTagInfo
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&attributes)),
		uint32(unsafe.Sizeof(attributes)),
	); err != nil {
		return err
	}
	if attributes.fileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider security boundary is a reparse point"),
		)
	}
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || current == nil || current.User.Sid == nil || !current.User.Sid.IsValid() {
		return errors.New("SQLite provider Windows owner is unavailable")
	}
	existing, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	owner, _, err := existing.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(current.User.Sid) {
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider security boundary is owned by another user"),
		)
	}
	inheritanceFlags := ""
	if directory {
		inheritanceFlags = "OICI"
	}
	descriptor, err := windows.SecurityDescriptorFromString(
		"O:" + current.User.Sid.String() + "D:P(A;" + inheritanceFlags + ";GA;;;" + current.User.Sid.String() + ")",
	)
	if err != nil {
		return err
	}
	owner, _, err = descriptor.Owner()
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return errors.New("SQLite provider Windows DACL is unavailable")
	}
	if err := windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|
			windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		return err
	}
	if err := validateWindowsProviderHandle(handle, current.User.Sid, directory); err != nil {
		return err
	}
	secured, statErr := file.Stat()
	currentPath, lstatErr = os.Lstat(path)
	if statErr != nil || lstatErr != nil || secured == nil || currentPath == nil ||
		!os.SameFile(opened, secured) || !os.SameFile(secured, currentPath) {
		if statErr != nil || lstatErr != nil {
			return errors.Join(
				errors.New("inspect SQLite provider Windows path while securing"), statErr, lstatErr,
			)
		}
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider Windows path changed while securing"),
		)
	}
	return nil
}

func validateWindowsProviderHandle(
	handle windows.Handle,
	current *windows.SID,
	directory bool,
) error {
	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		if err != nil {
			return err
		}
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider Windows security descriptor is invalid"),
		)
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(current) {
		return errors.Join(errProviderUnsafeBoundary, errors.New("SQLite provider Windows owner is invalid"))
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider Windows DACL is not protected"),
		)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if dacl == nil || dacl.AceCount != 1 {
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider Windows DACL is not owner-only"),
		)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	const fileAllAccess = windows.ACCESS_MASK(
		windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff,
	)
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		return err
	}
	if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
		ace.Mask&windows.GENERIC_ALL == 0 && ace.Mask&fileAllAccess != fileAllAccess ||
		!(*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(current) {
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider Windows DACL entry is invalid"),
		)
	}
	if directory && ace.Header.AceFlags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) !=
		(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) {
		return errors.Join(
			errProviderUnsafeBoundary,
			errors.New("SQLite provider Windows directory DACL does not protect children"),
		)
	}
	return nil
}

type providerWindowsFileAttributeTagInfo struct {
	fileAttributes uint32
	reparseTag     uint32
}

func generationOwnedByCurrentUser(path string, _ os.FileInfo) bool {
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || current == nil || current.User.Sid == nil {
		return false
	}
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		return false
	}
	owner, _, err := descriptor.Owner()
	return err == nil && owner != nil && owner.Equals(current.User.Sid)
}
