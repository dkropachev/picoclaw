package agent

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestWorkflowManagedExactNonnegativeInt64TypeCoverage(t *testing.T) {
	maximumUint := ^uint(0)
	cases := []struct {
		name      string
		value     any
		wantValue int64
		wantOK    bool
	}{
		{name: "int", value: int(7), wantValue: 7, wantOK: true},
		{name: "negative int", value: int(-7), wantValue: -7, wantOK: false},
		{name: "int8", value: int8(8), wantValue: 8, wantOK: true},
		{name: "negative int8", value: int8(-8), wantValue: -8, wantOK: false},
		{name: "int16", value: int16(16), wantValue: 16, wantOK: true},
		{name: "negative int16", value: int16(-16), wantValue: -16, wantOK: false},
		{name: "int32", value: int32(32), wantValue: 32, wantOK: true},
		{name: "negative int32", value: int32(-32), wantValue: -32, wantOK: false},
		{name: "int64", value: int64(64), wantValue: 64, wantOK: true},
		{name: "negative int64", value: int64(-64), wantValue: -64, wantOK: false},
		{name: "uint", value: uint(7), wantValue: 7, wantOK: true},
		{
			name: "uint maximum", value: maximumUint,
			wantValue: func() int64 {
				if uint64(maximumUint) > math.MaxInt64 {
					return 0
				}
				return int64(maximumUint)
			}(),
			wantOK: uint64(maximumUint) <= math.MaxInt64,
		},
		{name: "uint8", value: uint8(8), wantValue: 8, wantOK: true},
		{name: "uint16", value: uint16(16), wantValue: 16, wantOK: true},
		{name: "uint32", value: uint32(32), wantValue: 32, wantOK: true},
		{name: "uint64", value: uint64(64), wantValue: 64, wantOK: true},
		{name: "uint64 overflow", value: uint64(math.MaxUint64), wantValue: 0, wantOK: false},
		{name: "float64 integer", value: float64(12), wantValue: 12, wantOK: true},
		{name: "float64 negative", value: float64(-1), wantValue: -1, wantOK: false},
		{name: "float64 fraction", value: 1.5, wantValue: 1, wantOK: false},
		{name: "json number", value: json.Number("42"), wantValue: 42, wantOK: true},
		{name: "negative json number", value: json.Number("-42"), wantValue: -42, wantOK: false},
		{name: "fractional json number", value: json.Number("4.2"), wantValue: 0, wantOK: false},
		{name: "string unsupported", value: "1", wantValue: 0, wantOK: false},
		{name: "float32 unsupported", value: float32(1), wantValue: 0, wantOK: false},
		{name: "nil unsupported", value: nil, wantValue: 0, wantOK: false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, ok := workflowManagedExactNonnegativeInt64(test.value)
			if got != test.wantValue || ok != test.wantOK {
				t.Fatalf("exact integer %#v = (%d, %t), want (%d, %t)",
					test.value, got, ok, test.wantValue, test.wantOK)
			}
		})
	}
}

func TestWorkflowManagedExplicitAssignmentParserBoundaryCoverage(t *testing.T) {
	validFile := workflowManagedAssignmentTestFile("valid.go", "abc123", 12)
	if plans, err := workflowManagedExplicitAssignmentPlans(nil); err != nil || len(plans) != 0 {
		t.Fatalf("nil assignment plans = %#v, %v", plans, err)
	}
	base := func() map[string]any {
		return map[string]any{
			"assignment_id": "assignment-valid",
			"focus_id":      "correctness_state",
			"task":          "Trace state.",
			"files":         []any{validFile},
		}
	}

	t.Run("valid map slice and camel aliases", func(t *testing.T) {
		file := workflowManagedAssignmentTestFile("camel.go", "def456", 13)
		raw := []map[string]any{{
			"assignmentId": "assignment-camel", "focusId": "security_trust",
			"task": "Trace trust.", "reviewerModel": "review-a",
			"optional": true, "files": []map[string]any{file},
		}}
		plans, err := workflowManagedExplicitAssignmentPlans(raw)
		if err != nil || len(plans) != 1 || plans[0].label != "security_trust" ||
			plans[0].reviewer != "review-a" || !plans[0].optional || len(plans[0].files) != 1 {
			t.Fatalf("camel assignment plans = %#v, %v", plans, err)
		}
		file["path"] = "mutated.go"
		if plans[0].files[0]["path"] != "camel.go" {
			t.Fatalf("assignment file was not detached: %#v", plans[0].files)
		}
	})

	tooMany := make([]any, workflowManagedMaximumChildren+1)
	tooManyFiles := make([]any, workflowManagedMaximumChildren+1)
	invalid := []struct {
		name string
		raw  any
		want string
	}{
		{name: "wrong envelope", raw: "bad", want: "must be an array"},
		{name: "too many plans", raw: tooMany, want: "exceed"},
		{name: "non-object plan", raw: []any{"bad"}, want: "must be an object"},
		{name: "missing assignment", raw: []any{func() map[string]any {
			value := base()
			delete(value, "assignment_id")
			return value
		}()}, want: "invalid identity"},
		{name: "nil focus", raw: []any{func() map[string]any {
			value := base()
			value["focus_id"] = nil
			return value
		}()}, want: "invalid identity"},
		{name: "non-string task", raw: []any{func() map[string]any {
			value := base()
			value["task"] = 42
			return value
		}()}, want: "invalid identity"},
		{name: "padded label", raw: []any{func() map[string]any {
			value := base()
			value["label"] = " padded "
			return value
		}()}, want: "invalid identity"},
		{name: "nul reviewer", raw: []any{func() map[string]any {
			value := base()
			value["reviewer_model"] = "review\x00a"
			return value
		}()}, want: "invalid identity"},
		{name: "oversized assignment", raw: []any{func() map[string]any {
			value := base()
			value["assignment_id"] = strings.Repeat("a", 257)
			return value
		}()}, want: "invalid identity"},
		{name: "oversized focus", raw: []any{func() map[string]any {
			value := base()
			value["focus_id"] = strings.Repeat("f", 257)
			return value
		}()}, want: "invalid identity"},
		{name: "oversized task", raw: []any{func() map[string]any {
			value := base()
			value["task"] = strings.Repeat("t", (64<<10)+1)
			return value
		}()}, want: "invalid identity"},
		{name: "oversized label", raw: []any{func() map[string]any {
			value := base()
			value["label"] = strings.Repeat("l", 4097)
			return value
		}()}, want: "invalid identity"},
		{name: "oversized reviewer", raw: []any{func() map[string]any {
			value := base()
			value["reviewer_model"] = strings.Repeat("r", 257)
			return value
		}()}, want: "invalid identity"},
		{name: "duplicate assignment", raw: []any{base(), base()}, want: "duplicates assignment"},
		{name: "invalid optional", raw: []any{func() map[string]any {
			value := base()
			value["optional"] = "false"
			return value
		}()}, want: "optional flag"},
		{name: "missing files", raw: []any{func() map[string]any {
			value := base()
			delete(value, "files")
			return value
		}()}, want: "has no files"},
		{name: "nil files", raw: []any{func() map[string]any {
			value := base()
			value["files"] = nil
			return value
		}()}, want: "must be an array"},
		{name: "wrong files envelope", raw: []any{func() map[string]any {
			value := base()
			value["files"] = "bad"
			return value
		}()}, want: "must be an array"},
		{name: "too many files", raw: []any{func() map[string]any {
			value := base()
			value["files"] = tooManyFiles
			return value
		}()}, want: "scope is too large"},
		{name: "non-object file", raw: []any{func() map[string]any {
			value := base()
			value["files"] = []any{"bad"}
			return value
		}()}, want: "file 0 must be an object"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			plans, err := workflowManagedExplicitAssignmentPlans(test.raw)
			if err == nil || !strings.Contains(err.Error(), test.want) || plans != nil {
				t.Fatalf("invalid assignment plans = %#v, %v, want %q", plans, err, test.want)
			}
		})
	}
}

func TestWorkflowManagedExplicitScopeBindingBoundaryCoverage(t *testing.T) {
	file := workflowManagedAssignmentTestFile("scope.go", "abc", 10)
	assignment := workflowManagedExplicitAssignmentPlan{
		assignmentID: "assignment", focusID: "correctness_state", label: "correctness",
		task: "Trace state.", reviewer: "review-a", files: []map[string]any{file},
	}
	request := workflows.AgentRequest{Scope: []any{file}}

	invalidScopes := []struct {
		name        string
		request     workflows.AgentRequest
		assignments []workflowManagedExplicitAssignmentPlan
		want        string
	}{
		{
			name:        "non-object scope",
			request:     workflows.AgentRequest{Scope: []any{"bad"}},
			assignments: []workflowManagedExplicitAssignmentPlan{assignment},
			want:        "must be a file object",
		},
		{
			name:        "invalid scope ref",
			request:     workflows.AgentRequest{Scope: []any{map[string]any{}}},
			assignments: []workflowManagedExplicitAssignmentPlan{assignment},
			want:        "not an exact file reference",
		},
		{
			name:        "duplicate scope",
			request:     workflows.AgentRequest{Scope: []any{file, file}},
			assignments: []workflowManagedExplicitAssignmentPlan{assignment},
			want:        "duplicates file",
		},
		{
			name:    "invalid assignment ref",
			request: request,
			assignments: []workflowManagedExplicitAssignmentPlan{func() workflowManagedExplicitAssignmentPlan {
				value := assignment
				value.files = []map[string]any{{}}
				return value
			}()},
			want: "not an exact file reference",
		},
		{
			name:    "duplicate assignment file",
			request: request,
			assignments: []workflowManagedExplicitAssignmentPlan{func() workflowManagedExplicitAssignmentPlan {
				value := assignment
				value.files = []map[string]any{file, file}
				return value
			}()},
			want: "duplicates file",
		},
		{
			name:    "identity drift",
			request: request,
			assignments: []workflowManagedExplicitAssignmentPlan{func() workflowManagedExplicitAssignmentPlan {
				value := assignment
				drifted := cloneAnyMap(file)
				drifted["mode"] = "100755"
				value.files = []map[string]any{drifted}
				return value
			}()},
			want: "does not match the frozen scope",
		},
	}
	for _, test := range invalidScopes {
		t.Run(test.name, func(t *testing.T) {
			plans, err := workflowManagedExplicitChildPlans(test.request, test.assignments)
			if err == nil || !strings.Contains(err.Error(), test.want) || plans != nil {
				t.Fatalf("explicit scope plans = %#v, %v, want %q", plans, err, test.want)
			}
		})
	}

	t.Run("unavailable files skip only empty assignments", func(t *testing.T) {
		other := workflowManagedAssignmentTestFile("other.go", "def", 20)
		plans, err := workflowManagedExplicitChildPlans(
			workflows.AgentRequest{Scope: []any{file}},
			[]workflowManagedExplicitAssignmentPlan{
				assignment,
				{
					assignmentID: "unavailable",
					focusID:      "security_trust",
					task:         "Trace trust.",
					files:        []map[string]any{other},
				},
			},
		)
		if err != nil || len(plans) != 1 || plans[0].assignmentID != "assignment" ||
			len(plans[0].scope) != 1 || plans[0].index != 1 {
			t.Fatalf("missing-only explicit plans = %#v, %v", plans, err)
		}
	})

	t.Run("snake case exact reference", func(t *testing.T) {
		snake := map[string]any{
			"path": "snake.go", "blob_sha": "123", "size_bytes": json.Number("30"),
		}
		pathValue, identity, ok := workflowManagedExactFileReference(snake)
		if !ok || pathValue != "snake.go" || identity == "" {
			t.Fatalf("snake exact reference = (%q, %q, %t)", pathValue, identity, ok)
		}
	})
}

func TestWorkflowManagedExactFileReferenceBoundaryCoverage(t *testing.T) {
	valid := workflowManagedAssignmentTestFile("file.go", "abc", 10)
	invalid := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing path", mutate: func(value map[string]any) { delete(value, "path") }},
		{name: "non-string path", mutate: func(value map[string]any) { value["path"] = 1 }},
		{name: "padded path", mutate: func(value map[string]any) { value["path"] = " file.go " }},
		{name: "nul path", mutate: func(value map[string]any) { value["path"] = "file\x00.go" }},
		{name: "missing hash", mutate: func(value map[string]any) { delete(value, "fileHash") }},
		{name: "non-string hash", mutate: func(value map[string]any) { value["fileHash"] = 1 }},
		{name: "missing size", mutate: func(value map[string]any) { delete(value, "sizeBytes") }},
		{name: "negative size", mutate: func(value map[string]any) { value["sizeBytes"] = -1 }},
		{name: "fractional size", mutate: func(value map[string]any) { value["sizeBytes"] = 1.5 }},
		{name: "invalid category", mutate: func(value map[string]any) { value["category"] = " code " }},
		{name: "invalid mode", mutate: func(value map[string]any) { value["mode"] = 100644 }},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			value := cloneAnyMap(valid)
			test.mutate(value)
			if pathValue, identity, ok := workflowManagedExactFileReference(value); ok ||
				pathValue != "" || identity != "" {
				t.Fatalf("invalid exact reference = (%q, %q, %t)", pathValue, identity, ok)
			}
		})
	}

	withoutOptional := map[string]any{
		"path": "minimal.go", "fileHash": "def", "sizeBytes": uint16(11),
	}
	if pathValue, identity, ok := workflowManagedExactFileReference(withoutOptional); !ok ||
		pathValue != "minimal.go" || identity == "" {
		t.Fatalf("minimal exact reference = (%q, %q, %t)", pathValue, identity, ok)
	}
}

//nolint:govet // Boundary assertions intentionally reuse err in short scopes.
func TestWorkflowManagedAssignmentJSONAndScalarHelperCoverage(t *testing.T) {
	original := map[string]any{"summary": "ok", "nested": []any{"one"}}
	detached, err := workflowManagedDetachedJSON(original)
	if err != nil || !reflect.DeepEqual(detached, original) {
		t.Fatalf("detached JSON = %#v, %v", detached, err)
	}
	detached.(map[string]any)["summary"] = "changed"
	if original["summary"] != "ok" {
		t.Fatalf("detached JSON mutated original = %#v", original)
	}
	if _, err := workflowManagedDetachedJSON(make(chan int)); err == nil {
		t.Fatal("non-JSON value detached without error")
	}

	dispatch := workflows.ManagedAssignmentDispatchEvent{
		AssignmentID: "assignment", FocusID: "correctness_state", ReviewerModel: "review-a",
		Model: "actual-a", Required: true, Scope: []any{map[string]any{"path": "a.go"}},
	}
	first, err := workflowManagedAssignmentCheckpointDigest(dispatch, original)
	if err != nil || !strings.HasPrefix(first, "sha256:") || len(first) != 71 {
		t.Fatalf("checkpoint digest = %q, %v", first, err)
	}
	second, err := workflowManagedAssignmentCheckpointDigest(dispatch, original)
	if err != nil || second != first {
		t.Fatalf("checkpoint digest replay = %q, %v, want %q", second, err, first)
	}
	changed := dispatch
	changed.Model = "actual-b"
	third, err := workflowManagedAssignmentCheckpointDigest(changed, original)
	if err != nil || third == first {
		t.Fatalf("checkpoint identity was not bound: first=%q third=%q err=%v", first, third, err)
	}
	if _, err := workflowManagedAssignmentCheckpointDigest(dispatch, make(chan int)); err == nil {
		t.Fatal("non-JSON checkpoint output produced a digest")
	}

	allowedScalars := []any{
		nil, true, "text", int(1), int8(1), int16(1), int32(1), int64(1),
		uint(1), uint8(1), uint16(1), uint32(1), uint64(1), float32(1), float64(1),
		json.Number("1"),
	}
	for _, value := range allowedScalars {
		if !workflowManagedScopeReferenceScalar(value) {
			t.Fatalf("scalar helper rejected %T", value)
		}
	}
	for _, value := range []any{[]any{}, map[string]any{}, struct{}{}, func() {}, make(chan int)} {
		if workflowManagedScopeReferenceScalar(value) {
			t.Fatalf("scalar helper accepted %T", value)
		}
	}

	references := workflowManagedScopeReferences([]any{
		map[string]any{
			"path": "a.go", "fileHash": "abc", "sizeBytes": int64(1),
			"selected": true, "content": "secret", "id": []string{"not scalar"},
		},
		"opaque",
	})
	firstReference := references[0].(map[string]any)
	if firstReference["path"] != "a.go" || firstReference["selected"] != true ||
		firstReference["content"] != nil || firstReference["id"] != nil {
		t.Fatalf("scope references = %#v", references)
	}
	if references[1].(map[string]any)["value_hash"] == "" {
		t.Fatalf("opaque scope reference = %#v", references[1])
	}
}

func TestWorkflowManagedExplicitAssignmentCallbackAndContextErrorsCoverage(t *testing.T) {
	file := workflowManagedAssignmentTestFile("callback.go", "abc", 10)
	baseRequest := workflows.AgentRequest{
		Managed: map[string]any{
			"combine_structured_outputs": false,
			"calibration":                map[string]any{"enabled": false},
			"assignment_plans": []any{map[string]any{
				"assignment_id": "assignment-callback", "focus_id": "correctness_state",
				"task": "Trace state.", "reviewer_model": "review-a", "optional": false,
				"files": []any{file},
			}},
		},
		Scope:                    []any{file},
		Output:                   workflowManagedTestOutputContract(),
		AssignmentTimeoutSeconds: 60,
	}
	runner := &workflowAgentRunner{}
	runOnce := func(_ string, _ bool, _ workflowAgentRunOptions) (string, error) {
		return `{"summary":"reviewed","findings":[]}`, nil
	}
	if _, err := runner.runManagedSplit(
		baseRequest, &AgentInstance{ID: "main", Model: "default"},
		"main", "", "none", "none", "", "assignment_split", runOnce,
		context.Background(), context.Background(),
	); err == nil || !strings.Contains(err.Error(), "multiple execution contexts") {
		t.Fatalf("multiple managed contexts error = %v", err)
	}

	callbackWithoutPlan := baseRequest
	callbackWithoutPlan.Managed = map[string]any{"strategy": "scope_split"}
	callbackWithoutPlan.ManagedAssignmentCheckpoint = func(workflows.ManagedAssignmentCheckpointEvent) error {
		return nil
	}
	if _, err := runner.runManagedSplit(
		callbackWithoutPlan, &AgentInstance{ID: "main", Model: "default"},
		"main", "", "none", "none", "", "scope_split", runOnce,
	); err == nil || !strings.Contains(err.Error(), "require explicit assignment plans") {
		t.Fatalf("unplanned callback error = %v", err)
	}

	invalidPlan := baseRequest
	invalidPlan.Managed = map[string]any{"assignment_plans": "bad"}
	if _, err := runner.runManagedSplit(
		invalidPlan, &AgentInstance{ID: "main", Model: "default"},
		"main", "", "none", "none", "", "assignment_split", runOnce,
	); err == nil || !strings.Contains(err.Error(), "must be an array") {
		t.Fatalf("invalid assignment plan error = %v", err)
	}

	baseAdmissionCalls := 0
	dispatchCalls := 0
	admissionFailure := baseRequest
	admissionFailure.CallAdmission = func() error {
		baseAdmissionCalls++
		if baseAdmissionCalls == 2 {
			return errors.New("base admission changed")
		}
		return nil
	}
	admissionFailure.ManagedAssignmentDispatch = func(workflows.ManagedAssignmentDispatchEvent) error {
		dispatchCalls++
		return nil
	}
	providerCalls := 0
	outputs, err := runner.runManagedSplit(
		admissionFailure, &AgentInstance{ID: "main", Model: "default"},
		"main", "", "none", "none", "", "assignment_split",
		func(_ string, _ bool, options workflowAgentRunOptions) (string, error) {
			if admissionErr := options.CallAdmission(); admissionErr != nil {
				return "", errors.Join(workflows.ErrAgentCallNotAdmitted, admissionErr)
			}
			providerCalls++
			return `{"summary":"reviewed","findings":[]}`, nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "base admission changed") ||
		baseAdmissionCalls != 2 || dispatchCalls != 0 || providerCalls != 0 {
		t.Fatalf("base admission result outputs=%#v base=%d dispatch=%d provider=%d err=%v",
			outputs, baseAdmissionCalls, dispatchCalls, providerCalls, err)
	}

	expiredCtx, cancel := context.WithCancel(context.Background())
	cancel()
	expired := baseRequest
	outputs, err = runner.runManagedSplit(
		expired, &AgentInstance{ID: "main", Model: "default"},
		"main", "", "none", "none", "", "assignment_split",
		func(_ string, _ bool, options workflowAgentRunOptions) (string, error) {
			return "", options.Context.Err()
		},
		expiredCtx,
	)
	if !errors.Is(err, context.Canceled) || outputs == nil {
		t.Fatalf("expired assignment outputs=%#v err=%v", outputs, err)
	}
}

func TestWorkflowManagedExactPlanStringCoverage(t *testing.T) {
	values := map[string]any{"second": "value"}
	if value, ok := workflowManagedExactPlanString(values, "first", "second"); !ok || value != "value" {
		t.Fatalf("exact alias string = (%q, %t)", value, ok)
	}
	for name, raw := range map[string]any{
		"nil": nil, "non-string": 1, "padded": " value ", "nul": "value\x00x",
	} {
		t.Run(name, func(t *testing.T) {
			if value, ok := workflowManagedExactPlanString(map[string]any{"value": raw}, "value"); ok {
				t.Fatalf("invalid exact string = (%q, %t)", value, ok)
			}
		})
	}
	if value, ok := workflowManagedExactPlanString(map[string]any{}, "missing"); !ok || value != "" {
		t.Fatalf("missing optional string = (%q, %t)", value, ok)
	}
	if !workflowManagedBoundedPlanText("x", 1) || workflowManagedBoundedPlanText("", 1) ||
		workflowManagedBoundedPlanText("xx", 1) || workflowManagedBoundedPlanText(" x", 2) ||
		workflowManagedBoundedPlanText("x\x00", 2) {
		t.Fatal("bounded assignment text classification mismatch")
	}
}

func TestWorkflowStructuredAgentOutputsIncludesSchemaError(t *testing.T) {
	outputs := workflowStructuredAgentOutputs(
		"raw", workflows.StructuredOutputResult{Error: "schema mismatch"}, 0,
		"main", "", "none", "none", "", "message", "none",
	)
	if outputs["structured_error"] != "schema mismatch" {
		t.Fatalf("structured error output = %#v", outputs)
	}
}
