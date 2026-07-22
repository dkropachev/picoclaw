package workflows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	WorkflowValidationStatusValid               = "valid"
	WorkflowValidationStatusInvalid             = "invalid"
	WorkflowValidationStatusPendingRevalidation = "pending_revalidation"
	WorkflowValidationStatusNeedsReview         = "needs_review"

	WorkflowEngineVersion    = "4"
	WorkflowSchemaVersion    = "1"
	ValidatorFingerprint     = "picoclaw-workflow-validator-v1"
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
	runtime = NormalizeRuntimeCompatibility(runtime)
	manifest, missing, err := readCompatibilityManifest(workspace)
	if err != nil {
		return nil, err
	}
	defs, err := ListLocal(ctx, workspace, opts...)
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
	runtime = NormalizeRuntimeCompatibility(runtime)
	defs, err := ListLocal(ctx, workspace, opts...)
	if err != nil {
		return nil, err
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
		if hash, hashErr := workflowHash(ctx, workspace, def.Ref, opts...); hashErr == nil {
			stamp.WorkflowHash = hash
		} else {
			stamp.Status = WorkflowValidationStatusInvalid
			stamp.Errors = []WorkflowValidationIssue{{Message: hashErr.Error()}}
		}
		if def.Error != "" {
			stamp.Status = WorkflowValidationStatusInvalid
			stamp.Errors = []WorkflowValidationIssue{{Message: def.Error}}
		} else if workflow, loadErr := LoadLocal(ctx, workspace, def.Ref, opts...); loadErr != nil {
			stamp.Status = WorkflowValidationStatusInvalid
			stamp.Errors = []WorkflowValidationIssue{{Message: loadErr.Error()}}
		} else if validateErr := Validate(workflow); validateErr != nil {
			stamp.Status = WorkflowValidationStatusInvalid
			stamp.Errors = ValidationIssues(validateErr)
		}
		manifest.Workflows[def.Ref] = stamp
	}
	if err := writeCompatibilityManifest(workspace, manifest); err != nil {
		return nil, err
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
	runtime = NormalizeRuntimeCompatibility(runtime)
	canonical, err := CanonicalLocalRef(ref)
	if err != nil {
		return err
	}
	manifest, missing, err := readCompatibilityManifest(workspace)
	if err != nil {
		return err
	}
	if missing || manifest == nil {
		return fmt.Errorf("workflow %s must be revalidated before it can run", canonical)
	}
	hash, err := workflowHash(ctx, workspace, canonical, opts...)
	if err != nil {
		return err
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

func readCompatibilityManifest(workspace string) (*WorkflowCompatibilityManifest, bool, error) {
	data, err := os.ReadFile(compatibilityManifestPath(workspace))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, true, nil
		}
		return nil, false, err
	}
	var manifest WorkflowCompatibilityManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, false, err
	}
	if manifest.Workflows == nil {
		manifest.Workflows = map[string]WorkflowValidationStamp{}
	}
	return &manifest, false, nil
}

func writeCompatibilityManifest(workspace string, manifest *WorkflowCompatibilityManifest) error {
	if manifest == nil {
		return fmt.Errorf("compatibility manifest is required")
	}
	path := compatibilityManifestPath(workspace)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func compatibilityManifestPath(workspace string) string {
	return filepath.Join(workspace, compatibilityManifestDir, compatibilityManifest)
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
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
