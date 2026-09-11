package database

import (
	"context"
	"testing"
)

func TestIdempotencyFingerprintBindsSemanticMutation(t *testing.T) {
	t.Parallel()

	base := RequestEnvelope{
		RequestID: "request-a", StoreID: "workspace/repository-reviews",
		Domain: "repository-reviews", DomainVersion: 1, Operation: "save",
		DeadlineUnixNs: 100, IdempotencyKey: "stable", Payload: []byte(`{"value":1}`),
	}
	retry := base
	retry.RequestID = "request-b"
	retry.DeadlineUnixNs = 200
	if idempotencyRequestFingerprint(base) != idempotencyRequestFingerprint(retry) {
		t.Fatal("transport request ID or deadline changed the semantic mutation fingerprint")
	}

	tests := []struct {
		name   string
		mutate func(*RequestEnvelope)
	}{
		{name: "store", mutate: func(value *RequestEnvelope) {
			value.StoreID = "workspace/0123456789abcdef/repository-reviews"
		}},
		{name: "unscoped store", mutate: func(value *RequestEnvelope) { value.StoreID = "" }},
		{name: "domain", mutate: func(value *RequestEnvelope) { value.Domain = "other-domain" }},
		{name: "version", mutate: func(value *RequestEnvelope) { value.DomainVersion++ }},
		{name: "operation", mutate: func(value *RequestEnvelope) { value.Operation = "replace" }},
		{name: "key", mutate: func(value *RequestEnvelope) { value.IdempotencyKey = "other-key" }},
		{name: "payload", mutate: func(value *RequestEnvelope) { value.Payload = []byte(`{"value":2}`) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			changed := base
			test.mutate(&changed)
			if idempotencyRequestFingerprint(base) == idempotencyRequestFingerprint(changed) {
				t.Fatalf("semantic mutation fingerprint did not bind %s", test.name)
			}
		})
	}
}

func TestIdempotencyOperationScopeNeverReplaysAnotherMutation(t *testing.T) {
	t.Parallel()

	base := RequestEnvelope{
		RequestID: "request-a", StoreID: "workspace/repository-reviews",
		Domain: "repository-reviews", DomainVersion: 1, Operation: "save",
		IdempotencyKey: "stable", Payload: []byte(`{"value":1}`),
	}
	tests := []struct {
		name   string
		mutate func(*RequestEnvelope)
	}{
		{name: "store", mutate: func(value *RequestEnvelope) {
			value.StoreID = "workspace/0123456789abcdef/repository-reviews"
		}},
		{name: "domain", mutate: func(value *RequestEnvelope) { value.Domain = "other-domain" }},
		{name: "version", mutate: func(value *RequestEnvelope) { value.DomainVersion++ }},
		{name: "operation", mutate: func(value *RequestEnvelope) { value.Operation = "replace" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			registry := newIdempotencyRegistry()
			firstRecord, replay, _, err := registry.begin(context.Background(), base)
			if err != nil || firstRecord == nil || replay != nil {
				t.Fatalf("first admission = %#v, %#v, %v", firstRecord, replay, err)
			}
			changed := base
			changed.RequestID = "request-b"
			test.mutate(&changed)
			changedRecord, replay, _, err := registry.begin(context.Background(), changed)
			if err != nil || changedRecord == nil || changedRecord == firstRecord || replay != nil {
				t.Fatalf("changed admission = %#v, %#v, %v", changedRecord, replay, err)
			}
			if len(registry.records) != 2 {
				t.Fatalf("idempotency records = %d, want 2", len(registry.records))
			}
		})
	}
}

func TestIdempotencyRegistrySeparatesStores(t *testing.T) {
	t.Parallel()

	registry := newIdempotencyRegistry()
	first := RequestEnvelope{
		RequestID: "request-a", StoreID: "workspace/repository-reviews",
		Domain: "repository-reviews", DomainVersion: 1, Operation: "save",
		IdempotencyKey: "stable", Payload: []byte(`{"value":1}`),
	}
	second := first
	second.RequestID = "request-b"
	second.StoreID = "workspace/0123456789abcdef/repository-reviews"

	firstRecord, replay, _, err := registry.begin(context.Background(), first)
	if err != nil || firstRecord == nil || replay != nil {
		t.Fatalf("first store admission = %#v, %#v, %v", firstRecord, replay, err)
	}
	secondRecord, replay, _, err := registry.begin(context.Background(), second)
	if err != nil || secondRecord == nil || replay != nil || secondRecord == firstRecord {
		t.Fatalf("second store admission = %#v, %#v, %v", secondRecord, replay, err)
	}
	if len(registry.records) != 2 {
		t.Fatalf("idempotency records = %d, want 2", len(registry.records))
	}
}
