package tools

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

type toolPolicyFunc func(context.Context, ToolPolicyRequest) (ToolPolicyDecision, error)

func (fn toolPolicyFunc) EvaluateTool(
	ctx context.Context,
	request ToolPolicyRequest,
) (ToolPolicyDecision, error) {
	return fn(ctx, request)
}

func validToolPolicyRequest() ToolPolicyRequest {
	return ToolPolicyRequest{
		Subject: ToolPolicySubject{
			AgentID: "main", SessionKey: "agent:main:test", TurnID: "turn-1",
			ToolCallID: "call-1", Source: ToolPolicySourceAgentPipeline,
		},
		Tool:        "read_file",
		Arguments:   map[string]any{"path": "README.md"},
		Traits:      ToolTraits{Risk: ToolRiskReadOnly},
		Fulfillment: ToolFulfillmentExecute,
	}
}

func TestEvaluateToolPolicyCompatibilityAllow(t *testing.T) {
	decision, err := EvaluateToolPolicy(
		context.Background(),
		CompatibilityAllowToolPolicy{},
		validToolPolicyRequest(),
	)
	if err != nil {
		t.Fatalf("EvaluateToolPolicy() error = %v", err)
	}
	if decision.Kind != ToolPolicyDecisionAllow || decision.ReasonCode != "compatibility_allow" {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestEvaluateToolPolicyFailsClosed(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name   string
		ctx    context.Context
		policy ToolPolicy
		want   error
	}{
		{name: "nil", ctx: context.Background(), want: ErrToolPolicyUnavailable},
		{name: "typed nil", ctx: context.Background(), policy: (toolPolicyFunc)(nil), want: ErrToolPolicyUnavailable},
		{
			name: "error", ctx: context.Background(), want: ErrToolPolicyUnavailable,
			policy: toolPolicyFunc(func(context.Context, ToolPolicyRequest) (ToolPolicyDecision, error) {
				return ToolPolicyDecision{}, errors.New("secret broker error")
			}),
		},
		{
			name: "panic", ctx: context.Background(), want: ErrToolPolicyUnavailable,
			policy: toolPolicyFunc(func(context.Context, ToolPolicyRequest) (ToolPolicyDecision, error) {
				panic("secret panic")
			}),
		},
		{
			name: "zero decision", ctx: context.Background(), want: ErrToolPolicyUnavailable,
			policy: toolPolicyFunc(func(context.Context, ToolPolicyRequest) (ToolPolicyDecision, error) {
				return ToolPolicyDecision{}, nil
			}),
		},
		{
			name: "invalid reason", ctx: context.Background(), want: ErrToolPolicyUnavailable,
			policy: toolPolicyFunc(func(context.Context, ToolPolicyRequest) (ToolPolicyDecision, error) {
				return ToolPolicyDecision{Kind: ToolPolicyDecisionDeny, ReasonCode: "raw secret!"}, nil
			}),
		},
		{name: "canceled", ctx: canceled, policy: CompatibilityAllowToolPolicy{}, want: context.Canceled},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := EvaluateToolPolicy(test.ctx, test.policy, validToolPolicyRequest())
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err != nil && (test.name == "error" || test.name == "panic") &&
				containsAny(err.Error(), "secret broker error", "secret panic") {
				t.Fatalf("error leaked raw policy detail: %v", err)
			}
		})
	}
}

func TestEvaluateToolPolicyDetachesArgumentsAndNormalizesTraits(t *testing.T) {
	original := map[string]any{
		"nested": map[string]any{"value": "original"},
		"slice":  []any{map[string]any{"value": "original"}},
	}
	request := validToolPolicyRequest()
	request.Arguments = original
	request.Traits = ToolTraits{}

	decision, err := EvaluateToolPolicy(
		context.Background(),
		toolPolicyFunc(func(_ context.Context, got ToolPolicyRequest) (ToolPolicyDecision, error) {
			if got.Traits.Risk != ToolRiskUnknown || got.Traits.Parallel != ToolParallelSerialized ||
				got.Traits.Idempotency != ToolIdempotencyUnknown || got.Traits.Sharing != ToolSharingPerOwner {
				t.Fatalf("normalized traits = %#v", got.Traits)
			}
			got.Arguments["nested"].(map[string]any)["value"] = "policy"
			got.Arguments["slice"].([]any)[0].(map[string]any)["value"] = "policy"
			return ToolPolicyDecision{Kind: ToolPolicyDecisionDeny, ReasonCode: "test_deny"}, nil
		}),
		request,
	)
	if err != nil || decision.Kind != ToolPolicyDecisionDeny {
		t.Fatalf("decision/error = %#v / %v", decision, err)
	}
	if original["nested"].(map[string]any)["value"] != "original" ||
		original["slice"].([]any)[0].(map[string]any)["value"] != "original" {
		t.Fatalf("policy mutated caller arguments: %#v", original)
	}
}

func TestDetachToolArgumentsRejectsUnsafeGraphs(t *testing.T) {
	cycle := map[string]any{}
	cycle["self"] = cycle
	deep := map[string]any{}
	cursor := deep
	for index := 0; index < maxToolPolicyArgumentDepth+2; index++ {
		next := map[string]any{}
		cursor["next"] = next
		cursor = next
	}
	tooManyNodes := make([]any, maxToolPolicyArgumentNodes+1)

	tests := []struct {
		name string
		args map[string]any
	}{
		{name: "cycle", args: cycle},
		{name: "deep", args: deep},
		{name: "nan", args: map[string]any{"value": math.NaN()}},
		{name: "positive infinity", args: map[string]any{"value": math.Inf(1)}},
		{name: "non-string map", args: map[string]any{"value": map[int]any{1: "x"}}},
		{name: "too many nodes", args: map[string]any{"value": tooManyNodes}},
		{name: "too many bytes", args: map[string]any{"value": strings.Repeat("x", maxToolPolicyArgumentBytes+1)}},
		{name: "pointer", args: map[string]any{"value": new(string)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DetachToolArguments(test.args); err == nil {
				t.Fatal("DetachToolArguments() error = nil")
			}
		})
	}
}

func TestEvaluateToolPolicyRejectsUnboundedOrUntrustedMetadata(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ToolPolicyRequest)
	}{
		{name: "tool control", mutate: func(request *ToolPolicyRequest) { request.Tool = "read\nfile" }},
		{name: "tool too long", mutate: func(request *ToolPolicyRequest) {
			request.Tool = strings.Repeat("t", MaxToolPolicyNameLen+1)
		}},
		{name: "call ID empty", mutate: func(request *ToolPolicyRequest) { request.Subject.ToolCallID = "" }},
		{name: "call ID too long", mutate: func(request *ToolPolicyRequest) {
			request.Subject.ToolCallID = strings.Repeat("c", MaxToolPolicyCallIDLen+1)
		}},
		{name: "unsupported source", mutate: func(request *ToolPolicyRequest) {
			request.Subject.Source = ToolPolicySource("model_supplied")
		}},
		{name: "untrusted provenance", mutate: func(request *ToolPolicyRequest) {
			request.Hook = ToolHookProvenance{Name: "hook", Source: "process"}
		}},
		{name: "incomplete trusted provenance", mutate: func(request *ToolPolicyRequest) {
			request.Hook = ToolHookProvenance{Name: "hook", Trusted: true}
		}},
		{name: "trusted hook too long", mutate: func(request *ToolPolicyRequest) {
			request.Hook = ToolHookProvenance{
				Name: strings.Repeat("h", MaxToolPolicyNameLen+1), Source: "process", Trusted: true,
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validToolPolicyRequest()
			test.mutate(&request)
			called := false
			_, err := EvaluateToolPolicy(
				context.Background(),
				toolPolicyFunc(func(context.Context, ToolPolicyRequest) (ToolPolicyDecision, error) {
					called = true
					return ToolPolicyDecision{Kind: ToolPolicyDecisionAllow, ReasonCode: "unexpected"}, nil
				}),
				request,
			)
			if !errors.Is(err, ErrToolPolicyUnavailable) || called {
				t.Fatalf("EvaluateToolPolicy() error/called = %v/%v", err, called)
			}
		})
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
