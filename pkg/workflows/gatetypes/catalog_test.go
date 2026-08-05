package gatetypes

import (
	"strings"
	"testing"
)

func TestCanonicalGatePolicyCatalogSortsAndNormalizesRepositoryCase(t *testing.T) {
	global := map[string][]GateSpec{
		"review.z": {{ID: "z", Kind: GateZero}},
		"review.a": {{ID: "a", Kind: GateZero}},
	}
	repositories := map[string]map[string]RepositoryGatePolicy{
		"Zed/Repo": {
			"review.z": {Mode: GatePolicyDisable},
			"review.a": {Mode: GatePolicyInherit},
		},
		"acme/Repo": {},
	}

	encoded, err := MarshalCanonicalGatePolicyCatalog(global, repositories)
	if err != nil {
		t.Fatalf("MarshalCanonicalGatePolicyCatalog() error = %v", err)
	}
	text := string(encoded)
	for _, expected := range []string{
		`"format":"review-attention-catalog/v1"`,
		`"repository":"acme/repo"`,
		`"repository":"zed/repo"`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("canonical catalog %s does not contain %s", text, expected)
		}
	}
	if strings.Index(text, `"decision_point":"review.a"`) >
		strings.Index(text, `"decision_point":"review.z"`) ||
		strings.Index(text, `"repository":"acme/repo"`) >
			strings.Index(text, `"repository":"zed/repo"`) {
		t.Fatalf("canonical catalog is not sorted: %s", text)
	}
}
