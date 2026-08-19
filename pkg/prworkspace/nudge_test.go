package prworkspace

import "testing"

func TestShouldRunNudgeDoesNotStopAtZeroBeforeMinimum(t *testing.T) {
	policy := DefaultNudgePolicy()
	if !ShouldRunNudge(policy, 0, false) || !ShouldRunNudge(policy, 1, false) {
		t.Fatal("zero-finding round stopped mandatory nudges")
	}
	if ShouldRunNudge(policy, 2, false) {
		t.Fatal("no-novelty round continued after minimum")
	}
	if !ShouldRunNudge(policy, 2, true) {
		t.Fatal("novel finding did not extend nudge search")
	}
	if ShouldRunNudge(policy, 5, true) {
		t.Fatal("nudge search exceeded hard cap")
	}
}

func TestSelectNudgeStrategyColdStartThenUCB1(t *testing.T) {
	first := SelectNudgeStrategy(nil)
	if first != NudgeAcceptanceCriteria {
		t.Fatalf("first strategy = %q", first)
	}
	stats := make([]NudgeStrategyStat, 0, len(nudgeStrategies))
	for _, strategy := range nudgeStrategies {
		stats = append(stats, NudgeStrategyStat{Strategy: strategy, ResolvedRounds: 1, RewardTotal: .25})
	}
	stats[1].RewardTotal = 1
	if got := SelectNudgeStrategy(stats); got != nudgeStrategies[1] {
		t.Fatalf("UCB1 strategy = %q, want %q", got, nudgeStrategies[1])
	}
}

func TestNudgeStrategyStatsUseOnlyDurableResolvedRewards(t *testing.T) {
	zeroFinding := NudgeRoundRecord{
		ID: "pnr_zero", Stage: NudgeReviewSearch, Strategy: NudgeAcceptanceCriteria,
		State: ExecutionSucceeded, NovelFindings: 0,
	}
	reward := .75
	useful := NudgeRoundRecord{
		ID: "pnr_useful", Stage: NudgeReviewSearch, Strategy: NudgeAdversarial,
		State: ExecutionSucceeded, NovelFindings: 1, Reward: &reward,
	}
	otherStage := NudgeRoundRecord{
		ID: "pnr_other", Stage: NudgeImplementationDone, Strategy: NudgeCoverageGaps,
		State: ExecutionSucceeded, Reward: &reward,
	}
	stats := NudgeStrategyStats([]NudgeRoundRecord{zeroFinding, useful, otherStage}, NudgeReviewSearch)
	if len(stats) != 2 {
		t.Fatalf("stats = %#v", stats)
	}
	if stats[0].Strategy != NudgeAcceptanceCriteria || stats[0].Attempts != 1 || stats[0].ResolvedRounds != 0 {
		t.Fatalf("zero finding became reward evidence: %#v", stats[0])
	}
	if stats[1].Strategy != NudgeAdversarial || stats[1].Attempts != 1 || stats[1].ResolvedRounds != 1 ||
		stats[1].RewardTotal != .75 {
		t.Fatalf("resolved stat = %#v", stats[1])
	}
}

func TestSelectNudgeStrategyRotatesUnresolvedAttempts(t *testing.T) {
	stats := []NudgeStrategyStat{{Strategy: NudgeAcceptanceCriteria, Attempts: 1}}
	if got := SelectNudgeStrategy(stats); got == NudgeAcceptanceCriteria {
		t.Fatalf("cold-start repeated unresolved strategy %q", got)
	}
	stats = recordNudgeAttempt(stats, NudgeAdversarial)
	if stats[1].Attempts != 1 {
		t.Fatalf("recorded attempts = %#v", stats)
	}
}

func TestNudgeLearningExamplesPreserveVariantFeedbackWithoutInventingReward(t *testing.T) {
	reward := 1.0
	rounds := []NudgeRoundRecord{
		{
			Stage:         NudgeReviewSearch,
			State:         ExecutionSucceeded,
			Strategy:      NudgeCoverageGaps,
			Challenge:     "found nothing",
			VariantDigest: "zero",
		},
		{
			Stage:            NudgeReviewSearch,
			State:            ExecutionSucceeded,
			Strategy:         NudgeValidation,
			Challenge:        "check evidence",
			VariantDigest:    "useful",
			Reward:           &reward,
			RewardProvenance: "finding_outcomes",
		},
	}
	examples := NudgeLearningExamples(rounds)
	if len(examples) != 2 || examples[0].Reward != nil {
		t.Fatalf("zero-finding feedback gained a reward: %#v", examples)
	}
	if examples[1].Reward == nil || *examples[1].Reward != 1 || examples[1].RewardProvenance != "finding_outcomes" {
		t.Fatalf("resolved feedback missing: %#v", examples[1])
	}
}
