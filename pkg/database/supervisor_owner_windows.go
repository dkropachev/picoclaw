//go:build windows

package database

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

const (
	windowsSupervisorJobDrainTimeout = 2 * time.Second
	windowsSupervisorJobPollInterval = 10 * time.Millisecond
)

type windowsSupervisorProcessOwner struct {
	mu                sync.Mutex
	job               windows.Handle
	processHandle     windows.Handle
	thread            windows.Handle
	process           *os.Process
	done              chan struct{}
	resumeThread      func(windows.Handle) (uint32, error)
	waitForProcess    func(windows.Handle, uint32) (uint32, error)
	terminateJob      func(windows.Handle, uint32) error
	queryJobProcesses func(windows.Handle) (uint32, error)
	setKillOnJobClose func(windows.Handle, bool) error
	closeHandle       func(windows.Handle) error
	waitOnce          sync.Once
	waitErr           error
	killed            bool
}

func (owner *windowsSupervisorProcessOwner) activate() error {
	if owner == nil {
		return NewError(CodeIntegrity, "database supervisor primary thread is unavailable")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.thread == 0 || owner.resumeThread == nil || owner.closeHandle == nil {
		return NewError(CodeIntegrity, "database supervisor primary thread is unavailable")
	}
	previousSuspendCount, resumeErr := owner.resumeThread(owner.thread)
	if resumeErr == nil && previousSuspendCount != 1 {
		resumeErr = NewError(
			CodeIntegrity,
			"database supervisor primary thread suspension state is invalid",
		)
	}
	closeErr := owner.closeHandle(owner.thread)
	owner.thread = 0
	return errors.Join(resumeErr, closeErr)
}

func (owner *windowsSupervisorProcessOwner) exited() bool {
	if owner == nil {
		return true
	}
	if supervisorAttemptDone(owner.done) {
		return true
	}
	owner.mu.Lock()
	handle := owner.processHandle
	waitForProcess := owner.waitForProcess
	owner.mu.Unlock()
	if handle == 0 || waitForProcess == nil {
		return false
	}
	result, err := waitForProcess(handle, 0)
	return err == nil && result == windows.WAIT_OBJECT_0
}

func (owner *windowsSupervisorProcessOwner) terminate() error {
	// Windows has no process-group equivalent of cooperative SIGTERM. Retain
	// the Job through the common grace point, then terminate it in kill.
	return nil
}

func (owner *windowsSupervisorProcessOwner) kill() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	owner.killed = true
	job := owner.job
	terminateJob := owner.terminateJob
	queryJobProcesses := owner.queryJobProcesses
	owner.mu.Unlock()
	if job == 0 {
		owner.startWait()
		return nil
	}
	if terminateJob == nil {
		owner.startWait()
		return NewError(CodeIntegrity, "database supervisor job termination is unavailable")
	}
	err := terminateJob(job, 1)
	owner.startWait()
	if err != nil {
		return err
	}
	return waitForWindowsSupervisorJobDrain(job, queryJobProcesses)
}

func waitForWindowsSupervisorJobDrain(
	job windows.Handle,
	query func(windows.Handle) (uint32, error),
) error {
	deadline := time.Now().Add(windowsSupervisorJobDrainTimeout)
	return waitForWindowsSupervisorJobDrainWith(job, windowsSupervisorJobDrainOps{
		query: query,
		retry: func() bool {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return false
			}
			delay := windowsSupervisorJobPollInterval
			if remaining < delay {
				delay = remaining
			}
			timer := time.NewTimer(delay)
			<-timer.C
			return true
		},
	})
}

type windowsSupervisorJobDrainOps struct {
	query func(windows.Handle) (uint32, error)
	retry func() bool
}

func waitForWindowsSupervisorJobDrainWith(
	job windows.Handle,
	ops windowsSupervisorJobDrainOps,
) error {
	if job == 0 || job == windows.InvalidHandle || ops.query == nil || ops.retry == nil {
		return NewError(CodeInvalid, "database supervisor job-drain operations are invalid")
	}
	for {
		activeProcesses, err := ops.query(job)
		if err != nil {
			return fmt.Errorf("inspect database supervisor job processes: %w", err)
		}
		if activeProcesses == 0 {
			return nil
		}
		if !ops.retry() {
			return NewError(CodeUnavailable, "database supervisor job did not drain")
		}
	}
}

func (owner *windowsSupervisorProcessOwner) close() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	if owner.closeHandle == nil {
		owner.mu.Unlock()
		return NewError(CodeIntegrity, "database supervisor handle close is unavailable")
	}
	var result error
	if owner.thread != 0 {
		result = errors.Join(result, owner.closeHandle(owner.thread))
		owner.thread = 0
	}
	if owner.job != 0 {
		if !owner.killed {
			if owner.setKillOnJobClose == nil {
				result = errors.Join(result, NewError(
					CodeIntegrity,
					"database supervisor job detach is unavailable",
				))
				owner.killed = true
			} else if err := owner.setKillOnJobClose(owner.job, false); err != nil {
				result = errors.Join(
					result,
					fmt.Errorf("detach database supervisor job: %w", err),
				)
				owner.killed = true
			}
			if owner.killed && owner.terminateJob != nil {
				terminateErr := owner.terminateJob(owner.job, 1)
				result = errors.Join(result, terminateErr)
				if terminateErr == nil {
					result = errors.Join(
						result,
						waitForWindowsSupervisorJobDrain(owner.job, owner.queryJobProcesses),
					)
				}
			}
		}
		result = errors.Join(result, owner.closeHandle(owner.job))
		owner.job = 0
	}
	owner.mu.Unlock()
	owner.startWait()
	if supervisorAttemptDone(owner.done) {
		owner.mu.Lock()
		result = errors.Join(result, owner.waitErr)
		owner.mu.Unlock()
	}
	return result
}

func (owner *windowsSupervisorProcessOwner) startWait() {
	if owner == nil || owner.done == nil {
		return
	}
	owner.waitOnce.Do(func() {
		go func() {
			owner.mu.Lock()
			handle := owner.processHandle
			waitForProcess := owner.waitForProcess
			closeHandle := owner.closeHandle
			process := owner.process
			owner.mu.Unlock()

			var waitErr error
			if handle == 0 || waitForProcess == nil || closeHandle == nil {
				waitErr = NewError(CodeIntegrity, "database supervisor process waiter is unavailable")
			} else {
				result, err := waitForProcess(handle, windowsSupervisorInfiniteTimeout)
				switch {
				case err != nil:
					waitErr = fmt.Errorf("wait for database supervisor process: %w", err)
				case result != windows.WAIT_OBJECT_0:
					waitErr = NewError(CodeUnavailable, "database supervisor process wait failed")
				}
				waitErr = errors.Join(waitErr, closeHandle(handle))
			}
			if process != nil {
				waitErr = errors.Join(waitErr, process.Release())
			}
			owner.mu.Lock()
			owner.processHandle = 0
			owner.process = nil
			owner.waitErr = waitErr
			owner.mu.Unlock()
			close(owner.done)
		}()
	})
}
