package prdevelopment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sipeed/picoclaw/pkg/agent"
	"github.com/sipeed/picoclaw/pkg/eventing"
	"github.com/sipeed/picoclaw/pkg/gitworkspace"
)

const (
	defaultRepairLease         = 5 * time.Minute
	defaultRepairFinishTimeout = 10 * time.Second
	maximumRepairInternalError = 16 << 10
)

// RepairCaseVerifier is the read-only provider fence used for every attempt.
type RepairCaseVerifier interface {
	VerifyCase(
		ctx context.Context,
		stored eventing.PRDevelopmentCase,
		expected *eventing.PRDevelopmentThreadIdentity,
	) (VerifiedCase, error)
}

// LocalRepairExecutor is the exact edit-only primitive borrowed by a durable
// controller attempt.
type LocalRepairExecutor interface {
	Run(ctx context.Context, request agent.LocalRepairRequest) (agent.LocalRepairResult, error)
}

// RepairRuntimeFactory resolves one exact agent to one concrete provider/model
// while the gateway holds the matching runtime-generation lease.
type RepairRuntimeFactory func(
	agentID string,
	routingText string,
) (LocalRepairExecutor, error)

// RepairWorkerStore is deliberately narrower than the event inbox. It can
// read one atomic workbench and advance only the leased repair lifecycle.
type RepairWorkerStore interface {
	eventing.PRDevelopmentWorkbenchReader
	eventing.PRDevelopmentRepairQueue
}

// RepairWorker executes at most one durable local-repair attempt per call.
// Preparation is reclaimable; once Begin succeeds, every ambiguous outcome is
// terminal recovery_required and is never automatically invoked again.
type RepairWorker struct {
	Queue         RepairWorkerStore
	Verifier      RepairCaseVerifier
	Runtime       RepairRuntimeFactory
	WorkerLabel   string
	LeaseDuration time.Duration
}

func (worker *RepairWorker) ProcessOne(ctx context.Context) (bool, error) {
	if worker == nil || worker.Queue == nil {
		return false, ErrUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	label := strings.TrimSpace(worker.WorkerLabel)
	if label == "" {
		label = "gateway-pr-development-repair"
	}
	lease := worker.LeaseDuration
	if lease <= 0 {
		lease = defaultRepairLease
	}
	session, claimed, err := worker.Queue.ClaimPRDevelopmentRepair(
		ctx,
		eventing.PRDevelopmentRepairClaimRequest{
			WorkerLabel: label,
			Lease:       lease,
		},
	)
	if err != nil || !claimed {
		return claimed, err
	}
	return true, worker.processClaim(ctx, session, lease)
}

func (worker *RepairWorker) processClaim(
	ctx context.Context,
	session eventing.PRDevelopmentRepairSession,
	lease time.Duration,
) error {
	attempt, err := activePreparingAttempt(session)
	if err != nil {
		return err
	}
	workCtx, cancelWork := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	go worker.renewLease(
		workCtx,
		heartbeatDone,
		heartbeatErr,
		attempt.ID,
		attempt.LeaseToken,
		lease,
		cancelWork,
	)
	stopHeartbeat := func() error {
		cancelWork()
		<-heartbeatDone
		select {
		case renewErr := <-heartbeatErr:
			return renewErr
		default:
			return nil
		}
	}
	heartbeatStopped := false
	defer func() {
		if !heartbeatStopped {
			_ = stopHeartbeat()
		}
	}()

	workbench, err := worker.Queue.GetPRDevelopmentWorkbench(workCtx, session.CaseID)
	if err != nil {
		return err
	}
	if err = validateClaimedWorkbench(workbench, session, attempt); err != nil {
		return worker.finishPreparation(
			ctx,
			attempt,
			stopHeartbeat,
			&heartbeatStopped,
			eventing.PRDevelopmentRepairErrorInternal,
			"Local repair could not start because its durable state is invalid.",
			err,
		)
	}
	conversation := workbench.Conversation
	conversation.Version = attempt.ConversationVersion
	conversation.Messages = append(
		[]eventing.PRDevelopmentMessage(nil),
		conversation.Messages[:attempt.ConversationVersion]...,
	)
	if err = validateConversation(session.CaseID, conversation); err != nil {
		return worker.finishPreparation(
			ctx,
			attempt,
			stopHeartbeat,
			&heartbeatStopped,
			eventing.PRDevelopmentRepairErrorInternal,
			"Local repair could not start because its conversation snapshot is invalid.",
			err,
		)
	}
	if err = ctx.Err(); err != nil {
		// The runner has not started, so shutdown leaves preparation safely
		// reclaimable instead of consuming the user's attempt.
		return err
	}
	if worker.Verifier == nil || worker.Runtime == nil {
		return worker.finishPreparation(
			ctx,
			attempt,
			stopHeartbeat,
			&heartbeatStopped,
			eventing.PRDevelopmentRepairErrorRuntimeUnavailable,
			"The configured local repair runtime is unavailable.",
			errors.New("local repair verifier or runtime is unavailable"),
		)
	}

	expectedThread, err := repairThreadIdentity(workbench.Thread, session.CaseID)
	if err != nil {
		return worker.finishPreparation(
			ctx,
			attempt,
			stopHeartbeat,
			&heartbeatStopped,
			eventing.PRDevelopmentRepairErrorInternal,
			"Local repair could not start because its durable thread identity is invalid.",
			err,
		)
	}
	verified, err := worker.Verifier.VerifyCase(
		workCtx,
		workbench.Case,
		expectedThread,
	)
	if err != nil {
		if parentErr := ctx.Err(); parentErr != nil {
			// Verification is read-only. Parent shutdown must leave this preparing
			// lease for safe reclaim by the next runtime generation.
			return parentErr
		}
		code, summary := classifyRepairPreparationError(err)
		return worker.finishPreparation(
			ctx,
			attempt,
			stopHeartbeat,
			&heartbeatStopped,
			code,
			summary,
			err,
		)
	}
	if verified.CaseID != session.CaseID {
		return worker.finishPreparation(
			ctx,
			attempt,
			stopHeartbeat,
			&heartbeatStopped,
			eventing.PRDevelopmentRepairErrorProviderChanged,
			"The pull request changed before local repair could start.",
			errors.New("verified development case ID does not match the durable session"),
		)
	}

	session, err = worker.Queue.PinPRDevelopmentRepairSession(
		workCtx,
		eventing.PRDevelopmentRepairPin{
			AttemptID:      attempt.ID,
			LeaseToken:     attempt.LeaseToken,
			HeadRepository: verified.HeadRepository,
			HeadRef:        verified.HeadRef,
			HeadSHA:        verified.HeadSHA,
			CloneURL:       verified.HeadCloneURL,
			ReviewDigest:   verified.ReviewDigest,
		},
	)
	if err != nil {
		if errors.Is(err, eventing.ErrPRDevelopmentRepairConflict) {
			return worker.finishPreparation(
				ctx,
				attempt,
				stopHeartbeat,
				&heartbeatStopped,
				eventing.PRDevelopmentRepairErrorProviderChanged,
				"The pull request changed from the local development session's pinned source.",
				err,
			)
		}
		return err
	}
	attempt, err = activePreparingAttempt(session)
	if err != nil {
		return err
	}
	contextText, err := developmentAIContextWithRepair(
		workbench.Case,
		conversation,
		&session,
	)
	if err != nil {
		return worker.finishPreparation(
			ctx,
			attempt,
			stopHeartbeat,
			&heartbeatStopped,
			eventing.PRDevelopmentRepairErrorInternal,
			"Local repair could not prepare its bounded repository context.",
			err,
		)
	}
	runner, err := worker.Runtime(session.AgentID, attempt.Instruction)
	if err != nil || runner == nil {
		if err == nil {
			err = errors.New("local repair runtime factory returned no runner")
		}
		return worker.finishPreparation(
			ctx,
			attempt,
			stopHeartbeat,
			&heartbeatStopped,
			eventing.PRDevelopmentRepairErrorRuntimeUnavailable,
			"The configured local repair runtime is unavailable.",
			err,
		)
	}

	session, err = worker.Queue.BeginPRDevelopmentRepair(
		workCtx,
		eventing.PRDevelopmentRepairBegin{
			AttemptID:  attempt.ID,
			LeaseToken: attempt.LeaseToken,
			Lease:      lease,
		},
	)
	if err != nil {
		return err
	}
	attempt, err = activeRunningAttempt(session)
	if err != nil {
		return err
	}
	result, runErr := runner.Run(workCtx, agent.LocalRepairRequest{
		Pin: gitworkspace.PinnedAcquireRequest{
			Repository:     session.CloneURL,
			SourceRef:      session.HeadRef,
			ExpectedCommit: session.HeadSHA,
			ReservationKey: session.ReservationKey,
			AgentID:        session.AgentID,
		},
		Instruction: attempt.Instruction,
		Context:     contextText,
	})
	heartbeatStopped = true
	renewErr := stopHeartbeat()
	if renewErr != nil {
		if runErr != nil {
			return fmt.Errorf("renew local repair lease: %w (runner result: %v)", renewErr, runErr)
		}
		return fmt.Errorf("renew local repair lease: %w", renewErr)
	}
	if runErr != nil {
		return worker.finish(
			ctx,
			attempt,
			eventing.PRDevelopmentRepairRecoveryRequired,
			"Local repair stopped without a trustworthy completion result. Local edits may exist; automatic retry is disabled.",
			eventing.PRDevelopmentRepairErrorRecoveryRequired,
			runErr,
			result.Iterations,
			result.WorkspaceID,
		)
	}
	if strings.TrimSpace(result.WorkspaceID) == "" {
		return worker.finish(
			ctx,
			attempt,
			eventing.PRDevelopmentRepairRecoveryRequired,
			"Local repair returned without a trustworthy workspace fence. Local edits may exist; automatic retry is disabled.",
			eventing.PRDevelopmentRepairErrorRecoveryRequired,
			errors.New("local repair completed without an opaque workspace ID"),
			result.Iterations,
			"",
		)
	}
	return worker.finish(
		ctx,
		attempt,
		eventing.PRDevelopmentRepairCompleted,
		result.Content,
		"",
		nil,
		result.Iterations,
		result.WorkspaceID,
	)
}

func repairThreadIdentity(
	thread *eventing.PRDevelopmentThreadBinding,
	caseID string,
) (*eventing.PRDevelopmentThreadIdentity, error) {
	if thread == nil || strings.TrimSpace(thread.ID) == "" ||
		thread.CaseCount < 1 || thread.Case.Ordinal < 0 ||
		thread.Case.Ordinal >= thread.CaseCount || thread.Case.CaseID != caseID {
		return nil, errors.New("durable local repair thread is missing or incomplete")
	}
	switch thread.Kind {
	case eventing.PRDevelopmentThreadLegacy:
		if thread.LegacyCaseID != caseID || thread.CaseCount != 1 ||
			thread.Case.Ordinal != 0 {
			return nil, errors.New("durable legacy repair thread is invalid")
		}
		return nil, nil
	case eventing.PRDevelopmentThreadProvider:
		identity := thread.Identity
		if identity.Provider != "github" ||
			strings.TrimSpace(identity.ProviderOrigin) == "" ||
			strings.TrimSpace(identity.PullAuthorID) == "" ||
			strings.TrimSpace(identity.RepositoryID) == "" ||
			strings.TrimSpace(identity.PullRequestID) == "" ||
			identity.PullNumber <= 0 {
			return nil, errors.New("durable provider repair thread is invalid")
		}
		return &identity, nil
	default:
		return nil, errors.New("durable local repair thread kind is invalid")
	}
}

func (worker *RepairWorker) finishPreparation(
	ctx context.Context,
	attempt eventing.PRDevelopmentRepairAttempt,
	stopHeartbeat func() error,
	heartbeatStopped *bool,
	code eventing.PRDevelopmentRepairErrorCode,
	summary string,
	internalErr error,
) error {
	*heartbeatStopped = true
	if err := stopHeartbeat(); err != nil {
		return fmt.Errorf("renew local repair preparation lease: %w", err)
	}
	return worker.finish(
		ctx,
		attempt,
		eventing.PRDevelopmentRepairFailed,
		summary,
		code,
		internalErr,
		0,
		"",
	)
}

func (worker *RepairWorker) finish(
	ctx context.Context,
	attempt eventing.PRDevelopmentRepairAttempt,
	status eventing.PRDevelopmentRepairStatus,
	summary string,
	code eventing.PRDevelopmentRepairErrorCode,
	internalErr error,
	iterations int,
	workspaceID string,
) error {
	finishCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		defaultRepairFinishTimeout,
	)
	defer cancel()
	_, err := worker.Queue.FinishPRDevelopmentRepair(
		finishCtx,
		eventing.PRDevelopmentRepairOutcome{
			AttemptID:     attempt.ID,
			LeaseToken:    attempt.LeaseToken,
			Status:        status,
			Summary:       boundedRepairSummary(summary),
			ErrorCode:     code,
			InternalError: boundedRepairInternalError(internalErr),
			Iterations:    iterations,
			WorkspaceID:   workspaceID,
		},
	)
	return err
}

func (worker *RepairWorker) renewLease(
	ctx context.Context,
	done chan<- struct{},
	errs chan<- error,
	attemptID, leaseToken string,
	lease time.Duration,
	cancel context.CancelFunc,
) {
	defer close(done)
	interval := lease / 3
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := worker.Queue.RenewPRDevelopmentRepairLease(
				ctx,
				attemptID,
				leaseToken,
				lease,
			); err != nil {
				// An intentional stop or parent cancellation can win the select
				// while an in-flight renewal is returning. That cancellation is
				// not evidence that durable ownership was lost.
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

func activePreparingAttempt(
	session eventing.PRDevelopmentRepairSession,
) (eventing.PRDevelopmentRepairAttempt, error) {
	return activeRepairAttempt(session, eventing.PRDevelopmentRepairPreparing)
}

func activeRunningAttempt(
	session eventing.PRDevelopmentRepairSession,
) (eventing.PRDevelopmentRepairAttempt, error) {
	return activeRepairAttempt(session, eventing.PRDevelopmentRepairRunning)
}

func activeRepairAttempt(
	session eventing.PRDevelopmentRepairSession,
	status eventing.PRDevelopmentRepairStatus,
) (eventing.PRDevelopmentRepairAttempt, error) {
	var active *eventing.PRDevelopmentRepairAttempt
	for index := range session.Attempts {
		if session.Attempts[index].Status != status {
			continue
		}
		if active != nil {
			return eventing.PRDevelopmentRepairAttempt{}, errors.New(
				"durable local repair session has more than one active attempt",
			)
		}
		copyAttempt := session.Attempts[index]
		active = &copyAttempt
	}
	if active == nil || active.LeaseToken == "" || active.ID == "" {
		return eventing.PRDevelopmentRepairAttempt{}, errors.New(
			"durable local repair session has no owned active attempt",
		)
	}
	return *active, nil
}

func validateClaimedWorkbench(
	workbench eventing.PRDevelopmentWorkbench,
	claimed eventing.PRDevelopmentRepairSession,
	attempt eventing.PRDevelopmentRepairAttempt,
) error {
	if workbench.Case.ID != claimed.CaseID ||
		workbench.Conversation.CaseID != claimed.CaseID ||
		workbench.RepairSession == nil ||
		workbench.RepairSession.ID != claimed.ID ||
		workbench.RepairSession.Version != claimed.Version ||
		attempt.SessionID != claimed.ID || attempt.Ordinal < 0 ||
		attempt.Ordinal >= len(claimed.Attempts) ||
		attempt.Ordinal >= len(workbench.RepairSession.Attempts) ||
		attempt.ConversationVersion < 0 ||
		attempt.ConversationVersion > workbench.Conversation.Version ||
		workbench.RepairSession.Attempts[attempt.Ordinal].ID != attempt.ID ||
		workbench.RepairSession.Attempts[attempt.Ordinal].LeaseToken != attempt.LeaseToken ||
		workbench.RepairSession.Attempts[attempt.Ordinal].Status !=
			eventing.PRDevelopmentRepairPreparing {
		return errors.New("claimed local repair is not bound to one atomic workbench")
	}
	return validateConversation(claimed.CaseID, workbench.Conversation)
}

func classifyRepairPreparationError(
	err error,
) (eventing.PRDevelopmentRepairErrorCode, string) {
	switch {
	case errors.Is(err, ErrGitHubCaseNotActionable):
		return eventing.PRDevelopmentRepairErrorNotActionable,
			"The pull request or review is no longer actionable. No local repair was started."
	case errors.Is(err, ErrGitHubCaseDrift):
		return eventing.PRDevelopmentRepairErrorProviderChanged,
			"The pull request or review changed before local repair could start."
	default:
		return eventing.PRDevelopmentRepairErrorRuntimeUnavailable,
			"Current pull-request state could not be verified. No local repair was started."
	}
}

func boundedRepairSummary(value string) string {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		value = "Local repair finished without a valid public summary."
	}
	if len(value) > eventing.MaxPRDevelopmentRepairSummaryBytes {
		value = value[:eventing.MaxPRDevelopmentRepairSummaryBytes]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
		value = strings.TrimSpace(value)
	}
	if value == "" {
		return "Local repair finished without a public summary."
	}
	return value
}

func boundedRepairInternalError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(err.Error())
	value = strings.ReplaceAll(value, "\x00", "")
	if !utf8.ValidString(value) {
		return "local repair failed with invalid diagnostic text"
	}
	if len(value) > maximumRepairInternalError {
		value = value[:maximumRepairInternalError]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}
