//go:build aix

package sqliteprovider

import "os"

func providerOpenFile(string, int, os.FileMode) (providerFile, error) {
	return nil, unsupportedProviderFilesystem()
}
