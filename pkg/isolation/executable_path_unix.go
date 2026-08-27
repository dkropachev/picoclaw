//go:build !windows

package isolation

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func resolveExecutablePath(name, pathValue, _ string, _ bool) (string, error) {
	if name == "" || strings.IndexByte(name, 0) >= 0 {
		return "", fmt.Errorf("executable name is invalid")
	}
	if strings.ContainsRune(name, filepath.Separator) {
		return name, nil
	}
	for _, directory := range filepath.SplitList(pathValue) {
		if directory == "" || !filepath.IsAbs(directory) {
			continue
		}
		candidate := filepath.Join(directory, name)
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			return "", fmt.Errorf("resolve executable %s: %w", name, err)
		}
		return filepath.Clean(absolute), nil
	}
	return "", fmt.Errorf("resolve executable %s: %w", name, exec.ErrNotFound)
}
