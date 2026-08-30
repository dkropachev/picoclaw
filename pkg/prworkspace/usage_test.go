package prworkspace

import (
	"math"
	"testing"
)

func TestTokenUsageValidationAndCheckedAggregation(t *testing.T) {
	valid := TokenUsage{
		ProviderCalls: 1, UsageReportedCalls: 1,
		PromptTokens: 10, CachedTokens: 4,
		CompletionTokens: 6, ReasoningTokens: 2,
		TotalTokens: 16, LatencyMillis: 25,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid usage rejected: %v", err)
	}

	invalid := []TokenUsage{
		{ReasoningTokens: -1},
		{PromptTokens: 1, TotalTokens: 1},
		{PromptTokens: 1, CachedTokens: 2, TotalTokens: 1},
		{CompletionTokens: 1, ReasoningTokens: 2, TotalTokens: 1},
		{ProviderCalls: 1, UsageReportedCalls: 2},
		{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 1},
	}
	for _, usage := range invalid {
		if err := usage.Validate(); err == nil {
			t.Fatalf("invalid usage accepted: %#v", usage)
		}
	}

	overflow := TokenUsage{PromptTokens: math.MaxInt64, TotalTokens: math.MaxInt64}
	if _, err := AddTokenUsage(overflow, TokenUsage{PromptTokens: 1, TotalTokens: 1}); err == nil {
		t.Fatal("usage aggregation overflow was accepted")
	}
}

func TestImplementationUsageKeepsRepairAndAuditTotalsSeparate(t *testing.T) {
	usage := NewImplementationUsage()
	repair := TokenUsage{
		ProviderCalls: 2, UsageReportedCalls: 2,
		PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14,
	}
	audit := TokenUsage{
		ProviderCalls: 3, UsageReportedCalls: 3,
		PromptTokens: 20, CachedTokens: 5,
		CompletionTokens: 6, ReasoningTokens: 2, TotalTokens: 26,
	}
	var err error
	usage, err = AddImplementationRepair(usage, repair)
	if err != nil {
		t.Fatal(err)
	}
	usage, err = AddImplementationAudit(usage, audit)
	if err != nil {
		t.Fatal(err)
	}
	usage, err = FinalizeImplementationUsage(usage, true)
	if err != nil {
		t.Fatal(err)
	}
	if !usage.Complete || usage.Repair != repair || usage.Audit != audit ||
		usage.Total.ProviderCalls != 5 || usage.Total.PromptTokens != 30 ||
		usage.Total.CachedTokens != 5 || usage.Total.CompletionTokens != 10 ||
		usage.Total.TotalTokens != 40 {
		t.Fatalf("implementation usage = %#v", usage)
	}

	usage, err = FinalizeImplementationUsage(usage, false)
	if err != nil || usage.Complete {
		t.Fatalf("incomplete usage = %#v, error = %v", usage, err)
	}
}
