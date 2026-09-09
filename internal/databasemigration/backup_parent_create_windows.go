//go:build windows

package databasemigration

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"golang.org/x/sys/windows"
)

func secureBackupParentCreatedDirectoryHandle(
	file *os.File,
	expected fileidentity.Identity,
) (returnErr error) {
	if err := validateBackupParentCreatedDirectoryIdentity(file, expected); err != nil {
		return err
	}
	result, _, callErr := backupRemovalReOpenFile.Call(
		file.Fd(),
		uintptr(windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER|
			windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE),
		uintptr(windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE),
		uintptr(windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS|
			windows.FILE_FLAG_WRITE_THROUGH),
	)
	handle := windows.Handle(result)
	if handle == windows.InvalidHandle {
		if callErr != nil && !errors.Is(callErr, syscall.Errno(0)) {
			return callErr
		}
		return syscall.EINVAL
	}
	secured := os.NewFile(uintptr(handle), "database-backup-parent")
	if secured == nil {
		_ = windows.CloseHandle(handle)
		return errors.New("created database backup parent security handle is unavailable")
	}
	defer func() { returnErr = errors.Join(returnErr, secured.Close()) }()
	if err := validateBackupParentCreatedDirectoryIdentity(secured, expected); err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return errors.Join(errors.New("created database backup parent owner is unavailable"), err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}, nil)
	if err != nil {
		return err
	}
	if err := windows.SetSecurityInfo(
		handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|
			windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user.User.Sid, nil, acl, nil,
	); err != nil {
		return err
	}
	if err := validateBackupParentCreatedDirectoryHandle(secured, expected); err != nil {
		return err
	}
	return validateBackupParentCreatedDirectoryIdentity(file, expected)
}

func validateBackupParentCreatedDirectoryIdentity(
	file *os.File,
	expected fileidentity.Identity,
) error {
	identity, objectType, identityErr := fileidentity.Opened(file)
	info, statErr := file.Stat()
	if identityErr != nil || statErr != nil || !expected.Valid() || identity != expected ||
		objectType != fileidentity.ObjectTypeDirectory || info == nil || !info.IsDir() {
		return errors.Join(
			errors.New("created database backup parent handle is unsafe"),
			identityErr, statErr,
		)
	}
	return nil
}

func validateBackupParentCreatedDirectoryHandle(
	file *os.File,
	expected fileidentity.Identity,
) error {
	if err := validateBackupParentCreatedDirectoryIdentity(file, expected); err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return errors.Join(errors.New("created database backup parent owner is unavailable"), err)
	}
	descriptor, err := windows.GetSecurityInfo(
		windows.Handle(file.Fd()), windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
		return errors.Join(errors.New("created database backup parent has another owner"), err)
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.Join(errors.New("created database backup parent DACL is not protected"), err)
	}
	acl, _, err := descriptor.DACL()
	if err != nil || acl == nil || acl.AceCount != 1 {
		return errors.Join(errors.New("created database backup parent DACL is not owner-only"), err)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &ace); err != nil {
		return err
	}
	if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
		ace.Mask&windows.GENERIC_ALL == 0 ||
		!(*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(user.User.Sid) {
		return fmt.Errorf("created database backup parent DACL grants another principal")
	}
	return nil
}

func syncBackupParentCreatedDirectoryHandle(
	file *os.File,
	expected fileidentity.Identity,
) error {
	// Windows makes the subsequent exact-handle rename durable with
	// FILE_FLAG_WRITE_THROUGH; directory handles do not support a Unix-style
	// fsync. Recheck the captured inherited-private handle on both sides.
	if err := validateBackupParentCreatedDirectoryHandle(file, expected); err != nil {
		return err
	}
	return validateBackupParentCreatedDirectoryHandle(file, expected)
}
