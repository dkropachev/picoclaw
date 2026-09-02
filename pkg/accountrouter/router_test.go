package accountrouter

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestSafetyFilterDoesNotMarkAccountUnavailable(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	router := newTestRouter(t, &config.AccountRouterConfig{
		Enabled: true,
		Entry:   "entry",
		Blocks: []config.AccountRouterBlock{{
			ID: "entry", Type: config.AccountRouterBlockTypeAccount, Account: "account-a",
		}},
	}, now)
	selection := router.Select("", SelectReasonInitial)
	candidate := selection.Candidates[0]
	router.RecordFallbackResult(selection, &providers.FallbackResult{
		Attempts: []providers.FallbackAttempt{{
			Provider: candidate.Provider, Model: candidate.Model,
			IdentityKey: candidate.StableKey(), Reason: providers.FailoverSafetyFilter,
			Error: errors.New("security violation"),
		}},
	}, errors.New("security violation"))
	state := router.store.st.Routers["router-main"].Accounts["account-a"]
	if state != nil && (state.FailureCount != 0 || state.State == "unavailable") {
		t.Fatalf("safety filter poisoned account health: %#v", state)
	}
}

func TestUnattemptedFallbackErrorUsesSelectedCandidateWithoutLeakingPrivateDetails(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	newRouter := func(t *testing.T) *Router {
		t.Helper()
		return newTestRouter(t, &config.AccountRouterConfig{
			Enabled: true,
			Entry:   "entry",
			Blocks: []config.AccountRouterBlock{{
				ID: "entry", Type: config.AccountRouterBlockTypeAccount, Account: "account-a",
			}},
		}, now)
	}

	safetyRouter := newRouter(t)
	safetyRouter.RecordFallbackResult(
		safetyRouter.Select("", SelectReasonInitial), nil, errors.New("content safety filter blocked the request"),
	)
	if state := safetyRouter.store.st.Routers["router-main"].Accounts["account-a"]; state != nil &&
		state.FailureCount != 0 {
		t.Fatalf("unattempted safety filter poisoned health: %#v", state)
	}

	privateRouter := newRouter(t)
	privateRouter.RecordPrivateFallbackResult(
		privateRouter.Select("", SelectReasonInitial), nil, errors.New("secret upstream timeout detail"),
	)
	state := privateRouter.store.st.Routers["router-main"].Accounts["account-a"]
	if state == nil || state.FailureCount != 1 || state.LastError != errPrivateProviderRequest.Error() {
		t.Fatalf("private unattempted failure state = %#v", state)
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

func TestRecordPrivateFallbackResultRedactsProviderErrorAndPreservesHealth(t *testing.T) {
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
	const privateCanary = "PRIVATE-PROVIDER-ERROR-CANARY"
	failure := errors.New("rate limit response echoed " + privateCanary)
	router.RecordPrivateFallbackResult(selection, &providers.FallbackResult{
		Attempts: []providers.FallbackAttempt{{
			Provider:    attempted.Provider,
			Model:       attempted.Model,
			IdentityKey: attempted.StableKey(),
			Reason:      providers.FailoverRateLimit,
			Error:       failure,
		}},
	}, failure)

	state := router.store.st.Routers["router-main"].Accounts["account-a"]
	if state == nil || state.State != "unavailable" ||
		state.Reason != providers.FailoverRateLimit || state.FailureCount != 1 {
		t.Fatalf("private failure health state = %#v", state)
	}
	if state.LastError != errPrivateProviderRequest.Error() {
		t.Fatalf("last error = %q, want canonical private error", state.LastError)
	}
	db := openRawAccountRouterDB(t, router.store.path)
	defer db.Close()
	var persistedError string
	if err := db.QueryRow(`SELECT last_error FROM account_router_accounts
        WHERE router_name = 'router-main' AND account_ref = 'account-a'`).Scan(&persistedError); err != nil {
		t.Fatal(err)
	}
	if persistedError != errPrivateProviderRequest.Error() || strings.Contains(persistedError, privateCanary) {
		t.Fatalf("persisted private error = %q", persistedError)
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

func TestStoreAuditsAndArchivesMalformedLegacyState(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "account_router_state.json")
	legacyData := []byte("{not-json")
	if err := os.WriteFile(statePath, legacyData, 0o600); err != nil {
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

	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("legacy state remains after archive: %v", err)
	}
	archivePath := filepath.Join(
		filepath.Dir(statePath),
		"legacy-json",
		accountRouterLegacyArchiveLabel,
		filepath.Base(statePath),
	)
	archived, err := os.ReadFile(archivePath)
	if err != nil || string(archived) != string(legacyData) {
		t.Fatalf("malformed archive = %q, %v", archived, err)
	}
}

func TestCredentialAuthInvalidationRecoversExactAliasAccountAcrossRoutersAfterRestart(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "account_router_state.json")
	target := "credential:copilot:work"
	sibling := "credential:copilot:worker"
	cfg := &config.AccountRouterConfig{
		Enabled: true,
		Entry:   "target",
		Blocks: []config.AccountRouterBlock{
			{
				ID:       "target",
				Type:     config.AccountRouterBlockTypeAccount,
				Account:  target,
				Fallback: "sibling",
			},
			{
				ID:      "sibling",
				Type:    config.AccountRouterBlockTypeAccount,
				Account: sibling,
			},
		},
	}
	accounts := map[string]Account{
		target: {
			Candidates: []providers.FallbackCandidate{credentialCandidate(target)},
		},
		sibling: {
			Candidates: []providers.FallbackCandidate{credentialCandidate(sibling)},
		},
	}
	newRouter := func(name string) *Router {
		router := New(name, cfg, accounts, statePath)
		if router == nil {
			t.Fatalf("New(%q) returned nil", name)
		}
		return router
	}

	primary := newRouter("router-primary")
	secondary := newRouter("router-secondary")
	primarySelection := primary.Select("", SelectReasonInitial)
	recordSelectionFailure(t, primary, primarySelection, target, providers.FailoverAuth)
	recordSelectionFailure(t, primary, primarySelection, sibling, providers.FailoverAuth)
	secondarySelection := secondary.Select("", SelectReasonInitial)
	recordSelectionFailure(t, secondary, secondarySelection, target, providers.FailoverAuth)

	if err := InvalidateCredentialAuthFailure(statePath, "github-copilot:work"); err != nil {
		t.Fatalf("InvalidateCredentialAuthFailure() error = %v", err)
	}

	// Simulate the gateway starting after the launcher wrote the durable
	// invalidation. The main state file still contains the old failure until a
	// router operation consumes the sidecar generation.
	stores.Delete(primary.store.path)
	restarted := newRouter("router-primary")
	selection := restarted.Select("", SelectReasonInitial)
	if got := selectedAccount(t, selection); got != target {
		t.Fatalf("selected account after restart = %q, want %q", got, target)
	}

	for _, routerName := range []string{"router-primary", "router-secondary"} {
		state := restarted.store.st.Routers[routerName].Accounts[target]
		if state == nil || state.State != "operational" || state.Reason != "" ||
			state.FailureCount != 0 || !state.UnavailableUntil.IsZero() ||
			state.LastError != "" || state.AuthInvalidationGeneration == "" {
			t.Fatalf("%s target state after invalidation = %#v", routerName, state)
		}
		if state.LastFailureAt.IsZero() {
			t.Fatalf("%s target last failure history was erased", routerName)
		}
	}
	siblingState := restarted.store.st.Routers["router-primary"].Accounts[sibling]
	if siblingState == nil || siblingState.State != "unavailable" ||
		siblingState.Reason != providers.FailoverAuth || siblingState.FailureCount != 1 {
		t.Fatalf("prefix sibling state = %#v, want unchanged auth failure", siblingState)
	}
}

func TestCredentialAuthInvalidationPreservesNonAuthHealth(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "account_router_state.json")
	account := "credential:openai:work"
	router := New("router-main", &config.AccountRouterConfig{
		Enabled: true,
		Entry:   "account",
		Blocks: []config.AccountRouterBlock{{
			ID:      "account",
			Type:    config.AccountRouterBlockTypeAccount,
			Account: account,
		}},
	}, map[string]Account{
		account: {Candidates: []providers.FallbackCandidate{{
			Provider:    "openai",
			Model:       "gpt-4o",
			IdentityKey: "account:" + account,
		}}},
	}, statePath)
	if router == nil {
		t.Fatal("New() returned nil")
	}
	selection := router.Select("", SelectReasonInitial)
	recordSelectionFailure(t, router, selection, account, providers.FailoverRateLimit)
	if err := InvalidateCredentialAuthFailure(statePath, "openai:work"); err != nil {
		t.Fatalf("InvalidateCredentialAuthFailure() error = %v", err)
	}
	_ = router.Select("", SelectReasonInitial)

	state := router.store.st.Routers["router-main"].Accounts[account]
	if state == nil || state.State != "unavailable" ||
		state.Reason != providers.FailoverRateLimit || state.FailureCount != 1 ||
		state.AuthInvalidationGeneration == "" {
		t.Fatalf("non-auth state after invalidation = %#v", state)
	}
}

func TestCredentialAuthInvalidationFencesLatePreRenewalFailure(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "account_router_state.json")
	account := "credential:openai:work"
	router := New("router-main", &config.AccountRouterConfig{
		Enabled: true,
		Entry:   "account",
		Blocks: []config.AccountRouterBlock{{
			ID:      "account",
			Type:    config.AccountRouterBlockTypeAccount,
			Account: account,
		}},
	}, map[string]Account{
		account: {Candidates: []providers.FallbackCandidate{{
			Provider:    "openai",
			Model:       "gpt-4o",
			IdentityKey: "account:" + account,
		}}},
	}, statePath)
	if router == nil {
		t.Fatal("New() returned nil")
	}

	staleSelection := router.Select("", SelectReasonInitial)
	if err := InvalidateCredentialAuthFailure(statePath, "openai:work"); err != nil {
		t.Fatalf("InvalidateCredentialAuthFailure() error = %v", err)
	}
	recordSelectionFailure(t, router, staleSelection, account, providers.FailoverAuth)
	state := router.store.st.Routers["router-main"].Accounts[account]
	if state == nil || state.State != "operational" || state.FailureCount != 0 {
		t.Fatalf("late pre-renewal auth result poisoned state: %#v", state)
	}

	// A request selected after renewal carries the new generation and may mark
	// a genuine failure from the replacement credential.
	freshSelection := router.Select("", SelectReasonInitial)
	recordSelectionFailure(t, router, freshSelection, account, providers.FailoverAuth)
	state = router.store.st.Routers["router-main"].Accounts[account]
	if state == nil || state.State != "unavailable" ||
		state.Reason != providers.FailoverAuth || state.FailureCount != 1 {
		t.Fatalf("post-renewal auth failure was not recorded: %#v", state)
	}
}

func TestCredentialAuthInvalidationValidatesInputsAndFilesystemState(t *testing.T) {
	if err := InvalidateCredentialAuthFailure(" ", "openai:work"); err == nil ||
		!strings.Contains(err.Error(), "state path is required") {
		t.Fatalf("empty state path error = %v", err)
	}
	if err := InvalidateCredentialAuthFailure("unused.json", "openai:bad/name"); err == nil ||
		!strings.Contains(err.Error(), "normalize credential id") {
		t.Fatalf("invalid credential error = %v", err)
	}

	t.Run("missing state is a no-op", func(t *testing.T) {
		statePath := filepath.Join(t.TempDir(), "missing", "state.json")
		if err := InvalidateCredentialAuthFailure(statePath, "openai:work"); err != nil {
			t.Fatalf("InvalidateCredentialAuthFailure() error = %v", err)
		}
		if matches, err := filepath.Glob(statePath + ".auth-invalidation.*"); err != nil {
			t.Fatalf("Glob() error = %v", err)
		} else if len(matches) != 0 {
			t.Fatalf("missing state created invalidation files: %v", matches)
		}
	})

	t.Run("stat error", func(t *testing.T) {
		parentFile := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(parentFile, []byte("blocked"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		err := InvalidateCredentialAuthFailure(filepath.Join(parentFile, "state.json"), "openai:work")
		if err == nil || !strings.Contains(err.Error(), "stat account router") {
			t.Fatalf("stat error = %v", err)
		}
	})

	t.Run("generation error", func(t *testing.T) {
		dir := t.TempDir()
		statePath := filepath.Join(dir, "state.json")
		router := New("router", &config.AccountRouterConfig{
			Enabled: true,
			Entry:   "account",
			Blocks: []config.AccountRouterBlock{{
				ID: "account", Type: config.AccountRouterBlockTypeAccount, Account: "account-a",
			}},
		}, map[string]Account{
			"account-a": {Candidates: []providers.FallbackCandidate{candidate("account-a")}},
		}, statePath)
		if router == nil {
			t.Fatal("New() returned nil")
		}
		_ = router.Select("", SelectReasonInitial)
		original := accountRouterRandomRead
		accountRouterRandomRead = func([]byte) (int, error) { return 0, errors.New("injected random failure") }
		t.Cleanup(func() { accountRouterRandomRead = original })
		err := InvalidateCredentialAuthFailure(statePath, "openai:work")
		if err == nil || !strings.Contains(err.Error(), "generate") {
			t.Fatalf("generation error = %v", err)
		}
	})
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

func recordSelectionFailure(
	t *testing.T,
	router *Router,
	selection Selection,
	account string,
	reason providers.FailoverReason,
) {
	t.Helper()
	for _, candidate := range selection.Candidates {
		if selection.CandidateAccounts[candidate.StableKey()] != account {
			continue
		}
		failure := errors.New("provider request failed")
		router.RecordFallbackResult(selection, &providers.FallbackResult{
			Attempts: []providers.FallbackAttempt{{
				Provider:    candidate.Provider,
				Model:       candidate.Model,
				IdentityKey: candidate.StableKey(),
				Reason:      reason,
				Error:       failure,
			}},
		}, failure)
		return
	}
	t.Fatalf("selection has no candidate for account %q", account)
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
