package workflows

import (
	"errors"
	"strings"
	"testing"
)

func TestWorkflowDefinitionIDIsCanonicalStableAndOpaque(t *testing.T) {
	const ref = "workflows/team/review.yml"
	first, err := WorkflowDefinitionID(ref)
	if err != nil {
		t.Fatalf("WorkflowDefinitionID() error = %v", err)
	}
	second, err := WorkflowDefinitionID(ref)
	if err != nil || second != first {
		t.Fatalf("second ID = %q, %v; want %q", second, err, first)
	}
	if !ValidWorkflowDefinitionID(first) ||
		!WorkflowDefinitionIDMatches(first, ref) ||
		WorkflowDefinitionIDMatches(first, "workflows/team/other.yml") {
		t.Fatalf("workflow definition ID did not bind exact ref: %q", first)
	}
	if strings.Contains(first, "review") || strings.ContainsAny(first, "/=+") {
		t.Fatalf("workflow definition ID is not opaque URL-safe base64: %q", first)
	}
}

func TestWorkflowDefinitionIDRejectsAliasesAndInvalidWireShapes(t *testing.T) {
	for _, ref := range []string{
		" workflows/a.yml", "./workflows/a.yml", "workflows/../a.yml",
		"draft:workflows/a.yml", "workflows/a.txt", "workflows/a.yml\x00",
	} {
		if _, err := WorkflowDefinitionID(ref); !errors.Is(err, ErrInvalidWorkflowDefinitionID) {
			t.Fatalf("WorkflowDefinitionID(%q) error = %v", ref, err)
		}
	}
	valid, err := WorkflowDefinitionID("workflows/a.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", valid + "=", valid[:len(valid)-1], strings.Repeat("!", len(valid))} {
		if ValidWorkflowDefinitionID(id) {
			t.Fatalf("ValidWorkflowDefinitionID(%q) = true", id)
		}
		if WorkflowDefinitionIDMatches(id, "workflows/a.yml") {
			t.Fatalf("WorkflowDefinitionIDMatches(%q) = true", id)
		}
	}
}
