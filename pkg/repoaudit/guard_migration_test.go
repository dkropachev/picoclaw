package repoaudit

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestLegacyRepositoryReviewBudgetMigratesToGuardExpression(t *testing.T) {
	data := []byte(`{
      "budget": {
        "max_total_tokens": 500000,
        "max_estimated_cost_usd": 25,
        "account_ids": ["openai:work"],
        "min_remaining_percent_by_window": {"Weekly": 10},
        "pause_on_unknown": true,
        "check_interval_seconds": 30
      }
    }`)
	var decoded struct {
		AccountRef string                       `json:"account_ref"`
		Budget     RepositoryReviewBudgetPolicy `json:"budget"`
	}
	migrated, err := unmarshalRepositoryReviewGuardState(data, &decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated || decoded.AccountRef != "" {
		t.Fatalf("migration=%v account_ref=%q", migrated, decoded.AccountRef)
	}
	for _, expected := range []string{
		"spent.tokens.total < 500000",
		"spend.total.usd < 25",
		"account.limits.weekly.remaining_percent >= 10",
	} {
		if !strings.Contains(decoded.Budget.GuardExpression, expected) {
			t.Fatalf("migrated expression %q missing %q", decoded.Budget.GuardExpression, expected)
		}
	}
	if validationErr := ValidateRepositoryReviewGuardExpression(decoded.Budget.GuardExpression); validationErr != nil {
		t.Fatalf("migrated expression is invalid: %v", validationErr)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{
		"max_total_tokens", "max_estimated_cost_usd", "account_ids", "check_interval_seconds",
	} {
		if strings.Contains(string(encoded), retired) {
			t.Fatalf("new budget JSON retained %q: %s", retired, encoded)
		}
	}
}

func TestLegacyFailOpenLimitPolicyPreservesUnknownTelemetryBehavior(t *testing.T) {
	data := []byte(`{"budget":{"min_remaining_percent_by_window":{"weekly":10},"pause_on_unknown":false}}`)
	var decoded struct {
		Budget RepositoryReviewBudgetPolicy `json:"budget"`
	}
	if _, err := unmarshalRepositoryReviewGuardState(data, &decoded); err != nil {
		t.Fatal(err)
	}
	allowed, err := EvaluateRepositoryReviewGuardExpression(
		decoded.Budget.GuardExpression,
		RepositoryReviewGuardEnvironment{AccountLimitsKnown: false},
	)
	if err != nil || !allowed {
		t.Fatalf("legacy fail-open expression=%q allowed=%v err=%v", decoded.Budget.GuardExpression, allowed, err)
	}
}

func TestLegacyMultiAccountPolicyMigratesFailClosed(t *testing.T) {
	data := []byte(`{"budget":{"account_ids":["openai:work","openai:backup"],"min_remaining_percent":10}}`)
	var decoded struct {
		Budget RepositoryReviewBudgetPolicy `json:"budget"`
	}
	if _, err := unmarshalRepositoryReviewGuardState(data, &decoded); err != nil {
		t.Fatal(err)
	}
	allowed, err := EvaluateRepositoryReviewGuardExpression(
		decoded.Budget.GuardExpression,
		RepositoryReviewGuardEnvironment{AccountLimitsKnown: true},
	)
	if err != nil || allowed || !strings.Contains(decoded.Budget.GuardExpression, "false") {
		t.Fatalf("multi-account expression=%q allowed=%v err=%v", decoded.Budget.GuardExpression, allowed, err)
	}
}

func TestLegacyFailOpenPolicyStillBlocksKnownLowLimit(t *testing.T) {
	data := []byte(`{"budget":{"min_remaining_percent_by_window":{"weekly":10},"pause_on_unknown":false}}`)
	var decoded struct {
		Budget RepositoryReviewBudgetPolicy `json:"budget"`
	}
	if _, err := unmarshalRepositoryReviewGuardState(data, &decoded); err != nil {
		t.Fatal(err)
	}
	five := 5.0
	allowed, err := EvaluateRepositoryReviewGuardExpression(
		decoded.Budget.GuardExpression,
		RepositoryReviewGuardEnvironment{
			AccountLimitsKnown: false,
			AccountLimitSnapshots: []RepositoryReviewAccountLimitSnapshot{
				{AccountID: "work", Window: "weekly", RemainingPercent: &five},
				{AccountID: "backup", Window: "weekly"},
			},
		},
	)
	if err != nil || allowed {
		t.Fatalf("known-low fail-open expression=%q allowed=%v err=%v", decoded.Budget.GuardExpression, allowed, err)
	}
}

func TestLegacyLargeWindowPolicyMigratesToValidFailClosedExpression(t *testing.T) {
	windows := make(map[string]float64, 32)
	for index := 0; index < 32; index++ {
		windows["window_"+strconv.Itoa(index)] = 10
	}
	budget, err := json.Marshal(map[string]any{
		"min_remaining_percent_by_window": windows,
		"pause_on_unknown":                false,
	})
	if err != nil {
		t.Fatal(err)
	}
	data := append(append([]byte(`{"budget":`), budget...), '}')
	var decoded struct {
		Budget RepositoryReviewBudgetPolicy `json:"budget"`
	}
	if _, err := unmarshalRepositoryReviewGuardState(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Budget.GuardExpression != "false" ||
		ValidateRepositoryReviewGuardExpression(decoded.Budget.GuardExpression) != nil {
		t.Fatalf("large policy expression=%q", decoded.Budget.GuardExpression)
	}
}

func TestLegacyWildcardWindowMigratesToAnyLimit(t *testing.T) {
	data := []byte(`{"budget":{"min_remaining_percent_by_window":{"*":25},"pause_on_unknown":true}}`)
	var decoded struct {
		Budget RepositoryReviewBudgetPolicy `json:"budget"`
	}
	if _, err := unmarshalRepositoryReviewGuardState(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(decoded.Budget.GuardExpression, "account.limits.any.remaining_percent >= 25") {
		t.Fatalf("wildcard expression=%q", decoded.Budget.GuardExpression)
	}
}

func TestRepositoryReviewGuardMigrationReadBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		data      string
		wantErr   bool
		wantMoved bool
		wantGuard string
	}{
		{name: "invalid root", data: `{`, wantErr: true},
		{name: "absent budget", data: `{"name":"profile"}`},
		{name: "null budget", data: `{"budget":null}`},
		{name: "invalid budget", data: `{"budget":[]}`, wantErr: true},
		{
			name:      "current guard",
			data:      `{"budget":{"guard_expression":"spent.tokens.total < 10"}}`,
			wantGuard: "spent.tokens.total < 10",
		},
		{
			name:      "retired field keeps explicit guard",
			data:      `{"budget":{"guard_expression":"spent.tokens.total < 20","check_interval_seconds":30}}`,
			wantMoved: true, wantGuard: "spent.tokens.total < 20",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var decoded struct {
				Budget RepositoryReviewBudgetPolicy `json:"budget"`
			}
			migrated, err := unmarshalRepositoryReviewGuardState([]byte(test.data), &decoded)
			if (err != nil) != test.wantErr {
				t.Fatalf("migrated=%v error=%v", migrated, err)
			}
			if err == nil && (migrated != test.wantMoved || decoded.Budget.GuardExpression != test.wantGuard) {
				t.Fatalf("migrated=%v guard=%q", migrated, decoded.Budget.GuardExpression)
			}
		})
	}
}

func TestLegacyGuardMigrationHandlesGlobalAndUnusableWindows(t *testing.T) {
	t.Parallel()

	data := []byte(`{"budget":{
		"min_remaining_percent":15,
		"min_remaining_percent_by_window":{"  ":20,"monthly":0},
		"pause_on_unknown":false
	}}`)
	var decoded struct {
		Budget RepositoryReviewBudgetPolicy `json:"budget"`
	}
	if migrated, err := unmarshalRepositoryReviewGuardState(data, &decoded); err != nil || !migrated {
		t.Fatalf("migrated=%v error=%v", migrated, err)
	}
	if !strings.Contains(decoded.Budget.GuardExpression, "account.limits.any.observed") ||
		strings.Contains(decoded.Budget.GuardExpression, "monthly") {
		t.Fatalf("global fail-open guard = %q", decoded.Budget.GuardExpression)
	}

	// The legacy toggle by itself requested fail-closed quota discovery.
	data = []byte(`{"budget":{"pause_on_unknown":true}}`)
	if migrated, err := unmarshalRepositoryReviewGuardState(data, &decoded); err != nil || !migrated {
		t.Fatalf("toggle migration=%v error=%v", migrated, err)
	}
	if decoded.Budget.GuardExpression != "account.limits.known and not account.limits.exhausted" {
		t.Fatalf("toggle-only guard = %q", decoded.Budget.GuardExpression)
	}
}
