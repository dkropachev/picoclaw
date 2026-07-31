package workflows

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestNativeFunctionNamesReturnsSortedCopy(t *testing.T) {
	first := NativeFunctionNames()
	want := []string{
		"git.diff",
		"git.filter",
		"git.inventory",
		"workflow.artifact",
		"workflow.state",
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("NativeFunctionNames() = %#v, want %#v", first, want)
	}
	first[0] = "mutated"
	if got := NativeFunctionNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("NativeFunctionNames() aliased caller mutation: %#v", got)
	}
}

func TestWorkflowAuthoringShapeSanitizerProjectsOnlyWhitelist(t *testing.T) {
	sanitizer := &WorkflowAuthoringShapeSanitizer{}
	shape, ok := sanitizer.Project(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"zeta": map[string]any{
				"type":        "array",
				"description": "must not be projected",
				"items": map[string]any{
					"type": "integer",
					"enum": []any{1, 2, nil},
				},
			},
			"alpha": map[string]any{
				"type":    "string",
				"default": "secret-default",
			},
		},
		"required": []any{"zeta"},
		"additionalProperties": map[string]any{
			"type": "boolean",
		},
		"description": "secret description",
		"examples":    []any{"secret example"},
	})
	if !ok || shape == nil {
		t.Fatal("Project() rejected safe schema")
	}
	if len(shape.Properties) != 2 ||
		shape.Properties[0].Name != "alpha" ||
		shape.Properties[0].Required ||
		shape.Properties[1].Name != "zeta" ||
		!shape.Properties[1].Required {
		t.Fatalf("properties = %#v, want sorted properties with folded required", shape.Properties)
	}
	if shape.Properties[1].Shape.Items == nil ||
		len(shape.Properties[1].Shape.Items.Enum) != 3 {
		t.Fatalf("nested items = %#v", shape.Properties[1].Shape.Items)
	}
	if shape.AdditionalProperties == nil ||
		shape.AdditionalProperties.Allowed != nil ||
		shape.AdditionalProperties.Shape == nil ||
		shape.AdditionalProperties.Shape.Type != "boolean" {
		t.Fatalf("additional_properties = %#v", shape.AdditionalProperties)
	}

	encoded, err := json.Marshal(shape)
	if err != nil {
		t.Fatalf("json.Marshal(shape): %v", err)
	}
	for _, forbidden := range []string{
		"description",
		"default",
		"secret-default",
		"examples",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("projected schema leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestWorkflowAuthoringShapeSanitizerBooleanAdditionalProperties(t *testing.T) {
	sanitizer := &WorkflowAuthoringShapeSanitizer{}
	shape, ok := sanitizer.Project(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
	})
	if !ok || shape == nil ||
		shape.AdditionalProperties == nil ||
		shape.AdditionalProperties.Allowed == nil ||
		*shape.AdditionalProperties.Allowed ||
		shape.AdditionalProperties.Shape != nil {
		t.Fatalf("additional_properties = %#v, want {allowed:false}", shape)
	}
}

func TestWorkflowAuthoringShapeSanitizerRejectsUnsafeDeclarations(t *testing.T) {
	tests := []struct {
		name   string
		schema map[string]any
	}{
		{
			name: "required absent from properties",
			schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
				"required":   []any{"missing"},
			},
		},
		{
			name: "format control property",
			schema: map[string]any{
				"properties": map[string]any{"safe\u202Ehidden": map[string]any{}},
			},
		},
		{
			name: "control enum",
			schema: map[string]any{
				"enum": []any{"unsafe\nvalue"},
			},
		},
		{
			name: "unsafe integer",
			schema: map[string]any{
				"enum": []any{int64(9007199254740992)},
			},
		},
		{
			name: "duplicate enum scalars",
			schema: map[string]any{
				"enum": []any{json.Number("1.0"), int64(1)},
			},
		},
		{
			name: "uint64 max",
			schema: map[string]any{
				"enum": []any{^uint64(0)},
			},
		},
		{
			name: "overbound numeric spelling",
			schema: map[string]any{
				"enum": []any{json.Number(strings.Repeat("9", 1<<20))},
			},
		},
		{
			name: "huge positive numeric exponent",
			schema: map[string]any{
				"enum": []any{json.Number("1e999999999")},
			},
		},
		{
			name: "huge negative numeric exponent",
			schema: map[string]any{
				"enum": []any{json.Number("1e-999999999")},
			},
		},
		{
			name: "unsupported composition",
			schema: map[string]any{
				"type": []any{"string", "null"},
			},
		},
		{
			name: "ref only",
			schema: map[string]any{
				"$ref": "file:///private/schema.json",
			},
		},
		{
			name: "composition only",
			schema: map[string]any{
				"oneOf": []any{
					map[string]any{"type": "string"},
					map[string]any{"type": "number"},
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sanitizer := &WorkflowAuthoringShapeSanitizer{}
			if shape, ok := sanitizer.Project(test.schema); ok || shape != nil {
				t.Fatalf("Project() = %#v, %t; want rejected", shape, ok)
			}
			if sanitizer.units != 0 {
				t.Fatalf("failed projection consumed response units: %d", sanitizer.units)
			}
			if sanitizer.work == 0 {
				t.Fatal("failed projection did not consume work budget")
			}
		})
	}
}

func TestWorkflowAuthoringShapeSanitizerNumericBoundary(t *testing.T) {
	sanitizer := &WorkflowAuthoringShapeSanitizer{}
	shape, ok := sanitizer.Project(map[string]any{
		"enum": []any{
			int64(9007199254740991),
			json.Number("0.1"),
			float64(1.25),
		},
	})
	if !ok || shape == nil {
		t.Fatal("Project() rejected browser-safe numbers")
	}
	encoded, err := json.Marshal(shape)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != `{"enum":[9007199254740991,0.1,1.25]}` {
		t.Fatalf("encoded shape = %s", encoded)
	}
}

func TestWorkflowAuthoringShapeSanitizerResponseRollbackStillConsumesWork(t *testing.T) {
	sanitizer := &WorkflowAuthoringShapeSanitizer{}
	bad := map[string]any{
		"properties": map[string]any{
			"safe": map[string]any{
				"required": []any{"missing"},
			},
		},
	}
	if shape, ok := sanitizer.Project(bad); ok || shape != nil {
		t.Fatal("bad shape unexpectedly projected")
	}
	firstWork := sanitizer.work
	if firstWork == 0 || sanitizer.units != 0 {
		t.Fatalf("budgets after failure: work=%d units=%d", firstWork, sanitizer.units)
	}
	if shape, ok := sanitizer.Project(bad); ok || shape != nil {
		t.Fatal("bad shape unexpectedly projected on retry")
	}
	if sanitizer.work <= firstWork || sanitizer.units != 0 {
		t.Fatalf("retry budgets: work=%d units=%d", sanitizer.work, sanitizer.units)
	}

	for sanitizer.work < MaxWorkflowAuthoringShapeUnits {
		_, _ = sanitizer.Project(bad)
	}
	if shape, ok := sanitizer.Project(map[string]any{}); ok || shape != nil {
		t.Fatal("safe shape projected after global work budget exhaustion")
	}
}

func TestWorkflowAuthoringShapeSanitizerDepthAndAggregateBounds(t *testing.T) {
	deep := map[string]any{}
	cursor := deep
	for index := 0; index < MaxWorkflowAuthoringShapeDepth; index++ {
		next := map[string]any{}
		cursor["items"] = next
		cursor = next
	}
	if shape, ok := (&WorkflowAuthoringShapeSanitizer{}).Project(deep); ok || shape != nil {
		t.Fatal("shape beyond maximum depth projected")
	}

	properties := make(map[string]any, MaxWorkflowAuthoringShapeProperties+1)
	for index := 0; index <= MaxWorkflowAuthoringShapeProperties; index++ {
		properties[string(rune('a'+index))] = map[string]any{}
	}
	if shape, ok := (&WorkflowAuthoringShapeSanitizer{}).Project(map[string]any{
		"properties": properties,
	}); ok || shape != nil {
		t.Fatal("overbound properties projected")
	}

	oversizedRequired := make([]string, MaxWorkflowAuthoringShapeRequired+1)
	for index := range oversizedRequired {
		oversizedRequired[index] = "field"
	}
	requiredSanitizer := &WorkflowAuthoringShapeSanitizer{}
	if shape, ok := requiredSanitizer.Project(map[string]any{
		"properties": map[string]any{"field": map[string]any{}},
		"required":   oversizedRequired,
	}); ok || shape != nil {
		t.Fatal("overbound required collection projected")
	}
	if requiredSanitizer.work != 1 || requiredSanitizer.units != 0 {
		t.Fatalf(
			"overbound required budgets = work %d, units %d; want 1, 0",
			requiredSanitizer.work,
			requiredSanitizer.units,
		)
	}

	oversizedEnum := make([]any, MaxWorkflowAuthoringShapeEnum+1)
	enumSanitizer := &WorkflowAuthoringShapeSanitizer{}
	if shape, ok := enumSanitizer.Project(map[string]any{
		"enum": oversizedEnum,
	}); ok || shape != nil {
		t.Fatal("overbound enum collection projected")
	}
	if enumSanitizer.work != 1 || enumSanitizer.units != 0 {
		t.Fatalf(
			"overbound enum budgets = work %d, units %d; want 1, 0",
			enumSanitizer.work,
			enumSanitizer.units,
		)
	}

	oversizedStringEnum := make([]string, MaxWorkflowAuthoringShapeEnum+1)
	stringEnumSanitizer := &WorkflowAuthoringShapeSanitizer{}
	if shape, ok := stringEnumSanitizer.Project(map[string]any{
		"enum": oversizedStringEnum,
	}); ok || shape != nil {
		t.Fatal("overbound string enum collection projected")
	}
	if stringEnumSanitizer.work != 1 || stringEnumSanitizer.units != 0 {
		t.Fatalf(
			"overbound string enum budgets = work %d, units %d; want 1, 0",
			stringEnumSanitizer.work,
			stringEnumSanitizer.units,
		)
	}
}

func TestWorkflowAuthoringIdentityAndLimitHelpers(t *testing.T) {
	if !SafeWorkflowAuthoringIdentity("GitHub Server") ||
		SafeWorkflowAuthoringIdentity(" leading") ||
		SafeWorkflowAuthoringIdentity("hidden\u2066value") ||
		SafeWorkflowAuthoringIdentity(strings.Repeat("x", MaxWorkflowAuthoringStringBytes+1)) {
		t.Fatal("identity safety contract mismatch")
	}
	for _, id := range []string{"main", "agent-1", "agent_1", "0"} {
		if !SafeWorkflowAuthoringAgentID(id) {
			t.Errorf("SafeWorkflowAuthoringAgentID(%q) = false, want true", id)
		}
	}
	for _, id := range []string{
		"Main",
		"foo/bar",
		"foo.bar",
		"foo bar",
		"á",
		strings.Repeat("a", 65),
	} {
		if SafeWorkflowAuthoringAgentID(id) {
			t.Errorf("SafeWorkflowAuthoringAgentID(%q) = true, want false", id)
		}
	}
	got := NormalizeWorkflowAuthoringLimits([]WorkflowAuthoringLimitCode{
		WorkflowAuthoringToolsTruncated,
		WorkflowAuthoringAgentsTruncated,
		WorkflowAuthoringToolsTruncated,
	})
	want := []WorkflowAuthoringLimitCode{
		WorkflowAuthoringAgentsTruncated,
		WorkflowAuthoringToolsTruncated,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeWorkflowAuthoringLimits() = %#v, want %#v", got, want)
	}
}

func TestDecodeWorkflowAuthoringCapabilitiesStrictContract(t *testing.T) {
	valid := testWorkflowAuthoringCatalog()
	raw, ok := MarshalWorkflowAuthoringCapabilities(valid)
	if !ok {
		t.Fatal("valid test catalog did not marshal")
	}
	decoded, err := DecodeWorkflowAuthoringCapabilities(raw)
	if err != nil {
		t.Fatalf("DecodeWorkflowAuthoringCapabilities() error = %v", err)
	}
	if !reflect.DeepEqual(decoded, valid) {
		t.Fatalf("decoded = %#v, want %#v", decoded, valid)
	}

	invalid := map[string][]byte{
		"unknown field": bytesReplaceOnce(
			raw,
			[]byte(`"limits":[]`),
			[]byte(`"private":"sentinel","limits":[]`),
		),
		"duplicate field": bytesReplaceOnce(
			raw,
			[]byte(`"complete":true`),
			[]byte(`"complete":true,"complete":true`),
		),
		"wrong case root field": bytesReplaceOnce(
			raw,
			[]byte(`"complete":true`),
			[]byte(`"Complete":true`),
		),
		"case colliding root fields": bytesReplaceOnce(
			raw,
			[]byte(`"complete":true`),
			[]byte(`"complete":true,"Complete":true`),
		),
		"wrong case nested field": bytesReplaceOnce(
			raw,
			[]byte(`"parameter_shape":{}`),
			[]byte(`"Parameter_Shape":{}`),
		),
		"missing root field": bytesReplaceOnce(
			raw,
			[]byte(`"complete":true,`),
			[]byte{},
		),
		"missing agent field": bytesReplaceOnce(
			raw,
			[]byte(`,"is_default":true`),
			[]byte{},
		),
		"missing tool fixed field": bytesReplaceOnce(
			raw,
			[]byte(`,"parameter_shape_projected":true`),
			[]byte{},
		),
		"missing property field": bytesReplaceOnce(
			raw,
			[]byte(`"parameter_shape":{}`),
			[]byte(
				`"parameter_shape":{"properties":[{"name":"value","shape":{}}]}`,
			),
		),
		"target mismatch": bytesReplaceOnce(
			raw,
			[]byte(`"target":"tool/alpha"`),
			[]byte(`"target":"tool/private"`),
		),
		"agent slash normalization": bytesReplaceOnce(
			raw,
			[]byte(`"id":"main","target":"agent/main"`),
			[]byte(`"id":"foo/bar","target":"agent/foo/bar"`),
		),
		"agent uppercase normalization": bytesReplaceOnce(
			raw,
			[]byte(`"id":"main","target":"agent/main"`),
			[]byte(`"id":"Main","target":"agent/Main"`),
		),
		"agent unicode normalization": bytesReplaceOnce(
			raw,
			[]byte(`"id":"main","target":"agent/main"`),
			[]byte(`"id":"á","target":"agent/á"`),
		),
		"agent over runtime bound": bytesReplaceOnce(
			raw,
			[]byte(`"id":"main","target":"agent/main"`),
			[]byte(
				`"id":"`+strings.Repeat("a", 65)+
					`","target":"agent/`+strings.Repeat("a", 65)+`"`,
			),
		),
		"unsorted tools": bytesReplaceOnce(
			raw,
			[]byte(
				`"tools":[{"name":"alpha","target":"tool/alpha","readiness":"ready",`+
					`"parameter_shape_projected":true,"parameter_shape":{}}]`,
			),
			[]byte(
				`"tools":[{"name":"zeta","target":"tool/zeta","readiness":"ready",`+
					`"parameter_shape_projected":true,"parameter_shape":{}},`+
					`{"name":"alpha","target":"tool/alpha","readiness":"ready",`+
					`"parameter_shape_projected":true,"parameter_shape":{}}]`,
			),
		),
		"inconsistent complete": bytesReplaceOnce(
			raw,
			[]byte(`"complete":true`),
			[]byte(`"complete":false`),
		),
		"unknown limit": bytesReplaceOnce(
			raw,
			[]byte(`"limits":[]`),
			[]byte(`"limits":["private_limit"]`),
		),
		"unsafe identity": bytesReplaceOnce(
			raw,
			[]byte(`"name":"alpha"`),
			[]byte(`"name":"alpha\u202ehidden"`),
		),
		"explicit null shape": bytesReplaceOnce(
			raw,
			[]byte(`"parameter_shape":{}`),
			[]byte(`"parameter_shape":null`),
		),
		"explicit null properties": bytesReplaceOnce(
			raw,
			[]byte(`"parameter_shape":{}`),
			[]byte(`"parameter_shape":{"properties":null}`),
		),
		"explicit null enum": bytesReplaceOnce(
			raw,
			[]byte(`"parameter_shape":{}`),
			[]byte(`"parameter_shape":{"enum":null}`),
		),
		"missing native function": bytesReplaceOnce(
			raw,
			[]byte(`,{"name":"workflow.state","target":"function/workflow.state","readiness":"ready"}`),
			[]byte{},
		),
	}
	for name, candidate := range invalid {
		t.Run(name, func(t *testing.T) {
			if decoded, err := DecodeWorkflowAuthoringCapabilities(candidate); err == nil {
				t.Fatalf("decoded invalid catalog: %#v", decoded)
			}
		})
	}
}

func TestDecodeWorkflowAuthoringCapabilitiesRejectsConditionalShapePairs(t *testing.T) {
	toolRaw, ok := MarshalWorkflowAuthoringCapabilities(testWorkflowAuthoringCatalog())
	if !ok {
		t.Fatal("valid tool catalog did not marshal")
	}

	mcpCatalog := testWorkflowAuthoringCatalog()
	mcpCatalog.MCPStatus = WorkflowAuthoringMCPReady
	mcpCatalog.MCPTools = []WorkflowAuthoringMCPToolCapability{{
		Server:                  "github",
		Tool:                    "search",
		Target:                  "mcp/github/search",
		Readiness:               WorkflowDependencyReadinessReady,
		ParameterShapeProjected: true,
		ParameterShape:          &WorkflowAuthoringParameterShape{},
	}}
	mcpRaw, ok := MarshalWorkflowAuthoringCapabilities(mcpCatalog)
	if !ok {
		t.Fatal("valid MCP catalog did not marshal")
	}

	tests := []struct {
		name string
		raw  []byte
	}{
		{
			name: "tool projected true without shape",
			raw: bytesReplaceOnce(
				toolRaw,
				[]byte(`"parameter_shape_projected":true,"parameter_shape":{}`),
				[]byte(`"parameter_shape_projected":true`),
			),
		},
		{
			name: "tool projected false with shape",
			raw: bytesReplaceOnce(
				toolRaw,
				[]byte(`"parameter_shape_projected":true,"parameter_shape":{}`),
				[]byte(`"parameter_shape_projected":false,"parameter_shape":{}`),
			),
		},
		{
			name: "MCP projected true without shape",
			raw: bytesReplaceOnce(
				mcpRaw,
				[]byte(`"parameter_shape_projected":true,"parameter_shape":{}`),
				[]byte(`"parameter_shape_projected":true`),
			),
		},
		{
			name: "MCP projected false with shape",
			raw: bytesReplaceOnce(
				mcpRaw,
				[]byte(`"parameter_shape_projected":true,"parameter_shape":{}`),
				[]byte(`"parameter_shape_projected":false,"parameter_shape":{}`),
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if decoded, err := DecodeWorkflowAuthoringCapabilities(test.raw); err == nil {
				t.Fatalf("decoded inconsistent conditional shape pair: %#v", decoded)
			}
		})
	}
}

func TestDecodeWorkflowAuthoringCapabilitiesRejectsOverDepth(t *testing.T) {
	shape := `{}`
	for index := 0; index < MaxWorkflowAuthoringShapeDepth; index++ {
		shape = `{"items":` + shape + `}`
	}
	raw := []byte(`{"complete":true,"mcp_status":"disabled",` +
		`"agents":[{"id":"main","target":"agent/main","is_default":true,"readiness":"ready"}],` +
		`"tools":[{"name":"alpha","target":"tool/alpha","readiness":"ready",` +
		`"parameter_shape_projected":true,"parameter_shape":` + shape + `}],` +
		`"mcp_tools":[],"functions":[` + testWorkflowAuthoringFunctionsJSON() + `],"limits":[]}`)
	if decoded, err := DecodeWorkflowAuthoringCapabilities(raw); err == nil {
		t.Fatalf("decoded over-depth catalog: %#v", decoded)
	}
}

func TestDecodeWorkflowAuthoringCapabilitiesRejectsRawJSONOverDepth(t *testing.T) {
	raw, ok := MarshalWorkflowAuthoringCapabilities(testWorkflowAuthoringCatalog())
	if !ok {
		t.Fatal("valid test catalog did not marshal")
	}
	nested := `[]`
	for index := 0; index < 40; index++ {
		nested = `[` + nested + `]`
	}
	candidate := bytesReplaceOnce(raw, []byte(`"limits":[]`), []byte(`"limits":`+nested))
	if decoded, err := DecodeWorkflowAuthoringCapabilities(candidate); err == nil {
		t.Fatalf("decoded over-nested raw JSON: %#v", decoded)
	}
}

func TestDecodeWorkflowAuthoringCapabilitiesRejectsOverboundNumberSpelling(t *testing.T) {
	raw, ok := MarshalWorkflowAuthoringCapabilities(testWorkflowAuthoringCatalog())
	if !ok {
		t.Fatal("valid test catalog did not marshal")
	}
	oversizedNumber := strings.Repeat("9", 1<<20)
	candidate := bytesReplaceOnce(
		raw,
		[]byte(`"parameter_shape":{}`),
		[]byte(`"parameter_shape":{"enum":[`+oversizedNumber+`]}`),
	)
	if decoded, err := DecodeWorkflowAuthoringCapabilities(candidate); err == nil {
		t.Fatalf("decoded overbound numeric spelling: %#v", decoded)
	}
}

func TestMarshalWorkflowAuthoringCapabilitiesEnforcesResponseBound(t *testing.T) {
	catalog := WorkflowAuthoringCapabilities{
		Agents: []WorkflowAuthoringAgentCapability{{
			ID:     strings.Repeat("x", int(MaxWorkflowAuthoringResponseBytes)),
			Target: "agent/main",
		}},
	}
	if encoded, ok := MarshalWorkflowAuthoringCapabilities(catalog); ok || encoded != nil {
		t.Fatal("oversized catalog unexpectedly marshaled")
	}
}

func testWorkflowAuthoringCatalog() WorkflowAuthoringCapabilities {
	functions := make([]WorkflowAuthoringFunctionCapability, 0, MaxWorkflowAuthoringFunctions)
	for _, name := range NativeFunctionNames() {
		functions = append(functions, WorkflowAuthoringFunctionCapability{
			Name:      name,
			Target:    "function/" + name,
			Readiness: WorkflowDependencyReadinessReady,
		})
	}
	return WorkflowAuthoringCapabilities{
		Complete:  true,
		MCPStatus: WorkflowAuthoringMCPDisabled,
		Agents: []WorkflowAuthoringAgentCapability{{
			ID:        "main",
			Target:    "agent/main",
			IsDefault: true,
			Readiness: WorkflowDependencyReadinessReady,
		}},
		Tools: []WorkflowAuthoringToolCapability{{
			Name:                    "alpha",
			Target:                  "tool/alpha",
			Readiness:               WorkflowDependencyReadinessReady,
			ParameterShapeProjected: true,
			ParameterShape:          &WorkflowAuthoringParameterShape{},
		}},
		MCPTools:  []WorkflowAuthoringMCPToolCapability{},
		Functions: functions,
		Limits:    []WorkflowAuthoringLimitCode{},
	}
}

func testWorkflowAuthoringFunctionsJSON() string {
	parts := make([]string, 0, MaxWorkflowAuthoringFunctions)
	for _, name := range NativeFunctionNames() {
		parts = append(parts, `{"name":"`+name+`","target":"function/`+name+`","readiness":"ready"}`)
	}
	return strings.Join(parts, ",")
}

func bytesReplaceOnce(raw, old, replacement []byte) []byte {
	return []byte(strings.Replace(string(raw), string(old), string(replacement), 1))
}
