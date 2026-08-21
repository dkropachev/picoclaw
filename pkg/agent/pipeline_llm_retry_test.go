package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestTransientLLMRetryReasonSafetyFilter(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		want      string
		retriable bool
	}{
		{
			name:      "security violation",
			err:       errors.New(`API error status: 400 {"code":"security_violation"}`),
			want:      "safety_filter",
			retriable: true,
		},
		{
			name:      "content filter",
			err:       errors.New(`API error status: 403 {"code":"content_filter"}`),
			want:      "safety_filter",
			retriable: true,
		},
		{
			name: "generic forbidden",
			err:  errors.New("HTTP 403 Forbidden"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, retriable := transientLLMRetryReason(tt.err)
			if got != tt.want || retriable != tt.retriable {
				t.Fatalf(
					"transientLLMRetryReason() = (%q, %t), want (%q, %t)",
					got,
					retriable,
					tt.want,
					tt.retriable,
				)
			}
		})
	}
}

func TestTransientLLMRetryReasonUsesTypedFailoverCategories(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
		ok   bool
	}{
		{name: "nil", err: nil},
		{name: "deadline", err: context.DeadlineExceeded, want: "timeout", ok: true},
		{name: "server", err: &providers.FailoverError{Reason: providers.FailoverTimeout, Status: 503}, want: "server_error", ok: true},
		{name: "network", err: &providers.FailoverError{Reason: providers.FailoverNetwork}, want: "network", ok: true},
		{name: "rate limit", err: &providers.FailoverError{Reason: providers.FailoverRateLimit}, want: "rate_limit", ok: true},
		{name: "overloaded", err: &providers.FailoverError{Reason: providers.FailoverOverloaded}, want: "rate_limit", ok: true},
		{name: "safety", err: &providers.FailoverError{Reason: providers.FailoverSafetyFilter}, want: "safety_filter", ok: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := transientLLMRetryReason(test.err)
			if got != test.want || ok != test.ok {
				t.Fatalf("transientLLMRetryReason(%v) = (%q, %t), want (%q, %t)", test.err, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestFallbackResultFromSingleCandidateCapturesClassifiedError(t *testing.T) {
	candidate := providers.FallbackCandidate{Provider: "openai", Model: "gpt-test"}
	result := fallbackResultFromSingleCandidate(candidate, nil, &providers.FailoverError{
		Reason: providers.FailoverTimeout, Provider: candidate.Provider, Model: candidate.Model,
	})
	if result == nil || len(result.Attempts) != 1 || result.Attempts[0].Reason != providers.FailoverTimeout {
		t.Fatalf("single-candidate fallback error = %#v", result)
	}
	if got := fallbackResultFromSingleCandidate(candidate, nil, nil); got != nil {
		t.Fatalf("empty single-candidate result = %#v", got)
	}
}
