//go:build mipsle || netbsd || (freebsd && arm)

package eventing

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStoreRenewDispatchLeaseUnsupported(t *testing.T) {
	t.Parallel()
	var store Store
	err := store.RenewDispatchLease(
		context.Background(),
		"dsp_00000000000000000000000000000000",
		"lease_worker_00000000000000000000000000000000",
		time.Minute,
	)
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("RenewDispatchLease() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
}

func TestStoreGetDispatchMetadataUnsupported(t *testing.T) {
	t.Parallel()
	var store Store
	_, err := store.GetDispatchMetadata(
		context.Background(),
		"dsp_00000000000000000000000000000000",
	)
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("GetDispatchMetadata() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
}

func TestStoreRoutingDispatchCapabilitiesUnsupported(t *testing.T) {
	t.Parallel()
	var store Store
	ctx := context.Background()
	_, _, err := store.CreateDispatchForRoutingClaim(ctx, "event", "lease", "workflow")
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("CreateDispatchForRoutingClaim() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	_, _, err = store.CreateRevisionedDispatchForRoutingClaim(
		ctx, "event", "lease", "workflow", "revision",
	)
	if !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("CreateRevisionedDispatchForRoutingClaim() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
}
