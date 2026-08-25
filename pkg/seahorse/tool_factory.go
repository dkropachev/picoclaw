package seahorse

import (
	"fmt"

	"github.com/sipeed/picoclaw/pkg/tools"
)

const (
	ShortGrepToolName   = "short_grep"
	ShortExpandToolName = "short_expand"
)

// NewGrepToolWithFactory returns one compatibility wrapper plus a per-owner
// factory bound to the same borrowed retrieval engine. Neither the wrapper nor
// its owner registry owns or closes retrieval or its database.
func NewGrepToolWithFactory(
	retrieval *RetrievalEngine,
) (*GrepTool, tools.ToolFactory, error) {
	if err := validateToolRetrievalEngine(retrieval); err != nil {
		return nil, nil, fmt.Errorf("build %s factory: %w", ShortGrepToolName, err)
	}
	live := NewGrepTool(retrieval)
	factory, err := tools.NewToolFactoryFromPrototype(
		live,
		retrievalToolTraits(),
		func(tools.ToolBuildContext) (tools.Tool, error) {
			return NewGrepTool(retrieval), nil
		},
	)
	if err != nil {
		return nil, nil, fmt.Errorf("build %s factory: %w", ShortGrepToolName, err)
	}
	return live, factory, nil
}

// NewExpandToolWithFactory returns one compatibility wrapper plus a per-owner
// factory bound to the same borrowed retrieval engine. Its build closure does
// not derive authority or dependencies from ToolBuildContext.
func NewExpandToolWithFactory(
	retrieval *RetrievalEngine,
) (*ExpandTool, tools.ToolFactory, error) {
	if err := validateToolRetrievalEngine(retrieval); err != nil {
		return nil, nil, fmt.Errorf("build %s factory: %w", ShortExpandToolName, err)
	}
	live := NewExpandTool(retrieval)
	factory, err := tools.NewToolFactoryFromPrototype(
		live,
		retrievalToolTraits(),
		func(tools.ToolBuildContext) (tools.Tool, error) {
			return NewExpandTool(retrieval), nil
		},
	)
	if err != nil {
		return nil, nil, fmt.Errorf("build %s factory: %w", ShortExpandToolName, err)
	}
	return live, factory, nil
}

func retrievalToolTraits() tools.ToolTraits {
	return tools.ToolTraits{
		Risk:        tools.ToolRiskReadOnly,
		Parallel:    tools.ToolParallelSafe,
		Idempotency: tools.ToolIdempotencyIdempotent,
		Sharing:     tools.ToolSharingPerOwner,
	}
}

func validateToolRetrievalEngine(retrieval *RetrievalEngine) error {
	if retrieval == nil {
		return fmt.Errorf("retrieval engine is nil")
	}
	if retrieval.store == nil {
		return fmt.Errorf("retrieval engine store is nil")
	}
	if retrieval.store.db == nil {
		return fmt.Errorf("retrieval engine database is nil")
	}
	return nil
}
