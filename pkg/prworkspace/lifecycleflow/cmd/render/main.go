package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sipeed/picoclaw/pkg/fileutil"
	"github.com/sipeed/picoclaw/pkg/prworkspace"
	"github.com/sipeed/picoclaw/pkg/prworkspace/lifecycleflow"
	"github.com/sipeed/picoclaw/pkg/workflows/gatetypes"
)

const (
	defaultSVGPath  = "../../../docs/architecture/pr-lifecycle-gates.svg"
	defaultJSONPath = "../../../web/frontend/tests/fixtures/pr-lifecycle-flow.json"
)

func main() {
	svgPath := flag.String("svg", defaultSVGPath, "output path for the standalone SVG")
	jsonPath := flag.String("json", defaultJSONPath, "output path for the browser-test graph fixture")
	flag.Parse()

	graph, revision := lifecycleflow.Default()
	formats, err := defaultGateFormats(graph)
	if err != nil {
		fatal(err)
	}
	svg, err := lifecycleflow.RenderSVG(graph, revision, formats)
	if err != nil {
		fatal(err)
	}
	fixture, err := json.MarshalIndent(struct {
		Flow         lifecycleflow.Graph `json:"flow"`
		FlowRevision string              `json:"flow_revision"`
	}{Flow: graph, FlowRevision: revision}, "", "  ")
	if err != nil {
		fatal(fmt.Errorf("encode lifecycle flow fixture: %w", err))
	}
	fixture = append(fixture, '\n')
	if err := writeAtomic(*svgPath, svg); err != nil {
		fatal(err)
	}
	if err := writeAtomic(*jsonPath, fixture); err != nil {
		fatal(err)
	}
}

func defaultGateFormats(graph lifecycleflow.Graph) (map[string]string, error) {
	catalog, err := prworkspace.PRLifecycleGateCatalog()
	if err != nil {
		return nil, err
	}
	byDecisionPoint := make(map[string]prworkspace.PRLifecycleGateCatalogEntry, len(catalog))
	for _, entry := range catalog {
		if _, exists := byDecisionPoint[entry.DecisionPoint]; !exists {
			byDecisionPoint[entry.DecisionPoint] = entry
		}
	}
	formats := make(map[string]string)
	for _, flow := range graph.Flows {
		for _, node := range flow.Nodes {
			if node.Kind != lifecycleflow.NodeGate || !node.Editable {
				continue
			}
			entry, exists := byDecisionPoint[node.DecisionPoint]
			if !exists {
				return nil, fmt.Errorf("gate workflow is missing manifest decision point %q", node.DecisionPoint)
			}
			format, err := workflowFormat(entry.Gate.DefaultAction)
			if err != nil {
				return nil, fmt.Errorf("default gate %q: %w", node.DecisionPoint, err)
			}
			formats[node.DecisionPoint] = format
		}
	}
	return formats, nil
}

func workflowFormat(action *gatetypes.GateAction) (string, error) {
	if action == nil {
		return "needs setup", nil
	}
	switch action.Type {
	case gatetypes.GateActionHuman:
		return "human", nil
	case gatetypes.GateActionAI:
		return "ai", nil
	case gatetypes.GateActionDeterministic:
		return "rule", nil
	case gatetypes.GateActionWorkflow:
		return "workflow", nil
	default:
		return "", fmt.Errorf("unknown gate action type %q", action.Type)
	}
}

func writeAtomic(path string, data []byte) error {
	if err := fileutil.WriteFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("write generated artifact %q: %w", path, err)
	}
	return nil
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "pr-lifecycle-flow generator:", strings.TrimSpace(err.Error()))
	os.Exit(1)
}
