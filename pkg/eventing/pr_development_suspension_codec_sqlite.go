//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

type prDevelopmentControllerSuspensionRequestWire struct {
	Repository            string `json:"repository"`
	SourceRef             string `json:"source_ref"`
	SourceCommit          string `json:"source_commit"`
	AgentID               string `json:"agent_id"`
	WorkspaceID           string `json:"workspace_id"`
	LineID                string `json:"line_id"`
	IntentID              string `json:"intent_id"`
	ExpectedVersion       int64  `json:"expected_version"`
	ExpectedMutationEpoch int64  `json:"expected_mutation_epoch"`
	ExpectedTip           string `json:"expected_tip"`
	ExpectedTree          string `json:"expected_tree"`
	CommitIntentID        string `json:"commit_intent_id"`
	CommitExpectedParent  string `json:"commit_expected_parent"`
	CommitExpectedTree    string `json:"commit_expected_tree"`
	CommitCandidateDigest string `json:"commit_candidate_digest"`
	CommitMessage         string `json:"commit_message"`
	CommitAuthoredAtUnix  int64  `json:"commit_authored_at_unix"`
}

type prDevelopmentControllerSuspensionResultWire struct {
	WorkspaceID           string `json:"workspace_id"`
	Version               int64  `json:"version"`
	MutationEpoch         int64  `json:"mutation_epoch"`
	Tip                   string `json:"tip"`
	Tree                  string `json:"tree"`
	CandidateTree         string `json:"candidate_tree"`
	CandidateDigest       string `json:"candidate_digest"`
	ChangedFileCount      int    `json:"changed_file_count"`
	SuspensionHash        string `json:"suspension_hash"`
	PreparedCommit        string `json:"prepared_commit"`
	PreparedTree          string `json:"prepared_tree"`
	PreparedCommitApplied bool   `json:"prepared_commit_applied"`
}

type prDevelopmentControllerSuspendedResumeRequestWire struct {
	Repository            string `json:"repository"`
	SourceRef             string `json:"source_ref"`
	SourceCommit          string `json:"source_commit"`
	AgentID               string `json:"agent_id"`
	WorkspaceID           string `json:"workspace_id"`
	LineID                string `json:"line_id"`
	IntentID              string `json:"intent_id"`
	ExpectedVersion       int64  `json:"expected_version"`
	ExpectedMutationEpoch int64  `json:"expected_mutation_epoch"`
	ExpectedTip           string `json:"expected_tip"`
	ExpectedTree          string `json:"expected_tree"`
	SuspensionHash        string `json:"suspension_hash"`
	CandidateTree         string `json:"candidate_tree"`
	CandidateDigest       string `json:"candidate_digest"`
	ChangedFileCount      int    `json:"changed_file_count"`
}

type prDevelopmentControllerSuspendedResumeResultWire struct {
	WorkspaceID      string `json:"workspace_id"`
	Version          int64  `json:"version"`
	MutationEpoch    int64  `json:"mutation_epoch"`
	Tip              string `json:"tip"`
	Tree             string `json:"tree"`
	CandidateTree    string `json:"candidate_tree"`
	CandidateDigest  string `json:"candidate_digest"`
	ChangedFileCount int    `json:"changed_file_count"`
	SuspensionHash   string `json:"suspension_hash"`
	RotationHash     string `json:"rotation_hash"`
}

func encodePRDevelopmentControllerSuspensionRequest(
	request PRDevelopmentControllerSuspensionRequest,
) ([]byte, string, error) {
	wire, err := prDevelopmentControllerSuspensionRequestToWire(request)
	if err != nil {
		return nil, "", err
	}
	return encodePRDevelopmentControllerSuspensionPayload(
		wire,
		MaxPRDevelopmentOperationRequestBytes,
		"picoclaw-pr-development-controller-suspension-request-v1\x00",
		"suspension request",
	)
}

func decodePRDevelopmentControllerSuspensionRequest(
	encoded []byte,
) (PRDevelopmentControllerSuspensionRequest, error) {
	if len(encoded) < 2 || len(encoded) > MaxPRDevelopmentOperationRequestBytes {
		return PRDevelopmentControllerSuspensionRequest{}, errors.New(
			"stored controller suspension request length is invalid",
		)
	}
	var wire prDevelopmentControllerSuspensionRequestWire
	if err := decodeCanonicalPRDevelopmentOperationJSON(encoded, &wire); err != nil {
		return PRDevelopmentControllerSuspensionRequest{}, fmt.Errorf(
			"decode stored controller suspension request: %w", err,
		)
	}
	request, err := prDevelopmentControllerSuspensionRequestFromWire(wire)
	if err != nil {
		return PRDevelopmentControllerSuspensionRequest{}, err
	}
	canonical, _, err := encodePRDevelopmentControllerSuspensionRequest(request)
	if err != nil {
		return PRDevelopmentControllerSuspensionRequest{}, err
	}
	if !bytes.Equal(encoded, canonical) {
		return PRDevelopmentControllerSuspensionRequest{}, errors.New(
			"stored controller suspension request is not canonical",
		)
	}
	return request, nil
}

func encodePRDevelopmentControllerSuspensionResult(
	result PRDevelopmentControllerSuspensionResult,
) ([]byte, string, error) {
	// AlreadySuspended is operational replay metadata, not durable evidence.
	wire := prDevelopmentControllerSuspensionResultWire{
		WorkspaceID:           result.WorkspaceID,
		Version:               result.Version,
		MutationEpoch:         result.MutationEpoch,
		Tip:                   result.Tip,
		Tree:                  result.Tree,
		CandidateTree:         result.CandidateTree,
		CandidateDigest:       result.CandidateDigest,
		ChangedFileCount:      result.ChangedFileCount,
		SuspensionHash:        result.SuspensionHash,
		PreparedCommit:        result.PreparedCommit,
		PreparedTree:          result.PreparedTree,
		PreparedCommitApplied: result.PreparedCommitApplied,
	}
	return encodePRDevelopmentControllerSuspensionPayload(
		wire,
		MaxPRDevelopmentOperationResultBytes,
		"picoclaw-pr-development-controller-suspension-result-v1\x00",
		"suspension result",
	)
}

func decodePRDevelopmentControllerSuspensionResult(
	encoded []byte,
) (PRDevelopmentControllerSuspensionResult, error) {
	if len(encoded) < 2 || len(encoded) > MaxPRDevelopmentOperationResultBytes {
		return PRDevelopmentControllerSuspensionResult{}, errors.New(
			"stored controller suspension result length is invalid",
		)
	}
	var wire prDevelopmentControllerSuspensionResultWire
	if err := decodeCanonicalPRDevelopmentOperationJSON(encoded, &wire); err != nil {
		return PRDevelopmentControllerSuspensionResult{}, fmt.Errorf(
			"decode stored controller suspension result: %w", err,
		)
	}
	result := PRDevelopmentControllerSuspensionResult{
		WorkspaceID:           wire.WorkspaceID,
		Version:               wire.Version,
		MutationEpoch:         wire.MutationEpoch,
		Tip:                   wire.Tip,
		Tree:                  wire.Tree,
		CandidateTree:         wire.CandidateTree,
		CandidateDigest:       wire.CandidateDigest,
		ChangedFileCount:      wire.ChangedFileCount,
		SuspensionHash:        wire.SuspensionHash,
		PreparedCommit:        wire.PreparedCommit,
		PreparedTree:          wire.PreparedTree,
		PreparedCommitApplied: wire.PreparedCommitApplied,
	}
	canonical, _, err := encodePRDevelopmentControllerSuspensionResult(result)
	if err != nil {
		return PRDevelopmentControllerSuspensionResult{}, err
	}
	if !bytes.Equal(encoded, canonical) {
		return PRDevelopmentControllerSuspensionResult{}, errors.New(
			"stored controller suspension result is not canonical",
		)
	}
	return result, nil
}

func encodePRDevelopmentControllerSuspendedResumeRequest(
	request PRDevelopmentControllerSuspendedResumeRequest,
) ([]byte, string, error) {
	wire := prDevelopmentControllerSuspendedResumeRequestWire{
		Repository:            request.Repository,
		SourceRef:             request.SourceRef,
		SourceCommit:          request.SourceCommit,
		AgentID:               request.AgentID,
		WorkspaceID:           request.WorkspaceID,
		LineID:                request.LineID,
		IntentID:              request.IntentID,
		ExpectedVersion:       request.ExpectedVersion,
		ExpectedMutationEpoch: request.ExpectedMutationEpoch,
		ExpectedTip:           request.ExpectedTip,
		ExpectedTree:          request.ExpectedTree,
		SuspensionHash:        request.SuspensionHash,
		CandidateTree:         request.CandidateTree,
		CandidateDigest:       request.CandidateDigest,
		ChangedFileCount:      request.ChangedFileCount,
	}
	return encodePRDevelopmentControllerSuspensionPayload(
		wire,
		MaxPRDevelopmentOperationRequestBytes,
		"picoclaw-pr-development-controller-suspended-resume-request-v1\x00",
		"suspended resume request",
	)
}

func decodePRDevelopmentControllerSuspendedResumeRequest(
	encoded []byte,
) (PRDevelopmentControllerSuspendedResumeRequest, error) {
	if len(encoded) < 2 || len(encoded) > MaxPRDevelopmentOperationRequestBytes {
		return PRDevelopmentControllerSuspendedResumeRequest{}, errors.New(
			"stored controller suspended resume request length is invalid",
		)
	}
	var wire prDevelopmentControllerSuspendedResumeRequestWire
	if err := decodeCanonicalPRDevelopmentOperationJSON(encoded, &wire); err != nil {
		return PRDevelopmentControllerSuspendedResumeRequest{}, fmt.Errorf(
			"decode stored controller suspended resume request: %w", err,
		)
	}
	request := PRDevelopmentControllerSuspendedResumeRequest{
		Repository:            wire.Repository,
		SourceRef:             wire.SourceRef,
		SourceCommit:          wire.SourceCommit,
		AgentID:               wire.AgentID,
		WorkspaceID:           wire.WorkspaceID,
		LineID:                wire.LineID,
		IntentID:              wire.IntentID,
		ExpectedVersion:       wire.ExpectedVersion,
		ExpectedMutationEpoch: wire.ExpectedMutationEpoch,
		ExpectedTip:           wire.ExpectedTip,
		ExpectedTree:          wire.ExpectedTree,
		SuspensionHash:        wire.SuspensionHash,
		CandidateTree:         wire.CandidateTree,
		CandidateDigest:       wire.CandidateDigest,
		ChangedFileCount:      wire.ChangedFileCount,
	}
	canonical, _, err := encodePRDevelopmentControllerSuspendedResumeRequest(request)
	if err != nil {
		return PRDevelopmentControllerSuspendedResumeRequest{}, err
	}
	if !bytes.Equal(encoded, canonical) {
		return PRDevelopmentControllerSuspendedResumeRequest{}, errors.New(
			"stored controller suspended resume request is not canonical",
		)
	}
	return request, nil
}

func encodePRDevelopmentControllerSuspendedResumeResult(
	result PRDevelopmentControllerSuspendedResumeResult,
) ([]byte, string, error) {
	// AlreadyResumed is operational replay metadata, not durable evidence.
	wire := prDevelopmentControllerSuspendedResumeResultWire{
		WorkspaceID:      result.WorkspaceID,
		Version:          result.Version,
		MutationEpoch:    result.MutationEpoch,
		Tip:              result.Tip,
		Tree:             result.Tree,
		CandidateTree:    result.CandidateTree,
		CandidateDigest:  result.CandidateDigest,
		ChangedFileCount: result.ChangedFileCount,
		SuspensionHash:   result.SuspensionHash,
		RotationHash:     result.RotationHash,
	}
	return encodePRDevelopmentControllerSuspensionPayload(
		wire,
		MaxPRDevelopmentOperationResultBytes,
		"picoclaw-pr-development-controller-suspended-resume-result-v1\x00",
		"suspended resume result",
	)
}

func decodePRDevelopmentControllerSuspendedResumeResult(
	encoded []byte,
) (PRDevelopmentControllerSuspendedResumeResult, error) {
	if len(encoded) < 2 || len(encoded) > MaxPRDevelopmentOperationResultBytes {
		return PRDevelopmentControllerSuspendedResumeResult{}, errors.New(
			"stored controller suspended resume result length is invalid",
		)
	}
	var wire prDevelopmentControllerSuspendedResumeResultWire
	if err := decodeCanonicalPRDevelopmentOperationJSON(encoded, &wire); err != nil {
		return PRDevelopmentControllerSuspendedResumeResult{}, fmt.Errorf(
			"decode stored controller suspended resume result: %w", err,
		)
	}
	result := PRDevelopmentControllerSuspendedResumeResult{
		WorkspaceID:      wire.WorkspaceID,
		Version:          wire.Version,
		MutationEpoch:    wire.MutationEpoch,
		Tip:              wire.Tip,
		Tree:             wire.Tree,
		CandidateTree:    wire.CandidateTree,
		CandidateDigest:  wire.CandidateDigest,
		ChangedFileCount: wire.ChangedFileCount,
		SuspensionHash:   wire.SuspensionHash,
		RotationHash:     wire.RotationHash,
	}
	canonical, _, err := encodePRDevelopmentControllerSuspendedResumeResult(result)
	if err != nil {
		return PRDevelopmentControllerSuspendedResumeResult{}, err
	}
	if !bytes.Equal(encoded, canonical) {
		return PRDevelopmentControllerSuspendedResumeResult{}, errors.New(
			"stored controller suspended resume result is not canonical",
		)
	}
	return result, nil
}

func prDevelopmentControllerSuspensionRequestToWire(
	request PRDevelopmentControllerSuspensionRequest,
) (prDevelopmentControllerSuspensionRequestWire, error) {
	authoredAt := int64(0)
	if !request.CommitAuthoredAt.IsZero() {
		canonical := request.CommitAuthoredAt.UTC()
		if canonical.Unix() == 0 || canonical.Nanosecond() != 0 ||
			validateDBTimestamp("suspension commit authored time", canonical) != nil {
			return prDevelopmentControllerSuspensionRequestWire{}, fmt.Errorf(
				"%w: suspension commit authored time must be a non-epoch durable UTC whole second",
				ErrInvalidPRDevelopmentController,
			)
		}
		authoredAt = canonical.Unix()
	}
	return prDevelopmentControllerSuspensionRequestWire{
		Repository:            request.Repository,
		SourceRef:             request.SourceRef,
		SourceCommit:          request.SourceCommit,
		AgentID:               request.AgentID,
		WorkspaceID:           request.WorkspaceID,
		LineID:                request.LineID,
		IntentID:              request.IntentID,
		ExpectedVersion:       request.ExpectedVersion,
		ExpectedMutationEpoch: request.ExpectedMutationEpoch,
		ExpectedTip:           request.ExpectedTip,
		ExpectedTree:          request.ExpectedTree,
		CommitIntentID:        request.CommitIntentID,
		CommitExpectedParent:  request.CommitExpectedParent,
		CommitExpectedTree:    request.CommitExpectedTree,
		CommitCandidateDigest: request.CommitCandidateDigest,
		CommitMessage:         request.CommitMessage,
		CommitAuthoredAtUnix:  authoredAt,
	}, nil
}

func prDevelopmentControllerSuspensionRequestFromWire(
	wire prDevelopmentControllerSuspensionRequestWire,
) (PRDevelopmentControllerSuspensionRequest, error) {
	var authoredAt time.Time
	if wire.CommitAuthoredAtUnix != 0 {
		authoredAt = time.Unix(wire.CommitAuthoredAtUnix, 0).UTC()
		if authoredAt.Unix() != wire.CommitAuthoredAtUnix ||
			validateDBTimestamp("stored suspension commit authored time", authoredAt) != nil {
			return PRDevelopmentControllerSuspensionRequest{}, errors.New(
				"stored suspension commit authored time is invalid",
			)
		}
	}
	return PRDevelopmentControllerSuspensionRequest{
		Repository:            wire.Repository,
		SourceRef:             wire.SourceRef,
		SourceCommit:          wire.SourceCommit,
		AgentID:               wire.AgentID,
		WorkspaceID:           wire.WorkspaceID,
		LineID:                wire.LineID,
		IntentID:              wire.IntentID,
		ExpectedVersion:       wire.ExpectedVersion,
		ExpectedMutationEpoch: wire.ExpectedMutationEpoch,
		ExpectedTip:           wire.ExpectedTip,
		ExpectedTree:          wire.ExpectedTree,
		CommitIntentID:        wire.CommitIntentID,
		CommitExpectedParent:  wire.CommitExpectedParent,
		CommitExpectedTree:    wire.CommitExpectedTree,
		CommitCandidateDigest: wire.CommitCandidateDigest,
		CommitMessage:         wire.CommitMessage,
		CommitAuthoredAt:      authoredAt,
	}, nil
}

func encodePRDevelopmentControllerSuspensionPayload(
	wire any,
	maximum int,
	domain, label string,
) ([]byte, string, error) {
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, "", fmt.Errorf("encode %s: %w", label, err)
	}
	if len(encoded) < 2 || len(encoded) > maximum {
		return nil, "", fmt.Errorf(
			"%w: canonical %s is too large",
			ErrInvalidPRDevelopmentController,
			label,
		)
	}
	return encoded, prDevelopmentControllerSuspensionPayloadHash(domain, encoded), nil
}

func prDevelopmentControllerSuspensionPayloadHash(domain string, payload []byte) string {
	digest := sha256.New()
	writePRDevelopmentControllerHashField(digest, domain)
	writePRDevelopmentControllerHashField(digest, string(payload))
	return hex.EncodeToString(digest.Sum(nil))
}

func deterministicPRDevelopmentControllerSuspensionID(
	sourceKind PRDevelopmentControllerSuspensionSourceKind,
	sourceRecoveryID, sourceOperationID string,
) string {
	digest := sha256.New()
	for _, value := range []string{
		"picoclaw-pr-development-controller-suspension-id-v1\x00",
		string(sourceKind),
		sourceRecoveryID,
		sourceOperationID,
	} {
		writePRDevelopmentControllerHashField(digest, value)
	}
	return prDevelopmentSuspensionIDPrefix + hex.EncodeToString(digest.Sum(nil)[:16])
}

func emptyPRDevelopmentControllerSuspensionDigest() string {
	digest := sha256.Sum256([]byte(
		"picoclaw-pr-development-controller-suspensions-empty-v1\x00",
	))
	return hex.EncodeToString(digest[:])
}

func hashPRDevelopmentControllerSuspensionIntent(
	suspension PRDevelopmentControllerSuspension,
) string {
	digest := sha256.New()
	for _, value := range []string{
		"picoclaw-pr-development-controller-suspension-intent-v1\x00",
		suspension.ID,
		suspension.ControllerID,
		suspension.ThreadID,
		suspension.OwnerSessionID,
		suspension.AttemptID,
		strconv.Itoa(suspension.Ordinal),
		string(suspension.SourceKind),
		suspension.SourceRecoveryID,
		suspension.SourceOperationID,
		string(suspension.SourceOperationKind),
		strconv.FormatInt(suspension.SourceFinalRevision, 10),
		suspension.SourceFinalHash,
		string(suspension.Mode),
		suspension.AgentID,
		suspension.WorkspaceID,
		suspension.LineID,
		suspension.SourceCloneURL,
		suspension.SourceRef,
		suspension.SourceCommit,
		suspension.SourceTree,
		strconv.FormatInt(suspension.LineVersion, 10),
		strconv.FormatInt(suspension.MutationEpoch, 10),
		suspension.TipCommit,
		suspension.Tree,
		suspension.SuspensionReservationDigest,
		strconv.FormatInt(suspension.MutationLeaseEpoch, 10),
		suspension.MutationLeaseTokenDigest,
		suspension.SuspendIntentID,
		suspension.SuspendRequestHash,
		suspension.PreviousHash,
		strconv.FormatInt(toDBTime(suspension.CreatedAt), 10),
	} {
		writePRDevelopmentControllerHashField(digest, value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func hashPRDevelopmentControllerSuspensionFinal(
	suspension PRDevelopmentControllerSuspension,
) string {
	digest := sha256.New()
	for _, value := range []string{
		"picoclaw-pr-development-controller-suspension-final-v1\x00",
		suspension.IntentHash,
		suspension.SuspendClaimID,
		suspension.SuspendClaimOwner,
		strconv.FormatInt(suspension.SuspendClaimEpoch, 10),
		strconv.Itoa(suspension.SuspendClaims),
		formatOptionalPRDevelopmentControllerSuspensionTime(suspension.SuspendClaimedAt),
		suspension.SuspendClaimTokenDigest,
		suspension.SuspendResultHash,
		strconv.FormatInt(suspension.FinalSuspensionRevision, 10),
		formatOptionalPRDevelopmentControllerSuspensionTime(suspension.SuspendedAt),
	} {
		writePRDevelopmentControllerHashField(digest, value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func hashPRDevelopmentControllerSuspensionResumeIntent(
	suspension PRDevelopmentControllerSuspension,
) string {
	digest := sha256.New()
	for _, value := range []string{
		"picoclaw-pr-development-controller-suspension-resume-intent-v1\x00",
		suspension.SuspensionFinalHash,
		suspension.ResumeAttemptID,
		suspension.ResumeIntentID,
		suspension.ResumeReservationDigest,
		suspension.ResumeRequestHash,
		formatOptionalPRDevelopmentControllerSuspensionTime(suspension.ResumePreparedAt),
	} {
		writePRDevelopmentControllerHashField(digest, value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func hashPRDevelopmentControllerSuspensionResumeFinal(
	suspension PRDevelopmentControllerSuspension,
) string {
	digest := sha256.New()
	for _, value := range []string{
		"picoclaw-pr-development-controller-suspension-resume-final-v1\x00",
		suspension.ResumeIntentHash,
		suspension.ResumeClaimID,
		suspension.ResumeClaimOwner,
		strconv.FormatInt(suspension.ResumeClaimEpoch, 10),
		strconv.Itoa(suspension.ResumeClaims),
		formatOptionalPRDevelopmentControllerSuspensionTime(suspension.ResumeClaimedAt),
		suspension.ResumeClaimTokenDigest,
		suspension.ResumeResultHash,
		suspension.ResumeResult.RotationHash,
		strconv.FormatInt(suspension.NewMutationLeaseEpoch, 10),
		suspension.NewMutationLeaseTokenDigest,
		formatOptionalPRDevelopmentControllerSuspensionTime(suspension.NewMutationLeaseUntil),
		strconv.FormatInt(suspension.FinalResumeRevision, 10),
		formatOptionalPRDevelopmentControllerSuspensionTime(suspension.ResumedAt),
	} {
		writePRDevelopmentControllerHashField(digest, value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func prDevelopmentControllerSuspensionClaimTokenDigest(token string) string {
	return prDevelopmentControllerSuspensionTokenDigest(
		"picoclaw-pr-development-controller-suspension-claim-token-v1\x00",
		token,
	)
}

func prDevelopmentControllerSuspensionResumeClaimTokenDigest(token string) string {
	return prDevelopmentControllerSuspensionTokenDigest(
		"picoclaw-pr-development-controller-suspension-resume-claim-token-v1\x00",
		token,
	)
}

func prDevelopmentControllerSuspensionTokenDigest(domain, token string) string {
	digest := sha256.New()
	writePRDevelopmentControllerHashField(digest, domain)
	writePRDevelopmentControllerHashField(digest, token)
	return hex.EncodeToString(digest.Sum(nil))
}

func formatOptionalPRDevelopmentControllerSuspensionTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(toDBTime(*value), 10)
}
