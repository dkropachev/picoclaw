package repoaudit

import "time"

const SchemaVersion = 1

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

type Plan struct {
	ID                 string            `json:"id"`
	Repository         string            `json:"repository"`
	CommitSHA          string            `json:"commit_sha"`
	InventoryHash      string            `json:"inventory_hash"`
	ProfileHash        string            `json:"profile_hash"`
	ForceCampaignID    string            `json:"force_campaign_id,omitempty"`
	Authoritative      bool              `json:"authoritative,omitempty"`
	StateVersion       int64             `json:"state_version"`
	PendingFiles       []FileRef         `json:"pending_files"`
	DeferredFiles      []FileRef         `json:"deferred_files,omitempty"`
	UnchangedFiles     []FileRef         `json:"unchanged_files"`
	UnsupportedFiles   []UnsupportedFile `json:"unsupported_files,omitempty"`
	PreviouslyReviewed int               `json:"previously_reviewed"`
	CreatedAt          time.Time         `json:"created_at"`
}

type Validation struct {
	Status  string   `json:"status"`
	Summary string   `json:"summary"`
	Checks  []string `json:"checks,omitempty"`
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
}

type Observation struct {
	Model      string             `json:"model"`
	Reviewer   string             `json:"reviewer,omitempty"`
	ScopeFiles []FileRef          `json:"scope_files"`
	Findings   []FindingCandidate `json:"findings"`
	Summary    string             `json:"summary,omitempty"`
	RawDigest  string             `json:"raw_digest,omitempty"`
}

type FindingContext struct {
	ID            string    `json:"id"`
	Repository    string    `json:"repository"`
	CommitSHA     string    `json:"commit_sha"`
	InventoryHash string    `json:"inventory_hash"`
	ProfileHash   string    `json:"profile_hash"`
	RunID         string    `json:"run_id"`
	Model         string    `json:"model"`
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
	IssueDraftOriginLegacy      IssueDraftOrigin = "legacy"
)

type IssueDraftInstructionsMode string

const (
	IssueDraftInstructionsDefault IssueDraftInstructionsMode = "default"
	IssueDraftInstructionsCustom  IssueDraftInstructionsMode = "custom"
)

type IssueDraft struct {
	ID                          string                     `json:"id"`
	Repository                  string                     `json:"repository"`
	FindingIDs                  []string                   `json:"finding_ids"`
	Origin                      IssueDraftOrigin           `json:"origin"`
	GenerationID                string                     `json:"generation_id,omitempty"`
	ResolvedInstructions        string                     `json:"resolved_instructions,omitempty"`
	InstructionsMode            IssueDraftInstructionsMode `json:"instructions_mode,omitempty"`
	GeneratorModel              string                     `json:"generator_model,omitempty"`
	GeneratorAccount            string                     `json:"generator_account,omitempty"`
	AttemptGenerationID         string                     `json:"attempt_generation_id,omitempty"`
	AttemptResolvedInstructions string                     `json:"attempt_resolved_instructions,omitempty"`
	AttemptInstructionsMode     IssueDraftInstructionsMode `json:"attempt_instructions_mode,omitempty"`
	AttemptGeneratorModel       string                     `json:"attempt_generator_model,omitempty"`
	AttemptGeneratorAccount     string                     `json:"attempt_generator_account,omitempty"`
	GenerationError             string                     `json:"generation_error,omitempty"`
	Canonical                   bool                       `json:"canonical"`
	Title                       string                     `json:"title"`
	Body                        string                     `json:"body"`
	Labels                      []string                   `json:"labels,omitempty"`
	State                       IssueDraftState            `json:"state"`
	ExternalID                  string                     `json:"external_id,omitempty"`
	ExternalURL                 string                     `json:"external_url,omitempty"`
	Version                     int64                      `json:"version"`
	CreatedAt                   time.Time                  `json:"created_at"`
	UpdatedAt                   time.Time                  `json:"updated_at"`
}

type Finding struct {
	ID               string               `json:"id"`
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
	ContextIDs       []string             `json:"context_ids"`
	Models           []string             `json:"models"`
	ObservationCount int                  `json:"observation_count"`
	Observations     []FindingObservation `json:"observations,omitempty"`
	Status           FindingStatus        `json:"status"`
	IssueDraftID     string               `json:"issue_draft_id,omitempty"`
	Version          int64                `json:"version"`
	CreatedAt        time.Time            `json:"created_at"`
	UpdatedAt        time.Time            `json:"updated_at"`
}

type FindingObservation struct {
	ContextID  string     `json:"context_id"`
	Model      string     `json:"model"`
	Reviewer   string     `json:"reviewer,omitempty"`
	Severity   string     `json:"severity"`
	Title      string     `json:"title"`
	Symbol     string     `json:"symbol,omitempty"`
	Line       *int       `json:"line,omitempty"`
	Message    string     `json:"message,omitempty"`
	Evidence   string     `json:"evidence"`
	Impact     string     `json:"impact"`
	Validation Validation `json:"validation"`
}

type ReviewRun struct {
	ID               string    `json:"id"`
	PlanID           string    `json:"plan_id"`
	CommitSHA        string    `json:"commit_sha"`
	InventoryHash    string    `json:"inventory_hash"`
	ReviewedFiles    int       `json:"reviewed_files"`
	UnreviewedFiles  int       `json:"unreviewed_files"`
	UnsupportedCount int       `json:"unsupported_files"`
	RemainingFiles   int       `json:"remaining_files"`
	UnreviewedPaths  []string  `json:"unreviewed_paths,omitempty"`
	UnsupportedPaths []string  `json:"unsupported_paths,omitempty"`
	SkippedFiles     int       `json:"skipped_files"`
	ExcludedFiles    int       `json:"excluded_files"`
	AcceptedFindings int       `json:"accepted_findings"`
	FindingIDs       []string  `json:"finding_ids,omitempty"`
	RejectedFindings int       `json:"rejected_findings"`
	Models           []string  `json:"models"`
	CompletedAt      time.Time `json:"completed_at"`
}

type RepositoryState struct {
	SchemaVersion           int                        `json:"schema_version"`
	ID                      string                     `json:"id"`
	Repository              string                     `json:"repository"`
	Version                 int64                      `json:"version"`
	ReviewVersion           int64                      `json:"review_version"`
	LastCommitSHA           string                     `json:"last_commit_sha,omitempty"`
	Files                   map[string]ReviewedFile    `json:"files"`
	Unsupported             map[string]UnsupportedFile `json:"unsupported,omitempty"`
	ReviewAttempts          map[string]int             `json:"review_attempts,omitempty"`
	ReviewAttemptIdentities map[string]string          `json:"review_attempt_identities,omitempty"`
	Findings                []Finding                  `json:"findings"`
	Contexts                []FindingContext           `json:"contexts"`
	Runs                    []ReviewRun                `json:"runs"`
	IssueDrafts             []IssueDraft               `json:"issue_drafts"`
	ActiveForceCampaignID   string                     `json:"active_force_campaign_id,omitempty"`
	ActiveForceProfileHash  string                     `json:"active_force_profile_hash,omitempty"`
	ActiveForceCommitSHA    string                     `json:"active_force_commit_sha,omitempty"`
	UpdatedAt               time.Time                  `json:"updated_at"`
	FindingCount            int                        `json:"finding_count"`
	OpenFindingCount        int                        `json:"open_finding_count"`
	IssueDraftCount         int                        `json:"issue_draft_count"`
	UnsupportedCount        int                        `json:"unsupported_count"`
	ReviewedFileCount       int                        `json:"reviewed_file_count"`
	LastExcludedFiles       int                        `json:"last_excluded_files"`
}

type RepositorySummary struct {
	SchemaVersion     int       `json:"schema_version"`
	ID                string    `json:"id"`
	Repository        string    `json:"repository"`
	Version           int64     `json:"version"`
	ReviewVersion     int64     `json:"review_version"`
	LastCommitSHA     string    `json:"last_commit_sha,omitempty"`
	FindingCount      int       `json:"finding_count"`
	OpenFindingCount  int       `json:"open_finding_count"`
	IssueDraftCount   int       `json:"issue_draft_count"`
	UnsupportedCount  int       `json:"unsupported_count"`
	ReviewedFileCount int       `json:"reviewed_file_count"`
	ExcludedFileCount int       `json:"excluded_file_count"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func Summarize(state RepositoryState) RepositorySummary {
	openFindings := 0
	for _, finding := range state.Findings {
		if finding.Status == FindingOpen {
			openFindings++
		}
	}
	return RepositorySummary{
		SchemaVersion: state.SchemaVersion, ID: state.ID, Repository: state.Repository,
		Version: state.Version, ReviewVersion: state.ReviewVersion,
		LastCommitSHA: state.LastCommitSHA, FindingCount: len(state.Findings),
		OpenFindingCount: openFindings, IssueDraftCount: len(state.IssueDrafts),
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
	Plan             Plan              `json:"plan"`
	RunID            string            `json:"run_id"`
	Observations     []Observation     `json:"observations"`
	CompletedFiles   []FileRef         `json:"completed_files,omitempty"`
	UnsupportedFiles []UnsupportedFile `json:"unsupported_files,omitempty"`
	ExcludedFiles    int               `json:"excluded_files,omitempty"`
	CompletedAt      time.Time         `json:"completed_at,omitempty"`
}

type RecordResult struct {
	State              RepositoryState `json:"state"`
	Run                ReviewRun       `json:"run"`
	AcceptedFindingIDs []string        `json:"accepted_finding_ids"`
}
