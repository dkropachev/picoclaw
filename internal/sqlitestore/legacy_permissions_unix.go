//go:build !windows

package sqlitestore

import "os"

func safeLegacyDirectory(_ string, info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 &&
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 &&
		info.Mode().Perm()&0o022 == 0
}

func safeLegacyRegularFile(_ string, info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 &&
		info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 &&
		info.Mode().Perm()&0o022 == 0
}
