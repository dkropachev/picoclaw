package auth

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/cmd/picoclaw/internal"
	"github.com/sipeed/picoclaw/pkg/auth"
	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

const (
	supportedProvidersMsg = "supported providers: openai, anthropic, google-antigravity, antigravity"
	defaultAnthropicModel = "claude-sonnet-4.6"
)

func authLoginCmd(provider string, credentialID string, useDeviceCode bool, useOauth bool, noBrowser bool) error {
	switch provider {
	case "openai":
		return authLoginOpenAI(useDeviceCode, noBrowser, credentialID)
	case "anthropic":
		return authLoginAnthropic(useOauth, credentialID)
	case "google-antigravity", "antigravity":
		return authLoginGoogleAntigravity(noBrowser, credentialID)
	default:
		return fmt.Errorf("unsupported provider: %s (%s)", provider, supportedProvidersMsg)
	}
}

func authLoginOpenAI(useDeviceCode bool, noBrowser bool, credentialID string) error {
	cfg := auth.OpenAIOAuthConfig()
	storeKey, err := auth.NormalizeCredentialID("openai", credentialID)
	if err != nil {
		return err
	}

	var cred *auth.AuthCredential

	if useDeviceCode {
		cred, err = auth.LoginDeviceCode(cfg)
	} else {
		cred, err = auth.LoginBrowserWithOptions(cfg, auth.LoginBrowserOptions{NoBrowser: noBrowser})
	}

	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	cred.Provider = "openai"
	if err = auth.SetCredential(storeKey, cred); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	appCfg, err := internal.LoadConfig()
	if err == nil {
		// Update or add openai in ModelList
		foundOpenAI := false
		for i := range appCfg.ModelList {
			if isOpenAIModel(appCfg.ModelList[i]) && modelCredentialID("openai", appCfg.ModelList[i]) == storeKey {
				appCfg.ModelList[i].AuthMethod = "oauth"
				appCfg.ModelList[i].CredentialID = credentialIDForConfig("openai", storeKey)
				foundOpenAI = true
				break
			}
		}

		// If no openai in ModelList, add it
		if !foundOpenAI {
			appCfg.ModelList = append(appCfg.ModelList, &config.ModelConfig{
				ModelName:    modelNameForCredential("gpt-5.4", "openai", storeKey),
				Model:        "openai/gpt-5.4",
				AuthMethod:   "oauth",
				CredentialID: credentialIDForConfig("openai", storeKey),
			})
		}

		if storeKey == "openai" || appCfg.Agents.Defaults.GetModelName() == "" {
			appCfg.Agents.Defaults.ModelName = modelNameForCredential("gpt-5.4", "openai", storeKey)
		}

		if err = config.SaveConfig(internal.GetConfigPath(), appCfg); err != nil {
			return fmt.Errorf("could not update config: %w", err)
		}
	}

	fmt.Println("Login successful!")
	fmt.Printf("Credential ID: %s\n", storeKey)
	if cred.AccountID != "" {
		fmt.Printf("Account: %s\n", cred.AccountID)
	}
	if storeKey == "openai" {
		fmt.Println("Default model set to: gpt-5.4")
	}

	return nil
}

func authLoginGoogleAntigravity(noBrowser bool, credentialID string) error {
	cfg := auth.GoogleAntigravityOAuthConfig()
	storeKey, err := auth.NormalizeCredentialID("google-antigravity", credentialID)
	if err != nil {
		return err
	}

	cred, err := auth.LoginBrowserWithOptions(cfg, auth.LoginBrowserOptions{NoBrowser: noBrowser})
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	cred.Provider = "google-antigravity"

	// Fetch user email from Google userinfo
	email, err := fetchGoogleUserEmail(cred.AccessToken)
	if err != nil {
		fmt.Printf("Warning: could not fetch email: %v\n", err)
	} else {
		cred.Email = email
		fmt.Printf("Email: %s\n", email)
	}

	// Fetch Cloud Code Assist project ID
	projectID, err := providers.FetchAntigravityProjectID(cred.AccessToken)
	if err != nil {
		fmt.Printf("Warning: could not fetch project ID: %v\n", err)
		fmt.Println("You may need Google Cloud Code Assist enabled on your account.")
	} else {
		cred.ProjectID = projectID
		fmt.Printf("Project: %s\n", projectID)
	}

	if err = auth.SetCredential(storeKey, cred); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	appCfg, err := internal.LoadConfig()
	if err == nil {
		// Update or add antigravity in ModelList
		foundAntigravity := false
		for i := range appCfg.ModelList {
			if isAntigravityModel(appCfg.ModelList[i]) &&
				modelCredentialID("google-antigravity", appCfg.ModelList[i]) == storeKey {
				appCfg.ModelList[i].AuthMethod = "oauth"
				appCfg.ModelList[i].CredentialID = credentialIDForConfig("google-antigravity", storeKey)
				foundAntigravity = true
				break
			}
		}

		// If no antigravity in ModelList, add it
		if !foundAntigravity {
			appCfg.ModelList = append(appCfg.ModelList, &config.ModelConfig{
				ModelName:    modelNameForCredential("gemini-flash", "google-antigravity", storeKey),
				Model:        "antigravity/gemini-3-flash",
				AuthMethod:   "oauth",
				CredentialID: credentialIDForConfig("google-antigravity", storeKey),
			})
		}

		if storeKey == "google-antigravity" || appCfg.Agents.Defaults.GetModelName() == "" {
			appCfg.Agents.Defaults.ModelName = modelNameForCredential("gemini-flash", "google-antigravity", storeKey)
		}

		if err := config.SaveConfig(internal.GetConfigPath(), appCfg); err != nil {
			fmt.Printf("Warning: could not update config: %v\n", err)
		}
	}

	fmt.Println("\n✓ Google Antigravity login successful!")
	fmt.Printf("Credential ID: %s\n", storeKey)
	if storeKey == "google-antigravity" {
		fmt.Println("Default model set to: gemini-flash")
	}
	fmt.Println("Try it: picoclaw agent -m \"Hello world\"")

	return nil
}

func authLoginAnthropic(useOauth bool, credentialID string) error {
	if useOauth {
		return authLoginAnthropicSetupToken(credentialID)
	}

	fmt.Println("Anthropic login method:")
	fmt.Println("  1) Setup token (from `claude setup-token`) (Recommended)")
	fmt.Println("  2) API key (from console.anthropic.com)")

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("Choose [1]: ")
		choice := "1"
		if scanner.Scan() {
			text := strings.TrimSpace(scanner.Text())
			if text != "" {
				choice = text
			}
		}

		switch choice {
		case "1":
			return authLoginAnthropicSetupToken(credentialID)
		case "2":
			return authLoginPasteToken("anthropic", credentialID)
		default:
			fmt.Printf("Invalid choice: %s. Please enter 1 or 2.\n", choice)
		}
	}
}

func authLoginAnthropicSetupToken(credentialID string) error {
	storeKey, err := auth.NormalizeCredentialID("anthropic", credentialID)
	if err != nil {
		return err
	}
	cred, err := auth.LoginSetupToken(os.Stdin)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	if err = auth.SetCredential(storeKey, cred); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	appCfg, err := internal.LoadConfig()
	if err == nil {
		found := false
		for i := range appCfg.ModelList {
			if isAnthropicModel(appCfg.ModelList[i]) &&
				modelCredentialID("anthropic", appCfg.ModelList[i]) == storeKey {
				appCfg.ModelList[i].AuthMethod = "oauth"
				appCfg.ModelList[i].CredentialID = credentialIDForConfig("anthropic", storeKey)
				found = true
				break
			}
		}
		if !found {
			appCfg.ModelList = append(appCfg.ModelList, &config.ModelConfig{
				ModelName:    modelNameForCredential(defaultAnthropicModel, "anthropic", storeKey),
				Model:        "anthropic/" + defaultAnthropicModel,
				AuthMethod:   "oauth",
				CredentialID: credentialIDForConfig("anthropic", storeKey),
			})
			// Only set default model if user has no default configured yet
			if appCfg.Agents.Defaults.GetModelName() == "" {
				appCfg.Agents.Defaults.ModelName = modelNameForCredential(defaultAnthropicModel, "anthropic", storeKey)
			}
		}

		if err := config.SaveConfig(internal.GetConfigPath(), appCfg); err != nil {
			return fmt.Errorf("could not update config: %w", err)
		}
	}

	fmt.Printf("Setup token saved for Anthropic as %s!\n", storeKey)

	return nil
}

func fetchGoogleUserEmail(accessToken string) (string, error) {
	req, err := http.NewRequest("GET", "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading userinfo response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("userinfo request failed: %s", string(body))
	}

	var userInfo struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return "", err
	}
	return userInfo.Email, nil
}

func authLoginPasteToken(provider string, credentialID string) error {
	storeKey, err := auth.NormalizeCredentialID(provider, credentialID)
	if err != nil {
		return err
	}
	cred, err := auth.LoginPasteToken(provider, os.Stdin)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	if err = auth.SetCredential(storeKey, cred); err != nil {
		return fmt.Errorf("failed to save credentials: %w", err)
	}

	appCfg, err := internal.LoadConfig()
	if err == nil {
		switch provider {
		case "anthropic":
			// Update ModelList
			found := false
			for i := range appCfg.ModelList {
				if isAnthropicModel(appCfg.ModelList[i]) &&
					modelCredentialID("anthropic", appCfg.ModelList[i]) == storeKey {
					appCfg.ModelList[i].AuthMethod = "token"
					appCfg.ModelList[i].CredentialID = credentialIDForConfig("anthropic", storeKey)
					found = true
					break
				}
			}
			if !found {
				appCfg.ModelList = append(appCfg.ModelList, &config.ModelConfig{
					ModelName:    modelNameForCredential(defaultAnthropicModel, "anthropic", storeKey),
					Model:        "anthropic/" + defaultAnthropicModel,
					AuthMethod:   "token",
					CredentialID: credentialIDForConfig("anthropic", storeKey),
				})
				if storeKey == "anthropic" || appCfg.Agents.Defaults.GetModelName() == "" {
					appCfg.Agents.Defaults.ModelName = modelNameForCredential(
						defaultAnthropicModel,
						"anthropic",
						storeKey,
					)
				}
			}
		case "openai":
			// Update ModelList
			found := false
			for i := range appCfg.ModelList {
				if isOpenAIModel(appCfg.ModelList[i]) && modelCredentialID("openai", appCfg.ModelList[i]) == storeKey {
					appCfg.ModelList[i].AuthMethod = "token"
					appCfg.ModelList[i].CredentialID = credentialIDForConfig("openai", storeKey)
					found = true
					break
				}
			}
			if !found {
				appCfg.ModelList = append(appCfg.ModelList, &config.ModelConfig{
					ModelName:    modelNameForCredential("gpt-5.4", "openai", storeKey),
					Model:        "openai/gpt-5.4",
					AuthMethod:   "token",
					CredentialID: credentialIDForConfig("openai", storeKey),
				})
			}
			if storeKey == "openai" || appCfg.Agents.Defaults.GetModelName() == "" {
				appCfg.Agents.Defaults.ModelName = modelNameForCredential("gpt-5.4", "openai", storeKey)
			}
		}
		if err := config.SaveConfig(internal.GetConfigPath(), appCfg); err != nil {
			return fmt.Errorf("could not update config: %w", err)
		}
	}

	fmt.Printf("Token saved for %s as %s!\n", provider, storeKey)

	if appCfg != nil {
		fmt.Printf("Default model set to: %s\n", appCfg.Agents.Defaults.GetModelName())
	}

	return nil
}

func authLogoutCmd(provider string, credentialID string) error {
	if provider != "" {
		storeKey, err := auth.NormalizeCredentialID(provider, credentialID)
		if err != nil {
			return err
		}
		if deleteErr := auth.DeleteCredential(storeKey); deleteErr != nil {
			return fmt.Errorf("failed to remove credentials: %w", deleteErr)
		}

		appCfg, err := internal.LoadConfig()
		if err == nil {
			// Clear AuthMethod in ModelList
			for i := range appCfg.ModelList {
				switch provider {
				case "openai":
					if isOpenAIModel(appCfg.ModelList[i]) &&
						modelCredentialID("openai", appCfg.ModelList[i]) == storeKey {
						appCfg.ModelList[i].AuthMethod = ""
					}
				case "anthropic":
					if isAnthropicModel(appCfg.ModelList[i]) &&
						modelCredentialID("anthropic", appCfg.ModelList[i]) == storeKey {
						appCfg.ModelList[i].AuthMethod = ""
					}
				case "google-antigravity", "antigravity":
					if isAntigravityModel(appCfg.ModelList[i]) &&
						modelCredentialID("google-antigravity", appCfg.ModelList[i]) == storeKey {
						appCfg.ModelList[i].AuthMethod = ""
					}
				}
			}
			config.SaveConfig(internal.GetConfigPath(), appCfg)
		}

		fmt.Printf("Logged out from %s\n", storeKey)

		return nil
	}
	if strings.TrimSpace(credentialID) != "" {
		return fmt.Errorf("--credential-id requires --provider")
	}

	if err := auth.DeleteAllCredentials(); err != nil {
		return fmt.Errorf("failed to remove credentials: %w", err)
	}

	appCfg, err := internal.LoadConfig()
	if err == nil {
		// Clear all AuthMethods in ModelList
		for i := range appCfg.ModelList {
			appCfg.ModelList[i].AuthMethod = ""
		}
		config.SaveConfig(internal.GetConfigPath(), appCfg)
	}

	fmt.Println("Logged out from all providers")

	return nil
}

func authStatusCmd() error {
	store, err := auth.LoadStore()
	if err != nil {
		return fmt.Errorf("failed to load auth store: %w", err)
	}

	if len(store.Credentials) == 0 {
		fmt.Println("No authenticated providers.")
		fmt.Println("Run: picoclaw auth login --provider <name>")
		return nil
	}

	fmt.Println("\nAuthenticated Providers:")
	fmt.Println("------------------------")
	for provider, cred := range store.Credentials {
		status := "active"
		if cred.IsExpired() {
			status = "expired"
		} else if cred.NeedsRefresh() {
			status = "needs refresh"
		}

		fmt.Printf("  %s:\n", provider)
		fmt.Printf("    Method: %s\n", cred.AuthMethod)
		fmt.Printf("    Status: %s\n", status)
		if cred.AccountID != "" {
			fmt.Printf("    Account: %s\n", cred.AccountID)
		}
		if cred.Email != "" {
			fmt.Printf("    Email: %s\n", cred.Email)
		}
		if cred.ProjectID != "" {
			fmt.Printf("    Project: %s\n", cred.ProjectID)
		}
		if !cred.ExpiresAt.IsZero() {
			fmt.Printf("    Expires: %s\n", cred.ExpiresAt.Format("2006-01-02 15:04"))
		}

		if provider == "anthropic" && cred.AuthMethod == "oauth" {
			usage, err := auth.FetchAnthropicUsage(cred.AccessToken)
			if err != nil {
				fmt.Printf("    Usage: unavailable (%v)\n", err)
			} else {
				fmt.Printf("    Usage (5h):  %.1f%%\n", usage.FiveHourUtilization*100)
				fmt.Printf("    Usage (7d):  %.1f%%\n", usage.SevenDayUtilization*100)
			}
		}
	}

	return nil
}

func authModelsCmd() error {
	cred, err := auth.GetCredential("google-antigravity")
	if err != nil || cred == nil {
		return fmt.Errorf(
			"not logged in to Google Antigravity.\nrun: picoclaw auth login --provider google-antigravity",
		)
	}

	// Refresh token if needed
	if cred.NeedsRefresh() && cred.RefreshToken != "" {
		oauthCfg := auth.GoogleAntigravityOAuthConfig()
		refreshed, refreshErr := auth.RefreshAccessToken(cred, oauthCfg)
		if refreshErr == nil {
			cred = refreshed
			_ = auth.SetCredential("google-antigravity", cred)
		}
	}

	projectID := cred.ProjectID
	if projectID == "" {
		return fmt.Errorf("no project id stored. Try logging in again")
	}

	fmt.Printf("Fetching models for project: %s\n\n", projectID)

	models, err := providers.FetchAntigravityModels(cred.AccessToken, projectID)
	if err != nil {
		return fmt.Errorf("error fetching models: %w", err)
	}

	if len(models) == 0 {
		return fmt.Errorf("no models available")
	}

	fmt.Println("Available Antigravity Models:")
	fmt.Println("-----------------------------")
	for _, m := range models {
		status := "✓"
		if m.IsExhausted {
			status = "✗ (quota exhausted)"
		}
		name := m.ID
		if m.DisplayName != "" {
			name = fmt.Sprintf("%s (%s)", m.ID, m.DisplayName)
		}
		fmt.Printf("  %s %s\n", status, name)
	}

	return nil
}

func modelCredentialID(provider string, modelCfg *config.ModelConfig) string {
	if modelCfg == nil {
		return ""
	}
	credentialID, err := auth.NormalizeCredentialID(provider, modelCfg.CredentialID)
	if err != nil {
		return ""
	}
	return credentialID
}

func credentialIDForConfig(provider, credentialID string) string {
	if credentialID == provider {
		return ""
	}
	return credentialID
}

func modelNameForCredential(baseName, provider, credentialID string) string {
	if credentialID == provider {
		return baseName
	}
	_, suffix, ok := strings.Cut(credentialID, ":")
	if !ok || suffix == "" {
		return baseName
	}
	return baseName + "-" + suffix
}

// isAntigravityModel checks if a model config belongs to an Antigravity provider.
func isAntigravityModel(modelCfg *config.ModelConfig) bool {
	protocol, _ := providers.ExtractProtocol(modelCfg)
	return protocol == "antigravity" || protocol == "google-antigravity"
}

// isOpenAIModel checks if a model config belongs to the OpenAI provider.
func isOpenAIModel(modelCfg *config.ModelConfig) bool {
	protocol, _ := providers.ExtractProtocol(modelCfg)
	return protocol == "openai"
}

// isAnthropicModel checks if a model config belongs to the Anthropic provider.
func isAnthropicModel(modelCfg *config.ModelConfig) bool {
	protocol, _ := providers.ExtractProtocol(modelCfg)
	return protocol == "anthropic"
}
