package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

const (
	DefaultPRLifecycleGateProfileID   = "default"
	DefaultPRLifecycleGateProfileName = "Default"
	MaxPRLifecycleGateProfiles        = 256
	MaxPRLifecycleAssignments         = 8192
	MaxPRLifecycleDecisionPoints      = 128
	MaxPRLifecycleConfigBytes         = 4 << 20
)

var prLifecycleProfileIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type PRLifecycleConfig struct {
	GateProfiles          map[string]PRLifecycleGateProfile `json:"gate_profiles"`
	DefaultGateProfileID  string                            `json:"default_gate_profile_id"`
	RepositoryAssignments map[string]string                 `json:"repository_assignments,omitempty"`
	Nudge                 PRLifecycleNudgeConfig            `json:"nudge"`
	Scope                 PRLifecycleScopeConfig            `json:"scope"`
	DeferredIssues        PRLifecycleDeferredIssueConfig    `json:"deferred_issues"`
}

type PRLifecycleDeferredIssueMode string

const (
	PRLifecycleDeferredIssuesOff       PRLifecycleDeferredIssueMode = "off"
	PRLifecycleDeferredIssuesAsk       PRLifecycleDeferredIssueMode = "ask"
	PRLifecycleDeferredIssuesAutomatic PRLifecycleDeferredIssueMode = "automatic"
)

type PRLifecycleDeferredIssueConfig struct {
	Mode PRLifecycleDeferredIssueMode `json:"mode"`
}

type PRLifecycleGateProfile struct {
	Name      string                                `json:"name"`
	Workflows map[string]gatetypes.GateWorkflowSpec `json:"workflows"`
}

type PRLifecycleNudgeConfig struct {
	ReviewMinimumAdditional     int `json:"review_minimum_additional"`
	ReviewMaximumAdditional     int `json:"review_maximum_additional"`
	CompletionMinimumAdditional int `json:"completion_minimum_additional"`
	CompletionMaximumAdditional int `json:"completion_maximum_additional"`
}

type PRLifecycleScopeConfig struct {
	XS PRLifecycleSizeThreshold `json:"xs"`
	S  PRLifecycleSizeThreshold `json:"s"`
	M  PRLifecycleSizeThreshold `json:"m"`
}

type PRLifecycleSizeThreshold struct {
	Files         int `json:"files"`
	SemanticLines int `json:"semantic_lines"`
	Modules       int `json:"modules"`
}

func (config PRLifecycleConfig) IsZero() bool {
	return config.GateProfiles == nil && config.DefaultGateProfileID == "" &&
		config.RepositoryAssignments == nil && config.Nudge == (PRLifecycleNudgeConfig{}) &&
		config.Scope == (PRLifecycleScopeConfig{}) &&
		config.DeferredIssues == (PRLifecycleDeferredIssueConfig{})
}

func (config PRLifecycleConfig) Effective() PRLifecycleConfig {
	if config.IsZero() {
		return DefaultPRLifecycleConfig()
	}
	return config
}

func DefaultPRLifecycleConfig() PRLifecycleConfig {
	return PRLifecycleConfig{
		GateProfiles: map[string]PRLifecycleGateProfile{
			DefaultPRLifecycleGateProfileID: defaultPRLifecycleGateProfile(),
		},
		DefaultGateProfileID:  DefaultPRLifecycleGateProfileID,
		RepositoryAssignments: make(map[string]string),
		Nudge: PRLifecycleNudgeConfig{
			ReviewMinimumAdditional: 2, ReviewMaximumAdditional: 5,
			CompletionMinimumAdditional: 2, CompletionMaximumAdditional: 5,
		},
		Scope: PRLifecycleScopeConfig{
			XS: PRLifecycleSizeThreshold{Files: 1, SemanticLines: 20, Modules: 1},
			S:  PRLifecycleSizeThreshold{Files: 3, SemanticLines: 100, Modules: 1},
			M:  PRLifecycleSizeThreshold{Files: 10, SemanticLines: 500, Modules: 3},
		},
		DeferredIssues: PRLifecycleDeferredIssueConfig{Mode: PRLifecycleDeferredIssuesAsk},
	}
}

func defaultPRLifecycleGateProfile() PRLifecycleGateProfile {
	human := func(id, title string) gatetypes.GateStageSpec {
		return gatetypes.GateStageSpec{ID: id, Kind: gatetypes.GateHuman, Title: title, Questions: []any{"Approve this action?"}}
	}
	zero := func(id string) gatetypes.GateStageSpec {
		return gatetypes.GateStageSpec{ID: id, Kind: gatetypes.GateZero}
	}
	authorization := func(point string, stages ...gatetypes.GateStageSpec) gatetypes.GateWorkflowSpec {
		return gatetypes.GateWorkflowSpec{
			ID: strings.ReplaceAll(point, ".", "-"), Name: point,
			Purpose: gatetypes.GatePurposeAuthorization, DecisionPoint: point, Stages: stages,
		}
	}
	classification := func(point string, stages ...gatetypes.GateStageSpec) gatetypes.GateWorkflowSpec {
		return gatetypes.GateWorkflowSpec{
			ID: strings.ReplaceAll(point, ".", "-"), Name: point,
			Purpose: gatetypes.GatePurposeClassification, DecisionPoint: point, Stages: stages,
		}
	}
	return PRLifecycleGateProfile{
		Name: DefaultPRLifecycleGateProfileName,
		Workflows: map[string]gatetypes.GateWorkflowSpec{
			"pr.charter.confirm":   authorization("pr.charter.confirm", human("human-confirm", "Confirm PR charter")),
			"pr.charter.reconfirm": authorization("pr.charter.reconfirm", human("human-reconfirm", "Confirm revised PR charter")),
			"pr.review.start":      authorization("pr.review.start", zero("verified-by-domain")),
			"pr.review.complete":   authorization("pr.review.complete", zero("verified-by-domain")),
			"pr.finding.classify": classification(
				"pr.finding.classify",
				human("human-finding-scope", "Classify an ambiguous finding for this PR"),
			),
			"pr.implementation.eligibility": authorization(
				"pr.implementation.eligibility",
				human("human-eligibility", "Authorize implementation on a pull request not owned by the current user"),
			),
			"pr.implementation.start":    authorization("pr.implementation.start", zero("verified-by-domain")),
			"pr.implementation.scope":    authorization("pr.implementation.scope", human("human-scope", "Classify a large exact-scope or necessary-adjacent implementation")),
			"pr.implementation.complete": authorization("pr.implementation.complete", human("human-complete", "Accept completed implementation")),
			"pr.review.publish":          authorization("pr.review.publish", human("human-review-publish", "Publish GitHub review")),
			"pr.implementation.publish":  authorization("pr.implementation.publish", human("human-push", "Push implementation")),
			"pr.deferred.publish":        authorization("pr.deferred.publish", human("human-issue", "Create GitHub follow-up issue")),
			"pr.correction.promote":      authorization("pr.correction.promote", human("human-lesson", "Promote repository lesson")),
			"pr.publication.reconcile":   authorization("pr.publication.reconcile", human("human-reconcile", "Resolve ambiguous provider outcome")),
		},
	}
}

func (config PRLifecycleConfig) Validate() error {
	if len(config.GateProfiles) == 0 || len(config.GateProfiles) > MaxPRLifecycleGateProfiles {
		return fmt.Errorf("PR lifecycle gate profiles must contain between 1 and %d entries", MaxPRLifecycleGateProfiles)
	}
	if config.DefaultGateProfileID == "" {
		return errors.New("PR lifecycle default gate profile is required")
	}
	if _, exists := config.GateProfiles[config.DefaultGateProfileID]; !exists {
		return errors.New("PR lifecycle default gate profile does not exist")
	}
	defaultProfile, exists := config.GateProfiles[DefaultPRLifecycleGateProfileID]
	if !exists || defaultProfile.Name != DefaultPRLifecycleGateProfileName {
		return errors.New("PR lifecycle built-in default profile is missing or renamed")
	}
	names := make(map[string]string, len(config.GateProfiles))
	for id, profile := range config.GateProfiles {
		if !prLifecycleProfileIDPattern.MatchString(id) || profile.Name == "" ||
			profile.Name != strings.TrimSpace(profile.Name) || len(profile.Name) > 128 {
			return fmt.Errorf("PR lifecycle profile %q has invalid identity", id)
		}
		foldedName := strings.ToLower(profile.Name)
		if previous := names[foldedName]; previous != "" {
			return fmt.Errorf("PR lifecycle profiles %q and %q have duplicate names", previous, id)
		}
		names[foldedName] = id
		if len(profile.Workflows) > MaxPRLifecycleDecisionPoints {
			return fmt.Errorf("PR lifecycle profile %q has too many workflows", id)
		}
		for point, workflow := range profile.Workflows {
			if point != workflow.DecisionPoint {
				return fmt.Errorf("PR lifecycle profile %q workflow key does not match decision point", id)
			}
			if err := validatePRLifecycleGateWorkflow(workflow); err != nil {
				return fmt.Errorf("PR lifecycle profile %q workflow %q: %w", id, point, err)
			}
		}
	}
	if len(config.RepositoryAssignments) > MaxPRLifecycleAssignments {
		return fmt.Errorf("PR lifecycle repository assignments exceed %d", MaxPRLifecycleAssignments)
	}
	foldedRepositories := make(map[string]string, len(config.RepositoryAssignments))
	for identity, profileID := range config.RepositoryAssignments {
		if !validPRLifecycleRepositoryIdentity(identity) {
			return fmt.Errorf("PR lifecycle repository identity %q is invalid", identity)
		}
		folded := strings.ToLower(identity)
		if previous := foldedRepositories[folded]; previous != "" {
			return fmt.Errorf("PR lifecycle repository identities %q and %q collide", previous, identity)
		}
		foldedRepositories[folded] = identity
		if _, exists := config.GateProfiles[profileID]; !exists {
			return fmt.Errorf("PR lifecycle repository %q selects missing profile %q", identity, profileID)
		}
	}
	if err := config.Nudge.Validate(); err != nil {
		return err
	}
	if err := config.Scope.Validate(); err != nil {
		return err
	}
	if err := config.DeferredIssues.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(config)
	if err != nil || len(encoded) > MaxPRLifecycleConfigBytes {
		return fmt.Errorf("PR lifecycle config exceeds %d bytes", MaxPRLifecycleConfigBytes)
	}
	return nil
}

// ValidateAgentReferences rejects profiles that name an AI agent the runtime
// cannot instantiate from the same full configuration. Shape validation stays
// independent so scoped editors can validate a candidate before loading the
// current agent catalog, then perform this exact reference check under CAS.
func (config PRLifecycleConfig) ValidateAgentReferences(agents AgentsConfig) error {
	known := make(map[string]struct{}, len(agents.List)+1)
	if len(agents.List) == 0 {
		known["main"] = struct{}{}
	} else {
		for _, agent := range agents.List {
			known[agent.ID] = struct{}{}
		}
	}
	type reference struct {
		profileID     string
		decisionPoint string
		stageID       string
		agentID       string
	}
	var references []reference
	for profileID, profile := range config.GateProfiles {
		for decisionPoint, workflow := range profile.Workflows {
			for _, stage := range workflow.Stages {
				if stage.Kind != gatetypes.GateAIWorkingContext && stage.Kind != gatetypes.GateAIIsolatedContext {
					continue
				}
				references = append(references, reference{
					profileID: profileID, decisionPoint: decisionPoint,
					stageID: stage.ID, agentID: stage.AgentID,
				})
			}
		}
	}
	sort.Slice(references, func(left, right int) bool {
		if references[left].profileID != references[right].profileID {
			return references[left].profileID < references[right].profileID
		}
		if references[left].decisionPoint != references[right].decisionPoint {
			return references[left].decisionPoint < references[right].decisionPoint
		}
		return references[left].stageID < references[right].stageID
	})
	for _, candidate := range references {
		if _, exists := known[candidate.agentID]; !exists {
			return fmt.Errorf(
				"PR lifecycle profile %q workflow %q stage %q selects unknown agent %q",
				candidate.profileID, candidate.decisionPoint, candidate.stageID, candidate.agentID,
			)
		}
	}
	return nil
}

func (config PRLifecycleDeferredIssueConfig) Validate() error {
	switch config.Mode {
	case PRLifecycleDeferredIssuesOff,
		PRLifecycleDeferredIssuesAsk,
		PRLifecycleDeferredIssuesAutomatic:
		return nil
	default:
		return errors.New("PR lifecycle deferred issue mode must be off, ask, or automatic")
	}
}

func validatePRLifecycleGateWorkflow(workflow gatetypes.GateWorkflowSpec) error {
	return gatetypes.ValidateGateWorkflowSpecV2(workflow)
}

func (config PRLifecycleNudgeConfig) Validate() error {
	if !validNudgeBounds(config.ReviewMinimumAdditional, config.ReviewMaximumAdditional) ||
		!validNudgeBounds(config.CompletionMinimumAdditional, config.CompletionMaximumAdditional) {
		return errors.New("PR lifecycle nudge minimum/maximum must be ordered between 0 and 10")
	}
	return nil
}

func validNudgeBounds(minimum, maximum int) bool {
	return minimum >= 0 && maximum >= minimum && maximum <= 10
}

func (config PRLifecycleScopeConfig) Validate() error {
	if !positiveLifecycleThreshold(config.XS) || !positiveLifecycleThreshold(config.S) ||
		!positiveLifecycleThreshold(config.M) ||
		config.XS.Files > config.S.Files || config.S.Files > config.M.Files ||
		config.XS.SemanticLines > config.S.SemanticLines || config.S.SemanticLines > config.M.SemanticLines ||
		config.XS.Modules > config.S.Modules || config.S.Modules > config.M.Modules {
		return errors.New("PR lifecycle scope thresholds must be positive and monotonic")
	}
	return nil
}

func positiveLifecycleThreshold(value PRLifecycleSizeThreshold) bool {
	return value.Files > 0 && value.SemanticLines > 0 && value.Modules > 0
}

func validPRLifecycleRepositoryIdentity(value string) bool {
	parts := strings.Split(value, "|")
	return len(parts) == 2 && strings.HasPrefix(parts[0], "https://") && parts[1] != "" &&
		value == strings.TrimSpace(value) && len(value) <= 1024
}

func (config PRLifecycleConfig) ProfileForRepository(providerOrigin, repositoryID string) (string, PRLifecycleGateProfile, string, error) {
	if err := config.Validate(); err != nil {
		return "", PRLifecycleGateProfile{}, "", err
	}
	identity := strings.ToLower(strings.TrimSuffix(providerOrigin, "/") + "|" + repositoryID)
	profileID := config.DefaultGateProfileID
	for candidate, assigned := range config.RepositoryAssignments {
		if strings.ToLower(strings.TrimSuffix(strings.Split(candidate, "|")[0], "/")+"|"+strings.Split(candidate, "|")[1]) == identity {
			profileID = assigned
			break
		}
	}
	profile := config.GateProfiles[profileID]
	revision, err := PRLifecycleProfileRevision(profileID, profile)
	return profileID, profile, revision, err
}

func PRLifecycleProfileRevision(id string, profile PRLifecycleGateProfile) (string, error) {
	points := make([]string, 0, len(profile.Workflows))
	for point := range profile.Workflows {
		points = append(points, point)
	}
	sort.Strings(points)
	canonical := struct {
		ID        string            `json:"id"`
		Name      string            `json:"name"`
		Workflows []json.RawMessage `json:"workflows"`
	}{ID: id, Name: profile.Name}
	for _, point := range points {
		encoded, err := gatetypes.CanonicalGateWorkflowSpecJSON(profile.Workflows[point])
		if err != nil {
			return "", err
		}
		canonical.Workflows = append(canonical.Workflows, encoded)
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
