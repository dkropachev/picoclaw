package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

// RepositoryReviewFileAttributionBackfillOptions controls the one-shot legacy
// attribution and recovered-campaign credit repair. Apply requires the digest
// returned by an earlier dry run so an operator cannot commit a different
// evidence or semantic-credit set by accident.
type RepositoryReviewFileAttributionBackfillOptions struct {
	Apply          bool   `json:"apply"`
	ExpectedDigest string `json:"expected_digest,omitempty"`
}

// RepositoryReviewFileAttributionBackfillReport is safe to print from an
// administrative command. It contains counts and identities, never prompts,
// provider responses, repository content, or account credentials.
type RepositoryReviewFileAttributionBackfillReport struct {
	AutomationID                  string `json:"automation_id"`
	Repository                    string `json:"repository"`
	StateVersionBefore            int64  `json:"state_version_before"`
	StateVersionAfter             int64  `json:"state_version_after"`
	ReviewVersionBefore           int64  `json:"review_version_before"`
	ReviewVersionAfter            int64  `json:"review_version_after"`
	CampaignID                    string `json:"campaign_id,omitempty"`
	CampaignAssignmentCredits     int    `json:"campaign_assignment_credits"`
	NewCampaignAssignmentCredits  int    `json:"new_campaign_assignment_credits"`
	CampaignAttributedFiles       int    `json:"campaign_attributed_files"`
	NewCampaignInspectedFiles     int    `json:"new_campaign_inspected_files"`
	ProjectedCompletedAssignments int    `json:"projected_completed_assignments"`
	ProjectedPendingAssignments   int    `json:"projected_pending_assignments"`
	ProjectedInspectedFiles       int    `json:"projected_inspected_files"`
	ProjectedCompletedFiles       int    `json:"projected_completed_files"`
	ConfiguredRuns                int    `json:"configured_runs"`
	RecoveredRuns                 int    `json:"recovered_runs"`
	AllowedNonLedgerRuns          int    `json:"allowed_non_ledger_runs"`
	ChildAttempts                 int    `json:"child_attempts"`
	SuccessfulChildren            int    `json:"successful_children"`
	FailedChildren                int    `json:"failed_children"`
	AttributionRecords            int    `json:"attribution_records"`
	ExistingAttributionRecords    int    `json:"existing_attribution_records"`
	AcknowledgementOccurrences    int    `json:"acknowledgement_occurrences"`
	UniqueFiles                   int    `json:"unique_files"`
	UniqueFileAssignments         int    `json:"unique_file_assignments"`
	Digest                        string `json:"digest"`
	Applied                       bool   `json:"applied"`
}

type repositoryReviewPreparedFileAttributionBackfill struct {
	report         RepositoryReviewFileAttributionBackfillReport
	attributions   []repoaudit.RepositoryReviewFileAttribution
	campaignCredit *repoaudit.RepositoryReviewFileAttributionCreditFence
	creditPreview  repoaudit.RepositoryReviewFileAttributionCreditPreview
}

type repositoryReviewFileAttributionSourceRun struct {
	ID             string `json:"id"`
	Classification string `json:"classification"`
	Digest         string `json:"digest"`
}

type repositoryReviewFileAttributionDigestCounts struct {
	ConfiguredRuns                int `json:"configured_runs"`
	RecoveredRuns                 int `json:"recovered_runs"`
	AllowedNonLedgerRuns          int `json:"allowed_non_ledger_runs"`
	ChildAttempts                 int `json:"child_attempts"`
	SuccessfulChildren            int `json:"successful_children"`
	FailedChildren                int `json:"failed_children"`
	AttributionRecords            int `json:"attribution_records"`
	AcknowledgementOccurrences    int `json:"acknowledgement_occurrences"`
	UniqueFiles                   int `json:"unique_files"`
	UniqueFileAssignments         int `json:"unique_file_assignments"`
	CampaignAssignmentCredits     int `json:"campaign_assignment_credits"`
	CampaignAttributedFiles       int `json:"campaign_attributed_files"`
	ProjectedCompletedAssignments int `json:"projected_completed_assignments"`
	ProjectedPendingAssignments   int `json:"projected_pending_assignments"`
	ProjectedInspectedFiles       int `json:"projected_inspected_files"`
	ProjectedCompletedFiles       int `json:"projected_completed_files"`
}

type repositoryReviewFileAttributionDigestCampaignCredit struct {
	AutomationID      string                                                      `json:"automation_id"`
	CampaignID        string                                                      `json:"campaign_id"`
	RecoveryDigest    string                                                      `json:"recovery_digest"`
	CommitSHA         string                                                      `json:"commit_sha"`
	InventoryHash     string                                                      `json:"inventory_hash"`
	ProfileHash       string                                                      `json:"profile_hash"`
	ScopeDigest       string                                                      `json:"scope_digest"`
	SelectedFiles     int                                                         `json:"selected_files"`
	AssignmentCatalog []repoaudit.RepositoryReviewAssignment                      `json:"assignment_catalog"`
	Credits           []repoaudit.RepositoryReviewFileAttributionAssignmentCredit `json:"credits"`
}

type repositoryReviewFileAttributionDigestEnvelope struct {
	Schema            string                                               `json:"schema"`
	AutomationID      string                                               `json:"automation_id"`
	AutomationVersion int64                                                `json:"automation_version"`
	Repository        string                                               `json:"repository"`
	LedgerRepository  string                                               `json:"ledger_repository"`
	AutomationStatus  repoaudit.RepositoryReviewAutomationStatus           `json:"automation_status"`
	RunIDs            []string                                             `json:"run_ids"`
	Runs              []repositoryReviewFileAttributionSourceRun           `json:"runs"`
	Counts            repositoryReviewFileAttributionDigestCounts          `json:"counts"`
	Attributions      []repoaudit.RepositoryReviewFileAttribution          `json:"attributions"`
	CampaignCredit    *repositoryReviewFileAttributionDigestCampaignCredit `json:"campaign_credit,omitempty"`
}

// BackfillRepositoryReviewFileAttributions reconstructs successful historical
// file acknowledgements from retained workflow evidence. When an exact legacy
// campaign has already been recovered, the same digest-fenced operation also
// seeds strictly matched semantic focus/reviewer credits across profile drift.
func BackfillRepositoryReviewFileAttributions(
	ctx context.Context,
	workspace string,
	automationID string,
	options RepositoryReviewFileAttributionBackfillOptions,
) (RepositoryReviewFileAttributionBackfillReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	workspace = strings.TrimSpace(workspace)
	automationID = strings.TrimSpace(automationID)
	options.ExpectedDigest = strings.TrimSpace(options.ExpectedDigest)
	if workspace == "" || automationID == "" || options.Apply && options.ExpectedDigest == "" {
		return RepositoryReviewFileAttributionBackfillReport{}, repoaudit.ErrInvalidPlan
	}
	store := repoaudit.NewStore(workspace)
	unlockController, err := store.LockAutomationController()
	if err != nil {
		return RepositoryReviewFileAttributionBackfillReport{}, err
	}
	defer unlockController()
	automation, found, err := store.GetAutomation(ctx, automationID)
	if err != nil {
		return RepositoryReviewFileAttributionBackfillReport{}, err
	}
	if !found {
		return RepositoryReviewFileAttributionBackfillReport{}, errors.New(
			"repository review automation was not found",
		)
	}
	state, found, err := store.ResolveRepositoryState(automation.Repository, automation.RunIDs)
	if err != nil {
		return RepositoryReviewFileAttributionBackfillReport{}, err
	}
	if !found {
		return RepositoryReviewFileAttributionBackfillReport{}, errors.New(
			"repository review ledger was not found",
		)
	}
	prepared, err := prepareRepositoryReviewFileAttributionBackfill(
		ctx, automation, state, workflows.NewFileRunStore(workspace),
	)
	if err != nil {
		return prepared.report, err
	}
	if options.ExpectedDigest != "" && options.ExpectedDigest != prepared.report.Digest {
		return prepared.report, fmt.Errorf(
			"%w: attribution evidence digest changed", repoaudit.ErrConflict,
		)
	}
	if !options.Apply {
		return prepared.report, nil
	}
	return applyPreparedRepositoryReviewFileAttributionBackfill(ctx, store, state, prepared)
}

func applyPreparedRepositoryReviewFileAttributionBackfill(
	ctx context.Context,
	store repoaudit.Store,
	state repoaudit.RepositoryState,
	prepared repositoryReviewPreparedFileAttributionBackfill,
) (RepositoryReviewFileAttributionBackfillReport, error) {
	if len(prepared.attributions) == 0 &&
		(prepared.campaignCredit == nil || len(prepared.creditPreview.Credits) == 0) {
		prepared.report.Applied = true
		return prepared.report, nil
	}
	merged, err := store.MergeRepositoryReviewFileAttributions(
		ctx,
		repoaudit.MergeRepositoryReviewFileAttributionsRequest{
			Repository: state.Repository, ExpectedVersion: state.Version,
			Attributions: prepared.attributions, CampaignCredit: prepared.campaignCredit,
		},
	)
	if err != nil {
		return prepared.report, err
	}
	prepared.report.Applied = true
	prepared.report.StateVersionAfter = merged.Version
	prepared.report.ReviewVersionAfter = merged.ReviewVersion
	return prepared.report, nil
}

func prepareRepositoryReviewFileAttributionBackfill(
	ctx context.Context,
	automation repoaudit.RepositoryReviewAutomation,
	state repoaudit.RepositoryState,
	runStore repositoryReviewWorkflowRunLoader,
) (repositoryReviewPreparedFileAttributionBackfill, error) {
	prepared := repositoryReviewPreparedFileAttributionBackfill{
		report: RepositoryReviewFileAttributionBackfillReport{
			AutomationID: automation.ID, Repository: state.Repository,
			StateVersionBefore: state.Version, StateVersionAfter: state.Version,
			ReviewVersionBefore: state.ReviewVersion, ReviewVersionAfter: state.ReviewVersion,
			ConfiguredRuns: len(automation.RunIDs),
		},
	}
	if err := ctx.Err(); err != nil {
		return prepared, err
	}
	if runStore == nil || automation.ID == "" || state.Repository == "" ||
		automation.ActiveRunID != "" ||
		automation.Status == repoaudit.RepositoryReviewAutomationRunning ||
		automation.Status == repoaudit.RepositoryReviewAutomationStopping ||
		len(automation.RunIDs) == 0 || len(automation.RunIDs) >= repositoryReviewLegacyBackfillMaxRuns {
		return prepared, repoaudit.ErrInvalidAutomation
	}
	identityMatched := false
	for _, identity := range repoaudit.RepositoryLedgerIdentities(automation.Repository) {
		identityMatched = identityMatched || identity == state.Repository
	}
	if !identityMatched {
		return prepared, repoaudit.ErrInvalidAutomation
	}

	configured := make(map[string]struct{}, len(automation.RunIDs))
	for _, runID := range automation.RunIDs {
		runID = strings.TrimSpace(runID)
		if runID == "" {
			return prepared, repoaudit.ErrInvalidAutomation
		}
		if _, duplicate := configured[runID]; duplicate {
			return prepared, repoaudit.ErrInvalidAutomation
		}
		configured[runID] = struct{}{}
	}
	ledgerRuns := make(map[string]repoaudit.ReviewRun, len(configured))
	for _, run := range state.Runs {
		if _, selected := configured[run.ID]; !selected {
			continue
		}
		if _, duplicate := ledgerRuns[run.ID]; duplicate {
			return prepared, repoaudit.ErrInvalidPlan
		}
		ledgerRuns[run.ID] = run
	}

	existing := make(map[string]repoaudit.RepositoryReviewFileAttribution, len(state.FileAttributions))
	for _, attribution := range state.FileAttributions {
		existing[attribution.ID] = attribution
	}
	uniqueFiles := make(map[string]struct{})
	uniqueAssignments := make(map[string]struct{})
	sourceRuns := make([]repositoryReviewFileAttributionSourceRun, 0, len(automation.RunIDs))
	sourceBytes := 0
	for _, runID := range automation.RunIDs {
		if err := ctx.Err(); err != nil {
			return prepared, err
		}
		workflowRun, loadErr := repositoryReviewLoadLegacyWorkflowRun(ctx, runStore, runID)
		if loadErr != nil || workflowRun == nil {
			if loadErr != nil {
				return prepared, loadErr
			}
			return prepared, repoaudit.ErrInvalidPlan
		}
		encodedRun, encodeErr := json.Marshal(workflowRun)
		if encodeErr != nil || len(encodedRun) > repositoryReviewLegacyBackfillMaxRunBytes ||
			sourceBytes+len(encodedRun) > repositoryReviewLegacyBackfillMaxSourceBytes {
			return prepared, repoaudit.ErrInvalidPlan
		}
		sourceBytes += len(encodedRun)
		runDigest := sha256.Sum256(encodedRun)
		sourceRun := repositoryReviewFileAttributionSourceRun{
			ID: runID, Digest: "sha256:" + hex.EncodeToString(runDigest[:]),
		}

		ledgerRun, retained := ledgerRuns[runID]
		if !retained {
			allowed, _ := repositoryReviewLegacyNonLedgerRunAllowed(workflowRun)
			if !allowed {
				return prepared, repoaudit.ErrInvalidPlan
			}
			sourceRun.Classification = "allowed_non_ledger"
			sourceRuns = append(sourceRuns, sourceRun)
			prepared.report.AllowedNonLedgerRuns++
			continue
		}
		plan, evidence, valid := repositoryReviewLegacyRunEvidence(
			workflowRun, ledgerRun, state.Repository,
		)
		if !valid || !repositoryReviewFileAttributionEvidenceMatchesLedger(
			plan, evidence, ledgerRun,
		) {
			return prepared, repoaudit.ErrInvalidPlan
		}
		reviewStep := repositoryReviewRunStep(workflowRun, "review")
		var rawChildren []map[string]any
		if !repositoryReviewDecodeValue(reviewStep.Outputs["managed_children"], &rawChildren) ||
			len(rawChildren) != len(evidence.Children) {
			return prepared, repoaudit.ErrInvalidPlan
		}
		rootAgentID, _ := reviewStep.Outputs["agent_id"].(string)
		rootAgentID = strings.TrimSpace(rootAgentID)
		if rootAgentID != "main" {
			return prepared, repoaudit.ErrInvalidPlan
		}
		sourceRun.Classification = "retained_ledger"
		sourceRuns = append(sourceRuns, sourceRun)
		prepared.report.RecoveredRuns++
		for childIndex, childEvidence := range evidence.Children {
			prepared.report.ChildAttempts++
			declaredIndex, indexOK := repositoryReviewLegacyChildIndex(rawChildren[childIndex])
			if !indexOK || declaredIndex != childIndex+1 {
				return prepared, repoaudit.ErrInvalidPlan
			}
			if childEvidence.Successful {
				prepared.report.SuccessfulChildren++
			} else {
				prepared.report.FailedChildren++
			}
			if !childEvidence.Successful || len(childEvidence.AcknowledgedFiles) == 0 {
				continue
			}
			if childEvidence.Observation == nil || childEvidence.AssignmentID == "" ||
				childEvidence.FocusID == "" || childEvidence.ReviewerIdentity == "" {
				return prepared, repoaudit.ErrInvalidPlan
			}
			observation := childEvidence.Observation
			usageModel, usageErr := repositoryReviewLegacyChildUsageModel(
				rawChildren[childIndex], childEvidence.ReviewerIdentity,
			)
			if usageErr != nil {
				return prepared, usageErr
			}
			attribution, attributionErr := repoaudit.NewRepositoryReviewFileAttribution(
				repoaudit.RepositoryReviewFileAttribution{
					AutomationID: automation.ID, RunID: runID, CommitSHA: plan.CommitSHA,
					InventoryHash: plan.InventoryHash, ProfileHash: plan.ProfileHash,
					AssignmentID: childEvidence.AssignmentID, FocusID: childEvidence.FocusID,
					RootAgentID: rootAgentID, ReviewerIdentity: childEvidence.ReviewerIdentity,
					Model: observation.Model, ModelAlias: observation.ModelAlias,
					Account:           observation.Account,
					UsageModel:        usageModel,
					AcknowledgedFiles: append([]repoaudit.FileRef(nil), childEvidence.AcknowledgedFiles...),
					EvidenceDigest:    observation.RawDigest,
					Source:            repoaudit.RepositoryReviewFileAttributionSourceLegacyManagedChild,
					ChildIndex:        childIndex + 1, Required: childEvidence.Required,
					CompletedAt: ledgerRun.CompletedAt,
				},
			)
			if attributionErr != nil {
				return prepared, attributionErr
			}
			if retained, found := existing[attribution.ID]; found {
				resolved, resolveErr := repositoryReviewResolveExistingFileAttribution(
					retained, attribution,
				)
				if resolveErr != nil {
					return prepared, resolveErr
				}
				attribution = resolved
				prepared.report.ExistingAttributionRecords++
			}
			prepared.attributions = append(prepared.attributions, attribution)
			for _, file := range attribution.AcknowledgedFiles {
				prepared.report.AcknowledgementOccurrences++
				fileKey := strings.Join([]string{attribution.CommitSHA, file.Path, file.BlobSHA}, "\x00")
				uniqueFiles[fileKey] = struct{}{}
				uniqueAssignments[strings.Join([]string{
					fileKey, attribution.FocusID, attribution.ReviewerIdentity,
				}, "\x00")] = struct{}{}
			}
		}
	}
	if prepared.report.RecoveredRuns != len(ledgerRuns) {
		return prepared, repoaudit.ErrInvalidPlan
	}
	sort.Slice(prepared.attributions, func(i, j int) bool {
		return prepared.attributions[i].ID < prepared.attributions[j].ID
	})
	prepared.report.AttributionRecords = len(prepared.attributions)
	prepared.report.UniqueFiles = len(uniqueFiles)
	prepared.report.UniqueFileAssignments = len(uniqueAssignments)
	if campaign := state.CurrentCampaign; campaign != nil && campaign.Exact &&
		campaign.RecoveryDigest != "" && automation.CampaignID == campaign.ID {
		fence := repoaudit.RepositoryReviewFileAttributionCreditFence{
			AutomationID: automation.ID, CampaignID: campaign.ID,
			ExpectedReviewVersion: state.ReviewVersion,
		}
		preview, previewErr := repoaudit.PreviewRepositoryReviewFileAttributionCredits(
			state, fence, prepared.attributions,
		)
		if previewErr != nil {
			return prepared, previewErr
		}
		prepared.campaignCredit = &fence
		prepared.creditPreview = preview
		prepared.report.CampaignID = campaign.ID
		prepared.report.CampaignAssignmentCredits = preview.EffectiveAssignmentCredits
		prepared.report.NewCampaignAssignmentCredits = preview.NewAssignmentCredits
		prepared.report.CampaignAttributedFiles = preview.EffectiveInspectedFiles
		prepared.report.NewCampaignInspectedFiles = preview.NewInspectedFiles
		prepared.report.ProjectedCompletedAssignments = preview.ProjectedCompletedAssignments
		prepared.report.ProjectedPendingAssignments = preview.ProjectedPendingAssignments
		prepared.report.ProjectedInspectedFiles = preview.ProjectedInspectedFiles
		prepared.report.ProjectedCompletedFiles = preview.ProjectedCompletedFiles
	}
	var campaignCredit *repositoryReviewFileAttributionDigestCampaignCredit
	if prepared.campaignCredit != nil {
		campaign := state.CurrentCampaign
		campaignCredit = &repositoryReviewFileAttributionDigestCampaignCredit{
			AutomationID:   prepared.campaignCredit.AutomationID,
			CampaignID:     prepared.campaignCredit.CampaignID,
			RecoveryDigest: campaign.RecoveryDigest,
			CommitSHA:      campaign.CommitSHA,
			InventoryHash:  campaign.InventoryHash,
			ProfileHash:    campaign.ProfileHash,
			ScopeDigest:    campaign.ScopeDigest,
			SelectedFiles:  campaign.SelectedFiles,
			AssignmentCatalog: append(
				[]repoaudit.RepositoryReviewAssignment(nil), campaign.AssignmentCatalog...,
			),
			Credits: append(
				[]repoaudit.RepositoryReviewFileAttributionAssignmentCredit(nil),
				prepared.creditPreview.Credits...,
			),
		}
	}
	envelope := repositoryReviewFileAttributionDigestEnvelope{
		Schema:       "repository-review-file-attributions-v2",
		AutomationID: automation.ID, AutomationVersion: automation.Version,
		Repository: automation.Repository, LedgerRepository: state.Repository,
		AutomationStatus: automation.Status,
		RunIDs:           append([]string(nil), automation.RunIDs...),
		Runs:             sourceRuns,
		Counts: repositoryReviewFileAttributionDigestCounts{
			ConfiguredRuns:                prepared.report.ConfiguredRuns,
			RecoveredRuns:                 prepared.report.RecoveredRuns,
			AllowedNonLedgerRuns:          prepared.report.AllowedNonLedgerRuns,
			ChildAttempts:                 prepared.report.ChildAttempts,
			SuccessfulChildren:            prepared.report.SuccessfulChildren,
			FailedChildren:                prepared.report.FailedChildren,
			AttributionRecords:            prepared.report.AttributionRecords,
			AcknowledgementOccurrences:    prepared.report.AcknowledgementOccurrences,
			UniqueFiles:                   prepared.report.UniqueFiles,
			UniqueFileAssignments:         prepared.report.UniqueFileAssignments,
			CampaignAssignmentCredits:     prepared.report.CampaignAssignmentCredits,
			CampaignAttributedFiles:       prepared.report.CampaignAttributedFiles,
			ProjectedCompletedAssignments: prepared.report.ProjectedCompletedAssignments,
			ProjectedPendingAssignments:   prepared.report.ProjectedPendingAssignments,
			ProjectedInspectedFiles:       prepared.report.ProjectedInspectedFiles,
			ProjectedCompletedFiles:       prepared.report.ProjectedCompletedFiles,
		},
		Attributions: prepared.attributions, CampaignCredit: campaignCredit,
	}
	digest, err := repositoryReviewFileAttributionEnvelopeDigest(
		envelope, repositoryReviewLegacyBackfillMaxSourceBytes,
	)
	if err != nil {
		return prepared, err
	}
	prepared.report.Digest = digest
	return prepared, nil
}

func repositoryReviewFileAttributionEnvelopeDigest(
	envelope repositoryReviewFileAttributionDigestEnvelope,
	maximumBytes int,
) (string, error) {
	encoded, err := json.Marshal(envelope)
	if err != nil || maximumBytes < 1 || len(encoded) > maximumBytes {
		return "", repoaudit.ErrInvalidPlan
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func repositoryReviewFileAttributionEvidenceMatchesLedger(
	plan repoaudit.Plan,
	evidence workflows.RepositoryReviewManagedEvidence,
	ledgerRun repoaudit.ReviewRun,
) bool {
	pending := make(map[string]repoaudit.FileRef, len(plan.PendingFiles))
	for _, file := range plan.PendingFiles {
		pending[file.Path] = file
	}
	unsupported := make(map[string]repoaudit.FileRef, len(ledgerRun.UnsupportedPaths))
	for _, pathValue := range ledgerRun.UnsupportedPaths {
		file, exists := pending[pathValue]
		if !exists {
			return false
		}
		unsupported[pathValue] = file
	}
	for _, file := range evidence.UnsupportedFiles {
		if unsupported[file.Path] != file.FileRef {
			return false
		}
	}
	completed := make(map[string]struct{}, len(evidence.CompletedFiles))
	for _, file := range evidence.CompletedFiles {
		completed[file.Path] = struct{}{}
	}
	unreviewedPaths := make([]string, 0, len(plan.PendingFiles))
	for _, file := range plan.PendingFiles {
		if _, complete := completed[file.Path]; complete {
			continue
		}
		if _, terminal := unsupported[file.Path]; terminal {
			continue
		}
		unreviewedPaths = append(unreviewedPaths, file.Path)
	}
	sort.Strings(unreviewedPaths)
	models := make([]string, 0, len(evidence.Observations))
	seenModels := make(map[string]struct{}, len(evidence.Observations))
	for _, observation := range evidence.Observations {
		model := observation.Model
		if observation.ModelAlias != "" {
			model = observation.ModelAlias
		}
		if _, seen := seenModels[model]; seen {
			continue
		}
		seenModels[model] = struct{}{}
		models = append(models, model)
	}
	return ledgerRun.UnsupportedCount == len(unsupported) &&
		ledgerRun.ReviewedFiles == len(evidence.CompletedFiles) &&
		ledgerRun.SkippedFiles == len(plan.UnchangedFiles) &&
		ledgerRun.UnreviewedFiles == len(unreviewedPaths) &&
		ledgerRun.RemainingFiles == len(plan.DeferredFiles)+len(unreviewedPaths) &&
		slices.Equal(ledgerRun.UnreviewedPaths, unreviewedPaths) &&
		slices.Equal(ledgerRun.Models, models) && ledgerRun.RejectedFindings == 0
}

func repositoryReviewResolveExistingFileAttribution(
	existing repoaudit.RepositoryReviewFileAttribution,
	recovered repoaudit.RepositoryReviewFileAttribution,
) (repoaudit.RepositoryReviewFileAttribution, error) {
	if existing.ID != recovered.ID {
		return repoaudit.RepositoryReviewFileAttribution{}, repoaudit.ErrConflict
	}
	if existing.Source == repoaudit.RepositoryReviewFileAttributionSourceLegacyManagedChild {
		if reflect.DeepEqual(existing, recovered) {
			return existing, nil
		}
		return repoaudit.RepositoryReviewFileAttribution{}, repoaudit.ErrConflict
	}
	if existing.Source != repoaudit.RepositoryReviewFileAttributionSourceLiveCheckpoint ||
		!repositoryReviewLiveAttributionCoversRecovery(existing, recovered) {
		return repoaudit.RepositoryReviewFileAttribution{}, repoaudit.ErrConflict
	}
	return existing, nil
}

func repositoryReviewLiveAttributionCoversRecovery(
	live repoaudit.RepositoryReviewFileAttribution,
	recovered repoaudit.RepositoryReviewFileAttribution,
) bool {
	if live.AutomationID != recovered.AutomationID || live.RunID != recovered.RunID ||
		live.CommitSHA != recovered.CommitSHA || live.InventoryHash != recovered.InventoryHash ||
		live.ProfileHash != recovered.ProfileHash || live.FocusID != recovered.FocusID ||
		live.RootAgentID != recovered.RootAgentID ||
		live.ReviewerIdentity != recovered.ReviewerIdentity ||
		live.EvidenceDigest != recovered.EvidenceDigest ||
		live.ChildIndex != recovered.ChildIndex || live.Required != recovered.Required ||
		!reflect.DeepEqual(live.AcknowledgedFiles, recovered.AcknowledgedFiles) {
		return false
	}
	legacyModel := strings.TrimSpace(recovered.UsageModel)
	if legacyModel == "" && recovered.ModelAlias != "" {
		legacyModel = strings.TrimSpace(recovered.Model)
	}
	if legacyModel != "" && legacyModel != strings.TrimSpace(live.Model) &&
		legacyModel != strings.TrimSpace(live.UsageModel) {
		return false
	}
	if recovered.Account != "" && recovered.Account != live.Account {
		return false
	}
	return true
}

func repositoryReviewLegacyChildIndex(child map[string]any) (int, bool) {
	var envelope struct {
		Index int `json:"index"`
	}
	if !repositoryReviewDecodeValue(child, &envelope) || envelope.Index < 1 {
		return 0, false
	}
	return envelope.Index, true
}

func repositoryReviewLegacyChildUsageModel(
	child map[string]any,
	reviewerIdentity string,
) (string, error) {
	value, declared := child["usage"]
	if !declared || value == nil {
		return "", nil
	}
	var usage []struct {
		Model    string `json:"model"`
		Reviewer string `json:"reviewer"`
	}
	if !repositoryReviewDecodeValue(value, &usage) || len(usage) == 0 || len(usage) > 128 {
		return "", repoaudit.ErrInvalidPlan
	}
	selectedReviewer := ""
	if reviewerIdentity == "default" {
		var modelEnvelope struct {
			Selected string `json:"selected"`
		}
		if repositoryReviewDecodeValue(child["model"], &modelEnvelope) {
			selectedReviewer = strings.TrimSpace(modelEnvelope.Selected)
		}
	}
	model := ""
	for _, entry := range usage {
		candidate := strings.TrimSpace(entry.Model)
		reviewer := strings.TrimSpace(entry.Reviewer)
		reviewerMatches := reviewer == "" || reviewer == reviewerIdentity ||
			reviewerIdentity == "default" && selectedReviewer != "" && reviewer == selectedReviewer
		if candidate == "" || len(candidate) > 256 || !reviewerMatches {
			return "", repoaudit.ErrInvalidPlan
		}
		if model != "" && model != candidate {
			return "", nil
		}
		model = candidate
	}
	return model, nil
}
