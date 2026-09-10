//go:build linux && amd64

package databaseclaims

import (
	"bytes"
	"errors"
	"fmt"
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
	claimFaultMarkerTag           = uintptr(0x50434c41574d4152)
	claimFaultMarkerBegin         = uintptr(1)
	claimFaultMarkerEnd           = uintptr(2)
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
	runtime.LockOSThread()
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
	claimSyscallFaultMarker(claimFaultMarkerBegin)

	var err error
	switch scenario {
	case "root-created-open", "root-created-fstat":
		err = createClaimRootNoFollow(filepath.Join(root, "new"))
	case "root-open", "root-initial-fstat", "root-revalidation-open", "root-close":
		err = createClaimRootNoFollow(root)
	case "root-parent-fstat", "root-mkdir":
		err = createClaimRootNoFollow(filepath.Join(root, "new"))
	case "boundary-missing-ancestors":
		err = validateClaimCreationBoundary(filepath.Join(root, "new"))
	case "lock-fstat", "lock-flock", "lock-reinspection", "lock-root-open",
		"lock-second-inspection", "lock-root-close":
		_, err = acquireClaim(root, strings.Repeat("a", 64))
	case "test-lock-root-open", "test-lock-fstat":
		_, err = acquireClaimForTesting(root, strings.Repeat("b", 64))
	case "valid-root-open", "valid-root-close", "valid-root-descriptor":
		if validationClaim.valid() {
			err = nil
		} else {
			err = errors.New("faulted validation failed closed")
		}
	case "test-root-chmod", "test-root-prepare-fstat", "test-root-reinspect":
		_, err = PrepareRootForTesting(root)
	default:
		return 6
	}
	claimSyscallFaultMarker(claimFaultMarkerEnd)
	if err == nil {
		return 7
	}
	return 0
}

func claimSyscallFaultMarker(phase uintptr) {
	result, secondary, errno := unix.RawSyscall6(
		unix.SYS_GETPID, claimFaultMarkerTag, phase, 0, 0, 0, 0,
	)
	_ = result
	_ = secondary
	_ = errno
}

type claimSyscallFaultState struct {
	scenario   string
	root       string
	targetPath string
	lockName   string

	scoped       bool
	targetActive bool
	injected     bool

	pathStatCalls int
	slashOpens    int
	rootOpens     int
	afterFchmod   bool
	afterFlock    bool

	currentCall uint64
	currentFD   uint64
	currentPath string
	currentArg  uint64
	currentFlag uint64

	openingDirectory     bool
	currentDirectoryPath string
	currentSlashOpen     int
	currentRootOpen      int
	openingLock          bool
	openingCreated       bool
	openingMissingTarget bool
	creatingTarget       bool

	directories map[uint64]string

	firstSlashFD       uint64
	firstSlashKnown    bool
	firstRootFD        uint64
	firstRootKnown     bool
	secondRootFD       uint64
	secondRootKnown    bool
	firstRootStat      bool
	firstRootClosed    bool
	missingParentFD    uint64
	missingParentKnown bool
	createdFD          uint64
	createdKnown       bool
	createdParent      uint64
	lockFD             uint64
	lockFDKnown        bool
}

func newClaimSyscallFaultState(scenario, root string, scoped bool) claimSyscallFaultState {
	state := claimSyscallFaultState{
		scenario: scenario,
		root:     filepath.Clean(root),
		scoped:   scoped,
	}
	switch scenario {
	case "root-parent-fstat", "root-mkdir", "root-created-open", "root-created-fstat",
		"boundary-missing-ancestors":
		state.targetPath = filepath.Join(state.root, "new")
	}
	switch {
	case strings.HasPrefix(scenario, "lock-"):
		state.lockName = strings.Repeat("a", 64) + ".lock"
	case strings.HasPrefix(scenario, "test-lock-"):
		state.lockName = strings.Repeat("b", 64) + ".lock"
	case strings.HasPrefix(scenario, "valid-"):
		state.lockName = strings.Repeat("e", 64) + ".lock"
	}
	return state
}

func (state *claimSyscallFaultState) observeEntry(registers *unix.PtraceRegs, path string) {
	state.currentCall = registers.Orig_rax
	state.currentFD = registers.Rdi
	state.currentPath = path
	state.currentArg = registers.Rsi
	state.currentFlag = registers.R10
	state.resetEntryClassification()
	if state.currentCall == unix.SYS_GETPID && registers.Rdi == uint64(claimFaultMarkerTag) {
		state.targetActive = registers.Rsi == uint64(claimFaultMarkerBegin)
		return
	}
	if state.scoped && !state.targetActive {
		return
	}
	state.openingDirectory = claimFaultDirectoryOpen(registers)
	if state.openingDirectory {
		state.currentDirectoryPath = state.resolvePath(registers.Rdi, path)
		if state.currentDirectoryPath == string(os.PathSeparator) {
			state.slashOpens++
			state.currentSlashOpen = state.slashOpens
		}
		if claimFaultAtCWD(registers.Rdi) && state.currentDirectoryPath == state.root {
			state.rootOpens++
			state.currentRootOpen = state.rootOpens
		}
	}
	const lockOpenFlags = unix.O_CREAT | unix.O_RDWR | unix.O_NOFOLLOW
	state.openingLock = claimFaultNeedsLockFD(state.scenario) && state.firstRootKnown &&
		state.currentCall == unix.SYS_OPENAT && registers.Rdi == state.firstRootFD &&
		path == state.lockName && int(registers.Rdx)&lockOpenFlags == lockOpenFlags &&
		registers.R10&0o777 == 0o600
	createdScenario := state.scenario == "root-created-open" ||
		state.scenario == "root-created-fstat"
	state.openingMissingTarget = claimFaultCreationScenario(state.scenario) &&
		state.openingDirectory && state.currentDirectoryPath == state.targetPath
	state.openingCreated = createdScenario && state.missingParentKnown &&
		state.currentCall == unix.SYS_OPENAT && registers.Rdi == state.createdParent &&
		state.currentDirectoryPath == state.targetPath && state.openingDirectory
	state.creatingTarget = claimFaultCreationScenario(state.scenario) && state.missingParentKnown &&
		state.currentCall == unix.SYS_MKDIRAT && registers.Rdi == state.missingParentFD &&
		state.resolvePath(registers.Rdi, path) == state.targetPath && registers.Rdx&0o777 == 0o700
}

func (state *claimSyscallFaultState) observeExit(result int64) {
	if state.openingDirectory && result >= 0 {
		if state.directories == nil {
			state.directories = make(map[uint64]string)
		}
		fd := uint64(result)
		state.directories[fd] = state.currentDirectoryPath
		switch state.currentSlashOpen {
		case 1:
			state.firstSlashFD = fd
			state.firstSlashKnown = true
		}
		switch state.currentRootOpen {
		case 1:
			state.firstRootFD = fd
			state.firstRootKnown = true
		case 2:
			state.secondRootFD = fd
			state.secondRootKnown = true
		}
	}
	if state.openingMissingTarget && result == -int64(unix.ENOENT) {
		state.missingParentFD = state.currentFD
		state.missingParentKnown = true
	}
	if state.creatingTarget && result >= 0 {
		state.createdParent = state.currentFD
	}
	if state.openingLock && result >= 0 {
		state.lockFD = uint64(result)
		state.lockFDKnown = true
	}
	if state.openingCreated && result >= 0 {
		state.createdFD = uint64(result)
		state.createdKnown = true
	}
	if state.currentCall == unix.SYS_FSTAT && state.firstRootKnown &&
		state.currentFD == state.firstRootFD && result >= 0 {
		state.firstRootStat = true
	}
	if state.currentCall == unix.SYS_FCHMOD && state.firstRootKnown &&
		state.currentFD == state.firstRootFD && state.currentArg&0o777 == 0o700 && result >= 0 {
		state.afterFchmod = true
	}
	if state.currentCall == unix.SYS_FLOCK && state.lockFDKnown &&
		state.currentFD == state.lockFD && state.currentArg == unix.LOCK_EX|unix.LOCK_NB && result >= 0 {
		state.afterFlock = true
	}
	if state.currentCall == unix.SYS_CLOSE && result >= 0 {
		if state.firstRootKnown && state.currentFD == state.firstRootFD && state.firstRootStat {
			state.firstRootClosed = true
		}
		state.clearClosedFD(state.currentFD)
	}
	state.resetEntryClassification()
}

func (state *claimSyscallFaultState) resetEntryClassification() {
	state.openingDirectory = false
	state.currentDirectoryPath = ""
	state.currentSlashOpen = 0
	state.currentRootOpen = 0
	state.openingLock = false
	state.openingCreated = false
	state.openingMissingTarget = false
	state.creatingTarget = false
}

func (state *claimSyscallFaultState) resolvePath(dirFD uint64, path string) string {
	if path == "" {
		return ""
	}
	if claimFaultAtCWD(dirFD) {
		if !filepath.IsAbs(path) {
			return ""
		}
		return filepath.Clean(path)
	}
	parent, ok := state.directories[dirFD]
	if !ok || filepath.IsAbs(path) {
		return ""
	}
	return filepath.Join(parent, path)
}

func claimFaultDirectoryOpen(registers *unix.PtraceRegs) bool {
	flags := int(registers.Rdx)
	return registers.Orig_rax == unix.SYS_OPENAT &&
		flags&(unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW) ==
			unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW &&
		flags&unix.O_ACCMODE == unix.O_RDONLY && flags&unix.O_CREAT == 0
}

func claimFaultAtCWD(fd uint64) bool {
	return int64(fd) == int64(unix.AT_FDCWD)
}

func claimFaultCreationScenario(scenario string) bool {
	switch scenario {
	case "root-parent-fstat", "root-mkdir", "root-created-open", "root-created-fstat":
		return true
	default:
		return false
	}
}

func claimFaultNeedsLockFD(scenario string) bool {
	return scenario == "lock-fstat" || scenario == "lock-flock" ||
		scenario == "lock-reinspection"
}

func (state *claimSyscallFaultState) clearClosedFD(fd uint64) {
	delete(state.directories, fd)
	if state.firstSlashKnown && fd == state.firstSlashFD {
		state.firstSlashKnown = false
	}
	if state.firstRootKnown && fd == state.firstRootFD {
		state.firstRootKnown = false
	}
	if state.secondRootKnown && fd == state.secondRootFD {
		state.secondRootKnown = false
	}
	if state.createdKnown && fd == state.createdFD {
		state.createdKnown = false
	}
	if state.lockFDKnown && fd == state.lockFD {
		state.lockFDKnown = false
	}
}

func (state *claimSyscallFaultState) shouldInject(call uint64) (bool, unix.Errno) {
	if state.scoped && !state.targetActive {
		return false, 0
	}
	switch state.scenario {
	case "boundary-missing-ancestors":
		expected := state.targetPath
		for index := 0; index < state.pathStatCalls; index++ {
			expected = filepath.Dir(expected)
		}
		if call == unix.SYS_NEWFSTATAT && state.pathStatCalls < 4 &&
			claimFaultAtCWD(state.currentFD) && state.currentPath == expected &&
			state.currentFlag&unix.AT_SYMLINK_NOFOLLOW != 0 {
			state.pathStatCalls++
			return true, unix.ENOENT
		}
	case "root-open":
		if state.openingDirectory && state.currentDirectoryPath == string(os.PathSeparator) &&
			state.currentSlashOpen == 1 && !state.injected {
			return true, unix.EIO
		}
	case "root-initial-fstat":
		if call == unix.SYS_FSTAT && state.firstSlashKnown && state.currentFD == state.firstSlashFD &&
			!state.injected {
			return true, unix.EIO
		}
	case "root-revalidation-open":
		if state.openingDirectory && state.currentDirectoryPath == string(os.PathSeparator) &&
			state.currentSlashOpen == 2 && !state.injected {
			return true, unix.EIO
		}
	case "root-parent-fstat":
		if call == unix.SYS_FSTAT && state.missingParentKnown &&
			state.currentFD == state.missingParentFD && !state.injected {
			return true, unix.EIO
		}
	case "root-mkdir":
		if state.creatingTarget && !state.injected {
			return true, unix.EIO
		}
	case "root-close":
		if call == unix.SYS_CLOSE && state.firstSlashKnown && state.currentFD == state.firstSlashFD &&
			!state.injected {
			return true, unix.EIO
		}
	case "root-created-open":
		if state.openingCreated && !state.injected {
			return true, unix.EIO
		}
	case "root-created-fstat":
		if call == unix.SYS_FSTAT && state.createdKnown && state.currentFD == state.createdFD &&
			!state.injected {
			return true, unix.EIO
		}
	case "lock-fstat":
		if call == unix.SYS_FSTAT && state.lockFDKnown && state.currentFD == state.lockFD &&
			!state.injected {
			return true, unix.EIO
		}
	case "lock-root-open":
		if state.openingDirectory && state.currentDirectoryPath == state.root &&
			state.currentRootOpen == 1 && !state.injected {
			return true, unix.EIO
		}
	case "lock-second-inspection":
		if state.openingDirectory && state.currentDirectoryPath == string(os.PathSeparator) &&
			state.currentSlashOpen == 2 && state.firstRootKnown && !state.injected {
			return true, unix.EIO
		}
	case "lock-root-close":
		if call == unix.SYS_CLOSE && state.firstRootKnown && state.currentFD == state.firstRootFD &&
			!state.injected {
			return true, unix.EIO
		}
	case "test-lock-root-open":
		if state.openingDirectory && state.currentDirectoryPath == state.root &&
			state.currentRootOpen == 1 && !state.injected {
			return true, unix.EIO
		}
	case "test-lock-fstat", "test-root-prepare-fstat":
		if call == unix.SYS_FSTAT && state.firstRootKnown && state.currentFD == state.firstRootFD &&
			!state.injected {
			return true, unix.EIO
		}
	case "valid-root-open":
		if state.openingDirectory && state.currentDirectoryPath == state.root &&
			state.currentRootOpen == 2 && state.firstRootClosed && !state.injected {
			return true, unix.EIO
		}
	case "valid-root-close":
		if call == unix.SYS_CLOSE && state.secondRootKnown && state.currentFD == state.secondRootFD &&
			!state.injected {
			return true, unix.EIO
		}
	case "valid-root-descriptor":
		if call == unix.SYS_FSTAT && state.secondRootKnown && state.currentFD == state.secondRootFD &&
			!state.injected {
			return true, unix.EIO
		}
	case "test-root-chmod":
		if call == unix.SYS_FCHMOD && state.firstRootKnown && state.currentFD == state.firstRootFD &&
			state.currentArg&0o777 == 0o700 && !state.injected {
			return true, unix.EIO
		}
	case "test-root-reinspect":
		if state.afterFchmod && call == unix.SYS_NEWFSTATAT && claimFaultAtCWD(state.currentFD) &&
			state.currentPath == state.root && state.currentFlag&unix.AT_SYMLINK_NOFOLLOW != 0 &&
			!state.injected {
			return true, unix.EIO
		}
	case "lock-flock":
		if call == unix.SYS_FLOCK && state.lockFDKnown && state.currentFD == state.lockFD &&
			state.currentArg == unix.LOCK_EX|unix.LOCK_NB && !state.injected {
			return true, unix.EIO
		}
	case "lock-reinspection":
		if state.afterFlock && call == unix.SYS_NEWFSTATAT && state.firstRootKnown &&
			state.currentFD == state.firstRootFD && state.currentPath == state.lockName &&
			state.currentFlag&unix.AT_SYMLINK_NOFOLLOW != 0 && !state.injected {
			return true, unix.EIO
		}
	}
	return false, 0
}

func TestClaimSyscallFaultValidRootOpenUsesCompletedInspectionPhase(t *testing.T) {
	root := "/tmp/claim-root"
	state := newClaimSyscallFaultState("valid-root-open", root, false)
	atCWD := int64(unix.AT_FDCWD)
	rootOpen := claimFaultDirectoryOpenRegisters(uint64(atCWD))
	state.observeEntry(&rootOpen, root)
	if inject, _ := state.shouldInject(state.currentCall); inject {
		t.Fatal("valid-root reopen fault injected before root inspection")
	}
	state.observeExit(40)
	state.observeEntry(&unix.PtraceRegs{Orig_rax: unix.SYS_FSTAT, Rdi: 40}, "")
	state.observeExit(0)
	state.observeEntry(&unix.PtraceRegs{Orig_rax: unix.SYS_CLOSE, Rdi: 40}, "")
	state.observeExit(0)
	state.observeEntry(&rootOpen, root)
	if inject, errno := state.shouldInject(state.currentCall); !inject || errno != unix.EIO {
		t.Fatalf("valid-root reopen fault = %t, %v", inject, errno)
	}
}

func TestClaimSyscallFaultLockFstatUsesCapturedDescriptor(t *testing.T) {
	root := "/tmp/claim-root"
	state := newClaimSyscallFaultState("lock-fstat", root, false)
	runtimeDirectory := int64(unix.AT_FDCWD)
	runtimeOpen := unix.PtraceRegs{
		Orig_rax: unix.SYS_OPENAT, Rdi: uint64(runtimeDirectory),
		Rdx: unix.O_CREAT | unix.O_RDWR | unix.O_NOFOLLOW, R10: 0o600,
	}
	state.observeEntry(&runtimeOpen, "/tmp/runtime-file")
	state.observeExit(41)
	if state.lockFDKnown {
		t.Fatal("runtime openat was captured as the claim lock")
	}
	rootOpen := claimFaultDirectoryOpenRegisters(uint64(runtimeDirectory))
	state.observeEntry(&rootOpen, root)
	state.observeExit(7)
	lockOpen := runtimeOpen
	lockOpen.Rdi = 7
	state.observeEntry(&lockOpen, state.lockName)
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

func TestClaimSyscallFaultCreatedDirectoryUsesCapturedDescriptor(t *testing.T) {
	root := "/tmp/claim-root"
	runtimeDirectory := int64(unix.AT_FDCWD)
	runtimeOpen := claimFaultDirectoryOpenRegisters(uint64(runtimeDirectory))
	state := newClaimSyscallFaultState("root-created-fstat", root, false)
	state.directories = map[uint64]string{7: root, 8: "/tmp/unrelated"}
	state.observeEntry(&runtimeOpen, "/tmp/runtime-directory")
	state.observeExit(41)
	if state.createdKnown {
		t.Fatal("runtime directory openat was captured as the created claim root")
	}
	createdOpen := claimFaultDirectoryOpenRegisters(7)
	state.observeEntry(&createdOpen, "new")
	state.observeExit(-int64(unix.ENOENT))
	mkdir := unix.PtraceRegs{Orig_rax: unix.SYS_MKDIRAT, Rdi: 7, Rdx: 0o700}
	state.observeEntry(&mkdir, "new")
	state.observeExit(0)
	unrelatedOpen := createdOpen
	unrelatedOpen.Rdi = 8
	state.observeEntry(&unrelatedOpen, "new")
	state.observeExit(41)
	if state.createdKnown {
		t.Fatal("unrelated relative directory openat was captured as the created claim root")
	}
	state.observeEntry(&createdOpen, "new")
	state.observeExit(42)
	state.currentFD = 41
	if inject, _ := state.shouldInject(unix.SYS_FSTAT); inject {
		t.Fatal("unrelated descriptor fstat was faulted")
	}
	state.currentFD = 42
	if inject, errno := state.shouldInject(unix.SYS_FSTAT); !inject || errno != unix.EIO {
		t.Fatalf("created claim root descriptor fault = %t, %v", inject, errno)
	}
	state.injected = true
	if inject, _ := state.shouldInject(unix.SYS_FSTAT); inject {
		t.Fatal("created claim root descriptor was faulted twice")
	}

	openState := newClaimSyscallFaultState("root-created-open", root, false)
	openState.directories = map[uint64]string{7: root, 8: "/tmp/unrelated"}
	openState.observeEntry(&createdOpen, "new")
	openState.observeExit(-int64(unix.ENOENT))
	openState.observeEntry(&mkdir, "new")
	openState.observeExit(0)
	openState.observeEntry(&runtimeOpen, "/tmp/runtime-directory")
	if inject, _ := openState.shouldInject(unix.SYS_OPENAT); inject {
		t.Fatal("runtime directory openat was faulted")
	}
	openState.observeEntry(&unrelatedOpen, "new")
	if inject, _ := openState.shouldInject(unix.SYS_OPENAT); inject {
		t.Fatal("unrelated relative directory openat was faulted")
	}
	openState.observeEntry(&createdOpen, "new")
	if inject, errno := openState.shouldInject(unix.SYS_OPENAT); !inject || errno != unix.EIO {
		t.Fatalf("created claim root open fault = %t, %v", inject, errno)
	}
}

func TestClaimSyscallFaultScopeMarkers(t *testing.T) {
	state := newClaimSyscallFaultState("root-open", "/tmp/claim-root", true)
	atCWD := int64(unix.AT_FDCWD)
	open := claimFaultDirectoryOpenRegisters(uint64(atCWD))
	state.observeEntry(&open, string(os.PathSeparator))
	if inject, _ := state.shouldInject(state.currentCall); inject {
		t.Fatal("fault injected before target marker")
	}
	begin := unix.PtraceRegs{
		Orig_rax: unix.SYS_GETPID,
		Rdi:      uint64(claimFaultMarkerTag),
		Rsi:      uint64(claimFaultMarkerBegin),
	}
	state.observeEntry(&begin, "")
	state.observeExit(int64(os.Getpid()))
	state.observeEntry(&open, string(os.PathSeparator))
	if inject, errno := state.shouldInject(state.currentCall); !inject || errno != unix.EIO {
		t.Fatalf("marked target fault = %t, %v", inject, errno)
	}
	end := begin
	end.Rsi = uint64(claimFaultMarkerEnd)
	state.observeEntry(&end, "")
	state.injected = false
	state.observeEntry(&open, string(os.PathSeparator))
	if inject, _ := state.shouldInject(state.currentCall); inject {
		t.Fatal("fault injected after target marker")
	}
}

func claimFaultDirectoryOpenRegisters(dirFD uint64) unix.PtraceRegs {
	return unix.PtraceRegs{
		Orig_rax: unix.SYS_OPENAT,
		Rdi:      dirFD,
		Rdx:      unix.O_RDONLY | unix.O_CLOEXEC | unix.O_DIRECTORY | unix.O_NOFOLLOW,
	}
}

func claimFaultCallHasPath(call uint64) bool {
	return call == unix.SYS_OPENAT || call == unix.SYS_MKDIRAT || call == unix.SYS_NEWFSTATAT
}

func readClaimTraceeString(pid int, address uintptr) (string, error) {
	const (
		chunkSize = 64
		maxLength = 4096
	)
	if address == 0 {
		return "", errors.New("null syscall path")
	}
	buffer := make([]byte, 0, 256)
	for len(buffer) < maxLength {
		chunk := make([]byte, chunkSize)
		count, err := unix.PtracePeekData(pid, address+uintptr(len(buffer)), chunk)
		if count > 0 {
			chunk = chunk[:count]
			if terminator := bytes.IndexByte(chunk, 0); terminator >= 0 {
				buffer = append(buffer, chunk[:terminator]...)
				return string(buffer), nil
			}
			buffer = append(buffer, chunk...)
		}
		if err != nil {
			return "", fmt.Errorf("ptrace peek data: %w", err)
		}
		if count == 0 {
			return "", errors.New("ptrace returned an empty syscall path chunk")
		}
	}
	return "", errors.New("syscall path exceeds PATH_MAX")
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

	state := newClaimSyscallFaultState(scenario, root, true)
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
			path := ""
			if claimFaultCallHasPath(registers.Orig_rax) && (!state.scoped || state.targetActive) {
				var err error
				path, err = readClaimTraceeString(pid, uintptr(registers.Rsi))
				if err != nil {
					t.Fatalf("read claim fault child syscall path: %v", err)
				}
			}
			state.observeEntry(&registers, path)
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
