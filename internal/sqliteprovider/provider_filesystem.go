package sqliteprovider

import (
	"os"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

type generationLinkClass uint8

const (
	generationLinkUnavailable generationLinkClass = iota
	generationLinkZero
	generationLinkSingle
	generationLinkMultiple
	generationLinkUnsafe
)

type generationOwnerClass uint8

const (
	generationOwnerUnavailable generationOwnerClass = iota
	generationOwnerCurrent
	generationOwnerForeign
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
	validateLiveInfo  func(os.FileInfo) error
	syncDirectory     func(string) error
	linkCount         func(string, os.FileInfo) generationLinkClass
	owner             func(string, os.FileInfo) generationOwnerClass
}

func systemProviderFilesystem() providerFilesystem {
	return providerFilesystem{
		validateSyntax: validateProviderPathSyntax, validateAncestors: validateProviderAncestors,
		mkdirAll: makeProviderDirectories, lstat: os.Lstat,
		openFile:        providerOpenFile,
		secureDirectory: secureProviderDirectory, secureFile: secureProviderFile,
		validateLiveInfo: validateProviderLiveFileInfo,
		syncDirectory:    fileutil.SyncDirectory,
		linkCount:        classifyGenerationLinkCount, owner: classifyGenerationOwner,
	}
}

func providerGenerationOwner(
	filesystem providerFilesystem,
	path string,
	info os.FileInfo,
) generationOwnerClass {
	if filesystem.owner == nil {
		return generationOwnerUnavailable
	}
	return filesystem.owner(path, info)
}

func providerGenerationLinkCount(
	filesystem providerFilesystem,
	path string,
	info os.FileInfo,
) generationLinkClass {
	if filesystem.linkCount == nil {
		return generationLinkUnavailable
	}
	return filesystem.linkCount(path, info)
}
