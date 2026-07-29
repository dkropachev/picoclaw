package api

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

const workflowDependencyCheckRequestMaxBytes = 1 << 20

var (
	errWorkflowDependencyInvalidRequest = errors.New("invalid workflow dependency request")
	errWorkflowDependencyNotFound       = errors.New("workflow dependency root not found")
	errWorkflowDependencyInvalid        = errors.New("workflow dependency root is invalid")
	errWorkflowDependencyUnavailable    = errors.New("workflow dependency check unavailable")
	errWorkflowDependencyRevisionStale  = errors.New("workflow dependency revision mismatch")
	errWorkflowDependenciesNotReady     = errors.New("workflow dependencies are not ready")
)

type workflowDependencyDraftRequest struct {
	TargetRef string  `json:"target_ref"`
	YAML      *string `json:"yaml"`
}

type workflowDependencyCheckRequest struct {
	Ref   string                          `json:"ref,omitempty"`
	Draft *workflowDependencyDraftRequest `json:"draft,omitempty"`
}

type workflowDependencyCheckResponse struct {
	RootRef          string                                  `json:"root_ref"`
	Revision         string                                  `json:"revision"`
	Ready            bool                                    `json:"ready"`
	WorkflowEnabled  bool                                    `json:"workflow_enabled"`
	StructuralReady  bool                                    `json:"structural_ready"`
	RuntimeReady     bool                                    `json:"runtime_ready"`
	Dependencies     []workflows.WorkflowDependencyReadiness `json:"dependencies"`
	StructuralIssues []workflows.WorkflowDependencyIssue     `json:"structural_issues"`
}

type workflowDependencySnapshotLoader struct {
	resolver  workflows.Resolver
	revisions map[string]string
	snapshots map[string]*workflows.LocalWorkflowSnapshot
	bytesRead int64
}

func (l *workflowDependencySnapshotLoader) LoadReusableWorkflow(
	ctx context.Context,
	ref string,
) (*workflows.Workflow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	canonical, err := workflows.CanonicalLocalRef(ref)
	if err != nil {
		return nil, err
	}
	resolved, err := l.resolver.ResolveLocal(canonical)
	if err != nil {
		l.revisions[canonical] = "unavailable"
		return nil, err
	}
	data, err := readWorkflowDependencyDefinition(
		resolved.Path,
		workflows.MaxWorkflowDependencyTotalBytes-l.bytesRead,
	)
	if err != nil {
		l.revisions[canonical] = "unavailable"
		return nil, err
	}
	l.bytesRead += int64(len(data))
	revision := workflowDependencyContentRevision(data)
	l.revisions[canonical] = revision
	workflow, err := workflows.Parse(data)
	if err != nil {
		return nil, err
	}
	l.snapshots[canonical] = &workflows.LocalWorkflowSnapshot{
		Ref:      canonical,
		Revision: revision,
		Workflow: workflow,
	}
	return workflow, nil
}

type workflowDependencyAdmission struct {
	Response       *workflowDependencyCheckResponse
	Config         *config.Config
	ConfigRevision string
	Snapshots      map[string]*workflows.LocalWorkflowSnapshot
}

func (a *workflowDependencyAdmission) orderedSnapshots() []*workflows.LocalWorkflowSnapshot {
	if a == nil {
		return nil
	}
	refs := make([]string, 0, len(a.Snapshots))
	for ref := range a.Snapshots {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	snapshots := make([]*workflows.LocalWorkflowSnapshot, 0, len(refs))
	for _, ref := range refs {
		snapshots = append(snapshots, a.Snapshots[ref])
	}
	return snapshots
}

func (h *Handler) handleCheckWorkflowDependencies(w http.ResponseWriter, r *http.Request) {
	var request workflowDependencyCheckRequest
	if err := decodeWorkflowDependencyCheckRequest(w, r, &request); err != nil {
		var maximum *http.MaxBytesError
		if errors.As(err, &maximum) {
			writeWorkflowDependencyError(w, http.StatusRequestEntityTooLarge, "dependency_request_too_large")
			return
		}
		writeWorkflowDependencyError(w, http.StatusBadRequest, "invalid_dependency_request")
		return
	}

	cfg, configRevision, err := loadStableWorkflowDependencyConfig(h.configPath)
	if err != nil {
		writeWorkflowDependencyError(w, http.StatusServiceUnavailable, "dependency_check_unavailable")
		return
	}
	rootRef, raw, err := workflowDependencyRequestRoot(
		r.Context(),
		cfg,
		request,
	)
	if err != nil {
		switch {
		case errors.Is(err, errWorkflowDependencyInvalidRequest):
			writeWorkflowDependencyError(w, http.StatusBadRequest, "invalid_dependency_request")
		case errors.Is(err, errWorkflowDependencyNotFound):
			writeWorkflowDependencyError(w, http.StatusNotFound, "workflow_not_found")
		case errors.Is(err, errWorkflowDependencyInvalid):
			writeWorkflowDependencyError(w, http.StatusUnprocessableEntity, "workflow_invalid")
		default:
			writeWorkflowDependencyError(w, http.StatusServiceUnavailable, "dependency_check_unavailable")
		}
		return
	}
	response, err := h.evaluateWorkflowDependencies(
		r.Context(),
		cfg,
		configRevision,
		rootRef,
		raw,
	)
	if err != nil {
		if errors.Is(err, errWorkflowDependencyInvalid) {
			writeWorkflowDependencyError(w, http.StatusUnprocessableEntity, "workflow_invalid")
			return
		}
		writeWorkflowDependencyError(w, http.StatusServiceUnavailable, "dependency_check_unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeWorkflowJSON(w, response)
}

// evaluateCurrentWorkflowDependencies reloads the current config and evaluates
// the exact supplied draft. Publish uses this inside the workflow mutation
// transaction so the dependency revision is re-derived at activation time.
func (h *Handler) evaluateCurrentWorkflowDependencies(
	ctx context.Context,
	rootRef string,
	raw string,
) (*workflowDependencyCheckResponse, error) {
	cfg, revision, err := loadStableWorkflowDependencyConfig(h.configPath)
	if err != nil {
		return nil, errWorkflowDependencyUnavailable
	}
	return h.evaluateWorkflowDependencies(ctx, cfg, revision, rootRef, raw)
}

// evaluatePublishedWorkflowDependencyAdmission evaluates and retains the
// exact config and parsed workflow closure that produced the readiness
// revision. Run admission hands these snapshots to the executor rather than
// independently reloading mutable state.
func (h *Handler) evaluatePublishedWorkflowDependencyAdmission(
	ctx context.Context,
	ref string,
) (*workflowDependencyAdmission, error) {
	cfg, revision, err := loadStableWorkflowDependencyConfig(h.configPath)
	if err != nil {
		return nil, errWorkflowDependencyUnavailable
	}
	return h.evaluatePublishedWorkflowDependencyAdmissionFromConfig(
		ctx,
		cfg,
		revision,
		ref,
	)
}

func (h *Handler) evaluatePublishedWorkflowDependencyAdmissionFromConfig(
	ctx context.Context,
	cfg *config.Config,
	configRevision string,
	ref string,
) (*workflowDependencyAdmission, error) {
	rootRef, raw, err := workflowDependencyRequestRoot(
		ctx,
		cfg,
		workflowDependencyCheckRequest{Ref: ref},
	)
	if err != nil {
		return nil, err
	}
	return h.evaluateWorkflowDependencyAdmission(
		ctx,
		cfg,
		configRevision,
		rootRef,
		raw,
	)
}

func (h *Handler) requirePublishedWorkflowDependenciesReady(
	ctx context.Context,
	ref string,
	expectedRevision string,
) (*workflowDependencyAdmission, error) {
	admission, err := h.evaluatePublishedWorkflowDependencyAdmission(ctx, ref)
	return requireWorkflowDependencyAdmissionReady(
		admission,
		err,
		expectedRevision,
	)
}

func (h *Handler) requirePublishedWorkflowDependenciesReadyFromConfig(
	ctx context.Context,
	cfg *config.Config,
	configRevision string,
	ref string,
	expectedRevision string,
) (*workflowDependencyAdmission, error) {
	admission, err := h.evaluatePublishedWorkflowDependencyAdmissionFromConfig(
		ctx,
		cfg,
		configRevision,
		ref,
	)
	return requireWorkflowDependencyAdmissionReady(
		admission,
		err,
		expectedRevision,
	)
}

func requireWorkflowDependencyAdmissionReady(
	admission *workflowDependencyAdmission,
	err error,
	expectedRevision string,
) (*workflowDependencyAdmission, error) {
	if err != nil {
		switch {
		case errors.Is(err, errWorkflowDependencyRevisionStale):
			return nil, errWorkflowDependencyRevisionStale
		case errors.Is(err, errWorkflowDependencyInvalidRequest),
			errors.Is(err, errWorkflowDependencyNotFound),
			errors.Is(err, errWorkflowDependencyInvalid):
			return nil, errWorkflowDependenciesNotReady
		default:
			return nil, errWorkflowDependencyUnavailable
		}
	}
	if expectedRevision != "" && expectedRevision != admission.Response.Revision {
		return nil, errWorkflowDependencyRevisionStale
	}
	if !admission.Response.Ready {
		return nil, errWorkflowDependenciesNotReady
	}
	return admission, nil
}

// fenceWorkflowDependencyAdmission rechecks readiness at the executor's
// durable-create boundary while the workflow mutation lock is held.
func (h *Handler) fenceWorkflowDependencyAdmission(
	ctx context.Context,
	original *workflowDependencyAdmission,
	expectedRevision string,
) error {
	if original == nil || original.Response == nil {
		return errWorkflowDependencyUnavailable
	}
	current, err := h.requirePublishedWorkflowDependenciesReady(
		ctx,
		original.Response.RootRef,
		expectedRevision,
	)
	if err != nil {
		return err
	}
	if current.ConfigRevision != original.ConfigRevision {
		return errWorkflowDependencyRevisionStale
	}
	if current.Response.Revision != original.Response.Revision {
		return errWorkflowDependencyRevisionStale
	}
	return nil
}

// guardWorkflowDependencyAdmissionConfig reacquires the canonical config
// mutation lock after the full readiness fence, rejects any intervening file
// generation, and retains the lock through compatibility and durable create.
// Its caller already owns the workflow mutation lock, preserving the global
// workflow-before-config lock order used by workflow settings mutations.
func (h *Handler) guardWorkflowDependencyAdmissionConfig(
	original *workflowDependencyAdmission,
	operation func() error,
) error {
	if original == nil ||
		strings.TrimSpace(original.ConfigRevision) == "" ||
		operation == nil {
		return errWorkflowDependencyUnavailable
	}
	locked := false
	err := config.WithConfigMutationLock(h.configPath, func() error {
		locked = true
		currentRevision, revisionErr := config.ConfigRevision(h.configPath)
		if revisionErr != nil {
			return fmt.Errorf(
				"%w: %v",
				workflows.ErrWorkflowSnapshotAdmissionUnavailable,
				revisionErr,
			)
		}
		if currentRevision != original.ConfigRevision {
			return errWorkflowDependencyRevisionStale
		}
		return operation()
	})
	if err != nil && !locked {
		return fmt.Errorf(
			"%w: %v",
			workflows.ErrWorkflowSnapshotAdmissionUnavailable,
			err,
		)
	}
	return err
}

func writeWorkflowRunDependencyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errWorkflowDependencyRevisionStale):
		writeWorkflowDependencyError(
			w,
			http.StatusConflict,
			"dependency_revision_mismatch",
		)
	case errors.Is(err, errWorkflowDependenciesNotReady):
		writeWorkflowDependencyError(
			w,
			http.StatusConflict,
			"workflow_dependencies_not_ready",
		)
	default:
		writeWorkflowDependencyError(
			w,
			http.StatusServiceUnavailable,
			"dependency_check_unavailable",
		)
	}
}

func isWorkflowRunDependencyError(err error) bool {
	return errors.Is(err, errWorkflowDependencyRevisionStale) ||
		errors.Is(err, errWorkflowDependenciesNotReady) ||
		errors.Is(err, errWorkflowDependencyUnavailable)
}

func (h *Handler) evaluateWorkflowDependencies(
	ctx context.Context,
	cfg *config.Config,
	configRevision string,
	rootRef string,
	raw string,
) (*workflowDependencyCheckResponse, error) {
	admission, err := h.evaluateWorkflowDependencyAdmission(
		ctx,
		cfg,
		configRevision,
		rootRef,
		raw,
	)
	if err != nil {
		return nil, err
	}
	return admission.Response, nil
}

func (h *Handler) evaluateWorkflowDependencyAdmission(
	ctx context.Context,
	cfg *config.Config,
	configRevision string,
	rootRef string,
	raw string,
) (*workflowDependencyAdmission, error) {
	if cfg == nil {
		return nil, errWorkflowDependencyUnavailable
	}
	canonical, err := workflows.CanonicalLocalRef(rootRef)
	if err != nil {
		return nil, errWorkflowDependencyInvalidRequest
	}
	workflow, err := workflows.Parse([]byte(raw))
	if err != nil {
		return nil, errWorkflowDependencyInvalid
	}
	if validationErr := workflows.Validate(workflow); validationErr != nil {
		return nil, errWorkflowDependencyInvalid
	}

	rootRevision := workflowDependencyContentRevision([]byte(raw))
	loader := &workflowDependencySnapshotLoader{
		resolver: workflows.Resolver{
			WorkspaceDir:   cfg.WorkspacePath(),
			DefinitionsDir: cfg.Workflows.EffectiveDefinitionsDir(),
		},
		revisions: map[string]string{
			canonical: rootRevision,
		},
		snapshots: map[string]*workflows.LocalWorkflowSnapshot{
			canonical: {
				Ref:      canonical,
				Revision: rootRevision,
				Workflow: workflow,
			},
		},
	}
	closure, err := workflows.CheckWorkflowDependencyClosure(
		ctx,
		workflows.WorkflowDependencyCheckRequest{
			RootRef:      canonical,
			RootWorkflow: workflow,
			Loader:       loader,
			MaxCallDepth: cfg.Workflows.EffectiveMaxCallDepth(),
		},
	)
	if err != nil {
		return nil, errWorkflowDependencyUnavailable
	}

	runtime := newWorkflowDependencyRuntime(h.configPath, cfg)
	if runtime == nil {
		return nil, errWorkflowDependencyUnavailable
	}
	defer func() {
		_ = runtime.Close()
	}()
	readiness, err := workflows.ResolveWorkflowDependencyReadiness(
		ctx,
		closure.Dependencies,
		runtime,
	)
	if err != nil {
		return nil, errWorkflowDependencyUnavailable
	}
	runtimeReady := true
	for _, item := range readiness {
		if !item.Ready {
			runtimeReady = false
			break
		}
	}
	structuralReady := closure.Ready()
	response := &workflowDependencyCheckResponse{
		RootRef:          canonical,
		WorkflowEnabled:  cfg.Workflows.Enabled,
		StructuralReady:  structuralReady,
		RuntimeReady:     runtimeReady,
		Ready:            cfg.Workflows.Enabled && structuralReady && runtimeReady,
		Dependencies:     readiness,
		StructuralIssues: closure.Issues,
	}
	latestConfigRevision, err := config.ConfigRevision(h.configPath)
	if err != nil {
		return nil, errWorkflowDependencyUnavailable
	}
	if latestConfigRevision != configRevision {
		return nil, errWorkflowDependencyRevisionStale
	}
	response.Revision, err = workflowDependencyEvaluationRevision(
		configRevision,
		raw,
		loader.revisions,
		response,
	)
	if err != nil {
		return nil, errWorkflowDependencyUnavailable
	}
	return &workflowDependencyAdmission{
		Response:       response,
		Config:         cfg,
		ConfigRevision: configRevision,
		Snapshots:      loader.snapshots,
	}, nil
}

func workflowDependencyRequestRoot(
	ctx context.Context,
	cfg *config.Config,
	request workflowDependencyCheckRequest,
) (string, string, error) {
	if cfg == nil {
		return "", "", errWorkflowDependencyUnavailable
	}
	ref := strings.TrimSpace(request.Ref)
	if (ref == "") == (request.Draft == nil) {
		return "", "", errWorkflowDependencyInvalidRequest
	}
	if request.Draft != nil {
		if request.Draft.YAML == nil {
			return "", "", errWorkflowDependencyInvalidRequest
		}
		targetRef, err := workflows.CanonicalLocalRef(request.Draft.TargetRef)
		if err != nil {
			return "", "", errWorkflowDependencyInvalidRequest
		}
		return targetRef, *request.Draft.YAML, nil
	}
	canonical, err := workflows.CanonicalLocalRef(ref)
	if err != nil {
		return "", "", errWorkflowDependencyInvalidRequest
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return "", "", errWorkflowDependencyUnavailable
	}
	resolved, err := (workflows.Resolver{
		WorkspaceDir:   cfg.WorkspacePath(),
		DefinitionsDir: cfg.Workflows.EffectiveDefinitionsDir(),
	}).ResolveLocal(canonical)
	if err != nil {
		return "", "", errWorkflowDependencyNotFound
	}
	data, err := readWorkflowDependencyDefinition(
		resolved.Path,
		workflows.MaxWorkflowDependencyDefinitionBytes,
	)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", errWorkflowDependencyNotFound
		}
		return "", "", errWorkflowDependencyUnavailable
	}
	return canonical, string(data), nil
}

func readWorkflowDependencyDefinition(path string, remaining int64) ([]byte, error) {
	if remaining <= 0 {
		return nil, workflows.ErrWorkflowDependencyAnalysisLimitExceeded
	}
	limit := workflows.MaxWorkflowDependencyDefinitionBytes
	if remaining < limit {
		limit = remaining
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, workflows.ErrWorkflowDependencyAnalysisLimitExceeded
	}
	return data, nil
}

func decodeWorkflowDependencyCheckRequest(
	w http.ResponseWriter,
	r *http.Request,
	destination *workflowDependencyCheckRequest,
) error {
	if r.Body == nil {
		return io.EOF
	}
	decoder := json.NewDecoder(http.MaxBytesReader(
		w,
		r.Body,
		workflowDependencyCheckRequestMaxBytes,
	))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("workflow dependency request contains multiple JSON values")
		}
		return err
	}
	return nil
}

func loadStableWorkflowDependencyConfig(path string) (*config.Config, string, error) {
	return config.LoadConfigSnapshot(path)
}

func workflowDependencyEvaluationRevision(
	configRevision string,
	rootRaw string,
	revisions map[string]string,
	response *workflowDependencyCheckResponse,
) (string, error) {
	digest := sha256.New()
	writeWorkflowDependencyRevisionPart(digest, []byte(configRevision))
	writeWorkflowDependencyRevisionPart(digest, []byte(rootRaw))
	refs := make([]string, 0, len(revisions))
	for ref := range revisions {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		writeWorkflowDependencyRevisionPart(digest, []byte(ref))
		writeWorkflowDependencyRevisionPart(digest, []byte(revisions[ref]))
	}
	projected := *response
	projected.Revision = ""
	data, err := json.Marshal(projected)
	if err != nil {
		return "", err
	}
	writeWorkflowDependencyRevisionPart(digest, data)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func workflowDependencyContentRevision(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func writeWorkflowDependencyRevisionPart(digest hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(value)
}

func writeWorkflowDependencyError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Cache-Control", "no-store")
	writeWorkflowJSONStatus(w, status, map[string]any{"error": code})
}
