//go:build unix

package database

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
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
	return consumeSupervisorBootstrapFileExpectedWithOps(
		stateDir,
		expectedState,
		name,
		expectedFile,
		expectedFileIdentity,
		beforeDelete,
		supervisorUnixBootstrapOps{
			newFile:            os.NewFile,
			validateOpenedFile: validateSupervisorOpenedFile,
			linkCount:          supervisorFileLinkCount,
		},
	)
}

type supervisorUnixBootstrapOps struct {
	newFile            func(uintptr, string) *os.File
	validateOpenedFile func(string, *os.File, os.FileMode) error
	linkCount          func(*os.File) (int64, error)
}

func consumeSupervisorBootstrapFileExpectedWithOps(
	stateDir string,
	expectedState supervisorFileIdentity,
	name string,
	expectedFile supervisorFileIdentity,
	expectedFileIdentity string,
	beforeDelete func(),
	ops supervisorUnixBootstrapOps,
) error {
	token, err := supervisorBootstrapToken(name)
	if err != nil {
		return err
	}
	directoryFD, openErr := unix.Open(
		stateDir,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if openErr != nil {
		return NewError(CodeIntegrity, "database supervisor bootstrap directory is unavailable")
	}
	directory := ops.newFile(uintptr(directoryFD), stateDir)
	if directory == nil {
		_ = unix.Close(directoryFD)
		return NewError(CodeIntegrity, "database supervisor bootstrap directory is unavailable")
	}
	defer directory.Close()
	if err := validateSupervisorBootstrapDirectory(stateDir, expectedState, directory); err != nil {
		return err
	}

	fileFD, openErr := unix.Openat(
		directoryFD,
		name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if openErr != nil {
		return NewError(CodeIntegrity, "database supervisor bootstrap file is unavailable")
	}
	path := filepath.Join(stateDir, name)
	file := ops.newFile(uintptr(fileFD), path)
	if file == nil {
		_ = unix.Close(fileFD)
		return NewError(CodeIntegrity, "database supervisor bootstrap file is unavailable")
	}
	defer file.Close()
	if err := ops.validateOpenedFile(path, file, 0o600); err != nil {
		return err
	}
	openedIdentity, identityErr := supervisorOpenedIdentity(file)
	if identityErr != nil || expectedFile.Valid() && openedIdentity != expectedFile ||
		expectedFileIdentity != "" && openedIdentity.String() != expectedFileIdentity {
		return NewError(CodeIntegrity, "database supervisor bootstrap identity changed")
	}
	if beforeDelete != nil {
		beforeDelete()
	}
	if err := ops.validateOpenedFile(path, file, 0o600); err != nil {
		return NewError(CodeIntegrity, "database supervisor bootstrap identity changed before isolation")
	}
	consumedName := ".consumed-" + token
	if err := unix.Renameat(directoryFD, name, directoryFD, consumedName); err != nil {
		return NewError(CodeIntegrity, "database supervisor bootstrap file could not be isolated")
	}
	consumedPath := filepath.Join(stateDir, consumedName)
	if err := ops.validateOpenedFile(consumedPath, file, 0o600); err != nil {
		return NewError(CodeIntegrity, "database supervisor bootstrap file changed while isolating")
	}
	if err := unix.Unlinkat(directoryFD, consumedName, 0); err != nil {
		return NewError(CodeIntegrity, "database supervisor bootstrap file could not be consumed")
	}
	links, linkErr := ops.linkCount(file)
	if linkErr != nil || links != 0 {
		return NewError(CodeIntegrity, "database supervisor bootstrap file changed while consuming")
	}
	if err := directory.Sync(); err != nil {
		return NewError(CodeIntegrity, "database supervisor bootstrap directory could not be synchronized")
	}
	return validateSupervisorBootstrapDirectory(stateDir, expectedState, directory)
}

func validateSupervisorBootstrapDirectory(
	path string,
	expected supervisorFileIdentity,
	directory *os.File,
) error {
	if directory == nil || !expected.Valid() {
		return NewError(CodeIntegrity, "database supervisor bootstrap directory identity is unavailable")
	}
	opened, statErr := directory.Stat()
	openedIdentity, openedIdentityErr := supervisorOpenedDirectoryIdentity(directory)
	current, lstatErr := os.Lstat(path)
	currentIdentity, currentIdentityErr := supervisorDirectoryIdentity(path)
	if statErr != nil || openedIdentityErr != nil || lstatErr != nil || currentIdentityErr != nil ||
		opened == nil || current == nil || !openedIdentity.Valid() || !currentIdentity.Valid() ||
		opened.Mode()&os.ModeSymlink != 0 || current.Mode()&os.ModeSymlink != 0 ||
		!opened.IsDir() || !current.IsDir() || openedIdentity != expected ||
		openedIdentity != currentIdentity || validateOwnerOnlyDirectory(path, opened) != nil ||
		validateOwnerOnlyDirectory(path, current) != nil {
		return NewError(CodeIntegrity, "database supervisor bootstrap directory identity changed")
	}
	return nil
}
