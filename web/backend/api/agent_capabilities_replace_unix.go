//go:build unix && !linux && !darwin

package api

import "io/fs"

func agentCapabilitiesConditionalCreateSupported() bool {
	return false
}

func replaceAgentCapabilitiesFileIfUnchanged(
	temporaryPath string,
	targetPath string,
	expected agentDefinitionFile,
	expectedExists bool,
	candidate agentDefinitionFile,
	_ fs.FileInfo,
) (bool, error) {
	return replaceAgentCapabilitiesFileFallback(
		temporaryPath,
		targetPath,
		expected,
		expectedExists,
		candidate,
	)
}

func rollbackCreatedAgentCapabilitiesFile(
	_ string,
	_ agentDefinitionFile,
	_ fs.FileInfo,
) error {
	return errAgentCapabilitiesAtomicReplaceUnavailable
}
