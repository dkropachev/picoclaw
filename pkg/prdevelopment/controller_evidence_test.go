package prdevelopment

import (
	"regexp"
	"testing"
)

func TestControllerEvidenceDigestsAreStableAndDomainSeparated(t *testing.T) {
	t.Parallel()
	contextDigest := controllerContextDigest("same payload")
	modelDigest := controllerModelResultDigest("same payload", "workspace", 1)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(contextDigest) ||
		!regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(modelDigest) ||
		contextDigest == modelDigest || contextDigest != controllerContextDigest("same payload") {
		t.Fatalf("context digest = %q, model digest = %q", contextDigest, modelDigest)
	}
	if controllerContextDigest("ab") == controllerContextDigest("a"+"b\x00") {
		t.Fatal("length-prefixed context digest was ambiguous")
	}
	if modelDigest == controllerModelResultDigest("same payload", "workspace", 2) ||
		modelDigest == controllerModelResultDigest("same payload", "other", 1) {
		t.Fatal("model result digest did not bind iterations and workspace")
	}
}

func TestControllerCommitMessageBindsAttemptOrdinal(t *testing.T) {
	t.Parallel()
	first, err := controllerCommitMessage(0)
	if err != nil || first != "Apply PR review repair attempt 1" {
		t.Fatalf("controllerCommitMessage(0) = %q, %v", first, err)
	}
	second, err := controllerCommitMessage(1)
	if err != nil || second == first {
		t.Fatalf("controllerCommitMessage(1) = %q, %v", second, err)
	}
	for _, ordinal := range []int{-1, 8192} {
		if message, messageErr := controllerCommitMessage(ordinal); messageErr == nil {
			t.Fatalf("controllerCommitMessage(%d) = %q, nil", ordinal, message)
		}
	}
}
