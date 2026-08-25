package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func resolveTurnProfileOptions(cfg *config.Config, opts processOptions) (processOptions, error) {
	profile, err := canonicalTurnProfile(opts.TurnProfile)
	if err != nil {
		return opts, err
	}
	if cfg != nil {
		global, ok, resolveErr := cfg.Agents.Defaults.ResolveTurnProfile()
		if resolveErr != nil {
			return opts, resolveErr
		}
		if ok {
			profile = meetCanonicalTurnProfiles(canonicalResolvedTurnProfile(global), profile)
		}
	}
	if opts.DisableTools {
		profile = turnProfileWithToolsOff(profile)
	}
	opts.TurnProfile = profile
	if opts.NoHistory || profile.HistoryMode == config.TurnProfileModeOff {
		opts.NoHistory = true
		opts.EnableSummary = false
	}
	return opts, nil
}

// meetTurnProfiles returns the most restrictive authority represented by left
// and right. A disabled whole profile is the identity element. Enabled profiles
// are canonicalized before the meet so unknown modes fail instead of reaching
// permissive consumer defaults.
func meetTurnProfiles(
	left config.EffectiveTurnProfile,
	right config.EffectiveTurnProfile,
) (config.EffectiveTurnProfile, error) {
	left, err := canonicalTurnProfile(left)
	if err != nil {
		return config.EffectiveTurnProfile{}, err
	}
	right, err = canonicalTurnProfile(right)
	if err != nil {
		return config.EffectiveTurnProfile{}, err
	}
	return meetCanonicalTurnProfiles(left, right), nil
}

func meetCanonicalTurnProfiles(
	left config.EffectiveTurnProfile,
	right config.EffectiveTurnProfile,
) config.EffectiveTurnProfile {
	if !left.Enabled {
		return right
	}
	if !right.Enabled {
		return left
	}

	historyMode := meetDefaultOffMode(left.HistoryMode, right.HistoryMode)
	systemPromptMode := meetDefaultOffMode(left.SystemPromptMode, right.SystemPromptMode)
	skillsMode, allowedSkills := meetCapabilityMode(
		left.SkillsMode,
		left.AllowedSkills,
		right.SkillsMode,
		right.AllowedSkills,
	)
	toolsMode, allowedTools := meetCapabilityMode(
		left.ToolsMode,
		left.AllowedTools,
		right.ToolsMode,
		right.AllowedTools,
	)

	return config.EffectiveTurnProfile{
		Enabled:          true,
		HistoryMode:      historyMode,
		SystemPromptMode: systemPromptMode,
		SkillsMode:       skillsMode,
		ToolsMode:        toolsMode,
		AllowedSkills:    allowedSkills,
		AllowedTools:     allowedTools,
	}
}

func canonicalTurnProfile(
	profile config.EffectiveTurnProfile,
) (config.EffectiveTurnProfile, error) {
	if !profile.Enabled {
		return config.EffectiveTurnProfile{}, nil
	}

	var err error
	profile.HistoryMode, err = canonicalTurnProfileMode("history", profile.HistoryMode, false)
	if err != nil {
		return config.EffectiveTurnProfile{}, err
	}
	profile.SystemPromptMode, err = canonicalTurnProfileMode(
		"system_prompt",
		profile.SystemPromptMode,
		false,
	)
	if err != nil {
		return config.EffectiveTurnProfile{}, err
	}
	profile.SkillsMode, err = canonicalTurnProfileMode("skills", profile.SkillsMode, true)
	if err != nil {
		return config.EffectiveTurnProfile{}, err
	}
	profile.ToolsMode, err = canonicalTurnProfileMode("tools", profile.ToolsMode, true)
	if err != nil {
		return config.EffectiveTurnProfile{}, err
	}

	profile.AllowedSkills = canonicalTurnProfileNames(profile.AllowedSkills)
	profile.AllowedTools = canonicalTurnProfileNames(profile.AllowedTools)
	profile.SkillsMode, profile.AllowedSkills = canonicalCapabilityMode(
		profile.SkillsMode,
		profile.AllowedSkills,
	)
	profile.ToolsMode, profile.AllowedTools = canonicalCapabilityMode(
		profile.ToolsMode,
		profile.AllowedTools,
	)
	return profile, nil
}

// canonicalResolvedTurnProfile applies list and empty-custom normalization to
// a profile already validated and mode-normalized by AgentDefaults.ResolveTurnProfile.
func canonicalResolvedTurnProfile(
	profile config.EffectiveTurnProfile,
) config.EffectiveTurnProfile {
	profile.AllowedSkills = canonicalTurnProfileNames(profile.AllowedSkills)
	profile.AllowedTools = canonicalTurnProfileNames(profile.AllowedTools)
	profile.SkillsMode, profile.AllowedSkills = canonicalCapabilityMode(
		profile.SkillsMode,
		profile.AllowedSkills,
	)
	profile.ToolsMode, profile.AllowedTools = canonicalCapabilityMode(
		profile.ToolsMode,
		profile.AllowedTools,
	)
	return profile
}

func canonicalTurnProfileMode(
	field string,
	mode config.TurnProfileMode,
	allowCustom bool,
) (config.TurnProfileMode, error) {
	mode = mode.Effective()
	switch mode {
	case config.TurnProfileModeDefault, config.TurnProfileModeOff:
		return mode, nil
	case config.TurnProfileModeCustom:
		if allowCustom {
			return mode, nil
		}
	}
	return "", fmt.Errorf("turn profile %s mode %q is unsupported", field, mode)
}

func canonicalCapabilityMode(
	mode config.TurnProfileMode,
	allowed []string,
) (config.TurnProfileMode, []string) {
	if mode != config.TurnProfileModeCustom || len(allowed) == 0 {
		if mode == config.TurnProfileModeCustom {
			mode = config.TurnProfileModeOff
		}
		return mode, nil
	}
	return mode, append([]string(nil), allowed...)
}

func canonicalTurnProfileNames(values []string) []string {
	set := cleanAllowedSet(values)
	if len(set) == 0 {
		return nil
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func meetDefaultOffMode(
	left config.TurnProfileMode,
	right config.TurnProfileMode,
) config.TurnProfileMode {
	if left == config.TurnProfileModeOff || right == config.TurnProfileModeOff {
		return config.TurnProfileModeOff
	}
	return config.TurnProfileModeDefault
}

func meetCapabilityMode(
	leftMode config.TurnProfileMode,
	leftAllowed []string,
	rightMode config.TurnProfileMode,
	rightAllowed []string,
) (config.TurnProfileMode, []string) {
	if leftMode == config.TurnProfileModeOff || rightMode == config.TurnProfileModeOff {
		return config.TurnProfileModeOff, nil
	}
	if leftMode == config.TurnProfileModeDefault {
		return canonicalCapabilityMode(rightMode, rightAllowed)
	}
	if rightMode == config.TurnProfileModeDefault {
		return canonicalCapabilityMode(leftMode, leftAllowed)
	}

	rightSet := cleanAllowedSet(rightAllowed)
	intersection := make([]string, 0, min(len(leftAllowed), len(rightAllowed)))
	for _, name := range leftAllowed {
		if _, ok := rightSet[name]; ok {
			intersection = append(intersection, name)
		}
	}
	return canonicalCapabilityMode(config.TurnProfileModeCustom, intersection)
}

func turnProfileWithToolsOff(profile config.EffectiveTurnProfile) config.EffectiveTurnProfile {
	if !profile.Enabled {
		profile = config.EffectiveTurnProfile{
			Enabled:          true,
			HistoryMode:      config.TurnProfileModeDefault,
			SystemPromptMode: config.TurnProfileModeDefault,
			SkillsMode:       config.TurnProfileModeDefault,
		}
	}
	profile.Enabled = true
	profile.ToolsMode = config.TurnProfileModeOff
	profile.AllowedSkills = append([]string(nil), profile.AllowedSkills...)
	profile.AllowedTools = nil
	return profile
}

func turnProfileSystemPromptOff(profile config.EffectiveTurnProfile) bool {
	return profile.Enabled && profile.SystemPromptMode == config.TurnProfileModeOff
}

func turnProfileSkillsOff(profile config.EffectiveTurnProfile) bool {
	return profile.Enabled && profile.SkillsMode == config.TurnProfileModeOff
}

func turnProfileCustomSkills(profile config.EffectiveTurnProfile) bool {
	return profile.Enabled && profile.SkillsMode == config.TurnProfileModeCustom
}

func turnProfileHasCallableTools(
	profile config.EffectiveTurnProfile,
	defs []providers.ToolDefinition,
) bool {
	if !profile.Enabled {
		return true
	}
	return len(filterToolsByTurnProfile(defs, profile)) > 0
}

func turnProfileToolAllowed(profile config.EffectiveTurnProfile, name string) bool {
	if !profile.Enabled {
		return true
	}
	switch profile.ToolsMode {
	case config.TurnProfileModeOff:
		return false
	case config.TurnProfileModeCustom:
		allowed := cleanAllowedSet(profile.AllowedTools)
		if len(allowed) == 0 {
			return false
		}
		_, ok := allowed[strings.ToLower(strings.TrimSpace(name))]
		return ok
	default:
		return true
	}
}

func toolUseSystemPromptRule() string {
	return "**ALWAYS use tools** - When you need to perform an action (schedule reminders, send messages, execute commands, etc.), you MUST call the appropriate tool. Do NOT just say you'll do it or pretend to do it."
}

func filterNamesByTurnProfile(names []string, allowed []string) []string {
	if len(names) == 0 {
		return nil
	}
	allowedSet := cleanAllowedSet(allowed)
	if len(allowedSet) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := allowedSet[strings.ToLower(strings.TrimSpace(name))]; ok {
			out = append(out, name)
		}
	}
	return out
}

func filterToolsByTurnProfile(
	defs []providers.ToolDefinition,
	profile config.EffectiveTurnProfile,
) []providers.ToolDefinition {
	if !profile.Enabled {
		return defs
	}
	switch profile.ToolsMode {
	case config.TurnProfileModeOff:
		return nil
	case config.TurnProfileModeCustom:
		allowed := cleanAllowedSet(profile.AllowedTools)
		if len(allowed) == 0 {
			return nil
		}
		filtered := make([]providers.ToolDefinition, 0, len(defs))
		for _, def := range defs {
			if _, ok := allowed[strings.ToLower(strings.TrimSpace(def.Function.Name))]; ok {
				filtered = append(filtered, def)
			}
		}
		return filtered
	default:
		return defs
	}
}

func cleanAllowedSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		out[value] = struct{}{}
	}
	return out
}
