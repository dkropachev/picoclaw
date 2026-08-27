package tools

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

type p012PolicyFunc func(context.Context, ToolPolicyRequest) (ToolPolicyDecision, error)

func (fn p012PolicyFunc) EvaluateTool(
	ctx context.Context,
	request ToolPolicyRequest,
) (ToolPolicyDecision, error) {
	return fn(ctx, request)
}

func p012ValidPolicyRequest() ToolPolicyRequest {
	return ToolPolicyRequest{
		Subject: ToolPolicySubject{
			AgentID:    "main",
			SessionKey: "agent:main:coverage",
			TurnID:     "turn-coverage",
			ToolCallID: "call-coverage",
			Source:     ToolPolicySourceAgentPipeline,
		},
		Tool:        "read_file",
		Arguments:   map[string]any{"path": "README.md"},
		Traits:      ToolTraits{Risk: ToolRiskReadOnly},
		Fulfillment: ToolFulfillmentExecute,
	}
}

func p012ToolDefinition(name string, parameters map[string]any) providers.ToolDefinition {
	return providers.ToolDefinition{
		Type: "function",
		Function: providers.ToolFunctionDefinition{
			Name: name, Description: "coverage tool", Parameters: parameters,
		},
		PromptLayer: "capability",
		PromptSlot:  "tooling",
	}
}

func TestP012PolicyCoverageDetachOfferedToolDefinitions(t *testing.T) {
	empty, err := DetachOfferedToolDefinitions(nil)
	if err != nil || empty != nil {
		t.Fatalf("DetachOfferedToolDefinitions(nil) = %#v, %v", empty, err)
	}

	originalNested := map[string]any{"type": "string"}
	originalAlternatives := []any{
		map[string]any{"type": "integer"},
		[2]string{"null", "boolean"},
	}
	definitions := []providers.ToolDefinition{
		p012ToolDefinition("read_file", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": originalNested,
				"mode": map[string]any{"anyOf": originalAlternatives},
			},
		}),
		p012ToolDefinition("list_files", nil),
	}
	detached, err := DetachOfferedToolDefinitions(definitions)
	if err != nil {
		t.Fatalf("DetachOfferedToolDefinitions() error = %v", err)
	}
	if !reflect.DeepEqual(detached, definitions) {
		t.Fatalf("detached definitions differ\n got: %#v\nwant: %#v", detached, definitions)
	}
	if detached[0].PromptLayer != definitions[0].PromptLayer ||
		detached[0].PromptSlot != definitions[0].PromptSlot {
		t.Fatalf("definition metadata was not retained: %#v", detached[0])
	}

	detachedProperties := detached[0].Function.Parameters["properties"].(map[string]any)
	detachedProperties["path"].(map[string]any)["type"] = "number"
	detachedAlternatives := detachedProperties["mode"].(map[string]any)["anyOf"].([]any)
	detachedAlternatives[0].(map[string]any)["type"] = "boolean"
	if originalNested["type"] != "string" ||
		originalAlternatives[0].(map[string]any)["type"] != "integer" {
		t.Fatalf("detached schema aliases input: %#v / %#v", originalNested, originalAlternatives)
	}

	invalid := []providers.ToolDefinition{
		p012ToolDefinition("bad_schema", map[string]any{"value": make(chan int)}),
	}
	if got, err := DetachOfferedToolDefinitions(invalid); err == nil || got != nil {
		t.Fatalf("DetachOfferedToolDefinitions(invalid) = %#v, %v", got, err)
	}
}

func TestP012PolicyCoverageOfferedDefinitionValidation(t *testing.T) {
	cyclicSchema := map[string]any{}
	cyclicSchema["self"] = cyclicSchema

	tests := []struct {
		name        string
		definitions []providers.ToolDefinition
		want        string
	}{
		{name: "empty name", definitions: []providers.ToolDefinition{p012ToolDefinition("", nil)}, want: "non-empty"},
		{
			name:        "inexact name",
			definitions: []providers.ToolDefinition{p012ToolDefinition(" read_file", nil)},
			want:        "exact",
		},
		{
			name:        "control in name",
			definitions: []providers.ToolDefinition{p012ToolDefinition("read\x7ffile", nil)},
			want:        "control",
		},
		{
			name: "long name",
			definitions: []providers.ToolDefinition{
				p012ToolDefinition(strings.Repeat("x", MaxToolPolicyNameLen+1), nil),
			},
			want: "maximum length",
		},
		{
			name: "duplicate name",
			definitions: []providers.ToolDefinition{
				p012ToolDefinition("read_file", nil), p012ToolDefinition("read_file", nil),
			},
			want: "duplicates offered tool",
		},
		{
			name: "non-string schema keys",
			definitions: []providers.ToolDefinition{
				p012ToolDefinition("read_file", map[string]any{"bad": map[int]string{1: "one"}}),
			},
			want: "string keys",
		},
		{
			name: "cyclic schema",
			definitions: []providers.ToolDefinition{
				p012ToolDefinition("read_file", cyclicSchema),
			},
			want: "cycle",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateOfferedToolDefinitions(test.definitions)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateOfferedToolDefinitions() error = %v, want %q", err, test.want)
			}
		})
	}

	if err := ValidateOfferedToolDefinitions([]providers.ToolDefinition{
		p012ToolDefinition("read_file", map[string]any{"type": "object"}),
		p012ToolDefinition("list_files", nil),
	}); err != nil {
		t.Fatalf("ValidateOfferedToolDefinitions(valid) error = %v", err)
	}
}

func TestP012PolicyCoverageModelToolCallValidation(t *testing.T) {
	definitions := []providers.ToolDefinition{
		p012ToolDefinition("read_file", map[string]any{"type": "object"}),
		p012ToolDefinition("list_files", nil),
	}
	validCalls := []providers.ToolCall{
		{ID: "call-1", Name: "read_file"},
		{ID: "call-2", Name: "list_files"},
	}
	if err := ValidateModelToolCallBatch(validCalls, definitions); err != nil {
		t.Fatalf("ValidateModelToolCallBatch(valid) error = %v", err)
	}

	batchTests := []struct {
		name        string
		calls       []providers.ToolCall
		definitions []providers.ToolDefinition
		want        string
	}{
		{
			name: "invalid offered set", calls: validCalls,
			definitions: []providers.ToolDefinition{p012ToolDefinition("", nil)},
			want:        "non-empty",
		},
		{
			name: "invalid call identity", calls: []providers.ToolCall{{Name: "read_file"}},
			definitions: definitions, want: "tool call ID",
		},
		{
			name: "unoffered call", calls: []providers.ToolCall{{ID: "call-3", Name: "write_file"}},
			definitions: definitions, want: "unoffered tool",
		},
	}
	for _, test := range batchTests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateModelToolCallBatch(test.calls, test.definitions)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateModelToolCallBatch() error = %v, want %q", err, test.want)
			}
		})
	}

	identityTests := []struct {
		name  string
		calls []providers.ToolCall
		want  string
	}{
		{name: "empty ID", calls: []providers.ToolCall{{Name: "read_file"}}, want: "non-empty"},
		{name: "inexact ID", calls: []providers.ToolCall{{ID: " call-1", Name: "read_file"}}, want: "exact"},
		{name: "control ID", calls: []providers.ToolCall{{ID: "call\n1", Name: "read_file"}}, want: "control"},
		{
			name: "long ID", calls: []providers.ToolCall{{
				ID: strings.Repeat("c", MaxToolPolicyCallIDLen+1), Name: "read_file",
			}}, want: "maximum length",
		},
		{
			name: "duplicate ID",
			calls: []providers.ToolCall{
				{ID: "call-1", Name: "read_file"}, {ID: "call-1", Name: "list_files"},
			},
			want: "duplicates ID",
		},
		{name: "empty name", calls: []providers.ToolCall{{ID: "call-1"}}, want: "non-empty"},
		{name: "inexact name", calls: []providers.ToolCall{{ID: "call-1", Name: "read_file "}}, want: "exact"},
		{name: "control name", calls: []providers.ToolCall{{ID: "call-1", Name: "read\nfile"}}, want: "control"},
		{
			name: "long name", calls: []providers.ToolCall{{
				ID: "call-1", Name: strings.Repeat("t", MaxToolPolicyNameLen+1),
			}}, want: "maximum length",
		},
	}
	for _, test := range identityTests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateModelToolCallIdentity(test.calls)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateModelToolCallIdentity() error = %v, want %q", err, test.want)
			}
		})
	}
	if err := ValidateModelToolCallIdentity(nil); err != nil {
		t.Fatalf("ValidateModelToolCallIdentity(nil) error = %v", err)
	}
}

func TestP012PolicyCoverageRequestMetadataValidation(t *testing.T) {
	tooLong := func(limit int) string { return strings.Repeat("x", limit+1) }
	tests := []struct {
		name   string
		mutate func(*ToolPolicyRequest)
		want   string
	}{
		{name: "empty tool", mutate: func(r *ToolPolicyRequest) { r.Tool = "" }, want: "non-empty"},
		{name: "inexact agent ID", mutate: func(r *ToolPolicyRequest) { r.Subject.AgentID = " main" }, want: "exact"},
		{
			name:   "long agent ID",
			mutate: func(r *ToolPolicyRequest) { r.Subject.AgentID = tooLong(maxToolPolicyAgentIDLen) },
			want:   "maximum length",
		},
		{
			name:   "control agent ID",
			mutate: func(r *ToolPolicyRequest) { r.Subject.AgentID = "main\x00" },
			want:   "control",
		},
		{name: "inexact session", mutate: func(r *ToolPolicyRequest) { r.Subject.SessionKey += " " }, want: "exact"},
		{
			name:   "long session",
			mutate: func(r *ToolPolicyRequest) { r.Subject.SessionKey = tooLong(maxToolPolicySessionKeyLen) },
			want:   "maximum length",
		},
		{
			name:   "control session",
			mutate: func(r *ToolPolicyRequest) { r.Subject.SessionKey = "agent\nsession" },
			want:   "control",
		},
		{name: "inexact turn", mutate: func(r *ToolPolicyRequest) { r.Subject.TurnID = " turn" }, want: "exact"},
		{
			name:   "long turn",
			mutate: func(r *ToolPolicyRequest) { r.Subject.TurnID = tooLong(maxToolPolicyTurnIDLen) },
			want:   "maximum length",
		},
		{name: "control turn", mutate: func(r *ToolPolicyRequest) { r.Subject.TurnID = "turn\x7f" }, want: "control"},
		{name: "inexact call ID", mutate: func(r *ToolPolicyRequest) { r.Subject.ToolCallID += " " }, want: "exact"},
		{
			name:   "control call ID",
			mutate: func(r *ToolPolicyRequest) { r.Subject.ToolCallID = "call\t1" },
			want:   "control",
		},
		{
			name: "inexact hook name",
			mutate: func(r *ToolPolicyRequest) {
				r.Hook = ToolHookProvenance{Name: " hook", Source: "process", Trusted: true}
			},
			want: "exact",
		},
		{
			name: "control hook name",
			mutate: func(r *ToolPolicyRequest) {
				r.Hook = ToolHookProvenance{Name: "ho\nok", Source: "process", Trusted: true}
			},
			want: "control",
		},
		{
			name: "inexact hook source",
			mutate: func(r *ToolPolicyRequest) {
				r.Hook = ToolHookProvenance{Name: "hook", Source: " process", Trusted: true}
			},
			want: "exact",
		},
		{
			name: "long hook source",
			mutate: func(r *ToolPolicyRequest) {
				r.Hook = ToolHookProvenance{Name: "hook", Source: tooLong(maxToolPolicyAgentIDLen), Trusted: true}
			},
			want: "maximum length",
		},
		{
			name: "control hook source",
			mutate: func(r *ToolPolicyRequest) {
				r.Hook = ToolHookProvenance{Name: "hook", Source: "process\x00", Trusted: true}
			},
			want: "control",
		},
		{
			name: "untrusted hook fields",
			mutate: func(r *ToolPolicyRequest) {
				r.Hook = ToolHookProvenance{Name: "hook", Source: "process"}
			},
			want: "untrusted hook provenance",
		},
		{
			name: "trusted hook missing name",
			mutate: func(r *ToolPolicyRequest) {
				r.Hook = ToolHookProvenance{Source: "process", Trusted: true}
			},
			want: "complete",
		},
		{
			name:   "unsupported source",
			mutate: func(r *ToolPolicyRequest) { r.Subject.Source = "unknown" },
			want:   "unsupported policy source",
		},
		{
			name:   "unsupported fulfillment",
			mutate: func(r *ToolPolicyRequest) { r.Fulfillment = "unknown" },
			want:   "unsupported fulfillment",
		},
		{name: "invalid risk", mutate: func(r *ToolPolicyRequest) { r.Traits.Risk = "root" }, want: "normalize traits"},
		{
			name:   "invalid parallelism",
			mutate: func(r *ToolPolicyRequest) { r.Traits.Parallel = "sometimes" },
			want:   "normalize traits",
		},
		{
			name:   "invalid idempotency",
			mutate: func(r *ToolPolicyRequest) { r.Traits.Idempotency = "maybe" },
			want:   "normalize traits",
		},
		{
			name:   "invalid sharing",
			mutate: func(r *ToolPolicyRequest) { r.Traits.Sharing = "global" },
			want:   "normalize traits",
		},
		{
			name:   "invalid arguments",
			mutate: func(r *ToolPolicyRequest) { r.Arguments = map[string]any{"pointer": new(int)} },
			want:   "unsupported pointer",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := p012ValidPolicyRequest()
			test.mutate(&request)
			_, err := detachToolPolicyRequest(request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("detachToolPolicyRequest() error = %v, want %q", err, test.want)
			}
		})
	}

	for _, source := range []ToolPolicySource{
		ToolPolicySourceAgentPipeline,
		ToolPolicySourceGenericLoop,
		ToolPolicySourceLocalRepair,
		ToolPolicySourceLegacySubagent,
	} {
		for _, fulfillment := range []ToolFulfillmentKind{
			ToolFulfillmentExecute,
			ToolFulfillmentHookRespond,
		} {
			request := p012ValidPolicyRequest()
			request.Subject.Source = source
			request.Fulfillment = fulfillment
			request.Hook = ToolHookProvenance{Name: "admin-hook", Source: "process", Trusted: true}
			detached, err := detachToolPolicyRequest(request)
			if err != nil {
				t.Fatalf("detachToolPolicyRequest(%q, %q) error = %v", source, fulfillment, err)
			}
			if detached.Hook != request.Hook || detached.Subject != request.Subject {
				t.Fatalf("detached metadata = %#v, want %#v", detached, request)
			}
		}
	}
}

func TestP012PolicyCoverageEvaluationCancellationAndDecisionValidation(t *testing.T) {
	if _, err := EvaluateToolPolicy(
		nil,
		CompatibilityAllowToolPolicy{},
		p012ValidPolicyRequest(),
	); !errors.Is(
		err,
		ErrToolPolicyUnavailable,
	) {
		t.Fatalf("EvaluateToolPolicy(nil context) error = %v", err)
	}

	ctxWithError, cancelWithError := context.WithCancel(context.Background())
	_, err := EvaluateToolPolicy(
		ctxWithError,
		p012PolicyFunc(func(context.Context, ToolPolicyRequest) (ToolPolicyDecision, error) {
			cancelWithError()
			return ToolPolicyDecision{}, errors.New("broker stopped")
		}),
		p012ValidPolicyRequest(),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("EvaluateToolPolicy(cancel plus error) error = %v", err)
	}

	ctxAfterDecision, cancelAfterDecision := context.WithCancel(context.Background())
	_, err = EvaluateToolPolicy(
		ctxAfterDecision,
		p012PolicyFunc(func(context.Context, ToolPolicyRequest) (ToolPolicyDecision, error) {
			cancelAfterDecision()
			return ToolPolicyDecision{Kind: ToolPolicyDecisionAllow, ReasonCode: "too_late"}, nil
		}),
		p012ValidPolicyRequest(),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("EvaluateToolPolicy(cancel after decision) error = %v", err)
	}

	decisions := []struct {
		name     string
		decision ToolPolicyDecision
		wantErr  bool
	}{
		{name: "allow", decision: ToolPolicyDecision{Kind: ToolPolicyDecisionAllow, ReasonCode: "read.allow"}},
		{name: "deny", decision: ToolPolicyDecision{Kind: ToolPolicyDecisionDeny, ReasonCode: "risk-deny_1"}},
		{name: "unsupported kind", decision: ToolPolicyDecision{Kind: "abstain", ReasonCode: "abstain"}, wantErr: true},
		{name: "empty reason", decision: ToolPolicyDecision{Kind: ToolPolicyDecisionDeny}, wantErr: true},
		{
			name: "long reason",
			decision: ToolPolicyDecision{
				Kind:       ToolPolicyDecisionDeny,
				ReasonCode: strings.Repeat("a", maxToolPolicyReasonCodeLen+1),
			},
			wantErr: true,
		},
		{
			name:     "uppercase reason",
			decision: ToolPolicyDecision{Kind: ToolPolicyDecisionDeny, ReasonCode: "Not_Allowed"},
			wantErr:  true,
		},
	}
	for _, test := range decisions {
		t.Run(test.name, func(t *testing.T) {
			err := validateToolPolicyDecision(test.decision)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateToolPolicyDecision(%#v) error = %v", test.decision, err)
			}
		})
	}
}

func TestP012PolicyCoverageDetachToolArgumentsPreservesJSONLikeTypes(t *testing.T) {
	type namedMap map[string]int16
	type namedSlice []uint32
	type namedArray [2]string

	var nilMap map[string]int
	var nilSlice []string
	var nilPointer *int
	arguments := map[string]any{
		"nil_interface": nil,
		"nil_map":       nilMap,
		"nil_slice":     nilSlice,
		"nil_pointer":   nilPointer,
		"bool":          true,
		"string":        "text",
		"int":           int(-1),
		"int8":          int8(-8),
		"int16":         int16(-16),
		"int32":         int32(-32),
		"int64":         int64(-64),
		"uint":          uint(1),
		"uint8":         uint8(8),
		"uint16":        uint16(16),
		"uint32":        uint32(32),
		"uint64":        uint64(64),
		"float32":       float32(1.25),
		"float64":       float64(-2.5),
		"json_number":   json.Number("-12.5e+2"),
		"map":           map[string]any{"nested": "original"},
		"named_map":     namedMap{"count": 2},
		"slice":         []any{map[string]any{"nested": "original"}},
		"named_slice":   namedSlice{3, 4},
		"array":         [2]int{5, 6},
		"named_array":   namedArray{"a", "b"},
	}

	detached, err := DetachToolArguments(arguments)
	if err != nil {
		t.Fatalf("DetachToolArguments() error = %v", err)
	}
	if !reflect.DeepEqual(detached, arguments) {
		t.Fatalf("detached arguments differ\n got: %#v\nwant: %#v", detached, arguments)
	}
	if _, ok := detached["named_map"].(namedMap); !ok {
		t.Fatalf("named map type was not preserved: %T", detached["named_map"])
	}
	if _, ok := detached["named_slice"].(namedSlice); !ok {
		t.Fatalf("named slice type was not preserved: %T", detached["named_slice"])
	}
	if _, ok := detached["named_array"].(namedArray); !ok {
		t.Fatalf("named array type was not preserved: %T", detached["named_array"])
	}

	detached["map"].(map[string]any)["nested"] = "changed"
	detached["slice"].([]any)[0].(map[string]any)["nested"] = "changed"
	detached["named_map"].(namedMap)["count"] = 99
	detached["named_slice"].(namedSlice)[0] = 99
	if arguments["map"].(map[string]any)["nested"] != "original" ||
		arguments["slice"].([]any)[0].(map[string]any)["nested"] != "original" ||
		arguments["named_map"].(namedMap)["count"] != 2 ||
		arguments["named_slice"].(namedSlice)[0] != 3 {
		t.Fatalf("detached arguments alias input: %#v", arguments)
	}
}

func TestP012PolicyCoverageArgumentCloneFailuresAndBudgets(t *testing.T) {
	newState := func() toolArgumentCloneState {
		return toolArgumentCloneState{active: make(map[toolArgumentVisit]struct{})}
	}
	cloneError := func(t *testing.T, state toolArgumentCloneState, value any, depth int, want string) {
		t.Helper()
		_, err := state.clone(reflect.ValueOf(value), depth)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("clone(%T) error = %v, want %q", value, err, want)
		}
	}

	state := newState()
	invalid, err := state.clone(reflect.Value{}, 0)
	if err != nil || invalid.IsValid() {
		t.Fatalf("clone(invalid) = %#v, %v", invalid, err)
	}
	cloneError(t, newState(), true, maxToolPolicyArgumentDepth+1, "maximum depth")
	nodeState := newState()
	nodeState.nodes = maxToolPolicyArgumentNodes
	cloneError(t, nodeState, true, 0, "maximum nodes")

	negativeBytes := newState()
	if err := negativeBytes.addBytes(-1); err == nil {
		t.Fatal("addBytes(-1) error = nil")
	}
	for _, test := range []struct {
		name  string
		value any
	}{
		{name: "JSON number", value: json.Number("1")},
		{name: "boolean", value: true},
		{name: "string", value: "x"},
		{name: "integer", value: int64(1)},
		{name: "float", value: float64(1)},
	} {
		t.Run(test.name+" byte budget", func(t *testing.T) {
			byteState := newState()
			byteState.bytes = maxToolPolicyArgumentBytes
			cloneError(t, byteState, test.value, 0, "maximum bytes")
		})
	}
	mapByteState := newState()
	mapByteState.bytes = maxToolPolicyArgumentBytes
	cloneError(t, mapByteState, map[string]int{"key": 1}, 0, "maximum bytes")

	for _, number := range []json.Number{"", "01", "+1", ".5", "1.", "NaN"} {
		t.Run("invalid JSON number "+string(number), func(t *testing.T) {
			cloneError(t, newState(), number, 0, "invalid JSON number")
		})
	}
	cloneError(t, newState(), math.NaN(), 0, "non-finite")
	cloneError(t, newState(), math.Inf(-1), 0, "non-finite")
	cloneError(t, newState(), map[int]string{1: "one"}, 0, "string keys")

	mapCycle := map[string]any{}
	mapCycle["self"] = mapCycle
	cloneError(t, newState(), mapCycle, 0, "cycle")
	sliceCycle := make([]any, 1)
	sliceCycle[0] = sliceCycle
	cloneError(t, newState(), sliceCycle, 0, "cycle")

	for _, test := range []struct {
		name  string
		value any
	}{
		{name: "map", value: map[string]int{"one": 1}},
		{name: "slice", value: []int{1}},
		{name: "array", value: [1]int{1}},
	} {
		t.Run(test.name+" node preflight", func(t *testing.T) {
			preflightState := newState()
			preflightState.nodes = maxToolPolicyArgumentNodes - 1
			cloneError(t, preflightState, test.value, 0, "maximum nodes")
		})
	}

	value := 7
	cloneError(t, newState(), map[string]*int{"value": &value}, 0, "unsupported pointer")
	cloneError(t, newState(), []*int{&value}, 0, "unsupported pointer")
	cloneError(t, newState(), [1]*int{&value}, 0, "unsupported pointer")
	cloneError(t, newState(), &value, 0, "unsupported pointer")
	cloneError(t, newState(), make(chan int), 0, "unsupported chan")
	cloneError(t, newState(), complex(1, 2), 0, "unsupported complex")

	var nilMap map[string]int
	var nilSlice []int
	var nilPointer *int
	for _, value := range []any{nilMap, nilSlice, nilPointer} {
		state := newState()
		cloned, err := state.clone(reflect.ValueOf(value), 0)
		if err != nil || !cloned.IsNil() || cloned.Type() != reflect.TypeOf(value) {
			t.Fatalf("clone(typed nil %T) = %#v, %v", value, cloned, err)
		}
	}
}
