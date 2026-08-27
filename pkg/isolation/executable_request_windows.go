//go:build windows

package isolation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func normalizeExecutableRequest(name, dir string) (string, error) {
	volume := filepath.VolumeName(name)
	if volume != "" && !filepath.IsAbs(name) {
		return "", fmt.Errorf("drive-relative executable path is not allowed")
	}
	if !strings.ContainsAny(name, `\/`) && volume == "" {
		return name, nil
	}
	if filepath.IsAbs(name) {
		return filepath.Clean(name), nil
	}
	base := dir
	if base == "" {
		var err error
		base, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve executable directory: %w", err)
		}
	} else if !filepath.IsAbs(base) {
		absolute, err := filepath.Abs(base)
		if err != nil {
			return "", fmt.Errorf("resolve executable directory: %w", err)
		}
		base = absolute
	}
	return filepath.Clean(filepath.Join(base, name)), nil
}
