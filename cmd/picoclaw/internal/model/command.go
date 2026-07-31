package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func NewModelCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model [model_alias]",
		Short: "Show or change the configured model alias",
		Long: `Show or change the model alias used by default.

If no argument is provided, shows the current account and model alias.
If an alias is provided, selects that exact configured alias while retaining
the configured account.

To onboard a model from a custom OpenAI-compatible endpoint (fetch the
available list online and pick one), use the 'add' subcommand:

  picoclaw model add --help

Examples:
  picoclaw model                    # Show current account and model alias
  picoclaw model coding            # Select the configured "coding" alias
  picoclaw model add -b URL -k KEY # Add an account and model alias`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := internal.GetConfigPath()

			if len(args) == 0 {
				cfg, err := config.LoadConfig(configPath)
				if err != nil {
					return fmt.Errorf("failed to load config: %w", err)
				}
				showCurrentModel(cfg)
				return nil
			}

			cfg, revision, err := config.LoadConfigForUpdateSnapshot(configPath)
			if err != nil {
				return fmt.Errorf("failed to load config: %w", err)
			}
			return selectModelAlias(configPath, cfg, args[0], revision)
		},
	}

	cmd.AddCommand(newAddCommand())

	return cmd
}

func showCurrentModel(cfg *config.Config) {
	accountRef := cfg.Agents.Defaults.AccountRef
	modelAlias := cfg.Agents.Defaults.ModelName

	if accountRef == "" {
		fmt.Println("Current account: (none)")
	} else {
		fmt.Printf("Current account: %s\n", accountRef)
	}
	if modelAlias == "" {
		fmt.Printf("Current model alias: (none) — %s\n", config.ErrNoModelConfigured)
	} else {
		fmt.Printf("Current model alias: %s\n", modelAlias)
	}

	fmt.Println("\nAvailable model aliases:")
	listAvailableModels(cfg)
	fmt.Println("\nTip: 'picoclaw model add -b URL -k KEY' adds a model from a custom")
	fmt.Println("     OpenAI-compatible endpoint (see 'picoclaw model add --help').")
}

func listAvailableModels(cfg *config.Config) {
	if len(cfg.ModelAliases) == 0 {
		fmt.Println("  No model aliases configured")
		return
	}

	selectedAlias := cfg.Agents.Defaults.ModelName

	for i := range cfg.ModelAliases {
		alias := &cfg.ModelAliases[i]
		marker := "  "
		if alias.Name == selectedAlias {
			marker = "> "
		}
		suffix := ""
		if count := len(alias.AccountOverrides); count > 0 {
			suffix = fmt.Sprintf(", %d account override(s)", count)
		}
		fmt.Printf("%s- %s (%s%s)\n", marker, alias.Name, alias.Model, suffix)
	}
}

func selectModelAlias(
	configPath string,
	cfg *config.Config,
	modelName,
	expectedRevision string,
) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if _, err := cfg.GetModelAlias(modelName); err != nil {
		return err
	}
	if cfg.Agents.Defaults.AccountRef == "" {
		return fmt.Errorf("no account configured")
	}
	if err := validateAliasForAccountSelector(
		cfg,
		cfg.Agents.Defaults.AccountRef,
		modelName,
	); err != nil {
		return err
	}

	oldModel := cfg.Agents.Defaults.ModelName
	cfg.Agents.Defaults.ModelName = modelName

	if _, err := config.SaveConfigIfRevision(configPath, cfg, expectedRevision); err != nil {
		if errors.Is(err, config.ErrConfigRevisionMismatch) {
			return fmt.Errorf(
				"config changed while selecting model alias; reload and retry: %w",
				err,
			)
		}
		return fmt.Errorf("failed to save config: %w", err)
	}

	fmt.Printf("✓ Model alias changed from '%s' to '%s'\n",
		formatModelName(oldModel), modelName)
	fmt.Printf("Account remains '%s'.\n", cfg.Agents.Defaults.AccountRef)

	return nil
}

func validateAliasForAccountSelector(
	cfg *config.Config,
	accountSelector string,
	modelAlias string,
) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	accountSelector = strings.TrimSpace(accountSelector)
	if accountSelector == "" {
		return fmt.Errorf("no account configured")
	}
	accounts := []string{accountSelector}
	for i := range cfg.AccountRouters {
		if strings.TrimSpace(cfg.AccountRouters[i].Name) != accountSelector {
			continue
		}
		if !cfg.AccountRouters[i].Enabled {
			return fmt.Errorf("account router %q is disabled", accountSelector)
		}
		accounts = reachableCLIAccountRouterRefs(&cfg.AccountRouters[i])
		if len(accounts) == 0 {
			return fmt.Errorf("account router %q has no reachable accounts", accountSelector)
		}
		break
	}
	for _, accountRef := range accounts {
		model, err := cfg.ResolveModelAlias(modelAlias, accountRef)
		if err != nil {
			return err
		}
		if _, credential := config.AccountRouterCredentialAccountID(accountRef); credential {
			provider, ok := config.AccountRouterCredentialAccountProvider(accountRef)
			if !ok {
				return fmt.Errorf("credential account %q has an unsupported provider", accountRef)
			}
			if _, err := providers.ResolveModelForProvider(provider, model); err != nil {
				return fmt.Errorf(
					"model alias %q with account %q: %w",
					modelAlias,
					accountRef,
					err,
				)
			}
			if providers.NormalizeProvider(provider) == "elevenlabs" {
				return fmt.Errorf("account %q is not usable for chat", accountRef)
			}
			continue
		}

		found := false
		for _, account := range cfg.ModelList {
			if account == nil ||
				strings.TrimSpace(account.ModelName) != accountRef ||
				!account.Enabled ||
				account.IsAccountRouter() ||
				account.IsModelRouter() {
				continue
			}
			found = true
			provider, _ := providers.ExtractProtocol(account)
			if _, err := providers.ResolveModelForProvider(provider, model); err != nil {
				return fmt.Errorf(
					"model alias %q with account %q: %w",
					modelAlias,
					accountRef,
					err,
				)
			}
			if providers.NormalizeProvider(provider) == "elevenlabs" {
				return fmt.Errorf("account %q is not usable for chat", accountRef)
			}
		}
		if !found {
			return fmt.Errorf("account %q is not configured or enabled", accountRef)
		}
	}
	return nil
}

func reachableCLIAccountRouterRefs(router *config.AccountRouterConfig) []string {
	if router == nil {
		return nil
	}
	blocks := make(map[string]config.AccountRouterBlock, len(router.Blocks))
	for _, block := range router.Blocks {
		blocks[strings.TrimSpace(block.ID)] = block
	}
	seenBlocks := make(map[string]bool, len(blocks))
	seenAccounts := make(map[string]bool)
	accounts := make([]string, 0)
	add := func(account string) {
		account = strings.TrimSpace(account)
		if account == "" || seenAccounts[account] {
			return
		}
		seenAccounts[account] = true
		accounts = append(accounts, account)
	}
	var walk func(string)
	walk = func(blockID string) {
		blockID = strings.TrimSpace(blockID)
		if blockID == "" || seenBlocks[blockID] {
			return
		}
		seenBlocks[blockID] = true
		block, ok := blocks[blockID]
		if !ok {
			return
		}
		switch strings.TrimSpace(block.Type) {
		case config.AccountRouterBlockTypeAccount:
			add(block.Account)
		case config.AccountRouterBlockTypeLoadBalance:
			for _, account := range block.Accounts {
				add(account)
			}
		case config.AccountRouterBlockTypeBranch:
			walk(block.Then)
			walk(block.Else)
		}
		walk(block.Fallback)
	}
	walk(router.Entry)
	return accounts
}

func formatModelName(name string) string {
	if name == "" {
		return "(none)"
	}
	return name
}
