//go:build windows

package fileutil

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func SecurePrivateDirectory(path string) (os.FileInfo, error) {
	return securePrivateWindowsPath(path, true)
}

func ValidatePrivateDirectory(path string, expected os.FileInfo) error {
	return validatePrivateWindowsPath(path, expected, true)
}

func SecurePrivateFile(path string) (os.FileInfo, error) {
	return securePrivateWindowsPath(path, false)
}

func ValidatePrivateFile(path string, expected os.FileInfo) error {
	return validatePrivateWindowsPath(path, expected, false)
}

func securePrivateWindowsPath(path string, directory bool) (os.FileInfo, error) {
	expected, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	handle, file, err := openPrivateWindowsPath(path, expected, directory, true)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	inheritance := uint32(windows.NO_INHERITANCE)
	if directory {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}, nil)
	if err != nil {
		return nil, err
	}
	if err := windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		return nil, err
	}
	if err := validatePrivateWindowsHandle(handle, user.User.Sid); err != nil {
		return nil, err
	}
	secured, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if err := validatePrivateWindowsPath(path, secured, directory); err != nil {
		return nil, err
	}
	return secured, nil
}

func validatePrivateWindowsPath(path string, expected os.FileInfo, directory bool) error {
	handle, file, err := openPrivateWindowsPath(path, expected, directory, false)
	if err != nil {
		return err
	}
	defer file.Close()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	return validatePrivateWindowsHandle(handle, user.User.Sid)
}

func openPrivateWindowsPath(
	path string,
	expected os.FileInfo,
	directory,
	writeDACL bool,
) (windows.Handle, *os.File, error) {
	if expected == nil || expected.Mode()&os.ModeSymlink != 0 ||
		expected.IsDir() != directory || !directory && !expected.Mode().IsRegular() {
		return 0, nil, errors.New("private Windows path has an unsafe type")
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, nil, err
	}
	access := uint32(windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES)
	if writeDACL {
		access |= windows.WRITE_DAC
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(
		name,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		flags,
		0,
	)
	if err != nil {
		return 0, nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return 0, nil, errors.New("open private Windows path")
	}
	opened, statErr := file.Stat()
	current, lstatErr := os.Lstat(path)
	if statErr != nil || lstatErr != nil || !os.SameFile(opened, expected) ||
		!os.SameFile(opened, current) || opened.IsDir() != directory ||
		opened.Mode()&os.ModeSymlink != 0 {
		_ = file.Close()
		return 0, nil, errors.Join(
			errors.New("private Windows path changed while opening"),
			statErr,
			lstatErr,
		)
	}
	var attributes windowsFileAttributeTagInfo
	if err := windows.GetFileInformationByHandleEx(
		handle,
		windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&attributes)),
		uint32(unsafe.Sizeof(attributes)),
	); err != nil {
		_ = file.Close()
		return 0, nil, err
	}
	if attributes.fileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = file.Close()
		return 0, nil, errors.New("private Windows path is a reparse point")
	}
	return handle, file, nil
}

func validatePrivateWindowsHandle(handle windows.Handle, user *windows.SID) error {
	descriptor, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.Join(errors.New("private Windows DACL is not protected"), err)
	}
	acl, _, err := descriptor.DACL()
	if err != nil || acl == nil || acl.AceCount != 1 {
		return errors.Join(errors.New("private Windows DACL is not owner-only"), err)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &ace); err != nil {
		return err
	}
	if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
		ace.Mask&windows.GENERIC_ALL == 0 ||
		!(*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(user) {
		return fmt.Errorf("private Windows DACL grants another principal")
	}
	return nil
}

type windowsFileAttributeTagInfo struct {
	fileAttributes uint32
	reparseTag     uint32
}
