package agent

import (
	"reflect"
	"testing"
)

func TestWorkflowManagedRepositoryAndLanguageSignalCoverage(t *testing.T) {
	repositories := map[string]struct{}{}
	workflowManagedCollectRepositorySignals(map[string]any{
		"repository": "owner/repo", "repo": "repo", "repo_root": "root",
		"repoRoot": "root-camel", "working_directory": "working",
		"workingDirectory": "working-camel", "workspace": "workspace",
	}, repositories)
	if len(repositories) != 7 {
		t.Fatalf("repository signals = %#v", repositories)
	}
	workflowManagedCollectRepositorySignals(map[string]any{}, repositories)

	cases := map[string]string{
		"README": "", "a.js": "javascript", "a.jsx": "javascript", "a.mjs": "javascript", "a.cjs": "javascript",
		"a.ts": "typescript", "a.tsx": "typescript", "a.mts": "typescript", "a.cts": "typescript",
		"a.py": "python", "a.go": "go", "a.rb": "ruby", "a.rs": "rust", "a.java": "java",
		"a.kt": "kotlin", "a.kts": "kotlin", "a.cs": "csharp", "a.cpp": "cpp", "a.cc": "cpp",
		"a.cxx": "cpp", "a.hpp": "cpp", "a.hh": "cpp", "a.hxx": "cpp", "a.c": "c", "a.h": "c",
		"a.md": "markdown", "a.mdx": "markdown", "a.yml": "yaml", "a.yaml": "yaml", "a.json": "json",
		"a.zig": "zig",
	}
	for path, want := range cases {
		if got := workflowManagedLanguageFromPath(path); got != want {
			t.Fatalf("language(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestWorkflowManagedSimilarityAndSliceCoverage(t *testing.T) {
	if workflowExactSimilarity("", "a") != 0 || workflowExactSimilarity("a", "a") != 1 ||
		workflowExactSimilarity("a", "b") != 0 ||
		workflowExactOrChangedSimilarity("", "a", 0.3) != 0 ||
		workflowExactOrChangedSimilarity("a", "a", 0.3) != 1 ||
		workflowExactOrChangedSimilarity("a", "b", 0.3) != 0.3 ||
		workflowCountSimilarity(2, 2) != 1 || workflowCountSimilarity(0, 2) != 0 ||
		workflowCountSimilarity(2, 4) != 0.5 || workflowSetSimilarity(nil, nil) != 1 ||
		workflowSetSimilarity(nil, []string{"a"}) != 0.4 ||
		workflowSetSimilarity([]string{" "}, []string{"a"}) != 0.4 ||
		workflowSetSimilarity([]string{"a", "b"}, []string{"b", "c", " "}) != 1.0/3.0 {
		t.Fatal("managed similarity helper mismatch")
	}

	if got := workflowManagedSortedSet(map[string]struct{}{"b": {}, "": {}, "a": {}}); !reflect.DeepEqual(
		got,
		[]string{"a", "b"},
	) {
		t.Fatalf("sorted set = %#v", got)
	}
	if stringSliceMapValue(map[string]any{}, "missing") != nil ||
		stringSliceMapValue(map[string]any{"value": nil}, "value") != nil ||
		!reflect.DeepEqual(stringSliceMapValue(map[string]any{"value": []string{"a"}}, "value"), []string{"a"}) ||
		!reflect.DeepEqual(
			stringSliceMapValue(map[string]any{"value": []any{" a ", nil, ""}}, "value"),
			[]string{"a"},
		) || stringSliceMapValue(map[string]any{"value": " "}, "value") != nil ||
		!reflect.DeepEqual(stringSliceMapValue(map[string]any{"value": 3}, "value"), []string{"3"}) {
		t.Fatal("string slice conversion mismatch")
	}
	if intSliceMapValue(map[string]any{}, "missing") != nil ||
		intSliceMapValue(map[string]any{"value": nil}, "value") != nil ||
		!reflect.DeepEqual(intSliceMapValue(map[string]any{"value": []int{1, 2}}, "value"), []int{1, 2}) ||
		!reflect.DeepEqual(intSliceMapValue(map[string]any{"value": []any{1, "2"}}, "value"), []int{1, 2}) ||
		!reflect.DeepEqual(intSliceMapValue(map[string]any{"value": "3"}, "value"), []int{3}) {
		t.Fatal("integer slice conversion mismatch")
	}
}
