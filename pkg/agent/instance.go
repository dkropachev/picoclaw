package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/accountrouter"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/memory"
	"github.com/sipeed/picoclaw/pkg/modelrouter"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/tools"
)

// AgentInstance represents a fully configured agent with its own workspace,
// session manager, context builder, and tool registry.
type AgentInstance struct {
	ID                        string
	Name                      string
	AccountRef                string
	Model                     string
	Fallbacks                 []string
	Workspace                 string
	MaxIterations             int
	MaxTokens                 int
	Temperature               float64
	ThinkingLevel             ThinkingLevel
	ThinkingLevelConfigured   bool
	ContextWindow             int
	SummarizeMessageThreshold int
	SummarizeTokenPercent     int
	Provider                  providers.LLMProvider
	Sessions                  session.SessionStore
	ContextBuilder            *ContextBuilder
	Tools                     *tools.ToolRegistry
	Definition                AgentContextDefinition
	Subagents                 *config.SubagentsConfig
	SkillsFilter              []string
	MCPServerAllowlist        map[string]struct{}
	Candidates                []providers.FallbackCandidate
	ImageCandidates           []providers.FallbackCandidate
	ToolAdaptation            tools.ToolAdaptationDecision

	// Router is non-nil when model routing is configured and the light model
	// was successfully resolved. It scores each incoming message and decides
	// whether to route to LightCandidates or stay with Candidates.
	Router *routing.Router
	// LightCandidates holds the resolved provider candidates for the light model.
	// Pre-computed at agent creation to avoid repeated model_list lookups at runtime.
	LightCandidates []providers.FallbackCandidate
	// LightProvider is the concrete provider instance for the configured light model.
	// It is only used when routing selects the light tier for a turn.
	LightProvider providers.LLMProvider
	// CandidateProviders maps stable candidate identity keys and provider/model
	// keys to per-candidate LLMProvider instances. Stable identities let account
	// routers keep separate credentials even when accounts use the same provider
	// and model.
	CandidateProviders map[string]providers.LLMProvider
	AccountRouter      *accountrouter.Router
	ImageAccountRouter *accountrouter.Router
	LightAccountRouter *accountrouter.Router
	ModelRouter        *modelrouter.Router
	ConfigurationError error
	executionPolicy    isolation.ExecutionPolicy

	managedCalibrationCache map[string]workflowManagedCalibrationCacheEntry
}

type agentInstanceConstructionGuard struct {
	partial AgentInstance
}

func (guard *agentInstanceConstructionGuard) cleanupPanic() {
	recovered := recover()
	if recovered == nil {
		return
	}
	if closeErr := guard.partial.Close(); closeErr != nil {
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentFailedToClosePartiallyConstructedAgent,
			logger.NewSafeFields(
				agentDiagnosticErrorField(logger.ErrorClassInternal, closeErr),
			),
		)
	}
	panic(recovered)
}

var (
	agentCandidateProvidersMu      sync.RWMutex
	agentManagedCalibrationCacheMu sync.Mutex
)

func (a *AgentInstance) candidateProvider(key string) providers.LLMProvider {
	if a == nil || strings.TrimSpace(key) == "" {
		return nil
	}
	agentCandidateProvidersMu.RLock()
	defer agentCandidateProvidersMu.RUnlock()
	if a.CandidateProviders == nil {
		return nil
	}
	return a.CandidateProviders[key]
}

func (a *AgentInstance) candidateProviderForCandidate(candidate providers.FallbackCandidate) providers.LLMProvider {
	for _, key := range candidateProviderKeys(candidate) {
		if provider := a.candidateProvider(key); provider != nil {
			return provider
		}
	}
	return nil
}

func (a *AgentInstance) setCandidateProviderIfAbsent(key string, provider providers.LLMProvider) bool {
	if a == nil || strings.TrimSpace(key) == "" || provider == nil {
		return false
	}
	agentCandidateProvidersMu.Lock()
	defer agentCandidateProvidersMu.Unlock()
	if a.CandidateProviders == nil {
		a.CandidateProviders = make(map[string]providers.LLMProvider)
	}
	if a.CandidateProviders[key] != nil {
		return false
	}
	a.CandidateProviders[key] = provider
	return true
}

func candidateProviderKeys(candidate providers.FallbackCandidate) []string {
	keys := make([]string, 0, 3)
	stableKey := strings.TrimSpace(candidate.IdentityKey)
	modelKey := ""
	if strings.TrimSpace(candidate.Provider) != "" && strings.TrimSpace(candidate.Model) != "" {
		modelKey = providers.ModelKey(candidate.Provider, candidate.Model)
	}
	if stableKey != "" && modelKey != "" && stableKey != modelKey {
		keys = append(keys, stableKey+"\x00"+modelKey)
	}
	if stableKey != "" {
		keys = append(keys, stableKey)
	}
	if modelKey != "" && !containsProviderKey(keys, modelKey) {
		keys = append(keys, modelKey)
	}
	return keys
}

func containsProviderKey(keys []string, key string) bool {
	for _, existing := range keys {
		if existing == key {
			return true
		}
	}
	return false
}

func registerCandidateProvider(
	out map[string]providers.LLMProvider,
	candidate providers.FallbackCandidate,
	provider providers.LLMProvider,
) bool {
	if out == nil || provider == nil {
		return false
	}
	agentCandidateProvidersMu.Lock()
	defer agentCandidateProvidersMu.Unlock()

	inserted := false
	for _, key := range candidateProviderKeys(candidate) {
		if key == "" || out[key] != nil {
			continue
		}
		out[key] = provider
		inserted = true
	}
	return inserted
}

func bindBootstrapProvider(
	out map[string]providers.LLMProvider,
	candidate providers.FallbackCandidate,
	bootstrap providers.LLMProvider,
) {
	if out == nil || bootstrap == nil {
		return
	}
	var replaced []providers.LLMProvider
	agentCandidateProvidersMu.Lock()
	for _, key := range candidateProviderKeys(candidate) {
		if key == "" {
			continue
		}
		if existing := out[key]; existing != nil && existing != bootstrap {
			replaced = append(replaced, existing)
		}
		out[key] = bootstrap
	}
	remaining := make(map[providers.LLMProvider]bool, len(out))
	for _, existing := range out {
		remaining[existing] = true
	}
	agentCandidateProvidersMu.Unlock()

	closed := make(map[providers.LLMProvider]bool, len(replaced))
	for _, existing := range replaced {
		if existing == nil || remaining[existing] || closed[existing] {
			continue
		}
		closed[existing] = true
		closeProviderIfStateful(existing)
	}
}

// NewAgentInstance creates an agent instance from config.
func NewAgentInstance(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	cfg *config.Config,
	provider providers.LLMProvider,
) *AgentInstance {
	isolationCfg := config.DefaultConfig().Isolation
	if cfg != nil {
		isolationCfg = cfg.Isolation
	}
	return NewAgentInstanceWithRuntimePolicies(
		agentCfg,
		defaults,
		cfg,
		provider,
		isolation.NewExecutionPolicy(isolationCfg),
		logger.DiagnosticPolicy{},
	)
}

// NewAgentInstanceWithExecutionPolicy constructs every process-capable
// dependency from one exact immutable runtime-generation policy.
func NewAgentInstanceWithExecutionPolicy(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	cfg *config.Config,
	provider providers.LLMProvider,
	policy isolation.ExecutionPolicy,
) *AgentInstance {
	return NewAgentInstanceWithRuntimePolicies(
		agentCfg,
		defaults,
		cfg,
		provider,
		policy,
		logger.DiagnosticPolicy{},
	)
}

// NewAgentInstanceWithRuntimePolicies constructs every process-capable
// dependency from one complete immutable runtime-generation policy tuple.
// The diagnostic policy is installed only as the ToolRegistry owner cap; it is
// never used directly as request authority.
func NewAgentInstanceWithRuntimePolicies(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	cfg *config.Config,
	provider providers.LLMProvider,
	executionPolicy isolation.ExecutionPolicy,
	diagnosticPolicy logger.DiagnosticPolicy,
) *AgentInstance {
	return newAgentInstanceWithRuntimePolicies(
		agentCfg,
		defaults,
		cfg,
		provider,
		executionPolicy,
		diagnosticPolicy,
		nil,
		mustAgentRuntimeFileMutationProtectedRoots(""),
	)
}

func newAgentInstanceWithRuntimePolicies(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	cfg *config.Config,
	provider providers.LLMProvider,
	executionPolicy isolation.ExecutionPolicy,
	diagnosticPolicy logger.DiagnosticPolicy,
	initialCandidateProviders map[string]providers.LLMProvider,
	fileMutationProtectedRoots []string,
) *AgentInstance {
	construction := &agentInstanceConstructionGuard{}
	defer construction.cleanupPanic()

	workspace := resolveAgentWorkspace(agentCfg, defaults)
	os.MkdirAll(workspace, 0o755)

	definition := loadAgentDefinition(workspace)

	accountRef := resolveAgentAccountRef(agentCfg, defaults)
	model := resolveAgentModel(agentCfg, defaults, definition)
	fallbacks := resolveAgentFallbacks(agentCfg, defaults)
	modelRouter := buildModelRouter(cfg, model)
	toolModelAlias := model
	if modelRouter != nil {
		toolModelAlias = firstModelRouterAlias(modelRouter)
	}
	toolProvider, toolModel := resolveToolAdaptationProfileForAlias(
		cfg,
		accountRef,
		toolModelAlias,
		workspace,
	)
	toolAdaptation := tools.ResolveToolAdaptation(cfg.Tools.Adaptation, toolProvider, toolModel)
	mayUseCodexCompatibleTools := toolAdaptationMayUseCodexCompatibleTools(
		cfg.Tools.Adaptation,
		toolAdaptation,
	)
	logger.DebugSafeCF(
		logger.ComponentAgent,
		logger.DiagnosticMessageAgentResolvedToolAdaptationProfile,
		logger.NewSafeFields(
			agentDiagnosticModelField(toolModel),
			agentDiagnosticProviderField(toolProvider),
			agentDiagnosticToolSurfaceField(toolAdaptation.VisibleToolSurface),
			agentDiagnosticReasonField(toolAdaptation.ApplyVisibleChanges),
			logger.SafeBool(logger.FieldEnabled, toolAdaptation.Enabled),
			logger.SafeBool(logger.FieldCacheSensitive, toolAdaptation.CacheSensitive),
		),
	)

	restrict := defaults.RestrictToWorkspace
	readRestrict := restrict && !defaults.AllowReadOutsideWorkspace

	// Compile path whitelist patterns from config.
	allowReadPaths := buildAllowReadPatterns(cfg)
	allowWritePaths := compilePatterns(cfg.Tools.AllowWritePaths)
	agentToolAllowlist := resolveAgentToolAllowlist(definition)
	agentMCPServerAllowlist := resolveAgentMCPServerAllowlist(definition)

	toolsRegistry := tools.NewToolRegistryWithDiagnosticPolicy(diagnosticPolicy)
	construction.partial.Tools = toolsRegistry
	toolsRegistry.SetAllowlist(agentToolAllowlist)
	readPathPatterns := cloneToolPathPatterns(allowReadPaths)
	writePathPatterns := cloneToolPathPatterns(allowWritePaths)
	fileMutationProtectedRoots = append(
		cloneAgentRuntimeFileMutationProtectedRoots(fileMutationProtectedRoots),
		mustAgentWorkspaceFileMutationProtectedRoots(workspace)...,
	)
	fileMutationProtectedRoots = append(
		fileMutationProtectedRoots,
		mustAgentWorkspaceAccountRouterProtectedRoots(workspace)...,
	)
	fileMutationPolicy := tools.FileMutationPolicy{
		ProtectedRoots: cloneAgentRuntimeFileMutationProtectedRoots(
			fileMutationProtectedRoots,
		),
	}
	applyPatchCandidate := mayUseCodexCompatibleTools &&
		(cfg.Tools.IsToolEnabled("edit_file") || cfg.Tools.IsToolEnabled("write_file"))
	applyPatchTransactionRoot := ""
	applyPatchAdmission := false
	if applyPatchCandidate {
		var applyPatchRootErr error
		applyPatchTransactionRoot, applyPatchRootErr = agentApplyPatchTransactionStateRoot()
		if applyPatchRootErr != nil {
			panic(fmt.Sprintf("build apply_patch policy: %v", applyPatchRootErr))
		}
		applyPatchAdmission = agentApplyPatchAdmissionSafe(
			defaults,
			cfg,
			applyPatchTransactionRoot,
			readPathPatterns,
			writePathPatterns,
		)
		if agentApplyPatchTransactionRootOverlapsWorkspace(
			applyPatchTransactionRoot,
			workspace,
		) {
			applyPatchAdmission = false
		}
	}

	if cfg.Tools.IsToolEnabled("read_file") {
		maxReadFileSize := cfg.Tools.ReadFile.MaxReadFileSize
		var buildReadFile func() tools.Tool
		switch cfg.Tools.ReadFile.EffectiveMode() {
		case config.ReadFileModeLines:
			buildReadFile = func() tools.Tool {
				return tools.NewReadFileLinesTool(
					workspace,
					readRestrict,
					maxReadFileSize,
					cloneToolPathPatterns(readPathPatterns),
				)
			}
		default:
			buildReadFile = func() tools.Tool {
				return tools.NewReadFileBytesTool(
					workspace,
					readRestrict,
					maxReadFileSize,
					cloneToolPathPatterns(readPathPatterns),
				)
			}
		}
		live := buildReadFile()
		mustRegisterFactoryBackedTool(toolsRegistry, live, mustToolFactoryFromPrototype(
			live,
			mustBaseToolFactoryTraits("read_file"),
			func(tools.ToolBuildContext) (tools.Tool, error) { return buildReadFile(), nil },
		))
	}
	if cfg.Tools.IsToolEnabled("edit_file") {
		buildEditFile := func() (*tools.EditFileTool, error) {
			return tools.NewEditFileToolWithPolicy(
				workspace,
				restrict,
				tools.FileMutationPolicy{ProtectedRoots: append(
					[]string(nil), fileMutationPolicy.ProtectedRoots...,
				)},
				cloneToolPathPatterns(writePathPatterns),
			)
		}
		live, err := buildEditFile()
		if err != nil {
			panic(fmt.Sprintf("build edit_file policy: %v", err))
		}
		mustRegisterFactoryBackedTool(toolsRegistry, live, mustToolFactoryFromPrototype(
			live,
			mustBaseToolFactoryTraits("edit_file"),
			func(tools.ToolBuildContext) (tools.Tool, error) {
				return buildEditFile()
			},
		))
	}
	if cfg.Tools.IsToolEnabled("append_file") {
		buildAppendFile := func() (*tools.AppendFileTool, error) {
			return tools.NewAppendFileToolWithPolicy(
				workspace,
				restrict,
				tools.FileMutationPolicy{ProtectedRoots: append(
					[]string(nil), fileMutationPolicy.ProtectedRoots...,
				)},
				cloneToolPathPatterns(writePathPatterns),
			)
		}
		live, err := buildAppendFile()
		if err != nil {
			panic(fmt.Sprintf("build append_file policy: %v", err))
		}
		mustRegisterFactoryBackedTool(toolsRegistry, live, mustToolFactoryFromPrototype(
			live,
			mustBaseToolFactoryTraits("append_file"),
			func(tools.ToolBuildContext) (tools.Tool, error) {
				return buildAppendFile()
			},
		))
	}
	// Build write_file's copy from the registered editors so it steers the agent
	// to edit_file/append_file only when those tools are actually available.
	if cfg.Tools.IsToolEnabled("write_file") {
		var altTools []string
		if toolsRegistry.HasRegistered("append_file") {
			altTools = append(altTools, "append_file")
		}
		if toolsRegistry.HasRegistered("edit_file") {
			altTools = append(altTools, "edit_file")
		}
		buildWriteFile := func() (*tools.WriteFileTool, error) {
			writeTool, err := tools.NewWriteFileToolWithPolicy(
				workspace,
				restrict,
				tools.FileMutationPolicy{ProtectedRoots: append(
					[]string(nil), fileMutationPolicy.ProtectedRoots...,
				)},
				cloneToolPathPatterns(writePathPatterns),
			)
			if err != nil {
				return nil, err
			}
			writeTool.SetAlternativeTools(append([]string(nil), altTools...))
			return writeTool, nil
		}
		live, err := buildWriteFile()
		if err != nil {
			panic(fmt.Sprintf("build write_file policy: %v", err))
		}
		mustRegisterFactoryBackedTool(toolsRegistry, live, mustToolFactoryFromPrototype(
			live,
			mustBaseToolFactoryTraits("write_file"),
			func(tools.ToolBuildContext) (tools.Tool, error) {
				return buildWriteFile()
			},
		))
	}
	if applyPatchCandidate && applyPatchAdmission {
		allowCreate := cfg.Tools.IsToolEnabled("write_file")
		allowUpdate := cfg.Tools.IsToolEnabled("edit_file")
		applyPatchProtectedRoots := agentApplyPatchProtectedRoots(
			workspace,
			cfg,
		)
		buildApplyPatch := func() tools.Tool {
			tool, err := tools.NewApplyPatchToolWithPermissionsAndPolicy(
				workspace,
				restrict,
				allowCreate,
				allowUpdate,
				tools.ApplyPatchPreflightPolicy{
					ProtectedRoots: append(
						[]string(nil), applyPatchProtectedRoots...,
					),
					VolatileProtectedRoots: append(
						[]string(nil), fileMutationProtectedRoots...,
					),
					TransactionStateRoot: applyPatchTransactionRoot,
				},
				cloneToolPathPatterns(writePathPatterns),
			)
			if err != nil {
				panic(fmt.Sprintf("build apply_patch policy: %v", err))
			}
			return tool
		}
		live := buildApplyPatch()
		mustRegisterFactoryBackedTool(toolsRegistry, live, mustToolFactoryFromPrototype(
			live,
			mustBaseToolFactoryTraits("apply_patch"),
			func(tools.ToolBuildContext) (tools.Tool, error) { return buildApplyPatch(), nil },
		))
	}
	if cfg.Tools.IsToolEnabled("list_dir") {
		buildListDir := func() tools.Tool {
			return tools.NewListDirTool(
				workspace,
				readRestrict,
				cloneToolPathPatterns(readPathPatterns),
			)
		}
		live := buildListDir()
		mustRegisterFactoryBackedTool(toolsRegistry, live, mustToolFactoryFromPrototype(
			live,
			mustBaseToolFactoryTraits("list_dir"),
			func(tools.ToolBuildContext) (tools.Tool, error) { return buildListDir(), nil },
		))
	}
	if cfg.Tools.IsToolEnabled("exec") {
		execTool, err := tools.NewExecToolWithConfigAndExecutionPolicy(
			workspace,
			restrict,
			cfg,
			executionPolicy,
			allowReadPaths,
		)
		if err != nil {
			logger.ErrorSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentFailedToInitializeExecToolContinuingWithoutExec,
				logger.NewSafeFields(
					agentDiagnosticErrorField(logger.ErrorClassInternal, err),
					logger.SafeBool(logger.FieldFallback, true),
				),
			)
		} else {
			toolsRegistry.Register(execTool)
			if mayUseCodexCompatibleTools {
				toolsRegistry.Register(tools.NewCodexExecCommandTool(execTool))
				toolsRegistry.Register(tools.NewCodexWriteStdinTool(execTool))
			}
		}
	}
	if mayUseCodexCompatibleTools {
		mustRegisterFactoryBackedTool(
			toolsRegistry,
			tools.NewUpdatePlanTool(),
			tools.NewUpdatePlanToolFactory(),
		)
	}

	sessionsDir := filepath.Join(workspace, "sessions")
	sessions := initSessionStore(sessionsDir)
	construction.partial.Sessions = sessions

	contextBuilder := NewContextBuilder(workspace).
		WithSplitOnMarker(cfg.Agents.Defaults.SplitOnMarker)

	agentID := routing.DefaultAgentID
	agentName := ""
	var subagents *config.SubagentsConfig
	var skillsFilter []string

	if agentCfg != nil {
		agentID = routing.NormalizeAgentID(agentCfg.ID)
		agentName = agentCfg.Name
		if definition.Agent != nil && strings.TrimSpace(definition.Agent.Frontmatter.Name) != "" {
			agentName = strings.TrimSpace(definition.Agent.Frontmatter.Name)
		}
		subagents = agentCfg.Subagents
		skillsFilter = resolveAgentSkillsFilter(agentCfg, definition)
	}
	warnOnUnknownAgentMCPServerDeclarations(agentID, workspace, cfg, definition)

	maxIter := defaults.MaxToolIterations
	if maxIter == 0 {
		maxIter = 20
	}

	maxTokens := defaults.MaxTokens
	if maxTokens == 0 {
		maxTokens = 8192
	}

	contextWindow := defaults.ContextWindow
	if contextWindow == 0 {
		// Default heuristic: 4x the output token limit.
		// Most models have context windows well above their output limits
		// (e.g., GPT-4o 128k ctx / 16k out, Claude 200k ctx / 8k out).
		// 4x is a conservative lower bound that avoids premature
		// summarization while remaining safe — the reactive
		// forceCompression handles any overshoot.
		contextWindow = maxTokens * 4
	}

	temperature := 0.7
	if defaults.Temperature != nil {
		temperature = *defaults.Temperature
	}

	var thinkingLevelStr string
	if mc, err := concreteAccountModelConfig(
		cfg,
		firstConcreteAccountRef(cfg, accountRef),
		model,
		workspace,
	); err == nil {
		thinkingLevelStr = mc.ThinkingLevel
	}
	thinkingLevel := parseThinkingLevel(thinkingLevelStr)
	thinkingLevelConfigured := isConfiguredThinkingLevel(thinkingLevelStr)

	summarizeMessageThreshold := defaults.SummarizeMessageThreshold
	if summarizeMessageThreshold == 0 {
		summarizeMessageThreshold = 20
	}

	summarizeTokenPercent := defaults.SummarizeTokenPercent
	if summarizeTokenPercent == 0 {
		summarizeTokenPercent = 75
	}

	candidateProviders := make(
		map[string]providers.LLMProvider,
		len(initialCandidateProviders),
	)
	for key, candidateProvider := range initialCandidateProviders {
		candidateProviders[key] = candidateProvider
	}
	var configurationErr error
	if strings.TrimSpace(model) == "" {
		configurationErr = config.ErrNoModelConfigured
	} else if strings.TrimSpace(accountRef) == "" {
		configurationErr = fmt.Errorf("no account configured")
	} else if modelRouter != nil {
		configurationErr = validateModelRouterAliases(cfg, modelRouter)
	} else {
		configurationErr = validateModelAliasReferences(cfg, model, fallbacks)
	}

	var accountRouter *accountrouter.Router
	var candidates []providers.FallbackCandidate
	if configurationErr == nil && modelRouter == nil {
		accountRouter = buildAccountRouterWithAliases(
			cfg,
			accountRef,
			model,
			fallbacks,
			workspace,
			candidateProviders,
			executionPolicy,
		)
		if accountRouter != nil {
			initialSelection := accountRouter.Select("", accountrouter.SelectReasonInitial)
			candidates = initialSelection.Candidates
			if len(candidates) == 0 {
				configurationErr = fmt.Errorf(
					"account router %q has no runnable model alias %q",
					accountRef,
					model,
				)
			}
		} else {
			var err error
			candidates, err = candidatesForAccountAliases(
				cfg,
				accountRef,
				model,
				fallbacks,
				workspace,
				candidateProviders,
				executionPolicy,
			)
			if err != nil {
				configurationErr = err
			}
		}
	}
	if len(candidates) > 0 &&
		bootstrapProviderMatchesDefaultSelection(
			cfg,
			defaults,
			accountRef,
			model,
			candidates[0],
		) {
		bindBootstrapProvider(candidateProviders, candidates[0], provider)
	}

	var imageCandidates []providers.FallbackCandidate
	var imageAccountRouter *accountrouter.Router
	if strings.TrimSpace(defaults.ImageModel) != "" {
		imageAccountRouter = buildAccountRouterWithAliases(
			cfg,
			accountRef,
			defaults.ImageModel,
			defaults.ImageModelFallbacks,
			workspace,
			candidateProviders,
			executionPolicy,
		)
		var imageErr error
		if imageAccountRouter != nil {
			imageCandidates = imageAccountRouter.Select(
				"",
				accountrouter.SelectReasonInitial,
			).Candidates
		} else {
			imageCandidates, imageErr = candidatesForAccountAliases(
				cfg,
				accountRef,
				defaults.ImageModel,
				defaults.ImageModelFallbacks,
				workspace,
				candidateProviders,
				executionPolicy,
			)
		}
		if imageErr != nil && configurationErr == nil {
			configurationErr = fmt.Errorf("image model alias: %w", imageErr)
		} else if len(imageCandidates) == 0 && configurationErr == nil {
			configurationErr = fmt.Errorf(
				"image model alias %q has no runnable provider",
				defaults.ImageModel,
			)
		}
	}
	if len(candidates) > 0 {
		if selectedProvider := providerFromCandidateMap(candidateProviders, candidates[0]); selectedProvider != nil {
			provider = selectedProvider
		}
	}

	// Model routing setup: pre-resolve light model candidates at creation time
	// to avoid repeated model_list lookups on every incoming message.
	var router *routing.Router
	var lightCandidates []providers.FallbackCandidate
	var lightProvider providers.LLMProvider
	var lightAccountRouter *accountrouter.Router
	routingConfig := defaults.Routing
	if routingConfig != nil && routingConfig.Enabled && routingConfig.LightModel != "" {
		if err := validateModelAliasReferences(cfg, routingConfig.LightModel, nil); err != nil {
			if configurationErr == nil {
				configurationErr = fmt.Errorf("light model alias: %w", err)
			}
			routingConfig = nil
		}
	}
	if rc := routingConfig; rc != nil && rc.Enabled && rc.LightModel != "" {
		lightAccountRouter = buildAccountRouterWithAliases(
			cfg,
			accountRef,
			rc.LightModel,
			nil,
			workspace,
			candidateProviders,
			executionPolicy,
		)
		if lightAccountRouter != nil {
			selection := lightAccountRouter.Select("", accountrouter.SelectReasonInitial)
			lightCandidates = selection.Candidates
		} else {
			lightCandidates, _ = candidatesForAccountAliases(
				cfg,
				accountRef,
				rc.LightModel,
				nil,
				workspace,
				candidateProviders,
				executionPolicy,
			)
		}
		if len(lightCandidates) == 0 {
			logger.WarnSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentRoutingLightModelNotFoundRoutingDisabled,
				logger.NewSafeFields(
					agentDiagnosticLightModelField(rc.LightModel),
					agentDiagnosticAgentField(agentID),
					logger.SafeBool(logger.FieldAvailable, false),
				),
			)
		} else {
			router = routing.New(routing.RouterConfig{
				LightModel: rc.LightModel,
				Threshold:  rc.Threshold,
			})
			lightProvider = providerFromCandidateMap(
				candidateProviders,
				lightCandidates[0],
			)
		}
	}

	return &AgentInstance{
		ID:                        agentID,
		Name:                      agentName,
		AccountRef:                accountRef,
		Model:                     model,
		Fallbacks:                 fallbacks,
		ToolAdaptation:            toolAdaptation,
		Workspace:                 workspace,
		MaxIterations:             maxIter,
		MaxTokens:                 maxTokens,
		Temperature:               temperature,
		ThinkingLevel:             thinkingLevel,
		ThinkingLevelConfigured:   thinkingLevelConfigured,
		ContextWindow:             contextWindow,
		SummarizeMessageThreshold: summarizeMessageThreshold,
		SummarizeTokenPercent:     summarizeTokenPercent,
		Provider:                  provider,
		Sessions:                  sessions,
		ContextBuilder:            contextBuilder,
		Tools:                     toolsRegistry,
		Definition:                definition,
		Subagents:                 subagents,
		SkillsFilter:              skillsFilter,
		MCPServerAllowlist:        agentMCPServerAllowlist,
		Candidates:                candidates,
		ImageCandidates:           imageCandidates,
		Router:                    router,
		LightCandidates:           lightCandidates,
		LightProvider:             lightProvider,
		CandidateProviders:        candidateProviders,
		AccountRouter:             accountRouter,
		ImageAccountRouter:        imageAccountRouter,
		LightAccountRouter:        lightAccountRouter,
		ModelRouter:               modelRouter,
		ConfigurationError:        configurationErr,
		executionPolicy:           executionPolicy,
		managedCalibrationCache:   make(map[string]workflowManagedCalibrationCacheEntry),
	}
}

func bootstrapProviderMatchesDefaultSelection(
	cfg *config.Config,
	defaults *config.AgentDefaults,
	accountRef string,
	modelSelector string,
	candidate providers.FallbackCandidate,
) bool {
	if defaults == nil ||
		strings.TrimSpace(accountRef) != strings.TrimSpace(defaults.AccountRef) {
		return false
	}
	// Router bootstrap selects a representative terminal before the runtime
	// router makes its actual choice. Reusing it could bind the wrong concrete
	// account or alias, so routed selections always use their own providers.
	if lookupAccountRouterConfig(cfg, accountRef) != nil {
		return false
	}
	if buildModelRouter(cfg, modelSelector) != nil ||
		buildModelRouter(cfg, defaults.ModelName) != nil {
		return false
	}
	defaultConfig, err := concreteAccountModelConfig(
		cfg,
		defaults.AccountRef,
		defaults.ModelName,
		defaults.Workspace,
	)
	if err != nil {
		return false
	}
	defaultCandidate, ok := candidateFromModelConfig("", defaultConfig)
	if !ok {
		return false
	}
	return providers.NormalizeProvider(defaultCandidate.Provider) ==
		providers.NormalizeProvider(candidate.Provider) &&
		strings.TrimSpace(defaultCandidate.Model) == strings.TrimSpace(candidate.Model)
}

func buildModelRouter(cfg *config.Config, model string) *modelrouter.Router {
	if cfg == nil {
		return nil
	}
	for i := range cfg.ModelRouters {
		if cfg.ModelRouters[i].Enabled &&
			strings.TrimSpace(cfg.ModelRouters[i].Name) == strings.TrimSpace(model) {
			return modelrouter.New(cfg.ModelRouters[i].Name, &cfg.ModelRouters[i])
		}
	}
	return nil
}

func firstModelRouterAlias(router *modelrouter.Router) string {
	if router == nil {
		return ""
	}
	for _, block := range router.Config.Blocks {
		if strings.TrimSpace(block.Type) == config.ModelRouterBlockTypeModel {
			return strings.TrimSpace(block.Model)
		}
	}
	return ""
}

func validateModelRouterAliases(cfg *config.Config, router *modelrouter.Router) error {
	if router == nil {
		return nil
	}
	found := false
	for _, block := range router.Config.Blocks {
		if strings.TrimSpace(block.Type) != config.ModelRouterBlockTypeModel {
			continue
		}
		found = true
		alias := strings.TrimSpace(block.Model)
		if _, err := cfg.GetModelAlias(alias); err != nil {
			return fmt.Errorf(
				"model router %q block %q: %w",
				router.Name,
				block.ID,
				err,
			)
		}
	}
	if !found {
		return fmt.Errorf("model router %q has no model alias targets", router.Name)
	}
	return nil
}

func providerFromCandidateMap(
	providerMap map[string]providers.LLMProvider,
	candidate providers.FallbackCandidate,
) providers.LLMProvider {
	for _, key := range candidateProviderKeys(candidate) {
		if providerMap[key] != nil {
			return providerMap[key]
		}
	}
	return nil
}

func firstConcreteAccountRef(cfg *config.Config, accountRef string) string {
	accountRef = strings.TrimSpace(accountRef)
	if router := lookupAccountRouterConfig(cfg, accountRef); router != nil {
		accounts := accountRouterAccountNames(router)
		if len(accounts) > 0 {
			return accounts[0]
		}
	}
	return accountRef
}

func resolveToolAdaptationProfileForAlias(
	cfg *config.Config,
	accountRef string,
	modelAlias string,
	workspace string,
) (string, string) {
	modelCfg, err := concreteAccountModelConfig(
		cfg,
		firstConcreteAccountRef(cfg, accountRef),
		modelAlias,
		workspace,
	)
	if err != nil {
		return "", strings.TrimSpace(modelAlias)
	}
	provider, model := providers.ExtractProtocol(modelCfg)
	return providers.NormalizeProvider(provider), strings.TrimSpace(model)
}

func accountRouterCredentialAccountConfig(accountName string, modelID string) (*config.ModelConfig, bool) {
	credentialID, ok := config.AccountRouterCredentialAccountID(accountName)
	if !ok {
		return nil, false
	}
	provider, ok := config.AccountRouterCredentialAccountProvider(accountName)
	if !ok {
		return nil, true
	}
	provider = credentialRuntimeProvider(provider)
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil, true
	}
	return &config.ModelConfig{
		ModelName:    strings.TrimSpace(accountName),
		Provider:     provider,
		Model:        modelID,
		AuthMethod:   credentialRuntimeAuthMethod(provider),
		CredentialID: credentialID,
		Enabled:      true,
	}, true
}

func credentialRuntimeProvider(provider string) string {
	switch strings.TrimSpace(provider) {
	case "google-antigravity":
		return "antigravity"
	case "copilot":
		return "github-copilot"
	default:
		return strings.TrimSpace(provider)
	}
}

func credentialRuntimeAuthMethod(provider string) string {
	switch strings.TrimSpace(provider) {
	case "openai", "antigravity":
		return "oauth"
	case "anthropic", "github-copilot":
		return "token"
	default:
		return "token"
	}
}

func accountRouterAccountNames(routerCfg *config.AccountRouterConfig) []string {
	if routerCfg == nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0)
	add := func(account string) {
		account = strings.TrimSpace(account)
		if account == "" || seen[account] {
			return
		}
		seen[account] = true
		out = append(out, account)
	}
	for _, block := range routerCfg.Blocks {
		switch strings.TrimSpace(block.Type) {
		case config.AccountRouterBlockTypeAccount:
			add(block.Account)
		case config.AccountRouterBlockTypeLoadBalance:
			for _, account := range block.Accounts {
				add(account)
			}
		}
	}
	return out
}

func toolAdaptationMayUseCodexCompatibleTools(
	cfg config.ToolAdaptationConfig,
	initial tools.ToolAdaptationDecision,
) bool {
	if initial.MayUseCodexCompatibleTools() {
		return true
	}
	for _, override := range cfg.Normalized().ProfileOverrides {
		provider := providers.NormalizeProvider(override.Provider)
		decision := tools.ResolveToolAdaptation(cfg, provider, override.Model)
		if decision.MayUseCodexCompatibleTools() {
			return true
		}
	}
	return false
}

// AgentModelMayUseCodexCompatibleTools reports whether runtime construction
// can register Codex-compatible tool identities for an already resolved model.
func AgentModelMayUseCodexCompatibleTools(
	resolvedModel string,
	defaults *config.AgentDefaults,
	cfg *config.Config,
) bool {
	if defaults == nil || cfg == nil {
		return false
	}
	modelAlias := strings.TrimSpace(resolvedModel)
	if router := buildModelRouter(cfg, modelAlias); router != nil {
		modelAlias = firstModelRouterAlias(router)
	}
	provider, toolModel := resolveToolAdaptationProfileForAlias(
		cfg,
		defaults.AccountRef,
		modelAlias,
		defaults.Workspace,
	)
	initial := tools.ResolveToolAdaptation(
		cfg.Tools.Adaptation,
		provider,
		toolModel,
	)
	return toolAdaptationMayUseCodexCompatibleTools(
		cfg.Tools.Adaptation,
		initial,
	)
}

// resolveAgentWorkspace determines the workspace directory for an agent.
func resolveAgentWorkspace(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) string {
	if agentCfg != nil && strings.TrimSpace(agentCfg.Workspace) != "" {
		return expandHome(strings.TrimSpace(agentCfg.Workspace))
	}
	// Use the configured default workspace (respects PICOCLAW_HOME)
	if agentCfg == nil || agentCfg.Default || agentCfg.ID == "" ||
		routing.NormalizeAgentID(agentCfg.ID) == "main" {
		return expandHome(defaults.Workspace)
	}
	// For named agents without explicit workspace, use default workspace with agent ID suffix
	id := routing.NormalizeAgentID(agentCfg.ID)
	return filepath.Join(expandHome(defaults.Workspace), "..", "workspace-"+id)
}

// ResolveAgentWorkspace returns the exact workspace path used when the runtime
// constructs an agent instance. Management surfaces use this wrapper so they
// never drift from runtime workspace selection.
func ResolveAgentWorkspace(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) string {
	return resolveAgentWorkspace(agentCfg, defaults)
}

// resolveAgentModel resolves the primary model for an agent.
func resolveAgentModel(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	definition AgentContextDefinition,
) string {
	definitionModel := ""
	if definition.Agent != nil {
		definitionModel = definition.Agent.Frontmatter.Model
	}
	return ResolveAgentModelFromDefinition(agentCfg, defaults, definitionModel)
}

func resolveAgentAccountRef(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
) string {
	if agentCfg != nil && strings.TrimSpace(agentCfg.AccountRef) != "" {
		return strings.TrimSpace(agentCfg.AccountRef)
	}
	if defaults == nil {
		return ""
	}
	return strings.TrimSpace(defaults.AccountRef)
}

// ResolveAgentModelFromDefinition applies runtime model precedence to a model
// value already captured from an AGENT.md definition.
func ResolveAgentModelFromDefinition(
	agentCfg *config.AgentConfig,
	defaults *config.AgentDefaults,
	definitionModel string,
) string {
	if model := strings.TrimSpace(definitionModel); model != "" {
		return model
	}
	if agentCfg != nil && agentCfg.Model != nil && strings.TrimSpace(agentCfg.Model.Primary) != "" {
		return strings.TrimSpace(agentCfg.Model.Primary)
	}
	if defaults == nil {
		return ""
	}
	return defaults.GetModelName()
}

// resolveAgentFallbacks resolves the fallback models for an agent.
func resolveAgentFallbacks(agentCfg *config.AgentConfig, defaults *config.AgentDefaults) []string {
	if agentCfg != nil && agentCfg.Model != nil && agentCfg.Model.Fallbacks != nil {
		return agentCfg.Model.Fallbacks
	}
	return defaults.ModelFallbacks
}

func resolveSubagentModelPolicy(agent *AgentInstance) (string, []string) {
	if agent == nil {
		return "", nil
	}
	primary := strings.TrimSpace(agent.Model)
	fallbacks := cloneOptionalModelFallbacks(agent.Fallbacks)
	if agent.Subagents == nil || agent.Subagents.Model == nil {
		return primary, fallbacks
	}
	if configured := strings.TrimSpace(agent.Subagents.Model.Primary); configured != "" {
		primary = configured
	}
	if agent.Subagents.Model.Fallbacks != nil {
		fallbacks = cloneOptionalModelFallbacks(agent.Subagents.Model.Fallbacks)
	}
	return primary, fallbacks
}

func resolveAgentSkillsFilter(
	agentCfg *config.AgentConfig,
	definition AgentContextDefinition,
) []string {
	if definition.Agent != nil && definition.Agent.Frontmatter.Skills != nil {
		return append([]string(nil), definition.Agent.Frontmatter.Skills...)
	}
	if agentCfg == nil || agentCfg.Skills == nil {
		return nil
	}
	return append([]string(nil), agentCfg.Skills...)
}

func (a *AgentInstance) AllowsMCPServer(serverName string) bool {
	if a == nil || a.MCPServerAllowlist == nil {
		return true
	}
	_, ok := a.MCPServerAllowlist[strings.ToLower(strings.TrimSpace(serverName))]
	return ok
}

func compilePatterns(patterns []string) []*regexp.Regexp {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			logger.WarnSafeCF(
				logger.ComponentAgent,
				logger.DiagnosticMessageAgentInvalidPathPatternInCompilePatterns,
				logger.NewSafeFields(
					agentDiagnosticRegexField(p),
					agentDiagnosticErrorField(logger.ErrorClassValidation, err),
				),
			)
			continue
		}
		compiled = append(compiled, re)
	}
	return compiled
}

func buildAllowReadPatterns(cfg *config.Config) []*regexp.Regexp {
	var configured []string
	if cfg != nil {
		configured = cfg.Tools.AllowReadPaths
	}

	compiled := compilePatterns(configured)
	mediaDirPattern := regexp.MustCompile(mediaTempDirPattern())
	for _, pattern := range compiled {
		if pattern.String() == mediaDirPattern.String() {
			return compiled
		}
	}

	return append(compiled, mediaDirPattern)
}

func mediaTempDirPattern() string {
	sep := regexp.QuoteMeta(string(os.PathSeparator))
	return "^" + regexp.QuoteMeta(filepath.Clean(media.TempDir())) + "(?:" + sep + "|$)"
}

// Close releases the quiesced agent's tool registry and session store. Both
// cleanup paths run even when one fails so a stale generation cannot retain
// factory-backed source leases.
func (a *AgentInstance) Close() error {
	if a == nil {
		return nil
	}
	var closeErrors []error
	if a.Tools != nil {
		closeErrors = append(closeErrors, closeAgentResource("tool registry", a.Tools.Close))
	}
	if a.Sessions != nil {
		closeErrors = append(closeErrors, closeAgentResource("session store", a.Sessions.Close))
	}
	return errors.Join(closeErrors...)
}

func closeAgentResource(name string, closeResource func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("close agent %s: panic: %v", name, recovered)
		}
	}()
	if closeResource == nil {
		return nil
	}
	if closeErr := closeResource(); closeErr != nil {
		return fmt.Errorf("close agent %s: %w", name, closeErr)
	}
	return nil
}

// initSessionStore creates the session persistence backend.
// It uses the JSONL store by default and auto-migrates legacy JSON sessions.
// Falls back to SessionManager if the JSONL store cannot be initialized or
// if migration fails (which indicates the store cannot write reliably).
func initSessionStore(dir string) session.SessionStore {
	store, err := memory.NewJSONLStore(dir)
	if err != nil {
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentMemoryJSONLStoreInitFailedFallingBackToJSONSessions,
			logger.NewSafeFields(
				agentDiagnosticErrorField(logger.ErrorClassInternal, err),
				logger.SafeBool(logger.FieldFallback, true),
			),
		)
		return session.NewSessionManager(dir)
	}

	if n, merr := memory.MigrateFromJSON(context.Background(), dir, store); merr != nil {
		// Migration failure means the store could not write data.
		// Fall back to SessionManager to avoid a split state where
		// some sessions are in JSONL and others remain in JSON.
		logger.WarnSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentMemoryMigrationFailedFallingBackToJSONSessions,
			logger.NewSafeFields(
				agentDiagnosticErrorField(logger.ErrorClassInternal, merr),
				logger.SafeBool(logger.FieldFallback, true),
			),
		)
		store.Close()
		return session.NewSessionManager(dir)
	} else if n > 0 {
		logger.InfoSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentMemoryMigratedToJSONL,
			logger.NewSafeFields(
				logger.SafeInt(logger.FieldCompletedCount, n),
			),
		)
	}

	return session.NewJSONLBackend(store)
}

func expandHome(path string) string {
	if path == "" {
		return path
	}
	if path[0] == '~' {
		home, _ := os.UserHomeDir()
		if len(path) > 1 && path[1] == '/' {
			return home + path[1:]
		}
		return home
	}
	return path
}
