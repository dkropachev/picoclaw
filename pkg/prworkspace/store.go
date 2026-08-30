package prworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrNotFound        = errors.New("PR workspace not found")
	ErrConflict        = errors.New("PR workspace version conflict")
	ErrRequestConflict = errors.New("PR workspace request ID conflict")
	ErrInvalid         = errors.New("invalid PR workspace request")
	ErrUnsafeProvider  = errors.New("unsafe development provider")
)

type ListFilter struct {
	RepositoryID string
	Repository   string
	Phase        Phase
	State        ExecutionState
	Owned        *bool
	NeedsAction  *bool
	AfterUpdated time.Time
	AfterID      string
	Limit        int
}

type Page struct {
	Workspaces []Workspace
	Next       *WorkspaceCursor
}

type WorkspaceCursor struct {
	UpdatedAt time.Time
	ID        string
}

type CreateInput struct {
	RequestID string
	Workspace Workspace
	Provider  ProviderSnapshot
}

// AggregatePatch is one atomic lifecycle transition. Domain services produce
// it; stores only enforce shape, CAS, idempotency, and referential persistence.
type AggregatePatch struct {
	Phase               *Phase
	ExecutionState      *ExecutionState
	ActiveCharterID     *string
	Provider            *ProviderSnapshot
	AppendCharters      []Charter
	ReplaceCharters     []Charter
	AppendStageRuns     []StageRun
	ReplaceStageRuns    []StageRun
	UpsertFindings      []Finding
	AppendMessages      []Message
	AppendCorrections   []Correction
	ReplaceCorrections  []Correction
	AppendLessons       []RepositoryLesson
	ReplaceLessons      []RepositoryLesson
	AppendNudgeRounds   []NudgeRoundRecord
	ReplaceNudgeRounds  []NudgeRoundRecord
	UpsertDeferred      []DeferredGroup
	AppendRepairs       []RepairAttempt
	ReplaceRepairs      []RepairAttempt
	AppendValidations   []ValidationRun
	ReplaceValidations  []ValidationRun
	AppendGates         []GateRun
	ReplaceGates        []GateRun
	AppendPublications  []Publication
	ReplacePublications []Publication
	Activity            []Activity
}

type Mutation struct {
	WorkspaceID     string
	ExpectedVersion int64
	RequestID       string
	Patch           AggregatePatch

	// branchPublicationLeaseID is an internal capability used only to close or
	// recover a branch publication that is already running. While that external
	// side effect is in flight, every unrelated aggregate mutation is rejected
	// at this store boundary so the completion authorization cannot change after
	// the publisher has been claimed.
	branchPublicationLeaseID string
}

type MutationResult struct {
	Aggregate Aggregate
	Replayed  bool
}

type Store interface {
	Create(ctx context.Context, input CreateInput) (MutationResult, error)
	Get(ctx context.Context, workspaceID string) (Aggregate, error)
	List(ctx context.Context, filter ListFilter) (Page, error)
	Mutate(ctx context.Context, mutation Mutation) (MutationResult, error)
}

// publicationFencedStore turns a running branch publication into a durable
// workspace mutation lease. The guard deliberately sits immediately above the
// backing store: a mutation that read the aggregate before the lease was
// claimed loses the backing store CAS, while a mutation that reads it after the
// claim is rejected here. This closes the check/claim/push TOCTOU window even
// when separate Service instances share one durable store.
type publicationFencedStore struct {
	Store
}

func newPublicationFencedStore(store Store) Store {
	if _, alreadyWrapped := store.(*publicationFencedStore); alreadyWrapped {
		return store
	}
	return &publicationFencedStore{Store: store}
}

func (store *publicationFencedStore) Mutate(ctx context.Context, mutation Mutation) (MutationResult, error) {
	current, err := store.Store.Get(ctx, mutation.WorkspaceID)
	if err != nil {
		return MutationResult{}, err
	}
	// Let the backing store resolve stale-version requests so request-ID replay
	// keeps its normal semantics. A genuinely new stale request still conflicts.
	if current.Workspace.Version != mutation.ExpectedVersion {
		return store.Store.Mutate(ctx, mutation)
	}

	if running, found := runningBranchPublication(current.Publications); found {
		if mutation.branchPublicationLeaseID != running.ID ||
			!branchPublicationLeaseTransition(mutation.Patch, running.ID) {
			return MutationResult{Aggregate: current}, ErrConflict
		}
	} else if claimedID, claims := claimedBranchPublication(mutation.Patch); claims {
		if runningPublicationExists(current.Publications) ||
			!branchPublicationLeaseTransition(mutation.Patch, claimedID) {
			return MutationResult{Aggregate: current}, ErrConflict
		}
	}
	return store.Store.Mutate(ctx, mutation)
}

func runningBranchPublication(publications []Publication) (Publication, bool) {
	for _, publication := range publications {
		if publication.Kind == PublicationBranchPush && publication.State == ExecutionRunning {
			return publication, true
		}
	}
	return Publication{}, false
}

func runningPublicationExists(publications []Publication) bool {
	for _, publication := range publications {
		if publication.State == ExecutionRunning {
			return true
		}
	}
	return false
}

func claimedBranchPublication(patch AggregatePatch) (string, bool) {
	if len(patch.AppendPublications) != 0 || len(patch.ReplacePublications) != 1 {
		return "", false
	}
	publication := patch.ReplacePublications[0]
	return publication.ID, publication.Kind == PublicationBranchPush && publication.State == ExecutionRunning
}

func branchPublicationLeaseTransition(patch AggregatePatch, publicationID string) bool {
	if patch.ActiveCharterID != nil ||
		len(patch.AppendCharters) != 0 || len(patch.ReplaceCharters) != 0 ||
		len(patch.AppendStageRuns) != 0 || len(patch.ReplaceStageRuns) != 0 ||
		len(patch.UpsertFindings) != 0 || len(patch.AppendMessages) != 0 ||
		len(patch.AppendCorrections) != 0 || len(patch.ReplaceCorrections) != 0 ||
		len(patch.AppendLessons) != 0 || len(patch.ReplaceLessons) != 0 ||
		len(patch.AppendNudgeRounds) != 0 || len(patch.ReplaceNudgeRounds) != 0 ||
		len(patch.UpsertDeferred) != 0 || len(patch.AppendRepairs) != 0 ||
		len(patch.ReplaceRepairs) != 0 || len(patch.AppendValidations) != 0 ||
		len(patch.ReplaceValidations) != 0 || len(patch.AppendGates) != 0 ||
		len(patch.ReplaceGates) != 0 || len(patch.AppendPublications) != 0 ||
		len(patch.ReplacePublications) != 1 {
		return false
	}
	publication := patch.ReplacePublications[0]
	if publication.ID != publicationID || publication.Kind != PublicationBranchPush {
		return false
	}
	switch publication.State {
	case ExecutionRunning:
		return patch.Phase == nil && patch.ExecutionState == nil
	case ExecutionSucceeded:
		if patch.Provider != nil && (patch.Provider.Intent != IntentImplementFeature ||
			patch.Provider.PullRequestID == "" || patch.Provider.PullNumber < 1) {
			return false
		}
		if patch.Phase == nil && patch.ExecutionState == nil {
			return true
		}
		return patch.Phase != nil && *patch.Phase == PhaseComplete &&
			patch.ExecutionState != nil && *patch.ExecutionState == ExecutionSucceeded
	case ExecutionFailed, ExecutionUnknown, ExecutionQueued:
		return patch.Phase == nil && patch.ExecutionState == nil
	default:
		return false
	}
}

type memoryRequest struct {
	Fingerprint string
	Version     int64
}

// MemoryStore is a strict reference implementation used by domain tests and
// non-SQLite builds. Production uses the eventing adapter.
type MemoryStore struct {
	mu         sync.RWMutex
	aggregates map[string]Aggregate
	identities map[string]string
	requests   map[string]map[string]memoryRequest
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		aggregates: make(map[string]Aggregate), identities: make(map[string]string),
		requests: make(map[string]map[string]memoryRequest),
	}
}

func (store *MemoryStore) Create(ctx context.Context, input CreateInput) (MutationResult, error) {
	if err := ctx.Err(); err != nil {
		return MutationResult{}, err
	}
	if store == nil || !validRequestID(input.RequestID) || input.Workspace.ID == "" ||
		input.Workspace.Version != 1 || input.Workspace.RepositoryID == "" ||
		input.Workspace.SourceID == "" || input.Workspace.Intent != input.Provider.Intent ||
		input.Workspace.SourceKind != input.Provider.SourceKind ||
		input.Workspace.SourceID != input.Provider.SourceID ||
		input.Workspace.ProviderOrigin != input.Provider.ProviderOrigin ||
		input.Workspace.RepositoryID != input.Provider.RepositoryID ||
		input.Workspace.PullRequestID != input.Provider.PullRequestID {
		return MutationResult{}, ErrInvalid
	}
	fingerprintInput := input
	fingerprintInput.Workspace.CreatedAt = time.Time{}
	fingerprintInput.Workspace.UpdatedAt = time.Time{}
	fingerprintInput.Provider.ObservedAt = time.Time{}
	fingerprint, err := fingerprintValue(fingerprintInput)
	if err != nil {
		return MutationResult{}, ErrInvalid
	}
	identity := workspaceIdentity(
		input.Workspace.ProviderOrigin,
		input.Workspace.RepositoryID,
		input.Workspace.SourceKind,
		input.Workspace.SourceID,
	)
	store.mu.Lock()
	defer store.mu.Unlock()
	if existingID := store.identities[identity]; existingID != "" {
		existing := store.aggregates[existingID]
		request := store.requests[existingID][input.RequestID]
		if request.Fingerprint != "" && request.Fingerprint != fingerprint {
			return MutationResult{}, ErrRequestConflict
		}
		return MutationResult{Aggregate: cloneAggregate(existing), Replayed: true}, nil
	}
	now := input.Workspace.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	input.Workspace.CreatedAt = now.UTC()
	input.Workspace.UpdatedAt = now.UTC()
	aggregate := Aggregate{Workspace: input.Workspace, ProviderSnapshot: input.Provider}
	store.aggregates[input.Workspace.ID] = cloneAggregate(aggregate)
	store.identities[identity] = input.Workspace.ID
	store.requests[input.Workspace.ID] = map[string]memoryRequest{
		input.RequestID: {Fingerprint: fingerprint, Version: 1},
	}
	return MutationResult{Aggregate: cloneAggregate(aggregate)}, nil
}

func (store *MemoryStore) Get(ctx context.Context, workspaceID string) (Aggregate, error) {
	if err := ctx.Err(); err != nil {
		return Aggregate{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	aggregate, exists := store.aggregates[workspaceID]
	if !exists {
		return Aggregate{}, ErrNotFound
	}
	return cloneAggregate(aggregate), nil
}

func (store *MemoryStore) List(ctx context.Context, filter ListFilter) (Page, error) {
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	limit := filter.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return Page{}, ErrInvalid
	}
	store.mu.RLock()
	values := make([]Workspace, 0, len(store.aggregates))
	for _, aggregate := range store.aggregates {
		workspace := aggregate.Workspace
		if filter.RepositoryID != "" && workspace.RepositoryID != filter.RepositoryID ||
			filter.Repository != "" && !strings.EqualFold(workspace.Repository, filter.Repository) ||
			filter.Phase != "" && workspace.Phase != filter.Phase ||
			filter.State != "" && workspace.ExecutionState != filter.State ||
			filter.Owned != nil && aggregate.ProviderSnapshot.Owned != *filter.Owned ||
			filter.NeedsAction != nil && workspaceNeedsAction(workspace) != *filter.NeedsAction {
			continue
		}
		if !filter.AfterUpdated.IsZero() &&
			(workspace.UpdatedAt.After(filter.AfterUpdated) ||
				workspace.UpdatedAt.Equal(filter.AfterUpdated) && workspace.ID >= filter.AfterID) {
			continue
		}
		values = append(values, workspace)
	}
	store.mu.RUnlock()
	sort.Slice(values, func(i, j int) bool {
		if values[i].UpdatedAt.Equal(values[j].UpdatedAt) {
			return values[i].ID > values[j].ID
		}
		return values[i].UpdatedAt.After(values[j].UpdatedAt)
	})
	page := Page{}
	if len(values) > limit {
		last := values[limit-1]
		page.Next = &WorkspaceCursor{UpdatedAt: last.UpdatedAt, ID: last.ID}
		values = values[:limit]
	}
	page.Workspaces = append([]Workspace(nil), values...)
	return page, nil
}

func workspaceNeedsAction(workspace Workspace) bool {
	switch workspace.ExecutionState {
	case ExecutionWaitingGate, ExecutionWaitingUser, ExecutionFailed, ExecutionBlocked, ExecutionUnknown:
		return true
	default:
		return false
	}
}

func (store *MemoryStore) Mutate(ctx context.Context, mutation Mutation) (MutationResult, error) {
	if err := ctx.Err(); err != nil {
		return MutationResult{}, err
	}
	if store == nil || mutation.WorkspaceID == "" || mutation.ExpectedVersion < 1 ||
		!validRequestID(mutation.RequestID) {
		return MutationResult{}, ErrInvalid
	}
	fingerprint, err := fingerprintAggregatePatch(mutation.Patch, mutation.WorkspaceID)
	if err != nil {
		return MutationResult{}, ErrInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	aggregate, exists := store.aggregates[mutation.WorkspaceID]
	if !exists {
		return MutationResult{}, ErrNotFound
	}
	if previous := store.requests[mutation.WorkspaceID][mutation.RequestID]; previous.Fingerprint != "" {
		if previous.Fingerprint != fingerprint {
			return MutationResult{}, ErrRequestConflict
		}
		return MutationResult{Aggregate: cloneAggregate(aggregate), Replayed: true}, nil
	}
	if aggregate.Workspace.Version != mutation.ExpectedVersion {
		return MutationResult{Aggregate: cloneAggregate(aggregate)}, ErrConflict
	}
	if err := applyPatch(&aggregate, mutation.Patch); err != nil {
		return MutationResult{}, err
	}
	aggregate.Workspace.Version++
	aggregate.Workspace.UpdatedAt = time.Now().UTC()
	store.aggregates[mutation.WorkspaceID] = cloneAggregate(aggregate)
	store.requests[mutation.WorkspaceID][mutation.RequestID] = memoryRequest{
		Fingerprint: fingerprint, Version: aggregate.Workspace.Version,
	}
	return MutationResult{Aggregate: cloneAggregate(aggregate)}, nil
}

func applyPatch(aggregate *Aggregate, patch AggregatePatch) error {
	if patch.Phase != nil {
		aggregate.Workspace.Phase = *patch.Phase
	}
	if patch.ExecutionState != nil {
		aggregate.Workspace.ExecutionState = *patch.ExecutionState
	}
	if patch.ActiveCharterID != nil {
		aggregate.Workspace.ActiveCharterID = *patch.ActiveCharterID
	}
	if patch.Provider != nil {
		pullIdentityMatches := patch.Provider.PullRequestID == aggregate.Workspace.PullRequestID
		if aggregate.Workspace.Intent == IntentImplementFeature && aggregate.Workspace.PullRequestID == "" &&
			patch.Provider.Intent == IntentImplementFeature && patch.Provider.PullRequestID != "" &&
			patch.Provider.PullNumber > 0 {
			pullIdentityMatches = true
		}
		if patch.Provider.RepositoryID != aggregate.Workspace.RepositoryID ||
			!pullIdentityMatches ||
			patch.Provider.ProviderOrigin != aggregate.Workspace.ProviderOrigin {
			return ErrInvalid
		}
		aggregate.ProviderSnapshot = *patch.Provider
		aggregate.Workspace.Repository = patch.Provider.Repository
		aggregate.Workspace.PullRequestID = patch.Provider.PullRequestID
		aggregate.Workspace.PullNumber = patch.Provider.PullNumber
		aggregate.Workspace.ProviderHeadSHA = patch.Provider.HeadSHA
	}
	aggregate.Charters = replaceByID(
		aggregate.Charters,
		patch.ReplaceCharters,
		func(value Charter) string { return value.ID },
	)
	aggregate.Charters = append(aggregate.Charters, patch.AppendCharters...)
	aggregate.StageRuns = replaceByID(
		aggregate.StageRuns,
		patch.ReplaceStageRuns,
		func(value StageRun) string { return value.ID },
	)
	aggregate.StageRuns = append(aggregate.StageRuns, patch.AppendStageRuns...)
	for _, finding := range patch.UpsertFindings {
		if err := validateFindingSourceForWorkspace(finding, aggregate.Workspace.ID); err != nil {
			return err
		}
		if existing, index := findFinding(aggregate.Findings, finding.ID); index >= 0 &&
			!equalOptionalAIExecutionSource(existing.source, finding.source) {
			return ErrConflict
		}
	}
	aggregate.Findings = upsertByID(
		aggregate.Findings,
		patch.UpsertFindings,
		func(value Finding) string { return value.ID },
	)
	aggregate.Messages = append(aggregate.Messages, patch.AppendMessages...)
	aggregate.Corrections = replaceByID(
		aggregate.Corrections,
		patch.ReplaceCorrections,
		func(value Correction) string { return value.ID },
	)
	aggregate.Corrections = append(aggregate.Corrections, patch.AppendCorrections...)
	aggregate.RepositoryLessons = replaceByID(
		aggregate.RepositoryLessons,
		patch.ReplaceLessons,
		func(value RepositoryLesson) string { return value.ID },
	)
	aggregate.RepositoryLessons = append(aggregate.RepositoryLessons, patch.AppendLessons...)
	aggregate.NudgeRounds = replaceByID(
		aggregate.NudgeRounds,
		patch.ReplaceNudgeRounds,
		func(value NudgeRoundRecord) string { return value.ID },
	)
	aggregate.NudgeRounds = append(aggregate.NudgeRounds, patch.AppendNudgeRounds...)
	aggregate.DeferredGroups = upsertByID(
		aggregate.DeferredGroups,
		patch.UpsertDeferred,
		func(value DeferredGroup) string { return value.ID },
	)
	aggregate.RepairAttempts = replaceByID(
		aggregate.RepairAttempts,
		patch.ReplaceRepairs,
		func(value RepairAttempt) string { return value.ID },
	)
	aggregate.RepairAttempts = append(aggregate.RepairAttempts, patch.AppendRepairs...)
	aggregate.ValidationRuns = replaceByID(
		aggregate.ValidationRuns,
		patch.ReplaceValidations,
		func(value ValidationRun) string { return value.ID },
	)
	aggregate.ValidationRuns = append(aggregate.ValidationRuns, patch.AppendValidations...)
	aggregate.Gates = replaceByID(aggregate.Gates, patch.ReplaceGates, func(value GateRun) string { return value.ID })
	aggregate.Gates = append(aggregate.Gates, patch.AppendGates...)
	aggregate.Publications = replaceByID(
		aggregate.Publications,
		patch.ReplacePublications,
		func(value Publication) string { return value.ID },
	)
	aggregate.Publications = append(aggregate.Publications, patch.AppendPublications...)
	for _, activity := range patch.Activity {
		activity.Ordinal = int64(len(aggregate.Activity) + 1)
		aggregate.Activity = append(aggregate.Activity, activity)
	}
	return nil
}

func fingerprintAggregatePatch(patch AggregatePatch, workspaceID string) (string, error) {
	base, err := fingerprintValue(patch)
	if err != nil {
		return "", err
	}
	type findingSource struct {
		FindingID string             `json:"finding-id"`
		Source    *AIExecutionSource `json:"source,omitempty"`
	}
	sources := make([]findingSource, 0, len(patch.UpsertFindings))
	for _, finding := range patch.UpsertFindings {
		if err := validateFindingSourceForWorkspace(finding, workspaceID); err != nil {
			return "", err
		}
		sources = append(sources, findingSource{
			FindingID: finding.ID,
			Source:    cloneAIExecutionSource(finding.source),
		})
	}
	return fingerprintValue(struct {
		Base    string          `json:"base"`
		Sources []findingSource `json:"finding-sources"`
	}{Base: base, Sources: sources})
}

func validateFindingSourceForWorkspace(finding Finding, workspaceID string) error {
	if finding.source == nil {
		if finding.SourceAvailable {
			return ErrInvalid
		}
		return nil
	}
	if !finding.SourceAvailable || !validAIExecutionSource(finding.source) ||
		finding.source.WorkspaceID != workspaceID {
		return ErrInvalid
	}
	return nil
}

func equalOptionalAIExecutionSource(left, right *AIExecutionSource) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return sameAIExecutionSource(left, right)
}

func replaceByID[T any](current, replacements []T, id func(T) string) []T {
	if len(replacements) == 0 {
		return current
	}
	out := append([]T(nil), current...)
	for _, replacement := range replacements {
		for index := range out {
			if id(out[index]) == id(replacement) {
				out[index] = replacement
				break
			}
		}
	}
	return out
}

func upsertByID[T any](current, updates []T, id func(T) string) []T {
	out := append([]T(nil), current...)
	for _, update := range updates {
		found := false
		for index := range out {
			if id(out[index]) == id(update) {
				out[index] = update
				found = true
				break
			}
		}
		if !found {
			out = append(out, update)
		}
	}
	return out
}

func cloneAggregate(value Aggregate) Aggregate {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("clone PR workspace aggregate: %v", err))
	}
	var cloned Aggregate
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		panic(fmt.Sprintf("clone PR workspace aggregate: %v", err))
	}
	for index := range cloned.Gates {
		if index >= len(value.Gates) || value.Gates[index].runtime == nil {
			continue
		}
		runtime := *value.Gates[index].runtime
		runtime.PinnedPolicy = append(json.RawMessage(nil), runtime.PinnedPolicy...)
		runtime.PinnedSubject = append(json.RawMessage(nil), runtime.PinnedSubject...)
		cloned.Gates[index].runtime = &runtime
	}
	for index := range cloned.StageRuns {
		if index < len(value.StageRuns) {
			cloned.StageRuns[index].inputWorkspaceVersion = value.StageRuns[index].inputWorkspaceVersion
		}
	}
	for index := range cloned.Findings {
		if index < len(value.Findings) {
			cloned.Findings[index].source = cloneAIExecutionSource(value.Findings[index].source)
			cloned.Findings[index].SourceAvailable = cloned.Findings[index].source != nil
		}
	}
	for index := range cloned.RepairAttempts {
		if index >= len(value.RepairAttempts) || value.RepairAttempts[index].PublicationFence == nil {
			continue
		}
		fence := *value.RepairAttempts[index].PublicationFence
		cloned.RepairAttempts[index].PublicationFence = &fence
	}
	for index := range cloned.Publications {
		if index < len(value.Publications) {
			cloned.Publications[index].payload = append(
				json.RawMessage(nil),
				value.Publications[index].payload...,
			)
		}
	}
	return cloned
}

func fingerprintValue(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func workspaceIdentity(origin, repositoryID string, sourceKind SourceKind, sourceID string) string {
	return strings.ToLower(origin) + "\x00" + repositoryID + "\x00" + string(sourceKind) + "\x00" + sourceID
}

func validRequestID(value string) bool {
	if len(value) < 16 || len(value) > 128 || value != strings.TrimSpace(value) {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || strings.ContainsRune("._:-", char) {
			continue
		}
		return false
	}
	return true
}
