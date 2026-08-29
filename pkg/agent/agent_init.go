// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent/interfaces"
	"github.com/sipeed/picoclaw/pkg/audio/tts"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/channels"
	"github.com/sipeed/picoclaw/pkg/commands"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/skills"
	"github.com/sipeed/picoclaw/pkg/state"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func NewAgentLoop(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	provider providers.LLMProvider,
	opts ...AgentLoopOption,
) *AgentLoop {
	return newAgentLoop(
		cfg,
		msgBus,
		provider,
		isolation.NewExecutionPolicy(cfg.Isolation),
		logger.DiagnosticPolicy{},
		opts...,
	)
}

// NewAgentLoopWithExecutionPolicy constructs a loop from one caller-owned
// process generation snapshot. It avoids the compatibility constructor's
// default snapshot when the provider already owns this exact value.
func NewAgentLoopWithExecutionPolicy(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	provider providers.LLMProvider,
	policy isolation.ExecutionPolicy,
	opts ...AgentLoopOption,
) *AgentLoop {
	return newAgentLoop(
		cfg,
		msgBus,
		provider,
		policy,
		logger.DiagnosticPolicy{},
		opts...,
	)
}

// NewAgentLoopWithRuntimePolicies constructs a loop from one complete,
// caller-owned runtime-generation policy tuple. The diagnostic policy is an
// immutable owner cap; request contexts must still establish and narrow their
// own effective diagnostic authority.
func NewAgentLoopWithRuntimePolicies(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	provider providers.LLMProvider,
	executionPolicy isolation.ExecutionPolicy,
	diagnosticPolicy logger.DiagnosticPolicy,
	opts ...AgentLoopOption,
) *AgentLoop {
	return newAgentLoop(
		cfg,
		msgBus,
		provider,
		executionPolicy,
		diagnosticPolicy,
		opts...,
	)
}

func newAgentLoop(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	provider providers.LLMProvider,
	executionPolicy isolation.ExecutionPolicy,
	diagnosticPolicy logger.DiagnosticPolicy,
	opts ...AgentLoopOption,
) *AgentLoop {
	al := &AgentLoop{
		bus:                 msgBus,
		cfg:                 cfg,
		cmdRegistry:         commands.NewRegistry(commands.BuiltinDefinitions()),
		steering:            newSteeringQueue(parseSteeringMode(cfg.Agents.Defaults.SteeringMode)),
		gitWorkspaces:       newGitWorkspaceManagerFromConfig(cfg),
		ownsRuntimeEvents:   true,
		toolPolicy:          tools.CompatibilityAllowToolPolicy{},
		executionPolicy:     executionPolicy,
		diagnosticPolicy:    diagnosticPolicy,
		runtimeGenerationID: 1,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(al)
		}
	}
	if al.runtimeEvents == nil {
		al.runtimeEvents = runtimeevents.NewBus()
		al.ownsRuntimeEvents = true
	}

	registry := NewAgentRegistryWithRuntimePolicies(
		cfg,
		provider,
		al.executionPolicy,
		al.diagnosticPolicy,
	)
	al.registry = registry

	// Set up shared fallback chain with rate limiting.
	cooldown := providers.NewCooldownTracker()
	rl := providers.NewRateLimiterRegistry()
	// Register rate limiters for all agents' candidates so that RPM limits
	// configured in ModelConfig are enforced before each LLM call.
	for _, agentID := range registry.ListAgentIDs() {
		if agent, ok := registry.GetAgent(agentID); ok {
			rl.RegisterCandidates(agent.Candidates)
			rl.RegisterCandidates(agent.LightCandidates)
			if agent.AccountRouter != nil {
				for _, account := range agent.AccountRouter.Accounts {
					rl.RegisterCandidates(account.Candidates)
				}
			}
		}
	}
	fallbackChain := providers.NewFallbackChain(cooldown, rl)

	// Create state manager using default agent's workspace for channel recording
	defaultAgent := registry.GetDefaultAgent()
	var stateManager *state.Manager
	if defaultAgent != nil {
		stateManager = state.NewManager(defaultAgent.Workspace)
	}

	bridge, err := newEvolutionBridge(registry, cfg, provider)
	if err != nil {
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentFailedToInitializeEvolutionBridge,
			logger.NewSafeFields(
				agentDiagnosticErrorField(logger.ErrorClassInternal, err),
			),
		)
	}

	// Determine worker pool size from config (default: 1 = sequential)
	workerPoolSize := cfg.Agents.Defaults.MaxParallelTurns
	if workerPoolSize <= 0 {
		workerPoolSize = 1
	}

	al.state = stateManager
	al.fallback = fallbackChain
	al.evolution = bridge
	al.workerSem = make(chan struct{}, workerPoolSize)
	if bridge != nil {
		bridge.setCurrentCheck(al.isCurrentEvolutionBridge)
		if err := bridge.subscribeRuntimeEvents(al.runtimeEvents.Channel()); err != nil {
			logger.WarnSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentFailedToSubscribeEvolutionBridgeToRuntimeEvents,
				logger.NewSafeFields(
					agentDiagnosticErrorField(logger.ErrorClassInternal, err),
				),
			)
		}
		if !al.deferEvolutionActivation {
			if err := al.ActivateEvolution(); err != nil {
				logger.WarnSafeCF(
					logger.ComponentAgent,
					logger.DiagnosticMessageAgentFailedToActivateEvolutionBridge,
					logger.NewSafeFields(
						agentDiagnosticErrorField(logger.ErrorClassInternal, err),
					),
				)
			}
		}
	}
	al.activeReqCond = sync.NewCond(&al.activeReqMu)
	if err := al.initAgentActivityRecorder(); err != nil {
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentFailedToInitializeAgentActivityRecorder,
			logger.NewSafeFields(
				agentDiagnosticErrorField(logger.ErrorClassInternal, err),
			),
		)
	}
	al.refreshRuntimeEventLogger(cfg)
	al.policyProviderFactory = providers.CreateProviderFromConfigWithExecutionPolicy
	al.hooks = NewHookManager(al.runtimeEvents.Channel())
	configureHookManagerFromConfig(al.hooks, cfg)
	al.contextManager = al.resolveContextManager()

	// Register shared tools to all agents (now that al is created)
	if err := registerSharedTools(al, cfg, msgBus, registry, provider); err != nil {
		markRecursionCatalogConfigurationError(registry, err)
		logger.ErrorSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentFailedToInstallSharedRecursionToolCatalog,
			logger.NewSafeFields(
				agentDiagnosticErrorField(logger.ErrorClassInternal, err),
			),
		)
	}

	return al
}

func registerSharedTools(
	al *AgentLoop,
	cfg *config.Config,
	msgBus interfaces.MessageBus,
	registry *AgentRegistry,
	provider providers.LLMProvider,
) error {
	allowReadPaths := buildAllowReadPatterns(cfg)
	defaultAgent := registry.GetDefaultAgent()
	var ttsProvider tts.TTSProvider
	if cfg.Tools.IsToolEnabled("send_tts") {
		ttsProvider = tts.DetectTTS(cfg)
		if ttsProvider == nil {
			logger.WarnSafeCF(
				logger.ComponentVoiceTTS,
				logger.DiagnosticMessageVoiceTTSSendTTSEnabledButNoTTSProviderConfigured,
				logger.NewSafeFields(),
			)
		}
	}
	messageCallback := func(
		ctx context.Context,
		channel, chatID, content, replyToMessageID string,
		mediaParts []bus.MediaPart,
	) error {
		outboundCtx := bus.NewOutboundContext(channel, chatID, replyToMessageID)
		if tools.ToolChannel(ctx) == channel && tools.ToolChatID(ctx) == chatID {
			outboundCtx.TurnUXID = tools.ToolTurnUXID(ctx)
		}
		outboundCtx.TopicID = tools.ToolTopicID(ctx)
		outboundAgentID, outboundSessionKey, outboundScope := outboundTurnMetadata(
			tools.ToolAgentID(ctx),
			tools.ToolSessionKey(ctx),
			tools.ToolSessionScope(ctx),
		)
		if len(mediaParts) > 0 {
			outboundMedia := bus.OutboundMediaMessage{
				Channel:    channel,
				ChatID:     chatID,
				Context:    outboundCtx,
				AgentID:    outboundAgentID,
				SessionKey: outboundSessionKey,
				Scope:      outboundScope,
				Parts:      mediaParts,
			}
			if al.channelManager != nil && channel != "" {
				return al.channelManager.SendMedia(ctx, outboundMedia)
			}
			pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer pubCancel()
			return msgBus.PublishOutboundMedia(pubCtx, outboundMedia)
		}
		outboundMessage := bus.OutboundMessage{
			Channel:          channel,
			ChatID:           chatID,
			Context:          outboundCtx,
			AgentID:          outboundAgentID,
			SessionKey:       outboundSessionKey,
			Scope:            outboundScope,
			Content:          content,
			ReplyToMessageID: replyToMessageID,
		}
		if al.channelManager != nil && channel != "" {
			return al.channelManager.SendMessage(ctx, outboundMessage)
		}
		pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer pubCancel()
		return msgBus.PublishOutbound(pubCtx, outboundMessage)
	}
	reactionCallback := func(ctx context.Context, channel, chatID, messageID string) error {
		if al.channelManager == nil {
			return fmt.Errorf("channel manager not configured")
		}
		ch, ok := al.channelManager.GetChannel(channel)
		if !ok {
			return fmt.Errorf("channel %s not found", channel)
		}
		rc, ok := ch.(channels.ReactionCapable)
		if !ok {
			return fmt.Errorf("channel %s does not support reactions", channel)
		}
		_, err := rc.ReactToMessage(ctx, chatID, messageID)
		return err
	}

	agentIDs := registry.ListAgentIDs()
	sort.Strings(agentIDs)
	recursionCandidates := make([]recursionCatalogCandidate, 0, len(agentIDs))
	installLocks := &workspaceInstallLockCoordinator{}
	dependencies := defaultRecursionCatalogDependencies()
	if al.recursionInstaller != nil {
		dependencies.install = al.recursionInstaller
	}
	for _, agentID := range agentIDs {
		agent, ok := registry.GetAgent(agentID)
		if !ok {
			continue
		}

		if cfg.Tools.IsToolEnabled("web") {
			options := cloneWebSearchToolOptions(tools.WebSearchToolOptionsFromConfig(cfg))
			searchTool, err := tools.NewWebSearchTool(cloneWebSearchToolOptions(options))
			if err != nil {
				logger.ErrorSafeCF(
					logger.ComponentAgent,
					logger.DiagnosticMessageAgentFailedToCreateWebSearchTool,
					logger.NewSafeFields(
						agentDiagnosticErrorField(logger.ErrorClassValidation, err),
					),
				)
			} else if searchTool != nil {
				factory := mustToolFactoryFromPrototype(
					searchTool,
					mustBaseToolFactoryTraits("web_search"),
					func(tools.ToolBuildContext) (tools.Tool, error) {
						return tools.NewWebSearchTool(cloneWebSearchToolOptions(options))
					},
				)
				mustRegisterFactoryBackedTool(agent.Tools, searchTool, factory)
			}
		}
		if cfg.Tools.IsToolEnabled("web_fetch") {
			proxy := cfg.Tools.Web.Proxy
			format := cfg.Tools.Web.Format
			fetchLimitBytes := cfg.Tools.Web.FetchLimitBytes
			privateHostWhitelist := append(
				[]string(nil),
				cfg.Tools.Web.PrivateHostWhitelist...,
			)
			buildWebFetch := func() (*tools.WebFetchTool, error) {
				return tools.NewWebFetchToolWithProxy(
					50000,
					proxy,
					format,
					fetchLimitBytes,
					append([]string(nil), privateHostWhitelist...),
				)
			}
			fetchTool, err := buildWebFetch()
			if err != nil {
				logger.ErrorSafeCF(
					logger.ComponentAgent,
					logger.DiagnosticMessageAgentFailedToCreateWebFetchTool,
					logger.NewSafeFields(
						agentDiagnosticErrorField(logger.ErrorClassValidation, err),
					),
				)
			} else {
				factory := mustToolFactoryFromPrototype(
					fetchTool,
					mustBaseToolFactoryTraits("web_fetch"),
					func(tools.ToolBuildContext) (tools.Tool, error) { return buildWebFetch() },
				)
				mustRegisterFactoryBackedTool(agent.Tools, fetchTool, factory)
			}
		}
		if cfg.Tools.IsToolEnabled("git_workspace") && al.gitWorkspaces != nil {
			workspaceManager := al.gitWorkspaces
			buildGitWorkspace := func() tools.Tool {
				return tools.NewGitWorkspaceTool(workspaceManager)
			}
			live := buildGitWorkspace()
			mustRegisterFactoryBackedTool(agent.Tools, live, mustToolFactoryFromPrototype(
				live,
				mustBaseToolFactoryTraits("git_workspace"),
				func(tools.ToolBuildContext) (tools.Tool, error) {
					return buildGitWorkspace(), nil
				},
			))
		}

		// Hardware tools (I2C, SPI) - Linux only, returns error on other platforms
		if cfg.Tools.IsToolEnabled("i2c") {
			live := tools.NewI2CTool()
			mustRegisterFactoryBackedTool(agent.Tools, live, mustToolFactoryFromPrototype(
				live,
				mustBaseToolFactoryTraits("i2c"),
				func(tools.ToolBuildContext) (tools.Tool, error) { return tools.NewI2CTool(), nil },
			))
		}
		if cfg.Tools.IsToolEnabled("spi") {
			live := tools.NewSPITool()
			mustRegisterFactoryBackedTool(agent.Tools, live, mustToolFactoryFromPrototype(
				live,
				mustBaseToolFactoryTraits("spi"),
				func(tools.ToolBuildContext) (tools.Tool, error) { return tools.NewSPITool(), nil },
			))
		}
		if cfg.Tools.IsToolEnabled("serial") {
			live := tools.NewSerialTool()
			mustRegisterFactoryBackedTool(agent.Tools, live, mustToolFactoryFromPrototype(
				live,
				mustBaseToolFactoryTraits("serial"),
				func(tools.ToolBuildContext) (tools.Tool, error) { return tools.NewSerialTool(), nil },
			))
		}

		if cfg.Tools.IsToolEnabled("threads") && shouldRegisterThreadsTool(agent, defaultAgent) {
			agent.Tools.Register(tools.NewThreadsTool(cfg, al.configPath))
			if agent.Tools.HasRegistered(tools.ThreadsToolName) && agent.ContextBuilder != nil {
				agent.ContextBuilder.WithThreadPolicy(cfg)
			}
		}

		if cfg.Workflows.Enabled && cfg.Tools.IsToolEnabled("workflow") {
			agent.Tools.Register(newWorkflowTool(al, agentID, agent))
		}

		// Message tool
		if cfg.Tools.IsToolEnabled("message") {
			workspace := agent.Workspace
			restrictToWorkspace := cfg.Agents.Defaults.RestrictToWorkspace
			maxMediaSize := cfg.Agents.Defaults.GetMaxMediaSize()
			mediaEnabled := cfg.Tools.Message.MediaEnabled
			pathPatterns := cloneToolPathPatterns(allowReadPaths)
			buildMessage := func() tools.Tool {
				messageTool := tools.NewMessageTool()
				if mediaEnabled {
					messageTool.ConfigureLocalMedia(
						workspace,
						restrictToWorkspace,
						maxMediaSize,
						cloneToolPathPatterns(pathPatterns),
					)
				}
				messageTool.SetSendCallback(messageCallback)
				return messageTool
			}
			live := buildMessage()
			mustRegisterFactoryBackedTool(agent.Tools, live, mustToolFactoryFromPrototype(
				live,
				mustBaseToolFactoryTraits("message"),
				func(tools.ToolBuildContext) (tools.Tool, error) { return buildMessage(), nil },
			))
		}
		if cfg.Tools.IsToolEnabled("reaction") {
			buildReaction := func() tools.Tool {
				reactionTool := tools.NewReactionTool()
				reactionTool.SetReactionCallback(reactionCallback)
				return reactionTool
			}
			live := buildReaction()
			mustRegisterFactoryBackedTool(agent.Tools, live, mustToolFactoryFromPrototype(
				live,
				mustBaseToolFactoryTraits("reaction"),
				func(tools.ToolBuildContext) (tools.Tool, error) { return buildReaction(), nil },
			))
		}

		// Send file tool (outbound media via MediaStore — store injected later by SetMediaStore)
		if cfg.Tools.IsToolEnabled("send_file") {
			workspace := agent.Workspace
			restrictToWorkspace := cfg.Agents.Defaults.RestrictToWorkspace
			maxMediaSize := cfg.Agents.Defaults.GetMaxMediaSize()
			pathPatterns := cloneToolPathPatterns(allowReadPaths)
			buildSendFile := func() tools.Tool {
				return tools.NewSendFileTool(
					workspace,
					restrictToWorkspace,
					maxMediaSize,
					nil,
					cloneToolPathPatterns(pathPatterns),
				)
			}
			live := buildSendFile()
			mustRegisterFactoryBackedTool(agent.Tools, live, mustToolFactoryFromPrototype(
				live,
				mustBaseToolFactoryTraits("send_file"),
				func(tools.ToolBuildContext) (tools.Tool, error) { return buildSendFile(), nil },
			))
		}

		if ttsProvider != nil {
			buildSendTTS := func() tools.Tool {
				return tools.NewSendTTSTool(ttsProvider, nil)
			}
			live := buildSendTTS()
			mustRegisterFactoryBackedTool(agent.Tools, live, mustToolFactoryFromPrototype(
				live,
				mustBaseToolFactoryTraits("send_tts"),
				func(tools.ToolBuildContext) (tools.Tool, error) { return buildSendTTS(), nil },
			))
		}

		if cfg.Tools.IsToolEnabled("load_image") {
			workspace := agent.Workspace
			restrictToWorkspace := cfg.Agents.Defaults.RestrictToWorkspace
			maxMediaSize := cfg.Agents.Defaults.GetMaxMediaSize()
			pathPatterns := cloneToolPathPatterns(allowReadPaths)
			buildLoadImage := func() tools.Tool {
				return tools.NewLoadImageTool(
					workspace,
					restrictToWorkspace,
					maxMediaSize,
					nil,
					cloneToolPathPatterns(pathPatterns),
				)
			}
			loadImageTool := buildLoadImage()
			loadImageFactory := mustToolFactoryFromPrototype(
				loadImageTool,
				mustBaseToolFactoryTraits("load_image"),
				func(tools.ToolBuildContext) (tools.Tool, error) { return buildLoadImage(), nil },
			)
			mustRegisterFactoryDependency(agent.Tools, loadImageFactory)
			mustRegisterFactoryBackedTool(agent.Tools, loadImageTool, loadImageFactory)
			if toolAdaptationMayUseCodexCompatibleTools(
				cfg.Tools.Adaptation,
				agent.ToolAdaptation,
			) {
				viewImageTool := tools.NewCodexViewImageTool(loadImageTool)
				viewImageFactory := mustToolFactoryFromPrototype(
					viewImageTool,
					mustBaseToolFactoryTraits("view_image"),
					func(ctx tools.ToolBuildContext) (tools.Tool, error) {
						loader, err := ctx.Resolve("load_image")
						if err != nil {
							return nil, err
						}
						return tools.NewCodexViewImageTool(loader), nil
					},
				)
				mustRegisterFactoryBackedTool(agent.Tools, viewImageTool, viewImageFactory)
			}
		}

		// Skill discovery and installation tools
		skills_enabled := cfg.Tools.IsToolEnabled("skills")
		find_skills_enable := cfg.Tools.IsToolEnabled("find_skills")
		install_skills_enable := cfg.Tools.IsToolEnabled("install_skill")
		var registryMgr *skills.RegistryManager
		if skills_enabled && (find_skills_enable || install_skills_enable) {
			registryMgr = skills.NewRegistryManagerFromToolsConfig(cfg.Tools.Skills)

			if find_skills_enable {
				cacheMaxSize := cfg.Tools.Skills.SearchCache.MaxSize
				cacheTTL := time.Duration(cfg.Tools.Skills.SearchCache.TTLSeconds) * time.Second
				buildFindSkills := func() tools.Tool {
					searchCache := skills.NewSearchCache(cacheMaxSize, cacheTTL)
					return tools.NewFindSkillsTool(registryMgr, searchCache)
				}
				live := buildFindSkills()
				mustRegisterFactoryBackedTool(agent.Tools, live, mustToolFactoryFromPrototype(
					live,
					mustBaseToolFactoryTraits("find_skills"),
					func(tools.ToolBuildContext) (tools.Tool, error) {
						return buildFindSkills(), nil
					},
				))
			}
		}

		spawnEnabled := cfg.Tools.IsToolEnabled("spawn")
		spawnStatusEnabled := cfg.Tools.IsToolEnabled("spawn_status")
		if (spawnEnabled || spawnStatusEnabled) && !cfg.Tools.IsToolEnabled("subagent") {
			logger.WarnSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentSpawnSpawnStatusToolsRequireSubagentToBeEnabled,
				logger.NewSafeFields(),
			)
		}
		var installLock *sync.Mutex
		if install_skills_enable && skills_enabled {
			var err error
			installLock, err = installLocks.lockFor(agent.Workspace)
			if err != nil {
				return fmt.Errorf("resolve install-skill lock for agent %q: %w", agentID, err)
			}
		}
		candidate, err := prepareRecursionCatalogCandidate(
			al,
			cfg,
			registry,
			provider,
			agent,
			agentID,
			registryMgr,
			installLock,
			dependencies,
		)
		if err != nil {
			return err
		}
		recursionCandidates = append(recursionCandidates, candidate)
	}
	if err := installRecursionCatalog(recursionCandidates, dependencies.install); err != nil {
		return err
	}
	for _, agentID := range agentIDs {
		agent, ok := registry.GetAgent(agentID)
		if ok && agent != nil {
			warnOnUnknownAgentToolDeclarations(agentID, agent.Workspace, agent.Definition, agent.Tools)
		}
	}
	return nil
}

func shouldRegisterThreadsTool(agent, defaultAgent *AgentInstance) bool {
	if agent == nil {
		return false
	}
	if agent == defaultAgent {
		return true
	}
	return agentExplicitlyDeclaresTool(agent, tools.ThreadsToolName)
}

func agentExplicitlyDeclaresTool(agent *AgentInstance, name string) bool {
	if agent == nil ||
		frontmatterParseFailed(agent.Definition) ||
		agent.Definition.Agent == nil ||
		!frontmatterDeclaresField(agent.Definition, "tools") {
		return false
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	for _, raw := range agent.Definition.Agent.Frontmatter.Tools {
		if strings.ToLower(strings.TrimSpace(raw)) == name {
			return true
		}
	}
	return false
}
