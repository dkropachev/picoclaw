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

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/pkg/database"
	"golang.org/x/sys/windows"
)

type windowsClaimHandle struct {
	file          *os.File
	overlapped    *windows.Overlapped
	identity      fileidentity.Identity
	fullHierarchy bool
}

type windowsFileAttributeTagInformation struct {
	FileAttributes uint32
	ReparseTag     uint32
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
	if err := validateWindowsClaimAncestorChain(cache, false); err != nil {
		return err
	}
	if err := validateWindowsClaimAncestor(cache); err != nil {
		return err
	}
	if err := prepareWindowsClaimDirectory(filepath.Dir(root)); err != nil {
		return err
	}
	if err := validateWindowsClaimAncestorChain(filepath.Dir(root), true); err != nil {
		return err
	}
	if err := prepareWindowsClaimDirectory(root); err != nil {
		return err
	}
	return validateWindowsClaimAncestorChain(root, true)
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

func validateWindowsClaimAncestorChain(path string, exactLeaf bool) (result error) {
	current := filepath.Clean(path)
	leaf := true
	handles := make([]*os.File, 0, 8)
	defer func() {
		for index := len(handles) - 1; index >= 0; index-- {
			result = errors.Join(result, handles[index].Close())
		}
	}()
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
		handle, err := openWindowsClaimDirectoryHandle(current)
		if err != nil {
			return err
		}
		handles = append(handles, handle)
		openedIdentity, openedType, err := fileidentity.Opened(handle)
		pathIdentity, pathType, exists, pathErr := fileidentity.ExistingWithType(current)
		if err != nil || pathErr != nil || !exists || openedType != fileidentity.ObjectTypeDirectory ||
			pathType != fileidentity.ObjectTypeDirectory || openedIdentity != pathIdentity ||
			!windowsClaimHandleHasNoReparseTag(windows.Handle(handle.Fd())) {
			return errors.Join(
				database.NewError(database.CodeIntegrity, "physical database claim root ancestor changed"),
				err, pathErr,
			)
		}
		descriptor, securityErr := windows.GetSecurityInfo(
			windows.Handle(handle.Fd()), windows.SE_FILE_OBJECT,
			windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
		)
		if securityErr != nil {
			return fmt.Errorf("inspect physical database claim root ancestor security: %w", securityErr)
		}
		if leaf && exactLeaf {
			if err := validateWindowsClaimDescriptor(descriptor, true); err != nil {
				return err
			}
		} else if err := validateWindowsClaimAncestorDescriptor(descriptor); err != nil {
			return err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
		leaf = false
	}
}

func openWindowsClaimDirectoryHandle(path string) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pointer, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0,
	)
	if err != nil {
		return nil, fmt.Errorf("pin physical database claim root ancestor: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, database.NewError(database.CodeUnavailable, "physical database claim ancestor handle is unavailable")
	}
	return file, nil
}

func validateWindowsClaimAncestorDescriptor(descriptor *windows.SECURITY_DESCRIPTOR) error {
	if descriptor == nil || !descriptor.IsValid() {
		return database.NewError(database.CodeIntegrity, "physical database claim ancestor security is invalid")
	}
	owner, _, err := descriptor.Owner()
	if err != nil || !trustedWindowsClaimPrincipal(owner) {
		return database.NewError(database.CodeUnauthorized, "physical database claim ancestor owner is untrusted")
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PRESENT == 0 {
		return database.NewError(database.CodeIntegrity, "physical database claim ancestor DACL is unavailable")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return database.NewError(database.CodeIntegrity, "physical database claim ancestor DACL is unavailable")
	}
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil || ace == nil {
			return database.NewError(database.CodeIntegrity, "physical database claim ancestor DACL entry is invalid")
		}
		allowed, supported := classifyWindowsClaimACE(ace.Header.AceType)
		if !supported {
			return database.NewError(database.CodeIntegrity, "physical database claim ancestor DACL entry is unsupported")
		}
		if !allowed || !windowsClaimMutationAccess(uint32(ace.Mask)) {
			continue
		}
		trustee := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !trustedWindowsClaimPrincipal(trustee) {
			return database.NewError(database.CodeIntegrity, "physical database claim ancestor is mutable by another principal")
		}
	}
	return nil
}

func trustedWindowsClaimPrincipal(sid *windows.SID) bool {
	if sid == nil || !sid.IsValid() {
		return false
	}
	current, err := currentWindowsClaimSID()
	return err == nil && (sid.Equals(current) || sid.IsWellKnown(windows.WinLocalSystemSid) ||
		sid.IsWellKnown(windows.WinBuiltinAdministratorsSid))
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
	return acquireWindowsClaim(root, identity, true)
}

func acquireClaimForTesting(root string, identity string) (claimHandle, error) {
	return acquireWindowsClaim(root, identity, false)
}

func acquireWindowsClaim(root string, identity string, fullHierarchy bool) (claimHandle, error) {
	if !validClaimIdentity(identity) {
		return nil, database.NewError(database.CodeIntegrity, "physical database claim identity is invalid")
	}
	if fullHierarchy {
		if err := validateWindowsClaimAncestorChain(root, true); err != nil {
			return nil, err
		}
	} else if _, err := validateExplicitTestClaimRoot(root); err != nil {
		return nil, err
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
	openedIdentity, objectType, identityErr := fileidentity.Opened(file)
	if identityErr != nil || objectType != fileidentity.ObjectTypeRegular ||
		!windowsClaimHandleHasNoReparseTag(handle) || !safeWindowsClaimLinkCount(information.NumberOfLinks) {
		return nil, errors.Join(
			database.NewError(database.CodeIntegrity, "physical database claim lock identity is invalid"),
			identityErr,
		)
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
	current, currentErr := openWindowsClaimValidationHandle(path)
	if currentErr != nil {
		_ = windows.UnlockFileEx(handle, 0, 1, 0, overlapped)
		return nil, errors.Join(
			database.NewError(database.CodeIntegrity, "physical database claim lock changed"),
			currentErr,
		)
	}
	currentIdentity, currentType, currentIdentityErr := fileidentity.Opened(current)
	currentSafe := windowsClaimHandleHasNoReparseTag(windows.Handle(current.Fd()))
	currentSingleLink := windowsClaimHandleHasSingleLink(windows.Handle(current.Fd()))
	currentCloseErr := current.Close()
	if currentIdentityErr != nil || currentCloseErr != nil || currentType != fileidentity.ObjectTypeRegular ||
		currentIdentity != openedIdentity || !currentSafe || !currentSingleLink ||
		!windowsClaimHandleHasSingleLink(handle) {
		_ = windows.UnlockFileEx(handle, 0, 1, 0, overlapped)
		return nil, errors.Join(
			database.NewError(database.CodeIntegrity, "physical database claim lock changed"),
			currentIdentityErr, currentCloseErr,
		)
	}
	valid = true
	return &windowsClaimHandle{
		file: file, overlapped: overlapped, identity: openedIdentity, fullHierarchy: fullHierarchy,
	}, nil
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
	root := filepath.Dir(claim.file.Name())
	if claim.fullHierarchy {
		if err := validateWindowsClaimAncestorChain(root, true); err != nil {
			return false
		}
	} else if _, err := validateExplicitTestClaimRoot(root); err != nil {
		return false
	}
	handle := windows.Handle(claim.file.Fd())
	openedIdentity, openedType, openedErr := fileidentity.Opened(claim.file)
	if openedErr != nil || openedType != fileidentity.ObjectTypeRegular ||
		openedIdentity != claim.identity || !windowsClaimHandleHasNoReparseTag(handle) ||
		!windowsClaimHandleHasSingleLink(handle) {
		return false
	}
	current, err := openWindowsClaimValidationHandle(claim.file.Name())
	if err != nil {
		return false
	}
	currentIdentity, currentType, identityErr := fileidentity.Opened(current)
	currentSafe := windowsClaimHandleHasNoReparseTag(windows.Handle(current.Fd()))
	currentSingleLink := windowsClaimHandleHasSingleLink(windows.Handle(current.Fd()))
	closeErr := current.Close()
	if identityErr != nil || closeErr != nil || currentType != fileidentity.ObjectTypeRegular ||
		currentIdentity != openedIdentity || !currentSafe || !currentSingleLink {
		return false
	}
	security, err := windows.GetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	return err == nil && validateWindowsClaimDescriptor(security, false) == nil
}

func openWindowsClaimValidationHandle(path string) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pointer, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, database.NewError(database.CodeUnavailable, "physical database claim validation handle is unavailable")
	}
	return file, nil
}

func windowsClaimHandleHasNoReparseTag(handle windows.Handle) bool {
	var information windowsFileAttributeTagInformation
	if windows.GetFileInformationByHandleEx(
		handle, windows.FileAttributeTagInfo, (*byte)(unsafe.Pointer(&information)),
		uint32(unsafe.Sizeof(information)),
	) != nil {
		return false
	}
	return safeWindowsClaimTag(information.FileAttributes, information.ReparseTag)
}

func windowsClaimHandleHasSingleLink(handle windows.Handle) bool {
	var information windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(handle, &information) == nil &&
		safeWindowsClaimLinkCount(information.NumberOfLinks)
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
