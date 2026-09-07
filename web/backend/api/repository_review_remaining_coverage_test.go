package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/repoeval"
)

type repositoryModelEvaluationGetHookStore struct {
	base      repositoryModelEvaluationStateStore
	beforeGet func()
}

func (s *repositoryModelEvaluationGetHookStore) Create(
	ctx context.Context,
	request repoeval.CreateRequest,
) (repoeval.Evaluation, error) {
	return s.base.Create(ctx, request)
}

func (s *repositoryModelEvaluationGetHookStore) Get(
	ctx context.Context,
	id string,
) (repoeval.Evaluation, bool, error) {
	if s.beforeGet != nil {
		hook := s.beforeGet
		s.beforeGet = nil
		hook()
	}
	return s.base.Get(ctx, id)
}

func (s *repositoryModelEvaluationGetHookStore) Update(
	ctx context.Context,
	id string,
	version int64,
	mutate func(*repoeval.Evaluation) error,
) (repoeval.Evaluation, error) {
	return s.base.Update(ctx, id, version, mutate)
}

type repositoryModelEvaluationFinalizerErrorStore struct {
	base     repositoryModelEvaluationStateStore
	getCalls int
	getErrAt int
	getErr   error
}

func (s *repositoryModelEvaluationFinalizerErrorStore) Create(
	ctx context.Context,
	request repoeval.CreateRequest,
) (repoeval.Evaluation, error) {
	return s.base.Create(ctx, request)
}

func (s *repositoryModelEvaluationFinalizerErrorStore) Get(
	ctx context.Context,
	id string,
) (repoeval.Evaluation, bool, error) {
	s.getCalls++
	if s.getCalls == s.getErrAt {
		return repoeval.Evaluation{}, false, s.getErr
	}
	return s.base.Get(ctx, id)
}

func (*repositoryModelEvaluationFinalizerErrorStore) Update(
	context.Context,
	string,
	int64,
	func(*repoeval.Evaluation) error,
) (repoeval.Evaluation, error) {
	return repoeval.Evaluation{}, repoeval.ErrConflict
}

func TestRepositoryReviewRemainingProfileValidationCoverage(t *testing.T) {
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	t.Cleanup(handler.Shutdown)
	if err := handler.validateRepositoryReviewProfileSelectionWithModels(
		"api", "cheap", "", "missing-deduplicator", repoaudit.RepositoryReviewBudgetPolicy{},
	); err == nil {
		t.Fatal("missing deduplication alias was accepted")
	}
	if err := handler.validateRepositoryReviewProfileSelectionWithModels(
		"api", "cheap", "missing-writer", "", repoaudit.RepositoryReviewBudgetPolicy{},
	); err == nil {
		t.Fatal("missing issue writer alias was accepted")
	}
	if !errors.Is(
		handler.validateRepositoryReviewProfileSelectionWithModels(
			"missing-account", "cheap", "", "", repoaudit.RepositoryReviewBudgetPolicy{},
		),
		repoaudit.ErrInvalidProfile,
	) {
		t.Fatal("missing profile account did not return an invalid-profile error")
	}
}

func TestRepositoryModelEvaluationRemainingCancellationCoverage(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	handler.repositoryModelEvaluationController = controller
	t.Cleanup(handler.Shutdown)
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	base := controller.store.(repoeval.Store)

	draft, err := base.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/idempotent-active"))
	if err != nil {
		t.Fatal(err)
	}
	token, activeCtx, activeCancel, err := controller.reserveActive(draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := base.Update(t.Context(), draft.ID, draft.Version, repositoryModelEvaluationApplyCancellation)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := controller.Cancel(t.Context(), draft.ID, draft.Version)
	if err != nil || replayed.Version != canceled.Version {
		t.Fatalf("active idempotent cancel=%#v err=%v", replayed, err)
	}
	select {
	case <-activeCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("idempotent cancellation did not cancel its active context")
	}
	activeCancel()
	controller.releaseActive(draft.ID, token)

	running := seedRunningRepositoryModelEvaluation(t, controller, base, "owner/token-recheck")
	oldToken, oldCtx, oldCancel, err := controller.reserveActive(running.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacementCtx, replacementCancel := context.WithCancel(controller.ctx)
	replacementToken := "replacement-token-before-get"
	controller.store = &repositoryModelEvaluationGetHookStore{
		base: base,
		beforeGet: func() {
			controller.mu.Lock()
			controller.active[running.ID] = repositoryModelEvaluationActiveRun{
				token: replacementToken, cancel: replacementCancel,
			}
			controller.mu.Unlock()
		},
	}
	if _, cancelErr := controller.Cancel(
		t.Context(), running.ID, running.Version,
	); !errors.Is(cancelErr, repoeval.ErrConflict) {
		t.Fatalf("replacement-before-get cancellation err=%v", cancelErr)
	}
	select {
	case <-oldCtx.Done():
		t.Fatal("stale cancellation canceled the old context")
	case <-replacementCtx.Done():
		t.Fatal("stale cancellation canceled the replacement context")
	default:
	}
	oldCancel()
	replacementCancel()
	controller.releaseActive(running.ID, replacementToken)
	_ = oldToken
	controller.store = base

	if applyErr := repositoryModelEvaluationApplyCancellation(
		&repoeval.Evaluation{Status: repoeval.StatusCompleted},
	); !errors.Is(applyErr, repoeval.ErrInvalidTransition) {
		t.Fatalf("completed cancellation mutation err=%v", applyErr)
	}

	cancelingDraft, err := base.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/finalizer-errors"))
	if err != nil {
		t.Fatal(err)
	}
	preflighting, err := base.Update(t.Context(), cancelingDraft.ID, cancelingDraft.Version, func(
		value *repoeval.Evaluation,
	) error {
		value.Status = repoeval.StatusPreflighting
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	canceling, err := base.Update(
		t.Context(), preflighting.ID, preflighting.Version, repositoryModelEvaluationApplyCancellation,
	)
	if err != nil {
		t.Fatal(err)
	}
	controller.store = &repositoryModelEvaluationFaultStore{base: base, conflicts: 32}
	if _, finishErr := controller.finishCanceled(t.Context(), canceling.ID); !errors.Is(
		finishErr, repoeval.ErrConflict,
	) {
		t.Fatalf("unsettled finalizer conflict err=%v", finishErr)
	}
	wantGetErr := errors.New("injected finalizer reload failure")
	controller.store = &repositoryModelEvaluationFinalizerErrorStore{
		base: base, getErrAt: 33, getErr: wantGetErr,
	}
	if _, finishErr := controller.finishCanceled(t.Context(), canceling.ID); !errors.Is(finishErr, wantGetErr) {
		t.Fatalf("finalizer reload err=%v", finishErr)
	}
	controller.store = base
}

func TestRepositoryModelEvaluationRemainingRequestCoverage(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/model-evaluations", strings.NewReader(`{} {}`))
	var target repositoryModelEvaluationActionRequest
	if err := decodeRepositoryModelEvaluationRequest(request, &target); err == nil {
		t.Fatal("model evaluation request accepted a trailing JSON value")
	}
}

func TestRepositoryReviewRemainingStartupReconciliationCoverage(t *testing.T) {
	original := reconcileRepositoryReviewDeduplicationJobs
	t.Cleanup(func() { reconcileRepositoryReviewDeduplicationJobs = original })
	wantErr := errors.New("injected deduplication reconciliation failure")
	reconcileRepositoryReviewDeduplicationJobs = func(
		repoaudit.Store, context.Context,
	) (int, error) {
		return 0, wantErr
	}
	handler, _, _ := newRepositoryReviewAutomationTestHandler(t)
	controller := newRepositoryReviewController(handler)
	if err := controller.Start(); !errors.Is(err, wantErr) {
		t.Fatalf("controller reconciliation error=%v", err)
	}
	if controller.ctx.Err() == nil || controller.releaseLease != nil {
		t.Fatalf("failed controller retained lifecycle state: %#v", controller)
	}
	controller.Stop()
}
