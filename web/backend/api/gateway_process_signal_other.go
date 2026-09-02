//go:build !unix && !windows

package api

import (
	"os"
	"os/exec"
)

func signalManagedGatewayProcess(command *exec.Cmd, signal os.Signal) error {
	return command.Process.Signal(signal)
}

func killManagedGatewayProcess(command *exec.Cmd) error { return command.Process.Kill() }
