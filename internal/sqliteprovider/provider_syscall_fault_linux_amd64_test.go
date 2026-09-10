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
	providerFaultMarkerTag           = uintptr(0x50434c415750524f)
	providerFaultMarkerBegin         = uintptr(1)
	providerFaultMarkerEnd           = uintptr(2)
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
	runtime.LockOSThread()
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
	providerSyscallFaultMarker(providerFaultMarkerBegin)

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
	providerSyscallFaultMarker(providerFaultMarkerEnd)
	if err == nil {
		return 7
	}
	return 0
}

func providerSyscallFaultMarker(phase uintptr) {
	result, secondary, errno := unix.RawSyscall6(
		unix.SYS_GETPID, providerFaultMarkerTag, phase, 0, 0, 0, 0,
	)
	_ = result
	_ = secondary
	_ = errno
}

type providerSyscallFaultState struct {
	scenario string

	scoped       bool
	targetActive bool
	injected     bool
	statCalls    int

	currentCall   uint64
	currentFD     uint64
	currentSecond uint64
	currentFourth uint64

	openingRoot      bool
	openingComponent bool
	openingCreated   bool
	creatingTarget   bool

	rootFD         uint64
	rootKnown      bool
	hierarchyFD    uint64
	hierarchyKnown bool
	componentFD    uint64
	componentKnown bool
	createdParent  uint64
	parentKnown    bool
	createdFD      uint64
	createdKnown   bool
}

func (state *providerSyscallFaultState) observeEntry(registers *unix.PtraceRegs) {
	state.currentCall = registers.Orig_rax
	state.currentFD = registers.Rdi
	state.currentSecond = registers.Rsi
	state.currentFourth = registers.R10
	state.openingRoot = false
	state.openingComponent = false
	state.openingCreated = false
	state.creatingTarget = false
	if state.currentCall == unix.SYS_GETPID && registers.Rdi == uint64(providerFaultMarkerTag) {
		state.targetActive = registers.Rsi == uint64(providerFaultMarkerBegin)
		return
	}
	if state.scoped && !state.targetActive {
		return
	}

	directoryOpen := providerFaultDirectoryOpen(registers)
	state.openingRoot = directoryOpen && providerFaultAtWorkingDirectory(registers.Rdi)
	state.openingComponent = directoryOpen && state.hierarchyKnown &&
		registers.Rdi == state.hierarchyFD
	state.openingCreated = providerFaultCreatedScenario(state.scenario) &&
		state.parentKnown && directoryOpen && registers.Rdi == state.createdParent
	state.creatingTarget = providerFaultCreatedScenario(state.scenario) &&
		state.hierarchyKnown && state.currentCall == unix.SYS_MKDIRAT &&
		registers.Rdi == state.hierarchyFD && registers.Rdx == 0o700
}

func (state *providerSyscallFaultState) observeExit(result int64) {
	if state.openingRoot && result >= 0 {
		state.rootFD = uint64(result)
		state.rootKnown = true
		state.hierarchyFD = uint64(result)
		state.hierarchyKnown = true
	}
	if state.openingComponent && result >= 0 {
		if state.scenario == "directory-component-fstat" && !state.componentKnown {
			state.componentFD = uint64(result)
			state.componentKnown = true
		}
		state.hierarchyFD = uint64(result)
		state.hierarchyKnown = true
	}
	if state.creatingTarget && result >= 0 {
		state.createdParent = state.currentFD
		state.parentKnown = true
	}
	if state.openingCreated && result >= 0 {
		state.createdFD = uint64(result)
		state.createdKnown = true
	}
	if state.currentCall == unix.SYS_CLOSE && result >= 0 {
		state.clearClosedFD(state.currentFD)
	}
	state.openingRoot = false
	state.openingComponent = false
	state.openingCreated = false
	state.creatingTarget = false
}

func (state *providerSyscallFaultState) shouldInject(call uint64) (bool, unix.Errno) {
	if state.scoped && !state.targetActive ||
		state.injected && state.scenario != "missing-ancestors" {
		return false, 0
	}
	switch state.scenario {
	case "missing-ancestors":
		if call == unix.SYS_NEWFSTATAT && providerFaultAtWorkingDirectory(state.currentFD) &&
			state.currentFourth == unix.AT_SYMLINK_NOFOLLOW && state.statCalls < 4 {
			state.statCalls++
			return true, unix.ENOENT
		}
	case "directory-root-open", "file-root-open":
		if state.openingRoot {
			return true, unix.EIO
		}
	case "directory-root-fstat":
		if call == unix.SYS_FSTAT && state.rootKnown && state.currentFD == state.rootFD {
			return true, unix.EIO
		}
	case "directory-component-fstat":
		if call == unix.SYS_FSTAT && state.componentKnown && state.currentFD == state.componentFD {
			return true, unix.EIO
		}
	case "created-component-fstat":
		if call == unix.SYS_FSTAT && state.createdKnown && state.currentFD == state.createdFD {
			return true, unix.EIO
		}
	case "created-component-fchmod":
		if call == unix.SYS_FCHMOD && state.createdKnown && state.currentFD == state.createdFD &&
			state.currentSecond == 0o700 {
			return true, unix.EIO
		}
	case "created-parent-fsync":
		if call == unix.SYS_FSYNC && state.createdKnown && state.parentKnown &&
			state.currentFD == state.createdParent {
			return true, unix.EIO
		}
	case "created-component-fsync":
		if call == unix.SYS_FSYNC && state.createdKnown && state.currentFD == state.createdFD {
			return true, unix.EIO
		}
	}
	return false, 0
}

func providerFaultDirectoryOpen(registers *unix.PtraceRegs) bool {
	const flags = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_DIRECTORY | unix.O_NOFOLLOW
	return registers.Orig_rax == unix.SYS_OPENAT && int(registers.Rdx) == flags|unix.O_LARGEFILE
}

func providerFaultAtWorkingDirectory(fd uint64) bool {
	return int64(fd) == int64(unix.AT_FDCWD)
}

func providerFaultCreatedScenario(scenario string) bool {
	return scenario == "created-component-fstat" ||
		scenario == "created-component-fchmod" ||
		scenario == "created-parent-fsync" ||
		scenario == "created-component-fsync"
}

func (state *providerSyscallFaultState) clearClosedFD(fd uint64) {
	if state.rootKnown && fd == state.rootFD {
		state.rootKnown = false
	}
	if state.hierarchyKnown && fd == state.hierarchyFD {
		state.hierarchyKnown = false
	}
	if state.componentKnown && fd == state.componentFD {
		state.componentKnown = false
	}
	if state.parentKnown && fd == state.createdParent {
		state.parentKnown = false
	}
	if state.createdKnown && fd == state.createdFD {
		state.createdKnown = false
	}
}

func TestProviderSyscallFaultScopeMarkers(t *testing.T) {
	state := providerSyscallFaultState{scenario: "directory-root-open", scoped: true}
	rootOpen := providerFaultTestDirectoryOpen(unix.AT_FDCWD)
	state.observeEntry(&rootOpen)
	if inject, _ := state.shouldInject(state.currentCall); inject {
		t.Fatal("provider syscall before the begin marker was faulted")
	}

	begin := unix.PtraceRegs{
		Orig_rax: unix.SYS_GETPID,
		Rdi:      uint64(providerFaultMarkerTag),
		Rsi:      uint64(providerFaultMarkerBegin),
	}
	state.observeEntry(&begin)
	if !state.targetActive {
		t.Fatal("begin marker did not activate provider fault selection")
	}
	state.observeEntry(&rootOpen)
	if inject, errno := state.shouldInject(state.currentCall); !inject || errno != unix.EIO {
		t.Fatalf("scoped provider root fault = %t, %v", inject, errno)
	}

	end := begin
	end.Rsi = uint64(providerFaultMarkerEnd)
	state.observeEntry(&end)
	if state.targetActive {
		t.Fatal("end marker did not deactivate provider fault selection")
	}
	state.observeEntry(&rootOpen)
	if inject, _ := state.shouldInject(state.currentCall); inject {
		t.Fatal("provider syscall after the end marker was faulted")
	}
}

func TestProviderRootAndComponentFaultsUseDescriptorLineage(t *testing.T) {
	t.Run("root-open-shape", func(t *testing.T) {
		state := providerSyscallFaultState{scenario: "directory-root-open"}
		relativeOpen := providerFaultTestDirectoryOpen(7)
		state.observeEntry(&relativeOpen)
		if inject, _ := state.shouldInject(state.currentCall); inject {
			t.Fatal("relative directory open was mistaken for the root open")
		}
		wrongFlags := providerFaultTestDirectoryOpen(unix.AT_FDCWD)
		wrongFlags.Rdx &^= unix.O_NOFOLLOW
		state.observeEntry(&wrongFlags)
		if inject, _ := state.shouldInject(state.currentCall); inject {
			t.Fatal("root open with the wrong flags was faulted")
		}
		rootOpen := providerFaultTestDirectoryOpen(unix.AT_FDCWD)
		state.observeEntry(&rootOpen)
		if inject, errno := state.shouldInject(state.currentCall); !inject || errno != unix.EIO {
			t.Fatalf("provider root open fault = %t, %v", inject, errno)
		}
	})

	t.Run("root-fstat", func(t *testing.T) {
		state := providerSyscallFaultState{scenario: "directory-root-fstat"}
		providerFaultCaptureRoot(&state, 9)
		unrelated := providerFaultTestDescriptorCall(unix.SYS_FSTAT, 10)
		state.observeEntry(&unrelated)
		if inject, _ := state.shouldInject(state.currentCall); inject {
			t.Fatal("unrelated fstat was faulted as the root descriptor")
		}
		rootFstat := providerFaultTestDescriptorCall(unix.SYS_FSTAT, 9)
		state.observeEntry(&rootFstat)
		if inject, errno := state.shouldInject(state.currentCall); !inject || errno != unix.EIO {
			t.Fatalf("provider root fstat fault = %t, %v", inject, errno)
		}
		closeRoot := providerFaultTestDescriptorCall(unix.SYS_CLOSE, 9)
		state.observeEntry(&closeRoot)
		state.observeExit(0)
		state.observeEntry(&rootFstat)
		if inject, _ := state.shouldInject(state.currentCall); inject {
			t.Fatal("reused closed root descriptor was faulted")
		}
	})

	t.Run("component-fstat", func(t *testing.T) {
		state := providerSyscallFaultState{scenario: "directory-component-fstat"}
		providerFaultCaptureRoot(&state, 9)
		unrelatedOpen := providerFaultTestDirectoryOpen(8)
		state.observeEntry(&unrelatedOpen)
		state.observeExit(11)
		if state.componentKnown {
			t.Fatal("directory outside the root lineage was captured as a component")
		}
		componentOpen := providerFaultTestDirectoryOpen(9)
		state.observeEntry(&componentOpen)
		state.observeExit(12)
		unrelated := providerFaultTestDescriptorCall(unix.SYS_FSTAT, 11)
		state.observeEntry(&unrelated)
		if inject, _ := state.shouldInject(state.currentCall); inject {
			t.Fatal("unrelated component fstat was faulted")
		}
		componentFstat := providerFaultTestDescriptorCall(unix.SYS_FSTAT, 12)
		state.observeEntry(&componentFstat)
		if inject, errno := state.shouldInject(state.currentCall); !inject || errno != unix.EIO {
			t.Fatalf("provider component fstat fault = %t, %v", inject, errno)
		}
		closeComponent := providerFaultTestDescriptorCall(unix.SYS_CLOSE, 12)
		state.observeEntry(&closeComponent)
		state.observeExit(0)
		state.observeEntry(&componentFstat)
		if inject, _ := state.shouldInject(state.currentCall); inject {
			t.Fatal("reused closed component descriptor was faulted")
		}
	})
}

func TestProviderCreatedComponentFaultsUseCapturedDescriptors(t *testing.T) {
	for _, test := range []struct {
		scenario string
		call     uint64
		fd       int
		second   uint64
	}{
		{scenario: "created-component-fstat", call: unix.SYS_FSTAT, fd: 42},
		{scenario: "created-component-fchmod", call: unix.SYS_FCHMOD, fd: 42, second: 0o700},
		{scenario: "created-parent-fsync", call: unix.SYS_FSYNC, fd: 7},
		{scenario: "created-component-fsync", call: unix.SYS_FSYNC, fd: 42},
	} {
		t.Run(test.scenario, func(t *testing.T) {
			state := providerSyscallFaultState{scenario: test.scenario}
			providerFaultCaptureCreatedDirectory(t, &state)

			unrelated := providerFaultTestDescriptorCall(test.call, 41)
			unrelated.Rsi = test.second
			state.observeEntry(&unrelated)
			if inject, _ := state.shouldInject(state.currentCall); inject {
				t.Fatal("unrelated descriptor syscall was faulted")
			}
			if test.call == unix.SYS_FCHMOD {
				wrongMode := providerFaultTestDescriptorCall(test.call, test.fd)
				wrongMode.Rsi = 0o755
				state.observeEntry(&wrongMode)
				if inject, _ := state.shouldInject(state.currentCall); inject {
					t.Fatal("created provider descriptor chmod with the wrong mode was faulted")
				}
			}
			target := providerFaultTestDescriptorCall(test.call, test.fd)
			target.Rsi = test.second
			state.observeEntry(&target)
			if inject, errno := state.shouldInject(state.currentCall); !inject || errno != unix.EIO {
				t.Fatalf("created provider descriptor fault = %t, %v", inject, errno)
			}
			state.injected = true
			if inject, _ := state.shouldInject(state.currentCall); inject {
				t.Fatal("created provider descriptor was faulted twice")
			}
		})
	}
}

func TestProviderCreatedFaultRolesAreClearedOnClose(t *testing.T) {
	componentState := providerSyscallFaultState{scenario: "created-component-fstat"}
	providerFaultCaptureCreatedDirectory(t, &componentState)
	closeComponent := providerFaultTestDescriptorCall(unix.SYS_CLOSE, 42)
	componentState.observeEntry(&closeComponent)
	componentState.observeExit(0)
	componentFstat := providerFaultTestDescriptorCall(unix.SYS_FSTAT, 42)
	componentState.observeEntry(&componentFstat)
	if inject, _ := componentState.shouldInject(componentState.currentCall); inject {
		t.Fatal("reused closed created-component descriptor was faulted")
	}

	parentState := providerSyscallFaultState{scenario: "created-parent-fsync"}
	providerFaultCaptureCreatedDirectory(t, &parentState)
	closeParent := providerFaultTestDescriptorCall(unix.SYS_CLOSE, 7)
	parentState.observeEntry(&closeParent)
	parentState.observeExit(0)
	parentFsync := providerFaultTestDescriptorCall(unix.SYS_FSYNC, 7)
	parentState.observeEntry(&parentFsync)
	if inject, _ := parentState.shouldInject(parentState.currentCall); inject {
		t.Fatal("reused closed created-parent descriptor was faulted")
	}
}

func TestProviderMissingAncestorFaultMatchesLstatShape(t *testing.T) {
	state := providerSyscallFaultState{scenario: "missing-ancestors"}
	wrongDirectory := providerFaultTestDescriptorCall(unix.SYS_NEWFSTATAT, 7)
	wrongDirectory.R10 = unix.AT_SYMLINK_NOFOLLOW
	state.observeEntry(&wrongDirectory)
	if inject, _ := state.shouldInject(state.currentCall); inject {
		t.Fatal("descriptor-relative stat was faulted as an ancestor lstat")
	}
	wrongFlags := providerFaultTestDescriptorCall(unix.SYS_NEWFSTATAT, unix.AT_FDCWD)
	state.observeEntry(&wrongFlags)
	if inject, _ := state.shouldInject(state.currentCall); inject {
		t.Fatal("stat with the wrong flags was faulted as an ancestor lstat")
	}
	ancestorLstat := providerFaultTestDescriptorCall(unix.SYS_NEWFSTATAT, unix.AT_FDCWD)
	ancestorLstat.R10 = unix.AT_SYMLINK_NOFOLLOW
	for index := 0; index < 4; index++ {
		state.observeEntry(&ancestorLstat)
		if inject, errno := state.shouldInject(state.currentCall); !inject || errno != unix.ENOENT {
			t.Fatalf("ancestor lstat fault %d = %t, %v", index+1, inject, errno)
		}
	}
	state.observeEntry(&ancestorLstat)
	if inject, _ := state.shouldInject(state.currentCall); inject {
		t.Fatal("fifth ancestor lstat was faulted")
	}
}

func providerFaultCaptureRoot(state *providerSyscallFaultState, fd int) {
	rootOpen := providerFaultTestDirectoryOpen(unix.AT_FDCWD)
	state.observeEntry(&rootOpen)
	state.observeExit(int64(fd))
}

func providerFaultCaptureCreatedDirectory(t *testing.T, state *providerSyscallFaultState) {
	t.Helper()
	providerFaultCaptureRoot(state, 5)
	componentOpen := providerFaultTestDirectoryOpen(5)
	state.observeEntry(&componentOpen)
	state.observeExit(7)

	wrongParentMkdir := providerFaultTestDescriptorCall(unix.SYS_MKDIRAT, 5)
	wrongParentMkdir.Rdx = 0o700
	state.observeEntry(&wrongParentMkdir)
	state.observeExit(0)
	wrongModeMkdir := providerFaultTestDescriptorCall(unix.SYS_MKDIRAT, 7)
	wrongModeMkdir.Rdx = 0o755
	state.observeEntry(&wrongModeMkdir)
	state.observeExit(0)
	if state.parentKnown {
		t.Fatal("mkdirat with the wrong parent or mode armed created-component capture")
	}

	mkdir := providerFaultTestDescriptorCall(unix.SYS_MKDIRAT, 7)
	mkdir.Rdx = 0o700
	state.observeEntry(&mkdir)
	state.observeExit(0)
	unrelatedOpen := providerFaultTestDirectoryOpen(8)
	state.observeEntry(&unrelatedOpen)
	state.observeExit(41)
	wrongFlags := providerFaultTestDirectoryOpen(7)
	wrongFlags.Rdx &^= unix.O_NOFOLLOW
	state.observeEntry(&wrongFlags)
	state.observeExit(41)
	if state.createdKnown {
		t.Fatal("unrelated openat was captured as the created provider component")
	}

	createdOpen := providerFaultTestDirectoryOpen(7)
	state.observeEntry(&createdOpen)
	state.observeExit(42)
	if !state.createdKnown || state.createdFD != 42 || !state.parentKnown || state.createdParent != 7 {
		t.Fatalf(
			"created descriptor lineage = component(%t, %d), parent(%t, %d)",
			state.createdKnown, state.createdFD, state.parentKnown, state.createdParent,
		)
	}
}

func providerFaultTestDirectoryOpen(fd int) unix.PtraceRegs {
	const flags = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_DIRECTORY | unix.O_NOFOLLOW
	return unix.PtraceRegs{
		Orig_rax: unix.SYS_OPENAT,
		Rdi:      uint64(int64(fd)),
		Rdx:      flags | unix.O_LARGEFILE,
	}
}

func providerFaultTestDescriptorCall(call uint64, fd int) unix.PtraceRegs {
	return unix.PtraceRegs{Orig_rax: call, Rdi: uint64(int64(fd))}
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

	state := providerSyscallFaultState{scenario: scenario, scoped: true}
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
			}
		}
		entering = !entering
	}
}
