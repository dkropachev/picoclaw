package lifecycleflow

import "embed"

//go:embed manifest.yaml
var manifestFS embed.FS

var (
	defaultGraph    Graph
	defaultRevision string
)

func init() {
	graph, revision, err := LoadDefault()
	if err != nil {
		panic("load embedded PR lifecycle flow: " + err.Error())
	}
	defaultGraph = graph
	defaultRevision = revision
}

// LoadDefault reparses the embedded manifest. It is useful to generators and
// validation tests that want an explicit error boundary.
func LoadDefault() (Graph, string, error) {
	data, err := manifestFS.ReadFile("manifest.yaml")
	if err != nil {
		return Graph{}, "", err
	}
	return Parse(data)
}

// Default returns a detached copy of the validated embedded graph and its
// canonical revision. Callers may safely reorder or annotate their copy.
func Default() (Graph, string) {
	return cloneGraph(defaultGraph), defaultRevision
}

func cloneGraph(source Graph) Graph {
	clone := Graph{Schema: source.Schema, Flows: make([]Flow, len(source.Flows))}
	for index, flow := range source.Flows {
		clone.Flows[index] = Flow{
			ID: flow.ID, Title: flow.Title, Entry: flow.Entry,
			Nodes: append([]Node(nil), flow.Nodes...),
			Edges: append([]Edge(nil), flow.Edges...),
		}
	}
	return clone
}
