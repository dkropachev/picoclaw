//go:build unix

package database

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func configureSupervisorProcess(command *exec.Cmd, home string) error {
	if err := configureSupervisorLog(command, home); err != nil {
		return err
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}

func launchSupervisorProcess(command *exec.Cmd) (*supervisorProcessLaunch, error) {
	return launchSupervisorProcessWith(command, supervisorUnixLaunchOps{
		start:    (*exec.Cmd).Start,
		newOwner: newSupervisorProcessOwner,
		kill:     (*os.Process).Kill,
		wait:     (*exec.Cmd).Wait,
	})
}

type supervisorUnixLaunchOps struct {
	start    func(*exec.Cmd) error
	newOwner func(*os.Process, func() error, chan struct{}) (supervisorProcessOwner, error)
	kill     func(*os.Process) error
	wait     func(*exec.Cmd) error
}

func launchSupervisorProcessWith(
	command *exec.Cmd,
	ops supervisorUnixLaunchOps,
) (*supervisorProcessLaunch, error) {
	if err := ops.start(command); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	owner, err := ops.newOwner(command.Process, func() error { return ops.wait(command) }, done)
	if err != nil {
		_ = ops.kill(command.Process)
		_ = ops.wait(command)
		return nil, fmt.Errorf("database supervisor process ownership: %w", err)
	}
	if err := owner.activate(); err != nil {
		killErr := owner.kill()
		closeErr := owner.close()
		return nil, errors.Join(
			fmt.Errorf("database supervisor process activation: %w", err),
			killErr,
			closeErr,
		)
	}
	return &supervisorProcessLaunch{
		pid: command.Process.Pid, owner: owner, done: done,
	}, nil
}
