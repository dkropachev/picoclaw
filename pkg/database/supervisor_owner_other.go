//go:build !unix && !windows

package database

import (
	"errors"
	"os"
	"os/exec"
	"sync"
)

type otherSupervisorProcessOwner struct {
	process  *os.Process
	wait     func() error
	done     chan struct{}
	waitOnce sync.Once
	waitMu   sync.Mutex
	waitErr  error
}

func newSupervisorProcessOwner(
	process *os.Process,
	wait func() error,
	done chan struct{},
) (supervisorProcessOwner, error) {
	if process == nil || process.Pid <= 0 || wait == nil || done == nil {
		return nil, NewError(CodeIntegrity, "database supervisor process is unavailable")
	}
	return &otherSupervisorProcessOwner{process: process, wait: wait, done: done}, nil
}

func (*otherSupervisorProcessOwner) activate() error { return nil }

func (owner *otherSupervisorProcessOwner) exited() bool {
	if owner == nil || owner.process == nil {
		return true
	}
	// There is no portable non-reaping process probe outside Unix and Windows.
	// Conservatively retain ownership until disposition rather than mistaking a
	// reused numeric process ID for the launched child.
	return supervisorAttemptDone(owner.done)
}

func (owner *otherSupervisorProcessOwner) terminate() error {
	if owner == nil || owner.process == nil {
		return nil
	}
	err := owner.process.Signal(os.Interrupt)
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func (owner *otherSupervisorProcessOwner) kill() error {
	if owner == nil || owner.process == nil {
		return nil
	}
	err := owner.process.Kill()
	owner.startWait()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func (owner *otherSupervisorProcessOwner) close() error {
	if owner == nil {
		return nil
	}
	owner.startWait()
	if !supervisorAttemptDone(owner.done) {
		return nil
	}
	owner.waitMu.Lock()
	defer owner.waitMu.Unlock()
	return owner.waitErr
}

func (owner *otherSupervisorProcessOwner) startWait() {
	if owner == nil || owner.wait == nil || owner.done == nil {
		return
	}
	owner.waitOnce.Do(func() {
		go func() {
			waitErr := owner.wait()
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				waitErr = nil
			}
			owner.waitMu.Lock()
			owner.waitErr = waitErr
			owner.waitMu.Unlock()
			close(owner.done)
		}()
	})
}
