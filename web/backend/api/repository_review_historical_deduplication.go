package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

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
		if raw.LegacyFindingID != "" && strings.HasPrefix(raw.ID, "rrw_") {
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
	state, replay, err := ledger.Store.RetryHistoricalDeduplicationReplay(ledger.State.Repository)
	if err != nil {
		writeRepositoryReviewAutomationError(w, err)
		return
	}
	if controller := h.repositoryReviewControllerInstance(); controller != nil {
		controller.wakeHistoricalFindingDeduplication()
	}
	writeRepositoryReviewJSON(w, http.StatusAccepted, map[string]any{
		"automation":               projectRepositoryReviewAutomation(ledger.Automation),
		"repository":               repoaudit.Summarize(state),
		"historical_deduplication": replay,
	})
}

func (c *repositoryReviewController) startHistoricalFindingDeduplication(
	automations []repoaudit.RepositoryReviewAutomation,
) {
	if c == nil || c.ctx.Err() != nil || !c.historicalDeduplicationMu.TryLock() {
		return
	}
	c.wg.Add(1)
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
		_, _, err := c.leasedStore.CompleteHistoricalDeduplicationMerge(
			state.Repository, replay.MergeLease.ID,
		)
		if err != nil {
			_, _, failErr := c.leasedStore.FailHistoricalDeduplicationReplay(
				state.Repository, replay.MergeLease.ID,
			)
			return errors.Join(err, failErr)
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
	state, admission, err := c.leasedStore.AdmitNextHistoricalDeduplicationBatch(state.Repository)
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
	groups, groupErr := repoaudit.HistoricalDeduplicationRepositoryMergeGroups(state)
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
	_, acquiredReplay, _, err := c.leasedStore.AcquireHistoricalDeduplicationMerge(
		state.Repository, leaseID, groups,
	)
	if err != nil {
		return err
	}
	_, _, err = c.leasedStore.CompleteHistoricalDeduplicationMerge(
		state.Repository, acquiredReplay.MergeLease.ID,
	)
	if err != nil {
		_, _, failErr := c.leasedStore.FailHistoricalDeduplicationReplay(
			state.Repository, acquiredReplay.MergeLease.ID,
		)
		return errors.Join(err, failErr)
	}
	return nil
}
