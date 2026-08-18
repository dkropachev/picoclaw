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
	if candidate.DefaultWorkflowConfigurationID != DefaultPRLifecycleWorkflowConfigurationID {
		t.Fatalf("default workflow configuration = %q", candidate.DefaultWorkflowConfigurationID)
	}
	builtin := candidate.WorkflowConfigurations[DefaultPRLifecycleWorkflowConfigurationID]
	if builtin.Name != DefaultPRLifecycleWorkflowConfigurationName || len(builtin.Bindings) != 0 {
		t.Fatalf("built-in workflow configuration = %#v", builtin)
	}
	if len(candidate.RepositoryAssignments) != 0 ||
		builtin.DeferredIssues.Mode != PRLifecycleDeferredIssuesAsk {
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
	workflowConfigurationID, selected, revision, err := candidate.WorkflowConfigurationForRepository(
		"https://github.com/", "repo-42",
	)
	if err != nil {
		t.Fatal(err)
	}
	if workflowConfigurationID != "automated" || selected.Name != "Automated" || revision == "" {
		t.Fatalf("selected = %q %#v %q", workflowConfigurationID, selected, revision)
	}
}

func TestPRLifecycleWorkflowConfigurationRevisionIgnoresBindingOrder(t *testing.T) {
	candidate := lifecycleConfigWithOverrides().WorkflowConfigurations["automated"]
	left, err := PRLifecycleWorkflowConfigurationRevision("automated", candidate)
	if err != nil {
		t.Fatal(err)
	}
	for leftIndex, rightIndex := 0, len(candidate.Bindings)-1; leftIndex < rightIndex; leftIndex, rightIndex = leftIndex+1, rightIndex-1 {
		candidate.Bindings[leftIndex], candidate.Bindings[rightIndex] = candidate.Bindings[rightIndex], candidate.Bindings[leftIndex]
	}
	right, err := PRLifecycleWorkflowConfigurationRevision("automated", candidate)
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("revisions differ by binding order: %q != %q", left, right)
	}
}

func TestPRLifecycleWorkflowConfigurationRevisionIncludesDeferredIssuePolicy(t *testing.T) {
	candidate := lifecycleConfigWithOverrides().WorkflowConfigurations["automated"]
	before, err := PRLifecycleWorkflowConfigurationRevision("automated", candidate)
	if err != nil {
		t.Fatal(err)
	}
	candidate.DeferredIssues.Mode = PRLifecycleDeferredIssuesOff
	after, err := PRLifecycleWorkflowConfigurationRevision("automated", candidate)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("deferred issue policy did not change the Workflow configuration revision")
	}
}

func TestPRLifecycleConfigRejectsInvalidWorkflowConfigurations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PRLifecycleConfig)
		want   string
	}{
		{name: "missing configs", mutate: func(c *PRLifecycleConfig) { c.WorkflowConfigurations = nil }, want: "must contain"},
		{name: "missing default selection", mutate: func(c *PRLifecycleConfig) { c.DefaultWorkflowConfigurationID = "" }, want: "default workflow configuration is required"},
		{name: "unknown default selection", mutate: func(c *PRLifecycleConfig) { c.DefaultWorkflowConfigurationID = "missing" }, want: "does not exist"},
		{name: "renamed builtin", mutate: func(c *PRLifecycleConfig) {
			v := c.WorkflowConfigurations[DefaultPRLifecycleWorkflowConfigurationID]
			v.Name = "Renamed"
			c.WorkflowConfigurations[DefaultPRLifecycleWorkflowConfigurationID] = v
		}, want: "built-in default"},
		{name: "builtin override", mutate: func(c *PRLifecycleConfig) {
			v := c.WorkflowConfigurations[DefaultPRLifecycleWorkflowConfigurationID]
			v.Bindings = append(v.Bindings, validHumanBinding())
			c.WorkflowConfigurations[DefaultPRLifecycleWorkflowConfigurationID] = v
		}, want: "built-in default"},
		{name: "snake config id", mutate: func(c *PRLifecycleConfig) {
			c.WorkflowConfigurations["bad_id"] = PRLifecycleWorkflowConfiguration{Name: "Bad", Bindings: []PRLifecycleGateBinding{}}
		}, want: "invalid identity"},
		{name: "duplicate name", mutate: func(c *PRLifecycleConfig) {
			c.WorkflowConfigurations["another"] = PRLifecycleWorkflowConfiguration{
				Name: "automated", Bindings: []PRLifecycleGateBinding{},
				DeferredIssues: PRLifecycleDeferredIssueConfig{Mode: PRLifecycleDeferredIssuesAsk},
			}
		}, want: "duplicate names"},
		{name: "nil bindings", mutate: func(c *PRLifecycleConfig) {
			v := c.WorkflowConfigurations["automated"]
			v.Bindings = nil
			c.WorkflowConfigurations["automated"] = v
		}, want: "bindings are required"},
		{name: "duplicate binding", mutate: func(c *PRLifecycleConfig) {
			v := c.WorkflowConfigurations["automated"]
			v.Bindings = append(v.Bindings, v.Bindings[0])
			c.WorkflowConfigurations["automated"] = v
		}, want: "duplicate binding"},
		{name: "relative workflow ref", mutate: func(c *PRLifecycleConfig) {
			v := c.WorkflowConfigurations["automated"]
			v.Bindings[0].WorkflowRef = "pr-lifecycle.yml"
			c.WorkflowConfigurations["automated"] = v
		}, want: "workflow-ref is invalid"},
		{name: "runtime gate ref", mutate: func(c *PRLifecycleConfig) {
			v := c.WorkflowConfigurations["automated"]
			v.Bindings[0].GateRef = "${{ gates.charter-decision }}"
			c.WorkflowConfigurations["automated"] = v
		}, want: "static full path"},
		{name: "relative gate ref", mutate: func(c *PRLifecycleConfig) {
			v := c.WorkflowConfigurations["automated"]
			v.Bindings[0].GateRef = "charter-decision"
			c.WorkflowConfigurations["automated"] = v
		}, want: "static full path"},
		{name: "partial human action", mutate: func(c *PRLifecycleConfig) {
			v := c.WorkflowConfigurations["automated"]
			v.Bindings[0].Action = &gatetypes.GateAction{Type: gatetypes.GateActionHuman, Prompt: "not allowed"}
			c.WorkflowConfigurations["automated"] = v
		}, want: "human gate action"},
		{name: "partial AI action", mutate: func(c *PRLifecycleConfig) {
			v := c.WorkflowConfigurations["automated"]
			v.Bindings[0].Action = &gatetypes.GateAction{Type: gatetypes.GateActionAI, AgentID: "main"}
			c.WorkflowConfigurations["automated"] = v
		}, want: "requires prompt"},
		{name: "partial workflow action", mutate: func(c *PRLifecycleConfig) {
			v := c.WorkflowConfigurations["automated"]
			v.Bindings[0].Action = &gatetypes.GateAction{Type: gatetypes.GateActionWorkflow}
			c.WorkflowConfigurations["automated"] = v
		}, want: "requires workflow-ref"},
		{name: "unsafe private AI defaults", mutate: func(c *PRLifecycleConfig) {
			v := c.WorkflowConfigurations["automated"]
			action := *v.Bindings[0].Action
			action.Tools = "inherit"
			v.Bindings[0].Action = &action
			c.WorkflowConfigurations["automated"] = v
		}, want: "requires tools: none"},
		{name: "noncanonical workflow action ref", mutate: func(c *PRLifecycleConfig) {
			v := c.WorkflowConfigurations["automated"]
			v.Bindings[2].Action.WorkflowRef = "workflows/gate-actions/publication"
			c.WorkflowConfigurations["automated"] = v
		}, want: "workflow-ref is invalid"},
		{name: "unknown assignment", mutate: func(c *PRLifecycleConfig) {
			c.RepositoryAssignments["https://github.com|repo-42"] = "missing"
		}, want: "selects missing"},
		{name: "nil assignments", mutate: func(c *PRLifecycleConfig) {
			c.RepositoryAssignments = nil
		}, want: "assignments are required"},
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
	workflowConfiguration := candidate.WorkflowConfigurations["automated"]
	workflowConfiguration.Bindings[0].Action = nil
	candidate.WorkflowConfigurations["automated"] = workflowConfiguration
	if err := candidate.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestPRLifecycleConfigRejectsUnknownOverrideAgent(t *testing.T) {
	candidate := lifecycleConfigWithOverrides()
	config := candidate.WorkflowConfigurations["automated"]
	action := *config.Bindings[0].Action
	action.AgentID = "missing-agent"
	config.Bindings[0].Action = &action
	candidate.WorkflowConfigurations["automated"] = config
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
		"workflow-configurations", "default-workflow-configuration", "repository-assignments",
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
		`{"gate-configs":{},"default-gate-config":"default"}`,
		`{"gate-configs":{},"default-gate-config":"default","unknown":true}`,
		`{"workflow-configurations":{}} {}`,
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
		candidate.WorkflowConfigurations["mode"] = PRLifecycleWorkflowConfiguration{
			Name: "Mode", Bindings: []PRLifecycleGateBinding{},
			DeferredIssues: PRLifecycleDeferredIssueConfig{Mode: mode},
		}
		candidate.DefaultWorkflowConfigurationID = "mode"
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
	candidate.WorkflowConfigurations["automated"] = PRLifecycleWorkflowConfiguration{
		Name: "Automated", DeferredIssues: PRLifecycleDeferredIssueConfig{Mode: PRLifecycleDeferredIssuesAutomatic},
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
	candidate.DefaultWorkflowConfigurationID = "automated"
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
	id, selected, _, err := candidate.WorkflowConfigurationForRepository("HTTPS://GITHUB.COM", "REPO-42")
	if err != nil {
		t.Fatal(err)
	}
	if id != "automated" || !reflect.DeepEqual(selected, candidate.WorkflowConfigurations["automated"]) {
		t.Fatalf("selection = %q %#v", id, selected)
	}
}

func TestPRLifecycleConfigSelectionNormalizesAllTrailingOriginSlashes(t *testing.T) {
	candidate := lifecycleConfigWithOverrides()
	id, _, _, err := candidate.WorkflowConfigurationForRepository("HTTPS://GITHUB.COM///", "REPO-42")
	if err != nil {
		t.Fatal(err)
	}
	if id != "automated" {
		t.Fatalf("selection = %q, want automated", id)
	}
}

func TestPRLifecycleConfigRejectsCanonicalRepositoryAssignmentCollision(t *testing.T) {
	candidate := lifecycleConfigWithOverrides()
	candidate.RepositoryAssignments["HTTPS://GITHUB.COM///|REPO-42"] = "automated"
	err := candidate.Validate()
	if err == nil || !strings.Contains(err.Error(), "collide") {
		t.Fatalf("Validate() error = %v, want canonical collision", err)
	}
}

func TestPRLifecycleConfigAcceptsOnlyMinimalSourceAIAction(t *testing.T) {
	candidate := DefaultPRLifecycleConfig()
	candidate.WorkflowConfigurations["source"] = PRLifecycleWorkflowConfiguration{
		Name: "Originating session", DeferredIssues: PRLifecycleDeferredIssueConfig{Mode: PRLifecycleDeferredIssuesAsk},
		Bindings: []PRLifecycleGateBinding{{
			WorkflowRef: PRLifecycleWorkflowRef, GateRef: "gates.finding-classify",
			Action: &gatetypes.GateAction{
				Type: gatetypes.GateActionAI, Session: "source",
				Prompt: "Reassess the finding from its originating execution.",
			},
		}},
	}
	candidate.DefaultWorkflowConfigurationID = "source"
	if err := candidate.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := candidate
	config := invalid.WorkflowConfigurations["source"]
	action := *config.Bindings[0].Action
	action.AgentID, action.Tools = "main", "none"
	config.Bindings[0].Action = &action
	invalid.WorkflowConfigurations = map[string]PRLifecycleWorkflowConfiguration{
		"default": candidate.WorkflowConfigurations["default"], "source": config,
	}
	if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "derives agent") {
		t.Fatalf("source action with derived fields error = %v", err)
	}

	unsupported := DefaultPRLifecycleConfig()
	unsupported.WorkflowConfigurations["source"] = PRLifecycleWorkflowConfiguration{
		Name: "Unsupported source", DeferredIssues: PRLifecycleDeferredIssueConfig{Mode: PRLifecycleDeferredIssuesAsk},
		Bindings: []PRLifecycleGateBinding{{
			WorkflowRef: PRLifecycleWorkflowRef, GateRef: "gates.charter-confirm",
			Action: &gatetypes.GateAction{
				Type: gatetypes.GateActionAI, Session: "source",
				Prompt: "Reassess the finding from its originating execution.",
			},
		}},
	}
	unsupported.DefaultWorkflowConfigurationID = "source"
	if err := unsupported.Validate(); err == nil || !strings.Contains(err.Error(), "single-finding classification") {
		t.Fatalf("unsupported source gate error = %v", err)
	}
}
