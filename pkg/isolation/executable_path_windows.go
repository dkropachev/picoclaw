//go:build windows

package isolation

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func resolveExecutablePath(
	name,
	pathValue,
	pathExtValue string,
	pathExtPresent bool,
) (string, error) {
	if name == "" || strings.IndexByte(name, 0) >= 0 {
		return "", fmt.Errorf("executable name is invalid")
	}
	volume := filepath.VolumeName(name)
	if volume != "" && !filepath.IsAbs(name) {
		return "", fmt.Errorf("drive-relative executable path is not allowed")
	}
	if strings.ContainsAny(name, `\/`) || volume != "" {
		return resolveExplicitWindowsExecutable(name, pathExtValue, pathExtPresent)
	}

	extensions := windowsExecutableExtensions(name, pathExtValue, pathExtPresent)
	if len(extensions) == 0 {
		return "", fmt.Errorf("resolve executable %s: %w", name, exec.ErrNotFound)
	}
	for _, directory := range filepath.SplitList(pathValue) {
		if directory == "" || !filepath.IsAbs(directory) {
			continue
		}
		for _, extension := range extensions {
			candidate := filepath.Join(directory, name+extension)
			info, err := os.Stat(candidate)
			if err != nil || info.IsDir() {
				continue
			}
			absolute, err := filepath.Abs(candidate)
			if err != nil {
				return "", fmt.Errorf("resolve executable %s: %w", name, err)
			}
			return filepath.Clean(absolute), nil
		}
	}
	return "", fmt.Errorf("resolve executable %s: %w", name, exec.ErrNotFound)
}

func resolveExplicitWindowsExecutable(
	name,
	pathExtValue string,
	pathExtPresent bool,
) (string, error) {
	if !filepath.IsAbs(name) {
		return "", fmt.Errorf("explicit executable path must be absolute")
	}
	extensions := windowsExecutableExtensions(name, pathExtValue, pathExtPresent)
	if len(extensions) == 0 {
		return "", fmt.Errorf("resolve executable %s: %w", name, exec.ErrNotFound)
	}
	for _, extension := range extensions {
		candidate := name + extension
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		return filepath.Clean(candidate), nil
	}
	return "", fmt.Errorf("resolve executable %s: %w", name, exec.ErrNotFound)
}

func windowsExecutableExtensions(
	name,
	pathExtValue string,
	pathExtPresent bool,
) []string {
	if !pathExtPresent {
		pathExtValue = ".COM;.EXE;.BAT;.CMD"
	}
	configured := windowsConfiguredExecutableExtensions(pathExtValue)
	if extension := filepath.Ext(name); extension != "" {
		for _, admitted := range configured {
			if strings.EqualFold(extension, admitted) {
				return []string{""}
			}
		}
		return nil
	}
	return configured
}

func windowsConfiguredExecutableExtensions(pathExtValue string) []string {
	seen := make(map[string]struct{})
	extensions := make([]string, 0, 4)
	for _, extension := range strings.Split(pathExtValue, ";") {
		extension = strings.TrimSpace(extension)
		if extension == "" || strings.IndexByte(extension, 0) >= 0 {
			continue
		}
		if extension[0] != '.' {
			extension = "." + extension
		}
		if !validWindowsExecutableExtension(extension) {
			continue
		}
		canonical := strings.ToUpper(extension)
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		extensions = append(extensions, canonical)
	}
	return extensions
}

func validWindowsExecutableExtension(extension string) bool {
	if len(extension) < 2 || len(extension) > 17 || extension[0] != '.' {
		return false
	}
	for _, character := range extension[1:] {
		if character >= 'A' && character <= 'Z' ||
			character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}
