//go:build unix

package database

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const (
	supervisorProcessGroupDrainTimeout = 2 * time.Second
	supervisorProcessGroupPollInterval = 10 * time.Millisecond
)

type unixSupervisorProcessOwner struct {
	pid         int
	process     *os.Process
	wait        func() error
	done        chan struct{}
	signalGroup func(int, syscall.Signal) error
	waitGroup   func(int) error
	waitOnce    sync.Once
	waitMu      sync.Mutex
	waitErr     error
}

func newSupervisorProcessOwner(
	process *os.Process,
	wait func() error,
	done chan struct{},
) (supervisorProcessOwner, error) {
	if process == nil || process.Pid <= 0 || wait == nil || done == nil {
		return nil, NewError(CodeIntegrity, "database supervisor process is unavailable")
	}
	return &unixSupervisorProcessOwner{
		pid: process.Pid, process: process, wait: wait, done: done,
		signalGroup: func(processGroupID int, signal syscall.Signal) error {
			return syscall.Kill(-processGroupID, signal)
		},
		waitGroup: waitForSupervisorProcessGroupExit,
	}, nil
}

func (*unixSupervisorProcessOwner) activate() error { return nil }

func (owner *unixSupervisorProcessOwner) exited() bool {
	if owner == nil || owner.process == nil {
		return true
	}
	if supervisorAttemptDone(owner.done) {
		return true
	}
	err := owner.process.Signal(syscall.Signal(0))
	return errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH)
}

func (owner *unixSupervisorProcessOwner) terminate() error {
	if owner == nil || owner.pid <= 0 {
		return nil
	}
	if owner.signalGroup == nil {
		return NewError(CodeIntegrity, "database supervisor process-group signal is unavailable")
	}
	err := owner.signalGroup(owner.pid, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (owner *unixSupervisorProcessOwner) kill() error {
	if owner == nil || owner.pid <= 0 {
		return nil
	}
	if owner.signalGroup == nil || owner.waitGroup == nil {
		owner.startWait()
		return NewError(CodeIntegrity, "database supervisor process-group cleanup is unavailable")
	}
	err := owner.signalGroup(owner.pid, syscall.SIGKILL)
	owner.startWait()
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	return owner.waitGroup(owner.pid)
}

func waitForSupervisorProcessGroupExit(processGroupID int) error {
	started := time.Now()
	return waitForSupervisorProcessGroupExitWith(processGroupID, supervisorProcessGroupDrainOps{
		probe: func(candidate int) error {
			return syscall.Kill(-candidate, syscall.Signal(0))
		},
		retry: func() bool {
			time.Sleep(supervisorProcessGroupPollInterval)
			return time.Since(started) < supervisorProcessGroupDrainTimeout
		},
	})
}

type supervisorProcessGroupDrainOps struct {
	probe func(int) error
	retry func() bool
}

func waitForSupervisorProcessGroupExitWith(
	processGroupID int,
	ops supervisorProcessGroupDrainOps,
) error {
	if processGroupID <= 0 {
		return NewError(CodeInvalid, "database supervisor process group is invalid")
	}
	if ops.probe == nil || ops.retry == nil {
		return NewError(CodeInvalid, "database supervisor process-group drain operations are invalid")
	}
	for {
		err := ops.probe(processGroupID)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		if !ops.retry() {
			return NewError(CodeUnavailable, "database supervisor process group did not drain")
		}
	}
}

func (owner *unixSupervisorProcessOwner) close() error {
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

func (owner *unixSupervisorProcessOwner) startWait() {
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
