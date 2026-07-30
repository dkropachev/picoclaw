//go:build windows

package api

import "io/fs"

// ReplaceFileW follows reparse points and cannot bind the replacement to the
// no-follow target that was checked by the API. Keep capability editing
// read-only on Windows until a handle-relative, no-follow compare-and-swap is
// available. Runtime capability enforcement and the GET projection remain
// supported.
func agentCapabilitiesConditionalCreateSupported() bool {
	return false
}

func replaceAgentCapabilitiesFileIfUnchanged(
	_ string,
	_ string,
	_ agentDefinitionFile,
	_ bool,
	_ agentDefinitionFile,
	_ fs.FileInfo,
) (bool, error) {
	return false, errAgentCapabilitiesAtomicReplaceUnavailable
}

func rollbackCreatedAgentCapabilitiesFile(
	_ string,
	_ agentDefinitionFile,
	_ fs.FileInfo,
) error {
	return errAgentCapabilitiesAtomicReplaceUnavailable
}
