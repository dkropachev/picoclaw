package workflows

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	maxWorkflowDependencyDefinitions = 256
	maxWorkflowDependencyOccurrences = 4096
	maxWorkflowDependencyIssues      = 4096
	maxWorkflowDependencyDepthVisits = 16384

	// MaxWorkflowDependencyDefinitionBytes bounds one root or reusable
	// definition admitted to dependency analysis.
	MaxWorkflowDependencyDefinitionBytes int64 = 1 << 20
	// MaxWorkflowDependencyTotalBytes bounds reusable definition bytes loaded
	// for one dependency analysis.
	MaxWorkflowDependencyTotalBytes int64 = 16 << 20
)

// ErrWorkflowDependencyAnalysisLimitExceeded lets bounded loaders fail closed
// without exposing filesystem or parser details to readiness clients.
var ErrWorkflowDependencyAnalysisLimitExceeded = errors.New(
	"workflow dependency analysis limit exceeded",
)

// WorkflowDependencyKind identifies a workflow dependency namespace.
type WorkflowDependencyKind string

const (
	// WorkflowDependencyKindAgent identifies an agent/<name> step target.
	WorkflowDependencyKindAgent WorkflowDependencyKind = "agent"
	// WorkflowDependencyKindTool identifies a tool/<name> step target.
	WorkflowDependencyKindTool WorkflowDependencyKind = "tool"
	// WorkflowDependencyKindMCP identifies an mcp/<server>/<tool> step target.
	WorkflowDependencyKindMCP WorkflowDependencyKind = "mcp"
	// WorkflowDependencyKindFunction identifies a function/<name> step target.
	WorkflowDependencyKindFunction WorkflowDependencyKind = "function"
	// WorkflowDependencyKindReusable identifies a reusable workflow job target.
	WorkflowDependencyKindReusable WorkflowDependencyKind = "reusable"
)

// WorkflowDependencyOccurrence describes one declared dependency and its stable
// source location. Conditional jobs and steps remain declared dependencies.
type WorkflowDependencyOccurrence struct {
	Kind        WorkflowDependencyKind `json:"kind"`
	Name        string                 `json:"name"`
	WorkflowRef string                 `json:"workflow_ref"`
	Path        string                 `json:"path"`
}

// WorkflowDependencyIssueCode is a fixed, safe structural readiness reason.
type WorkflowDependencyIssueCode string

const (
	// WorkflowDependencyIssueInvalidReusableRef means a reusable target is not
	// a canonical, workspace-local workflow reference.
	WorkflowDependencyIssueInvalidReusableRef WorkflowDependencyIssueCode = "invalid_reusable_ref"
	// WorkflowDependencyIssueReusableUnavailable means the reusable target
	// could not be loaded. Loader errors are deliberately not retained.
	WorkflowDependencyIssueReusableUnavailable WorkflowDependencyIssueCode = "reusable_unavailable"
	// WorkflowDependencyIssueReusableInvalid means the loaded reusable target
	// did not pass the workflow validator.
	WorkflowDependencyIssueReusableInvalid WorkflowDependencyIssueCode = "reusable_invalid"
	// WorkflowDependencyIssueReusableCycle means the reusable closure contains
	// a reference cycle.
	WorkflowDependencyIssueReusableCycle WorkflowDependencyIssueCode = "reusable_cycle"
	// WorkflowDependencyIssueCallDepthExceeded means an edge exceeds the
	// executor's reusable call-depth limit.
	WorkflowDependencyIssueCallDepthExceeded WorkflowDependencyIssueCode = "call_depth_exceeded"
	// WorkflowDependencyIssueMissingInput means a required reusable input is
	// neither mapped by the caller nor supplied by a default.
	WorkflowDependencyIssueMissingInput WorkflowDependencyIssueCode = "missing_required_input"
	// WorkflowDependencyIssueInputTypeMismatch means a statically known mapped
	// input does not satisfy the callee's input type.
	WorkflowDependencyIssueInputTypeMismatch WorkflowDependencyIssueCode = "input_type_mismatch"
	// WorkflowDependencyIssueInvalidSecrets means a reusable job has a secrets
	// value other than "inherit" or a string-keyed mapping.
	WorkflowDependencyIssueInvalidSecrets WorkflowDependencyIssueCode = "invalid_secrets"
	// WorkflowDependencyIssueMissingSecret means a required reusable secret is
	// not inherited or mapped to a statically nonempty value.
	WorkflowDependencyIssueMissingSecret WorkflowDependencyIssueCode = "missing_required_secret"
	// WorkflowDependencyIssueAnalysisLimitExceeded means the bounded closure
	// analysis stopped before loading or reporting additional declarations.
	WorkflowDependencyIssueAnalysisLimitExceeded WorkflowDependencyIssueCode = "analysis_limit_exceeded"
)

// WorkflowDependencyIssue locates one structural closure failure without
// retaining loader, filesystem, configuration, or secret details.
type WorkflowDependencyIssue struct {
	Code           WorkflowDependencyIssueCode `json:"code"`
	WorkflowRef    string                      `json:"workflow_ref"`
	Path           string                      `json:"path"`
	DependencyKind WorkflowDependencyKind      `json:"dependency_kind,omitempty"`
	DependencyName string                      `json:"dependency_name,omitempty"`
}

// WorkflowDependencyClosure is the deterministic structural dependency report
// for a root workflow and all reachable reusable definitions.
type WorkflowDependencyClosure struct {
	RootRef      string                         `json:"root_ref"`
	Dependencies []WorkflowDependencyOccurrence `json:"dependencies"`
	Issues       []WorkflowDependencyIssue      `json:"issues"`
}

// Ready reports whether the reusable closure and its static call contracts are
// structurally ready. It does not perform dynamic runtime readiness checks.
func (c WorkflowDependencyClosure) Ready() bool {
	return len(c.Issues) == 0
}

// ReusableWorkflowLoader loads a reusable definition by canonical local ref.
// Implementations must treat the returned definition as read-only.
type ReusableWorkflowLoader interface {
	LoadReusableWorkflow(ctx context.Context, ref string) (*Workflow, error)
}

// ReusableWorkflowLoaderFunc adapts a function to ReusableWorkflowLoader.
type ReusableWorkflowLoaderFunc func(ctx context.Context, ref string) (*Workflow, error)

// LoadReusableWorkflow calls f with the requested canonical local ref.
func (f ReusableWorkflowLoaderFunc) LoadReusableWorkflow(
	ctx context.Context,
	ref string,
) (*Workflow, error) {
	return f(ctx, ref)
}

// WorkflowDependencyCheckRequest configures reusable closure analysis. The
// root workflow is an overlay for RootRef, so a draft is never reloaded from
// the published definition while its closure is checked.
type WorkflowDependencyCheckRequest struct {
	RootRef      string
	RootWorkflow *Workflow
	Loader       ReusableWorkflowLoader
	MaxCallDepth int
}

// WorkflowDependencyReadinessCode is a fixed, safe dynamic readiness result.
type WorkflowDependencyReadinessCode string

const (
	// WorkflowDependencyReadinessReady means the dependency can be used now.
	WorkflowDependencyReadinessReady WorkflowDependencyReadinessCode = "ready"
	// WorkflowDependencyReadinessUnchecked means no runtime resolver was
	// supplied.
	WorkflowDependencyReadinessUnchecked WorkflowDependencyReadinessCode = "unchecked"
	// WorkflowDependencyReadinessNotConfigured means required runtime
	// configuration is absent.
	WorkflowDependencyReadinessNotConfigured WorkflowDependencyReadinessCode = "not_configured"
	// WorkflowDependencyReadinessDisabled means the dependency or its owning
	// subsystem is disabled.
	WorkflowDependencyReadinessDisabled WorkflowDependencyReadinessCode = "disabled"
	// WorkflowDependencyReadinessNotAllowed means runtime policy rejects the
	// dependency.
	WorkflowDependencyReadinessNotAllowed WorkflowDependencyReadinessCode = "not_allowed"
	// WorkflowDependencyReadinessNotConnected means a required runtime server
	// is not connected.
	WorkflowDependencyReadinessNotConnected WorkflowDependencyReadinessCode = "not_connected"
	// WorkflowDependencyReadinessNotFound means the named runtime capability
	// does not exist.
	WorkflowDependencyReadinessNotFound WorkflowDependencyReadinessCode = "not_found"
	// WorkflowDependencyReadinessInvalidConfiguration means configuration
	// exists but cannot safely activate the dependency.
	WorkflowDependencyReadinessInvalidConfiguration WorkflowDependencyReadinessCode = "invalid_configuration"
	// WorkflowDependencyReadinessNameCollision means more than one runtime
	// capability maps to the dependency's canonical name.
	WorkflowDependencyReadinessNameCollision WorkflowDependencyReadinessCode = "name_collision"
	// WorkflowDependencyReadinessUnavailable is the safe fallback for an
	// unavailable dependency or an unrecognized resolver result.
	WorkflowDependencyReadinessUnavailable WorkflowDependencyReadinessCode = "unavailable"
)

// WorkflowDependencyReadiness is the safe dynamic state for one occurrence.
type WorkflowDependencyReadiness struct {
	Dependency WorkflowDependencyOccurrence    `json:"dependency"`
	Code       WorkflowDependencyReadinessCode `json:"code"`
	Ready      bool                            `json:"ready"`
}

// WorkflowDependencyRuntimeResolver resolves current runtime availability. It
// returns only a readiness code so implementation errors cannot leak through
// the dependency report.
type WorkflowDependencyRuntimeResolver interface {
	ResolveWorkflowDependency(
		ctx context.Context,
		dependency WorkflowDependencyOccurrence,
	) WorkflowDependencyReadinessCode
}

// WorkflowDependencyRuntimeResolverFunc adapts a function to the runtime
// readiness resolver interface.
type WorkflowDependencyRuntimeResolverFunc func(
	ctx context.Context,
	dependency WorkflowDependencyOccurrence,
) WorkflowDependencyReadinessCode

// ResolveWorkflowDependency calls f for one declared dependency.
func (f WorkflowDependencyRuntimeResolverFunc) ResolveWorkflowDependency(
	ctx context.Context,
	dependency WorkflowDependencyOccurrence,
) WorkflowDependencyReadinessCode {
	return f(ctx, dependency)
}

type workflowDependencySite struct {
	occurrence WorkflowDependencyOccurrence
	job        *Job
}

type reusableWorkflowLoad struct {
	workflow *Workflow
	invalid  bool
	missing  bool
	limited  bool
}

type workflowDependencyClosureChecker struct {
	ctx          context.Context
	request      WorkflowDependencyCheckRequest
	maxCallDepth int
	loads        map[string]reusableWorkflowLoad
	visited      map[string]bool
	stack        map[string]bool
	issueKeys    map[string]struct{}
	limitIssue   bool
	depthVisits  int
	depthLimited bool
	report       WorkflowDependencyClosure
}

// ExtractWorkflowDependencies returns every dependency declaration in stable
// source order, including declarations guarded by false or dynamic conditions.
func ExtractWorkflowDependencies(
	workflowRef string,
	workflow *Workflow,
) []WorkflowDependencyOccurrence {
	workflowRef = strings.TrimSpace(workflowRef)
	if canonicalRef, err := CanonicalLocalRef(workflowRef); err == nil {
		workflowRef = canonicalRef
	}
	sites := extractWorkflowDependencySites(workflowRef, workflow)
	dependencies := make([]WorkflowDependencyOccurrence, 0, len(sites))
	for _, site := range sites {
		dependencies = append(dependencies, site.occurrence)
	}
	sortWorkflowDependencyOccurrences(dependencies)

	return dependencies
}

// CheckWorkflowDependencyClosure extracts the complete reusable closure,
// detects cycles and excessive depth, and verifies each reusable call against
// the callee's input and secret contract. It does not query runtime state or
// alter normal workflow validation and compatibility.
func CheckWorkflowDependencyClosure(
	ctx context.Context,
	request WorkflowDependencyCheckRequest,
) (WorkflowDependencyClosure, error) {
	if err := ctx.Err(); err != nil {
		return WorkflowDependencyClosure{}, err
	}
	if request.RootWorkflow == nil {
		return WorkflowDependencyClosure{}, fmt.Errorf("root workflow is required")
	}
	rootRef, err := CanonicalLocalRef(request.RootRef)
	if err != nil {
		return WorkflowDependencyClosure{}, fmt.Errorf("root workflow ref: %w", err)
	}
	maxCallDepth := request.MaxCallDepth
	if maxCallDepth <= 0 {
		maxCallDepth = defaultMaxCallDepth
	}
	checker := workflowDependencyClosureChecker{
		ctx:          ctx,
		request:      request,
		maxCallDepth: maxCallDepth,
		loads: map[string]reusableWorkflowLoad{
			rootRef: {workflow: request.RootWorkflow},
		},
		visited:   make(map[string]bool),
		stack:     make(map[string]bool),
		issueKeys: make(map[string]struct{}),
		report: WorkflowDependencyClosure{
			RootRef:      rootRef,
			Dependencies: make([]WorkflowDependencyOccurrence, 0),
			Issues:       make([]WorkflowDependencyIssue, 0),
		},
	}
	if err := checker.visit(rootRef, request.RootWorkflow, 0); err != nil {
		return WorkflowDependencyClosure{}, err
	}
	checker.checkCallDepth(rootRef, 0, make(map[string]bool))
	sortWorkflowDependencyOccurrences(checker.report.Dependencies)
	sortWorkflowDependencyIssues(checker.report.Issues)

	return checker.report, nil
}

// ResolveWorkflowDependencyReadiness applies dynamic runtime readiness without
// changing workflow validation or compatibility. Unknown resolver codes are
// reduced to the fixed "unavailable" result.
func ResolveWorkflowDependencyReadiness(
	ctx context.Context,
	dependencies []WorkflowDependencyOccurrence,
	resolver WorkflowDependencyRuntimeResolver,
) ([]WorkflowDependencyReadiness, error) {
	ordered := append([]WorkflowDependencyOccurrence(nil), dependencies...)
	sortWorkflowDependencyOccurrences(ordered)
	readiness := make([]WorkflowDependencyReadiness, 0, len(ordered))
	for _, dependency := range ordered {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		code := WorkflowDependencyReadinessUnchecked
		if resolver != nil {
			code = resolver.ResolveWorkflowDependency(ctx, dependency)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			code = safeWorkflowDependencyReadinessCode(code)
		}
		readiness = append(readiness, WorkflowDependencyReadiness{
			Dependency: dependency,
			Code:       code,
			Ready:      code == WorkflowDependencyReadinessReady,
		})
	}

	return readiness, nil
}

func (c *workflowDependencyClosureChecker) visit(
	workflowRef string,
	workflow *Workflow,
	depth int,
) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if c.visited[workflowRef] {
		return nil
	}
	c.visited[workflowRef] = true
	c.stack[workflowRef] = true
	defer delete(c.stack, workflowRef)

	sites := extractWorkflowDependencySites(workflowRef, workflow)
	for _, site := range sites {
		if len(c.report.Dependencies) >= maxWorkflowDependencyOccurrences {
			c.addIssue(
				site.occurrence,
				WorkflowDependencyIssueAnalysisLimitExceeded,
				site.occurrence.Path,
			)
			return nil
		}
		c.report.Dependencies = append(c.report.Dependencies, site.occurrence)
		if site.occurrence.Kind != WorkflowDependencyKindReusable {
			continue
		}
		if err := c.checkReusableSite(site, depth); err != nil {
			return err
		}
	}

	return nil
}

func (c *workflowDependencyClosureChecker) checkReusableSite(
	site workflowDependencySite,
	depth int,
) error {
	canonicalRef, validRef := canonicalWorkflowDependencyRef(site.occurrence.Name)
	if !validRef {
		c.addIssue(site.occurrence, WorkflowDependencyIssueInvalidReusableRef, site.occurrence.Path)

		return nil
	}
	if c.stack[canonicalRef] {
		c.addIssue(site.occurrence, WorkflowDependencyIssueReusableCycle, site.occurrence.Path)

		return nil
	}
	nextDepth := depth + 1
	if nextDepth > c.maxCallDepth {
		c.addIssue(
			site.occurrence,
			WorkflowDependencyIssueCallDepthExceeded,
			site.occurrence.Path,
		)
		return nil
	}
	if !c.visited[canonicalRef] &&
		len(c.visited) >= maxWorkflowDependencyDefinitions {
		c.addIssue(
			site.occurrence,
			WorkflowDependencyIssueAnalysisLimitExceeded,
			site.occurrence.Path,
		)
		return nil
	}
	loaded, loadErr := c.load(canonicalRef)
	if loadErr != nil {
		if errors.Is(loadErr, ErrWorkflowDependencyAnalysisLimitExceeded) {
			c.addIssue(
				site.occurrence,
				WorkflowDependencyIssueAnalysisLimitExceeded,
				site.occurrence.Path,
			)
			return nil
		}
		return loadErr
	}
	if loaded.missing {
		c.addIssue(site.occurrence, WorkflowDependencyIssueReusableUnavailable, site.occurrence.Path)

		return nil
	}
	if loaded.invalid {
		c.addIssue(site.occurrence, WorkflowDependencyIssueReusableInvalid, site.occurrence.Path)
	}
	c.addIssues(reusableCallContractIssues(site, loaded.workflow.On.WorkflowCall))
	if c.visited[canonicalRef] {
		return nil
	}

	return c.visit(canonicalRef, loaded.workflow, nextDepth)
}

func (c *workflowDependencyClosureChecker) checkCallDepth(
	workflowRef string,
	depth int,
	stack map[string]bool,
) {
	if c.depthLimited {
		return
	}
	loaded, ok := c.loads[workflowRef]
	if !ok || loaded.missing || loaded.workflow == nil {
		return
	}
	stack[workflowRef] = true
	defer delete(stack, workflowRef)
	for _, site := range extractWorkflowDependencySites(workflowRef, loaded.workflow) {
		if c.depthLimited {
			return
		}
		if site.occurrence.Kind != WorkflowDependencyKindReusable {
			continue
		}
		if c.depthVisits >= maxWorkflowDependencyDepthVisits {
			c.addIssue(
				site.occurrence,
				WorkflowDependencyIssueAnalysisLimitExceeded,
				site.occurrence.Path,
			)
			c.depthLimited = true
			return
		}
		c.depthVisits++
		canonicalRef, validRef := canonicalWorkflowDependencyRef(site.occurrence.Name)
		if !validRef {
			continue
		}
		nextDepth := depth + 1
		if nextDepth > c.maxCallDepth {
			c.addIssue(
				site.occurrence,
				WorkflowDependencyIssueCallDepthExceeded,
				site.occurrence.Path,
			)
			continue
		}
		if stack[canonicalRef] {
			continue
		}
		c.checkCallDepth(canonicalRef, nextDepth, stack)
	}
}

func (c *workflowDependencyClosureChecker) load(
	ref string,
) (reusableWorkflowLoad, error) {
	if loaded, ok := c.loads[ref]; ok {
		if loaded.limited {
			return reusableWorkflowLoad{}, ErrWorkflowDependencyAnalysisLimitExceeded
		}
		return loaded, nil
	}
	if c.request.Loader == nil {
		loaded := reusableWorkflowLoad{missing: true}
		c.loads[ref] = loaded

		return loaded, nil
	}
	workflow, available, loadErr := loadReusableWorkflowDependency(
		c.ctx,
		c.request.Loader,
		ref,
	)
	if ctxErr := c.ctx.Err(); ctxErr != nil {
		return reusableWorkflowLoad{}, ctxErr
	}
	if errors.Is(loadErr, ErrWorkflowDependencyAnalysisLimitExceeded) {
		c.loads[ref] = reusableWorkflowLoad{limited: true}
		return reusableWorkflowLoad{}, loadErr
	}
	if !available {
		loaded := reusableWorkflowLoad{missing: true}
		c.loads[ref] = loaded

		return loaded, nil
	}
	loaded := reusableWorkflowLoad{workflow: workflow, invalid: Validate(workflow) != nil}
	c.loads[ref] = loaded

	return loaded, nil
}

func loadReusableWorkflowDependency(
	ctx context.Context,
	loader ReusableWorkflowLoader,
	ref string,
) (*Workflow, bool, error) {
	workflow, err := loader.LoadReusableWorkflow(ctx, ref)
	if errors.Is(err, ErrWorkflowDependencyAnalysisLimitExceeded) {
		return nil, false, err
	}

	return workflow, err == nil && workflow != nil, nil
}

func (c *workflowDependencyClosureChecker) addIssue(
	dependency WorkflowDependencyOccurrence,
	code WorkflowDependencyIssueCode,
	path string,
) {
	c.addIssues([]WorkflowDependencyIssue{{
		Code:           code,
		WorkflowRef:    dependency.WorkflowRef,
		Path:           path,
		DependencyKind: dependency.Kind,
		DependencyName: dependency.Name,
	}})
}

func (c *workflowDependencyClosureChecker) addIssues(issues []WorkflowDependencyIssue) {
	for _, issue := range issues {
		if len(c.report.Issues) >= maxWorkflowDependencyIssues-1 {
			if !c.limitIssue {
				issue.Code = WorkflowDependencyIssueAnalysisLimitExceeded
				c.report.Issues = append(c.report.Issues, issue)
				c.limitIssue = true
			}
			return
		}
		key := strings.Join([]string{
			string(issue.Code),
			issue.WorkflowRef,
			issue.Path,
			string(issue.DependencyKind),
			issue.DependencyName,
		}, "\x00")
		if _, exists := c.issueKeys[key]; exists {
			continue
		}
		c.issueKeys[key] = struct{}{}
		c.report.Issues = append(c.report.Issues, issue)
	}
}

func extractWorkflowDependencySites(
	workflowRef string,
	workflow *Workflow,
) []workflowDependencySite {
	if workflow == nil {
		return nil
	}
	jobIDs := make([]string, 0, len(workflow.Jobs))
	for jobID := range workflow.Jobs {
		jobIDs = append(jobIDs, jobID)
	}
	sort.Strings(jobIDs)
	sites := make([]workflowDependencySite, 0)
	for _, jobID := range jobIDs {
		job := workflow.Jobs[jobID]
		jobPath := "/jobs/" + workflowDependencyJSONPointerToken(jobID)
		if uses := strings.TrimSpace(job.Uses); uses != "" {
			name := uses
			if canonical, err := CanonicalLocalRef(uses); err == nil {
				name = canonical
			}
			jobCopy := job
			sites = append(sites, workflowDependencySite{
				occurrence: WorkflowDependencyOccurrence{
					Kind:        WorkflowDependencyKindReusable,
					Name:        name,
					WorkflowRef: workflowRef,
					Path:        jobPath + "/uses",
				},
				job: &jobCopy,
			})
		}
		for stepIndex, step := range job.Steps {
			kind, name, ok := workflowStepDependency(strings.TrimSpace(step.Uses))
			if !ok {
				continue
			}
			sites = append(sites, workflowDependencySite{
				occurrence: WorkflowDependencyOccurrence{
					Kind:        kind,
					Name:        name,
					WorkflowRef: workflowRef,
					Path:        jobPath + "/steps/" + strconv.Itoa(stepIndex) + "/uses",
				},
			})
		}
	}

	return sites
}

func workflowStepDependency(
	uses string,
) (WorkflowDependencyKind, string, bool) {
	for _, candidate := range []struct {
		prefix string
		kind   WorkflowDependencyKind
	}{
		{prefix: "agent/", kind: WorkflowDependencyKindAgent},
		{prefix: "tool/", kind: WorkflowDependencyKindTool},
		{prefix: "mcp/", kind: WorkflowDependencyKindMCP},
		{prefix: "function/", kind: WorkflowDependencyKindFunction},
	} {
		if strings.HasPrefix(uses, candidate.prefix) {
			return candidate.kind, strings.TrimSpace(strings.TrimPrefix(uses, candidate.prefix)), true
		}
	}

	return "", "", false
}

func reusableCallContractIssues(
	site workflowDependencySite,
	call *WorkflowCall,
) []WorkflowDependencyIssue {
	if call == nil || site.job == nil {
		return nil
	}
	issues := make([]WorkflowDependencyIssue, 0)
	jobPath := strings.TrimSuffix(site.occurrence.Path, "/uses")
	for name, input := range call.Inputs {
		value, supplied := site.job.With[name]
		if !supplied {
			if input.Default == nil && input.Required {
				issues = append(issues, dependencyContractIssue(
					site,
					WorkflowDependencyIssueMissingInput,
					jobPath+"/with/"+workflowDependencyJSONPointerToken(name),
				))
			}
			continue
		}
		if workflowDependencyInputTypeIsDynamic(value) {
			continue
		}
		if err := validateWorkflowInputValue(name, input.Type, value); err != nil {
			issues = append(issues, dependencyContractIssue(
				site,
				WorkflowDependencyIssueInputTypeMismatch,
				jobPath+"/with/"+workflowDependencyJSONPointerToken(name),
			))
		}
	}
	issues = append(issues, reusableSecretContractIssues(site, call.Secrets)...)

	return issues
}

func reusableSecretContractIssues(
	site workflowDependencySite,
	secrets map[string]Secret,
) []WorkflowDependencyIssue {
	if len(secrets) == 0 {
		return nil
	}
	basePath := strings.TrimSuffix(site.occurrence.Path, "/uses") + "/secrets"
	if text, ok := site.job.Secrets.(string); ok {
		if strings.EqualFold(strings.TrimSpace(text), "inherit") {
			return nil
		}

		return []WorkflowDependencyIssue{
			dependencyContractIssue(site, WorkflowDependencyIssueInvalidSecrets, basePath),
		}
	}
	mapped, ok := site.job.Secrets.(map[string]any)
	if site.job.Secrets != nil && !ok {
		return []WorkflowDependencyIssue{
			dependencyContractIssue(site, WorkflowDependencyIssueInvalidSecrets, basePath),
		}
	}
	issues := make([]WorkflowDependencyIssue, 0)
	for name, secret := range secrets {
		if !secret.Required {
			continue
		}
		value, supplied := mapped[name]
		if !supplied || workflowDependencySecretIsStaticallyMissing(value) {
			issues = append(issues, dependencyContractIssue(
				site,
				WorkflowDependencyIssueMissingSecret,
				basePath+"/"+workflowDependencyJSONPointerToken(name),
			))
		}
	}

	return issues
}

func dependencyContractIssue(
	site workflowDependencySite,
	code WorkflowDependencyIssueCode,
	path string,
) WorkflowDependencyIssue {
	return WorkflowDependencyIssue{
		Code:           code,
		WorkflowRef:    site.occurrence.WorkflowRef,
		Path:           path,
		DependencyKind: site.occurrence.Kind,
		DependencyName: site.occurrence.Name,
	}
}

func workflowDependencyInputTypeIsDynamic(value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	matches := expressionPattern.FindAllStringSubmatch(text, -1)

	return len(matches) == 1 &&
		strings.TrimSpace(matches[0][0]) == strings.TrimSpace(text)
}

func workflowDependencySecretIsStaticallyMissing(value any) bool {
	if text, ok := value.(string); ok && expressionPattern.MatchString(text) {
		return false
	}

	return secretValueMissing(value) ||
		(strings.TrimSpace(fmt.Sprint(value)) == "")
}

func workflowDependencyJSONPointerToken(value string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(value)
}

func canonicalWorkflowDependencyRef(ref string) (string, bool) {
	canonicalRef, err := CanonicalLocalRef(ref)

	return canonicalRef, err == nil
}

func sortWorkflowDependencyOccurrences(dependencies []WorkflowDependencyOccurrence) {
	sort.SliceStable(dependencies, func(i, j int) bool {
		left := dependencies[i]
		right := dependencies[j]
		if left.WorkflowRef != right.WorkflowRef {
			return left.WorkflowRef < right.WorkflowRef
		}
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		if left.Kind != right.Kind {
			return left.Kind < right.Kind
		}

		return left.Name < right.Name
	})
}

func sortWorkflowDependencyIssues(issues []WorkflowDependencyIssue) {
	sort.SliceStable(issues, func(i, j int) bool {
		left := issues[i]
		right := issues[j]
		if left.WorkflowRef != right.WorkflowRef {
			return left.WorkflowRef < right.WorkflowRef
		}
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		if left.DependencyKind != right.DependencyKind {
			return left.DependencyKind < right.DependencyKind
		}

		return left.DependencyName < right.DependencyName
	})
}

func safeWorkflowDependencyReadinessCode(
	code WorkflowDependencyReadinessCode,
) WorkflowDependencyReadinessCode {
	switch code {
	case WorkflowDependencyReadinessReady,
		WorkflowDependencyReadinessUnchecked,
		WorkflowDependencyReadinessNotConfigured,
		WorkflowDependencyReadinessDisabled,
		WorkflowDependencyReadinessNotAllowed,
		WorkflowDependencyReadinessNotConnected,
		WorkflowDependencyReadinessNotFound,
		WorkflowDependencyReadinessInvalidConfiguration,
		WorkflowDependencyReadinessNameCollision,
		WorkflowDependencyReadinessUnavailable:
		return code
	default:
		return WorkflowDependencyReadinessUnavailable
	}
}
