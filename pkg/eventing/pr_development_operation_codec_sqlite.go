//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"
)

// These wire values are deliberately private to the eventing database. The
// exported operation structs all use json:"-" so an accidental HTTP or model
// projection cannot serialize controller authority or local Git evidence.
type prDevelopmentOperationRequestWire struct {
	Repository           string `json:"repository"`
	SourceRef            string `json:"source_ref"`
	SourceCommit         string `json:"source_commit"`
	AgentID              string `json:"agent_id"`
	WorkspaceID          string `json:"workspace_id"`
	LineID               string `json:"line_id"`
	ExpectedTree         string `json:"expected_tree"`
	ExpectedVersion      int64  `json:"expected_version"`
	ExpectedEpoch        int64  `json:"expected_epoch"`
	ExpectedTip          string `json:"expected_tip"`
	EffectIntentID       string `json:"effect_intent_id"`
	ExpectedParent       string `json:"expected_parent"`
	CandidateDigest      string `json:"candidate_digest"`
	CommitMessage        string `json:"commit_message"`
	AuthoredAtUnix       int64  `json:"authored_at_unix"`
	MutationEpoch        int64  `json:"mutation_epoch"`
	PreviousTip          string `json:"previous_tip"`
	Tip                  string `json:"tip"`
	Tree                 string `json:"tree"`
	NoChanges            bool   `json:"no_changes"`
	CompletionSummary    string `json:"completion_summary"`
	CompletionIterations int    `json:"completion_iterations"`
}

type prDevelopmentOperationResultWire struct {
	WorkspaceID    string `json:"workspace_id"`
	Version        int64  `json:"version"`
	MutationEpoch  int64  `json:"mutation_epoch"`
	PreviousTip    string `json:"previous_tip"`
	Tip            string `json:"tip"`
	Tree           string `json:"tree"`
	NoChanges      bool   `json:"no_changes"`
	WorkspaceClean bool   `json:"workspace_clean"`

	IntentID        string `json:"intent_id"`
	ParentCommit    string `json:"parent_commit"`
	CandidateDigest string `json:"candidate_digest"`
	Commit          string `json:"commit"`
	ChangedFiles    int    `json:"changed_files"`

	ReviewVersion       int64  `json:"review_version"`
	ReviewMutationEpoch int64  `json:"review_mutation_epoch"`
	ReviewParkIntentID  string `json:"review_park_intent_id"`
	ReviewBaseCommit    string `json:"review_base_commit"`
	ReviewCommit        string `json:"review_commit"`
	ReviewTree          string `json:"review_tree"`
	ReviewDigest        string `json:"review_digest"`
}

func encodePRDevelopmentOperationRequest(
	request PRDevelopmentControllerOperationRequest,
) ([]byte, string, error) {
	wire, err := prDevelopmentOperationRequestToWire(request)
	if err != nil {
		return nil, "", err
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, "", fmt.Errorf("encode operation request: %w", err)
	}
	if len(encoded) == 0 || len(encoded) > MaxPRDevelopmentOperationRequestBytes {
		return nil, "", fmt.Errorf(
			"%w: canonical operation request is too large",
			ErrInvalidPRDevelopmentController,
		)
	}
	return encoded, prDevelopmentOperationPayloadHash(
		"picoclaw-pr-development-operation-request-v1\x00",
		encoded,
	), nil
}

func decodePRDevelopmentOperationRequest(
	encoded []byte,
) (PRDevelopmentControllerOperationRequest, error) {
	if len(encoded) == 0 || len(encoded) > MaxPRDevelopmentOperationRequestBytes {
		return PRDevelopmentControllerOperationRequest{}, errors.New(
			"stored operation request length is invalid",
		)
	}
	var wire prDevelopmentOperationRequestWire
	if err := decodeCanonicalPRDevelopmentOperationJSON(encoded, &wire); err != nil {
		return PRDevelopmentControllerOperationRequest{}, fmt.Errorf(
			"decode stored operation request: %w",
			err,
		)
	}
	request, err := prDevelopmentOperationRequestFromWire(wire)
	if err != nil {
		return PRDevelopmentControllerOperationRequest{}, err
	}
	canonical, _, err := encodePRDevelopmentOperationRequest(request)
	if err != nil {
		return PRDevelopmentControllerOperationRequest{}, err
	}
	if !bytes.Equal(encoded, canonical) {
		return PRDevelopmentControllerOperationRequest{}, errors.New(
			"stored operation request is not canonical",
		)
	}
	return request, nil
}

func encodePRDevelopmentOperationResult(
	result PRDevelopmentControllerOperationResult,
) ([]byte, string, error) {
	// Replay markers are deliberately excluded from durable evidence. Git's
	// semantic result is identical whether this was first execution or replay.
	wire := prDevelopmentOperationResultWire{
		WorkspaceID:         result.WorkspaceID,
		Version:             result.Version,
		MutationEpoch:       result.MutationEpoch,
		PreviousTip:         result.PreviousTip,
		Tip:                 result.Tip,
		Tree:                result.Tree,
		NoChanges:           result.NoChanges,
		WorkspaceClean:      result.WorkspaceClean,
		IntentID:            result.IntentID,
		ParentCommit:        result.ParentCommit,
		CandidateDigest:     result.CandidateDigest,
		Commit:              result.Commit,
		ChangedFiles:        result.ChangedFiles,
		ReviewVersion:       result.ReviewVersion,
		ReviewMutationEpoch: result.ReviewMutationEpoch,
		ReviewParkIntentID:  result.ReviewParkIntentID,
		ReviewBaseCommit:    result.ReviewBaseCommit,
		ReviewCommit:        result.ReviewCommit,
		ReviewTree:          result.ReviewTree,
		ReviewDigest:        result.ReviewDigest,
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, "", fmt.Errorf("encode operation result: %w", err)
	}
	if len(encoded) == 0 || len(encoded) > MaxPRDevelopmentOperationResultBytes {
		return nil, "", fmt.Errorf(
			"%w: canonical operation result is too large",
			ErrInvalidPRDevelopmentController,
		)
	}
	return encoded, prDevelopmentOperationPayloadHash(
		"picoclaw-pr-development-operation-result-v1\x00",
		encoded,
	), nil
}

func decodePRDevelopmentOperationResult(
	encoded []byte,
) (PRDevelopmentControllerOperationResult, error) {
	if len(encoded) == 0 || len(encoded) > MaxPRDevelopmentOperationResultBytes {
		return PRDevelopmentControllerOperationResult{}, errors.New(
			"stored operation result length is invalid",
		)
	}
	var wire prDevelopmentOperationResultWire
	if err := decodeCanonicalPRDevelopmentOperationJSON(encoded, &wire); err != nil {
		return PRDevelopmentControllerOperationResult{}, fmt.Errorf(
			"decode stored operation result: %w",
			err,
		)
	}
	result := PRDevelopmentControllerOperationResult{
		WorkspaceID:         wire.WorkspaceID,
		Version:             wire.Version,
		MutationEpoch:       wire.MutationEpoch,
		PreviousTip:         wire.PreviousTip,
		Tip:                 wire.Tip,
		Tree:                wire.Tree,
		NoChanges:           wire.NoChanges,
		WorkspaceClean:      wire.WorkspaceClean,
		IntentID:            wire.IntentID,
		ParentCommit:        wire.ParentCommit,
		CandidateDigest:     wire.CandidateDigest,
		Commit:              wire.Commit,
		ChangedFiles:        wire.ChangedFiles,
		ReviewVersion:       wire.ReviewVersion,
		ReviewMutationEpoch: wire.ReviewMutationEpoch,
		ReviewParkIntentID:  wire.ReviewParkIntentID,
		ReviewBaseCommit:    wire.ReviewBaseCommit,
		ReviewCommit:        wire.ReviewCommit,
		ReviewTree:          wire.ReviewTree,
		ReviewDigest:        wire.ReviewDigest,
	}
	canonical, _, err := encodePRDevelopmentOperationResult(result)
	if err != nil {
		return PRDevelopmentControllerOperationResult{}, err
	}
	if !bytes.Equal(encoded, canonical) {
		return PRDevelopmentControllerOperationResult{}, errors.New(
			"stored operation result is not canonical",
		)
	}
	return result, nil
}

func decodeCanonicalPRDevelopmentOperationJSON(encoded []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func prDevelopmentOperationRequestToWire(
	request PRDevelopmentControllerOperationRequest,
) (prDevelopmentOperationRequestWire, error) {
	authoredAt := int64(0)
	if !request.AuthoredAt.IsZero() {
		canonical := request.AuthoredAt.UTC()
		if canonical.Unix() == 0 || canonical.Nanosecond() != 0 || validateDBTimestamp(
			"operation authored time",
			canonical,
		) != nil {
			return prDevelopmentOperationRequestWire{}, fmt.Errorf(
				"%w: operation authored time must be a non-epoch durable UTC whole second",
				ErrInvalidPRDevelopmentController,
			)
		}
		authoredAt = canonical.Unix()
	}
	return prDevelopmentOperationRequestWire{
		Repository:           request.Repository,
		SourceRef:            request.SourceRef,
		SourceCommit:         request.SourceCommit,
		AgentID:              request.AgentID,
		WorkspaceID:          request.WorkspaceID,
		LineID:               request.LineID,
		ExpectedTree:         request.ExpectedTree,
		ExpectedVersion:      request.ExpectedVersion,
		ExpectedEpoch:        request.ExpectedEpoch,
		ExpectedTip:          request.ExpectedTip,
		EffectIntentID:       request.EffectIntentID,
		ExpectedParent:       request.ExpectedParent,
		CandidateDigest:      request.CandidateDigest,
		CommitMessage:        request.CommitMessage,
		AuthoredAtUnix:       authoredAt,
		MutationEpoch:        request.MutationEpoch,
		PreviousTip:          request.PreviousTip,
		Tip:                  request.Tip,
		Tree:                 request.Tree,
		NoChanges:            request.NoChanges,
		CompletionSummary:    request.CompletionSummary,
		CompletionIterations: request.CompletionIterations,
	}, nil
}

func prDevelopmentOperationRequestFromWire(
	wire prDevelopmentOperationRequestWire,
) (PRDevelopmentControllerOperationRequest, error) {
	var authoredAt time.Time
	if wire.AuthoredAtUnix != 0 {
		authoredAt = time.Unix(wire.AuthoredAtUnix, 0).UTC()
		if validateDBTimestamp("stored operation authored time", authoredAt) != nil {
			return PRDevelopmentControllerOperationRequest{}, errors.New(
				"stored operation authored time is invalid",
			)
		}
	}
	return PRDevelopmentControllerOperationRequest{
		Repository:           wire.Repository,
		SourceRef:            wire.SourceRef,
		SourceCommit:         wire.SourceCommit,
		AgentID:              wire.AgentID,
		WorkspaceID:          wire.WorkspaceID,
		LineID:               wire.LineID,
		ExpectedTree:         wire.ExpectedTree,
		ExpectedVersion:      wire.ExpectedVersion,
		ExpectedEpoch:        wire.ExpectedEpoch,
		ExpectedTip:          wire.ExpectedTip,
		EffectIntentID:       wire.EffectIntentID,
		ExpectedParent:       wire.ExpectedParent,
		CandidateDigest:      wire.CandidateDigest,
		CommitMessage:        wire.CommitMessage,
		AuthoredAt:           authoredAt,
		MutationEpoch:        wire.MutationEpoch,
		PreviousTip:          wire.PreviousTip,
		Tip:                  wire.Tip,
		Tree:                 wire.Tree,
		NoChanges:            wire.NoChanges,
		CompletionSummary:    wire.CompletionSummary,
		CompletionIterations: wire.CompletionIterations,
	}, nil
}

func prDevelopmentOperationPayloadHash(domain string, payload []byte) string {
	digest := sha256.New()
	writePRDevelopmentControllerHashField(digest, domain)
	writePRDevelopmentControllerHashField(digest, string(payload))
	return hex.EncodeToString(digest.Sum(nil))
}

func emptyPRDevelopmentOperationDigest() string {
	digest := sha256.Sum256([]byte("picoclaw-pr-development-operations-empty-v1\x00"))
	return hex.EncodeToString(digest[:])
}

func hashPRDevelopmentOperationIntent(operation PRDevelopmentControllerOperation) string {
	digest := sha256.New()
	writePRDevelopmentControllerHashField(
		digest,
		"picoclaw-pr-development-operation-intent-v1\x00",
	)
	for _, value := range []string{
		operation.ID,
		operation.ControllerID,
		operation.AttemptID,
		strconv.Itoa(operation.Ordinal),
		string(operation.Kind),
		strconv.FormatInt(operation.PreparedControllerRevision, 10),
		operation.AgentID,
		operation.WorkspaceID,
		operation.LineID,
		operation.SourceCloneURL,
		operation.SourceRef,
		operation.SourceCommit,
		operation.SourceTree,
		strconv.FormatInt(operation.LineVersion, 10),
		strconv.FormatInt(operation.MutationEpoch, 10),
		operation.TipCommit,
		operation.Tree,
		operation.MutationReservationDigest,
		strconv.FormatInt(operation.MutationLeaseEpoch, 10),
		operation.MutationLeaseTokenDigest,
		operation.EffectIntentID,
		operation.RequestHash,
		operation.PreviousHash,
		operation.CreatedAt.UTC().Format(time.RFC3339Nano),
	} {
		writePRDevelopmentControllerHashField(digest, value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func hashPRDevelopmentOperationRecovery(operation PRDevelopmentControllerOperation) string {
	digest := sha256.New()
	writePRDevelopmentControllerHashField(
		digest,
		"picoclaw-pr-development-operation-recovery-v1\x00",
	)
	for _, value := range []string{
		operation.IntentHash,
		operation.RecoveryID,
		operation.ReplacementReservationDigest,
		strconv.FormatInt(operation.RecoveryRevision, 10),
		strconv.FormatInt(operation.ExpiredControllerRevision, 10),
		strconv.FormatInt(operation.ExpiredLeaseEpoch, 10),
		operation.ExpiredLeaseTokenDigest,
		formatOptionalPRDevelopmentOperationTime(operation.RecoveryLeaseUntil),
		formatOptionalPRDevelopmentOperationTime(operation.RecoveryStagedAt),
	} {
		writePRDevelopmentControllerHashField(digest, value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func hashPRDevelopmentOperationFinal(operation PRDevelopmentControllerOperation) string {
	digest := sha256.New()
	writePRDevelopmentControllerHashField(
		digest,
		"picoclaw-pr-development-operation-final-v1\x00",
	)
	previousHash := operation.IntentHash
	if operation.RecoveryHash != "" {
		previousHash = operation.RecoveryHash
	}
	for _, value := range []string{
		previousHash,
		string(operation.Status),
		operation.ClaimID,
		operation.ClaimOwner,
		strconv.FormatInt(operation.ClaimEpoch, 10),
		strconv.Itoa(operation.Claims),
		formatOptionalPRDevelopmentOperationTime(operation.ClaimedAt),
		operation.ResultHash,
		operation.StageAuthorizationDigest,
		operation.RotationResultHash,
		operation.RecoveryClaimTokenDigest,
		strconv.FormatInt(operation.NewMutationLeaseEpoch, 10),
		operation.NewMutationLeaseTokenDigest,
		formatOptionalPRDevelopmentOperationTime(operation.NewMutationLeaseUntil),
		strconv.FormatInt(operation.FinalControllerRevision, 10),
		string(operation.FinalControllerPhase),
		operation.FinalFenceHash,
		formatOptionalPRDevelopmentOperationTime(operation.FinalizedAt),
	} {
		writePRDevelopmentControllerHashField(digest, value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func formatOptionalPRDevelopmentOperationTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
