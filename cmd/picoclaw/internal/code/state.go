package code

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

const (
	// ResultSchemaVersion is the stable machine-output schema.
	ResultSchemaVersion = 1
	resultVersion       = ResultSchemaVersion
)

var developmentIDPattern = regexp.MustCompile(`^devw_[0-9a-f]{32}$`)

// Result is the only machine-readable output emitted by `picoclaw code`.
type Result struct {
	Version           int          `json:"version"`
	RequestID         string       `json:"request_id"`
	WorkspaceID       string       `json:"workspace_id"`
	Phase             string       `json:"phase"`
	Status            string       `json:"status"`
	CandidateRevision string       `json:"candidate_revision"`
	ValidationStatus  string       `json:"validation_status"`
	PendingGate       *PendingGate `json:"pending_gate"`
	Branch            string       `json:"branch"`
	PullRequestURL    string       `json:"pull_request_url"`
	ErrorCode         string       `json:"error_code"`
}

type PendingGate struct {
	ID              string          `json:"id"`
	DecisionPoint   string          `json:"decision_point"`
	SubjectRevision string          `json:"subject_revision"`
	Prompt          string          `json:"prompt"`
	Fields          []GateField     `json:"fields"`
	Evidence        PendingEvidence `json:"evidence"`
}

type GateField struct {
	ID            string            `json:"id"`
	Type          string            `json:"type"`
	Label         string            `json:"label"`
	Required      bool              `json:"required"`
	MinSelections int               `json:"min_selections"`
	MaxSelections int               `json:"max_selections"`
	Options       []GateFieldOption `json:"options"`
}

type GateFieldOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type PendingEvidence struct {
	CandidateRevision string   `json:"candidate_revision"`
	ChangedFiles      []string `json:"changed_files"`
	ValidationStatus  string   `json:"validation_status"`
	FindingCount      int      `json:"finding_count"`
	PublicationKind   string   `json:"publication_kind"`
	Repository        string   `json:"repository"`
}

type lifecycleAction uint8

const (
	actionPoll lifecycleAction = iota
	actionGate
	actionCharter
	actionReconcile
	actionComplete
	actionFail
)

type lifecycleSnapshot struct {
	result      Result
	action      lifecycleAction
	gate        *prworkspace.GateRun
	charter     *prworkspace.Charter
	publication *prworkspace.Publication
}

func classifyAggregate(requestID string, aggregate prworkspace.Aggregate) (lifecycleSnapshot, error) {
	workspace := aggregate.Workspace
	if !developmentIDPattern.MatchString(workspace.ID) || workspace.Version < 1 ||
		workspace.Intent != prworkspace.IntentImplementFeature ||
		workspace.SourceKind != prworkspace.SourceBrief ||
		!validDevelopmentPhase(workspace.Phase) || !validExecutionState(workspace.ExecutionState) {
		return lifecycleSnapshot{}, fmt.Errorf("development workspace response is malformed")
	}
	snapshot := lifecycleSnapshot{result: Result{
		Version: resultVersion, RequestID: requestID, WorkspaceID: workspace.ID,
		Phase: string(workspace.Phase), Status: string(workspace.ExecutionState),
	}}
	repair, hasRepair := latestSucceededRepair(aggregate.RepairAttempts)
	if hasRepair {
		snapshot.result.CandidateRevision = boundedTerminalText(repair.CandidateSHA, 256)
	}
	validation, hasValidation := validationForRepair(aggregate.ValidationRuns, repair, hasRepair)
	if hasValidation {
		snapshot.result.ValidationStatus = string(validation.State)
	}
	publication, hasPublication := branchPublicationForRepair(aggregate.Publications, repair, hasRepair)
	if hasPublication && validPullRequestURL(publication.ExternalURL, aggregate.ProviderSnapshot) {
		snapshot.result.PullRequestURL = publication.ExternalURL
	}
	if aggregate.ProviderSnapshot.PullRequestID != "" && aggregate.ProviderSnapshot.PullNumber > 0 &&
		validBranchName(aggregate.ProviderSnapshot.HeadRef) {
		snapshot.result.Branch = aggregate.ProviderSnapshot.HeadRef
	}

	// Complete and terminal workspace state is authoritative over historical
	// gates. The one exception is an unknown branch publication: that state has
	// an explicit, human-authorized recovery protocol below.
	if workspace.Phase == prworkspace.PhaseComplete {
		if completeEvidenceValid(aggregate, repair, hasRepair, validation, hasValidation, publication, hasPublication) {
			snapshot.action = actionComplete
			snapshot.result.Status = string(prworkspace.ExecutionSucceeded)
			return snapshot, nil
		}
		snapshot.action = actionFail
		snapshot.result.ErrorCode = "incomplete_publication_evidence"
		return snapshot, nil
	}
	recoverablePublication := hasPublication && publication.State == prworkspace.ExecutionUnknown
	if terminalExecutionState(workspace.ExecutionState) && !recoverablePublication {
		snapshot.action = actionFail
		snapshot.result.ErrorCode = terminalErrorCode(aggregate)
		return snapshot, nil
	}

	if gate, form, ok := activeHumanGate(aggregate.Gates); ok {
		if terminalExecutionState(workspace.ExecutionState) &&
			(!recoverablePublication || gate.DecisionPoint != "pr.publication.reconcile" ||
				gate.TargetID != publication.ID) {
			snapshot.action = actionFail
			snapshot.result.ErrorCode = terminalErrorCode(aggregate)
			return snapshot, nil
		}
		gateCopy := gate
		snapshot.action, snapshot.gate = actionGate, &gateCopy
		snapshot.result.PendingGate = projectPendingGate(gate, form)
		return snapshot, nil
	}
	if charter, required := charterRevisionRequired(aggregate); required {
		charterCopy := charter
		snapshot.action, snapshot.charter = actionFail, &charterCopy
		snapshot.result.ErrorCode = "charter_revision_required"
		return snapshot, nil
	}
	if charter, ok := pendingCharterDecision(aggregate); ok {
		charterCopy := charter
		snapshot.action, snapshot.charter = actionCharter, &charterCopy
		return snapshot, nil
	}
	if hasPublication && publication.State == prworkspace.ExecutionUnknown {
		publicationCopy := publication
		snapshot.action, snapshot.publication = actionReconcile, &publicationCopy
		return snapshot, nil
	}
	if hasPublication {
		switch publication.State {
		case prworkspace.ExecutionFailed,
			prworkspace.ExecutionBlocked,
			prworkspace.ExecutionCanceled,
			prworkspace.ExecutionStale:
			snapshot.action = actionFail
			snapshot.result.ErrorCode = "publication_failed"
			if publication.PublicErrorCode == "unsafe_provider" {
				snapshot.result.ErrorCode = "unsafe_provider"
			}
			return snapshot, nil
		}
	}
	if workspace.Phase == prworkspace.PhaseTriage && hasOpenFinding(aggregate.Findings) {
		snapshot.action = actionFail
		snapshot.result.ErrorCode = "implementation_unavailable"
		return snapshot, nil
	}
	if workspace.Phase == prworkspace.PhaseTriage && len(aggregate.Findings) == 0 &&
		(workspace.ExecutionState == prworkspace.ExecutionBlocked ||
			workspace.ExecutionState == prworkspace.ExecutionWaitingUser) {
		snapshot.action = actionFail
		snapshot.result.ErrorCode = "implementation_unavailable"
		return snapshot, nil
	}

	if terminalExecutionState(workspace.ExecutionState) {
		snapshot.action = actionFail
		snapshot.result.ErrorCode = terminalErrorCode(aggregate)
		return snapshot, nil
	}
	snapshot.action = actionPoll
	return snapshot, nil
}

func terminalExecutionState(value prworkspace.ExecutionState) bool {
	switch value {
	case prworkspace.ExecutionFailed,
		prworkspace.ExecutionBlocked,
		prworkspace.ExecutionCanceled,
		prworkspace.ExecutionStale,
		prworkspace.ExecutionUnknown:
		return true
	default:
		return false
	}
}

func validDevelopmentPhase(value prworkspace.Phase) bool {
	switch value {
	case prworkspace.PhaseIntake,
		prworkspace.PhaseCharter,
		prworkspace.PhasePlanning,
		prworkspace.PhaseReview,
		prworkspace.PhaseTriage,
		prworkspace.PhaseImplementation,
		prworkspace.PhaseValidation,
		prworkspace.PhaseCompletionAudit,
		prworkspace.PhasePublication,
		prworkspace.PhaseComplete:
		return true
	default:
		return false
	}
}

func validExecutionState(value prworkspace.ExecutionState) bool {
	switch value {
	case prworkspace.ExecutionQueued,
		prworkspace.ExecutionRunning,
		prworkspace.ExecutionWaitingGate,
		prworkspace.ExecutionWaitingUser,
		prworkspace.ExecutionSucceeded,
		prworkspace.ExecutionFailed,
		prworkspace.ExecutionBlocked,
		prworkspace.ExecutionCanceled,
		prworkspace.ExecutionStale,
		prworkspace.ExecutionUnknown:
		return true
	default:
		return false
	}
}

func hasOpenFinding(values []prworkspace.Finding) bool {
	for _, finding := range values {
		if finding.Disposition == prworkspace.FindingOpen {
			return true
		}
	}
	return false
}

func latestSucceededRepair(values []prworkspace.RepairAttempt) (prworkspace.RepairAttempt, bool) {
	if len(values) == 0 {
		return prworkspace.RepairAttempt{}, false
	}
	value := values[len(values)-1]
	if value.State == prworkspace.ExecutionSucceeded && value.ID != "" &&
		value.StageRunID != "" && value.CandidateSHA != "" {
		return value, true
	}
	return prworkspace.RepairAttempt{}, false
}

func validationForRepair(
	values []prworkspace.ValidationRun,
	repair prworkspace.RepairAttempt,
	hasRepair bool,
) (prworkspace.ValidationRun, bool) {
	if !hasRepair {
		return prworkspace.ValidationRun{}, false
	}
	for index := len(values) - 1; index >= 0; index-- {
		if values[index].StageRunID == repair.StageRunID &&
			values[index].RepairAttemptID == repair.ID && values[index].ID != "" {
			return values[index], true
		}
	}
	return prworkspace.ValidationRun{}, false
}

func branchPublicationForRepair(
	values []prworkspace.Publication,
	repair prworkspace.RepairAttempt,
	hasRepair bool,
) (prworkspace.Publication, bool) {
	if !hasRepair {
		return prworkspace.Publication{}, false
	}
	for index := len(values) - 1; index >= 0; index-- {
		value := values[index]
		if value.Kind == prworkspace.PublicationBranchPush && value.TargetID == repair.ID {
			return value, true
		}
	}
	return prworkspace.Publication{}, false
}

func activeHumanGate(
	values []prworkspace.GateRun,
) (prworkspace.GateRun, *prworkspace.GateForm, bool) {
	for index := len(values) - 1; index >= 0; index-- {
		gate := values[index]
		if gate.State != prworkspace.ExecutionWaitingUser && gate.State != prworkspace.ExecutionWaitingGate {
			continue
		}
		for turnIndex := len(gate.Turns) - 1; turnIndex >= 0; turnIndex-- {
			turn := gate.Turns[turnIndex]
			if turn.Status == "waiting" && turn.GateForm != nil {
				return gate, turn.GateForm, true
			}
		}
	}
	return prworkspace.GateRun{}, nil, false
}

func pendingCharterDecision(aggregate prworkspace.Aggregate) (prworkspace.Charter, bool) {
	if aggregate.Workspace.Phase != prworkspace.PhaseCharter ||
		aggregate.Workspace.ActiveCharterID != "" || len(aggregate.Charters) == 0 {
		return prworkspace.Charter{}, false
	}
	charter := aggregate.Charters[len(aggregate.Charters)-1]
	if charter.Confirmed {
		return prworkspace.Charter{}, false
	}
	if action, found := latestCharterGateAction(aggregate.Gates, charter.ID); found {
		// A revision cannot be accepted by confirming the same charter again.
		// Approval is committed atomically by the server, so an unconfirmed
		// charter paired with a completed approval is not actionable here.
		return charter, action == ""
	}
	return charter, true
}

func charterRevisionRequired(aggregate prworkspace.Aggregate) (prworkspace.Charter, bool) {
	if aggregate.Workspace.Phase != prworkspace.PhaseCharter ||
		aggregate.Workspace.ActiveCharterID != "" || len(aggregate.Charters) == 0 {
		return prworkspace.Charter{}, false
	}
	charter := aggregate.Charters[len(aggregate.Charters)-1]
	if charter.Confirmed {
		return prworkspace.Charter{}, false
	}
	action, found := latestCharterGateAction(aggregate.Gates, charter.ID)
	return charter, found && action == "revise"
}

func latestCharterGateAction(values []prworkspace.GateRun, charterID string) (string, bool) {
	for index := len(values) - 1; index >= 0; index-- {
		gate := values[index]
		if (gate.DecisionPoint != "pr.charter.confirm" &&
			gate.DecisionPoint != "pr.charter.reconfirm") ||
			(gate.TargetID != "" && gate.TargetID != charterID) {
			continue
		}
		if gate.State != prworkspace.ExecutionSucceeded &&
			gate.State != prworkspace.ExecutionFailed &&
			gate.State != prworkspace.ExecutionBlocked &&
			gate.State != prworkspace.ExecutionCanceled {
			continue
		}
		for turnIndex := len(gate.Turns) - 1; turnIndex >= 0; turnIndex-- {
			action, _ := gate.Turns[turnIndex].FieldValues["action"].(string)
			if action != "" {
				return action, true
			}
		}
		return "", true
	}
	return "", false
}

func completeEvidenceValid(
	aggregate prworkspace.Aggregate,
	repair prworkspace.RepairAttempt,
	hasRepair bool,
	validation prworkspace.ValidationRun,
	hasValidation bool,
	publication prworkspace.Publication,
	hasPublication bool,
) bool {
	provider := aggregate.ProviderSnapshot
	if aggregate.Workspace.ExecutionState != prworkspace.ExecutionSucceeded || !hasRepair ||
		!hasValidation || validation.State != prworkspace.ExecutionSucceeded ||
		validation.StageRunID != repair.StageRunID || validation.RepairAttemptID != repair.ID ||
		validation.CandidateSHA == "" ||
		!validationChecksPassed(validation.Checks) ||
		!hasPublication || publication.State != prworkspace.ExecutionSucceeded ||
		publication.TargetID != repair.ID || publication.ExternalID == "" ||
		publication.ExternalID != provider.PullRequestID || provider.PullNumber <= 0 ||
		provider.HeadSHA != repair.CandidateSHA || !validBranchName(provider.HeadRef) ||
		!validPullRequestURL(publication.ExternalURL, provider) {
		return false
	}
	// Validation CandidateSHA is the pinned pre-finalization tree; repair
	// CandidateSHA is replaced with the retained commit after validation. Their
	// RepairAttemptID plus StageRunID is the public exact-attempt correlation. The server
	// additionally pins the private publication-fence tree/tip before queuing.
	return true
}

func validationChecksPassed(checks []prworkspace.ValidationCheck) bool {
	if len(checks) == 0 {
		return false
	}
	for _, check := range checks {
		if check.ID == "" || check.Name == "" ||
			(check.Status != "passed" && check.Status != "skipped") {
			return false
		}
	}
	return true
}

func validPullRequestURL(raw string, provider prworkspace.ProviderSnapshot) bool {
	if raw == "" || provider.PullNumber <= 0 || provider.Repository == "" {
		return false
	}
	external, err := url.ParseRequestURI(raw)
	if err != nil || external.Scheme != "https" || external.User != nil ||
		external.RawQuery != "" || external.Fragment != "" {
		return false
	}
	origin, err := url.ParseRequestURI(provider.ProviderOrigin)
	if err != nil || !strings.EqualFold(external.Host, origin.Host) {
		return false
	}
	wanted := strings.TrimSuffix(origin.Path, "/") + "/" + provider.Repository +
		"/pull/" + strconv.FormatInt(provider.PullNumber, 10)
	return external.Path == wanted
}

func validBranchName(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 255 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return false
		}
	}
	return true
}

func terminalErrorCode(aggregate prworkspace.Aggregate) string {
	if aggregate.Workspace.Phase == prworkspace.PhaseTriage &&
		(len(aggregate.Findings) == 0 || hasOpenFinding(aggregate.Findings)) {
		return "implementation_unavailable"
	}
	for index := len(aggregate.Activity) - 1; index >= 0; index-- {
		if code, ok := aggregate.Activity[index].Metadata["code"].(string); ok {
			switch code {
			case "unsafe_provider", "implementation_unavailable":
				return code
			}
		}
	}
	for index := len(aggregate.ValidationRuns) - 1; index >= 0; index-- {
		for _, check := range aggregate.ValidationRuns[index].Checks {
			switch check.Status {
			case "infrastructure_error", "environment_unavailable":
				return "validation_unavailable"
			case "failed":
				return "validation_failed"
			}
		}
	}
	switch aggregate.Workspace.ExecutionState {
	case prworkspace.ExecutionBlocked:
		return "development_blocked"
	case prworkspace.ExecutionCanceled:
		return "development_canceled"
	case prworkspace.ExecutionStale:
		return "development_stale"
	case prworkspace.ExecutionUnknown:
		return "development_outcome_unknown"
	default:
		return "development_failed"
	}
}

func projectPendingGate(gate prworkspace.GateRun, form *prworkspace.GateForm) *PendingGate {
	fields := make([]GateField, 0, min(len(form.Fields), 16))
	for _, field := range form.Fields[:min(len(form.Fields), 16)] {
		if !gateFieldTypeSupported(field.Type) {
			continue
		}
		options := make([]GateFieldOption, 0, min(len(field.Options), 16))
		for _, option := range field.Options[:min(len(field.Options), 16)] {
			options = append(options, GateFieldOption{
				ID: boundedTerminalText(option.ID, 128), Label: boundedTerminalText(option.Label, 512),
			})
		}
		fields = append(fields, GateField{
			ID: boundedTerminalText(field.ID, 128), Type: string(field.Type),
			Label: boundedTerminalText(field.Label, 512), Required: field.Required,
			MinSelections: field.MinSelections, MaxSelections: field.MaxSelections, Options: options,
		})
	}
	evidence := gate.Evidence
	changedFiles := make([]string, 0, min(len(evidence.ChangedFiles), 20))
	for _, path := range evidence.ChangedFiles[:min(len(evidence.ChangedFiles), 20)] {
		changedFiles = append(changedFiles, boundedTerminalText(path, 512))
	}
	return &PendingGate{
		ID:              boundedTerminalText(gate.ID, 128),
		DecisionPoint:   boundedTerminalText(gate.DecisionPoint, 128),
		SubjectRevision: boundedTerminalText(gate.SubjectRevision, 256),
		Prompt:          boundedTerminalText(form.Prompt, 4096), Fields: fields,
		Evidence: PendingEvidence{
			CandidateRevision: boundedTerminalText(evidence.CandidateSHA, 256),
			ChangedFiles:      changedFiles, ValidationStatus: string(evidence.ValidationState),
			FindingCount: evidence.FindingCount, PublicationKind: string(evidence.PublicationKind),
			Repository: boundedTerminalText(evidence.Repository, 512),
		},
	}
}

func boundedTerminalText(value string, maximumBytes int) string {
	value = strings.ToValidUTF8(value, "")
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return ' '
		}
		return character
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	for len(value) > maximumBytes {
		_, width := utf8.DecodeLastRuneInString(value)
		value = value[:len(value)-width]
	}
	return value
}

func gateFieldTypeSupported(value gatetypes.GateFieldType) bool {
	switch value {
	case gatetypes.GateFieldShortText,
		gatetypes.GateFieldLongText,
		gatetypes.GateFieldBoolean,
		gatetypes.GateFieldSelect:
		return true
	default:
		return false
	}
}
