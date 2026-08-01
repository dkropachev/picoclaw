package workflows

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestResolveGatePolicyInheritsAndDetaches(t *testing.T) {
	questions := map[string]any{
		"prompt": "Approve?",
		"choices": []any{
			map[string]any{"id": "yes", "label": "Yes"},
		},
	}
	global := []GateSpec{
		policyDeterministicGate("approval", "${{ inputs.gate_subject.ready == true }}", questions),
		{ID: "reserved", Kind: GateZero},
	}

	resolution, err := ResolveGatePolicy(global, nil)
	if err != nil {
		t.Fatalf("ResolveGatePolicy() error = %v", err)
	}
	if resolution.Mode != GatePolicyInherit {
		t.Fatalf("Mode = %q, want %q", resolution.Mode, GatePolicyInherit)
	}
	gotIDs := gatePolicyIDs(resolution.Effective)
	wantIDs := []string{"approval", "reserved"}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("effective IDs = %#v, want %#v", gotIDs, wantIDs)
	}
	if got, want := resolution.Entries, []GatePolicyResolutionEntry{
		{
			ID: "approval", Action: GatePolicyResolutionInherited,
			GlobalPosition: 1, EffectivePosition: 1,
		},
		{
			ID: "reserved", Action: GatePolicyResolutionInherited,
			GlobalPosition: 2, EffectivePosition: 2,
		},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Entries = %#v, want %#v", got, want)
	}

	global[0].Title = "mutated source"
	questions["prompt"] = "mutated source"
	questions["choices"].([]any)[0].(map[string]any)["label"] = "mutated source"
	resolvedQuestions := resolution.Effective[0].Questions.(map[string]any)
	if resolution.Effective[0].Title != "approval attention" ||
		resolvedQuestions["prompt"] != "Approve?" ||
		resolvedQuestions["choices"].([]any)[0].(map[string]any)["label"] != "Yes" {
		t.Fatalf("resolution aliases source values: %#v", resolution.Effective[0])
	}

	secondSource := []GateSpec{
		policyDeterministicGate(
			"approval",
			"${{ inputs.gate_subject.ready == true }}",
			map[string]any{"prompt": "Approve?"},
		),
	}
	first, err := ResolveGatePolicy(secondSource, nil)
	if err != nil {
		t.Fatalf("first ResolveGatePolicy() error = %v", err)
	}
	first.Effective[0].Questions.(map[string]any)["prompt"] = "mutated output"
	second, err := ResolveGatePolicy(secondSource, nil)
	if err != nil {
		t.Fatalf("second ResolveGatePolicy() error = %v", err)
	}
	if got := second.Effective[0].Questions.(map[string]any)["prompt"]; got != "Approve?" {
		t.Fatalf("later resolution observed prior output mutation: %#v", got)
	}
}

func TestResolveGatePolicyOverlayUsesStableSlotsAndZeroTombstones(t *testing.T) {
	global := []GateSpec{
		policyDeterministicGate(
			"security",
			"${{ inputs.gate_subject.risk == 'critical' }}",
			[]any{"Global security question"},
		),
		policyWorkingGate("conversation", "main"),
		{ID: "opt_in", Kind: GateZero},
	}
	repository := &RepositoryGatePolicy{
		Mode: GatePolicyOverlay,
		Gates: []GateSpec{
			{ID: "conversation", Kind: GateZero},
			policyDeterministicGate(
				"security",
				"${{ inputs.gate_subject.risk == 'high' }}",
				[]any{"Repository security question"},
			),
			policyIsolatedGate("opt_in", "reviewer"),
			policyIsolatedGate("style", "reviewer"),
		},
	}

	resolution, err := ResolveGatePolicy(global, repository)
	if err != nil {
		t.Fatalf("ResolveGatePolicy() error = %v", err)
	}
	if got, want := gatePolicyIDs(resolution.Effective),
		[]string{"security", "conversation", "opt_in", "style"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("effective IDs = %#v, want %#v", got, want)
	}
	if resolution.Effective[0].When != "${{ inputs.gate_subject.risk == 'high' }}" ||
		resolution.Effective[1].Kind != GateZero ||
		resolution.Effective[2].Kind != GateAIIsolatedContext {
		t.Fatalf("effective stable-slot replacements = %#v", resolution.Effective)
	}
	if got, want := resolution.Entries, []GatePolicyResolutionEntry{
		{
			ID: "security", Action: GatePolicyResolutionReplaced,
			GlobalPosition: 1, RepositoryPosition: 2, EffectivePosition: 1,
		},
		{
			ID: "conversation", Action: GatePolicyResolutionTombstoned,
			GlobalPosition: 2, RepositoryPosition: 1, EffectivePosition: 2,
		},
		{
			ID: "opt_in", Action: GatePolicyResolutionReplaced,
			GlobalPosition: 3, RepositoryPosition: 3, EffectivePosition: 3,
		},
		{
			ID: "style", Action: GatePolicyResolutionAppended,
			RepositoryPosition: 4, EffectivePosition: 4,
		},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Entries = %#v, want %#v", got, want)
	}

	compilation, err := CompileGateWorkflow(
		"Resolved repository gates",
		resolution.Effective,
		map[string]any{"risk": "high"},
	)
	if err != nil {
		t.Fatalf("CompileGateWorkflow(resolved) error = %v", err)
	}
	if compilation.Noop || compilation.RequiresSession {
		t.Fatalf("compiled resolution = %#v, want executable without inherited session", compilation)
	}
	if got, want := compilation.GateIDs,
		[]string{"security", "conversation", "opt_in", "style"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("compiled GateIDs = %#v, want %#v", got, want)
	}
	if got, want := gateStepIDs(compilation.Workflow.Jobs[workflowGateJobID].Steps), []string{
		"gate_security_attention",
		"gate_opt_in_decision", "gate_opt_in_attention",
		"gate_style_decision", "gate_style_attention",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("compiled steps = %#v, want %#v", got, want)
	}
}

func TestResolveGatePolicySoleTombstoneCompilesToNoop(t *testing.T) {
	global := []GateSpec{policyDeterministicGate(
		"approval",
		"${{ inputs.gate_subject.needs_approval == true }}",
		[]any{"Approve?"},
	)}
	resolution, err := ResolveGatePolicy(global, &RepositoryGatePolicy{
		Mode:  GatePolicyOverlay,
		Gates: []GateSpec{{ID: "approval", Kind: GateZero}},
	})
	if err != nil {
		t.Fatalf("ResolveGatePolicy() error = %v", err)
	}
	if got, want := resolution.Entries, []GatePolicyResolutionEntry{{
		ID: "approval", Action: GatePolicyResolutionTombstoned,
		GlobalPosition: 1, RepositoryPosition: 1, EffectivePosition: 1,
	}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tombstone Entries = %#v, want %#v", got, want)
	}
	if len(resolution.Effective) != 1 || resolution.Effective[0].Kind != GateZero {
		t.Fatalf("tombstone Effective = %#v", resolution.Effective)
	}

	compilation, err := CompileGateWorkflow(
		"Tombstoned policy",
		resolution.Effective,
		make(chan int),
	)
	if err != nil {
		t.Fatalf("CompileGateWorkflow(tombstone, unused invalid subject) error = %v", err)
	}
	if !compilation.Noop || compilation.Workflow != nil ||
		!reflect.DeepEqual(compilation.GateIDs, []string{"approval"}) {
		t.Fatalf("tombstone compilation = %#v, want no-op retaining gate ID", compilation)
	}
}

func TestResolveGatePolicyModes(t *testing.T) {
	global := []GateSpec{policyWorkingGate("global", "main")}

	t.Run("explicit inherit", func(t *testing.T) {
		resolution, err := ResolveGatePolicy(global, &RepositoryGatePolicy{Mode: GatePolicyInherit})
		if err != nil {
			t.Fatalf("ResolveGatePolicy() error = %v", err)
		}
		if resolution.Mode != GatePolicyInherit ||
			!reflect.DeepEqual(gatePolicyIDs(resolution.Effective), []string{"global"}) {
			t.Fatalf("inherit resolution = %#v", resolution)
		}
	})

	t.Run("replace", func(t *testing.T) {
		repository := &RepositoryGatePolicy{
			Mode: GatePolicyReplace,
			Gates: []GateSpec{
				policyIsolatedGate("repository", "reviewer"),
				{ID: "off", Kind: GateZero},
			},
		}
		resolution, err := ResolveGatePolicy(global, repository)
		if err != nil {
			t.Fatalf("ResolveGatePolicy() error = %v", err)
		}
		gotIDs := gatePolicyIDs(resolution.Effective)
		wantIDs := []string{"repository", "off"}
		if !reflect.DeepEqual(gotIDs, wantIDs) {
			t.Fatalf("replace IDs = %#v, want %#v", gotIDs, wantIDs)
		}
		for index, entry := range resolution.Entries {
			if entry.Action != GatePolicyResolutionSelected ||
				entry.RepositoryPosition != index+1 || entry.EffectivePosition != index+1 ||
				entry.GlobalPosition != 0 {
				t.Fatalf("replace entry %d = %#v", index, entry)
			}
		}
	})

	t.Run("disable", func(t *testing.T) {
		resolution, err := ResolveGatePolicy(global, &RepositoryGatePolicy{Mode: GatePolicyDisable})
		if err != nil {
			t.Fatalf("ResolveGatePolicy() error = %v", err)
		}
		if resolution.Mode != GatePolicyDisable || resolution.Effective == nil ||
			len(resolution.Effective) != 0 || resolution.Entries == nil || len(resolution.Entries) != 0 {
			t.Fatalf("disable resolution = %#v, want explicit empty slices", resolution)
		}
		compilation, compileErr := CompileGateWorkflow(
			"Disabled policy",
			resolution.Effective,
			make(chan int),
		)
		if compileErr != nil {
			t.Fatalf("CompileGateWorkflow(disabled, unused invalid subject) error = %v", compileErr)
		}
		if !compilation.Noop || compilation.Workflow != nil {
			t.Fatalf("disabled compilation = %#v, want explicit no-op", compilation)
		}
	})
}

func TestResolveGatePolicyDetachesRepositoryLayers(t *testing.T) {
	for _, mode := range []GatePolicyMode{GatePolicyOverlay, GatePolicyReplace} {
		t.Run(string(mode), func(t *testing.T) {
			questions := map[string]any{
				"prompt":  "Repository question",
				"choices": []any{map[string]any{"id": "yes", "label": "Yes"}},
			}
			repository := &RepositoryGatePolicy{
				Mode: mode,
				Gates: []GateSpec{policyDeterministicGate(
					"repository",
					"${{ inputs.gate_subject.ready == true }}",
					questions,
				)},
			}
			global := []GateSpec{{ID: "repository", Kind: GateZero}}

			first, err := ResolveGatePolicy(global, repository)
			if err != nil {
				t.Fatalf("first ResolveGatePolicy() error = %v", err)
			}
			firstQuestions := first.Effective[0].Questions.(map[string]any)
			firstQuestions["prompt"] = "mutated output"
			firstQuestions["choices"].([]any)[0].(map[string]any)["label"] = "mutated output"

			second, err := ResolveGatePolicy(global, repository)
			if err != nil {
				t.Fatalf("second ResolveGatePolicy() error = %v", err)
			}
			secondQuestions := second.Effective[0].Questions.(map[string]any)
			if secondQuestions["prompt"] != "Repository question" ||
				secondQuestions["choices"].([]any)[0].(map[string]any)["label"] != "Yes" {
				t.Fatalf("later resolution observed output mutation: %#v", secondQuestions)
			}

			questions["prompt"] = "mutated source"
			questions["choices"].([]any)[0].(map[string]any)["label"] = "mutated source"
			if secondQuestions["prompt"] != "Repository question" ||
				secondQuestions["choices"].([]any)[0].(map[string]any)["label"] != "Yes" {
				t.Fatalf("resolution aliases repository source: %#v", secondQuestions)
			}
		})
	}
}

func TestGatePolicyJSONContract(t *testing.T) {
	policyFixtures := []struct {
		name   string
		policy RepositoryGatePolicy
		want   string
	}{
		{name: "inherit", policy: RepositoryGatePolicy{Mode: GatePolicyInherit}, want: `{"mode":"inherit"}`},
		{
			name: "overlay", policy: RepositoryGatePolicy{
				Mode: GatePolicyOverlay, Gates: []GateSpec{{ID: "off", Kind: GateZero}},
			},
			want: `{"mode":"overlay","gates":[{"id":"off","kind":"zero"}]}`,
		},
		{
			name: "replace", policy: RepositoryGatePolicy{
				Mode: GatePolicyReplace, Gates: []GateSpec{{ID: "off", Kind: GateZero}},
			},
			want: `{"mode":"replace","gates":[{"id":"off","kind":"zero"}]}`,
		},
		{name: "disable", policy: RepositoryGatePolicy{Mode: GatePolicyDisable}, want: `{"mode":"disable"}`},
	}
	for _, fixture := range policyFixtures {
		t.Run("policy "+fixture.name, func(t *testing.T) {
			encoded, err := json.Marshal(fixture.policy)
			if err != nil {
				t.Fatalf("json.Marshal(policy) error = %v", err)
			}
			if string(encoded) != fixture.want {
				t.Fatalf("policy JSON = %s, want %s", encoded, fixture.want)
			}
		})
	}

	resolution := GatePolicyResolution{
		Mode: GatePolicyOverlay,
		Effective: []GateSpec{policyDeterministicGate(
			"repository",
			"false",
			map[string]any{"nested": []any{"yes"}},
		)},
		Entries: []GatePolicyResolutionEntry{{
			ID: "repository", Action: GatePolicyResolutionAppended,
			RepositoryPosition: 1, EffectivePosition: 1,
		}},
	}
	resolutionJSON, err := json.Marshal(resolution)
	if err != nil {
		t.Fatalf("json.Marshal(resolution) error = %v", err)
	}
	wantResolutionJSON := `{"mode":"overlay","effective":[{"id":"repository","kind":"deterministic","when":"false","title":"repository attention","questions":{"nested":["yes"]}}],"entries":[{"id":"repository","action":"appended","repository_position":1,"effective_position":1}]}`
	if string(resolutionJSON) != wantResolutionJSON {
		t.Fatalf("resolution JSON = %s, want %s", resolutionJSON, wantResolutionJSON)
	}
	var decoded GatePolicyResolution
	if decodeErr := json.Unmarshal(resolutionJSON, &decoded); decodeErr != nil {
		t.Fatalf("json.Unmarshal(resolution) error = %v", decodeErr)
	}
	if decoded.Mode != GatePolicyOverlay || decoded.Entries[0].Action != GatePolicyResolutionAppended ||
		decoded.Effective[0].Questions.(map[string]any)["nested"].([]any)[0] != "yes" {
		t.Fatalf("decoded resolution = %#v", decoded)
	}

	actionFixtures := []struct {
		name  string
		entry GatePolicyResolutionEntry
		want  string
	}{
		{
			name: "inherited",
			entry: GatePolicyResolutionEntry{
				ID: "gate", Action: GatePolicyResolutionInherited,
				GlobalPosition: 1, EffectivePosition: 1,
			},
			want: `{"id":"gate","action":"inherited","global_position":1,"effective_position":1}`,
		},
		{
			name: "replaced",
			entry: GatePolicyResolutionEntry{
				ID: "gate", Action: GatePolicyResolutionReplaced,
				GlobalPosition: 1, RepositoryPosition: 2, EffectivePosition: 1,
			},
			want: `{"id":"gate","action":"replaced","global_position":1,"repository_position":2,"effective_position":1}`,
		},
		{
			name: "tombstoned",
			entry: GatePolicyResolutionEntry{
				ID: "gate", Action: GatePolicyResolutionTombstoned,
				GlobalPosition: 1, RepositoryPosition: 2, EffectivePosition: 1,
			},
			want: `{"id":"gate","action":"tombstoned","global_position":1,"repository_position":2,"effective_position":1}`,
		},
		{
			name: "appended",
			entry: GatePolicyResolutionEntry{
				ID: "gate", Action: GatePolicyResolutionAppended,
				RepositoryPosition: 2, EffectivePosition: 3,
			},
			want: `{"id":"gate","action":"appended","repository_position":2,"effective_position":3}`,
		},
		{
			name: "selected",
			entry: GatePolicyResolutionEntry{
				ID: "gate", Action: GatePolicyResolutionSelected,
				RepositoryPosition: 2, EffectivePosition: 2,
			},
			want: `{"id":"gate","action":"selected","repository_position":2,"effective_position":2}`,
		},
	}
	for _, fixture := range actionFixtures {
		t.Run("action "+fixture.name, func(t *testing.T) {
			encoded, encodeErr := json.Marshal(fixture.entry)
			if encodeErr != nil {
				t.Fatalf("json.Marshal(entry) error = %v", encodeErr)
			}
			if string(encoded) != fixture.want {
				t.Fatalf("entry JSON = %s, want %s", encoded, fixture.want)
			}
		})
	}

	disabled, err := ResolveGatePolicy(nil, &RepositoryGatePolicy{Mode: GatePolicyDisable})
	if err != nil {
		t.Fatalf("ResolveGatePolicy(disable) error = %v", err)
	}
	disabledJSON, err := json.Marshal(disabled)
	if err != nil {
		t.Fatalf("json.Marshal(disabled resolution) error = %v", err)
	}
	if got, want := string(disabledJSON), `{"mode":"disable","effective":[],"entries":[]}`; got != want {
		t.Fatalf("disabled resolution JSON = %s, want %s", got, want)
	}
}

func TestResolveGatePolicyValidatesEveryLayerAndEffectiveComposition(t *testing.T) {
	tooMany := make([]GateSpec, MaxWorkflowGateCount+1)
	for index := range tooMany {
		tooMany[index] = GateSpec{ID: fmt.Sprintf("gate_%d", index), Kind: GateZero}
	}
	full := make([]GateSpec, MaxWorkflowGateCount)
	for index := range full {
		full[index] = GateSpec{ID: fmt.Sprintf("gate_%d", index), Kind: GateZero}
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	var typedNilQuestions []any

	tests := []struct {
		name       string
		global     []GateSpec
		repository *RepositoryGatePolicy
		want       string
	}{
		{name: "global count", global: tooMany, want: "global exceeds 64 gates"},
		{
			name: "repository count", global: nil,
			repository: &RepositoryGatePolicy{Mode: GatePolicyOverlay, Gates: tooMany},
			want:       "repository.gates exceeds 64 gates",
		},
		{
			name: "effective count", global: full,
			repository: &RepositoryGatePolicy{
				Mode:  GatePolicyOverlay,
				Gates: []GateSpec{{ID: "extra", Kind: GateZero}},
			},
			want: "effective gate policy exceeds 64 gates",
		},
		{
			name: "duplicate global",
			global: []GateSpec{
				{ID: "same", Kind: GateZero},
				{ID: "same", Kind: GateZero},
			},
			want: "global gate \"same\" is duplicated",
		},
		{
			name: "duplicate repository", global: nil,
			repository: &RepositoryGatePolicy{Mode: GatePolicyOverlay, Gates: []GateSpec{
				{ID: "same", Kind: GateZero},
				{ID: "same", Kind: GateZero},
			}},
			want: "repository.gates gate \"same\" is duplicated",
		},
		{
			name:       "invalid mode",
			repository: &RepositoryGatePolicy{Mode: "merge"},
			want:       "repository gate policy mode \"merge\" is unsupported",
		},
		{
			name: "inherit gates",
			repository: &RepositoryGatePolicy{
				Mode: GatePolicyInherit, Gates: []GateSpec{{ID: "off", Kind: GateZero}},
			},
			want: "repository inherit policy cannot configure gates",
		},
		{
			name: "disable gates",
			repository: &RepositoryGatePolicy{
				Mode: GatePolicyDisable, Gates: []GateSpec{{ID: "off", Kind: GateZero}},
			},
			want: "repository disable policy cannot configure gates",
		},
		{
			name:       "empty overlay",
			repository: &RepositoryGatePolicy{Mode: GatePolicyOverlay},
			want:       "repository overlay policy requires at least one gate",
		},
		{
			name:       "empty replace",
			repository: &RepositoryGatePolicy{Mode: GatePolicyReplace},
			want:       "repository replace policy requires at least one gate",
		},
		{
			name:   "invalid global under replace",
			global: []GateSpec{{ID: "bad", Kind: "surprise"}},
			repository: &RepositoryGatePolicy{
				Mode: GatePolicyReplace, Gates: []GateSpec{{ID: "off", Kind: GateZero}},
			},
			want: "global[0].kind \"surprise\" is unsupported",
		},
		{
			name:   "invalid global under disable",
			global: []GateSpec{{ID: "bad", Kind: GateAIWorkingContext}},
			repository: &RepositoryGatePolicy{
				Mode: GatePolicyDisable,
			},
			want: "global[0].agent_id must be an exact canonical agent ID",
		},
		{
			name: "invalid repository path",
			repository: &RepositoryGatePolicy{
				Mode:  GatePolicyOverlay,
				Gates: []GateSpec{{ID: "bad", Kind: GateAIIsolatedContext}},
			},
			want: "repository.gates[0].agent_id must be an exact canonical agent ID",
		},
		{
			name: "global working agent mismatch",
			global: []GateSpec{
				policyWorkingGate("first", "main"),
				policyWorkingGate("second", "reviewer"),
			},
			want: "global working-context gates must use one session-owning agent",
		},
		{
			name:   "effective working agent mismatch",
			global: []GateSpec{policyWorkingGate("global", "main")},
			repository: &RepositoryGatePolicy{
				Mode:  GatePolicyOverlay,
				Gates: []GateSpec{policyWorkingGate("repository", "reviewer")},
			},
			want: "effective working-context gates must use one session-owning agent",
		},
		{
			name: "typed nil deterministic questions",
			global: []GateSpec{policyDeterministicGate(
				"typed_nil", "false", typedNilQuestions,
			)},
			want: "global[0].questions are required",
		},
		{
			name: "cyclic questions",
			global: []GateSpec{{
				ID: "cycle", Kind: GateAIIsolatedContext, AgentID: "reviewer",
				Criteria: "Review supplied context.", Title: "Cycle", Questions: cycle,
			}},
			want: "global[0].questions must be acyclic JSON",
		},
		{
			name: "nonfinite questions",
			global: []GateSpec{{
				ID: "number", Kind: GateAIIsolatedContext, AgentID: "reviewer",
				Criteria: "Review supplied context.", Title: "Number",
				Questions: []any{math.Inf(1)},
			}},
			want: "global[0].questions contains a non-finite number",
		},
		{
			name: "custom marshaler questions",
			global: []GateSpec{{
				ID: "custom", Kind: GateAIIsolatedContext, AgentID: "reviewer",
				Criteria: "Review supplied context.", Title: "Custom",
				Questions: panickingGateJSONMarshaler("do not call"),
			}},
			want: "global[0].questions contains custom marshaler type",
		},
		{
			name: "oversized questions",
			global: []GateSpec{{
				ID: "large", Kind: GateAIIsolatedContext, AgentID: "reviewer",
				Criteria: "Review supplied context.", Title: "Large",
				Questions: strings.Repeat("x", MaxWorkflowGateQuestionBytes),
			}},
			want: "global[0].questions exceeds 131072 encoded JSON bytes",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ResolveGatePolicy(test.global, test.repository)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ResolveGatePolicy() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestResolveGatePolicyAcceptsExactLayerBounds(t *testing.T) {
	global := make([]GateSpec, MaxWorkflowGateCount)
	repository := make([]GateSpec, MaxWorkflowGateCount)
	for index := range global {
		id := fmt.Sprintf("gate_%d", index)
		global[index] = GateSpec{ID: id, Kind: GateZero}
		repository[index] = GateSpec{ID: id, Kind: GateZero}
	}

	resolution, err := ResolveGatePolicy(global, &RepositoryGatePolicy{
		Mode:  GatePolicyOverlay,
		Gates: repository,
	})
	if err != nil {
		t.Fatalf("ResolveGatePolicy() exact bounds error = %v", err)
	}
	if len(resolution.Effective) != MaxWorkflowGateCount ||
		len(resolution.Entries) != MaxWorkflowGateCount {
		t.Fatalf("exact-bound resolution sizes = %d/%d", len(resolution.Effective), len(resolution.Entries))
	}
	for index, entry := range resolution.Entries {
		if entry.Action != GatePolicyResolutionReplaced ||
			entry.GlobalPosition != index+1 || entry.RepositoryPosition != index+1 {
			t.Fatalf("exact-bound entry %d = %#v", index, entry)
		}
	}
}

func TestResolveGatePolicyIsSafeForConcurrentReuse(t *testing.T) {
	global := []GateSpec{
		policyDeterministicGate(
			"policy",
			"${{ inputs.gate_subject.ready == true }}",
			map[string]any{"prompt": "Approve?"},
		),
		policyWorkingGate("discussion", "main"),
	}
	repository := &RepositoryGatePolicy{
		Mode: GatePolicyOverlay,
		Gates: []GateSpec{
			{ID: "discussion", Kind: GateZero},
			policyIsolatedGate("review", "reviewer"),
		},
	}

	const workers = 32
	errors := make(chan error, workers)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			resolution, err := ResolveGatePolicy(global, repository)
			if err != nil {
				errors <- err
				return
			}
			if got, want := gatePolicyIDs(resolution.Effective),
				[]string{"policy", "discussion", "review"}; !reflect.DeepEqual(got, want) {
				errors <- fmt.Errorf("effective IDs = %#v, want %#v", got, want)
				return
			}
			if resolution.Effective[0].Questions.(map[string]any)["prompt"] != "Approve?" {
				errors <- fmt.Errorf("detached questions = %#v", resolution.Effective[0].Questions)
			}
		}()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func policyDeterministicGate(id, when string, questions any) GateSpec {
	return GateSpec{
		ID:        id,
		Kind:      GateDeterministic,
		When:      when,
		Title:     id + " attention",
		Questions: questions,
	}
}

func policyWorkingGate(id, agentID string) GateSpec {
	return GateSpec{
		ID:       id,
		Kind:     GateAIWorkingContext,
		AgentID:  agentID,
		Criteria: "Ask only when the active discussion cannot resolve the issue.",
		Title:    id + " attention",
	}
}

func policyIsolatedGate(id, agentID string) GateSpec {
	return GateSpec{
		ID:       id,
		Kind:     GateAIIsolatedContext,
		AgentID:  agentID,
		Criteria: "Ask only when the supplied context cannot resolve the issue.",
		Title:    id + " attention",
	}
}

func gatePolicyIDs(specs []GateSpec) []string {
	ids := make([]string, len(specs))
	for index, spec := range specs {
		ids[index] = spec.ID
	}
	return ids
}
