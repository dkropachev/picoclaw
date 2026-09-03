package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
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
	gatewayRunEmbedded = func(ctx context.Context, options EmbeddedGatewayRunOptions) error {
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
	gatewayRunEmbedded = func(ctx context.Context, _ EmbeddedGatewayRunOptions) error {
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
	gatewayRunEmbedded = func(ctx context.Context, options EmbeddedGatewayRunOptions) error {
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
		pidData := *gateway.pidData
		finalPIDData = &pidData
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
	gatewayRunEmbedded = func(ctx context.Context, options EmbeddedGatewayRunOptions) error {
		runCount.Add(1)
		options.OnReady(ppid.PidFileData{
			PID:   os.Getpid(),
			Token: "dirty-generation",
			Host:  "127.0.0.1",
			Port:  18790,
		})
		close(started)
		<-ctx.Done()
		return ErrEmbeddedGatewayRuntimeNotRetired
	}

	if _, err := h.startEmbeddedGateway("starting"); err != nil {
		t.Fatalf("startEmbeddedGateway() error = %v", err)
	}
	waitForEmbeddedGatewayTestValue(t, started, "dirty embedded generation")
	if _, running, err := h.stopEmbeddedGateway(); !running || !errors.Is(err, ErrEmbeddedGatewayRuntimeNotRetired) {
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
	gatewayRunEmbedded = func(ctx context.Context, options EmbeddedGatewayRunOptions) error {
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
	gatewayRunEmbedded = func(ctx context.Context, options EmbeddedGatewayRunOptions) error {
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
	gatewayRunEmbedded = func(ctx context.Context, options EmbeddedGatewayRunOptions) error {
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
	gatewayRunEmbedded = func(context.Context, EmbeddedGatewayRunOptions) error {
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
	if !running || !errors.Is(stopErr, ErrEmbeddedGatewayRuntimeNotRetired) {
		t.Fatalf(
			"timed stop = (running=%v, err=%v), want ErrRuntimeNotRetired",
			running,
			stopErr,
		)
	}
	fatalErr := waitForEmbeddedGatewayTestValue(t, fatal, "launcher terminal failure")
	if !errors.Is(fatalErr, ErrEmbeddedGatewayRuntimeNotRetired) {
		t.Fatalf("fatal error = %v, want ErrRuntimeNotRetired", fatalErr)
	}
	close(release)
}

func TestEmbeddedGatewayHTTPRoutesUseHostedRuntime(t *testing.T) {
	h := newEmbeddedGatewayTestHandler(t)
	readyData := ppid.PidFileData{
		PID: os.Getpid(), Token: "http-generation", Host: "0.0.0.0", Port: 18791,
	}
	started := make(chan struct{})
	h.EmbedGateway(func(ctx context.Context, options EmbeddedGatewayRunOptions) error {
		options.OnReady(readyData)
		close(started)
		<-ctx.Done()
		return nil
	})

	startRecorder := httptest.NewRecorder()
	h.handleGatewayStart(startRecorder, httptest.NewRequest(http.MethodPost, "/api/gateway/start", nil))
	if startRecorder.Code != http.StatusOK {
		t.Fatalf("start status = %d, want %d; body=%s", startRecorder.Code, http.StatusOK, startRecorder.Body.String())
	}
	waitForEmbeddedGatewayTestValue(t, started, "HTTP-started embedded runtime")
	var startBody map[string]any
	if err := json.Unmarshal(startRecorder.Body.Bytes(), &startBody); err != nil {
		t.Fatalf("decode start response: %v", err)
	}
	if got := int(startBody["pid"].(float64)); got != os.Getpid() {
		t.Fatalf("start pid = %d, want %d", got, os.Getpid())
	}

	if got := h.gatewayProxyURL().String(); got != "http://127.0.0.1:18791" {
		t.Fatalf("embedded proxy URL = %q, want loopback URL", got)
	}
	if !h.gatewayAvailableForProxy() {
		t.Fatal("gatewayAvailableForProxy() = false for ready embedded runtime")
	}
	proxyPID := h.gatewayPIDDataForProxy(&ppid.PidFileData{PID: 999, Token: "foreign"})
	if proxyPID == nil || *proxyPID != readyData {
		t.Fatalf("gatewayPIDDataForProxy() = %#v, want live identity %#v", proxyPID, readyData)
	}
	proxyPID.Token = "mutated-copy"
	gateway.mu.Lock()
	storedToken := gateway.pidData.Token
	gateway.mu.Unlock()
	if storedToken != readyData.Token {
		t.Fatalf("proxy identity mutation changed stored token to %q", storedToken)
	}
	if pid, alive := gatewayVersionState(); pid != os.Getpid() || !alive {
		t.Fatalf("gatewayVersionState() = (%d, %v), want (%d, true)", pid, alive, os.Getpid())
	}
	if got := resolveGatewayBinaryForVersionInfo(); got != "" {
		t.Fatalf("embedded Gateway binary = %q, want empty", got)
	}
	if got := h.activeTurnsGatewayPidData(nil); got == nil || got.Token != readyData.Token {
		t.Fatalf("activeTurnsGatewayPidData() = %#v, want live embedded identity", got)
	}

	status := h.gatewayStatusData()
	if status["gateway_embedded"] != true || status["gateway_status"] != "running" {
		t.Fatalf("embedded gateway status = %#v, want embedded running", status)
	}

	stopRecorder := httptest.NewRecorder()
	h.handleGatewayStop(stopRecorder, httptest.NewRequest(http.MethodPost, "/api/gateway/stop", nil))
	if stopRecorder.Code != http.StatusOK || !strings.Contains(stopRecorder.Body.String(), `"status":"ok"`) {
		t.Fatalf("stop response = %d %s, want ok", stopRecorder.Code, stopRecorder.Body.String())
	}
	if h.gatewayAvailableForProxy() {
		t.Fatal("gatewayAvailableForProxy() = true after embedded stop")
	}
	if got := h.gatewayProxyURL().String(); got != "http://localhost:18790" {
		t.Fatalf("stopped embedded proxy fallback URL = %q, want configured URL", got)
	}
	if pid, alive := gatewayVersionState(); pid != 0 || alive {
		t.Fatalf("stopped gatewayVersionState() = (%d, %v), want (0, false)", pid, alive)
	}

	notRunningRecorder := httptest.NewRecorder()
	h.handleGatewayStop(notRunningRecorder, httptest.NewRequest(http.MethodPost, "/api/gateway/stop", nil))
	if notRunningRecorder.Code != http.StatusOK ||
		!strings.Contains(notRunningRecorder.Body.String(), `"status":"not_running"`) {
		t.Fatalf(
			"second stop response = %d %s, want not_running",
			notRunningRecorder.Code,
			notRunningRecorder.Body.String(),
		)
	}
	h.StopGateway()
}

func TestTryAutoStartEmbeddedGatewayBranches(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		h := newEmbeddedGatewayTestHandler(t)
		started := make(chan struct{})
		gatewayRunEmbedded = func(ctx context.Context, options EmbeddedGatewayRunOptions) error {
			options.OnReady(ppid.PidFileData{
				PID: os.Getpid(), Token: "auto-start", Host: "127.0.0.1", Port: 18790,
			})
			close(started)
			<-ctx.Done()
			return nil
		}
		h.TryAutoStartGateway()
		waitForEmbeddedGatewayTestValue(t, started, "auto-started runtime")
		h.StopGateway()
	})

	t.Run("config error", func(t *testing.T) {
		resetGatewayTestState(t)
		configPath := filepath.Join(globalConfigDir(), "malformed.json")
		if err := os.WriteFile(configPath, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		h := NewHandler(configPath)
		h.EmbedGateway()
		h.TryAutoStartGateway()
		gateway.mu.Lock()
		runtime := gateway.embedded
		gateway.mu.Unlock()
		if runtime != nil {
			t.Fatal("auto-started with an unreadable config")
		}
	})

	t.Run("precondition", func(t *testing.T) {
		h := newEmbeddedGatewayTestHandler(t)
		cfg, err := config.LoadConfig(h.configPath)
		if err != nil {
			t.Fatal(err)
		}
		addGatewayTestModel(cfg)
		cfg.Agents.Defaults.AccountRef = ""
		if err = config.SaveConfig(h.configPath, cfg); err != nil {
			t.Fatal(err)
		}
		h.TryAutoStartGateway()
		gateway.mu.Lock()
		runtime := gateway.embedded
		gateway.mu.Unlock()
		if runtime != nil {
			t.Fatal("auto-started despite an unmet account precondition")
		}
	})

	t.Run("lifecycle conflict", func(t *testing.T) {
		h := newEmbeddedGatewayTestHandler(t)
		gateway.mu.Lock()
		gateway.cmd = &exec.Cmd{}
		gateway.mu.Unlock()
		h.TryAutoStartGateway()
		gateway.mu.Lock()
		runtime := gateway.embedded
		gateway.mu.Unlock()
		if runtime != nil {
			t.Fatal("auto-started while a managed command was attached")
		}
	})
}

func TestEmbeddedGatewayStartOperationGuards(t *testing.T) {
	t.Run("nil handler", func(t *testing.T) {
		resetGatewayTestState(t)
		var h *Handler
		if _, err := h.startEmbeddedGatewayOperation("starting"); err == nil {
			t.Fatal("nil handler start succeeded")
		}
	})

	t.Run("not configured", func(t *testing.T) {
		resetGatewayTestState(t)
		if _, err := NewHandler("").startEmbeddedGatewayOperation("starting"); err == nil {
			t.Fatal("non-embedded handler start succeeded")
		}
	})

	for _, test := range []struct {
		name string
		set  func()
		want string
	}{
		{name: "host closing", set: func() { gateway.hostClosing = true }, want: "shutting down"},
		{name: "retirement blocked", set: func() { gateway.embeddedBlocked = true }, want: "restart the launcher"},
		{name: "separate command", set: func() { gateway.cmd = &exec.Cmd{} }, want: "separately managed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newEmbeddedGatewayTestHandler(t)
			gateway.mu.Lock()
			test.set()
			gateway.mu.Unlock()
			if _, err := h.startEmbeddedGatewayOperation(
				"starting",
			); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("start error = %v, want substring %q", err, test.want)
			}
		})
	}

	t.Run("already running", func(t *testing.T) {
		h := newEmbeddedGatewayTestHandler(t)
		ctx, cancel := context.WithCancel(context.Background())
		gateway.mu.Lock()
		gateway.embedded = &embeddedGatewayRuntime{cancel: cancel, done: make(chan error, 1)}
		gateway.mu.Unlock()
		t.Cleanup(cancel)
		pid, err := h.startEmbeddedGatewayOperation("starting")
		if err != nil || pid != os.Getpid() {
			t.Fatalf("already-running start = (%d, %v), want (%d, nil)", pid, err, os.Getpid())
		}
		_ = ctx
	})

	t.Run("foreign pid authority", func(t *testing.T) {
		h := newEmbeddedGatewayTestHandler(t)
		foreignPID := os.Getppid()
		if foreignPID <= 1 || foreignPID == os.Getpid() {
			t.Skip("no suitable live foreign PID")
		}
		writeTestPidFile(t, ppid.PidFileData{
			PID: foreignPID, Token: "foreign", Host: "127.0.0.1", Port: 18790,
		})
		if _, err := h.startEmbeddedGatewayOperation("starting"); err == nil ||
			!strings.Contains(err.Error(), strconv.Itoa(foreignPID)) {
			t.Fatalf("foreign PID start error = %v, want PID conflict", err)
		}
	})

	t.Run("invalid config", func(t *testing.T) {
		resetGatewayTestState(t)
		configPath := filepath.Join(globalConfigDir(), "malformed.json")
		if err := os.WriteFile(configPath, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		h := NewHandler(configPath)
		h.EmbedGateway()
		if _, err := h.startEmbeddedGatewayOperation("starting"); err == nil ||
			!strings.Contains(err.Error(), "failed to load config") {
			t.Fatalf("invalid-config start error = %v", err)
		}
	})
}

func TestEmbeddedGatewayHandlersReportPreconditionsAndRuntimeErrors(t *testing.T) {
	t.Run("start validation error", func(t *testing.T) {
		resetGatewayTestState(t)
		configPath := filepath.Join(globalConfigDir(), "malformed.json")
		if err := os.WriteFile(configPath, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		h := NewHandler(configPath)
		h.EmbedGateway()
		recorder := httptest.NewRecorder()
		h.handleGatewayStart(recorder, httptest.NewRequest(http.MethodPost, "/api/gateway/start", nil))
		if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "validate") {
			t.Fatalf("validation response = %d %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("start precondition", func(t *testing.T) {
		h := newEmbeddedGatewayTestHandler(t)
		cfg, err := config.LoadConfig(h.configPath)
		if err != nil {
			t.Fatal(err)
		}
		addGatewayTestModel(cfg)
		cfg.Agents.Defaults.AccountRef = ""
		if err = config.SaveConfig(h.configPath, cfg); err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		h.handleGatewayStart(recorder, httptest.NewRequest(http.MethodPost, "/api/gateway/start", nil))
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "precondition_failed") {
			t.Fatalf("precondition response = %d %s", recorder.Code, recorder.Body.String())
		}
		status := h.gatewayStatusData()
		if status["gateway_start_allowed"] != false ||
			!strings.Contains(fmt.Sprint(status["gateway_start_reason"]), "no account configured") {
			t.Fatalf("precondition status = %#v", status)
		}
	})

	t.Run("start lifecycle error", func(t *testing.T) {
		h := newEmbeddedGatewayTestHandler(t)
		gateway.mu.Lock()
		gateway.cmd = &exec.Cmd{}
		gateway.mu.Unlock()
		recorder := httptest.NewRecorder()
		h.handleGatewayStart(recorder, httptest.NewRequest(http.MethodPost, "/api/gateway/start", nil))
		if recorder.Code != http.StatusInternalServerError ||
			!strings.Contains(recorder.Body.String(), "separately managed") {
			t.Fatalf("lifecycle response = %d %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("stop runtime error", func(t *testing.T) {
		h := newEmbeddedGatewayTestHandler(t)
		started := make(chan struct{})
		gatewayRunEmbedded = func(ctx context.Context, options EmbeddedGatewayRunOptions) error {
			options.OnReady(ppid.PidFileData{
				PID: os.Getpid(), Token: "stop-error", Host: "127.0.0.1", Port: 18790,
			})
			close(started)
			<-ctx.Done()
			return errors.New("retirement failed")
		}
		if _, err := h.startEmbeddedGateway("starting"); err != nil {
			t.Fatal(err)
		}
		waitForEmbeddedGatewayTestValue(t, started, "erroring runtime")
		recorder := httptest.NewRecorder()
		h.handleGatewayStop(recorder, httptest.NewRequest(http.MethodPost, "/api/gateway/stop", nil))
		if recorder.Code != http.StatusInternalServerError ||
			!strings.Contains(recorder.Body.String(), "retirement failed") {
			t.Fatalf("stop error response = %d %s", recorder.Code, recorder.Body.String())
		}
	})
}

func TestEmbeddedGatewayPanicIsTerminalAndBlocksReplacement(t *testing.T) {
	h := newEmbeddedGatewayTestHandler(t)
	started := make(chan struct{})
	release := make(chan struct{})
	fatal := make(chan error, 1)
	h.SetGatewayFatalHandler(func(err error) { fatal <- err })
	h.EmbedGateway(func(context.Context, EmbeddedGatewayRunOptions) error {
		close(started)
		<-release
		panic("runtime panic")
	})

	if _, err := h.startEmbeddedGateway("starting"); err != nil {
		t.Fatal(err)
	}
	waitForEmbeddedGatewayTestValue(t, started, "panicking runtime start")
	gateway.mu.Lock()
	runtime := gateway.embedded
	gateway.mu.Unlock()
	close(release)
	runErr := waitForEmbeddedGatewayTestValue(t, runtime.done, "panic retirement")
	if !errors.Is(runErr, ErrEmbeddedGatewayRuntimeNotRetired) || !strings.Contains(runErr.Error(), "panicked") {
		t.Fatalf("panic error = %v, want terminal retirement error", runErr)
	}
	fatalErr := waitForEmbeddedGatewayTestValue(t, fatal, "panic fatal notification")
	if !errors.Is(fatalErr, ErrEmbeddedGatewayRuntimeNotRetired) {
		t.Fatalf("fatal error = %v, want ErrEmbeddedGatewayRuntimeNotRetired", fatalErr)
	}
	gateway.mu.Lock()
	blocked := gateway.embeddedBlocked
	status := gateway.runtimeStatus
	gateway.mu.Unlock()
	if !blocked || status != "error" {
		t.Fatalf("post-panic state = (blocked=%v, status=%q), want blocked error", blocked, status)
	}
}

func TestEmbeddedGatewayStatusReportsFixedAdmissionReasons(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     string
		closing    bool
		blocked    bool
		wantReason string
	}{
		{name: "closing", closing: true, wantReason: "shutting down"},
		{name: "blocked", blocked: true, wantReason: "did not retire cleanly"},
		{name: "starting", status: "starting", wantReason: "transition is in progress"},
		{name: "restarting", status: "restarting", wantReason: "transition is in progress"},
		{name: "stopping", status: "stopping", wantReason: "transition is in progress"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newEmbeddedGatewayTestHandler(t)
			_, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			gateway.mu.Lock()
			gateway.hostClosing = test.closing
			gateway.embeddedBlocked = test.blocked
			gateway.runtimeStatus = test.status
			if test.status != "" {
				gateway.embedded = &embeddedGatewayRuntime{cancel: cancel, done: make(chan error, 1)}
			}
			gateway.mu.Unlock()
			data := h.gatewayStatusData()
			if data["gateway_start_allowed"] != false ||
				!strings.Contains(fmt.Sprint(data["gateway_start_reason"]), test.wantReason) {
				t.Fatalf("status = %#v, want fixed reason containing %q", data, test.wantReason)
			}
		})
	}

	t.Run("external conflict", func(t *testing.T) {
		h := newEmbeddedGatewayTestHandler(t)
		foreignPID := os.Getppid()
		if foreignPID <= 1 || foreignPID == os.Getpid() {
			t.Skip("no suitable live foreign PID")
		}
		writeTestPidFile(t, ppid.PidFileData{
			PID: foreignPID, Token: "foreign-status", Host: "127.0.0.1", Port: 18790,
		})
		data := h.gatewayStatusData()
		if data["gateway_status"] != "error" || data["gateway_external_conflict"] != true ||
			!strings.Contains(fmt.Sprint(data["gateway_start_reason"]), strconv.Itoa(foreignPID)) {
			t.Fatalf("external conflict status = %#v", data)
		}
	})
}

func TestEmbeddedGatewayPIDAuthoritySanitization(t *testing.T) {
	t.Run("stale generation token is removed", func(t *testing.T) {
		h := newEmbeddedGatewayTestHandler(t)
		_, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		candidate := ppid.PidFileData{
			PID: os.Getpid(), Token: "stale-token", Host: "127.0.0.1", Port: 18790,
		}
		writeTestPidFile(t, candidate)
		gateway.mu.Lock()
		gateway.embedded = &embeddedGatewayRuntime{
			cancel: cancel, done: make(chan error, 1),
			pidData: &ppid.PidFileData{PID: os.Getpid(), Token: "live-token", Host: "127.0.0.1", Port: 18790},
		}
		gateway.runtimeStatus = "running"
		gateway.mu.Unlock()
		if got := h.sanitizeGatewayPidData(&candidate, nil); got != nil {
			t.Fatalf("sanitize stale generation = %#v, want nil", got)
		}
		if _, err := os.Stat(filepath.Join(globalConfigDir(), ".picoclaw.pid")); !os.IsNotExist(err) {
			t.Fatalf("stale generation PID file still exists: %v", err)
		}
	})

	t.Run("tokenless same-pid record is preserved", func(t *testing.T) {
		h := newEmbeddedGatewayTestHandler(t)
		candidate := ppid.PidFileData{PID: os.Getpid(), Host: "127.0.0.1", Port: 18790}
		path := writeTestPidFile(t, candidate)
		if got := h.sanitizeGatewayPidData(&candidate, nil); got != nil {
			t.Fatalf("sanitize tokenless record = %#v, want nil", got)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("tokenless embedded PID record was destructively removed: %v", err)
		}
	})
}

func TestEmbeddedGatewayHelperFallbacks(t *testing.T) {
	resetGatewayTestState(t)
	candidate := &ppid.PidFileData{PID: 123, Token: "candidate"}
	var nilHandler *Handler
	if got := nilHandler.gatewayPIDDataForProxy(candidate); got != candidate {
		t.Fatalf("nil handler proxy PID = %#v, want original candidate", got)
	}
	plain := NewHandler("")
	if got := plain.gatewayPIDDataForProxy(candidate); got != candidate {
		t.Fatalf("plain handler proxy PID = %#v, want original candidate", got)
	}
	nilHandler.beginEmbeddedGatewayShutdown()
	plain.beginEmbeddedGatewayShutdown()
	if err := gatewayRunEmbedded(context.Background(), EmbeddedGatewayRunOptions{}); err == nil ||
		!strings.Contains(err.Error(), "not configured") {
		t.Fatalf("default embedded runner error = %v", err)
	}

	gateway.mu.Lock()
	gateway.runtimeStatus = "running"
	gateway.embedded = nil
	gateway.mu.Unlock()
	effects := agentEffectsForRuntimeConfig(config.DefaultConfig())
	if effects.GatewayEffect != "applied" {
		t.Fatalf("effects after missing runtime = %#v, want gateway applied", effects)
	}

	gateway.mu.Lock()
	gateway.cmd = &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}
	if got := gatewayRuntimePIDLocked(); got != os.Getpid() {
		gateway.mu.Unlock()
		t.Fatalf("command runtime PID = %d, want %d", got, os.Getpid())
	}
	gateway.mu.Unlock()
}

func TestEmbeddedGatewayValidationDefersDuringLifecycleTransition(t *testing.T) {
	for _, test := range []struct {
		name    string
		closing bool
		status  string
		want    string
	}{
		{name: "closing", closing: true, want: "shutting down"},
		{name: "starting", status: "starting", want: "not published readiness"},
		{name: "restarting", status: "restarting", want: "not published readiness"},
		{name: "stopping", status: "stopping", want: "not published readiness"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newEmbeddedGatewayTestHandler(t)
			_, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			gateway.mu.Lock()
			gateway.hostClosing = test.closing
			gateway.runtimeStatus = test.status
			gateway.embedded = &embeddedGatewayRuntime{cancel: cancel, done: make(chan error, 1)}
			gateway.mu.Unlock()
			candidate := &ppid.PidFileData{
				PID: os.Getpid(), Token: "unready", Host: "127.0.0.1", Port: 18790,
			}
			ok, decisive, reason := h.validateGatewayPidData(candidate, nil)
			if ok || decisive || !strings.Contains(reason, test.want) {
				t.Fatalf("validation = (%v, %v, %q), want deferred %q", ok, decisive, reason, test.want)
			}
		})
	}
}

func TestStandaloneTokenlessSanitizationRetainsLegacyPIDCleanup(t *testing.T) {
	resetGatewayTestState(t)
	h := NewHandler("")
	candidate := ppid.PidFileData{PID: os.Getpid(), Host: "127.0.0.1", Port: 18790}
	path := writeTestPidFile(t, candidate)
	gatewayProcessMatcher = func(int) (bool, bool) { return false, true }
	if got := h.sanitizeGatewayPidData(&candidate, nil); got != nil {
		t.Fatalf("sanitize tokenless standalone record = %#v, want nil", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("legacy standalone PID file still exists: %v", err)
	}
}

func TestEmbeddedGatewayStartRemovesSameProcessStalePIDFile(t *testing.T) {
	h := newEmbeddedGatewayTestHandler(t)
	path := writeTestPidFile(t, ppid.PidFileData{
		PID: os.Getpid(), Token: "previous-generation", Host: "127.0.0.1", Port: 18790,
	})
	started := make(chan struct{})
	gatewayRunEmbedded = func(ctx context.Context, _ EmbeddedGatewayRunOptions) error {
		close(started)
		<-ctx.Done()
		return nil
	}
	if _, err := h.startEmbeddedGateway("starting"); err != nil {
		t.Fatal(err)
	}
	waitForEmbeddedGatewayTestValue(t, started, "runtime after stale PID cleanup")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("same-process stale PID file still exists: %v", err)
	}
	h.StopGateway()
}

func TestEmbeddedGatewayStopGatewayLogsRuntimeFailure(t *testing.T) {
	h := newEmbeddedGatewayTestHandler(t)
	started := make(chan struct{})
	gatewayRunEmbedded = func(ctx context.Context, _ EmbeddedGatewayRunOptions) error {
		close(started)
		<-ctx.Done()
		return errors.New("stop through host failed")
	}
	if _, err := h.startEmbeddedGateway("starting"); err != nil {
		t.Fatal(err)
	}
	waitForEmbeddedGatewayTestValue(t, started, "runtime stopped through StopGateway")
	h.StopGateway()
	gateway.mu.Lock()
	status := gateway.runtimeStatus
	gateway.mu.Unlock()
	if status != "error" {
		t.Fatalf("runtime status after StopGateway error = %q, want error", status)
	}
}

func TestRestartEmbeddedGatewayFailurePaths(t *testing.T) {
	t.Run("validation error", func(t *testing.T) {
		resetGatewayTestState(t)
		configPath := filepath.Join(globalConfigDir(), "malformed.json")
		if err := os.WriteFile(configPath, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		h := NewHandler(configPath)
		h.EmbedGateway()
		if _, err := h.RestartGateway(); err == nil || !strings.Contains(err.Error(), "validate") {
			t.Fatalf("restart validation error = %v", err)
		}
	})

	t.Run("precondition", func(t *testing.T) {
		h := newEmbeddedGatewayTestHandler(t)
		cfg, err := config.LoadConfig(h.configPath)
		if err != nil {
			t.Fatal(err)
		}
		addGatewayTestModel(cfg)
		cfg.Agents.Defaults.AccountRef = ""
		if err = config.SaveConfig(h.configPath, cfg); err != nil {
			t.Fatal(err)
		}
		if _, err = h.RestartGateway(); err == nil {
			t.Fatal("restart succeeded despite unmet account precondition")
		} else {
			var precondition *preconditionFailedError
			if !errors.As(err, &precondition) {
				t.Fatalf("restart error = %T %v, want preconditionFailedError", err, err)
			}
		}
	})

	t.Run("host closing", func(t *testing.T) {
		h := newEmbeddedGatewayTestHandler(t)
		gateway.mu.Lock()
		gateway.hostClosing = true
		gateway.mu.Unlock()
		if _, err := h.RestartGateway(); err == nil || !strings.Contains(err.Error(), "shutting down") {
			t.Fatalf("restart while closing error = %v", err)
		}
	})

	t.Run("stop error", func(t *testing.T) {
		h := newEmbeddedGatewayTestHandler(t)
		_, cancel := context.WithCancel(context.Background())
		runtime := &embeddedGatewayRuntime{cancel: cancel, done: make(chan error, 1)}
		runtime.done <- errors.New("runtime retirement failed")
		gateway.mu.Lock()
		gateway.embedded = runtime
		gateway.runtimeStatus = "running"
		gateway.mu.Unlock()
		if _, err := h.RestartGateway(); err == nil || !strings.Contains(err.Error(), "failed to stop") {
			t.Fatalf("restart stop error = %v", err)
		}
	})

	t.Run("replacement start error", func(t *testing.T) {
		h := newEmbeddedGatewayTestHandler(t)
		cancelRequested := make(chan struct{})
		done := make(chan error, 1)
		runtime := &embeddedGatewayRuntime{
			cancel: func() { close(cancelRequested) },
			done:   done,
		}
		gateway.mu.Lock()
		gateway.embedded = runtime
		gateway.runtimeStatus = "running"
		gateway.mu.Unlock()
		go func() {
			<-cancelRequested
			gateway.mu.Lock()
			gateway.embedded = nil
			gateway.cmd = &exec.Cmd{}
			gateway.mu.Unlock()
			done <- nil
		}()
		if _, err := h.RestartGateway(); err == nil || !strings.Contains(err.Error(), "separately managed") {
			t.Fatalf("replacement start error = %v", err)
		}
		gateway.mu.Lock()
		status := gateway.runtimeStatus
		gateway.mu.Unlock()
		if status != "error" {
			t.Fatalf("replacement start status = %q, want error", status)
		}
	})
}
