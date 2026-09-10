//go:build windows

package database

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func createOwnerOnlyExclusiveFile(path string, _ os.FileMode) (*os.File, error) {
	return openWindowsOwnerOnlyFile(
		path,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.CREATE_NEW,
		true,
	)
}

func createOwnerOnlyExclusiveAppendFile(path string, _ os.FileMode) (*os.File, error) {
	return openWindowsOwnerOnlyFile(
		path,
		windows.FILE_APPEND_DATA|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL,
		windows.CREATE_NEW,
		true,
	)
}

func openOwnerOnlyAppendFile(path string, _ os.FileMode) (*os.File, error) {
	return openWindowsOwnerOnlyFile(
		path,
		windows.FILE_APPEND_DATA|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL,
		windows.OPEN_EXISTING,
		false,
	)
}

func supervisorFileLinkCount(file *os.File) (int64, error) {
	if file == nil {
		return 0, NewError(CodeIntegrity, "database supervisor file handle is unavailable")
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(
		windows.Handle(file.Fd()),
		&information,
	); err != nil {
		return 0, err
	}
	if information.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return 0, NewError(CodeIntegrity, "database supervisor file boundary is invalid")
	}
	return int64(information.NumberOfLinks), nil
}

func supervisorExecutableModeValid(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular()
}

func validateSupervisorExecutableTrust(path string, info os.FileInfo) error {
	if !supervisorExecutableModeValid(info) {
		return NewError(CodeIntegrity, "database supervisor executable trust is invalid")
	}
	return validateSupervisorWindowsMutationACL(path, false)
}

func validateSupervisorExecutableAncestorTrust(path string, info os.FileInfo) error {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return NewError(CodeIntegrity, "database supervisor executable ancestor trust is invalid")
	}
	return validateSupervisorWindowsMutationACL(path, true)
}

func validateSupervisorWindowsMutationACL(path string, directory bool) error {
	if err := rejectWindowsReparsePath(path); err != nil {
		return NewError(CodeIntegrity, "database supervisor executable boundary is a reparse point")
	}
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		return NewError(CodeIntegrity, "database supervisor executable security is unavailable")
	}
	current, err := currentWindowsProcessUserSID()
	if err != nil {
		return NewError(CodeIntegrity, "database supervisor executable trustee is unavailable")
	}
	owner, _, err := descriptor.Owner()
	if err != nil || !supervisorTrustedWindowsExecutableSID(owner, current) {
		return NewError(CodeIntegrity, "database supervisor executable owner is untrusted")
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 {
		return NewError(CodeIntegrity, "database supervisor executable DACL is unavailable")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return NewError(CodeIntegrity, "database supervisor executable DACL is invalid")
	}
	const (
		accessAllowedCompoundACE = 4
		accessAllowedObjectACE   = 5
		accessAllowedCallbackACE = 9
		accessAllowedCallbackObj = 11
		fileDeleteChild          = windows.ACCESS_MASK(0x40)
	)
	fileMutation := windows.ACCESS_MASK(
		windows.GENERIC_ALL | windows.GENERIC_WRITE | windows.DELETE |
			windows.WRITE_DAC | windows.WRITE_OWNER | windows.FILE_WRITE_DATA |
			windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA | windows.FILE_WRITE_ATTRIBUTES,
	)
	directoryMutation := fileMutation | fileDeleteChild
	mutation := fileMutation
	if directory {
		mutation = directoryMutation
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil {
			return NewError(CodeIntegrity, "database supervisor executable DACL entry is invalid")
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		switch ace.Header.AceType {
		case windows.ACCESS_ALLOWED_ACE_TYPE:
		case accessAllowedCompoundACE, accessAllowedObjectACE,
			accessAllowedCallbackACE, accessAllowedCallbackObj:
			return NewError(CodeIntegrity, "database supervisor executable DACL entry is unsupported")
		default:
			continue
		}
		trustee := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if supervisorTrustedWindowsExecutableSID(trustee, current) {
			continue
		}
		if ace.Mask&mutation != 0 {
			return NewError(CodeIntegrity, "database supervisor executable is mutable by another principal")
		}
	}
	return nil
}

func supervisorTrustedWindowsExecutableSID(candidate, current *windows.SID) bool {
	if candidate == nil || !candidate.IsValid() || current == nil || !current.IsValid() {
		return false
	}
	if candidate.Equals(current) || candidate.IsWellKnown(windows.WinLocalSystemSid) ||
		candidate.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return true
	}
	trustedInstaller, err := windows.StringToSid(
		"S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464",
	)
	return err == nil && trustedInstaller != nil && candidate.Equals(trustedInstaller)
}
