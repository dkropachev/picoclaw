//go:build unix

package api

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func signalManagedGatewayProcess(command *exec.Cmd, signal os.Signal) error {
	value, ok := signal.(syscall.Signal)
	if ok {
		if err := syscall.Kill(-command.Process.Pid, value); err == nil {
			return nil
		} else if !errors.Is(err, syscall.ESRCH) {
			return err
		}
	}
	return command.Process.Signal(signal)
}

func killManagedGatewayProcess(command *exec.Cmd) error {
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	} else if !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return command.Process.Kill()
}
