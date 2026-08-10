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

func TestStorePRDevelopmentRepairOrchestrationUnsupported(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var store Store
	assertUnsupported := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, ErrUnsupportedPlatform) {
			t.Fatalf("%s error = %v, want %v", name, err, ErrUnsupportedPlatform)
		}
	}
	_, _, err := store.ClaimPRDevelopmentRepairOrchestration(
		ctx, PRDevelopmentRepairOrchestrationClaim{},
	)
	assertUnsupported("ClaimPRDevelopmentRepairOrchestration()", err)
	assertUnsupported("RenewPRDevelopmentRepairOrchestration()",
		store.RenewPRDevelopmentRepairOrchestration(
			ctx, PRDevelopmentRepairOrchestrationRenew{},
		))
	_, err = store.GetPRDevelopmentRepairOrchestration(ctx, "attempt")
	assertUnsupported("GetPRDevelopmentRepairOrchestration()", err)
	_, _, err = store.PinPRDevelopmentRepairOrchestration(
		ctx, PRDevelopmentRepairOrchestrationPin{},
	)
	assertUnsupported("PinPRDevelopmentRepairOrchestration()", err)
	_, _, err = store.AcquirePRDevelopmentRepairOrchestrationController(
		ctx, PRDevelopmentRepairOrchestrationControllerAcquire{},
	)
	assertUnsupported("AcquirePRDevelopmentRepairOrchestrationController()", err)
	_, _, err = store.StartPRDevelopmentRepairOrchestrationModel(
		ctx, PRDevelopmentRepairOrchestrationModelStart{},
	)
	assertUnsupported("StartPRDevelopmentRepairOrchestrationModel()", err)
	_, _, err = store.CompletePRDevelopmentRepairOrchestrationModel(
		ctx, PRDevelopmentRepairOrchestrationModelComplete{},
	)
	assertUnsupported("CompletePRDevelopmentRepairOrchestrationModel()", err)
	_, _, err = store.RecordPRDevelopmentRepairOrchestrationValidation(
		ctx, PRDevelopmentRepairOrchestrationValidation{},
	)
	assertUnsupported("RecordPRDevelopmentRepairOrchestrationValidation()", err)
	_, _, err = store.FailPRDevelopmentRepairOrchestration(
		ctx, PRDevelopmentRepairOrchestrationFail{},
	)
	assertUnsupported("FailPRDevelopmentRepairOrchestration()", err)
}

func TestStorePRDevelopmentControllerUnsupported(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	var store Store
	if _, err := store.GetPRDevelopmentControllerForCase(
		ctx,
		"pdc_00000000000000000000000000000000",
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("GetPRDevelopmentControllerForCase() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.AcquirePRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerAcquire{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("AcquirePRDevelopmentControllerLease() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if err := store.RenewPRDevelopmentControllerLease(
		ctx,
		PRDevelopmentControllerRenew{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("RenewPRDevelopmentControllerLease() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.PreparePRDevelopmentControllerOperation(
		ctx,
		PRDevelopmentControllerOperationPrepare{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("PreparePRDevelopmentControllerOperation() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.FinalizePRDevelopmentControllerOperation(
		ctx,
		PRDevelopmentControllerOperationFinalize{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("FinalizePRDevelopmentControllerOperation() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.ClaimPRDevelopmentControllerOperationRecovery(
		ctx,
		PRDevelopmentControllerOperationRecoveryClaim{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("ClaimPRDevelopmentControllerOperationRecovery() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if err := store.RenewPRDevelopmentControllerOperationRecovery(
		ctx,
		PRDevelopmentControllerOperationRecoveryRenew{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("RenewPRDevelopmentControllerOperationRecovery() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.FinalizePRDevelopmentControllerOperationRecovery(
		ctx,
		PRDevelopmentControllerOperationRecoveryFinalize{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("FinalizePRDevelopmentControllerOperationRecovery() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.ClaimPRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryClaim{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("ClaimPRDevelopmentControllerRecovery() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if err := store.RenewPRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryRenew{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("RenewPRDevelopmentControllerRecovery() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.FinalizePRDevelopmentControllerRecovery(
		ctx,
		PRDevelopmentControllerRecoveryFinalize{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("FinalizePRDevelopmentControllerRecovery() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.BindPRDevelopmentControllerLine(
		ctx,
		PRDevelopmentControllerLineBind{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("BindPRDevelopmentControllerLine() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.RecordPRDevelopmentAttemptReviewFence(
		ctx,
		PRDevelopmentAttemptReviewFenceRecord{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("RecordPRDevelopmentAttemptReviewFence() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.FinishPRDevelopmentControllerReview(
		ctx,
		PRDevelopmentControllerReviewTransition{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("FinishPRDevelopmentControllerReview() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, err := store.ReleasePRDevelopmentControllerReview(
		ctx,
		PRDevelopmentControllerReviewTransition{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("ReleasePRDevelopmentControllerReview() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
}

func TestStorePRDevelopmentLedgerUnsupported(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	var store Store
	if _, err := store.GetPRDevelopmentLedgerForCase(
		ctx,
		"pdc_00000000000000000000000000000000",
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("GetPRDevelopmentLedgerForCase() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, err := store.GetPRDevelopmentContextSnapshot(
		ctx,
		"pdc_00000000000000000000000000000000",
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("GetPRDevelopmentContextSnapshot() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.AppendPRDevelopmentLedgerAttempt(
		ctx,
		PRDevelopmentLedgerAttemptAppend{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("AppendPRDevelopmentLedgerAttempt() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.AppendPRDevelopmentLedgerReview(
		ctx,
		PRDevelopmentLedgerReviewAppend{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("AppendPRDevelopmentLedgerReview() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.ClaimPRDevelopmentReview(
		ctx,
		PRDevelopmentReviewClaimRequest{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("ClaimPRDevelopmentReview() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.CompletePRDevelopmentReview(
		ctx,
		PRDevelopmentLedgerReviewAppend{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("CompletePRDevelopmentReview() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.AppendPRDevelopmentLedgerCheckpoint(
		ctx,
		PRDevelopmentLedgerCheckpointAppend{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("AppendPRDevelopmentLedgerCheckpoint() error = %v, want %v", err, ErrUnsupportedPlatform)
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
