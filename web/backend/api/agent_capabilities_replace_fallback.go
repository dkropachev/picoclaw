//go:build !linux && !darwin && !windows

package api

import (
	"errors"
	"os"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

func replaceAgentCapabilitiesFileFallback(
	temporaryPath string,
	targetPath string,
	expected agentDefinitionFile,
	expectedExists bool,
	_ agentDefinitionFile,
) (bool, error) {
	if !expectedExists {
		return linkNewAgentCapabilitiesFileFallback(temporaryPath, targetPath)
	}
	return false, errAgentCapabilitiesAtomicReplaceUnavailable
}

func linkNewAgentCapabilitiesFileFallback(
	temporaryPath string,
	targetPath string,
) (bool, error) {
	if err := os.Link(temporaryPath, targetPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, errAgentCapabilitiesRevisionMismatch
		}
		return false, err
	}
	if err := fileutil.RemoveDurable(temporaryPath); err != nil {
		return true, err
	}
	return true, nil
}
