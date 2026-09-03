package database

import (
	"context"
	"crypto/sha256"
	"strconv"
	"sync"
)

const (
	maxIdempotencyRecords     = 4096
	maxIdempotencyResultBytes = 64 << 20
)

type idempotencyRecord struct {
	fingerprint [sha256.Size]byte
	ready       chan struct{}
	response    ResponseEnvelope
	shutdown    bool
}

// idempotencyRegistry retains completed outcomes for the complete broker
// epoch. It never evicts a key: eviction could turn a late replay into a
// duplicate mutation. Once its fixed bounds are reached, new keyed mutations
// fail before dispatch.
type idempotencyRegistry struct {
	mu          sync.Mutex
	records     map[string]*idempotencyRecord
	resultBytes int
}

func newIdempotencyRegistry() *idempotencyRegistry {
	return &idempotencyRegistry{records: make(map[string]*idempotencyRecord)}
}

func (registry *idempotencyRegistry) begin(
	ctx context.Context,
	envelope RequestEnvelope,
) (*idempotencyRecord, *ResponseEnvelope, bool, error) {
	if envelope.IdempotencyKey == "" {
		return nil, nil, false, nil
	}
	if registry == nil {
		return nil, nil, false, NewError(CodeUnavailable, "database idempotency registry is unavailable")
	}
	key := idempotencyOperationKey(envelope)
	fingerprint := idempotencyRequestFingerprint(envelope)
	registry.mu.Lock()
	if existing := registry.records[key]; existing != nil {
		if existing.fingerprint != fingerprint {
			registry.mu.Unlock()
			return nil, nil, false, NewError(
				CodeConflict,
				"database idempotency key was reused for a different request",
			)
		}
		ready := existing.ready
		registry.mu.Unlock()
		select {
		case <-ready:
			registry.mu.Lock()
			response := cloneResponseEnvelope(existing.response)
			shutdown := existing.shutdown
			registry.mu.Unlock()
			return nil, &response, shutdown, nil
		case <-ctx.Done():
			return nil, nil, false, NewError(CodeDeadline, "database request deadline was exceeded")
		}
	}
	if len(registry.records) >= maxIdempotencyRecords {
		registry.mu.Unlock()
		return nil, nil, false, NewError(CodeUnavailable, "database idempotency registry is full")
	}
	record := &idempotencyRecord{fingerprint: fingerprint, ready: make(chan struct{})}
	registry.records[key] = record
	registry.mu.Unlock()
	return record, nil, false, nil
}

func (registry *idempotencyRegistry) complete(
	record *idempotencyRecord,
	response ResponseEnvelope,
	shutdown bool,
) (ResponseEnvelope, bool) {
	if registry == nil || record == nil {
		return response, shutdown
	}
	registry.mu.Lock()
	stored := cloneResponseEnvelope(response)
	resultBytes := responseEnvelopeBytes(stored)
	if resultBytes > maxIdempotencyResultBytes-registry.resultBytes {
		stored.Payload = nil
		stored.Error = NewError(
			CodeOutcomeUnknown,
			"database mutation committed but its replay result exceeded broker bounds",
		)
		resultBytes = responseEnvelopeBytes(stored)
		shutdown = false
	}
	record.response = stored
	record.shutdown = shutdown
	registry.resultBytes += resultBytes
	close(record.ready)
	response = cloneResponseEnvelope(stored)
	registry.mu.Unlock()
	return response, shutdown
}

func idempotencyOperationKey(envelope RequestEnvelope) string {
	return envelope.Domain + "\x00" + strconv.Itoa(envelope.DomainVersion) + "\x00" +
		envelope.Operation + "\x00" + envelope.IdempotencyKey
}

func idempotencyRequestFingerprint(envelope RequestEnvelope) [sha256.Size]byte {
	hash := sha256.New()
	_, _ = hash.Write([]byte(envelope.RequestID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(envelope.Payload)
	var fingerprint [sha256.Size]byte
	copy(fingerprint[:], hash.Sum(nil))
	return fingerprint
}

func cloneResponseEnvelope(response ResponseEnvelope) ResponseEnvelope {
	response.Payload = append([]byte(nil), response.Payload...)
	if response.Error != nil {
		response.Error = NewError(response.Error.Code, response.Error.Message)
	}
	return response
}

func responseEnvelopeBytes(response ResponseEnvelope) int {
	size := len(response.Payload) + len(response.RequestID) + len(response.BrokerEpoch) + 32
	if response.Error != nil {
		size += len(response.Error.Code) + len(response.Error.Message)
	}
	return size
}
