package prdevelopment

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	sharedattention "github.com/sipeed/picoclaw/pkg/attention"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const (
	defaultPRDevelopmentAttentionTriggerLease = 5 * time.Minute
	prDevelopmentAttentionCompletionLimit     = 10 * time.Second
	minimumPRDevelopmentAttentionTriggerLease = 3 * prDevelopmentAttentionCompletionLimit
)

// AttentionTriggerWorker delivers durable, reservation-free local-review
// attention occurrences. Policy and subject are pinned in separate immutable
// stages; every fully pinned retry checks the deterministic run before reading
// mutable development state.
type AttentionTriggerWorker struct {
	Queue         eventing.PRDevelopmentAttentionTriggerQueue
	Launcher      *AttentionLauncher
	WorkerLabel   string
	LeaseDuration time.Duration
	Now           func() time.Time
}

func (worker *AttentionTriggerWorker) ProcessOne(ctx context.Context) (bool, error) {
	if worker == nil || worker.Queue == nil || isNilServiceValue(worker.Queue) ||
		worker.Launcher == nil || !worker.Launcher.available() {
		return false, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	label := strings.TrimSpace(worker.WorkerLabel)
	if label == "" {
		label = "gateway-pr-development-attention"
	}
	lease := worker.LeaseDuration
	if lease <= 0 {
		lease = defaultPRDevelopmentAttentionTriggerLease
	}
	// The heartbeat stops before detached finalization. With renewal every
	// lease/3, a valid lease always retains at least twice the completion
	// timeout after this lower bound.
	if lease < minimumPRDevelopmentAttentionTriggerLease {
		return false, ErrInvalidRequest
	}
	claimed, err := worker.Queue.ClaimPRDevelopmentAttentionTriggers(
		ctx,
		label,
		1,
		lease,
	)
	if err != nil {
		return false, err
	}
	if len(claimed) == 0 {
		return false, nil
	}
	if len(claimed) != 1 {
		return true, errors.New("pull request development attention claim exceeded requested limit")
	}
	return true, worker.processClaim(ctx, claimed[0], lease)
}

func (worker *AttentionTriggerWorker) processClaim(
	ctx context.Context,
	trigger eventing.PRDevelopmentAttentionTrigger,
	lease time.Duration,
) error {
	if err := validateAttentionTriggerClaim(trigger, worker.now()); err != nil {
		if validAttentionTriggerLeaseIdentity(trigger, worker.now()) {
			return worker.completeAttentionTrigger(
				ctx,
				trigger,
				eventing.PRDevelopmentAttentionTriggerFailed,
				"",
				err,
			)
		}
		return err
	}

	workCtx, cancelWork := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	go worker.renewAttentionTriggerLease(
		workCtx,
		heartbeatDone,
		heartbeatErr,
		trigger,
		lease,
		cancelWork,
	)
	heartbeatStopped := false
	stopHeartbeat := func() error {
		if heartbeatStopped {
			return nil
		}
		heartbeatStopped = true
		cancelWork()
		<-heartbeatDone
		select {
		case renewErr := <-heartbeatErr:
			return renewErr
		default:
			return nil
		}
	}
	defer func() { _ = stopHeartbeat() }()

	prepared, policy, hasPolicy, prepareErr := pinnedAttentionTriggerPolicy(trigger)
	if prepareErr != nil {
		if renewErr := stopHeartbeat(); renewErr != nil {
			return renewErr
		}
		return worker.completeAttentionTrigger(
			ctx,
			trigger,
			eventing.PRDevelopmentAttentionTriggerFailed,
			"",
			prepareErr,
		)
	}
	if hasPolicy && policy.IsNoop() {
		if trigger.SubjectRevision != "" {
			if renewErr := stopHeartbeat(); renewErr != nil {
				return renewErr
			}
			return worker.completeAttentionTrigger(
				ctx,
				trigger,
				eventing.PRDevelopmentAttentionTriggerFailed,
				"",
				ErrUnavailable,
			)
		}
		result, found, noopErr := worker.Launcher.findPinnedAttentionTrigger(
			workCtx,
			trigger,
			prepared,
		)
		if renewErr := stopHeartbeat(); renewErr != nil {
			return renewErr
		}
		completion, ok := attentionTriggerResultCompletion(trigger, result)
		if noopErr != nil || !found || !ok ||
			completion.Status != eventing.PRDevelopmentAttentionTriggerNoop {
			return worker.completeAttentionTrigger(
				ctx,
				trigger,
				eventing.PRDevelopmentAttentionTriggerFailed,
				"",
				ErrUnavailable,
			)
		}
		return worker.completeAttentionTriggerInput(ctx, completion)
	}

	// A complete active pin has enough immutable identity to recover a prior
	// launch without consulting the current controller, conversation, Git, or
	// CI state.
	if hasPolicy && trigger.SubjectRevision != "" {
		result, found, findErr := worker.Launcher.findPinnedAttentionTrigger(
			workCtx,
			trigger,
			prepared,
		)
		if findErr != nil {
			if renewErr := stopHeartbeat(); renewErr != nil {
				return renewErr
			}
			if errors.Is(findErr, sharedattention.ErrPrivateRunAdmissionUncertain) {
				return worker.completeAttentionTrigger(
					ctx,
					trigger,
					eventing.PRDevelopmentAttentionTriggerRecoveryRequired,
					"",
					findErr,
				)
			}
			return worker.releaseAttentionTrigger(ctx, trigger, findErr)
		}
		if found {
			if renewErr := stopHeartbeat(); renewErr != nil {
				return renewErr
			}
			completion, ok := attentionTriggerResultCompletion(trigger, result)
			if !ok {
				return worker.completeAttentionTrigger(
					ctx,
					trigger,
					eventing.PRDevelopmentAttentionTriggerFailed,
					"",
					ErrUnavailable,
				)
			}
			return worker.completeAttentionTriggerInput(ctx, completion)
		}
	}

	claimed, snapshot, snapshotErr := worker.Queue.GetClaimedPRDevelopmentAttentionSnapshot(
		workCtx,
		trigger.ReviewEntryID,
		trigger.LeaseToken,
	)
	if snapshotErr != nil {
		if renewErr := stopHeartbeat(); renewErr != nil {
			return renewErr
		}
		if errors.Is(snapshotErr, eventing.ErrPRDevelopmentAttentionSuperseded) {
			return worker.completeAttentionTrigger(
				ctx,
				trigger,
				eventing.PRDevelopmentAttentionTriggerSuperseded,
				"",
				snapshotErr,
			)
		}
		return worker.releaseAttentionTrigger(ctx, trigger, snapshotErr)
	}
	if !sameAttentionTriggerClaim(trigger, claimed) ||
		validateAttentionTriggerSnapshotAnchor(claimed, snapshot) != nil {
		if renewErr := stopHeartbeat(); renewErr != nil {
			return renewErr
		}
		return worker.completeAttentionTrigger(
			ctx,
			trigger,
			eventing.PRDevelopmentAttentionTriggerFailed,
			"",
			ErrUnavailable,
		)
	}
	trigger = claimed

	if !hasPolicy {
		prepared, prepareErr = worker.Launcher.prepareAttentionTriggerPolicy(
			workCtx,
			trigger,
			snapshot,
		)
		if prepareErr != nil {
			if renewErr := stopHeartbeat(); renewErr != nil {
				return renewErr
			}
			return worker.releaseAttentionTrigger(ctx, trigger, prepareErr)
		}
		policy, prepareErr = decodePreparedAttentionPolicy(prepared)
		if prepareErr != nil {
			if renewErr := stopHeartbeat(); renewErr != nil {
				return renewErr
			}
			return worker.completeAttentionTrigger(
				ctx,
				trigger,
				eventing.PRDevelopmentAttentionTriggerFailed,
				"",
				prepareErr,
			)
		}
		pinned, pinErr := worker.Queue.PinPRDevelopmentAttentionTriggerPolicy(
			workCtx,
			eventing.PRDevelopmentAttentionPolicyPin{
				ReviewEntryID:  trigger.ReviewEntryID,
				LeaseToken:     trigger.LeaseToken,
				PolicyRevision: policy.DecisionRevision(),
				PinnedPolicy:   append([]byte(nil), prepared.canonical...),
				Snapshot:       snapshot.HighWater,
			},
		)
		if pinErr != nil {
			if renewErr := stopHeartbeat(); renewErr != nil {
				return renewErr
			}
			if errors.Is(pinErr, eventing.ErrPRDevelopmentAttentionSuperseded) {
				return worker.completeAttentionTrigger(
					ctx,
					trigger,
					eventing.PRDevelopmentAttentionTriggerSuperseded,
					"",
					pinErr,
				)
			}
			return worker.releaseAttentionTrigger(ctx, trigger, pinErr)
		}
		if !sameAttentionTriggerClaim(trigger, pinned) ||
			pinned.PolicyRevision != policy.DecisionRevision() ||
			!bytes.Equal(pinned.PinnedPolicy, prepared.canonical) ||
			pinned.SubjectRevision != "" {
			if renewErr := stopHeartbeat(); renewErr != nil {
				return renewErr
			}
			return worker.completeAttentionTrigger(
				ctx,
				trigger,
				eventing.PRDevelopmentAttentionTriggerFailed,
				"",
				ErrUnavailable,
			)
		}
		trigger = pinned
		hasPolicy = true
	}

	if policy.IsNoop() {
		result, found, noopErr := worker.Launcher.findPinnedAttentionTrigger(
			workCtx,
			trigger,
			prepared,
		)
		if renewErr := stopHeartbeat(); renewErr != nil {
			return renewErr
		}
		completion, ok := attentionTriggerResultCompletion(trigger, result)
		if noopErr != nil || !found || !ok ||
			completion.Status != eventing.PRDevelopmentAttentionTriggerNoop {
			return worker.completeAttentionTrigger(
				ctx,
				trigger,
				eventing.PRDevelopmentAttentionTriggerFailed,
				"",
				ErrUnavailable,
			)
		}
		return worker.completeAttentionTriggerInput(ctx, completion)
	}
	if !hasPolicy {
		return ErrUnavailable
	}

	if trigger.SubjectRevision == "" {
		subjectRevision, subjectErr := worker.Launcher.prepareAttentionTriggerSubject(
			workCtx,
			trigger,
			snapshot,
			prepared,
			worker.attentionTriggerSnapshotRefresh(trigger, snapshot),
		)
		if subjectErr != nil {
			if renewErr := stopHeartbeat(); renewErr != nil {
				return renewErr
			}
			if errors.Is(subjectErr, eventing.ErrPRDevelopmentAttentionSuperseded) {
				return worker.completeAttentionTrigger(
					ctx,
					trigger,
					eventing.PRDevelopmentAttentionTriggerSuperseded,
					"",
					subjectErr,
				)
			}
			if errors.Is(subjectErr, ErrAttentionSubjectTooLarge) {
				return worker.completeAttentionTrigger(
					ctx,
					trigger,
					eventing.PRDevelopmentAttentionTriggerFailed,
					"",
					subjectErr,
				)
			}
			if errors.Is(subjectErr, eventing.ErrInvalidPRDevelopmentAttentionTrigger) {
				return worker.completeAttentionTrigger(
					ctx,
					trigger,
					eventing.PRDevelopmentAttentionTriggerFailed,
					"",
					subjectErr,
				)
			}
			return worker.releaseAttentionTrigger(ctx, trigger, subjectErr)
		}
		pinned, pinErr := worker.Queue.PinPRDevelopmentAttentionTriggerSubject(
			workCtx,
			eventing.PRDevelopmentAttentionSubjectPin{
				ReviewEntryID:   trigger.ReviewEntryID,
				LeaseToken:      trigger.LeaseToken,
				PolicyRevision:  trigger.PolicyRevision,
				SubjectRevision: subjectRevision,
				Snapshot:        snapshot.HighWater,
			},
		)
		if pinErr != nil {
			if renewErr := stopHeartbeat(); renewErr != nil {
				return renewErr
			}
			if errors.Is(pinErr, eventing.ErrPRDevelopmentAttentionSuperseded) {
				return worker.completeAttentionTrigger(
					ctx,
					trigger,
					eventing.PRDevelopmentAttentionTriggerSuperseded,
					"",
					pinErr,
				)
			}
			return worker.releaseAttentionTrigger(ctx, trigger, pinErr)
		}
		if !sameAttentionTriggerClaim(trigger, pinned) ||
			pinned.PolicyRevision != trigger.PolicyRevision ||
			!bytes.Equal(pinned.PinnedPolicy, trigger.PinnedPolicy) ||
			pinned.SubjectRevision != subjectRevision {
			if renewErr := stopHeartbeat(); renewErr != nil {
				return renewErr
			}
			return worker.completeAttentionTrigger(
				ctx,
				trigger,
				eventing.PRDevelopmentAttentionTriggerFailed,
				"",
				ErrUnavailable,
			)
		}
		trigger = pinned
	}

	// The subject pin now makes exact replay independent of the snapshot read
	// above. Check once more before entering any runtime/context work.
	if result, found, findErr := worker.Launcher.findPinnedAttentionTrigger(
		workCtx,
		trigger,
		prepared,
	); findErr != nil {
		if renewErr := stopHeartbeat(); renewErr != nil {
			return renewErr
		}
		if errors.Is(findErr, sharedattention.ErrPrivateRunAdmissionUncertain) {
			return worker.completeAttentionTrigger(
				ctx,
				trigger,
				eventing.PRDevelopmentAttentionTriggerRecoveryRequired,
				"",
				findErr,
			)
		}
		return worker.releaseAttentionTrigger(ctx, trigger, findErr)
	} else if found {
		if renewErr := stopHeartbeat(); renewErr != nil {
			return renewErr
		}
		completion, ok := attentionTriggerResultCompletion(trigger, result)
		if !ok {
			return worker.completeAttentionTrigger(
				ctx,
				trigger,
				eventing.PRDevelopmentAttentionTriggerFailed,
				"",
				ErrUnavailable,
			)
		}
		return worker.completeAttentionTriggerInput(ctx, completion)
	}

	result, launchErr := worker.Launcher.launchPinnedAttentionTrigger(
		workCtx,
		trigger,
		snapshot,
		prepared,
		worker.attentionTriggerSnapshotRefresh(trigger, snapshot),
	)
	if renewErr := stopHeartbeat(); renewErr != nil {
		// The exact immutable pins remain; a later owner always performs replay
		// lookup before attempting another launch.
		return renewErr
	}
	if completion, ok := attentionTriggerResultCompletion(trigger, result); ok {
		return worker.completeAttentionTriggerInput(ctx, completion)
	}
	if errors.Is(launchErr, sharedattention.ErrPrivateRunAdmissionUncertain) {
		return worker.completeAttentionTrigger(
			ctx,
			trigger,
			eventing.PRDevelopmentAttentionTriggerRecoveryRequired,
			"",
			launchErr,
		)
	}
	if errors.Is(launchErr, errPinnedAttentionSubjectDrift) {
		return worker.completeAttentionTrigger(
			ctx,
			trigger,
			eventing.PRDevelopmentAttentionTriggerRecoveryRequired,
			"",
			launchErr,
		)
	}
	if errors.Is(launchErr, workflows.ErrRunAdmissionConflict) {
		// Admission conflicts are a proven pre-create fence failure. Release
		// so the next claimed snapshot can distinguish harmless mutable
		// lifecycle advance from a truly superseded review tail.
		return worker.releaseAttentionTrigger(ctx, trigger, launchErr)
	}
	if errors.Is(launchErr, eventing.ErrPRDevelopmentAttentionSuperseded) {
		return worker.completeAttentionTrigger(
			ctx,
			trigger,
			eventing.PRDevelopmentAttentionTriggerSuperseded,
			"",
			launchErr,
		)
	}
	if errors.Is(launchErr, eventing.ErrInvalidPRDevelopmentAttentionTrigger) {
		return worker.completeAttentionTrigger(
			ctx,
			trigger,
			eventing.PRDevelopmentAttentionTriggerFailed,
			"",
			launchErr,
		)
	}
	if launchErr == nil {
		launchErr = ErrUnavailable
	}
	return worker.releaseAttentionTrigger(ctx, trigger, launchErr)
}

func (worker *AttentionTriggerWorker) attentionTriggerSnapshotRefresh(
	trigger eventing.PRDevelopmentAttentionTrigger,
	expected eventing.PRDevelopmentAttentionSnapshot,
) attentionRuntimeSnapshotRefresh {
	return func(
		ctx context.Context,
	) (eventing.PRDevelopmentAttentionSnapshot, error) {
		claimed, snapshot, err := worker.Queue.GetClaimedPRDevelopmentAttentionSnapshot(
			ctx,
			trigger.ReviewEntryID,
			trigger.LeaseToken,
		)
		if err != nil {
			return eventing.PRDevelopmentAttentionSnapshot{}, err
		}
		if !sameAttentionTriggerClaim(trigger, claimed) ||
			claimed.PolicyRevision != trigger.PolicyRevision ||
			!bytes.Equal(claimed.PinnedPolicy, trigger.PinnedPolicy) ||
			claimed.SubjectRevision != trigger.SubjectRevision ||
			validateAttentionTriggerSnapshotAnchor(claimed, snapshot) != nil {
			return eventing.PRDevelopmentAttentionSnapshot{},
				eventing.ErrInvalidPRDevelopmentAttentionTrigger
		}
		if !reflect.DeepEqual(snapshot, expected) {
			if trigger.SubjectRevision == "" {
				return eventing.PRDevelopmentAttentionSnapshot{},
					workflows.ErrRunAdmissionConflict
			}
			return eventing.PRDevelopmentAttentionSnapshot{},
				errPinnedAttentionSubjectDrift
		}
		return snapshot, nil
	}
}

func pinnedAttentionTriggerPolicy(
	trigger eventing.PRDevelopmentAttentionTrigger,
) (preparedAttentionPolicy, sharedattention.PreparedPolicy, bool, error) {
	hasRevision := trigger.PolicyRevision != ""
	hasPolicy := len(trigger.PinnedPolicy) != 0
	if hasRevision != hasPolicy || trigger.SubjectRevision != "" && !hasPolicy {
		return preparedAttentionPolicy{}, sharedattention.PreparedPolicy{}, false, ErrUnavailable
	}
	if !hasPolicy {
		return preparedAttentionPolicy{}, sharedattention.PreparedPolicy{}, false, nil
	}
	if trigger.SubjectRevision != "" && !validAttentionRevision(trigger.SubjectRevision) {
		return preparedAttentionPolicy{}, sharedattention.PreparedPolicy{}, false, ErrUnavailable
	}
	prepared := preparedAttentionPolicy{
		canonical: append([]byte(nil), trigger.PinnedPolicy...),
	}
	policy, err := decodePreparedAttentionPolicy(prepared)
	if err != nil || policy.DecisionRevision() != trigger.PolicyRevision {
		return preparedAttentionPolicy{}, sharedattention.PreparedPolicy{}, false, ErrUnavailable
	}
	return prepared, policy, true, nil
}

func validateAttentionTriggerClaim(
	trigger eventing.PRDevelopmentAttentionTrigger,
	now time.Time,
) error {
	if validateAttentionTriggerIdentity(trigger) != nil ||
		trigger.DecisionPoint != eventing.PRDevelopmentAttentionDecisionReviewRequired ||
		trigger.Status != eventing.PRDevelopmentAttentionTriggerClaimed ||
		strings.TrimSpace(trigger.LeaseToken) == "" || trigger.Attempts <= 0 ||
		trigger.LeaseUntil == nil || !trigger.LeaseUntil.After(now) ||
		trigger.RunID != "" || trigger.CompletedAt != nil ||
		((trigger.PolicyRevision == "") != (len(trigger.PinnedPolicy) == 0)) ||
		(trigger.SubjectRevision != "" && len(trigger.PinnedPolicy) == 0) {
		return ErrUnavailable
	}
	return nil
}

func validAttentionTriggerLeaseIdentity(
	trigger eventing.PRDevelopmentAttentionTrigger,
	now time.Time,
) bool {
	return validDevelopmentID(trigger.ReviewEntryID, "pdle_") &&
		trigger.Status == eventing.PRDevelopmentAttentionTriggerClaimed &&
		strings.TrimSpace(trigger.LeaseToken) != "" && trigger.LeaseUntil != nil &&
		trigger.LeaseUntil.After(now)
}

func sameAttentionTriggerClaim(
	before eventing.PRDevelopmentAttentionTrigger,
	after eventing.PRDevelopmentAttentionTrigger,
) bool {
	return after.ReviewEntryID == before.ReviewEntryID &&
		after.ReviewEntryHash == before.ReviewEntryHash &&
		after.CaseID == before.CaseID &&
		after.ConversationVersion == before.ConversationVersion &&
		after.TranscriptDigest == before.TranscriptDigest &&
		after.DecisionPoint == before.DecisionPoint &&
		after.Status == eventing.PRDevelopmentAttentionTriggerClaimed &&
		after.LeaseToken == before.LeaseToken &&
		after.Attempts == before.Attempts &&
		sameAttentionTriggerLeaseDeadline(after.LeaseUntil, before.LeaseUntil) &&
		after.RunID == "" && after.CompletedAt == nil
}

func sameAttentionTriggerLeaseDeadline(left, right *time.Time) bool {
	return left != nil && right != nil && !left.Before(*right)
}

func attentionTriggerResultCompletion(
	trigger eventing.PRDevelopmentAttentionTrigger,
	result AttentionLaunchResult,
) (eventing.PRDevelopmentAttentionTriggerCompletion, bool) {
	base := eventing.PRDevelopmentAttentionTriggerCompletion{
		ReviewEntryID: trigger.ReviewEntryID,
		LeaseToken:    trigger.LeaseToken,
	}
	if result.CaseID != trigger.CaseID ||
		result.ReviewEntryID != trigger.ReviewEntryID ||
		result.ConversationVersion != trigger.ConversationVersion ||
		result.DecisionPoint != trigger.DecisionPoint ||
		result.PolicyRevision != trigger.PolicyRevision ||
		result.SubjectRevision != trigger.SubjectRevision {
		return eventing.PRDevelopmentAttentionTriggerCompletion{}, false
	}
	if result.Noop && result.RunID == "" && result.Status == "" &&
		!result.Existing && trigger.SubjectRevision == "" {
		base.Status = eventing.PRDevelopmentAttentionTriggerNoop
		return base, true
	}
	if !result.Noop && result.RunID != "" && result.Status != "" &&
		trigger.SubjectRevision != "" {
		base.Status = eventing.PRDevelopmentAttentionTriggerDelivered
		base.RunID = result.RunID
		return base, true
	}
	return eventing.PRDevelopmentAttentionTriggerCompletion{}, false
}

func (worker *AttentionTriggerWorker) renewAttentionTriggerLease(
	ctx context.Context,
	done chan<- struct{},
	errs chan<- error,
	trigger eventing.PRDevelopmentAttentionTrigger,
	lease time.Duration,
	cancel context.CancelFunc,
) {
	defer close(done)
	ticker := time.NewTicker(lease / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := worker.Queue.RenewPRDevelopmentAttentionTriggerLease(
				ctx,
				trigger.ReviewEntryID,
				trigger.LeaseToken,
				lease,
			); err != nil {
				// A normal stop can cancel an in-flight store call. Do not
				// promote that shutdown race into a lease failure that prevents
				// the caller from releasing or completing the still-owned claim.
				if ctx.Err() != nil {
					return
				}
				select {
				case errs <- err:
				default:
				}
				cancel()
				return
			}
		}
	}
}

func (worker *AttentionTriggerWorker) releaseAttentionTrigger(
	ctx context.Context,
	trigger eventing.PRDevelopmentAttentionTrigger,
	workErr error,
) error {
	finishCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		prDevelopmentAttentionCompletionLimit,
	)
	defer cancel()
	releaseErr := worker.Queue.ReleasePRDevelopmentAttentionTrigger(
		finishCtx,
		eventing.PRDevelopmentAttentionTriggerRelease{
			ReviewEntryID: trigger.ReviewEntryID,
			LeaseToken:    trigger.LeaseToken,
			AvailableAt: worker.now().Add(
				prDevelopmentAttentionRetryDelay(trigger.Attempts),
			),
			Error: safePRDevelopmentAttentionTriggerError(workErr),
		},
	)
	if releaseErr != nil {
		return releaseErr
	}
	return workErr
}

func (worker *AttentionTriggerWorker) completeAttentionTrigger(
	ctx context.Context,
	trigger eventing.PRDevelopmentAttentionTrigger,
	status eventing.PRDevelopmentAttentionTriggerStatus,
	runID string,
	workErr error,
) error {
	return worker.completeAttentionTriggerInput(
		ctx,
		eventing.PRDevelopmentAttentionTriggerCompletion{
			ReviewEntryID: trigger.ReviewEntryID,
			LeaseToken:    trigger.LeaseToken,
			Status:        status,
			RunID:         runID,
			Error:         safePRDevelopmentAttentionTriggerError(workErr),
		},
	)
}

func (worker *AttentionTriggerWorker) completeAttentionTriggerInput(
	ctx context.Context,
	completion eventing.PRDevelopmentAttentionTriggerCompletion,
) error {
	finishCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		prDevelopmentAttentionCompletionLimit,
	)
	defer cancel()
	return worker.Queue.CompletePRDevelopmentAttentionTrigger(finishCtx, completion)
}

func prDevelopmentAttentionRetryDelay(attempts int) time.Duration {
	return PublicationRetryDelay(attempts)
}

func (worker *AttentionTriggerWorker) now() time.Time {
	if worker != nil && worker.Now != nil {
		return worker.Now().UTC()
	}
	return time.Now().UTC()
}

func safePRDevelopmentAttentionTriggerError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, sharedattention.ErrPrivateRunAdmissionUncertain):
		return sharedattention.ErrPrivateRunAdmissionUncertain.Error()
	case errors.Is(err, eventing.ErrPRDevelopmentAttentionSuperseded):
		return eventing.ErrPRDevelopmentAttentionSuperseded.Error()
	case errors.Is(err, ErrAttentionSubjectTooLarge):
		return ErrAttentionSubjectTooLarge.Error()
	case errors.Is(err, ErrAIContextCompactionRequired):
		return ErrAIContextCompactionRequired.Error()
	case errors.Is(err, errPinnedAttentionSubjectDrift):
		return errPinnedAttentionSubjectDrift.Error()
	case errors.Is(err, context.Canceled):
		return context.Canceled.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded.Error()
	default:
		return ErrUnavailable.Error()
	}
}
