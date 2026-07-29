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

func TestStoreRoutingDispatchCapabilitiesUnsupported(t *testing.T) {
	t.Parallel()

	var store Store
	if err := store.RenewRoutingLease(
		context.Background(),
		"ev_00000000000000000000000000000000",
		"lease_worker_00000000000000000000000000000000",
		time.Minute,
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("RenewRoutingLease() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.CreateDispatchForRoutingClaim(
		context.Background(),
		"ev_00000000000000000000000000000000",
		"lease_worker_00000000000000000000000000000000",
		"workflows/test.yaml",
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("CreateDispatchForRoutingClaim() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.CreateRevisionedDispatchForRoutingClaim(
		context.Background(),
		"ev_00000000000000000000000000000000",
		"lease_worker_00000000000000000000000000000000",
		"workflows/test.yaml",
		"sha256:revision",
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf(
			"CreateRevisionedDispatchForRoutingClaim() error = %v, want %v",
			err,
			ErrUnsupportedPlatform,
		)
	}
}
