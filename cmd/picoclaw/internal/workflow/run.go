package workflow

import "github.com/spf13/cobra"

func newRunCommand() *cobra.Command {
	var inputsJSON string
	var secretsJSON string
	var sessionKey string
	cmd := &cobra.Command{
		Use:   "run REF",
		Short: "Run a workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			inputs, err := parseJSONMap(inputsJSON)
			if err != nil {
				return err
			}
			secrets, err := parseJSONSecrets(secretsJSON)
			if err != nil {
				return err
			}
			content, err := runWorkflowTool(commandContext(cmd), map[string]any{
				"action":  "run",
				"ref":     args[0],
				"inputs":  inputs,
				"secrets": secrets,
			}, sessionKey)
			if err != nil {
				return err
			}
			return printResult(cmd, content)
		},
	}
	cmd.Flags().StringVar(&inputsJSON, "inputs", "", "JSON object with workflow inputs")
	cmd.Flags().StringVar(&secretsJSON, "secrets", "", "JSON object with workflow secrets")
	cmd.Flags().StringVar(&sessionKey, "session", "workflow:cli", "Session key for workflow agent steps")
	return cmd
}
