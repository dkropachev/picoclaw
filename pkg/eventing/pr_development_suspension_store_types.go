package eventing

import (
	"context"
	"time"
)

// PRDevelopmentControllerSuspensionWorkCandidate is a non-authorizing pointer to
// one exact, durable Git suspension. A worker must claim it before using the
// raw reservation bearer carried by the suspension row.
type PRDevelopmentControllerSuspensionWorkCandidate struct {
	CaseID           string                                      `json:"-"`
	SuspensionID     string                                      `json:"-"`
	ControllerID     string                                      `json:"-"`
	AttemptID        string                                      `json:"-"`
	SourceKind       PRDevelopmentControllerSuspensionSourceKind `json:"-"`
	Mode             PRDevelopmentControllerSuspensionMode       `json:"-"`
	ExpectedRevision int64                                       `json:"-"`
	AvailableAt      time.Time                                   `json:"-"`
}

// PRDevelopmentControllerSuspensionClaim acquires one exact pending or
// expired suspension claim. ClaimID is caller-durable, so replaying a lost
// successful response does not rotate its credential while it remains live.
type PRDevelopmentControllerSuspensionClaim struct {
	CaseID           string        `json:"-"`
	SuspensionID     string        `json:"-"`
	ControllerID     string        `json:"-"`
	AttemptID        string        `json:"-"`
	ExpectedRevision int64         `json:"-"`
	ClaimID          string        `json:"-"`
	WorkerLabel      string        `json:"-"`
	Lease            time.Duration `json:"-"`
}

// PRDevelopmentControllerSuspensionLease carries the private filesystem
// authority needed to perform the exact canonical suspension request.
type PRDevelopmentControllerSuspensionLease struct {
	Controller PRDevelopmentController           `json:"-"`
	Suspension PRDevelopmentControllerSuspension `json:"-"`
	Reclaimed  bool                              `json:"-"`
}

// PRDevelopmentControllerSuspensionRenew extends only the exact live worker
// claim. It neither changes controller revision nor grants new Git authority.
type PRDevelopmentControllerSuspensionRenew struct {
	SuspensionID string        `json:"-"`
	ControllerID string        `json:"-"`
	AttemptID    string        `json:"-"`
	ClaimID      string        `json:"-"`
	ClaimToken   string        `json:"-"`
	ClaimEpoch   int64         `json:"-"`
	Lease        time.Duration `json:"-"`
}

// PRDevelopmentControllerSuspensionFinalize consumes one exact live claim
// and records content-addressed Git evidence that the line is reservation-free.
type PRDevelopmentControllerSuspensionFinalize struct {
	SuspensionID     string                                  `json:"-"`
	ControllerID     string                                  `json:"-"`
	AttemptID        string                                  `json:"-"`
	ExpectedRevision int64                                   `json:"-"`
	ClaimID          string                                  `json:"-"`
	ClaimToken       string                                  `json:"-"`
	ClaimEpoch       int64                                   `json:"-"`
	Result           PRDevelopmentControllerSuspensionResult `json:"-"`
}

// PRDevelopmentControllerSuspensionTransition is the exact durable state
// produced by suspension finalization.
type PRDevelopmentControllerSuspensionTransition struct {
	Controller PRDevelopmentController           `json:"-"`
	Suspension PRDevelopmentControllerSuspension `json:"-"`
}

// PRDevelopmentControllerSuspensionExecutionStore is deliberately narrower
// than the later explicit-resume protocol. Discovery grants no authority;
// only Claim returns the precommitted raw reservation bearer.
type PRDevelopmentControllerSuspensionExecutionStore interface {
	NextPRDevelopmentControllerSuspension(
		ctx context.Context,
	) (PRDevelopmentControllerSuspensionWorkCandidate, bool, error)
	ClaimPRDevelopmentControllerSuspension(
		ctx context.Context,
		input PRDevelopmentControllerSuspensionClaim,
	) (PRDevelopmentControllerSuspensionLease, bool, error)
	RenewPRDevelopmentControllerSuspension(
		ctx context.Context,
		input PRDevelopmentControllerSuspensionRenew,
	) error
	FinalizePRDevelopmentControllerSuspension(
		ctx context.Context,
		input PRDevelopmentControllerSuspensionFinalize,
	) (PRDevelopmentControllerSuspensionTransition, bool, error)
}
