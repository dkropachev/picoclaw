package lifecycleflow

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderSVGUsesNormalizedGraphAndClickableGates(t *testing.T) {
	graph := Graph{
		Schema: SchemaV1,
		Flows: []Flow{{
			ID: "review", Title: "Review workflow", Entry: "github_review_requested",
			Nodes: []Node{
				{ID: "github_review_requested", Kind: NodeAction, Title: "GitHub review request", Description: "GitHub assigns the pull request.", Operation: "github.review.requested"},
				{ID: "allow_review", Kind: NodeGate, Title: "Allow AI review", Description: "Decides whether review can begin.", DecisionPoint: "pr.review.start", Ordinal: 3, Editable: true},
				{ID: "hard_scope", Kind: NodeGate, Title: "Resolve hard scope", Description: "Blocks code outside the approved charter.", Safeguard: "pr.scope.hard"},
			},
			Edges: []Edge{
				{From: "github_review_requested", To: "allow_review", Outcome: "allow", Label: "Allow", Mode: EdgeChoice},
				{From: "github_review_requested", To: "hard_scope", Outcome: "hard_stop", Label: "Hard stop", Mode: EdgeChoice},
				{From: "allow_review", To: "github_review_requested", Mode: EdgeLinear, Loop: true},
			},
		}},
	}
	first, err := RenderSVG(graph, "sha256:test", map[string]string{"pr.review.start": "mixed"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderSVG(graph, "sha256:test", map[string]string{"pr.review.start": "mixed"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("SVG rendering is not deterministic")
	}
	text := string(first)
	for _, expected := range []string{
		`data-source="pkg/prworkspace/lifecycleflow/manifest.yaml"`,
		`data-flow-revision="sha256:test"`,
		`data-flow-id="review"`,
		`data-decision-point="pr.review.start"`,
		`data-safeguard="pr.scope.hard"`,
		`gate=pr.review.start`,
		`>GATE</text>`,
		`MIXED`,
		`Hard stop`,
		`data-flow-edge="loop"`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("SVG missing %q", expected)
		}
	}
	if !strings.Contains(text, `/pull-requests/gate-configs/default?flow=review&amp;gate=`) {
		t.Fatal("editable gate URL is not XML-safe")
	}
}

func TestRenderSVGRejectsInvalidEnvelope(t *testing.T) {
	if _, err := RenderSVG(Graph{}, "sha256:test", nil); err == nil {
		t.Fatal("empty graph rendered")
	}
	if _, err := RenderSVG(Graph{Schema: SchemaV1, Flows: []Flow{{ID: "review"}}}, "sha256:test", nil); err == nil {
		t.Fatal("flow without nodes rendered")
	}
	if _, err := RenderSVG(Graph{Schema: SchemaV1, Flows: []Flow{{
		ID: "review", Nodes: []Node{{ID: "a"}}, Edges: []Edge{{From: "a", To: "missing"}},
	}}}, "sha256:test", nil); err == nil {
		t.Fatal("dangling edge rendered")
	}
}
