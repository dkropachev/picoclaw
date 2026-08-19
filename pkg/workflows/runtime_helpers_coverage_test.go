package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func TestExpressionRuntimeCoversFailureAndCoercionMatrix(t *testing.T) {
	ctx := expressionContext{
		Inputs: map[string]any{
			"ok": true, "text": "value", "number": 2,
		},
		Private: map[string]any{"answer": "yes"},
		Secrets: map[string]string{"token": "secret"},
		Event:   map[string]any{"kind": "push"},
		Steps: map[string]StepExecution{
			"build": {Outputs: map[string]any{"count": 2}, Status: RunStatusSucceeded},
		},
		Needs: map[string]JobExecution{
			"test": {Outputs: map[string]any{"passed": true}, Status: RunStatusSucceeded},
		},
		Jobs: map[string]JobExecution{
			"deploy": {Outputs: map[string]any{"url": "example"}, Status: RunStatusSucceeded},
		},
		Delivery: Delivery{
			Channel: "chat", ChatID: "room", ReplyHandles: map[string]string{"one": "handle"},
		},
		Session: "session-key",
	}
	if _, err := renderValue(map[string]any{"bad": "${{ unknown.value }}"}, ctx); err == nil {
		t.Fatal("map render ignored expression error")
	}
	if _, err := renderValue([]any{"${{ unknown.value }}"}, ctx); err == nil {
		t.Fatal("slice render ignored expression error")
	}
	if _, err := renderMap(map[string]any{"bad": "${{ unknown.value }}"}, ctx); err == nil {
		t.Fatal("renderMap ignored expression error")
	}
	if _, err := renderString("${{ unknown.value }} ${{ inputs.ok }}", ctx); err == nil {
		t.Fatal("interpolated expression error was ignored")
	}
	if _, err := evalIf("${{ unknown.value }}", ctx); err == nil {
		t.Fatal("if expression error was ignored")
	}

	tests := []struct {
		expression string
		want       any
		wantErr    bool
	}{
		{expression: "inputs.ok and inputs.number > 1", want: true},
		{expression: "false and unknown.value", want: false},
		{expression: "true and unknown.value", wantErr: true},
		{expression: "unknown.value == 1", wantErr: true},
		{expression: "1 == unknown.value", wantErr: true},
		{expression: "not unknown.value", wantErr: true},
		{expression: `"quoted"`, want: "quoted"},
		{expression: "null", want: nil},
		{expression: "2.5", want: float64(2.5)},
	}
	for _, test := range tests {
		got, err := evalExpression(test.expression, ctx)
		if test.wantErr {
			if err == nil {
				t.Fatalf("evalExpression(%q) succeeded: %#v", test.expression, got)
			}
			continue
		}
		if err != nil || canonicalJSON(got) != canonicalJSON(test.want) {
			t.Fatalf("evalExpression(%q) = (%#v, %v), want %#v", test.expression, got, err, test.want)
		}
	}
	quotedExpression := `'one and two' and "three\\" and four" and inputs.ok`
	if parts, ok := splitExpressionLogicalAND(quotedExpression); !ok || len(parts) != 3 {
		t.Fatalf("quoted AND split = %#v, %t", parts, ok)
	}
	if parts, ok := splitExpressionLogicalAND("left and right"); !ok || len(parts) != 2 {
		t.Fatalf("AND split = %#v, %t", parts, ok)
	}

	lookupErrors := []string{
		"", "steps", "steps.missing", "needs", "needs.missing", "jobs", "jobs.missing",
		"unknown.value", "session.value",
	}
	for _, path := range lookupErrors {
		if _, err := lookupPath(path, ctx); err == nil {
			t.Fatalf("lookupPath(%q) succeeded", path)
		}
	}
	lookupValues := map[string]any{
		"private.answer":            "yes",
		"secrets.token":             "secret",
		"event.kind":                "push",
		"steps.build.outputs.count": 2,
		"needs.test.outputs.passed": true,
		"jobs.deploy.outputs.url":   "example",
		"delivery.channel":          "chat",
		"session":                   "session-key",
	}
	for path, want := range lookupValues {
		got, err := lookupPath(path, ctx)
		if err != nil || canonicalJSON(got) != canonicalJSON(want) {
			t.Fatalf("lookupPath(%q) = (%#v, %v), want %#v", path, got, err, want)
		}
	}
	if value, err := lookupPath("inputs.missing", ctx); err != nil || value != nil {
		t.Fatalf("missing input lookup = (%#v, %v)", value, err)
	}
}

func TestExpressionNumericAndTruthinessMatrix(t *testing.T) {
	comparisons := []struct {
		left  any
		op    string
		right any
		want  bool
	}{
		{1, "==", json.Number("1.0"), true},
		{1, "!=", 2, true},
		{2, ">", 1, true},
		{2, ">=", 2, true},
		{1, "<", 2, true},
		{2, "<=", 2, true},
		{"a", "==", "a", true},
		{"a", "!=", "b", true},
		{"2", ">", "1", true},
		{"bad", ">", "also-bad", false},
		{1, "unsupported", 1, false},
	}
	for _, test := range comparisons {
		if got := compareValues(test.left, test.op, test.right); got != test.want {
			t.Fatalf("compareValues(%#v, %q, %#v) = %t", test.left, test.op, test.right, got)
		}
	}
	for _, value := range []any{int64(1), float64(1), json.Number("1"), math.Inf(1), "not-number"} {
		_, _ = asNumericRat(value)
		_, _ = asNumericFloat(value)
		_, _ = asFloat(value)
	}
	truthValues := []any{
		nil,
		false,
		true,
		"",
		"false",
		"yes",
		0,
		1,
		int64(0),
		int64(1),
		float64(0),
		float64(1),
		json.Number("0"),
		json.Number("1"),
		struct{}{},
	}
	for _, value := range truthValues {
		_ = truthy(value)
	}
	if got := deliveryMap(Delivery{MessageID: "message"}); got["message_id"] != "message" {
		t.Fatalf("delivery map = %#v", got)
	}
}

func TestExecutorConcurrencyCursorAndSecretFailureContracts(t *testing.T) {
	ctx := context.Background()
	executor := &Executor{MaxConcurrentRuns: 1}
	if err := executor.enforceConcurrency(ctx, nil); err != nil {
		t.Fatalf("nil store concurrency error = %v", err)
	}
	workspace := t.TempDir()
	store := NewFileRunStore(workspace)
	now := time.Now().UTC()
	if err := store.CreateRun(ctx, &Run{
		ID: "wr_running", Status: RunStatusRunning, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := executor.enforceConcurrency(ctx, store); !errors.Is(err, ErrRunConcurrencyLimit) {
		t.Fatalf("concurrency error = %v", err)
	}

	steps := []Step{{ID: "first"}, {ID: "second"}, {ID: "third"}}
	run := &Run{Steps: map[string]StepExecution{
		"main/first": {Status: RunStatusSucceeded},
		"main/third": {Status: RunStatusSucceeded},
	}}
	if _, err := persistedWorkflowStepPrefix(run, "main", steps); err == nil {
		t.Fatal("gapped persisted cursor was accepted")
	}
	run.Steps = map[string]StepExecution{
		"main/first":  {Status: RunStatusSucceeded},
		"main/second": {Status: RunStatusSkipped},
	}
	if prefix, err := persistedWorkflowStepPrefix(run, "main", steps); err != nil || prefix != 2 {
		t.Fatalf("persisted prefix = (%d, %v)", prefix, err)
	}

	exprCtx := expressionContext{Secrets: map[string]string{"token": "secret"}}
	for _, raw := range []any{"unsupported", []any{"not-map"}} {
		if _, err := renderJobSecrets(raw, ExecutionContext{}, nil); err == nil {
			t.Fatalf("invalid job secrets %#v were accepted", raw)
		}
	}
	if _, err := renderSecretValue("value", map[string]any{"nested": nil}, exprCtx); err == nil {
		t.Fatal("nested missing secret was accepted")
	}
	if _, err := renderSecretValue("value", []any{"present", nil}, exprCtx); err == nil {
		t.Fatal("array missing secret was accepted")
	}
	if _, err := renderSecretString("value", "${{ secrets.missing }}", exprCtx); err == nil {
		t.Fatal("missing exact secret was accepted")
	}
	if _, err := renderSecretString("value", "prefix-${{ secrets.missing }}", exprCtx); err == nil {
		t.Fatal("missing interpolated secret was accepted")
	}
	if _, err := renderSecretString("value", "${{ unknown.value }}-${{ secrets.token }}", exprCtx); err == nil {
		t.Fatal("invalid interpolated secret expression was accepted")
	}
	if value, err := renderSecretString("value", "plain", exprCtx); err != nil || value != "plain" {
		t.Fatalf("plain secret = (%#v, %v)", value, err)
	}

	inputCases := []struct {
		typeName string
		value    any
	}{
		{typeName: "string", value: true},
		{typeName: "number", value: "bad"},
		{typeName: "boolean", value: "true"},
		{typeName: "object", value: []any{}},
		{typeName: "array", value: map[string]any{}},
	}
	for _, test := range inputCases {
		if err := validateWorkflowInputValue("input", test.typeName, test.value); err == nil {
			t.Fatalf("invalid %s input was accepted", test.typeName)
		}
	}
}

func TestFileRunStorePrunesOnlyOldTerminalRuns(t *testing.T) {
	ctx := context.Background()
	store := NewFileRunStore(t.TempDir())
	now := time.Now().UTC()
	old := now.Add(-2 * time.Hour)
	fresh := now.Add(-10 * time.Minute)
	runs := []*Run{
		{ID: "wr_old_success", Status: RunStatusSucceeded, CreatedAt: old, UpdatedAt: old, CompletedAt: &old},
		{ID: "wr_old_skipped", Status: RunStatusSkipped, CreatedAt: old, UpdatedAt: old},
		{ID: "wr_fresh_failed", Status: RunStatusFailed, CreatedAt: fresh, UpdatedAt: fresh, CompletedAt: &fresh},
		{ID: "wr_old_running", Status: RunStatusRunning, CreatedAt: old, UpdatedAt: old},
	}
	for _, run := range runs {
		if err := store.CreateRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := store.PruneTerminalRuns(ctx, now.Add(time.Hour))
	if err != nil || deleted != 3 {
		t.Fatalf("PruneTerminalRuns() = (%d, %v)", deleted, err)
	}
	remaining, err := store.ListRuns(ctx)
	if err != nil || len(remaining) != 1 {
		t.Fatalf("remaining runs = (%#v, %v)", remaining, err)
	}
}

func TestCompatibilityAndStructuredOutputCompatibilityWrappers(t *testing.T) {
	if got := MergeStructuredOutputs(nil, nil); canonicalJSON(got) != canonicalJSON(map[string]any{}) {
		t.Fatalf("MergeStructuredOutputs(nil) = %#v", got)
	}
	if err := EnsureWorkflowSnapshotsRunnable(
		context.Background(), t.TempDir(), nil, RuntimeCompatibility{},
	); err == nil {
		t.Fatal("EnsureWorkflowSnapshotsRunnable(nil) accepted an empty snapshot set")
	}
	if !strings.Contains(strings.ToLower(safeID(" ../bad\\id ")), "bad") {
		t.Fatal("safeID did not preserve bounded identity text")
	}
}
