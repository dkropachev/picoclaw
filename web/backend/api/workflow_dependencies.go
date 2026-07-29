package api

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	l.revisions[canonical] = workflowDependencyContentRevision(data)
	return workflows.Parse(data)
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

func (h *Handler) evaluateWorkflowDependencies(
	ctx context.Context,
	cfg *config.Config,
	configRevision string,
	rootRef string,
	raw string,
) (*workflowDependencyCheckResponse, error) {
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
	if err := workflows.Validate(workflow); err != nil {
		return nil, errWorkflowDependencyInvalid
	}

	loader := &workflowDependencySnapshotLoader{
		resolver: workflows.Resolver{
			WorkspaceDir:   cfg.WorkspacePath(),
			DefinitionsDir: cfg.Workflows.EffectiveDefinitionsDir(),
		},
		revisions: map[string]string{
			canonical: workflowDependencyContentRevision([]byte(raw)),
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
	if err != nil || latestConfigRevision != configRevision {
		return nil, errWorkflowDependencyUnavailable
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
	return response, nil
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
	if err := ctx.Err(); err != nil {
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
