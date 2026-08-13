package config

import (
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

// ErrReviewAttentionLegacyMigrationExceedsBounds means a valid compact legacy
// overlay catalog cannot be represented as standalone named sets within the
// named-catalog limits. The legacy catalog remains valid for runtime use.
var ErrReviewAttentionLegacyMigrationExceedsBounds = errors.New(
	"legacy review attention catalog exceeds named migration bounds",
)

const (
	MaxReviewAttentionDecisionPointBytes = gatetypes.MaxGatePolicyDecisionPointBytes
	MaxReviewAttentionDecisionPoints     = gatetypes.MaxGatePolicyDecisionPoints
	MaxReviewAttentionRepositories       = gatetypes.MaxGatePolicyRepositories
	MaxReviewAttentionPolicies           = gatetypes.MaxGatePolicyEntries
	MaxReviewAttentionGateSpecs          = gatetypes.MaxGatePolicyGateEntries
	MaxReviewAttentionConfigBytes        = gatetypes.MaxGatePolicyCatalogBytes
	MaxReviewAttentionRuleSets           = MaxReviewAttentionRepositories + 1
	MaxReviewAttentionRuleSetIDBytes     = 64
	MaxReviewAttentionRuleSetNameBytes   = 128
	DefaultReviewAttentionRuleSetID      = "default"
	DefaultReviewAttentionRuleSetName    = "Default"
)

var (
	reviewAttentionDecisionPointPattern  = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)
	reviewAttentionRuleSetIDPattern      = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	reviewAttentionGateIDPattern         = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	reviewAttentionAgentIDPattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	reviewAttentionExpressionPathPattern = regexp.MustCompile(
		`^[A-Za-z_][A-Za-z0-9_-]*(?:\.[A-Za-z0-9_-]+)*$`,
	)
	reviewAttentionJSONNumberType    = reflect.TypeOf(json.Number(""))
	reviewAttentionJSONMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	reviewAttentionTextMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

// ReviewsConfig groups trusted pull-request review automation policy.
type ReviewsConfig struct {
	Attention ReviewAttentionConfig `json:"attention,omitempty"`
}

// ReviewAttentionConfig stores global policies by decision point and exact
// repository-local overrides selected by trusted base-repository identity.
//
//nolint:recvcheck // JSON requires pointer unmarshalling; read and validation APIs use values.
type ReviewAttentionConfig struct {
	Global                map[string][]gatetypes.GateSpec                      `json:"global,omitempty"`
	Repositories          map[string]map[string]gatetypes.RepositoryGatePolicy `json:"repositories,omitempty"`
	RuleSets              map[string]ReviewAttentionRuleSet                    `json:"rule_sets,omitempty"`
	DefaultRuleSetID      string                                               `json:"default_rule_set_id,omitempty"`
	RepositoryAssignments map[string]string                                    `json:"repository_assignments,omitempty"`
}

// UnmarshalJSON resets the representation before decoding. Config loading
// overlays JSON onto DefaultConfig, whose legacy empty maps are intentionally
// initialized for callers; without the reset a normalized document would
// accidentally retain those maps and appear to mix both representations.
func (config *ReviewAttentionConfig) UnmarshalJSON(data []byte) error {
	type wire ReviewAttentionConfig
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*config = ReviewAttentionConfig(decoded)
	if !config.UsesNamedRuleSets() {
		if config.Global == nil {
			config.Global = make(map[string][]gatetypes.GateSpec)
		}
		if config.Repositories == nil {
			config.Repositories = make(
				map[string]map[string]gatetypes.RepositoryGatePolicy,
			)
		}
	}
	return nil
}

// ReviewAttentionRuleSet is one reusable, named collection of decision rules.
// The enclosing map key is its stable identity; Name is intentionally separate
// so the management API can prevent renaming while allowing rules to change.
type ReviewAttentionRuleSet struct {
	Name  string                          `json:"name"`
	Rules map[string][]gatetypes.GateSpec `json:"rules"`
}

// UsesNamedRuleSets reports whether the normalized reusable-set representation
// is present. Legacy global/repository catalogs remain accepted for migration.
func (config ReviewAttentionConfig) UsesNamedRuleSets() bool {
	return config.RuleSets != nil || config.DefaultRuleSetID != "" ||
		config.RepositoryAssignments != nil
}

// Validate rejects ambiguous repository identities, malformed structural gate
// policy, and policy collections that cannot be handled within fixed bounds.
func (config ReviewsConfig) Validate() error {
	return config.Attention.Validate()
}

// Validate checks one complete review-attention policy tree.
func (config ReviewAttentionConfig) Validate() error {
	if config.UsesNamedRuleSets() {
		return config.validateNamedRuleSets()
	}
	return config.validateLegacyCatalog()
}

func (config ReviewAttentionConfig) validateLegacyCatalog() error {
	if len(config.Global) > MaxReviewAttentionDecisionPoints {
		return fmt.Errorf(
			"reviews.attention.global must contain at most %d decision points",
			MaxReviewAttentionDecisionPoints,
		)
	}
	if len(config.Repositories) > MaxReviewAttentionRepositories {
		return fmt.Errorf(
			"reviews.attention.repositories must contain at most %d repositories",
			MaxReviewAttentionRepositories,
		)
	}

	policyCount := len(config.Global)
	gateCount := 0
	for _, decisionPoint := range sortedReviewAttentionKeys(config.Global) {
		gates := config.Global[decisionPoint]
		if gates == nil {
			return fmt.Errorf(
				"reviews.attention.global[%q] must be an array, not null",
				decisionPoint,
			)
		}
		if err := validateReviewAttentionDecisionPoint(decisionPoint); err != nil {
			return fmt.Errorf("reviews.attention.global: %w", err)
		}
		if err := validateReviewAttentionGateList(
			"reviews.attention.global["+decisionPoint+"]",
			gates,
		); err != nil {
			return err
		}
		gateCount += len(gates)
	}

	repositories := sortedReviewAttentionKeys(config.Repositories)
	foldedRepositories := make(map[string]string, len(repositories))
	for _, repository := range repositories {
		if repository == "" || repository != strings.TrimSpace(repository) ||
			!utf8.ValidString(repository) || len(repository) > gatetypes.MaxGatePolicyRepositoryBytes ||
			!githubRepositoryPattern.MatchString(repository) {
			return fmt.Errorf(
				"reviews.attention repository %q must be a trimmed owner/repo name of at most %d bytes",
				repository,
				gatetypes.MaxGatePolicyRepositoryBytes,
			)
		}
		folded := strings.ToLower(repository)
		if previous, exists := foldedRepositories[folded]; exists {
			return fmt.Errorf(
				"reviews.attention repositories %q and %q differ only by case",
				previous,
				repository,
			)
		}
		foldedRepositories[folded] = repository

		policies := config.Repositories[repository]
		if policies == nil {
			return fmt.Errorf(
				"reviews.attention.repositories[%q] must be an object, not null",
				repository,
			)
		}
		if len(policies) > MaxReviewAttentionDecisionPoints {
			return fmt.Errorf(
				"reviews.attention.repositories[%q] must contain at most %d decision points",
				repository,
				MaxReviewAttentionDecisionPoints,
			)
		}
		policyCount += len(policies)
		for _, decisionPoint := range sortedReviewAttentionKeys(policies) {
			if err := validateReviewAttentionDecisionPoint(decisionPoint); err != nil {
				return fmt.Errorf(
					"reviews.attention.repositories[%q]: %w",
					repository,
					err,
				)
			}
			policy := policies[decisionPoint]
			path := "reviews.attention.repositories[" + repository + "][" + decisionPoint + "]"
			if err := validateReviewAttentionRepositoryPolicy(path, policy); err != nil {
				return err
			}
			if err := validateReviewAttentionEffectivePolicy(
				path,
				config.Global[decisionPoint],
				policy,
			); err != nil {
				return err
			}
			gateCount += len(policy.Gates)
		}
	}

	if policyCount > MaxReviewAttentionPolicies {
		return fmt.Errorf(
			"reviews.attention must contain at most %d policies",
			MaxReviewAttentionPolicies,
		)
	}
	if gateCount > MaxReviewAttentionGateSpecs {
		return fmt.Errorf(
			"reviews.attention must contain at most %d gates",
			MaxReviewAttentionGateSpecs,
		)
	}
	encoded, err := gatetypes.MarshalCanonicalGatePolicyCatalog(
		config.Global,
		config.Repositories,
	)
	if err != nil {
		return fmt.Errorf("reviews.attention must contain durable JSON: %w", err)
	}
	if len(encoded) > MaxReviewAttentionConfigBytes {
		return fmt.Errorf(
			"reviews.attention exceeds %d encoded bytes",
			MaxReviewAttentionConfigBytes,
		)
	}
	return nil
}

func (config ReviewAttentionConfig) validateNamedRuleSets() error {
	if config.Global != nil || config.Repositories != nil {
		return fmt.Errorf("reviews.attention cannot mix named rule sets with legacy global/repositories")
	}
	if config.DefaultRuleSetID == "" ||
		len(config.DefaultRuleSetID) > MaxReviewAttentionRuleSetIDBytes ||
		!reviewAttentionRuleSetIDPattern.MatchString(config.DefaultRuleSetID) {
		return fmt.Errorf("reviews.attention.default_rule_set_id must name a canonical rule set")
	}
	if len(config.RuleSets) == 0 || len(config.RuleSets) > MaxReviewAttentionRuleSets {
		return fmt.Errorf(
			"reviews.attention.rule_sets must contain between 1 and %d rule sets",
			MaxReviewAttentionRuleSets,
		)
	}
	defaultSet, exists := config.RuleSets[DefaultReviewAttentionRuleSetID]
	if !exists || defaultSet.Name != DefaultReviewAttentionRuleSetName {
		return fmt.Errorf(
			"reviews.attention.rule_sets[%q] must have name %q",
			DefaultReviewAttentionRuleSetID,
			DefaultReviewAttentionRuleSetName,
		)
	}
	if _, exists := config.RuleSets[config.DefaultRuleSetID]; !exists {
		return fmt.Errorf(
			"reviews.attention.default_rule_set_id references unknown rule set %q",
			config.DefaultRuleSetID,
		)
	}
	if len(config.RepositoryAssignments) > MaxReviewAttentionRepositories {
		return fmt.Errorf(
			"reviews.attention.repository_assignments must contain at most %d repositories",
			MaxReviewAttentionRepositories,
		)
	}

	policyCount := 0
	gateCount := 0
	foldedNames := make(map[string]string, len(config.RuleSets))
	for _, id := range sortedReviewAttentionKeys(config.RuleSets) {
		if len(id) > MaxReviewAttentionRuleSetIDBytes ||
			!reviewAttentionRuleSetIDPattern.MatchString(id) {
			return fmt.Errorf(
				"reviews.attention rule-set ID %q must match %s and be at most %d bytes",
				id,
				reviewAttentionRuleSetIDPattern.String(),
				MaxReviewAttentionRuleSetIDBytes,
			)
		}
		set := config.RuleSets[id]
		if set.Name == "" || set.Name != strings.TrimSpace(set.Name) ||
			!utf8.ValidString(set.Name) || len(set.Name) > MaxReviewAttentionRuleSetNameBytes {
			return fmt.Errorf(
				"reviews.attention.rule_sets[%q].name must be trimmed, nonempty UTF-8 of at most %d bytes",
				id,
				MaxReviewAttentionRuleSetNameBytes,
			)
		}
		foldedName := strings.ToLower(set.Name)
		if previous, duplicate := foldedNames[foldedName]; duplicate {
			return fmt.Errorf(
				"reviews.attention rule-set names %q and %q differ only by case",
				previous,
				set.Name,
			)
		}
		foldedNames[foldedName] = set.Name
		if set.Rules == nil {
			return fmt.Errorf("reviews.attention.rule_sets[%q].rules must be an object, not null", id)
		}
		if len(set.Rules) > MaxReviewAttentionDecisionPoints {
			return fmt.Errorf(
				"reviews.attention.rule_sets[%q].rules must contain at most %d decision points",
				id,
				MaxReviewAttentionDecisionPoints,
			)
		}
		policyCount += len(set.Rules)
		for _, decisionPoint := range sortedReviewAttentionKeys(set.Rules) {
			gates := set.Rules[decisionPoint]
			if gates == nil {
				return fmt.Errorf(
					"reviews.attention.rule_sets[%q].rules[%q] must be an array, not null",
					id,
					decisionPoint,
				)
			}
			if err := validateReviewAttentionDecisionPoint(decisionPoint); err != nil {
				return fmt.Errorf("reviews.attention.rule_sets[%q].rules: %w", id, err)
			}
			if err := validateReviewAttentionGateList(
				"reviews.attention.rule_sets["+id+"].rules["+decisionPoint+"]",
				gates,
			); err != nil {
				return err
			}
			gateCount += len(gates)
		}
	}
	if policyCount > MaxReviewAttentionPolicies {
		return fmt.Errorf(
			"reviews.attention must contain at most %d policies",
			MaxReviewAttentionPolicies,
		)
	}
	if gateCount > MaxReviewAttentionGateSpecs {
		return fmt.Errorf(
			"reviews.attention must contain at most %d gates",
			MaxReviewAttentionGateSpecs,
		)
	}
	foldedRepositories := make(map[string]string, len(config.RepositoryAssignments))
	for _, repository := range sortedReviewAttentionKeys(config.RepositoryAssignments) {
		if err := validateReviewAttentionRepository(repository, foldedRepositories); err != nil {
			return err
		}
		id := config.RepositoryAssignments[repository]
		if _, exists := config.RuleSets[id]; !exists {
			return fmt.Errorf(
				"reviews.attention.repository_assignments[%q] references unknown rule set %q",
				repository,
				id,
			)
		}
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("reviews.attention must contain durable JSON: %w", err)
	}
	if len(encoded) > MaxReviewAttentionConfigBytes {
		return fmt.Errorf(
			"reviews.attention exceeds %d encoded bytes",
			MaxReviewAttentionConfigBytes,
		)
	}
	return nil
}

func validateReviewAttentionRepository(
	repository string,
	foldedRepositories map[string]string,
) error {
	if repository == "" || repository != strings.TrimSpace(repository) ||
		!utf8.ValidString(repository) || len(repository) > gatetypes.MaxGatePolicyRepositoryBytes ||
		!githubRepositoryPattern.MatchString(repository) {
		return fmt.Errorf(
			"reviews.attention repository %q must be a trimmed owner/repo name of at most %d bytes",
			repository,
			gatetypes.MaxGatePolicyRepositoryBytes,
		)
	}
	folded := strings.ToLower(repository)
	if previous, exists := foldedRepositories[folded]; exists {
		return fmt.Errorf(
			"reviews.attention repositories %q and %q differ only by case",
			previous,
			repository,
		)
	}
	foldedRepositories[folded] = repository
	return nil
}

// NamedRuleSets returns a detached normalized catalog. Legacy repository
// overlays are materialized into effective reusable sets so callers never need
// to understand the retired two-layer representation.
func (config ReviewAttentionConfig) NamedRuleSets() (ReviewAttentionConfig, error) {
	if config.UsesNamedRuleSets() {
		if err := config.Validate(); err != nil {
			return ReviewAttentionConfig{}, err
		}
		return cloneNamedReviewAttentionConfig(config)
	}
	if err := config.Validate(); err != nil {
		return ReviewAttentionConfig{}, err
	}
	normalized := ReviewAttentionConfig{
		RuleSets: map[string]ReviewAttentionRuleSet{
			DefaultReviewAttentionRuleSetID: {
				Name:  DefaultReviewAttentionRuleSetName,
				Rules: cloneReviewAttentionRules(config.Global),
			},
		},
		DefaultRuleSetID:      DefaultReviewAttentionRuleSetID,
		RepositoryAssignments: make(map[string]string),
	}
	usedIDs := map[string]struct{}{DefaultReviewAttentionRuleSetID: {}}
	importedRuleSets := make(map[string]string)
	for _, repository := range sortedReviewAttentionKeys(config.Repositories) {
		policies := config.Repositories[repository]
		pureInherit := true
		decisions := make(map[string]struct{}, len(config.Global)+len(policies))
		for decision := range config.Global {
			decisions[decision] = struct{}{}
		}
		for decision, policy := range policies {
			decisions[decision] = struct{}{}
			if policy.Mode != gatetypes.GatePolicyInherit {
				pureInherit = false
			}
		}
		if pureInherit {
			continue
		}
		rules := make(map[string][]gatetypes.GateSpec, len(decisions))
		for _, decision := range sortedReviewAttentionKeys(decisions) {
			policy, configured := policies[decision]
			effective, err := resolveLegacyReviewAttentionPolicy(
				config.Global[decision],
				policy,
				configured,
				decision,
			)
			if err != nil {
				return ReviewAttentionConfig{}, err
			}
			rules[decision] = effective
		}
		encodedRules, err := json.Marshal(rules)
		if err != nil {
			return ReviewAttentionConfig{}, fmt.Errorf(
				"encode legacy review attention rules for %q: %w",
				repository,
				err,
			)
		}
		if id, exists := importedRuleSets[string(encodedRules)]; exists {
			normalized.RepositoryAssignments[repository] = id
			continue
		}
		id := legacyReviewAttentionRuleSetID(encodedRules, usedIDs)
		usedIDs[id] = struct{}{}
		importedRuleSets[string(encodedRules)] = id
		normalized.RuleSets[id] = ReviewAttentionRuleSet{
			Name:  "Imported " + id,
			Rules: rules,
		}
		normalized.RepositoryAssignments[repository] = id
	}
	if err := normalized.Validate(); err != nil {
		return ReviewAttentionConfig{}, fmt.Errorf(
			"%w: %v",
			ErrReviewAttentionLegacyMigrationExceedsBounds,
			err,
		)
	}
	return normalized, nil
}

func resolveLegacyReviewAttentionPolicy(
	global []gatetypes.GateSpec,
	policy gatetypes.RepositoryGatePolicy,
	hasPolicies bool,
	decision string,
) ([]gatetypes.GateSpec, error) {
	if !hasPolicies {
		return cloneReviewAttentionGates(global), nil
	}
	// The zero value is not a configured policy. Callers distinguish it by
	// checking whether the exact decision exists in the repository map.
	if policy.Mode == "" {
		return cloneReviewAttentionGates(global), nil
	}
	switch policy.Mode {
	case gatetypes.GatePolicyInherit:
		return cloneReviewAttentionGates(global), nil
	case gatetypes.GatePolicyDisable:
		return []gatetypes.GateSpec{}, nil
	case gatetypes.GatePolicyReplace:
		return cloneReviewAttentionGates(policy.Gates), nil
	case gatetypes.GatePolicyOverlay:
		effective := cloneReviewAttentionGates(global)
		positions := make(map[string]int, len(effective))
		for index, gate := range effective {
			positions[gate.ID] = index
		}
		for _, gate := range cloneReviewAttentionGates(policy.Gates) {
			if index, exists := positions[gate.ID]; exists {
				effective[index] = gate
				continue
			}
			positions[gate.ID] = len(effective)
			effective = append(effective, gate)
		}
		return effective, nil
	default:
		return nil, fmt.Errorf("legacy review attention policy %q has unsupported mode %q", decision, policy.Mode)
	}
}

func legacyReviewAttentionRuleSetID(
	encodedRules []byte,
	used map[string]struct{},
) string {
	digest := sha256.Sum256(encodedRules)
	base := "legacy_" + hex.EncodeToString(digest[:8])
	for suffix := 0; ; suffix++ {
		candidate := base
		if suffix != 0 {
			candidate = fmt.Sprintf("%s_%d", base, suffix)
		}
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

func cloneNamedReviewAttentionConfig(
	config ReviewAttentionConfig,
) (ReviewAttentionConfig, error) {
	encoded, err := json.Marshal(config)
	if err != nil {
		return ReviewAttentionConfig{}, err
	}
	var cloned ReviewAttentionConfig
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	if err = decoder.Decode(&cloned); err != nil {
		return ReviewAttentionConfig{}, err
	}
	if cloned.RuleSets == nil {
		cloned.RuleSets = make(map[string]ReviewAttentionRuleSet)
	}
	if cloned.RepositoryAssignments == nil {
		cloned.RepositoryAssignments = make(map[string]string)
	}
	return cloned, nil
}

func cloneReviewAttentionRules(
	rules map[string][]gatetypes.GateSpec,
) map[string][]gatetypes.GateSpec {
	cloned := make(map[string][]gatetypes.GateSpec, len(rules))
	for decision, gates := range rules {
		cloned[decision] = cloneReviewAttentionGates(gates)
	}
	return cloned
}

func cloneReviewAttentionGates(gates []gatetypes.GateSpec) []gatetypes.GateSpec {
	encoded, err := json.Marshal(gates)
	if err != nil {
		return nil
	}
	var cloned []gatetypes.GateSpec
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	if err = decoder.Decode(&cloned); err != nil {
		return nil
	}
	if cloned == nil {
		return []gatetypes.GateSpec{}
	}
	return cloned
}

func validateReviewAttentionDecisionPoint(value string) error {
	if value != strings.TrimSpace(value) || !reviewAttentionDecisionPointPattern.MatchString(value) ||
		len(value) > MaxReviewAttentionDecisionPointBytes {
		return fmt.Errorf(
			"decision point %q must match %s and be at most %d bytes",
			value,
			reviewAttentionDecisionPointPattern.String(),
			MaxReviewAttentionDecisionPointBytes,
		)
	}
	return nil
}

func validateReviewAttentionRepositoryPolicy(
	path string,
	policy gatetypes.RepositoryGatePolicy,
) error {
	switch policy.Mode {
	case gatetypes.GatePolicyInherit, gatetypes.GatePolicyDisable:
		if len(policy.Gates) != 0 {
			return fmt.Errorf("%s mode %q cannot configure gates", path, policy.Mode)
		}
	case gatetypes.GatePolicyOverlay, gatetypes.GatePolicyReplace:
		if len(policy.Gates) == 0 {
			return fmt.Errorf("%s mode %q requires at least one gate", path, policy.Mode)
		}
	default:
		return fmt.Errorf("%s has unsupported mode %q", path, policy.Mode)
	}
	return validateReviewAttentionGateList(path+".gates", policy.Gates)
}

func validateReviewAttentionEffectivePolicy(
	path string,
	global []gatetypes.GateSpec,
	policy gatetypes.RepositoryGatePolicy,
) error {
	if policy.Mode != gatetypes.GatePolicyOverlay {
		return nil
	}
	effective := append([]gatetypes.GateSpec(nil), global...)
	positions := make(map[string]int, len(effective))
	for index, gate := range effective {
		positions[gate.ID] = index
	}
	for _, gate := range policy.Gates {
		if index, exists := positions[gate.ID]; exists {
			effective[index] = gate
			continue
		}
		if len(effective) == gatetypes.MaxWorkflowGateCount {
			return fmt.Errorf(
				"%s effective policy exceeds %d gates",
				path,
				gatetypes.MaxWorkflowGateCount,
			)
		}
		positions[gate.ID] = len(effective)
		effective = append(effective, gate)
	}
	workingAgent := ""
	for _, gate := range effective {
		if gate.Kind != gatetypes.GateAIWorkingContext {
			continue
		}
		if workingAgent != "" && workingAgent != gate.AgentID {
			return fmt.Errorf("%s effective working-context gates must use one agent", path)
		}
		workingAgent = gate.AgentID
	}
	return nil
}

func validateReviewAttentionGateList(path string, gates []gatetypes.GateSpec) error {
	if len(gates) > gatetypes.MaxWorkflowGateCount {
		return fmt.Errorf(
			"%s must contain at most %d gates",
			path,
			gatetypes.MaxWorkflowGateCount,
		)
	}
	seen := make(map[string]struct{}, len(gates))
	workingAgent := ""
	for index, gate := range gates {
		gatePath := fmt.Sprintf("%s[%d]", path, index)
		if err := validateReviewAttentionGate(gatePath, gate); err != nil {
			return err
		}
		if _, exists := seen[gate.ID]; exists {
			return fmt.Errorf("%s duplicates gate ID %q", path, gate.ID)
		}
		seen[gate.ID] = struct{}{}
		if gate.Kind == gatetypes.GateAIWorkingContext {
			if workingAgent != "" && workingAgent != gate.AgentID {
				return fmt.Errorf("%s working-context gates must use one agent", path)
			}
			workingAgent = gate.AgentID
		}
	}
	return nil
}

func validateReviewAttentionGate(path string, gate gatetypes.GateSpec) error {
	if gate.ID != strings.TrimSpace(gate.ID) || !reviewAttentionGateIDPattern.MatchString(gate.ID) ||
		len(gate.ID) > gatetypes.MaxWorkflowGateIDBytes {
		return fmt.Errorf(
			"%s.id must match %s and be at most %d bytes",
			path,
			reviewAttentionGateIDPattern.String(),
			gatetypes.MaxWorkflowGateIDBytes,
		)
	}
	switch gate.Kind {
	case gatetypes.GateAIWorkingContext, gatetypes.GateAIIsolatedContext:
		if gate.AgentID != strings.TrimSpace(gate.AgentID) ||
			!reviewAttentionAgentIDPattern.MatchString(gate.AgentID) {
			return fmt.Errorf("%s.agent_id must be an exact canonical agent ID", path)
		}
		if err := validateReviewAttentionText(
			path+".criteria",
			gate.Criteria,
			gatetypes.MaxWorkflowGateCriteriaBytes,
		); err != nil {
			return err
		}
		if err := validateReviewAttentionTitle(path+".title", gate.Title); err != nil {
			return err
		}
		if gate.When != "" {
			return fmt.Errorf("%s.when is only supported for deterministic gates", path)
		}
	case gatetypes.GateDeterministic:
		if gate.AgentID != "" || gate.Criteria != "" {
			return fmt.Errorf("%s deterministic gate cannot configure agent_id or criteria", path)
		}
		if err := validateReviewAttentionTitle(path+".title", gate.Title); err != nil {
			return err
		}
		if strings.TrimSpace(gate.When) == "" || !utf8.ValidString(gate.When) ||
			len(gate.When) > gatetypes.MaxWorkflowGateConditionBytes {
			return fmt.Errorf(
				"%s.when must be nonblank valid UTF-8 and at most %d bytes",
				path,
				gatetypes.MaxWorkflowGateConditionBytes,
			)
		}
		if err := validateReviewAttentionCondition(gate.When); err != nil {
			return fmt.Errorf("%s.when: %w", path, err)
		}
		if gate.Questions == nil {
			return fmt.Errorf("%s.questions are required", path)
		}
	case gatetypes.GateZero:
		if gate.AgentID != "" || gate.Criteria != "" || gate.When != "" || gate.Title != "" ||
			gate.Questions != nil {
			return fmt.Errorf("%s zero gate cannot configure behavior fields", path)
		}
	default:
		return fmt.Errorf("%s.kind %q is unsupported", path, gate.Kind)
	}
	if gate.Questions != nil {
		if err := validateReviewAttentionJSONValue(path+".questions", gate.Questions); err != nil {
			return fmt.Errorf("%s.questions must contain durable JSON: %w", path, err)
		}
		encoded, err := marshalReviewAttentionValue(gate.Questions)
		if err != nil {
			return fmt.Errorf("%s.questions must contain durable JSON: %w", path, err)
		}
		if gate.Kind == gatetypes.GateDeterministic && string(encoded) == "null" {
			return fmt.Errorf("%s.questions are required", path)
		}
		if len(encoded) > gatetypes.MaxWorkflowGateQuestionBytes {
			return fmt.Errorf(
				"%s.questions exceeds %d encoded bytes",
				path,
				gatetypes.MaxWorkflowGateQuestionBytes,
			)
		}
	}
	return nil
}

func validateReviewAttentionCondition(value string) error {
	condition := strings.TrimSpace(value)
	if strings.HasPrefix(condition, "${{") || strings.HasSuffix(condition, "}}") {
		if !strings.HasPrefix(condition, "${{") || !strings.HasSuffix(condition, "}}") {
			return fmt.Errorf("expression delimiters are incomplete")
		}
		condition = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(condition, "${{"), "}}"))
	}
	return validateReviewAttentionConditionOperands(condition)
}

func validateReviewAttentionConditionOperands(expression string) error {
	expression = strings.TrimSpace(expression)
	for _, operator := range []string{" == ", " != ", " >= ", " <= ", " > ", " < "} {
		if index := strings.Index(expression, operator); index >= 0 {
			if err := validateReviewAttentionConditionOperands(expression[:index]); err != nil {
				return err
			}
			return validateReviewAttentionConditionOperands(expression[index+len(operator):])
		}
	}
	if strings.HasPrefix(expression, "not ") {
		return validateReviewAttentionConditionOperands(
			strings.TrimSpace(strings.TrimPrefix(expression, "not ")),
		)
	}
	if len(expression) >= 2 &&
		((strings.HasPrefix(expression, "'") && strings.HasSuffix(expression, "'")) ||
			(strings.HasPrefix(expression, `"`) && strings.HasSuffix(expression, `"`))) {
		return nil
	}
	switch expression {
	case "true", "false", "null":
		return nil
	}
	if _, err := strconv.ParseFloat(expression, 64); err == nil {
		return nil
	}
	if !reviewAttentionExpressionPathPattern.MatchString(expression) {
		return fmt.Errorf("unsupported expression syntax %q", expression)
	}
	root := strings.SplitN(expression, ".", 2)[0]
	if root != "inputs" {
		return fmt.Errorf("deterministic gate expression root %q is unsupported; use inputs", root)
	}
	return nil
}

func validateReviewAttentionTitle(path, value string) error {
	return validateReviewAttentionText(path, value, gatetypes.MaxWorkflowGateTitleBytes)
}

func validateReviewAttentionText(path, value string, maximum int) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || len(value) > maximum {
		return fmt.Errorf(
			"%s must be nonblank valid UTF-8 and at most %d bytes",
			path,
			maximum,
		)
	}
	return nil
}

func sortedReviewAttentionKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type reviewAttentionJSONVisit struct {
	kind    reflect.Kind
	pointer uintptr
}

type reviewAttentionJSONBudget struct {
	nodes  int
	active map[reviewAttentionJSONVisit]struct{}
}

func validateReviewAttentionJSONValue(path string, value any) error {
	budget := &reviewAttentionJSONBudget{
		active: make(map[reviewAttentionJSONVisit]struct{}),
	}
	return budget.visit(path, reflect.ValueOf(value), 0)
}

func (budget *reviewAttentionJSONBudget) visit(
	path string,
	value reflect.Value,
	depth int,
) error {
	if depth > gatetypes.MaxWorkflowGateJSONDepth {
		return fmt.Errorf("%s exceeds JSON depth %d", path, gatetypes.MaxWorkflowGateJSONDepth)
	}
	budget.nodes++
	if budget.nodes > gatetypes.MaxWorkflowGateJSONNodes {
		return fmt.Errorf("%s exceeds %d JSON nodes", path, gatetypes.MaxWorkflowGateJSONNodes)
	}
	if !value.IsValid() {
		return nil
	}
	for value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if reviewAttentionHasCustomMarshaler(value.Type()) {
		return fmt.Errorf(
			"%s contains custom marshaler type %s; use JSON maps, arrays, and scalars",
			path,
			value.Type(),
		)
	}

	switch value.Kind() {
	case reflect.Bool:
		return nil
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return fmt.Errorf("%s contains invalid UTF-8", path)
		}
		if value.Type() == reviewAttentionJSONNumberType {
			if _, err := json.Marshal(json.Number(value.String())); err != nil {
				return fmt.Errorf("%s contains an invalid JSON number", path)
			}
		}
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr:
		return nil
	case reflect.Float32, reflect.Float64:
		number := value.Float()
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return fmt.Errorf("%s contains a non-finite number", path)
		}
		return nil
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		if value.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("%s contains a map with non-string keys", path)
		}
		if reviewAttentionHasCustomMarshaler(value.Type().Key()) {
			return fmt.Errorf("%s contains a custom-marshaler map key", path)
		}
		leave, err := budget.enter(path, value)
		if err != nil {
			return err
		}
		defer leave()
		iterator := value.MapRange()
		for iterator.Next() {
			key := iterator.Key().String()
			if !utf8.ValidString(key) {
				return fmt.Errorf("%s contains an invalid UTF-8 map key", path)
			}
			if err := budget.visit(path, iterator.Value(), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice:
		if value.IsNil() {
			return nil
		}
		if value.Type().Elem().Kind() == reflect.Uint8 &&
			!reviewAttentionHasCustomMarshaler(value.Type().Elem()) {
			return nil
		}
		leave, err := budget.enter(path, value)
		if err != nil {
			return err
		}
		defer leave()
		for index := 0; index < value.Len(); index++ {
			if err := budget.visit(path, value.Index(index), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := budget.visit(path, value.Index(index), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Pointer:
		if value.IsNil() {
			return nil
		}
		return fmt.Errorf("%s contains a pointer; use JSON maps, arrays, and scalars", path)
	default:
		return fmt.Errorf("%s contains unsupported JSON type %s", path, value.Type())
	}
}

func reviewAttentionHasCustomMarshaler(valueType reflect.Type) bool {
	if valueType.Implements(reviewAttentionJSONMarshalerType) ||
		valueType.Implements(reviewAttentionTextMarshalerType) {
		return true
	}
	return valueType.Kind() != reflect.Pointer &&
		(reflect.PointerTo(valueType).Implements(reviewAttentionJSONMarshalerType) ||
			reflect.PointerTo(valueType).Implements(reviewAttentionTextMarshalerType))
}

func (budget *reviewAttentionJSONBudget) enter(
	path string,
	value reflect.Value,
) (func(), error) {
	visit := reviewAttentionJSONVisit{kind: value.Kind(), pointer: value.Pointer()}
	if _, exists := budget.active[visit]; exists {
		return nil, fmt.Errorf("%s must be acyclic JSON", path)
	}
	budget.active[visit] = struct{}{}
	return func() { delete(budget.active, visit) }, nil
}

func marshalReviewAttentionValue(value any) (encoded []byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			encoded = nil
			err = fmt.Errorf("JSON encoding panicked")
		}
	}()
	return json.Marshal(value)
}
