package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/repoeval"
	"github.com/sipeed/picoclaw/pkg/reposcope"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	repositoryModelEvaluationBatchSize        = 12
	repositoryModelEvaluationPhaseMinTimeout  = 15 * time.Minute
	repositoryModelEvaluationBatchSetupBudget = 5 * time.Minute
	repositoryModelEvaluationTaskBudget       = 2 * time.Minute
	repositoryModelEvaluationMaxTimeout       = 4 * time.Hour
)

var errRepositoryModelEvaluationRunFenced = errors.New("repository model evaluation run is fenced")

type repositoryModelEvaluationWorkflowRun func(
	ctx context.Context,
	workflowYAML string,
	workflowRef string,
	runID string,
	inputs map[string]any,
	observe workflows.AgentUsageEventObserver,
) (*workflows.RunResult, error)

type repositoryModelEvaluationStepObserverContextKey struct{}

type repositoryModelEvaluationActiveRun struct {
	token  string
	cancel context.CancelFunc
}

type repositoryModelEvaluationStateStore interface {
	Create(ctx context.Context, request repoeval.CreateRequest) (repoeval.Evaluation, error)
	Get(ctx context.Context, id string) (repoeval.Evaluation, bool, error)
	Update(
		ctx context.Context,
		id string,
		expectedVersion int64,
		mutate func(*repoeval.Evaluation) error,
	) (repoeval.Evaluation, error)
}

type repositoryModelEvaluationController struct {
	handler          *Handler
	store            repositoryModelEvaluationStateStore
	workspace        string
	gitWorkspaceRoot string

	ctx    context.Context
	cancel context.CancelFunc

	lifecycleMu   sync.Mutex
	started       bool
	stopped       bool
	startErr      error
	releaseLease  func()
	activeRuns    int
	activeDrained chan struct{}

	mu     sync.Mutex
	active map[string]repositoryModelEvaluationActiveRun
	wg     sync.WaitGroup

	runWorkflow repositoryModelEvaluationWorkflowRun
	now         func() time.Time
	stopTimeout time.Duration
}

func newRepositoryModelEvaluationController(handler *Handler) *repositoryModelEvaluationController {
	ctx, cancel := context.WithCancel(context.Background())
	controller := &repositoryModelEvaluationController{
		handler: handler, ctx: ctx, cancel: cancel,
		active: make(map[string]repositoryModelEvaluationActiveRun),
		now:    time.Now, stopTimeout: 10 * time.Second,
	}
	controller.runWorkflow = controller.runWorkflowRuntime
	return controller
}

// StartRepositoryModelEvaluationController acquires the durable controller
// lease and reconciles orphaned in-flight evaluations.
func (h *Handler) StartRepositoryModelEvaluationController() {
	if _, err := h.ensureRepositoryModelEvaluationController(); err != nil {
		logger.ErrorC(
			"repository-model-evaluation",
			"Repository model evaluation controller did not start: "+err.Error(),
		)
	}
}

func (h *Handler) ensureRepositoryModelEvaluationController() (*repositoryModelEvaluationController, error) {
	if h == nil {
		return nil, errors.New("repository model evaluation controller is unavailable")
	}
	h.repositoryModelEvaluationControllerMu.Lock()
	if h.repositoryModelEvaluationController == nil {
		h.repositoryModelEvaluationController = newRepositoryModelEvaluationController(h)
	}
	controller := h.repositoryModelEvaluationController
	h.repositoryModelEvaluationControllerMu.Unlock()
	if err := controller.Start(); err != nil {
		return nil, err
	}
	return controller, nil
}

func (h *Handler) stopRepositoryModelEvaluationController() {
	if h == nil {
		return
	}
	h.repositoryModelEvaluationControllerMu.Lock()
	controller := h.repositoryModelEvaluationController
	h.repositoryModelEvaluationControllerMu.Unlock()
	if controller != nil {
		controller.Stop()
	}
}

func (c *repositoryModelEvaluationController) Start() error {
	if c == nil || c.handler == nil {
		return errors.New("repository model evaluation controller is unavailable")
	}
	c.lifecycleMu.Lock()
	if c.started {
		err := c.startErr
		c.lifecycleMu.Unlock()
		return err
	}
	if c.stopped {
		c.lifecycleMu.Unlock()
		return context.Canceled
	}
	c.started = true
	store, cfg, err := c.handler.repositoryModelEvaluationStore()
	if err == nil {
		c.releaseLease, err = store.LockController()
	}
	if err != nil {
		c.startErr = err
		c.lifecycleMu.Unlock()
		return err
	}
	c.store = store
	c.workspace = cfg.WorkspacePath()
	c.gitWorkspaceRoot = cfg.GitWorkspaceRootPath()
	evaluations, err := store.List(c.ctx)
	if err != nil {
		c.releaseLease()
		c.releaseLease = nil
		c.startErr = err
		c.lifecycleMu.Unlock()
		return err
	}
	c.lifecycleMu.Unlock()
	for _, evaluation := range evaluations {
		switch evaluation.Status {
		case repoeval.StatusCanceling:
			_, _ = c.updateLatest(c.ctx, evaluation.ID, func(candidate *repoeval.Evaluation) error {
				candidate.Status = repoeval.StatusCanceled
				candidate.Progress.Stage = repoeval.ProgressCanceled
				candidate.Progress.Message = "Canceled after launcher recovery."
				return nil
			})
		case repoeval.StatusPreflighting:
			c.recoverPreflight(evaluation.ID)
		case repoeval.StatusReady:
			c.recoverReadyEvaluation(evaluation.ID)
		case repoeval.StatusRunning, repoeval.StatusJudging, repoeval.StatusAnalyzing:
			c.recoverEvaluation(evaluation.ID)
		}
	}
	return nil
}

func (c *repositoryModelEvaluationController) Stop() {
	if c == nil {
		return
	}
	c.lifecycleMu.Lock()
	if c.stopped {
		c.lifecycleMu.Unlock()
		return
	}
	c.stopped = true
	c.cancel()
	activeDrained := c.activeDrained
	c.lifecycleMu.Unlock()
	done := make(chan struct{})
	go func() {
		if activeDrained != nil {
			<-activeDrained
		}
		c.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		c.releaseControllerLease()
	case <-time.After(c.stopTimeout):
		logger.WarnC("repository-model-evaluation", "Timed out waiting for repository model evaluation shutdown")
		go func() {
			<-done
			c.releaseControllerLease()
		}()
	}
}

func (c *repositoryModelEvaluationController) releaseControllerLease() {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.releaseLease != nil {
		c.releaseLease()
		c.releaseLease = nil
	}
}

func (c *repositoryModelEvaluationController) config() (*config.Config, error) {
	if c == nil || c.handler == nil {
		return nil, errors.New("repository model evaluation controller is unavailable")
	}
	cfg, err := config.LoadConfig(c.handler.configPath)
	if err != nil {
		return nil, err
	}
	if c.workspace != "" && cfg.WorkspacePath() != c.workspace {
		return nil, errors.New("repository evaluation workspace changed; restart the launcher")
	}
	return cfg, nil
}

func (c *repositoryModelEvaluationController) Preflight(
	ctx context.Context,
	id string,
	expectedVersion int64,
) (repoeval.Evaluation, error) {
	if err := c.Start(); err != nil {
		return repoeval.Evaluation{}, err
	}
	evaluation, found, err := c.store.Get(ctx, id)
	if err != nil || !found {
		if err == nil {
			err = os.ErrNotExist
		}
		return repoeval.Evaluation{}, err
	}
	if evaluation.Version != expectedVersion {
		return repoeval.Evaluation{}, repoeval.ErrConflict
	}
	if evaluation.Status != repoeval.StatusDraft {
		return repoeval.Evaluation{}, repoeval.ErrInvalidTransition
	}
	return c.startPreflight(ctx, evaluation, false)
}

func (c *repositoryModelEvaluationController) RunExisting(
	ctx context.Context,
	id string,
	expectedVersion int64,
) (repoeval.Evaluation, error) {
	if err := c.Start(); err != nil {
		return repoeval.Evaluation{}, err
	}
	evaluation, found, err := c.store.Get(ctx, id)
	if err != nil || !found {
		if err == nil {
			err = os.ErrNotExist
		}
		return repoeval.Evaluation{}, err
	}
	if evaluation.Version != expectedVersion {
		return repoeval.Evaluation{}, repoeval.ErrConflict
	}
	if evaluation.Status != repoeval.StatusDraft {
		return repoeval.Evaluation{}, repoeval.ErrInvalidTransition
	}
	return c.startPreflight(ctx, evaluation, true)
}

func (c *repositoryModelEvaluationController) Run(
	ctx context.Context,
	request repoeval.CreateRequest,
) (repoeval.Evaluation, error) {
	if err := c.Start(); err != nil {
		return repoeval.Evaluation{}, err
	}
	cfg, err := c.config()
	if err != nil {
		return repoeval.Evaluation{}, err
	}
	if aliasErr := validateRepositoryModelEvaluationAliases(
		cfg,
		request.CandidateModels,
		request.SelectorModelAlias,
		request.JudgeModelAlias,
	); aliasErr != nil {
		return repoeval.Evaluation{}, aliasErr
	}
	runID := workflows.NewRunID()
	request.OneShot = true
	request.InitialRunID = runID
	created, err := c.store.Create(ctx, request)
	if err != nil {
		return repoeval.Evaluation{}, err
	}
	return c.launchCreatedPreflight(created, runID)
}

func (c *repositoryModelEvaluationController) launchCreatedPreflight(
	created repoeval.Evaluation,
	runID string,
) (repoeval.Evaluation, error) {
	token, runCtx, _, err := c.reserveActive(created.ID)
	if err != nil {
		return repoeval.Evaluation{}, err
	}
	c.wg.Add(1)
	go c.executePreflight(runCtx, created.ID, token, runID)
	return created, nil
}

func (c *repositoryModelEvaluationController) startPreflight(
	ctx context.Context,
	evaluation repoeval.Evaluation,
	oneShot bool,
) (repoeval.Evaluation, error) {
	cfg, err := c.config()
	if err != nil {
		return repoeval.Evaluation{}, err
	}
	if aliasErr := validateRepositoryModelEvaluationAliases(
		cfg,
		evaluation.CandidateModels,
		evaluation.SelectorModelAlias,
		evaluation.JudgeModelAlias,
	); aliasErr != nil {
		return repoeval.Evaluation{}, aliasErr
	}
	token, runCtx, cancel, err := c.reserveActive(evaluation.ID)
	if err != nil {
		return repoeval.Evaluation{}, err
	}
	runID := workflows.NewRunID()
	updated, err := c.store.Update(ctx, evaluation.ID, evaluation.Version, func(candidate *repoeval.Evaluation) error {
		candidate.Status = repoeval.StatusPreflighting
		candidate.OneShot = candidate.OneShot || oneShot
		candidate.RunIDs = append(candidate.RunIDs, runID)
		candidate.Progress.Stage = repoeval.ProgressResolving
		candidate.Progress.Message = "Resolving the exact repository commit."
		candidate.Progress.Percent = 1
		return nil
	})
	if err != nil {
		cancel()
		c.releaseActive(evaluation.ID, token)
		return repoeval.Evaluation{}, err
	}
	c.wg.Add(1)
	go c.executePreflight(runCtx, evaluation.ID, token, runID)
	return updated, nil
}

func (c *repositoryModelEvaluationController) StartEvaluation(
	ctx context.Context,
	id string,
	expectedVersion int64,
) (repoeval.Evaluation, error) {
	if err := c.Start(); err != nil {
		return repoeval.Evaluation{}, err
	}
	evaluation, found, err := c.store.Get(ctx, id)
	if err != nil || !found {
		if err == nil {
			err = os.ErrNotExist
		}
		return repoeval.Evaluation{}, err
	}
	if evaluation.Version != expectedVersion {
		return repoeval.Evaluation{}, repoeval.ErrConflict
	}
	if evaluation.Status != repoeval.StatusReady || evaluation.Corpus == nil {
		return repoeval.Evaluation{}, repoeval.ErrInvalidTransition
	}
	cfg, err := c.config()
	if err != nil {
		return repoeval.Evaluation{}, err
	}
	if aliasErr := validateRepositoryModelEvaluationAliases(
		cfg,
		evaluation.CandidateModels,
		evaluation.SelectorModelAlias,
		evaluation.JudgeModelAlias,
	); aliasErr != nil {
		return repoeval.Evaluation{}, aliasErr
	}
	token, runCtx, cancel, err := c.reserveActive(id)
	if err != nil {
		return repoeval.Evaluation{}, err
	}
	updated, err := c.store.Update(ctx, id, expectedVersion, func(candidate *repoeval.Evaluation) error {
		return c.initializeReadyEvaluation(candidate)
	})
	if err != nil {
		cancel()
		c.releaseActive(id, token)
		return repoeval.Evaluation{}, err
	}
	c.wg.Add(1)
	go c.executeEvaluation(runCtx, id, token)
	return updated, nil
}

func (c *repositoryModelEvaluationController) initializeReadyEvaluation(
	candidate *repoeval.Evaluation,
) error {
	if candidate == nil || candidate.Status != repoeval.StatusReady || candidate.Corpus == nil {
		return repoeval.ErrInvalidTransition
	}
	totalTasks := 1 // final analyzer
	for _, batch := range repositoryModelEvaluationBatches(*candidate) {
		totalTasks += repositoryModelEvaluationBatchTaskCount(len(batch.files), len(candidate.CandidateModels))
	}
	now := c.clock()
	candidate.OneShot = true
	candidate.Status = repoeval.StatusRunning
	candidate.Checkpoint = repoeval.Checkpoint{ConcreteModels: make(map[string]map[string]int)}
	candidate.Progress.Stage = repoeval.ProgressCandidateExecution
	candidate.Progress.TotalTasks = totalTasks
	candidate.Progress.CompletedTasks = 0
	candidate.Progress.CompletedFiles = 0
	candidate.Progress.Message = "Running candidate models over the frozen corpus."
	candidate.Progress.Percent = max(candidate.Progress.Percent, 20)
	candidate.ModelStats = make(map[string]repoeval.ModelStats, len(candidate.CandidateModels))
	for _, alias := range candidate.CandidateModels {
		started := now
		candidate.ModelStats[alias] = repoeval.ModelStats{
			FilesSelected: len(candidate.Corpus.Files),
			StartedAt:     &started,
		}
	}
	return nil
}

func (c *repositoryModelEvaluationController) startReadyEvaluationActive(
	ctx context.Context,
	id string,
	token string,
) (repoeval.Evaluation, error) {
	cfg, err := c.config()
	if err != nil {
		return repoeval.Evaluation{}, err
	}
	current, found, err := c.store.Get(ctx, id)
	if err != nil || !found {
		if err == nil {
			err = os.ErrNotExist
		}
		return repoeval.Evaluation{}, err
	}
	if aliasErr := validateRepositoryModelEvaluationAliases(
		cfg,
		current.CandidateModels,
		current.SelectorModelAlias,
		current.JudgeModelAlias,
	); aliasErr != nil {
		return repoeval.Evaluation{}, aliasErr
	}
	return c.updateActiveLatest(ctx, id, token, []repoeval.Status{repoeval.StatusReady}, func(
		candidate *repoeval.Evaluation,
	) error {
		return c.initializeReadyEvaluation(candidate)
	})
}

func (c *repositoryModelEvaluationController) Cancel(
	ctx context.Context,
	id string,
	expectedVersion int64,
) (repoeval.Evaluation, error) {
	if err := c.Start(); err != nil {
		return repoeval.Evaluation{}, err
	}
	evaluation, found, err := c.store.Get(ctx, id)
	if err != nil || !found {
		if err == nil {
			err = os.ErrNotExist
		}
		return repoeval.Evaluation{}, err
	}
	if evaluation.Version != expectedVersion {
		return repoeval.Evaluation{}, repoeval.ErrConflict
	}
	updated, err := c.store.Update(ctx, id, expectedVersion, func(candidate *repoeval.Evaluation) error {
		switch candidate.Status {
		case repoeval.StatusDraft:
			candidate.Status = repoeval.StatusCanceled
			candidate.Progress.Stage = repoeval.ProgressCanceled
			candidate.Progress.Message = "Canceled."
		case repoeval.StatusPreflighting,
			repoeval.StatusReady,
			repoeval.StatusRunning,
			repoeval.StatusJudging,
			repoeval.StatusAnalyzing:
			candidate.Status = repoeval.StatusCanceling
			candidate.Progress.Stage = repoeval.ProgressCanceling
			candidate.Progress.Message = "Canceling at the current durable boundary."
		case repoeval.StatusCanceling:
			return nil
		default:
			return repoeval.ErrInvalidTransition
		}
		return nil
	})
	if err != nil {
		return repoeval.Evaluation{}, err
	}
	c.mu.Lock()
	active, ok := c.active[id]
	c.mu.Unlock()
	if ok {
		active.cancel()
	}
	if updated.Status == repoeval.StatusCanceling {
		updated, err = c.finishCanceled(context.Background(), id)
	}
	return updated, err
}

func (c *repositoryModelEvaluationController) Resume(
	ctx context.Context,
	id string,
	expectedVersion int64,
) (repoeval.Evaluation, error) {
	if err := c.Start(); err != nil {
		return repoeval.Evaluation{}, err
	}
	evaluation, found, err := c.store.Get(ctx, id)
	if err != nil || !found {
		if err == nil {
			err = os.ErrNotExist
		}
		return repoeval.Evaluation{}, err
	}
	if evaluation.Version != expectedVersion {
		return repoeval.Evaluation{}, repoeval.ErrConflict
	}
	if evaluation.Status != repoeval.StatusFailed {
		return repoeval.Evaluation{}, repoeval.ErrInvalidTransition
	}
	if evaluation.Corpus == nil {
		return c.resumeTerminalPreflight(ctx, evaluation)
	}
	return c.resumeTerminalEvaluation(ctx, evaluation)
}

func (c *repositoryModelEvaluationController) resumeTerminalPreflight(
	ctx context.Context,
	evaluation repoeval.Evaluation,
) (repoeval.Evaluation, error) {
	cfg, err := c.config()
	if err != nil {
		return repoeval.Evaluation{}, err
	}
	if aliasErr := validateRepositoryModelEvaluationAliases(
		cfg,
		evaluation.CandidateModels,
		evaluation.SelectorModelAlias,
		evaluation.JudgeModelAlias,
	); aliasErr != nil {
		return repoeval.Evaluation{}, aliasErr
	}
	token, runCtx, cancel, err := c.reserveActive(evaluation.ID)
	if err != nil {
		return repoeval.Evaluation{}, err
	}
	runID := workflows.NewRunID()
	updated, err := c.store.Update(ctx, evaluation.ID, evaluation.Version, func(candidate *repoeval.Evaluation) error {
		candidate.Status = repoeval.StatusPreflighting
		candidate.OneShot = true
		candidate.Failure = ""
		candidate.Checkpoint = repoeval.Checkpoint{}
		candidate.ModelStats = make(map[string]repoeval.ModelStats)
		candidate.Comparisons = []repoeval.ModelComparison{}
		candidate.RunIDs = append(candidate.RunIDs, runID)
		candidate.Progress = repoeval.Progress{
			Stage: repoeval.ProgressResolving, Languages: make(map[string]repoeval.LanguageProgress),
			Message: "Resuming preflight at the same repository ref.", Percent: 1,
		}
		return nil
	})
	if err != nil {
		cancel()
		c.releaseActive(evaluation.ID, token)
		return repoeval.Evaluation{}, err
	}
	c.wg.Add(1)
	go c.executePreflight(runCtx, evaluation.ID, token, runID)
	return updated, nil
}

func (c *repositoryModelEvaluationController) resumeTerminalEvaluation(
	ctx context.Context,
	evaluation repoeval.Evaluation,
) (repoeval.Evaluation, error) {
	cfg, err := c.config()
	if err != nil {
		return repoeval.Evaluation{}, err
	}
	if aliasErr := validateRepositoryModelEvaluationAliases(
		cfg,
		evaluation.CandidateModels,
		evaluation.SelectorModelAlias,
		evaluation.JudgeModelAlias,
	); aliasErr != nil {
		return repoeval.Evaluation{}, aliasErr
	}
	token, runCtx, cancel, err := c.reserveActive(evaluation.ID)
	if err != nil {
		return repoeval.Evaluation{}, err
	}
	metrics := repoeval.Clone(evaluation)
	repositoryModelEvaluationApplyCheckpointMetrics(&metrics)
	completedTasks := metrics.Progress.CompletedTasks
	totalTasks := completedTasks + 1 // final analyzer
	for _, batch := range repositoryModelEvaluationPendingBatches(evaluation) {
		totalTasks += repositoryModelEvaluationBatchTaskCount(len(batch.files), len(batch.models))
	}
	totalTasks = max(totalTasks, evaluation.Progress.TotalTasks)
	now := c.clock()
	updated, err := c.store.Update(ctx, evaluation.ID, evaluation.Version, func(candidate *repoeval.Evaluation) error {
		candidate.Status = repoeval.StatusRunning
		candidate.OneShot = true
		candidate.Failure = ""
		candidate.Comparisons = []repoeval.ModelComparison{}
		candidate.Progress.Stage = repoeval.ProgressCandidateExecution
		candidate.Progress.TotalTasks = totalTasks
		candidate.Progress.CompletedTasks = completedTasks
		candidate.Progress.CurrentModel = ""
		candidate.Progress.CurrentPath = ""
		candidate.Progress.Message = "Resuming from durable corpus batch checkpoints."
		repositoryModelEvaluationApplyCheckpointMetrics(candidate)
		candidate.Progress.Percent = 80 * float64(candidate.Progress.CompletedFiles) /
			float64(max(1, candidate.Progress.SelectedFiles))
		if candidate.Checkpoint.ConcreteModels == nil {
			candidate.Checkpoint.ConcreteModels = make(map[string]map[string]int)
		}
		for _, alias := range candidate.CandidateModels {
			stats := candidate.ModelStats[alias]
			stats.FilesSelected = len(candidate.Corpus.Files)
			stats.CompletedAt = nil
			if stats.StartedAt == nil {
				started := now
				stats.StartedAt = &started
			}
			candidate.ModelStats[alias] = stats
		}
		return nil
	})
	if err != nil {
		cancel()
		c.releaseActive(evaluation.ID, token)
		return repoeval.Evaluation{}, err
	}
	c.wg.Add(1)
	go c.executeEvaluation(runCtx, evaluation.ID, token)
	return updated, nil
}

func (c *repositoryModelEvaluationController) Restart(
	ctx context.Context,
	id string,
	expectedVersion int64,
) (repoeval.Evaluation, error) {
	if err := c.Start(); err != nil {
		return repoeval.Evaluation{}, err
	}
	evaluation, found, err := c.store.Get(ctx, id)
	if err != nil || !found {
		if err == nil {
			err = os.ErrNotExist
		}
		return repoeval.Evaluation{}, err
	}
	if evaluation.Version != expectedVersion {
		return repoeval.Evaluation{}, repoeval.ErrConflict
	}
	if evaluation.Status != repoeval.StatusFailed {
		return repoeval.Evaluation{}, repoeval.ErrInvalidTransition
	}
	cfg, err := c.config()
	if err != nil {
		return repoeval.Evaluation{}, err
	}
	if aliasErr := validateRepositoryModelEvaluationAliases(
		cfg,
		evaluation.CandidateModels,
		evaluation.SelectorModelAlias,
		evaluation.JudgeModelAlias,
	); aliasErr != nil {
		return repoeval.Evaluation{}, aliasErr
	}
	repository, err := normalizeRepositoryModelEvaluationRepository(ctx, evaluation.Repository)
	if err != nil {
		return repoeval.Evaluation{}, err
	}
	runID := workflows.NewRunID()
	created, err := c.store.Create(ctx, repoeval.CreateRequest{
		Repository: repository, Ref: evaluation.Ref,
		CandidateModels:    append([]string(nil), evaluation.CandidateModels...),
		SelectorModelAlias: evaluation.SelectorModelAlias, JudgeModelAlias: evaluation.JudgeModelAlias,
		Focus: evaluation.Focus, DefaultFilesPerLanguage: evaluation.DefaultFilesPerLanguage,
		FilesPerLanguage: evaluation.FilesPerLanguage,
		OneShot:          true, InitialRunID: runID,
	})
	if err != nil {
		return repoeval.Evaluation{}, err
	}
	return c.launchCreatedPreflight(created, runID)
}

func (c *repositoryModelEvaluationController) reserveActive(
	id string,
) (string, context.Context, context.CancelFunc, error) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.stopped || c.ctx.Err() != nil {
		return "", nil, nil, context.Canceled
	}
	token := workflows.NewRunID()
	runCtx, cancel := context.WithCancel(c.ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.active[id]; exists {
		cancel()
		return "", nil, nil, errRepositoryModelEvaluationBusy
	}
	c.active[id] = repositoryModelEvaluationActiveRun{token: token, cancel: cancel}
	if c.activeRuns == 0 {
		c.activeDrained = make(chan struct{})
	}
	c.activeRuns++
	return token, runCtx, cancel, nil
}

func (c *repositoryModelEvaluationController) releaseActive(id, token string) {
	c.mu.Lock()
	released := false
	if active, ok := c.active[id]; ok && active.token == token {
		delete(c.active, id)
		released = true
	}
	c.mu.Unlock()
	if !released {
		return
	}
	c.lifecycleMu.Lock()
	c.activeRuns--
	if c.activeRuns == 0 && c.activeDrained != nil {
		close(c.activeDrained)
		c.activeDrained = nil
	}
	c.lifecycleMu.Unlock()
}

func (c *repositoryModelEvaluationController) activeToken(id, token string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	active, ok := c.active[id]
	return ok && active.token == token
}

func (c *repositoryModelEvaluationController) recoverPreflight(id string) {
	token, runCtx, _, err := c.reserveActive(id)
	if err != nil {
		return
	}
	runID := workflows.NewRunID()
	_, err = c.updateLatest(c.ctx, id, func(candidate *repoeval.Evaluation) error {
		if candidate.Status != repoeval.StatusPreflighting {
			return repoeval.ErrInvalidTransition
		}
		candidate.OneShot = true
		candidate.RunIDs = append(candidate.RunIDs, runID)
		candidate.Progress.Stage = repoeval.ProgressResolving
		candidate.Progress.Message = "Resuming preflight after launcher recovery."
		return nil
	})
	if err != nil {
		c.releaseActive(id, token)
		return
	}
	c.wg.Add(1)
	go c.executePreflight(runCtx, id, token, runID)
}

func (c *repositoryModelEvaluationController) recoverEvaluation(id string) {
	token, runCtx, _, err := c.reserveActive(id)
	if err != nil {
		return
	}
	c.wg.Add(1)
	go c.executeEvaluation(runCtx, id, token)
}

func (c *repositoryModelEvaluationController) recoverReadyEvaluation(id string) {
	token, runCtx, _, err := c.reserveActive(id)
	if err != nil {
		return
	}
	c.wg.Add(1)
	go c.executeReadyEvaluation(runCtx, id, token)
}

func (c *repositoryModelEvaluationController) executeReadyEvaluation(
	ctx context.Context,
	id string,
	token string,
) {
	defer c.wg.Done()
	defer c.releaseActive(id, token)
	if _, err := c.startReadyEvaluationActive(ctx, id, token); err != nil {
		if !c.handleActiveMutationError(id, err) {
			c.fail(id, token, err.Error())
		}
		return
	}
	c.executeEvaluationPhase(ctx, id, token)
}

func (c *repositoryModelEvaluationController) executePreflight(ctx context.Context, id, token, runID string) {
	defer c.wg.Done()
	defer c.releaseActive(id, token)
	evaluation, found, err := c.store.Get(ctx, id)
	if err != nil || !found {
		return
	}
	repository, normalizeErr := normalizeRepositoryModelEvaluationRepository(ctx, evaluation.Repository)
	if normalizeErr != nil {
		c.fail(id, token, boundedRepositoryModelEvaluationDetail(normalizeErr.Error()))
		return
	}
	scopeJSON, _ := json.Marshal(repositoryModelEvaluationScopeInput(evaluation.Focus))
	policyJSON, _ := json.Marshal(repositoryModelEvaluationPolicyInput(evaluation))
	workflowCtx := repositoryModelEvaluationWithStepObserver(
		ctx,
		c.preflightStepObserver(id, token),
	)
	result, runErr := c.runWorkflow(workflowCtx, workflows.RepositoryModelEvaluationPreflightWorkflowYAML,
		workflows.RepositoryModelEvaluationPreflightWorkflowRef, runID, map[string]any{
			"repository": repository, "ref": evaluation.Ref,
			"scope": string(scopeJSON), "selection_policy": string(policyJSON),
			"selector_model": evaluation.SelectorModelAlias,
		}, c.usageObserver(id, token, workflows.RepositoryModelEvaluationPreflightWorkflowRef))
	if ctx.Err() != nil {
		if c.ctx.Err() != nil {
			return
		}
		_, _ = c.finishCanceled(context.Background(), id)
		return
	}
	if runErr != nil || result == nil || result.Status != workflows.RunStatusSucceeded {
		c.fail(id, token, repositoryModelEvaluationRunError(runErr, result))
		return
	}
	manifest, progress, warnings, parseErr := c.preflightManifest(evaluation, runID, result.Outputs)
	if parseErr != nil {
		c.fail(id, token, parseErr.Error())
		return
	}
	updated, err := c.updateActiveLatest(
		context.Background(),
		id,
		token,
		[]repoeval.Status{repoeval.StatusPreflighting},
		func(candidate *repoeval.Evaluation) error {
			candidate.Corpus = manifest
			candidate.Status = repoeval.StatusReady
			if candidate.OneShot {
				progress.Message = "Corpus is frozen; starting model evaluation."
				progress.Percent = 20
			}
			candidate.Progress = progress
			candidate.Warnings = warnings
			return nil
		})
	if err != nil && !c.handleActiveMutationError(id, err) {
		c.fail(id, token, err.Error())
		return
	}
	if err != nil || !updated.OneShot {
		return
	}
	if _, err = c.startReadyEvaluationActive(context.Background(), id, token); err != nil {
		if !c.handleActiveMutationError(id, err) {
			c.fail(id, token, err.Error())
		}
		return
	}
	c.executeEvaluationPhase(ctx, id, token)
}

func (c *repositoryModelEvaluationController) preflightManifest(
	evaluation repoeval.Evaluation,
	runID string,
	outputs map[string]any,
) (*repoeval.CorpusManifest, repoeval.Progress, []string, error) {
	commit := strings.ToLower(strings.TrimSpace(anyString(outputs["commit"])))
	inventoryHash := strings.TrimSpace(anyString(outputs["inventoryHash"]))
	selection := anyMap(outputs["selection"])
	var selected []reposcope.Candidate
	if err := decodeAny(selection["selected"], &selected); err != nil || len(selected) == 0 {
		return nil, repoeval.Progress{}, nil, errors.New("preflight returned no exact selected candidates")
	}
	if commit == "" {
		commit = strings.ToLower(strings.TrimSpace(selected[0].CommitID))
	}
	if inventoryHash == "" {
		inventoryHash = strings.TrimSpace(selected[0].InventoryID)
	}
	files := make([]repoeval.CorpusFile, 0, len(selected))
	counts := make(map[string]int)
	languages := make(map[string]repoeval.LanguageProgress)
	regions := make(map[string]map[string]struct{})
	for _, candidate := range selected {
		language := string(candidate.Language)
		counts[language]++
		if regions[language] == nil {
			regions[language] = make(map[string]struct{})
		}
		regions[language][candidate.Region] = struct{}{}
		progress := languages[language]
		progress.SelectedFiles++
		progress.SelectedBytes += candidate.Size
		languages[language] = progress
		chunkDigest := sha256.Sum256([]byte(candidate.ID + "\x00" + candidate.BlobID))
		files = append(files, repoeval.CorpusFile{
			CandidateID: candidate.ID,
			Path:        candidate.Path,
			BlobSHA:     candidate.BlobID,
			SizeBytes:   candidate.Size,
			Language:    language,
			CodeType:    repoeval.CodeType(candidate.CodeType),
			Module:      candidate.Module,
			Region:      candidate.Region,
			Chunks: []repoeval.CorpusChunk{
				{
					ID:          "chunk_" + hex.EncodeToString(chunkDigest[:]),
					StartLine:   1,
					EndLine:     1,
					ContentHash: candidate.BlobID,
				},
			},
		})
	}
	catalog := anyMap(outputs["catalog"])
	countsMap := anyMap(catalog["counts"])
	available := intMap(countsMap["availableByLanguage"])
	for language, languageProgress := range languages {
		languageProgress.AvailableFiles = max(languageProgress.SelectedFiles, available[language])
		languageProgress.Limited = languageProgress.AvailableFiles < repositoryModelEvaluationLanguageLimit(
			evaluation,
			language,
		)
		regionValues := make([]string, 0, len(regions[language]))
		for region := range regions[language] {
			regionValues = append(regionValues, region)
		}
		sort.Strings(regionValues)
		languageProgress.Regions = regionValues
		languages[language] = languageProgress
	}
	selector := anyMap(outputs["selector"])
	rationale := strings.TrimSpace(anyString(selector["rationale"]))
	policyHash, _ := stableJSONHash(repositoryModelEvaluationPolicyInput(evaluation))
	rubricHash := sha256.Sum256([]byte("repository-model-evaluation-rubric-v1"))
	manifest := &repoeval.CorpusManifest{
		CommitSHA: commit, InventoryHash: inventoryHash, PolicyHash: policyHash,
		RubricHash: hex.EncodeToString(rubricHash[:]), SelectorRunID: runID,
		SelectionRationale: rationale, Files: files, LanguageCounts: counts, GeneratedAt: c.clock(),
	}
	totalFiles := intValue(countsMap["eligibleFiles"])
	if totalFiles < len(files) {
		totalFiles = len(files)
	}
	progress := repoeval.Progress{
		Stage: repoeval.ProgressValidating, Languages: languages, TotalFiles: totalFiles,
		SelectedFiles: len(files), CompletedFiles: 0, Message: "Corpus is ready for evaluation.",
		Percent: 100, UpdatedAt: c.clock(),
	}
	warnings := stringSlice(selector["warnings"])
	if boolValue(catalog["candidatePoolTruncated"]) {
		warnings = append(
			warnings,
			"The AI selector catalog was bounded; native validation still retained every detected language.",
		)
	}
	for language, languageProgress := range languages {
		if languageProgress.Limited {
			warnings = append(
				warnings,
				fmt.Sprintf(
					"%s has only %d eligible files; all were retained.",
					language,
					languageProgress.AvailableFiles,
				),
			)
		}
	}
	for _, candidate := range evaluation.CandidateModels {
		if candidate == evaluation.JudgeModelAlias {
			warnings = append(
				warnings,
				"The judge model is also a candidate; comparison results may contain self-judging bias.",
			)
			break
		}
	}
	return manifest, progress, uniqueBoundedStrings(warnings, 256, 16<<10), nil
}

func (c *repositoryModelEvaluationController) executeEvaluation(ctx context.Context, id, token string) {
	defer c.wg.Done()
	defer c.releaseActive(id, token)
	c.executeEvaluationPhase(ctx, id, token)
}

func (c *repositoryModelEvaluationController) executeEvaluationPhase(ctx context.Context, id, token string) {
	evaluation, found, err := c.store.Get(ctx, id)
	if err != nil || !found || evaluation.Corpus == nil {
		if err == nil {
			err = errors.New("repository evaluation corpus is unavailable")
		}
		c.fail(id, token, err.Error())
		return
	}
	batches := []repositoryModelEvaluationBatch{}
	if evaluation.Status != repoeval.StatusAnalyzing {
		batches = repositoryModelEvaluationPendingBatches(evaluation)
	}
	for pendingIndex, batch := range batches {
		runID := workflows.NewRunID()
		evaluation, err = c.updateActiveLatest(
			ctx,
			id,
			token,
			[]repoeval.Status{repoeval.StatusRunning, repoeval.StatusJudging},
			func(candidate *repoeval.Evaluation) error {
				candidate.RunIDs = append(candidate.RunIDs, runID)
				candidate.Progress.Stage = repoeval.ProgressCandidateExecution
				candidate.Progress.CurrentModel = ""
				candidate.Progress.CurrentPath = ""
				candidate.Progress.Message = fmt.Sprintf(
					"Evaluating pending batch %d of %d.",
					pendingIndex+1,
					len(batches),
				)
				candidate.Progress.Percent = max(
					candidate.Progress.Percent,
					80*float64(candidate.Progress.CompletedFiles)/
						float64(max(1, candidate.Progress.SelectedFiles)),
				)
				return nil
			})
		if err != nil {
			if !c.handleActiveMutationError(id, err) {
				c.fail(id, token, err.Error())
			}
			return
		}
		result, runErr := c.runEvaluationBatch(ctx, evaluation, batch, runID, token)
		if c.ctx.Err() != nil {
			return
		}
		if ctx.Err() != nil {
			c.handleExecutionCancellation(id)
			return
		}
		if runErr != nil || result == nil || result.Status != workflows.RunStatusSucceeded {
			c.fail(id, token, repositoryModelEvaluationRunError(runErr, result))
			return
		}
		judgeJSON, judgeErr := compactJSON(result.Outputs["judge"])
		if judgeErr != nil {
			c.fail(id, token, "invalid bounded judge output: "+judgeErr.Error())
			return
		}
		mappingJSON, mappingErr := compactJSON(result.Outputs["mapping"])
		if mappingErr != nil {
			c.fail(id, token, "invalid bounded candidate mapping: "+mappingErr.Error())
			return
		}
		if evidenceErr := repositoryModelEvaluationValidateJudgeEvidence(
			judgeJSON,
			mappingJSON,
			batch.models,
		); evidenceErr != nil {
			c.fail(id, token, evidenceErr.Error())
			return
		}
		checkpoint := repoeval.BatchCheckpoint{
			ID:           batch.id,
			CandidateIDs: batch.ids,
			Candidates:   repositoryModelEvaluationBatchOutcomes(batch, result.Outputs["candidates"]),
			JudgeJSON:    judgeJSON,
			MappingJSON:  mappingJSON,
			CompletedAt:  c.clock(),
		}
		evaluation, err = c.updateActiveLatest(
			context.Background(),
			id,
			token,
			[]repoeval.Status{repoeval.StatusRunning, repoeval.StatusJudging},
			func(candidate *repoeval.Evaluation) error {
				for _, existing := range candidate.Checkpoint.Batches {
					if existing.ID == checkpoint.ID {
						return nil
					}
				}
				candidate.Checkpoint.Batches = append(candidate.Checkpoint.Batches, checkpoint)
				candidate.Progress.CurrentPath = ""
				repositoryModelEvaluationApplyCheckpointMetrics(candidate)
				candidate.Progress.Percent = max(
					candidate.Progress.Percent,
					80*float64(candidate.Progress.CompletedFiles)/
						float64(max(1, candidate.Progress.SelectedFiles)),
				)
				return nil
			})
		if err != nil {
			if !c.handleActiveMutationError(id, err) {
				c.fail(id, token, err.Error())
			}
			return
		}
	}
	if evaluation.Status == repoeval.StatusRunning {
		evaluation, err = c.updateActiveLatest(
			ctx,
			id,
			token,
			[]repoeval.Status{repoeval.StatusRunning},
			func(candidate *repoeval.Evaluation) error {
				candidate.Status = repoeval.StatusJudging
				candidate.Progress.Stage = repoeval.ProgressJudging
				candidate.Progress.CurrentModel = candidate.JudgeModelAlias
				candidate.Progress.CurrentPath = ""
				candidate.Progress.Message = "Synthesizing durable blinded judgments with the judge model."
				candidate.Progress.Percent = 90
				return nil
			})
		if err != nil {
			if !c.handleActiveMutationError(id, err) {
				c.fail(id, token, err.Error())
			}
			return
		}
	}
	if evaluation.Status != repoeval.StatusJudging && evaluation.Status != repoeval.StatusAnalyzing {
		c.fail(id, token, "repository evaluation entered an unexpected analysis state")
		return
	}
	runID := workflows.NewRunID()
	evaluation, err = c.updateActiveLatest(
		ctx,
		id,
		token,
		[]repoeval.Status{repoeval.StatusJudging, repoeval.StatusAnalyzing},
		func(candidate *repoeval.Evaluation) error {
			candidate.RunIDs = append(candidate.RunIDs, runID)
			return nil
		})
	if err != nil {
		if !c.handleActiveMutationError(id, err) {
			c.fail(id, token, err.Error())
		}
		return
	}
	result, runErr := c.runEvaluationAnalysis(ctx, evaluation, runID, token)
	if c.ctx.Err() != nil {
		return
	}
	if ctx.Err() != nil {
		c.handleExecutionCancellation(id)
		return
	}
	if runErr != nil || result == nil || result.Status != workflows.RunStatusSucceeded {
		c.fail(id, token, repositoryModelEvaluationRunError(runErr, result))
		return
	}
	if evaluation.Status == repoeval.StatusJudging {
		evaluation, err = c.updateActiveLatest(
			ctx,
			id,
			token,
			[]repoeval.Status{repoeval.StatusJudging},
			func(candidate *repoeval.Evaluation) error {
				candidate.Status = repoeval.StatusAnalyzing
				candidate.Progress.Stage = repoeval.ProgressAnalyzing
				candidate.Progress.CurrentModel = ""
				candidate.Progress.Message = "Building the comparison table from AI-judged results."
				candidate.Progress.Percent = 96
				return nil
			},
		)
		if err != nil {
			if !c.handleActiveMutationError(id, err) {
				c.fail(id, token, err.Error())
			}
			return
		}
	}
	comparisons, warnings, err := repositoryModelEvaluationComparisons(evaluation, result.Outputs["comparison"])
	if err != nil {
		c.fail(id, token, err.Error())
		return
	}
	_, err = c.updateActiveLatest(
		context.Background(),
		id,
		token,
		[]repoeval.Status{repoeval.StatusAnalyzing},
		func(candidate *repoeval.Evaluation) error {
			candidate.Comparisons = comparisons
			candidate.Warnings = uniqueBoundedStrings(append(candidate.Warnings, warnings...), 256, 16<<10)
			candidate.Status = repoeval.StatusCompleted
			candidate.Progress.Stage = repoeval.ProgressCompleted
			repositoryModelEvaluationApplyCheckpointMetrics(candidate)
			candidate.Progress.TotalTasks = candidate.Progress.CompletedTasks + 1
			candidate.Progress.CompletedTasks++
			candidate.Progress.CurrentModel = ""
			candidate.Progress.CurrentPath = ""
			candidate.Progress.Message = "Repository model evaluation completed."
			candidate.Progress.Percent = 100
			now := c.clock()
			for _, comparison := range comparisons {
				stats := candidate.ModelStats[comparison.ModelAlias]
				stats.OverallScore = 0
				if comparison.OverallScore != nil {
					stats.OverallScore = *comparison.OverallScore
				}
				stats.Summary = comparison.Verdict
				completed := now
				stats.CompletedAt = &completed
				candidate.ModelStats[comparison.ModelAlias] = stats
			}
			return nil
		})
	if err != nil && !c.handleActiveMutationError(id, err) {
		logger.ErrorC("repository-model-evaluation", "Failed to persist completed repository evaluation: "+err.Error())
	}
}

type repositoryModelEvaluationBatch struct {
	index  int
	id     string
	ids    []string
	files  []repoeval.CorpusFile
	models []string
}

func repositoryModelEvaluationBatches(evaluation repoeval.Evaluation) []repositoryModelEvaluationBatch {
	if evaluation.Corpus == nil {
		return nil
	}
	out := make(
		[]repositoryModelEvaluationBatch,
		0,
		(len(evaluation.Corpus.Files)+repositoryModelEvaluationBatchSize-1)/repositoryModelEvaluationBatchSize,
	)
	for start := 0; start < len(evaluation.Corpus.Files); start += repositoryModelEvaluationBatchSize {
		end := min(len(evaluation.Corpus.Files), start+repositoryModelEvaluationBatchSize)
		files := append([]repoeval.CorpusFile(nil), evaluation.Corpus.Files[start:end]...)
		ids := make([]string, len(files))
		for index := range files {
			ids[index] = files[index].CandidateID
		}
		digest := sha256.Sum256(
			[]byte(evaluation.ID + "\x00" + evaluation.Corpus.InventoryHash + "\x00" + strings.Join(ids, "\x00")),
		)
		out = append(
			out,
			repositoryModelEvaluationBatch{
				index: len(out), id: hex.EncodeToString(digest[:]), ids: ids, files: files,
				models: append([]string(nil), evaluation.CandidateModels...),
			},
		)
	}
	return out
}

func repositoryModelEvaluationPendingBatches(
	evaluation repoeval.Evaluation,
) []repositoryModelEvaluationBatch {
	completed := repositoryModelEvaluationCompletedPairs(evaluation)
	baseBatches := repositoryModelEvaluationBatches(evaluation)
	out := make([]repositoryModelEvaluationBatch, 0, len(baseBatches))
	for _, base := range baseBatches {
		type pendingGroup struct {
			ids    []string
			files  []repoeval.CorpusFile
			models []string
		}
		groups := make(map[string]*pendingGroup)
		order := make([]string, 0)
		for _, alias := range evaluation.CandidateModels {
			ids := make([]string, 0, len(base.ids))
			files := make([]repoeval.CorpusFile, 0, len(base.files))
			for index, id := range base.ids {
				if _, done := completed[alias+"\x00"+id]; done {
					continue
				}
				ids = append(ids, id)
				files = append(files, base.files[index])
			}
			if len(ids) == 0 {
				continue
			}
			key := strings.Join(ids, "\x00")
			group := groups[key]
			if group == nil {
				group = &pendingGroup{ids: ids, files: files}
				groups[key] = group
				order = append(order, key)
			}
			group.models = append(group.models, alias)
		}
		for _, key := range order {
			group := groups[key]
			attempt := repositoryModelEvaluationBatchAttempt(
				evaluation.Checkpoint.Batches,
				group.models,
				group.ids,
			)
			digest := sha256.Sum256([]byte(
				evaluation.ID + "\x00" + evaluation.Corpus.InventoryHash + "\x00" +
					strings.Join(group.models, "\x00") + "\x00" + strings.Join(group.ids, "\x00") +
					fmt.Sprintf("\x00%d", attempt),
			))
			out = append(out, repositoryModelEvaluationBatch{
				index: base.index, id: hex.EncodeToString(digest[:]),
				ids: append([]string(nil), group.ids...), files: append([]repoeval.CorpusFile(nil), group.files...),
				models: append([]string(nil), group.models...),
			})
		}
	}
	return out
}

func repositoryModelEvaluationCompletedPairs(evaluation repoeval.Evaluation) map[string]struct{} {
	completed := make(map[string]struct{})
	for _, checkpoint := range evaluation.Checkpoint.Batches {
		if len(checkpoint.Candidates) == 0 {
			for _, alias := range evaluation.CandidateModels {
				for _, id := range checkpoint.CandidateIDs {
					completed[alias+"\x00"+id] = struct{}{}
				}
			}
			continue
		}
		for alias, outcome := range checkpoint.Candidates {
			for _, id := range outcome.CompletedCandidateIDs {
				completed[alias+"\x00"+id] = struct{}{}
			}
		}
	}
	return completed
}

func repositoryModelEvaluationBatchAttempt(
	checkpoints []repoeval.BatchCheckpoint,
	models []string,
	ids []string,
) int {
	wantModels := append([]string(nil), models...)
	sort.Strings(wantModels)
	wantIDs := strings.Join(ids, "\x00")
	attempt := 0
	for _, checkpoint := range checkpoints {
		checkpointModels := make([]string, 0, len(checkpoint.Candidates))
		for alias := range checkpoint.Candidates {
			checkpointModels = append(checkpointModels, alias)
		}
		sort.Strings(checkpointModels)
		if strings.Join(checkpointModels, "\x00") == strings.Join(wantModels, "\x00") &&
			strings.Join(checkpoint.CandidateIDs, "\x00") == wantIDs {
			attempt++
		}
	}
	return attempt
}

func repositoryModelEvaluationBatchOutcomes(
	batch repositoryModelEvaluationBatch,
	value any,
) map[string]repoeval.BatchCandidateCheckpoint {
	children := []map[string]any{}
	_ = decodeAny(value, &children)
	pathToID := make(map[string]string, len(batch.files))
	for _, file := range batch.files {
		pathToID[file.Path] = file.CandidateID
	}
	completed := make(map[string]map[string]struct{}, len(batch.models))
	out := make(map[string]repoeval.BatchCandidateCheckpoint, len(batch.models))
	allowed := make(map[string]struct{}, len(batch.models))
	for _, alias := range batch.models {
		allowed[alias] = struct{}{}
		completed[alias] = make(map[string]struct{})
	}
	for _, child := range children {
		model := anyMap(child["model"])
		alias := strings.TrimSpace(anyString(model["requested"]))
		if alias == "" {
			alias = strings.TrimSpace(anyString(model["selected"]))
		}
		if _, ok := allowed[alias]; !ok {
			continue
		}
		outcome := out[alias]
		outcome.Attempts++
		succeeded := boolValue(child["valid"]) &&
			strings.TrimSpace(anyString(child["run_error"])) == "" &&
			strings.TrimSpace(anyString(child["error"])) == ""
		if succeeded {
			outcome.Successes++
			var scope []map[string]any
			if decodeAny(child["scope"], &scope) == nil {
				for _, item := range scope {
					if complete, declared := item["contentComplete"].(bool); declared && !complete {
						continue
					}
					if strings.TrimSpace(anyString(item["contentUnavailable"])) != "" {
						continue
					}
					if id := pathToID[strings.TrimSpace(anyString(item["path"]))]; id != "" {
						completed[alias][id] = struct{}{}
					}
				}
			}
		} else {
			outcome.Failures++
		}
		out[alias] = outcome
	}
	for _, alias := range batch.models {
		outcome := out[alias]
		for _, id := range batch.ids {
			if _, ok := completed[alias][id]; ok {
				outcome.CompletedCandidateIDs = append(outcome.CompletedCandidateIDs, id)
			}
		}
		out[alias] = outcome
	}
	return out
}

func repositoryModelEvaluationApplyCheckpointMetrics(evaluation *repoeval.Evaluation) {
	if evaluation == nil || evaluation.Corpus == nil {
		return
	}
	if evaluation.ModelStats == nil {
		evaluation.ModelStats = make(map[string]repoeval.ModelStats, len(evaluation.CandidateModels))
	}
	completedByAlias := make(map[string]map[string]struct{}, len(evaluation.CandidateModels))
	for _, alias := range evaluation.CandidateModels {
		completedByAlias[alias] = make(map[string]struct{})
		stats := evaluation.ModelStats[alias]
		stats.FilesCompleted = 0
		stats.Attempts = 0
		stats.Successes = 0
		stats.Failures = 0
		evaluation.ModelStats[alias] = stats
	}
	completedTasks := 0
	for _, checkpoint := range evaluation.Checkpoint.Batches {
		completedTasks++ // the blinded judge result is part of every durable batch checkpoint
		if len(checkpoint.Candidates) == 0 {
			calls := (len(checkpoint.CandidateIDs) + 2) / 3
			for _, alias := range evaluation.CandidateModels {
				stats := evaluation.ModelStats[alias]
				stats.Attempts += calls
				stats.Successes += calls
				evaluation.ModelStats[alias] = stats
				completedTasks += calls
				for _, id := range checkpoint.CandidateIDs {
					completedByAlias[alias][id] = struct{}{}
				}
			}
			continue
		}
		for alias, outcome := range checkpoint.Candidates {
			stats := evaluation.ModelStats[alias]
			stats.Attempts += outcome.Attempts
			stats.Successes += outcome.Successes
			stats.Failures += outcome.Failures
			evaluation.ModelStats[alias] = stats
			completedTasks += outcome.Attempts
			for _, id := range outcome.CompletedCandidateIDs {
				completedByAlias[alias][id] = struct{}{}
			}
		}
	}
	evaluation.Progress.CompletedTasks = min(evaluation.Progress.TotalTasks, completedTasks)
	for _, alias := range evaluation.CandidateModels {
		stats := evaluation.ModelStats[alias]
		stats.FilesCompleted = len(completedByAlias[alias])
		evaluation.ModelStats[alias] = stats
	}
	completedForAll := make(map[string]struct{})
	for _, file := range evaluation.Corpus.Files {
		all := true
		for _, alias := range evaluation.CandidateModels {
			if _, ok := completedByAlias[alias][file.CandidateID]; !ok {
				all = false
				break
			}
		}
		if all {
			completedForAll[file.CandidateID] = struct{}{}
		}
	}
	evaluation.Progress.CompletedFiles = len(completedForAll)
	for language, progress := range evaluation.Progress.Languages {
		progress.CompletedFiles = 0
		for _, file := range evaluation.Corpus.Files {
			if file.Language == language {
				if _, ok := completedForAll[file.CandidateID]; ok {
					progress.CompletedFiles++
				}
			}
		}
		evaluation.Progress.Languages[language] = progress
	}
}

func repositoryModelEvaluationBatchTaskCount(files, candidateModels int) int {
	if files <= 0 || candidateModels <= 0 {
		return 0
	}
	// Content-byte grouping can split every file into its own child. Use that
	// safe upper bound until durable outcomes reveal the exact child count.
	return files*candidateModels + 1 // managed candidate calls plus one blinded judge call
}

func (c *repositoryModelEvaluationController) runEvaluationBatch(
	ctx context.Context,
	evaluation repoeval.Evaluation,
	batch repositoryModelEvaluationBatch,
	runID string,
	token string,
) (*workflows.RunResult, error) {
	repository, err := normalizeRepositoryModelEvaluationRepository(ctx, evaluation.Repository)
	if err != nil {
		return nil, err
	}
	scopeJSON, _ := json.Marshal(repositoryModelEvaluationScopeInput(evaluation.Focus))
	selected := make([]reposcope.Candidate, 0, len(batch.files))
	for _, file := range batch.files {
		selected = append(selected, reposcope.Candidate{
			ID: file.CandidateID, CommitID: evaluation.Corpus.CommitSHA,
			InventoryID: evaluation.Corpus.InventoryHash, Path: file.Path, BlobID: file.BlobSHA,
			Size: file.SizeBytes, Language: reposcope.Language(file.Language),
			CodeType: reposcope.CodeType(file.CodeType), Region: file.Region, Module: file.Module,
		})
	}
	selectedJSON, _ := json.Marshal(selected)
	candidateModels := rotateRepositoryModelEvaluationCandidates(
		batch.models,
		batch.index,
	)
	ctx = repositoryModelEvaluationWithStepObserver(
		ctx,
		c.batchStepObserver(evaluation.ID, token, batch, evaluation.JudgeModelAlias),
	)
	return c.runWorkflow(ctx, workflows.RepositoryModelEvaluationBatchWorkflowYAML,
		workflows.RepositoryModelEvaluationBatchWorkflowRef, runID, map[string]any{
			"repository": repository, "commit": evaluation.Corpus.CommitSHA,
			"inventory_hash": evaluation.Corpus.InventoryHash, "scope": string(scopeJSON),
			"selected_candidates":       string(selectedJSON),
			"candidate_models":          strings.Join(candidateModels, ","),
			"candidate_identity_models": strings.Join(evaluation.CandidateModels, ","),
			"judge_model":               evaluation.JudgeModelAlias,
			"evaluation_focus":          evaluation.Focus.FreeText,
		}, c.usageObserver(evaluation.ID, token, workflows.RepositoryModelEvaluationBatchWorkflowRef))
}

func rotateRepositoryModelEvaluationCandidates(models []string, batchIndex int) []string {
	if len(models) < 2 {
		return append([]string(nil), models...)
	}
	offset := batchIndex % len(models)
	if offset < 0 {
		offset += len(models)
	}
	rotated := make([]string, 0, len(models))
	rotated = append(rotated, models[offset:]...)
	rotated = append(rotated, models[:offset]...)
	return rotated
}

func (c *repositoryModelEvaluationController) runEvaluationAnalysis(
	ctx context.Context,
	evaluation repoeval.Evaluation,
	runID string,
	token string,
) (*workflows.RunResult, error) {
	type judgedBatch struct {
		Judge        json.RawMessage                              `json:"judge"`
		Mapping      json.RawMessage                              `json:"mapping"`
		CandidateIDs []string                                     `json:"candidateIds"`
		Outcomes     map[string]repoeval.BatchCandidateCheckpoint `json:"candidateOutcomes"`
	}
	judged := make([]judgedBatch, 0, len(evaluation.Checkpoint.Batches))
	mapping := json.RawMessage("[]")
	for index, checkpoint := range evaluation.Checkpoint.Batches {
		judged = append(judged, judgedBatch{
			Judge: json.RawMessage(checkpoint.JudgeJSON), Mapping: json.RawMessage(checkpoint.MappingJSON),
			CandidateIDs: append([]string(nil), checkpoint.CandidateIDs...), Outcomes: checkpoint.Candidates,
		})
		if index == 0 {
			mapping = json.RawMessage(checkpoint.MappingJSON)
		}
	}
	judgedJSON, _ := json.Marshal(judged)
	ctx = repositoryModelEvaluationWithStepObserver(
		ctx,
		c.analysisStepObserver(evaluation.ID, token, evaluation.JudgeModelAlias),
	)
	return c.runWorkflow(ctx, workflows.RepositoryModelEvaluationAnalysisWorkflowYAML,
		workflows.RepositoryModelEvaluationAnalysisWorkflowRef, runID, map[string]any{
			"judge_model":      evaluation.JudgeModelAlias,
			"candidate_models": strings.Join(evaluation.CandidateModels, ","),
			"judged_batches":   string(judgedJSON), "candidate_mapping": string(mapping),
		}, c.usageObserver(evaluation.ID, token, workflows.RepositoryModelEvaluationAnalysisWorkflowRef))
}

func repositoryModelEvaluationWithStepObserver(
	ctx context.Context,
	observer workflows.StepActivityObserver,
) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, repositoryModelEvaluationStepObserverContextKey{}, observer)
}

func repositoryModelEvaluationStepObserver(ctx context.Context) workflows.StepActivityObserver {
	observer, _ := ctx.Value(repositoryModelEvaluationStepObserverContextKey{}).(workflows.StepActivityObserver)
	return observer
}

func (c *repositoryModelEvaluationController) preflightStepObserver(
	id string,
	token string,
) workflows.StepActivityObserver {
	return func(event workflows.StepActivityEvent) error {
		stage := repoeval.ProgressStage("")
		message := ""
		percent := float64(0)
		currentModel := ""
		switch event.StepID {
		case "checkout":
			stage, message, percent = repoeval.ProgressResolving, "Resolving the exact repository commit.", 3
		case "inventory":
			stage, message, percent = repoeval.ProgressInventorying, "Inventorying exact tracked repository blobs.", 15
		case "catalog":
			stage, message, percent = repoeval.ProgressClassifying, "Classifying safe files by language, code type, and region.", 45
		case "selector":
			stage, message, percent = repoeval.ProgressSelecting, "Selecting a representative cross-language corpus.", 72
		case "select":
			stage, message, percent = repoeval.ProgressValidating, "Validating AI choices and filling language quotas.", 94
		default:
			return nil
		}
		_, err := c.updateActiveLatest(
			context.Background(), id, token, []repoeval.Status{repoeval.StatusPreflighting},
			func(candidate *repoeval.Evaluation) error {
				if event.StepID == "selector" {
					currentModel = candidate.SelectorModelAlias
				}
				candidate.Progress.Stage = stage
				candidate.Progress.Message = message
				candidate.Progress.Percent = percent
				candidate.Progress.CurrentModel = currentModel
				candidate.Progress.CurrentPath = ""
				return nil
			},
		)
		return err
	}
}

func (c *repositoryModelEvaluationController) batchStepObserver(
	id string,
	token string,
	batch repositoryModelEvaluationBatch,
	judgeModel string,
) workflows.StepActivityObserver {
	return func(event workflows.StepActivityEvent) error {
		if len(batch.files) == 0 {
			return errors.New("repository evaluation batch has no files")
		}
		_, err := c.updateActiveLatest(
			context.Background(),
			id,
			token,
			[]repoeval.Status{repoeval.StatusRunning, repoeval.StatusJudging},
			func(candidate *repoeval.Evaluation) error {
				base := 80 * float64(candidate.Progress.CompletedFiles) /
					float64(max(1, candidate.Progress.SelectedFiles))
				switch event.StepID {
				case "checkout", "validate", "freeze", "release":
					candidate.Progress.Stage = repoeval.ProgressValidating
					candidate.Progress.CurrentModel = ""
					candidate.Progress.Message = "Validating selected corpus files against the exact commit."
					candidate.Progress.Percent = max(candidate.Progress.Percent, base)
				case "candidates":
					candidate.Progress.Stage = repoeval.ProgressCandidateExecution
					candidate.Progress.CurrentModel = ""
					candidate.Progress.Message = "Running candidate models over the current frozen corpus batch."
					candidate.Progress.Percent = max(candidate.Progress.Percent, base)
				case "judge":
					candidate.Progress.Stage = repoeval.ProgressJudging
					candidate.Progress.CurrentModel = judgeModel
					candidate.Progress.Message = "Judging blinded candidate results against exact source."
					batchShare := 80 * float64(len(batch.files)) /
						float64(max(1, candidate.Progress.SelectedFiles))
					candidate.Progress.Percent = max(
						candidate.Progress.Percent,
						min(89, base+batchShare*.9),
					)
				default:
					return nil
				}
				candidate.Progress.CurrentPath = ""
				return nil
			},
		)
		return err
	}
}

func (c *repositoryModelEvaluationController) analysisStepObserver(
	id string,
	token string,
	judgeModel string,
) workflows.StepActivityObserver {
	return func(event workflows.StepActivityEvent) error {
		if event.StepID != "analyze" {
			return nil
		}
		_, err := c.updateActiveLatest(
			context.Background(), id, token,
			[]repoeval.Status{repoeval.StatusJudging, repoeval.StatusAnalyzing},
			func(candidate *repoeval.Evaluation) error {
				candidate.Progress.CurrentModel = judgeModel
				candidate.Progress.CurrentPath = ""
				if candidate.Status == repoeval.StatusJudging {
					candidate.Progress.Stage = repoeval.ProgressJudging
					candidate.Progress.Message = "Synthesizing durable blinded judgments with the judge model."
					candidate.Progress.Percent = 92
				} else {
					candidate.Progress.Stage = repoeval.ProgressAnalyzing
					candidate.Progress.Message = "Rebuilding the final comparison from durable judgments."
					candidate.Progress.Percent = 96
				}
				return nil
			},
		)
		return err
	}
}

func (c *repositoryModelEvaluationController) usageObserver(
	id string,
	token string,
	workflowRef string,
) workflows.AgentUsageEventObserver {
	return func(event workflows.AgentUsageEvent) error {
		candidateUsage := workflowRef == workflows.RepositoryModelEvaluationBatchWorkflowRef &&
			event.StepID == "candidates"
		return c.recordUsage(id, token, event.Usage, candidateUsage)
	}
}

func (c *repositoryModelEvaluationController) runWorkflowRuntime(
	ctx context.Context,
	workflowYAML string,
	workflowRef string,
	runID string,
	inputs map[string]any,
	observe workflows.AgentUsageEventObserver,
) (*workflows.RunResult, error) {
	cfg, err := c.config()
	if err != nil {
		return nil, err
	}
	_, _, executor, err := c.handler.workflowRuntimeFromConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer closeWorkflowRuntime(executor)
	workflow, err := workflows.Parse([]byte(workflowYAML))
	if err != nil {
		return nil, err
	}
	executor.DefaultTimeout = repositoryModelEvaluationEffectiveWorkflowTimeout(
		executor.DefaultTimeout,
		workflowRef,
		inputs,
	)
	executor.AgentUsageObserver = repositoryModelEvaluationRuntimeUsageObserver(runID, observe)
	executor.StepActivityObserver = repositoryModelEvaluationRuntimeStepObserver(
		runID,
		repositoryModelEvaluationStepObserver(ctx),
	)
	return executor.Run(
		ctx,
		workflows.RunRequest{RunID: runID, Workflow: workflow, WorkflowRef: workflowRef, Inputs: inputs},
	)
}

func repositoryModelEvaluationEffectiveWorkflowTimeout(
	configured time.Duration,
	workflowRef string,
	inputs map[string]any,
) time.Duration {
	minimum := time.Duration(0)
	switch workflowRef {
	case workflows.RepositoryModelEvaluationPreflightWorkflowRef,
		workflows.RepositoryModelEvaluationAnalysisWorkflowRef:
		minimum = repositoryModelEvaluationPhaseMinTimeout
	case workflows.RepositoryModelEvaluationBatchWorkflowRef:
		var selected []json.RawMessage
		_ = decodeAny(inputs["selected_candidates"], &selected)
		models := 0
		for _, alias := range strings.Split(anyString(inputs["candidate_models"]), ",") {
			if strings.TrimSpace(alias) != "" {
				models++
			}
		}
		// A managed batch may conservatively split every file/model pair into
		// its own sequential provider call, followed by one judge request.
		tasks := max(1, len(selected)) * max(1, models)
		minimum = repositoryModelEvaluationBatchSetupBudget +
			time.Duration(tasks+1)*repositoryModelEvaluationTaskBudget
		minimum = max(minimum, repositoryModelEvaluationPhaseMinTimeout)
		minimum = min(minimum, repositoryModelEvaluationMaxTimeout)
	}
	return max(configured, minimum)
}

func repositoryModelEvaluationRuntimeStepObserver(
	runID string,
	observe workflows.StepActivityObserver,
) workflows.StepActivityObserver {
	return func(event workflows.StepActivityEvent) error {
		if event.RunID != runID || observe == nil {
			return nil
		}
		return observe(event)
	}
}

func repositoryModelEvaluationRuntimeUsageObserver(
	runID string,
	observe workflows.AgentUsageEventObserver,
) workflows.AgentUsageEventObserver {
	return func(event workflows.AgentUsageEvent) error {
		if event.RunID != runID || observe == nil {
			return nil
		}
		return observe(event)
	}
}

func (c *repositoryModelEvaluationController) recordUsage(
	id string,
	token string,
	usage workflows.AgentUsage,
	candidateUsage bool,
) error {
	if !c.activeToken(id, token) {
		return context.Canceled
	}
	cfg, _ := c.config()
	price, priceKnown := repositoryModelEvaluationUsagePrice(cfg, usage)
	_, err := c.updateActiveLatest(
		context.Background(),
		id,
		token,
		[]repoeval.Status{
			repoeval.StatusPreflighting,
			repoeval.StatusReady,
			repoeval.StatusRunning,
			repoeval.StatusJudging,
			repoeval.StatusAnalyzing,
		},
		func(candidate *repoeval.Evaluation) error {
			repositoryModelEvaluationAddUsage(&candidate.Usage, usage, price, priceKnown)
			alias := strings.TrimSpace(usage.Reviewer)
			if _, ok := candidate.ModelStats[alias]; candidateUsage && ok {
				stats := candidate.ModelStats[alias]
				repositoryModelEvaluationAddUsage(&stats.Usage, usage, price, priceKnown)
				candidate.ModelStats[alias] = stats
				model := strings.TrimSpace(usage.Model)
				if model != "" {
					if candidate.Checkpoint.ConcreteModels == nil {
						candidate.Checkpoint.ConcreteModels = make(map[string]map[string]int)
					}
					if candidate.Checkpoint.ConcreteModels[alias] == nil {
						candidate.Checkpoint.ConcreteModels[alias] = make(map[string]int)
					}
					candidate.Checkpoint.ConcreteModels[alias][model]++
				}
			}
			return nil
		})
	if errors.Is(err, errRepositoryModelEvaluationRunFenced) {
		return context.Canceled
	}
	return err
}

type repositoryModelEvaluationPrice struct {
	inputPerMillion  float64
	outputPerMillion float64
}

func repositoryModelEvaluationUsagePrice(
	cfg *config.Config,
	usage workflows.AgentUsage,
) (repositoryModelEvaluationPrice, bool) {
	prices := make(map[string]repositoryModelEvaluationPrice)
	add := func(model string, price repositoryModelEvaluationPrice) {
		if price.inputPerMillion <= 0 && price.outputPerMillion <= 0 {
			return
		}
		for _, key := range repositoryModelEvaluationPriceKeys(model) {
			current := prices[key]
			current.inputPerMillion = max(current.inputPerMillion, price.inputPerMillion)
			current.outputPerMillion = max(current.outputPerMillion, price.outputPerMillion)
			prices[key] = current
		}
	}
	for _, option := range repositoryReviewModelOptions(cfg) {
		if !option.PriceKnown {
			continue
		}
		price := repositoryModelEvaluationPrice{
			inputPerMillion: option.InputPricePer1M, outputPerMillion: option.OutputPricePer1M,
		}
		add(option.Alias, price)
		add(option.ResolvedModel, price)
	}
	if cfg != nil {
		for _, alias := range cfg.ModelAliases {
			for _, accountRef := range repositoryReviewRuntimeAccountRefs(cfg) {
				resolved, err := cfg.ResolveModelAliasConfig(alias.Name, accountRef)
				if err != nil || resolved == nil {
					continue
				}
				price := repositoryModelEvaluationPrice{
					inputPerMillion:  resolved.InputPricePerMTok,
					outputPerMillion: resolved.OutputPricePerMTok,
				}
				if price.inputPerMillion <= 0 && price.outputPerMillion <= 0 &&
					resolved.Subscription && strings.TrimSpace(resolved.SubscriptionEquivalentModel) != "" {
					if inherited, ok := repositoryReviewAliasPrice(
						cfg,
						resolved.SubscriptionEquivalentModel,
						make(map[string]bool),
					); ok {
						price.inputPerMillion = inherited.InputPricePerMTok
						price.outputPerMillion = inherited.OutputPricePerMTok
					}
				}
				add(resolved.Model, price)
			}
		}
	}
	model := strings.TrimSpace(usage.Model)
	for _, key := range repositoryModelEvaluationPriceKeys(model) {
		if price, ok := prices[key]; ok {
			return price, true
		}
	}
	if model != "" {
		return repositoryModelEvaluationPrice{}, false
	}
	for _, key := range repositoryModelEvaluationPriceKeys(usage.Reviewer) {
		if price, ok := prices[key]; ok {
			return price, true
		}
	}
	return repositoryModelEvaluationPrice{}, false
}

func repositoryModelEvaluationPriceKeys(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	keys := []string{value}
	if _, suffix, ok := strings.Cut(value, "/"); ok && strings.TrimSpace(suffix) != "" {
		keys = append(keys, strings.TrimSpace(suffix))
	}
	return keys
}

func repositoryModelEvaluationAddUsage(
	target *repoeval.Usage,
	usage workflows.AgentUsage,
	price repositoryModelEvaluationPrice,
	priceKnown bool,
) {
	if target == nil {
		return
	}
	priorRequests := target.Requests
	prompt := int64(max(0, usage.PromptTokens))
	output := int64(max(0, usage.CompletionTokens))
	target.Requests++
	target.InputTokens += prompt
	target.CachedInputTokens += int64(min(max(0, usage.CachedTokens), usage.PromptTokens))
	target.OutputTokens += output
	target.ReasoningTokens += int64(max(0, usage.ReasoningTokens))
	target.DurationMillis += max(int64(0), usage.LatencyMillis)
	if !priceKnown {
		target.EstimatedCostUSD = nil
		return
	}
	cost := (float64(prompt)*price.inputPerMillion + float64(output)*price.outputPerMillion) / 1_000_000
	if target.EstimatedCostUSD != nil {
		*target.EstimatedCostUSD += cost
	} else if priorRequests == 0 {
		target.EstimatedCostUSD = &cost
	}
}

func (c *repositoryModelEvaluationController) updateLatest(
	ctx context.Context,
	id string,
	mutate func(*repoeval.Evaluation) error,
) (repoeval.Evaluation, error) {
	for attempt := 0; attempt < 32; attempt++ {
		current, found, err := c.store.Get(ctx, id)
		if err != nil {
			return repoeval.Evaluation{}, err
		}
		if !found {
			return repoeval.Evaluation{}, os.ErrNotExist
		}
		updated, err := c.store.Update(ctx, id, current.Version, mutate)
		if errors.Is(err, repoeval.ErrConflict) {
			continue
		}
		return updated, err
	}
	return repoeval.Evaluation{}, repoeval.ErrConflict
}

func (c *repositoryModelEvaluationController) updateActiveLatest(
	ctx context.Context,
	id string,
	token string,
	allowed []repoeval.Status,
	mutate func(*repoeval.Evaluation) error,
) (repoeval.Evaluation, error) {
	return c.updateLatest(ctx, id, func(candidate *repoeval.Evaluation) error {
		if c.ctx.Err() != nil || !c.activeToken(id, token) {
			return errRepositoryModelEvaluationRunFenced
		}
		allowedStatus := false
		for _, status := range allowed {
			if candidate.Status == status {
				allowedStatus = true
				break
			}
		}
		if !allowedStatus {
			return errRepositoryModelEvaluationRunFenced
		}
		return mutate(candidate)
	})
}

func (c *repositoryModelEvaluationController) handleActiveMutationError(id string, err error) bool {
	if !errors.Is(err, errRepositoryModelEvaluationRunFenced) && !errors.Is(err, context.Canceled) {
		return false
	}
	if c.ctx.Err() == nil {
		_, _ = c.finishCanceled(context.Background(), id)
	}
	return true
}

func (c *repositoryModelEvaluationController) finishCanceled(
	ctx context.Context,
	id string,
) (repoeval.Evaluation, error) {
	return c.updateLatest(ctx, id, func(candidate *repoeval.Evaluation) error {
		if candidate.Status == repoeval.StatusCanceled {
			return nil
		}
		if candidate.Status != repoeval.StatusCanceling {
			return repoeval.ErrInvalidTransition
		}
		candidate.Status = repoeval.StatusCanceled
		candidate.Progress.Stage = repoeval.ProgressCanceled
		candidate.Progress.CurrentModel = ""
		candidate.Progress.CurrentPath = ""
		candidate.Progress.Message = "Canceled at a durable boundary."
		return nil
	})
}

func (c *repositoryModelEvaluationController) handleExecutionCancellation(id string) {
	if c.ctx.Err() != nil {
		return
	}
	_, _ = c.finishCanceled(context.Background(), id)
}

func (c *repositoryModelEvaluationController) fail(id, token, detail string) {
	if c.ctx.Err() != nil || !c.activeToken(id, token) {
		return
	}
	detail = sanitizeRepositoryModelEvaluationRuntimeText(detail, c.workspace, c.gitWorkspaceRoot)
	_, err := c.updateActiveLatest(
		context.Background(),
		id,
		token,
		[]repoeval.Status{
			repoeval.StatusPreflighting,
			repoeval.StatusReady,
			repoeval.StatusRunning,
			repoeval.StatusJudging,
			repoeval.StatusAnalyzing,
		},
		func(candidate *repoeval.Evaluation) error {
			return repositoryModelEvaluationApplyFailure(candidate, detail)
		},
	)
	if err != nil {
		c.handleActiveMutationError(id, err)
	}
}

func repositoryModelEvaluationApplyFailure(
	candidate *repoeval.Evaluation,
	detail string,
	paths ...string,
) error {
	if candidate == nil {
		return repoeval.ErrInvalidEvaluation
	}
	detail = sanitizeRepositoryModelEvaluationRuntimeText(detail, append(paths, candidate.Repository)...)
	if detail == "" {
		detail = boundedRepositoryModelEvaluationDetail(detail)
	}
	candidate.Status = repoeval.StatusFailed
	candidate.Failure = detail
	candidate.Progress.Stage = repoeval.ProgressFailed
	candidate.Progress.CurrentModel = ""
	candidate.Progress.CurrentPath = ""
	candidate.Progress.Message = detail
	return nil
}

func sanitizeRepositoryModelEvaluationRuntimeText(value string, paths ...string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	for _, pathValue := range paths {
		pathValue = strings.TrimSpace(pathValue)
		if pathValue == "" ||
			(!filepath.IsAbs(pathValue) && !strings.HasPrefix(strings.ToLower(pathValue), "file://")) {
			continue
		}
		value = strings.ReplaceAll(value, pathValue, "[repository path]")
	}
	value = redactRepositoryModelEvaluationAbsolutePaths(value)
	return boundedRepositoryModelEvaluationDetail(value)
}

func redactRepositoryModelEvaluationAbsolutePaths(value string) string {
	var sanitized strings.Builder
	for index := 0; index < len(value); {
		if value[index] != '/' || !repositoryModelEvaluationAbsolutePathBoundary(value, index) {
			sanitized.WriteByte(value[index])
			index++
			continue
		}
		end := index + 1
		for end < len(value) && !strings.ContainsRune(" \t\r\n'\"`()[]{}<>,;", rune(value[end])) {
			end++
		}
		sanitized.WriteString("[filesystem path]")
		index = end
	}
	return sanitized.String()
}

func repositoryModelEvaluationAbsolutePathBoundary(value string, index int) bool {
	if index == 0 {
		return true
	}
	previous := value[index-1]
	if previous == ':' {
		return index+1 >= len(value) || value[index+1] != '/'
	}
	return strings.ContainsRune(" \t\r\n'\"`()[]{}<>=,;", rune(previous))
}

func (c *repositoryModelEvaluationController) clock() time.Time {
	if c.now == nil {
		return time.Now().UTC()
	}
	return c.now().UTC()
}

func repositoryModelEvaluationScopeInput(focus repoeval.Focus) map[string]any {
	return map[string]any{
		"codeTypes": focus.CodeTypes, "includePrefixes": focus.IncludeFolders,
		"excludePrefixes": focus.ExcludeFolders, "freeText": focus.FreeText,
	}
}

func repositoryModelEvaluationPolicyInput(evaluation repoeval.Evaluation) map[string]any {
	return map[string]any{
		"defaultPerLanguage": evaluation.DefaultFilesPerLanguage,
		"perLanguage":        evaluation.FilesPerLanguage,
	}
}

func repositoryModelEvaluationLanguageLimit(evaluation repoeval.Evaluation, language string) int {
	if limit, ok := evaluation.FilesPerLanguage[language]; ok {
		return limit
	}
	return evaluation.DefaultFilesPerLanguage
}

func repositoryModelEvaluationRunError(runErr error, result *workflows.RunResult) string {
	if runErr != nil {
		return boundedRepositoryModelEvaluationDetail(runErr.Error())
	}
	if result != nil && strings.TrimSpace(result.Error) != "" {
		return boundedRepositoryModelEvaluationDetail(result.Error)
	}
	return "The repository model evaluation workflow failed."
}

func boundedRepositoryModelEvaluationDetail(value string) string {
	const maximum = 64 << 10
	value = strings.TrimSpace(value)
	if value == "" {
		return "The repository model evaluation failed."
	}
	if len(value) <= maximum {
		return value
	}
	end := maximum - 3
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + "..."
}

func stableJSONHash(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func compactJSON(value any) (string, error) {
	if raw, ok := value.(string); ok {
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(raw)); err != nil {
			return "", err
		}
		if compact.Len() > 256<<10 {
			return "", errors.New("structured workflow evidence exceeds its durable bound")
		}
		return compact.String(), nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(encoded) > 256<<10 {
		return "", errors.New("structured workflow evidence exceeds its durable bound")
	}
	return string(encoded), nil
}

func decodeAny(value any, target any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if raw, ok := value.(string); ok && json.Valid([]byte(raw)) {
		encoded = []byte(raw)
	}
	return json.Unmarshal(encoded, target)
}

func anyMap(value any) map[string]any {
	if mapped, ok := value.(map[string]any); ok {
		return mapped
	}
	var mapped map[string]any
	if decodeAny(value, &mapped) != nil {
		return map[string]any{}
	}
	if mapped == nil {
		return map[string]any{}
	}
	return mapped
}

func anyString(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(value)
	}
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	default:
		return 0
	}
}

func intMap(value any) map[string]int {
	var out map[string]int
	if decodeAny(value, &out) != nil || out == nil {
		return map[string]int{}
	}
	return out
}

func stringSlice(value any) []string {
	var out []string
	if decodeAny(value, &out) != nil {
		return nil
	}
	return out
}

func boolValue(value any) bool {
	valueBool, _ := value.(bool)
	return valueBool
}

func uniqueBoundedStrings(values []string, maximumCount, maximumBytes int) []string {
	out := make([]string, 0, min(len(values), maximumCount))
	seen := make(map[string]struct{})
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > maximumBytes {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
		if len(out) == maximumCount {
			break
		}
	}
	return out
}

type repositoryModelEvaluationAnalysisOutput struct {
	Comparisons []struct {
		ModelAlias   string             `json:"modelAlias"`
		Rank         int                `json:"rank"`
		Completion   string             `json:"completion"`
		Scores       map[string]float64 `json:"scores"`
		OverallScore *float64           `json:"overallScore"`
		Verdict      string             `json:"verdict"`
		Strengths    []string           `json:"strengths"`
		Limitations  []string           `json:"limitations"`
	} `json:"comparisons"`
	Warnings []string `json:"warnings"`
}

func repositoryModelEvaluationComparisons(
	evaluation repoeval.Evaluation,
	value any,
) ([]repoeval.ModelComparison, []string, error) {
	var analysis repositoryModelEvaluationAnalysisOutput
	if err := decodeAny(value, &analysis); err != nil {
		return nil, nil, fmt.Errorf("invalid repository evaluation analysis: %w", err)
	}
	byAlias := make(map[string]repoeval.ModelComparison, len(analysis.Comparisons))
	confirmed, unsupported := repositoryModelEvaluationJudgedClaimCounts(evaluation.Checkpoint.Batches)
	metrics := repoeval.Clone(evaluation)
	repositoryModelEvaluationApplyCheckpointMetrics(&metrics)
	for _, row := range analysis.Comparisons {
		alias := strings.TrimSpace(row.ModelAlias)
		if alias == "" {
			continue
		}
		languages, regions, filesAnalyzed, bytesAnalyzed := repositoryModelEvaluationAnalyzedScope(
			evaluation,
			alias,
		)
		completion := repositoryModelEvaluationObjectiveCompletion(filesAnalyzed, len(evaluation.Corpus.Files))
		comparison := repoeval.ModelComparison{
			ModelAlias:        alias,
			ConcreteModels:    cloneNestedConcrete(evaluation.Checkpoint.ConcreteModels[alias]),
			Completion:        completion,
			Rank:              row.Rank,
			OverallScore:      row.OverallScore,
			Scores:            row.Scores,
			Languages:         languages,
			Regions:           regions,
			FilesAnalyzed:     filesAnalyzed,
			BytesAnalyzed:     bytesAnalyzed,
			Failures:          metrics.ModelStats[alias].Failures,
			ConfirmedFindings: confirmed[alias],
			UnsupportedFiles:  min(filesAnalyzed, unsupported[alias]),
			Usage:             evaluation.ModelStats[alias].Usage,
			Verdict:           row.Verdict,
			Summary:           "AI-judged comparison over successfully analyzed immutable corpus files.",
			Strengths:         row.Strengths,
			Limitations:       row.Limitations,
		}
		if completion == repoeval.ModelCompletionFailed {
			comparison.Failure = "Candidate evaluation did not produce a score."
			comparison.Rank = 0
			comparison.OverallScore = nil
			comparison.Scores = map[string]float64{}
		} else if completion == repoeval.ModelCompletionPartial {
			comparison.Failure = "Candidate evaluation completed only partially."
			comparison.Rank = 0
			comparison.OverallScore = nil
			comparison.Scores = map[string]float64{}
		}
		byAlias[alias] = comparison
	}
	rows := make([]repoeval.ModelComparison, 0, len(evaluation.CandidateModels))
	for _, alias := range evaluation.CandidateModels {
		row, ok := byAlias[alias]
		if !ok {
			languages, regions, filesAnalyzed, bytesAnalyzed := repositoryModelEvaluationAnalyzedScope(
				evaluation,
				alias,
			)
			row = repoeval.ModelComparison{
				ModelAlias: alias, ConcreteModels: cloneNestedConcrete(evaluation.Checkpoint.ConcreteModels[alias]),
				Completion: repoeval.ModelCompletionFailed, Failure: "The analyzer omitted this candidate.",
				Scores: map[string]float64{}, Languages: languages, Regions: regions,
				FilesAnalyzed: filesAnalyzed, BytesAnalyzed: bytesAnalyzed,
				Failures: metrics.ModelStats[alias].Failures, Usage: evaluation.ModelStats[alias].Usage,
			}
		}
		rows = append(rows, row)
	}
	ranked := make([]int, 0, len(rows))
	for index := range rows {
		if rows[index].Completion != repoeval.ModelCompletionFailed && rows[index].OverallScore != nil {
			ranked = append(ranked, index)
		}
	}
	sort.SliceStable(
		ranked,
		func(i, j int) bool { return *rows[ranked[i]].OverallScore > *rows[ranked[j]].OverallScore },
	)
	for rank, index := range ranked {
		rows[index].Rank = rank + 1
	}
	return rows, analysis.Warnings, nil
}

func repositoryModelEvaluationObjectiveCompletion(filesAnalyzed, selectedFiles int) repoeval.ModelCompletion {
	if filesAnalyzed <= 0 || selectedFiles <= 0 {
		return repoeval.ModelCompletionFailed
	}
	if filesAnalyzed < selectedFiles {
		return repoeval.ModelCompletionPartial
	}
	return repoeval.ModelCompletionCompleted
}

func repositoryModelEvaluationAnalyzedScope(
	evaluation repoeval.Evaluation,
	alias string,
) ([]string, []string, int, int64) {
	completed := repositoryModelEvaluationCompletedPairs(evaluation)
	languageSet := make(map[string]struct{})
	regionSet := make(map[string]struct{})
	files := 0
	var bytes int64
	for _, file := range evaluation.Corpus.Files {
		if _, ok := completed[alias+"\x00"+file.CandidateID]; !ok {
			continue
		}
		files++
		bytes += file.SizeBytes
		languageSet[file.Language] = struct{}{}
		regionSet[file.Region] = struct{}{}
	}
	languages := make([]string, 0, len(languageSet))
	for language := range languageSet {
		languages = append(languages, language)
	}
	regions := make([]string, 0, len(regionSet))
	for region := range regionSet {
		regions = append(regions, region)
	}
	sort.Strings(languages)
	sort.Strings(regions)
	return languages, regions, files, bytes
}

func repositoryModelEvaluationJudgedClaimCounts(batches []repoeval.BatchCheckpoint) (map[string]int, map[string]int) {
	confirmed := make(map[string]int)
	unsupported := make(map[string]int)
	for _, batch := range batches {
		var mapping []struct {
			CandidateID string `json:"candidateId"`
			ModelAlias  string `json:"modelAlias"`
		}
		var judge struct {
			Evaluations []struct {
				CandidateID       string `json:"candidateId"`
				ConfirmedClaims   int    `json:"confirmedClaims"`
				UnsupportedClaims int    `json:"unsupportedClaims"`
			} `json:"evaluations"`
		}
		if json.Unmarshal([]byte(batch.MappingJSON), &mapping) != nil ||
			json.Unmarshal([]byte(batch.JudgeJSON), &judge) != nil {
			continue
		}
		aliases := make(map[string]string, len(mapping))
		for _, item := range mapping {
			aliases[item.CandidateID] = item.ModelAlias
		}
		for _, item := range judge.Evaluations {
			alias := aliases[item.CandidateID]
			if alias == "" {
				continue
			}
			confirmed[alias] += max(0, item.ConfirmedClaims)
			unsupported[alias] += max(0, item.UnsupportedClaims)
		}
	}
	return confirmed, unsupported
}

func repositoryModelEvaluationValidateJudgeEvidence(
	judgeJSON string,
	mappingJSON string,
	expectedAliases []string,
) error {
	var mapping []struct {
		CandidateID string `json:"candidateId"`
		ModelAlias  string `json:"modelAlias"`
	}
	var judge struct {
		Evaluations []struct {
			CandidateID string `json:"candidateId"`
		} `json:"evaluations"`
	}
	if json.Unmarshal([]byte(mappingJSON), &mapping) != nil ||
		json.Unmarshal([]byte(judgeJSON), &judge) != nil || len(mapping) == 0 {
		return errors.New("invalid bounded judge identity evidence")
	}
	expected := make(map[string]struct{}, len(expectedAliases))
	for _, alias := range expectedAliases {
		expected[strings.TrimSpace(alias)] = struct{}{}
	}
	allowedIDs := make(map[string]struct{}, len(mapping))
	mappedAliases := make(map[string]struct{}, len(mapping))
	for _, item := range mapping {
		id := strings.TrimSpace(item.CandidateID)
		alias := strings.TrimSpace(item.ModelAlias)
		_, aliasExpected := expected[alias]
		if id == "" || alias == "" || !aliasExpected {
			return errors.New("judge mapping contains an absent candidate alias")
		}
		if _, duplicate := allowedIDs[id]; duplicate {
			return errors.New("judge mapping contains a duplicate candidate ID")
		}
		if _, duplicate := mappedAliases[alias]; duplicate {
			return errors.New("judge mapping contains a duplicate candidate alias")
		}
		allowedIDs[id] = struct{}{}
		mappedAliases[alias] = struct{}{}
	}
	seen := make(map[string]struct{}, len(judge.Evaluations))
	for _, item := range judge.Evaluations {
		id := strings.TrimSpace(item.CandidateID)
		if _, allowed := allowedIDs[id]; !allowed {
			return errors.New("judge evaluated an absent candidate")
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("judge evaluated a candidate more than once")
		}
		seen[id] = struct{}{}
	}
	if len(mappedAliases) != len(expected) || len(seen) != len(allowedIDs) {
		return errors.New("judge omitted a present candidate")
	}
	return nil
}

func cloneNestedConcrete(values map[string]int) map[string]int {
	out := make(map[string]int, len(values))
	for model, count := range values {
		out[model] = count
	}
	return out
}
