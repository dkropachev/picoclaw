//go:build !unix && !windows

package database

import (
	"errors"
	"fmt"
	"os/exec"
)

func configureSupervisorProcess(command *exec.Cmd, home string) error {
	return NewError(CodeUnsupported, "database supervisor process ownership is unsupported")
}

func launchSupervisorProcess(command *exec.Cmd) (*supervisorProcessLaunch, error) {
	if err := command.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	owner, err := newSupervisorProcessOwner(command.Process, command.Wait, done)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
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
