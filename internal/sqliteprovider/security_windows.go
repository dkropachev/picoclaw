//go:build windows

package sqliteprovider

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func secureProviderDirectory(path string) error { return secureWindowsProviderPath(path, true) }
func secureProviderFile(path string) error      { return secureWindowsProviderPath(path, false) }

func secureWindowsProviderPath(path string, directory bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || directory != info.IsDir() || !directory && !info.Mode().IsRegular() {
		return errors.New("SQLite provider security boundary is unsafe")
	}
	attributes, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(path))
	if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("SQLite provider security boundary is a reparse point")
	}
	current, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || current == nil || current.User.Sid == nil || !current.User.Sid.IsValid() {
		return errors.New("SQLite provider Windows owner is unavailable")
	}
	existing, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	owner, _, err := existing.Owner()
	if err != nil || owner == nil || !owner.Equals(current.User.Sid) {
		return errors.New("SQLite provider security boundary is owned by another user")
	}
	flags := ""
	if directory {
		flags = "OICI"
	}
	descriptor, err := windows.SecurityDescriptorFromString(
		"O:" + current.User.Sid.String() + "D:P(A;" + flags + ";GA;;;" + current.User.Sid.String() + ")",
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
	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|
			windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner,
		nil,
		dacl,
		nil,
	)
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
