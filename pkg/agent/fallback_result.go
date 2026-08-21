package agent

import (
	"errors"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func fallbackResultFromError(
	err error,
	candidates ...providers.FallbackCandidate,
) *providers.FallbackResult {
	var exhausted *providers.FallbackExhaustedError
	if errors.As(err, &exhausted) {
		return &providers.FallbackResult{Attempts: exhausted.Attempts}
	}
	var failover *providers.FailoverError
	if errors.As(err, &failover) {
		if failover.IdentityKey != "" {
			return &providers.FallbackResult{
				Attempts: []providers.FallbackAttempt{{
					Provider:    failover.Provider,
					Model:       failover.Model,
					IdentityKey: failover.IdentityKey,
					Error:       failover,
					Reason:      failover.Reason,
				}},
			}
		}
		for _, candidate := range candidates {
			if providers.ModelKey(candidate.Provider, candidate.Model) !=
				providers.ModelKey(failover.Provider, failover.Model) {
				continue
			}
			return &providers.FallbackResult{
				Attempts: []providers.FallbackAttempt{{
					Provider:    candidate.Provider,
					Model:       candidate.Model,
					IdentityKey: candidate.StableKey(),
					Error:       failover,
					Reason:      failover.Reason,
				}},
			}
		}
	}
	return nil
}

func fallbackResultFromSingleCandidate(
	candidate providers.FallbackCandidate,
	resp *providers.LLMResponse,
	err error,
) *providers.FallbackResult {
	if err != nil {
		return fallbackResultFromError(err, candidate)
	}
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
