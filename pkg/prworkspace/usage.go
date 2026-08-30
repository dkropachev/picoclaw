package prworkspace

import (
	"errors"
	"math"
)

const ImplementationUsageScope = "implementation_lifetime"

// TokenUsage is normalized provider usage. Cached tokens are a subset of
// prompt tokens, reasoning tokens are a subset of completion tokens, and the
// total is always prompt plus completion.
type TokenUsage struct {
	ProviderCalls      int64 `json:"provider_calls"`
	UsageReportedCalls int64 `json:"usage_reported_calls"`
	PromptTokens       int64 `json:"prompt_tokens"`
	CachedTokens       int64 `json:"cached_tokens"`
	CompletionTokens   int64 `json:"completion_tokens"`
	ReasoningTokens    int64 `json:"reasoning_tokens"`
	TotalTokens        int64 `json:"total_tokens"`
	LatencyMillis      int64 `json:"latency_millis"`
}

// ImplementationUsage is the cumulative model usage for one workspace's
// implementation lifetime. Tool diagnostics deliberately remain outside this
// durable/public projection.
type ImplementationUsage struct {
	Scope    string     `json:"scope"`
	Complete bool       `json:"complete"`
	Repair   TokenUsage `json:"repair"`
	Audit    TokenUsage `json:"audit"`
	Total    TokenUsage `json:"total"`
}

// UsageMeasurement keeps completeness next to a normalized usage aggregate at
// internal execution boundaries. Complete means every dispatched provider call
// returned valid provider-reported, non-estimated usage, not that the model
// operation itself succeeded.
type UsageMeasurement struct {
	Complete bool
	Usage    TokenUsage
}

func AddUsageMeasurement(total, additional UsageMeasurement) (UsageMeasurement, error) {
	usage, err := AddTokenUsage(total.Usage, additional.Usage)
	if err != nil {
		return UsageMeasurement{}, err
	}
	return UsageMeasurement{Complete: total.Complete && additional.Complete, Usage: usage}, nil
}

func (usage TokenUsage) Validate() error {
	values := [...]int64{
		usage.ProviderCalls,
		usage.UsageReportedCalls,
		usage.PromptTokens,
		usage.CachedTokens,
		usage.CompletionTokens,
		usage.ReasoningTokens,
		usage.TotalTokens,
		usage.LatencyMillis,
	}
	for _, value := range values {
		if value < 0 {
			return errors.New("token usage contains a negative value")
		}
	}
	if usage.UsageReportedCalls > usage.ProviderCalls {
		return errors.New("usage-reported calls exceed provider calls")
	}
	if usage.ProviderCalls == 0 && usage != (TokenUsage{}) {
		return errors.New("token usage without a provider call is invalid")
	}
	if usage.CachedTokens > usage.PromptTokens {
		return errors.New("cached tokens exceed prompt tokens")
	}
	if usage.ReasoningTokens > usage.CompletionTokens {
		return errors.New("reasoning tokens exceed completion tokens")
	}
	if usage.CompletionTokens > math.MaxInt64-usage.PromptTokens ||
		usage.TotalTokens != usage.PromptTokens+usage.CompletionTokens {
		return errors.New("total tokens do not equal prompt plus completion")
	}
	return nil
}

// AddTokenUsage performs checked, invariant-preserving aggregation.
func AddTokenUsage(left, right TokenUsage) (TokenUsage, error) {
	if err := left.Validate(); err != nil {
		return TokenUsage{}, err
	}
	if err := right.Validate(); err != nil {
		return TokenUsage{}, err
	}
	fields := [...][2]int64{
		{left.ProviderCalls, right.ProviderCalls},
		{left.UsageReportedCalls, right.UsageReportedCalls},
		{left.PromptTokens, right.PromptTokens},
		{left.CachedTokens, right.CachedTokens},
		{left.CompletionTokens, right.CompletionTokens},
		{left.ReasoningTokens, right.ReasoningTokens},
		{left.TotalTokens, right.TotalTokens},
		{left.LatencyMillis, right.LatencyMillis},
	}
	var sums [len(fields)]int64
	for index, pair := range fields {
		if pair[1] > math.MaxInt64-pair[0] {
			return TokenUsage{}, errors.New("token usage aggregation overflow")
		}
		sums[index] = pair[0] + pair[1]
	}
	result := TokenUsage{
		ProviderCalls: sums[0], UsageReportedCalls: sums[1],
		PromptTokens: sums[2], CachedTokens: sums[3], CompletionTokens: sums[4],
		ReasoningTokens: sums[5], TotalTokens: sums[6], LatencyMillis: sums[7],
	}
	if err := result.Validate(); err != nil {
		return TokenUsage{}, err
	}
	return result, nil
}

func NewImplementationUsage() ImplementationUsage {
	return ImplementationUsage{Scope: ImplementationUsageScope}
}

func (usage ImplementationUsage) Validate() error {
	if usage.Scope != ImplementationUsageScope {
		return errors.New("implementation usage scope is invalid")
	}
	if err := usage.Repair.Validate(); err != nil {
		return err
	}
	if err := usage.Audit.Validate(); err != nil {
		return err
	}
	total, err := AddTokenUsage(usage.Repair, usage.Audit)
	if err != nil {
		return err
	}
	if total != usage.Total {
		return errors.New("implementation usage total is inconsistent")
	}
	if usage.Complete && (usage.Total.ProviderCalls == 0 ||
		usage.Total.ProviderCalls != usage.Total.UsageReportedCalls) {
		return errors.New("complete implementation usage is missing provider reports")
	}
	return nil
}

func AddImplementationRepair(
	implementation ImplementationUsage,
	repair TokenUsage,
) (ImplementationUsage, error) {
	return addImplementationUsage(implementation, repair, true)
}

func AddImplementationAudit(
	implementation ImplementationUsage,
	audit TokenUsage,
) (ImplementationUsage, error) {
	return addImplementationUsage(implementation, audit, false)
}

func addImplementationUsage(
	implementation ImplementationUsage,
	additional TokenUsage,
	repair bool,
) (ImplementationUsage, error) {
	if implementation.Scope == "" {
		implementation = NewImplementationUsage()
	}
	// Running accumulation is intentionally incomplete. Validate the numeric
	// invariants independently from the terminal completeness bit.
	implementation.Complete = false
	if err := implementation.Validate(); err != nil {
		return ImplementationUsage{}, err
	}
	var err error
	if repair {
		implementation.Repair, err = AddTokenUsage(implementation.Repair, additional)
	} else {
		implementation.Audit, err = AddTokenUsage(implementation.Audit, additional)
	}
	if err != nil {
		return ImplementationUsage{}, err
	}
	implementation.Total, err = AddTokenUsage(implementation.Repair, implementation.Audit)
	if err != nil {
		return ImplementationUsage{}, err
	}
	return implementation, nil
}

func FinalizeImplementationUsage(
	implementation ImplementationUsage,
	measurementsComplete bool,
) (ImplementationUsage, error) {
	if implementation.Scope == "" {
		implementation = NewImplementationUsage()
	}
	implementation.Complete = false
	if err := implementation.Validate(); err != nil {
		return ImplementationUsage{}, err
	}
	implementation.Complete = measurementsComplete && implementation.Total.ProviderCalls > 0 &&
		implementation.Total.ProviderCalls == implementation.Total.UsageReportedCalls
	if err := implementation.Validate(); err != nil {
		return ImplementationUsage{}, err
	}
	return implementation, nil
}
