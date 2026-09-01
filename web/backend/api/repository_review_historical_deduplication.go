package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

var errRepositoryReviewHistoricalCampaignRecovery = errors.New(
	"exact historical campaign recovery is unavailable",
)

var resolveRepositoryReviewHistoricalCampaignProfile = resolveRepositoryReviewCampaignProfile

var prepareRepositoryReviewHistoricalCampaignBackfill = prepareRepositoryReviewLegacyCampaignBackfill

var updateRepositoryReviewHistoricalAutomation = func(
	ctx context.Context,
	store repoaudit.Store,
	installed repoaudit.RepositoryReviewAutomation,
	prepared repositoryReviewLegacyCampaignBackfill,
	reconciled repoaudit.RepositoryState,
) (repoaudit.RepositoryReviewAutomation, error) {
	return store.UpdateAutomation(
		ctx, installed.ID, installed.Version,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			return finalizeRepositoryReviewLegacyCampaign(
				candidate, prepared.Request.Coverage.ID, reconciled,
			)
		},
	)
}

var admitNextHistoricalDeduplicationBatch = func(
	store repoaudit.Store,
	repository string,
) (repoaudit.RepositoryState, repoaudit.HistoricalDeduplicationAdmission, error) {
	return store.AdmitNextHistoricalDeduplicationBatch(repository)
}

var historicalDeduplicationRepositoryMergeGroups = repoaudit.HistoricalDeduplicationRepositoryMergeGroups

var acquireHistoricalDeduplicationMerge = func(
	store repoaudit.Store,
	repository, leaseID string,
	groups []repoaudit.HistoricalDeduplicationMergeGroup,
) (repoaudit.RepositoryState, repoaudit.HistoricalDeduplicationReplay, bool, error) {
	return store.AcquireHistoricalDeduplicationMerge(repository, leaseID, groups)
}

var completeHistoricalDeduplicationMerge = func(
	store repoaudit.Store,
	repository, leaseID string,
) (repoaudit.RepositoryState, repoaudit.HistoricalDeduplicationReplay, error) {
	return store.CompleteHistoricalDeduplicationMerge(repository, leaseID)
}

var failHistoricalDeduplicationReplay = func(
	store repoaudit.Store,
	repository, leaseID string,
) (repoaudit.RepositoryState, repoaudit.HistoricalDeduplicationReplay, error) {
	return store.FailHistoricalDeduplicationReplay(repository, leaseID)
}

var recoverHistoricalDeduplicationMerge = func(
	store repoaudit.Store,
	repository, leaseID string,
) (repoaudit.RepositoryState, repoaudit.HistoricalDeduplicationReplay, error) {
	return store.RecoverHistoricalDeduplicationMerge(repository, leaseID)
}

var resumeHistoricalDeduplicationReplay = func(
	store repoaudit.Store,
	repository string,
	snapshot repoaudit.HistoricalDeduplicationProfileSnapshot,
	dependencies []repoaudit.HistoricalDeduplicationDependency,
) (repoaudit.RepositoryState, repoaudit.HistoricalDeduplicationReplay, error) {
	return store.ResumeHistoricalDeduplicationReplay(repository, snapshot, dependencies)
}

var restartHistoricalDeduplicationReplay = func(
	store repoaudit.Store,
	repository string,
	request repoaudit.HistoricalDeduplicationRestartRequest,
) (repoaudit.RepositoryState, repoaudit.HistoricalDeduplicationReplay, error) {
	return store.RestartHistoricalDeduplicationReplay(repository, request)
}

type repositoryReviewHistoricalRetryPlan struct {
	Snapshot         repoaudit.HistoricalDeduplicationProfileSnapshot
	Dependencies     []repoaudit.HistoricalDeduplicationDependency
	PreparedRecovery repositoryReviewLegacyCampaignBackfill
	NeedsRecovery    bool
}

func (h *Handler) handleGetRepositoryReviewHistoricalDeduplication(
	w http.ResponseWriter,
	r *http.Request,
) {
	offset, limit, pageErr := repositoryReviewRawPage(r)
	if pageErr != nil {
		writeRepositoryReviewError(w, pageErr)
		return
	}
	ledger, err := h.repositoryReviewAutomationLedger(r.Context(), r.PathValue("automation_id"))
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	rawFindings := make([]repositoryReviewRawFindingSummary, 0)
	for _, raw := range ledger.State.RawFindings {
		if repoaudit.HistoricalDeduplicationRawFinding(raw) {
			rawFindings = append(rawFindings, projectRepositoryReviewRawFindingSummary(raw))
		}
	}
	total := len(rawFindings)
	offset = min(offset, total)
	end := min(total, offset+limit)
	response := map[string]any{
		"automation":               projectRepositoryReviewAutomation(ledger.Automation),
		"repository":               repoaudit.Summarize(ledger.State),
		"historical_deduplication": ledger.State.HistoricalDeduplication,
		"batches":                  repoaudit.HistoricalDeduplicationReplayBatches(ledger.State),
		"raw_findings":             append([]repositoryReviewRawFindingSummary(nil), rawFindings[offset:end]...),
		"offset":                   offset,
		"total":                    total,
	}
	if end < total {
		response["next_offset"] = end
	}
	writeRepositoryReviewJSON(w, http.StatusOK, response)
}

func (h *Handler) handleRetryRepositoryReviewHistoricalDeduplication(
	w http.ResponseWriter,
	r *http.Request,
) {
	if err := validateRepositoryReviewMutation(r); err != nil || r.URL == nil || r.URL.RawQuery != "" {
		writeRepositoryReviewError(w, errors.New("invalid historical deduplication retry request"))
		return
	}
	var request struct{}
	if err := decodeRepositoryReviewRequest(r, &request); err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	ledger, err := h.repositoryReviewAutomationLedger(r.Context(), r.PathValue("automation_id"))
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	if ledger.State.HistoricalDeduplication.Required &&
		ledger.State.HistoricalDeduplication.Status == repoaudit.HistoricalDeduplicationPending &&
		ledger.State.HistoricalDeduplication.Attempts == 0 {
		writeRepositoryReviewAutomationError(w, repoaudit.ErrConflict)
		return
	}
	if !ledger.State.HistoricalDeduplication.Required ||
		ledger.State.HistoricalDeduplication.Status == repoaudit.HistoricalDeduplicationCompleted {
		state, replay, retryErr := ledger.Store.RetryHistoricalDeduplicationReplay(
			ledger.State.Repository,
		)
		if retryErr != nil {
			writeRepositoryReviewAutomationError(w, retryErr)
			return
		}
		writeRepositoryReviewJSON(w, http.StatusAccepted, map[string]any{
			"automation":               projectRepositoryReviewAutomation(ledger.Automation),
			"repository":               repoaudit.Summarize(state),
			"historical_deduplication": replay,
		})
		return
	}
	controller := h.repositoryReviewControllerInstance()
	plan, planErr := h.repositoryReviewHistoricalRetryPlan(r.Context(), controller, ledger)
	if errors.Is(planErr, errRepositoryReviewHistoricalCampaignRecovery) {
		writeRepositoryReviewHistoricalCampaignRecoveryRequired(w)
		return
	}
	if planErr != nil {
		writeRepositoryReviewAutomationError(w, planErr)
		return
	}
	state, replay, err := resumeHistoricalDeduplicationReplay(
		ledger.Store, ledger.State.Repository, plan.Snapshot, plan.Dependencies,
	)
	if errors.Is(err, repoaudit.ErrHistoricalDeduplicationRestartRequired) {
		writeRepositoryReviewHistoricalRestartRequired(w)
		return
	}
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	controller.wakeHistoricalFindingDeduplication()
	writeRepositoryReviewJSON(w, http.StatusAccepted, map[string]any{
		"automation":               projectRepositoryReviewAutomation(ledger.Automation),
		"repository":               repoaudit.Summarize(state),
		"historical_deduplication": replay,
	})
}

func (h *Handler) handleRestartRepositoryReviewHistoricalDeduplication(
	w http.ResponseWriter,
	r *http.Request,
) {
	if err := validateRepositoryReviewMutation(r); err != nil || r.URL == nil || r.URL.RawQuery != "" {
		writeRepositoryReviewError(w, errors.New("invalid historical deduplication restart request"))
		return
	}
	var request struct {
		Confirmed bool `json:"confirmed"`
	}
	if err := decodeRepositoryReviewRequest(r, &request); err != nil {
		writeRepositoryReviewError(w, err)
		return
	}
	if !request.Confirmed {
		writeRepositoryReviewError(w, errors.New(
			"confirmed true is required to restart incompatible historical deduplication work",
		))
		return
	}
	ledger, err := h.repositoryReviewAutomationLedger(r.Context(), r.PathValue("automation_id"))
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	replay := ledger.State.HistoricalDeduplication
	if !replay.Required || replay.Status == repoaudit.HistoricalDeduplicationCompleted {
		writeRepositoryReviewAutomationError(w, repoaudit.ErrConflict)
		return
	}
	controller := h.repositoryReviewControllerInstance()
	plan, planErr := h.repositoryReviewHistoricalRetryPlan(r.Context(), controller, ledger)
	if errors.Is(planErr, errRepositoryReviewHistoricalCampaignRecovery) {
		writeRepositoryReviewHistoricalCampaignRecoveryRequired(w)
		return
	}
	if planErr != nil {
		writeRepositoryReviewAutomationError(w, planErr)
		return
	}
	if plan.NeedsRecovery {
		// Exact campaign recovery spans automation and repository CAS writes.
		// Fence the only worker that can start historical model work, then
		// reload and re-plan under that process-wide controller boundary. A
		// running worker keeps the request non-mutating and retryable.
		if controller == nil || !controller.deduplicationMu.TryLock() {
			writeRepositoryReviewAutomationError(
				w, repoaudit.ErrHistoricalDeduplicationNotQuiescent,
			)
			return
		}
		defer controller.deduplicationMu.Unlock()
		ledger, err = h.repositoryReviewAutomationLedger(
			r.Context(), r.PathValue("automation_id"),
		)
		if err != nil {
			writeRepositoryReviewAutomationError(w, err)
			return
		}
		plan, planErr = h.repositoryReviewHistoricalRetryPlan(
			r.Context(), controller, ledger,
		)
		if errors.Is(planErr, errRepositoryReviewHistoricalCampaignRecovery) {
			writeRepositoryReviewHistoricalCampaignRecoveryRequired(w)
			return
		}
		if planErr != nil {
			writeRepositoryReviewAutomationError(w, planErr)
			return
		}
		replay = ledger.State.HistoricalDeduplication
	}
	if replay.Status != repoaudit.HistoricalDeduplicationFailed && plan.NeedsRecovery {
		writeRepositoryReviewAutomationError(w, repoaudit.ErrConflict)
		return
	}
	// Exact campaign recovery is a separate durable CAS sequence. Avoid
	// starting it while the subsequent selective reset is known to be unable
	// to acquire its quiescent boundary. For already-recovered campaigns the
	// store performs this check atomically with the reset; compatible repeated
	// requests remain idempotent even if workers have since started.
	if plan.NeedsRecovery &&
		(!repoaudit.HistoricalDeduplicationQuiescenceForState(ledger.State).Ready() ||
			!repoaudit.HistoricalDeduplicationModelWorkQuiescent(ledger.State)) {
		writeRepositoryReviewAutomationError(
			w, repoaudit.ErrHistoricalDeduplicationNotQuiescent,
		)
		return
	}
	if plan.NeedsRecovery {
		ledger.Automation, ledger.State, err = applyRepositoryReviewHistoricalCampaignRecovery(
			r.Context(), ledger.Store, plan.PreparedRecovery,
		)
		if err != nil {
			writeRepositoryReviewAutomationError(w, err)
			return
		}
	}
	state, restarted, err := restartHistoricalDeduplicationReplay(
		ledger.Store,
		ledger.State.Repository,
		repoaudit.HistoricalDeduplicationRestartRequest{
			ProfileSnapshot: plan.Snapshot,
			Dependencies:    plan.Dependencies,
		},
	)
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	controller.wakeHistoricalFindingDeduplication()
	writeRepositoryReviewJSON(w, http.StatusAccepted, map[string]any{
		"automation":               projectRepositoryReviewAutomation(ledger.Automation),
		"repository":               repoaudit.Summarize(state),
		"historical_deduplication": restarted,
	})
}

func writeRepositoryReviewHistoricalCampaignRecoveryRequired(w http.ResponseWriter) {
	writeRepositoryReviewJSON(w, http.StatusConflict, map[string]string{
		"code":    "historical_deduplication_campaign_recovery_required",
		"message": "Historical deduplication cannot be resumed because the exact retained review campaign could not be recovered. Restore the automation's workflow run history, then resume.",
	})
}

func writeRepositoryReviewHistoricalRestartRequired(w http.ResponseWriter) {
	writeRepositoryReviewJSON(w, http.StatusConflict, map[string]string{
		"code":    "historical_consolidation_restart_required",
		"message": "The historical consolidation profile or campaign identity changed. Confirm a restart to reprocess only the incompatible work; unrelated completed work will be preserved.",
	})
}

func (h *Handler) repositoryReviewHistoricalRetryPlan(
	ctx context.Context,
	controller *repositoryReviewController,
	ledger repositoryReviewAutomationLedger,
) (repositoryReviewHistoricalRetryPlan, error) {
	replay := ledger.State.HistoricalDeduplication
	if !replay.Required || replay.Status == repoaudit.HistoricalDeduplicationCompleted {
		return repositoryReviewHistoricalRetryPlan{Snapshot: replay.ProfileSnapshot}, nil
	}
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return repositoryReviewHistoricalRetryPlan{}, err
	}
	materialized, snapshot, err := repositoryReviewHistoricalProfileSnapshot(
		ctx, controller, ledger.Store, cfg, ledger.Automation,
	)
	if err != nil {
		return repositoryReviewHistoricalRetryPlan{}, err
	}
	plan := repositoryReviewHistoricalRetryPlan{Snapshot: snapshot}
	if repositoryReviewHistoricalCampaignRecovered(materialized, ledger.State) {
		plan.Dependencies, err = repoaudit.HistoricalDeduplicationDependencies(
			ledger.State, "", nil,
		)
		return plan, err
	}
	resolvedProfile, err := resolveRepositoryReviewHistoricalCampaignProfile(
		ctx, h.configPath, cfg, materialized,
	)
	if err != nil {
		return repositoryReviewHistoricalRetryPlan{}, err
	}
	prepared, err := prepareRepositoryReviewHistoricalCampaignRecovery(
		ctx, cfg.WorkspacePath(), materialized, ledger.State, resolvedProfile,
	)
	if err != nil {
		return repositoryReviewHistoricalRetryPlan{}, err
	}
	plan.PreparedRecovery = prepared
	plan.NeedsRecovery = true
	plan.Dependencies, err = repoaudit.HistoricalDeduplicationDependencies(
		ledger.State, prepared.Request.Coverage.ID, prepared.Request.FindingIDs,
	)
	return plan, err
}

func repositoryReviewHistoricalProfileSnapshot(
	ctx context.Context,
	controller *repositoryReviewController,
	store repoaudit.Store,
	cfg *config.Config,
	automation repoaudit.RepositoryReviewAutomation,
) (
	repoaudit.RepositoryReviewAutomation,
	repoaudit.HistoricalDeduplicationProfileSnapshot,
	error,
) {
	if controller == nil || cfg == nil {
		return repoaudit.RepositoryReviewAutomation{},
			repoaudit.HistoricalDeduplicationProfileSnapshot{},
			errors.New("historical deduplication profile resolver is unavailable")
	}
	materialized := automation
	if strings.TrimSpace(automation.ProfileID) != "" {
		profile, found, err := store.GetProfile(ctx, automation.ProfileID)
		if err != nil {
			return repoaudit.RepositoryReviewAutomation{},
				repoaudit.HistoricalDeduplicationProfileSnapshot{}, err
		}
		if !found {
			return repoaudit.RepositoryReviewAutomation{},
				repoaudit.HistoricalDeduplicationProfileSnapshot{},
				errors.New("historical deduplication profile was not found")
		}
		materialized, err = repoaudit.MaterializeRepositoryReviewAutomation(
			profile, automation,
		)
		if err != nil {
			return repoaudit.RepositoryReviewAutomation{},
				repoaudit.HistoricalDeduplicationProfileSnapshot{}, err
		}
	}
	materialized.EffectiveAccountRef = repositoryReviewEffectiveAccountRef(
		cfg, materialized.AccountRef,
	)
	snapshot, err := controller.repositoryReviewDeduplicationSnapshot(materialized)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{},
			repoaudit.HistoricalDeduplicationProfileSnapshot{}, err
	}
	return materialized, snapshot, nil
}

func prepareRepositoryReviewHistoricalCampaignRecovery(
	ctx context.Context,
	workspace string,
	automation repoaudit.RepositoryReviewAutomation,
	state repoaudit.RepositoryState,
	resolvedProfile workflows.RepositoryReviewModelProfile,
) (repositoryReviewLegacyCampaignBackfill, error) {
	if err := ctx.Err(); err != nil {
		return repositoryReviewLegacyCampaignBackfill{}, err
	}
	if strings.TrimSpace(workspace) == "" || automation.ActiveRunID != "" ||
		automation.Status == repoaudit.RepositoryReviewAutomationRunning ||
		automation.Status == repoaudit.RepositoryReviewAutomationStopping {
		return repositoryReviewLegacyCampaignBackfill{},
			errRepositoryReviewHistoricalCampaignRecovery
	}
	campaignID := automation.CampaignID
	if campaignID == "" {
		campaignID = repoaudit.NewRepositoryReviewCampaignID()
	} else if !repoaudit.ValidRepositoryReviewCampaignID(campaignID) {
		return repositoryReviewLegacyCampaignBackfill{},
			errRepositoryReviewHistoricalCampaignRecovery
	}
	prepared, err := prepareRepositoryReviewHistoricalCampaignBackfill(
		ctx, automation, repositoryReviewHistoricalRecoveryProjection(state), campaignID,
		workflows.NewFileRunStore(workspace), resolvedProfile,
	)
	if err != nil {
		if errors.Is(err, repoaudit.ErrInvalidAutomation) ||
			errors.Is(err, repoaudit.ErrInvalidPlan) || errors.Is(err, os.ErrNotExist) {
			return repositoryReviewLegacyCampaignBackfill{},
				errRepositoryReviewHistoricalCampaignRecovery
		}
		return repositoryReviewLegacyCampaignBackfill{}, err
	}
	if !prepared.Available || !prepared.Exact || !prepared.Request.Coverage.Exact ||
		prepared.Request.Coverage.CommitSHA == "" {
		return repositoryReviewLegacyCampaignBackfill{},
			errRepositoryReviewHistoricalCampaignRecovery
	}
	return prepared, nil
}

func applyRepositoryReviewHistoricalCampaignRecovery(
	ctx context.Context,
	store repoaudit.Store,
	prepared repositoryReviewLegacyCampaignBackfill,
) (repoaudit.RepositoryReviewAutomation, repoaudit.RepositoryState, error) {
	installed, prepared, err := installRepositoryReviewLegacyCampaignAuthority(
		ctx, store, prepared,
	)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, repoaudit.RepositoryState{}, err
	}
	reconciled, err := applyRepositoryReviewLegacyCampaignBackfill(ctx, store, prepared)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, repoaudit.RepositoryState{}, err
	}
	final, err := updateRepositoryReviewHistoricalAutomation(
		ctx, store, installed, prepared, reconciled,
	)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, repoaudit.RepositoryState{}, err
	}
	return final, reconciled, nil
}

func repositoryReviewHistoricalCampaignRecovered(
	automation repoaudit.RepositoryReviewAutomation,
	state repoaudit.RepositoryState,
) bool {
	commitSHA := repositoryReviewRememberedCommit(automation)
	coverage := state.CurrentCampaign
	if !repoaudit.ValidRepositoryReviewCampaignID(automation.CampaignID) ||
		automation.CampaignRecoveryPending || commitSHA == "" ||
		coverage == nil || coverage.ID != automation.CampaignID ||
		coverage.CommitSHA != commitSHA || !coverage.Exact ||
		coverage.RecoveryDigest == "" || len(coverage.AssignmentCatalog) == 0 ||
		state.CampaignHistory[coverage.ID] != coverage.CommitSHA {
		return false
	}
	wantedRuns := make(map[string]struct{}, len(automation.RunIDs))
	for _, runID := range automation.RunIDs {
		if runID = strings.TrimSpace(runID); runID != "" {
			wantedRuns[runID] = struct{}{}
		}
	}
	for _, run := range state.Runs {
		if _, selected := wantedRuns[run.ID]; !selected ||
			!automation.StartedAt.IsZero() && run.CompletedAt.Before(automation.StartedAt) {
			continue
		}
		if run.CampaignID != automation.CampaignID {
			return false
		}
	}
	for _, finding := range repoaudit.CurrentCampaignFindings(
		state, automation.RunIDs, automation.StartedAt,
	) {
		if finding.CampaignID != automation.CampaignID {
			return false
		}
	}
	return true
}

// recoverRepositoryReviewHistoricalCampaign runs the exact legacy recovery
// adapter used by confirmed incompatible-work restart. Its staged automation
// marker, BeginCampaign, ReconcileCampaign, and final CAS are deliberately
// reused so a crash or lost response at any phase is safe to repeat.
func recoverRepositoryReviewHistoricalCampaign(
	ctx context.Context,
	store repoaudit.Store,
	workspace string,
	automation repoaudit.RepositoryReviewAutomation,
	state repoaudit.RepositoryState,
	resolvedProfile workflows.RepositoryReviewModelProfile,
) (repoaudit.RepositoryReviewAutomation, repoaudit.RepositoryState, error) {
	if err := ctx.Err(); err != nil {
		return repoaudit.RepositoryReviewAutomation{}, repoaudit.RepositoryState{}, err
	}
	if repositoryReviewHistoricalCampaignRecovered(automation, state) {
		return automation, state, nil
	}
	prepared, err := prepareRepositoryReviewHistoricalCampaignRecovery(
		ctx, workspace, automation, state, resolvedProfile,
	)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, repoaudit.RepositoryState{}, err
	}
	return applyRepositoryReviewHistoricalCampaignRecovery(ctx, store, prepared)
}

// repositoryReviewHistoricalRecoveryProjection removes only replay-created
// processing projections from the evidence view passed to legacy recovery.
// The durable state is left untouched. In particular, original rfn_* findings
// and their contexts remain available for exact workflow-envelope validation.
func repositoryReviewHistoricalRecoveryProjection(
	state repoaudit.RepositoryState,
) repoaudit.RepositoryState {
	replayRawIDs := make(map[string]struct{})
	replayContextIDs := make(map[string]struct{})
	for _, raw := range state.RawFindings {
		if !repoaudit.HistoricalDeduplicationRawFinding(raw) {
			continue
		}
		replayRawIDs[raw.ID] = struct{}{}
		replayContextIDs[raw.ContextID] = struct{}{}
	}
	replayFindingIDs := make(map[string]struct{})
	for _, finding := range state.DeduplicatedFindings {
		for _, rawID := range finding.RawSourceIDs {
			if _, replaySource := replayRawIDs[rawID]; replaySource {
				replayFindingIDs[finding.ID] = struct{}{}
				break
			}
		}
	}
	contexts := make([]repoaudit.FindingContext, 0, len(state.Contexts))
	for _, contextRecord := range state.Contexts {
		_, replayContext := replayContextIDs[contextRecord.ID]
		if replayContext && contextRecord.InventoryHash == "historical-replay" &&
			contextRecord.ProfileHash == "historical-replay" {
			continue
		}
		contexts = append(contexts, contextRecord)
	}
	findings := make([]repoaudit.Finding, 0, len(state.Findings))
	for _, finding := range state.Findings {
		if _, replayProjection := replayFindingIDs[finding.ID]; replayProjection {
			continue
		}
		findings = append(findings, finding)
	}
	state.Contexts = contexts
	state.Findings = findings
	return state
}

func (c *repositoryReviewController) startHistoricalFindingDeduplication(
	automations []repoaudit.RepositoryReviewAutomation,
) {
	if c == nil || !c.admitBackgroundWorker(&c.historicalDeduplicationMu) {
		return
	}
	go func() {
		defer c.wg.Done()
		defer c.historicalDeduplicationMu.Unlock()
		_ = c.processHistoricalFindingDeduplications(c.ctx, automations)
	}()
}

func (c *repositoryReviewController) wakeHistoricalFindingDeduplication() {
	if c == nil || c.ctx.Err() != nil || c.leasedConfig == nil {
		return
	}
	automations, err := c.leasedStore.ListAutomations(c.ctx)
	if err != nil {
		return
	}
	c.startHistoricalFindingDeduplication(automations)
}

func (c *repositoryReviewController) processHistoricalFindingDeduplications(
	ctx context.Context,
	automations []repoaudit.RepositoryReviewAutomation,
) error {
	if c == nil || c.leasedConfig == nil {
		return errors.New("historical finding deduplication controller is unavailable")
	}
	states, err := c.leasedStore.List()
	if err != nil {
		return err
	}
	var joined error
	for _, listed := range states {
		if err := ctx.Err(); err != nil {
			return errors.Join(joined, err)
		}
		if !listed.HistoricalDeduplication.Required {
			continue
		}
		advanceErr := c.advanceHistoricalFindingDeduplication(ctx, listed, automations)
		if advanceErr != nil &&
			!errors.Is(advanceErr, context.Canceled) &&
			!errors.Is(advanceErr, repoaudit.ErrHistoricalDeduplicationNotQuiescent) &&
			!errors.Is(advanceErr, repoaudit.ErrHistoricalDeduplicationInProgress) {
			_, _, failErr := c.leasedStore.FailHistoricalDeduplicationReplay(
				listed.Repository, listed.HistoricalDeduplication.MergeLease.ID,
			)
			joined = errors.Join(joined, fmt.Errorf(
				"replay historical finding deduplication for %s: %w",
				listed.Repository, errors.Join(advanceErr, failErr),
			))
		}
	}
	return joined
}

func (c *repositoryReviewController) advanceHistoricalFindingDeduplication(
	ctx context.Context,
	state repoaudit.RepositoryState,
	automations []repoaudit.RepositoryReviewAutomation,
) error {
	replay := state.HistoricalDeduplication
	var err error
	switch replay.Status {
	case repoaudit.HistoricalDeduplicationFailed:
		// Terminal replay failures require an explicit resume so operators can
		// inspect conflicts. Completed checkpoints and their frozen identities
		// remain durable until that request is accepted.
		return nil
	case repoaudit.HistoricalDeduplicationMerging:
		// A process may stop after acquiring the narrow lease but before its
		// completion is durable. Atomically clear the interrupted lease, then
		// fall through to recompute groups from current repository-finding
		// versions. This recovery path performs no model work.
		state, replay, err = recoverHistoricalDeduplicationMerge(
			c.leasedStore,
			state.Repository, replay.MergeLease.ID,
		)
		if err != nil {
			return err
		}
	case repoaudit.HistoricalDeduplicationPending:
		automation, found := repositoryAutomationForLedger(
			c.leasedStore, automations, state,
		)
		if !found {
			automation = repositoryFallbackAutomation(c.leasedConfig, state)
		}
		_, snapshot, snapshotErr := repositoryReviewHistoricalProfileSnapshot(
			ctx, c, c.leasedStore, c.leasedConfig, automation,
		)
		if snapshotErr != nil {
			return snapshotErr
		}
		state, replay, err = c.leasedStore.FreezeHistoricalDeduplicationReplay(
			state.Repository, snapshot,
		)
		if err != nil {
			return err
		}
	}
	if replay.Status != repoaudit.HistoricalDeduplicationReplaying {
		return nil
	}
	state, admission, err := admitNextHistoricalDeduplicationBatch(c.leasedStore, state.Repository)
	if err != nil {
		return err
	}
	if !admission.AllComplete {
		for _, raw := range admission.RawFindings {
			if raw.State == repoaudit.RawFindingDeduplicationFailed {
				return errors.New("historical deduplication raw finding reached its attempt limit")
			}
		}
		return nil
	}
	groups, groupErr := historicalDeduplicationRepositoryMergeGroups(state)
	if errors.Is(groupErr, repoaudit.ErrHistoricalDeduplicationNotQuiescent) {
		return nil
	}
	if groupErr != nil {
		return groupErr
	}
	encoded, _ := json.Marshal(groups)
	leaseID := repoaudit.HistoricalDeduplicationMergeLeaseID(
		state.Repository, state.Version, string(encoded),
	)
	_, acquiredReplay, _, err := acquireHistoricalDeduplicationMerge(
		c.leasedStore, state.Repository, leaseID, groups,
	)
	if err != nil {
		return err
	}
	_, _, err = completeHistoricalDeduplicationMerge(
		c.leasedStore, state.Repository, acquiredReplay.MergeLease.ID,
	)
	if err != nil {
		_, _, failErr := failHistoricalDeduplicationReplay(
			c.leasedStore, state.Repository, acquiredReplay.MergeLease.ID,
		)
		return errors.Join(err, failErr)
	}
	return nil
}
