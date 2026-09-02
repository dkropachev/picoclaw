//go:build unix && !linux

package gateway

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureGatewayRuntimeChild(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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

func startGatewayParentWatcher() {
	parentPID := os.Getppid()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if err := syscall.Kill(parentPID, 0); err != nil && errors.Is(err, syscall.ESRCH) {
				terminateCurrentGatewayRuntime()
				return
			}
		}
	}()
}
