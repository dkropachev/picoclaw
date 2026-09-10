//go:build linux

package database

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type supervisorLinuxSyscallFaultResult struct {
	callErr  error
	setupErr error
}

func callSupervisorWithDeniedLinuxSyscalls(
	t *testing.T,
	syscalls []uint32,
	call func() error,
) error {
	t.Helper()
	result := make(chan supervisorLinuxSyscallFaultResult, 1)
	go func() {
		runtime.LockOSThread()
		if err := installSupervisorDeniedLinuxSyscalls(syscalls); err != nil {
			result <- supervisorLinuxSyscallFaultResult{setupErr: err}
			return
		}
		result <- supervisorLinuxSyscallFaultResult{callErr: call()}
	}()
	got := <-result
	if got.setupErr != nil {
		t.Fatalf("install supervisor syscall fault filter: %v", got.setupErr)
	}
	return got.callErr
}

func installSupervisorDeniedLinuxSyscalls(syscalls []uint32) error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	filters := supervisorDeniedLinuxSyscallFilters(syscalls)
	program := unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	return unix.Prctl(
		unix.PR_SET_SECCOMP,
		unix.SECCOMP_MODE_FILTER,
		uintptr(unsafe.Pointer(&program)),
		0,
		0,
	)
}

func supervisorDeniedLinuxSyscallFilters(syscalls []uint32) []unix.SockFilter {
	filters := make([]unix.SockFilter, 0, 2*len(syscalls)+2)
	filters = append(filters, unix.SockFilter{
		Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS,
		K:    0,
	})
	for _, number := range syscalls {
		filters = append(filters,
			unix.SockFilter{
				Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K,
				Jf:   1,
				K:    number,
			},
			unix.SockFilter{
				Code: unix.BPF_RET | unix.BPF_K,
				K:    unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM),
			},
		)
	}
	filters = append(filters, unix.SockFilter{
		Code: unix.BPF_RET | unix.BPF_K,
		K:    unix.SECCOMP_RET_ALLOW,
	})
	return filters
}

func TestSupervisorReapsShortLivedDetachedChild(t *testing.T) {
	home := t.TempDir()
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	executable := filepath.Join(t.TempDir(), "short-lived-supervisor")
	if err := os.WriteFile(executable, []byte(
		"#!/bin/sh\nprintf '%s\\n' \"$$\" > \"$PICOCLAW_CONFIG\"\nexit 0\n",
	), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := startSupervisorProcess(EnsureOptions{
		Executable: executable, ConfigPath: pidPath,
		CatalogFingerprint: testCatalogFingerprint, Timeout: 3 * time.Second,
	}, home); err != nil {
		t.Fatal(err)
	}
	pid := waitForSupervisorPID(t, pidPath)
	waitForUnixProcessExit(t, pid)
}

func TestEnsureSupervisorTerminatesHungUnreadyChild(t *testing.T) {
	home := t.TempDir()
	pidPath := filepath.Join(t.TempDir(), "hung.pid")
	executable := filepath.Join(t.TempDir(), "hung-supervisor")
	if err := os.WriteFile(executable, []byte(
		"#!/bin/sh\nprintf '%s\\n' \"$$\" > \"$PICOCLAW_CONFIG\"\nsleep 30\n",
	), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := EnsureSupervisor(t.Context(), EnsureOptions{
		Home: home, Executable: executable, ConfigPath: pidPath,
		CatalogFingerprint: testCatalogFingerprint, Timeout: 150 * time.Millisecond,
	})
	if CodeOf(err) != CodeUnavailable {
		t.Fatalf("hung supervisor ensure error = %v", err)
	}
	pid := waitForSupervisorPID(t, pidPath)
	waitForUnixProcessExit(t, pid)
}

func TestFailedAttemptKillsDescendantAfterRootAlreadyExited(t *testing.T) {
	home := t.TempDir()
	sentinelPath := filepath.Join(t.TempDir(), "sentinel.pid")
	rootPath := sentinelPath + ".root"
	executable := filepath.Join(t.TempDir(), "descendant-wrapper")
	if err := os.WriteFile(executable, []byte(
		"#!/bin/sh\nprintf '%s\\n' \"$$\" > \"$PICOCLAW_CONFIG.root\"\nsleep 30 &\nprintf '%s\\n' \"$!\" > \"$PICOCLAW_CONFIG\"\nexit 0\n",
	), 0o700); err != nil {
		t.Fatal(err)
	}
	attempt, err := startSupervisorProcessUntil(EnsureOptions{
		Executable: executable, ConfigPath: sentinelPath,
		CatalogFingerprint: testCatalogFingerprint,
	}, home, time.Now().Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	rootPID := waitForSupervisorPID(t, rootPath)
	sentinelPID := waitForSupervisorPID(t, sentinelPath)
	rootPIDFD := openSupervisorPIDFD(t, rootPID)
	defer unix.Close(rootPIDFD)
	sentinelPIDFD := openSupervisorPIDFD(t, sentinelPID)
	defer unix.Close(sentinelPIDFD)
	waitForSupervisorPIDFD(t, rootPIDFD, rootPID)
	select {
	case <-attempt.done:
		t.Fatal("failed supervisor root was reaped before cleanup disposition")
	default:
	}
	if err := attempt.terminate(); err != nil {
		t.Fatal(err)
	}
	waitForSupervisorPIDFD(t, sentinelPIDFD, sentinelPID)
}

func TestUnixSupervisorWinnerReleaseStartsNonDestructiveReaper(t *testing.T) {
	home := t.TempDir()
	pidPath := filepath.Join(t.TempDir(), "released.pid")
	executable := filepath.Join(t.TempDir(), "released-supervisor")
	if err := os.WriteFile(executable, []byte(
		"#!/bin/sh\nprintf '%s\\n' \"$$\" > \"$PICOCLAW_CONFIG\"\nexit 0\n",
	), 0o700); err != nil {
		t.Fatal(err)
	}
	attempt, err := startSupervisorProcessUntil(EnsureOptions{
		Executable: executable, ConfigPath: pidPath,
		CatalogFingerprint: testCatalogFingerprint,
	}, home, time.Now().Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	pid := waitForSupervisorPID(t, pidPath)
	pidFD := openSupervisorPIDFD(t, pid)
	defer unix.Close(pidFD)
	waitForSupervisorPIDFD(t, pidFD, pid)
	select {
	case <-attempt.done:
		t.Fatal("supervisor root was reaped before winner release")
	default:
	}
	if err := attempt.release(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-attempt.done:
	case <-time.After(3 * time.Second):
		t.Fatal("released supervisor root was not reaped")
	}
}

func TestFailedAttemptEscalatesTermToKillAfterRootExit(t *testing.T) {
	home := t.TempDir()
	rootPIDPath := filepath.Join(t.TempDir(), "root.pid")
	childPIDPath := rootPIDPath + ".child"
	termMarkerPath := rootPIDPath + ".term"
	executable := filepath.Join(t.TempDir(), "term-kill-supervisor")
	script := `#!/bin/sh
child_pid_path="$PICOCLAW_CONFIG.child"
term_marker_path="$PICOCLAW_CONFIG.term"
trap 'printf "%s\n" term > "$term_marker_path"; exit 0' TERM
trap '' HUP
sh -c 'trap "" TERM HUP; printf "%s\n" "$$" > "$1"; while :; do sleep 30; done' child "$child_pid_path" &
printf "%s\n" "$$" > "$PICOCLAW_CONFIG"
wait
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	attempt, err := startSupervisorProcessUntil(EnsureOptions{
		Executable: executable, ConfigPath: rootPIDPath,
		CatalogFingerprint: testCatalogFingerprint,
	}, home, time.Now().Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	rootPID := waitForSupervisorPID(t, rootPIDPath)
	childPID := waitForSupervisorPID(t, childPIDPath)
	rootPIDFD := openSupervisorPIDFD(t, rootPID)
	defer unix.Close(rootPIDFD)
	childPIDFD := openSupervisorPIDFD(t, childPID)
	defer unix.Close(childPIDFD)

	if err := attempt.terminate(); err != nil {
		t.Fatal(err)
	}
	if marker, err := os.ReadFile(termMarkerPath); err != nil || strings.TrimSpace(string(marker)) != "term" {
		t.Fatalf("supervisor root did not handle TERM before forced cleanup: %q, %v", marker, err)
	}
	waitForSupervisorPIDFD(t, rootPIDFD, rootPID)
	waitForSupervisorPIDFD(t, childPIDFD, childPID)
}

func TestLinuxSupervisorFileHelperSyscallFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		create func(string, os.FileMode) (*os.File, error)
	}{
		{name: "exclusive chmod", create: createOwnerOnlyExclusiveFile},
		{name: "append chmod", create: createOwnerOnlyExclusiveAppendFile},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "created")
			err := callSupervisorWithDeniedLinuxSyscalls(
				t,
				[]uint32{unix.SYS_FCHMOD},
				func() error {
					file, createErr := test.create(path, 0o600)
					if file != nil {
						_ = file.Close()
						return errors.New("chmod failure returned an open file")
					}
					return createErr
				},
			)
			if !errors.Is(err, unix.EPERM) {
				t.Fatalf("owner-only create chmod failure = %v", err)
			}
		})
	}

	t.Run("append open", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "log")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		err := callSupervisorWithDeniedLinuxSyscalls(
			t,
			[]uint32{unix.SYS_OPENAT},
			func() error {
				file, openErr := openOwnerOnlyAppendFile(path, 0o600)
				if file != nil {
					_ = file.Close()
					return errors.New("append-open failure returned a file")
				}
				return openErr
			},
		)
		if !errors.Is(err, unix.EPERM) {
			t.Fatalf("append-open syscall failure = %v", err)
		}
	})

	t.Run("link count", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		err = callSupervisorWithDeniedLinuxSyscalls(
			t,
			[]uint32{unix.SYS_FSTAT},
			func() error {
				links, linkErr := supervisorFileLinkCount(file)
				if links != 0 {
					return errors.New("failed link-count inspection returned links")
				}
				return linkErr
			},
		)
		if !errors.Is(err, unix.EPERM) {
			t.Fatalf("link-count syscall failure = %v", err)
		}
	})
}

func TestLinuxSupervisorBootstrapSyscallFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		syscalls []uint32
	}{
		{name: "directory open", syscalls: []uint32{unix.SYS_OPENAT}},
		{name: "isolation rename", syscalls: []uint32{unix.SYS_RENAMEAT}},
		{name: "consume unlink", syscalls: []uint32{unix.SYS_UNLINKAT}},
		{name: "directory sync", syscalls: []uint32{unix.SYS_FSYNC}},
	} {
		t.Run(test.name, func(t *testing.T) {
			token := strings.Repeat("a", tokenBytes*2)
			artifact, err := prepareSupervisorBootstrapArtifact(t.TempDir(), token)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = os.Remove(artifact.path)
				_ = os.Remove(filepath.Join(artifact.stateDir, ".consumed-"+token))
			})
			err = callSupervisorWithDeniedLinuxSyscalls(t, test.syscalls, func() error {
				return consumeSupervisorBootstrapFileExpected(
					artifact.stateDir,
					artifact.stateIdentity,
					filepath.Base(artifact.path),
					artifact.fileIdentity,
				)
			})
			if CodeOf(err) != CodeIntegrity {
				t.Fatalf("bootstrap %s syscall failure = %v", test.name, err)
			}
		})
	}
}

func waitForSupervisorPID(t *testing.T, path string) int {
	t.Helper()
	if pid, ok := readSupervisorPID(path); ok {
		return pid
	}
	watch, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(watch)
	if _, err := unix.InotifyAddWatch(
		watch, filepath.Dir(path), unix.IN_CREATE|unix.IN_CLOSE_WRITE|unix.IN_MOVED_TO,
	); err != nil {
		t.Fatal(err)
	}
	if pid, ok := readSupervisorPID(path); ok {
		return pid
	}
	events := make([]byte, unix.SizeofInotifyEvent*8+unix.PathMax)
	deadline := time.Now().Add(3 * time.Second)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatal("supervisor did not publish its PID")
		}
		timeoutMilliseconds := int((remaining + time.Millisecond - 1) / time.Millisecond)
		ready, pollErr := unix.Poll([]unix.PollFd{{Fd: int32(watch), Events: unix.POLLIN}}, timeoutMilliseconds)
		if errors.Is(pollErr, unix.EINTR) {
			continue
		}
		if pollErr != nil {
			t.Fatal(pollErr)
		}
		if ready == 0 {
			t.Fatal("supervisor did not publish its PID")
		}
		if _, readErr := unix.Read(watch, events); readErr != nil && !errors.Is(readErr, unix.EINTR) {
			t.Fatal(readErr)
		}
		if pid, ok := readSupervisorPID(path); ok {
			return pid
		}
	}
}

func readSupervisorPID(path string) (int, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	return pid, err == nil && pid > 0
}

func openSupervisorPIDFD(t *testing.T, pid int) int {
	t.Helper()
	pidFD, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		t.Fatalf("open pidfd for supervisor process %d: %v", pid, err)
	}
	return pidFD
}

func waitForSupervisorPIDFD(t *testing.T, pidFD, pid int) {
	t.Helper()
	descriptors := []unix.PollFd{{Fd: int32(pidFD), Events: unix.POLLIN}}
	ready, err := unix.Poll(
		descriptors,
		int((3*time.Second)/time.Millisecond),
	)
	if err != nil {
		t.Fatalf("wait for supervisor process %d: %v", pid, err)
	}
	if ready != 1 || descriptors[0].Revents&unix.POLLIN == 0 {
		t.Fatalf("supervisor process %d did not exit", pid)
	}
}
