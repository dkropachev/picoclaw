package agent

import (
	"context"
	"encoding/json"

	"github.com/sipeed/picoclaw/pkg/seahorse"
)

func init() {
	legacy := func(raw json.RawMessage, loop *AgentLoop) (ContextManager, error) {
		return newSeahorseContextManagerWithDependencies(
			context.Background(), raw, loop, testSeahorseContextDependencies(),
		)
	}
	withContext := func(
		ctx context.Context,
		raw json.RawMessage,
		loop *AgentLoop,
	) (ContextManager, error) {
		return newSeahorseContextManagerWithDependencies(
			ctx, raw, loop, testSeahorseContextDependencies(),
		)
	}
	cmRegistryMu.Lock()
	cmRegistry["seahorse"] = contextManagerRegistration{
		factory: legacy, contextFactory: withContext,
	}
	cmRegistryMu.Unlock()
}

func testRuntimeSeahorseEngine(
	config seahorse.Config,
	complete seahorse.CompleteFn,
) (*seahorse.Engine, error) {
	return seahorse.NewOfflineEngine(
		seahorse.OfflineConfig{DatabasePath: ":memory:", Config: config},
		complete,
	)
}

func testSeahorseContextDependencies() seahorseContextDependencies {
	dependencies := defaultSeahorseContextDependencies()
	dependencies.newEngine = testRuntimeSeahorseEngine
	return dependencies
}
