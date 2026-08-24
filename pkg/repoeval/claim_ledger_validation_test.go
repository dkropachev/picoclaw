package repoeval

import (
	"errors"
	"strings"
	"testing"
)

func TestModelClaimNormalizationTrimsEveryPersistedField(t *testing.T) {
	normalizeModelClaim(nil)

	evaluation := validStoredEvaluationForValidation()
	evaluation.Comparisons = validComparisons()
	evaluation.Comparisons[0].Claims = []ModelClaim{{
		ID:             " claim-001 ",
		Path:           " pkg/service.go ",
		Title:          " Boundary state is accepted ",
		Evidence:       " The predicate accepts zero. ",
		Impact:         " The operation enters an invalid state. ",
		Disposition:    ClaimDisposition(" supported "),
		JudgeRationale: " The predicate is present. ",
	}}
	normalized, err := normalizeEvaluation(evaluation)
	if err != nil {
		t.Fatal(err)
	}
	claim := normalized.Comparisons[0].Claims[0]
	if claim.ID != "claim-001" || claim.Path != "pkg/service.go" ||
		claim.Title != "Boundary state is accepted" ||
		claim.Evidence != "The predicate accepts zero." ||
		claim.Impact != "The operation enters an invalid state." ||
		claim.Disposition != ClaimDispositionSupported ||
		claim.JudgeRationale != "The predicate is present." {
		t.Fatalf("normalized claim = %#v", claim)
	}
}

func TestModelClaimValidationRejectsUnboundedRationaleAndNonCanonicalPath(t *testing.T) {
	valid := validModelClaimForValidation("claim-a")

	invalidRationale := valid
	invalidRationale.JudgeRationale = strings.Repeat("r", maxClaimRationaleBytes+1)
	if err := validateModelClaim(invalidRationale); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("unbounded rationale error = %v", err)
	}

	nonCanonicalPath := valid
	nonCanonicalPath.Path = "pkg//service.go"
	if err := validateModelClaim(nonCanonicalPath); !errors.Is(err, ErrInvalidEvaluation) {
		t.Fatalf("non-canonical path error = %v", err)
	}
}

func TestClaimLedgerValidationRejectsBoundsInvalidClaimsAndDuplicateIDs(t *testing.T) {
	aliases := map[string]struct{}{"model-a": {}, "model-b": {}}
	first := validModelClaimForValidation("claim-a")
	second := validModelClaimForValidation("claim-b")

	tests := map[string]struct {
		ledger  map[string][]ModelClaim
		omitted map[string]int
		aliases map[string]struct{}
		maximum int
	}{
		"too many alias ledgers": {
			ledger:  map[string][]ModelClaim{"model-a": {}, "model-b": {}},
			aliases: map[string]struct{}{"model-a": {}}, maximum: 2,
		},
		"too many total claims": {
			ledger:  map[string][]ModelClaim{"model-a": {first, second}},
			aliases: aliases, maximum: 1,
		},
		"invalid claim": {
			ledger:  map[string][]ModelClaim{"model-a": {{}}},
			aliases: aliases, maximum: 2,
		},
		"duplicate claim ID across models": {
			ledger:  map[string][]ModelClaim{"model-a": {first}, "model-b": {first}},
			aliases: aliases, maximum: 2,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateClaimLedger(test.ledger, test.omitted, test.aliases, test.maximum); !errors.Is(
				err,
				ErrInvalidEvaluation,
			) {
				t.Fatalf("validateClaimLedger() error = %v", err)
			}
		})
	}
}

func TestComparisonClaimValidationRejectsOversizeInvalidAndDuplicateLedgers(t *testing.T) {
	base := validComparisons()
	valid := validModelClaimForValidation("claim-a")

	tests := map[string]func([]ModelComparison){
		"too many claims": func(comparisons []ModelComparison) {
			comparisons[0].Claims = make([]ModelClaim, maxClaimsPerModel+1)
		},
		"invalid claim": func(comparisons []ModelComparison) {
			comparisons[0].Claims = []ModelClaim{{}}
		},
		"duplicate claim ID": func(comparisons []ModelComparison) {
			comparisons[0].Claims = []ModelClaim{valid, valid}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			comparisons := Clone(Evaluation{Comparisons: base}).Comparisons
			mutate(comparisons)
			if err := validateComparisons(
				comparisons,
				[]string{"model-a", "model-b"},
				StatusCompleted,
			); !errors.Is(err, ErrInvalidEvaluation) {
				t.Fatalf("validateComparisons() error = %v", err)
			}
		})
	}
}

func validModelClaimForValidation(id string) ModelClaim {
	return ModelClaim{
		ID:             id,
		Path:           "pkg/service.go",
		Title:          "Boundary state is accepted",
		Evidence:       "The exact predicate accepts the invalid boundary.",
		Impact:         "The operation enters an invalid state.",
		Disposition:    ClaimDispositionSupported,
		JudgeRationale: "The predicate and state transition are present in source.",
	}
}
