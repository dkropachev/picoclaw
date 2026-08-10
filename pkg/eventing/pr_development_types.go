package eventing

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const (
	prDevelopmentCaseIDPrefix             = "pdc_"
	prDevelopmentThreadIDPrefix           = "pdt_"
	prDevelopmentMessageIDPrefix          = "pdm_"
	prDevelopmentRepairSessionIDPrefix    = "pds_"
	prDevelopmentRepairAttemptIDPrefix    = "pdr_"
	prDevelopmentRepairReservationPrefix  = "pdrk_"
	prDevelopmentControllerIDPrefix       = "pctl_"
	prDevelopmentLineIDPrefix             = "pdln_"
	prDevelopmentControllerKeyPrefix      = "pdck_"
	prDevelopmentRecoveryIntentIDPrefix   = "pdri_"
	prDevelopmentOperationIDPrefix        = "pdop_"
	prDevelopmentSuspensionIDPrefix       = "pdsi_"
	prDevelopmentCommitIntentIDPrefix     = "pdcmt_"
	prDevelopmentParkIntentIDPrefix       = "pdlnpark_"
	prDevelopmentLedgerEntryIDPrefix      = "pdle_"
	prDevelopmentLedgerCheckpointIDPrefix = "pdlc_"

	// MaxPRDevelopmentMessageBytes bounds one durable conversation message by
	// UTF-8 bytes after trimming.
	MaxPRDevelopmentMessageBytes = 64 << 10
	// MaxPRDevelopmentMessagesPerCase keeps one development-case transcript
	// finite while allowing 128 human/assistant exchanges.
	MaxPRDevelopmentMessagesPerCase = 256
	// MaxPRDevelopmentTranscriptBytes bounds the sum of durable message content
	// for one development case.
	MaxPRDevelopmentTranscriptBytes = 4 << 20
	// MaxPRDevelopmentThreadCases bounds immutable review occurrences carried
	// by one provider-verified pull-request thread.
	MaxPRDevelopmentThreadCases = 8192

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
	// MaxPRDevelopmentControllerRevision bounds material controller state
	// transitions independently of private lease renewals and safe review
	// reclaims.
	MaxPRDevelopmentControllerRevision = 65_536
	// MaxPRDevelopmentControllerFences matches the retained development-line
	// version bound. One parked attempt contributes at most one immutable fence.
	MaxPRDevelopmentControllerFences = 8_192
	// MaxPRDevelopmentControllerRecoveries matches the bounded per-workspace
	// Git reservation-rotation history. Expiration fails before staging an
	// intent that the workspace inventory could not retain.
	MaxPRDevelopmentControllerRecoveries = 8_192
	// MaxPRDevelopmentControllerOperations permits Adopt/Resume, Commit, and
	// Park evidence for every retained-line version. Operation rows have their
	// own claim epochs and do not consume controller revisions merely by being
	// prepared or reclaimed.
	MaxPRDevelopmentControllerOperations = MaxPRDevelopmentControllerFences * 3
	// MaxPRDevelopmentOperationRequestBytes and
	// MaxPRDevelopmentOperationResultBytes bound the canonical private effect
	// evidence stored for one operation. Review diff text is deliberately not
	// retained here; it is re-snapshotted by the reservation-free review lease.
	MaxPRDevelopmentOperationRequestBytes = 32 << 10
	MaxPRDevelopmentOperationResultBytes  = 32 << 10
	// MaxPRDevelopmentControllerIdentityBytes bounds private controller, line,
	// lease-owner, and intent identities.
	MaxPRDevelopmentControllerIdentityBytes = 256
	// MaxPRDevelopmentLedgerEntries reserves exactly one mutation-log and one
	// local-review record for every retained-line fence.
	MaxPRDevelopmentLedgerEntries = MaxPRDevelopmentControllerFences * 2
	// MaxPRDevelopmentLedgerSummaryBytes keeps each attempt and review account
	// concise enough to carry into the next model context.
	MaxPRDevelopmentLedgerSummaryBytes = 4 << 10
	// MaxPRDevelopmentLedgerReviewFindings bounds one structured local review.
	MaxPRDevelopmentLedgerReviewFindings = 128
	// MaxPRDevelopmentLedgerReviewBytes bounds the aggregate UTF-8 review
	// finding payload independently of its per-field schema limits.
	MaxPRDevelopmentLedgerReviewBytes = 64 << 10
	// MaxPRDevelopmentLedgerCheckpointSummaryBytes bounds one logical
	// compaction summary. Raw ledger records are never removed.
	MaxPRDevelopmentLedgerCheckpointSummaryBytes = 32 << 10

	// PRDevelopmentAttentionDecisionReviewRequired is the one automatic
	// attention decision emitted when the local review worker cannot safely
	// continue without a configured gate decision.
	PRDevelopmentAttentionDecisionReviewRequired = "pr_development.review_attention_required"
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
	// ErrPRDevelopmentThreadCapacity reports that a provider thread cannot
	// accept another immutable review occurrence within its durable bound.
	ErrPRDevelopmentThreadCapacity = errors.New(
		"pull request development thread capacity exceeded",
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
	// ErrInvalidPRDevelopmentOrchestration reports malformed trusted-worker
	// checkpoint, claim, model-result, or local-CI evidence.
	ErrInvalidPRDevelopmentOrchestration = errors.New(
		"invalid pull request development repair orchestration",
	)
	// ErrPRDevelopmentOrchestrationConflict reports a changed replay or a
	// checkpoint that no longer owns its exact durable/controller fence.
	ErrPRDevelopmentOrchestrationConflict = errors.New(
		"pull request development repair orchestration conflict",
	)
	// ErrInvalidPRDevelopmentController reports malformed controller lease,
	// line-binding, or review-fence input.
	ErrInvalidPRDevelopmentController = errors.New(
		"invalid pull request development controller",
	)
	// ErrPRDevelopmentControllerConflict reports stale, cross-thread, changed,
	// or corrupt controller evidence.
	ErrPRDevelopmentControllerConflict = errors.New(
		"pull request development controller conflict",
	)
	// ErrPRDevelopmentControllerActive reports a live or unresolved operation
	// that excludes another mutation or review owner.
	ErrPRDevelopmentControllerActive = errors.New(
		"pull request development controller is active",
	)
	// ErrPRDevelopmentControllerRecoveryRequired reports an expired mutation
	// owner whose filesystem effects cannot be reclaimed automatically.
	ErrPRDevelopmentControllerRecoveryRequired = errors.New(
		"pull request development controller requires recovery",
	)
	// ErrInvalidPRDevelopmentLedger reports malformed append or compaction
	// input.
	ErrInvalidPRDevelopmentLedger = errors.New(
		"invalid pull request development ledger",
	)
	// ErrPRDevelopmentLedgerConflict reports changed replay, causal-order, or
	// integrity-chain disagreement.
	ErrPRDevelopmentLedgerConflict = errors.New(
		"pull request development ledger conflict",
	)
	// ErrPRDevelopmentLedgerCapacity reports exhausted entry or checkpoint
	// capacity.
	ErrPRDevelopmentLedgerCapacity = errors.New(
		"pull request development ledger capacity exceeded",
	)
	// ErrInvalidPRDevelopmentAttention reports a malformed attention snapshot,
	// semantic decision key, or deterministic workflow-run binding.
	ErrInvalidPRDevelopmentAttention = errors.New(
		"invalid pull request development attention decision",
	)
	// ErrPRDevelopmentAttentionConflict reports a stale subject snapshot, a
	// changed semantic-key replay, or a workflow run already bound elsewhere.
	ErrPRDevelopmentAttentionConflict = errors.New(
		"pull request development attention decision conflict",
	)
	// ErrPRDevelopmentAttentionAdmissionUncertain reports that the external
	// workflow create callback completed but SQLite could not confirm whether
	// the corresponding durable decision-run link committed.
	ErrPRDevelopmentAttentionAdmissionUncertain = errors.New(
		"pull request development attention decision run admission outcome is uncertain",
	)
	// ErrInvalidPRDevelopmentAttentionTrigger reports malformed automatic
	// attention occurrence, lease, policy-pin, or terminal input.
	ErrInvalidPRDevelopmentAttentionTrigger = errors.New(
		"invalid pull request development attention trigger",
	)
	// ErrPRDevelopmentAttentionTriggerConflict reports an immutable occurrence
	// or policy pin that differs from the already-durable value.
	ErrPRDevelopmentAttentionTriggerConflict = errors.New(
		"pull request development attention trigger conflict",
	)
	// ErrPRDevelopmentAttentionSuperseded reports that a claimed occurrence no
	// longer names the current review/controller tail and cannot admit a new
	// decision run. Historical exact linked-run replay remains separately valid.
	ErrPRDevelopmentAttentionSuperseded = errors.New(
		"pull request development attention trigger was superseded",
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

// PRDevelopmentThreadKind distinguishes provider-verified pull identity from
// one-case legacy isolation created during the schema-v9 migration.
type PRDevelopmentThreadKind string

const (
	PRDevelopmentThreadProvider PRDevelopmentThreadKind = "provider"
	PRDevelopmentThreadLegacy   PRDevelopmentThreadKind = "legacy"
)

// PRDevelopmentThreadIdentity is the immutable provider identity of one pull
// request. These provider object IDs are controller evidence and never belong
// in browser projections.
type PRDevelopmentThreadIdentity struct {
	Provider       string `json:"-"`
	ProviderOrigin string `json:"-"`
	PullAuthorID   string `json:"-"`
	RepositoryID   string `json:"-"`
	PullRequestID  string `json:"-"`
	PullNumber     int64  `json:"-"`
}

// PRDevelopmentCaptureRequest atomically binds one immutable review capture
// to its authenticated and current-provider-cross-bound pull-request identity.
type PRDevelopmentCaptureRequest struct {
	Case   PRDevelopmentCaptureInput   `json:"-"`
	Thread PRDevelopmentThreadIdentity `json:"-"`
}

// PRDevelopmentThreadCaseLink is one immutable position in a thread's
// database-assigned, contiguous capture order. Hash-chain fields remain store
// private and are checked by GetPRDevelopmentThreadForCase.
type PRDevelopmentThreadCaseLink struct {
	CaseID       string    `json:"-"`
	Ordinal      int       `json:"-"`
	LinkedAt     time.Time `json:"-"`
	PreviousHash string    `json:"-"`
	LinkHash     string    `json:"-"`
}

// PRDevelopmentThread is the complete integrity-checked provider or isolated
// legacy thread. Identity is zero for legacy threads.
type PRDevelopmentThread struct {
	ID           string                        `json:"-"`
	Kind         PRDevelopmentThreadKind       `json:"-"`
	Identity     PRDevelopmentThreadIdentity   `json:"-"`
	LegacyCaseID string                        `json:"-"`
	CaseCount    int                           `json:"-"`
	IdentityHash string                        `json:"-"`
	CasesDigest  string                        `json:"-"`
	Cases        []PRDevelopmentThreadCaseLink `json:"-"`
	CreatedAt    time.Time                     `json:"-"`
	UpdatedAt    time.Time                     `json:"-"`
}

// PRDevelopmentThreadBinding is the narrow integrity-checked thread header and
// selected case link carried by an ordinary case workbench. It neither
// enumerates sibling memberships nor loads sibling case payloads.
type PRDevelopmentThreadBinding struct {
	ID           string                      `json:"-"`
	Kind         PRDevelopmentThreadKind     `json:"-"`
	Identity     PRDevelopmentThreadIdentity `json:"-"`
	LegacyCaseID string                      `json:"-"`
	CaseCount    int                         `json:"-"`
	IdentityHash string                      `json:"-"`
	CasesDigest  string                      `json:"-"`
	Case         PRDevelopmentThreadCaseLink `json:"-"`
	CreatedAt    time.Time                   `json:"-"`
	UpdatedAt    time.Time                   `json:"-"`
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

// PRDevelopmentCaseListItem is one immutable capture plus the only mutable,
// browser-safe list hint. AttentionRequired is derived atomically by the
// SQLite list projection; no trigger identity, status, workflow run, or lease
// authority crosses this boundary.
type PRDevelopmentCaseListItem struct {
	PRDevelopmentCase
	AttentionRequired bool `json:"-"`
}

// PRDevelopmentCasePage is one stable keyset-paginated development-case
// result. Attention changes never affect its immutable capture ordering.
type PRDevelopmentCasePage struct {
	Cases []PRDevelopmentCaseListItem `json:"cases"`
	Next  *PRDevelopmentCaseCursor    `json:"next,omitempty"`
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

// PRDevelopmentLocalEvidenceSnapshot is the private, integrity-checked source
// used to project bounded local-development evidence for one workbench. It
// intentionally keeps the controller and full thread ledger outside JSON so
// browser DTOs cannot accidentally inherit retained-line identity, lease
// authority, findings, or sibling-case history.
type PRDevelopmentLocalEvidenceSnapshot struct {
	Controller    *PRDevelopmentController          `json:"-"`
	Orchestration *PRDevelopmentRepairOrchestration `json:"-"`
	Ledger        PRDevelopmentLedger               `json:"-"`
}

// PRDevelopmentWorkbench is one atomic read snapshot of the immutable case,
// bounded conversation, optional singleton repair session, and its optional
// private local-development evidence source.
type PRDevelopmentWorkbench struct {
	Case          PRDevelopmentCase                   `json:"case"`
	Thread        *PRDevelopmentThreadBinding         `json:"-"`
	Conversation  PRDevelopmentConversation           `json:"conversation"`
	RepairSession *PRDevelopmentRepairSession         `json:"repair_session,omitempty"`
	LocalEvidence *PRDevelopmentLocalEvidenceSnapshot `json:"-"`
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

// PRDevelopmentRepairOrchestrationPhase is the private lifecycle that keeps a
// public attempt queued until its validated candidate is atomically Parked.
// Editing is deliberately the only non-reclaimable phase because an expired
// model invocation may have changed the retained checkout.
type PRDevelopmentRepairOrchestrationPhase string

const (
	PRDevelopmentRepairOrchestrationBootstrap        PRDevelopmentRepairOrchestrationPhase = "bootstrap"
	PRDevelopmentRepairOrchestrationEditing          PRDevelopmentRepairOrchestrationPhase = "editing"
	PRDevelopmentRepairOrchestrationEdited           PRDevelopmentRepairOrchestrationPhase = "edited"
	PRDevelopmentRepairOrchestrationValidated        PRDevelopmentRepairOrchestrationPhase = "validated"
	PRDevelopmentRepairOrchestrationCompleted        PRDevelopmentRepairOrchestrationPhase = "completed"
	PRDevelopmentRepairOrchestrationFailed           PRDevelopmentRepairOrchestrationPhase = "failed"
	PRDevelopmentRepairOrchestrationRecoveryRequired PRDevelopmentRepairOrchestrationPhase = "recovery_required"
)

// PRDevelopmentCIStatus is the exact terminal result of a bounded local-CI
// execution with a valid persisted attestation. Only passed is green; every
// other status remains truthful durable evidence for later gates.
type PRDevelopmentCIStatus string

const (
	PRDevelopmentCIPassed                 PRDevelopmentCIStatus = "passed"
	PRDevelopmentCIFailed                 PRDevelopmentCIStatus = "failed"
	PRDevelopmentCIIncomplete             PRDevelopmentCIStatus = "incomplete"
	PRDevelopmentCIPlanChanged            PRDevelopmentCIStatus = "plan_changed"
	PRDevelopmentCITimedOut               PRDevelopmentCIStatus = "timed_out"
	PRDevelopmentCICanceled               PRDevelopmentCIStatus = "canceled"
	PRDevelopmentCIOutputLimitExceeded    PRDevelopmentCIStatus = "output_limit_exceeded"
	PRDevelopmentCIEnvironmentUnavailable PRDevelopmentCIStatus = "environment_unavailable"
	PRDevelopmentCIInfrastructureError    PRDevelopmentCIStatus = "infrastructure_error"
)

// PRDevelopmentRepairValidationReceipt is immutable, hash-bound evidence for
// the exact edited candidate and terminal local-CI execution accepted before
// Commit/Park. All fields are trusted-worker private.
type PRDevelopmentRepairValidationReceipt struct {
	ControllerID              string                `json:"-"`
	WorkspaceID               string                `json:"-"`
	ModelControllerRevision   int64                 `json:"-"`
	ModelLineID               string                `json:"-"`
	ModelLineVersion          int64                 `json:"-"`
	ModelMutationEpoch        int64                 `json:"-"`
	ModelMutationLeaseEpoch   int64                 `json:"-"`
	ModelLeaseTokenDigest     string                `json:"-"`
	ModelReservationDigest    string                `json:"-"`
	ContextDigest             string                `json:"-"`
	PromptDigest              string                `json:"-"`
	LineID                    string                `json:"-"`
	ControllerRevision        int64                 `json:"-"`
	LineVersion               int64                 `json:"-"`
	MutationEpoch             int64                 `json:"-"`
	MutationLeaseEpoch        int64                 `json:"-"`
	MutationLeaseTokenDigest  string                `json:"-"`
	MutationReservationDigest string                `json:"-"`
	ParentCommit              string                `json:"-"`
	ParentTree                string                `json:"-"`
	CandidateTree             string                `json:"-"`
	CandidateDigest           string                `json:"-"`
	ChangedFiles              int                   `json:"-"`
	NoChanges                 bool                  `json:"-"`
	CIStatus                  PRDevelopmentCIStatus `json:"-"`
	CIAttestationID           string                `json:"-"`
	CIAttestationDigest       string                `json:"-"`
	CIResultKey               string                `json:"-"`
	CIEffectivePlanDigest     string                `json:"-"`
	CIExecutionDigest         string                `json:"-"`
	ModelResultDigest         string                `json:"-"`
	ModelSummary              string                `json:"-"`
	ModelIterations           int                   `json:"-"`
	ReceiptHash               string                `json:"-"`
	CreatedAt                 time.Time             `json:"-"`
}

// PRDevelopmentRepairOrchestration is the durable trusted-worker checkpoint.
// Raw claim authority is omitted from JSON and is blanked by read-only Get;
// it is returned only by Claim and Renew rotates no authority.
type PRDevelopmentRepairOrchestration struct {
	AttemptID               string                                `json:"-"`
	SessionID               string                                `json:"-"`
	CaseID                  string                                `json:"-"`
	ThreadID                string                                `json:"-"`
	AgentID                 string                                `json:"-"`
	Instruction             string                                `json:"-"`
	Phase                   PRDevelopmentRepairOrchestrationPhase `json:"-"`
	ClaimOwner              string                                `json:"-"`
	ClaimToken              string                                `json:"-"`
	ClaimUntil              *time.Time                            `json:"-"`
	ClaimEpoch              int64                                 `json:"-"`
	Claims                  int                                   `json:"-"`
	HeadRepository          string                                `json:"-"`
	HeadRef                 string                                `json:"-"`
	HeadSHA                 string                                `json:"-"`
	CloneURL                string                                `json:"-"`
	ReviewDigest            string                                `json:"-"`
	WorkspaceID             string                                `json:"-"`
	SourceTree              string                                `json:"-"`
	ControllerID            string                                `json:"-"`
	ModelControllerRevision int64                                 `json:"-"`
	ModelLineID             string                                `json:"-"`
	ModelLineVersion        int64                                 `json:"-"`
	ModelMutationEpoch      int64                                 `json:"-"`
	ModelMutationLeaseEpoch int64                                 `json:"-"`
	ModelLeaseTokenDigest   string                                `json:"-"`
	ModelReservationDigest  string                                `json:"-"`
	ContextDigest           string                                `json:"-"`
	PromptDigest            string                                `json:"-"`
	ModelResultDigest       string                                `json:"-"`
	Summary                 string                                `json:"-"`
	Iterations              int                                   `json:"-"`
	Validation              *PRDevelopmentRepairValidationReceipt `json:"-"`
	ParkOperationID         string                                `json:"-"`
	LedgerEntryID           string                                `json:"-"`
	FenceHash               string                                `json:"-"`
	FailedClaimTokenDigest  string                                `json:"-"`
	CreatedAt               time.Time                             `json:"-"`
	ModelStartedAt          *time.Time                            `json:"-"`
	ModelCompletedAt        *time.Time                            `json:"-"`
	ValidatedAt             *time.Time                            `json:"-"`
	CompletedAt             *time.Time                            `json:"-"`
	FailedAt                *time.Time                            `json:"-"`
	RecoveryRequiredAt      *time.Time                            `json:"-"`
	UpdatedAt               time.Time                             `json:"-"`
}

// PRDevelopmentRepairOrchestrationClaim claims the oldest provider-thread
// queued attempt, or safely reclaims bootstrap/edited/validated work.
type PRDevelopmentRepairOrchestrationClaim struct {
	WorkerLabel string        `json:"-"`
	Lease       time.Duration `json:"-"`
}

// PRDevelopmentRepairOrchestrationRenew extends the exact live claim.
type PRDevelopmentRepairOrchestrationRenew struct {
	AttemptID  string        `json:"-"`
	ClaimToken string        `json:"-"`
	Lease      time.Duration `json:"-"`
}

// PRDevelopmentRepairOrchestrationPin atomically persists or exactly replays
// the provider pin plus freshly acquired workspace/source-tree baseline.
type PRDevelopmentRepairOrchestrationPin struct {
	AttemptID      string `json:"-"`
	ClaimToken     string `json:"-"`
	HeadRepository string `json:"-"`
	HeadRef        string `json:"-"`
	HeadSHA        string `json:"-"`
	CloneURL       string `json:"-"`
	ReviewDigest   string `json:"-"`
	WorkspaceID    string `json:"-"`
	SourceTree     string `json:"-"`
}

// PRDevelopmentRepairOrchestrationControllerAcquire is the narrow exception
// that lets the exact live orchestration claim create/acquire mutation control
// after it has already suppressed the legacy queue.
type PRDevelopmentRepairOrchestrationControllerAcquire struct {
	CaseID           string        `json:"-"`
	AttemptID        string        `json:"-"`
	ClaimToken       string        `json:"-"`
	ExpectedRevision int64         `json:"-"`
	WorkerLabel      string        `json:"-"`
	Lease            time.Duration `json:"-"`
}

// PRDevelopmentRepairOrchestrationModelStart binds the exact model context
// immediately before the potentially mutating edit invocation.
type PRDevelopmentRepairOrchestrationModelStart struct {
	AttemptID          string `json:"-"`
	ClaimToken         string `json:"-"`
	ControllerID       string `json:"-"`
	ControllerRevision int64  `json:"-"`
	MutationLeaseToken string `json:"-"`
	MutationLeaseEpoch int64  `json:"-"`
	ContextDigest      string `json:"-"`
	PromptDigest       string `json:"-"`
}

// PRDevelopmentRepairOrchestrationModelComplete stores the bounded model
// outcome under the same exact live claim and controller lease.
type PRDevelopmentRepairOrchestrationModelComplete struct {
	AttemptID          string `json:"-"`
	ClaimToken         string `json:"-"`
	ControllerID       string `json:"-"`
	ControllerRevision int64  `json:"-"`
	MutationLeaseToken string `json:"-"`
	MutationLeaseEpoch int64  `json:"-"`
	ModelResultDigest  string `json:"-"`
	Summary            string `json:"-"`
	Iterations         int    `json:"-"`
}

// PRDevelopmentRepairOrchestrationValidation stores an immutable local-CI
// receipt. Lease and reservation digests are derived from the exact current
// controller instead of accepted from the caller.
type PRDevelopmentRepairOrchestrationValidation struct {
	AttemptID             string                `json:"-"`
	ClaimToken            string                `json:"-"`
	ControllerID          string                `json:"-"`
	ControllerRevision    int64                 `json:"-"`
	MutationLeaseToken    string                `json:"-"`
	MutationLeaseEpoch    int64                 `json:"-"`
	ParentCommit          string                `json:"-"`
	ParentTree            string                `json:"-"`
	CandidateTree         string                `json:"-"`
	CandidateDigest       string                `json:"-"`
	ChangedFiles          int                   `json:"-"`
	NoChanges             bool                  `json:"-"`
	CIStatus              PRDevelopmentCIStatus `json:"-"`
	CIAttestationID       string                `json:"-"`
	CIAttestationDigest   string                `json:"-"`
	CIResultKey           string                `json:"-"`
	CIEffectivePlanDigest string                `json:"-"`
	CIExecutionDigest     string                `json:"-"`
}

// PRDevelopmentRepairOrchestrationFail safely terminalizes an unpinned
// bootstrap before this attempt acquires mutation/model authority. A retained
// Ready controller from an earlier attempt remains reserved and suppressed.
type PRDevelopmentRepairOrchestrationFail struct {
	AttemptID     string                       `json:"-"`
	ClaimToken    string                       `json:"-"`
	Summary       string                       `json:"-"`
	ErrorCode     PRDevelopmentRepairErrorCode `json:"-"`
	InternalError string                       `json:"-"`
}

// PRDevelopmentControllerPhase is the private lifecycle of the one retained
// development line owned by a provider-verified pull-request thread. A stable
// owner is not itself a lease: idle, review-pending, ready, and recovery states
// retain the line without granting a worker mutation authority.
type PRDevelopmentControllerPhase string

const (
	PRDevelopmentControllerIdle              PRDevelopmentControllerPhase = "idle"
	PRDevelopmentControllerMutation          PRDevelopmentControllerPhase = "mutation"
	PRDevelopmentControllerReviewPending     PRDevelopmentControllerPhase = "review_pending"
	PRDevelopmentControllerReview            PRDevelopmentControllerPhase = "review"
	PRDevelopmentControllerReady             PRDevelopmentControllerPhase = "ready"
	PRDevelopmentControllerRecoveryRequired  PRDevelopmentControllerPhase = "recovery_required"
	PRDevelopmentControllerSuspensionPending PRDevelopmentControllerPhase = "suspension_pending"
	PRDevelopmentControllerSuspended         PRDevelopmentControllerPhase = "suspended"
)

// PRDevelopmentControllerLeaseKind separates an exclusive filesystem
// mutation lease from a reservation-free immutable-review lease.
type PRDevelopmentControllerLeaseKind string

const (
	PRDevelopmentControllerMutationLease PRDevelopmentControllerLeaseKind = "mutation"
	PRDevelopmentControllerReviewLease   PRDevelopmentControllerLeaseKind = "review"
)

// PRDevelopmentController is controller-private durable state. Every field is
// deliberately excluded from JSON so reusing this type cannot expose provider
// pins, retained-line identity, worker credentials, or the mutation bearer in
// a browser projection.
type PRDevelopmentController struct {
	ID                     string                           `json:"-"`
	ThreadID               string                           `json:"-"`
	OwnerSessionID         string                           `json:"-"`
	AgentID                string                           `json:"-"`
	Revision               int64                            `json:"-"`
	Phase                  PRDevelopmentControllerPhase     `json:"-"`
	LineID                 string                           `json:"-"`
	WorkspaceID            string                           `json:"-"`
	SourceCloneURL         string                           `json:"-"`
	SourceRef              string                           `json:"-"`
	SourceCommit           string                           `json:"-"`
	SourceTree             string                           `json:"-"`
	LineVersion            int64                            `json:"-"`
	MutationEpoch          int64                            `json:"-"`
	TipCommit              string                           `json:"-"`
	Tree                   string                           `json:"-"`
	CurrentAttemptID       string                           `json:"-"`
	LeaseKind              PRDevelopmentControllerLeaseKind `json:"-"`
	LeaseOwner             string                           `json:"-"`
	LeaseToken             string                           `json:"-"`
	LeaseUntil             *time.Time                       `json:"-"`
	LeaseEpoch             int64                            `json:"-"`
	Claims                 int                              `json:"-"`
	MutationReservationKey string                           `json:"-"`
	FenceCount             int                              `json:"-"`
	FencesDigest           string                           `json:"-"`
	CreatedAt              time.Time                        `json:"-"`
	UpdatedAt              time.Time                        `json:"-"`
}

// PRDevelopmentAttemptReviewFence is one immutable, ordered parked-line
// projection. It binds the reservation-free local review digest to the exact
// line version, park epoch/intent, base, tip, and tree for one repair attempt.
type PRDevelopmentAttemptReviewFence struct {
	AttemptID        string `json:"-"`
	ControllerID     string `json:"-"`
	ThreadID         string `json:"-"`
	LineID           string `json:"-"`
	Ordinal          int    `json:"-"`
	LineVersion      int64  `json:"-"`
	MutationEpoch    int64  `json:"-"`
	ParkIntentID     string `json:"-"`
	BaseCommit       string `json:"-"`
	TipCommit        string `json:"-"`
	Tree             string `json:"-"`
	NoChanges        bool   `json:"-"`
	LineReviewDigest string `json:"-"`
	// MutationReservationDigest retains non-authorizing evidence that the raw
	// filesystem bearer retired by this fence is never issued again.
	MutationReservationDigest  string     `json:"-"`
	MutationLeaseEpoch         int64      `json:"-"`
	MutationLeaseTokenDigest   string     `json:"-"`
	MutationControllerRevision int64      `json:"-"`
	ReviewLeaseEpoch           int64      `json:"-"`
	ReviewLeaseTokenDigest     string     `json:"-"`
	ReviewControllerRevision   int64      `json:"-"`
	PreviousHash               string     `json:"-"`
	FenceHash                  string     `json:"-"`
	CreatedAt                  time.Time  `json:"-"`
	ReviewedAt                 *time.Time `json:"-"`
}

// PRDevelopmentControllerAcquire claims the selected case's provider thread
// for the latest attempt in its immutable owner session. ExpectedRevision is
// zero only when creating the controller and its stable line identity.
type PRDevelopmentControllerAcquire struct {
	CaseID           string                           `json:"-"`
	AttemptID        string                           `json:"-"`
	ExpectedRevision int64                            `json:"-"`
	Kind             PRDevelopmentControllerLeaseKind `json:"-"`
	WorkerLabel      string                           `json:"-"`
	Lease            time.Duration                    `json:"-"`
}

// PRDevelopmentControllerLease returns the complete private controller and,
// for review ownership, the exact pending immutable fence. MutationReservationKey
// is populated only for a mutation lease.
type PRDevelopmentControllerLease struct {
	Controller      PRDevelopmentController                      `json:"-"`
	ReviewFence     *PRDevelopmentAttemptReviewFence             `json:"-"`
	SuspendedResume *PRDevelopmentControllerSuspendedResumeLease `json:"-"`
	Created         bool                                         `json:"-"`
	Reclaimed       bool                                         `json:"-"`
}

// PRDevelopmentControllerRenew extends only the exact live lease epoch/token.
type PRDevelopmentControllerRenew struct {
	ControllerID string        `json:"-"`
	AttemptID    string        `json:"-"`
	LeaseToken   string        `json:"-"`
	LeaseEpoch   int64         `json:"-"`
	Lease        time.Duration `json:"-"`
}

// PRDevelopmentControllerLineBind records the exact AdoptPinnedLine or
// ResumePinnedLine result under a live mutation lease. Source fields become an
// immutable all-or-none binding on first adoption.
type PRDevelopmentControllerLineBind struct {
	ControllerID     string `json:"-"`
	AttemptID        string `json:"-"`
	ExpectedRevision int64  `json:"-"`
	LeaseToken       string `json:"-"`
	LeaseEpoch       int64  `json:"-"`
	WorkspaceID      string `json:"-"`
	SourceCloneURL   string `json:"-"`
	SourceRef        string `json:"-"`
	SourceCommit     string `json:"-"`
	SourceTree       string `json:"-"`
	LineVersion      int64  `json:"-"`
	MutationEpoch    int64  `json:"-"`
	TipCommit        string `json:"-"`
	Tree             string `json:"-"`
}

// PRDevelopmentAttemptReviewFenceRecord atomically records the exact parked
// review snapshot, retires the controller's raw mutation bearer, and releases
// its mutation lease into review_pending.
type PRDevelopmentAttemptReviewFenceRecord struct {
	ControllerID     string `json:"-"`
	AttemptID        string `json:"-"`
	ExpectedRevision int64  `json:"-"`
	LeaseToken       string `json:"-"`
	LeaseEpoch       int64  `json:"-"`
	LineVersion      int64  `json:"-"`
	MutationEpoch    int64  `json:"-"`
	ParkIntentID     string `json:"-"`
	BaseCommit       string `json:"-"`
	TipCommit        string `json:"-"`
	Tree             string `json:"-"`
	NoChanges        bool   `json:"-"`
	LineReviewDigest string `json:"-"`
}

// PRDevelopmentControllerReviewTransition completes a live immutable review
// or releases it back to review_pending without granting mutation authority.
type PRDevelopmentControllerReviewTransition struct {
	ControllerID     string `json:"-"`
	AttemptID        string `json:"-"`
	ExpectedRevision int64  `json:"-"`
	LeaseToken       string `json:"-"`
	LeaseEpoch       int64  `json:"-"`
}

// PRDevelopmentReviewClaimRequest leases at most one oldest eligible parked
// candidate for reservation-free immutable review.
type PRDevelopmentReviewClaimRequest struct {
	WorkerLabel string        `json:"-"`
	Lease       time.Duration `json:"-"`
}

// PRDevelopmentReviewLease binds one background claim to its owning case,
// exact controller lease, and immutable parked fence. It never carries a
// mutation reservation.
type PRDevelopmentReviewLease struct {
	CaseID     string                          `json:"-"`
	Controller PRDevelopmentController         `json:"-"`
	Fence      PRDevelopmentAttemptReviewFence `json:"-"`
	Reclaimed  bool                            `json:"-"`
}

// PRDevelopmentControllerRecoveryMode records whether eventing had no durable
// line binding or had a retained line at its active mutation fence when the
// lease expired. Unbound does not prove Git adoption has not already happened.
type PRDevelopmentControllerRecoveryMode string

const (
	PRDevelopmentControllerRecoveryUnbound PRDevelopmentControllerRecoveryMode = "unbound"
	PRDevelopmentControllerRecoveryBound   PRDevelopmentControllerRecoveryMode = "bound"
)

// PRDevelopmentControllerRecoveryStatus is the durable, exactly replayable
// reservation-rotation lifecycle. Rotation is the only filesystem effect;
// the eventing store only records and fences its result.
type PRDevelopmentControllerRecoveryStatus string

const (
	PRDevelopmentControllerRecoveryPending   PRDevelopmentControllerRecoveryStatus = "pending"
	PRDevelopmentControllerRecoveryClaimed   PRDevelopmentControllerRecoveryStatus = "claimed"
	PRDevelopmentControllerRecoveryFinalized PRDevelopmentControllerRecoveryStatus = "finalized"
)

// PRDevelopmentControllerRecoveryIntent is the private hash-chained evidence
// created atomically when an eventing-recoverable mutation lease expires.
// Recoverable means eventing-unbound or bound at its active mutation epoch.
// Raw reservation bearers exist only while recovery is unresolved and are
// erased when it finalizes.
type PRDevelopmentControllerRecoveryIntent struct {
	ID                           string                                `json:"-"`
	ControllerID                 string                                `json:"-"`
	AttemptID                    string                                `json:"-"`
	Ordinal                      int                                   `json:"-"`
	RecoveryRevision             int64                                 `json:"-"`
	Mode                         PRDevelopmentControllerRecoveryMode   `json:"-"`
	Status                       PRDevelopmentControllerRecoveryStatus `json:"-"`
	AgentID                      string                                `json:"-"`
	WorkspaceID                  string                                `json:"-"`
	LineID                       string                                `json:"-"`
	SourceCloneURL               string                                `json:"-"`
	SourceRef                    string                                `json:"-"`
	SourceCommit                 string                                `json:"-"`
	SourceTree                   string                                `json:"-"`
	LineVersion                  int64                                 `json:"-"`
	MutationEpoch                int64                                 `json:"-"`
	TipCommit                    string                                `json:"-"`
	Tree                         string                                `json:"-"`
	PreviousReservationKey       string                                `json:"-"`
	ReplacementReservationKey    string                                `json:"-"`
	PreviousReservationDigest    string                                `json:"-"`
	ReplacementReservationDigest string                                `json:"-"`
	ExpiredControllerRevision    int64                                 `json:"-"`
	ExpiredLeaseEpoch            int64                                 `json:"-"`
	ExpiredLeaseTokenDigest      string                                `json:"-"`
	PreviousHash                 string                                `json:"-"`
	IntentHash                   string                                `json:"-"`
	ClaimID                      string                                `json:"-"`
	ClaimOwner                   string                                `json:"-"`
	ClaimToken                   string                                `json:"-"`
	ClaimUntil                   *time.Time                            `json:"-"`
	ClaimEpoch                   int64                                 `json:"-"`
	Claims                       int                                   `json:"-"`
	RotationResultHash           string                                `json:"-"`
	RecoveryClaimTokenDigest     string                                `json:"-"`
	NewMutationLeaseEpoch        int64                                 `json:"-"`
	NewMutationLeaseTokenDigest  string                                `json:"-"`
	NewMutationLeaseUntil        *time.Time                            `json:"-"`
	FinalRevision                int64                                 `json:"-"`
	FinalHash                    string                                `json:"-"`
	CreatedAt                    time.Time                             `json:"-"`
	ClaimedAt                    *time.Time                            `json:"-"`
	FinalizedAt                  *time.Time                            `json:"-"`
	UpdatedAt                    time.Time                             `json:"-"`
}

// PRDevelopmentControllerRecoveryClaim acquires one exact recovery intent.
// ClaimID is caller-durable so a lost successful response can be replayed
// without rotating the recovery claim token.
type PRDevelopmentControllerRecoveryClaim struct {
	CaseID           string        `json:"-"`
	AttemptID        string        `json:"-"`
	ExpectedRevision int64         `json:"-"`
	ClaimID          string        `json:"-"`
	WorkerLabel      string        `json:"-"`
	Lease            time.Duration `json:"-"`
}

// PRDevelopmentControllerRecoveryLease carries both raw reservation bearers
// and the recovery-claim credential. It must remain inside the trusted local
// controller/worker boundary. Intent.AgentID is the unchanged Git ownership
// identity on both sides of rotation; Intent.ClaimOwner is only the eventing
// worker lease owner and must never replace it.
type PRDevelopmentControllerRecoveryLease struct {
	Controller PRDevelopmentController               `json:"-"`
	Intent     PRDevelopmentControllerRecoveryIntent `json:"-"`
	Reclaimed  bool                                  `json:"-"`
}

// PRDevelopmentControllerRecoveryRenew extends only the exact live recovery
// claim. It never changes controller revision or mutation authority.
type PRDevelopmentControllerRecoveryRenew struct {
	ControllerID string        `json:"-"`
	AttemptID    string        `json:"-"`
	RecoveryID   string        `json:"-"`
	ClaimID      string        `json:"-"`
	ClaimToken   string        `json:"-"`
	ClaimEpoch   int64         `json:"-"`
	Lease        time.Duration `json:"-"`
}

// PRDevelopmentControllerRecoveryRotationResult mirrors the non-authorizing
// result of gitworkspace.Manager.RotatePinnedReservation. AlreadyRotated is
// operational information only; the other fields form the durable result
// fence and are identical for first execution and exact replay.
type PRDevelopmentControllerRecoveryRotationResult struct {
	WorkspaceID    string `json:"-"`
	Bound          bool   `json:"-"`
	Version        int64  `json:"-"`
	MutationEpoch  int64  `json:"-"`
	Tip            string `json:"-"`
	Tree           string `json:"-"`
	RotationHash   string `json:"-"`
	AlreadyRotated bool   `json:"-"`
}

// PRDevelopmentControllerRecoveryFinalize consumes the exact live recovery
// claim and matching reservation-rotation result. Legacy unbound recovery
// grants a fresh mutation lease; bound recovery transfers that fresh authority
// into the durable suspension lifecycle before returning.
type PRDevelopmentControllerRecoveryFinalize struct {
	ControllerID     string                                        `json:"-"`
	AttemptID        string                                        `json:"-"`
	RecoveryID       string                                        `json:"-"`
	ExpectedRevision int64                                         `json:"-"`
	ClaimID          string                                        `json:"-"`
	ClaimToken       string                                        `json:"-"`
	ClaimEpoch       int64                                         `json:"-"`
	Rotation         PRDevelopmentControllerRecoveryRotationResult `json:"-"`
	Lease            time.Duration                                 `json:"-"`
}

// PRDevelopmentControllerSuspensionSourceKind identifies the already-final
// recovery record that caused a retained, bound line to relinquish mutation
// authority. Legacy unbound controller recovery can never form this source.
type PRDevelopmentControllerSuspensionSourceKind string

const (
	PRDevelopmentControllerSuspensionSourceControllerRecovery      PRDevelopmentControllerSuspensionSourceKind = "controller_recovery"
	PRDevelopmentControllerSuspensionSourceOperationRecovery       PRDevelopmentControllerSuspensionSourceKind = "operation_recovery"
	PRDevelopmentControllerSuspensionSourceSuspendedResumeRecovery PRDevelopmentControllerSuspensionSourceKind = "suspended_resume_recovery"
)

// PRDevelopmentControllerSuspensionMode selects the deterministic Git
// reconciliation needed before a retained line becomes reservation-free.
type PRDevelopmentControllerSuspensionMode string

const (
	PRDevelopmentControllerSuspensionCandidate      PRDevelopmentControllerSuspensionMode = "candidate"
	PRDevelopmentControllerSuspensionCommitRecovery PRDevelopmentControllerSuspensionMode = "commit_recovery"
)

// PRDevelopmentControllerSuspensionStatus is one append-only suspension and
// later explicit-resume lifecycle. A suspended row remains active until its
// exact retained candidate has been resumed under a fresh reservation.
type PRDevelopmentControllerSuspensionStatus string

const (
	PRDevelopmentControllerSuspensionStatusSuspendPending PRDevelopmentControllerSuspensionStatus = "suspend_pending"
	PRDevelopmentControllerSuspensionStatusSuspendClaimed PRDevelopmentControllerSuspensionStatus = "suspend_claimed"
	PRDevelopmentControllerSuspensionStatusSuspended      PRDevelopmentControllerSuspensionStatus = "suspended"
	PRDevelopmentControllerSuspensionStatusResumePending  PRDevelopmentControllerSuspensionStatus = "resume_pending"
	PRDevelopmentControllerSuspensionStatusResumeClaimed  PRDevelopmentControllerSuspensionStatus = "resume_claimed"
	PRDevelopmentControllerSuspensionStatusResumed        PRDevelopmentControllerSuspensionStatus = "resumed"
)

// PRDevelopmentControllerSuspensionRequest is the exact private input to
// SuspendPinnedLine or, for commit recovery, SuspendPinnedLineCommitRecovery.
type PRDevelopmentControllerSuspensionRequest struct {
	Repository            string    `json:"-"`
	SourceRef             string    `json:"-"`
	SourceCommit          string    `json:"-"`
	ReservationKey        string    `json:"-"`
	AgentID               string    `json:"-"`
	WorkspaceID           string    `json:"-"`
	LineID                string    `json:"-"`
	IntentID              string    `json:"-"`
	ExpectedVersion       int64     `json:"-"`
	ExpectedMutationEpoch int64     `json:"-"`
	ExpectedTip           string    `json:"-"`
	ExpectedTree          string    `json:"-"`
	CommitIntentID        string    `json:"-"`
	CommitExpectedParent  string    `json:"-"`
	CommitExpectedTree    string    `json:"-"`
	CommitCandidateDigest string    `json:"-"`
	CommitMessage         string    `json:"-"`
	CommitAuthoredAt      time.Time `json:"-"`
}

// PRDevelopmentControllerSuspensionResult is content-addressed evidence from
// Git after the retained candidate has become reservation-free.
type PRDevelopmentControllerSuspensionResult struct {
	WorkspaceID           string `json:"-"`
	Version               int64  `json:"-"`
	MutationEpoch         int64  `json:"-"`
	Tip                   string `json:"-"`
	Tree                  string `json:"-"`
	CandidateTree         string `json:"-"`
	CandidateDigest       string `json:"-"`
	ChangedFileCount      int    `json:"-"`
	SuspensionHash        string `json:"-"`
	PreparedCommit        string `json:"-"`
	PreparedTree          string `json:"-"`
	PreparedCommitApplied bool   `json:"-"`
	AlreadySuspended      bool   `json:"-"`
}

// PRDevelopmentControllerSuspendedResumeRequest is the exact private input
// that restores one suspended candidate under a globally fresh reservation.
type PRDevelopmentControllerSuspendedResumeRequest struct {
	Repository            string `json:"-"`
	SourceRef             string `json:"-"`
	SourceCommit          string `json:"-"`
	ReservationKey        string `json:"-"`
	AgentID               string `json:"-"`
	WorkspaceID           string `json:"-"`
	LineID                string `json:"-"`
	IntentID              string `json:"-"`
	ExpectedVersion       int64  `json:"-"`
	ExpectedMutationEpoch int64  `json:"-"`
	ExpectedTip           string `json:"-"`
	ExpectedTree          string `json:"-"`
	SuspensionHash        string `json:"-"`
	CandidateTree         string `json:"-"`
	CandidateDigest       string `json:"-"`
	ChangedFileCount      int    `json:"-"`
}

// PRDevelopmentControllerSuspendedResumeResult binds restored mutation
// ownership to both the immutable suspension and fresh reservation rotation.
type PRDevelopmentControllerSuspendedResumeResult struct {
	WorkspaceID      string `json:"-"`
	Version          int64  `json:"-"`
	MutationEpoch    int64  `json:"-"`
	Tip              string `json:"-"`
	Tree             string `json:"-"`
	CandidateTree    string `json:"-"`
	CandidateDigest  string `json:"-"`
	ChangedFileCount int    `json:"-"`
	SuspensionHash   string `json:"-"`
	RotationHash     string `json:"-"`
	AlreadyResumed   bool   `json:"-"`
}

// PRDevelopmentControllerSuspension is private append-only evidence for one
// recovery-triggered suspension and a possible later explicit resume. Raw
// reservation and claim bearers exist only in their unfinished states.
type PRDevelopmentControllerSuspension struct {
	ID                          string                                        `json:"-"`
	ControllerID                string                                        `json:"-"`
	ThreadID                    string                                        `json:"-"`
	OwnerSessionID              string                                        `json:"-"`
	AttemptID                   string                                        `json:"-"`
	Ordinal                     int                                           `json:"-"`
	SourceKind                  PRDevelopmentControllerSuspensionSourceKind   `json:"-"`
	SourceRecoveryID            string                                        `json:"-"`
	SourceOperationID           string                                        `json:"-"`
	SourceOperationKind         PRDevelopmentControllerOperationKind          `json:"-"`
	SourceFinalRevision         int64                                         `json:"-"`
	SourceFinalHash             string                                        `json:"-"`
	Mode                        PRDevelopmentControllerSuspensionMode         `json:"-"`
	Status                      PRDevelopmentControllerSuspensionStatus       `json:"-"`
	AgentID                     string                                        `json:"-"`
	WorkspaceID                 string                                        `json:"-"`
	LineID                      string                                        `json:"-"`
	SourceCloneURL              string                                        `json:"-"`
	SourceRef                   string                                        `json:"-"`
	SourceCommit                string                                        `json:"-"`
	SourceTree                  string                                        `json:"-"`
	LineVersion                 int64                                         `json:"-"`
	MutationEpoch               int64                                         `json:"-"`
	TipCommit                   string                                        `json:"-"`
	Tree                        string                                        `json:"-"`
	SuspensionReservationKey    string                                        `json:"-"`
	SuspensionReservationDigest string                                        `json:"-"`
	MutationLeaseEpoch          int64                                         `json:"-"`
	MutationLeaseTokenDigest    string                                        `json:"-"`
	SuspendIntentID             string                                        `json:"-"`
	SuspendRequest              PRDevelopmentControllerSuspensionRequest      `json:"-"`
	SuspendRequestJSON          []byte                                        `json:"-"`
	SuspendRequestHash          string                                        `json:"-"`
	PreviousHash                string                                        `json:"-"`
	IntentHash                  string                                        `json:"-"`
	SuspendClaimID              string                                        `json:"-"`
	SuspendClaimOwner           string                                        `json:"-"`
	SuspendClaimToken           string                                        `json:"-"`
	SuspendClaimUntil           *time.Time                                    `json:"-"`
	SuspendClaimEpoch           int64                                         `json:"-"`
	SuspendClaims               int                                           `json:"-"`
	SuspendClaimedAt            *time.Time                                    `json:"-"`
	SuspendClaimTokenDigest     string                                        `json:"-"`
	SuspendResult               PRDevelopmentControllerSuspensionResult       `json:"-"`
	SuspendResultJSON           []byte                                        `json:"-"`
	SuspendResultHash           string                                        `json:"-"`
	FinalSuspensionRevision     int64                                         `json:"-"`
	SuspensionFinalHash         string                                        `json:"-"`
	SuspendedAt                 *time.Time                                    `json:"-"`
	ResumeAttemptID             string                                        `json:"-"`
	ResumeIntentID              string                                        `json:"-"`
	ResumeReservationKey        string                                        `json:"-"`
	ResumeReservationDigest     string                                        `json:"-"`
	ResumeRequest               PRDevelopmentControllerSuspendedResumeRequest `json:"-"`
	ResumeRequestJSON           []byte                                        `json:"-"`
	ResumeRequestHash           string                                        `json:"-"`
	ResumeIntentHash            string                                        `json:"-"`
	ResumePreparedAt            *time.Time                                    `json:"-"`
	ResumeClaimID               string                                        `json:"-"`
	ResumeClaimOwner            string                                        `json:"-"`
	ResumeClaimToken            string                                        `json:"-"`
	ResumeClaimUntil            *time.Time                                    `json:"-"`
	ResumeClaimEpoch            int64                                         `json:"-"`
	ResumeClaims                int                                           `json:"-"`
	ResumeClaimedAt             *time.Time                                    `json:"-"`
	ResumeClaimTokenDigest      string                                        `json:"-"`
	ResumeResult                PRDevelopmentControllerSuspendedResumeResult  `json:"-"`
	ResumeResultJSON            []byte                                        `json:"-"`
	ResumeResultHash            string                                        `json:"-"`
	NewMutationLeaseEpoch       int64                                         `json:"-"`
	NewMutationLeaseTokenDigest string                                        `json:"-"`
	NewMutationLeaseUntil       *time.Time                                    `json:"-"`
	FinalResumeRevision         int64                                         `json:"-"`
	ResumeFinalHash             string                                        `json:"-"`
	ResumedAt                   *time.Time                                    `json:"-"`
	CreatedAt                   time.Time                                     `json:"-"`
	UpdatedAt                   time.Time                                     `json:"-"`
}

// PRDevelopmentControllerOperationKind identifies the exact local Git effect
// fenced by one durable operation. Adopt and Resume bind a line, Commit
// materializes one validated candidate, and Park releases mutation authority
// while recording the immutable review fence and completed attempt.
type PRDevelopmentControllerOperationKind string

const (
	PRDevelopmentControllerOperationAdopt  PRDevelopmentControllerOperationKind = "adopt"
	PRDevelopmentControllerOperationResume PRDevelopmentControllerOperationKind = "resume"
	PRDevelopmentControllerOperationCommit PRDevelopmentControllerOperationKind = "commit"
	PRDevelopmentControllerOperationPark   PRDevelopmentControllerOperationKind = "park"
)

// PRDevelopmentControllerOperationStatus is the operation-owned recovery
// lifecycle. An expired operation never manufactures a pending legacy
// controller-recovery row: its own recovery_pending/recovery_claimed states
// retain the exact effect and reservation-rotation evidence until finalization.
type PRDevelopmentControllerOperationStatus string

const (
	PRDevelopmentControllerOperationPending         PRDevelopmentControllerOperationStatus = "pending"
	PRDevelopmentControllerOperationRecoveryPending PRDevelopmentControllerOperationStatus = "recovery_pending"
	PRDevelopmentControllerOperationRecoveryClaimed PRDevelopmentControllerOperationStatus = "recovery_claimed"
	PRDevelopmentControllerOperationFinalized       PRDevelopmentControllerOperationStatus = "finalized"
)

// PRDevelopmentControllerOperationRequest is a private, flat union of the
// exact gitworkspace AdoptPinnedLine, ResumePinnedLine, CommitPinned, and
// ParkPinnedLine requests. Fields unused by Kind remain zero. Park also carries
// the bounded attempt completion that is committed atomically with its review
// fence; the raw origin mutation reservation remains only on the controller.
type PRDevelopmentControllerOperationRequest struct {
	Repository           string    `json:"-"`
	SourceRef            string    `json:"-"`
	SourceCommit         string    `json:"-"`
	AgentID              string    `json:"-"`
	WorkspaceID          string    `json:"-"`
	LineID               string    `json:"-"`
	ExpectedTree         string    `json:"-"`
	ExpectedVersion      int64     `json:"-"`
	ExpectedEpoch        int64     `json:"-"`
	ExpectedTip          string    `json:"-"`
	EffectIntentID       string    `json:"-"`
	ExpectedParent       string    `json:"-"`
	CandidateDigest      string    `json:"-"`
	CommitMessage        string    `json:"-"`
	AuthoredAt           time.Time `json:"-"`
	MutationEpoch        int64     `json:"-"`
	PreviousTip          string    `json:"-"`
	Tip                  string    `json:"-"`
	Tree                 string    `json:"-"`
	NoChanges            bool      `json:"-"`
	CompletionSummary    string    `json:"-"`
	CompletionIterations int       `json:"-"`
}

// PRDevelopmentControllerOperationResult is the private, flat durable result
// union. Replay booleans are operational only; content-addressed fields must be
// identical for first execution and exact replay. A Park result carries only
// bounded review identity and digest evidence. Changed paths and diff text are
// intentionally re-snapshotted by the later reservation-free review lease.
type PRDevelopmentControllerOperationResult struct {
	WorkspaceID    string `json:"-"`
	Version        int64  `json:"-"`
	MutationEpoch  int64  `json:"-"`
	PreviousTip    string `json:"-"`
	Tip            string `json:"-"`
	Tree           string `json:"-"`
	NoChanges      bool   `json:"-"`
	WorkspaceClean bool   `json:"-"`
	AlreadyOwned   bool   `json:"-"`
	AlreadyApplied bool   `json:"-"`
	AlreadyParked  bool   `json:"-"`

	IntentID        string `json:"-"`
	ParentCommit    string `json:"-"`
	CandidateDigest string `json:"-"`
	Commit          string `json:"-"`
	ChangedFiles    int    `json:"-"`

	ReviewVersion       int64  `json:"-"`
	ReviewMutationEpoch int64  `json:"-"`
	ReviewParkIntentID  string `json:"-"`
	ReviewBaseCommit    string `json:"-"`
	ReviewCommit        string `json:"-"`
	ReviewTree          string `json:"-"`
	ReviewDigest        string `json:"-"`
}

// PRDevelopmentControllerOperation is private append-only effect evidence.
// The controller remains authoritative for the origin mutation bearer; the
// operation retains only its digest. A raw replacement bearer is available
// only while recovery is unresolved and is erased on finalization.
type PRDevelopmentControllerOperation struct {
	ID                         string                                  `json:"-"`
	ControllerID               string                                  `json:"-"`
	AttemptID                  string                                  `json:"-"`
	Ordinal                    int                                     `json:"-"`
	Kind                       PRDevelopmentControllerOperationKind    `json:"-"`
	Status                     PRDevelopmentControllerOperationStatus  `json:"-"`
	PreparedControllerRevision int64                                   `json:"-"`
	AgentID                    string                                  `json:"-"`
	WorkspaceID                string                                  `json:"-"`
	LineID                     string                                  `json:"-"`
	SourceCloneURL             string                                  `json:"-"`
	SourceRef                  string                                  `json:"-"`
	SourceCommit               string                                  `json:"-"`
	SourceTree                 string                                  `json:"-"`
	LineVersion                int64                                   `json:"-"`
	MutationEpoch              int64                                   `json:"-"`
	TipCommit                  string                                  `json:"-"`
	Tree                       string                                  `json:"-"`
	MutationReservationDigest  string                                  `json:"-"`
	MutationLeaseEpoch         int64                                   `json:"-"`
	MutationLeaseTokenDigest   string                                  `json:"-"`
	EffectIntentID             string                                  `json:"-"`
	Request                    PRDevelopmentControllerOperationRequest `json:"-"`
	RequestJSON                []byte                                  `json:"-"`
	RequestHash                string                                  `json:"-"`
	PreviousHash               string                                  `json:"-"`
	IntentHash                 string                                  `json:"-"`

	RecoveryID                   string     `json:"-"`
	ReplacementReservationKey    string     `json:"-"`
	ReplacementReservationDigest string     `json:"-"`
	RecoveryRevision             int64      `json:"-"`
	ExpiredControllerRevision    int64      `json:"-"`
	ExpiredLeaseEpoch            int64      `json:"-"`
	ExpiredLeaseTokenDigest      string     `json:"-"`
	RecoveryLeaseUntil           *time.Time `json:"-"`
	RecoveryStagedAt             *time.Time `json:"-"`
	RecoveryHash                 string     `json:"-"`
	ClaimID                      string     `json:"-"`
	ClaimOwner                   string     `json:"-"`
	ClaimToken                   string     `json:"-"`
	ClaimUntil                   *time.Time `json:"-"`
	ClaimEpoch                   int64      `json:"-"`
	Claims                       int        `json:"-"`
	ClaimedAt                    *time.Time `json:"-"`
	RotationResultHash           string     `json:"-"`
	RecoveryClaimTokenDigest     string     `json:"-"`
	NewMutationLeaseEpoch        int64      `json:"-"`
	NewMutationLeaseTokenDigest  string     `json:"-"`
	NewMutationLeaseUntil        *time.Time `json:"-"`

	Result                   PRDevelopmentControllerOperationResult `json:"-"`
	ResultJSON               []byte                                 `json:"-"`
	ResultHash               string                                 `json:"-"`
	StageAuthorizationDigest string                                 `json:"-"`
	FinalControllerRevision  int64                                  `json:"-"`
	FinalControllerPhase     PRDevelopmentControllerPhase           `json:"-"`
	FinalFenceHash           string                                 `json:"-"`
	FinalHash                string                                 `json:"-"`
	CreatedAt                time.Time                              `json:"-"`
	FinalizedAt              *time.Time                             `json:"-"`
	UpdatedAt                time.Time                              `json:"-"`
}

// PRDevelopmentControllerOperationTransition returns the complete private
// controller authority resulting from finalization together with the durable
// operation and, for Park, its immutable review fence.
type PRDevelopmentControllerOperationTransition struct {
	Controller PRDevelopmentController          `json:"-"`
	Operation  PRDevelopmentControllerOperation `json:"-"`
	Fence      *PRDevelopmentAttemptReviewFence `json:"-"`
}

// PRDevelopmentControllerOperationPrepare durably stages one exact Git effect
// under the current live mutation lease. OperationID is caller-supplied so a
// lost successful response can be replayed without changing effect identity.
type PRDevelopmentControllerOperationPrepare struct {
	OperationID      string                                  `json:"-"`
	ControllerID     string                                  `json:"-"`
	AttemptID        string                                  `json:"-"`
	ExpectedRevision int64                                   `json:"-"`
	LeaseToken       string                                  `json:"-"`
	LeaseEpoch       int64                                   `json:"-"`
	Kind             PRDevelopmentControllerOperationKind    `json:"-"`
	Request          PRDevelopmentControllerOperationRequest `json:"-"`
}

// PRDevelopmentControllerOperationFinalize records one exact successful Git
// result under the same live mutation lease used to prepare it.
type PRDevelopmentControllerOperationFinalize struct {
	ControllerID     string                                 `json:"-"`
	AttemptID        string                                 `json:"-"`
	OperationID      string                                 `json:"-"`
	ExpectedRevision int64                                  `json:"-"`
	LeaseToken       string                                 `json:"-"`
	LeaseEpoch       int64                                  `json:"-"`
	Result           PRDevelopmentControllerOperationResult `json:"-"`
}

// PRDevelopmentControllerOperationRecoveryClaim acquires one unresolved
// operation after its mutation lease expired. ClaimID is caller-durable.
type PRDevelopmentControllerOperationRecoveryClaim struct {
	CaseID           string        `json:"-"`
	AttemptID        string        `json:"-"`
	OperationID      string        `json:"-"`
	ExpectedRevision int64         `json:"-"`
	ClaimID          string        `json:"-"`
	WorkerLabel      string        `json:"-"`
	Lease            time.Duration `json:"-"`
}

// PRDevelopmentControllerOperationRecoveryLease carries the exact unresolved
// operation and any raw old/replacement bearers inside the trusted worker
// boundary. Park recovery intentionally has no RecoveryID or replacement.
type PRDevelopmentControllerOperationRecoveryLease struct {
	Controller PRDevelopmentController          `json:"-"`
	Operation  PRDevelopmentControllerOperation `json:"-"`
	Reclaimed  bool                             `json:"-"`
}

// PRDevelopmentControllerOperationRecoveryRenew extends only the exact live
// operation-recovery claim and never grants mutation authority itself.
type PRDevelopmentControllerOperationRecoveryRenew struct {
	ControllerID string        `json:"-"`
	AttemptID    string        `json:"-"`
	OperationID  string        `json:"-"`
	RecoveryID   string        `json:"-"`
	ClaimID      string        `json:"-"`
	ClaimToken   string        `json:"-"`
	ClaimEpoch   int64         `json:"-"`
	Lease        time.Duration `json:"-"`
}

// PRDevelopmentControllerOperationRecoveryFinalize consumes the exact live
// claim after reconciling the Git effect. Adopt, Resume, and Commit supply the
// durable reservation-rotation result and transfer the fresh authority into
// suspension; Park supplies a zero rotation and enters review_pending.
type PRDevelopmentControllerOperationRecoveryFinalize struct {
	ControllerID     string                                        `json:"-"`
	AttemptID        string                                        `json:"-"`
	OperationID      string                                        `json:"-"`
	RecoveryID       string                                        `json:"-"`
	ExpectedRevision int64                                         `json:"-"`
	ClaimID          string                                        `json:"-"`
	ClaimToken       string                                        `json:"-"`
	ClaimEpoch       int64                                         `json:"-"`
	Rotation         PRDevelopmentControllerRecoveryRotationResult `json:"-"`
	Result           PRDevelopmentControllerOperationResult        `json:"-"`
	Lease            time.Duration                                 `json:"-"`
}

// PRDevelopmentLedgerEntryKind alternates one completed mutation account with
// its reservation-free local review. Ordinal 2*n is the mutation account for
// fence n and ordinal 2*n+1 is its review.
type PRDevelopmentLedgerEntryKind string

const (
	PRDevelopmentLedgerAttempt PRDevelopmentLedgerEntryKind = "attempt"
	PRDevelopmentLedgerReview  PRDevelopmentLedgerEntryKind = "review"
)

// PRDevelopmentLedgerReviewOutcome is the bounded result of reviewing an
// immutable parked candidate. Review execution and retries are owned by a
// later worker; this value is only durable evidence.
type PRDevelopmentLedgerReviewOutcome string

const (
	PRDevelopmentLedgerReviewPassed            PRDevelopmentLedgerReviewOutcome = "passed"
	PRDevelopmentLedgerReviewChangesRequired   PRDevelopmentLedgerReviewOutcome = "changes_required"
	PRDevelopmentLedgerReviewAttentionRequired PRDevelopmentLedgerReviewOutcome = "attention_required"
)

// PRDevelopmentLedgerReviewFinding is one structured, immutable local-review
// finding. All fields remain controller-private and are treated as untrusted
// quoted data when projected into a later model context.
type PRDevelopmentLedgerReviewFinding struct {
	Severity       ReviewSeverity `json:"-"`
	Title          string         `json:"-"`
	File           string         `json:"-"`
	Line           *int           `json:"-"`
	Message        string         `json:"-"`
	Evidence       string         `json:"-"`
	Impact         string         `json:"-"`
	Recommendation string         `json:"-"`
	Validation     string         `json:"-"`
}

// PRDevelopmentLedgerEntry is one append-only, hash-chained mutation or
// review account. Attempt-only and review-only fields are validated as
// mutually exclusive. The complete type is private to trusted local workers.
type PRDevelopmentLedgerEntry struct {
	ID             string                       `json:"-"`
	ThreadID       string                       `json:"-"`
	Ordinal        int                          `json:"-"`
	Kind           PRDevelopmentLedgerEntryKind `json:"-"`
	AttemptID      string                       `json:"-"`
	FenceOrdinal   int                          `json:"-"`
	CaseID         string                       `json:"-"`
	CaseOrdinal    int                          `json:"-"`
	Commit         string                       `json:"-"`
	Tree           string                       `json:"-"`
	NoChanges      bool                         `json:"-"`
	Summary        string                       `json:"-"`
	CIPlanDigest   string                       `json:"-"`
	CIResultDigest string                       `json:"-"`
	// CIStatus is derived from the v14 orchestration receipt. Legacy attempt
	// entries predate status storage and are projected as passed.
	CIStatus      PRDevelopmentCIStatus `json:"-"`
	ciStatusBound bool
	ReviewOutcome PRDevelopmentLedgerReviewOutcome   `json:"-"`
	Findings      []PRDevelopmentLedgerReviewFinding `json:"-"`
	FenceHash     string                             `json:"-"`
	PreviousHash  string                             `json:"-"`
	EntryHash     string                             `json:"-"`
	CreatedAt     time.Time                          `json:"-"`
}

// PRDevelopmentLedgerCheckpoint is a logical model-context compaction over an
// exact, fully reviewed contiguous prefix. SourceDigest is the covered entry's
// chain hash. Raw entries and earlier checkpoints remain immutable.
type PRDevelopmentLedgerCheckpoint struct {
	ID             string    `json:"-"`
	ThreadID       string    `json:"-"`
	Generation     int       `json:"-"`
	ThroughOrdinal int       `json:"-"`
	SourceDigest   string    `json:"-"`
	Summary        string    `json:"-"`
	CompactorID    string    `json:"-"`
	PromptDigest   string    `json:"-"`
	PreviousHash   string    `json:"-"`
	CheckpointHash string    `json:"-"`
	CreatedAt      time.Time `json:"-"`
}

// PRDevelopmentLedger is one complete integrity-checked private thread
// ledger. LatestCheckpoint is nil before the first logical compaction.
type PRDevelopmentLedger struct {
	ThreadID          string                          `json:"-"`
	Entries           []PRDevelopmentLedgerEntry      `json:"-"`
	Checkpoints       []PRDevelopmentLedgerCheckpoint `json:"-"`
	EntriesDigest     string                          `json:"-"`
	CheckpointsDigest string                          `json:"-"`
	LatestCheckpoint  *PRDevelopmentLedgerCheckpoint  `json:"-"`
}

// PRDevelopmentContextSnapshot atomically binds one selected immutable case
// position to the complete provider thread and its integrity-checked ledger.
// Case payloads remain separately loadable immutable records.
type PRDevelopmentContextSnapshot struct {
	SelectedOrdinal int                 `json:"-"`
	Thread          PRDevelopmentThread `json:"-"`
	Ledger          PRDevelopmentLedger `json:"-"`
}

// PRDevelopmentAttentionHighWater is the compact, durable audit fence for one
// exact attention subject. It intentionally includes mutable aggregate
// revisions and integrity digests while keeping ControllerRevision and
// OwnerSessionVersion out of semantic decision identity.
type PRDevelopmentAttentionHighWater struct {
	CaseID                  string `json:"-"`
	SelectedOrdinal         int    `json:"-"`
	ConversationVersion     int64  `json:"-"`
	TranscriptDigest        string `json:"-"`
	ThreadID                string `json:"-"`
	ThreadCaseCount         int    `json:"-"`
	ThreadCasesDigest       string `json:"-"`
	LedgerEntryCount        int    `json:"-"`
	LedgerEntriesDigest     string `json:"-"`
	LedgerCheckpointCount   int    `json:"-"`
	LedgerCheckpointsDigest string `json:"-"`
	ReviewEntryID           string `json:"-"`
	ReviewEntryOrdinal      int    `json:"-"`
	ReviewEntryHash         string `json:"-"`
	AttemptID               string `json:"-"`
	AttemptOrdinal          int    `json:"-"`
	FenceOrdinal            int    `json:"-"`
	FenceHash               string `json:"-"`
	ControllerID            string `json:"-"`
	ControllerRevision      int64  `json:"-"`
	ControllerLineVersion   int64  `json:"-"`
	ControllerFenceCount    int    `json:"-"`
	ControllerFencesDigest  string `json:"-"`
	OwnerSessionID          string `json:"-"`
	OwnerSessionVersion     int64  `json:"-"`
	OwnerAttemptCount       int    `json:"-"`
}

// PRDevelopmentAttentionSnapshot is one rich, atomic SQLite read of the exact
// attention-required review subject. Full case, conversation, ledger, and
// controller evidence lets a trusted launcher build the private workflow
// subject without racing independent reads. HighWater is the compact portion
// callers must return unchanged during decision-run admission.
type PRDevelopmentAttentionSnapshot struct {
	Case         PRDevelopmentCase               `json:"-"`
	Thread       PRDevelopmentThread             `json:"-"`
	Conversation PRDevelopmentConversation       `json:"-"`
	OwnerSession PRDevelopmentRepairSession      `json:"-"`
	Controller   PRDevelopmentController         `json:"-"`
	Fence        PRDevelopmentAttemptReviewFence `json:"-"`
	Ledger       PRDevelopmentLedger             `json:"-"`
	ReviewEntry  PRDevelopmentLedgerEntry        `json:"-"`
	HighWater    PRDevelopmentAttentionHighWater `json:"-"`
}

// PRDevelopmentAttentionTriggerStatus is the durable lifecycle of one
// automatically emitted attention-required review occurrence.
type PRDevelopmentAttentionTriggerStatus string

const (
	PRDevelopmentAttentionTriggerPending          PRDevelopmentAttentionTriggerStatus = "pending"
	PRDevelopmentAttentionTriggerClaimed          PRDevelopmentAttentionTriggerStatus = "claimed"
	PRDevelopmentAttentionTriggerDelivered        PRDevelopmentAttentionTriggerStatus = "delivered"
	PRDevelopmentAttentionTriggerNoop             PRDevelopmentAttentionTriggerStatus = "noop"
	PRDevelopmentAttentionTriggerSuperseded       PRDevelopmentAttentionTriggerStatus = "superseded"
	PRDevelopmentAttentionTriggerRecoveryRequired PRDevelopmentAttentionTriggerStatus = "recovery_required"
	PRDevelopmentAttentionTriggerFailed           PRDevelopmentAttentionTriggerStatus = "failed"
)

// PRDevelopmentAttentionTrigger is one immutable local-review occurrence plus
// its private leased launch state. Every field is JSON-private: a later
// case-owned projection must reconstruct only its bounded public status and
// must never expose policy, subject, workflow, retry, or lease authority.
type PRDevelopmentAttentionTrigger struct {
	ReviewEntryID       string                              `json:"-"`
	ReviewEntryHash     string                              `json:"-"`
	CaseID              string                              `json:"-"`
	ConversationVersion int64                               `json:"-"`
	TranscriptDigest    string                              `json:"-"`
	DecisionPoint       string                              `json:"-"`
	Status              PRDevelopmentAttentionTriggerStatus `json:"-"`
	LeaseToken          string                              `json:"-"`
	LeaseUntil          *time.Time                          `json:"-"`
	Attempts            int                                 `json:"-"`
	AvailableAt         time.Time                           `json:"-"`
	PolicyRevision      string                              `json:"-"`
	PinnedPolicy        json.RawMessage                     `json:"-"`
	SubjectRevision     string                              `json:"-"`
	RunID               string                              `json:"-"`
	LastError           string                              `json:"-"`
	CreatedAt           time.Time                           `json:"-"`
	UpdatedAt           time.Time                           `json:"-"`
	CompletedAt         *time.Time                          `json:"-"`
}

// PRDevelopmentAttentionTriggerCaseSnapshot is one atomic, integrity-checked
// bridge read. AttentionRequired distinguishes a historical pre-v16 missing
// occurrence from a case whose current review does not require attention;
// TriggerCurrent reports whether the latest durable trigger still owns the
// current review/controller tail.
type PRDevelopmentAttentionTriggerCaseSnapshot struct {
	CaseID                 string                           `json:"-"`
	ConversationVersion    int64                            `json:"-"`
	CurrentReviewEntryID   string                           `json:"-"`
	CurrentReviewEntryHash string                           `json:"-"`
	CurrentReviewOutcome   PRDevelopmentLedgerReviewOutcome `json:"-"`
	AttentionRequired      bool                             `json:"-"`
	Trigger                *PRDevelopmentAttentionTrigger   `json:"-"`
	TriggerCurrent         bool                             `json:"-"`
}

// PRDevelopmentAttentionPolicyPin immutably binds a live trigger claim to its
// canonical configured policy. Snapshot is revalidated in the same SQLite
// transaction as the pin. An all-zero policy may then complete as Noop;
// active policies separately pin their projected subject before admission.
type PRDevelopmentAttentionPolicyPin struct {
	ReviewEntryID  string                          `json:"-"`
	LeaseToken     string                          `json:"-"`
	PolicyRevision string                          `json:"-"`
	PinnedPolicy   json.RawMessage                 `json:"-"`
	Snapshot       PRDevelopmentAttentionHighWater `json:"-"`
}

// PRDevelopmentAttentionSubjectPin adds the immutable subject revision for an
// already policy-pinned active composition. PolicyRevision prevents a subject
// prepared for another policy from crossing the second pin boundary.
type PRDevelopmentAttentionSubjectPin struct {
	ReviewEntryID   string                          `json:"-"`
	LeaseToken      string                          `json:"-"`
	PolicyRevision  string                          `json:"-"`
	SubjectRevision string                          `json:"-"`
	Snapshot        PRDevelopmentAttentionHighWater `json:"-"`
}

// PRDevelopmentAttentionTriggerRelease returns a live claim to pending while
// retaining its immutable policy/subject pin. Error is sanitized and bounded
// by the store before persistence.
type PRDevelopmentAttentionTriggerRelease struct {
	ReviewEntryID string    `json:"-"`
	LeaseToken    string    `json:"-"`
	AvailableAt   time.Time `json:"-"`
	Error         string    `json:"-"`
}

// PRDevelopmentAttentionTriggerCompletion retires one live claim. Delivered
// requires the exact linked private workflow run; Noop requires a pinned
// all-zero composition as verified by the trusted caller. Other terminal
// states never carry a run ID.
type PRDevelopmentAttentionTriggerCompletion struct {
	ReviewEntryID string                              `json:"-"`
	LeaseToken    string                              `json:"-"`
	Status        PRDevelopmentAttentionTriggerStatus `json:"-"`
	RunID         string                              `json:"-"`
	Error         string                              `json:"-"`
}

// PRDevelopmentAttentionDecisionKey is the semantic identity of one exact
// attention decision. SubjectRevision and PolicyRevision are canonical
// lowercase SHA-256 revisions supplied by trusted workflow preparation.
// Controller and repair-session revisions are deliberately not identity.
type PRDevelopmentAttentionDecisionKey struct {
	CaseID              string `json:"-"`
	ReviewEntryID       string `json:"-"`
	ReviewEntryHash     string `json:"-"`
	ConversationVersion int64  `json:"-"`
	SubjectRevision     string `json:"-"`
	DecisionPoint       string `json:"-"`
	PolicyRevision      string `json:"-"`
}

// PRDevelopmentAttentionDecisionRunAdmission proposes the deterministic
// workflow run for one semantic decision and returns the exact high-water read
// by GetPRDevelopmentAttentionSnapshot as an admission fence.
type PRDevelopmentAttentionDecisionRunAdmission struct {
	Key      PRDevelopmentAttentionDecisionKey `json:"-"`
	Snapshot PRDevelopmentAttentionHighWater   `json:"-"`
	RunID    string                            `json:"-"`
}

// PRDevelopmentAttentionDecisionRunLink is the durable binding from one exact
// attention decision to its external workflow run, including the high-water
// state admitted for audit and historical exact replay.
type PRDevelopmentAttentionDecisionRunLink struct {
	Key       PRDevelopmentAttentionDecisionKey `json:"-"`
	Snapshot  PRDevelopmentAttentionHighWater   `json:"-"`
	RunID     string                            `json:"-"`
	CreatedAt time.Time                         `json:"-"`
}

// PRDevelopmentLedgerAttemptAppend records the concise account for one
// completed, validated, deterministically committed and parked candidate. The
// commit/tree/no-change and fence proof are derived from controller storage.
type PRDevelopmentLedgerAttemptAppend struct {
	CaseID         string `json:"-"`
	AttemptID      string `json:"-"`
	Summary        string `json:"-"`
	CIPlanDigest   string `json:"-"`
	CIResultDigest string `json:"-"`
}

// PRDevelopmentLedgerReviewAppend atomically records one local review for the
// exact immutable fence and finishes the live reservation-free review lease.
// It cannot be appended before its attempt account. The private lease proof is
// retained only as a digest in the completed fence.
type PRDevelopmentLedgerReviewAppend struct {
	CaseID           string                             `json:"-"`
	AttemptID        string                             `json:"-"`
	ControllerID     string                             `json:"-"`
	ExpectedRevision int64                              `json:"-"`
	LeaseToken       string                             `json:"-"`
	LeaseEpoch       int64                              `json:"-"`
	Summary          string                             `json:"-"`
	Outcome          PRDevelopmentLedgerReviewOutcome   `json:"-"`
	Findings         []PRDevelopmentLedgerReviewFinding `json:"-"`
}

// PRDevelopmentReviewCompletion is the atomic durable result of one local AI
// review. NextAttempt is populated only for changes_required and is the exact
// deterministic retry admitted in the same transaction.
type PRDevelopmentReviewCompletion struct {
	Entry       PRDevelopmentLedgerEntry    `json:"-"`
	Controller  PRDevelopmentController     `json:"-"`
	NextAttempt *PRDevelopmentRepairAttempt `json:"-"`
}

// PRDevelopmentLedgerCheckpointAppend records a derived compaction of an
// exact reviewed prefix. The store independently verifies SourceDigest.
type PRDevelopmentLedgerCheckpointAppend struct {
	CaseID         string `json:"-"`
	ThroughOrdinal int    `json:"-"`
	SourceDigest   string `json:"-"`
	Summary        string `json:"-"`
	CompactorID    string `json:"-"`
	PromptDigest   string `json:"-"`
}

// PRDevelopmentCaseStore owns immutable development-case capture and exact
// lookup. Conversation, checkout, execution, publication, and provider actions
// are intentionally separate capabilities.
type PRDevelopmentCaseStore interface {
	LookupPRDevelopmentCapture(
		ctx context.Context,
		identity PRDevelopmentCaptureIdentity,
		expectedThread *PRDevelopmentThreadIdentity,
	) (PRDevelopmentCase, bool, error)
	CapturePRDevelopmentCase(
		ctx context.Context,
		input PRDevelopmentCaptureRequest,
	) (PRDevelopmentCase, bool, error)
	GetPRDevelopmentCase(ctx context.Context, id string) (PRDevelopmentCase, error)
}

// PRDevelopmentThreadReader returns the complete integrity-checked thread for
// one immutable development case without granting capture or mutation.
type PRDevelopmentThreadReader interface {
	GetPRDevelopmentThreadForCase(
		ctx context.Context,
		caseID string,
	) (PRDevelopmentThread, error)
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

// PRDevelopmentRepairOrchestrationStore owns the durable trusted-worker
// checkpoints between provider verification and atomic Park-to-ledger handoff.
type PRDevelopmentRepairOrchestrationStore interface {
	ClaimPRDevelopmentRepairOrchestration(
		ctx context.Context,
		input PRDevelopmentRepairOrchestrationClaim,
	) (PRDevelopmentRepairOrchestration, bool, error)
	RenewPRDevelopmentRepairOrchestration(
		ctx context.Context,
		input PRDevelopmentRepairOrchestrationRenew,
	) error
	GetPRDevelopmentRepairOrchestration(
		ctx context.Context,
		attemptID string,
	) (PRDevelopmentRepairOrchestration, error)
	PinPRDevelopmentRepairOrchestration(
		ctx context.Context,
		input PRDevelopmentRepairOrchestrationPin,
	) (PRDevelopmentRepairOrchestration, bool, error)
	AcquirePRDevelopmentRepairOrchestrationController(
		ctx context.Context,
		input PRDevelopmentRepairOrchestrationControllerAcquire,
	) (PRDevelopmentControllerLease, bool, error)
	StartPRDevelopmentRepairOrchestrationModel(
		ctx context.Context,
		input PRDevelopmentRepairOrchestrationModelStart,
	) (PRDevelopmentRepairOrchestration, bool, error)
	CompletePRDevelopmentRepairOrchestrationModel(
		ctx context.Context,
		input PRDevelopmentRepairOrchestrationModelComplete,
	) (PRDevelopmentRepairOrchestration, bool, error)
	RecordPRDevelopmentRepairOrchestrationValidation(
		ctx context.Context,
		input PRDevelopmentRepairOrchestrationValidation,
	) (PRDevelopmentRepairOrchestration, bool, error)
	FailPRDevelopmentRepairOrchestration(
		ctx context.Context,
		input PRDevelopmentRepairOrchestrationFail,
	) (PRDevelopmentRepairOrchestration, bool, error)
}

// PRDevelopmentControllerReader resolves one private stable thread owner and
// validates its complete immutable review-fence chain without granting a lease.
type PRDevelopmentControllerReader interface {
	GetPRDevelopmentControllerForCase(
		ctx context.Context,
		caseID string,
	) (PRDevelopmentController, error)
}

// PRDevelopmentControllerStore owns only the private controller lease, exact
// retained-line binding, and immutable parked review fences. It runs no model,
// Git, CI, filesystem, workflow, provider, or publication effect.
type PRDevelopmentControllerStore interface {
	PRDevelopmentControllerReader
	AcquirePRDevelopmentControllerLease(
		ctx context.Context,
		input PRDevelopmentControllerAcquire,
	) (PRDevelopmentControllerLease, bool, error)
	RenewPRDevelopmentControllerLease(
		ctx context.Context,
		input PRDevelopmentControllerRenew,
	) error
	ClaimPRDevelopmentControllerRecovery(
		ctx context.Context,
		input PRDevelopmentControllerRecoveryClaim,
	) (PRDevelopmentControllerRecoveryLease, bool, error)
	RenewPRDevelopmentControllerRecovery(
		ctx context.Context,
		input PRDevelopmentControllerRecoveryRenew,
	) error
	FinalizePRDevelopmentControllerRecovery(
		ctx context.Context,
		input PRDevelopmentControllerRecoveryFinalize,
	) (PRDevelopmentController, bool, error)
	BindPRDevelopmentControllerLine(
		ctx context.Context,
		input PRDevelopmentControllerLineBind,
	) (PRDevelopmentController, bool, error)
	RecordPRDevelopmentAttemptReviewFence(
		ctx context.Context,
		input PRDevelopmentAttemptReviewFenceRecord,
	) (PRDevelopmentAttemptReviewFence, bool, error)
	FinishPRDevelopmentControllerReview(
		ctx context.Context,
		input PRDevelopmentControllerReviewTransition,
	) (PRDevelopmentController, bool, error)
	ReleasePRDevelopmentControllerReview(
		ctx context.Context,
		input PRDevelopmentControllerReviewTransition,
	) (PRDevelopmentController, error)
}

// PRDevelopmentControllerOperationStore owns only the schema-v13 write-ahead
// operation and operation-local recovery lifecycle. It is separate from the
// base controller interface so callers can depend on the narrower capability.
type PRDevelopmentControllerOperationStore interface {
	PreparePRDevelopmentControllerOperation(
		ctx context.Context,
		input PRDevelopmentControllerOperationPrepare,
	) (PRDevelopmentControllerOperation, bool, error)
	FinalizePRDevelopmentControllerOperation(
		ctx context.Context,
		input PRDevelopmentControllerOperationFinalize,
	) (PRDevelopmentControllerOperationTransition, bool, error)
	ClaimPRDevelopmentControllerOperationRecovery(
		ctx context.Context,
		input PRDevelopmentControllerOperationRecoveryClaim,
	) (PRDevelopmentControllerOperationRecoveryLease, bool, error)
	RenewPRDevelopmentControllerOperationRecovery(
		ctx context.Context,
		input PRDevelopmentControllerOperationRecoveryRenew,
	) error
	FinalizePRDevelopmentControllerOperationRecovery(
		ctx context.Context,
		input PRDevelopmentControllerOperationRecoveryFinalize,
	) (PRDevelopmentControllerOperationTransition, bool, error)
}

// PRDevelopmentLedgerReader returns one complete private, integrity-checked
// attempt/review ledger for the selected provider-verified thread.
type PRDevelopmentLedgerReader interface {
	GetPRDevelopmentLedgerForCase(
		ctx context.Context,
		caseID string,
	) (PRDevelopmentLedger, error)
}

// PRDevelopmentContextReader captures the thread high-water and ledger in one
// read transaction for deterministic later model-context construction.
type PRDevelopmentContextReader interface {
	GetPRDevelopmentContextSnapshot(
		ctx context.Context,
		caseID string,
	) (PRDevelopmentContextSnapshot, error)
}

// PRDevelopmentAttentionSnapshotReader captures every subject component and
// its integrity high-water in one read transaction. It grants no workflow,
// model, Git, provider, filesystem, or controller mutation authority.
type PRDevelopmentAttentionSnapshotReader interface {
	GetPRDevelopmentAttentionSnapshot(
		ctx context.Context,
		caseID string,
	) (PRDevelopmentAttentionSnapshot, error)
}

// PRDevelopmentAttentionTriggerSnapshotReader returns the immutable claimed
// occurrence together with its exact rich subject snapshot. The queue lease is
// scheduling authority only and grants no controller or repository mutation.
type PRDevelopmentAttentionTriggerSnapshotReader interface {
	GetClaimedPRDevelopmentAttentionSnapshot(
		ctx context.Context,
		reviewEntryID string,
		leaseToken string,
	) (PRDevelopmentAttentionTrigger, PRDevelopmentAttentionSnapshot, error)
}

// PRDevelopmentAttentionTriggerCaseReader provides the later case-owned chat
// bridge one atomic status read without stitching ledger and trigger rows.
type PRDevelopmentAttentionTriggerCaseReader interface {
	GetCurrentPRDevelopmentAttentionTriggerForCase(
		ctx context.Context,
		caseID string,
	) (PRDevelopmentAttentionTriggerCaseSnapshot, error)
}

// PRDevelopmentAttentionTriggerQueue owns automatic attention occurrence
// delivery. Every mutation and claimed snapshot read is fenced by a live opaque
// lease token; a completed trigger is never reclaimable.
type PRDevelopmentAttentionTriggerQueue interface {
	PRDevelopmentAttentionTriggerSnapshotReader
	GetPRDevelopmentAttentionTrigger(
		ctx context.Context,
		reviewEntryID string,
	) (PRDevelopmentAttentionTrigger, error)
	ClaimPRDevelopmentAttentionTriggers(
		ctx context.Context,
		workerLabel string,
		limit int,
		lease time.Duration,
	) ([]PRDevelopmentAttentionTrigger, error)
	RenewPRDevelopmentAttentionTriggerLease(
		ctx context.Context,
		reviewEntryID string,
		leaseToken string,
		lease time.Duration,
	) error
	PinPRDevelopmentAttentionTriggerPolicy(
		ctx context.Context,
		input PRDevelopmentAttentionPolicyPin,
	) (PRDevelopmentAttentionTrigger, error)
	PinPRDevelopmentAttentionTriggerSubject(
		ctx context.Context,
		input PRDevelopmentAttentionSubjectPin,
	) (PRDevelopmentAttentionTrigger, error)
	ReleasePRDevelopmentAttentionTrigger(
		ctx context.Context,
		input PRDevelopmentAttentionTriggerRelease,
	) error
	CompletePRDevelopmentAttentionTrigger(
		ctx context.Context,
		input PRDevelopmentAttentionTriggerCompletion,
	) error
}

// PRDevelopmentAttentionDecisionRunStore atomically fences one external
// workflow-run create with an exact attention-required review snapshot. An
// exact historical retry returns existed=true before checking current mutable
// state and never invokes create. The callback must not call back into the same
// Store because admission holds its sole SQLite connection in a write
// transaction until the callback returns.
type PRDevelopmentAttentionDecisionRunStore interface {
	GetPRDevelopmentAttentionDecisionRun(
		ctx context.Context,
		key PRDevelopmentAttentionDecisionKey,
	) (PRDevelopmentAttentionDecisionRunLink, error)
	AdmitPRDevelopmentAttentionDecisionRun(
		ctx context.Context,
		admission PRDevelopmentAttentionDecisionRunAdmission,
		create func(context.Context) error,
	) (link PRDevelopmentAttentionDecisionRunLink, existed bool, err error)
}

// PRDevelopmentLedgerStore owns only append-only post-effect accounts and
// logical compaction checkpoints. It runs no model, Git, CI, filesystem,
// workflow, provider, publication, or destructive compaction effect.
type PRDevelopmentLedgerStore interface {
	PRDevelopmentLedgerReader
	AppendPRDevelopmentLedgerAttempt(
		ctx context.Context,
		input PRDevelopmentLedgerAttemptAppend,
	) (PRDevelopmentLedgerEntry, bool, error)
	AppendPRDevelopmentLedgerReview(
		ctx context.Context,
		input PRDevelopmentLedgerReviewAppend,
	) (PRDevelopmentLedgerEntry, bool, error)
	AppendPRDevelopmentLedgerCheckpoint(
		ctx context.Context,
		input PRDevelopmentLedgerCheckpointAppend,
	) (PRDevelopmentLedgerCheckpoint, bool, error)
}

// PRDevelopmentReviewQueue owns background reservation-free review claim and
// atomic completion scheduling. Renewal and explicit release reuse the exact
// controller lease methods on PRDevelopmentControllerStore.
type PRDevelopmentReviewQueue interface {
	ClaimPRDevelopmentReview(
		ctx context.Context,
		input PRDevelopmentReviewClaimRequest,
	) (PRDevelopmentReviewLease, bool, error)
	CompletePRDevelopmentReview(
		ctx context.Context,
		input PRDevelopmentLedgerReviewAppend,
	) (PRDevelopmentReviewCompletion, bool, error)
}
