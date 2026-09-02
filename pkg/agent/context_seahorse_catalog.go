//go:build !mipsle && !netbsd && !(freebsd && arm)

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/database"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/seahorse"
	"github.com/sipeed/picoclaw/pkg/tools"
)

type seahorseConstructionState uint8

const (
	seahorseConstructionPrivate seahorseConstructionState = iota
	seahorseConstructionCommitted
	seahorseConstructionReturned
)

type seahorseEngineFactory func(
	seahorse.Config,
	seahorse.CompleteFn,
) (*seahorse.Engine, error)

type seahorseEngineCloser func(*seahorse.Engine) error

type seahorseCatalogInstaller func(
	[]tools.FactoryBackedBatch,
) ([]tools.FactoryBackedAdmission, error)

type seahorseBootstrapFunc func(
	context.Context,
	*seahorseContextManager,
	*AgentInstance,
	*seahorse.Engine,
	string,
) error

var newRuntimeSeahorseEngine seahorseEngineFactory = seahorse.NewEngine

type seahorseContextDependencies struct {
	newEngine   seahorseEngineFactory
	closeEngine seahorseEngineCloser
	install     seahorseCatalogInstaller
	bootstrap   seahorseBootstrapFunc
}

func defaultSeahorseContextDependencies() seahorseContextDependencies {
	return seahorseContextDependencies{
		newEngine: newRuntimeSeahorseEngine,
		closeEngine: func(engine *seahorse.Engine) error {
			return engine.Close()
		},
		install: tools.InstallFactoryBackedTransaction,
		bootstrap: func(
			ctx context.Context,
			manager *seahorseContextManager,
			agent *AgentInstance,
			engine *seahorse.Engine,
			sessionKey string,
		) error {
			return manager.bootstrapAgentSession(ctx, agent, engine, sessionKey)
		},
	}
}

func normalizeSeahorseContextDependencies(
	dependencies seahorseContextDependencies,
) (seahorseContextDependencies, error) {
	if dependencies.newEngine == nil {
		return seahorseContextDependencies{}, fmt.Errorf("Seahorse engine factory is nil")
	}
	if dependencies.closeEngine == nil {
		return seahorseContextDependencies{}, fmt.Errorf("Seahorse engine closer is nil")
	}
	if dependencies.install == nil {
		return seahorseContextDependencies{}, fmt.Errorf("Seahorse catalog installer is nil")
	}
	if dependencies.bootstrap == nil {
		return seahorseContextDependencies{}, fmt.Errorf("Seahorse bootstrap function is nil")
	}
	return dependencies, nil
}

type seahorseAgentCandidate struct {
	id            string
	agent         *AgentInstance
	registry      *tools.ToolRegistry
	storeID       database.StoreID
	engine        *seahorse.Engine
	retrieval     *seahorse.RetrievalEngine
	grep          *seahorse.GrepTool
	grepFactory   tools.ToolFactory
	expand        *seahorse.ExpandTool
	expandFactory tools.ToolFactory
}

type seahorseInstallSidecar struct {
	batchIndex   int
	installIndex int
	agentID      string
	name         string
	registry     *tools.ToolRegistry
	live         tools.Tool
}

type stagedSeahorseCatalog struct {
	batches        []tools.FactoryBackedBatch
	sidecars       []seahorseInstallSidecar
	beforeVersions []uint64
}

func newSeahorseContextManagerWithDependencies(
	ctx context.Context,
	_ json.RawMessage,
	al *AgentLoop,
	dependencies seahorseContextDependencies,
) (result ContextManager, returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if al == nil {
		return nil, fmt.Errorf("seahorse: AgentLoop is required")
	}
	normalizedDependencies, dependencyErr := normalizeSeahorseContextDependencies(dependencies)
	if dependencyErr != nil {
		return nil, fmt.Errorf("seahorse: %w", dependencyErr)
	}
	dependencies = normalizedDependencies
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, fmt.Errorf(
			"seahorse: construction canceled before snapshot: %w",
			contextErr,
		)
	}

	candidates, defaultAgent, snapshotErr := snapshotSeahorseAgents(al.GetRegistry())
	if snapshotErr != nil {
		return nil, fmt.Errorf("seahorse: snapshot agents: %w", snapshotErr)
	}
	manager := &seahorseContextManager{
		sessions:    defaultAgent.Sessions,
		al:          al,
		engines:     make(map[string]*seahorse.Engine, len(candidates)),
		engineIDs:   make([]string, 0, len(candidates)),
		closeEngine: dependencies.closeEngine,
	}
	state := seahorseConstructionPrivate
	defer func() {
		recovered := recover()
		if state == seahorseConstructionPrivate {
			if recovered != nil {
				returnErr = errors.Join(
					returnErr,
					fmt.Errorf("seahorse: construction panic: %v", recovered),
				)
			}
			returnErr = errors.Join(returnErr, manager.Close())
			result = nil
			return
		}
		if recovered != nil {
			logger.ErrorSafeCF(
				logger.ComponentSeahorse,
				logger.DiagnosticMessageAgentPostCommitSeahorseCatalogHandlingPanicked,
				logger.NewSafeFields(agentDiagnosticPanicField(recovered)),
			)
			result = manager
			returnErr = nil
		}
	}()

	for index := range candidates {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, fmt.Errorf(
				"seahorse: construction canceled before agent %q engine: %w",
				candidates[index].id,
				contextErr,
			)
		}
		candidate := &candidates[index]
		engine, engineErr := dependencies.newEngine(
			seahorse.Config{Workspace: candidate.agent.Workspace},
			agentProviderToCompleteFn(al, candidate.agent),
		)
		if engineErr != nil {
			return nil, fmt.Errorf(
				"seahorse: create engine for agent %q: %w",
				candidate.id,
				engineErr,
			)
		}
		if engine == nil {
			return nil, fmt.Errorf(
				"seahorse: create engine for agent %q returned nil",
				candidate.id,
			)
		}
		candidate.engine = engine
		candidate.retrieval = engine.GetRetrieval()
		manager.engines[candidate.id] = engine
		manager.engineIDs = append(manager.engineIDs, candidate.id)
		if candidate.agent == defaultAgent {
			manager.engine = engine
		}
	}
	if manager.engine == nil {
		return nil, fmt.Errorf("seahorse: default agent engine is required")
	}
	if identityErr := validateSeahorseStoreIdentities(candidates); identityErr != nil {
		return nil, fmt.Errorf("seahorse: validate agent stores: %w", identityErr)
	}

	for index := range candidates {
		candidate := &candidates[index]
		if candidate.agent.Sessions == nil {
			continue
		}
		sessionKeys := append([]string(nil), candidate.agent.Sessions.ListSessions()...)
		sort.Strings(sessionKeys)
		for _, sessionKey := range sessionKeys {
			if contextErr := ctx.Err(); contextErr != nil {
				return nil, fmt.Errorf(
					"seahorse: construction canceled before agent %q bootstrap: %w",
					candidate.id,
					contextErr,
				)
			}
			if bootstrapErr := dependencies.bootstrap(
				ctx,
				manager,
				candidate.agent,
				candidate.engine,
				sessionKey,
			); bootstrapErr != nil {
				return nil, fmt.Errorf(
					"seahorse: bootstrap agent %q: %w",
					candidate.id,
					bootstrapErr,
				)
			}
		}
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, fmt.Errorf(
			"seahorse: construction canceled after bootstrap: %w",
			contextErr,
		)
	}

	for index := range candidates {
		candidate := &candidates[index]
		grep, grepFactory, factoryErr := seahorse.NewGrepToolWithFactory(candidate.retrieval)
		if factoryErr != nil {
			return nil, fmt.Errorf(
				"seahorse: build grep factory for agent %q: %w",
				candidate.id,
				factoryErr,
			)
		}
		candidate.grep = grep
		candidate.grepFactory = grepFactory
		expand, expandFactory, factoryErr := seahorse.NewExpandToolWithFactory(
			candidate.retrieval,
		)
		if factoryErr != nil {
			return nil, fmt.Errorf(
				"seahorse: build expand factory for agent %q: %w",
				candidate.id,
				factoryErr,
			)
		}
		candidate.expand = expand
		candidate.expandFactory = expandFactory
	}

	stage := stageSeahorseCatalog(candidates)
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, fmt.Errorf(
			"seahorse: construction canceled before catalog commit: %w",
			contextErr,
		)
	}
	admissions, installErr := dependencies.install(stage.batches)
	if installErr != nil {
		return nil, fmt.Errorf("seahorse: install retrieval catalog: %w", installErr)
	}
	state = seahorseConstructionCommitted
	result = manager

	if verificationErr := verifySeahorseAdmissions(stage, admissions); verificationErr != nil {
		logger.ErrorSafeCF(
			logger.ComponentSeahorse,
			logger.DiagnosticMessageAgentSeahorseAdmissionProjectionFailedAfterCatalogCommit,
			logger.NewSafeFields(
				agentDiagnosticErrorField(logger.ErrorClassInternal, verificationErr),
			),
		)
		state = seahorseConstructionReturned
		return manager, nil
	}

	state = seahorseConstructionReturned
	return manager, nil
}

func snapshotSeahorseAgents(
	registry *AgentRegistry,
) ([]seahorseAgentCandidate, *AgentInstance, error) {
	if registry == nil {
		return nil, nil, fmt.Errorf("agent registry is required")
	}
	defaultAgent := registry.GetDefaultAgent()
	if defaultAgent == nil {
		return nil, nil, fmt.Errorf("default agent is required")
	}
	agentIDs := registry.ListAgentIDs()
	sort.Strings(agentIDs)
	if len(agentIDs) == 0 {
		return nil, nil, fmt.Errorf("at least one agent is required")
	}

	seenRegistries := make(map[*tools.ToolRegistry]string, len(agentIDs))
	candidates := make([]seahorseAgentCandidate, 0, len(agentIDs))
	defaultFound := false
	for _, agentID := range agentIDs {
		agent, ok := registry.GetAgent(agentID)
		if !ok || agent == nil {
			return nil, nil, fmt.Errorf("agent %q is unavailable", agentID)
		}
		if agent.ID == "" || agent.ID != strings.TrimSpace(agent.ID) || agent.ID != agentID {
			return nil, nil, fmt.Errorf(
				"agent %q has noncanonical identity %q",
				agentID,
				agent.ID,
			)
		}
		if agent.Tools == nil {
			return nil, nil, fmt.Errorf("agent %q tool registry is nil", agentID)
		}
		if owner, owned := agent.Tools.Owner(); owned {
			return nil, nil, fmt.Errorf(
				"agent %q tool registry is owner-scoped (%q)",
				agentID,
				owner.Scope,
			)
		}
		if previous, duplicate := seenRegistries[agent.Tools]; duplicate {
			return nil, nil, fmt.Errorf(
				"agents %q and %q share one tool registry",
				previous,
				agentID,
			)
		}
		seenRegistries[agent.Tools] = agentID
		if agent.Workspace == "" || agent.Workspace != strings.TrimSpace(agent.Workspace) {
			return nil, nil, fmt.Errorf("agent %q workspace must be exact and nonempty", agentID)
		}

		candidates = append(candidates, seahorseAgentCandidate{
			id: agentID, agent: agent, registry: agent.Tools,
		})
		if agent == defaultAgent {
			defaultFound = true
		}
	}
	if !defaultFound {
		return nil, nil, fmt.Errorf("default agent is absent from the registry snapshot")
	}
	return candidates, defaultAgent, nil
}

func validateSeahorseStoreIdentities(candidates []seahorseAgentCandidate) error {
	seen := make(map[database.StoreID]string, len(candidates))
	requireCatalogIdentity := database.RuntimeClient() != nil
	for index := range candidates {
		candidate := &candidates[index]
		if candidate.engine == nil {
			return fmt.Errorf("agent %q engine is unavailable", candidate.id)
		}
		storeID := candidate.engine.StoreID()
		if !storeID.Valid() {
			// Tests may inject explicit offline engines. The production dependency
			// always has an installed runtime broker and must return a catalog ID.
			if requireCatalogIdentity {
				return fmt.Errorf("agent %q Seahorse StoreID is invalid", candidate.id)
			}
			continue
		}
		if previous, duplicate := seen[storeID]; duplicate {
			return fmt.Errorf(
				"agents %q and %q share Seahorse StoreID %q",
				previous,
				candidate.id,
				storeID,
			)
		}
		seen[storeID] = candidate.id
		candidate.storeID = storeID
	}
	return nil
}

func stageSeahorseCatalog(
	candidates []seahorseAgentCandidate,
) stagedSeahorseCatalog {
	stage := stagedSeahorseCatalog{
		batches:        make([]tools.FactoryBackedBatch, 0, len(candidates)),
		sidecars:       make([]seahorseInstallSidecar, 0, len(candidates)*2),
		beforeVersions: make([]uint64, 0, len(candidates)),
	}
	for batchIndex := range candidates {
		candidate := &candidates[batchIndex]
		installs := []tools.FactoryBackedInstall{
			{Live: candidate.grep, Factory: candidate.grepFactory},
			{Live: candidate.expand, Factory: candidate.expandFactory},
		}
		stage.batches = append(stage.batches, tools.FactoryBackedBatch{
			Registry: candidate.registry,
			Installs: installs,
		})
		stage.beforeVersions = append(
			stage.beforeVersions,
			candidate.registry.Version(),
		)
		for installIndex, install := range installs {
			stage.sidecars = append(stage.sidecars, seahorseInstallSidecar{
				batchIndex: batchIndex, installIndex: installIndex,
				agentID: candidate.id, name: install.Live.Name(),
				registry: candidate.registry, live: install.Live,
			})
		}
	}
	return stage
}

func verifySeahorseAdmissions(
	stage stagedSeahorseCatalog,
	admissions []tools.FactoryBackedAdmission,
) error {
	if len(admissions) != len(stage.sidecars) {
		return fmt.Errorf(
			"Seahorse admission count %d does not match staged count %d",
			len(admissions),
			len(stage.sidecars),
		)
	}
	admittedByBatch := make([]uint64, len(stage.batches))
	for index, sidecar := range stage.sidecars {
		admission := admissions[index]
		if admission.BatchIndex != sidecar.batchIndex ||
			admission.InstallIndex != sidecar.installIndex ||
			admission.Name != sidecar.name {
			return fmt.Errorf(
				"Seahorse admission %d identity = %d/%d/%q, want %d/%d/%q",
				index,
				admission.BatchIndex,
				admission.InstallIndex,
				admission.Name,
				sidecar.batchIndex,
				sidecar.installIndex,
				sidecar.name,
			)
		}
		if admission.Replaced {
			return fmt.Errorf(
				"Seahorse insert-only admission %d unexpectedly replaced %q",
				index,
				sidecar.name,
			)
		}
		occupant, occupied := sidecar.registry.GetRegistered(sidecar.name)
		if admission.Admitted {
			if !occupied || occupant != sidecar.live {
				return fmt.Errorf(
					"Seahorse admission %d did not publish exact %q root for agent %q",
					index,
					sidecar.name,
					sidecar.agentID,
				)
			}
			admittedByBatch[sidecar.batchIndex]++
		} else if occupied && occupant == sidecar.live {
			return fmt.Errorf(
				"denied Seahorse admission %d published %q for agent %q",
				index,
				sidecar.name,
				sidecar.agentID,
			)
		}
	}
	for batchIndex, batch := range stage.batches {
		want := stage.beforeVersions[batchIndex] + admittedByBatch[batchIndex]
		if got := batch.Registry.Version(); got != want {
			return fmt.Errorf(
				"Seahorse registry %d version = %d, want %d",
				batchIndex,
				got,
				want,
			)
		}
	}
	return nil
}

func closeSeahorseEngine(
	closeEngine seahorseEngineCloser,
	engine *seahorse.Engine,
) (returnErr error) {
	if engine == nil {
		return nil
	}
	if closeEngine == nil {
		closeEngine = func(engine *seahorse.Engine) error { return engine.Close() }
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("close Seahorse engine panic: %v", recovered),
			)
		}
	}()
	return closeEngine(engine)
}
