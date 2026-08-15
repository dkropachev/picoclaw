package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/fileutil"
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
	profile := config.DefaultPRLifecycleConfig().GateProfiles[config.DefaultPRLifecycleGateProfileID]
	formats := make(map[string]string)
	for _, flow := range graph.Flows {
		for _, node := range flow.Nodes {
			if node.Kind != lifecycleflow.NodeGate || !node.Editable {
				continue
			}
			workflow, exists := profile.Workflows[node.DecisionPoint]
			if !exists {
				return nil, fmt.Errorf("default gate profile is missing manifest decision point %q", node.DecisionPoint)
			}
			format, err := workflowFormat(workflow.Stages)
			if err != nil {
				return nil, fmt.Errorf("default gate %q: %w", node.DecisionPoint, err)
			}
			formats[node.DecisionPoint] = format
		}
	}
	return formats, nil
}

func workflowFormat(stages []gatetypes.GateStageSpec) (string, error) {
	categories := make(map[string]struct{})
	for _, stage := range stages {
		var category string
		switch stage.Kind {
		case gatetypes.GateZero:
			category = "automatic"
		case gatetypes.GateDeterministic:
			category = "rule"
		case gatetypes.GateAIWorkingContext, gatetypes.GateAIIsolatedContext:
			category = "ai"
		case gatetypes.GateHuman:
			category = "user"
		default:
			return "", fmt.Errorf("unknown stage kind %q", stage.Kind)
		}
		categories[category] = struct{}{}
	}
	if len(categories) == 0 {
		return "needs setup", nil
	}
	if len(categories) > 1 {
		return "mixed", nil
	}
	values := make([]string, 0, len(categories))
	for category := range categories {
		values = append(values, category)
	}
	sort.Strings(values)
	return values[0], nil
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
