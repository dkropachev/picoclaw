package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	coregateway "github.com/sipeed/picoclaw/pkg/gateway"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
)

func newEmbeddedGatewayTestHandler(t *testing.T) *Handler {
	t.Helper()
	resetGatewayTestState(t)

	configPath := filepath.Join(globalConfigDir(), "config.json")
	if err := config.SaveConfig(configPath, config.DefaultConfig()); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}
	h := NewHandler(configPath)
	h.EmbedGateway()
	h.SetServerOptions(18800, false, false, nil)
	return h
}

func waitForEmbeddedGatewayTestValue[T any](
	t *testing.T,
	values <-chan T,
	description string,
) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		var zero T
		return zero
	}
}

func TestEmbeddedGatewayStartsWithoutCommandAndPublishesLauncherIdentity(t *testing.T) {
	h := newEmbeddedGatewayTestHandler(t)

	var commandCalls atomic.Int32
	gatewayExecCommand = func(string, ...string) *exec.Cmd {
		commandCalls.Add(1)
		return nil
	}

	readyData := ppid.PidFileData{
		PID:   os.Getpid(),
		Token: "embedded-generation-token",
		Host:  "127.0.0.1",
		Port:  18790,
	}
	runnerStarted := make(chan struct{})
	gatewayRunEmbedded = func(ctx context.Context, options coregateway.RunOptions) error {
		options.OnReady(readyData)
		close(runnerStarted)
		<-ctx.Done()
		return nil
	}

	pid, err := h.startEmbeddedGateway("starting")
	if err != nil {
		t.Fatalf("startEmbeddedGateway() error = %v", err)
	}
	if pid != os.Getpid() {
		t.Fatalf("startEmbeddedGateway() pid = %d, want launcher pid %d", pid, os.Getpid())
	}
	waitForEmbeddedGatewayTestValue(t, runnerStarted, "embedded runner start")

	gateway.mu.Lock()
	cmd := gateway.cmd
	runtime := gateway.embedded
	status := gateway.runtimeStatus
	pidData := gateway.pidData
	matches := embeddedGatewayPIDDataMatchesLocked(&readyData)
	gateway.mu.Unlock()

	if commandCalls.Load() != 0 {
		t.Fatalf("gatewayExecCommand() calls = %d, want 0", commandCalls.Load())
	}
	if cmd != nil {
		t.Fatalf("gateway.cmd = %#v, want nil for embedded runtime", cmd)
	}
	if runtime == nil {
		t.Fatal("gateway.embedded = nil, want live embedded runtime")
	}
	if status != "running" {
		t.Fatalf("gateway.runtimeStatus = %q, want running", status)
	}
	if pidData == nil || pidData.PID != os.Getpid() || pidData.Token != readyData.Token {
		t.Fatalf("gateway.pidData = %#v, want launcher PID and generation token %#v", pidData, readyData)
	}
	if !matches {
		t.Fatal("embeddedGatewayPIDDataMatchesLocked() = false for live ready identity")
	}

	if _, running, stopErr := h.stopEmbeddedGateway(); stopErr != nil || !running {
		t.Fatalf("stopEmbeddedGateway() = (running=%v, err=%v), want (true, nil)", running, stopErr)
	}
}

func TestEmbeddedGatewayStopWaitsForRuntimeRetirement(t *testing.T) {
	h := newEmbeddedGatewayTestHandler(t)
	embeddedGatewayStopLimit = time.Second

	runnerStarted := make(chan struct{})
	cancelObserved := make(chan struct{})
	releaseRetirement := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseRetirement) })
	})
	gatewayRunEmbedded = func(ctx context.Context, _ coregateway.RunOptions) error {
		close(runnerStarted)
		<-ctx.Done()
		close(cancelObserved)
		<-releaseRetirement
		return nil
	}

	if _, err := h.startEmbeddedGateway("starting"); err != nil {
		t.Fatalf("startEmbeddedGateway() error = %v", err)
	}
	waitForEmbeddedGatewayTestValue(t, runnerStarted, "embedded runner start")

	type stopResult struct {
		pid     int
		running bool
		err     error
	}
	stopped := make(chan stopResult, 1)
	go func() {
		pid, running, err := h.stopEmbeddedGateway()
		stopped <- stopResult{pid: pid, running: running, err: err}
	}()
	waitForEmbeddedGatewayTestValue(t, cancelObserved, "embedded cancellation")

	select {
	case result := <-stopped:
		t.Fatalf("stop returned before runtime retirement was released: %#v", result)
	case <-time.After(50 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(releaseRetirement) })
	result := waitForEmbeddedGatewayTestValue(t, stopped, "embedded stop completion")
	if result.err != nil || !result.running || result.pid != os.Getpid() {
		t.Fatalf(
			"stopEmbeddedGateway() = (pid=%d, running=%v, err=%v), want (%d, true, nil)",
			result.pid,
			result.running,
			result.err,
			os.Getpid(),
		)
	}
}

func TestEmbeddedGatewayConcurrentRestartsSerializeAndRotateGeneration(t *testing.T) {
	h := newEmbeddedGatewayTestHandler(t)

	type runRecord struct {
		ordinal int32
		data    ppid.PidFileData
	}
	started := make(chan runRecord, 3)
	var runCount atomic.Int32
	var active atomic.Int32
	var maxActive atomic.Int32
	gatewayRunEmbedded = func(ctx context.Context, options coregateway.RunOptions) error {
		ordinal := runCount.Add(1)
		currentActive := active.Add(1)
		defer active.Add(-1)
		for {
			previousMax := maxActive.Load()
			if currentActive <= previousMax || maxActive.CompareAndSwap(previousMax, currentActive) {
				break
			}
		}
		data := ppid.PidFileData{
			PID:   os.Getpid(),
			Token: fmt.Sprintf("embedded-generation-%d", ordinal),
			Host:  "127.0.0.1",
			Port:  18790,
		}
		options.OnReady(data)
		started <- runRecord{ordinal: ordinal, data: data}
		<-ctx.Done()
		return nil
	}

	if _, err := h.startEmbeddedGateway("starting"); err != nil {
		t.Fatalf("startEmbeddedGateway() error = %v", err)
	}
	first := waitForEmbeddedGatewayTestValue(t, started, "initial embedded generation")
	gateway.mu.Lock()
	initialGeneration := gateway.embeddedGeneration
	gateway.mu.Unlock()

	type restartResult struct {
		pid int
		err error
	}
	restartGate := make(chan struct{})
	restarted := make(chan restartResult, 2)
	for range 2 {
		go func() {
			<-restartGate
			pid, err := h.RestartGateway()
			restarted <- restartResult{pid: pid, err: err}
		}()
	}
	close(restartGate)

	second := waitForEmbeddedGatewayTestValue(t, started, "second embedded generation")
	third := waitForEmbeddedGatewayTestValue(t, started, "third embedded generation")
	for range 2 {
		result := waitForEmbeddedGatewayTestValue(t, restarted, "concurrent restart result")
		if result.err != nil || result.pid != os.Getpid() {
			t.Fatalf(
				"RestartGateway() = (pid=%d, err=%v), want (%d, nil)",
				result.pid,
				result.err,
				os.Getpid(),
			)
		}
	}

	gateway.mu.Lock()
	finalGeneration := gateway.embeddedGeneration
	finalRuntime := gateway.embedded
	var finalPIDData *ppid.PidFileData
	if gateway.pidData != nil {
		copy := *gateway.pidData
		finalPIDData = &copy
	}
	gateway.mu.Unlock()

	if maxActive.Load() != 1 {
		t.Fatalf("maximum concurrent embedded runners = %d, want 1", maxActive.Load())
	}
	if finalGeneration != initialGeneration+2 {
		t.Fatalf(
			"embedded generation = %d, want initial %d + 2 restarts",
			finalGeneration,
			initialGeneration,
		)
	}
	if first.ordinal != 1 || second.ordinal != 2 || third.ordinal != 3 {
		t.Fatalf("runner ordinals = (%d, %d, %d), want (1, 2, 3)", first.ordinal, second.ordinal, third.ordinal)
	}
	if finalRuntime == nil || finalRuntime.generation != finalGeneration {
		t.Fatalf("final embedded runtime = %#v, want generation %d", finalRuntime, finalGeneration)
	}
	if finalPIDData == nil || finalPIDData.Token != third.data.Token || finalPIDData.Token == first.data.Token {
		t.Fatalf(
			"final PID identity = %#v, want rotated token %q (not %q)",
			finalPIDData,
			third.data.Token,
			first.data.Token,
		)
	}

	if _, running, err := h.stopEmbeddedGateway(); err != nil || !running {
		t.Fatalf("stopEmbeddedGateway() = (running=%v, err=%v), want (true, nil)", running, err)
	}
}

func TestEmbeddedGatewayDirtyShutdownBlocksReplacement(t *testing.T) {
	h := newEmbeddedGatewayTestHandler(t)

	started := make(chan struct{})
	var runCount atomic.Int32
	gatewayRunEmbedded = func(ctx context.Context, options coregateway.RunOptions) error {
		runCount.Add(1)
		options.OnReady(ppid.PidFileData{
			PID:   os.Getpid(),
			Token: "dirty-generation",
			Host:  "127.0.0.1",
			Port:  18790,
		})
		close(started)
		<-ctx.Done()
		return coregateway.ErrRuntimeNotRetired
	}

	if _, err := h.startEmbeddedGateway("starting"); err != nil {
		t.Fatalf("startEmbeddedGateway() error = %v", err)
	}
	waitForEmbeddedGatewayTestValue(t, started, "dirty embedded generation")
	if _, running, err := h.stopEmbeddedGateway(); !running || !errors.Is(err, coregateway.ErrRuntimeNotRetired) {
		t.Fatalf(
			"stopEmbeddedGateway() = (running=%v, err=%v), want (true, ErrRuntimeNotRetired)",
			running,
			err,
		)
	}

	gateway.mu.Lock()
	blocked := gateway.embeddedBlocked
	runtime := gateway.embedded
	gateway.mu.Unlock()
	if !blocked || runtime != nil {
		t.Fatalf("dirty shutdown state = (blocked=%v, runtime=%#v), want (true, nil)", blocked, runtime)
	}

	_, err := h.RestartGateway()
	if err == nil || !strings.Contains(err.Error(), "restart the launcher") {
		t.Fatalf("RestartGateway() error = %v, want launcher-restart requirement", err)
	}
	if runCount.Load() != 1 {
		t.Fatalf("embedded runner calls = %d, want no replacement after dirty shutdown", runCount.Load())
	}
}

func TestEmbeddedGatewayPIDValidationRejectsStaleGenerationIdentity(t *testing.T) {
	h := newEmbeddedGatewayTestHandler(t)

	ready := make(chan ppid.PidFileData, 2)
	var runCount atomic.Int32
	gatewayRunEmbedded = func(ctx context.Context, options coregateway.RunOptions) error {
		ordinal := runCount.Add(1)
		data := ppid.PidFileData{
			PID:   os.Getpid(),
			Token: fmt.Sprintf("validation-generation-%d", ordinal),
			Host:  "127.0.0.1",
			Port:  18790,
		}
		options.OnReady(data)
		ready <- data
		<-ctx.Done()
		return nil
	}

	if _, err := h.startEmbeddedGateway("starting"); err != nil {
		t.Fatalf("startEmbeddedGateway() error = %v", err)
	}
	stale := waitForEmbeddedGatewayTestValue(t, ready, "initial ready identity")
	if _, err := h.RestartGateway(); err != nil {
		t.Fatalf("RestartGateway() error = %v", err)
	}
	live := waitForEmbeddedGatewayTestValue(t, ready, "replacement ready identity")

	if ok, decisive, reason := h.validateGatewayPidData(&live, nil); !ok || !decisive || reason != "" {
		t.Fatalf(
			"validate live PID data = (ok=%v, decisive=%v, reason=%q), want (true, true, empty)",
			ok,
			decisive,
			reason,
		)
	}
	if ok, decisive, reason := h.validateGatewayPidData(&stale, nil); ok || !decisive ||
		!strings.Contains(reason, "generation") {
		t.Fatalf(
			"validate stale PID data = (ok=%v, decisive=%v, reason=%q), want decisive generation rejection",
			ok,
			decisive,
			reason,
		)
	}

	wrongPID := live
	wrongPID.PID++
	gatewayProcessMatcher = func(int) (bool, bool) { return false, true }
	if ok, decisive, reason := h.validateGatewayPidData(&wrongPID, nil); ok || decisive ||
		!strings.Contains(reason, "conflicts with embedded hosting") {
		t.Fatalf(
			"validate wrong PID data = (ok=%v, decisive=%v, reason=%q), want non-destructive external conflict",
			ok,
			decisive,
			reason,
		)
	}

	if _, running, err := h.stopEmbeddedGateway(); err != nil || !running {
		t.Fatalf("stopEmbeddedGateway() = (running=%v, err=%v), want (true, nil)", running, err)
	}
}

func TestEmbeddedGatewayStopRejectsLateReadyPublication(t *testing.T) {
	h := newEmbeddedGatewayTestHandler(t)
	started := make(chan struct{})
	gatewayRunEmbedded = func(ctx context.Context, options coregateway.RunOptions) error {
		close(started)
		<-ctx.Done()
		options.OnReady(ppid.PidFileData{
			PID: os.Getpid(), Token: "late-ready", Host: "127.0.0.1", Port: 18790,
		})
		return nil
	}

	if _, err := h.startEmbeddedGateway("starting"); err != nil {
		t.Fatalf("startEmbeddedGateway() error = %v", err)
	}
	waitForEmbeddedGatewayTestValue(t, started, "embedded runner start")
	if _, running, err := h.stopEmbeddedGateway(); err != nil || !running {
		t.Fatalf("stopEmbeddedGateway() = (running=%v, err=%v), want (true, nil)", running, err)
	}

	gateway.mu.Lock()
	status := gateway.runtimeStatus
	pidData := gateway.pidData
	gateway.mu.Unlock()
	if status != "stopped" || pidData != nil {
		t.Fatalf("late ready state = (status=%q, pid=%#v), want stopped without authority", status, pidData)
	}
}

func TestEmbeddedGatewayShutdownFenceClosesAdmission(t *testing.T) {
	h := newEmbeddedGatewayTestHandler(t)
	started := make(chan struct{})
	gatewayRunEmbedded = func(ctx context.Context, options coregateway.RunOptions) error {
		options.OnReady(ppid.PidFileData{
			PID: os.Getpid(), Token: "live-ready", Host: "127.0.0.1", Port: 18790,
		})
		close(started)
		<-ctx.Done()
		return nil
	}

	if _, err := h.startEmbeddedGateway("starting"); err != nil {
		t.Fatalf("startEmbeddedGateway() error = %v", err)
	}
	waitForEmbeddedGatewayTestValue(t, started, "embedded runner start")
	h.beginEmbeddedGatewayShutdown()
	if pidData := h.gatewayPIDDataForProxy(nil); pidData != nil {
		t.Fatalf("shutdown fence exposed PID authority: %#v", pidData)
	}
	if _, err := h.startEmbeddedGateway("starting"); err == nil ||
		!strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("start after shutdown fence error = %v, want shutting-down rejection", err)
	}
	status := h.gatewayStatusData()
	if allowed, _ := status["gateway_start_allowed"].(bool); allowed {
		t.Fatalf("gateway status allows start during shutdown: %#v", status)
	}
	if _, running, err := h.stopEmbeddedGateway(); err != nil || !running {
		t.Fatalf("stopEmbeddedGateway() = (running=%v, err=%v), want (true, nil)", running, err)
	}
}

func TestEmbeddedGatewayShutdownTimeoutTerminatesHost(t *testing.T) {
	h := newEmbeddedGatewayTestHandler(t)
	embeddedGatewayStopLimit = 20 * time.Millisecond
	started := make(chan struct{})
	release := make(chan struct{})
	gatewayRunEmbedded = func(context.Context, coregateway.RunOptions) error {
		close(started)
		<-release
		return nil
	}
	fatal := make(chan error, 1)
	h.SetGatewayFatalHandler(func(err error) { fatal <- err })

	if _, err := h.startEmbeddedGateway("starting"); err != nil {
		t.Fatalf("startEmbeddedGateway() error = %v", err)
	}
	waitForEmbeddedGatewayTestValue(t, started, "embedded runner start")
	_, running, stopErr := h.stopEmbeddedGateway()
	if !running || !errors.Is(stopErr, coregateway.ErrRuntimeNotRetired) {
		t.Fatalf(
			"timed stop = (running=%v, err=%v), want ErrRuntimeNotRetired",
			running,
			stopErr,
		)
	}
	if fatalErr := waitForEmbeddedGatewayTestValue(t, fatal, "launcher terminal failure"); !errors.Is(fatalErr, coregateway.ErrRuntimeNotRetired) {
		t.Fatalf("fatal error = %v, want ErrRuntimeNotRetired", fatalErr)
	}
	close(release)
}
