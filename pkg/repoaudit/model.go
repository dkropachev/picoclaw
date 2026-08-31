package repoaudit

import "time"

const SchemaVersion = 5

type FileRef struct {
	Path      string `json:"path"`
	BlobSHA   string `json:"blob_sha"`
	SizeBytes int64  `json:"size_bytes"`
	Category  string `json:"category,omitempty"`
	Mode      string `json:"mode,omitempty"`
}

type ReviewedFile struct {
	FileRef
	CommitSHA       string    `json:"commit_sha"`
	ProfileHash     string    `json:"profile_hash"`
	ForceCampaignID string    `json:"force_campaign_id,omitempty"`
	RunID           string    `json:"run_id"`
	ReviewedAt      time.Time `json:"reviewed_at"`
}

type UnsupportedFile struct {
	FileRef
	CommitSHA       string    `json:"commit_sha"`
	ProfileHash     string    `json:"profile_hash"`
	ForceCampaignID string    `json:"force_campaign_id,omitempty"`
	Reason          string    `json:"reason"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// RepositoryReviewFileAttribution records one successful review child and the
// exact files it acknowledged. Records are append-only evidence: current
// campaign coverage may move forward, while attribution remains tied to the
// immutable run, assignment, source, and child index that produced it.
type RepositoryReviewFileAttribution struct {
	ID                string                                `json:"id"`
	AutomationID      string                                `json:"automation_id"`
	RunID             string                                `json:"run_id"`
	CommitSHA         string                                `json:"commit_sha"`
	InventoryHash     string                                `json:"inventory_hash"`
	ProfileHash       string                                `json:"profile_hash"`
	AssignmentID      string                                `json:"assignment_id"`
	FocusID           string                                `json:"focus_id"`
	RootAgentID       string                                `json:"root_agent_id"`
	ReviewerIdentity  string                                `json:"reviewer_identity"`
	Model             string                                `json:"model"`
	ModelAlias        string                                `json:"model_alias"`
	Account           string                                `json:"account"`
	UsageModel        string                                `json:"usage_model,omitempty"`
	AcknowledgedFiles []FileRef                             `json:"acknowledged_files"`
	EvidenceDigest    string                                `json:"evidence_digest"`
	Source            RepositoryReviewFileAttributionSource `json:"source"`
	ChildIndex        int                                   `json:"child_index"`
	Required          bool                                  `json:"required"`
	CompletedAt       time.Time                             `json:"completed_at"`
}

type Plan struct {
	ID                      string                           `json:"id"`
	CampaignID              string                           `json:"campaign_id,omitempty"`
	Repository              string                           `json:"repository"`
	CommitSHA               string                           `json:"commit_sha"`
	InventoryHash           string                           `json:"inventory_hash"`
	ProfileHash             string                           `json:"profile_hash"`
	RequiredAssignments     int                              `json:"required_assignments,omitempty"`
	AssignmentCatalog       []RepositoryReviewAssignment     `json:"assignment_catalog,omitempty"`
	AssignmentPlans         []RepositoryReviewAssignmentPlan `json:"assignment_plans,omitempty"`
	ForceCampaignID         string                           `json:"force_campaign_id,omitempty"`
	Authoritative           bool                             `json:"authoritative,omitempty"`
	TargetBranch            string                           `json:"target_branch,omitempty"`
	AdvertisedDefaultBranch string                           `json:"advertised_default_branch,omitempty"`
	TargetIsDefault         bool                             `json:"target_is_default"`
	StateVersion            int64                            `json:"state_version"`
	PendingFiles            []FileRef                        `json:"pending_files"`
	DeferredFiles           []FileRef                        `json:"deferred_files,omitempty"`
	UnchangedFiles          []FileRef                        `json:"unchanged_files"`
	UnsupportedFiles        []UnsupportedFile                `json:"unsupported_files,omitempty"`
	PreviouslyReviewed      int                              `json:"previously_reviewed"`
	CreatedAt               time.Time                        `json:"created_at"`
}

type Validation struct {
	Status  string   `json:"status"`
	Summary string   `json:"summary"`
	Checks  []string `json:"checks,omitempty"`
}

// MatchHints are diagnosis-only causal identity signals used to recognize the
// same defect after code moves, renames, and refactors. They deliberately do
// not contain remediation or implementation guidance.
type MatchHints struct {
	Component           string   `json:"component"`
	Operation           string   `json:"operation"`
	FailureMode         string   `json:"failure_mode"`
	Trigger             string   `json:"trigger"`
	ViolatedInvariant   string   `json:"violated_invariant"`
	ObservableOutcome   string   `json:"observable_outcome"`
	RelatedSymbols      []string `json:"related_symbols"`
	SourceAnchors       []string `json:"source_anchors"`
	DistinguishingFacts []string `json:"distinguishing_facts"`
}

type FixEffortEstimate struct {
	LOCMin    int    `json:"loc_min"`
	LOCMax    int    `json:"loc_max"`
	Class     string `json:"class"`
	Rationale string `json:"rationale"`
}

type FixEffort struct {
	Quick   FixEffortEstimate `json:"quick"`
	Quality FixEffortEstimate `json:"quality"`
}

type FindingCandidate struct {
	Severity   string     `json:"severity"`
	Title      string     `json:"title"`
	Symbol     string     `json:"symbol,omitempty"`
	File       string     `json:"file"`
	Line       *int       `json:"line,omitempty"`
	Message    string     `json:"message,omitempty"`
	Evidence   string     `json:"evidence"`
	Impact     string     `json:"impact"`
	Validation Validation `json:"validation"`
	MatchHints MatchHints `json:"match_hints"`
	FixEffort  FixEffort  `json:"fix_effort"`
}

type Observation struct {
	Model      string             `json:"model"`
	ModelAlias string             `json:"model_alias,omitempty"`
	Account    string             `json:"account,omitempty"`
	Reviewer   string             `json:"reviewer,omitempty"`
	ScopeFiles []FileRef          `json:"scope_files"`
	Findings   []FindingCandidate `json:"findings"`
	Summary    string             `json:"summary,omitempty"`
	RawDigest  string             `json:"raw_digest,omitempty"`
}

// RepositoryReviewEvidence is one assigned review child. Campaign-aware Record
// requires every assigned child, including failures, so completion can be
// derived from the full required-child denominator instead of caller-supplied
// aggregate counts. A successful child carries its validated observation and
// exact acknowledged subset; an unsuccessful child carries neither.
type RepositoryReviewEvidence struct {
	AssignmentID      string       `json:"assignment_id"`
	FocusID           string       `json:"focus_id,omitempty"`
	ReviewerIdentity  string       `json:"reviewer_identity,omitempty"`
	ScopeFiles        []FileRef    `json:"scope_files"`
	Required          bool         `json:"required"`
	Successful        bool         `json:"successful"`
	AcknowledgedFiles []FileRef    `json:"acknowledged_files,omitempty"`
	Observation       *Observation `json:"observation,omitempty"`
}

type FindingContext struct {
	ID            string    `json:"id"`
	CampaignID    string    `json:"campaign_id,omitempty"`
	Repository    string    `json:"repository"`
	CommitSHA     string    `json:"commit_sha"`
	InventoryHash string    `json:"inventory_hash"`
	ProfileHash   string    `json:"profile_hash"`
	RunID         string    `json:"run_id"`
	Model         string    `json:"model"`
	ModelAlias    string    `json:"model_alias,omitempty"`
	Account       string    `json:"account,omitempty"`
	Reviewer      string    `json:"reviewer,omitempty"`
	Files         []FileRef `json:"files"`
	RawDigest     string    `json:"raw_digest,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type FindingStatus string

const (
	FindingOpen      FindingStatus = "open"
	FindingDismissed FindingStatus = "dismissed"
	FindingPosted    FindingStatus = "posted"
)

type IssueDraftState string

const (
	IssueDraftGenerating IssueDraftState = "generating"
	IssueDraftFailed     IssueDraftState = "failed"
	IssueDraftEditing    IssueDraftState = "editing"
	IssueDraftPublishing IssueDraftState = "publishing"
	IssueDraftPosted     IssueDraftState = "posted"
	IssueDraftUnknown    IssueDraftState = "unknown"
)

type IssueDraftOrigin string

const (
	IssueDraftOriginAIGenerated IssueDraftOrigin = "ai_generated"
	IssueDraftOriginLinked      IssueDraftOrigin = "linked"
	IssueDraftOriginDiscovered  IssueDraftOrigin = "discovered"
	IssueDraftOriginLegacy      IssueDraftOrigin = "legacy"
)

type IssueDraftInstructionsMode string

const (
	IssueDraftInstructionsDefault IssueDraftInstructionsMode = "default"
	IssueDraftInstructionsCustom  IssueDraftInstructionsMode = "custom"
)

type IssueDraft struct {
	ID                             string                     `json:"id"`
	Repository                     string                     `json:"repository"`
	FindingIDs                     []string                   `json:"finding_ids"`
	Origin                         IssueDraftOrigin           `json:"origin"`
	GenerationID                   string                     `json:"generation_id,omitempty"`
	ResolvedInstructions           string                     `json:"resolved_instructions,omitempty"`
	InstructionsMode               IssueDraftInstructionsMode `json:"instructions_mode,omitempty"`
	GeneratorModel                 string                     `json:"generator_model,omitempty"`
	GeneratorAccount               string                     `json:"generator_account,omitempty"`
	GeneratorProfileID             string                     `json:"generator_profile_id,omitempty"`
	GeneratorProfileVersion        int64                      `json:"generator_profile_version,omitempty"`
	AttemptGenerationID            string                     `json:"attempt_generation_id,omitempty"`
	AttemptResolvedInstructions    string                     `json:"attempt_resolved_instructions,omitempty"`
	AttemptInstructionsMode        IssueDraftInstructionsMode `json:"attempt_instructions_mode,omitempty"`
	AttemptGeneratorModel          string                     `json:"attempt_generator_model,omitempty"`
	AttemptGeneratorAccount        string                     `json:"attempt_generator_account,omitempty"`
	AttemptGeneratorProfileID      string                     `json:"attempt_generator_profile_id,omitempty"`
	AttemptGeneratorProfileVersion int64                      `json:"attempt_generator_profile_version,omitempty"`
	GenerationError                string                     `json:"generation_error,omitempty"`
	Canonical                      bool                       `json:"canonical"`
	Title                          string                     `json:"title"`
	Body                           string                     `json:"body"`
	Labels                         []string                   `json:"labels,omitempty"`
	State                          IssueDraftState            `json:"state"`
	ExternalID                     string                     `json:"external_id,omitempty"`
	ExternalURL                    string                     `json:"external_url,omitempty"`
	ExternalState                  string                     `json:"external_state,omitempty"`
	Version                        int64                      `json:"version"`
	CreatedAt                      time.Time                  `json:"created_at"`
	UpdatedAt                      time.Time                  `json:"updated_at"`
}

type Finding struct {
	ID               string               `json:"id"`
	CampaignID       string               `json:"campaign_id,omitempty"`
	Fingerprint      string               `json:"fingerprint"`
	Repository       string               `json:"repository"`
	CommitSHA        string               `json:"commit_sha"`
	File             FileRef              `json:"file"`
	Line             *int                 `json:"line,omitempty"`
	Severity         string               `json:"severity"`
	Title            string               `json:"title"`
	Symbol           string               `json:"symbol,omitempty"`
	Message          string               `json:"message,omitempty"`
	Evidence         string               `json:"evidence"`
	Impact           string               `json:"impact"`
	Validation       Validation           `json:"validation"`
	MatchHints       MatchHints           `json:"match_hints,omitempty"`
	FixEffort        FixEffort            `json:"fix_effort,omitempty"`
	ContextIDs       []string             `json:"context_ids"`
	Models           []string             `json:"models"`
	ObservationCount int                  `json:"observation_count"`
	Observations     []FindingObservation `json:"observations,omitempty"`
	// DeduplicationPending marks a compatibility projection of one or more raw
	// findings. It is never eligible for mapping, issue generation, or lifecycle
	// mutation; only a promoted deduplicated finding clears this gate.
	DeduplicationPending    bool                 `json:"deduplication_pending,omitempty"`
	RawFindingIDs           []string             `json:"raw_finding_ids,omitempty"`
	Status                  FindingStatus        `json:"status"`
	IssueDraftID            string               `json:"issue_draft_id,omitempty"`
	RepositoryFindingID     string               `json:"repository_finding_id,omitempty"`
	RepositoryMatchState    RepositoryMatchState `json:"repository_match_state,omitempty"`
	TargetBranch            string               `json:"target_branch,omitempty"`
	AdvertisedDefaultBranch string               `json:"advertised_default_branch,omitempty"`
	TargetIsDefault         bool                 `json:"target_is_default"`
	DefaultBranchVerified   bool                 `json:"default_branch_verified,omitempty"`
	PostResolutionVerified  bool                 `json:"post_resolution_verified,omitempty"`
	PostResolutionFixCommit string               `json:"post_resolution_fix_commit,omitempty"`
	PostResolutionFindingID string               `json:"post_resolution_finding_id,omitempty"`
	Version                 int64                `json:"version"`
	CreatedAt               time.Time            `json:"created_at"`
	UpdatedAt               time.Time            `json:"updated_at"`
}

type FindingObservation struct {
	ContextID  string     `json:"context_id"`
	Model      string     `json:"model"`
	ModelAlias string     `json:"model_alias,omitempty"`
	Account    string     `json:"account,omitempty"`
	Reviewer   string     `json:"reviewer,omitempty"`
	Severity   string     `json:"severity"`
	Title      string     `json:"title"`
	Symbol     string     `json:"symbol,omitempty"`
	Line       *int       `json:"line,omitempty"`
	Message    string     `json:"message,omitempty"`
	Evidence   string     `json:"evidence"`
	Impact     string     `json:"impact"`
	Validation Validation `json:"validation"`
	MatchHints MatchHints `json:"match_hints,omitempty"`
	FixEffort  FixEffort  `json:"fix_effort,omitempty"`
}

type RepositoryMatchState string

const (
	RepositoryMatchNew         RepositoryMatchState = "new"
	RepositoryMatchKnown       RepositoryMatchState = "known"
	RepositoryMatchProvisional RepositoryMatchState = "provisional"
)

type RepositoryFindingLifecycle string

const (
	RepositoryFindingOpen              RepositoryFindingLifecycle = "open"
	RepositoryFindingResolutionPending RepositoryFindingLifecycle = "resolution_pending"
	RepositoryFindingResolved          RepositoryFindingLifecycle = "resolved"
	RepositoryFindingRegressed         RepositoryFindingLifecycle = "regressed"
	RepositoryFindingDismissed         RepositoryFindingLifecycle = "dismissed"
)

type RepositoryFindingIssueState string

const (
	RepositoryFindingIssueNone    RepositoryFindingIssueState = "none"
	RepositoryFindingIssueDraft   RepositoryFindingIssueState = "draft"
	RepositoryFindingIssueOpen    RepositoryFindingIssueState = "open"
	RepositoryFindingIssueClosed  RepositoryFindingIssueState = "closed"
	RepositoryFindingIssueUnknown RepositoryFindingIssueState = "unknown"
)

type RepositoryFindingValidationState string

const (
	RepositoryValidationNotRequested RepositoryFindingValidationState = "not_requested"
	RepositoryValidationPending      RepositoryFindingValidationState = "pending"
	RepositoryValidationRunning      RepositoryFindingValidationState = "running"
	RepositoryValidationConfirmed    RepositoryFindingValidationState = "confirmed"
	RepositoryValidationNotFixed     RepositoryFindingValidationState = "not_fixed"
	RepositoryValidationInconclusive RepositoryFindingValidationState = "inconclusive"
	RepositoryValidationFailed       RepositoryFindingValidationState = "failed"
)

type RepositoryFindingPathSymbol struct {
	ReviewFindingID       string    `json:"review_finding_id"`
	CommitSHA             string    `json:"commit_sha"`
	Path                  string    `json:"path"`
	Symbol                string    `json:"symbol,omitempty"`
	DefaultBranchVerified bool      `json:"default_branch_verified,omitempty"`
	ObservedAt            time.Time `json:"observed_at"`
}

type RepositoryFindingPossibleDuplicate struct {
	CandidateID        string    `json:"candidate_id"`
	Relation           string    `json:"relation"`
	Confidence         float64   `json:"confidence"`
	MatchingAnchors    []string  `json:"matching_anchors,omitempty"`
	ConflictingAnchors []string  `json:"conflicting_anchors,omitempty"`
	Explanation        string    `json:"explanation,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
}

type RepositoryFindingIssueAssociation struct {
	ExternalID   string                      `json:"external_id,omitempty"`
	URL          string                      `json:"url,omitempty"`
	Origin       IssueDraftOrigin            `json:"origin,omitempty"`
	State        RepositoryFindingIssueState `json:"state"`
	Title        string                      `json:"title,omitempty"`
	SnapshotAt   time.Time                   `json:"snapshot_at,omitempty"`
	Conflict     bool                        `json:"conflict,omitempty"`
	ConflictURLs []string                    `json:"conflict_urls,omitempty"`
}

type RepositoryFindingResolution struct {
	Outcome            RepositoryFindingValidationState `json:"outcome"`
	FixCommitSHA       string                           `json:"fix_commit_sha,omitempty"`
	FixCommitTime      time.Time                        `json:"fix_commit_time,omitempty"`
	ValidatedAt        time.Time                        `json:"validated_at"`
	FirstContainingTag string                           `json:"first_containing_tag,omitempty"`
	Summary            string                           `json:"summary,omitempty"`
	Failure            *RepositoryValidationFailure     `json:"failure,omitempty"`
}

// RepositoryFinding is the stable cross-commit aggregate. Review findings are
// retained as immutable occurrences and referenced by ID.
type RepositoryFinding struct {
	ID                 string                               `json:"id"`
	Repository         string                               `json:"repository"`
	CanonicalTitle     string                               `json:"canonical_title"`
	CanonicalSeverity  string                               `json:"canonical_severity"`
	MatchHints         MatchHints                           `json:"match_hints,omitempty"`
	FixEffort          FixEffort                            `json:"fix_effort,omitempty"`
	ReviewFindingIDs   []string                             `json:"review_finding_ids"`
	OccurrenceCount    int                                  `json:"occurrence_count,omitempty"`
	FoundCommits       []string                             `json:"found_commits"`
	FoundCommitCount   int                                  `json:"found_commit_count,omitempty"`
	PathSymbolHistory  []RepositoryFindingPathSymbol        `json:"path_symbol_history"`
	MatchState         RepositoryMatchState                 `json:"match_state"`
	Lifecycle          RepositoryFindingLifecycle           `json:"lifecycle"`
	Issue              RepositoryFindingIssueAssociation    `json:"issue"`
	PossibleDuplicates []RepositoryFindingPossibleDuplicate `json:"possible_duplicates,omitempty"`
	ValidationState    RepositoryFindingValidationState     `json:"validation_state"`
	FixCommitSHA       string                               `json:"fix_commit_sha,omitempty"`
	FixCommitTime      time.Time                            `json:"fix_commit_time,omitempty"`
	FirstContainingTag string                               `json:"first_containing_tag,omitempty"`
	ResolutionHistory  []RepositoryFindingResolution        `json:"resolution_history,omitempty"`
	Version            int64                                `json:"version"`
	CreatedAt          time.Time                            `json:"created_at"`
	UpdatedAt          time.Time                            `json:"updated_at"`
}

type RepositoryMappingJobState string

const (
	RepositoryMappingPending   RepositoryMappingJobState = "pending"
	RepositoryMappingRunning   RepositoryMappingJobState = "running"
	RepositoryMappingCompleted RepositoryMappingJobState = "completed"

	// RepositoryRunFindingStatusAttemptLimit bounds automatic attempts to
	// associate one immutable run finding with repository-level state. An
	// explicit operator retry resets the counter before work is admitted again.
	RepositoryRunFindingStatusAttemptLimit = 3
)

type RepositoryMappingModelSnapshot struct {
	ProfileID      string `json:"profile_id,omitempty"`
	ProfileVersion int64  `json:"profile_version,omitempty"`
	Prompt         string `json:"prompt,omitempty"`
	Model          string `json:"model,omitempty"`
	Account        string `json:"account,omitempty"`
}

type RepositoryMappingAdjudication struct {
	Decision           string   `json:"decision"`
	CandidateID        string   `json:"candidate_id,omitempty"`
	Confidence         float64  `json:"confidence"`
	MatchingAnchors    []string `json:"matching_anchors,omitempty"`
	ConflictingAnchors []string `json:"conflicting_anchors,omitempty"`
	Explanation        string   `json:"explanation,omitempty"`
}

type RepositoryMappingJob struct {
	ID                  string                         `json:"id"`
	ReviewFindingID     string                         `json:"review_finding_id"`
	State               RepositoryMappingJobState      `json:"state"`
	RepositoryFindingID string                         `json:"repository_finding_id,omitempty"`
	ModelSnapshot       RepositoryMappingModelSnapshot `json:"model_snapshot,omitempty"`
	Adjudication        RepositoryMappingAdjudication  `json:"adjudication,omitempty"`
	CandidateUniverse   string                         `json:"candidate_universe,omitempty"`
	Attempts            int                            `json:"attempts"`
	Error               string                         `json:"error,omitempty"`
	ReservedAt          time.Time                      `json:"reserved_at,omitempty"`
	CreatedAt           time.Time                      `json:"created_at"`
	UpdatedAt           time.Time                      `json:"updated_at"`
}

type RepositoryValidationJob struct {
	ID                  string                           `json:"id"`
	RepositoryFindingID string                           `json:"repository_finding_id"`
	State               RepositoryFindingValidationState `json:"state"`
	ModelSnapshot       RepositoryMappingModelSnapshot   `json:"model_snapshot,omitempty"`
	CandidateCommits    []string                         `json:"candidate_commits"`
	FindingVersion      int64                            `json:"finding_version"`
	Attempts            int                              `json:"attempts"`
	Error               string                           `json:"error,omitempty"`
	Failure             *RepositoryValidationFailure     `json:"failure,omitempty"`
	ReservedAt          time.Time                        `json:"reserved_at,omitempty"`
	CreatedAt           time.Time                        `json:"created_at"`
	UpdatedAt           time.Time                        `json:"updated_at"`
}

type ReviewRun struct {
	ID                      string               `json:"id"`
	CampaignID              string               `json:"campaign_id,omitempty"`
	PlanID                  string               `json:"plan_id"`
	CommitSHA               string               `json:"commit_sha"`
	InventoryHash           string               `json:"inventory_hash"`
	ProfileHash             string               `json:"profile_hash,omitempty"`
	ScopeDigest             string               `json:"scope_digest,omitempty"`
	InspectedFiles          int                  `json:"inspected_files,omitempty"`
	LegacyRecovered         bool                 `json:"legacy_recovered,omitempty"`
	Interrupted             bool                 `json:"interrupted,omitempty"`
	ReviewedFiles           int                  `json:"reviewed_files"`
	UnreviewedFiles         int                  `json:"unreviewed_files"`
	UnsupportedCount        int                  `json:"unsupported_files"`
	RemainingFiles          int                  `json:"remaining_files"`
	UnreviewedPaths         []string             `json:"unreviewed_paths,omitempty"`
	UnsupportedPaths        []string             `json:"unsupported_paths,omitempty"`
	SkippedFiles            int                  `json:"skipped_files"`
	ExcludedFiles           int                  `json:"excluded_files"`
	AcceptedFindings        int                  `json:"accepted_findings"`
	FindingIDs              []string             `json:"finding_ids,omitempty"`
	CheckpointDigests       map[string]string    `json:"checkpoint_digests,omitempty"`
	CheckpointScopes        map[string][]FileRef `json:"checkpoint_scopes,omitempty"`
	RejectedFindings        int                  `json:"rejected_findings"`
	Models                  []string             `json:"models"`
	TargetBranch            string               `json:"target_branch,omitempty"`
	AdvertisedDefaultBranch string               `json:"advertised_default_branch,omitempty"`
	TargetIsDefault         bool                 `json:"target_is_default"`
	CompletedAt             time.Time            `json:"completed_at"`
}

// RepositoryReviewCampaignPathCoverage is the monotonic durable state of one
// selected repository path in the current controller-owned review campaign.
// Completed may be true without Inspected when the campaign inherited an exact
// same-profile checkpoint. Unsupported is terminal and mutually exclusive
// with both review states.
type RepositoryReviewCampaignPathCoverage struct {
	AssignmentBits string `json:"assignment_bits,omitempty"`
	Inspected      bool   `json:"inspected,omitempty"`
	Completed      bool   `json:"completed,omitempty"`
	Unsupported    bool   `json:"unsupported,omitempty"`
}

// RepositoryReviewAssignment is one stable campaign credit. ID is derived
// from the focus, reviewer identity, prompt revision, and frozen profile hash.
// Required assignments gate file completion; optional assignments are tracked
// independently but do not keep a file pending after all required work lands.
type RepositoryReviewAssignment struct {
	ID             string `json:"id"`
	FocusID        string `json:"focus_id"`
	Reviewer       string `json:"reviewer"`
	PromptRevision string `json:"prompt_revision"`
	ProfileHash    string `json:"profile_hash"`
	Required       bool   `json:"required"`
}

// RepositoryReviewAssignmentPlan is the exact missing-only scope reserved for
// one assignment in a review run. Empty scopes are never persisted or sent to
// a provider.
type RepositoryReviewAssignmentPlan struct {
	AssignmentID string    `json:"assignment_id"`
	FocusID      string    `json:"focus_id"`
	Label        string    `json:"label,omitempty"`
	Task         string    `json:"task,omitempty"`
	Reviewer     string    `json:"reviewer_model,omitempty"`
	Optional     bool      `json:"optional,omitempty"`
	Files        []FileRef `json:"files"`
}

// RepositoryReviewAssignmentReservation freezes the exact file-assignment
// pairs owned by an active run. CheckpointDigest is set only after the child
// result has been durably committed.
type RepositoryReviewAssignmentReservation struct {
	AssignmentID      string    `json:"assignment_id"`
	Files             []FileRef `json:"files"`
	AcknowledgedFiles []FileRef `json:"acknowledged_files,omitempty"`
	CheckpointDigest  string    `json:"checkpoint_digest,omitempty"`
}

// RepositoryReviewActiveRun is the durable crash-recovery fence for managed
// review dispatch. A launcher restart interrupts it, preserving committed bits
// while releasing only reservations without a durable checkpoint.
type RepositoryReviewActiveRun struct {
	ID            string                                           `json:"id"`
	CampaignID    string                                           `json:"campaign_id"`
	PlanID        string                                           `json:"plan_id"`
	CommitSHA     string                                           `json:"commit_sha"`
	InventoryHash string                                           `json:"inventory_hash"`
	ProfileHash   string                                           `json:"profile_hash"`
	Reservations  map[string]RepositoryReviewAssignmentReservation `json:"reservations"`
	FindingIDs    []string                                         `json:"finding_ids,omitempty"`
	StartedAt     time.Time                                        `json:"started_at"`
}

// RepositoryReviewCampaignCoverage is the compact, exact-path ledger for the
// current controller-owned campaign. InventoryHash and ProfileHash are empty
// only between trusted BeginCampaign authorization and the first matching
// Plan or Record. A false Exact value means Paths are only a known lower bound.
type RepositoryReviewCampaignCoverage struct {
	ID                    string                                          `json:"id"`
	CommitSHA             string                                          `json:"commit_sha"`
	InventoryHash         string                                          `json:"inventory_hash,omitempty"`
	ProfileHash           string                                          `json:"profile_hash,omitempty"`
	ScopeDigest           string                                          `json:"scope_digest,omitempty"`
	RequiredAssignments   int                                             `json:"required_assignments,omitempty"`
	AssignmentCatalog     []RepositoryReviewAssignment                    `json:"assignment_catalog,omitempty"`
	SelectedFiles         int                                             `json:"selected_files"`
	Exact                 bool                                            `json:"exact"`
	RecoveryDigest        string                                          `json:"recovery_digest,omitempty"`
	DeduplicationSnapshot *RepositoryReviewDeduplicationSnapshot          `json:"deduplication_snapshot,omitempty"`
	Paths                 map[string]RepositoryReviewCampaignPathCoverage `json:"paths"`
}

type RepositoryState struct {
	SchemaVersion            int                               `json:"schema_version"`
	ID                       string                            `json:"id"`
	Repository               string                            `json:"repository"`
	Version                  int64                             `json:"version"`
	ReviewVersion            int64                             `json:"review_version"`
	LastCommitSHA            string                            `json:"last_commit_sha,omitempty"`
	Files                    map[string]ReviewedFile           `json:"files"`
	Unsupported              map[string]UnsupportedFile        `json:"unsupported,omitempty"`
	ReviewAttempts           map[string]int                    `json:"review_attempts,omitempty"`
	ReviewAttemptIdentities  map[string]string                 `json:"review_attempt_identities,omitempty"`
	Findings                 []Finding                         `json:"findings"`
	RawFindings              []RawReviewFinding                `json:"raw_findings"`
	DeduplicatedFindings     []DeduplicatedReviewFinding       `json:"deduplicated_findings"`
	DeduplicationJobs        []DeduplicationJob                `json:"deduplication_jobs"`
	NextDeduplicationOrdinal uint64                            `json:"next_deduplication_ordinal"`
	FindingsProcessing       FindingsProcessingCounters        `json:"findings_processing"`
	Contexts                 []FindingContext                  `json:"contexts"`
	Runs                     []ReviewRun                       `json:"runs"`
	FileAttributions         []RepositoryReviewFileAttribution `json:"file_attributions"`
	IssueDrafts              []IssueDraft                      `json:"issue_drafts"`
	RepositoryFindings       []RepositoryFinding               `json:"repository_findings"`
	MappingJobs              []RepositoryMappingJob            `json:"mapping_jobs"`
	ValidationJobs           []RepositoryValidationJob         `json:"validation_jobs"`
	CurrentCampaign          *RepositoryReviewCampaignCoverage `json:"current_campaign,omitempty"`
	ActiveReviewRun          *RepositoryReviewActiveRun        `json:"active_review_run,omitempty"`
	CampaignHistory          map[string]string                 `json:"campaign_history,omitempty"`
	ActiveForceCampaignID    string                            `json:"active_force_campaign_id,omitempty"`
	ActiveForceProfileHash   string                            `json:"active_force_profile_hash,omitempty"`
	ActiveForceCommitSHA     string                            `json:"active_force_commit_sha,omitempty"`
	HistoricalDeduplication  HistoricalDeduplicationReplay     `json:"historical_deduplication"`
	UpdatedAt                time.Time                         `json:"updated_at"`
	FindingCount             int                               `json:"finding_count"`
	RepositoryFindingCount   int                               `json:"repository_finding_count"`
	OpenFindingCount         int                               `json:"open_finding_count"`
	IssueDraftCount          int                               `json:"issue_draft_count"`
	UnsupportedCount         int                               `json:"unsupported_count"`
	ReviewedFileCount        int                               `json:"reviewed_file_count"`
	LastExcludedFiles        int                               `json:"last_excluded_files"`
}

type RepositorySummary struct {
	SchemaVersion          int       `json:"schema_version"`
	ID                     string    `json:"id"`
	Repository             string    `json:"repository"`
	Version                int64     `json:"version"`
	ReviewVersion          int64     `json:"review_version"`
	LastCommitSHA          string    `json:"last_commit_sha,omitempty"`
	FindingCount           int       `json:"finding_count"`
	RepositoryFindingCount int       `json:"repository_finding_count"`
	OpenFindingCount       int       `json:"open_finding_count"`
	IssueDraftCount        int       `json:"issue_draft_count"`
	UnsupportedCount       int       `json:"unsupported_count"`
	ReviewedFileCount      int       `json:"reviewed_file_count"`
	ExcludedFileCount      int       `json:"excluded_file_count"`
	UpdatedAt              time.Time `json:"updated_at"`
}

func Summarize(state RepositoryState) RepositorySummary {
	openFindings := 0
	findingCount := len(state.Findings)
	if len(state.RawFindings) > 0 || len(state.DeduplicationJobs) > 0 ||
		len(state.DeduplicatedFindings) > 0 {
		findingCount = len(state.DeduplicatedFindings)
		for _, finding := range state.DeduplicatedFindings {
			if finding.Status == FindingOpen {
				openFindings++
			}
		}
	} else {
		for _, finding := range state.Findings {
			if finding.Status == FindingOpen {
				openFindings++
			}
		}
	}
	return RepositorySummary{
		SchemaVersion: state.SchemaVersion, ID: state.ID, Repository: state.Repository,
		Version: state.Version, ReviewVersion: state.ReviewVersion,
		LastCommitSHA: state.LastCommitSHA, FindingCount: findingCount,
		RepositoryFindingCount: len(state.RepositoryFindings),
		OpenFindingCount:       openFindings, IssueDraftCount: len(state.IssueDrafts),
		UnsupportedCount: len(state.Unsupported), ReviewedFileCount: len(state.Files),
		ExcludedFileCount: state.LastExcludedFiles,
		UpdatedAt:         state.UpdatedAt,
	}
}

type IssueDraftRequest struct {
	Repository      string   `json:"repository"`
	FindingIDs      []string `json:"finding_ids"`
	Title           string   `json:"title,omitempty"`
	Body            string   `json:"body,omitempty"`
	Labels          []string `json:"labels,omitempty"`
	ExpectedVersion int64    `json:"expected_version"`
}

type RecordRequest struct {
	Plan                    Plan                       `json:"plan"`
	RunID                   string                     `json:"run_id"`
	Observations            []Observation              `json:"observations"`
	ReviewEvidence          []RepositoryReviewEvidence `json:"review_evidence,omitempty"`
	InspectedFiles          []FileRef                  `json:"inspected_files,omitempty"`
	CompletedFiles          []FileRef                  `json:"completed_files,omitempty"`
	UnsupportedFiles        []UnsupportedFile          `json:"unsupported_files,omitempty"`
	ExcludedFiles           int                        `json:"excluded_files,omitempty"`
	TargetBranch            string                     `json:"target_branch,omitempty"`
	AdvertisedDefaultBranch string                     `json:"advertised_default_branch,omitempty"`
	TargetIsDefault         bool                       `json:"target_is_default"`
	CompletedAt             time.Time                  `json:"completed_at,omitempty"`
}

type RecordResult struct {
	State              RepositoryState `json:"state"`
	Run                ReviewRun       `json:"run"`
	AcceptedFindingIDs []string        `json:"accepted_finding_ids"`
}
