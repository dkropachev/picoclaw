package agent

import (
	"errors"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func fallbackResultFromError(err error) *providers.FallbackResult {
	var exhausted *providers.FallbackExhaustedError
	if errors.As(err, &exhausted) {
		return &providers.FallbackResult{Attempts: exhausted.Attempts}
	}
	return nil
}

func fallbackResultFromSingleCandidate(
	candidate providers.FallbackCandidate,
	resp *providers.LLMResponse,
) *providers.FallbackResult {
	if resp == nil {
		return nil
	}
	return &providers.FallbackResult{
		Response:    resp,
		Provider:    candidate.Provider,
		Model:       candidate.Model,
		IdentityKey: candidate.StableKey(),
	}
}

func optsSessionKey(opts *processOptions) string {
	if opts == nil {
		return ""
	}
	if opts.SessionKey != "" {
		return opts.SessionKey
	}
	return opts.Dispatch.SessionKey
}
