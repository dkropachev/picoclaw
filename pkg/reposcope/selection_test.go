package reposcope

import (
	"errors"
	"reflect"
	"slices"
	"testing"
)

func selectionCandidates(t *testing.T) []Candidate {
	t.Helper()
	files := []FileMetadata{
		regularFile("api/a.go", 9000, "package api"),
		regularFile("worker/b.go", 8000, "package worker"),
		regularFile("api/parser/c.go", 7000, "package parser"),
		regularFile("worker/queue/d.go", 6000, "package queue"),
		regularFile("root.go", 5000, "package root"),
		regularFile("other/tiny.go", 1000, "package other"),
		regularFile("tools/check.py", 500, "print('ok')"),
	}
	candidates, _, err := BuildCandidates(testInventory(files...), Scope{}, BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return candidates
}

func TestSelectDeterministicIsDiverseSubstantialAndStable(t *testing.T) {
	candidates := selectionCandidates(t)
	policy := SelectionPolicy{DefaultPerLanguage: 4, PerLanguage: map[Language]int{"python": 1}}
	result, err := SelectDeterministic(candidates, policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Selected) != 5 {
		t.Fatalf("selected %d candidates, want 5: %#v", len(result.Selected), result.Selected)
	}
	goRegions := make(map[string]bool)
	pythonCount := 0
	for _, candidate := range result.Selected {
		if candidate.Language == "go" {
			goRegions[candidate.Region] = true
			if candidate.Size < DefaultPreferredMinBytes {
				t.Fatalf("selected tiny Go file despite sufficient alternatives: %#v", candidate)
			}
		}
		if candidate.Language == "python" {
			pythonCount++
		}
	}
	if len(goRegions) < 3 || pythonCount != 1 {
		t.Fatalf("representation/diversity failed: regions=%#v python=%d", goRegions, pythonCount)
	}

	reversed := slices.Clone(candidates)
	slices.Reverse(reversed)
	again, err := SelectDeterministic(reversed, policy)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(result.Selected), ids(again.Selected)) ||
		!reflect.DeepEqual(result.FilledIDs, again.FilledIDs) {
		t.Fatalf("selection depends on candidate input order:\n%#v\n%#v", result, again)
	}
}

func TestDefaultQuotaHasHardMaximumAndRepresentsEveryLanguage(t *testing.T) {
	files := make([]FileMetadata, 0, 26)
	for index := range 25 {
		path := "src/file" + twoDigits(index) + ".go"
		files = append(files, regularFile(path, int64(5000+index), "package src"))
	}
	files = append(files, regularFile("scripts/tool.py", 100, "print('ok')"))
	candidates, _, err := BuildCandidates(testInventory(files...), Scope{}, BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := SelectDeterministic(candidates, SelectionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[Language]int)
	for _, candidate := range result.Selected {
		counts[candidate.Language]++
	}
	if counts["go"] != MaxPerLanguageQuota || counts["python"] != 1 {
		t.Fatalf("language counts = %#v", counts)
	}
}

func twoDigits(value int) string {
	return string(rune('a'+value/26)) + string(rune('a'+value%26))
}

func TestValidateAISelectionHonorsOpaqueChoicesAndFillsOmissions(t *testing.T) {
	candidates := selectionCandidates(t)
	var tinyGo, python Candidate
	for _, candidate := range candidates {
		if candidate.Path == "other/tiny.go" {
			tinyGo = candidate
		}
		if candidate.Language == "python" {
			python = candidate
		}
	}
	result, err := ValidateAISelection(
		candidates,
		AISelection{CandidateIDs: []string{tinyGo.ID, python.ID}},
		SelectionPolicy{DefaultPerLanguage: 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.AcceptedAIIDs, []string{tinyGo.ID, python.ID}) {
		t.Fatalf("accepted IDs = %#v", result.AcceptedAIIDs)
	}
	if len(result.FilledIDs) != 2 || len(result.Selected) != 4 {
		t.Fatalf("AI fill result = %#v", result)
	}
	if !slices.Contains(ids(result.Selected), tinyGo.ID) || !slices.Contains(ids(result.Selected), python.ID) {
		t.Fatalf("AI choices missing from result: %#v", result)
	}
}

func TestValidateAISelectionRejectsUntrustedOrInvalidInput(t *testing.T) {
	candidates := selectionCandidates(t)
	goIDs := make([]string, 0)
	for _, candidate := range candidates {
		if candidate.Language == "go" {
			goIDs = append(goIDs, candidate.ID)
		}
	}
	tests := []struct {
		name     string
		input    []Candidate
		proposal AISelection
		policy   SelectionPolicy
		want     error
	}{
		{
			"unknown ID",
			candidates,
			AISelection{CandidateIDs: []string{"api/a.go"}},
			SelectionPolicy{},
			ErrUnknownCandidate,
		},
		{
			"duplicate ID",
			candidates,
			AISelection{CandidateIDs: []string{goIDs[0], goIDs[0]}},
			SelectionPolicy{},
			ErrDuplicateCandidate,
		},
		{
			"quota exceeded",
			candidates,
			AISelection{CandidateIDs: goIDs[:2]},
			SelectionPolicy{DefaultPerLanguage: 1},
			ErrQuotaExceeded,
		},
		{
			"default quota high",
			candidates,
			AISelection{},
			SelectionPolicy{DefaultPerLanguage: MaxPerLanguageQuota + 1},
			ErrInvalidPolicy,
		},
		{
			"default quota negative",
			candidates,
			AISelection{},
			SelectionPolicy{DefaultPerLanguage: -1},
			ErrInvalidPolicy,
		},
		{
			"language quota zero",
			candidates,
			AISelection{},
			SelectionPolicy{PerLanguage: map[Language]int{"go": 0}},
			ErrInvalidPolicy,
		},
		{
			"language quota high",
			candidates,
			AISelection{},
			SelectionPolicy{PerLanguage: map[Language]int{"go": MaxPerLanguageQuota + 1}},
			ErrInvalidPolicy,
		},
		{
			"absent language",
			candidates,
			AISelection{},
			SelectionPolicy{PerLanguage: map[Language]int{"rust": 1}},
			ErrInvalidPolicy,
		},
		{
			"preferred bytes negative",
			candidates,
			AISelection{},
			SelectionPolicy{PreferredMinBytes: -1},
			ErrInvalidPolicy,
		},
		{
			"preferred bytes high",
			candidates,
			AISelection{},
			SelectionPolicy{PreferredMinBytes: AbsoluteMaxFileBytes + 1},
			ErrInvalidPolicy,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidateAISelection(test.input, test.proposal, test.policy)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	tampered := slices.Clone(candidates)
	tampered[0].Size++
	if _, err := SelectDeterministic(tampered, SelectionPolicy{}); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("tampered candidate error = %v", err)
	}
	duplicate := append(slices.Clone(candidates), candidates[0])
	if _, err := SelectDeterministic(duplicate, SelectionPolicy{}); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("duplicate candidate error = %v", err)
	}
	mixed := slices.Clone(candidates)
	mixed[1].CommitID = "other-commit"
	mixed[1].ID = makeCandidateID(mixed[1])
	if _, err := SelectDeterministic(mixed, SelectionPolicy{}); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("mixed snapshot error = %v", err)
	}
	if result, err := SelectDeterministic(nil, SelectionPolicy{}); err != nil || len(result.Selected) != 0 {
		t.Fatalf("empty selection = %#v, %v", result, err)
	}
}

func ids(candidates []Candidate) []string {
	result := make([]string, len(candidates))
	for index, candidate := range candidates {
		result[index] = candidate.ID
	}
	return result
}

func TestSelectionTieBreakersAndExhaustion(t *testing.T) {
	candidates := selectionCandidates(t)
	chosen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		chosen[candidate.ID] = struct{}{}
	}
	if _, ok := bestFillCandidate(candidates, chosen, nil, DefaultPreferredMinBytes); ok {
		t.Fatal("exhausted candidate set returned a fill")
	}

	base := candidates[0]
	other := base
	other.Path = "z.go"
	other.Module, other.Region, _ = DeriveLocation(other.Path)
	other.ID = makeCandidateID(other)
	if !fillLess(base, other, map[string]int{}, map[string]int{}, 1) {
		t.Fatal("path tie-breaker did not choose lexical path")
	}
	other = base
	other.ID = "cand_ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if !fillLess(base, other, map[string]int{}, map[string]int{}, 1) {
		t.Fatal("ID tie-breaker did not choose lexical ID")
	}
	left, right := base, base
	left.Module, right.Module = "less-used", "busy"
	if !fillLess(
		left,
		right,
		map[string]int{left.Region: 1},
		map[string]int{"less-used": 0, "busy": 2},
		1,
	) {
		t.Fatal("module diversity did not prefer the less-used module")
	}
}
