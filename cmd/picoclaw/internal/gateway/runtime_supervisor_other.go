//go:build !unix && !windows

package gateway

import (
	"os"
	"os/exec"
)

func configureGatewayRuntimeChild(*exec.Cmd) {}

func terminateGatewayRuntimeChild(command *exec.Cmd) {
	if command != nil && command.Process != nil {
		_ = command.Process.Kill()
	}
}

func terminateCurrentGatewayRuntime() {
	process, err := os.FindProcess(os.Getpid())
	if err == nil {
		_ = process.Kill()
	}
}

func startGatewayParentWatcher() {}
