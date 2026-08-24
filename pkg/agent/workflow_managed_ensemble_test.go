package agent

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestWorkflowManagedEnsembleAssignsEveryScopeChunkToEveryModel(t *testing.T) {
	req := workflows.AgentRequest{
		Managed: map[string]any{
			"strategy":            "scope_split",
			"max_items_per_chunk": 1,
			"reviewer_models":     "review-a, review-b",
		},
		Scope: []any{
			map[string]any{"path": "a.go", "fileHash": "a"},
			map[string]any{"path": "b.go", "fileHash": "b"},
		},
		Output: workflowManagedTestOutputContract(),
	}
	options := workflowManagedOptions(req.Managed)
	plans := workflowManagedChildPlans(req, &AgentInstance{}, options, "scope_split")
	if len(plans) != 4 {
		t.Fatalf("plans=%#v, want two files by two models", plans)
	}
	want := []struct {
		path  string
		model string
	}{{"a.go", "review-a"}, {"a.go", "review-b"}, {"b.go", "review-a"}, {"b.go", "review-b"}}
	for index, expected := range want {
		path := plans[index].scope[0].(map[string]any)["path"]
		if path != expected.path || plans[index].modelName != expected.model {
			t.Fatalf(
				"plan %d path=%v model=%q, want %q/%q",
				index,
				path,
				plans[index].modelName,
				expected.path,
				expected.model,
			)
		}
	}
}

func TestWorkflowManagedSplitRejectsPlansBeyondBoundedChildLimit(t *testing.T) {
	scope := make([]any, 513)
	for index := range scope {
		scope[index] = map[string]any{"path": fmt.Sprintf("file-%04d.go", index)}
	}
	request := workflows.AgentRequest{
		Prompt: "Review every assigned file.",
		Scope:  scope,
		Managed: map[string]any{
			"strategy":            "scope_split",
			"max_items_per_chunk": 1,
			"reviewer_models":     []any{"a", "b", "c", "d", "e", "f", "g", "h"},
		},
	}
	called := false
	_, err := (&workflowAgentRunner{}).runManagedSplit(
		request,
		&AgentInstance{ID: "main", Model: "default"},
		"main", "", "none", "none", "", "scope_split",
		func(string, bool, workflowAgentRunOptions) (string, error) {
			called = true
			return "", nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "more than") || called {
		t.Fatalf("oversized managed split error=%v called=%t", err, called)
	}
}

func TestWorkflowManagedExplicitScopeGroupsAndTaskContextStayBounded(t *testing.T) {
	if groups, explicit := workflowManagedExplicitScopeGroups([]any{"not-an-object"}, 2); explicit || groups != nil {
		t.Fatalf("non-object explicit groups = (%#v, %t)", groups, explicit)
	}
	overflow := []any{
		map[string]any{"reviewGroup": "one"},
		map[string]any{"reviewGroup": "one"},
		map[string]any{"reviewGroup": "one"},
	}
	if groups, explicit := workflowManagedExplicitScopeGroups(overflow, 2); explicit || groups != nil {
		t.Fatalf("oversized explicit group = (%#v, %t)", groups, explicit)
	}
	valid := []any{
		map[string]any{"reviewGroup": "one", "path": "a.go"},
		map[string]any{"reviewGroup": "two", "path": "b.go"},
	}
	groups, explicit := workflowManagedExplicitScopeGroups(valid, 2)
	if !explicit || len(groups) != 2 {
		t.Fatalf("valid explicit groups = (%#v, %t)", groups, explicit)
	}

	contextText := strings.Join([]string{
		"Keep this context.",
		"",
		"Assigned textual agent tasks:",
		"",
		"- old task",
		"- another old task",
		"Keep this suffix.",
	}, "\n")
	cleaned := workflowContextWithoutAssignedTasks(contextText)
	if strings.Contains(cleaned, "old task") || !strings.Contains(cleaned, "Keep this context") ||
		!strings.Contains(cleaned, "Keep this suffix") {
		t.Fatalf("cleaned managed context = %q", cleaned)
	}
	request := workflowManagedApplyTasks(workflows.AgentRequest{Context: contextText}, []string{"new task"})
	if strings.Contains(request.Context, "old task") || !strings.Contains(request.Context, "new task") {
		t.Fatalf("updated managed task context = %q", request.Context)
	}
}

func TestEnsureWorkflowManagedProvidersDeduplicatesUnavailableReviewers(t *testing.T) {
	runner := &workflowAgentRunner{loop: &AgentLoop{cfg: config.DefaultConfig()}}
	err := runner.ensureWorkflowManagedProviders(&AgentInstance{Model: "default"}, map[string]any{
		"reviewer_models": []any{"same"},
		"optimization": map[string]any{"model": map[string]any{
			"enabled": true, "candidates": []any{"same"},
		}},
	})
	if err == nil || strings.Count(err.Error(), "same:") != 1 {
		t.Fatalf("deduplicated unavailable reviewer error = %v", err)
	}
}

func TestWorkflowManagedUsageIsPerChildAndAggregate(t *testing.T) {
	req := workflows.AgentRequest{
		Managed: map[string]any{
			"strategy":              "scope_split",
			"max_items_per_chunk":   1,
			"max_parallel_children": 2,
			"reviewer_models":       "review-a, review-b",
			"calibration":           map[string]any{"enabled": false},
		},
		Scope:  []any{map[string]any{"path": "a.go"}},
		Output: workflowManagedTestOutputContract(),
	}
	var observerCalls atomic.Int64
	req.UsageObserver = func(workflows.AgentUsage) error {
		observerCalls.Add(1)
		return nil
	}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	go func() {
		<-started
		<-started
		close(release)
	}()
	var maxConcurrent atomic.Int64
	var current atomic.Int64
	runOnce := func(_ string, _ bool, options workflowAgentRunOptions) (string, error) {
		concurrent := current.Add(1)
		defer current.Add(-1)
		for {
			old := maxConcurrent.Load()
			if concurrent <= old || maxConcurrent.CompareAndSwap(old, concurrent) {
				break
			}
		}
		started <- struct{}{}
		<-release
		model := options.ModelName
		if options.ActualModelName != nil && *options.ActualModelName != "" {
			model = *options.ActualModelName
		}
		usage := workflows.AgentUsage{
			Model: model, PromptTokens: 10, CompletionTokens: 2,
			TotalTokens: 12, CachedTokens: 1,
		}
		if model == "review-b" {
			usage.PromptTokens = 20
			usage.TotalTokens = 22
		}
		if options.UsageObserver == nil {
			return "", errors.New("managed child usage observer is nil")
		}
		if err := options.UsageObserver(usage); err != nil {
			return "", err
		}
		return `{"summary":"reviewed","findings":[]}`, nil
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req, &AgentInstance{Model: "default-model"}, "main", "ephemeral",
		"none", "none", "", "scope_split", runOnce,
	)
	if err != nil {
		t.Fatalf("runManagedSplit() error = %v", err)
	}
	if maxConcurrent.Load() != 2 {
		t.Fatalf("max concurrent children = %d, want 2", maxConcurrent.Load())
	}
	if observerCalls.Load() != 2 {
		t.Fatalf("observer calls = %d, want one per child response", observerCalls.Load())
	}
	wantAggregate := []workflows.AgentUsage{
		{
			Model:            "review-a",
			Reviewer:         "review-a",
			PromptTokens:     10,
			CompletionTokens: 2,
			TotalTokens:      12,
			CachedTokens:     1,
		},
		{
			Model:            "review-b",
			Reviewer:         "review-b",
			PromptTokens:     20,
			CompletionTokens: 2,
			TotalTokens:      22,
			CachedTokens:     1,
		},
	}
	if got := outputs["usage"]; !reflect.DeepEqual(got, wantAggregate) {
		t.Fatalf("aggregate usage = %#v, want %#v", got, wantAggregate)
	}
	children, ok := outputs["managed_children"].([]map[string]any)
	if !ok || len(children) != 2 {
		t.Fatalf("managed children = %#v", outputs["managed_children"])
	}
	seen := make(map[string]workflows.AgentUsage, 2)
	for _, child := range children {
		usage, ok := child["usage"].([]workflows.AgentUsage)
		if !ok || len(usage) != 1 {
			t.Fatalf("child usage = %#v, want one exact model aggregate", child["usage"])
		}
		seen[usage[0].Model] = usage[0]
	}
	if !reflect.DeepEqual(seen, map[string]workflows.AgentUsage{
		"review-a": wantAggregate[0],
		"review-b": wantAggregate[1],
	}) {
		t.Fatalf("per-child usage = %#v", seen)
	}
}

func TestWorkflowManagedParallelismKeepsOneCallPerReviewer(t *testing.T) {
	req := workflows.AgentRequest{
		Managed: map[string]any{
			"strategy":                  "scope_split",
			"max_items_per_chunk":       1,
			"max_parallel_children":     4,
			"max_parallel_per_reviewer": 1,
			"reviewer_models":           "review-a,review-b",
			"continue_on_child_error":   false,
			"calibration":               map[string]any{"enabled": false},
		},
		Scope: []any{
			map[string]any{"path": "a.go"},
			map[string]any{"path": "b.go"},
		},
		Output: workflowManagedTestOutputContract(),
	}
	started := make(chan struct{}, 4)
	releaseFirst := make(chan struct{})
	go func() {
		<-started
		<-started
		close(releaseFirst)
	}()
	var mu sync.Mutex
	current := make(map[string]int)
	maximum := make(map[string]int)
	globalCurrent, globalMaximum := 0, 0
	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req,
		&AgentInstance{Model: "default"},
		"main",
		"ephemeral",
		"none",
		"none",
		"",
		"scope_split",
		func(_ string, _ bool, options workflowAgentRunOptions) (string, error) {
			model := options.ModelName
			mu.Lock()
			current[model]++
			maximum[model] = max(maximum[model], current[model])
			globalCurrent++
			globalMaximum = max(globalMaximum, globalCurrent)
			mu.Unlock()
			started <- struct{}{}
			<-releaseFirst
			mu.Lock()
			current[model]--
			globalCurrent--
			mu.Unlock()
			return `{"summary":"reviewed","findings":[]}`, nil
		},
	)
	if err != nil || outputs == nil || maximum["review-a"] != 1 || maximum["review-b"] != 1 ||
		globalMaximum != 2 {
		t.Fatalf(
			"reviewer concurrency maximum=%#v global=%d outputs=%#v err=%v",
			maximum,
			globalMaximum,
			outputs,
			err,
		)
	}
}

func TestWorkflowManagedParallelismAssignsDefaultReviewerSlot(t *testing.T) {
	plans := []workflowManagedChildPlan{{
		index: 1,
		label: "default reviewer",
		scope: []any{map[string]any{"path": "a.go"}},
	}}
	results := workflowRunManagedChildren(
		workflows.AgentRequest{Output: workflowManagedTestOutputContract()},
		&AgentInstance{Model: "review-default"},
		nil,
		workflowManagedExecutionOptions{
			maxParallelChildren:    1,
			maxParallelPerReviewer: 1,
		},
		"scope_split",
		plans,
		func(_ string, _ bool, _ workflowAgentRunOptions) (string, error) {
			return `{"summary":"reviewed","findings":[]}`, nil
		},
	)
	if len(results) != 1 || results[0].err != nil ||
		results[0].choice.modelName != "review-default" {
		t.Fatalf("default reviewer result = %#v", results)
	}
}

func TestWorkflowManagedChildrenEmitExactLifecycle(t *testing.T) {
	plans := []workflowManagedChildPlan{
		{
			index: 1, label: "scope chunk 1 of 2, reviewer 1 of 1 (review-a)",
			scope: []any{map[string]any{"path": "a.go"}}, modelName: "review-a",
		},
		{
			index: 2, label: "scope chunk 2 of 2, reviewer 1 of 1 (review-a)",
			scope: []any{map[string]any{"path": "b.go"}}, modelName: "review-a",
		},
	}
	var events []workflows.ManagedChildActivity
	request := workflows.AgentRequest{
		Output: workflowManagedTestOutputContract(),
		ManagedChildObserver: func(event workflows.ManagedChildActivity) error {
			events = append(events, event)
			return nil
		},
	}
	var calls int
	results := workflowRunManagedChildren(
		request,
		&AgentInstance{Model: "default"},
		nil,
		workflowManagedExecutionOptions{maxParallelChildren: 1},
		"scope_split",
		plans,
		func(_ string, _ bool, _ workflowAgentRunOptions) (string, error) {
			calls++
			if calls == 2 {
				return "", errors.New("candidate failed")
			}
			return `{"summary":"reviewed","findings":[]}`, nil
		},
	)
	if len(results) != 2 || len(events) != 4 {
		t.Fatalf("managed results/events = %d/%#v", len(results), events)
	}
	for index, event := range events {
		wantIndex := index/2 + 1
		wantPhase := workflows.ManagedChildStarted
		if index%2 == 1 {
			wantPhase = workflows.ManagedChildCompleted
		}
		if event.Index != wantIndex || event.Total != 2 || event.Phase != wantPhase ||
			event.ModelAlias != "review-a" || event.ScopeCount != 1 || event.Label != plans[wantIndex-1].label {
			t.Fatalf("managed lifecycle event %d = %#v", index, event)
		}
	}
	if !events[1].Success || events[3].Success {
		t.Fatalf("managed completion success = %#v", events)
	}
}

func TestWorkflowManagedChildLifecycleObserverErrorsFenceExecution(t *testing.T) {
	plan := []workflowManagedChildPlan{{index: 1, label: "child", modelName: "review-a"}}
	startErr := errors.New("start observation failed")
	var calls int
	results := workflowRunManagedChildren(
		workflows.AgentRequest{
			ManagedChildObserver: func(workflows.ManagedChildActivity) error { return startErr },
		},
		&AgentInstance{Model: "default"},
		nil,
		workflowManagedExecutionOptions{maxParallelChildren: 1},
		"scope_split",
		plan,
		func(_ string, _ bool, _ workflowAgentRunOptions) (string, error) {
			calls++
			return "", nil
		},
	)
	if calls != 0 || len(results) != 1 || !errors.Is(results[0].err, startErr) ||
		!errors.Is(results[0].err, workflows.ErrManagedChildActivityNotRecorded) {
		t.Fatalf("start observer fence calls=%d results=%#v", calls, results)
	}

	completeErr := errors.New("completion observation failed")
	results = workflowRunManagedChildren(
		workflows.AgentRequest{
			Output: workflowManagedTestOutputContract(),
			ManagedChildObserver: func(activity workflows.ManagedChildActivity) error {
				if activity.Phase == workflows.ManagedChildCompleted {
					return completeErr
				}
				return nil
			},
		},
		&AgentInstance{Model: "default"},
		nil,
		workflowManagedExecutionOptions{maxParallelChildren: 1},
		"scope_split",
		plan,
		func(_ string, _ bool, _ workflowAgentRunOptions) (string, error) {
			return `{"summary":"reviewed","findings":[]}`, nil
		},
	)
	if len(results) != 1 || !errors.Is(results[0].err, completeErr) ||
		!errors.Is(results[0].err, workflows.ErrManagedChildActivityNotRecorded) {
		t.Fatalf("completion observer fence results=%#v", results)
	}

	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		workflows.AgentRequest{
			Managed: map[string]any{
				"strategy": "scope_split", "reviewer_models": "review-a",
				"continue_on_child_error": true,
				"calibration":             map[string]any{"enabled": false},
			},
			Scope:  []any{map[string]any{"path": "single.go"}},
			Output: workflowManagedTestOutputContract(),
			ManagedChildObserver: func(activity workflows.ManagedChildActivity) error {
				if activity.Phase == workflows.ManagedChildCompleted {
					return completeErr
				}
				return nil
			},
		},
		&AgentInstance{Model: "default"},
		"main",
		"ephemeral",
		"none",
		"none",
		"",
		"scope_split",
		func(_ string, _ bool, _ workflowAgentRunOptions) (string, error) {
			return `{"summary":"reviewed","findings":[]}`, nil
		},
	)
	if !errors.Is(err, workflows.ErrManagedChildActivityNotRecorded) ||
		!errors.Is(err, completeErr) || outputs["managed_children"] == nil {
		t.Fatalf("single-child observer failure outputs=%#v err=%v", outputs, err)
	}
}

func TestWorkflowManagedUsageObserverErrorStopsAfterCurrentBatch(t *testing.T) {
	wantErr := errors.New("shared budget reached")
	var observed atomic.Int64
	req := workflows.AgentRequest{
		Managed: map[string]any{
			"strategy":              "scope_split",
			"max_items_per_chunk":   1,
			"max_parallel_children": 2,
			"reviewer_models":       "review-a, review-b",
			"calibration":           map[string]any{"enabled": false},
		},
		Scope: []any{
			map[string]any{"path": "a.go"},
			map[string]any{"path": "b.go"},
		},
		Output: workflowManagedTestOutputContract(),
		UsageObserver: func(workflows.AgentUsage) error {
			observed.Add(1)
			return wantErr
		},
	}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	go func() {
		<-started
		<-started
		close(release)
	}()
	var providerCalls atomic.Int64
	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req, &AgentInstance{}, "main", "ephemeral", "none", "none", "",
		"scope_split",
		func(_ string, _ bool, options workflowAgentRunOptions) (string, error) {
			providerCalls.Add(1)
			started <- struct{}{}
			<-release
			model := options.ModelName
			if options.ActualModelName != nil {
				model = *options.ActualModelName
			}
			if err := options.UsageObserver(workflows.AgentUsage{
				Model: model, PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12,
			}); err != nil {
				return "", err
			}
			return `{"summary":"reviewed","findings":[]}`, nil
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("runManagedSplit() error = %v, want observer error", err)
	}
	if providerCalls.Load() != 2 || observed.Load() != 2 {
		t.Fatalf(
			"provider calls/observations = %d/%d, want only two in-flight children",
			providerCalls.Load(), observed.Load(),
		)
	}
	usage, ok := outputs["usage"].([]workflows.AgentUsage)
	if !ok || len(usage) != 2 {
		t.Fatalf("failed-batch usage = %#v, want both completed responses", outputs["usage"])
	}
}

func TestWorkflowManagedEnsembleSplitsSingleFileAcrossModels(t *testing.T) {
	req := workflows.AgentRequest{
		Managed: map[string]any{
			"strategy":        "scope_split",
			"reviewer_models": []any{"review-a", "review-b"},
		},
		Scope:  []any{map[string]any{"path": "a.go"}},
		Output: workflowManagedTestOutputContract(),
	}
	if strategy := workflowManagedSplitStrategy(req, &AgentInstance{}); strategy != "scope_split" {
		t.Fatalf("strategy=%q, want scope_split for one file with multiple reviewers", strategy)
	}
}

func TestWorkflowManagedDefaultChainMakesFallbackEnsembleOpportunistic(t *testing.T) {
	options := workflowManagedExecutionOptions{
		includeDefaultReviewer: true,
		reviewerModels:         []string{"review-fallback"},
	}
	plans := workflowManagedChildPlans(
		workflows.AgentRequest{Scope: []any{map[string]any{"path": "a.go"}}},
		&AgentInstance{Model: "review-primary", Fallbacks: []string{"review-fallback"}},
		options,
		"scope_split",
	)
	if len(plans) != 2 || plans[0].modelName != "" || plans[0].optional ||
		plans[1].modelName != "review-fallback" || !plans[1].optional {
		t.Fatalf("default/fallback child plans=%#v", plans)
	}
	if workflowManagedChildOutput(workflowManagedChildResult{plan: plans[0]})["required"] != true ||
		workflowManagedChildOutput(workflowManagedChildResult{plan: plans[1]})["required"] != false {
		t.Fatalf("default/fallback required metadata missing")
	}
}

func TestWorkflowManagedTaskChildReplacesFullAssignmentWithSubset(t *testing.T) {
	req := workflows.AgentRequest{Context: strings.Join([]string{
		"Repository context.",
		"",
		"Assigned textual agent tasks:",
		"- Find correctness bugs.",
		"- Challenge security boundaries.",
	}, "\n")}
	child := workflowManagedApplyTasks(req, []string{"Find correctness bugs."})
	if strings.Contains(child.Context, "Challenge security boundaries") ||
		strings.Count(child.Context, "Assigned textual agent tasks:") != 1 ||
		!strings.Contains(child.Context, "Repository context.") {
		t.Fatalf("child context retained full task assignment:\n%s", child.Context)
	}
}

func TestWorkflowManagedChildScopeProjectionKeepsProvenanceButDropsContentCapability(t *testing.T) {
	result := workflowManagedChildResult{plan: workflowManagedChildPlan{
		scope: []any{
			map[string]any{
				"path": "a.go", "fileHash": "abc", "sizeBytes": int64(12),
				"content": "secret source", "source": map[string]any{"path": "/private/a.go"},
			},
			"raw secret scope",
		},
	}}
	output := workflowManagedChildOutput(result)
	projected, ok := output["scope"].([]any)
	if !ok || len(projected) != 2 || !reflect.DeepEqual(
		projected[0],
		map[string]any{"path": "a.go", "fileHash": "abc", "sizeBytes": int64(12)},
	) {
		t.Fatalf("scope projection=%#v", output["scope"])
	}
	if fallback, ok := projected[1].(map[string]any); !ok || fallback["value_hash"] == "" {
		t.Fatalf("non-object scope projection=%#v, want opaque hash", projected[1])
	}
}

func TestWorkflowManagedScopeChunkingPreservesDeclaredReviewGroups(t *testing.T) {
	scope := []any{
		map[string]any{"path": "a.go", "reviewGroup": "group-a"},
		map[string]any{"path": "b.go", "reviewGroup": "group-a"},
		map[string]any{"path": "c.go", "reviewGroup": "group-b"},
		map[string]any{"path": "d.go", "reviewGroup": "group-b"},
	}
	chunks := workflowManagedScopeChunks(
		workflows.AgentRequest{Scope: scope},
		workflowManagedExecutionOptions{maxItemsPerChunk: 3, adaptiveChunking: true},
	)
	if len(chunks) != 2 || len(chunks[0]) != 2 || len(chunks[1]) != 2 ||
		chunks[0][0].(map[string]any)["reviewGroup"] != "group-a" ||
		chunks[1][0].(map[string]any)["reviewGroup"] != "group-b" {
		t.Fatalf("declared review group chunks=%#v", chunks)
	}
}

func TestWorkflowManagedContinueOnChildErrorRequiresAtLeastOneSuccess(t *testing.T) {
	req := workflows.AgentRequest{
		Managed: map[string]any{
			"strategy": "scope_split", "max_items_per_chunk": 1,
			"max_parallel_children": 2, "continue_on_child_error": true,
			"calibration": map[string]any{"enabled": false},
		},
		Scope:  []any{map[string]any{"path": "a.go"}, map[string]any{"path": "b.go"}},
		Output: workflowManagedTestOutputContract(),
	}
	var calls atomic.Int64
	runOnce := func(_ string, _ bool, _ workflowAgentRunOptions) (string, error) {
		if calls.Add(1) == 1 {
			return "", errors.New("security violation")
		}
		return `{"summary":"reviewed","findings":[]}`, nil
	}
	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req, &AgentInstance{}, "main", "ephemeral", "none", "none", "",
		"scope_split", runOnce,
	)
	if err != nil {
		t.Fatalf("partial managed review error=%v outputs=%#v", err, outputs)
	}
	children, ok := outputs["managed_children"].([]map[string]any)
	if !ok || len(children) != 2 {
		t.Fatalf("managed children=%#v", outputs["managed_children"])
	}
	managed := outputs["managed"].(map[string]any)
	if failures := managed["partial_failures"].(map[string]any); failures["failed_children"] != 1 ||
		failures["continued"] != true {
		t.Fatalf("partial failure metadata=%#v", failures)
	}
}

func TestStructuredRepairRetainsOriginalImmutableEvidenceContext(t *testing.T) {
	contract := workflowManagedTestOutputContract()
	original := "Review immutable file content:\nIMMUTABLE_SOURCE_LINE"
	calls := make([]string, 0, 2)
	runOnce := func(message string, _ bool, options workflowAgentRunOptions) (string, error) {
		calls = append(calls, message)
		if options.UsageObserver == nil {
			return "", errors.New("repair usage observer is nil")
		}
		if err := options.UsageObserver(workflows.AgentUsage{
			Model: "review-model", PromptTokens: len(calls) * 10,
			CompletionTokens: len(calls), TotalTokens: len(calls) * 11,
		}); err != nil {
			return "", err
		}
		if len(calls) == 1 {
			return `{"summary":"missing findings"}`, nil
		}
		return `{"summary":"repaired","findings":[]}`, nil
	}
	_, result, repairs, usage, err := workflowRunStructuredAgentWithOptions(
		original, contract, runOnce, workflowAgentRunOptions{},
	)
	if err != nil || !result.Valid || repairs != 1 || len(calls) != 2 {
		t.Fatalf("repair result=%#v repairs=%d calls=%#v err=%v", result, repairs, calls, err)
	}
	if !reflect.DeepEqual(usage, []workflows.AgentUsage{{
		Model: "review-model", PromptTokens: 30, CompletionTokens: 3, TotalTokens: 33,
	}}) {
		t.Fatalf("repair usage = %#v, want both attempts accumulated", usage)
	}
	if !strings.Contains(calls[1], "IMMUTABLE_SOURCE_LINE") ||
		!strings.Contains(calls[1], "Previous response") {
		t.Fatalf("repair lost original evidence context:\n%s", calls[1])
	}
}

func TestWorkflowManagedHardCapsParallelismAndTotalChildren(t *testing.T) {
	options := workflowManagedOptions(map[string]any{
		"max_parallel_children": 1_000_000,
		"max_items_per_chunk":   1,
	})
	if options.maxParallelChildren != workflowManagedMaximumParallelChildren {
		t.Fatalf(
			"parallel children=%d, want hard cap %d",
			options.maxParallelChildren,
			workflowManagedMaximumParallelChildren,
		)
	}
	scope := make([]any, workflowManagedMaximumChildren+100)
	for index := range scope {
		scope[index] = map[string]any{"path": fmt.Sprintf("%d.go", index)}
	}
	plans := workflowManagedChildPlans(
		workflows.AgentRequest{Scope: scope},
		&AgentInstance{},
		options,
		"scope_split",
	)
	if len(plans) != workflowManagedMaximumChildren+1 {
		t.Fatalf("bounded plan sentinel length=%d", len(plans))
	}
}

func TestWorkflowManagedEnsembleReviewerUsesExactAliasWithoutInheritedFallbacks(t *testing.T) {
	options := workflowManagedExecutionOptions{
		maxParallelChildren: 1,
		reviewerModels:      []string{"review-a"},
	}
	plans := []workflowManagedChildPlan{{index: 1, modelName: "review-a"}}
	var captured workflowAgentRunOptions
	results := workflowRunManagedChildren(
		workflows.AgentRequest{Output: workflowManagedTestOutputContract()},
		&AgentInstance{Model: "default"},
		nil,
		options,
		"scope_split",
		plans,
		func(_ string, _ bool, runOptions workflowAgentRunOptions) (string, error) {
			captured = runOptions
			return `{"summary":"reviewed","findings":[]}`, nil
		},
	)
	if len(results) != 1 || results[0].err != nil || captured.ModelName != "review-a" ||
		captured.ModelFallbacks == nil || len(captured.ModelFallbacks) != 0 {
		t.Fatalf("exact reviewer options=%#v results=%#v", captured, results)
	}
}

func TestWorkflowManagedChildReportsActuallySuccessfulFallbackAlias(t *testing.T) {
	results := workflowRunManagedChildren(
		workflows.AgentRequest{Output: workflowManagedTestOutputContract()},
		&AgentInstance{Model: "review-primary"},
		nil,
		workflowManagedExecutionOptions{maxParallelChildren: 1},
		"scope_split",
		[]workflowManagedChildPlan{{index: 1}},
		func(_ string, _ bool, runOptions workflowAgentRunOptions) (string, error) {
			if runOptions.ActualModelName == nil {
				t.Fatal("managed child did not request successful model provenance")
			}
			*runOptions.ActualModelName = "review-fallback"
			return `{"summary":"reviewed","findings":[]}`, nil
		},
	)
	if len(results) != 1 || results[0].err != nil || results[0].choice.modelName != "review-fallback" ||
		results[0].choice.modelMeta["selected"] != "review-fallback" ||
		results[0].choice.modelMeta["requested"] != "review-primary" {
		t.Fatalf("actual model provenance=%#v", results)
	}
}

func TestWorkflowManagedPartialAggregateExcludesInvalidChildStructuredData(t *testing.T) {
	req := workflows.AgentRequest{
		Managed: map[string]any{
			"strategy": "scope_split", "max_items_per_chunk": 1,
			"max_parallel_children": 1, "continue_on_child_error": true,
			"calibration": map[string]any{"enabled": false},
		},
		Scope:  []any{map[string]any{"path": "a.go"}, map[string]any{"path": "b.go"}},
		Output: workflowManagedTestOutputContract(),
	}
	var calls atomic.Int64
	outputs, err := (&workflowAgentRunner{}).runManagedSplit(
		req, &AgentInstance{}, "main", "ephemeral", "none", "none", "",
		"scope_split",
		func(_ string, _ bool, _ workflowAgentRunOptions) (string, error) {
			switch calls.Add(1) {
			case 1:
				return `{"findings":[{"title":"must not leak"}]}`, nil
			case 2:
				return "", errors.New("repair failed")
			default:
				return `{"summary":"valid sibling","findings":[]}`, nil
			}
		},
	)
	if err != nil {
		t.Fatalf("partial aggregate error=%v outputs=%#v", err, outputs)
	}
	structured := outputs["structured"].(map[string]any)
	if findings := structured["findings"].([]any); len(findings) != 0 {
		t.Fatalf("invalid child findings leaked into partial aggregate: %#v", findings)
	}
}
