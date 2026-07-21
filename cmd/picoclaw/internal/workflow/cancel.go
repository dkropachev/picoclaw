package workflow

import (
	"encoding/json"

	"github.com/spf13/cobra"
)

func newCancelCommand() *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "cancel RUN_ID",
		Short: "Cancel a running workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := workflowRunStore(commandContext(cmd))
			if err != nil {
				return err
			}
			run, err := store.CancelRun(commandContext(cmd), args[0], reason)
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
	cmd.Flags().StringVar(&reason, "reason", "", "Cancellation reason")
	return cmd
}
