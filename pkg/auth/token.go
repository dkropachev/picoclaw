package auth

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

func LoginPasteToken(provider string, r io.Reader) (*AuthCredential, error) {
	fmt.Printf("Paste your API key or session token from %s:\n", providerDisplayName(provider))
	fmt.Print("> ")

	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("reading token: %w", err)
		}
		return nil, fmt.Errorf("no input received")
	}

	token := strings.TrimSpace(scanner.Text())
	if token == "" {
		return nil, fmt.Errorf("token cannot be empty")
	}
	if provider == "github-copilot" {
		if err := ValidateGitHubCopilotToken(token); err != nil {
			return nil, err
		}
	}

	return &AuthCredential{
		AccessToken: token,
		Provider:    provider,
		AuthMethod:  "token",
	}, nil
}

func LoginSetupToken(r io.Reader) (*AuthCredential, error) {
	fmt.Println("Paste your setup token from `claude setup-token`:")
	fmt.Print("> ")

	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("reading token: %w", err)
		}
		return nil, fmt.Errorf("no input received")
	}

	token := strings.TrimSpace(scanner.Text())

	if !strings.HasPrefix(token, "sk-ant-oat01-") {
		return nil, fmt.Errorf("invalid setup token: expected prefix sk-ant-oat01-")
	}

	if len(token) < 80 {
		return nil, fmt.Errorf("invalid setup token: too short (expected at least 80 characters)")
	}

	return &AuthCredential{
		AccessToken: token,
		Provider:    "anthropic",
		AuthMethod:  "oauth",
	}, nil
}

func ValidateGitHubCopilotToken(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("token cannot be empty")
	}
	switch {
	case strings.HasPrefix(token, "gho_"),
		strings.HasPrefix(token, "ghu_"),
		strings.HasPrefix(token, "github_pat_"):
		return nil
	case strings.HasPrefix(token, "ghp_"):
		return fmt.Errorf("classic GitHub personal access tokens (ghp_) are not supported by GitHub Copilot; use a GitHub OAuth user token or fine-grained personal access token")
	default:
		return fmt.Errorf("unsupported GitHub Copilot token prefix; expected gho_, ghu_, or github_pat_")
	}
}

func providerDisplayName(provider string) string {
	switch provider {
	case "anthropic":
		return "console.anthropic.com"
	case "openai":
		return "platform.openai.com"
	case "github-copilot":
		return "github.com/copilot"
	default:
		return provider
	}
}
