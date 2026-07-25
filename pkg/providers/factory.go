package providers

import (
	"github.com/sipeed/picoclaw/pkg/auth"
)

var getCredential = auth.GetCredential

var newGitHubCopilotProviderWithToken = func(token string, model string) (LLMProvider, error) {
	return NewGitHubCopilotProviderWithToken(token, model)
}
