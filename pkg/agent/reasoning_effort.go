package agent

import (
	"github.com/sipeed/picoclaw/pkg/config"
	providercommon "github.com/sipeed/picoclaw/pkg/providers/common"
)

func applyReasoningEffortOption(opts map[string]any, modelCfg *config.ModelConfig) {
	if opts == nil || modelCfg == nil {
		return
	}
	delete(opts, "reasoning_effort")
	effort, err := providercommon.NormalizeReasoningEffort(modelCfg.ReasoningEffort)
	if err != nil || effort == "" {
		return
	}
	opts["reasoning_effort"] = effort
}

func hasReasoningEffortConfig(modelCfg *config.ModelConfig) bool {
	if modelCfg == nil {
		return false
	}
	effort, err := providercommon.NormalizeReasoningEffort(modelCfg.ReasoningEffort)
	return err == nil && effort != ""
}
