package eventing

import (
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
)

const maxPRFindingSourceValueBytes = 4096

// PRFindingSourceExecution is a protected persistence capability for resuming
// the exact AI execution that produced a finding. PRFinding deliberately keeps
// this value in an unexported field so ordinary finding, aggregate, patch, and
// mutation JSON projections cannot disclose session identity or revision.
//
// Storage adapters must use the explicit protected finding codec rather than
// relying on encoding/json to persist this value.
type PRFindingSourceExecution struct {
	ExecutionID     string `json:"execution-id"`
	WorkspaceID     string `json:"workspace-id"`
	Binding         string `json:"binding"`
	AgentID         string `json:"agent-id"`
	Session         string `json:"session"`
	SessionRevision string `json:"session-revision"`
	Tools           string `json:"tools"`
}

// SetProtectedSourceExecution attaches a validated source capability. Passing
// nil explicitly clears it; callers updating a durable finding are still
// subject to the store's immutable-provenance check.
func (finding *PRFinding) SetProtectedSourceExecution(source *PRFindingSourceExecution) error {
	if finding == nil {
		return fmt.Errorf("%w: finding is required", ErrInvalidPRWorkspace)
	}
	if source == nil {
		finding.protectedSource = nil
		return nil
	}
	if err := validatePRFindingSourceExecution(*source); err != nil {
		return err
	}
	cloned := *source
	finding.protectedSource = &cloned
	return nil
}

// ProtectedSourceExecution returns a copy of the protected source capability.
// It is an intentional internal adapter boundary; normal JSON marshaling never
// calls it and therefore never exposes the capability.
func (finding *PRFinding) ProtectedSourceExecution() (PRFindingSourceExecution, bool) {
	if finding.protectedSource == nil {
		return PRFindingSourceExecution{}, false
	}
	return *finding.protectedSource, true
}

func validatePRFindingSourceExecution(source PRFindingSourceExecution) error {
	if !validPRFindingSourceID(source.ExecutionID, "aix_") {
		return fmt.Errorf("%w: invalid finding source execution ID", ErrInvalidPRWorkspace)
	}
	if !validPRFindingSourceID(source.WorkspaceID, prWorkspaceIDPrefix) {
		return fmt.Errorf("%w: invalid finding source workspace ID", ErrInvalidPRWorkspace)
	}
	if source.AgentID != strings.TrimSpace(source.AgentID) ||
		!routing.IsCanonicalAgentID(source.AgentID) {
		return fmt.Errorf("%w: invalid finding source agent ID", ErrInvalidPRWorkspace)
	}
	if source.Binding != strings.ToLower(source.Binding) {
		return fmt.Errorf("%w: invalid finding source binding", ErrInvalidPRWorkspace)
	}
	for field, value := range map[string]string{
		"binding":          source.Binding,
		"session":          source.Session,
		"session revision": source.SessionRevision,
	} {
		if !validPRFindingSourceText(value) {
			return fmt.Errorf("%w: invalid finding source %s", ErrInvalidPRWorkspace, field)
		}
	}
	if source.Tools != "none" {
		return fmt.Errorf("%w: finding source tools must be none", ErrInvalidPRWorkspace)
	}
	if source.Session != prFindingSourceSessionKey(source) {
		return fmt.Errorf("%w: finding source session does not match its provenance", ErrInvalidPRWorkspace)
	}
	return nil
}

func prFindingSourceSessionKey(source PRFindingSourceExecution) string {
	scope := session.SessionScope{
		Version: session.ScopeVersionV1, AgentID: source.AgentID,
		Channel: "review", Account: routing.DefaultAccountID,
		Dimensions: []string{"execution", "workspace", "binding"},
		Values: map[string]string{
			"execution": source.ExecutionID,
			"workspace": source.WorkspaceID,
			"binding":   source.Binding,
		},
	}
	return session.BuildSessionKey(scope)
}

func validPRFindingSourceID(value, prefix string) bool {
	if value != strings.ToLower(value) || !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+32 {
		return false
	}
	_, err := hex.DecodeString(value[len(prefix):])
	return err == nil
}

func validPRFindingSourceText(value string) bool {
	return value != "" && value == strings.TrimSpace(value) &&
		len(value) <= maxPRFindingSourceValueBytes && utf8.ValidString(value) &&
		!strings.ContainsRune(value, '\x00')
}

func equalProtectedPRFindingSource(left, right PRFinding) bool {
	leftSource, leftOK := left.ProtectedSourceExecution()
	rightSource, rightOK := right.ProtectedSourceExecution()
	return leftOK == rightOK && (!leftOK || leftSource == rightSource)
}
