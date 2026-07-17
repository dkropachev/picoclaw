package workflow

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func newGraphCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "graph RUN_ID",
		Short: "Show workflow child and retry run graph",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := workflowRunStore(commandContext(cmd))
			if err != nil {
				return err
			}
			graph, err := workflows.BuildRunGraph(commandContext(cmd), store, args[0])
			if err != nil {
				return err
			}
			data, err := json.MarshalIndent(graph, "", "  ")
			if err != nil {
				return err
			}
			return printResult(cmd, string(data))
		},
	}
}
