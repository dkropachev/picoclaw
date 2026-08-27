package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/audio/asr"
	"github.com/sipeed/picoclaw/pkg/audio/tts"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	_ "github.com/sipeed/picoclaw/pkg/channels/deltachat"
	_ "github.com/sipeed/picoclaw/pkg/channels/dingtalk"
	_ "github.com/sipeed/picoclaw/pkg/channels/discord"
	_ "github.com/sipeed/picoclaw/pkg/channels/feishu"
	_ "github.com/sipeed/picoclaw/pkg/channels/irc"
	_ "github.com/sipeed/picoclaw/pkg/channels/line"
	_ "github.com/sipeed/picoclaw/pkg/channels/maixcam"
	_ "github.com/sipeed/picoclaw/pkg/channels/mqtt"
	_ "github.com/sipeed/picoclaw/pkg/channels/onebot"
	_ "github.com/sipeed/picoclaw/pkg/channels/pico"
	_ "github.com/sipeed/picoclaw/pkg/channels/qq"
	_ "github.com/sipeed/picoclaw/pkg/channels/slack"
	_ "github.com/sipeed/picoclaw/pkg/channels/slack_webhook"
	_ "github.com/sipeed/picoclaw/pkg/channels/teams_webhook"
	_ "github.com/sipeed/picoclaw/pkg/channels/telegram"
	_ "github.com/sipeed/picoclaw/pkg/channels/vk"
	_ "github.com/sipeed/picoclaw/pkg/channels/wecom"
	_ "github.com/sipeed/picoclaw/pkg/channels/weixin"
	_ "github.com/sipeed/picoclaw/pkg/channels/whatsapp"
	_ "github.com/sipeed/picoclaw/pkg/channels/whatsapp_native"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/cron"
	"github.com/sipeed/picoclaw/pkg/devices"
	eventchannel "github.com/sipeed/picoclaw/pkg/eventing/channelmessage"
	eventoperator "github.com/sipeed/picoclaw/pkg/eventing/operator"
	eventwebhook "github.com/sipeed/picoclaw/pkg/eventing/webhook"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/health"
	"github.com/sipeed/picoclaw/pkg/heartbeat"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/netbind"
	"github.com/sipeed/picoclaw/pkg/pid"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/state"
	"github.com/sipeed/picoclaw/pkg/tools"
)

const (
	serviceShutdownTimeout  = 30 * time.Second
	providerReloadTimeout   = 30 * time.Second
	gracefulShutdownTimeout = 15 * time.Second

	logPath   = "logs"
	panicFile = "gateway_panic.log"
	logFile   = "gateway.log"
)

type services struct {
	CronService                        *cron.CronService
	HeartbeatService                   *heartbeat.HeartbeatService
	MediaStore                         media.MediaStore
	ChannelManager                     *channels.Manager
	DeviceService                      *devices.Service
	EventAutomation                    *eventAutomationService
	HealthServer                       *health.Server
	eventChannelBus                    *bus.MessageBus
	eventChannelController             *eventchannel.Controller
	eventChannelGeneration             eventchannel.Generation
	eventChannelInstalled              bool
	eventChannelRelease                func()
	eventWebhookController             *eventwebhook.Controller
	eventWebhookGeneration             eventwebhook.Generation
	eventWebhookRelease                func()
	eventOperatorController            *eventoperator.Controller
	eventOperatorGeneration            eventoperator.Generation
	eventOperatorRelease               func()
	workflowAuthoringHandler           *workflowAuthoringCapabilitiesHandler
	workflowAuthoringRelease           func()
	agentActivityHandler               *agentActivityHandler
	agentActivityRelease               func()
	repositoryReviewPublicationHandler *repositoryReviewPublicationHandler
	repositoryReviewPublicationRelease func()
	VoiceAgentCancel                   context.CancelFunc
	manualReloadChan                   chan struct{}
	reloading                          atomic.Bool
	authToken                          string
}

type startupBlockedProvider struct {
	reason string
}

func logChannelVoiceCapabilities(cm *channels.Manager, asrAvailable bool, ttsAvailable bool) {
	if cm == nil {
		return
	}

	names := cm.GetEnabledChannels()
	sort.Strings(names)
	for _, name := range names {
		ch, ok := cm.GetChannel(name)
		if !ok {
			continue
		}
		caps := channels.DetectVoiceCapabilities(name, ch, asrAvailable, ttsAvailable)
		logger.InfoCF("voice", "Channel voice capabilities", map[string]any{
			"channel": name,
			"asr":     caps.ASR,
			"tts":     caps.TTS,
		})
	}
}

func (p *startupBlockedProvider) Chat(
	_ context.Context,
	_ []providers.Message,
	_ []providers.ToolDefinition,
	_ string,
	_ map[string]any,
) (*providers.LLMResponse, error) {
	return nil, fmt.Errorf("%s", p.reason)
}

// Run starts the gateway runtime using the configuration loaded from configPath.
func Run(debug bool, homePath, configPath string, allowEmptyStartup bool) (runErr error) {
	startedAt := time.Now()
	panicPath := filepath.Join(homePath, logPath, panicFile)
	panicFunc, err := logger.InitPanic(panicPath)
	if err != nil {
		return fmt.Errorf("error initializing panic log: %w", err)
	}
	defer panicFunc()

	if err = logger.EnableFileLogging(filepath.Join(homePath, logPath, logFile)); err != nil {
		logger.Fatal(fmt.Sprintf("error enabling file logging: %v", err))
	}
	defer logger.DisableFileLogging()

	if debug {
		logger.SetLevel(logger.DEBUG)
	} else {
		logger.SetLevelFromString(config.ResolveGatewayLogLevel(configPath))
	}
	defer func() {
		if runErr != nil {
			logger.ErrorCF("gateway", "Gateway startup failed", map[string]any{
				"config_path": configPath,
				"error":       runErr.Error(),
				"home_path":   homePath,
				"allow_empty": allowEmptyStartup,
				"debug":       debug,
			})
		}
	}()

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("error loading config: %w", err)
	}

	if err = preCheckConfig(cfg); err != nil {
		return fmt.Errorf("config pre-check failed: %w", err)
	}

	// Debug mode permanently overrides the config log level to DEBUG.
	if debug {
		fmt.Println("🔍 Debug mode enabled")
	} else {
		effectiveLogLevel := config.EffectiveGatewayLogLevel(cfg)
		logger.SetLevelFromString(effectiveLogLevel)
		logger.Infof("Log level set to %q", effectiveLogLevel)
	}

	bindPlan, listenResult, err := openGatewayListeners(cfg.Gateway.Host, cfg.Gateway.Port)
	if err != nil {
		return fmt.Errorf("error opening gateway listeners: %w", err)
	}

	pidProbeHost, err := gatewayPIDProbeHost(
		bindPlan.ProbeHost,
		listenResult.Listeners,
	)
	if err != nil {
		for _, ln := range listenResult.Listeners {
			_ = ln.Close()
		}
		return fmt.Errorf("select gateway PID probe host: %w", err)
	}

	// Enforce singleton: write PID file with generated token.
	pidData, err := pid.WritePidFile(homePath, pidProbeHost, cfg.Gateway.Port)
	if err != nil {
		logger.Warnf("write pid file failed: %v", err)
		for _, ln := range listenResult.Listeners {
			_ = ln.Close()
		}
		return fmt.Errorf("singleton check failed: %w", err)
	}
	defer pid.RemovePidFile(homePath)
	closeListeners := true
	defer func() {
		if !closeListeners {
			return
		}
		for _, ln := range listenResult.Listeners {
			_ = ln.Close()
		}
	}()

	provider, _, err := createStartupProvider(cfg, allowEmptyStartup)
	if err != nil {
		return fmt.Errorf("error creating provider: %w", err)
	}

	msgBus := bus.NewMessageBus()
	agentLoop := agent.NewAgentLoop(
		cfg,
		msgBus,
		provider,
		agent.WithConfigPath(configPath),
		agent.WithRuntimeStartupBarrier(),
		agent.WithDeferredEvolutionActivation(),
	)
	var runningServices *services
	startupResourcesOwned := true
	defer func() {
		if startupResourcesOwned {
			cleanupFailedGatewayStartup(runningServices, agentLoop, provider, msgBus)
		}
	}()
	if err = agentLoop.ActivateEvolution(); err != nil {
		return fmt.Errorf("activate startup evolution runtime: %w", err)
	}
	msgBus.SetEventPublisher(agentLoop.RuntimeEventBus())
	publishGatewayEvent(agentLoop, runtimeevents.KindGatewayStart, startedAt, nil)

	fmt.Println("\n📦 Agent Status:")
	startupStatus := collectGatewayStartupStatus(agentLoop.GetStartupInfo())
	fmt.Printf("  • Tools: %d loaded\n", startupStatus.toolsCount)
	fmt.Printf("  • Skills: %d/%d available\n", startupStatus.skillsAvailable, startupStatus.skillsTotal)

	logger.InfoCF("agent", "Agent initialized", startupStatus.logFields)

	startupCtx, releaseStartup, err := agentLoop.AcquireRuntimeStartupUse(
		context.Background(),
		cfg,
	)
	if err != nil {
		return fmt.Errorf("claim startup runtime generation: %w", err)
	}
	func() {
		defer releaseStartup()
		runningServices, err = setupAndStartServices(
			startupCtx,
			cfg,
			agentLoop,
			msgBus,
			pidData.Token,
			listenResult,
		)
	}()
	if err != nil {
		return err
	}

	// Setup manual reload channel for /reload endpoint before readiness.
	manualReloadChan := make(chan struct{}, 1)
	runningServices.manualReloadChan = manualReloadChan
	reloadTrigger := func() error {
		if !runningServices.reloading.CompareAndSwap(false, true) {
			return fmt.Errorf("reload already in progress")
		}
		select {
		case manualReloadChan <- struct{}{}:
			return nil
		default:
			// Should not happen, but reset flag if channel is full
			runningServices.reloading.Store(false)
			return fmt.Errorf("reload already queued")
		}
	}
	runningServices.HealthServer.SetReloadFunc(reloadTrigger)
	agentLoop.SetReloadFunc(reloadTrigger)

	// Event admission is the final generation-specific service committed before
	// readiness. Webhook requests return 503 and configured channel publishers
	// wait until these activations publish the durable store backends.
	if err = activateEventAdmissions(runningServices); err != nil {
		return err
	}

	// All services (channels + shared HTTP server) are up; mark the health
	// server ready so GET /ready reports "ready". The health endpoints are
	// mounted on the shared gateway mux, so Health.Server.Start() (which would
	// otherwise set this) is never called — we flip the flag explicitly here.
	runningServices.HealthServer.SetReady(true)
	publishGatewayEvent(agentLoop, runtimeevents.KindGatewayReady, startedAt, nil)
	closeListeners = false

	// Readiness is now externally observable, so autonomous runtime work may
	// begin. From here on, normal shutdown owns all initialized resources.
	agentLoop.ReleaseRuntimeStartupBarrier()
	startupResourcesOwned = false

	for _, bindHost := range listenResult.BindHosts {
		fmt.Printf("✓ Gateway started on %s\n", net.JoinHostPort(bindHost, strconv.Itoa(cfg.Gateway.Port)))
	}
	fmt.Println("Press Ctrl+C to stop")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go agentLoop.Run(ctx)

	var configReloadChan <-chan *config.Config
	stopWatch := func() {}
	if cfg.Gateway.HotReload {
		configReloadChan, stopWatch = setupConfigWatcherPolling(configPath, debug)
		logger.Info("Config hot reload enabled")
	}
	defer stopWatch()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	for {
		select {
		case <-sigChan:
			logger.Info("Shutting down...")
			cancel()
			shutdownGateway(runningServices, agentLoop, provider, msgBus, true)
			return nil
		case newCfg := <-configReloadChan:
			if !runningServices.reloading.CompareAndSwap(false, true) {
				logger.Warn("Config reload skipped: another reload is in progress")
				continue
			}
			err := executeReload(ctx, agentLoop, newCfg, &provider, runningServices, msgBus, allowEmptyStartup, debug)
			if err != nil {
				logger.Errorf("Config reload failed: %v", err)
			}
		case <-manualReloadChan:
			logger.Info("Manual reload triggered via /reload endpoint")
			newCfg, err := config.LoadConfig(configPath)
			if err != nil {
				logger.Errorf("Error loading config for manual reload: %v", err)
				runningServices.reloading.Store(false)
				continue
			}
			if err = newCfg.ValidateModelList(); err != nil {
				logger.Errorf("Config validation failed: %v", err)
				runningServices.reloading.Store(false)
				continue
			}
			err = executeReload(ctx, agentLoop, newCfg, &provider, runningServices, msgBus, allowEmptyStartup, debug)
			if err != nil {
				logger.Errorf("Manual reload failed: %v", err)
			} else {
				logger.Info("Manual reload completed successfully")
			}
		}
	}
}

func gatewayPIDProbeHost(
	plannedProbeHost string,
	listeners []net.Listener,
) (string, error) {
	// Adaptive wildcard listeners deliberately publish a concrete loopback as
	// their planned probe host. A localhost or custom-hostname plan must instead
	// publish an address from the listener that actually opened: preserving the
	// hostname could send a bearer-bearing internal request to an occupied
	// address from the other IP family.
	if ip := net.ParseIP(plannedProbeHost); ip != nil {
		if ip.IsUnspecified() || ip.IsMulticast() {
			return "", fmt.Errorf(
				"planned probe host %q is not a concrete unicast address",
				plannedProbeHost,
			)
		}
		return ip.String(), nil
	}

	// A hostname was already resolved by net.Listen. Reuse the concrete address
	// selected for the listener instead of making PID consumers resolve it
	// again.
	for _, listener := range listeners {
		tcpAddr, ok := listener.Addr().(*net.TCPAddr)
		if !ok || tcpAddr.IP == nil ||
			tcpAddr.IP.IsUnspecified() ||
			tcpAddr.IP.IsMulticast() {
			continue
		}
		return tcpAddr.IP.String(), nil
	}
	// Wildcard listeners intentionally report an unspecified address. Publish
	// the matching numeric loopback family, which the opened wildcard listener
	// necessarily accepts, instead of retaining the ambiguous localhost probe.
	for _, listener := range listeners {
		tcpAddr, ok := listener.Addr().(*net.TCPAddr)
		if !ok || tcpAddr.IP == nil || !tcpAddr.IP.IsUnspecified() {
			continue
		}
		if tcpAddr.IP.To4() != nil {
			return "127.0.0.1", nil
		}
		if tcpAddr.IP.To16() != nil {
			return "::1", nil
		}
	}

	return "", fmt.Errorf(
		"no concrete listener address available for probe host %q",
		plannedProbeHost,
	)
}

func cleanupFailedGatewayStartup(
	runningServices *services,
	agentLoop *agent.AgentLoop,
	provider providers.LLMProvider,
	msgBus *bus.MessageBus,
) {
	shutdownGateway(runningServices, agentLoop, provider, msgBus, true)
}

func preCheckConfig(cfg *config.Config) error {
	if cfg.Gateway.Port <= 0 || cfg.Gateway.Port > 65535 {
		return fmt.Errorf("invalid gateway port: %d, port must be between 1 and 65535", cfg.Gateway.Port)
	}
	if err := cfg.Events.Ingress.Validate(); err != nil {
		return fmt.Errorf("invalid event ingress: %w", err)
	}
	if err := cfg.Events.Ingress.ValidateEventChannelAdapters(
		cfg.Channels,
		cfg.SensitiveDataValues()...,
	); err != nil {
		return fmt.Errorf("invalid event channel adapters: %w", err)
	}
	return nil
}

type gatewayStartupStatus struct {
	toolsCount      int
	skillsAvailable int
	skillsTotal     int
	logFields       map[string]any
}

func collectGatewayStartupStatus(startupInfo map[string]any) gatewayStartupStatus {
	status := gatewayStartupStatus{logFields: map[string]any{}}

	if toolsInfo, ok := startupInfo["tools"].(map[string]any); ok {
		if count, ok := startupInfoInt(toolsInfo["count"]); ok {
			status.toolsCount = count
			status.logFields["tools_count"] = count
		}
	}

	if skillsInfo, ok := startupInfo["skills"].(map[string]any); ok {
		if total, ok := startupInfoInt(skillsInfo["total"]); ok {
			status.skillsTotal = total
			status.logFields["skills_total"] = total
		}
		if available, ok := startupInfoInt(skillsInfo["available"]); ok {
			status.skillsAvailable = available
			status.logFields["skills_available"] = available
		}
	}

	return status
}

func startupInfoInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float32:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

func executeReload(
	ctx context.Context,
	agentLoop *agent.AgentLoop,
	newCfg *config.Config,
	provider *providers.LLMProvider,
	runningServices *services,
	msgBus *bus.MessageBus,
	allowEmptyStartup bool,
	debug bool,
) (err error) {
	startedAt := time.Now()
	publishGatewayEvent(agentLoop, runtimeevents.KindGatewayReloadStarted, startedAt, nil)
	defer runningServices.reloading.Store(false)
	defer func() {
		if err != nil {
			publishGatewayEvent(agentLoop, runtimeevents.KindGatewayReloadFailed, startedAt, err)
			return
		}
		publishGatewayEvent(agentLoop, runtimeevents.KindGatewayReloadCompleted, startedAt, nil)
	}()

	err = handleConfigReload(ctx, agentLoop, newCfg, provider, runningServices, msgBus, allowEmptyStartup, debug)
	return err
}

func createStartupProvider(
	cfg *config.Config,
	allowEmptyStartup bool,
) (providers.LLMProvider, string, error) {
	modelName := strings.TrimSpace(cfg.Agents.Defaults.GetModelName())
	if modelName == "" && allowEmptyStartup {
		reason := config.ErrNoModelConfigured.Error()
		fmt.Printf("⚠ Warning: %s\n", reason)
		logger.WarnCF("gateway", "Gateway started without a configured model alias", map[string]any{
			"limited_mode": true,
		})
		return &startupBlockedProvider{reason: reason}, "", nil
	}

	provider, modelID, err := providers.CreateProvider(cfg)
	if err != nil {
		return nil, "", err
	}
	return provider, modelID, nil
}

func setupAndStartServices(
	ctx context.Context,
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
	msgBus *bus.MessageBus,
	authToken string,
	listenResult netbind.OpenResult,
) (*services, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateEventAutomationRuntime(ctx, cfg, agentLoop); err != nil {
		return nil, fmt.Errorf("validate event automation runtime: %w", err)
	}

	runningServices := &services{}
	if err := setupEventChannelController(runningServices, msgBus, cfg); err != nil {
		return runningServices, err
	}

	execTimeout := time.Duration(cfg.Tools.Cron.ExecTimeoutMinutes) * time.Minute
	var err error
	runningServices.CronService, err = setupCronTool(
		agentLoop,
		msgBus,
		cfg.WorkspacePath(),
		cfg.Agents.Defaults.RestrictToWorkspace,
		execTimeout,
		cfg,
	)
	if err != nil {
		return runningServices, fmt.Errorf("error setting up cron service: %w", err)
	}

	runningServices.HeartbeatService = heartbeat.NewHeartbeatService(
		cfg.WorkspacePath(),
		cfg.Heartbeat.Interval,
		cfg.Heartbeat.Enabled,
	)
	runningServices.HeartbeatService.SetBus(msgBus)
	runningServices.HeartbeatService.SetHandler(createHeartbeatHandler(agentLoop, cfg))

	runningServices.MediaStore = media.NewFileMediaStoreWithCleanup(media.MediaCleanerConfig{
		Enabled:  cfg.Tools.MediaCleanup.Enabled,
		MaxAge:   time.Duration(cfg.Tools.MediaCleanup.MaxAge) * time.Minute,
		Interval: time.Duration(cfg.Tools.MediaCleanup.Interval) * time.Minute,
	})
	if fms, ok := runningServices.MediaStore.(*media.FileMediaStore); ok {
		fms.Start()
	}

	runningServices.ChannelManager, err = channels.NewManager(
		cfg,
		msgBus,
		runningServices.MediaStore,
		channels.WithRuntimeEvents(agentLoop.RuntimeEventBus()),
	)
	if err != nil {
		return runningServices, fmt.Errorf("error creating channel manager: %w", err)
	}

	agentLoop.SetChannelManager(runningServices.ChannelManager)
	agentLoop.SetMediaStore(runningServices.MediaStore)

	transcriber := asr.DetectTranscriber(cfg)
	if transcriber != nil {
		agentLoop.SetTranscriber(transcriber)
		logger.InfoCF("voice", "Transcription enabled (agent-level)", map[string]any{"provider": transcriber.Name()})
	}

	ttsAvailable := tts.DetectTTS(cfg) != nil

	enabledChannels := runningServices.ChannelManager.GetEnabledChannels()
	if len(enabledChannels) > 0 {
		fmt.Printf("✓ Channels enabled: %s\n", enabledChannels)
	} else {
		fmt.Println("⚠ Warning: No channels enabled")
	}

	runningServices.authToken = authToken
	runningServices.HealthServer = health.NewServer(listenResult.ProbeHost, cfg.Gateway.Port, authToken)
	runningServices.HealthServer.SetActiveTurnsFunc(func() []health.ActiveTurn {
		activeTurns := agentLoop.GetActiveTurns()
		result := make([]health.ActiveTurn, 0, len(activeTurns))
		for _, turn := range activeTurns {
			result = append(result, health.ActiveTurn{
				TurnID:       turn.TurnID,
				AgentID:      turn.AgentID,
				SessionKey:   turn.SessionKey,
				Channel:      turn.Channel,
				ChatID:       turn.ChatID,
				Phase:        string(turn.Phase),
				StartedAt:    turn.StartedAt,
				Depth:        turn.Depth,
				ParentTurnID: turn.ParentTurnID,
				ChildTurnIDs: append([]string(nil), turn.ChildTurnIDs...),
			})
		}
		return result
	})

	var listenAddr string
	if len(listenResult.Listeners) > 0 {
		listenAddr = listenResult.Listeners[0].Addr().String()
	} else {
		listenAddr = net.JoinHostPort(listenResult.ProbeHost, strconv.Itoa(cfg.Gateway.Port))
	}
	runningServices.ChannelManager.SetupHTTPServerListeners(
		listenResult.Listeners,
		listenAddr,
		runningServices.HealthServer,
	)
	if err = prepareWorkflowAuthoringRoute(runningServices, agentLoop); err != nil {
		return runningServices, err
	}
	if err = prepareAgentActivityRoute(runningServices, agentLoop); err != nil {
		return runningServices, err
	}
	if err = prepareRepositoryReviewPublicationRoute(runningServices, agentLoop); err != nil {
		return runningServices, err
	}
	if err = prepareEventHTTPRoutesForConfig(runningServices, cfg); err != nil {
		return runningServices, err
	}
	if err = validateEventAutomationStorage(context.Background(), cfg); err != nil {
		return runningServices, fmt.Errorf("validate event automation storage: %w", err)
	}

	if err = runningServices.ChannelManager.StartAll(context.Background()); err != nil {
		return runningServices, fmt.Errorf("error starting channels: %w", err)
	}

	logChannelVoiceCapabilities(runningServices.ChannelManager, transcriber != nil, ttsAvailable)

	if transcriber != nil {
		// Start Voice Agent Orchestrator after channels are ready.
		vaCtx, vaCancel := context.WithCancel(context.Background())
		runningServices.VoiceAgentCancel = vaCancel
		voiceAgent := asr.NewAgent(msgBus, transcriber)
		voiceAgent.Start(vaCtx)
	}

	healthAddr := net.JoinHostPort(listenResult.ProbeHost, strconv.Itoa(cfg.Gateway.Port))
	fmt.Printf(
		"✓ Health endpoints available at http://%s/health, /ready and /reload (POST)\n",
		healthAddr,
	)

	stateManager := state.NewManager(cfg.WorkspacePath())
	runningServices.DeviceService = devices.NewService(devices.Config{
		Enabled:    cfg.Devices.Enabled,
		MonitorUSB: cfg.Devices.MonitorUSB,
	}, stateManager)
	runningServices.DeviceService.SetBus(msgBus)
	if err = runningServices.DeviceService.Start(context.Background()); err != nil {
		logger.ErrorCF("device", "Error starting device service", map[string]any{"error": err.Error()})
	} else if cfg.Devices.Enabled {
		fmt.Println("✓ Device event service started")
	}

	runningServices.EventAutomation, err = setupEventAutomationService(
		ctx,
		cfg,
		agentLoop,
	)
	if err != nil {
		return runningServices, fmt.Errorf("error starting event automation: %w", err)
	}
	if runningServices.EventAutomation != nil {
		if cfg.Workflows.Enabled {
			fmt.Println("✓ Durable event inbox and workflow workers started")
		} else {
			fmt.Println("✓ Durable event inbox opened (workflow dispatch disabled)")
		}
	}
	if err = runningServices.HeartbeatService.Start(); err != nil {
		return runningServices, fmt.Errorf("error starting heartbeat service: %w", err)
	}
	fmt.Println("✓ Heartbeat service started")

	// Cron is deliberately the final service started. Its durable store may
	// contain overdue jobs, so starting it before later fallible initialization
	// could execute work for a gateway that never becomes ready.
	if err = runningServices.CronService.Start(); err != nil {
		return runningServices, fmt.Errorf("error starting cron service: %w", err)
	}
	fmt.Println("✓ Cron service started")

	return runningServices, nil
}

func stopAndCleanupServices(
	runningServices *services,
	shutdownTimeout time.Duration,
	isReload bool,
) error {
	producerErr := stopRuntimeProducers(runningServices, shutdownTimeout)
	dependencyErr := stopRuntimeDependencies(
		runningServices,
		shutdownTimeout,
		!isReload,
	)
	return errors.Join(producerErr, dependencyErr)
}

func stopRuntimeProducers(
	runningServices *services,
	shutdownTimeout time.Duration,
) error {
	if runningServices == nil {
		return nil
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	var cleanupErr error
	admissionDrained := true
	if err := deactivateEventChannel(shutdownCtx, runningServices, nil); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
		admissionDrained = false
	}
	if err := deactivateEventOperator(shutdownCtx, runningServices); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
		admissionDrained = false
	}
	if err := deactivateEventWebhook(shutdownCtx, runningServices); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
		admissionDrained = false
	}
	if admissionDrained {
		if err := closeEventAutomationService(
			shutdownCtx,
			&runningServices.EventAutomation,
		); err != nil {
			cleanupErr = errors.Join(
				cleanupErr,
				fmt.Errorf("stop event automation: %w", err),
			)
		}
	}

	if runningServices.VoiceAgentCancel != nil {
		runningServices.VoiceAgentCancel()
		runningServices.VoiceAgentCancel = nil
	}
	if runningServices.DeviceService != nil {
		runningServices.DeviceService.Stop()
	}
	if runningServices.HeartbeatService != nil {
		runningServices.HeartbeatService.Stop()
	}
	if runningServices.CronService != nil {
		runningServices.CronService.Stop()
	}
	return cleanupErr
}

func stopRuntimeDependencies(
	runningServices *services,
	shutdownTimeout time.Duration,
	stopChannels bool,
) error {
	if runningServices == nil {
		return nil
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	var cleanupErr error
	if stopChannels && runningServices.ChannelManager != nil {
		if err := runningServices.ChannelManager.StopAll(shutdownCtx); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("stop channels: %w", err))
		} else {
			releaseEventHTTPRoutes(runningServices)
		}
	} else if stopChannels {
		releaseEventHTTPRoutes(runningServices)
	}
	if runningServices.MediaStore != nil {
		if fms, ok := runningServices.MediaStore.(*media.FileMediaStore); ok {
			fms.Stop()
		}
	}
	return cleanupErr
}

func shutdownGateway(
	runningServices *services,
	agentLoop *agent.AgentLoop,
	provider providers.LLMProvider,
	msgBus *bus.MessageBus,
	fullShutdown bool,
) {
	publishGatewayEvent(agentLoop, runtimeevents.KindGatewayShutdown, time.Time{}, nil)

	producerErr := stopRuntimeProducers(runningServices, gracefulShutdownTimeout)
	if producerErr != nil {
		logger.ErrorCF("gateway", "Failed to stop runtime producers cleanly", map[string]any{
			"error": producerErr.Error(),
		})
	}

	agentLoop.Stop()
	drainCtx, drainCancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
	defer drainCancel()
	runStopErr := agentLoop.WaitStopped(drainCtx)
	if runStopErr != nil {
		logger.ErrorCF("gateway", "Agent loop did not stop cleanly", map[string]any{"error": runStopErr.Error()})
	}
	_, runtimeDrainErr := agentLoop.PauseRuntimeForReload(drainCtx)
	if runtimeDrainErr != nil {
		logger.ErrorCF("gateway", "Agent runtime did not drain cleanly", map[string]any{
			"error": runtimeDrainErr.Error(),
		})
	}

	var dependencyErr error
	admissionCloseErr := closeEventChannelAdmission(drainCtx, runningServices)
	if admissionCloseErr != nil {
		logger.ErrorCF("gateway", "Channel event admission did not close cleanly", map[string]any{
			"error": admissionCloseErr.Error(),
		})
	}
	if runStopErr == nil && runtimeDrainErr == nil && admissionCloseErr == nil {
		dependencyErr = stopRuntimeDependencies(
			runningServices,
			gracefulShutdownTimeout,
			true,
		)
		if dependencyErr != nil {
			logger.ErrorCF("gateway", "Failed to stop runtime dependencies cleanly", map[string]any{
				"error": dependencyErr.Error(),
			})
		}
	}

	// Keep the terminal runtime pause held. Resuming it after closing the
	// provider would allow a blocked background admission onto closed state.
	safeToClose := producerErr == nil &&
		runStopErr == nil &&
		runtimeDrainErr == nil &&
		admissionCloseErr == nil &&
		dependencyErr == nil
	if safeToClose {
		if fullShutdown && msgBus != nil {
			msgBus.Close()
		}
		agentLoop.Close()
		if cp, ok := provider.(providers.StatefulProvider); ok && fullShutdown {
			cp.Close()
		}
	}

	logger.Info("✓ Gateway stopped")
}

func handleConfigReload(
	ctx context.Context,
	al *agent.AgentLoop,
	newCfg *config.Config,
	providerRef *providers.LLMProvider,
	runningServices *services,
	msgBus *bus.MessageBus,
	allowEmptyStartup bool,
	debug bool,
) error {
	return handleConfigReloadWithServiceOps(
		ctx,
		al,
		newCfg,
		providerRef,
		runningServices,
		msgBus,
		allowEmptyStartup,
		debug,
		configReloadServiceOps{
			stop:    stopAndCleanupServices,
			restart: restartServices,
		},
	)
}

type configReloadServiceOps struct {
	stop    func(*services, time.Duration, bool) error
	restart func(context.Context, *agent.AgentLoop, *services, *bus.MessageBus) error
}

func handleConfigReloadWithServiceOps(
	ctx context.Context,
	al *agent.AgentLoop,
	newCfg *config.Config,
	providerRef *providers.LLMProvider,
	runningServices *services,
	msgBus *bus.MessageBus,
	allowEmptyStartup bool,
	debug bool,
	serviceOps configReloadServiceOps,
) error {
	logger.Info("🔄 Config file changed, reloading...")

	if al == nil || al.GetConfig() == nil || providerRef == nil || *providerRef == nil {
		return fmt.Errorf("active agent configuration and provider are required for reload")
	}
	if serviceOps.stop == nil || serviceOps.restart == nil {
		return fmt.Errorf("reload service lifecycle operations are required")
	}

	oldCfg := al.GetConfig()
	oldProvider := *providerRef
	newModel := newCfg.Agents.Defaults.ModelName
	hadEventWebhookRoute := runningServices != nil &&
		runningServices.eventWebhookRelease != nil
	hadEventOperatorRoute := runningServices != nil &&
		runningServices.eventOperatorRelease != nil
	prepareEventRoutesErr := prepareEventHTTPRoutesForConfig(runningServices, newCfg)
	var provisionalEventWebhookRelease func()
	if !hadEventWebhookRoute &&
		runningServices != nil &&
		runningServices.eventWebhookRelease != nil {
		provisionalEventWebhookRelease = runningServices.eventWebhookRelease
	}
	var provisionalEventOperatorRelease func()
	if !hadEventOperatorRoute &&
		runningServices != nil &&
		runningServices.eventOperatorRelease != nil {
		provisionalEventOperatorRelease = runningServices.eventOperatorRelease
	}
	retainProvisionalEventHTTPRoutes := false
	defer func() {
		if retainProvisionalEventHTTPRoutes {
			return
		}
		if provisionalEventOperatorRelease != nil {
			provisionalEventOperatorRelease()
		}
		if provisionalEventWebhookRelease != nil {
			provisionalEventWebhookRelease()
		}
		if runningServices != nil && provisionalEventOperatorRelease != nil {
			runningServices.eventOperatorRelease = nil
		}
		if runningServices != nil && provisionalEventWebhookRelease != nil {
			runningServices.eventWebhookRelease = nil
		}
	}()
	if prepareEventRoutesErr != nil {
		return fmt.Errorf(
			"prepare event HTTP routes before reload: %w",
			prepareEventRoutesErr,
		)
	}
	if err := validateEventAutomationStorage(ctx, newCfg); err != nil {
		return fmt.Errorf("validate event automation before reload: %w", err)
	}

	logger.Infof(" New model is '%s', recreating provider...", newModel)

	newProvider, _, err := createStartupProvider(newCfg, allowEmptyStartup)
	if err != nil {
		logger.Errorf("  ⚠ Error creating new provider: %v", err)
		return fmt.Errorf("error creating new provider: %w", err)
	}
	if err = prepareEventChannelAdmission(runningServices, newCfg); err != nil {
		if stateful, ok := newProvider.(providers.StatefulProvider); ok {
			stateful.Close()
		}
		return fmt.Errorf("prepare channel event admission before reload: %w", err)
	}

	if runningServices != nil && runningServices.HealthServer != nil {
		runningServices.HealthServer.SetReady(false)
	}
	// From this point, newly prepared routes remain mounted as retryable 503
	// if recovery itself fails. Successful rollback to a disabled old config
	// explicitly releases them through aggregate event admission activation.
	retainProvisionalEventHTTPRoutes = true
	httpDrainCtx, httpDrainCancel := context.WithTimeout(
		ctx,
		providerReloadTimeout,
	)
	operatorDrainErr := deactivateEventOperator(httpDrainCtx, runningServices)
	webhookDrainErr := deactivateEventWebhook(httpDrainCtx, runningServices)
	httpDrainCancel()
	httpDrainErr := errors.Join(operatorDrainErr, webhookDrainErr)
	if httpDrainErr != nil {
		if stateful, ok := newProvider.(providers.StatefulProvider); ok {
			stateful.Close()
		}
		return errors.Join(
			fmt.Errorf(
				"deactivate event HTTP admission for reload: %w",
				httpDrainErr,
			),
			cancelEventChannelPreparation(runningServices),
		)
	}
	pauseCtx, pauseCancel := context.WithTimeout(ctx, providerReloadTimeout)
	runtimeCtx, resumeRuntime, pauseErr := al.PauseRuntimeForReloadWithContext(
		pauseCtx,
		ctx,
	)
	pauseCancel()
	if pauseErr != nil {
		if stateful, ok := newProvider.(providers.StatefulProvider); ok {
			stateful.Close()
		}
		restoreChannelErr := cancelEventChannelPreparation(runningServices)
		reactivateHTTPErr := activateEventHTTPAdmissions(runningServices)
		if restoreChannelErr == nil &&
			reactivateHTTPErr == nil &&
			runningServices != nil &&
			runningServices.HealthServer != nil {
			runningServices.HealthServer.SetReady(true)
		}
		return errors.Join(
			fmt.Errorf("pause agent runtime for reload: %w", pauseErr),
			restoreChannelErr,
			reactivateHTTPErr,
		)
	}
	defer resumeRuntime()

	channelDrainCtx, channelDrainCancel := context.WithTimeout(
		ctx,
		providerReloadTimeout,
	)
	channelDrainErr := deactivateEventChannel(
		channelDrainCtx,
		runningServices,
		newCfg,
	)
	channelDrainCancel()
	if channelDrainErr != nil {
		if stateful, ok := newProvider.(providers.StatefulProvider); ok {
			stateful.Close()
		}
		return fmt.Errorf(
			"deactivate channel event admission for reload: %w",
			channelDrainErr,
		)
	}

	logger.Info("  Stopping all services...")
	if stopErr := serviceOps.stop(runningServices, serviceShutdownTimeout, true); stopErr != nil {
		if stateful, ok := newProvider.(providers.StatefulProvider); ok {
			stateful.Close()
		}
		recoveryStopErr := serviceOps.stop(runningServices, serviceShutdownTimeout, true)
		var recoveryRestartErr error
		var recoveryActivateErr error
		if recoveryStopErr == nil {
			recoveryRestartErr = prepareEventChannelAdmission(runningServices, oldCfg)
			if recoveryRestartErr == nil {
				recoveryRestartErr = serviceOps.restart(runtimeCtx, al, runningServices, msgBus)
			}
			if recoveryRestartErr == nil {
				recoveryActivateErr = activateEventAdmissions(runningServices)
			}
			if recoveryRestartErr == nil &&
				recoveryActivateErr == nil &&
				runningServices != nil &&
				runningServices.HealthServer != nil {
				runningServices.HealthServer.SetReady(true)
			}
		}
		return errors.Join(
			fmt.Errorf("stop services for reload: %w", stopErr),
			recoveryStopErr,
			recoveryRestartErr,
			recoveryActivateErr,
		)
	}

	reloadCtx, reloadCancel := context.WithTimeout(context.Background(), providerReloadTimeout)
	defer reloadCancel()

	previousProvider, err := al.ReloadProviderAndConfigRetainingPrevious(
		reloadCtx,
		newProvider,
		newCfg,
	)
	if err != nil {
		logger.Errorf("  ⚠ Error reloading agent loop: %v", err)
		if stateful, ok := newProvider.(providers.StatefulProvider); ok {
			stateful.Close()
		}
		logger.Warn("  Attempting to restart services with old provider and config...")
		restartErr := prepareEventChannelAdmission(runningServices, oldCfg)
		if restartErr == nil {
			restartErr = serviceOps.restart(runtimeCtx, al, runningServices, msgBus)
		}
		var reactivateErr error
		if restartErr == nil {
			reactivateErr = activateEventAdmissions(runningServices)
		}
		if restartErr != nil {
			logger.Errorf("  ⚠ Failed to restart services: %v", restartErr)
		} else if reactivateErr == nil &&
			runningServices != nil &&
			runningServices.HealthServer != nil {
			runningServices.HealthServer.SetReady(true)
		}
		return errors.Join(
			fmt.Errorf("error reloading agent loop: %w", err),
			restartErr,
			reactivateErr,
		)
	}
	if previousProvider == nil {
		previousProvider = oldProvider
	}

	logger.Info("  Preflighting and restarting all services with new configuration...")
	candidateServicesStarted := false
	restartErr := validateEventAutomationRuntime(runtimeCtx, newCfg, al)
	if restartErr != nil {
		restartErr = fmt.Errorf("preflight replacement event runtime: %w", restartErr)
	} else {
		candidateServicesStarted = true
		restartErr = serviceOps.restart(runtimeCtx, al, runningServices, msgBus)
		if restartErr == nil {
			restartErr = activateEventAdmissions(runningServices)
		}
	}
	if restartErr != nil {
		logger.Errorf("  ⚠ Error restarting services: %v", restartErr)
		var cleanupErr error
		if candidateServicesStarted {
			cleanupErr = serviceOps.stop(runningServices, serviceShutdownTimeout, true)
		}
		if cleanupErr != nil {
			*providerRef = newProvider
			closeRetainedProviderAfterReload(al, previousProvider)
			return errors.Join(
				fmt.Errorf("error restarting services: %w", restartErr),
				fmt.Errorf("stop partially restarted services: %w", cleanupErr),
			)
		}

		rollbackCtx, rollbackCancel := context.WithTimeout(
			context.Background(),
			providerReloadTimeout,
		)
		failedProvider, rollbackErr := al.ReloadProviderAndConfigRetainingPrevious(
			rollbackCtx,
			previousProvider,
			oldCfg,
		)
		rollbackCancel()
		if rollbackErr != nil {
			*providerRef = newProvider
			closeRetainedProviderAfterReload(al, previousProvider)
			return errors.Join(
				fmt.Errorf("error restarting services: %w", restartErr),
				fmt.Errorf("roll back agent configuration: %w", rollbackErr),
			)
		}
		if failedProvider == nil {
			failedProvider = newProvider
		}
		recoveryErr := prepareEventChannelAdmission(runningServices, oldCfg)
		if recoveryErr == nil {
			recoveryErr = serviceOps.restart(runtimeCtx, al, runningServices, msgBus)
		}
		var recoveryActivateErr error
		if recoveryErr == nil {
			recoveryActivateErr = activateEventAdmissions(runningServices)
		}
		if recoveryErr == nil &&
			recoveryActivateErr == nil &&
			runningServices != nil &&
			runningServices.HealthServer != nil {
			runningServices.HealthServer.SetReady(true)
		}
		closeRetainedProviderAfterReload(al, failedProvider)
		return errors.Join(
			fmt.Errorf("error restarting services: %w", restartErr),
			recoveryErr,
			recoveryActivateErr,
		)
	}

	*providerRef = newProvider
	if runningServices != nil && runningServices.HealthServer != nil {
		runningServices.HealthServer.SetReady(true)
	}
	closeRetainedProviderAfterReload(al, previousProvider)

	logger.Info("  ✓ Provider, configuration, and services reloaded successfully (thread-safe)")

	// Debug mode permanently overrides the config log level to DEBUG.
	if !debug {
		// Update log level last so that reload-related info/warn logs above are not suppressed.
		effectiveLogLevel := config.EffectiveGatewayLogLevel(newCfg)
		logger.SetLevelFromString(effectiveLogLevel)
		logger.Infof("Log level changing from current to %q", effectiveLogLevel)
	}

	return nil
}

func closeRetainedProviderAfterReload(
	agentLoop *agent.AgentLoop,
	provider providers.LLMProvider,
) {
	closeCtx, closeCancel := context.WithTimeout(context.Background(), providerReloadTimeout)
	defer closeCancel()
	agentLoop.CloseRetainedProvider(closeCtx, provider)
}

func restartServices(
	ctx context.Context,
	al *agent.AgentLoop,
	runningServices *services,
	msgBus *bus.MessageBus,
) error {
	cfg := al.GetConfig()

	execTimeout := time.Duration(cfg.Tools.Cron.ExecTimeoutMinutes) * time.Minute
	var err error
	runningServices.CronService, err = setupCronTool(
		al,
		msgBus,
		cfg.WorkspacePath(),
		cfg.Agents.Defaults.RestrictToWorkspace,
		execTimeout,
		cfg,
	)
	if err != nil {
		return fmt.Errorf("error restarting cron service: %w", err)
	}

	runningServices.HeartbeatService = heartbeat.NewHeartbeatService(
		cfg.WorkspacePath(),
		cfg.Heartbeat.Interval,
		cfg.Heartbeat.Enabled,
	)
	runningServices.HeartbeatService.SetBus(msgBus)
	runningServices.HeartbeatService.SetHandler(createHeartbeatHandler(al, cfg))
	if err = runningServices.HeartbeatService.Start(); err != nil {
		return fmt.Errorf("error restarting heartbeat service: %w", err)
	}
	fmt.Println("  ✓ Heartbeat service restarted")

	runningServices.MediaStore = media.NewFileMediaStoreWithCleanup(media.MediaCleanerConfig{
		Enabled:  cfg.Tools.MediaCleanup.Enabled,
		MaxAge:   time.Duration(cfg.Tools.MediaCleanup.MaxAge) * time.Minute,
		Interval: time.Duration(cfg.Tools.MediaCleanup.Interval) * time.Minute,
	})
	if fms, ok := runningServices.MediaStore.(*media.FileMediaStore); ok {
		fms.Start()
	}
	if runningServices.ChannelManager != nil {
		runningServices.ChannelManager.SetMediaStore(runningServices.MediaStore)
	}
	al.SetMediaStore(runningServices.MediaStore)

	al.SetChannelManager(runningServices.ChannelManager)

	if err = prepareWorkflowAuthoringRoute(runningServices, al); err != nil {
		return err
	}
	if err = prepareAgentActivityRoute(runningServices, al); err != nil {
		return err
	}
	if err = prepareRepositoryReviewPublicationRoute(runningServices, al); err != nil {
		return err
	}
	if err = prepareEventHTTPRoutesForConfig(runningServices, cfg); err != nil {
		return err
	}
	if err = runningServices.ChannelManager.Reload(context.Background(), cfg); err != nil {
		return fmt.Errorf("error reload channels: %w", err)
	}
	fmt.Println("  ✓ Channels restarted.")

	enabledChannels := runningServices.ChannelManager.GetEnabledChannels()
	if len(enabledChannels) > 0 {
		fmt.Printf("  ✓ Channels enabled: %s\n", enabledChannels)
	} else {
		fmt.Println("  ⚠ Warning: No channels enabled")
	}

	stateManager := state.NewManager(cfg.WorkspacePath())
	runningServices.DeviceService = devices.NewService(devices.Config{
		Enabled:    cfg.Devices.Enabled,
		MonitorUSB: cfg.Devices.MonitorUSB,
	}, stateManager)
	runningServices.DeviceService.SetBus(msgBus)
	if startErr := runningServices.DeviceService.Start(context.Background()); startErr != nil {
		logger.WarnCF("device", "Failed to restart device service", map[string]any{"error": startErr.Error()})
	} else if cfg.Devices.Enabled {
		fmt.Println("  ✓ Device event service restarted")
	}

	transcriber := asr.DetectTranscriber(cfg)
	al.SetTranscriber(transcriber)
	if transcriber != nil {
		logger.InfoCF("voice", "Transcription re-enabled (agent-level)", map[string]any{"provider": transcriber.Name()})

		// Start Voice Agent Orchestrator on reload
		vaCtx, vaCancel := context.WithCancel(context.Background())
		runningServices.VoiceAgentCancel = vaCancel
		voiceAgent := asr.NewAgent(msgBus, transcriber)
		voiceAgent.Start(vaCtx)
	} else {
		logger.InfoCF("voice", "Transcription disabled", nil)
	}

	ttsAvailable := tts.DetectTTS(cfg) != nil
	logChannelVoiceCapabilities(runningServices.ChannelManager, transcriber != nil, ttsAvailable)

	runningServices.EventAutomation, err = setupEventAutomationService(
		ctx,
		cfg,
		al,
	)
	if err != nil {
		return fmt.Errorf("error restarting event automation: %w", err)
	}
	if runningServices.EventAutomation != nil {
		if cfg.Workflows.Enabled {
			fmt.Println("  ✓ Durable event inbox and workflow workers restarted")
		} else {
			fmt.Println("  ✓ Durable event inbox reopened (workflow dispatch disabled)")
		}
	}
	// Start cron last. Event workers and agent-backed services are fenced by the
	// outer runtime pause, so no candidate scheduled command can run before all
	// other fallible replacement initialization has succeeded.
	if err = runningServices.CronService.Start(); err != nil {
		return fmt.Errorf("error restarting cron service: %w", err)
	}
	fmt.Println("  ✓ Cron service restarted")
	// NOTE: PID file is written once at startup and not updated on reload.
	// Changing the gateway listen address requires a full restart.

	return nil
}

func setupConfigWatcherPolling(configPath string, debug bool) (chan *config.Config, func()) {
	configChan := make(chan *config.Config, 1)
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()

		lastModTime := getFileModTime(configPath)
		lastSize := getFileSize(configPath)

		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				currentModTime := getFileModTime(configPath)
				currentSize := getFileSize(configPath)

				if currentModTime.After(lastModTime) || currentSize != lastSize {
					if debug {
						logger.Debugf("🔍 Config file change detected")
					}

					time.Sleep(500 * time.Millisecond)

					lastModTime = currentModTime
					lastSize = currentSize

					newCfg, err := config.LoadConfig(configPath)
					if err != nil {
						logger.Errorf("⚠ Error loading new config: %v", err)
						logger.Warn("  Using previous valid config")
						continue
					}

					if err := newCfg.ValidateModelList(); err != nil {
						logger.Errorf("  ⚠ New config validation failed: %v", err)
						logger.Warn("  Using previous valid config")
						continue
					}

					logger.Info("✓ Config file validated and loaded")

					select {
					case configChan <- newCfg:
					default:
						logger.Warn("⚠ Previous config reload still in progress, skipping")
					}
				}
			case <-stop:
				return
			}
		}
	}()

	stopFunc := func() {
		close(stop)
		wg.Wait()
	}

	return configChan, stopFunc
}

func getFileModTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func getFileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func setupCronTool(
	agentLoop *agent.AgentLoop,
	msgBus *bus.MessageBus,
	workspace string,
	restrict bool,
	execTimeout time.Duration,
	cfg *config.Config,
) (*cron.CronService, error) {
	cronStorePath := filepath.Join(workspace, "cron", "jobs.json")

	cronService := cron.NewCronService(cronStorePath, nil)
	cronService.SetJobAdmission(func(
		ctx context.Context,
		_ *cron.CronJob,
	) (context.Context, func(), error) {
		return agentLoop.AcquireRuntimeGeneration(ctx, cfg)
	})

	var cronTool *tools.CronTool
	if cfg.Tools.IsToolEnabled("cron") {
		var err error
		cronTool, err = tools.NewCronTool(cronService, agentLoop, msgBus, workspace, restrict, execTimeout, cfg)
		if err != nil {
			return nil, fmt.Errorf("critical error during CronTool initialization: %w", err)
		}

		agentLoop.RegisterTool(cronTool)
	}

	if cronTool != nil {
		cronService.SetOnJobContext(func(
			ctx context.Context,
			job *cron.CronJob,
		) (string, error) {
			result := cronTool.ExecuteJob(ctx, job)
			return result, nil
		})
	}

	return cronService, nil
}

func createHeartbeatHandler(
	agentLoop *agent.AgentLoop,
	runtimeConfig *config.Config,
) func(prompt, channel, chatID string) *tools.ToolResult {
	return func(prompt, channel, chatID string) *tools.ToolResult {
		ctx, releaseRuntime, err := agentLoop.AcquireRuntimeGeneration(
			context.Background(),
			runtimeConfig,
		)
		if err != nil {
			return tools.ErrorResult(fmt.Sprintf("Heartbeat runtime unavailable: %v", err))
		}
		defer releaseRuntime()

		if channel == "" || chatID == "" {
			channel, chatID = "cli", "direct"
		}

		response, err := agentLoop.ProcessHeartbeat(ctx, prompt, channel, chatID)
		if err != nil {
			return tools.ErrorResult(fmt.Sprintf("Heartbeat error: %v", err))
		}
		if response == "HEARTBEAT_OK" {
			return tools.SilentResult("Heartbeat OK")
		}
		return tools.SilentResult(response)
	}
}
