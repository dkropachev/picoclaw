//go:build windows

package gateway

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureGatewayRuntimeChild(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000200, HideWindow: true}
}

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

func startGatewayParentWatcher() {
	parentPID := os.Getppid()
	go func() {
		handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(parentPID))
		if err != nil {
			terminateCurrentGatewayRuntime()
			return
		}
		defer windows.CloseHandle(handle)
		status, err := windows.WaitForSingleObject(handle, windows.INFINITE)
		if err == nil && status == windows.WAIT_OBJECT_0 {
			terminateCurrentGatewayRuntime()
		}
	}()
}
