package repoaudit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const storeDirectory = "repository_reviews"

const (
	maxRepositoryIdentityBytes       = 4096
	maxReviewFiles                   = 100_000
	maxReviewObservations            = 100_000
	maxFindingsPerObservation        = 256
	maxFindingTextBytes              = 64 << 10
	maxMatchHintItems                = 32
	maxMatchHintIdentityBytes        = 4096
	maxFixEffortLOC                  = 1_000_000
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
	workspace   string
	root        string
	database    string
	now         func() time.Time
	loadForTest func(string) (RepositoryState, error)
	openForTest func(context.Context) (*sql.DB, error)
}

// NewSQLiteStore opens repository-review persistence lazily at the first
// operation. The returned value is safe to copy; mutations remain version
// fenced in repository-reviews.db.
func NewSQLiteStore(workspace string) Store {
	root := filepath.Join(workspace, storeDirectory)
	return Store{
		workspace: workspace,
		root:      root,
		database:  filepath.Join(root, repositoryReviewDatabaseFilename),
		now:       time.Now,
	}
}

// NewStore is retained for source compatibility. Repository reviews are
// persisted exclusively in SQLite.
// Deprecated: use NewSQLiteStore.
func NewStore(workspace string) Store { return NewSQLiteStore(workspace) }

// PlanAssignmentsForCampaign selects distinct incomplete files and freezes one
// missing-only scope for every assignment in catalog. The catalog is part of
// campaign identity and cannot drift after its first successful binding.
func (s Store) PlanAssignmentsForCampaign(
	ctx context.Context,
	repository, commitSHA, inventoryHash, profileHash, campaignID string,
	catalog []RepositoryReviewAssignment,
	files []FileRef,
	force bool,
	maximumPending int,
	authoritative bool,
) (Plan, error) {
	if !ValidRepositoryReviewCampaignID(strings.TrimSpace(campaignID)) || !authoritative {
		return Plan{}, ErrInvalidPlan
	}
	normalized, err := NormalizeRepositoryReviewAssignmentCatalog(catalog)
	if err != nil {
		return Plan{}, err
	}
	return s.planWithProfileLimitAuthoritative(
		ctx,
		repository,
		commitSHA,
		inventoryHash,
		profileHash,
		campaignID,
		normalized,
		files,
		force,
		maximumPending,
		authoritative,
	)
}

func (s Store) planWithProfileLimitAuthoritative(
	ctx context.Context,
	repository, commitSHA, inventoryHash, profileHash, campaignID string,
	assignmentCatalog []RepositoryReviewAssignment,
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
	campaignID = strings.TrimSpace(campaignID)
	commitSHA = strings.ToLower(commitSHA)
	normalizedCatalog, catalogErr := NormalizeRepositoryReviewAssignmentCatalog(assignmentCatalog)
	if !validBoundedText(repository, maxRepositoryIdentityBytes) ||
		!validBoundedText(commitSHA, 256) || !validBoundedText(inventoryHash, 256) ||
		!validBoundedText(profileHash, 256) || !ValidRepositoryReviewCampaignID(campaignID) ||
		!authoritative || catalogErr != nil {
		return Plan{}, fmt.Errorf("%w: repository, commit SHA, and inventory hash are required", ErrInvalidPlan)
	}
	assignmentCatalog = normalizedCatalog
	requiredAssignments := repositoryReviewRequiredAssignmentCount(assignmentCatalog)
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
	scopeDigest, _ := repositoryReviewCampaignScopeDigestForFiles(files)
	campaignChanged, err := bindRepositoryReviewCampaignAssignmentCatalog(
		&state, campaignID, commitSHA, inventoryHash, profileHash, scopeDigest,
		assignmentCatalog, len(files),
	)
	if err != nil {
		return Plan{}, err
	}
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
		if state.CurrentCampaign.Paths[file.Path].Unsupported {
			unsupported := state.Unsupported[file.Path]
			unsupported.FileRef = file
			unsupported.CommitSHA = commitSHA
			unsupported.ProfileHash = profileHash
			if strings.TrimSpace(unsupported.Reason) == "" {
				unsupported.Reason = "campaign_terminal"
			}
			planUnsupported = append(planUnsupported, unsupported)
			continue
		}
		if unsupported, exists := state.Unsupported[file.Path]; exists &&
			unsupported.BlobSHA == file.BlobSHA && unsupported.SizeBytes == file.SizeBytes &&
			unsupported.Mode == file.Mode && unsupported.ProfileHash == profileHash &&
			(!force || unsupported.ForceCampaignID == forceCampaignID) {
			// Classification metadata is inventory-owned. Preserve the durable
			// terminal reason/provenance while rebinding the exact current FileRef.
			unsupported.FileRef = file
			planUnsupported = append(planUnsupported, unsupported)
			continue
		}
		_, reviewed := state.Files[file.Path]
		if reviewed {
			previouslyReviewed++
		}
		campaignComplete := false
		if pathCoverage, exists := state.CurrentCampaign.Paths[file.Path]; exists &&
			!pathCoverage.Unsupported {
			projected, projectionErr := projectRepositoryReviewAssignmentCoverage(
				pathCoverage, assignmentCatalog,
			)
			if projectionErr != nil {
				return Plan{}, projectionErr
			}
			campaignComplete = projected.Completed
		}
		if campaignComplete {
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
		CampaignID: campaignID,
		Repository: repository, CommitSHA: commitSHA, InventoryHash: inventoryHash,
		ProfileHash: profileHash, RequiredAssignments: requiredAssignments,
		AssignmentCatalog: append([]RepositoryReviewAssignment(nil), assignmentCatalog...),
		ForceCampaignID:   forceCampaignID, Authoritative: authoritative,
		TargetIsDefault: true,
		StateVersion:    state.ReviewVersion, PendingFiles: pending, DeferredFiles: deferred,
		UnchangedFiles:     unchanged,
		UnsupportedFiles:   planUnsupported,
		PreviouslyReviewed: previouslyReviewed, CreatedAt: now,
	}
	plan.AssignmentPlans = make([]RepositoryReviewAssignmentPlan, 0, len(assignmentCatalog))
	for _, assignment := range assignmentCatalog {
		missing := make([]FileRef, 0, len(pending))
		for _, file := range pending {
			complete, assignmentErr := repositoryReviewAssignmentComplete(
				state.CurrentCampaign.Paths[file.Path], assignmentCatalog, assignment.ID,
			)
			if assignmentErr != nil {
				return Plan{}, assignmentErr
			}
			if !complete {
				missing = append(missing, file)
			}
		}
		if len(missing) == 0 {
			continue
		}
		reviewerModel := assignment.Reviewer
		if reviewerModel == "default" {
			reviewerModel = ""
		}
		plan.AssignmentPlans = append(plan.AssignmentPlans, RepositoryReviewAssignmentPlan{
			AssignmentID: assignment.ID,
			FocusID:      assignment.FocusID,
			Label:        assignment.FocusID,
			Reviewer:     reviewerModel,
			Optional:     !assignment.Required,
			Files:        missing,
		})
	}
	for _, file := range unchanged {
		changed, coverageErr := mergeRepositoryReviewCampaignPath(
			state.CurrentCampaign, file.Path,
			RepositoryReviewCampaignPathCoverage{Completed: true},
		)
		if coverageErr != nil {
			return Plan{}, coverageErr
		}
		campaignChanged = campaignChanged || changed
	}
	for _, unsupported := range planUnsupported {
		changed, coverageErr := mergeRepositoryReviewCampaignPath(
			state.CurrentCampaign, unsupported.Path,
			RepositoryReviewCampaignPathCoverage{Unsupported: true},
		)
		if coverageErr != nil {
			return Plan{}, coverageErr
		}
		campaignChanged = campaignChanged || changed
	}
	if campaignChanged {
		state.Version++
		state.ReviewVersion++
		state.UpdatedAt = now
		if err := s.save(&state); err != nil {
			return Plan{}, err
		}
		plan.StateVersion = state.ReviewVersion
	}
	plan.ID = planDigest(plan)
	return plan, nil
}

// BindPlanBranch adds canonical branch provenance before a plan is dispatched.
func BindPlanBranch(
	plan Plan,
	targetBranch string,
	advertisedDefaultBranch string,
	targetIsDefault bool,
) (Plan, error) {
	var err error
	targetBranch, err = NormalizeRepositoryReviewBranch(targetBranch)
	if err != nil {
		return Plan{}, ErrInvalidPlan
	}
	advertisedDefaultBranch, err = NormalizeRepositoryReviewBranch(advertisedDefaultBranch)
	if err != nil || (targetBranch == "") != (advertisedDefaultBranch == "") ||
		targetBranch != "" && targetIsDefault != (targetBranch == advertisedDefaultBranch) ||
		targetBranch == "" && !targetIsDefault {
		return Plan{}, ErrInvalidPlan
	}
	plan.ID = ""
	plan.TargetBranch = targetBranch
	plan.AdvertisedDefaultBranch = advertisedDefaultBranch
	plan.TargetIsDefault = targetIsDefault
	plan.ID = planDigest(plan)
	return plan, nil
}

// SnapshotMappingJobs freezes the assigned reviewer/profile/account into
// newly created pending mapping jobs before dispatch. Existing snapshots are
// immutable, so retries after profile changes continue with original
// provenance.
func (s Store) SnapshotMappingJobs(
	repository string,
	findingIDs []string,
	snapshot RepositoryMappingModelSnapshot,
) (RepositoryState, error) {
	if err := validateMappingModelSnapshot(snapshot); err != nil || mappingModelSnapshotEmpty(snapshot) {
		if err == nil {
			err = errors.New("mapping model snapshot is required")
		}
		return RepositoryState{}, err
	}
	wanted := make(map[string]struct{}, len(findingIDs))
	for _, findingID := range findingIDs {
		if findingID = strings.TrimSpace(findingID); findingID != "" {
			wanted[findingID] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return RepositoryState{}, errors.New("mapping finding IDs are required")
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
	now := s.clock()
	changed := false
	for index := range state.MappingJobs {
		job := &state.MappingJobs[index]
		if _, ok := wanted[job.ReviewFindingID]; !ok || job.State == RepositoryMappingCompleted {
			continue
		}
		if !mappingModelSnapshotEmpty(job.ModelSnapshot) {
			if !mappingModelSnapshotsEqual(job.ModelSnapshot, snapshot) {
				return RepositoryState{}, ErrConflict
			}
			continue
		}
		job.ModelSnapshot = snapshot
		job.UpdatedAt = now
		changed = true
	}
	if !changed {
		return state, nil
	}
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, err
	}
	return state, nil
}

func (s Store) FinalizeNoopPlan(plan Plan, excludedFiles ...int) (RepositoryState, error) {
	if plan.ID == "" || plan.ID != planDigest(plan) || len(plan.PendingFiles) != 0 ||
		len(plan.DeferredFiles) != 0 || !plan.Authoritative {
		return RepositoryState{}, ErrInvalidPlan
	}
	if _, err := validateRepositoryReviewCampaignPlan(plan); err != nil {
		return RepositoryState{}, err
	}
	campaignSelectedFiles, campaignErr := validateRepositoryReviewCampaignPlan(plan)
	if campaignErr != nil {
		return RepositoryState{}, campaignErr
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
	scopeDigest, _ := repositoryReviewCampaignScopeDigestForPlan(plan)
	bound, bindErr := bindRepositoryReviewCampaignAssignmentCatalog(
		&state, plan.CampaignID, plan.CommitSHA, plan.InventoryHash, plan.ProfileHash, scopeDigest,
		plan.AssignmentCatalog, campaignSelectedFiles,
	)
	if bindErr != nil {
		return RepositoryState{}, bindErr
	}
	changed := bound
	for _, file := range plan.UnchangedFiles {
		merged, mergeErr := mergeRepositoryReviewCampaignPath(
			state.CurrentCampaign, file.Path,
			RepositoryReviewCampaignPathCoverage{Completed: true},
		)
		if mergeErr != nil {
			return RepositoryState{}, mergeErr
		}
		changed = changed || merged
	}
	for _, unsupported := range plan.UnsupportedFiles {
		merged, mergeErr := mergeRepositoryReviewCampaignPath(
			state.CurrentCampaign, unsupported.Path,
			RepositoryReviewCampaignPathCoverage{Unsupported: true},
		)
		if mergeErr != nil {
			return RepositoryState{}, mergeErr
		}
		changed = changed || merged
	}
	changed = pruneCheckpointMetadata(&state, plan, nil) || changed
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
	unlock, err := s.lock("repository-review-state-id:" + id)
	if err != nil {
		return RepositoryState{}, false, err
	}
	defer unlock()
	if _, purging, purgeErr := s.loadPurgeFenceForStateID(id); purgeErr != nil {
		return RepositoryState{}, false, purgeErr
	} else if purging {
		return RepositoryState{}, false, ErrRepositoryReviewPurgeInProgress
	}
	database, err := s.openDatabase(context.Background())
	if err != nil {
		return RepositoryState{}, false, err
	}
	defer database.Close()
	state, err := loadRepositoryStateRow(context.Background(), database, id)
	if errors.Is(err, sql.ErrNoRows) {
		return RepositoryState{}, false, nil
	}
	if err != nil {
		return RepositoryState{}, false, err
	}
	return state, true, nil
}

func (s Store) ListSummaries() ([]RepositorySummary, error) {
	unlock, err := s.lock("repository-review-list-summaries")
	if err != nil {
		return nil, err
	}
	defer unlock()
	return s.listSummaries(10_000)
}

func (s Store) listSummaries(maximum int) ([]RepositorySummary, error) {
	database, err := s.openDatabase(context.Background())
	if err != nil {
		return nil, err
	}
	defer database.Close()
	rows, err := database.Query(`
		SELECT schema_version, state_id, repository, version, review_version, last_commit_sha,
		       finding_count, repository_finding_count, open_finding_count, issue_draft_count,
		       unsupported_count, reviewed_file_count, excluded_file_count, updated_at_unix_nano
		  FROM repository_review_states
	 ORDER BY updated_at_unix_nano DESC, state_id ASC
	 LIMIT ?`, maximum+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := make([]RepositorySummary, 0)
	for rows.Next() {
		if len(summaries) >= maximum {
			return nil, errors.New("repository review catalog exceeds its repository limit")
		}
		var summary RepositorySummary
		var updated int64
		if err := rows.Scan(
			&summary.SchemaVersion, &summary.ID, &summary.Repository, &summary.Version,
			&summary.ReviewVersion, &summary.LastCommitSHA, &summary.FindingCount,
			&summary.RepositoryFindingCount, &summary.OpenFindingCount, &summary.IssueDraftCount,
			&summary.UnsupportedCount, &summary.ReviewedFileCount, &summary.ExcludedFileCount,
			&updated,
		); err != nil {
			return nil, err
		}
		summary.UpdatedAt = time.Unix(0, updated).UTC()
		if _, purging, purgeErr := s.loadPurgeFence(summary.Repository); purgeErr != nil {
			return nil, purgeErr
		} else if purging {
			return nil, ErrRepositoryReviewPurgeInProgress
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
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
	if publicationState == IssueDraftPosted {
		for _, findingID := range draft.FindingIDs {
			findingIndex := findingIndexByID(state.Findings, findingID)
			if findingIndex < 0 ||
				!repositoryFindingAllowsIssueActions(state, state.Findings[findingIndex]) {
				return RepositoryState{}, IssueDraft{}, ErrConflict
			}
		}
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
	eligibility := EvaluateIssuePublication(state, *draft)
	if draft.State == IssueDraftPosted {
		if !eligibility.AllowsPostedAcknowledgement() {
			return RepositoryState{}, IssueDraft{}, false, ErrConflict
		}
		return state, *draft, false, nil
	}
	if !eligibility.CanPublish {
		return RepositoryState{}, IssueDraft{}, false, ErrConflict
	}
	if draft.State == IssueDraftPublishing || draft.State == IssueDraftUnknown {
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
	unlock, err := s.lock("repository-review-list")
	if err != nil {
		return nil, err
	}
	defer unlock()
	return s.listStates(true)
}

func (s Store) listStates(enforcePurgeFence bool) ([]RepositoryState, error) {
	database, err := s.openDatabase(context.Background())
	if err != nil {
		return nil, err
	}
	defer database.Close()
	rows, err := database.Query(`
		SELECT state_id FROM repository_review_states
	 ORDER BY updated_at_unix_nano DESC, state_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make([]RepositoryState, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		state, err := loadRepositoryStateRow(context.Background(), database, id)
		if err != nil {
			return nil, err
		}
		if enforcePurgeFence {
			if _, purging, purgeErr := s.loadPurgeFence(state.Repository); purgeErr != nil {
				return nil, purgeErr
			} else if purging {
				return nil, ErrRepositoryReviewPurgeInProgress
			}
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

func (s Store) load(repository string) (RepositoryState, error) {
	if _, purging, err := s.loadPurgeFence(repository); err != nil {
		return RepositoryState{}, err
	} else if purging {
		return RepositoryState{}, ErrRepositoryReviewPurgeInProgress
	}
	return s.loadIgnoringPurge(repository)
}

func (s Store) loadIgnoringPurge(repository string) (RepositoryState, error) {
	if s.loadForTest != nil {
		return s.loadForTest(repository)
	}
	state := RepositoryState{
		SchemaVersion:           SchemaVersion,
		ID:                      RepositoryID(repository),
		Repository:              strings.TrimSpace(repository),
		Files:                   make(map[string]ReviewedFile),
		Unsupported:             make(map[string]UnsupportedFile),
		ReviewAttempts:          make(map[string]int),
		ReviewAttemptIdentities: make(map[string]string),
		Findings:                []Finding{},
		RawFindings:             []RawReviewFinding{},
		DeduplicationJobs:       []DeduplicationJob{},
		Contexts:                []FindingContext{},
		Runs:                    []ReviewRun{},
		FileAttributions:        []RepositoryReviewFileAttribution{},
		IssueDrafts:             []IssueDraft{},
		RepositoryFindings:      []RepositoryFinding{},
		MappingJobs:             []RepositoryMappingJob{},
		ValidationJobs:          []RepositoryValidationJob{},
		CampaignHistory:         make(map[string]string),
	}
	database, err := s.openDatabase(context.Background())
	if err != nil {
		return RepositoryState{}, err
	}
	defer database.Close()
	loaded, err := loadRepositoryStateRow(context.Background(), database, state.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return RepositoryState{}, err
	}
	return loaded, nil
}

func (s Store) save(state *RepositoryState) error {
	if err := prepareRepositoryStateForPersistence(state); err != nil {
		return err
	}
	database, err := s.openDatabase(context.Background())
	if err != nil {
		return err
	}
	defer database.Close()
	return saveRepositoryStateDatabase(context.Background(), database, state)
}

func prepareRepositoryStateForPersistence(state *RepositoryState) error {
	if state == nil {
		return errors.New("repository review state is required")
	}
	if state.FileAttributions == nil {
		state.FileAttributions = []RepositoryReviewFileAttribution{}
	}
	synchronizeRepositoryFindingIssues(state)
	summary := Summarize(*state)
	state.FindingCount = summary.FindingCount
	state.RepositoryFindingCount = summary.RepositoryFindingCount
	state.OpenFindingCount = summary.OpenFindingCount
	state.IssueDraftCount = summary.IssueDraftCount
	state.UnsupportedCount = summary.UnsupportedCount
	state.ReviewedFileCount = summary.ReviewedFileCount
	if err := validateState(*state); err != nil {
		return err
	}
	return nil
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
		file.BlobSHA = strings.ToLower(strings.TrimSpace(file.BlobSHA))
		file.Category = strings.TrimSpace(file.Category)
		file.Mode = strings.TrimSpace(file.Mode)
		if !validRepositoryReviewPath(file.Path) ||
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

func validRepositoryReviewPath(pathValue string) bool {
	if !validBoundedText(pathValue, 4096) || pathValue == "." ||
		pathValue != strings.TrimSpace(filepath.ToSlash(pathValue)) {
		return false
	}
	cleanPath := path.Clean(pathValue)
	return cleanPath == pathValue && !strings.HasPrefix(cleanPath, "../") &&
		!strings.HasPrefix(cleanPath, "/")
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

func validateRepositoryReviewCampaignPlan(plan Plan) (int, error) {
	if !ValidRepositoryReviewCampaignID(plan.CampaignID) ||
		!plan.Authoritative ||
		plan.CampaignID != strings.TrimSpace(plan.CampaignID) ||
		!validBoundedText(plan.Repository, maxRepositoryIdentityBytes) ||
		plan.Repository != strings.TrimSpace(plan.Repository) ||
		!validRepositoryReviewCommitSHA(plan.CommitSHA) ||
		plan.CommitSHA != strings.ToLower(strings.TrimSpace(plan.CommitSHA)) ||
		!validBoundedText(strings.TrimSpace(plan.InventoryHash), 256) ||
		plan.InventoryHash != strings.TrimSpace(plan.InventoryHash) ||
		!validBoundedText(strings.TrimSpace(plan.ProfileHash), 256) ||
		plan.ProfileHash != strings.TrimSpace(plan.ProfileHash) ||
		(plan.ForceCampaignID != "" &&
			(plan.ForceCampaignID != strings.TrimSpace(plan.ForceCampaignID) ||
				!validBoundedText(plan.ForceCampaignID, 256))) ||
		plan.RequiredAssignments < 1 || plan.RequiredAssignments > maxRepositoryReviewRequiredAssignments ||
		len(plan.AssignmentCatalog) == 0 ||
		plan.StateVersion < 0 || plan.PreviouslyReviewed < 0 || plan.PreviouslyReviewed > maxReviewFiles {
		return 0, ErrInvalidPlan
	}
	catalog, err := NormalizeRepositoryReviewAssignmentCatalog(plan.AssignmentCatalog)
	if err != nil || !repositoryReviewAssignmentCatalogEqual(catalog, plan.AssignmentCatalog) ||
		catalog[0].ProfileHash != plan.ProfileHash ||
		repositoryReviewRequiredAssignmentCount(catalog) != plan.RequiredAssignments {
		return 0, ErrInvalidPlan
	}
	allowed := make(map[string]FileRef, len(plan.PendingFiles))
	for _, file := range plan.PendingFiles {
		allowed[file.Path] = file
	}
	plans, planErr := normalizeRepositoryReviewAssignmentPlans(
		plan.AssignmentPlans, catalog, allowed,
	)
	if planErr != nil || len(plans) != len(plan.AssignmentPlans) ||
		len(plans) > 0 && !reflect.DeepEqual(plans, plan.AssignmentPlans) {
		return 0, ErrInvalidPlan
	}
	selected := make(map[string]struct{})
	for _, group := range [][]FileRef{plan.PendingFiles, plan.DeferredFiles, plan.UnchangedFiles} {
		canonical, groupErr := canonicalRepositoryReviewCampaignFiles(group)
		if groupErr != nil || len(canonical) != len(group) {
			return 0, ErrInvalidPlan
		}
		for _, file := range canonical {
			if _, duplicate := selected[file.Path]; duplicate {
				return 0, ErrInvalidPlan
			}
			selected[file.Path] = struct{}{}
		}
	}
	unsupportedRefs := make([]FileRef, 0, len(plan.UnsupportedFiles))
	for _, unsupported := range plan.UnsupportedFiles {
		if !validBoundedText(strings.TrimSpace(unsupported.Reason), 256) ||
			unsupported.Reason != strings.TrimSpace(unsupported.Reason) {
			return 0, ErrInvalidPlan
		}
		unsupportedRefs = append(unsupportedRefs, unsupported.FileRef)
	}
	canonicalUnsupported, err := canonicalRepositoryReviewCampaignFiles(unsupportedRefs)
	if err != nil || len(canonicalUnsupported) != len(unsupportedRefs) {
		return 0, ErrInvalidPlan
	}
	for _, file := range canonicalUnsupported {
		if _, duplicate := selected[file.Path]; duplicate {
			return 0, ErrInvalidPlan
		}
		selected[file.Path] = struct{}{}
	}
	if len(selected) > maxReviewFiles {
		return 0, ErrInvalidPlan
	}
	return len(selected), nil
}

func repositoryReviewCampaignScopeDigestForPlan(plan Plan) (string, error) {
	files, err := repositoryReviewCampaignFilesForPlan(plan)
	if err != nil {
		return "", err
	}
	return repositoryReviewCampaignScopeDigestForFiles(files)
}

func repositoryReviewCampaignFilesForPlan(plan Plan) ([]FileRef, error) {
	files := make([]FileRef, 0,
		len(plan.PendingFiles)+len(plan.DeferredFiles)+len(plan.UnchangedFiles)+len(plan.UnsupportedFiles),
	)
	files = append(files, plan.PendingFiles...)
	files = append(files, plan.DeferredFiles...)
	files = append(files, plan.UnchangedFiles...)
	for _, unsupported := range plan.UnsupportedFiles {
		files = append(files, unsupported.FileRef)
	}
	return canonicalRepositoryReviewCampaignFiles(files)
}

func repositoryReviewCampaignScopeDigestForFiles(files []FileRef) (string, error) {
	canonical, err := canonicalRepositoryReviewCampaignFiles(files)
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal(canonical)
	return stableID("sha256:", string(data)), nil
}

func canonicalRepositoryReviewCampaignFiles(files []FileRef) ([]FileRef, error) {
	canonical, err := normalizeFiles(files)
	if err != nil {
		return nil, err
	}
	byPath := make(map[string]FileRef, len(canonical))
	for _, file := range canonical {
		byPath[file.Path] = file
	}
	for _, file := range files {
		if normalized, exists := byPath[file.Path]; !exists || normalized != file {
			return nil, ErrInvalidPlan
		}
	}
	return canonical, nil
}

func bindRepositoryReviewCampaignFiles(
	files []FileRef,
	allowed map[string]FileRef,
) ([]FileRef, error) {
	canonical, err := canonicalRepositoryReviewCampaignFiles(files)
	if err != nil {
		return nil, err
	}
	for _, file := range canonical {
		trusted, ok := allowed[file.Path]
		if !ok || trusted != file {
			return nil, fmt.Errorf("%w: file %q is outside the exact pending plan", ErrInvalidPlan, file.Path)
		}
	}
	return canonical, nil
}

func containsRepositoryReviewFile(files []FileRef, pathValue string) bool {
	for _, file := range files {
		if file.Path == pathValue {
			return true
		}
	}
	return false
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
	candidate.Validation.Status = strings.ToLower(strings.TrimSpace(candidate.Validation.Status))
	candidate.Validation.Summary = strings.TrimSpace(candidate.Validation.Summary)
	candidate.MatchHints.Component = strings.TrimSpace(candidate.MatchHints.Component)
	candidate.MatchHints.Operation = strings.TrimSpace(candidate.MatchHints.Operation)
	candidate.MatchHints.FailureMode = strings.TrimSpace(candidate.MatchHints.FailureMode)
	candidate.MatchHints.Trigger = strings.TrimSpace(candidate.MatchHints.Trigger)
	candidate.MatchHints.ViolatedInvariant = strings.TrimSpace(candidate.MatchHints.ViolatedInvariant)
	candidate.MatchHints.ObservableOutcome = strings.TrimSpace(candidate.MatchHints.ObservableOutcome)
	candidate.MatchHints.RelatedSymbols = normalizeFindingIdentityHints(
		candidate.MatchHints.RelatedSymbols,
	)
	candidate.MatchHints.SourceAnchors = normalizeFindingIdentityHints(
		candidate.MatchHints.SourceAnchors,
	)
	candidate.MatchHints.DistinguishingFacts = normalizeFindingIdentityHints(
		candidate.MatchHints.DistinguishingFacts,
	)
	candidate.FixEffort.Quick.Class = strings.ToLower(strings.TrimSpace(candidate.FixEffort.Quick.Class))
	candidate.FixEffort.Quick.Rationale = strings.TrimSpace(candidate.FixEffort.Quick.Rationale)
	candidate.FixEffort.Quality.Class = strings.ToLower(strings.TrimSpace(candidate.FixEffort.Quality.Class))
	candidate.FixEffort.Quality.Rationale = strings.TrimSpace(candidate.FixEffort.Quality.Rationale)
	return candidate
}

// NormalizeRepositoryReviewFindingCandidate returns the detached canonical form.
func NormalizeRepositoryReviewFindingCandidate(candidate FindingCandidate) FindingCandidate {
	return normalizeCandidate(candidate)
}

func normalizeFindingIdentityHints(values []string) []string {
	if values == nil {
		return nil
	}
	normalized := make([]string, len(values))
	for index, value := range values {
		normalized[index] = strings.TrimSpace(value)
	}
	return normalized
}

func validateCandidate(candidate FindingCandidate) error {
	switch candidate.Severity {
	case "critical", "high", "medium", "low":
	default:
		return errors.New("invalid severity")
	}
	if candidate.Title == "" || candidate.Symbol == "" || candidate.File == "" || candidate.Message == "" ||
		candidate.Evidence == "" || candidate.Impact == "" || candidate.Validation.Status != "confirmed" ||
		candidate.Validation.Summary == "" || !findingCandidateHasEnrichment(candidate) {
		return errors.New("finding is incomplete")
	}
	for _, value := range []string{
		candidate.Title, candidate.File, candidate.Evidence,
		candidate.Impact, candidate.Validation.Summary,
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
	if err := validateMatchHints(candidate.MatchHints); err != nil {
		return err
	}
	if err := validateFixEffort(candidate.FixEffort); err != nil {
		return err
	}
	return nil
}

// ValidateGeneratedFindingCandidate normalizes and validates the current finder contract.
func ValidateGeneratedFindingCandidate(candidate FindingCandidate) error {
	candidate = normalizeCandidate(candidate)
	return validateCandidate(candidate)
}

func findingCandidateHasEnrichment(candidate FindingCandidate) bool {
	hints := candidate.MatchHints
	return hints.Component != "" || hints.Operation != "" || hints.FailureMode != "" ||
		hints.Trigger != "" || hints.ViolatedInvariant != "" || hints.ObservableOutcome != "" ||
		len(hints.RelatedSymbols) > 0 || len(hints.SourceAnchors) > 0 ||
		len(hints.DistinguishingFacts) > 0 || candidate.FixEffort != (FixEffort{})
}

func validateMatchHints(hints MatchHints) error {
	for _, value := range []string{
		hints.Component, hints.Operation, hints.FailureMode, hints.Trigger,
		hints.ViolatedInvariant, hints.ObservableOutcome,
	} {
		if !validBoundedText(value, maxMatchHintIdentityBytes) {
			return errors.New("finding match hints are incomplete or invalid")
		}
	}
	for _, values := range [][]string{
		hints.RelatedSymbols, hints.SourceAnchors, hints.DistinguishingFacts,
	} {
		if len(values) > maxMatchHintItems {
			return errors.New("finding match hints have too many identity values")
		}
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			if !validBoundedText(value, maxMatchHintIdentityBytes) ||
				strings.ContainsAny(value, "\r\n") {
				return errors.New("finding match hint identity value is invalid")
			}
			key := normalizedText(value)
			if _, duplicate := seen[key]; duplicate {
				return errors.New("finding match hints contain a duplicate identity value")
			}
			seen[key] = struct{}{}
		}
	}
	return nil
}

func validateFixEffort(effort FixEffort) error {
	if err := validateFixEffortEstimate(effort.Quick); err != nil {
		return err
	}
	if err := validateFixEffortEstimate(effort.Quality); err != nil {
		return err
	}
	if effort.Quick.LOCMin > effort.Quality.LOCMin ||
		effort.Quick.LOCMax > effort.Quality.LOCMax {
		return errors.New("finding quality fix effort must not be smaller than quick containment")
	}
	return nil
}

func validateFixEffortEstimate(estimate FixEffortEstimate) error {
	if estimate.LOCMin < 1 || estimate.LOCMin > estimate.LOCMax ||
		estimate.LOCMax > maxFixEffortLOC ||
		!validBoundedText(estimate.Rationale, maxMatchHintIdentityBytes) {
		return errors.New("finding fix effort range or rationale is invalid")
	}
	wantClass := "refactor"
	switch {
	case estimate.LOCMax <= 10:
		wantClass = "tiny"
	case estimate.LOCMax <= 40:
		wantClass = "small"
	case estimate.LOCMax <= 150:
		wantClass = "medium"
	case estimate.LOCMax <= 500:
		wantClass = "large"
	}
	rationale := strings.ToLower(estimate.Rationale)
	architecturalRefactor := estimate.Class == "refactor" &&
		strings.Contains(rationale, "cross-subsystem") &&
		(strings.Contains(rationale, "architectural") ||
			strings.Contains(rationale, "contract migration"))
	if estimate.Class != wantClass && !architecturalRefactor {
		return errors.New("finding fix effort class is inconsistent with its maximum LOC")
	}
	return nil
}

func findingFingerprint(file FileRef, candidate FindingCandidate) string {
	line := 0
	if candidate.Line != nil {
		line = *candidate.Line
	}
	values := []string{
		file.Path, file.BlobSHA, fmt.Sprint(line),
		normalizedText(candidate.Symbol), normalizedText(candidate.Title),
		normalizedText(candidate.Message), normalizedText(candidate.Evidence),
	}
	if findingCandidateHasEnrichment(candidate) {
		encodedHints, _ := json.Marshal(candidate.MatchHints)
		values = append(values, string(encodedHints))
	}
	return stableID("sha256:", values...)
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
	contextID, model, modelAlias, account, reviewer string,
) FindingObservation {
	return FindingObservation{
		ContextID: contextID, Model: strings.TrimSpace(model),
		ModelAlias: strings.TrimSpace(modelAlias), Account: strings.TrimSpace(account),
		Reviewer: strings.TrimSpace(reviewer),
		Severity: candidate.Severity, Title: candidate.Title, Symbol: candidate.Symbol,
		Line: candidate.Line, Message: candidate.Message, Evidence: candidate.Evidence,
		Impact: candidate.Impact, Validation: candidate.Validation,
		MatchHints: candidate.MatchHints, FixEffort: candidate.FixEffort,
	}
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
	for _, raw := range state.RawFindings {
		if raw.ContextID != "" {
			referenced[raw.ContextID] = struct{}{}
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
		len(state.Findings) > 100_000 || len(state.RawFindings) > 100_000 ||
		len(state.DeduplicationJobs) > 100_000 ||
		len(state.Runs) > 100_000 || len(state.IssueDrafts) > 100_000 {
		return errors.New("invalid repository review state")
	}
	if err := validateRepositoryReviewCampaignCoverage(state.CurrentCampaign); err != nil {
		return err
	}
	if err := validateRepositoryReviewCampaignHistory(state.CampaignHistory); err != nil {
		return err
	}
	if err := validateRepositoryReviewActiveRun(state); err != nil {
		return err
	}
	if err := validateRepositoryReviewFileAttributions(state.FileAttributions); err != nil {
		return err
	}
	if err := validateDeduplicationState(state); err != nil {
		return err
	}
	if state.CurrentCampaign != nil &&
		state.CampaignHistory[state.CurrentCampaign.ID] != state.CurrentCampaign.CommitSHA {
		return errors.New("current repository review campaign is absent from history")
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
		if len(finding.Observations) > maxFindingContributorObservations ||
			len(finding.ContextIDs) > maxFindingContributorObservations ||
			!ValidRepositoryReviewCampaignID(finding.CampaignID) ||
			state.CampaignHistory[finding.CampaignID] != finding.CommitSHA {
			return errors.New("invalid repository review finding observations")
		}
	}
	for _, contextRecord := range state.Contexts {
		if !ValidRepositoryReviewCampaignID(contextRecord.CampaignID) ||
			state.CampaignHistory[contextRecord.CampaignID] != contextRecord.CommitSHA ||
			!validBoundedText(contextRecord.ProfileHash, 256) ||
			contextRecord.ID != stableID("rctx_", contextBindingDigest(contextRecord)) {
			return errors.New("invalid repository review finding context campaign")
		}
	}
	for _, run := range state.Runs {
		if run.InspectedFiles < 0 || run.InspectedFiles > maxReviewFiles ||
			!ValidRepositoryReviewCampaignID(run.CampaignID) ||
			state.CampaignHistory[run.CampaignID] != run.CommitSHA ||
			!validBoundedText(run.ProfileHash, 256) ||
			!validRepositoryReviewCampaignScopeDigest(run.ScopeDigest) ||
			!validRepositoryReviewBranchProvenance(
				run.TargetBranch, run.AdvertisedDefaultBranch, run.TargetIsDefault,
			) ||
			run.InspectedFiles > run.ReviewedFiles+run.UnreviewedFiles+run.UnsupportedCount {
			return errors.New("invalid repository review run campaign")
		}
		if len(run.CheckpointDigests) > maxRepositoryReviewRequiredAssignments {
			return errors.New("invalid repository review run checkpoint digests")
		}
		if len(run.CheckpointScopes) != len(run.CheckpointDigests) {
			return errors.New("invalid repository review run checkpoint scopes")
		}
		for assignmentID, files := range run.CheckpointScopes {
			canonical, err := canonicalRepositoryReviewCampaignFiles(files)
			if _, found := run.CheckpointDigests[assignmentID]; !found || err != nil ||
				len(canonical) == 0 || !reflect.DeepEqual(canonical, files) {
				return errors.New("invalid repository review run checkpoint scope")
			}
		}
		for assignmentID, digest := range run.CheckpointDigests {
			if !validBoundedText(assignmentID, 128) ||
				!validRepositoryReviewCheckpointDigest(digest) {
				return errors.New("invalid repository review run checkpoint digest")
			}
		}
	}
	if err := validateRepositoryReviewCampaignRecordBindings(state); err != nil {
		return err
	}
	if err := validateIssueAssociations(state); err != nil {
		return err
	}
	if err := validateRepositoryLifecycleState(state); err != nil {
		return err
	}
	return nil
}

func validBoundedText(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && len(value) <= maximum && !strings.ContainsRune(value, 0)
}

func validFindingSourceProvenance(model, modelAlias, account string) bool {
	return validBoundedText(model, 256) && validBoundedText(modelAlias, 256) &&
		validBoundedText(account, 256)
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
