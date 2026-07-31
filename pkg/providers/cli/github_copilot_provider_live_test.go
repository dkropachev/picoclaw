package cliprovider

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestGitHubCopilotProviderWithTokenLive(t *testing.T) {
	token := strings.TrimSpace(os.Getenv("PICOCLAW_LIVE_GITHUB_COPILOT_TOKEN"))
	if token == "" {
		t.Skip("set PICOCLAW_LIVE_GITHUB_COPILOT_TOKEN to run live GitHub Copilot smoke test")
	}
	model := strings.TrimSpace(os.Getenv("PICOCLAW_LIVE_GITHUB_COPILOT_MODEL"))
	if model == "" {
		t.Skip("set PICOCLAW_LIVE_GITHUB_COPILOT_MODEL to an exact model to run the live smoke test")
	}

	provider, err := NewGitHubCopilotProviderWithToken(token, model)
	if err != nil {
		t.Fatalf("NewGitHubCopilotProviderWithToken() error = %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	resp, err := provider.Chat(ctx, []Message{{
		Role:    "user",
		Content: "Respond with exactly: pong",
	}}, nil, model, nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp == nil || strings.TrimSpace(resp.Content) == "" {
		t.Fatalf("Chat() returned empty response: %#v", resp)
	}
}
