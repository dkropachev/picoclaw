//go:build !unix && !windows

package sqliteprovider

import "os"

func secureProviderDirectory(path string) error { return os.Chmod(path, 0o700) }
func secureProviderFile(path string) error      { return os.Chmod(path, 0o600) }
