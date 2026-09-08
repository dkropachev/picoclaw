//go:build linux && amd64

package sqliteprovider

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/coverage"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const (
	providerFaultScenarioEnvironment = "PICOCLAW_PROVIDER_FAULT_SCENARIO"
	providerFaultRootEnvironment     = "PICOCLAW_PROVIDER_FAULT_ROOT"
)

// TestMain gives syscall-fault children a single-threaded entry point before
// the testing harness starts worker goroutines. Normal test processes take the
// standard m.Run path.
func TestMain(m *testing.M) {
	scenario := os.Getenv(providerFaultScenarioEnvironment)
	if scenario == "" {
		os.Exit(m.Run())
	}
	code := runProviderSyscallFaultChild(scenario, os.Getenv(providerFaultRootEnvironment))
	writeProviderChildCoverage()
	os.Exit(code)
}

func writeProviderChildCoverage() {
	for _, argument := range os.Args {
		const prefix = "-test.gocoverdir="
		if strings.HasPrefix(argument, prefix) {
			directory := strings.TrimPrefix(argument, prefix)
			_ = coverage.WriteMetaDir(directory)
			_ = coverage.WriteCountersDir(directory)
			return
		}
	}
}

func runProviderSyscallFaultChild(scenario, root string) int {
	if root == "" || unix.Gettid() != os.Getpid() {
		return 3
	}
	if _, _, err := unix.RawSyscall6(
		unix.SYS_PTRACE, uintptr(unix.PTRACE_TRACEME), 0, 0, 0, 0, 0,
	); err != 0 {
		return 4
	}
	if err := unix.Kill(os.Getpid(), unix.SIGSTOP); err != nil {
		return 5
	}

	var err error
	switch scenario {
	case "missing-ancestors":
		err = makeProviderDirectories(filepath.Join(root, "new"), 0o700)
	case "directory-root-open", "directory-root-fstat", "directory-component-fstat":
		err = makeProviderDirectories(root, 0o700)
	case "created-component-fstat", "created-component-fchmod",
		"created-parent-fsync", "created-component-fsync":
		err = makeProviderDirectories(filepath.Join(root, "new"), 0o700)
	case "file-root-open":
		_, err = providerOpenFile(filepath.Join(root, "store.db"), os.O_RDWR|os.O_CREATE, 0o600)
	default:
		return 6
	}
	if err == nil {
		return 7
	}
	return 0
}

type providerSyscallFaultState struct {
	scenario    string
	fstatCalls  int
	statCalls   int
	fsyncCalls  int
	afterMkdir  bool
	injected    bool
	currentCall uint64
}

func (state *providerSyscallFaultState) shouldInject(call uint64) (bool, unix.Errno) {
	switch state.scenario {
	case "missing-ancestors":
		if call == unix.SYS_NEWFSTATAT && state.statCalls < 4 {
			state.statCalls++
			return true, unix.ENOENT
		}
	case "directory-root-open", "file-root-open":
		if call == unix.SYS_OPENAT && !state.injected {
			return true, unix.EIO
		}
	case "directory-root-fstat":
		if call == unix.SYS_FSTAT && !state.injected {
			return true, unix.EIO
		}
	case "directory-component-fstat":
		if call == unix.SYS_FSTAT {
			state.fstatCalls++
			if state.fstatCalls == 2 {
				return true, unix.EIO
			}
		}
	case "created-component-fstat":
		if state.afterMkdir && call == unix.SYS_FSTAT && !state.injected {
			return true, unix.EIO
		}
	case "created-component-fchmod":
		if state.afterMkdir && call == unix.SYS_FCHMOD && !state.injected {
			return true, unix.EIO
		}
	case "created-parent-fsync":
		if state.afterMkdir && call == unix.SYS_FSYNC && !state.injected {
			return true, unix.EIO
		}
	case "created-component-fsync":
		if state.afterMkdir && call == unix.SYS_FSYNC {
			state.fsyncCalls++
			if state.fsyncCalls == 2 {
				return true, unix.EIO
			}
		}
	}
	return false, 0
}

func TestCoverageProviderSyscallFailureBranches(t *testing.T) {
	for _, scenario := range []string{
		"missing-ancestors",
		"directory-root-open",
		"directory-root-fstat",
		"directory-component-fstat",
		"created-component-fstat",
		"created-component-fchmod",
		"created-parent-fsync",
		"created-component-fsync",
		"file-root-open",
	} {
		t.Run(scenario, func(t *testing.T) {
			runProviderSyscallFault(t, scenario)
		})
	}
}

func runProviderSyscallFault(t *testing.T, scenario string) {
	t.Helper()
	// Linux records a ptrace relationship against a specific tracer thread.
	// Keep fork, wait, and every ptrace operation on that same thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	root, err := os.MkdirTemp("/tmp", "pc-provider-fault-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	arguments := []string{"-test.run=^$"}
	for _, argument := range os.Args {
		if strings.HasPrefix(argument, "-test.gocoverdir=") {
			arguments = append(arguments, argument)
			break
		}
	}
	command := exec.Command(os.Args[0], arguments...)
	command.Env = append(os.Environ(),
		providerFaultScenarioEnvironment+"="+scenario,
		providerFaultRootEnvironment+"="+root,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	pid := command.Process.Pid
	defer command.Process.Release()

	var status unix.WaitStatus
	if _, err := unix.Wait4(pid, &status, 0, nil); err != nil {
		t.Fatalf("wait for fault child stop: %v", err)
	}
	if !status.Stopped() || status.StopSignal() != unix.SIGSTOP {
		t.Fatalf("fault child initial status = %v", status)
	}
	if err := unix.PtraceSetOptions(pid, unix.PTRACE_O_TRACESYSGOOD); err != nil {
		t.Fatalf("configure fault child tracing: %v", err)
	}

	state := providerSyscallFaultState{scenario: scenario}
	entering := true
	var injectedErrno unix.Errno
	signalToDeliver := 0
	checkExit := func(status unix.WaitStatus) bool {
		if !status.Exited() {
			return false
		}
		if status.ExitStatus() != 0 {
			t.Fatalf("fault child exit = %d", status.ExitStatus())
		}
		if !state.injected {
			t.Fatal("fault child exited without injected syscall failure")
		}
		return true
	}
	for {
		if err := unix.PtraceSyscall(pid, signalToDeliver); err != nil {
			if errors.Is(err, unix.ESRCH) {
				if _, waitErr := unix.Wait4(pid, &status, 0, nil); waitErr == nil && checkExit(status) {
					return
				}
			}
			t.Fatalf("resume fault child: %v", err)
		}
		signalToDeliver = 0
		if _, err := unix.Wait4(pid, &status, 0, nil); err != nil {
			t.Fatalf("wait for fault child: %v", err)
		}
		if checkExit(status) {
			return
		}
		if status.Signaled() {
			t.Fatalf("fault child signal = %v", status.Signal())
		}
		if !status.Stopped() {
			continue
		}
		if status.StopSignal() != unix.Signal(int(unix.SIGTRAP)|0x80) {
			if status.StopSignal() != unix.SIGURG && status.StopSignal() != unix.SIGCHLD {
				signalToDeliver = int(status.StopSignal())
			}
			continue
		}

		var registers unix.PtraceRegs
		if err := unix.PtraceGetRegs(pid, &registers); err != nil {
			t.Fatal(err)
		}
		if entering {
			state.currentCall = registers.Orig_rax
			inject, errno := state.shouldInject(state.currentCall)
			if inject {
				registers.Orig_rax = ^uint64(0)
				if err := unix.PtraceSetRegs(pid, &registers); err != nil {
					t.Fatal(err)
				}
				injectedErrno = errno
				state.injected = true
			} else {
				injectedErrno = 0
			}
		} else {
			if injectedErrno != 0 {
				registers.Rax = uint64(-int64(injectedErrno))
				if err := unix.PtraceSetRegs(pid, &registers); err != nil {
					t.Fatal(err)
				}
			} else if state.currentCall == unix.SYS_MKDIRAT && int64(registers.Rax) >= 0 {
				state.afterMkdir = true
			}
		}
		entering = !entering
	}
}
