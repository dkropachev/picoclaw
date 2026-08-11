package gateway

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/sipeed/picoclaw/pkg/agent"
	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/prdevelopment"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type prDevelopmentPublicationPusherFactory func() (
	prdevelopment.PublicationPinnedLinePusher,
	error,
)

// prDevelopmentPublicationRuntimeStore is the complete private storage
// capability required to compose the publication workers. Keeping this as an
// interface lets composition tests observe every worker entry point without
// widening any of the narrower capabilities passed to individual handlers.
type prDevelopmentPublicationRuntimeStore interface {
	eventing.PRDevelopmentPublicationQueue
	eventing.PRDevelopmentPublicationPushJournal
	eventing.PRDevelopmentPublicationOutcomeReconciler
	eventing.PRDevelopmentPublicationGateClaimAuthenticator
	eventing.PRDevelopmentPublicationPushClaimAuthenticator
	eventing.PRDevelopmentPublicationGateContextSnapshotReader
	eventing.PRDevelopmentPublicationPinnedGateContextSnapshotReader
	eventing.PRDevelopmentPublicationDecisionRunStore
	GetPRDevelopmentCase(
		ctx context.Context,
		id string,
	) (eventing.PRDevelopmentCase, error)
	GetPRDevelopmentThreadForCase(
		ctx context.Context,
		caseID string,
	) (eventing.PRDevelopmentThread, error)
}

var _ prDevelopmentPublicationRuntimeStore = (*eventing.Store)(nil)

// prDevelopmentPublicationRuntimeConfig is an all-or-none internal
// composition boundary. Every capability stays structurally private if this
// value is ever embedded in a diagnostic projection.
type prDevelopmentPublicationRuntimeConfig struct {
	Enabled                 bool                                          `json:"-"`
	Store                   prDevelopmentPublicationRuntimeStore          `json:"-"`
	Executor                *workflows.Executor                           `json:"-"`
	Runs                    workflows.RunStore                            `json:"-"`
	Policies                sharedattention.PolicySource                  `json:"-"`
	Evidence                prdevelopment.AttentionEvidenceStore          `json:"-"`
	Workspaces              prdevelopment.AttentionReviewWorkspaceFactory `json:"-"`
	AcquireAttentionRuntime prdevelopment.AttentionContextRuntimeAcquire  `json:"-"`
	Provider                prdevelopment.PublicationProviderObserver     `json:"-"`
	RemoteHead              prdevelopment.PublicationRemoteHeadObserver   `json:"-"`
	Pusher                  prDevelopmentPublicationPusherFactory         `json:"-"`
	AcquireRuntime          eventAutomationRuntimeAcquire                 `json:"-"`
	PublicationProcess      func(context.Context) (bool, error)           `json:"-"`
	ReconciliationProcess   func(context.Context) (bool, error)           `json:"-"`
}

// prDevelopmentPublicationRuntime resolves generation-owned Git projections
// before either worker may touch durable work. The projections are cached only
// for this event-service generation, which is drained before a runtime swap.
type prDevelopmentPublicationRuntime struct {
	mu     sync.Mutex
	config prDevelopmentPublicationRuntimeConfig

	publication    *prdevelopment.PublicationWorker
	reconciliation *prdevelopment.PublicationOutcomeReconciliationWorker
}

func newPRDevelopmentPublicationRuntime(
	config prDevelopmentPublicationRuntimeConfig,
) (*prDevelopmentPublicationRuntime, error) {
	if !completePRDevelopmentPublicationRuntimeConfig(config) {
		return nil, nil
	}
	return &prDevelopmentPublicationRuntime{config: config}, nil
}

func completePRDevelopmentPublicationRuntimeConfig(
	config prDevelopmentPublicationRuntimeConfig,
) bool {
	return config.Enabled && config.Store != nil &&
		!nilPRDevelopmentPublicationCapability(config.Store) && config.Executor != nil &&
		config.Runs != nil && !nilPRDevelopmentPublicationCapability(config.Runs) &&
		config.Policies != nil && !nilPRDevelopmentPublicationCapability(config.Policies) &&
		config.Evidence != nil && !nilPRDevelopmentPublicationCapability(config.Evidence) &&
		config.Workspaces != nil && config.AcquireAttentionRuntime != nil &&
		config.Provider != nil && !nilPRDevelopmentPublicationCapability(config.Provider) &&
		config.RemoteHead != nil && !nilPRDevelopmentPublicationCapability(config.RemoteHead) &&
		config.Pusher != nil && config.AcquireRuntime != nil
}

func (runtime *prDevelopmentPublicationRuntime) ProcessPublication(
	ctx context.Context,
) (bool, error) {
	if runtime == nil {
		return false, prdevelopment.ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := runtime.initialize(); err != nil {
		return false, err
	}
	if runtime.config.PublicationProcess != nil {
		return runtime.config.PublicationProcess(ctx)
	}
	return runtime.publication.ProcessOne(ctx)
}

func (runtime *prDevelopmentPublicationRuntime) ProcessReconciliation(
	ctx context.Context,
) (bool, error) {
	if runtime == nil {
		return false, prdevelopment.ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := runtime.initialize(); err != nil {
		return false, err
	}
	if runtime.config.ReconciliationProcess != nil {
		return runtime.config.ReconciliationProcess(ctx)
	}
	return runtime.reconciliation.ProcessOne(ctx)
}

func (runtime *prDevelopmentPublicationRuntime) initialize() error {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.publication != nil && runtime.reconciliation != nil {
		return nil
	}
	if !completePRDevelopmentPublicationRuntimeConfig(runtime.config) {
		return prdevelopment.ErrUnavailable
	}

	workspace, err := runtime.config.Workspaces()
	if err != nil {
		return fmt.Errorf("resolve PR development publication Git reader: %w", err)
	}
	if nilPRDevelopmentPublicationCapability(workspace) {
		return fmt.Errorf("%w: PR development publication Git reader is unavailable", prdevelopment.ErrUnavailable)
	}
	pusher, err := runtime.config.Pusher()
	if err != nil {
		return fmt.Errorf("resolve PR development publication Git pusher: %w", err)
	}
	if nilPRDevelopmentPublicationCapability(pusher) {
		return fmt.Errorf("%w: PR development publication Git pusher is unavailable", prdevelopment.ErrUnavailable)
	}
	workspaces := func() (prdevelopment.AttentionReviewWorkspace, error) {
		return workspace, nil
	}

	processor, err := prdevelopment.NewPublicationGateProcessor(
		prdevelopment.PublicationGateProcessorConfig{
			Store: runtime.config.Store, Policies: runtime.config.Policies,
			Provider: runtime.config.Provider,
		},
	)
	if err != nil {
		return fmt.Errorf("compose publication gate processor: %w", err)
	}
	executor, err := prdevelopment.NewPublicationGateExecutor(
		prdevelopment.PublicationGateExecutorConfig{
			Store: runtime.config.Store, Executor: runtime.config.Executor,
			Runs: runtime.config.Runs, Evidence: runtime.config.Evidence,
			Workspaces: workspaces, AcquireRuntime: runtime.config.AcquireAttentionRuntime,
			Provider: runtime.config.Provider,
		},
	)
	if err != nil {
		return fmt.Errorf("compose publication gate executor: %w", err)
	}
	pending, err := prdevelopment.NewPublicationPendingGateHandler(
		prdevelopment.PublicationPendingGateHandlerConfig{
			Store: runtime.config.Store, Processor: processor, Executor: executor,
		},
	)
	if err != nil {
		return fmt.Errorf("compose pending publication handler: %w", err)
	}
	waiting, err := prdevelopment.NewPublicationGateWaitingHandler(
		prdevelopment.PublicationGateWaitingHandlerConfig{
			Store: runtime.config.Store, Runs: runtime.config.Runs,
		},
	)
	if err != nil {
		return fmt.Errorf("compose waiting publication handler: %w", err)
	}
	pushReady, err := prdevelopment.NewPublicationPushReadyHandler(
		prdevelopment.PublicationPushReadyHandlerConfig{
			Store: runtime.config.Store, Provider: runtime.config.Provider, Pusher: pusher,
		},
	)
	if err != nil {
		return fmt.Errorf("compose push-ready publication handler: %w", err)
	}
	dispatcher, err := prdevelopment.NewPublicationDispatcher(
		prdevelopment.PublicationDispatcherConfig{
			Pending: pending, GateWaiting: waiting, PushReady: pushReady,
		},
	)
	if err != nil {
		return fmt.Errorf("compose publication dispatcher: %w", err)
	}
	publication, err := prdevelopment.NewPublicationWorker(
		prdevelopment.PublicationWorkerConfig{
			Queue: runtime.config.Store, Dispatcher: dispatcher,
		},
	)
	if err != nil {
		return fmt.Errorf("compose publication worker: %w", err)
	}
	reconciliation, err := prdevelopment.NewPublicationOutcomeReconciliationWorker(
		prdevelopment.PublicationOutcomeReconciliationWorkerConfig{
			Store: runtime.config.Store, Observer: runtime.config.RemoteHead,
		},
	)
	if err != nil {
		return fmt.Errorf("compose publication reconciliation worker: %w", err)
	}

	runtime.publication = publication
	runtime.reconciliation = reconciliation
	return nil
}

func nilPRDevelopmentPublicationCapability(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// newPRDevelopmentPublicationPusherFactory resolves the concrete manager only
// while the caller holds the outer runtime-generation lease. The returned
// adapter cannot be widened back into generic Git-workspace authority.
func newPRDevelopmentPublicationPusherFactory(
	agentLoop *agent.AgentLoop,
) prDevelopmentPublicationPusherFactory {
	if agentLoop == nil {
		return nil
	}
	return func() (prdevelopment.PublicationPinnedLinePusher, error) {
		manager, err := agentLoop.ControllerGitWorkspaceManager()
		if err != nil {
			return nil, err
		}
		if manager == nil {
			return nil, errors.New("controller Git workspace manager is unavailable")
		}
		return &prDevelopmentPublicationPusher{pusher: manager}, nil
	}
}

type prDevelopmentPublicationPusher struct {
	pusher prdevelopment.PublicationPinnedLinePusher
}

func (pusher *prDevelopmentPublicationPusher) PushPinnedLine(
	ctx context.Context,
	request gitworkspace.PinnedLinePushRequest,
) (gitworkspace.PinnedLinePushResult, error) {
	if pusher == nil || nilPRDevelopmentPublicationCapability(pusher.pusher) {
		return gitworkspace.PinnedLinePushResult{}, prdevelopment.ErrUnavailable
	}
	return pusher.pusher.PushPinnedLine(ctx, request)
}
