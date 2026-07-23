package modelrouter

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestLoadBalanceKeepsSessionStickyUntilCompression(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	router := newTestRouter(t, &config.ModelRouterConfig{
		Enabled: true,
		Entry:   "pool",
		Blocks: []config.ModelRouterBlock{{
			ID:       "pool",
			Type:     config.ModelRouterBlockTypeLoadBalance,
			Accounts: []string{"account-a", "account-b"},
			Strategy: config.ModelRouterStrategyTokensSpent,
		}},
	}, now)

	first := router.Select("session-1", SelectReasonInitial)
	if got := selectedAccount(t, first); got != "account-a" {
		t.Fatalf("initial account = %q, want account-a", got)
	}
	router.RecordFallbackResult(first, successResult(first, 300), nil)

	sticky := router.Select("session-1", SelectReasonInitial)
	if got := selectedAccount(t, sticky); got != "account-a" {
		t.Fatalf("sticky account = %q, want account-a", got)
	}

	compressed := router.Select("session-1", SelectReasonCompression)
	if got := selectedAccount(t, compressed); got != "account-b" {
		t.Fatalf("compression account = %q, want account-b", got)
	}
}

func TestAccountFallbackWhenSelectedAccountUnavailable(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	router := newTestRouter(t, &config.ModelRouterConfig{
		Enabled: true,
		Entry:   "entry",
		Blocks: []config.ModelRouterBlock{
			{
				ID:       "entry",
				Type:     config.ModelRouterBlockTypeAccount,
				Account:  "account-a",
				Fallback: "fallback",
			},
			{
				ID:      "fallback",
				Type:    config.ModelRouterBlockTypeAccount,
				Account: "account-b",
			},
		},
	}, now)

	first := router.Select("session-1", SelectReasonInitial)
	if got := first.Candidates[0].StableKey(); got != "account:account-a" {
		t.Fatalf("first candidate = %q, want account:account-a", got)
	}
	router.RecordFallbackResult(first, &providers.FallbackResult{
		Response:    &providers.LLMResponse{Content: "ok"},
		Provider:    "openai",
		Model:       "gpt-4o",
		IdentityKey: "account:account-b",
		Attempts: []providers.FallbackAttempt{{
			Provider:    "openai",
			Model:       "gpt-4o",
			IdentityKey: "account:account-a",
			Reason:      providers.FailoverRateLimit,
			Error:       errors.New("rate limited"),
		}},
	}, nil)

	next := router.Select("session-1", SelectReasonInitial)
	if got := next.Candidates[0].StableKey(); got != "account:account-b" {
		t.Fatalf("next candidate = %q, want account:account-b", got)
	}
}

func TestLoadBalancerFallbackToAccountFallbackToLoadBalancer(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	cfg := &config.ModelRouterConfig{
		Enabled: true,
		Entry:   "primary-pool",
		Blocks: []config.ModelRouterBlock{
			{
				ID:       "primary-pool",
				Type:     config.ModelRouterBlockTypeLoadBalance,
				Accounts: []string{"missing-primary"},
				Strategy: config.ModelRouterStrategyTokensSpent,
				Fallback: "fallback-account",
			},
			{
				ID:       "fallback-account",
				Type:     config.ModelRouterBlockTypeAccount,
				Account:  "account-b",
				Fallback: "backup-pool",
			},
			{
				ID:       "backup-pool",
				Type:     config.ModelRouterBlockTypeLoadBalance,
				Accounts: []string{"account-c"},
				Strategy: config.ModelRouterStrategyBlind,
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	router := New("router-main", cfg, map[string]Account{
		"account-b": {
			Candidates: []providers.FallbackCandidate{candidate("account-b")},
			RPM:        60,
		},
		"account-c": {
			Candidates: []providers.FallbackCandidate{candidate("account-c")},
			RPM:        60,
		},
	}, filepath.Join(t.TempDir(), "model_router_state.json"))
	if router == nil {
		t.Fatal("New() returned nil")
	}
	router.now = func() time.Time { return now }

	selection := router.Select("session-1", SelectReasonInitial)
	if len(selection.Candidates) != 2 {
		t.Fatalf("len(candidates) = %d, want 2", len(selection.Candidates))
	}
	if got := selection.Candidates[0].StableKey(); got != "account:account-b" {
		t.Fatalf("first candidate = %q, want account-b", got)
	}
	if got := selection.Candidates[1].StableKey(); got != "account:account-c" {
		t.Fatalf("second candidate = %q, want account-c", got)
	}
	if got := selection.BlockAccountChoices["fallback-account"]; got != "account-b" {
		t.Fatalf("fallback-account choice = %q, want account-b", got)
	}
	if got := selection.BlockAccountChoices["backup-pool"]; got != "account-c" {
		t.Fatalf("backup-pool choice = %q, want account-c", got)
	}
}

func TestClosestLimitUsesCurrentMinuteWindow(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	router := newTestRouter(t, &config.ModelRouterConfig{
		Enabled: true,
		Entry:   "pool",
		Blocks: []config.ModelRouterBlock{{
			ID:       "pool",
			Type:     config.ModelRouterBlockTypeLoadBalance,
			Accounts: []string{"account-a", "account-b"},
			Strategy: config.ModelRouterStrategyClosestLimit,
		}},
	}, now)
	currentNow := now
	router.now = func() time.Time { return currentNow }

	first := router.Select("session-a", SelectReasonInitial)
	if got := selectedAccount(t, first); got != "account-a" {
		t.Fatalf("initial account = %q, want account-a", got)
	}
	router.RecordFallbackResult(first, successResult(first, 0), nil)

	second := router.Select("session-b", SelectReasonInitial)
	if got := selectedAccount(t, second); got != "account-b" {
		t.Fatalf("current-window account = %q, want account-b", got)
	}

	currentNow = now.Add(time.Minute + time.Second)
	afterWindow := router.Select("session-c", SelectReasonInitial)
	if got := selectedAccount(t, afterWindow); got != "account-a" {
		t.Fatalf("after-window account = %q, want account-a", got)
	}
}

func TestBlindNonSessionChoiceRotatesByInterval(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	router := newTestRouter(t, &config.ModelRouterConfig{
		Enabled:                true,
		Entry:                  "pool",
		RefreshIntervalSeconds: 60,
		Blocks: []config.ModelRouterBlock{{
			ID:       "pool",
			Type:     config.ModelRouterBlockTypeLoadBalance,
			Accounts: []string{"account-a", "account-b"},
			Strategy: config.ModelRouterStrategyBlind,
		}},
	}, now)
	currentNow := now
	router.now = func() time.Time { return currentNow }

	first := router.Select("", SelectReasonInitial)
	if got := selectedAccount(t, first); got != "account-a" {
		t.Fatalf("initial blind account = %q, want account-a", got)
	}
	sameWindow := router.Select("", SelectReasonInitial)
	if got := selectedAccount(t, sameWindow); got != "account-a" {
		t.Fatalf("same-window blind account = %q, want account-a", got)
	}
	currentNow = now.Add(61 * time.Second)
	nextWindow := router.Select("", SelectReasonInitial)
	if got := selectedAccount(t, nextWindow); got != "account-b" {
		t.Fatalf("next-window blind account = %q, want account-b", got)
	}
}

func TestFallbackAttemptIdentityKeepsSameProviderModelAccountsSeparate(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	router := newTestRouter(t, &config.ModelRouterConfig{
		Enabled: true,
		Entry:   "entry",
		Blocks: []config.ModelRouterBlock{
			{
				ID:       "entry",
				Type:     config.ModelRouterBlockTypeAccount,
				Account:  "account-a",
				Fallback: "fallback",
			},
			{
				ID:      "fallback",
				Type:    config.ModelRouterBlockTypeAccount,
				Account: "account-b",
			},
		},
	}, now)

	selection := router.Select("", SelectReasonInitial)
	router.RecordFallbackResult(selection, &providers.FallbackResult{
		Response:    &providers.LLMResponse{Content: "ok"},
		Provider:    "openai",
		Model:       "gpt-4o",
		IdentityKey: "account:account-b",
		Attempts: []providers.FallbackAttempt{{
			Provider:    "openai",
			Model:       "gpt-4o",
			IdentityKey: "account:account-a",
			Reason:      providers.FailoverRateLimit,
			Error:       errors.New("rate limited"),
		}},
	}, nil)

	next := router.Select("", SelectReasonInitial)
	if got := next.Candidates[0].StableKey(); got != "account:account-b" {
		t.Fatalf("candidate after failure = %q, want account:account-b", got)
	}
}

func TestStoreRenamesCorruptStateAndContinues(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "model_router_state.json")
	if err := os.WriteFile(statePath, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	router := New("router-main", &config.ModelRouterConfig{
		Enabled: true,
		Entry:   "entry",
		Blocks: []config.ModelRouterBlock{{
			ID:      "entry",
			Type:    config.ModelRouterBlockTypeAccount,
			Account: "account-a",
		}},
	}, map[string]Account{
		"account-a": {
			Candidates: []providers.FallbackCandidate{candidate("account-a")},
		},
	}, statePath)
	if router == nil {
		t.Fatal("New() returned nil")
	}

	selection := router.Select("", SelectReasonInitial)
	if got := selectedAccount(t, selection); got != "account-a" {
		t.Fatalf("selected account = %q, want account-a", got)
	}

	matches, err := filepath.Glob(statePath + ".corrupt.*")
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("corrupt backups = %v, want one", matches)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state file not rewritten: %v", err)
	}
}

func newTestRouter(t *testing.T, cfg *config.ModelRouterConfig, now time.Time) *Router {
	t.Helper()
	router := New("router-main", cfg, map[string]Account{
		"account-a": {
			Candidates: []providers.FallbackCandidate{candidate("account-a")},
			RPM:        60,
		},
		"account-b": {
			Candidates: []providers.FallbackCandidate{candidate("account-b")},
			RPM:        60,
		},
	}, filepath.Join(t.TempDir(), "model_router_state.json"))
	if router == nil {
		t.Fatal("New() returned nil")
	}
	router.now = func() time.Time { return now }
	return router
}

func candidate(account string) providers.FallbackCandidate {
	return providers.FallbackCandidate{
		Provider:    "openai",
		Model:       "gpt-4o",
		DisplayName: account,
		IdentityKey: "account:" + account,
	}
}

func selectedAccount(t *testing.T, selection Selection) string {
	t.Helper()
	if len(selection.Candidates) == 0 {
		t.Fatal("selection has no candidates")
	}
	account := selection.CandidateAccounts[selection.Candidates[0].StableKey()]
	if account == "" {
		t.Fatalf("candidate %q has no account mapping", selection.Candidates[0].StableKey())
	}
	return account
}

func successResult(selection Selection, totalTokens int) *providers.FallbackResult {
	candidate := selection.Candidates[0]
	return &providers.FallbackResult{
		Response: &providers.LLMResponse{
			Content: "ok",
			Usage:   &providers.UsageInfo{TotalTokens: totalTokens},
		},
		Provider:    candidate.Provider,
		Model:       candidate.Model,
		IdentityKey: candidate.StableKey(),
	}
}
