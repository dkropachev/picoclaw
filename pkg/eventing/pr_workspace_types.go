package eventing

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const (
	prWorkspaceIDPrefix         = "prw_"
	prProviderSnapshotIDPrefix  = "pps_"
	prCharterRevisionIDPrefix   = "pcr_"
	prStageRunIDPrefix          = "psr_"
	prFindingIDPrefix           = "pfn_"
	prFindingEventIDPrefix      = "pfe_"
	prConversationIDPrefix      = "pcv_"
	prMessageIDPrefix           = "pms_"
	prCorrectionIDPrefix        = "pco_"
	prRepositoryLessonIDPrefix  = "prl_"
	prNudgeRoundIDPrefix        = "pnr_"
	prNudgeRewardIDPrefix       = "pnw_"
	prDeferredGroupIDPrefix     = "pdg_"
	prDeferredGroupItemIDPrefix = "pdi_"
	prRepairAttemptIDPrefix     = "pra_"
	prValidationRunIDPrefix     = "pvr_"
	prGateRunIDPrefix           = "pgr_"
	prPublicationIDPrefix       = "ppb_"
	prOperationIntentIDPrefix   = "poi_"
	prIngressWatermarkIDPrefix  = "piw_"
	prActivityIDPrefix          = "pac_"
	prHistoryIDPrefix           = "phs_"
	prLeaseTokenIDPrefix        = "plt_"

	// MaxPRWorkspaceRecordBytes bounds one typed JSON child record. Larger
	// artifacts belong in the repository/workspace artifact stores, not in the
	// eventing coordination ledger.
	MaxPRWorkspaceRecordBytes = 1 << 20
	// MaxPRWorkspaceMessageBytes bounds one conversation message.
	MaxPRWorkspaceMessageBytes = 64 << 10
)

var (
	// ErrInvalidPRWorkspace reports malformed workspace identity or mutation
	// content.
	ErrInvalidPRWorkspace = errors.New("invalid pull request workspace")
	// ErrPRWorkspaceConflict reports aggregate-version, request-id, or entity
	// ownership conflicts.
	ErrPRWorkspaceConflict = errors.New("pull request workspace conflict")
)

// PRType is the single, strict purpose of a confirmed pull-request charter.
type PRType string

const (
	PRTypeFix           PRType = "fix"
	PRTypeRefactor      PRType = "refactor"
	PRTypeFeature       PRType = "feature"
	PRTypeDocumentation PRType = "documentation"
	PRTypeTest          PRType = "test"
)

// PRWorkspacePhase is the durable lifecycle position of a workspace.
type PRWorkspacePhase string

const (
	PRWorkspaceIntake          PRWorkspacePhase = "intake"
	PRWorkspaceCharter         PRWorkspacePhase = "charter"
	PRWorkspaceReview          PRWorkspacePhase = "review"
	PRWorkspaceTriage          PRWorkspacePhase = "triage"
	PRWorkspaceImplementation  PRWorkspacePhase = "implementation"
	PRWorkspaceValidation      PRWorkspacePhase = "validation"
	PRWorkspaceCompletionAudit PRWorkspacePhase = "completion_audit"
	PRWorkspacePublication     PRWorkspacePhase = "publication"
	PRWorkspaceComplete        PRWorkspacePhase = "complete"
)

// PRExecutionState is shared by workspace and durable stage operations.
type PRExecutionState string

const (
	PRExecutionQueued      PRExecutionState = "queued"
	PRExecutionRunning     PRExecutionState = "running"
	PRExecutionWaitingGate PRExecutionState = "waiting_gate"
	PRExecutionWaitingUser PRExecutionState = "waiting_user"
	PRExecutionSucceeded   PRExecutionState = "succeeded"
	PRExecutionFailed      PRExecutionState = "failed"
	PRExecutionBlocked     PRExecutionState = "blocked"
	PRExecutionCanceled    PRExecutionState = "canceled"
	PRExecutionStale       PRExecutionState = "stale"
	PRExecutionUnknown     PRExecutionState = "unknown"
)

type PRFindingDisposition string

const (
	PRFindingOpen      PRFindingDisposition = "open"
	PRFindingInScope   PRFindingDisposition = "in_scope"
	PRFindingFixed     PRFindingDisposition = "fixed"
	PRFindingDeferred  PRFindingDisposition = "deferred"
	PRFindingDismissed PRFindingDisposition = "dismissed"
)

type PRScopeDistance string

const (
	PRScopeExact             PRScopeDistance = "S0_exact"
	PRScopeNecessaryAdjacent PRScopeDistance = "S1_necessary_adjacent"
	PRScopeRelatedFollowup   PRScopeDistance = "S2_related_followup"
	PRScopeUnrelated         PRScopeDistance = "S3_unrelated"
)

type PRChangeSize string

const (
	PRChangeSizeXS PRChangeSize = "XS"
	PRChangeSizeS  PRChangeSize = "S"
	PRChangeSizeM  PRChangeSize = "M"
	PRChangeSizeL  PRChangeSize = "L"
)

type PRWorkPresence string

const (
	PRWorkCandidatePresent PRWorkPresence = "candidate_present"
	PRWorkFollowUp         PRWorkPresence = "follow_up"
)

type PRScopeChange struct {
	Path           string          `json:"path"`
	Hunk           string          `json:"hunk,omitempty"`
	Module         string          `json:"module,omitempty"`
	SemanticLines  int             `json:"semantic_lines"`
	Presence       PRWorkPresence  `json:"presence,omitempty"`
	ScopeDistance  PRScopeDistance `json:"scope_distance"`
	ChangeSize     PRChangeSize    `json:"change_size"`
	TypeCompatible bool            `json:"type_compatible"`
	Confidence     float64         `json:"confidence"`
	CharterClauses []string        `json:"charter_clauses,omitempty"`
	Explanation    string          `json:"explanation,omitempty"`
}

type PRPublicationKind string

const (
	PRPublicationGitHubReview PRPublicationKind = "github_review"
	PRPublicationBranchPush   PRPublicationKind = "branch_push"
	PRPublicationGitHubIssue  PRPublicationKind = "github_issue"
)

type PRPublicationStatus string

const (
	PRPublicationPending   PRPublicationStatus = "pending"
	PRPublicationClaimed   PRPublicationStatus = "claimed"
	PRPublicationPublished PRPublicationStatus = "published"
	PRPublicationUnknown   PRPublicationStatus = "unknown"
	PRPublicationFailed    PRPublicationStatus = "failed"
)

type PRPullState string

const (
	PRPullOpen   PRPullState = "open"
	PRPullClosed PRPullState = "closed"
	PRPullMerged PRPullState = "merged"
)

type PRRecordStatus string

const (
	PRRecordDraft      PRRecordStatus = "draft"
	PRRecordConfirmed  PRRecordStatus = "confirmed"
	PRRecordSuperseded PRRecordStatus = "superseded"
	PRRecordActive     PRRecordStatus = "active"
	PRRecordRevoked    PRRecordStatus = "revoked"
	PRRecordResolved   PRRecordStatus = "resolved"
	PRRecordDismissed  PRRecordStatus = "dismissed"
)

// PRWorkspaceRecord is embedded by every workspace-owned child record.
// Ordinal and timestamps are store-assigned. Supplying an ID updates that
// entity; omitting it appends a new entity.
type PRWorkspaceRecord struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Ordinal     int64     `json:"ordinal"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// PRProviderSnapshot is a provider-verified, immutable-in-history observation.
type PRProviderSnapshot struct {
	PRWorkspaceRecord
	Provider            string    `json:"provider"`
	ProviderOrigin      string    `json:"provider_origin"`
	RepositoryID        string    `json:"repository_id"`
	Repository          string    `json:"repository"`
	PullRequestID       string    `json:"pull_request_id"`
	PullNumber          int64     `json:"pull_number"`
	Title               string    `json:"title"`
	Body                string    `json:"body,omitempty"`
	AuthorID            string    `json:"author_id"`
	AuthorLogin         string    `json:"author_login"`
	AuthenticatedUserID string    `json:"authenticated_user_id"`
	BaseRef             string    `json:"base_ref"`
	BaseSHA             string    `json:"base_sha"`
	HeadRepositoryID    string    `json:"head_repository_id"`
	HeadRepository      string    `json:"head_repository"`
	HeadRef             string    `json:"head_ref"`
	HeadSHA             string    `json:"head_sha"`
	State               string    `json:"state"`
	Owned               bool      `json:"owned"`
	HeadWritable        bool      `json:"head_writable"`
	CanReview           bool      `json:"can_review"`
	CanCreateIssue      bool      `json:"can_create_issue"`
	ProviderRevision    string    `json:"provider_revision,omitempty"`
	ObservedAt          time.Time `json:"observed_at"`
}

// PRWorkspace is the current provider/lifecycle projection.
type PRWorkspace struct {
	ID                     string           `json:"id"`
	Provider               string           `json:"provider"`
	ProviderOrigin         string           `json:"provider_origin"`
	RepositoryID           string           `json:"repository_id"`
	PullRequestID          string           `json:"pull_request_id"`
	Repository             string           `json:"repository"`
	PullNumber             int64            `json:"pull_number"`
	Phase                  PRWorkspacePhase `json:"phase"`
	ExecutionState         PRExecutionState `json:"execution_state"`
	ActiveCharterID        string           `json:"active_charter_id,omitempty"`
	ProviderHeadSHA        string           `json:"provider_head_sha"`
	CurrentProviderOrdinal int64            `json:"current_provider_ordinal"`
	Version                int64            `json:"version"`
	CreatedAt              time.Time        `json:"created_at"`
	UpdatedAt              time.Time        `json:"updated_at"`
}

type PRCharterRevision struct {
	PRWorkspaceRecord
	Status             PRRecordStatus `json:"status"`
	Revision           int64          `json:"revision"`
	Type               PRType         `json:"type"`
	Goal               string         `json:"goal"`
	AcceptanceCriteria []string       `json:"acceptance_criteria"`
	IncludedAreas      []string       `json:"included_areas,omitempty"`
	Exclusions         []string       `json:"exclusions,omitempty"`
	NonGoals           []string       `json:"non_goals,omitempty"`
	BaseSHA            string         `json:"base_sha"`
	HeadSHA            string         `json:"head_sha"`
	CreatedBy          string         `json:"created_by"`
	ContentDigest      string         `json:"content_digest,omitempty"`
	SupersedesID       string         `json:"supersedes_id,omitempty"`
	ConfirmedAt        *time.Time     `json:"confirmed_at,omitempty"`
}

type PRStageRun struct {
	PRWorkspaceRecord
	Phase            PRWorkspacePhase `json:"phase"`
	Kind             string           `json:"kind"`
	State            PRExecutionState `json:"state"`
	Attempt          int              `json:"attempt"`
	CharterID        string           `json:"charter_id,omitempty"`
	WorkspaceVersion int64            `json:"workspace_version"`
	BaseSHA          string           `json:"base_sha,omitempty"`
	HeadSHA          string           `json:"head_sha,omitempty"`
	AgentID          string           `json:"agent_id,omitempty"`
	Model            string           `json:"model,omitempty"`
	PromptDigest     string           `json:"prompt_digest,omitempty"`
	Summary          string           `json:"summary,omitempty"`
	PublicErrorCode  string           `json:"public_error_code,omitempty"`
	Evidence         json.RawMessage  `json:"evidence,omitempty"`
	StartedAt        *time.Time       `json:"started_at,omitempty"`
	FinishedAt       *time.Time       `json:"finished_at,omitempty"`
}

type PRChangeMetrics struct {
	Files         int `json:"files"`
	SemanticLines int `json:"semantic_lines"`
	Modules       int `json:"modules"`
	RawLines      int `json:"raw_lines,omitempty"`
}

type PRFinding struct {
	PRWorkspaceRecord
	Origin              string               `json:"origin"`
	StageRunID          string               `json:"stage_run_id,omitempty"`
	NudgeRoundID        string               `json:"nudge_round_id,omitempty"`
	ExternalID          string               `json:"external_id,omitempty"`
	Fingerprint         string               `json:"fingerprint"`
	Severity            string               `json:"severity"`
	Title               string               `json:"title"`
	Message             string               `json:"message"`
	File                string               `json:"file,omitempty"`
	Line                *int                 `json:"line,omitempty"`
	Evidence            string               `json:"evidence,omitempty"`
	Impact              string               `json:"impact,omitempty"`
	Recommendation      string               `json:"recommendation,omitempty"`
	Validation          string               `json:"validation,omitempty"`
	Disposition         PRFindingDisposition `json:"disposition"`
	ScopeDistance       PRScopeDistance      `json:"scope_distance"`
	ChangeSize          PRChangeSize         `json:"change_size"`
	TypeCompatible      bool                 `json:"type_compatible"`
	ClassificationConf  float64              `json:"classification_confidence"`
	CharterClauses      []string             `json:"charter_clauses,omitempty"`
	EstimatedMetrics    PRChangeMetrics      `json:"estimated_metrics"`
	MetricsEstimated    bool                 `json:"metrics_estimated"`
	ScopeExplanation    string               `json:"scope_explanation,omitempty"`
	ScopePresence       PRWorkPresence       `json:"scope_presence,omitempty"`
	ScopeChangeEvidence []PRScopeChange      `json:"scope_change_evidence,omitempty"`
	ActualMetrics       *PRChangeMetrics     `json:"actual_metrics,omitempty"`
	DeferredGroupID     string               `json:"deferred_group_id,omitempty"`
	NudgeReward         *float64             `json:"nudge_reward,omitempty"`
	RewardSource        string               `json:"reward_source,omitempty"`
	Version             int64                `json:"version"`
}

type PRFindingEvent struct {
	PRWorkspaceRecord
	FindingID string          `json:"finding_id"`
	Kind      string          `json:"kind"`
	Actor     string          `json:"actor"`
	Before    json.RawMessage `json:"before,omitempty"`
	After     json.RawMessage `json:"after,omitempty"`
	Reason    string          `json:"reason,omitempty"`
}

type PRConversation struct {
	PRWorkspaceRecord
	Channel string           `json:"channel"`
	Phase   PRWorkspacePhase `json:"phase"`
	Status  PRRecordStatus   `json:"status"`
}

type PRMessage struct {
	PRWorkspaceRecord
	ConversationID string           `json:"conversation_id,omitempty"`
	StageRunID     string           `json:"stage_run_id,omitempty"`
	FindingID      string           `json:"finding_id,omitempty"`
	Phase          PRWorkspacePhase `json:"phase"`
	Kind           string           `json:"kind"`
	Role           string           `json:"role"`
	Content        string           `json:"content"`
	CharterID      string           `json:"charter_id,omitempty"`
	HeadSHA        string           `json:"head_sha,omitempty"`
	CorrectionID   string           `json:"correction_id,omitempty"`
}

type PRCorrection struct {
	PRWorkspaceRecord
	Kind               string         `json:"kind"`
	Status             PRRecordStatus `json:"status"`
	TargetKind         string         `json:"target_kind"`
	TargetID           string         `json:"target_id,omitempty"`
	StageRunID         string         `json:"stage_run_id,omitempty"`
	OriginalClaim      string         `json:"original_claim"`
	Correction         string         `json:"correction"`
	Reason             string         `json:"reason,omitempty"`
	Evidence           string         `json:"evidence,omitempty"`
	AppliesToReview    bool           `json:"applies_to_review"`
	AppliesToImplement bool           `json:"applies_to_implementation"`
	CharterID          string         `json:"charter_id,omitempty"`
	HeadSHA            string         `json:"head_sha,omitempty"`
	SupersedesID       string         `json:"supersedes_id,omitempty"`
	RepositoryLessonID string         `json:"repository_lesson_id,omitempty"`
	Promoted           bool           `json:"promoted"`
}

type PRRepositoryLesson struct {
	PRWorkspaceRecord
	RepositoryID       string             `json:"repository_id"`
	Status             PRRecordStatus     `json:"status"`
	Kind               string             `json:"kind"`
	Content            string             `json:"content"`
	SourceCorrectionID string             `json:"source_correction_id"`
	ApplicableTypes    []PRType           `json:"applicable_types,omitempty"`
	ApplicablePhases   []PRWorkspacePhase `json:"applicable_phases,omitempty"`
	ConfirmedBy        string             `json:"confirmed_by"`
	RevokedAt          *time.Time         `json:"revoked_at,omitempty"`
}

type PRNudgeRound struct {
	PRWorkspaceRecord
	StageRunID       string           `json:"stage_run_id"`
	Phase            PRWorkspacePhase `json:"phase"`
	Stage            string           `json:"stage,omitempty"`
	State            PRExecutionState `json:"state"`
	Round            int              `json:"round"`
	MinimumRounds    int              `json:"minimum_rounds"`
	HardCap          int              `json:"hard_cap"`
	StrategyFamily   string           `json:"strategy_family"`
	Strategy         string           `json:"strategy,omitempty"`
	CoverageTarget   string           `json:"coverage_target"`
	Challenge        string           `json:"challenge,omitempty"`
	ChallengeDigest  string           `json:"challenge_digest"`
	VariantDigest    string           `json:"variant_digest,omitempty"`
	PromptDigest     string           `json:"prompt_digest"`
	AgentID          string           `json:"agent_id,omitempty"`
	Model            string           `json:"model,omitempty"`
	CandidateCount   int              `json:"candidate_count"`
	NovelCount       int              `json:"novel_count"`
	DuplicateCount   int              `json:"duplicate_count"`
	FindingIDs       []string         `json:"finding_ids,omitempty"`
	ResolvedFindings int              `json:"resolved_findings"`
	Reward           *float64         `json:"reward,omitempty"`
	RewardProvenance string           `json:"reward_provenance,omitempty"`
	Summary          string           `json:"summary,omitempty"`
	PublicError      string           `json:"public_error,omitempty"`
}

type PRNudgeReward struct {
	PRWorkspaceRecord
	NudgeRoundID string  `json:"nudge_round_id"`
	FindingID    string  `json:"finding_id,omitempty"`
	Reward       float64 `json:"reward"`
	Outcome      string  `json:"outcome"`
	Provenance   string  `json:"provenance"`
}

type PRDeferredGroup struct {
	PRWorkspaceRecord
	Status                PRRecordStatus        `json:"status"`
	Title                 string                `json:"title"`
	Body                  string                `json:"body"`
	ScopeDistance         PRScopeDistance       `json:"scope_distance"`
	ChangeSize            PRChangeSize          `json:"change_size"`
	ScopeFiles            int                   `json:"scope_files"`
	ScopeSemanticLines    int                   `json:"scope_semantic_lines"`
	ScopeModules          int                   `json:"scope_modules"`
	ScopeEstimated        bool                  `json:"scope_estimated"`
	ScopeTypeCompatible   bool                  `json:"scope_type_compatible"`
	ScopeConfidence       float64               `json:"scope_confidence"`
	ScopeCharterClauses   []string              `json:"scope_charter_clauses,omitempty"`
	ScopeExplanation      string                `json:"scope_explanation,omitempty"`
	ScopePresence         PRWorkPresence        `json:"scope_presence,omitempty"`
	ScopeChangeEvidence   []PRScopeChange       `json:"scope_change_evidence,omitempty"`
	Labels                []string              `json:"labels,omitempty"`
	DraftRevision         int64                 `json:"draft_revision"`
	ExistingIssueID       string                `json:"existing_issue_id,omitempty"`
	ExternalURL           string                `json:"external_url,omitempty"`
	PublicationID         string                `json:"publication_id,omitempty"`
	PublicationSuppressed bool                  `json:"publication_suppressed,omitempty"`
	SuppressionReason     string                `json:"suppression_reason,omitempty"`
	Version               int64                 `json:"version"`
	Items                 []PRDeferredGroupItem `json:"items,omitempty"`
}

type PRDeferredGroupItem struct {
	PRWorkspaceRecord
	GroupID        string `json:"group_id"`
	FindingID      string `json:"finding_id"`
	OrdinalInGroup int    `json:"ordinal_in_group"`
	Removed        bool   `json:"removed,omitempty"`
}

type PRRepairAttempt struct {
	PRWorkspaceRecord
	StageRunID          string           `json:"stage_run_id"`
	State               PRExecutionState `json:"state"`
	Attempt             int              `json:"attempt"`
	Instruction         string           `json:"instruction,omitempty"`
	RepairWorkspaceID   string           `json:"repair_workspace_id,omitempty"`
	ResultSummary       string           `json:"result_summary,omitempty"`
	FindingIDs          []string         `json:"finding_ids,omitempty"`
	GoalDigest          string           `json:"goal_digest"`
	BaseCommit          string           `json:"base_commit"`
	TipCommit           string           `json:"tip_commit,omitempty"`
	CandidateSHA        string           `json:"candidate_sha,omitempty"`
	Tree                string           `json:"tree,omitempty"`
	ChangedFiles        []string         `json:"changed_files,omitempty"`
	Metrics             PRChangeMetrics  `json:"metrics"`
	ScopeDrift          bool             `json:"scope_drift"`
	TypeDrift           bool             `json:"type_drift"`
	ScopeDistance       PRScopeDistance  `json:"scope_distance,omitempty"`
	ScopeChangeSize     PRChangeSize     `json:"scope_change_size,omitempty"`
	ScopeEstimated      bool             `json:"scope_estimated"`
	ScopeTypeCompatible bool             `json:"scope_type_compatible"`
	ScopeConfidence     float64          `json:"scope_confidence"`
	ScopeCharterClauses []string         `json:"scope_charter_clauses,omitempty"`
	ScopeExplanation    string           `json:"scope_explanation,omitempty"`
	ScopePresence       PRWorkPresence   `json:"scope_presence,omitempty"`
	ScopeChangeEvidence []PRScopeChange  `json:"scope_change_evidence,omitempty"`
	AgentID             string           `json:"agent_id,omitempty"`
	Model               string           `json:"model,omitempty"`
	PromptDigest        string           `json:"prompt_digest,omitempty"`
	ScopePromptDigest   string           `json:"scope_prompt_digest,omitempty"`
	PublicErrorCode     string           `json:"public_error_code,omitempty"`
	StartedAt           *time.Time       `json:"started_at,omitempty"`
	FinishedAt          *time.Time       `json:"finished_at,omitempty"`
	// PublicationFence is private coordination evidence. It remains in the
	// durable eventing payload, but the prworkspace adapter deliberately keeps
	// it out of browser-facing domain JSON.
	PublicationFence *PRImplementationPublicationFence `json:"publication_fence,omitempty"`
}

// PRImplementationPublicationFence identifies the exact isolated git line
// and commit tuple that may be published or reconciled after a restart.
type PRImplementationPublicationFence struct {
	GitWorkspaceID string `json:"git_workspace_id"`
	LineID         string `json:"line_id"`
	LineVersion    int64  `json:"line_version"`
	MutationEpoch  int64  `json:"mutation_epoch"`
	ParkIntentID   string `json:"park_intent_id"`
	BaseCommit     string `json:"base_commit"`
	Tip            string `json:"tip"`
	Tree           string `json:"tree"`
}

type PRValidationCheck struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Summary    string `json:"summary,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

type PRValidationRun struct {
	PRWorkspaceRecord
	StageRunID      string              `json:"stage_run_id"`
	RepairAttemptID string              `json:"repair_attempt_id,omitempty"`
	CandidateSHA    string              `json:"candidate_sha,omitempty"`
	State           PRExecutionState    `json:"state"`
	Kind            string              `json:"kind"`
	Command         string              `json:"command,omitempty"`
	ExitCode        *int                `json:"exit_code,omitempty"`
	Summary         string              `json:"summary,omitempty"`
	EvidenceDigest  string              `json:"evidence_digest,omitempty"`
	PublicErrorCode string              `json:"public_error_code,omitempty"`
	Checks          []PRValidationCheck `json:"checks,omitempty"`
	StartedAt       *time.Time          `json:"started_at,omitempty"`
	FinishedAt      *time.Time          `json:"finished_at,omitempty"`
}

type PRGateTurn struct {
	StageID        string          `json:"stage_id"`
	Kind           string          `json:"kind"`
	Title          string          `json:"title"`
	Status         string          `json:"status"`
	GateForm       json.RawMessage `json:"gate-form,omitempty"`
	FieldValues    map[string]any  `json:"field-values,omitempty"`
	ActorKind      string          `json:"actor-kind,omitempty"`
	ExecutionID    string          `json:"execution-id,omitempty"`
	ActionRevision string          `json:"action-revision,omitempty"`
	InputHash      string          `json:"input-hash,omitempty"`
}

type PRGateRun struct {
	PRWorkspaceRecord
	DecisionPoint     string           `json:"decision_point"`
	TargetID          string           `json:"target_id,omitempty"`
	State             PRExecutionState `json:"state"`
	PolicyRevision    string           `json:"policy-revision"`
	WorkflowRef       string           `json:"workflow-ref,omitempty"`
	WorkflowRevision  string           `json:"workflow-revision,omitempty"`
	GateRef           string           `json:"gate-ref,omitempty"`
	ConfigID          string           `json:"config-id"`
	ConfigRevision    string           `json:"config-revision"`
	PinnedPolicy      json.RawMessage  `json:"pinned_policy"`
	PinnedPolicyHash  string           `json:"pinned_policy_hash"`
	SubjectRevision   string           `json:"subject_revision"`
	PinnedSubject     json.RawMessage  `json:"pinned_subject"`
	PinnedSubjectHash string           `json:"pinned_subject_hash"`
	WorkflowRunID     string           `json:"workflow_run_id,omitempty"`
	RuntimePresent    bool             `json:"runtime_present,omitempty"`
	CurrentStageID    string           `json:"current_stage_id,omitempty"`
	Turns             []PRGateTurn     `json:"turns,omitempty"`
	Evidence          json.RawMessage  `json:"evidence,omitempty"`
	FinishedAt        *time.Time       `json:"finished_at,omitempty"`
}

type PRPublication struct {
	PRWorkspaceRecord
	Kind            PRPublicationKind   `json:"kind"`
	Status          PRPublicationStatus `json:"status"`
	TargetID        string              `json:"target_id,omitempty"`
	FindingIDs      []string            `json:"finding_ids,omitempty"`
	GateRunID       string              `json:"gate_run_id,omitempty"`
	DeferredGroupID string              `json:"deferred_group_id,omitempty"`
	ExpectedHeadSHA string              `json:"expected_head_sha,omitempty"`
	Marker          string              `json:"marker,omitempty"`
	Request         json.RawMessage     `json:"request"`
	RequestDigest   string              `json:"request_digest"`
	PayloadDigest   string              `json:"payload_digest,omitempty"`
	// ExecutionState preserves the richer prworkspace lifecycle while Status
	// remains the durable worker-queue state.
	ExecutionState  PRExecutionState `json:"execution_state,omitempty"`
	AvailableAt     time.Time        `json:"available_at"`
	LeaseOwner      string           `json:"lease_owner,omitempty"`
	LeaseToken      string           `json:"lease_token,omitempty"`
	LeaseUntil      *time.Time       `json:"lease_until,omitempty"`
	Attempts        int              `json:"attempts"`
	ExternalID      string           `json:"external_id,omitempty"`
	ExternalURL     string           `json:"external_url,omitempty"`
	PublicErrorCode string           `json:"public_error_code,omitempty"`
	InternalError   string           `json:"internal_error,omitempty"`
	PublishedAt     *time.Time       `json:"published_at,omitempty"`
}

type PRWorkspaceOperationIntent struct {
	PRWorkspaceRecord
	Kind                  string           `json:"kind"`
	State                 PRExecutionState `json:"state"`
	StageRunID            string           `json:"stage_run_id,omitempty"`
	InputWorkspaceVersion int64            `json:"input_workspace_version"`
	InputCharterID        string           `json:"input_charter_id,omitempty"`
	InputHeadSHA          string           `json:"input_head_sha,omitempty"`
	InputDigest           string           `json:"input_digest"`
	Input                 json.RawMessage  `json:"input"`
	LeaseOwner            string           `json:"lease_owner,omitempty"`
	LeaseToken            string           `json:"lease_token,omitempty"`
	LeaseUntil            *time.Time       `json:"lease_until,omitempty"`
	Attempts              int              `json:"attempts"`
	AvailableAt           time.Time        `json:"available_at"`
	Result                json.RawMessage  `json:"result,omitempty"`
	ResultDigest          string           `json:"result_digest,omitempty"`
	PublicErrorCode       string           `json:"public_error_code,omitempty"`
	InternalError         string           `json:"internal_error,omitempty"`
}

type PRIngressWatermark struct {
	PRWorkspaceRecord
	Source          string    `json:"source"`
	Connector       string    `json:"connector"`
	InboxReceivedAt time.Time `json:"inbox_received_at"`
	InboxEventID    string    `json:"inbox_event_id"`
}

type PRIngressCutoverWatermark struct {
	Source          string    `json:"source"`
	Connector       string    `json:"connector"`
	InboxReceivedAt time.Time `json:"inbox_received_at"`
	InboxEventID    string    `json:"inbox_event_id"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type PRActivity struct {
	PRWorkspaceRecord
	Kind     string         `json:"kind"`
	Actor    string         `json:"actor"`
	Summary  string         `json:"summary"`
	EntityID string         `json:"entity_id,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// PRWorkspaceAggregate is loaded in one database read transaction.
type PRWorkspaceAggregate struct {
	Workspace         PRWorkspace                  `json:"workspace"`
	ProviderSnapshot  PRProviderSnapshot           `json:"provider_snapshot"`
	ProviderSnapshots []PRProviderSnapshot         `json:"provider_snapshots"`
	Charters          []PRCharterRevision          `json:"charters"`
	StageRuns         []PRStageRun                 `json:"stage_runs"`
	Findings          []PRFinding                  `json:"findings"`
	FindingEvents     []PRFindingEvent             `json:"finding_events"`
	Conversations     []PRConversation             `json:"conversations"`
	Messages          []PRMessage                  `json:"messages"`
	Corrections       []PRCorrection               `json:"corrections"`
	RepositoryLessons []PRRepositoryLesson         `json:"repository_lessons"`
	NudgeRounds       []PRNudgeRound               `json:"nudge_rounds"`
	NudgeRewards      []PRNudgeReward              `json:"nudge_rewards"`
	DeferredGroups    []PRDeferredGroup            `json:"deferred_groups"`
	RepairAttempts    []PRRepairAttempt            `json:"repair_attempts"`
	ValidationRuns    []PRValidationRun            `json:"validation_runs"`
	GateRuns          []PRGateRun                  `json:"gate_runs"`
	Publications      []PRPublication              `json:"publications"`
	OperationIntents  []PRWorkspaceOperationIntent `json:"operation_intents"`
	IngressWatermarks []PRIngressWatermark         `json:"ingress_watermarks"`
	Activity          []PRActivity                 `json:"activity"`
}

type PRWorkspaceCreate struct {
	RequestID      string             `json:"request_id"`
	WorkspaceID    string             `json:"workspace_id,omitempty"`
	Provider       PRProviderSnapshot `json:"provider_snapshot"`
	Phase          PRWorkspacePhase   `json:"phase,omitempty"`
	ExecutionState PRExecutionState   `json:"execution_state,omitempty"`
}

type PRWorkspaceCursor struct {
	UpdatedAt time.Time `json:"updated_at"`
	ID        string    `json:"id"`
}

type PRWorkspaceFilter struct {
	ProviderOrigin string
	RepositoryID   string
	Repository     string
	Phase          PRWorkspacePhase
	ExecutionState PRExecutionState
	OwnedOnly      *bool
	HeadWritable   *bool
	NeedsAction    *bool
	After          *PRWorkspaceCursor
	Limit          int
}

type PRWorkspacePage struct {
	Workspaces []PRWorkspace      `json:"workspaces"`
	Next       *PRWorkspaceCursor `json:"next,omitempty"`
}

type PRWorkspaceMutationKind string

const (
	PRMutationWorkspaceState    PRWorkspaceMutationKind = "workspace_state"
	PRMutationProviderSnapshot  PRWorkspaceMutationKind = "provider_snapshot"
	PRMutationCharter           PRWorkspaceMutationKind = "charter"
	PRMutationStageRun          PRWorkspaceMutationKind = "stage_run"
	PRMutationFinding           PRWorkspaceMutationKind = "finding"
	PRMutationFindingEvent      PRWorkspaceMutationKind = "finding_event"
	PRMutationConversation      PRWorkspaceMutationKind = "conversation"
	PRMutationMessage           PRWorkspaceMutationKind = "message"
	PRMutationCorrection        PRWorkspaceMutationKind = "correction"
	PRMutationRepositoryLesson  PRWorkspaceMutationKind = "repository_lesson"
	PRMutationNudgeRound        PRWorkspaceMutationKind = "nudge_round"
	PRMutationNudgeReward       PRWorkspaceMutationKind = "nudge_reward"
	PRMutationDeferredGroup     PRWorkspaceMutationKind = "deferred_group"
	PRMutationDeferredGroupItem PRWorkspaceMutationKind = "deferred_group_item"
	PRMutationRepairAttempt     PRWorkspaceMutationKind = "repair_attempt"
	PRMutationValidationRun     PRWorkspaceMutationKind = "validation_run"
	PRMutationGateRun           PRWorkspaceMutationKind = "gate_run"
	PRMutationPublication       PRWorkspaceMutationKind = "publication"
	PRMutationOperationIntent   PRWorkspaceMutationKind = "operation_intent"
	PRMutationIngressWatermark  PRWorkspaceMutationKind = "ingress_watermark"
	PRMutationActivity          PRWorkspaceMutationKind = "activity"
)

type PRWorkspaceStateChange struct {
	Phase          PRWorkspacePhase `json:"phase"`
	ExecutionState PRExecutionState `json:"execution_state"`
}

type PRWorkspaceMutation struct {
	WorkspaceID     string                  `json:"workspace_id"`
	ExpectedVersion int64                   `json:"expected_version"`
	RequestID       string                  `json:"request_id"`
	Kind            PRWorkspaceMutationKind `json:"kind"`
	Payload         json.RawMessage         `json:"payload"`
}

type PRWorkspaceMutationResult struct {
	WorkspaceID      string                  `json:"workspace_id"`
	WorkspaceVersion int64                   `json:"workspace_version"`
	RequestID        string                  `json:"request_id"`
	Kind             PRWorkspaceMutationKind `json:"kind"`
	EntityID         string                  `json:"entity_id"`
	Created          bool                    `json:"created"`
	AppliedAt        time.Time               `json:"applied_at"`
}

// PRWorkspacePatch applies a complete lifecycle transition atomically. Every
// child record carries its domain-assigned stable ID, including appends. Only
// provider snapshots are store-assigned because they are eventing observations.
type PRWorkspacePatch struct {
	Phase                   *PRWorkspacePhase            `json:"phase,omitempty"`
	ExecutionState          *PRExecutionState            `json:"execution_state,omitempty"`
	ActiveCharterID         *string                      `json:"active_charter_id,omitempty"`
	ProviderSnapshot        *PRProviderSnapshot          `json:"provider_snapshot,omitempty"`
	AppendCharters          []PRCharterRevision          `json:"append_charters,omitempty"`
	ReplaceCharters         []PRCharterRevision          `json:"replace_charters,omitempty"`
	AppendStageRuns         []PRStageRun                 `json:"append_stage_runs,omitempty"`
	ReplaceStageRuns        []PRStageRun                 `json:"replace_stage_runs,omitempty"`
	UpsertFindings          []PRFinding                  `json:"upsert_findings,omitempty"`
	AppendFindingEvents     []PRFindingEvent             `json:"append_finding_events,omitempty"`
	AppendConversations     []PRConversation             `json:"append_conversations,omitempty"`
	ReplaceConversations    []PRConversation             `json:"replace_conversations,omitempty"`
	AppendMessages          []PRMessage                  `json:"append_messages,omitempty"`
	AppendCorrections       []PRCorrection               `json:"append_corrections,omitempty"`
	ReplaceCorrections      []PRCorrection               `json:"replace_corrections,omitempty"`
	AppendLessons           []PRRepositoryLesson         `json:"append_lessons,omitempty"`
	ReplaceLessons          []PRRepositoryLesson         `json:"replace_lessons,omitempty"`
	AppendNudgeRounds       []PRNudgeRound               `json:"append_nudge_rounds,omitempty"`
	ReplaceNudgeRounds      []PRNudgeRound               `json:"replace_nudge_rounds,omitempty"`
	AppendNudgeRewards      []PRNudgeReward              `json:"append_nudge_rewards,omitempty"`
	UpsertDeferredGroups    []PRDeferredGroup            `json:"upsert_deferred_groups,omitempty"`
	UpsertDeferredItems     []PRDeferredGroupItem        `json:"upsert_deferred_items,omitempty"`
	AppendRepairAttempts    []PRRepairAttempt            `json:"append_repair_attempts,omitempty"`
	ReplaceRepairAttempts   []PRRepairAttempt            `json:"replace_repair_attempts,omitempty"`
	AppendValidationRuns    []PRValidationRun            `json:"append_validation_runs,omitempty"`
	ReplaceValidationRuns   []PRValidationRun            `json:"replace_validation_runs,omitempty"`
	AppendGateRuns          []PRGateRun                  `json:"append_gate_runs,omitempty"`
	ReplaceGateRuns         []PRGateRun                  `json:"replace_gate_runs,omitempty"`
	AppendPublications      []PRPublication              `json:"append_publications,omitempty"`
	ReplacePublications     []PRPublication              `json:"replace_publications,omitempty"`
	AppendOperationIntents  []PRWorkspaceOperationIntent `json:"append_operation_intents,omitempty"`
	ReplaceOperationIntents []PRWorkspaceOperationIntent `json:"replace_operation_intents,omitempty"`
	UpsertIngressWatermarks []PRIngressWatermark         `json:"upsert_ingress_watermarks,omitempty"`
	AppendActivity          []PRActivity                 `json:"append_activity,omitempty"`
}

type PRWorkspacePatchMutation struct {
	WorkspaceID     string           `json:"workspace_id"`
	ExpectedVersion int64            `json:"expected_version"`
	RequestID       string           `json:"request_id"`
	Patch           PRWorkspacePatch `json:"patch"`
}

type PRWorkspacePatchResult struct {
	Aggregate PRWorkspaceAggregate `json:"aggregate"`
	Replayed  bool                 `json:"replayed"`
}

// PRWorkspaceStore owns durable aggregate capture, inspection, and CAS
// mutations. Request IDs are globally unique and replay their original result.
type PRWorkspaceStore interface {
	CreatePRWorkspace(context.Context, PRWorkspaceCreate) (PRWorkspaceAggregate, bool, error)
	GetPRWorkspace(context.Context, string) (PRWorkspaceAggregate, error)
	ListPRWorkspaces(context.Context, PRWorkspaceFilter) (PRWorkspacePage, error)
	ApplyPRWorkspaceMutation(context.Context, PRWorkspaceMutation) (PRWorkspaceMutationResult, error)
	ApplyPRWorkspacePatch(context.Context, PRWorkspacePatchMutation) (PRWorkspacePatchResult, error)
}

type PRWorkspaceCutoverStore interface {
	SetPRWorkspaceIngressCutover(context.Context, PRIngressCutoverWatermark) error
	GetPRWorkspaceIngressCutover(context.Context, string, string) (PRIngressCutoverWatermark, error)
}

// PRWorkspaceClaimRequest controls a bounded worker claim. LeaseDuration is
// fenced to a short interval by the store; expired claims are recoverable by a
// later worker.
type PRWorkspaceClaimRequest struct {
	WorkerID      string        `json:"worker_id"`
	Limit         int           `json:"limit"`
	LeaseDuration time.Duration `json:"lease_duration"`
}

type PRClaimedOperationIntent struct {
	Intent           PRWorkspaceOperationIntent `json:"intent"`
	WorkspaceVersion int64                      `json:"workspace_version"`
}

type PRWorkspaceOperationFinish struct {
	IntentID        string           `json:"intent_id"`
	LeaseToken      string           `json:"lease_token"`
	State           PRExecutionState `json:"state"`
	Result          json.RawMessage  `json:"result,omitempty"`
	ResultDigest    string           `json:"result_digest,omitempty"`
	PublicErrorCode string           `json:"public_error_code,omitempty"`
	InternalError   string           `json:"internal_error,omitempty"`
}

type PRClaimedPublication struct {
	Publication      PRPublication `json:"publication"`
	WorkspaceVersion int64         `json:"workspace_version"`
}

type PRWorkspacePublicationFinish struct {
	PublicationID   string              `json:"publication_id"`
	LeaseToken      string              `json:"lease_token"`
	Status          PRPublicationStatus `json:"status"`
	ExternalID      string              `json:"external_id,omitempty"`
	ExternalURL     string              `json:"external_url,omitempty"`
	PublicErrorCode string              `json:"public_error_code,omitempty"`
	InternalError   string              `json:"internal_error,omitempty"`
	PublishedAt     *time.Time          `json:"published_at,omitempty"`
}

// PRWorkspaceWorkerStore exposes atomic cross-workspace claims and
// lease-token-fenced completion for durable operations and publications.
type PRWorkspaceWorkerStore interface {
	ClaimPRWorkspaceOperations(context.Context, PRWorkspaceClaimRequest) ([]PRClaimedOperationIntent, error)
	FinishPRWorkspaceOperation(context.Context, PRWorkspaceOperationFinish) (PRClaimedOperationIntent, error)
	ClaimPRWorkspacePublications(context.Context, PRWorkspaceClaimRequest) ([]PRClaimedPublication, error)
	FinishPRWorkspacePublication(context.Context, PRWorkspacePublicationFinish) (PRClaimedPublication, error)
}
