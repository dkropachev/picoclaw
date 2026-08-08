package eventing

import (
	"context"
	"errors"
	"time"
)

const (
	prDevelopmentCaseIDPrefix            = "pdc_"
	prDevelopmentMessageIDPrefix         = "pdm_"
	prDevelopmentRepairSessionIDPrefix   = "pds_"
	prDevelopmentRepairAttemptIDPrefix   = "pdr_"
	prDevelopmentRepairReservationPrefix = "pdrk_"

	// MaxPRDevelopmentMessageBytes bounds one durable conversation message by
	// UTF-8 bytes after trimming.
	MaxPRDevelopmentMessageBytes = 64 << 10
	// MaxPRDevelopmentMessagesPerCase keeps one development-case transcript
	// finite while allowing 128 human/assistant exchanges.
	MaxPRDevelopmentMessagesPerCase = 256
	// MaxPRDevelopmentTranscriptBytes bounds the sum of durable message content
	// for one development case.
	MaxPRDevelopmentTranscriptBytes = 4 << 20

	// MaxPRDevelopmentRepairAttempts keeps one durable local-development
	// session finite. A completed or failed attempt remains immutable evidence
	// when a user explicitly admits another attempt.
	MaxPRDevelopmentRepairAttempts = 64
	// MaxPRDevelopmentRepairVersion bounds browser-visible lifecycle transitions.
	// Admission and claim reserve enough remaining revisions to reach a terminal
	// outcome; safe preparing-lease reclaims rotate only private lease state.
	MaxPRDevelopmentRepairVersion = 1024
	// MaxPRDevelopmentRepairInstructionBytes bounds one durable, browser-visible
	// local repair instruction independently of the generic runner's larger input.
	MaxPRDevelopmentRepairInstructionBytes = 4 << 10
	// MaxPRDevelopmentRepairSummaryBytes bounds one durable, browser-visible
	// local repair outcome independently of the generic runner's larger answer.
	MaxPRDevelopmentRepairSummaryBytes = 4 << 10
	// MaxPRDevelopmentRepairIterations matches the largest supported isolated
	// local repair loop configuration.
	MaxPRDevelopmentRepairIterations = 128
	// MaxPRDevelopmentRepairAgentIDBytes is the canonical runtime agent-ID
	// boundary. IDs must additionally match [a-z0-9][a-z0-9_-]*.
	MaxPRDevelopmentRepairAgentIDBytes = 64
	// MaxPRDevelopmentRepairIdempotencyBytes bounds a caller-controlled private
	// admission marker.
	MaxPRDevelopmentRepairIdempotencyBytes = 256
)

var (
	// ErrInvalidPRDevelopment reports malformed development-case capture input.
	ErrInvalidPRDevelopment = errors.New("invalid pull request development case")
	// ErrPRDevelopmentConflict reports an immutable capture or provenance conflict.
	ErrPRDevelopmentConflict = errors.New("pull request development case conflict")
	// ErrPRDevelopmentConversationConflict reports a stale optimistic
	// conversation version.
	ErrPRDevelopmentConversationConflict = errors.New(
		"pull request development conversation conflict",
	)
	// ErrPRDevelopmentConversationCapacity reports that a transcript cannot
	// accept another otherwise-valid message within its durable bounds.
	ErrPRDevelopmentConversationCapacity = errors.New(
		"pull request development conversation capacity exceeded",
	)
	// ErrInvalidPRDevelopmentRepair reports malformed repair admission or
	// lifecycle input.
	ErrInvalidPRDevelopmentRepair = errors.New(
		"invalid pull request development repair",
	)
	// ErrPRDevelopmentRepairConflict reports a stale repair version, a changed
	// idempotency binding, or an immutable pin conflict.
	ErrPRDevelopmentRepairConflict = errors.New(
		"pull request development repair conflict",
	)
	// ErrPRDevelopmentRepairActive reports that the singleton session already
	// has queued or leased work.
	ErrPRDevelopmentRepairActive = errors.New(
		"pull request development repair is already active",
	)
	// ErrPRDevelopmentRepairCapacity reports that the singleton session has
	// reached its append-only attempt limit.
	ErrPRDevelopmentRepairCapacity = errors.New(
		"pull request development repair attempt capacity exceeded",
	)
)

// PRDevelopmentPullState is the provider-verified current pull-request state.
type PRDevelopmentPullState string

const (
	PRDevelopmentPullOpen   PRDevelopmentPullState = "open"
	PRDevelopmentPullClosed PRDevelopmentPullState = "closed"
)

// PRDevelopmentReviewState is a provider review state. A submitted occurrence
// cannot be dismissed, while the provider's later current view may be.
type PRDevelopmentReviewState string

const (
	PRDevelopmentReviewApproved         PRDevelopmentReviewState = "approved"
	PRDevelopmentReviewChangesRequested PRDevelopmentReviewState = "changes_requested"
	PRDevelopmentReviewCommented        PRDevelopmentReviewState = "commented"
	PRDevelopmentReviewDismissed        PRDevelopmentReviewState = "dismissed"
)

// PRDevelopmentCaptureIdentity is the immutable dispatch provenance checked
// before a caller performs another mutable provider read after a sink retry.
type PRDevelopmentCaptureIdentity struct {
	EventID          string `json:"event_id"`
	DispatchID       string `json:"dispatch_id"`
	RunID            string `json:"run_id"`
	WorkflowRef      string `json:"workflow_ref"`
	WorkflowRevision string `json:"workflow_revision"`
	Connector        string `json:"connector"`
}

// PRDevelopmentCaptureInput binds one provider-verified own-PR review
// occurrence to its trusted event-workflow provenance. TriggerReviewNodeID is
// only body-authenticated routing evidence; it is deliberately not described
// as provider-verified identity.
type PRDevelopmentCaptureInput struct {
	PRDevelopmentCaptureIdentity

	Repository string                 `json:"repository"`
	PullNumber int64                  `json:"pull_number"`
	PullURL    string                 `json:"pull_url"`
	PullAuthor string                 `json:"pull_author"`
	TargetUser string                 `json:"target_user"`
	PullState  PRDevelopmentPullState `json:"pull_state"`
	PullDraft  bool                   `json:"pull_draft"`
	PullMerged bool                   `json:"pull_merged"`

	BaseRepository string `json:"base_repository"`
	BaseRef        string `json:"base_ref"`
	BaseSHA        string `json:"base_sha"`
	HeadRepository string `json:"head_repository"`
	HeadRef        string `json:"head_ref"`
	HeadSHA        string `json:"head_sha"`

	ReviewID             string                   `json:"review_id"`
	TriggerReviewNodeID  string                   `json:"trigger_review_node_id"`
	ReviewAuthor         string                   `json:"review_author"`
	SubmittedReviewState PRDevelopmentReviewState `json:"submitted_review_state"`
	CurrentReviewState   PRDevelopmentReviewState `json:"current_review_state"`
	ReviewCommitSHA      string                   `json:"review_commit_sha"`
	ReviewSubmittedAt    time.Time                `json:"review_submitted_at"`
	ReviewURL            string                   `json:"review_url"`
	Feedback             string                   `json:"feedback"`
}

// PRDevelopmentCase is one immutable provider review occurrence captured by
// one exact event dispatch and workflow run. An explicit event replay has new
// provenance and may intentionally create a distinct case.
type PRDevelopmentCase struct {
	ID string `json:"id"`
	PRDevelopmentCaptureInput
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PRDevelopmentCaseCursor identifies one immutable position in the
// newest-first development-case list.
type PRDevelopmentCaseCursor struct {
	UpdatedAt time.Time `json:"updated_at"`
	ID        string    `json:"id"`
}

// PRDevelopmentCaseFilter selects immutable development cases in newest-first
// keyset order. Repository matching follows the provider's case-insensitive
// owner/repository identity, while PullNumber is an exact numeric match.
type PRDevelopmentCaseFilter struct {
	Repository string
	PullNumber int64
	After      *PRDevelopmentCaseCursor
	Limit      int
}

// PRDevelopmentCasePage is one stable keyset-paginated development-case
// result.
type PRDevelopmentCasePage struct {
	Cases []PRDevelopmentCase      `json:"cases"`
	Next  *PRDevelopmentCaseCursor `json:"next,omitempty"`
}

// PRDevelopmentMessageRole identifies the author side of one append-only
// development-case conversation message.
type PRDevelopmentMessageRole string

const (
	PRDevelopmentMessageUser      PRDevelopmentMessageRole = "user"
	PRDevelopmentMessageAssistant PRDevelopmentMessageRole = "assistant"
)

// PRDevelopmentMessage is one append-only development-case conversation
// entry. Ordinals are zero-based and contiguous within a case.
type PRDevelopmentMessage struct {
	ID        string                   `json:"id"`
	CaseID    string                   `json:"case_id"`
	Ordinal   int                      `json:"ordinal"`
	Role      PRDevelopmentMessageRole `json:"role"`
	Content   string                   `json:"content"`
	CreatedAt time.Time                `json:"created_at"`
}

// PRDevelopmentConversation is the complete bounded transcript for one
// immutable development case. Version is exactly len(Messages).
type PRDevelopmentConversation struct {
	CaseID   string                 `json:"case_id"`
	Version  int64                  `json:"version"`
	Messages []PRDevelopmentMessage `json:"messages"`
}

// PRDevelopmentMessageAppend appends one message under an optimistic
// transcript version. ExpectedVersion starts at zero for an empty transcript.
type PRDevelopmentMessageAppend struct {
	CaseID          string
	ExpectedVersion int64
	Role            PRDevelopmentMessageRole
	Content         string
}

// PRDevelopmentRepairStatus is the durable lifecycle of one local repair
// attempt. A running lease that expires is terminalized as recovery_required;
// it is never executed again automatically because local edits may exist.
type PRDevelopmentRepairStatus string

const (
	PRDevelopmentRepairQueued           PRDevelopmentRepairStatus = "queued"
	PRDevelopmentRepairPreparing        PRDevelopmentRepairStatus = "preparing"
	PRDevelopmentRepairRunning          PRDevelopmentRepairStatus = "running"
	PRDevelopmentRepairCompleted        PRDevelopmentRepairStatus = "completed"
	PRDevelopmentRepairFailed           PRDevelopmentRepairStatus = "failed"
	PRDevelopmentRepairRecoveryRequired PRDevelopmentRepairStatus = "recovery_required"
)

// PRDevelopmentRepairErrorCode is a bounded browser-safe outcome category.
// Detailed provider, model, checkout, and filesystem errors remain private.
type PRDevelopmentRepairErrorCode string

const (
	PRDevelopmentRepairErrorProviderChanged      PRDevelopmentRepairErrorCode = "provider_changed"
	PRDevelopmentRepairErrorNotActionable        PRDevelopmentRepairErrorCode = "not_actionable"
	PRDevelopmentRepairErrorRuntimeUnavailable   PRDevelopmentRepairErrorCode = "runtime_unavailable"
	PRDevelopmentRepairErrorWorkspaceUnavailable PRDevelopmentRepairErrorCode = "workspace_unavailable"
	PRDevelopmentRepairErrorRepairFailed         PRDevelopmentRepairErrorCode = "repair_failed"
	PRDevelopmentRepairErrorRecoveryRequired     PRDevelopmentRepairErrorCode = "recovery_required"
	PRDevelopmentRepairErrorInternal             PRDevelopmentRepairErrorCode = "internal_error"
)

// PRDevelopmentRepairAttempt is one append-only user-authorized repair
// attempt. Lease and idempotency credentials are intentionally omitted from
// JSON so a trusted worker can share the same type without exposing authority
// through browser projections.
type PRDevelopmentRepairAttempt struct {
	ID                    string                       `json:"id"`
	SessionID             string                       `json:"session_id"`
	Ordinal               int                          `json:"ordinal"`
	ExpectedRepairVersion int64                        `json:"expected_repair_version"`
	ConversationVersion   int64                        `json:"conversation_version"`
	IdempotencyKey        string                       `json:"-"`
	Instruction           string                       `json:"instruction"`
	Status                PRDevelopmentRepairStatus    `json:"status"`
	LeaseOwner            string                       `json:"-"`
	LeaseToken            string                       `json:"-"`
	LeaseUntil            *time.Time                   `json:"-"`
	Claims                int                          `json:"claims"`
	Summary               string                       `json:"summary,omitempty"`
	ErrorCode             PRDevelopmentRepairErrorCode `json:"error_code,omitempty"`
	InternalError         string                       `json:"-"`
	Iterations            int                          `json:"iterations"`
	CreatedAt             time.Time                    `json:"created_at"`
	UpdatedAt             time.Time                    `json:"updated_at"`
}

// PRDevelopmentRepairSession is the singleton local checkout reservation for
// one immutable development case. The verified provider pin is all-or-none
// and immutable after first persistence. CloneURL, ReviewDigest, and
// ReservationKey remain controller-private.
type PRDevelopmentRepairSession struct {
	ID             string                       `json:"id"`
	CaseID         string                       `json:"case_id"`
	Version        int64                        `json:"version"`
	AgentID        string                       `json:"agent_id"`
	HeadRepository string                       `json:"head_repository,omitempty"`
	HeadRef        string                       `json:"head_ref,omitempty"`
	HeadSHA        string                       `json:"head_sha,omitempty"`
	CloneURL       string                       `json:"-"`
	ReviewDigest   string                       `json:"-"`
	ReservationKey string                       `json:"-"`
	WorkspaceID    string                       `json:"workspace_id,omitempty"`
	Attempts       []PRDevelopmentRepairAttempt `json:"attempts"`
	CreatedAt      time.Time                    `json:"created_at"`
	UpdatedAt      time.Time                    `json:"updated_at"`
}

// PRDevelopmentWorkbench is one atomic read snapshot of the immutable case,
// bounded conversation, and optional singleton repair session.
type PRDevelopmentWorkbench struct {
	Case          PRDevelopmentCase           `json:"case"`
	Conversation  PRDevelopmentConversation   `json:"conversation"`
	RepairSession *PRDevelopmentRepairSession `json:"repair_session,omitempty"`
}

// PRDevelopmentRepairAdmit atomically fences both mutable workbench versions
// before creating one queued attempt. ExpectedRepairVersion is zero before a
// session exists.
type PRDevelopmentRepairAdmit struct {
	CaseID                      string
	ExpectedConversationVersion int64
	ExpectedRepairVersion       int64
	IdempotencyKey              string
	AgentID                     string
	Instruction                 string
}

// PRDevelopmentRepairClaimRequest leases at most one oldest queued or safely
// reclaimable preparing attempt.
type PRDevelopmentRepairClaimRequest struct {
	WorkerLabel string
	Lease       time.Duration
}

// PRDevelopmentRepairPin stores the exact provider-observed source and review
// evidence under a live preparing lease. Repeating the same pin is idempotent;
// changing any field is a conflict.
type PRDevelopmentRepairPin struct {
	AttemptID      string
	LeaseToken     string
	HeadRepository string
	HeadRef        string
	HeadSHA        string
	CloneURL       string
	ReviewDigest   string
}

// PRDevelopmentRepairBegin advances a live preparing attempt to running and
// atomically refreshes its lease for the execution window. The isolated runner
// reveals its opaque workspace ID only after acquisition, so that optional ID
// is persisted with the terminal outcome instead.
type PRDevelopmentRepairBegin struct {
	AttemptID  string
	LeaseToken string
	Lease      time.Duration
}

// PRDevelopmentRepairOutcome records one terminal result under a live lease.
// Failed is the safe pre-execution outcome from preparing. Completed and
// recovery_required are post-execution outcomes valid only from running.
// Completed requires a stable opaque workspace ID; recovery_required keeps it
// optional because workspace acquisition itself can become ambiguous.
type PRDevelopmentRepairOutcome struct {
	AttemptID     string
	LeaseToken    string
	Status        PRDevelopmentRepairStatus
	Summary       string
	ErrorCode     PRDevelopmentRepairErrorCode
	InternalError string
	Iterations    int
	WorkspaceID   string
}

// PRDevelopmentCaseStore owns immutable development-case capture and exact
// lookup. Conversation, checkout, execution, publication, and provider actions
// are intentionally separate capabilities.
type PRDevelopmentCaseStore interface {
	LookupPRDevelopmentCapture(
		ctx context.Context,
		identity PRDevelopmentCaptureIdentity,
	) (PRDevelopmentCase, bool, error)
	CapturePRDevelopmentCase(
		ctx context.Context,
		input PRDevelopmentCaptureInput,
	) (PRDevelopmentCase, bool, error)
	GetPRDevelopmentCase(ctx context.Context, id string) (PRDevelopmentCase, error)
}

// PRDevelopmentCaseReader is the separate immutable workbench read boundary.
// Keeping it distinct avoids widening the capture interface implemented by
// existing integrations.
type PRDevelopmentCaseReader interface {
	GetPRDevelopmentCase(ctx context.Context, id string) (PRDevelopmentCase, error)
	ListPRDevelopmentCases(
		ctx context.Context,
		filter PRDevelopmentCaseFilter,
	) (PRDevelopmentCasePage, error)
}

// PRDevelopmentConversationStore owns only the bounded append-only
// conversation beside an immutable development case. It grants no capture,
// repository, execution, publication, or provider authority.
type PRDevelopmentConversationStore interface {
	GetPRDevelopmentConversation(
		ctx context.Context,
		caseID string,
	) (PRDevelopmentConversation, error)
	AppendPRDevelopmentMessage(
		ctx context.Context,
		input PRDevelopmentMessageAppend,
	) (PRDevelopmentConversation, error)
}

// PRDevelopmentWorkbenchReader provides one cross-table SQLite snapshot for
// browser/controller reads without granting mutation authority.
type PRDevelopmentWorkbenchReader interface {
	GetPRDevelopmentWorkbench(
		ctx context.Context,
		caseID string,
	) (PRDevelopmentWorkbench, error)
}

// PRDevelopmentRepairAdmitter owns only user-authorized, version-fenced
// attempt admission.
type PRDevelopmentRepairAdmitter interface {
	AdmitPRDevelopmentRepair(
		ctx context.Context,
		input PRDevelopmentRepairAdmit,
	) (PRDevelopmentWorkbench, bool, error)
}

// PRDevelopmentRepairQueue owns the private leased worker lifecycle.
type PRDevelopmentRepairQueue interface {
	ClaimPRDevelopmentRepair(
		ctx context.Context,
		input PRDevelopmentRepairClaimRequest,
	) (PRDevelopmentRepairSession, bool, error)
	RenewPRDevelopmentRepairLease(
		ctx context.Context,
		attemptID, leaseToken string,
		lease time.Duration,
	) error
	PinPRDevelopmentRepairSession(
		ctx context.Context,
		input PRDevelopmentRepairPin,
	) (PRDevelopmentRepairSession, error)
	BeginPRDevelopmentRepair(
		ctx context.Context,
		input PRDevelopmentRepairBegin,
	) (PRDevelopmentRepairSession, error)
	FinishPRDevelopmentRepair(
		ctx context.Context,
		input PRDevelopmentRepairOutcome,
	) (PRDevelopmentRepairSession, error)
}
