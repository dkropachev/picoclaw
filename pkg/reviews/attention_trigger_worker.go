package reviews

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/eventing"
)

const (
	defaultAttentionTriggerLease    = 5 * time.Minute
	attentionTriggerRetryBase       = time.Second
	attentionTriggerRetryMaximum    = time.Minute
	attentionTriggerCompletionLimit = 10 * time.Second
)

// AttentionTriggerWorker delivers durably submitted-review occurrences to
// AttentionLauncher. It pins a trusted canonical policy before launch, then
// retries that exact snapshot until either a workflow run is linked or the
// policy resolves to a zero/no-op composition.
type AttentionTriggerWorker struct {
	Queue         eventing.ReviewAttentionTriggerQueue
	Launcher      *AttentionLauncher
	WorkerLabel   string
	LeaseDuration time.Duration
	Now           func() time.Time
}

func (worker *AttentionTriggerWorker) ProcessOne(ctx context.Context) (bool, error) {
	if worker == nil || worker.Queue == nil || isNilWorkingContextValue(worker.Queue) ||
		worker.Launcher == nil || !worker.Launcher.available() {
		return false, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	label := strings.TrimSpace(worker.WorkerLabel)
	if label == "" {
		label = "gateway-review-attention"
	}
	lease := worker.LeaseDuration
	if lease <= 0 {
		lease = defaultAttentionTriggerLease
	}
	if lease < 3*time.Nanosecond {
		return false, ErrInvalidRequest
	}
	claimed, err := worker.Queue.ClaimReviewAttentionTriggers(ctx, label, 1, lease)
	if err != nil {
		return false, err
	}
	if len(claimed) == 0 {
		return false, nil
	}
	if len(claimed) != 1 {
		return true, errors.New("review attention claim exceeded requested limit")
	}
	return true, worker.processClaim(ctx, claimed[0], lease)
}

func (worker *AttentionTriggerWorker) processClaim(
	ctx context.Context,
	trigger eventing.ReviewAttentionTrigger,
	lease time.Duration,
) error {
	request, err := attentionLaunchRequestForTrigger(trigger, worker.now())
	if err != nil {
		return worker.releaseClaim(ctx, trigger, err)
	}

	launchCtx, cancelLaunch := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	go worker.renewLease(
		launchCtx,
		heartbeatDone,
		heartbeatErr,
		trigger,
		lease,
		cancelLaunch,
	)
	stopHeartbeat := func() error {
		cancelLaunch()
		<-heartbeatDone
		select {
		case renewErr := <-heartbeatErr:
			return renewErr
		default:
			return nil
		}
	}

	prepared, prepareErr := worker.pinnedOrPrepare(launchCtx, &trigger, request)
	if prepareErr != nil {
		if renewErr := stopHeartbeat(); renewErr != nil {
			return renewErr
		}
		return worker.releaseClaim(ctx, trigger, prepareErr)
	}
	result, launchErr := worker.Launcher.launchPreparedAttentionPolicy(
		launchCtx,
		request,
		prepared,
		false,
	)
	if renewErr := stopHeartbeat(); renewErr != nil {
		// A run may already have been admitted. Never complete through a stale
		// lease; reclaiming the exact pinned snapshot converges on its link.
		return renewErr
	}

	completion, terminal := attentionTriggerCompletion(trigger, result)
	if terminal {
		if completeErr := worker.completeClaim(ctx, completion); completeErr != nil {
			return completeErr
		}
		// An admitted run is the durable result even when its private execution
		// returned a sanitized failure. The run itself carries that status.
		return nil
	}
	if launchErr == nil {
		launchErr = ErrUnavailable
	}
	return worker.releaseClaim(ctx, trigger, launchErr)
}

func (worker *AttentionTriggerWorker) pinnedOrPrepare(
	ctx context.Context,
	trigger *eventing.ReviewAttentionTrigger,
	request AttentionLaunchRequest,
) (preparedAttentionPolicy, error) {
	if trigger == nil {
		return preparedAttentionPolicy{}, ErrUnavailable
	}
	if trigger.PolicyRevision != "" || len(trigger.PinnedPolicy) != 0 {
		if trigger.PolicyRevision == "" || len(trigger.PinnedPolicy) == 0 {
			return preparedAttentionPolicy{}, ErrUnavailable
		}
		prepared := preparedAttentionPolicy{
			canonical: append([]byte(nil), trigger.PinnedPolicy...),
		}
		policy, err := decodePreparedAttentionPolicy(prepared.canonical)
		if err != nil || policy.decisionRevision != trigger.PolicyRevision {
			return preparedAttentionPolicy{}, ErrUnavailable
		}
		return prepared, nil
	}

	prepared, err := worker.Launcher.prepareAttentionPolicy(ctx, request)
	if err != nil {
		return preparedAttentionPolicy{}, err
	}
	policy, err := decodePreparedAttentionPolicy(prepared.canonical)
	if err != nil {
		return preparedAttentionPolicy{}, ErrUnavailable
	}
	pinned, err := worker.Queue.PinReviewAttentionTriggerPolicy(
		ctx,
		eventing.ReviewAttentionPolicyPin{
			SubmissionID:   trigger.SubmissionID,
			LeaseToken:     trigger.LeaseToken,
			PolicyRevision: policy.decisionRevision,
			PinnedPolicy:   append([]byte(nil), prepared.canonical...),
		},
	)
	if err != nil {
		return preparedAttentionPolicy{}, err
	}
	if !sameAttentionTriggerClaim(*trigger, pinned) ||
		pinned.PolicyRevision != policy.decisionRevision ||
		!bytes.Equal(pinned.PinnedPolicy, prepared.canonical) {
		return preparedAttentionPolicy{}, ErrUnavailable
	}
	*trigger = pinned
	return prepared, nil
}

func attentionLaunchRequestForTrigger(
	trigger eventing.ReviewAttentionTrigger,
	now time.Time,
) (AttentionLaunchRequest, error) {
	request := AttentionLaunchRequest{
		CaseID:              trigger.CaseID,
		ExpectedCaseVersion: trigger.CaseVersion,
		DecisionPoint:       trigger.DecisionPoint,
	}
	if !validWorkingContextPrefixedHexID(trigger.SubmissionID, "prs_") ||
		trigger.Status != eventing.ReviewAttentionClaimed ||
		strings.TrimSpace(trigger.LeaseToken) == "" || trigger.Attempts <= 0 ||
		trigger.LeaseUntil == nil || !trigger.LeaseUntil.After(now) ||
		trigger.DecisionPoint != eventing.ReviewAttentionDecisionSubmitted ||
		trigger.RunID != "" || validateAttentionLaunchRequest(request) != nil ||
		((trigger.PolicyRevision == "") != (len(trigger.PinnedPolicy) == 0)) {
		return AttentionLaunchRequest{}, ErrUnavailable
	}
	return request, nil
}

func sameAttentionTriggerClaim(
	before eventing.ReviewAttentionTrigger,
	after eventing.ReviewAttentionTrigger,
) bool {
	return after.SubmissionID == before.SubmissionID &&
		after.CaseID == before.CaseID &&
		after.CaseVersion == before.CaseVersion &&
		after.DecisionPoint == before.DecisionPoint &&
		after.Status == eventing.ReviewAttentionClaimed &&
		after.LeaseToken == before.LeaseToken && after.Attempts == before.Attempts &&
		sameAttentionLeaseDeadline(after.LeaseUntil, before.LeaseUntil) && after.RunID == ""
}

func sameAttentionLeaseDeadline(left, right *time.Time) bool {
	// The heartbeat may have extended the same owned lease while policy was
	// being captured. A pin may therefore return a later, but never earlier,
	// deadline for the identical token.
	return left != nil && right != nil && !left.Before(*right)
}

func attentionTriggerCompletion(
	trigger eventing.ReviewAttentionTrigger,
	result AttentionLaunchResult,
) (eventing.ReviewAttentionTriggerCompletion, bool) {
	base := eventing.ReviewAttentionTriggerCompletion{
		SubmissionID: trigger.SubmissionID,
		LeaseToken:   trigger.LeaseToken,
	}
	if result.CaseID != trigger.CaseID || result.CaseVersion != trigger.CaseVersion ||
		result.DecisionPoint != trigger.DecisionPoint ||
		result.PolicyRevision != trigger.PolicyRevision {
		return eventing.ReviewAttentionTriggerCompletion{}, false
	}
	if result.Noop && result.RunID == "" && result.Status == "" && !result.Existing {
		base.Status = eventing.ReviewAttentionNoop
		return base, true
	}
	if !result.Noop && result.RunID != "" && result.Status != "" {
		base.Status = eventing.ReviewAttentionDelivered
		base.RunID = result.RunID
		return base, true
	}
	return eventing.ReviewAttentionTriggerCompletion{}, false
}

func (worker *AttentionTriggerWorker) renewLease(
	ctx context.Context,
	done chan<- struct{},
	errs chan<- error,
	trigger eventing.ReviewAttentionTrigger,
	lease time.Duration,
	cancel context.CancelFunc,
) {
	defer close(done)
	interval := lease / 3
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := worker.Queue.RenewReviewAttentionTriggerLease(
				ctx,
				trigger.SubmissionID,
				trigger.LeaseToken,
				lease,
			); err != nil {
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

func (worker *AttentionTriggerWorker) releaseClaim(
	ctx context.Context,
	trigger eventing.ReviewAttentionTrigger,
	workErr error,
) error {
	finishCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		attentionTriggerCompletionLimit,
	)
	defer cancel()
	delay := attentionTriggerRetryDelay(trigger.Attempts)
	releaseErr := worker.Queue.ReleaseReviewAttentionTrigger(
		finishCtx,
		eventing.ReviewAttentionTriggerRelease{
			SubmissionID: trigger.SubmissionID,
			LeaseToken:   trigger.LeaseToken,
			AvailableAt:  worker.now().Add(delay),
			Error:        safeAttentionTriggerError(workErr),
		},
	)
	if releaseErr != nil {
		return releaseErr
	}
	return workErr
}

func (worker *AttentionTriggerWorker) completeClaim(
	ctx context.Context,
	completion eventing.ReviewAttentionTriggerCompletion,
) error {
	finishCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		attentionTriggerCompletionLimit,
	)
	defer cancel()
	return worker.Queue.CompleteReviewAttentionTrigger(finishCtx, completion)
}

func attentionTriggerRetryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	delay := attentionTriggerRetryBase
	for attempt := 1; attempt < attempts && delay < attentionTriggerRetryMaximum; attempt++ {
		delay *= 2
		if delay > attentionTriggerRetryMaximum {
			delay = attentionTriggerRetryMaximum
		}
	}
	return delay
}

func (worker *AttentionTriggerWorker) now() time.Time {
	if worker != nil && worker.Now != nil {
		return worker.Now().UTC()
	}
	return time.Now().UTC()
}

func safeAttentionTriggerError(err error) string {
	if err == nil {
		return ErrUnavailable.Error()
	}
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded.Error()
	case errors.Is(err, ErrInvalidRequest):
		return ErrInvalidRequest.Error()
	case errors.Is(err, ErrUnavailable):
		return ErrUnavailable.Error()
	default:
		// Launch and preparation errors are sanitized before reaching the
		// worker. Collapse every unknown value defensively so policy/provider
		// diagnostics cannot enter the durable queue.
		return ErrUnavailable.Error()
	}
}
