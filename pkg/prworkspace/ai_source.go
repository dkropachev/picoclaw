package prworkspace

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/routing"
	"github.com/sipeed/picoclaw/pkg/session"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const maxAISourceCapabilityBytes = 4096

func validAIExecutionSource(source *AIExecutionSource) bool {
	if source == nil ||
		!validOpaqueID(source.ExecutionID, "aix_") ||
		source.ExecutionID != strings.ToLower(source.ExecutionID) ||
		!validOpaqueID(source.WorkspaceID, "prw_") ||
		source.WorkspaceID != strings.ToLower(source.WorkspaceID) ||
		source.AgentID != strings.TrimSpace(source.AgentID) ||
		!routing.IsCanonicalAgentID(source.AgentID) ||
		!validAISourceCapability(source.Binding) ||
		source.Binding != strings.ToLower(source.Binding) ||
		!validAISourceCapability(source.SessionRevision) ||
		source.Tools != workflows.AgentToolsNone {
		return false
	}
	return source.Session == aiExecutionSourceSessionKey(source)
}

func aiExecutionSourceSessionKey(source *AIExecutionSource) string {
	return session.BuildSessionKey(aiExecutionSourceSessionScope(source))
}

// aiExecutionSourceSessionScope is the single protected session identity used
// by both source capture and later Gate execution. The opaque session key binds
// every provenance dimension, so a durable finding cannot redirect a source
// action to another protected session owned by the same agent.
func aiExecutionSourceSessionScope(source *AIExecutionSource) session.SessionScope {
	return session.SessionScope{
		Version: session.ScopeVersionV1,
		AgentID: source.AgentID,
		Channel: "review",
		Account: routing.DefaultAccountID,
		Dimensions: []string{
			"execution", "workspace", "binding",
		},
		Values: map[string]string{
			"execution": source.ExecutionID,
			"workspace": source.WorkspaceID,
			"binding":   source.Binding,
		},
	}
}

func validAISourceCapability(value string) bool {
	return value != "" && value == strings.TrimSpace(value) &&
		utf8.ValidString(value) && len(value) <= maxAISourceCapabilityBytes &&
		!strings.ContainsRune(value, '\x00')
}

func sameAIExecutionSource(left, right *AIExecutionSource) bool {
	if !validAIExecutionSource(left) || !validAIExecutionSource(right) {
		return false
	}
	return *left == *right
}

func sourceForGateSubject(subject map[string]any, workspaceID string) (*AIExecutionSource, error) {
	sources := collectGateSubjectSources(subject)
	var selected *AIExecutionSource
	for _, source := range sources {
		if !validAIExecutionSource(source) || source.WorkspaceID != workspaceID {
			return nil, fmt.Errorf("originating AI execution provenance is invalid")
		}
		if selected == nil {
			selected = source
			continue
		}
		if !sameAIExecutionSource(selected, source) {
			return nil, fmt.Errorf("gate subject contains multiple originating AI sessions")
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("gate subject has no originating AI session")
	}
	return cloneAIExecutionSource(selected), nil
}

func collectGateSubjectSources(subject map[string]any) []*AIExecutionSource {
	var sources []*AIExecutionSource
	var collect func(any)
	collect = func(value any) {
		switch typed := value.(type) {
		case Finding:
			if typed.source != nil {
				sources = append(sources, typed.source)
			}
		case *Finding:
			if typed != nil && typed.source != nil {
				sources = append(sources, typed.source)
			}
		case []Finding:
			for index := range typed {
				collect(typed[index])
			}
		case ReviewRound:
			if typed.Source != nil {
				sources = append(sources, typed.Source)
			}
		case CompletionRound:
			if typed.Source != nil {
				sources = append(sources, typed.Source)
			}
		case []ReviewRound:
			for index := range typed {
				collect(typed[index])
			}
		case []CompletionRound:
			for index := range typed {
				collect(typed[index])
			}
		case map[string]any:
			for _, item := range typed {
				collect(item)
			}
		case []any:
			for _, item := range typed {
				collect(item)
			}
		}
	}
	collect(subject)
	return sources
}

func fingerprintGateSubject(subject map[string]any) (string, error) {
	base, err := fingerprintValue(subject)
	if err != nil {
		return "", err
	}
	sources := collectGateSubjectSources(subject)
	if len(sources) == 0 {
		return base, nil
	}
	values := make([]AIExecutionSource, 0, len(sources))
	for _, source := range sources {
		if !validAIExecutionSource(source) {
			return "", fmt.Errorf("gate subject contains invalid AI source provenance")
		}
		values = append(values, *source)
	}
	sort.Slice(values, func(left, right int) bool {
		leftFields := [...]string{
			values[left].ExecutionID, values[left].WorkspaceID, values[left].Binding,
			values[left].AgentID, values[left].Session, values[left].SessionRevision,
			values[left].Tools,
		}
		rightFields := [...]string{
			values[right].ExecutionID, values[right].WorkspaceID, values[right].Binding,
			values[right].AgentID, values[right].Session, values[right].SessionRevision,
			values[right].Tools,
		}
		for index := range leftFields {
			if leftFields[index] != rightFields[index] {
				return leftFields[index] < rightFields[index]
			}
		}
		return false
	})
	return fingerprintValue(struct {
		Base    string              `json:"base"`
		Sources []AIExecutionSource `json:"sources"`
	}{Base: base, Sources: values})
}
