package auth

import (
	"strings"
	"testing"
)

func TestValidateGitHubCopilotToken(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		wantErr string
	}{
		{name: "oauth user token", token: "gho_token"},
		{name: "oauth user-to-server token", token: "ghu_token"},
		{name: "fine-grained pat", token: "github_pat_token"},
		{name: "classic pat", token: "ghp_token", wantErr: "ghp_"},
		{name: "unsupported prefix", token: "sk-token", wantErr: "expected gho_"},
		{name: "empty", token: " ", wantErr: "empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateGitHubCopilotToken(tt.token)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateGitHubCopilotToken() error = %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateGitHubCopilotToken() error = nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestLoginPasteTokenGitHubCopilotValidatesPrefix(t *testing.T) {
	if _, err := LoginPasteToken("github-copilot", strings.NewReader("ghp_unsupported\n")); err == nil {
		t.Fatal("LoginPasteToken() error = nil, want unsupported token error")
	}

	cred, err := LoginPasteToken("github-copilot", strings.NewReader("gho_supported\n"))
	if err != nil {
		t.Fatalf("LoginPasteToken() error = %v", err)
	}
	if cred.Provider != "github-copilot" {
		t.Fatalf("Provider = %q, want github-copilot", cred.Provider)
	}
	if cred.AccessToken != "gho_supported" {
		t.Fatalf("AccessToken = %q, want trimmed token", cred.AccessToken)
	}
}
