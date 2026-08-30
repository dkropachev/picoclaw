package api

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoeval"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type repositoryModelEvaluationCASBlockStore struct {
	base            repositoryModelEvaluationStateStore
	status          repoeval.Status
	blockOccurrence int64
	seen            atomic.Int64
	blocked         chan struct{}
	release         chan struct{}
	blockedOnce     sync.Once
}

type repositoryModelEvaluationCancellationFixture struct {
	controller *repositoryModelEvaluationController
	base       repoeval.Store
	running    repoeval.Evaluation
	runCtx     context.Context
	block      *repositoryModelEvaluationCASBlockStore
}

func TestRepositoryModelEvaluationCancelingCannotTransitionToFailed(t *testing.T) {
	store := repoeval.NewStore(t.TempDir())
	draft, err := store.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/cancel-only"))
	if err != nil {
		t.Fatal(err)
	}
	preflighting, err := store.Update(t.Context(), draft.ID, draft.Version, func(value *repoeval.Evaluation) error {
		value.Status = repoeval.StatusPreflighting
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	canceling, err := store.Update(
		t.Context(),
		preflighting.ID,
		preflighting.Version,
		func(value *repoeval.Evaluation) error {
			value.Status = repoeval.StatusCanceling
			value.Progress.Stage = repoeval.ProgressCanceling
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(t.Context(), canceling.ID, canceling.Version, func(value *repoeval.Evaluation) error {
		value.Status = repoeval.StatusFailed
		value.Failure = "late provider failure"
		value.Progress.Stage = repoeval.ProgressFailed
		return nil
	})
	if !errors.Is(err, repoeval.ErrInvalidTransition) {
		t.Fatalf("canceling-to-failed transition error = %v", err)
	}
}

func (s *repositoryModelEvaluationCASBlockStore) Create(
	ctx context.Context,
	request repoeval.CreateRequest,
) (repoeval.Evaluation, error) {
	return s.base.Create(ctx, request)
}

func (s *repositoryModelEvaluationCASBlockStore) Get(
	ctx context.Context,
	id string,
) (repoeval.Evaluation, bool, error) {
	return s.base.Get(ctx, id)
}

func (s *repositoryModelEvaluationCASBlockStore) Update(
	ctx context.Context,
	id string,
	version int64,
	mutate func(*repoeval.Evaluation) error,
) (repoeval.Evaluation, error) {
	current, found, err := s.base.Get(ctx, id)
	if err != nil {
		return repoeval.Evaluation{}, err
	}
	if found && current.Status == s.status && s.seen.Add(1) == s.blockOccurrence {
		s.blockedOnce.Do(func() { close(s.blocked) })
		<-s.release
	}
	return s.base.Update(ctx, id, version, mutate)
}

func newRepositoryModelEvaluationCancellationFixture(
	t *testing.T,
	repository string,
) repositoryModelEvaluationCancellationFixture {
	t.Helper()
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	handler.repositoryModelEvaluationController = controller
	t.Cleanup(handler.Shutdown)
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	base, _, err := handler.repositoryModelEvaluationStore()
	if err != nil {
		t.Fatal(err)
	}
	running := seedRunningRepositoryModelEvaluation(t, controller, base, repository)
	_, runCtx, cancel, err := controller.reserveActive(running.ID)
	if err != nil {
		t.Fatal(err)
	}
	block := &repositoryModelEvaluationCASBlockStore{
		base: base, status: repoeval.StatusRunning, blockOccurrence: 1,
		blocked: make(chan struct{}), release: make(chan struct{}),
	}
	controller.store = block
	t.Cleanup(func() {
		select {
		case <-block.release:
		default:
			close(block.release)
		}
		cancel()
		controller.mu.Lock()
		active, activeFound := controller.active[running.ID]
		controller.mu.Unlock()
		if activeFound {
			active.cancel()
			controller.releaseActive(running.ID, active.token)
		}
	})
	return repositoryModelEvaluationCancellationFixture{
		controller: controller, base: base, running: running,
		runCtx: runCtx, block: block,
	}
}

type repositoryModelEvaluationCancellationResult struct {
	evaluation repoeval.Evaluation
	err        error
}

func startRepositoryModelEvaluationCancellation(
	t *testing.T,
	fixture repositoryModelEvaluationCancellationFixture,
) <-chan repositoryModelEvaluationCancellationResult {
	t.Helper()
	result := make(chan repositoryModelEvaluationCancellationResult, 1)
	go func() {
		evaluation, err := fixture.controller.Cancel(
			context.Background(), fixture.running.ID, fixture.running.Version,
		)
		result <- repositoryModelEvaluationCancellationResult{evaluation: evaluation, err: err}
	}()
	select {
	case <-fixture.block.blocked:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach the guarded CAS boundary")
	}
	return result
}

func TestRepositoryModelEvaluationCancelRetriesSameTokenVersionChurn(t *testing.T) {
	fixture := newRepositoryModelEvaluationCancellationFixture(t, "owner/cancel-version-churn")
	result := startRepositoryModelEvaluationCancellation(t, fixture)
	if _, err := fixture.base.Update(
		t.Context(), fixture.running.ID, fixture.running.Version,
		func(value *repoeval.Evaluation) error {
			value.Progress.Message = "Concurrent progress update."
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	close(fixture.block.release)
	completed := <-result
	if completed.err != nil || completed.evaluation.Status != repoeval.StatusCanceled {
		t.Fatalf("same-token cancellation=%#v err=%v", completed.evaluation, completed.err)
	}
	select {
	case <-fixture.runCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("same-token cancellation did not cancel its active context")
	}
}

func TestRepositoryModelEvaluationCancelRejectsReplacementToken(t *testing.T) {
	fixture := newRepositoryModelEvaluationCancellationFixture(t, "owner/cancel-token-replaced")
	result := startRepositoryModelEvaluationCancellation(t, fixture)
	if _, err := fixture.base.Update(
		t.Context(), fixture.running.ID, fixture.running.Version,
		func(value *repoeval.Evaluation) error {
			value.Progress.Message = "Concurrent replacement progress."
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	replacementCtx, replacementCancel := context.WithCancel(fixture.controller.ctx)
	t.Cleanup(replacementCancel)
	replacementToken := "replacement-token"
	fixture.controller.mu.Lock()
	fixture.controller.active[fixture.running.ID] = repositoryModelEvaluationActiveRun{
		token: replacementToken, cancel: replacementCancel,
	}
	fixture.controller.mu.Unlock()
	close(fixture.block.release)
	completed := <-result
	if !errors.Is(completed.err, repoeval.ErrConflict) {
		t.Fatalf("replacement-token cancellation error=%v", completed.err)
	}
	current, found, err := fixture.base.Get(t.Context(), fixture.running.ID)
	if err != nil || !found || current.Status != repoeval.StatusRunning {
		t.Fatalf("replacement-token durable state=%#v found=%v err=%v", current, found, err)
	}
	select {
	case <-replacementCtx.Done():
		t.Fatal("stale cancellation canceled the replacement token")
	default:
	}
	select {
	case <-fixture.runCtx.Done():
		t.Fatal("failed stale cancellation canceled the captured context")
	default:
	}
}

func TestRepositoryModelEvaluationCancelAcceptsConcurrentDurableCancellation(t *testing.T) {
	fixture := newRepositoryModelEvaluationCancellationFixture(t, "owner/cancel-concurrent")
	result := startRepositoryModelEvaluationCancellation(t, fixture)
	canceling, err := fixture.base.Update(
		t.Context(), fixture.running.ID, fixture.running.Version,
		repositoryModelEvaluationApplyCancellation,
	)
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := fixture.base.Update(
		t.Context(), canceling.ID, canceling.Version,
		func(value *repoeval.Evaluation) error {
			value.Status = repoeval.StatusCanceled
			value.Progress.Stage = repoeval.ProgressCanceled
			repositoryModelEvaluationClearActiveChildren(&value.Progress)
			value.Progress.Message = "Canceled at a durable boundary."
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	close(fixture.block.release)
	completed := <-result
	if completed.err != nil || completed.evaluation.Status != repoeval.StatusCanceled ||
		completed.evaluation.Version != canceled.Version ||
		!completed.evaluation.UpdatedAt.Equal(canceled.UpdatedAt) {
		t.Fatalf("concurrent durable cancellation=%#v err=%v", completed.evaluation, completed.err)
	}
	select {
	case <-fixture.runCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("concurrent durable cancellation did not cancel the captured context")
	}
}

func TestRepositoryModelEvaluationCancelAcceptsConcurrentFinalizer(t *testing.T) {
	fixture := newRepositoryModelEvaluationCancellationFixture(t, "owner/cancel-finalizer")
	fixture.block.status = repoeval.StatusCanceling
	result := startRepositoryModelEvaluationCancellation(t, fixture)
	canceling, found, err := fixture.base.Get(t.Context(), fixture.running.ID)
	if err != nil || !found || canceling.Status != repoeval.StatusCanceling {
		t.Fatalf("canceling state=%#v found=%v err=%v", canceling, found, err)
	}
	canceled, err := fixture.base.Update(
		t.Context(), canceling.ID, canceling.Version,
		func(value *repoeval.Evaluation) error {
			value.Status = repoeval.StatusCanceled
			value.Progress.Stage = repoeval.ProgressCanceled
			repositoryModelEvaluationClearActiveChildren(&value.Progress)
			value.Progress.Message = "Canceled at a durable boundary."
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	close(fixture.block.release)
	completed := <-result
	if completed.err != nil || completed.evaluation.Status != repoeval.StatusCanceled ||
		completed.evaluation.Version != canceled.Version ||
		!completed.evaluation.UpdatedAt.Equal(canceled.UpdatedAt) {
		t.Fatalf("concurrent finalizer cancellation=%#v err=%v", completed.evaluation, completed.err)
	}
	select {
	case <-fixture.runCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("concurrent finalizer did not cancel the captured context")
	}
}

func TestRepositoryModelEvaluationCancelFencesPreflightProviderResult(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	controller.runWorkflow = func(
		context.Context,
		string,
		string,
		string,
		map[string]any,
		workflows.AgentUsageEventObserver,
	) (*workflows.RunResult, error) {
		return repositoryModelEvaluationPreflightResult(), nil
	}
	handler.repositoryModelEvaluationController = controller
	t.Cleanup(handler.Shutdown)
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	base := controller.store
	block := &repositoryModelEvaluationCASBlockStore{
		base: base, status: repoeval.StatusPreflighting, blockOccurrence: 1,
		blocked: make(chan struct{}), release: make(chan struct{}),
	}
	controller.store = block
	t.Cleanup(func() {
		select {
		case <-block.release:
		default:
			close(block.release)
		}
	})

	draft, err := base.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/preflight-fence"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = controller.Preflight(t.Context(), draft.ID, draft.Version); err != nil {
		t.Fatal(err)
	}
	select {
	case <-block.blocked:
	case <-time.After(time.Second):
		t.Fatal("preflight result did not reach the guarded CAS boundary")
	}
	preflighting, found, err := base.Get(t.Context(), draft.ID)
	if err != nil || !found {
		t.Fatalf("preflight state found=%v err=%v", found, err)
	}
	canceled, err := controller.Cancel(t.Context(), draft.ID, preflighting.Version)
	if err != nil || canceled.Status != repoeval.StatusCanceled {
		t.Fatalf("cancel result=%#v err=%v", canceled, err)
	}
	close(block.release)
	waitRepositoryModelEvaluationControllerIdle(t, controller)

	final, found, err := base.Get(t.Context(), draft.ID)
	if err != nil || !found {
		t.Fatalf("final state found=%v err=%v", found, err)
	}
	if final.Status != repoeval.StatusCanceled || final.Corpus != nil || final.Failure != "" {
		t.Fatalf("fenced preflight result mutated canceled state: %#v", final)
	}
}

func TestRepositoryModelEvaluationCancelFencesFinalAnalysisResult(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	controller.runWorkflow = func(
		_ context.Context,
		_ string,
		ref string,
		_ string,
		_ map[string]any,
		_ workflows.AgentUsageEventObserver,
	) (*workflows.RunResult, error) {
		switch ref {
		case workflows.RepositoryModelEvaluationBatchWorkflowRef:
			return repositoryModelEvaluationBatchResult(), nil
		case workflows.RepositoryModelEvaluationAnalysisWorkflowRef:
			return repositoryModelEvaluationAnalysisResult(), nil
		default:
			return nil, errors.New("unexpected workflow")
		}
	}
	handler.repositoryModelEvaluationController = controller
	t.Cleanup(handler.Shutdown)
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	base := controller.store
	durableStore, _, err := handler.repositoryModelEvaluationStore()
	if err != nil {
		t.Fatal(err)
	}
	ready := seedReadyRepositoryModelEvaluation(
		t,
		controller,
		durableStore,
		"owner/final-fence",
	)
	block := &repositoryModelEvaluationCASBlockStore{
		base: base, status: repoeval.StatusAnalyzing, blockOccurrence: 1,
		blocked: make(chan struct{}), release: make(chan struct{}),
	}
	controller.store = block
	t.Cleanup(func() {
		select {
		case <-block.release:
		default:
			close(block.release)
		}
	})

	if _, startErr := controller.StartEvaluation(t.Context(), ready.ID, ready.Version); startErr != nil {
		t.Fatal(startErr)
	}
	select {
	case <-block.blocked:
	case <-time.After(time.Second):
		t.Fatal("analysis result did not reach the guarded CAS boundary")
	}
	analyzing, found, err := base.Get(t.Context(), ready.ID)
	if err != nil || !found || analyzing.Status != repoeval.StatusAnalyzing {
		t.Fatalf("analysis state=%#v found=%v err=%v", analyzing, found, err)
	}
	canceled, err := controller.Cancel(t.Context(), ready.ID, analyzing.Version)
	if err != nil || canceled.Status != repoeval.StatusCanceled {
		t.Fatalf("cancel result=%#v err=%v", canceled, err)
	}
	close(block.release)
	waitRepositoryModelEvaluationControllerIdle(t, controller)

	final, found, err := base.Get(t.Context(), ready.ID)
	if err != nil || !found {
		t.Fatalf("final state found=%v err=%v", found, err)
	}
	if final.Status != repoeval.StatusCanceled || len(final.Comparisons) != 0 || final.Failure != "" {
		t.Fatalf("fenced analysis result mutated canceled state: %#v", final)
	}
}

func TestRepositoryModelEvaluationTimedOutStopRetainsControllerLease(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Stop)
	token, _, cancel, err := controller.reserveActive("lease-fence")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	t.Cleanup(func() { controller.releaseActive("lease-fence", token) })
	controller.stopTimeout = time.Millisecond
	leaseReleased := make(chan struct{})
	controller.lifecycleMu.Lock()
	releaseLease := controller.releaseLease
	controller.releaseLease = func() {
		releaseLease()
		close(leaseReleased)
	}
	controller.lifecycleMu.Unlock()

	stopReturned := make(chan struct{})
	go func() {
		controller.Stop()
		close(stopReturned)
	}()
	select {
	case <-stopReturned:
	case <-time.After(time.Second):
		t.Fatal("timed-out Stop did not return")
	}
	select {
	case <-leaseReleased:
		t.Fatal("Stop released the controller lease before its reserved run drained")
	default:
	}

	blocked := newRepositoryModelEvaluationController(NewHandler(handler.configPath))
	if err := blocked.Start(); !errors.Is(err, repoeval.ErrControllerLocked) {
		t.Fatalf("new controller acquired an old controller's live lease: %v", err)
	}
	blocked.Stop()

	controller.releaseActive("lease-fence", token)
	select {
	case <-leaseReleased:
	case <-time.After(time.Second):
		t.Fatal("controller lease was not released after its reserved run drained")
	}

	replacement := newRepositoryModelEvaluationController(NewHandler(handler.configPath))
	if err := replacement.Start(); err != nil {
		t.Fatalf("replacement controller did not acquire the drained lease: %v", err)
	}
	replacement.Stop()
}

func waitRepositoryModelEvaluationControllerIdle(
	t *testing.T,
	controller *repositoryModelEvaluationController,
) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		controller.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("repository model evaluation worker did not exit")
	}
}
