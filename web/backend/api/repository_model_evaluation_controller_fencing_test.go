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
