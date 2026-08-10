package eventing

import (
	"context"
	"time"
)

// PRDevelopmentControllerSuspendedResumeLease carries the exact durable
// resume request and its fresh reservation only inside the trusted local
// repair boundary. It grants no model or filesystem authority by itself.
type PRDevelopmentControllerSuspendedResumeLease struct {
	Controller PRDevelopmentController           `json:"-"`
	Suspension PRDevelopmentControllerSuspension `json:"-"`
	Reclaimed  bool                              `json:"-"`
}

// PRDevelopmentControllerSuspendedResumeRenew extends only the exact live
// resume claim. It cannot change the controller or replacement reservation.
type PRDevelopmentControllerSuspendedResumeRenew struct {
	ControllerID string        `json:"-"`
	AttemptID    string        `json:"-"`
	SuspensionID string        `json:"-"`
	ClaimID      string        `json:"-"`
	ClaimToken   string        `json:"-"`
	ClaimEpoch   int64         `json:"-"`
	Lease        time.Duration `json:"-"`
}

// PRDevelopmentControllerSuspendedResumeFinalize consumes an exact live
// resume claim only after Git proves the retained candidate was restored
// under the precommitted globally fresh reservation.
type PRDevelopmentControllerSuspendedResumeFinalize struct {
	ControllerID     string                                       `json:"-"`
	AttemptID        string                                       `json:"-"`
	SuspensionID     string                                       `json:"-"`
	ExpectedRevision int64                                        `json:"-"`
	ClaimID          string                                       `json:"-"`
	ClaimToken       string                                       `json:"-"`
	ClaimEpoch       int64                                        `json:"-"`
	Result           PRDevelopmentControllerSuspendedResumeResult `json:"-"`
	Lease            time.Duration                                `json:"-"`
}

// PRDevelopmentControllerSuspendedResumeStore owns the private store half of
// resume-before-model. Preparation is intentionally coupled to the existing
// orchestration controller acquisition so a queued attempt cannot gain a
// reservation without its exact live orchestration claim.
type PRDevelopmentControllerSuspendedResumeStore interface {
	RenewPRDevelopmentControllerSuspendedResume(
		ctx context.Context,
		input PRDevelopmentControllerSuspendedResumeRenew,
	) error
	FinalizePRDevelopmentControllerSuspendedResume(
		ctx context.Context,
		input PRDevelopmentControllerSuspendedResumeFinalize,
	) (PRDevelopmentControllerLease, bool, error)
}
