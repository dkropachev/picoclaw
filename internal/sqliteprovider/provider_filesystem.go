package sqliteprovider

import (
	"os"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

type providerFile interface {
	Stat() (os.FileInfo, error)
	Chmod(mode os.FileMode) error
	Sync() error
	Close() error
}

type providerFilesystem struct {
	validateSyntax    func(string) error
	validateAncestors func(string) error
	mkdirAll          func(string, os.FileMode) error
	lstat             func(string) (os.FileInfo, error)
	openFile          func(string, int, os.FileMode) (providerFile, error)
	secureDirectory   func(string) error
	secureFile        func(string) error
	syncDirectory     func(string) error
	singleLink        func(string, os.FileInfo) bool
	owned             func(string, os.FileInfo) bool
}

func systemProviderFilesystem() providerFilesystem {
	return providerFilesystem{
		validateSyntax: validateProviderPathSyntax, validateAncestors: validateProviderAncestors,
		mkdirAll: makeProviderDirectories, lstat: os.Lstat,
		openFile:        providerOpenFile,
		secureDirectory: secureProviderDirectory, secureFile: secureProviderFile,
		syncDirectory: fileutil.SyncDirectory,
		singleLink:    generationHasSingleLink, owned: generationOwnedByCurrentUser,
	}
}
