package eventing

import (
	"context"
	"time"
)

// PRDevelopmentControllerSuspendedResumeRecoveryCandidate is a
// non-authorizing pointer to one expired, crash-ambiguous suspended resume.
// It intentionally carries neither the staged reservation nor a claim token.
type PRDevelopmentControllerSuspendedResumeRecoveryCandidate struct {
	CaseID           string    `json:"-"`
	SuspensionID     string    `json:"-"`
	ControllerID     string    `json:"-"`
	AttemptID        string    `json:"-"`
	ExpectedRevision int64     `json:"-"`
	AvailableAt      time.Time `json:"-"`
}

// PRDevelopmentControllerSuspendedResumeRecoveryClaim replaces one expired
// child resume claim with a recovery-worker-only lease. It never grants an
// orchestration claim, controller mutation lease, or model authority.
type PRDevelopmentControllerSuspendedResumeRecoveryClaim struct {
	CaseID           string        `json:"-"`
	SuspensionID     string        `json:"-"`
	ControllerID     string        `json:"-"`
	AttemptID        string        `json:"-"`
	ExpectedRevision int64         `json:"-"`
	ClaimID          string        `json:"-"`
	WorkerLabel      string        `json:"-"`
	Lease            time.Duration `json:"-"`
}

// PRDevelopmentControllerSuspendedResumeRecoveryLease carries only the exact
// already-persisted resume request needed for deterministic Git replay.
type PRDevelopmentControllerSuspendedResumeRecoveryLease struct {
	Controller PRDevelopmentController           `json:"-"`
	Suspension PRDevelopmentControllerSuspension `json:"-"`
	Reclaimed  bool                              `json:"-"`
}

// PRDevelopmentControllerSuspendedResumeRecoveryRenew extends only the exact
// recovery claim. It cannot change the staged request or gain other authority.
type PRDevelopmentControllerSuspendedResumeRecoveryRenew struct {
	SuspensionID string        `json:"-"`
	ControllerID string        `json:"-"`
	AttemptID    string        `json:"-"`
	ClaimID      string        `json:"-"`
	ClaimToken   string        `json:"-"`
	ClaimEpoch   int64         `json:"-"`
	Lease        time.Duration `json:"-"`
}

// PRDevelopmentControllerSuspendedResumeRecoveryFinalize accepts only exact
// replay evidence for the immutable resume request. Finalization transfers
// the same staged reservation directly into a new suspension checkpoint.
type PRDevelopmentControllerSuspendedResumeRecoveryFinalize struct {
	SuspensionID     string                                       `json:"-"`
	ControllerID     string                                       `json:"-"`
	AttemptID        string                                       `json:"-"`
	ExpectedRevision int64                                        `json:"-"`
	ClaimID          string                                       `json:"-"`
	ClaimToken       string                                       `json:"-"`
	ClaimEpoch       int64                                        `json:"-"`
	Result           PRDevelopmentControllerSuspendedResumeResult `json:"-"`
}

// PRDevelopmentControllerSuspendedResumeRecoveryTransition is the atomic
// recovery result: the old resume is final evidence and the child suspension
// is the sole temporary owner of the same reservation bearer.
type PRDevelopmentControllerSuspendedResumeRecoveryTransition struct {
	Controller     PRDevelopmentController           `json:"-"`
	Resumed        PRDevelopmentControllerSuspension `json:"-"`
	NextSuspension PRDevelopmentControllerSuspension `json:"-"`
}

// PRDevelopmentControllerSuspendedResumeRecoveryStore is intentionally
// independent of repair orchestration and provider/model capabilities.
type PRDevelopmentControllerSuspendedResumeRecoveryStore interface {
	NextPRDevelopmentControllerSuspendedResumeRecovery(
		ctx context.Context,
	) (PRDevelopmentControllerSuspendedResumeRecoveryCandidate, bool, error)
	ClaimPRDevelopmentControllerSuspendedResumeRecovery(
		ctx context.Context,
		input PRDevelopmentControllerSuspendedResumeRecoveryClaim,
	) (PRDevelopmentControllerSuspendedResumeRecoveryLease, bool, error)
	RenewPRDevelopmentControllerSuspendedResumeRecovery(
		ctx context.Context,
		input PRDevelopmentControllerSuspendedResumeRecoveryRenew,
	) error
	FinalizePRDevelopmentControllerSuspendedResumeRecovery(
		ctx context.Context,
		input PRDevelopmentControllerSuspendedResumeRecoveryFinalize,
	) (PRDevelopmentControllerSuspendedResumeRecoveryTransition, bool, error)
}
