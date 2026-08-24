package repoaudit

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

var automationTestNow = time.Date(2026, 8, 20, 15, 0, 0, 0, time.UTC)

func TestAutomationLoadRemovesLegacyPriceResolutionMetadata(t *testing.T) {
	store := NewStore(t.TempDir())
	store.now = func() time.Time { return automationTestNow }
	automation := createAutomationForTest(
		t,
		store,
		"rra_11111111111111111111111111111111",
		"Legacy price metadata",
	)
	path := store.automationPath(automation.ID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if unmarshalErr := json.Unmarshal(data, &raw); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	prices := raw["model_prices"].(map[string]any)
	price := prices["review-a"].(map[string]any)
	price["subscription"] = true
	price["equivalent_model"] = "metered-review"
	data, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	loaded, found, err := store.GetAutomation(context.Background(), automation.ID)
	if err != nil || !found {
		t.Fatalf("GetAutomation() found=%v error=%v", found, err)
	}
	if loaded.ModelPrices["review-a"].InputPricePer1M != 1 {
		t.Fatalf("numeric price snapshot changed: %#v", loaded.ModelPrices)
	}
	rewritten, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rewritten), `"subscription"`) ||
		strings.Contains(string(rewritten), `"equivalent_model"`) {
		t.Fatalf("legacy price metadata was not removed: %s", rewritten)
	}
}

func TestAutomationStoreCreatesConfigurationBeforeRepositoryReviewState(t *testing.T) {
	workspace := t.TempDir()
	store := NewStore(workspace)
	store.now = func() time.Time { return automationTestNow }
	automation, err := store.CreateAutomation(context.Background(), RepositoryReviewAutomation{
		Name:           "  Nightly correctness review  ",
		Repository:     "  owner/repo  ",
		Ref:            " main ",
		Target:         "  ",
		ReviewFocus:    "  Find concrete concurrency bugs.  ",
		ReviewerModels: []string{" review-expensive ", "review-cheap"},
		CompareModels:  true,
		ModelPrices: map[string]RepositoryReviewModelPrice{
			" review-expensive ": {InputPricePer1M: 5, OutputPricePer1M: 15},
			"review-cheap": {
				InputPricePer1M: 0.1, OutputPricePer1M: 0.4,
			},
		},
		AutoContinue: true,
		BudgetPolicy: RepositoryReviewBudgetPolicy{
			GuardExpression: " spent.tokens.total < 200000 and spend.total.usd < 8.5 ",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(automation.ID, "rra_") || !validAutomationID(automation.ID) {
		t.Fatalf("generated ID = %q", automation.ID)
	}
	if automation.SchemaVersion != 1 || automation.Version != 1 ||
		automation.Name != "Nightly correctness review" || automation.Repository != "owner/repo" ||
		automation.Ref != "main" || automation.Target != "all" ||
		automation.ReviewFocus != "Find concrete concurrency bugs." ||
		!reflect.DeepEqual(automation.ReviewerModels, []string{"review-expensive", "review-cheap"}) {
		t.Fatalf("normalized automation = %#v", automation)
	}
	if automation.Status != RepositoryReviewAutomationIdle || automation.ActiveRunID != "" ||
		len(automation.RunIDs) != 0 || automation.Progress != (RepositoryReviewProgress{}) {
		t.Fatalf("new automation runtime = %#v", automation)
	}
	if automation.MaxFilesPerRun != 24 || automation.MaxContentBytes != 512<<10 ||
		automation.MaxParallelChildren != 8 || automation.EstimatedOutputTokens != 1_800 {
		t.Fatalf("new automation defaults = %#v", automation)
	}
	if automation.BudgetPolicy.GuardExpression != "spent.tokens.total < 200000 and spend.total.usd < 8.5" ||
		automation.ModelPrices["review-cheap"].InputPricePer1M != 0.1 {
		t.Fatalf("normalized budget/prices = %#v %#v", automation.BudgetPolicy, automation.ModelPrices)
	}
	if !automation.AutoContinue {
		t.Fatalf("continuation controls were not persisted independently: %#v", automation)
	}
	state, found, err := store.Get("owner/repo")
	if err != nil || found || state.Version != 0 {
		t.Fatalf("repository state exists before a review: found=%v state=%#v err=%v", found, state, err)
	}
	statePath := filepath.Join(store.root, automationFilename(automation.ID))
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("automation mode = %o, want 600", info.Mode().Perm())
	}
	loaded, loadedFound, err := NewStore(workspace).GetAutomation(context.Background(), automation.ID)
	if err != nil || !loadedFound || !reflect.DeepEqual(loaded, automation) {
		t.Fatalf(
			"reopened automation = %#v\ncreated automation = %#v\nfound=%v err=%v",
			loaded,
			automation,
			loadedFound,
			err,
		)
	}

	wire, err := json.Marshal(automation)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(wire)
	for _, field := range []string{`"budget":`, `"account_limits":`, `"prompt_tokens":`, `"completion_tokens":`, `"cached_tokens"`} {
		if !strings.Contains(encoded, field) && field != `"cached_tokens"` {
			t.Errorf("automation JSON %s is missing %s", encoded, field)
		}
	}
	if strings.Contains(encoded, `"budget_policy"`) || strings.Contains(encoded, `"account_limit_snapshots"`) {
		t.Errorf("automation JSON uses stale wire names: %s", encoded)
	}
}

func TestAutomationStoreCASAndMutationFailureAreAtomic(t *testing.T) {
	store := newAutomationTestStore(t)
	created := createAutomationForTest(t, store, "rra_cas", "CAS")
	store.now = func() time.Time { return automationTestNow.Add(time.Minute) }
	updated, updateErr := store.UpdateAutomation(
		context.Background(),
		created.ID,
		created.Version,
		func(value *RepositoryReviewAutomation) error {
			value.Name = " Updated "
			value.AutoContinue = true
			return nil
		},
	)
	if updateErr != nil {
		t.Fatal(updateErr)
	}
	if updated.Version != 2 || updated.Name != "Updated" || !updated.AutoContinue ||
		!updated.UpdatedAt.Equal(automationTestNow.Add(time.Minute)) {
		t.Fatalf("updated automation = %#v", updated)
	}
	called := false
	if _, err := store.UpdateAutomation(
		context.Background(),
		created.ID,
		created.Version,
		func(*RepositoryReviewAutomation) error {
			called = true
			return nil
		},
	); !errors.Is(err, ErrConflict) ||
		called {
		t.Fatalf("stale update error=%v callback-called=%v", err, called)
	}
	mutationErr := errors.New("mutation failed")
	if _, err := store.UpdateAutomation(
		context.Background(),
		created.ID,
		updated.Version,
		func(value *RepositoryReviewAutomation) error {
			value.Name = "must not persist"
			return mutationErr
		},
	); !errors.Is(
		err,
		mutationErr,
	) {
		t.Fatalf("mutation error = %v", err)
	}
	if _, err := store.UpdateAutomation(
		context.Background(),
		created.ID,
		updated.Version,
		func(value *RepositoryReviewAutomation) error {
			value.ID = "rra_changed"
			return nil
		},
	); !errors.Is(
		err,
		ErrInvalidAutomation,
	) {
		t.Fatalf("immutable-field update error = %v", err)
	}
	loaded, found, err := store.GetAutomation(context.Background(), created.ID)
	if err != nil || !found || loaded.Version != updated.Version || loaded.Name != "Updated" {
		t.Fatalf("state after rejected mutations = %#v found=%v err=%v", loaded, found, err)
	}
}

func TestAutomationStorePersistsRuntimeProgressBudgetsAndModelComparison(t *testing.T) {
	workspace := t.TempDir()
	store := NewStore(workspace)
	store.now = func() time.Time { return automationTestNow }
	created := createAutomationForTest(t, store, "rra_runtime", "Runtime")
	startedAt := automationTestNow.Add(time.Minute)
	checkedAt := startedAt.Add(20 * time.Second)
	remaining := 7.5
	store.now = func() time.Time { return startedAt }
	running, err := store.UpdateAutomation(
		context.Background(),
		created.ID,
		created.Version,
		func(value *RepositoryReviewAutomation) error {
			value.Status = RepositoryReviewAutomationRunning
			value.ActiveRunID = "run-1"
			value.RunIDs = []string{"run-1"}
			value.StartedAt = startedAt
			value.Usage = RepositoryReviewTokenUsage{
				PromptTokens: 900, CompletionTokens: 100, CachedTokens: 200,
			}
			value.EstimatedCostUSD = 0.42
			value.Progress = RepositoryReviewProgress{
				Stage: "reviewing", CompletedBatches: 1, TotalBatches: 4,
				ReviewedFiles: 6, RemainingFiles: 18, UnsupportedFiles: 1, Findings: 2,
			}
			value.ModelStats = map[string]RepositoryReviewModelStats{
				"review-a": {
					Tokens: RepositoryReviewTokenUsage{
						PromptTokens:     500,
						CompletionTokens: 50,
						CachedTokens:     100,
					},
					EstimatedCostUSD: 0.35,
					Requests:         2,
					Failures:         0,
					Findings:         2,
					ReviewedFiles:    3,
					LatencyMillis:    1_250,
				},
			}
			value.AccountLimitSnapshots = []RepositoryReviewAccountLimitSnapshot{
				{AccountID: "account-a", Window: " Weekly ", RemainingPercent: &remaining, CheckedAt: checkedAt},
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if running.Usage.TotalTokens != 1_000 || running.ModelStats["review-a"].Tokens.TotalTokens != 550 {
		t.Fatalf("normalized runtime token totals = %#v %#v", running.Usage, running.ModelStats)
	}
	loaded, found, err := NewStore(workspace).GetAutomation(context.Background(), created.ID)
	if err != nil || !found || !reflect.DeepEqual(loaded, running) {
		t.Fatalf("reopened runtime = %#v found=%v err=%v", loaded, found, err)
	}

	pausedAt := startedAt.Add(2 * time.Minute)
	store.now = func() time.Time { return pausedAt }
	paused, err := store.UpdateAutomation(
		context.Background(),
		created.ID,
		running.Version,
		func(value *RepositoryReviewAutomation) error {
			value.Status = RepositoryReviewAutomationPaused
			value.ActiveRunID = ""
			value.PauseReason = RepositoryReviewPauseGuardExpression
			value.PauseDetail = " task admission guard is false "
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if paused.Status != RepositoryReviewAutomationPaused || paused.PauseReason != RepositoryReviewPauseGuardExpression ||
		paused.PauseDetail != "task admission guard is false" || paused.ActiveRunID != "" {
		t.Fatalf("paused automation = %#v", paused)
	}
}

func TestAutomationStoreBoundsRunHistoryToNewestEntries(t *testing.T) {
	store := newAutomationTestStore(t)
	created := createAutomationForTest(t, store, "rra_history", "History")
	runs := make([]string, maxAutomationRunIDs+2)
	for index := range runs {
		runs[index] = "run-" + automationTestIndex(index)
	}
	updated, err := store.UpdateAutomation(
		context.Background(),
		created.ID,
		created.Version,
		func(value *RepositoryReviewAutomation) error {
			value.RunIDs = runs
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.RunIDs) != maxAutomationRunIDs || updated.RunIDs[0] != runs[2] ||
		updated.RunIDs[len(updated.RunIDs)-1] != runs[len(runs)-1] {
		t.Fatalf(
			"bounded run history length=%d first=%q last=%q",
			len(updated.RunIDs),
			updated.RunIDs[0],
			updated.RunIDs[len(updated.RunIDs)-1],
		)
	}
}

func TestAutomationStoreListsMostRecentlyUpdatedFirst(t *testing.T) {
	store := newAutomationTestStore(t)
	first := createAutomationForTest(t, store, "rra_first", "First")
	store.now = func() time.Time { return automationTestNow.Add(time.Minute) }
	second := createAutomationForTest(t, store, "rra_second", "Second")
	store.now = func() time.Time { return automationTestNow.Add(2 * time.Minute) }
	first, err := store.UpdateAutomation(
		context.Background(),
		first.ID,
		first.Version,
		func(value *RepositoryReviewAutomation) error {
			value.ReviewFocus = "new focus"
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := store.ListAutomations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].ID != first.ID || listed[1].ID != second.ID {
		t.Fatalf("list order = %#v", listed)
	}
}

func TestAutomationStoreDeleteUsesCAS(t *testing.T) {
	store := newAutomationTestStore(t)
	created := createAutomationForTest(t, store, "rra_delete", "Delete")
	if err := store.DeleteAutomation(
		context.Background(),
		created.ID,
		created.Version+1,
	); !errors.Is(
		err,
		ErrConflict,
	) {
		t.Fatalf("stale delete error = %v", err)
	}
	if err := store.DeleteAutomation(context.Background(), created.ID, created.Version); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetAutomation(context.Background(), created.ID); err != nil || found {
		t.Fatalf("deleted automation found=%v err=%v", found, err)
	}
	if err := store.DeleteAutomation(
		context.Background(),
		created.ID,
		created.Version,
	); !errors.Is(
		err,
		os.ErrNotExist,
	) {
		t.Fatalf("second delete error = %v", err)
	}
}

func TestAutomationStoreRejectsInvalidPolicyRuntimeAndPricing(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RepositoryReviewAutomation)
	}{
		{
			name:   "duplicate reviewer",
			mutate: func(value *RepositoryReviewAutomation) { value.ReviewerModels = []string{"review-a", " review-a "} },
		},
		{name: "compare one reviewer", mutate: func(value *RepositoryReviewAutomation) {
			value.ReviewerModels = []string{"review-a"}
			value.CompareModels = true
		}},
		{name: "negative max files", mutate: func(value *RepositoryReviewAutomation) { value.MaxFilesPerRun = -1 }},
		{
			name:   "oversized content",
			mutate: func(value *RepositoryReviewAutomation) { value.MaxContentBytes = 512<<10 + 1 },
		},
		{name: "parallelism", mutate: func(value *RepositoryReviewAutomation) { value.MaxParallelChildren = 65 }},
		{
			name:   "estimated output",
			mutate: func(value *RepositoryReviewAutomation) { value.EstimatedOutputTokens = 65_537 },
		},
		{name: "guard unknown field", mutate: func(value *RepositoryReviewAutomation) {
			value.BudgetPolicy.GuardExpression = "account.secret > 0"
		}},
		{name: "guard wildcard", mutate: func(value *RepositoryReviewAutomation) {
			value.BudgetPolicy.GuardExpression = "spent.tokens.* > 0"
		}},
		{name: "guard malformed", mutate: func(value *RepositoryReviewAutomation) {
			value.BudgetPolicy.GuardExpression = "spent.tokens.total <"
		}},
		{name: "invalid status", mutate: func(value *RepositoryReviewAutomation) { value.Status = "waiting" }},
		{
			name:   "paused without reason",
			mutate: func(value *RepositoryReviewAutomation) { value.Status = RepositoryReviewAutomationPaused },
		},
		{
			name:   "idle with reason",
			mutate: func(value *RepositoryReviewAutomation) { value.PauseReason = RepositoryReviewPauseManual },
		},
		{
			name:   "running without run",
			mutate: func(value *RepositoryReviewAutomation) { value.Status = RepositoryReviewAutomationRunning },
		},
		{
			name:   "failed without run failed reason",
			mutate: func(value *RepositoryReviewAutomation) { value.Status = RepositoryReviewAutomationFailed },
		},
		{name: "usage total", mutate: func(value *RepositoryReviewAutomation) {
			value.Usage = RepositoryReviewTokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 12}
		}},
		{name: "progress", mutate: func(value *RepositoryReviewAutomation) {
			value.Progress = RepositoryReviewProgress{CompletedBatches: 2, TotalBatches: 1}
		}},
		{name: "unknown price alias", mutate: func(value *RepositoryReviewAutomation) {
			value.ModelPrices = map[string]RepositoryReviewModelPrice{"not-selected": {InputPricePer1M: 1}}
		}},
		{name: "invalid price", mutate: func(value *RepositoryReviewAutomation) {
			value.ModelPrices = map[string]RepositoryReviewModelPrice{"review-a": {InputPricePer1M: math.NaN()}}
		}},
		{name: "invalid snapshot percent", mutate: func(value *RepositoryReviewAutomation) {
			remaining := -0.1
			value.AccountLimitSnapshots = []RepositoryReviewAccountLimitSnapshot{
				{AccountID: "account-a", Window: "daily", RemainingPercent: &remaining, CheckedAt: automationTestNow},
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newAutomationTestStore(t)
			candidate := validAutomationForTest("", test.name)
			test.mutate(&candidate)
			if _, err := store.CreateAutomation(
				context.Background(),
				candidate,
			); !errors.Is(
				err,
				ErrInvalidAutomation,
			) {
				t.Fatalf("CreateAutomation() error = %v, want ErrInvalidAutomation", err)
			}
		})
	}
	store := newAutomationTestStore(t)
	invalidID := validAutomationForTest("rra_../../escape", "invalid ID")
	if _, err := store.CreateAutomation(context.Background(), invalidID); !errors.Is(err, ErrInvalidAutomation) {
		t.Fatalf("unsafe ID error = %v", err)
	}
}

func TestAutomationStoreRejectsSymlinkAndNonRegularStorage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires platform-specific privileges")
	}
	t.Run("root symlink", func(t *testing.T) {
		workspace := t.TempDir()
		store := NewStore(workspace)
		target := t.TempDir()
		if err := os.Symlink(target, store.root); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ListAutomations(context.Background()); err == nil {
			t.Fatal("ListAutomations() accepted a symlink root")
		}
	})
	t.Run("automation symlink", func(t *testing.T) {
		store := newAutomationTestStore(t)
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target.json")
		if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		id := "rra_symlink"
		if err := os.Symlink(target, store.automationPath(id)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.GetAutomation(context.Background(), id); err == nil {
			t.Fatal("GetAutomation() accepted a symlink file")
		}
		if _, err := store.ListAutomations(context.Background()); err == nil {
			t.Fatal("ListAutomations() accepted a symlink file")
		}
	})
	t.Run("automation directory", func(t *testing.T) {
		store := newAutomationTestStore(t)
		if err := os.MkdirAll(store.automationPath("rra_directory"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.GetAutomation(context.Background(), "rra_directory"); err == nil {
			t.Fatal("GetAutomation() accepted a directory")
		}
		if _, err := store.ListAutomations(context.Background()); err == nil {
			t.Fatal("ListAutomations() accepted a directory")
		}
	})
	t.Run("lock symlink", func(t *testing.T) {
		store := newAutomationTestStore(t)
		target := filepath.Join(t.TempDir(), "lock")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, store.root+".lock"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.GetAutomation(context.Background(), "rra_lock"); err == nil {
			t.Fatal("GetAutomation() accepted a symlink lock")
		}
	})
}

func newAutomationTestStore(t *testing.T) Store {
	t.Helper()
	store := NewStore(t.TempDir())
	store.now = func() time.Time { return automationTestNow }
	return store
}

func createAutomationForTest(t *testing.T, store Store, id, name string) RepositoryReviewAutomation {
	t.Helper()
	automation, err := store.CreateAutomation(context.Background(), validAutomationForTest(id, name))
	if err != nil {
		t.Fatal(err)
	}
	return automation
}

func validAutomationForTest(id, name string) RepositoryReviewAutomation {
	return RepositoryReviewAutomation{
		ID: id, Name: name, Repository: "owner/repo", Ref: "main", Target: "all",
		ReviewFocus: "Find concrete bugs.", ReviewerModels: []string{"review-a", "review-b"},
		CompareModels: true, MaxFilesPerRun: 12, MaxContentBytes: 64 << 10,
		MaxParallelChildren: 1, EstimatedOutputTokens: 1_500,
		BudgetPolicy: RepositoryReviewBudgetPolicy{
			GuardExpression: "account.limits.any.remaining_percent >= 10",
		},
		ModelPrices: map[string]RepositoryReviewModelPrice{
			"review-a": {InputPricePer1M: 1, OutputPricePer1M: 2},
			"review-b": {InputPricePer1M: 0.1, OutputPricePer1M: 0.2},
		},
	}
}

func automationTestIndex(index int) string {
	const digits = "0123456789"
	if index == 0 {
		return "0"
	}
	value := ""
	for index > 0 {
		value = string(digits[index%10]) + value
		index /= 10
	}
	return value
}
