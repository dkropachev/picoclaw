package lifecycleflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDefaultManifestIsDeterministicCompleteAndDetached(t *testing.T) {
	graph, revision := Default()
	loaded, loadedRevision, err := LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(graph, loaded) || revision != loadedRevision {
		t.Fatalf("Default() and LoadDefault() differ: %s vs %s", revision, loadedRevision)
	}
	if graph.Schema != SchemaV1 || len(graph.Flows) != 2 ||
		graph.Flows[0].ID != "review" || graph.Flows[1].ID != "implementation" {
		t.Fatalf("default graph envelope = %#v", graph)
	}
	encoded, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	wantRevision := "sha256:" + hex.EncodeToString(digest[:])
	if revision != wantRevision {
		t.Fatalf("revision = %q, want %q", revision, wantRevision)
	}

	decisionPoints := make(map[string]struct{})
	operations := make(map[string]struct{})
	outcomes := make(map[string]struct{})
	safeguards := make(map[string]struct{})
	nodeIDs := make(map[string]struct{})
	for _, flow := range graph.Flows {
		if flow.Entry == "" || len(flow.Nodes) == 0 || len(flow.Edges) == 0 {
			t.Fatalf("incomplete flow = %#v", flow)
		}
		for _, node := range flow.Nodes {
			if _, duplicate := nodeIDs[node.ID]; duplicate {
				t.Fatalf("duplicate default node ID %q", node.ID)
			}
			nodeIDs[node.ID] = struct{}{}
			if node.DecisionPoint != "" {
				decisionPoints[node.DecisionPoint] = struct{}{}
			}
			if node.Operation != "" {
				operations[node.Operation] = struct{}{}
			}
			if node.Safeguard != "" {
				safeguards[node.Safeguard] = struct{}{}
			}
		}
		for _, edge := range flow.Edges {
			if edge.Outcome != "" {
				outcomes[edge.Outcome] = struct{}{}
			}
		}
	}
	if got := sortedSetKeys(decisionPoints); !reflect.DeepEqual(got, KnownDecisionPoints()) {
		t.Fatalf("default decision points = %v, want %v", got, KnownDecisionPoints())
	}
	if got := sortedSetKeys(operations); !reflect.DeepEqual(got, KnownOperations()) {
		t.Fatalf("default operations = %v, want %v", got, KnownOperations())
	}
	if got := sortedSetKeys(outcomes); !reflect.DeepEqual(got, KnownOutcomes()) {
		t.Fatalf("default outcomes = %v, want %v", got, KnownOutcomes())
	}
	if got := sortedSetKeys(safeguards); !reflect.DeepEqual(got, KnownSafeguards()) {
		t.Fatalf("default safeguards = %v, want %v", got, KnownSafeguards())
	}

	graph.Flows[0].Title = "mutated"
	graph.Flows[0].Nodes[0].Title = "mutated"
	graph.Flows[0].Edges[0].To = "mutated"
	fresh, freshRevision := Default()
	if fresh.Flows[0].Title == "mutated" || fresh.Flows[0].Nodes[0].Title == "mutated" ||
		fresh.Flows[0].Edges[0].To == "mutated" || freshRevision != revision {
		t.Fatal("Default() returned shared mutable graph storage")
	}
}

func TestDefaultManifestPreservesCriticalLifecycleTopology(t *testing.T) {
	graph, _ := Default()
	review := normalizedFlow(t, graph, "review")
	implementation := normalizedFlow(t, graph, "implementation")

	for _, edge := range []struct {
		flow              Flow
		from, to, outcome string
		mode              EdgeMode
		loop              bool
	}{
		{review, "review_process_result", "review_finish", "no_findings", EdgeChoice, false},
		{review, "review_process_result", "review_record_correction", "", EdgeOptional, false},
		{review, "review_record_correction", "review_gate_correction_promote", "", EdgeOptional, false},
		{review, "review_classify_automatic", "review_route_classified", "", EdgeLinear, false},
		{review, "review_gate_classify", "review_route_classified", "", EdgeLinear, false},
		{review, "review_route_classified", "review_revise_charter", "revise", EdgeChoice, false},
		{review, "review_revise_charter", "review_gate_charter_reconfirm", "", EdgeLinear, true},
		{review, "review_keep_in_scope", "review_select_review_findings", "", EdgeOptional, false},
		{review, "review_keep_in_scope", "review_select_implementation_findings", "", EdgeOptional, false},
		{review, "review_group_deferred", "review_link_followup_issue", "existing", EdgeChoice, false},
		{review, "review_group_deferred", "review_gate_deferred_publish", "create", EdgeChoice, false},
		{review, "review_publish_github", "review_retry_publication", "failed", EdgeChoice, false},
		{review, "review_gate_reconcile", "review_resolve_publication", "reobserve", EdgeChoice, false},
		{review, "review_gate_reconcile", "review_retry_publication", "assume_failed", EdgeChoice, false},
		{review, "review_resolve_publication", "review_gate_reconcile", "still_unknown", EdgeChoice, true},
		{review, "review_retry_publication", "review_gate_publish", "", EdgeLinear, true},
		{implementation, "implementation_check_ownership", "implementation_gate_start", "owned", EdgeChoice, false},
		{implementation, "implementation_check_ownership", "implementation_gate_eligibility", "non_owned", EdgeChoice, false},
		{implementation, "implementation_check_ownership", "implementation_stop", "read_only", EdgeChoice, false},
		{implementation, "implementation_repair_validation", "implementation_run_ai", "", EdgeLinear, true},
		{implementation, "implementation_completion_audit", "implementation_route_completion", "", EdgeParallel, false},
		{implementation, "implementation_completion_audit", "implementation_group_deferred", "", EdgeOptional, false},
		{implementation, "implementation_route_completion", "implementation_finalize_candidate", "complete", EdgeChoice, false},
		{implementation, "implementation_finalize_candidate", "implementation_final_scope_check", "", EdgeLinear, false},
		{implementation, "implementation_start_joint_gates", "implementation_gate_scope_policy", "", EdgeParallel, false},
		{implementation, "implementation_start_joint_gates", "implementation_gate_complete_policy", "", EdgeParallel, false},
		{implementation, "implementation_gate_complete_direct", "implementation_queue_branch", "", EdgeLinear, false},
		{implementation, "implementation_completion_gates_join", "implementation_queue_branch", "", EdgeLinear, false},
		{implementation, "implementation_queue_branch", "implementation_gate_publish", "", EdgeLinear, false},
		{implementation, "implementation_push", "implementation_retry_publication", "failed", EdgeChoice, false},
		{implementation, "implementation_result", "implementation_finish", "", EdgeLinear, false},
		{implementation, "implementation_result", "implementation_record_correction", "", EdgeOptional, false},
		{implementation, "implementation_record_correction", "implementation_gate_correction_promote", "", EdgeOptional, false},
		{implementation, "implementation_remove_and_defer", "implementation_run_ai", "", EdgeParallel, true},
		{implementation, "implementation_remove_and_defer", "implementation_group_deferred", "", EdgeParallel, false},
		{implementation, "implementation_group_deferred", "implementation_link_followup_issue", "existing", EdgeChoice, false},
		{implementation, "implementation_group_deferred", "implementation_gate_deferred_publish", "create", EdgeChoice, false},
		{implementation, "implementation_gate_charter_reconfirm", "implementation_return_to_review", "", EdgeLinear, false},
		{implementation, "implementation_gate_reconcile", "implementation_resolve_publication", "reobserve", EdgeChoice, false},
		{implementation, "implementation_gate_reconcile", "implementation_retry_publication", "assume_failed", EdgeChoice, false},
		{implementation, "implementation_resolve_publication", "implementation_gate_reconcile", "still_unknown", EdgeChoice, true},
		{implementation, "implementation_retry_publication", "implementation_queue_branch", "", EdgeLinear, true},
	} {
		actual := normalizedEdge(t, edge.flow, edge.from, edge.to)
		if actual.Outcome != edge.outcome || actual.Mode != edge.mode || actual.Loop != edge.loop {
			t.Fatalf("edge %s -> %s = %#v", edge.from, edge.to, actual)
		}
	}

	wantDispositionTargets := map[string]string{
		"in_scope":  "review_keep_in_scope",
		"deferred":  "review_group_deferred",
		"dismissed": "review_dismiss_finding",
		"revise":    "review_revise_charter",
	}
	if got := outgoingTargets(review, "review_route_classified"); !reflect.DeepEqual(got, wantDispositionTargets) {
		t.Fatalf("classification outcomes = %v, want %v", got, wantDispositionTargets)
	}
	if normalizedNode(t, implementation, "implementation_gate_scope_policy").DecisionPoint != "pr.implementation.scope" ||
		normalizedNode(t, implementation, "implementation_gate_complete_policy").DecisionPoint != "pr.implementation.complete" ||
		normalizedNode(t, implementation, "implementation_gate_complete_direct").DecisionPoint != "pr.implementation.complete" {
		t.Fatal("conditional/joint implementation gate decision points changed")
	}
	for _, flow := range graph.Flows {
		for _, node := range flow.Nodes {
			if node.DecisionPoint != "" && node.Ordinal != decisionPointOrdinals[node.DecisionPoint] {
				t.Fatalf("node %q ordinal = %d, want %d", node.ID, node.Ordinal, decisionPointOrdinals[node.DecisionPoint])
			}
		}
	}
}

func TestParseRejectsUnsafeOrNonStrictYAML(t *testing.T) {
	source := defaultSource(t)
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "empty", data: nil, want: "empty"},
		{name: "oversized", data: []byte(strings.Repeat("x", MaxManifestBytes+1)), want: "exceeds"},
		{name: "unknown root field", data: append(append([]byte(nil), source...), []byte("unknown: true\n")...), want: "field unknown not found"},
		{
			name: "unknown nested field",
			data: []byte(strings.Replace(string(source), "        operation: github.review.requested", "        operation: github.review.requested\n        surprise: true", 1)),
			want: "field surprise not found",
		},
		{
			name: "duplicate key",
			data: []byte(strings.Replace(string(source), "schema: pr-lifecycle-flow/v1", "schema: pr-lifecycle-flow/v1\nschema: pr-lifecycle-flow/v1", 1)),
			want: "duplicate key",
		},
		{name: "multiple documents", data: append(append([]byte(nil), source...), []byte("---\nschema: pr-lifecycle-flow/v1\n")...), want: "multiple YAML documents"},
		{
			name: "anchor",
			data: []byte(strings.Replace(string(source), "    title: Review workflow", "    title: &shared Review workflow", 1)),
			want: "aliases or anchors",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := Parse(test.data)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestParseRejectsInvalidFlowAndNodeSemantics(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*manifestDocument)
		want   string
	}{
		{
			name:   "wrong schema",
			mutate: func(document *manifestDocument) { document.Schema = "pr-lifecycle-flow/v2" },
			want:   "schema must be",
		},
		{
			name:   "missing flow",
			mutate: func(document *manifestDocument) { document.Flows = document.Flows[:1] },
			want:   "exactly 2 flows",
		},
		{
			name:   "unknown flow",
			mutate: func(document *manifestDocument) { document.Flows[0].ID = "other" },
			want:   "unknown flow ID",
		},
		{
			name:   "duplicate flow",
			mutate: func(document *manifestDocument) { document.Flows[1].ID = "review" },
			want:   "duplicates flow ID",
		},
		{
			name: "reordered flows",
			mutate: func(document *manifestDocument) {
				document.Flows[0], document.Flows[1] = document.Flows[1], document.Flows[0]
			},
			want: `flows[0] must contain flow "review"`,
		},
		{
			name:   "missing entry",
			mutate: func(document *manifestDocument) { document.Flows[0].Entry = "review_missing" },
			want:   "entry node \"review_missing\" does not exist",
		},
		{
			name:   "duplicate local node",
			mutate: func(document *manifestDocument) { document.Flows[0].Nodes[1].ID = document.Flows[0].Nodes[0].ID },
			want:   "duplicates node ID",
		},
		{
			name:   "duplicate global node",
			mutate: func(document *manifestDocument) { document.Flows[1].Nodes[0].ID = document.Flows[0].Nodes[0].ID },
			want:   "from flow \"review\"",
		},
		{
			name:   "unknown node kind",
			mutate: func(document *manifestDocument) { document.Flows[0].Nodes[0].Kind = "prompt" },
			want:   "unknown node kind",
		},
		{
			name:   "unknown operation",
			mutate: func(document *manifestDocument) { document.Flows[0].Nodes[0].Operation = "shell.exec" },
			want:   "unknown action operation",
		},
		{
			name: "action gate metadata",
			mutate: func(document *manifestDocument) {
				value := true
				document.Flows[0].Nodes[0].Editable = &value
			},
			want: "action may only declare operation metadata",
		},
		{
			name:   "gate editable omitted",
			mutate: func(document *manifestDocument) { document.Flows[0].Nodes[3].Editable = nil },
			want:   "must explicitly declare editable",
		},
		{
			name:   "gate ordinal omitted",
			mutate: func(document *manifestDocument) { document.Flows[0].Nodes[3].Ordinal = nil },
			want:   "must declare its ordinal",
		},
		{
			name: "wrong gate ordinal",
			mutate: func(document *manifestDocument) {
				ordinal := 14
				document.Flows[0].Nodes[3].Ordinal = &ordinal
			},
			want: "has ordinal 14, want 1",
		},
		{
			name:   "unknown decision point",
			mutate: func(document *manifestDocument) { document.Flows[0].Nodes[3].DecisionPoint = "pr.unknown" },
			want:   "unknown decision point",
		},
		{
			name:   "editable safeguard",
			mutate: func(document *manifestDocument) { document.Flows[0].Nodes[3].Safeguard = "pr.scope.hard" },
			want:   "editable gate may not declare a safeguard",
		},
		{
			name: "locked decision point",
			mutate: func(document *manifestDocument) {
				node := findRawNode(t, document, "implementation_hard_scope")
				node.DecisionPoint = "pr.implementation.scope"
			},
			want: "locked gate may not declare a decision point",
		},
		{
			name: "unknown safeguard",
			mutate: func(document *manifestDocument) {
				findRawNode(t, document, "implementation_hard_scope").Safeguard = "pr.scope.unknown"
			},
			want: "unknown safeguard",
		},
		{
			name:   "untrimmed title",
			mutate: func(document *manifestDocument) { document.Flows[0].Nodes[0].Title = " Request" },
			want:   "trimmed UTF-8",
		},
		{
			name: "missing catalog decision",
			mutate: func(document *manifestDocument) {
				node := findRawNode(t, document, "review_gate_charter_confirm")
				ordinal := 2
				node.DecisionPoint, node.Ordinal = "pr.charter.reconfirm", &ordinal
			},
			want: "missing decision point \"pr.charter.confirm\"",
		},
		{
			name: "missing mandatory safeguard",
			mutate: func(document *manifestDocument) {
				node := findRawNode(t, document, "implementation_hard_scope")
				value := true
				ordinal := 8
				node.Editable = &value
				node.Safeguard = ""
				node.DecisionPoint = "pr.implementation.scope"
				node.Ordinal = &ordinal
			},
			want: "missing safeguard \"pr.scope.hard\"",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := defaultDocument(t)
			test.mutate(&document)
			_, _, err := Parse(marshalDocument(t, document))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestParseRejectsInvalidEdgesForksReachabilityAndLoops(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*manifestDocument)
		want   string
	}{
		{
			name:   "missing source",
			mutate: func(document *manifestDocument) { document.Flows[0].Edges[0].From = "review_missing" },
			want:   "missing source node",
		},
		{
			name:   "missing target",
			mutate: func(document *manifestDocument) { document.Flows[0].Edges[0].To = "review_missing" },
			want:   "missing target node",
		},
		{
			name:   "self edge",
			mutate: func(document *manifestDocument) { document.Flows[0].Edges[0].To = document.Flows[0].Edges[0].From },
			want:   "self-edge",
		},
		{
			name: "duplicate edge",
			mutate: func(document *manifestDocument) {
				document.Flows[0].Edges = append(document.Flows[0].Edges, document.Flows[0].Edges[0])
			},
			want: "duplicates edge",
		},
		{
			name:   "edge mode omitted",
			mutate: func(document *manifestDocument) { document.Flows[0].Edges[0].Mode = "" },
			want:   "unknown edge mode",
		},
		{
			name: "linear label",
			mutate: func(document *manifestDocument) {
				document.Flows[0].Edges[0].Label = "Next"
				document.Flows[0].Edges[0].Outcome = "confirmed"
			},
			want: "one outgoing edge, which must be unlabeled",
		},
		{
			name: "single optional label",
			mutate: func(document *manifestDocument) {
				findRawEdge(t, document, "review_record_correction", "review_gate_correction_promote").Label = "Promote"
			},
			want: "one outgoing edge, which must be unlabeled",
		},
		{
			name: "single choice edge",
			mutate: func(document *manifestDocument) {
				findRawEdge(t, document, "review_record_correction", "review_gate_correction_promote").Mode = EdgeChoice
			},
			want: `must use mode "linear" or "optional"`,
		},
		{
			name: "single parallel edge",
			mutate: func(document *manifestDocument) {
				findRawEdge(t, document, "review_record_correction", "review_gate_correction_promote").Mode = EdgeParallel
			},
			want: `must use mode "linear" or "optional"`,
		},
		{
			name: "fork missing label",
			mutate: func(document *manifestDocument) {
				findRawEdge(t, document, "review_define_charter", "review_gate_charter_confirm").Label = ""
			},
			want: "requires a label and outcome",
		},
		{
			name: "fork label too long",
			mutate: func(document *manifestDocument) {
				findRawEdge(t, document, "review_define_charter", "review_gate_charter_confirm").Label = "First approved scope"
			},
			want: "one or two words",
		},
		{
			name: "fork duplicate label",
			mutate: func(document *manifestDocument) {
				findRawEdge(t, document, "review_define_charter", "review_gate_charter_confirm").Label = "Revised"
			},
			want: "duplicate label",
		},
		{
			name: "fork duplicate outcome",
			mutate: func(document *manifestDocument) {
				findRawEdge(t, document, "review_define_charter", "review_gate_charter_confirm").Outcome = "revised"
			},
			want: "duplicate outcome",
		},
		{
			name: "unknown outcome",
			mutate: func(document *manifestDocument) {
				findRawEdge(t, document, "review_define_charter", "review_gate_charter_confirm").Outcome = "maybe"
			},
			want: "unknown outcome",
		},
		{
			name: "choice mixed with parallel",
			mutate: func(document *manifestDocument) {
				findRawEdge(t, document, "review_define_charter", "review_gate_charter_confirm").Mode = EdgeParallel
			},
			want: "parallel edge",
		},
		{
			name: "optional edge outcome",
			mutate: func(document *manifestDocument) {
				findRawEdge(t, document, "review_keep_in_scope", "review_select_review_findings").Outcome = "confirmed"
			},
			want: "optional edge",
		},
		{
			name: "unlabeled linear primary mixed with optional sidecar",
			mutate: func(document *manifestDocument) {
				findRawEdge(t, document, "implementation_result", "implementation_finish").Label = ""
			},
			want: "requires a label",
		},
		{
			name: "unreachable node",
			mutate: func(document *manifestDocument) {
				document.Flows[0].Nodes = append(document.Flows[0].Nodes, manifestNode{
					ID: "review_orphan", Kind: NodeAction, Title: "Orphan", Description: "Never reached.",
					Operation: "pr.review.finish",
				})
			},
			want: "unreachable nodes: review_orphan",
		},
		{
			name: "unmarked cycle",
			mutate: func(document *manifestDocument) {
				findRawEdge(t, document, "review_revise_charter", "review_gate_charter_reconfirm").Loop = false
			},
			want: "cycle without an explicit loop edge",
		},
		{
			name:   "loop does not return",
			mutate: func(document *manifestDocument) { document.Flows[0].Edges[0].Loop = true },
			want:   "does not return to an ancestor",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := defaultDocument(t)
			test.mutate(&document)
			_, _, err := Parse(marshalDocument(t, document))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestKnownCatalogsAreSortedAndDetached(t *testing.T) {
	for name, values := range map[string][]string{
		"decision points": KnownDecisionPoints(),
		"operations":      KnownOperations(),
		"outcomes":        KnownOutcomes(),
		"safeguards":      KnownSafeguards(),
	} {
		if len(values) == 0 || !sort.StringsAreSorted(values) {
			t.Fatalf("%s = %v", name, values)
		}
		original := values[0]
		values[0] = "mutated"
		var fresh []string
		switch name {
		case "decision points":
			fresh = KnownDecisionPoints()
		case "operations":
			fresh = KnownOperations()
		case "outcomes":
			fresh = KnownOutcomes()
		default:
			fresh = KnownSafeguards()
		}
		if fresh[0] != original {
			t.Fatalf("%s returned shared storage", name)
		}
	}
}

func defaultSource(t *testing.T) []byte {
	t.Helper()
	data, err := manifestFS.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func defaultDocument(t *testing.T) manifestDocument {
	t.Helper()
	var document manifestDocument
	if err := yaml.Unmarshal(defaultSource(t), &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func marshalDocument(t *testing.T, document manifestDocument) []byte {
	t.Helper()
	data, err := yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func findRawNode(t *testing.T, document *manifestDocument, id string) *manifestNode {
	t.Helper()
	for flowIndex := range document.Flows {
		for nodeIndex := range document.Flows[flowIndex].Nodes {
			node := &document.Flows[flowIndex].Nodes[nodeIndex]
			if node.ID == id {
				return node
			}
		}
	}
	t.Fatalf("node %q not found", id)
	return nil
}

func findRawEdge(t *testing.T, document *manifestDocument, from, to string) *manifestEdge {
	t.Helper()
	for flowIndex := range document.Flows {
		for edgeIndex := range document.Flows[flowIndex].Edges {
			edge := &document.Flows[flowIndex].Edges[edgeIndex]
			if edge.From == from && edge.To == to {
				return edge
			}
		}
	}
	t.Fatalf("edge %q -> %q not found", from, to)
	return nil
}

func normalizedFlow(t *testing.T, graph Graph, id string) Flow {
	t.Helper()
	for _, flow := range graph.Flows {
		if flow.ID == id {
			return flow
		}
	}
	t.Fatalf("flow %q not found", id)
	return Flow{}
}

func normalizedNode(t *testing.T, flow Flow, id string) Node {
	t.Helper()
	for _, node := range flow.Nodes {
		if node.ID == id {
			return node
		}
	}
	t.Fatalf("node %q not found in flow %q", id, flow.ID)
	return Node{}
}

func normalizedEdge(t *testing.T, flow Flow, from, to string) Edge {
	t.Helper()
	for _, edge := range flow.Edges {
		if edge.From == from && edge.To == to {
			return edge
		}
	}
	t.Fatalf("edge %q -> %q not found in flow %q", from, to, flow.ID)
	return Edge{}
}

func outgoingTargets(flow Flow, source string) map[string]string {
	targets := make(map[string]string)
	for _, edge := range flow.Edges {
		if edge.From == source {
			targets[edge.Outcome] = edge.To
		}
	}
	return targets
}

func sortedSetKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
