package database

import (
	"os"
	"strings"

	"github.com/sipeed/picoclaw/internal/fileidentity"
)

type supervisorFileIdentity = fileidentity.Identity

func supervisorBootstrapToken(name string) (string, error) {
	token, found := strings.CutPrefix(name, ".bootstrap-")
	if !found || !validLowerHex(token, tokenBytes*2) {
		return "", NewError(CodeInvalid, "database supervisor bootstrap filename is invalid")
	}
	return token, nil
}

// supervisorPathIdentity resolves an exact regular-file identity. Directory
// callers must use supervisorDirectoryIdentity so an object cannot silently
// change type while a supervisor operation is in progress.
func supervisorPathIdentity(path string) (supervisorFileIdentity, error) {
	return supervisorExistingIdentity(path, fileidentity.ObjectTypeRegular)
}

func supervisorDirectoryIdentity(path string) (supervisorFileIdentity, error) {
	return supervisorExistingIdentity(path, fileidentity.ObjectTypeDirectory)
}

func supervisorExistingIdentity(
	path string,
	expectedType fileidentity.ObjectType,
) (supervisorFileIdentity, error) {
	identity, objectType, exists, err := fileidentity.ExistingWithType(path)
	if err != nil || !exists || !identity.Valid() || objectType != expectedType {
		return supervisorFileIdentity{}, NewError(
			CodeIntegrity,
			"database supervisor path identity is unavailable",
		)
	}
	return identity, nil
}

// supervisorOpenedIdentity resolves an exact regular-file identity. Directory
// handles use supervisorOpenedDirectoryIdentity for the same type binding.
func supervisorOpenedIdentity(file *os.File) (supervisorFileIdentity, error) {
	return supervisorOpenedIdentityForType(file, fileidentity.ObjectTypeRegular)
}

func supervisorOpenedDirectoryIdentity(file *os.File) (supervisorFileIdentity, error) {
	return supervisorOpenedIdentityForType(file, fileidentity.ObjectTypeDirectory)
}

func supervisorOpenedIdentityForType(
	file *os.File,
	expectedType fileidentity.ObjectType,
) (supervisorFileIdentity, error) {
	identity, objectType, err := fileidentity.Opened(file)
	if err != nil || !identity.Valid() || objectType != expectedType {
		return supervisorFileIdentity{}, NewError(
			CodeIntegrity,
			"database supervisor opened-file identity is unavailable",
		)
	}
	return identity, nil
}

func validateSupervisorOpenedFile(path string, file *os.File, mode os.FileMode) error {
	if file == nil {
		return NewError(CodeIntegrity, "database supervisor file handle is unavailable")
	}
	opened, statErr := file.Stat()
	current, lstatErr := os.Lstat(path)
	links, linkErr := supervisorFileLinkCount(file)
	openedIdentity, openedIdentityErr := supervisorOpenedIdentity(file)
	pathIdentity, pathIdentityErr := supervisorPathIdentity(path)
	if statErr != nil || lstatErr != nil || linkErr != nil ||
		openedIdentityErr != nil || pathIdentityErr != nil || opened == nil || current == nil ||
		current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() ||
		openedIdentity != pathIdentity || links != 1 ||
		validateOwnerOnlyFile(path, opened, mode) != nil ||
		validateOwnerOnlyFile(path, current, mode) != nil {
		return NewError(CodeIntegrity, "database supervisor file identity is invalid")
	}
	return nil
}
