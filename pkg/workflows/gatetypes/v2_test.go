package gatetypes

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestGateWorkflowSpecV2StrictCanonicalJSON(t *testing.T) {
	raw := []byte(`{
  "stages":[{"questions":{"minimum":9007199254740993},"kind":"human","title":"Approve","id":"approve"}],
  "decision_point":"pr.charter.confirm","purpose":"authorization","name":"Charter approval","id":"charter"
}`)
	var spec GateWorkflowSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	questions, ok := spec.Stages[0].Questions.(map[string]any)
	if !ok || questions["minimum"] != json.Number("9007199254740993") {
		t.Fatalf("questions = %#v, want exact json.Number", spec.Stages[0].Questions)
	}
	canonical, err := CanonicalGateWorkflowSpecJSON(spec)
	if err != nil {
		t.Fatalf("CanonicalGateWorkflowSpecJSON() error = %v", err)
	}
	want := `{"id":"charter","name":"Charter approval","purpose":"authorization","decision_point":"pr.charter.confirm","stages":[{"id":"approve","title":"Approve","kind":"human","questions":{"minimum":9007199254740993}}]}`
	if string(canonical) != want {
		t.Fatalf("canonical JSON = %s, want %s", canonical, want)
	}
	second, err := CanonicalGateWorkflowSpecJSON(spec)
	if err != nil || !bytes.Equal(canonical, second) {
		t.Fatalf("second canonical JSON = %s, %v; want identical", second, err)
	}
}

func TestGateWorkflowSpecV2RejectsUnknownDuplicateAndTrailingJSON(t *testing.T) {
	valid := `"id":"charter","name":"Charter","purpose":"authorization","decision_point":"pr.charter.confirm","stages":[{"id":"allow","kind":"zero"}]`
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "unknown", raw: `{` + valid + `,"surprise":true}`, want: "unknown field"},
		{name: "duplicate root", raw: `{` + valid + `,"id":"again"}`, want: "duplicate key"},
		{name: "duplicate nested", raw: `{"id":"charter","name":"Charter","purpose":"authorization","decision_point":"pr.charter.confirm","stages":[{"id":"allow","kind":"zero","kind":"human"}]}`, want: "duplicate key"},
		{name: "trailing", raw: `{` + valid + `} {}`, want: "after top-level value"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var spec GateWorkflowSpec
			err := json.Unmarshal([]byte(test.raw), &spec)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Unmarshal() error = %v, want containing %q", err, test.want)
			}
		})
	}
}
