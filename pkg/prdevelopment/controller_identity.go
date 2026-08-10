package prdevelopment

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const (
	controllerOperationIDPrefix = "pdop_"
	controllerCommitIDPrefix    = "pdcmt_"
	controllerParkIDPrefix      = "pdlnpark_"
	controllerIdentityHexBytes  = 16
)

// controllerAttemptIdentities are caller-durable identities for every local
// side effect in one repair attempt. They are derived from the immutable
// attempt ID, so a restart reconstructs the same write-ahead operation and Git
// intent instead of creating a second commit or Park transition.
type controllerAttemptIdentities struct {
	AdoptOperation  string
	ResumeOperation string
	CommitOperation string
	ParkOperation   string
	CommitIntent    string
	ParkIntent      string
	CIAttestation   string
	CIOwner         string
}

func newControllerAttemptIdentities(attemptID string) (controllerAttemptIdentities, error) {
	if attemptID != strings.TrimSpace(attemptID) || !validControllerAttemptID(attemptID) {
		return controllerAttemptIdentities{}, fmt.Errorf(
			"invalid durable pull request repair attempt ID",
		)
	}
	return controllerAttemptIdentities{
		AdoptOperation:  deterministicControllerID(controllerOperationIDPrefix, attemptID, "adopt-operation"),
		ResumeOperation: deterministicControllerID(controllerOperationIDPrefix, attemptID, "resume-operation"),
		CommitOperation: deterministicControllerID(controllerOperationIDPrefix, attemptID, "commit-operation"),
		ParkOperation:   deterministicControllerID(controllerOperationIDPrefix, attemptID, "park-operation"),
		CommitIntent:    deterministicControllerID(controllerCommitIDPrefix, attemptID, "commit-effect"),
		ParkIntent:      deterministicControllerID(controllerParkIDPrefix, attemptID, "park-effect"),
		CIAttestation:   "pr-development:ci:attestation:" + attemptID,
		CIOwner:         "pr-development:repair-attempt:" + attemptID,
	}, nil
}

func deterministicControllerID(prefix, attemptID, purpose string) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("picoclaw-pr-development-controller-identity-v1\x00"))
	_, _ = digest.Write([]byte(purpose))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(attemptID))
	return prefix + hex.EncodeToString(digest.Sum(nil)[:controllerIdentityHexBytes])
}

func validControllerAttemptID(value string) bool {
	const prefix = "pdr_"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+controllerIdentityHexBytes*2 {
		return false
	}
	for _, character := range value[len(prefix):] {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

// controllerCommitTime makes deterministic Git authorship evidence from the
// immutable attempt creation time while satisfying Git's whole-second fence.
func controllerCommitTime(createdAt time.Time) (time.Time, error) {
	createdAt = createdAt.UTC().Truncate(time.Second)
	if createdAt.IsZero() || createdAt.Year() < 1970 || createdAt.Year() > 9999 {
		return time.Time{}, fmt.Errorf("invalid durable repair attempt creation time")
	}
	return createdAt, nil
}
