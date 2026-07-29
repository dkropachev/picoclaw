package workflows

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"os"
)

const (
	WorkflowTargetRevisionMissing = "missing"
	WorkflowTargetRevisionUnknown = "unknown"
)

var (
	ErrWorkflowSessionRevisionMismatch               = errors.New("workflow development session revision mismatch")
	ErrWorkflowDraftRevisionMismatch                 = errors.New("workflow development draft revision mismatch")
	ErrWorkflowTargetRevisionMismatch                = errors.New("workflow development target revision mismatch")
	ErrWorkflowDevelopmentDependencyRevisionMismatch = errors.New(
		"workflow development dependency revision mismatch",
	)
	ErrWorkflowDevelopmentPublishNotReady = errors.New(
		"workflow development dependencies are not ready",
	)
	ErrWorkflowDevelopmentPublishGateRequired = errors.New(
		"workflow development dependency gate is required",
	)
	ErrWorkflowDevelopmentDraftNotReady = errors.New(
		"workflow development draft is not ready to publish",
	)
)

type WorkflowDevelopmentPublishRequest struct {
	SessionID                  string `json:"session_id"`
	ExpectedSessionRevision    string `json:"expected_session_revision"`
	ExpectedDraftRevision      string `json:"expected_draft_revision"`
	ExpectedBaseTargetRevision string `json:"expected_base_target_revision"`
	ExpectedDependencyRevision string `json:"expected_dependency_revision,omitempty"`
}

// WorkflowDevelopmentPublishGateInput is the exact, parsed draft snapshot
// checked while the workspace mutation lock is held. Implementations must
// treat Workflow as read-only.
type WorkflowDevelopmentPublishGateInput struct {
	WorkflowRef   string    `json:"workflow_ref"`
	DraftRevision string    `json:"draft_revision"`
	YAML          string    `json:"yaml"`
	Workflow      *Workflow `json:"-"`
}

// WorkflowDevelopmentPublishGateResult fences publish to the same dependency
// state that was presented to the author. Revision is opaque to this package.
type WorkflowDevelopmentPublishGateResult struct {
	Revision string `json:"revision"`
	Ready    bool   `json:"ready"`
}

// WorkflowDevelopmentPublishGate rechecks structural and live dependency
// readiness against the exact persisted draft while publish owns the workspace
// mutation lock.
type WorkflowDevelopmentPublishGate func(
	context.Context,
	WorkflowDevelopmentPublishGateInput,
) (WorkflowDevelopmentPublishGateResult, error)

// WorkflowDevelopmentDraftRevision hashes the exact target ref and YAML bytes.
// Length prefixes make the token unambiguous without normalizing either value.
func WorkflowDevelopmentDraftRevision(targetRef string, yaml string) string {
	digest := sha256.New()
	writeWorkflowRevisionPart(digest, []byte(targetRef))
	writeWorkflowRevisionPart(digest, []byte(yaml))
	return workflowRevisionDigest(digest)
}

func workflowContentRevision(data []byte) string {
	digest := sha256.New()
	writeWorkflowRevisionPart(digest, data)
	return workflowRevisionDigest(digest)
}

func refreshWorkflowDevelopmentRevisions(session *WorkflowDevelopmentSession) error {
	if session == nil {
		return ErrNoActiveDevelopment
	}
	previousRevision := session.SessionRevision
	session.DraftRevision = WorkflowDevelopmentDraftRevision(
		session.TargetWorkflowRef,
		session.YAML,
	)
	copySession := *session
	copySession.SessionRevision = ""
	data, err := json.Marshal(copySession)
	if err != nil {
		return fmt.Errorf("encode workflow development revision: %w", err)
	}
	digest := sha256.New()
	writeWorkflowRevisionPart(digest, []byte(previousRevision))
	writeWorkflowRevisionPart(digest, data)
	session.SessionRevision = workflowRevisionDigest(digest)
	return nil
}

func captureWorkflowDevelopmentTargetRevision(
	workspace string,
	ref string,
	opts ...LocalOption,
) (string, error) {
	local := collectLocalOptions(opts...)
	resolved, err := local.resolver(workspace).ResolveLocal(ref)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return WorkflowTargetRevisionMissing, nil
		}
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("workflow target is not a regular file")
	}
	data, err := os.ReadFile(resolved.Path)
	if err != nil {
		return "", err
	}
	return workflowContentRevision(data), nil
}

func checkWorkflowDevelopmentPublishRevisions(
	session *WorkflowDevelopmentSession,
	request WorkflowDevelopmentPublishRequest,
	currentTargetRevision string,
) error {
	if session == nil {
		return ErrNoActiveDevelopment
	}
	if request.SessionID == "" || request.SessionID != session.ID ||
		request.ExpectedSessionRevision == "" ||
		request.ExpectedSessionRevision != session.SessionRevision {
		return ErrWorkflowSessionRevisionMismatch
	}
	if request.ExpectedDraftRevision == "" ||
		request.ExpectedDraftRevision != session.DraftRevision {
		return ErrWorkflowDraftRevisionMismatch
	}
	if request.ExpectedBaseTargetRevision == "" ||
		request.ExpectedBaseTargetRevision != session.BaseTargetRevision ||
		currentTargetRevision != session.BaseTargetRevision {
		return ErrWorkflowTargetRevisionMismatch
	}
	return nil
}

func writeWorkflowRevisionPart(digest hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(value)
}

func workflowRevisionDigest(digest hash.Hash) string {
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}
