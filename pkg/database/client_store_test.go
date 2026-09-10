package database

import (
	"context"
	"testing"
)

func TestCallStoreRejectsInvalidIdentityBeforeDial(t *testing.T) {
	t.Parallel()

	client := &Client{manifest: Manifest{}}
	for _, storeID := range []StoreID{"", "invalid StoreID"} {
		if err := client.CallStore(
			context.Background(), storeID, "domain", 1, "read", EmptyPayload{}, nil,
		); CodeOf(err) != CodeInvalid {
			t.Errorf("CallStore StoreID %q error = %v, want Invalid", storeID, err)
		}
	}
	if err := client.CallWithOptions(
		context.Background(), ControlDomain, ControlVersion, ControlOperationPing,
		EmptyPayload{}, nil, CallOptions{StoreID: "global/auth"},
	); CodeOf(err) != CodeInvalid {
		t.Fatalf("scoped control CallWithOptions error = %v, want Invalid", err)
	}
	rediscovering := &Client{home: t.TempDir(), rediscover: true}
	if err := rediscovering.CallWithOptions(
		context.Background(), "domain", 1, "read", EmptyPayload{}, nil,
		CallOptions{StoreID: "invalid StoreID"},
	); CodeOf(err) != CodeInvalid {
		t.Fatalf("pre-discovery StoreID validation error = %v, want Invalid", err)
	}
	if err := client.CallStoreWithOptions(
		context.Background(), "workspace/reviews", "reviews", 1, "save",
		EmptyPayload{}, nil, CallOptions{StoreID: "workspace/other", Mutation: true},
	); CodeOf(err) != CodeInvalid {
		t.Fatalf("conflicting CallStoreWithOptions target error = %v, want Invalid", err)
	}
	if err := client.CallStoreWithOptions(
		context.Background(), "invalid StoreID", "reviews", 1, "save",
		EmptyPayload{}, nil, CallOptions{Mutation: true},
	); CodeOf(err) != CodeInvalid {
		t.Fatalf("invalid CallStoreWithOptions target error = %v, want Invalid", err)
	}

	var nilClient *Client
	if err := nilClient.CallStore(
		context.Background(), "global/auth", "domain", 1, "read", EmptyPayload{}, nil,
	); CodeOf(err) != CodeUnavailable {
		t.Fatalf("nil CallStore error = %v, want Unavailable", err)
	}
	if err := nilClient.CallStoreWithOptions(
		context.Background(), "global/auth", "domain", 1, "write",
		EmptyPayload{}, nil, CallOptions{Mutation: true},
	); CodeOf(err) != CodeUnavailable {
		t.Fatalf("nil CallStoreWithOptions error = %v, want Unavailable", err)
	}
}
