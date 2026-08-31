package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

var errRepositoryReviewHistoricalCampaignRecovery = errors.New(
	"exact historical campaign recovery is unavailable",
)

var recoverRepositoryReviewHistoricalCampaignForRetry = recoverRepositoryReviewHistoricalCampaign

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
	controller := h.repositoryReviewControllerInstance()
	if ledger.State.HistoricalDeduplication.Required &&
		ledger.State.HistoricalDeduplication.Status != repoaudit.HistoricalDeduplicationCompleted &&
		!repositoryReviewHistoricalCampaignRecovered(ledger.Automation, ledger.State) {
		cfg, configErr := config.LoadConfig(h.configPath)
		if configErr != nil {
			writeRepositoryReviewAutomationError(w, configErr)
			return
		}
		resolvedProfile, profileErr := resolveRepositoryReviewHistoricalCampaignProfile(
			r.Context(), h.configPath, cfg, ledger.Automation,
		)
		if profileErr != nil {
			writeRepositoryReviewAutomationError(w, profileErr)
			return
		}
		ledger.Automation, ledger.State, err = recoverRepositoryReviewHistoricalCampaignForRetry(
			r.Context(), ledger.Store, cfg.WorkspacePath(), ledger.Automation,
			ledger.State, resolvedProfile,
		)
		if errors.Is(err, errRepositoryReviewHistoricalCampaignRecovery) {
			writeRepositoryReviewJSON(w, http.StatusConflict, map[string]string{
				"code":    "historical_deduplication_campaign_recovery_required",
				"message": "Historical deduplication cannot be retried because the exact retained review campaign could not be recovered. Restore the automation's workflow run history, then retry.",
			})
			return
		}
		if err != nil {
			writeRepositoryReviewAutomationError(w, err)
			return
		}
	}
	state, replay, err := ledger.Store.RetryHistoricalDeduplicationReplay(ledger.State.Repository)
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

// recoverRepositoryReviewHistoricalCampaign runs the existing exact legacy
// recovery adapter before replay is made pending. Its staged automation marker,
// BeginCampaign, ReconcileCampaign, and final CAS are deliberately reused so a
// crash or lost response at any phase is safe to repeat.
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
	if strings.TrimSpace(workspace) == "" || automation.ActiveRunID != "" ||
		automation.Status == repoaudit.RepositoryReviewAutomationRunning ||
		automation.Status == repoaudit.RepositoryReviewAutomationStopping {
		return repoaudit.RepositoryReviewAutomation{}, repoaudit.RepositoryState{},
			errRepositoryReviewHistoricalCampaignRecovery
	}
	campaignID := automation.CampaignID
	if campaignID == "" {
		campaignID = repoaudit.NewRepositoryReviewCampaignID()
	} else if !repoaudit.ValidRepositoryReviewCampaignID(campaignID) {
		return repoaudit.RepositoryReviewAutomation{}, repoaudit.RepositoryState{},
			errRepositoryReviewHistoricalCampaignRecovery
	}
	prepared, err := prepareRepositoryReviewHistoricalCampaignBackfill(
		ctx, automation, repositoryReviewHistoricalRecoveryProjection(state), campaignID,
		workflows.NewFileRunStore(workspace), resolvedProfile,
	)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, repoaudit.RepositoryState{},
			errRepositoryReviewHistoricalCampaignRecovery
	}
	if !prepared.Available || !prepared.Exact || !prepared.Request.Coverage.Exact ||
		prepared.Request.Coverage.CommitSHA == "" {
		return repoaudit.RepositoryReviewAutomation{}, repoaudit.RepositoryState{},
			errRepositoryReviewHistoricalCampaignRecovery
	}
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
		// Terminal replay failures require an explicit retry so operators can
		// inspect conflicts. Retry re-snapshots the then-current profile.
		return nil
	case repoaudit.HistoricalDeduplicationMerging:
		_, _, mergeErr := c.leasedStore.CompleteHistoricalDeduplicationMerge(
			state.Repository, replay.MergeLease.ID,
		)
		if mergeErr != nil {
			_, _, failErr := c.leasedStore.FailHistoricalDeduplicationReplay(
				state.Repository, replay.MergeLease.ID,
			)
			return errors.Join(mergeErr, failErr)
		}
		return nil
	case repoaudit.HistoricalDeduplicationPending:
		automation, found := repositoryAutomationForLedger(
			c.leasedStore, automations, state,
		)
		if !found {
			automation = repositoryFallbackAutomation(c.leasedConfig, state)
		}
		materialized := automation
		if strings.TrimSpace(automation.ProfileID) != "" {
			profile, profileFound, profileErr := c.leasedStore.GetProfile(ctx, automation.ProfileID)
			if profileErr != nil {
				return profileErr
			}
			if !profileFound {
				return errors.New("historical deduplication profile was not found")
			}
			materialized, profileErr = repoaudit.MaterializeRepositoryReviewAutomation(
				profile, automation,
			)
			if profileErr != nil {
				return profileErr
			}
		}
		materialized.EffectiveAccountRef = repositoryReviewEffectiveAccountRef(
			c.leasedConfig, materialized.AccountRef,
		)
		snapshot, snapshotErr := c.repositoryReviewDeduplicationSnapshot(materialized)
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
