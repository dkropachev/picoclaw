package workflows

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	workflowDefinitionIDNamespace   = "workflow-definition"
	workflowDefinitionIDBytes       = sha256.Size
	workflowDefinitionIDEncodedSize = (workflowDefinitionIDBytes*8 + 5) / 6
	maximumWorkflowDefinitionRefLen = 16 << 10
)

var (
	ErrInvalidWorkflowDefinitionID = errors.New("invalid workflow definition id")
	workflowDefinitionIDEncoding   = base64.RawURLEncoding.Strict()
)

// WorkflowDefinitionID returns the backend-owned stable URL identity for one
// exact canonical published workflow ref. Clients must treat the result as
// opaque and must not reconstruct it from the ref.
func WorkflowDefinitionID(ref string) (string, error) {
	if ref == "" || len(ref) > maximumWorkflowDefinitionRefLen ||
		!utf8.ValidString(ref) || strings.ContainsRune(ref, 0) {
		return "", ErrInvalidWorkflowDefinitionID
	}
	canonical, err := CanonicalLocalRef(ref)
	if err != nil || canonical != ref {
		return "", ErrInvalidWorkflowDefinitionID
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(workflowDefinitionIDNamespace))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(ref))
	return workflowDefinitionIDEncoding.EncodeToString(digest.Sum(nil)), nil
}

// ValidWorkflowDefinitionID validates only the canonical wire shape. Resolving
// an ID still requires matching it against bounded server-owned definitions.
func ValidWorkflowDefinitionID(id string) bool {
	if len(id) != workflowDefinitionIDEncodedSize {
		return false
	}
	raw, err := workflowDefinitionIDEncoding.DecodeString(id)
	return err == nil && len(raw) == workflowDefinitionIDBytes &&
		workflowDefinitionIDEncoding.EncodeToString(raw) == id
}

// WorkflowDefinitionIDMatches resolves an opaque ID against one exact
// canonical candidate without decoding an identity from the digest.
func WorkflowDefinitionIDMatches(id, ref string) bool {
	if !ValidWorkflowDefinitionID(id) {
		return false
	}
	candidate, err := WorkflowDefinitionID(ref)
	return err == nil && candidate == id
}
