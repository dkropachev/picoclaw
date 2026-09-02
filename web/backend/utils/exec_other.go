//go:build !unix && !windows

package utils

import "os/exec"

func LauncherExecCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

func ApplyLauncherProcAttrs(*exec.Cmd) {}
