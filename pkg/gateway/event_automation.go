package gateway

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/eventing"
	eventchannel "github.com/sipeed/picoclaw/pkg/eventing/channelmessage"
	eventgithubpoll "github.com/sipeed/picoclaw/pkg/eventing/githubpoll"
	eventoperator "github.com/sipeed/picoclaw/pkg/eventing/operator"
	eventwebhook "github.com/sipeed/picoclaw/pkg/eventing/webhook"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/reviews"
	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	eventRetentionMaintenanceInterval = 6 * time.Hour
	eventRetentionPruneBatchSize      = 500
	eventRetentionMaxBatchesPerCycle  = 20
	// Signed Unix nanoseconds span 213,503 complete UTC days. A longer
	// retention period cannot expire any timestamp representable by the event
	// store and must be handled before time.AddDate can overflow internally.
	eventRetentionMaxDurableDays = 213_503
)

type eventAutomationService struct {
	store           *eventing.Store
	operatorBackend *eventoperator.Backend
	webhookBackend  *eventwebhook.Backend
	channelBackend  *eventchannel.Backend
	prWorkspaces    *prworkspace.Service
	githubPoller    *eventgithubpoll.Poller
	prLocalCI       *prWorkspaceLocalCIRuntime
	cancel          context.CancelFunc
	done            chan struct{}

	stopOnce  sync.Once
	closeOnce sync.Once
	closeErr  error
}

type eventAutomationRuntimeAcquire func(context.Context) (context.Context, func(), error)

type eventRetentionPruner interface {
	Prune(ctx context.Context, before time.Time, limit int) (int64, error)
}

type eventReviewRuntime struct {
	agentLoop       *agent.AgentLoop
	agent           workflows.AgentRunner
	agentID         string
	localCI         *prWorkspaceLocalCIRuntime
	submitter       reviews.Submitter
	provider        *reviews.GitHubProvider
	notificationMCP workflows.ToolRunner
	mcpArtifactRoot string
}

func newEventReviewRuntime(
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
) eventReviewRuntime {
	runtime := eventReviewRuntime{agentLoop: agentLoop}
	if agentLoop != nil {
		runtime.mcpArtifactRoot = githubMCPArtifactRoot(cfg, agentLoop)
	}
	return runtime
}

func defaultPRWorkspaceRuntimeAgent(agentLoop *agent.AgentLoop) (*agent.AgentInstance, error) {
	if agentLoop == nil {
		return nil, errors.New("PR workspace agent runtime is not configured")
	}
	registry := agentLoop.GetRegistry()
	if registry == nil {
		return nil, errors.New("PR workspace agent registry is not configured")
	}
	defaultAgent := registry.GetDefaultAgent()
	if defaultAgent == nil {
		return nil, errors.New("PR workspace runtime has no default agent")
	}
	agentID := strings.TrimSpace(defaultAgent.ID)
	if !routing.IsCanonicalAgentID(agentID) || agentID != defaultAgent.ID {
		return nil, errors.New("PR workspace default agent ID is not exact and canonical")
	}
	return defaultAgent, nil
}

func setupEventAutomationService(
	ctx context.Context,
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
) (*eventAutomationService, error) {
	if cfg == nil || !cfg.Events.Ingress.Enabled {
		return nil, nil
	}
	var executor *workflows.Executor
	var runtimeEvents runtimeevents.Bus
	if agentLoop != nil {
		runtimeEvents = agentLoop.RuntimeEventBus()
	}
	if cfg.Workflows.Enabled {
		var err error
		executor, err = agent.NewEventWorkflowExecutor(ctx, agentLoop)
		if err != nil {
			return nil, err
		}
	}
	var acquireRuntime eventAutomationRuntimeAcquire
	if agentLoop != nil {
		acquireRuntime = func(workerCtx context.Context) (context.Context, func(), error) {
			return agentLoop.AcquireRuntimeGeneration(workerCtx, cfg)
		}
	}
	reviewRuntime := newEventReviewRuntime(cfg, agentLoop)
	if agentLoop != nil {
		reviewRuntime.agent = agent.NewWorkflowAgentRunner(agentLoop)
		defaultAgent, err := defaultPRWorkspaceRuntimeAgent(agentLoop)
		if err != nil {
			return nil, err
		}
		reviewRuntime.agentID = defaultAgent.ID
		pollNotifications := githubNotificationPollingEnabled(cfg)
		reviewSubmission := githubReviewSubmissionReady(ctx, agentLoop)
		reviewProviderRead := githubReviewProviderReadReady(ctx, agentLoop)
		developmentProviderRead := githubDevelopmentProviderReadReady(ctx, agentLoop)
		if pollNotifications {
			if err := validateGitHubNotificationPollingRuntime(
				ctx,
				cfg,
				agentLoop,
			); err != nil {
				return nil, err
			}
		}
		if pollNotifications || reviewSubmission || reviewProviderRead || developmentProviderRead {
			toolRunner, err := agent.NewWorkflowToolRunner(agentLoop, "")
			if err != nil {
				return nil, fmt.Errorf("initialize GitHub event MCP tools: %w", err)
			}
			configureGitHubMCPReviewRuntime(
				&reviewRuntime,
				toolRunner,
				githubMCPArtifactRoot(cfg, agentLoop),
				pollNotifications,
				reviewSubmission,
			)
			if reviewProviderRead || developmentProviderRead {
				reviewRuntime.provider, err = reviews.NewGitHubProvider(
					toolRunner,
					githubMCPArtifactRoot(cfg, agentLoop),
				)
				if err != nil {
					return nil, fmt.Errorf("initialize GitHub review provider: %w", err)
				}
			}
		}
	} else if githubNotificationPollingEnabled(cfg) {
		return nil, errors.New(
			"GitHub notification polling requires the agent MCP runtime",
		)
	}
	return newEventAutomationServiceWithRuntime(
		ctx,
		cfg,
		executor,
		runtimeEvents,
		acquireRuntime,
		reviewRuntime,
	)
}

func configureGitHubMCPReviewRuntime(
	runtime *eventReviewRuntime,
	runner workflows.ToolRunner,
	artifactRoot string,
	pollNotifications bool,
	reviewSubmission bool,
) {
	if runtime == nil {
		return
	}
	runtime.mcpArtifactRoot = strings.TrimSpace(artifactRoot)
	if pollNotifications {
		runtime.notificationMCP = runner
	}
	if reviewSubmission {
		runtime.submitter = &reviews.GitHubSubmitter{
			Runner:       runner,
			ArtifactRoot: runtime.mcpArtifactRoot,
		}
	}
}

func newEventAutomationService(
	ctx context.Context,
	cfg *config.Config,
	executor *workflows.Executor,
	runtimeEvents runtimeevents.Bus,
	acquireRuntime eventAutomationRuntimeAcquire,
) (*eventAutomationService, error) {
	reviewRuntime := eventReviewRuntime{}
	if executor != nil {
		reviewRuntime.agent = executor.Agents
	}
	return newEventAutomationServiceWithRuntime(
		ctx,
		cfg,
		executor,
		runtimeEvents,
		acquireRuntime,
		reviewRuntime,
	)
}

func newEventAutomationServiceWithRuntime(
	ctx context.Context,
	cfg *config.Config,
	executor *workflows.Executor,
	runtimeEvents runtimeevents.Bus,
	acquireRuntime eventAutomationRuntimeAcquire,
	reviewRuntime eventReviewRuntime,
) (*eventAutomationService, error) {
	if cfg == nil || !cfg.Events.Ingress.Enabled {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg.Workflows.Enabled && executor == nil {
		return nil, fmt.Errorf("workflow executor is required when event workflows are enabled")
	}

	workspace := cfg.WorkspacePath()
	store, err := openEventAutomationStore(ctx, cfg)
	if err != nil {
		return nil, err
	}
	prLocalCI := reviewRuntime.localCI
	if prLocalCI == nil && reviewRuntime.agentLoop != nil && reviewRuntime.agentID != "" {
		prLocalCI, err = newPRWorkspaceLocalCIRuntime(cfg)
		if err != nil {
			logger.WarnSafeCF(
				logger.ComponentEventing,
				logger.DiagnosticMessageEventingPRWorkspaceLocalCIIsUnavailable,
				logger.NewSafeFields(
					gatewayDiagnosticErrorField(logger.ErrorClassInternal, err),
				),
			)
			prLocalCI = nil
		}
	}
	closeSetup := func(setupErr error) error {
		var ciErr error
		if prLocalCI != nil {
			ciErr = prLocalCI.Close()
		}
		return errors.Join(setupErr, ciErr, store.Close())
	}
	var runStore workflows.RunStore = workflows.NewFileRunStore(workspace)
	if cfg.Workflows.Enabled {
		if executor.Store != nil {
			runStore = executor.Store
		} else {
			executor.Store = runStore
		}
	}

	var resolver *prWorkspaceGitHubResolver
	var provider prworkspace.ProviderResolver
	var reviewEvidence prworkspace.ReviewEvidenceLoader
	var issuePublisher prworkspace.IssuePublisher
	issuePublicationReady := githubPRWorkspaceIssuePublicationReady(ctx, reviewRuntime.agentLoop)
	pullCreationReady := githubDevelopmentPullCreationReady(ctx, reviewRuntime.agentLoop)
	if reviewRuntime.provider != nil {
		resolver = &prWorkspaceGitHubResolver{
			provider:             reviewRuntime.provider,
			canReview:            reviewRuntime.submitter != nil,
			canCreateIssue:       issuePublicationReady,
			canCreatePullRequest: pullCreationReady,
		}
		provider = resolver
		reviewEvidence = resolver
		if issuePublicationReady {
			issuePublisher = &prWorkspaceGitHubIssuePublisher{provider: reviewRuntime.provider}
		}
	}
	var isolatedAI prworkspace.IsolatedAIRunner
	if reviewRuntime.agent != nil {
		isolatedAI = prworkspace.WorkflowAIRunner{
			Runner: reviewRuntime.agent, AgentID: reviewRuntime.agentID,
		}
	}
	lifecycle := cfg.PRLifecycle.Effective()
	if resolver != nil {
		resolver.repositories = make(map[string]config.PRLifecycleRepositoryDescriptor, len(lifecycle.Repositories))
		for identity, descriptor := range lifecycle.Repositories {
			resolver.repositories[identity] = descriptor
		}
	}
	deferredModeForRepository := func(providerOrigin, repositoryID string) prworkspace.DeferredIssueMode {
		_, workflowConfiguration, _, resolveErr := lifecycle.WorkflowConfigurationForRepository(
			providerOrigin,
			repositoryID,
		)
		if resolveErr != nil {
			return prworkspace.DeferredIssuesOff
		}
		return prworkspace.DeferredIssueMode(workflowConfiguration.DeferredIssues.Mode)
	}
	scopeDispositionForRepository := func(providerOrigin, repositoryID string) prworkspace.ScopeDispositionPolicy {
		_, configuration, _, resolveErr := lifecycle.WorkflowConfigurationForRepository(providerOrigin, repositoryID)
		if resolveErr != nil {
			return prworkspace.DefaultScopeDispositionPolicy()
		}
		policy := prworkspace.ScopeDispositionPolicy{
			Default: prworkspace.ScopeDispositionRule{
				Mode:   prworkspace.ScopeDispositionMode(configuration.ScopeDisposition.Default.Mode),
				Prompt: configuration.ScopeDisposition.Default.Prompt,
			},
			ByType: make(map[prworkspace.PRType]prworkspace.ScopeDispositionRule),
		}
		for kind, rule := range configuration.ScopeDisposition.ByType {
			policy.ByType[prworkspace.PRType(kind)] = prworkspace.ScopeDispositionRule{
				Mode: prworkspace.ScopeDispositionMode(rule.Mode), Prompt: rule.Prompt,
			}
		}
		return policy
	}
	defaultDeferredMode := prworkspace.DeferredIssueMode(
		lifecycle.WorkflowConfigurations[lifecycle.DefaultWorkflowConfigurationID].DeferredIssues.Mode,
	)
	gateEvaluator := &prworkspace.WorkflowGateEvaluator{
		Config: lifecycle, Executor: executor,
	}
	if reviewRuntime.agentLoop != nil {
		gateEvaluator.WorkingContext = &prworkspace.SessionGateWorkingContextBinder{
			Acquire: newPRWorkspaceGateWorkingContextAcquire(cfg, reviewRuntime.agentLoop),
		}
	}
	var implementationRuntime *prWorkspaceImplementationRuntime
	if reviewRuntime.agentLoop != nil && prLocalCI != nil && prLocalCI.runner != nil &&
		reviewRuntime.agentID != "" {
		implementationRuntime, err = newPRWorkspaceImplementationRuntime(
			reviewRuntime.agentLoop,
			prLocalCI.runner,
			reviewRuntime.agentID,
			acquireRuntime,
		)
		if err != nil {
			logger.WarnSafeCF(
				logger.ComponentEventing,
				logger.DiagnosticMessageEventingPRWorkspaceImplementationIsUnavailable,
				logger.NewSafeFields(
					gatewayDiagnosticErrorField(logger.ErrorClassInternal, err),
				),
			)
			implementationRuntime = nil
		}
	}
	prWorkspaceService, err := prworkspace.NewService(prworkspace.ServiceConfig{
		Store:                          prworkspace.NewEventingStore(store),
		Provider:                       provider,
		ReviewEvidence:                 reviewEvidence,
		CandidateEvidence:              implementationRuntime,
		PlanningEvidence:               implementationRuntime,
		AI:                             isolatedAI,
		Gates:                          gateEvaluator,
		DeferredIssueMode:              defaultDeferredMode,
		DeferredIssueModeForRepository: deferredModeForRepository,
		ScopeDispositionForRepository:  scopeDispositionForRepository,
	})
	if err != nil {
		return nil, closeSetup(err)
	}
	implementation := prworkspace.ImplementationConfig{MaxCycles: 3}
	var branchPublisher prworkspace.BranchPublisher
	if implementationRuntime != nil {
		implementationRuntime.provider = reviewRuntime.provider
		implementation.Repair = implementationRuntime
		implementation.Validation = implementationRuntime
		branchPublisher = implementationRuntime
	}
	var reviewPublisher prworkspace.ReviewPublisher
	if reviewRuntime.submitter != nil && reviewRuntime.provider != nil {
		reviewPublisher = &prWorkspaceReviewPublicationRuntime{
			submitter: reviewRuntime.submitter,
			provider:  reviewRuntime.provider,
		}
	}
	reviewNudgePolicy := prworkspace.ConfiguredNudgePolicy(
		lifecycle.Nudge.ReviewMinimumAdditional,
		lifecycle.Nudge.ReviewMaximumAdditional,
	)
	completionNudgePolicy := prworkspace.ConfiguredNudgePolicy(
		lifecycle.Nudge.CompletionMinimumAdditional,
		lifecycle.Nudge.CompletionMaximumAdditional,
	)
	sizePolicy := prworkspace.SizePolicy{
		XS: prWorkspaceSizeThreshold(lifecycle.Scope.XS),
		S:  prWorkspaceSizeThreshold(lifecycle.Scope.S),
		M:  prWorkspaceSizeThreshold(lifecycle.Scope.M),
	}
	prWorkspaceHandler, err := prworkspace.NewHTTPHandler(prworkspace.HTTPConfig{
		Service:               prWorkspaceService,
		Implementation:        implementation,
		IssuePublisher:        issuePublisher,
		ReviewPublisher:       reviewPublisher,
		BranchPublisher:       branchPublisher,
		ReviewNudgePolicy:     reviewNudgePolicy,
		CompletionNudgePolicy: completionNudgePolicy,
		SizePolicy:            sizePolicy,
	})
	if err != nil {
		return nil, closeSetup(err)
	}
	publicationWorker := newPRWorkspacePublicationWorker(
		prWorkspaceService,
		issuePublisher,
		reviewPublisher,
		branchPublisher,
	)
	developmentWorker := &developmentWorkspaceWorker{
		service: prWorkspaceService, handler: prWorkspaceHandler,
	}
	notificationPushWorker := &developmentNotificationPushWorker{service: prWorkspaceService}
	operatorBackend, err := eventoperator.NewBackend(eventoperator.BackendConfig{
		Store: store, PRWorkspaces: prWorkspaceHandler,
	})
	if err != nil {
		return nil, closeSetup(err)
	}
	webhookBackend, err := newEventWebhookBackend(cfg, store)
	if err != nil {
		return nil, closeSetup(err)
	}
	channelBackend, err := newEventChannelBackend(cfg, store)
	if err != nil {
		return nil, closeSetup(err)
	}
	githubPoller, err := newGitHubNotificationPoller(
		cfg,
		store,
		reviewRuntime.notificationMCP,
		reviewRuntime.mcpArtifactRoot,
	)
	if err != nil {
		return nil, closeSetup(err)
	}

	service := &eventAutomationService{
		store: store, operatorBackend: operatorBackend,
		webhookBackend: webhookBackend, channelBackend: channelBackend,
		prWorkspaces: prWorkspaceService, githubPoller: githubPoller,
		prLocalCI: prLocalCI, done: make(chan struct{}),
	}

	workerCtx, cancel := context.WithCancel(context.Background())
	service.cancel = cancel
	ingress := config.EffectiveEventIngressConfig(cfg, workspace)

	var workers sync.WaitGroup
	workers.Add(1)
	go runEventRetentionWorker(
		workerCtx,
		&workers,
		store,
		acquireRuntime,
		ingress.RetentionDays,
		eventRetentionMaintenanceInterval,
		time.Now,
	)

	if cfg.Workflows.Enabled {
		router := &workflows.EventWorkflowRouter{
			Inbox:                store,
			WorkspaceDir:         workspace,
			DefinitionsDir:       executor.DefinitionsDir,
			RuntimeCompatibility: executor.RuntimeCompatibility,
			RuntimeEvents:        runtimeEvents,
			WorkerLabel:          "gateway-workflow-router",
		}
		dispatcher := &workflows.EventWorkflowDispatcher{
			Inbox:                store,
			Executor:             executor,
			RunStore:             runStore,
			WorkspaceDir:         workspace,
			DefinitionsDir:       executor.DefinitionsDir,
			RuntimeCompatibility: executor.RuntimeCompatibility,
			WorkerLabel:          "gateway-workflow-dispatcher",
		}

		workers.Add(2)
		go runEventAutomationWorker(
			workerCtx,
			&workers,
			"router",
			withEventAutomationRuntime(acquireRuntime, router.ProcessOne),
		)
		go runEventAutomationWorker(
			workerCtx,
			&workers,
			"dispatcher",
			withEventAutomationRuntime(acquireRuntime, dispatcher.ProcessOne),
		)
	}
	if publicationWorker != nil {
		workers.Add(1)
		go runEventAutomationWorker(
			workerCtx,
			&workers,
			"pr-workspace-publications",
			withEventAutomationRuntime(acquireRuntime, publicationWorker.ProcessOne),
		)
	}
	workers.Add(2)
	go runEventAutomationWorker(
		workerCtx,
		&workers,
		"development-workspaces",
		withEventAutomationRuntime(acquireRuntime, developmentWorker.ProcessOne),
	)
	go runEventAutomationWorker(
		workerCtx,
		&workers,
		"development-notification-push",
		notificationPushWorker.ProcessOne,
	)
	if githubPoller != nil {
		workers.Add(1)
		go runGitHubNotificationPollWorker(
			workerCtx,
			&workers,
			githubPoller,
			eventgithubpoll.DefaultInterval,
		)
	}
	go func() {
		workers.Wait()
		close(service.done)
	}()
	return service, nil
}

func prWorkspaceSizeThreshold(value config.PRLifecycleSizeThreshold) prworkspace.SizeThreshold {
	return prworkspace.SizeThreshold{
		Files: value.Files, SemanticLines: value.SemanticLines, Modules: value.Modules,
	}
}

func githubReviewSubmissionReady(
	ctx context.Context,
	agentLoop *agent.AgentLoop,
) bool {
	if agentLoop == nil {
		return false
	}
	return githubReviewSubmissionToolsReady(func(
		occurrence workflows.WorkflowDependencyOccurrence,
	) workflows.WorkflowDependencyReadinessCode {
		return agentLoop.ResolveWorkflowDependency(ctx, occurrence)
	})
}

func githubReviewProviderReadReady(
	ctx context.Context,
	agentLoop *agent.AgentLoop,
) bool {
	if agentLoop == nil {
		return false
	}
	return githubReviewProviderReadToolsReady(func(
		occurrence workflows.WorkflowDependencyOccurrence,
	) workflows.WorkflowDependencyReadinessCode {
		return agentLoop.ResolveWorkflowDependency(ctx, occurrence)
	})
}

func githubPRWorkspaceIssuePublicationReady(
	ctx context.Context,
	agentLoop *agent.AgentLoop,
) bool {
	if agentLoop == nil {
		return false
	}
	return githubPRWorkspaceIssuePublicationToolsReady(func(
		occurrence workflows.WorkflowDependencyOccurrence,
	) workflows.WorkflowDependencyReadinessCode {
		return agentLoop.ResolveWorkflowDependency(ctx, occurrence)
	})
}

func githubDevelopmentPullCreationReady(ctx context.Context, agentLoop *agent.AgentLoop) bool {
	if agentLoop == nil {
		return false
	}
	for _, tool := range []string{
		reviews.GitHubCreatePullRequestTool,
		reviews.GitHubListPullRequestsTool,
	} {
		if agentLoop.ResolveWorkflowDependency(ctx, workflows.WorkflowDependencyOccurrence{
			Kind: workflows.WorkflowDependencyKindMCP,
			Name: reviews.DefaultGitHubMCPServer + "/" + tool,
		}) != workflows.WorkflowDependencyReadinessReady {
			return false
		}
	}
	return true
}

func githubDevelopmentProviderReadReady(ctx context.Context, agentLoop *agent.AgentLoop) bool {
	if agentLoop == nil {
		return false
	}
	for _, tool := range []string{
		reviews.GitHubGetMeTool,
		reviews.GitHubSearchRepositoriesTool,
		reviews.GitHubListCommitsTool,
	} {
		if agentLoop.ResolveWorkflowDependency(ctx, workflows.WorkflowDependencyOccurrence{
			Kind: workflows.WorkflowDependencyKindMCP,
			Name: reviews.DefaultGitHubMCPServer + "/" + tool,
		}) != workflows.WorkflowDependencyReadinessReady {
			return false
		}
	}
	return true
}

func githubPRWorkspaceIssuePublicationToolsReady(
	resolve func(
		workflows.WorkflowDependencyOccurrence,
	) workflows.WorkflowDependencyReadinessCode,
) bool {
	if resolve == nil {
		return false
	}
	for _, tool := range []string{
		reviews.GitHubIssueWriteTool,
		reviews.GitHubSearchIssuesTool,
	} {
		if resolve(workflows.WorkflowDependencyOccurrence{
			Kind: workflows.WorkflowDependencyKindMCP,
			Name: reviews.DefaultGitHubMCPServer + "/" + tool,
		}) != workflows.WorkflowDependencyReadinessReady {
			return false
		}
	}
	return true
}

func githubReviewProviderReadToolsReady(
	resolve func(
		workflows.WorkflowDependencyOccurrence,
	) workflows.WorkflowDependencyReadinessCode,
) bool {
	if resolve == nil {
		return false
	}
	for _, tool := range []string{
		reviews.GitHubPullRequestReadTool,
		reviews.GitHubGetMeTool,
		reviews.GitHubSearchRepositoriesTool,
	} {
		if resolve(workflows.WorkflowDependencyOccurrence{
			Kind: workflows.WorkflowDependencyKindMCP,
			Name: reviews.DefaultGitHubMCPServer + "/" + tool,
		}) != workflows.WorkflowDependencyReadinessReady {
			return false
		}
	}
	return true
}

func githubReviewSubmissionToolsReady(
	resolve func(
		workflows.WorkflowDependencyOccurrence,
	) workflows.WorkflowDependencyReadinessCode,
) bool {
	if resolve == nil {
		return false
	}
	for _, tool := range []string{
		reviews.GitHubPullRequestReadTool,
		reviews.GitHubPullRequestReviewWriteTool,
		reviews.GitHubPendingReviewCommentTool,
	} {
		readiness := resolve(
			workflows.WorkflowDependencyOccurrence{
				Kind: workflows.WorkflowDependencyKindMCP,
				Name: reviews.DefaultGitHubMCPServer + "/" + tool,
			},
		)
		if readiness != workflows.WorkflowDependencyReadinessReady {
			return false
		}
	}
	return true
}

func githubNotificationPollingEnabled(cfg *config.Config) bool {
	if cfg == nil || !cfg.Events.Ingress.Enabled {
		return false
	}
	for _, connector := range cfg.Events.Ingress.Webhooks {
		if connector.Enabled &&
			connector.PollNotifications &&
			config.EffectiveEventWebhookFormat(connector) ==
				config.EventWebhookFormatGitHub {
			return true
		}
	}
	return false
}

func validateGitHubNotificationPollingRuntime(
	ctx context.Context,
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
) error {
	if !githubNotificationPollingEnabled(cfg) {
		return nil
	}
	if agentLoop == nil {
		return errors.New(
			"GitHub notification polling requires the agent MCP runtime",
		)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for _, tool := range []string{
		eventgithubpoll.ListNotificationsTool,
		eventgithubpoll.PullRequestReadTool,
	} {
		readiness := agentLoop.ResolveWorkflowDependency(
			ctx,
			workflows.WorkflowDependencyOccurrence{
				Kind: workflows.WorkflowDependencyKindMCP,
				Name: eventgithubpoll.DefaultMCPServer + "/" + tool,
			},
		)
		if readiness != workflows.WorkflowDependencyReadinessReady {
			return fmt.Errorf(
				"GitHub notification polling requires ready eager MCP tool %q (readiness %q)",
				eventgithubpoll.DefaultMCPServer+"/"+tool,
				readiness,
			)
		}
	}
	return nil
}

func validateEventAutomationRuntime(
	ctx context.Context,
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
) error {
	if cfg == nil || !cfg.Events.Ingress.Enabled {
		return nil
	}
	if cfg.Workflows.Enabled {
		if _, err := agent.NewEventWorkflowExecutor(ctx, agentLoop); err != nil {
			return fmt.Errorf("initialize event workflow runtime: %w", err)
		}
	}
	return validateGitHubNotificationPollingRuntime(ctx, cfg, agentLoop)
}

func newGitHubNotificationPoller(
	cfg *config.Config,
	store eventgithubpoll.Inserter,
	toolRunner workflows.ToolRunner,
	artifactRoot string,
) (*eventgithubpoll.Poller, error) {
	if !githubNotificationPollingEnabled(cfg) {
		return nil, nil
	}
	if store == nil {
		return nil, errors.New(
			"GitHub notification polling requires the durable event store",
		)
	}
	if toolRunner == nil {
		return nil, errors.New(
			"GitHub notification polling requires the dynamic MCP tool runner",
		)
	}
	ingress := config.EffectiveEventIngressConfig(cfg, cfg.WorkspacePath())
	connectors := make([]eventgithubpoll.Connector, 0, len(ingress.Webhooks))
	names := make([]string, 0, len(ingress.Webhooks))
	for name := range ingress.Webhooks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		connector := ingress.Webhooks[name]
		if !connector.Enabled ||
			!connector.PollNotifications ||
			config.EffectiveEventWebhookFormat(connector) !=
				config.EventWebhookFormatGitHub {
			continue
		}
		connectors = append(connectors, eventgithubpoll.Connector{
			Name:         name,
			Repositories: append([]string(nil), connector.Repositories...),
			TargetUser:   connector.TargetUser,
		})
	}
	poller, err := eventgithubpoll.New(eventgithubpoll.Config{
		Store:        store,
		ToolRunner:   toolRunner,
		Connectors:   connectors,
		ArtifactRoot: effectiveGitHubMCPArtifactRoot(cfg, artifactRoot),
	})
	if err != nil {
		return nil, fmt.Errorf("prepare GitHub notification polling: %w", err)
	}
	return poller, nil
}

func githubMCPArtifactRoot(cfg *config.Config, agentLoop *agent.AgentLoop) string {
	workspace := ""
	if cfg != nil {
		workspace = strings.TrimSpace(cfg.WorkspacePath())
	}
	if agentLoop != nil {
		if registry := agentLoop.GetRegistry(); registry != nil {
			if defaultAgent := registry.GetDefaultAgent(); defaultAgent != nil {
				if resolved := strings.TrimSpace(defaultAgent.Workspace); resolved != "" {
					workspace = resolved
				}
			}
		}
	}
	if workspace == "" {
		return ""
	}
	return filepath.Join(workspace, ".artifacts", "mcp")
}

func effectiveGitHubMCPArtifactRoot(cfg *config.Config, artifactRoot string) string {
	if resolved := strings.TrimSpace(artifactRoot); resolved != "" {
		return resolved
	}
	return githubMCPArtifactRoot(cfg, nil)
}

func withEventAutomationRuntime(
	acquire eventAutomationRuntimeAcquire,
	process func(context.Context) (bool, error),
) func(context.Context) (bool, error) {
	if acquire == nil {
		return process
	}
	return func(ctx context.Context) (bool, error) {
		leaseCtx, releaseRuntime, err := acquire(ctx)
		if err != nil {
			return false, err
		}
		defer releaseRuntime()
		return process(leaseCtx)
	}
}

func openEventAutomationStore(ctx context.Context, cfg *config.Config) (*eventing.Store, error) {
	if cfg == nil || !cfg.Events.Ingress.Enabled {
		return nil, nil
	}
	if err := cfg.Events.Ingress.Validate(); err != nil {
		return nil, fmt.Errorf("validate event ingress: %w", err)
	}
	if err := cfg.Events.Ingress.ValidateEventChannelAdapters(
		cfg.Channels,
		cfg.SensitiveDataValues()...,
	); err != nil {
		return nil, fmt.Errorf("validate event channel adapters: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	workspace := cfg.WorkspacePath()
	ingress := config.EffectiveEventIngressConfig(cfg, workspace)
	store, err := eventing.Open(
		ctx,
		ingress.DatabasePath,
		eventing.WithMaxPayloadBytes(ingress.MaxPayloadBytes),
		eventing.WithRedaction(
			ingress.RedactFields,
			eventRedactionSecretValues(cfg, ingress),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("open durable event inbox: %w", err)
	}
	return store, nil
}

func newEventWebhookBackend(
	cfg *config.Config,
	store eventwebhook.Inserter,
) (*eventwebhook.Backend, error) {
	if cfg == nil || !cfg.Events.Ingress.Enabled || store == nil {
		return nil, nil
	}
	ingress := config.EffectiveEventIngressConfig(cfg, cfg.WorkspacePath())
	secrets := make(map[string]string)
	formats := make(map[string]string)
	repositories := make(map[string][]string)
	targetUsers := make(map[string]string)
	for name, connector := range ingress.Webhooks {
		if !connector.Enabled {
			continue
		}
		secret := connector.Secret.String()
		if secret == "" {
			continue
		}
		secrets[name] = secret
		formats[name] = config.EffectiveEventWebhookFormat(connector)
		if len(connector.Repositories) > 0 {
			repositories[name] = append([]string(nil), connector.Repositories...)
		}
		if connector.TargetUser != "" {
			targetUsers[name] = connector.TargetUser
		}
	}
	if len(secrets) == 0 {
		return nil, nil
	}
	backend, err := eventwebhook.NewBackend(eventwebhook.BackendConfig{
		Store:                 store,
		ConnectorSecrets:      secrets,
		ConnectorFormats:      formats,
		ConnectorRepositories: repositories,
		ConnectorTargetUsers:  targetUsers,
		MaxPayloadBytes:       ingress.MaxPayloadBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("prepare generic event webhook ingress: %w", err)
	}
	return backend, nil
}

func eventWebhookSecretValues(ingress config.EventIngressConfig) []string {
	secrets := make([]string, 0, len(ingress.Webhooks))
	for _, connector := range ingress.Webhooks {
		if !connector.Enabled {
			continue
		}
		if secret := connector.Secret.String(); secret != "" {
			secrets = append(secrets, secret)
		}
	}
	return secrets
}

func eventRedactionSecretValues(
	cfg *config.Config,
	ingress config.EventIngressConfig,
) []string {
	secrets := eventWebhookSecretValues(ingress)
	seen := make(map[string]struct{}, len(secrets))
	for _, secret := range secrets {
		seen[secret] = struct{}{}
	}
	if cfg == nil {
		return secrets
	}
	for _, secret := range cfg.SensitiveDataValues() {
		// Very short credentials would redact common characters throughout
		// otherwise-safe message text. Match the existing LLM-boundary policy:
		// keys and tokens longer than three characters are exact-value secrets.
		if len(secret) <= 3 {
			continue
		}
		if _, exists := seen[secret]; exists {
			continue
		}
		seen[secret] = struct{}{}
		secrets = append(secrets, secret)
	}
	return secrets
}

// validateEventAutomationStorage proves the prospective durable inbox can be
// opened before a hot reload stops or commits the currently running services.
func validateEventAutomationStorage(ctx context.Context, cfg *config.Config) error {
	store, err := openEventAutomationStore(ctx, cfg)
	if err != nil {
		return err
	}
	if store == nil {
		return nil
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("close validated durable event inbox: %w", err)
	}
	return nil
}

func runEventRetentionWorker(
	ctx context.Context,
	workers *sync.WaitGroup,
	pruner eventRetentionPruner,
	acquireRuntime eventAutomationRuntimeAcquire,
	retentionDays int,
	interval time.Duration,
	now func() time.Time,
) {
	defer workers.Done()
	if interval <= 0 {
		interval = eventRetentionMaintenanceInterval
	}
	if now == nil {
		now = time.Now
	}

	runMaintenance := func() {
		maintenanceCtx := ctx
		releaseRuntime := func() {}
		var err error
		if acquireRuntime != nil {
			maintenanceCtx, releaseRuntime, err = acquireRuntime(ctx)
			if err != nil {
				if ctx.Err() == nil {
					logger.WarnSafeCF(
						logger.ComponentEventing,
						logger.DiagnosticMessageEventingEventRetentionMaintenanceFailed,
						logger.NewSafeFields(
							gatewayDiagnosticErrorField(logger.ErrorClassInternal, err),
						),
					)
				}
				return
			}
		}
		defer releaseRuntime()

		pruned, err := pruneExpiredEvents(
			maintenanceCtx,
			pruner,
			retentionDays,
			now,
		)
		if err != nil {
			if ctx.Err() == nil {
				logger.WarnSafeCF(
					logger.ComponentEventing,
					logger.DiagnosticMessageEventingEventRetentionMaintenanceFailed,
					logger.NewSafeFields(
						gatewayDiagnosticErrorField(logger.ErrorClassInternal, err),
					),
				)
			}
			return
		}
		if pruned > 0 {
			logger.DebugSafeCF(
				logger.ComponentEventing,
				logger.DiagnosticMessageEventingPrunedExpiredDurableEvents,
				logger.NewSafeFields(
					logger.SafeInt(logger.FieldCount, int(pruned)),
				),
			)
		}
	}

	// Run once immediately so expiration does not depend on process uptime or
	// the time of day at which the gateway happened to start.
	runMaintenance()
	if ctx.Err() != nil {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runMaintenance()
		}
	}
}

func runGitHubNotificationPollWorker(
	ctx context.Context,
	workers *sync.WaitGroup,
	poller *eventgithubpoll.Poller,
	interval time.Duration,
) {
	defer workers.Done()
	if poller == nil {
		return
	}
	if interval <= 0 {
		interval = eventgithubpoll.DefaultInterval
	}
	poll := func() {
		result, err := poller.Poll(ctx)
		if err != nil {
			if ctx.Err() == nil {
				logger.WarnSafeCF(
					logger.ComponentEventing,
					logger.DiagnosticMessageEventingGitHubNotificationPollingFailed,
					logger.NewSafeFields(
						gatewayDiagnosticErrorField(logger.ErrorClassInternal, err),
					),
				)
			}
			return
		}
		if result.Inserted > 0 {
			logger.DebugSafeCF(
				logger.ComponentEventing,
				logger.DiagnosticMessageEventingStoredGitHubNotifications,
				logger.NewSafeFields(
					logger.SafeInt(logger.FieldNotificationCount, result.Notifications),
					logger.SafeInt(logger.FieldMatchedCount, result.Matched),
					logger.SafeInt(logger.FieldInsertedCount, result.Inserted),
				),
			)
		}
	}

	// Poll once at startup, then wait one complete interval after each scan.
	// Slow scans cannot accumulate ticker ticks into a provider retry spin.
	poll()
	if ctx.Err() != nil {
		return
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			poll()
			timer.Reset(interval)
		}
	}
}

func pruneExpiredEvents(
	ctx context.Context,
	pruner eventRetentionPruner,
	retentionDays int,
	now func() time.Time,
) (int64, error) {
	if pruner == nil {
		return 0, fmt.Errorf("event retention store is required")
	}
	if retentionDays <= 0 {
		return 0, fmt.Errorf("positive event retention days are required")
	}
	if now == nil {
		now = time.Now
	}

	cutoff, applicable, err := durableEventRetentionCutoff(
		now().UTC(),
		retentionDays,
	)
	if err != nil {
		return 0, err
	}
	if !applicable {
		// Nothing stored in a signed Unix-nanosecond column can be old enough
		// to expire. Treat this as a safe no-op rather than allowing calendar
		// arithmetic to wrap into a destructive future cutoff.
		return 0, nil
	}

	var total int64
	for range eventRetentionMaxBatchesPerCycle {
		count, err := pruner.Prune(ctx, cutoff, eventRetentionPruneBatchSize)
		total += count
		if err != nil {
			return total, err
		}
		if count < eventRetentionPruneBatchSize {
			break
		}
	}
	if notificationPruner, ok := pruner.(interface {
		PruneDevelopmentNotifications(ctx context.Context, cutoff time.Time, limit int) (int64, error)
	}); ok {
		cutoff := now().UTC().Add(-90 * 24 * time.Hour)
		for range eventRetentionMaxBatchesPerCycle {
			count, pruneErr := notificationPruner.PruneDevelopmentNotifications(
				ctx,
				cutoff,
				eventRetentionPruneBatchSize,
			)
			if pruneErr != nil {
				return total, pruneErr
			}
			if count < eventRetentionPruneBatchSize {
				break
			}
		}
	}
	return total, nil
}

func durableEventRetentionCutoff(
	now time.Time,
	retentionDays int,
) (time.Time, bool, error) {
	if retentionDays <= 0 {
		return time.Time{}, false, fmt.Errorf(
			"positive event retention days are required",
		)
	}
	if !isDurableEventTimestamp(now) {
		return time.Time{}, false, fmt.Errorf(
			"event retention clock is outside the durable nanosecond range",
		)
	}
	if retentionDays > eventRetentionMaxDurableDays {
		return time.Time{}, false, nil
	}

	cutoff := now.AddDate(0, 0, -retentionDays)
	if cutoff.After(now) || !isDurableEventTimestamp(cutoff) {
		return time.Time{}, false, nil
	}
	return cutoff, true, nil
}

func isDurableEventTimestamp(value time.Time) bool {
	encoded := value.UnixNano()
	return !value.IsZero() && value.Equal(time.Unix(0, encoded))
}

func runEventAutomationWorker(
	ctx context.Context,
	workers *sync.WaitGroup,
	name string,
	process func(context.Context) (bool, error),
) {
	defer workers.Done()
	for {
		processed, err := process(ctx)
		if err != nil && ctx.Err() == nil {
			logger.WarnSafeCF(
				logger.ComponentEventing,
				logger.DiagnosticMessageEventingEventWorkflowWorkerIterationFailed,
				logger.NewSafeFields(
					gatewayDiagnosticWorkerField(name),
					gatewayDiagnosticErrorField(logger.ErrorClassInternal, err),
				),
			)
		}
		if ctx.Err() != nil {
			return
		}
		if processed && err == nil {
			continue
		}
		timer := time.NewTimer(workflows.DefaultEventWorkerPollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

// Close stops and drains workers before closing the inbox. A timed-out call
// can be retried; the store is never closed while workers may still use it.
func (s *eventAutomationService) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
	})
	select {
	case <-s.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.closeOnce.Do(func() {
		var ciErr error
		if s.prLocalCI != nil {
			ciErr = s.prLocalCI.Close()
		}
		s.closeErr = errors.Join(ciErr, s.store.Close())
	})
	return s.closeErr
}

func closeEventAutomationService(
	ctx context.Context,
	service **eventAutomationService,
) error {
	if service == nil || *service == nil {
		return nil
	}
	err := (*service).Close(ctx)
	if err == nil {
		*service = nil
	}
	return err
}
