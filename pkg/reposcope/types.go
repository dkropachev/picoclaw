// Package reposcope builds a safe, immutable source corpus from a repository
// inventory. It deliberately has no dependency on workflow or transport types.
package reposcope

import "errors"

const (
	// DefaultPerLanguageQuota is used when a selection policy does not specify a
	// quota. A quota applies independently to every detected eligible language.
	DefaultPerLanguageQuota = 20
	// MaxPerLanguageQuota is an invariant, not merely a UI recommendation.
	MaxPerLanguageQuota = 20
	// DefaultPreferredMinBytes makes substantial files win deterministic ties;
	// smaller files remain eligible so uncommon languages are still represented.
	DefaultPreferredMinBytes int64 = 4 << 10
	// DefaultMaxFileBytes bounds model input and protects inventory consumers.
	DefaultMaxFileBytes int64 = 2 << 20
	// AbsoluteMaxFileBytes prevents callers from accidentally disabling the
	// oversized-file safety boundary.
	AbsoluteMaxFileBytes int64 = 4 << 20
	// MaxFreeTextBytes bounds persisted user guidance. It is never interpreted
	// by this package as permission to widen the hard scope.
	MaxFreeTextBytes = 16 << 10
	// MaxSampleBytes bounds inspection work even if a caller accidentally passes
	// an entire blob instead of a leading sample.
	MaxSampleBytes = 64 << 10
	// MaxRepositoryPathBytes follows the practical Git/platform path ceiling
	// while rejecting pathological inventory input.
	MaxRepositoryPathBytes = 4 << 10
	// MaxScopePrefixes prevents an untrusted request from causing quadratic
	// admission work over a repository inventory.
	MaxScopePrefixes = 512
)

var (
	ErrInvalidScope       = errors.New("invalid repository scope")
	ErrInvalidInventory   = errors.New("invalid repository inventory")
	ErrInvalidPolicy      = errors.New("invalid repository selection policy")
	ErrInvalidCandidate   = errors.New("invalid repository candidate")
	ErrUnknownCandidate   = errors.New("AI selected an unknown candidate ID")
	ErrDuplicateCandidate = errors.New("AI selected a candidate ID more than once")
	ErrQuotaExceeded      = errors.New("AI selection exceeds a per-language quota")
)

// Language is a stable, lower-case language identifier suitable for map keys
// and persisted plans.
type Language string

// CodeType is the independently selectable role of a source file.
type CodeType string

const (
	CodeTypeHotpath   CodeType = "hotpath-code"
	CodeTypeCode      CodeType = "code"
	CodeTypeTest      CodeType = "test"
	CodeTypeBenchTest CodeType = "bench-test"
)

// Scope is the user-controlled hard boundary. IncludePrefixes and
// ExcludePrefixes are repository-relative directory prefixes. Empty includes
// mean the repository root; exclusions always win. FreeText is retained for a
// later AI prompt but is intentionally ignored by Allows.
type Scope struct {
	IncludePrefixes []string   `json:"includePrefixes,omitempty"`
	ExcludePrefixes []string   `json:"excludePrefixes,omitempty"`
	CodeTypes       []CodeType `json:"codeTypes,omitempty"`
	FreeText        string     `json:"freeText,omitempty"`
}

// FileKind describes a tree entry without exposing an operating-system mode.
type FileKind string

const (
	FileKindRegular   FileKind = "regular"
	FileKindSymlink   FileKind = "symlink"
	FileKindDirectory FileKind = "directory"
	FileKindOther     FileKind = "other"
)

// FileMetadata is bounded inventory input. Sample should contain at most a
// small leading portion of the blob; BuildCandidates never retains it.
type FileMetadata struct {
	Path      string   `json:"path"`
	BlobID    string   `json:"blobId"`
	Size      int64    `json:"size"`
	Kind      FileKind `json:"kind"`
	Binary    bool     `json:"binary,omitempty"`
	LFS       bool     `json:"lfs,omitempty"`
	Generated bool     `json:"generated,omitempty"`
	Sample    []byte   `json:"-"`
}

// Inventory identifies one immutable repository snapshot.
type Inventory struct {
	CommitID string         `json:"commitId"`
	ID       string         `json:"inventoryId"`
	Files    []FileMetadata `json:"files"`
}

// BuildOptions controls corpus safety limits. A zero MaxFileBytes selects the
// safe default; values above AbsoluteMaxFileBytes are rejected.
type BuildOptions struct {
	MaxFileBytes int64 `json:"maxFileBytes,omitempty"`
}

// Candidate is an immutable, content-addressed choice presented to an AI.
// Consumers should expose ID, not repository paths, in the selection protocol.
type Candidate struct {
	ID          string   `json:"id"`
	CommitID    string   `json:"commitId"`
	InventoryID string   `json:"inventoryId"`
	Path        string   `json:"path"`
	BlobID      string   `json:"blobId"`
	Size        int64    `json:"size"`
	Language    Language `json:"language"`
	CodeType    CodeType `json:"codeType"`
	Region      string   `json:"region"`
	Module      string   `json:"module"`
}

// RejectionReason is stable enough for progress counters and audit logs.
type RejectionReason string

const (
	RejectUnsafePath   RejectionReason = "unsafe-path"
	RejectDuplicate    RejectionReason = "duplicate-path"
	RejectNonRegular   RejectionReason = "non-regular"
	RejectVendor       RejectionReason = "vendor-or-dependency"
	RejectBuildOutput  RejectionReason = "build-output"
	RejectGenerated    RejectionReason = "generated"
	RejectBinary       RejectionReason = "binary"
	RejectLockFile     RejectionReason = "lock-file"
	RejectLFS          RejectionReason = "lfs-pointer"
	RejectEmpty        RejectionReason = "empty"
	RejectOversized    RejectionReason = "oversized"
	RejectUnknownLang  RejectionReason = "unknown-language"
	RejectOutsideScope RejectionReason = "outside-scope"
	RejectInvalidBlob  RejectionReason = "invalid-blob-id"
)

// Rejection explains why an inventory entry was not made available.
type Rejection struct {
	Path   string          `json:"path"`
	Reason RejectionReason `json:"reason"`
}

// SelectionPolicy applies a quota independently to each language. PerLanguage
// overrides the default, while zero DefaultPerLanguage selects the package
// default. Quotas must remain between one and MaxPerLanguageQuota so every
// eligible detected language is represented.
type SelectionPolicy struct {
	DefaultPerLanguage int              `json:"defaultPerLanguage,omitempty"`
	PerLanguage        map[Language]int `json:"perLanguage,omitempty"`
	PreferredMinBytes  int64            `json:"preferredMinBytes,omitempty"`
}

// AISelection contains only opaque candidate IDs. Paths and classifications
// from an AI are never accepted as authority.
type AISelection struct {
	CandidateIDs []string `json:"candidateIds"`
}

// SelectionResult distinguishes AI choices from deterministic quota fills.
type SelectionResult struct {
	Selected      []Candidate `json:"selected"`
	AcceptedAIIDs []string    `json:"acceptedAiIds,omitempty"`
	FilledIDs     []string    `json:"filledIds,omitempty"`
}
