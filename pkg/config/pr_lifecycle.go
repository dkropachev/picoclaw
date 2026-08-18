package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

const (
	DefaultPRLifecycleGateConfigID   = "default"
	DefaultPRLifecycleGateConfigName = "Default"
	MaxPRLifecycleGateConfigs        = 256
	MaxPRLifecycleAssignments        = 8192
	MaxPRLifecycleGateBindings       = 8192
	MaxPRLifecycleConfigBytes        = 4 << 20
	PRLifecycleWorkflowRef           = "workflows/pr-lifecycle.yml"
)

var (
	prLifecycleConfigIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	prLifecycleGateRefPattern  = regexp.MustCompile(`^gates\.[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	prLifecycleBuiltInGateRefs = map[string]struct{}{
		"gates.charter-confirm": {}, "gates.charter-reconfirm": {},
		"gates.review-start": {}, "gates.review-complete": {},
		"gates.finding-classify": {}, "gates.implementation-eligibility": {},
		"gates.implementation-start": {}, "gates.implementation-scope": {},
		"gates.implementation-hard-scope": {}, "gates.implementation-complete": {},
		"gates.review-publish": {}, "gates.implementation-publish": {},
		"gates.deferred-publish": {}, "gates.correction-promote": {},
		"gates.publication-reconcile": {},
	}
)

// PRLifecycleConfig contains the repository-selected gate action overrides and
// the lifecycle settings that are independent of workflow gate execution.
//
// A binding is an atomic replacement for the default action declared by the
// referenced workflow gate. Missing bindings deliberately inherit that
// workflow default; no field-by-field merge exists.
type PRLifecycleConfig struct {
	GateConfigs           map[string]PRLifecycleGateConfig `json:"gate-configs"`
	DefaultGateConfigID   string                           `json:"default-gate-config"`
	RepositoryAssignments map[string]string                `json:"repository-assignments,omitempty"`
	Nudge                 PRLifecycleNudgeConfig           `json:"nudge"`
	Scope                 PRLifecycleScopeConfig           `json:"scope"`
	DeferredIssues        PRLifecycleDeferredIssueConfig   `json:"deferred-issues"`
}

func (config *PRLifecycleConfig) UnmarshalJSON(data []byte) error {
	type plain PRLifecycleConfig
	var decoded plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("PR lifecycle config contains trailing JSON")
		}
		return err
	}
	*config = PRLifecycleConfig(decoded)
	return nil
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

// PRLifecycleGateConfig is a named, assignable collection of workflow gate
// overrides. Bindings are matched by the exact pair (workflow-ref, gate-ref).
type PRLifecycleGateConfig struct {
	Name     string                   `json:"name"`
	Bindings []PRLifecycleGateBinding `json:"bindings"`
}

type PRLifecycleGateBinding struct {
	WorkflowRef string                `json:"workflow-ref"`
	GateRef     string                `json:"gate-ref"`
	Action      *gatetypes.GateAction `json:"action,omitempty"`
}

type PRLifecycleNudgeConfig struct {
	ReviewMinimumAdditional     int `json:"review-minimum-additional"`
	ReviewMaximumAdditional     int `json:"review-maximum-additional"`
	CompletionMinimumAdditional int `json:"completion-minimum-additional"`
	CompletionMaximumAdditional int `json:"completion-maximum-additional"`
}

type PRLifecycleScopeConfig struct {
	XS PRLifecycleSizeThreshold `json:"xs"`
	S  PRLifecycleSizeThreshold `json:"s"`
	M  PRLifecycleSizeThreshold `json:"m"`
}

type PRLifecycleSizeThreshold struct {
	Files         int `json:"files"`
	SemanticLines int `json:"semantic-lines"`
	Modules       int `json:"modules"`
}

func (config PRLifecycleConfig) IsZero() bool {
	return config.GateConfigs == nil && config.DefaultGateConfigID == "" &&
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
		GateConfigs: map[string]PRLifecycleGateConfig{
			DefaultPRLifecycleGateConfigID: {
				Name:     DefaultPRLifecycleGateConfigName,
				Bindings: []PRLifecycleGateBinding{},
			},
		},
		DefaultGateConfigID:   DefaultPRLifecycleGateConfigID,
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

func (config PRLifecycleConfig) Validate() error {
	if len(config.GateConfigs) == 0 || len(config.GateConfigs) > MaxPRLifecycleGateConfigs {
		return fmt.Errorf("PR lifecycle gate configurations must contain between 1 and %d entries", MaxPRLifecycleGateConfigs)
	}
	if config.DefaultGateConfigID == "" {
		return errors.New("PR lifecycle default gate configuration is required")
	}
	if _, exists := config.GateConfigs[config.DefaultGateConfigID]; !exists {
		return errors.New("PR lifecycle default gate configuration does not exist")
	}
	defaultConfig, exists := config.GateConfigs[DefaultPRLifecycleGateConfigID]
	if !exists || defaultConfig.Name != DefaultPRLifecycleGateConfigName || len(defaultConfig.Bindings) != 0 {
		return errors.New("PR lifecycle built-in default gate configuration must exist unchanged and contain no overrides")
	}

	names := make(map[string]string, len(config.GateConfigs))
	totalBindings := 0
	for id, gateConfig := range config.GateConfigs {
		if len(id) > 64 || !prLifecycleConfigIDPattern.MatchString(id) ||
			gateConfig.Name == "" || gateConfig.Name != strings.TrimSpace(gateConfig.Name) ||
			len(gateConfig.Name) > 128 {
			return fmt.Errorf("PR lifecycle gate configuration %q has invalid identity", id)
		}
		foldedName := strings.ToLower(gateConfig.Name)
		if previous := names[foldedName]; previous != "" {
			return fmt.Errorf("PR lifecycle gate configurations %q and %q have duplicate names", previous, id)
		}
		names[foldedName] = id
		totalBindings += len(gateConfig.Bindings)
		if totalBindings > MaxPRLifecycleGateBindings {
			return fmt.Errorf("PR lifecycle gate bindings exceed %d", MaxPRLifecycleGateBindings)
		}
		seenBindings := make(map[string]struct{}, len(gateConfig.Bindings))
		for index, binding := range gateConfig.Bindings {
			if err := validatePRLifecycleGateBinding(binding); err != nil {
				return fmt.Errorf("PR lifecycle gate configuration %q binding %d: %w", id, index, err)
			}
			key := binding.WorkflowRef + "\x00" + binding.GateRef
			if _, duplicate := seenBindings[key]; duplicate {
				return fmt.Errorf(
					"PR lifecycle gate configuration %q contains duplicate binding for workflow %q gate %q",
					id, binding.WorkflowRef, binding.GateRef,
				)
			}
			seenBindings[key] = struct{}{}
		}
	}

	if len(config.RepositoryAssignments) > MaxPRLifecycleAssignments {
		return fmt.Errorf("PR lifecycle repository assignments exceed %d", MaxPRLifecycleAssignments)
	}
	foldedRepositories := make(map[string]string, len(config.RepositoryAssignments))
	for identity, configID := range config.RepositoryAssignments {
		if !validPRLifecycleRepositoryIdentity(identity) {
			return fmt.Errorf("PR lifecycle repository identity %q is invalid", identity)
		}
		folded := strings.ToLower(identity)
		if previous := foldedRepositories[folded]; previous != "" {
			return fmt.Errorf("PR lifecycle repository identities %q and %q collide", previous, identity)
		}
		foldedRepositories[folded] = identity
		if _, exists := config.GateConfigs[configID]; !exists {
			return fmt.Errorf("PR lifecycle repository %q selects missing gate configuration %q", identity, configID)
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

func validatePRLifecycleGateBinding(binding PRLifecycleGateBinding) error {
	if !validPRLifecycleWorkflowRef(binding.WorkflowRef) {
		return errors.New("workflow-ref is invalid")
	}
	if !prLifecycleGateRefPattern.MatchString(binding.GateRef) || len(binding.GateRef) > 128 {
		return errors.New("gate-ref must be a static full path in the form gates.<kebab-case-id>")
	}
	if binding.WorkflowRef == PRLifecycleWorkflowRef {
		if _, exists := prLifecycleBuiltInGateRefs[binding.GateRef]; !exists {
			return errors.New("gate-ref is not published by the built-in PR lifecycle workflow")
		}
	}
	if binding.Action == nil {
		// An explicit binding without an action means "use workflow default".
		// When Action is present it is validated as one complete atomic override.
		return nil
	}
	if err := validatePRLifecycleGateAction(*binding.Action); err != nil {
		return err
	}
	return nil
}

func validPRLifecycleWorkflowRef(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 1024 ||
		strings.Contains(value, "${{") || strings.HasPrefix(value, "draft:") ||
		strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	if !strings.HasPrefix(value, "workflows/") || strings.Contains(value, "\\") ||
		path.Clean(value) != value {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	extension := strings.ToLower(path.Ext(value))
	return extension == ".yml" || extension == ".yaml"
}

func validatePRLifecycleGateAction(action gatetypes.GateAction) error {
	if err := gatetypes.ValidateGateAction(action); err != nil {
		return fmt.Errorf("action override is invalid: %w", err)
	}
	switch action.Type {
	case gatetypes.GateActionAI:
		if !validPRLifecycleActionIdentifier(action.AgentID) {
			return errors.New("AI action agent-id must be kebab-case")
		}
		if action.Tools != "none" {
			return errors.New("AI action over private PR evidence requires tools: none")
		}
		switch action.Session {
		case "ephemeral":
			if action.History != "none" || action.Cache != "none" {
				return errors.New("ephemeral AI action requires history: none and cache: none")
			}
		case "private":
			if action.History != "read_only" || action.Cache != "none" && action.Cache != "session" {
				return errors.New("private AI action requires history: read_only and cache: none or session")
			}
		default:
			return errors.New("AI action session must be ephemeral or private")
		}
	case gatetypes.GateActionDeterministic:
		for id := range action.Fields {
			if len(id) > 64 || !prLifecycleConfigIDPattern.MatchString(id) {
				return fmt.Errorf("deterministic action field %q is invalid", id)
			}
		}
		encoded, err := json.Marshal(action.Fields)
		if err != nil || len(encoded) > 1<<20 {
			return errors.New("deterministic action fields are invalid or too large")
		}
	case gatetypes.GateActionWorkflow:
		if !validPRLifecycleWorkflowRef(action.WorkflowRef) {
			return errors.New("workflow action workflow-ref is invalid")
		}
	}
	return nil
}

func validPRLifecycleActionIdentifier(value string) bool {
	return len(value) <= 64 && prLifecycleConfigIDPattern.MatchString(value)
}

// ValidateAgentReferences rejects configuration overrides that name an AI
// agent the runtime cannot instantiate from the same full configuration.
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
		configID    string
		workflowRef string
		gateRef     string
		agentID     string
	}
	var references []reference
	for configID, gateConfig := range config.GateConfigs {
		for _, binding := range gateConfig.Bindings {
			if binding.Action == nil || string(binding.Action.Type) != "ai" {
				continue
			}
			references = append(references, reference{
				configID: configID, workflowRef: binding.WorkflowRef,
				gateRef: binding.GateRef, agentID: binding.Action.AgentID,
			})
		}
	}
	sort.Slice(references, func(left, right int) bool {
		if references[left].configID != references[right].configID {
			return references[left].configID < references[right].configID
		}
		if references[left].workflowRef != references[right].workflowRef {
			return references[left].workflowRef < references[right].workflowRef
		}
		return references[left].gateRef < references[right].gateRef
	})
	for _, candidate := range references {
		if _, exists := known[candidate.agentID]; !exists {
			return fmt.Errorf(
				"PR lifecycle gate configuration %q workflow %q gate %q selects unknown agent %q",
				candidate.configID, candidate.workflowRef, candidate.gateRef, candidate.agentID,
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

func (config PRLifecycleConfig) ConfigForRepository(
	providerOrigin, repositoryID string,
) (string, PRLifecycleGateConfig, string, error) {
	if err := config.Validate(); err != nil {
		return "", PRLifecycleGateConfig{}, "", err
	}
	identity := strings.ToLower(strings.TrimSuffix(providerOrigin, "/") + "|" + repositoryID)
	configID := config.DefaultGateConfigID
	for candidate, assigned := range config.RepositoryAssignments {
		parts := strings.Split(candidate, "|")
		if strings.ToLower(strings.TrimSuffix(parts[0], "/")+"|"+parts[1]) == identity {
			configID = assigned
			break
		}
	}
	gateConfig := config.GateConfigs[configID]
	revision, err := PRLifecycleGateConfigRevision(configID, gateConfig)
	return configID, gateConfig, revision, err
}

func PRLifecycleGateConfigRevision(id string, gateConfig PRLifecycleGateConfig) (string, error) {
	bindings := append([]PRLifecycleGateBinding(nil), gateConfig.Bindings...)
	sort.Slice(bindings, func(left, right int) bool {
		if bindings[left].WorkflowRef != bindings[right].WorkflowRef {
			return bindings[left].WorkflowRef < bindings[right].WorkflowRef
		}
		return bindings[left].GateRef < bindings[right].GateRef
	})
	encoded, err := json.Marshal(struct {
		ID       string                   `json:"id"`
		Name     string                   `json:"name"`
		Bindings []PRLifecycleGateBinding `json:"bindings"`
	}{ID: id, Name: gateConfig.Name, Bindings: bindings})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
