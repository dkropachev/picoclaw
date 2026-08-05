package gatetypes

import (
	"encoding/json"
	"testing"
)

func TestGateSpecUnmarshalJSONPreservesQuestionNumbers(t *testing.T) {
	const number = "9007199254740993"
	var spec GateSpec
	if err := json.Unmarshal([]byte(`{
		"id":"ask",
		"kind":"ai_isolated_context",
		"questions":{"issue_number":`+number+`}
	}`), &spec); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	questions, ok := spec.Questions.(map[string]any)
	if !ok {
		t.Fatalf("questions = %T, want map[string]any", spec.Questions)
	}
	if got := questions["issue_number"]; got != json.Number(number) {
		t.Fatalf("issue_number = %#v, want json.Number(%q)", got, number)
	}
}

func TestGateSpecUnmarshalJSONRejectsUnknownField(t *testing.T) {
	var spec GateSpec
	err := json.Unmarshal([]byte(`{
		"id":"ask",
		"kind":"zero",
		"unexpected":"field"
	}`), &spec)
	if err == nil {
		t.Fatal("Unmarshal() error = nil, want unknown-field failure")
	}
}
