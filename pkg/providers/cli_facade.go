package providers

import (
	"time"

	"github.com/sipeed/picoclaw/pkg/isolation"
	cliprovider "github.com/sipeed/picoclaw/pkg/providers/cli"
)

type (
	ClaudeCliProvider     = cliprovider.ClaudeCliProvider
	CodexCliProvider      = cliprovider.CodexCliProvider
	CodexCliAuth          = cliprovider.CodexCliAuth
	GitHubCopilotProvider = cliprovider.GitHubCopilotProvider
)

const CodexHomeEnvVar = cliprovider.CodexHomeEnvVar

func NewClaudeCliProvider(workspace string) *ClaudeCliProvider {
	return cliprovider.NewClaudeCliProvider(workspace)
}

func NewClaudeCliProviderWithExecutionPolicy(
	workspace string,
	policy isolation.ExecutionPolicy,
) *ClaudeCliProvider {
	return cliprovider.NewClaudeCliProviderWithExecutionPolicy(workspace, policy)
}

func NewCodexCliProvider(workspace string) *CodexCliProvider {
	return cliprovider.NewCodexCliProvider(workspace)
}

func NewCodexCliProviderWithExecutionPolicy(
	workspace string,
	policy isolation.ExecutionPolicy,
) *CodexCliProvider {
	return cliprovider.NewCodexCliProviderWithExecutionPolicy(workspace, policy)
}

func NewGitHubCopilotProvider(uri string, connectMode string, model string) (*GitHubCopilotProvider, error) {
	return cliprovider.NewGitHubCopilotProvider(uri, connectMode, model)
}

func NewGitHubCopilotProviderWithToken(token string, model string) (*GitHubCopilotProvider, error) {
	return cliprovider.NewGitHubCopilotProviderWithToken(token, model)
}

func ReadCodexCliCredentials() (accessToken, accountID string, expiresAt time.Time, err error) {
	return cliprovider.ReadCodexCliCredentials()
}

func CreateCodexCliTokenSource() func() (string, string, error) {
	return cliprovider.CreateCodexCliTokenSource()
}

func NormalizeToolCall(tc ToolCall) ToolCall {
	return cliprovider.NormalizeToolCall(tc)
}
