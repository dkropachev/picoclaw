package agent

import (
	"reflect"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestMeetTurnProfilesDisabledIdentityAndDetached(t *testing.T) {
	restriction := config.EffectiveTurnProfile{
		Enabled:          true,
		HistoryMode:      config.TurnProfileModeDefault,
		SystemPromptMode: config.TurnProfileModeDefault,
		SkillsMode:       config.TurnProfileModeCustom,
		ToolsMode:        config.TurnProfileModeCustom,
		AllowedSkills:    []string{" Beta ", "alpha", "ALPHA"},
		AllowedTools:     []string{"WRITE_FILE", " read_file "},
	}

	got, err := meetTurnProfiles(config.EffectiveTurnProfile{}, restriction)
	if err != nil {
		t.Fatalf("meetTurnProfiles() error = %v", err)
	}
	want := config.EffectiveTurnProfile{
		Enabled:          true,
		HistoryMode:      config.TurnProfileModeDefault,
		SystemPromptMode: config.TurnProfileModeDefault,
		SkillsMode:       config.TurnProfileModeCustom,
		ToolsMode:        config.TurnProfileModeCustom,
		AllowedSkills:    []string{"alpha", "beta"},
		AllowedTools:     []string{"read_file", "write_file"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("meetTurnProfiles() = %#v, want %#v", got, want)
	}

	got.AllowedTools[0] = "mutated"
	if restriction.AllowedTools[0] != "WRITE_FILE" {
		t.Fatalf("meet output aliases input: %#v", restriction.AllowedTools)
	}
	identity, err := meetTurnProfiles(config.EffectiveTurnProfile{}, config.EffectiveTurnProfile{})
	if err != nil || identity.Enabled || identity.AllowedSkills != nil || identity.AllowedTools != nil {
		t.Fatalf("disabled identity meet = %#v, error = %v", identity, err)
	}
}

func TestMeetTurnProfilesDefaultOffLattice(t *testing.T) {
	tests := []struct {
		name  string
		left  config.TurnProfileMode
		right config.TurnProfileMode
		want  config.TurnProfileMode
	}{
		{
			name:  "default default",
			left:  config.TurnProfileModeDefault,
			right: config.TurnProfileModeDefault,
			want:  config.TurnProfileModeDefault,
		},
		{
			name:  "default off",
			left:  config.TurnProfileModeDefault,
			right: config.TurnProfileModeOff,
			want:  config.TurnProfileModeOff,
		},
		{
			name:  "off default",
			left:  config.TurnProfileModeOff,
			right: config.TurnProfileModeDefault,
			want:  config.TurnProfileModeOff,
		},
		{
			name:  "off off",
			left:  config.TurnProfileModeOff,
			right: config.TurnProfileModeOff,
			want:  config.TurnProfileModeOff,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := meetTurnProfiles(
				config.EffectiveTurnProfile{
					Enabled: true, HistoryMode: tt.left, SystemPromptMode: tt.right,
				},
				config.EffectiveTurnProfile{
					Enabled: true, HistoryMode: tt.right, SystemPromptMode: tt.left,
				},
			)
			if err != nil {
				t.Fatalf("meetTurnProfiles() error = %v", err)
			}
			if got.HistoryMode != tt.want || got.SystemPromptMode != tt.want {
				t.Fatalf("meet modes = (%q, %q), want %q", got.HistoryMode, got.SystemPromptMode, tt.want)
			}
		})
	}
}

func TestMeetTurnProfilesCapabilityLattice(t *testing.T) {
	tests := []struct {
		name         string
		leftMode     config.TurnProfileMode
		leftAllowed  []string
		rightMode    config.TurnProfileMode
		rightAllowed []string
		wantMode     config.TurnProfileMode
		wantAllowed  []string
	}{
		{
			name:         "default default",
			leftMode:     config.TurnProfileModeDefault,
			leftAllowed:  []string{"stale-left"},
			rightMode:    config.TurnProfileModeDefault,
			rightAllowed: []string{"stale-right"},
			wantMode:     config.TurnProfileModeDefault,
		},
		{
			name:         "default custom",
			leftMode:     config.TurnProfileModeDefault,
			rightMode:    config.TurnProfileModeCustom,
			rightAllowed: []string{"B", "a", "a"},
			wantMode:     config.TurnProfileModeCustom,
			wantAllowed:  []string{"a", "b"},
		},
		{
			name:        "custom default",
			leftMode:    config.TurnProfileModeCustom,
			leftAllowed: []string{"b", "a"},
			rightMode:   config.TurnProfileModeDefault,
			wantMode:    config.TurnProfileModeCustom,
			wantAllowed: []string{"a", "b"},
		},
		{
			name:         "custom intersection",
			leftMode:     config.TurnProfileModeCustom,
			leftAllowed:  []string{"a", "b"},
			rightMode:    config.TurnProfileModeCustom,
			rightAllowed: []string{"B", "c"},
			wantMode:     config.TurnProfileModeCustom,
			wantAllowed:  []string{"b"},
		},
		{
			name:         "empty intersection",
			leftMode:     config.TurnProfileModeCustom,
			leftAllowed:  []string{"a"},
			rightMode:    config.TurnProfileModeCustom,
			rightAllowed: []string{"b"},
			wantMode:     config.TurnProfileModeOff,
		},
		{
			name:      "explicit empty custom",
			leftMode:  config.TurnProfileModeDefault,
			rightMode: config.TurnProfileModeCustom,
			wantMode:  config.TurnProfileModeOff,
		},
		{
			name:         "default off",
			leftMode:     config.TurnProfileModeDefault,
			leftAllowed:  []string{"stale-left"},
			rightMode:    config.TurnProfileModeOff,
			rightAllowed: []string{"stale-right"},
			wantMode:     config.TurnProfileModeOff,
		},
		{
			name:         "off dominates",
			leftMode:     config.TurnProfileModeCustom,
			leftAllowed:  []string{"a"},
			rightMode:    config.TurnProfileModeOff,
			rightAllowed: []string{"a"},
			wantMode:     config.TurnProfileModeOff,
		},
		{
			name:         "off off",
			leftMode:     config.TurnProfileModeOff,
			leftAllowed:  []string{"stale-left"},
			rightMode:    config.TurnProfileModeOff,
			rightAllowed: []string{"stale-right"},
			wantMode:     config.TurnProfileModeOff,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := config.EffectiveTurnProfile{
				Enabled: true, SkillsMode: tt.leftMode, ToolsMode: tt.leftMode,
				AllowedSkills: tt.leftAllowed, AllowedTools: tt.leftAllowed,
			}
			right := config.EffectiveTurnProfile{
				Enabled: true, SkillsMode: tt.rightMode, ToolsMode: tt.rightMode,
				AllowedSkills: tt.rightAllowed, AllowedTools: tt.rightAllowed,
			}
			got, err := meetTurnProfiles(left, right)
			if err != nil {
				t.Fatalf("meetTurnProfiles() error = %v", err)
			}
			if got.SkillsMode != tt.wantMode || got.ToolsMode != tt.wantMode ||
				!reflect.DeepEqual(got.AllowedSkills, tt.wantAllowed) ||
				!reflect.DeepEqual(got.AllowedTools, tt.wantAllowed) {
				t.Fatalf("meet capabilities = %#v, want mode %q allowed %#v", got, tt.wantMode, tt.wantAllowed)
			}

			reversed, err := meetTurnProfiles(right, left)
			if err != nil {
				t.Fatalf("reversed meet error = %v", err)
			}
			if !reflect.DeepEqual(reversed, got) {
				t.Fatalf("meet is not commutative: forward %#v, reverse %#v", got, reversed)
			}
		})
	}
}

func TestMeetTurnProfilesRejectsUnknownEnabledModes(t *testing.T) {
	tests := []config.EffectiveTurnProfile{
		{Enabled: true, HistoryMode: "sometimes"},
		{Enabled: true, HistoryMode: config.TurnProfileModeCustom},
		{Enabled: true, SystemPromptMode: "sometimes"},
		{Enabled: true, SystemPromptMode: config.TurnProfileModeCustom},
		{Enabled: true, SkillsMode: "sometimes"},
		{Enabled: true, ToolsMode: "sometimes"},
	}
	for _, profile := range tests {
		if _, err := meetTurnProfiles(profile, config.EffectiveTurnProfile{}); err == nil {
			t.Fatalf("meetTurnProfiles(%#v) error = nil", profile)
		}
	}

	// Dormant fields on a disabled profile carry no authority and remain a no-op.
	if got, err := meetTurnProfiles(
		config.EffectiveTurnProfile{Enabled: false, ToolsMode: "sometimes"},
		config.EffectiveTurnProfile{},
	); err != nil || got.Enabled {
		t.Fatalf("disabled malformed profile meet = %#v, error = %v", got, err)
	}
	if _, err := meetTurnProfiles(
		config.EffectiveTurnProfile{Enabled: true},
		config.EffectiveTurnProfile{Enabled: true, ToolsMode: "malformed-right"},
	); err == nil {
		t.Fatal("malformed right profile error = nil")
	}
}

func TestMeetTurnProfilesAssociative(t *testing.T) {
	profiles := []config.EffectiveTurnProfile{
		{
			Enabled: true, HistoryMode: config.TurnProfileModeDefault,
			SkillsMode: config.TurnProfileModeCustom, ToolsMode: config.TurnProfileModeCustom,
			AllowedSkills: []string{"alpha", "beta", "gamma"},
			AllowedTools:  []string{"read_file", "write_file", "web_search"},
		},
		{
			Enabled: true, HistoryMode: config.TurnProfileModeOff,
			SkillsMode: config.TurnProfileModeCustom, ToolsMode: config.TurnProfileModeCustom,
			AllowedSkills: []string{"BETA", "gamma"},
			AllowedTools:  []string{"READ_FILE", "web_search"},
		},
		{
			Enabled:    true,
			SkillsMode: config.TurnProfileModeCustom, ToolsMode: config.TurnProfileModeCustom,
			AllowedSkills: []string{"beta", "delta"},
			AllowedTools:  []string{"read_file", "exec"},
		},
	}

	leftPair, err := meetTurnProfiles(profiles[0], profiles[1])
	if err != nil {
		t.Fatal(err)
	}
	leftGrouped, err := meetTurnProfiles(leftPair, profiles[2])
	if err != nil {
		t.Fatal(err)
	}
	rightPair, err := meetTurnProfiles(profiles[1], profiles[2])
	if err != nil {
		t.Fatal(err)
	}
	rightGrouped, err := meetTurnProfiles(profiles[0], rightPair)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(leftGrouped, rightGrouped) {
		t.Fatalf("meet is not associative: left %#v, right %#v", leftGrouped, rightGrouped)
	}
}

func TestMeetTurnProfilesIdempotent(t *testing.T) {
	profiles := []config.EffectiveTurnProfile{
		{},
		{
			Enabled:          true,
			HistoryMode:      config.TurnProfileModeDefault,
			SystemPromptMode: config.TurnProfileModeDefault,
			SkillsMode:       config.TurnProfileModeDefault,
			ToolsMode:        config.TurnProfileModeDefault,
		},
		{
			Enabled:       true,
			SkillsMode:    config.TurnProfileModeCustom,
			ToolsMode:     config.TurnProfileModeCustom,
			AllowedSkills: []string{" Beta ", "alpha", "ALPHA"},
			AllowedTools:  []string{"WRITE_FILE", "read_file"},
		},
		{
			Enabled:          true,
			HistoryMode:      config.TurnProfileModeOff,
			SystemPromptMode: config.TurnProfileModeOff,
			SkillsMode:       config.TurnProfileModeOff,
			ToolsMode:        config.TurnProfileModeOff,
		},
	}
	for _, profile := range profiles {
		want, err := canonicalTurnProfile(profile)
		if err != nil {
			t.Fatal(err)
		}
		got, err := meetTurnProfiles(profile, profile)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("meet(%#v, self) = %#v, want canonical %#v", profile, got, want)
		}
	}
}

func TestResolveTurnProfileOptionsMeetsGlobalAndIncomingIdempotently(t *testing.T) {
	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		TurnProfile: config.TurnProfileConfig{
			Enabled: true,
			History: config.TurnProfileBlock{Mode: config.TurnProfileModeDefault},
			Tools: config.TurnProfileBlock{
				Mode:  config.TurnProfileModeCustom,
				Allow: []string{"read_file", "write_file"},
			},
		},
	}}}
	opts := processOptions{
		EnableSummary:          true,
		NoHistory:              true,
		DisableTools:           true,
		DisablePromptCache:     true,
		SuppressDefaultContext: true,
		TurnProfile: config.EffectiveTurnProfile{
			Enabled:     true,
			HistoryMode: config.TurnProfileModeOff,
			ToolsMode:   config.TurnProfileModeCustom,
			AllowedTools: []string{
				"read_file",
			},
		},
	}

	first, err := resolveTurnProfileOptions(cfg, opts)
	if err != nil {
		t.Fatalf("resolveTurnProfileOptions() error = %v", err)
	}
	second, err := resolveTurnProfileOptions(cfg, first)
	if err != nil {
		t.Fatalf("second resolveTurnProfileOptions() error = %v", err)
	}
	if !reflect.DeepEqual(second.TurnProfile, first.TurnProfile) ||
		second.NoHistory != first.NoHistory ||
		second.EnableSummary != first.EnableSummary ||
		second.DisableTools != first.DisableTools ||
		second.DisablePromptCache != first.DisablePromptCache ||
		second.SuppressDefaultContext != first.SuppressDefaultContext {
		t.Fatalf("resolution is not idempotent: first %#v, second %#v", first, second)
	}
	if !first.NoHistory || first.EnableSummary {
		t.Fatalf("history hard caps not preserved: %#v", first)
	}
	if !first.DisablePromptCache || !first.SuppressDefaultContext {
		t.Fatalf("non-profile hard caps not preserved: %#v", first)
	}
	if first.TurnProfile.HistoryMode != config.TurnProfileModeOff ||
		first.TurnProfile.ToolsMode != config.TurnProfileModeOff ||
		first.TurnProfile.AllowedTools != nil {
		t.Fatalf("effective profile = %#v, want history/tools off", first.TurnProfile)
	}
}

func TestResolveTurnProfileOptionsDisableToolsOnDisabledProfileIsIdempotent(t *testing.T) {
	opts := processOptions{DisableTools: true}
	first, err := resolveTurnProfileOptions(nil, opts)
	if err != nil {
		t.Fatalf("resolveTurnProfileOptions() error = %v", err)
	}
	second, err := resolveTurnProfileOptions(nil, first)
	if err != nil {
		t.Fatalf("second resolveTurnProfileOptions() error = %v", err)
	}
	if !reflect.DeepEqual(first.TurnProfile, second.TurnProfile) ||
		first.NoHistory != second.NoHistory ||
		first.EnableSummary != second.EnableSummary ||
		first.DisableTools != second.DisableTools {
		t.Fatalf("resolution is not idempotent: first %#v, second %#v", first, second)
	}
	want := config.EffectiveTurnProfile{
		Enabled:          true,
		HistoryMode:      config.TurnProfileModeDefault,
		SystemPromptMode: config.TurnProfileModeDefault,
		SkillsMode:       config.TurnProfileModeDefault,
		ToolsMode:        config.TurnProfileModeOff,
	}
	if !reflect.DeepEqual(first.TurnProfile, want) {
		t.Fatalf("hard-cap profile = %#v, want %#v", first.TurnProfile, want)
	}
}

func TestResolveTurnProfileOptionsRejectsInvalidGlobalProfile(t *testing.T) {
	cfg := &config.Config{Agents: config.AgentsConfig{Defaults: config.AgentDefaults{
		TurnProfile: config.TurnProfileConfig{
			Enabled: true,
			History: config.TurnProfileBlock{Mode: config.TurnProfileModeCustom},
		},
	}}}
	if _, err := resolveTurnProfileOptions(cfg, processOptions{}); err == nil {
		t.Fatal("invalid global profile error = nil")
	}
}

func TestTurnProfilePolicyBoundarySemantics(t *testing.T) {
	definitions := []providers.ToolDefinition{{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name: "read_file",
		},
	}}
	if !turnProfileHasCallableTools(config.EffectiveTurnProfile{}, nil) {
		t.Fatal("disabled profile should not suppress callable-tool fallback")
	}
	if turnProfileHasCallableTools(config.EffectiveTurnProfile{
		Enabled: true, ToolsMode: config.TurnProfileModeCustom,
	}, definitions) {
		t.Fatal("custom-empty profile should expose no callable tools")
	}

	if !turnProfileToolAllowed(config.EffectiveTurnProfile{}, "anything") {
		t.Fatal("disabled profile should allow ordinary tool policy")
	}
	if turnProfileToolAllowed(config.EffectiveTurnProfile{
		Enabled: true, ToolsMode: config.TurnProfileModeCustom,
	}, "read_file") {
		t.Fatal("custom-empty profile should allow no tool")
	}
	if !turnProfileToolAllowed(config.EffectiveTurnProfile{
		Enabled: true, ToolsMode: config.TurnProfileModeDefault,
	}, "read_file") {
		t.Fatal("default profile should retain registered tools")
	}

	if got := filterNamesByTurnProfile(nil, []string{"shell"}); got != nil {
		t.Fatalf("empty names filtered to %#v, want nil", got)
	}
	if got := filterNamesByTurnProfile([]string{"shell"}, nil); got != nil {
		t.Fatalf("empty allowlist filtered to %#v, want nil", got)
	}
	if got := filterToolsByTurnProfile(definitions, config.EffectiveTurnProfile{
		Enabled: true, ToolsMode: config.TurnProfileModeCustom,
	}); got != nil {
		t.Fatalf("custom-empty tools filtered to %#v, want nil", got)
	}
	if got := cleanAllowedSet([]string{" ", "\t", "read_file"}); !reflect.DeepEqual(
		got,
		map[string]struct{}{"read_file": {}},
	) {
		t.Fatalf("cleanAllowedSet() = %#v", got)
	}
}
