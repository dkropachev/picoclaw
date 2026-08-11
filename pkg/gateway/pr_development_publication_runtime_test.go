//go:build !mipsle && !netbsd && !(freebsd && arm)

package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/agent"
	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/prdevelopment"
	"github.com/sipeed/picoclaw/pkg/prdevelopment/localci"
	"github.com/sipeed/picoclaw/pkg/reviews"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type publicationRuntimeTestDependencies struct {
	workspaceResolutions atomic.Int32
	pusherResolutions    atomic.Int32
	policyCalls          atomic.Int32
	store                *publicationRuntimeRecordingStore
	evidence             *publicationRuntimeTestEvidence
	workspace            *publicationRuntimeTestWorkspace
	provider             *publicationRuntimeTestProvider
	remoteHead           *publicationRuntimeTestProvider
	pusher               *publicationRuntimeTestPusher
}

// publicationRuntimeRecordingStore makes the three durable worker entry
// points observable while delegating the complete composition capability to a
// real SQLite store. A resolver failure must leave all four counters at zero;
// otherwise an empty database could hide an attempted claim or reconciliation.
type publicationRuntimeRecordingStore struct {
	prDevelopmentPublicationRuntimeStore
	raw             *eventing.Store
	claims          atomic.Int32
	expirations     atomic.Int32
	unknownScans    atomic.Int32
	reconciliations atomic.Int32
}

func (store *publicationRuntimeRecordingStore) ClaimPRDevelopmentPublications(
	ctx context.Context,
	input eventing.PRDevelopmentPublicationClaimRequest,
) ([]eventing.PRDevelopmentPublication, error) {
	store.claims.Add(1)
	return store.prDevelopmentPublicationRuntimeStore.
		ClaimPRDevelopmentPublications(ctx, input)
}

func (store *publicationRuntimeRecordingStore) ExpirePRDevelopmentPublicationPushes(
	ctx context.Context,
	limit int,
) ([]eventing.PRDevelopmentPublication, error) {
	store.expirations.Add(1)
	return store.prDevelopmentPublicationRuntimeStore.
		ExpirePRDevelopmentPublicationPushes(ctx, limit)
}

func (store *publicationRuntimeRecordingStore) ListPRDevelopmentPublicationUnknownOutcomes(
	ctx context.Context,
	filter eventing.PRDevelopmentPublicationUnknownOutcomeFilter,
) (eventing.PRDevelopmentPublicationUnknownOutcomePage, error) {
	store.unknownScans.Add(1)
	return store.prDevelopmentPublicationRuntimeStore.
		ListPRDevelopmentPublicationUnknownOutcomes(ctx, filter)
}

func (store *publicationRuntimeRecordingStore) ReconcilePRDevelopmentPublicationOutcome(
	ctx context.Context,
	input eventing.PRDevelopmentPublicationOutcomeReconciliation,
) (eventing.PRDevelopmentPublication, bool, error) {
	store.reconciliations.Add(1)
	return store.prDevelopmentPublicationRuntimeStore.
		ReconcilePRDevelopmentPublicationOutcome(ctx, input)
}

type publicationRuntimeTestEvidence struct {
	calls atomic.Int32
}

func (evidence *publicationRuntimeTestEvidence) GetPlan(
	context.Context,
	string,
) (localci.Plan, bool, error) {
	evidence.calls.Add(1)
	return localci.Plan{}, false, nil
}

func (evidence *publicationRuntimeTestEvidence) GetExecution(
	context.Context,
	string,
) (localci.Execution, bool, error) {
	evidence.calls.Add(1)
	return localci.Execution{}, false, nil
}

type publicationRuntimeTestWorkspace struct {
	calls atomic.Int32
}

func (workspace *publicationRuntimeTestWorkspace) SnapshotPinnedLineReview(
	context.Context,
	gitworkspace.PinnedLineReviewRequest,
) (gitworkspace.PinnedLineReviewSnapshot, error) {
	workspace.calls.Add(1)
	return gitworkspace.PinnedLineReviewSnapshot{}, nil
}

type publicationRuntimeTestProvider struct {
	providerCalls atomic.Int32
	remoteCalls   atomic.Int32
}

func (provider *publicationRuntimeTestProvider) ObservePublication(
	context.Context,
	eventing.PRDevelopmentCase,
	eventing.PRDevelopmentThreadIdentity,
) (prdevelopment.TimedPublicationProviderObservation, error) {
	provider.providerCalls.Add(1)
	return prdevelopment.TimedPublicationProviderObservation{}, nil
}

func (provider *publicationRuntimeTestProvider) ObservePublicationRemoteHead(
	context.Context,
	eventing.PRDevelopmentCase,
	eventing.PRDevelopmentThreadIdentity,
) (prdevelopment.TimedPublicationRemoteObservation, error) {
	provider.remoteCalls.Add(1)
	return prdevelopment.TimedPublicationRemoteObservation{}, nil
}

type publicationRuntimeTestPusher struct {
	calls   atomic.Int32
	ctx     context.Context
	request gitworkspace.PinnedLinePushRequest
	result  gitworkspace.PinnedLinePushResult
	err     error
}

func (pusher *publicationRuntimeTestPusher) PushPinnedLine(
	ctx context.Context,
	request gitworkspace.PinnedLinePushRequest,
) (gitworkspace.PinnedLinePushResult, error) {
	pusher.calls.Add(1)
	pusher.ctx = ctx
	pusher.request = request
	return pusher.result, pusher.err
}

func newPublicationRuntimeTestConfig(
	t *testing.T,
) (prDevelopmentPublicationRuntimeConfig, *publicationRuntimeTestDependencies) {
	t.Helper()
	workspaceRoot := t.TempDir()
	store, err := eventing.Open(
		context.Background(),
		filepath.Join(workspaceRoot, "eventing.db"),
	)
	if err != nil {
		t.Fatalf("eventing.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Errorf("event store Close() error = %v", closeErr)
		}
	})
	runs := workflows.NewFileRunStore(workspaceRoot)
	recordingStore := &publicationRuntimeRecordingStore{
		prDevelopmentPublicationRuntimeStore: store,
		raw:                                  store,
	}
	dependencies := &publicationRuntimeTestDependencies{
		store:      recordingStore,
		evidence:   &publicationRuntimeTestEvidence{},
		workspace:  &publicationRuntimeTestWorkspace{},
		provider:   &publicationRuntimeTestProvider{},
		remoteHead: &publicationRuntimeTestProvider{},
		pusher:     &publicationRuntimeTestPusher{},
	}
	config := prDevelopmentPublicationRuntimeConfig{
		Enabled:  true,
		Store:    recordingStore,
		Executor: &workflows.Executor{WorkspaceDir: workspaceRoot, Store: runs},
		Runs:     runs,
		Policies: sharedattention.PolicySourceFunc(func(
			ctx context.Context,
			_ sharedattention.PolicySelector,
			use sharedattention.PolicyUse,
		) error {
			dependencies.policyCalls.Add(1)
			if use == nil {
				return errors.New("attention policy callback is unavailable")
			}
			return use(ctx, sharedattention.PolicySnapshot{Revision: "publication-runtime-test"})
		}),
		Evidence: dependencies.evidence,
		Workspaces: func() (prdevelopment.AttentionReviewWorkspace, error) {
			dependencies.workspaceResolutions.Add(1)
			return dependencies.workspace, nil
		},
		AcquireAttentionRuntime: func(
			ctx context.Context,
			_ string,
		) (context.Context, session.SessionStore, func(), error) {
			return ctx, nil, func() {}, nil
		},
		Provider:   dependencies.provider,
		RemoteHead: dependencies.remoteHead,
		Pusher: func() (prdevelopment.PublicationPinnedLinePusher, error) {
			dependencies.pusherResolutions.Add(1)
			return dependencies.pusher, nil
		},
		AcquireRuntime: func(ctx context.Context) (context.Context, func(), error) {
			return ctx, func() {}, nil
		},
	}
	return config, dependencies
}

func TestPRDevelopmentPublicationRuntimeComposesCompleteStaticDependencies(t *testing.T) {
	config, dependencies := newPublicationRuntimeTestConfig(t)
	runtime, err := newPRDevelopmentPublicationRuntime(config)
	if err != nil {
		t.Fatalf("newPRDevelopmentPublicationRuntime() error = %v", err)
	}
	if runtime == nil {
		t.Fatal("newPRDevelopmentPublicationRuntime() returned nil for complete dependencies")
	}
	if err := runtime.initialize(); err != nil {
		t.Fatalf("initialize() error = %v", err)
	}
	if runtime.publication == nil || runtime.reconciliation == nil {
		t.Fatalf(
			"initialize() workers = (%p, %p), want both composed",
			runtime.publication,
			runtime.reconciliation,
		)
	}
	if err := runtime.initialize(); err != nil {
		t.Fatalf("cached initialize() error = %v", err)
	}
	if got := dependencies.workspaceResolutions.Load(); got != 1 {
		t.Fatalf("Git reader resolutions = %d, want 1", got)
	}
	if got := dependencies.pusherResolutions.Load(); got != 1 {
		t.Fatalf("Git pusher resolutions = %d, want 1", got)
	}
	assertPublicationRuntimeDurableStateUntouched(t, dependencies, config.Runs)
}

func TestPRDevelopmentPublicationRuntimeIsInertWithoutEveryCriticalDependency(t *testing.T) {
	tests := []struct {
		name string
		omit func(*prDevelopmentPublicationRuntimeConfig)
	}{
		{name: "disabled", omit: func(config *prDevelopmentPublicationRuntimeConfig) { config.Enabled = false }},
		{name: "store", omit: func(config *prDevelopmentPublicationRuntimeConfig) { config.Store = nil }},
		{name: "executor", omit: func(config *prDevelopmentPublicationRuntimeConfig) { config.Executor = nil }},
		{name: "runs", omit: func(config *prDevelopmentPublicationRuntimeConfig) { config.Runs = nil }},
		{name: "policies", omit: func(config *prDevelopmentPublicationRuntimeConfig) { config.Policies = nil }},
		{name: "evidence", omit: func(config *prDevelopmentPublicationRuntimeConfig) { config.Evidence = nil }},
		{name: "workspaces", omit: func(config *prDevelopmentPublicationRuntimeConfig) { config.Workspaces = nil }},
		{
			name: "attention runtime",
			omit: func(config *prDevelopmentPublicationRuntimeConfig) {
				config.AcquireAttentionRuntime = nil
			},
		},
		{name: "provider", omit: func(config *prDevelopmentPublicationRuntimeConfig) { config.Provider = nil }},
		{name: "remote head", omit: func(config *prDevelopmentPublicationRuntimeConfig) { config.RemoteHead = nil }},
		{name: "pusher", omit: func(config *prDevelopmentPublicationRuntimeConfig) { config.Pusher = nil }},
		{
			name: "outer runtime",
			omit: func(config *prDevelopmentPublicationRuntimeConfig) {
				config.AcquireRuntime = nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, dependencies := newPublicationRuntimeTestConfig(t)
			var publicationCalls, reconciliationCalls atomic.Int32
			config.PublicationProcess = func(context.Context) (bool, error) {
				publicationCalls.Add(1)
				return true, nil
			}
			config.ReconciliationProcess = func(context.Context) (bool, error) {
				reconciliationCalls.Add(1)
				return true, nil
			}
			test.omit(&config)
			runtime, err := newPRDevelopmentPublicationRuntime(config)
			if err != nil {
				t.Fatalf("newPRDevelopmentPublicationRuntime() error = %v", err)
			}
			if runtime != nil {
				t.Fatalf("newPRDevelopmentPublicationRuntime() = %#v, want nil", runtime)
			}
			if got := dependencies.workspaceResolutions.Load(); got != 0 {
				t.Fatalf("Git reader resolutions = %d, want 0", got)
			}
			if got := dependencies.pusherResolutions.Load(); got != 0 {
				t.Fatalf("Git pusher resolutions = %d, want 0", got)
			}
			if got := publicationCalls.Load(); got != 0 {
				t.Fatalf("publication override calls = %d, want 0", got)
			}
			if got := reconciliationCalls.Load(); got != 0 {
				t.Fatalf("reconciliation override calls = %d, want 0", got)
			}
			assertPublicationRuntimeDurableStateUntouched(t, dependencies, config.Runs)
		})
	}
}

func TestPRDevelopmentPublicationRuntimeRejectsTypedNilCriticalCapabilities(t *testing.T) {
	tests := []struct {
		name string
		omit func(*prDevelopmentPublicationRuntimeConfig)
	}{
		{name: "store", omit: func(config *prDevelopmentPublicationRuntimeConfig) {
			var store *publicationRuntimeRecordingStore
			config.Store = store
		}},
		{name: "runs", omit: func(config *prDevelopmentPublicationRuntimeConfig) {
			var runs *workflows.FileRunStore
			config.Runs = runs
		}},
		{name: "policies", omit: func(config *prDevelopmentPublicationRuntimeConfig) {
			var policies sharedattention.PolicySourceFunc
			config.Policies = policies
		}},
		{name: "evidence", omit: func(config *prDevelopmentPublicationRuntimeConfig) {
			var evidence *publicationRuntimeTestEvidence
			config.Evidence = evidence
		}},
		{name: "provider", omit: func(config *prDevelopmentPublicationRuntimeConfig) {
			var provider *publicationRuntimeTestProvider
			config.Provider = provider
		}},
		{name: "remote head", omit: func(config *prDevelopmentPublicationRuntimeConfig) {
			var remoteHead *publicationRuntimeTestProvider
			config.RemoteHead = remoteHead
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, dependencies := newPublicationRuntimeTestConfig(t)
			test.omit(&config)
			runtime, err := newPRDevelopmentPublicationRuntime(config)
			if err != nil {
				t.Fatalf("newPRDevelopmentPublicationRuntime() error = %v", err)
			}
			if runtime != nil {
				t.Fatalf("newPRDevelopmentPublicationRuntime() = %#v, want nil", runtime)
			}
			if got := dependencies.workspaceResolutions.Load(); got != 0 {
				t.Fatalf("Git reader resolutions = %d, want 0", got)
			}
			if got := dependencies.pusherResolutions.Load(); got != 0 {
				t.Fatalf("Git pusher resolutions = %d, want 0", got)
			}
			assertPublicationRuntimeDurableStateUntouched(t, dependencies, config.Runs)
		})
	}
}

func TestPRDevelopmentPublicationRuntimeResolverFailurePrecedesOverridesAndDurableWork(t *testing.T) {
	errResolve := errors.New("generation-owned Git projection failed")
	tests := []struct {
		name             string
		configure        func(*prDevelopmentPublicationRuntimeConfig, *publicationRuntimeTestDependencies)
		wantError        error
		wantReaderCalls  int32
		wantPusherCalls  int32
		wantErrorMessage string
	}{
		{
			name: "reader error",
			configure: func(config *prDevelopmentPublicationRuntimeConfig, dependencies *publicationRuntimeTestDependencies) {
				config.Workspaces = func() (prdevelopment.AttentionReviewWorkspace, error) {
					dependencies.workspaceResolutions.Add(1)
					return nil, errResolve
				}
			},
			wantError:        errResolve,
			wantReaderCalls:  1,
			wantErrorMessage: "resolve PR development publication Git reader",
		},
		{
			name: "reader typed nil",
			configure: func(config *prDevelopmentPublicationRuntimeConfig, dependencies *publicationRuntimeTestDependencies) {
				config.Workspaces = func() (prdevelopment.AttentionReviewWorkspace, error) {
					dependencies.workspaceResolutions.Add(1)
					var workspace *publicationRuntimeTestWorkspace
					return workspace, nil
				}
			},
			wantError:        prdevelopment.ErrUnavailable,
			wantReaderCalls:  1,
			wantErrorMessage: "Git reader is unavailable",
		},
		{
			name: "pusher error",
			configure: func(config *prDevelopmentPublicationRuntimeConfig, dependencies *publicationRuntimeTestDependencies) {
				config.Pusher = func() (prdevelopment.PublicationPinnedLinePusher, error) {
					dependencies.pusherResolutions.Add(1)
					return nil, errResolve
				}
			},
			wantError:        errResolve,
			wantReaderCalls:  1,
			wantPusherCalls:  1,
			wantErrorMessage: "resolve PR development publication Git pusher",
		},
		{
			name: "pusher typed nil",
			configure: func(config *prDevelopmentPublicationRuntimeConfig, dependencies *publicationRuntimeTestDependencies) {
				config.Pusher = func() (prdevelopment.PublicationPinnedLinePusher, error) {
					dependencies.pusherResolutions.Add(1)
					var pusher *publicationRuntimeTestPusher
					return pusher, nil
				}
			},
			wantError:        prdevelopment.ErrUnavailable,
			wantReaderCalls:  1,
			wantPusherCalls:  1,
			wantErrorMessage: "Git pusher is unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, dependencies := newPublicationRuntimeTestConfig(t)
			var publicationCalls, reconciliationCalls atomic.Int32
			config.PublicationProcess = func(context.Context) (bool, error) {
				publicationCalls.Add(1)
				return true, nil
			}
			config.ReconciliationProcess = func(context.Context) (bool, error) {
				reconciliationCalls.Add(1)
				return true, nil
			}
			test.configure(&config, dependencies)
			runtime, err := newPRDevelopmentPublicationRuntime(config)
			if err != nil {
				t.Fatalf("newPRDevelopmentPublicationRuntime() error = %v", err)
			}
			processed, err := runtime.ProcessPublication(context.Background())
			if processed {
				t.Fatal("ProcessPublication() processed = true after resolver failure")
			}
			if !errors.Is(err, test.wantError) {
				t.Fatalf("ProcessPublication() error = %v, want %v", err, test.wantError)
			}
			if !strings.Contains(err.Error(), test.wantErrorMessage) {
				t.Fatalf("ProcessPublication() error = %q, want %q", err, test.wantErrorMessage)
			}
			if runtime.publication != nil || runtime.reconciliation != nil {
				t.Fatalf(
					"workers after resolver failure = (%p, %p), want nil",
					runtime.publication,
					runtime.reconciliation,
				)
			}
			if got := dependencies.workspaceResolutions.Load(); got != test.wantReaderCalls {
				t.Fatalf("Git reader resolutions = %d, want %d", got, test.wantReaderCalls)
			}
			if got := dependencies.pusherResolutions.Load(); got != test.wantPusherCalls {
				t.Fatalf("Git pusher resolutions = %d, want %d", got, test.wantPusherCalls)
			}
			if got := publicationCalls.Load(); got != 0 {
				t.Fatalf("publication override calls = %d, want 0", got)
			}
			if got := reconciliationCalls.Load(); got != 0 {
				t.Fatalf("reconciliation override calls = %d, want 0", got)
			}
			assertPublicationRuntimeDurableStateUntouched(t, dependencies, config.Runs)
		})
	}
}

type publicationRuntimeGenerationContextKey struct{}

type publicationRuntimeGenerationMarker struct {
	released atomic.Bool
}

func TestPRDevelopmentPublicationRuntimeInitializesAndCachesBeforeBothOverrides(t *testing.T) {
	config, dependencies := newPublicationRuntimeTestConfig(t)
	var acquisitions, releases atomic.Int32
	config.AcquireRuntime = func(ctx context.Context) (context.Context, func(), error) {
		marker := &publicationRuntimeGenerationMarker{}
		acquisitions.Add(1)
		return context.WithValue(ctx, publicationRuntimeGenerationContextKey{}, marker), func() {
			marker.released.Store(true)
			releases.Add(1)
		}, nil
	}
	var runtime *prDevelopmentPublicationRuntime
	checkOverride := func(ctx context.Context) error {
		marker, _ := ctx.Value(publicationRuntimeGenerationContextKey{}).(*publicationRuntimeGenerationMarker)
		if marker == nil {
			return errors.New("outer runtime generation marker is absent")
		}
		if marker.released.Load() {
			return errors.New("outer runtime generation was released before override returned")
		}
		if runtime == nil || runtime.publication == nil || runtime.reconciliation == nil {
			return errors.New("publication workers were not initialized before override")
		}
		return nil
	}
	var publicationCalls, reconciliationCalls atomic.Int32
	config.PublicationProcess = func(ctx context.Context) (bool, error) {
		publicationCalls.Add(1)
		return true, checkOverride(ctx)
	}
	config.ReconciliationProcess = func(ctx context.Context) (bool, error) {
		reconciliationCalls.Add(1)
		return true, checkOverride(ctx)
	}
	var err error
	runtime, err = newPRDevelopmentPublicationRuntime(config)
	if err != nil {
		t.Fatalf("newPRDevelopmentPublicationRuntime() error = %v", err)
	}
	processes := []struct {
		name    string
		process func(context.Context) (bool, error)
	}{
		{name: "publication", process: runtime.ProcessPublication},
		{name: "reconciliation", process: runtime.ProcessReconciliation},
	}
	for _, process := range processes {
		processed, processErr := withEventAutomationRuntime(
			config.AcquireRuntime,
			process.process,
		)(context.Background())
		if processErr != nil {
			t.Fatalf("%s process error = %v", process.name, processErr)
		}
		if !processed {
			t.Fatalf("%s process processed = false, want true", process.name)
		}
	}
	if got := dependencies.workspaceResolutions.Load(); got != 1 {
		t.Fatalf("Git reader resolutions = %d, want 1", got)
	}
	if got := dependencies.pusherResolutions.Load(); got != 1 {
		t.Fatalf("Git pusher resolutions = %d, want 1", got)
	}
	if got := publicationCalls.Load(); got != 1 {
		t.Fatalf("publication override calls = %d, want 1", got)
	}
	if got := reconciliationCalls.Load(); got != 1 {
		t.Fatalf("reconciliation override calls = %d, want 1", got)
	}
	if got := acquisitions.Load(); got != 2 {
		t.Fatalf("outer runtime acquisitions = %d, want 2", got)
	}
	if got := releases.Load(); got != 2 {
		t.Fatalf("outer runtime releases = %d, want 2", got)
	}
}

func TestPRDevelopmentPublicationRuntimeOverridesDrainBeforeGenerationRelease(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*prDevelopmentPublicationRuntimeConfig, func(context.Context) (bool, error))
		process   func(*prDevelopmentPublicationRuntime) func(context.Context) (bool, error)
	}{
		{
			name: "publication",
			configure: func(config *prDevelopmentPublicationRuntimeConfig, override func(context.Context) (bool, error)) {
				config.PublicationProcess = override
			},
			process: func(runtime *prDevelopmentPublicationRuntime) func(context.Context) (bool, error) {
				return runtime.ProcessPublication
			},
		},
		{
			name: "reconciliation",
			configure: func(config *prDevelopmentPublicationRuntimeConfig, override func(context.Context) (bool, error)) {
				config.ReconciliationProcess = override
			},
			process: func(runtime *prDevelopmentPublicationRuntime) func(context.Context) (bool, error) {
				return runtime.ProcessReconciliation
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, _ := newPublicationRuntimeTestConfig(t)
			entered := make(chan struct{})
			observedCancellation := make(chan struct{})
			allowReturn := make(chan struct{})
			var allowReturnOnce sync.Once
			t.Cleanup(func() { allowReturnOnce.Do(func() { close(allowReturn) }) })
			override := func(ctx context.Context) (bool, error) {
				close(entered)
				<-ctx.Done()
				close(observedCancellation)
				<-allowReturn
				return false, ctx.Err()
			}
			test.configure(&config, override)
			markers := make(chan *publicationRuntimeGenerationMarker, 1)
			config.AcquireRuntime = func(ctx context.Context) (context.Context, func(), error) {
				marker := &publicationRuntimeGenerationMarker{}
				markers <- marker
				return ctx, func() { marker.released.Store(true) }, nil
			}
			runtime, err := newPRDevelopmentPublicationRuntime(config)
			if err != nil {
				t.Fatalf("newPRDevelopmentPublicationRuntime() error = %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type processResult struct {
				processed bool
				err       error
			}
			results := make(chan processResult, 1)
			go func() {
				processed, processErr := withEventAutomationRuntime(
					config.AcquireRuntime,
					test.process(runtime),
				)(ctx)
				results <- processResult{processed: processed, err: processErr}
			}()
			marker := receivePublicationRuntimeTest(t, markers, "outer runtime acquisition")
			receivePublicationRuntimeTest(t, entered, "override entry")
			cancel()
			receivePublicationRuntimeTest(t, observedCancellation, "override cancellation")
			if marker.released.Load() {
				t.Fatal("outer runtime generation released while canceled override was still draining")
			}
			select {
			case result := <-results:
				t.Fatalf("process returned before override drained: %#v", result)
			default:
			}
			allowReturnOnce.Do(func() { close(allowReturn) })
			result := receivePublicationRuntimeTest(t, results, "process completion")
			if result.processed {
				t.Fatal("process processed = true after cancellation")
			}
			if !errors.Is(result.err, context.Canceled) {
				t.Fatalf("process error = %v, want context.Canceled", result.err)
			}
			if !marker.released.Load() {
				t.Fatal("outer runtime generation was not released after override drained")
			}
		})
	}
}

func TestEventAutomationPublicationLoopsStartWithCompleteGraphAndDrain(t *testing.T) {
	workspaceRoot := t.TempDir()
	cfg := eventAutomationTestConfig(
		workspaceRoot,
		filepath.Join(workspaceRoot, "eventing", "events.db"),
		true,
		true,
	)
	policies, err := reviews.NewConfigAttentionPolicySource(nil, nil)
	if err != nil {
		t.Fatalf("NewConfigAttentionPolicySource() error = %v", err)
	}
	workspace := &publicationRuntimeTestWorkspace{}
	provider := &publicationRuntimeTestProvider{}
	pusher := &publicationRuntimeTestPusher{}
	var workspaceResolutions, pusherResolutions atomic.Int32

	type generationKey struct{}
	publicationEntered := make(chan struct{})
	reconciliationEntered := make(chan struct{})
	publicationExited := make(chan struct{})
	reconciliationExited := make(chan struct{})
	process := func(entered chan struct{}, exited chan struct{}) func(context.Context) (bool, error) {
		return func(ctx context.Context) (bool, error) {
			if active, _ := ctx.Value(generationKey{}).(bool); !active {
				return false, errors.New("publication loop has no outer generation")
			}
			close(entered)
			<-ctx.Done()
			close(exited)
			return false, ctx.Err()
		}
	}
	runtime := eventReviewRuntime{
		attentionPolicies:              policies,
		prDevelopmentAttentionPolicies: policies,
		prDevelopmentAttentionWorkspaces: func() (
			prdevelopment.AttentionReviewWorkspace,
			error,
		) {
			workspaceResolutions.Add(1)
			return workspace, nil
		},
		acquirePRDevelopmentAttentionRuntime: func(
			ctx context.Context,
			_ string,
		) (context.Context, session.SessionStore, func(), error) {
			return ctx, nil, func() {}, nil
		},
		publicationProvider:   provider,
		publicationRemoteHead: provider,
		publicationPusher: func() (prdevelopment.PublicationPinnedLinePusher, error) {
			pusherResolutions.Add(1)
			return pusher, nil
		},
		publicationProcess: process(publicationEntered, publicationExited),
		publicationReconciliationProcess: process(
			reconciliationEntered,
			reconciliationExited,
		),
	}
	acquire := func(ctx context.Context) (context.Context, func(), error) {
		return context.WithValue(ctx, generationKey{}, true), func() {}, nil
	}
	runs := workflows.NewFileRunStore(workspaceRoot)
	service, err := newEventAutomationServiceWithReviews(
		context.Background(),
		cfg,
		&workflows.Executor{WorkspaceDir: workspaceRoot, Store: runs},
		nil,
		acquire,
		runtime,
	)
	if err != nil {
		t.Fatalf("newEventAutomationServiceWithReviews() error = %v", err)
	}
	if service == nil || service.prLocalCI == nil || service.prLocalCI.evidence == nil {
		t.Fatal("complete publication graph did not retain its CI evidence runtime")
	}
	closed := false
	t.Cleanup(func() {
		if closed {
			return
		}
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := service.Close(closeCtx); closeErr != nil {
			t.Errorf("event automation Close() error = %v", closeErr)
		}
	})

	receivePublicationRuntimeTest(t, publicationEntered, "publication loop entry")
	receivePublicationRuntimeTest(t, reconciliationEntered, "reconciliation loop entry")
	if got := workspaceResolutions.Load(); got != 1 {
		t.Fatalf("service Git reader resolutions = %d, want 1", got)
	}
	if got := pusherResolutions.Load(); got != 1 {
		t.Fatalf("service Git pusher resolutions = %d, want 1", got)
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = service.Close(closeCtx); err != nil {
		t.Fatalf("event automation Close() error = %v", err)
	}
	closed = true
	receivePublicationRuntimeTest(t, publicationExited, "publication loop drain")
	receivePublicationRuntimeTest(t, reconciliationExited, "reconciliation loop drain")
	_, err = service.store.List(
		context.Background(),
		eventing.EventFilter{Limit: 1},
	)
	if !errors.Is(err, eventing.ErrClosed) {
		t.Fatalf("event store List() after worker drain error = %v, want ErrClosed", err)
	}
}

func TestEventAutomationPublicationReloadDrainsAndRebuildsGenerationAdapters(
	t *testing.T,
) {
	oldRoot := t.TempDir()
	oldCfg := eventAutomationTestConfig(
		oldRoot,
		filepath.Join(oldRoot, "eventing", "events.db"),
		true,
		true,
	)
	newRoot := t.TempDir()
	newCfg := eventAutomationTestConfig(
		newRoot,
		filepath.Join(newRoot, "eventing", "events.db"),
		true,
		true,
	)
	messageBus := bus.NewMessageBus()
	oldProvider := &orderedShutdownProvider{closed: make(chan struct{})}
	newProvider := &orderedShutdownProvider{closed: make(chan struct{})}
	agentLoop := agent.NewAgentLoop(oldCfg, messageBus, oldProvider)
	oldRelease := make(chan struct{})
	var oldReleaseOnce sync.Once
	var pausedResume func()
	var oldService, newService *eventAutomationService
	t.Cleanup(func() {
		oldReleaseOnce.Do(func() { close(oldRelease) })
		if pausedResume != nil {
			pausedResume()
		}
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if newService != nil {
			if err := newService.Close(closeCtx); err != nil {
				t.Errorf("new event automation Close() error = %v", err)
			}
		}
		if oldService != nil {
			if err := oldService.Close(closeCtx); err != nil {
				t.Errorf("old event automation Close() error = %v", err)
			}
		}
		messageBus.Close()
		agentLoop.Close()
		oldProvider.Close()
		newProvider.Close()
	})

	policies, err := reviews.NewConfigAttentionPolicySource(nil, nil)
	if err != nil {
		t.Fatalf("NewConfigAttentionPolicySource() error = %v", err)
	}
	makeDependencies := func() *publicationRuntimeTestDependencies {
		return &publicationRuntimeTestDependencies{
			evidence:   &publicationRuntimeTestEvidence{},
			workspace:  &publicationRuntimeTestWorkspace{},
			provider:   &publicationRuntimeTestProvider{},
			remoteHead: &publicationRuntimeTestProvider{},
			pusher:     &publicationRuntimeTestPusher{},
		}
	}
	oldDependencies := makeDependencies()
	newDependencies := makeDependencies()
	makeRuntime := func(
		dependencies *publicationRuntimeTestDependencies,
		publicationProcess func(context.Context) (bool, error),
	) eventReviewRuntime {
		return eventReviewRuntime{
			attentionPolicies:              policies,
			prDevelopmentAttentionPolicies: policies,
			prDevelopmentAttentionWorkspaces: func() (
				prdevelopment.AttentionReviewWorkspace,
				error,
			) {
				dependencies.workspaceResolutions.Add(1)
				return dependencies.workspace, nil
			},
			acquirePRDevelopmentAttentionRuntime: func(
				ctx context.Context,
				_ string,
			) (context.Context, session.SessionStore, func(), error) {
				return ctx, nil, func() {}, nil
			},
			publicationProvider:   dependencies.provider,
			publicationRemoteHead: dependencies.remoteHead,
			publicationPusher: func() (prdevelopment.PublicationPinnedLinePusher, error) {
				dependencies.pusherResolutions.Add(1)
				return dependencies.pusher, nil
			},
			publicationProcess: publicationProcess,
			publicationReconciliationProcess: func(context.Context) (bool, error) {
				return false, nil
			},
		}
	}
	startService := func(
		cfg *config.Config,
		root string,
		runtime eventReviewRuntime,
	) *eventAutomationService {
		t.Helper()
		runs := workflows.NewFileRunStore(root)
		service, setupErr := newEventAutomationServiceWithReviews(
			context.Background(),
			cfg,
			&workflows.Executor{WorkspaceDir: root, Store: runs},
			nil,
			func(ctx context.Context) (context.Context, func(), error) {
				return agentLoop.AcquireRuntimeGeneration(ctx, cfg)
			},
			runtime,
		)
		if setupErr != nil {
			t.Fatalf("newEventAutomationServiceWithReviews() error = %v", setupErr)
		}
		return service
	}

	oldEntered := make(chan struct{})
	oldExited := make(chan struct{})
	var oldEnterOnce, oldExitOnce sync.Once
	oldProcess := func(ctx context.Context) (bool, error) {
		blocked := false
		oldEnterOnce.Do(func() {
			blocked = true
			close(oldEntered)
		})
		if !blocked {
			return false, nil
		}
		select {
		case <-oldRelease:
			oldExitOnce.Do(func() { close(oldExited) })
			return false, nil
		case <-ctx.Done():
			oldExitOnce.Do(func() { close(oldExited) })
			return false, ctx.Err()
		}
	}
	oldService = startService(oldCfg, oldRoot, makeRuntime(oldDependencies, oldProcess))
	receivePublicationRuntimeTest(t, oldEntered, "old publication generation entry")
	if got := oldDependencies.workspaceResolutions.Load(); got != 1 {
		t.Fatalf("old Git reader resolutions = %d, want 1", got)
	}
	if got := oldDependencies.pusherResolutions.Load(); got != 1 {
		t.Fatalf("old Git pusher resolutions = %d, want 1", got)
	}

	type pauseResult struct {
		resume func()
		err    error
	}
	pauseResults := make(chan pauseResult, 1)
	pauseCtx, cancelPause := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelPause()
	go func() {
		resume, pauseErr := agentLoop.PauseRuntimeForReload(pauseCtx)
		pauseResults <- pauseResult{resume: resume, err: pauseErr}
	}()
	select {
	case result := <-pauseResults:
		if result.resume != nil {
			result.resume()
		}
		t.Fatalf("runtime reload pause crossed active old publication work: %v", result.err)
	case <-time.After(100 * time.Millisecond):
	}
	oldReleaseOnce.Do(func() { close(oldRelease) })
	receivePublicationRuntimeTest(t, oldExited, "old publication generation drain")
	paused := receivePublicationRuntimeTest(t, pauseResults, "runtime reload pause")
	if paused.err != nil || paused.resume == nil {
		t.Fatalf(
			"PauseRuntimeForReload() has resume = %t, error = %v",
			paused.resume != nil,
			paused.err,
		)
	}
	pausedResume = paused.resume

	closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
	if err = oldService.Close(closeCtx); err != nil {
		cancelClose()
		t.Fatalf("old event automation Close() error = %v", err)
	}
	cancelClose()
	oldService = nil
	retained, err := agentLoop.ReloadProviderAndConfigRetainingPrevious(
		context.Background(),
		newProvider,
		newCfg,
	)
	if err != nil {
		t.Fatalf("ReloadProviderAndConfigRetainingPrevious() error = %v", err)
	}
	if retained != oldProvider {
		t.Fatalf("retained provider = %#v, want old provider", retained)
	}

	newEntered := make(chan struct{})
	var newEnterOnce sync.Once
	newService = startService(
		newCfg,
		newRoot,
		makeRuntime(newDependencies, func(context.Context) (bool, error) {
			newEnterOnce.Do(func() { close(newEntered) })
			return false, nil
		}),
	)
	select {
	case <-newEntered:
		t.Fatal("replacement publication work entered while reload remained paused")
	case <-time.After(100 * time.Millisecond):
	}
	if got := newDependencies.workspaceResolutions.Load(); got != 0 {
		t.Fatalf("new Git reader resolved before replacement generation resumed: %d", got)
	}
	if got := newDependencies.pusherResolutions.Load(); got != 0 {
		t.Fatalf("new Git pusher resolved before replacement generation resumed: %d", got)
	}

	pausedResume()
	pausedResume = nil
	receivePublicationRuntimeTest(t, newEntered, "replacement publication generation entry")
	if got := newDependencies.workspaceResolutions.Load(); got != 1 {
		t.Fatalf("new Git reader resolutions = %d, want 1", got)
	}
	if got := newDependencies.pusherResolutions.Load(); got != 1 {
		t.Fatalf("new Git pusher resolutions = %d, want 1", got)
	}
	if got := oldDependencies.workspaceResolutions.Load(); got != 1 {
		t.Fatalf("old Git reader crossed into replacement generation: %d resolutions", got)
	}
	if got := oldDependencies.pusherResolutions.Load(); got != 1 {
		t.Fatalf("old Git pusher crossed into replacement generation: %d resolutions", got)
	}
}

func TestPRDevelopmentPublicationRuntimeJSONIsPrivate(t *testing.T) {
	config, _ := newPublicationRuntimeTestConfig(t)
	config.PublicationProcess = func(context.Context) (bool, error) { return false, nil }
	config.ReconciliationProcess = func(context.Context) (bool, error) { return false, nil }
	runtime, err := newPRDevelopmentPublicationRuntime(config)
	if err != nil {
		t.Fatalf("newPRDevelopmentPublicationRuntime() error = %v", err)
	}
	if err := runtime.initialize(); err != nil {
		t.Fatalf("initialize() error = %v", err)
	}
	for name, value := range map[string]any{
		"config":  config,
		"runtime": runtime,
	} {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatalf("json.Marshal(%s) error = %v", name, marshalErr)
		}
		if got := string(encoded); got != "{}" {
			t.Fatalf("json.Marshal(%s) = %s, want {}", name, got)
		}
	}
}

func TestPRDevelopmentPublicationPusherForwardsOnlyPinnedLinePush(t *testing.T) {
	ctx := context.WithValue(context.Background(), publicationRuntimeGenerationContextKey{}, "push-context")
	request := gitworkspace.PinnedLinePushRequest{
		Repository:            "owner/private-repository",
		SourceRef:             "refs/heads/review-fix",
		ExpectedSourceCommit:  "remote-before",
		WorkspaceID:           "workspace-id",
		LineID:                "line-id",
		ExpectedVersion:       7,
		ExpectedMutationEpoch: 11,
		ExpectedParkIntentID:  "park-intent",
		ExpectedBase:          "base",
		ExpectedTip:           "tip",
		ExpectedTree:          "tree",
		ExpectedRemoteTip:     "remote-before",
	}
	wantResult := gitworkspace.PinnedLinePushResult{
		WorkspaceID:       "workspace-id",
		Version:           7,
		MutationEpoch:     11,
		ParkIntentID:      "park-intent",
		BaseCommit:        "base",
		Tip:               "tip",
		Tree:              "tree",
		RemoteRef:         "refs/heads/review-fix",
		ExpectedRemoteTip: "remote-before",
		RemoteTip:         "tip",
		Disposition:       gitworkspace.PinnedLinePushApplied,
		WorkspaceClean:    true,
	}
	wantErr := errors.New("push response was uncertain")
	delegate := &publicationRuntimeTestPusher{result: wantResult, err: wantErr}
	adapter := &prDevelopmentPublicationPusher{pusher: delegate}
	gotResult, err := adapter.PushPinnedLine(ctx, request)
	if !errors.Is(err, wantErr) {
		t.Fatalf("PushPinnedLine() error = %v, want %v", err, wantErr)
	}
	if gotResult != wantResult {
		t.Fatalf("PushPinnedLine() result = %#v, want %#v", gotResult, wantResult)
	}
	if got := delegate.calls.Load(); got != 1 {
		t.Fatalf("delegate calls = %d, want 1", got)
	}
	if delegate.ctx != ctx {
		t.Fatal("PushPinnedLine() did not preserve context identity")
	}
	if delegate.request != request {
		t.Fatalf("delegate request = %#v, want %#v", delegate.request, request)
	}
}

func TestPRDevelopmentPublicationPusherRejectsNilCapabilities(t *testing.T) {
	request := gitworkspace.PinnedLinePushRequest{LineID: "line-id"}
	var nilAdapter *prDevelopmentPublicationPusher
	_, err := nilAdapter.PushPinnedLine(context.Background(), request)
	if !errors.Is(err, prdevelopment.ErrUnavailable) {
		t.Fatalf("nil adapter PushPinnedLine() error = %v, want ErrUnavailable", err)
	}
	var typedNilDelegate *publicationRuntimeTestPusher
	adapter := &prDevelopmentPublicationPusher{pusher: typedNilDelegate}
	_, err = adapter.PushPinnedLine(context.Background(), request)
	if !errors.Is(err, prdevelopment.ErrUnavailable) {
		t.Fatalf("typed-nil delegate PushPinnedLine() error = %v, want ErrUnavailable", err)
	}
}

func assertPublicationRuntimeDurableStateUntouched(
	t *testing.T,
	dependencies *publicationRuntimeTestDependencies,
	runs workflows.RunStore,
) {
	t.Helper()
	if dependencies != nil && dependencies.store != nil {
		store := dependencies.store
		for name, calls := range map[string]int32{
			"publication claims":      store.claims.Load(),
			"push expirations":        store.expirations.Load(),
			"unknown-outcome scans":   store.unknownScans.Load(),
			"outcome reconciliations": store.reconciliations.Load(),
		} {
			if calls != 0 {
				t.Fatalf("%s = %d, want 0", name, calls)
			}
		}
		page, err := store.raw.List(
			context.Background(),
			eventing.EventFilter{Limit: 10},
		)
		if err != nil {
			t.Fatalf("event store List() error = %v", err)
		}
		if len(page.Events) != 0 {
			t.Fatalf("event store records = %d, want 0", len(page.Events))
		}
	}
	if runs != nil && !nilPRDevelopmentPublicationCapability(runs) {
		storedRuns, err := runs.ListRuns(context.Background())
		if err != nil {
			t.Fatalf("run store ListRuns() error = %v", err)
		}
		if len(storedRuns) != 0 {
			t.Fatalf("workflow runs = %d, want 0", len(storedRuns))
		}
	}
}

func receivePublicationRuntimeTest[T any](
	t *testing.T,
	channel <-chan T,
	description string,
) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(3 * time.Second):
		var zero T
		t.Fatalf("timed out waiting for %s", description)
		return zero
	}
}

var (
	_ prdevelopment.AttentionEvidenceStore        = (*publicationRuntimeTestEvidence)(nil)
	_ prdevelopment.AttentionReviewWorkspace      = (*publicationRuntimeTestWorkspace)(nil)
	_ prdevelopment.PublicationProviderObserver   = (*publicationRuntimeTestProvider)(nil)
	_ prdevelopment.PublicationRemoteHeadObserver = (*publicationRuntimeTestProvider)(nil)
	_ prdevelopment.PublicationPinnedLinePusher   = (*publicationRuntimeTestPusher)(nil)
)
