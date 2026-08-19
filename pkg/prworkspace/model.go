package prworkspace

import (
	"encoding/json"
	"time"

	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

// PRType is the single primary change type authorized by a confirmed charter.
type PRType string

const (
	PRTypeFix           PRType = "fix"
	PRTypeRefactor      PRType = "refactor"
	PRTypeFeature       PRType = "feature"
	PRTypeDocumentation PRType = "documentation"
	PRTypeTest          PRType = "test"
)

// Phase is the current durable lifecycle phase of one pull request.
type Phase string

const (
	PhaseIntake          Phase = "intake"
	PhaseCharter         Phase = "charter"
	PhaseReview          Phase = "review"
	PhaseTriage          Phase = "triage"
	PhaseImplementation  Phase = "implementation"
	PhaseValidation      Phase = "validation"
	PhaseCompletionAudit Phase = "completion_audit"
	PhasePublication     Phase = "publication"
	PhaseComplete        Phase = "complete"
)

// ExecutionState is shared by lifecycle operations that can be recovered.
type ExecutionState string

const (
	ExecutionQueued      ExecutionState = "queued"
	ExecutionRunning     ExecutionState = "running"
	ExecutionWaitingGate ExecutionState = "waiting_gate"
	ExecutionWaitingUser ExecutionState = "waiting_user"
	ExecutionSucceeded   ExecutionState = "succeeded"
	ExecutionFailed      ExecutionState = "failed"
	ExecutionBlocked     ExecutionState = "blocked"
	ExecutionCanceled    ExecutionState = "canceled"
	ExecutionStale       ExecutionState = "stale"
	ExecutionUnknown     ExecutionState = "unknown"
)

// FindingDisposition determines whether a finding belongs in this PR.
type FindingDisposition string

const (
	FindingOpen      FindingDisposition = "open"
	FindingInScope   FindingDisposition = "in_scope"
	FindingFixed     FindingDisposition = "fixed"
	FindingDeferred  FindingDisposition = "deferred"
	FindingDismissed FindingDisposition = "dismissed"
)

// ScopeDistance grades semantic distance from the confirmed charter.
type ScopeDistance string

const (
	ScopeExact             ScopeDistance = "S0_exact"
	ScopeNecessaryAdjacent ScopeDistance = "S1_necessary_adjacent"
	ScopeRelatedFollowup   ScopeDistance = "S2_related_followup"
	ScopeUnrelated         ScopeDistance = "S3_unrelated"
)

// ChangeSize grades the amount of semantic code affected by a change.
type ChangeSize string

const (
	ChangeSizeXS ChangeSize = "XS"
	ChangeSizeS  ChangeSize = "S"
	ChangeSizeM  ChangeSize = "M"
	ChangeSizeL  ChangeSize = "L"
)

// WorkPresence distinguishes code that is already part of the exact candidate
// from work that exists only as a proposed follow-up. This distinction is
// security-sensitive: deferring a candidate-present change does not remove it
// from the pull request and therefore cannot make the candidate publishable.
type WorkPresence string

const (
	WorkCandidatePresent WorkPresence = "candidate_present"
	WorkFollowUp         WorkPresence = "follow_up"
)

type FindingOrigin string

const (
	FindingOriginReview         FindingOrigin = "review"
	FindingOriginImplementation FindingOrigin = "implementation"
	FindingOriginNudge          FindingOrigin = "nudge"
	FindingOriginUser           FindingOrigin = "user"
)

type CorrectionKind string

const (
	CorrectionFactual              CorrectionKind = "factual"
	CorrectionFindingQuality       CorrectionKind = "finding_quality"
	CorrectionScope                CorrectionKind = "scope"
	CorrectionPRType               CorrectionKind = "pr_type"
	CorrectionImplementation       CorrectionKind = "implementation"
	CorrectionValidation           CorrectionKind = "validation"
	CorrectionRepositoryPreference CorrectionKind = "repository_preference"
)

type CorrectionApplicability string

const (
	CorrectionReviewOnly         CorrectionApplicability = "review"
	CorrectionImplementationOnly CorrectionApplicability = "implementation"
	CorrectionReviewAndImpl      CorrectionApplicability = "both"
)

// ProviderSnapshot contains verified provider authority. Mutable names are
// display fields; numeric provider IDs are the workspace identity.
type ProviderSnapshot struct {
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

// Charter is one immutable confirmed or editable draft revision.
type Charter struct {
	ID                 string     `json:"id"`
	Revision           int64      `json:"revision"`
	Type               PRType     `json:"type"`
	Goal               string     `json:"goal"`
	AcceptanceCriteria []string   `json:"acceptance_criteria"`
	IncludedAreas      []string   `json:"included_areas"`
	ExcludedAreas      []string   `json:"excluded_areas"`
	NonGoals           []string   `json:"non_goals"`
	BaseSHA            string     `json:"base_sha"`
	HeadSHA            string     `json:"head_sha"`
	Confirmed          bool       `json:"confirmed"`
	CreatedAt          time.Time  `json:"created_at"`
	ConfirmedAt        *time.Time `json:"confirmed_at,omitempty"`
}

// ScopeAssessment preserves both the AI classification and raw measurements.
type ScopeAssessment struct {
	Distance       ScopeDistance `json:"distance"`
	Size           ChangeSize    `json:"size"`
	Presence       WorkPresence  `json:"presence,omitempty"`
	Files          int           `json:"files"`
	SemanticLines  int           `json:"semantic_lines"`
	Modules        int           `json:"modules"`
	Estimated      bool          `json:"estimated"`
	TypeCompatible bool          `json:"type_compatible"`
	Confidence     float64       `json:"confidence"`
	CharterClauses []string      `json:"charter_clauses,omitempty"`
	Explanation    string        `json:"explanation,omitempty"`
	ChangeEvidence []ScopeChange `json:"change_evidence,omitempty"`
}

// ScopeChange is path/hunk-level evidence produced by the isolated scope
// auditor. Aggregate scope fields above are conservative rollups of these
// records and the deterministic candidate metrics.
type ScopeChange struct {
	Path           string        `json:"path"`
	Hunk           string        `json:"hunk"`
	Module         string        `json:"module"`
	SemanticLines  int           `json:"semantic_lines"`
	Presence       WorkPresence  `json:"presence"`
	Distance       ScopeDistance `json:"scope_distance"`
	Size           ChangeSize    `json:"change_size"`
	TypeCompatible bool          `json:"type_compatible"`
	Confidence     float64       `json:"confidence"`
	CharterClauses []string      `json:"charter_clauses,omitempty"`
	Explanation    string        `json:"explanation"`
}

type Finding struct {
	ID              string             `json:"id"`
	Fingerprint     string             `json:"fingerprint"`
	Origin          FindingOrigin      `json:"origin"`
	OriginRunID     string             `json:"origin_run_id,omitempty"`
	NudgeRoundID    string             `json:"nudge_round_id,omitempty"`
	Severity        string             `json:"severity"`
	Title           string             `json:"title"`
	File            string             `json:"file,omitempty"`
	Line            *int               `json:"line,omitempty"`
	Message         string             `json:"message"`
	Evidence        string             `json:"evidence,omitempty"`
	Impact          string             `json:"impact,omitempty"`
	Recommendation  string             `json:"recommendation,omitempty"`
	Validation      string             `json:"validation,omitempty"`
	Scope           ScopeAssessment    `json:"scope"`
	Disposition     FindingDisposition `json:"disposition"`
	NudgeReward     *float64           `json:"nudge_reward,omitempty"`
	RewardSource    string             `json:"reward_source,omitempty"`
	SourceAvailable bool               `json:"source_available,omitempty"`
	Version         int64              `json:"version"`
	CreatedAt       time.Time          `json:"created_at"`
	UpdatedAt       time.Time          `json:"updated_at"`
	source          *AIExecutionSource
}

// AIExecutionSource is private durable provenance for one finding-producing
// AI execution. Raw session capabilities never enter the PR workspace HTTP
// projection.
type AIExecutionSource struct {
	ExecutionID     string `json:"execution-id"`
	WorkspaceID     string `json:"workspace-id"`
	Binding         string `json:"binding"`
	AgentID         string `json:"agent-id"`
	Session         string `json:"session"`
	SessionRevision string `json:"session-revision"`
	Tools           string `json:"tools"`
}

type Correction struct {
	ID            string                  `json:"id"`
	Kind          CorrectionKind          `json:"kind"`
	Applicability CorrectionApplicability `json:"applicability"`
	TargetType    string                  `json:"target_type"`
	TargetID      string                  `json:"target_id"`
	OriginalClaim string                  `json:"original_claim"`
	Correction    string                  `json:"correction"`
	Evidence      string                  `json:"evidence,omitempty"`
	CharterID     string                  `json:"charter_id"`
	HeadSHA       string                  `json:"head_sha"`
	SupersedesID  string                  `json:"supersedes_id,omitempty"`
	Promoted      bool                    `json:"promoted"`
	CreatedAt     time.Time               `json:"created_at"`
}

type RepositoryLesson struct {
	ID            string                  `json:"id"`
	RepositoryID  string                  `json:"repository_id"`
	SourcePR      string                  `json:"source_workspace_id"`
	CorrectionID  string                  `json:"correction_id"`
	Kind          CorrectionKind          `json:"kind"`
	Applicability CorrectionApplicability `json:"applicability"`
	PRType        PRType                  `json:"pr_type,omitempty"`
	Text          string                  `json:"text"`
	Active        bool                    `json:"active"`
	CreatedAt     time.Time               `json:"created_at"`
	RevokedAt     *time.Time              `json:"revoked_at,omitempty"`
}

type Coverage struct {
	ReviewedAreas   []string `json:"reviewed_areas"`
	UnreviewedAreas []string `json:"unreviewed_areas"`
	TestsConsidered []string `json:"tests_considered"`
	ResidualRisks   []string `json:"residual_risks"`
}

type StageEvidence struct {
	Stage        string         `json:"stage"`
	RunID        string         `json:"run_id"`
	Summary      string         `json:"summary"`
	Coverage     Coverage       `json:"coverage"`
	FindingIDs   []string       `json:"finding_ids"`
	Validation   map[string]any `json:"validation,omitempty"`
	PromptDigest string         `json:"prompt_digest"`
	CreatedAt    time.Time      `json:"created_at"`
}

type DeferredGroup struct {
	ID                    string          `json:"id"`
	Title                 string          `json:"title"`
	Body                  string          `json:"body"`
	FindingIDs            []string        `json:"finding_ids"`
	Scope                 ScopeAssessment `json:"scope"`
	Labels                []string        `json:"labels,omitempty"`
	ExistingIssueURL      string          `json:"existing_issue_url,omitempty"`
	PublicationID         string          `json:"publication_id,omitempty"`
	PublicationSuppressed bool            `json:"publication_suppressed,omitempty"`
	SuppressionReason     string          `json:"suppression_reason,omitempty"`
	Version               int64           `json:"version"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
}

type Workspace struct {
	ID              string         `json:"id"`
	Provider        string         `json:"provider"`
	ProviderOrigin  string         `json:"provider_origin"`
	RepositoryID    string         `json:"repository_id"`
	PullRequestID   string         `json:"pull_request_id"`
	Repository      string         `json:"repository"`
	PullNumber      int64          `json:"pull_number"`
	Phase           Phase          `json:"phase"`
	ExecutionState  ExecutionState `json:"execution_state"`
	ActiveCharterID string         `json:"active_charter_id,omitempty"`
	ProviderHeadSHA string         `json:"provider_head_sha"`
	Version         int64          `json:"version"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

type StageRun struct {
	ID           string         `json:"id"`
	Stage        string         `json:"stage"`
	State        ExecutionState `json:"state"`
	CharterID    string         `json:"charter_id"`
	HeadSHA      string         `json:"head_sha"`
	Attempt      int            `json:"attempt"`
	PromptDigest string         `json:"prompt_digest,omitempty"`
	Summary      string         `json:"summary,omitempty"`
	Evidence     *StageEvidence `json:"evidence,omitempty"`
	PublicError  string         `json:"public_error,omitempty"`
	StartedAt    time.Time      `json:"started_at"`
	FinishedAt   *time.Time     `json:"finished_at,omitempty"`
	// inputWorkspaceVersion is restored by the production adapter so later
	// state replacements retain the immutable CAS input that started the run.
	inputWorkspaceVersion int64
}

type Message struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Stage     string    `json:"stage,omitempty"`
	Content   string    `json:"content"`
	CharterID string    `json:"charter_id,omitempty"`
	HeadSHA   string    `json:"head_sha,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type NudgeRoundRecord struct {
	ID               string         `json:"id"`
	StageRunID       string         `json:"stage_run_id"`
	Stage            NudgeStage     `json:"stage"`
	Round            int            `json:"round"`
	MinimumRounds    int            `json:"minimum_rounds"`
	HardCap          int            `json:"hard_cap"`
	Strategy         NudgeStrategy  `json:"strategy"`
	Challenge        string         `json:"challenge"`
	VariantDigest    string         `json:"variant_digest"`
	PromptDigest     string         `json:"prompt_digest"`
	State            ExecutionState `json:"state"`
	PublicError      string         `json:"public_error,omitempty"`
	NovelFindings    int            `json:"novel_findings"`
	DuplicateCount   int            `json:"duplicate_count"`
	FindingIDs       []string       `json:"finding_ids,omitempty"`
	ResolvedFindings int            `json:"resolved_findings"`
	Reward           *float64       `json:"reward,omitempty"`
	RewardProvenance string         `json:"reward_provenance,omitempty"`
	CreatedAt        time.Time      `json:"created_at"`
}

type RepairAttempt struct {
	ID                string                          `json:"id"`
	StageRunID        string                          `json:"stage_run_id"`
	Number            int                             `json:"number"`
	State             ExecutionState                  `json:"state"`
	Instruction       string                          `json:"instruction"`
	WorkspaceID       string                          `json:"workspace_id,omitempty"`
	ResultSummary     string                          `json:"result_summary,omitempty"`
	ChangedFiles      []string                        `json:"changed_files,omitempty"`
	FindingIDs        []string                        `json:"finding_ids,omitempty"`
	CandidateSHA      string                          `json:"candidate_sha,omitempty"`
	Scope             ScopeAssessment                 `json:"scope"`
	PromptDigest      string                          `json:"prompt_digest"`
	ScopePromptDigest string                          `json:"scope_prompt_digest"`
	StartedAt         time.Time                       `json:"started_at"`
	FinishedAt        *time.Time                      `json:"finished_at,omitempty"`
	PublicationFence  *ImplementationPublicationFence `json:"-"`
}

// ImplementationPublicationFence is private, durable evidence needed to
// publish or reconcile one exact validated commit. It is persisted by the
// production store but deliberately omitted from HTTP/JSON projections.
type ImplementationPublicationFence struct {
	GitWorkspaceID string `json:"git_workspace_id"`
	LineID         string `json:"line_id"`
	LineVersion    int64  `json:"line_version"`
	MutationEpoch  int64  `json:"mutation_epoch"`
	ParkIntentID   string `json:"park_intent_id"`
	BaseCommit     string `json:"base_commit"`
	Tip            string `json:"tip"`
	Tree           string `json:"tree"`
}

type ValidationCheck struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Summary    string `json:"summary,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

type ValidationRun struct {
	ID           string            `json:"id"`
	StageRunID   string            `json:"stage_run_id"`
	State        ExecutionState    `json:"state"`
	CandidateSHA string            `json:"candidate_sha"`
	Checks       []ValidationCheck `json:"checks"`
	StartedAt    time.Time         `json:"started_at"`
	FinishedAt   *time.Time        `json:"finished_at,omitempty"`
}

type GateTurn struct {
	StageID        string         `json:"stage_id"`
	Kind           string         `json:"kind"`
	Title          string         `json:"title"`
	Status         string         `json:"status"`
	GateForm       *GateForm      `json:"gate-form,omitempty"`
	FieldValues    map[string]any `json:"field-values,omitempty"`
	ActorKind      string         `json:"actor-kind,omitempty"`
	ExecutionID    string         `json:"execution-id,omitempty"`
	ActionRevision string         `json:"action-revision,omitempty"`
	InputHash      string         `json:"input-hash,omitempty"`
}

// GateForm is the generic, browser-safe request defined by an application
// workflow. Its fields have no lifecycle-specific result semantics.
type GateForm struct {
	GateRef string                `json:"gate-ref"`
	Prompt  string                `json:"prompt"`
	Fields  []gatetypes.GateField `json:"fields,omitempty"`
}

type GateRun struct {
	ID              string         `json:"id"`
	DecisionPoint   string         `json:"decision_point"`
	TargetID        string         `json:"target_id,omitempty"`
	State           ExecutionState `json:"state"`
	PolicyRevision  string         `json:"policy_revision"`
	SubjectRevision string         `json:"subject_revision"`
	Turns           []GateTurn     `json:"turns"`
	Evidence        GateEvidence   `json:"evidence,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	FinishedAt      *time.Time     `json:"finished_at,omitempty"`
	runtime         *gateRuntime
}

// GateEvidence is the deliberately allowlisted browser projection of a gate's
// immutable private subject. Raw prompts, policies, diffs, and provider data
// remain private.
type GateEvidence struct {
	CharterType         PRType                `json:"charter_type,omitempty"`
	CharterGoal         string                `json:"charter_goal,omitempty"`
	CandidateSHA        string                `json:"candidate_sha,omitempty"`
	ChangedFiles        []string              `json:"changed_files,omitempty"`
	Scope               *ScopeAssessment      `json:"scope,omitempty"`
	HardScope           bool                  `json:"hard_scope,omitempty"`
	HardScopeFindingIDs []string              `json:"hard_scope_finding_ids,omitempty"`
	ScopeResolutionIDs  []string              `json:"scope-resolution-finding-ids,omitempty"`
	ValidationState     ExecutionState        `json:"validation_state,omitempty"`
	ValidationChecks    []ValidationCheck     `json:"validation_checks,omitempty"`
	FindingIDs          []string              `json:"finding_ids,omitempty"`
	FindingCount        int                   `json:"finding_count,omitempty"`
	PublicationKind     PublicationKind       `json:"publication_kind,omitempty"`
	PayloadDigest       string                `json:"payload_digest,omitempty"`
	ExpectedHeadSHA     string                `json:"expected_head_sha,omitempty"`
	ProviderRevision    string                `json:"provider_revision,omitempty"`
	Repository          string                `json:"repository,omitempty"`
	ReviewSummary       string                `json:"review_summary,omitempty"`
	PublicationFindings []GateFindingEvidence `json:"publication_findings,omitempty"`
	IssueTitle          string                `json:"issue_title,omitempty"`
	IssueBody           string                `json:"issue_body,omitempty"`
	IssueLabels         []string              `json:"issue_labels,omitempty"`
	RepairSummary       string                `json:"repair_summary,omitempty"`
}

// GateFindingEvidence is the exact safe subset rendered by the GitHub review
// publisher. Internal prompts, rewards, and model-only evidence are excluded.
type GateFindingEvidence struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	File    string `json:"file,omitempty"`
	Line    *int   `json:"line,omitempty"`
	Message string `json:"message"`
}

// gateRuntime is persisted by the production adapter but never projected to
// the browser. It pins the exact private policy/subject and workflow cursor
// needed to resume a staged human gate after restart.
type gateRuntime struct {
	WorkflowConfigurationID string
	WorkflowRunID           string
	PinnedPolicy            json.RawMessage
	PinnedSubject           json.RawMessage
}

type PublicationKind string

const (
	PublicationGitHubReview PublicationKind = "github_review"
	PublicationBranchPush   PublicationKind = "branch_push"
	PublicationGitHubIssue  PublicationKind = "github_issue"
)

type Publication struct {
	ID              string          `json:"id"`
	Kind            PublicationKind `json:"kind"`
	State           ExecutionState  `json:"state"`
	TargetID        string          `json:"target_id,omitempty"`
	FindingIDs      []string        `json:"finding_ids,omitempty"`
	ExpectedHeadSHA string          `json:"expected_head_sha,omitempty"`
	PayloadDigest   string          `json:"payload_digest"`
	ExternalID      string          `json:"external_id,omitempty"`
	ExternalURL     string          `json:"external_url,omitempty"`
	PublicErrorCode string          `json:"public_error_code,omitempty"`
	Attempts        int             `json:"attempts"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	PublishedAt     *time.Time      `json:"published_at,omitempty"`
	// payload is the exact authorization-bound external request. It is durable
	// but never exposed through the browser aggregate.
	payload json.RawMessage
}

type Activity struct {
	Ordinal   int64          `json:"ordinal"`
	Kind      string         `json:"kind"`
	Actor     string         `json:"actor"`
	Summary   string         `json:"summary"`
	EntityID  string         `json:"entity_id,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type Aggregate struct {
	Workspace         Workspace          `json:"workspace"`
	ProviderSnapshot  ProviderSnapshot   `json:"provider_snapshot"`
	Charters          []Charter          `json:"charters"`
	StageRuns         []StageRun         `json:"stage_runs"`
	Findings          []Finding          `json:"findings"`
	Messages          []Message          `json:"messages"`
	Corrections       []Correction       `json:"corrections"`
	RepositoryLessons []RepositoryLesson `json:"repository_lessons"`
	NudgeRounds       []NudgeRoundRecord `json:"nudge_rounds"`
	DeferredGroups    []DeferredGroup    `json:"deferred_groups"`
	RepairAttempts    []RepairAttempt    `json:"repair_attempts"`
	ValidationRuns    []ValidationRun    `json:"validation_runs"`
	Gates             []GateRun          `json:"gates"`
	Publications      []Publication      `json:"publications"`
	Activity          []Activity         `json:"activity"`
}

func (aggregate Aggregate) ActiveCharter() (Charter, bool) {
	for index := len(aggregate.Charters) - 1; index >= 0; index-- {
		if aggregate.Charters[index].ID == aggregate.Workspace.ActiveCharterID {
			return aggregate.Charters[index], true
		}
	}
	return Charter{}, false
}
