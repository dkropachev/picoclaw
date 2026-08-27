package api

import (
	"testing"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/repoaudit"
)

func TestRepositoryReviewCoverageCushionHelpers(t *testing.T) {
	previous := repoaudit.RepositoryReviewAutomation{
		AccountRef: "account-a",
		BudgetPolicy: repoaudit.RepositoryReviewBudgetPolicy{
			GuardExpression: "spent.tokens.total < 10",
		},
	}
	next := repoaudit.RepositoryReviewAutomation{
		AccountRef: "account-b", BudgetPolicy: previous.BudgetPolicy,
		AccountLimitSnapshots: []repoaudit.RepositoryReviewAccountLimitSnapshot{{AccountID: "account-a"}},
	}
	repositoryReviewClearStaleAccountLimits(&next, previous)
	if len(next.AccountLimitSnapshots) != 0 {
		t.Fatal("account change did not invalidate account-limit snapshots")
	}
	input, output, ok := repositoryReviewResolvedAliasPrices(
		&config.ModelConfig{Subscription: true, SubscriptionEquivalentModel: "metered"},
		func() (*config.ModelConfig, bool) {
			return &config.ModelConfig{InputPricePerMTok: 1.25, OutputPricePerMTok: 5}, true
		},
	)
	if !ok || input != 1.25 || output != 5 {
		t.Fatalf("inherited prices=(%v, %v, %v)", input, output, ok)
	}
}
