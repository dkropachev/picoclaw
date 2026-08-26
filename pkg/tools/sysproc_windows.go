//go:build windows

package tools

import (
	"os"
	"os/exec"
)

func setSysProcAttrForPty(cmd *exec.Cmd) {
	// Windows doesn't support Setsid, and PTY is not available on Windows anyway.
	// This function is a no-op for Windows builds.
}

func makePTYMasterInterruptible(master *os.File) (*os.File, error) {
	return master, nil
}
