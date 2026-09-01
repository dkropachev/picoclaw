//go:build windows

package agent

import (
	"path/filepath"
	"strings"
)

func agentFileMutationWorkspaceKey(path string) string {
	cleaned := filepath.Clean(path)
	volume := filepath.VolumeName(cleaned)
	remainder := strings.TrimPrefix(cleaned, volume)
	components := strings.FieldsFunc(remainder, func(character rune) bool {
		return character == '/' || character == '\\'
	})
	for index := range components {
		components[index] = strings.TrimRight(components[index], " .")
	}
	return strings.ToLower(volume + `\` + strings.Join(components, `\`))
}
