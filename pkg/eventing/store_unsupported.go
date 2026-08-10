//go:build mipsle || netbsd || (freebsd && arm)

package eventing

import (
	"context"
	"time"
)

// Store is unavailable on targets unsupported by modernc SQLite.
type Store struct{}

var (
	_ Inbox                                  = (*Store)(nil)
	_ EventOperatorReader                    = (*Store)(nil)
	_ DispatchOperatorReader                 = (*Store)(nil)
	_ DispatchOperatorGetter                 = (*Store)(nil)
	_ RevisionRoutingDispatchCreator         = (*Store)(nil)
	_ DispatchLeaseRenewer                   = (*Store)(nil)
	_ ReviewStore                            = (*Store)(nil)
	_ ReviewDecisionRunStore                 = (*Store)(nil)
	_ ReviewAttentionTriggerQueue            = (*Store)(nil)
	_ PRDevelopmentCaseStore                 = (*Store)(nil)
	_ PRDevelopmentCaseReader                = (*Store)(nil)
	_ PRDevelopmentThreadReader              = (*Store)(nil)
	_ PRDevelopmentConversationStore         = (*Store)(nil)
	_ PRDevelopmentWorkbenchReader           = (*Store)(nil)
	_ PRDevelopmentRepairAdmitter            = (*Store)(nil)
	_ PRDevelopmentRepairQueue               = (*Store)(nil)
	_ PRDevelopmentRepairOrchestrationStore  = (*Store)(nil)
	_ PRDevelopmentControllerReader          = (*Store)(nil)
	_ PRDevelopmentControllerStore           = (*Store)(nil)
	_ PRDevelopmentControllerOperationStore  = (*Store)(nil)
	_ PRDevelopmentLedgerReader              = (*Store)(nil)
	_ PRDevelopmentLedgerStore               = (*Store)(nil)
	_ PRDevelopmentReviewQueue               = (*Store)(nil)
	_ PRDevelopmentContextReader             = (*Store)(nil)
	_ PRDevelopmentAttentionSnapshotReader   = (*Store)(nil)
	_ PRDevelopmentAttentionDecisionRunStore = (*Store)(nil)
)

func Open(context.Context, string, ...Option) (*Store, error) {
	return nil, ErrUnsupportedPlatform
}

func OpenStore(ctx context.Context, path string, options ...Option) (*Store, error) {
	return Open(ctx, path, options...)
}

func (*Store) Close() error { return nil }

func (*Store) Insert(context.Context, Envelope) (InsertResult, error) {
	return InsertResult{}, ErrUnsupportedPlatform
}

func (*Store) Get(context.Context, string) (StoredEvent, error) {
	return StoredEvent{}, ErrUnsupportedPlatform
}

func (*Store) List(context.Context, EventFilter) (EventPage, error) {
	return EventPage{}, ErrUnsupportedPlatform
}

func (s *Store) ListEvents(ctx context.Context, filter EventFilter) (EventPage, error) {
	return s.List(ctx, filter)
}

func (*Store) GetEventMetadata(context.Context, string) (StoredEventMetadata, error) {
	return StoredEventMetadata{}, ErrUnsupportedPlatform
}

func (*Store) ListEventMetadata(context.Context, EventFilter) (EventMetadataPage, error) {
	return EventMetadataPage{}, ErrUnsupportedPlatform
}

func (*Store) GetEventPayload(context.Context, string) ([]byte, error) {
	return nil, ErrUnsupportedPlatform
}

func (*Store) ClaimRouting(context.Context, string, int, time.Duration) ([]StoredEvent, error) {
	return nil, ErrUnsupportedPlatform
}

func (*Store) AckRouting(context.Context, string, string) error {
	return ErrUnsupportedPlatform
}

func (*Store) NackRouting(context.Context, string, string, time.Time, string) error {
	return ErrUnsupportedPlatform
}

func (*Store) DeadRouting(context.Context, string, string, string) error {
	return ErrUnsupportedPlatform
}

func (*Store) CreateDispatchForRoutingClaim(
	context.Context,
	string,
	string,
	string,
) (Dispatch, bool, error) {
	return Dispatch{}, false, ErrUnsupportedPlatform
}

func (*Store) CreateRevisionedDispatchForRoutingClaim(
	context.Context,
	string,
	string,
	string,
	string,
) (Dispatch, bool, error) {
	return Dispatch{}, false, ErrUnsupportedPlatform
}

func (*Store) RenewRoutingLease(context.Context, string, string, time.Duration) error {
	return ErrUnsupportedPlatform
}

func (*Store) CreateDispatch(context.Context, string, string) (Dispatch, bool, error) {
	return Dispatch{}, false, ErrUnsupportedPlatform
}

func (*Store) GetDispatch(context.Context, string) (Dispatch, error) {
	return Dispatch{}, ErrUnsupportedPlatform
}

func (*Store) GetDispatchMetadata(context.Context, string) (DispatchMetadata, error) {
	return DispatchMetadata{}, ErrUnsupportedPlatform
}

func (*Store) ClaimDispatches(context.Context, string, int, time.Duration) ([]Dispatch, error) {
	return nil, ErrUnsupportedPlatform
}

func (*Store) LinkDispatchRun(context.Context, string, string, string) error {
	return ErrUnsupportedPlatform
}

func (*Store) RenewDispatchLease(context.Context, string, string, time.Duration) error {
	return ErrUnsupportedPlatform
}

func (*Store) FinishDispatch(context.Context, string, string, DispatchStatus, string) error {
	return ErrUnsupportedPlatform
}

func (*Store) NackDispatch(context.Context, string, string, time.Time, string) error {
	return ErrUnsupportedPlatform
}

func (*Store) ListDispatches(context.Context, DispatchFilter) (DispatchPage, error) {
	return DispatchPage{}, ErrUnsupportedPlatform
}

func (*Store) ListDispatchMetadata(
	context.Context,
	DispatchFilter,
) (DispatchMetadataPage, error) {
	return DispatchMetadataPage{}, ErrUnsupportedPlatform
}

func (*Store) Replay(context.Context, string) (InsertResult, error) {
	return InsertResult{}, ErrUnsupportedPlatform
}

func (*Store) Prune(context.Context, time.Time, int) (int64, error) {
	return 0, ErrUnsupportedPlatform
}

func (*Store) LookupPRDevelopmentCapture(
	context.Context,
	PRDevelopmentCaptureIdentity,
	*PRDevelopmentThreadIdentity,
) (PRDevelopmentCase, bool, error) {
	return PRDevelopmentCase{}, false, ErrUnsupportedPlatform
}

func (*Store) CapturePRDevelopmentCase(
	context.Context,
	PRDevelopmentCaptureRequest,
) (PRDevelopmentCase, bool, error) {
	return PRDevelopmentCase{}, false, ErrUnsupportedPlatform
}

func (*Store) GetPRDevelopmentThreadForCase(
	context.Context,
	string,
) (PRDevelopmentThread, error) {
	return PRDevelopmentThread{}, ErrUnsupportedPlatform
}

func (*Store) GetPRDevelopmentCase(
	context.Context,
	string,
) (PRDevelopmentCase, error) {
	return PRDevelopmentCase{}, ErrUnsupportedPlatform
}

func (*Store) ListPRDevelopmentCases(
	context.Context,
	PRDevelopmentCaseFilter,
) (PRDevelopmentCasePage, error) {
	return PRDevelopmentCasePage{}, ErrUnsupportedPlatform
}

func (*Store) GetPRDevelopmentConversation(
	context.Context,
	string,
) (PRDevelopmentConversation, error) {
	return PRDevelopmentConversation{}, ErrUnsupportedPlatform
}

func (*Store) AppendPRDevelopmentMessage(
	context.Context,
	PRDevelopmentMessageAppend,
) (PRDevelopmentConversation, error) {
	return PRDevelopmentConversation{}, ErrUnsupportedPlatform
}

func (*Store) GetPRDevelopmentWorkbench(
	context.Context,
	string,
) (PRDevelopmentWorkbench, error) {
	return PRDevelopmentWorkbench{}, ErrUnsupportedPlatform
}

func (*Store) AdmitPRDevelopmentRepair(
	context.Context,
	PRDevelopmentRepairAdmit,
) (PRDevelopmentWorkbench, bool, error) {
	return PRDevelopmentWorkbench{}, false, ErrUnsupportedPlatform
}

func (*Store) ClaimPRDevelopmentRepair(
	context.Context,
	PRDevelopmentRepairClaimRequest,
) (PRDevelopmentRepairSession, bool, error) {
	return PRDevelopmentRepairSession{}, false, ErrUnsupportedPlatform
}

func (*Store) RenewPRDevelopmentRepairLease(
	context.Context,
	string,
	string,
	time.Duration,
) error {
	return ErrUnsupportedPlatform
}

func (*Store) PinPRDevelopmentRepairSession(
	context.Context,
	PRDevelopmentRepairPin,
) (PRDevelopmentRepairSession, error) {
	return PRDevelopmentRepairSession{}, ErrUnsupportedPlatform
}

func (*Store) BeginPRDevelopmentRepair(
	context.Context,
	PRDevelopmentRepairBegin,
) (PRDevelopmentRepairSession, error) {
	return PRDevelopmentRepairSession{}, ErrUnsupportedPlatform
}

func (*Store) FinishPRDevelopmentRepair(
	context.Context,
	PRDevelopmentRepairOutcome,
) (PRDevelopmentRepairSession, error) {
	return PRDevelopmentRepairSession{}, ErrUnsupportedPlatform
}

func (*Store) ClaimPRDevelopmentRepairOrchestration(
	context.Context,
	PRDevelopmentRepairOrchestrationClaim,
) (PRDevelopmentRepairOrchestration, bool, error) {
	return PRDevelopmentRepairOrchestration{}, false, ErrUnsupportedPlatform
}

func (*Store) RenewPRDevelopmentRepairOrchestration(
	context.Context,
	PRDevelopmentRepairOrchestrationRenew,
) error {
	return ErrUnsupportedPlatform
}

func (*Store) GetPRDevelopmentRepairOrchestration(
	context.Context,
	string,
) (PRDevelopmentRepairOrchestration, error) {
	return PRDevelopmentRepairOrchestration{}, ErrUnsupportedPlatform
}

func (*Store) PinPRDevelopmentRepairOrchestration(
	context.Context,
	PRDevelopmentRepairOrchestrationPin,
) (PRDevelopmentRepairOrchestration, bool, error) {
	return PRDevelopmentRepairOrchestration{}, false, ErrUnsupportedPlatform
}

func (*Store) AcquirePRDevelopmentRepairOrchestrationController(
	context.Context,
	PRDevelopmentRepairOrchestrationControllerAcquire,
) (PRDevelopmentControllerLease, bool, error) {
	return PRDevelopmentControllerLease{}, false, ErrUnsupportedPlatform
}

func (*Store) StartPRDevelopmentRepairOrchestrationModel(
	context.Context,
	PRDevelopmentRepairOrchestrationModelStart,
) (PRDevelopmentRepairOrchestration, bool, error) {
	return PRDevelopmentRepairOrchestration{}, false, ErrUnsupportedPlatform
}

func (*Store) CompletePRDevelopmentRepairOrchestrationModel(
	context.Context,
	PRDevelopmentRepairOrchestrationModelComplete,
) (PRDevelopmentRepairOrchestration, bool, error) {
	return PRDevelopmentRepairOrchestration{}, false, ErrUnsupportedPlatform
}

func (*Store) RecordPRDevelopmentRepairOrchestrationValidation(
	context.Context,
	PRDevelopmentRepairOrchestrationValidation,
) (PRDevelopmentRepairOrchestration, bool, error) {
	return PRDevelopmentRepairOrchestration{}, false, ErrUnsupportedPlatform
}

func (*Store) FailPRDevelopmentRepairOrchestration(
	context.Context,
	PRDevelopmentRepairOrchestrationFail,
) (PRDevelopmentRepairOrchestration, bool, error) {
	return PRDevelopmentRepairOrchestration{}, false, ErrUnsupportedPlatform
}

func (*Store) GetPRDevelopmentControllerForCase(
	context.Context,
	string,
) (PRDevelopmentController, error) {
	return PRDevelopmentController{}, ErrUnsupportedPlatform
}

func (*Store) AcquirePRDevelopmentControllerLease(
	context.Context,
	PRDevelopmentControllerAcquire,
) (PRDevelopmentControllerLease, bool, error) {
	return PRDevelopmentControllerLease{}, false, ErrUnsupportedPlatform
}

func (*Store) RenewPRDevelopmentControllerLease(
	context.Context,
	PRDevelopmentControllerRenew,
) error {
	return ErrUnsupportedPlatform
}

func (*Store) PreparePRDevelopmentControllerOperation(
	context.Context,
	PRDevelopmentControllerOperationPrepare,
) (PRDevelopmentControllerOperation, bool, error) {
	return PRDevelopmentControllerOperation{}, false, ErrUnsupportedPlatform
}

func (*Store) FinalizePRDevelopmentControllerOperation(
	context.Context,
	PRDevelopmentControllerOperationFinalize,
) (PRDevelopmentControllerOperationTransition, bool, error) {
	return PRDevelopmentControllerOperationTransition{}, false, ErrUnsupportedPlatform
}

func (*Store) ClaimPRDevelopmentControllerOperationRecovery(
	context.Context,
	PRDevelopmentControllerOperationRecoveryClaim,
) (PRDevelopmentControllerOperationRecoveryLease, bool, error) {
	return PRDevelopmentControllerOperationRecoveryLease{}, false, ErrUnsupportedPlatform
}

func (*Store) RenewPRDevelopmentControllerOperationRecovery(
	context.Context,
	PRDevelopmentControllerOperationRecoveryRenew,
) error {
	return ErrUnsupportedPlatform
}

func (*Store) FinalizePRDevelopmentControllerOperationRecovery(
	context.Context,
	PRDevelopmentControllerOperationRecoveryFinalize,
) (PRDevelopmentControllerOperationTransition, bool, error) {
	return PRDevelopmentControllerOperationTransition{}, false, ErrUnsupportedPlatform
}

func (*Store) ClaimPRDevelopmentControllerRecovery(
	context.Context,
	PRDevelopmentControllerRecoveryClaim,
) (PRDevelopmentControllerRecoveryLease, bool, error) {
	return PRDevelopmentControllerRecoveryLease{}, false, ErrUnsupportedPlatform
}

func (*Store) RenewPRDevelopmentControllerRecovery(
	context.Context,
	PRDevelopmentControllerRecoveryRenew,
) error {
	return ErrUnsupportedPlatform
}

func (*Store) FinalizePRDevelopmentControllerRecovery(
	context.Context,
	PRDevelopmentControllerRecoveryFinalize,
) (PRDevelopmentController, bool, error) {
	return PRDevelopmentController{}, false, ErrUnsupportedPlatform
}

func (*Store) BindPRDevelopmentControllerLine(
	context.Context,
	PRDevelopmentControllerLineBind,
) (PRDevelopmentController, bool, error) {
	return PRDevelopmentController{}, false, ErrUnsupportedPlatform
}

func (*Store) RecordPRDevelopmentAttemptReviewFence(
	context.Context,
	PRDevelopmentAttemptReviewFenceRecord,
) (PRDevelopmentAttemptReviewFence, bool, error) {
	return PRDevelopmentAttemptReviewFence{}, false, ErrUnsupportedPlatform
}

func (*Store) FinishPRDevelopmentControllerReview(
	context.Context,
	PRDevelopmentControllerReviewTransition,
) (PRDevelopmentController, bool, error) {
	return PRDevelopmentController{}, false, ErrUnsupportedPlatform
}

func (*Store) ReleasePRDevelopmentControllerReview(
	context.Context,
	PRDevelopmentControllerReviewTransition,
) (PRDevelopmentController, error) {
	return PRDevelopmentController{}, ErrUnsupportedPlatform
}

func (*Store) GetPRDevelopmentLedgerForCase(
	context.Context,
	string,
) (PRDevelopmentLedger, error) {
	return PRDevelopmentLedger{}, ErrUnsupportedPlatform
}

func (*Store) GetPRDevelopmentContextSnapshot(
	context.Context,
	string,
) (PRDevelopmentContextSnapshot, error) {
	return PRDevelopmentContextSnapshot{}, ErrUnsupportedPlatform
}

func (*Store) AppendPRDevelopmentLedgerAttempt(
	context.Context,
	PRDevelopmentLedgerAttemptAppend,
) (PRDevelopmentLedgerEntry, bool, error) {
	return PRDevelopmentLedgerEntry{}, false, ErrUnsupportedPlatform
}

func (*Store) AppendPRDevelopmentLedgerReview(
	context.Context,
	PRDevelopmentLedgerReviewAppend,
) (PRDevelopmentLedgerEntry, bool, error) {
	return PRDevelopmentLedgerEntry{}, false, ErrUnsupportedPlatform
}

func (*Store) ClaimPRDevelopmentReview(
	context.Context,
	PRDevelopmentReviewClaimRequest,
) (PRDevelopmentReviewLease, bool, error) {
	return PRDevelopmentReviewLease{}, false, ErrUnsupportedPlatform
}

func (*Store) CompletePRDevelopmentReview(
	context.Context,
	PRDevelopmentLedgerReviewAppend,
) (PRDevelopmentReviewCompletion, bool, error) {
	return PRDevelopmentReviewCompletion{}, false, ErrUnsupportedPlatform
}

func (*Store) AppendPRDevelopmentLedgerCheckpoint(
	context.Context,
	PRDevelopmentLedgerCheckpointAppend,
) (PRDevelopmentLedgerCheckpoint, bool, error) {
	return PRDevelopmentLedgerCheckpoint{}, false, ErrUnsupportedPlatform
}

func (*Store) CaptureReview(
	context.Context,
	ReviewCaptureInput,
) (ReviewCase, bool, error) {
	return ReviewCase{}, false, ErrUnsupportedPlatform
}

func (*Store) GetReviewCase(context.Context, string) (ReviewCaseDetail, error) {
	return ReviewCaseDetail{}, ErrUnsupportedPlatform
}

func (*Store) ListReviewCases(context.Context, ReviewCaseFilter) (ReviewCasePage, error) {
	return ReviewCasePage{}, ErrUnsupportedPlatform
}

func (*Store) UpdateReviewFinding(
	context.Context,
	ReviewFindingUpdate,
) (ReviewCaseDetail, error) {
	return ReviewCaseDetail{}, ErrUnsupportedPlatform
}

func (*Store) DropReviewFinding(
	context.Context,
	ReviewFindingTransition,
) (ReviewCaseDetail, error) {
	return ReviewCaseDetail{}, ErrUnsupportedPlatform
}

func (*Store) RestoreReviewFinding(
	context.Context,
	ReviewFindingTransition,
) (ReviewCaseDetail, error) {
	return ReviewCaseDetail{}, ErrUnsupportedPlatform
}

func (*Store) AppendReviewMessages(
	context.Context,
	ReviewMessageAppend,
) (ReviewCaseDetail, error) {
	return ReviewCaseDetail{}, ErrUnsupportedPlatform
}

func (*Store) CreateReviewSubmission(
	context.Context,
	ReviewSubmissionDraft,
) (ReviewCaseDetail, error) {
	return ReviewCaseDetail{}, ErrUnsupportedPlatform
}

func (*Store) ReconcileReviewSubmission(
	context.Context,
	ReviewSubmissionReconciliation,
) (ReviewCaseDetail, error) {
	return ReviewCaseDetail{}, ErrUnsupportedPlatform
}

func (*Store) GetReviewSubmission(context.Context, string) (ReviewSubmission, error) {
	return ReviewSubmission{}, ErrUnsupportedPlatform
}

func (*Store) ClaimReviewSubmissions(
	context.Context,
	string,
	int,
	time.Duration,
) ([]ReviewSubmission, error) {
	return nil, ErrUnsupportedPlatform
}

func (*Store) RenewReviewSubmissionLease(
	context.Context,
	string,
	string,
	time.Duration,
) error {
	return ErrUnsupportedPlatform
}

func (*Store) FinishReviewSubmission(
	context.Context,
	ReviewSubmissionOutcome,
) (ReviewCaseDetail, error) {
	return ReviewCaseDetail{}, ErrUnsupportedPlatform
}

func (*Store) GetReviewAttentionTrigger(
	context.Context,
	string,
) (ReviewAttentionTrigger, error) {
	return ReviewAttentionTrigger{}, ErrUnsupportedPlatform
}

func (*Store) ClaimReviewAttentionTriggers(
	context.Context,
	string,
	int,
	time.Duration,
) ([]ReviewAttentionTrigger, error) {
	return nil, ErrUnsupportedPlatform
}

func (*Store) RenewReviewAttentionTriggerLease(
	context.Context,
	string,
	string,
	time.Duration,
) error {
	return ErrUnsupportedPlatform
}

func (*Store) PinReviewAttentionTriggerPolicy(
	context.Context,
	ReviewAttentionPolicyPin,
) (ReviewAttentionTrigger, error) {
	return ReviewAttentionTrigger{}, ErrUnsupportedPlatform
}

func (*Store) ReleaseReviewAttentionTrigger(
	context.Context,
	ReviewAttentionTriggerRelease,
) error {
	return ErrUnsupportedPlatform
}

func (*Store) CompleteReviewAttentionTrigger(
	context.Context,
	ReviewAttentionTriggerCompletion,
) error {
	return ErrUnsupportedPlatform
}

func (*Store) GetReviewDecisionRun(
	context.Context,
	ReviewDecisionKey,
) (ReviewDecisionRunLink, error) {
	return ReviewDecisionRunLink{}, ErrUnsupportedPlatform
}

func (*Store) AdmitReviewDecisionRun(
	context.Context,
	ReviewDecisionRunAdmission,
	func(context.Context) error,
) (ReviewDecisionRunLink, bool, error) {
	return ReviewDecisionRunLink{}, false, ErrUnsupportedPlatform
}

func (*Store) GetPRDevelopmentAttentionSnapshot(
	context.Context,
	string,
) (PRDevelopmentAttentionSnapshot, error) {
	return PRDevelopmentAttentionSnapshot{}, ErrUnsupportedPlatform
}

func (*Store) GetPRDevelopmentAttentionDecisionRun(
	context.Context,
	PRDevelopmentAttentionDecisionKey,
) (PRDevelopmentAttentionDecisionRunLink, error) {
	return PRDevelopmentAttentionDecisionRunLink{}, ErrUnsupportedPlatform
}

func (*Store) AdmitPRDevelopmentAttentionDecisionRun(
	context.Context,
	PRDevelopmentAttentionDecisionRunAdmission,
	func(context.Context) error,
) (PRDevelopmentAttentionDecisionRunLink, bool, error) {
	return PRDevelopmentAttentionDecisionRunLink{}, false, ErrUnsupportedPlatform
}
