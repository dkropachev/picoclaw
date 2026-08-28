package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	repositoryReviewLegacyBackfillMaxRuns         = 1_000
	repositoryReviewLegacyBackfillMaxPaths        = 100_000
	repositoryReviewLegacyBackfillMaxBytes        = 32 << 20
	repositoryReviewLegacyBackfillMaxRunBytes     = 8 << 20
	repositoryReviewLegacyBackfillMaxSourceBytes  = 256 << 20
	repositoryReviewLegacyBackfillMaxManifestRefs = 2_000_000
)

type repositoryReviewWorkflowRunLoader interface {
	GetRun(ctx context.Context, runID string) (*workflows.Run, error)
}

type repositoryReviewBoundedWorkflowRunLoader interface {
	GetRunBounded(ctx context.Context, runID string, maximumBytes int64) (*workflows.Run, error)
}

func repositoryReviewLoadLegacyWorkflowRun(
	ctx context.Context,
	store repositoryReviewWorkflowRunLoader,
	runID string,
) (*workflows.Run, error) {
	if bounded, ok := store.(repositoryReviewBoundedWorkflowRunLoader); ok {
		return bounded.GetRunBounded(ctx, runID, repositoryReviewLegacyBackfillMaxRunBytes)
	}
	return store.GetRun(ctx, runID)
}

// repositoryReviewLegacyCampaignBackfill is a prepared, evidence-only CAS
// request. Exact means the entire retained campaign history and provenance are
// recoverable; incomplete recovery is unavailable and never mutates authority.
type repositoryReviewLegacyCampaignBackfill struct {
	Available             bool
	Exact                 bool
	InspectedFiles        int
	CompletedFiles        int
	UnsupportedFiles      int
	FindingOccurrences    int
	Request               repoaudit.ReconcileCampaignRequest
	AutomationID          string
	AutomationVersion     int64
	AutomationStatus      repoaudit.RepositoryReviewAutomationStatus
	ScopeSelection        repoaudit.RepositoryReviewScopeSelection
	ScopePlan             repoaudit.RepositoryReviewScopePlan
	RecoveredRuns         int
	ExpectedRuns          int
	RecoveredContexts     int
	RecoveredFindings     int
	UnrecoveredFindingIDs []string
}

type repositoryReviewLegacyRecoveredRun struct {
	recovery   repoaudit.RepositoryReviewCampaignRunRecovery
	contextIDs []string
	findingIDs []string
}

type repositoryReviewLegacyRunEvidenceRecord struct {
	ledgerRun       repoaudit.ReviewRun
	plan            repoaudit.Plan
	evidence        workflows.RepositoryReviewManagedEvidence
	manifest        []repoaudit.FileRef
	scopePlanHash   string
	workflowUpdated time.Time
}

// prepareRepositoryReviewLegacyCampaignBackfill is pure apart from bounded,
// context-fenced reads of retained workflow runs. It trusts neither finding
// contexts nor assigned scopes as proof that a file was inspected.
func prepareRepositoryReviewLegacyCampaignBackfill(
	ctx context.Context,
	automation repoaudit.RepositoryReviewAutomation,
	state repoaudit.RepositoryState,
	campaignID string,
	runStore repositoryReviewWorkflowRunLoader,
	resolvedProfiles ...workflows.RepositoryReviewModelProfile,
) (repositoryReviewLegacyCampaignBackfill, error) {
	result := repositoryReviewLegacyCampaignBackfill{
		Exact: true, AutomationID: automation.ID, AutomationVersion: automation.Version,
		AutomationStatus: automation.Status,
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	identityMatched := false
	for _, identity := range repoaudit.RepositoryLedgerIdentities(automation.Repository) {
		if identity == state.Repository {
			identityMatched = true
			break
		}
	}
	if runStore == nil || !repoaudit.ValidRepositoryReviewCampaignID(campaignID) ||
		strings.TrimSpace(automation.Repository) == "" || !identityMatched ||
		automation.ActiveRunID != "" ||
		automation.Status == repoaudit.RepositoryReviewAutomationRunning ||
		automation.Status == repoaudit.RepositoryReviewAutomationStopping {
		return result, repoaudit.ErrInvalidAutomation
	}
	if automation.CampaignID != "" && automation.CampaignID != campaignID {
		return result, repoaudit.ErrConflict
	}
	if state.CurrentCampaign != nil && state.CurrentCampaign.ID != campaignID {
		return result, repoaudit.ErrConflict
	}
	if len(resolvedProfiles) > 1 {
		return result, repoaudit.ErrInvalidPlan
	}
	resolvedProfile := workflows.RepositoryReviewModelProfile{
		Revision:        "legacy-automation-profile",
		AccountRef:      automation.EffectiveAccountRef,
		ReviewerModels:  repositoryReviewExecutionModels(automation),
		MaxContentBytes: int(automation.MaxContentBytes),
	}
	if len(resolvedProfiles) == 1 {
		resolvedProfile = resolvedProfiles[0]
	}
	if len(resolvedProfile.ReviewerModels) == 0 && !resolvedProfile.IncludeDefaultReviewer ||
		resolvedProfile.MaxContentBytes < 1 {
		return result, repoaudit.ErrInvalidPlan
	}
	_, resolvedMaxErr := workflows.RepositoryBugFinderEffectiveMaxContentBytes(
		automation.MaxContentBytes, resolvedProfile.MaxContentBytes,
	)
	if resolvedMaxErr != nil {
		return result, resolvedMaxErr
	}
	if automation.StartedAt.IsZero() || len(automation.RunIDs) == 0 ||
		len(automation.RunIDs) >= repositoryReviewLegacyBackfillMaxRuns ||
		len(state.Runs) >= repositoryReviewLegacyBackfillMaxRuns {
		result.Exact = false
	}

	configuredRuns := make(map[string]struct{}, len(automation.RunIDs))
	for _, runID := range automation.RunIDs {
		runID = strings.TrimSpace(runID)
		if runID == "" {
			result.Exact = false
			continue
		}
		if _, duplicate := configuredRuns[runID]; duplicate {
			result.Exact = false
		}
		configuredRuns[runID] = struct{}{}
	}
	currentRuns := make([]repoaudit.ReviewRun, 0, len(configuredRuns))
	nonLedgerNoopPlans := make([]repoaudit.Plan, 0)
	sourceBytes := 0
	manifestRefs := 0
	seenLedgerRuns := make(map[string]struct{}, len(state.Runs))
	currentLedgerRuns := make(map[string]struct{}, len(configuredRuns))
	for _, run := range state.Runs {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if run.ID == "" {
			continue
		}
		if _, duplicate := seenLedgerRuns[run.ID]; duplicate {
			result.Exact = false
			continue
		}
		seenLedgerRuns[run.ID] = struct{}{}
		if _, configured := configuredRuns[run.ID]; !configured ||
			!automation.StartedAt.IsZero() && run.CompletedAt.Before(automation.StartedAt) {
			continue
		}
		if run.CampaignID != "" && run.CampaignID != campaignID {
			result.Exact = false
			continue
		}
		currentRuns = append(currentRuns, run)
		currentLedgerRuns[run.ID] = struct{}{}
	}
	// A configured workflow with a successful durable record but no retained
	// ledger ReviewRun proves history truncation. Zero-file/no-op workflows do
	// not, because they intentionally have no ReviewRun.
	for runID := range configuredRuns {
		if _, retained := currentLedgerRuns[runID]; retained {
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		workflowRun, loadErr := repositoryReviewLoadLegacyWorkflowRun(ctx, runStore, runID)
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if errors.Is(loadErr, context.Canceled) || errors.Is(loadErr, context.DeadlineExceeded) {
			return result, loadErr
		}
		if loadErr != nil || workflowRun == nil {
			result.Exact = false
			continue
		}
		encodedRun, encodeErr := json.Marshal(workflowRun)
		if encodeErr != nil {
			return result, encodeErr
		}
		if len(encodedRun) > repositoryReviewLegacyBackfillMaxRunBytes ||
			sourceBytes+len(encodedRun) > repositoryReviewLegacyBackfillMaxSourceBytes {
			result.Exact = false
			return result, nil
		}
		sourceBytes += len(encodedRun)
		if !automation.StartedAt.IsZero() && workflowRun.CreatedAt.Before(automation.StartedAt) {
			continue
		}
		recordStep := repositoryReviewRunStep(workflowRun, "record")
		if recordStep.Status == workflows.RunStatusSucceeded && strings.TrimSpace(recordStep.Error) == "" &&
			recordStep.Outputs["run"] != nil {
			result.Exact = false
			continue
		}
		allowed, noopPlan := repositoryReviewLegacyNonLedgerRunAllowed(workflowRun)
		if !allowed {
			result.Exact = false
		} else if noopPlan != nil {
			nonLedgerNoopPlans = append(nonLedgerNoopPlans, *noopPlan)
		}
	}
	sort.Slice(currentRuns, func(i, j int) bool {
		if currentRuns[i].CompletedAt.Equal(currentRuns[j].CompletedAt) {
			return currentRuns[i].ID < currentRuns[j].ID
		}
		return currentRuns[i].CompletedAt.Before(currentRuns[j].CompletedAt)
	})
	result.ExpectedRuns = len(currentRuns)
	if len(currentRuns) == 0 {
		result.Exact = false
		return result, nil
	}

	contextsByID := make(map[string]repoaudit.FindingContext, len(state.Contexts))
	contextsByRun := make(map[string][]repoaudit.FindingContext)
	for _, contextRecord := range state.Contexts {
		if contextRecord.ID == "" {
			continue
		}
		if _, duplicate := contextsByID[contextRecord.ID]; duplicate {
			result.Exact = false
			continue
		}
		contextsByID[contextRecord.ID] = contextRecord
		contextsByRun[contextRecord.RunID] = append(contextsByRun[contextRecord.RunID], contextRecord)
	}
	findingsByID := make(map[string]repoaudit.Finding, len(state.Findings))
	for _, finding := range state.Findings {
		if finding.ID == "" {
			continue
		}
		if _, duplicate := findingsByID[finding.ID]; duplicate {
			result.Exact = false
			continue
		}
		findingsByID[finding.ID] = finding
	}

	selectedByPath := make(map[string]repoaudit.FileRef)
	var commitSHA, inventoryHash string
	requiredAssignmentHint, assignmentErr := workflows.RepositoryBugFinderRequiredAssignments(
		resolvedProfile.ReviewerModels, resolvedProfile.IncludeDefaultReviewer,
	)
	if assignmentErr != nil {
		result.Exact = false
		return result, assignmentErr
	}
	evidenceRecords := make([]repositoryReviewLegacyRunEvidenceRecord, 0, len(currentRuns))
	anchorIndex := -1
	anchorMatched := false
	var anchorCatalog any
	fullCatalogDigest := ""
	recoveredRuns := make([]repositoryReviewLegacyRecoveredRun, 0, len(currentRuns))
	recoveredPlans := make(map[string]repoaudit.Plan, len(currentRuns))
	recoveredManifests := make(map[string]map[string]repoaudit.FileRef, len(currentRuns))
	recoveredEvidence := make(map[string]workflows.RepositoryReviewManagedEvidence, len(currentRuns))

	for _, ledgerRun := range currentRuns {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		workflowRun, loadErr := repositoryReviewLoadLegacyWorkflowRun(ctx, runStore, ledgerRun.ID)
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if errors.Is(loadErr, context.Canceled) || errors.Is(loadErr, context.DeadlineExceeded) {
			return result, loadErr
		}
		if loadErr != nil || workflowRun == nil {
			result.Exact = false
			continue
		}
		encodedRun, encodeErr := json.Marshal(workflowRun)
		if encodeErr != nil {
			return result, encodeErr
		}
		if len(encodedRun) > repositoryReviewLegacyBackfillMaxRunBytes ||
			sourceBytes+len(encodedRun) > repositoryReviewLegacyBackfillMaxSourceBytes {
			result.Exact = false
			return result, nil
		}
		sourceBytes += len(encodedRun)
		plan, evidence, valid := repositoryReviewLegacyRunEvidence(
			workflowRun, ledgerRun, state.Repository,
		)
		if !valid {
			result.Exact = false
			continue
		}
		manifest, manifestErr := repositoryReviewLegacyPlanManifest(plan)
		if manifestErr != nil {
			result.Exact = false
			continue
		}
		manifestRefs += len(manifest)
		if manifestRefs > repositoryReviewLegacyBackfillMaxManifestRefs {
			result.Exact = false
			return result, nil
		}
		fullCatalog, scopePlanHash, scopeValid := repositoryReviewLegacyScopeEvidence(workflowRun, manifest)
		if !scopeValid {
			result.Exact = false
			continue
		}
		catalogDigest, digestErr := repositoryReviewLegacyCatalogDigest(fullCatalog)
		if digestErr != nil {
			result.Exact = false
			continue
		}
		if fullCatalogDigest == "" {
			fullCatalogDigest = catalogDigest
		} else if catalogDigest != fullCatalogDigest {
			result.Exact = false
			return result, nil
		}
		if commitSHA == "" {
			commitSHA = plan.CommitSHA
			inventoryHash = plan.InventoryHash
		} else if plan.CommitSHA != commitSHA || plan.InventoryHash != inventoryHash {
			result.Exact = false
			continue
		}
		unionValid := true
		for _, file := range manifest {
			if previous, exists := selectedByPath[file.Path]; exists && previous != file {
				unionValid = false
				break
			}
			selectedByPath[file.Path] = file
		}
		if !unionValid {
			result.Exact = false
			continue
		}
		evidenceRecords = append(evidenceRecords, repositoryReviewLegacyRunEvidenceRecord{
			ledgerRun: ledgerRun, plan: plan, evidence: evidence, manifest: manifest,
			scopePlanHash: scopePlanHash, workflowUpdated: workflowRun.UpdatedAt,
		})
		if scopePlanHash == automation.ScopePlan.Hash {
			anchorIndex = len(evidenceRecords) - 1
			anchorMatched = true
			anchorCatalog = fullCatalog
		} else if !anchorMatched && (anchorIndex < 0 ||
			workflowRun.UpdatedAt.After(evidenceRecords[anchorIndex].workflowUpdated)) {
			anchorIndex = len(evidenceRecords) - 1
			anchorCatalog = fullCatalog
		}
	}

	if len(evidenceRecords) == 0 || anchorIndex < 0 || commitSHA == "" || inventoryHash == "" {
		result.Exact = false
		return result, nil
	}
	for _, noopPlan := range nonLedgerNoopPlans {
		manifest, manifestErr := repositoryReviewLegacyPlanManifest(noopPlan)
		if manifestErr != nil {
			return result, manifestErr
		}
		if noopPlan.Repository != state.Repository ||
			noopPlan.CommitSHA != commitSHA || noopPlan.InventoryHash != inventoryHash {
			result.Exact = false
			return result, nil
		}
		for _, file := range manifest {
			if selectedByPath[file.Path] != file {
				result.Exact = false
				return result, nil
			}
		}
	}
	selectedScope := make([]repoaudit.FileRef, 0, len(selectedByPath))
	selectedPaths := make([]string, 0, len(selectedByPath))
	for _, file := range selectedByPath {
		selectedScope = append(selectedScope, file)
		selectedPaths = append(selectedPaths, file.Path)
	}
	var err error
	selectedScope, err = repoaudit.CanonicalRepositoryReviewCampaignScope(selectedScope)
	if err != nil {
		result.Exact = false
		return result, err
	}
	scopePolicyJSON, err := json.Marshal(automation.ScopePolicy)
	if err != nil {
		result.Exact = false
		return result, err
	}
	scopeSelection, scopePlan, err := workflows.RecoverRepositoryReviewFrozenScope(
		anchorCatalog,
		string(scopePolicyJSON),
		commitSHA,
		inventoryHash,
		selectedPaths,
	)
	if err != nil {
		result.Exact = false
		return result, err
	}
	scopeSelection, err = repoaudit.NormalizeRepositoryReviewScopeSelection(scopeSelection)
	if err != nil {
		result.Exact = false
		return result, err
	}
	scopePlan, err = repoaudit.NormalizeRepositoryReviewScopePlan(scopePlan)
	if err != nil {
		result.Exact = false
		return result, err
	}
	profileHash, err := repositoryReviewLegacyProfileHash(automation, scopePlan.Hash, resolvedProfile)
	if err != nil {
		result.Exact = false
		return result, err
	}
	scopeDigest, err := repoaudit.RepositoryReviewCampaignScopeDigest(selectedScope)
	if err != nil {
		result.Exact = false
		return result, err
	}
	result.ScopeSelection = scopeSelection
	result.ScopePlan = scopePlan
	coveragePaths := make(map[string]repoaudit.RepositoryReviewCampaignPathCoverage)
	requiredAssignments := requiredAssignmentHint
	recoverableRunIDs := make(map[string]struct{}, len(evidenceRecords))
	for _, record := range evidenceRecords {
		recoverableRunIDs[record.ledgerRun.ID] = struct{}{}
	}

	for _, record := range evidenceRecords {
		ledgerRun, plan, evidence := record.ledgerRun, record.plan, record.evidence
		historicalProfileHash, historicalProfileErr := repositoryReviewHistoricalProfileHash(
			automation, record.scopePlanHash, resolvedProfile,
		)
		if historicalProfileErr != nil {
			return result, historicalProfileErr
		}
		profileCompatible := plan.ProfileHash == historicalProfileHash
		if profileCompatible {
			for _, file := range plan.UnchangedFiles {
				checkpoint, exists := state.Files[file.Path]
				_, currentRun := recoverableRunIDs[checkpoint.RunID]
				if !exists || !currentRun || checkpoint.FileRef != file ||
					checkpoint.ProfileHash != plan.ProfileHash {
					result.Exact = false
					continue
				}
				repositoryReviewMergeLegacyCoverage(
					coveragePaths, selectedByPath, file,
					repoaudit.RepositoryReviewCampaignPathCoverage{Completed: true}, &result.Exact,
				)
			}
		}

		for _, file := range evidence.InspectedFiles {
			repositoryReviewMergeLegacyCoverage(
				coveragePaths, selectedByPath, file,
				repoaudit.RepositoryReviewCampaignPathCoverage{Inspected: true}, &result.Exact,
			)
		}
		if profileCompatible {
			for _, file := range evidence.CompletedFiles {
				repositoryReviewMergeLegacyCoverage(
					coveragePaths, selectedByPath, file,
					repoaudit.RepositoryReviewCampaignPathCoverage{Inspected: true, Completed: true},
					&result.Exact,
				)
			}
		}
		ledgerUnsupported := make(map[string]repoaudit.FileRef, len(ledgerRun.UnsupportedPaths))
		for _, pathValue := range ledgerRun.UnsupportedPaths {
			if file, exists := selectedByPath[pathValue]; exists {
				ledgerUnsupported[pathValue] = file
			}
		}
		unsupportedEvidenceValid := true
		for _, unsupported := range evidence.UnsupportedFiles {
			if ledgerUnsupported[unsupported.Path] != unsupported.FileRef {
				unsupportedEvidenceValid = false
				break
			}
			if profileCompatible {
				repositoryReviewMergeLegacyCoverage(
					coveragePaths, selectedByPath, unsupported.FileRef,
					repoaudit.RepositoryReviewCampaignPathCoverage{Unsupported: true}, &result.Exact,
				)
			}
		}
		if !unsupportedEvidenceValid {
			result.Exact = false
			continue
		}
		unsupportedSeen := make(map[string]struct{}, len(ledgerRun.UnsupportedPaths))
		unsupportedPathsValid := true
		for _, pathValue := range ledgerRun.UnsupportedPaths {
			if _, duplicate := unsupportedSeen[pathValue]; duplicate {
				unsupportedPathsValid = false
				break
			}
			file, exists := selectedByPath[pathValue]
			if !exists {
				result.Exact = false
				continue
			}
			unsupportedSeen[pathValue] = struct{}{}
			if profileCompatible {
				repositoryReviewMergeLegacyCoverage(
					coveragePaths, selectedByPath, file,
					repoaudit.RepositoryReviewCampaignPathCoverage{Unsupported: true}, &result.Exact,
				)
			}
		}
		if !unsupportedPathsValid {
			result.Exact = false
			continue
		}
		completedSeen := make(map[string]struct{}, len(evidence.CompletedFiles))
		for _, file := range evidence.CompletedFiles {
			completedSeen[file.Path] = struct{}{}
		}
		expectedUnreviewedPaths := make([]string, 0, len(plan.PendingFiles))
		for _, file := range plan.PendingFiles {
			if _, completed := completedSeen[file.Path]; completed {
				continue
			}
			if _, unsupported := unsupportedSeen[file.Path]; unsupported {
				continue
			}
			expectedUnreviewedPaths = append(expectedUnreviewedPaths, file.Path)
		}
		sort.Strings(expectedUnreviewedPaths)
		expectedUnreviewed := len(expectedUnreviewedPaths)
		expectedModels := make([]string, 0, len(evidence.Observations))
		modelSeen := make(map[string]struct{}, len(evidence.Observations))
		for _, observation := range evidence.Observations {
			if _, exists := modelSeen[observation.Model]; !exists {
				modelSeen[observation.Model] = struct{}{}
				expectedModels = append(expectedModels, observation.Model)
			}
		}
		runValid := ledgerRun.UnsupportedCount == len(unsupportedSeen) &&
			ledgerRun.ReviewedFiles == len(evidence.CompletedFiles) &&
			ledgerRun.SkippedFiles == len(plan.UnchangedFiles) &&
			ledgerRun.UnreviewedFiles == expectedUnreviewed &&
			ledgerRun.RemainingFiles == len(plan.DeferredFiles)+expectedUnreviewed &&
			slices.Equal(ledgerRun.UnreviewedPaths, expectedUnreviewedPaths) &&
			slices.Equal(ledgerRun.Models, expectedModels) && ledgerRun.RejectedFindings == 0
		if !runValid {
			result.Exact = false
			continue
		}
		recovery := repoaudit.RepositoryReviewCampaignRunRecovery{
			ID: ledgerRun.ID, Plan: plan, InspectedFiles: len(evidence.InspectedFiles),
			LegacyRecovered: true,
		}
		if validationErr := repoaudit.ValidateRepositoryReviewCampaignRunRecovery(recovery); validationErr != nil {
			result.Exact = false
			continue
		}
		recoveredRuns = append(recoveredRuns, repositoryReviewLegacyRecoveredRun{
			recovery: recovery,
		})
		recoveredPlans[ledgerRun.ID] = plan
		recoveredEvidence[ledgerRun.ID] = evidence
		manifestIndex := make(map[string]repoaudit.FileRef, len(record.manifest))
		for _, file := range record.manifest {
			manifestIndex[file.Path] = file
		}
		recoveredManifests[ledgerRun.ID] = manifestIndex
	}
	contextIDs, findingIDs, unrecoveredFindingIDs, tagsExact := repositoryReviewLegacyGlobalTags(
		campaignID, state.Repository, commitSHA, inventoryHash,
		currentRuns, recoveredPlans, recoveredManifests, selectedByPath, coveragePaths,
		recoveredEvidence, contextsByID, contextsByRun, findingsByID,
	)
	if !tagsExact || len(recoveredRuns) == 0 {
		result.Exact = false
	} else {
		recoveredRuns[0].contextIDs = contextIDs
		recoveredRuns[0].findingIDs = findingIDs
	}
	result.RecoveredRuns = len(recoveredRuns)
	result.RecoveredContexts = len(contextIDs)
	result.RecoveredFindings = len(findingIDs)
	result.UnrecoveredFindingIDs = unrecoveredFindingIDs

	if len(selectedScope) == 0 || profileHash == "" || scopeDigest == "" || requiredAssignments < 1 {
		result.Exact = false
		return result, nil
	}
	if len(selectedScope) >= repositoryReviewLegacyBackfillMaxPaths ||
		len(coveragePaths) >= repositoryReviewLegacyBackfillMaxPaths ||
		len(recoveredRuns) != len(currentRuns) {
		result.Exact = false
	}
	if !result.Exact {
		return result, nil
	}
	request := repoaudit.ReconcileCampaignRequest{
		Repository: state.Repository, ExpectedReviewVersion: state.ReviewVersion,
		Coverage: repoaudit.RepositoryReviewCampaignCoverage{
			ID: campaignID, CommitSHA: commitSHA, InventoryHash: inventoryHash,
			ProfileHash: profileHash, ScopeDigest: scopeDigest,
			RequiredAssignments: requiredAssignments, SelectedFiles: len(selectedScope),
			Exact: result.Exact, Paths: coveragePaths,
		},
		SelectedScope: selectedScope,
	}
	baseBytes, marshalErr := json.Marshal(request)
	if marshalErr != nil {
		return result, marshalErr
	}
	if len(baseBytes) >= repositoryReviewLegacyBackfillMaxBytes {
		result.Exact = false
		return result, nil
	}
	estimatedBytes := len(baseBytes)
	for _, recovered := range recoveredRuns {
		candidateBytes, _ := json.Marshal(struct {
			Recovery   repoaudit.RepositoryReviewCampaignRunRecovery `json:"recovery"`
			ContextIDs []string                                      `json:"context_ids"`
			FindingIDs []string                                      `json:"finding_ids"`
		}{
			Recovery: recovered.recovery, ContextIDs: recovered.contextIDs,
			FindingIDs: recovered.findingIDs,
		})
		if estimatedBytes+len(candidateBytes)+1024 >= repositoryReviewLegacyBackfillMaxBytes {
			result.Exact = false
			request.Coverage.Exact = false
			continue
		}
		request.Runs = append(request.Runs, recovered.recovery)
		request.ContextIDs = append(request.ContextIDs, recovered.contextIDs...)
		request.FindingIDs = append(request.FindingIDs, recovered.findingIDs...)
		estimatedBytes += len(candidateBytes)
	}
	if len(request.Runs) != len(recoveredRuns) {
		result.Exact = false
		request.Coverage.Exact = false
	}
	sort.Strings(request.ContextIDs)
	sort.Strings(request.FindingIDs)
	encoded, marshalErr := json.Marshal(request)
	if marshalErr != nil {
		return result, marshalErr
	}
	if len(encoded) >= repositoryReviewLegacyBackfillMaxBytes {
		result.Exact = false
		return result, nil
	}
	if !result.Exact {
		return result, nil
	}
	result.Available = true
	result.Request = request
	for _, pathCoverage := range coveragePaths {
		if pathCoverage.Inspected {
			result.InspectedFiles++
		}
		if pathCoverage.Completed {
			result.CompletedFiles++
		}
		if pathCoverage.Unsupported {
			result.UnsupportedFiles++
		}
	}
	result.FindingOccurrences = len(request.FindingIDs)
	return result, nil
}

func repositoryReviewLegacyNonLedgerRunAllowed(run *workflows.Run) (bool, *repoaudit.Plan) {
	if run == nil || run.WorkflowRef != workflows.RepositoryBugFinderWorkflowRef || run.CompletedAt == nil {
		return false, nil
	}
	record := repositoryReviewRunStep(run, "record")
	if record.Status == workflows.RunStatusSucceeded && strings.TrimSpace(record.Error) == "" &&
		record.Outputs["run"] != nil {
		return false, nil
	}
	switch run.Status {
	case workflows.RunStatusFailed, workflows.RunStatusCanceled:
		review := repositoryReviewRunStep(run, "review")
		_, managedChildrenDeclared := review.Outputs["managed_children"]
		_, structuredDeclared := review.Outputs["structured"]
		allowed := review.Status != workflows.RunStatusSucceeded &&
			!managedChildrenDeclared && !structuredDeclared &&
			repositoryReviewRunStep(run, "scope").Status != workflows.RunStatusSucceeded &&
			repositoryReviewRunStep(run, "plan").Status != workflows.RunStatusSucceeded
		return allowed, nil
	case workflows.RunStatusSucceeded:
		plan := repositoryReviewRunStep(run, "plan")
		result := repositoryReviewRunStep(run, "result")
		pending, pendingOK := repositoryReviewNonnegativeInt(plan.Outputs["pendingCount"])
		if !pendingOK {
			pending, pendingOK = repositoryReviewNonnegativeInt(plan.Outputs["pending_count"])
		}
		remaining, remainingOK := repositoryReviewDurableResultRemainingFiles(run)
		directRemaining, directFound, directConflict := repositoryReviewOutputNonnegativeIntDetailed(
			result.Outputs, "remainingFiles", "remaining_files",
		)
		if directConflict || directFound && (!remainingOK || directRemaining != remaining) {
			return false, nil
		}
		allowed := plan.Status == workflows.RunStatusSucceeded && strings.TrimSpace(plan.Error) == "" &&
			result.Status == workflows.RunStatusSucceeded && strings.TrimSpace(result.Error) == "" &&
			pendingOK && pending == 0 && remainingOK && remaining == 0
		if !allowed {
			return false, nil
		}
		var recoveredPlan repoaudit.Plan
		if !repositoryReviewDecodeValue(plan.Outputs["plan"], &recoveredPlan) {
			return false, nil
		}
		if len(recoveredPlan.PendingFiles) != 0 || len(recoveredPlan.DeferredFiles) != 0 ||
			repoaudit.ValidateRepositoryReviewCampaignRunRecovery(
				repoaudit.RepositoryReviewCampaignRunRecovery{
					ID: run.ID, Plan: recoveredPlan, LegacyRecovered: true,
				},
			) != nil {
			return false, nil
		}
		return true, &recoveredPlan
	default:
		return false, nil
	}
}

func repositoryReviewLegacyScopeEvidence(
	run *workflows.Run,
	manifest []repoaudit.FileRef,
) (any, string, bool) {
	catalog := repositoryReviewRunStep(run, "full_scope_catalog")
	scope := repositoryReviewRunStep(run, "scope")
	if catalog.Status != workflows.RunStatusSucceeded || strings.TrimSpace(catalog.Error) != "" ||
		scope.Status != workflows.RunStatusSucceeded || strings.TrimSpace(scope.Error) != "" ||
		catalog.Outputs["candidates"] == nil {
		return nil, "", false
	}
	rawPlan, planFound := repositoryReviewOutputValue(scope.Outputs, "scopePlan", "scope_plan")
	rawPaths, pathsFound := repositoryReviewOutputValue(scope.Outputs, "selectedPaths", "selected_paths")
	var plan repoaudit.RepositoryReviewScopePlan
	var selectedPaths []string
	if !planFound || !pathsFound || !repositoryReviewDecodeValue(rawPlan, &plan) ||
		!repositoryReviewDecodeValue(rawPaths, &selectedPaths) || plan.Hash == "" {
		return nil, "", false
	}
	manifestPaths := make([]string, 0, len(manifest))
	for _, file := range manifest {
		manifestPaths = append(manifestPaths, file.Path)
	}
	sort.Strings(manifestPaths)
	sort.Strings(selectedPaths)
	if !slices.Equal(manifestPaths, selectedPaths) {
		return nil, "", false
	}
	return catalog.Outputs["candidates"], plan.Hash, true
}

func repositoryReviewLegacyCatalogDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > repositoryReviewLegacyBackfillMaxRunBytes {
		if err != nil {
			return "", err
		}
		return "", repoaudit.ErrInvalidPlan
	}
	digest := sha256.Sum256(encoded)
	return string(digest[:]), nil
}

func repositoryReviewLegacyProfileHash(
	automation repoaudit.RepositoryReviewAutomation,
	scopePlanHash string,
	resolved workflows.RepositoryReviewModelProfile,
) (string, error) {
	input, err := repositoryReviewLegacyProfileHashInput(automation, scopePlanHash, resolved)
	if err != nil {
		return "", err
	}
	return workflows.RepositoryBugFinderProfileHash(input)
}

func repositoryReviewHistoricalProfileHash(
	automation repoaudit.RepositoryReviewAutomation,
	scopePlanHash string,
	resolved workflows.RepositoryReviewModelProfile,
) (string, error) {
	input, err := repositoryReviewLegacyProfileHashInput(automation, scopePlanHash, resolved)
	if err != nil {
		return "", err
	}
	return workflows.RepositoryBugFinderLegacyResolvedProfileHash(input)
}

func repositoryReviewLegacyProfileHashInput(
	automation repoaudit.RepositoryReviewAutomation,
	scopePlanHash string,
	resolved workflows.RepositoryReviewModelProfile,
) (workflows.RepositoryBugFinderProfileHashInput, error) {
	scopePolicy, err := json.Marshal(automation.ScopePolicy)
	if err != nil {
		return workflows.RepositoryBugFinderProfileHashInput{}, err
	}
	effectiveMaxContentBytes, err := workflows.RepositoryBugFinderEffectiveMaxContentBytes(
		automation.MaxContentBytes, resolved.MaxContentBytes,
	)
	if err != nil {
		return workflows.RepositoryBugFinderProfileHashInput{}, err
	}
	input := workflows.NewRepositoryBugFinderProfileHashInput(
		resolved.AccountRef,
		automation.Target,
		automation.ReviewFocus,
		string(scopePolicy),
		scopePlanHash,
		strings.Join(repositoryReviewExecutionModels(automation), ","),
		resolved.Revision,
		resolved.ReviewerModels,
		resolved.IncludeDefaultReviewer,
		effectiveMaxContentBytes,
	)
	return input, nil
}

func repositoryReviewLegacyRunEvidence(
	run *workflows.Run,
	ledgerRun repoaudit.ReviewRun,
	repository string,
) (repoaudit.Plan, workflows.RepositoryReviewManagedEvidence, bool) {
	if run == nil || run.ID != ledgerRun.ID || run.WorkflowRef != workflows.RepositoryBugFinderWorkflowRef ||
		run.Status != workflows.RunStatusSucceeded || run.CompletedAt == nil {
		return repoaudit.Plan{}, workflows.RepositoryReviewManagedEvidence{}, false
	}
	planStep := repositoryReviewRunStep(run, "plan")
	reviewStep := repositoryReviewRunStep(run, "review")
	recordStep := repositoryReviewRunStep(run, "record")
	if planStep.Status != workflows.RunStatusSucceeded || strings.TrimSpace(planStep.Error) != "" ||
		recordStep.Status != workflows.RunStatusSucceeded || strings.TrimSpace(recordStep.Error) != "" {
		return repoaudit.Plan{}, workflows.RepositoryReviewManagedEvidence{}, false
	}
	var plan repoaudit.Plan
	if !repositoryReviewDecodeValue(planStep.Outputs["plan"], &plan) || plan.ID == "" ||
		plan.ID != ledgerRun.PlanID || plan.Repository != repository || !plan.Authoritative ||
		plan.CommitSHA != ledgerRun.CommitSHA || plan.InventoryHash != ledgerRun.InventoryHash ||
		strings.TrimSpace(plan.ProfileHash) == "" {
		return repoaudit.Plan{}, workflows.RepositoryReviewManagedEvidence{}, false
	}
	var outputReviewers []string
	outputAccount, accountDeclared := planStep.Outputs["accountRef"].(string)
	includeDefault, includeDefaultDeclared := planStep.Outputs["includeDefaultReviewer"].(bool)
	maxContentBytes, maxContentOK := repositoryReviewLegacyPositiveContentBytes(
		planStep.Outputs["maxContentBytes"],
	)
	if !repositoryReviewDecodeValue(planStep.Outputs["reviewerModels"], &outputReviewers) ||
		!accountDeclared || strings.TrimSpace(outputAccount) != outputAccount ||
		!repositoryReviewLegacyModelsCanonical(outputReviewers) ||
		!includeDefaultDeclared || !maxContentOK || maxContentBytes < 1 {
		return repoaudit.Plan{}, workflows.RepositoryReviewManagedEvidence{}, false
	}
	requiredAssignments, assignmentErr := workflows.RepositoryBugFinderRequiredAssignments(
		outputReviewers, includeDefault,
	)
	if assignmentErr != nil {
		return repoaudit.Plan{}, workflows.RepositoryReviewManagedEvidence{}, false
	}
	if err := repoaudit.ValidateRepositoryReviewCampaignRunRecovery(
		repoaudit.RepositoryReviewCampaignRunRecovery{
			ID: ledgerRun.ID, Plan: plan, LegacyRecovered: true,
		},
	); err != nil {
		return repoaudit.Plan{}, workflows.RepositoryReviewManagedEvidence{}, false
	}
	var recorded repoaudit.ReviewRun
	if !repositoryReviewDecodeValue(recordStep.Outputs["run"], &recorded) ||
		!repositoryReviewLegacyRunOutputMatches(ledgerRun, recorded) {
		return repoaudit.Plan{}, workflows.RepositoryReviewManagedEvidence{}, false
	}
	pendingByPath := make(map[string]repoaudit.FileRef, len(plan.PendingFiles))
	for _, file := range plan.PendingFiles {
		pendingByPath[file.Path] = file
	}
	terminalUnsupported := make([]repoaudit.FileRef, 0, len(ledgerRun.UnsupportedPaths))
	for _, pathValue := range ledgerRun.UnsupportedPaths {
		file, exists := pendingByPath[pathValue]
		if !exists {
			return repoaudit.Plan{}, workflows.RepositoryReviewManagedEvidence{}, false
		}
		terminalUnsupported = append(terminalUnsupported, file)
	}
	managedChildren, declared := reviewStep.Outputs["managed_children"]
	if reviewStep.Status == workflows.RunStatusSucceeded {
		if !declared && len(terminalUnsupported) != len(plan.PendingFiles) {
			return repoaudit.Plan{}, workflows.RepositoryReviewManagedEvidence{}, false
		}
	} else if reviewStep.Status != workflows.RunStatusSkipped ||
		len(terminalUnsupported) != len(plan.PendingFiles) {
		return repoaudit.Plan{}, workflows.RepositoryReviewManagedEvidence{}, false
	}
	assignmentHint := 0
	if len(terminalUnsupported) == len(plan.PendingFiles) {
		assignmentHint = requiredAssignments
	}
	evidence, err := workflows.DecodeRepositoryReviewManagedEvidence(
		managedChildren, plan, workflows.RepositoryReviewManagedEvidenceOptions{
			TerminalUnsupportedFiles: terminalUnsupported,
			RequiredAssignments:      assignmentHint,
			AllowLegacyCoreFindings:  true,
		},
	)
	if err != nil {
		return repoaudit.Plan{}, workflows.RepositoryReviewManagedEvidence{}, false
	}
	return plan, evidence, true
}

func repositoryReviewLegacyModelsCanonical(models []string) bool {
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model == "" || model != strings.TrimSpace(model) || len(model) > 256 {
			return false
		}
		if _, duplicate := seen[model]; duplicate {
			return false
		}
		seen[model] = struct{}{}
	}
	return true
}

func repositoryReviewLegacyPositiveContentBytes(value any) (int64, bool) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0, false
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	var decoded any
	if decoder.Decode(&decoded) != nil {
		return 0, false
	}
	number, ok := decoded.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := number.Int64()
	return parsed, err == nil && parsed > 0 && parsed <= 4<<20
}

func repositoryReviewLegacyRunOutputMatches(left, right repoaudit.ReviewRun) bool {
	left.CampaignID, right.CampaignID = "", ""
	left.ProfileHash, right.ProfileHash = "", ""
	left.ScopeDigest, right.ScopeDigest = "", ""
	left.InspectedFiles, right.InspectedFiles = 0, 0
	left.LegacyRecovered, right.LegacyRecovered = false, false
	return reflect.DeepEqual(left, right)
}

func repositoryReviewLegacyPlanManifest(plan repoaudit.Plan) ([]repoaudit.FileRef, error) {
	files := make([]repoaudit.FileRef, 0,
		len(plan.PendingFiles)+len(plan.DeferredFiles)+len(plan.UnchangedFiles)+len(plan.UnsupportedFiles),
	)
	files = append(files, plan.PendingFiles...)
	files = append(files, plan.DeferredFiles...)
	files = append(files, plan.UnchangedFiles...)
	for _, unsupported := range plan.UnsupportedFiles {
		files = append(files, unsupported.FileRef)
	}
	return repoaudit.CanonicalRepositoryReviewCampaignScope(files)
}

func repositoryReviewMergeLegacyCoverage(
	coverage map[string]repoaudit.RepositoryReviewCampaignPathCoverage,
	selected map[string]repoaudit.FileRef,
	file repoaudit.FileRef,
	update repoaudit.RepositoryReviewCampaignPathCoverage,
	exact *bool,
) {
	trusted, exists := selected[file.Path]
	if !exists || trusted != file {
		*exact = false
		return
	}
	current := coverage[file.Path]
	if current.Unsupported && (update.Inspected || update.Completed) ||
		update.Unsupported && (current.Inspected || current.Completed) {
		delete(coverage, file.Path)
		*exact = false
		return
	}
	coverage[file.Path] = repoaudit.RepositoryReviewCampaignPathCoverage{
		Inspected:   current.Inspected || update.Inspected,
		Completed:   current.Completed || update.Completed,
		Unsupported: current.Unsupported || update.Unsupported,
	}
}

func repositoryReviewLegacyGlobalTags(
	campaignID string,
	repository string,
	commitSHA string,
	inventoryHash string,
	runs []repoaudit.ReviewRun,
	plans map[string]repoaudit.Plan,
	manifests map[string]map[string]repoaudit.FileRef,
	selected map[string]repoaudit.FileRef,
	coverage map[string]repoaudit.RepositoryReviewCampaignPathCoverage,
	evidenceByRun map[string]workflows.RepositoryReviewManagedEvidence,
	contextsByID map[string]repoaudit.FindingContext,
	contextsByRun map[string][]repoaudit.FindingContext,
	findingsByID map[string]repoaudit.Finding,
) ([]string, []string, []string, bool) {
	exact := true
	unrecovered := make([]string, 0)
	runsByID := make(map[string]repoaudit.ReviewRun, len(runs))
	findingRunRefs := make(map[string]map[string]struct{})
	for _, run := range runs {
		runsByID[run.ID] = run
		if _, recovered := plans[run.ID]; !recovered {
			exact = false
			continue
		}
		seen := make(map[string]struct{}, len(run.FindingIDs))
		for _, findingID := range run.FindingIDs {
			if _, duplicate := seen[findingID]; duplicate || strings.TrimSpace(findingID) == "" {
				exact = false
				continue
			}
			seen[findingID] = struct{}{}
			if findingRunRefs[findingID] == nil {
				findingRunRefs[findingID] = make(map[string]struct{})
			}
			findingRunRefs[findingID][run.ID] = struct{}{}
		}
	}
	// Some legacy checkpoints omitted the run-level finding projection. A
	// retained context bound to a recovered run is the compatibility authority.
	for findingID, finding := range findingsByID {
		for _, contextID := range finding.ContextIDs {
			contextRecord, exists := contextsByID[contextID]
			if !exists {
				continue
			}
			if _, recovered := plans[contextRecord.RunID]; !recovered {
				continue
			}
			if findingRunRefs[findingID] == nil {
				findingRunRefs[findingID] = make(map[string]struct{})
			}
			findingRunRefs[findingID][contextRecord.RunID] = struct{}{}
		}
	}
	for _, run := range runs {
		count := 0
		for _, referencedRuns := range findingRunRefs {
			if _, referenced := referencedRuns[run.ID]; referenced {
				count++
			}
		}
		if count != run.AcceptedFindings {
			exact = false
		}
	}
	findingIDs := make([]string, 0, len(findingRunRefs))
	contextSet := make(map[string]struct{})
	for findingID, referencedRuns := range findingRunRefs {
		finding, exists := findingsByID[findingID]
		if !exists || !repositoryReviewLegacyFindingProjectionValid(finding) ||
			finding.Repository != repository || finding.CommitSHA != commitSHA ||
			(finding.CampaignID != "" && finding.CampaignID != campaignID) ||
			selected[finding.File.Path] != finding.File || !coverage[finding.File.Path].Inspected ||
			len(finding.ContextIDs) == 0 {
			exact = false
			unrecovered = append(unrecovered, findingID)
			continue
		}
		validFinding := true
		contextRuns := make(map[string]struct{}, len(finding.ContextIDs))
		matchedCandidates := make(map[string]repoaudit.FindingCandidate, len(finding.ContextIDs))
		for _, contextID := range finding.ContextIDs {
			contextRecord, found := contextsByID[contextID]
			plan, recovered := plans[contextRecord.RunID]
			manifest := manifests[contextRecord.RunID]
			if !found || !recovered || contextRecord.Repository != repository ||
				contextRecord.CommitSHA != commitSHA || contextRecord.InventoryHash != inventoryHash ||
				contextRecord.ProfileHash != plan.ProfileHash ||
				(contextRecord.CampaignID != "" && contextRecord.CampaignID != campaignID) ||
				!repoaudit.ValidateRepositoryReviewLegacyContextIdentity(contextRecord) ||
				!repositoryReviewLegacyContextMatchesScope(contextRecord, finding.File, manifest) ||
				!repositoryReviewLegacyContextHasEvidence(
					contextRecord, runsByID[contextRecord.RunID], evidenceByRun[contextRecord.RunID],
				) {
				validFinding = false
				break
			}
			matchedCandidate, candidateFound := repositoryReviewLegacyFindingEvidence(
				finding, contextRecord, evidenceByRun[contextRecord.RunID],
			)
			if !candidateFound {
				validFinding = false
				break
			}
			matchedCandidates[contextID] = matchedCandidate
			contextSet[contextID] = struct{}{}
			contextRuns[contextRecord.RunID] = struct{}{}
		}
		for runID := range referencedRuns {
			if _, represented := contextRuns[runID]; !represented {
				validFinding = false
			}
		}
		if validFinding {
			originObservation := finding.Observations[0]
			originContext, originFound := contextsByID[originObservation.ContextID]
			originCandidate, candidateFound := matchedCandidates[originObservation.ContextID]
			severity := ""
			for _, candidate := range matchedCandidates {
				severity = repositoryReviewLegacyMoreSevere(severity, candidate.Severity)
			}
			if !originFound || !candidateFound || severity != finding.Severity ||
				!repoaudit.ValidateRepositoryReviewLegacyFindingIdentity(
					finding, originContext, originCandidate,
				) {
				validFinding = false
			}
		}
		if !validFinding {
			exact = false
			unrecovered = append(unrecovered, findingID)
			continue
		}
		findingIDs = append(findingIDs, findingID)
	}
	for runID := range plans {
		for _, contextRecord := range contextsByRun[runID] {
			if _, selectedContext := contextSet[contextRecord.ID]; !selectedContext {
				exact = false
			}
		}
	}
	for findingID, finding := range findingsByID {
		if _, selectedFinding := findingRunRefs[findingID]; selectedFinding {
			continue
		}
		for _, contextID := range finding.ContextIDs {
			if contextRecord, exists := contextsByID[contextID]; exists {
				if _, recovered := plans[contextRecord.RunID]; recovered {
					exact = false
				}
			}
		}
	}
	contextIDs := make([]string, 0, len(contextSet))
	for contextID := range contextSet {
		contextIDs = append(contextIDs, contextID)
	}
	sort.Strings(contextIDs)
	sort.Strings(findingIDs)
	sort.Strings(unrecovered)
	return contextIDs, findingIDs, unrecovered, exact
}

func repositoryReviewLegacyFindingProjectionValid(finding repoaudit.Finding) bool {
	if finding.ObservationCount != len(finding.Observations) || len(finding.ContextIDs) == 0 {
		return false
	}
	contextSet := make(map[string]struct{}, len(finding.ContextIDs))
	for _, contextID := range finding.ContextIDs {
		if contextID == "" {
			return false
		}
		if _, duplicate := contextSet[contextID]; duplicate {
			return false
		}
		contextSet[contextID] = struct{}{}
	}
	observationContexts := make(map[string]struct{}, len(finding.Observations))
	modelSet := make(map[string]struct{})
	for _, observation := range finding.Observations {
		if _, exists := contextSet[observation.ContextID]; !exists || observation.Model == "" {
			return false
		}
		if _, duplicate := observationContexts[observation.ContextID]; duplicate {
			return false
		}
		observationContexts[observation.ContextID] = struct{}{}
		modelSet[observation.Model] = struct{}{}
	}
	if len(observationContexts) != len(contextSet) {
		return false
	}
	models := append([]string(nil), finding.Models...)
	wantModels := make([]string, 0, len(modelSet))
	for model := range modelSet {
		wantModels = append(wantModels, model)
	}
	sort.Strings(models)
	sort.Strings(wantModels)
	return slices.Equal(models, wantModels)
}

func repositoryReviewLegacyContextMatchesScope(
	contextRecord repoaudit.FindingContext,
	primary repoaudit.FileRef,
	selected map[string]repoaudit.FileRef,
) bool {
	if len(contextRecord.Files) == 0 {
		return false
	}
	containsPrimary := false
	for _, file := range contextRecord.Files {
		if selected[file.Path] != file {
			return false
		}
		containsPrimary = containsPrimary || file == primary
	}
	return containsPrimary
}

func repositoryReviewLegacyContextHasEvidence(
	contextRecord repoaudit.FindingContext,
	run repoaudit.ReviewRun,
	evidence workflows.RepositoryReviewManagedEvidence,
) bool {
	if !contextRecord.CreatedAt.Equal(run.CompletedAt) {
		return false
	}
	contextFiles, err := repoaudit.CanonicalRepositoryReviewCampaignScope(contextRecord.Files)
	if err != nil {
		return false
	}
	for _, observation := range evidence.Observations {
		observationFiles, canonicalErr := repoaudit.CanonicalRepositoryReviewCampaignScope(
			observation.ScopeFiles,
		)
		if observation.Model == contextRecord.Model && observation.Reviewer == contextRecord.Reviewer &&
			observation.RawDigest == contextRecord.RawDigest &&
			canonicalErr == nil && reflect.DeepEqual(observationFiles, contextFiles) {
			return true
		}
	}
	return false
}

func repositoryReviewLegacyFindingEvidence(
	finding repoaudit.Finding,
	contextRecord repoaudit.FindingContext,
	evidence workflows.RepositoryReviewManagedEvidence,
) (repoaudit.FindingCandidate, bool) {
	contextFiles, err := repoaudit.CanonicalRepositoryReviewCampaignScope(contextRecord.Files)
	if err != nil {
		return repoaudit.FindingCandidate{}, false
	}
	for _, observation := range evidence.Observations {
		observationFiles, canonicalErr := repoaudit.CanonicalRepositoryReviewCampaignScope(
			observation.ScopeFiles,
		)
		if observation.Model != contextRecord.Model || observation.Reviewer != contextRecord.Reviewer ||
			observation.RawDigest != contextRecord.RawDigest ||
			canonicalErr != nil || !reflect.DeepEqual(observationFiles, contextFiles) {
			continue
		}
		for _, candidate := range observation.Findings {
			candidate = repoaudit.NormalizeRepositoryReviewFindingCandidate(candidate)
			if candidate.File != finding.File.Path {
				continue
			}
			for _, occurrence := range finding.Observations {
				if occurrence.ContextID == contextRecord.ID &&
					occurrence.Model == contextRecord.Model &&
					occurrence.Reviewer == contextRecord.Reviewer &&
					repositoryReviewLegacyOccurrenceMatchesCandidate(occurrence, candidate) {
					return candidate, true
				}
			}
		}
	}
	return repoaudit.FindingCandidate{}, false
}

func repositoryReviewLegacyMoreSevere(left, right string) string {
	rank := map[string]int{"low": 1, "medium": 2, "high": 3, "critical": 4}
	if rank[right] > rank[left] {
		return right
	}
	return left
}

func repositoryReviewLegacyOccurrenceMatchesCandidate(
	occurrence repoaudit.FindingObservation,
	candidate repoaudit.FindingCandidate,
) bool {
	return occurrence.Severity == candidate.Severity && occurrence.Title == candidate.Title &&
		occurrence.Symbol == candidate.Symbol && reflect.DeepEqual(occurrence.Line, candidate.Line) &&
		occurrence.Message == candidate.Message && occurrence.Evidence == candidate.Evidence &&
		occurrence.Impact == candidate.Impact &&
		reflect.DeepEqual(occurrence.Validation, candidate.Validation) &&
		reflect.DeepEqual(occurrence.MatchHints, candidate.MatchHints) &&
		reflect.DeepEqual(occurrence.FixEffort, candidate.FixEffort)
}

func installRepositoryReviewLegacyCampaignAuthority(
	ctx context.Context,
	store repoaudit.Store,
	prepared repositoryReviewLegacyCampaignBackfill,
) (repoaudit.RepositoryReviewAutomation, repositoryReviewLegacyCampaignBackfill, error) {
	if err := ctx.Err(); err != nil {
		return repoaudit.RepositoryReviewAutomation{}, prepared, err
	}
	if !prepared.Available || !prepared.Exact || prepared.AutomationID == "" ||
		prepared.AutomationVersion < 1 || prepared.Request.Coverage.ID == "" ||
		prepared.ScopePlan.Hash == "" {
		return repoaudit.RepositoryReviewAutomation{}, prepared, repoaudit.ErrInvalidPlan
	}
	current, found, err := store.GetAutomation(ctx, prepared.AutomationID)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, prepared, err
	}
	if !found {
		return repoaudit.RepositoryReviewAutomation{}, prepared, os.ErrNotExist
	}
	installed := current.CampaignID == prepared.Request.Coverage.ID &&
		current.ScopeSelection != nil &&
		repositoryReviewScopeSelectionsEqual(*current.ScopeSelection, prepared.ScopeSelection) &&
		repositoryReviewScopePlansEqual(current.ScopePlan, prepared.ScopePlan)
	if installed {
		if current.Version != prepared.AutomationVersion &&
			current.Version != prepared.AutomationVersion+1 {
			return repoaudit.RepositoryReviewAutomation{}, prepared, repoaudit.ErrConflict
		}
	} else {
		if current.Version != prepared.AutomationVersion || current.CampaignID != "" ||
			current.Status != prepared.AutomationStatus || current.ActiveRunID != "" ||
			current.Status == repoaudit.RepositoryReviewAutomationRunning ||
			current.Status == repoaudit.RepositoryReviewAutomationStopping {
			return repoaudit.RepositoryReviewAutomation{}, prepared, repoaudit.ErrConflict
		}
		selection := prepared.ScopeSelection
		current, err = store.UpdateAutomation(
			ctx, current.ID, current.Version,
			func(candidate *repoaudit.RepositoryReviewAutomation) error {
				if candidate.CampaignID != "" || candidate.Status != prepared.AutomationStatus ||
					candidate.ActiveRunID != "" {
					return repoaudit.ErrConflict
				}
				candidate.CampaignID = prepared.Request.Coverage.ID
				candidate.CampaignRecoveryPending = true
				candidate.ResolvedCommitSHA = prepared.Request.Coverage.CommitSHA
				candidate.ScopeSelection = &selection
				candidate.ScopePlan = prepared.ScopePlan
				return nil
			},
		)
		if err != nil {
			return repoaudit.RepositoryReviewAutomation{}, prepared, err
		}
	}
	prepared.AutomationVersion = current.Version
	prepared.AutomationStatus = current.Status
	return current, prepared, nil
}

// applyRepositoryReviewLegacyCampaignBackfill reconciles controller-installed
// authority and exact recovered coverage under review-version CAS. A retry
// with the same campaign ID and request is idempotent after a lost response.
func applyRepositoryReviewLegacyCampaignBackfill(
	ctx context.Context,
	store repoaudit.Store,
	prepared repositoryReviewLegacyCampaignBackfill,
) (repoaudit.RepositoryState, error) {
	if err := ctx.Err(); err != nil {
		return repoaudit.RepositoryState{}, err
	}
	if !prepared.Available || !prepared.Exact || !prepared.Request.Coverage.Exact ||
		prepared.Request.Repository == "" ||
		prepared.Request.Coverage.ID == "" || prepared.Request.Coverage.CommitSHA == "" ||
		prepared.Request.SelectedScope == nil || prepared.AutomationID == "" ||
		prepared.AutomationVersion < 1 {
		return repoaudit.RepositoryState{}, repoaudit.ErrInvalidPlan
	}
	automation, found, err := store.GetAutomation(ctx, prepared.AutomationID)
	if err != nil {
		return repoaudit.RepositoryState{}, err
	}
	if !found || automation.Version != prepared.AutomationVersion ||
		automation.Status != prepared.AutomationStatus ||
		automation.CampaignID != prepared.Request.Coverage.ID ||
		!automation.CampaignRecoveryPending || automation.ActiveRunID != "" ||
		automation.Status == repoaudit.RepositoryReviewAutomationRunning ||
		automation.Status == repoaudit.RepositoryReviewAutomationStopping {
		return repoaudit.RepositoryState{}, repoaudit.ErrConflict
	}
	begun, err := store.BeginCampaign(ctx, repoaudit.BeginCampaignRequest{
		Repository:            prepared.Request.Repository,
		CampaignID:            prepared.Request.Coverage.ID,
		CommitSHA:             prepared.Request.Coverage.CommitSHA,
		ExpectedReviewVersion: prepared.Request.ExpectedReviewVersion,
		Exact:                 false,
	})
	if err != nil {
		return repoaudit.RepositoryState{}, err
	}
	prepared.Request.ExpectedReviewVersion = begun.ReviewVersion
	return store.ReconcileCampaign(ctx, prepared.Request)
}

// recoverLegacyRepositoryReviewCampaign is the controller entry point usable
// by startup reconciliation and immediately before Resume. It never scans an
// HTTP request path; it atomically installs a new campaign ID and frozen scope
// on an inactive automation before reconciling the exact recovered ledger.
func (c *repositoryReviewController) recoverLegacyRepositoryReviewCampaign(
	ctx context.Context,
	store repoaudit.Store,
	workspace string,
	automation repoaudit.RepositoryReviewAutomation,
	resolvedCommit string,
	resolvedProfile workflows.RepositoryReviewModelProfile,
) (repoaudit.RepositoryReviewAutomation, error) {
	if err := ctx.Err(); err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	if strings.TrimSpace(workspace) == "" {
		return repoaudit.RepositoryReviewAutomation{}, repoaudit.ErrInvalidAutomation
	}
	resolvedCommit = strings.ToLower(strings.TrimSpace(resolvedCommit))
	if !repositoryReviewValidCommitSHA(resolvedCommit) {
		return repoaudit.RepositoryReviewAutomation{}, repoaudit.ErrInvalidAutomation
	}
	state, found, err := store.ResolveRepositoryState(automation.Repository, automation.RunIDs)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	if !found {
		return repoaudit.RepositoryReviewAutomation{}, os.ErrNotExist
	}
	if automation.CampaignID != "" && !automation.CampaignRecoveryPending {
		if state.CurrentCampaign != nil && state.CurrentCampaign.ID == automation.CampaignID &&
			state.CurrentCampaign.Exact && state.CurrentCampaign.CommitSHA == resolvedCommit &&
			automation.ResolvedCommitSHA == resolvedCommit {
			return automation, nil
		}
		return repoaudit.RepositoryReviewAutomation{}, repoaudit.ErrConflict
	}
	campaignID := automation.CampaignID
	if campaignID == "" {
		campaignID = repoaudit.NewRepositoryReviewCampaignID()
	}
	prepared, err := prepareRepositoryReviewLegacyCampaignBackfill(
		ctx, automation, state, campaignID, workflows.NewFileRunStore(workspace), resolvedProfile,
	)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	if !prepared.Available || prepared.Request.Coverage.CommitSHA != resolvedCommit {
		return repoaudit.RepositoryReviewAutomation{}, repoaudit.ErrConflict
	}
	installed, prepared, err := installRepositoryReviewLegacyCampaignAuthority(ctx, store, prepared)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	_, err = applyRepositoryReviewLegacyCampaignBackfill(ctx, store, prepared)
	if err != nil {
		return repoaudit.RepositoryReviewAutomation{}, err
	}
	final, err := store.UpdateAutomation(
		ctx, installed.ID, installed.Version,
		func(candidate *repoaudit.RepositoryReviewAutomation) error {
			if candidate.CampaignID != prepared.Request.Coverage.ID ||
				!candidate.CampaignRecoveryPending || candidate.ActiveRunID != "" {
				return repoaudit.ErrConflict
			}
			candidate.CampaignRecoveryPending = false
			return nil
		},
	)
	return final, err
}
