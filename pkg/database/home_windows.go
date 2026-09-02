//go:build windows

package database

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	windowsCurrentProcessUserSID = currentWindowsProcessUserSID
	windowsNamedSecurityInfo     = windows.GetNamedSecurityInfo
	windowsHandleSecurityInfo    = windows.GetSecurityInfo
)

func sameCanonicalPath(first, second string) bool {
	return strings.EqualFold(filepath.Clean(first), filepath.Clean(second))
}

func validateOwnerOnlyDirectory(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return NewError(CodeIntegrity, "database state boundary is not a real directory")
	}
	descriptor, err := windowsNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("inspect database state directory security: %w", err)
	}
	return validateWindowsOwnerOnlyDescriptor(descriptor, true)
}

func validateOwnerOnlyFile(path string, info os.FileInfo, _ os.FileMode) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return NewError(CodeIntegrity, "database broker file is not regular")
	}
	descriptor, err := windowsNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("inspect database broker file security: %w", err)
	}
	return validateWindowsOwnerOnlyDescriptor(descriptor, false)
}

func validateWindowsOwnerOnlyHandle(file *os.File) error {
	if file == nil {
		return NewError(CodeIntegrity, "database broker file handle is unavailable")
	}
	descriptor, err := windowsHandleSecurityInfo(
		windows.Handle(file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("inspect database broker file handle security: %w", err)
	}
	return validateWindowsOwnerOnlyDescriptor(descriptor, false)
}

func validateWindowsOwnerOnlyDescriptor(descriptor *windows.SECURITY_DESCRIPTOR, directory bool) error {
	if descriptor == nil || !descriptor.IsValid() {
		return NewError(CodeIntegrity, "database Windows security descriptor is invalid")
	}
	current, err := windowsCurrentProcessUserSID()
	if err != nil {
		return fmt.Errorf("resolve database Windows owner: %w", err)
	}
	if current == nil || !current.IsValid() {
		return NewError(CodeUnauthorized, "current Windows owner is unavailable")
	}
	owner, ownerDefaulted, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.IsValid() {
		return NewError(CodeIntegrity, "database Windows owner descriptor is invalid")
	}
	if !owner.Equals(current) {
		return NewError(CodeUnauthorized, "database state is owned by another Windows user")
	}
	if ownerDefaulted {
		return NewError(CodeIntegrity, "database Windows owner must be explicit")
	}

	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 || control&windows.SE_DACL_PROTECTED == 0 {
		return NewError(CodeIntegrity, "database Windows DACL is not protected")
	}
	dacl, daclDefaulted, err := descriptor.DACL()
	if err != nil || dacl == nil || daclDefaulted || dacl.AceCount != 1 {
		return NewError(CodeIntegrity, "database Windows DACL is not owner-only")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil || ace == nil ||
		ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
		ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
		return NewError(CodeIntegrity, "database Windows DACL entry is invalid")
	}
	if directory && ace.Header.AceFlags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) !=
		(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) {
		return NewError(CodeIntegrity, "database Windows directory DACL does not protect children")
	}
	const fileAllAccess = windows.ACCESS_MASK(
		windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff,
	)
	if ace.Mask&windows.GENERIC_ALL == 0 && ace.Mask&fileAllAccess != fileAllAccess {
		return NewError(CodeIntegrity, "database Windows owner lacks full control")
	}
	trustee := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if trustee == nil || !trustee.IsValid() || !trustee.Equals(current) {
		return NewError(CodeIntegrity, "database Windows DACL grants another principal")
	}
	return nil
}

func currentWindowsProcessUserSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	if user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return nil, NewError(CodeUnauthorized, "current Windows user SID is unavailable")
	}
	return user.User.Sid.Copy()
}

func validateOwnerOnlySocket(string, os.FileInfo) error {
	return NewError(CodeUnsupported, "Windows named-pipe ACL validation is unavailable")
}
