//go:build windows

package fstools

import (
	"fmt"
	"path/filepath"
	"strings"
)

func validateFileMutationPlatformPath(path string) error {
	cleaned := filepath.Clean(path)
	remainder := strings.TrimPrefix(cleaned, filepath.VolumeName(cleaned))
	for _, component := range strings.FieldsFunc(remainder, func(character rune) bool {
		return character == '/' || character == '\\'
	}) {
		if component != strings.TrimRight(component, " .") ||
			strings.ContainsRune(component, ':') {
			return fmt.Errorf("file-mutation path contains an ambiguous Windows component")
		}
		if fileMutationWindowsShortNameLike(component) {
			return fmt.Errorf("file-mutation path contains a DOS short-name alias")
		}
		deviceBase, _, _ := strings.Cut(component, ".")
		switch strings.ToUpper(deviceBase) {
		case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$",
			"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
			"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
			return fmt.Errorf("file-mutation path contains a reserved Windows component")
		}
	}
	return nil
}

func fileMutationWindowsShortNameLike(component string) bool {
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
