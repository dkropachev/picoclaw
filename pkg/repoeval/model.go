// Package repoeval provides the durable domain model for repository model
// evaluations. It intentionally stores corpus references and derived results,
// never repository source contents.
package repoeval

import "time"

const SchemaVersion = 1

// Status is the durable lifecycle state of an evaluation.
type Status string

const (
	StatusDraft        Status = "draft"
	StatusPreflighting Status = "preflighting"
	StatusReady        Status = "ready"
	StatusRunning      Status = "running"
	StatusJudging      Status = "judging"
	StatusAnalyzing    Status = "analyzing"
	StatusCompleted    Status = "completed"
	StatusCanceling    Status = "canceling"
	StatusCanceled     Status = "canceled"
	StatusFailed       Status = "failed"
)

// RecoveryDirective tells a restart coordinator what durable work remains.
type RecoveryDirective string

const (
	RecoveryNone         RecoveryDirective = "none"
	RecoveryResume       RecoveryDirective = "resume"
	RecoveryFinishCancel RecoveryDirective = "finish-cancel"
)

func (status Status) Valid() bool {
	switch status {
	case StatusDraft, StatusPreflighting, StatusReady, StatusRunning, StatusJudging,
		StatusAnalyzing, StatusCompleted, StatusCanceling, StatusCanceled, StatusFailed:
		return true
	default:
		return false
	}
}

func (status Status) Terminal() bool {
	return status == StatusCompleted || status == StatusCanceled || status == StatusFailed
}

func (status Status) InFlight() bool {
	return status == StatusPreflighting || status == StatusRunning || status == StatusJudging ||
		status == StatusAnalyzing || status == StatusCanceling
}

func (status Status) RecoveryDirective() RecoveryDirective {
	switch status {
	case StatusPreflighting, StatusReady, StatusRunning, StatusJudging, StatusAnalyzing:
		return RecoveryResume
	case StatusCanceling:
		return RecoveryFinishCancel
	default:
		return RecoveryNone
	}
}

func (status Status) CanTransitionTo(next Status) bool {
	if status == next {
		return status.Valid()
	}
	switch status {
	case StatusDraft:
		return next == StatusPreflighting || next == StatusCanceled || next == StatusFailed
	case StatusPreflighting:
		return next == StatusReady || next == StatusCanceling || next == StatusFailed
	case StatusReady:
		return next == StatusRunning || next == StatusCanceling || next == StatusFailed
	case StatusRunning:
		return next == StatusJudging || next == StatusCanceling || next == StatusFailed
	case StatusJudging:
		return next == StatusAnalyzing || next == StatusCanceling || next == StatusFailed
	case StatusAnalyzing:
		return next == StatusCompleted || next == StatusCanceling || next == StatusFailed
	case StatusCanceling:
		return next == StatusCanceled
	case StatusFailed:
		return next == StatusPreflighting || next == StatusRunning
	default:
		return false
	}
}

// CodeType is a semantic class used by AI-assisted corpus selection.
type CodeType string

const (
	CodeTypeHotpath   CodeType = "hotpath-code"
	CodeTypeCode      CodeType = "code"
	CodeTypeTest      CodeType = "test"
	CodeTypeBenchTest CodeType = "bench-test"
	// CodeTypeBenchmark is retained as a descriptive alias for callers that do
	// not use reposcope's CodeTypeBenchTest name.
	CodeTypeBenchmark = CodeTypeBenchTest
)

func (codeType CodeType) Valid() bool {
	switch codeType {
	case CodeTypeHotpath, CodeTypeCode, CodeTypeTest, CodeTypeBenchTest:
		return true
	default:
		return false
	}
}

// Focus controls which repository areas and semantic code classes are eligible.
type Focus struct {
	CodeTypes      []CodeType `json:"code_types,omitempty"`
	IncludeFolders []string   `json:"include_folders,omitempty"`
	ExcludeFolders []string   `json:"exclude_folders,omitempty"`
	FreeText       string     `json:"free_text,omitempty"`
}

type CorpusChunk struct {
	ID          string `json:"id"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	ContentHash string `json:"content_hash"`
}

type CorpusFile struct {
	CandidateID string        `json:"candidate_id"`
	Path        string        `json:"path"`
	BlobSHA     string        `json:"blob_sha"`
	SizeBytes   int64         `json:"size_bytes"`
	Language    string        `json:"language"`
	CodeType    CodeType      `json:"code_type"`
	Module      string        `json:"module"`
	Region      string        `json:"region"`
	Chunks      []CorpusChunk `json:"chunks"`
}

// CorpusManifest pins all inputs that make an evaluation reproducible.
type CorpusManifest struct {
	CommitSHA          string         `json:"commit_sha"`
	InventoryHash      string         `json:"inventory_hash"`
	PolicyHash         string         `json:"policy_hash"`
	RubricHash         string         `json:"rubric_hash"`
	SelectorRunID      string         `json:"selector_run_id"`
	SelectionRationale string         `json:"selection_rationale,omitempty"`
	Files              []CorpusFile   `json:"files"`
	LanguageCounts     map[string]int `json:"language_counts"`
	GeneratedAt        time.Time      `json:"generated_at"`
}

type ProgressStage string

const (
	ProgressIdle               ProgressStage = "idle"
	ProgressResolving          ProgressStage = "resolving"
	ProgressInventorying       ProgressStage = "inventorying"
	ProgressClassifying        ProgressStage = "classifying"
	ProgressSelecting          ProgressStage = "selecting"
	ProgressValidating         ProgressStage = "validating"
	ProgressCandidateExecution ProgressStage = "candidate-execution"
	ProgressJudging            ProgressStage = "judging"
	ProgressAnalyzing          ProgressStage = "analyzing"
	ProgressCompleted          ProgressStage = "completed"
	ProgressCanceling          ProgressStage = "canceling"
	ProgressCanceled           ProgressStage = "canceled"
	ProgressFailed             ProgressStage = "failed"
)

func (stage ProgressStage) Valid() bool {
	switch stage {
	case ProgressIdle, ProgressResolving, ProgressInventorying, ProgressClassifying,
		ProgressSelecting, ProgressValidating, ProgressCandidateExecution, ProgressJudging,
		ProgressAnalyzing, ProgressCompleted, ProgressCanceling, ProgressCanceled, ProgressFailed:
		return true
	default:
		return false
	}
}

type LanguageProgress struct {
	AvailableFiles int      `json:"available_files"`
	SelectedFiles  int      `json:"selected_files"`
	CompletedFiles int      `json:"completed_files"`
	SelectedBytes  int64    `json:"selected_bytes"`
	Regions        []string `json:"regions"`
	Limited        bool     `json:"limited"`
}

type Progress struct {
	Stage          ProgressStage               `json:"stage"`
	Languages      map[string]LanguageProgress `json:"languages"`
	TotalFiles     int                         `json:"total_files"`
	SelectedFiles  int                         `json:"selected_files"`
	CompletedFiles int                         `json:"completed_files"`
	TotalTasks     int                         `json:"total_tasks"`
	CompletedTasks int                         `json:"completed_tasks"`
	CurrentModel   string                      `json:"current_model,omitempty"`
	CurrentPath    string                      `json:"current_path,omitempty"`
	Message        string                      `json:"message,omitempty"`
	Percent        float64                     `json:"percent"`
	UpdatedAt      time.Time                   `json:"updated_at"`
}

type Usage struct {
	Requests          int64    `json:"requests"`
	InputTokens       int64    `json:"input_tokens"`
	CachedInputTokens int64    `json:"cached_input_tokens"`
	OutputTokens      int64    `json:"output_tokens"`
	ReasoningTokens   int64    `json:"reasoning_tokens"`
	DurationMillis    int64    `json:"duration_millis"`
	EstimatedCostUSD  *float64 `json:"estimated_cost_usd,omitempty"`
}

type ModelStats struct {
	FilesSelected  int        `json:"files_selected"`
	FilesCompleted int        `json:"files_completed"`
	Attempts       int        `json:"attempts"`
	Successes      int        `json:"successes"`
	Failures       int        `json:"failures"`
	OverallScore   float64    `json:"overall_score"`
	Usage          Usage      `json:"usage"`
	Summary        string     `json:"summary,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

// BatchCandidateCheckpoint records the successful immutable file inputs and
// child-call outcomes for one candidate alias in a judged batch. It is the
// durable task boundary used to resume only missing alias/file pairs.
type BatchCandidateCheckpoint struct {
	CompletedCandidateIDs []string `json:"completed_candidate_ids"`
	Attempts              int      `json:"attempts"`
	Successes             int      `json:"successes"`
	Failures              int      `json:"failures"`
}

// BatchCheckpoint is the compact durable recovery boundary for one fully
// judged corpus batch. It deliberately retains only bounded structured judge
// evidence and the blinded alias mapping; candidate source and raw provider
// payloads never enter the evaluation store.
type BatchCheckpoint struct {
	ID           string                              `json:"id"`
	CandidateIDs []string                            `json:"candidate_ids"`
	Candidates   map[string]BatchCandidateCheckpoint `json:"candidates,omitempty"`
	JudgeJSON    string                              `json:"judge_json"`
	MappingJSON  string                              `json:"mapping_json"`
	CompletedAt  time.Time                           `json:"completed_at"`
}

// Checkpoint contains enough compact evidence to skip completed batches after
// a launcher restart and to rerun only the final analysis when needed.
type Checkpoint struct {
	Batches        []BatchCheckpoint         `json:"batches,omitempty"`
	ConcreteModels map[string]map[string]int `json:"concrete_models,omitempty"`
}

type ModelCompletion string

const (
	ModelCompletionPending   ModelCompletion = "pending"
	ModelCompletionCompleted ModelCompletion = "completed"
	ModelCompletionPartial   ModelCompletion = "partial"
	ModelCompletionFailed    ModelCompletion = "failed"
)

func (completion ModelCompletion) Valid() bool {
	switch completion {
	case ModelCompletionPending, ModelCompletionCompleted, ModelCompletionPartial, ModelCompletionFailed:
		return true
	default:
		return false
	}
}

// ModelComparison is one self-contained row in the final model comparison
// table. ConcreteModels records the resolved model distribution behind an
// alias, while Usage retains objective cost/performance evidence beside the
// explicitly AI-judged score dimensions.
type ModelComparison struct {
	ModelAlias        string             `json:"model_alias"`
	ConcreteModels    map[string]int     `json:"concrete_models"`
	Completion        ModelCompletion    `json:"completion"`
	Failure           string             `json:"failure,omitempty"`
	Failures          int                `json:"failures"`
	Rank              int                `json:"rank"`
	OverallScore      *float64           `json:"overall_score,omitempty"`
	Scores            map[string]float64 `json:"scores"`
	Languages         []string           `json:"languages"`
	Regions           []string           `json:"regions"`
	FilesAnalyzed     int                `json:"files_analyzed"`
	BytesAnalyzed     int64              `json:"bytes_analyzed"`
	ConfirmedFindings int                `json:"confirmed_findings"`
	UnsupportedFiles  int                `json:"unsupported_files"`
	Usage             Usage              `json:"usage"`
	Verdict           string             `json:"verdict,omitempty"`
	Summary           string             `json:"summary,omitempty"`
	Strengths         []string           `json:"strengths,omitempty"`
	Limitations       []string           `json:"limitations,omitempty"`
}

type Evaluation struct {
	SchemaVersion           int                   `json:"schema_version"`
	ID                      string                `json:"id"`
	Version                 int64                 `json:"version"`
	Status                  Status                `json:"status"`
	OneShot                 bool                  `json:"one_shot,omitempty"`
	Repository              string                `json:"repository"`
	Ref                     string                `json:"ref"`
	CandidateModels         []string              `json:"candidate_models"`
	SelectorModelAlias      string                `json:"selector_model_alias"`
	JudgeModelAlias         string                `json:"judge_model_alias"`
	Focus                   Focus                 `json:"focus"`
	DefaultFilesPerLanguage int                   `json:"default_files_per_language"`
	FilesPerLanguage        map[string]int        `json:"files_per_language"`
	Corpus                  *CorpusManifest       `json:"corpus,omitempty"`
	Progress                Progress              `json:"progress"`
	Usage                   Usage                 `json:"usage"`
	ModelStats              map[string]ModelStats `json:"model_stats"`
	Checkpoint              Checkpoint            `json:"checkpoint,omitempty"`
	Comparisons             []ModelComparison     `json:"comparisons"`
	Warnings                []string              `json:"warnings"`
	RunIDs                  []string              `json:"run_ids"`
	Failure                 string                `json:"failure,omitempty"`
	CreatedAt               time.Time             `json:"created_at"`
	UpdatedAt               time.Time             `json:"updated_at"`
	StartedAt               *time.Time            `json:"started_at,omitempty"`
	FinishedAt              *time.Time            `json:"finished_at,omitempty"`
}

func (evaluation Evaluation) RestartDirective() RecoveryDirective {
	return evaluation.Status.RecoveryDirective()
}

func (evaluation Evaluation) LatestRunID() string {
	if len(evaluation.RunIDs) == 0 {
		return ""
	}
	return evaluation.RunIDs[len(evaluation.RunIDs)-1]
}

type CreateRequest struct {
	Repository              string         `json:"repository"`
	Ref                     string         `json:"ref"`
	CandidateModels         []string       `json:"candidate_models"`
	SelectorModelAlias      string         `json:"selector_model_alias"`
	JudgeModelAlias         string         `json:"judge_model_alias"`
	Focus                   Focus          `json:"focus"`
	DefaultFilesPerLanguage int            `json:"default_files_per_language,omitempty"`
	FilesPerLanguage        map[string]int `json:"files_per_language,omitempty"`
	OneShot                 bool           `json:"-"`
	InitialRunID            string         `json:"-"`
}
