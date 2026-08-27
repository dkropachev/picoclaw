package api

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
)

// ownedGatewayTestProcessOps grants lifecycle authority only for children
// explicitly created by this test binary. It delegates real liveness and
// signal behavior after the ownership check, so lifecycle tests remain
// realistic without gaining authority over arbitrary host PIDs.
type ownedGatewayTestProcessOps struct {
	mu     sync.Mutex
	system systemGatewayProcessOperations
	owned  map[int]*os.Process
}

var apiSuiteGatewayProcessOps *ownedGatewayTestProcessOps

func newOwnedGatewayTestProcessOps() *ownedGatewayTestProcessOps {
	return &ownedGatewayTestProcessOps{owned: make(map[int]*os.Process)}
}

func (o *ownedGatewayTestProcessOps) Find(pid int) (*os.Process, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	process, ok := o.owned[pid]
	if !ok {
		return nil, fmt.Errorf("%w: PID %d", errUnregisteredGatewayTestProcess, pid)
	}
	return process, nil
}

func (o *ownedGatewayTestProcessOps) Alive(cmd *exec.Cmd) bool {
	if !o.owns(cmd) {
		return false
	}
	return o.system.Alive(cmd)
}

func (o *ownedGatewayTestProcessOps) Signal(cmd *exec.Cmd, signal os.Signal) error {
	if !o.owns(cmd) {
		return o.unownedError(cmd)
	}
	return o.system.Signal(cmd, signal)
}

func (o *ownedGatewayTestProcessOps) Kill(cmd *exec.Cmd) error {
	if !o.owns(cmd) {
		return o.unownedError(cmd)
	}
	return o.system.Kill(cmd)
}

func (o *ownedGatewayTestProcessOps) Track(cmd *exec.Cmd) {
	pid, ok := gatewayTestCommandPID(cmd)
	if !ok {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.owned[pid] = cmd.Process
}

func (o *ownedGatewayTestProcessOps) Forget(cmd *exec.Cmd) {
	pid, ok := gatewayTestCommandPID(cmd)
	if !ok {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.owned[pid] == cmd.Process {
		delete(o.owned, pid)
	}
}

func (o *ownedGatewayTestProcessOps) owns(cmd *exec.Cmd) bool {
	pid, ok := gatewayTestCommandPID(cmd)
	if !ok {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	process, ok := o.owned[pid]
	return ok && process == cmd.Process
}

func (o *ownedGatewayTestProcessOps) unownedError(cmd *exec.Cmd) error {
	pid, _ := gatewayTestCommandPID(cmd)
	return fmt.Errorf("%w: PID %d", errUnregisteredGatewayTestProcess, pid)
}

func registerGatewayTestCommand(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if apiSuiteGatewayProcessOps == nil {
		t.Fatal("API suite process registry is not initialized")
	}
	apiSuiteGatewayProcessOps.Track(cmd)
	t.Cleanup(func() { apiSuiteGatewayProcessOps.Forget(cmd) })
}

func TestOwnedGatewayProcessOpsRejectForgedProcessObjectWithOwnedPID(t *testing.T) {
	ops := newOwnedGatewayTestProcessOps()
	originalProcess := &os.Process{Pid: 424250}
	original := &exec.Cmd{Process: originalProcess}
	forged := &exec.Cmd{Process: &os.Process{Pid: originalProcess.Pid}}
	ops.Track(original)

	if ops.Alive(forged) {
		t.Fatal("forged process object inherited liveness authority")
	}
	if err := ops.Signal(forged, os.Interrupt); !errors.Is(err, errUnregisteredGatewayTestProcess) {
		t.Fatalf("Signal(forged) error = %v, want unregistered refusal", err)
	}
	if err := ops.Kill(forged); !errors.Is(err, errUnregisteredGatewayTestProcess) {
		t.Fatalf("Kill(forged) error = %v, want unregistered refusal", err)
	}
	ops.Forget(forged)
	got, err := ops.Find(originalProcess.Pid)
	if err != nil || got != originalProcess {
		t.Fatalf("forged Forget removed original registration: process=%v err=%v", got, err)
	}
}
