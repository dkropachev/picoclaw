// Package localci discovers and executes bounded local validation plans for
// controller-owned pull-request candidates.
package localci

import (
	"errors"
	"time"
)

const (
	PlanVersion            = 1
	DiscoveryVersion       = "picoclaw-local-ci-discovery-v1"
	SandboxProfile         = "picoclaw-local-ci-linux-bwrap-v2"
	maximumPlanSteps       = 64
	maximumPlanDiagnostics = 512
	maximumCommandLen      = 64 << 10
)

var (
	ErrInvalid                = errors.New("invalid local CI request")
	ErrIncomplete             = errors.New("local CI plan is incomplete")
	ErrPlanChanged            = errors.New("local CI plan changed")
	ErrSandboxUnavailable     = errors.New("local CI sandbox is unavailable")
	ErrEnvironmentUnavailable = errors.New("local CI environment is unavailable")
	ErrEvidenceConflict       = errors.New("local CI evidence conflict")
	ErrEvidenceCorrupt        = errors.New("local CI evidence is corrupt")
)

type Status string

const (
	StatusPassed                 Status = "passed"
	StatusFailed                 Status = "failed"
	StatusIncomplete             Status = "incomplete"
	StatusPlanChanged            Status = "plan_changed"
	StatusTimedOut               Status = "timed_out"
	StatusCanceled               Status = "canceled"
	StatusOutputLimitExceeded    Status = "output_limit_exceeded"
	StatusEnvironmentUnavailable Status = "environment_unavailable"
	StatusInfrastructureError    Status = "infrastructure_error"
)

type StepKind string

const (
	StepLint  StepKind = "lint"
	StepTest  StepKind = "test"
	StepBuild StepKind = "build"
	StepCheck StepKind = "check"
)

type StepOrigin string

const (
	OriginExplicit     StepOrigin = "picoclaw"
	OriginGitHubAction StepOrigin = "github_actions"
	OriginMake         StepOrigin = "make"
	OriginPackage      StepOrigin = "package"
	OriginLanguage     StepOrigin = "language"
)

type Diagnostic struct {
	Code   string `json:"code"`
	Source string `json:"source,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type EnvironmentVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Step is one exact repository-authored or detector-authored sandbox action.
// Exactly one of Argv and Script is populated. Repository text is data until a
// dedicated sandbox writes Script into a private file and invokes it there.
type Step struct {
	ID               string                `json:"id"`
	Name             string                `json:"name"`
	Kind             StepKind              `json:"kind"`
	Origin           StepOrigin            `json:"origin"`
	Source           string                `json:"source"`
	WorkingDirectory string                `json:"working_directory,omitempty"`
	Argv             []string              `json:"argv,omitempty"`
	Script           string                `json:"script,omitempty"`
	Shell            string                `json:"shell,omitempty"`
	Environment      []EnvironmentVariable `json:"environment,omitempty"`
	TimeoutSeconds   int64                 `json:"timeout_seconds"`
	Required         bool                  `json:"required"`
}

// Plan is a canonical workflow-like sequence. DefinitionDigest covers only
// gate definitions; DependencyDigest covers dependency inputs that invalidate
// execution identity without silently changing the required checks.
type Plan struct {
	Version          int          `json:"version"`
	DiscoveryVersion string       `json:"discovery_version"`
	DefinitionDigest string       `json:"definition_digest"`
	DependencyDigest string       `json:"dependency_digest"`
	Digest           string       `json:"digest"`
	Complete         bool         `json:"complete"`
	Steps            []Step       `json:"steps"`
	Diagnostics      []Diagnostic `json:"diagnostics,omitempty"`
}

type ResolvedPlan struct {
	Baseline  Plan `json:"baseline"`
	Candidate Plan `json:"candidate"`
	Effective Plan `json:"effective"`
	Changed   bool `json:"changed"`
}

type CandidateEvidence struct {
	Repository              string        `json:"repository"`
	ParentCommit            string        `json:"parent_commit"`
	Tree                    string        `json:"tree"`
	CandidateDigest         string        `json:"candidate_digest"`
	ParentManifestDigest    string        `json:"parent_manifest_digest"`
	CandidateManifestDigest string        `json:"candidate_manifest_digest"`
	DependencyDigest        string        `json:"dependency_digest"`
	PlanDigest              string        `json:"plan_digest"`
	EnvironmentDigest       string        `json:"environment_digest"`
	Limits                  LimitEvidence `json:"limits"`
}

type LimitEvidence struct {
	StepTimeoutMillis  int64  `json:"step_timeout_millis"`
	TotalTimeoutMillis int64  `json:"total_timeout_millis"`
	OutputBytes        int64  `json:"output_bytes"`
	ResourcePolicy     string `json:"resource_policy"`
}

type StepResult struct {
	StepID              string `json:"step_id"`
	Status              Status `json:"status"`
	ExitCode            int    `json:"exit_code"`
	Output              string `json:"output,omitempty"`
	OutputDigest        string `json:"output_digest"`
	OutputTruncated     bool   `json:"output_truncated,omitempty"`
	ObservedOutputBytes int64  `json:"observed_output_bytes"`
	DurationMillis      int64  `json:"duration_millis"`
}

type Execution struct {
	Version     int               `json:"version"`
	Digest      string            `json:"digest"`
	ResultKey   string            `json:"result_key"`
	Evidence    CandidateEvidence `json:"evidence"`
	Status      Status            `json:"status"`
	Steps       []StepResult      `json:"steps"`
	StartedAt   time.Time         `json:"started_at"`
	CompletedAt time.Time         `json:"completed_at"`
}

type Attestation struct {
	Version         int       `json:"version"`
	ID              string    `json:"id"`
	OwnerID         string    `json:"owner_id"`
	Digest          string    `json:"digest"`
	ExecutionDigest string    `json:"execution_digest"`
	ResultKey       string    `json:"result_key"`
	Status          Status    `json:"status"`
	CacheHit        bool      `json:"cache_hit"`
	CreatedAt       time.Time `json:"created_at"`
}

type Limits struct {
	StepTimeout  time.Duration
	TotalTimeout time.Duration
	OutputBytes  int
}

func DefaultLimits() Limits {
	return Limits{
		StepTimeout:  5 * time.Minute,
		TotalTimeout: 20 * time.Minute,
		OutputBytes:  256 << 10,
	}
}
