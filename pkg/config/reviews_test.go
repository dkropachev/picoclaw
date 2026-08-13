package config

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

func TestReviewAttentionConfigRoundTripPreservesPolicies(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Reviews.Attention = validReviewAttentionConfig()

	encoded, err := json.Marshal(cfg)
	require.NoError(t, err)
	loaded, err := loadConfig(encoded)
	require.NoError(t, err)
	require.NoError(t, loaded.Reviews.Validate())
	require.Equal(t, cfg.Reviews, loaded.Reviews)
	require.NotNil(t, loaded.Reviews.Attention.Global["review.empty"])
	require.NotNil(t, loaded.Reviews.Attention.Repositories["Empty/Policy"])
}

func TestReviewAttentionConfigRoundTripPreservesLargeQuestionInteger(t *testing.T) {
	const largeInteger = "9007199254740993"
	data := []byte(`{
  "version": 6,
  "reviews": {
    "attention": {
      "global": {
        "review.submitted": [{
          "id": "ask",
          "kind": "ai_isolated_context",
          "agent_id": "main",
          "criteria": "ask when direction is required",
          "title": "Discuss",
          "questions": {"issue_number": ` + largeInteger + `}
        }]
      }
    }
  }
}`)

	loaded, err := loadConfig(data)
	require.NoError(t, err)
	require.NoError(t, loaded.Reviews.Validate())
	questions := loaded.Reviews.Attention.Global["review.submitted"][0].Questions.(map[string]any)
	require.Equal(t, json.Number(largeInteger), questions["issue_number"])

	encoded, err := json.Marshal(loaded)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"issue_number":`+largeInteger)
	reloaded, err := loadConfig(encoded)
	require.NoError(t, err)
	reloadedQuestions := reloaded.Reviews.Attention.Global["review.submitted"][0].Questions.(map[string]any)
	require.Equal(t, json.Number(largeInteger), reloadedQuestions["issue_number"])
}

func TestReviewAttentionConfigValidation(t *testing.T) {
	zeroGates := func(count int) []gatetypes.GateSpec {
		gates := make([]gatetypes.GateSpec, count)
		for index := range gates {
			gates[index] = gatetypes.GateSpec{
				ID:   fmt.Sprintf("g%03d", index),
				Kind: gatetypes.GateZero,
			}
		}
		return gates
	}
	tests := []struct {
		name    string
		build   func() ReviewAttentionConfig
		wantErr string
	}{
		{
			name:  "valid",
			build: validReviewAttentionConfig,
		},
		{
			name: "invalid global decision point",
			build: func() ReviewAttentionConfig {
				return ReviewAttentionConfig{Global: map[string][]gatetypes.GateSpec{
					"Review Submitted": {},
				}}
			},
			wantErr: "decision point",
		},
		{
			name: "too many global decision points",
			build: func() ReviewAttentionConfig {
				global := make(map[string][]gatetypes.GateSpec, MaxReviewAttentionDecisionPoints+1)
				for index := 0; index <= MaxReviewAttentionDecisionPoints; index++ {
					global[fmt.Sprintf("decision.%03d", index)] = []gatetypes.GateSpec{}
				}
				return ReviewAttentionConfig{Global: global}
			},
			wantErr: fmt.Sprintf("at most %d decision points", MaxReviewAttentionDecisionPoints),
		},
		{
			name: "null global policy",
			build: func() ReviewAttentionConfig {
				return ReviewAttentionConfig{Global: map[string][]gatetypes.GateSpec{
					"review.submitted": nil,
				}}
			},
			wantErr: "array, not null",
		},
		{
			name: "invalid repository",
			build: func() ReviewAttentionConfig {
				return ReviewAttentionConfig{Repositories: map[string]map[string]gatetypes.RepositoryGatePolicy{
					"owner": {},
				}}
			},
			wantErr: "owner/repo",
		},
		{
			name: "case-insensitive repository collision",
			build: func() ReviewAttentionConfig {
				return ReviewAttentionConfig{Repositories: map[string]map[string]gatetypes.RepositoryGatePolicy{
					"Acme/Widget": {},
					"acme/widget": {},
				}}
			},
			wantErr: "differ only by case",
		},
		{
			name: "too many repositories",
			build: func() ReviewAttentionConfig {
				repositories := make(
					map[string]map[string]gatetypes.RepositoryGatePolicy,
					MaxReviewAttentionRepositories+1,
				)
				for index := 0; index <= MaxReviewAttentionRepositories; index++ {
					repositories[fmt.Sprintf("owner/repo-%04d", index)] = map[string]gatetypes.RepositoryGatePolicy{}
				}
				return ReviewAttentionConfig{Repositories: repositories}
			},
			wantErr: fmt.Sprintf("at most %d repositories", MaxReviewAttentionRepositories),
		},
		{
			name: "null repository policy map",
			build: func() ReviewAttentionConfig {
				return ReviewAttentionConfig{Repositories: map[string]map[string]gatetypes.RepositoryGatePolicy{
					"acme/widget": nil,
				}}
			},
			wantErr: "object, not null",
		},
		{
			name: "too many aggregate policies",
			build: func() ReviewAttentionConfig {
				policies := make(map[string]gatetypes.RepositoryGatePolicy, MaxReviewAttentionDecisionPoints)
				for index := 0; index < MaxReviewAttentionDecisionPoints; index++ {
					policies[fmt.Sprintf("decision.%03d", index)] = gatetypes.RepositoryGatePolicy{
						Mode: gatetypes.GatePolicyDisable,
					}
				}
				repositoryCount := MaxReviewAttentionPolicies/MaxReviewAttentionDecisionPoints + 1
				repositories := make(
					map[string]map[string]gatetypes.RepositoryGatePolicy,
					repositoryCount,
				)
				for index := 0; index < repositoryCount; index++ {
					repositories[fmt.Sprintf("owner/repo-%04d", index)] = policies
				}
				return ReviewAttentionConfig{Repositories: repositories}
			},
			wantErr: fmt.Sprintf("at most %d policies", MaxReviewAttentionPolicies),
		},
		{
			name: "too many gates in one policy",
			build: func() ReviewAttentionConfig {
				return ReviewAttentionConfig{Global: map[string][]gatetypes.GateSpec{
					"review.submitted": zeroGates(gatetypes.MaxWorkflowGateCount + 1),
				}}
			},
			wantErr: "at most 64 gates",
		},
		{
			name: "too many aggregate gates",
			build: func() ReviewAttentionConfig {
				gates := zeroGates(gatetypes.MaxWorkflowGateCount)
				repositoryCount := MaxReviewAttentionGateSpecs/gatetypes.MaxWorkflowGateCount + 1
				repositories := make(
					map[string]map[string]gatetypes.RepositoryGatePolicy,
					repositoryCount,
				)
				for index := 0; index < repositoryCount; index++ {
					repositories[fmt.Sprintf("owner/repo-%04d", index)] = map[string]gatetypes.RepositoryGatePolicy{
						"review.submitted": {Mode: gatetypes.GatePolicyReplace, Gates: gates},
					}
				}
				return ReviewAttentionConfig{Repositories: repositories}
			},
			wantErr: fmt.Sprintf("at most %d gates", MaxReviewAttentionGateSpecs),
		},
		{
			name: "overlay effective policy has a gate bound",
			build: func() ReviewAttentionConfig {
				return ReviewAttentionConfig{
					Global: map[string][]gatetypes.GateSpec{
						"review.submitted": zeroGates(gatetypes.MaxWorkflowGateCount),
					},
					Repositories: map[string]map[string]gatetypes.RepositoryGatePolicy{
						"acme/widget": {
							"review.submitted": {
								Mode: gatetypes.GatePolicyOverlay,
								Gates: []gatetypes.GateSpec{{
									ID: "new", Kind: gatetypes.GateZero,
								}},
							},
						},
					},
				}
			},
			wantErr: "effective policy exceeds 64 gates",
		},
		{
			name: "overlay effective policy requires one working agent",
			build: func() ReviewAttentionConfig {
				return ReviewAttentionConfig{
					Global: map[string][]gatetypes.GateSpec{
						"review.submitted": {
							validReviewAIGate("global", gatetypes.GateAIWorkingContext, "main"),
						},
					},
					Repositories: map[string]map[string]gatetypes.RepositoryGatePolicy{
						"acme/widget": {
							"review.submitted": {
								Mode: gatetypes.GatePolicyOverlay,
								Gates: []gatetypes.GateSpec{
									validReviewAIGate("local", gatetypes.GateAIWorkingContext, "other"),
								},
							},
						},
					},
				}
			},
			wantErr: "effective working-context gates must use one agent",
		},
		{
			name: "invalid repository mode",
			build: func() ReviewAttentionConfig {
				return ReviewAttentionConfig{Repositories: map[string]map[string]gatetypes.RepositoryGatePolicy{
					"acme/widget": {
						"review.submitted": {Mode: gatetypes.GatePolicyMode("sometimes")},
					},
				}}
			},
			wantErr: "unsupported mode",
		},
		{
			name: "overlay requires gates",
			build: func() ReviewAttentionConfig {
				return ReviewAttentionConfig{Repositories: map[string]map[string]gatetypes.RepositoryGatePolicy{
					"acme/widget": {
						"review.submitted": {Mode: gatetypes.GatePolicyOverlay},
					},
				}}
			},
			wantErr: "requires at least one gate",
		},
		{
			name: "inherit rejects gates",
			build: func() ReviewAttentionConfig {
				return ReviewAttentionConfig{Repositories: map[string]map[string]gatetypes.RepositoryGatePolicy{
					"acme/widget": {
						"review.submitted": {
							Mode:  gatetypes.GatePolicyInherit,
							Gates: zeroGates(1),
						},
					},
				}}
			},
			wantErr: "cannot configure gates",
		},
		{
			name: "duplicate gate IDs",
			build: func() ReviewAttentionConfig {
				gate := gatetypes.GateSpec{ID: "same", Kind: gatetypes.GateZero}
				return ReviewAttentionConfig{Global: map[string][]gatetypes.GateSpec{
					"review.submitted": {gate, gate},
				}}
			},
			wantErr: "duplicates gate ID",
		},
		{
			name: "working gates require one agent",
			build: func() ReviewAttentionConfig {
				return ReviewAttentionConfig{Global: map[string][]gatetypes.GateSpec{
					"review.submitted": {
						validReviewAIGate("first", gatetypes.GateAIWorkingContext, "main"),
						validReviewAIGate("second", gatetypes.GateAIWorkingContext, "other"),
					},
				}}
			},
			wantErr: "must use one agent",
		},
		{
			name: "malformed gate",
			build: func() ReviewAttentionConfig {
				gate := validReviewAIGate("ask", gatetypes.GateAIIsolatedContext, "Main Agent")
				return ReviewAttentionConfig{Global: map[string][]gatetypes.GateSpec{
					"review.submitted": {gate},
				}}
			},
			wantErr: "canonical agent ID",
		},
		{
			name: "deterministic gate requires questions",
			build: func() ReviewAttentionConfig {
				return ReviewAttentionConfig{Global: map[string][]gatetypes.GateSpec{
					"review.submitted": {{
						ID: "policy", Kind: gatetypes.GateDeterministic,
						When: "true", Title: "Confirm policy",
					}},
				}}
			},
			wantErr: "questions are required",
		},
		{
			name: "deterministic gate rejects typed JSON null questions",
			build: func() ReviewAttentionConfig {
				var questions *string
				return ReviewAttentionConfig{Global: map[string][]gatetypes.GateSpec{
					"review.submitted": {{
						ID: "policy", Kind: gatetypes.GateDeterministic,
						When: "true", Title: "Confirm policy", Questions: questions,
					}},
				}}
			},
			wantErr: "questions are required",
		},
		{
			name: "deterministic gate condition is validated",
			build: func() ReviewAttentionConfig {
				return ReviewAttentionConfig{Global: map[string][]gatetypes.GateSpec{
					"review.submitted": {{
						ID: "policy", Kind: gatetypes.GateDeterministic,
						When: "secrets.token", Title: "Confirm policy", Questions: []any{"Proceed?"},
					}},
				}}
			},
			wantErr: "expression root \"secrets\" is unsupported",
		},
		{
			name: "zero gate rejects behavior",
			build: func() ReviewAttentionConfig {
				return ReviewAttentionConfig{Global: map[string][]gatetypes.GateSpec{
					"review.submitted": {{
						ID: "off", Kind: gatetypes.GateZero, Title: "not allowed",
					}},
				}}
			},
			wantErr: "cannot configure behavior fields",
		},
		{
			name: "questions must be durable JSON",
			build: func() ReviewAttentionConfig {
				gate := validReviewAIGate("ask", gatetypes.GateAIIsolatedContext, "main")
				gate.Questions = math.Inf(1)
				return ReviewAttentionConfig{Global: map[string][]gatetypes.GateSpec{
					"review.submitted": {gate},
				}}
			},
			wantErr: "durable JSON",
		},
		{
			name: "questions must be acyclic",
			build: func() ReviewAttentionConfig {
				cycle := make([]any, 1)
				cycle[0] = cycle
				gate := validReviewAIGate("ask", gatetypes.GateAIIsolatedContext, "main")
				gate.Questions = cycle
				return ReviewAttentionConfig{Global: map[string][]gatetypes.GateSpec{
					"review.submitted": {gate},
				}}
			},
			wantErr: "acyclic JSON",
		},
		{
			name: "questions have bounded depth",
			build: func() ReviewAttentionConfig {
				var questions any = "leaf"
				for range gatetypes.MaxWorkflowGateJSONDepth + 1 {
					questions = []any{questions}
				}
				gate := validReviewAIGate("ask", gatetypes.GateAIIsolatedContext, "main")
				gate.Questions = questions
				return ReviewAttentionConfig{Global: map[string][]gatetypes.GateSpec{
					"review.submitted": {gate},
				}}
			},
			wantErr: "exceeds JSON depth 64",
		},
		{
			name: "questions have bounded node count",
			build: func() ReviewAttentionConfig {
				gate := validReviewAIGate("ask", gatetypes.GateAIIsolatedContext, "main")
				gate.Questions = make([]any, gatetypes.MaxWorkflowGateJSONNodes)
				return ReviewAttentionConfig{Global: map[string][]gatetypes.GateSpec{
					"review.submitted": {gate},
				}}
			},
			wantErr: "exceeds 100000 JSON nodes",
		},
		{
			name: "questions are size bounded",
			build: func() ReviewAttentionConfig {
				gate := validReviewAIGate("ask", gatetypes.GateAIIsolatedContext, "main")
				gate.Questions = strings.Repeat("x", gatetypes.MaxWorkflowGateQuestionBytes)
				return ReviewAttentionConfig{Global: map[string][]gatetypes.GateSpec{
					"review.submitted": {gate},
				}}
			},
			wantErr: "questions exceeds 131072 encoded bytes",
		},
		{
			name: "complete policy is encoded-size bounded",
			build: func() ReviewAttentionConfig {
				gate := validReviewAIGate("ask", gatetypes.GateAIIsolatedContext, "main")
				gate.Questions = strings.Repeat("x", gatetypes.MaxWorkflowGateQuestionBytes-2)
				entryCount := MaxReviewAttentionConfigBytes/gatetypes.MaxWorkflowGateQuestionBytes + 1
				global := make(map[string][]gatetypes.GateSpec, entryCount)
				for index := 0; index < entryCount; index++ {
					global[fmt.Sprintf("decision.%03d", index)] = []gatetypes.GateSpec{gate}
				}
				return ReviewAttentionConfig{Global: global}
			},
			wantErr: fmt.Sprintf("exceeds %d encoded bytes", MaxReviewAttentionConfigBytes),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.build().Validate()
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestSaveConfigRejectsInvalidReviewAttentionPolicy(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Reviews.Attention.Global["Review Submitted"] = []gatetypes.GateSpec{}
	path := filepath.Join(t.TempDir(), "config.json")

	err := SaveConfig(path, cfg)
	require.ErrorContains(t, err, "invalid reviews config")
	_, statErr := os.Stat(path)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestReviewAttentionConfigUsesCanonicalCatalogSizeBoundary(t *testing.T) {
	repositories := make(
		map[string]map[string]gatetypes.RepositoryGatePolicy,
		64,
	)
	for repositoryIndex := 0; repositoryIndex < 64; repositoryIndex++ {
		policies := make(map[string]gatetypes.RepositoryGatePolicy, 128)
		for decisionIndex := 0; decisionIndex < 128; decisionIndex++ {
			decisionPoint := fmt.Sprintf(
				"review.%03d.%s",
				decisionIndex,
				strings.Repeat("x", 70),
			)
			policies[decisionPoint] = gatetypes.RepositoryGatePolicy{
				Mode: gatetypes.GatePolicyDisable,
			}
		}
		repositories[fmt.Sprintf("owner%02d/repository", repositoryIndex)] = policies
	}
	attention := ReviewAttentionConfig{Repositories: repositories}
	mapEncoded, err := json.Marshal(attention)
	require.NoError(t, err)
	canonical, err := gatetypes.MarshalCanonicalGatePolicyCatalog(
		attention.Global,
		attention.Repositories,
	)
	require.NoError(t, err)
	require.Less(t, len(mapEncoded), MaxReviewAttentionConfigBytes)
	require.Greater(t, len(canonical), MaxReviewAttentionConfigBytes)
	require.ErrorContains(
		t,
		attention.Validate(),
		fmt.Sprintf("exceeds %d encoded bytes", MaxReviewAttentionConfigBytes),
	)
}

func TestMigrateV5ToV6PreservesReviewAttentionPolicy(t *testing.T) {
	reviews := map[string]any{
		"attention": map[string]any{
			"global": map[string]any{
				"review.empty": []any{},
			},
			"repositories": map[string]any{
				"Empty/Policy": map[string]any{},
			},
		},
	}
	m := map[string]any{"version": 5, "reviews": reviews}

	require.NoError(t, migrateV5ToV6(m))
	require.Equal(t, 6, m["version"])
	require.Equal(t, reviews, m["reviews"])
	require.Equal(t, []any{}, reviews["attention"].(map[string]any)["global"].(map[string]any)["review.empty"])
	require.Equal(
		t,
		map[string]any{},
		reviews["attention"].(map[string]any)["repositories"].(map[string]any)["Empty/Policy"],
	)
	require.ErrorContains(t, migrateV5ToV6(m), "expected version 5")
}

func TestLoadConfigMigratesV5ReviewAttentionPolicy(t *testing.T) {
	const largeInteger = "9007199254740993"
	mustSetupSSHKey(t)
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{
  "version": 5,
  "reviews": {
    "attention": {
	  "global": {
	    "review.empty": [],
	    "review.submitted": [{
	      "id": "ask",
	      "kind": "ai_isolated_context",
	      "agent_id": "main",
	      "criteria": "ask when direction is required",
	      "title": "Discuss",
	      "questions": {"issue_number": ` + largeInteger + `}
	    }]
	  },
      "repositories": {"Empty/Policy": {}}
    }
  }
}`)
	require.NoError(t, os.WriteFile(path, data, 0o600))

	cfg, err := LoadConfig(path)
	require.NoError(t, err)
	require.Equal(t, CurrentVersion, cfg.Version)
	require.NotNil(t, cfg.Reviews.Attention.Global["review.empty"])
	require.NotNil(t, cfg.Reviews.Attention.Repositories["Empty/Policy"])
	questions := cfg.Reviews.Attention.Global["review.submitted"][0].Questions.(map[string]any)
	require.Equal(t, json.Number(largeInteger), questions["issue_number"])

	persisted, err := os.ReadFile(path)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(persisted, &raw))
	require.Equal(t, float64(CurrentVersion), raw["version"])
	require.Contains(t, string(persisted), `"issue_number": `+largeInteger)
	attention := raw["reviews"].(map[string]any)["attention"].(map[string]any)
	require.Equal(t, []any{}, attention["global"].(map[string]any)["review.empty"])
	require.Equal(t, map[string]any{}, attention["repositories"].(map[string]any)["Empty/Policy"])
}

func validReviewAttentionConfig() ReviewAttentionConfig {
	return ReviewAttentionConfig{
		Global: map[string][]gatetypes.GateSpec{
			"review.empty": {},
			"review.submitted": {
				validReviewAIGate("discuss", gatetypes.GateAIWorkingContext, "main"),
				validReviewAIGate("inspect", gatetypes.GateAIIsolatedContext, "reviewer"),
				{
					ID: "policy", Kind: gatetypes.GateDeterministic,
					When: "true", Title: "Confirm policy", Questions: []any{"Proceed?"},
				},
				{ID: "off", Kind: gatetypes.GateZero},
			},
		},
		Repositories: map[string]map[string]gatetypes.RepositoryGatePolicy{
			"Acme/Widget": {
				"review.submitted": {
					Mode: gatetypes.GatePolicyOverlay,
					Gates: []gatetypes.GateSpec{
						{ID: "inspect", Kind: gatetypes.GateZero},
						{
							ID: "release", Kind: gatetypes.GateDeterministic,
							When: "false", Title: "Confirm release", Questions: map[string]any{"prompt": "Ship?"},
						},
					},
				},
				"review.ready": {Mode: gatetypes.GatePolicyDisable},
			},
			"Empty/Policy": {},
		},
	}
}

func validReviewAIGate(id string, kind gatetypes.GateKind, agentID string) gatetypes.GateSpec {
	return gatetypes.GateSpec{
		ID: id, Kind: kind, AgentID: agentID,
		Criteria: "ask when user direction is required",
		Title:    "Discuss the finding",
		Questions: map[string]any{
			"prompt": "What outcome do you want?",
		},
	}
}

func TestReviewAttentionNamedRuleSetsValidateAndProjectEmptyDefault(t *testing.T) {
	normalized, err := (ReviewAttentionConfig{}).NamedRuleSets()
	require.NoError(t, err)
	require.Equal(t, DefaultReviewAttentionRuleSetID, normalized.DefaultRuleSetID)
	require.Equal(t, DefaultReviewAttentionRuleSetName, normalized.RuleSets["default"].Name)
	require.Empty(t, normalized.RuleSets["default"].Rules)
	require.NotNil(t, normalized.RepositoryAssignments)
	require.NoError(t, normalized.Validate())

	configured := ReviewAttentionConfig{
		RuleSets: map[string]ReviewAttentionRuleSet{
			"default": {Name: "Default", Rules: map[string][]gatetypes.GateSpec{}},
			"strict": {
				Name: "Strict review",
				Rules: map[string][]gatetypes.GateSpec{
					"review.ready": {{ID: "off", Kind: gatetypes.GateZero}},
				},
			},
		},
		DefaultRuleSetID: "strict",
		RepositoryAssignments: map[string]string{
			"Acme/Default": "default",
			"Acme/Strict":  "strict",
		},
	}
	require.NoError(t, configured.Validate())
	unicodeNames, err := cloneNamedReviewAttentionConfig(configured)
	require.NoError(t, err)
	strictSet := unicodeNames.RuleSets["strict"]
	strictSet.Name = "Σ"
	unicodeNames.RuleSets["strict"] = strictSet
	unicodeNames.RuleSets["unicode"] = ReviewAttentionRuleSet{
		Name: "ς", Rules: map[string][]gatetypes.GateSpec{},
	}
	require.NoError(t, unicodeNames.Validate(), "match JavaScript toLowerCase rather than EqualFold")
	unicodeSet := unicodeNames.RuleSets["unicode"]
	unicodeSet.Name = "σ"
	unicodeNames.RuleSets["unicode"] = unicodeSet
	require.ErrorContains(t, unicodeNames.Validate(), "differ only by case")
	strictSet.Name = "İ"
	unicodeNames.RuleSets["strict"] = strictSet
	unicodeSet.Name = "i"
	unicodeNames.RuleSets["unicode"] = unicodeSet
	require.ErrorContains(t, unicodeNames.Validate(), "differ only by case")

	tests := []struct {
		name   string
		mutate func(*ReviewAttentionConfig)
		want   string
	}{
		{
			name: "mixed legacy representation",
			mutate: func(candidate *ReviewAttentionConfig) {
				candidate.Global = map[string][]gatetypes.GateSpec{}
			},
			want: "cannot mix",
		},
		{
			name: "built-in removed",
			mutate: func(candidate *ReviewAttentionConfig) {
				delete(candidate.RuleSets, "default")
			},
			want: `rule_sets["default"]`,
		},
		{
			name: "built-in renamed",
			mutate: func(candidate *ReviewAttentionConfig) {
				set := candidate.RuleSets["default"]
				set.Name = "Renamed"
				candidate.RuleSets["default"] = set
			},
			want: `must have name "Default"`,
		},
		{
			name: "unknown selected default",
			mutate: func(candidate *ReviewAttentionConfig) {
				candidate.DefaultRuleSetID = "missing"
			},
			want: "references unknown rule set",
		},
		{
			name: "duplicate names",
			mutate: func(candidate *ReviewAttentionConfig) {
				set := candidate.RuleSets["strict"]
				set.Name = "default"
				candidate.RuleSets["strict"] = set
			},
			want: "differ only by case",
		},
		{
			name: "invalid id",
			mutate: func(candidate *ReviewAttentionConfig) {
				candidate.RuleSets["Bad ID"] = ReviewAttentionRuleSet{
					Name: "Bad", Rules: map[string][]gatetypes.GateSpec{},
				}
			},
			want: "rule-set ID",
		},
		{
			name: "nil rules",
			mutate: func(candidate *ReviewAttentionConfig) {
				set := candidate.RuleSets["strict"]
				set.Rules = nil
				candidate.RuleSets["strict"] = set
			},
			want: "must be an object, not null",
		},
		{
			name: "unknown assignment",
			mutate: func(candidate *ReviewAttentionConfig) {
				candidate.RepositoryAssignments["Acme/Strict"] = "missing"
			},
			want: "references unknown rule set",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate, cloneErr := cloneNamedReviewAttentionConfig(configured)
			require.NoError(t, cloneErr)
			test.mutate(&candidate)
			require.ErrorContains(t, candidate.Validate(), test.want)
		})
	}
}

func TestReviewAttentionLegacyCatalogProjectsEffectiveRepositoryRuleSets(t *testing.T) {
	legacy := ReviewAttentionConfig{
		Global: map[string][]gatetypes.GateSpec{
			"review.ready": {
				{ID: "keep", Kind: gatetypes.GateZero},
				{ID: "replace", Kind: gatetypes.GateZero},
			},
			"review.empty": {},
		},
		Repositories: map[string]map[string]gatetypes.RepositoryGatePolicy{
			"Acme/Inherit": {
				"review.ready": {Mode: gatetypes.GatePolicyInherit},
			},
			"Acme/Overlay": {
				"review.ready": {
					Mode: gatetypes.GatePolicyOverlay,
					Gates: []gatetypes.GateSpec{
						{ID: "replace", Kind: gatetypes.GateZero},
						{ID: "append", Kind: gatetypes.GateZero},
					},
				},
			},
			"Acme/Disable": {
				"review.ready": {Mode: gatetypes.GatePolicyDisable},
			},
			"Acme/Replace": {
				"review.ready": {
					Mode:  gatetypes.GatePolicyReplace,
					Gates: []gatetypes.GateSpec{{ID: "only", Kind: gatetypes.GateZero}},
				},
			},
		},
	}
	normalized, err := legacy.NamedRuleSets()
	require.NoError(t, err)
	require.NoError(t, normalized.Validate())
	require.NotContains(t, normalized.RepositoryAssignments, "Acme/Inherit")
	require.Len(t, normalized.RepositoryAssignments, 3)

	overlay := normalized.RuleSets[normalized.RepositoryAssignments["Acme/Overlay"]]
	require.Equal(t, []string{"keep", "replace", "append"}, []string{
		overlay.Rules["review.ready"][0].ID,
		overlay.Rules["review.ready"][1].ID,
		overlay.Rules["review.ready"][2].ID,
	})
	require.NotNil(t, overlay.Rules["review.empty"])

	disabled := normalized.RuleSets[normalized.RepositoryAssignments["Acme/Disable"]]
	require.Contains(t, disabled.Rules, "review.ready")
	require.Empty(t, disabled.Rules["review.ready"])
	require.NotEqual(
		t,
		normalized.RepositoryAssignments["Acme/Disable"],
		normalized.DefaultRuleSetID,
	)

	replaced := normalized.RuleSets[normalized.RepositoryAssignments["Acme/Replace"]]
	require.Equal(t, "only", replaced.Rules["review.ready"][0].ID)
}

func TestReviewAttentionLegacyCatalogSharesIdenticalEffectiveRuleSetsAtRepositoryLimit(
	t *testing.T,
) {
	globalGates := make([]gatetypes.GateSpec, 9)
	for index := range globalGates {
		globalGates[index] = gatetypes.GateSpec{
			ID:   fmt.Sprintf("gate_%d", index),
			Kind: gatetypes.GateZero,
		}
	}
	legacy := ReviewAttentionConfig{
		Global: map[string][]gatetypes.GateSpec{
			"review.ready": globalGates,
		},
		Repositories: make(
			map[string]map[string]gatetypes.RepositoryGatePolicy,
			MaxReviewAttentionRepositories,
		),
	}
	for index := range MaxReviewAttentionRepositories {
		legacy.Repositories[fmt.Sprintf("owner/repository-%d", index)] = map[string]gatetypes.RepositoryGatePolicy{
			"review.ready": {
				Mode: gatetypes.GatePolicyOverlay,
				Gates: []gatetypes.GateSpec{
					{ID: "gate_0", Kind: gatetypes.GateZero},
				},
			},
		}
	}

	normalized, err := legacy.NamedRuleSets()
	require.NoError(t, err)
	require.NoError(t, normalized.Validate())
	require.Len(t, normalized.RuleSets, 2)
	require.Len(t, normalized.RepositoryAssignments, MaxReviewAttentionRepositories)

	var importedID string
	for _, id := range normalized.RepositoryAssignments {
		if importedID == "" {
			importedID = id
		}
		require.Equal(t, importedID, id)
	}
	require.NotEqual(t, DefaultReviewAttentionRuleSetID, importedID)
	require.Equal(t, globalGates, normalized.RuleSets[importedID].Rules["review.ready"])
}

func TestReviewAttentionLegacyCatalogReportsOnlyUnrepresentableNamedMigration(
	t *testing.T,
) {
	legacy := ReviewAttentionConfig{
		Global: map[string][]gatetypes.GateSpec{
			"review.ready": make([]gatetypes.GateSpec, 9),
		},
		Repositories: make(
			map[string]map[string]gatetypes.RepositoryGatePolicy,
			MaxReviewAttentionRepositories,
		),
	}
	for index := range legacy.Global["review.ready"] {
		legacy.Global["review.ready"][index] = gatetypes.GateSpec{
			ID:   fmt.Sprintf("global_%d", index),
			Kind: gatetypes.GateZero,
		}
	}
	for index := range MaxReviewAttentionRepositories {
		legacy.Repositories[fmt.Sprintf("owner/repository-%d", index)] = map[string]gatetypes.RepositoryGatePolicy{
			"review.ready": {
				Mode: gatetypes.GatePolicyOverlay,
				Gates: []gatetypes.GateSpec{
					{ID: fmt.Sprintf("repository_%d", index), Kind: gatetypes.GateZero},
				},
			},
		}
	}

	require.NoError(t, legacy.Validate())
	_, err := legacy.NamedRuleSets()
	require.ErrorIs(t, err, ErrReviewAttentionLegacyMigrationExceedsBounds)
}
