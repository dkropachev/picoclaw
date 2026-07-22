package workflows

import "testing"

func TestParseAgentOutputContractDefaultsRepairAttempts(t *testing.T) {
	contract, err := ParseAgentOutputContract("json")
	if err != nil {
		t.Fatalf("ParseAgentOutputContract(string) error = %v", err)
	}
	if contract == nil || contract.Format != "json" || contract.RepairAttempts != 1 {
		t.Fatalf("contract = %#v, want json with one repair attempt", contract)
	}

	contract, err = ParseAgentOutputContract(map[string]any{
		"schema": map[string]any{
			"type": "object",
		},
		"repair_attempts": -1,
	})
	if err != nil {
		t.Fatalf("ParseAgentOutputContract(map) error = %v", err)
	}
	if contract.Format != "json" || contract.RepairAttempts != 1 {
		t.Fatalf("contract = %#v, want inferred json with default repair attempt", contract)
	}
}

func TestParseAgentOutputContractRejectsUnsupportedFormat(t *testing.T) {
	_, err := ParseAgentOutputContract(map[string]any{"format": "xml"})
	if err == nil {
		t.Fatal("ParseAgentOutputContract() error = nil, want unsupported format error")
	}
}

func TestValidateAgentStructuredOutputParsesAndValidatesSchema(t *testing.T) {
	contract := &AgentOutputContract{
		Format: "json",
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"summary", "findings"},
			"properties": map[string]any{
				"summary": map[string]any{"type": "string"},
				"findings": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type":     "object",
						"required": []any{"severity", "title"},
						"properties": map[string]any{
							"severity": map[string]any{
								"type": "string",
								"enum": []any{"high", "low"},
							},
							"title": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
	}

	result := ValidateAgentStructuredOutput(
		"```json\n{\"summary\":\"ok\",\"findings\":[{\"severity\":\"high\",\"title\":\"bug\"}]}\n```",
		contract,
	)
	if !result.Valid {
		t.Fatalf("structured output valid = false: %s", result.Error)
	}
	parsed, ok := result.Structured.(map[string]any)
	if !ok || parsed["summary"] != "ok" {
		t.Fatalf("structured output = %#v, want parsed object", result.Structured)
	}
}

func TestValidateAgentStructuredOutputRejectsSchemaMismatch(t *testing.T) {
	contract := &AgentOutputContract{
		Format: "json",
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"summary"},
			"properties": map[string]any{
				"summary": map[string]any{"type": "string"},
			},
		},
	}

	result := ValidateAgentStructuredOutput(`{"findings":[]}`, contract)
	if result.Valid {
		t.Fatalf("structured output valid = true, want schema error")
	}
	if result.Error == "" {
		t.Fatalf("structured output error is empty")
	}
}

func TestValidateAgentStructuredOutputChecksArrayItemSchema(t *testing.T) {
	contract := &AgentOutputContract{
		Format: "json",
		Schema: map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":     "object",
				"required": []any{"id"},
				"properties": map[string]any{
					"id": map[string]any{"type": "string"},
				},
			},
		},
	}

	result := ValidateAgentStructuredOutput(`[{"id":1}]`, contract)
	if result.Valid {
		t.Fatalf("structured output valid = true, want item schema error")
	}
	if result.Error == "" {
		t.Fatalf("structured output error is empty")
	}
}

func TestValidateAgentStructuredOutputChecksScalarSchemaTypes(t *testing.T) {
	tests := []struct {
		name   string
		schema map[string]any
		text   string
		valid  bool
	}{
		{
			name:   "integer",
			schema: map[string]any{"type": "integer"},
			text:   `3`,
			valid:  true,
		},
		{
			name:   "integer rejects fraction",
			schema: map[string]any{"type": "integer"},
			text:   `3.5`,
		},
		{
			name:   "number",
			schema: map[string]any{"type": "number"},
			text:   `3.5`,
			valid:  true,
		},
		{
			name:   "boolean",
			schema: map[string]any{"type": "boolean"},
			text:   `true`,
			valid:  true,
		},
		{
			name:   "enum",
			schema: map[string]any{"type": "string", "enum": []any{"low", "high"}},
			text:   `"high"`,
			valid:  true,
		},
		{
			name:   "enum rejects unknown",
			schema: map[string]any{"type": "string", "enum": []any{"low", "high"}},
			text:   `"medium"`,
		},
		{
			name:   "nullable type chooses non-null",
			schema: map[string]any{"type": []any{"null", "string"}},
			text:   `"ok"`,
			valid:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateAgentStructuredOutput(tt.text, &AgentOutputContract{
				Format: "json",
				Schema: tt.schema,
			})
			if result.Valid != tt.valid {
				t.Fatalf("Valid = %v, want %v; error=%s", result.Valid, tt.valid, result.Error)
			}
		})
	}
}

func TestCombineStructuredOutputsConcatenatesSchemaArrays(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary":  map[string]any{"type": "string"},
			"findings": map[string]any{"type": "array"},
		},
	}
	combined := CombineStructuredOutputs([]any{
		map[string]any{"summary": "one", "findings": []any{map[string]any{"id": "a"}}},
		map[string]any{"summary": "two", "findings": []any{map[string]any{"id": "b"}}},
	}, schema)
	obj, ok := combined.(map[string]any)
	if !ok {
		t.Fatalf("combined = %#v, want object", combined)
	}
	if obj["summary"] != "one\ntwo" {
		t.Fatalf("summary = %#v, want joined summaries", obj["summary"])
	}
	if got := len(obj["findings"].([]any)); got != 2 {
		t.Fatalf("findings len = %d, want 2", got)
	}
}

func TestCombineStructuredOutputsWrapsNonObjectSchemaResults(t *testing.T) {
	combined := CombineStructuredOutputs([]any{
		[]any{map[string]any{"id": "a"}},
		[]any{map[string]any{"id": "b"}},
	}, map[string]any{"type": "array"})
	obj, ok := combined.(map[string]any)
	if !ok {
		t.Fatalf("combined = %#v, want wrapper object", combined)
	}
	results, ok := obj["results"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("results = %#v, want two child results", obj["results"])
	}
}

func TestCompareStructuredOutputsMatchesStableObjectArrayIdentities(t *testing.T) {
	left := map[string]any{
		"summary": "grouped wording",
		"findings": []any{
			map[string]any{
				"file":     "src/auth.js",
				"line":     3,
				"severity": "high",
				"title":    "first wording",
				"evidence": "long explanation A",
			},
			map[string]any{
				"file":     "src/session.js",
				"line":     3,
				"severity": "high",
				"title":    "second wording",
				"evidence": "long explanation B",
			},
		},
	}
	right := map[string]any{
		"summary": "split wording",
		"findings": []any{
			map[string]any{
				"file":           "src/session.js",
				"line":           3,
				"severity":       "high",
				"title":          "different prose",
				"recommendation": "fix it",
			},
			map[string]any{
				"file":           "src/auth.js",
				"line":           3,
				"severity":       "high",
				"title":          "different prose too",
				"recommendation": "fix it",
			},
		},
	}

	comparison := CompareStructuredOutputs(left, right)
	if comparison["match"] != true {
		t.Fatalf("comparison = %#v, want stable identity match", comparison)
	}
	if comparison["method"] != "object_array_identities" {
		t.Fatalf("method = %#v, want object_array_identities", comparison["method"])
	}
}
