//go:build linux

package gateway

import (
	"os"
	"os/exec"
	"syscall"
)

func configureGatewayRuntimeChild(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}

func terminateGatewayRuntimeChild(command *exec.Cmd) {
	if command != nil && command.Process != nil {
		_ = command.Process.Signal(syscall.SIGTERM)
	}
}

func terminateCurrentGatewayRuntime() {
	process, err := os.FindProcess(os.Getpid())
	if err == nil {
		_ = process.Signal(syscall.SIGTERM)
	}
}

func startGatewayParentWatcher() {}
