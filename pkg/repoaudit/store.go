package repoaudit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/fileutil"
)

const storeDirectory = "repository_reviews"

const (
	maxRepositoryIdentityBytes       = 4096
	maxReviewFiles                   = 100_000
	maxReviewObservations            = 100_000
	maxFindingsPerObservation        = 256
	maxFindingTextBytes              = 64 << 10
	maxIssueDraftBodyBytes           = 60 << 10
	maxReviewFileMetadataBytes       = 16 << 20
	maxStateFileBytes          int64 = 64 << 20
)

var (
	ErrConflict    = errors.New("repository review state changed")
	ErrInvalidPlan = errors.New("invalid repository review plan")
	storeLocks     sync.Map
)

type Store struct {
	root string
	now  func() time.Time
}

func NewStore(workspace string) Store {
	return Store{root: filepath.Join(workspace, storeDirectory), now: time.Now}
}

func (s Store) Plan(
	ctx context.Context,
	repository, commitSHA, inventoryHash string,
	files []FileRef,
	force bool,
) (Plan, error) {
	return s.PlanWithProfile(ctx, repository, commitSHA, inventoryHash, "repository-bug-finder-v1", files, force)
}

func (s Store) PlanWithProfile(
	ctx context.Context,
	repository, commitSHA, inventoryHash, profileHash string,
	files []FileRef,
	force bool,
) (Plan, error) {
	return s.PlanWithProfileLimit(
		ctx, repository, commitSHA, inventoryHash, profileHash, files, force, maxReviewFiles,
	)
}

func (s Store) PlanWithProfileLimit(
	ctx context.Context,
	repository, commitSHA, inventoryHash, profileHash string,
	files []FileRef,
	force bool,
	maximumPending int,
) (Plan, error) {
	return s.PlanWithProfileLimitAuthoritative(
		ctx, repository, commitSHA, inventoryHash, profileHash,
		files, force, maximumPending, false,
	)
}

func (s Store) PlanWithProfileLimitAuthoritative(
	ctx context.Context,
	repository, commitSHA, inventoryHash, profileHash string,
	files []FileRef,
	force bool,
	maximumPending int,
	authoritative bool,
) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	repository = strings.TrimSpace(repository)
	commitSHA = strings.TrimSpace(commitSHA)
	inventoryHash = strings.TrimSpace(inventoryHash)
	profileHash = strings.TrimSpace(profileHash)
	if !validBoundedText(repository, maxRepositoryIdentityBytes) ||
		!validBoundedText(commitSHA, 256) || !validBoundedText(inventoryHash, 256) ||
		!validBoundedText(profileHash, 256) {
		return Plan{}, fmt.Errorf("%w: repository, commit SHA, and inventory hash are required", ErrInvalidPlan)
	}
	files, err := normalizeFiles(files)
	if err != nil {
		return Plan{}, err
	}
	if len(files) > maxReviewFiles {
		return Plan{}, fmt.Errorf("%w: too many review files", ErrInvalidPlan)
	}
	if maximumPending < 1 || maximumPending > maxReviewFiles {
		return Plan{}, fmt.Errorf("%w: invalid pending-file limit", ErrInvalidPlan)
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return Plan{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return Plan{}, err
	}
	now := s.clock()
	forceCampaignID := ""
	if force {
		if state.ActiveForceCampaignID != "" &&
			state.ActiveForceProfileHash == profileHash &&
			state.ActiveForceCommitSHA == commitSHA {
			forceCampaignID = state.ActiveForceCampaignID
		} else {
			forceCampaignID = stableID(
				"rfc_", repository, commitSHA, profileHash,
				fmt.Sprint(state.ReviewVersion), fmt.Sprint(now.UnixNano()),
			)
		}
	}
	candidates := make([]FileRef, 0, len(files))
	unchanged := make([]FileRef, 0, len(files))
	planUnsupported := make([]UnsupportedFile, 0)
	previouslyReviewed := 0
	for _, file := range files {
		if unsupported, exists := state.Unsupported[file.Path]; exists &&
			unsupported.BlobSHA == file.BlobSHA && unsupported.SizeBytes == file.SizeBytes &&
			unsupported.Mode == file.Mode && unsupported.ProfileHash == profileHash &&
			(!force || unsupported.ForceCampaignID == forceCampaignID) {
			planUnsupported = append(planUnsupported, unsupported)
			continue
		}
		previous, reviewed := state.Files[file.Path]
		if reviewed {
			previouslyReviewed++
		}
		matchesBase := reviewed && previous.BlobSHA == file.BlobSHA &&
			previous.SizeBytes == file.SizeBytes && previous.Mode == file.Mode &&
			previous.ProfileHash == profileHash
		if matchesBase && (!force || previous.ForceCampaignID == forceCampaignID) {
			unchanged = append(unchanged, file)
			continue
		}
		candidates = append(candidates, file)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left := reviewAttemptsFor(state, candidates[i], profileHash)
		right := reviewAttemptsFor(state, candidates[j], profileHash)
		if left != right {
			return left < right
		}
		return candidates[i].Path < candidates[j].Path
	})
	pendingEnd := min(maximumPending, len(candidates))
	pending := append([]FileRef(nil), candidates[:pendingEnd]...)
	deferred := append([]FileRef(nil), candidates[pendingEnd:]...)
	plan := Plan{
		Repository: repository, CommitSHA: commitSHA, InventoryHash: inventoryHash,
		ProfileHash: profileHash, ForceCampaignID: forceCampaignID, Authoritative: authoritative,
		StateVersion: state.ReviewVersion, PendingFiles: pending, DeferredFiles: deferred,
		UnchangedFiles:     unchanged,
		UnsupportedFiles:   planUnsupported,
		PreviouslyReviewed: previouslyReviewed, CreatedAt: now,
	}
	plan.ID = planDigest(plan)
	return plan, nil
}

func (s Store) Record(ctx context.Context, request RecordRequest) (RecordResult, error) {
	if err := ctx.Err(); err != nil {
		return RecordResult{}, err
	}
	request.RunID = strings.TrimSpace(request.RunID)
	if request.RunID == "" || request.Plan.ID == "" || request.Plan.ID != planDigest(request.Plan) {
		return RecordResult{}, ErrInvalidPlan
	}
	if request.Plan.ForceCampaignID != "" &&
		!validBoundedText(request.Plan.ForceCampaignID, 256) {
		return RecordResult{}, ErrInvalidPlan
	}
	if !validBoundedText(request.RunID, 1024) || len(request.Observations) > maxReviewObservations {
		return RecordResult{}, ErrInvalidPlan
	}
	if request.ExcludedFiles < 0 || request.ExcludedFiles > maxReviewFiles {
		return RecordResult{}, ErrInvalidPlan
	}
	files, err := normalizeFiles(request.Plan.PendingFiles)
	if err != nil || len(files) != len(request.Plan.PendingFiles) {
		return RecordResult{}, ErrInvalidPlan
	}
	deferred, err := normalizeFiles(request.Plan.DeferredFiles)
	if err != nil || len(deferred) != len(request.Plan.DeferredFiles) {
		return RecordResult{}, ErrInvalidPlan
	}
	paths := make(map[string]struct{}, len(files)+len(deferred))
	for _, file := range append(append([]FileRef(nil), files...), deferred...) {
		if _, duplicate := paths[file.Path]; duplicate {
			return RecordResult{}, ErrInvalidPlan
		}
		paths[file.Path] = struct{}{}
	}
	request.Plan.PendingFiles = files
	request.Plan.DeferredFiles = deferred
	unlock, err := s.lock(request.Plan.Repository)
	if err != nil {
		return RecordResult{}, err
	}
	defer unlock()
	state, err := s.load(request.Plan.Repository)
	if err != nil {
		return RecordResult{}, err
	}
	if previous, ok := replayedRun(state, request); ok {
		return RecordResult{
			State: state, Run: previous,
			AcceptedFindingIDs: append([]string(nil), previous.FindingIDs...),
		}, nil
	}
	if state.ReviewVersion != request.Plan.StateVersion {
		return RecordResult{}, ErrConflict
	}
	completedAt := request.CompletedAt.UTC()
	if completedAt.IsZero() {
		completedAt = s.clock()
	}
	allowed := make(map[string]FileRef, len(files))
	for _, file := range files {
		allowed[file.Path] = file
	}
	unsupportedFiles := make(map[string]UnsupportedFile, len(request.UnsupportedFiles))
	for _, unsupported := range request.UnsupportedFiles {
		bound, ok := allowed[unsupported.Path]
		unsupported.Reason = strings.TrimSpace(unsupported.Reason)
		if !ok || bound.BlobSHA != unsupported.BlobSHA || bound.SizeBytes != unsupported.SizeBytes ||
			bound.Mode != unsupported.Mode || !validBoundedText(unsupported.Reason, 256) {
			return RecordResult{}, ErrInvalidPlan
		}
		unsupported.FileRef = bound
		unsupported.CommitSHA = request.Plan.CommitSHA
		unsupported.ProfileHash = request.Plan.ProfileHash
		unsupported.ForceCampaignID = request.Plan.ForceCampaignID
		unsupported.UpdatedAt = completedAt
		unsupportedFiles[unsupported.Path] = unsupported
	}
	contexts := make([]FindingContext, 0, len(request.Observations))
	existingContexts := make(map[string]int, len(state.Contexts))
	for index, contextRecord := range state.Contexts {
		existingContexts[contextRecord.ID] = index
	}
	covered := make(map[string]FileRef, len(files))
	var acceptedIDs []string
	rejected := 0
	models := make([]string, 0)
	for observationIndex, observation := range request.Observations {
		observation.Model = strings.TrimSpace(observation.Model)
		if !validBoundedText(observation.Model, 256) || len(observation.Findings) > maxFindingsPerObservation {
			return RecordResult{}, fmt.Errorf("observation %d has no model", observationIndex)
		}
		scope, scopeErr := bindScopeFiles(observation.ScopeFiles, allowed)
		if scopeErr != nil {
			return RecordResult{}, fmt.Errorf("observation %d: %w", observationIndex, scopeErr)
		}
		contextRecord := FindingContext{
			Repository: request.Plan.Repository, CommitSHA: request.Plan.CommitSHA,
			InventoryHash: request.Plan.InventoryHash, ProfileHash: request.Plan.ProfileHash,
			RunID: request.RunID,
			Model: observation.Model, Reviewer: strings.TrimSpace(observation.Reviewer),
			Files: scope, RawDigest: strings.TrimSpace(observation.RawDigest), CreatedAt: completedAt,
		}
		contextRecord.ID = stableID("rctx_", contextBindingDigest(contextRecord))
		contextUsed := false
		if request.CompletedFiles == nil {
			for _, file := range scope {
				covered[file.Path] = file
			}
		}
		models = appendUnique(models, observation.Model)
		for findingIndex, candidate := range observation.Findings {
			candidate = normalizeCandidate(candidate)
			if candidate.Validation.Status != "confirmed" {
				rejected++
				continue
			}
			primary, ok := fileInScope(candidate.File, scope)
			if !ok {
				return RecordResult{}, fmt.Errorf(
					"observation %d finding %d references a file outside its exact context",
					observationIndex,
					findingIndex,
				)
			}
			if err := validateCandidate(candidate); err != nil {
				rejected++
				continue
			}
			fingerprint := findingFingerprint(primary, candidate)
			candidateObservation := findingObservationFrom(
				candidate, contextRecord.ID, observation.Model, observation.Reviewer,
			)
			index := findingIndexByFingerprint(state.Findings, fingerprint)
			if index < 0 {
				index = semanticFindingIndex(state.Findings, primary, candidate)
			}
			if index < 0 {
				finding := Finding{
					ID: stableID("rfn_", request.Plan.Repository, fingerprint), Fingerprint: fingerprint,
					Repository: request.Plan.Repository, CommitSHA: request.Plan.CommitSHA,
					File: primary, Line: candidate.Line, Severity: candidate.Severity,
					Title: candidate.Title, Symbol: candidate.Symbol,
					Message: candidate.Message, Evidence: candidate.Evidence,
					Impact: candidate.Impact, Recommendation: candidate.Recommendation,
					Validation: candidate.Validation, ContextIDs: []string{contextRecord.ID},
					Models: []string{observation.Model}, ObservationCount: 1, Status: FindingOpen,
					Observations: []FindingObservation{candidateObservation},
					Version:      1, CreatedAt: completedAt, UpdatedAt: completedAt,
				}
				state.Findings = append(state.Findings, finding)
				contextUsed = true
				acceptedIDs = appendUnique(acceptedIDs, finding.ID)
				continue
			}
			finding := &state.Findings[index]
			if finding.Severity != candidate.Severity {
				finding.Severity = moreSevere(finding.Severity, candidate.Severity)
			}
			var addedObservation bool
			finding.Observations, addedObservation = upsertFindingObservation(
				finding.Observations, candidateObservation,
			)
			finding.ContextIDs = findingObservationContextIDs(finding.Observations)
			finding.Models = appendUnique(finding.Models, observation.Model)
			if addedObservation {
				finding.ObservationCount++
			}
			finding.Version++
			finding.UpdatedAt = completedAt
			contextUsed = true
			acceptedIDs = appendUnique(acceptedIDs, finding.ID)
		}
		if contextUsed {
			if existingIndex, exists := existingContexts[contextRecord.ID]; exists {
				if existingIndex < len(state.Contexts) {
					state.Contexts[existingIndex] = contextRecord
				} else {
					contexts[existingIndex-len(state.Contexts)] = contextRecord
				}
				continue
			}
			existingContexts[contextRecord.ID] = len(state.Contexts) + len(contexts)
			contexts = append(contexts, contextRecord)
		}
	}
	if request.CompletedFiles != nil {
		completed, completedErr := bindScopeFiles(request.CompletedFiles, allowed)
		if completedErr != nil && len(request.CompletedFiles) > 0 {
			return RecordResult{}, fmt.Errorf("completed review files: %w", completedErr)
		}
		for _, file := range completed {
			covered[file.Path] = file
		}
	}
	state.Contexts = append(state.Contexts, contexts...)
	pruneUnreferencedFindingContexts(&state)
	var unreviewedPaths []string
	for _, file := range files {
		if _, complete := covered[file.Path]; complete {
			delete(state.ReviewAttempts, file.Path)
			delete(state.ReviewAttemptIdentities, file.Path)
			delete(state.Unsupported, file.Path)
			continue
		}
		if unsupported, terminal := unsupportedFiles[file.Path]; terminal {
			state.Unsupported[file.Path] = unsupported
			delete(state.ReviewAttempts, file.Path)
			delete(state.ReviewAttemptIdentities, file.Path)
			continue
		}
		identity := reviewAttemptIdentity(file, request.Plan.ProfileHash)
		if state.ReviewAttemptIdentities[file.Path] != identity {
			state.ReviewAttempts[file.Path] = 0
		}
		state.ReviewAttemptIdentities[file.Path] = identity
		state.ReviewAttempts[file.Path]++
		unreviewedPaths = append(unreviewedPaths, file.Path)
	}
	for _, file := range covered {
		state.Files[file.Path] = ReviewedFile{
			FileRef: file, CommitSHA: request.Plan.CommitSHA,
			ProfileHash: request.Plan.ProfileHash, ForceCampaignID: request.Plan.ForceCampaignID,
			RunID: request.RunID, ReviewedAt: completedAt,
		}
	}
	var unsupportedPaths []string
	for pathValue := range unsupportedFiles {
		unsupportedPaths = append(unsupportedPaths, pathValue)
	}
	sort.Strings(unsupportedPaths)
	run := ReviewRun{
		ID: request.RunID, PlanID: request.Plan.ID, CommitSHA: request.Plan.CommitSHA,
		InventoryHash: request.Plan.InventoryHash, ReviewedFiles: len(covered),
		UnreviewedFiles:  len(files) - len(covered) - len(unsupportedFiles),
		UnsupportedCount: len(unsupportedFiles),
		RemainingFiles:   len(request.Plan.DeferredFiles) + len(files) - len(covered) - len(unsupportedFiles),
		UnreviewedPaths:  unreviewedPaths,
		UnsupportedPaths: unsupportedPaths,
		SkippedFiles:     len(request.Plan.UnchangedFiles), AcceptedFindings: len(acceptedIDs),
		ExcludedFiles:    request.ExcludedFiles,
		FindingIDs:       append([]string(nil), acceptedIDs...),
		RejectedFindings: rejected, Models: models, CompletedAt: completedAt,
	}
	state.Runs = append(state.Runs, run)
	if len(state.Runs) > 1000 {
		state.Runs = append([]ReviewRun(nil), state.Runs[len(state.Runs)-1000:]...)
	}
	pruneCheckpointMetadata(&state, request.Plan, files)
	state.LastCommitSHA = request.Plan.CommitSHA
	state.LastExcludedFiles = request.ExcludedFiles
	if request.Plan.ForceCampaignID != "" && run.RemainingFiles > 0 {
		state.ActiveForceCampaignID = request.Plan.ForceCampaignID
		state.ActiveForceProfileHash = request.Plan.ProfileHash
		state.ActiveForceCommitSHA = request.Plan.CommitSHA
	} else {
		state.ActiveForceCampaignID = ""
		state.ActiveForceProfileHash = ""
		state.ActiveForceCommitSHA = ""
	}
	state.Version++
	state.ReviewVersion++
	state.UpdatedAt = completedAt
	if err := s.save(&state); err != nil {
		return RecordResult{}, err
	}
	return RecordResult{State: state, Run: run, AcceptedFindingIDs: acceptedIDs}, nil
}

func (s Store) FinalizeNoopPlan(plan Plan, excludedFiles ...int) (RepositoryState, error) {
	if plan.ID == "" || plan.ID != planDigest(plan) || len(plan.PendingFiles) != 0 ||
		len(plan.DeferredFiles) != 0 || !plan.Authoritative {
		return RepositoryState{}, ErrInvalidPlan
	}
	unlock, err := s.lock(plan.Repository)
	if err != nil {
		return RepositoryState{}, err
	}
	defer unlock()
	state, err := s.load(plan.Repository)
	if err != nil {
		return RepositoryState{}, err
	}
	if state.ReviewVersion != plan.StateVersion {
		return RepositoryState{}, ErrConflict
	}
	changed := pruneCheckpointMetadata(&state, plan, nil)
	excluded := 0
	if len(excludedFiles) > 0 {
		excluded = excludedFiles[0]
	}
	if excluded < 0 || excluded > maxReviewFiles {
		return RepositoryState{}, ErrInvalidPlan
	}
	if state.LastExcludedFiles != excluded {
		state.LastExcludedFiles = excluded
		changed = true
	}
	if state.LastCommitSHA != plan.CommitSHA {
		state.LastCommitSHA = plan.CommitSHA
		changed = true
	}
	if !changed {
		return state, nil
	}
	state.Version++
	state.ReviewVersion++
	state.UpdatedAt = s.clock()
	if err := s.save(&state); err != nil {
		return RepositoryState{}, err
	}
	return state, nil
}

func pruneCheckpointMetadata(state *RepositoryState, plan Plan, pending []FileRef) bool {
	if state == nil || !plan.Authoritative {
		return false
	}
	current := make(
		map[string]struct{},
		len(pending)+len(plan.DeferredFiles)+len(plan.UnchangedFiles)+len(plan.UnsupportedFiles),
	)
	for _, file := range append(append(append([]FileRef(nil), pending...), plan.DeferredFiles...), plan.UnchangedFiles...) {
		current[file.Path] = struct{}{}
	}
	for _, unsupported := range plan.UnsupportedFiles {
		current[unsupported.Path] = struct{}{}
	}
	changed := false
	for pathValue := range state.Files {
		if _, exists := current[pathValue]; !exists {
			delete(state.Files, pathValue)
			changed = true
		}
	}
	for pathValue := range state.Unsupported {
		if _, exists := current[pathValue]; !exists {
			delete(state.Unsupported, pathValue)
			changed = true
		}
	}
	for pathValue := range state.ReviewAttempts {
		if _, exists := current[pathValue]; !exists {
			delete(state.ReviewAttempts, pathValue)
			delete(state.ReviewAttemptIdentities, pathValue)
			changed = true
		}
	}
	return changed
}

func (s Store) Get(repository string) (RepositoryState, bool, error) {
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, false, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, false, err
	}
	return state, state.Version > 0, nil
}

func (s Store) GetByID(id string) (RepositoryState, bool, error) {
	id = strings.TrimSpace(id)
	suffix, valid := strings.CutPrefix(id, "rrp_")
	if !valid || len(suffix) != 64 || !validHexDigest(suffix) {
		return RepositoryState{}, false, nil
	}
	if err := s.requireSafeRoot(true); err != nil {
		return RepositoryState{}, false, err
	}
	statePath := filepath.Join(s.root, "repo_"+suffix+".json")
	info, err := os.Lstat(statePath)
	if os.IsNotExist(err) {
		return RepositoryState{}, false, nil
	}
	if err != nil {
		return RepositoryState{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxStateFileBytes {
		return RepositoryState{}, false, errors.New("invalid repository review state")
	}
	file, err := os.Open(statePath)
	if err != nil {
		return RepositoryState{}, false, err
	}
	var summary RepositorySummary
	decodeErr := json.NewDecoder(file).Decode(&summary)
	closeErr := file.Close()
	if decodeErr != nil || closeErr != nil {
		return RepositoryState{}, false, errors.Join(decodeErr, closeErr)
	}
	if summary.ID != id || summary.ID != RepositoryID(summary.Repository) {
		return RepositoryState{}, false, errors.New("repository review state ID mismatch")
	}
	return s.Get(summary.Repository)
}

func (s Store) ListSummaries() ([]RepositorySummary, error) {
	return s.listSummaries(10_000)
}

func (s Store) listSummaries(maximum int) ([]RepositorySummary, error) {
	if err := s.requireSafeRoot(true); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.root)
	if os.IsNotExist(err) {
		return []RepositorySummary{}, nil
	}
	if err != nil {
		return nil, err
	}
	summaries := make([]RepositorySummary, 0, len(entries))
	stateCount := 0
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("repository review state %q must not be a symlink", entry.Name())
		}
		if entry.IsDir() || !entry.Type().IsRegular() || !repositoryReviewStateFilename(entry.Name()) {
			continue
		}
		stateCount++
		if stateCount > maximum {
			return nil, errors.New("repository review catalog exceeds its repository limit")
		}
		summary, summaryErr := repositoryReviewSummaryFromEntry(s.root, entry)
		if summaryErr != nil {
			return nil, summaryErr
		}
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].UpdatedAt.After(summaries[j].UpdatedAt) })
	return summaries, nil
}

func repositoryReviewSummaryFromEntry(root string, entry os.DirEntry) (RepositorySummary, error) {
	statePath := filepath.Join(root, entry.Name())
	stateInfo, infoErr := entry.Info()
	if infoErr != nil || stateInfo.Size() > maxStateFileBytes {
		return RepositorySummary{}, errors.Join(infoErr, errors.New("repository review state exceeds its size limit"))
	}
	summaryPath := strings.TrimSuffix(statePath, ".json") + ".summary.json"
	readPath := statePath
	if summaryInfo, summaryErr := os.Lstat(summaryPath); summaryErr == nil {
		if summaryInfo.Mode()&os.ModeSymlink != 0 || !summaryInfo.Mode().IsRegular() {
			return RepositorySummary{}, errors.New("repository review summary must be a regular file")
		}
		if !summaryInfo.ModTime().Before(stateInfo.ModTime()) {
			readPath = summaryPath
		}
	} else if !os.IsNotExist(summaryErr) {
		return RepositorySummary{}, summaryErr
	}
	file, openErr := os.Open(readPath)
	if openErr != nil {
		return RepositorySummary{}, openErr
	}
	var summary RepositorySummary
	decodeErr := json.NewDecoder(file).Decode(&summary)
	closeErr := file.Close()
	if decodeErr != nil || closeErr != nil {
		return RepositorySummary{}, errors.Join(decodeErr, closeErr)
	}
	if summary.SchemaVersion != SchemaVersion || summary.ID != RepositoryID(summary.Repository) {
		return RepositorySummary{}, errors.New("invalid repository review summary")
	}
	return summary, nil
}

func (s Store) SetFindingStatus(
	repository string,
	findingID string,
	status FindingStatus,
	expectedVersion int64,
) (RepositoryState, error) {
	if status != FindingOpen && status != FindingDismissed && status != FindingPosted {
		return RepositoryState{}, errors.New("invalid repository review finding status")
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, err
	}
	index := -1
	for candidate := range state.Findings {
		if state.Findings[candidate].ID == strings.TrimSpace(findingID) {
			index = candidate
			break
		}
	}
	if index < 0 {
		return RepositoryState{}, os.ErrNotExist
	}
	if state.Findings[index].Status == status {
		return state, nil
	}
	if expectedVersion < 1 || state.Version != expectedVersion {
		return RepositoryState{}, ErrConflict
	}
	now := s.clock()
	state.Findings[index].Status = status
	state.Findings[index].Version++
	state.Findings[index].UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, err
	}
	return state, nil
}

func (s Store) PrepareIssue(request IssueDraftRequest) (RepositoryState, IssueDraft, error) {
	request.Repository = strings.TrimSpace(request.Repository)
	unlock, err := s.lock(request.Repository)
	if err != nil {
		return RepositoryState{}, IssueDraft{}, err
	}
	defer unlock()
	state, err := s.load(request.Repository)
	if err != nil {
		return RepositoryState{}, IssueDraft{}, err
	}
	findings, ids, err := selectedFindings(state.Findings, request.FindingIDs)
	if err != nil {
		return RepositoryState{}, IssueDraft{}, err
	}
	title := strings.TrimSpace(request.Title)
	if title == "" {
		title = defaultIssueTitle(findings)
	}
	body := strings.TrimSpace(request.Body)
	if body == "" {
		body = defaultIssueBody(state, findings)
	}
	labels := normalizeLabels(request.Labels)
	if len(labels) == 0 {
		labels = []string{"bug"}
	}
	if !validBoundedText(title, 256) || !validBoundedText(body, maxIssueDraftBodyBytes) {
		return RepositoryState{}, IssueDraft{}, errors.New("invalid repository review issue draft")
	}
	draftID := stableID(
		"rid_", state.Repository, strings.Join(ids, "\x00"), title, body,
		strings.Join(labels, "\x00"),
	)
	for _, existing := range state.IssueDrafts {
		if existing.ID == draftID {
			return state, existing, nil
		}
	}
	if request.ExpectedVersion < 1 || state.Version != request.ExpectedVersion {
		return RepositoryState{}, IssueDraft{}, ErrConflict
	}
	now := s.clock()
	draft := IssueDraft{
		ID:         draftID,
		Repository: state.Repository, FindingIDs: ids, Title: title, Body: body,
		Labels: labels, State: IssueDraftEditing, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	state.IssueDrafts = append(state.IssueDrafts, draft)
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, IssueDraft{}, err
	}
	return state, draft, nil
}

func (s Store) UpdateIssueDraft(
	repository, draftID, title, body string,
	labels []string,
	expectedVersion int64,
) (RepositoryState, IssueDraft, error) {
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, IssueDraft{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, IssueDraft{}, err
	}
	index := -1
	for candidate := range state.IssueDrafts {
		if state.IssueDrafts[candidate].ID == strings.TrimSpace(draftID) {
			index = candidate
			break
		}
	}
	if index < 0 {
		return RepositoryState{}, IssueDraft{}, os.ErrNotExist
	}
	draft := &state.IssueDrafts[index]
	if draft.State != IssueDraftEditing {
		return RepositoryState{}, IssueDraft{}, ErrConflict
	}
	title, body = strings.TrimSpace(title), strings.TrimSpace(body)
	labels = normalizeLabels(labels)
	if draft.Title == title && draft.Body == body &&
		strings.Join(draft.Labels, "\x00") == strings.Join(labels, "\x00") {
		return state, *draft, nil
	}
	if expectedVersion < 1 || draft.Version != expectedVersion {
		return RepositoryState{}, IssueDraft{}, ErrConflict
	}
	if !validBoundedText(title, 256) || !validBoundedText(body, maxIssueDraftBodyBytes) {
		return RepositoryState{}, IssueDraft{}, errors.New("invalid repository review issue draft")
	}
	draft.Title, draft.Body, draft.Labels = title, body, labels
	draft.Version++
	draft.UpdatedAt = s.clock()
	state.Version++
	state.UpdatedAt = draft.UpdatedAt
	if err := s.save(&state); err != nil {
		return RepositoryState{}, IssueDraft{}, err
	}
	return state, *draft, nil
}

func (s Store) SetIssueDraftPublication(
	repository, draftID string,
	expectedVersion int64,
	publicationState IssueDraftState,
	externalID, externalURL string,
) (RepositoryState, IssueDraft, error) {
	if publicationState != IssueDraftEditing && publicationState != IssueDraftPosted &&
		publicationState != IssueDraftUnknown {
		return RepositoryState{}, IssueDraft{}, errors.New("invalid issue publication state")
	}
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, IssueDraft{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, IssueDraft{}, err
	}
	index := -1
	for candidate := range state.IssueDrafts {
		if state.IssueDrafts[candidate].ID == strings.TrimSpace(draftID) {
			index = candidate
			break
		}
	}
	if index < 0 {
		return RepositoryState{}, IssueDraft{}, os.ErrNotExist
	}
	draft := &state.IssueDrafts[index]
	if draft.State == IssueDraftPosted {
		return state, *draft, nil
	}
	if expectedVersion < 1 || draft.Version != expectedVersion {
		return RepositoryState{}, IssueDraft{}, ErrConflict
	}
	if draft.State != IssueDraftPublishing && draft.State != IssueDraftUnknown {
		return RepositoryState{}, IssueDraft{}, ErrConflict
	}
	if publicationState == IssueDraftEditing && draft.State != IssueDraftPublishing {
		return RepositoryState{}, IssueDraft{}, ErrConflict
	}
	if publicationState == IssueDraftPosted &&
		(!validBoundedText(strings.TrimSpace(externalID), 1024) ||
			!validBoundedText(strings.TrimSpace(externalURL), 4096) ||
			!strings.HasPrefix(strings.TrimSpace(externalURL), "https://")) {
		return RepositoryState{}, IssueDraft{}, errors.New("posted issue identity is required")
	}
	now := s.clock()
	draft.State = publicationState
	draft.ExternalID = strings.TrimSpace(externalID)
	draft.ExternalURL = strings.TrimSpace(externalURL)
	draft.Version++
	draft.UpdatedAt = now
	if publicationState == IssueDraftPosted {
		selected := make(map[string]struct{}, len(draft.FindingIDs))
		for _, id := range draft.FindingIDs {
			selected[id] = struct{}{}
		}
		for findingIndex := range state.Findings {
			if _, ok := selected[state.Findings[findingIndex].ID]; !ok {
				continue
			}
			state.Findings[findingIndex].Status = FindingPosted
			state.Findings[findingIndex].Version++
			state.Findings[findingIndex].UpdatedAt = now
		}
	}
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, IssueDraft{}, err
	}
	return state, *draft, nil
}

func (s Store) ClaimIssueDraftPublication(
	repository, draftID string,
	expectedVersion int64,
) (RepositoryState, IssueDraft, bool, error) {
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, IssueDraft{}, false, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, IssueDraft{}, false, err
	}
	index := -1
	for candidate := range state.IssueDrafts {
		if state.IssueDrafts[candidate].ID == strings.TrimSpace(draftID) {
			index = candidate
			break
		}
	}
	if index < 0 {
		return RepositoryState{}, IssueDraft{}, false, os.ErrNotExist
	}
	draft := &state.IssueDrafts[index]
	if draft.State == IssueDraftPosted || draft.State == IssueDraftPublishing ||
		draft.State == IssueDraftUnknown {
		return state, *draft, false, nil
	}
	if draft.State != IssueDraftEditing || expectedVersion < 1 || draft.Version != expectedVersion {
		return RepositoryState{}, IssueDraft{}, false, ErrConflict
	}
	now := s.clock()
	draft.State = IssueDraftPublishing
	draft.Version++
	draft.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, IssueDraft{}, false, err
	}
	return state, *draft, true, nil
}

func (s Store) List() ([]RepositoryState, error) {
	if err := s.requireSafeRoot(true); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.root)
	if os.IsNotExist(err) {
		return []RepositoryState{}, nil
	}
	if err != nil {
		return nil, err
	}
	states := make([]RepositoryState, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("repository review state %q must not be a symlink", entry.Name())
		}
		if entry.IsDir() || !entry.Type().IsRegular() || !repositoryReviewStateFilename(entry.Name()) {
			continue
		}
		state, stateErr := repositoryReviewStateFromEntry(s.root, entry)
		if stateErr != nil {
			return nil, stateErr
		}
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].UpdatedAt.After(states[j].UpdatedAt) })
	return states, nil
}

func repositoryReviewStateFromEntry(root string, entry os.DirEntry) (RepositoryState, error) {
	info, infoErr := entry.Info()
	if infoErr != nil {
		return RepositoryState{}, infoErr
	}
	if info.Size() > maxStateFileBytes {
		return RepositoryState{}, errors.New("repository review state exceeds its size limit")
	}
	data, readErr := os.ReadFile(filepath.Join(root, entry.Name()))
	if readErr != nil {
		return RepositoryState{}, readErr
	}
	var state RepositoryState
	if jsonErr := json.Unmarshal(data, &state); jsonErr != nil {
		return RepositoryState{}, jsonErr
	}
	if err := validateState(state); err != nil {
		return RepositoryState{}, err
	}
	return state, nil
}

func (s Store) load(repository string) (RepositoryState, error) {
	state := RepositoryState{
		SchemaVersion:           SchemaVersion,
		ID:                      RepositoryID(repository),
		Repository:              strings.TrimSpace(repository),
		Files:                   make(map[string]ReviewedFile),
		Unsupported:             make(map[string]UnsupportedFile),
		ReviewAttempts:          make(map[string]int),
		ReviewAttemptIdentities: make(map[string]string),
		Findings:                []Finding{},
		Contexts:                []FindingContext{},
		Runs:                    []ReviewRun{},
		IssueDrafts:             []IssueDraft{},
	}
	if err := s.requireSafeRoot(true); err != nil {
		return RepositoryState{}, err
	}
	statePath := s.path(repository)
	info, err := os.Lstat(statePath)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return RepositoryState{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return RepositoryState{}, errors.New("repository review state must be a regular file")
	}
	if info.Size() > maxStateFileBytes {
		return RepositoryState{}, errors.New("repository review state exceeds its size limit")
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		return RepositoryState{}, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return RepositoryState{}, err
	}
	if err := validateState(state); err != nil {
		return RepositoryState{}, err
	}
	if state.Repository != strings.TrimSpace(repository) {
		return RepositoryState{}, errors.New("repository review state identity mismatch")
	}
	if state.Files == nil {
		state.Files = make(map[string]ReviewedFile)
	}
	if state.Unsupported == nil {
		state.Unsupported = make(map[string]UnsupportedFile)
	}
	if state.ReviewAttempts == nil {
		state.ReviewAttempts = make(map[string]int)
	}
	if state.ReviewAttemptIdentities == nil {
		state.ReviewAttemptIdentities = make(map[string]string)
	}
	if state.Findings == nil {
		state.Findings = []Finding{}
	}
	if state.Contexts == nil {
		state.Contexts = []FindingContext{}
	}
	if state.Runs == nil {
		state.Runs = []ReviewRun{}
	}
	if state.IssueDrafts == nil {
		state.IssueDrafts = []IssueDraft{}
	}
	return state, nil
}

func (s Store) save(state *RepositoryState) error {
	if state == nil {
		return errors.New("repository review state is required")
	}
	summary := Summarize(*state)
	state.FindingCount = summary.FindingCount
	state.OpenFindingCount = summary.OpenFindingCount
	state.IssueDraftCount = summary.IssueDraftCount
	state.UnsupportedCount = summary.UnsupportedCount
	state.ReviewedFileCount = summary.ReviewedFileCount
	if err := validateState(*state); err != nil {
		return err
	}
	if err := s.ensureSafeRoot(fileutil.MkdirAllDurable); err != nil {
		return err
	}
	if info, err := os.Lstat(s.path(state.Repository)); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("repository review state must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if int64(len(data)) > maxStateFileBytes {
		return errors.New("repository review state exceeds its size limit")
	}
	statePath := s.path(state.Repository)
	summaryPath := strings.TrimSuffix(statePath, ".json") + ".summary.json"
	if removeErr := os.Remove(summaryPath); removeErr != nil && !os.IsNotExist(removeErr) {
		return removeErr
	}
	if writeErr := fileutil.WriteFileAtomic(statePath, data, 0o600); writeErr != nil {
		return writeErr
	}
	summaryData, err := json.Marshal(Summarize(*state))
	if err != nil {
		return err
	}
	// The sidecar is a rebuildable list projection. The authoritative state is
	// already committed, so a projection write failure must not turn a successful
	// versioned mutation into an ambiguous failure.
	_ = fileutil.WriteFileAtomic(summaryPath, summaryData, 0o600)
	return nil
}

func (s Store) requireSafeRoot(allowMissing bool) error {
	info, err := os.Lstat(s.root)
	if os.IsNotExist(err) && allowMissing {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("repository review storage root must be a real directory")
	}
	return nil
}

func (s Store) ensureSafeRoot(mkdir func(string, os.FileMode) error) error {
	if err := s.requireSafeRoot(true); err != nil {
		return err
	}
	if err := mkdir(s.root, 0o700); err != nil {
		return err
	}
	return s.requireSafeRoot(false)
}

func (s Store) path(repository string) string {
	return filepath.Join(s.root, stableID("repo_", strings.TrimSpace(repository))+".json")
}

func repositoryReviewStateFilename(name string) bool {
	return strings.HasPrefix(name, "repo_") && strings.HasSuffix(name, ".json") &&
		!strings.HasSuffix(name, ".summary.json")
}

func validHexDigest(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return value != ""
}

func (s Store) lock(repository string) (func(), error) {
	key := s.root + "\x00" + strings.TrimSpace(repository)
	value, _ := storeLocks.LoadOrStore(key, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	unlockFile, err := lockRepositoryReviewStore(s.root)
	if err != nil {
		mutex.Unlock()
		return nil, err
	}
	return func() {
		unlockFile()
		mutex.Unlock()
	}, nil
}

func (s Store) clock() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

func normalizeFiles(files []FileRef) ([]FileRef, error) {
	out := make([]FileRef, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	metadataBytes := 0
	for _, file := range files {
		file.Path = strings.TrimSpace(filepath.ToSlash(file.Path))
		cleanPath := path.Clean(file.Path)
		file.BlobSHA = strings.ToLower(strings.TrimSpace(file.BlobSHA))
		file.Category = strings.TrimSpace(file.Category)
		file.Mode = strings.TrimSpace(file.Mode)
		if !validBoundedText(file.Path, 4096) || file.Path == "." || cleanPath != file.Path ||
			strings.HasPrefix(cleanPath, "../") || strings.HasPrefix(cleanPath, "/") ||
			!validBlobSHA(file.BlobSHA) || file.SizeBytes < 0 {
			return nil, fmt.Errorf("%w: invalid file reference", ErrInvalidPlan)
		}
		if _, duplicate := seen[file.Path]; duplicate {
			return nil, fmt.Errorf("%w: duplicate file path %q", ErrInvalidPlan, file.Path)
		}
		metadataBytes += len(file.Path) + len(file.BlobSHA) + len(file.Category) + len(file.Mode) + 32
		if metadataBytes > maxReviewFileMetadataBytes {
			return nil, fmt.Errorf("%w: file inventory metadata exceeds its size limit", ErrInvalidPlan)
		}
		seen[file.Path] = struct{}{}
		out = append(out, file)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func reviewAttemptIdentity(file FileRef, profileHash string) string {
	return stableID(
		"rat_", file.Path, file.BlobSHA, fmt.Sprint(file.SizeBytes), file.Mode,
		strings.TrimSpace(profileHash),
	)
}

func reviewAttemptsFor(state RepositoryState, file FileRef, profileHash string) int {
	if state.ReviewAttemptIdentities[file.Path] != reviewAttemptIdentity(file, profileHash) {
		return 0
	}
	return state.ReviewAttempts[file.Path]
}

func validBlobSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func replayedRun(state RepositoryState, request RecordRequest) (ReviewRun, bool) {
	for _, run := range state.Runs {
		if run.ID != request.RunID {
			continue
		}
		return run, run.PlanID == request.Plan.ID
	}
	return ReviewRun{}, false
}

func bindScopeFiles(files []FileRef, allowed map[string]FileRef) ([]FileRef, error) {
	normalized, err := normalizeFiles(files)
	if err != nil || len(normalized) == 0 {
		return nil, errors.New("exact finding context is empty or invalid")
	}
	for index, file := range normalized {
		trusted, ok := allowed[file.Path]
		if !ok || trusted.BlobSHA != file.BlobSHA || trusted.SizeBytes != file.SizeBytes {
			return nil, fmt.Errorf("context file %q is outside the immutable review plan", file.Path)
		}
		normalized[index] = trusted
	}
	return normalized, nil
}

func fileInScope(path string, scope []FileRef) (FileRef, bool) {
	path = strings.TrimSpace(filepath.ToSlash(path))
	for _, file := range scope {
		if file.Path == path {
			return file, true
		}
	}
	return FileRef{}, false
}

func normalizeCandidate(candidate FindingCandidate) FindingCandidate {
	candidate.Severity = strings.ToLower(strings.TrimSpace(candidate.Severity))
	candidate.Title = strings.TrimSpace(candidate.Title)
	candidate.Symbol = strings.TrimSpace(candidate.Symbol)
	candidate.File = strings.TrimSpace(filepath.ToSlash(candidate.File))
	candidate.Message = strings.TrimSpace(candidate.Message)
	candidate.Evidence = strings.TrimSpace(candidate.Evidence)
	candidate.Impact = strings.TrimSpace(candidate.Impact)
	candidate.Recommendation = strings.TrimSpace(candidate.Recommendation)
	candidate.Validation.Status = strings.ToLower(strings.TrimSpace(candidate.Validation.Status))
	candidate.Validation.Summary = strings.TrimSpace(candidate.Validation.Summary)
	return candidate
}

func validateCandidate(candidate FindingCandidate) error {
	switch candidate.Severity {
	case "critical", "high", "medium", "low":
	default:
		return errors.New("invalid severity")
	}
	if candidate.Title == "" || candidate.File == "" || candidate.Evidence == "" || candidate.Impact == "" ||
		candidate.Recommendation == "" ||
		candidate.Validation.Summary == "" {
		return errors.New("finding is incomplete")
	}
	for _, value := range []string{
		candidate.Title, candidate.File, candidate.Evidence,
		candidate.Impact, candidate.Recommendation, candidate.Validation.Summary,
	} {
		if !validBoundedText(value, maxFindingTextBytes) {
			return errors.New("finding text exceeds its limit or is invalid UTF-8")
		}
	}
	if candidate.Message != "" && !validBoundedText(candidate.Message, maxFindingTextBytes) {
		return errors.New("finding message exceeds its limit or is invalid UTF-8")
	}
	if candidate.Symbol != "" && !validBoundedText(candidate.Symbol, 4096) {
		return errors.New("finding symbol is invalid")
	}
	if len(candidate.Validation.Checks) > 128 {
		return errors.New("finding validation has too many checks")
	}
	for _, check := range candidate.Validation.Checks {
		if !validBoundedText(strings.TrimSpace(check), 4096) {
			return errors.New("finding validation check is invalid")
		}
	}
	if candidate.Line != nil && *candidate.Line < 1 {
		return errors.New("finding line must be positive")
	}
	return nil
}

func findingFingerprint(file FileRef, candidate FindingCandidate) string {
	line := 0
	if candidate.Line != nil {
		line = *candidate.Line
	}
	return stableID(
		"sha256:", file.Path, file.BlobSHA, fmt.Sprint(line),
		normalizedText(candidate.Symbol), normalizedText(candidate.Title),
		normalizedText(candidate.Message), normalizedText(candidate.Evidence),
	)
}

func findingIndexByFingerprint(findings []Finding, fingerprint string) int {
	for index := range findings {
		if findings[index].Fingerprint == fingerprint {
			return index
		}
	}
	return -1
}

func semanticFindingIndex(findings []Finding, file FileRef, candidate FindingCandidate) int {
	candidateTitle := findingTokens(candidate.Title)
	candidateBody := findingTokens(candidate.Title + "\n" + candidate.Message + "\n" + candidate.Evidence)
	for index, finding := range findings {
		if finding.File.Path != file.Path || finding.File.BlobSHA != file.BlobSHA ||
			!nearbyLines(finding.Line, candidate.Line) || candidate.Symbol == "" ||
			normalizedText(finding.Symbol) != normalizedText(candidate.Symbol) {
			continue
		}
		titleSimilarity := tokenDice(findingTokens(finding.Title), candidateTitle)
		bodySimilarity := tokenDice(
			findingTokens(finding.Title+"\n"+finding.Message+"\n"+finding.Evidence),
			candidateBody,
		)
		if titleSimilarity >= 0.65 && bodySimilarity >= 0.35 || bodySimilarity >= 0.72 {
			return index
		}
	}
	return -1
}

func moreSevere(left, right string) string {
	rank := map[string]int{"low": 1, "medium": 2, "high": 3, "critical": 4}
	if rank[right] > rank[left] {
		return right
	}
	return left
}

func findingObservationFrom(
	candidate FindingCandidate,
	contextID, model, reviewer string,
) FindingObservation {
	return FindingObservation{
		ContextID: contextID, Model: strings.TrimSpace(model), Reviewer: strings.TrimSpace(reviewer),
		Severity: candidate.Severity, Title: candidate.Title, Symbol: candidate.Symbol,
		Line: candidate.Line, Message: candidate.Message, Evidence: candidate.Evidence,
		Impact: candidate.Impact, Recommendation: candidate.Recommendation,
		Validation: candidate.Validation,
	}
}

func upsertFindingObservation(
	observations []FindingObservation,
	candidate FindingObservation,
) ([]FindingObservation, bool) {
	for index := range observations {
		if observations[index].ContextID == candidate.ContextID {
			observations[index] = candidate
			return observations, false
		}
	}
	if len(observations) >= 64 {
		copy(observations, observations[len(observations)-63:])
		observations = observations[:63]
	}
	return append(observations, candidate), true
}

func findingObservationContextIDs(observations []FindingObservation) []string {
	contexts := make([]string, 0, len(observations))
	for _, observation := range observations {
		contexts = appendUnique(contexts, observation.ContextID)
	}
	return contexts
}

func pruneUnreferencedFindingContexts(state *RepositoryState) {
	if state == nil {
		return
	}
	referenced := make(map[string]struct{})
	for _, finding := range state.Findings {
		for _, contextID := range finding.ContextIDs {
			referenced[contextID] = struct{}{}
		}
	}
	contexts := state.Contexts[:0]
	for _, contextRecord := range state.Contexts {
		if _, keep := referenced[contextRecord.ID]; keep {
			contexts = append(contexts, contextRecord)
		}
	}
	state.Contexts = contexts
}

func nearbyLines(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	delta := *left - *right
	if delta < 0 {
		delta = -delta
	}
	return delta <= 5
}

func findingTokens(value string) map[string]struct{} {
	stop := map[string]struct{}{
		"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {},
		"by": {}, "for": {}, "from": {}, "in": {}, "is": {}, "it": {}, "of": {},
		"on": {}, "or": {}, "that": {}, "the": {}, "this": {}, "to": {}, "with": {},
	}
	tokens := make(map[string]struct{})
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(token) > 4 && strings.HasSuffix(token, "ing") {
			token = token[:len(token)-3]
		} else if len(token) > 3 && strings.HasSuffix(token, "s") && !strings.HasSuffix(token, "ss") {
			token = token[:len(token)-1]
		}
		if _, ignored := stop[token]; ignored {
			continue
		}
		tokens[token] = struct{}{}
	}
	return tokens
}

func tokenDice(left, right map[string]struct{}) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	shared := 0
	for token := range left {
		if _, ok := right[token]; ok {
			shared++
		}
	}
	return float64(2*shared) / float64(len(left)+len(right))
}

func normalizedText(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}

func appendUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func stableID(prefix string, values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return prefix + hex.EncodeToString(hash.Sum(nil))
}

func planDigest(plan Plan) string {
	digestPlan := plan
	digestPlan.ID = ""
	data, _ := json.Marshal(digestPlan)
	return stableID("rpl_", string(data))
}

func contextBindingDigest(context FindingContext) string {
	context.ID = ""
	context.CreatedAt = time.Time{}
	data, _ := json.Marshal(context)
	return stableID("sha256:", string(data))
}

func validateState(state RepositoryState) error {
	if state.SchemaVersion != SchemaVersion || strings.TrimSpace(state.Repository) == "" ||
		!validBoundedText(state.Repository, maxRepositoryIdentityBytes) ||
		state.ID != RepositoryID(state.Repository) || state.Version < 0 || state.ReviewVersion < 0 ||
		state.LastExcludedFiles < 0 || state.LastExcludedFiles > maxReviewFiles ||
		len(state.Files) > maxReviewFiles || len(state.Unsupported) > maxReviewFiles ||
		len(state.ReviewAttempts) > maxReviewFiles ||
		len(state.ReviewAttemptIdentities) > maxReviewFiles ||
		len(state.Contexts) > 1_000_000 ||
		len(state.Findings) > 100_000 || len(state.Runs) > 100_000 || len(state.IssueDrafts) > 100_000 {
		return errors.New("invalid repository review state")
	}
	activeForceFields := 0
	for _, value := range []string{
		state.ActiveForceCampaignID, state.ActiveForceProfileHash, state.ActiveForceCommitSHA,
	} {
		if value != "" {
			if !validBoundedText(value, 256) {
				return errors.New("invalid repository review force campaign")
			}
			activeForceFields++
		}
	}
	if activeForceFields != 0 && activeForceFields != 3 {
		return errors.New("invalid repository review force campaign")
	}
	for pathValue, attempts := range state.ReviewAttempts {
		if !validBoundedText(pathValue, 4096) || attempts < 0 {
			return errors.New("invalid repository review attempt state")
		}
	}
	for pathValue, identity := range state.ReviewAttemptIdentities {
		if !validBoundedText(pathValue, 4096) || !validBoundedText(identity, 128) {
			return errors.New("invalid repository review attempt identity")
		}
		if _, exists := state.ReviewAttempts[pathValue]; !exists {
			return errors.New("invalid repository review attempt identity")
		}
	}
	for pathValue, unsupported := range state.Unsupported {
		if pathValue != unsupported.Path || !validBoundedText(unsupported.Reason, 256) ||
			!validBlobSHA(unsupported.BlobSHA) || unsupported.SizeBytes < 0 {
			return errors.New("invalid repository review unsupported file state")
		}
	}
	for _, finding := range state.Findings {
		if len(finding.Observations) > 64 || len(finding.ContextIDs) > 64 {
			return errors.New("invalid repository review finding observations")
		}
	}
	return nil
}

func validBoundedText(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && len(value) <= maximum && !strings.ContainsRune(value, 0)
}

func RepositoryID(repository string) string {
	return stableID("rrp_", strings.TrimSpace(repository))
}

func selectedFindings(all []Finding, requested []string) ([]Finding, []string, error) {
	if len(requested) == 0 || len(requested) > 200 {
		return nil, nil, errors.New("one to 200 finding IDs are required")
	}
	byID := make(map[string]Finding, len(all))
	for _, finding := range all {
		byID[finding.ID] = finding
	}
	selected := make([]Finding, 0, len(requested))
	ids := make([]string, 0, len(requested))
	seen := make(map[string]struct{})
	for _, id := range requested {
		id = strings.TrimSpace(id)
		if _, duplicate := seen[id]; duplicate {
			return nil, nil, errors.New("duplicate finding ID")
		}
		finding, ok := byID[id]
		if !ok {
			return nil, nil, os.ErrNotExist
		}
		seen[id] = struct{}{}
		selected, ids = append(selected, finding), append(ids, id)
	}
	return selected, ids, nil
}

func defaultIssueTitle(findings []Finding) string {
	if len(findings) == 1 {
		return truncateUTF8Bytes(findings[0].Title, 256)
	}
	return fmt.Sprintf("Repository review: %d validated bugs", len(findings))
}

func truncateUTF8Bytes(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if maximum < 1 || len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}

func defaultIssueBody(state RepositoryState, findings []Finding) string {
	const maximumBodyBytes = maxIssueDraftBodyBytes
	const issueFieldBytes = 8 << 10
	var builder strings.Builder
	fmt.Fprintf(
		&builder,
		"## Repository review findings\n\nRepository: `%s`\nLatest reviewed commit: `%s`\n\n",
		state.Repository,
		state.LastCommitSHA,
	)
	for _, finding := range findings {
		section := strings.Builder{}
		fmt.Fprintf(
			&section,
			"### [%s] %s\n\n",
			strings.ToUpper(finding.Severity),
			truncateUTF8Bytes(finding.Title, 256),
		)
		fmt.Fprintf(&section, "Finding ID: `%s`\n\nLocation: `%s", finding.ID, finding.File.Path)
		if finding.Line != nil {
			fmt.Fprintf(&section, ":%d", *finding.Line)
		}
		fmt.Fprintf(
			&section,
			"` (commit `%s`, blob `%s`)\n\n%s\n\nImpact: %s\n\nRecommendation: %s\n\nValidation: %s\n\n",
			finding.CommitSHA,
			finding.File.BlobSHA,
			truncateUTF8Bytes(finding.Evidence, issueFieldBytes),
			truncateUTF8Bytes(finding.Impact, issueFieldBytes),
			truncateUTF8Bytes(finding.Recommendation, issueFieldBytes),
			truncateUTF8Bytes(finding.Validation.Summary, issueFieldBytes),
		)
		if builder.Len()+section.Len()+128 > maximumBodyBytes {
			fmt.Fprintf(
				&builder,
				"\n%d additional selected findings are retained in draft finding_ids but omitted here to keep the issue body bounded.\n",
				len(findings)-strings.Count(builder.String(), "Finding ID: `"),
			)
			break
		}
		builder.WriteString(section.String())
	}
	return strings.TrimSpace(builder.String())
}

func normalizeLabels(labels []string) []string {
	out := make([]string, 0, len(labels))
	seen := make(map[string]struct{})
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if !validBoundedText(label, 50) {
			continue
		}
		if _, duplicate := seen[label]; duplicate {
			continue
		}
		seen[label] = struct{}{}
		out = append(out, label)
		if len(out) == 20 {
			break
		}
	}
	return out
}
