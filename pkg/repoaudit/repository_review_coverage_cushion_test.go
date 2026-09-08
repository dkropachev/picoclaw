package repoaudit

import (
	"errors"
	"testing"
)

func TestRepositoryReviewCoverageCushionSeams(t *testing.T) {
	if _, err := decodeLegacyAutomationPriceMetadata([]byte(`{`)); err == nil {
		t.Fatal("malformed legacy automation metadata was accepted")
	}
	if err := validateEncodedAutomationSize(make([]byte, maxAutomationFileBytes+1)); err == nil {
		t.Fatal("oversized encoded automation was accepted")
	}
	allowed, err := repositoryReviewGuardBooleanResult(knownGuardNumber(1))
	if allowed || !errors.Is(err, ErrInvalidRepositoryReviewGuardExpression) {
		t.Fatalf("numeric guard result=(%v, %v)", allowed, err)
	}
}
