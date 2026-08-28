package workflows

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestRepositoryBugFinderProfileHashBindsResolvedModelGraph(t *testing.T) {
	base := NewRepositoryBugFinderProfileHashInput(
		"account", "all", "focus", `{}`, "scope", "requested",
		"graph-a", []string{"effective-a"}, false, 1024,
	)
	want, err := RepositoryBugFinderProfileHash(base)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RepositoryBugFinderProfileHashInput){
		"revision": func(value *RepositoryBugFinderProfileHashInput) {
			value.ModelGraphRevision = "graph-b"
		},
		"effective models": func(value *RepositoryBugFinderProfileHashInput) {
			value.EffectiveModels = []string{"effective-b"}
		},
		"default mode": func(value *RepositoryBugFinderProfileHashInput) {
			value.IncludeDefaultReviewer = true
		},
		"requested aliases": func(value *RepositoryBugFinderProfileHashInput) {
			value.Models = "other"
		},
		"account": func(value *RepositoryBugFinderProfileHashInput) { value.AccountRef = "other" },
		"content bound": func(value *RepositoryBugFinderProfileHashInput) {
			value.MaxContentBytes++
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			candidate.EffectiveModels = append([]string(nil), base.EffectiveModels...)
			mutate(&candidate)
			got, hashErr := RepositoryBugFinderProfileHash(candidate)
			if hashErr != nil || got == want {
				t.Fatalf("drifted hash=%q baseline=%q err=%v", got, want, hashErr)
			}
		})
	}
	invalid := base
	invalid.ModelGraphRevision = ""
	if _, err := RepositoryBugFinderProfileHash(invalid); err == nil {
		t.Fatal("empty model graph revision was accepted")
	}
}

func TestRepositoryBugFinderProfileHashMatchesResolvedMapAndLegacyVector(t *testing.T) {
	input := NewRepositoryBugFinderProfileHashInput(
		"account", "all", "focus", `{}`, "scope", " requested-a,requested-b ",
		"graph", []string{" effective-a ", "effective-b"}, false, 2048,
	)
	got, err := RepositoryBugFinderProfileHash(input)
	if err != nil {
		t.Fatal(err)
	}
	canonical := map[string]any{
		"schema": RepositoryBugFinderProfileSchema, "prompt_revision": RepositoryBugFinderPromptRevision,
		"account_ref": "account", "target": "all", "focus": "focus", "scope_policy": `{}`,
		"scope_plan_hash": "scope", "models": "requested-a,requested-b",
		"model_graph_revision": "graph", "effective_models": "effective-a,effective-b",
		"include_default_reviewer": false, "max_content_bytes": int64(2048),
	}
	native, err := nativeStableHash(canonical)
	if err != nil || got != "sha256:"+native {
		t.Fatalf("canonical hash=%q native=%q err=%v", got, native, err)
	}
	legacy, err := RepositoryBugFinderLegacyResolvedProfileHash(input)
	if err != nil {
		t.Fatal(err)
	}
	legacyMap := map[string]any{
		"schema": RepositoryBugFinderProfileSchema, "prompt_revision": RepositoryBugFinderPromptRevision,
		"account_ref": "account", "target": "all", "focus": "focus", "scope_policy": `{}`,
		"scope_plan_hash": "scope", "models": []string{"requested-a", "requested-b"},
		"model_graph_revision": "graph", "effective_models": []string{"effective-a", "effective-b"},
		"include_default_reviewer": false, "max_content_bytes": int64(2048),
	}
	legacyNative, err := nativeStableHash(legacyMap)
	if err != nil || legacy != "sha256:"+legacyNative {
		t.Fatalf("legacy hash=%q native=%q err=%v", legacy, legacyNative, err)
	}
}

func TestRepositoryBugFinderProfileHashMatchesCanonicalNativeMap(t *testing.T) {
	input := NewRepositoryBugFinderProfileHashInput(
		" account ", " all ", "Find bugs.", `{"code_types":["code"]}`,
		strings.Repeat("a", 64), " review-a,review-b ", " sha256:graph ",
		[]string{" provider/model-a ", "provider/model-b"}, false, 524288,
	)
	got, err := RepositoryBugFinderProfileHash(input)
	if err != nil {
		t.Fatal(err)
	}
	canonical := map[string]any{
		"schema":          RepositoryBugFinderProfileSchema,
		"prompt_revision": RepositoryBugFinderPromptRevision,
		"account_ref":     "account", "target": "all", "focus": "Find bugs.",
		"scope_policy":             `{"code_types":["code"]}`,
		"scope_plan_hash":          strings.Repeat("a", 64),
		"models":                   "review-a,review-b",
		"model_graph_revision":     "sha256:graph",
		"effective_models":         "provider/model-a,provider/model-b",
		"include_default_reviewer": false,
		"max_content_bytes":        524288,
	}
	digest, err := nativeStableHash(canonical)
	if err != nil || got != "sha256:"+digest {
		t.Fatalf("profile hash=%q native=%q err=%v", got, digest, err)
	}
	fromNative, err := nativeRepositoryBugFinderProfileHash(canonical)
	if err != nil || fromNative != got {
		t.Fatalf("native profile hash=%q want=%q err=%v", fromNative, got, err)
	}
	canonical["models"] = []any{"review-a", "review-b"}
	canonical["effective_models"] = []any{"provider/model-a", "provider/model-b"}
	fromResolvedNative, err := nativeRepositoryBugFinderProfileHash(canonical)
	if err != nil || fromResolvedNative != got {
		t.Fatalf("resolved native profile hash=%q want=%q err=%v", fromResolvedNative, got, err)
	}
	if input.AccountRef != "account" || input.Target != "all" ||
		input.Models != "review-a,review-b" || input.Focus != "Find bugs." ||
		input.ModelGraphRevision != "sha256:graph" ||
		!slices.Equal(input.EffectiveModels, []string{"provider/model-a", "provider/model-b"}) ||
		input.MaxContentBytes != 524288 {
		t.Fatalf("constructor canonicalization=%#v", input)
	}
}

func TestRepositoryBugFinderProfileHashDefaultChainAndCanonicalValidation(t *testing.T) {
	defaultChain := NewRepositoryBugFinderProfileHashInput(
		"", "all", "focus", `{}`, "scope", "", "graph", nil, true, 1024,
	)
	if _, err := RepositoryBugFinderProfileHash(defaultChain); err != nil {
		t.Fatalf("default-chain profile rejected: %v", err)
	}
	invalid := defaultChain
	invalid.IncludeDefaultReviewer = false
	if _, err := RepositoryBugFinderProfileHash(invalid); err == nil {
		t.Fatal("empty nondefault reviewer profile was accepted")
	}
	for name, mutate := range map[string]func(*RepositoryBugFinderProfileHashInput){
		"revision whitespace": func(value *RepositoryBugFinderProfileHashInput) {
			value.ModelGraphRevision = " graph "
		},
		"scope whitespace": func(value *RepositoryBugFinderProfileHashInput) {
			value.ScopePlanHash = " scope "
		},
		"duplicate effective": func(value *RepositoryBugFinderProfileHashInput) {
			value.EffectiveModels = []string{"same", "same"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := NewRepositoryBugFinderProfileHashInput(
				"account", "all", "focus", `{}`, "scope", "requested", "graph",
				[]string{"effective"}, false, 1024,
			)
			mutate(&candidate)
			if _, err := RepositoryBugFinderProfileHash(candidate); err == nil {
				t.Fatal("noncanonical profile was accepted")
			}
		})
	}
}

func TestRepositoryBugFinderProfileHashRejectsSchemaAndFieldDrift(t *testing.T) {
	valid := map[string]any{
		"schema": RepositoryBugFinderProfileSchema, "prompt_revision": RepositoryBugFinderPromptRevision,
		"account_ref": "account", "target": "all", "focus": "Find bugs.",
		"scope_policy": `{}`, "scope_plan_hash": strings.Repeat("a", 64),
		"models": "review-a", "model_graph_revision": "sha256:graph",
		"effective_models": []any{"provider/model-a"}, "include_default_reviewer": false,
		"max_content_bytes": 524288,
	}
	for name, mutate := range map[string]func(map[string]any){
		"schema":          func(value map[string]any) { value["schema"] = "other" },
		"prompt revision": func(value map[string]any) { value["prompt_revision"] = "other" },
		"field type":      func(value map[string]any) { value["max_content_bytes"] = "524288" },
		"models type":     func(value map[string]any) { value["models"] = 7 },
		"effective type":  func(value map[string]any) { value["effective_models"] = []any{7} },
		"unknown field":   func(value map[string]any) { value["extra"] = true },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := make(map[string]any, len(valid)+1)
			for key, item := range valid {
				candidate[key] = item
			}
			mutate(candidate)
			if _, err := nativeRepositoryBugFinderProfileHash(candidate); err == nil {
				t.Fatal("drifted profile hash input was accepted")
			}
		})
	}
}

func TestRepositoryBugFinderProfileHashChangesWithResolvedModelGraphIdentity(t *testing.T) {
	base := NewRepositoryBugFinderProfileHashInput(
		"account", "all", "Find bugs.", `{}`, strings.Repeat("a", 64), "review-alias",
		"sha256:graph-a", []string{"provider/model-a", "provider/model-b"}, false, 524288,
	)
	want, err := RepositoryBugFinderProfileHash(base)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RepositoryBugFinderProfileHashInput){
		"requested aliases": func(input *RepositoryBugFinderProfileHashInput) {
			input.Models = "another-alias"
		},
		"model graph revision": func(input *RepositoryBugFinderProfileHashInput) {
			input.ModelGraphRevision = "sha256:graph-b"
		},
		"effective reviewer cohort": func(input *RepositoryBugFinderProfileHashInput) {
			input.EffectiveModels = []string{"provider/model-a", "provider/model-c"}
		},
		"default reviewer classification": func(input *RepositoryBugFinderProfileHashInput) {
			input.IncludeDefaultReviewer = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			got, hashErr := RepositoryBugFinderProfileHash(candidate)
			if hashErr != nil || got == want {
				t.Fatalf("drifted hash=%q want different from %q err=%v", got, want, hashErr)
			}
		})
	}
}

func TestRepositoryBugFinderRequiredAssignmentsMatchesReviewerRouting(t *testing.T) {
	if got, err := RepositoryBugFinderRequiredAssignments(
		[]string{"review-a", "review-b"}, false,
	); err != nil || got != 8 {
		t.Fatalf("explicit reviewer assignments=%d err=%v", got, err)
	}
	if got, err := RepositoryBugFinderRequiredAssignments(
		[]string{"fallback-a", "fallback-b"}, true,
	); err != nil || got != 4 {
		t.Fatalf("default-chain assignments=%d err=%v", got, err)
	}
	if _, err := RepositoryBugFinderRequiredAssignments(nil, false); err == nil {
		t.Fatal("empty required reviewer set was accepted")
	}
}

func TestRepositoryBugFinderEffectiveMaxContentBytesMatchesResolverClamp(t *testing.T) {
	if got, err := RepositoryBugFinderEffectiveMaxContentBytes(64<<10, 32<<10); err != nil || got != 32<<10 {
		t.Fatalf("clamped max content=%d err=%v", got, err)
	}
	if got, err := RepositoryBugFinderEffectiveMaxContentBytes(16<<10, 32<<10); err != nil || got != 16<<10 {
		t.Fatalf("preserved max content=%d err=%v", got, err)
	}
	if _, err := RepositoryBugFinderEffectiveMaxContentBytes(1, 0); err == nil {
		t.Fatal("invalid resolved maximum was accepted")
	}
}

func TestRepositoryBugFinderProfileHelpersRejectMalformedBoundaryValues(t *testing.T) {
	invalid := NewRepositoryBugFinderProfileHashInput(
		"account", "all", "focus", `{}`, strings.Repeat("a", 64), "review-a",
		"sha256:graph", []string{"provider/model-a"}, false, 0,
	)
	if _, err := RepositoryBugFinderProfileHash(invalid); err == nil {
		t.Fatal("zero content profile hash was accepted")
	}
	if _, err := RepositoryBugFinderRequiredAssignments(make([]string, 9), false); err == nil {
		t.Fatal("oversized reviewer denominator was accepted")
	}
	if got, err := RepositoryBugFinderEffectiveMaxContentBytes(0, 1024); err != nil || got != 1024 {
		t.Fatalf("defaulted max content=%d err=%v", got, err)
	}
	if _, err := nativeRepositoryBugFinderProfileHash(func() {}); err == nil {
		t.Fatal("unmarshalable native profile was accepted")
	}
	if _, err := nativeRepositoryBugFinderProfileHash(json.Number("1")); err == nil {
		t.Fatal("non-object native profile was accepted")
	}
}
