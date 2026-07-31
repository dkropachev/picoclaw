package eventing

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const (
	reviewCaseIDPrefix       = "prc_"
	reviewFindingIDPrefix    = "prf_"
	reviewMessageIDPrefix    = "prm_"
	reviewSubmissionIDPrefix = "prs_"

	ReviewDraftSchemaVersion = 1

	// MaxReviewMessageBytes bounds one durable conversation entry by UTF-8
	// bytes after trimming.
	MaxReviewMessageBytes = 64 << 10
	// MaxReviewMessagesPerCase keeps the browser-visible transcript finite
	// while allowing 128 human/assistant exchanges.
	MaxReviewMessagesPerCase = 256
	// MaxReviewTranscriptBytes bounds the sum of durable message content for
	// one case. Individual messages remain subject to MaxReviewMessageBytes.
	MaxReviewTranscriptBytes = 4 << 20
)

var (
	// ErrInvalidReview reports malformed review capture or mutation input.
	ErrInvalidReview = errors.New("invalid pull request review")
	// ErrReviewConflict reports an optimistic-version or idempotency conflict.
	ErrReviewConflict = errors.New("pull request review conflict")
)

// ReviewCaseStatus is the durable lifecycle state of one captured review.
type ReviewCaseStatus string

const (
	ReviewCaseOpen              ReviewCaseStatus = "open"
	ReviewCaseAllDropped        ReviewCaseStatus = "all_dropped"
	ReviewCaseSubmitting        ReviewCaseStatus = "submitting"
	ReviewCaseSubmissionUnknown ReviewCaseStatus = "submission_unknown"
	ReviewCaseSubmitted         ReviewCaseStatus = "submitted"
	ReviewCaseStale             ReviewCaseStatus = "stale"
)

// ReviewFindingState records whether a finding remains in the draft.
type ReviewFindingState string

const (
	ReviewFindingActive  ReviewFindingState = "active"
	ReviewFindingDropped ReviewFindingState = "dropped"
)

// ReviewSeverity is the normalized severity of one review finding.
type ReviewSeverity string

const (
	ReviewSeverityCritical ReviewSeverity = "critical"
	ReviewSeverityHigh     ReviewSeverity = "high"
	ReviewSeverityMedium   ReviewSeverity = "medium"
	ReviewSeverityLow      ReviewSeverity = "low"
)

// ReviewMessageKind distinguishes general discussion from finding rephrasing.
type ReviewMessageKind string

const (
	ReviewMessageChat     ReviewMessageKind = "chat"
	ReviewMessageRephrase ReviewMessageKind = "rephrase"
)

// ReviewMessageRole identifies the author side of one review-case message.
type ReviewMessageRole string

const (
	ReviewMessageUser      ReviewMessageRole = "user"
	ReviewMessageAssistant ReviewMessageRole = "assistant"
)

// ReviewSubmissionStatus is the durable submission-outbox state.
type ReviewSubmissionStatus string

const (
	ReviewSubmissionPending   ReviewSubmissionStatus = "pending"
	ReviewSubmissionClaimed   ReviewSubmissionStatus = "claimed"
	ReviewSubmissionSubmitted ReviewSubmissionStatus = "submitted"
	ReviewSubmissionUnknown   ReviewSubmissionStatus = "unknown"
	ReviewSubmissionFailed    ReviewSubmissionStatus = "failed"
)

// ReviewDraft is the typed workflow output captured by the review sink.
type ReviewDraft struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Summary       string               `json:"summary"`
	Findings      []ReviewFindingDraft `json:"findings"`
	Tests         []string             `json:"tests,omitempty"`
	ResidualRisks []string             `json:"residualRisks,omitempty"`
}

// ReviewFindingDraft is one immutable-at-capture proposed review comment.
type ReviewFindingDraft struct {
	Severity       ReviewSeverity `json:"severity"`
	Title          string         `json:"title"`
	File           string         `json:"file,omitempty"`
	Line           *int           `json:"line,omitempty"`
	Message        string         `json:"message"`
	Evidence       string         `json:"evidence,omitempty"`
	Impact         string         `json:"impact,omitempty"`
	Recommendation string         `json:"recommendation,omitempty"`
	Validation     string         `json:"validation,omitempty"`
}

// ReviewCaptureInput binds a parsed workflow draft to trusted dispatch and
// pull-request identity. The store verifies dispatch-owned fields before
// inserting the review case.
type ReviewCaptureInput struct {
	EventID          string      `json:"event_id"`
	DispatchID       string      `json:"dispatch_id"`
	RunID            string      `json:"run_id"`
	WorkflowRef      string      `json:"workflow_ref"`
	WorkflowRevision string      `json:"workflow_revision,omitempty"`
	Connector        string      `json:"connector"`
	Repository       string      `json:"repository"`
	PullNumber       int64       `json:"pull_number"`
	PullURL          string      `json:"pull_url"`
	BaseSHA          string      `json:"base_sha"`
	HeadSHA          string      `json:"head_sha"`
	Draft            ReviewDraft `json:"draft"`
}

// ReviewCase is the payload-free durable projection used by list and capture.
type ReviewCase struct {
	ID               string           `json:"id"`
	EventID          string           `json:"event_id"`
	DispatchID       string           `json:"dispatch_id"`
	RunID            string           `json:"run_id"`
	WorkflowRef      string           `json:"workflow_ref"`
	WorkflowRevision string           `json:"workflow_revision,omitempty"`
	Connector        string           `json:"connector"`
	Repository       string           `json:"repository"`
	PullNumber       int64            `json:"pull_number"`
	PullURL          string           `json:"pull_url"`
	BaseSHA          string           `json:"base_sha"`
	HeadSHA          string           `json:"head_sha"`
	Summary          string           `json:"summary"`
	Tests            []string         `json:"tests,omitempty"`
	ResidualRisks    []string         `json:"residual_risks,omitempty"`
	Status           ReviewCaseStatus `json:"status"`
	Version          int64            `json:"version"`
	ActiveFindings   int              `json:"active_findings"`
	TotalFindings    int              `json:"total_findings"`
	PublicErrorCode  string           `json:"public_error_code,omitempty"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
	ResolvedAt       *time.Time       `json:"resolved_at,omitempty"`
	SubmittedAt      *time.Time       `json:"submitted_at,omitempty"`
}

// ReviewFinding is one editable finding in a captured review.
type ReviewFinding struct {
	ID             string             `json:"id"`
	CaseID         string             `json:"case_id"`
	Ordinal        int                `json:"ordinal"`
	State          ReviewFindingState `json:"state"`
	Severity       ReviewSeverity     `json:"severity"`
	Title          string             `json:"title"`
	File           string             `json:"file,omitempty"`
	Line           *int               `json:"line,omitempty"`
	Message        string             `json:"message"`
	Evidence       string             `json:"evidence,omitempty"`
	Impact         string             `json:"impact,omitempty"`
	Recommendation string             `json:"recommendation,omitempty"`
	Validation     string             `json:"validation,omitempty"`
	DroppedReason  string             `json:"dropped_reason,omitempty"`
	Revision       int64              `json:"revision"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
	DroppedAt      *time.Time         `json:"dropped_at,omitempty"`
}

// ReviewMessage is an append-only case conversation entry.
type ReviewMessage struct {
	ID        string            `json:"id"`
	CaseID    string            `json:"case_id"`
	Ordinal   int               `json:"ordinal"`
	FindingID string            `json:"finding_id,omitempty"`
	Kind      ReviewMessageKind `json:"kind"`
	Role      ReviewMessageRole `json:"role"`
	Content   string            `json:"content"`
	CreatedAt time.Time         `json:"created_at"`
}

// ReviewSubmission is one immutable request in the submission outbox.
type ReviewSubmission struct {
	ID               string                 `json:"id"`
	CaseID           string                 `json:"case_id"`
	DraftVersion     int64                  `json:"draft_version"`
	Marker           string                 `json:"-"`
	Status           ReviewSubmissionStatus `json:"status"`
	ClaimFrom        ReviewSubmissionStatus `json:"-"`
	LeaseToken       string                 `json:"-"`
	LeaseUntil       *time.Time             `json:"-"`
	Attempts         int                    `json:"attempts"`
	Request          json.RawMessage        `json:"-"`
	PublicErrorCode  string                 `json:"public_error_code,omitempty"`
	InternalError    string                 `json:"-"`
	ExternalReviewID string                 `json:"external_review_id,omitempty"`
	ExternalURL      string                 `json:"external_url,omitempty"`
	CreatedAt        time.Time              `json:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
	SubmittedAt      *time.Time             `json:"submitted_at,omitempty"`
}

// ReviewCaseDetail is the complete operator-editable case aggregate.
type ReviewCaseDetail struct {
	Case       ReviewCase        `json:"case"`
	Findings   []ReviewFinding   `json:"findings"`
	Messages   []ReviewMessage   `json:"messages"`
	Submission *ReviewSubmission `json:"submission,omitempty"`
}

// ReviewCaseCursor is the stable newest-first case-list position.
type ReviewCaseCursor struct {
	UpdatedAt time.Time `json:"updated_at"`
	ID        string    `json:"id"`
}

// ReviewCaseFilter selects review cases in newest-first keyset order.
type ReviewCaseFilter struct {
	Status     ReviewCaseStatus
	Connector  string
	Repository string
	PullNumber int64
	After      *ReviewCaseCursor
	Limit      int
}

// ReviewCasePage is one keyset-paginated review-case result.
type ReviewCasePage struct {
	Cases []ReviewCase      `json:"cases"`
	Next  *ReviewCaseCursor `json:"next,omitempty"`
}

// ReviewFindingUpdate replaces all editable fields of an active finding.
type ReviewFindingUpdate struct {
	CaseID          string
	FindingID       string
	ExpectedVersion int64
	Finding         ReviewFindingDraft
}

// ReviewFindingTransition moves a finding between active and dropped states.
type ReviewFindingTransition struct {
	CaseID          string
	FindingID       string
	ExpectedVersion int64
	Reason          string
}

// ReviewMessageDraft is one entry to append atomically with a case-version
// increment. FindingID is optional and must belong to the case when present.
type ReviewMessageDraft struct {
	FindingID string            `json:"finding_id,omitempty"`
	Kind      ReviewMessageKind `json:"kind"`
	Role      ReviewMessageRole `json:"role"`
	Content   string            `json:"content"`
}

// ReviewMessageAppend appends one or more messages under an optimistic case
// version.
type ReviewMessageAppend struct {
	CaseID          string
	ExpectedVersion int64
	Messages        []ReviewMessageDraft
}

// ReviewSubmissionDraft atomically freezes a case version into an immutable
// pending outbox request and moves the case to submitting.
type ReviewSubmissionDraft struct {
	CaseID          string
	ExpectedVersion int64
	Marker          string
	Request         json.RawMessage
}

// ReviewSubmissionOutcome finishes an owned submission attempt. Status must
// be submitted, unknown, or failed. Unknown is terminal and read-only because
// automatically posting again could duplicate a remote review. Stale marks a
// failed case permanently stale because its pull-request head no longer
// matches.
type ReviewSubmissionOutcome struct {
	SubmissionID     string
	LeaseToken       string
	Status           ReviewSubmissionStatus
	Stale            bool
	PublicErrorCode  string
	InternalError    string
	ExternalReviewID string
	ExternalURL      string
}

// ReviewReconciliationResolution is a human assertion about an ambiguous
// external GitHub outcome.
type ReviewReconciliationResolution string

const (
	// ReviewReconciliationSubmitted confirms that the unknown attempt did
	// create the intended GitHub review.
	ReviewReconciliationSubmitted ReviewReconciliationResolution = "submitted"
	// ReviewReconciliationAbsent confirms that no review was created, allowing
	// the case to return to an editable open version.
	ReviewReconciliationAbsent ReviewReconciliationResolution = "absent"
)

// ReviewSubmissionReconciliation atomically resolves the latest unknown
// submission for a case under optimistic version control.
type ReviewSubmissionReconciliation struct {
	CaseID          string
	ExpectedVersion int64
	Resolution      ReviewReconciliationResolution
}

// ReviewCaseStore owns captured review inspection and optimistic editing.
type ReviewCaseStore interface {
	CaptureReview(ctx context.Context, input ReviewCaptureInput) (ReviewCase, bool, error)
	GetReviewCase(ctx context.Context, caseID string) (ReviewCaseDetail, error)
	ListReviewCases(ctx context.Context, filter ReviewCaseFilter) (ReviewCasePage, error)
	UpdateReviewFinding(ctx context.Context, input ReviewFindingUpdate) (ReviewCaseDetail, error)
	DropReviewFinding(ctx context.Context, input ReviewFindingTransition) (ReviewCaseDetail, error)
	RestoreReviewFinding(ctx context.Context, input ReviewFindingTransition) (ReviewCaseDetail, error)
	AppendReviewMessages(ctx context.Context, input ReviewMessageAppend) (ReviewCaseDetail, error)
	CreateReviewSubmission(ctx context.Context, input ReviewSubmissionDraft) (ReviewCaseDetail, error)
	ReconcileReviewSubmission(
		ctx context.Context,
		input ReviewSubmissionReconciliation,
	) (ReviewCaseDetail, error)
}

// ReviewSubmissionQueue owns leased submission-outbox delivery.
type ReviewSubmissionQueue interface {
	GetReviewSubmission(ctx context.Context, submissionID string) (ReviewSubmission, error)
	ClaimReviewSubmissions(
		ctx context.Context,
		workerLabel string,
		limit int,
		lease time.Duration,
	) ([]ReviewSubmission, error)
	RenewReviewSubmissionLease(ctx context.Context, submissionID, leaseToken string, lease time.Duration) error
	FinishReviewSubmission(ctx context.Context, outcome ReviewSubmissionOutcome) (ReviewCaseDetail, error)
}

// ReviewStore is the complete durable pull-request review boundary.
type ReviewStore interface {
	ReviewCaseStore
	ReviewSubmissionQueue
}
