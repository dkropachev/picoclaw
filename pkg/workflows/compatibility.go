package workflows

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/sqlitestore"
)

const (
	WorkflowValidationStatusValid               = "valid"
	WorkflowValidationStatusInvalid             = "invalid"
	WorkflowValidationStatusPendingRevalidation = "pending_revalidation"
	WorkflowValidationStatusNeedsReview         = "needs_review"

	WorkflowEngineVersion    = "13"
	WorkflowSchemaVersion    = "7"
	ValidatorFingerprint     = "picoclaw-workflow-validator-v9"
	compatibilityManifestDir = "workflow_validations"
	compatibilityManifest    = "manifest.json"
)

type RuntimeCompatibility struct {
	PicoclawVersion      string `json:"picoclaw_version"`
	GitCommit            string `json:"git_commit,omitempty"`
	WorkflowEngine       string `json:"workflow_engine_version"`
	WorkflowSchema       string `json:"workflow_schema_version"`
	ValidatorFingerprint string `json:"validator_fingerprint"`
}

type WorkflowValidationIssue struct {
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

type WorkflowValidationStamp struct {
	WorkflowRef          string                    `json:"workflow_ref"`
	WorkflowHash         string                    `json:"workflow_hash,omitempty"`
	PicoclawVersion      string                    `json:"validated_against_picoclaw_version"`
	GitCommit            string                    `json:"validated_against_git_commit,omitempty"`
	WorkflowEngine       string                    `json:"workflow_engine_version"`
	WorkflowSchema       string                    `json:"workflow_schema_version"`
	ValidatorFingerprint string                    `json:"validator_fingerprint"`
	Status               string                    `json:"status"`
	Errors               []WorkflowValidationIssue `json:"errors,omitempty"`
	Warnings             []WorkflowValidationIssue `json:"warnings,omitempty"`
	ValidatedAt          time.Time                 `json:"validated_at"`
}

type WorkflowCompatibilityManifest struct {
	PicoclawVersion      string                             `json:"picoclaw_version"`
	GitCommit            string                             `json:"git_commit,omitempty"`
	WorkflowEngine       string                             `json:"workflow_engine_version"`
	WorkflowSchema       string                             `json:"workflow_schema_version"`
	ValidatorFingerprint string                             `json:"validator_fingerprint"`
	UpdatedAt            time.Time                          `json:"updated_at"`
	Workflows            map[string]WorkflowValidationStamp `json:"workflows"`
}

type WorkflowCompatibilitySummary struct {
	Current         RuntimeCompatibility      `json:"current"`
	ManifestRuntime RuntimeCompatibility      `json:"manifest_runtime,omitempty"`
	Workflows       []WorkflowValidationStamp `json:"workflows"`
	Counts          map[string]int            `json:"counts"`
	VersionChanged  bool                      `json:"version_changed"`
	ManifestMissing bool                      `json:"manifest_missing"`
	HasBlocking     bool                      `json:"has_blocking"`
}

// LocalWorkflowSnapshot binds one parsed and validated workflow to the exact
// content revision that produced it. Revision is opaque to callers.
type LocalWorkflowSnapshot struct {
	Ref      string
	Revision string
	Workflow *Workflow
}

var (
	// ErrWorkflowSnapshotAdmissionUnavailable identifies infrastructure or
	// persisted-state failures that prevent a snapshot admission decision.
	ErrWorkflowSnapshotAdmissionUnavailable = errors.New(
		"workflow snapshot admission unavailable",
	)
	// ErrWorkflowSnapshotsNotRunnable identifies a completed compatibility
	// decision that rejected one or more admitted snapshots.
	ErrWorkflowSnapshotsNotRunnable = errors.New(
		"workflow snapshots are not runnable",
	)
)

type workflowCompatibilityOverlay struct {
	ref  string
	data []byte
}

func NormalizeRuntimeCompatibility(runtime RuntimeCompatibility) RuntimeCompatibility {
	runtime.PicoclawVersion = strings.TrimSpace(runtime.PicoclawVersion)
	if runtime.PicoclawVersion == "" {
		runtime.PicoclawVersion = "dev"
	}
	runtime.GitCommit = strings.TrimSpace(runtime.GitCommit)
	runtime.WorkflowEngine = strings.TrimSpace(runtime.WorkflowEngine)
	if runtime.WorkflowEngine == "" {
		runtime.WorkflowEngine = WorkflowEngineVersion
	}
	runtime.WorkflowSchema = strings.TrimSpace(runtime.WorkflowSchema)
	if runtime.WorkflowSchema == "" {
		runtime.WorkflowSchema = WorkflowSchemaVersion
	}
	runtime.ValidatorFingerprint = strings.TrimSpace(runtime.ValidatorFingerprint)
	if runtime.ValidatorFingerprint == "" {
		runtime.ValidatorFingerprint = ValidatorFingerprint
	}
	return runtime
}

func LoadCompatibilitySummary(
	ctx context.Context,
	workspace string,
	runtime RuntimeCompatibility,
	opts ...LocalOption,
) (*WorkflowCompatibilitySummary, error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	unlock, err := lockWorkflowMutation(workspace)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	runtime = NormalizeRuntimeCompatibility(runtime)
	manifest, missing, err := readCompatibilityManifest(workspace)
	if err != nil {
		return nil, err
	}
	defs, err := listLocalLocked(ctx, workspace, opts...)
	if err != nil {
		return nil, err
	}
	summary := &WorkflowCompatibilitySummary{
		Current:         runtime,
		Counts:          map[string]int{},
		VersionChanged:  missing || !manifestRuntimeMatches(manifest, runtime),
		ManifestMissing: missing,
	}
	if manifest != nil {
		summary.ManifestRuntime = RuntimeCompatibility{
			PicoclawVersion:      manifest.PicoclawVersion,
			GitCommit:            manifest.GitCommit,
			WorkflowEngine:       manifest.WorkflowEngine,
			WorkflowSchema:       manifest.WorkflowSchema,
			ValidatorFingerprint: manifest.ValidatorFingerprint,
		}
	}
	stamps := make([]WorkflowValidationStamp, 0, len(defs))
	for _, def := range defs {
		stamp := WorkflowValidationStamp{
			WorkflowRef:          def.Ref,
			PicoclawVersion:      runtime.PicoclawVersion,
			GitCommit:            runtime.GitCommit,
			WorkflowEngine:       runtime.WorkflowEngine,
			WorkflowSchema:       runtime.WorkflowSchema,
			ValidatorFingerprint: runtime.ValidatorFingerprint,
			Status:               WorkflowValidationStatusPendingRevalidation,
			Warnings: []WorkflowValidationIssue{{
				Message: "workflow has not been validated against the current Picoclaw runtime",
			}},
		}
		if manifest != nil {
			if existing, ok := manifest.Workflows[def.Ref]; ok {
				stamp = existing
			}
		}
		currentHash, hashErr := workflowHash(ctx, workspace, def.Ref, opts...)
		matchesCurrentRuntime := stampMatchesRuntime(stamp, runtime, currentHash)
		if !matchesCurrentRuntime {
			if hashErr == nil {
				stamp.WorkflowHash = currentHash
			}
			stamp.PicoclawVersion = runtime.PicoclawVersion
			stamp.GitCommit = runtime.GitCommit
			stamp.WorkflowEngine = runtime.WorkflowEngine
			stamp.WorkflowSchema = runtime.WorkflowSchema
			stamp.ValidatorFingerprint = runtime.ValidatorFingerprint
			stamp.Status = WorkflowValidationStatusPendingRevalidation
			stamp.Errors = nil
			stamp.Warnings = []WorkflowValidationIssue{{
				Message: "workflow must be revalidated after the current Picoclaw runtime or workflow change",
			}}
		} else if hashErr == nil {
			stamp.WorkflowHash = currentHash
		}
		if def.Error != "" && stamp.Status != WorkflowValidationStatusInvalid {
			stamp.Status = WorkflowValidationStatusInvalid
			stamp.Errors = []WorkflowValidationIssue{{Message: def.Error}}
			stamp.Warnings = nil
		}
		if stamp.ValidatedAt.IsZero() {
			stamp.ValidatedAt = time.Time{}
		}
		stamps = append(stamps, stamp)
		summary.Counts[stamp.Status]++
		if stamp.Status == WorkflowValidationStatusInvalid ||
			stamp.Status == WorkflowValidationStatusPendingRevalidation {
			summary.HasBlocking = true
		}
	}
	sort.Slice(stamps, func(i, j int) bool {
		return stamps[i].WorkflowRef < stamps[j].WorkflowRef
	})
	summary.Workflows = stamps
	return summary, nil
}

func RevalidateLocal(
	ctx context.Context,
	workspace string,
	runtime RuntimeCompatibility,
	opts ...LocalOption,
) (*WorkflowCompatibilityManifest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	unlock, err := lockWorkflowMutation(workspace)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return revalidateLocalLocked(ctx, workspace, runtime, opts...)
}

// revalidateLocalLocked rebuilds the compatibility manifest while the caller
// holds the workspace mutation lock.
func revalidateLocalLocked(
	ctx context.Context,
	workspace string,
	runtime RuntimeCompatibility,
	opts ...LocalOption,
) (*WorkflowCompatibilityManifest, error) {
	manifest, err := buildCompatibilityManifestLocked(
		ctx,
		workspace,
		runtime,
		nil,
		opts...,
	)
	if err != nil {
		return nil, err
	}
	if err := writeCompatibilityManifest(workspace, manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

// buildCompatibilityManifestLocked builds a complete manifest without
// activating it. When overlay is non-nil, its exact bytes replace (or add) the
// selected definition in memory so publish can prepare the manifest before it
// changes the target file.
func buildCompatibilityManifestLocked(
	ctx context.Context,
	workspace string,
	runtime RuntimeCompatibility,
	overlay *workflowCompatibilityOverlay,
	opts ...LocalOption,
) (*WorkflowCompatibilityManifest, error) {
	runtime = NormalizeRuntimeCompatibility(runtime)
	defs, err := listLocalLocked(ctx, workspace, opts...)
	if err != nil {
		return nil, err
	}
	if overlay != nil {
		found := false
		for _, def := range defs {
			if def.Ref == overlay.ref {
				found = true
				break
			}
		}
		if !found {
			defs = append(defs, Definition{Ref: overlay.ref})
			sort.Slice(defs, func(i, j int) bool {
				return defs[i].Ref < defs[j].Ref
			})
		}
	}
	now := time.Now().UTC()
	manifest := &WorkflowCompatibilityManifest{
		PicoclawVersion:      runtime.PicoclawVersion,
		GitCommit:            runtime.GitCommit,
		WorkflowEngine:       runtime.WorkflowEngine,
		WorkflowSchema:       runtime.WorkflowSchema,
		ValidatorFingerprint: runtime.ValidatorFingerprint,
		UpdatedAt:            now,
		Workflows:            make(map[string]WorkflowValidationStamp, len(defs)),
	}
	for _, def := range defs {
		stamp := WorkflowValidationStamp{
			WorkflowRef:          def.Ref,
			PicoclawVersion:      runtime.PicoclawVersion,
			GitCommit:            runtime.GitCommit,
			WorkflowEngine:       runtime.WorkflowEngine,
			WorkflowSchema:       runtime.WorkflowSchema,
			ValidatorFingerprint: runtime.ValidatorFingerprint,
			Status:               WorkflowValidationStatusValid,
			ValidatedAt:          now,
		}
		if overlay != nil && def.Ref == overlay.ref {
			stamp.WorkflowHash = workflowHashBytes(overlay.data)
			workflow, parseErr := Parse(overlay.data)
			if parseErr != nil {
				stamp.Status = WorkflowValidationStatusInvalid
				stamp.Errors = ValidationIssues(parseErr)
			} else if validateErr := Validate(workflow); validateErr != nil {
				stamp.Status = WorkflowValidationStatusInvalid
				stamp.Errors = ValidationIssues(validateErr)
			}
		} else if hash, hashErr := workflowHash(ctx, workspace, def.Ref, opts...); hashErr == nil {
			stamp.WorkflowHash = hash
		} else {
			stamp.Status = WorkflowValidationStatusInvalid
			stamp.Errors = []WorkflowValidationIssue{{Message: hashErr.Error()}}
			if def.Error != "" {
				stamp.Errors = []WorkflowValidationIssue{{Message: def.Error}}
			}
		}
		if overlay == nil || def.Ref != overlay.ref {
			if def.Error != "" {
				stamp.Status = WorkflowValidationStatusInvalid
				stamp.Errors = []WorkflowValidationIssue{{Message: def.Error}}
			} else if workflow, loadErr := loadLocalLocked(
				ctx,
				workspace,
				def.Ref,
				opts...,
			); loadErr != nil {
				stamp.Status = WorkflowValidationStatusInvalid
				stamp.Errors = []WorkflowValidationIssue{{Message: loadErr.Error()}}
			} else if validateErr := Validate(workflow); validateErr != nil {
				stamp.Status = WorkflowValidationStatusInvalid
				stamp.Errors = ValidationIssues(validateErr)
			}
		}
		manifest.Workflows[def.Ref] = stamp
	}
	return manifest, nil
}

func EnsureWorkflowRunnable(
	ctx context.Context,
	workspace string,
	ref string,
	runtime RuntimeCompatibility,
	opts ...LocalOption,
) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	unlock, err := lockWorkflowMutation(workspace)
	if err != nil {
		return err
	}
	defer unlock()
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	runtime = NormalizeRuntimeCompatibility(runtime)
	canonical, err := CanonicalLocalRef(ref)
	if err != nil {
		return err
	}
	hash, err := workflowHash(ctx, workspace, canonical, opts...)
	if err != nil {
		return err
	}
	return ensureWorkflowHashRunnable(workspace, canonical, runtime, hash)
}

// LoadRunnableLocalSnapshot reads, compatibility-checks, parses, and validates
// one exact workflow byte snapshot. The compatibility decision therefore
// cannot be separated from execution by a second file read.
func LoadRunnableLocalSnapshot(
	ctx context.Context,
	workspace string,
	ref string,
	runtime RuntimeCompatibility,
	opts ...LocalOption,
) (*Workflow, error) {
	snapshot, err := LoadRunnableLocalSnapshotWithRevision(
		ctx,
		workspace,
		ref,
		runtime,
		opts...,
	)
	if err != nil {
		return nil, err
	}
	return snapshot.Workflow, nil
}

// LoadRunnableLocalSnapshotWithRevision reads, compatibility-checks, parses,
// and validates one exact workflow byte snapshot and returns its opaque
// content revision.
func LoadRunnableLocalSnapshotWithRevision(
	ctx context.Context,
	workspace string,
	ref string,
	runtime RuntimeCompatibility,
	opts ...LocalOption,
) (*LocalWorkflowSnapshot, error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	unlock, err := lockWorkflowMutation(workspace)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	runtime = NormalizeRuntimeCompatibility(runtime)
	local := collectLocalOptions(opts...)
	resolved, err := local.resolver(workspace).ResolveLocal(ref)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(resolved.Path)
	if err != nil {
		return nil, err
	}
	hash := workflowHashBytes(data)
	if compatibilityErr := ensureWorkflowHashRunnable(
		workspace,
		resolved.Canonical,
		runtime,
		hash,
	); compatibilityErr != nil {
		return nil, compatibilityErr
	}
	workflow, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if err := Validate(workflow); err != nil {
		return nil, err
	}
	return &LocalWorkflowSnapshot{
		Ref:      resolved.Canonical,
		Revision: hash,
		Workflow: workflow,
	}, nil
}

// LoadValidatedLocalSnapshot reads, parses, and validates one exact local
// workflow byte snapshot without requiring a runtime compatibility manifest.
func LoadValidatedLocalSnapshot(
	ctx context.Context,
	workspace string,
	ref string,
	opts ...LocalOption,
) (*LocalWorkflowSnapshot, error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	unlock, err := lockWorkflowMutation(workspace)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	local := collectLocalOptions(opts...)
	resolved, err := local.resolver(workspace).ResolveLocal(ref)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(resolved.Path)
	if err != nil {
		return nil, err
	}
	workflow, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if err := Validate(workflow); err != nil {
		return nil, err
	}
	return &LocalWorkflowSnapshot{
		Ref:      resolved.Canonical,
		Revision: workflowHashBytes(data),
		Workflow: workflow,
	}, nil
}

// EnsureWorkflowSnapshotsRunnable checks compatibility for exact, already
// parsed workflow snapshots. Callers can then execute those same snapshots
// without a second definition read opening a compatibility TOCTOU window.
func EnsureWorkflowSnapshotsRunnable(
	ctx context.Context,
	workspace string,
	snapshots []*LocalWorkflowSnapshot,
	runtime RuntimeCompatibility,
) error {
	return WithRunnableWorkflowSnapshots(
		ctx,
		workspace,
		snapshots,
		runtime,
		func() error { return nil },
	)
}

// WithRunnableWorkflowSnapshots holds the workflow mutation lock while it
// validates the exact admitted snapshots and runs operation. Durable run
// creation can be supplied as operation so compatibility revalidation and
// definition publication cannot race the final check/create boundary.
func WithRunnableWorkflowSnapshots(
	ctx context.Context,
	workspace string,
	snapshots []*LocalWorkflowSnapshot,
	runtime RuntimeCompatibility,
	operation func() error,
) error {
	return WithFencedRunnableWorkflowSnapshots(
		ctx,
		workspace,
		snapshots,
		runtime,
		func() error { return nil },
		operation,
	)
}

// WithFencedRunnableWorkflowSnapshots holds the workflow mutation lock while
// fence rechecks current admission state, the exact admitted snapshots are
// compatibility-checked, and operation crosses its durable boundary. The
// ordering lets callers report definition drift before an obsolete snapshot's
// compatibility stamp is considered.
func WithFencedRunnableWorkflowSnapshots(
	ctx context.Context,
	workspace string,
	snapshots []*LocalWorkflowSnapshot,
	runtime RuntimeCompatibility,
	fence func() error,
	operation func() error,
) error {
	return WithGuardedFencedRunnableWorkflowSnapshots(
		ctx,
		workspace,
		snapshots,
		runtime,
		fence,
		func(guarded func() error) error { return guarded() },
		operation,
	)
}

// WithGuardedFencedRunnableWorkflowSnapshots holds the workflow mutation lock,
// runs fence, then enters guard while compatibility-checking the exact admitted
// snapshots and crossing operation's durable boundary. A caller can use guard
// to retain another canonical mutation lock through compatibility and create
// without changing the global workflow-before-dependent-lock ordering.
func WithGuardedFencedRunnableWorkflowSnapshots(
	ctx context.Context,
	workspace string,
	snapshots []*LocalWorkflowSnapshot,
	runtime RuntimeCompatibility,
	fence func() error,
	guard func(func() error) error,
	operation func() error,
) error {
	if fence == nil {
		return fmt.Errorf("workflow snapshot fence is required")
	}
	if guard == nil {
		return fmt.Errorf("workflow snapshot guard is required")
	}
	if operation == nil {
		return fmt.Errorf("workflow snapshot operation is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock, err := lockWorkflowMutation(workspace)
	if err != nil {
		return fmt.Errorf(
			"%w: %v",
			ErrWorkflowSnapshotAdmissionUnavailable,
			err,
		)
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := fence(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return guard(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		runtime = NormalizeRuntimeCompatibility(runtime)
		if err := ensureWorkflowSnapshotsRunnableLocked(
			workspace,
			snapshots,
			runtime,
		); err != nil {
			if errors.Is(err, ErrWorkflowSnapshotAdmissionUnavailable) {
				return err
			}
			return fmt.Errorf("%w: %v", ErrWorkflowSnapshotsNotRunnable, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return operation()
	})
}

func ensureWorkflowSnapshotsRunnableLocked(
	workspace string,
	snapshots []*LocalWorkflowSnapshot,
	runtime RuntimeCompatibility,
) error {
	ordered := append([]*LocalWorkflowSnapshot(nil), snapshots...)
	if len(ordered) == 0 {
		return fmt.Errorf("at least one workflow snapshot is required")
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i] == nil {
			return ordered[j] != nil
		}
		if ordered[j] == nil {
			return false
		}
		return ordered[i].Ref < ordered[j].Ref
	})
	for _, snapshot := range ordered {
		if snapshot == nil || snapshot.Workflow == nil {
			return fmt.Errorf("workflow snapshot is required")
		}
		canonical, canonicalErr := CanonicalLocalRef(snapshot.Ref)
		if canonicalErr != nil {
			return canonicalErr
		}
		if canonical != snapshot.Ref || strings.TrimSpace(snapshot.Revision) == "" {
			return fmt.Errorf("workflow snapshot %s is invalid", snapshot.Ref)
		}
		if validationErr := Validate(snapshot.Workflow); validationErr != nil {
			return validationErr
		}
		if compatibilityErr := ensureWorkflowHashRunnable(
			workspace,
			canonical,
			runtime,
			snapshot.Revision,
		); compatibilityErr != nil {
			return compatibilityErr
		}
	}
	return nil
}

func ensureWorkflowHashRunnable(
	workspace string,
	canonical string,
	runtime RuntimeCompatibility,
	hash string,
) error {
	manifest, missing, err := readCompatibilityManifest(workspace)
	if err != nil {
		return fmt.Errorf(
			"%w: %v",
			ErrWorkflowSnapshotAdmissionUnavailable,
			err,
		)
	}
	if missing || manifest == nil {
		return fmt.Errorf("workflow %s must be revalidated before it can run", canonical)
	}
	stamp, ok := manifest.Workflows[canonical]
	if !ok {
		return fmt.Errorf("workflow %s has not been validated against the current Picoclaw runtime", canonical)
	}
	if !stampMatchesRuntime(stamp, runtime, hash) {
		return fmt.Errorf(
			"workflow %s must be revalidated after the current Picoclaw runtime or workflow change",
			canonical,
		)
	}
	if stamp.Status != WorkflowValidationStatusValid && stamp.Status != WorkflowValidationStatusNeedsReview {
		return fmt.Errorf("workflow %s cannot run while validation status is %s", canonical, stamp.Status)
	}
	return nil
}

func ValidationIssues(err error) []WorkflowValidationIssue {
	if err == nil {
		return nil
	}
	if validationErrs, ok := err.(ValidationErrors); ok {
		issues := make([]WorkflowValidationIssue, 0, len(validationErrs))
		for _, item := range validationErrs {
			issues = append(issues, WorkflowValidationIssue{
				Path:    item.Path,
				Message: item.Message,
			})
		}
		return issues
	}
	return []WorkflowValidationIssue{{Message: err.Error()}}
}

func manifestRuntimeMatches(manifest *WorkflowCompatibilityManifest, runtime RuntimeCompatibility) bool {
	if manifest == nil {
		return false
	}
	return manifest.PicoclawVersion == runtime.PicoclawVersion &&
		manifest.GitCommit == runtime.GitCommit &&
		manifest.WorkflowEngine == runtime.WorkflowEngine &&
		manifest.WorkflowSchema == runtime.WorkflowSchema &&
		manifest.ValidatorFingerprint == runtime.ValidatorFingerprint
}

func stampMatchesRuntime(stamp WorkflowValidationStamp, runtime RuntimeCompatibility, hash string) bool {
	return stamp.PicoclawVersion == runtime.PicoclawVersion &&
		stamp.GitCommit == runtime.GitCommit &&
		stamp.WorkflowEngine == runtime.WorkflowEngine &&
		stamp.WorkflowSchema == runtime.WorkflowSchema &&
		stamp.ValidatorFingerprint == runtime.ValidatorFingerprint &&
		(hash == "" || stamp.WorkflowHash == hash)
}

//nolint:govet // Short-lived row errors stay scoped to their exact read boundary.
func readCompatibilityManifest(workspace string) (*WorkflowCompatibilityManifest, bool, error) {
	ctx := context.Background()
	db, release, err := borrowWorkflowDatabase(ctx, workspace)
	if err != nil {
		return nil, false, err
	}
	defer release()
	manifest := &WorkflowCompatibilityManifest{Workflows: map[string]WorkflowValidationStamp{}}
	var seconds, nanos int64
	err = db.QueryRowContext(ctx, `SELECT picoclaw_version,git_commit,workflow_engine,
		workflow_schema,validator_fingerprint,updated_at_seconds,updated_at_nanosecond
		FROM workflow_compatibility_runtime WHERE singleton=1`).Scan(&manifest.PicoclawVersion,
		&manifest.GitCommit, &manifest.WorkflowEngine, &manifest.WorkflowSchema,
		&manifest.ValidatorFingerprint, &seconds, &nanos)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	manifest.UpdatedAt = workflowTime(seconds, nanos)
	if err := func() error {
		rows, queryErr := db.QueryContext(ctx, `SELECT workflow_ref,workflow_hash,picoclaw_version,
			git_commit,workflow_engine,workflow_schema,validator_fingerprint,status,
			validated_at_seconds,validated_at_nanosecond FROM workflow_validation_stamps
			ORDER BY workflow_ref`)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var stamp WorkflowValidationStamp
			if scanErr := rows.Scan(&stamp.WorkflowRef, &stamp.WorkflowHash, &stamp.PicoclawVersion,
				&stamp.GitCommit, &stamp.WorkflowEngine, &stamp.WorkflowSchema,
				&stamp.ValidatorFingerprint, &stamp.Status, &seconds, &nanos); scanErr != nil {
				return scanErr
			}
			stamp.ValidatedAt = workflowTime(seconds, nanos)
			manifest.Workflows[stamp.WorkflowRef] = stamp
		}
		return rows.Err()
	}(); err != nil {
		return nil, false, err
	}
	issueRows, err := db.QueryContext(ctx, `SELECT workflow_ref,issue_kind,path_text,message
		FROM workflow_validation_issues ORDER BY workflow_ref,issue_kind,position`)
	if err != nil {
		return nil, false, err
	}
	defer issueRows.Close()
	for issueRows.Next() {
		var ref, kind string
		var issue WorkflowValidationIssue
		if err := issueRows.Scan(&ref, &kind, &issue.Path, &issue.Message); err != nil {
			return nil, false, err
		}
		stamp, exists := manifest.Workflows[ref]
		if !exists {
			return nil, false, fmt.Errorf("workflow validation issue has no stamp")
		}
		if kind == "error" {
			stamp.Errors = append(stamp.Errors, issue)
		} else {
			stamp.Warnings = append(stamp.Warnings, issue)
		}
		manifest.Workflows[ref] = stamp
	}
	if err := issueRows.Err(); err != nil {
		return nil, false, err
	}
	return manifest, false, nil
}

func writeCompatibilityManifest(workspace string, manifest *WorkflowCompatibilityManifest) error {
	if manifest == nil {
		return fmt.Errorf("compatibility manifest is required")
	}
	ctx := context.Background()
	db, release, err := borrowWorkflowDatabase(ctx, workspace)
	if err != nil {
		return err
	}
	defer release()
	return sqlitestore.Immediate(ctx, db, func(conn *sql.Conn) error {
		return writeCompatibilityManifestConn(ctx, conn, manifest)
	})
}

func writeCompatibilityManifestConn(
	ctx context.Context,
	conn *sql.Conn,
	manifest *WorkflowCompatibilityManifest,
) error {
	if manifest == nil {
		return fmt.Errorf("compatibility manifest is required")
	}
	encoded, err := json.Marshal(manifest)
	if err != nil || int64(len(encoded)) > maximumWorkflowManifestBytes {
		return fmt.Errorf("compatibility manifest exceeds its storage limit")
	}
	seconds, nanos, err := workflowTimestamp(manifest.UpdatedAt)
	if err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO workflow_compatibility_runtime
			(singleton,picoclaw_version,git_commit,workflow_engine,workflow_schema,
			validator_fingerprint,updated_at_seconds,updated_at_nanosecond,version)
			VALUES(1,?,?,?,?,?,?,?,1) ON CONFLICT(singleton) DO UPDATE SET
			picoclaw_version=excluded.picoclaw_version,git_commit=excluded.git_commit,
			workflow_engine=excluded.workflow_engine,workflow_schema=excluded.workflow_schema,
			validator_fingerprint=excluded.validator_fingerprint,
			updated_at_seconds=excluded.updated_at_seconds,
			updated_at_nanosecond=excluded.updated_at_nanosecond,
			version=workflow_compatibility_runtime.version+1`, manifest.PicoclawVersion,
		manifest.GitCommit, manifest.WorkflowEngine, manifest.WorkflowSchema,
		manifest.ValidatorFingerprint, seconds, nanos); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM workflow_validation_stamps`); err != nil {
		return err
	}
	for _, ref := range sortedStringKeys(manifest.Workflows) {
		stamp := manifest.Workflows[ref]
		if stamp.WorkflowRef == "" {
			stamp.WorkflowRef = ref
		}
		if stamp.WorkflowRef != ref {
			return fmt.Errorf("workflow validation identity mismatch")
		}
		if err := insertWorkflowValidationStamp(ctx, conn, stamp); err != nil {
			return err
		}
	}
	return validateWorkflowChildAggregateLimitsConn(ctx, conn)
}

func compatibilityManifestPath(workspace string) string {
	return filepath.Join(workspace, compatibilityManifestDir, compatibilityManifest)
}

func checkedCompatibilityManifestPath(workspace string) (string, error) {
	return resolveWorkflowInternalPath(
		workspace,
		compatibilityManifestDir,
		compatibilityManifest,
	)
}

func workflowHash(ctx context.Context, workspace, ref string, opts ...LocalOption) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	local := collectLocalOptions(opts...)
	resolved, err := local.resolver(workspace).ResolveLocal(ref)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(resolved.Path)
	if err != nil {
		return "", err
	}
	return workflowHashBytes(data), nil
}

func workflowHashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
