//go:build !unix && !windows

package sqliteprovider

import (
	"github.com/sipeed/picoclaw/pkg/database"
)

func unsupportedProviderFilesystem() error {
	return database.NewError(
		database.CodeUnsupported,
		"secure file-backed SQLite provider is unsupported on this platform",
	)
}

func validateProviderPathSyntax(string) error { return unsupportedProviderFilesystem() }
func validateProviderAncestors(string) error  { return unsupportedProviderFilesystem() }
func secureProviderDirectory(string) error    { return unsupportedProviderFilesystem() }
func secureProviderFile(string) error         { return unsupportedProviderFilesystem() }
