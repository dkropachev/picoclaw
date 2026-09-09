//go:build linux && amd64

package databaseclaims

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
	claimFaultScenarioEnvironment = "PICOCLAW_CLAIM_FAULT_SCENARIO"
	claimFaultRootEnvironment     = "PICOCLAW_CLAIM_FAULT_ROOT"
)

// TestMain provides fault children a stable main-thread trace point. Normal
// package tests use the standard testing path.
func TestMain(m *testing.M) {
	scenario := os.Getenv(claimFaultScenarioEnvironment)
	if scenario == "" {
		os.Exit(m.Run())
	}
	code := runClaimSyscallFaultChild(scenario, os.Getenv(claimFaultRootEnvironment))
	writeClaimChildCoverage()
	os.Exit(code)
}

func writeClaimChildCoverage() {
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

func runClaimSyscallFaultChild(scenario, root string) int {
	if root == "" || unix.Gettid() != os.Getpid() {
		return 3
	}
	var validationClaim *unixClaimHandle
	if strings.HasPrefix(scenario, "valid-") {
		claim, err := acquireClaimForTesting(root, strings.Repeat("e", 64))
		if err != nil {
			return 8
		}
		var ok bool
		validationClaim, ok = claim.(*unixClaimHandle)
		if !ok {
			return 9
		}
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
	case "root-missing-ancestors", "root-created-open", "root-created-fstat":
		err = createClaimRootNoFollow(filepath.Join(root, "new"))
	case "root-open", "root-initial-fstat", "root-revalidation-open", "root-close":
		err = createClaimRootNoFollow(root)
	case "root-parent-fstat", "root-mkdir":
		err = createClaimRootNoFollow(filepath.Join(root, "new"))
	case "boundary-missing-ancestors":
		err = validateClaimCreationBoundary(filepath.Join(root, "new"))
	case "boundary-canonicalization":
		err = validateClaimCreationBoundary(root)
	case "lock-fstat", "lock-flock", "lock-reinspection", "lock-root-open",
		"lock-second-inspection", "lock-root-close":
		_, err = acquireClaim(root, strings.Repeat("a", 64))
	case "test-lock-root-open", "test-lock-fstat":
		_, err = acquireClaimForTesting(root, strings.Repeat("b", 64))
	case "valid-root-open", "valid-root-close", "valid-root-descriptor":
		if validationClaim.valid() {
			return 10
		}
		err = errors.New("faulted validation failed closed")
	case "test-root-chmod", "test-root-prepare-fstat", "test-root-reinspect":
		_, err = PrepareRootForTesting(root)
	default:
		return 6
	}
	if err == nil {
		return 7
	}
	return 0
}

type claimSyscallFaultState struct {
	scenario    string
	statCalls   int
	openCalls   int
	closeCalls  int
	afterMkdir  bool
	afterFchmod bool
	injected    bool
	currentCall uint64
	currentFD   uint64
	lockFD      uint64
	lockFDKnown bool
	openingLock bool
	hierarchy   int
}

func (state *claimSyscallFaultState) observeEntry(registers *unix.PtraceRegs) {
	state.currentCall = registers.Orig_rax
	state.currentFD = registers.Rdi
	const lockOpenFlags = unix.O_CREAT | unix.O_RDWR | unix.O_NOFOLLOW
	state.openingLock = state.scenario == "lock-fstat" &&
		state.currentCall == unix.SYS_OPENAT && int64(registers.Rdi) != int64(unix.AT_FDCWD) &&
		int(registers.Rdx)&lockOpenFlags == lockOpenFlags && registers.R10&0o777 == 0o600
}

func (state *claimSyscallFaultState) observeExit(result int64) {
	if state.openingLock && result >= 0 {
		state.lockFD = uint64(result)
		state.lockFDKnown = true
	}
	state.openingLock = false
}

func (state *claimSyscallFaultState) shouldInject(call uint64) (bool, unix.Errno) {
	switch call {
	case unix.SYS_OPENAT:
		state.openCalls++
	case unix.SYS_FSTAT:
		state.statCalls++
	case unix.SYS_CLOSE:
		state.closeCalls++
	}
	switch state.scenario {
	case "root-missing-ancestors", "boundary-missing-ancestors":
		if call == unix.SYS_NEWFSTATAT && state.statCalls < 4 {
			state.statCalls++
			return true, unix.ENOENT
		}
	case "root-open":
		if call == unix.SYS_OPENAT && !state.injected {
			return true, unix.EIO
		}
	case "root-initial-fstat":
		if call == unix.SYS_FSTAT && state.statCalls == 1 {
			return true, unix.EIO
		}
	case "root-revalidation-open":
		if call == unix.SYS_OPENAT && state.openCalls == state.hierarchy+1 {
			return true, unix.EIO
		}
	case "root-parent-fstat":
		if call == unix.SYS_FSTAT && state.statCalls == state.hierarchy+1 {
			return true, unix.EIO
		}
	case "root-mkdir":
		if call == unix.SYS_MKDIRAT && !state.injected {
			return true, unix.EIO
		}
	case "root-close":
		if call == unix.SYS_CLOSE && state.closeCalls == 1 {
			return true, unix.EIO
		}
	case "root-created-open":
		if state.afterMkdir && call == unix.SYS_OPENAT && !state.injected {
			return true, unix.EIO
		}
	case "root-created-fstat":
		if state.afterMkdir && call == unix.SYS_FSTAT && !state.injected {
			return true, unix.EIO
		}
	case "boundary-canonicalization":
		if call == unix.SYS_NEWFSTATAT {
			state.statCalls++
			if state.statCalls == 2 {
				return true, unix.EIO
			}
		}
	case "lock-fstat":
		if call == unix.SYS_FSTAT && state.lockFDKnown && state.currentFD == state.lockFD &&
			!state.injected {
			return true, unix.EIO
		}
	case "lock-root-open":
		if call == unix.SYS_OPENAT && state.openCalls == state.hierarchy+1 {
			return true, unix.EIO
		}
	case "lock-second-inspection":
		if call == unix.SYS_OPENAT && state.openCalls == state.hierarchy+2 {
			return true, unix.EIO
		}
	case "lock-root-close":
		if call == unix.SYS_CLOSE && state.closeCalls == 2*state.hierarchy+1 {
			return true, unix.EIO
		}
	case "test-lock-root-open":
		if call == unix.SYS_OPENAT && state.openCalls == 1 {
			return true, unix.EIO
		}
	case "test-lock-fstat", "test-root-prepare-fstat":
		if call == unix.SYS_FSTAT && state.statCalls == 1 {
			return true, unix.EIO
		}
	case "valid-root-open":
		// Target the root reopen after inspectUnixClaimRoot has completed,
		// rather than assuming it is the process's second observed openat.
		// Ptrace can observe restarted or runtime open calls differently across
		// hosted-runner kernels, while the completed fstat+close phase is stable.
		if call == unix.SYS_OPENAT && state.statCalls >= 1 && state.closeCalls >= 1 &&
			!state.injected {
			return true, unix.EIO
		}
	case "valid-root-close":
		if call == unix.SYS_CLOSE && state.closeCalls == 2 {
			return true, unix.EIO
		}
	case "valid-root-descriptor":
		if call == unix.SYS_FSTAT && state.statCalls == 2 {
			return true, unix.EIO
		}
	case "test-root-chmod":
		if call == unix.SYS_FCHMOD && !state.injected {
			return true, unix.EIO
		}
	case "test-root-reinspect":
		if state.afterFchmod && call == unix.SYS_NEWFSTATAT && !state.injected {
			return true, unix.EIO
		}
	case "lock-flock":
		if call == unix.SYS_FLOCK && !state.injected {
			return true, unix.EIO
		}
	case "lock-reinspection":
		if call == unix.SYS_NEWFSTATAT && !state.injected {
			return true, unix.EIO
		}
	}
	return false, 0
}

func TestClaimSyscallFaultValidRootOpenUsesCompletedInspectionPhase(t *testing.T) {
	state := claimSyscallFaultState{scenario: "valid-root-open"}
	if inject, _ := state.shouldInject(unix.SYS_OPENAT); inject {
		t.Fatal("valid-root reopen fault injected before root inspection")
	}
	state.shouldInject(unix.SYS_FSTAT)
	state.shouldInject(unix.SYS_CLOSE)
	if inject, errno := state.shouldInject(unix.SYS_OPENAT); !inject || errno != unix.EIO {
		t.Fatalf("valid-root reopen fault = %t, %v", inject, errno)
	}
}

func TestClaimSyscallFaultLockFstatUsesCapturedDescriptor(t *testing.T) {
	state := claimSyscallFaultState{scenario: "lock-fstat"}
	runtimeDirectory := int64(unix.AT_FDCWD)
	runtimeOpen := unix.PtraceRegs{
		Orig_rax: unix.SYS_OPENAT, Rdi: uint64(runtimeDirectory),
		Rdx: unix.O_CREAT | unix.O_RDWR | unix.O_NOFOLLOW, R10: 0o600,
	}
	state.observeEntry(&runtimeOpen)
	state.observeExit(41)
	if state.lockFDKnown {
		t.Fatal("runtime openat was captured as the claim lock")
	}
	lockOpen := runtimeOpen
	lockOpen.Rdi = 7
	state.observeEntry(&lockOpen)
	state.observeExit(42)
	state.currentFD = 41
	if inject, _ := state.shouldInject(unix.SYS_FSTAT); inject {
		t.Fatal("unrelated descriptor fstat was faulted")
	}
	state.currentFD = 42
	if inject, errno := state.shouldInject(unix.SYS_FSTAT); !inject || errno != unix.EIO {
		t.Fatalf("claim lock descriptor fault = %t, %v", inject, errno)
	}
	state.injected = true
	if inject, _ := state.shouldInject(unix.SYS_FSTAT); inject {
		t.Fatal("claim lock descriptor was faulted twice")
	}
}

func TestCoverageClaimSyscallFailureBranches(t *testing.T) {
	for _, scenario := range []string{
		"root-open",
		"root-initial-fstat",
		"root-revalidation-open",
		"root-parent-fstat",
		"root-mkdir",
		"root-close",
		"root-created-open",
		"root-created-fstat",
		"boundary-missing-ancestors",
		"lock-fstat",
		"lock-root-open",
		"lock-second-inspection",
		"lock-root-close",
		"lock-flock",
		"lock-reinspection",
		"test-lock-root-open",
		"test-lock-fstat",
		"valid-root-open",
		"valid-root-close",
		"valid-root-descriptor",
		"test-root-chmod",
		"test-root-prepare-fstat",
		"test-root-reinspect",
	} {
		t.Run(scenario, func(t *testing.T) {
			runClaimSyscallFault(t, scenario)
		})
	}
}

func runClaimSyscallFault(t *testing.T, scenario string) {
	t.Helper()
	// Linux records ptrace ownership against one tracer thread. Keep process
	// creation, waits, and tracing on that same thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	root := productionHierarchyTestRoot(t)

	arguments := []string{"-test.run=^$"}
	for _, argument := range os.Args {
		if strings.HasPrefix(argument, "-test.gocoverdir=") {
			arguments = append(arguments, argument)
			break
		}
	}
	command := exec.Command(os.Args[0], arguments...)
	command.Env = append(os.Environ(),
		claimFaultScenarioEnvironment+"="+scenario,
		claimFaultRootEnvironment+"="+root,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	pid := command.Process.Pid
	defer command.Process.Release()

	var status unix.WaitStatus
	if _, err := unix.Wait4(pid, &status, 0, nil); err != nil {
		t.Fatalf("wait for claim fault child stop: %v", err)
	}
	if !status.Stopped() || status.StopSignal() != unix.SIGSTOP {
		t.Fatalf("claim fault child initial status = %v", status)
	}
	if err := unix.PtraceSetOptions(pid, unix.PTRACE_O_TRACESYSGOOD); err != nil {
		t.Fatalf("configure claim fault child tracing: %v", err)
	}

	cleanRoot := strings.TrimPrefix(filepath.Clean(root), string(os.PathSeparator))
	hierarchy := 1
	if cleanRoot != "" {
		hierarchy += len(strings.Split(cleanRoot, string(os.PathSeparator)))
	}
	state := claimSyscallFaultState{scenario: scenario, hierarchy: hierarchy}
	entering := true
	var injectedErrno unix.Errno
	signalToDeliver := 0
	checkExit := func(status unix.WaitStatus) bool {
		if !status.Exited() {
			return false
		}
		if status.ExitStatus() != 0 {
			t.Fatalf("claim fault child exit = %d", status.ExitStatus())
		}
		if !state.injected {
			t.Fatal("claim fault child exited without injected syscall failure")
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
			t.Fatalf("resume claim fault child: %v", err)
		}
		signalToDeliver = 0
		if _, err := unix.Wait4(pid, &status, 0, nil); err != nil {
			t.Fatalf("wait for claim fault child: %v", err)
		}
		if checkExit(status) {
			return
		}
		if status.Signaled() {
			t.Fatalf("claim fault child signal = %v", status.Signal())
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
			} else if state.currentCall == unix.SYS_FCHMOD && int64(registers.Rax) >= 0 {
				state.afterFchmod = true
			}
		}
		entering = !entering
	}
}
