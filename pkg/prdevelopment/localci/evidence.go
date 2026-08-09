package localci

import (
	"fmt"
	"strings"
	"time"
)

const EvidenceVersion = 1

func resultCacheKey(evidence CandidateEvidence) (string, error) {
	if err := validateCandidateEvidence(evidence); err != nil {
		return "", err
	}
	return digestJSON("picoclaw-local-ci-result-key-v1", evidence)
}

func finalizeExecution(execution Execution) (Execution, error) {
	execution.Steps = append([]StepResult(nil), execution.Steps...)
	execution.Version = EvidenceVersion
	execution.Digest = ""
	if !validDigest(execution.ResultKey) || execution.ResultKey != mustResultKey(execution.Evidence) ||
		!validExecutionStatus(execution.Status) || execution.StartedAt.IsZero() || execution.CompletedAt.IsZero() ||
		execution.StartedAt.Location() != time.UTC || execution.CompletedAt.Location() != time.UTC ||
		execution.CompletedAt.Before(execution.StartedAt) || len(execution.Steps) > maximumPlanSteps {
		return Execution{}, fmt.Errorf("%w: invalid execution envelope", ErrInvalid)
	}
	seen := make(map[string]struct{}, len(execution.Steps))
	var totalOutput int64
	for index := range execution.Steps {
		result := execution.Steps[index]
		if !localCIIDPattern.MatchString(result.StepID) || !validStepStatus(result.Status) ||
			!validDigest(result.OutputDigest) ||
			result.OutputDigest != digestParts("picoclaw-local-ci-output-v1", []byte(result.Output)) ||
			result.DurationMillis < 0 || len(result.Output) > 4<<20 ||
			result.ObservedOutputBytes < int64(len(result.Output)) ||
			result.ObservedOutputBytes > execution.Evidence.Limits.OutputBytes ||
			result.OutputTruncated && result.Status != StatusOutputLimitExceeded {
			return Execution{}, fmt.Errorf("%w: invalid step result", ErrInvalid)
		}
		if totalOutput > execution.Evidence.Limits.OutputBytes-result.ObservedOutputBytes {
			return Execution{}, fmt.Errorf("%w: execution exceeds aggregate output limit", ErrInvalid)
		}
		totalOutput += result.ObservedOutputBytes
		if _, exists := seen[result.StepID]; exists {
			return Execution{}, fmt.Errorf("%w: duplicate step result", ErrInvalid)
		}
		seen[result.StepID] = struct{}{}
		execution.Steps[index] = result
	}
	if execution.Status == StatusPassed {
		if len(execution.Steps) == 0 {
			return Execution{}, fmt.Errorf("%w: passing execution contains no steps", ErrInvalid)
		}
		for _, result := range execution.Steps {
			if result.Status != StatusPassed || result.ExitCode != 0 || result.OutputTruncated {
				return Execution{}, fmt.Errorf("%w: passing execution contains a non-passing step", ErrInvalid)
			}
		}
	}
	digest, err := digestJSON("picoclaw-local-ci-execution-v1", execution)
	if err != nil {
		return Execution{}, err
	}
	execution.Digest = digest
	return execution, nil
}

func finalizeAttestation(attestation Attestation) (Attestation, error) {
	attestation.Version = EvidenceVersion
	attestation.Digest = ""
	if !localCIIDPattern.MatchString(attestation.ID) || !localCIIDPattern.MatchString(attestation.OwnerID) ||
		!validDigest(attestation.ExecutionDigest) || !validDigest(attestation.ResultKey) ||
		!validExecutionStatus(attestation.Status) || attestation.CreatedAt.IsZero() ||
		attestation.CreatedAt.Location() != time.UTC {
		return Attestation{}, fmt.Errorf("%w: invalid attestation", ErrInvalid)
	}
	digest, err := digestJSON("picoclaw-local-ci-attestation-v1", attestation)
	if err != nil {
		return Attestation{}, err
	}
	attestation.Digest = digest
	return attestation, nil
}

func validateCandidateEvidence(evidence CandidateEvidence) error {
	if strings.TrimSpace(evidence.Repository) != evidence.Repository || evidence.Repository == "" ||
		len(evidence.Repository) > 1024 || !validObjectID(evidence.ParentCommit) ||
		!validObjectID(evidence.Tree) || !validDigest(evidence.CandidateDigest) ||
		!validDigest(evidence.ParentManifestDigest) || !validDigest(evidence.CandidateManifestDigest) ||
		!validDigest(evidence.DependencyDigest) ||
		!validDigest(evidence.PlanDigest) || !validDigest(evidence.EnvironmentDigest) ||
		!validLimitEvidence(evidence.Limits) {
		return fmt.Errorf("%w: invalid candidate evidence", ErrInvalid)
	}
	return nil
}

func validLimitEvidence(limits LimitEvidence) bool {
	return limits.StepTimeoutMillis > 0 && limits.StepTimeoutMillis <= int64((30*time.Minute)/time.Millisecond) &&
		limits.TotalTimeoutMillis > 0 && limits.TotalTimeoutMillis <= int64((30*time.Minute)/time.Millisecond) &&
		limits.OutputBytes > 0 && limits.OutputBytes <= 4<<20 &&
		limits.ResourcePolicy == "aggregate-resource-policy-v1"
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	if strings.ToLower(value) != value {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func validExecutionStatus(status Status) bool {
	return status == StatusPassed || status == StatusFailed || status == StatusIncomplete ||
		status == StatusPlanChanged || status == StatusTimedOut || status == StatusCanceled ||
		status == StatusOutputLimitExceeded || status == StatusEnvironmentUnavailable ||
		status == StatusInfrastructureError
}

func validStepStatus(status Status) bool {
	return status == StatusPassed || status == StatusFailed || status == StatusTimedOut ||
		status == StatusCanceled || status == StatusOutputLimitExceeded ||
		status == StatusEnvironmentUnavailable || status == StatusInfrastructureError
}

func mustResultKey(evidence CandidateEvidence) string {
	key, _ := resultCacheKey(evidence)
	return key
}
