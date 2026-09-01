package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoeval"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

type repositoryModelEvaluationFaultStore struct {
	base         repoeval.Store
	getErr       error
	getMissing   bool
	getValue     *repoeval.Evaluation
	updateErr    error
	createErr    error
	conflicts    int
	updateCalls  int
	failAt       map[int]error
	beforeUpdate func(int)
}

func (s *repositoryModelEvaluationFaultStore) Create(
	ctx context.Context,
	request repoeval.CreateRequest,
) (repoeval.Evaluation, error) {
	if s.createErr != nil {
		return repoeval.Evaluation{}, s.createErr
	}
	return s.base.Create(ctx, request)
}

func (s *repositoryModelEvaluationFaultStore) Get(
	ctx context.Context,
	id string,
) (repoeval.Evaluation, bool, error) {
	if s.getErr != nil {
		return repoeval.Evaluation{}, false, s.getErr
	}
	if s.getMissing {
		return repoeval.Evaluation{}, false, nil
	}
	if s.getValue != nil {
		return repoeval.Clone(*s.getValue), true, nil
	}
	return s.base.Get(ctx, id)
}

func (s *repositoryModelEvaluationFaultStore) Update(
	ctx context.Context,
	id string,
	version int64,
	mutate func(*repoeval.Evaluation) error,
) (repoeval.Evaluation, error) {
	s.updateCalls++
	if s.beforeUpdate != nil {
		s.beforeUpdate(s.updateCalls)
	}
	if injected := s.failAt[s.updateCalls]; injected != nil {
		return repoeval.Evaluation{}, injected
	}
	if s.conflicts > 0 {
		s.conflicts--
		return repoeval.Evaluation{}, repoeval.ErrConflict
	}
	if s.updateErr != nil {
		return repoeval.Evaluation{}, s.updateErr
	}
	return s.base.Update(ctx, id, version, mutate)
}

func TestRepositoryModelEvaluationControllerLifecycleCoverage(t *testing.T) {
	var nilController *repositoryModelEvaluationController
	if err := nilController.Start(); err == nil {
		t.Fatal("nil controller started")
	}
	nilController.Stop()
	if _, err := nilController.config(); err == nil {
		t.Fatal("nil controller loaded config")
	}

	handler, _, workspace := newRepositoryModelEvaluationTestHandler(t)
	createdController, err := handler.ensureRepositoryModelEvaluationController()
	if err != nil || createdController == nil {
		t.Fatalf("ensure controller=%v err=%v", createdController, err)
	}
	handler.stopRepositoryModelEvaluationController()
	runtimeController := newRepositoryModelEvaluationController(handler)
	result, err := runtimeController.runWorkflowRuntime(
		t.Context(),
		`name: Controller coverage
on: {workflow_call: {}}
jobs:
  main:
    runs-on: picoclaw
    steps:
      - uses: function/workflow.state
        with: {action: set, key: controller_coverage, value: ok}
`,
		"workflows/controller-coverage.yml",
		workflows.NewRunID(),
		nil,
		nil,
	)
	if err != nil || result == nil || result.Status != workflows.RunStatusSucceeded {
		t.Fatalf("native runtime result=%#v err=%v", result, err)
	}

	badConfigPath := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(badConfigPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	badHandler := NewHandler(badConfigPath)
	if _, err := badHandler.ensureRepositoryModelEvaluationController(); err == nil {
		t.Fatal("missing config controller started")
	}
	badHandler.stopRepositoryModelEvaluationController()

	corruptConfigPath := filepath.Join(t.TempDir(), "config.json")
	writeRepositoryModelEvaluationTestConfig(t, corruptConfigPath, workspace)
	stateRoot := filepath.Join(workspace, "repository_evaluations")
	if _, err := repoeval.NewSQLiteStore(workspace).List(t.Context()); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(stateRoot, "evaluations.db")
	if err := os.WriteFile(statePath, []byte("not-sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	corruptController := newRepositoryModelEvaluationController(NewHandler(corruptConfigPath))
	if err := corruptController.Start(); err == nil {
		t.Fatal("controller accepted corrupt durable catalog")
	}
	if err := corruptController.Start(); err == nil {
		t.Fatal("controller lost cached startup error")
	}
	corruptController.Stop()

	timeoutCtx, timeoutCancel := context.WithCancel(context.Background())
	timeoutController := &repositoryModelEvaluationController{
		ctx: timeoutCtx, cancel: timeoutCancel, stopTimeout: time.Millisecond,
	}
	timeoutController.wg.Add(1)
	workerDone := make(chan struct{})
	released := make(chan struct{})
	timeoutController.releaseLease = func() { close(released) }
	go func() {
		<-workerDone
		timeoutController.wg.Done()
	}()
	timeoutController.Stop()
	select {
	case <-released:
		t.Fatal("timed-out controller released its lease while a worker was active")
	default:
	}
	close(workerDone)
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("controller did not release its lease after the worker exited")
	}
	timeoutController.Stop()

	mismatch := newRepositoryModelEvaluationController(handler)
	mismatch.workspace = filepath.Join(workspace, "different")
	if _, err := mismatch.config(); err == nil {
		t.Fatal("workspace mismatch was accepted")
	}
	stopped := newRepositoryModelEvaluationController(handler)
	stopped.Stop()
	for name, invoke := range map[string]func() error{
		"preflight": func() error { _, actionErr := stopped.Preflight(t.Context(), "id", 1); return actionErr },
		"run existing": func() error {
			_, actionErr := stopped.RunExisting(t.Context(), "id", 1)
			return actionErr
		},
		"run new": func() error {
			_, actionErr := stopped.Run(t.Context(), repositoryModelEvaluationCreateRequest("owner/stopped"))
			return actionErr
		},
		"start":   func() error { _, actionErr := stopped.StartEvaluation(t.Context(), "id", 1); return actionErr },
		"cancel":  func() error { _, actionErr := stopped.Cancel(t.Context(), "id", 1); return actionErr },
		"resume":  func() error { _, actionErr := stopped.Resume(t.Context(), "id", 1); return actionErr },
		"restart": func() error { _, actionErr := stopped.Restart(t.Context(), "id", 1); return actionErr },
	} {
		if branchErr := invoke(); !errors.Is(branchErr, context.Canceled) {
			t.Fatalf("stopped %s error=%v", name, branchErr)
		}
	}
}

func TestRepositoryModelEvaluationControllerActionErrorCoverage(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	controller.runWorkflow = blockingRepositoryModelEvaluationWorkflow
	handler.repositoryModelEvaluationController = controller
	t.Cleanup(handler.Shutdown)
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	base := controller.store.(repoeval.Store)
	missingID := "rme_" + strings.Repeat("f", 32)

	for name, invoke := range map[string]func() error{
		"preflight": func() error { _, actionErr := controller.Preflight(t.Context(), missingID, 1); return actionErr },
		"run":       func() error { _, actionErr := controller.RunExisting(t.Context(), missingID, 1); return actionErr },
		"start":     func() error { _, actionErr := controller.StartEvaluation(t.Context(), missingID, 1); return actionErr },
		"cancel":    func() error { _, actionErr := controller.Cancel(t.Context(), missingID, 1); return actionErr },
		"resume":    func() error { _, actionErr := controller.Resume(t.Context(), missingID, 1); return actionErr },
		"restart":   func() error { _, actionErr := controller.Restart(t.Context(), missingID, 1); return actionErr },
	} {
		if branchErr := invoke(); !errors.Is(branchErr, os.ErrNotExist) {
			t.Fatalf("%s missing error=%v", name, branchErr)
		}
	}

	draft, err := base.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/actions"))
	if err != nil {
		t.Fatal(err)
	}
	for name, invoke := range map[string]func() error{
		"preflight": func() error {
			_, actionErr := controller.Preflight(t.Context(), draft.ID, draft.Version+1)
			return actionErr
		},
		"run": func() error {
			_, actionErr := controller.RunExisting(t.Context(), draft.ID, draft.Version+1)
			return actionErr
		},
		"start": func() error {
			_, actionErr := controller.StartEvaluation(t.Context(), draft.ID, draft.Version+1)
			return actionErr
		},
		"cancel": func() error {
			_, actionErr := controller.Cancel(t.Context(), draft.ID, draft.Version+1)
			return actionErr
		},
		"resume": func() error {
			_, actionErr := controller.Resume(t.Context(), draft.ID, draft.Version+1)
			return actionErr
		},
		"restart": func() error {
			_, actionErr := controller.Restart(t.Context(), draft.ID, draft.Version+1)
			return actionErr
		},
	} {
		if branchErr := invoke(); !errors.Is(branchErr, repoeval.ErrConflict) {
			t.Fatalf("%s stale error=%v", name, branchErr)
		}
	}
	if _, branchErr := controller.StartEvaluation(t.Context(), draft.ID, draft.Version); !errors.Is(
		branchErr,
		repoeval.ErrInvalidTransition,
	) {
		t.Fatalf("draft start error=%v", branchErr)
	}
	busyToken, _, busyCancel, actionErr := controller.reserveActive(draft.ID)
	if actionErr != nil {
		t.Fatal(actionErr)
	}
	if _, branchErr := controller.Preflight(t.Context(), draft.ID, draft.Version); !errors.Is(
		branchErr,
		errRepositoryModelEvaluationBusy,
	) {
		t.Fatalf("busy preflight error=%v", branchErr)
	}
	if _, branchErr := controller.launchCreatedPreflight(draft, "wr_busy_launch"); !errors.Is(
		branchErr,
		errRepositoryModelEvaluationBusy,
	) {
		t.Fatalf("busy atomic launch error=%v", branchErr)
	}
	busyCancel()
	controller.releaseActive(draft.ID, busyToken)
	if initializeErr := controller.initializeReadyEvaluation(nil); !errors.Is(
		initializeErr,
		repoeval.ErrInvalidTransition,
	) {
		t.Fatalf("nil ready initialization error=%v", initializeErr)
	}

	terminal, actionErr := controller.Cancel(t.Context(), draft.ID, draft.Version)
	if actionErr != nil {
		t.Fatal(actionErr)
	}
	if _, branchErr := controller.Preflight(t.Context(), terminal.ID, terminal.Version); !errors.Is(
		branchErr,
		repoeval.ErrInvalidTransition,
	) {
		t.Fatalf("terminal preflight error=%v", branchErr)
	}
	if _, branchErr := controller.RunExisting(t.Context(), terminal.ID, terminal.Version); !errors.Is(
		branchErr,
		repoeval.ErrInvalidTransition,
	) {
		t.Fatalf("terminal run error=%v", branchErr)
	}
	replayedCancellation, branchErr := controller.Cancel(t.Context(), terminal.ID, draft.Version)
	if branchErr != nil || replayedCancellation.Status != repoeval.StatusCanceled ||
		replayedCancellation.Version != terminal.Version ||
		!replayedCancellation.UpdatedAt.Equal(terminal.UpdatedAt) {
		t.Fatalf("idempotent terminal cancel=%#v error=%v", replayedCancellation, branchErr)
	}
	if _, branchErr = controller.Cancel(
		t.Context(), terminal.ID, terminal.Version+1,
	); !errors.Is(branchErr, repoeval.ErrConflict) {
		t.Fatalf("future terminal cancel error=%v", branchErr)
	}
	terminalToken, _, terminalCancel, actionErr := controller.reserveActive(terminal.ID)
	if actionErr != nil {
		t.Fatal(actionErr)
	}
	if _, branchErr := controller.Resume(t.Context(), terminal.ID, terminal.Version); !errors.Is(
		branchErr,
		repoeval.ErrInvalidTransition,
	) {
		t.Fatalf("busy terminal resume error=%v", branchErr)
	}
	terminalCancel()
	controller.releaseActive(terminal.ID, terminalToken)

	preflightDraft, err := base.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/inflight"))
	if err != nil {
		t.Fatal(err)
	}
	preflighting, err := base.Update(
		t.Context(),
		preflightDraft.ID,
		preflightDraft.Version,
		func(value *repoeval.Evaluation) error {
			value.Status = repoeval.StatusPreflighting
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, branchErr := controller.Restart(t.Context(), preflighting.ID, preflighting.Version); !errors.Is(
		branchErr,
		repoeval.ErrInvalidTransition,
	) {
		t.Fatalf("in-flight restart error=%v", branchErr)
	}

	canceling, err := base.Update(
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
	canceled, actionErr := controller.Cancel(t.Context(), canceling.ID, canceling.Version)
	if actionErr != nil || canceled.Status != repoeval.StatusCanceled {
		t.Fatalf("finish canceling=%#v err=%v", canceled, actionErr)
	}

	ready := seedReadyRepositoryModelEvaluation(t, controller, base, "owner/busy-ready")
	readyToken, _, readyCancel, actionErr := controller.reserveActive(ready.ID)
	if actionErr != nil {
		t.Fatal(actionErr)
	}
	if _, branchErr := controller.StartEvaluation(t.Context(), ready.ID, ready.Version); !errors.Is(
		branchErr,
		errRepositoryModelEvaluationBusy,
	) {
		t.Fatalf("busy start error=%v", branchErr)
	}
	readyCancel()
	controller.releaseActive(ready.ID, readyToken)

	getFailure := errors.New("injected get failure")
	controller.store = &repositoryModelEvaluationFaultStore{base: base, getErr: getFailure}
	if _, branchErr := controller.startReadyEvaluationActive(
		t.Context(),
		draft.ID,
		"token",
	); !errors.Is(branchErr, getFailure) {
		t.Fatalf("ready get failure=%v", branchErr)
	}
	for name, invoke := range map[string]func() error{
		"preflight": func() error {
			_, invokeErr := controller.Preflight(t.Context(), draft.ID, draft.Version)
			return invokeErr
		},
		"start": func() error {
			_, invokeErr := controller.StartEvaluation(t.Context(), draft.ID, draft.Version)
			return invokeErr
		},
		"cancel": func() error {
			_, invokeErr := controller.Cancel(t.Context(), draft.ID, draft.Version)
			return invokeErr
		},
		"resume": func() error {
			_, invokeErr := controller.Resume(t.Context(), draft.ID, draft.Version)
			return invokeErr
		},
		"restart": func() error {
			_, invokeErr := controller.Restart(t.Context(), draft.ID, draft.Version)
			return invokeErr
		},
	} {
		if branchErr := invoke(); !errors.Is(branchErr, getFailure) {
			t.Fatalf("%s get failure=%v", name, branchErr)
		}
	}
	controller.store = &repositoryModelEvaluationFaultStore{base: base, getMissing: true}
	if _, branchErr := controller.startReadyEvaluationActive(
		t.Context(),
		ready.ID,
		"token",
	); !errors.Is(branchErr, os.ErrNotExist) {
		t.Fatalf("ready missing error=%v", branchErr)
	}

	controller.store = base
	orphanDraft, err := base.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/orphan-preflight"))
	if err != nil {
		t.Fatal(err)
	}
	orphanPreflight, err := base.Update(
		t.Context(),
		orphanDraft.ID,
		orphanDraft.Version,
		func(value *repoeval.Evaluation) error {
			value.Status = repoeval.StatusPreflighting
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, resumeErr := controller.Resume(
		t.Context(),
		orphanPreflight.ID,
		orphanPreflight.Version,
	); !errors.Is(resumeErr, repoeval.ErrInvalidTransition) {
		t.Fatalf("orphan preflight resume err=%v", resumeErr)
	}
	orphanReady := seedReadyRepositoryModelEvaluation(t, controller, base, "owner/orphan-running")
	orphanRunning, err := base.Update(
		t.Context(),
		orphanReady.ID,
		orphanReady.Version,
		func(value *repoeval.Evaluation) error {
			value.Status = repoeval.StatusRunning
			value.Checkpoint = repoeval.Checkpoint{ConcreteModels: make(map[string]map[string]int)}
			value.ModelStats = make(map[string]repoeval.ModelStats)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, resumeErr := controller.Resume(
		t.Context(),
		orphanRunning.ID,
		orphanRunning.Version,
	); !errors.Is(resumeErr, repoeval.ErrInvalidTransition) {
		t.Fatalf("orphan running resume err=%v", resumeErr)
	}
	failedResume := seedFailedReadyRepositoryModelEvaluation(t, controller, base, "owner/direct-failed-resume")
	failedToken, _, failedCancel, actionErr := controller.reserveActive(failedResume.ID)
	if actionErr != nil {
		t.Fatal(actionErr)
	}
	if _, branchErr := controller.Resume(t.Context(), failedResume.ID, failedResume.Version); !errors.Is(
		branchErr,
		errRepositoryModelEvaluationBusy,
	) {
		t.Fatalf("busy failed resume error=%v", branchErr)
	}
	failedCancel()
	controller.releaseActive(failedResume.ID, failedToken)
	if resumed, resumeErr := controller.Resume(t.Context(), failedResume.ID, failedResume.Version); resumeErr != nil ||
		resumed.Status != repoeval.StatusRunning {
		t.Fatalf("failed resume=%#v err=%v", resumed, resumeErr)
	}
	controller.mu.Lock()
	for _, active := range controller.active {
		active.cancel()
	}
	controller.mu.Unlock()
	controller.wg.Wait()
	blockedToken, _, blockedCancel, actionErr := controller.reserveActive("blocked-recovery")
	if actionErr != nil {
		t.Fatal(actionErr)
	}
	controller.recoverPreflight("blocked-recovery")
	controller.recoverEvaluation("blocked-recovery")
	controller.recoverReadyEvaluation("blocked-recovery")
	blockedCancel()
	controller.releaseActive("blocked-recovery", blockedToken)
	missingToken, missingCtx, _, actionErr := controller.reserveActive(missingID)
	if actionErr != nil {
		t.Fatal(actionErr)
	}
	controller.wg.Add(1)
	controller.executePreflight(missingCtx, missingID, missingToken, "wr_missing_preflight")
}

func TestRepositoryModelEvaluationControllerFaultStoreCoverage(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	controller.runWorkflow = blockingRepositoryModelEvaluationWorkflow
	handler.repositoryModelEvaluationController = controller
	t.Cleanup(handler.Shutdown)
	if err := controller.Start(); err != nil {
		t.Fatal(err)
	}
	base := controller.store.(repoeval.Store)
	draft, err := base.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/faults"))
	if err != nil {
		t.Fatal(err)
	}
	updateFailure := errors.New("injected update failure")
	faults := &repositoryModelEvaluationFaultStore{base: base, updateErr: updateFailure}
	controller.store = faults
	if _, branchErr := controller.Preflight(
		t.Context(),
		draft.ID,
		draft.Version,
	); !errors.Is(
		branchErr,
		updateFailure,
	) {
		t.Fatalf("preflight update error=%v", branchErr)
	}
	if len(controller.active) != 0 {
		t.Fatal("preflight update failure leaked active reservation")
	}
	controller.recoverPreflight(draft.ID)
	if len(controller.active) != 0 {
		t.Fatal("failed preflight recovery leaked active reservation")
	}
	if _, branchErr := controller.Cancel(t.Context(), draft.ID, draft.Version); !errors.Is(branchErr, updateFailure) {
		t.Fatalf("cancel update error=%v", branchErr)
	}

	controller.store = base
	ready := seedReadyRepositoryModelEvaluation(t, controller, base, "owner/ready-fault")
	controller.store = faults
	if _, branchErr := controller.StartEvaluation(
		t.Context(),
		ready.ID,
		ready.Version,
	); !errors.Is(
		branchErr,
		updateFailure,
	) {
		t.Fatalf("start update error=%v", branchErr)
	}

	controller.store = base
	canceledDraft, err := base.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/resume-fault"))
	if err != nil {
		t.Fatal(err)
	}
	canceledDraft, err = base.Update(
		t.Context(),
		canceledDraft.ID,
		canceledDraft.Version,
		func(value *repoeval.Evaluation) error {
			value.Status = repoeval.StatusPreflighting
			value.Progress.Stage = repoeval.ProgressResolving
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	canceledDraft, err = base.Update(
		t.Context(),
		canceledDraft.ID,
		canceledDraft.Version,
		func(value *repoeval.Evaluation) error {
			value.Status = repoeval.StatusFailed
			value.Progress.Stage = repoeval.ProgressFailed
			value.Failure = "preflight failed"
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	controller.store = faults
	if _, branchErr := controller.Resume(
		t.Context(),
		canceledDraft.ID,
		canceledDraft.Version,
	); !errors.Is(
		branchErr,
		updateFailure,
	) {
		t.Fatalf("preflight resume update error=%v", branchErr)
	}

	controller.store = base
	failedReady := seedReadyRepositoryModelEvaluation(t, controller, base, "owner/execution-resume-fault")
	failedReady, err = base.Update(
		t.Context(),
		failedReady.ID,
		failedReady.Version,
		func(value *repoeval.Evaluation) error {
			value.Status = repoeval.StatusRunning
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	failedReady, err = base.Update(
		t.Context(),
		failedReady.ID,
		failedReady.Version,
		func(value *repoeval.Evaluation) error {
			value.Status = repoeval.StatusFailed
			value.Failure = "failed"
			value.Progress.Stage = repoeval.ProgressFailed
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	controller.store = faults
	if _, branchErr := controller.Resume(
		t.Context(),
		failedReady.ID,
		failedReady.Version,
	); !errors.Is(
		branchErr,
		updateFailure,
	) {
		t.Fatalf("execution resume update error=%v", branchErr)
	}

	createFailure := errors.New("injected create failure")
	controller.store = &repositoryModelEvaluationFaultStore{base: base, createErr: createFailure}
	if _, branchErr := controller.Run(
		t.Context(),
		repositoryModelEvaluationCreateRequest("owner/run-create-fault"),
	); !errors.Is(branchErr, createFailure) {
		t.Fatalf("run create error=%v", branchErr)
	}
	if _, branchErr := controller.Restart(
		t.Context(),
		canceledDraft.ID,
		canceledDraft.Version,
	); !errors.Is(
		branchErr,
		createFailure,
	) {
		t.Fatalf("restart create error=%v", branchErr)
	}

	controller.store = &repositoryModelEvaluationFaultStore{base: base, getMissing: true}
	if _, branchErr := controller.updateLatest(
		t.Context(),
		draft.ID,
		func(*repoeval.Evaluation) error { return nil },
	); !errors.Is(
		branchErr,
		os.ErrNotExist,
	) {
		t.Fatalf("updateLatest missing error=%v", branchErr)
	}
	getFailure := errors.New("injected updateLatest get failure")
	controller.store = &repositoryModelEvaluationFaultStore{base: base, getErr: getFailure}
	if _, branchErr := controller.updateLatest(
		t.Context(),
		draft.ID,
		func(*repoeval.Evaluation) error { return nil },
	); !errors.Is(
		branchErr,
		getFailure,
	) {
		t.Fatalf("updateLatest get error=%v", branchErr)
	}
	controller.store = &repositoryModelEvaluationFaultStore{base: base, conflicts: 32}
	if _, branchErr := controller.updateLatest(
		t.Context(),
		draft.ID,
		func(*repoeval.Evaluation) error { return nil },
	); !errors.Is(
		branchErr,
		repoeval.ErrConflict,
	) {
		t.Fatalf("updateLatest conflict exhaustion=%v", err)
	}
	controller.store = base
	controller.recoverPreflight(draft.ID)
	if len(controller.active) != 0 {
		t.Fatal("invalid-status recovery leaked active reservation")
	}
}

func TestRepositoryModelEvaluationPreflightManifestCoverage(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	evaluation := repoeval.Evaluation{
		CandidateModels: []string{"model-a", "model-b"}, JudgeModelAlias: "model-a",
		DefaultFilesPerLanguage: 20, FilesPerLanguage: map[string]int{},
	}
	outputs := repositoryModelEvaluationPreflightResult().Outputs
	delete(outputs, "commit")
	delete(outputs, "inventoryHash")
	catalog := outputs["catalog"].(map[string]any)
	catalog["candidatePoolTruncated"] = true
	catalog["counts"].(map[string]any)["eligibleFiles"] = 0
	outputs["selector"].(map[string]any)["warnings"] = []string{"selector warning", "selector warning"}
	manifest, progress, warnings, actionErr := controller.preflightManifest(evaluation, "wr_manifest", outputs)
	if actionErr != nil {
		t.Fatal(actionErr)
	}
	if manifest.CommitSHA == "" || manifest.InventoryHash == "" || progress.TotalFiles != 2 || len(warnings) < 4 {
		t.Fatalf("manifest=%#v progress=%#v warnings=%v", manifest, progress, warnings)
	}
	if _, _, _, actionErr := controller.preflightManifest(evaluation, "wr_empty", map[string]any{
		"selection": map[string]any{"selected": make(chan int)},
	}); actionErr == nil {
		t.Fatal("unmarshalable preflight selection accepted")
	}
	if repositoryModelEvaluationBatches(repoeval.Evaluation{}) != nil {
		t.Fatal("nil corpus produced evaluation batches")
	}
}

func TestRepositoryModelEvaluationPreflightExecutionEdgeCoverage(t *testing.T) {
	for name, stateStore := range map[string]repositoryModelEvaluationStateStore{
		"missing":   &repositoryModelEvaluationFaultStore{getMissing: true},
		"get error": &repositoryModelEvaluationFaultStore{getErr: errors.New("preflight get failed")},
	} {
		t.Run(name, func(t *testing.T) {
			controller := newRepositoryModelEvaluationController(NewHandler("unused"))
			controller.store = stateStore
			controller.runWorkflow = successfulRepositoryModelEvaluationWorkflow
			token, runCtx, _, actionErr := controller.reserveActive("missing")
			if actionErr != nil {
				t.Fatal(actionErr)
			}
			controller.wg.Add(1)
			controller.executePreflight(runCtx, "missing", token, "wr_missing")
		})
	}

	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	base, _, err := handler.repositoryModelEvaluationStore()
	if err != nil {
		t.Fatal(err)
	}
	draft, err := base.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/parse-failure"))
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := base.Update(t.Context(), draft.ID, draft.Version, func(value *repoeval.Evaluation) error {
		value.Status = repoeval.StatusPreflighting
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	controller.store = base
	controller.runWorkflow = func(
		context.Context,
		string,
		string,
		string,
		map[string]any,
		workflows.AgentUsageEventObserver,
	) (*workflows.RunResult, error) {
		return &workflows.RunResult{
			Status:  workflows.RunStatusSucceeded,
			Outputs: map[string]any{"selection": map[string]any{"selected": []any{}}},
		}, nil
	}
	token, runCtx, _, actionErr := controller.reserveActive(preflight.ID)
	if actionErr != nil {
		t.Fatal(actionErr)
	}
	controller.wg.Add(1)
	controller.executePreflight(runCtx, preflight.ID, token, "wr_parse_failure")
	failed, found, err := base.Get(t.Context(), preflight.ID)
	if err != nil || !found || failed.Status != repoeval.StatusFailed {
		t.Fatalf("parse failure state=%#v found=%v err=%v", failed, found, err)
	}
	if got := anyMap(map[string]string{"value": "ok"}); got["value"] != "ok" {
		t.Fatalf("decoded map=%v", got)
	}
}

func TestRepositoryModelEvaluationPreflightPersistenceFailureCoverage(t *testing.T) {
	for _, failureCall := range []int{1, 2} {
		t.Run(fmt.Sprintf("update-%d", failureCall), func(t *testing.T) {
			handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
			controller := newRepositoryModelEvaluationController(handler)
			base, _, err := handler.repositoryModelEvaluationStore()
			if err != nil {
				t.Fatal(err)
			}
			request := repositoryModelEvaluationCreateRequest(
				fmt.Sprintf("owner/preflight-update-%d", failureCall),
			)
			request.OneShot = true
			request.InitialRunID = workflows.NewRunID()
			preflighting, err := base.Create(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			controller.store = &repositoryModelEvaluationFaultStore{
				base:   base,
				failAt: map[int]error{failureCall: errors.New("injected preflight persistence failure")},
			}
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
			token, runCtx, _, reserveErr := controller.reserveActive(preflighting.ID)
			if reserveErr != nil {
				t.Fatal(reserveErr)
			}
			controller.wg.Add(1)
			controller.executePreflight(runCtx, preflighting.ID, token, workflows.NewRunID())
			failed, found, getErr := base.Get(t.Context(), preflighting.ID)
			if getErr != nil || !found || failed.Status != repoeval.StatusFailed {
				t.Fatalf("failed preflight=%#v found=%v err=%v", failed, found, getErr)
			}
		})
	}
}

func TestRepositoryModelEvaluationExecutionStructuredFailureCoverage(t *testing.T) {
	tests := []struct {
		name     string
		batch    func() *workflows.RunResult
		analysis func() *workflows.RunResult
		want     string
	}{
		{
			name: "judge encoding",
			batch: func() *workflows.RunResult {
				result := repositoryModelEvaluationBatchResult()
				result.Outputs["judge"] = make(chan int)
				return result
			},
			want: "judge output",
		},
		{
			name: "mapping encoding",
			batch: func() *workflows.RunResult {
				result := repositoryModelEvaluationBatchResult()
				result.Outputs["mapping"] = make(chan int)
				return result
			},
			want: "candidate mapping",
		},
		{
			name:  "analysis encoding",
			batch: repositoryModelEvaluationBatchResult,
			analysis: func() *workflows.RunResult {
				return &workflows.RunResult{
					Status:  workflows.RunStatusSucceeded,
					Outputs: map[string]any{"comparison": make(chan int)},
				}
			},
			want: "invalid repository evaluation analysis",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, mux, _ := newRepositoryModelEvaluationTestHandler(t)
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
				case workflows.RepositoryModelEvaluationPreflightWorkflowRef:
					return repositoryModelEvaluationPreflightResult(), nil
				case workflows.RepositoryModelEvaluationBatchWorkflowRef:
					return test.batch(), nil
				case workflows.RepositoryModelEvaluationAnalysisWorkflowRef:
					return test.analysis(), nil
				default:
					return nil, errors.New("unexpected workflow")
				}
			}
			handler.repositoryModelEvaluationController = controller
			t.Cleanup(handler.Shutdown)
			created := createRepositoryModelEvaluation(t, mux, "owner/structured-failure")
			repositoryModelEvaluationMutation(
				t,
				mux,
				http.MethodPost,
				"/api/model-evaluations/"+created.ID+"/preflight",
				map[string]any{"expected_version": created.Version},
			)
			ready := waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusReady)
			repositoryModelEvaluationMutation(
				t,
				mux,
				http.MethodPost,
				"/api/model-evaluations/"+created.ID+"/start",
				map[string]any{"expected_version": ready.Version},
			)
			failed := waitRepositoryModelEvaluationStatus(t, handler, created.ID, repoeval.StatusFailed)
			if !strings.Contains(failed.Failure, test.want) {
				t.Fatalf("failure=%q want substring=%q", failed.Failure, test.want)
			}
		})
	}
}

func TestRepositoryModelEvaluationComparisonEdgeCoverage(t *testing.T) {
	handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
	controller := newRepositoryModelEvaluationController(handler)
	store, _, err := handler.repositoryModelEvaluationStore()
	if err != nil {
		t.Fatal(err)
	}
	ready := seedReadyRepositoryModelEvaluation(t, controller, store, "owner/comparison-edges")
	ready.CandidateModels = []string{"model-a", "model-b", "model-c"}
	ready.ModelStats = map[string]repoeval.ModelStats{
		"model-a": {}, "model-b": {}, "model-c": {},
	}
	ready.Checkpoint = repoeval.Checkpoint{
		ConcreteModels: map[string]map[string]int{"model-a": {"gpt-a": 1}},
		Batches: []repoeval.BatchCheckpoint{
			{MappingJSON: "not-json", JudgeJSON: `[]`},
		},
	}
	score := 70.0
	rows, _, err := repositoryModelEvaluationComparisons(ready, map[string]any{
		"comparisons": []map[string]any{
			{"modelAlias": "", "completion": "completed", "overallScore": score},
			{"modelAlias": "model-a", "completion": "unknown", "overallScore": score},
			{
				"modelAlias": "model-b", "completion": "partial", "overallScore": score,
				"scores": map[string]float64{"quality": 70},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].Completion != repoeval.ModelCompletionFailed || rows[0].OverallScore != nil ||
		rows[1].Completion != repoeval.ModelCompletionFailed || rows[1].Failure == "" ||
		rows[1].OverallScore != nil || len(rows[1].Scores) != 0 ||
		rows[2].Completion != repoeval.ModelCompletionFailed || !strings.Contains(rows[2].Failure, "omitted") {
		t.Fatalf("edge comparisons=%#v", rows)
	}
	if _, _, err := repositoryModelEvaluationComparisons(ready, make(chan int)); err == nil {
		t.Fatal("unmarshalable analysis comparison accepted")
	}
}

func TestRepositoryModelEvaluationConfigurationAndAliasErrorCoverage(t *testing.T) {
	t.Run("configuration errors", func(t *testing.T) {
		handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
		controller := newRepositoryModelEvaluationController(handler)
		controller.runWorkflow = blockingRepositoryModelEvaluationWorkflow
		handler.repositoryModelEvaluationController = controller
		t.Cleanup(handler.Shutdown)
		if err := controller.Start(); err != nil {
			t.Fatal(err)
		}
		store := controller.store.(repoeval.Store)
		draft, err := store.Create(t.Context(), repositoryModelEvaluationCreateRequest("owner/config-draft"))
		if err != nil {
			t.Fatal(err)
		}
		ready := seedReadyRepositoryModelEvaluation(t, controller, store, "owner/config-ready")
		failedPreflight, err := store.Create(
			t.Context(),
			repositoryModelEvaluationCreateRequest("owner/config-preflight-failed"),
		)
		if err != nil {
			t.Fatal(err)
		}
		failedPreflight, err = store.Update(
			t.Context(),
			failedPreflight.ID,
			failedPreflight.Version,
			func(value *repoeval.Evaluation) error {
				value.Status = repoeval.StatusPreflighting
				value.Progress.Stage = repoeval.ProgressResolving
				return nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		failedPreflight, err = store.Update(
			t.Context(),
			failedPreflight.ID,
			failedPreflight.Version,
			func(value *repoeval.Evaluation) error {
				value.Status = repoeval.StatusFailed
				value.Progress.Stage = repoeval.ProgressFailed
				value.Failure = "preflight failed"
				return nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		failed := seedFailedReadyRepositoryModelEvaluation(t, controller, store, "owner/config-failed")
		if err := os.WriteFile(handler.configPath, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		for name, invoke := range map[string]func() error{
			"preflight": func() error {
				_, actionErr := controller.Preflight(t.Context(), draft.ID, draft.Version)
				return actionErr
			},
			"run": func() error {
				_, actionErr := controller.Run(
					t.Context(),
					repositoryModelEvaluationCreateRequest("owner/config-run"),
				)
				return actionErr
			},
			"start": func() error {
				_, actionErr := controller.StartEvaluation(t.Context(), ready.ID, ready.Version)
				return actionErr
			},
			"resume preflight": func() error {
				_, actionErr := controller.Resume(t.Context(), failedPreflight.ID, failedPreflight.Version)
				return actionErr
			},
			"resume execution": func() error {
				_, actionErr := controller.Resume(t.Context(), failed.ID, failed.Version)
				return actionErr
			},
			"ready recovery": func() error {
				_, actionErr := controller.startReadyEvaluationActive(t.Context(), ready.ID, "token")
				return actionErr
			},
			"start over": func() error {
				_, actionErr := controller.Restart(t.Context(), failed.ID, failed.Version)
				return actionErr
			},
		} {
			if actionErr := invoke(); actionErr == nil {
				t.Fatalf("%s accepted invalid current config", name)
			}
		}
	})

	t.Run("alias errors", func(t *testing.T) {
		handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
		controller := newRepositoryModelEvaluationController(handler)
		controller.runWorkflow = blockingRepositoryModelEvaluationWorkflow
		handler.repositoryModelEvaluationController = controller
		t.Cleanup(handler.Shutdown)
		if err := controller.Start(); err != nil {
			t.Fatal(err)
		}
		store := controller.store.(repoeval.Store)
		request := repositoryModelEvaluationCreateRequest("owner/unknown-alias")
		request.CandidateModels = []string{"unknown-model", "model-b"}
		draft, err := store.Create(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		if _, branchErr := controller.Preflight(t.Context(), draft.ID, draft.Version); !errors.Is(
			branchErr,
			errRepositoryModelEvaluationUnavailableModel,
		) {
			t.Fatalf("preflight alias error=%v", branchErr)
		}
		if _, branchErr := controller.Run(t.Context(), request); !errors.Is(
			branchErr,
			errRepositoryModelEvaluationUnavailableModel,
		) {
			t.Fatalf("run alias error=%v", branchErr)
		}
		preflighting, err := store.Update(t.Context(), draft.ID, draft.Version, func(value *repoeval.Evaluation) error {
			value.Status = repoeval.StatusPreflighting
			value.Progress.Stage = repoeval.ProgressResolving
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		failed, err := store.Update(
			t.Context(),
			preflighting.ID,
			preflighting.Version,
			func(value *repoeval.Evaluation) error {
				value.Status = repoeval.StatusFailed
				value.Progress.Stage = repoeval.ProgressFailed
				value.Failure = "preflight failed"
				return nil
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, branchErr := controller.Resume(t.Context(), failed.ID, failed.Version); !errors.Is(
			branchErr,
			errRepositoryModelEvaluationUnavailableModel,
		) {
			t.Fatalf("preflight resume alias error=%v", branchErr)
		}

		unknownReady := seedReadyRepositoryModelEvaluationFromRequest(t, controller, store, request)
		if _, branchErr := controller.StartEvaluation(t.Context(), unknownReady.ID, unknownReady.Version); !errors.Is(
			branchErr,
			errRepositoryModelEvaluationUnavailableModel,
		) {
			t.Fatalf("start alias error=%v", branchErr)
		}
		unknownFailed := transitionReadyRepositoryModelEvaluationToFailed(t, store, unknownReady)
		if _, branchErr := controller.Resume(t.Context(), unknownFailed.ID, unknownFailed.Version); !errors.Is(
			branchErr,
			errRepositoryModelEvaluationUnavailableModel,
		) {
			t.Fatalf("execution resume alias error=%v", branchErr)
		}
		if _, branchErr := controller.Restart(t.Context(), unknownFailed.ID, unknownFailed.Version); !errors.Is(
			branchErr,
			errRepositoryModelEvaluationUnavailableModel,
		) {
			t.Fatalf("start-over alias error=%v", branchErr)
		}
	})
}

func TestRepositoryModelEvaluationExecutionPersistenceFailureCoverage(t *testing.T) {
	for _, failureCall := range []int{1, 2, 3, 4, 5, 6} {
		t.Run(fmt.Sprintf("update-%d", failureCall), func(t *testing.T) {
			handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
			controller := newRepositoryModelEvaluationController(handler)
			base, _, err := handler.repositoryModelEvaluationStore()
			if err != nil {
				t.Fatal(err)
			}
			running := seedRunningRepositoryModelEvaluation(t, controller, base, "owner/persistence-fault")
			fault := errors.New("injected execution persistence failure")
			controller.store = &repositoryModelEvaluationFaultStore{
				base: base, failAt: map[int]error{failureCall: fault},
			}
			controller.runWorkflow = func(
				_ context.Context,
				_ string,
				ref string,
				_ string,
				_ map[string]any,
				_ workflows.AgentUsageEventObserver,
			) (*workflows.RunResult, error) {
				if ref == workflows.RepositoryModelEvaluationBatchWorkflowRef {
					return repositoryModelEvaluationBatchResult(), nil
				}
				return repositoryModelEvaluationAnalysisResult(), nil
			}
			token, runCtx, _, actionErr := controller.reserveActive(running.ID)
			if actionErr != nil {
				t.Fatal(actionErr)
			}
			controller.wg.Add(1)
			controller.executeEvaluation(runCtx, running.ID, token)
		})
	}

	t.Run("canceled progress update", func(t *testing.T) {
		handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
		controller := newRepositoryModelEvaluationController(handler)
		base, _, err := handler.repositoryModelEvaluationStore()
		if err != nil {
			t.Fatal(err)
		}
		running := seedRunningRepositoryModelEvaluation(t, controller, base, "owner/canceled-update")
		controller.store = &repositoryModelEvaluationFaultStore{
			base: base, failAt: map[int]error{1: context.Canceled},
		}
		token, runCtx, _, actionErr := controller.reserveActive(running.ID)
		if actionErr != nil {
			t.Fatal(actionErr)
		}
		controller.wg.Add(1)
		controller.executeEvaluation(runCtx, running.ID, token)
	})
}

func TestRepositoryModelEvaluationExecutionControlFlowCoverage(t *testing.T) {
	t.Run("missing and get error", func(t *testing.T) {
		for name, store := range map[string]repositoryModelEvaluationStateStore{
			"missing": &repositoryModelEvaluationFaultStore{getMissing: true},
			"error":   &repositoryModelEvaluationFaultStore{getErr: errors.New("get failed")},
		} {
			t.Run(name, func(t *testing.T) {
				controller := newRepositoryModelEvaluationController(NewHandler("unused"))
				controller.store = store
				token, runCtx, _, actionErr := controller.reserveActive("missing")
				if actionErr != nil {
					t.Fatal(actionErr)
				}
				controller.wg.Add(1)
				controller.executeEvaluation(runCtx, "missing", token)
			})
		}
	})

	t.Run("already canceled context", func(t *testing.T) {
		handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
		controller := newRepositoryModelEvaluationController(handler)
		base, _, _ := handler.repositoryModelEvaluationStore()
		running := seedRunningRepositoryModelEvaluation(t, controller, base, "owner/pre-canceled")
		controller.store = base
		token, runCtx, cancel, actionErr := controller.reserveActive(running.ID)
		if actionErr != nil {
			t.Fatal(actionErr)
		}
		cancel()
		controller.wg.Add(1)
		controller.executeEvaluation(runCtx, running.ID, token)
	})

	t.Run("progress token conflict", func(t *testing.T) {
		handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
		controller := newRepositoryModelEvaluationController(handler)
		base, _, _ := handler.repositoryModelEvaluationStore()
		running := seedRunningRepositoryModelEvaluation(t, controller, base, "owner/token-conflict")
		token, runCtx, _, actionErr := controller.reserveActive(running.ID)
		if actionErr != nil {
			t.Fatal(actionErr)
		}
		controller.store = &repositoryModelEvaluationFaultStore{
			base: base,
			beforeUpdate: func(call int) {
				if call == 1 {
					controller.releaseActive(running.ID, token)
				}
			},
		}
		controller.wg.Add(1)
		controller.executeEvaluation(runCtx, running.ID, token)
	})

	t.Run("duplicate checkpoint", func(t *testing.T) {
		handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
		controller := newRepositoryModelEvaluationController(handler)
		base, _, _ := handler.repositoryModelEvaluationStore()
		running := seedRunningRepositoryModelEvaluation(t, controller, base, "owner/duplicate-checkpoint")
		batch := repositoryModelEvaluationPendingBatches(running)[0]
		fault := &repositoryModelEvaluationFaultStore{base: base}
		fault.beforeUpdate = func(call int) {
			if call != 2 {
				return
			}
			current, found, getErr := base.Get(t.Context(), running.ID)
			if getErr != nil || !found {
				t.Fatalf("checkpoint hook get found=%v err=%v", found, getErr)
			}
			_, updateErr := base.Update(
				t.Context(),
				current.ID,
				current.Version,
				func(value *repoeval.Evaluation) error {
					value.Checkpoint.Batches = append(value.Checkpoint.Batches, repoeval.BatchCheckpoint{
						ID: batch.id, CandidateIDs: batch.ids, JudgeJSON: `{"evaluations":[]}`,
						MappingJSON: `[]`, CompletedAt: time.Now().UTC(),
					})
					return nil
				},
			)
			if updateErr != nil {
				t.Fatal(updateErr)
			}
		}
		controller.store = fault
		controller.runWorkflow = successfulRepositoryModelEvaluationWorkflow
		token, runCtx, _, actionErr := controller.reserveActive(running.ID)
		if actionErr != nil {
			t.Fatal(actionErr)
		}
		controller.wg.Add(1)
		controller.executeEvaluation(runCtx, running.ID, token)
	})

	t.Run("unexpected durable status", func(t *testing.T) {
		handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
		controller := newRepositoryModelEvaluationController(handler)
		base, _, _ := handler.repositoryModelEvaluationStore()
		ready := seedReadyRepositoryModelEvaluation(t, controller, base, "owner/unexpected-status")
		batch := repositoryModelEvaluationBatches(ready)[0]
		custom := repoeval.Clone(ready)
		custom.Checkpoint = repoeval.Checkpoint{Batches: []repoeval.BatchCheckpoint{{
			ID: batch.id, CandidateIDs: batch.ids, JudgeJSON: `{"evaluations":[]}`,
			MappingJSON: `[]`, CompletedAt: time.Now().UTC(),
		}}}
		controller.store = &repositoryModelEvaluationFaultStore{base: base, getValue: &custom}
		token, runCtx, _, actionErr := controller.reserveActive(ready.ID)
		if actionErr != nil {
			t.Fatal(actionErr)
		}
		controller.wg.Add(1)
		controller.executeEvaluation(runCtx, ready.ID, token)
	})

	for _, conflictCall := range []int{2, 6} {
		t.Run(fmt.Sprintf("token-conflict-%d", conflictCall), func(t *testing.T) {
			handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
			controller := newRepositoryModelEvaluationController(handler)
			base, _, _ := handler.repositoryModelEvaluationStore()
			running := seedRunningRepositoryModelEvaluation(t, controller, base, "owner/late-token-conflict")
			token, runCtx, _, actionErr := controller.reserveActive(running.ID)
			if actionErr != nil {
				t.Fatal(actionErr)
			}
			controller.store = &repositoryModelEvaluationFaultStore{
				base: base,
				beforeUpdate: func(call int) {
					if call == conflictCall {
						controller.releaseActive(running.ID, token)
					}
				},
			}
			controller.runWorkflow = successfulRepositoryModelEvaluationWorkflow
			controller.wg.Add(1)
			controller.executeEvaluation(runCtx, running.ID, token)
		})
	}

	for _, stage := range []string{"batch-root", "batch-child", "analysis-root", "analysis-child"} {
		t.Run(stage, func(t *testing.T) {
			handler, _, _ := newRepositoryModelEvaluationTestHandler(t)
			controller := newRepositoryModelEvaluationController(handler)
			base, _, _ := handler.repositoryModelEvaluationStore()
			running := seedRunningRepositoryModelEvaluation(t, controller, base, "owner/"+stage)
			controller.store = base
			var childCancel context.CancelFunc
			controller.runWorkflow = func(
				_ context.Context,
				_ string,
				ref string,
				_ string,
				_ map[string]any,
				_ workflows.AgentUsageEventObserver,
			) (*workflows.RunResult, error) {
				atBatch := ref == workflows.RepositoryModelEvaluationBatchWorkflowRef
				if (stage == "batch-root" || stage == "batch-child") && atBatch ||
					(stage == "analysis-root" || stage == "analysis-child") && !atBatch {
					if strings.HasSuffix(stage, "root") {
						controller.cancel()
					} else {
						childCancel()
					}
				}
				if atBatch {
					return repositoryModelEvaluationBatchResult(), nil
				}
				return repositoryModelEvaluationAnalysisResult(), nil
			}
			token, runCtx, cancel, actionErr := controller.reserveActive(running.ID)
			if actionErr != nil {
				t.Fatal(actionErr)
			}
			childCancel = cancel
			controller.wg.Add(1)
			controller.executeEvaluation(runCtx, running.ID, token)
		})
	}
}

func blockingRepositoryModelEvaluationWorkflow(
	ctx context.Context,
	_ string,
	_ string,
	_ string,
	_ map[string]any,
	_ workflows.AgentUsageEventObserver,
) (*workflows.RunResult, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func successfulRepositoryModelEvaluationWorkflow(
	_ context.Context,
	_ string,
	ref string,
	_ string,
	_ map[string]any,
	_ workflows.AgentUsageEventObserver,
) (*workflows.RunResult, error) {
	if ref == workflows.RepositoryModelEvaluationBatchWorkflowRef {
		return repositoryModelEvaluationBatchResult(), nil
	}
	return repositoryModelEvaluationAnalysisResult(), nil
}

func seedReadyRepositoryModelEvaluation(
	t *testing.T,
	controller *repositoryModelEvaluationController,
	store repoeval.Store,
	repository string,
) repoeval.Evaluation {
	t.Helper()
	return seedReadyRepositoryModelEvaluationFromRequest(
		t,
		controller,
		store,
		repositoryModelEvaluationCreateRequest(repository),
	)
}

func seedReadyRepositoryModelEvaluationFromRequest(
	t *testing.T,
	controller *repositoryModelEvaluationController,
	store repoeval.Store,
	request repoeval.CreateRequest,
) repoeval.Evaluation {
	t.Helper()
	draft, err := store.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := store.Update(t.Context(), draft.ID, draft.Version, func(value *repoeval.Evaluation) error {
		value.Status = repoeval.StatusPreflighting
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, progress, warnings, actionErr := controller.preflightManifest(
		preflight,
		"wr_seed",
		repositoryModelEvaluationPreflightResult().Outputs,
	)
	if actionErr != nil {
		t.Fatal(actionErr)
	}
	ready, err := store.Update(t.Context(), draft.ID, preflight.Version, func(value *repoeval.Evaluation) error {
		value.Status = repoeval.StatusReady
		value.Corpus = manifest
		value.Progress = progress
		value.Warnings = warnings
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return ready
}

func seedFailedReadyRepositoryModelEvaluation(
	t *testing.T,
	controller *repositoryModelEvaluationController,
	store repoeval.Store,
	repository string,
) repoeval.Evaluation {
	t.Helper()
	return transitionReadyRepositoryModelEvaluationToFailed(
		t,
		store,
		seedReadyRepositoryModelEvaluation(t, controller, store, repository),
	)
}

func transitionReadyRepositoryModelEvaluationToFailed(
	t *testing.T,
	store repoeval.Store,
	ready repoeval.Evaluation,
) repoeval.Evaluation {
	t.Helper()
	running, err := store.Update(t.Context(), ready.ID, ready.Version, func(value *repoeval.Evaluation) error {
		value.Status = repoeval.StatusRunning
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := store.Update(t.Context(), running.ID, running.Version, func(value *repoeval.Evaluation) error {
		value.Status = repoeval.StatusFailed
		value.Failure = "injected failure"
		value.Progress.Stage = repoeval.ProgressFailed
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return failed
}

func seedRunningRepositoryModelEvaluation(
	t *testing.T,
	controller *repositoryModelEvaluationController,
	store repoeval.Store,
	repository string,
) repoeval.Evaluation {
	t.Helper()
	ready := seedReadyRepositoryModelEvaluation(t, controller, store, repository)
	now := time.Now().UTC()
	running, err := store.Update(t.Context(), ready.ID, ready.Version, func(value *repoeval.Evaluation) error {
		value.Status = repoeval.StatusRunning
		value.Checkpoint = repoeval.Checkpoint{ConcreteModels: make(map[string]map[string]int)}
		value.Progress.Stage = repoeval.ProgressCandidateExecution
		value.Progress.TotalTasks = 6
		value.ModelStats = map[string]repoeval.ModelStats{
			"model-a": {FilesSelected: len(value.Corpus.Files), StartedAt: &now},
			"model-b": {FilesSelected: len(value.Corpus.Files), StartedAt: &now},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return running
}

func writeRepositoryModelEvaluationTestConfig(t *testing.T, path, workspace string) {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = workspace
	cfg.Agents.Defaults.AccountRef = "api"
	cfg.Agents.Defaults.ModelName = "model-a"
	cfg.ModelAliases = []config.ModelAliasConfig{
		{Name: "model-a", Model: "openai/gpt-a"},
		{Name: "model-b", Model: "openai/gpt-b"},
		{Name: "selector", Model: "openai/gpt-selector"},
		{Name: "judge", Model: "openai/gpt-judge"},
	}
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "api", Provider: "openai", Model: "openai/gpt-a", Enabled: true,
	}}
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
}
