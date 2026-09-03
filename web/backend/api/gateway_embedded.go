package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	ppid "github.com/sipeed/picoclaw/pkg/pid"
)

// ErrEmbeddedGatewayRuntimeNotRetired means the hosted runtime could not prove
// that all of its resources stopped before control returned to the launcher.
var ErrEmbeddedGatewayRuntimeNotRetired = errors.New("embedded gateway runtime was not fully retired")

// EmbeddedGatewayRunOptions is the dependency boundary between the launcher
// API and the core Gateway runtime. Keeping this contract here lets storage
// integration tests import the API without creating a package cycle.
type EmbeddedGatewayRunOptions struct {
	Debug               bool
	HomePath            string
	ConfigPath          string
	AllowEmptyStartup   bool
	ManageLogLevel      bool
	GatewayHostOverride string
	OnReady             func(ppid.PidFileData)
}

// EmbeddedGatewayRunner hosts one Gateway generation until its context is
// canceled. The launcher injects the core runtime implementation from main.
type EmbeddedGatewayRunner func(context.Context, EmbeddedGatewayRunOptions) error

type embeddedGatewayRuntime struct {
	generation uint64
	cancel     context.CancelFunc
	done       chan error
	pidData    *ppid.PidFileData
}

var (
	gatewayRunEmbedded EmbeddedGatewayRunner = func(context.Context, EmbeddedGatewayRunOptions) error {
		return errors.New("embedded gateway runtime runner is not configured")
	}
	embeddedGatewayStopLimit = 60 * time.Second
)

func (h *Handler) tryAutoStartEmbeddedGateway() {
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		logger.ErrorC("gateway", fmt.Sprintf("Skip auto-starting embedded gateway: %v", err))
		return
	}
	if !ready {
		logger.InfoC("gateway", fmt.Sprintf("Skip auto-starting embedded gateway: %s", reason))
		return
	}
	pid, err := h.startEmbeddedGateway("starting")
	if err != nil {
		logger.ErrorC("gateway", fmt.Sprintf("Failed to auto-start embedded gateway: %v", err))
		return
	}
	logger.InfoC("gateway", fmt.Sprintf("Embedded gateway auto-started in launcher PID %d", pid))
}

func (h *Handler) startEmbeddedGateway(initialStatus string) (int, error) {
	gateway.operationMu.Lock()
	defer gateway.operationMu.Unlock()
	return h.startEmbeddedGatewayOperation(initialStatus)
}

func (h *Handler) startEmbeddedGatewayOperation(initialStatus string) (int, error) {
	if h == nil || !h.embedGateway {
		return 0, errors.New("embedded gateway hosting is not configured")
	}

	gateway.mu.Lock()
	if gateway.hostClosing {
		gateway.mu.Unlock()
		return 0, errors.New("launcher is shutting down")
	}
	if gateway.embeddedBlocked {
		gateway.mu.Unlock()
		return 0, errors.New("embedded gateway did not retire cleanly; restart the launcher")
	}
	if gateway.embedded != nil {
		gateway.mu.Unlock()
		return os.Getpid(), nil
	}
	if gateway.cmd != nil {
		gateway.mu.Unlock()
		return 0, errors.New("a separately managed gateway is still attached")
	}
	gateway.mu.Unlock()

	if pidData := ppid.ReadPidFileWithCheck(globalConfigDir()); pidData != nil {
		if pidData.PID != os.Getpid() {
			return 0, fmt.Errorf(
				"standalone gateway PID %d must stop before embedded startup",
				pidData.PID,
			)
		} else {
			_ = ppid.RemovePidFileIfPID(globalConfigDir(), pidData.PID)
		}
	}

	if _, err := h.EnsurePicoChannel(); err != nil {
		logger.ErrorC("gateway", fmt.Sprintf("Warning: failed to ensure pico channel: %v", err))
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return 0, fmt.Errorf("failed to load config: %w", err)
	}

	runtimeCtx, cancel := context.WithCancel(context.Background())
	runtime := &embeddedGatewayRuntime{
		cancel: cancel,
		done:   make(chan error, 1),
	}
	startedPRLifecycle := cfg.PRLifecycle.Effective()
	defaultModelName := strings.TrimSpace(cfg.Agents.Defaults.GetModelName())
	bootConfigSignature := computeGatewayRuntimeSignature(cfg)

	gateway.mu.Lock()
	if gateway.hostClosing || gateway.embeddedBlocked || gateway.embedded != nil || gateway.cmd != nil {
		gateway.mu.Unlock()
		cancel()
		return 0, errors.New("gateway lifecycle changed during embedded startup")
	}
	gateway.embeddedGeneration++
	runtime.generation = gateway.embeddedGeneration
	gateway.embedded = runtime
	gateway.owned = true
	gateway.pidData = nil
	gateway.bootDefaultModel = defaultModelName
	gateway.bootConfigSignature = bootConfigSignature
	refreshPicoTokensLocked(h.configPath)
	setGatewayRuntimeStatusLocked(initialStatus)
	gateway.logs.Reset()
	gateway.logs.Append(fmt.Sprintf("Gateway runtime starting inside launcher PID %d", os.Getpid()))
	gateway.mu.Unlock()

	options := EmbeddedGatewayRunOptions{
		Debug:             h.debug,
		HomePath:          globalConfigDir(),
		ConfigPath:        h.configPath,
		AllowEmptyStartup: true,
		ManageLogLevel:    true,
		// The launcher listener may be public, but the embedded transport stays
		// on its own configured (loopback by default) bind. Direct Gateway
		// exposure remains an explicit PICOCLAW_GATEWAY_HOST/config choice.
		GatewayHostOverride: "",
		OnReady: func(pidData ppid.PidFileData) {
			ready := false
			gateway.mu.Lock()
			if gateway.embedded == runtime &&
				gateway.embeddedGeneration == runtime.generation &&
				!gateway.hostClosing && runtimeCtx.Err() == nil &&
				(gateway.runtimeStatus == "starting" || gateway.runtimeStatus == "restarting") {
				readyData := pidData
				runtime.pidData = &readyData
				gateway.pidData = &readyData
				setGatewayRuntimeStatusLocked("running")
				gateway.logs.Append(fmt.Sprintf(
					"Gateway runtime ready inside launcher PID %d on %s:%d",
					pidData.PID,
					pidData.Host,
					pidData.Port,
				))
				ready = true
			}
			gateway.mu.Unlock()
			if ready {
				h.markPRLifecycleGatewayApplied(startedPRLifecycle)
			}
		},
	}

	runner := h.embeddedGatewayRunner
	if runner == nil {
		runner = gatewayRunEmbedded
	}
	go func() {
		runErr := runEmbeddedGatewaySafely(runtimeCtx, options, runner)
		gateway.mu.Lock()
		if gateway.embedded == runtime &&
			gateway.embeddedGeneration == runtime.generation {
			gateway.embedded = nil
			gateway.owned = false
			gateway.pidData = nil
			gateway.bootDefaultModel = ""
			gateway.bootConfigSignature = ""
			if errors.Is(runErr, ErrEmbeddedGatewayRuntimeNotRetired) {
				gateway.embeddedBlocked = true
			}
			if runErr != nil {
				setGatewayRuntimeStatusLocked("error")
				gateway.logs.Append("Embedded gateway stopped with error: " + runErr.Error())
			} else {
				if gateway.runtimeStatus != "restarting" {
					setGatewayRuntimeStatusLocked("stopped")
				}
				gateway.logs.Append("Embedded gateway stopped")
			}
		}
		gateway.mu.Unlock()
		runtime.done <- runErr
		close(runtime.done)
		if errors.Is(runErr, ErrEmbeddedGatewayRuntimeNotRetired) && h.gatewayFatal != nil {
			h.gatewayFatal(runErr)
		}
		if runErr != nil {
			logger.ErrorC("gateway", "Embedded gateway runtime stopped: "+runErr.Error())
		} else {
			logger.InfoC("gateway", "Embedded gateway runtime stopped")
		}
	}()

	return os.Getpid(), nil
}

func runEmbeddedGatewaySafely(
	ctx context.Context,
	options EmbeddedGatewayRunOptions,
	runner EmbeddedGatewayRunner,
) (runErr error) {
	defer func() {
		if recover() != nil {
			runErr = errors.Join(
				ErrEmbeddedGatewayRuntimeNotRetired,
				errors.New("embedded gateway runtime panicked"),
			)
		}
	}()
	return runner(ctx, options)
}

func (h *Handler) stopEmbeddedGateway() (int, bool, error) {
	gateway.operationMu.Lock()
	defer gateway.operationMu.Unlock()
	return h.stopEmbeddedGatewayOperation()
}

func (h *Handler) stopEmbeddedGatewayOperation() (int, bool, error) {
	gateway.mu.Lock()
	runtime := gateway.embedded
	if runtime == nil {
		gateway.mu.Unlock()
		return 0, false, nil
	}
	pID := os.Getpid()
	if gateway.runtimeStatus != "restarting" {
		setGatewayRuntimeStatusLocked("stopping")
	}
	runtime.cancel()
	gateway.mu.Unlock()

	timer := time.NewTimer(embeddedGatewayStopLimit)
	defer timer.Stop()
	select {
	case runErr := <-runtime.done:
		if runErr != nil {
			return pID, true, runErr
		}
		return pID, true, nil
	case <-timer.C:
		timeoutErr := errors.Join(
			ErrEmbeddedGatewayRuntimeNotRetired,
			errors.New("timed out waiting for embedded gateway shutdown; restart the launcher"),
		)
		gateway.mu.Lock()
		if gateway.embedded == runtime {
			gateway.embeddedBlocked = true
			setGatewayRuntimeStatusLocked("error")
		}
		gateway.mu.Unlock()
		if h.gatewayFatal != nil {
			h.gatewayFatal(timeoutErr)
		}
		return pID, true, timeoutErr
	}
}

func (h *Handler) restartEmbeddedGateway() (int, error) {
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		return 0, fmt.Errorf("failed to validate gateway start conditions: %w", err)
	}
	if !ready {
		return 0, &preconditionFailedError{reason: reason}
	}

	gateway.operationMu.Lock()
	defer gateway.operationMu.Unlock()
	gateway.mu.Lock()
	if gateway.hostClosing {
		gateway.mu.Unlock()
		return 0, errors.New("launcher is shutting down")
	}
	if gateway.embeddedBlocked {
		gateway.mu.Unlock()
		return 0, errors.New("embedded gateway did not retire cleanly; restart the launcher")
	}
	setGatewayRuntimeStatusLocked("restarting")
	gateway.mu.Unlock()
	if _, _, err = h.stopEmbeddedGatewayOperation(); err != nil {
		return 0, fmt.Errorf("failed to stop embedded gateway: %w", err)
	}
	pid, err := h.startEmbeddedGatewayOperation("restarting")
	if err != nil {
		gateway.mu.Lock()
		setGatewayRuntimeStatusLocked("error")
		gateway.mu.Unlock()
	}
	return pid, err
}

func (h *Handler) beginEmbeddedGatewayShutdown() {
	if h == nil || !h.embedGateway {
		return
	}
	gateway.mu.Lock()
	gateway.hostClosing = true
	gateway.mu.Unlock()
}

func (h *Handler) handleEmbeddedGatewayStart(w http.ResponseWriter) {
	ready, reason, err := h.gatewayStartReady()
	if err != nil {
		http.Error(
			w,
			fmt.Sprintf("Failed to validate gateway start conditions: %v", err),
			http.StatusInternalServerError,
		)
		return
	}
	if !ready {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "precondition_failed", "message": reason,
		})
		return
	}
	pid, err := h.startEmbeddedGateway("starting")
	if err != nil {
		http.Error(
			w,
			fmt.Sprintf("Failed to start embedded gateway: %v", err),
			http.StatusInternalServerError,
		)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "pid": pid})
}

func (h *Handler) handleEmbeddedGatewayStop(w http.ResponseWriter) {
	pid, running, err := h.stopEmbeddedGateway()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to stop embedded gateway: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if !running {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "not_running"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "pid": pid})
}

func embeddedGatewayPIDDataMatchesLocked(pidData *ppid.PidFileData) bool {
	if pidData == nil || gateway.embedded == nil || gateway.embedded.pidData == nil ||
		gateway.runtimeStatus != "running" || gateway.hostClosing {
		return false
	}
	live := gateway.embedded.pidData
	return pidData.PID == os.Getpid() &&
		pidData.PID == live.PID &&
		pidData.Token != "" &&
		pidData.Token == live.Token &&
		pidData.Host == live.Host &&
		pidData.Port == live.Port
}

func (h *Handler) gatewayPIDDataForProxy(candidate *ppid.PidFileData) *ppid.PidFileData {
	if h == nil || !h.embedGateway {
		return candidate
	}
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	if gateway.embedded == nil || gateway.embedded.pidData == nil ||
		gateway.runtimeStatus != "running" || gateway.hostClosing {
		return nil
	}
	pidData := *gateway.embedded.pidData
	return &pidData
}

func gatewayRuntimeAliveLocked() bool {
	if gateway.embedded != nil {
		return true
	}
	return isCmdProcessAliveLocked(gateway.cmd)
}

func gatewayRuntimePIDLocked() int {
	if gateway.embedded != nil {
		return os.Getpid()
	}
	if gateway.cmd == nil || gateway.cmd.Process == nil {
		return 0
	}
	return gateway.cmd.Process.Pid
}
