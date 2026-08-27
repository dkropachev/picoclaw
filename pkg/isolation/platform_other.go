//go:build !linux && !windows

package isolation

import "os/exec"

func applyPlatformIsolation(cmd *exec.Cmd, launch launchProjection) error {
	// Unsupported platforms currently keep the command unchanged. Callers rely on
	// Preflight and higher-level checks to surface unsupported isolation modes.
	return nil
}

func postStartPlatformIsolation(cmd *exec.Cmd, launch launchProjection) error {
	return nil
}

func cleanupPendingPlatformResources(cmd *exec.Cmd) {
}
