package api

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

// gatewayProcessOperations is the single boundary for interacting with an
// existing gateway process. Tests replace this boundary with an implementation
// that recognizes only processes created by the test harness, so a PID from the
// host cannot be discovered, probed, or signaled accidentally.
type gatewayProcessOperations interface {
	Find(pid int) (*os.Process, error)
	Alive(cmd *exec.Cmd) bool
	Signal(cmd *exec.Cmd, signal os.Signal) error
	Kill(cmd *exec.Cmd) error
	Track(cmd *exec.Cmd)
	Forget(cmd *exec.Cmd)
}

// gatewayProcessOps preserves the launcher's existing host-process behavior in
// production. Test harnesses may replace it with a process registry scoped to
// their own child binaries.
var gatewayProcessOps gatewayProcessOperations = systemGatewayProcessOperations{}

type systemGatewayProcessOperations struct{}

func (systemGatewayProcessOperations) Find(pid int) (*os.Process, error) {
	return os.FindProcess(pid)
}

func (systemGatewayProcessOperations) Alive(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}

	// Wait() sets ProcessState when the process exits; use it when available.
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		return false
	}

	// Windows does not support Signal(0) probing. If we still own cmd and it
	// has not reported exit, treat it as alive.
	if runtime.GOOS == "windows" {
		return true
	}

	err := cmd.Process.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	var errno syscall.Errno
	// EPERM means the process exists but cannot be signaled by this user.
	return errors.As(err, &errno) && errno == syscall.EPERM
}

func (systemGatewayProcessOperations) Signal(cmd *exec.Cmd, signal os.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrInvalid
	}
	return cmd.Process.Signal(signal)
}

func (systemGatewayProcessOperations) Kill(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrInvalid
	}
	return cmd.Process.Kill()
}

func (systemGatewayProcessOperations) Track(*exec.Cmd)  {}
func (systemGatewayProcessOperations) Forget(*exec.Cmd) {}
