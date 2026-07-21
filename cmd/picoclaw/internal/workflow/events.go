package workflow

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func newEventsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "events RUN_ID",
		Short: "Show workflow run events",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workspace, err := workflowWorkspace()
			if err != nil {
				return err
			}
			events, err := workflows.NewFileRunStore(workspace).Events(commandContext(cmd), args[0])
			if err != nil {
				return err
			}
			data, err := json.MarshalIndent(map[string]any{"run_id": args[0], "events": events}, "", "  ")
			if err != nil {
				return err
			}
			return printResult(cmd, string(data))
		},
	}
}
