package session

// ScopeVersionV1 is the first structured session-scope schema version.
const ScopeVersionV1 = 1

// SessionScope describes the semantic session partition selected for a turn.
type SessionScope struct {
	Version    int               `json:"version"`
	AgentID    string            `json:"agent_id"`
	Channel    string            `json:"channel"`
	Account    string            `json:"account"`
	Dimensions []string          `json:"dimensions"`
	Values     map[string]string `json:"values"`
}

// CloneScope returns a deep copy of scope.
func CloneScope(scope *SessionScope) *SessionScope {
	if scope == nil {
		return nil
	}
	cloned := *scope
	if scope.Dimensions != nil {
		cloned.Dimensions = make([]string, len(scope.Dimensions))
		copy(cloned.Dimensions, scope.Dimensions)
	}
	if scope.Values != nil {
		cloned.Values = make(map[string]string, len(scope.Values))
		for key, value := range scope.Values {
			cloned.Values[key] = value
		}
	}
	return &cloned
}
