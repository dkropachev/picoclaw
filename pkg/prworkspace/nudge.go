package prworkspace

import (
	"errors"
	"math"
	"reflect"
	"sort"
	"strings"
)

type NudgeStage string

const (
	NudgeReviewSearch       NudgeStage = "review"
	NudgeImplementationDone NudgeStage = "implementation_completion"
)

type NudgeStrategy string

const (
	NudgeAcceptanceCriteria NudgeStrategy = "acceptance_criteria"
	NudgeAdversarial        NudgeStrategy = "adversarial"
	NudgeCoverageGaps       NudgeStrategy = "coverage_gaps"
	NudgeErrorRecovery      NudgeStrategy = "error_recovery"
	NudgeIntegration        NudgeStrategy = "integration_boundaries"
	NudgeValidation         NudgeStrategy = "validation_adequacy"
)

var nudgeStrategies = []NudgeStrategy{
	NudgeAcceptanceCriteria,
	NudgeAdversarial,
	NudgeCoverageGaps,
	NudgeErrorRecovery,
	NudgeIntegration,
	NudgeValidation,
}

const maxNudgeLearningExamples = 32

// NudgeLearningExample is bounded feedback supplied to the isolated wording
// planner. It lets the planner vary wording in light of durable delayed
// outcomes without exposing tools or treating a prior zero-finding pass as a
// successful completeness signal.
type NudgeLearningExample struct {
	Stage            NudgeStage    `json:"stage"`
	Strategy         NudgeStrategy `json:"strategy"`
	VariantDigest    string        `json:"variant_digest"`
	Challenge        string        `json:"challenge"`
	NovelFindings    int           `json:"novel_findings"`
	DuplicateCount   int           `json:"duplicate_count"`
	Reward           *float64      `json:"reward,omitempty"`
	RewardProvenance string        `json:"reward_provenance,omitempty"`
}

func NudgeLearningExamples(rounds []NudgeRoundRecord) []NudgeLearningExample {
	start := len(rounds) - maxNudgeLearningExamples
	if start < 0 {
		start = 0
	}
	examples := make([]NudgeLearningExample, 0, len(rounds)-start)
	for _, round := range rounds[start:] {
		if !validNudgeStrategy(round.Strategy) {
			continue
		}
		example := NudgeLearningExample{
			Stage: round.Stage, Strategy: round.Strategy, VariantDigest: round.VariantDigest,
			Challenge: round.Challenge, NovelFindings: round.NovelFindings,
			DuplicateCount: round.DuplicateCount, RewardProvenance: round.RewardProvenance,
		}
		if round.State == ExecutionSucceeded && round.Reward != nil && !math.IsNaN(*round.Reward) &&
			!math.IsInf(*round.Reward, 0) && *round.Reward >= 0 && *round.Reward <= 1 {
			example.Reward = float64Pointer(*round.Reward)
		}
		examples = append(examples, example)
	}
	return examples
}

type NudgePolicy struct {
	MinimumAdditionalRounds int `json:"minimum_additional_rounds"`
	MaximumAdditionalRounds int `json:"maximum_additional_rounds"`
	// Explicit distinguishes a configured 0/0 policy (nudging disabled) from
	// an omitted request that should receive lifecycle defaults.
	Explicit bool `json:"-"`
}

func DefaultNudgePolicy() NudgePolicy {
	return NudgePolicy{MinimumAdditionalRounds: 2, MaximumAdditionalRounds: 5, Explicit: true}
}

func ConfiguredNudgePolicy(minimum, maximum int) NudgePolicy {
	return NudgePolicy{MinimumAdditionalRounds: minimum, MaximumAdditionalRounds: maximum, Explicit: true}
}

func (policy NudgePolicy) Validate() error {
	if policy.MinimumAdditionalRounds < 0 || policy.MinimumAdditionalRounds > 10 {
		return errors.New("minimum additional nudge rounds must be between 0 and 10")
	}
	if policy.MaximumAdditionalRounds < policy.MinimumAdditionalRounds ||
		policy.MaximumAdditionalRounds > 10 {
		return errors.New("maximum additional nudge rounds must be between the minimum and 10")
	}
	return nil
}

// ShouldRunNudge deliberately ignores a zero-finding initial or early round
// until the configured minimum has completed.
func ShouldRunNudge(policy NudgePolicy, completedAdditional int, previousNovel bool) bool {
	if policy.Validate() != nil || completedAdditional < 0 {
		return false
	}
	if completedAdditional < policy.MinimumAdditionalRounds {
		return true
	}
	return completedAdditional < policy.MaximumAdditionalRounds && previousNovel
}

type NudgeStrategyStat struct {
	Strategy       NudgeStrategy `json:"strategy"`
	Attempts       int           `json:"attempts"`
	ResolvedRounds int           `json:"resolved_rounds"`
	RewardTotal    float64       `json:"reward_total"`
}

func (stat NudgeStrategyStat) MeanReward() float64 {
	if stat.ResolvedRounds <= 0 {
		return 0
	}
	return stat.RewardTotal / float64(stat.ResolvedRounds)
}

// NudgeStrategyStats rebuilds the learning state exclusively from durable
// round records. A round with no adjudicated finding remains unresolved: in
// particular, zero findings is never converted into a successful reward.
func NudgeStrategyStats(rounds []NudgeRoundRecord, stage NudgeStage) []NudgeStrategyStat {
	byStrategy := make(map[NudgeStrategy]NudgeStrategyStat, len(nudgeStrategies))
	for _, round := range rounds {
		if round.Stage != stage || !validNudgeStrategy(round.Strategy) {
			continue
		}
		stat := byStrategy[round.Strategy]
		stat.Strategy = round.Strategy
		stat.Attempts++
		if round.State == ExecutionSucceeded && round.Reward != nil && !math.IsNaN(*round.Reward) && !math.IsInf(*round.Reward, 0) &&
			*round.Reward >= 0 && *round.Reward <= 1 {
			stat.ResolvedRounds++
			stat.RewardTotal += *round.Reward
		}
		byStrategy[round.Strategy] = stat
	}
	stats := make([]NudgeStrategyStat, 0, len(byStrategy))
	for _, strategy := range nudgeStrategies {
		if stat, ok := byStrategy[strategy]; ok {
			stats = append(stats, stat)
		}
	}
	return stats
}

// SelectNudgeStrategy performs deterministic cold-start rotation followed by
// an UCB-style choice. Attempts with delayed outcomes affect exploration but
// do not count as failures and are not added to ResolvedRounds.
func SelectNudgeStrategy(stats []NudgeStrategyStat) NudgeStrategy {
	byStrategy := make(map[NudgeStrategy]NudgeStrategyStat, len(stats))
	for _, stat := range stats {
		if validNudgeStrategy(stat.Strategy) && stat.Attempts >= 0 && stat.ResolvedRounds >= 0 &&
			!math.IsNaN(stat.RewardTotal) && !math.IsInf(stat.RewardTotal, 0) &&
			stat.RewardTotal >= 0 {
			if stat.Attempts < stat.ResolvedRounds {
				stat.Attempts = stat.ResolvedRounds
			}
			byStrategy[stat.Strategy] = stat
		}
	}
	strategies := append([]NudgeStrategy(nil), nudgeStrategies...)
	sort.Slice(strategies, func(i, j int) bool { return strategies[i] < strategies[j] })
	for _, strategy := range strategies {
		if byStrategy[strategy].Attempts == 0 {
			return strategy
		}
	}
	totalAttempts := 0
	resolvedTotal := 0
	resolvedReward := 0.0
	for _, strategy := range strategies {
		stat := byStrategy[strategy]
		totalAttempts += stat.Attempts
		resolvedTotal += stat.ResolvedRounds
		resolvedReward += stat.RewardTotal
	}
	priorMean := .5
	if resolvedTotal > 0 {
		priorMean = resolvedReward / float64(resolvedTotal)
	}
	selected := strategies[0]
	selectedScore := math.Inf(-1)
	for _, strategy := range strategies {
		stat := byStrategy[strategy]
		mean := priorMean
		if stat.ResolvedRounds > 0 {
			mean = stat.MeanReward()
		}
		score := mean + math.Sqrt(
			2*math.Log(float64(totalAttempts+1))/float64(stat.Attempts),
		)
		if score > selectedScore || (score == selectedScore && strategy < selected) {
			selected = strategy
			selectedScore = score
		}
	}
	return selected
}

func recordNudgeAttempt(stats []NudgeStrategyStat, strategy NudgeStrategy) []NudgeStrategyStat {
	out := append([]NudgeStrategyStat(nil), stats...)
	for index := range out {
		if out[index].Strategy == strategy {
			out[index].Attempts++
			return out
		}
	}
	return append(out, NudgeStrategyStat{Strategy: strategy, Attempts: 1})
}

func nudgeStrategyAttempts(stats []NudgeStrategyStat, strategy NudgeStrategy) int {
	for _, stat := range stats {
		if stat.Strategy == strategy {
			if stat.Attempts < stat.ResolvedRounds {
				return stat.ResolvedRounds
			}
			return stat.Attempts
		}
	}
	return 0
}

func validNudgeStrategy(candidate NudgeStrategy) bool {
	for _, strategy := range nudgeStrategies {
		if candidate == strategy {
			return true
		}
	}
	return false
}

type FindingRewardState string

const (
	RewardConfirmedFixed    FindingRewardState = "confirmed_fixed"
	RewardConfirmedDeferred FindingRewardState = "confirmed_deferred"
	RewardRetainedOpen      FindingRewardState = "retained_open"
	RewardRejected          FindingRewardState = "rejected"
)

func NudgeReward(state FindingRewardState) float64 {
	switch state {
	case RewardConfirmedFixed:
		return 1
	case RewardConfirmedDeferred:
		return .75
	case RewardRetainedOpen:
		return .25
	default:
		return 0
	}
}

func nudgeRewardForDisposition(disposition FindingDisposition) (float64, bool) {
	switch disposition {
	case FindingFixed:
		return NudgeReward(RewardConfirmedFixed), true
	case FindingDeferred:
		return NudgeReward(RewardConfirmedDeferred), true
	case FindingInScope, FindingOpen:
		return NudgeReward(RewardRetainedOpen), true
	case FindingDismissed:
		return NudgeReward(RewardRejected), true
	default:
		return 0, false
	}
}

func setFindingNudgeReward(finding Finding, reward float64, source string) Finding {
	if finding.NudgeRoundID == "" || math.IsNaN(reward) || math.IsInf(reward, 0) || reward < 0 || reward > 1 {
		return finding
	}
	finding.NudgeReward = float64Pointer(reward)
	finding.RewardSource = source
	return finding
}

func rewardDeferredFindings(findings []Finding, findingIDs []string, source string) []Finding {
	wanted := make(map[string]struct{}, len(findingIDs))
	for _, id := range findingIDs {
		wanted[id] = struct{}{}
	}
	var updates []Finding
	for _, finding := range findings {
		if _, ok := wanted[finding.ID]; !ok || finding.Disposition != FindingDeferred || finding.NudgeRoundID == "" {
			continue
		}
		updated := setFindingNudgeReward(finding, NudgeReward(RewardConfirmedDeferred), source)
		if updated.RewardSource == finding.RewardSource && updated.NudgeReward != nil && finding.NudgeReward != nil &&
			*updated.NudgeReward == *finding.NudgeReward {
			continue
		}
		updated.Version++
		updates = append(updates, updated)
	}
	return updates
}

// recomputeNudgeRoundRewards derives each round reward from the latest durable
// outcome of its attributed findings. Re-adjudication replaces a contribution
// instead of double counting it. Rounds with no resolved findings keep nil
// reward, including successful zero-finding rounds.
func recomputeNudgeRoundRewards(rounds []NudgeRoundRecord, findings []Finding) []NudgeRoundRecord {
	byID := make(map[string]Finding, len(findings))
	for _, finding := range findings {
		byID[finding.ID] = finding
	}
	replacements := make([]NudgeRoundRecord, 0)
	for _, round := range rounds {
		updated := round
		total := 0.0
		resolved := 0
		sources := make(map[string]struct{})
		for _, findingID := range round.FindingIDs {
			finding, ok := byID[findingID]
			if !ok || finding.NudgeReward == nil || math.IsNaN(*finding.NudgeReward) ||
				math.IsInf(*finding.NudgeReward, 0) || *finding.NudgeReward < 0 || *finding.NudgeReward > 1 {
				continue
			}
			total += *finding.NudgeReward
			resolved++
			if finding.RewardSource != "" {
				sources[finding.RewardSource] = struct{}{}
			}
		}
		updated.ResolvedFindings = resolved
		if resolved == 0 {
			updated.Reward = nil
			updated.RewardProvenance = ""
		} else {
			updated.Reward = float64Pointer(total / float64(resolved))
			provenance := make([]string, 0, len(sources))
			for source := range sources {
				provenance = append(provenance, source)
			}
			sort.Strings(provenance)
			updated.RewardProvenance = strings.Join(provenance, ",")
			if updated.RewardProvenance == "" {
				updated.RewardProvenance = "finding_outcomes"
			}
		}
		if !reflect.DeepEqual(updated, round) {
			replacements = append(replacements, updated)
		}
	}
	return replacements
}

func refreshNudgeRewardsForPatch(aggregate Aggregate, patch *AggregatePatch) {
	if patch == nil {
		return
	}
	findings := upsertByID(aggregate.Findings, patch.UpsertFindings, func(value Finding) string { return value.ID })
	rounds := replaceByID(aggregate.NudgeRounds, patch.ReplaceNudgeRounds, func(value NudgeRoundRecord) string { return value.ID })
	rounds = append(rounds, patch.AppendNudgeRounds...)
	for _, replacement := range recomputeNudgeRoundRewards(rounds, findings) {
		appended := false
		for index := range patch.AppendNudgeRounds {
			if patch.AppendNudgeRounds[index].ID == replacement.ID {
				patch.AppendNudgeRounds[index] = replacement
				appended = true
			}
		}
		if !appended {
			patch.ReplaceNudgeRounds = upsertByID(
				patch.ReplaceNudgeRounds, []NudgeRoundRecord{replacement},
				func(value NudgeRoundRecord) string { return value.ID },
			)
		}
	}
}

func float64Pointer(value float64) *float64 {
	return &value
}
