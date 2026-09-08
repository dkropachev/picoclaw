//go:build windows

package databaseclaims

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/sipeed/picoclaw/internal/fileidentity"
	"github.com/sipeed/picoclaw/pkg/database"
)

func validateExplicitTestClaimRoot(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", database.NewError(database.CodeIntegrity, "test claim root must be canonical and absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !strings.EqualFold(filepath.Clean(resolved), path) {
		return "", errors.Join(database.NewError(database.CodeIntegrity, "test claim root contains an alias"), err)
	}
	info, err := os.Lstat(path)
	if err != nil || info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.Join(database.NewError(database.CodeIntegrity, "test claim root is not a directory"), err)
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", database.NewError(database.CodeIntegrity, "test claim root path is invalid")
	}
	attributes, err := windows.GetFileAttributes(pointer)
	if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return "", errors.Join(database.NewError(database.CodeIntegrity, "test claim root is a reparse point"), err)
	}
	if err := validateWindowsClaimAncestorChain(path, true); err != nil {
		return "", err
	}
	return path, nil
}

func prepareExplicitTestClaimRoot(path string) (result string, resultErr error) {
	canonical, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", database.NewError(database.CodeIntegrity, "test claim root path is invalid")
	}
	resolved, err := filepath.EvalSymlinks(canonical)
	if err != nil {
		return "", database.NewError(database.CodeIntegrity, "test claim root cannot be canonicalized")
	}
	resolved = filepath.Clean(resolved)
	if !strings.EqualFold(resolved, canonical) {
		return "", database.NewError(database.CodeIntegrity, "test claim root contains an alias")
	}
	canonical = resolved
	if err := validateWindowsClaimAncestor(canonical); err != nil {
		return "", err
	}
	pathIdentity, pathType, exists, err := fileidentity.ExistingWithType(canonical)
	if err != nil || !exists || pathType != fileidentity.ObjectTypeDirectory {
		return "", errors.Join(database.NewError(database.CodeIntegrity, "test claim root identity is invalid"), err)
	}
	pointer, err := windows.UTF16PtrFromString(canonical)
	if err != nil {
		return "", database.NewError(database.CodeIntegrity, "test claim root path is invalid")
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(handle), canonical)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return "", database.NewError(database.CodeUnavailable, "test claim root handle is unavailable")
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	openedIdentity, openedType, err := fileidentity.Opened(file)
	if err != nil || openedType != fileidentity.ObjectTypeDirectory || openedIdentity != pathIdentity ||
		!windowsClaimHandleHasNoReparseTag(handle) {
		return "", errors.Join(database.NewError(database.CodeIntegrity, "test claim root changed"), err)
	}
	_, descriptor, err := windowsClaimSecurityAttributes(true)
	if err != nil {
		return "", err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return "", err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return "", err
	}
	if err := windows.SetSecurityInfo(
		handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|
			windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, dacl, nil,
	); err != nil {
		return "", err
	}
	runtime.KeepAlive(descriptor)
	validated, err := validateExplicitTestClaimRoot(canonical)
	finalIdentity, finalType, finalExists, finalErr := fileidentity.ExistingWithType(canonical)
	if err != nil || finalErr != nil || !finalExists || finalType != fileidentity.ObjectTypeDirectory ||
		finalIdentity != openedIdentity {
		return "", errors.Join(
			database.NewError(database.CodeIntegrity, "test claim root changed while securing"),
			err, finalErr,
		)
	}
	return validated, nil
}
