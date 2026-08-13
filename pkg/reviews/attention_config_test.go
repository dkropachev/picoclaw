package reviews

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/workflows"
)

func TestConfigAttentionPolicySourceSelectsDetachesAndRevisesExactPolicy(t *testing.T) {
	global := map[string][]workflows.GateSpec{
		"review.submitted": {
			{
				ID: "discussion", Kind: workflows.GateAIWorkingContext,
				AgentID: "main", Criteria: "ask when reviewer intent is ambiguous", Title: "Discuss",
				Questions: map[string]any{"prompt": "What should change?"},
			},
			{
				ID: "security", Kind: workflows.GateAIIsolatedContext,
				AgentID: "reviewer", Criteria: "ask for unresolved security risk", Title: "Security",
			},
			{
				ID: "policy", Kind: workflows.GateDeterministic,
				When: "false", Title: "Policy", Questions: []any{"Approve?"},
			},
			{ID: "off", Kind: workflows.GateZero},
		},
	}
	repositories := map[string]map[string]workflows.RepositoryGatePolicy{
		"Acme/Widgets": {
			"review.submitted": {
				Mode: workflows.GatePolicyOverlay,
				Gates: []workflows.GateSpec{
					{ID: "security", Kind: workflows.GateZero},
					{
						ID: "release", Kind: workflows.GateDeterministic,
						When: "true", Title: "Release", Questions: []any{"Ship?"},
					},
				},
			},
		},
		"Other/Repository": {
			"review.ready": {Mode: workflows.GatePolicyDisable},
		},
	}
	source, err := NewConfigAttentionPolicySource(global, repositories)
	if err != nil {
		t.Fatalf("NewConfigAttentionPolicySource() error = %v", err)
	}
	if !strings.HasPrefix(source.CatalogRevision(), "sha256:") ||
		len(source.CatalogRevision()) != len("sha256:")+64 {
		t.Fatalf("catalog revision = %q", source.CatalogRevision())
	}
	if got := source.AgentIDs(); !reflect.DeepEqual(got, []string{"main", "reviewer"}) {
		t.Fatalf("AgentIDs() = %#v", got)
	}
	if got := source.WorkingContextAgentIDs(); !reflect.DeepEqual(got, []string{"main"}) {
		t.Fatalf("WorkingContextAgentIDs() = %#v", got)
	}

	selector := AttentionPolicySelector{
		Repository: "acme/widgets", DecisionPoint: "review.submitted",
	}
	first := captureConfigAttentionSnapshot(t, source, selector)
	if !strings.HasPrefix(first.Revision, "sha256:") || len(first.Global) != 4 ||
		first.Repository == nil || first.Repository.Mode != workflows.GatePolicyOverlay ||
		len(first.Repository.Gates) != 2 {
		t.Fatalf("first snapshot = %#v", first)
	}
	resolution, err := workflows.ResolveGatePolicy(first.Global, first.Repository)
	if err != nil {
		t.Fatalf("ResolveGatePolicy() error = %v", err)
	}
	if len(resolution.Effective) != 5 || resolution.Effective[1].Kind != workflows.GateZero ||
		resolution.Effective[4].ID != "release" {
		t.Fatalf("effective policy = %#v", resolution.Effective)
	}

	first.Global[0].Criteria = "mutated"
	first.Global[0].Questions.(map[string]any)["prompt"] = "mutated"
	first.Repository.Gates[0].ID = "mutated"
	second := captureConfigAttentionSnapshot(t, source, selector)
	if second.Revision != first.Revision || second.Global[0].Criteria == "mutated" ||
		second.Global[0].Questions.(map[string]any)["prompt"] == "mutated" ||
		second.Repository.Gates[0].ID == "mutated" {
		t.Fatalf("source snapshot was not detached: %#v", second)
	}

	unrelatedRepositories := cloneAttentionPolicyTestRepositories(repositories)
	unrelatedRepositories["Other/Repository"]["review.ready"] = workflows.RepositoryGatePolicy{
		Mode: workflows.GatePolicyReplace,
		Gates: []workflows.GateSpec{{
			ID: "other", Kind: workflows.GateDeterministic,
			When: "true", Title: "Other", Questions: []any{"Other?"},
		}},
	}
	unrelated, err := NewConfigAttentionPolicySource(global, unrelatedRepositories)
	if err != nil {
		t.Fatal(err)
	}
	if got := captureConfigAttentionSnapshot(t, unrelated, selector).Revision; got != second.Revision {
		t.Fatalf("unrelated policy changed selected revision: %q != %q", got, second.Revision)
	}
	if unrelated.CatalogRevision() == source.CatalogRevision() {
		t.Fatal("unrelated policy did not change complete catalog revision")
	}

	selectedGlobal := cloneAttentionPolicyTestGlobal(global)
	selectedGlobal["review.submitted"][0].Criteria = "ask on every ambiguity"
	selected, err := NewConfigAttentionPolicySource(selectedGlobal, repositories)
	if err != nil {
		t.Fatal(err)
	}
	if got := captureConfigAttentionSnapshot(t, selected, selector).Revision; got == second.Revision {
		t.Fatal("selected policy change did not change selected revision")
	}

	reordered := map[string]map[string]workflows.RepositoryGatePolicy{
		"other/repository": repositories["Other/Repository"],
		"acme/widgets":     repositories["Acme/Widgets"],
	}
	stable, err := NewConfigAttentionPolicySource(global, reordered)
	if err != nil {
		t.Fatal(err)
	}
	if stable.CatalogRevision() != source.CatalogRevision() {
		t.Fatalf(
			"repository order/case changed catalog revision: %q != %q",
			stable.CatalogRevision(),
			source.CatalogRevision(),
		)
	}
}

func TestConfigAttentionPolicySourceResolvesEveryRepositoryModeAndNoop(t *testing.T) {
	global := map[string][]workflows.GateSpec{
		"review.ready": {{
			ID: "global", Kind: workflows.GateDeterministic,
			When: "false", Title: "Global", Questions: []any{"Global?"},
		}},
	}
	repositories := map[string]map[string]workflows.RepositoryGatePolicy{
		"acme/inherit": {"review.ready": {Mode: workflows.GatePolicyInherit}},
		"acme/overlay": {"review.ready": {
			Mode:  workflows.GatePolicyOverlay,
			Gates: []workflows.GateSpec{{ID: "global", Kind: workflows.GateZero}},
		}},
		"acme/replace": {"review.ready": {
			Mode:  workflows.GatePolicyReplace,
			Gates: []workflows.GateSpec{{ID: "replacement", Kind: workflows.GateZero}},
		}},
		"acme/disable": {"review.ready": {Mode: workflows.GatePolicyDisable}},
	}
	source, err := NewConfigAttentionPolicySource(global, repositories)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		repository string
		decision   string
		mode       workflows.GatePolicyMode
		gateID     string
		noop       bool
	}{
		{repository: "acme/global", decision: "review.ready", mode: workflows.GatePolicyInherit, gateID: "global"},
		{repository: "ACME/INHERIT", decision: "review.ready", mode: workflows.GatePolicyInherit, gateID: "global"},
		{
			repository: "acme/overlay", decision: "review.ready",
			mode: workflows.GatePolicyOverlay, gateID: "global", noop: true,
		},
		{
			repository: "acme/replace", decision: "review.ready",
			mode: workflows.GatePolicyReplace, gateID: "replacement", noop: true,
		},
		{repository: "acme/disable", decision: "review.ready", mode: workflows.GatePolicyDisable, noop: true},
		{repository: "acme/missing", decision: "review.missing", mode: workflows.GatePolicyInherit, noop: true},
	}
	for _, test := range tests {
		t.Run(test.repository+"/"+test.decision, func(t *testing.T) {
			snapshot := captureConfigAttentionSnapshot(t, source, AttentionPolicySelector{
				Repository: test.repository, DecisionPoint: test.decision,
			})
			resolution, resolveErr := workflows.ResolveGatePolicy(snapshot.Global, snapshot.Repository)
			if resolveErr != nil {
				t.Fatal(resolveErr)
			}
			if resolution.Mode != test.mode {
				t.Fatalf("mode = %q, want %q", resolution.Mode, test.mode)
			}
			if test.gateID != "" && (len(resolution.Effective) != 1 ||
				resolution.Effective[0].ID != test.gateID) {
				t.Fatalf("effective = %#v", resolution.Effective)
			}
			compilation, compileErr := workflows.CompileGateWorkflow(
				"policy test",
				resolution.Effective,
				nil,
			)
			if compileErr != nil || compilation.Noop != test.noop {
				t.Fatalf("CompileGateWorkflow() = (%#v, %v), want noop=%v", compilation, compileErr, test.noop)
			}
		})
	}
}

func TestNamedConfigAttentionPolicySourceSharesAssignmentsAndScopesRevisions(t *testing.T) {
	defaultRules := map[string][]workflows.GateSpec{
		"review.ready": {{
			ID: "default_check", Kind: workflows.GateDeterministic,
			When: "true", Title: "Default", Questions: []any{"Continue?"},
		}},
	}
	strictRules := map[string][]workflows.GateSpec{
		"review.ready": {{
			ID: "strict_check", Kind: workflows.GateAIIsolatedContext,
			AgentID: "reviewer", Criteria: "Ask", Title: "Strict",
		}},
	}
	ruleSets := map[string]NamedAttentionRuleSet{
		"default": {Name: "Default", Rules: defaultRules},
		"strict":  {Name: "Strict", Rules: strictRules},
		"unused": {
			Name: "Unused",
			Rules: map[string][]workflows.GateSpec{
				"review.other": {{
					ID: "working", Kind: workflows.GateAIWorkingContext,
					AgentID: "main", Criteria: "Ask", Title: "Working",
				}},
			},
		},
	}
	assignments := map[string]string{
		"Acme/One": "strict",
		"Acme/Two": "strict",
	}
	source, err := NewNamedConfigAttentionPolicySource(ruleSets, "default", assignments)
	require.NoError(t, err)
	require.Equal(t, []string{"main", "reviewer"}, source.AgentIDs())
	require.Equal(t, []string{"main"}, source.WorkingContextAgentIDs())

	unassigned := captureConfigAttentionSnapshot(t, source, AttentionPolicySelector{
		Repository: "acme/other", DecisionPoint: "review.ready",
	})
	require.Equal(t, "default_check", unassigned.Global[0].ID)
	require.Nil(t, unassigned.Repository)

	first := captureConfigAttentionSnapshot(t, source, AttentionPolicySelector{
		Repository: "acme/one", DecisionPoint: "review.ready",
	})
	second := captureConfigAttentionSnapshot(t, source, AttentionPolicySelector{
		Repository: "ACME/TWO", DecisionPoint: "review.ready",
	})
	require.Equal(t, "strict_check", first.Global[0].ID)
	require.Equal(t, "strict_check", second.Global[0].ID)
	require.NotEqual(t, first.Revision, second.Revision)

	first.Global[0].Criteria = "mutated"
	detached := captureConfigAttentionSnapshot(t, source, AttentionPolicySelector{
		Repository: "acme/one", DecisionPoint: "review.ready",
	})
	require.Equal(t, "Ask", detached.Global[0].Criteria)

	changedDefault := cloneAttentionPolicyTestGlobal(defaultRules)
	changedDefault["review.ready"][0].Title = "Changed default"
	changedSets := map[string]NamedAttentionRuleSet{
		"default": {Name: "Default", Rules: changedDefault},
		"strict":  {Name: "Strict", Rules: strictRules},
		"unused":  ruleSets["unused"],
	}
	changed, err := NewNamedConfigAttentionPolicySource(changedSets, "default", assignments)
	require.NoError(t, err)
	changedAssigned := captureConfigAttentionSnapshot(t, changed, AttentionPolicySelector{
		Repository: "acme/one", DecisionPoint: "review.ready",
	})
	require.Equal(t, detached.Revision, changedAssigned.Revision)
	require.NotEqual(t, source.CatalogRevision(), changed.CatalogRevision())

	renamedUnused := map[string]NamedAttentionRuleSet{
		"default": ruleSets["default"],
		"strict":  ruleSets["strict"],
		"unused":  {Name: "Renamed unused", Rules: ruleSets["unused"].Rules},
	}
	renamed, err := NewNamedConfigAttentionPolicySource(renamedUnused, "default", assignments)
	require.NoError(t, err)
	require.NotEqual(t, source.CatalogRevision(), renamed.CatalogRevision())
	require.Equal(
		t,
		detached.Revision,
		captureConfigAttentionSnapshot(t, renamed, AttentionPolicySelector{
			Repository: "acme/one", DecisionPoint: "review.ready",
		}).Revision,
	)

	reassigned, err := NewNamedConfigAttentionPolicySource(ruleSets, "default", map[string]string{
		"Acme/One": "default",
		"Acme/Two": "strict",
	})
	require.NoError(t, err)
	require.NotEqual(
		t,
		detached.Revision,
		captureConfigAttentionSnapshot(t, reassigned, AttentionPolicySelector{
			Repository: "acme/one", DecisionPoint: "review.ready",
		}).Revision,
	)

	selectedStrict, err := NewNamedConfigAttentionPolicySource(ruleSets, "strict", map[string]string{
		"Acme/Default": "default",
	})
	require.NoError(t, err)
	require.Equal(
		t,
		"strict_check",
		captureConfigAttentionSnapshot(t, selectedStrict, AttentionPolicySelector{
			Repository: "acme/unassigned", DecisionPoint: "review.ready",
		}).Global[0].ID,
	)
	require.Equal(
		t,
		"default_check",
		captureConfigAttentionSnapshot(t, selectedStrict, AttentionPolicySelector{
			Repository: "acme/default", DecisionPoint: "review.ready",
		}).Global[0].ID,
	)
}

func TestConfigAttentionPolicySourceRejectsInvalidCatalogs(t *testing.T) {
	validGate := workflows.GateSpec{
		ID: "gate", Kind: workflows.GateAIIsolatedContext,
		AgentID: "main", Criteria: "decide", Title: "Ask",
	}
	tooManyGlobal := make(map[string][]workflows.GateSpec, maxAttentionPolicyDecisionPoints+1)
	for index := 0; index <= maxAttentionPolicyDecisionPoints; index++ {
		tooManyGlobal[fmt.Sprintf("review.point_%03d", index)] = nil
	}
	tooManyRepositories := make(
		map[string]map[string]workflows.RepositoryGatePolicy,
		maxAttentionPolicyRepositories+1,
	)
	for index := 0; index <= maxAttentionPolicyRepositories; index++ {
		tooManyRepositories[fmt.Sprintf("owner/repo_%04d", index)] = map[string]workflows.RepositoryGatePolicy{
			"review.ready": {Mode: workflows.GatePolicyDisable},
		}
	}
	tooManyRepositoryDecisions := make(
		map[string]workflows.RepositoryGatePolicy,
		maxAttentionPolicyRepositoryDecisionPoints+1,
	)
	for index := 0; index <= maxAttentionPolicyRepositoryDecisionPoints; index++ {
		tooManyRepositoryDecisions[fmt.Sprintf("review.point_%03d", index)] = workflows.RepositoryGatePolicy{
			Mode: workflows.GatePolicyDisable,
		}
	}
	tooManyPolicies := make(
		map[string]map[string]workflows.RepositoryGatePolicy,
		maxAttentionPolicyRepositories,
	)
	for repositoryIndex := 0; repositoryIndex < maxAttentionPolicyRepositories; repositoryIndex++ {
		policies := make(map[string]workflows.RepositoryGatePolicy, 9)
		for policyIndex := 0; policyIndex < 9; policyIndex++ {
			policies[fmt.Sprintf("review.point_%02d", policyIndex)] = workflows.RepositoryGatePolicy{
				Mode: workflows.GatePolicyDisable,
			}
		}
		tooManyPolicies[fmt.Sprintf("owner/repo_%04d", repositoryIndex)] = policies
	}
	largeGates := make([]workflows.GateSpec, workflows.MaxWorkflowGateCount)
	for index := range largeGates {
		largeGates[index] = workflows.GateSpec{
			ID:       fmt.Sprintf("gate_%02d", index),
			Kind:     workflows.GateAIIsolatedContext,
			AgentID:  "main",
			Criteria: strings.Repeat("x", workflows.MaxWorkflowGateCriteriaBytes),
			Title:    "Ask",
		}
	}
	tests := []struct {
		name         string
		global       map[string][]workflows.GateSpec
		repositories map[string]map[string]workflows.RepositoryGatePolicy
	}{
		{name: "invalid global decision", global: map[string][]workflows.GateSpec{"Review.Ready": nil}},
		{name: "null global policy", global: map[string][]workflows.GateSpec{"review.ready": nil}},
		{name: "invalid repository decision", repositories: map[string]map[string]workflows.RepositoryGatePolicy{
			"acme/widgets": {" review.ready": {Mode: workflows.GatePolicyDisable}},
		}},
		{name: "null repository policy map", repositories: map[string]map[string]workflows.RepositoryGatePolicy{
			"acme/widgets": nil,
		}},
		{name: "invalid repository", repositories: map[string]map[string]workflows.RepositoryGatePolicy{
			"acme/widgets/extra": {"review.ready": {Mode: workflows.GatePolicyDisable}},
		}},
		{name: "case collision", repositories: map[string]map[string]workflows.RepositoryGatePolicy{
			"Acme/Widgets": {"review.ready": {Mode: workflows.GatePolicyDisable}},
			"acme/widgets": {"review.ready": {Mode: workflows.GatePolicyDisable}},
		}},
		{name: "duplicate global gate", global: map[string][]workflows.GateSpec{
			"review.ready": {validGate, validGate},
		}},
		{name: "invalid mode", repositories: map[string]map[string]workflows.RepositoryGatePolicy{
			"acme/widgets": {"review.ready": {Mode: "merge"}},
		}},
		{
			name: "effective working agents conflict",
			global: map[string][]workflows.GateSpec{
				"review.ready": {{
					ID: "first", Kind: workflows.GateAIWorkingContext,
					AgentID: "main", Criteria: "decide", Title: "First",
				}},
			},
			repositories: map[string]map[string]workflows.RepositoryGatePolicy{
				"acme/widgets": {"review.ready": {
					Mode: workflows.GatePolicyOverlay,
					Gates: []workflows.GateSpec{{
						ID: "second", Kind: workflows.GateAIWorkingContext,
						AgentID: "other", Criteria: "decide", Title: "Second",
					}},
				}},
			},
		},
		{name: "too many global decisions", global: tooManyGlobal},
		{name: "too many repositories", repositories: tooManyRepositories},
		{name: "too many repository decisions", repositories: map[string]map[string]workflows.RepositoryGatePolicy{
			"acme/widgets": tooManyRepositoryDecisions,
		}},
		{name: "too many policies", repositories: tooManyPolicies},
		{name: "catalog bytes", global: map[string][]workflows.GateSpec{"review.ready": largeGates}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source, err := NewConfigAttentionPolicySource(test.global, test.repositories)
			if err == nil || source != nil {
				t.Fatalf("NewConfigAttentionPolicySource() = (%#v, %v), want failure", source, err)
			}
		})
	}
}

func TestConfigAttentionPolicySourceCallbackContractAndConcurrency(t *testing.T) {
	source, err := NewConfigAttentionPolicySource(
		map[string][]workflows.GateSpec{"review.ready": {{ID: "off", Kind: workflows.GateZero}}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	selector := AttentionPolicySelector{Repository: "acme/widgets", DecisionPoint: "review.ready"}
	if err = source.WithReviewAttentionPolicy(context.Background(), selector, nil); err == nil {
		t.Fatal("nil callback succeeded")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err = source.WithReviewAttentionPolicy(canceled, selector, func(context.Context, AttentionPolicySnapshot) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("canceled callback = (%v, %v)", called, err)
	}
	for _, invalid := range []AttentionPolicySelector{
		{Repository: " acme/widgets", DecisionPoint: "review.ready"},
		{Repository: "acme/widgets", DecisionPoint: "Review.Ready"},
	} {
		err = source.WithReviewAttentionPolicy(
			context.Background(),
			invalid,
			func(context.Context, AttentionPolicySnapshot) error { return nil },
		)
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid selector error = %v", err)
		}
	}

	want := captureConfigAttentionSnapshot(t, source, selector)
	var wait sync.WaitGroup
	errorsByCall := make(chan error, 64)
	for index := 0; index < cap(errorsByCall); index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			err := source.WithReviewAttentionPolicy(
				context.Background(),
				selector,
				func(_ context.Context, snapshot AttentionPolicySnapshot) error {
					if !reflect.DeepEqual(snapshot, want) {
						return fmt.Errorf("snapshot = %#v, want %#v", snapshot, want)
					}
					snapshot.Global[0].ID = "mutated"
					return nil
				},
			)
			errorsByCall <- err
		}()
	}
	wait.Wait()
	close(errorsByCall)
	for callErr := range errorsByCall {
		if callErr != nil {
			t.Fatal(callErr)
		}
	}
	if got := captureConfigAttentionSnapshot(t, source, selector); !reflect.DeepEqual(got, want) {
		t.Fatalf("concurrent callbacks mutated source: %#v", got)
	}
}

func captureConfigAttentionSnapshot(
	t *testing.T,
	source AttentionPolicySource,
	selector AttentionPolicySelector,
) AttentionPolicySnapshot {
	t.Helper()
	var snapshot AttentionPolicySnapshot
	called := 0
	err := source.WithReviewAttentionPolicy(
		context.Background(),
		selector,
		func(_ context.Context, captured AttentionPolicySnapshot) error {
			called++
			snapshot = captured
			return nil
		},
	)
	if err != nil || called != 1 {
		t.Fatalf("WithReviewAttentionPolicy() = (%d calls, %v)", called, err)
	}
	return snapshot
}

func cloneAttentionPolicyTestGlobal(
	source map[string][]workflows.GateSpec,
) map[string][]workflows.GateSpec {
	cloned := make(map[string][]workflows.GateSpec, len(source))
	for decisionPoint, gates := range source {
		resolution, err := workflows.ResolveGatePolicy(gates, nil)
		if err != nil {
			panic(err)
		}
		cloned[decisionPoint] = resolution.Effective
	}
	return cloned
}

func cloneAttentionPolicyTestRepositories(
	source map[string]map[string]workflows.RepositoryGatePolicy,
) map[string]map[string]workflows.RepositoryGatePolicy {
	cloned := make(map[string]map[string]workflows.RepositoryGatePolicy, len(source))
	for repository, policies := range source {
		cloned[repository] = make(map[string]workflows.RepositoryGatePolicy, len(policies))
		for decisionPoint, policy := range policies {
			resolution, err := workflows.ResolveGatePolicy(nil, &policy)
			if err != nil {
				panic(err)
			}
			clonedPolicy := workflows.RepositoryGatePolicy{Mode: policy.Mode}
			if policy.Mode == workflows.GatePolicyOverlay || policy.Mode == workflows.GatePolicyReplace {
				clonedPolicy.Gates = resolution.Effective
			}
			cloned[repository][decisionPoint] = clonedPolicy
		}
	}
	return cloned
}
