package api

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
)

var errUnregisteredGatewayTestProcess = errors.New("unregistered gateway test process")

// registeredGatewayTestProcessOps gives tests process authority only over
// explicitly registered child PIDs. In particular, constructing an os.Process
// value is not enough to gain signal authority.
type registeredGatewayTestProcessOps struct {
	mu        sync.Mutex
	processes map[int]*os.Process
	alive     map[int]bool
	findPIDs  []int
	signals   []registeredGatewayTestSignal
	killPIDs  []int
}

type registeredGatewayTestSignal struct {
	pid    int
	signal os.Signal
}

func newRegisteredGatewayTestProcessOps() *registeredGatewayTestProcessOps {
	return &registeredGatewayTestProcessOps{
		processes: make(map[int]*os.Process),
		alive:     make(map[int]bool),
	}
}

func (o *registeredGatewayTestProcessOps) register(process *os.Process, alive bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.processes[process.Pid] = process
	o.alive[process.Pid] = alive
}

func (o *registeredGatewayTestProcessOps) Find(pid int) (*os.Process, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.findPIDs = append(o.findPIDs, pid)
	process, ok := o.processes[pid]
	if !ok {
		return nil, fmt.Errorf("%w: PID %d", errUnregisteredGatewayTestProcess, pid)
	}
	return process, nil
}

func (o *registeredGatewayTestProcessOps) Alive(cmd *exec.Cmd) bool {
	pid, ok := gatewayTestCommandPID(cmd)
	if !ok {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	_, registered := o.processes[pid]
	return registered && o.alive[pid]
}

func (o *registeredGatewayTestProcessOps) Signal(cmd *exec.Cmd, signal os.Signal) error {
	pid, ok := gatewayTestCommandPID(cmd)
	if !ok {
		return os.ErrInvalid
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, registered := o.processes[pid]; !registered {
		return fmt.Errorf("%w: PID %d", errUnregisteredGatewayTestProcess, pid)
	}
	o.signals = append(o.signals, registeredGatewayTestSignal{pid: pid, signal: signal})
	return nil
}

func (o *registeredGatewayTestProcessOps) Kill(cmd *exec.Cmd) error {
	pid, ok := gatewayTestCommandPID(cmd)
	if !ok {
		return os.ErrInvalid
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, registered := o.processes[pid]; !registered {
		return fmt.Errorf("%w: PID %d", errUnregisteredGatewayTestProcess, pid)
	}
	o.killPIDs = append(o.killPIDs, pid)
	return nil
}

func (o *registeredGatewayTestProcessOps) Track(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	o.register(cmd.Process, true)
}

func (o *registeredGatewayTestProcessOps) Forget(cmd *exec.Cmd) {
	pid, ok := gatewayTestCommandPID(cmd)
	if !ok {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.processes, pid)
	delete(o.alive, pid)
}

func gatewayTestCommandPID(cmd *exec.Cmd) (int, bool) {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return 0, false
	}
	return cmd.Process.Pid, true
}

func installGatewayProcessOpsForTest(
	t *testing.T,
	ops gatewayProcessOperations,
) {
	t.Helper()
	previous := gatewayProcessOps
	gatewayProcessOps = ops
	t.Cleanup(func() { gatewayProcessOps = previous })
}

func TestGatewayProcessOpsRejectUnregisteredAttachment(t *testing.T) {
	resetGatewayTestState(t)
	ops := newRegisteredGatewayTestProcessOps()
	installGatewayProcessOpsForTest(t, ops)

	const unregisteredPID = 424242
	gateway.mu.Lock()
	err := attachToGatewayProcessLocked(unregisteredPID, config.DefaultConfig())
	tracked := gateway.cmd
	gateway.mu.Unlock()

	if !errors.Is(err, errUnregisteredGatewayTestProcess) {
		t.Fatalf("attachToGatewayProcessLocked() error = %v, want unregistered refusal", err)
	}
	if tracked != nil {
		t.Fatalf("gateway.cmd = %#v, want nil after refused attachment", tracked)
	}
	ops.mu.Lock()
	defer ops.mu.Unlock()
	if len(ops.findPIDs) != 1 || ops.findPIDs[0] != unregisteredPID {
		t.Fatalf("Find() PIDs = %v, want [%d]", ops.findPIDs, unregisteredPID)
	}
}

func TestGatewayProcessOpsRejectUnregisteredStopSignal(t *testing.T) {
	resetGatewayTestState(t)
	ops := newRegisteredGatewayTestProcessOps()
	installGatewayProcessOpsForTest(t, ops)

	const unregisteredPID = 424243
	cmd := &exec.Cmd{Process: &os.Process{Pid: unregisteredPID}}
	gateway.mu.Lock()
	gateway.cmd = cmd
	gateway.owned = true
	setGatewayRuntimeStatusLocked("running")
	pid, err := stopGatewayLocked()
	tracked := gateway.cmd
	gateway.mu.Unlock()

	if pid != unregisteredPID {
		t.Fatalf("stopGatewayLocked() PID = %d, want %d", pid, unregisteredPID)
	}
	if !errors.Is(err, errUnregisteredGatewayTestProcess) {
		t.Fatalf("stopGatewayLocked() error = %v, want unregistered refusal", err)
	}
	if tracked != cmd {
		t.Fatal("refused stop discarded tracked process")
	}

	ops.mu.Lock()
	defer ops.mu.Unlock()
	if runtime.GOOS == "windows" {
		if len(ops.killPIDs) != 0 || len(ops.signals) != 0 {
			t.Fatalf("unregistered operation was recorded: kills=%v signals=%v", ops.killPIDs, ops.signals)
		}
		return
	}
	if len(ops.killPIDs) != 0 || len(ops.signals) != 0 {
		t.Fatalf("unregistered operation was recorded: kills=%v signals=%v", ops.killPIDs, ops.signals)
	}
}

func TestGatewayProcessOpsAllowOnlyRegisteredProcess(t *testing.T) {
	resetGatewayTestState(t)
	ops := newRegisteredGatewayTestProcessOps()
	installGatewayProcessOpsForTest(t, ops)

	const registeredPID = 424244
	process := &os.Process{Pid: registeredPID}
	ops.register(process, true)

	gateway.mu.Lock()
	err := attachToGatewayProcessLocked(registeredPID, config.DefaultConfig())
	cmd := gateway.cmd
	alive := isCmdProcessAliveLocked(cmd)
	gateway.owned = true
	stoppedPID, stopErr := stopGatewayLocked()
	gateway.mu.Unlock()
	if err != nil {
		t.Fatalf("attachToGatewayProcessLocked() error = %v", err)
	}
	if cmd == nil || cmd.Process != process {
		t.Fatalf("gateway.cmd = %#v, want registered process", cmd)
	}
	if !alive {
		t.Fatal("registered live process reported dead")
	}
	if stopErr != nil {
		t.Fatalf("stopGatewayLocked() error = %v", stopErr)
	}
	if stoppedPID != registeredPID {
		t.Fatalf("stopGatewayLocked() PID = %d, want %d", stoppedPID, registeredPID)
	}

	ops.mu.Lock()
	defer ops.mu.Unlock()
	if runtime.GOOS == "windows" {
		if len(ops.killPIDs) != 1 || ops.killPIDs[0] != registeredPID {
			t.Fatalf("Kill() PIDs = %v, want [%d]", ops.killPIDs, registeredPID)
		}
		return
	}
	if len(ops.signals) != 1 || ops.signals[0].pid != registeredPID ||
		ops.signals[0].signal != syscall.SIGTERM {
		t.Fatalf("Signal() calls = %v, want SIGTERM for PID %d", ops.signals, registeredPID)
	}
}
