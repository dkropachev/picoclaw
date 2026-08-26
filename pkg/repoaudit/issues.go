package repoaudit

import (
	"errors"
	"net/url"
	"os"
	"sort"
	"strings"
)

const (
	maxIssueGenerationIDBytes    = 256
	maxIssueInstructionsBytes    = 16 << 10
	maxIssueGenerationErrorBytes = 1024
)

// IssueGenerationRequest is the durable reservation written before an
// isolated issue-writer call is dispatched.
type IssueGenerationRequest struct {
	Repository           string
	FindingID            string
	GenerationID         string
	ResolvedInstructions string
	InstructionsMode     IssueDraftInstructionsMode
	GeneratorModel       string
	GeneratorAccount     string
	ExpectedDraftVersion int64
}

// ExistingIssueLink is an issue identity that has already been re-fetched and
// validated by the protected GitHub boundary.
type ExistingIssueLink struct {
	Repository             string
	FindingID              string
	ExpectedFindingVersion int64
	ExternalID             string
	ExternalURL            string
	Title                  string
	Body                   string
	Labels                 []string
	Confirmed              bool
	Replace                bool
}

// SetFindingStatusByVersion is the automation-owned status mutation fence.
// The repository-ID compatibility route continues to fence the aggregate
// ledger version through SetFindingStatus.
func (s Store) SetFindingStatusByVersion(
	repository, findingID string,
	status FindingStatus,
	expectedFindingVersion int64,
) (RepositoryState, Finding, error) {
	if status != FindingOpen && status != FindingDismissed {
		return RepositoryState{}, Finding{}, errors.New("invalid repository review finding status")
	}
	repository = strings.TrimSpace(repository)
	findingID = strings.TrimSpace(findingID)
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, Finding{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, Finding{}, err
	}
	index := findingIndexByID(state.Findings, findingID)
	if index < 0 {
		return RepositoryState{}, Finding{}, os.ErrNotExist
	}
	finding := &state.Findings[index]
	if finding.Status == status {
		return state, *finding, nil
	}
	if finding.Status == FindingPosted || finding.IssueDraftID != "" ||
		expectedFindingVersion < 1 || finding.Version != expectedFindingVersion {
		return RepositoryState{}, Finding{}, ErrConflict
	}
	now := s.clock()
	finding.Status = status
	finding.Version++
	finding.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, Finding{}, err
	}
	return state, *finding, nil
}

// ReserveIssueGeneration associates one open finding before the provider call.
// Repeating the same generation ID is idempotent and never creates a second
// preview.
func (s Store) ReserveIssueGeneration(
	request IssueGenerationRequest,
) (RepositoryState, IssueDraft, bool, error) {
	request = normalizeIssueGenerationRequest(request)
	if err := validateIssueGenerationRequest(request); err != nil {
		return RepositoryState{}, IssueDraft{}, false, err
	}
	unlock, err := s.lock(request.Repository)
	if err != nil {
		return RepositoryState{}, IssueDraft{}, false, err
	}
	defer unlock()
	state, err := s.load(request.Repository)
	if err != nil {
		return RepositoryState{}, IssueDraft{}, false, err
	}
	findingIndex := findingIndexByID(state.Findings, request.FindingID)
	if findingIndex < 0 {
		return RepositoryState{}, IssueDraft{}, false, os.ErrNotExist
	}
	finding := &state.Findings[findingIndex]
	if finding.IssueDraftID != "" {
		draftIndex := issueDraftIndexByID(state.IssueDrafts, finding.IssueDraftID)
		if draftIndex >= 0 {
			draft := state.IssueDrafts[draftIndex]
			if draft.Origin == IssueDraftOriginAIGenerated &&
				issueDraftAttemptGenerationID(draft) == request.GenerationID {
				return state, draft, false, nil
			}
		}
		return RepositoryState{}, IssueDraft{}, false, ErrConflict
	}
	if finding.Status != FindingOpen {
		return RepositoryState{}, IssueDraft{}, false, ErrConflict
	}
	now := s.clock()
	draft := IssueDraft{
		ID: stableID(
			"rid_", state.Repository, finding.ID, request.GenerationID,
		),
		Repository:                  state.Repository,
		FindingIDs:                  []string{finding.ID},
		Origin:                      IssueDraftOriginAIGenerated,
		GenerationID:                request.GenerationID,
		ResolvedInstructions:        request.ResolvedInstructions,
		InstructionsMode:            request.InstructionsMode,
		GeneratorModel:              request.GeneratorModel,
		GeneratorAccount:            request.GeneratorAccount,
		AttemptGenerationID:         request.GenerationID,
		AttemptResolvedInstructions: request.ResolvedInstructions,
		AttemptInstructionsMode:     request.InstructionsMode,
		AttemptGeneratorModel:       request.GeneratorModel,
		AttemptGeneratorAccount:     request.GeneratorAccount,
		Canonical:                   true,
		State:                       IssueDraftGenerating,
		Version:                     1,
		CreatedAt:                   now,
		UpdatedAt:                   now,
	}
	state.IssueDrafts = append(state.IssueDrafts, draft)
	finding.IssueDraftID = draft.ID
	finding.Version++
	finding.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, IssueDraft{}, false, err
	}
	return state, draft, true, nil
}

// BeginIssueRegeneration reserves a new attempt while retaining the last good
// title/body. A repeated generation ID is idempotent.
func (s Store) BeginIssueRegeneration(
	repository, draftID string,
	request IssueGenerationRequest,
) (RepositoryState, IssueDraft, bool, error) {
	request.Repository = strings.TrimSpace(repository)
	request = normalizeIssueGenerationRequest(request)
	if err := validateIssueGenerationRequest(request); err != nil {
		return RepositoryState{}, IssueDraft{}, false, err
	}
	unlock, err := s.lock(request.Repository)
	if err != nil {
		return RepositoryState{}, IssueDraft{}, false, err
	}
	defer unlock()
	state, err := s.load(request.Repository)
	if err != nil {
		return RepositoryState{}, IssueDraft{}, false, err
	}
	draftIndex := issueDraftIndexByID(state.IssueDrafts, draftID)
	if draftIndex < 0 {
		return RepositoryState{}, IssueDraft{}, false, os.ErrNotExist
	}
	draft := &state.IssueDrafts[draftIndex]
	if draft.Origin != IssueDraftOriginAIGenerated || !draft.Canonical ||
		len(draft.FindingIDs) != 1 || draft.FindingIDs[0] != request.FindingID {
		return RepositoryState{}, IssueDraft{}, false, ErrConflict
	}
	if issueDraftAttemptGenerationID(*draft) == request.GenerationID &&
		draft.State == IssueDraftGenerating {
		return state, *draft, false, nil
	}
	if request.ExpectedDraftVersion < 1 || draft.Version != request.ExpectedDraftVersion {
		return RepositoryState{}, IssueDraft{}, false, ErrConflict
	}
	if draft.State != IssueDraftEditing && draft.State != IssueDraftFailed {
		return RepositoryState{}, IssueDraft{}, false, ErrConflict
	}
	now := s.clock()
	draft.AttemptGenerationID = request.GenerationID
	draft.AttemptResolvedInstructions = request.ResolvedInstructions
	draft.AttemptInstructionsMode = request.InstructionsMode
	draft.AttemptGeneratorModel = request.GeneratorModel
	draft.AttemptGeneratorAccount = request.GeneratorAccount
	draft.GenerationError = ""
	draft.State = IssueDraftGenerating
	draft.Version++
	draft.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, IssueDraft{}, false, err
	}
	return state, *draft, true, nil
}

// CompleteIssueGeneration records only the validated structured projection.
// Provider payloads are intentionally absent from this boundary.
func (s Store) CompleteIssueGeneration(
	repository, draftID, generationID, title, body string,
	labels []string,
	generationErr string,
) (RepositoryState, IssueDraft, error) {
	repository = strings.TrimSpace(repository)
	draftID = strings.TrimSpace(draftID)
	generationID = strings.TrimSpace(generationID)
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, IssueDraft{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, IssueDraft{}, err
	}
	draftIndex := issueDraftIndexByID(state.IssueDrafts, draftID)
	if draftIndex < 0 {
		return RepositoryState{}, IssueDraft{}, os.ErrNotExist
	}
	draft := &state.IssueDrafts[draftIndex]
	if draft.Origin != IssueDraftOriginAIGenerated || !draft.Canonical ||
		issueDraftAttemptGenerationID(*draft) != generationID {
		return RepositoryState{}, IssueDraft{}, ErrConflict
	}
	if draft.State != IssueDraftGenerating {
		// A completed replay is idempotent once this generation has settled.
		if draft.State == IssueDraftEditing || draft.State == IssueDraftFailed {
			return state, *draft, nil
		}
		return RepositoryState{}, IssueDraft{}, ErrConflict
	}
	now := s.clock()
	if safeErr := safeIssueGenerationError(generationErr); safeErr != "" {
		draft.GenerationError = safeErr
		if strings.TrimSpace(draft.Title) != "" && strings.TrimSpace(draft.Body) != "" {
			draft.State = IssueDraftEditing
		} else {
			promoteIssueDraftAttempt(draft)
			draft.State = IssueDraftFailed
		}
	} else {
		title = strings.TrimSpace(title)
		body = strings.TrimSpace(body)
		labels = normalizeLabels(labels)
		if len(labels) == 0 {
			labels = []string{"bug"}
		}
		if !validBoundedText(title, 256) || !validBoundedText(body, maxIssueDraftBodyBytes) {
			return RepositoryState{}, IssueDraft{}, errors.New("invalid repository review issue preview")
		}
		draft.Title = title
		draft.Body = body
		draft.Labels = labels
		promoteIssueDraftAttempt(draft)
		draft.GenerationError = ""
		draft.State = IssueDraftEditing
	}
	draft.Version++
	draft.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, IssueDraft{}, err
	}
	return state, *draft, nil
}

// DeleteIssueDraft removes only an unpublished canonical preview and clears
// its finding reservation.
func (s Store) DeleteIssueDraft(
	repository, draftID string,
	expectedVersion int64,
) (RepositoryState, error) {
	repository = strings.TrimSpace(repository)
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, err
	}
	draftIndex := issueDraftIndexByID(state.IssueDrafts, draftID)
	if draftIndex < 0 {
		return RepositoryState{}, os.ErrNotExist
	}
	draft := state.IssueDrafts[draftIndex]
	if !draft.Canonical || (draft.State != IssueDraftEditing && draft.State != IssueDraftFailed) ||
		expectedVersion < 1 || draft.Version != expectedVersion {
		return RepositoryState{}, ErrConflict
	}
	now := s.clock()
	for findingIndex := range state.Findings {
		if state.Findings[findingIndex].IssueDraftID != draft.ID {
			continue
		}
		state.Findings[findingIndex].IssueDraftID = ""
		state.Findings[findingIndex].Version++
		state.Findings[findingIndex].UpdatedAt = now
	}
	state.IssueDrafts = append(state.IssueDrafts[:draftIndex], state.IssueDrafts[draftIndex+1:]...)
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, err
	}
	return state, nil
}

// LinkExistingIssue persists a provider-validated manual association. The same
// external issue may intentionally be linked to more than one finding.
func (s Store) LinkExistingIssue(
	request ExistingIssueLink,
) (RepositoryState, IssueDraft, error) {
	request.Repository = strings.TrimSpace(request.Repository)
	request.FindingID = strings.TrimSpace(request.FindingID)
	request.ExternalID = strings.TrimSpace(request.ExternalID)
	request.ExternalURL = strings.TrimSpace(request.ExternalURL)
	request.Title = strings.TrimSpace(request.Title)
	request.Body = strings.TrimSpace(request.Body)
	request.Labels = normalizeLabels(request.Labels)
	if !request.Confirmed || !validBoundedText(request.ExternalID, 1024) ||
		!validHTTPSURL(request.ExternalURL) || !validBoundedText(request.Title, 256) ||
		!validOptionalIssueBody(request.Body) {
		return RepositoryState{}, IssueDraft{}, errors.New("invalid existing issue link")
	}
	unlock, err := s.lock(request.Repository)
	if err != nil {
		return RepositoryState{}, IssueDraft{}, err
	}
	defer unlock()
	state, err := s.load(request.Repository)
	if err != nil {
		return RepositoryState{}, IssueDraft{}, err
	}
	findingIndex := findingIndexByID(state.Findings, request.FindingID)
	if findingIndex < 0 {
		return RepositoryState{}, IssueDraft{}, os.ErrNotExist
	}
	finding := &state.Findings[findingIndex]
	if request.ExpectedFindingVersion < 1 || finding.Version != request.ExpectedFindingVersion {
		return RepositoryState{}, IssueDraft{}, ErrConflict
	}
	if finding.IssueDraftID != "" {
		existingIndex := issueDraftIndexByID(state.IssueDrafts, finding.IssueDraftID)
		if !request.Replace || existingIndex < 0 ||
			state.IssueDrafts[existingIndex].Origin != IssueDraftOriginLinked ||
			!state.IssueDrafts[existingIndex].Canonical ||
			state.IssueDrafts[existingIndex].State != IssueDraftPosted {
			return RepositoryState{}, IssueDraft{}, ErrConflict
		}
		existing := state.IssueDrafts[existingIndex]
		if existing.ExternalID == request.ExternalID && existing.ExternalURL == request.ExternalURL {
			return state, existing, nil
		}
		state.IssueDrafts = append(
			state.IssueDrafts[:existingIndex], state.IssueDrafts[existingIndex+1:]...,
		)
	} else if finding.Status != FindingOpen {
		return RepositoryState{}, IssueDraft{}, ErrConflict
	}
	now := s.clock()
	draft := IssueDraft{
		ID:         stableID("rid_", state.Repository, finding.ID, "linked", request.ExternalURL),
		Repository: state.Repository, FindingIDs: []string{finding.ID},
		Origin: IssueDraftOriginLinked, Canonical: true,
		Title: request.Title, Body: request.Body, Labels: request.Labels,
		State: IssueDraftPosted, ExternalID: request.ExternalID, ExternalURL: request.ExternalURL,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	state.IssueDrafts = append(state.IssueDrafts, draft)
	finding.IssueDraftID = draft.ID
	finding.Status = FindingPosted
	finding.Version++
	finding.UpdatedAt = now
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, IssueDraft{}, err
	}
	return state, draft, nil
}

// UnlinkExistingIssue is intentionally limited to manually linked issues;
// issues created from previews remain permanently associated.
func (s Store) UnlinkExistingIssue(
	repository, findingID string,
	expectedFindingVersion int64,
	confirmed bool,
) (RepositoryState, error) {
	if !confirmed {
		return RepositoryState{}, errors.New("existing issue unlink requires confirmation")
	}
	repository = strings.TrimSpace(repository)
	findingID = strings.TrimSpace(findingID)
	unlock, err := s.lock(repository)
	if err != nil {
		return RepositoryState{}, err
	}
	defer unlock()
	state, err := s.load(repository)
	if err != nil {
		return RepositoryState{}, err
	}
	findingIndex := findingIndexByID(state.Findings, findingID)
	if findingIndex < 0 {
		return RepositoryState{}, os.ErrNotExist
	}
	finding := &state.Findings[findingIndex]
	if expectedFindingVersion < 1 || finding.Version != expectedFindingVersion {
		return RepositoryState{}, ErrConflict
	}
	draftIndex := issueDraftIndexByID(state.IssueDrafts, finding.IssueDraftID)
	if draftIndex < 0 || state.IssueDrafts[draftIndex].Origin != IssueDraftOriginLinked ||
		!state.IssueDrafts[draftIndex].Canonical || state.IssueDrafts[draftIndex].State != IssueDraftPosted {
		return RepositoryState{}, ErrConflict
	}
	now := s.clock()
	finding.IssueDraftID = ""
	finding.Status = FindingOpen
	finding.Version++
	finding.UpdatedAt = now
	state.IssueDrafts = append(state.IssueDrafts[:draftIndex], state.IssueDrafts[draftIndex+1:]...)
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, err
	}
	return state, nil
}

func normalizeIssueGenerationRequest(request IssueGenerationRequest) IssueGenerationRequest {
	request.Repository = strings.TrimSpace(request.Repository)
	request.FindingID = strings.TrimSpace(request.FindingID)
	request.GenerationID = strings.TrimSpace(request.GenerationID)
	request.ResolvedInstructions = strings.TrimSpace(request.ResolvedInstructions)
	request.GeneratorModel = strings.TrimSpace(request.GeneratorModel)
	request.GeneratorAccount = strings.TrimSpace(request.GeneratorAccount)
	if request.InstructionsMode == "" {
		request.InstructionsMode = IssueDraftInstructionsDefault
	}
	return request
}

func validateIssueGenerationRequest(request IssueGenerationRequest) error {
	if !validBoundedText(request.Repository, maxRepositoryIdentityBytes) ||
		!validBoundedText(request.FindingID, 256) ||
		!validBoundedText(request.GenerationID, maxIssueGenerationIDBytes) ||
		!validBoundedText(request.ResolvedInstructions, maxIssueInstructionsBytes) ||
		!validBoundedText(request.GeneratorModel, 256) ||
		!validBoundedText(request.GeneratorAccount, 256) ||
		(request.InstructionsMode != IssueDraftInstructionsDefault &&
			request.InstructionsMode != IssueDraftInstructionsCustom) {
		return errors.New("invalid repository review issue generation request")
	}
	return nil
}

func safeIssueGenerationError(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	// Provider errors can contain request metadata or credential-adjacent
	// diagnostics. Persist only this stable user-safe projection.
	return "Issue preview generation failed."
}

func issueDraftAttemptGenerationID(draft IssueDraft) string {
	if value := strings.TrimSpace(draft.AttemptGenerationID); value != "" {
		return value
	}
	return strings.TrimSpace(draft.GenerationID)
}

func promoteIssueDraftAttempt(draft *IssueDraft) {
	if draft == nil || strings.TrimSpace(draft.AttemptGenerationID) == "" {
		return
	}
	draft.GenerationID = draft.AttemptGenerationID
	draft.ResolvedInstructions = draft.AttemptResolvedInstructions
	draft.InstructionsMode = draft.AttemptInstructionsMode
	draft.GeneratorModel = draft.AttemptGeneratorModel
	draft.GeneratorAccount = draft.AttemptGeneratorAccount
	clearIssueDraftAttempt(draft)
}

func clearIssueDraftAttempt(draft *IssueDraft) {
	if draft == nil {
		return
	}
	draft.AttemptGenerationID = ""
	draft.AttemptResolvedInstructions = ""
	draft.AttemptInstructionsMode = ""
	draft.AttemptGeneratorModel = ""
	draft.AttemptGeneratorAccount = ""
}

func validHTTPSURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil &&
		parsed.RawQuery == "" && parsed.Fragment == ""
}

func validOptionalIssueBody(value string) bool {
	return value == "" || validBoundedText(value, maxIssueDraftBodyBytes)
}

func findingIndexByID(findings []Finding, id string) int {
	id = strings.TrimSpace(id)
	for index := range findings {
		if findings[index].ID == id {
			return index
		}
	}
	return -1
}

func issueDraftIndexByID(drafts []IssueDraft, id string) int {
	id = strings.TrimSpace(id)
	for index := range drafts {
		if drafts[index].ID == id {
			return index
		}
	}
	return -1
}

// backfillCanonicalIssueAssociations upgrades legacy grouped drafts in place.
// It never discards conflicts: the selected draft becomes canonical and every
// other matching record remains visible and read-only.
func backfillCanonicalIssueAssociations(state *RepositoryState) bool {
	if state == nil {
		return false
	}
	changed := false
	for index := range state.IssueDrafts {
		draft := &state.IssueDrafts[index]
		if draft.Origin == "" {
			draft.Origin = IssueDraftOriginLegacy
			changed = true
		}
	}
	byID := make(map[string]int, len(state.IssueDrafts))
	for index := range state.IssueDrafts {
		byID[state.IssueDrafts[index].ID] = index
	}
	assigned := make(map[string]int, len(state.Findings))
	canonical := make(map[int]struct{})
	// New one-finding associations are authoritative. Legacy associations are
	// recomputed below so a grouped record can never overlap another canonical
	// record for only part of its membership.
	for _, finding := range state.Findings {
		index, ok := byID[finding.IssueDraftID]
		if !ok || state.IssueDrafts[index].Origin == IssueDraftOriginLegacy ||
			!issueDraftContainsFinding(state.IssueDrafts[index], finding.ID) {
			continue
		}
		if issueDraftCanClaimFindings(state.IssueDrafts[index], assigned) {
			assignIssueDraft(state.IssueDrafts[index], index, assigned)
			canonical[index] = struct{}{}
		}
	}
	legacy := make([]int, 0, len(state.IssueDrafts))
	for index, draft := range state.IssueDrafts {
		if draft.Origin == IssueDraftOriginLegacy {
			legacy = append(legacy, index)
		}
	}
	sort.SliceStable(legacy, func(left, right int) bool {
		leftDraft := state.IssueDrafts[legacy[left]]
		rightDraft := state.IssueDrafts[legacy[right]]
		leftPriority := legacyIssueDraftPriority(leftDraft.State)
		rightPriority := legacyIssueDraftPriority(rightDraft.State)
		if leftPriority != rightPriority {
			return leftPriority > rightPriority
		}
		// Provider-side legacy states are indivisible. Prefer the grouped record
		// so every finding it already affected retains one consistent canonical
		// issue. Editable drafts still follow the required newest-first rule.
		if leftPriority >= 2 && len(leftDraft.FindingIDs) != len(rightDraft.FindingIDs) {
			return len(leftDraft.FindingIDs) > len(rightDraft.FindingIDs)
		}
		if !leftDraft.UpdatedAt.Equal(rightDraft.UpdatedAt) {
			return leftDraft.UpdatedAt.After(rightDraft.UpdatedAt)
		}
		if !leftDraft.CreatedAt.Equal(rightDraft.CreatedAt) {
			return leftDraft.CreatedAt.After(rightDraft.CreatedAt)
		}
		return leftDraft.ID < rightDraft.ID
	})
	for _, index := range legacy {
		draft := state.IssueDrafts[index]
		if !issueDraftCanClaimFindings(draft, assigned) {
			continue
		}
		assignIssueDraft(draft, index, assigned)
		canonical[index] = struct{}{}
	}
	for findingIndex := range state.Findings {
		finding := &state.Findings[findingIndex]
		selected, ok := assigned[finding.ID]
		expectedDraftID := ""
		if ok {
			expectedDraftID = state.IssueDrafts[selected].ID
		}
		if finding.IssueDraftID != expectedDraftID {
			finding.IssueDraftID = expectedDraftID
			changed = true
		}
		if ok && state.IssueDrafts[selected].State == IssueDraftPosted {
			if finding.Status != FindingPosted {
				finding.Status = FindingPosted
				changed = true
			}
		} else if finding.Status == FindingPosted {
			// Historical UI versions allowed an untracked manual "posted"
			// transition. Posted now requires a durable provider-validated issue.
			finding.Status = FindingOpen
			changed = true
		}
	}
	for index := range state.IssueDrafts {
		_, shouldBeCanonical := canonical[index]
		if state.IssueDrafts[index].Canonical != shouldBeCanonical {
			state.IssueDrafts[index].Canonical = shouldBeCanonical
			changed = true
		}
	}
	return changed
}

func issueDraftCanClaimFindings(draft IssueDraft, assigned map[string]int) bool {
	if len(draft.FindingIDs) == 0 {
		return false
	}
	for _, findingID := range draft.FindingIDs {
		if _, exists := assigned[findingID]; exists {
			return false
		}
	}
	return true
}

func assignIssueDraft(draft IssueDraft, index int, assigned map[string]int) {
	for _, findingID := range draft.FindingIDs {
		assigned[findingID] = index
	}
}

func issueDraftContainsFinding(draft IssueDraft, findingID string) bool {
	for _, candidate := range draft.FindingIDs {
		if candidate == findingID {
			return true
		}
	}
	return false
}

func legacyIssueDraftPriority(state IssueDraftState) int {
	switch state {
	case IssueDraftPosted:
		return 3
	case IssueDraftPublishing, IssueDraftUnknown:
		return 2
	case IssueDraftEditing:
		return 1
	default:
		return 0
	}
}

func validateIssueAssociations(state RepositoryState) error {
	findingIDs := make(map[string]struct{}, len(state.Findings))
	for _, finding := range state.Findings {
		if !validBoundedText(finding.ID, 256) {
			return errors.New("invalid repository review finding identity")
		}
		if _, duplicate := findingIDs[finding.ID]; duplicate {
			return errors.New("duplicate repository review finding identity")
		}
		findingIDs[finding.ID] = struct{}{}
		if finding.IssueDraftID != "" && !validBoundedText(finding.IssueDraftID, 256) {
			return errors.New("invalid repository review finding issue association")
		}
	}
	drafts := make(map[string]IssueDraft, len(state.IssueDrafts))
	canonicalReferences := make(map[string]int)
	for _, draft := range state.IssueDrafts {
		if !validBoundedText(draft.ID, 256) || draft.Repository != state.Repository ||
			draft.Version < 1 || draft.CreatedAt.IsZero() || draft.UpdatedAt.IsZero() ||
			draft.UpdatedAt.Before(draft.CreatedAt) || len(draft.FindingIDs) == 0 ||
			len(draft.FindingIDs) > 200 || len(draft.Labels) > 20 {
			return errors.New("invalid repository review issue preview")
		}
		if _, duplicate := drafts[draft.ID]; duplicate {
			return errors.New("duplicate repository review issue preview")
		}
		drafts[draft.ID] = draft
		if draft.Origin != IssueDraftOriginAIGenerated && draft.Origin != IssueDraftOriginLinked &&
			draft.Origin != IssueDraftOriginLegacy {
			return errors.New("invalid repository review issue preview origin")
		}
		if draft.State != IssueDraftGenerating && draft.State != IssueDraftFailed &&
			draft.State != IssueDraftEditing && draft.State != IssueDraftPublishing &&
			draft.State != IssueDraftUnknown && draft.State != IssueDraftPosted {
			return errors.New("invalid repository review issue preview state")
		}
		if draft.State != IssueDraftGenerating && draft.State != IssueDraftFailed &&
			(!validBoundedText(draft.Title, 256) ||
				draft.Origin != IssueDraftOriginLinked &&
					!validBoundedText(draft.Body, maxIssueDraftBodyBytes)) {
			return errors.New("invalid repository review issue preview content")
		}
		if (draft.Title != "" && !validBoundedText(draft.Title, 256)) ||
			(draft.Body != "" && !validBoundedText(draft.Body, maxIssueDraftBodyBytes)) ||
			!validOptionalAutomationText(draft.GenerationID, maxIssueGenerationIDBytes) ||
			!validOptionalAutomationText(draft.ResolvedInstructions, maxIssueInstructionsBytes) ||
			!validOptionalAutomationText(draft.GeneratorModel, 256) ||
			!validOptionalAutomationText(draft.GeneratorAccount, 256) ||
			!validOptionalAutomationText(draft.AttemptGenerationID, maxIssueGenerationIDBytes) ||
			!validOptionalAutomationText(draft.AttemptResolvedInstructions, maxIssueInstructionsBytes) ||
			!validOptionalAutomationText(draft.AttemptGeneratorModel, 256) ||
			!validOptionalAutomationText(draft.AttemptGeneratorAccount, 256) ||
			!validOptionalAutomationText(draft.GenerationError, maxIssueGenerationErrorBytes) ||
			!validOptionalAutomationText(draft.ExternalID, 1024) ||
			!validOptionalAutomationText(draft.ExternalURL, 4096) {
			return errors.New("invalid repository review issue preview metadata")
		}
		if draft.InstructionsMode != "" && draft.InstructionsMode != IssueDraftInstructionsDefault &&
			draft.InstructionsMode != IssueDraftInstructionsCustom {
			return errors.New("invalid repository review issue preview instructions mode")
		}
		if draft.AttemptInstructionsMode != "" &&
			draft.AttemptInstructionsMode != IssueDraftInstructionsDefault &&
			draft.AttemptInstructionsMode != IssueDraftInstructionsCustom {
			return errors.New("invalid repository review issue preview attempt instructions mode")
		}
		attemptFields := 0
		for _, present := range []bool{
			draft.AttemptGenerationID != "", draft.AttemptResolvedInstructions != "",
			draft.AttemptInstructionsMode != "", draft.AttemptGeneratorModel != "",
			draft.AttemptGeneratorAccount != "",
		} {
			if present {
				attemptFields++
			}
		}
		if attemptFields != 0 && attemptFields != 5 {
			return errors.New("incomplete repository review issue generation attempt provenance")
		}
		if attemptFields == 5 && draft.Origin != IssueDraftOriginAIGenerated {
			return errors.New("invalid repository review issue generation attempt provenance")
		}
		if attemptFields == 5 && draft.State != IssueDraftGenerating &&
			draft.GenerationError == "" {
			return errors.New("settled repository review issue attempt lacks an error")
		}
		if draft.Origin == IssueDraftOriginAIGenerated &&
			(len(draft.FindingIDs) != 1 || draft.GenerationID == "" ||
				draft.ResolvedInstructions == "" || draft.InstructionsMode == "" ||
				draft.GeneratorModel == "" || draft.GeneratorAccount == "") {
			return errors.New("invalid generated repository review issue preview")
		}
		if draft.Origin == IssueDraftOriginLinked &&
			(len(draft.FindingIDs) != 1 || draft.State != IssueDraftPosted ||
				draft.ExternalID == "" || !validHTTPSURL(draft.ExternalURL)) {
			return errors.New("invalid linked repository review issue")
		}
		seenFindings := make(map[string]struct{}, len(draft.FindingIDs))
		for _, findingID := range draft.FindingIDs {
			if _, exists := findingIDs[findingID]; !exists {
				return errors.New("repository review issue preview references a missing finding")
			}
			if _, duplicate := seenFindings[findingID]; duplicate {
				return errors.New("repository review issue preview repeats a finding")
			}
			seenFindings[findingID] = struct{}{}
		}
		for _, label := range draft.Labels {
			if !validBoundedText(label, 50) {
				return errors.New("invalid repository review issue preview label")
			}
		}
	}
	for _, finding := range state.Findings {
		if finding.IssueDraftID == "" {
			continue
		}
		draft, exists := drafts[finding.IssueDraftID]
		if !exists || !draft.Canonical || !issueDraftContainsFinding(draft, finding.ID) {
			return errors.New("invalid repository review canonical issue association")
		}
		canonicalReferences[draft.ID]++
	}
	for _, draft := range state.IssueDrafts {
		if draft.Canonical && canonicalReferences[draft.ID] != len(draft.FindingIDs) ||
			!draft.Canonical && canonicalReferences[draft.ID] != 0 {
			return errors.New("invalid repository review canonical issue preview")
		}
	}
	return nil
}
