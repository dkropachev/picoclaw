//go:build windows

package storecatalog

import (
	"errors"
	"path/filepath"
	"strings"
)

func validateCatalogPlatformPath(path string) error {
	cleaned := filepath.Clean(path)
	volume := filepath.VolumeName(cleaned)
	if strings.HasPrefix(volume, `\\?\`) || strings.HasPrefix(volume, `\\.\`) {
		return errors.New("path uses an ambiguous Windows device namespace")
	}
	remainder := strings.TrimPrefix(cleaned, volume)
	for _, component := range strings.FieldsFunc(remainder, func(character rune) bool {
		return character == '/' || character == '\\'
	}) {
		if component != strings.TrimRight(component, " .") || strings.ContainsRune(component, ':') {
			return errors.New("path contains an ambiguous Windows component")
		}
		if catalogWindowsShortNameLike(component) {
			return errors.New("path contains a DOS short-name alias")
		}
		deviceBase, _, _ := strings.Cut(component, ".")
		switch strings.ToUpper(deviceBase) {
		case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$",
			"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
			"COM¹", "COM²", "COM³",
			"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9",
			"LPT¹", "LPT²", "LPT³":
			return errors.New("path contains a reserved Windows component")
		}
	}
	return nil
}

func catalogWindowsShortNameLike(component string) bool {
	base, _, _ := strings.Cut(component, ".")
	tilde := strings.LastIndexByte(base, '~')
	if tilde <= 0 || tilde == len(base)-1 {
		return false
	}
	for _, character := range base[tilde+1:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func catalogPathKey(path string) string { return strings.ToLower(filepath.Clean(path)) }

func sameCatalogResolvedPath(first, second string) bool {
	return strings.EqualFold(filepath.Clean(first), filepath.Clean(second))
}
