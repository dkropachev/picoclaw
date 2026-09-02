//go:build unix

package utils

import (
	"os/exec"
	"syscall"
)

// LauncherExecCommand creates an exec.Cmd. On non-Windows platforms, this is
// a simple wrapper around exec.Command.
func LauncherExecCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

// ApplyLauncherProcAttrs gives managed processes a private process group so a
// forced launcher stop can terminate their runtime descendants too.
func ApplyLauncherProcAttrs(command *exec.Cmd) {
	if command != nil {
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
}
