// Package lifecycleflow owns the canonical, presentation-safe graph for the
// pull-request review and implementation lifecycle.
package lifecycleflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/sipeed/picoclaw/pkg/prlifecycle"
)

const (
	SchemaV1         = "pr-lifecycle-flow/v1"
	MaxManifestBytes = 256 << 10
	maxFlows         = 2
	maxNodesPerFlow  = 256
	maxEdgesPerFlow  = 1024
	maxIDBytes       = 128
	maxTitleBytes    = 256
	maxDescription   = 1024
)

type NodeKind string

// EdgeMode makes routing semantics explicit. Linear edges are mandatory,
// choice edges are mutually exclusive, parallel edges are mandatory fan-out,
// and optional edges are independent zero-or-one/zero-or-more side paths.
type EdgeMode string

const (
	NodeAction NodeKind = "action"
	NodeGate   NodeKind = "gate"

	EdgeLinear   EdgeMode = "linear"
	EdgeChoice   EdgeMode = "choice"
	EdgeParallel EdgeMode = "parallel"
	EdgeOptional EdgeMode = "optional"
)

// Graph is the normalized, deterministic browser and generator projection of
// one validated lifecycle manifest. Slice order is source order and therefore
// part of both layout and FlowRevision.
type Graph struct {
	Schema string `json:"schema"`
	Flows  []Flow `json:"flows"`
}

type Flow struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Entry string `json:"entry"`
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

type Node struct {
	ID            string   `json:"id"`
	Kind          NodeKind `json:"kind"`
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Operation     string   `json:"operation,omitempty"`
	DecisionPoint string   `json:"decision_point,omitempty"`
	Safeguard     string   `json:"safeguard,omitempty"`
	Editable      bool     `json:"editable"`
	Ordinal       int      `json:"ordinal,omitempty"`
}

type Edge struct {
	From    string   `json:"from"`
	To      string   `json:"to"`
	Mode    EdgeMode `json:"mode"`
	Outcome string   `json:"outcome,omitempty"`
	Label   string   `json:"label,omitempty"`
	Loop    bool     `json:"loop"`
}

type manifestDocument struct {
	Schema string         `yaml:"schema"`
	Flows  []manifestFlow `yaml:"flows"`
}

type manifestFlow struct {
	ID    string         `yaml:"id"`
	Title string         `yaml:"title"`
	Entry string         `yaml:"entry"`
	Nodes []manifestNode `yaml:"nodes"`
	Edges []manifestEdge `yaml:"edges"`
}

type manifestNode struct {
	ID            string   `yaml:"id"`
	Kind          NodeKind `yaml:"kind"`
	Title         string   `yaml:"title"`
	Description   string   `yaml:"description"`
	Operation     string   `yaml:"operation,omitempty"`
	DecisionPoint string   `yaml:"decision_point,omitempty"`
	Safeguard     string   `yaml:"safeguard,omitempty"`
	Editable      *bool    `yaml:"editable,omitempty"`
	Ordinal       *int     `yaml:"ordinal,omitempty"`
}

type manifestEdge struct {
	From    string   `yaml:"from"`
	To      string   `yaml:"to"`
	Mode    EdgeMode `yaml:"mode"`
	Outcome string   `yaml:"outcome,omitempty"`
	Label   string   `yaml:"label,omitempty"`
	Loop    bool     `yaml:"loop,omitempty"`
}

var safeIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)

var knownFlowIDs = map[string]struct{}{
	"review":         {},
	"implementation": {},
}

var canonicalFlowOrder = [...]string{"review", "implementation"}

var knownDecisionPoints, decisionPointOrdinals = func() (map[string]struct{}, map[string]int) {
	known := make(map[string]struct{})
	ordinals := make(map[string]int)
	for _, point := range prlifecycle.DecisionPoints() {
		known[point.ID] = struct{}{}
		ordinals[point.ID] = point.Ordinal
	}
	return known, ordinals
}()

var knownOperations = map[string]struct{}{
	"github.issue.create":                  {},
	"github.review.publish":                {},
	"github.review.requested":              {},
	"github.branch.push":                   {},
	"pr.charter.define":                    {},
	"pr.charter.revise":                    {},
	"pr.correction.record":                 {},
	"pr.deferred.group":                    {},
	"github.issue.link":                    {},
	"pr.finding.classify_automatic":        {},
	"pr.finding.dismiss":                   {},
	"pr.finding.classification.route":      {},
	"pr.finding.keep_in_scope":             {},
	"pr.finding.scope.assess":              {},
	"pr.guidance.save":                     {},
	"pr.implementation.completion.audit":   {},
	"pr.implementation.completion.route":   {},
	"pr.implementation.branch.queue":       {},
	"pr.implementation.candidate.finalize": {},
	"pr.implementation.finish":             {},
	"pr.implementation.findings.load":      {},
	"pr.implementation.final_scope.check":  {},
	"pr.implementation.gates.join":         {},
	"pr.implementation.gates.start":        {},
	"pr.implementation.ownership.check":    {},
	"pr.implementation.result.handle":      {},
	"pr.implementation.resume":             {},
	"pr.implementation.run":                {},
	"pr.implementation.scope.audit":        {},
	"pr.implementation.scope.remove_defer": {},
	"pr.implementation.stop":               {},
	"pr.implementation.validation.repair":  {},
	"pr.implementation.validate":           {},
	"pr.publication.resolve":               {},
	"pr.publication.retry":                 {},
	"pr.review.finish":                     {},
	"pr.review.findings.select":            {},
	"pr.review.invoke":                     {},
	"pr.review.result.handle":              {},
	"pr.review.run":                        {},
	"pr.workspace.track":                   {},
}

var knownOutcomes = map[string]struct{}{
	"ambiguous":          {},
	"assume_failed":      {},
	"clear":              {},
	"complete":           {},
	"confirmed":          {},
	"create":             {},
	"deferred":           {},
	"direct":             {},
	"dismissed":          {},
	"existing":           {},
	"failed":             {},
	"findings":           {},
	"first_scope":        {},
	"fixable":            {},
	"hard_stop":          {},
	"in_scope":           {},
	"invalid_definition": {},
	"missing":            {},
	"no_findings":        {},
	"non_owned":          {},
	"owned":              {},
	"passed":             {},
	"policy":             {},
	"read_only":          {},
	"remove_code":        {},
	"reobserve":          {},
	"revise":             {},
	"revise_scope":       {},
	"revised":            {},
	"safe":               {},
	"still_unknown":      {},
	"stop":               {},
	"unknown":            {},
	"unreliable":         {},
}

var knownSafeguards = map[string]struct{}{
	"pr.scope.hard": {},
}

// Parse strictly decodes, validates, normalizes, and revision-stamps one
// lifecycle manifest. Unknown fields, aliases, duplicate keys, extra YAML
// documents, and noncanonical graph semantics fail closed.
func Parse(data []byte) (Graph, string, error) {
	if len(data) == 0 {
		return Graph{}, "", errors.New("lifecycle flow manifest is empty")
	}
	if len(data) > MaxManifestBytes {
		return Graph{}, "", fmt.Errorf("lifecycle flow manifest exceeds %d bytes", MaxManifestBytes)
	}
	if err := inspectYAMLStructure(data); err != nil {
		return Graph{}, "", err
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var document manifestDocument
	if err := decoder.Decode(&document); err != nil {
		return Graph{}, "", fmt.Errorf("decode lifecycle flow manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Graph{}, "", errors.New("lifecycle flow manifest contains multiple YAML documents")
		}
		return Graph{}, "", fmt.Errorf("decode lifecycle flow manifest: %w", err)
	}

	graph, err := normalizeAndValidate(document)
	if err != nil {
		return Graph{}, "", err
	}
	encoded, err := json.Marshal(graph)
	if err != nil {
		return Graph{}, "", fmt.Errorf("encode lifecycle flow graph: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return graph, "sha256:" + hex.EncodeToString(digest[:]), nil
}

func inspectYAMLStructure(data []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode lifecycle flow YAML: %w", err)
	}
	var trailing yaml.Node
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("lifecycle flow manifest contains multiple YAML documents")
		}
		return fmt.Errorf("decode lifecycle flow YAML: %w", err)
	}
	return inspectYAMLNode(&document, "document")
}

func inspectYAMLNode(node *yaml.Node, path string) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return fmt.Errorf("lifecycle flow manifest %s uses YAML aliases or anchors", path)
	}
	switch node.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for index, child := range node.Content {
			if err := inspectYAMLNode(child, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		seen := make(map[string]struct{}, len(node.Content)/2)
		for index := 0; index+1 < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "" {
				return fmt.Errorf("lifecycle flow manifest %s contains a non-string or empty key", path)
			}
			if key.Value == "<<" {
				return fmt.Errorf("lifecycle flow manifest %s uses a YAML merge key", path)
			}
			if _, exists := seen[key.Value]; exists {
				return fmt.Errorf("lifecycle flow manifest %s contains duplicate key %q", path, key.Value)
			}
			seen[key.Value] = struct{}{}
			if err := inspectYAMLNode(node.Content[index+1], path+"."+key.Value); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeAndValidate(document manifestDocument) (Graph, error) {
	if err := validateDecisionPointCatalog(); err != nil {
		return Graph{}, err
	}
	if document.Schema != SchemaV1 {
		return Graph{}, fmt.Errorf("lifecycle flow schema must be %q", SchemaV1)
	}
	if len(document.Flows) != maxFlows {
		return Graph{}, fmt.Errorf("lifecycle flow manifest must contain exactly %d flows", maxFlows)
	}

	graph := Graph{Schema: SchemaV1, Flows: make([]Flow, 0, len(document.Flows))}
	flowIDs := make(map[string]struct{}, len(document.Flows))
	globalNodeIDs := make(map[string]string)
	declaredDecisionPoints := make(map[string]struct{})
	declaredSafeguards := make(map[string]struct{})
	for flowIndex, rawFlow := range document.Flows {
		path := fmt.Sprintf("flows[%d]", flowIndex)
		if _, known := knownFlowIDs[rawFlow.ID]; !known || !validID(rawFlow.ID) {
			return Graph{}, fmt.Errorf("%s has unknown flow ID %q", path, rawFlow.ID)
		}
		if _, duplicate := flowIDs[rawFlow.ID]; duplicate {
			return Graph{}, fmt.Errorf("%s duplicates flow ID %q", path, rawFlow.ID)
		}
		flowIDs[rawFlow.ID] = struct{}{}
		if rawFlow.ID != canonicalFlowOrder[flowIndex] {
			return Graph{}, fmt.Errorf("%s must contain flow %q", path, canonicalFlowOrder[flowIndex])
		}
		if err := validateText(path+".title", rawFlow.Title, maxTitleBytes); err != nil {
			return Graph{}, err
		}
		if !validID(rawFlow.Entry) {
			return Graph{}, fmt.Errorf("%s has invalid entry node %q", path, rawFlow.Entry)
		}
		if len(rawFlow.Nodes) == 0 || len(rawFlow.Nodes) > maxNodesPerFlow {
			return Graph{}, fmt.Errorf("%s must contain between 1 and %d nodes", path, maxNodesPerFlow)
		}
		if len(rawFlow.Edges) > maxEdgesPerFlow {
			return Graph{}, fmt.Errorf("%s exceeds %d edges", path, maxEdgesPerFlow)
		}

		flow := Flow{
			ID: rawFlow.ID, Title: rawFlow.Title, Entry: rawFlow.Entry,
			Nodes: make([]Node, 0, len(rawFlow.Nodes)),
			Edges: make([]Edge, 0, len(rawFlow.Edges)),
		}
		nodes := make(map[string]Node, len(rawFlow.Nodes))
		for nodeIndex, rawNode := range rawFlow.Nodes {
			nodePath := fmt.Sprintf("%s.nodes[%d]", path, nodeIndex)
			node, err := validateNode(nodePath, rawNode)
			if err != nil {
				return Graph{}, err
			}
			if _, duplicate := nodes[node.ID]; duplicate {
				return Graph{}, fmt.Errorf("%s duplicates node ID %q", nodePath, node.ID)
			}
			if owner, duplicate := globalNodeIDs[node.ID]; duplicate {
				return Graph{}, fmt.Errorf("%s duplicates node ID %q from flow %q", nodePath, node.ID, owner)
			}
			nodes[node.ID] = node
			globalNodeIDs[node.ID] = rawFlow.ID
			if node.DecisionPoint != "" {
				declaredDecisionPoints[node.DecisionPoint] = struct{}{}
			}
			if node.Safeguard != "" {
				declaredSafeguards[node.Safeguard] = struct{}{}
			}
			flow.Nodes = append(flow.Nodes, node)
		}
		if _, exists := nodes[flow.Entry]; !exists {
			return Graph{}, fmt.Errorf("%s entry node %q does not exist", path, flow.Entry)
		}

		outgoing := make(map[string][]Edge, len(nodes))
		seenEdges := make(map[string]struct{}, len(rawFlow.Edges))
		for edgeIndex, rawEdge := range rawFlow.Edges {
			edgePath := fmt.Sprintf("%s.edges[%d]", path, edgeIndex)
			edge, err := validateEdge(edgePath, rawEdge, nodes)
			if err != nil {
				return Graph{}, err
			}
			edgeIdentity := edge.From + "\x00" + edge.To
			if _, duplicate := seenEdges[edgeIdentity]; duplicate {
				return Graph{}, fmt.Errorf("%s duplicates edge %q -> %q", edgePath, edge.From, edge.To)
			}
			seenEdges[edgeIdentity] = struct{}{}
			outgoing[edge.From] = append(outgoing[edge.From], edge)
			flow.Edges = append(flow.Edges, edge)
		}
		if err := validateForks(path, outgoing); err != nil {
			return Graph{}, err
		}
		if err := validateAcyclicForwardGraph(path, nodes, outgoing); err != nil {
			return Graph{}, err
		}
		if err := validateLoopEdges(path, nodes, outgoing); err != nil {
			return Graph{}, err
		}
		if err := validateReachability(path, flow.Entry, nodes, outgoing); err != nil {
			return Graph{}, err
		}
		graph.Flows = append(graph.Flows, flow)
	}
	for flowID := range knownFlowIDs {
		if _, exists := flowIDs[flowID]; !exists {
			return Graph{}, fmt.Errorf("lifecycle flow manifest is missing flow %q", flowID)
		}
	}
	for decisionPoint := range knownDecisionPoints {
		if _, exists := declaredDecisionPoints[decisionPoint]; !exists {
			return Graph{}, fmt.Errorf("lifecycle flow manifest is missing decision point %q", decisionPoint)
		}
	}
	for safeguard := range knownSafeguards {
		if _, exists := declaredSafeguards[safeguard]; !exists {
			return Graph{}, fmt.Errorf("lifecycle flow manifest is missing safeguard %q", safeguard)
		}
	}
	return graph, nil
}

func validateDecisionPointCatalog() error {
	if len(knownDecisionPoints) != len(decisionPointOrdinals) {
		return errors.New("lifecycle flow decision-point ordinal catalog is incomplete")
	}
	seenOrdinals := make(map[int]string, len(decisionPointOrdinals))
	for decisionPoint := range knownDecisionPoints {
		ordinal, exists := decisionPointOrdinals[decisionPoint]
		if !exists || ordinal < 1 || ordinal > len(knownDecisionPoints) {
			return fmt.Errorf("lifecycle flow decision point %q has an invalid ordinal", decisionPoint)
		}
		if owner, duplicate := seenOrdinals[ordinal]; duplicate {
			return fmt.Errorf(
				"lifecycle flow decision points %q and %q duplicate ordinal %d",
				owner, decisionPoint, ordinal,
			)
		}
		seenOrdinals[ordinal] = decisionPoint
	}
	for decisionPoint := range decisionPointOrdinals {
		if _, exists := knownDecisionPoints[decisionPoint]; !exists {
			return fmt.Errorf("lifecycle flow ordinal catalog contains unknown decision point %q", decisionPoint)
		}
	}
	return nil
}

func validateNode(path string, raw manifestNode) (Node, error) {
	if !validID(raw.ID) {
		return Node{}, fmt.Errorf("%s has invalid node ID %q", path, raw.ID)
	}
	if err := validateText(path+".title", raw.Title, maxTitleBytes); err != nil {
		return Node{}, err
	}
	if err := validateText(path+".description", raw.Description, maxDescription); err != nil {
		return Node{}, err
	}
	node := Node{
		ID: raw.ID, Kind: raw.Kind, Title: raw.Title, Description: raw.Description,
		Operation: raw.Operation, DecisionPoint: raw.DecisionPoint, Safeguard: raw.Safeguard,
	}
	switch raw.Kind {
	case NodeAction:
		if raw.Editable != nil || raw.Ordinal != nil || raw.DecisionPoint != "" || raw.Safeguard != "" {
			return Node{}, fmt.Errorf("%s action may only declare operation metadata", path)
		}
		if _, known := knownOperations[raw.Operation]; !known {
			return Node{}, fmt.Errorf("%s has unknown action operation %q", path, raw.Operation)
		}
	case NodeGate:
		if raw.Editable == nil {
			return Node{}, fmt.Errorf("%s gate must explicitly declare editable", path)
		}
		node.Editable = *raw.Editable
		if raw.Operation != "" {
			return Node{}, fmt.Errorf("%s gate may not declare an action operation", path)
		}
		if node.Editable {
			if raw.Safeguard != "" {
				return Node{}, fmt.Errorf("%s editable gate may not declare a safeguard", path)
			}
			if _, known := knownDecisionPoints[raw.DecisionPoint]; !known {
				return Node{}, fmt.Errorf("%s has unknown decision point %q", path, raw.DecisionPoint)
			}
			if raw.Ordinal == nil {
				return Node{}, fmt.Errorf("%s editable gate must declare its ordinal", path)
			}
			wantOrdinal := decisionPointOrdinals[raw.DecisionPoint]
			if *raw.Ordinal != wantOrdinal {
				return Node{}, fmt.Errorf(
					"%s decision point %q has ordinal %d, want %d",
					path, raw.DecisionPoint, *raw.Ordinal, wantOrdinal,
				)
			}
			node.Ordinal = *raw.Ordinal
		} else {
			if raw.DecisionPoint != "" {
				return Node{}, fmt.Errorf("%s locked gate may not declare a decision point", path)
			}
			if raw.Ordinal != nil {
				return Node{}, fmt.Errorf("%s locked gate may not declare an ordinal", path)
			}
			if _, known := knownSafeguards[raw.Safeguard]; !known {
				return Node{}, fmt.Errorf("%s has unknown safeguard %q", path, raw.Safeguard)
			}
		}
	default:
		return Node{}, fmt.Errorf("%s has unknown node kind %q", path, raw.Kind)
	}
	return node, nil
}

func validateEdge(path string, raw manifestEdge, nodes map[string]Node) (Edge, error) {
	if !validID(raw.From) || !validID(raw.To) {
		return Edge{}, fmt.Errorf("%s has an invalid endpoint", path)
	}
	if raw.From == raw.To {
		return Edge{}, fmt.Errorf("%s may not be a self-edge", path)
	}
	if _, exists := nodes[raw.From]; !exists {
		return Edge{}, fmt.Errorf("%s references missing source node %q", path, raw.From)
	}
	if _, exists := nodes[raw.To]; !exists {
		return Edge{}, fmt.Errorf("%s references missing target node %q", path, raw.To)
	}
	switch raw.Mode {
	case EdgeLinear, EdgeChoice, EdgeParallel, EdgeOptional:
	default:
		return Edge{}, fmt.Errorf("%s has unknown edge mode %q", path, raw.Mode)
	}
	if raw.Outcome != "" && !validID(raw.Outcome) {
		return Edge{}, fmt.Errorf("%s has invalid outcome %q", path, raw.Outcome)
	}
	if raw.Outcome != "" {
		if _, known := knownOutcomes[raw.Outcome]; !known {
			return Edge{}, fmt.Errorf("%s has unknown outcome %q", path, raw.Outcome)
		}
	}
	if raw.Label != "" {
		if err := validateText(path+".label", raw.Label, maxTitleBytes); err != nil {
			return Edge{}, err
		}
		wordCount := len(strings.Fields(raw.Label))
		if wordCount < 1 || wordCount > 2 {
			return Edge{}, fmt.Errorf("%s label must contain one or two words", path)
		}
	}
	return Edge{
		From: raw.From, To: raw.To, Mode: raw.Mode, Outcome: raw.Outcome, Label: raw.Label, Loop: raw.Loop,
	}, nil
}

func validateForks(path string, outgoing map[string][]Edge) error {
	for source, edges := range outgoing {
		if len(edges) == 1 {
			if edges[0].Label != "" || edges[0].Outcome != "" {
				return fmt.Errorf(
					"%s node %q has one outgoing edge, which must be unlabeled and outcome-free",
					path,
					source,
				)
			}
			if edges[0].Mode != EdgeLinear && edges[0].Mode != EdgeOptional {
				return fmt.Errorf(
					"%s node %q has one outgoing edge, which must use mode %q or %q",
					path,
					source,
					EdgeLinear,
					EdgeOptional,
				)
			}
			continue
		}
		modeCounts := make(map[EdgeMode]int, 4)
		labels := make(map[string]struct{}, len(edges))
		outcomes := make(map[string]struct{}, len(edges))
		for _, edge := range edges {
			modeCounts[edge.Mode]++
			if edge.Mode == EdgeChoice && (edge.Label == "" || edge.Outcome == "") {
				return fmt.Errorf("%s choice node %q requires a label and outcome on every edge", path, source)
			}
			if edge.Label == "" {
				return fmt.Errorf("%s split node %q requires a label on every edge", path, source)
			}
			if edge.Mode != EdgeChoice && edge.Outcome != "" {
				return fmt.Errorf("%s %s edge from node %q may not declare an outcome", path, edge.Mode, source)
			}
			foldedLabel := strings.ToLower(edge.Label)
			if _, duplicate := labels[foldedLabel]; duplicate {
				return fmt.Errorf("%s split node %q has duplicate label %q", path, source, edge.Label)
			}
			labels[foldedLabel] = struct{}{}
			if edge.Mode == EdgeChoice {
				if _, duplicate := outcomes[edge.Outcome]; duplicate {
					return fmt.Errorf("%s choice node %q has duplicate outcome %q", path, source, edge.Outcome)
				}
				outcomes[edge.Outcome] = struct{}{}
			}
		}

		choiceCount := modeCounts[EdgeChoice]
		parallelCount := modeCounts[EdgeParallel]
		linearCount := modeCounts[EdgeLinear]
		optionalCount := modeCounts[EdgeOptional]
		switch {
		case choiceCount > 0:
			if choiceCount < 2 || parallelCount != 0 || linearCount != 0 {
				return fmt.Errorf(
					"%s split node %q must use at least two choice edges plus only optional sidecars",
					path,
					source,
				)
			}
		case parallelCount > 0:
			if choiceCount != 0 || linearCount != 0 {
				return fmt.Errorf(
					"%s split node %q may combine parallel edges only with optional sidecars",
					path,
					source,
				)
			}
		case linearCount > 0:
			if linearCount != 1 || optionalCount == 0 {
				return fmt.Errorf(
					"%s split node %q may combine one linear edge only with optional sidecars",
					path,
					source,
				)
			}
		case optionalCount == len(edges):
			// Independent zero-or-more fan-out.
		default:
			return fmt.Errorf("%s split node %q has invalid edge mode composition", path, source)
		}
	}
	return nil
}

func validateAcyclicForwardGraph(path string, nodes map[string]Node, outgoing map[string][]Edge) error {
	const (
		unvisited = iota
		visiting
		done
	)
	state := make(map[string]int, len(nodes))
	var visit func(string) error
	visit = func(nodeID string) error {
		switch state[nodeID] {
		case visiting:
			return fmt.Errorf("%s contains a cycle without an explicit loop edge at node %q", path, nodeID)
		case done:
			return nil
		}
		state[nodeID] = visiting
		for _, edge := range outgoing[nodeID] {
			if edge.Loop {
				continue
			}
			if err := visit(edge.To); err != nil {
				return err
			}
		}
		state[nodeID] = done
		return nil
	}
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func validateLoopEdges(path string, nodes map[string]Node, outgoing map[string][]Edge) error {
	for _, edges := range outgoing {
		for _, edge := range edges {
			if !edge.Loop {
				continue
			}
			if !hasForwardPath(edge.To, edge.From, outgoing) {
				return fmt.Errorf("%s loop edge %q -> %q does not return to an ancestor", path, edge.From, edge.To)
			}
		}
	}
	_ = nodes
	return nil
}

func hasForwardPath(start, target string, outgoing map[string][]Edge) bool {
	queue := []string{start}
	seen := map[string]struct{}{start: {}}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == target {
			return true
		}
		for _, edge := range outgoing[current] {
			if edge.Loop {
				continue
			}
			if _, exists := seen[edge.To]; exists {
				continue
			}
			seen[edge.To] = struct{}{}
			queue = append(queue, edge.To)
		}
	}
	return false
}

func validateReachability(path, entry string, nodes map[string]Node, outgoing map[string][]Edge) error {
	queue := []string{entry}
	reached := map[string]struct{}{entry: {}}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, edge := range outgoing[current] {
			if _, exists := reached[edge.To]; exists {
				continue
			}
			reached[edge.To] = struct{}{}
			queue = append(queue, edge.To)
		}
	}
	if len(reached) == len(nodes) {
		return nil
	}
	unreachable := make([]string, 0, len(nodes)-len(reached))
	for id := range nodes {
		if _, exists := reached[id]; !exists {
			unreachable = append(unreachable, id)
		}
	}
	sort.Strings(unreachable)
	return fmt.Errorf("%s contains unreachable nodes: %s", path, strings.Join(unreachable, ", "))
}

func validID(value string) bool {
	return value == strings.TrimSpace(value) && len(value) <= maxIDBytes && safeIDPattern.MatchString(value)
}

func validateText(path, value string, maxBytes int) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxBytes || !utf8.ValidString(value) {
		return fmt.Errorf("%s must be nonempty, trimmed UTF-8 up to %d bytes", path, maxBytes)
	}
	for _, char := range value {
		if char == '\x00' || unicode.IsControl(char) || unicode.Is(unicode.Cf, char) {
			return fmt.Errorf("%s contains a control or format character", path)
		}
	}
	return nil
}

// KnownDecisionPoints returns a sorted detached copy of the decision-point
// catalog accepted by Parse.
func KnownDecisionPoints() []string {
	return sortedCatalogKeys(knownDecisionPoints)
}

// IsKnownDecisionPoint reports whether value is an editable gate referenced by
// the canonical lifecycle schema.
func IsKnownDecisionPoint(value string) bool {
	_, exists := knownDecisionPoints[value]
	return exists
}

// KnownOperations returns a sorted detached copy of the action-operation
// catalog accepted by Parse.
func KnownOperations() []string {
	return sortedCatalogKeys(knownOperations)
}

// KnownOutcomes returns a sorted detached copy of the exclusive-choice outcome
// catalog accepted by Parse.
func KnownOutcomes() []string {
	return sortedCatalogKeys(knownOutcomes)
}

// KnownSafeguards returns a sorted detached copy of the mandatory-safeguard
// catalog accepted by Parse.
func KnownSafeguards() []string {
	return sortedCatalogKeys(knownSafeguards)
}

func sortedCatalogKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
