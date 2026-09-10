//go:build !unix && !windows

package database

import "os"

func createOwnerOnlyExclusiveFile(path string, mode os.FileMode) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
}

func createOwnerOnlyExclusiveAppendFile(string, os.FileMode) (*os.File, error) {
	return nil, NewError(CodeUnsupported, "database supervisor append files are unsupported")
}

func openOwnerOnlyAppendFile(string, os.FileMode) (*os.File, error) {
	return nil, NewError(CodeUnsupported, "database supervisor append files are unsupported")
}

func supervisorFileLinkCount(*os.File) (int64, error) {
	return 0, NewError(CodeUnsupported, "database supervisor file identity is unsupported")
}

func supervisorExecutableModeValid(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 &&
		info.Mode().Perm()&0o022 == 0
}

func validateSupervisorExecutableTrust(string, os.FileInfo) error {
	return NewError(CodeUnsupported, "database supervisor executable trust is unsupported")
}

func validateSupervisorExecutableAncestorTrust(string, os.FileInfo) error {
	return NewError(CodeUnsupported, "database supervisor executable ancestor trust is unsupported")
}
