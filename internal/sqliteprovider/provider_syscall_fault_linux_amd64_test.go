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
	scenario       string
	fstatCalls     int
	statCalls      int
	afterMkdir     bool
	injected       bool
	currentCall    uint64
	currentFD      uint64
	parentFD       uint64
	createdFD      uint64
	createdKnown   bool
	openingCreated bool
}

func (state *providerSyscallFaultState) observeEntry(registers *unix.PtraceRegs) {
	state.currentCall = registers.Orig_rax
	state.currentFD = registers.Rdi
	createdScenario := state.scenario == "created-component-fstat" ||
		state.scenario == "created-component-fchmod" ||
		state.scenario == "created-parent-fsync" ||
		state.scenario == "created-component-fsync"
	flags := int(registers.Rdx)
	state.openingCreated = createdScenario && state.afterMkdir &&
		state.currentCall == unix.SYS_OPENAT && int64(registers.Rdi) >= 0 &&
		flags&(unix.O_DIRECTORY|unix.O_NOFOLLOW) == unix.O_DIRECTORY|unix.O_NOFOLLOW &&
		flags&unix.O_ACCMODE == unix.O_RDONLY && flags&unix.O_CREAT == 0
}

func (state *providerSyscallFaultState) observeExit(result int64) {
	if state.openingCreated && result >= 0 {
		state.parentFD = state.currentFD
		state.createdFD = uint64(result)
		state.createdKnown = true
	}
	state.openingCreated = false
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
		if call == unix.SYS_FSTAT && state.createdKnown && state.currentFD == state.createdFD &&
			!state.injected {
			return true, unix.EIO
		}
	case "created-component-fchmod":
		if call == unix.SYS_FCHMOD && state.createdKnown && state.currentFD == state.createdFD &&
			!state.injected {
			return true, unix.EIO
		}
	case "created-parent-fsync":
		if call == unix.SYS_FSYNC && state.createdKnown && state.currentFD == state.parentFD &&
			!state.injected {
			return true, unix.EIO
		}
	case "created-component-fsync":
		if call == unix.SYS_FSYNC && state.createdKnown && state.currentFD == state.createdFD &&
			!state.injected {
			return true, unix.EIO
		}
	}
	return false, 0
}

func TestProviderCreatedComponentFaultsUseCapturedDescriptors(t *testing.T) {
	runtimeDirectory := int64(unix.AT_FDCWD)
	runtimeOpen := unix.PtraceRegs{
		Orig_rax: unix.SYS_OPENAT, Rdi: uint64(runtimeDirectory),
		Rdx: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW,
	}
	createdOpen := runtimeOpen
	createdOpen.Rdi = 7
	for _, test := range []struct {
		scenario string
		call     uint64
		fd       uint64
	}{
		{scenario: "created-component-fstat", call: unix.SYS_FSTAT, fd: 42},
		{scenario: "created-component-fchmod", call: unix.SYS_FCHMOD, fd: 42},
		{scenario: "created-parent-fsync", call: unix.SYS_FSYNC, fd: 7},
		{scenario: "created-component-fsync", call: unix.SYS_FSYNC, fd: 42},
	} {
		t.Run(test.scenario, func(t *testing.T) {
			state := providerSyscallFaultState{scenario: test.scenario, afterMkdir: true}
			state.observeEntry(&runtimeOpen)
			state.observeExit(41)
			if state.createdKnown {
				t.Fatal("runtime directory openat was captured as the provider component")
			}
			state.observeEntry(&createdOpen)
			state.observeExit(42)
			state.currentFD = 41
			if inject, _ := state.shouldInject(test.call); inject {
				t.Fatal("unrelated descriptor syscall was faulted")
			}
			state.currentFD = test.fd
			if inject, errno := state.shouldInject(test.call); !inject || errno != unix.EIO {
				t.Fatalf("created provider descriptor fault = %t, %v", inject, errno)
			}
			state.injected = true
			if inject, _ := state.shouldInject(test.call); inject {
				t.Fatal("created provider descriptor was faulted twice")
			}
		})
	}
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
			state.observeEntry(&registers)
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
			state.observeExit(int64(registers.Rax))
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
