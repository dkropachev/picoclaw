package repoeval

import (
	"testing"
	"time"
)

func TestProgressValidatesAndClonesManagedChildActivity(t *testing.T) {
	now := time.Now().UTC()
	progress := Progress{
		Stage: ProgressCandidateExecution, Languages: map[string]LanguageProgress{},
		CurrentBatch: 1, TotalBatches: 3, CompletedCalls: 1, TotalCalls: 4,
		ActiveChildren: []ActiveChildProgress{{
			Index: 2, Label: "scope chunk 1", ModelAlias: "model-a", ScopeCount: 3, StartedAt: now,
		}},
	}
	if err := validateProgress(progress); err != nil {
		t.Fatal(err)
	}
	cloneProgress := func() Progress {
		clone := progress
		clone.ActiveChildren = append([]ActiveChildProgress(nil), progress.ActiveChildren...)
		return clone
	}
	evaluation := Evaluation{Progress: progress}
	clone := Clone(evaluation)
	clone.Progress.ActiveChildren[0].Label = "changed"
	if evaluation.Progress.ActiveChildren[0].Label != "scope chunk 1" {
		t.Fatal("progress active children were not cloned")
	}
	normalized, err := normalizeEvaluation(Evaluation{Progress: Progress{
		ActiveChildren: []ActiveChildProgress{{
			Label: " child ", ModelAlias: " model-a ", StartedAt: now.In(time.FixedZone("offset", 3600)),
		}},
	}})
	if err != nil || normalized.Progress.ActiveChildren[0].Label != "child" ||
		normalized.Progress.ActiveChildren[0].ModelAlias != "model-a" ||
		normalized.Progress.ActiveChildren[0].StartedAt.Location() != time.UTC {
		t.Fatalf("normalized active child=%#v err=%v", normalized.Progress.ActiveChildren, err)
	}

	invalid := cloneProgress()
	invalid.FailedCalls = 2
	if err := validateProgress(invalid); err == nil {
		t.Fatal("failed calls above completed calls were accepted")
	}
	invalid = cloneProgress()
	invalid.ActiveChildren = make([]ActiveChildProgress, MaxProgressActiveChildren+1)
	if err := validateProgress(invalid); err == nil {
		t.Fatal("unbounded active children were accepted")
	}
	invalid = cloneProgress()
	invalid.CompletedCalls = invalid.TotalCalls
	if err := validateProgress(invalid); err == nil {
		t.Fatal("completed and active calls above total were accepted")
	}
	invalid = cloneProgress()
	invalid.ActiveChildren[0].Index = invalid.TotalCalls + 1
	if err := validateProgress(invalid); err == nil {
		t.Fatal("active child index above total calls was accepted")
	}
	invalid = cloneProgress()
	invalid.ActiveChildren[0].StartedAt = now.In(time.FixedZone("offset", 3600))
	if err := validateProgress(invalid); err == nil {
		t.Fatal("non-UTC active child timestamp was accepted")
	}
	invalid = cloneProgress()
	invalid.ActiveChildren = append(invalid.ActiveChildren, invalid.ActiveChildren[0])
	if err := validateProgress(invalid); err == nil {
		t.Fatal("duplicate active child index was accepted")
	}
}
