package repoaudit

import (
	"errors"
	"net/url"
	"os"
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
	Repository              string
	FindingID               string
	GenerationID            string
	ResolvedInstructions    string
	InstructionsMode        IssueDraftInstructionsMode
	GeneratorModel          string
	GeneratorAccount        string
	GeneratorProfileID      string
	GeneratorProfileVersion int64
	ExpectedDraftVersion    int64
}

// ExistingIssueLink is an issue identity that has already been re-fetched and
// validated by the protected GitHub boundary.
type ExistingIssueLink struct {
	Repository             string
	FindingID              string
	ExpectedFindingVersion int64
	ExternalID             string
	ExternalURL            string
	State                  string
	Title                  string
	Body                   string
	Labels                 []string
	Origin                 IssueDraftOrigin
	Confirmed              bool
	Replace                bool
}

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
	if !repositoryFindingAllowsIssueActions(state, *finding) {
		return RepositoryState{}, IssueDraft{}, false, ErrConflict
	}
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
		Repository:                     state.Repository,
		FindingIDs:                     []string{finding.ID},
		Origin:                         IssueDraftOriginAIGenerated,
		GenerationID:                   request.GenerationID,
		ResolvedInstructions:           request.ResolvedInstructions,
		InstructionsMode:               request.InstructionsMode,
		GeneratorModel:                 request.GeneratorModel,
		GeneratorAccount:               request.GeneratorAccount,
		GeneratorProfileID:             request.GeneratorProfileID,
		GeneratorProfileVersion:        request.GeneratorProfileVersion,
		AttemptGenerationID:            request.GenerationID,
		AttemptResolvedInstructions:    request.ResolvedInstructions,
		AttemptInstructionsMode:        request.InstructionsMode,
		AttemptGeneratorModel:          request.GeneratorModel,
		AttemptGeneratorAccount:        request.GeneratorAccount,
		AttemptGeneratorProfileID:      request.GeneratorProfileID,
		AttemptGeneratorProfileVersion: request.GeneratorProfileVersion,
		State:                          IssueDraftGenerating,
		Version:                        1,
		CreatedAt:                      now,
		UpdatedAt:                      now,
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
	if draft.Origin != IssueDraftOriginAIGenerated ||
		len(draft.FindingIDs) != 1 || draft.FindingIDs[0] != request.FindingID {
		return RepositoryState{}, IssueDraft{}, false, ErrConflict
	}
	findingIndex := findingIndexByID(state.Findings, request.FindingID)
	if findingIndex < 0 || !repositoryFindingAllowsIssueActions(state, state.Findings[findingIndex]) {
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
	draft.AttemptGeneratorProfileID = request.GeneratorProfileID
	draft.AttemptGeneratorProfileVersion = request.GeneratorProfileVersion
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
	if draft.Origin != IssueDraftOriginAIGenerated ||
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
	if (draft.State != IssueDraftEditing && draft.State != IssueDraftFailed) ||
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

// LinkExistingIssue persists a provider-validated manual or discovered
// association. The same external issue may intentionally be linked to more
// than one finding.
func (s Store) LinkExistingIssue(
	request ExistingIssueLink,
) (RepositoryState, IssueDraft, error) {
	request.Repository = strings.TrimSpace(request.Repository)
	request.FindingID = strings.TrimSpace(request.FindingID)
	request.ExternalID = strings.TrimSpace(request.ExternalID)
	request.ExternalURL = strings.TrimSpace(request.ExternalURL)
	request.Title = strings.TrimSpace(request.Title)
	request.State = strings.ToLower(strings.TrimSpace(request.State))
	request.Body = strings.TrimSpace(request.Body)
	request.Labels = normalizeLabels(request.Labels)
	if request.Origin == "" {
		request.Origin = IssueDraftOriginLinked
	}
	if !request.Confirmed || !validBoundedText(request.ExternalID, 1024) ||
		!validHTTPSURL(request.ExternalURL) || !validBoundedText(request.Title, 256) ||
		!validOptionalIssueBody(request.Body) || !reversibleIssueDraftOrigin(request.Origin) ||
		(request.State != "" && request.State != "open" && request.State != "closed") {
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
	if !repositoryFindingAllowsIssueActions(state, *finding) {
		return RepositoryState{}, IssueDraft{}, ErrConflict
	}
	if request.ExpectedFindingVersion < 1 || finding.Version != request.ExpectedFindingVersion {
		return RepositoryState{}, IssueDraft{}, ErrConflict
	}
	if finding.IssueDraftID != "" {
		existingIndex := issueDraftIndexByID(state.IssueDrafts, finding.IssueDraftID)
		if !request.Replace || existingIndex < 0 ||
			!reversibleIssueDraftOrigin(state.IssueDrafts[existingIndex].Origin) ||
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
		ID:         stableID("rid_", state.Repository, finding.ID, string(request.Origin), request.ExternalURL),
		Repository: state.Repository, FindingIDs: []string{finding.ID},
		Origin: request.Origin,
		Title:  request.Title, Body: request.Body, Labels: request.Labels,
		State: IssueDraftPosted, ExternalID: request.ExternalID, ExternalURL: request.ExternalURL,
		ExternalState: request.State,
		Version:       1, CreatedAt: now, UpdatedAt: now,
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

// UnlinkExistingIssue is intentionally limited to reversible manual or
// discovered links; issues created from previews remain permanently associated.
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
	if draftIndex < 0 || !reversibleIssueDraftOrigin(state.IssueDrafts[draftIndex].Origin) ||
		state.IssueDrafts[draftIndex].State != IssueDraftPosted {
		return RepositoryState{}, ErrConflict
	}
	now := s.clock()
	unlinkedURL := state.IssueDrafts[draftIndex].ExternalURL
	finding.IssueDraftID = ""
	finding.Status = FindingOpen
	finding.Version++
	finding.UpdatedAt = now
	state.IssueDrafts = append(state.IssueDrafts[:draftIndex], state.IssueDrafts[draftIndex+1:]...)
	clearRepositoryFindingIssueAssociation(
		&state, finding.RepositoryFindingID, unlinkedURL, now,
	)
	state.Version++
	state.UpdatedAt = now
	if err := s.save(&state); err != nil {
		return RepositoryState{}, err
	}
	return state, nil
}

func reversibleIssueDraftOrigin(origin IssueDraftOrigin) bool {
	return origin == IssueDraftOriginLinked || origin == IssueDraftOriginDiscovered
}

func normalizeIssueGenerationRequest(request IssueGenerationRequest) IssueGenerationRequest {
	request.Repository = strings.TrimSpace(request.Repository)
	request.FindingID = strings.TrimSpace(request.FindingID)
	request.GenerationID = strings.TrimSpace(request.GenerationID)
	request.ResolvedInstructions = strings.TrimSpace(request.ResolvedInstructions)
	request.GeneratorModel = strings.TrimSpace(request.GeneratorModel)
	request.GeneratorAccount = strings.TrimSpace(request.GeneratorAccount)
	request.GeneratorProfileID = strings.TrimSpace(request.GeneratorProfileID)
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
		(request.GeneratorProfileID == "") != (request.GeneratorProfileVersion == 0) ||
		(request.GeneratorProfileID != "" &&
			(!validProfileID(request.GeneratorProfileID) || request.GeneratorProfileVersion < 1)) ||
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
	draft.GeneratorProfileID = draft.AttemptGeneratorProfileID
	draft.GeneratorProfileVersion = draft.AttemptGeneratorProfileVersion
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
	draft.AttemptGeneratorProfileID = ""
	draft.AttemptGeneratorProfileVersion = 0
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

func issueDraftContainsFinding(draft IssueDraft, findingID string) bool {
	for _, candidate := range draft.FindingIDs {
		if candidate == findingID {
			return true
		}
	}
	return false
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
			draft.UpdatedAt.Before(draft.CreatedAt) || len(draft.FindingIDs) != 1 ||
			len(draft.Labels) > 20 {
			return errors.New("invalid repository review issue preview")
		}
		if _, duplicate := drafts[draft.ID]; duplicate {
			return errors.New("duplicate repository review issue preview")
		}
		drafts[draft.ID] = draft
		if draft.Origin != IssueDraftOriginAIGenerated && !reversibleIssueDraftOrigin(draft.Origin) {
			return errors.New("invalid repository review issue preview origin")
		}
		if draft.State != IssueDraftGenerating && draft.State != IssueDraftFailed &&
			draft.State != IssueDraftEditing && draft.State != IssueDraftPublishing &&
			draft.State != IssueDraftUnknown && draft.State != IssueDraftPosted {
			return errors.New("invalid repository review issue preview state")
		}
		if draft.State != IssueDraftGenerating && draft.State != IssueDraftFailed &&
			(!validBoundedText(draft.Title, 256) ||
				!reversibleIssueDraftOrigin(draft.Origin) &&
					!validBoundedText(draft.Body, maxIssueDraftBodyBytes)) {
			return errors.New("invalid repository review issue preview content")
		}
		if (draft.Title != "" && !validBoundedText(draft.Title, 256)) ||
			(draft.Body != "" && !validBoundedText(draft.Body, maxIssueDraftBodyBytes)) ||
			!validOptionalAutomationText(draft.GenerationID, maxIssueGenerationIDBytes) ||
			!validOptionalAutomationText(draft.ResolvedInstructions, maxIssueInstructionsBytes) ||
			!validOptionalAutomationText(draft.GeneratorModel, 256) ||
			!validOptionalAutomationText(draft.GeneratorAccount, 256) ||
			!validOptionalAutomationText(draft.GeneratorProfileID, 128) ||
			draft.GeneratorProfileVersion < 0 ||
			!validOptionalAutomationText(draft.AttemptGenerationID, maxIssueGenerationIDBytes) ||
			!validOptionalAutomationText(draft.AttemptResolvedInstructions, maxIssueInstructionsBytes) ||
			!validOptionalAutomationText(draft.AttemptGeneratorModel, 256) ||
			!validOptionalAutomationText(draft.AttemptGeneratorAccount, 256) ||
			!validOptionalAutomationText(draft.AttemptGeneratorProfileID, 128) ||
			draft.AttemptGeneratorProfileVersion < 0 ||
			!validOptionalAutomationText(draft.GenerationError, maxIssueGenerationErrorBytes) ||
			!validOptionalAutomationText(draft.ExternalID, 1024) ||
			!validOptionalAutomationText(draft.ExternalURL, 4096) ||
			(draft.ExternalState != "" && draft.ExternalState != "open" &&
				draft.ExternalState != "closed") {
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
		if (draft.GeneratorProfileID == "") != (draft.GeneratorProfileVersion == 0) ||
			(draft.GeneratorProfileID != "" && !validProfileID(draft.GeneratorProfileID)) ||
			(draft.AttemptGeneratorProfileID == "") !=
				(draft.AttemptGeneratorProfileVersion == 0) ||
			(draft.AttemptGeneratorProfileID != "" &&
				!validProfileID(draft.AttemptGeneratorProfileID)) {
			return errors.New("invalid repository review issue generation profile provenance")
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
		if draft.AttemptGeneratorProfileID != "" && attemptFields != 5 {
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
		if reversibleIssueDraftOrigin(draft.Origin) &&
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
		if !exists || !issueDraftContainsFinding(draft, finding.ID) {
			return errors.New("invalid repository review canonical issue association")
		}
		canonicalReferences[draft.ID]++
	}
	for _, draft := range state.IssueDrafts {
		if canonicalReferences[draft.ID] != len(draft.FindingIDs) {
			return errors.New("invalid repository review canonical issue preview")
		}
	}
	return nil
}
