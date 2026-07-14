package auth

import "github.com/spf13/cobra"

func newLogoutCommand() *cobra.Command {
	var (
		provider     string
		credentialID string
	)

	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Remove stored credentials",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return authLogoutCmd(provider, credentialID)
		},
	}

	cmd.Flags().StringVarP(&provider, "provider", "p", "", "Provider to logout from (openai, anthropic); empty = all")
	cmd.Flags().StringVar(
		&credentialID,
		"credential-id",
		"",
		"Credential ID to remove for the selected provider",
	)

	return cmd
}
