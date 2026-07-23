package workflow

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func newInstallCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "install [code-review]",
		Short: "Install a local workflow template",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := workflows.CodeReviewWorkflowName
			if len(args) > 0 {
				name = args[0]
			}
			cfg, err := workflowConfig()
			if err != nil {
				return err
			}
			workspace := cfg.WorkspacePath()
			result, err := workflows.InstallWorkflowTemplate(
				commandContext(cmd),
				workspace,
				name,
				force,
				workflowLocalOptions(cfg)...,
			)
			if err != nil {
				return err
			}
			compatibility, err := workflows.RevalidateLocal(
				commandContext(cmd),
				workspace,
				workflowRuntimeCompatibility(),
				workflowLocalOptions(cfg)...,
			)
			if err != nil {
				return err
			}
			payload := map[string]any{
				"template":      result,
				"compatibility": compatibility,
			}
			data, err := json.MarshalIndent(payload, "", "  ")
			if err != nil {
				return err
			}
			return printResult(cmd, string(data))
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite an existing installed workflow")
	return cmd
}
