package agent

import "github.com/sipeed/picoclaw/pkg/seahorse"

func init() {
	// Production binaries do not compile these test-only authorities. Agent
	// tests retain legacy provider fixtures while Seahorse construction uses an
	// in-memory provider and never reconstructs a runtime database path.
	newRuntimeSeahorseEngine = func(
		config seahorse.Config,
		complete seahorse.CompleteFn,
	) (*seahorse.Engine, error) {
		return seahorse.NewOfflineEngine(
			seahorse.OfflineConfig{DatabasePath: ":memory:", Config: config},
			complete,
		)
	}
}
