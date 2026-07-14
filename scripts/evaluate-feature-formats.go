//go:build featuretools

package main

import (
	"fmt"
	"sort"
	"strings"
)

type formatVariant struct {
	Name     string
	Sections []string
	Notes    string
}

type formatMetric struct {
	Name   string
	Weight int
	Terms  []string
}

type formatScore struct {
	Variant formatVariant
	Score   int
	Hits    map[string]int
	Order   int
}

var regenerationMetrics = []formatMetric{
	{Name: "behavior contracts", Weight: 18, Terms: []string{"requirement", "contract", "behavior", "acceptance"}},
	{Name: "interface precision", Weight: 14, Terms: []string{"interface", "api", "cli", "config", "schema", "input", "output"}},
	{Name: "state and data model", Weight: 14, Terms: []string{"state", "data", "storage", "persistence", "model"}},
	{Name: "algorithm and ordering", Weight: 14, Terms: []string{"algorithm", "ordering", "sequence", "flow", "decision"}},
	{Name: "failure semantics", Weight: 10, Terms: []string{"failure", "error", "edge"}},
	{Name: "test mapping", Weight: 10, Terms: []string{"test", "evidence", "acceptance"}},
	{Name: "implementation anchors", Weight: 8, Terms: []string{"implementation", "anchor", "file", "function"}},
	{Name: "cross-feature behavior", Weight: 6, Terms: []string{"cross-feature", "dependency", "interaction"}},
	{Name: "machine ownership", Weight: 4, Terms: []string{"ownership", "owns", "surface"}},
	{Name: "prompt efficiency", Weight: 2, Terms: []string{"summary", "goal"}},
}

var formatVariants = []formatVariant{
	{
		Name: "current-requirements-table",
		Sections: []string{
			"Feature ID", "Behavior Summary", "Requirements", "Auxiliary Interfaces",
			"Cross-Feature Behavior", "Failure And Edge Cases", "Acceptance Evidence",
			"Implementation Anchors",
		},
		Notes: "Current broad requirement table with evidence and surface ownership.",
	},
	{
		Name: "user-story-acceptance",
		Sections: []string{
			"Feature ID", "Actors", "User Stories", "Acceptance Criteria",
			"Out Of Scope", "Interfaces", "Tests",
		},
		Notes: "Product-management format optimized for intent over code shape.",
	},
	{
		Name: "api-first-contract",
		Sections: []string{
			"Feature ID", "Endpoints And Commands", "Request Schemas", "Response Schemas",
			"Config Schema", "Errors", "Examples", "Tests",
		},
		Notes: "Good for HTTP/CLI surfaces, weak for internal runtime behavior.",
	},
	{
		Name: "behavior-driven-scenarios",
		Sections: []string{
			"Feature ID", "Background", "Given When Then Scenarios", "Examples",
			"Edge Scenarios", "Acceptance Tests",
		},
		Notes: "Strong at observable examples, weak at data structures and implementation anchors.",
	},
	{
		Name: "code-reconstruction-blueprint",
		Sections: []string{
			"Feature ID", "Reconstruction Goal", "Public Interfaces", "Types And Data Model",
			"State And Persistence", "Algorithms And Ordering", "Errors And Edge Cases",
			"Test Matrix", "Implementation Anchors", "Surface Ownership",
		},
		Notes: "Optimized for a coding agent recreating current implementation.",
	},
	{
		Name: "contract-matrix",
		Sections: []string{
			"Feature ID", "Behavior Contract Matrix", "Inputs", "Outputs", "State Changes",
			"Failures", "Cross-Feature Contracts", "Acceptance Evidence", "Ownership",
		},
		Notes: "Dense, machine-checkable behavior table with strong traceability.",
	},
	{
		Name: "test-first-spec",
		Sections: []string{
			"Feature ID", "Acceptance Tests", "Unit Test Contracts", "Integration Test Contracts",
			"Required Fixtures", "Implementation Notes", "Regression Risks",
		},
		Notes: "Good for proving behavior, weaker for initial implementation design.",
	},
	{
		Name: "domain-model-spec",
		Sections: []string{
			"Feature ID", "Domain Concepts", "Entities", "State Transitions",
			"Invariants", "Interfaces", "Failures", "Tests",
		},
		Notes: "Good for state-heavy features, less direct for tools and channels.",
	},
	{
		Name: "agent-prompt-pack",
		Sections: []string{
			"Feature ID", "Task Prompt", "Must Implement", "Do Not Implement",
			"Reference Behavior", "Examples", "Tests To Pass", "Files To Create Or Edit",
		},
		Notes: "Direct coding prompt, but less stable as long-term documentation.",
	},
	{
		Name: "hybrid-reconstruction-contract",
		Sections: []string{
			"Feature ID", "Behavior Summary", "Reconstruction Notes", "Requirements",
			"Data And State Model", "Auxiliary Interfaces", "Algorithms And Ordering",
			"Cross-Feature Behavior", "Failure And Edge Cases", "Acceptance Evidence",
			"Implementation Anchors", "Surface Ownership",
		},
		Notes: "Keeps current docs compatible while adding code-regeneration slots.",
	},
}

var topFormatIterations = []formatVariant{
	{
		Name: "hybrid-reconstruction-contract-v2",
		Sections: []string{
			"Feature ID", "Behavior Summary", "Reconstruction Notes", "Similarity Target",
			"Requirements Behavior Contract Matrix", "Data And State Model",
			"Auxiliary Interfaces", "Algorithms And Ordering", "Cross-Feature Behavior",
			"Failure And Edge Cases", "Acceptance Evidence", "Implementation Anchors",
			"Surface Ownership",
		},
		Notes: "Adds explicit similarity target and treats requirements as a contract matrix while preserving current docs/linter shape.",
	},
	{
		Name: "code-reconstruction-blueprint-v2",
		Sections: []string{
			"Feature ID", "Reconstruction Goal", "Similarity Target", "Public Interfaces",
			"Input Output Schemas", "Types And Data Model", "State And Persistence",
			"Algorithms And Ordering", "Errors And Edge Cases", "Test Matrix",
			"Cross-Feature Contracts", "Implementation Anchors", "Surface Ownership",
		},
		Notes: "Adds missing cross-feature and ownership slots to the blueprint.",
	},
	{
		Name: "contract-matrix-v2",
		Sections: []string{
			"Feature ID", "Behavior Summary", "Behavior Contract Matrix",
			"Inputs", "Outputs", "State Changes", "Algorithms And Ordering",
			"Data And State Model", "Failures", "Cross-Feature Contracts",
			"Acceptance Evidence", "Implementation Anchors", "Surface Ownership",
		},
		Notes: "Adds implementation anchors, algorithm ordering, and data model to the dense matrix.",
	},
}

func main() {
	scores := scoreFormats(formatVariants)
	fmt.Println("# Feature Format Evaluation")
	fmt.Println()
	fmt.Println("| Rank | Format | Score | Notes |")
	fmt.Println("| --- | --- | ---: | --- |")
	for i, score := range scores {
		fmt.Printf("| %d | `%s` | %d | %s |\n", i+1, score.Variant.Name, score.Score, score.Variant.Notes)
	}

	fmt.Println()
	fmt.Println("## Metric Weights")
	fmt.Println()
	fmt.Println("| Metric | Weight |")
	fmt.Println("| --- | ---: |")
	for _, metric := range regenerationMetrics {
		fmt.Printf("| %s | %d |\n", metric.Name, metric.Weight)
	}

	fmt.Println()
	fmt.Println("## Top 3 Iterations")
	fmt.Println()
	fmt.Println("| Rank | Format | Score | Notes |")
	fmt.Println("| --- | --- | ---: | --- |")
	for i, score := range scoreFormats(topFormatIterations) {
		fmt.Printf("| %d | `%s` | %d | %s |\n", i+1, score.Variant.Name, score.Score, score.Variant.Notes)
	}
}

func scoreFormats(variants []formatVariant) []formatScore {
	var scores []formatScore
	for i, variant := range variants {
		text := strings.ToLower(strings.Join(variant.Sections, " ") + " " + variant.Notes)
		score := formatScore{
			Variant: variant,
			Hits:    make(map[string]int),
			Order:   i,
		}
		for _, metric := range regenerationMetrics {
			hits := 0
			for _, term := range metric.Terms {
				if strings.Contains(text, term) {
					hits++
				}
			}
			if hits > 0 {
				score.Score += metric.Weight
				score.Hits[metric.Name] = hits
			}
		}
		scores = append(scores, score)
	}
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].Score != scores[j].Score {
			return scores[i].Score > scores[j].Score
		}
		return scores[i].Order < scores[j].Order
	})
	return scores
}
