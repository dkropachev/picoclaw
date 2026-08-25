package agent

import (
	"fmt"
	"regexp"

	"github.com/sipeed/picoclaw/pkg/tools"
)

func cloneToolPathPatterns(patterns []*regexp.Regexp) []*regexp.Regexp {
	return append([]*regexp.Regexp(nil), patterns...)
}

func cloneWebSearchToolOptions(options tools.WebSearchToolOptions) tools.WebSearchToolOptions {
	cloned := options
	cloned.BraveAPIKeys = append([]string(nil), options.BraveAPIKeys...)
	cloned.TavilyAPIKeys = append([]string(nil), options.TavilyAPIKeys...)
	cloned.KagiAPIKeys = append([]string(nil), options.KagiAPIKeys...)
	cloned.PerplexityAPIKeys = append([]string(nil), options.PerplexityAPIKeys...)
	return cloned
}

func mustToolFactoryFromPrototype(
	prototype tools.Tool,
	traits tools.ToolTraits,
	build tools.ToolBuildFunc,
) tools.ToolFactory {
	factory, err := tools.NewToolFactoryFromPrototype(prototype, traits, build)
	if err != nil {
		panic(fmt.Sprintf("build agent tool factory: %v", err))
	}
	return factory
}

func mustRegisterFactoryBackedTool(
	registry *tools.ToolRegistry,
	live tools.Tool,
	factory tools.ToolFactory,
) {
	if err := registry.RegisterFactoryBacked(live, factory); err != nil {
		panic(fmt.Sprintf("register factory-backed agent tool: %v", err))
	}
}

func mustRegisterFactoryDependency(
	registry *tools.ToolRegistry,
	factory tools.ToolFactory,
) {
	if err := registry.RegisterFactoryDependency(factory); err != nil {
		panic(fmt.Sprintf("register agent tool factory dependency: %v", err))
	}
}

func baseToolFactoryTraits(name string) (tools.ToolTraits, bool) {
	traits := tools.ToolTraits{Sharing: tools.ToolSharingPerOwner}
	switch name {
	case "read_file", "list_dir":
		traits.Risk = tools.ToolRiskReadOnly
		traits.Parallel = tools.ToolParallelSafe
		traits.Idempotency = tools.ToolIdempotencyIdempotent
	case "update_plan":
		traits.Risk = tools.ToolRiskMutation
		traits.Parallel = tools.ToolParallelSerialized
		traits.Idempotency = tools.ToolIdempotencyIdempotent
	case "edit_file", "append_file":
		traits.Risk = tools.ToolRiskMutation
		traits.Parallel = tools.ToolParallelSerialized
		traits.Idempotency = tools.ToolIdempotencyNonIdempotent
	case "write_file", "apply_patch", "git_workspace":
		traits.Risk = tools.ToolRiskDestructive
		traits.Parallel = tools.ToolParallelSerialized
		traits.Idempotency = tools.ToolIdempotencyNonIdempotent
	case "web_search", "web_fetch", "find_skills":
		traits.Risk = tools.ToolRiskNetwork
		traits.Parallel = tools.ToolParallelSafe
		traits.Idempotency = tools.ToolIdempotencyUnknown
	case "i2c", "spi", "serial", "message", "reaction", "send_file", "send_tts":
		traits.Risk = tools.ToolRiskExternalWrite
		traits.Parallel = tools.ToolParallelSerialized
		traits.Idempotency = tools.ToolIdempotencyNonIdempotent
	case "load_image", "view_image":
		traits.Risk = tools.ToolRiskMutation
		traits.Parallel = tools.ToolParallelSerialized
		traits.Idempotency = tools.ToolIdempotencyNonIdempotent
	default:
		return tools.ToolTraits{}, false
	}
	return traits, true
}

func mustBaseToolFactoryTraits(name string) tools.ToolTraits {
	traits, ok := baseToolFactoryTraits(name)
	if !ok {
		panic(fmt.Sprintf("agent tool %q is not in the base factory catalog", name))
	}
	return traits
}
