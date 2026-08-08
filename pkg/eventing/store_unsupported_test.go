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
	if _, err := store.GetDispatchMetadata(
		context.Background(),
		"dsp_00000000000000000000000000000000",
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf(
			"GetDispatchMetadata() error = %v, want %v",
			err,
			ErrUnsupportedPlatform,
		)
	}
}

func TestStoreGetReviewCaseUnsupported(t *testing.T) {
	t.Parallel()

	var store Store
	if _, err := store.GetReviewCase(
		context.Background(),
		"prc_00000000000000000000000000000000",
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf(
			"GetReviewCase() error = %v, want %v",
			err,
			ErrUnsupportedPlatform,
		)
	}
}

func TestStorePRDevelopmentConversationUnsupported(t *testing.T) {
	t.Parallel()

	var store Store
	if _, err := store.GetPRDevelopmentConversation(
		context.Background(),
		"pdc_00000000000000000000000000000000",
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf(
			"GetPRDevelopmentConversation() error = %v, want %v",
			err,
			ErrUnsupportedPlatform,
		)
	}
	if _, err := store.AppendPRDevelopmentMessage(
		context.Background(),
		PRDevelopmentMessageAppend{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf(
			"AppendPRDevelopmentMessage() error = %v, want %v",
			err,
			ErrUnsupportedPlatform,
		)
	}
}

func TestStorePRDevelopmentRepairUnsupported(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	var store Store
	if _, err := store.GetPRDevelopmentWorkbench(
		ctx,
		"pdc_00000000000000000000000000000000",
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("GetPRDevelopmentWorkbench() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.AdmitPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairAdmit{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("AdmitPRDevelopmentRepair() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.ClaimPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairClaimRequest{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("ClaimPRDevelopmentRepair() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if err := store.RenewPRDevelopmentRepairLease(
		ctx,
		"pdr_00000000000000000000000000000000",
		"lease",
		time.Minute,
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("RenewPRDevelopmentRepairLease() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, err := store.PinPRDevelopmentRepairSession(
		ctx,
		PRDevelopmentRepairPin{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("PinPRDevelopmentRepairSession() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, err := store.BeginPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairBegin{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("BeginPRDevelopmentRepair() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, err := store.FinishPRDevelopmentRepair(
		ctx,
		PRDevelopmentRepairOutcome{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("FinishPRDevelopmentRepair() error = %v, want %v", err, ErrUnsupportedPlatform)
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
