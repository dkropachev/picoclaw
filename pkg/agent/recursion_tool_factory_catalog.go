package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/skills"
	"github.com/sipeed/picoclaw/pkg/tools"
)

func markRecursionCatalogConfigurationError(registry *AgentRegistry, err error) {
	if registry == nil || err == nil {
		return
	}
	for _, agentID := range registry.ListAgentIDs() {
		if agent, ok := registry.GetAgent(agentID); ok && agent != nil {
			agent.ConfigurationError = errors.Join(agent.ConfigurationError, err)
		}
	}
}

type recursionCatalogInstaller func(
	[]tools.FactoryBackedBatch,
) ([]tools.FactoryBackedAdmission, error)

type recursionManagerConstructor func(
	providers.LLMProvider,
	string,
	string,
) *tools.SubagentManager

type recursionCatalogDependencies struct {
	newManager recursionManagerConstructor
	install    recursionCatalogInstaller
}

func defaultRecursionCatalogDependencies() recursionCatalogDependencies {
	return recursionCatalogDependencies{
		newManager: tools.NewSubagentManager,
		install:    tools.InstallFactoryBackedTransaction,
	}
}

type recursionCatalogSpec struct {
	al             *AgentLoop
	registry       *AgentRegistry
	provider       providers.LLMProvider
	agentID        string
	workspace      string
	model          string
	fallbacks      []string
	maxTokens      int
	temperature    float64
	maxMediaSize   int
	legacyTools    *tools.ToolRegistry
	policy         tools.ToolPolicy
	managerService string
}

type recursionOwnerBundle struct {
	manager *tools.SubagentManager
	spawner *AgentLoopSpawner
}

type recursionCatalogCandidate struct {
	agentID       string
	registry      *tools.ToolRegistry
	installs      []tools.FactoryBackedInstall
	beforeVersion uint64
}

type recursionInstallSidecar struct {
	batchIndex   int
	installIndex int
	agentID      string
	name         string
	registry     *tools.ToolRegistry
	live         tools.Tool
}

type stagedRecursionCatalog struct {
	batches        []tools.FactoryBackedBatch
	sidecars       []recursionInstallSidecar
	beforeVersions []uint64
}

type workspaceInstallLockEntry struct {
	identity string
	info     os.FileInfo
	lock     *sync.Mutex
}

type workspaceInstallLockCoordinator struct {
	entries []workspaceInstallLockEntry
}

func (coordinator *workspaceInstallLockCoordinator) lockFor(
	workspace string,
) (*sync.Mutex, error) {
	if workspace == "" || workspace != strings.TrimSpace(workspace) {
		return nil, fmt.Errorf("install-skill workspace must be exact and nonempty")
	}
	identity, err := filepath.Abs(filepath.Clean(workspace))
	if err != nil {
		return nil, fmt.Errorf("make install-skill workspace absolute: %w", err)
	}
	identity = filepath.Clean(identity)
	if resolved, resolveErr := filepath.EvalSymlinks(identity); resolveErr == nil {
		identity = filepath.Clean(resolved)
	}
	info, statErr := os.Stat(identity)
	if statErr != nil && !os.IsNotExist(statErr) {
		return nil, fmt.Errorf("inspect install-skill workspace: %w", statErr)
	}
	for _, entry := range coordinator.entries {
		if entry.identity == identity ||
			info != nil && entry.info != nil && os.SameFile(info, entry.info) {
			return entry.lock, nil
		}
	}
	lock := &sync.Mutex{}
	coordinator.entries = append(coordinator.entries, workspaceInstallLockEntry{
		identity: identity,
		info:     info,
		lock:     lock,
	})
	return lock, nil
}

func prepareRecursionCatalogCandidate(
	al *AgentLoop,
	cfg *config.Config,
	registry *AgentRegistry,
	provider providers.LLMProvider,
	agent *AgentInstance,
	agentID string,
	registryManager *skills.RegistryManager,
	installLock *sync.Mutex,
	dependencies recursionCatalogDependencies,
) (recursionCatalogCandidate, error) {
	candidate := recursionCatalogCandidate{agentID: agentID}
	if al == nil || cfg == nil || registry == nil || agent == nil {
		return candidate, fmt.Errorf("recursion catalog input is incomplete")
	}
	if agentID == "" || agentID != strings.TrimSpace(agentID) || agent.ID != agentID {
		return candidate, fmt.Errorf("recursion agent identity %q is not exact", agentID)
	}
	if agent.Tools == nil {
		return candidate, fmt.Errorf("recursion agent %q tool registry is nil", agentID)
	}
	if _, owned := agent.Tools.Owner(); owned {
		return candidate, fmt.Errorf("recursion agent %q requires a compatibility registry", agentID)
	}
	if dependencies.install == nil {
		return candidate, fmt.Errorf("recursion catalog dependencies are incomplete")
	}
	candidate.registry = agent.Tools

	installSkillEnabled := cfg.Tools.IsToolEnabled("skills") &&
		cfg.Tools.IsToolEnabled("install_skill")
	var installSkillLive tools.Tool
	var installSkillFactory tools.ToolFactory
	if installSkillEnabled {
		if registryManager == nil {
			return candidate, fmt.Errorf("install-skill registry manager is nil")
		}
		if installLock == nil {
			return candidate, fmt.Errorf("install-skill workspace lock is nil")
		}
		buildInstallSkill := func() tools.Tool {
			return tools.NewInstallSkillToolWithLock(
				registryManager,
				agent.Workspace,
				installLock,
			)
		}
		installSkillLive = buildInstallSkill()
		var err error
		installSkillFactory, err = tools.NewToolFactoryFromPrototype(
			installSkillLive,
			tools.ToolTraits{
				Risk:        tools.ToolRiskExternalWrite,
				Parallel:    tools.ToolParallelSerialized,
				Idempotency: tools.ToolIdempotencyNonIdempotent,
				Sharing:     tools.ToolSharingPerOwner,
			},
			func(tools.ToolBuildContext) (tools.Tool, error) {
				return buildInstallSkill(), nil
			},
		)
		if err != nil {
			return candidate, fmt.Errorf("build install-skill factory for %q: %w", agentID, err)
		}
		candidate.installs = append(candidate.installs, tools.FactoryBackedInstall{
			Live: installSkillLive, Factory: installSkillFactory,
		})
	}

	spawnEnabled := cfg.Tools.IsToolEnabled("spawn")
	statusEnabled := cfg.Tools.IsToolEnabled("spawn_status")
	subagentEnabled := cfg.Tools.IsToolEnabled("subagent")
	bundleEnabled := (spawnEnabled || statusEnabled) && subagentEnabled
	delegateEnabled := len(registry.ListAgentIDs()) > 1
	if !bundleEnabled && !delegateEnabled {
		candidate.beforeVersion = agent.Tools.Version()
		return candidate, nil
	}
	if bundleEnabled && dependencies.newManager == nil {
		return candidate, fmt.Errorf("recursion manager constructor is nil")
	}

	model, fallbacks := resolveSubagentModelPolicy(agent)
	var legacyTools *tools.ToolRegistry
	if bundleEnabled {
		legacyTools = agent.Tools.Clone()
		if installSkillLive != nil {
			legacyTools.Register(installSkillLive)
		}
	}
	spec := recursionCatalogSpec{
		al:             al,
		registry:       registry,
		provider:       provider,
		agentID:        agentID,
		workspace:      agent.Workspace,
		model:          model,
		fallbacks:      cloneOptionalModelFallbacks(fallbacks),
		maxTokens:      agent.MaxTokens,
		temperature:    agent.Temperature,
		maxMediaSize:   cfg.Agents.Defaults.GetMaxMediaSize(),
		legacyTools:    legacyTools,
		policy:         al.toolPolicy,
		managerService: "agent.recursion.manager.v1:" + agentID,
	}
	var rootBundle *recursionOwnerBundle
	if bundleEnabled {
		var err error
		rootBundle, err = buildRecursionOwnerBundle(spec, dependencies)
		if err != nil {
			return candidate, fmt.Errorf("build root recursion bundle for %q: %w", agentID, err)
		}
	}

	appendFactory := func(
		live tools.Tool,
		traits tools.ToolTraits,
		build tools.ToolBuildFunc,
	) error {
		factory, factoryErr := tools.NewToolFactoryFromPrototype(live, traits, build)
		if factoryErr != nil {
			return factoryErr
		}
		candidate.installs = append(candidate.installs, tools.FactoryBackedInstall{
			Live: live, Factory: factory,
		})
		return nil
	}
	mutationTraits := tools.ToolTraits{
		Risk:        tools.ToolRiskProcess,
		Parallel:    tools.ToolParallelSerialized,
		Idempotency: tools.ToolIdempotencyNonIdempotent,
		Sharing:     tools.ToolSharingPerOwner,
	}
	statusTraits := tools.ToolTraits{
		Risk:        tools.ToolRiskReadOnly,
		Parallel:    tools.ToolParallelSafe,
		Idempotency: tools.ToolIdempotencyIdempotent,
		Sharing:     tools.ToolSharingPerOwner,
	}

	if bundleEnabled && spawnEnabled {
		live := buildSpawnTool(rootBundle, spec)
		if err := appendFactory(live, mutationTraits, func(
			ctx tools.ToolBuildContext,
		) (tools.Tool, error) {
			bundle, bundleErr := recursionOwnerBundleForBuild(ctx, spec, dependencies)
			if bundleErr != nil {
				return nil, bundleErr
			}
			return buildSpawnTool(bundle, spec), nil
		}); err != nil {
			return candidate, fmt.Errorf("build spawn factory for %q: %w", agentID, err)
		}

		subagentLive := buildSynchronousSubagentTool(rootBundle)
		if err := appendFactory(subagentLive, mutationTraits, func(
			ctx tools.ToolBuildContext,
		) (tools.Tool, error) {
			bundle, bundleErr := recursionOwnerBundleForBuild(ctx, spec, dependencies)
			if bundleErr != nil {
				return nil, bundleErr
			}
			return buildSynchronousSubagentTool(bundle), nil
		}); err != nil {
			return candidate, fmt.Errorf("build subagent factory for %q: %w", agentID, err)
		}
	}
	if bundleEnabled && statusEnabled {
		live := tools.NewSpawnStatusTool(rootBundle.manager)
		if err := appendFactory(live, statusTraits, func(
			ctx tools.ToolBuildContext,
		) (tools.Tool, error) {
			bundle, bundleErr := recursionOwnerBundleForBuild(ctx, spec, dependencies)
			if bundleErr != nil {
				return nil, bundleErr
			}
			return tools.NewSpawnStatusTool(bundle.manager), nil
		}); err != nil {
			return candidate, fmt.Errorf("build spawn-status factory for %q: %w", agentID, err)
		}
	}
	if delegateEnabled {
		live := buildDelegateTool(NewSubTurnSpawner(spec.al), spec)
		if err := appendFactory(live, mutationTraits, func(
			_ tools.ToolBuildContext,
		) (tools.Tool, error) {
			return buildDelegateTool(NewSubTurnSpawner(spec.al), spec), nil
		}); err != nil {
			return candidate, fmt.Errorf("build delegate factory for %q: %w", agentID, err)
		}
	}
	candidate.beforeVersion = agent.Tools.Version()
	return candidate, nil
}

func buildRecursionOwnerBundle(
	spec recursionCatalogSpec,
	dependencies recursionCatalogDependencies,
) (*recursionOwnerBundle, error) {
	if dependencies.newManager == nil || spec.al == nil || spec.registry == nil ||
		spec.legacyTools == nil {
		return nil, fmt.Errorf("recursion bundle specification is incomplete")
	}
	manager := dependencies.newManager(spec.provider, spec.model, spec.workspace)
	if manager == nil {
		return nil, fmt.Errorf("subagent manager constructor returned nil")
	}
	manager.SetDefaultModelFallbacks(cloneOptionalModelFallbacks(spec.fallbacks))
	manager.SetToolPolicy(spec.policy)
	manager.SetLLMOptions(spec.maxTokens, spec.temperature)
	manager.SetMediaResolver(func(messages []providers.Message) []providers.Message {
		return resolveMediaRefs(
			messages,
			spec.al.mediaStoreSnapshot(),
			spec.maxMediaSize,
			0,
		)
	})
	manager.SetSpawner(legacyRecursionSpawner(spec))
	manager.SetTools(spec.legacyTools.Clone())
	return &recursionOwnerBundle{
		manager: manager,
		spawner: NewSubTurnSpawner(spec.al),
	}, nil
}

func recursionOwnerBundleForBuild(
	ctx tools.ToolBuildContext,
	spec recursionCatalogSpec,
	dependencies recursionCatalogDependencies,
) (*recursionOwnerBundle, error) {
	value, err := ctx.Service(spec.managerService, func() (any, error) {
		return buildRecursionOwnerBundle(spec, dependencies)
	})
	if err != nil {
		return nil, err
	}
	bundle, ok := value.(*recursionOwnerBundle)
	if !ok || bundle == nil || bundle.manager == nil || bundle.spawner == nil {
		return nil, fmt.Errorf("recursion owner service has unexpected type %T", value)
	}
	return bundle, nil
}

func buildSpawnTool(
	bundle *recursionOwnerBundle,
	spec recursionCatalogSpec,
) *tools.SpawnTool {
	tool := tools.NewSpawnTool(bundle.manager)
	tool.SetSpawner(bundle.spawner)
	tool.SetAllowlistChecker(func(targetAgentID string) bool {
		if spec.registry == nil {
			return false
		}
		if target, exists := spec.registry.GetAgent(targetAgentID); !exists || target == nil {
			return false
		}
		return spec.registry.CanSpawnSubagent(spec.agentID, targetAgentID)
	})
	return tool
}

func buildSynchronousSubagentTool(bundle *recursionOwnerBundle) *tools.SubagentTool {
	tool := tools.NewSubagentTool(bundle.manager)
	tool.SetSpawner(bundle.spawner)
	return tool
}

func buildDelegateTool(
	spawner *AgentLoopSpawner,
	spec recursionCatalogSpec,
) *tools.DelegateTool {
	tool := tools.NewDelegateTool()
	tool.SetSpawner(spawner)
	tool.SetSelfAgentID(spec.agentID)
	tool.SetAllowlistChecker(func(targetAgentID string) bool {
		return spec.registry.CanSpawnSubagent(spec.agentID, targetAgentID)
	})
	return tool
}

func legacyRecursionSpawner(spec recursionCatalogSpec) tools.SpawnSubTurnFunc {
	return func(
		ctx context.Context,
		task, label, targetAgentID string,
		registry *tools.ToolRegistry,
		maxTokens int,
		_ float64,
		hasMaxTokens, _ bool,
	) (*tools.ToolResult, error) {
		parent := turnStateFromContext(ctx)
		if parent == nil {
			parent = spec.al.newAdHocRootTurnState(ctx)
			if source, ok := spec.registry.GetAgent(spec.agentID); ok && source != nil {
				parent.agent = source
				parent.agentID = source.ID
			}
		}
		spec.al.prepareTurnState(parent)

		constructible := make(map[string]struct{})
		if source, ok := spec.registry.GetAgent(spec.agentID); ok && source != nil &&
			source.Tools != nil {
			for _, capability := range source.Tools.InstantiationCapabilities() {
				if capability.FactoryBacked || capability.ImmutableShared {
					constructible[capability.Name] = struct{}{}
				}
			}
		}
		toolSlice := make([]tools.Tool, 0)
		if registry != nil {
			for _, name := range registry.List() {
				_, eligible := constructible[name]
				if tool, ok := registry.Get(name); eligible && ok {
					toolSlice = append(toolSlice, tool)
				}
			}
		}
		systemPrompt := "You are a subagent. Complete the given task independently and report the result.\n" +
			"You have access to tools - use them as needed to complete your task.\n" +
			"After completing the task, provide a clear summary of what was done.\n\n" +
			"Task: " + task
		model := spec.model
		fallbacks := cloneOptionalModelFallbacks(spec.fallbacks)
		if targetAgentID != "" {
			if target, ok := spec.registry.GetAgent(targetAgentID); ok && target != nil {
				model = target.Model
				fallbacks = cloneOptionalModelFallbacks(target.Fallbacks)
			}
		}
		turnConfig := SubTurnConfig{
			Model:          model,
			ModelFallbacks: fallbacks,
			TargetAgentID:  targetAgentID,
			Tools:          toolSlice,
			SystemPrompt:   systemPrompt,
		}
		if hasMaxTokens {
			turnConfig.MaxTokens = maxTokens
		}
		_ = label // Legacy prompt did not include the label.
		return spawnSubTurn(ctx, spec.al, parent, turnConfig)
	}
}

func installRecursionCatalog(
	candidates []recursionCatalogCandidate,
	install recursionCatalogInstaller,
) error {
	if install == nil {
		return fmt.Errorf("recursion catalog installer is nil")
	}
	stage, err := stageRecursionCatalog(candidates)
	if err != nil {
		return err
	}
	if len(stage.batches) == 0 {
		return nil
	}
	admissions, err := callRecursionCatalogInstaller(install, stage.batches)
	if err != nil {
		return fmt.Errorf("install recursion factory catalog: %w", err)
	}
	if err := verifyRecursionAdmissions(stage, admissions); err != nil {
		logger.ErrorSafeCF(
			logger.ComponentAgent,
			logger.DiagnosticMessageAgentRecursionAdmissionProjectionFailedAfterCatalogCommit,
			logger.NewSafeFields(
				agentDiagnosticErrorField(logger.ErrorClassInternal, err),
			),
		)
	}
	return nil
}

func callRecursionCatalogInstaller(
	install recursionCatalogInstaller,
	batches []tools.FactoryBackedBatch,
) (admissions []tools.FactoryBackedAdmission, returnErr error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			admissions = nil
			returnErr = fmt.Errorf("recursion catalog installer panicked: %v", recovered)
		}
	}()
	return install(batches)
}

func stageRecursionCatalog(
	candidates []recursionCatalogCandidate,
) (stagedRecursionCatalog, error) {
	sorted := append([]recursionCatalogCandidate(nil), candidates...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left].agentID < sorted[right].agentID })
	stage := stagedRecursionCatalog{}
	seenRegistries := make(map[*tools.ToolRegistry]string, len(sorted))
	seenAgentIDs := make(map[string]struct{}, len(sorted))
	for _, candidate := range sorted {
		if candidate.agentID == "" || candidate.registry == nil {
			return stagedRecursionCatalog{}, fmt.Errorf("recursion catalog candidate is incomplete")
		}
		if previous, duplicate := seenRegistries[candidate.registry]; duplicate {
			return stagedRecursionCatalog{}, fmt.Errorf(
				"recursion agents %q and %q share one registry",
				previous,
				candidate.agentID,
			)
		}
		if _, duplicate := seenAgentIDs[candidate.agentID]; duplicate {
			return stagedRecursionCatalog{}, fmt.Errorf(
				"recursion agent %q is duplicated",
				candidate.agentID,
			)
		}
		seenAgentIDs[candidate.agentID] = struct{}{}
		seenRegistries[candidate.registry] = candidate.agentID
		if len(candidate.installs) == 0 {
			continue
		}
		batchIndex := len(stage.batches)
		installs := append([]tools.FactoryBackedInstall(nil), candidate.installs...)
		stage.batches = append(stage.batches, tools.FactoryBackedBatch{
			Registry: candidate.registry,
			Installs: installs,
		})
		stage.beforeVersions = append(stage.beforeVersions, candidate.beforeVersion)
		for installIndex, entry := range installs {
			stage.sidecars = append(stage.sidecars, recursionInstallSidecar{
				batchIndex: batchIndex, installIndex: installIndex,
				agentID: candidate.agentID, name: entry.Live.Name(),
				registry: candidate.registry, live: entry.Live,
			})
		}
	}
	return stage, nil
}

func verifyRecursionAdmissions(
	stage stagedRecursionCatalog,
	admissions []tools.FactoryBackedAdmission,
) error {
	if len(admissions) != len(stage.sidecars) {
		return fmt.Errorf(
			"recursion admission count %d does not match staged count %d",
			len(admissions),
			len(stage.sidecars),
		)
	}
	admittedByBatch := make([]uint64, len(stage.batches))
	for index, sidecar := range stage.sidecars {
		admission := admissions[index]
		if admission.BatchIndex != sidecar.batchIndex ||
			admission.InstallIndex != sidecar.installIndex ||
			admission.Name != sidecar.name || admission.Replaced {
			return fmt.Errorf("recursion admission %d identity or replacement is invalid", index)
		}
		occupant, occupied := sidecar.registry.GetRegistered(sidecar.name)
		if admission.Admitted {
			if !occupied || occupant != sidecar.live {
				return fmt.Errorf(
					"recursion admission %d did not publish exact %q for %q",
					index,
					sidecar.name,
					sidecar.agentID,
				)
			}
			admittedByBatch[sidecar.batchIndex]++
		} else if occupied && occupant == sidecar.live {
			return fmt.Errorf("denied recursion admission %d published %q", index, sidecar.name)
		}
	}
	for batchIndex, batch := range stage.batches {
		want := stage.beforeVersions[batchIndex] + admittedByBatch[batchIndex]
		if got := batch.Registry.Version(); got != want {
			return fmt.Errorf("recursion registry %d version = %d, want %d", batchIndex, got, want)
		}
	}
	return nil
}
