package gatetypes

import (
	"encoding/json"
	"sort"
	"strings"
)

// gatePolicyCatalogFormatV2 domain-separates the canonical representation used
// for PR-lifecycle gate-catalog sizing and revision hashes.
const gatePolicyCatalogFormatV2 = "pr-lifecycle-gate-catalog/v2"

type canonicalGatePolicyDecision struct {
	DecisionPoint string     `json:"decision_point"`
	Gates         []GateSpec `json:"gates"`
}

type canonicalRepositoryGatePolicyDecision struct {
	DecisionPoint string               `json:"decision_point"`
	Policy        RepositoryGatePolicy `json:"policy"`
}

type canonicalRepositoryGatePolicyCatalog struct {
	Repository string                                  `json:"repository"`
	Policies   []canonicalRepositoryGatePolicyDecision `json:"policies"`
}

type canonicalGatePolicyCatalog struct {
	Format       string                                 `json:"format"`
	Global       []canonicalGatePolicyDecision          `json:"global"`
	Repositories []canonicalRepositoryGatePolicyCatalog `json:"repositories"`
}

func newCanonicalGatePolicyCatalog(
	global map[string][]GateSpec,
	repositories map[string]map[string]RepositoryGatePolicy,
) canonicalGatePolicyCatalog {
	catalog := canonicalGatePolicyCatalog{
		Format:       gatePolicyCatalogFormatV2,
		Global:       make([]canonicalGatePolicyDecision, 0, len(global)),
		Repositories: make([]canonicalRepositoryGatePolicyCatalog, 0, len(repositories)),
	}
	for _, decisionPoint := range sortedGatePolicyKeys(global) {
		catalog.Global = append(catalog.Global, canonicalGatePolicyDecision{
			DecisionPoint: decisionPoint,
			Gates:         global[decisionPoint],
		})
	}
	for _, repository := range sortedCanonicalRepositoryKeys(repositories) {
		configured := repositories[repository]
		entry := canonicalRepositoryGatePolicyCatalog{
			Repository: strings.ToLower(repository),
			Policies: make(
				[]canonicalRepositoryGatePolicyDecision,
				0,
				len(configured),
			),
		}
		for _, decisionPoint := range sortedGatePolicyKeys(configured) {
			entry.Policies = append(entry.Policies, canonicalRepositoryGatePolicyDecision{
				DecisionPoint: decisionPoint,
				Policy:        configured[decisionPoint],
			})
		}
		catalog.Repositories = append(catalog.Repositories, entry)
	}
	return catalog
}

func sortedCanonicalRepositoryKeys[T any](values map[string]T) []string {
	keys := sortedGatePolicyKeys(values)
	sort.Slice(keys, func(left, right int) bool {
		leftCanonical := strings.ToLower(keys[left])
		rightCanonical := strings.ToLower(keys[right])
		if leftCanonical == rightCanonical {
			return keys[left] < keys[right]
		}
		return leftCanonical < rightCanonical
	})
	return keys
}

// MarshalCanonicalGatePolicyCatalog encodes the one representation whose byte
// size and hash define the complete catalog contract.
func MarshalCanonicalGatePolicyCatalog(
	global map[string][]GateSpec,
	repositories map[string]map[string]RepositoryGatePolicy,
) ([]byte, error) {
	return json.Marshal(newCanonicalGatePolicyCatalog(global, repositories))
}

func sortedGatePolicyKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
