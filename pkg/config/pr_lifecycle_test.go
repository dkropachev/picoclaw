package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

func TestDefaultPRLifecycleConfigIsValidAndSerious(t *testing.T) {
	config := DefaultPRLifecycleConfig()
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	profile := config.GateProfiles[DefaultPRLifecycleGateProfileID]
	for _, point := range []string{"pr.charter.confirm", "pr.implementation.complete", "pr.deferred.publish", "pr.correction.promote"} {
		workflow, exists := profile.Workflows[point]
		if !exists || len(workflow.Stages) == 0 || workflow.Stages[0].Kind != "human" {
			t.Fatalf("default authorization %q = %#v", point, workflow)
		}
	}
	if config.Nudge.ReviewMinimumAdditional != 2 || config.Nudge.CompletionMinimumAdditional != 2 {
		t.Fatalf("nudge defaults = %#v", config.Nudge)
	}
	if config.DeferredIssues.Mode != PRLifecycleDeferredIssuesAsk {
		t.Fatalf("deferred issue defaults = %#v", config.DeferredIssues)
	}
	for point, want := range map[string]string{
		"pr.charter.confirm":   "Approve the PR purpose, type, included scope, exclusions, and non-goals?",
		"pr.charter.reconfirm": "Approve the revised PR purpose, type, included scope, exclusions, and non-goals?",
	} {
		workflow := profile.Workflows[point]
		questions, ok := workflow.Stages[0].Questions.([]any)
		if !ok || len(questions) != 1 || questions[0] != want {
			t.Fatalf("default charter questions for %q = %#v, want %q", point, workflow.Stages[0].Questions, want)
		}
	}
	for point, want := range map[string]string{
		"pr.charter.confirm":   "Confirm PR charter",
		"pr.charter.reconfirm": "Confirm revised PR charter",
	} {
		if got := profile.Workflows[point].Name; got != want {
			t.Fatalf("default workflow name for %q = %q, want %q", point, got, want)
		}
	}
}

func TestPRLifecycleRejectsWorkflowPurposeOutsideDecisionPointContract(t *testing.T) {
	tests := []struct {
		point        string
		wrongPurpose gatetypes.GatePurpose
		wantPurpose  gatetypes.GatePurpose
	}{
		{
			point: "pr.charter.reconfirm", wrongPurpose: gatetypes.GatePurposeClassification,
			wantPurpose: gatetypes.GatePurposeAuthorization,
		},
		{
			point: "pr.finding.classify", wrongPurpose: gatetypes.GatePurposeAuthorization,
			wantPurpose: gatetypes.GatePurposeClassification,
		},
	}
	for _, test := range tests {
		t.Run(test.point, func(t *testing.T) {
			candidate := DefaultPRLifecycleConfig()
			profile := candidate.GateProfiles[DefaultPRLifecycleGateProfileID]
			workflow := profile.Workflows[test.point]
			workflow.Purpose = test.wrongPurpose
			profile.Workflows[test.point] = workflow
			candidate.GateProfiles[DefaultPRLifecycleGateProfileID] = profile

			err := candidate.Validate()
			want := "purpose must be \"" + string(test.wantPurpose) + "\""
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("Validate() error = %v, want %q", err, want)
			}
		})
	}
}

func TestPRLifecycleDeferredIssueModesAreExact(t *testing.T) {
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
	candidate.DeferredIssues.Mode = "prompt"
	if err := candidate.Validate(); err == nil {
		t.Fatal("invalid deferred issue mode accepted")
	}
}

func TestPRLifecycleRejectsDecisionPointsOutsideClosedCatalog(t *testing.T) {
	addUnknownDecisionPoint := func(candidate *PRLifecycleConfig) {
		profile := candidate.GateProfiles[DefaultPRLifecycleGateProfileID]
		workflow := profile.Workflows["pr.charter.confirm"]
		workflow.ID = "pr-custom-undeclared"
		workflow.Name = "pr.custom.undeclared"
		workflow.DecisionPoint = "pr.custom.undeclared"
		profile.Workflows[workflow.DecisionPoint] = workflow
		candidate.GateProfiles[DefaultPRLifecycleGateProfileID] = profile
	}

	candidate := DefaultPRLifecycleConfig()
	addUnknownDecisionPoint(&candidate)
	if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), `unknown decision point "pr.custom.undeclared"`) {
		t.Fatalf("Validate() error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultConfig()
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var edited Config
	if err := json.Unmarshal(data, &edited); err != nil {
		t.Fatal(err)
	}
	addUnknownDecisionPoint(&edited.PRLifecycle)
	data, err = json.Marshal(&edited)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), `unknown decision point "pr.custom.undeclared"`) {
		t.Fatalf("LoadConfig() error = %v", err)
	}
}

func TestPRLifecycleProfileAssignmentUsesProviderRepositoryIdentity(t *testing.T) {
	config := DefaultPRLifecycleConfig()
	config.RepositoryAssignments["https://github.example|123"] = "default"
	id, _, revision, err := config.ProfileForRepository("https://github.example/", "123")
	if err != nil {
		t.Fatal(err)
	}
	if id != "default" || len(revision) != len("sha256:")+64 {
		t.Fatalf("profile = %q revision=%q", id, revision)
	}
}

func TestPRLifecycleValidationUsesRuntimeGateSemantics(t *testing.T) {
	tests := []struct {
		name      string
		stages    []gatetypes.GateStageSpec
		wantError string
	}{
		{
			name: "valid mixed workflow",
			stages: []gatetypes.GateStageSpec{
				{ID: "zero", Kind: gatetypes.GateZero},
				{ID: "condition", Kind: gatetypes.GateDeterministic, Title: "Condition", When: "inputs.gate_subject.ready == true"},
				{ID: "isolated", Kind: gatetypes.GateAIIsolatedContext, Title: "Isolated", AgentID: "reviewer", Criteria: "Check evidence."},
				{ID: "working", Kind: gatetypes.GateAIWorkingContext, Title: "Working", AgentID: "main", Criteria: "Check discussion."},
				{ID: "human", Kind: gatetypes.GateHuman, Title: "Approve", Questions: []any{"Approve?"}},
			},
		},
		{
			name: "malformed deterministic expression",
			stages: []gatetypes.GateStageSpec{{
				ID: "condition", Kind: gatetypes.GateDeterministic, Title: "Condition",
				When: "inputs.gate_subject.ready ==",
			}},
			wantError: "unsupported expression syntax",
		},
		{
			name: "noncanonical agent",
			stages: []gatetypes.GateStageSpec{{
				ID: "working", Kind: gatetypes.GateAIWorkingContext, Title: "Working",
				AgentID: "Main", Criteria: "Check discussion.",
			}},
			wantError: "agent",
		},
		{
			name: "mixed working owners",
			stages: []gatetypes.GateStageSpec{
				{ID: "first", Kind: gatetypes.GateAIWorkingContext, Title: "First", AgentID: "main", Criteria: "Check one."},
				{ID: "second", Kind: gatetypes.GateAIWorkingContext, Title: "Second", AgentID: "reviewer", Criteria: "Check two."},
			},
			wantError: "one session-owning agent",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := DefaultPRLifecycleConfig()
			workflow := candidate.GateProfiles[DefaultPRLifecycleGateProfileID].Workflows["pr.charter.confirm"]
			workflow.Stages = test.stages
			profile := candidate.GateProfiles[DefaultPRLifecycleGateProfileID]
			profile.Workflows["pr.charter.confirm"] = workflow
			candidate.GateProfiles[DefaultPRLifecycleGateProfileID] = profile
			err := candidate.Validate()
			if test.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Validate() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestFullConfigRejectsUnknownPRLifecycleGateAgents(t *testing.T) {
	workflowWithAgent := func(agentID string) PRLifecycleConfig {
		candidate := DefaultPRLifecycleConfig()
		profile := candidate.GateProfiles[DefaultPRLifecycleGateProfileID]
		workflow := profile.Workflows["pr.charter.confirm"]
		workflow.Stages = []gatetypes.GateStageSpec{{
			ID: "ai", Kind: gatetypes.GateAIIsolatedContext, AgentID: agentID,
			Title: "Check", Criteria: "Check the charter.",
		}}
		profile.Workflows["pr.charter.confirm"] = workflow
		candidate.GateProfiles[DefaultPRLifecycleGateProfileID] = profile
		return candidate
	}

	t.Run("unknown canonical agent", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Agents.List = []AgentConfig{{ID: "main", Default: true}}
		cfg.PRLifecycle = workflowWithAgent("ghost")
		if err := cfg.PRLifecycle.Validate(); err != nil {
			t.Fatalf("shape validation unexpectedly failed: %v", err)
		}
		err := SaveConfig(filepath.Join(t.TempDir(), "config.json"), cfg)
		if err == nil || !strings.Contains(err.Error(), `unknown agent "ghost"`) {
			t.Fatalf("SaveConfig() error = %v, want unknown ghost agent", err)
		}
	})

	t.Run("configured exact agent", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		cfg := DefaultConfig()
		cfg.Agents.List = []AgentConfig{{ID: "main", Default: true}, {ID: "reviewer"}}
		cfg.PRLifecycle = workflowWithAgent("reviewer")
		if err := SaveConfig(path, cfg); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err != nil {
			t.Fatalf("LoadConfig() rejected saved exact agent reference: %v", err)
		}
	})

	t.Run("implicit main agent", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.PRLifecycle = workflowWithAgent("main")
		if err := cfg.PRLifecycle.ValidateAgentReferences(cfg.Agents); err != nil {
			t.Fatal(err)
		}
	})
}
