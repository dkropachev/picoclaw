package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	osexec "os/exec"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

var (
	errRepositoryReviewAutomationBusy    = errors.New("repository review automation is already active")
	errRepositoryReviewSafeStop          = errors.New("repository review stopped at a safe checkpoint")
	errRepositoryReviewInvalidTransition = errors.New("repository review action is not valid for the current status")
	errRepositoryReviewCommitSelection   = errors.New("repository review commit selection is required")
	errRepositoryReviewPauseSettled      = errors.New("repository review is already stopped")
	errRepositoryReviewProfileActive     = errors.New(
		"repository review profile is assigned to an active repository review",
	)
)

const (
	repositoryReviewControllerInterval = 5 * time.Second
	repositoryReviewQuotaProbeTimeout  = 30 * time.Second
)

type repositoryReviewActiveRun struct {
	runID        string
	pauseReason  repoaudit.RepositoryReviewPauseReason
	pauseDetail  string
	store        repoaudit.Store
	config       *config.Config
	reservations map[int]repositoryReviewTaskReservation
	guardMu      *sync.Mutex
}

type repositoryReviewTaskReservation struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CostUSD          float64
	CostKnown        bool
}

type repositoryReviewAutomationUpdater func(
	context.Context,
	repoaudit.Store,
	string,
	int64,
	func(*repoaudit.RepositoryReviewAutomation) error,
) (repoaudit.RepositoryReviewAutomation, error)

type repositoryReviewCommitResolver func(
	context.Context,
	*config.Config,
	repoaudit.RepositoryReviewAutomation,
	string,
) (string, error)

func updateRepositoryReviewAutomation(
	ctx context.Context,
	store repoaudit.Store,
	id string,
	expectedVersion int64,
	mutate func(*repoaudit.RepositoryReviewAutomation) error,
) (repoaudit.RepositoryReviewAutomation, error) {
	return store.UpdateAutomation(ctx, id, expectedVersion, mutate)
}

type repositoryReviewController struct {
	handler *Handler
	ctx     context.Context
	cancel  context.CancelFunc

	startOnce     sync.Once
	stopOnce      sync.Once
	releaseOnce   sync.Once
	wg            sync.WaitGroup
	admissionWG   sync.WaitGroup
	lifecycleMu   sync.Mutex
	stopped       bool
	startErr      error
	releaseLease  func()
	leasedStore   repoaudit.Store
	leasedConfig  *config.Config
	mu            sync.Mutex
	active        map[string]*repositoryReviewActiveRun
	now           func() time.Time
	probe         func(context.Context) (codexAccountLimitsResponse, error)
	update        repositoryReviewAutomationUpdater
	resolveCommit repositoryReviewCommitResolver
	stopTimeout   time.Duration
	monitorEvery  time.Duration
	progressEvery time.Duration
	runBatch      func(
		context.Context,
		repoaudit.RepositoryReviewAutomation,
		string,
		workflows.AgentUsageObserver,
	) (*workflows.RunResult, error)
}

func newRepositoryReviewController(handler *Handler) *repositoryReviewController {
	ctx, cancel := context.WithCancel(context.Background())
	return &repositoryReviewController{
		handler:       handler,
		ctx:           ctx,
		cancel:        cancel,
		active:        make(map[string]*repositoryReviewActiveRun),
		now:           time.Now,
		probe:         loadCodexAccountLimits,
		update:        updateRepositoryReviewAutomation,
		resolveCommit: resolveRepositoryReviewAutomationCommit,
		stopTimeout:   10 * time.Second,
		monitorEvery:  repositoryReviewControllerInterval,
		progressEvery: time.Second,
	}
}

func (h *Handler) repositoryReviewControllerInstance() *repositoryReviewController {
	if h == nil {
		return nil
	}
	h.repositoryReviewControllerMu.Lock()
	defer h.repositoryReviewControllerMu.Unlock()
	if h.repositoryReviewController == nil {
		h.repositoryReviewController = newRepositoryReviewController(h)
	}
	return h.repositoryReviewController
}

// StartRepositoryReviewController starts the durable quota/recovery monitor.
// It is safe to call more than once.
func (h *Handler) StartRepositoryReviewController() {
	if controller := h.repositoryReviewControllerInstance(); controller != nil {
		if err := controller.Start(); err != nil {
			logger.ErrorC("repository-review", "Repository review controller did not start: "+err.Error())
		}
	}
}

func (h *Handler) stopRepositoryReviewController() {
	if h == nil {
		return
	}
	h.repositoryReviewControllerMu.Lock()
	controller := h.repositoryReviewController
	h.repositoryReviewControllerMu.Unlock()
	if controller != nil {
		controller.Stop()
	}
}

func (c *repositoryReviewController) Start() error {
	if c == nil {
		return errors.New("repository review controller is unavailable")
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.stopped {
		return context.Canceled
	}
	c.startOnce.Do(func() {
		store, cfg, err := c.store()
		if err == nil {
			c.releaseLease, err = store.LockAutomationController()
		}
		if err != nil {
			c.startErr = err
			c.cancel()
			return
		}
		c.leasedStore = store
		c.leasedConfig = cfg
		c.wg.Add(1)
		go c.monitor()
	})
	return c.startErr
}

func (c *repositoryReviewController) Stop() {
	if c == nil {
		return
	}
	c.lifecycleMu.Lock()
	c.stopOnce.Do(func() {
		c.stopped = true
		c.cancel()
	})
	c.lifecycleMu.Unlock()
	done := make(chan struct{})
	go func() {
		c.admissionWG.Wait()
		c.wg.Wait()
		c.releaseOnce.Do(func() {
			if c.releaseLease != nil {
				c.releaseLease()
			}
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(c.stopTimeout):
		logger.WarnC("repository-review", "Timed out waiting for repository review controller shutdown")
	}
}

func (c *repositoryReviewController) store() (repoaudit.Store, *config.Config, error) {
	if c == nil || c.handler == nil {
		return repoaudit.Store{}, nil, errors.New("repository review controller is unavailable")
	}
	cfg, err := config.LoadConfig(c.handler.configPath)
	if err != nil {
		return repoaudit.Store{}, nil, err
	}
	return repoaudit.NewStore(cfg.WorkspacePath()), cfg, nil
}

func resolveRepositoryReviewAutomationCommit(
	ctx context.Context,
	cfg *config.Config,
	automation repoaudit.RepositoryReviewAutomation,
	revision string,
) (string, error) {
	if cfg == nil {
		return "", errors.New("repository review configuration is unavailable")
	}
	manager, err := gitworkspace.NewManager(gitworkspace.Options{
		RootDir:             cfg.GitWorkspaceRootPath(),
		MaxTotalSizeBytes:   cfg.GitWorkspaces.EffectiveMaxTotalSizeBytes(),
		IgnoredCleanupDelay: cfg.GitWorkspaces.EffectiveIgnoredCleanupDelay(),
		DropDelay:           cfg.GitWorkspaces.EffectiveDropDelay(),
	})
	if err != nil {
		return "", fmt.Errorf("initialize repository review commit resolver: %w", err)
	}
	ref := strings.TrimSpace(revision)
	if ref == "" {
		ref = strings.TrimSpace(automation.Ref)
	}
	sessionKey := "repository-review-commit/" + automation.ID + "/" + workflows.NewRunID()
	workspace, err := manager.Acquire(ctx, gitworkspace.AcquireRequest{
		Repository: automation.Repository,
		Ref:        ref,
		Fresh:      true,
		SessionKey: sessionKey,
		AgentID:    "repository-review-controller",
	})
	if err != nil {
		return "", fmt.Errorf("resolve repository review commit: %w", err)
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, releaseErr := manager.ReleaseSession(releaseCtx, gitworkspace.ReleaseRequest{
			SessionKey: sessionKey,
			AgentID:    "repository-review-controller",
		}); releaseErr != nil {
			logger.WarnCF("repository-review", "Failed to release commit-resolution workspace", map[string]any{
				"automation_id": automation.ID,
				"error":         releaseErr.Error(),
			})
		}
	}()
	output, err := repositoryReviewGitOutput(
		ctx,
		workspace.Path,
		128,
		"git",
		"rev-parse",
		"--verify",
		"--end-of-options",
		"HEAD^{commit}",
	)
	if err != nil {
		return "", fmt.Errorf("resolve repository review commit ID: %w", err)
	}
	commit := strings.ToLower(strings.TrimSpace(string(output)))
	if !repositoryReviewValidCommitSHA(commit) {
		return "", errors.New("repository review resolved a noncanonical commit")
	}
	return commit, nil
}

func repositoryReviewGitOutput(
	ctx context.Context,
	directory string,
	maximumBytes int64,
	name string,
	arguments ...string,
) ([]byte, error) {
	command := osexec.CommandContext(ctx, name, arguments...)
	command.Dir = directory
	command.Env = repositoryReviewGitEnvironment()
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, maximumBytes+1))
	waitErr := command.Wait()
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		return nil, waitErr
	}
	if int64(len(output)) > maximumBytes {
		return nil, errors.New("git command output is too large")
	}
	return output, nil
}

func repositoryReviewGitEnvironment() []string {
	environment := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "LC_ALL", "GIT_PAGER":
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "LC_ALL=C", "GIT_PAGER=cat")
}

func repositoryReviewRememberedCommit(
	automation repoaudit.RepositoryReviewAutomation,
) string {
	if commit := strings.ToLower(strings.TrimSpace(automation.ResolvedCommitSHA)); repositoryReviewValidCommitSHA(commit) {
		return commit
	}
	commit := strings.ToLower(strings.TrimSpace(automation.ScopePlan.CommitSHA))
	if repositoryReviewValidCommitSHA(commit) {
		return commit
	}
	return ""
}

func repositoryReviewValidCommitSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func repositoryReviewValidCommitSelection(value string) bool {
	return repositoryReviewValidCommitSHA(strings.ToLower(strings.TrimSpace(value)))
}

func (c *repositoryReviewController) startAutomation(
	ctx context.Context,
	id string,
	expectedVersion int64,
	resetBudget bool,
	action string,
) (repoaudit.RepositoryReviewAutomation, error) {
	return c.startAutomationAtCommit(
		ctx,
		id,
		expectedVersion,
		resetBudget,
		action,
		"",
	)
}

func (c *repositoryReviewController) repositoryReviewCommitOptions(
	ctx context.Context,
	id string,
) (repoaudit.RepositoryReviewAutomation, string, string, error) {
	if c == nil || c.resolveCommit == nil {
		return repoaudit.RepositoryReviewAutomation{}, "", "", errors.New(
			"repository review commit resolver is unavailable",
		)
	}
	if err := c.Start(); err != nil {
		return repoaudit.RepositoryReviewAutomation{}, "", "", err
	}
	c.lifecycleMu.Lock()
	if c.stopped || c.ctx.Err() != nil {
		c.lifecycleMu.Unlock()
		return repoaudit.RepositoryReviewAutomation{}, "", "", context.Canceled
	}
	c.admissionWG.Add(1)
	c.lifecycleMu.Unlock()
	defer c.admissionWG.Done()
	optionsCtx, cancelOptions := context.WithCancel(c.ctx)
	stopCallerCancellation := context.AfterFunc(ctx, cancelOptions)
	defer func() {
		stopCallerCancellation()
		cancelOptions()
	}()
	ctx = optionsCtx
	store := c.leasedStore
	cfg, err := c.currentLeasedConfiguration()
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, "", "", err
	}
	automation, found, err := store.GetAutomation(ctx, id)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, "", "", err
	}
	if !found {
		return repoaudit.RepositoryReviewAutomation{}, "", "", os.ErrNotExist
	}
	if automation.Status != repoaudit.RepositoryReviewAutomationPaused {
		return repoaudit.RepositoryReviewAutomation{}, "", "", errRepositoryReviewInvalidTransition
	}
	remembered := repositoryReviewRememberedCommit(automation)
	resolutionAutomation, err := repositoryReviewAutomationResolutionTarget(automation)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, "", "", err
	}
	latest, err := c.resolveCommit(ctx, cfg, resolutionAutomation, "")
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, "", "", err
	}
	latest = strings.ToLower(strings.TrimSpace(latest))
	if !repositoryReviewValidCommitSHA(latest) {
		return repoaudit.RepositoryReviewAutomation{}, "", "", errors.New(
			"repository review resolved a noncanonical latest commit",
		)
	}
	if remembered == "" {
		remembered = latest
	}
	current, currentFound, err := store.GetAutomation(ctx, id)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, "", "", err
	}
	if !currentFound {
		return repoaudit.RepositoryReviewAutomation{}, "", "", os.ErrNotExist
	}
	if current.Version != automation.Version ||
		current.Status != repoaudit.RepositoryReviewAutomationPaused ||
		repositoryReviewRememberedCommit(current) != repositoryReviewRememberedCommit(automation) {
		return repoaudit.RepositoryReviewAutomation{}, "", "", repoaudit.ErrConflict
	}
	return current, remembered, latest, nil
}

func (c *repositoryReviewController) startAutomationAtCommit(
	ctx context.Context,
	id string,
	expectedVersion int64,
	resetBudget bool,
	action string,
	commitSelection string,
) (repoaudit.RepositoryReviewAutomation, error) {
	if err := c.Start(); err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	c.lifecycleMu.Lock()
	if c.stopped || c.ctx.Err() != nil {
		c.lifecycleMu.Unlock()
		return repoaudit.RepositoryReviewAutomation{}, context.Canceled
	}
	c.admissionWG.Add(1)
	c.lifecycleMu.Unlock()
	defer c.admissionWG.Done()
	admissionCtx, cancelAdmission := context.WithCancel(c.ctx)
	stopCallerCancellation := context.AfterFunc(ctx, cancelAdmission)
	defer func() {
		stopCallerCancellation()
		cancelAdmission()
	}()
	ctx = admissionCtx
	store := c.leasedStore
	cfg, err := c.currentLeasedConfiguration()
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	automation, found, err := store.GetAutomation(ctx, id)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	if !found {
		return repoaudit.RepositoryReviewAutomation{}, errors.New("repository review automation not found")
	}
	if automation.Version != expectedVersion {
		return repoaudit.RepositoryReviewAutomation{}, repoaudit.ErrConflict
	}
	rememberedAtAdmission := repositoryReviewRememberedCommit(automation)
	switch action {
	case "start":
		if automation.Status != repoaudit.RepositoryReviewAutomationIdle {
			return repoaudit.RepositoryReviewAutomation{}, errRepositoryReviewInvalidTransition
		}
	case "resume":
		if automation.Status != repoaudit.RepositoryReviewAutomationPaused {
			return repoaudit.RepositoryReviewAutomation{}, errRepositoryReviewInvalidTransition
		}
	case "restart":
		if automation.Status != repoaudit.RepositoryReviewAutomationPaused &&
			automation.Status != repoaudit.RepositoryReviewAutomationCompleted &&
			automation.Status != repoaudit.RepositoryReviewAutomationFailed {
			return repoaudit.RepositoryReviewAutomation{}, errRepositoryReviewInvalidTransition
		}
	default:
		return repoaudit.RepositoryReviewAutomation{}, errRepositoryReviewInvalidTransition
	}
	restart := action == "restart"
	c.mu.Lock()
	_, locallyActive := c.active[id]
	c.mu.Unlock()
	if locallyActive || automation.Status == repoaudit.RepositoryReviewAutomationRunning ||
		automation.Status == repoaudit.RepositoryReviewAutomationStopping {
		return repoaudit.RepositoryReviewAutomation{}, errRepositoryReviewAutomationBusy
	}
	automation, err = c.normalizeRepositoryReviewAutomationAdmission(ctx, store, automation)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	automation, err = c.materializeLatestRepositoryReviewProfile(ctx, store, automation)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	effectiveAccount := repositoryReviewEffectiveAccountRef(cfg, automation.AccountRef)
	if validationErr := validateSelectableAccountRef(cfg, effectiveAccount); validationErr != nil {
		return repoaudit.RepositoryReviewAutomation{}, fmt.Errorf(
			"%w: account_ref: %v", repoaudit.ErrInvalidAutomation, validationErr,
		)
	}
	for _, model := range repositoryReviewExecutionModels(automation) {
		if !repositoryReviewAliasAvailableForAccount(cfg, model, effectiveAccount) {
			return repoaudit.RepositoryReviewAutomation{}, fmt.Errorf(
				"%w: reviewer model %q is unavailable on account %q",
				repoaudit.ErrInvalidAutomation, model, effectiveAccount,
			)
		}
	}
	commitAutomation := automation
	if repositoryReviewRememberedCommit(commitAutomation) == "" && rememberedAtAdmission != "" {
		commitAutomation.ResolvedCommitSHA = rememberedAtAdmission
	}
	resolvedCommit, err := c.resolveRepositoryReviewAdmissionCommit(
		ctx,
		cfg,
		commitAutomation,
		action,
		commitSelection,
	)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	expectedVersion = automation.Version
	priced := automation
	if pricingErr := repositoryReviewRefreshAccountingSnapshot(cfg, &priced); pricingErr != nil {
		return repoaudit.RepositoryReviewAutomation{}, pricingErr
	}
	automation, err = c.update(
		ctx,
		store,
		id,
		expectedVersion,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			candidate.ModelPrices = maps.Clone(priced.ModelPrices)
			candidate.EffectiveAccountRef = effectiveAccount
			return nil
		},
	)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	expectedVersion = automation.Version

	if resetBudget || restart {
		automation, err = c.update(
			ctx,
			store,
			id,
			expectedVersion,
			func(candidate *repoaudit.RepositoryReviewAutomation) error {
				candidate.Usage = repoaudit.RepositoryReviewTokenUsage{}
				candidate.EstimatedCostUSD = 0
				if restart {
					candidate.Progress = repoaudit.RepositoryReviewProgress{}
					candidate.ModelStats = make(map[string]repoaudit.RepositoryReviewModelStats)
					candidate.ModelCoverageSketches = make(map[string]string)
					candidate.StartedAt = time.Time{}
					candidate.CompletedAt = time.Time{}
				}
				return nil
			},
		)
		if err != nil {
			return repoaudit.RepositoryReviewAutomation{}, err
		}
		expectedVersion = automation.Version
	}
	runID := workflows.NewRunID()
	now := c.clock()
	c.lifecycleMu.Lock()
	if c.stopped || c.ctx.Err() != nil {
		c.lifecycleMu.Unlock()
		return repoaudit.RepositoryReviewAutomation{}, context.Canceled
	}
	c.mu.Lock()
	if _, exists := c.active[id]; exists {
		c.mu.Unlock()
		c.lifecycleMu.Unlock()
		return repoaudit.RepositoryReviewAutomation{}, errRepositoryReviewAutomationBusy
	}
	c.active[id] = &repositoryReviewActiveRun{
		runID: runID, store: store, config: cfg,
		reservations: make(map[int]repositoryReviewTaskReservation),
		guardMu:      &sync.Mutex{},
	}
	c.mu.Unlock()
	updated, err := c.update(
		ctx,
		store,
		id,
		expectedVersion,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			if candidate.ResolvedCommitSHA != resolvedCommit {
				candidate.ScopePlan = repoaudit.RepositoryReviewScopePlan{}
			}
			candidate.ResolvedCommitSHA = resolvedCommit
			candidate.Status = repoaudit.RepositoryReviewAutomationRunning
			candidate.PauseReason = ""
			candidate.PauseDetail = ""
			candidate.RequestedPauseReason = ""
			candidate.RequestedPauseDetail = ""
			candidate.ActiveRunID = runID
			candidate.RunIDs = append(candidate.RunIDs, runID)
			candidate.AccountLimitSnapshots = nil
			candidate.CompletedAt = time.Time{}
			if candidate.StartedAt.IsZero() {
				candidate.StartedAt = now
			}
			candidate.Progress.Stage = "queued"
			candidate.Progress.TotalBatches = max(
				candidate.Progress.TotalBatches,
				candidate.Progress.CompletedBatches+1,
			)
			return nil
		},
	)
	if err != nil {
		c.removeActive(id, runID)
		c.lifecycleMu.Unlock()
		return repoaudit.RepositoryReviewAutomation{}, err
	}

	c.wg.Add(1)
	go c.executeAutomation(id, runID)
	c.lifecycleMu.Unlock()
	return updated, nil
}

func (c *repositoryReviewController) resolveRepositoryReviewAdmissionCommit(
	ctx context.Context,
	cfg *config.Config,
	automation repoaudit.RepositoryReviewAutomation,
	action string,
	selection string,
) (string, error) {
	if c == nil || c.resolveCommit == nil {
		return "", errors.New("repository review commit resolver is unavailable")
	}
	selection = strings.TrimSpace(selection)
	if selection != "" && !repositoryReviewValidCommitSelection(selection) {
		return "", fmt.Errorf(
			"%w: commit_sha must be a full 40 or 64 character hexadecimal commit ID",
			repoaudit.ErrInvalidAutomation,
		)
	}
	remembered := repositoryReviewRememberedCommit(automation)
	if action == "start" && selection == "" && remembered != "" &&
		strings.EqualFold(strings.TrimSpace(automation.Progress.Stage), "next batch queued") {
		return remembered, nil
	}
	if action == "resume" && selection == "" {
		latest, err := c.resolveCommit(ctx, cfg, automation, "")
		if err != nil {
			return "", err
		}
		latest = strings.ToLower(strings.TrimSpace(latest))
		if !repositoryReviewValidCommitSHA(latest) {
			return "", errors.New("repository review resolved a noncanonical latest commit")
		}
		if remembered != "" && remembered != latest {
			return "", fmt.Errorf(
				"%w: remembered commit %s differs from latest commit %s",
				errRepositoryReviewCommitSelection,
				remembered,
				latest,
			)
		}
		if remembered != "" {
			return remembered, nil
		}
		return latest, nil
	}
	resolved, err := c.resolveCommit(ctx, cfg, automation, selection)
	if err != nil {
		if selection != "" {
			return "", fmt.Errorf(
				"%w: commit_sha could not be resolved in this repository: %v",
				repoaudit.ErrInvalidAutomation,
				err,
			)
		}
		return "", err
	}
	resolved = strings.ToLower(strings.TrimSpace(resolved))
	if !repositoryReviewValidCommitSHA(resolved) {
		return "", errors.New("repository review resolved a noncanonical commit")
	}
	return resolved, nil
}

func (c *repositoryReviewController) normalizeRepositoryReviewAutomationAdmission(
	ctx context.Context,
	store repoaudit.Store,
	automation repoaudit.RepositoryReviewAutomation,
) (repoaudit.RepositoryReviewAutomation, error) {
	normalized, err := repositoryReviewAutomationResolutionTarget(automation)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	if automation.Repository == normalized.Repository && automation.Ref == normalized.Ref && automation.Target == "all" {
		return automation, nil
	}
	updated, err := c.update(
		ctx,
		store,
		automation.ID,
		automation.Version,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			candidate.Repository = normalized.Repository
			candidate.Ref = normalized.Ref
			candidate.Target = "all"
			resetRepositoryReviewExecutionCampaign(candidate)
			return nil
		},
	)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	return updated, nil
}

func repositoryReviewAutomationResolutionTarget(
	automation repoaudit.RepositoryReviewAutomation,
) (repoaudit.RepositoryReviewAutomation, error) {
	repository, err := normalizeRepositoryReviewAutomationRepository(automation.Repository)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	branch := automation.Ref
	if automation.ProfileID == "" && strings.EqualFold(branch, "HEAD") {
		branch = ""
	} else {
		branch, err = repoaudit.NormalizeRepositoryReviewBranch(branch)
		if err != nil {
			return repoaudit.RepositoryReviewAutomation{}, err
		}
	}
	automation.Repository = repository
	automation.Ref = branch
	automation.Target = "all"
	return automation, nil
}

func (c *repositoryReviewController) materializeLatestRepositoryReviewProfile(
	ctx context.Context,
	store repoaudit.Store,
	automation repoaudit.RepositoryReviewAutomation,
) (repoaudit.RepositoryReviewAutomation, error) {
	if strings.TrimSpace(automation.ProfileID) == "" {
		return automation, nil
	}
	profile, found, err := store.GetProfile(ctx, automation.ProfileID)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	if !found {
		return repoaudit.RepositoryReviewAutomation{}, fmt.Errorf(
			"repository review profile %q not found", automation.ProfileID,
		)
	}
	materialized, err := repoaudit.MaterializeRepositoryReviewAutomation(profile, automation)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	materialized.Name = repositoryReviewAssignedAutomationName(materialized.Repository, profile.Name)
	if repositoryReviewProfileSnapshotMatches(automation, materialized) {
		return automation, nil
	}
	cfg, err := config.LoadConfig(c.handler.configPath)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	if pricingErr := repositoryReviewRefreshAccountingSnapshot(cfg, &materialized); pricingErr != nil {
		return repoaudit.RepositoryReviewAutomation{}, pricingErr
	}
	materializedPrices := maps.Clone(materialized.ModelPrices)
	updated, err := c.update(
		ctx,
		store,
		automation.ID,
		automation.Version,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			materialized, materializeErr := repoaudit.MaterializeRepositoryReviewAutomation(
				profile, *candidate,
			)
			if materializeErr != nil {
				return materializeErr
			}
			materialized.Name = repositoryReviewAssignedAutomationName(
				materialized.Repository, profile.Name,
			)
			materialized.ModelPrices = maps.Clone(materializedPrices)
			resetRepositoryReviewExecutionCampaign(&materialized)
			*candidate = materialized
			return nil
		},
	)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	return updated, nil
}

func repositoryReviewProfileSnapshotMatches(
	automation repoaudit.RepositoryReviewAutomation,
	materialized repoaudit.RepositoryReviewAutomation,
) bool {
	return automation.ProfileID == materialized.ProfileID &&
		automation.ProfileVersion == materialized.ProfileVersion &&
		automation.AccountRef == materialized.AccountRef &&
		automation.Name == materialized.Name &&
		automation.Target == "all" &&
		automation.ReviewFocus == materialized.ReviewFocus &&
		reflect.DeepEqual(automation.ScopePolicy, materialized.ScopePolicy) &&
		reflect.DeepEqual(automation.ReviewerModels, materialized.ReviewerModels) &&
		!automation.CompareModels &&
		automation.Force == materialized.Force &&
		automation.AutoContinue == materialized.AutoContinue &&
		automation.MaxFilesPerRun == materialized.MaxFilesPerRun &&
		automation.MaxContentBytes == materialized.MaxContentBytes &&
		automation.MaxParallelChildren == materialized.MaxParallelChildren &&
		automation.EstimatedOutputTokens == materialized.EstimatedOutputTokens &&
		reflect.DeepEqual(automation.BudgetPolicy, materialized.BudgetPolicy)
}

func resetRepositoryReviewExecutionCampaign(automation *repoaudit.RepositoryReviewAutomation) {
	if automation == nil {
		return
	}
	automation.ScopePlan = repoaudit.RepositoryReviewScopePlan{}
	automation.ResolvedCommitSHA = ""
	automation.RequestedPauseReason = ""
	automation.RequestedPauseDetail = ""
	automation.ActiveRunID = ""
	automation.EffectiveAccountRef = ""
	automation.Usage = repoaudit.RepositoryReviewTokenUsage{}
	automation.EstimatedCostUSD = 0
	automation.Progress = repoaudit.RepositoryReviewProgress{}
	automation.ModelStats = make(map[string]repoaudit.RepositoryReviewModelStats)
	automation.ModelCoverageSketches = make(map[string]string)
	automation.AccountLimitSnapshots = nil
	automation.StartedAt = time.Time{}
	automation.CompletedAt = time.Time{}
}

func (c *repositoryReviewController) currentLeasedConfiguration() (*config.Config, error) {
	if c == nil || c.handler == nil || c.leasedConfig == nil {
		return nil, errors.New("repository review controller lease is unavailable")
	}
	cfg, err := config.LoadConfig(c.handler.configPath)
	if err != nil {
		return nil, err
	}
	if cfg.WorkspacePath() != c.leasedConfig.WorkspacePath() {
		return nil, errors.New("repository review workspace changed; restart the launcher before controlling reviews")
	}
	return cfg, nil
}

func (c *repositoryReviewController) pauseAutomation(
	ctx context.Context,
	id string,
	expectedVersion int64,
) (repoaudit.RepositoryReviewAutomation, error) {
	return c.pauseAutomationForRun(ctx, id, expectedVersion, "")
}

func (c *repositoryReviewController) pauseAutomationForRun(
	ctx context.Context,
	id string,
	expectedVersion int64,
	expectedRunID string,
) (repoaudit.RepositoryReviewAutomation, error) {
	if err := c.Start(); err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	c.lifecycleMu.Lock()
	if c.stopped || c.ctx.Err() != nil {
		c.lifecycleMu.Unlock()
		return repoaudit.RepositoryReviewAutomation{}, context.Canceled
	}
	c.admissionWG.Add(1)
	c.lifecycleMu.Unlock()
	defer c.admissionWG.Done()
	pauseCtx, cancelPause := context.WithCancel(c.ctx)
	stopCallerCancellation := context.AfterFunc(ctx, cancelPause)
	defer func() {
		stopCallerCancellation()
		cancelPause()
	}()
	ctx = pauseCtx
	active, locallyActive := c.activeRunSnapshot(id, "")
	store := active.store
	if !locallyActive {
		store = c.leasedStore
	}
	current, found, err := store.GetAutomation(ctx, id)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	if !found {
		return repoaudit.RepositoryReviewAutomation{}, os.ErrNotExist
	}
	if expectedVersion < 1 || expectedVersion > current.Version {
		return repoaudit.RepositoryReviewAutomation{}, repoaudit.ErrConflict
	}
	expectedRunID = strings.TrimSpace(expectedRunID)
	if expectedRunID == "" {
		if expectedVersion != current.Version {
			return repoaudit.RepositoryReviewAutomation{}, repoaudit.ErrConflict
		}
	} else if len(expectedRunID) > 1024 || !repositoryReviewPauseRunMatches(current, expectedRunID) {
		return repoaudit.RepositoryReviewAutomation{}, repoaudit.ErrConflict
	}
	if current.Status == repoaudit.RepositoryReviewAutomationStopping ||
		current.Status == repoaudit.RepositoryReviewAutomationPaused ||
		current.Status == repoaudit.RepositoryReviewAutomationCompleted ||
		current.Status == repoaudit.RepositoryReviewAutomationFailed {
		return current, nil
	}
	latchedRunID := current.ActiveRunID
	updated, err := c.updateLatest(ctx, store, id, func(candidate *repoaudit.RepositoryReviewAutomation) error {
		if expectedRunID == "" && candidate.Version != expectedVersion {
			return repoaudit.ErrConflict
		}
		if expectedRunID != "" && !repositoryReviewPauseRunMatches(*candidate, expectedRunID) {
			return repoaudit.ErrConflict
		}
		switch candidate.Status {
		case repoaudit.RepositoryReviewAutomationRunning:
			candidate.Status = repoaudit.RepositoryReviewAutomationStopping
			candidate.RequestedPauseReason = repoaudit.RepositoryReviewPauseManual
			candidate.RequestedPauseDetail = "Paused manually after the current safe checkpoint."
			candidate.Progress.Stage = "stopping after current batch"
		case repoaudit.RepositoryReviewAutomationIdle:
			if !candidate.AutoContinue ||
				!strings.EqualFold(strings.TrimSpace(candidate.Progress.Stage), "next batch queued") {
				return errRepositoryReviewInvalidTransition
			}
			candidate.Status = repoaudit.RepositoryReviewAutomationPaused
			candidate.PauseReason = repoaudit.RepositoryReviewPauseManual
			candidate.PauseDetail = "Paused before the next review batch started."
			candidate.Progress.Stage = "paused"
		case repoaudit.RepositoryReviewAutomationStopping,
			repoaudit.RepositoryReviewAutomationPaused,
			repoaudit.RepositoryReviewAutomationCompleted,
			repoaudit.RepositoryReviewAutomationFailed:
			return errRepositoryReviewPauseSettled
		default:
			return errRepositoryReviewInvalidTransition
		}
		return nil
	})
	if errors.Is(err, errRepositoryReviewPauseSettled) {
		settled, settledFound, settledErr := store.GetAutomation(ctx, id)
		if settledErr != nil {
			return repoaudit.RepositoryReviewAutomation{}, settledErr
		}
		if !settledFound {
			return repoaudit.RepositoryReviewAutomation{}, os.ErrNotExist
		}
		return settled, nil
	}
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	c.latchManualPause(id, latchedRunID)
	return updated, nil
}

func (c *repositoryReviewController) latchManualPause(id, runID string) {
	if c == nil || strings.TrimSpace(runID) == "" {
		return
	}
	c.mu.Lock()
	if active := c.active[id]; active != nil && active.runID == runID {
		active.pauseReason = repoaudit.RepositoryReviewPauseManual
		active.pauseDetail = "Paused manually after the current safe checkpoint."
	}
	c.mu.Unlock()
}

func repositoryReviewPauseRunMatches(
	automation repoaudit.RepositoryReviewAutomation,
	expectedRunID string,
) bool {
	expectedRunID = strings.TrimSpace(expectedRunID)
	if expectedRunID == "" {
		return false
	}
	if automation.ActiveRunID != "" {
		return automation.ActiveRunID == expectedRunID
	}
	return len(automation.RunIDs) > 0 && automation.RunIDs[len(automation.RunIDs)-1] == expectedRunID
}

func (c *repositoryReviewController) executeAutomation(id, runID string) {
	defer c.wg.Done()
	runCtx, cancel := context.WithCancel(c.ctx)
	defer cancel()

	activeSnapshot, ok := c.activeRunSnapshot(id, runID)
	if !ok {
		c.finishAutomationRun(id, runID, nil, errors.New("repository review active run is unavailable"), false)
		return
	}
	store, cfg := activeSnapshot.store, activeSnapshot.config
	automation, found, err := store.GetAutomation(runCtx, id)
	if err != nil || !found || automation.ActiveRunID != runID {
		if err == nil {
			err = errors.New("repository review automation disappeared before execution")
		}
		c.finishAutomationRun(id, runID, nil, err, false)
		return
	}
	priceIndex := repositoryReviewAccountingIndex(nil, automation)
	observeUsage := func(usage workflows.AgentUsage) error {
		return c.recordUsage(id, runID, usage, priceIndex)
	}
	if c.runBatch != nil {
		result, runErr := c.runBatch(runCtx, automation, runID, observeUsage)
		if commitErr := repositoryReviewValidateExecutionCommit(automation, result); commitErr != nil {
			runErr = errors.Join(runErr, commitErr)
		}
		checkpointed := runErr == nil && result != nil && result.Status == workflows.RunStatusSucceeded
		c.finishAutomationRun(id, runID, result, runErr, checkpointed)
		return
	}

	_, workflowStore, executor, err := c.handler.workflowRuntimeFromConfig(runCtx, cfg)
	if err != nil {
		c.finishAutomationRun(id, runID, nil, err, false)
		return
	}
	defer closeWorkflowRuntime(executor)
	workflow, err := workflows.Parse([]byte(workflows.RepositoryBugFinderWorkflowYAML))
	if err != nil {
		c.finishAutomationRun(id, runID, nil, err, false)
		return
	}
	priceIndex = repositoryReviewAccountingIndex(cfg, automation)
	executor.AgentUsageObserver = repositoryReviewAgentUsageObserver(runID, observeUsage)
	executor.AgentCallAdmission = repositoryReviewAgentCallAdmissionObserver(
		runID,
		func() error { return c.admitProviderCall(id, runID) },
	)
	executor.ManagedChildActivityObserver = func(event workflows.ManagedChildActivityEvent) error {
		if event.RunID != runID || event.StepID != "review" {
			return nil
		}
		return c.observeRepositoryReviewTask(id, runID, event.ManagedChildActivity)
	}

	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		c.monitorWorkflowProgress(runCtx, store, workflowStore, id, runID)
	}()
	scopePolicyJSON, err := json.Marshal(automation.ScopePolicy)
	if err != nil {
		c.finishAutomationRun(id, runID, nil, err, false)
		return
	}
	result, runErr := executor.Run(runCtx, workflows.RunRequest{
		RunID:       runID,
		Workflow:    workflow,
		WorkflowRef: workflows.RepositoryBugFinderWorkflowRef,
		Inputs: map[string]any{
			"repository":              automation.Repository,
			"account_ref":             repositoryReviewEffectiveAccountRef(cfg, automation.EffectiveAccountRef),
			"ref":                     repositoryReviewExecutionRef(automation),
			"target":                  automation.Target,
			"review_focus":            automation.ReviewFocus,
			"review_models":           strings.Join(repositoryReviewExecutionModels(automation), ","),
			"planner_model":           repositoryReviewPlannerModel(automation),
			"scope_policy":            string(scopePolicyJSON),
			"force":                   automation.Force,
			"max_content_bytes":       automation.MaxContentBytes,
			"max_files_per_run":       automation.MaxFilesPerRun,
			"max_parallel_children":   automation.MaxParallelChildren,
			"estimated_output_tokens": automation.EstimatedOutputTokens,
		},
	})
	if commitErr := repositoryReviewValidateExecutionCommit(automation, result); commitErr != nil {
		runErr = errors.Join(runErr, commitErr)
	}
	checkpointed := false
	if persisted, persistedErr := workflowStore.GetRun(context.Background(), runID); persistedErr == nil {
		c.recordManagedChildOutcomes(id, runID, persisted, priceIndex)
		checkpointed = runErr == nil && repositoryReviewRunCheckpointed(persisted, result)
	}
	cancel()
	<-monitorDone
	c.finishAutomationRun(id, runID, result, runErr, checkpointed)
}

func repositoryReviewWorkflowRef(branch string) string {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return ""
	}
	return "refs/heads/" + branch
}

func repositoryReviewExecutionRef(automation repoaudit.RepositoryReviewAutomation) string {
	if commit := repositoryReviewRememberedCommit(automation); commit != "" {
		return commit
	}
	return repositoryReviewWorkflowRef(automation.Ref)
}

func repositoryReviewValidateExecutionCommit(
	automation repoaudit.RepositoryReviewAutomation,
	result *workflows.RunResult,
) error {
	expected := repositoryReviewRememberedCommit(automation)
	if expected == "" || result == nil || result.Outputs == nil {
		return nil
	}
	actual := strings.ToLower(strings.TrimSpace(fmt.Sprint(result.Outputs["commit"])))
	if actual == "" || actual == "<nil>" {
		return nil
	}
	if actual != expected {
		return fmt.Errorf(
			"repository review workflow resolved commit %s, want remembered commit %s",
			actual,
			expected,
		)
	}
	return nil
}

func repositoryReviewAgentUsageObserver(
	runID string,
	observe workflows.AgentUsageObserver,
) workflows.AgentUsageEventObserver {
	return func(event workflows.AgentUsageEvent) error {
		if event.RunID != runID {
			return nil
		}
		return observe(event.Usage)
	}
}

func repositoryReviewAgentCallAdmissionObserver(
	runID string,
	admit workflows.AgentCallAdmission,
) workflows.AgentCallAdmissionEventObserver {
	return func(event workflows.AgentCallAdmissionEvent) error {
		if event.RunID != runID {
			return nil
		}
		return admit()
	}
}

func repositoryReviewExecutionModels(automation repoaudit.RepositoryReviewAutomation) []string {
	if automation.CompareModels || len(automation.ReviewerModels) <= 1 {
		return append([]string(nil), automation.ReviewerModels...)
	}
	return append([]string(nil), automation.ReviewerModels[0])
}

func repositoryReviewPlannerModel(automation repoaudit.RepositoryReviewAutomation) string {
	models := repositoryReviewExecutionModels(automation)
	if len(models) == 0 {
		return ""
	}
	return models[0]
}

type repositoryReviewAccountingModel struct {
	alias string
	price repoaudit.RepositoryReviewModelPrice
	known bool
}

func repositoryReviewAccountingIndex(
	cfg *config.Config,
	automation repoaudit.RepositoryReviewAutomation,
) map[string]repositoryReviewAccountingModel {
	index := make(map[string]repositoryReviewAccountingModel)
	conservative := repositoryReviewAccountingModel{}
	aliases := make(map[string]config.ModelAliasConfig)
	if cfg != nil {
		for _, alias := range cfg.ModelAliases {
			aliases[alias.Name] = alias
		}
	}
	for _, aliasName := range repositoryReviewExecutionModels(automation) {
		price, known := automation.ModelPrices[aliasName]
		entry := repositoryReviewAccountingModel{alias: aliasName, price: price, known: known}
		if known {
			conservative.known = true
			conservative.price.InputPricePer1M = max(
				conservative.price.InputPricePer1M, price.InputPricePer1M,
			)
			conservative.price.OutputPricePer1M = max(
				conservative.price.OutputPricePer1M, price.OutputPricePer1M,
			)
		}
		index[aliasName] = entry
		if alias, exists := aliases[aliasName]; exists {
			for _, model := range append([]string{alias.Model}, mapStringValues(alias.AccountOverrides)...) {
				model = strings.TrimSpace(model)
				if model == "" {
					continue
				}
				index[model] = entry
				if _, concrete, ok := strings.Cut(model, "/"); ok && concrete != "" {
					index[concrete] = entry
				}
			}
		}
	}
	if models := repositoryReviewExecutionModels(automation); len(models) == 1 {
		index[""] = index[models[0]]
	}
	if conservative.known {
		index["*"] = conservative
	}
	return index
}

func mapStringValues(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, values[key])
	}
	return out
}

func (c *repositoryReviewController) recordUsage(
	id string,
	runID string,
	usage workflows.AgentUsage,
	priceIndex map[string]repositoryReviewAccountingModel,
) error {
	activeSnapshot, ok := c.activeRunSnapshot(id, runID)
	if !ok {
		return c.latchAccountingFailure(id, runID, errors.New("repository review active store is unavailable"))
	}
	store := activeSnapshot.store
	accounting, known := priceIndex[strings.TrimSpace(usage.Reviewer)]
	if !known {
		accounting, known = priceIndex[strings.TrimSpace(usage.Model)]
	}
	if !known {
		accounting, known = priceIndex[""]
	}
	if !known {
		accounting, known = priceIndex["*"]
	}
	priceKnown := accounting.known &&
		(accounting.price.InputPricePer1M > 0 || accounting.price.OutputPricePer1M > 0)
	promptTokens := max(0, usage.PromptTokens)
	completionTokens := max(0, usage.CompletionTokens)
	totalTokens := max(max(0, usage.TotalTokens), promptTokens+completionTokens)
	cachedTokens := min(max(0, usage.CachedTokens), promptTokens)
	cost := 0.0
	if priceKnown {
		cost = (float64(promptTokens)*accounting.price.InputPricePer1M +
			float64(completionTokens)*accounting.price.OutputPricePer1M) / 1_000_000
	}
	updated, updateErr := c.updateLatest(
		context.Background(),
		store,
		id,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			if candidate.ActiveRunID != runID {
				return nil
			}
			candidate.Usage.PromptTokens += int64(promptTokens)
			candidate.Usage.CompletionTokens += int64(completionTokens)
			candidate.Usage.CachedTokens += int64(cachedTokens)
			candidate.Usage.TotalTokens += int64(totalTokens)
			candidate.EstimatedCostUSD += cost
			if known && accounting.alias != "" {
				stats := candidate.ModelStats[accounting.alias]
				stats.Tokens.PromptTokens += int64(promptTokens)
				stats.Tokens.CompletionTokens += int64(completionTokens)
				stats.Tokens.CachedTokens += int64(cachedTokens)
				stats.Tokens.TotalTokens += int64(totalTokens)
				stats.EstimatedCostUSD += cost
				stats.Requests++
				stats.LatencyMillis += max(int64(0), usage.LatencyMillis)
				candidate.ModelStats[accounting.alias] = stats
			}
			return nil
		},
	)
	if updateErr != nil {
		logger.WarnCF("repository-review", "Failed to persist repository review usage", map[string]any{
			"automation_id": id, "run_id": runID, "error": updateErr.Error(),
		})
		return c.latchAccountingFailure(id, runID, updateErr)
	}
	_ = updated
	return nil
}

func (c *repositoryReviewController) latchAccountingFailure(
	id string,
	runID string,
	accountingErr error,
) error {
	detail := "Usage accounting failed closed; no additional review work will be admitted."
	if accountingErr != nil {
		detail += " " + accountingErr.Error()
	}
	detail = repositoryReviewBoundedDetail(detail)
	c.mu.Lock()
	if active := c.active[id]; active != nil && active.runID == runID {
		active.pauseReason = repoaudit.RepositoryReviewPauseRunFailed
		active.pauseDetail = detail
	}
	c.mu.Unlock()
	return errors.Join(errRepositoryReviewSafeStop, accountingErr)
}

type repositoryReviewChildOutcome struct {
	failures      int64
	reviewedPaths []string
}

func (c *repositoryReviewController) recordManagedChildOutcomes(
	id string,
	runID string,
	run *workflows.Run,
	accountingIndex map[string]repositoryReviewAccountingModel,
) {
	if run == nil {
		return
	}
	review := repositoryReviewRunStep(run, "review")
	children := repositoryReviewAnySlice(review.Outputs["managed_children"])
	if len(children) == 0 {
		return
	}
	outcomes := make(map[string]repositoryReviewChildOutcome)
	for _, raw := range children {
		child, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		modelMeta, _ := child["model"].(map[string]any)
		model := ""
		if requested, exists := modelMeta["requested"]; exists && requested != nil {
			model = strings.TrimSpace(fmt.Sprint(requested))
		}
		if model == "" || model == "<nil>" {
			model = strings.TrimSpace(fmt.Sprint(modelMeta["selected"]))
		}
		accounting, found := accountingIndex[model]
		if !found {
			accounting, found = accountingIndex[""]
		}
		if !found || accounting.alias == "" {
			continue
		}
		admitted, _ := child["admitted"].(bool)
		if !admitted {
			continue
		}
		outcome := outcomes[accounting.alias]
		valid, _ := child["valid"].(bool)
		runError := ""
		if rawError, exists := child["run_error"]; exists && rawError != nil {
			runError = strings.TrimSpace(fmt.Sprint(rawError))
		}
		if !valid || runError != "" {
			outcome.failures++
			outcomes[accounting.alias] = outcome
			continue
		}
		scopePaths := make(map[string]struct{})
		for _, scopeRaw := range repositoryReviewAnySlice(child["scope"]) {
			if scope, ok := scopeRaw.(map[string]any); ok {
				if path := strings.TrimSpace(fmt.Sprint(scope["path"])); path != "" && path != "<nil>" {
					scopePaths[path] = struct{}{}
				}
			}
		}
		acknowledged := make(map[string]struct{})
		if structured, ok := child["structured"].(map[string]any); ok {
			reviewed := repositoryReviewAnySlice(structured["reviewedFiles"])
			if len(reviewed) == 0 {
				reviewed = repositoryReviewAnySlice(structured["reviewed_files"])
			}
			for _, rawPath := range reviewed {
				path := strings.TrimSpace(fmt.Sprint(rawPath))
				if _, assigned := scopePaths[path]; assigned {
					acknowledged[path] = struct{}{}
				}
			}
		}
		for path := range acknowledged {
			outcome.reviewedPaths = append(outcome.reviewedPaths, path)
		}
		outcomes[accounting.alias] = outcome
	}
	if len(outcomes) == 0 {
		return
	}
	activeSnapshot, ok := c.activeRunSnapshot(id, runID)
	if !ok {
		return
	}
	store := activeSnapshot.store
	_, _ = c.updateLatest(context.Background(), store, id, func(candidate *repoaudit.RepositoryReviewAutomation) error {
		if candidate.ActiveRunID != runID {
			return nil
		}
		for alias, outcome := range outcomes {
			stats := candidate.ModelStats[alias]
			stats.Failures += outcome.failures
			stats.Requests = max(stats.Requests, stats.Failures)
			candidate.ModelStats[alias] = stats
			addRepositoryReviewModelPaths(candidate, alias, outcome.reviewedPaths)
		}
		return nil
	})
}

func (c *repositoryReviewController) admitProviderCall(id, runID string) error {
	if c == nil {
		return errRepositoryReviewSafeStop
	}
	if err := c.ctx.Err(); err != nil {
		return errors.Join(errRepositoryReviewSafeStop, err)
	}
	c.mu.Lock()
	active := c.active[id]
	if active == nil || active.runID != runID {
		c.mu.Unlock()
		return fmt.Errorf("%w: repository review is no longer active", errRepositoryReviewSafeStop)
	}
	reason, detail := active.pauseReason, active.pauseDetail
	c.mu.Unlock()
	if reason != "" && reason != repoaudit.RepositoryReviewPauseGuardExpression {
		return fmt.Errorf("%w: %s", errRepositoryReviewSafeStop, detail)
	}
	return nil
}

func (c *repositoryReviewController) observeRepositoryReviewTask(
	id string,
	runID string,
	activity workflows.ManagedChildActivity,
) error {
	if activity.Phase == workflows.ManagedChildCompleted {
		c.mu.Lock()
		if active := c.active[id]; active != nil && active.runID == runID {
			delete(active.reservations, activity.Index)
		}
		c.mu.Unlock()
		return nil
	}
	if activity.Phase != workflows.ManagedChildStarted {
		return nil
	}
	activeSnapshot, ok := c.activeRunSnapshot(id, runID)
	if !ok {
		return fmt.Errorf("%w: repository review is no longer active", errRepositoryReviewSafeStop)
	}
	if activeSnapshot.pauseReason != "" {
		return fmt.Errorf("%w: %s", errRepositoryReviewSafeStop, activeSnapshot.pauseDetail)
	}
	guardMu := activeSnapshot.guardMu
	if guardMu == nil {
		c.mu.Lock()
		if active := c.active[id]; active != nil && active.runID == runID {
			if active.guardMu == nil {
				active.guardMu = &sync.Mutex{}
			}
			guardMu = active.guardMu
		}
		c.mu.Unlock()
	}
	if guardMu == nil {
		return fmt.Errorf("%w: task admission guard is unavailable", errRepositoryReviewSafeStop)
	}
	guardMu.Lock()
	defer guardMu.Unlock()
	activeSnapshot, ok = c.activeRunSnapshot(id, runID)
	if !ok || activeSnapshot.pauseReason != "" {
		detail := "repository review stopped before task admission"
		if ok && activeSnapshot.pauseDetail != "" {
			detail = activeSnapshot.pauseDetail
		}
		return fmt.Errorf("%w: %s", errRepositoryReviewSafeStop, detail)
	}
	automation, found, err := activeSnapshot.store.GetAutomation(c.ctx, id)
	if err != nil || !found || automation.ActiveRunID != runID {
		if err == nil {
			err = errors.New("repository review automation is unavailable at task admission")
		}
		return errors.Join(errRepositoryReviewSafeStop, err)
	}
	expression := strings.TrimSpace(automation.BudgetPolicy.GuardExpression)
	if expression == "" {
		return nil
	}

	reservation := repositoryReviewGuardReservation(automation, activity)

	environment := repoaudit.RepositoryReviewGuardEnvironment{
		SpentTokens:   automation.Usage,
		SpendTotalUSD: automation.EstimatedCostUSD,
		CostKnown:     repositoryReviewAutomationPriceKnown(automation),
	}
	c.mu.Lock()
	if active := c.active[id]; active != nil && active.runID == runID {
		for _, pending := range active.reservations {
			addRepositoryReviewGuardReservation(&environment, pending)
		}
	}
	c.mu.Unlock()
	addRepositoryReviewGuardReservation(&environment, reservation)

	if repoaudit.RepositoryReviewGuardUsesAccountLimits(expression) {
		snapshots, known, probeErr := c.repositoryReviewGuardAccountLimits(
			c.ctx, activeSnapshot.config, automation,
		)
		environment.AccountLimitSnapshots = snapshots
		environment.AccountLimitsKnown = known && probeErr == nil
		if len(snapshots) > 0 {
			_, _ = c.updateLatest(
				context.Background(), activeSnapshot.store, id,
				func(candidate *repoaudit.RepositoryReviewAutomation) error {
					if candidate.ActiveRunID == runID {
						candidate.AccountLimitSnapshots = snapshots
					}
					return nil
				},
			)
		}
	}

	allowed, evaluateErr := repoaudit.EvaluateRepositoryReviewGuardExpression(expression, environment)
	if evaluateErr != nil || !allowed {
		detail := "Task admission guard evaluated to false."
		if evaluateErr != nil {
			detail = "Task admission guard could not produce true: " + evaluateErr.Error()
		}
		detail = repositoryReviewBoundedDetail(detail)
		c.requestSafeStop(id, runID, repoaudit.RepositoryReviewPauseGuardExpression, detail)
		return fmt.Errorf("%w: %s", errRepositoryReviewSafeStop, detail)
	}

	c.mu.Lock()
	active := c.active[id]
	if active == nil || active.runID != runID || active.pauseReason != "" {
		detail := "repository review stopped before task dispatch"
		if active != nil && active.pauseDetail != "" {
			detail = active.pauseDetail
		}
		c.mu.Unlock()
		return fmt.Errorf("%w: %s", errRepositoryReviewSafeStop, detail)
	}
	active.reservations[activity.Index] = reservation
	c.mu.Unlock()
	return nil
}

func repositoryReviewGuardReservation(
	automation repoaudit.RepositoryReviewAutomation,
	activity workflows.ManagedChildActivity,
) repositoryReviewTaskReservation {
	reservation := repositoryReviewTaskReservation{
		PromptTokens:     int64(max(0, activity.EstimatedPromptTokens)),
		CompletionTokens: int64(max(0, activity.EstimatedOutputTokens)),
	}
	reservation.TotalTokens = reservation.PromptTokens + reservation.CompletionTokens
	alias := strings.TrimSpace(activity.ModelAlias)
	price, found := automation.ModelPrices[alias]
	if !found {
		if models := repositoryReviewExecutionModels(automation); len(models) == 1 {
			price, found = automation.ModelPrices[models[0]]
		}
	}
	reservation.CostKnown = found && (price.InputPricePer1M > 0 || price.OutputPricePer1M > 0)
	if reservation.CostKnown {
		reservation.CostUSD = (float64(reservation.PromptTokens)*price.InputPricePer1M +
			float64(reservation.CompletionTokens)*price.OutputPricePer1M) / 1_000_000
	}
	return reservation
}

func addRepositoryReviewGuardReservation(
	environment *repoaudit.RepositoryReviewGuardEnvironment,
	reservation repositoryReviewTaskReservation,
) {
	if environment == nil {
		return
	}
	environment.SpentTokens.PromptTokens += reservation.PromptTokens
	environment.SpentTokens.CompletionTokens += reservation.CompletionTokens
	environment.SpentTokens.TotalTokens += reservation.TotalTokens
	environment.SpendTotalUSD += reservation.CostUSD
	environment.CostKnown = environment.CostKnown && reservation.CostKnown
}

func repositoryReviewAutomationPriceKnown(automation repoaudit.RepositoryReviewAutomation) bool {
	models := repositoryReviewExecutionModels(automation)
	if len(models) == 0 {
		return false
	}
	for _, model := range models {
		price, ok := automation.ModelPrices[model]
		if !ok || price.InputPricePer1M <= 0 && price.OutputPricePer1M <= 0 {
			return false
		}
	}
	return true
}

func repositoryReviewAnySlice(value any) []any {
	switch values := value.(type) {
	case []any:
		return values
	case []map[string]any:
		out := make([]any, len(values))
		for index := range values {
			out[index] = values[index]
		}
		return out
	default:
		return nil
	}
}

func (c *repositoryReviewController) requestSafeStop(
	id, runID string,
	reason repoaudit.RepositoryReviewPauseReason,
	detail string,
) {
	c.mu.Lock()
	active := c.active[id]
	if active != nil && active.runID == runID && active.pauseReason == "" {
		active.pauseReason = reason
		active.pauseDetail = repositoryReviewBoundedDetail(detail)
	}
	c.mu.Unlock()
	activeSnapshot, ok := c.activeRunSnapshot(id, runID)
	if !ok {
		return
	}
	store := activeSnapshot.store
	_, _ = c.updateLatest(context.Background(), store, id, func(candidate *repoaudit.RepositoryReviewAutomation) error {
		if candidate.ActiveRunID == runID && candidate.Status == repoaudit.RepositoryReviewAutomationRunning {
			candidate.Status = repoaudit.RepositoryReviewAutomationStopping
			candidate.RequestedPauseReason = reason
			candidate.RequestedPauseDetail = repositoryReviewBoundedDetail(detail)
			candidate.Progress.Stage = "stopping after current batch"
		}
		return nil
	})
}

func (c *repositoryReviewController) monitorWorkflowProgress(
	ctx context.Context,
	automationStore repoaudit.Store,
	workflowStore *workflows.FileRunStore,
	automationID string,
	runID string,
) {
	ticker := time.NewTicker(c.progressEvery)
	defer ticker.Stop()
	lastStage := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run, err := workflowStore.GetRun(ctx, runID)
			if err != nil || run == nil {
				continue
			}
			stage := repositoryReviewWorkflowStage(run)
			if stage == "" || stage == lastStage {
				continue
			}
			lastStage = stage
			_, _ = c.updateLatest(
				context.Background(),
				automationStore,
				automationID,
				func(candidate *repoaudit.RepositoryReviewAutomation) error {
					if candidate.ActiveRunID == runID &&
						candidate.Status == repoaudit.RepositoryReviewAutomationRunning {
						candidate.Progress.Stage = stage
					}
					return nil
				},
			)
		}
	}
}

func repositoryReviewWorkflowStage(run *workflows.Run) string {
	if run == nil {
		return ""
	}
	labels := map[string]string{
		"checkout":           "Acquiring repository snapshot",
		"inventory":          "Inventorying tracked files",
		"scope_catalog":      "Classifying target code",
		"release_structure":  "Releasing checkout before scope planning",
		"plan_scope":         "AI planning target scope",
		"scope_checkout":     "Reacquiring the exact commit",
		"scope_inventory":    "Validating target inventory",
		"full_scope_catalog": "Rebuilding complete target scope",
		"scope":              "Validating AI target scope",
		"scope_files":        "Binding exact target files",
		"plan":               "Planning changed files",
		"freeze":             "Freezing immutable evidence",
		"release":            "Releasing checkout",
		"review":             "Reviewing bounded file batch",
		"record":             "Checkpointing findings",
		"result":             "Finalizing batch",
	}
	order := []string{
		"checkout", "inventory", "scope_catalog", "release_structure", "plan_scope",
		"scope_checkout", "scope_inventory", "full_scope_catalog", "scope", "scope_files",
		"plan", "freeze", "release", "review", "record", "result",
	}
	for index := len(order) - 1; index >= 0; index-- {
		step := repositoryReviewRunStep(run, order[index])
		if step.Status == workflows.RunStatusRunning {
			return labels[order[index]]
		}
	}
	for index := len(order) - 1; index >= 0; index-- {
		step := repositoryReviewRunStep(run, order[index])
		if step.Status == workflows.RunStatusSucceeded {
			return labels[order[index]]
		}
	}
	return "queued"
}

func repositoryReviewRunStep(run *workflows.Run, stepID string) workflows.StepExecution {
	if run == nil {
		return workflows.StepExecution{}
	}
	if step, exists := run.Steps["find_bugs/"+stepID]; exists {
		return step
	}
	if step, exists := run.Steps[stepID]; exists {
		return step
	}
	return workflows.StepExecution{}
}

func repositoryReviewRunCheckpointed(
	run *workflows.Run,
	result *workflows.RunResult,
) bool {
	if run == nil || result == nil || result.Status != workflows.RunStatusSucceeded {
		return false
	}
	record := repositoryReviewRunStep(run, "record")
	if record.Status == workflows.RunStatusSucceeded && strings.TrimSpace(record.Error) == "" {
		if recordedRun, ok := record.Outputs["run"].(map[string]any); ok && len(recordedRun) > 0 {
			return true
		}
	}
	plan := repositoryReviewRunStep(run, "plan")
	resultStep := repositoryReviewRunStep(run, "result")
	pending := repositoryReviewInt(plan.Outputs["pendingCount"])
	if pending == 0 {
		pending = repositoryReviewInt(plan.Outputs["pending_count"])
	}
	return pending == 0 && resultStep.Status == workflows.RunStatusSucceeded &&
		strings.TrimSpace(resultStep.Error) == "" &&
		repositoryReviewInt(result.Outputs["remainingFiles"]) == 0
}

func (c *repositoryReviewController) finishAutomationRun(
	id, runID string,
	result *workflows.RunResult,
	runErr error,
	checkpointed bool,
) {
	activeSnapshot, activeFound := c.activeRunSnapshot(id, runID)
	if !activeFound {
		c.removeActive(id, runID)
		return
	}
	store := activeSnapshot.store
	c.mu.Lock()
	active := c.active[id]
	pauseReason := repoaudit.RepositoryReviewPauseReason("")
	pauseDetail := ""
	if active != nil && active.runID == runID {
		pauseReason, pauseDetail = active.pauseReason, active.pauseDetail
	}
	c.mu.Unlock()
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, workflows.ErrRunCanceled) {
		if pauseReason == "" {
			pauseReason = repoaudit.RepositoryReviewPauseServiceRestart
			pauseDetail = "The launcher stopped while this batch was running. Resume continues from durable checkpoints."
		}
	}
	current, currentFound, _ := store.GetAutomation(context.Background(), id)
	outcome := repositoryReviewOutcome{}
	if currentFound {
		outcome = loadRepositoryReviewOutcome(store, current)
		if pauseReason == "" && current.RequestedPauseReason != "" {
			pauseReason = current.RequestedPauseReason
			pauseDetail = current.RequestedPauseDetail
		}
	}

	updated, err := c.updateLatest(
		context.Background(),
		store,
		id,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			if candidate.ActiveRunID != runID {
				return nil
			}
			candidate.ActiveRunID = ""
			candidate.RequestedPauseReason = ""
			candidate.RequestedPauseDetail = ""
			candidate.Progress.Stage = ""
			if checkpointed {
				candidate.Progress.CompletedBatches++
				candidate.Progress.TotalBatches = max(
					candidate.Progress.TotalBatches,
					candidate.Progress.CompletedBatches,
				)
				applyRepositoryReviewRunProgress(candidate, result)
				applyRepositoryReviewOutcome(candidate, outcome)
			}
			if runErr == nil && result != nil && result.Status == workflows.RunStatusSucceeded && !checkpointed {
				candidate.Status = repoaudit.RepositoryReviewAutomationFailed
				candidate.PauseReason = repoaudit.RepositoryReviewPauseRunFailed
				candidate.PauseDetail = "The workflow ended without a verified durable repository review checkpoint."
				candidate.Progress.Stage = "failed"
				return nil
			}
			if pauseReason == repoaudit.RepositoryReviewPauseRunFailed {
				candidate.Status = repoaudit.RepositoryReviewAutomationFailed
				candidate.PauseReason = repoaudit.RepositoryReviewPauseRunFailed
				candidate.PauseDetail = repositoryReviewBoundedDetail(pauseDetail)
				candidate.Progress.Stage = "failed"
				return nil
			}

			finalPauseReason, finalPauseDetail, shouldPause := repositoryReviewFinalPause(
				pauseReason, pauseDetail, candidate.Status,
			)
			if shouldPause {
				candidate.Status = repoaudit.RepositoryReviewAutomationPaused
				candidate.PauseReason = finalPauseReason
				candidate.PauseDetail = finalPauseDetail
				candidate.Progress.Stage = "paused"
				return nil
			}
			if runErr != nil || result == nil || result.Status == workflows.RunStatusFailed {
				candidate.Status = repoaudit.RepositoryReviewAutomationFailed
				candidate.PauseReason = repoaudit.RepositoryReviewPauseRunFailed
				candidate.PauseDetail = repositoryReviewRunError(runErr, result)
				candidate.Progress.Stage = "failed"
				return nil //nolint:nilerr // Persist the run failure as durable automation state.
			}
			if candidate.Progress.RemainingFiles <= 0 {
				candidate.Status = repoaudit.RepositoryReviewAutomationCompleted
				candidate.PauseReason = ""
				candidate.PauseDetail = ""
				candidate.Progress.Stage = "complete"
				candidate.CompletedAt = c.clock()
				return nil
			}
			if candidate.AutoContinue {
				candidate.Status = repoaudit.RepositoryReviewAutomationIdle
				candidate.PauseReason = ""
				candidate.PauseDetail = ""
				candidate.Progress.Stage = "next batch queued"
				return nil
			}
			candidate.Status = repoaudit.RepositoryReviewAutomationPaused
			candidate.PauseReason = repoaudit.RepositoryReviewPauseManual
			candidate.PauseDetail = "The bounded batch completed. Resume to review the remaining files."
			candidate.Progress.Stage = "paused"
			return nil
		},
	)
	c.removeActive(id, runID)
	if err != nil {
		logger.WarnCF("repository-review", "Failed to finalize repository review automation", map[string]any{
			"automation_id": id, "run_id": runID, "error": err.Error(),
		})
		return
	}
	if updated.Status == repoaudit.RepositoryReviewAutomationIdle && updated.AutoContinue {
		_, startErr := c.startAutomation(context.Background(), id, updated.Version, false, "start")
		if startErr != nil {
			logger.WarnCF("repository-review", "Failed to start next repository review batch", map[string]any{
				"automation_id": id, "error": startErr.Error(),
			})
		}
	}
}

func repositoryReviewFinalPause(
	reason repoaudit.RepositoryReviewPauseReason,
	detail string,
	status repoaudit.RepositoryReviewAutomationStatus,
) (repoaudit.RepositoryReviewPauseReason, string, bool) {
	if reason == "" && status != repoaudit.RepositoryReviewAutomationStopping {
		return "", "", false
	}
	if reason == "" {
		return repoaudit.RepositoryReviewPauseManual, "Paused after the current safe checkpoint.", true
	}
	return reason, detail, true
}

func (c *repositoryReviewController) removeActive(id, runID string) {
	c.mu.Lock()
	if active := c.active[id]; active != nil && active.runID == runID {
		delete(c.active, id)
	}
	c.mu.Unlock()
}

func (c *repositoryReviewController) activeRunSnapshot(
	id string,
	runID string,
) (repositoryReviewActiveRun, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	active := c.active[id]
	if active == nil || runID != "" && active.runID != runID {
		return repositoryReviewActiveRun{}, false
	}
	return *active, true
}

func applyRepositoryReviewRunProgress(
	automation *repoaudit.RepositoryReviewAutomation,
	result *workflows.RunResult,
) {
	if automation == nil || result == nil {
		return
	}
	if rawPlan, exists := result.Outputs["scopePlan"]; exists && rawPlan != nil {
		if encoded, err := json.Marshal(rawPlan); err == nil {
			var scopePlan repoaudit.RepositoryReviewScopePlan
			if json.Unmarshal(encoded, &scopePlan) == nil {
				automation.ScopePlan = scopePlan
			}
		}
	}
	remaining := repositoryReviewInt(result.Outputs["remainingFiles"])
	if remaining == 0 {
		remaining = repositoryReviewInt(result.Outputs["remaining_files"])
	}
	if remaining >= 0 {
		automation.Progress.RemainingFiles = remaining
	}
	reviewed := repositoryReviewInt(result.Outputs["reviewedFiles"])
	if reviewed == 0 {
		reviewed = repositoryReviewInt(result.Outputs["reviewed_files"])
	}
	if reviewed > 0 {
		automation.Progress.ReviewedFiles += reviewed
	}
	if automation.MaxFilesPerRun > 0 {
		remainingBatches := int(
			math.Ceil(float64(automation.Progress.RemainingFiles) / float64(automation.MaxFilesPerRun)),
		)
		automation.Progress.TotalBatches = max(
			automation.Progress.TotalBatches,
			automation.Progress.CompletedBatches+remainingBatches,
		)
	}
}

type repositoryReviewOutcome struct {
	found            bool
	reviewedFiles    int
	unsupportedFiles int
	findings         int
	modelFindings    map[string]int
	modelPaths       map[string][]string
}

func loadRepositoryReviewOutcome(
	store repoaudit.Store,
	automation repoaudit.RepositoryReviewAutomation,
) repositoryReviewOutcome {
	state, found, err := store.Get(automation.Repository)
	if err != nil || !found {
		return repositoryReviewOutcome{}
	}
	configuredRuns := make(map[string]struct{}, len(automation.RunIDs))
	for _, runID := range automation.RunIDs {
		configuredRuns[runID] = struct{}{}
	}
	campaignRuns := make(map[string]struct{})
	findingIDs := make(map[string]struct{})
	unsupportedPaths := make(map[string]struct{})
	for _, run := range state.Runs {
		if _, selected := configuredRuns[run.ID]; !selected ||
			!automation.StartedAt.IsZero() && run.CompletedAt.Before(automation.StartedAt) {
			continue
		}
		campaignRuns[run.ID] = struct{}{}
		for _, findingID := range run.FindingIDs {
			findingIDs[findingID] = struct{}{}
		}
		for _, path := range run.UnsupportedPaths {
			unsupportedPaths[path] = struct{}{}
		}
	}
	if len(campaignRuns) == 0 {
		return repositoryReviewOutcome{}
	}
	reviewedPaths := make(map[string]struct{})
	for path, file := range state.Files {
		if _, selected := campaignRuns[file.RunID]; selected {
			reviewedPaths[path] = struct{}{}
		}
	}
	selectedContexts := make(map[string]repoaudit.FindingContext)
	for _, findingContext := range state.Contexts {
		if _, selected := campaignRuns[findingContext.RunID]; selected {
			selectedContexts[findingContext.ID] = findingContext
		}
	}
	outcome := repositoryReviewOutcome{
		found: true, reviewedFiles: len(reviewedPaths),
		unsupportedFiles: len(unsupportedPaths), findings: len(findingIDs),
		modelFindings: make(map[string]int), modelPaths: make(map[string][]string),
	}
	for _, alias := range automation.ReviewerModels {
		modelFindingIDs := make(map[string]struct{})
		files := make(map[string]struct{})
		for _, finding := range state.Findings {
			if _, selected := findingIDs[finding.ID]; !selected {
				continue
			}
			for _, observation := range finding.Observations {
				if contextRecord, selected := selectedContexts[observation.ContextID]; selected &&
					(observation.Model == alias || contextRecord.Reviewer == alias) {
					modelFindingIDs[finding.ID] = struct{}{}
				}
			}
		}
		for _, findingContext := range selectedContexts {
			if findingContext.Model != alias && findingContext.Reviewer != alias {
				continue
			}
			for _, file := range findingContext.Files {
				files[file.Path] = struct{}{}
			}
		}
		outcome.modelFindings[alias] = len(modelFindingIDs)
		for path := range files {
			outcome.modelPaths[alias] = append(outcome.modelPaths[alias], path)
		}
	}
	return outcome
}

func applyRepositoryReviewOutcome(
	automation *repoaudit.RepositoryReviewAutomation,
	outcome repositoryReviewOutcome,
) {
	if automation == nil || !outcome.found {
		return
	}
	automation.Progress.ReviewedFiles = max(automation.Progress.ReviewedFiles, outcome.reviewedFiles)
	automation.Progress.UnsupportedFiles = max(automation.Progress.UnsupportedFiles, outcome.unsupportedFiles)
	automation.Progress.Findings = max(automation.Progress.Findings, outcome.findings)
	for _, alias := range automation.ReviewerModels {
		stats := automation.ModelStats[alias]
		stats.Findings = max(stats.Findings, outcome.modelFindings[alias])
		automation.ModelStats[alias] = stats
		addRepositoryReviewModelPaths(automation, alias, outcome.modelPaths[alias])
	}
}

func addRepositoryReviewModelPaths(
	automation *repoaudit.RepositoryReviewAutomation,
	alias string,
	paths []string,
) {
	if automation == nil || alias == "" || len(paths) == 0 {
		return
	}
	const sketchBytes = 8 << 10
	raw := make([]byte, sketchBytes)
	if encoded := automation.ModelCoverageSketches[alias]; encoded != "" {
		if decoded, err := base64.RawStdEncoding.DecodeString(encoded); err == nil && len(decoded) == sketchBytes {
			copy(raw, decoded)
		}
	}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		digest := sha256.Sum256([]byte(path))
		bucket := int(digest[0])<<8 | int(digest[1])
		raw[bucket/8] |= byte(1 << uint(bucket%8))
	}
	if automation.ModelCoverageSketches == nil {
		automation.ModelCoverageSketches = make(map[string]string)
	}
	automation.ModelCoverageSketches[alias] = base64.RawStdEncoding.EncodeToString(raw)
	setBits := 0
	for _, value := range raw {
		for current := value; current != 0; current &= current - 1 {
			setBits++
		}
	}
	totalBits := float64(sketchBytes * 8)
	zeroBits := totalBits - float64(setBits)
	estimate := 100_000
	if zeroBits > 0 {
		estimate = min(100_000, int(math.Round(-totalBits*math.Log(zeroBits/totalBits))))
	}
	stats := automation.ModelStats[alias]
	stats.ReviewedFiles = max(stats.ReviewedFiles, estimate)
	automation.ModelStats[alias] = stats
}

func repositoryReviewRunError(runErr error, result *workflows.RunResult) string {
	if runErr != nil {
		return repositoryReviewBoundedDetail(runErr.Error())
	}
	if result != nil && strings.TrimSpace(result.Error) != "" {
		return repositoryReviewBoundedDetail(result.Error)
	}
	return "The repository review batch failed."
}

func repositoryReviewBoundedDetail(value string) string {
	const maximumBytes = 4096
	value = strings.TrimSpace(value)
	if len(value) <= maximumBytes {
		return value
	}
	end := maximumBytes - len("...")
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + "..."
}

func repositoryReviewInt(value any) int {
	switch number := value.(type) {
	case int:
		return number
	case int64:
		return int(number)
	case float64:
		return int(number)
	case float32:
		return int(number)
	case string:
		var parsed int
		_, _ = fmt.Sscanf(strings.TrimSpace(number), "%d", &parsed)
		return parsed
	default:
		return 0
	}
}

func (c *repositoryReviewController) updateLatest(
	ctx context.Context,
	store repoaudit.Store,
	id string,
	mutate func(*repoaudit.RepositoryReviewAutomation) error,
) (repoaudit.RepositoryReviewAutomation, error) {
	var lastErr error
	for attempt := 0; attempt < 12; attempt++ {
		current, found, err := store.GetAutomation(ctx, id)
		if err != nil {
			return repoaudit.RepositoryReviewAutomation{}, err
		}
		if !found {
			return repoaudit.RepositoryReviewAutomation{}, errors.New("repository review automation not found")
		}
		updated, err := store.UpdateAutomation(ctx, id, current.Version, mutate)
		if !errors.Is(err, repoaudit.ErrConflict) {
			return updated, err
		}
		lastErr = err
	}
	return repoaudit.RepositoryReviewAutomation{}, lastErr
}

func (c *repositoryReviewController) clock() time.Time {
	if c == nil || c.now == nil {
		return time.Now().UTC()
	}
	return c.now().UTC()
}

func (c *repositoryReviewController) repositoryReviewGuardAccountLimits(
	ctx context.Context,
	cfg *config.Config,
	automation repoaudit.RepositoryReviewAutomation,
) ([]repoaudit.RepositoryReviewAccountLimitSnapshot, bool, error) {
	probe := c.probe
	if probe == nil {
		probe = loadCodexAccountLimits
	}
	probeCtx, cancel := context.WithTimeout(ctx, repositoryReviewQuotaProbeTimeout)
	defer cancel()
	response, probeErr := probe(probeCtx)
	now := c.clock()
	refs := repositoryReviewAccountRefsForSelection(cfg, automation.AccountRef)
	if len(refs) == 0 {
		return nil, false, errors.New("selected review account is unavailable")
	}
	byID := make(map[string]codexAccountLimitAccount, len(response.Accounts))
	for _, account := range response.Accounts {
		byID[strings.ToLower(strings.TrimSpace(account.ID))] = account
	}
	snapshots := make([]repoaudit.RepositoryReviewAccountLimitSnapshot, 0)
	complete := probeErr == nil && strings.TrimSpace(response.Error) == ""
	for _, ref := range refs {
		telemetryIDs := repositoryReviewTelemetryIDsForAccountRef(cfg, ref)
		var telemetry codexAccountLimitAccount
		matched := false
		for _, telemetryID := range telemetryIDs {
			if candidate, exists := byID[strings.ToLower(strings.TrimSpace(telemetryID))]; exists {
				telemetry, matched = candidate, true
				break
			}
		}
		if !matched || len(telemetry.Entries) == 0 {
			complete = false
			detail := "account limit telemetry is unavailable"
			if matched {
				detail = firstRepositoryReviewLimitDetail(
					telemetry.LimitsError, telemetry.LimitsStatus, telemetry.CredentialStatus,
				)
			}
			snapshots = append(snapshots, repoaudit.RepositoryReviewAccountLimitSnapshot{
				AccountID: ref, Window: "unknown", CheckedAt: now, Detail: detail,
			})
			continue
		}
		for _, entry := range telemetry.Entries {
			window := normalizeRepositoryReviewWindow(entry.Window)
			snapshot := repoaudit.RepositoryReviewAccountLimitSnapshot{
				AccountID: ref, Name: strings.TrimSpace(entry.Name), Window: window,
				CheckedAt: now, Detail: strings.TrimSpace(entry.Status),
			}
			status := strings.ToLower(strings.TrimSpace(entry.Status))
			exhausted := status == "limit_reached" || status == "exhausted" ||
				status == "blocked" || status == "quota_exhausted"
			if exhausted {
				remaining := 0.0
				snapshot.RemainingPercent = &remaining
			} else if entry.UsedPercent != nil {
				remaining := math.Max(0, math.Min(100, 100-float64(*entry.UsedPercent)))
				snapshot.RemainingPercent = &remaining
			} else {
				complete = false
			}
			if reset, ok := parseRepositoryReviewReset(entry.RefreshesAt); ok {
				snapshot.ResetsAt = reset
			}
			snapshots = append(snapshots, snapshot)
		}
	}
	if probeErr != nil {
		return snapshots, false, probeErr
	}
	if detail := strings.TrimSpace(response.Error); detail != "" {
		return snapshots, false, errors.New(detail)
	}
	return snapshots, complete, nil
}

func repositoryReviewTelemetryIDsForAccountRef(cfg *config.Config, accountRef string) []string {
	accountRef = strings.TrimSpace(accountRef)
	ids := make([]string, 0, 2)
	add := func(value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || slices.Contains(ids, value) {
			return
		}
		ids = append(ids, value)
	}
	if credentialID, ok := config.AccountRouterCredentialAccountID(accountRef); ok {
		add(credentialID)
		return ids
	}
	if cfg != nil {
		if account, err := cfg.GetEnabledModelConfig(accountRef); err == nil && account != nil {
			add(account.CredentialID)
		}
	}
	add(accountRef)
	return ids
}

func firstRepositoryReviewLimitDetail(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return "unavailable"
}

func normalizeRepositoryReviewWindow(window string) string {
	window = strings.ToLower(strings.TrimSpace(window))
	switch {
	case window == "":
		return "unknown"
	case strings.Contains(window, "week") || window == "7d":
		return "weekly"
	case strings.Contains(window, "day") || window == "24h":
		return "daily"
	default:
		return window
	}
}

func parseRepositoryReviewReset(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" || value == "-" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC(), true
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05 MST", value, time.Local)
	return parsed.UTC(), err == nil
}

func (c *repositoryReviewController) monitor() {
	defer c.wg.Done()
	c.reconcile()
	ticker := time.NewTicker(c.monitorEvery)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.reconcile()
		}
	}
}

func (c *repositoryReviewController) reconcile() {
	store, cfg := c.leasedStore, c.leasedConfig
	if cfg == nil {
		return
	}
	automations, err := store.ListAutomations(c.ctx)
	if err != nil {
		return
	}
	for _, automation := range automations {
		if c.ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		_, active := c.active[automation.ID]
		c.mu.Unlock()
		if (automation.Status == repoaudit.RepositoryReviewAutomationRunning ||
			automation.Status == repoaudit.RepositoryReviewAutomationStopping) && !active {
			if strings.TrimSpace(cfg.WorkspacePath()) != "" {
				workflowStore := workflows.NewFileRunStore(cfg.WorkspacePath())
				_, _ = workflowStore.CancelRun(context.Background(), automation.ActiveRunID, "launcher restarted")
			}
			requestedReason := automation.RequestedPauseReason
			requestedDetail := automation.RequestedPauseDetail
			if requestedReason == "" {
				requestedReason = repoaudit.RepositoryReviewPauseServiceRestart
				requestedDetail = "The launcher restarted. Resume continues from durable review checkpoints."
			}
			_, _ = store.UpdateAutomation(
				context.Background(),
				automation.ID,
				automation.Version,
				func(candidate *repoaudit.RepositoryReviewAutomation) error {
					candidate.Status = repoaudit.RepositoryReviewAutomationPaused
					candidate.ActiveRunID = ""
					candidate.PauseReason = requestedReason
					candidate.PauseDetail = requestedDetail
					candidate.RequestedPauseReason = ""
					candidate.RequestedPauseDetail = ""
					candidate.Progress.Stage = "paused"
					return nil
				},
			)
		}
	}
}
