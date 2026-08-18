package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

func TestDefaultPRLifecycleConfigUsesInheritedWorkflowDefaults(t *testing.T) {
	candidate := DefaultPRLifecycleConfig()
	if err := candidate.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if candidate.DefaultGateConfigID != DefaultPRLifecycleGateConfigID {
		t.Fatalf("default gate configuration = %q", candidate.DefaultGateConfigID)
	}
	builtin := candidate.GateConfigs[DefaultPRLifecycleGateConfigID]
	if builtin.Name != DefaultPRLifecycleGateConfigName || len(builtin.Bindings) != 0 {
		t.Fatalf("built-in gate configuration = %#v", builtin)
	}
	if len(candidate.RepositoryAssignments) != 0 ||
		candidate.DeferredIssues.Mode != PRLifecycleDeferredIssuesAsk {
		t.Fatalf("default lifecycle = %#v", candidate)
	}
}

func TestPRLifecycleConfigAcceptsAtomicGateActionOverrides(t *testing.T) {
	candidate := lifecycleConfigWithOverrides()
	if err := candidate.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if err := candidate.ValidateAgentReferences(AgentsConfig{}); err != nil {
		t.Fatalf("ValidateAgentReferences() error = %v", err)
	}
	configID, selected, revision, err := candidate.ConfigForRepository(
		"https://github.com/", "repo-42",
	)
	if err != nil {
		t.Fatal(err)
	}
	if configID != "automated" || selected.Name != "Automated" || revision == "" {
		t.Fatalf("selected = %q %#v %q", configID, selected, revision)
	}
}

func TestPRLifecycleGateConfigRevisionIgnoresBindingOrder(t *testing.T) {
	candidate := lifecycleConfigWithOverrides().GateConfigs["automated"]
	left, err := PRLifecycleGateConfigRevision("automated", candidate)
	if err != nil {
		t.Fatal(err)
	}
	for leftIndex, rightIndex := 0, len(candidate.Bindings)-1; leftIndex < rightIndex; leftIndex, rightIndex = leftIndex+1, rightIndex-1 {
		candidate.Bindings[leftIndex], candidate.Bindings[rightIndex] = candidate.Bindings[rightIndex], candidate.Bindings[leftIndex]
	}
	right, err := PRLifecycleGateConfigRevision("automated", candidate)
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("revisions differ by binding order: %q != %q", left, right)
	}
}

func TestPRLifecycleConfigRejectsInvalidGateConfigurations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PRLifecycleConfig)
		want   string
	}{
		{name: "missing configs", mutate: func(c *PRLifecycleConfig) { c.GateConfigs = nil }, want: "must contain"},
		{name: "missing default selection", mutate: func(c *PRLifecycleConfig) { c.DefaultGateConfigID = "" }, want: "default gate configuration is required"},
		{name: "unknown default selection", mutate: func(c *PRLifecycleConfig) { c.DefaultGateConfigID = "missing" }, want: "does not exist"},
		{name: "renamed builtin", mutate: func(c *PRLifecycleConfig) {
			v := c.GateConfigs[DefaultPRLifecycleGateConfigID]
			v.Name = "Renamed"
			c.GateConfigs[DefaultPRLifecycleGateConfigID] = v
		}, want: "built-in default"},
		{name: "builtin override", mutate: func(c *PRLifecycleConfig) {
			v := c.GateConfigs[DefaultPRLifecycleGateConfigID]
			v.Bindings = append(v.Bindings, validHumanBinding())
			c.GateConfigs[DefaultPRLifecycleGateConfigID] = v
		}, want: "built-in default"},
		{name: "snake config id", mutate: func(c *PRLifecycleConfig) {
			c.GateConfigs["bad_id"] = PRLifecycleGateConfig{Name: "Bad", Bindings: []PRLifecycleGateBinding{}}
		}, want: "invalid identity"},
		{name: "duplicate name", mutate: func(c *PRLifecycleConfig) {
			c.GateConfigs["another"] = PRLifecycleGateConfig{Name: "automated", Bindings: []PRLifecycleGateBinding{}}
		}, want: "duplicate names"},
		{name: "duplicate binding", mutate: func(c *PRLifecycleConfig) {
			v := c.GateConfigs["automated"]
			v.Bindings = append(v.Bindings, v.Bindings[0])
			c.GateConfigs["automated"] = v
		}, want: "duplicate binding"},
		{name: "relative workflow ref", mutate: func(c *PRLifecycleConfig) {
			v := c.GateConfigs["automated"]
			v.Bindings[0].WorkflowRef = "pr-lifecycle.yml"
			c.GateConfigs["automated"] = v
		}, want: "workflow-ref is invalid"},
		{name: "runtime gate ref", mutate: func(c *PRLifecycleConfig) {
			v := c.GateConfigs["automated"]
			v.Bindings[0].GateRef = "${{ gates.charter-decision }}"
			c.GateConfigs["automated"] = v
		}, want: "static full path"},
		{name: "relative gate ref", mutate: func(c *PRLifecycleConfig) {
			v := c.GateConfigs["automated"]
			v.Bindings[0].GateRef = "charter-decision"
			c.GateConfigs["automated"] = v
		}, want: "static full path"},
		{name: "partial human action", mutate: func(c *PRLifecycleConfig) {
			v := c.GateConfigs["automated"]
			v.Bindings[0].Action = &gatetypes.GateAction{Type: gatetypes.GateActionHuman, Prompt: "not allowed"}
			c.GateConfigs["automated"] = v
		}, want: "human gate action"},
		{name: "partial AI action", mutate: func(c *PRLifecycleConfig) {
			v := c.GateConfigs["automated"]
			v.Bindings[0].Action = &gatetypes.GateAction{Type: gatetypes.GateActionAI, AgentID: "main"}
			c.GateConfigs["automated"] = v
		}, want: "requires prompt"},
		{name: "partial workflow action", mutate: func(c *PRLifecycleConfig) {
			v := c.GateConfigs["automated"]
			v.Bindings[0].Action = &gatetypes.GateAction{Type: gatetypes.GateActionWorkflow}
			c.GateConfigs["automated"] = v
		}, want: "requires workflow-ref"},
		{name: "unsafe private AI defaults", mutate: func(c *PRLifecycleConfig) {
			v := c.GateConfigs["automated"]
			action := *v.Bindings[0].Action
			action.Tools = "inherit"
			v.Bindings[0].Action = &action
			c.GateConfigs["automated"] = v
		}, want: "requires tools: none"},
		{name: "noncanonical workflow action ref", mutate: func(c *PRLifecycleConfig) {
			v := c.GateConfigs["automated"]
			v.Bindings[2].Action.WorkflowRef = "workflows/gate-actions/publication"
			c.GateConfigs["automated"] = v
		}, want: "workflow-ref is invalid"},
		{name: "unknown assignment", mutate: func(c *PRLifecycleConfig) {
			c.RepositoryAssignments["https://github.com|repo-42"] = "missing"
		}, want: "selects missing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := lifecycleConfigWithOverrides()
			test.mutate(&candidate)
			err := candidate.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestPRLifecycleConfigBindingMayExplicitlyInheritWorkflowDefault(t *testing.T) {
	candidate := lifecycleConfigWithOverrides()
	gateConfig := candidate.GateConfigs["automated"]
	gateConfig.Bindings[0].Action = nil
	candidate.GateConfigs["automated"] = gateConfig
	if err := candidate.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestPRLifecycleConfigRejectsUnknownOverrideAgent(t *testing.T) {
	candidate := lifecycleConfigWithOverrides()
	config := candidate.GateConfigs["automated"]
	action := *config.Bindings[0].Action
	action.AgentID = "missing-agent"
	config.Bindings[0].Action = &action
	candidate.GateConfigs["automated"] = config
	if err := candidate.Validate(); err != nil {
		t.Fatalf("shape validation error = %v", err)
	}
	if err := candidate.ValidateAgentReferences(AgentsConfig{}); err == nil ||
		!strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("ValidateAgentReferences() error = %v", err)
	}
}

func TestPRLifecycleConfigWireNamesAreKebabCase(t *testing.T) {
	encoded, err := json.Marshal(lifecycleConfigWithOverrides())
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"gate_configs", "default_gate_config", "repository_assignments",
		"deferred_issues", "workflow_ref", "gate_ref", "agent_id",
		"review_minimum_additional", "semantic_lines",
	} {
		if strings.Contains(string(encoded), `"`+forbidden+`"`) {
			t.Fatalf("wire JSON contains snake-case key %q: %s", forbidden, encoded)
		}
	}
	for _, required := range []string{
		"gate-configs", "default-gate-config", "repository-assignments",
		"deferred-issues", "workflow-ref", "gate-ref", "agent-id",
		"review-minimum-additional", "semantic-lines",
	} {
		if !strings.Contains(string(encoded), `"`+required+`"`) {
			t.Fatalf("wire JSON omits kebab-case key %q: %s", required, encoded)
		}
	}
}

func TestPRLifecycleConfigRejectsRetiredAndUnknownWireFields(t *testing.T) {
	for _, raw := range []string{
		`{"gate-profiles":{},"default-gate-profile":"default"}`,
		`{"gate_profiles":{},"default_gate_profile_id":"default"}`,
		`{"gate-configs":{},"default-gate-config":"default","unknown":true}`,
		`{"gate-configs":{}} {}`,
	} {
		var decoded PRLifecycleConfig
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			t.Fatalf("retired/unknown PR lifecycle config was accepted: %s", raw)
		}
	}
}

func TestPRLifecycleDeferredIssueModesAndThresholdsRemainExact(t *testing.T) {
	for _, mode := range []PRLifecycleDeferredIssueMode{
		PRLifecycleDeferredIssuesOff,
		PRLifecycleDeferredIssuesAsk,
		PRLifecycleDeferredIssuesAutomatic,
	} {
		candidate := DefaultPRLifecycleConfig()
		candidate.DeferredIssues.Mode = mode
		if err := candidate.Validate(); err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
	}
	candidate := DefaultPRLifecycleConfig()
	candidate.Scope.S.Files = candidate.Scope.XS.Files - 1
	if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), "monotonic") {
		t.Fatalf("non-monotonic scope error = %v", err)
	}
}

func lifecycleConfigWithOverrides() PRLifecycleConfig {
	candidate := DefaultPRLifecycleConfig()
	candidate.GateConfigs["automated"] = PRLifecycleGateConfig{
		Name: "Automated",
		Bindings: []PRLifecycleGateBinding{
			{
				WorkflowRef: "workflows/pr-lifecycle.yml",
				GateRef:     "gates.charter-confirm",
				Action: &gatetypes.GateAction{
					Type: gatetypes.GateActionAI, AgentID: "main",
					Prompt:  "Review the charter and complete the gate fields.",
					Session: "ephemeral", History: "none", Cache: "none", Tools: "none",
				},
			},
			{
				WorkflowRef: "workflows/pr-lifecycle.yml",
				GateRef:     "gates.finding-classify",
				Action: &gatetypes.GateAction{
					Type:   gatetypes.GateActionDeterministic,
					Fields: map[string]any{"action": "keep-in-pr"},
				},
			},
			{
				WorkflowRef: "workflows/pr-lifecycle.yml",
				GateRef:     "gates.review-publish",
				Action: &gatetypes.GateAction{
					Type:        gatetypes.GateActionWorkflow,
					WorkflowRef: "workflows/gate-actions/publication.yml",
				},
			},
		},
	}
	candidate.DefaultGateConfigID = "automated"
	candidate.RepositoryAssignments["https://github.com|repo-42"] = "automated"
	return candidate
}

func validHumanBinding() PRLifecycleGateBinding {
	return PRLifecycleGateBinding{
		WorkflowRef: "workflows/pr-lifecycle.yml",
		GateRef:     "gates.charter-confirm",
		Action:      &gatetypes.GateAction{Type: gatetypes.GateActionHuman},
	}
}

func TestPRLifecycleConfigSelectionIsCaseInsensitiveForRepositoryIdentity(t *testing.T) {
	candidate := lifecycleConfigWithOverrides()
	id, selected, _, err := candidate.ConfigForRepository("HTTPS://GITHUB.COM", "REPO-42")
	if err != nil {
		t.Fatal(err)
	}
	if id != "automated" || !reflect.DeepEqual(selected, candidate.GateConfigs["automated"]) {
		t.Fatalf("selection = %q %#v", id, selected)
	}
}
