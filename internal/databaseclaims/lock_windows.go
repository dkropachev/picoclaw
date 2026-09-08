//go:build windows

package databaseclaims

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"github.com/sipeed/picoclaw/pkg/database"
	"golang.org/x/sys/windows"
)

type windowsClaimHandle struct {
	file       *os.File
	overlapped *windows.Overlapped
}

func stableClaimCacheRoot() (string, error) {
	path, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil || path == "" || path != strings.TrimSpace(path) || strings.ContainsRune(path, 0) ||
		!filepath.IsAbs(path) {
		return "", database.NewError(database.CodeUnavailable, "physical database claim owner cache is unavailable")
	}
	return filepath.Clean(path), nil
}

func preparePlatformClaimRoot(cache, root string) error {
	if filepath.Dir(filepath.Dir(root)) != cache {
		return database.NewError(database.CodeIntegrity, "physical database claim root layout is invalid")
	}
	if err := validateWindowsClaimAncestorChain(cache); err != nil {
		return err
	}
	if err := validateWindowsClaimAncestor(cache); err != nil {
		return err
	}
	if err := prepareWindowsClaimDirectory(filepath.Dir(root)); err != nil {
		return err
	}
	return prepareWindowsClaimDirectory(root)
}

func prepareWindowsClaimDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		attributes, descriptor, securityErr := windowsClaimSecurityAttributes(true)
		if securityErr != nil {
			return securityErr
		}
		pathPointer, pointerErr := windows.UTF16PtrFromString(path)
		if pointerErr != nil {
			return pointerErr
		}
		createErr := windows.CreateDirectory(pathPointer, attributes)
		runtime.KeepAlive(descriptor)
		if createErr != nil && !errors.Is(createErr, windows.ERROR_ALREADY_EXISTS) {
			return fmt.Errorf("create physical database claim root: %w", createErr)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("inspect physical database claim root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return database.NewError(database.CodeIntegrity, "physical database claim root is unsafe")
	}
	attributes, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(path))
	if err != nil {
		return fmt.Errorf("inspect physical database claim root attributes: %w", err)
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return database.NewError(database.CodeIntegrity, "physical database claim root contains a reparse point")
	}
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("inspect physical database claim root security: %w", err)
	}
	return validateWindowsClaimDescriptor(descriptor, true)
}

func validateWindowsClaimAncestorChain(path string) error {
	current := filepath.Clean(path)
	for {
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect physical database claim root ancestor: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return database.NewError(database.CodeIntegrity, "physical database claim root ancestor is unsafe")
		}
		attributes, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(current))
		if err != nil {
			return fmt.Errorf("inspect physical database claim root ancestor attributes: %w", err)
		}
		if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return database.NewError(database.CodeIntegrity, "physical database claim root ancestor contains a reparse point")
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
	}
}

func validateWindowsClaimAncestor(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect physical database claim root ancestor: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return database.NewError(database.CodeIntegrity, "physical database claim root ancestor is unsafe")
	}
	attributes, err := windows.GetFileAttributes(windows.StringToUTF16Ptr(path))
	if err != nil {
		return fmt.Errorf("inspect physical database claim root ancestor attributes: %w", err)
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return database.NewError(database.CodeIntegrity, "physical database claim root ancestor contains a reparse point")
	}
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
	)
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		return database.NewError(database.CodeIntegrity, "physical database claim root ancestor security is invalid")
	}
	current, err := currentWindowsClaimSID()
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.IsValid() {
		return database.NewError(database.CodeIntegrity, "physical database claim root ancestor owner is invalid")
	}
	if !owner.Equals(current) {
		return database.NewError(database.CodeUnauthorized, "physical database claim root ancestor is owned by another user")
	}
	return nil
}

func acquireClaim(root string, identity string) (claimHandle, error) {
	if !validClaimIdentity(identity) {
		return nil, database.NewError(database.CodeIntegrity, "physical database claim identity is invalid")
	}
	path := filepath.Join(root, identity+".lock")
	attributes, descriptor, err := windowsClaimSecurityAttributes(false)
	if err != nil {
		return nil, err
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		attributes,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	runtime.KeepAlive(descriptor)
	if err != nil {
		return nil, fmt.Errorf("open physical database claim lock: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("open physical database claim lock returned no file")
	}
	valid := false
	defer func() {
		if !valid {
			_ = file.Close()
		}
	}()
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		return nil, fmt.Errorf("inspect physical database claim lock: %w", err)
	}
	if information.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return nil, database.NewError(database.CodeIntegrity, "physical database claim lock boundary is invalid")
	}
	security, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return nil, fmt.Errorf("inspect physical database claim lock security: %w", err)
	}
	if err := validateWindowsClaimDescriptor(security, false); err != nil {
		return nil, err
	}
	overlapped := new(windows.Overlapped)
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped); err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, errClaimBusy
		}
		return nil, fmt.Errorf("lock physical database claim: %w", err)
	}
	opened, statErr := file.Stat()
	current, lstatErr := os.Lstat(path)
	if statErr != nil || lstatErr != nil || opened == nil || current == nil ||
		!os.SameFile(opened, current) || current.Mode()&os.ModeSymlink != 0 {
		_ = windows.UnlockFileEx(handle, 0, 1, 0, overlapped)
		return nil, errors.Join(
			database.NewError(database.CodeIntegrity, "physical database claim lock changed"),
			statErr,
			lstatErr,
		)
	}
	valid = true
	return &windowsClaimHandle{file: file, overlapped: overlapped}, nil
}

func (claim *windowsClaimHandle) close() error {
	if claim == nil || claim.file == nil {
		return nil
	}
	file := claim.file
	overlapped := claim.overlapped
	claim.file = nil
	claim.overlapped = nil
	var unlockErr error
	if overlapped != nil {
		unlockErr = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
	}
	return errors.Join(unlockErr, file.Close())
}

func (claim *windowsClaimHandle) valid() bool {
	if claim == nil || claim.file == nil {
		return false
	}
	opened, statErr := claim.file.Stat()
	current, lstatErr := os.Lstat(claim.file.Name())
	if statErr != nil || lstatErr != nil || opened == nil || current == nil ||
		!opened.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(opened, current) {
		return false
	}
	security, err := windows.GetSecurityInfo(
		windows.Handle(claim.file.Fd()),
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	return err == nil && validateWindowsClaimDescriptor(security, false) == nil
}

func windowsClaimSecurityAttributes(
	directory bool,
) (*windows.SecurityAttributes, *windows.SECURITY_DESCRIPTOR, error) {
	sid, err := currentWindowsClaimSID()
	if err != nil {
		return nil, nil, err
	}
	flags := ""
	if directory {
		flags = "OICI"
	}
	sidString := sid.String()
	if !validWindowsClaimSIDString(sidString) {
		return nil, nil, database.NewError(database.CodeIntegrity, "physical database claim owner SID is invalid")
	}
	sddl := "O:" + sidString + "D:P(A;" + flags + ";GA;;;" + sidString + ")"
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, nil, fmt.Errorf("build physical database claim security: %w", err)
	}
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}, descriptor, nil
}

func validateWindowsClaimDescriptor(descriptor *windows.SECURITY_DESCRIPTOR, directory bool) error {
	if descriptor == nil || !descriptor.IsValid() {
		return database.NewError(database.CodeIntegrity, "physical database claim security descriptor is invalid")
	}
	current, err := currentWindowsClaimSID()
	if err != nil {
		return err
	}
	owner, ownerDefaulted, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.IsValid() || ownerDefaulted {
		return database.NewError(database.CodeIntegrity, "physical database claim owner descriptor is invalid")
	}
	if !owner.Equals(current) {
		return database.NewError(database.CodeUnauthorized, "physical database claim boundary is owned by another user")
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 || control&windows.SE_DACL_PROTECTED == 0 {
		return database.NewError(database.CodeIntegrity, "physical database claim DACL is not protected")
	}
	dacl, defaulted, err := descriptor.DACL()
	if err != nil || dacl == nil || defaulted || dacl.AceCount != 1 {
		return database.NewError(database.CodeIntegrity, "physical database claim DACL is not owner-only")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil || ace == nil ||
		ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
		ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
		return database.NewError(database.CodeIntegrity, "physical database claim DACL entry is invalid")
	}
	if directory && ace.Header.AceFlags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) !=
		(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) {
		return database.NewError(database.CodeIntegrity, "physical database claim directory does not protect children")
	}
	const fileAllAccess = windows.ACCESS_MASK(windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff)
	if ace.Mask&windows.GENERIC_ALL == 0 && ace.Mask&fileAllAccess != fileAllAccess {
		return database.NewError(database.CodeIntegrity, "physical database claim owner lacks full control")
	}
	trustee := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if trustee == nil || !trustee.IsValid() || !trustee.Equals(current) {
		return database.NewError(database.CodeIntegrity, "physical database claim DACL grants another principal")
	}
	return nil
}

func currentWindowsClaimSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("resolve physical database claim owner: %w", err)
	}
	if user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() {
		return nil, database.NewError(database.CodeUnauthorized, "physical database claim owner is unavailable")
	}
	return user.User.Sid.Copy()
}

func validWindowsClaimSIDString(value string) bool {
	if len(value) < 5 || len(value) > 184 || !strings.HasPrefix(value, "S-") {
		return false
	}
	parts := strings.Split(value[2:], "-")
	if len(parts) < 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}
