package tools

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

// WorkflowDevelopmentPublishGateConfig supplies the effective workflow and
// runtime state required to fence an agent-initiated development publish.
// Resolver is mandatory: publishing without live dependency readiness would
// be a fail-open path.
type WorkflowDevelopmentPublishGateConfig struct {
	WorkflowsEnabled bool
	DefinitionsDir   string
	MaxCallDepth     int
	Resolver         workflows.WorkflowDependencyRuntimeResolver
}

// ConfigureDevelopmentPublishGate enables fenced dev_publish for this tool.
// NewWorkflowTool intentionally remains source-compatible; callers that do not
// inject a complete gate configuration cannot publish.
func (t *WorkflowTool) ConfigureDevelopmentPublishGate(
	config WorkflowDevelopmentPublishGateConfig,
) *WorkflowTool {
	if t == nil {
		return nil
	}
	if strings.TrimSpace(config.DefinitionsDir) == "" {
		config.DefinitionsDir = t.definitionsDir
	}
	t.publishGate = &config
	return t
}

type workflowDevelopmentDependencyReport struct {
	RootRef          string                                  `json:"root_ref"`
	WorkflowsEnabled bool                                    `json:"workflows_enabled"`
	DefinitionsDir   string                                  `json:"definitions_dir"`
	MaxCallDepth     int                                     `json:"max_call_depth"`
	StructuralReady  bool                                    `json:"structural_ready"`
	RuntimeReady     bool                                    `json:"runtime_ready"`
	Ready            bool                                    `json:"ready"`
	Dependencies     []workflows.WorkflowDependencyReadiness `json:"dependencies"`
	StructuralIssues []workflows.WorkflowDependencyIssue     `json:"structural_issues"`
}

type workflowDevelopmentDependencySnapshot struct {
	ref       string
	available bool
	content   []byte
}

type workflowDevelopmentDependencySnapshotLoader struct {
	resolver  workflows.Resolver
	snapshots map[string]workflowDevelopmentDependencySnapshot
	bytesRead int64
}

func (l *workflowDevelopmentDependencySnapshotLoader) LoadReusableWorkflow(
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
		l.snapshots[canonical] = workflowDevelopmentDependencySnapshot{ref: canonical}
		return nil, err
	}
	data, err := readWorkflowDevelopmentDependencyDefinition(
		resolved.Path,
		workflows.MaxWorkflowDependencyTotalBytes-l.bytesRead,
	)
	if err != nil {
		l.snapshots[canonical] = workflowDevelopmentDependencySnapshot{ref: canonical}
		return nil, err
	}
	l.bytesRead += int64(len(data))
	l.snapshots[canonical] = workflowDevelopmentDependencySnapshot{
		ref:       canonical,
		available: true,
		content:   append([]byte(nil), data...),
	}
	return workflows.Parse(data)
}

func (t *WorkflowTool) workflowDevelopmentPublishGate() (
	workflows.WorkflowDevelopmentPublishGate,
	error,
) {
	if t == nil || t.publishGate == nil || t.publishGate.Resolver == nil {
		return nil, workflows.ErrWorkflowDevelopmentPublishGateRequired
	}
	config := *t.publishGate
	return func(
		ctx context.Context,
		input workflows.WorkflowDevelopmentPublishGateInput,
	) (workflows.WorkflowDevelopmentPublishGateResult, error) {
		return evaluateWorkflowDevelopmentPublishGate(ctx, t.workspace, config, input)
	}, nil
}

func evaluateWorkflowDevelopmentPublishGate(
	ctx context.Context,
	workspace string,
	config WorkflowDevelopmentPublishGateConfig,
	input workflows.WorkflowDevelopmentPublishGateInput,
) (workflows.WorkflowDevelopmentPublishGateResult, error) {
	if err := ctx.Err(); err != nil {
		return workflows.WorkflowDevelopmentPublishGateResult{}, err
	}
	if config.Resolver == nil {
		return workflows.WorkflowDevelopmentPublishGateResult{},
			workflows.ErrWorkflowDevelopmentPublishGateRequired
	}
	if input.Workflow == nil {
		return workflows.WorkflowDevelopmentPublishGateResult{},
			fmt.Errorf("workflow development dependency gate requires parsed draft")
	}
	if int64(len(input.YAML)) > workflows.MaxWorkflowDependencyDefinitionBytes {
		return workflows.WorkflowDevelopmentPublishGateResult{},
			workflows.ErrWorkflowDependencyAnalysisLimitExceeded
	}
	rootRef, err := workflows.CanonicalLocalRef(input.WorkflowRef)
	if err != nil {
		return workflows.WorkflowDevelopmentPublishGateResult{}, err
	}
	definitionsDir := strings.TrimSpace(config.DefinitionsDir)
	if definitionsDir == "" {
		definitionsDir = workflows.DefaultDefinitionsDir
	}
	loader := &workflowDevelopmentDependencySnapshotLoader{
		resolver: workflows.Resolver{
			WorkspaceDir:   workspace,
			DefinitionsDir: definitionsDir,
		},
		snapshots: make(map[string]workflowDevelopmentDependencySnapshot),
	}
	closure, err := workflows.CheckWorkflowDependencyClosure(
		ctx,
		workflows.WorkflowDependencyCheckRequest{
			RootRef:      rootRef,
			RootWorkflow: input.Workflow,
			Loader:       loader,
			MaxCallDepth: config.MaxCallDepth,
		},
	)
	if err != nil {
		return workflows.WorkflowDevelopmentPublishGateResult{}, err
	}
	readiness, err := workflows.ResolveWorkflowDependencyReadiness(
		ctx,
		closure.Dependencies,
		config.Resolver,
	)
	if err != nil {
		return workflows.WorkflowDevelopmentPublishGateResult{}, err
	}
	runtimeReady := true
	for _, item := range readiness {
		if !item.Ready {
			runtimeReady = false
			break
		}
	}
	structuralReady := closure.Ready()
	report := workflowDevelopmentDependencyReport{
		RootRef:          rootRef,
		WorkflowsEnabled: config.WorkflowsEnabled,
		DefinitionsDir:   definitionsDir,
		MaxCallDepth:     config.MaxCallDepth,
		StructuralReady:  structuralReady,
		RuntimeReady:     runtimeReady,
		Ready:            config.WorkflowsEnabled && structuralReady && runtimeReady,
		Dependencies:     readiness,
		StructuralIssues: closure.Issues,
	}
	revision, err := workflowDevelopmentDependencyRevision(input, loader.snapshots, report)
	if err != nil {
		return workflows.WorkflowDevelopmentPublishGateResult{}, err
	}
	return workflows.WorkflowDevelopmentPublishGateResult{
		Revision: revision,
		Ready:    report.Ready,
	}, nil
}

func readWorkflowDevelopmentDependencyDefinition(
	path string,
	remaining int64,
) ([]byte, error) {
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

func workflowDevelopmentDependencyRevision(
	input workflows.WorkflowDevelopmentPublishGateInput,
	snapshots map[string]workflowDevelopmentDependencySnapshot,
	report workflowDevelopmentDependencyReport,
) (string, error) {
	digest := sha256.New()
	writeWorkflowDevelopmentDependencyRevisionPart(digest, []byte(input.WorkflowRef))
	writeWorkflowDevelopmentDependencyRevisionPart(digest, []byte(input.DraftRevision))
	writeWorkflowDevelopmentDependencyRevisionPart(digest, []byte(input.YAML))

	refs := make([]string, 0, len(snapshots))
	for ref := range snapshots {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		snapshot := snapshots[ref]
		writeWorkflowDevelopmentDependencyRevisionPart(digest, []byte(snapshot.ref))
		if snapshot.available {
			writeWorkflowDevelopmentDependencyRevisionPart(digest, []byte("available"))
			writeWorkflowDevelopmentDependencyRevisionPart(digest, snapshot.content)
		} else {
			writeWorkflowDevelopmentDependencyRevisionPart(digest, []byte("unavailable"))
		}
	}
	reportData, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	writeWorkflowDevelopmentDependencyRevisionPart(digest, reportData)
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

func writeWorkflowDevelopmentDependencyRevisionPart(digest hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = digest.Write(length[:])
	_, _ = digest.Write(value)
}
