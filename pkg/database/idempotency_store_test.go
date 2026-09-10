package database

import (
	"context"
	"testing"
)

func TestIdempotencyIdentityIncludesStoreID(t *testing.T) {
	t.Parallel()

	first := RequestEnvelope{
		RequestID: "request", StoreID: "workspace/repository-reviews",
		Domain: "repository-reviews", DomainVersion: 1, Operation: "save",
		IdempotencyKey: "stable", Payload: []byte(`{"value":1}`),
	}
	second := first
	second.StoreID = "workspace/0123456789abcdef/repository-reviews"
	if idempotencyOperationKey(first) == idempotencyOperationKey(second) {
		t.Fatal("idempotency operation key did not bind StoreID")
	}
	if idempotencyRequestFingerprint(first) == idempotencyRequestFingerprint(second) {
		t.Fatal("idempotency request fingerprint did not bind StoreID")
	}

	unscoped := first
	unscoped.StoreID = ""
	if idempotencyOperationKey(first) == idempotencyOperationKey(unscoped) ||
		idempotencyRequestFingerprint(first) == idempotencyRequestFingerprint(unscoped) {
		t.Fatal("scoped and compatibility idempotency identities overlap")
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
