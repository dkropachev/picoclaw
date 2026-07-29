package providers

import (
	"github.com/sipeed/picoclaw/pkg/auth"
)

var getCredential = auth.GetCredential

var newAntigravityProviderForCredential = func(credentialID string) LLMProvider {
	return NewAntigravityProviderForCredential(credentialID)
}

var newGitHubCopilotProviderWithToken = func(token string, model string) (LLMProvider, error) {
	return NewGitHubCopilotProviderWithToken(token, model)
}
