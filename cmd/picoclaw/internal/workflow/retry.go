package workflow

import "github.com/spf13/cobra"

func newRetryCommand() *cobra.Command {
	var secretsJSON string
	cmd := &cobra.Command{
		Use:   "retry RUN_ID",
		Short: "Retry a workflow run with its original inputs and event",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			secrets, err := parseJSONSecrets(secretsJSON)
			if err != nil {
				return err
			}
			content, err := runWorkflowTool(commandContext(cmd), map[string]any{
				"action":  "retry",
				"run_id":  args[0],
				"secrets": secrets,
			}, "workflow:cli")
			if err != nil {
				return err
			}
			return printResult(cmd, content)
		},
	}
	cmd.Flags().StringVar(&secretsJSON, "secrets", "", "JSON object with workflow secrets for the retry")
	return cmd
}
