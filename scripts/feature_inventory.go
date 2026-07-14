//go:build featuretools

package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "feature inventory: %v\n", err)
		os.Exit(1)
	}
	surfaces, err := discoverFeatureSurfaces(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "feature inventory: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("# Feature Surface Inventory")
	fmt.Println()
	fmt.Println("Generated from current repository code. Use this as audit input; `docs/features/` remains the source of truth.")

	currentKind := ""
	for _, surface := range surfaces {
		if surface.Kind != currentKind {
			currentKind = surface.Kind
			fmt.Println()
			fmt.Printf("## %s\n\n", titleForKind(currentKind))
			fmt.Println("| Surface | Source |")
			fmt.Println("| --- | --- |")
		}
		fmt.Printf("| `%s` | `%s` |\n", escapePipe(surface.ID), escapePipe(surface.Source))
	}
}

func titleForKind(kind string) string {
	switch kind {
	case "CHANNEL":
		return "Channels"
	case "CLI":
		return "CLI Commands"
	case "CONFIG":
		return "Config Fields"
	case "EVENT":
		return "Runtime Events"
	case "HTTP":
		return "HTTP Routes"
	case "INTEGRATION":
		return "Integration Suites"
	case "TEST":
		return "Tests"
	case "TOOL":
		return "Tools"
	default:
		return kind
	}
}

func escapePipe(s string) string {
	return strings.ReplaceAll(s, "|", `\|`)
}
