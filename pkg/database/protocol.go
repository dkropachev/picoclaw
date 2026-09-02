package database

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

const (
	ProtocolVersion = 1

	ControlDomain  = "control"
	ControlVersion = 1

	ControlOperationPing     = "ping"
	ControlOperationStatus   = "status"
	ControlOperationShutdown = "shutdown"
)

const (
	maxRequestIDBytes      = 64
	maxDomainBytes         = 64
	maxOperationBytes      = 96
	maxIdempotencyKeyBytes = 256
)

// RequestEnvelope is the authenticated protocol v1 wire request. Deadline is
// an absolute UTC Unix nanosecond deadline.
type RequestEnvelope struct {
	Protocol       int             `json:"protocol"`
	RequestID      string          `json:"request_id"`
	Token          string          `json:"token"`
	BrokerEpoch    string          `json:"broker_epoch"`
	Domain         string          `json:"domain"`
	DomainVersion  int             `json:"domain_version"`
	Operation      string          `json:"operation"`
	DeadlineUnixNs int64           `json:"deadline_unix_ns"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	Payload        json.RawMessage `json:"payload"`
}

// ResponseEnvelope is the protocol v1 wire response. Exactly one of Payload
// and Error is populated.
type ResponseEnvelope struct {
	Protocol    int             `json:"protocol"`
	RequestID   string          `json:"request_id"`
	BrokerEpoch string          `json:"broker_epoch"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Error       *Error          `json:"error,omitempty"`
}

// Request is the authenticated, epoch-fenced request given to a domain handler.
type Request struct {
	ID             string
	Domain         string
	Version        int
	Operation      string
	IdempotencyKey string
	Payload        json.RawMessage
}

// DecodePayload strictly decodes the request's canonical JSON object.
func (request Request) DecodePayload(destination any) error {
	return unmarshalCanonicalStrict(request.Payload, destination)
}

// Handler implements broker-side typed domain dispatch. Implementations must
// return domain values, never SQL handles, callbacks, paths, or provider errors.
type Handler interface {
	Handle(ctx context.Context, request Request) (any, error)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(context.Context, Request) (any, error)

func (handler HandlerFunc) Handle(ctx context.Context, request Request) (any, error) {
	return handler(ctx, request)
}

// StatusProvider returns a detached readiness snapshot of the broker catalog.
type StatusProvider func(context.Context) ([]StoreStatus, error)

type EmptyPayload struct{}

type PingResponse struct {
	Protocol int    `json:"protocol"`
	PID      int    `json:"pid"`
	Epoch    string `json:"epoch"`
}

type BrokerStatus struct {
	Protocol           int           `json:"protocol"`
	PID                int           `json:"pid"`
	Epoch              string        `json:"epoch"`
	StartedAt          time.Time     `json:"started_at"`
	CatalogFingerprint string        `json:"catalog_fingerprint,omitempty"`
	RequiredStores     []StoreID     `json:"required_stores,omitempty"`
	Stores             []StoreStatus `json:"stores"`
}

type ShutdownResponse struct {
	Accepted bool `json:"accepted"`
}

func validRequestEnvelope(envelope RequestEnvelope) error {
	if envelope.Protocol != ProtocolVersion {
		return NewError(CodeUnsupported, "database broker protocol version is unsupported")
	}
	if !validRequestID(envelope.RequestID) {
		return NewError(CodeInvalid, "database request ID is invalid")
	}
	if !validProtocolName(envelope.Domain, maxDomainBytes) ||
		!validProtocolName(envelope.Operation, maxOperationBytes) || envelope.DomainVersion <= 0 {
		return NewError(CodeInvalid, "database domain operation is invalid")
	}
	if envelope.IdempotencyKey != "" && !validIdempotencyKey(envelope.IdempotencyKey) {
		return NewError(CodeInvalid, "database idempotency key is invalid")
	}
	if envelope.DeadlineUnixNs <= 0 {
		return NewError(CodeInvalid, "database request deadline is invalid")
	}
	if !jsonObject(envelope.Payload) {
		return NewError(CodeInvalid, "database request payload must be a JSON object")
	}
	return nil
}

func validResponseEnvelope(envelope ResponseEnvelope, requestID, epoch string) error {
	if envelope.Protocol != ProtocolVersion {
		return NewError(CodeUnsupported, "database broker protocol version is unsupported")
	}
	if envelope.RequestID != requestID {
		return NewError(CodeIntegrity, "database broker response request ID does not match")
	}
	if envelope.BrokerEpoch != epoch {
		return NewError(CodeConflict, "database broker epoch changed during request")
	}
	if (envelope.Error == nil) == (len(envelope.Payload) == 0) {
		return NewError(CodeIntegrity, "database broker response shape is invalid")
	}
	if envelope.Error != nil {
		if !envelope.Error.Code.Valid() || strings.TrimSpace(envelope.Error.Message) == "" {
			return NewError(CodeIntegrity, "database broker returned an invalid structured error")
		}
		return nil
	}
	if !jsonObject(envelope.Payload) {
		return NewError(CodeIntegrity, "database broker response payload is invalid")
	}
	return nil
}

func validRequestID(value string) bool {
	if value == "" || len(value) > maxRequestIDBytes {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validProtocolName(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			index > 0 && (character == '-' || character == '_' || character == '.') {
			continue
		}
		return false
	}
	return true
}

func validIdempotencyKey(value string) bool {
	if value == "" || len(value) > maxIdempotencyKeyBytes || value != strings.TrimSpace(value) {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func jsonObject(raw []byte) bool {
	return len(raw) >= 2 && raw[0] == '{' && raw[len(raw)-1] == '}'
}
