//go:build !mipsle && !netbsd && !(freebsd && arm)

package eventing

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestEventingPRUsageValidationAndAggregationBoundaries(t *testing.T) {
	left := PRTokenUsage{
		ProviderCalls: 1, UsageReportedCalls: 1, PromptTokens: 10, CachedTokens: 2,
		CompletionTokens: 5, ReasoningTokens: 1, TotalTokens: 15, LatencyMillis: 20,
	}
	right := PRTokenUsage{
		ProviderCalls: 2, UsageReportedCalls: 2, PromptTokens: 20, CachedTokens: 3,
		CompletionTokens: 7, ReasoningTokens: 2, TotalTokens: 27, LatencyMillis: 30,
	}
	total, err := addPRTokenUsage(left, right)
	if err != nil || total.ProviderCalls != 3 || total.TotalTokens != 42 || total.LatencyMillis != 50 {
		t.Fatalf("usage total = %#v, %v", total, err)
	}
	invalid := []PRTokenUsage{
		{ProviderCalls: -1},
		{ProviderCalls: 1, UsageReportedCalls: 2},
		{ProviderCalls: 1, PromptTokens: 1, CachedTokens: 2, CompletionTokens: 1, TotalTokens: 2},
		{ProviderCalls: 1, CompletionTokens: 1, ReasoningTokens: 2, TotalTokens: 1},
		{PromptTokens: 1, TotalTokens: 1},
		{ProviderCalls: 1, PromptTokens: 1, CompletionTokens: 1, TotalTokens: 1},
		{ProviderCalls: 1, PromptTokens: math.MaxInt64, CompletionTokens: 1, TotalTokens: math.MaxInt64},
	}
	for _, value := range invalid {
		if err := validatePRTokenUsage(value); !errors.Is(err, ErrInvalidPRWorkspace) {
			t.Errorf("invalid token usage %#v error = %v", value, err)
		}
	}
	if _, err := addPRTokenUsage(invalid[0], right); !errors.Is(err, ErrInvalidPRWorkspace) {
		t.Fatalf("invalid left aggregation error = %v", err)
	}
	if _, err := addPRTokenUsage(left, invalid[0]); !errors.Is(err, ErrInvalidPRWorkspace) {
		t.Fatalf("invalid right aggregation error = %v", err)
	}
	overflowLeft := PRTokenUsage{
		ProviderCalls: math.MaxInt64, UsageReportedCalls: math.MaxInt64,
		PromptTokens: math.MaxInt64, CachedTokens: math.MaxInt64,
		TotalTokens: math.MaxInt64,
	}
	overflowRight := PRTokenUsage{ProviderCalls: 1, UsageReportedCalls: 1}
	if _, err := addPRTokenUsage(overflowLeft, overflowRight); !errors.Is(err, ErrInvalidPRWorkspace) {
		t.Fatalf("overflow aggregation error = %v", err)
	}
	usage := PRImplementationUsage{
		Scope: "implementation_lifetime", Complete: true, Repair: left, Audit: right, Total: total,
	}
	if err := validatePRImplementationUsage(usage); err != nil {
		t.Fatalf("valid implementation usage error = %v", err)
	}
	usage.Scope = "other"
	if err := validatePRImplementationUsage(usage); !errors.Is(err, ErrInvalidPRWorkspace) {
		t.Fatalf("invalid scope error = %v", err)
	}
	usage.Scope = "implementation_lifetime"
	usage.Total = left
	if err := validatePRImplementationUsage(usage); !errors.Is(err, ErrInvalidPRWorkspace) {
		t.Fatalf("invalid total error = %v", err)
	}
	usage.Total = total
	usage.Total.UsageReportedCalls--
	if err := validatePRImplementationUsage(usage); !errors.Is(err, ErrInvalidPRWorkspace) {
		t.Fatalf("incomplete reports error = %v", err)
	}
	if err := validatePRChangeMetrics(PRChangeMetrics{Files: -1}); !errors.Is(err, ErrInvalidPRWorkspace) {
		t.Fatalf("negative metrics error = %v", err)
	}
}

func TestEventingPRMutationDecodeKindMatrix(t *testing.T) {
	kinds := []PRWorkspaceMutationKind{
		PRMutationWorkspaceState, PRMutationProviderSnapshot, PRMutationCharter,
		PRMutationStageRun, PRMutationFinding, PRMutationFindingEvent,
		PRMutationConversation, PRMutationMessage, PRMutationCorrection,
		PRMutationRepositoryLesson, PRMutationNudgeRound, PRMutationNudgeReward,
		PRMutationDeferredGroup, PRMutationDeferredGroupItem, PRMutationRepairAttempt,
		PRMutationValidationRun, PRMutationGateRun, PRMutationPublication,
		PRMutationOperationIntent, PRMutationIngressWatermark, PRMutationActivity,
	}
	for _, kind := range kinds {
		_, _, err := decodePRWorkspaceMutation(kind, json.RawMessage(`{}`))
		if !errors.Is(err, ErrInvalidPRWorkspace) {
			t.Errorf("zero %s mutation error = %v", kind, err)
		}
	}
	for _, payload := range []json.RawMessage{nil, {1}, json.RawMessage(`{} {}`)} {
		if _, _, err := decodePRWorkspaceMutation(PRMutationFinding, payload); !errors.Is(err, ErrInvalidPRWorkspace) {
			t.Errorf("invalid payload %q error = %v", payload, err)
		}
	}
	if _, _, err := decodePRWorkspaceMutation("unknown", json.RawMessage(`{}`)); !errors.Is(
		err,
		ErrInvalidPRWorkspace,
	) {
		t.Fatalf("unknown mutation error = %v", err)
	}
	if err := decodeStrictPRWorkspaceJSON([]byte(`{"id":"one","id":"two"}`), &PRFinding{}); !errors.Is(
		err,
		ErrInvalidPRWorkspace,
	) {
		t.Fatalf("duplicate key error = %v", err)
	}
}
