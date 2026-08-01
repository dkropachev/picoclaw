package workflows

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestExtractWorkflowDependenciesIncludesEveryDeclaredConditionalPath(t *testing.T) {
	workflow := &Workflow{Jobs: map[string]Job{
		"reusable": {
			If:   "${{ false }}",
			Uses: " ./workflows/child.yml ",
		},
		"steps": {
			RunsOn: "picoclaw",
			Steps: []Step{
				{If: "${{ false }}", Uses: "agent/main"},
				{Uses: "tool/github.comment"},
				{Uses: "mcp/github/issues.list"},
				{Uses: "function/workflow.state"},
				{Uses: "human/task"},
				{Uses: "unsupported/ignored"},
			},
		},
	}}

	got := ExtractWorkflowDependencies(" ./workflows/root.yml ", workflow)
	want := []WorkflowDependencyOccurrence{
		{
			Kind:        WorkflowDependencyKindReusable,
			Name:        "workflows/child.yml",
			WorkflowRef: "workflows/root.yml",
			Path:        "/jobs/reusable/uses",
		},
		{
			Kind:        WorkflowDependencyKindAgent,
			Name:        "main",
			WorkflowRef: "workflows/root.yml",
			Path:        "/jobs/steps/steps/0/uses",
		},
		{
			Kind:        WorkflowDependencyKindTool,
			Name:        "github.comment",
			WorkflowRef: "workflows/root.yml",
			Path:        "/jobs/steps/steps/1/uses",
		},
		{
			Kind:        WorkflowDependencyKindMCP,
			Name:        "github/issues.list",
			WorkflowRef: "workflows/root.yml",
			Path:        "/jobs/steps/steps/2/uses",
		},
		{
			Kind:        WorkflowDependencyKindFunction,
			Name:        "workflow.state",
			WorkflowRef: "workflows/root.yml",
			Path:        "/jobs/steps/steps/3/uses",
		},
		{
			Kind:        WorkflowDependencyKindHuman,
			Name:        "task",
			WorkflowRef: "workflows/root.yml",
			Path:        "/jobs/steps/steps/4/uses",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExtractWorkflowDependencies() = %#v, want %#v", got, want)
	}
}

func TestExtractWorkflowDependenciesUsesEscapedJSONPointerPaths(t *testing.T) {
	got := ExtractWorkflowDependencies("workflows/root.yml", &Workflow{Jobs: map[string]Job{
		"review/~all": {
			RunsOn: "picoclaw",
			Steps:  []Step{{Uses: "agent/main"}},
		},
	}})
	if len(got) != 1 || got[0].Path != "/jobs/review~1~0all/steps/0/uses" {
		t.Fatalf("dependencies = %#v", got)
	}
}

func TestCheckWorkflowDependencyClosureUsesDraftOverlayAndDetectsCycle(t *testing.T) {
	rootRef := "workflows/root.yml"
	childRef := "workflows/shared.yml"
	root := &Workflow{Jobs: map[string]Job{
		"z_call": {Uses: childRef},
		"a_call": {Uses: "./" + childRef},
	}}
	child := &Workflow{Jobs: map[string]Job{
		"work": {
			RunsOn: "picoclaw",
			Steps:  []Step{{Uses: "function/workflow.state"}},
		},
		"loop": {Uses: rootRef},
	}}
	loader := &dependencyTestLoader{
		workflows: map[string]*Workflow{childRef: child},
		calls:     make(map[string]int),
	}

	report, err := CheckWorkflowDependencyClosure(context.Background(), WorkflowDependencyCheckRequest{
		RootRef:      rootRef,
		RootWorkflow: root,
		Loader:       loader,
	})
	if err != nil {
		t.Fatalf("CheckWorkflowDependencyClosure() error = %v", err)
	}
	if loader.calls[rootRef] != 0 {
		t.Fatalf("root draft was reloaded %d times", loader.calls[rootRef])
	}
	if loader.calls[childRef] != 1 {
		t.Fatalf("shared child loads = %d, want 1", loader.calls[childRef])
	}
	wantDependencies := []WorkflowDependencyOccurrence{
		{
			Kind:        WorkflowDependencyKindReusable,
			Name:        childRef,
			WorkflowRef: rootRef,
			Path:        "/jobs/a_call/uses",
		},
		{
			Kind:        WorkflowDependencyKindReusable,
			Name:        childRef,
			WorkflowRef: rootRef,
			Path:        "/jobs/z_call/uses",
		},
		{
			Kind:        WorkflowDependencyKindReusable,
			Name:        rootRef,
			WorkflowRef: childRef,
			Path:        "/jobs/loop/uses",
		},
		{
			Kind:        WorkflowDependencyKindFunction,
			Name:        "workflow.state",
			WorkflowRef: childRef,
			Path:        "/jobs/work/steps/0/uses",
		},
	}
	if !reflect.DeepEqual(report.Dependencies, wantDependencies) {
		t.Fatalf("dependencies = %#v, want %#v", report.Dependencies, wantDependencies)
	}
	wantIssues := []WorkflowDependencyIssue{{
		Code:           WorkflowDependencyIssueReusableCycle,
		WorkflowRef:    childRef,
		Path:           "/jobs/loop/uses",
		DependencyKind: WorkflowDependencyKindReusable,
		DependencyName: rootRef,
	}}
	if !reflect.DeepEqual(report.Issues, wantIssues) {
		t.Fatalf("issues = %#v, want %#v", report.Issues, wantIssues)
	}
	if report.Ready() {
		t.Fatal("cyclic closure reported ready")
	}
}

func TestCheckWorkflowDependencyClosureEnforcesExecutorCallDepth(t *testing.T) {
	rootRef := "workflows/root.yml"
	oneRef := "workflows/one.yml"
	twoRef := "workflows/two.yml"
	loader := &dependencyTestLoader{
		workflows: map[string]*Workflow{
			oneRef: {Jobs: map[string]Job{"two": {Uses: twoRef}}},
			twoRef: dependencyTestLeafWorkflow(),
		},
		calls: make(map[string]int),
	}
	report, err := CheckWorkflowDependencyClosure(context.Background(), WorkflowDependencyCheckRequest{
		RootRef:      rootRef,
		RootWorkflow: &Workflow{Jobs: map[string]Job{"one": {Uses: oneRef}}},
		Loader:       loader,
		MaxCallDepth: 1,
	})
	if err != nil {
		t.Fatalf("CheckWorkflowDependencyClosure() error = %v", err)
	}
	if loader.calls[oneRef] != 1 || loader.calls[twoRef] != 0 {
		t.Fatalf("loader calls = %#v, want one=%d, two=%d", loader.calls, 1, 0)
	}
	want := WorkflowDependencyIssue{
		Code:           WorkflowDependencyIssueCallDepthExceeded,
		WorkflowRef:    oneRef,
		Path:           "/jobs/two/uses",
		DependencyKind: WorkflowDependencyKindReusable,
		DependencyName: twoRef,
	}
	if !reflect.DeepEqual(report.Issues, []WorkflowDependencyIssue{want}) {
		t.Fatalf("issues = %#v, want %#v", report.Issues, want)
	}
}

func TestCheckWorkflowDependencyClosureChecksDepthAcrossDiamondPaths(t *testing.T) {
	rootRef := "workflows/root.yml"
	aRef := "workflows/a.yml"
	bRef := "workflows/b.yml"
	cRef := "workflows/c.yml"
	loader := &dependencyTestLoader{
		workflows: map[string]*Workflow{
			aRef: {Jobs: map[string]Job{"c": {Uses: cRef}}},
			bRef: {Jobs: map[string]Job{"a": {Uses: aRef}}},
			cRef: dependencyTestLeafWorkflow(),
		},
		calls: make(map[string]int),
	}
	report, err := CheckWorkflowDependencyClosure(context.Background(), WorkflowDependencyCheckRequest{
		RootRef: rootRef,
		RootWorkflow: &Workflow{Jobs: map[string]Job{
			"a": {Uses: aRef},
			"b": {Uses: bRef},
		}},
		Loader:       loader,
		MaxCallDepth: 2,
	})
	if err != nil {
		t.Fatalf("CheckWorkflowDependencyClosure() error = %v", err)
	}
	want := dependencyTestIssue(
		WorkflowDependencyIssueCallDepthExceeded,
		aRef,
		"/jobs/c/uses",
		cRef,
	)
	if !reflect.DeepEqual(report.Issues, []WorkflowDependencyIssue{want}) {
		t.Fatalf("issues = %#v, want %#v", report.Issues, want)
	}
	for _, ref := range []string{aRef, bRef, cRef} {
		if loader.calls[ref] != 1 {
			t.Fatalf("loader calls[%q] = %d, want 1", ref, loader.calls[ref])
		}
	}
}

func TestCheckWorkflowDependencyClosureReportsCycleAndDepthIndependently(t *testing.T) {
	rootRef := "workflows/root.yml"
	childRef := "workflows/child.yml"
	loader := &dependencyTestLoader{
		workflows: map[string]*Workflow{
			childRef: {Jobs: map[string]Job{"root": {Uses: rootRef}}},
		},
		calls: make(map[string]int),
	}
	report, err := CheckWorkflowDependencyClosure(context.Background(), WorkflowDependencyCheckRequest{
		RootRef:      rootRef,
		RootWorkflow: &Workflow{Jobs: map[string]Job{"child": {Uses: childRef}}},
		Loader:       loader,
		MaxCallDepth: 1,
	})
	if err != nil {
		t.Fatalf("CheckWorkflowDependencyClosure() error = %v", err)
	}
	want := []WorkflowDependencyIssue{
		dependencyTestIssue(
			WorkflowDependencyIssueCallDepthExceeded,
			childRef,
			"/jobs/root/uses",
			rootRef,
		),
		dependencyTestIssue(
			WorkflowDependencyIssueReusableCycle,
			childRef,
			"/jobs/root/uses",
			rootRef,
		),
	}
	if !reflect.DeepEqual(report.Issues, want) {
		t.Fatalf("issues = %#v, want %#v", report.Issues, want)
	}
}

func TestCheckWorkflowDependencyClosureBoundsDefinitionsAndOccurrences(t *testing.T) {
	t.Run("definitions", func(t *testing.T) {
		root := &Workflow{Jobs: make(map[string]Job)}
		loader := &dependencyTestLoader{
			workflows: make(map[string]*Workflow),
			calls:     make(map[string]int),
		}
		for index := 0; index < maxWorkflowDependencyDefinitions+8; index++ {
			ref := fmt.Sprintf("workflows/child-%03d.yml", index)
			root.Jobs[fmt.Sprintf("child-%03d", index)] = Job{Uses: ref}
			loader.workflows[ref] = dependencyTestLeafWorkflow()
		}
		report, err := CheckWorkflowDependencyClosure(
			context.Background(),
			WorkflowDependencyCheckRequest{
				RootRef:      "workflows/root.yml",
				RootWorkflow: root,
				Loader:       loader,
				MaxCallDepth: 2,
			},
		)
		if err != nil {
			t.Fatalf("CheckWorkflowDependencyClosure() error = %v", err)
		}
		loadCount := 0
		for _, count := range loader.calls {
			loadCount += count
		}
		if loadCount > maxWorkflowDependencyDefinitions-1 {
			t.Fatalf("loaded definitions = %d", loadCount)
		}
		if !dependencyReportHasIssue(
			report,
			WorkflowDependencyIssueAnalysisLimitExceeded,
		) {
			t.Fatalf("issues = %#v, want analysis limit", report.Issues)
		}
	})

	t.Run("occurrences", func(t *testing.T) {
		steps := make([]Step, maxWorkflowDependencyOccurrences+8)
		for index := range steps {
			steps[index] = Step{Uses: fmt.Sprintf("tool/tool-%04d", index)}
		}
		report, err := CheckWorkflowDependencyClosure(
			context.Background(),
			WorkflowDependencyCheckRequest{
				RootRef: "workflows/root.yml",
				RootWorkflow: &Workflow{Jobs: map[string]Job{
					"many": {Steps: steps},
				}},
			},
		)
		if err != nil {
			t.Fatalf("CheckWorkflowDependencyClosure() error = %v", err)
		}
		if len(report.Dependencies) != maxWorkflowDependencyOccurrences {
			t.Fatalf("dependencies = %d", len(report.Dependencies))
		}
		if !dependencyReportHasIssue(
			report,
			WorkflowDependencyIssueAnalysisLimitExceeded,
		) {
			t.Fatalf("issues = %#v, want analysis limit", report.Issues)
		}
	})
}

func TestCheckWorkflowDependencyClosureValidatesReusableCallContracts(t *testing.T) {
	rootRef := "workflows/root.yml"
	childRef := "workflows/child.yml"
	child := dependencyTestLeafWorkflow()
	child.On.WorkflowCall = &WorkflowCall{
		Inputs: map[string]Input{
			"count":   {Type: "number", Required: true},
			"enabled": {Type: "boolean", Default: true},
			"title":   {Type: "string", Required: true},
		},
		Secrets: map[string]Secret{
			"optional": {},
			"token":    {Required: true},
		},
	}
	root := &Workflow{Jobs: map[string]Job{
		"dynamic": {
			Uses: childRef,
			With: map[string]any{
				"count": "${{ inputs.count }}",
				"title": "dynamic",
			},
			Secrets: "inherit",
		},
		"invalid": {
			Uses: childRef,
			With: map[string]any{
				"count": 1,
				"title": "invalid secrets mode",
			},
			Secrets: "all",
		},
		"mapped": {
			Uses: childRef,
			With: map[string]any{
				"count": 2,
				"title": "mapped",
			},
			Secrets: map[string]any{"token": "${{ secrets.parent_token }}"},
		},
		"mismatch": {
			Uses: childRef,
			With: map[string]any{
				"count": "prefix-${{ inputs.count }}",
				"title": 7,
			},
			Secrets: map[string]any{"token": ""},
		},
		"missing": {Uses: childRef},
	}}
	loader := &dependencyTestLoader{
		workflows: map[string]*Workflow{childRef: child},
		calls:     make(map[string]int),
	}

	report, err := CheckWorkflowDependencyClosure(context.Background(), WorkflowDependencyCheckRequest{
		RootRef:      rootRef,
		RootWorkflow: root,
		Loader:       loader,
	})
	if err != nil {
		t.Fatalf("CheckWorkflowDependencyClosure() error = %v", err)
	}
	want := []WorkflowDependencyIssue{
		dependencyTestIssue(
			WorkflowDependencyIssueInvalidSecrets,
			rootRef,
			"/jobs/invalid/secrets",
			childRef,
		),
		dependencyTestIssue(
			WorkflowDependencyIssueMissingSecret,
			rootRef,
			"/jobs/mismatch/secrets/token",
			childRef,
		),
		dependencyTestIssue(
			WorkflowDependencyIssueInputTypeMismatch,
			rootRef,
			"/jobs/mismatch/with/count",
			childRef,
		),
		dependencyTestIssue(
			WorkflowDependencyIssueInputTypeMismatch,
			rootRef,
			"/jobs/mismatch/with/title",
			childRef,
		),
		dependencyTestIssue(
			WorkflowDependencyIssueMissingSecret,
			rootRef,
			"/jobs/missing/secrets/token",
			childRef,
		),
		dependencyTestIssue(
			WorkflowDependencyIssueMissingInput,
			rootRef,
			"/jobs/missing/with/count",
			childRef,
		),
		dependencyTestIssue(
			WorkflowDependencyIssueMissingInput,
			rootRef,
			"/jobs/missing/with/title",
			childRef,
		),
	}
	if !reflect.DeepEqual(report.Issues, want) {
		t.Fatalf("issues = %#v, want %#v", report.Issues, want)
	}
}

func TestCheckWorkflowDependencyClosureReturnsOnlySafeLoadIssues(t *testing.T) {
	rootRef := "workflows/root.yml"
	badRef := "workflows/bad.yml"
	missingRef := "workflows/missing.yml"
	loader := &dependencyTestLoader{
		workflows: map[string]*Workflow{badRef: {}},
		errs: map[string]error{
			missingRef: errors.New("read /private/runtime/secrets/workflow.yml: permission denied"),
		},
		calls: make(map[string]int),
	}
	report, err := CheckWorkflowDependencyClosure(context.Background(), WorkflowDependencyCheckRequest{
		RootRef: rootRef,
		RootWorkflow: &Workflow{Jobs: map[string]Job{
			"bad":     {Uses: badRef},
			"missing": {Uses: missingRef},
		}},
		Loader: loader,
	})
	if err != nil {
		t.Fatalf("CheckWorkflowDependencyClosure() error = %v", err)
	}
	want := []WorkflowDependencyIssue{
		dependencyTestIssue(
			WorkflowDependencyIssueReusableInvalid,
			rootRef,
			"/jobs/bad/uses",
			badRef,
		),
		dependencyTestIssue(
			WorkflowDependencyIssueReusableUnavailable,
			rootRef,
			"/jobs/missing/uses",
			missingRef,
		),
	}
	if !reflect.DeepEqual(report.Issues, want) {
		t.Fatalf("issues = %#v, want %#v", report.Issues, want)
	}
	if strings.Contains(fmt.Sprintf("%#v", report), "/private/") {
		t.Fatalf("report leaked loader error: %#v", report)
	}
}

func TestCheckWorkflowDependencyClosureReportsBoundedLoaderLimitSafely(t *testing.T) {
	rootRef := "workflows/root.yml"
	childRef := "workflows/oversized.yml"
	loader := &dependencyTestLoader{
		errs: map[string]error{
			childRef: fmt.Errorf(
				"private size detail: %w",
				ErrWorkflowDependencyAnalysisLimitExceeded,
			),
		},
		calls: make(map[string]int),
	}
	report, err := CheckWorkflowDependencyClosure(
		context.Background(),
		WorkflowDependencyCheckRequest{
			RootRef: rootRef,
			RootWorkflow: &Workflow{Jobs: map[string]Job{
				"first":  {Uses: childRef},
				"second": {Uses: childRef},
			}},
			Loader: loader,
		},
	)
	if err != nil {
		t.Fatalf("CheckWorkflowDependencyClosure() error = %v", err)
	}
	if loader.calls[childRef] != 1 {
		t.Fatalf("bounded child loads = %d, want 1", loader.calls[childRef])
	}
	if len(report.Issues) != 2 {
		t.Fatalf("issues = %#v, want two bounded edge issues", report.Issues)
	}
	for _, issue := range report.Issues {
		if issue.Code != WorkflowDependencyIssueAnalysisLimitExceeded {
			t.Fatalf("issue = %#v, want analysis limit", issue)
		}
	}
	if strings.Contains(fmt.Sprintf("%#v", report), "private size detail") {
		t.Fatalf("report leaked loader detail: %#v", report)
	}
}

func TestCheckWorkflowDependencyClosureBoundsDiamondDepthTraversal(t *testing.T) {
	const layers = 20
	rootRef := "workflows/root.yml"
	definitions := make(map[string]*Workflow)
	layerRef := func(layer int, side string) string {
		return fmt.Sprintf("workflows/layer-%02d-%s.yml", layer, side)
	}
	root := &Workflow{Jobs: map[string]Job{
		"left":  {Uses: layerRef(0, "left")},
		"right": {Uses: layerRef(0, "right")},
	}}
	for layer := 0; layer < layers; layer++ {
		for _, side := range []string{"left", "right"} {
			ref := layerRef(layer, side)
			if layer == layers-1 {
				definitions[ref] = dependencyTestLeafWorkflow()
				continue
			}
			definitions[ref] = &Workflow{Jobs: map[string]Job{
				"left":  {Uses: layerRef(layer+1, "left")},
				"right": {Uses: layerRef(layer+1, "right")},
			}}
		}
	}
	report, err := CheckWorkflowDependencyClosure(
		context.Background(),
		WorkflowDependencyCheckRequest{
			RootRef:      rootRef,
			RootWorkflow: root,
			Loader: &dependencyTestLoader{
				workflows: definitions,
				calls:     make(map[string]int),
			},
			MaxCallDepth: 64,
		},
	)
	if err != nil {
		t.Fatalf("CheckWorkflowDependencyClosure() error = %v", err)
	}
	if !dependencyReportHasIssue(
		report,
		WorkflowDependencyIssueAnalysisLimitExceeded,
	) {
		t.Fatalf("diamond report did not hit traversal budget: %#v", report.Issues)
	}
}

func TestCheckWorkflowDependencyClosureRejectsInvalidRequestAndCancellation(t *testing.T) {
	if _, err := CheckWorkflowDependencyClosure(context.Background(), WorkflowDependencyCheckRequest{
		RootRef: "workflows/root.yml",
	}); err == nil {
		t.Fatal("nil root workflow error = nil")
	}
	if _, err := CheckWorkflowDependencyClosure(context.Background(), WorkflowDependencyCheckRequest{
		RootRef:      "../root.yml",
		RootWorkflow: dependencyTestLeafWorkflow(),
	}); err == nil {
		t.Fatal("invalid root ref error = nil")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CheckWorkflowDependencyClosure(ctx, WorkflowDependencyCheckRequest{
		RootRef:      "workflows/root.yml",
		RootWorkflow: dependencyTestLeafWorkflow(),
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v, want context.Canceled", err)
	}
}

func TestResolveWorkflowDependencyReadinessSortsAndSanitizesResults(t *testing.T) {
	dependencies := []WorkflowDependencyOccurrence{
		{
			Kind:        WorkflowDependencyKindTool,
			Name:        "missing",
			WorkflowRef: "workflows/root.yml",
			Path:        "/jobs/z/steps/0/uses",
		},
		{
			Kind:        WorkflowDependencyKindAgent,
			Name:        "main",
			WorkflowRef: "workflows/root.yml",
			Path:        "/jobs/a/steps/0/uses",
		},
	}
	resolver := WorkflowDependencyRuntimeResolverFunc(func(
		_ context.Context,
		dependency WorkflowDependencyOccurrence,
	) WorkflowDependencyReadinessCode {
		if dependency.Kind == WorkflowDependencyKindAgent {
			return WorkflowDependencyReadinessReady
		}

		return WorkflowDependencyReadinessCode("raw provider failure: /private/config")
	})
	got, err := ResolveWorkflowDependencyReadiness(context.Background(), dependencies, resolver)
	if err != nil {
		t.Fatalf("ResolveWorkflowDependencyReadiness() error = %v", err)
	}
	want := []WorkflowDependencyReadiness{
		{
			Dependency: dependencies[1],
			Code:       WorkflowDependencyReadinessReady,
			Ready:      true,
		},
		{
			Dependency: dependencies[0],
			Code:       WorkflowDependencyReadinessUnavailable,
			Ready:      false,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("readiness = %#v, want %#v", got, want)
	}
}

func TestResolveWorkflowDependencyReadinessSupportsUncheckedAndCancellation(t *testing.T) {
	dependencies := []WorkflowDependencyOccurrence{{
		Kind:        WorkflowDependencyKindFunction,
		Name:        "workflow.state",
		WorkflowRef: "workflows/root.yml",
		Path:        "/jobs/run/steps/0/uses",
	}}
	got, err := ResolveWorkflowDependencyReadiness(context.Background(), dependencies, nil)
	if err != nil {
		t.Fatalf("ResolveWorkflowDependencyReadiness() error = %v", err)
	}
	if len(got) != 1 ||
		got[0].Code != WorkflowDependencyReadinessUnchecked ||
		got[0].Ready {
		t.Fatalf("unchecked readiness = %#v", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ResolveWorkflowDependencyReadiness(
		ctx,
		dependencies,
		WorkflowDependencyRuntimeResolverFunc(func(
			context.Context,
			WorkflowDependencyOccurrence,
		) WorkflowDependencyReadinessCode {
			t.Fatal("resolver called after cancellation")

			return WorkflowDependencyReadinessReady
		}),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v, want context.Canceled", err)
	}
}

func dependencyReportHasIssue(
	report WorkflowDependencyClosure,
	code WorkflowDependencyIssueCode,
) bool {
	for _, issue := range report.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

type dependencyTestLoader struct {
	workflows map[string]*Workflow
	errs      map[string]error
	calls     map[string]int
}

func (l *dependencyTestLoader) LoadReusableWorkflow(
	_ context.Context,
	ref string,
) (*Workflow, error) {
	l.calls[ref]++
	if err := l.errs[ref]; err != nil {
		return nil, err
	}

	return l.workflows[ref], nil
}

func dependencyTestLeafWorkflow() *Workflow {
	return &Workflow{Jobs: map[string]Job{
		"run": {
			RunsOn: "picoclaw",
			Steps:  []Step{{Uses: "function/workflow.state"}},
		},
	}}
}

func dependencyTestIssue(
	code WorkflowDependencyIssueCode,
	workflowRef string,
	path string,
	dependencyName string,
) WorkflowDependencyIssue {
	return WorkflowDependencyIssue{
		Code:           code,
		WorkflowRef:    workflowRef,
		Path:           path,
		DependencyKind: WorkflowDependencyKindReusable,
		DependencyName: dependencyName,
	}
}
