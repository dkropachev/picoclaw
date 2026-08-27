//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
)

var errWindowsHermeticProcessTreeUnavailable = errors.New(
	"hermetic Go tests require atomic Windows Job Object assignment; refusing an uncontained test process tree",
)

func hermeticRunnerSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// Go's os/exec starts a Windows child before callers can assign it to a Job
// Object. That gap can leak grandchildren. Fail before Start until the runner
// has an atomic create-suspended/assign/resume implementation.
func configureHermeticCommand(*exec.Cmd) error {
	return errWindowsHermeticProcessTreeUnavailable
}

func attachHermeticCommand(*exec.Cmd) error { return errWindowsHermeticProcessTreeUnavailable }

func terminateHermeticCommand(*exec.Cmd) error { return os.ErrProcessDone }

func cleanupHermeticCommand(*exec.Cmd) {}
