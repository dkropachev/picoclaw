package workflow

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func newStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status RUN_ID",
		Short: "Show workflow run status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workspace, err := workflowWorkspace()
			if err != nil {
				return err
			}
			run, err := workflows.NewFileRunStore(workspace).GetRun(commandContext(cmd), args[0])
			if err != nil {
				return err
			}
			data, err := json.MarshalIndent(run, "", "  ")
			if err != nil {
				return err
			}
			return printResult(cmd, string(data))
		},
	}
}
