//go:build !windows

package tools

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func setSysProcAttrForPty(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// makePTYMasterInterruptible replaces the blocking file wrapper returned by
// creack/pty with a close-on-exec nonblocking duplicate registered with Go's
// poller. creack/pty uses File.Fd during setup, which intentionally switches
// the original wrapper to blocking mode and prevents deadlines from waking a
// read held by another goroutine.
func makePTYMasterInterruptible(master *os.File) (*os.File, error) {
	if master == nil {
		return nil, ErrProcessSessionInvalid
	}
	duplicateFD, err := unix.FcntlInt(master.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("duplicate PTY master: %w", err)
	}
	if err = unix.SetNonblock(duplicateFD, true); err != nil {
		_ = unix.Close(duplicateFD)
		return nil, fmt.Errorf("make PTY master nonblocking: %w", err)
	}
	replacement := os.NewFile(uintptr(duplicateFD), master.Name())
	if replacement == nil {
		_ = unix.Close(duplicateFD)
		return nil, errors.New("create interruptible PTY master")
	}
	if err = replacement.SetReadDeadline(time.Time{}); err != nil {
		closeProcessHandle(replacement)
		return nil, fmt.Errorf("register PTY master with poller: %w", err)
	}
	closeProcessHandle(master)
	return replacement, nil
}
