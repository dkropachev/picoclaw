//go:build unix

package database

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func configureSupervisorProcess(command *exec.Cmd, home string) error {
	logs := filepath.Join(home, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		return fmt.Errorf("create database supervisor log directory: %w", err)
	}
	log, err := os.OpenFile(
		filepath.Join(logs, "database-supervisor.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return fmt.Errorf("open database supervisor log: %w", err)
	}
	command.Stdin = nil
	command.Stdout = log
	command.Stderr = log
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}
