package cliprovider

import (
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/isolation"
)

func defaultExecutionPolicy() isolation.ExecutionPolicy {
	return isolation.NewExecutionPolicy(config.DefaultConfig().Isolation)
}
