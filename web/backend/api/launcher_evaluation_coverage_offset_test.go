package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/providers"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestLauncherEvaluationCoverageOffsetModelAndAttachmentHelpers(t *testing.T) {
	expression := &config.AccountRouterExpression{
		Op: "add",
		Left: &config.AccountRouterExpression{
			Account: " primary ",
		},
		Right: &config.AccountRouterExpression{
			Op: "multiply",
			Left: &config.AccountRouterExpression{
				Account: "secondary",
			},
		},
	}
	condition := &config.AccountRouterCondition{
		Left: *expression,
		Right: config.AccountRouterExpression{
			Account: "fallback",
		},
	}
	if !accountRouterConditionReferences(condition, "primary") ||
		!accountRouterConditionReferences(condition, "secondary") ||
		!accountRouterConditionReferences(condition, "fallback") ||
		accountRouterConditionReferences(condition, "missing") ||
		accountRouterConditionReferences(nil, "primary") ||
		accountRouterExpressionReferences(nil, "primary") {
		t.Fatal("account-router reference traversal returned an invalid result")
	}
	if got := accountRouterExpressionAccountsForAdaptation(*expression); !reflect.DeepEqual(
		got,
		[]string{"primary", "secondary"},
	) {
		t.Fatalf("adaptation expression accounts = %#v", got)
	}
	branchAccounts := accountRouterBlockAccountsForAdaptation(config.AccountRouterBlock{
		Type: config.AccountRouterBlockTypeBranch, Condition: condition,
	})
	if !reflect.DeepEqual(branchAccounts, []string{"primary", "secondary", "fallback"}) ||
		!reflect.DeepEqual(
			accountRouterBlockAccountsForAdaptation(config.AccountRouterBlock{
				Type: config.AccountRouterBlockTypeAccount, Account: "direct",
			}),
			[]string{"direct"},
		) || !reflect.DeepEqual(
		accountRouterBlockAccountsForAdaptation(config.AccountRouterBlock{
			Type: config.AccountRouterBlockTypeLoadBalance, Accounts: []string{"a", "b"},
		}),
		[]string{"a", "b"},
	) || accountRouterBlockAccountsForAdaptation(config.AccountRouterBlock{Type: "unsupported"}) != nil {
		t.Fatalf("adaptation block accounts = %#v", branchAccounts)
	}

	for _, test := range []struct {
		name     string
		apiBase  string
		wantRoot string
		wantHost string
	}{
		{name: "https default", apiBase: " https://api.example.test/v1 ", wantRoot: "https://api.example.test", wantHost: "api.example.test:443"},
		{name: "http default", apiBase: "http://api.example.test/v1", wantRoot: "http://api.example.test", wantHost: "api.example.test:80"},
		{name: "explicit port", apiBase: "https://api.example.test:8443/v1", wantRoot: "https://api.example.test:8443", wantHost: "api.example.test:8443"},
		{name: "schemeless", apiBase: "localhost:11434/api", wantRoot: "http://localhost:11434", wantHost: "localhost:11434"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, rootErr := apiRootFromAPIBase(test.apiBase)
			host, hostErr := hostPortFromAPIBase(test.apiBase)
			if rootErr != nil || hostErr != nil || root != test.wantRoot || host != test.wantHost {
				t.Fatalf("api base %q -> root=%q host=%q errors=%v/%v", test.apiBase, root, host, rootErr, hostErr)
			}
		})
	}
	for _, invalid := range []string{"", "   ", "://"} {
		if _, err := apiRootFromAPIBase(invalid); err == nil {
			t.Fatalf("apiRootFromAPIBase(%q) accepted invalid value", invalid)
		}
		if _, err := hostPortFromAPIBase(invalid); err == nil {
			t.Fatalf("hostPortFromAPIBase(%q) accepted invalid value", invalid)
		}
	}

	attachments := []struct {
		attachment providers.Attachment
		want       string
	}{
		{attachment: providers.Attachment{ContentType: " image/png "}, want: "image"},
		{attachment: providers.Attachment{Ref: "data:image/jpeg;base64,AA=="}, want: "image"},
		{attachment: providers.Attachment{URL: "data:image/webp;base64,AA=="}, want: "image"},
		{attachment: providers.Attachment{ContentType: "audio/ogg"}, want: "audio"},
		{attachment: providers.Attachment{ContentType: "video/mp4"}, want: "video"},
		{attachment: providers.Attachment{Filename: "photo.SVG"}, want: "image"},
		{attachment: providers.Attachment{Filename: "voice.opus"}, want: "audio"},
		{attachment: providers.Attachment{Filename: "clip.mkv"}, want: "video"},
		{attachment: providers.Attachment{Filename: "notes.txt"}, want: "file"},
	}
	for _, test := range attachments {
		if got := sessionAttachmentType(test.attachment); got != test.want {
			t.Fatalf("sessionAttachmentType(%#v) = %q, want %q", test.attachment, got, test.want)
		}
	}
}

func TestLauncherReviewAccountSelectionAndPriceResolutionBoundaries(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.AccountRef = "review-pool"
	cfg.AccountRouters = []config.AccountRouterConfig{{
		Name: "review-pool", Enabled: true, Entry: "pool",
		Blocks: []config.AccountRouterBlock{{
			ID: "pool", Type: config.AccountRouterBlockTypeLoadBalance,
			Accounts: []string{"account-a", "account-b"},
		}},
	}}
	cfg.ModelAliases = []config.ModelAliasConfig{
		{Name: "review", Model: "openai/review"},
		{Name: "partial", Model: "openai/partial", DisabledAccounts: []string{"account-b"}},
		{Name: "cli-model", Model: "codex-cli/codex"},
	}
	cfg.ModelList = []*config.ModelConfig{
		{
			ModelName: "account-a", Provider: "openai", Model: "openai/review", Enabled: true,
			InputPricePerMTok: 1, OutputPricePerMTok: 4,
		},
		{
			ModelName: "account-b", Provider: "openai", Model: "openai/review", Enabled: true,
			InputPricePerMTok: 2, OutputPricePerMTok: 3,
		},
		{ModelName: "cli-account", Provider: "claude-cli", Model: "claude", Enabled: true},
	}

	if refs := repositoryReviewAccountRefsForSelection(nil, "account-a"); refs != nil {
		t.Fatalf("nil config account refs = %#v", refs)
	}
	if refs := repositoryReviewAccountRefsForSelection(&config.Config{}, ""); refs != nil {
		t.Fatalf("blank account refs = %#v", refs)
	}
	if refs := repositoryReviewAccountRefsForSelection(cfg, "review-pool"); !reflect.DeepEqual(
		refs,
		[]string{"account-a", "account-b"},
	) {
		t.Fatalf("account-router refs = %#v", refs)
	}
	embedded := config.DefaultConfig()
	embedded.ModelList = []*config.ModelConfig{{
		ModelName: "embedded", Enabled: true,
		Router: &config.AccountRouterConfig{
			Entry: "account", Blocks: []config.AccountRouterBlock{{
				ID: "account", Type: config.AccountRouterBlockTypeAccount, Account: "account-a",
			}},
		},
	}}
	if refs := repositoryReviewAccountRefsForSelection(embedded, "embedded"); !reflect.DeepEqual(
		refs,
		[]string{"account-a"},
	) {
		t.Fatalf("embedded account-router refs = %#v", refs)
	}

	if repositoryReviewAliasAvailableForAccount(nil, "review", "account-a") ||
		repositoryReviewAliasAvailableForAccount(cfg, " ", "account-a") ||
		repositoryReviewAliasAvailableForAccount(&config.Config{}, "review", "") ||
		!repositoryReviewAliasAvailableForAccount(cfg, "review", "review-pool") ||
		repositoryReviewAliasAvailableForAccount(cfg, "partial", "review-pool") {
		t.Fatal("account-scoped alias availability was not fail-closed")
	}
	if repositoryReviewAliasUsesAgenticCLIOnAccount(nil, "review", "account-a") ||
		!repositoryReviewAliasUsesAgenticCLIOnAccount(cfg, "cli-model", "account-a") ||
		!repositoryReviewAliasUsesAgenticCLIOnAccount(cfg, "review", "cli-account") ||
		repositoryReviewAliasUsesAgenticCLIOnAccount(cfg, "missing", "account-a") {
		t.Fatal("account-scoped agentic CLI classification mismatch")
	}

	price, known := repositoryReviewAliasPriceForAccount(
		cfg, "review", "review-pool", make(map[string]bool),
	)
	if !known || price.InputPricePerMTok != 2 || price.OutputPricePerMTok != 4 {
		t.Fatalf("conservative account price = %#v, known=%v", price, known)
	}
	for _, test := range []struct {
		name       string
		cfg        *config.Config
		alias      string
		accountRef string
		visiting   map[string]bool
	}{
		{name: "nil config", alias: "review", accountRef: "account-a", visiting: map[string]bool{}},
		{name: "blank alias", cfg: cfg, alias: " ", accountRef: "account-a", visiting: map[string]bool{}},
		{name: "recursive alias", cfg: cfg, alias: "review", accountRef: "account-a", visiting: map[string]bool{"review": true}},
		{name: "missing route", cfg: &config.Config{}, alias: "review", visiting: map[string]bool{}},
		{name: "unknown direct account", cfg: cfg, alias: "review", accountRef: "unknown", visiting: map[string]bool{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, ok := repositoryReviewAliasPriceForAccount(
				test.cfg, test.alias, test.accountRef, test.visiting,
			); got != nil || ok {
				t.Fatalf("unexpected price = %#v, known=%v", got, ok)
			}
		})
	}

	credentialCfg := config.DefaultConfig()
	credentialCfg.Agents.Defaults.AccountRef = "mixed"
	credentialCfg.AccountRouters = []config.AccountRouterConfig{{
		Name: "mixed", Enabled: true, Entry: "pool",
		Blocks: []config.AccountRouterBlock{{
			ID: "pool", Type: config.AccountRouterBlockTypeLoadBalance,
			Accounts: []string{"credential:openai:work", "priced"},
		}},
	}}
	credentialCfg.ModelAliases = []config.ModelAliasConfig{{Name: "review", Model: "openai/review"}}
	credentialCfg.ModelList = []*config.ModelConfig{{
		ModelName: "priced", Provider: "openai", Model: "openai/review", Enabled: true,
		InputPricePerMTok: 3, OutputPricePerMTok: 9,
	}}
	price, known = repositoryReviewAliasPriceForAccount(
		credentialCfg, "review", "mixed", make(map[string]bool),
	)
	if !known || price.InputPricePerMTok != 3 || price.OutputPricePerMTok != 9 {
		t.Fatalf("credential fallback price = %#v, known=%v", price, known)
	}
	credentialCfg.ModelList[0].InputPricePerMTok = 0
	credentialCfg.ModelList[0].OutputPricePerMTok = 0
	if price, known = repositoryReviewAliasPriceForAccount(
		credentialCfg, "review", "mixed", make(map[string]bool),
	); price != nil || known {
		t.Fatalf("unpriced credential fallback = %#v, known=%v", price, known)
	}

	subscriptionCfg := config.DefaultConfig()
	subscriptionCfg.ModelAliases = []config.ModelAliasConfig{
		{Name: "subscription", Model: "openai/subscription"},
		{Name: "equivalent", Model: "openai/equivalent"},
	}
	subscriptionCfg.ModelList = []*config.ModelConfig{{
		ModelName: "subscription-account", Provider: "openai", Enabled: true,
		Subscription: true, SubscriptionEquivalentModel: "equivalent",
	}}
	if price, known = repositoryReviewAliasPriceForAccount(
		subscriptionCfg, "subscription", "subscription-account", make(map[string]bool),
	); price != nil || known {
		t.Fatalf("cyclic subscription price = %#v, known=%v", price, known)
	}
}

func TestLauncherReviewTaskGuardAndStartupBoundaries(t *testing.T) {
	if model := repositoryReviewPlannerModel(repoaudit.RepositoryReviewAutomation{}); model != "" {
		t.Fatalf("empty planner model = %q", model)
	}
	if repositoryReviewAutomationPriceKnown(repoaudit.RepositoryReviewAutomation{}) {
		t.Fatal("automation without reviewers has known pricing")
	}
	addRepositoryReviewGuardReservation(nil, repositoryReviewTaskReservation{TotalTokens: 1})

	controller := &repositoryReviewController{
		ctx: context.Background(), active: make(map[string]*repositoryReviewActiveRun),
		now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}
	if err := controller.observeRepositoryReviewTask("missing", "run", workflows.ManagedChildActivity{
		Phase: "future-phase",
	}); err != nil {
		t.Fatalf("unknown managed-child phase = %v", err)
	}
	if err := controller.observeRepositoryReviewTask("missing", "run", workflows.ManagedChildActivity{
		Phase: workflows.ManagedChildStarted,
	}); !errors.Is(err, errRepositoryReviewSafeStop) {
		t.Fatalf("missing active review task error = %v", err)
	}
	controller.active["paused"] = &repositoryReviewActiveRun{
		runID: "run", pauseReason: repoaudit.RepositoryReviewPauseManual, pauseDetail: "operator pause",
	}
	if err := controller.observeRepositoryReviewTask("paused", "run", workflows.ManagedChildActivity{
		Phase: workflows.ManagedChildStarted,
	}); !errors.Is(err, errRepositoryReviewSafeStop) || !strings.Contains(err.Error(), "operator pause") {
		t.Fatalf("paused review task error = %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.AccountRef = "direct"
	cfg.ModelList = []*config.ModelConfig{{
		ModelName: "direct", Provider: "openai", Enabled: true, CredentialID: "Quota-ID",
	}}
	automation := repoaudit.RepositoryReviewAutomation{AccountRef: "direct"}
	if snapshots, known, err := controller.repositoryReviewGuardAccountLimits(
		t.Context(), nil, repoaudit.RepositoryReviewAutomation{},
	); err == nil || known || snapshots != nil {
		t.Fatalf("missing selected account = (%#v, %v, %v)", snapshots, known, err)
	}
	controller.probe = func(context.Context) (codexAccountLimitsResponse, error) {
		return codexAccountLimitsResponse{Accounts: []codexAccountLimitAccount{{
			ID: "quota-id", LimitsError: "telemetry offline",
		}}}, nil
	}
	snapshots, known, err := controller.repositoryReviewGuardAccountLimits(t.Context(), cfg, automation)
	if err != nil || known || len(snapshots) != 1 || snapshots[0].Detail != "telemetry offline" {
		t.Fatalf("empty account telemetry = (%#v, %v, %v)", snapshots, known, err)
	}

	controller.probe = func(context.Context) (codexAccountLimitsResponse, error) {
		used := 150
		return codexAccountLimitsResponse{Accounts: []codexAccountLimitAccount{{
			ID: "quota-id", Entries: []codexAccountLimitEntry{
				{
					Name: "Weekly", Window: "weekly", Status: "limit_reached",
					RefreshesAt: "2026-08-25T00:00:00Z",
				},
				{Name: "Daily", Window: "daily", Status: "available", UsedPercent: &used},
				{Name: "Monthly", Window: "monthly", Status: "available"},
			},
		}}}, nil
	}
	snapshots, known, err = controller.repositoryReviewGuardAccountLimits(t.Context(), cfg, automation)
	if err != nil || known || len(snapshots) != 3 || snapshots[0].RemainingPercent == nil ||
		*snapshots[0].RemainingPercent != 0 || snapshots[0].ResetsAt.IsZero() ||
		snapshots[1].RemainingPercent == nil || *snapshots[1].RemainingPercent != 0 {
		t.Fatalf("bounded account telemetry = (%#v, %v, %v)", snapshots, known, err)
	}

	controller.probe = func(context.Context) (codexAccountLimitsResponse, error) {
		return codexAccountLimitsResponse{Error: "upstream unavailable"}, nil
	}
	if _, known, err = controller.repositoryReviewGuardAccountLimits(
		t.Context(), cfg, automation,
	); err == nil || known || !strings.Contains(err.Error(), "upstream unavailable") {
		t.Fatalf("response-level telemetry error = (%v, %v)", known, err)
	}
	probeErr := errors.New("probe failed")
	controller.probe = func(context.Context) (codexAccountLimitsResponse, error) {
		return codexAccountLimitsResponse{}, probeErr
	}
	if _, known, err = controller.repositoryReviewGuardAccountLimits(
		t.Context(), cfg, automation,
	); !errors.Is(err, probeErr) || known {
		t.Fatalf("probe telemetry error = (%v, %v)", known, err)
	}
	if ids := repositoryReviewTelemetryIDsForAccountRef(
		nil, "credential:openai:Work",
	); !reflect.DeepEqual(ids, []string{"openai:work"}) {
		t.Fatalf("credential telemetry IDs = %#v", ids)
	}

	temporary := filepath.Join(t.TempDir(), "exists")
	if exists, err := fileExists(temporary); err != nil || exists {
		t.Fatalf("missing file = (%v, %v)", exists, err)
	}
	if err := os.WriteFile(temporary, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if exists, err := fileExists(temporary); err != nil || !exists {
		t.Fatalf("existing file = (%v, %v)", exists, err)
	}
	if got := xmlEscape(`<&>"'`); got != "&lt;&amp;&gt;&quot;&apos;" {
		t.Fatalf("XML escape = %q", got)
	}
	if shellQuote("") != "''" || shellQuote("plain") != "plain" ||
		shellQuote("it's spaced") != `'it'"'"'s spaced'` {
		t.Fatal("shell quoting failed")
	}
	if got := buildLinuxExecLine(
		"/tmp/pico claw",
		[]string{"-d", "it's here"},
	); got != `'/tmp/pico claw' -d 'it'"'"'s here'` {
		t.Fatalf("Linux Exec line = %q", got)
	}
	if got := windowsCommandLine(
		`C:\Program Files\PicoClaw.exe`,
		[]string{"-d", "config file"},
	); got != `"C:\\Program Files\\PicoClaw.exe" "-d" "config file"` {
		t.Fatalf("Windows command line = %q", got)
	}
	if macLaunchAgentPath() == "" || linuxAutoStartPath() == "" || defaultToolFeedbackMaxArgsLength() <= 0 {
		t.Fatal("launcher default paths or feedback bound are unavailable")
	}
}
