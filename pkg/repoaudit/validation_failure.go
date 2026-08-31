package repoaudit

import (
	"errors"
	"time"
)

// RepositoryValidationFailureCode identifies the safe, user-actionable stage
// at which a repository fix check failed. The code and its corresponding
// message are allowlisted below; provider errors and source content must never
// be copied into RepositoryValidationFailure.
type RepositoryValidationFailureCode string

const (
	RepositoryValidationFailureCodeEvidenceUnavailable       RepositoryValidationFailureCode = "evidence_unavailable"
	RepositoryValidationFailureCodeEvidenceInvalid           RepositoryValidationFailureCode = "evidence_invalid"
	RepositoryValidationFailureCodeEvidenceChanged           RepositoryValidationFailureCode = "evidence_changed"
	RepositoryValidationFailureCodeModelRequest              RepositoryValidationFailureCode = "model_request"
	RepositoryValidationFailureCodeModelUnavailable          RepositoryValidationFailureCode = "model_unavailable"
	RepositoryValidationFailureCodeModelTimeout              RepositoryValidationFailureCode = "model_timeout"
	RepositoryValidationFailureCodeModelOutputInvalid        RepositoryValidationFailureCode = "model_output_invalid"
	RepositoryValidationFailureCodeResultInvalid             RepositoryValidationFailureCode = "result_invalid"
	RepositoryValidationFailureCodeDefaultBranchVerification RepositoryValidationFailureCode = "default_branch_verification"
	RepositoryValidationFailureCodeReleaseTag                RepositoryValidationFailureCode = "release_tag"
	RepositoryValidationFailureCodeProcessing                RepositoryValidationFailureCode = "processing"
)

// RepositoryValidationFailure is deliberately safe for API responses.
// Provider payloads, prompts, credentials, source content, and raw errors must
// never be stored in it.
type RepositoryValidationFailure struct {
	Code      RepositoryValidationFailureCode `json:"code"`
	Message   string                          `json:"message"`
	Retryable bool                            `json:"retryable"`
	At        time.Time                       `json:"at"`
}

type repositoryValidationFailureError struct {
	code  RepositoryValidationFailureCode
	cause error
}

func (e *repositoryValidationFailureError) Error() string {
	return e.cause.Error()
}

func (e *repositoryValidationFailureError) Unwrap() error {
	return e.cause
}

// WrapRepositoryValidationFailure associates an allowlisted safe code with an
// internal error. The original error remains available through errors.Is and
// errors.As, but only the safe code is eligible for persistence.
func WrapRepositoryValidationFailure(code RepositoryValidationFailureCode, cause error) error {
	if cause == nil {
		return nil
	}
	if _, _, ok := repositoryValidationFailureDetails(code); !ok {
		code = RepositoryValidationFailureCodeProcessing
	}
	return &repositoryValidationFailureError{code: code, cause: cause}
}

// RepositoryValidationFailureCodeFromError returns only an allowlisted safe
// code carried by WrapRepositoryValidationFailure. It never returns the
// wrapped provider error or any of its text.
func RepositoryValidationFailureCodeFromError(err error) (RepositoryValidationFailureCode, bool) {
	var failureErr *repositoryValidationFailureError
	if !errors.As(err, &failureErr) {
		return "", false
	}
	if _, _, ok := repositoryValidationFailureDetails(failureErr.code); !ok {
		return "", false
	}
	return failureErr.code, true
}

func safeRepositoryValidationFailure(
	code RepositoryValidationFailureCode,
	now time.Time,
) *RepositoryValidationFailure {
	message, retryable, ok := repositoryValidationFailureDetails(code)
	if !ok {
		code = RepositoryValidationFailureCodeProcessing
		message, retryable, _ = repositoryValidationFailureDetails(code)
	}
	return &RepositoryValidationFailure{
		Code: code, Message: message, Retryable: retryable, At: now,
	}
}

func repositoryValidationFailureDetails(
	code RepositoryValidationFailureCode,
) (message string, retryable bool, ok bool) {
	switch code {
	case RepositoryValidationFailureCodeEvidenceUnavailable:
		return "Repository evidence for the fix check could not be loaded.", true, true
	case RepositoryValidationFailureCodeEvidenceInvalid:
		return "Repository evidence for the fix check was invalid.", true, true
	case RepositoryValidationFailureCodeEvidenceChanged:
		return "Repository evidence changed while the fix check was running.", true, true
	case RepositoryValidationFailureCodeModelRequest:
		return "The fix-check model request failed.", true, true
	case RepositoryValidationFailureCodeModelUnavailable:
		return "The fix-check model is unavailable.", true, true
	case RepositoryValidationFailureCodeModelTimeout:
		return "The fix-check model request timed out.", true, true
	case RepositoryValidationFailureCodeModelOutputInvalid:
		return "The fix-check model returned an invalid response.", true, true
	case RepositoryValidationFailureCodeResultInvalid:
		return "The fix-check result was invalid.", true, true
	case RepositoryValidationFailureCodeDefaultBranchVerification:
		return "The selected fix could not be verified on the default branch.", true, true
	case RepositoryValidationFailureCodeReleaseTag:
		return "The release tag for the selected fix could not be determined.", true, true
	case RepositoryValidationFailureCodeProcessing:
		return "Fix-check processing failed.", true, true
	default:
		return "", false, false
	}
}

func validRepositoryValidationFailure(failure *RepositoryValidationFailure) bool {
	if failure == nil {
		return true
	}
	message, retryable, ok := repositoryValidationFailureDetails(failure.Code)
	return ok && failure.Message == message && failure.Retryable == retryable && !failure.At.IsZero()
}
