package accountrouter

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
	router := newTestRouter(t, &config.AccountRouterConfig{
		Enabled: true,
		Entry:   "pool",
		Blocks: []config.AccountRouterBlock{{
			ID:       "pool",
			Type:     config.AccountRouterBlockTypeLoadBalance,
			Accounts: []string{"account-a", "account-b"},
			Strategy: config.AccountRouterStrategyTokensSpent,
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
	router := newTestRouter(t, &config.AccountRouterConfig{
		Enabled: true,
		Entry:   "entry",
		Blocks: []config.AccountRouterBlock{
			{
				ID:       "entry",
				Type:     config.AccountRouterBlockTypeAccount,
				Account:  "account-a",
				Fallback: "fallback",
			},
			{
				ID:      "fallback",
				Type:    config.AccountRouterBlockTypeAccount,
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

func TestRecordFallbackResultDoesNotDoubleCountRecordedAttempt(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	router := newTestRouter(t, &config.AccountRouterConfig{
		Enabled: true,
		Entry:   "entry",
		Blocks: []config.AccountRouterBlock{{
			ID:      "entry",
			Type:    config.AccountRouterBlockTypeAccount,
			Account: "account-a",
		}},
	}, now)

	selection := router.Select("", SelectReasonInitial)
	attempted := selection.Candidates[0]
	failure := &providers.FailoverError{
		Reason:      providers.FailoverFormat,
		Provider:    attempted.Provider,
		Model:       attempted.Model,
		IdentityKey: attempted.StableKey(),
		Wrapped:     errors.New("bad request"),
	}
	router.RecordFallbackResult(selection, &providers.FallbackResult{
		Attempts: []providers.FallbackAttempt{{
			Provider:    attempted.Provider,
			Model:       attempted.Model,
			IdentityKey: attempted.StableKey(),
			Reason:      failure.Reason,
			Error:       failure,
		}},
	}, failure)

	state := router.store.st.Routers["router-main"].Accounts["account-a"]
	if state == nil {
		t.Fatal("account-a failure state is missing")
	}
	if state.FailureCount != 1 {
		t.Fatalf("failure_count = %d, want one recorded attempt", state.FailureCount)
	}
}

func TestLoadBalancerFallbackToAccountFallbackToLoadBalancer(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	cfg := &config.AccountRouterConfig{
		Enabled: true,
		Entry:   "primary-pool",
		Blocks: []config.AccountRouterBlock{
			{
				ID:       "primary-pool",
				Type:     config.AccountRouterBlockTypeLoadBalance,
				Accounts: []string{"missing-primary"},
				Strategy: config.AccountRouterStrategyTokensSpent,
				Fallback: "fallback-account",
			},
			{
				ID:       "fallback-account",
				Type:     config.AccountRouterBlockTypeAccount,
				Account:  "account-b",
				Fallback: "backup-pool",
			},
			{
				ID:       "backup-pool",
				Type:     config.AccountRouterBlockTypeLoadBalance,
				Accounts: []string{"account-c"},
				Strategy: config.AccountRouterStrategyBlind,
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
	}, filepath.Join(t.TempDir(), "account_router_state.json"))
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

func TestLoadBalanceStrategiesKeepGitHubCopilotCredentialIdentities(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	for _, strategy := range []string{
		config.AccountRouterStrategyBlind,
		config.AccountRouterStrategyTokensSpent,
		config.AccountRouterStrategyClosestLimit,
	} {
		t.Run(strategy, func(t *testing.T) {
			router := newCredentialTestRouter(t, &config.AccountRouterConfig{
				Enabled: true,
				Entry:   "pool",
				Blocks: []config.AccountRouterBlock{{
					ID:       "pool",
					Type:     config.AccountRouterBlockTypeLoadBalance,
					Accounts: []string{"credential:github-copilot:gh-copilot", "credential:github-copilot:backup"},
					Strategy: strategy,
				}},
			}, now)

			selection := router.Select("session-1", SelectReasonInitial)
			selected := selectedAccount(t, selection)
			switch selected {
			case "credential:github-copilot:gh-copilot", "credential:github-copilot:backup":
			default:
				t.Fatalf("selected account = %q, want full Copilot credential ref", selected)
			}
			if got := selection.BlockAccountChoices["pool"]; got != selected {
				t.Fatalf("pool choice = %q, want selected Copilot account %q", got, selected)
			}

			router.RecordFallbackResult(selection, &providers.FallbackResult{
				Response:    &providers.LLMResponse{Content: "ok", Usage: &providers.UsageInfo{TotalTokens: 25}},
				Provider:    "github-copilot",
				Model:       "gpt-5",
				IdentityKey: "account:" + selected,
			}, nil)

			state := router.store.st.Routers["router-main"].Accounts[selected]
			if state == nil {
				t.Fatal("state for full Copilot credential account missing")
			}
			if state.TotalTokens != 25 {
				t.Fatalf("TotalTokens = %d, want 25", state.TotalTokens)
			}
		})
	}
}

func TestClosestLimitUsesCurrentMinuteWindow(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	router := newTestRouter(t, &config.AccountRouterConfig{
		Enabled: true,
		Entry:   "pool",
		Blocks: []config.AccountRouterBlock{{
			ID:       "pool",
			Type:     config.AccountRouterBlockTypeLoadBalance,
			Accounts: []string{"account-a", "account-b"},
			Strategy: config.AccountRouterStrategyClosestLimit,
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

func TestBranchBlockSelectsByAccountLimitMath(t *testing.T) {
	tests := []struct {
		name      string
		operator  string
		right     float64
		want      string
		leftValue config.AccountRouterExpression
	}{
		{
			name:     "greater than",
			operator: config.AccountRouterBranchOpGT,
			right:    50,
			want:     "account-a",
		},
		{
			name:     "less than",
			operator: config.AccountRouterBranchOpLT,
			right:    50,
			want:     "account-b",
		},
		{
			name:     "equal",
			operator: config.AccountRouterBranchOpEQ,
			right:    60,
			want:     "account-a",
		},
		{
			name:     "math add",
			operator: config.AccountRouterBranchOpGT,
			right:    65,
			want:     "account-a",
			leftValue: config.AccountRouterExpression{
				Op: config.AccountRouterMathAdd,
				Left: &config.AccountRouterExpression{
					Account: "account-a",
					Metric:  "rpm",
				},
				Right: &config.AccountRouterExpression{Value: float64Ptr(10)},
			},
		},
	}

	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := tt.leftValue
			if left == (config.AccountRouterExpression{}) {
				left = config.AccountRouterExpression{Account: "account-a", Metric: "rpm"}
			}
			router := newTestRouter(t, &config.AccountRouterConfig{
				Enabled: true,
				Entry:   "limit-branch",
				Blocks: []config.AccountRouterBlock{
					{
						ID:   "limit-branch",
						Type: config.AccountRouterBlockTypeBranch,
						Condition: &config.AccountRouterCondition{
							Left:     left,
							Operator: tt.operator,
							Right:    config.AccountRouterExpression{Value: float64Ptr(tt.right)},
						},
						Then: "more-limit",
						Else: "less-limit",
					},
					{
						ID:      "more-limit",
						Type:    config.AccountRouterBlockTypeAccount,
						Account: "account-a",
					},
					{
						ID:      "less-limit",
						Type:    config.AccountRouterBlockTypeAccount,
						Account: "account-b",
					},
				},
			}, now)

			selection := router.Select("session-1", SelectReasonInitial)
			if got := selectedAccount(t, selection); got != tt.want {
				t.Fatalf("selected account = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBlindNonSessionChoiceRotatesByInterval(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	router := newTestRouter(t, &config.AccountRouterConfig{
		Enabled:                true,
		Entry:                  "pool",
		RefreshIntervalSeconds: 60,
		Blocks: []config.AccountRouterBlock{{
			ID:       "pool",
			Type:     config.AccountRouterBlockTypeLoadBalance,
			Accounts: []string{"account-a", "account-b"},
			Strategy: config.AccountRouterStrategyBlind,
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
	router := newTestRouter(t, &config.AccountRouterConfig{
		Enabled: true,
		Entry:   "entry",
		Blocks: []config.AccountRouterBlock{
			{
				ID:       "entry",
				Type:     config.AccountRouterBlockTypeAccount,
				Account:  "account-a",
				Fallback: "fallback",
			},
			{
				ID:      "fallback",
				Type:    config.AccountRouterBlockTypeAccount,
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
	statePath := filepath.Join(t.TempDir(), "account_router_state.json")
	if err := os.WriteFile(statePath, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	router := New("router-main", &config.AccountRouterConfig{
		Enabled: true,
		Entry:   "entry",
		Blocks: []config.AccountRouterBlock{{
			ID:      "entry",
			Type:    config.AccountRouterBlockTypeAccount,
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

func newTestRouter(t *testing.T, cfg *config.AccountRouterConfig, now time.Time) *Router {
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
	}, filepath.Join(t.TempDir(), "account_router_state.json"))
	if router == nil {
		t.Fatal("New() returned nil")
	}
	router.now = func() time.Time { return now }
	return router
}

func newCredentialTestRouter(t *testing.T, cfg *config.AccountRouterConfig, now time.Time) *Router {
	t.Helper()
	router := New("router-main", cfg, map[string]Account{
		"credential:github-copilot:gh-copilot": {
			Candidates: []providers.FallbackCandidate{credentialCandidate("credential:github-copilot:gh-copilot")},
			RPM:        60,
		},
		"credential:github-copilot:backup": {
			Candidates: []providers.FallbackCandidate{credentialCandidate("credential:github-copilot:backup")},
			RPM:        60,
		},
	}, filepath.Join(t.TempDir(), "account_router_state.json"))
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

func credentialCandidate(account string) providers.FallbackCandidate {
	return providers.FallbackCandidate{
		Provider:    "github-copilot",
		Model:       "gpt-5",
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

func float64Ptr(value float64) *float64 {
	return &value
}
