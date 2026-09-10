//go:build windows

package database

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func consumeSupervisorBootstrapFile(
	stateDir string,
	expectedState supervisorFileIdentity,
	name string,
	expectedFileIdentity string,
) error {
	return consumeSupervisorBootstrapFileExpectedWithHook(
		stateDir, expectedState, name, supervisorFileIdentity{}, expectedFileIdentity, nil,
	)
}

func consumeSupervisorBootstrapFileWithHook(
	stateDir string,
	expectedState supervisorFileIdentity,
	name string,
	beforeDelete func(),
) error {
	return consumeSupervisorBootstrapFileExpectedWithHook(
		stateDir, expectedState, name, supervisorFileIdentity{}, "", beforeDelete,
	)
}

func consumeSupervisorBootstrapFileExpected(
	stateDir string,
	expectedState supervisorFileIdentity,
	name string,
	expectedFile supervisorFileIdentity,
) error {
	return consumeSupervisorBootstrapFileExpectedWithHook(
		stateDir, expectedState, name, expectedFile, expectedFile.String(), nil,
	)
}

func consumeSupervisorBootstrapFileExpectedWithHook(
	stateDir string,
	expectedState supervisorFileIdentity,
	name string,
	expectedFile supervisorFileIdentity,
	expectedFileIdentity string,
	beforeDelete func(),
) error {
	if _, err := supervisorBootstrapToken(name); err != nil {
		return err
	}
	directory, err := openSupervisorWindowsDirectory(stateDir, expectedState)
	if err != nil {
		return err
	}
	defer directory.Close()
	path := filepath.Join(stateDir, name)
	file, err := openWindowsOwnerOnlyFile(
		path,
		windows.DELETE|windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL,
		windows.OPEN_EXISTING,
		false,
	)
	if err != nil {
		return NewError(CodeIntegrity, "database supervisor bootstrap file is unavailable")
	}
	if err := validateSupervisorOpenedFile(path, file, 0o600); err != nil {
		_ = file.Close()
		return err
	}
	openedIdentity, err := supervisorOpenedIdentity(file)
	if err != nil || expectedFile.Valid() && openedIdentity != expectedFile ||
		expectedFileIdentity != "" && openedIdentity.String() != expectedFileIdentity {
		_ = file.Close()
		return NewError(CodeIntegrity, "database supervisor bootstrap identity changed")
	}
	if beforeDelete != nil {
		beforeDelete()
	}
	if err := validateSupervisorOpenedFile(path, file, 0o600); err != nil {
		_ = file.Close()
		return NewError(CodeIntegrity, "database supervisor bootstrap identity changed before deletion")
	}
	deleteFile := byte(1)
	if err := windows.SetFileInformationByHandle(
		windows.Handle(file.Fd()), windows.FileDispositionInfo, &deleteFile, 1,
	); err != nil {
		_ = file.Close()
		return NewError(CodeIntegrity, "database supervisor bootstrap file could not be consumed")
	}
	if err := file.Close(); err != nil {
		return NewError(CodeIntegrity, "database supervisor bootstrap file could not be closed")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return NewError(CodeIntegrity, "database supervisor bootstrap file changed while consuming")
	}
	return validateSupervisorWindowsDirectoryHandle(stateDir, expectedState, directory)
}

func openSupervisorWindowsDirectory(
	path string,
	expected supervisorFileIdentity,
) (*os.File, error) {
	syscallPath, err := supervisorWindowsPath(path)
	if err != nil {
		return nil, err
	}
	pointer, err := windows.UTF16PtrFromString(syscallPath)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, NewError(CodeIntegrity, "database supervisor bootstrap directory is unavailable")
	}
	directory := os.NewFile(uintptr(handle), path)
	if directory == nil {
		_ = windows.CloseHandle(handle)
		return nil, NewError(CodeIntegrity, "database supervisor bootstrap directory is unavailable")
	}
	if err := validateSupervisorWindowsDirectoryHandle(path, expected, directory); err != nil {
		_ = directory.Close()
		return nil, err
	}
	return directory, nil
}

func validateSupervisorWindowsDirectoryHandle(
	path string,
	expected supervisorFileIdentity,
	directory *os.File,
) error {
	opened, err := supervisorOpenedDirectoryIdentity(directory)
	if err != nil || !opened.Valid() || !expected.Valid() || opened != expected {
		return NewError(CodeIntegrity, "database supervisor bootstrap directory handle changed")
	}
	return validateSupervisorBootstrapDirectoryPath(path, expected)
}

func validateSupervisorBootstrapDirectoryPath(path string, expected supervisorFileIdentity) error {
	current, err := os.Lstat(path)
	identity, identityErr := supervisorDirectoryIdentity(path)
	if err != nil || identityErr != nil || !expected.Valid() || !identity.Valid() ||
		current == nil || identity != expected || current.Mode()&os.ModeSymlink != 0 ||
		!current.IsDir() || validateOwnerOnlyDirectory(path, current) != nil {
		return NewError(CodeIntegrity, "database supervisor bootstrap directory identity changed")
	}
	return nil
}

func supervisorWindowsPath(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, 0) {
		return "", NewError(CodeInvalid, "database supervisor Windows path is invalid")
	}
	cleaned := filepath.Clean(path)
	absolute, err := filepath.Abs(cleaned)
	if err != nil || absolute != path {
		return "", NewError(CodeInvalid, "database supervisor Windows path is invalid")
	}
	normalized := strings.ReplaceAll(absolute, "/", `\`)
	volume := filepath.VolumeName(absolute)
	if strings.HasPrefix(normalized, `\??\`) || strings.HasPrefix(normalized, `\\??\`) ||
		strings.HasPrefix(volume, `\\?\`) || strings.HasPrefix(volume, `\\.\`) {
		return "", NewError(CodeInvalid, "database supervisor Windows namespace is invalid")
	}
	if strings.HasPrefix(normalized, `\\`) {
		return `\\?\UNC\` + strings.TrimPrefix(normalized, `\\`), nil
	}
	return `\\?\` + normalized, nil
}
