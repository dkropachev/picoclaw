//go:build mipsle || netbsd || (freebsd && arm)

package eventing

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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

func TestStorePRDevelopmentAttentionUnsupported(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	var store Store
	assertUnsupported := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, ErrUnsupportedPlatform) {
			t.Fatalf("%s error = %v, want %v", name, err, ErrUnsupportedPlatform)
		}
	}
	if _, err := store.GetPRDevelopmentAttentionSnapshot(
		ctx,
		"pdc_00000000000000000000000000000000",
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("GetPRDevelopmentAttentionSnapshot() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	_, err := store.GetPRDevelopmentAttentionTrigger(ctx, "pdl_00000000000000000000000000000000")
	assertUnsupported("GetPRDevelopmentAttentionTrigger()", err)
	_, err = store.GetCurrentPRDevelopmentAttentionTriggerForCase(
		ctx,
		"pdc_00000000000000000000000000000000",
	)
	assertUnsupported("GetCurrentPRDevelopmentAttentionTriggerForCase()", err)
	_, _, err = store.GetClaimedPRDevelopmentAttentionSnapshot(
		ctx,
		"pdl_00000000000000000000000000000000",
		"lease",
	)
	assertUnsupported("GetClaimedPRDevelopmentAttentionSnapshot()", err)
	_, err = store.ClaimPRDevelopmentAttentionTriggers(ctx, "worker", 1, time.Minute)
	assertUnsupported("ClaimPRDevelopmentAttentionTriggers()", err)
	assertUnsupported(
		"RenewPRDevelopmentAttentionTriggerLease()",
		store.RenewPRDevelopmentAttentionTriggerLease(
			ctx,
			"pdl_00000000000000000000000000000000",
			"lease",
			time.Minute,
		),
	)
	_, err = store.PinPRDevelopmentAttentionTriggerPolicy(
		ctx,
		PRDevelopmentAttentionPolicyPin{},
	)
	assertUnsupported("PinPRDevelopmentAttentionTriggerPolicy()", err)
	_, err = store.PinPRDevelopmentAttentionTriggerSubject(
		ctx,
		PRDevelopmentAttentionSubjectPin{},
	)
	assertUnsupported("PinPRDevelopmentAttentionTriggerSubject()", err)
	assertUnsupported(
		"ReleasePRDevelopmentAttentionTrigger()",
		store.ReleasePRDevelopmentAttentionTrigger(
			ctx,
			PRDevelopmentAttentionTriggerRelease{},
		),
	)
	assertUnsupported(
		"CompletePRDevelopmentAttentionTrigger()",
		store.CompletePRDevelopmentAttentionTrigger(
			ctx,
			PRDevelopmentAttentionTriggerCompletion{},
		),
	)
	if _, err := store.GetPRDevelopmentAttentionDecisionRun(
		ctx,
		PRDevelopmentAttentionDecisionKey{},
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("GetPRDevelopmentAttentionDecisionRun() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
	if _, _, err := store.AdmitPRDevelopmentAttentionDecisionRun(
		ctx,
		PRDevelopmentAttentionDecisionRunAdmission{},
		func(context.Context) error { return nil },
	); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("AdmitPRDevelopmentAttentionDecisionRun() error = %v, want %v", err, ErrUnsupportedPlatform)
	}
}

func TestStorePRDevelopmentPublicationUnsupported(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	var store Store
	assertUnsupported := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, ErrUnsupportedPlatform) {
			t.Fatalf("%s error = %v, want %v", name, err, ErrUnsupportedPlatform)
		}
	}
	assertPublication := func(
		name string,
		publication PRDevelopmentPublication,
		changed bool,
		err error,
	) {
		t.Helper()
		assertUnsupported(name, err)
		if !reflect.DeepEqual(publication, PRDevelopmentPublication{}) {
			t.Fatalf("%s publication = %#v, want zero value", name, publication)
		}
		if changed {
			t.Fatalf("%s changed = true, want false", name)
		}
	}

	publication, err := store.GetPRDevelopmentPublication(ctx, "publication")
	assertPublication("GetPRDevelopmentPublication()", publication, false, err)
	publication, err = store.GetPRDevelopmentPublicationForReview(ctx, "review")
	assertPublication(
		"GetPRDevelopmentPublicationForReview()",
		publication,
		false,
		err,
	)
	authentication, err := store.AuthenticateClaimedPRDevelopmentPublicationGate(
		ctx,
		"publication",
		"claim",
		1,
	)
	assertUnsupported(
		"AuthenticateClaimedPRDevelopmentPublicationGate()",
		err,
	)
	if !reflect.DeepEqual(authentication, PRDevelopmentPublicationGateAuthentication{}) {
		t.Fatalf(
			"AuthenticateClaimedPRDevelopmentPublicationGate() = %#v, want zero value",
			authentication,
		)
	}
	snapshot, err := store.GetClaimedPRDevelopmentPublicationGateContextSnapshot(
		ctx,
		"publication",
		"claim",
		1,
	)
	assertUnsupported(
		"GetClaimedPRDevelopmentPublicationGateContextSnapshot()",
		err,
	)
	if !reflect.DeepEqual(snapshot, PRDevelopmentPublicationGateContextSnapshot{}) {
		t.Fatalf(
			"GetClaimedPRDevelopmentPublicationGateContextSnapshot() = %#v, want zero value",
			snapshot,
		)
	}
	publications, err := store.ClaimPRDevelopmentPublications(
		ctx,
		PRDevelopmentPublicationClaimRequest{},
	)
	assertUnsupported("ClaimPRDevelopmentPublications()", err)
	if publications != nil {
		t.Fatalf("ClaimPRDevelopmentPublications() = %#v, want nil", publications)
	}
	assertUnsupported(
		"RenewPRDevelopmentPublication()",
		store.RenewPRDevelopmentPublication(ctx, PRDevelopmentPublicationRenew{}),
	)
	assertUnsupported(
		"RenewPRDevelopmentPublicationPush()",
		store.RenewPRDevelopmentPublicationPush(ctx, PRDevelopmentPublicationRenew{}),
	)
	publication, changed, err := store.PinPRDevelopmentPublicationPolicy(
		ctx,
		PRDevelopmentPublicationPolicyPin{},
	)
	assertPublication("PinPRDevelopmentPublicationPolicy()", publication, changed, err)
	publication, changed, err = store.PinPRDevelopmentPublicationSubject(
		ctx,
		PRDevelopmentPublicationSubjectPin{},
	)
	assertPublication("PinPRDevelopmentPublicationSubject()", publication, changed, err)
	publication, changed, err = store.PinPRDevelopmentPublicationProvider(
		ctx,
		PRDevelopmentPublicationProviderPin{},
	)
	assertPublication("PinPRDevelopmentPublicationProvider()", publication, changed, err)
	publication, changed, err = store.ReleasePRDevelopmentPublicationGateWait(
		ctx,
		PRDevelopmentPublicationGateWait{},
	)
	assertPublication(
		"ReleasePRDevelopmentPublicationGateWait()",
		publication,
		changed,
		err,
	)
	publication, changed, err = store.MarkPRDevelopmentPublicationPushReady(
		ctx,
		PRDevelopmentPublicationMarkPushReady{},
	)
	assertPublication(
		"MarkPRDevelopmentPublicationPushReady()",
		publication,
		changed,
		err,
	)
	publication, changed, err = store.CompletePRDevelopmentPublicationPrestart(
		ctx,
		PRDevelopmentPublicationPrestartCompletion{},
	)
	assertPublication(
		"CompletePRDevelopmentPublicationPrestart()",
		publication,
		changed,
		err,
	)
	publication, changed, err = store.StartPRDevelopmentPublicationPush(
		ctx,
		PRDevelopmentPublicationPushStart{},
	)
	assertPublication("StartPRDevelopmentPublicationPush()", publication, changed, err)
	publication, changed, err = store.FinalizePRDevelopmentPublicationPush(
		ctx,
		PRDevelopmentPublicationPushFinalize{},
	)
	assertPublication(
		"FinalizePRDevelopmentPublicationPush()",
		publication,
		changed,
		err,
	)
	publications, err = store.ExpirePRDevelopmentPublicationPushes(ctx, 1)
	assertUnsupported("ExpirePRDevelopmentPublicationPushes()", err)
	if publications != nil {
		t.Fatalf(
			"ExpirePRDevelopmentPublicationPushes() = %#v, want nil",
			publications,
		)
	}
	publication, changed, err = store.ReconcilePRDevelopmentPublicationOutcome(
		ctx,
		PRDevelopmentPublicationOutcomeReconciliation{},
	)
	assertPublication(
		"ReconcilePRDevelopmentPublicationOutcome()",
		publication,
		changed,
		err,
	)
	link, err := store.GetPRDevelopmentPublicationDecisionRun(
		ctx,
		PRDevelopmentPublicationDecisionKey{},
	)
	assertUnsupported("GetPRDevelopmentPublicationDecisionRun()", err)
	if !reflect.DeepEqual(link, PRDevelopmentPublicationDecisionRunLink{}) {
		t.Fatalf(
			"GetPRDevelopmentPublicationDecisionRun() = %#v, want zero value",
			link,
		)
	}
	createCalled := false
	link, existed, err := store.AdmitPRDevelopmentPublicationDecisionRun(
		ctx,
		PRDevelopmentPublicationDecisionRunAdmission{},
		func(context.Context) error {
			createCalled = true
			return nil
		},
	)
	assertUnsupported("AdmitPRDevelopmentPublicationDecisionRun()", err)
	if !reflect.DeepEqual(link, PRDevelopmentPublicationDecisionRunLink{}) {
		t.Fatalf(
			"AdmitPRDevelopmentPublicationDecisionRun() = %#v, want zero value",
			link,
		)
	}
	if existed {
		t.Fatal("AdmitPRDevelopmentPublicationDecisionRun() existed = true, want false")
	}
	if createCalled {
		t.Fatal("AdmitPRDevelopmentPublicationDecisionRun() invoked create callback")
	}
}

func TestPRDevelopmentPublicationUnsupportedSurfaceIsJSONPrivate(t *testing.T) {
	t.Parallel()

	const sentinel = "private-publication-sentinel"
	values := []any{
		PRDevelopmentPublication{ID: sentinel},
		PRDevelopmentPublicationGateContextSnapshot{
			TranscriptDigest: sentinel,
		},
		PRDevelopmentPublicationProviderObservation{Repository: sentinel},
		PRDevelopmentPublicationRemoteObservation{Repository: sentinel},
		PRDevelopmentPublicationPushRequest{Repository: sentinel},
		PRDevelopmentPublicationPushResult{WorkspaceID: sentinel},
		PRDevelopmentPublicationClaimRequest{WorkerLabel: sentinel},
		PRDevelopmentPublicationRenew{PublicationID: sentinel},
		PRDevelopmentPublicationPolicyPin{PublicationID: sentinel},
		PRDevelopmentPublicationSubjectPin{PublicationID: sentinel},
		PRDevelopmentPublicationProviderPin{PublicationID: sentinel},
		PRDevelopmentPublicationGateWait{PublicationID: sentinel},
		PRDevelopmentPublicationMarkPushReady{PublicationID: sentinel},
		PRDevelopmentPublicationPrestartCompletion{PublicationID: sentinel},
		PRDevelopmentPublicationPushStart{PublicationID: sentinel},
		PRDevelopmentPublicationPushFinalize{PublicationID: sentinel},
		PRDevelopmentPublicationOutcomeReconciliation{PublicationID: sentinel},
		PRDevelopmentPublicationDecisionKey{PublicationID: sentinel},
		PRDevelopmentPublicationDecisionRunAdmission{RunID: sentinel},
		PRDevelopmentPublicationDecisionRunLink{RunID: sentinel},
	}
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("json.Marshal(%T) error = %v", value, err)
		}
		if string(encoded) != `{}` {
			t.Fatalf("json.Marshal(%T) = %s, want {}", value, encoded)
		}
	}
}
