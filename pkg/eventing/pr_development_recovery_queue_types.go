package eventing

import (
	"context"
	"time"
)

// PRDevelopmentControllerRecoveryWorkKind identifies the private durable
// recovery protocol that owns one claimable controller. The value is never
// projected through an HTTP or model-facing DTO.
type PRDevelopmentControllerRecoveryWorkKind string

const (
	PRDevelopmentControllerRecoveryWorkOperation   PRDevelopmentControllerRecoveryWorkKind = "operation_recovery"
	PRDevelopmentControllerRecoveryWorkReservation PRDevelopmentControllerRecoveryWorkKind = "reservation_recovery"
)

// PRDevelopmentControllerRecoveryCandidate is a non-authorizing pointer to
// the oldest exact durable recovery intent. A caller must still acquire the
// corresponding v12 or v13 claim before invoking any Git effect.
type PRDevelopmentControllerRecoveryCandidate struct {
	Kind             PRDevelopmentControllerRecoveryWorkKind `json:"-"`
	CaseID           string                                  `json:"-"`
	ControllerID     string                                  `json:"-"`
	AttemptID        string                                  `json:"-"`
	RecoveryID       string                                  `json:"-"`
	OperationID      string                                  `json:"-"`
	ExpectedRevision int64                                   `json:"-"`
	AvailableAt      time.Time                               `json:"-"`
}

// PRDevelopmentControllerRecoveryScanner owns only database discovery and
// staging. It grants neither a recovery claim nor filesystem authority.
type PRDevelopmentControllerRecoveryScanner interface {
	StageExpiredPRDevelopmentControllerRecoveries(
		ctx context.Context,
		limit int,
	) (int, error)
	NextPRDevelopmentControllerRecovery(
		ctx context.Context,
	) (PRDevelopmentControllerRecoveryCandidate, bool, error)
}
