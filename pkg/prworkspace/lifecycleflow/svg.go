package lifecycleflow

import (
	"bytes"
	"fmt"
	"html"
	"math"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	svgWidth         = 1920
	svgMargin        = 40
	svgPanelGap      = 28
	svgPanelTop      = 164
	svgPanelPadding  = 22
	svgPanelHeader   = 88
	svgCardHeight    = 118
	svgCardGap       = 16
	svgLayerGap      = 74
	svgMaxLayerCols  = 3
	svgFooterPadding = 42
)

// RenderSVG renders the same normalized graph consumed by the launcher UI as
// a standalone, clickable SVG. Layout is derived presentation state: no x/y
// coordinates are accepted from the lifecycle manifest.
func RenderSVG(graph Graph, revision string, gateFormats map[string]string) ([]byte, error) {
	if graph.Schema != SchemaV1 || len(graph.Flows) == 0 {
		return nil, errorsForSVG("invalid lifecycle flow graph")
	}
	if revision == "" {
		return nil, errorsForSVG("missing lifecycle flow revision")
	}

	panelWidth := float64(svgWidth-svgMargin*2-svgPanelGap) / float64(len(graph.Flows))
	layouts := make([]svgFlowLayout, 0, len(graph.Flows))
	maxPanelHeight := 0.0
	for index, flow := range graph.Flows {
		layout, err := layoutSVGFlow(flow, float64(svgMargin)+float64(index)*(panelWidth+svgPanelGap), panelWidth)
		if err != nil {
			return nil, err
		}
		layouts = append(layouts, layout)
		maxPanelHeight = math.Max(maxPanelHeight, layout.height)
	}
	height := int(math.Ceil(float64(svgPanelTop) + maxPanelHeight + svgFooterPadding))

	var output bytes.Buffer
	fmt.Fprintf(&output, `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-labelledby="title desc" data-source="pkg/prworkspace/lifecycleflow/manifest.yaml" data-flow-schema="%s" data-flow-revision="%s">
  <title id="title">PicoClaw pull-request lifecycle gates</title>
  <desc id="desc">Review and implementation action flow generated from the canonical PR lifecycle YAML manifest. Editable gates link to their gate-profile editor; locked safeguards are not interactive.</desc>
  <defs>
    <linearGradient id="page-bg" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#07111f"/><stop offset="1" stop-color="#111827"/></linearGradient>
    <marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0L10 5L0 10Z" fill="#94a3b8"/></marker>
    <marker id="loop-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0L10 5L0 10Z" fill="#f59e0b"/></marker>
    <filter id="shadow" x="-20%%" y="-30%%" width="140%%" height="170%%"><feDropShadow dx="0" dy="4" stdDeviation="5" flood-color="#020617" flood-opacity="0.42"/></filter>
    <style>
      text { font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; fill: #e5edf7; }
      .page-title { font-size: 34px; font-weight: 800; }
      .page-copy { font-size: 15px; fill: #9fb0c5; }
      .revision { font: 11px ui-monospace, SFMono-Regular, Menlo, monospace; fill: #74869d; }
      .panel { fill: #0b1728; stroke: #334155; stroke-width: 1.5; }
      .panel-title { font-size: 22px; font-weight: 800; }
      .panel-copy { font-size: 12px; fill: #8ea2ba; }
      .node { filter: url(#shadow); stroke-width: 1.7; }
      .action { fill: #172033; stroke: #64748b; }
      .gate { fill: #102b48; stroke: #38bdf8; }
      .safeguard { fill: #3a1821; stroke: #fb7185; stroke-width: 2.2; }
      .gate-link { cursor: pointer; outline: none; }
      .gate-link:hover .gate, .gate-link:focus .gate { fill: #153958; stroke: #67e8f9; stroke-width: 3; }
      .node-kind { font-size: 9px; font-weight: 900; letter-spacing: 1px; }
      .action-kind { fill: #a8b5c7; }
      .gate-kind { fill: #67e8f9; }
      .safeguard-kind { fill: #fda4af; }
      .node-title { font-size: 14px; font-weight: 800; }
      .node-copy { font-size: 11px; fill: #a9b8ca; }
      .format { font-size: 8.5px; font-weight: 900; text-anchor: end; fill: #dbeafe; }
      .edge { fill: none; stroke: #94a3b8; stroke-width: 2; marker-end: url(#arrow); }
      .edge-parallel { stroke: #2dd4bf; stroke-width: 2.6; }
      .edge-optional { stroke: #c084fc; stroke-dasharray: 4 5; }
      .loop { fill: none; stroke: #f59e0b; stroke-width: 2; stroke-dasharray: 7 6; marker-end: url(#loop-arrow); }
      .edge-label-bg { fill: #111c2c; stroke: #475569; }
      .edge-label { font-size: 10px; font-weight: 800; text-anchor: middle; fill: #dbe7f4; }
      .legend { font-size: 11px; fill: #9fb0c5; }
    </style>
  </defs>
  <rect width="%d" height="%d" fill="url(#page-bg)"/>
  <text x="%d" y="57" class="page-title">PR lifecycle gate flow</text>
  <text x="%d" y="86" class="page-copy">Generated from YAML · actions describe work · gates decide whether and how the flow continues</text>
  <text x="%d" y="112" class="revision">%s</text>
  <g aria-label="Legend" transform="translate(%d 128)">
    <rect width="15" height="15" rx="4" class="action"/><text x="22" y="12" class="legend">Action</text>
    <rect x="82" width="15" height="15" rx="4" class="gate"/><text x="104" y="12" class="legend">Editable gate</text>
    <rect x="208" width="15" height="15" rx="4" class="safeguard"/><text x="230" y="12" class="legend">Locked safeguard</text>
  </g>
`, svgWidth, height, svgWidth, height, escapeXML(graph.Schema), escapeXML(revision), svgWidth, height, svgMargin, svgMargin, svgMargin, escapeXML(revision), svgMargin)

	for _, layout := range layouts {
		renderSVGFlow(&output, layout, maxPanelHeight, gateFormats)
	}
	output.WriteString("</svg>\n")
	return output.Bytes(), nil
}

type svgPoint struct {
	x      float64
	y      float64
	width  float64
	height float64
}

type svgFlowLayout struct {
	flow      Flow
	x         float64
	width     float64
	height    float64
	positions map[string]svgPoint
}

func layoutSVGFlow(flow Flow, panelX, panelWidth float64) (svgFlowLayout, error) {
	if len(flow.Nodes) == 0 {
		return svgFlowLayout{}, errorsForSVG("flow %q has no nodes", flow.ID)
	}
	index := make(map[string]int, len(flow.Nodes))
	indegree := make(map[string]int, len(flow.Nodes))
	outgoing := make(map[string][]Edge, len(flow.Nodes))
	for nodeIndex, node := range flow.Nodes {
		if _, duplicate := index[node.ID]; duplicate {
			return svgFlowLayout{}, errorsForSVG("flow %q duplicates node %q", flow.ID, node.ID)
		}
		index[node.ID] = nodeIndex
		indegree[node.ID] = 0
	}
	for _, edge := range flow.Edges {
		if _, exists := index[edge.From]; !exists {
			return svgFlowLayout{}, errorsForSVG("flow %q edge references missing node %q", flow.ID, edge.From)
		}
		if _, exists := index[edge.To]; !exists {
			return svgFlowLayout{}, errorsForSVG("flow %q edge references missing node %q", flow.ID, edge.To)
		}
		outgoing[edge.From] = append(outgoing[edge.From], edge)
		if !edge.Loop {
			indegree[edge.To]++
		}
	}

	queue := make([]string, 0, len(flow.Nodes))
	for _, node := range flow.Nodes {
		if indegree[node.ID] == 0 {
			queue = append(queue, node.ID)
		}
	}
	ranks := make(map[string]int, len(flow.Nodes))
	visited := 0
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		visited++
		for _, edge := range outgoing[current] {
			if edge.Loop {
				continue
			}
			if candidate := ranks[current] + 1; candidate > ranks[edge.To] {
				ranks[edge.To] = candidate
			}
			indegree[edge.To]--
			if indegree[edge.To] == 0 {
				queue = append(queue, edge.To)
			}
		}
	}
	if visited != len(flow.Nodes) {
		return svgFlowLayout{}, errorsForSVG("flow %q contains an unmarked cycle", flow.ID)
	}

	layers := make(map[int][]Node)
	maxRank := 0
	for _, node := range flow.Nodes {
		rank := ranks[node.ID]
		layers[rank] = append(layers[rank], node)
		maxRank = max(maxRank, rank)
	}
	positions := make(map[string]svgPoint, len(flow.Nodes))
	contentX := panelX + svgPanelPadding
	contentWidth := panelWidth - svgPanelPadding*2
	y := float64(svgPanelTop + svgPanelHeader)
	for rank := 0; rank <= maxRank; rank++ {
		nodes := layers[rank]
		if len(nodes) == 0 {
			continue
		}
		rows := int(math.Ceil(float64(len(nodes)) / svgMaxLayerCols))
		for row := 0; row < rows; row++ {
			start := row * svgMaxLayerCols
			end := min(start+svgMaxLayerCols, len(nodes))
			rowNodes := nodes[start:end]
			cardWidth := math.Min(276, (contentWidth-float64(len(rowNodes)-1)*svgCardGap)/float64(len(rowNodes)))
			rowWidth := float64(len(rowNodes))*cardWidth + float64(len(rowNodes)-1)*svgCardGap
			rowX := contentX + (contentWidth-rowWidth)/2
			for column, node := range rowNodes {
				positions[node.ID] = svgPoint{
					x:     rowX + float64(column)*(cardWidth+svgCardGap),
					y:     y + float64(row)*(svgCardHeight+svgCardGap),
					width: cardWidth, height: svgCardHeight,
				}
			}
		}
		y += float64(rows)*(svgCardHeight+svgCardGap) + svgLayerGap
	}
	height := y - float64(svgPanelTop) - svgLayerGap + svgPanelPadding
	return svgFlowLayout{flow: flow, x: panelX, width: panelWidth, height: height, positions: positions}, nil
}

func renderSVGFlow(output *bytes.Buffer, layout svgFlowLayout, panelHeight float64, gateFormats map[string]string) {
	fmt.Fprintf(output, "  <g data-flow-id=\"%s\">\n", escapeXML(layout.flow.ID))
	fmt.Fprintf(output, "    <rect x=\"%.1f\" y=\"%d\" width=\"%.1f\" height=\"%.1f\" rx=\"18\" class=\"panel\"/>\n", layout.x, svgPanelTop, layout.width, panelHeight)
	fmt.Fprintf(output, "    <text x=\"%.1f\" y=\"%d\" class=\"panel-title\">%s</text>\n", layout.x+svgPanelPadding, svgPanelTop+34, escapeXML(layout.flow.Title))
	fmt.Fprintf(output, "    <text x=\"%.1f\" y=\"%d\" class=\"panel-copy\">Entry · %s</text>\n", layout.x+svgPanelPadding, svgPanelTop+58, escapeXML(layout.flow.Entry))

	for _, edge := range layout.flow.Edges {
		from := layout.positions[edge.From]
		to := layout.positions[edge.To]
		if edge.Loop {
			loopX := layout.x + 9
			path := fmt.Sprintf("M %.1f %.1f C %.1f %.1f, %.1f %.1f, %.1f %.1f", from.x, from.y+from.height/2, loopX, from.y+from.height/2, loopX, to.y+to.height/2, to.x, to.y+to.height/2)
			fmt.Fprintf(output, "    <path d=\"%s\" class=\"loop edge-%s\" data-flow-edge=\"loop\" data-edge-mode=\"%s\"/>\n", path, escapeXML(string(edge.Mode)), escapeXML(string(edge.Mode)))
			renderSVGEdgeLabel(output, edge, loopX+42, (from.y+to.y+to.height)/2)
			continue
		}
		startX, startY := from.x+from.width/2, from.y+from.height
		endX, endY := to.x+to.width/2, to.y
		middleY := (startY + endY) / 2
		path := fmt.Sprintf("M %.1f %.1f C %.1f %.1f, %.1f %.1f, %.1f %.1f", startX, startY, startX, middleY, endX, middleY, endX, endY)
		fmt.Fprintf(output, "    <path d=\"%s\" class=\"edge edge-%s\" data-flow-edge=\"forward\" data-edge-mode=\"%s\"/>\n", path, escapeXML(string(edge.Mode)), escapeXML(string(edge.Mode)))
		renderSVGEdgeLabel(output, edge, (startX+endX)/2, middleY)
	}

	for _, node := range layout.flow.Nodes {
		renderSVGNode(output, layout.flow.ID, node, layout.positions[node.ID], gateFormats[node.DecisionPoint])
	}
	output.WriteString("  </g>\n")
}

func renderSVGEdgeLabel(output *bytes.Buffer, edge Edge, x, y float64) {
	if edge.Label == "" {
		return
	}
	width := math.Max(58, float64(utf8.RuneCountInString(edge.Label))*7+20)
	fmt.Fprintf(output, "    <g data-edge-outcome=\"%s\"><rect x=\"%.1f\" y=\"%.1f\" width=\"%.1f\" height=\"22\" rx=\"11\" class=\"edge-label-bg\"/><text x=\"%.1f\" y=\"%.1f\" class=\"edge-label\">%s</text></g>\n", escapeXML(edge.Outcome), x-width/2, y-14, width, x, y+1, escapeXML(edge.Label))
}

func renderSVGNode(output *bytes.Buffer, flowID string, node Node, point svgPoint, gateFormat string) {
	className := "action"
	kindLabel := "ACTION"
	kindClass := "action-kind"
	if node.Kind == NodeGate {
		className, kindLabel, kindClass = "gate", "GATE", "gate-kind"
		if !node.Editable {
			className, kindLabel, kindClass = "safeguard", "LOCKED GATE", "safeguard-kind"
		}
	}
	if gateFormat == "" && node.Kind == NodeGate && node.Editable {
		gateFormat = "needs setup"
	}

	if node.Kind == NodeGate && node.Editable {
		href := "/pull-requests/workflow-configurations/default?flow=" + url.QueryEscape(flowID) + "&gate=" + url.QueryEscape(node.DecisionPoint)
		fmt.Fprintf(output, "    <a class=\"gate-link\" href=\"%s\" target=\"_top\" aria-label=\"Edit %s\" data-decision-point=\"%s\">\n", escapeXML(href), escapeXML(node.Title), escapeXML(node.DecisionPoint))
	} else {
		fmt.Fprintf(output, "    <g data-flow-kind=\"%s\" data-node-id=\"%s\"%s>\n", escapeXML(string(node.Kind)), escapeXML(node.ID), optionalSVGAttribute("data-safeguard", node.Safeguard))
	}
	fmt.Fprintf(output, "      <rect x=\"%.1f\" y=\"%.1f\" width=\"%.1f\" height=\"%.1f\" rx=\"12\" class=\"node %s\"/>\n", point.x, point.y, point.width, point.height, className)
	fmt.Fprintf(output, "      <text x=\"%.1f\" y=\"%.1f\" class=\"node-kind %s\">%s</text>\n", point.x+14, point.y+20, kindClass, kindLabel)
	if gateFormat != "" {
		fmt.Fprintf(output, "      <text x=\"%.1f\" y=\"%.1f\" class=\"format\">%s</text>\n", point.x+point.width-14, point.y+20, escapeXML(strings.ToUpper(gateFormat)))
	}
	renderSVGLines(output, node.Title, point.x+14, point.y+44, "node-title", max(18, int(point.width/8)), 17, 2)
	renderSVGLines(output, node.Description, point.x+14, point.y+79, "node-copy", max(24, int(point.width/6.6)), 15, 2)
	if node.Kind == NodeGate && node.Editable {
		output.WriteString("    </a>\n")
	} else {
		output.WriteString("    </g>\n")
	}
}

func renderSVGLines(output *bytes.Buffer, value string, x, y float64, className string, maxRunes int, lineHeight float64, limit int) {
	lines := wrapSVGText(value, maxRunes, limit)
	fmt.Fprintf(output, "      <text x=\"%.1f\" y=\"%.1f\" class=\"%s\">", x, y, className)
	for index, line := range lines {
		dy := 0.0
		if index > 0 {
			dy = lineHeight
		}
		fmt.Fprintf(output, "<tspan x=\"%.1f\" dy=\"%.1f\">%s</tspan>", x, dy, escapeXML(line))
	}
	output.WriteString("</text>\n")
}

func wrapSVGText(value string, maxRunes, limit int) []string {
	words := strings.Fields(value)
	if len(words) == 0 {
		return []string{""}
	}
	lines := make([]string, 0, limit)
	for _, word := range words {
		if len(lines) == 0 {
			lines = append(lines, word)
			continue
		}
		candidate := lines[len(lines)-1] + " " + word
		if utf8.RuneCountInString(candidate) <= maxRunes {
			lines[len(lines)-1] = candidate
			continue
		}
		if len(lines) == limit {
			last := []rune(lines[len(lines)-1])
			if len(last) >= maxRunes-1 {
				last = last[:maxRunes-1]
			}
			lines[len(lines)-1] = strings.TrimSpace(string(last)) + "…"
			break
		}
		lines = append(lines, word)
	}
	return lines
}

func optionalSVGAttribute(name, value string) string {
	if value == "" {
		return ""
	}
	return fmt.Sprintf(" %s=\"%s\"", name, escapeXML(value))
}

func escapeXML(value string) string {
	return html.EscapeString(value)
}

func errorsForSVG(format string, values ...any) error {
	return fmt.Errorf("render lifecycle flow SVG: "+format, values...)
}

// StableNodeIDs is useful to generators and tests that need a deterministic
// inventory without reproducing manifest traversal rules.
func StableNodeIDs(graph Graph) []string {
	ids := make([]string, 0)
	for _, flow := range graph.Flows {
		for _, node := range flow.Nodes {
			ids = append(ids, node.ID)
		}
	}
	sort.Strings(ids)
	return ids
}
