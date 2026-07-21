package workflow

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func newListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List workspace workflows",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := workflowConfig()
			if err != nil {
				return err
			}
			defs, err := workflows.ListLocal(
				commandContext(cmd),
				cfg.WorkspacePath(),
				workflowLocalOptions(cfg)...,
			)
			if err != nil {
				return err
			}
			data, err := json.MarshalIndent(map[string]any{"workflows": defs}, "", "  ")
			if err != nil {
				return err
			}
			return printResult(cmd, string(data))
		},
	}
}
