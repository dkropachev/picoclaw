package workflow

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

func NewWorkflowCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workflow",
		Aliases: []string{"wf"},
		Short:   "Manage reusable workflows",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(
		newListCommand(),
		newCompatibilityCommand(),
		newRevalidateCommand(),
		newValidateCommand(),
		newReloadCommand(),
		newRunCommand(),
		newCancelCommand(),
		newRetryCommand(),
		newStatusCommand(),
		newEventsCommand(),
		newGraphCommand(),
	)
	return cmd
}

func printResult(cmd *cobra.Command, content string) error {
	_, err := fmt.Fprintln(cmd.OutOrStdout(), content)
	return err
}

func commandContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}
