package accountrouter

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
)

func TestAccountRouterExpressionMetricAndConditionMatrix(t *testing.T) {
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	router := &Router{
		Accounts: map[string]Account{
			"account":   {RPM: 100, Candidates: []providers.FallbackCandidate{candidate("account")}},
			"unlimited": {Candidates: []providers.FallbackCandidate{candidate("unlimited")}},
		},
		now: func() time.Time { return now },
	}
	state := &RouterState{Accounts: map[string]*AccountState{
		"account": {
			Requests: 7, RateWindowStart: now.Add(-10 * time.Second), RateWindowReqs: 25,
			PromptTokens: 11, CompletionTokens: 13, TotalTokens: 24,
		},
	}}

	metrics := map[string]float64{
		"rpm": 100, "requests": 7, "rate_window_reqs": 25,
		"prompt_tokens": 11, "completion_tokens": 13,
		"total_tokens": 24, "tokens_spent": 24, "limit_pressure": .25,
	}
	for metric, want := range metrics {
		got, ok := router.accountMetric(state, "account", metric)
		if !ok || got != want {
			t.Fatalf("metric %s = (%v, %v), want %v", metric, got, ok, want)
		}
	}
	for _, metric := range []string{"requests", "rate_window_reqs", "prompt_tokens", "completion_tokens"} {
		got, ok := router.accountMetric(&RouterState{Accounts: map[string]*AccountState{}}, "account", metric)
		if !ok || got != 0 {
			t.Fatalf("missing metric %s = (%v, %v)", metric, got, ok)
		}
	}
	state.Accounts["account"].RateWindowStart = time.Time{}
	if got, ok := router.accountMetric(state, "account", "rate_window_reqs"); !ok || got != 0 {
		t.Fatalf("zero-window metric = (%v, %v)", got, ok)
	}
	state.Accounts["account"].RateWindowStart = now.Add(-time.Minute)
	if got, ok := router.accountMetric(state, "account", "rate_window_reqs"); !ok || got != 0 {
		t.Fatalf("stale-window metric = (%v, %v)", got, ok)
	}
	for _, input := range []struct{ account, metric string }{
		{"", "rpm"}, {"account", ""}, {"missing", "rpm"}, {"account", "invalid"},
	} {
		if _, ok := router.accountMetric(state, input.account, input.metric); ok {
			t.Fatalf("invalid metric accepted: %#v", input)
		}
	}

	value := func(number float64) config.AccountRouterExpression {
		return config.AccountRouterExpression{Value: &number}
	}
	binary := func(op string, left, right config.AccountRouterExpression) config.AccountRouterExpression {
		return config.AccountRouterExpression{Op: op, Left: &left, Right: &right}
	}
	for name, test := range map[string]struct {
		expr config.AccountRouterExpression
		want float64
		ok   bool
	}{
		"value":        {expr: value(5), want: 5, ok: true},
		"metric":       {expr: config.AccountRouterExpression{Account: "account", Metric: "rpm"}, want: 100, ok: true},
		"add":          {expr: binary(config.AccountRouterMathAdd, value(7), value(3)), want: 10, ok: true},
		"subtract":     {expr: binary(config.AccountRouterMathSubtract, value(7), value(3)), want: 4, ok: true},
		"multiply":     {expr: binary(config.AccountRouterMathMultiply, value(7), value(3)), want: 21, ok: true},
		"divide":       {expr: binary(config.AccountRouterMathDivide, value(7), value(2)), want: 3.5, ok: true},
		"modulo":       {expr: binary(config.AccountRouterMathModulo, value(7), value(3)), want: 1, ok: true},
		"divide zero":  {expr: binary(config.AccountRouterMathDivide, value(7), value(0))},
		"modulo zero":  {expr: binary(config.AccountRouterMathModulo, value(7), value(0))},
		"missing left": {expr: config.AccountRouterExpression{Op: config.AccountRouterMathAdd}},
		"bad left":     {expr: binary(config.AccountRouterMathAdd, config.AccountRouterExpression{Op: "bad"}, value(1))},
		"bad right":    {expr: binary(config.AccountRouterMathAdd, value(1), config.AccountRouterExpression{Op: "bad"})},
		"bad op":       {expr: config.AccountRouterExpression{Op: "pow"}},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := router.evaluateExpression(state, test.expr)
			if ok != test.ok || got != test.want {
				t.Fatalf("evaluateExpression = (%v, %v), want (%v, %v)", got, ok, test.want, test.ok)
			}
		})
	}

	operators := map[string]bool{
		config.AccountRouterBranchOpGT: true, config.AccountRouterBranchOpGTE: true,
		config.AccountRouterBranchOpLT: false, config.AccountRouterBranchOpLTE: false,
		config.AccountRouterBranchOpEQ: false, config.AccountRouterBranchOpNEQ: true,
		"invalid": false,
	}
	for operator, want := range operators {
		condition := &config.AccountRouterCondition{Left: value(2), Operator: operator, Right: value(1)}
		if got := router.evaluateCondition(state, condition); got != want {
			t.Fatalf("condition %s = %v, want %v", operator, got, want)
		}
	}
	if router.evaluateCondition(state, nil) ||
		router.evaluateCondition(state, &config.AccountRouterCondition{
			Left: config.AccountRouterExpression{Op: "bad"}, Right: value(1),
		}) || router.evaluateCondition(state, &config.AccountRouterCondition{
		Left: value(1), Right: config.AccountRouterExpression{Op: "bad"},
	}) {
		t.Fatal("invalid condition evaluated true")
	}
}

//nolint:gocognit // Boundary matrix is intentionally linear.
func TestAccountRouterPureStateAndSelectionBoundaryMatrix(t *testing.T) {
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	accounts := map[string]Account{
		"account-a": {RPM: 60, Candidates: []providers.FallbackCandidate{candidate("account-a")}},
		"account-b": {RPM: 0, Candidates: []providers.FallbackCandidate{candidate("account-b")}},
	}
	router := &Router{
		Name: "router", ConfigHash: "hash", Accounts: accounts,
		Config: config.AccountRouterConfig{Enabled: true, RefreshIntervalSeconds: 1},
		now:    func() time.Time { return now },
	}
	state := &RouterState{
		Accounts: map[string]*AccountState{}, Sessions: map[string]*SessionState{},
	}

	if got := (*Router)(nil).Select("", SelectReasonInitial); got.RouterName != "" {
		t.Fatalf("nil Select = %#v", got)
	}
	disabled := *router
	disabled.Config.Enabled = false
	if got := disabled.Select("", SelectReasonInitial); len(got.Candidates) != 0 {
		t.Fatalf("disabled Select = %#v", got)
	}
	if New("", testAccountRouterConfig(), accounts, filepath.Join(t.TempDir(), "state.json")) != nil ||
		New("router", testAccountRouterConfig(), accounts, "\x00") != nil {
		t.Fatal("New accepted invalid input")
	}
	created, err := NewSQLite("router", testAccountRouterConfig(), map[string]Account{
		"":       {Candidates: []providers.FallbackCandidate{candidate("blank")}},
		"empty":  {},
		" good ": {Candidates: []providers.FallbackCandidate{candidate("good")}},
	}, filepath.Join(privateAccountRouterWorkspace(t), "state.db"))
	if err != nil || len(created.Accounts) != 1 || created.Accounts["good"].Name != "good" {
		t.Fatalf("clean accounts = %#v, %v", created, err)
	}

	selection := &Selection{
		CandidateAccounts: map[string]string{}, ProviderAccounts: map[string]string{},
		BlockAccountChoices: map[string]string{}, accountAuthInvalidationGenerations: map[string]string{},
	}
	if got := router.expandBlock(state, nil, "", "", SelectReasonInitial, map[string]bool{}, selection); got != nil {
		t.Fatalf("empty block expansion = %#v", got)
	}
	if got := router.expandBlock(
		state, nil, "seen", "", SelectReasonInitial, map[string]bool{"seen": true}, selection,
	); got != nil {
		t.Fatalf("cycle expansion = %#v", got)
	}
	if got := router.expandBlock(
		state, nil, "missing", "", SelectReasonInitial, map[string]bool{}, selection,
	); got != nil {
		t.Fatalf("missing block expansion = %#v", got)
	}
	router.Config.Blocks = []config.AccountRouterBlock{{
		ID: "missing-account", Type: config.AccountRouterBlockTypeAccount, Account: "missing",
	}}
	if got := router.expandBlock(
		state, nil, "missing-account", "", SelectReasonInitial, map[string]bool{}, selection,
	); got != nil {
		t.Fatalf("missing account expansion = %#v", got)
	}
	if _, ok := router.block("missing"); ok {
		t.Fatal("missing block was found")
	}
	if candidates := router.accountCandidates(state, "missing"); candidates != nil {
		t.Fatalf("missing candidates = %#v", candidates)
	}

	block := config.AccountRouterBlock{ID: "pool", Accounts: []string{"missing"}}
	if got := router.selectLoadBalancedAccount(state, nil, block, "", SelectReasonInitial); got != "" {
		t.Fatalf("empty load balance = %q", got)
	}
	if got := router.nextBlindAccount(state, block, nil); got != "" {
		t.Fatalf("empty blind account = %q", got)
	}
	state.Blocks = nil
	block.Accounts = []string{"account-a", "account-b"}
	chosen := router.nextBlindAccount(state, block, []string{"account-a", "account-b"})
	if chosen != "account-a" || state.Blocks == nil {
		t.Fatalf("first blind choice = %q, state=%#v", chosen, state.Blocks)
	}
	router.now = func() time.Time { return now.Add(2 * time.Second) }
	if chosen = router.nextBlindAccount(state, block, []string{"account-a", "account-b"}); chosen != "account-b" {
		t.Fatalf("rotated blind choice = %q", chosen)
	}
	router.now = func() time.Time { return now }

	if pressure := router.accountLimitPressure(state, "missing", now); pressure != 0 {
		t.Fatalf("missing pressure = %v", pressure)
	}
	if pressure := router.accountLimitPressure(state, "account-b", now); pressure != 0 {
		t.Fatalf("unlimited pressure = %v", pressure)
	}
	if pressure := router.accountLimitPressure(state, "account-a", now); pressure != 0 {
		t.Fatalf("missing-state pressure = %v", pressure)
	}
	state.Accounts["account-a"] = &AccountState{RateWindowStart: now.Add(-time.Minute), RateWindowReqs: 30}
	if pressure := router.accountLimitPressure(state, "account-a", now); pressure != 0 {
		t.Fatalf("stale pressure = %v", pressure)
	}
	state.Accounts["account-a"].RateWindowStart = now
	if pressure := router.accountLimitPressure(state, "account-a", now); pressure != .5 {
		t.Fatalf("current pressure = %v", pressure)
	}

	if !isAccountOperational(state, "missing", now) {
		t.Fatal("missing account is not operational")
	}
	state.Accounts["account-a"] = &AccountState{
		State: "unavailable", Reason: providers.FailoverRateLimit,
		UnavailableUntil: now.Add(-time.Second),
	}
	if !isAccountOperational(state, "account-a", now) || state.Accounts["account-a"].Reason != "" {
		t.Fatalf("expired account did not recover: %#v", state.Accounts["account-a"])
	}
	state.Accounts["account-a"] = &AccountState{State: "unavailable", UnavailableUntil: now.Add(time.Hour)}
	if isAccountOperational(state, "account-a", now) {
		t.Fatal("future cooldown account is operational")
	}
	if got := router.selectLoadBalancedAccount(
		state,
		nil,
		config.AccountRouterBlock{ID: "fallback", Accounts: []string{"account-a"}},
		"",
		SelectReasonInitial,
	); got != "account-a" {
		t.Fatalf("unavailable fallback account = %q", got)
	}

	blankState := &State{}
	routerState := routerState(blankState, "router", "hash", map[string]bool{"account-a": true})
	if routerState.Accounts["account-a"] == nil || routerState.Sessions == nil || routerState.Blocks == nil {
		t.Fatalf("normalized router state = %#v", routerState)
	}
	routerState.Sessions["nil"] = nil
	routerState.Sessions["wrong"] = &SessionState{ConfigHash: "wrong", UpdatedAt: now}
	routerState.Sessions["old"] = &SessionState{ConfigHash: "hash", UpdatedAt: now.Add(-sessionStateTTL - time.Second)}
	routerState.Sessions["keep"] = &SessionState{ConfigHash: "hash", UpdatedAt: now}
	routerState.Accounts["removed"] = &AccountState{}
	pruneRouterState(routerState, now, "hash", map[string]bool{"account-a": true})
	if len(routerState.Sessions) != 1 || routerState.Sessions["keep"] == nil || routerState.Accounts["removed"] != nil {
		t.Fatalf("pruned state = %#v", routerState)
	}
	if sessionState(routerState, "", "hash", now) != nil {
		t.Fatal("empty session created state")
	}
	routerState.Sessions["sparse"] = &SessionState{ConfigHash: "hash", UpdatedAt: now}
	if session := sessionState(routerState, "sparse", "hash", now); session.Blocks == nil {
		t.Fatal("sparse session blocks remain nil")
	}
	if accountAuthInvalidationGeneration(nil, "account") != "" ||
		accountAuthInvalidationGeneration(&RouterState{}, "account") != "" {
		t.Fatal("missing invalidation generation is non-empty")
	}

	markAccountFailure(routerState, "", providers.FailoverUnknown, errors.New("ignored"), now)
	markAccountFailure(routerState, "new", providers.FailoverNetwork, nil, now)
	if routerState.Accounts["new"] == nil || routerState.Accounts["new"].LastError != "" {
		t.Fatalf("new failure = %#v", routerState.Accounts["new"])
	}
	markAccountSuccess(routerState, "", nil, now)
	markAccountSuccess(routerState, "success", nil, now)
	if routerState.Accounts["success"] == nil || routerState.Accounts["success"].Requests != 1 {
		t.Fatalf("new success = %#v", routerState.Accounts["success"])
	}
	resetAccountAuthFailure(nil)

	if accountTokens(&RouterState{Accounts: map[string]*AccountState{}}, "missing") != 0 ||
		accountTokens(&RouterState{Accounts: map[string]*AccountState{
			"account": {PromptTokens: 2, CompletionTokens: 3},
		}}, "account") != 5 {
		t.Fatal("account token fallback mismatch")
	}
	if got := nonEmptyUnique([]string{"", " a ", "a", "b"}); len(got) != 2 {
		t.Fatalf("nonEmptyUnique = %#v", got)
	}
	if containsString([]string{"a"}, "b") {
		t.Fatal("containsString found absent value")
	}
	if stableIndex("seed", 1) != 0 {
		t.Fatal("stableIndex modulo one mismatch")
	}
	if got := dedupeCandidates([]providers.FallbackCandidate{
		{}, candidate("a"), candidate("a"), candidate("b"),
	}); len(got) != 3 {
		t.Fatalf("dedupeCandidates = %#v", got)
	}
}

func TestAccountRouterFallbackAttributionAndIdentityBoundaryMatrix(t *testing.T) {
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	router := newTestRouter(t, testAccountRouterConfig(), now)
	var nilRouter *Router
	nilRouter.RecordFallbackResult(Selection{RouterName: "router-main"}, nil, errors.New("ignored"))
	router.RecordFallbackResult(Selection{}, nil, errors.New("ignored"))
	router.RecordFallbackResult(Selection{RouterName: "other"}, nil, errors.New("ignored"))

	providerKey := providers.ModelKey("openai", "gpt-4o")
	selection := Selection{
		RouterName: "router-main", SessionKey: "session", Reason: SelectReasonInitial,
		CandidateAccounts: map[string]string{
			candidate("account-a").StableKey(): "account-a",
		},
		ProviderAccounts:                   map[string]string{providerKey: "account-a"},
		BlockAccountChoices:                map[string]string{"pool": "account-b"},
		accountAuthInvalidationGenerations: map[string]string{},
		Candidates: []providers.FallbackCandidate{
			{Provider: "missing", Model: "missing", IdentityKey: "missing"},
			candidate("account-a"),
		},
	}
	router.RecordFallbackResult(selection, &providers.FallbackResult{Attempts: []providers.FallbackAttempt{
		{Provider: "openai", Model: "gpt-4o"},
		{Provider: "openai", Model: "gpt-4o", Reason: providers.FailoverSafetyFilter, Error: errors.New("safe")},
		{
			Provider: "missing", Model: "missing", IdentityKey: "unknown",
			Reason: providers.FailoverNetwork, Error: errors.New("unknown"),
		},
		{
			Provider: "openai", Model: "gpt-4o", IdentityKey: "provider-fallback",
			Reason: providers.FailoverNetwork, Error: errors.New("network"),
		},
	}}, nil)
	state, ok := router.AccountStateSnapshot("account-a")
	if !ok || state.FailureCount != 1 {
		t.Fatalf("provider fallback attribution = (%#v, %v)", state, ok)
	}

	router.RecordFallbackResult(selection, &providers.FallbackResult{
		Response: &providers.LLMResponse{Content: "ok"}, Provider: "openai", Model: "gpt-4o",
	}, nil)
	state, ok = router.AccountStateSnapshot("account-a")
	if !ok || state.Requests != 1 {
		t.Fatalf("provider success attribution = (%#v, %v)", state, ok)
	}
	router.RecordFallbackResult(selection, &providers.FallbackResult{
		Response: &providers.LLMResponse{Content: "unmapped"}, Provider: "other", Model: "other",
	}, nil)

	// First candidate has no account mapping and must not prevent the mapped
	// second candidate from receiving an unattempted classified failure.
	router.RecordPrivateFallbackResult(selection, nil, errors.New("secret upstream timeout detail"))
	state, ok = router.AccountStateSnapshot("account-a")
	if !ok || state.LastError != errPrivateProviderRequest.Error() {
		t.Fatalf("private unattempted attribution = (%#v, %v)", state, ok)
	}

	if _, err := normalizeCredentialID(""); err == nil {
		t.Fatal("blank credential ID was accepted")
	}
	if _, err := normalizeCredentialID("openai:bad/name"); err == nil {
		t.Fatal("malformed credential ID was accepted")
	}
	for _, account := range []string{"account-a", "credential:unsupported:value", "credential:openai:bad/name"} {
		if _, ok := normalizedCredentialIDForAccount(account); ok {
			t.Fatalf("invalid credential account %q normalized", account)
		}
	}

	sparse := &State{Routers: map[string]*RouterState{"router": {
		ConfigHash: "hash",
		Accounts:   nil,
		Sessions:   nil,
		Blocks:     nil,
	}}}
	got := routerState(sparse, "router", "hash", map[string]bool{"account": true})
	if got.Accounts == nil || got.Sessions == nil || got.Blocks == nil {
		t.Fatalf("sparse router state = %#v", got)
	}
}
