package workflow

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func newCompatibilityCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "compatibility",
		Short: "Show workflow runtime compatibility status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := workflowConfig()
			if err != nil {
				return err
			}
			workspace := cfg.WorkspacePath()
			summary, err := workflows.LoadCompatibilitySummary(
				commandContext(cmd),
				workspace,
				workflowRuntimeCompatibility(),
				workflowLocalOptions(cfg)...,
			)
			if err != nil {
				return err
			}
			data, err := json.MarshalIndent(summary, "", "  ")
			if err != nil {
				return err
			}
			return printResult(cmd, string(data))
		},
	}
}

func newRevalidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "revalidate",
		Short: "Revalidate workspace workflows against the current runtime",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := workflowConfig()
			if err != nil {
				return err
			}
			workspace := cfg.WorkspacePath()
			localOpts := workflowLocalOptions(cfg)
			runtime := workflowRuntimeCompatibility()
			if _, revalidateErr := workflows.RevalidateLocal(
				commandContext(cmd),
				workspace,
				runtime,
				localOpts...,
			); revalidateErr != nil {
				return revalidateErr
			}
			summary, err := workflows.LoadCompatibilitySummary(commandContext(cmd), workspace, runtime, localOpts...)
			if err != nil {
				return err
			}
			data, err := json.MarshalIndent(summary, "", "  ")
			if err != nil {
				return err
			}
			return printResult(cmd, string(data))
		},
	}
}
