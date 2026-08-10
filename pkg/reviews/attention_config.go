package reviews

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/workflows"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

const (
	maxAttentionPolicyDecisionPoints           = gatetypes.MaxGatePolicyDecisionPoints
	maxAttentionPolicyRepositories             = gatetypes.MaxGatePolicyRepositories
	maxAttentionPolicyRepositoryDecisionPoints = gatetypes.MaxGatePolicyDecisionPoints
	maxAttentionPolicyEntries                  = gatetypes.MaxGatePolicyEntries
	maxAttentionPolicyGateEntries              = gatetypes.MaxGatePolicyGateEntries
	maxAttentionPolicyCatalogBytes             = gatetypes.MaxGatePolicyCatalogBytes
	maxAttentionPolicyRepositoryBytes          = gatetypes.MaxGatePolicyRepositoryBytes

	attentionPolicySelectionFormat = "review-attention-selection/v1"
)

// ConfigAttentionPolicySource is an immutable, validated policy catalog. Its
// selected-policy revision intentionally excludes unrelated repositories and
// decision points, while CatalogRevision covers the complete runtime catalog.
type ConfigAttentionPolicySource struct {
	global          map[string][]workflows.GateSpec
	repositories    map[string]map[string]workflows.RepositoryGatePolicy
	agentIDs        []string
	workingAgentIDs []string
	revision        string
}

var _ sharedattention.PolicySource = (*ConfigAttentionPolicySource)(nil)

type canonicalAttentionSelection struct {
	Format           string                          `json:"format"`
	Repository       string                          `json:"repository"`
	DecisionPoint    string                          `json:"decision_point"`
	Global           []workflows.GateSpec            `json:"global"`
	RepositoryPolicy *workflows.RepositoryGatePolicy `json:"repository_policy"`
}

// NewConfigAttentionPolicySource validates and detaches an operator-owned
// global/repository policy catalog. Repository keys are selected
// case-insensitively but case-colliding configured keys are rejected.
func NewConfigAttentionPolicySource(
	global map[string][]workflows.GateSpec,
	repositories map[string]map[string]workflows.RepositoryGatePolicy,
) (*ConfigAttentionPolicySource, error) {
	if len(global) > maxAttentionPolicyDecisionPoints {
		return nil, fmt.Errorf(
			"review attention global catalog exceeds %d decision points",
			maxAttentionPolicyDecisionPoints,
		)
	}
	if len(repositories) > maxAttentionPolicyRepositories {
		return nil, fmt.Errorf(
			"review attention catalog exceeds %d repositories",
			maxAttentionPolicyRepositories,
		)
	}

	source := &ConfigAttentionPolicySource{
		global: make(map[string][]workflows.GateSpec, len(global)),
		repositories: make(
			map[string]map[string]workflows.RepositoryGatePolicy,
			len(repositories),
		),
	}
	gateEntries := 0
	policyEntries := len(global)
	agentIDs := make(map[string]struct{})
	workingAgentIDs := make(map[string]struct{})
	for _, decisionPoint := range sortedAttentionGlobalDecisionPoints(global) {
		if !validAttentionDecisionPoint(decisionPoint) {
			return nil, fmt.Errorf("review attention global decision point is invalid")
		}
		if global[decisionPoint] == nil {
			return nil, fmt.Errorf("review attention global policy must be an array, not null")
		}
		layer, err := cloneAttentionGlobalLayer(global[decisionPoint])
		if err != nil {
			return nil, fmt.Errorf("review attention global policy %q is invalid: %w", decisionPoint, err)
		}
		gateEntries += len(layer)
		if gateEntries > maxAttentionPolicyGateEntries {
			return nil, fmt.Errorf(
				"review attention catalog exceeds %d gate entries",
				maxAttentionPolicyGateEntries,
			)
		}
		collectAttentionAgentIDs(agentIDs, workingAgentIDs, layer)
		source.global[decisionPoint] = layer
	}

	for _, repository := range sortedAttentionRepositories(repositories) {
		if repository != strings.TrimSpace(repository) ||
			len(repository) > maxAttentionPolicyRepositoryBytes ||
			!validRepository(repository) {
			return nil, fmt.Errorf("review attention repository key is invalid")
		}
		canonicalRepository := strings.ToLower(repository)
		if _, duplicate := source.repositories[canonicalRepository]; duplicate {
			return nil, fmt.Errorf(
				"review attention repository keys collide case-insensitively",
			)
		}
		configured := repositories[repository]
		if configured == nil {
			return nil, fmt.Errorf("review attention repository policy map must be an object, not null")
		}
		if len(configured) > maxAttentionPolicyRepositoryDecisionPoints {
			return nil, fmt.Errorf(
				"review attention repository catalog exceeds %d decision points",
				maxAttentionPolicyRepositoryDecisionPoints,
			)
		}
		policyEntries += len(configured)
		if policyEntries > maxAttentionPolicyEntries {
			return nil, fmt.Errorf(
				"review attention catalog exceeds %d policies",
				maxAttentionPolicyEntries,
			)
		}
		policies := make(
			map[string]workflows.RepositoryGatePolicy,
			len(configured),
		)
		for _, decisionPoint := range sortedAttentionRepositoryDecisionPoints(configured) {
			if !validAttentionDecisionPoint(decisionPoint) {
				return nil, fmt.Errorf("review attention repository decision point is invalid")
			}
			policy, err := cloneAttentionRepositoryPolicy(configured[decisionPoint])
			if err != nil {
				return nil, fmt.Errorf(
					"review attention repository policy %q/%q is invalid: %w",
					repository,
					decisionPoint,
					err,
				)
			}
			gateEntries += len(policy.Gates)
			if gateEntries > maxAttentionPolicyGateEntries {
				return nil, fmt.Errorf(
					"review attention catalog exceeds %d gate entries",
					maxAttentionPolicyGateEntries,
				)
			}
			if _, err = workflows.ResolveGatePolicy(
				source.global[decisionPoint],
				&policy,
			); err != nil {
				return nil, fmt.Errorf(
					"review attention effective policy %q/%q is invalid: %w",
					repository,
					decisionPoint,
					err,
				)
			}
			collectAttentionAgentIDs(agentIDs, workingAgentIDs, policy.Gates)
			policies[decisionPoint] = policy
		}
		source.repositories[canonicalRepository] = policies
	}

	source.agentIDs = make([]string, 0, len(agentIDs))
	for agentID := range agentIDs {
		source.agentIDs = append(source.agentIDs, agentID)
	}
	sort.Strings(source.agentIDs)
	source.workingAgentIDs = make([]string, 0, len(workingAgentIDs))
	for agentID := range workingAgentIDs {
		source.workingAgentIDs = append(source.workingAgentIDs, agentID)
	}
	sort.Strings(source.workingAgentIDs)
	encodedCatalog, err := gatetypes.MarshalCanonicalGatePolicyCatalog(
		source.global,
		source.repositories,
	)
	if err != nil {
		return nil, fmt.Errorf("encode review attention catalog: %w", err)
	}
	if len(encodedCatalog) > maxAttentionPolicyCatalogBytes {
		return nil, fmt.Errorf(
			"review attention catalog exceeds %d encoded bytes",
			maxAttentionPolicyCatalogBytes,
		)
	}
	source.revision = hashAttentionPolicyBytes(encodedCatalog)
	return source, nil
}

// CatalogRevision is a content revision for the complete canonical catalog.
func (source *ConfigAttentionPolicySource) CatalogRevision() string {
	if source == nil {
		return ""
	}
	return source.revision
}

// AgentIDs returns every configured AI gate agent in stable order.
func (source *ConfigAttentionPolicySource) AgentIDs() []string {
	if source == nil {
		return nil
	}
	return append([]string(nil), source.agentIDs...)
}

// WorkingContextAgentIDs returns every AI gate agent that must own a durable
// session store for exact review-chat projection.
func (source *ConfigAttentionPolicySource) WorkingContextAgentIDs() []string {
	if source == nil {
		return nil
	}
	return append([]string(nil), source.workingAgentIDs...)
}

// WithReviewAttentionPolicy selects one immutable policy and hands a detached
// snapshot to use synchronously. The selector never supplies policy material.
func (source *ConfigAttentionPolicySource) WithReviewAttentionPolicy(
	ctx context.Context,
	selector AttentionPolicySelector,
	use AttentionPolicyUse,
) error {
	if source == nil {
		return ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if selector.Repository != strings.TrimSpace(selector.Repository) ||
		len(selector.Repository) > maxWorkingContextRepositoryBytes ||
		!validRepository(selector.Repository) ||
		!validAttentionDecisionPoint(selector.DecisionPoint) {
		return ErrInvalidRequest
	}
	if use == nil {
		return errors.New("review attention policy callback is required")
	}
	global, err := cloneAttentionGlobalLayer(source.global[selector.DecisionPoint])
	if err != nil {
		return ErrUnavailable
	}
	canonicalRepository := strings.ToLower(selector.Repository)
	var repository *workflows.RepositoryGatePolicy
	if policies := source.repositories[canonicalRepository]; policies != nil {
		if configured, exists := policies[selector.DecisionPoint]; exists {
			cloned, cloneErr := cloneAttentionRepositoryPolicy(configured)
			if cloneErr != nil {
				return ErrUnavailable
			}
			repository = &cloned
		}
	}
	revision, _, err := hashAttentionPolicyValue(canonicalAttentionSelection{
		Format:           attentionPolicySelectionFormat,
		Repository:       canonicalRepository,
		DecisionPoint:    selector.DecisionPoint,
		Global:           global,
		RepositoryPolicy: repository,
	})
	if err != nil {
		return ErrUnavailable
	}
	return use(ctx, AttentionPolicySnapshot{
		Revision:   revision,
		Global:     global,
		Repository: repository,
	})
}

// WithAttentionPolicy adapts this established global/repository catalog to the
// domain-neutral attention workflow core. The legacy review source method
// remains unchanged for custom review integrations.
func (source *ConfigAttentionPolicySource) WithAttentionPolicy(
	ctx context.Context,
	selector sharedattention.PolicySelector,
	use sharedattention.PolicyUse,
) error {
	if use == nil {
		return errors.New("attention policy callback is required")
	}
	return source.WithReviewAttentionPolicy(
		ctx,
		AttentionPolicySelector{
			Repository:    selector.Repository,
			DecisionPoint: selector.DecisionPoint,
		},
		func(policyCtx context.Context, snapshot AttentionPolicySnapshot) error {
			return use(policyCtx, sharedattention.PolicySnapshot{
				Revision:   snapshot.Revision,
				Global:     snapshot.Global,
				Repository: snapshot.Repository,
			})
		},
	)
}

func validAttentionDecisionPoint(value string) bool {
	return value == strings.TrimSpace(value) &&
		len(value) <= maxAttentionDecisionBytes &&
		attentionDecisionPattern.MatchString(value)
}

func cloneAttentionGlobalLayer(
	layer []workflows.GateSpec,
) ([]workflows.GateSpec, error) {
	resolution, err := workflows.ResolveGatePolicy(layer, nil)
	if err != nil {
		return nil, err
	}
	return resolution.Effective, nil
}

func cloneAttentionRepositoryPolicy(
	policy workflows.RepositoryGatePolicy,
) (workflows.RepositoryGatePolicy, error) {
	resolution, err := workflows.ResolveGatePolicy(nil, &policy)
	if err != nil {
		return workflows.RepositoryGatePolicy{}, err
	}
	cloned := workflows.RepositoryGatePolicy{Mode: policy.Mode}
	switch policy.Mode {
	case workflows.GatePolicyOverlay, workflows.GatePolicyReplace:
		cloned.Gates = resolution.Effective
	case workflows.GatePolicyInherit, workflows.GatePolicyDisable:
		cloned.Gates = nil
	}
	return cloned, nil
}

func collectAttentionAgentIDs(
	destination map[string]struct{},
	working map[string]struct{},
	gates []workflows.GateSpec,
) {
	for _, gate := range gates {
		switch gate.Kind {
		case workflows.GateAIWorkingContext:
			destination[gate.AgentID] = struct{}{}
			working[gate.AgentID] = struct{}{}
		case workflows.GateAIIsolatedContext:
			destination[gate.AgentID] = struct{}{}
		}
	}
}

func sortedAttentionGlobalDecisionPoints(
	global map[string][]workflows.GateSpec,
) []string {
	keys := make([]string, 0, len(global))
	for decisionPoint := range global {
		keys = append(keys, decisionPoint)
	}
	sort.Strings(keys)
	return keys
}

func sortedAttentionRepositories(
	repositories map[string]map[string]workflows.RepositoryGatePolicy,
) []string {
	keys := make([]string, 0, len(repositories))
	for repository := range repositories {
		keys = append(keys, repository)
	}
	sort.Strings(keys)
	return keys
}

func sortedAttentionRepositoryDecisionPoints(
	policies map[string]workflows.RepositoryGatePolicy,
) []string {
	keys := make([]string, 0, len(policies))
	for decisionPoint := range policies {
		keys = append(keys, decisionPoint)
	}
	sort.Strings(keys)
	return keys
}

func hashAttentionPolicyValue(value any) (string, int, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", 0, err
	}
	return hashAttentionPolicyBytes(encoded), len(encoded), nil
}

func hashAttentionPolicyBytes(encoded []byte) string {
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}
