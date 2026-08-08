package eventing

import (
	"context"
	"errors"
	"time"
)

const (
	prDevelopmentCaseIDPrefix    = "pdc_"
	prDevelopmentMessageIDPrefix = "pdm_"

	// MaxPRDevelopmentMessageBytes bounds one durable conversation message by
	// UTF-8 bytes after trimming.
	MaxPRDevelopmentMessageBytes = 64 << 10
	// MaxPRDevelopmentMessagesPerCase keeps one development-case transcript
	// finite while allowing 128 human/assistant exchanges.
	MaxPRDevelopmentMessagesPerCase = 256
	// MaxPRDevelopmentTranscriptBytes bounds the sum of durable message content
	// for one development case.
	MaxPRDevelopmentTranscriptBytes = 4 << 20
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
