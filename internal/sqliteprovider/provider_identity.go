package sqliteprovider

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func inspectedPoolKey(path string) (string, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		absolute = strings.ToLower(absolute)
	}
	return absolute, nil
}

func inspectedGenerationIdentity(path string, main os.FileInfo) ([4]os.FileInfo, error) {
	var result [4]os.FileInfo
	for index, member := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		info, err := os.Lstat(member)
		if errors.Is(err, os.ErrNotExist) && index > 0 {
			continue
		}
		if err != nil || info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return [4]os.FileInfo{}, errors.Join(
				errors.New("SQLite inspected generation identity is unavailable"), err,
			)
		}
		if index == 0 && (main == nil || !os.SameFile(main, info)) {
			return [4]os.FileInfo{}, errors.New("SQLite inspected main generation changed")
		}
		result[index] = info
	}
	return result, nil
}
