package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const maxStructuredErrorMessageBytes = 512

// ErrorCode is a backend-neutral failure classification returned by the broker.
// Callers must make retry and user-interface decisions from the code rather
// than from Error.Message.
type ErrorCode string

const (
	CodeUnavailable       ErrorCode = "Unavailable"
	CodeMigrationRequired ErrorCode = "MigrationRequired"
	CodeConflict          ErrorCode = "Conflict"
	CodeNotFound          ErrorCode = "NotFound"
	CodeAlreadyExists     ErrorCode = "AlreadyExists"
	CodeDeadline          ErrorCode = "Deadline"
	CodeIntegrity         ErrorCode = "Integrity"
	CodeInvalid           ErrorCode = "Invalid"
	CodeUnauthorized      ErrorCode = "Unauthorized"
	CodeUnsupported       ErrorCode = "Unsupported"
	CodeOutcomeUnknown    ErrorCode = "OutcomeUnknown"
	CodeInternal          ErrorCode = "Internal"
)

var validErrorCodes = map[ErrorCode]struct{}{
	CodeUnavailable:       {},
	CodeMigrationRequired: {},
	CodeConflict:          {},
	CodeNotFound:          {},
	CodeAlreadyExists:     {},
	CodeDeadline:          {},
	CodeIntegrity:         {},
	CodeInvalid:           {},
	CodeUnauthorized:      {},
	CodeUnsupported:       {},
	CodeOutcomeUnknown:    {},
	CodeInternal:          {},
}

// Error is the only error representation carried by the broker protocol.
// Message is deliberately diagnostic-only and must not contain SQL, provider
// names, paths, DSNs, or other backend details.
type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Message) == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Is lets callers match errors by a structured code without comparing text.
func (e *Error) Is(target error) bool {
	var other *Error
	return e != nil && errors.As(target, &other) && other != nil && e.Code == other.Code
}

// NewError returns a structured provider-neutral error. Invalid codes are
// collapsed to Internal so an extension cannot accidentally escape the wire
// contract.
func NewError(code ErrorCode, message string) *Error {
	if !code.Valid() {
		code = CodeInternal
		message = "database broker operation failed"
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = defaultErrorMessage(code)
	}
	message = strings.Map(func(character rune) rune {
		if character < ' ' || character == '\u007f' {
			return ' '
		}
		return character
	}, message)
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > maxStructuredErrorMessageBytes {
		message = message[:maxStructuredErrorMessageBytes]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
		message = strings.TrimSpace(message)
	}
	return &Error{Code: code, Message: message}
}

// Valid reports whether code belongs to protocol v1.
func (code ErrorCode) Valid() bool {
	_, ok := validErrorCodes[code]
	return ok
}

// CodeOf returns the structured code carried by err, or Internal for an
// unclassified implementation error.
func CodeOf(err error) ErrorCode {
	if err == nil {
		return ""
	}
	var brokerErr *Error
	if errors.As(err, &brokerErr) && brokerErr != nil && brokerErr.Code.Valid() {
		return brokerErr.Code
	}
	return CodeInternal
}

func protocolError(err error) *Error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return NewError(CodeDeadline, "database request deadline was exceeded")
	}
	if errors.Is(err, context.Canceled) {
		return NewError(CodeDeadline, "database broker request was canceled")
	}
	var brokerErr *Error
	if errors.As(err, &brokerErr) && brokerErr != nil && brokerErr.Code.Valid() {
		return NewError(brokerErr.Code, brokerErr.Message)
	}
	return NewError(CodeInternal, "database broker operation failed")
}

func defaultErrorMessage(code ErrorCode) string {
	switch code {
	case CodeUnavailable:
		return "database broker is unavailable"
	case CodeMigrationRequired:
		return "offline database migration is required"
	case CodeConflict:
		return "database operation conflicts with current state"
	case CodeNotFound:
		return "requested database resource was not found"
	case CodeAlreadyExists:
		return "database resource already exists"
	case CodeDeadline:
		return "database operation deadline was exceeded"
	case CodeIntegrity:
		return "database integrity validation failed"
	case CodeInvalid:
		return "database operation is invalid"
	case CodeUnauthorized:
		return "database broker authentication failed"
	case CodeUnsupported:
		return "database operation is unsupported"
	case CodeOutcomeUnknown:
		return "database mutation outcome is unknown"
	default:
		return "database broker operation failed"
	}
}
