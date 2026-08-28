package workflows

import (
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
