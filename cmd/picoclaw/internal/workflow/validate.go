package workflow

import "github.com/spf13/cobra"

func newValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate REF",
		Short: "Validate a workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			content, err := loadAndValidate(commandContext(cmd), args[0])
			if err != nil {
				return err
			}
			return printResult(cmd, content)
		},
	}
}
