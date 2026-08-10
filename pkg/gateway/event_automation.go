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
	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/eventing"
	eventchannel "github.com/sipeed/picoclaw/pkg/eventing/channelmessage"
	eventgithubpoll "github.com/sipeed/picoclaw/pkg/eventing/githubpoll"
	eventoperator "github.com/sipeed/picoclaw/pkg/eventing/operator"
	eventwebhook "github.com/sipeed/picoclaw/pkg/eventing/webhook"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/prdevelopment"
	"github.com/sipeed/picoclaw/pkg/prdevelopment/localci"
	"github.com/sipeed/picoclaw/pkg/reviews"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	eventRetentionMaintenanceInterval = 6 * time.Hour
	eventRetentionPruneBatchSize      = 500
	eventRetentionMaxBatchesPerCycle  = 20
	prDevelopmentContextCompactorID   = "gateway-pr-development-ledger-compactor-v1"
	// Signed Unix nanoseconds span 213,503 complete UTC days. A longer
	// retention period cannot expire any timestamp representable by the event
	// store and must be handled before time.AddDate can overflow internally.
	eventRetentionMaxDurableDays = 213_503
)

type eventAutomationService struct {
	store                  *eventing.Store
	operatorBackend        *eventoperator.Backend
	webhookBackend         *eventwebhook.Backend
	channelBackend         *eventchannel.Backend
	reviewService          *reviews.Service
	prDevelopment          *prdevelopment.Service
	reviewAttention        *reviews.AttentionLauncher
	prDevelopmentAttention *prdevelopment.AttentionLauncher
	reviewBridge           *reviews.AttentionBridge
	prDevelopmentBridge    *prdevelopment.AttentionBridge
	githubPoller           *eventgithubpoll.Poller
	prLocalCI              *prDevelopmentLocalCIRuntime
	cancel                 context.CancelFunc
	done                   chan struct{}

	stopOnce  sync.Once
	closeOnce sync.Once
	closeErr  error
}

type eventAutomationRuntimeAcquire func(context.Context) (context.Context, func(), error)

type prDevelopmentRepairReadiness interface {
	ResolveWorkflowDependency(
		ctx context.Context,
		occurrence workflows.WorkflowDependencyOccurrence,
	) workflows.WorkflowDependencyReadinessCode
}

type eventRetentionPruner interface {
	Prune(ctx context.Context, before time.Time, limit int) (int64, error)
}

type eventReviewRuntime struct {
	agent                                workflows.AgentRunner
	agentID                              string
	repairAgentReady                     func(agentID string) bool
	repairVerifier                       prdevelopment.RepairCaseVerifier
	repairRuntime                        prdevelopment.RepairRuntimeFactory
	repairWorkspaces                     prdevelopment.RepairControllerWorkspaceFactory
	repairLocalCI                        *prDevelopmentLocalCIRuntime
	repairControllerProcess              func(context.Context) (bool, error)
	recoveryWorkspaces                   prdevelopment.ControllerRecoveryWorkspaceFactory
	recoveryProcess                      func(context.Context) (bool, error)
	reviewRuntime                        prdevelopment.ReviewRuntimeFactory
	reviewWorkspaces                     prdevelopment.ReviewControllerWorkspaceFactory
	reviewProcess                        func(context.Context) (bool, error)
	acquireWorkingContextRuntime         reviews.WorkingContextRuntimeAcquire
	prDevelopmentAttentionPolicies       sharedattention.PolicySource
	prDevelopmentAttentionWorkspaces     prdevelopment.AttentionReviewWorkspaceFactory
	acquirePRDevelopmentAttentionRuntime prdevelopment.AttentionContextRuntimeAcquire
	prDevelopmentAttentionProcess        func(context.Context) (bool, error)
	submitter                            reviews.Submitter
	attentionPolicies                    reviews.AttentionPolicySource
	notificationMCP                      workflows.ToolRunner
	mcpArtifactRoot                      string
}

func newEventReviewRuntime(
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
	attentionPolicies reviews.AttentionPolicySource,
) eventReviewRuntime {
	runtime := eventReviewRuntime{attentionPolicies: attentionPolicies}
	if shared, ok := attentionPolicies.(sharedattention.PolicySource); ok {
		runtime.prDevelopmentAttentionPolicies = shared
	}
	if agentLoop != nil {
		runtime.mcpArtifactRoot = githubMCPArtifactRoot(cfg, agentLoop)
		// Recovery owns only provider-independent, replayable local Git effects.
		// Always compose it when a runtime generation can resolve the controller
		// workspace manager, regardless of model, provider, CI, or review readiness.
		runtime.recoveryWorkspaces = agentLoop.ControllerGitWorkspaceManager
	}
	return runtime
}

func setupEventAutomationService(
	ctx context.Context,
	cfg *config.Config,
	agentLoop *agent.AgentLoop,
) (*eventAutomationService, error) {
	if cfg == nil || !cfg.Events.Ingress.Enabled {
		return nil, nil
	}
	var attentionPolicies reviews.AttentionPolicySource
	if cfg.Workflows.Enabled {
		configured, err := configuredReviewAttentionPolicySource(cfg)
		if err != nil {
			return nil, err
		}
		if err = validateConfiguredReviewAttentionAgents(configured, agentLoop); err != nil {
			return nil, err
		}
		attentionPolicies = configured
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
	reviewRuntime := newEventReviewRuntime(cfg, agentLoop, attentionPolicies)
	if agentLoop != nil {
		reviewRuntime.agent = agent.NewWorkflowAgentRunner(agentLoop)
		defaultAgent, err := defaultReviewRuntimeAgent(agentLoop)
		if err != nil {
			return nil, err
		}
		reviewRuntime.agentID = defaultAgent.ID
		reviewRuntime.acquireWorkingContextRuntime = newReviewWorkingContextRuntimeAcquire(cfg, agentLoop)
		reviewRuntime.prDevelopmentAttentionWorkspaces = newPRDevelopmentAttentionWorkspaceFactory(
			cfg,
			agentLoop,
		)
		reviewRuntime.acquirePRDevelopmentAttentionRuntime = newPRDevelopmentAttentionRuntimeAcquire(
			cfg,
			agentLoop,
		)
		// A parked candidate is reviewed entirely from durable local evidence and
		// its retained branch. Keep this composition independent of provider MCP
		// readiness so an already-pending review can finish during an outage.
		reviewRuntime.reviewRuntime = func(
			agentID string,
		) (prdevelopment.LocalReviewExecutor, error) {
			return agentLoop.NewControllerLocalReviewRunner(agentID)
		}
		reviewRuntime.reviewWorkspaces = agentLoop.ControllerGitWorkspaceManager
		if executor != nil && githubPRDevelopmentRepairReady(ctx, agentLoop) {
			reviewRuntime.repairAgentReady = combinedPRDevelopmentAgentReadiness(
				agentLoop.ControllerLocalRepairReady,
				agentLoop.ControllerLocalReviewReady,
			)
			reviewRuntime.repairVerifier = &prdevelopment.GitHubVerifier{
				Runner: executor.Tools,
				ArtifactRoot: effectiveGitHubMCPArtifactRoot(
					cfg,
					reviewRuntime.mcpArtifactRoot,
				),
			}
			reviewRuntime.repairRuntime = func(
				agentID string,
				routingText string,
			) (prdevelopment.LocalRepairExecutor, error) {
				return agentLoop.NewControllerLocalRepairRunner(agentID, routingText)
			}
			reviewRuntime.repairWorkspaces = agentLoop.ControllerGitWorkspaceManager
		}
		pollNotifications := githubNotificationPollingEnabled(cfg)
		reviewSubmission := githubReviewSubmissionReady(ctx, agentLoop)
		if pollNotifications {
			if err := validateGitHubNotificationPollingRuntime(
				ctx,
				cfg,
				agentLoop,
			); err != nil {
				return nil, err
			}
		}
		if pollNotifications || reviewSubmission {
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
		}
	} else if githubNotificationPollingEnabled(cfg) {
		return nil, errors.New(
			"GitHub notification polling requires the agent MCP runtime",
		)
	}
	return newEventAutomationServiceWithReviews(
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
	return newEventAutomationServiceWithReviews(
		ctx,
		cfg,
		executor,
		runtimeEvents,
		acquireRuntime,
		reviewRuntime,
	)
}

func newEventAutomationServiceWithReviews(
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
	if cfg.Workflows.Enabled && reviewRuntime.attentionPolicies == nil {
		configured, policyErr := configuredReviewAttentionPolicySource(cfg)
		if policyErr != nil {
			return nil, policyErr
		}
		reviewRuntime.attentionPolicies = configured
	}
	if cfg.Workflows.Enabled && reviewRuntime.prDevelopmentAttentionPolicies == nil {
		if shared, ok := reviewRuntime.attentionPolicies.(sharedattention.PolicySource); ok {
			reviewRuntime.prDevelopmentAttentionPolicies = shared
		}
	}

	workspace := cfg.WorkspacePath()
	store, err := openEventAutomationStore(ctx, cfg)
	if err != nil {
		return nil, err
	}
	prLocalCI := reviewRuntime.repairLocalCI
	controllerRuntimeReady := reviewRuntime.repairVerifier != nil &&
		reviewRuntime.repairRuntime != nil &&
		reviewRuntime.repairAgentReady != nil &&
		reviewRuntime.repairWorkspaces != nil &&
		reviewRuntime.agent != nil && reviewRuntime.agentID != ""
	reviewRuntimeConfigured := reviewRuntime.reviewRuntime != nil &&
		reviewRuntime.reviewWorkspaces != nil && reviewRuntime.agent != nil
	prDevelopmentAttentionRuntimeReady := cfg.Workflows.Enabled && executor != nil &&
		reviewRuntime.prDevelopmentAttentionPolicies != nil &&
		reviewRuntime.prDevelopmentAttentionWorkspaces != nil &&
		reviewRuntime.acquirePRDevelopmentAttentionRuntime != nil
	if prLocalCI == nil && prDevelopmentAttentionRuntimeReady &&
		!controllerRuntimeReady && !reviewRuntimeConfigured {
		prLocalCI, err = newPRDevelopmentLocalCIEvidenceRuntime(cfg)
		if err != nil {
			logger.WarnCF(
				"eventing",
				"PR development attention evidence is unavailable",
				map[string]any{"error": err.Error()},
			)
			prLocalCI = nil
		}
	} else if prLocalCI == nil && (controllerRuntimeReady || reviewRuntimeConfigured) {
		prLocalCI, err = newPRDevelopmentLocalCIRuntime(cfg)
		if err != nil {
			logger.WarnCF("eventing", "PR development local CI is unavailable", map[string]any{
				"error": err.Error(),
			})
			prLocalCI = nil
			if reviewRuntimeConfigured || prDevelopmentAttentionRuntimeReady {
				prLocalCI, err = newPRDevelopmentLocalCIEvidenceRuntime(cfg)
				if err != nil {
					logger.WarnCF(
						"eventing",
						"PR development local review evidence is unavailable",
						map[string]any{"error": err.Error()},
					)
					prLocalCI = nil
				}
			}
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

	reviewService, err := reviews.NewService(reviews.ServiceConfig{
		Store:                        store,
		Agent:                        reviewRuntime.agent,
		AgentID:                      reviewRuntime.agentID,
		Submitter:                    reviewRuntime.submitter,
		AcquireWorkingContextRuntime: reviewRuntime.acquireWorkingContextRuntime,
	})
	if err != nil {
		return nil, closeSetup(err)
	}
	reviewRuntimeReady := reviewRuntimeConfigured && prLocalCI != nil &&
		prLocalCI.evidence != nil
	repairEnabled := controllerRuntimeReady && reviewRuntimeReady &&
		prLocalCI.runner != nil
	prDevelopmentService, err := prdevelopment.NewService(prdevelopment.ServiceConfig{
		Store:            store,
		RepairStore:      store,
		RepairEnabled:    repairEnabled,
		RepairAgentReady: reviewRuntime.repairAgentReady,
		Agent:            reviewRuntime.agent,
		AgentID:          reviewRuntime.agentID,
	})
	if err != nil {
		return nil, closeSetup(err)
	}
	var prDevelopmentAttention *prdevelopment.AttentionLauncher
	if prDevelopmentAttentionRuntimeReady && prLocalCI != nil && prLocalCI.evidence != nil {
		prDevelopmentAttention, err = prdevelopment.NewAttentionLauncher(
			prdevelopment.AttentionLauncherConfig{
				Store:          store,
				Executor:       executor,
				Runs:           runStore,
				Policies:       reviewRuntime.prDevelopmentAttentionPolicies,
				Evidence:       prLocalCI.evidence,
				Workspaces:     reviewRuntime.prDevelopmentAttentionWorkspaces,
				AcquireRuntime: reviewRuntime.acquirePRDevelopmentAttentionRuntime,
			},
		)
		if err != nil {
			return nil, closeSetup(err)
		}
	}
	var reviewAttention *reviews.AttentionLauncher
	if cfg.Workflows.Enabled && reviewRuntime.attentionPolicies != nil {
		reviewAttention, err = reviews.NewAttentionLauncher(reviews.AttentionLauncherConfig{
			Service:  reviewService,
			Executor: executor,
			Policies: reviewRuntime.attentionPolicies,
		})
		if err != nil {
			return nil, closeSetup(err)
		}
	}
	var reviewBridgeExecutor *workflows.Executor
	if cfg.Workflows.Enabled {
		reviewBridgeExecutor = executor
	}
	reviewBridge, err := reviews.NewAttentionBridge(reviews.AttentionBridgeConfig{
		Service:  reviewService,
		Executor: reviewBridgeExecutor,
		RunStore: runStore,
	})
	if err != nil {
		return nil, closeSetup(err)
	}
	prDevelopmentBridge, err := prdevelopment.NewAttentionBridge(
		prdevelopment.AttentionBridgeConfig{
			Service:  prDevelopmentService,
			Executor: reviewBridgeExecutor,
			RunStore: runStore,
		},
	)
	if err != nil {
		return nil, closeSetup(err)
	}
	operatorBackend, err := eventoperator.NewBackend(eventoperator.BackendConfig{
		Store: store,
		Reviews: &reviews.Handler{
			Service:   reviewService,
			Attention: reviewBridge,
		},
		PRDevelopment: &prdevelopment.Handler{
			Service:   prDevelopmentService,
			Attention: prDevelopmentBridge,
		},
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
	var localCIRunner *localci.Runner
	if prLocalCI != nil {
		localCIRunner = prLocalCI.runner
	}
	repairController, err := prdevelopment.NewRepairControllerWorker(
		prdevelopment.RepairControllerWorkerConfig{
			Store:              store,
			Verifier:           reviewRuntime.repairVerifier,
			Runtime:            reviewRuntime.repairRuntime,
			Workspaces:         reviewRuntime.repairWorkspaces,
			LocalCI:            localCIRunner,
			ContextAgent:       reviewRuntime.agent,
			ContextCompactorID: prDevelopmentContextCompactorID,
			WorkerLabel:        "gateway-pr-development-controller",
		},
	)
	if err != nil {
		return nil, closeSetup(err)
	}
	repairControllerProcess := repairController.ProcessOne
	if reviewRuntime.repairControllerProcess != nil {
		repairControllerProcess = reviewRuntime.repairControllerProcess
	}
	var recoveryProcess func(context.Context) (bool, error)
	if reviewRuntime.recoveryProcess != nil {
		// This process-shaped seam verifies generation and shutdown ownership
		// without weakening the production worker's narrow store/Git boundary.
		recoveryProcess = reviewRuntime.recoveryProcess
	} else if reviewRuntime.recoveryWorkspaces != nil {
		recoveryWorker, recoveryErr := prdevelopment.NewControllerRecoveryWorker(
			prdevelopment.ControllerRecoveryWorkerConfig{
				Store:       store,
				Workspaces:  reviewRuntime.recoveryWorkspaces,
				WorkerLabel: "gateway-pr-development-recovery",
			},
		)
		if recoveryErr != nil {
			return nil, closeSetup(recoveryErr)
		}
		recoveryProcess = recoveryWorker.ProcessOne
	}
	var localReviewProcess func(context.Context) (bool, error)
	if reviewRuntime.reviewProcess != nil {
		// This seam is intentionally process-shaped: tests can prove generation
		// and shutdown ownership without supplying production storage or model
		// implementations.
		localReviewProcess = reviewRuntime.reviewProcess
	} else if reviewRuntimeReady {
		localReview, reviewErr := prdevelopment.NewReviewWorker(
			prdevelopment.ReviewWorkerConfig{
				Store:              store,
				Workspaces:         reviewRuntime.reviewWorkspaces,
				Evidence:           prLocalCI.evidence,
				ContextAgent:       reviewRuntime.agent,
				ContextCompactorID: prDevelopmentContextCompactorID,
				Runtime:            reviewRuntime.reviewRuntime,
				WorkerLabel:        "gateway-pr-development-review",
			},
		)
		if reviewErr != nil {
			return nil, closeSetup(reviewErr)
		}
		localReviewProcess = localReview.ProcessOne
	}

	service := &eventAutomationService{
		store:                  store,
		operatorBackend:        operatorBackend,
		webhookBackend:         webhookBackend,
		channelBackend:         channelBackend,
		reviewService:          reviewService,
		prDevelopment:          prDevelopmentService,
		reviewAttention:        reviewAttention,
		prDevelopmentAttention: prDevelopmentAttention,
		reviewBridge:           reviewBridge,
		prDevelopmentBridge:    prDevelopmentBridge,
		githubPoller:           githubPoller,
		prLocalCI:              prLocalCI,
		done:                   make(chan struct{}),
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
			SucceededRunSink: workflows.SucceededEventRunSinkFanout{
				&reviews.CaptureSink{Store: store},
				&prdevelopment.CaptureSink{
					Store: store,
					Verifier: &prdevelopment.GitHubVerifier{
						Runner: executor.Tools,
						ArtifactRoot: effectiveGitHubMCPArtifactRoot(
							cfg,
							reviewRuntime.mcpArtifactRoot,
						),
					},
				},
			},
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
	if reviewRuntime.submitter != nil {
		submitter := &reviews.SubmissionWorker{
			Queue:       store,
			Submitter:   reviewRuntime.submitter,
			WorkerLabel: "gateway-review-submitter",
		}
		workers.Add(1)
		go runEventAutomationWorker(
			workerCtx,
			&workers,
			"review submitter",
			withEventAutomationRuntime(acquireRuntime, submitter.ProcessOne),
		)
	}
	// Recovery is a separate provider-independent loop so ambiguous local work
	// is retired even when ordinary controller composition is unavailable.
	if recoveryProcess != nil {
		workers.Add(1)
		go runEventAutomationWorker(
			workerCtx,
			&workers,
			"PR development recovery",
			withEventAutomationRuntime(acquireRuntime, recoveryProcess),
		)
	}
	// Provider-verified threads use the controller lifecycle even when one of
	// its execution dependencies is unavailable. A fresh unpinned Bootstrap can
	// then fail safely instead of being stranded behind queue suppression.
	workers.Add(1)
	go runEventAutomationWorker(
		workerCtx,
		&workers,
		"PR development controller",
		withEventAutomationRuntime(acquireRuntime, repairControllerProcess),
	)
	if localReviewProcess != nil {
		workers.Add(1)
		go runEventAutomationWorker(
			workerCtx,
			&workers,
			"PR development review",
			withEventAutomationRuntime(acquireRuntime, localReviewProcess),
		)
	}

	// The legacy queue remains active only for isolated legacy threads and
	// pre-v14 preparation compatibility. It still reconciles unavailable and
	// expired legacy work without racing a provider-thread controller claim.
	repair := &prdevelopment.RepairWorker{
		Queue:       store,
		Verifier:    reviewRuntime.repairVerifier,
		Runtime:     reviewRuntime.repairRuntime,
		WorkerLabel: "gateway-pr-development-repair",
	}
	workers.Add(1)
	go runEventAutomationWorker(
		workerCtx,
		&workers,
		"PR development repair",
		withEventAutomationRuntime(acquireRuntime, repair.ProcessOne),
	)
	if reviewAttention != nil {
		attention := &reviews.AttentionTriggerWorker{
			Queue:       store,
			Launcher:    reviewAttention,
			WorkerLabel: "gateway-review-attention",
		}
		workers.Add(1)
		go runEventAutomationWorker(
			workerCtx,
			&workers,
			"review attention",
			withEventAutomationRuntime(acquireRuntime, attention.ProcessOne),
		)
	}
	if prDevelopmentAttention != nil {
		attention := &prdevelopment.AttentionTriggerWorker{
			Queue:       store,
			Launcher:    prDevelopmentAttention,
			WorkerLabel: "gateway-pr-development-attention",
		}
		attentionProcess := attention.ProcessOne
		if reviewRuntime.prDevelopmentAttentionProcess != nil {
			attentionProcess = reviewRuntime.prDevelopmentAttentionProcess
		}
		workers.Add(1)
		go runEventAutomationWorker(
			workerCtx,
			&workers,
			"PR development attention",
			withEventAutomationRuntime(acquireRuntime, attentionProcess),
		)
	}
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

func combinedPRDevelopmentAgentReadiness(
	repairReady func(string) bool,
	reviewReady func(string) bool,
) func(string) bool {
	if repairReady == nil || reviewReady == nil {
		return nil
	}
	return func(agentID string) bool {
		return repairReady(agentID) && reviewReady(agentID)
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

func githubPRDevelopmentRepairReady(
	ctx context.Context,
	runtime prDevelopmentRepairReadiness,
) bool {
	if runtime == nil {
		return false
	}
	return githubPRDevelopmentRepairToolsReady(func(
		occurrence workflows.WorkflowDependencyOccurrence,
	) workflows.WorkflowDependencyReadinessCode {
		return runtime.ResolveWorkflowDependency(ctx, occurrence)
	})
}

func githubPRDevelopmentRepairToolsReady(
	resolve func(
		workflows.WorkflowDependencyOccurrence,
	) workflows.WorkflowDependencyReadinessCode,
) bool {
	if resolve == nil {
		return false
	}
	return resolve(workflows.WorkflowDependencyOccurrence{
		Kind: workflows.WorkflowDependencyKindMCP,
		Name: reviews.DefaultGitHubMCPServer + "/" +
			reviews.GitHubPullRequestReadTool,
	}) == workflows.WorkflowDependencyReadinessReady
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
		attentionPolicies, err := configuredReviewAttentionPolicySource(cfg)
		if err != nil {
			return err
		}
		if err = validateConfiguredReviewAttentionAgents(attentionPolicies, agentLoop); err != nil {
			return err
		}
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
					logger.WarnCF("eventing", "Event retention maintenance failed", map[string]any{
						"error": err.Error(),
					})
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
				logger.WarnCF("eventing", "Event retention maintenance failed", map[string]any{
					"error": err.Error(),
				})
			}
			return
		}
		if pruned > 0 {
			logger.DebugCF("eventing", "Pruned expired durable events", map[string]any{
				"count": pruned,
			})
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
				logger.WarnCF(
					"eventing",
					"GitHub notification polling failed",
					map[string]any{"error": err.Error()},
				)
			}
			return
		}
		if result.Inserted > 0 {
			logger.DebugCF(
				"eventing",
				"Stored GitHub notifications",
				map[string]any{
					"notifications": result.Notifications,
					"matched":       result.Matched,
					"inserted":      result.Inserted,
				},
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
			return total, nil
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
			logger.WarnCF("eventing", "Event workflow worker iteration failed", map[string]any{
				"worker": name,
				"error":  err.Error(),
			})
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
